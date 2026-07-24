import type { ApiKeyAlias } from '@/services/api/usageService';
import type { ResponseHeaderMetadata } from '@/services/api/usageService';
import type { AuthFileItem } from '@/types/authFile';
import type { Config } from '@/types/config';
import type { ModelPrice } from '@/utils/usage';
import type { MonitoringDataTab } from '../monitoringCenterUiState';

export type MonitoringChannelMeta = {
  key: string;
  name: string;
  baseUrl: string;
  host: string;
  disabled: boolean;
  authIndices: string[];
  modelNames: string[];
};

export type MonitoringAuthMeta = {
  authIndex: string;
  label: string;
  account: string;
  provider: string;
  status: string;
  disabled: boolean;
  unavailable: boolean;
  runtimeOnly: boolean;
  planType: string;
  updatedAt: string;
};

export type MonitoringTimeRange = 'today' | '7d' | '14d' | '30d' | 'all' | 'custom';

export type MonitoringCustomTimeRange = {
  startMs: number;
  endMs: number;
};

export type MonitoringStatusTone = 'good' | 'warn' | 'bad';

export type MonitoringStatusChip = {
  key: string;
  label: string;
  value: string;
  tone: MonitoringStatusTone;
};

export type MonitoringKpi = {
  key: string;
  label: string;
  value: number;
  meta: number;
};

export type MonitoringTimelinePoint = {
  label: string;
  requests: number;
  tokens: number;
  cost: number;
};

export type MonitoringModelShareRow = {
  model: string;
  requests: number;
  totalTokens: number;
  totalCost: number;
  successRate: number;
};

export type MonitoringChannelRow = {
  id: string;
  label: string;
  host: string;
  provider: string;
  planTypes: string[];
  disabled: boolean;
  authCount: number;
  modelCount: number;
  requests: number;
  failures: number;
  successRate: number;
  totalTokens: number;
  totalCost: number;
  averageLatencyMs: number | null;
  authLabels: string[];
};

export type MonitoringModelRow = {
  model: string;
  requests: number;
  failures: number;
  successRate: number;
  totalTokens: number;
  totalCost: number;
  averageLatencyMs: number | null;
  sources: number;
  channels: number;
};

export type MonitoringFailureSourceRow = {
  id: string;
  label: string;
  channel: string;
  failures: number;
  totalRequests: number;
  failureRate: number;
  lastSeenAt: number;
  averageLatencyMs: number | null;
};

export type MonitoringTaskBucketRow = {
  id: string;
  timestampMs: number;
  timestamp: string;
  source: string;
  sourceMasked: string;
  channel: string;
  authLabel: string;
  planType: string;
  calls: number;
  failedCalls: number;
  failed: boolean;
  modelsText: string;
  totalTokens: number;
  cachedTokens: number;
  cacheReadTokens: number;
  cacheCreationTokens: number;
  totalCost: number;
  averageLatencyMs: number | null;
  maxLatencyMs: number | null;
  endpointsText: string;
};

export type MonitoringFailureRow = {
  id: string;
  timestampMs: number;
  timestamp: string;
  model: string;
  source: string;
  channel: string;
  authIndex: string;
  latencyMs: number | null;
};

export type MonitoringEventRow = {
  id: string;
  timestamp: string;
  timestampMs: number;
  dayKey: string;
  hourLabel: string;
  model: string;
  resolvedModel?: string;
  endpoint: string;
  endpointMethod: string;
  endpointPath: string;
  sourceKey: string;
  source: string;
  sourceMasked: string;
  account: string;
  accountMasked: string;
  authIndex: string;
  authIndexMasked: string;
  authLabel: string;
  projectId: string;
  apiKeyHash: string;
  apiKeyLabel: string;
  apiKeyMasked: string;
  provider: string;
  planType: string;
  channel: string;
  channelHost: string;
  channelDisabled: boolean;
  failed: boolean;
  statsIncluded: boolean;
  latencyMs: number | null;
  ttftMs: number | null;
  tokensPerSecond: number | null;
  inputTokens: number;
  outputTokens: number;
  reasoningTokens: number;
  cachedTokens: number;
  cacheReadTokens: number;
  cacheCreationTokens: number;
  totalTokens: number;
  totalCost: number;
  reasoningEffort?: string;
  serviceTier?: string;
  requestServiceTier?: string;
  responseServiceTier?: string;
  executorType?: string;
  failStatusCode?: number | null;
  failSummary?: string;
  responseMetadata?: ResponseHeaderMetadata;
  headerQuotaRecoverAtMs?: number | null;
  headerQuotaUsedPercent?: number | null;
  headerQuotaPlanType?: string;
  headerErrorKind?: string;
  headerErrorCode?: string;
  headerTraceId?: string;
  taskKey: string;
  searchText: string;
};

export type MonitoringSummary = {
  totalCalls: number;
  successCalls: number;
  failureCalls: number;
  successRate: number;
  inputTokens: number;
  outputTokens: number;
  reasoningTokens: number;
  cachedTokens: number;
  cacheReadTokens: number;
  cacheCreationTokens: number;
  cacheHitRate?: number;
  totalTokens: number;
  totalCost: number;
  averageLatencyMs: number | null;
  rpm30m: number;
  tpm30m: number;
  avgDailyRequests: number;
  avgDailyTokens: number;
  approxTasks: number;
  approxTaskFailures: number;
  approxTaskSuccessRate: number;
  zeroTokenCalls: number;
  zeroTokenModels: string[];
};

export type MonitoringAccountModelSpendRow = {
  model: string;
  totalCalls: number;
  successCalls: number;
  failureCalls: number;
  successRate: number;
  inputTokens: number;
  outputTokens: number;
  cachedTokens: number;
  cacheReadTokens: number;
  cacheCreationTokens: number;
  totalTokens: number;
  totalCost: number;
  lastSeenAt: number;
};

export type MonitoringAccountRow = {
  id: string;
  account: string;
  filterValue?: string;
  displayAccount: string;
  accountMasked: string;
  authLabels: string[];
  authIndices: string[];
  sourceKeys?: string[];
  channels: string[];
  totalCalls: number;
  successCalls: number;
  failureCalls: number;
  successRate: number;
  inputTokens: number;
  outputTokens: number;
  cachedTokens: number;
  cacheReadTokens: number;
  cacheCreationTokens: number;
  totalTokens: number;
  totalCost: number;
  averageLatencyMs: number | null;
  lastSeenAt: number;
  recentPattern: boolean[];
  models: MonitoringAccountModelSpendRow[];
};

export type MonitoringApiKeyModelSpendRow = MonitoringAccountModelSpendRow;

export type MonitoringApiKeyRow = {
  id: string;
  apiKeyHash: string;
  apiKeyLabel: string;
  apiKeyMasked: string;
  apiKeyCopyValue?: string;
  isUnknown: boolean;
  authLabels: string[];
  sourceLabels: string[];
  channels: string[];
  totalCalls: number;
  successCalls: number;
  failureCalls: number;
  successRate: number;
  inputTokens: number;
  outputTokens: number;
  cachedTokens: number;
  cacheReadTokens: number;
  cacheCreationTokens: number;
  totalTokens: number;
  totalCost: number;
  averageLatencyMs: number | null;
  lastSeenAt: number;
  models: MonitoringApiKeyModelSpendRow[];
};

export type MonitoringFilterOptions = {
  accountRows: MonitoringAccountRow[];
  apiKeyRows: MonitoringApiKeyRow[];
  accountCount?: number;
  apiKeyCount?: number;
  providers: string[];
  models: string[];
  channels: string[];
  headerTraceIds: string[];
};

export type MonitoringRealtimeRow = {
  id: string;
  account: string;
  accountMasked: string;
  authLabel: string;
  authIndexMasked: string;
  provider: string;
  requestType: string;
  model: string;
  channel: string;
  latestFailed: boolean;
  successRate: number;
  totalCalls: number;
  successCalls: number;
  failureCalls: number;
  averageLatencyMs: number | null;
  latestLatencyMs: number | null;
  inputTokens: number;
  outputTokens: number;
  cachedTokens: number;
  cacheReadTokens: number;
  cacheCreationTokens: number;
  totalTokens: number;
  totalCost: number;
  lastSeenAt: number;
  recentPattern: boolean[];
};

export type MonitoringMetadata = {
  totalAuthFiles: number;
  activeAuthFiles: number;
  unavailableAuthFiles: number;
  runtimeOnlyAuthFiles: number;
  totalChannels: number;
  enabledChannels: number;
  configuredModels: number;
  planTypes: string[];
};

export interface MonitoringScopeFilters {
  account?: string;
  provider?: string;
  authFile?: string;
  projectId?: string;
  requestType?: string;
  model?: string;
  channel?: string;
  apiKeyHash?: string;
  status?: 'all' | 'success' | 'failed';
  minLatencyMs?: number;
  cacheStatus?: string;
  headerTraceId?: string;
}

export interface UseMonitoringDataParams {
  usage?: unknown;
  config: Config | null | undefined;
  modelPrices: Record<string, ModelPrice>;
  apiKeyAliases?: ApiKeyAlias[];
  timeRange: MonitoringTimeRange;
  customTimeRange?: MonitoringCustomTimeRange | null;
  searchQuery: string;
  searchApiKeyHash?: string;
  scopeFilters?: MonitoringScopeFilters;
  activeDataTab?: MonitoringDataTab;
}

export interface UseMonitoringDataReturn {
  loading: boolean;
  error: string;
  authFiles: AuthFileItem[];
  channels: MonitoringChannelMeta[];
  summary: MonitoringSummary;
  metadata: MonitoringMetadata;
  statusChips: MonitoringStatusChip[];
  timeline: MonitoringTimelinePoint[];
  timelineGranularity: 'hour' | 'day';
  hourlyDistribution: MonitoringTimelinePoint[];
  modelShareRows: MonitoringModelShareRow[];
  channelRows: MonitoringChannelRow[];
  modelRows: MonitoringModelRow[];
  failureSourceRows: MonitoringFailureSourceRow[];
  taskBuckets: MonitoringTaskBucketRow[];
  recentFailures: MonitoringFailureRow[];
  accountRows: MonitoringAccountRow[];
  apiKeyRows: MonitoringApiKeyRow[];
  filterOptions: MonitoringFilterOptions;
  filteredRows: MonitoringEventRow[];
  eventsHasMore: boolean;
  eventsLoadingMore: boolean;
  eventsRetentionLimited: boolean;
  eventsTotalCount: number;
  eventsLoadedCount: number;
  lastRefreshedAt: Date | null;
  isTransitioningScope: boolean;
  hasPresentationSnapshot: boolean;
  refreshMeta: (showLoading?: boolean) => Promise<void>;
  loadMoreEvents: () => void;
}

export type MonitoringMetaPayload = {
  authFiles: AuthFileItem[];
  channels: MonitoringChannelMeta[];
  error: string;
};
