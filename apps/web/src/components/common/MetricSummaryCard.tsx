import { useId, type ComponentType } from 'react';
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
  IconPercentCircle,
  IconRefreshCw,
  IconShield,
  IconShieldCheck,
  IconTrash2,
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
  | 'unconfirmed';

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

export type MetricSummaryCardProps = {
  label: string;
  fullLabel?: string;
  value: string;
  valueTitle?: string;
  meta: string;
  tone?: MetricSummaryCardTone;
  variant?: 'primary' | 'secondary';
  icon?: MetricSummaryCardIcon;
  accent?: MetricSummaryCardAccent;
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
  icon,
  accent = 'blue',
  className = '',
}: MetricSummaryCardProps) {
  const Icon = icon ? summaryIconMap[icon] : null;
  const tooltipId = useId();
  const tooltipValue = valueTitle ?? value;
  const resolvedLabel = fullLabel ?? label;
  const hasValueTooltip = tooltipValue !== value;

  const cardClassName = [
    'card',
    styles.card,
    variant === 'secondary' ? styles.cardSecondary : styles.cardPrimary,
    summaryAccentClassMap[accent],
    className,
  ]
    .filter(Boolean)
    .join(' ');

  return (
    <div className={cardClassName} data-summary-icon={icon}>
      <div className={styles.header}>
        {Icon ? (
          <span className={styles.icon}>
            <Icon size={20} />
          </span>
        ) : null}
        <span className={styles.label} title={resolvedLabel}>
          {label}
        </span>
      </div>
      <div className={styles.body}>
        <span className={styles.valueWrap}>
          <strong
            className={`${styles.value} ${tone ? styles[`tone${tone}`] : ''}`}
            tabIndex={hasValueTooltip ? 0 : undefined}
            aria-describedby={hasValueTooltip ? tooltipId : undefined}
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
        <span className={styles.meta} title={meta}>
          {meta}
        </span>
      </div>
      <div className={styles.chart} aria-hidden="true">
        <svg viewBox="0 0 100 30" preserveAspectRatio="none">
          <path d="M0,25 Q15,5 30,20 T60,10 T100,25" />
        </svg>
      </div>
    </div>
  );
}
