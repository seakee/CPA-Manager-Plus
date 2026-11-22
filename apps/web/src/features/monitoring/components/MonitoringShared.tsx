import type { ReactNode } from 'react';
import type { TFunction } from 'i18next';
import {
  MetricSummaryCard,
  type MetricSummaryCardAccent,
  type MetricSummaryCardIcon,
  type MetricSummaryCardProps,
} from '@/components/common/MetricSummaryCard';
import { PaginationFooter } from '@/components/common/PaginationFooter';
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

const shortLabel = (t: TFunction, shortKey: string, fallbackKey: string) => {
  const fallback = t(fallbackKey);
  const label = t(shortKey, { defaultValue: fallback });
  return label === shortKey ? fallback : label;
};

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
  extraActions,
  t,
}: PaginationControlsProps) {
  if (count === 0) return null;

  const baseInfo = t('monitoring.pagination_info', {
    current: currentPage,
    total: totalPages,
    start: startItem,
    end: endItem,
    count,
  });

  return (
    <PaginationFooter
      variant="inset"
      currentPage={currentPage}
      totalPages={totalPages}
      infoText={
        infoSuffix ? (
          <>
            <span>{baseInfo}</span>
            <span className={styles.paginationInfoSuffix}> · {infoSuffix}</span>
          </>
        ) : (
          baseInfo
        )
      }
      extraActions={extraActions}
      pageSize={pageSize}
      pageSizeOptions={pageSizeOptions}
      pageSizeLabel={shortLabel(t, 'monitoring.page_size_label_short', 'monitoring.page_size_label')}
      pageSizeTitle={t('monitoring.page_size_label')}
      pageSizeAriaLabel={t('monitoring.page_size_label')}
      formatPageSizeOption={(size) => t('monitoring.page_size_option', { count: size })}
      onPageChange={onPageChange}
      onPageSizeChange={onPageSizeChange}
      prevLabel={shortLabel(t, 'monitoring.pagination_prev_short', 'monitoring.pagination_prev')}
      nextLabel={shortLabel(t, 'monitoring.pagination_next_short', 'monitoring.pagination_next')}
      showPageJump
      jumpPrefixLabel={t('monitoring.pagination_jump_prefix')}
      jumpSuffixLabel={t('monitoring.pagination_jump_suffix')}
      jumpInputAriaLabel={t('monitoring.pagination_jump_label')}
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
