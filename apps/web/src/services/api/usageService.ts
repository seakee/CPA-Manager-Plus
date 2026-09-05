import axios from 'axios';
import type { UsagePayload } from '@/features/monitoring/hooks/useUsageData';
import {
  getDemoAccountActionCandidates,
  getDemoAccountHistory,
  getDemoAccountWindowUsage,
  getDemoAccountProcessingPolicy,
  getDemoApiKeyAliases,
  getDemoAuthFiles,
  getDemoCodexInspectionRun,
  getDemoDashboardSummary,
  getDemoHeaderSnapshots,
  getDemoManagerConfig,
  getDemoModelPriceUsageSummary,
  getDemoModelPrices,
  getDemoMonitoringAnalytics,
  getDemoQuotaCooldowns,
  getDemoUsagePayload,
  getDemoUsageServiceInfo,
  getDemoUsageServiceStatus,
} from '@/features/demo/demoFixtures';
import { isDemoMode } from '@/features/demo/demoMode';
import { hasCodexInspectionStableIdentity } from '@/features/monitoring/model/codexInspectionOwnership';
import type { AuthFileItem, QuotaModelScope } from '@/types';
import { normalizeApiBase } from '@/utils/connection';
import {
  getAuthFileStatusIdentityKey,
  readAuthFileStatusPhysicalName,
  resolveAuthFileStatusMutationTarget,
} from '@/utils/authFileStatusMutation';
import type { ModelPrice } from '@/utils/usage';

const USAGE_SERVICE_ERROR_CODES = new Set([
  'request_failed',
  'connection_env_managed',
  'cpa_connection_required',
  'cpa_connection_required_for_monitoring',
  'management_api_validation_failed',
  'management_api_config_failed',
  'cpa_usage_retention_invalid',
  'poll_interval_exceeds_retention',
  'invalid_time_zone',
  'enable_cpa_usage_statistics_failed',
  'setup_env_managed',
  'invalid_existing_management_key',
  'invalid_admin_key',
  'invalid_management_key',
  'usage_service_not_configured',
  'prices_required',
  'api_key_aliases_required',
  'api_key_alias_duplicate',
  'model_price_sync_failed',
  'method_not_allowed',
  'account_processing_policy_env_locked',
  'usage_import_session_invalid_request',
  'usage_import_session_not_found',
  'usage_import_session_conflict',
  'usage_import_session_too_large',
  'usage_import_session_quota_exceeded',
  'usage_import_session_limit_exceeded',
  'usage_import_session_unavailable',
]);

export interface UsageServiceApiError extends Error {
  status?: number;
  code?: string;
  details?: unknown;
  data?: unknown;
}

export interface UsageServiceInfo {
  service?: string;
  mode?: string;
  startedAt?: number;
  configured?: boolean;
  adminReady?: boolean;
  projectInitialized?: boolean;
  setupRequired?: boolean;
  migrationStatus?: string;
  dataKeyReady?: boolean;
  hasHistoricalData?: boolean;
}

export interface UsageServiceCollectorStatus {
  collector?: string;
  upstream?: string;
  mode?: string;
  transport?: string;
  queue?: string;
  lastConsumedAt?: number;
  lastInsertedAt?: number;
  totalInserted?: number;
  totalSkipped?: number;
  deadLetters?: number;
  lastError?: string;
}

export interface UsageServiceCheckpointStatus {
  mode?: string;
  busy?: number;
  logFrames?: number;
  checkpointedFrames?: number;
  executedAtMs?: number;
  durationMs?: number;
  lastTruncateAttemptAtMs?: number;
  error?: string;
}

export interface UsageServiceDatabaseStatus {
  databaseBytes?: number;
  walBytes?: number;
  shmBytes?: number;
  totalBytes?: number;
  journalSizeLimitBytes?: number;
  checkpoint?: UsageServiceCheckpointStatus;
}

export type UsageServiceDatabaseMaintenanceReason =
  | 'deferred_indexes'
  | 'offline_derived_cleanup'
  | 'offline_quota_snapshot_migration'
  | 'legacy_index_replacement'
  | string;

export interface UsageServiceDatabaseMaintenanceStatus {
  required: boolean;
  performanceDegraded: boolean;
  deferredIndexes: number;
  offlineJobs: number;
  reasons: UsageServiceDatabaseMaintenanceReason[];
  command?: string;
}

export interface UsageServiceStatus {
  service?: string;
  dbPath?: string;
  events?: number;
  deadLetters?: number;
  collector?: UsageServiceCollectorStatus;
  database?: UsageServiceDatabaseStatus;
  databaseMaintenance?: UsageServiceDatabaseMaintenanceStatus;
}

export type UsageServiceStatusScope = 'database-maintenance';

export interface AccountPolicyCapability {
  enabled: boolean;
  configured?: boolean;
  source?: string;
  locked?: boolean;
  envKey: string;
  configFileKey: string;
  dependsOn?: string;
}

export interface AccountProcessingPolicy {
  source: string;
  updatedAtMs?: number;
  codexQuotaCooldown: AccountPolicyCapability;
  authIssueQueue: AccountPolicyCapability;
  authIssueAutoDisable: AccountPolicyCapability;
}

export interface AccountProcessingPolicyPatch {
  codexQuotaCooldownEnabled?: boolean;
  authIssueQueueEnabled?: boolean;
  authIssueAutoDisableEnabled?: boolean;
}

export interface QuotaCooldownInfo {
  authFileName: string;
  authIndex?: string;
  accountSnapshot?: string;
  provider?: string;
  owner?: string;
  reasonCode?: string;
  windowKind?: 'five_hour' | 'weekly' | 'monthly' | 'rolling_24h' | 'unknown' | string;
  recoverAtMs: number;
  disabledAtMs?: number;
  createdAtMs?: number;
  evidence?: ProviderUsageMetadata;
}

export interface QuotaCooldownsResponse {
  items: QuotaCooldownInfo[];
}

export interface UsageServiceSetupRequest {
  cpaBaseUrl: string;
  cpaManagementKey: string;
  managementKey?: string;
  collectorMode?: string;
  queue?: string;
  popSide?: string;
  batchSize?: number;
  pollIntervalMs?: number;
  queryLimit?: number;
  tlsSkipVerify?: boolean;
  ensureUsageStatisticsEnabled?: boolean;
  requestMonitoringEnabled?: boolean;
}

export interface ManagerCPAConnectionConfig {
  cpaBaseUrl: string;
  managementKeyConfigured?: boolean;
  /** Write-only. Responses never include the saved CPA Management Key. */
  managementKey?: string;
}

export interface ManagerCollectorConfig {
  enabled?: boolean;
  collectorMode: string;
  queue: string;
  popSide: string;
  batchSize: number;
  pollIntervalMs: number;
  queryLimit: number;
  tlsSkipVerify?: boolean;
}

export interface ManagerExternalUsageServiceConfig {
  enabled: boolean;
  serviceBase: string;
}

export type ManagerCodexInspectionScheduleMode = 'interval' | 'time_points';
export type ManagerCodexInspectionAutoActionMode = 'none' | 'enable' | 'disable' | 'delete';

export interface ManagerCodexInspectionScheduleConfig {
  mode?: ManagerCodexInspectionScheduleMode | string;
  timePoints?: string[];
  intervalMinutes?: number;
  timeZone?: string;
}

export interface ManagerCodexInspectionConfig {
  enabled?: boolean;
  schedule?: ManagerCodexInspectionScheduleConfig;
  targetTypes?: string[];
  targetType?: string;
  workers?: number;
  deleteWorkers?: number;
  timeout?: number;
  retries?: number;
  userAgent?: string;
  xaiInferenceUserAgent?: string;
  xaiInferenceEnabled?: boolean;
  xaiInferenceModel?: string;
  xaiInferencePrompt?: string;
  usedPercentThreshold?: number;
  sampleSize?: number;
  autoActionMode?: ManagerCodexInspectionAutoActionMode | string;
  autoRecoverEnabled?: boolean;
}

export interface ManagerConfig {
  cpaConnection: ManagerCPAConnectionConfig;
  collector: ManagerCollectorConfig;
  codexInspection?: ManagerCodexInspectionConfig;
  externalUsageService: ManagerExternalUsageServiceConfig;
  updatedAtMs?: number;
}

export interface CPAUsageConfig {
  usageStatisticsEnabled: boolean;
  redisUsageQueueRetentionSeconds: number;
  retentionSourceDefault?: boolean;
}

export interface ManagerConfigResponse {
  config: ManagerConfig;
  source?: 'env' | 'db' | '';
  cpaUsage?: CPAUsageConfig;
}

export interface CodexInspectionRun {
  id: number;
  triggerType: string;
  triggerKey?: string;
  status: string;
  startedAtMs: number;
  finishedAtMs?: number;
  totalFiles: number;
  probeSetCount: number;
  sampledCount: number;
  disabledCount: number;
  enabledCount: number;
  deleteCount: number;
  disableCount: number;
  enableCount: number;
  reauthCount: number;
  keepCount: number;
  error?: string;
  settings?: ManagerCodexInspectionConfig;
  createdAtMs: number;
  updatedAtMs: number;
  active?: boolean;
  cancellable?: boolean;
}

export interface CodexInspectionQuotaWindow {
  id: string;
  labelKey: string;
  labelParams?: Record<string, string | number>;
  usedPercent?: number | null;
  resetLabel?: string;
  resetAtMs?: number | null;
  resetAccuracy?: 'exact' | 'derived' | 'estimated' | 'unknown';
  limitWindowSeconds?: number | null;
  modelScope?: QuotaModelScope;
  providerWindowAliases?: string[];
}

export interface CodexInspectionResult {
  id: number;
  runId: number;
  accountKey: string;
  fileName: string;
  displayAccount: string;
  runtimeId?: string;
  accountSnapshot?: string;
  authIndex?: string;
  accountId?: string;
  provider: string;
  disabled: boolean;
  status?: string;
  state?: string;
  action: string;
  actionReason: string;
  actionStatus?: string;
  executedAction?: string;
  actionError?: string;
  statusCode?: number;
  usedPercent?: number;
  isQuota: boolean;
  autoRecoverEligible?: boolean;
  error?: string;
  planType?: string | null;
  quotaWindows?: CodexInspectionQuotaWindow[];
  quotaInventoryObserved?: boolean;
  errorKind?: string;
  errorDetail?: string;
  createdAtMs: number;
}

export interface CodexInspectionLog {
  id: number;
  runId: number;
  level: string;
  message: string;
  detail?: unknown;
  createdAtMs: number;
}

export interface CodexInspectionRunsResponse {
  items: CodexInspectionRun[];
}

export interface CodexInspectionRunDetail {
  run: CodexInspectionRun;
  results: CodexInspectionResult[];
  logs: CodexInspectionLog[];
}

export interface CodexInspectionActionOutcome {
  resultId?: number;
  accountKey?: string;
  fileName: string;
  displayAccount: string;
  action: string;
  status: string;
  success: boolean;
  error?: string;
}

export interface CodexInspectionActionsResponse {
  outcomes: CodexInspectionActionOutcome[];
  detail: CodexInspectionRunDetail;
}

export interface CodexInspectionActionOverride {
  resultId: number;
  action: 'delete';
}

export interface ModelPricesResponse {
  prices: Record<string, ModelPrice>;
}

export interface ModelPriceUsageStat {
  model: string;
  calls: number;
  requested_calls: number;
  resolved_calls: number;
}

export interface ModelPriceUsageSummaryResponse {
  sampled_events: number;
  total_events: number;
  truncated: boolean;
  models: ModelPriceUsageStat[];
}

export interface ModelPriceSyncCandidate {
  sourceModelId: string;
  score: number;
  reason: string;
  price: ModelPrice;
}

export interface ModelPriceSyncCandidateSet {
  model: string;
  candidates: ModelPriceSyncCandidate[];
}

export interface ModelPriceSyncSourceResult {
  source: string;
  models: number;
  skipped: number;
  error?: string;
}

export interface ModelPriceSyncResponse extends ModelPricesResponse {
  source?: string;
  sources?: string[];
  imported: number;
  skipped: number;
  matched?: Record<string, ModelPrice>;
  candidates?: ModelPriceSyncCandidateSet[];
  unmatched?: string[];
  preserved?: string[];
  proxyUsed?: boolean;
  sourceResults?: ModelPriceSyncSourceResult[];
}

export interface ApiKeyAlias {
  apiKeyHash: string;
  alias: string;
  updatedAtMs?: number;
}

export interface ApiKeyAliasesResponse {
  items: ApiKeyAlias[];
}

export type AccountActionType = 'delete' | 'reauth' | 'review' | string;
export type AccountActionStatus = 'pending' | 'ignored' | 'resolved' | 'deleted' | string;

export interface AccountActionCandidate {
  id: number;
  actionType: AccountActionType;
  status: AccountActionStatus;
  provider?: string;
  authFileName: string;
  authIndex?: string;
  accountSnapshot?: string;
  accountIdSnapshot?: string;
  authLabel?: string;
  reasonCode?: string;
  reason: string;
  autoDisableEligible?: boolean;
  autoDisabledAtMs?: number;
  evidence?: unknown;
  lastError?: string;
  firstSeenAtMs: number;
  lastSeenAtMs: number;
  hitCount: number;
  createdAtMs: number;
  updatedAtMs: number;
}

export interface AccountActionCandidatesResponse {
  items: AccountActionCandidate[];
  pendingCount: number;
}

export interface AccountActionCandidateResponse {
  item: AccountActionCandidate;
}

export interface UsageImportResponse {
  format?: string;
  added: number;
  skipped: number;
  total: number;
  failed: number;
  unsupported?: number;
  warnings?: string[];
}

export type UsageImportSessionStatus =
  | 'uploading'
  | 'ready'
  | 'processing'
  | 'completed'
  | 'failed'
  | 'cancelled';

export interface UsageImportSession {
  id: string;
  filename: string;
  status: UsageImportSessionStatus;
  size_bytes: number;
  received_bytes: number;
  chunk_size_bytes: number;
  created_at_ms: number;
  updated_at_ms: number;
  expires_at_ms: number;
  retryable?: boolean;
  error?: string;
  result?: UsageImportResponse;
}

const demoUsageImportSessions = new Map<string, UsageImportSession>();
let demoUsageImportSessionSequence = 0;

const cloneUsageImportSession = (session: UsageImportSession): UsageImportSession => ({
  ...session,
  result: session.result
    ? { ...session.result, warnings: [...(session.result.warnings ?? [])] }
    : undefined,
});

const getDemoUsageImportSession = (sessionId: string): UsageImportSession => {
  const session = demoUsageImportSessions.get(sessionId);
  if (!session) {
    const error = new Error('usage import session not found') as UsageServiceApiError;
    error.code = 'usage_import_session_not_found';
    throw error;
  }
  return cloneUsageImportSession(session);
};

const createDemoUsageImportSession = (filename: string, sizeBytes: number): UsageImportSession => {
  const now = Date.now();
  const id = `demo-usage-import-${++demoUsageImportSessionSequence}`;
  const session: UsageImportSession = {
    id,
    filename,
    status: 'uploading',
    size_bytes: sizeBytes,
    received_bytes: 0,
    chunk_size_bytes: Math.min(4 * 1024 * 1024, sizeBytes),
    created_at_ms: now,
    updated_at_ms: now,
    expires_at_ms: now + 24 * 60 * 60 * 1000,
  };
  demoUsageImportSessions.set(id, session);
  return cloneUsageImportSession(session);
};

const uploadDemoUsageImportSessionChunk = (
  sessionId: string,
  offset: number,
  chunkSize: number
): UsageImportSession => {
  const session = getDemoUsageImportSession(sessionId);
  session.received_bytes = Math.min(session.size_bytes, offset + chunkSize);
  session.status = session.received_bytes === session.size_bytes ? 'ready' : 'uploading';
  session.updated_at_ms = Date.now();
  demoUsageImportSessions.set(sessionId, session);
  return cloneUsageImportSession(session);
};

const completeDemoUsageImportSession = (sessionId: string): UsageImportSession => {
  const session = getDemoUsageImportSession(sessionId);
  session.status = 'completed';
  session.updated_at_ms = Date.now();
  session.result = { format: 'jsonl', added: 12, skipped: 0, total: 12, failed: 0 };
  demoUsageImportSessions.set(sessionId, session);
  return cloneUsageImportSession(session);
};

const cancelDemoUsageImportSession = (sessionId: string): UsageImportSession => {
  const session = getDemoUsageImportSession(sessionId);
  session.status = 'cancelled';
  session.updated_at_ms = Date.now();
  demoUsageImportSessions.set(sessionId, session);
  return cloneUsageImportSession(session);
};

export interface UsageExportResponse {
  blob: Blob;
  filename: string;
}

export interface DashboardSummaryWindow {
  today_start_ms: number;
  now_ms: number;
  rolling_30m_start_ms: number;
}

export interface DashboardTodaySummary {
  total_calls: number;
  success_calls: number;
  failure_calls: number;
  success_rate: number;
  input_tokens: number;
  output_tokens: number;
  cached_tokens: number;
  cache_read_tokens: number;
  cache_creation_tokens: number;
  reasoning_tokens: number;
  total_tokens: number;
  total_cost: number;
  average_latency_ms: number | null;
  zero_token_calls: number;
}

export interface DashboardRollingSummary {
  rpm: number;
  tpm: number;
  total_calls: number;
  total_tokens: number;
}

export interface DashboardTopModel {
  model: string;
  calls: number;
  tokens: number;
  cost: number;
  success_rate: number;
}

export interface DashboardTrafficPoint {
  bucket_ms: number;
  calls: number;
  tokens: number;
  success: number;
  failure: number;
  calls_share: number;
  tokens_share: number;
  failure_rate: number;
}

export interface DashboardHourlyActivityPoint {
  hour_index: number;
  bucket_ms: number;
  calls: number;
  tokens: number;
  intensity: number;
}

export interface DashboardTodayRequestHealthTimelinePoint {
  bucket_ms: number;
  calls: number;
  tokens: number;
  success: number;
  failure: number;
  success_rate: number;
  failure_rate: number;
  tone: 'future' | 'empty' | 'good' | 'warn' | 'bad' | string;
  intensity: number;
  future: boolean;
}

export interface DashboardTodayRequestHealthTimeline {
  from_ms: number;
  to_ms: number;
  bucket_ms: number;
  success_calls: number;
  failure_calls: number;
  total_calls: number;
  success_rate: number;
  points: DashboardTodayRequestHealthTimelinePoint[];
}

export interface DashboardTokenMixSegment {
  key: 'input' | 'output' | 'reasoning' | 'cached' | 'cache_read' | 'cache_creation' | string;
  tokens: number;
  share: number;
}

export interface DashboardModelCostRank {
  model: string;
  calls: number;
  tokens: number;
  cost: number;
  success_rate: number;
  cost_share: number;
}

export interface DashboardChannelHealth {
  auth_index: string;
  auth_label?: string;
  account?: string;
  channel?: string;
  source?: string;
  account_snapshot?: string;
  auth_label_snapshot?: string;
  auth_provider_snapshot?: string;
  calls: number;
  failures: number;
  failure_rate: number;
  success_rate: number;
  tokens: number;
  cost: number;
  average_latency_ms: number | null;
  tone: 'good' | 'warn' | 'bad' | string;
}

export interface DashboardFailureSource {
  source_hash: string;
  auth_index: string;
  auth_label?: string;
  account?: string;
  channel?: string;
  source?: string;
  account_snapshot?: string;
  auth_label_snapshot?: string;
  auth_provider_snapshot?: string;
  calls: number;
  failures: number;
  failure_rate: number;
  last_seen_ms: number;
  average_latency_ms: number | null;
  tone: 'good' | 'warn' | 'bad' | string;
}

export interface DashboardRecentFailure {
  timestamp_ms: number;
  model: string;
  api_key_hash: string;
  source_hash: string;
  auth_index: string;
  auth_label?: string;
  account?: string;
  channel?: string;
  api_key_alias?: string;
  source?: string;
  account_snapshot?: string;
  auth_label_snapshot?: string;
  auth_provider_snapshot?: string;
  auth_account_id_snapshot?: string;
  auth_project_id_snapshot?: string;
  endpoint: string;
  duration_ms: number | null;
  fail_status_code?: number | null;
  fail_summary?: string;
  response_metadata?: ResponseHeaderMetadata;
  header_quota_recover_at_ms?: number | null;
  header_quota_used_percent?: number | null;
  header_quota_plan_type?: string;
  header_error_kind?: string;
  header_error_code?: string;
  header_trace_id?: string;
}

export interface DashboardSummaryResponse {
  generated_at_ms: number;
  window: DashboardSummaryWindow;
  today: DashboardTodaySummary;
  rolling_30m: DashboardRollingSummary;
  top_models_today: DashboardTopModel[];
  model_cost_rank?: DashboardModelCostRank[];
  traffic_timeline?: DashboardTrafficPoint[];
  hourly_activity?: DashboardHourlyActivityPoint[];
  today_request_health_timeline?: DashboardTodayRequestHealthTimeline;
  token_mix?: DashboardTokenMixSegment[];
  channel_health?: DashboardChannelHealth[];
  failure_sources?: DashboardFailureSource[];
  recent_failures: DashboardRecentFailure[];
}

export interface DashboardSummaryParams {
  todayStartMs: number;
  nowMs?: number;
  topModels?: number;
  recentFailures?: number;
}

export interface MonitoringAnalyticsFilters {
  models?: string[];
  providers?: string[];
  accounts?: string[];
  credential_ids?: string[];
  auth_files?: string[];
  auth_indices?: string[];
  api_key_hashes?: string[];
  source_hashes?: string[];
  project_ids?: string[];
  request_types?: string[];
  header_error_kinds?: string[];
  header_error_codes?: string[];
  header_quota_plans?: string[];
  header_trace_ids?: string[];
  include_failed?: boolean;
  failed_only?: boolean;
  min_latency_ms?: number;
  cache_status?: string;
}

export interface MonitoringAnalyticsEventsPageRequest {
  limit?: number;
  before_ms?: number | null;
  before_id?: number | null;
}

export interface MonitoringAnalyticsDrilldownPreviewRequest {
  from_ms: number;
  to_ms: number;
  limit?: number;
}

export interface MonitoringAnalyticsInclude {
  summary?: boolean;
  summary_profile?: 'full' | 'compact';
  summary_percentiles?: boolean;
  summary_comparison?: boolean;
  timeline?: boolean;
  hourly_distribution?: boolean;
  model_share?: boolean;
  channel_share?: boolean;
  model_stats?: boolean;
  failure_sources?: boolean;
  account_stats?: boolean;
  credential_stats?: boolean;
  credential_timeline?: boolean;
  api_key_timeline?: boolean;
  api_key_stats?: boolean;
  filter_options?: boolean;
  filter_selectors?: boolean;
  heatmap?: boolean;
  anomaly_points?: boolean;
  task_buckets?: boolean;
  recent_failures?: number;
  events_page?: MonitoringAnalyticsEventsPageRequest;
  drilldown_preview?: MonitoringAnalyticsDrilldownPreviewRequest;
  granularity?: 'hour' | 'day' | string;
}

export interface MonitoringAnalyticsRequest {
  from_ms: number;
  to_ms: number;
  now_ms?: number;
  time_zone?: string;
  search_query?: string;
  search_api_key_hash?: string;
  filters?: MonitoringAnalyticsFilters;
  include?: MonitoringAnalyticsInclude;
}

export interface MonitoringAccountHistoryTarget {
  row_key: string;
  account_key?: string;
  account_snapshot?: string;
  auth_label_snapshot?: string;
  auth_file_snapshot?: string;
  auth_provider_snapshot?: string;
  auth_account_id_snapshot?: string;
  auth_project_id_snapshot?: string;
  auth_index?: string;
  source?: string;
}

export interface MonitoringAccountHistoryRequest {
  accounts: MonitoringAccountHistoryTarget[];
  catch_up?: boolean;
}

export interface MonitoringAccountHistoryCheckpoint {
  last_event_id: number;
  latest_id: number;
  pending: boolean;
  processed: number;
}

export interface MonitoringAccountLatestRequest {
  timestamp_ms: number;
  failed: boolean;
  fail_status_code?: number | null;
  fail_summary?: string;
  header_error_kind?: string;
  header_error_code?: string;
  header_trace_id?: string;
}

export interface MonitoringAccountHistoryItem {
  row_key: string;
  account_key: string;
  generated_at_ms?: number;
  matched: boolean;
  total_requests: number;
  success_calls: number;
  failure_calls: number;
  total_tokens: number;
  total_cost: number;
  success_rate: number | null;
  first_seen_ms: number | null;
  last_seen_ms: number | null;
  latest_request?: MonitoringAccountLatestRequest | null;
  recent_requests?: MonitoringAccountLatestRequest[];
  sync_status: 'ready' | 'pending' | 'empty' | string;
}

export interface MonitoringAccountHistoryResponse {
  generated_at_ms: number;
  checkpoint: MonitoringAccountHistoryCheckpoint;
  items: MonitoringAccountHistoryItem[];
}

export interface MonitoringAccountWindowUsageTarget {
  request_key?: string;
  row_key: string;
  window_key?: string;
  provider_window_id?: string;
  period?: 'current' | 'previous' | 'previous_equal_range';
  from_ms: number;
  to_ms: number;
  model_scope?: MonitoringAccountWindowModelScope;
  account_snapshot?: string;
  auth_label_snapshot?: string;
  auth_file_snapshot?: string;
  auth_provider_snapshot?: string;
  auth_account_id_snapshot?: string;
  auth_project_id_snapshot?: string;
  auth_index?: string;
  source?: string;
}

export interface MonitoringAccountWindowModelScope {
  kind: 'all' | 'family' | 'models' | 'product' | 'feature';
  key?: string;
  models?: string[];
  complete?: boolean;
}

export interface MonitoringAccountWindowUsageRequest {
  windows: MonitoringAccountWindowUsageTarget[];
}

export interface MonitoringAccountWindowUsageItem {
  request_key?: string;
  row_key: string;
  window_key?: string;
  provider_window_id?: string;
  period?: 'current' | 'previous' | 'previous_equal_range';
  from_ms: number;
  to_ms: number;
  matched: boolean;
  total_requests: number;
  success_calls: number;
  failure_calls: number;
  total_tokens: number;
  total_cost: number;
  success_rate: number | null;
  last_seen_ms: number | null;
  sync_status: 'ready' | 'empty' | string;
  scope_match_status?: 'complete' | 'partial' | 'unmatched' | string;
  unmatched_requests?: number;
}

export interface MonitoringAccountWindowUsageResponse {
  generated_at_ms: number;
  items: MonitoringAccountWindowUsageItem[];
}

export type AccountQuotaSnapshotWindowMode =
  | 'fixed'
  | 'calendar'
  | 'rolling'
  | 'non_window'
  | 'unknown';
export type AccountQuotaSnapshotSource =
  | 'api_query'
  | 'response_header'
  | 'response_body'
  | 'inspection';
export type AccountQuotaSnapshotBoundaryAccuracy = 'exact' | 'derived' | 'estimated' | 'unknown';
export type AccountQuotaSnapshotInventoryMode = 'complete' | 'partial' | 'delta';

export interface AccountQuotaSnapshotTarget {
  account_snapshot?: string;
  auth_label_snapshot?: string;
  auth_file_snapshot?: string;
  auth_provider_snapshot?: string;
  auth_account_id_snapshot?: string;
  auth_project_id_snapshot?: string;
  auth_index?: string;
  source?: string;
}

export interface AccountQuotaSnapshotResetCredit {
  id: string;
  expires_at_ms: number;
}

export interface AccountQuotaSnapshotObservationInput {
  source: AccountQuotaSnapshotSource;
  source_observation_id?: string;
  observed_at_ms?: number;
  inventory_scope_key: string;
  inventory_mode: AccountQuotaSnapshotInventoryMode;
}

export interface AccountQuotaSnapshotRemovedWindowInput {
  provider_window_id: string;
  window_kind?: string;
  model_scope_kind?: MonitoringAccountWindowModelScope['kind'];
  model_scope_key?: string;
  model_ids?: string[];
}

export interface AccountQuotaSnapshotWindowInput {
  provider_window_id: string;
  provider_window_aliases?: string[];
  window_kind: string;
  window_mode: AccountQuotaSnapshotWindowMode;
  model_scope_kind: MonitoringAccountWindowModelScope['kind'];
  model_scope_key?: string;
  model_ids?: string[];
  source: AccountQuotaSnapshotSource;
  source_observation_id?: string;
  observed_at_ms: number;
  boundary_accuracy: AccountQuotaSnapshotBoundaryAccuracy;
  cycle_start_ms?: number;
  cycle_end_ms?: number;
  duration_seconds?: number;
  used_percent?: number;
  remaining_percent?: number;
  used_value?: number;
  limit_value?: number;
  quota_unit?: string;
  reset_credits_available?: number;
  reset_credits?: AccountQuotaSnapshotResetCredit[];
  plan_type?: string;
  relationship_kind?: string;
  container_provider_window_id?: string;
}

export interface AccountQuotaSnapshotWriteEntry {
  row_key?: string;
  provider: string;
  account: AccountQuotaSnapshotTarget;
  observation?: AccountQuotaSnapshotObservationInput;
  windows: AccountQuotaSnapshotWindowInput[];
  removed_windows?: AccountQuotaSnapshotRemovedWindowInput[];
}

export interface AccountQuotaSnapshotWriteResponse {
  observed_at_ms: number;
  items: Array<{
    row_key?: string;
    account_key: string;
    provider: string;
    inserted_count: number;
  }>;
}

export interface AccountQuotaSnapshotCycle {
  id: number;
  activation_id: number;
  state: string;
  scheduled_start_ms?: number;
  scheduled_end_ms?: number;
  actual_start_ms: number;
  actual_end_ms?: number;
  duration_seconds?: number;
  boundary_accuracy: AccountQuotaSnapshotBoundaryAccuracy;
  end_reason?: string;
  parent_cycle_id?: number;
  forecast_eligible: boolean;
}

export interface AccountQuotaSnapshotWindow extends AccountQuotaSnapshotWindowInput {
  stale: boolean;
  field_sources?: Record<string, { source: AccountQuotaSnapshotSource; observed_at_ms: number }>;
  logical_window_id?: number;
  activation_generation?: number;
  availability?: string;
  first_seen_at_ms?: number;
  last_seen_at_ms?: number;
  missing_since_ms?: number;
  deactivated_at_ms?: number;
  current_cycle?: AccountQuotaSnapshotCycle;
  previous_cycle?: AccountQuotaSnapshotCycle;
}

export interface AccountQuotaSnapshotQueryAccount {
  row_key: string;
  provider: string;
  account: AccountQuotaSnapshotTarget;
}

export interface AccountQuotaSnapshotQueryResponse {
  generated_at_ms: number;
  items: Array<{
    row_key: string;
    account_key: string;
    provider: string;
    windows: AccountQuotaSnapshotWindow[];
  }>;
}

const buildDemoAccountQuotaSnapshotWindows = (
  account: AccountQuotaSnapshotQueryAccount,
  nowMs: number
): AccountQuotaSnapshotWindow[] => {
  if (account.provider === 'codex' && account.account.auth_index === 'codex-team-01') {
    const fiveHourDuration = 5 * 60 * 60;
    const weeklyDuration = 7 * 24 * 60 * 60;
    const fiveHourEndMs = nowMs + 2 * 60 * 60 * 1000 + 18 * 60 * 1000;
    const fiveHourStartMs = fiveHourEndMs - fiveHourDuration * 1000;
    const weeklyEndMs = nowMs + (3 * 24 * 60 * 60 + 8 * 60 * 60) * 1000;
    const weeklyStartMs = weeklyEndMs - weeklyDuration * 1000;
    const observedAtMs = nowMs - 8 * 60 * 1000;
    return [
      {
        provider_window_id: 'five-hour',
        window_kind: 'five_hour',
        window_mode: 'fixed',
        model_scope_kind: 'all',
        source: 'api_query',
        observed_at_ms: observedAtMs,
        boundary_accuracy: 'exact',
        cycle_start_ms: fiveHourStartMs,
        cycle_end_ms: fiveHourEndMs,
        duration_seconds: fiveHourDuration,
        used_percent: 36,
        remaining_percent: 64,
        relationship_kind: 'concurrent_subwindow',
        container_provider_window_id: 'weekly',
        stale: false,
        logical_window_id: 101,
        activation_generation: 2,
        availability: 'active',
        first_seen_at_ms: weeklyStartMs - weeklyDuration * 1000,
        last_seen_at_ms: observedAtMs,
        current_cycle: {
          id: 301,
          activation_id: 201,
          state: 'active',
          scheduled_start_ms: fiveHourStartMs,
          scheduled_end_ms: fiveHourEndMs,
          actual_start_ms: fiveHourStartMs,
          duration_seconds: fiveHourDuration,
          boundary_accuracy: 'exact',
          parent_cycle_id: 302,
          forecast_eligible: true,
        },
        previous_cycle: {
          id: 299,
          activation_id: 201,
          state: 'closed',
          scheduled_start_ms: fiveHourStartMs - fiveHourDuration * 1000,
          scheduled_end_ms: fiveHourStartMs,
          actual_start_ms: fiveHourStartMs - fiveHourDuration * 1000,
          actual_end_ms: fiveHourStartMs,
          duration_seconds: fiveHourDuration,
          boundary_accuracy: 'exact',
          end_reason: 'scheduled',
          parent_cycle_id: 298,
          forecast_eligible: true,
        },
      },
      {
        provider_window_id: 'weekly',
        window_kind: 'weekly',
        window_mode: 'fixed',
        model_scope_kind: 'all',
        source: 'api_query',
        observed_at_ms: observedAtMs,
        boundary_accuracy: 'exact',
        cycle_start_ms: weeklyStartMs,
        cycle_end_ms: weeklyEndMs,
        duration_seconds: weeklyDuration,
        used_percent: 41,
        remaining_percent: 59,
        stale: false,
        logical_window_id: 102,
        activation_generation: 1,
        availability: 'active',
        first_seen_at_ms: weeklyStartMs - weeklyDuration * 1000,
        last_seen_at_ms: observedAtMs,
        current_cycle: {
          id: 302,
          activation_id: 202,
          state: 'active',
          scheduled_start_ms: weeklyStartMs,
          scheduled_end_ms: weeklyEndMs,
          actual_start_ms: weeklyStartMs,
          duration_seconds: weeklyDuration,
          boundary_accuracy: 'exact',
          forecast_eligible: true,
        },
        previous_cycle: {
          id: 300,
          activation_id: 202,
          state: 'closed',
          scheduled_start_ms: weeklyStartMs - weeklyDuration * 1000,
          scheduled_end_ms: weeklyStartMs + 3 * 24 * 60 * 60 * 1000,
          actual_start_ms: weeklyStartMs - 3 * 24 * 60 * 60 * 1000,
          actual_end_ms: weeklyStartMs,
          duration_seconds: weeklyDuration,
          boundary_accuracy: 'exact',
          end_reason: 'early_reset',
          forecast_eligible: false,
        },
      },
      {
        provider_window_id: 'monthly',
        window_kind: 'monthly',
        window_mode: 'fixed',
        model_scope_kind: 'all',
        source: 'inspection',
        observed_at_ms: nowMs - 5 * 24 * 60 * 60 * 1000,
        boundary_accuracy: 'exact',
        cycle_start_ms: nowMs - 20 * 24 * 60 * 60 * 1000,
        cycle_end_ms: nowMs + 10 * 24 * 60 * 60 * 1000,
        duration_seconds: 30 * 24 * 60 * 60,
        used_percent: 12,
        remaining_percent: 88,
        stale: true,
        logical_window_id: 103,
        activation_generation: 1,
        availability: 'inactive',
        first_seen_at_ms: nowMs - 20 * 24 * 60 * 60 * 1000,
        last_seen_at_ms: nowMs - 5 * 24 * 60 * 60 * 1000,
        missing_since_ms: nowMs - 4 * 24 * 60 * 60 * 1000,
        deactivated_at_ms: nowMs - 4 * 24 * 60 * 60 * 1000,
      },
    ];
  }
  if (account.provider !== 'xai' || account.account.auth_index !== 'xai-ops-01') return [];
  const observedAtMs = nowMs - 60_000;
  return [
    {
      provider_window_id: 'included-free-rolling-24h',
      window_kind: 'rolling_24h',
      window_mode: 'rolling',
      model_scope_kind: 'models',
      model_scope_key: 'grok-4.5-build-free',
      model_ids: ['grok-4.5-build-free'],
      source: 'response_body',
      source_observation_id: 'demo-xai-free-usage-429',
      observed_at_ms: observedAtMs,
      boundary_accuracy: 'estimated',
      cycle_end_ms: observedAtMs + 24 * 60 * 60 * 1000,
      duration_seconds: 24 * 60 * 60,
      used_percent: 100,
      remaining_percent: 0,
      used_value: 1_024_413,
      limit_value: 1_000_000,
      quota_unit: 'tokens',
      stale: false,
      logical_window_id: 201,
      activation_generation: 1,
      availability: 'active',
      first_seen_at_ms: observedAtMs,
      last_seen_at_ms: observedAtMs,
    },
  ];
};

export interface MonitoringAnalyticsSummary {
  total_calls: number;
  success_calls: number;
  failure_calls: number;
  success_rate: number;
  input_tokens: number;
  output_tokens: number;
  cached_tokens: number;
  cache_read_tokens: number;
  cache_creation_tokens: number;
  cache_hit_rate?: number;
  reasoning_tokens: number;
  total_tokens: number;
  total_cost: number;
  average_cost_per_call?: number;
  average_latency_ms: number | null;
  p95_latency_ms?: number | null;
  p95_ttft_ms?: number | null;
  zero_token_calls: number;
  rpm_30m: number;
  tpm_30m: number;
  avg_daily_requests: number;
  avg_daily_tokens: number;
  approx_tasks: number;
  approx_task_failures: number;
  approx_task_success_rate: number;
  zero_token_models: string[];
}

export interface MonitoringAnalyticsSummaryComparison {
  from_ms: number;
  to_ms: number;
  total_calls: number;
  success_calls: number;
  failure_calls: number;
  success_rate: number;
  total_tokens: number;
  total_cost: number;
}

export interface MonitoringAnalyticsTimelinePoint {
  bucket_ms: number;
  bucket_end_ms?: number;
  label: string;
  calls: number;
  tokens: number;
  success: number;
  failure: number;
  input_tokens?: number;
  output_tokens?: number;
  cached_tokens?: number;
  cache_read_tokens?: number;
  cache_creation_tokens?: number;
  cache_hit_rate?: number;
  reasoning_tokens?: number;
  total_tokens?: number;
  cost?: number;
  average_latency_ms?: number | null;
  p95_latency_ms?: number | null;
  p95_ttft_ms?: number | null;
  success_rate?: number;
  failure_rate?: number;
}

export interface MonitoringAnalyticsHourlyPoint {
  hour: number;
  calls: number;
  tokens: number;
}

export interface MonitoringAnalyticsHeatmapContributor {
  key: string;
  label?: string;
  calls: number;
  success: number;
  failure: number;
  tokens: number;
  cost: number;
  failure_rate: number;
  share: number;
}

export interface MonitoringAnalyticsHeatmapPoint {
  weekday: number;
  hour: number;
  calls: number;
  success: number;
  failure: number;
  tokens: number;
  cost: number;
  failure_rate: number;
  model_contributors?: MonitoringAnalyticsHeatmapContributor[];
  api_key_contributors?: MonitoringAnalyticsHeatmapContributor[];
  provider_contributors?: MonitoringAnalyticsHeatmapContributor[];
}

export type MonitoringAnalyticsAnomalySeverity = 'low' | 'medium' | 'high' | string;

export interface MonitoringAnalyticsAnomalyPoint {
  bucket_ms: number;
  bucket_end_ms: number;
  label: string;
  severity: MonitoringAnalyticsAnomalySeverity;
  metric_keys: string[];
  calls: number;
  total_tokens: number;
  cost: number;
  failure_rate: number;
  request_change: number;
  cost_change: number;
  tokens_per_request_change: number;
  cache_hit_rate_change: number;
  failure_rate_change: number;
  latency_p95_change: number;
}

export interface MonitoringAnalyticsModelShareRow {
  model: string;
  calls: number;
  tokens: number;
  cost: number;
}

export interface MonitoringAnalyticsModelStat {
  model: string;
  calls: number;
  success_calls: number;
  failure_calls: number;
  success_rate: number;
  input_tokens: number;
  output_tokens: number;
  cached_tokens: number;
  cache_read_tokens: number;
  cache_creation_tokens: number;
  cache_hit_tokens?: number;
  cache_hit_input_tokens?: number;
  cache_hit_rate?: number;
  total_tokens: number;
  cost: number;
}

export interface MonitoringAnalyticsChannelShareRow {
  auth_index: string;
  source?: string;
  account_snapshot?: string;
  auth_label_snapshot?: string;
  auth_provider_snapshot?: string;
  calls: number;
  success: number;
  failure: number;
  tokens: number;
  cost: number;
  average_latency_ms: number | null;
}

export interface MonitoringAnalyticsFailureSourceRow {
  source?: string;
  source_hash: string;
  auth_index: string;
  account_snapshot?: string;
  auth_label_snapshot?: string;
  auth_provider_snapshot?: string;
  calls: number;
  failure: number;
  last_seen_ms: number;
  average_latency_ms: number | null;
}

export interface MonitoringAnalyticsAccountModelStatRow {
  model: string;
  calls: number;
  success_calls: number;
  failure_calls: number;
  success_rate: number;
  input_tokens: number;
  output_tokens: number;
  cached_tokens: number;
  cache_read_tokens: number;
  cache_creation_tokens: number;
  cache_hit_tokens?: number;
  cache_hit_input_tokens?: number;
  cache_hit_rate?: number;
  total_tokens: number;
  cost: number;
  last_seen_ms: number;
}

export interface MonitoringAnalyticsAccountStatRow {
  id: string;
  account_snapshot?: string;
  auth_label_snapshot?: string;
  auth_provider_snapshot?: string;
  auth_indices?: string[];
  sources?: string[];
  source_hashes?: string[];
  calls: number;
  success_calls: number;
  failure_calls: number;
  success_rate: number;
  input_tokens: number;
  output_tokens: number;
  cached_tokens: number;
  cache_read_tokens: number;
  cache_creation_tokens: number;
  total_tokens: number;
  cost: number;
  average_latency_ms: number | null;
  last_seen_ms: number;
  models?: MonitoringAnalyticsAccountModelStatRow[];
}

export interface MonitoringAnalyticsCredentialStatRow {
  id: string;
  auth_file_snapshot?: string;
  auth_index?: string;
  source?: string;
  source_hash?: string;
  account_snapshot?: string;
  auth_label_snapshot?: string;
  auth_provider_snapshot?: string;
  auth_account_id_snapshot?: string;
  auth_project_id_snapshot?: string;
  calls: number;
  success_calls: number;
  failure_calls: number;
  success_rate: number;
  input_tokens: number;
  output_tokens: number;
  cached_tokens: number;
  cache_read_tokens: number;
  cache_creation_tokens: number;
  total_tokens: number;
  cost: number;
  average_latency_ms: number | null;
  last_seen_ms: number;
  models?: MonitoringAnalyticsAccountModelStatRow[];
}

export interface MonitoringAnalyticsCredentialTimelinePoint {
  id: string;
  label?: string;
  auth_file_snapshot?: string;
  auth_index?: string;
  source?: string;
  source_hash?: string;
  account_snapshot?: string;
  auth_label_snapshot?: string;
  auth_provider_snapshot?: string;
  auth_account_id_snapshot?: string;
  auth_project_id_snapshot?: string;
  bucket_ms: number;
  bucket_label?: string;
  calls: number;
  tokens: number;
  success: number;
  failure: number;
  input_tokens?: number;
  output_tokens?: number;
  cached_tokens?: number;
  cache_read_tokens?: number;
  cache_creation_tokens?: number;
  reasoning_tokens?: number;
  total_tokens?: number;
  cost?: number;
  average_latency_ms?: number | null;
  success_rate?: number;
  failure_rate?: number;
}

export interface MonitoringAnalyticsApiKeyTimelinePoint {
  api_key_hash: string;
  bucket_ms: number;
  bucket_label?: string;
  calls: number;
  tokens: number;
  success: number;
  failure: number;
  input_tokens?: number;
  output_tokens?: number;
  cached_tokens?: number;
  cache_read_tokens?: number;
  cache_creation_tokens?: number;
  reasoning_tokens?: number;
  total_tokens?: number;
  cost?: number;
  average_latency_ms?: number | null;
  success_rate?: number;
  failure_rate?: number;
}

export interface MonitoringAnalyticsApiKeyStatRow {
  id: string;
  api_key_hash: string;
  account_snapshot?: string;
  auth_label_snapshot?: string;
  auth_provider_snapshot?: string;
  auth_indices?: string[];
  sources?: string[];
  source_hashes?: string[];
  calls: number;
  success_calls: number;
  failure_calls: number;
  success_rate: number;
  input_tokens: number;
  output_tokens: number;
  cached_tokens: number;
  cache_read_tokens: number;
  cache_creation_tokens: number;
  total_tokens: number;
  cost: number;
  average_latency_ms: number | null;
  last_seen_ms: number;
  models?: MonitoringAnalyticsAccountModelStatRow[];
  contexts?: MonitoringAnalyticsApiKeyContextRow[];
}

export interface MonitoringAnalyticsApiKeyContextRow {
  id: string;
  account_snapshot?: string;
  auth_label_snapshot?: string;
  auth_provider_snapshot?: string;
  auth_index?: string;
  source?: string;
  source_hash?: string;
  calls: number;
  success_calls: number;
  failure_calls: number;
  success_rate: number;
  failure_rate: number;
  total_tokens: number;
  cost: number;
  average_latency_ms?: number | null;
  last_seen_ms: number;
}

export interface MonitoringAnalyticsFilterOptions {
  account_stats?: MonitoringAnalyticsAccountStatRow[];
  api_key_stats?: MonitoringAnalyticsApiKeyStatRow[];
  channel_share?: MonitoringAnalyticsChannelShareRow[];
  model_stats?: MonitoringAnalyticsModelStat[];
  models?: string[];
  api_key_hashes?: string[];
  providers?: string[];
  auth_files?: string[];
  accounts?: string[];
  account_count?: number;
  api_key_count?: number;
  project_ids?: string[];
  request_types?: string[];
  header_error_kinds?: string[];
  header_error_codes?: string[];
  header_quota_plans?: string[];
  header_trace_ids?: string[];
}

export interface MonitoringAnalyticsTaskBucketRow {
  bucket_key: string;
  total: number;
  success: number;
  failure: number;
  first_ms: number;
  last_ms: number;
  source: string;
  source_hash: string;
  auth_index: string;
  models: string[];
  endpoints: string[];
  input_tokens: number;
  output_tokens: number;
  cached_tokens: number;
  cache_read_tokens: number;
  cache_creation_tokens: number;
  total_tokens: number;
  average_latency_ms: number | null;
  max_latency_ms: number | null;
}

export interface ResponseHeaderQuotaWindow {
  used_percent?: number;
  reset_at_ms?: number;
  reset_after_seconds?: number;
  window_minutes?: number;
}

export interface ResponseHeaderQuotaMetadata {
  plan_type?: string;
  active_limit?: string;
  rate_limit_reached_type?: string;
  summary_window_kind?: string;
  summary_window_source?: string;
  reached_window_kind?: string;
  reached_window_source?: string;
  credits_balance?: string;
  credits_has_credits?: boolean;
  credits_unlimited?: boolean;
  primary_over_secondary_limit_percent?: number;
  primary?: ResponseHeaderQuotaWindow;
  secondary?: ResponseHeaderQuotaWindow;
  recover_at_ms?: number;
  used_percent?: number;
}

export interface ResponseHeaderErrorMetadata {
  kind?: string;
  code?: string;
  authorization_error?: string;
  ide_error_code?: string;
  ide_root_error_code?: string;
  retry_after_seconds?: number;
  retry_after_recover_at_ms?: number;
  rate_limit_bypass?: string;
  should_retry?: boolean;
}

export interface ResponseHeaderTraceMetadata {
  primary_trace_id?: string;
  openai_request_id?: string;
  request_id?: string;
  oneapi_request_id?: string;
  cf_ray?: string;
  eagle_id?: string;
  cloud_ai_companion_trace_id?: string;
  client_request_id?: string;
  zeabur_request_id?: string;
  traceparent?: string;
}

export interface ResponseHeaderRoutingMetadata {
  openai_proxy_wasm?: string;
  models_etag?: string;
  new_api_version?: string;
  server?: string;
  via?: string;
  cf_cache_status?: string;
  site_cache_status?: string;
  served_by?: string;
  mife_upstream_status?: string;
}

export interface ResponseHeaderResponseMetadata {
  content_type?: string;
  content_length?: number;
  content_disposition?: string;
  server_timing?: string;
}

export interface ResponseHeaderProviderMetadata {
  antigravity_trace_id?: string;
  antigravity_server_timing?: string;
  mife_upstream_status?: string;
  oneapi_request_id?: string;
  cloudflare_ray?: string;
  cloudflare_cache_status?: string;
}

export interface ResponseHeaderRateLimitBucket {
  limit?: number;
  remaining?: number;
}

export interface ResponseHeaderRateLimitMetadata {
  requests?: ResponseHeaderRateLimitBucket;
  tokens?: ResponseHeaderRateLimitBucket;
}

export interface ResponseHeaderDataPolicyMetadata {
  retention_mode?: string;
  zero_retention?: boolean;
}

export interface ProviderUsageMetadata {
  provider?: string;
  kind?: string;
  state?: string;
  code?: string;
  model?: string;
  unit?: string;
  actual?: number;
  limit?: number;
  remaining?: number;
  overage?: number;
  window_kind?: string;
  observed_at_ms?: number;
  recover_at_ms?: number;
  recover_at_estimated?: boolean;
  source?: string;
}

export interface ResponseHeaderMetadata {
  quota?: ResponseHeaderQuotaMetadata;
  errors?: ResponseHeaderErrorMetadata;
  trace?: ResponseHeaderTraceMetadata;
  routing?: ResponseHeaderRoutingMetadata;
  response?: ResponseHeaderResponseMetadata;
  providers?: ResponseHeaderProviderMetadata;
  rate_limit?: ResponseHeaderRateLimitMetadata;
  data_policy?: ResponseHeaderDataPolicyMetadata;
  provider_usage?: ProviderUsageMetadata;
}

export interface UsageHeaderSnapshot {
  event_hash: string;
  timestamp_ms: number;
  model?: string;
  analytics_model?: string;
  requested_model?: string;
  resolved_model?: string;
  auth_file_snapshot?: string;
  auth_index?: string;
  account_snapshot?: string;
  auth_label_snapshot?: string;
  auth_provider_snapshot?: string;
  auth_account_id_snapshot?: string;
  auth_project_id_snapshot?: string;
  source?: string;
  source_hash?: string;
  response_metadata?: ResponseHeaderMetadata;
  header_quota_recover_at_ms?: number | null;
  header_quota_used_percent?: number | null;
  header_quota_plan_type?: string;
  header_error_kind?: string;
  header_error_code?: string;
  header_trace_id?: string;
}

export interface UsageHeaderSnapshotsResponse {
  generated_at_ms: number;
  from_ms: number;
  to_ms: number;
  items: UsageHeaderSnapshot[];
}

export interface MonitoringAnalyticsRecentFailure {
  timestamp_ms: number;
  model: string;
  api_key_hash: string;
  source?: string;
  source_hash: string;
  auth_index: string;
  account_snapshot?: string;
  auth_label_snapshot?: string;
  auth_provider_snapshot?: string;
  auth_account_id_snapshot?: string;
  auth_project_id_snapshot?: string;
  endpoint: string;
  duration_ms: number | null;
  fail_status_code?: number | null;
  fail_summary?: string;
  response_metadata?: ResponseHeaderMetadata;
  header_quota_recover_at_ms?: number | null;
  header_quota_used_percent?: number | null;
  header_quota_plan_type?: string;
  header_error_kind?: string;
  header_error_code?: string;
  header_trace_id?: string;
}

export interface MonitoringAnalyticsEventRow {
  request_id?: string;
  event_hash: string;
  timestamp_ms: number;
  model: string;
  analytics_model?: string;
  requested_model?: string;
  endpoint: string;
  method: string;
  path: string;
  client_ip?: string;
  x_forwarded_for?: string;
  user_agent?: string;
  auth_index: string;
  source: string;
  source_hash: string;
  api_key_hash: string;
  account_snapshot: string;
  auth_label_snapshot: string;
  auth_file_snapshot?: string;
  auth_provider_snapshot: string;
  auth_account_id_snapshot?: string;
  auth_project_id_snapshot?: string;
  resolved_model?: string;
  reasoning_effort?: string;
  service_tier?: string;
  executor_type?: string;
  input_tokens: number;
  output_tokens: number;
  cached_tokens: number;
  cache_read_tokens: number;
  cache_creation_tokens: number;
  reasoning_tokens: number;
  total_tokens: number;
  latency_ms: number | null;
  ttft_ms?: number | null;
  failed: boolean;
  fail_status_code?: number | null;
  fail_summary?: string;
  response_metadata?: ResponseHeaderMetadata;
  header_quota_recover_at_ms?: number | null;
  header_quota_used_percent?: number | null;
  header_quota_plan_type?: string;
  header_error_kind?: string;
  header_error_code?: string;
  header_trace_id?: string;
}

export interface MonitoringAnalyticsEventsResponse {
  items: MonitoringAnalyticsEventRow[];
  next_before_ms: number;
  next_before_id?: number;
  has_more: boolean;
  total_count?: number;
}

export interface MonitoringAnalyticsResponse {
  generated_at_ms: number;
  granularity: 'hour' | 'day' | string;
  summary?: MonitoringAnalyticsSummary;
  summary_comparison?: MonitoringAnalyticsSummaryComparison;
  timeline?: MonitoringAnalyticsTimelinePoint[];
  hourly_distribution?: MonitoringAnalyticsHourlyPoint[];
  heatmap?: MonitoringAnalyticsHeatmapPoint[];
  anomaly_points?: MonitoringAnalyticsAnomalyPoint[];
  model_share?: MonitoringAnalyticsModelShareRow[];
  model_stats?: MonitoringAnalyticsModelStat[];
  channel_share?: MonitoringAnalyticsChannelShareRow[];
  failure_sources?: MonitoringAnalyticsFailureSourceRow[];
  account_stats?: MonitoringAnalyticsAccountStatRow[];
  credential_stats?: MonitoringAnalyticsCredentialStatRow[];
  credential_timeline?: MonitoringAnalyticsCredentialTimelinePoint[];
  api_key_timeline?: MonitoringAnalyticsApiKeyTimelinePoint[];
  api_key_stats?: MonitoringAnalyticsApiKeyStatRow[];
  filter_options?: MonitoringAnalyticsFilterOptions;
  task_buckets?: MonitoringAnalyticsTaskBucketRow[];
  recent_failures?: MonitoringAnalyticsRecentFailure[];
  events?: MonitoringAnalyticsEventsResponse;
  drilldown_preview?: MonitoringAnalyticsEventsResponse;
}

const USAGE_SERVICE_TIMEOUT_MS = 30 * 1000;
const USAGE_SERVICE_TRANSFER_TIMEOUT_MS = 60 * 1000;
const USAGE_IMPORT_CHUNK_TIMEOUT_MS = 5 * 60 * 1000;
const CODEX_INSPECTION_RUN_TIMEOUT_MS = 10 * 60 * 1000;
export const USAGE_SERVICE_ID = 'cpa-manager-plus';
export const LEGACY_USAGE_SERVICE_ID = 'cpa-manager';
export const LEGACY_USAGE_SERVICE_IDS = [LEGACY_USAGE_SERVICE_ID, 'cpa-usage-service'] as const;
export const USAGE_SERVICE_LAST_CPA_BASE_KEY = 'cpa-manager-plus:last-cpa-base';
export const LEGACY_USAGE_SERVICE_LAST_CPA_BASE_KEY = 'cpa-manager:last-cpa-base';
export const LEGACY_USAGE_SERVICE_LAST_CPA_BASE_KEYS = [
  LEGACY_USAGE_SERVICE_LAST_CPA_BASE_KEY,
  'cpa-usage-service:last-cpa-base',
] as const;

export const isUsageServiceId = (service?: string): boolean =>
  service === USAGE_SERVICE_ID ||
  (typeof service === 'string' &&
    (LEGACY_USAGE_SERVICE_IDS as readonly string[]).includes(service));

export const normalizeUsageServiceBase = (input: string): string => normalizeApiBase(input);

const buildUrl = (base: string, path: string): string => {
  const normalized = normalizeUsageServiceBase(base).replace(/\/+$/, '');
  return `${normalized}${path}`;
};

const authHeaders = (managementKey?: string) =>
  managementKey ? { Authorization: `Bearer ${managementKey}` } : undefined;

const isRecord = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === 'object';

const readUsageServiceErrorCode = (value: unknown): string => {
  if (!isRecord(value) || typeof value.code !== 'string') return '';
  return USAGE_SERVICE_ERROR_CODES.has(value.code) ? value.code : '';
};

const fallbackUsageServiceCodeByStatus = (status?: number): string => {
  switch (status) {
    case 401:
      return 'invalid_admin_key';
    case 405:
      return 'method_not_allowed';
    case 412:
      return 'usage_service_not_configured';
    default:
      return '';
  }
};

export const getUsageServiceErrorCode = (error: unknown): string => {
  if (axios.isAxiosError(error)) {
    return (
      readUsageServiceErrorCode(error.response?.data) ||
      fallbackUsageServiceCodeByStatus(error.response?.status)
    );
  }

  if (!isRecord(error)) return '';
  const code = typeof error.code === 'string' ? error.code : '';
  if (USAGE_SERVICE_ERROR_CODES.has(code)) return code;
  return readUsageServiceErrorCode(error.data) || readUsageServiceErrorCode(error.details);
};

const readUsageServiceErrorMessage = (value: unknown): string => {
  if (!isRecord(value)) return '';
  if (typeof value.error === 'string') return value.error;
  if (typeof value.message === 'string') return value.message;
  return '';
};

const toUsageServiceApiError = (error: unknown): UsageServiceApiError => {
  if (axios.isAxiosError(error)) {
    const data = error.response?.data;
    const message =
      readUsageServiceErrorMessage(data) || error.message || 'Manager Server request failed';
    const apiError = new Error(message) as UsageServiceApiError;
    apiError.name = 'UsageServiceApiError';
    apiError.status = error.response?.status;
    apiError.code = getUsageServiceErrorCode(error) || error.code;
    apiError.details = data;
    apiError.data = data;
    return apiError;
  }

  if (error instanceof Error) return error as UsageServiceApiError;
  const fallback = new Error(
    typeof error === 'string' ? error : 'Manager Server request failed'
  ) as UsageServiceApiError;
  fallback.name = 'UsageServiceApiError';
  return fallback;
};

const withUsageServiceError = async <T>(operation: () => Promise<T>): Promise<T> => {
  try {
    return await operation();
  } catch (error) {
    throw toUsageServiceApiError(error);
  }
};

const readHeader = (headers: unknown, name: string): string => {
  if (!headers || typeof headers !== 'object') return '';
  const getter = (headers as { get?: (key: string) => unknown }).get;
  if (typeof getter === 'function') {
    const value = getter.call(headers, name);
    return value === undefined || value === null ? '' : String(value);
  }
  const target = name.toLowerCase();
  const entries = Object.entries(headers as Record<string, unknown>);
  const match = entries.find(([key]) => key.toLowerCase() === target);
  return match?.[1] === undefined || match?.[1] === null ? '' : String(match[1]);
};

const parseContentDispositionFilename = (value: string): string => {
  const utf8Match = value.match(/filename\*=UTF-8''([^;]+)/i);
  if (utf8Match?.[1]) {
    try {
      return decodeURIComponent(utf8Match[1].trim());
    } catch {
      return utf8Match[1].trim();
    }
  }
  const quotedMatch = value.match(/filename="([^"]+)"/i);
  if (quotedMatch?.[1]) return quotedMatch[1].trim();
  const plainMatch = value.match(/filename=([^;]+)/i);
  return plainMatch?.[1]?.trim() || '';
};

const getDemoAccountActionCandidateResponse = (
  id: number,
  status?: AccountActionStatus
): AccountActionCandidateResponse => {
  const candidates = getDemoAccountActionCandidates().items;
  const fallback = candidates[0];
  const item = candidates.find((candidate) => candidate.id === id) ?? fallback;
  return {
    item: {
      ...item,
      status: status ?? item.status,
      updatedAtMs: Date.now(),
    },
  };
};

const getDemoAccountActionCandidatesResponse = (
  status: string,
  limit: number
): AccountActionCandidatesResponse => {
  const response = getDemoAccountActionCandidates();
  const filtered =
    !status || status === 'all'
      ? response.items
      : response.items.filter((item) => item.status === status);
  return {
    items: filtered.slice(0, limit),
    pendingCount: response.pendingCount,
  };
};

const getDemoPatchedAccountProcessingPolicy = (
  patch: AccountProcessingPolicyPatch
): AccountProcessingPolicy => {
  const policy = getDemoAccountProcessingPolicy();
  return {
    ...policy,
    updatedAtMs: Date.now(),
    codexQuotaCooldown: {
      ...policy.codexQuotaCooldown,
      enabled: patch.codexQuotaCooldownEnabled ?? policy.codexQuotaCooldown.enabled,
    },
    authIssueQueue: {
      ...policy.authIssueQueue,
      enabled: patch.authIssueQueueEnabled ?? policy.authIssueQueue.enabled,
    },
    authIssueAutoDisable: {
      ...policy.authIssueAutoDisable,
      enabled: patch.authIssueAutoDisableEnabled ?? policy.authIssueAutoDisable.enabled,
    },
  };
};

const createDemoCodexInspectionError = (message: string, status: number): UsageServiceApiError => {
  const details = { error: message, code: 'request_failed' };
  const error = new Error(message) as UsageServiceApiError;
  error.name = 'UsageServiceApiError';
  error.status = status;
  error.code = 'request_failed';
  error.details = details;
  error.data = details;
  return error;
};

const cloneDemoCodexInspectionDetail = (
  detail: CodexInspectionRunDetail
): CodexInspectionRunDetail => JSON.parse(JSON.stringify(detail)) as CodexInspectionRunDetail;

export const isDemoCodexInspectionStatusMutationAmbiguous = (
  _results: Array<Pick<CodexInspectionResult, 'accountKey' | 'fileName'>>,
  result: Pick<
    CodexInspectionResult,
    | 'accountKey'
    | 'fileName'
    | 'action'
    | 'authIndex'
    | 'accountId'
    | 'provider'
    | 'accountSnapshot'
  >,
  authFiles = getDemoAuthFiles().files
): boolean => {
  if (result.action !== 'disable' && result.action !== 'enable') return false;
  const fileName = result.fileName.trim();
  if (!fileName) return true;
  const accountSnapshot = result.accountSnapshot?.trim() ?? '';
  const resolution = resolveAuthFileStatusMutationTarget(authFiles, {
    name: fileName,
    authIndex: result.authIndex,
    provider: result.provider,
    accountId: result.accountId,
    accountSnapshot: accountSnapshot && accountSnapshot !== fileName ? accountSnapshot : null,
  });
  return (
    resolution.failure !== null ||
    resolution.scope === 'ambiguous' ||
    resolution.scope === 'expanded-child'
  );
};

type DemoSourceFileStatusActionPlan = {
  canonicalResultId: number;
  action: 'disable' | 'enable';
  memberResultIds: Set<number>;
};

export const buildDemoCodexInspectionSourceFileStatusActionPlans = (
  results: CodexInspectionResult[],
  authFiles: AuthFileItem[] = getDemoAuthFiles().files
): Map<string, DemoSourceFileStatusActionPlan> => {
  const plans = new Map<string, DemoSourceFileStatusActionPlan>();
  const filesByName = new Map<string, AuthFileItem[]>();
  authFiles.forEach((file) => {
    const fileName = readAuthFileStatusPhysicalName(file);
    if (!fileName) return;
    const siblings = filesByName.get(fileName) ?? [];
    siblings.push(file);
    filesByName.set(fileName, siblings);
  });

  const resolved = results.flatMap((result) => {
    if (result.action !== 'disable' && result.action !== 'enable') return [];
    const accountSnapshot = result.accountSnapshot?.trim() ?? '';
    const resolution = resolveAuthFileStatusMutationTarget(authFiles, {
      name: result.fileName,
      authIndex: result.authIndex,
      provider: result.provider,
      accountId: result.accountId,
      accountSnapshot:
        accountSnapshot && accountSnapshot !== result.fileName.trim() ? accountSnapshot : null,
    });
    if (!resolution.target || resolution.failure !== null) return [];
    return [{ result, resolution }];
  });

  filesByName.forEach((currentFiles, fileName) => {
    if (currentFiles.length <= 1) return;
    const entries = resolved.filter(
      (entry) => readAuthFileStatusPhysicalName(entry.resolution.target!) === fileName
    );
    const matchedEntries: typeof entries = [];
    const members: number[] = [];
    let action: 'disable' | 'enable' | null = null;
    for (const currentFile of currentFiles) {
      const matches = entries.filter((entry) => entry.resolution.target === currentFile);
      if (matches.length !== 1) return;
      const matchedAction = matches[0].result.action;
      if (matchedAction !== 'disable' && matchedAction !== 'enable') return;
      if (action === null) action = matchedAction;
      if (matchedAction !== action) return;
      matchedEntries.push(matches[0]);
      members.push(matches[0].result.id);
    }
    const sourceEntries = matchedEntries.filter(
      (entry) => entry.resolution.scope === 'source-file'
    );
    if (!action || sourceEntries.length > 1) return;
    const canonicalEntry = sourceEntries[0] ?? matchedEntries[0];
    if (!canonicalEntry) return;
    plans.set(fileName, {
      canonicalResultId: canonicalEntry.result.id,
      action,
      memberResultIds: new Set(members),
    });
  });
  return plans;
};

export const getDemoCodexInspectionActionIdentityKey = (
  item: Pick<
    CodexInspectionResult,
    'fileName' | 'provider' | 'authIndex' | 'accountId' | 'accountSnapshot' | 'displayAccount'
  >
): string => {
  const fileName = item.fileName.trim();
  const accountSnapshot = item.accountSnapshot?.trim() ?? '';
  return getAuthFileStatusIdentityKey({
    name: fileName,
    provider: item.provider,
    authIndex: item.authIndex,
    accountId: item.accountId,
    accountSnapshot: accountSnapshot && accountSnapshot !== fileName ? accountSnapshot : null,
  });
};

let demoCodexInspectionRunState: CodexInspectionRunDetail | null = null;

export const resetDemoCodexInspectionRunState = () => {
  demoCodexInspectionRunState = null;
};

const readDemoCodexInspectionRunState = (): CodexInspectionRunDetail => {
  if (!demoCodexInspectionRunState) {
    demoCodexInspectionRunState = getDemoCodexInspectionRun();
  }
  return cloneDemoCodexInspectionDetail(demoCodexInspectionRunState);
};

const replaceDemoCodexInspectionRunState = (
  detail: CodexInspectionRunDetail
): CodexInspectionRunDetail => {
  demoCodexInspectionRunState = cloneDemoCodexInspectionDetail(detail);
  return cloneDemoCodexInspectionDetail(demoCodexInspectionRunState);
};

const getDemoCodexInspectionActionsResponse = (
  runId: number,
  resultIds: number[],
  actionOverrides: CodexInspectionActionOverride[] = []
): CodexInspectionActionsResponse => {
  const requestedIDs = new Set(resultIds.filter((resultId) => resultId > 0));
  if (requestedIDs.size === 0) {
    throw createDemoCodexInspectionError('codex inspection action result ids are required', 400);
  }

  const detail = readDemoCodexInspectionRunState();
  if (runId !== detail.run.id) {
    throw createDemoCodexInspectionError('codex inspection run not found', 404);
  }

  const completedAt = Date.now();
  const overrideByID = new Map<number, 'delete'>();
  actionOverrides.forEach((item) => {
    const result = detail.results.find((candidate) => candidate.id === item.resultId);
    if (
      item.resultId <= 0 ||
      item.action !== 'delete' ||
      !requestedIDs.has(item.resultId) ||
      result?.action !== 'reauth'
    ) {
      throw createDemoCodexInspectionError('codex inspection action override is invalid', 400);
    }
    overrideByID.set(item.resultId, item.action);
  });

  const manualResults = detail.results.map((result) => {
    const action = overrideByID.get(result.id);
    return action ? { ...result, action } : result;
  });
  const selected = manualResults.filter((result) => requestedIDs.has(result.id));
  if (selected.length === 0) {
    throw createDemoCodexInspectionError('codex inspection has no actionable results', 400);
  }
  const executableActions = new Set(['delete', 'disable', 'enable']);
  const normalizeActionStatus = (result: CodexInspectionResult): string => {
    switch (result.actionStatus) {
      case 'none':
      case 'pending':
      case 'success':
      case 'failed':
      case 'skipped':
      case 'needs_review':
        return result.actionStatus;
      default:
        return executableActions.has(result.action) ? 'pending' : 'none';
    }
  };
  const sourceFilePlans = buildDemoCodexInspectionSourceFileStatusActionPlans(
    selected.filter((result) => {
      const status = normalizeActionStatus(result);
      return status !== 'success' && status !== 'skipped' && status !== 'needs_review';
    })
  );
  type DemoActionGroup = { key: string; items: CodexInspectionResult[]; mixed: boolean };
  const itemsByFileName = new Map<string, CodexInspectionResult[]>();
  manualResults.forEach((result) => {
    const fileName = result.fileName.trim();
    if (!fileName) return;
    const fileItems = itemsByFileName.get(fileName) ?? [];
    fileItems.push(result);
    itemsByFileName.set(fileName, fileItems);
  });
  const groupByResultID = new Map<number, DemoActionGroup>();
  itemsByFileName.forEach((allFileItems, fileName) => {
    const fileItems = allFileItems.filter((item) => executableActions.has(item.action));
    if (fileItems.length === 0) return;
    if (fileItems.some((item) => item.action === 'delete')) {
      const group = {
        key: `file:${fileName}`,
        items: fileItems,
        mixed: allFileItems.some((item) => item.action !== 'delete'),
      };
      fileItems.forEach((item) => groupByResultID.set(item.id, group));
      return;
    }
    const identityGroups = new Map<string, DemoActionGroup>();
    fileItems.forEach((item) => {
      const identityKey = getDemoCodexInspectionActionIdentityKey(item);
      const group = identityGroups.get(identityKey) ?? {
        key: `credential:${identityKey}`,
        items: [],
        mixed: false,
      };
      if (group.items.length > 0 && group.items[0].action !== item.action) group.mixed = true;
      group.items.push(item);
      identityGroups.set(identityKey, group);
      groupByResultID.set(item.id, group);
    });
  });
  const seenGroupKeys = new Set<string>();
  const plannedOutcomes = selected.map((result) => {
    const action = result.action;
    const currentStatus = normalizeActionStatus(result);
    if (!executableActions.has(action)) {
      return {
        result,
        action,
        status: 'skipped',
        success: true,
        error: '该巡检结果不是可执行动作',
      };
    }
    if (currentStatus === 'success') {
      return {
        result,
        action,
        status: 'skipped',
        success: true,
        error: '该建议动作已执行成功',
      };
    }
    if (currentStatus === 'skipped') {
      return {
        result,
        action,
        status: 'skipped',
        success: true,
        error: '该建议动作已跳过',
      };
    }
    if (currentStatus === 'needs_review') {
      return {
        result,
        action,
        status: 'needs_review',
        success: true,
        error: '该建议动作需要到凭证管理中人工处理',
      };
    }
    const fileName = result.fileName.trim();
    if (!fileName) {
      return {
        result,
        action,
        status: 'failed',
        success: false,
        error: '认证文件名为空，无法执行',
      };
    }
    if (
      !hasCodexInspectionStableIdentity({
        fileName: result.fileName,
        runtimeId: result.runtimeId,
        provider: result.provider,
        authIndex: result.authIndex,
        accountId: result.accountId,
        accountSnapshot: result.accountSnapshot,
      })
    ) {
      return {
        result,
        action,
        status: 'needs_review',
        success: true,
        error: '巡检结果缺少稳定账号标识，已阻止处理，请人工确认',
      };
    }
    const sourceFilePlan = sourceFilePlans.get(fileName);
    if (
      sourceFilePlan?.memberResultIds.has(result.id) &&
      sourceFilePlan.canonicalResultId !== result.id
    ) {
      return {
        result,
        action,
        status: 'skipped',
        success: true,
        error: '该认证目标已由另一条结果处理',
      };
    }
    if (
      sourceFilePlan?.canonicalResultId !== result.id &&
      isDemoCodexInspectionStatusMutationAmbiguous(manualResults, result)
    ) {
      return {
        result,
        action,
        status: 'needs_review',
        success: true,
        error:
          '认证凭证缺少唯一 runtime ID，或 runtime ID 与物理文件选择器冲突，已阻止状态修改，请人工确认',
      };
    }
    const group = groupByResultID.get(result.id) ?? {
      key: `credential:${result.id}`,
      items: [result],
      mixed: false,
    };
    if (group.mixed) {
      return {
        result,
        action,
        status: 'needs_review',
        success: true,
        error: '同一认证文件下存在多个不同建议动作，文件级处理已阻止，请到凭证管理中手动处理',
      };
    }
    if (seenGroupKeys.has(group.key)) {
      return {
        result,
        action,
        status: 'skipped',
        success: true,
        error: '该认证目标已由另一条结果处理',
      };
    }
    seenGroupKeys.add(group.key);
    return {
      result,
      action,
      status: 'success',
      success: true,
      error: '',
    };
  });
  const outcomeByID = new Map(plannedOutcomes.map((outcome) => [outcome.result.id, outcome]));
  const actionCount = plannedOutcomes.filter((outcome) => outcome.status === 'success').length;
  let nextLogID = detail.logs.reduce((maximum, entry) => Math.max(maximum, entry.id), 0) + 1;
  const createLog = (level: string, message: string, logDetail: unknown) => ({
    id: nextLogID++,
    runId: detail.run.id,
    level,
    message,
    detail: logDetail,
    createdAtMs: completedAt,
  });

  detail.logs.push(
    createLog('info', '手动处理账号开始', {
      requestedCount: resultIds.length,
      actionCount,
    })
  );
  plannedOutcomes
    .filter((outcome) => outcome.status !== 'success')
    .forEach((outcome) => {
      const failed = outcome.status === 'failed' || !outcome.success;
      detail.logs.push(
        createLog(
          failed ? 'error' : outcome.status === 'needs_review' ? 'warning' : 'info',
          failed ? '手动处理账号失败' : '手动处理账号跳过',
          {
            fileName: outcome.result.fileName,
            displayAccount: outcome.result.displayAccount,
            action: outcome.action,
            status: outcome.status,
            reason: outcome.error,
          }
        )
      );
    });
  detail.results = detail.results.map((result) => {
    const outcome = outcomeByID.get(result.id);
    if (!outcome) return result;
    if (normalizeActionStatus(result) === 'success' && outcome.status === 'skipped') {
      return result;
    }
    if (outcome.status !== 'success') {
      return {
        ...result,
        actionStatus: outcome.status,
        executedAction: undefined,
        actionError: outcome.error,
      };
    }
    return {
      ...result,
      disabled:
        outcome.action === 'disable' ? true : outcome.action === 'enable' ? false : result.disabled,
      actionStatus: 'success',
      executedAction: outcome.action,
      actionError: undefined,
    };
  });
  plannedOutcomes
    .filter((outcome) => outcome.status === 'success')
    .forEach((outcome) => {
      detail.logs.push(
        createLog('success', '手动处理账号成功', {
          fileName: outcome.result.fileName,
          displayAccount: outcome.result.displayAccount,
          action: outcome.action,
        })
      );
    });
  const outcomeSummary = plannedOutcomes.reduce(
    (summary, outcome) => {
      if (outcome.status === 'success') summary.success += 1;
      else if (outcome.status === 'skipped') summary.skipped += 1;
      else if (outcome.status === 'needs_review') summary.needsReview += 1;
      else summary.failed += 1;
      return summary;
    },
    { success: 0, failed: 0, skipped: 0, needsReview: 0 }
  );
  detail.logs.push(
    createLog(
      outcomeSummary.failed > 0 || outcomeSummary.needsReview > 0 ? 'warning' : 'success',
      '手动处理账号完成',
      {
        successCount: outcomeSummary.success,
        failedCount: outcomeSummary.failed,
        skippedCount: outcomeSummary.skipped,
        needsReviewCount: outcomeSummary.needsReview,
        resultWriteFailedCount: 0,
      }
    )
  );
  detail.run.disabledCount = detail.results.filter((result) => result.disabled).length;
  detail.run.enabledCount = detail.results.length - detail.run.disabledCount;
  detail.run.error =
    outcomeSummary.failed > 0
      ? `${outcomeSummary.failed} 个手动处理动作执行失败，详见巡检日志`
      : undefined;
  detail.run.updatedAtMs = completedAt;
  const nextDetail = replaceDemoCodexInspectionRunState(detail);
  return {
    outcomes: plannedOutcomes.map((outcome) => ({
      resultId: outcome.result.id,
      accountKey: outcome.result.accountKey,
      fileName: outcome.result.fileName,
      displayAccount: outcome.result.displayAccount,
      action: outcome.action,
      status: outcome.status,
      success: outcome.success,
      ...(outcome.error ? { error: outcome.error } : {}),
    })),
    detail: nextDetail,
  };
};

const getDemoModelPriceSyncResponse = (models?: string[]): ModelPriceSyncResponse => {
  const prices = getDemoModelPrices().prices;
  const selectedModels = new Set((models || []).map((model) => model.trim()).filter(Boolean));
  const selectedPrices =
    selectedModels.size > 0
      ? Object.fromEntries(Object.entries(prices).filter(([model]) => selectedModels.has(model)))
      : prices;

  return {
    prices: selectedPrices,
    source: 'demo',
    sources: ['demo'],
    imported: Object.keys(selectedPrices).length,
    skipped: 0,
    matched: selectedPrices,
    candidates: [],
    unmatched: [],
    proxyUsed: false,
    sourceResults: [{ source: 'demo', models: Object.keys(selectedPrices).length, skipped: 0 }],
  };
};

export const usageServiceApi = {
  getInfo: async (base: string): Promise<UsageServiceInfo> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      return getDemoUsageServiceInfo();
    }

    return withUsageServiceError(async () => {
      const response = await axios.get<UsageServiceInfo>(buildUrl(base, '/usage-service/info'), {
        timeout: USAGE_SERVICE_TIMEOUT_MS,
      });
      return response.data;
    });
  },

  setup: async (
    base: string,
    payload: UsageServiceSetupRequest,
    adminKey?: string
  ): Promise<void> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      return;
    }

    await withUsageServiceError(async () => {
      await axios.post(buildUrl(base, '/setup'), payload, {
        timeout: USAGE_SERVICE_TIMEOUT_MS,
        headers: authHeaders(adminKey),
      });
    });
  },

  getManagerConfig: async (
    base: string,
    managementKey?: string
  ): Promise<ManagerConfigResponse> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      return getDemoManagerConfig();
    }

    return withUsageServiceError(async () => {
      const response = await axios.get<ManagerConfigResponse>(
        buildUrl(base, '/usage-service/config'),
        {
          timeout: USAGE_SERVICE_TIMEOUT_MS,
          headers: authHeaders(managementKey),
        }
      );
      return response.data;
    });
  },

  saveManagerConfig: async (
    base: string,
    config: ManagerConfig,
    managementKey?: string
  ): Promise<ManagerConfigResponse> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      const submittedKey = config.cpaConnection.managementKey?.trim();
      return {
        ...getDemoManagerConfig(),
        config: {
          ...config,
          cpaConnection: {
            cpaBaseUrl: config.cpaConnection.cpaBaseUrl,
            managementKeyConfigured: Boolean(
              submittedKey || config.cpaConnection.managementKeyConfigured
            ),
          },
        },
        source: 'db',
      };
    }

    return withUsageServiceError(async () => {
      const response = await axios.put<ManagerConfigResponse>(
        buildUrl(base, '/usage-service/config'),
        { config },
        {
          timeout: USAGE_SERVICE_TIMEOUT_MS,
          headers: authHeaders(managementKey),
        }
      );
      return response.data;
    });
  },

  listCodexInspectionRuns: async (
    base: string,
    managementKey?: string,
    limit = 20
  ): Promise<CodexInspectionRunsResponse> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      const detail = readDemoCodexInspectionRunState();
      return { items: [detail.run].slice(0, limit) };
    }

    return withUsageServiceError(async () => {
      const response = await axios.get<CodexInspectionRunsResponse>(
        buildUrl(base, '/v0/management/codex-inspection/runs'),
        {
          timeout: USAGE_SERVICE_TIMEOUT_MS,
          headers: authHeaders(managementKey),
          params: { limit },
        }
      );
      return response.data;
    });
  },

  getCodexInspectionRun: async (
    base: string,
    managementKey: string | undefined,
    id: number
  ): Promise<CodexInspectionRunDetail> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      const detail = readDemoCodexInspectionRunState();
      if (id !== detail.run.id) {
        throw createDemoCodexInspectionError('codex inspection run not found', 404);
      }
      return detail;
    }

    return withUsageServiceError(async () => {
      const response = await axios.get<CodexInspectionRunDetail>(
        buildUrl(base, `/v0/management/codex-inspection/runs/${id}`),
        {
          timeout: USAGE_SERVICE_TIMEOUT_MS,
          headers: authHeaders(managementKey),
        }
      );
      return response.data;
    });
  },

  runCodexInspection: async (
    base: string,
    managementKey?: string
  ): Promise<CodexInspectionRunDetail> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      return replaceDemoCodexInspectionRunState(getDemoCodexInspectionRun());
    }

    return withUsageServiceError(async () => {
      const response = await axios.post<CodexInspectionRunDetail>(
        buildUrl(base, '/v0/management/codex-inspection/run'),
        undefined,
        {
          timeout: CODEX_INSPECTION_RUN_TIMEOUT_MS,
          headers: authHeaders(managementKey),
        }
      );
      return response.data;
    });
  },

  cancelCodexInspectionRun: async (
    base: string,
    managementKey: string | undefined,
    runId: number
  ): Promise<CodexInspectionRunDetail> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      const detail = readDemoCodexInspectionRunState();
      if (runId !== detail.run.id) {
        throw createDemoCodexInspectionError('codex inspection run not found', 404);
      }
      if (detail.run.status === 'cancelled') {
        return detail;
      }
      if (detail.run.status !== 'running' && detail.run.status !== 'cancelling') {
        throw createDemoCodexInspectionError('codex inspection run cannot be cancelled', 409);
      }
      const completedAt = Date.now();
      detail.run.status = 'cancelled';
      detail.run.active = false;
      detail.run.cancellable = false;
      detail.run.finishedAtMs = completedAt;
      detail.run.updatedAtMs = completedAt;
      detail.run.error = '用户主动取消巡检';
      detail.logs.push({
        id: detail.logs.reduce((maximum, entry) => Math.max(maximum, entry.id), 0) + 1,
        runId,
        level: 'warning',
        message: '凭证健康巡检已取消',
        detail: { status: 'cancelled' },
        createdAtMs: completedAt,
      });
      return replaceDemoCodexInspectionRunState(detail);
    }

    return withUsageServiceError(async () => {
      const response = await axios.post<CodexInspectionRunDetail>(
        buildUrl(base, `/v0/management/codex-inspection/runs/${runId}/cancel`),
        undefined,
        {
          timeout: USAGE_SERVICE_TIMEOUT_MS,
          headers: authHeaders(managementKey),
        }
      );
      return response.data;
    });
  },

  executeCodexInspectionActions: async (
    base: string,
    managementKey: string | undefined,
    runId: number,
    resultIds: number[],
    actionOverrides: CodexInspectionActionOverride[] = []
  ): Promise<CodexInspectionActionsResponse> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      return getDemoCodexInspectionActionsResponse(runId, resultIds, actionOverrides);
    }

    return withUsageServiceError(async () => {
      const response = await axios.post<CodexInspectionActionsResponse>(
        buildUrl(base, `/v0/management/codex-inspection/runs/${runId}/actions`),
        { resultIds, actionOverrides },
        {
          timeout: CODEX_INSPECTION_RUN_TIMEOUT_MS,
          headers: authHeaders(managementKey),
        }
      );
      return response.data;
    });
  },

  getStatus: async (
    base: string,
    managementKey?: string,
    scope?: UsageServiceStatusScope
  ): Promise<UsageServiceStatus> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      return getDemoUsageServiceStatus();
    }

    return withUsageServiceError(async () => {
      const statusPath =
        scope === 'database-maintenance' ? '/status?scope=database-maintenance' : '/status';
      const response = await axios.get<UsageServiceStatus>(buildUrl(base, statusPath), {
        timeout: USAGE_SERVICE_TIMEOUT_MS,
        headers: authHeaders(managementKey),
      });
      return response.data;
    });
  },

  getAccountProcessingPolicy: async (
    base: string,
    managementKey?: string
  ): Promise<AccountProcessingPolicy> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      return getDemoAccountProcessingPolicy();
    }

    return withUsageServiceError(async () => {
      const response = await axios.get<AccountProcessingPolicy>(
        buildUrl(base, '/usage-service/account-processing-policy'),
        {
          timeout: USAGE_SERVICE_TIMEOUT_MS,
          headers: authHeaders(managementKey),
        }
      );
      return response.data;
    });
  },

  getActiveQuotaCooldowns: async (
    base: string,
    managementKey?: string
  ): Promise<QuotaCooldownInfo[]> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      return getDemoQuotaCooldowns();
    }

    return withUsageServiceError(async () => {
      const response = await axios.get<QuotaCooldownsResponse>(
        buildUrl(base, '/usage-service/quota-cooldowns'),
        {
          timeout: USAGE_SERVICE_TIMEOUT_MS,
          headers: authHeaders(managementKey),
        }
      );
      return response.data.items ?? [];
    });
  },

  updateAccountProcessingPolicy: async (
    base: string,
    managementKey: string,
    patch: AccountProcessingPolicyPatch
  ): Promise<AccountProcessingPolicy> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      return getDemoPatchedAccountProcessingPolicy(patch);
    }

    return withUsageServiceError(async () => {
      const response = await axios.patch<AccountProcessingPolicy>(
        buildUrl(base, '/usage-service/account-processing-policy'),
        patch,
        {
          timeout: USAGE_SERVICE_TIMEOUT_MS,
          headers: authHeaders(managementKey),
        }
      );
      return response.data;
    });
  },

  getUsage: async (base: string, managementKey?: string): Promise<UsagePayload> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      return getDemoUsagePayload() as UsagePayload;
    }

    return withUsageServiceError(async () => {
      const response = await axios.get<UsagePayload>(buildUrl(base, '/v0/management/usage'), {
        timeout: USAGE_SERVICE_TIMEOUT_MS,
        headers: authHeaders(managementKey),
      });
      return response.data;
    });
  },

  getModelPrices: async (base: string, managementKey?: string): Promise<ModelPricesResponse> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      return getDemoModelPrices();
    }

    return withUsageServiceError(async () => {
      const response = await axios.get<ModelPricesResponse>(
        buildUrl(base, '/v0/management/model-prices'),
        {
          timeout: USAGE_SERVICE_TIMEOUT_MS,
          headers: authHeaders(managementKey),
        }
      );
      return response.data;
    });
  },

  getModelPriceUsageSummary: async (
    base: string,
    managementKey?: string,
    signal?: AbortSignal
  ): Promise<ModelPriceUsageSummaryResponse> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      return getDemoModelPriceUsageSummary();
    }

    return withUsageServiceError(async () => {
      const response = await axios.get<ModelPriceUsageSummaryResponse>(
        buildUrl(base, '/v0/management/model-prices/usage-summary'),
        {
          timeout: USAGE_SERVICE_TIMEOUT_MS,
          headers: authHeaders(managementKey),
          signal,
        }
      );
      return response.data;
    });
  },

  saveModelPrices: async (
    base: string,
    prices: Record<string, ModelPrice>,
    managementKey?: string
  ): Promise<ModelPricesResponse> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      return { prices };
    }

    return withUsageServiceError(async () => {
      const response = await axios.put<ModelPricesResponse>(
        buildUrl(base, '/v0/management/model-prices'),
        { prices },
        {
          timeout: USAGE_SERVICE_TIMEOUT_MS,
          headers: authHeaders(managementKey),
        }
      );
      return response.data;
    });
  },

  getApiKeyAliases: async (
    base: string,
    managementKey?: string
  ): Promise<ApiKeyAliasesResponse> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      return getDemoApiKeyAliases();
    }

    return withUsageServiceError(async () => {
      const response = await axios.get<ApiKeyAliasesResponse>(
        buildUrl(base, '/v0/management/api-key-aliases'),
        {
          timeout: USAGE_SERVICE_TIMEOUT_MS,
          headers: authHeaders(managementKey),
        }
      );
      return response.data;
    });
  },

  saveApiKeyAliases: async (
    base: string,
    items: ApiKeyAlias[],
    managementKey?: string,
    activeApiKeyHashes?: string[],
    allowOrphanAliasCleanup?: boolean
  ): Promise<ApiKeyAliasesResponse> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      return { items };
    }

    return withUsageServiceError(async () => {
      const body: {
        items: ApiKeyAlias[];
        activeApiKeyHashes?: string[];
        allowOrphanAliasCleanup?: boolean;
      } = { items };
      if (activeApiKeyHashes && activeApiKeyHashes.length > 0) {
        body.activeApiKeyHashes = activeApiKeyHashes;
      }
      if (allowOrphanAliasCleanup) {
        body.allowOrphanAliasCleanup = true;
      }
      const response = await axios.put<ApiKeyAliasesResponse>(
        buildUrl(base, '/v0/management/api-key-aliases'),
        body,
        {
          timeout: USAGE_SERVICE_TIMEOUT_MS,
          headers: authHeaders(managementKey),
        }
      );
      return response.data;
    });
  },

  deleteApiKeyAlias: async (
    base: string,
    apiKeyHash: string,
    managementKey?: string
  ): Promise<void> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      return;
    }

    await withUsageServiceError(async () => {
      await axios.delete(
        buildUrl(base, `/v0/management/api-key-aliases/${encodeURIComponent(apiKeyHash)}`),
        {
          timeout: USAGE_SERVICE_TIMEOUT_MS,
          headers: authHeaders(managementKey),
        }
      );
    });
  },

  listAccountActionCandidates: async (
    base: string,
    managementKey?: string,
    status = 'pending',
    limit = 100
  ): Promise<AccountActionCandidatesResponse> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      return getDemoAccountActionCandidatesResponse(status, limit);
    }

    return withUsageServiceError(async () => {
      const response = await axios.get<AccountActionCandidatesResponse>(
        buildUrl(base, '/v0/management/account-action-candidates'),
        {
          timeout: USAGE_SERVICE_TIMEOUT_MS,
          headers: authHeaders(managementKey),
          params: { status, limit },
        }
      );
      return response.data;
    });
  },

  ignoreAccountActionCandidate: async (
    base: string,
    managementKey: string | undefined,
    id: number
  ): Promise<AccountActionCandidateResponse> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      return getDemoAccountActionCandidateResponse(id, 'ignored');
    }

    return withUsageServiceError(async () => {
      const response = await axios.post<AccountActionCandidateResponse>(
        buildUrl(
          base,
          `/v0/management/account-action-candidates/${encodeURIComponent(String(id))}/ignore`
        ),
        undefined,
        {
          timeout: USAGE_SERVICE_TIMEOUT_MS,
          headers: authHeaders(managementKey),
        }
      );
      return response.data;
    });
  },

  resolveAccountActionCandidate: async (
    base: string,
    managementKey: string | undefined,
    id: number
  ): Promise<AccountActionCandidateResponse> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      return getDemoAccountActionCandidateResponse(id, 'resolved');
    }

    return withUsageServiceError(async () => {
      const response = await axios.post<AccountActionCandidateResponse>(
        buildUrl(
          base,
          `/v0/management/account-action-candidates/${encodeURIComponent(String(id))}/resolve`
        ),
        undefined,
        {
          timeout: USAGE_SERVICE_TIMEOUT_MS,
          headers: authHeaders(managementKey),
        }
      );
      return response.data;
    });
  },

  enableAccountActionCandidate: async (
    base: string,
    managementKey: string | undefined,
    id: number
  ): Promise<AccountActionCandidateResponse> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      return getDemoAccountActionCandidateResponse(id, 'resolved');
    }

    return withUsageServiceError(async () => {
      const response = await axios.post<AccountActionCandidateResponse>(
        buildUrl(
          base,
          `/v0/management/account-action-candidates/${encodeURIComponent(String(id))}/enable`
        ),
        undefined,
        {
          timeout: USAGE_SERVICE_TIMEOUT_MS,
          headers: authHeaders(managementKey),
        }
      );
      return response.data;
    });
  },

  deleteAccountActionCandidateAuthFile: async (
    base: string,
    managementKey: string | undefined,
    id: number
  ): Promise<AccountActionCandidateResponse> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      return getDemoAccountActionCandidateResponse(id, 'deleted');
    }

    return withUsageServiceError(async () => {
      const response = await axios.delete<AccountActionCandidateResponse>(
        buildUrl(
          base,
          `/v0/management/account-action-candidates/${encodeURIComponent(String(id))}/auth-file`
        ),
        {
          timeout: USAGE_SERVICE_TIMEOUT_MS,
          headers: authHeaders(managementKey),
        }
      );
      return response.data;
    });
  },

  syncModelPrices: async (
    base: string,
    managementKey?: string,
    models?: string[]
  ): Promise<ModelPriceSyncResponse> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      return getDemoModelPriceSyncResponse(models);
    }

    return withUsageServiceError(async () => {
      const response = await axios.post<ModelPriceSyncResponse>(
        buildUrl(base, '/v0/management/model-prices/sync'),
        models ? { models } : {},
        {
          timeout: 45 * 1000,
          headers: authHeaders(managementKey),
        }
      );
      return response.data;
    });
  },

  exportUsage: async (base: string, managementKey?: string): Promise<UsageExportResponse> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      return {
        blob: new Blob(['{"demo":true,"event":"usage-export"}\n'], {
          type: 'application/jsonl',
        }),
        filename: 'demo-usage-events.jsonl',
      };
    }

    return withUsageServiceError(async () => {
      const response = await axios.get<Blob>(buildUrl(base, '/v0/management/usage/export'), {
        timeout: USAGE_SERVICE_TRANSFER_TIMEOUT_MS,
        headers: authHeaders(managementKey),
        responseType: 'blob',
      });
      const contentDisposition = readHeader(response.headers, 'content-disposition');
      return {
        blob: response.data,
        filename: parseContentDispositionFilename(contentDisposition) || 'usage-events.jsonl',
      };
    });
  },

  importUsage: async (
    base: string,
    payload: Blob | string,
    managementKey?: string
  ): Promise<UsageImportResponse> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      return { format: 'jsonl', added: 12, skipped: 0, total: 12, failed: 0 };
    }

    return withUsageServiceError(async () => {
      const response = await axios.post<UsageImportResponse>(
        buildUrl(base, '/v0/management/usage/import'),
        payload,
        {
          timeout: USAGE_SERVICE_TRANSFER_TIMEOUT_MS,
          headers: authHeaders(managementKey),
        }
      );
      return response.data;
    });
  },

  createUsageImportSession: async (
    base: string,
    filename: string,
    sizeBytes: number,
    managementKey?: string,
    resumeKey?: string,
    signal?: AbortSignal
  ): Promise<UsageImportSession> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      return createDemoUsageImportSession(filename, sizeBytes);
    }

    return withUsageServiceError(async () => {
      const response = await axios.post<UsageImportSession>(
        buildUrl(base, '/v0/management/usage/import-sessions'),
        {
          filename,
          size_bytes: sizeBytes,
          ...(resumeKey ? { resume_key: resumeKey } : {}),
        },
        {
          timeout: USAGE_SERVICE_TIMEOUT_MS,
          headers: authHeaders(managementKey),
          signal,
        }
      );
      return response.data;
    });
  },

  getUsageImportSession: async (
    base: string,
    sessionId: string,
    managementKey?: string,
    signal?: AbortSignal
  ): Promise<UsageImportSession> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      return getDemoUsageImportSession(sessionId);
    }

    return withUsageServiceError(async () => {
      const response = await axios.get<UsageImportSession>(
        buildUrl(base, `/v0/management/usage/import-sessions/${encodeURIComponent(sessionId)}`),
        {
          timeout: USAGE_SERVICE_TIMEOUT_MS,
          headers: authHeaders(managementKey),
          signal,
        }
      );
      return response.data;
    });
  },

  uploadUsageImportSessionChunk: async (
    base: string,
    sessionId: string,
    offset: number,
    chunk: Blob,
    managementKey?: string,
    signal?: AbortSignal
  ): Promise<UsageImportSession> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      return uploadDemoUsageImportSessionChunk(sessionId, offset, chunk.size);
    }

    return withUsageServiceError(async () => {
      const response = await axios.put<UsageImportSession>(
        buildUrl(
          base,
          `/v0/management/usage/import-sessions/${encodeURIComponent(sessionId)}/chunk?offset=${offset}`
        ),
        chunk,
        {
          timeout: USAGE_IMPORT_CHUNK_TIMEOUT_MS,
          headers: {
            ...(authHeaders(managementKey) ?? {}),
            'Content-Type': 'application/octet-stream',
          },
          signal,
        }
      );
      return response.data;
    });
  },

  completeUsageImportSession: async (
    base: string,
    sessionId: string,
    managementKey?: string,
    signal?: AbortSignal
  ): Promise<UsageImportSession> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      return completeDemoUsageImportSession(sessionId);
    }

    return withUsageServiceError(async () => {
      const response = await axios.post<UsageImportSession>(
        buildUrl(
          base,
          `/v0/management/usage/import-sessions/${encodeURIComponent(sessionId)}/complete`
        ),
        undefined,
        {
          timeout: USAGE_SERVICE_TIMEOUT_MS,
          headers: authHeaders(managementKey),
          signal,
        }
      );
      return response.data;
    });
  },

  cancelUsageImportSession: async (
    base: string,
    sessionId: string,
    managementKey?: string
  ): Promise<UsageImportSession> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      return cancelDemoUsageImportSession(sessionId);
    }

    return withUsageServiceError(async () => {
      const response = await axios.delete<UsageImportSession>(
        buildUrl(base, `/v0/management/usage/import-sessions/${encodeURIComponent(sessionId)}`),
        {
          timeout: USAGE_SERVICE_TIMEOUT_MS,
          headers: authHeaders(managementKey),
        }
      );
      return response.data;
    });
  },
};

export const dashboardApi = {
  getSummary: async (
    base: string,
    managementKey: string | undefined,
    params: DashboardSummaryParams
  ): Promise<DashboardSummaryResponse> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      return getDemoDashboardSummary();
    }

    return withUsageServiceError(async () => {
      const query: Record<string, number> = {
        today_start_ms: params.todayStartMs,
      };
      if (params.nowMs !== undefined) query.now_ms = params.nowMs;
      if (params.topModels !== undefined) query.top_models = params.topModels;
      if (params.recentFailures !== undefined) query.recent_failures = params.recentFailures;

      const response = await axios.get<DashboardSummaryResponse>(
        buildUrl(base, '/v0/management/dashboard/summary'),
        {
          timeout: USAGE_SERVICE_TIMEOUT_MS,
          headers: authHeaders(managementKey),
          params: query,
        }
      );
      return response.data;
    });
  },
};

export const monitoringAnalyticsApi = {
  getHeaderSnapshots: async (
    base: string,
    managementKey: string | undefined,
    params: { days?: number; limit?: number } = {},
    signal?: AbortSignal
  ): Promise<UsageHeaderSnapshotsResponse> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      return getDemoHeaderSnapshots();
    }

    return withUsageServiceError(async () => {
      const response = await axios.get<UsageHeaderSnapshotsResponse>(
        buildUrl(base, '/v0/management/monitoring/header-snapshots'),
        {
          timeout: USAGE_SERVICE_TIMEOUT_MS,
          headers: authHeaders(managementKey),
          params,
          signal,
        }
      );
      return response.data;
    });
  },
  getAccountHistory: async (
    base: string,
    managementKey: string | undefined,
    request: MonitoringAccountHistoryRequest,
    signal?: AbortSignal
  ): Promise<MonitoringAccountHistoryResponse> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      return getDemoAccountHistory(request);
    }

    return withUsageServiceError(async () => {
      const response = await axios.post<MonitoringAccountHistoryResponse>(
        buildUrl(base, '/v0/management/monitoring/account-history'),
        request,
        {
          timeout: USAGE_SERVICE_TIMEOUT_MS,
          headers: authHeaders(managementKey),
          signal,
        }
      );
      return response.data;
    });
  },
  getAccountWindowUsage: async (
    base: string,
    managementKey: string | undefined,
    request: MonitoringAccountWindowUsageRequest,
    signal?: AbortSignal
  ): Promise<MonitoringAccountWindowUsageResponse> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      return getDemoAccountWindowUsage(request);
    }

    return withUsageServiceError(async () => {
      const response = await axios.post<MonitoringAccountWindowUsageResponse>(
        buildUrl(base, '/v0/management/monitoring/account-window-usage'),
        request,
        {
          timeout: USAGE_SERVICE_TIMEOUT_MS,
          headers: authHeaders(managementKey),
          signal,
        }
      );
      return response.data;
    });
  },
  getAnalytics: async (
    base: string,
    managementKey: string | undefined,
    request: MonitoringAnalyticsRequest,
    signal?: AbortSignal
  ): Promise<MonitoringAnalyticsResponse> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      return getDemoMonitoringAnalytics(request);
    }

    return withUsageServiceError(async () => {
      const response = await axios.post<MonitoringAnalyticsResponse>(
        buildUrl(base, '/v0/management/monitoring/analytics'),
        request,
        {
          timeout: USAGE_SERVICE_TIMEOUT_MS,
          headers: authHeaders(managementKey),
          signal,
        }
      );
      return response.data;
    });
  },
};

export const accountQuotaSnapshotApi = {
  write: async (
    base: string,
    managementKey: string | undefined,
    entries: AccountQuotaSnapshotWriteEntry[],
    signal?: AbortSignal
  ): Promise<AccountQuotaSnapshotWriteResponse> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      return {
        observed_at_ms: Date.now(),
        items: entries.map((entry) => ({
          row_key: entry.row_key,
          account_key: entry.row_key ?? '',
          provider: entry.provider,
          inserted_count: entry.windows.length,
        })),
      };
    }
    return withUsageServiceError(async () => {
      const response = await axios.post<AccountQuotaSnapshotWriteResponse>(
        buildUrl(base, '/v0/management/quota-snapshots'),
        { entries },
        {
          timeout: USAGE_SERVICE_TIMEOUT_MS,
          headers: authHeaders(managementKey),
          signal,
        }
      );
      return response.data;
    });
  },
  query: async (
    base: string,
    managementKey: string | undefined,
    accounts: AccountQuotaSnapshotQueryAccount[],
    options: { nowMs?: number; includeInactive?: boolean } = {},
    signal?: AbortSignal
  ): Promise<AccountQuotaSnapshotQueryResponse> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      const generatedAtMs = options.nowMs ?? Date.now();
      return {
        generated_at_ms: generatedAtMs,
        items: accounts.map((account) => ({
          row_key: account.row_key,
          account_key: account.row_key,
          provider: account.provider,
          windows: buildDemoAccountQuotaSnapshotWindows(account, generatedAtMs).filter(
            (window) => options.includeInactive || window.availability !== 'inactive'
          ),
        })),
      };
    }
    return withUsageServiceError(async () => {
      const response = await axios.post<AccountQuotaSnapshotQueryResponse>(
        buildUrl(base, '/v0/management/quota-snapshots/query'),
        {
          accounts,
          now_ms: options.nowMs,
          include_inactive: options.includeInactive,
        },
        {
          timeout: USAGE_SERVICE_TIMEOUT_MS,
          headers: authHeaders(managementKey),
          signal,
        }
      );
      return response.data;
    });
  },
};
