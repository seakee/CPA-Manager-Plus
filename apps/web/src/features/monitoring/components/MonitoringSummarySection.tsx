import { SummaryCard, type SummaryCardProps } from '@/features/monitoring/components/MonitoringShared';
import styles from '../MonitoringCenterPage.module.scss';

type MonitoringSummarySectionProps = {
  primaryCards: SummaryCardProps[];
  secondaryCards: SummaryCardProps[];
};

export function MonitoringSummarySection({
  primaryCards,
  secondaryCards,
}: MonitoringSummarySectionProps) {
  const cards = [...primaryCards, ...secondaryCards];

  return (
    <section className={styles.summarySection}>
      <div className={styles.summaryGrid}>
        {cards.map((card) => (
          <SummaryCard key={card.label} {...card} />
        ))}
      </div>
    </section>
  );
}
