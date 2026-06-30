import axios from 'axios';
import type { UsagePayload } from '@/features/monitoring/hooks/useUsageData';
import {
  getDemoAccountActionCandidates,
  getDemoAccountProcessingPolicy,
  getDemoApiKeyAliases,
  getDemoCodexInspectionRun,
  getDemoCodexInspectionRuns,
  getDemoDashboardSummary,
  getDemoHeaderSnapshots,
  getDemoManagerConfig,
  getDemoModelPrices,
  getDemoMonitoringAnalytics,
  getDemoQuotaCooldowns,
  getDemoUsagePayload,
  getDemoUsageServiceInfo,
  getDemoUsageServiceStatus,
} from '@/features/demo/demoFixtures';
import { isDemoMode } from '@/features/demo/demoMode';
import { normalizeApiBase } from '@/utils/connection';
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

export interface UsageServiceStatus {
  service?: string;
  dbPath?: string;
  events?: number;
  deadLetters?: number;
  collector?: UsageServiceCollectorStatus;
}

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
  provider?: string;
  owner?: string;
  recoverAtMs: number;
  disabledAtMs?: number;
  createdAtMs?: number;
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
  targetType?: string;
  workers?: number;
  deleteWorkers?: number;
  timeout?: number;
  retries?: number;
  userAgent?: string;
  usedPercentThreshold?: number;
  sampleSize?: number;
  autoActionMode?: ManagerCodexInspectionAutoActionMode | string;
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
}

export interface CodexInspectionQuotaWindow {
  id: string;
  labelKey: string;
  labelParams?: Record<string, string | number>;
  usedPercent?: number | null;
  resetLabel?: string;
  limitWindowSeconds?: number | null;
}

export interface CodexInspectionResult {
  id: number;
  runId: number;
  accountKey: string;
  fileName: string;
  displayAccount: string;
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
  error?: string;
  planType?: string | null;
  quotaWindows?: CodexInspectionQuotaWindow[];
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

export interface ModelPricesResponse {
  prices: Record<string, ModelPrice>;
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
  reason: string;
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
  api_key_stats?: boolean;
  filter_options?: boolean;
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
  providers?: string[];
  auth_files?: string[];
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

export interface ResponseHeaderMetadata {
  quota?: ResponseHeaderQuotaMetadata;
  errors?: ResponseHeaderErrorMetadata;
  trace?: ResponseHeaderTraceMetadata;
  routing?: ResponseHeaderRoutingMetadata;
  response?: ResponseHeaderResponseMetadata;
  providers?: ResponseHeaderProviderMetadata;
}

export interface UsageHeaderSnapshot {
  event_hash: string;
  timestamp_ms: number;
  auth_file_snapshot?: string;
  auth_index?: string;
  account_snapshot?: string;
  auth_label_snapshot?: string;
  auth_provider_snapshot?: string;
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
  endpoint: string;
  method: string;
  path: string;
  auth_index: string;
  source: string;
  source_hash: string;
  api_key_hash: string;
  account_snapshot: string;
  auth_label_snapshot: string;
  auth_file_snapshot?: string;
  auth_provider_snapshot: string;
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
  api_key_stats?: MonitoringAnalyticsApiKeyStatRow[];
  filter_options?: MonitoringAnalyticsFilterOptions;
  task_buckets?: MonitoringAnalyticsTaskBucketRow[];
  recent_failures?: MonitoringAnalyticsRecentFailure[];
  events?: MonitoringAnalyticsEventsResponse;
  drilldown_preview?: MonitoringAnalyticsEventsResponse;
}

const USAGE_SERVICE_TIMEOUT_MS = 30 * 1000;
const USAGE_SERVICE_TRANSFER_TIMEOUT_MS = 60 * 1000;
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

const getDemoCodexInspectionActionsResponse = (
  resultIds: number[]
): CodexInspectionActionsResponse => {
  const detail = getDemoCodexInspectionRun();
  const selected = resultIds.length
    ? detail.results.filter((result) => resultIds.includes(result.id))
    : detail.results;
  return {
    outcomes: selected.map((result) => ({
      resultId: result.id,
      accountKey: result.accountKey,
      fileName: result.fileName,
      displayAccount: result.displayAccount,
      action: result.action,
      status: 'done',
      success: true,
    })),
    detail,
  };
};

const getDemoModelPriceSyncResponse = (models?: string[]): ModelPriceSyncResponse => {
  const prices = getDemoModelPrices().prices;
  const selectedModels = new Set((models || []).map((model) => model.trim()).filter(Boolean));
  const selectedPrices =
    selectedModels.size > 0
      ? Object.fromEntries(
          Object.entries(prices).filter(([model]) => selectedModels.has(model))
        )
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
      return { ...getDemoManagerConfig(), config, source: 'db' };
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
      const response = getDemoCodexInspectionRuns();
      return { items: response.items.slice(0, limit) };
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
      return getDemoCodexInspectionRun();
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
      return getDemoCodexInspectionRun();
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

  executeCodexInspectionActions: async (
    base: string,
    managementKey: string | undefined,
    runId: number,
    resultIds: number[]
  ): Promise<CodexInspectionActionsResponse> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      return getDemoCodexInspectionActionsResponse(resultIds);
    }

    return withUsageServiceError(async () => {
      const response = await axios.post<CodexInspectionActionsResponse>(
        buildUrl(base, `/v0/management/codex-inspection/runs/${runId}/actions`),
        { resultIds },
        {
          timeout: CODEX_INSPECTION_RUN_TIMEOUT_MS,
          headers: authHeaders(managementKey),
        }
      );
      return response.data;
    });
  },

  getStatus: async (base: string, managementKey?: string): Promise<UsageServiceStatus> => {
    if (__DEMO_SITE__ && isDemoMode()) {
      return getDemoUsageServiceStatus();
    }

    return withUsageServiceError(async () => {
      const response = await axios.get<UsageServiceStatus>(buildUrl(base, '/status'), {
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
          timeout: 30 * 1000,
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
    params: { days?: number; limit?: number } = {}
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
        }
      );
      return response.data;
    });
  },
  getAnalytics: async (
    base: string,
    managementKey: string | undefined,
    request: MonitoringAnalyticsRequest
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
        }
      );
      return response.data;
    });
  },
};
