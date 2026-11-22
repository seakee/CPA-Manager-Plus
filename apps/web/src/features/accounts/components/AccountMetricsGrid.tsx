import { useTranslation } from 'react-i18next';
import type { AccountMetrics } from '@/features/accounts/model/accountRows';
import {
  MetricSummaryCard,
  type MetricSummaryCardAccent,
  type MetricSummaryCardIcon,
} from '@/components/common/MetricSummaryCard';
import styles from '../AccountsPage.module.scss';

interface AccountMetricsGridProps {
  metrics: AccountMetrics;
}

export function AccountMetricsGrid({ metrics }: AccountMetricsGridProps) {
  const { t } = useTranslation();
  const cards = [
    {
      key: 'total',
      label: t('accounts.metric_total'),
      value: metrics.total,
      meta: t('accounts.metric_total_meta'),
      icon: 'credential' as const,
      accent: 'blue' as const,
    },
    {
      key: 'available',
      label: t('accounts.metric_available'),
      value: metrics.available,
      meta: t('accounts.metric_available_meta'),
      icon: 'available' as const,
      accent: 'green' as const,
    },
    {
      key: 'attention',
      label: t('accounts.metric_attention'),
      value: metrics.needsAttention,
      meta: t('accounts.metric_attention_meta'),
      icon: 'attention' as const,
      accent: 'red' as const,
    },
    {
      key: 'quota-risk',
      label: t('accounts.metric_quota_risk'),
      value: metrics.quotaRisk,
      meta: t('accounts.metric_quota_risk_meta'),
      icon: 'quota-risk' as const,
      accent: 'amber' as const,
    },
    {
      key: 'disabled',
      label: t('accounts.metric_disabled'),
      value: metrics.disabled,
      meta: t('accounts.metric_disabled_meta'),
      icon: 'disabled' as const,
      accent: 'neutral' as const,
    },
    {
      key: 'unconfirmed',
      label: t('accounts.metric_unconfirmed'),
      value: metrics.unconfirmed,
      meta: t('accounts.metric_unconfirmed_meta'),
      icon: 'unconfirmed' as const,
      accent: 'violet' as const,
    },
  ] satisfies Array<{
    key: string;
    label: string;
    value: number;
    meta: string;
    icon: MetricSummaryCardIcon;
    accent: MetricSummaryCardAccent;
  }>;

  return (
    <section className={styles.metricsSection} aria-label={t('accounts.metrics_label')}>
      <div className={styles.metricsGrid}>
        {cards.map((card) => (
          <MetricSummaryCard
            key={card.key}
            label={card.label}
            value={String(card.value)}
            meta={card.meta}
            icon={card.icon}
            accent={card.accent}
          />
        ))}
      </div>
    </section>
  );
}
