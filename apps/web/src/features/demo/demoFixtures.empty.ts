const emptyObject = {};
const emptyArray: unknown[] = [];

export const getDemoRawConfig = () => emptyObject;
export const getDemoProviderModels = () => emptyArray;
export const getDemoAuthFiles = () => ({ files: [] });
export const requestDemoCredentialRefresh = (_selector: string) => false;
export const advanceDemoCredentialRefresh = () => undefined;
export const resetDemoCredentialRefresh = () => undefined;
export const getDemoPlugins = () => ({ plugins: [] });
export const getDemoPluginStore = () => ({ sources: [], plugins: [] });
export const getDemoManagerConfig = () => emptyObject;
export const getDemoDashboardSummary = () => emptyObject;
export const getDemoMonitoringAnalytics = () => emptyObject;
export const getDemoAccountHistory = () => ({
  generated_at_ms: Date.now(),
  checkpoint: {
    last_event_id: 0,
    latest_id: 0,
    pending: false,
    processed: 0,
  },
  items: [],
});
export const getDemoAccountWindowUsage = () => ({
  generated_at_ms: Date.now(),
  items: [],
});
export const getDemoModelPrices = () => ({ prices: {} });
export const getDemoModelPriceUsageSummary = () => ({
  sampled_events: 0,
  total_events: 0,
  truncated: false,
  models: [],
});
export const getDemoUsagePayload = () => emptyObject;
export const getDemoUsageServiceInfo = () => emptyObject;
export const getDemoUsageServiceStatus = () => emptyObject;
export const getDemoAccountProcessingPolicy = () => emptyObject;
export const getDemoQuotaStoreState = () => emptyObject;
export const getDemoQuotaCooldowns = () => emptyArray;
export const previewDemoUsageArchive = (cutoffTimestampMs: number) => ({
  cutoff_timestamp_ms: cutoffTimestampMs,
  target_event_id: 0,
  event_count: 0,
  estimated_bytes: 0,
});
export const createDemoUsageArchive = (_cutoffTimestampMs: number) => emptyObject;
export const getDemoUsageArchive = (_runId: string) => emptyObject;
export const getDemoUsageArchives = (_limit = 20) => ({ runs: [] });
export const resumeDemoUsageArchive = (_runId: string) => emptyObject;
export const verifyDemoUsageArchive = (_runId: string) => emptyObject;
export const deleteDemoUsageArchive = (_runId: string) => emptyObject;
export const getDemoUsageMaintenance = () => emptyObject;
export const resetDemoUsageArchiveState = () => undefined;
export const getDemoHeaderSnapshots = () => emptyObject;
export const getDemoCodexInspectionRuns = () => ({ items: [] });
export const getDemoCodexInspectionRun = () => ({ results: [] });
export const getDemoCodexInspectionLocalRun = () => ({
  settings: {},
  files: [],
  results: [],
  summary: {},
  startedAt: 0,
  finishedAt: 0,
});
export const getDemoCodexInspectionLocalLogs = (_baseNow?: number, _t?: unknown) => [];
export const getDemoAccountActionCandidates = () => ({ items: [], pendingCount: 0 });
export const getDemoApiKeyAliases = () => ({ items: [] });
export const getDemoLogsResponse = () => ({
  lines: [],
  'line-count': 0,
  'latest-timestamp': Date.now(),
  latestAfter: Date.now(),
  nextCursor: '',
  cursorReset: false,
});
export const getDemoErrorLogsResponse = () => ({ files: [] });
export const getDemoLatestVersion = () => ({
  latest: '',
  current: '',
  buildDate: '',
  updateAvailable: false,
});
export const getDemoManagerLatestRelease = () => ({
  tag_name: '',
  name: '',
  html_url: '',
  published_at: new Date(0).toISOString(),
});
export const getDemoConfigYaml = () => '';
export const getDemoApiCallResult = () => emptyObject;
