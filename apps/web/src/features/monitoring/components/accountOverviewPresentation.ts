import type { TFunction } from 'i18next';
import type { MonitoringAccountAuthState } from '@/features/monitoring/accountOverviewState';
import type { MonitoringAccountQuotaProvider } from '@/features/monitoring/accountOverviewQuotaTargets';
import type { MonitoringAccountRow } from '@/features/monitoring/hooks/useMonitoringData';
import { normalizePlanType } from '@/utils/quota';
import { formatCompactNumber, formatUsd } from '@/utils/usage';
import styles from '../MonitoringCenterPage.module.scss';

const PREMIUM_CODEX_PLAN_TYPES = new Set(['pro', 'prolite', 'pro-lite', 'pro_lite']);

export type AccountQuotaWindow = {
  id: string;
  label: string;
  remainingPercent: number | null;
  resetLabel: string;
  usageLabel: string | null;
};

export type AccountQuotaEntry = {
  key: string;
  provider: MonitoringAccountQuotaProvider;
  providerLabel: string;
  authLabel: string;
  fileName: string;
  planType: string | null;
  metaLabels?: string[];
  emptyMessage?: string;
  windows: AccountQuotaWindow[];
  error?: string;
  failedAtMs?: number;
  fetchedAtMs?: number;
  observedAtMs?: number;
  observedFromUsageHeaders?: boolean;
};

export type AccountQuotaState = {
  status: 'idle' | 'loading' | 'success' | 'error';
  targetKey: string;
  entries: AccountQuotaEntry[];
  error?: string;
  failedAtMs?: number;
  lastRefreshedAt?: number;
};

export type AccountSummaryMetric = {
  key: string;
  label: string;
  fullLabel?: string;
  value: string;
  valueClassName?: string;
};

export const formatPercent = (value: number) => `${(value * 100).toFixed(1)}%`;

const joinShort = (values: string[], limit = 2) => {
  if (values.length <= limit) {
    return values.join(', ');
  }
  return `${values.slice(0, limit).join(', ')} +${values.length - limit}`;
};

const shortLabel = (t: TFunction, shortKey: string, fallbackKey: string) => {
  const fallback = t(fallbackKey);
  const label = t(shortKey, { defaultValue: fallback });
  return label === shortKey ? fallback : label;
};

export const getCodexPlanLabel = (
  planType: string | null | undefined,
  t: TFunction
): string | null => {
  const normalized = normalizePlanType(planType);
  if (!normalized) return null;
  if (normalized === 'pro') return t('codex_quota.plan_pro');
  if (PREMIUM_CODEX_PLAN_TYPES.has(normalized) && normalized !== 'pro') {
    return t('codex_quota.plan_prolite');
  }
  if (normalized === 'plus') return t('codex_quota.plan_plus');
  if (normalized === 'team') return t('codex_quota.plan_team');
  if (normalized === 'free') return t('codex_quota.plan_free');
  return planType || normalized;
};

export const buildAccountSecondaryText = (row: MonitoringAccountRow) => {
  const primaryText = row.displayAccount || row.account;
  if (row.account && row.account !== primaryText) {
    return row.account;
  }

  const extraAuthLabels = row.authLabels.filter((label) => label && label !== primaryText);
  if (extraAuthLabels.length > 0) {
    return joinShort(extraAuthLabels, 2);
  }
  const extraChannels = row.channels.filter(
    (label) => label && label !== '-' && label !== primaryText
  );
  if (extraChannels.length > 0) {
    return joinShort(extraChannels, 2);
  }
  return '';
};

export const buildAccountSummaryMetrics = (
  row: MonitoringAccountRow,
  hasPrices: boolean,
  locale: string,
  t: TFunction
): AccountSummaryMetric[] => [
  {
    key: 'total-calls',
    label: shortLabel(t, 'monitoring.total_calls_short', 'monitoring.total_calls'),
    fullLabel: t('monitoring.total_calls'),
    value: formatCompactNumber(row.totalCalls),
  },
  {
    key: 'success-calls',
    label: shortLabel(t, 'monitoring.success_calls_short', 'monitoring.success_calls'),
    fullLabel: t('monitoring.success_calls'),
    value: formatCompactNumber(row.successCalls),
    valueClassName: styles.goodText,
  },
  {
    key: 'failure-calls',
    label: shortLabel(t, 'monitoring.failure_calls_short', 'monitoring.failure_calls'),
    fullLabel: t('monitoring.failure_calls'),
    value: formatCompactNumber(row.failureCalls),
    valueClassName: row.failureCalls > 0 ? styles.badText : undefined,
  },
  {
    key: 'total-tokens',
    label: shortLabel(t, 'monitoring.total_tokens_short', 'monitoring.total_tokens'),
    fullLabel: t('monitoring.total_tokens'),
    value: formatCompactNumber(row.totalTokens),
  },
  {
    key: 'input-tokens',
    label: shortLabel(t, 'monitoring.input_tokens_short', 'monitoring.input_tokens'),
    fullLabel: t('monitoring.input_tokens'),
    value: formatCompactNumber(row.inputTokens),
  },
  {
    key: 'output-tokens',
    label: shortLabel(t, 'monitoring.output_tokens_short', 'monitoring.output_tokens'),
    fullLabel: t('monitoring.output_tokens'),
    value: formatCompactNumber(row.outputTokens),
  },
  {
    key: 'cached-tokens',
    label: shortLabel(t, 'monitoring.cached_tokens_short', 'monitoring.cached_tokens'),
    fullLabel: t('monitoring.cached_tokens'),
    value: formatCompactNumber(row.cachedTokens),
  },
  {
    key: 'cache-creation-tokens',
    label: shortLabel(
      t,
      'monitoring.cache_creation_tokens_short',
      'monitoring.cache_creation_tokens'
    ),
    fullLabel: t('monitoring.cache_creation_tokens'),
    value: formatCompactNumber(row.cacheCreationTokens),
  },
  {
    key: 'cache-read-tokens',
    label: shortLabel(t, 'monitoring.cache_read_tokens_short', 'monitoring.cache_read_tokens'),
    fullLabel: t('monitoring.cache_read_tokens'),
    value: formatCompactNumber(row.cacheReadTokens),
  },
  {
    key: 'estimated-cost',
    label: shortLabel(t, 'monitoring.estimated_cost_short', 'monitoring.estimated_cost'),
    fullLabel: t('monitoring.estimated_cost'),
    value: hasPrices ? formatUsd(row.totalCost) : '--',
  },
  {
    key: 'latest-request-time',
    label: shortLabel(t, 'monitoring.latest_request_time_short', 'monitoring.latest_request_time'),
    fullLabel: t('monitoring.latest_request_time'),
    value: new Date(row.lastSeenAt).toLocaleString(locale),
  },
];

export const getAccountStatusTone = (authState: MonitoringAccountAuthState) => {
  switch (authState.enabledState) {
    case 'enabled':
      return 'enabled';
    case 'disabled':
      return 'disabled';
    case 'mixed':
      return 'mixed';
    case 'unavailable':
    default:
      return 'unavailable';
  }
};

export const getAccountStatusLabel = (authState: MonitoringAccountAuthState, t: TFunction) => {
  switch (authState.enabledState) {
    case 'enabled':
      return t('monitoring.account_overview_enabled_state_enabled');
    case 'disabled':
      return t('monitoring.account_overview_enabled_state_disabled');
    case 'mixed':
      return t('monitoring.account_overview_enabled_state_mixed');
    case 'unavailable':
    default:
      return t('monitoring.account_overview_enabled_state_unavailable');
  }
};

export const getAccountStatusDotClassName = (tone: string) => {
  switch (tone) {
    case 'enabled':
      return styles.accountStatusDotEnabled;
    case 'disabled':
      return styles.accountStatusDotDisabled;
    case 'mixed':
      return styles.accountStatusDotMixed;
    case 'unavailable':
    default:
      return styles.accountStatusDotUnavailable;
  }
};

export const getSuccessRateClassName = (rate: number) =>
  rate >= 0.95 ? styles.goodText : rate >= 0.85 ? styles.warnText : styles.badText;
