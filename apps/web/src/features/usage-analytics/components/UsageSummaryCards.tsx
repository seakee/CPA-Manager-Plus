import { MetricSummaryGrid } from '@/components/common/MetricSummaryCard';
import type { UsageSummaryCard } from '../usageAnalyticsPresentation';
import styles from '../UsageAnalyticsPage.module.scss';

const resolveSummaryColumns = (count: number) => Math.max(Math.min(count, 8), 1);

export function UsageSummarySection({ cards }: { cards: UsageSummaryCard[] }) {
  return (
    <section className={styles.usageSummarySection}>
      <MetricSummaryGrid cards={cards} columns={resolveSummaryColumns(cards.length)} />
    </section>
  );
}
