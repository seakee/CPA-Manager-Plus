import { useId, type CSSProperties, type ComponentType } from 'react';
import {
  IconArrowDownToLine,
  IconArrowUpFromLine,
  IconBan,
  IconBinary,
  IconChartLine,
  IconCheck,
  IconCircleHelp,
  IconDatabaseZap,
  IconDollarSign,
  IconInbox,
  IconKey,
  IconModelCluster,
  IconPercentCircle,
  IconRefreshCw,
  IconShield,
  IconShieldCheck,
  IconTimer,
  IconTrash2,
  IconTrendingUp,
  IconTriangleAlert,
  IconX,
  type IconProps,
} from '@/components/ui/icons';
import styles from './MetricSummaryCard.module.scss';

export type MetricSummaryCardTone = 'good' | 'warn' | 'bad';

export type MetricSummaryCardIcon =
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
  | 'reauth'
  | 'credential'
  | 'available'
  | 'attention'
  | 'quota-risk'
  | 'disabled'
  | 'unconfirmed'
  | 'anomaly'
  | 'key'
  | 'latency'
  | 'model'
  | 'trend';

export type MetricSummaryCardAccent =
  | 'blue'
  | 'green'
  | 'red'
  | 'amber'
  | 'indigo'
  | 'cyan'
  | 'violet'
  | 'teal'
  | 'neutral';

export type MetricSummaryDensity = 'default' | 'compact';

export type MetricSummaryCardProps = {
  label: string;
  fullLabel?: string;
  value: string;
  valueTitle?: string;
  meta: string;
  tone?: MetricSummaryCardTone;
  variant?: 'primary' | 'secondary';
  density?: MetricSummaryDensity;
  icon?: MetricSummaryCardIcon;
  accent?: MetricSummaryCardAccent;
  dataAttributes?: Record<`data-${string}`, string>;
  className?: string;
};

const summaryIconMap: Record<MetricSummaryCardIcon, ComponentType<IconProps>> = {
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
  credential: IconKey,
  available: IconShieldCheck,
  attention: IconTriangleAlert,
  'quota-risk': IconPercentCircle,
  disabled: IconBan,
  unconfirmed: IconCircleHelp,
  anomaly: IconShield,
  key: IconKey,
  latency: IconTimer,
  model: IconModelCluster,
  trend: IconTrendingUp,
};

const summaryAccentClassMap: Record<MetricSummaryCardAccent, string> = {
  blue: styles.accentBlue,
  green: styles.accentGreen,
  red: styles.accentRed,
  amber: styles.accentAmber,
  indigo: styles.accentIndigo,
  cyan: styles.accentCyan,
  violet: styles.accentViolet,
  teal: styles.accentTeal,
  neutral: styles.accentNeutral,
};

export function MetricSummaryCard({
  label,
  fullLabel,
  value,
  valueTitle,
  meta,
  tone,
  variant = 'primary',
  density = 'default',
  icon,
  accent = 'blue',
  dataAttributes,
  className = '',
}: MetricSummaryCardProps) {
  const Icon = icon ? summaryIconMap[icon] : null;
  const tooltipId = useId();
  const tooltipValue = valueTitle ?? value;
  const resolvedLabel = fullLabel ?? label;
  const isCompact = density === 'compact';
  const hasValueTooltip = !isCompact && tooltipValue !== value;

  const cardClassName = [
    'card',
    styles.card,
    variant === 'secondary' ? styles.cardSecondary : styles.cardPrimary,
    isCompact ? styles.cardCompact : '',
    summaryAccentClassMap[accent],
    className,
  ]
    .filter(Boolean)
    .join(' ');

  return (
    <div className={cardClassName} data-summary-icon={icon} {...dataAttributes}>
      <div data-metric-summary-header="true" className={styles.header}>
        {Icon ? (
          <span data-metric-summary-icon="true" className={styles.icon}>
            <Icon size={20} />
          </span>
        ) : null}
        <span data-metric-summary-label="true" className={styles.label} title={resolvedLabel}>
          {label}
        </span>
      </div>
      <div data-metric-summary-body="true" className={styles.body}>
        <span className={styles.valueWrap}>
          <strong
            data-metric-summary-value="true"
            className={`${styles.value} ${tone ? styles[`tone${tone}`] : ''}`}
            tabIndex={hasValueTooltip ? 0 : undefined}
            aria-describedby={hasValueTooltip ? tooltipId : undefined}
            title={valueTitle}
          >
            {value}
          </strong>
          {hasValueTooltip ? (
            <span id={tooltipId} className={styles.valueTooltip} role="tooltip">
              <span className={styles.valueTooltipLabel}>{resolvedLabel}</span>
              <span className={styles.valueTooltipValue}>{tooltipValue}</span>
            </span>
          ) : null}
        </span>
        {!isCompact ? (
          <span data-metric-summary-meta="true" className={styles.meta} title={meta}>
            {meta}
          </span>
        ) : null}
      </div>
      {!isCompact ? (
        <div data-metric-summary-chart="true" className={styles.chart} aria-hidden="true">
          <svg viewBox="0 0 100 30" preserveAspectRatio="none">
            <path d="M0,25 Q15,5 30,20 T60,10 T100,25" />
          </svg>
        </div>
      ) : null}
    </div>
  );
}

export function MetricSummaryGrid({
  cards,
  density = 'default',
  columns,
  className = '',
}: {
  cards: MetricSummaryCardProps[];
  density?: MetricSummaryDensity;
  columns?: number;
  className?: string;
}) {
  const isCompact = density === 'compact';
  const gridStyle = columns
    ? ({ '--metric-summary-columns': columns } as CSSProperties)
    : undefined;

  return (
    <div
      className={[styles.grid, isCompact ? styles.gridCompact : '', className]
        .filter(Boolean)
        .join(' ')}
      data-metric-summary-density={density}
      style={gridStyle}
    >
      {cards.map((card) => (
        <MetricSummaryCard key={`${card.label}-${card.value}`} {...card} density={density} />
      ))}
    </div>
  );
}
