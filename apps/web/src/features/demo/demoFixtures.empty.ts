const emptyObject = {};
const emptyArray: unknown[] = [];

export const getDemoRawConfig = () => emptyObject;
export const getDemoProviderModels = () => emptyArray;
export const getDemoAuthFiles = () => ({ files: [] });
export const requestDemoCredentialRefresh = (_selector: string) => false;
export const advanceDemoCredentialRefresh = () => undefined;
export const resetDemoCredentialRefresh = () => undefined;
export const resetDemoEvidenceEpoch = () => undefined;
export const getDemoEvidenceEpochMs = () => 0;
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
export const getDemoCodexClientModelsState = () => emptyObject;
export const setDemoCodexClientModelsOverride = (_document: Record<string, unknown>) => emptyObject;
export const clearDemoCodexClientModelsOverride = () => emptyObject;
export const setDemoCodexClientModelOverrideEntry = (_slug: string, _patch: unknown) => emptyObject;
export const deleteDemoCodexClientModelOverrideEntry = (_slug: string) => emptyObject;
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
