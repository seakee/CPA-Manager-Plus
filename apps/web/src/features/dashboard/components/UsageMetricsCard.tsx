import type { CSSProperties } from 'react';
import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { MetricSummaryGrid, type MetricSummaryCardProps } from '@/components/common/MetricSummaryCard';
import type { DashboardSummaryResponse } from '@/services/api/usageService';
import { formatCompactNumber, formatDurationMs, formatUsd } from '@/utils/usage';
import styles from './UsageMetricsCard.module.scss';
interface UsageMetricsCardProps {
  summary: DashboardSummaryResponse | null;
  topModels: DashboardSummaryResponse['top_models_today'];
  modelCostRank: DashboardSummaryResponse['model_cost_rank'];
  loading: boolean;
  error?: string;
  lastRefreshedAt: Date | null;
  mode?: 'metrics-only' | 'rank-only' | 'all';
}

const formatMetric = (value: number | undefined, digits = 0) => {
  if (value === undefined || !Number.isFinite(value)) return '-';
  return value.toLocaleString(undefined, {
    maximumFractionDigits: digits,
    minimumFractionDigits: digits,
  });
};

const formatPercent = (value: number | undefined) => {
  if (value === undefined || !Number.isFinite(value)) return '-';
  return `${(value * 100).toFixed(2)}%`;
};

type RankStyle = CSSProperties & Record<'--share', number>;
export function UsageMetricsCard({
  summary,
  topModels,
  modelCostRank,
  loading,
  error,
  lastRefreshedAt,
  mode = 'all',
}: UsageMetricsCardProps) {
  const { t, i18n } = useTranslation();
  const today = summary?.today;
  const rolling = summary?.rolling_30m;
  const isInitialLoading = loading && !summary;
  const loadingText = isInitialLoading ? '...' : '-';
  const lastRefreshedText = lastRefreshedAt
    ? t('dashboard.last_refreshed_at', {
        time: lastRefreshedAt.toLocaleTimeString(i18n.language),
      })
    : '';

  const successTone: MetricSummaryCardProps['tone'] =
    today && today.total_calls > 0
      ? today.success_rate >= 0.95
        ? 'good'
        : today.success_rate >= 0.8
          ? 'warn'
          : 'bad'
      : undefined;

  const metricCards: MetricSummaryCardProps[] = [
    {
      label: t('dashboard.today_requests'),
      value: isInitialLoading ? '...' : today ? formatMetric(today.total_calls) : loadingText,
      valueTitle: today ? today.total_calls.toLocaleString(i18n.language) : undefined,
      meta: today
        ? t('dashboard.metric_failure_count', { value: formatMetric(today.failure_calls) })
        : lastRefreshedText,
      icon: 'calls',
      accent: 'blue',
    },
    {
      label: t('dashboard.rpm_30m'),
      value: isInitialLoading ? '...' : rolling ? formatMetric(rolling.rpm, 1) : loadingText,
      meta: rolling
        ? t('dashboard.metric_rolling_calls', { value: formatMetric(rolling.total_calls) })
        : '',
      icon: 'sampled',
      accent: 'violet',
    },
    {
      label: t('dashboard.tpm_30m'),
      value: isInitialLoading ? '...' : rolling ? formatCompactNumber(rolling.tpm) : loadingText,
      valueTitle: rolling ? Math.round(rolling.tpm).toLocaleString(i18n.language) : undefined,
      meta: rolling
        ? t('dashboard.metric_rolling_tokens', { value: formatCompactNumber(rolling.total_tokens) })
        : '',
      icon: 'tokens',
      accent: 'teal',
    },
    {
      label: t('dashboard.today_cost'),
      value: isInitialLoading ? '...' : today ? formatUsd(today.total_cost) : loadingText,
      meta: today
        ? t('dashboard.metric_total_tokens', { value: formatCompactNumber(today.total_tokens) })
        : '',
      icon: 'cost',
      accent: 'amber',
    },
    {
      label: t('dashboard.success_rate'),
      value: isInitialLoading ? '...' : today ? formatPercent(today.success_rate) : loadingText,
      meta: today ? `${formatMetric(today.success_calls)} / ${formatMetric(today.total_calls)}` : '',
      tone: successTone,
      icon: 'success',
      accent: 'green',
    },
    {
      label: t('dashboard.avg_latency'),
      value: isInitialLoading
        ? '...'
        : today
          ? formatDurationMs(today.average_latency_ms, { locale: i18n.language })
          : loadingText,
      meta: today
        ? t('dashboard.metric_zero_token_calls', { value: formatMetric(today.zero_token_calls) })
        : '',
      icon: 'latency',
      accent: 'red',
    },
  ];

  if (mode === 'metrics-only') {
    return (
      <>
        <MetricSummaryGrid
          cards={metricCards}
          columns={6}
          className={styles.dashboardMetricsGrid}
        />
        {error ? <div className={styles.error}>{error}</div> : null}
      </>
    );
  }

  if (mode === 'rank-only') {
    return (
      <section className={styles.rankCard}>
        <div className={styles.rankHeader}>
          <h3>{t('dashboard.model_cost_rank_today')}</h3>
          <Link to="/usage-analytics" className={styles.moreBtn}>
            {t('dashboard.view_top_models')}
          </Link>
        </div>
        <div className={styles.rankList}>
          {(modelCostRank?.length ? modelCostRank : topModels)?.slice(0, 5).map((model, index) => {
            const hasCostShare = 'cost_share' in model && typeof model.cost_share === 'number';
            const share =
              hasCostShare
                ? (model as { cost_share: number }).cost_share
                : topModels.length
                  ? model.tokens / Math.max(...topModels.map((item) => item.tokens), 1)
                  : 0;
            return (
            <div key={model.model} className={styles.rankItem}>
              <div className={styles.rankIndex} data-rank={index + 1}>{index + 1}</div>
              <div className={styles.rankInfo}>
                <div className={styles.modelName} title={model.model}>{model.model}</div>
                <div className={styles.rankTrack}>
                  <div className={styles.rankBar} style={{ '--share': share } as RankStyle} />
                </div>
              </div>
              <div className={styles.rankValue}>
                <div className={styles.cost}>{formatUsd(model.cost)}</div>
                <div className={styles.share}>
                  {hasCostShare
                    ? `${(share * 100).toFixed(1)}%`
                    : formatCompactNumber(model.tokens)}
                </div>
              </div>
            </div>
            );
          })}
          {!modelCostRank?.length && topModels.length === 0 ? (
            <div className={styles.empty}>{loading ? '...' : t('dashboard.no_usage_rank_data')}</div>
          ) : null}
        </div>
      </section>
    );
  }

  return null;
}
