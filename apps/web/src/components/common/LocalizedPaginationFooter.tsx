import type { ReactNode } from 'react';
import type { TFunction } from 'i18next';
import { useTranslation } from 'react-i18next';
import {
  PaginationFooter,
  type PaginationFooterProps,
  type PaginationFooterTestIds,
} from './PaginationFooter';

export type LocalizedPaginationFooterProps = {
  currentPage: number;
  totalPages: number;
  totalCount: number;
  startItem: number;
  endItem: number;
  pageSize: number;
  pageSizeOptions: readonly number[];
  onPageChange: (nextPage: number) => void;
  onPageSizeChange: (nextPageSize: number) => void;
  variant?: PaginationFooterProps['variant'];
  showPageJump?: boolean;
  compactLabels?: boolean;
  infoText?: ReactNode;
  infoSuffix?: ReactNode;
  infoSuffixClassName?: string;
  prevLabel?: ReactNode;
  nextLabel?: ReactNode;
  extraActions?: ReactNode;
  disabled?: boolean;
  t?: TFunction;
  className?: string;
  testIds?: PaginationFooterTestIds;
};

const resolveCompactLabel = (t: TFunction, shortKey: string, fallbackKey: string): string => {
  const fallback = t(fallbackKey);
  const shortLabel = t(shortKey, { defaultValue: fallback });
  return shortLabel === shortKey ? fallback : shortLabel;
};

export function LocalizedPaginationFooter({
  currentPage,
  totalPages,
  totalCount,
  startItem,
  endItem,
  pageSize,
  pageSizeOptions,
  onPageChange,
  onPageSizeChange,
  variant = 'flush',
  showPageJump = true,
  compactLabels = false,
  infoText,
  infoSuffix,
  infoSuffixClassName,
  prevLabel: customPrevLabel,
  nextLabel: customNextLabel,
  extraActions,
  disabled = false,
  t: customT,
  className = '',
  testIds,
}: LocalizedPaginationFooterProps) {
  const { t: hookT } = useTranslation();
  const t = customT ?? hookT;

  const baseInfo = t('monitoring.pagination_info', {
    current: currentPage,
    total: totalPages,
    start: startItem,
    end: endItem,
    count: totalCount,
  });

  const fullPageSizeLabel = t('monitoring.page_size_label');
  const pageSizeLabel = compactLabels
    ? resolveCompactLabel(t, 'monitoring.page_size_label_short', 'monitoring.page_size_label')
    : fullPageSizeLabel;
  const prevLabel =
    customPrevLabel ??
    (compactLabels
      ? resolveCompactLabel(t, 'monitoring.pagination_prev_short', 'monitoring.pagination_prev')
      : t('monitoring.pagination_prev'));
  const nextLabel =
    customNextLabel ??
    (compactLabels
      ? resolveCompactLabel(t, 'monitoring.pagination_next_short', 'monitoring.pagination_next')
      : t('monitoring.pagination_next'));

  const resolvedInfoText =
    infoText !== undefined
      ? infoText
      : infoSuffix
        ? (
          <>
            <span>{baseInfo}</span>
            <span className={infoSuffixClassName}> · {infoSuffix}</span>
          </>
        )
        : baseInfo;

  return (
    <PaginationFooter
      variant={variant}
      currentPage={currentPage}
      totalPages={totalPages}
      infoText={resolvedInfoText}
      pageSize={pageSize}
      pageSizeOptions={pageSizeOptions}
      pageSizeLabel={pageSizeLabel}
      pageSizeTitle={fullPageSizeLabel}
      pageSizeAriaLabel={fullPageSizeLabel}
      formatPageSizeOption={(size) => t('monitoring.page_size_option', { count: size })}
      onPageChange={onPageChange}
      onPageSizeChange={onPageSizeChange}
      prevLabel={prevLabel}
      nextLabel={nextLabel}
      showPageJump={showPageJump}
      jumpPrefixLabel={t('monitoring.pagination_jump_prefix')}
      jumpSuffixLabel={t('monitoring.pagination_jump_suffix')}
      jumpInputAriaLabel={t('monitoring.pagination_jump_label')}
      extraActions={extraActions}
      disabled={disabled}
      className={className}
      testIds={testIds}
    />
  );
}
