import type { TFunction } from 'i18next';
import type {
  AntigravityQuotaGroup,
  AuthFileItem,
  ClaudeQuotaWindow,
  CodexQuotaWindow,
  GeminiCliQuotaBucketState,
  KimiQuotaRow,
  XaiBillingSummary,
} from '@/types';
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
import type {
  AccountQuotaEntry,
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
  fetchGeminiCliCodeAssist,
  fetchGeminiCliQuotaBuckets,
  fetchKimiQuota,
  fetchXaiQuota,
  formatKimiResetHint,
  formatQuotaResetTime,
} from '@/utils/quota';
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
      { value: 'all', label: t('monitoring.filter_all_providers') },
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
      { value: 'all', label: t('monitoring.filter_all_accounts') },
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
      { value: 'all', label: t('monitoring.filter_all_models') },
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
      { value: 'all', label: t('monitoring.filter_all_channels') },
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
      { value: 'all', label: t('monitoring.filter_all_api_keys') },
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
  { value: 'all', label: t('monitoring.filter_all_statuses') },
  { value: 'success', label: t('monitoring.filter_status_success') },
  { value: 'failed', label: t('monitoring.filter_status_failed') },
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
  { key: 'account', label: t('monitoring.account_overview_col_account') },
  { key: 'status', label: t('monitoring.column_status') },
  { key: 'total-calls', label: t('monitoring.total_calls'), sortKey: 'totalCalls' },
  {
    key: 'success-calls',
    label: t('monitoring.account_overview_col_success'),
    sortKey: 'successCalls',
  },
  {
    key: 'failure-calls',
    label: t('monitoring.account_overview_col_failure'),
    sortKey: 'failureCalls',
  },
  { key: 'success-rate', label: t('monitoring.column_success_rate'), sortKey: 'successRate' },
  { key: 'total-tokens', label: t('monitoring.total_tokens'), sortKey: 'totalTokens' },
  {
    key: 'estimated-cost',
    label: t('monitoring.account_overview_col_cost'),
    sortKey: 'totalCost',
  },
  {
    key: 'latest-request-time',
    label: t('monitoring.latest_request_time'),
    sortKey: 'lastSeenAt',
  },
  { key: 'action', label: t('common.action') },
];

export const buildApiKeyOverviewColumns = (t: TFunction): AccountOverviewColumn[] => [
  { key: 'api-key', label: t('monitoring.api_key_summary_col_key') },
  { key: 'total-calls', label: t('monitoring.total_calls') },
  { key: 'success-calls', label: t('monitoring.account_overview_col_success') },
  { key: 'failure-calls', label: t('monitoring.account_overview_col_failure') },
  { key: 'total-tokens', label: t('monitoring.total_tokens') },
  { key: 'estimated-cost', label: t('monitoring.account_overview_col_cost') },
  { key: 'latest-request-time', label: t('monitoring.latest_request_time') },
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
    label: t('monitoring.total_calls'),
    value: formatCompactNumber(summary.totalCalls),
    valueTitle: formatFullNumber(summary.totalCalls, locale),
    meta: `${accountCount} ${t('monitoring.accounts_suffix')}`,
    icon: 'calls',
    accent: 'blue',
  },
  {
    label: t('monitoring.call_success_rate'),
    value: formatPercent(summary.successRate),
    meta: formatDurationMs(summary.averageLatencyMs, { locale }),
    tone: summary.successRate >= 0.95 ? 'good' : summary.successRate >= 0.85 ? 'warn' : 'bad',
    icon: 'success',
    accent: 'green',
  },
  {
    label: t('monitoring.failure_calls'),
    value: formatCompactNumber(summary.failureCalls),
    valueTitle: formatFullNumber(summary.failureCalls, locale),
    meta: `${failedGroupCount} ${t('monitoring.groups_suffix')}`,
    tone: summary.failureCalls > 0 ? 'bad' : 'good',
    icon: 'failure',
    accent: 'red',
  },
  {
    label: t('monitoring.estimated_cost'),
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
  const cachedTokenMetaParts = [
    `${t('monitoring.of_input_tokens')} ${formatPercent(summary.inputTokens > 0 ? summary.cachedTokens / summary.inputTokens : 0)}`,
  ];

  if (summary.cacheCreationTokens > 0 || summary.cacheReadTokens > 0) {
    cachedTokenMetaParts.push(
      `${t('monitoring.cache_creation_tokens_short')} ${formatCompactNumber(summary.cacheCreationTokens)}`,
      `${t('monitoring.cache_read_tokens_short')} ${formatCompactNumber(summary.cacheReadTokens)}`
    );
  }

  return [
    {
      label: t('monitoring.total_tokens'),
      value: formatCompactNumber(summary.totalTokens),
      valueTitle: formatFullNumber(summary.totalTokens, locale),
      meta: `${t('monitoring.reasoning_tokens')} ${formatCompactNumber(summary.reasoningTokens)}`,
      variant: 'secondary',
      icon: 'tokens',
      accent: 'indigo',
    },
    {
      label: t('monitoring.input_tokens'),
      value: formatCompactNumber(summary.inputTokens),
      valueTitle: formatFullNumber(summary.inputTokens, locale),
      meta: `${t('monitoring.of_token_mix')} ${formatPercent(summary.totalTokens > 0 ? summary.inputTokens / summary.totalTokens : 0)}`,
      variant: 'secondary',
      icon: 'input',
      accent: 'cyan',
    },
    {
      label: t('monitoring.output_tokens'),
      value: formatCompactNumber(summary.outputTokens),
      valueTitle: formatFullNumber(summary.outputTokens, locale),
      meta: `${t('monitoring.of_token_mix')} ${formatPercent(summary.totalTokens > 0 ? summary.outputTokens / summary.totalTokens : 0)}`,
      variant: 'secondary',
      icon: 'output',
      accent: 'violet',
    },
    {
      label: t('monitoring.cached_tokens'),
      value: formatCompactNumber(summary.cachedTokens),
      valueTitle: formatFullNumber(summary.cachedTokens, locale),
      meta: cachedTokenMetaParts.join(' · '),
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
  groups.map((group) => ({
    id: group.id,
    label: group.label,
    remainingPercent: clampRemainingPercent(group.remainingFraction * 100),
    resetLabel: formatQuotaResetTime(group.resetTime),
    usageLabel: null,
  }));

const buildGeminiCliAccountQuotaWindows = (
  buckets: GeminiCliQuotaBucketState[],
  t: TFunction
): AccountQuotaWindow[] =>
  buckets.map((bucket) => {
    const remainingPercent =
      bucket.remainingFraction === null
        ? null
        : clampRemainingPercent(bucket.remainingFraction * 100);
    const usageLabelParts = [
      bucket.remainingAmount === null || bucket.remainingAmount === undefined
        ? ''
        : t('gemini_cli_quota.remaining_amount', { count: bucket.remainingAmount }),
      bucket.tokenType ?? '',
    ].filter(Boolean);

    return {
      id: bucket.id,
      label: bucket.label,
      remainingPercent,
      resetLabel: formatQuotaResetTime(bucket.resetTime),
      usageLabel: usageLabelParts.length > 0 ? usageLabelParts.join(' · ') : null,
    };
  });

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
      usageLabel: limit > 0 ? `${used} / ${limit}` : null,
    };
  });

const formatXaiCurrency = (value: number | null): string => {
  if (value === null) return '--';
  return `$${(value / 100).toFixed(2)}`;
};

const buildXaiAccountQuotaWindows = (
  billing: XaiBillingSummary,
  t: TFunction
): AccountQuotaWindow[] => [
  {
    id: 'monthly-limit',
    label: t('xai_quota.monthly_limit'),
    remainingPercent: buildRemainingFromUsedPercent(billing.usedPercent),
    resetLabel: billing.billingPeriodEnd ? formatQuotaResetTime(billing.billingPeriodEnd) : '-',
    usageLabel: t('xai_quota.usage_amount', {
      used: formatXaiCurrency(billing.usedCents),
      limit: formatXaiCurrency(billing.monthlyLimitCents),
    }),
  },
];

export const getAccountQuotaProviderLabel = (
  provider: MonitoringAccountQuotaProvider,
  t: TFunction
) => {
  switch (provider) {
    case 'antigravity':
      return t('antigravity_quota.title');
    case 'claude':
      return t('claude_quota.title');
    case 'gemini-cli':
      return t('gemini_cli_quota.title');
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
    case 'gemini-cli':
      return t('gemini_cli_quota.empty_buckets');
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

export const buildAccountQuotaErrorEntry = (
  target: MonitoringAccountQuotaTarget,
  error: string,
  t: TFunction
): AccountQuotaEntry => ({
  ...buildBaseAccountQuotaEntry(target, t),
  windows: [],
  error,
});

export const requestAccountQuota = async (
  target: MonitoringAccountQuotaTarget,
  t: TFunction
): Promise<AccountQuotaEntry> => {
  switch (target.provider) {
    case 'antigravity': {
      const groups = await fetchAntigravityQuota(target.file, t);
      return {
        ...buildBaseAccountQuotaEntry(target, t),
        windows: buildAntigravityAccountQuotaWindows(groups),
      };
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
      return {
        ...buildBaseAccountQuotaEntry(target, t, metaLabels),
        planType: quota.planType ?? target.planType,
        windows: buildClaudeAccountQuotaWindows(quota.windows, t),
      };
    }
    case 'gemini-cli': {
      const quota = await fetchGeminiCliQuotaBuckets(target.file, t);
      const supplementary = await fetchGeminiCliCodeAssist(quota.authIndex, quota.projectId, t);
      const metaLabels = [
        supplementary.tierLabel
          ? `${t('gemini_cli_quota.tier_label')}: ${supplementary.tierLabel}`
          : '',
        supplementary.creditBalance !== null
          ? `${t('gemini_cli_quota.credit_label')}: ${t('gemini_cli_quota.credit_amount', {
              count: supplementary.creditBalance,
            })}`
          : '',
      ].filter(Boolean);
      return {
        ...buildBaseAccountQuotaEntry(target, t, metaLabels),
        windows: buildGeminiCliAccountQuotaWindows(quota.buckets, t),
      };
    }
    case 'kimi': {
      const rows = await fetchKimiQuota(target.file, t);
      return {
        ...buildBaseAccountQuotaEntry(target, t),
        windows: buildKimiAccountQuotaWindows(rows, t),
      };
    }
    case 'xai': {
      const billing = await fetchXaiQuota(target.file, t);
      const metaLabels =
        billing.onDemandCapCents !== null
          ? [`${t('xai_quota.on_demand_cap')}: ${formatXaiCurrency(billing.onDemandCapCents)}`]
          : [];
      return {
        ...buildBaseAccountQuotaEntry(target, t, metaLabels),
        windows: buildXaiAccountQuotaWindows(billing, t),
      };
    }
    case 'codex':
    default: {
      const quota = await fetchCodexQuota(target.file, t);
      const planLabel = getCodexPlanLabel(quota.planType ?? target.planType, t);
      return {
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
      };
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
