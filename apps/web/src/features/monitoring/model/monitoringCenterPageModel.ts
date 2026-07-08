import type { TFunction } from 'i18next';
import type {
  AntigravityQuotaGroup,
  AuthFileItem,
  ClaudeQuotaWindow,
  CodexQuotaWindow,
  KimiQuotaRow,
  XaiBillingSummary,
} from '@/types';
import type { UsageHeaderSnapshot } from '@/services/api/usageService';
import type {
  MonitoringAccountRow,
  MonitoringApiKeyRow,
  MonitoringEventRow,
  MonitoringSummary,
} from '@/features/monitoring/hooks/useMonitoringData';
import {
  resolveAccountDisplayText,
  type AccountDisplayMode,
  type AccountSortKey,
} from '@/features/monitoring/accountOverviewState';
import type { MonitoringCenterUiState } from '@/features/monitoring/monitoringCenterUiState';
import type {
  AccountQuotaEntry,
  AccountQuotaState,
  AccountQuotaWindow,
} from '@/features/monitoring/components/accountOverviewPresentation';
import {
  formatPercent,
  getCodexPlanLabel,
} from '@/features/monitoring/components/accountOverviewPresentation';
import type { SummaryCardProps } from '@/features/monitoring/components/MonitoringShared';
import type {
  MonitoringAccountQuotaProvider,
  MonitoringAccountQuotaTarget,
} from '@/features/monitoring/accountOverviewQuotaTargets';
import { formatStatusWindowLabel } from '@/features/monitoring/model/statusWindow';
import {
  fetchAntigravityQuota,
  fetchClaudeQuota,
  fetchCodexQuota,
  fetchKimiQuota,
  fetchXaiQuota,
  buildCodexQuotaWindowInfos,
  formatKimiResetHint,
  formatQuotaResetTime,
} from '@/utils/quota';
import {
  buildObservedCodexQuotaFromHeaderSnapshot,
  getHeaderSnapshotErrorCode,
  getHeaderSnapshotErrorKind,
  getHeaderSnapshotPlanType,
  getHeaderSnapshotRecoverAtMs,
  getHeaderSnapshotTraceId,
  getHeaderSnapshotUsedPercent,
  hasUsageHeaderQuotaSignal,
} from '@/utils/usageHeaderSnapshots';
import {
  formatCompactNumber,
  formatDurationMs,
  formatUsd,
  normalizeAuthIndex,
  type ModelPrice,
} from '@/utils/usage';

export type StatusFilter = 'all' | 'success' | 'failed';

export type FocusSnapshot = {
  searchInput: string;
  selectedAccount: string;
  selectedProvider: string;
  selectedModel: string;
  selectedChannel: string;
  selectedApiKeyHash: string;
  selectedStatus: StatusFilter;
  selectedHeaderTraceId: string;
};

export type PriceDraft = {
  prompt: string;
  completion: string;
  cache: string;
};

export type RealtimeLogRow = MonitoringEventRow & {
  requestCount: number;
  successRate: number;
  streamKey: string;
  recentPattern: boolean[];
};

export type AccountOverviewColumn = {
  key: string;
  label: string;
  fullLabel?: string;
  sortKey?: AccountSortKey;
};

export type MonitoringOption = {
  value: string;
  label: string;
};

export type PaginationState<T> = {
  currentPage: number;
  totalPages: number;
  pageItems: T[];
  startItem: number;
  endItem: number;
};

const padDateUnit = (value: number) => String(value).padStart(2, '0');

export const formatDateTimeLocalValue = (date: Date) =>
  `${date.getFullYear()}-${padDateUnit(date.getMonth() + 1)}-${padDateUnit(date.getDate())}T${padDateUnit(date.getHours())}:${padDateUnit(date.getMinutes())}`;

export const getTodayStartInputValue = () => {
  const date = new Date();
  date.setHours(0, 0, 0, 0);
  return formatDateTimeLocalValue(date);
};

export const getCurrentInputValue = () => formatDateTimeLocalValue(new Date());

const formatFullNumber = (value: number, locale?: string) => {
  const num = Number(value);
  if (!Number.isFinite(num)) return '0';

  try {
    return new Intl.NumberFormat(locale || undefined, {
      maximumFractionDigits: 0,
    }).format(num);
  } catch {
    return String(Math.round(num));
  }
};

export const parseDateTimeLocalValue = (value: string) => {
  if (!value) return null;
  const timestamp = new Date(value).getTime();
  return Number.isFinite(timestamp) ? timestamp : null;
};

const parseQueryTimestamp = (params: URLSearchParams, key: string) => {
  const value = Number(params.get(key));
  return Number.isFinite(value) && value > 0 ? value : null;
};

export const buildMonitoringInitialStateFromQuery = (
  search: string,
  state: MonitoringCenterUiState
): MonitoringCenterUiState => {
  const params = new URLSearchParams(search);
  const fromMs = parseQueryTimestamp(params, 'from_ms');
  const toMs = parseQueryTimestamp(params, 'to_ms');
  const model = params.get('model')?.trim();
  const apiKeyHash = params.get('api_key_hash')?.trim();
  const status = params.get('status')?.trim();
  const provider = params.get('provider')?.trim();
  const authFile = params.get('auth_file')?.trim();
  const projectId = params.get('project_id')?.trim();
  const requestType = params.get('request_type')?.trim();
  const searchQuery = params.get('search')?.trim();
  const minLatencyMs = params.get('min_latency_ms')?.trim();
  const cacheStatus = params.get('cache_status')?.trim();
  const headerTraceId = params.get('header_trace_id')?.trim();
  const hasRange = fromMs !== null && toMs !== null && fromMs < toMs;
  const hasStructuredScopeFilter = Boolean(
    authFile ||
    projectId ||
    requestType ||
    minLatencyMs ||
    cacheStatus ||
    headerTraceId
  );

  return {
    ...state,
    timeRange: hasRange ? 'custom' : state.timeRange,
    customStartInput: hasRange
      ? formatDateTimeLocalValue(new Date(fromMs))
      : state.customStartInput,
    customEndInput: hasRange ? formatDateTimeLocalValue(new Date(toMs)) : state.customEndInput,
    selectedModel: model || state.selectedModel,
    selectedProvider: provider || state.selectedProvider,
    selectedApiKeyHash: apiKeyHash || state.selectedApiKeyHash,
    selectedHeaderTraceId: headerTraceId || state.selectedHeaderTraceId,
    selectedStatus:
      status === 'success' || status === 'failed' || status === 'all'
        ? status
        : state.selectedStatus,
    searchInput: searchQuery || state.searchInput,
    activeDataTab:
      hasRange ||
      model ||
      apiKeyHash ||
      status ||
      provider ||
      searchQuery ||
      hasStructuredScopeFilter
        ? 'realtime'
        : state.activeDataTab,
  };
};

export const ensureSelectedOption = <T extends { value: string; label: string }>(
  options: T[],
  value: string,
  label = value
): T[] => {
  if (!value || value === 'all' || options.some((option) => option.value === value)) {
    return options;
  }
  return [...options, { value, label } as T];
};

const buildSortedValueOptions = (values: string[]): MonitoringOption[] =>
  Array.from(new Set(values))
    .filter(Boolean)
    .sort((left, right) => left.localeCompare(right))
    .map((value) => ({ value, label: value }));

const shortLabel = (
  t: TFunction,
  shortKey: string,
  fallbackKey: string,
  options?: Record<string, unknown>
) => {
  const fallback = t(fallbackKey, options);
  const label = t(shortKey, { ...(options ?? {}), defaultValue: fallback });
  return label === shortKey ? fallback : label;
};

export const buildProviderOptions = (
  rows: MonitoringEventRow[],
  selectedProvider: string,
  t: TFunction
) =>
  buildProviderOptionsFromValues(
    rows.map((row) => row.provider),
    selectedProvider,
    t
  );

export const buildProviderOptionsFromValues = (
  providers: string[],
  selectedProvider: string,
  t: TFunction
) =>
  ensureSelectedOption(
    [
      {
        value: 'all',
        label: shortLabel(
          t,
          'monitoring.filter_all_providers_short',
          'monitoring.filter_all_providers'
        ),
      },
      ...buildSortedValueOptions(providers),
    ],
    selectedProvider
  );

export const buildAccountOptions = (
  rows: MonitoringAccountRow[],
  selectedAccount: string,
  t: TFunction,
  accountDisplayMode: AccountDisplayMode = 'masked'
) =>
  ensureSelectedOption(
    [
      {
        value: 'all',
        label: shortLabel(
          t,
          'monitoring.filter_all_accounts_short',
          'monitoring.filter_all_accounts'
        ),
      },
      ...Array.from(
        new Map(
          rows.map((row) => [
            row.filterValue || row.account,
            buildAccountOptionLabel(row, accountDisplayMode),
          ])
        ).entries()
      )
        .sort((left, right) => left[1].localeCompare(right[1]))
        .map(([value, label]) => ({ value, label })),
    ],
    selectedAccount
  );

export const buildModelOptions = (
  rows: MonitoringEventRow[],
  selectedModel: string,
  t: TFunction
) =>
  buildModelOptionsFromValues(
    rows.map((row) => row.model),
    selectedModel,
    t
  );

export const buildModelOptionsFromValues = (
  models: string[],
  selectedModel: string,
  t: TFunction
) =>
  ensureSelectedOption(
    [
      {
        value: 'all',
        label: shortLabel(t, 'monitoring.filter_all_models_short', 'monitoring.filter_all_models'),
      },
      ...buildSortedValueOptions(models),
    ],
    selectedModel
  );

export const buildChannelOptions = (
  rows: MonitoringEventRow[],
  selectedChannel: string,
  t: TFunction
) =>
  buildChannelOptionsFromValues(
    rows.map((row) => row.channel),
    selectedChannel,
    t
  );

export const buildChannelOptionsFromValues = (
  channels: string[],
  selectedChannel: string,
  t: TFunction
) =>
  ensureSelectedOption(
    [
      {
        value: 'all',
        label: shortLabel(
          t,
          'monitoring.filter_all_channels_short',
          'monitoring.filter_all_channels'
        ),
      },
      ...buildSortedValueOptions(channels),
    ],
    selectedChannel
  );

const buildApiKeyOptionsFromMap = (
  optionMap: Map<string, string>,
  selectedApiKeyHash: string,
  t: TFunction
) =>
  ensureSelectedOption(
    [
      {
        value: 'all',
        label: shortLabel(
          t,
          'monitoring.filter_all_api_keys_short',
          'monitoring.filter_all_api_keys'
        ),
      },
      ...Array.from(optionMap.entries())
        .sort((left, right) => left[1].localeCompare(right[1]))
        .map(([value, label]) => ({ value, label })),
    ],
    selectedApiKeyHash,
    selectedApiKeyHash
  );

export const buildApiKeyOptions = (
  rows: MonitoringEventRow[],
  selectedApiKeyHash: string,
  t: TFunction
) => {
  const optionMap = new Map<string, string>();
  rows.forEach((row) => {
    if (!row.apiKeyHash || optionMap.has(row.apiKeyHash)) return;
    optionMap.set(row.apiKeyHash, row.apiKeyLabel || row.apiKeyMasked || row.apiKeyHash);
  });

  return buildApiKeyOptionsFromMap(optionMap, selectedApiKeyHash, t);
};

export const buildApiKeyOptionsFromRows = (
  rows: MonitoringApiKeyRow[],
  selectedApiKeyHash: string,
  t: TFunction
) => {
  const optionMap = new Map<string, string>();
  rows.forEach((row) => {
    if (!row.apiKeyHash || optionMap.has(row.apiKeyHash)) return;
    optionMap.set(row.apiKeyHash, row.apiKeyLabel || row.apiKeyMasked || row.apiKeyHash);
  });

  return buildApiKeyOptionsFromMap(optionMap, selectedApiKeyHash, t);
};

export const buildStatusOptions = (t: TFunction): MonitoringOption[] => [
  {
    value: 'all',
    label: shortLabel(t, 'monitoring.filter_all_statuses_short', 'monitoring.filter_all_statuses'),
  },
  {
    value: 'success',
    label: shortLabel(
      t,
      'monitoring.filter_status_success_short',
      'monitoring.filter_status_success'
    ),
  },
  {
    value: 'failed',
    label: shortLabel(
      t,
      'monitoring.filter_status_failed_short',
      'monitoring.filter_status_failed'
    ),
  },
];

export const buildSyncPriceModels = (
  rows: MonitoringEventRow[],
  modelPrices: Record<string, ModelPrice>
) =>
  Array.from(
    new Set([
      ...rows.map((row) => row.model),
      ...rows.map((row) => row.resolvedModel ?? ''),
      ...Object.keys(modelPrices),
    ])
  )
    .filter(Boolean)
    .sort((left, right) => left.localeCompare(right));

export const buildPriceModelOptions = (models: string[], t: TFunction): MonitoringOption[] => [
  { value: '', label: t('usage_stats.model_price_select_placeholder') },
  ...models.map((value) => ({ value, label: value })),
];

export const buildAuthFilesByAuthIndex = (authFiles: AuthFileItem[]) => {
  const map = new Map<string, AuthFileItem>();
  authFiles.forEach((file) => {
    const authIndex = normalizeAuthIndex(file['auth_index'] ?? file.authIndex);
    if (!authIndex || map.has(authIndex)) return;
    map.set(authIndex, file);
  });
  return map;
};

export const buildAccountOverviewColumns = (t: TFunction): AccountOverviewColumn[] => [
  {
    key: 'account',
    label: shortLabel(
      t,
      'monitoring.account_overview_col_account_short',
      'monitoring.account_overview_col_account'
    ),
    fullLabel: t('monitoring.account_overview_col_account'),
  },
  { key: 'status', label: t('monitoring.column_status') },
  {
    key: 'total-calls',
    label: shortLabel(t, 'monitoring.total_calls_short', 'monitoring.total_calls'),
    fullLabel: t('monitoring.total_calls'),
    sortKey: 'totalCalls',
  },
  {
    key: 'success-calls',
    label: shortLabel(t, 'monitoring.success_calls_short', 'monitoring.success_calls'),
    fullLabel: t('monitoring.success_calls'),
    sortKey: 'successCalls',
  },
  {
    key: 'failure-calls',
    label: shortLabel(t, 'monitoring.failure_calls_short', 'monitoring.failure_calls'),
    fullLabel: t('monitoring.failure_calls'),
    sortKey: 'failureCalls',
  },
  {
    key: 'success-rate',
    label: shortLabel(t, 'monitoring.column_success_rate_short', 'monitoring.column_success_rate'),
    fullLabel: t('monitoring.column_success_rate'),
    sortKey: 'successRate',
  },
  {
    key: 'total-tokens',
    label: shortLabel(t, 'monitoring.total_tokens_short', 'monitoring.total_tokens'),
    fullLabel: t('monitoring.total_tokens'),
    sortKey: 'totalTokens',
  },
  {
    key: 'estimated-cost',
    label: shortLabel(
      t,
      'monitoring.account_overview_col_cost_short',
      'monitoring.account_overview_col_cost'
    ),
    fullLabel: t('monitoring.account_overview_col_cost'),
    sortKey: 'totalCost',
  },
  {
    key: 'latest-request-time',
    label: shortLabel(t, 'monitoring.latest_request_time_short', 'monitoring.latest_request_time'),
    fullLabel: t('monitoring.latest_request_time'),
    sortKey: 'lastSeenAt',
  },
  { key: 'action', label: t('common.action') },
];

export const buildApiKeyOverviewColumns = (t: TFunction): AccountOverviewColumn[] => [
  {
    key: 'api-key',
    label: shortLabel(
      t,
      'monitoring.api_key_summary_col_key_short',
      'monitoring.api_key_summary_col_key'
    ),
    fullLabel: t('monitoring.api_key_summary_col_key'),
  },
  {
    key: 'total-calls',
    label: shortLabel(t, 'monitoring.total_calls_short', 'monitoring.total_calls'),
    fullLabel: t('monitoring.total_calls'),
  },
  {
    key: 'success-calls',
    label: shortLabel(t, 'monitoring.success_calls_short', 'monitoring.success_calls'),
    fullLabel: t('monitoring.success_calls'),
  },
  {
    key: 'failure-calls',
    label: shortLabel(t, 'monitoring.failure_calls_short', 'monitoring.failure_calls'),
    fullLabel: t('monitoring.failure_calls'),
  },
  {
    key: 'total-tokens',
    label: shortLabel(t, 'monitoring.total_tokens_short', 'monitoring.total_tokens'),
    fullLabel: t('monitoring.total_tokens'),
  },
  {
    key: 'estimated-cost',
    label: shortLabel(
      t,
      'monitoring.account_overview_col_cost_short',
      'monitoring.account_overview_col_cost'
    ),
    fullLabel: t('monitoring.account_overview_col_cost'),
  },
  {
    key: 'latest-request-time',
    label: shortLabel(t, 'monitoring.latest_request_time_short', 'monitoring.latest_request_time'),
    fullLabel: t('monitoring.latest_request_time'),
  },
];

export const buildAccountSortOptions = (
  columns: AccountOverviewColumn[],
  t: TFunction
): MonitoringOption[] => {
  const prefix = t('monitoring.account_overview_sort_prefix');
  return columns
    .filter((column): column is AccountOverviewColumn & { sortKey: AccountSortKey } =>
      Boolean(column.sortKey)
    )
    .map((column) => ({
      value: column.sortKey,
      label: `${prefix}${column.label}`,
    }));
};

export const buildPrimarySummaryCards = ({
  summary,
  accountCount,
  failedGroupCount,
  hasPrices,
  locale,
  t,
}: {
  summary: MonitoringSummary;
  accountCount: number;
  failedGroupCount: number;
  hasPrices: boolean;
  locale: string;
  t: TFunction;
}): SummaryCardProps[] => [
  {
    label: shortLabel(t, 'monitoring.total_calls_short', 'monitoring.total_calls'),
    fullLabel: t('monitoring.total_calls'),
    value: formatCompactNumber(summary.totalCalls),
    valueTitle: formatFullNumber(summary.totalCalls, locale),
    meta: `${accountCount} ${t('monitoring.accounts_suffix')}`,
    icon: 'calls',
    accent: 'blue',
  },
  {
    label: shortLabel(t, 'monitoring.call_success_rate_short', 'monitoring.call_success_rate'),
    fullLabel: t('monitoring.call_success_rate'),
    value: formatPercent(summary.successRate),
    meta: formatDurationMs(summary.averageLatencyMs, { locale }),
    tone: summary.successRate >= 0.95 ? 'good' : summary.successRate >= 0.85 ? 'warn' : 'bad',
    icon: 'success',
    accent: 'green',
  },
  {
    label: shortLabel(t, 'monitoring.failure_calls_short', 'monitoring.failure_calls'),
    fullLabel: t('monitoring.failure_calls'),
    value: formatCompactNumber(summary.failureCalls),
    valueTitle: formatFullNumber(summary.failureCalls, locale),
    meta: `${failedGroupCount} ${t('monitoring.groups_suffix')}`,
    tone: summary.failureCalls > 0 ? 'bad' : 'good',
    icon: 'failure',
    accent: 'red',
  },
  {
    label: shortLabel(t, 'monitoring.estimated_cost_short', 'monitoring.estimated_cost'),
    fullLabel: t('monitoring.estimated_cost'),
    value: hasPrices ? formatUsd(summary.totalCost) : '--',
    valueTitle: hasPrices ? formatUsd(summary.totalCost) : undefined,
    meta: hasPrices ? t('monitoring.estimated_cost_hint') : t('monitoring.estimated_cost_missing'),
    tone: hasPrices ? undefined : 'warn',
    icon: 'cost',
    accent: 'amber',
  },
];

export const buildSecondarySummaryCards = (
  summary: MonitoringSummary,
  locale: string,
  t: TFunction
): SummaryCardProps[] => {
  const totalCacheTokens =
    summary.cachedTokens + summary.cacheCreationTokens + summary.cacheReadTokens;
  const cacheHitTokens = summary.cachedTokens + summary.cacheReadTokens;
  const inputSideTokens =
    Math.max(summary.inputTokens, summary.cachedTokens) +
    summary.cacheReadTokens +
    summary.cacheCreationTokens;
  const cacheHitRate = inputSideTokens > 0 ? cacheHitTokens / inputSideTokens : 0;

  return [
    {
      label: shortLabel(t, 'monitoring.total_tokens_short', 'monitoring.total_tokens'),
      fullLabel: t('monitoring.total_tokens'),
      value: formatCompactNumber(summary.totalTokens),
      valueTitle: formatFullNumber(summary.totalTokens, locale),
      meta: `${t('monitoring.reasoning_tokens')} ${formatCompactNumber(summary.reasoningTokens)}`,
      variant: 'secondary',
      icon: 'tokens',
      accent: 'indigo',
    },
    {
      label: shortLabel(t, 'monitoring.input_tokens_short', 'monitoring.input_tokens'),
      fullLabel: t('monitoring.input_tokens'),
      value: formatCompactNumber(summary.inputTokens),
      valueTitle: formatFullNumber(summary.inputTokens, locale),
      meta: `${t('monitoring.of_token_mix')} ${formatPercent(summary.totalTokens > 0 ? summary.inputTokens / summary.totalTokens : 0)}`,
      variant: 'secondary',
      icon: 'input',
      accent: 'cyan',
    },
    {
      label: shortLabel(t, 'monitoring.output_tokens_short', 'monitoring.output_tokens'),
      fullLabel: t('monitoring.output_tokens'),
      value: formatCompactNumber(summary.outputTokens),
      valueTitle: formatFullNumber(summary.outputTokens, locale),
      meta: `${t('monitoring.of_token_mix')} ${formatPercent(summary.totalTokens > 0 ? summary.outputTokens / summary.totalTokens : 0)}`,
      variant: 'secondary',
      icon: 'output',
      accent: 'violet',
    },
    {
      label: shortLabel(t, 'monitoring.cached_tokens_short', 'monitoring.cached_tokens'),
      fullLabel: t('monitoring.cached_tokens'),
      value: formatCompactNumber(totalCacheTokens),
      valueTitle: formatFullNumber(totalCacheTokens, locale),
      meta: `${t('monitoring.cache_hit_rate')} ${formatPercent(cacheHitRate)}`,
      variant: 'secondary',
      icon: 'cache',
      accent: 'teal',
    },
  ];
};

export const isUsageImportFile = (file: File) => {
  const normalizedName = file.name.toLowerCase();
  const normalizedType = file.type.toLowerCase();
  return (
    /\.(json|jsonl|ndjson|txt)$/.test(normalizedName) ||
    normalizedType === 'application/json' ||
    normalizedType === 'application/x-ndjson' ||
    normalizedType === 'text/plain'
  );
};

export const buildPaginationState = <T>(
  items: readonly T[],
  page: number,
  pageSize: number
): PaginationState<T> => {
  const safePageSize = Math.max(1, pageSize);
  const totalPages = Math.max(1, Math.ceil(items.length / safePageSize));
  const currentPage = Math.min(Math.max(1, page), totalPages);
  const startIndex = (currentPage - 1) * safePageSize;
  const endIndex = Math.min(startIndex + safePageSize, items.length);

  return {
    currentPage,
    totalPages,
    pageItems: items.slice(startIndex, endIndex),
    startItem: items.length > 0 ? startIndex + 1 : 0,
    endItem: endIndex,
  };
};

export const createPriceDraft = (price?: ModelPrice): PriceDraft => ({
  prompt: price ? String(price.prompt) : '',
  completion: price ? String(price.completion) : '',
  cache: price ? String(price.cache) : '',
});

export const parsePriceValue = (value: string) => {
  const parsed = Number.parseFloat(value);
  return Number.isFinite(parsed) && parsed >= 0 ? parsed : 0;
};

export const buildAccountOptionLabel = (
  row: MonitoringAccountRow,
  accountDisplayMode: AccountDisplayMode = 'masked'
) => {
  const display = resolveAccountDisplayText(row, accountDisplayMode);
  if (!display.secondary || display.secondary === display.primary) {
    return display.primary;
  }
  return `${display.primary} / ${display.secondary}`;
};

const clampRemainingPercent = (value: number | null | undefined): number | null =>
  value === null || value === undefined ? null : Math.max(0, Math.min(100, value));

const buildRemainingFromUsedPercent = (usedPercent: number | null | undefined) => {
  const clampedUsed = clampRemainingPercent(usedPercent);
  return clampedUsed === null ? null : Math.max(0, 100 - clampedUsed);
};

const formatQuotaWindowDurationValue = (value: number) =>
  Number.isInteger(value) ? value.toFixed(0) : value.toFixed(1);

const formatCodexQuotaWindowDuration = (seconds: number) => {
  const daySeconds = 86_400;
  const hourSeconds = 3_600;
  if (seconds >= daySeconds) {
    return `${formatQuotaWindowDurationValue(seconds / daySeconds)}d`;
  }
  return `${formatQuotaWindowDurationValue(seconds / hourSeconds)}h`;
};

const buildCodexAccountQuotaWindows = (
  windows: CodexQuotaWindow[],
  t: TFunction
): AccountQuotaWindow[] =>
  windows.map((window) => {
    const clampedUsed = clampRemainingPercent(window.usedPercent);
    const remainingPercent = buildRemainingFromUsedPercent(window.usedPercent);
    let usageLabel: string | null = null;

    if (
      window.limitWindowSeconds !== null &&
      window.limitWindowSeconds !== undefined &&
      window.limitWindowSeconds > 0 &&
      clampedUsed !== null
    ) {
      const usedSeconds = (window.limitWindowSeconds * clampedUsed) / 100;
      usageLabel = t('codex_quota.window_usage_duration', {
        used: formatCodexQuotaWindowDuration(usedSeconds),
        total: formatCodexQuotaWindowDuration(window.limitWindowSeconds),
      });
    }

    return {
      id: window.id,
      label: window.labelKey
        ? t(window.labelKey, window.labelParams as Record<string, string | number>)
        : window.label,
      remainingPercent,
      resetLabel: window.resetLabel,
      usageLabel,
    };
  });

const hasKnownAccountQuotaResetLabel = (value: unknown): value is string => {
  if (typeof value !== 'string') return false;
  const trimmed = value.trim();
  return trimmed !== '' && trimmed !== '-';
};

const readFiniteTimestamp = (value: unknown): number | null =>
  typeof value === 'number' && Number.isFinite(value) ? value : null;

const mergeAccountQuotaWindow = (
  activeWindow: AccountQuotaWindow,
  observedWindow: AccountQuotaWindow
): AccountQuotaWindow => ({
  ...activeWindow,
  ...(observedWindow.label.trim() ? { label: observedWindow.label } : {}),
  ...(observedWindow.remainingPercent !== null &&
  observedWindow.remainingPercent !== undefined &&
  Number.isFinite(observedWindow.remainingPercent)
    ? { remainingPercent: observedWindow.remainingPercent }
    : {}),
  ...(hasKnownAccountQuotaResetLabel(observedWindow.resetLabel)
    ? { resetLabel: observedWindow.resetLabel }
    : {}),
  ...(observedWindow.usageLabel && observedWindow.usageLabel.trim()
    ? { usageLabel: observedWindow.usageLabel }
    : {}),
});

const mergeAccountQuotaWindows = (
  activeWindows: AccountQuotaWindow[],
  observedWindows: AccountQuotaWindow[]
): AccountQuotaWindow[] => {
  if (observedWindows.length === 0) return activeWindows;
  if (activeWindows.length === 0) return observedWindows;

  const observedById = new Map(observedWindows.map((window) => [window.id, window]));
  const mergedWindows = activeWindows.map((window) => {
    const observedWindow = observedById.get(window.id);
    if (!observedWindow) return window;
    observedById.delete(window.id);
    return mergeAccountQuotaWindow(window, observedWindow);
  });

  return [...mergedWindows, ...observedById.values()];
};

const mergeAccountQuotaMetaLabels = (
  activeLabels: string[] | undefined,
  observedLabels: string[] | undefined
) => {
  const labels: string[] = [];
  [...(activeLabels ?? []), ...(observedLabels ?? [])].forEach((label) => {
    const trimmed = label.trim();
    if (!trimmed || labels.includes(trimmed)) return;
    labels.push(trimmed);
  });
  return labels.length > 0 ? labels : undefined;
};

const mergeAccountQuotaEntryMetaLabels = (
  activeEntry: AccountQuotaEntry,
  observedEntry: AccountQuotaEntry
) => {
  if (
    observedEntry.planType &&
    observedEntry.planType !== activeEntry.planType &&
    observedEntry.metaLabels &&
    observedEntry.metaLabels.length > 0
  ) {
    return mergeAccountQuotaMetaLabels(undefined, observedEntry.metaLabels);
  }

  return mergeAccountQuotaMetaLabels(activeEntry.metaLabels, observedEntry.metaLabels);
};

const getMergeableAccountQuotaEntry = (
  entry: AccountQuotaEntry | undefined
): AccountQuotaEntry | undefined => {
  if (!entry?.error) return entry;
  const mergeableEntry = { ...entry };
  delete mergeableEntry.error;
  return mergeableEntry;
};

export const mergeObservedAccountQuotaEntry = (
  activeEntry: AccountQuotaEntry | undefined,
  observedEntry: AccountQuotaEntry | null
): AccountQuotaEntry | null => {
  const mergeableActiveEntry = getMergeableAccountQuotaEntry(activeEntry);
  if (!mergeableActiveEntry) return observedEntry;
  if (!observedEntry || observedEntry.error) return mergeableActiveEntry;

  return {
    ...mergeableActiveEntry,
    planType: observedEntry.planType ?? mergeableActiveEntry.planType,
    metaLabels: mergeAccountQuotaEntryMetaLabels(mergeableActiveEntry, observedEntry),
    windows: mergeAccountQuotaWindows(mergeableActiveEntry.windows, observedEntry.windows),
    fetchedAtMs: mergeableActiveEntry.fetchedAtMs,
    observedAtMs: observedEntry.observedAtMs ?? mergeableActiveEntry.observedAtMs,
    observedFromUsageHeaders:
      observedEntry.observedFromUsageHeaders ?? mergeableActiveEntry.observedFromUsageHeaders,
  };
};

const isObservedAccountQuotaNewerThanFailure = (
  failedAtMs: number | undefined,
  observedEntry: AccountQuotaEntry | null | undefined
) => {
  const failureTime = readFiniteTimestamp(failedAtMs);
  const observedTime = readFiniteTimestamp(observedEntry?.observedAtMs);
  return failureTime !== null && observedTime !== null && observedTime > failureTime;
};

const clearAccountQuotaEntryFailure = (entry: AccountQuotaEntry): AccountQuotaEntry => {
  const recovered = { ...entry };
  delete recovered.error;
  delete recovered.failedAtMs;
  return recovered;
};

export const buildAccountQuotaRefreshFailureEntry = (
  target: MonitoringAccountQuotaTarget,
  error: string,
  t: TFunction,
  activeEntry?: AccountQuotaEntry,
  observedEntry?: AccountQuotaEntry | null,
  failedAtMs = Date.now()
): AccountQuotaEntry => {
  const mergeableActiveEntry = getMergeableAccountQuotaEntry(activeEntry);
  const mergedEntry =
    mergeObservedAccountQuotaEntry(mergeableActiveEntry, observedEntry ?? null) ??
    observedEntry ??
    mergeableActiveEntry ??
    null;

  if (!mergedEntry) {
    return {
      ...buildAccountQuotaErrorEntry(target, error, t),
      failedAtMs,
    };
  }

  return {
    ...mergedEntry,
    error,
    failedAtMs,
  };
};

export const mergeObservedAccountQuotaState = (
  state: AccountQuotaState | undefined,
  targets: MonitoringAccountQuotaTarget[],
  observedEntries: AccountQuotaEntry[]
): AccountQuotaState | undefined => {
  if (!state || state.status === 'loading' || observedEntries.length === 0) return state;

  const targetKey = targets.map((target) => target.key).join('|');
  if (state.targetKey !== targetKey) return state;

  const observedByKey = new Map(observedEntries.map((entry) => [entry.key, entry]));
  const activeKeys = new Set(state.entries.map((entry) => entry.key));
  let changed = false;

  const entries = state.entries.map((entry) => {
    const observedEntry = observedByKey.get(entry.key);
    if (!observedEntry) return entry;

    const mergedEntry = mergeObservedAccountQuotaEntry(entry, observedEntry) ?? entry;
    const nextEntry = entry.error
      ? isObservedAccountQuotaNewerThanFailure(entry.failedAtMs, observedEntry)
        ? clearAccountQuotaEntryFailure(mergedEntry)
        : { ...mergedEntry, error: entry.error, failedAtMs: entry.failedAtMs }
      : mergedEntry;
    changed = changed || nextEntry !== entry;
    return nextEntry;
  });

  const targetKeys = new Set(targets.map((target) => target.key));
  observedEntries.forEach((observedEntry) => {
    if (!targetKeys.has(observedEntry.key) || activeKeys.has(observedEntry.key)) return;

    if (state.status === 'error' && !isObservedAccountQuotaNewerThanFailure(state.failedAtMs, observedEntry)) {
      if (!state.error) return;
      entries.push({ ...observedEntry, error: state.error, failedAtMs: state.failedAtMs });
    } else {
      entries.push(observedEntry);
    }
    changed = true;
  });

  if (!changed) return state;

  const firstError = entries.find((entry) => entry.error)?.error;
  return {
    ...state,
    status: firstError ? 'error' : 'success',
    entries,
    error: firstError || '',
    failedAtMs: firstError ? state.failedAtMs : undefined,
  };
};

const buildClaudeAccountQuotaWindows = (
  windows: ClaudeQuotaWindow[],
  t: TFunction
): AccountQuotaWindow[] =>
  windows.map((window) => ({
    id: window.id,
    label: window.labelKey ? t(window.labelKey) : window.label,
    remainingPercent: buildRemainingFromUsedPercent(window.usedPercent),
    resetLabel: window.resetLabel,
    usageLabel: null,
  }));

const buildAntigravityAccountQuotaWindows = (
  groups: AntigravityQuotaGroup[]
): AccountQuotaWindow[] =>
  groups
    .map((group): AccountQuotaWindow | null => {
      if (group.buckets.length === 0) return null;
      const remainingFraction = Math.min(
        ...group.buckets.map((bucket) => bucket.remainingFraction)
      );
      const resetTime = group.buckets.reduce<string | undefined>((current, bucket) => {
        if (!current) return bucket.resetTime;
        if (!bucket.resetTime) return current;
        const currentTime = new Date(current).getTime();
        const nextTime = new Date(bucket.resetTime).getTime();
        if (Number.isNaN(currentTime)) return bucket.resetTime;
        if (Number.isNaN(nextTime)) return current;
        return currentTime <= nextTime ? current : bucket.resetTime;
      }, undefined);

      return {
        id: group.id,
        label: group.label,
        remainingPercent: clampRemainingPercent(remainingFraction * 100),
        resetLabel: formatQuotaResetTime(resetTime),
        usageLabel: null,
      };
    })
    .filter((window): window is AccountQuotaWindow => window !== null);

const buildKimiAccountQuotaWindows = (rows: KimiQuotaRow[], t: TFunction): AccountQuotaWindow[] =>
  rows.map((row) => {
    const limit = row.limit;
    const used = row.used;
    const remainingPercent =
      limit > 0
        ? clampRemainingPercent(Math.round(((limit - used) / limit) * 100))
        : used > 0
          ? 0
          : null;
    const rowLabel = row.labelKey
      ? t(row.labelKey, (row.labelParams ?? {}) as Record<string, string | number>)
      : (row.label ?? '');
    const resetLabel = formatKimiResetHint(t, row.resetHint);

    return {
      id: row.id,
      label: rowLabel,
      remainingPercent,
      resetLabel: resetLabel || '-',
      usageLabel: null,
    };
  });

const formatXaiCurrency = (value: number | null): string => {
  if (value === null) return '--';
  return `$${(value / 100).toFixed(2)}`;
};

const buildXaiAccountQuotaWindows = (
  billing: XaiBillingSummary,
  t: TFunction
): AccountQuotaWindow[] => {
  const remainingCents =
    billing.monthlyLimitCents !== null && billing.includedUsedCents !== null
      ? Math.max(0, billing.monthlyLimitCents - billing.includedUsedCents)
      : null;
  const windows: AccountQuotaWindow[] = [
    {
      id: 'monthly-limit',
      label: t('xai_quota.monthly_credits'),
      remainingPercent: buildRemainingFromUsedPercent(billing.usedPercent),
      resetLabel: billing.billingPeriodEnd ? formatQuotaResetTime(billing.billingPeriodEnd) : '-',
      usageLabel: t('xai_quota.usage_amount', {
        remaining: formatXaiCurrency(remainingCents),
        limit: formatXaiCurrency(billing.monthlyLimitCents),
      }),
    },
  ];

  if (billing.onDemandCapCents !== null && billing.onDemandCapCents > 0) {
    const onDemandRemainingCents =
      billing.onDemandUsedCents !== null
        ? Math.max(0, billing.onDemandCapCents - billing.onDemandUsedCents)
        : null;
    windows.push({
      id: 'pay-as-you-go',
      label: t('xai_quota.pay_as_you_go_label'),
      remainingPercent: buildRemainingFromUsedPercent(billing.onDemandUsedPercent),
      resetLabel: '-',
      usageLabel: t('xai_quota.usage_amount', {
        remaining: formatXaiCurrency(onDemandRemainingCents),
        limit: formatXaiCurrency(billing.onDemandCapCents),
      }),
    });
  }

  return windows;
};

export const getAccountQuotaProviderLabel = (
  provider: MonitoringAccountQuotaProvider,
  t: TFunction
) => {
  switch (provider) {
    case 'antigravity':
      return t('antigravity_quota.title');
    case 'claude':
      return t('claude_quota.title');
    case 'kimi':
      return t('kimi_quota.title');
    case 'xai':
      return t('xai_quota.title');
    case 'codex':
    default:
      return t('codex_quota.title');
  }
};

const getAccountQuotaEmptyMessage = (provider: MonitoringAccountQuotaProvider, t: TFunction) => {
  switch (provider) {
    case 'antigravity':
      return t('antigravity_quota.empty_models');
    case 'claude':
      return t('claude_quota.empty_windows');
    case 'kimi':
      return t('kimi_quota.empty_data');
    case 'xai':
      return t('xai_quota.empty_data');
    case 'codex':
    default:
      return t('codex_quota.empty_windows');
  }
};

const buildBaseAccountQuotaEntry = (
  target: MonitoringAccountQuotaTarget,
  t: TFunction,
  metaLabels: string[] = []
): Omit<AccountQuotaEntry, 'windows'> => {
  const providerLabel = getAccountQuotaProviderLabel(target.provider, t);
  return {
    key: target.key,
    provider: target.provider,
    providerLabel,
    authLabel: target.authLabel,
    fileName: target.fileName,
    planType: target.planType,
    metaLabels: [providerLabel, ...metaLabels].filter(Boolean),
    emptyMessage: getAccountQuotaEmptyMessage(target.provider, t),
  };
};

const stampAccountQuotaFetchTime = <T extends AccountQuotaEntry>(entry: T): T => ({
  ...entry,
  fetchedAtMs: Date.now(),
});

export const buildAccountQuotaErrorEntry = (
  target: MonitoringAccountQuotaTarget,
  error: string,
  t: TFunction
): AccountQuotaEntry => ({
  ...buildBaseAccountQuotaEntry(target, t),
  windows: [],
  error,
});

export const buildObservedCodexAccountQuotaEntry = (
  target: MonitoringAccountQuotaTarget,
  snapshot: UsageHeaderSnapshot | undefined,
  t: TFunction
): AccountQuotaEntry | null => {
  if (target.provider !== 'codex' || !hasUsageHeaderQuotaSignal(snapshot)) return null;
  const planType = target.planType ?? getHeaderSnapshotPlanType(snapshot) ?? null;
  const observedQuota = buildObservedCodexQuotaFromHeaderSnapshot(snapshot);
  const planLabel = getCodexPlanLabel(planType, t);
  const observedAtMs = readFiniteTimestamp(snapshot?.timestamp_ms) ?? undefined;
  const observedAt = observedAtMs ? new Date(observedAtMs).toLocaleString() : '';
  const usedPercent = getHeaderSnapshotUsedPercent(snapshot);
  const recoverAtMS = getHeaderSnapshotRecoverAtMs(snapshot);
  const errorKind = getHeaderSnapshotErrorKind(snapshot);
  const errorCode = getHeaderSnapshotErrorCode(snapshot);
  const traceID = getHeaderSnapshotTraceId(snapshot);
  const metaLabels = [
    planLabel ? `${t('codex_quota.plan_label')}: ${planLabel}` : '',
    observedAt
      ? t('quota_management.observed_from_usage_headers_at', {
          time: observedAt,
          defaultValue: `Observed from latest usage response headers · ${observedAt}`,
        })
      : t('quota_management.observed_from_usage_headers', {
          defaultValue: 'Observed from latest usage response headers',
        }),
    [errorKind, errorCode].filter(Boolean).join(' / '),
    traceID ? `Trace: ${traceID}` : '',
  ].filter(Boolean);

  const observedWindows: CodexQuotaWindow[] = observedQuota?.payload
    ? buildCodexQuotaWindowInfos(observedQuota.payload, { planType }).map((window) => ({
        id: window.id,
        label: t(window.labelKey, window.labelParams),
        labelKey: window.labelKey,
        labelParams: window.labelParams,
        usedPercent: window.usedPercent,
        resetLabel: window.resetLabel,
        limitWindowSeconds: window.limitWindowSeconds,
      }))
    : [];
  const windows: AccountQuotaWindow[] =
    observedWindows.length > 0
      ? buildCodexAccountQuotaWindows(observedWindows, t)
      : usedPercent !== null || recoverAtMS
        ? [
            {
              id: 'usage-header-observed',
              label: t('codex_quota.observed_window', { defaultValue: 'Latest request' }),
              remainingPercent: buildRemainingFromUsedPercent(usedPercent),
              resetLabel: recoverAtMS ? new Date(recoverAtMS).toLocaleString() : '-',
              usageLabel:
                usedPercent !== null
                  ? t('monitoring.account_quota_observed_used', {
                      percent: `${Math.round(usedPercent)}%`,
                      defaultValue: `Observed used ${Math.round(usedPercent)}%`,
                    })
                  : null,
            },
          ]
        : [];

  return {
    ...buildBaseAccountQuotaEntry({ ...target, planType }, t, metaLabels),
    planType,
    windows,
    observedAtMs,
    observedFromUsageHeaders: true,
  };
};

export const requestAccountQuota = async (
  target: MonitoringAccountQuotaTarget,
  t: TFunction
): Promise<AccountQuotaEntry> => {
  switch (target.provider) {
    case 'antigravity': {
      const { groups } = await fetchAntigravityQuota(target.file, t);
      return stampAccountQuotaFetchTime({
        ...buildBaseAccountQuotaEntry(target, t),
        windows: buildAntigravityAccountQuotaWindows(groups),
      });
    }
    case 'claude': {
      const quota = await fetchClaudeQuota(target.file, t);
      const metaLabels: string[] = [];
      if (quota.planType) {
        metaLabels.push(`${t('claude_quota.plan_label')}: ${t(`claude_quota.${quota.planType}`)}`);
      }
      if (quota.extraUsage?.is_enabled) {
        metaLabels.push(
          `${t('claude_quota.extra_usage_label')}: $${(quota.extraUsage.used_credits / 100).toFixed(2)} / $${(quota.extraUsage.monthly_limit / 100).toFixed(2)}`
        );
      }
      return stampAccountQuotaFetchTime({
        ...buildBaseAccountQuotaEntry(target, t, metaLabels),
        planType: quota.planType ?? target.planType,
        windows: buildClaudeAccountQuotaWindows(quota.windows, t),
      });
    }
    case 'kimi': {
      const rows = await fetchKimiQuota(target.file, t);
      return stampAccountQuotaFetchTime({
        ...buildBaseAccountQuotaEntry(target, t),
        windows: buildKimiAccountQuotaWindows(rows, t),
      });
    }
    case 'xai': {
      const billing = await fetchXaiQuota(target.file, t);
      const metaLabels =
        billing.onDemandCapCents !== null
          ? [`${t('xai_quota.on_demand_cap')}: ${formatXaiCurrency(billing.onDemandCapCents)}`]
          : [];
      return stampAccountQuotaFetchTime({
        ...buildBaseAccountQuotaEntry(target, t, metaLabels),
        windows: buildXaiAccountQuotaWindows(billing, t),
      });
    }
    case 'codex':
    default: {
      const quota = await fetchCodexQuota(target.file, t);
      const planLabel = getCodexPlanLabel(quota.planType ?? target.planType, t);
      return stampAccountQuotaFetchTime({
        ...buildBaseAccountQuotaEntry(
          {
            ...target,
            planType: quota.planType ?? target.planType,
          },
          t,
          planLabel ? [`${t('codex_quota.plan_label')}: ${planLabel}`] : []
        ),
        planType: quota.planType ?? target.planType,
        windows: buildCodexAccountQuotaWindows(quota.windows, t),
      });
    }
  }
};

export const buildRealtimeLogRows = (rows: MonitoringEventRow[]): RealtimeLogRow[] => {
  const sortedAsc = [...rows].sort(
    (left, right) => left.timestampMs - right.timestampMs || left.id.localeCompare(right.id)
  );
  const metricsByStream = new Map<string, { total: number; success: number; pattern: boolean[] }>();

  const enriched = sortedAsc.map((row) => {
    const streamKey = [row.account, row.provider, row.model, row.channel].join('::');
    const previous = metricsByStream.get(streamKey) ?? { total: 0, success: 0, pattern: [] };
    const nextPattern = [...previous.pattern, !row.failed].slice(-10);
    const next = {
      total: previous.total + (row.statsIncluded ? 1 : 0),
      success: previous.success + (row.statsIncluded && !row.failed ? 1 : 0),
      pattern: nextPattern,
    };
    metricsByStream.set(streamKey, next);

    return {
      ...row,
      streamKey,
      requestCount: next.total,
      successRate: next.total > 0 ? next.success / next.total : 1,
      recentPattern: nextPattern,
    } satisfies RealtimeLogRow;
  });

  return enriched.sort(
    (left, right) =>
      right.timestampMs - left.timestampMs ||
      right.requestCount - left.requestCount ||
      right.id.localeCompare(left.id)
  );
};

export const formatAccountOverviewScopeText = (
  bounds: { startMs: number; endMs: number } | null,
  locale: string,
  t: TFunction
) => {
  if (!bounds) {
    return t('monitoring.account_overview_scope_current_filters');
  }

  const rangeLabel =
    Number.isFinite(bounds.startMs) && Number.isFinite(bounds.endMs)
      ? formatStatusWindowLabel(bounds.startMs, bounds.endMs, locale)
      : t('monitoring.range_all');

  return t('monitoring.account_overview_scope_range', { range: rangeLabel });
};
