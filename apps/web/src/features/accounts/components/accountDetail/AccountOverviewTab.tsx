import { useTranslation } from 'react-i18next';
import {
  IconChartLine,
  IconDatabaseZap,
  IconKey,
  IconShield,
  IconTriangleAlert,
} from '@/components/ui/icons';
import {
  DrawerEmptyBox,
  DrawerScopePill,
  DrawerSectionCard,
} from '@/components/common/WorkspaceDrawer';
import { DrawerRecentStatusCard } from '@/components/common/WorkspaceDrawer/DrawerRecentStatusCard';
import type {
  AccountDetailField,
  AccountDetailOverviewTargetTab,
  AccountDetailViewModel,
} from '@/features/accounts/model/accountDetailViewModel';
import type { AccountListHealthStatusKey } from '@/features/accounts/model/accountListPresentation';
import {
  formatMoney,
  formatPercent,
  formatQuotaResetTooltipParams,
  formatTimestamp,
  formatTimestampTitle,
} from '@/features/accounts/model/accountsPagePresentation';
import { formatCompactNumber } from '@/utils/usage';
import {
  MetricSummaryGrid,
  type MetricSummaryCardProps,
  type MetricSummaryCardTone,
} from '@/components/common/MetricSummaryCard';
import { statusBarDataFromRecentRequests } from '@/utils/recentRequests';
import { AccountDetailFieldList, AccountDetailFieldValue } from './AccountDetailFieldList';
import styles from '@/features/accounts/AccountsPage.module.scss';

interface AccountOverviewTabProps {
  detailView: AccountDetailViewModel;
  getHealthStatusClass: (status: AccountListHealthStatusKey) => string;
  onSelectTab: (tab: AccountDetailOverviewTargetTab) => void;
}


type ActivityCardPresentation = Pick<MetricSummaryCardProps, 'accent' | 'icon' | 'variant'>;

const activityCardPresentations: Record<string, ActivityCardPresentation> = {
  requests: { accent: 'blue', icon: 'calls' },
  successRate: { accent: 'green', icon: 'success' },
  failureCalls: { accent: 'red', icon: 'failure' },
  cost: { accent: 'amber', icon: 'cost' },
  tokens: { accent: 'teal', icon: 'tokens', variant: 'secondary' },
  inputTokens: { accent: 'cyan', icon: 'input', variant: 'secondary' },
  outputTokens: { accent: 'blue', icon: 'output', variant: 'secondary' },
  cachedTokens: { accent: 'teal', icon: 'cache', variant: 'secondary' },
  lastSeenMs: { accent: 'blue', icon: 'trend' },
  successCalls: { accent: 'green', icon: 'success' },
};

const formatActivityMetricValue = (metric: AccountDetailField, locale: string): string => {
  if (metric.value === null || metric.value === '') return '-';
  if (metric.valueKind === 'percent') {
    return typeof metric.value === 'number'
      ? formatPercent(metric.value, metric.key === 'successRate' ? 1 : 0)
      : String(metric.value);
  }
  if (metric.valueKind === 'money') {
    return typeof metric.value === 'number' ? formatMoney(metric.value) : String(metric.value);
  }
  if (metric.valueKind === 'timestamp') {
    return typeof metric.value === 'number' ? formatTimestamp(metric.value, locale) : '-';
  }
  if (metric.valueKind === 'number') {
    return typeof metric.value === 'number'
      ? formatCompactNumber(metric.value)
      : String(metric.value);
  }
  return String(metric.value);
};

const getActivityMetricTone = (metric: AccountDetailField): MetricSummaryCardTone | undefined => {
  if (metric.key === 'successRate' && typeof metric.value === 'number') {
    return metric.value >= 95 ? 'good' : metric.value >= 85 ? 'warn' : 'bad';
  }
  if (metric.key === 'failureCalls' && typeof metric.value === 'number') {
    return metric.value > 0 ? 'bad' : 'good';
  }
  return undefined;
};

export function AccountOverviewTab({ detailView, getHealthStatusClass }: AccountOverviewTabProps) {
  const { t, i18n } = useTranslation();
  const { decision, capacity, credential, recentStatus, activity, attention } = detailView.overview;
  const recentStatusData = statusBarDataFromRecentRequests(recentStatus.recentRequests);
  const hasRecentRequests = recentStatusData.totalSuccess + recentStatusData.totalFailure > 0;
  const hasStatusMessage = Boolean(recentStatus.statusMessage);
  const activityScopeLabel =
    activity.scope === 'monitoring_7d'
      ? t('accounts.detail_overview_activity_scope_7d', { days: activity.scopeDays ?? 7 })
      : t('accounts.detail_overview_activity_scope_recent');
  const healthTooltipParams = formatQuotaResetTooltipParams(
    detailView.health.tooltipParams,
    detailView.health.resetAtMs,
    i18n.language,
    detailView.quota.cooldown?.recoverAtMs
  );
  const activityCards: MetricSummaryCardProps[] = activity.metrics.map((metric) => {
    const presentation =
      activityCardPresentations[metric.key] ?? activityCardPresentations.lastSeenMs;
    const label = t(metric.labelKey, { defaultValue: metric.labelKey });
    return {
      ...presentation,
      dataAttributes: {
        'data-overview-metric-key': metric.key,
        'data-overview-metric-kind': metric.valueKind ?? 'text',
      },
      fullLabel: label,
      label,
      meta: t(activity.sourceLabelKey),
      tone: getActivityMetricTone(metric),
      value: formatActivityMetricValue(metric, i18n.language),
      valueTitle:
        metric.valueKind === 'timestamp' && typeof metric.value === 'number'
          ? formatTimestampTitle(metric.value, i18n.language)
          : undefined,
    };
  });

  return (
    <div className={styles.overviewStack}>
      <DrawerSectionCard
        as="section"
        icon={<IconShield size={15} />}
        title={t('accounts.detail_overview_decision_title')}
        headingLevel="h3"
        metaActions={
          <span
            className={`${styles.badge} ${getHealthStatusClass(decision.status)}`}
            title={t(detailView.health.tooltipKey, healthTooltipParams)}
          >
            {t(decision.labelKey)}
          </span>
        }
        className={styles.overviewDecisionCard}
        dataAttributes={{
          'data-overview-section': 'decision',
          'data-overview-health': decision.status,
        }}
      >
        <p className={styles.overviewDecisionReason}>
          {t(decision.reasonKey, decision.reasonParams)}
        </p>
        <div className={styles.overviewEvidenceRow}>
          <div>
            <span>{t('accounts.detail_overview_decision_basis')}</span>
            <strong>{t(decision.basisLabelKey)}</strong>
          </div>
          <div>
            <span>{t('accounts.detail_overview_recent_observation')}</span>
            <strong>
              {decision.observedAtMs ? (
                <AccountDetailFieldValue
                  field={{
                    key: 'overviewObservedAt',
                    labelKey: 'accounts.detail_overview_recent_observation',
                    value: decision.observedAtMs,
                    valueKind: 'timestamp',
                  }}
                />
              ) : (
                t('accounts.detail_overview_observation_missing')
              )}
            </strong>
          </div>
        </div>
      </DrawerSectionCard>

      <DrawerRecentStatusCard
        title={t('accounts.detail_overview_recent_status_title')}
        headingLevel="h3"
        scopeLabel={t('accounts.detail_overview_recent_status_scope')}
        statusData={recentStatusData}
        statusBarStyles={styles}
        successLabel={t('accounts.detail_overview_recent_status_success')}
        failureLabel={t('accounts.detail_overview_recent_status_failure')}
        timelineLabel={t('accounts.detail_overview_recent_status_timeline')}
        timelineHint={t('accounts.detail_overview_recent_status_timeline_hint')}
        emptyMessage={t('accounts.detail_overview_recent_status_empty')}
        failureBannerTitle={
          hasStatusMessage ? t('accounts.detail_overview_recent_status_message') : undefined
        }
        failureBannerMessage={hasStatusMessage ? recentStatus.statusMessage : undefined}
        dataAttributes={{
          'data-overview-section': 'recent-status',
          'data-overview-recent-status-empty': !hasRecentRequests,
        }}
        timelineDataAttributes={{
          'data-overview-recent-status-bar': 'true',
        }}
        emptyDataAttributes={{
          'data-overview-recent-status-empty-message': 'true',
        }}
        messageDataAttributes={{
          'data-overview-recent-status-message': 'true',
        }}
      />

      <div className={styles.overviewCardGrid}>
        <DrawerSectionCard
          variant="default"
          icon={<IconDatabaseZap size={15} />}
          title={t('accounts.detail_overview_capacity_title')}
          headingLevel="h3"
          metaActions={<DrawerScopePill>{t(capacity.statusLabelKey)}</DrawerScopePill>}
          dataAttributes={{ 'data-overview-section': 'capacity' }}
        >
          <div className={styles.overviewPrimaryRow}>
            <strong className={styles.overviewPrimaryValue}>
              {capacity.kind === 'group_availability'
                ? t('accounts.detail_overview_capacity_group_count', {
                    available: capacity.availableGroupCount ?? 0,
                    total: capacity.totalGroupCount ?? 0,
                  })
                : capacity.remainingPercent === null
                  ? t('accounts.detail_overview_capacity_missing')
                  : formatPercent(capacity.remainingPercent)}
            </strong>
          </div>
          <p className={styles.overviewCardDescription}>{t(capacity.descriptionKey)}</p>
          {capacity.fields.length > 0 ? <AccountDetailFieldList fields={capacity.fields} /> : null}
        </DrawerSectionCard>

        <DrawerSectionCard
          variant="default"
          icon={<IconKey size={15} />}
          title={t('accounts.detail_overview_credential_title')}
          headingLevel="h3"
          metaActions={<DrawerScopePill>{t(credential.sourceLabelKey)}</DrawerScopePill>}
          dataAttributes={{ 'data-overview-section': 'credential' }}
        >
          <div className={styles.overviewCredentialState}>
            <strong>{t(credential.statusLabelKey)}</strong>
          </div>
          {credential.fields.length > 0 ? (
            <AccountDetailFieldList fields={credential.fields} />
          ) : null}
        </DrawerSectionCard>
      </div>

      <DrawerSectionCard
        variant="default"
        icon={<IconChartLine size={15} />}
        title={t('accounts.detail_overview_activity_title')}
        headingLevel="h3"
        metaActions={
          <DrawerScopePill fallback={activity.scope === 'recent_snapshot'}>
            {activityScopeLabel}
          </DrawerScopePill>
        }
        dataAttributes={{
          'data-overview-section': 'activity',
          'data-overview-activity-scope': activity.scope,
        }}
      >
        {activity.hasActivity ? (
          <MetricSummaryGrid
            cards={activityCards}
            density="compact"
            columns={Math.min(activityCards.length, 5)}
          />
        ) : (
          <DrawerEmptyBox>{t(activity.emptyStateKey)}</DrawerEmptyBox>
        )}
      </DrawerSectionCard>

      {attention ? (
        <section
          className={styles.overviewAttentionCard}
          data-overview-section="attention"
          data-overview-attention-priority={attention.priority}
        >
          <span className={styles.overviewAttentionIcon} aria-hidden="true">
            <IconTriangleAlert size={19} />
          </span>
          <div className={styles.overviewAttentionBody}>
            <h3 className={styles.overviewAttentionHeading}>
              {t('accounts.detail_overview_attention_heading', {
                action: t(attention.actionLabelKey),
              })}
            </h3>
            <p>{t(attention.reasonKey, attention.reasonParams)}</p>
          </div>
        </section>
      ) : null}
    </div>
  );
}
