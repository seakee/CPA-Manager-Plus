import {
  useEffect,
  useId,
  useState,
  type ComponentType,
  type KeyboardEvent,
  type ReactNode,
} from 'react';
import type { TFunction } from 'i18next';
import { Button } from '@/components/ui/Button';
import { Select } from '@/components/ui/Select';
import {
  IconArrowDownToLine,
  IconArrowUpFromLine,
  IconBinary,
  IconChartLine,
  IconCheck,
  IconDatabaseZap,
  IconDollarSign,
  IconInbox,
  IconRefreshCw,
  IconShield,
  IconTrash2,
  IconX,
  type IconProps,
} from '@/components/ui/icons';
import type { MonitoringStatusTone } from '@/features/monitoring/hooks/useMonitoringData';
import styles from '../MonitoringCenterPage.module.scss';

export type SummaryCardIcon =
  | 'calls'
  | 'success'
  | 'failure'
  | 'cost'
  | 'tokens'
  | 'input'
  | 'output'
  | 'cache'
  | 'probe'
  | 'sampled'
  | 'delete'
  | 'disable'
  | 'enable'
  | 'reauth';

export type SummaryCardAccent =
  | 'blue'
  | 'green'
  | 'red'
  | 'amber'
  | 'indigo'
  | 'cyan'
  | 'violet'
  | 'teal';

export type SummaryCardProps = {
  label: string;
  fullLabel?: string;
  value: string;
  valueTitle?: string;
  meta: string;
  tone?: MonitoringStatusTone;
  variant?: 'primary' | 'secondary';
  icon?: SummaryCardIcon;
  accent?: SummaryCardAccent;
};

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
  t: TFunction;
};

const parsePageSize = (value: string, fallback: number) => {
  const parsed = Number.parseInt(value, 10);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : fallback;
};

const clampPage = (value: number, totalPages: number) =>
  Math.min(Math.max(Number.isFinite(value) ? value : 1, 1), Math.max(totalPages, 1));

const shortLabel = (t: TFunction, shortKey: string, fallbackKey: string) => {
  const fallback = t(fallbackKey);
  const label = t(shortKey, { defaultValue: fallback });
  return label === shortKey ? fallback : label;
};

const summaryIconMap: Record<SummaryCardIcon, ComponentType<IconProps>> = {
  calls: IconInbox,
  success: IconCheck,
  failure: IconX,
  cost: IconDollarSign,
  tokens: IconBinary,
  input: IconArrowDownToLine,
  output: IconArrowUpFromLine,
  cache: IconDatabaseZap,
  probe: IconInbox,
  sampled: IconChartLine,
  delete: IconTrash2,
  disable: IconShield,
  enable: IconCheck,
  reauth: IconRefreshCw,
};

const summaryAccentClassMap: Record<SummaryCardAccent, string> = {
  blue: styles.summaryAccentBlue,
  green: styles.summaryAccentGreen,
  red: styles.summaryAccentRed,
  amber: styles.summaryAccentAmber,
  indigo: styles.summaryAccentIndigo,
  cyan: styles.summaryAccentCyan,
  violet: styles.summaryAccentViolet,
  teal: styles.summaryAccentTeal,
};

export function SummaryCard({
  label,
  fullLabel,
  value,
  valueTitle,
  meta,
  tone,
  variant = 'primary',
  icon,
  accent = 'blue',
}: SummaryCardProps) {
  const Icon = icon ? summaryIconMap[icon] : null;
  const tooltipId = useId();
  const tooltipValue = valueTitle ?? value;
  const resolvedLabel = fullLabel ?? label;
  const hasValueTooltip = tooltipValue !== value;
  const cardClassName = [
    'card',
    styles.summaryCard,
    variant === 'secondary' ? styles.summaryCardSecondary : styles.summaryCardPrimary,
    summaryAccentClassMap[accent],
  ]
    .filter(Boolean)
    .join(' ');

  return (
    <div className={cardClassName}>
      <div className={styles.summaryCardHeader}>
        {Icon ? (
          <span className={styles.summaryIcon}>
            <Icon size={20} />
          </span>
        ) : null}
        <span className={styles.summaryLabel} title={resolvedLabel}>
          {label}
        </span>
      </div>
      <div className={styles.summaryCardBody}>
        <span className={styles.summaryValueWrap}>
          <strong
            className={`${styles.summaryValue} ${tone ? styles[`tone${tone}`] : ''}`}
            tabIndex={hasValueTooltip ? 0 : undefined}
            aria-describedby={hasValueTooltip ? tooltipId : undefined}
          >
            {value}
          </strong>
          {hasValueTooltip ? (
            <span id={tooltipId} className={styles.summaryValueTooltip} role="tooltip">
              <span className={styles.summaryValueTooltipLabel}>{resolvedLabel}</span>
              <span className={styles.summaryValueTooltipValue}>{tooltipValue}</span>
            </span>
          ) : null}
        </span>
        <span className={styles.summaryMeta} title={meta}>
          {meta}
        </span>
      </div>
      <div className={styles.summaryCardChart} aria-hidden="true">
        <svg viewBox="0 0 100 30" preserveAspectRatio="none">
          <path d="M0,25 Q15,5 30,20 T60,10 T100,25" />
        </svg>
      </div>
    </div>
  );
}

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
  t,
}: PaginationControlsProps) {
  const [pageDraft, setPageDraft] = useState(String(currentPage));

  useEffect(() => {
    setPageDraft(String(currentPage));
  }, [currentPage]);

  if (count === 0) return null;

  const commitPageDraft = () => {
    const nextPage = clampPage(Number.parseInt(pageDraft, 10), totalPages);
    setPageDraft(String(nextPage));
    if (nextPage !== currentPage) {
      onPageChange(nextPage);
    }
  };

  const handlePageJumpKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key !== 'Enter') return;
    event.preventDefault();
    commitPageDraft();
  };

  return (
    <div className={styles.paginationBar}>
      <div className={styles.paginationInfo}>
        {t('monitoring.pagination_info', {
          current: currentPage,
          total: totalPages,
          start: startItem,
          end: endItem,
          count,
        })}
      </div>
      <div className={styles.paginationControls}>
        <div className={styles.pageSizeField}>
          <span title={t('monitoring.page_size_label')}>
            {shortLabel(t, 'monitoring.page_size_label_short', 'monitoring.page_size_label')}
          </span>
          <Select
            className={styles.pageSizeSelect}
            triggerClassName={styles.pageSizeSelectTrigger}
            value={String(pageSize)}
            options={pageSizeOptions.map((size) => ({
              value: String(size),
              label: t('monitoring.page_size_option', { count: size }),
            }))}
            onChange={(value) => onPageSizeChange(parsePageSize(value, pageSize))}
            ariaLabel={t('monitoring.page_size_label')}
            fullWidth={false}
          />
        </div>
        <Button
          variant="secondary"
          size="sm"
          onClick={() => onPageChange(Math.max(1, currentPage - 1))}
          disabled={currentPage <= 1}
        >
          {shortLabel(t, 'monitoring.pagination_prev_short', 'monitoring.pagination_prev')}
        </Button>
        <Button
          variant="secondary"
          size="sm"
          onClick={() => onPageChange(Math.min(totalPages, currentPage + 1))}
          disabled={currentPage >= totalPages}
        >
          {shortLabel(t, 'monitoring.pagination_next_short', 'monitoring.pagination_next')}
        </Button>
        <label className={styles.pageJumpField}>
          <span>{t('monitoring.pagination_jump_prefix')}</span>
          <input
            type="number"
            min={1}
            max={Math.max(totalPages, 1)}
            value={pageDraft}
            onChange={(event) => setPageDraft(event.target.value)}
            onBlur={commitPageDraft}
            onKeyDown={handlePageJumpKeyDown}
            aria-label={t('monitoring.pagination_jump_label')}
          />
          <span>{t('monitoring.pagination_jump_suffix')}</span>
        </label>
      </div>
    </div>
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

export function RecentPattern({
  pattern,
  variant = 'default',
}: {
  pattern: boolean[];
  variant?: 'default' | 'plain';
}) {
  const fallbackLength = variant === 'plain' ? 5 : 10;
  const normalized = pattern.length > 0 ? pattern : Array.from({ length: fallbackLength }, () => true);
  const visiblePattern = variant === 'plain' ? normalized.slice(-5) : normalized;
  const containerClassName = [
    styles.patternBars,
    variant === 'plain' ? styles.patternBarsPlain : '',
  ]
    .filter(Boolean)
    .join(' ');
  const barClassName = [styles.patternBar, variant === 'plain' ? styles.patternBarPlain : '']
    .filter(Boolean)
    .join(' ');

  return (
    <div className={containerClassName} aria-hidden="true">
      {visiblePattern.map((item, index) => (
        <span
          key={`${index}-${item ? 'success' : 'failed'}`}
          className={`${barClassName} ${item ? styles.patternSuccess : styles.patternFailed}`}
        />
      ))}
    </div>
  );
}
