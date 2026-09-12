import { useId } from 'react';
import { useTranslation } from 'react-i18next';
import type { JSX } from 'react';
import { Button } from '@/components/ui/Button';
import {
  IconCalendar,
  IconCheck,
  IconDiamond,
  IconRefreshCw,
  IconTimer,
  IconTriangleAlert,
} from '@/components/ui/icons';
import { LoadingSpinner } from '@/components/ui/LoadingSpinner';
import type { AccountDetailField } from '@/features/accounts/model/accountDetailViewModel';
import {
  formatQuotaResetTimestamp,
  formatTimestampTitle,
} from '@/features/accounts/model/accountsPagePresentation';
import type { CodexSubscriptionEntry } from '@/features/accounts/model/codexSubscription';
import {
  buildCodexSubscriptionSecondaryFields,
  buildCodexSubscriptionSummaryFields,
  getCodexSubscriptionRemainingDays,
} from '@/features/accounts/model/codexSubscription';
import { getPlanLabel, getPlanPresentation } from '@/utils/plans';
import { AccountDetailFieldValue } from './AccountDetailFieldList';
import styles from '@/features/accounts/AccountsPage.module.scss';

type MetricTone = 'blue' | 'green' | 'teal' | 'amber';

interface MetricCellProps {
  icon: JSX.Element;
  tone: MetricTone;
  label: string;
  value: string;
  hint?: string;
  valueTitle?: string;
  metricKey: string;
}

const metricIconClass = (tone: MetricTone): string => {
  switch (tone) {
    case 'blue':
      return `${styles.metricIcon} ${styles.metricIconBlue}`;
    case 'green':
      return `${styles.metricIcon} ${styles.metricIconGreen}`;
    case 'teal':
      return `${styles.metricIcon} ${styles.metricIconTeal}`;
    case 'amber':
      return `${styles.metricIcon} ${styles.metricIconAmber}`;
    default:
      return styles.metricIcon;
  }
};

const metricCardClass = (tone: MetricTone): string => {
  switch (tone) {
    case 'blue':
      return styles.quotaSummaryMetricBlue;
    case 'green':
      return styles.quotaSummaryMetricGreen;
    case 'teal':
      return styles.quotaSummaryMetricTeal;
    case 'amber':
      return styles.quotaSummaryMetricAmber;
    default:
      return '';
  }
};

const MetricCell = ({
  icon,
  tone,
  label,
  value,
  hint,
  valueTitle,
  metricKey,
}: MetricCellProps): JSX.Element => {
  const tooltipId = useId();
  const hasValueTooltip = valueTitle !== undefined && valueTitle !== value;

  return (
    <div
      className={`${styles.quotaSummaryMetric} ${metricCardClass(tone)}`}
      data-account-subscription-metric={metricKey}
    >
      <div className={styles.quotaSummaryMetricHeader}>
        <span className={metricIconClass(tone)} aria-hidden="true">
          {icon}
        </span>
        <span className={styles.quotaSummaryMetricLabel}>{label}</span>
      </div>
      <span className={styles.quotaSummaryValueWrap}>
        <strong
          className={styles.quotaSummaryValue}
          tabIndex={hasValueTooltip ? 0 : undefined}
          aria-describedby={hasValueTooltip ? tooltipId : undefined}
        >
          {value}
        </strong>
        {hasValueTooltip ? (
          <span id={tooltipId} className={styles.quotaSummaryValueTooltip} role="tooltip">
            <span className={styles.quotaSummaryValueTooltipLabel}>{label}</span>
            <span className={styles.quotaSummaryValueTooltipValue}>{valueTitle}</span>
          </span>
        ) : null}
      </span>
      {hint ? <span className={styles.quotaSummaryMetricLabel}>{hint}</span> : null}
    </div>
  );
};

const SUMMARY_METRIC_PRESENTATION: Record<
  string,
  { icon: (size?: number) => JSX.Element; tone: MetricTone }
> = {
  planType: { icon: (size = 20) => <IconDiamond size={size} />, tone: 'blue' },
  activeUntilMs: { icon: (size = 20) => <IconCalendar size={size} />, tone: 'teal' },
  billingPeriod: { icon: (size = 20) => <IconTimer size={size} />, tone: 'amber' },
  willRenew: { icon: (size = 20) => <IconCheck size={size} />, tone: 'green' },
};

interface AccountSubscriptionTabProps {
  entry: CodexSubscriptionEntry;
  missingAccountId: boolean;
  refreshing: boolean;
  onRefresh: () => void;
}

export function AccountSubscriptionTab({
  entry,
  missingAccountId,
  refreshing,
  onRefresh,
}: AccountSubscriptionTabProps) {
  const { t, i18n } = useTranslation();
  const record = entry.status === 'ready' ? entry.record : null;
  const summaryFields = record ? buildCodexSubscriptionSummaryFields(record) : [];
  const secondaryFields = record ? buildCodexSubscriptionSecondaryFields(record) : [];
  const failed = entry.status === 'soft_failed';
  const delinquent = record?.extras.isDelinquent === true;

  const formatSummaryValue = (
    field: AccountDetailField
  ): { value: string; hint?: string; valueTitle?: string } => {
    if (field.key === 'planType') {
      const presentation = getPlanPresentation({
        provider: 'codex',
        planType: field.value,
        t,
      });
      return {
        value: getPlanLabel(presentation, 'compact') ?? (field.value ? String(field.value) : '-'),
      };
    }

    if (field.key === 'activeUntilMs') {
      if (typeof field.value !== 'number') return { value: '-' };
      const timestamp = formatQuotaResetTimestamp(field.value, i18n.language);
      const remainingDays = getCodexSubscriptionRemainingDays(field.value);
      const title = formatTimestampTitle(field.value, i18n.language);
      if (remainingDays === null) {
        return { value: timestamp, valueTitle: title };
      }
      const remaining = t('accounts.list_plan_remaining_days', { days: remainingDays });
      const remainingTitle = t('accounts.list_plan_remaining_days_tooltip', {
        days: remainingDays,
      });
      return {
        value: timestamp,
        hint: remaining,
        valueTitle: title ? `${title} · ${remainingTitle}` : remainingTitle,
      };
    }

    if (field.value === null || field.value === '') return { value: '-' };
    if (field.valueKind === 'i18n') {
      return { value: t(String(field.value), { defaultValue: String(field.value) }) };
    }
    return { value: String(field.value) };
  };

  return (
    <div className={styles.quotaTab} data-account-subscription-tab="true">
      <div className={styles.quotaTabHeader}>
        <div className={styles.quotaPageHeading}>
          <h2 className={styles.quotaPageTitle}>{t('accounts.detail_tab_subscription')}</h2>
          <p>{t('accounts.detail_subscription_desc')}</p>
        </div>
        <div className={styles.quotaTabActions}>
          <Button
            variant="secondary"
            size="sm"
            onClick={onRefresh}
            disabled={refreshing || missingAccountId}
            loading={refreshing}
          >
            {!refreshing ? <IconRefreshCw size={15} /> : null}
            {t('accounts.detail_subscription_refresh')}
          </Button>
        </div>
      </div>

      {missingAccountId ? (
        <p className={styles.quotaTabStatus}>{t('accounts.detail_subscription_missing_account')}</p>
      ) : null}

      {entry.status === 'loading' && !record ? (
        <div className={styles.quotaTabStatus}>
          <LoadingSpinner size={16} />
          <span>{t('accounts.detail_subscription_loading')}</span>
        </div>
      ) : null}

      {failed ? (
        <div className={styles.errorBox}>
          {t('accounts.detail_subscription_soft_failed', { kind: entry.errorKind })}
        </div>
      ) : null}

      {record ? (
        <>
          {delinquent ? (
            <section
              className={styles.overviewAttentionCard}
              data-overview-attention-priority="high"
              data-account-subscription-delinquent="true"
            >
              <span className={styles.overviewAttentionIcon} aria-hidden="true">
                <IconTriangleAlert size={19} />
              </span>
              <div className={styles.overviewAttentionBody}>
                <h3 className={styles.overviewAttentionHeading}>
                  {t('accounts.detail_subscription_delinquent')}
                </h3>
                <p>{t('accounts.detail_subscription_delinquent_notice')}</p>
              </div>
            </section>
          ) : null}

          <section className={styles.quotaSummaryPanel} data-account-subscription-summary="true">
            <div className={styles.quotaSummaryHeading}>
              <h3>{t('accounts.detail_subscription_summary_title')}</h3>
            </div>
            <div className={styles.quotaSummaryMetrics} data-account-subscription-metrics="true">
              {summaryFields.map((field) => {
                const presentation = SUMMARY_METRIC_PRESENTATION[field.key];
                const formatted = formatSummaryValue(field);
                const tone =
                  field.key === 'willRenew' && field.value === 'common.no'
                    ? 'amber'
                    : (presentation?.tone ?? 'blue');
                return (
                  <MetricCell
                    key={field.key}
                    metricKey={field.key}
                    icon={presentation?.icon() ?? <IconDiamond size={20} />}
                    tone={tone}
                    label={t(field.labelKey, { defaultValue: field.labelKey })}
                    value={formatted.value}
                    hint={formatted.hint}
                    valueTitle={formatted.valueTitle}
                  />
                );
              })}
            </div>
          </section>

          {secondaryFields.length > 0 ? (
            <section className={styles.quotaSection} data-account-subscription-details="true">
              <div className={styles.quotaSectionHeading}>
                <h3>{t('accounts.detail_subscription_details_title')}</h3>
                <span>{t('accounts.detail_subscription_details_desc')}</span>
              </div>
              <dl className={styles.overviewFieldGrid}>
                {secondaryFields.map((field) => {
                  const isDelinquentField = field.key === 'isDelinquent';
                  return (
                    <div key={field.key} data-subscription-field={field.key}>
                      <dt>{t(field.labelKey, { defaultValue: field.labelKey })}</dt>
                      <dd>
                        {isDelinquentField ? (
                          <span className={`${styles.badge} ${styles.badgeBad}`}>
                            <AccountDetailFieldValue field={field} />
                          </span>
                        ) : (
                          <AccountDetailFieldValue field={field} />
                        )}
                      </dd>
                    </div>
                  );
                })}
              </dl>
            </section>
          ) : null}
        </>
      ) : null}

      {!missingAccountId && entry.status === 'idle' ? (
        <p className={styles.quotaEmpty}>{t('accounts.detail_subscription_empty')}</p>
      ) : null}
    </div>
  );
}
