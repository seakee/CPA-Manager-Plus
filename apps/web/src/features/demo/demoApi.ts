import type { AxiosRequestConfig, AxiosResponse } from 'axios';
import { parse as parseYaml, parseDocument } from 'yaml';
import {
  advanceDemoCredentialRefresh,
  getDemoApiCallResult,
  getDemoAuthFiles,
  getDemoConfigYaml,
  getDemoErrorLogsResponse,
  getDemoLatestVersion,
  getDemoLogsResponse,
  getDemoPluginStore,
  getDemoPlugins,
  getDemoRawConfig,
  requestDemoCredentialRefresh,
} from '@/features/demo/demoFixtures';
import type { AuthFileItem } from '@/types';
import {
  DEMO_API_BASE,
  DEMO_SERVER_COMMIT,
  DEMO_SERVER_VERSION,
  getDemoServerBuildDate,
} from './demoMode';

type DemoMethod = 'get' | 'post' | 'put' | 'patch' | 'delete';

const ok = { status: 'ok', success: true };
const FORCE_REFRESH_TIMESTAMP = '2000-01-01T00:00:00Z';

const isCredentialRefreshPatch = (data: unknown): data is Record<string, unknown> =>
  Boolean(
    data &&
    typeof data === 'object' &&
    (data as Record<string, unknown>).expired === FORCE_REFRESH_TIMESTAMP &&
    (data as Record<string, unknown>).last_refresh === FORCE_REFRESH_TIMESTAMP
  );

const normalizeDemoUrl = (url: string, config?: AxiosRequestConfig) => {
  const parsed = new URL(url || '/', DEMO_API_BASE);
  const params = new URLSearchParams(parsed.search);
  const configParams = config?.params;
  if (configParams && typeof configParams === 'object') {
    Object.entries(configParams as Record<string, unknown>).forEach(([key, value]) => {
      if (value === undefined || value === null) return;
      params.set(key, String(value));
    });
  }
  return {
    pathname: parsed.pathname.replace(/\/generative-language-api-key\b/g, '/gemini-api-key'),
    params,
  };
};

const createAxiosResponse = <T>(
  data: T,
  config?: AxiosRequestConfig,
  headers: Record<string, string> = {}
): AxiosResponse<T> =>
  ({
    data,
    status: 200,
    statusText: 'OK',
    headers: {
      'x-cpa-version': DEMO_SERVER_VERSION,
      'x-cpa-commit': DEMO_SERVER_COMMIT,
      'x-cpa-build-date': getDemoServerBuildDate(),
      'x-cpa-support-plugin': 'true',
      ...headers,
    },
    config: config || {},
    request: {},
  }) as AxiosResponse<T>;

const providerEndpointKeys: Record<string, string> = {
  '/api-keys': 'api-keys',
  '/gemini-api-key': 'gemini-api-key',
  '/codex-api-key': 'codex-api-key',
  '/xai-api-key': 'xai-api-key',
  '/claude-api-key': 'claude-api-key',
  '/vertex-api-key': 'vertex-api-key',
  '/openai-compatibility': 'openai-compatibility',
};

type DemoAuthFileModel = {
  id: string;
  display_name?: string;
  type?: string;
  owned_by?: string;
};

const DEMO_AUTH_FILE_MODELS: Record<string, DemoAuthFileModel[]> = {
  codex: [
    { id: 'gpt-5-codex', display_name: 'GPT-5 Codex', type: 'responses', owned_by: 'openai' },
    { id: 'gpt-5', display_name: 'GPT-5', type: 'responses', owned_by: 'openai' },
    { id: 'o1-preview', display_name: 'o1 Preview', type: 'responses', owned_by: 'openai' },
  ],
  openai: [
    { id: 'gpt-4.1', display_name: 'GPT-4.1', type: 'chat', owned_by: 'openai' },
    { id: 'gpt-4.1-mini', display_name: 'GPT-4.1 Mini', type: 'chat', owned_by: 'openai' },
  ],
  claude: [
    {
      id: 'claude-sonnet-4-5-20250929',
      display_name: 'Claude Sonnet 4.5',
      type: 'messages',
      owned_by: 'anthropic',
    },
    {
      id: 'claude-opus-4-1-20250805',
      display_name: 'Claude Opus 4.1',
      type: 'messages',
      owned_by: 'anthropic',
    },
    {
      id: 'claude-opus-legacy',
      display_name: 'Claude Opus Legacy',
      type: 'messages',
      owned_by: 'anthropic',
    },
  ],
  xai: [
    { id: 'grok-4.5', display_name: 'Grok 4.5', type: 'responses', owned_by: 'xai' },
    { id: 'grok-4-fast', display_name: 'Grok 4 Fast', type: 'responses', owned_by: 'xai' },
    {
      id: 'grok-code-fast-1',
      display_name: 'Grok Code Fast 1',
      type: 'responses',
      owned_by: 'xai',
    },
  ],
  gemini: [
    {
      id: 'gemini-2.5-pro',
      display_name: 'Gemini 2.5 Pro',
      type: 'generateContent',
      owned_by: 'google',
    },
    {
      id: 'gemini-2.5-flash',
      display_name: 'Gemini 2.5 Flash',
      type: 'generateContent',
      owned_by: 'google',
    },
  ],
  antigravity: [
    {
      id: 'gemini-2.5-pro',
      display_name: 'Gemini 2.5 Pro',
      type: 'generateContent',
      owned_by: 'google',
    },
    {
      id: 'claude-sonnet-4-5',
      display_name: 'Claude Sonnet 4.5',
      type: 'messages',
      owned_by: 'anthropic',
    },
  ],
  kimi: [
    { id: 'kimi-k2', display_name: 'Kimi K2', type: 'chat', owned_by: 'moonshot' },
    {
      id: 'kimi-k2-thinking',
      display_name: 'Kimi K2 Thinking',
      type: 'chat',
      owned_by: 'moonshot',
    },
  ],
};

const demoAuthFileConfigurationOverrides = new Map<string, Record<string, unknown>>();
let demoConfigYamlOverride: string | null = null;

const normalizeDemoProvider = (value: unknown): string => {
  const provider = String(value ?? '')
    .trim()
    .toLowerCase()
    .replace(/_/g, '-');
  if (provider === 'x-ai' || provider === 'grok') return 'xai';
  if (provider === 'gemini-cli') return 'gemini';
  return provider;
};

const readDemoString = (...values: unknown[]): string => {
  for (const value of values) {
    if (typeof value !== 'string' && typeof value !== 'number') continue;
    const text = String(value).trim();
    if (text) return text;
  }
  return '';
};

const readDemoAuthIndex = (file: AuthFileItem): string =>
  readDemoString(file.authIndex, file['auth_index'], file['auth-index']);

const getDemoAuthFileConfigurationKey = (file: AuthFileItem): string =>
  `${readDemoString(file.id, file.name)}\u0000${readDemoAuthIndex(file)}`;

const isDemoConfigurationRecord = (value: unknown): value is Record<string, unknown> =>
  Boolean(value) && typeof value === 'object' && !Array.isArray(value);

const getCurrentDemoConfigYaml = () => demoConfigYamlOverride ?? getDemoConfigYaml();

const getCurrentDemoRawConfig = (): Record<string, unknown> => {
  const rawConfig = getDemoRawConfig();
  try {
    const parsed = parseYaml(getCurrentDemoConfigYaml());
    return isDemoConfigurationRecord(parsed) ? { ...rawConfig, ...parsed } : rawConfig;
  } catch {
    return rawConfig;
  }
};

const getCurrentDemoRoutingStrategy = (): string => {
  const routing = getCurrentDemoRawConfig().routing;
  if (!isDemoConfigurationRecord(routing)) return 'round-robin';
  return typeof routing.strategy === 'string' ? routing.strategy : 'round-robin';
};

const updateDemoRoutingStrategy = (strategy: string) => {
  const document = parseDocument(getCurrentDemoConfigYaml());
  if (document.errors.length > 0) return;
  document.setIn(['routing', 'strategy'], strategy);
  demoConfigYamlOverride = document.toString().trimEnd();
};

const readDemoConfigurationRecordAuthIndex = (
  record: Record<string, unknown>,
  arrayIndex?: number
): string =>
  readDemoString(
    record.authIndex,
    record.auth_index,
    record['auth-index'],
    arrayIndex === undefined ? '' : arrayIndex
  );

const findDemoAuthFile = (selector: string): AuthFileItem | null => {
  const normalized = selector.trim();
  if (!normalized) return null;
  return (
    getDemoAuthFiles().files.find((file) =>
      [file.id, file.name, file.authIndex, file['auth_index'], file['auth-index']].some(
        (value) => String(value ?? '').trim() === normalized
      )
    ) ?? null
  );
};

const getDemoModelsForProvider = (provider: string): DemoAuthFileModel[] => {
  const normalized = normalizeDemoProvider(provider);
  return (DEMO_AUTH_FILE_MODELS[normalized] ?? DEMO_AUTH_FILE_MODELS.openai).map((model) => ({
    ...model,
  }));
};

const buildDemoAuthFileConfiguration = (file: AuthFileItem): Record<string, unknown> => {
  const provider = normalizeDemoProvider(file.type ?? file.provider);
  const rawProvider = readDemoString(file.type, file.provider) || provider || 'unknown';
  const authIndex = readDemoAuthIndex(file);
  const account = readDemoString(file.account, file.email, file.account_snapshot, file.label);
  const accountId = readDemoString(file.account_id, file.accountId);
  const projectId = readDemoString(file.project_id, file.projectId);
  const excludedModels =
    provider === 'codex' ? ['o1-preview'] : provider === 'claude' ? ['claude-opus-legacy'] : [];
  const record: Record<string, unknown> = {
    type: rawProvider,
    ...(authIndex ? { auth_index: authIndex } : {}),
    ...(account ? { account } : {}),
    ...(accountId ? { account_id: accountId } : {}),
    ...(projectId ? { project_id: projectId } : {}),
    prefix: `demo-${provider || 'oauth'}`,
    priority: typeof file.priority === 'number' ? file.priority : 50,
    weight: 100,
    note: `Fictional ${provider || 'OAuth'} credential used by the CPAMP demo`,
    'excluded-models': excludedModels,
    disable_cooling: false,
    request_retry: 2,
    headers: {
      Authorization: 'Bearer fictional-demo-header-token',
      'X-Demo-Tenant': 'fictional-team',
    },
  };

  if (provider === 'codex') {
    Object.assign(record, {
      websockets: true,
      access_token: 'fictional-demo-codex-access-token',
      refresh_token: 'fictional-demo-codex-refresh-token',
      id_token: 'fictional-demo-codex-id-token',
    });
  } else if (provider === 'xai') {
    const usingApi = file.name.includes('payg');
    Object.assign(record, {
      websockets: true,
      using_api: usingApi,
      base_url: usingApi ? 'https://api.x.ai/v1' : 'https://grok-demo-gateway.invalid/v1',
      access_token: 'fictional-demo-xai-access-token',
      refresh_token: 'fictional-demo-xai-refresh-token',
      id_token: 'fictional-demo-xai-id-token',
    });
  } else if (provider === 'claude') {
    Object.assign(record, {
      cloak_mode: 'auto',
      cloak_strict_mode: 'true',
      cloak_sensitive_words: 'internal,confidential',
      cloak_cache_user_id: 'true',
      tool_prefix_disabled: false,
      access_token: 'fictional-demo-claude-access-token',
      refresh_token: 'fictional-demo-claude-refresh-token',
    });
  } else {
    record.access_token = `fictional-demo-${provider || 'oauth'}-access-token`;
  }

  return demoAuthFileConfigurationOverrides.get(getDemoAuthFileConfigurationKey(file)) ?? record;
};

const applyDemoAuthFileConfigurationPatch = (
  record: Record<string, unknown>,
  fields: Record<string, unknown>
): Record<string, unknown> => {
  const next = { ...record };
  const handledKeys = new Set(['name']);

  const applyTrimmedString = (key: string) => {
    handledKeys.add(key);
    const value = fields[key];
    if (value === undefined) return;
    if (typeof value !== 'string') {
      next[key] = value;
      return;
    }
    const normalized = value.trim();
    if (normalized) next[key] = normalized;
    else delete next[key];
  };

  [
    'expired',
    'last_refresh',
    'prefix',
    'proxy_url',
    'base_url',
    'note',
    'cloak_mode',
    'cloak_strict_mode',
    'cloak_sensitive_words',
    'cloak_cache_user_id',
  ].forEach(applyTrimmedString);

  handledKeys.add('priority');
  if (fields.priority !== undefined) {
    if (fields.priority === 0) delete next.priority;
    else next.priority = fields.priority;
  }

  handledKeys.add('weight');
  if (fields.weight !== undefined) {
    if (fields.weight === null) delete next.weight;
    else next.weight = fields.weight;
  }

  handledKeys.add('excluded-models');
  handledKeys.add('excluded_models');
  const excludedModelsPatch =
    fields['excluded-models'] !== undefined ? fields['excluded-models'] : fields.excluded_models;
  if (excludedModelsPatch !== undefined) {
    if (excludedModelsPatch === null) {
      delete next['excluded-models'];
      delete next.excluded_models;
    } else if (Array.isArray(excludedModelsPatch)) {
      const models = excludedModelsPatch.map((model) => String(model).trim()).filter(Boolean);
      if (models.length > 0) next['excluded-models'] = models;
      else delete next['excluded-models'];
      delete next.excluded_models;
    }
  }

  handledKeys.add('disable_cooling');
  if (fields.disable_cooling !== undefined) {
    if (fields.disable_cooling === null) delete next.disable_cooling;
    else next.disable_cooling = fields.disable_cooling;
    delete next.disableCooling;
    delete next['disable-cooling'];
  }

  handledKeys.add('request_retry');
  if (fields.request_retry !== undefined) {
    if (fields.request_retry === null) delete next.request_retry;
    else next.request_retry = fields.request_retry;
  }

  handledKeys.add('tool_prefix_disabled');
  if (fields.tool_prefix_disabled !== undefined) {
    if (fields.tool_prefix_disabled) next.tool_prefix_disabled = true;
    else delete next.tool_prefix_disabled;
  }

  handledKeys.add('headers');
  if (
    fields.headers !== undefined &&
    fields.headers &&
    typeof fields.headers === 'object' &&
    !Array.isArray(fields.headers)
  ) {
    const currentHeaders =
      next.headers && typeof next.headers === 'object' && !Array.isArray(next.headers)
        ? { ...(next.headers as Record<string, unknown>) }
        : {};
    Object.entries(fields.headers as Record<string, unknown>).forEach(
      ([headerName, headerValue]) => {
        const normalizedName = headerName.trim();
        if (!normalizedName) return;
        const normalizedValue = String(headerValue ?? '').trim();
        if (normalizedValue) currentHeaders[normalizedName] = normalizedValue;
        else delete currentHeaders[normalizedName];
      }
    );
    if (Object.keys(currentHeaders).length > 0) next.headers = currentHeaders;
    else delete next.headers;
  }

  handledKeys.add('websockets');
  if (fields.websockets !== undefined) {
    delete next.websocket;
    next.websockets = fields.websockets;
  }

  handledKeys.add('using_api');
  if (fields.using_api !== undefined) next.using_api = fields.using_api;

  Object.entries(fields).forEach(([key, value]) => {
    if (handledKeys.has(key)) return;
    if (value === null) delete next[key];
    else next[key] = value;
  });
  return next;
};

type DemoAuthFileUploadResult = {
  name: string;
  error: string;
};

const persistDemoAuthFileConfigurationUpload = async (
  formData: FormData
): Promise<DemoAuthFileUploadResult> => {
  const uploaded = formData.get('file');
  if (!(uploaded instanceof Blob)) return { name: '', error: 'Missing auth file' };

  const uploadedName =
    'name' in uploaded && typeof uploaded.name === 'string' ? uploaded.name.trim() : '';
  if (!uploadedName) return { name: '', error: 'Missing auth file name' };

  let parsed: unknown;
  try {
    parsed = JSON.parse(await uploaded.text()) as unknown;
  } catch {
    return { name: uploadedName, error: 'Invalid auth file JSON' };
  }

  if (Array.isArray(parsed) && !parsed.every(isDemoConfigurationRecord)) {
    return { name: uploadedName, error: 'Auth file must contain a JSON object or object array' };
  }

  const records = Array.isArray(parsed)
    ? parsed
    : isDemoConfigurationRecord(parsed)
      ? [parsed]
      : [];
  if (records.length === 0) {
    return { name: uploadedName, error: 'Auth file must contain a JSON object or object array' };
  }

  const candidates = getDemoAuthFiles().files.filter(
    (file) => file.name.trim() === uploadedName || readDemoString(file.id) === uploadedName
  );
  candidates.forEach((file) => {
    const expectedAuthIndex = readDemoAuthIndex(file);
    const record =
      records.length === 1
        ? records[0]
        : records.find(
            (candidate, index) =>
              readDemoConfigurationRecordAuthIndex(candidate, index) === expectedAuthIndex
          );
    if (!record) return;
    demoAuthFileConfigurationOverrides.set(getDemoAuthFileConfigurationKey(file), { ...record });
  });

  return { name: uploadedName, error: '' };
};

export const resetDemoAuthFileConfiguration = () => {
  demoAuthFileConfigurationOverrides.clear();
};

export const resetDemoConfigState = () => {
  demoConfigYamlOverride = null;
};

export async function handleDemoApiRequest<T = unknown>(
  method: DemoMethod,
  url: string,
  data?: unknown,
  config?: AxiosRequestConfig
): Promise<T> {
  const { pathname, params } = normalizeDemoUrl(url, config);
  const rawConfig = getCurrentDemoRawConfig();

  if (pathname === '/config') return rawConfig as T;
  if (pathname === '/latest-version') return getDemoLatestVersion() as T;
  if (pathname === '/config.yaml') {
    if (method === 'put' && typeof data === 'string') {
      demoConfigYamlOverride = data;
      return ok as T;
    }
    return getCurrentDemoConfigYaml() as T;
  }

  const providerKey = providerEndpointKeys[pathname];
  if (providerKey) {
    if (method === 'get') return rawConfig[providerKey] as T;
    return ok as T;
  }

  if (
    [
      '/debug',
      '/proxy-url',
      '/request-retry',
      '/quota-exceeded/switch-project',
      '/quota-exceeded/switch-preview-model',
      '/request-log',
      '/logging-to-file',
      '/logs-max-total-size-mb',
      '/ws-auth',
      '/force-model-prefix',
    ].includes(pathname)
  ) {
    if (method === 'get') {
      if (pathname === '/logs-max-total-size-mb') return { 'logs-max-total-size-mb': 512 } as T;
      if (pathname === '/force-model-prefix') return { 'force-model-prefix': false } as T;
    }
    return ok as T;
  }

  if (pathname === '/routing/strategy') {
    if (method === 'get') return { strategy: getCurrentDemoRoutingStrategy() } as T;
    if (method === 'put' && isDemoConfigurationRecord(data) && typeof data.value === 'string') {
      updateDemoRoutingStrategy(data.value);
    }
    return ok as T;
  }

  if (pathname === '/reset-quota') {
    const authIndex =
      typeof data === 'object' && data !== null && 'auth_index' in data
        ? String((data as { auth_index?: unknown }).auth_index ?? '')
        : '';
    return { status: 'ok', auth_index: authIndex, models: [] } as T;
  }

  if (pathname === '/auth-files') {
    if (method === 'get') {
      advanceDemoCredentialRefresh();
      return getDemoAuthFiles() as T;
    }
    if (method === 'delete') return { deleted: params.get('all') === 'true' ? 8 : 1 } as T;
    return { ...ok, files: getDemoAuthFiles().files } as T;
  }
  if (pathname === '/auth-files/fields') {
    if (method === 'patch' && isCredentialRefreshPatch(data)) {
      const selector = typeof data.name === 'string' ? data.name : '';
      requestDemoCredentialRefresh(selector);
    } else if (method === 'patch' && data && typeof data === 'object' && !Array.isArray(data)) {
      const fields = data as Record<string, unknown>;
      const selector = typeof fields.name === 'string' ? fields.name : '';
      const file = findDemoAuthFile(selector);
      if (file) {
        const next = applyDemoAuthFileConfigurationPatch(
          buildDemoAuthFileConfiguration(file),
          fields
        );
        demoAuthFileConfigurationOverrides.set(getDemoAuthFileConfigurationKey(file), next);
      }
    }
    return ok as T;
  }
  if (pathname === '/auth-files/status') return ok as T;
  if (pathname === '/auth-files/models') {
    const file = findDemoAuthFile(params.get('name') ?? '');
    return {
      models: file ? getDemoModelsForProvider(String(file.type ?? file.provider ?? '')) : [],
    } as T;
  }
  if (pathname.startsWith('/auth-files/')) return ok as T;

  if (pathname.startsWith('/model-definitions/')) {
    const provider = decodeURIComponent(pathname.slice('/model-definitions/'.length));
    return { models: getDemoModelsForProvider(provider) } as T;
  }

  if (pathname === '/oauth-excluded-models') {
    if (method === 'get') return rawConfig['oauth-excluded-models'] as T;
    return ok as T;
  }
  if (pathname === '/oauth-model-alias') {
    if (method === 'get') {
      return {
        'oauth-model-alias': {
          codex: [
            { name: 'gpt-5-codex', alias: 'team-codex', fork: true },
            { name: 'gpt-5', alias: 'g5', fork: true },
          ],
          claude: [
            { name: 'claude-sonnet-4-5-20250929', alias: 'claude-sonnet-4-5', fork: true },
            { name: 'claude-opus-4-1-20250805', alias: 'claude-opus-4-1', fork: true },
          ],
        },
      } as T;
    }
    return ok as T;
  }

  if (pathname === '/logs') {
    return (method === 'delete' ? ok : getDemoLogsResponse()) as T;
  }
  if (pathname === '/request-error-logs') return getDemoErrorLogsResponse() as T;

  if (pathname === '/plugins') return getDemoPlugins() as T;
  if (/^\/plugins\/[^/]+\/enabled$/.test(pathname)) return ok as T;
  if (/^\/plugins\/[^/]+\/config$/.test(pathname)) {
    if (method === 'get') return { sampleWindow: 30, enabled: true } as T;
    return ok as T;
  }
  if (/^\/plugins\/[^/]+$/.test(pathname)) return ok as T;

  if (pathname === '/plugin-store') return getDemoPluginStore() as T;
  if (/^\/plugin-store\/[^/]+\/install$/.test(pathname)) {
    const requestedVersion =
      params.get('version') ||
      (data && typeof data === 'object' && 'version' in data
        ? String((data as { version?: unknown }).version ?? '')
        : ''
      ).trim();
    return {
      status: 'installed',
      source_id: params.get('source') || 'official',
      source_name: 'official',
      source_url: 'https://plugins.example.com/index.json',
      id: decodeURIComponent(pathname.split('/')[2] || ''),
      version: requestedVersion || '1.0.0',
      install_type: 'github-release',
      path: `plugins/${decodeURIComponent(pathname.split('/')[2] || '')}`,
      plugins_enabled: true,
      restart_required: false,
    } as T;
  }

  if (pathname === '/api-call') return getDemoApiCallResult(data as never) as T;
  if (pathname === '/api-key-usage') {
    return {
      items: [
        { apiKeyHash: 'hash_openai_primary', count: 4200, success: 4168, failed: 32 },
        { apiKeyHash: 'hash_codex_team', count: 5200, success: 5120, failed: 80 },
      ],
    } as T;
  }

  if (pathname.endsWith('-auth-url')) {
    return { url: '#/demo/oauth?provider=demo', state: 'demo-oauth-state' } as T;
  }
  if (pathname === '/get-auth-status') return { status: 'ok' } as T;
  if (pathname === '/oauth-callback') return { status: 'ok', message: 'demo' } as T;
  if (pathname === '/vertex/import') return { imported: 1, skipped: 0, items: [] } as T;

  return ok as T;
}

export async function handleDemoRawRequest(
  url: string,
  config?: AxiosRequestConfig
): Promise<AxiosResponse> {
  const { pathname, params } = normalizeDemoUrl(url, config);
  if (pathname === '/config.yaml') {
    return createAxiosResponse(getCurrentDemoConfigYaml(), config, {
      'content-type': 'text/yaml',
    });
  }
  if (pathname.startsWith('/request-error-logs/')) {
    return createAxiosResponse(
      new Blob(['{"level":"error","message":"Demo upstream quota event"}\n'], {
        type: 'application/jsonl',
      }),
      config,
      { 'content-type': 'application/jsonl' }
    );
  }
  if (pathname.startsWith('/request-log-by-id/')) {
    return createAxiosResponse(
      new Blob([JSON.stringify({ id: pathname.split('/').pop(), demo: true }, null, 2)], {
        type: 'application/json',
      }),
      config,
      { 'content-type': 'application/json' }
    );
  }
  if (pathname === '/auth-files/download') {
    const file = findDemoAuthFile(params.get('name') ?? '');
    const record = file ? buildDemoAuthFileConfiguration(file) : {};
    return createAxiosResponse(
      new Blob([JSON.stringify(record, null, 2)], {
        type: 'application/json',
      }),
      config,
      { 'content-type': 'application/json' }
    );
  }
  return createAxiosResponse({}, config);
}

export async function handleDemoFormRequest<T = unknown>(
  url: string,
  formData: FormData,
  config?: AxiosRequestConfig
): Promise<T> {
  const { pathname } = normalizeDemoUrl(url, config);
  if (pathname === '/auth-files') {
    const upload = await persistDemoAuthFileConfigurationUpload(formData);
    const succeeded = Boolean(upload.name) && !upload.error;
    return {
      ...ok,
      status: succeeded ? 'ok' : 'error',
      success: succeeded,
      uploaded: succeeded ? 1 : 0,
      files: succeeded ? [upload.name] : [],
      imported: succeeded ? 1 : 0,
      skipped: succeeded ? 0 : 1,
      failed: succeeded ? [] : [{ name: upload.name, error: upload.error }],
    } as T;
  }
  if (pathname === '/vertex/import') {
    return { imported: 1, skipped: 0, errors: [] } as T;
  }
  return ok as T;
}
