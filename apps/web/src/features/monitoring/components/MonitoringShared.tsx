import type { ReactNode } from 'react';
import type { TFunction } from 'i18next';
import {
  MetricSummaryCard,
  type MetricSummaryCardAccent,
  type MetricSummaryCardIcon,
  type MetricSummaryCardProps,
} from '@/components/common/MetricSummaryCard';
import { LocalizedPaginationFooter } from '@/components/common/LocalizedPaginationFooter';
import type { MonitoringStatusTone } from '@/features/monitoring/hooks/useMonitoringData';
import styles from '../MonitoringCenterPage.module.scss';

export type SummaryCardIcon = MetricSummaryCardIcon;
export type SummaryCardAccent = MetricSummaryCardAccent;
export type SummaryCardProps = MetricSummaryCardProps;

type PaginationControlsProps = {
  count: number;
  currentPage: number;
  totalPages: number;
  startItem: number;
  endItem: number;
  pageSize: number;
  pageSizeOptions: readonly number[];
  onPageChange: (page: number) => void;
  onPageSizeChange: (pageSize: number) => void;
  infoSuffix?: ReactNode;
  extraActions?: ReactNode;
  t: TFunction;
};

export type { PaginationControlsProps };

export const SummaryCard = MetricSummaryCard;

export function PaginationControls({
  count,
  currentPage,
  totalPages,
  startItem,
  endItem,
  pageSize,
  pageSizeOptions,
  onPageChange,
  onPageSizeChange,
  infoSuffix,
  t,
  extraActions,
}: PaginationControlsProps) {
  if (count === 0) return null;

  return (
    <LocalizedPaginationFooter
      variant="flush"
      compactLabels
      currentPage={currentPage}
      totalPages={totalPages}
      totalCount={count}
      startItem={startItem}
      endItem={endItem}
      pageSize={pageSize}
      pageSizeOptions={pageSizeOptions}
      onPageChange={onPageChange}
      onPageSizeChange={onPageSizeChange}
      infoSuffix={infoSuffix}
      infoSuffixClassName={styles.paginationInfoSuffix}
      extraActions={extraActions}
      t={t}
    />
  );
}

export function StatusBadge({
  tone,
  children,
}: {
  tone: MonitoringStatusTone;
  children: ReactNode;
}) {
  return <span className={`${styles.statusBadge} ${styles[`tone${tone}`]}`}>{children}</span>;
}
