import { useTranslation } from 'react-i18next';
import { formatCompactNumber, formatUsd } from '@/utils/usage';
import type {
  CodexQuotaAggregateSummary as CodexQuotaAggregateSummaryData,
  CodexQuotaAggregateWindow,
} from './codexQuotaAggregateModel';
import styles from './QuotaPage.module.scss';

export interface CodexQuotaAggregateSummaryProps {
  summary: CodexQuotaAggregateSummaryData;
  loading?: boolean;
}

const formatEstimatedTokens = (value: number | null): string =>
  value === null ? '--' : `~${formatCompactNumber(Math.round(value))}`;

const formatEstimatedCost = (value: number | null): string =>
  value === null ? '--' : `~${formatUsd(value)}`;

export function CodexQuotaAggregateSummary({
  summary,
  loading = false,
}: CodexQuotaAggregateSummaryProps) {
  const { t } = useTranslation();
  const rows: Array<{
    id: 'five-hour' | 'weekly';
    label: string;
    value: CodexQuotaAggregateWindow;
  }> = [
    {
      id: 'five-hour',
      label: t('codex_quota.aggregate_five_hour'),
      value: summary.fiveHour,
    },
    {
      id: 'weekly',
      label: t('codex_quota.aggregate_weekly'),
      value: summary.weekly,
    },
  ];

  return (
    <section
      className={styles.codexAggregateSummary}
      aria-label={t('codex_quota.aggregate_title')}
      aria-busy={loading}
    >
      <div className={styles.codexAggregateHeader}>{t('codex_quota.aggregate_title')}</div>
      <div className={styles.codexAggregateRows}>
        {rows.map((row) => (
          <div
            key={row.id}
            className={styles.codexAggregateRow}
            data-codex-aggregate-window={row.id}
          >
            <div className={styles.codexAggregateWindow}>{row.label}</div>
            <div
              className={`${styles.codexAggregateMetric} ${styles.codexAggregateCostMetric}`}
            >
              <span className={styles.codexAggregateLabel}>
                {t('codex_quota.aggregate_value')}
              </span>
              <strong className={styles.codexAggregateValue}>
                {formatEstimatedCost(row.value.remainingCost)}
              </strong>
            </div>
            <div
              className={`${styles.codexAggregateMetric} ${styles.codexAggregateTokenMetric}`}
            >
              <span className={styles.codexAggregateLabel}>
                {t('codex_quota.aggregate_tokens')}
              </span>
              <strong className={styles.codexAggregateValue}>
                {formatEstimatedTokens(row.value.remainingTokens)}
              </strong>
            </div>
            <span className={styles.codexAggregateCoverage}>
              {loading
                ? t('codex_quota.aggregate_loading')
                : t('codex_quota.aggregate_coverage', {
                    estimated: row.value.estimatedCredentialCount,
                    total: row.value.eligibleCredentialCount,
                  })}
            </span>
          </div>
        ))}
      </div>
    </section>
  );
}
