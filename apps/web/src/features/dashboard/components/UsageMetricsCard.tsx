import type { CSSProperties, ReactNode } from 'react';
import { useTranslation } from 'react-i18next';
import {
  IconChartLine,
  IconDollarSign,
  IconInbox,
  IconTimer,
  IconTrendingUp,
} from '@/components/ui/icons';
import { useThemeStore } from '@/stores';
import type { DashboardSummaryResponse } from '@/services/api/usageService';
import { getDataPalette } from '@/utils/dataPalette';
import { formatCompactNumber, formatDurationMs, formatUsd } from '@/utils/usage';
import { ModelPriceAttentionLink } from '@/features/model-price-attention';
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

interface MetricCardProps {
  label: string;
  value: string;
  subValue?: string;
  icon: ReactNode;
  color: string;
  loading: boolean;
  extraAction?: ReactNode;
}

type MetricStyle = CSSProperties & Record<'--accent-color', string>;
type RankStyle = CSSProperties & Record<'--share', number>;

function MetricCard({ label, value, subValue, icon, color, loading, extraAction }: MetricCardProps) {
  return (
    <div className={styles.metricCard} style={{ '--accent-color': color } as MetricStyle}>
      <div className={styles.metricHeader}>
        <div className={styles.metricIcon}>{icon}</div>
        <span className={styles.metricLabel}>{label}</span>
        {extraAction ? <div className={styles.metricExtraAction}>{extraAction}</div> : null}
      </div>
      <div className={styles.metricBody}>
        <div className={styles.metricValue}>{loading ? '...' : value}</div>
        {subValue && <div className={styles.metricSubValue}>{subValue}</div>}
      </div>
      <div className={styles.metricBgChart}>
        <svg viewBox="0 0 100 30" preserveAspectRatio="none">
          <path d="M0,25 Q15,5 30,20 T60,10 T100,25" fill="none" stroke={color} strokeWidth="2" opacity="0.2" />
        </svg>
      </div>
    </div>
  );
}

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
  const resolvedTheme = useThemeStore((state) => state.resolvedTheme);
  const dataPalette = getDataPalette(resolvedTheme);
  const today = summary?.today;
  const rolling = summary?.rolling_30m;
  const loadingText = loading ? '...' : '-';
  const lastRefreshedText = lastRefreshedAt
    ? t('dashboard.last_refreshed_at', {
        time: lastRefreshedAt.toLocaleTimeString(i18n.language),
      })
    : undefined;

  const metrics = [
    {
      label: t('dashboard.today_requests'),
      value: today ? formatMetric(today.total_calls) : loadingText,
      subValue: today
        ? t('dashboard.metric_failure_count', { value: formatMetric(today.failure_calls) })
        : lastRefreshedText,
      icon: <IconInbox size={20} />,
      color: dataPalette.blue,
    },
    {
      label: t('dashboard.rpm_30m'),
      value: rolling ? formatMetric(rolling.rpm, 1) : loadingText,
      subValue: rolling
        ? t('dashboard.metric_rolling_calls', { value: formatMetric(rolling.total_calls) })
        : undefined,
      icon: <IconChartLine size={20} />,
      color: dataPalette.violet,
    },
    {
      label: t('dashboard.tpm_30m'),
      value: rolling ? formatCompactNumber(rolling.tpm) : loadingText,
      subValue: rolling
        ? t('dashboard.metric_rolling_tokens', { value: formatCompactNumber(rolling.total_tokens) })
        : undefined,
      icon: <IconTrendingUp size={20} />,
      color: dataPalette.emerald,
    },
    {
      label: t('dashboard.today_cost'),
      value: today ? formatUsd(today.total_cost) : loadingText,
      subValue: today
        ? t('dashboard.metric_total_tokens', { value: formatCompactNumber(today.total_tokens) })
        : undefined,
      icon: <IconDollarSign size={20} />,
      color: dataPalette.amber,
      extraAction: <ModelPriceAttentionLink variant="inline" />,
    },
    {
      label: t('dashboard.success_rate'),
      value: today ? formatPercent(today.success_rate) : loadingText,
      subValue: today
        ? `${formatMetric(today.success_calls)} / ${formatMetric(today.total_calls)}`
        : undefined,
      icon: <IconTrendingUp size={20} />,
      color: dataPalette.green,
    },
    {
      label: t('dashboard.avg_latency'),
      value: today
        ? formatDurationMs(today.average_latency_ms, { locale: i18n.language })
        : loadingText,
      subValue: today
        ? t('dashboard.metric_zero_token_calls', { value: formatMetric(today.zero_token_calls) })
        : undefined,
      icon: <IconTimer size={20} />,
      color: dataPalette.red,
    },
  ];

  if (mode === 'metrics-only') {
    return (
      <>
        <div className={styles.metricsGrid}>
          {metrics.map((m) => (
            <MetricCard key={m.label} {...m} loading={loading && !summary} />
          ))}
        </div>
        {error ? <div className={styles.error}>{error}</div> : null}
      </>
    );
  }

  if (mode === 'rank-only') {
    return (
      <section className={styles.rankCard}>
        <div className={styles.rankHeader}>
          <h3>{t('dashboard.model_cost_rank_today')}</h3>
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
                <div className={styles.modelName}>{model.model}</div>
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
