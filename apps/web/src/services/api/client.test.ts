import { beforeEach, describe, expect, it, vi } from 'vitest';

const { mocks, axiosInstance } = vi.hoisted(() => {
  const requestUse = vi.fn();
  const responseUse = vi.fn();
  return {
    mocks: {
      requestUse,
      responseUse,
    },
    axiosInstance: {
      defaults: { timeout: 0 },
      interceptors: {
        request: { use: requestUse },
        response: { use: responseUse },
      },
    },
  };
});

vi.mock('axios', () => ({
  default: {
    create: vi.fn(() => axiosInstance),
    isAxiosError: (error: unknown) =>
      Boolean(error && typeof error === 'object' && 'isAxiosError' in error),
  },
}));

import { apiClient, createScopedApiRequestConfig } from './client';

type RequestConfig = {
  baseURL?: string;
  url?: string;
  headers: Record<string, string>;
  cpampScopedRequest?: true;
};

const applyRequestInterceptor = (config: RequestConfig): RequestConfig => {
  const interceptor = mocks.requestUse.mock.calls[0]?.[0] as
    | ((request: RequestConfig) => RequestConfig)
    | undefined;
  if (!interceptor) throw new Error('request interceptor was not registered');
  return interceptor(config);
};

const applyResponseErrorInterceptor = async (error: unknown): Promise<void> => {
  const interceptor = mocks.responseUse.mock.calls[0]?.[1] as
    | ((reason: unknown) => Promise<never>)
    | undefined;
  if (!interceptor) throw new Error('response interceptor was not registered');
  await interceptor(error);
};

const applyResponseSuccessInterceptor = (response: unknown): unknown => {
  const interceptor = mocks.responseUse.mock.calls[0]?.[0] as
    | ((value: unknown) => unknown)
    | undefined;
  if (!interceptor) throw new Error('response interceptor was not registered');
  return interceptor(response);
};

describe('ApiClient request scoping', () => {
  const dispatchEvent = vi.fn();

  beforeEach(() => {
    dispatchEvent.mockReset();
    vi.stubGlobal('window', { dispatchEvent });
    apiClient.setConfig({
      apiBase: 'http://new-cpa.local:8317',
      managementKey: 'new-cpa-key',
    });
  });

  it('preserves an explicit old connection while regular requests use the current connection', () => {
    const scoped = createScopedApiRequestConfig({
      apiBase: 'http://old-cpa.local:8317',
      managementKey: 'old-cpa-key',
    }) as RequestConfig;
    scoped.url = '/oauth-model-alias';

    expect(applyRequestInterceptor(scoped)).toMatchObject({
      baseURL: 'http://old-cpa.local:8317/v0/management',
      headers: { Authorization: 'Bearer old-cpa-key' },
    });
    expect(applyRequestInterceptor({ url: '/oauth-model-alias', headers: {} })).toMatchObject({
      baseURL: 'http://new-cpa.local:8317/v0/management',
      headers: { Authorization: 'Bearer new-cpa-key' },
    });
  });

  it('blocks absolute CPAMP targets before adding the Panel CPA key', () => {
    const request = { url: 'https://manager.example/control/api/v2/status', headers: {} };
    expect(() => applyRequestInterceptor(request)).toThrow('CPA Management target');
    expect(request.headers).not.toHaveProperty('Authorization');
    expect(() => applyRequestInterceptor({ url: '/api/v2/status', headers: {} })).toThrow(
      'CPA Management target'
    );
  });

  it('does not let a stale scoped 401 log out the current connection', async () => {
    const staleConfig = applyRequestInterceptor({
      ...(createScopedApiRequestConfig({
        apiBase: 'http://old-cpa.local:8317',
        managementKey: 'old-cpa-key',
      }) as RequestConfig),
      url: '/oauth-model-alias',
    });
    const currentConfig = applyRequestInterceptor({
      url: '/oauth-model-alias',
      headers: {},
    });
    const createUnauthorizedError = (config: RequestConfig) => ({
      isAxiosError: true,
      message: 'unauthorized',
      config,
      response: { status: 401, data: { message: 'unauthorized' } },
    });

    await expect(
      applyResponseErrorInterceptor(createUnauthorizedError(staleConfig))
    ).rejects.toThrow('unauthorized');
    expect(dispatchEvent).not.toHaveBeenCalled();

    await expect(
      applyResponseErrorInterceptor(createUnauthorizedError(currentConfig))
    ).rejects.toThrow('unauthorized');
    expect(dispatchEvent).toHaveBeenCalledTimes(1);
  });

  it('does not publish server metadata from a stale scoped response', () => {
    const staleConfig = applyRequestInterceptor({
      ...(createScopedApiRequestConfig({
        apiBase: 'http://old-cpa.local:8317',
        managementKey: 'old-cpa-key',
      }) as RequestConfig),
      url: '/oauth-model-alias',
    });
    const currentConfig = applyRequestInterceptor({
      url: '/oauth-model-alias',
      headers: {},
    });
    const createResponse = (config: RequestConfig) => ({
      config,
      headers: {
        'x-cpa-version': 'v1.2.3',
        'x-cpa-commit': '5bffd1514fba2ca7cbfd13bb6530a6f7d9d72d43',
        'x-cpa-support-plugin': 'true',
      },
    });

    applyResponseSuccessInterceptor(createResponse(staleConfig));
    expect(dispatchEvent).not.toHaveBeenCalled();

    applyResponseSuccessInterceptor(createResponse(currentConfig));
    expect(dispatchEvent).toHaveBeenCalledTimes(2);
    expect((dispatchEvent.mock.calls[0]?.[0] as CustomEvent).detail).toEqual({
      version: 'v1.2.3',
      commit: '5bffd1514fba2ca7cbfd13bb6530a6f7d9d72d43',
      buildDate: null,
    });
  });

  it('publishes a commit even when a development build omits version metadata', () => {
    const currentConfig = applyRequestInterceptor({
      url: '/config',
      headers: {},
    });

    applyResponseSuccessInterceptor({
      config: currentConfig,
      headers: { 'x-cpa-commit': '5bffd151' },
    });

    expect(dispatchEvent).toHaveBeenCalledTimes(1);
    expect((dispatchEvent.mock.calls[0]?.[0] as CustomEvent).detail).toEqual({
      version: null,
      commit: '5bffd151',
      buildDate: null,
    });
  });

  it('surfaces detailed response message when CPA returns machine error codes alongside message', async () => {
    const currentConfig = applyRequestInterceptor({
      url: '/config',
      headers: {},
    });

    await expect(
      applyResponseErrorInterceptor({
        isAxiosError: true,
        message: 'Request failed with status code 400',
        config: currentConfig,
        response: {
          status: 400,
          data: {
            error: 'invalid_config',
            message: 'yaml: unmarshal errors:\n  line 4: field priority not found in type config.ClaudeModel',
          },
        },
      })
    ).rejects.toMatchObject({
      status: 400,
      message: 'yaml: unmarshal errors:\n  line 4: field priority not found in type config.ClaudeModel',
      details: {
        error: 'invalid_config',
        message: 'yaml: unmarshal errors:\n  line 4: field priority not found in type config.ClaudeModel',
      },
    });

    await expect(
      applyResponseErrorInterceptor({
        isAxiosError: true,
        message: 'Request failed with status code 400',
        config: currentConfig,
        response: {
          status: 400,
          data: {
            error: 'Validation failed',
            message: 'base-url is required',
          },
        },
      })
    ).rejects.toMatchObject({
      status: 400,
      message: 'Validation failed: base-url is required',
    });
  });

  it('retains response headers and normalized etag on ApiError from Axios responses', async () => {
    const currentConfig = applyRequestInterceptor({
      url: '/config/api-keys/claude',
      headers: {},
    });
    const responseHeaders = {
      ETag: '  "sha256-etag-99"  ',
      'content-type': 'application/json',
    };

    await expect(
      applyResponseErrorInterceptor({
        isAxiosError: true,
        message: 'Request failed with status code 412',
        config: currentConfig,
        response: {
          status: 412,
          headers: responseHeaders,
          data: { error: 'precondition_failed' },
        },
      })
    ).rejects.toMatchObject({
      name: 'ApiError',
      status: 412,
      etag: '"sha256-etag-99"',
      headers: responseHeaders,
    });
  });

  it('strips weak W/ and w/ ETag prefixes when populating ApiError.etag', async () => {
    const currentConfig = applyRequestInterceptor({
      url: '/config/api-keys/claude',
      headers: {},
    });

    await expect(
      applyResponseErrorInterceptor({
        isAxiosError: true,
        message: 'Request failed with status code 412',
        config: currentConfig,
        response: {
          status: 412,
          headers: { ETag: '  W/"sha256-weak-upper"  ' },
          data: { error: 'precondition_failed' },
        },
      })
    ).rejects.toMatchObject({
      name: 'ApiError',
      status: 412,
      etag: '"sha256-weak-upper"',
    });

    await expect(
      applyResponseErrorInterceptor({
        isAxiosError: true,
        message: 'Request failed with status code 412',
        config: currentConfig,
        response: {
          status: 412,
          headers: { etag: 'w/"sha256-weak-lower"' },
          data: { error: 'precondition_failed' },
        },
      })
    ).rejects.toMatchObject({
      name: 'ApiError',
      status: 412,
      etag: '"sha256-weak-lower"',
    });

    await expect(
      applyResponseErrorInterceptor({
        isAxiosError: true,
        message: 'Request failed with status code 412',
        config: currentConfig,
        response: {
          status: 412,
          headers: { etag: 'W/   ' },
          data: { error: 'precondition_failed' },
        },
      })
    ).rejects.toMatchObject({
      name: 'ApiError',
      status: 412,
      etag: undefined,
    });
  });

  it('increments connectionEpoch only when apiBase or managementKey changes', () => {
    const initialEpoch = apiClient.getConnectionEpoch();

    apiClient.setConfig({
      apiBase: 'http://new-cpa.local:8317',
      managementKey: 'new-cpa-key',
      timeout: 15_000,
    });
    expect(apiClient.getConnectionEpoch()).toBe(initialEpoch);

    apiClient.setConfig({
      apiBase: 'http://new-cpa.local:8317',
      managementKey: 'rotated-cpa-key',
    });
    expect(apiClient.getConnectionEpoch()).toBe(initialEpoch + 1);

    apiClient.setConfig({
      apiBase: 'http://other-cpa.local:8317',
      managementKey: 'rotated-cpa-key',
    });
    expect(apiClient.getConnectionEpoch()).toBe(initialEpoch + 2);
  });

  it('snapshots current v0 and v8 connection scopes and preserves them across later setConfig changes', async () => {
    apiClient.setConfig({
      apiBase: 'http://origin-a.local:8317',
      managementKey: 'key-origin-a',
    });

    const scopedV0 = apiClient.createCurrentScopedConfig();
    const scopedV8 = apiClient.createCurrentScopedV8Config();

    apiClient.setConfig({
      apiBase: 'http://origin-b.local:8317',
      managementKey: 'key-origin-b',
    });

    const preparedV0 = await applyRequestInterceptor({
      url: '/claude-api-key',
      ...scopedV0,
      headers: { ...(scopedV0.headers as Record<string, string>) },
    });
    expect(preparedV0.baseURL).toBe('http://origin-a.local:8317/v0/management');
    expect(preparedV0.headers.Authorization).toBe('Bearer key-origin-a');

    const preparedV8 = await applyRequestInterceptor({
      url: '/config/api-keys/claude',
      ...scopedV8,
      headers: {
        ...(scopedV8.headers as Record<string, string>),
        'If-Match': '"sha256-a"',
      },
    });
    expect(preparedV8.baseURL).toBe('http://origin-a.local:8317/v8/management');
    expect(preparedV8.headers.Authorization).toBe('Bearer key-origin-a');
    expect(preparedV8.headers['If-Match']).toBe('"sha256-a"');
  });
});
