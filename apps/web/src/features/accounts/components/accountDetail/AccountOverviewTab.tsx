import { useTranslation } from 'react-i18next';
import {
  IconChartLine,
  IconDatabaseZap,
  IconKey,
  IconShield,
  IconTriangleAlert,
} from '@/components/ui/icons';
import { ProviderStatusBar } from '@/components/providers/ProviderStatusBar';
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
import { UsageSummaryGrid } from '@/features/usage-analytics/components/UsageSummaryCards';
import type {
  UsageSummaryCard,
  UsageSummaryCardTone,
} from '@/features/usage-analytics/usageAnalyticsPresentation';
import { statusBarDataFromRecentRequests } from '@/utils/recentRequests';
import { AccountDetailFieldValue } from './AccountDetailFieldList';
import styles from '@/features/accounts/AccountsPage.module.scss';

interface AccountOverviewTabProps {
  detailView: AccountDetailViewModel;
  getHealthStatusClass: (status: AccountListHealthStatusKey) => string;
  onSelectTab: (tab: AccountDetailOverviewTargetTab) => void;
}

function OverviewFieldGrid({ fields }: { fields: AccountDetailField[] }) {
  const { t } = useTranslation();
  if (fields.length === 0) return null;
  return (
    <dl className={styles.overviewFieldGrid}>
      {fields.map((field) => (
        <div key={field.key}>
          <dt>{t(field.labelKey, { defaultValue: field.labelKey })}</dt>
          <dd>
            <AccountDetailFieldValue field={field} />
          </dd>
        </div>
      ))}
    </dl>
  );
}

type ActivityCardPresentation = Pick<UsageSummaryCard, 'accent' | 'icon' | 'variant'>;

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

const getActivityMetricTone = (metric: AccountDetailField): UsageSummaryCardTone | undefined => {
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
  const activityCards: UsageSummaryCard[] = activity.metrics.map((metric) => {
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
      <section
        className={styles.overviewDecisionCard}
        data-overview-section="decision"
        data-overview-health={decision.status}
      >
        <div className={styles.overviewCardHeader}>
          <div className={styles.overviewSectionHeading}>
            <span className={styles.overviewSectionIcon} aria-hidden="true">
              <IconShield size={19} />
            </span>
            <h3>{t('accounts.detail_overview_decision_title')}</h3>
          </div>
          <span
            className={`${styles.badge} ${getHealthStatusClass(decision.status)}`}
            title={t(detailView.health.tooltipKey, healthTooltipParams)}
          >
            {t(decision.labelKey)}
          </span>
        </div>
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
      </section>

      <section
        className={styles.overviewRecentStatusCard}
        data-overview-section="recent-status"
        data-overview-recent-status-empty={!hasRecentRequests}
      >
        <div className={styles.overviewCardHeader}>
          <div className={styles.overviewSectionHeading}>
            <span className={styles.overviewSectionIcon} aria-hidden="true">
              <IconChartLine size={18} />
            </span>
            <h3>{t('accounts.detail_overview_recent_status_title')}</h3>
          </div>
          <span className={styles.overviewScopePill}>
            {t('accounts.detail_overview_recent_status_scope')}
          </span>
        </div>

        <div className={styles.overviewRecentStatusLayout}>
          <div className={styles.overviewRecentStatusStats}>
            <div
              className={`${styles.overviewRecentStatusMetric} ${styles.overviewRecentStatusMetricSuccess}`}
            >
              <span>{t('accounts.detail_overview_recent_status_success')}</span>
              <strong>{recentStatusData.totalSuccess}</strong>
            </div>
            <div
              className={`${styles.overviewRecentStatusMetric} ${styles.overviewRecentStatusMetricFailure}`}
            >
              <span>{t('accounts.detail_overview_recent_status_failure')}</span>
              <strong>{recentStatusData.totalFailure}</strong>
            </div>
          </div>

          <div
            className={styles.overviewRecentStatusTimeline}
            data-overview-recent-status-bar="true"
          >
            <div className={styles.overviewRecentStatusTimelineHeader}>
              <span>{t('accounts.detail_overview_recent_status_timeline')}</span>
              <span>{t('accounts.detail_overview_recent_status_timeline_hint')}</span>
            </div>
            <ProviderStatusBar statusData={recentStatusData} styles={styles} />
          </div>
        </div>

        {!hasRecentRequests ? (
          <div
            className={styles.overviewEmptyState}
            data-overview-recent-status-empty-message="true"
          >
            {t('accounts.detail_overview_recent_status_empty')}
          </div>
        ) : null}

        {hasStatusMessage ? (
          <div
            className={styles.overviewRecentStatusMessage}
            data-overview-recent-status-message="true"
          >
            <span>{t('accounts.detail_overview_recent_status_message')}</span>
            <p>{recentStatus.statusMessage}</p>
          </div>
        ) : null}
      </section>

      <div className={styles.overviewCardGrid}>
        <section className={styles.overviewCard} data-overview-section="capacity">
          <div className={styles.overviewCardHeader}>
            <div className={styles.overviewSectionHeading}>
              <span className={styles.overviewSectionIcon} aria-hidden="true">
                <IconDatabaseZap size={18} />
              </span>
              <h3>{t('accounts.detail_overview_capacity_title')}</h3>
            </div>
          </div>
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
            <span className={styles.overviewStatusPill}>{t(capacity.statusLabelKey)}</span>
          </div>
          <p className={styles.overviewCardDescription}>{t(capacity.descriptionKey)}</p>
          <OverviewFieldGrid fields={capacity.fields} />
        </section>

        <section className={styles.overviewCard} data-overview-section="credential">
          <div className={styles.overviewCardHeader}>
            <div className={styles.overviewSectionHeading}>
              <span className={styles.overviewSectionIcon} aria-hidden="true">
                <IconKey size={18} />
              </span>
              <h3>{t('accounts.detail_overview_credential_title')}</h3>
            </div>
          </div>
          <div className={styles.overviewCredentialState}>
            <strong>{t(credential.statusLabelKey)}</strong>
            <span>{t(credential.sourceLabelKey)}</span>
          </div>
          <OverviewFieldGrid fields={credential.fields} />
        </section>
      </div>

      <section
        className={styles.overviewActivityCard}
        data-overview-section="activity"
        data-overview-activity-scope={activity.scope}
      >
        <div className={styles.overviewCardHeader}>
          <div className={styles.overviewSectionHeading}>
            <span className={styles.overviewSectionIcon} aria-hidden="true">
              <IconChartLine size={18} />
            </span>
            <h3>{t('accounts.detail_overview_activity_title')}</h3>
          </div>
          <span
            className={`${styles.overviewScopePill} ${
              activity.scope === 'recent_snapshot' ? styles.overviewScopePillFallback : ''
            }`}
          >
            {activityScopeLabel}
          </span>
        </div>
        {activity.hasActivity ? (
          <UsageSummaryGrid cards={activityCards} density="compact" />
        ) : (
          <div className={styles.overviewEmptyState}>{t(activity.emptyStateKey)}</div>
        )}
      </section>

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
