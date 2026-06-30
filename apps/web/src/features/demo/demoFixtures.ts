import type {
  AccountActionCandidate,
  AccountProcessingPolicy,
  ApiKeyAlias,
  CodexInspectionRunDetail,
  CodexInspectionRunsResponse,
  DashboardSummaryResponse,
  ManagerConfigResponse,
  ModelPricesResponse,
  MonitoringAnalyticsRequest,
  MonitoringAnalyticsResponse,
  QuotaCooldownInfo,
  UsageHeaderSnapshotsResponse,
  UsageServiceInfo,
  UsageServiceStatus,
} from '@/services/api/usageService';
import type { AuthFilesResponse } from '@/types/authFile';
import type { PluginListResponse, PluginStoreResponse } from '@/types/plugin';
import type { ModelInfo } from '@/utils/models';
import {
  DEMO_API_BASE,
  DEMO_SERVER_VERSION,
  formatDemoDate,
  getDemoServerBuildDate,
} from './demoMode';

type DemoApiCallPayload = {
  method?: string;
  url?: string;
  authIndex?: string;
};

const clone = <T>(value: T): T => {
  if (typeof structuredClone === 'function') {
    return structuredClone(value);
  }
  return JSON.parse(JSON.stringify(value)) as T;
};

const now = () => Date.now();
const minute = 60 * 1000;
const hour = 60 * minute;
const day = 24 * hour;

const startOfLocalDayIso = (input = now()) => {
  const date = new Date(input);
  return new Date(date.getFullYear(), date.getMonth(), date.getDate()).toISOString();
};

type DemoMonitoringEventRow = NonNullable<MonitoringAnalyticsResponse['events']>['items'][number];
type DemoMonitoringEventsResponse = NonNullable<MonitoringAnalyticsResponse['events']>;
type DemoNestedModelRow = NonNullable<
  NonNullable<NonNullable<MonitoringAnalyticsResponse['account_stats']>[number]['models']>
>[number];

const safeRate = (part: number, total: number) => (total > 0 ? part / total : 0);
const round2 = (value: number) => Number(value.toFixed(2));

const splitTokens = (totalTokens: number) => {
  const inputTokens = Math.round(totalTokens * 0.56);
  const outputTokens = Math.round(totalTokens * 0.24);
  const cachedTokens = Math.round(totalTokens * 0.13);
  const cacheReadTokens = Math.round(cachedTokens * 0.78);
  const cacheCreationTokens = cachedTokens - cacheReadTokens;
  const reasoningTokens = Math.max(
    0,
    totalTokens - inputTokens - outputTokens - cachedTokens
  );
  return {
    input_tokens: inputTokens,
    output_tokens: outputTokens,
    cached_tokens: cachedTokens,
    cache_read_tokens: cacheReadTokens,
    cache_creation_tokens: cacheCreationTokens,
    reasoning_tokens: reasoningTokens,
    total_tokens: totalTokens,
  };
};

const buildNestedModelRow = (
  model: string,
  calls: number,
  failureCalls: number,
  totalTokens: number,
  cost: number,
  lastSeenMs: number
): DemoNestedModelRow => {
  const successCalls = Math.max(0, calls - failureCalls);
  const tokens = splitTokens(totalTokens);
  return {
    model,
    calls,
    success_calls: successCalls,
    failure_calls: failureCalls,
    success_rate: safeRate(successCalls, calls),
    input_tokens: tokens.input_tokens,
    output_tokens: tokens.output_tokens,
    cached_tokens: tokens.cached_tokens,
    cache_read_tokens: tokens.cache_read_tokens,
    cache_creation_tokens: tokens.cache_creation_tokens,
    total_tokens: tokens.total_tokens,
    cost,
    last_seen_ms: lastSeenMs,
  };
};

const buildDemoPluginPage = (title: string, body: string): string =>
  `data:text/html;charset=utf-8,${encodeURIComponent(
    [
      '<!doctype html>',
      '<html>',
      '<head>',
      '<meta charset="utf-8" />',
      '<meta name="viewport" content="width=device-width, initial-scale=1" />',
      '<style>',
      ':root{color-scheme:light dark;font-family:Inter,ui-sans-serif,system-ui,sans-serif;}',
      'body{margin:0;padding:24px;background:Canvas;color:CanvasText;}',
      'main{max-width:960px;margin:0 auto;}',
      'h1{font-size:20px;margin:0 0 12px;}',
      'p{margin:0 0 16px;color:color-mix(in srgb,CanvasText 72%,transparent);}',
      '.grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(180px,1fr));gap:12px;}',
      '.card{border:1px solid color-mix(in srgb,CanvasText 14%,transparent);border-radius:8px;padding:14px;}',
      '.metric{font-size:24px;font-weight:700;}',
      '</style>',
      '</head>',
      '<body>',
      '<main>',
      `<h1>${title}</h1>`,
      `<p>${body}</p>`,
      '<section class="grid">',
      '<div class="card"><div class="metric">1,846</div><div>Requests today</div></div>',
      '<div class="card"><div class="metric">97.7%</div><div>Success rate</div></div>',
      '<div class="card"><div class="metric">42.68</div><div>Estimated cost</div></div>',
      '</section>',
      '</main>',
      '</body>',
      '</html>',
    ].join('')
  )}`;

const demoProviderModels = [
  { name: 'gpt-4.1', alias: 'GPT-4.1' },
  { name: 'gpt-4.1-mini', alias: 'GPT-4.1 Mini' },
  { name: 'claude-sonnet-4-5', alias: 'Claude Sonnet 4.5' },
  { name: 'claude-haiku-4-5', alias: 'Claude Haiku 4.5' },
  { name: 'gemini-2.5-pro', alias: 'Gemini 2.5 Pro' },
  { name: 'gemini-2.5-flash', alias: 'Gemini 2.5 Flash' },
] satisfies ModelInfo[];

const initialRawConfig: Record<string, unknown> = {
  debug: false,
  'proxy-url': 'http://127.0.0.1:7890',
  'request-retry': 2,
  'quota-exceeded': {
    'switch-project': true,
    'switch-preview-model': true,
    'antigravity-credits': true,
  },
  clean: {
    base_url: DEMO_API_BASE,
    target_type: 'codex',
    workers: 6,
    delete_workers: 2,
    timeout: 30,
    retries: 2,
    user_agent: 'CPA-Manager-Plus Demo',
    used_percent_threshold: 92,
    sample_size: 24,
  },
  'usage-statistics-enabled': true,
  'redis-usage-queue-retention-seconds': 1800,
  'request-log': true,
  'logging-to-file': true,
  'logs-max-total-size-mb': 512,
  plugins: { enabled: true },
  'ws-auth': true,
  'force-model-prefix': false,
  routing: { strategy: 'round-robin' },
  'api-keys': ['sk-demo-primary', 'sk-demo-automation', 'sk-demo-fallback'],
  'gemini-api-key': [
    {
      'api-key': 'AIza-demo-gemini-primary',
      priority: 10,
      prefix: 'gemini',
      'base-url': 'https://generativelanguage.googleapis.com',
      models: [
        { name: 'gemini-2.5-pro', alias: 'Production Pro', priority: 100 },
        { name: 'gemini-2.5-flash', alias: 'Fast Lane', priority: 80 },
      ],
    },
  ],
  'codex-api-key': [
    {
      'api-key': 'codex-demo-team-pool',
      'auth-index': 'codex-team-01',
      priority: 20,
      prefix: 'codex',
      'base-url': 'https://chatgpt.com',
      models: [{ name: 'gpt-5-codex', alias: 'Codex Team' }],
    },
  ],
  'claude-api-key': [
    {
      'api-key': 'claude-demo-team-key',
      'auth-index': 'claude-team-01',
      priority: 30,
      prefix: 'claude',
      'base-url': 'https://api.anthropic.com',
      models: [
        { name: 'claude-sonnet-4-5', alias: 'Sonnet Team' },
        { name: 'claude-haiku-4-5', alias: 'Haiku Batch' },
      ],
    },
  ],
  'vertex-api-key': [
    {
      'api-key': 'vertex-demo-service-account',
      'auth-index': 'vertex-prod-01',
      priority: 40,
      prefix: 'vertex',
      'base-url': 'https://aiplatform.googleapis.com',
      models: [{ name: 'gemini-2.5-pro', alias: 'Vertex Regional' }],
    },
  ],
  'openai-compatibility': [
    {
      name: 'OpenAI Compatible',
      prefix: 'openai',
      'base-url': 'https://api.openai.example/v1',
      'api-key-entries': [{ 'api-key': 'sk-compatible-demo-primary' }],
      models: [
        { name: 'gpt-4.1', alias: 'GPT-4.1' },
        { name: 'gpt-4.1-mini', alias: 'GPT-4.1 Mini' },
      ],
      priority: 50,
      'test-model': 'gpt-4.1-mini',
    },
    {
      name: 'Automation Shared Pool',
      prefix: 'auto',
      'base-url': 'https://gateway.example.com/v1',
      'api-key-entries': [{ 'api-key': 'sk-automation-demo' }],
      models: [{ name: 'qwen-plus', alias: 'Qwen Plus' }],
      priority: 70,
    },
  ],
  'oauth-excluded-models': {
    codex: ['o1-preview'],
    claude: ['claude-opus-legacy'],
  },
};

const demoAuthFiles: AuthFilesResponse = {
  total: 12,
  files: [
    {
      name: 'codex-team-01.json',
      type: 'codex',
      provider: 'codex',
      authIndex: 'codex-team-01',
      disabled: false,
      status: 'healthy',
      statusMessage: 'Ready',
      size: 4820,
      modified: now() - 2 * hour,
      account_snapshot: 'Platform Team',
      account_id: 'acct_codex_team',
      success: 1842,
      failed: 18,
    },
    {
      name: 'codex-fallback-02.json',
      type: 'codex',
      provider: 'codex',
      authIndex: 'codex-fallback-02',
      disabled: false,
      status: 'cooldown',
      statusMessage: 'Recovering from quota pressure',
      size: 4710,
      modified: now() - 6 * hour,
      account_snapshot: 'Automation Pool',
      account_id: 'acct_codex_auto',
      success: 934,
      failed: 42,
    },
    {
      name: 'claude-team-01.json',
      type: 'claude',
      provider: 'claude',
      authIndex: 'claude-team-01',
      disabled: false,
      status: 'healthy',
      size: 3920,
      modified: now() - day,
      account_snapshot: 'Research Team',
      success: 1520,
      failed: 9,
    },
    {
      name: 'gemini-prod-01.json',
      type: 'gemini',
      provider: 'gemini',
      authIndex: 'gemini-prod-01',
      disabled: false,
      status: 'healthy',
      project_id: 'demo-gemini-prod',
      size: 5160,
      modified: now() - 3 * hour,
      success: 2104,
      failed: 11,
    },
    {
      name: 'vertex-regional-01.json',
      type: 'vertex',
      provider: 'vertex',
      authIndex: 'vertex-regional-01',
      disabled: false,
      status: 'healthy',
      projectId: 'demo-vertex-regional',
      size: 5544,
      modified: now() - 5 * hour,
      success: 642,
      failed: 7,
    },
    {
      name: 'antigravity-builder.json',
      type: 'antigravity',
      provider: 'antigravity',
      authIndex: 'antigravity-builder-01',
      disabled: false,
      status: 'healthy',
      project_id: 'demo-antigravity-project',
      size: 4980,
      modified: now() - 9 * hour,
      success: 721,
      failed: 5,
    },
    {
      name: 'kimi-coding.json',
      type: 'kimi',
      provider: 'kimi',
      authIndex: 'kimi-coding-01',
      disabled: true,
      status: 'disabled',
      statusMessage: 'Queued for review',
      size: 2360,
      modified: now() - 2 * day,
      success: 186,
      failed: 36,
    },
    {
      name: 'xai-ops.json',
      type: 'xai',
      provider: 'xai',
      authIndex: 'xai-ops-01',
      disabled: false,
      status: 'healthy',
      size: 3180,
      modified: now() - day,
      success: 294,
      failed: 4,
    },
    {
      name: 'openai-support-02.json',
      type: 'openai',
      provider: 'openai',
      authIndex: 'openai-support-02',
      disabled: false,
      status: 'healthy',
      size: 3440,
      modified: now() - 4 * hour,
      account_snapshot: 'Support Desk',
      success: 1086,
      failed: 12,
    },
    {
      name: 'claude-research-02.json',
      type: 'claude',
      provider: 'claude',
      authIndex: 'claude-research-02',
      disabled: false,
      status: 'healthy',
      size: 4048,
      modified: now() - 7 * hour,
      account_snapshot: 'Batch Research',
      success: 934,
      failed: 18,
    },
    {
      name: 'gemini-batch-02.json',
      type: 'gemini',
      provider: 'gemini',
      authIndex: 'gemini-batch-02',
      disabled: false,
      status: 'healthy',
      project_id: 'demo-gemini-batch',
      size: 5216,
      modified: now() - 11 * hour,
      account_snapshot: 'Gemini Batch',
      success: 844,
      failed: 9,
    },
    {
      name: 'deepseek-ops-01.json',
      type: 'openai',
      provider: 'deepseek',
      authIndex: 'deepseek-ops-01',
      disabled: false,
      status: 'cooldown',
      statusMessage: 'Short retry backoff',
      size: 2860,
      modified: now() - 14 * hour,
      account_snapshot: 'Edge Experiments',
      success: 312,
      failed: 16,
    },
  ],
};

const demoPlugins: PluginListResponse = {
  pluginsEnabled: true,
  pluginsDir: 'plugins',
  plugins: [
    {
      id: 'request-insights',
      path: 'plugins/request-insights',
      configured: true,
      registered: true,
      enabled: true,
      effectiveEnabled: true,
      supportsOAuth: false,
      logo: '',
      configFields: [
        { name: 'sampleWindow', type: 'integer', enumValues: [], description: 'Sample window' },
      ],
      menus: [
        {
          path: buildDemoPluginPage(
            'Request Insights',
            'Embedded demo plugin resource backed by frontend mock data.'
          ),
          menu: 'Request Insights',
          description: 'Request analysis panel',
        },
      ],
      metadata: {
        name: 'Request Insights',
        version: '1.2.0',
        author: 'CPA Manager Plus',
        githubRepository: 'router-for-me/request-insights',
        logo: '',
        configFields: [],
      },
    },
    {
      id: 'account-auditor',
      path: 'plugins/account-auditor',
      configured: true,
      registered: true,
      enabled: true,
      effectiveEnabled: true,
      supportsOAuth: true,
      oauthProvider: 'codex',
      logo: '',
      configFields: [],
      menus: [
        {
          path: buildDemoPluginPage(
            'Account Auditor',
            'Credential health overview rendered without backend access.'
          ),
          menu: 'Account Auditor',
          description: 'Credential health overview',
        },
      ],
      metadata: {
        name: 'Account Auditor',
        version: '0.8.4',
        author: 'CPA Manager Plus',
        githubRepository: 'router-for-me/account-auditor',
        logo: '',
        configFields: [],
      },
    },
  ],
};

const demoPluginStore: PluginStoreResponse = {
  pluginsEnabled: true,
  pluginsDir: 'plugins',
  sources: [{ id: 'official', name: 'official', url: 'https://plugins.example.com/index.json' }],
  sourceErrors: [],
  plugins: [
    {
      storeId: 'official/request-insights',
      sourceId: 'official',
      sourceName: 'official',
      sourceUrl: 'https://plugins.example.com/index.json',
      id: 'request-insights',
      name: 'Request Insights',
      description: 'Adds a focused request-analysis workspace.',
      author: 'CPA Manager Plus',
      version: '1.2.0',
      repository: 'router-for-me/request-insights',
      logo: '',
      homepage: '',
      license: 'MIT',
      tags: ['monitoring', 'usage'],
      installed: true,
      installedVersion: '1.2.0',
      path: 'plugins/request-insights',
      configured: true,
      registered: true,
      enabled: true,
      effectiveEnabled: true,
      updateAvailable: false,
    },
    {
      storeId: 'official/routing-lab',
      sourceId: 'official',
      sourceName: 'official',
      sourceUrl: 'https://plugins.example.com/index.json',
      id: 'routing-lab',
      name: 'Routing Lab',
      description: 'Experiments with routing policy previews.',
      author: 'CPA Manager Plus',
      version: '0.5.1',
      repository: 'router-for-me/routing-lab',
      logo: '',
      homepage: '',
      license: 'MIT',
      tags: ['routing'],
      installed: false,
      installedVersion: '',
      path: '',
      configured: false,
      registered: false,
      enabled: false,
      effectiveEnabled: false,
      updateAvailable: false,
    },
  ],
};

const demoManagerConfig: ManagerConfigResponse = {
  source: 'db',
  cpaUsage: {
    usageStatisticsEnabled: true,
    redisUsageQueueRetentionSeconds: 1800,
  },
  config: {
    cpaConnection: {
      cpaBaseUrl: DEMO_API_BASE,
      managementKey: 'demo-cpa-management-key',
    },
    collector: {
      enabled: true,
      collectorMode: 'http',
      queue: 'usage-events',
      popSide: 'right',
      batchSize: 100,
      pollIntervalMs: 2000,
      queryLimit: 1000,
      tlsSkipVerify: false,
    },
    codexInspection: {
      enabled: true,
      schedule: {
        mode: 'interval',
        intervalMinutes: 45,
        timeZone: 'Asia/Shanghai',
      },
      targetType: 'codex',
      workers: 6,
      deleteWorkers: 2,
      timeout: 30,
      retries: 2,
      userAgent: 'CPA-Manager-Plus Demo',
      usedPercentThreshold: 92,
      sampleSize: 24,
      autoActionMode: 'disable',
    },
    externalUsageService: {
      enabled: true,
      serviceBase: DEMO_API_BASE,
    },
    updatedAtMs: now() - hour,
  },
};

const demoModelPrices: ModelPricesResponse = {
  prices: {
    'gpt-4.1': { prompt: 2, completion: 8, cache: 0.5, source: 'demo' },
    'gpt-4.1-mini': { prompt: 0.4, completion: 1.6, cache: 0.1, source: 'demo' },
    'claude-sonnet-4-5': { prompt: 3, completion: 15, cache: 0.3, source: 'demo' },
    'gemini-2.5-pro': { prompt: 1.25, completion: 10, cache: 0.25, source: 'demo' },
    'gemini-2.5-flash': { prompt: 0.3, completion: 2.5, cache: 0.08, source: 'demo' },
    'qwen-plus': { prompt: 0.4, completion: 1.2, cache: 0.1, source: 'demo' },
    'claude-haiku-4-5': { prompt: 0.8, completion: 4, cache: 0.08, source: 'demo' },
    'deepseek-chat': { prompt: 0.27, completion: 1.1, cache: 0.07, source: 'demo' },
    'grok-4-fast': { prompt: 0.2, completion: 0.8, cache: 0.05, source: 'demo' },
  },
};

const demoApiAliases: ApiKeyAlias[] = [
  { apiKeyHash: 'hash_openai_primary', alias: 'OpenAI Primary', updatedAtMs: now() - day },
  { apiKeyHash: 'hash_codex_team', alias: 'Codex Team', updatedAtMs: now() - 2 * hour },
  { apiKeyHash: 'hash_gemini_prod', alias: 'Gemini Production', updatedAtMs: now() - 3 * hour },
  { apiKeyHash: 'hash_automation_pool', alias: 'Automation Pool', updatedAtMs: now() - 4 * hour },
  { apiKeyHash: 'hash_research_shared', alias: 'Research Shared', updatedAtMs: now() - 5 * hour },
  { apiKeyHash: 'hash_support_console', alias: 'Support Console', updatedAtMs: now() - 6 * hour },
  { apiKeyHash: 'hash_research_batch', alias: 'Research Batch', updatedAtMs: now() - 7 * hour },
  { apiKeyHash: 'hash_gemini_batch', alias: 'Gemini Batch', updatedAtMs: now() - 8 * hour },
  { apiKeyHash: 'hash_kimi_coding', alias: 'Kimi Coding', updatedAtMs: now() - 9 * hour },
  { apiKeyHash: 'hash_builder_lab', alias: 'Builder Lab', updatedAtMs: now() - 10 * hour },
  { apiKeyHash: 'hash_xai_ops', alias: 'xAI Ops', updatedAtMs: now() - 11 * hour },
  { apiKeyHash: 'hash_deepseek_ops', alias: 'DeepSeek Ops', updatedAtMs: now() - 12 * hour },
];

const dashboardBase = (inputNow = now()): DashboardSummaryResponse => {
  const todayStart = new Date(inputNow);
  todayStart.setHours(0, 0, 0, 0);
  const todayStartMs = todayStart.getTime();
  const baseNow = Math.min(
    todayStartMs + 23 * hour + 50 * minute,
    Math.max(inputNow, todayStartMs + 18 * hour + 20 * minute)
  );
  const healthBucketMs = 10 * minute;
  const bucketsPerHour = hour / healthBucketMs;
  const healthPoints = Array.from({ length: 24 * bucketsPerHour }, (_, index) => {
    const bucket = todayStartMs + index * healthBucketMs;
    const hourIndex = Math.floor(index / bucketsPerHour);
    const minuteIndex = index % bucketsPerHour;
    const future = bucket > baseNow;
    const quietHour = hourIndex < 6 || hourIndex >= 22;
    const empty =
      !future &&
      (quietHour
        ? minuteIndex % 2 === 1 || (hourIndex < 3 && minuteIndex !== 0)
        : index % 41 === 0);
    const baseCalls = 7 + ((hourIndex * 5 + minuteIndex * 3) % 18);
    const peakCalls =
      hourIndex >= 9 && hourIndex <= 11
        ? 14
        : hourIndex >= 14 && hourIndex <= 17
          ? 18
          : hourIndex === 20
            ? 9
            : 0;
    const calls = future || empty ? 0 : baseCalls + peakCalls;
    const highFailure =
      (hourIndex === 10 && minuteIndex === 4) || (hourIndex === 16 && minuteIndex === 2);
    const warnFailure = index % 19 === 0 || (hourIndex === 13 && minuteIndex === 5);
    const failure = calls
      ? highFailure
        ? Math.max(2, Math.ceil(calls * 0.18))
        : warnFailure
          ? Math.max(1, Math.ceil(calls * 0.08))
          : index % 11 === 0
            ? 1
            : 0
      : 0;
    const success = Math.max(0, calls - failure);
    const tokens = calls * (640 + (hourIndex % 5) * 70 + minuteIndex * 24);
    const failureRate = safeRate(failure, calls);
    return {
      bucket_ms: bucket,
      calls,
      tokens,
      success,
      failure,
      success_rate: safeRate(success, calls),
      failure_rate: failureRate,
      tone: future
        ? 'future'
        : calls === 0
          ? 'empty'
          : failureRate >= 0.12
            ? 'bad'
            : failureRate >= 0.05
              ? 'warn'
              : 'good',
      intensity: future ? 0.18 : calls === 0 ? 0.12 : Math.min(1, 0.22 + calls / 48),
      future,
    };
  });
  const totalCalls = healthPoints.reduce((sum, point) => sum + point.calls, 0);
  const failureCalls = healthPoints.reduce((sum, point) => sum + point.failure, 0);
  const successCalls = totalCalls - failureCalls;
  const totalTokens = healthPoints.reduce((sum, point) => sum + point.tokens, 0);
  const todayTokens = splitTokens(totalTokens);
  const totalCost = round2((totalTokens / 1_000_000) * 22.9);
  const timeline = Array.from({ length: 24 }, (_, hourIndex) => {
    const hourPoints = healthPoints.slice(hourIndex * bucketsPerHour, (hourIndex + 1) * bucketsPerHour);
    const calls = hourPoints.reduce((sum, point) => sum + point.calls, 0);
    const tokens = hourPoints.reduce((sum, point) => sum + point.tokens, 0);
    const success = hourPoints.reduce((sum, point) => sum + point.success, 0);
    const failure = hourPoints.reduce((sum, point) => sum + point.failure, 0);
    return {
      bucket_ms: todayStartMs + hourIndex * hour,
      calls,
      tokens,
      success,
      failure,
      calls_share: safeRate(calls, totalCalls),
      tokens_share: safeRate(tokens, totalTokens),
      failure_rate: safeRate(failure, calls),
    };
  });
  const rollingPoints = healthPoints.filter(
    (point) => point.bucket_ms > baseNow - 30 * minute && point.bucket_ms <= baseNow
  );
  const rollingCalls = rollingPoints.reduce((sum, point) => sum + point.calls, 0);
  const rollingTokens = rollingPoints.reduce((sum, point) => sum + point.tokens, 0);
  const modelMix = [
    { model: 'gpt-4.1-mini', callShare: 0.28, tokenShare: 0.21, costShare: 0.11, successRate: 0.991 },
    { model: 'claude-sonnet-4-5', callShare: 0.22, tokenShare: 0.27, costShare: 0.3, successRate: 0.982 },
    { model: 'gemini-2.5-pro', callShare: 0.2, tokenShare: 0.23, costShare: 0.25, successRate: 0.986 },
    { model: 'gpt-4.1', callShare: 0.17, tokenShare: 0.19, costShare: 0.24, successRate: 0.976 },
    { model: 'gemini-2.5-flash', callShare: 0.13, tokenShare: 0.1, costShare: 0.1, successRate: 0.994 },
  ].map((item) => ({
    model: item.model,
    calls: Math.round(totalCalls * item.callShare),
    tokens: Math.round(totalTokens * item.tokenShare),
    cost: round2(totalCost * item.costShare),
    success_rate: item.successRate,
    cost_share: item.costShare,
  }));
  return {
    generated_at_ms: baseNow,
    window: {
      today_start_ms: todayStartMs,
      now_ms: baseNow,
      rolling_30m_start_ms: baseNow - 30 * 60 * 1000,
    },
    today: {
      total_calls: totalCalls,
      success_calls: successCalls,
      failure_calls: failureCalls,
      success_rate: safeRate(successCalls, totalCalls),
      input_tokens: todayTokens.input_tokens,
      output_tokens: todayTokens.output_tokens,
      cached_tokens: todayTokens.cached_tokens,
      cache_read_tokens: todayTokens.cache_read_tokens,
      cache_creation_tokens: todayTokens.cache_creation_tokens,
      reasoning_tokens: todayTokens.reasoning_tokens,
      total_tokens: todayTokens.total_tokens,
      total_cost: totalCost,
      average_latency_ms: 1280,
      zero_token_calls: 7,
    },
    rolling_30m: {
      rpm: round2(rollingCalls / 30),
      tpm: Math.round(rollingTokens / 30),
      total_calls: rollingCalls,
      total_tokens: rollingTokens,
    },
    top_models_today: modelMix.slice(0, 4),
    model_cost_rank: [...modelMix].sort((left, right) => right.cost - left.cost),
    traffic_timeline: timeline,
    hourly_activity: timeline.map((point, index) => ({
      hour_index: index,
      bucket_ms: point.bucket_ms,
      calls: point.calls,
      tokens: point.tokens,
      intensity: Math.min(1, point.calls / 110),
    })),
    today_request_health_timeline: {
      from_ms: todayStartMs,
      to_ms: todayStartMs + 24 * hour,
      bucket_ms: healthBucketMs,
      success_calls: successCalls,
      failure_calls: failureCalls,
      total_calls: totalCalls,
      success_rate: safeRate(successCalls, totalCalls),
      points: healthPoints,
    },
    token_mix: [
      { key: 'input', tokens: todayTokens.input_tokens, share: safeRate(todayTokens.input_tokens, totalTokens) },
      { key: 'output', tokens: todayTokens.output_tokens, share: safeRate(todayTokens.output_tokens, totalTokens) },
      { key: 'cached', tokens: todayTokens.cached_tokens, share: safeRate(todayTokens.cached_tokens, totalTokens) },
      { key: 'reasoning', tokens: todayTokens.reasoning_tokens, share: safeRate(todayTokens.reasoning_tokens, totalTokens) },
    ],
    channel_health: [
      {
        auth_index: 'codex-team-01',
        auth_label: 'Codex Team',
        account: 'Platform Team',
        channel: 'Codex',
        source: 'team',
        calls: Math.round(totalCalls * 0.32),
        failures: Math.round(failureCalls * 0.2),
        failure_rate: 0.012,
        success_rate: 0.988,
        tokens: Math.round(totalTokens * 0.28),
        cost: round2(totalCost * 0.24),
        average_latency_ms: 1220,
        tone: 'good',
      },
      {
        auth_index: 'claude-team-01',
        auth_label: 'Claude Team',
        account: 'Research Team',
        channel: 'Claude',
        source: 'research',
        calls: Math.round(totalCalls * 0.22),
        failures: Math.round(failureCalls * 0.2),
        failure_rate: 0.021,
        success_rate: 0.979,
        tokens: Math.round(totalTokens * 0.27),
        cost: round2(totalCost * 0.3),
        average_latency_ms: 1380,
        tone: 'good',
      },
      {
        auth_index: 'codex-fallback-02',
        auth_label: 'Fallback Pool',
        account: 'Automation Pool',
        channel: 'Codex',
        source: 'automation',
        calls: Math.round(totalCalls * 0.14),
        failures: Math.max(3, Math.round(failureCalls * 0.45)),
        failure_rate: 0.092,
        success_rate: 0.908,
        tokens: Math.round(totalTokens * 0.12),
        cost: round2(totalCost * 0.12),
        average_latency_ms: 2140,
        tone: 'warn',
      },
    ],
    failure_sources: [
      {
        source_hash: 'src_fallback_pool',
        auth_index: 'codex-fallback-02',
        auth_label: 'Fallback Pool',
        account: 'Automation Pool',
        channel: 'Codex',
        source: 'automation',
        calls: Math.round(totalCalls * 0.14),
        failures: Math.max(3, Math.round(failureCalls * 0.45)),
        failure_rate: 0.092,
        last_seen_ms: baseNow - 18 * 60 * 1000,
        average_latency_ms: 2140,
        tone: 'warn',
      },
    ],
    recent_failures: [
      {
        timestamp_ms: baseNow - 18 * 60 * 1000,
        model: 'gpt-4.1',
        api_key_hash: 'hash_codex_team',
        source_hash: 'src_fallback_pool',
        auth_index: 'codex-fallback-02',
        auth_label: 'Fallback Pool',
        account: 'Automation Pool',
        endpoint: '/v1/chat/completions',
        duration_ms: 2840,
        fail_status_code: 429,
        fail_summary: 'Quota window reached',
        header_quota_used_percent: 96,
        header_quota_plan_type: 'team',
        header_error_kind: 'quota',
        header_error_code: 'rate_limit',
        header_trace_id: 'demo-trace-429',
      },
    ],
  };
};

const paginateDemoEvents = (
  items: DemoMonitoringEventRow[],
  limit: number,
  beforeMs?: number | null
): DemoMonitoringEventsResponse => {
  const sorted = [...items].sort((left, right) => right.timestamp_ms - left.timestamp_ms);
  const filtered = beforeMs
    ? sorted.filter((item) => item.timestamp_ms < beforeMs)
    : sorted;
  const safeLimit = Math.max(1, Math.min(Math.trunc(limit || filtered.length), filtered.length || 1));
  const pageItems = filtered.slice(0, safeLimit);
  const last = pageItems[pageItems.length - 1];
  return {
    items: pageItems,
    next_before_ms: last?.timestamp_ms ?? 0,
    has_more: filtered.length > pageItems.length,
    total_count: items.length,
  };
};

const buildMonitoringAnalytics = (
  baseNow = now(),
  request?: MonitoringAnalyticsRequest
): MonitoringAnalyticsResponse => {
  const dashboard = dashboardBase(baseNow);
  const analyticsNow = dashboard.generated_at_ms;
  const timeline = Array.from({ length: 14 }, (_, index) => {
    const bucket = analyticsNow - (13 - index) * day;
    const calls = 1180 + ((index * 137) % 620);
    const failure = index % 6 === 0 ? 54 : 18 + (index % 4) * 7;
    const success = calls - failure;
    const tokens = calls * (860 + (index % 3) * 105);
    const tokenSplit = splitTokens(tokens);
    return {
      bucket_ms: bucket,
      bucket_end_ms: bucket + day,
      label: new Date(bucket).toLocaleDateString(),
      calls,
      tokens,
      success,
      failure,
      input_tokens: tokenSplit.input_tokens,
      output_tokens: tokenSplit.output_tokens,
      cached_tokens: tokenSplit.cached_tokens,
      cache_read_tokens: tokenSplit.cache_read_tokens,
      cache_creation_tokens: tokenSplit.cache_creation_tokens,
      reasoning_tokens: tokenSplit.reasoning_tokens,
      total_tokens: tokenSplit.total_tokens,
      cost: round2((tokens / 1_000_000) * 18.6),
      average_latency_ms: 1100 + (index % 5) * 90,
      p95_latency_ms: 2400 + (index % 5) * 180,
      p95_ttft_ms: 720 + (index % 4) * 65,
      success_rate: safeRate(success, calls),
      failure_rate: safeRate(failure, calls),
    };
  });

  const modelStats = [
    buildNestedModelRow('gpt-4.1-mini', 6200, 48, 4_680_000, 56.2, analyticsNow - 8 * minute),
    buildNestedModelRow('claude-sonnet-4-5', 4380, 96, 5_720_000, 158.7, analyticsNow - 13 * minute),
    buildNestedModelRow('gemini-2.5-pro', 3620, 74, 4_960_000, 124.4, analyticsNow - 21 * minute),
    buildNestedModelRow('gpt-4.1', 2940, 81, 3_840_000, 102.8, analyticsNow - 16 * minute),
    buildNestedModelRow('gemini-2.5-flash', 2140, 24, 1_780_000, 28.9, analyticsNow - 6 * minute),
    buildNestedModelRow('qwen-plus', 1160, 18, 980_000, 12.6, analyticsNow - 34 * minute),
    buildNestedModelRow('claude-haiku-4-5', 980, 12, 860_000, 18.4, analyticsNow - 52 * minute),
    buildNestedModelRow('deepseek-chat', 740, 20, 610_000, 6.8, analyticsNow - 44 * minute),
    buildNestedModelRow('grok-4-fast', 860, 14, 690_000, 12.2, analyticsNow - 55 * minute),
  ].map(({ last_seen_ms: _lastSeenMs, ...row }) => row);
  const summaryCalls = modelStats.reduce((sum, row) => sum + row.calls, 0);
  const summaryFailures = modelStats.reduce((sum, row) => sum + row.failure_calls, 0);
  const summarySuccess = summaryCalls - summaryFailures;
  const summaryInputTokens = modelStats.reduce((sum, row) => sum + row.input_tokens, 0);
  const summaryOutputTokens = modelStats.reduce((sum, row) => sum + row.output_tokens, 0);
  const summaryCachedTokens = modelStats.reduce((sum, row) => sum + row.cached_tokens, 0);
  const summaryCacheReadTokens = modelStats.reduce(
    (sum, row) => sum + row.cache_read_tokens,
    0
  );
  const summaryCacheCreationTokens = modelStats.reduce(
    (sum, row) => sum + row.cache_creation_tokens,
    0
  );
  const summaryTokens = modelStats.reduce((sum, row) => sum + row.total_tokens, 0);
  const summaryCost = round2(modelStats.reduce((sum, row) => sum + row.cost, 0));

  const accountStats = [
    {
      id: 'acct_platform_team',
      account_snapshot: 'Platform Team',
      auth_label_snapshot: 'Codex Team',
      auth_provider_snapshot: 'codex',
      auth_indices: ['codex-team-01'],
      sources: ['team'],
      source_hashes: ['src_codex_team'],
      calls: 5200,
      failure_calls: 62,
      total_tokens: 4_220_000,
      cost: 88.1,
      average_latency_ms: 1220,
      last_seen_ms: analyticsNow - 8 * minute,
      models: [
        buildNestedModelRow('gpt-4.1-mini', 2380, 18, 1_760_000, 21.2, analyticsNow - 8 * minute),
        buildNestedModelRow('gpt-4.1', 1680, 33, 1_980_000, 52.4, analyticsNow - 16 * minute),
        buildNestedModelRow('qwen-plus', 1140, 11, 480_000, 6.1, analyticsNow - 34 * minute),
      ],
    },
    {
      id: 'acct_research_team',
      account_snapshot: 'Research Team',
      auth_label_snapshot: 'Claude Team',
      auth_provider_snapshot: 'claude',
      auth_indices: ['claude-team-01'],
      sources: ['research'],
      source_hashes: ['src_claude_team'],
      calls: 4380,
      failure_calls: 96,
      total_tokens: 5_720_000,
      cost: 158.7,
      average_latency_ms: 1380,
      last_seen_ms: analyticsNow - 13 * minute,
      models: [
        buildNestedModelRow(
          'claude-sonnet-4-5',
          3920,
          88,
          5_120_000,
          145.6,
          analyticsNow - 13 * minute
        ),
        buildNestedModelRow(
          'claude-haiku-4-5',
          460,
          8,
          600_000,
          13.1,
          analyticsNow - 52 * minute
        ),
      ],
    },
    {
      id: 'acct_gemini_prod',
      account_snapshot: 'Gemini Production',
      auth_label_snapshot: 'Gemini Production',
      auth_provider_snapshot: 'gemini',
      auth_indices: ['gemini-prod-01', 'vertex-regional-01'],
      sources: ['gateway', 'regional'],
      source_hashes: ['src_gemini_prod', 'src_vertex_regional'],
      calls: 5760,
      failure_calls: 98,
      total_tokens: 6_360_000,
      cost: 153.3,
      average_latency_ms: 1160,
      last_seen_ms: analyticsNow - 6 * minute,
      models: [
        buildNestedModelRow(
          'gemini-2.5-pro',
          3620,
          74,
          4_960_000,
          124.4,
          analyticsNow - 21 * minute
        ),
        buildNestedModelRow(
          'gemini-2.5-flash',
          2140,
          24,
          1_400_000,
          28.9,
          analyticsNow - 6 * minute
        ),
      ],
    },
    {
      id: 'acct_openai_gateway',
      account_snapshot: 'OpenAI Compatible',
      auth_label_snapshot: 'OpenAI Primary',
      auth_provider_snapshot: 'openai',
      auth_indices: ['openai-primary'],
      sources: ['gateway'],
      source_hashes: ['src_openai_primary'],
      calls: 3540,
      failure_calls: 39,
      total_tokens: 2_700_000,
      cost: 45.8,
      average_latency_ms: 1080,
      last_seen_ms: analyticsNow - 10 * minute,
      models: [
        buildNestedModelRow(
          'gpt-4.1-mini',
          2720,
          24,
          2_040_000,
          25.0,
          analyticsNow - 10 * minute
        ),
        buildNestedModelRow('gpt-4.1', 820, 15, 660_000, 20.8, analyticsNow - 36 * minute),
      ],
    },
    {
      id: 'acct_automation_pool',
      account_snapshot: 'Automation Pool',
      auth_label_snapshot: 'Fallback Pool',
      auth_provider_snapshot: 'codex',
      auth_indices: ['codex-fallback-02'],
      sources: ['automation'],
      source_hashes: ['src_fallback_pool'],
      calls: 1560,
      failure_calls: 46,
      total_tokens: 1_260_000,
      cost: 31.7,
      average_latency_ms: 2140,
      last_seen_ms: analyticsNow - 18 * minute,
      models: [
        buildNestedModelRow('gpt-4.1', 440, 24, 520_000, 22.6, analyticsNow - 18 * minute),
        buildNestedModelRow(
          'gpt-4.1-mini',
          1120,
          22,
          740_000,
          9.1,
          analyticsNow - 28 * minute
        ),
      ],
    },
    {
      id: 'acct_support_desk',
      account_snapshot: 'Support Desk',
      auth_label_snapshot: 'OpenAI Support',
      auth_provider_snapshot: 'openai',
      auth_indices: ['openai-support-02'],
      sources: ['support'],
      source_hashes: ['src_openai_support'],
      calls: 2480,
      failure_calls: 28,
      total_tokens: 1_920_000,
      cost: 32.4,
      average_latency_ms: 980,
      last_seen_ms: analyticsNow - 11 * minute,
      models: [
        buildNestedModelRow(
          'gpt-4.1-mini',
          1800,
          18,
          1_240_000,
          15.2,
          analyticsNow - 11 * minute
        ),
        buildNestedModelRow('gpt-4.1', 680, 10, 680_000, 17.2, analyticsNow - 32 * minute),
      ],
    },
    {
      id: 'acct_research_batch',
      account_snapshot: 'Batch Research',
      auth_label_snapshot: 'Claude Batch',
      auth_provider_snapshot: 'claude',
      auth_indices: ['claude-research-02'],
      sources: ['batch'],
      source_hashes: ['src_claude_batch'],
      calls: 2100,
      failure_calls: 42,
      total_tokens: 3_080_000,
      cost: 83.5,
      average_latency_ms: 1510,
      last_seen_ms: analyticsNow - 19 * minute,
      models: [
        buildNestedModelRow(
          'claude-sonnet-4-5',
          1280,
          30,
          2_220_000,
          65.1,
          analyticsNow - 19 * minute
        ),
        buildNestedModelRow(
          'claude-haiku-4-5',
          820,
          12,
          860_000,
          18.4,
          analyticsNow - 52 * minute
        ),
      ],
    },
    {
      id: 'acct_gemini_batch',
      account_snapshot: 'Gemini Batch',
      auth_label_snapshot: 'Gemini Batch',
      auth_provider_snapshot: 'gemini',
      auth_indices: ['gemini-batch-02'],
      sources: ['batch'],
      source_hashes: ['src_gemini_batch'],
      calls: 1980,
      failure_calls: 25,
      total_tokens: 1_840_000,
      cost: 38.2,
      average_latency_ms: 1120,
      last_seen_ms: analyticsNow - 24 * minute,
      models: [
        buildNestedModelRow(
          'gemini-2.5-flash',
          1380,
          14,
          1_020_000,
          15.8,
          analyticsNow - 24 * minute
        ),
        buildNestedModelRow(
          'gemini-2.5-pro',
          600,
          11,
          820_000,
          22.4,
          analyticsNow - 43 * minute
        ),
      ],
    },
    {
      id: 'acct_kimi_coding',
      account_snapshot: 'Kimi Coding',
      auth_label_snapshot: 'Kimi Coding',
      auth_provider_snapshot: 'kimi',
      auth_indices: ['kimi-coding-01'],
      sources: ['coding'],
      source_hashes: ['src_kimi_coding'],
      calls: 1220,
      failure_calls: 36,
      total_tokens: 980_000,
      cost: 15.8,
      average_latency_ms: 1710,
      last_seen_ms: analyticsNow - 48 * minute,
      models: [buildNestedModelRow('qwen-plus', 1220, 36, 980_000, 15.8, analyticsNow - 48 * minute)],
    },
    {
      id: 'acct_builder_lab',
      account_snapshot: 'Builder Lab',
      auth_label_snapshot: 'Antigravity Builder',
      auth_provider_snapshot: 'antigravity',
      auth_indices: ['antigravity-builder-01'],
      sources: ['builder'],
      source_hashes: ['src_antigravity_builder'],
      calls: 960,
      failure_calls: 12,
      total_tokens: 820_000,
      cost: 14.4,
      average_latency_ms: 1320,
      last_seen_ms: analyticsNow - 27 * minute,
      models: [
        buildNestedModelRow(
          'gemini-2.5-flash',
          960,
          12,
          820_000,
          14.4,
          analyticsNow - 27 * minute
        ),
      ],
    },
    {
      id: 'acct_ops_console',
      account_snapshot: 'Ops Console',
      auth_label_snapshot: 'xAI Ops',
      auth_provider_snapshot: 'xai',
      auth_indices: ['xai-ops-01'],
      sources: ['ops'],
      source_hashes: ['src_xai_ops'],
      calls: 860,
      failure_calls: 14,
      total_tokens: 690_000,
      cost: 12.2,
      average_latency_ms: 1490,
      last_seen_ms: analyticsNow - 55 * minute,
      models: [buildNestedModelRow('grok-4-fast', 860, 14, 690_000, 12.2, analyticsNow - 55 * minute)],
    },
    {
      id: 'acct_edge_experiments',
      account_snapshot: 'Edge Experiments',
      auth_label_snapshot: 'DeepSeek Ops',
      auth_provider_snapshot: 'deepseek',
      auth_indices: ['deepseek-ops-01'],
      sources: ['ops'],
      source_hashes: ['src_deepseek_ops'],
      calls: 740,
      failure_calls: 20,
      total_tokens: 610_000,
      cost: 6.8,
      average_latency_ms: 1580,
      last_seen_ms: analyticsNow - 44 * minute,
      models: [buildNestedModelRow('deepseek-chat', 740, 20, 610_000, 6.8, analyticsNow - 44 * minute)],
    },
  ].map((row) => {
    const tokenSplit = splitTokens(row.total_tokens);
    const successCalls = row.calls - row.failure_calls;
    return {
      ...row,
      success_calls: successCalls,
      success_rate: safeRate(successCalls, row.calls),
      input_tokens: tokenSplit.input_tokens,
      output_tokens: tokenSplit.output_tokens,
      cached_tokens: tokenSplit.cached_tokens,
      cache_read_tokens: tokenSplit.cache_read_tokens,
      cache_creation_tokens: tokenSplit.cache_creation_tokens,
    };
  });

  const credentialStats = [
    {
      id: 'codex-team-01',
      auth_file_snapshot: 'codex-team-01.json',
      auth_index: 'codex-team-01',
      source: 'team',
      source_hash: 'src_codex_team',
      account_snapshot: 'Platform Team',
      auth_label_snapshot: 'Codex Team',
      auth_provider_snapshot: 'codex',
      calls: 5200,
      failure_calls: 62,
      total_tokens: 4_220_000,
      cost: 88.1,
      average_latency_ms: 1220,
      last_seen_ms: analyticsNow - 8 * minute,
      models: accountStats[0].models,
    },
    {
      id: 'claude-team-01',
      auth_file_snapshot: 'claude-team-01.json',
      auth_index: 'claude-team-01',
      source: 'research',
      source_hash: 'src_claude_team',
      account_snapshot: 'Research Team',
      auth_label_snapshot: 'Claude Team',
      auth_provider_snapshot: 'claude',
      calls: 4380,
      failure_calls: 96,
      total_tokens: 5_720_000,
      cost: 158.7,
      average_latency_ms: 1380,
      last_seen_ms: analyticsNow - 13 * minute,
      models: accountStats[1].models,
    },
    {
      id: 'gemini-prod-01',
      auth_file_snapshot: 'gemini-prod-01.json',
      auth_index: 'gemini-prod-01',
      source: 'gateway',
      source_hash: 'src_gemini_prod',
      account_snapshot: 'Gemini Production',
      auth_label_snapshot: 'Gemini Production',
      auth_provider_snapshot: 'gemini',
      auth_project_id_snapshot: 'demo-gemini-prod',
      calls: 3620,
      failure_calls: 74,
      total_tokens: 4_960_000,
      cost: 124.4,
      average_latency_ms: 1160,
      last_seen_ms: analyticsNow - 21 * minute,
      models: [buildNestedModelRow('gemini-2.5-pro', 3620, 74, 4_960_000, 124.4, analyticsNow - 21 * minute)],
    },
    {
      id: 'vertex-regional-01',
      auth_file_snapshot: 'vertex-regional-01.json',
      auth_index: 'vertex-regional-01',
      source: 'regional',
      source_hash: 'src_vertex_regional',
      account_snapshot: 'Gemini Production',
      auth_label_snapshot: 'Vertex Regional',
      auth_provider_snapshot: 'vertex',
      auth_project_id_snapshot: 'demo-vertex-regional',
      calls: 2140,
      failure_calls: 24,
      total_tokens: 1_400_000,
      cost: 28.9,
      average_latency_ms: 1040,
      last_seen_ms: analyticsNow - 6 * minute,
      models: [buildNestedModelRow('gemini-2.5-flash', 2140, 24, 1_400_000, 28.9, analyticsNow - 6 * minute)],
    },
    {
      id: 'codex-fallback-02',
      auth_file_snapshot: 'codex-fallback-02.json',
      auth_index: 'codex-fallback-02',
      source: 'automation',
      source_hash: 'src_fallback_pool',
      account_snapshot: 'Automation Pool',
      auth_label_snapshot: 'Fallback Pool',
      auth_provider_snapshot: 'codex',
      calls: 1560,
      failure_calls: 46,
      total_tokens: 1_260_000,
      cost: 31.7,
      average_latency_ms: 2140,
      last_seen_ms: analyticsNow - 18 * minute,
      models: accountStats[4].models,
    },
    {
      id: 'kimi-coding-01',
      auth_file_snapshot: 'kimi-coding.json',
      auth_index: 'kimi-coding-01',
      source: 'coding',
      source_hash: 'src_kimi_coding',
      account_snapshot: 'Kimi Coding',
      auth_label_snapshot: 'Kimi Coding',
      auth_provider_snapshot: 'kimi',
      calls: 1220,
      failure_calls: 36,
      total_tokens: 980_000,
      cost: 15.8,
      average_latency_ms: 1710,
      last_seen_ms: analyticsNow - 48 * minute,
      models: accountStats[8].models,
    },
    {
      id: 'antigravity-builder',
      auth_file_snapshot: 'antigravity-builder.json',
      auth_index: 'antigravity-builder-01',
      source: 'builder',
      source_hash: 'src_antigravity_builder',
      account_snapshot: 'Builder Lab',
      auth_label_snapshot: 'Antigravity Builder',
      auth_provider_snapshot: 'antigravity',
      calls: 960,
      failure_calls: 12,
      total_tokens: 820_000,
      cost: 14.4,
      average_latency_ms: 1320,
      last_seen_ms: analyticsNow - 27 * minute,
      models: accountStats[9].models,
    },
    {
      id: 'xai-ops-01',
      auth_file_snapshot: 'xai-ops.json',
      auth_index: 'xai-ops-01',
      source: 'ops',
      source_hash: 'src_xai_ops',
      account_snapshot: 'Ops Console',
      auth_label_snapshot: 'xAI Ops',
      auth_provider_snapshot: 'xai',
      calls: 860,
      failure_calls: 14,
      total_tokens: 690_000,
      cost: 12.2,
      average_latency_ms: 1490,
      last_seen_ms: analyticsNow - 55 * minute,
      models: accountStats[10].models,
    },
    {
      id: 'openai-support-02',
      auth_file_snapshot: 'openai-support-02.json',
      auth_index: 'openai-support-02',
      source: 'support',
      source_hash: 'src_openai_support',
      account_snapshot: 'Support Desk',
      auth_label_snapshot: 'OpenAI Support',
      auth_provider_snapshot: 'openai',
      calls: 2480,
      failure_calls: 28,
      total_tokens: 1_920_000,
      cost: 32.4,
      average_latency_ms: 980,
      last_seen_ms: analyticsNow - 11 * minute,
      models: accountStats[5].models,
    },
    {
      id: 'claude-research-02',
      auth_file_snapshot: 'claude-research-02.json',
      auth_index: 'claude-research-02',
      source: 'batch',
      source_hash: 'src_claude_batch',
      account_snapshot: 'Batch Research',
      auth_label_snapshot: 'Claude Batch',
      auth_provider_snapshot: 'claude',
      calls: 2100,
      failure_calls: 42,
      total_tokens: 3_080_000,
      cost: 83.5,
      average_latency_ms: 1510,
      last_seen_ms: analyticsNow - 19 * minute,
      models: accountStats[6].models,
    },
    {
      id: 'gemini-batch-02',
      auth_file_snapshot: 'gemini-batch-02.json',
      auth_index: 'gemini-batch-02',
      source: 'batch',
      source_hash: 'src_gemini_batch',
      account_snapshot: 'Gemini Batch',
      auth_label_snapshot: 'Gemini Batch',
      auth_provider_snapshot: 'gemini',
      auth_project_id_snapshot: 'demo-gemini-batch',
      calls: 1980,
      failure_calls: 25,
      total_tokens: 1_840_000,
      cost: 38.2,
      average_latency_ms: 1120,
      last_seen_ms: analyticsNow - 24 * minute,
      models: accountStats[7].models,
    },
    {
      id: 'deepseek-ops-01',
      auth_file_snapshot: 'deepseek-ops-01.json',
      auth_index: 'deepseek-ops-01',
      source: 'ops',
      source_hash: 'src_deepseek_ops',
      account_snapshot: 'Edge Experiments',
      auth_label_snapshot: 'DeepSeek Ops',
      auth_provider_snapshot: 'deepseek',
      calls: 740,
      failure_calls: 20,
      total_tokens: 610_000,
      cost: 6.8,
      average_latency_ms: 1580,
      last_seen_ms: analyticsNow - 44 * minute,
      models: accountStats[11].models,
    },
  ].map((row) => {
    const tokenSplit = splitTokens(row.total_tokens);
    const successCalls = row.calls - row.failure_calls;
    return {
      ...row,
      success_calls: successCalls,
      success_rate: safeRate(successCalls, row.calls),
      input_tokens: tokenSplit.input_tokens,
      output_tokens: tokenSplit.output_tokens,
      cached_tokens: tokenSplit.cached_tokens,
      cache_read_tokens: tokenSplit.cache_read_tokens,
      cache_creation_tokens: tokenSplit.cache_creation_tokens,
    };
  });

  const apiKeyStats = [
    {
      id: 'hash_openai_primary',
      api_key_hash: 'hash_openai_primary',
      account_snapshot: 'OpenAI Compatible',
      auth_label_snapshot: 'OpenAI Primary',
      auth_provider_snapshot: 'openai',
      auth_indices: ['openai-primary'],
      sources: ['gateway'],
      source_hashes: ['src_openai_primary'],
      calls: 3540,
      failure_calls: 39,
      total_tokens: 2_700_000,
      cost: 45.8,
      average_latency_ms: 1080,
      last_seen_ms: analyticsNow - 10 * minute,
      models: accountStats[3].models,
    },
    {
      id: 'hash_codex_team',
      api_key_hash: 'hash_codex_team',
      account_snapshot: 'Platform Team',
      auth_label_snapshot: 'Codex Team',
      auth_provider_snapshot: 'codex',
      auth_indices: ['codex-team-01'],
      sources: ['team'],
      source_hashes: ['src_codex_team'],
      calls: 5200,
      failure_calls: 62,
      total_tokens: 4_220_000,
      cost: 88.1,
      average_latency_ms: 1220,
      last_seen_ms: analyticsNow - 8 * minute,
      models: accountStats[0].models,
    },
    {
      id: 'hash_gemini_prod',
      api_key_hash: 'hash_gemini_prod',
      account_snapshot: 'Gemini Production',
      auth_label_snapshot: 'Gemini Production',
      auth_provider_snapshot: 'gemini',
      auth_indices: ['gemini-prod-01', 'vertex-regional-01'],
      sources: ['gateway', 'regional'],
      source_hashes: ['src_gemini_prod', 'src_vertex_regional'],
      calls: 5760,
      failure_calls: 98,
      total_tokens: 6_360_000,
      cost: 153.3,
      average_latency_ms: 1160,
      last_seen_ms: analyticsNow - 6 * minute,
      models: accountStats[2].models,
    },
    {
      id: 'hash_automation_pool',
      api_key_hash: 'hash_automation_pool',
      account_snapshot: 'Automation Pool',
      auth_label_snapshot: 'Fallback Pool',
      auth_provider_snapshot: 'codex',
      auth_indices: ['codex-fallback-02'],
      sources: ['automation'],
      source_hashes: ['src_fallback_pool'],
      calls: 1560,
      failure_calls: 46,
      total_tokens: 1_260_000,
      cost: 31.7,
      average_latency_ms: 2140,
      last_seen_ms: analyticsNow - 18 * minute,
      models: accountStats[4].models,
    },
    {
      id: 'hash_research_shared',
      api_key_hash: 'hash_research_shared',
      account_snapshot: 'Research Team',
      auth_label_snapshot: 'Claude Team',
      auth_provider_snapshot: 'claude',
      auth_indices: ['claude-team-01', 'kimi-coding-01'],
      sources: ['research', 'coding'],
      source_hashes: ['src_claude_team', 'src_kimi_coding'],
      calls: 5000,
      failure_calls: 112,
      total_tokens: 6_260_000,
      cost: 167.1,
      average_latency_ms: 1420,
      last_seen_ms: analyticsNow - 13 * minute,
      models: [...accountStats[1].models, ...credentialStats[5].models],
    },
    {
      id: 'hash_support_console',
      api_key_hash: 'hash_support_console',
      account_snapshot: 'Support Desk',
      auth_label_snapshot: 'OpenAI Support',
      auth_provider_snapshot: 'openai',
      auth_indices: ['openai-support-02'],
      sources: ['support'],
      source_hashes: ['src_openai_support'],
      calls: 2480,
      failure_calls: 28,
      total_tokens: 1_920_000,
      cost: 32.4,
      average_latency_ms: 980,
      last_seen_ms: analyticsNow - 11 * minute,
      models: accountStats[5].models,
    },
    {
      id: 'hash_research_batch',
      api_key_hash: 'hash_research_batch',
      account_snapshot: 'Batch Research',
      auth_label_snapshot: 'Claude Batch',
      auth_provider_snapshot: 'claude',
      auth_indices: ['claude-research-02'],
      sources: ['batch'],
      source_hashes: ['src_claude_batch'],
      calls: 2100,
      failure_calls: 42,
      total_tokens: 3_080_000,
      cost: 83.5,
      average_latency_ms: 1510,
      last_seen_ms: analyticsNow - 19 * minute,
      models: accountStats[6].models,
    },
    {
      id: 'hash_gemini_batch',
      api_key_hash: 'hash_gemini_batch',
      account_snapshot: 'Gemini Batch',
      auth_label_snapshot: 'Gemini Batch',
      auth_provider_snapshot: 'gemini',
      auth_indices: ['gemini-batch-02'],
      sources: ['batch'],
      source_hashes: ['src_gemini_batch'],
      calls: 1980,
      failure_calls: 25,
      total_tokens: 1_840_000,
      cost: 38.2,
      average_latency_ms: 1120,
      last_seen_ms: analyticsNow - 24 * minute,
      models: accountStats[7].models,
    },
    {
      id: 'hash_kimi_coding',
      api_key_hash: 'hash_kimi_coding',
      account_snapshot: 'Kimi Coding',
      auth_label_snapshot: 'Kimi Coding',
      auth_provider_snapshot: 'kimi',
      auth_indices: ['kimi-coding-01'],
      sources: ['coding'],
      source_hashes: ['src_kimi_coding'],
      calls: 1220,
      failure_calls: 36,
      total_tokens: 980_000,
      cost: 15.8,
      average_latency_ms: 1710,
      last_seen_ms: analyticsNow - 48 * minute,
      models: accountStats[8].models,
    },
    {
      id: 'hash_builder_lab',
      api_key_hash: 'hash_builder_lab',
      account_snapshot: 'Builder Lab',
      auth_label_snapshot: 'Antigravity Builder',
      auth_provider_snapshot: 'antigravity',
      auth_indices: ['antigravity-builder-01'],
      sources: ['builder'],
      source_hashes: ['src_antigravity_builder'],
      calls: 960,
      failure_calls: 12,
      total_tokens: 820_000,
      cost: 14.4,
      average_latency_ms: 1320,
      last_seen_ms: analyticsNow - 27 * minute,
      models: accountStats[9].models,
    },
    {
      id: 'hash_xai_ops',
      api_key_hash: 'hash_xai_ops',
      account_snapshot: 'Ops Console',
      auth_label_snapshot: 'xAI Ops',
      auth_provider_snapshot: 'xai',
      auth_indices: ['xai-ops-01'],
      sources: ['ops'],
      source_hashes: ['src_xai_ops'],
      calls: 860,
      failure_calls: 14,
      total_tokens: 690_000,
      cost: 12.2,
      average_latency_ms: 1490,
      last_seen_ms: analyticsNow - 55 * minute,
      models: accountStats[10].models,
    },
    {
      id: 'hash_deepseek_ops',
      api_key_hash: 'hash_deepseek_ops',
      account_snapshot: 'Edge Experiments',
      auth_label_snapshot: 'DeepSeek Ops',
      auth_provider_snapshot: 'deepseek',
      auth_indices: ['deepseek-ops-01'],
      sources: ['ops'],
      source_hashes: ['src_deepseek_ops'],
      calls: 740,
      failure_calls: 20,
      total_tokens: 610_000,
      cost: 6.8,
      average_latency_ms: 1580,
      last_seen_ms: analyticsNow - 44 * minute,
      models: accountStats[11].models,
    },
  ].map((row) => {
    const tokenSplit = splitTokens(row.total_tokens);
    const successCalls = row.calls - row.failure_calls;
    return {
      ...row,
      success_calls: successCalls,
      success_rate: safeRate(successCalls, row.calls),
      input_tokens: tokenSplit.input_tokens,
      output_tokens: tokenSplit.output_tokens,
      cached_tokens: tokenSplit.cached_tokens,
      cache_read_tokens: tokenSplit.cache_read_tokens,
      cache_creation_tokens: tokenSplit.cache_creation_tokens,
      contexts: row.auth_indices.map((authIndex, index) => {
        const calls = Math.round(row.calls / row.auth_indices.length);
        const failureCalls = Math.max(0, Math.round(row.failure_calls / row.auth_indices.length));
        return {
          id: `${row.id}-${authIndex}`,
          account_snapshot: row.account_snapshot,
          auth_label_snapshot: row.auth_label_snapshot,
          auth_provider_snapshot: row.auth_provider_snapshot,
          auth_index: authIndex,
          source: row.sources[index] ?? row.sources[0],
          source_hash: row.source_hashes[index] ?? row.source_hashes[0],
          calls,
          success_calls: calls - failureCalls,
          failure_calls: failureCalls,
          success_rate: safeRate(calls - failureCalls, calls),
          failure_rate: safeRate(failureCalls, calls),
          total_tokens: Math.round(row.total_tokens / row.auth_indices.length),
          cost: round2(row.cost / row.auth_indices.length),
          average_latency_ms: row.average_latency_ms,
          last_seen_ms: row.last_seen_ms,
        };
      }),
    };
  });

  const channelShare = accountStats.map((row) => ({
    auth_index: row.auth_indices?.[0] ?? row.id,
    source: row.sources?.[0],
    account_snapshot: row.account_snapshot,
    auth_label_snapshot: row.auth_label_snapshot,
    auth_provider_snapshot: row.auth_provider_snapshot,
    calls: row.calls,
    success: row.success_calls,
    failure: row.failure_calls,
    tokens: row.total_tokens,
    cost: row.cost,
    average_latency_ms: row.average_latency_ms,
  }));

  const eventProfiles = [
    {
      model: 'gpt-4.1-mini',
      apiKeyHash: 'hash_openai_primary',
      authIndex: 'openai-primary',
      authFile: 'openai-primary.json',
      account: 'OpenAI Compatible',
      label: 'OpenAI Primary',
      provider: 'openai',
      source: 'gateway',
      sourceHash: 'src_openai_primary',
      endpoint: '/v1/chat/completions',
      executor: 'dashboard',
    },
    {
      model: 'claude-sonnet-4-5',
      apiKeyHash: 'hash_research_shared',
      authIndex: 'claude-team-01',
      authFile: 'claude-team-01.json',
      account: 'Research Team',
      label: 'Claude Team',
      provider: 'claude',
      source: 'research',
      sourceHash: 'src_claude_team',
      endpoint: '/v1/messages',
      executor: 'batch',
    },
    {
      model: 'gemini-2.5-pro',
      apiKeyHash: 'hash_gemini_prod',
      authIndex: 'gemini-prod-01',
      authFile: 'gemini-prod-01.json',
      account: 'Gemini Production',
      label: 'Gemini Production',
      provider: 'gemini',
      source: 'gateway',
      sourceHash: 'src_gemini_prod',
      endpoint: '/v1beta/models/gemini-2.5-pro:generateContent',
      executor: 'workflow',
    },
    {
      model: 'gpt-4.1',
      apiKeyHash: 'hash_codex_team',
      authIndex: 'codex-team-01',
      authFile: 'codex-team-01.json',
      account: 'Platform Team',
      label: 'Codex Team',
      provider: 'codex',
      source: 'team',
      sourceHash: 'src_codex_team',
      endpoint: '/v1/responses',
      executor: 'interactive',
    },
    {
      model: 'gemini-2.5-flash',
      apiKeyHash: 'hash_gemini_prod',
      authIndex: 'vertex-regional-01',
      authFile: 'vertex-regional-01.json',
      account: 'Gemini Production',
      label: 'Vertex Regional',
      provider: 'vertex',
      source: 'regional',
      sourceHash: 'src_vertex_regional',
      endpoint: '/v1/projects/demo-vertex-regional/locations/us-central1/publishers/google/models/gemini-2.5-flash:generateContent',
      executor: 'worker',
    },
    {
      model: 'gpt-4.1-mini',
      apiKeyHash: 'hash_automation_pool',
      authIndex: 'codex-fallback-02',
      authFile: 'codex-fallback-02.json',
      account: 'Automation Pool',
      label: 'Fallback Pool',
      provider: 'codex',
      source: 'automation',
      sourceHash: 'src_fallback_pool',
      endpoint: '/v1/chat/completions',
      executor: 'retry',
    },
    {
      model: 'gpt-4.1-mini',
      apiKeyHash: 'hash_support_console',
      authIndex: 'openai-support-02',
      authFile: 'openai-support-02.json',
      account: 'Support Desk',
      label: 'OpenAI Support',
      provider: 'openai',
      source: 'support',
      sourceHash: 'src_openai_support',
      endpoint: '/v1/chat/completions',
      executor: 'ticket',
    },
    {
      model: 'claude-haiku-4-5',
      apiKeyHash: 'hash_research_batch',
      authIndex: 'claude-research-02',
      authFile: 'claude-research-02.json',
      account: 'Batch Research',
      label: 'Claude Batch',
      provider: 'claude',
      source: 'batch',
      sourceHash: 'src_claude_batch',
      endpoint: '/v1/messages',
      executor: 'batch',
    },
    {
      model: 'gemini-2.5-flash',
      apiKeyHash: 'hash_gemini_batch',
      authIndex: 'gemini-batch-02',
      authFile: 'gemini-batch-02.json',
      account: 'Gemini Batch',
      label: 'Gemini Batch',
      provider: 'gemini',
      source: 'batch',
      sourceHash: 'src_gemini_batch',
      endpoint: '/v1beta/models/gemini-2.5-flash:generateContent',
      executor: 'batch',
    },
    {
      model: 'qwen-plus',
      apiKeyHash: 'hash_kimi_coding',
      authIndex: 'kimi-coding-01',
      authFile: 'kimi-coding.json',
      account: 'Kimi Coding',
      label: 'Kimi Coding',
      provider: 'kimi',
      source: 'coding',
      sourceHash: 'src_kimi_coding',
      endpoint: '/v1/chat/completions',
      executor: 'coding',
    },
    {
      model: 'grok-4-fast',
      apiKeyHash: 'hash_xai_ops',
      authIndex: 'xai-ops-01',
      authFile: 'xai-ops.json',
      account: 'Ops Console',
      label: 'xAI Ops',
      provider: 'xai',
      source: 'ops',
      sourceHash: 'src_xai_ops',
      endpoint: '/v1/chat/completions',
      executor: 'ops',
    },
    {
      model: 'deepseek-chat',
      apiKeyHash: 'hash_deepseek_ops',
      authIndex: 'deepseek-ops-01',
      authFile: 'deepseek-ops-01.json',
      account: 'Edge Experiments',
      label: 'DeepSeek Ops',
      provider: 'deepseek',
      source: 'ops',
      sourceHash: 'src_deepseek_ops',
      endpoint: '/v1/chat/completions',
      executor: 'ops',
    },
  ];

  const events: DemoMonitoringEventRow[] = Array.from({ length: 72 }, (_, index) => {
    const profile = eventProfiles[index % eventProfiles.length];
    const failed = index % 9 === 0 || index % 22 === 0;
    const quotaFailure = failed && index % 2 === 0;
    const inputTokens = 620 + ((index * 113) % 2600);
    const outputTokens = 210 + ((index * 71) % 980);
    const cachedTokens = index % 3 === 0 ? 180 + ((index * 17) % 520) : 0;
    const reasoningTokens = index % 4 === 0 ? 80 + ((index * 13) % 360) : 0;
    const totalTokens = inputTokens + outputTokens + cachedTokens + reasoningTokens;
    const timestampMs = analyticsNow - (index * 5 + (index % 4)) * minute;
    return {
      request_id: `demo-request-${String(index + 1).padStart(3, '0')}`,
      event_hash: `demo-event-${String(index + 1).padStart(3, '0')}`,
      timestamp_ms: timestampMs,
      model: profile.model,
      endpoint: profile.endpoint,
      method: 'POST',
      path: profile.endpoint,
      auth_index: profile.authIndex,
      auth_file_snapshot: profile.authFile,
      source: profile.source,
      source_hash: profile.sourceHash,
      api_key_hash: profile.apiKeyHash,
      account_snapshot: profile.account,
      auth_label_snapshot: profile.label,
      auth_provider_snapshot: profile.provider,
      auth_project_id_snapshot:
        profile.provider === 'gemini' || profile.provider === 'vertex'
          ? 'demo-gemini-prod'
          : undefined,
      resolved_model: profile.model,
      reasoning_effort: index % 4 === 0 ? 'medium' : undefined,
      service_tier: index % 5 === 0 ? 'priority' : 'standard',
      executor_type: profile.executor,
      input_tokens: inputTokens,
      output_tokens: outputTokens,
      cached_tokens: cachedTokens,
      cache_read_tokens: Math.round(cachedTokens * 0.78),
      cache_creation_tokens: Math.round(cachedTokens * 0.22),
      reasoning_tokens: reasoningTokens,
      total_tokens: totalTokens,
      latency_ms: failed ? 2400 + ((index * 97) % 1800) : 780 + ((index * 83) % 1540),
      ttft_ms: failed ? 820 + ((index * 23) % 360) : 180 + ((index * 19) % 420),
      failed,
      fail_status_code: failed ? (quotaFailure ? 429 : 503) : undefined,
      fail_summary: failed
        ? quotaFailure
          ? 'Quota window reached'
          : 'Upstream response timeout'
        : undefined,
      header_quota_recover_at_ms: quotaFailure ? analyticsNow + 68 * minute : undefined,
      header_quota_used_percent: quotaFailure ? 94 + (index % 5) : undefined,
      header_quota_plan_type: quotaFailure ? 'team' : undefined,
      header_error_kind: failed ? (quotaFailure ? 'quota' : 'upstream') : undefined,
      header_error_code: failed ? (quotaFailure ? 'rate_limit' : 'timeout') : undefined,
      header_trace_id: failed ? `demo-trace-${String(index + 1).padStart(3, '0')}` : undefined,
      response_metadata: failed
        ? {
            quota: quotaFailure
              ? {
                  plan_type: 'team',
                  recover_at_ms: analyticsNow + 68 * minute,
                  used_percent: 94 + (index % 5),
                }
              : undefined,
            errors: {
              kind: quotaFailure ? 'quota' : 'upstream',
              code: quotaFailure ? 'rate_limit' : 'timeout',
            },
            trace: {
              request_id: `demo-trace-${String(index + 1).padStart(3, '0')}`,
            },
          }
        : undefined,
    };
  });

  const recentFailures = events
    .filter((event) => event.failed)
    .slice(0, 8)
    .map((event) => ({
      timestamp_ms: event.timestamp_ms,
      model: event.model,
      api_key_hash: event.api_key_hash,
      source: event.source,
      source_hash: event.source_hash,
      auth_index: event.auth_index,
      account_snapshot: event.account_snapshot,
      auth_label_snapshot: event.auth_label_snapshot,
      auth_provider_snapshot: event.auth_provider_snapshot,
      auth_project_id_snapshot: event.auth_project_id_snapshot,
      endpoint: event.endpoint,
      duration_ms: event.latency_ms,
      fail_status_code: event.fail_status_code,
      fail_summary: event.fail_summary,
      response_metadata: event.response_metadata,
      header_quota_recover_at_ms: event.header_quota_recover_at_ms,
      header_quota_used_percent: event.header_quota_used_percent,
      header_quota_plan_type: event.header_quota_plan_type,
      header_error_kind: event.header_error_kind,
      header_error_code: event.header_error_code,
      header_trace_id: event.header_trace_id,
    }));

  const heatmap = Array.from({ length: 7 * 24 }, (_, index) => {
    const weekday = Math.floor(index / 24);
    const hourIndex = index % 24;
    const weekdayBoost = weekday >= 1 && weekday <= 5 ? 18 : 4;
    const officeBoost = hourIndex >= 9 && hourIndex <= 18 ? 42 : 0;
    const eveningBoost = hourIndex >= 20 && hourIndex <= 22 ? 16 : 0;
    const calls = Math.max(
      3,
      10 + weekdayBoost + officeBoost + eveningBoost + ((weekday * 13 + hourIndex * 7) % 28)
    );
    const failure = Math.round(calls * (hourIndex === 10 || hourIndex === 16 ? 0.075 : 0.024));
    const success = calls - failure;
    const tokens = calls * (780 + ((weekday + hourIndex) % 5) * 95);
    const cost = round2((tokens / 1_000_000) * (16 + (officeBoost ? 7 : 2)));
    const modelPrimaryCalls = Math.round(calls * 0.58);
    const modelSecondaryCalls = calls - modelPrimaryCalls;
    const primaryFailure = Math.round(failure * 0.55);
    const secondaryFailure = failure - primaryFailure;
    return {
      weekday,
      hour: hourIndex,
      calls,
      success,
      failure,
      tokens,
      cost,
      failure_rate: safeRate(failure, calls),
      model_contributors: [
        {
          key: hourIndex % 2 === 0 ? 'gpt-4.1-mini' : 'claude-sonnet-4-5',
          label: hourIndex % 2 === 0 ? 'gpt-4.1-mini' : 'claude-sonnet-4-5',
          calls: modelPrimaryCalls,
          success: modelPrimaryCalls - primaryFailure,
          failure: primaryFailure,
          tokens: Math.round(tokens * 0.58),
          cost: round2(cost * 0.58),
          failure_rate: safeRate(primaryFailure, modelPrimaryCalls),
          share: 0.58,
        },
        {
          key: hourIndex % 3 === 0 ? 'gemini-2.5-pro' : 'gpt-4.1',
          label: hourIndex % 3 === 0 ? 'gemini-2.5-pro' : 'gpt-4.1',
          calls: modelSecondaryCalls,
          success: modelSecondaryCalls - secondaryFailure,
          failure: secondaryFailure,
          tokens: Math.round(tokens * 0.42),
          cost: round2(cost * 0.42),
          failure_rate: safeRate(secondaryFailure, modelSecondaryCalls),
          share: 0.42,
        },
      ],
      api_key_contributors: [
        {
          key: hourIndex % 2 === 0 ? 'hash_openai_primary' : 'hash_codex_team',
          label: hourIndex % 2 === 0 ? 'OpenAI Primary' : 'Codex Team',
          calls: modelPrimaryCalls,
          success: modelPrimaryCalls - primaryFailure,
          failure: primaryFailure,
          tokens: Math.round(tokens * 0.58),
          cost: round2(cost * 0.58),
          failure_rate: safeRate(primaryFailure, modelPrimaryCalls),
          share: 0.58,
        },
        {
          key: hourIndex % 3 === 0 ? 'hash_gemini_prod' : 'hash_research_shared',
          label: hourIndex % 3 === 0 ? 'Gemini Production' : 'Research Shared',
          calls: modelSecondaryCalls,
          success: modelSecondaryCalls - secondaryFailure,
          failure: secondaryFailure,
          tokens: Math.round(tokens * 0.42),
          cost: round2(cost * 0.42),
          failure_rate: safeRate(secondaryFailure, modelSecondaryCalls),
          share: 0.42,
        },
      ],
      provider_contributors: [
        {
          key: hourIndex % 2 === 0 ? 'openai' : 'codex',
          label: hourIndex % 2 === 0 ? 'openai' : 'codex',
          calls: modelPrimaryCalls,
          success: modelPrimaryCalls - primaryFailure,
          failure: primaryFailure,
          tokens: Math.round(tokens * 0.58),
          cost: round2(cost * 0.58),
          failure_rate: safeRate(primaryFailure, modelPrimaryCalls),
          share: 0.58,
        },
        {
          key: hourIndex % 3 === 0 ? 'gemini' : 'claude',
          label: hourIndex % 3 === 0 ? 'gemini' : 'claude',
          calls: modelSecondaryCalls,
          success: modelSecondaryCalls - secondaryFailure,
          failure: secondaryFailure,
          tokens: Math.round(tokens * 0.42),
          cost: round2(cost * 0.42),
          failure_rate: safeRate(secondaryFailure, modelSecondaryCalls),
          share: 0.42,
        },
      ],
    };
  });

  const credentialTimeline = timeline.slice(-7).flatMap((point, dayIndex) =>
    credentialStats.slice(0, 10).map((credential, credentialIndex) => {
      const share = [0.26, 0.22, 0.18, 0.12, 0.09, 0.05][credentialIndex] ?? 0.04;
      const calls = Math.round(point.calls * share);
      const failure = Math.max(0, Math.round(point.failure * share));
      const tokens = Math.round(point.tokens * share);
      return {
        id: credential.id,
        label: credential.auth_label_snapshot,
        auth_file_snapshot: credential.auth_file_snapshot,
        auth_index: credential.auth_index,
        source: credential.source,
        source_hash: credential.source_hash,
        account_snapshot: credential.account_snapshot,
        auth_label_snapshot: credential.auth_label_snapshot,
        auth_provider_snapshot: credential.auth_provider_snapshot,
        auth_project_id_snapshot: credential.auth_project_id_snapshot,
        bucket_ms: point.bucket_ms + dayIndex,
        bucket_label: point.label,
        calls,
        tokens,
        success: calls - failure,
        failure,
        total_tokens: tokens,
        cost: round2((credential.cost / 14) * (0.82 + credentialIndex * 0.04)),
        average_latency_ms: credential.average_latency_ms,
        success_rate: safeRate(calls - failure, calls),
        failure_rate: safeRate(failure, calls),
      };
    })
  );

  const eventsPageRequest = request?.include?.events_page;
  const eventsPage = paginateDemoEvents(
    events,
    eventsPageRequest?.limit ?? events.length,
    eventsPageRequest?.before_ms
  );
  const drilldownRequest = request?.include?.drilldown_preview;
  const drilldownPreview = paginateDemoEvents(events, drilldownRequest?.limit ?? 12, null);

  return {
    generated_at_ms: analyticsNow,
    granularity: 'day',
    summary: {
      total_calls: summaryCalls,
      success_calls: summarySuccess,
      failure_calls: summaryFailures,
      success_rate: safeRate(summarySuccess, summaryCalls),
      input_tokens: summaryInputTokens,
      output_tokens: summaryOutputTokens,
      cached_tokens: summaryCachedTokens,
      cache_read_tokens: summaryCacheReadTokens,
      cache_creation_tokens: summaryCacheCreationTokens,
      reasoning_tokens: Math.max(
        0,
        summaryTokens - summaryInputTokens - summaryOutputTokens - summaryCachedTokens
      ),
      total_tokens: summaryTokens,
      total_cost: summaryCost,
      average_cost_per_call: safeRate(summaryCost, summaryCalls),
      average_latency_ms: 1280,
      p95_latency_ms: 2460,
      p95_ttft_ms: 820,
      zero_token_calls: 19,
      rpm_30m: dashboard.rolling_30m.rpm,
      tpm_30m: dashboard.rolling_30m.tpm,
      avg_daily_requests: Math.round(summaryCalls / 14),
      avg_daily_tokens: Math.round(summaryTokens / 14),
      approx_tasks: 428,
      approx_task_failures: 12,
      approx_task_success_rate: 0.972,
      zero_token_models: ['gpt-4.1-mini', 'gemini-2.5-flash'],
    },
    summary_comparison: {
      from_ms: analyticsNow - 28 * day,
      to_ms: analyticsNow - 14 * day,
      total_calls: 18_400,
      success_calls: 17_910,
      failure_calls: 490,
      success_rate: 0.973,
      total_tokens: 18_600_000,
      total_cost: 382.42,
    },
    timeline,
    hourly_distribution: Array.from({ length: 24 }, (_, hourIndex) => ({
      hour: hourIndex,
      calls:
        24 +
        ((hourIndex * 11) % 80) +
        (hourIndex >= 9 && hourIndex <= 18 ? 42 : 0),
      tokens: 24_000 + ((hourIndex * 7100) % 90_000),
    })),
    heatmap,
    anomaly_points: [
      {
        bucket_ms: timeline[9].bucket_ms,
        bucket_end_ms: timeline[9].bucket_end_ms ?? timeline[9].bucket_ms + day,
        label: timeline[9].label,
        severity: 'high',
        metric_keys: ['request_spike', 'cost_spike'],
        calls: timeline[9].calls,
        total_tokens: timeline[9].total_tokens,
        cost: timeline[9].cost,
        failure_rate: timeline[9].failure_rate,
        request_change: 1.18,
        cost_change: 1.34,
        tokens_per_request_change: 0.22,
        cache_hit_rate_change: -0.06,
        failure_rate_change: 0.012,
        latency_p95_change: 0.18,
      },
      {
        bucket_ms: timeline[12].bucket_ms,
        bucket_end_ms: timeline[12].bucket_end_ms ?? timeline[12].bucket_ms + day,
        label: timeline[12].label,
        severity: 'medium',
        metric_keys: ['failure_rate_spike', 'latency_spike'],
        calls: timeline[12].calls,
        total_tokens: timeline[12].total_tokens,
        cost: timeline[12].cost,
        failure_rate: timeline[12].failure_rate,
        request_change: 0.34,
        cost_change: 0.42,
        tokens_per_request_change: 0.08,
        cache_hit_rate_change: -0.03,
        failure_rate_change: 0.021,
        latency_p95_change: 0.27,
      },
    ],
    model_share: modelStats.map((row) => ({
      model: row.model,
      calls: row.calls,
      tokens: row.total_tokens,
      cost: row.cost,
    })),
    model_stats: modelStats,
    channel_share: channelShare,
    failure_sources: [
      {
        source: 'automation',
        source_hash: 'src_fallback_pool',
        auth_index: 'codex-fallback-02',
        account_snapshot: 'Automation Pool',
        auth_label_snapshot: 'Fallback Pool',
        auth_provider_snapshot: 'codex',
        calls: 1560,
        failure: 46,
        last_seen_ms: analyticsNow - 18 * minute,
        average_latency_ms: 2140,
      },
      {
        source: 'regional',
        source_hash: 'src_vertex_regional',
        auth_index: 'vertex-regional-01',
        account_snapshot: 'Gemini Production',
        auth_label_snapshot: 'Vertex Regional',
        auth_provider_snapshot: 'vertex',
        calls: 2140,
        failure: 24,
        last_seen_ms: analyticsNow - 6 * minute,
        average_latency_ms: 1040,
      },
      {
        source: 'research',
        source_hash: 'src_claude_team',
        auth_index: 'claude-team-01',
        account_snapshot: 'Research Team',
        auth_label_snapshot: 'Claude Team',
        auth_provider_snapshot: 'claude',
        calls: 4380,
        failure: 96,
        last_seen_ms: analyticsNow - 13 * minute,
        average_latency_ms: 1380,
      },
    ],
    account_stats: accountStats,
    credential_stats: credentialStats,
    credential_timeline: credentialTimeline,
    api_key_stats: apiKeyStats,
    filter_options: {
      account_stats: accountStats,
      api_key_stats: apiKeyStats,
      channel_share: channelShare,
      model_stats: modelStats,
      providers: [
        'codex',
        'claude',
        'gemini',
        'openai',
        'vertex',
        'kimi',
        'antigravity',
        'xai',
        'deepseek',
      ],
      auth_files: demoAuthFiles.files.map((file) => file.name),
      project_ids: ['demo-gemini-prod', 'demo-vertex-regional', 'demo-gemini-batch'],
      request_types: ['chat', 'responses', 'models'],
      header_error_kinds: ['quota', 'upstream'],
      header_error_codes: ['rate_limit', 'timeout'],
      header_quota_plans: ['team', 'pro'],
      header_trace_ids: recentFailures
        .map((failure) => failure.header_trace_id)
        .filter((traceId): traceId is string => Boolean(traceId)),
    },
    task_buckets: [
      {
        bucket_key: 'team-dashboard-refresh',
        total: 1880,
        success: 1848,
        failure: 32,
        first_ms: analyticsNow - 6 * day,
        last_ms: analyticsNow - 8 * minute,
        source: 'team',
        source_hash: 'src_codex_team',
        auth_index: 'codex-team-01',
        models: ['gpt-4.1-mini', 'gpt-4.1'],
        endpoints: ['/v1/chat/completions', '/v1/responses'],
        input_tokens: 1_260_000,
        output_tokens: 540_000,
        cached_tokens: 260_000,
        cache_read_tokens: 210_000,
        cache_creation_tokens: 50_000,
        total_tokens: 2_060_000,
        average_latency_ms: 1220,
        max_latency_ms: 3160,
      },
      {
        bucket_key: 'research-batch-analysis',
        total: 1240,
        success: 1206,
        failure: 34,
        first_ms: analyticsNow - 5 * day,
        last_ms: analyticsNow - 13 * minute,
        source: 'research',
        source_hash: 'src_claude_team',
        auth_index: 'claude-team-01',
        models: ['claude-sonnet-4-5'],
        endpoints: ['/v1/messages'],
        input_tokens: 1_480_000,
        output_tokens: 620_000,
        cached_tokens: 180_000,
        cache_read_tokens: 150_000,
        cache_creation_tokens: 30_000,
        total_tokens: 2_280_000,
        average_latency_ms: 1380,
        max_latency_ms: 4280,
      },
    ],
    recent_failures: recentFailures,
    events: eventsPage,
    drilldown_preview: drilldownPreview,
  };
};

const demoInspectionRunDetail = (baseNow = now()): CodexInspectionRunDetail => ({
  run: {
    id: 1001,
    triggerType: 'schedule',
    triggerKey: 'interval:45m',
    status: 'completed',
    startedAtMs: baseNow - 42 * 60 * 1000,
    finishedAtMs: baseNow - 39 * 60 * 1000,
    totalFiles: 24,
    probeSetCount: 24,
    sampledCount: 18,
    disabledCount: 2,
    enabledCount: 16,
    deleteCount: 0,
    disableCount: 2,
    enableCount: 1,
    reauthCount: 1,
    keepCount: 14,
    createdAtMs: baseNow - 42 * 60 * 1000,
    updatedAtMs: baseNow - 39 * 60 * 1000,
    settings: demoManagerConfig.config.codexInspection,
  },
  results: [
    {
      id: 501,
      runId: 1001,
      accountKey: 'codex-team-01',
      fileName: 'codex-team-01.json',
      displayAccount: 'Platform Team',
      authIndex: 'codex-team-01',
      accountId: 'acct_codex_team',
      provider: 'codex',
      disabled: false,
      status: 'ok',
      state: 'active',
      action: 'keep',
      actionReason: 'Healthy quota',
      actionStatus: 'done',
      usedPercent: 63,
      isQuota: true,
      planType: 'team',
      quotaWindows: [
        {
          id: 'primary',
          labelKey: 'codex_quota.primary_window',
          usedPercent: 63,
          resetLabel: '2h 18m',
          limitWindowSeconds: 18000,
        },
      ],
      createdAtMs: baseNow - 41 * 60 * 1000,
    },
    {
      id: 502,
      runId: 1001,
      accountKey: 'codex-fallback-02',
      fileName: 'codex-fallback-02.json',
      displayAccount: 'Automation Pool',
      authIndex: 'codex-fallback-02',
      accountId: 'acct_codex_auto',
      provider: 'codex',
      disabled: true,
      status: 'quota',
      state: 'disabled',
      action: 'disable',
      actionReason: 'Quota above threshold',
      actionStatus: 'done',
      statusCode: 429,
      usedPercent: 96,
      isQuota: true,
      planType: 'team',
      errorKind: 'quota',
      errorDetail: 'Primary quota window reached',
      createdAtMs: baseNow - 40 * 60 * 1000,
    },
  ],
  logs: [
    {
      id: 9001,
      runId: 1001,
      level: 'info',
      message: 'Inspection completed for 24 auth files',
      createdAtMs: baseNow - 39 * 60 * 1000,
    },
  ],
});

const demoAccountCandidates: AccountActionCandidate[] = [
  {
    id: 201,
    actionType: 'reauth',
    status: 'pending',
    provider: 'codex',
    authFileName: 'codex-fallback-02.json',
    authIndex: 'codex-fallback-02',
    accountSnapshot: 'Automation Pool',
    accountIdSnapshot: 'acct_codex_auto',
    authLabel: 'Fallback Pool',
    reason: 'Repeated quota and authentication warnings',
    firstSeenAtMs: now() - 2 * day,
    lastSeenAtMs: now() - 18 * 60 * 1000,
    hitCount: 6,
    createdAtMs: now() - 2 * day,
    updatedAtMs: now() - 18 * 60 * 1000,
  },
  {
    id: 202,
    actionType: 'review',
    status: 'pending',
    provider: 'kimi',
    authFileName: 'kimi-coding.json',
    authIndex: 'kimi-coding-01',
    accountSnapshot: 'Kimi Coding',
    authLabel: 'Kimi Coding',
    reason: 'High failure rate in the last 24 hours',
    firstSeenAtMs: now() - day,
    lastSeenAtMs: now() - 3 * hour,
    hitCount: 3,
    createdAtMs: now() - day,
    updatedAtMs: now() - 3 * hour,
  },
];

export const getDemoRawConfig = () => clone(initialRawConfig);
export const getDemoProviderModels = () => clone(demoProviderModels);
export const getDemoAuthFiles = () => clone(demoAuthFiles);
export const getDemoPlugins = () => clone(demoPlugins);
export const getDemoPluginStore = () => clone(demoPluginStore);
export const getDemoManagerConfig = () => clone(demoManagerConfig);
export const getDemoDashboardSummary = () => clone(dashboardBase());
export const getDemoMonitoringAnalytics = (request?: MonitoringAnalyticsRequest) =>
  clone(buildMonitoringAnalytics(undefined, request));
export const getDemoModelPrices = () => clone(demoModelPrices);
export const getDemoUsagePayload = () => {
  const dashboard = dashboardBase();
  return {
    total_requests: dashboard.today.total_calls,
    success_count: dashboard.today.success_calls,
    failure_count: dashboard.today.failure_calls,
    total_tokens: dashboard.today.total_tokens,
    apis: {
      'gpt-4.1-mini': {
        total_requests: 520,
        success_count: 516,
        failure_count: 4,
        total_tokens: 392_000,
      },
      'claude-sonnet-4-5': {
        total_requests: 416,
        success_count: 408,
        failure_count: 8,
        total_tokens: 486_000,
      },
      'gemini-2.5-pro': {
        total_requests: 384,
        success_count: 379,
        failure_count: 5,
        total_tokens: 421_000,
      },
      'gpt-4.1': {
        total_requests: 318,
        success_count: 310,
        failure_count: 8,
        total_tokens: 386_000,
      },
    },
  };
};

export const getDemoUsageServiceInfo = (): UsageServiceInfo => ({
  service: 'cpa-manager-plus',
  mode: 'demo',
  startedAt: now() - 4 * day,
  configured: true,
  adminReady: true,
  projectInitialized: true,
  setupRequired: false,
  migrationStatus: 'ready',
  dataKeyReady: true,
  hasHistoricalData: true,
});

export const getDemoUsageServiceStatus = (): UsageServiceStatus => ({
  service: 'cpa-manager-plus',
  dbPath: '/data/demo-usage.sqlite',
  events: 184_260,
  deadLetters: 3,
  collector: {
    collector: 'usage-events',
    upstream: DEMO_API_BASE,
    mode: 'http',
    transport: 'http',
    queue: 'usage-events',
    lastConsumedAt: now() - 8_000,
    lastInsertedAt: now() - 7_000,
    totalInserted: 184_260,
    totalSkipped: 124,
    deadLetters: 3,
  },
});

export const getDemoAccountProcessingPolicy = (): AccountProcessingPolicy => ({
  source: 'db',
  updatedAtMs: now() - hour,
  codexQuotaCooldown: {
    enabled: true,
    configured: true,
    source: 'db',
    locked: false,
    envKey: 'CPA_CODEX_QUOTA_COOLDOWN_ENABLED',
    configFileKey: 'codexQuotaCooldownEnabled',
  },
  authIssueQueue: {
    enabled: true,
    configured: true,
    source: 'db',
    locked: false,
    envKey: 'CPA_AUTH_ISSUE_QUEUE_ENABLED',
    configFileKey: 'authIssueQueueEnabled',
  },
  authIssueAutoDisable: {
    enabled: true,
    configured: true,
    source: 'db',
    locked: false,
    envKey: 'CPA_AUTH_ISSUE_AUTO_DISABLE_ENABLED',
    configFileKey: 'authIssueAutoDisableEnabled',
  },
});

export const getDemoQuotaCooldowns = (): QuotaCooldownInfo[] => [
  {
    authFileName: 'codex-fallback-02.json',
    authIndex: 'codex-fallback-02',
    provider: 'codex',
    owner: 'Automation Pool',
    recoverAtMs: now() + 68 * 60 * 1000,
    disabledAtMs: now() - 18 * 60 * 1000,
    createdAtMs: now() - 18 * 60 * 1000,
  },
];

export const getDemoHeaderSnapshots = (): UsageHeaderSnapshotsResponse => ({
  generated_at_ms: now(),
  from_ms: now() - 30 * day,
  to_ms: now(),
  items: [
    {
      event_hash: 'demo-event-1',
      timestamp_ms: now() - 18 * 60 * 1000,
      auth_file_snapshot: 'codex-fallback-02.json',
      auth_index: 'codex-fallback-02',
      account_snapshot: 'Automation Pool',
      auth_label_snapshot: 'Fallback Pool',
      auth_provider_snapshot: 'codex',
      source: 'automation',
      source_hash: 'src_fallback_pool',
      header_quota_recover_at_ms: now() + 68 * 60 * 1000,
      header_quota_used_percent: 96,
      header_quota_plan_type: 'team',
      header_error_kind: 'quota',
      header_error_code: 'rate_limit',
      header_trace_id: 'demo-trace-429',
      response_metadata: {
        quota: {
          plan_type: 'team',
          recover_at_ms: now() + 68 * 60 * 1000,
          used_percent: 96,
        },
        errors: {
          kind: 'quota',
          code: 'rate_limit',
        },
        trace: {
          request_id: 'demo-trace-429',
        },
      },
    },
  ],
});

export const getDemoCodexInspectionRuns = (): CodexInspectionRunsResponse => {
  const detail = demoInspectionRunDetail();
  return { items: [detail.run] };
};

export const getDemoCodexInspectionRun = () => clone(demoInspectionRunDetail());

export const getDemoAccountActionCandidates = () => ({
  items: clone(demoAccountCandidates),
  pendingCount: demoAccountCandidates.filter((item) => item.status === 'pending').length,
});

export const getDemoApiKeyAliases = () => ({ items: clone(demoApiAliases) });

export const getDemoLogsResponse = () => {
  const lines = [
    '[INFO] manager server demo started at http://demo.local',
    '[INFO] usage collector consumed 100 events from usage-events',
    '[WARN] codex-fallback-02 reached quota threshold and entered cooldown',
    '[INFO] plugin request-insights rendered embedded resource',
    '[INFO] model price sync completed with 6 models',
  ];
  return {
    lines,
    'line-count': lines.length,
    'latest-timestamp': now(),
    latestAfter: now(),
    nextCursor: '',
    cursorReset: false,
  };
};

export const getDemoErrorLogsResponse = () => ({
  files: [
    {
      name: `request-errors-${formatDemoDate()}.jsonl`,
      size: 18420,
      modified: now() - 18 * 60 * 1000,
    },
    {
      name: `request-errors-${formatDemoDate(now() - day)}.jsonl`,
      size: 9280,
      modified: now() - day,
    },
  ],
});

export const getDemoLatestVersion = () => ({
  latest: 'v7.1.18',
  current: DEMO_SERVER_VERSION,
  buildDate: getDemoServerBuildDate(),
  updateAvailable: false,
});

export const getDemoManagerLatestRelease = () => ({
  tag_name: 'v7.1.18',
  name: 'CPA Manager Plus v7.1.18',
  html_url: 'https://github.com/seakee/CPA-Manager-Plus/releases/tag/v7.1.18',
  published_at: startOfLocalDayIso(),
});

export const getDemoConfigYaml = () =>
  [
    'debug: false',
    'request-log: true',
    'logging-to-file: true',
    'routing:',
    '  strategy: round-robin',
    'plugins:',
    '  enabled: true',
  ].join('\n');

export const getDemoApiCallResult = (payload: DemoApiCallPayload = {}) => {
  const requestUrl = String(payload.url || '');
  let body: unknown = { data: demoProviderModels.map((model) => ({ id: model.name })) };

  if (requestUrl.includes('/wham/usage')) {
    body = {
      user_id: 'demo-user',
      account_id: 'acct_codex_team',
      email: 'platform@example.com',
      plan_type: 'team',
      rate_limit: {
        allowed: true,
        primary_window: {
          used_percent: 0.63,
          limit_window_seconds: 18000,
          reset_after_seconds: 8280,
        },
        secondary_window: {
          used_percent: 0.42,
          limit_window_seconds: 604800,
          reset_after_seconds: 246000,
        },
      },
      code_review_rate_limit: {
        allowed: true,
        primary_window: {
          used_percent: 0.38,
          limit_window_seconds: 18000,
          reset_after_seconds: 7200,
        },
      },
      credits: {
        has_credits: true,
        unlimited: false,
        balance: 18.4,
      },
      rate_limit_reset_credits: {
        available_count: 2,
      },
      subscription_active_until: new Date(now() + 23 * day).toISOString(),
    };
  } else if (requestUrl.includes('/rate-limit-reset-credits')) {
    body = {
      available_count: 2,
      credits: [
        {
          id: 'demo-credit-1',
          status: 'available',
          granted_at: new Date(now() - day).toISOString(),
          expires_at: new Date(now() + 6 * day).toISOString(),
        },
      ],
    };
  } else if (requestUrl.includes('anthropic.com/api/oauth/profile')) {
    body = { email: 'research@example.com', organization_name: 'Research Team' };
  } else if (requestUrl.includes('anthropic.com/api/oauth/usage')) {
    body = {
      plan_type: 'pro',
      usage: {
        five_hour: { utilization: 0.44, resets_at: new Date(now() + 2 * hour).toISOString() },
        seven_day: { utilization: 0.31, resets_at: new Date(now() + 3 * day).toISOString() },
      },
    };
  } else if (requestUrl.includes('api.kimi.com')) {
    body = {
      items: [
        { model: 'kimi-k2', used: 62, total: 100, reset_time: new Date(now() + day).toISOString() },
      ],
    };
  } else if (requestUrl.includes('grok.com')) {
    body = {
      billing: {
        plan: 'pro',
        usage: [{ name: 'requests', used: 320, total: 1000 }],
      },
    };
  } else if (requestUrl.includes('cloudcode-pa.googleapis.com')) {
    body = {
      groups: [
        {
          displayName: 'Gemini 2.5 Flash',
          buckets: [
            {
              bucketId: 'gemini-2.5-flash',
              displayName: 'Gemini 2.5 Flash',
              remainingFraction: 0.72,
              resetTime: new Date(now() + 5 * hour).toISOString(),
            },
          ],
        },
      ],
      paidTier: {
        id: 'g1-pro-tier',
        name: 'Pro',
        availableCredits: [{ creditType: 'monthly', creditAmount: 260, minimumCreditAmountForUsage: 1 }],
      },
    };
  }

  return {
    status_code: 200,
    has_status_code: true,
    header: {
      'content-type': ['application/json'],
      date: [new Date().toUTCString()],
    },
    body,
  };
};
