import { beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  get: vi.fn(),
  getRawV8: vi.fn(),
  epoch: 0,
}));

vi.mock('./client', () => ({
  apiClient: {
    get: mocks.get,
    getRawV8: mocks.getRawV8,
    getConnectionEpoch: () => mocks.epoch,
  },
}));

import { configApi } from './config';
import { providersApi } from './providers';

describe('configApi.getConfig', () => {
  beforeEach(() => {
    mocks.get.mockReset();
    mocks.getRawV8.mockReset();
    mocks.epoch = 0;
    providersApi.setLatestV8ConfigEtag(undefined);
  });
  it('overlays all 8 CPA v8 provider families including meta and records v8Supported + v8Etag', async () => {
    mocks.get.mockResolvedValueOnce({
      debug: true,
      'api-keys': ['client-key-1'],
      'meta-api-key': [{ 'api-key': 'legacy-meta' }],
    });
    mocks.getRawV8.mockResolvedValueOnce({
      data: {
        claude: [{ name: 'Claude Pool', keys: [{ 'api-key': 'sk-ant-1', auth_index: 'c-1' }] }],
        meta: [
          {
            name: 'Meta Endpoint',
            'base-url': 'https://meta.example/v1',
            keys: [{ 'api-key': '', auth_index: 'meta-1' }],
          },
        ],
      },
      headers: { ETag: '"v8-etag-42"' },
    });

    const config = await configApi.getConfig();
    expect(config.v8Supported).toBe(true);
    expect(config.v8Etag).toBe('"v8-etag-42"');
    expect(config.apiKeys).toEqual(['client-key-1']);
    expect(config.claudeApiKeys?.[0]).toMatchObject({
      name: 'Claude Pool',
      apiKey: 'sk-ant-1',
      authIndex: 'c-1',
    });
    expect(config.metaApiKeys?.[0]).toMatchObject({
      name: 'Meta Endpoint',
      apiKey: '',
      baseUrl: 'https://meta.example/v1',
      authIndex: 'meta-1',
    });
    expect(providersApi.getLatestV8ConfigEtag()).toBe('"v8-etag-42"');
  });

  it('initializes all 8 provider arrays to empty when v8 returns 404 not_found', async () => {
    providersApi.setLatestV8ConfigEtag('"stale-before-empty"');
    mocks.get.mockResolvedValueOnce({
      debug: false,
      'claude-api-key': [{ 'api-key': 'stale-v0-key' }],
    });
    mocks.getRawV8.mockRejectedValueOnce(
      Object.assign(new Error('not_found'), {
        status: 404,
        details: { error: 'not_found' },
      })
    );

    const config = await configApi.getConfig();
    expect(config.v8Supported).toBe(true);
    expect(config.v8Etag).toBeUndefined();
    expect(config.geminiApiKeys).toEqual([]);
    expect(config.interactionsApiKeys).toEqual([]);
    expect(config.codexApiKeys).toEqual([]);
    expect(config.xaiApiKeys).toEqual([]);
    expect(config.claudeApiKeys).toEqual([]);
    expect(config.vertexApiKeys).toEqual([]);
    expect(config.metaApiKeys).toEqual([]);
    expect(config.openaiCompatibility).toEqual([]);
    expect(providersApi.getLatestV8ConfigEtag()).toBeUndefined();
  });

  it('marks v8Supported false and preserves v0 provider arrays when v8 endpoint returns 404/405 unsupported', async () => {
    providersApi.setLatestV8ConfigEtag('"stale-before-unsupported"');
    mocks.get.mockResolvedValueOnce({
      'claude-api-key': [{ 'api-key': 'v0-claude' }],
      'meta-api-key': [{ 'api-key': 'v0-meta' }],
    });
    mocks.getRawV8.mockRejectedValueOnce(
      Object.assign(new Error('Method Not Allowed'), { status: 405 })
    );

    const config = await configApi.getConfig();
    expect(config.v8Supported).toBe(false);
    expect(config.claudeApiKeys).toEqual([{ apiKey: 'v0-claude' }]);
    expect(config.metaApiKeys).toEqual([{ apiKey: 'v0-meta' }]);
    expect(providersApi.getLatestV8ConfigEtag()).toBeUndefined();
  });

  it('rethrows transient 5xx and 429 errors instead of caching degraded alias-less config', async () => {
    mocks.get.mockResolvedValue({
      'claude-api-key': [{ 'api-key': 'v0-claude' }],
    });
    mocks.getRawV8.mockRejectedValueOnce(
      Object.assign(new Error('Bad Gateway'), { status: 502 })
    );
    await expect(configApi.getConfig()).rejects.toMatchObject({ status: 502 });

    mocks.getRawV8.mockRejectedValueOnce(
      Object.assign(new Error('Too Many Requests'), { status: 429 })
    );
    await expect(configApi.getConfig()).rejects.toMatchObject({ status: 429 });
  });

  it('ignores late getConfig completions from an older connection epoch when syncing latestV8ConfigEtag', async () => {
    let resolveOldV8!: (value: unknown) => void;
    mocks.get.mockResolvedValueOnce({ debug: false });
    mocks.getRawV8.mockReturnValueOnce(
      new Promise((resolve) => {
        resolveOldV8 = resolve;
      })
    );

    const oldRequest = configApi.getConfig();
    mocks.epoch = 1;
    providersApi.setLatestV8ConfigEtag('"new-target-etag"');

    resolveOldV8({
      data: {},
      headers: { etag: '"old-target-etag"' },
    });
    await oldRequest;

    expect(providersApi.getLatestV8ConfigEtag()).toBe('"new-target-etag"');
  });

  it('preserves normalized strong ETag from 404 not_found error responses and skips ETag sync when disabled', async () => {
    mocks.get.mockResolvedValueOnce({ debug: false });
    mocks.getRawV8.mockRejectedValueOnce(
      Object.assign(new Error('not_found'), {
        status: 404,
        details: { error: 'not_found' },
        headers: { ETag: 'W/"empty-root-etag-1"' },
      })
    );

    const config = await configApi.getConfig();
    expect(config.v8Supported).toBe(true);
    expect(config.v8Etag).toBe('"empty-root-etag-1"');
    expect(providersApi.getLatestV8ConfigEtag()).toBe('"empty-root-etag-1"');

    mocks.get.mockResolvedValueOnce({ debug: true });
    mocks.getRawV8.mockResolvedValueOnce({
      data: {},
      headers: { etag: '"unsynced-etag-2"' },
    });
    const unsynced = await configApi.getConfig({ syncLatestV8Etag: false });
    expect(unsynced.v8Etag).toBe('"unsynced-etag-2"');
    expect(providersApi.getLatestV8ConfigEtag()).toBe('"empty-root-etag-1"');
  });

  it('does not regress latestV8ConfigEtag when a concurrent write advances ETag while getConfig is in flight', async () => {
    let resolveSlowV8!: (value: unknown) => void;
    mocks.get.mockResolvedValueOnce({ debug: false });
    mocks.getRawV8.mockReturnValueOnce(
      new Promise((resolve) => {
        resolveSlowV8 = resolve;
      })
    );

    const inFlightGetConfig = configApi.getConfig();

    // Concurrent provider write updates latestV8ConfigEtag while getConfig is still waiting.
    providersApi.setLatestV8ConfigEtag('"newer-write-etag-2"');

    resolveSlowV8({
      data: {},
      headers: { etag: '"older-read-etag-1"' },
    });
    const result = await inFlightGetConfig;

    expect(result.v8Etag).toBe('"older-read-etag-1"');
    expect(providersApi.getLatestV8ConfigEtag()).toBe('"newer-write-etag-2"');
  });

  it('allows getConfig to sync a newer ETag when a concurrent passive getVertexConfigs read completes mid-flight', async () => {
    let resolveConfigV8!: (value: unknown) => void;
    mocks.get.mockResolvedValueOnce({ debug: false });
    mocks.getRawV8
      .mockReturnValueOnce(
        new Promise((resolve) => {
          resolveConfigV8 = resolve;
        })
      )
      .mockResolvedValueOnce({
        data: [{ name: 'Vertex 1', keys: [{ 'api-key': 'vx-1' }] }],
        headers: { etag: '"older-vertex-read-etag-1"' },
      });

    const inFlightGetConfig = configApi.getConfig();
    await providersApi.getVertexConfigs();
    expect(providersApi.getLatestV8ConfigEtag()).toBe('"older-vertex-read-etag-1"');

    resolveConfigV8({
      data: {},
      headers: { etag: '"newer-config-etag-2"' },
    });
    await inFlightGetConfig;

    expect(providersApi.getLatestV8ConfigEtag()).toBe('"newer-config-etag-2"');
  });
});
