import { beforeEach, describe, expect, it, vi } from 'vitest';

const { mocks } = vi.hoisted(() => ({
  mocks: {
    get: vi.fn(),
    post: vi.fn(),
    delete: vi.fn(),
  },
}));

vi.mock('./client', () => ({
  apiClient: {
    get: mocks.get,
    post: mocks.post,
    delete: mocks.delete,
  },
  createScopedApiRequestConfig: (scope: { apiBase: string; managementKey: string }) => ({
    baseURL: `${scope.apiBase.replace(/\/+$/, '')}/v0/management`,
    headers: { Authorization: `Bearer ${scope.managementKey}` },
    cpampScopedRequest: true,
  }),
  createScopedV8ApiRequestConfig: (scope: { apiBase: string; managementKey: string }) => ({
    baseURL: `${scope.apiBase.replace(/\/+$/, '')}/v8/management`,
    headers: { Authorization: `Bearer ${scope.managementKey}` },
    cpampScopedRequest: true,
    cpampV8Namespace: true,
  }),
}));

import { captureSiwcBindingApi, oauthApi } from './oauth';

beforeEach(() => {
  mocks.get.mockReset();
  mocks.post.mockReset();
  mocks.delete.mockReset();
});

describe('oauthApi', () => {
  it('marks built-in web UI OAuth starts with is_webui', async () => {
    mocks.get.mockResolvedValue({ url: 'https://auth.example/codex', state: 'state-1' });

    await oauthApi.startAuth('codex');

    expect(mocks.get).toHaveBeenCalledWith('/codex-auth-url', {
      params: { is_webui: true },
    });
  });

  it('starts plugin OAuth providers through their dynamic auth-url endpoint', async () => {
    mocks.get.mockResolvedValue({ url: 'https://auth.example/plugin', state: 'state-2' });

    await oauthApi.startAuth('sample-provider');

    expect(mocks.get).toHaveBeenCalledWith('/sample-provider-auth-url', {
      params: undefined,
    });
  });

  it('pins auth-link, polling, and callback requests to the captured CPA scope', async () => {
    const requestScope = {
      apiBase: 'http://old-cpa.local:8317',
      managementKey: 'old-cpa-key',
    };
    const scopedConfig = {
      baseURL: 'http://old-cpa.local:8317/v0/management',
      headers: { Authorization: 'Bearer old-cpa-key' },
      cpampScopedRequest: true,
    };
    mocks.get
      .mockResolvedValueOnce({ url: 'https://auth.example/codex', state: 'state-1' })
      .mockResolvedValueOnce({ status: 'wait' });
    mocks.post.mockResolvedValue({ status: 'ok' });

    await oauthApi.startAuth('codex', requestScope);
    await oauthApi.getAuthStatus('state-1', requestScope);
    await oauthApi.submitCallback('codex', 'http://localhost/callback?code=1', requestScope);

    expect(mocks.get).toHaveBeenNthCalledWith(1, '/codex-auth-url', {
      ...scopedConfig,
      params: { is_webui: true },
    });
    expect(mocks.get).toHaveBeenNthCalledWith(2, '/get-auth-status', {
      ...scopedConfig,
      params: { state: 'state-1' },
    });
    expect(mocks.post).toHaveBeenCalledWith(
      '/oauth-callback',
      {
        provider: 'codex',
        redirect_url: 'http://localhost/callback?code=1',
      },
      scopedConfig
    );
  });

  it('passes is_webui=true for devin and omits it for kimi-ai and meta', async () => {
    mocks.get.mockResolvedValue({ url: 'https://auth.example/provider', state: 'st' });

    await oauthApi.startAuth('devin');
    await oauthApi.startAuth('kimi-ai');
    await oauthApi.startAuth('meta');

    expect(mocks.get).toHaveBeenNthCalledWith(1, '/devin-auth-url', {
      params: { is_webui: true },
    });
    expect(mocks.get).toHaveBeenNthCalledWith(2, '/kimi-ai-auth-url', {
      params: undefined,
    });
    expect(mocks.get).toHaveBeenNthCalledWith(3, '/meta-auth-url', {
      params: undefined,
    });
  });

  it('forwards AbortSignal and cancels an active OAuth session', async () => {
    const requestScope = {
      apiBase: 'http://old-cpa.local:8317',
      managementKey: 'old-cpa-key',
    };
    const controller = new AbortController();
    mocks.delete.mockResolvedValue({ status: 'ok', cancelled: true });

    await oauthApi.cancelSession('state-9', requestScope, controller.signal);

    expect(mocks.delete).toHaveBeenCalledWith('/oauth-session', {
      baseURL: 'http://old-cpa.local:8317/v0/management',
      headers: { Authorization: 'Bearer old-cpa-key' },
      cpampScopedRequest: true,
      params: { state: 'state-9' },
      signal: controller.signal,
    });
  });

  it('routes v8 SIWC bind, status, callback, and cancel calls through v8 namespace and captured scope', async () => {
    const controller = new AbortController();
    const scope = {
      apiBase: 'http://cpa-v8.local:8317',
      managementKey: 'v8-secret',
    };

    mocks.post.mockResolvedValueOnce({ url: 'https://auth.openai.com/siwc', state: 'st_siwc_1', flow: 'siwc_bind', expires_in: 300 });
    mocks.get.mockResolvedValueOnce({ status: 'wait' });
    mocks.post.mockResolvedValueOnce({ status: 'ok' });
    mocks.delete.mockResolvedValueOnce({ status: 'ok', cancelled: true });

    const scopedApi = captureSiwcBindingApi(scope);
    await scopedApi.start('codex-alice.json', controller.signal);
    await scopedApi.getStatus('st_siwc_1', controller.signal);
    await scopedApi.submitCallback(
      'codex',
      'http://127.0.0.1:1455/auth/callback?state=st_siwc_1&code=abc&client_id=cid',
      controller.signal
    );
    await scopedApi.cancelSession('st_siwc_1', controller.signal);

    const expectedScoped = {
      baseURL: 'http://cpa-v8.local:8317/v8/management',
      headers: { Authorization: 'Bearer v8-secret' },
      cpampScopedRequest: true,
      cpampV8Namespace: true,
      signal: controller.signal,
    };
    expect(mocks.post).toHaveBeenNthCalledWith(
      1,
      '/oauth/siwc/bind',
      { name: 'codex-alice.json' },
      expectedScoped
    );
    expect(mocks.get).toHaveBeenCalledWith('/oauth/status', {
      ...expectedScoped,
      params: { state: 'st_siwc_1' },
    });
    expect(mocks.post).toHaveBeenNthCalledWith(
      2,
      '/oauth/callback',
      {
        provider: 'codex',
        redirect_url: 'http://127.0.0.1:1455/auth/callback?state=st_siwc_1&code=abc&client_id=cid',
      },
      expectedScoped
    );
    expect(mocks.delete).toHaveBeenCalledWith('/oauth/session', {
      ...expectedScoped,
      params: { state: 'st_siwc_1' },
    });

    // Unscoped calls use cpampV8Namespace without scoped headers
    mocks.post.mockResolvedValueOnce({ url: 'https://auth.openai.com/siwc', state: 'st_siwc_2' });
    await oauthApi.startSiwcBind('codex-bob.json');
    expect(mocks.post).toHaveBeenLastCalledWith(
      '/oauth/siwc/bind',
      { name: 'codex-bob.json' },
      { cpampV8Namespace: true }
    );
  });
});
