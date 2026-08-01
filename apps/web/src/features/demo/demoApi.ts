import type { AxiosRequestConfig, AxiosResponse } from 'axios';
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
import { DEMO_API_BASE, DEMO_SERVER_VERSION, getDemoServerBuildDate } from './demoMode';

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

export async function handleDemoApiRequest<T = unknown>(
  method: DemoMethod,
  url: string,
  data?: unknown,
  config?: AxiosRequestConfig
): Promise<T> {
  const { pathname, params } = normalizeDemoUrl(url, config);
  const rawConfig = getDemoRawConfig();

  if (pathname === '/config') return rawConfig as T;
  if (pathname === '/latest-version') return getDemoLatestVersion() as T;
  if (pathname === '/config.yaml')
    return (typeof data === 'string' ? ok : getDemoConfigYaml()) as T;

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
      '/routing/strategy',
    ].includes(pathname)
  ) {
    if (method === 'get') {
      if (pathname === '/logs-max-total-size-mb') return { 'logs-max-total-size-mb': 512 } as T;
      if (pathname === '/force-model-prefix') return { 'force-model-prefix': false } as T;
      if (pathname === '/routing/strategy') return { strategy: 'round-robin' } as T;
    }
    return ok as T;
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
    }
    return ok as T;
  }
  if (pathname === '/auth-files/status') return ok as T;
  if (pathname.startsWith('/auth-files/')) return ok as T;

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
  const { pathname } = normalizeDemoUrl(url, config);
  if (pathname === '/config.yaml') {
    return createAxiosResponse(getDemoConfigYaml(), config, { 'content-type': 'text/yaml' });
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
  if (pathname.startsWith('/auth-files/')) {
    return createAxiosResponse(
      new Blob([JSON.stringify({ demo: true, name: pathname.split('/').pop() }, null, 2)], {
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
  _formData: FormData,
  config?: AxiosRequestConfig
): Promise<T> {
  const { pathname } = normalizeDemoUrl(url, config);
  if (pathname === '/auth-files') {
    return { ...ok, files: getDemoAuthFiles().files, imported: 1, skipped: 0 } as T;
  }
  if (pathname === '/vertex/import') {
    return { imported: 1, skipped: 0, errors: [] } as T;
  }
  return ok as T;
}
