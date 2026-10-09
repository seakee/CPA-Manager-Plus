import {
  USAGE_ANALYTICS_DEFAULT_FILTERS,
  USAGE_ANALYTICS_TABS,
  type UsageAnalyticsCacheStatus,
  type UsageAnalyticsCustomRange,
  type UsageAnalyticsFiltersState,
  type UsageAnalyticsGranularity,
  type UsageAnalyticsLatencyFilter,
  type UsageAnalyticsStatus,
  type UsageAnalyticsTab,
  type UsageAnalyticsTimeRange,
} from './usageAnalyticsModel';

export const USAGE_ANALYTICS_UI_STATE_STORAGE_KEY = 'usageAnalytics.uiState';

export type UsageAnalyticsUiState = {
  activeTab: UsageAnalyticsTab;
  filters: UsageAnalyticsFiltersState;
};

export type UsageAnalyticsUiStatePatch = {
  activeTab?: UsageAnalyticsTab;
  filters?: Partial<UsageAnalyticsFiltersState>;
};

const TIME_RANGE_SET = new Set<UsageAnalyticsTimeRange>([
  '24h',
  'today',
  'yesterday',
  '7d',
  '30d',
  'custom',
]);
const GRANULARITY_SET = new Set<UsageAnalyticsGranularity>(['auto', '1m', '15m', 'hour', 'day']);
const STATUS_SET = new Set<UsageAnalyticsStatus>(['all', 'success', 'failed']);
const LATENCY_SET = new Set<UsageAnalyticsLatencyFilter>(['all', '3000', '10000', '30000']);
const CACHE_STATUS_SET = new Set<UsageAnalyticsCacheStatus>(['all', 'hit', 'miss']);
const TAB_SET = new Set<UsageAnalyticsTab>(USAGE_ANALYTICS_TABS);

const normalizeTimeRange = (value: unknown): UsageAnalyticsTimeRange =>
  typeof value === 'string' && TIME_RANGE_SET.has(value as UsageAnalyticsTimeRange)
    ? (value as UsageAnalyticsTimeRange)
    : USAGE_ANALYTICS_DEFAULT_FILTERS.timeRange;

const normalizeGranularity = (value: unknown): UsageAnalyticsGranularity =>
  typeof value === 'string' && GRANULARITY_SET.has(value as UsageAnalyticsGranularity)
    ? (value as UsageAnalyticsGranularity)
    : USAGE_ANALYTICS_DEFAULT_FILTERS.granularity;

const normalizeStatus = (value: unknown): UsageAnalyticsStatus =>
  typeof value === 'string' && STATUS_SET.has(value as UsageAnalyticsStatus)
    ? (value as UsageAnalyticsStatus)
    : USAGE_ANALYTICS_DEFAULT_FILTERS.status;

const normalizeLatency = (value: unknown): UsageAnalyticsLatencyFilter =>
  typeof value === 'string' && LATENCY_SET.has(value as UsageAnalyticsLatencyFilter)
    ? (value as UsageAnalyticsLatencyFilter)
    : USAGE_ANALYTICS_DEFAULT_FILTERS.minLatencyMs;

const normalizeCacheStatus = (value: unknown): UsageAnalyticsCacheStatus =>
  typeof value === 'string' && CACHE_STATUS_SET.has(value as UsageAnalyticsCacheStatus)
    ? (value as UsageAnalyticsCacheStatus)
    : USAGE_ANALYTICS_DEFAULT_FILTERS.cacheStatus;

const normalizeActiveTab = (value: unknown): UsageAnalyticsTab =>
  typeof value === 'string' && TAB_SET.has(value as UsageAnalyticsTab)
    ? (value as UsageAnalyticsTab)
    : 'overview';

const normalizeSelectValue = (value: unknown): string => {
  const normalized = typeof value === 'string' ? value.trim() : '';
  return normalized || 'all';
};

const normalizeInputValue = (value: unknown): string =>
  typeof value === 'string' ? value : '';

const normalizeCustomRange = (value: unknown): UsageAnalyticsCustomRange | null => {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null;

  const record = value as Record<string, unknown>;
  const startMs = record.startMs;
  const endMs = record.endMs;
  if (
    typeof startMs !== 'number' ||
    typeof endMs !== 'number' ||
    !Number.isFinite(startMs) ||
    !Number.isFinite(endMs) ||
    startMs >= endMs
  ) {
    return null;
  }
  return { startMs, endMs };
};

export const getDefaultUsageAnalyticsUiState = (): UsageAnalyticsUiState => ({
  activeTab: 'overview',
  filters: USAGE_ANALYTICS_DEFAULT_FILTERS,
});

export const normalizeUsageAnalyticsFilters = (value: unknown): UsageAnalyticsFiltersState => {
  const defaults = USAGE_ANALYTICS_DEFAULT_FILTERS;
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    return defaults;
  }

  const record = value as Record<string, unknown>;
  const customRange = normalizeCustomRange(record.customRange);
  const timeRange = normalizeTimeRange(record.timeRange);
  return {
    timeRange: timeRange === 'custom' && !customRange ? defaults.timeRange : timeRange,
    customRange,
    granularity: normalizeGranularity(record.granularity),
    model: normalizeSelectValue(record.model),
    apiKeyHash: normalizeSelectValue(record.apiKeyHash),
    provider: normalizeSelectValue(record.provider),
    authFile: normalizeSelectValue(record.authFile),
    status: normalizeStatus(record.status),
    searchQuery: normalizeInputValue(record.searchQuery),
    minLatencyMs: normalizeLatency(record.minLatencyMs),
    cacheStatus: normalizeCacheStatus(record.cacheStatus),
    apiKeyKeyword: normalizeInputValue(record.apiKeyKeyword),
  };
};

export const normalizeUsageAnalyticsUiState = (value: unknown): UsageAnalyticsUiState => {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    return getDefaultUsageAnalyticsUiState();
  }

  return {
    activeTab: normalizeActiveTab((value as Record<string, unknown>).activeTab),
    filters: normalizeUsageAnalyticsFilters((value as Record<string, unknown>).filters),
  };
};

export const readUsageAnalyticsUiState = (): UsageAnalyticsUiState => {
  if (typeof window === 'undefined' || typeof window.localStorage === 'undefined') {
    return getDefaultUsageAnalyticsUiState();
  }

  try {
    const raw = window.localStorage.getItem(USAGE_ANALYTICS_UI_STATE_STORAGE_KEY);
    if (raw) {
      return normalizeUsageAnalyticsUiState(JSON.parse(raw));
    }
  } catch {
    // Ignore storage failures and fall back to defaults.
  }

  return getDefaultUsageAnalyticsUiState();
};

const parseQueryTimestamp = (params: URLSearchParams, key: string) => {
  const value = Number(params.get(key));
  return Number.isFinite(value) && value > 0 ? value : null;
};

const queryHasAnyFilter = (params: URLSearchParams) =>
  [
    'time_range',
    'from_ms',
    'to_ms',
    'granularity',
    'model',
    'api_key_hash',
    'provider',
    'auth_file',
    'status',
    'search',
    'min_latency_ms',
    'cache_status',
    'api_key_keyword',
  ].some((key) => params.has(key));

export const buildUsageAnalyticsUiStateFromSearchParams = (
  params: URLSearchParams,
  fallback: UsageAnalyticsUiState = getDefaultUsageAnalyticsUiState()
): UsageAnalyticsUiState => {
  const activeTab = params.has('tab') ? normalizeActiveTab(params.get('tab')) : fallback.activeTab;
  if (!queryHasAnyFilter(params)) {
    return {
      activeTab,
      filters: fallback.filters,
    };
  }

  const record: Record<string, unknown> = { ...fallback.filters };
  const timeRangeParam = params.get('time_range');
  const fromMs = parseQueryTimestamp(params, 'from_ms');
  const toMs = parseQueryTimestamp(params, 'to_ms');
  const hasRange = fromMs !== null && toMs !== null && fromMs < toMs;

  if (params.has('time_range')) {
    record.timeRange = timeRangeParam;
    if (timeRangeParam !== 'custom') {
      record.customRange = null;
    }
  }
  if (hasRange && (!params.has('time_range') || timeRangeParam === 'custom')) {
    record.timeRange = 'custom';
    record.customRange = { startMs: fromMs, endMs: toMs };
  }
  if (params.has('granularity')) record.granularity = params.get('granularity');
  if (params.has('model')) record.model = params.get('model');
  if (params.has('api_key_hash')) record.apiKeyHash = params.get('api_key_hash');
  if (params.has('provider')) record.provider = params.get('provider');
  if (params.has('auth_file')) record.authFile = params.get('auth_file');
  if (params.has('status')) record.status = params.get('status');
  if (params.has('search')) record.searchQuery = params.get('search') ?? '';
  if (params.has('min_latency_ms')) record.minLatencyMs = params.get('min_latency_ms');
  if (params.has('cache_status')) record.cacheStatus = params.get('cache_status');
  if (params.has('api_key_keyword')) record.apiKeyKeyword = params.get('api_key_keyword') ?? '';

  return {
    activeTab,
    filters: normalizeUsageAnalyticsFilters(record),
  };
};

const setNonDefaultParam = (
  params: URLSearchParams,
  key: string,
  value: string,
  defaultValue = ''
) => {
  const trimmed = value.trim();
  if (trimmed && trimmed !== defaultValue) {
    params.set(key, trimmed);
  }
};

export const buildUsageAnalyticsSearchParams = (state: UsageAnalyticsUiState) => {
  const normalized = normalizeUsageAnalyticsUiState(state);
  const { activeTab, filters } = normalized;
  const defaults = USAGE_ANALYTICS_DEFAULT_FILTERS;
  const params = new URLSearchParams();

  if (activeTab !== 'overview') params.set('tab', activeTab);
  if (filters.timeRange !== defaults.timeRange || filters.timeRange === 'custom') {
    params.set('time_range', filters.timeRange);
  }
  if (filters.timeRange === 'custom' && filters.customRange) {
    params.set('from_ms', String(filters.customRange.startMs));
    params.set('to_ms', String(filters.customRange.endMs));
  }
  if (filters.granularity !== defaults.granularity) {
    params.set('granularity', filters.granularity);
  }
  setNonDefaultParam(params, 'model', filters.model, defaults.model);
  setNonDefaultParam(params, 'api_key_hash', filters.apiKeyHash, defaults.apiKeyHash);
  setNonDefaultParam(params, 'provider', filters.provider, defaults.provider);
  setNonDefaultParam(params, 'auth_file', filters.authFile, defaults.authFile);
  if (filters.status !== defaults.status) params.set('status', filters.status);
  setNonDefaultParam(params, 'search', filters.searchQuery, defaults.searchQuery);
  if (filters.minLatencyMs !== defaults.minLatencyMs) {
    params.set('min_latency_ms', filters.minLatencyMs);
  }
  if (filters.cacheStatus !== defaults.cacheStatus) {
    params.set('cache_status', filters.cacheStatus);
  }
  setNonDefaultParam(
    params,
    'api_key_keyword',
    filters.apiKeyKeyword,
    defaults.apiKeyKeyword
  );

  return params;
};

export const writeUsageAnalyticsUiState = (state: UsageAnalyticsUiStatePatch) => {
  if (typeof window === 'undefined' || typeof window.localStorage === 'undefined') return;

  try {
    const current = readUsageAnalyticsUiState();
    const next = normalizeUsageAnalyticsUiState({
      activeTab: state.activeTab ?? current.activeTab,
      filters: state.filters ? { ...current.filters, ...state.filters } : current.filters,
    });
    window.localStorage.setItem(
      USAGE_ANALYTICS_UI_STATE_STORAGE_KEY,
      JSON.stringify(next)
    );
  } catch {
    // Ignore storage failures and keep the runtime state in memory only.
  }
};
