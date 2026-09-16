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
}));

import { oauthApi } from './oauth';

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

  it('starts Devin OAuth with is_webui flag', async () => {
    mocks.get.mockResolvedValue({ url: 'https://auth.example/devin', state: 'state-devin-1' });

    await oauthApi.startAuth('devin');

    expect(mocks.get).toHaveBeenCalledWith('/devin-auth-url', {
      params: { is_webui: true },
    });
  });

  it('cancels an active OAuth session using DELETE /oauth-session with captured scope', async () => {
    const requestScope = {
      apiBase: 'http://cpa.example:8317',
      managementKey: 'cpa-key-1',
    };
    const scopedConfig = {
      baseURL: 'http://cpa.example:8317/v0/management',
      headers: { Authorization: 'Bearer cpa-key-1' },
      cpampScopedRequest: true,
    };
    mocks.delete.mockResolvedValue({ status: 'ok', cancelled: true });

    const result = await oauthApi.cancelSession('state-devin-1', requestScope);

    expect(mocks.delete).toHaveBeenCalledWith('/oauth-session', {
      ...scopedConfig,
      params: { state: 'state-devin-1' },
    });
    expect(result).toEqual({ status: 'ok', cancelled: true });
  });
});
