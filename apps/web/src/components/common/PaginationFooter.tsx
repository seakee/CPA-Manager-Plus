import { useEffect, useState, type KeyboardEvent, type ReactNode } from 'react';
import { Button } from '@/components/ui/Button';
import { Select } from '@/components/ui/Select';
import styles from './PaginationFooter.module.scss';

export type PaginationFooterTestIds = {
  root?: string;
  info?: string;
  prev?: string;
  next?: string;
  jumpInput?: string;
};

export type PaginationFooterProps = {
  currentPage: number;
  totalPages: number;
  infoText: ReactNode;
  prevLabel: ReactNode;
  nextLabel: ReactNode;
  onPageChange: (nextPage: number) => void;
  pageSize?: number;
  pageSizeOptions?: ReadonlyArray<number>;
  pageSizeLabel?: ReactNode;
  pageSizeTitle?: string;
  pageSizeAriaLabel?: string;
  formatPageSizeOption?: (size: number) => string;
  onPageSizeChange?: (nextPageSize: number) => void;
  showPageJump?: boolean;
  jumpPrefixLabel?: ReactNode;
  jumpSuffixLabel?: ReactNode;
  jumpInputAriaLabel?: string;
  extraActions?: ReactNode;
  disabled?: boolean;
  variant?: 'flush' | 'inset';
  className?: string;
  testIds?: PaginationFooterTestIds;
};

const clampPageNumber = (value: number, totalPages: number) => {
  const safeTotal = Math.max(1, totalPages);
  if (!Number.isFinite(value)) return 1;
  return Math.min(Math.max(1, Math.trunc(value)), safeTotal);
};

export function PaginationFooter({
  currentPage,
  totalPages,
  infoText,
  prevLabel,
  nextLabel,
  onPageChange,
  pageSize,
  pageSizeOptions,
  pageSizeLabel,
  pageSizeTitle,
  pageSizeAriaLabel,
  formatPageSizeOption = (size) => String(size),
  onPageSizeChange,
  showPageJump = false,
  jumpPrefixLabel,
  jumpSuffixLabel,
  jumpInputAriaLabel,
  extraActions,
  disabled = false,
  variant = 'flush',
  className = '',
  testIds,
}: PaginationFooterProps) {
  const [pageDraft, setPageDraft] = useState(String(currentPage));

  useEffect(() => {
    setPageDraft(String(currentPage));
  }, [currentPage]);

  const commitPageDraft = () => {
    const nextPage = clampPageNumber(Number.parseInt(pageDraft, 10), totalPages);
    setPageDraft(String(nextPage));
    if (nextPage !== currentPage) {
      onPageChange(nextPage);
    }
  };

  const handleJumpKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key !== 'Enter') return;
    event.preventDefault();
    commitPageDraft();
  };

  const rootClasses = [
    styles.root,
    variant === 'inset' ? styles.variantInset : styles.variantFlush,
    className,
  ]
    .filter(Boolean)
    .join(' ');

  const hasPageSizeControl =
    typeof pageSize === 'number' &&
    Array.isArray(pageSizeOptions) &&
    pageSizeOptions.length > 0 &&
    typeof onPageSizeChange === 'function';

  return (
    <div className={rootClasses} data-testid={testIds?.root}>
      <div className={styles.info} data-testid={testIds?.info}>
        {infoText}
      </div>
      <div className={styles.controls}>
        {extraActions}
        {hasPageSizeControl ? (
          <div className={styles.pageSizeField}>
            {pageSizeLabel ? <span title={pageSizeTitle}>{pageSizeLabel}</span> : null}
            <Select
              className={styles.pageSizeSelect}
              triggerClassName={styles.pageSizeTrigger}
              value={String(pageSize)}
              options={pageSizeOptions.map((size) => ({
                value: String(size),
                label: formatPageSizeOption(size),
              }))}
              onChange={(value) => {
                const parsed = Number.parseInt(value, 10);
                onPageSizeChange(Number.isFinite(parsed) && parsed > 0 ? parsed : pageSize);
              }}
              disabled={disabled}
              fullWidth={false}
              ariaLabel={
                pageSizeAriaLabel ??
                (typeof pageSizeLabel === 'string' ? pageSizeLabel : 'Page size')
              }
            />
          </div>
        ) : null}

        <Button
          variant="secondary"
          size="sm"
          onClick={() => onPageChange(Math.max(1, currentPage - 1))}
          disabled={disabled || currentPage <= 1}
          data-testid={testIds?.prev}
        >
          {prevLabel}
        </Button>
        <Button
          variant="secondary"
          size="sm"
          onClick={() => onPageChange(Math.min(Math.max(1, totalPages), currentPage + 1))}
          disabled={disabled || currentPage >= totalPages}
          data-testid={testIds?.next}
        >
          {nextLabel}
        </Button>

        {showPageJump ? (
          <label className={styles.pageJumpField}>
            {jumpPrefixLabel ? <span>{jumpPrefixLabel}</span> : null}
            <input
              type="number"
              min={1}
              max={Math.max(totalPages, 1)}
              value={pageDraft}
              disabled={disabled}
              onChange={(event) => setPageDraft(event.target.value)}
              onBlur={commitPageDraft}
              onKeyDown={handleJumpKeyDown}
              aria-label={jumpInputAriaLabel}
              className={styles.pageJumpInput}
              data-testid={testIds?.jumpInput}
            />
            {jumpSuffixLabel ? <span>{jumpSuffixLabel}</span> : null}
          </label>
        ) : null}
      </div>
    </div>
  );
}
