import { beforeEach, describe, expect, it, vi } from 'vitest';
import {
  MODEL_THINKING_LEVELS_CLEAR_MARKER,
  MODEL_THINKING_LEVELS_EDIT_MARKER,
  hasModelThinkingLevelsEditMarker,
  markModelThinkingLevelsForClear,
  markModelThinkingLevelsForEdit,
} from '@/types';
import { updateOpenAIApiKey } from '@/utils/openAIKeyEntries';
import { normalizeOpenAIProvider } from './transformers';

const { mocks } = vi.hoisted(() => ({
  mocks: {
    get: vi.fn(),
    put: vi.fn(),
    delete: vi.fn(),
    getV8: vi.fn(),
    getRawV8: vi.fn(),
    putV8: vi.fn(),
    epoch: 0,
  },
}));

vi.mock('./client', () => ({
  apiClient: {
    get: mocks.get,
    put: mocks.put,
    delete: mocks.delete,
    getV8: mocks.getV8,
    getRawV8: mocks.getRawV8,
    putV8: mocks.putV8,
    getConnectionEpoch: () => mocks.epoch,
  },
}));

import { apiClient } from './client';
import { configApi } from './config';
import {
  loadAllProvidersV8Snapshot,
  providersApi,
  verifyClaudeFingerprintInRawConfig,
} from './providers';

beforeEach(() => {
  mocks.get.mockReset();
  mocks.put.mockReset();
  mocks.delete.mockReset();
  mocks.getV8.mockReset();
  mocks.getRawV8.mockReset();
  mocks.putV8.mockReset();
  mocks.epoch = 0;
  mocks.getRawV8.mockRejectedValue(Object.assign(new Error('Not Found'), { status: 404 }));
  mocks.putV8.mockRejectedValue(Object.assign(new Error('Not Found'), { status: 404 }));
  Reflect.deleteProperty(apiClient, 'putRawV8');
  providersApi.setLatestV8ConfigEtag(undefined);
});

describe('providersApi auth-index preservation', () => {
  it('saves anonymous OpenAI providers with no placeholder credential while retaining real keys', async () => {
    mocks.get.mockResolvedValueOnce({ 'openai-compatibility': [] });
    mocks.put.mockResolvedValue({});

    await providersApi.saveOpenAIProviders([
      {
        name: 'local-anonymous',
        baseUrl: 'http://localhost:11434/v1',
        apiKeyEntries: [{ apiKey: '' }],
      },
      {
        name: 'with-key',
        baseUrl: 'https://remote.example/v1',
        apiKeyEntries: [{ apiKey: 'real-key' }, { apiKey: '' }],
      },
      {
        name: 'anonymous-with-proxy',
        baseUrl: 'https://another.example/v1',
        apiKeyEntries: [{ apiKey: '', proxyUrl: 'http://proxy.example:8080' }],
      },
      {
        name: 'header-only-placeholder',
        baseUrl: 'https://headers.example/v1',
        apiKeyEntries: [{ apiKey: '', headers: { 'X-Unsupported-Per-Key': 'value' } }],
      },
    ]);

    expect(mocks.put).toHaveBeenCalledWith('/openai-compatibility', [
      {
        name: 'local-anonymous',
        'base-url': 'http://localhost:11434/v1',
        'api-key-entries': [],
      },
      {
        name: 'with-key',
        'base-url': 'https://remote.example/v1',
        'api-key-entries': [{ 'api-key': 'real-key' }],
      },
      {
        name: 'anonymous-with-proxy',
        'base-url': 'https://another.example/v1',
        'api-key-entries': [{ 'proxy-url': 'http://proxy.example:8080' }],
      },
      {
        name: 'header-only-placeholder',
        'base-url': 'https://headers.example/v1',
        'api-key-entries': [],
      },
    ]);
  });

  it('reads CPA keyless providers back with an empty credential list', async () => {
    mocks.get.mockResolvedValueOnce({
      'openai-compatibility': [
        {
          name: 'local-anonymous',
          'base-url': 'http://localhost:11434/v1',
          'api-key-entries': [],
          'auth-index': 'cpa-assigned-index',
        },
        {
          name: 'header-only-placeholder',
          'base-url': 'https://headers.example/v1',
          'api-key-entries': [{ headers: { 'X-Unsupported-Per-Key': 'value' } }],
        },
      ],
    });

    await expect(providersApi.getOpenAIProviders()).resolves.toEqual([
      expect.objectContaining({
        name: 'local-anonymous',
        baseUrl: 'http://localhost:11434/v1',
        apiKeyEntries: [],
        authIndex: 'cpa-assigned-index',
      }),
      expect.objectContaining({
        name: 'header-only-placeholder',
        baseUrl: 'https://headers.example/v1',
        apiKeyEntries: [],
      }),
    ]);
  });

  it('round-trips a keyless provider without inventing an empty credential', async () => {
    const cpaManagementKeylessProvider = {
      name: 'ollama-local',
      'base-url': 'http://127.0.0.1:11434/v1',
      'auth-index': 'cpa-keyless-index',
      models: [{ name: 'llama3' }],
      'disable-cooling': true,
    };
    const cpaRawConfigKeylessProvider = {
      name: 'ollama-local',
      'base-url': 'http://127.0.0.1:11434/v1',
      models: [{ name: 'llama3' }],
      'disable-cooling': true,
      'connection-pool': { enabled: true },
    };
    mocks.get.mockImplementation(async (url: string) => {
      if (url === '/openai-compatibility') {
        return { 'openai-compatibility': [cpaManagementKeylessProvider] };
      }
      if (url === '/config') {
        return { 'openai-compatibility': [cpaRawConfigKeylessProvider] };
      }
      throw new Error(`unexpected url: ${url}`);
    });
    mocks.put.mockResolvedValue({});

    const [provider] = await providersApi.getOpenAIProviders();
    expect(provider.apiKeyEntries).toEqual([]);
    expect(provider.authIndex).toBe('cpa-keyless-index');

    await providersApi.updateOpenAIProvider('ollama-local', 0, {
      ...provider,
      apiKeyEntries: [{ apiKey: '' }],
    });

    const [, payload] = mocks.put.mock.calls[0] as [string, Array<Record<string, unknown>>];
    expect(payload[0]).toMatchObject({
      name: 'ollama-local',
      'base-url': 'http://127.0.0.1:11434/v1',
      'api-key-entries': [],
      'auth-index': 'cpa-keyless-index',
      'disable-cooling': true,
      'connection-pool': { enabled: true },
    });
    expect(payload[0]['api-key-entries']).toEqual([]);
  });

  it('round-trips mixed keyed and keyless entries while dropping blank placeholder rows', async () => {
    const cpaManagementMixedProvider = {
      name: 'mixed-upstream',
      'base-url': 'https://api.mixed.example/v1',
      'api-key-entries': [
        {
          'api-key': '',
          'proxy-url': 'http://proxy1.internal:8080',
          weight: 2,
          'auth-index': 'cpa-keyless-proxy-index',
        },
        {
          'api-key': 'sk-real-key',
          weight: 5,
          'auth-index': 'cpa-keyed-index',
        },
      ],
      models: [{ name: 'qwen-2.5-coder' }],
    };
    const cpaRawConfigMixedProvider = {
      name: 'mixed-upstream',
      'base-url': 'https://api.mixed.example/v1',
      'api-key-entries': [
        {
          'api-key': '',
          'proxy-url': 'http://proxy1.internal:8080',
          weight: 2,
          'connection-pool': { enabled: true },
        },
        {
          'api-key': 'sk-real-key',
          weight: 5,
          'connection-pool': { 'idle-conn-timeout': '60s' },
        },
      ],
      models: [{ name: 'qwen-2.5-coder' }],
    };
    mocks.get.mockImplementation(async (url: string) => {
      if (url === '/openai-compatibility') {
        return { 'openai-compatibility': [cpaManagementMixedProvider] };
      }
      if (url === '/config') {
        return { 'openai-compatibility': [cpaRawConfigMixedProvider] };
      }
      throw new Error(`unexpected url: ${url}`);
    });
    mocks.put.mockResolvedValue({});

    const [provider] = await providersApi.getOpenAIProviders();
    expect(provider.apiKeyEntries).toEqual([
      {
        apiKey: '',
        proxyUrl: 'http://proxy1.internal:8080',
        weight: 2,
        authIndex: 'cpa-keyless-proxy-index',
        headers: undefined,
      },
      {
        apiKey: 'sk-real-key',
        proxyUrl: undefined,
        weight: 5,
        authIndex: 'cpa-keyed-index',
        headers: undefined,
      },
    ]);

    await providersApi.saveOpenAIProviders([
      {
        ...provider,
        apiKeyEntries: [...provider.apiKeyEntries, { apiKey: '   ' }],
      },
    ]);

    expect(mocks.put).toHaveBeenCalledWith('/openai-compatibility', [
      {
        name: 'mixed-upstream',
        'base-url': 'https://api.mixed.example/v1',
        'api-key-entries': [
          {
            'api-key': '',
            'auth-index': 'cpa-keyless-proxy-index',
            'proxy-url': 'http://proxy1.internal:8080',
            weight: 2,
            'connection-pool': { enabled: true },
          },
          {
            'api-key': 'sk-real-key',
            'auth-index': 'cpa-keyed-index',
            weight: 5,
            'connection-pool': { 'idle-conn-timeout': '60s' },
          },
        ],
        models: [{ name: 'qwen-2.5-coder' }],
      },
    ]);
  });

  it('preserves a bare keyless entry on a startup-disabled mixed provider without runtime auth-index', async () => {
    const cpaDisabledMixedProvider = {
      name: 'disabled-mixed-upstream',
      disabled: true,
      'base-url': 'https://api.disabled-mixed.example/v1',
      'api-key-entries': [{ 'api-key': '' }, { 'api-key': 'sk-compat-b' }],
      models: [{ name: 'qwen-2.5-coder' }],
    };
    mocks.get.mockImplementation(async (url: string) => {
      if (url === '/openai-compatibility') {
        return { 'openai-compatibility': [cpaDisabledMixedProvider] };
      }
      if (url === '/config') {
        return { 'openai-compatibility': [cpaDisabledMixedProvider] };
      }
      throw new Error(`unexpected url: ${url}`);
    });
    mocks.put.mockResolvedValue({});

    const [provider] = await providersApi.getOpenAIProviders();
    expect(provider.apiKeyEntries).toHaveLength(2);
    expect(provider.apiKeyEntries).toEqual([
      { apiKey: '', proxyUrl: undefined, headers: undefined },
      { apiKey: 'sk-compat-b', proxyUrl: undefined, headers: undefined },
    ]);

    await providersApi.updateOpenAIProvider('disabled-mixed-upstream', 0, {
      ...provider,
      disabled: false,
      apiKeyEntries: [...provider.apiKeyEntries, { apiKey: '' }],
    });

    expect(mocks.put).toHaveBeenCalledWith('/openai-compatibility', [
      {
        name: 'disabled-mixed-upstream',
        disabled: false,
        'base-url': 'https://api.disabled-mixed.example/v1',
        'api-key-entries': [{ 'api-key': '' }, { 'api-key': 'sk-compat-b' }],
        models: [{ name: 'qwen-2.5-coder' }],
      },
    ]);
  });

  it('does not write back a cleared keyed entry as an empty api-key', async () => {
    const keyedProvider = {
      name: 'keyed-upstream',
      'base-url': 'https://api.keyed.example/v1',
      'api-key-entries': [{ 'api-key': 'sk-only-key' }],
    };
    mocks.get.mockImplementation(async (url: string) => {
      if (url === '/openai-compatibility' || url === '/config') {
        return { 'openai-compatibility': [keyedProvider] };
      }
      throw new Error(`unexpected url: ${url}`);
    });
    mocks.put.mockResolvedValue({});

    const [provider] = await providersApi.getOpenAIProviders();
    await providersApi.updateOpenAIProvider('keyed-upstream', 0, {
      ...provider,
      apiKeyEntries: [updateOpenAIApiKey(provider.apiKeyEntries[0], '')],
    });

    expect(mocks.put).toHaveBeenCalledWith('/openai-compatibility', [
      {
        name: 'keyed-upstream',
        'base-url': 'https://api.keyed.example/v1',
        'api-key-entries': [],
      },
    ]);
  });

  it.each([
    { reversed: false, action: 'delete' },
    { reversed: false, action: 'clear' },
    { reversed: true, action: 'delete' },
    { reversed: true, action: 'clear' },
  ])(
    'retains the original keyless connection-pool after $action with reversed=$reversed',
    async ({ reversed, action }) => {
      const rawEntries = [
        {
          'api-key': 'key-A',
          'proxy-url': 'http://proxy-A',
          weight: 0,
          'connection-pool': { name: 'A' },
        },
        {
          'api-key': '',
          'proxy-url': 'http://proxy-B',
          weight: 2,
          'connection-pool': { name: 'B' },
        },
      ];
      if (reversed) rawEntries.reverse();
      const raw = {
        name: 'mixed',
        disabled: true,
        'base-url': 'http://upstream/v1',
        'api-key-entries': rawEntries,
      };
      mocks.get.mockResolvedValue({ 'openai-compatibility': [raw] });
      mocks.put.mockResolvedValue({});
      const [provider] = await providersApi.getOpenAIProviders();
      const entries =
        action === 'delete'
          ? provider.apiKeyEntries.filter((entry) => !entry.apiKey)
          : provider.apiKeyEntries.map((entry) =>
              entry.apiKey ? updateOpenAIApiKey(entry, '') : entry
            );

      await providersApi.saveOpenAIProviders([
        { ...provider, disabled: false, apiKeyEntries: entries },
      ]);

      expect(mocks.put).toHaveBeenCalledWith('/openai-compatibility', [
        expect.objectContaining({
          disabled: false,
          'api-key-entries': [
            {
              'api-key': '',
              'proxy-url': 'http://proxy-B',
              weight: 2,
              'connection-pool': { name: 'B' },
            },
          ],
        }),
      ]);
    }
  );

  it.each(['delete', 'reverse'])(
    'preserves original identity for two indistinguishable keyless entries after %s',
    async (action) => {
      const raw = {
        name: 'two-keyless',
        disabled: true,
        'base-url': 'http://upstream/v1',
        'api-key-entries': [
          { 'api-key': '', 'connection-pool': { name: 'first' } },
          { 'api-key': '', 'connection-pool': { name: 'second' } },
        ],
      };
      mocks.get.mockResolvedValue({ 'openai-compatibility': [raw] });
      mocks.put.mockResolvedValue({});
      const [provider] = await providersApi.getOpenAIProviders();
      const entries =
        action === 'delete'
          ? provider.apiKeyEntries.slice(1)
          : [...provider.apiKeyEntries].reverse();

      await providersApi.saveOpenAIProviders([{ ...provider, apiKeyEntries: entries }]);

      const [, saved] = mocks.put.mock.calls[0];
      expect(saved[0]['api-key-entries']).toEqual(
        (action === 'delete' ? ['second'] : ['second', 'first']).map((name) => ({
          'api-key': '',
          'connection-pool': { name },
        }))
      );
    }
  );

  it('does not inherit unknown fields when an unindexed keyless entry has an ambiguous match', async () => {
    mocks.get.mockResolvedValue({
      'openai-compatibility': [
        {
          name: 'ambiguous',
          'base-url': 'http://upstream/v1',
          'api-key-entries': [
            { 'api-key': '', 'connection-pool': { name: 'first' } },
            { 'api-key': '', 'connection-pool': { name: 'second' } },
          ],
        },
      ],
    });
    mocks.put.mockResolvedValue({});

    await providersApi.saveOpenAIProviders([
      {
        name: 'ambiguous',
        baseUrl: 'http://upstream/v1',
        apiKeyEntries: [{ apiKey: '', weight: 0 }],
      },
    ]);

    const [, saved] = mocks.put.mock.calls[0];
    expect(saved[0]['api-key-entries']).toEqual([{ weight: 0 }]);
  });

  it('keeps a newly added keyless row independent of a removed keyless row', async () => {
    const raw = {
      name: 'local',
      'base-url': 'http://localhost/v1',
      'api-key-entries': [
        { 'api-key': '', 'connection-pool': { enabled: true, 'max-idle-conns': 3 } },
        { 'api-key': '', 'connection-pool': { enabled: true, 'max-idle-conns': 7 } },
      ],
    };
    mocks.get.mockResolvedValue({ 'openai-compatibility': [raw] });
    mocks.put.mockResolvedValue({});
    const provider = normalizeOpenAIProvider(raw)!;

    await providersApi.updateOpenAIProvider('local', 0, {
      ...provider,
      apiKeyEntries: [provider.apiKeyEntries[1], { apiKey: '', weight: 0 }],
    });

    const entries = mocks.put.mock.calls[0][1][0]['api-key-entries'];
    expect(entries[0]['connection-pool']).toEqual(raw['api-key-entries'][1]['connection-pool']);
    expect(entries[1]).not.toHaveProperty('connection-pool');
  });

  it('does not give a new keyed row the unknown fields of a removed row with the same key', async () => {
    const raw = {
      name: 'local',
      'base-url': 'http://localhost/v1',
      'api-key-entries': [
        { 'api-key': 'key-a', 'connection-pool': { enabled: true, 'max-idle-conns': 3 } },
        { 'api-key': 'key-b', 'connection-pool': { enabled: true, 'max-idle-conns': 7 } },
      ],
    };
    mocks.get.mockResolvedValue({ 'openai-compatibility': [raw] });
    mocks.put.mockResolvedValue({});
    const provider = normalizeOpenAIProvider(raw)!;

    await providersApi.updateOpenAIProvider('local', 0, {
      ...provider,
      apiKeyEntries: [provider.apiKeyEntries[1], { apiKey: 'key-a' }],
    });

    const entries = mocks.put.mock.calls[0][1][0]['api-key-entries'];
    expect(entries[0]['connection-pool']).toEqual(raw['api-key-entries'][1]['connection-pool']);
    expect(entries[1]).toEqual({ 'api-key': 'key-a' });
  });

  it('preserves original unknown fields when a keyed entry receives a replacement key', async () => {
    const pool = { enabled: true, 'max-idle-conns': 3 };
    const raw = {
      name: 'local',
      'base-url': 'http://localhost/v1',
      'api-key-entries': [{ 'api-key': 'old-key', 'connection-pool': pool }],
    };
    mocks.get.mockResolvedValue({ 'openai-compatibility': [raw] });
    mocks.put.mockResolvedValue({});
    const provider = normalizeOpenAIProvider(raw)!;

    await providersApi.updateOpenAIProvider('local', 0, {
      ...provider,
      apiKeyEntries: [updateOpenAIApiKey(provider.apiKeyEntries[0], 'new-key')],
    });

    expect(mocks.put.mock.calls[0][1][0]['api-key-entries'][0]).toEqual({
      'api-key': 'new-key',
      'connection-pool': pool,
    });
  });

  it('retains the original entry identity when its replacement key matches a removed entry', async () => {
    const ownPool = { enabled: true, 'max-idle-conns': 3 };
    const raw = {
      name: 'local',
      'base-url': 'http://localhost/v1',
      'api-key-entries': [
        { 'api-key': 'old-key', 'connection-pool': ownPool },
        { 'api-key': 'existing-key', 'connection-pool': { enabled: true, 'max-idle-conns': 7 } },
      ],
    };
    mocks.get.mockResolvedValue({ 'openai-compatibility': [raw] });
    mocks.put.mockResolvedValue({});
    const provider = normalizeOpenAIProvider(raw)!;

    await providersApi.updateOpenAIProvider('local', 0, {
      ...provider,
      apiKeyEntries: [updateOpenAIApiKey(provider.apiKeyEntries[0], 'existing-key')],
    });

    expect(mocks.put.mock.calls[0][1][0]['api-key-entries'][0]['connection-pool']).toEqual(ownPool);
  });

  it('keeps each entry connection-pool when one of several entries changes its key and entries are reordered', async () => {
    const poolA = { enabled: true, 'max-idle-conns': 3 };
    const poolB = { enabled: true, 'max-idle-conns': 7 };
    const poolC = { enabled: true, 'max-idle-conns': 11 };
    const raw = {
      name: 'multi-entry',
      'base-url': 'http://localhost/v1',
      'api-key-entries': [
        { 'api-key': 'key-a', 'connection-pool': poolA },
        { 'api-key': '', 'proxy-url': 'http://proxy-b', 'connection-pool': poolB },
        { 'api-key': 'key-c', 'connection-pool': poolC },
      ],
    };
    mocks.get.mockResolvedValue({ 'openai-compatibility': [raw] });
    mocks.put.mockResolvedValue({});
    const provider = normalizeOpenAIProvider(raw)!;

    await providersApi.updateOpenAIProvider('multi-entry', 0, {
      ...provider,
      apiKeyEntries: [
        provider.apiKeyEntries[2],
        updateOpenAIApiKey(provider.apiKeyEntries[0], 'key-a-rotated'),
        provider.apiKeyEntries[1],
      ],
    });

    expect(mocks.put.mock.calls[0][1][0]['api-key-entries']).toEqual([
      { 'api-key': 'key-c', 'connection-pool': poolC },
      { 'api-key': 'key-a-rotated', 'connection-pool': poolA },
      { 'api-key': '', 'proxy-url': 'http://proxy-b', 'connection-pool': poolB },
    ]);
  });

  it('normalizes credential weights without collapsing explicit zero into omission', async () => {
    mocks.get.mockResolvedValueOnce({
      'codex-api-key': [
        { 'api-key': 'absent', 'base-url': 'https://example.com/absent' },
        { 'api-key': 'zero', 'base-url': 'https://example.com/zero', weight: 0 },
        { 'api-key': 'excluded', 'base-url': 'https://example.com/excluded', weight: -2 },
        { 'api-key': 'positive', 'base-url': 'https://example.com/positive', weight: 5 },
        { 'api-key': 'fraction', 'base-url': 'https://example.com/fraction', weight: 1.5 },
        { 'api-key': 'too-large', 'base-url': 'https://example.com/large', weight: 1_000_001 },
      ],
    });

    await expect(providersApi.getCodexConfigs()).resolves.toEqual([
      expect.not.objectContaining({ weight: expect.anything() }),
      expect.objectContaining({ apiKey: 'zero', weight: 0 }),
      expect.objectContaining({ apiKey: 'excluded', weight: -2 }),
      expect.objectContaining({ apiKey: 'positive', weight: 5 }),
      expect.not.objectContaining({ weight: expect.anything() }),
      expect.not.objectContaining({ weight: expect.anything() }),
    ]);
  });

  it('serializes provider and OpenAI key weights while allowing an existing weight to be cleared', async () => {
    mocks.get.mockResolvedValueOnce({
      'codex-api-key': [
        { 'api-key': 'clear', 'base-url': 'https://example.com/clear', weight: 9 },
        { 'api-key': 'zero', 'base-url': 'https://example.com/zero' },
        { 'api-key': 'positive', 'base-url': 'https://example.com/positive' },
      ],
    });
    mocks.put.mockResolvedValue({});

    await providersApi.saveCodexConfigs([
      { apiKey: 'clear', baseUrl: 'https://example.com/clear' },
      { apiKey: 'zero', baseUrl: 'https://example.com/zero', weight: 0 },
      { apiKey: 'excluded', baseUrl: 'https://example.com/excluded', weight: -2 },
      { apiKey: 'positive', baseUrl: 'https://example.com/positive', weight: 7 },
    ]);

    expect(mocks.put).toHaveBeenLastCalledWith('/codex-api-key', [
      { 'api-key': 'clear', 'base-url': 'https://example.com/clear' },
      { 'api-key': 'zero', 'base-url': 'https://example.com/zero', weight: 0 },
      { 'api-key': 'excluded', 'base-url': 'https://example.com/excluded', weight: -2 },
      { 'api-key': 'positive', 'base-url': 'https://example.com/positive', weight: 7 },
    ]);

    mocks.get.mockResolvedValueOnce({
      'openai-compatibility': [
        {
          name: 'weighted',
          'base-url': 'https://openai.example/v1',
          'api-key-entries': [
            { 'api-key': 'clear', weight: 4 },
            { 'api-key': 'zero' },
            { 'api-key': 'positive' },
          ],
        },
      ],
    });

    await providersApi.saveOpenAIProviders([
      {
        name: 'weighted',
        baseUrl: 'https://openai.example/v1',
        apiKeyEntries: [
          { apiKey: 'clear' },
          { apiKey: 'zero', weight: 0 },
          { apiKey: 'positive', weight: 3 },
        ],
      },
    ]);

    expect(mocks.put).toHaveBeenLastCalledWith('/openai-compatibility', [
      {
        name: 'weighted',
        'base-url': 'https://openai.example/v1',
        'api-key-entries': [
          { 'api-key': 'clear' },
          { 'api-key': 'zero', weight: 0 },
          { 'api-key': 'positive', weight: 3 },
        ],
      },
    ]);
  });

  it('loads and creates xAI API key configs through the native management section', async () => {
    mocks.get.mockResolvedValueOnce({
      'xai-api-key': [
        {
          'api-key': 'xai-existing',
          'base-url': 'https://api.x.ai/v1',
          websockets: true,
          'raw-field': 'keep',
        },
      ],
    });

    await expect(providersApi.getXAIConfigs()).resolves.toEqual([
      expect.objectContaining({
        apiKey: 'xai-existing',
        baseUrl: 'https://api.x.ai/v1',
        websockets: true,
      }),
    ]);

    mocks.get.mockResolvedValueOnce({
      'xai-api-key': [
        {
          'api-key': 'xai-existing',
          'base-url': 'https://api.x.ai/v1',
          'raw-field': 'keep',
        },
      ],
    });
    mocks.put.mockResolvedValue({});

    await providersApi.createXAIConfig({
      apiKey: 'xai-new',
      baseUrl: 'https://api.x.ai/v1',
      websockets: true,
      models: [{ name: 'grok-4.5' }],
    });

    expect(mocks.put).toHaveBeenCalledWith('/xai-api-key', [
      {
        'api-key': 'xai-existing',
        'base-url': 'https://api.x.ai/v1',
        'raw-field': 'keep',
      },
      {
        'api-key': 'xai-new',
        'base-url': 'https://api.x.ai/v1',
        websockets: true,
        models: [{ name: 'grok-4.5' }],
      },
    ]);
  });

  it('preserves existing xAI configs from camel-case raw config aliases', async () => {
    mocks.get.mockResolvedValueOnce({
      xaiApiKeys: [
        {
          'api-key': 'xai-existing',
          'base-url': 'https://api.x.ai/v1',
          'future-field': 'keep',
        },
      ],
    });
    mocks.put.mockResolvedValue({});

    await providersApi.createXAIConfig({
      apiKey: 'xai-new',
      baseUrl: 'https://api.x.ai/v1',
    });

    expect(mocks.put).toHaveBeenCalledWith('/xai-api-key', [
      {
        'api-key': 'xai-existing',
        'base-url': 'https://api.x.ai/v1',
        'future-field': 'keep',
      },
      {
        'api-key': 'xai-new',
        'base-url': 'https://api.x.ai/v1',
      },
    ]);
  });

  it('loads and saves native Interactions API keys through their management endpoint', async () => {
    mocks.get.mockResolvedValueOnce({
      'interactions-api-key': [
        {
          'api-key': 'interactions-key',
          prefix: 'native',
          'base-url': 'https://generativelanguage.googleapis.com',
          'disable-cooling': true,
        },
      ],
    });

    await expect(providersApi.getInteractionsKeys()).resolves.toEqual([
      expect.objectContaining({
        apiKey: 'interactions-key',
        prefix: 'native',
        disableCooling: true,
      }),
    ]);

    mocks.get.mockResolvedValueOnce({ 'interactions-api-key': [] });
    mocks.put.mockResolvedValue({});
    await providersApi.saveInteractionsKeys([
      {
        apiKey: 'interactions-key',
        authIndex: 'runtime-only-index',
        prefix: 'native',
        disableCooling: true,
      },
    ]);

    expect(mocks.put).toHaveBeenCalledWith('/interactions-api-key', [
      {
        'api-key': 'interactions-key',
        prefix: 'native',
        'disable-cooling': true,
      },
    ]);
  });

  it('rejects auth-index-only Gemini-compatible provider entries', async () => {
    await expect(
      providersApi.saveInteractionsKeys([
        {
          apiKey: '',
          authIndex: 'runtime-only-index',
        },
      ])
    ).rejects.toThrow('API key is required for Gemini and Interactions providers');

    expect(mocks.get).not.toHaveBeenCalled();
    expect(mocks.put).not.toHaveBeenCalled();
  });

  it('serializes auth-index-only provider keys and preserves unknown raw fields', async () => {
    mocks.get.mockResolvedValue({
      'codex-api-key': [
        {
          'auth-index': 'auth-1',
          'api-key': 'old-key',
          'base-url': 'https://old.example.com/v1',
          'raw-field': 'keep',
          models: [{ name: 'old-model', 'raw-model-field': true }],
        },
      ],
    });
    mocks.put.mockResolvedValue({});

    await providersApi.saveCodexConfigs([
      {
        apiKey: '',
        authIndex: 'auth-1',
        baseUrl: 'https://new.example.com/v1',
        models: [{ name: 'new-model', alias: 'alias' }],
      },
    ]);

    expect(mocks.put).toHaveBeenCalledWith('/codex-api-key', [
      {
        'raw-field': 'keep',
        'auth-index': 'auth-1',
        'base-url': 'https://new.example.com/v1',
        models: [{ name: 'new-model', alias: 'alias', 'raw-model-field': true }],
      },
    ]);
  });

  it('serializes OpenAI auth-index entries and preserves raw provider fields', async () => {
    mocks.get.mockResolvedValue({
      'openai-compatibility': [
        {
          name: 'openai-compatible',
          'base-url': 'https://api.example.com/v1',
          'api-key-entries': [
            {
              'auth-index': 'auth-2',
              'api-key': 'old-key',
              'raw-entry-field': 'keep-entry',
            },
          ],
          'raw-provider-field': 'keep-provider',
        },
      ],
    });
    mocks.put.mockResolvedValue({});

    await providersApi.saveOpenAIProviders([
      normalizeOpenAIProvider({
        name: 'openai-compatible',
        'base-url': 'https://api.example.com/v1',
        'api-key-entries': [{ 'auth-index': 'auth-2' }],
      })!,
    ]);

    expect(mocks.put).toHaveBeenCalledWith('/openai-compatibility', [
      {
        'raw-provider-field': 'keep-provider',
        name: 'openai-compatible',
        'base-url': 'https://api.example.com/v1',
        'api-key-entries': [{ 'raw-entry-field': 'keep-entry', 'auth-index': 'auth-2' }],
      },
    ]);
  });

  it('falls back to serialized payload when raw config loading fails', async () => {
    mocks.get.mockRejectedValue(new Error('forbidden'));
    mocks.put.mockResolvedValue({});

    await providersApi.saveGeminiKeys([{ apiKey: 'gemini-key', authIndex: 'runtime-only-index' }]);

    expect(mocks.put).toHaveBeenCalledWith('/gemini-api-key', [{ 'api-key': 'gemini-key' }]);
  });
});

describe('providersApi v1.16 provider fields', () => {
  it('normalizes OpenAI model image/thinking and provider disable-cooling fields', async () => {
    mocks.get.mockResolvedValue({
      'openai-compatibility': [
        {
          name: 'openai-compatible',
          'base-url': 'https://api.example.com/v1',
          'disable-cooling': true,
          models: [
            {
              name: 'gpt-image',
              image: true,
              thinking: { effort: 'high' },
            },
          ],
        },
      ],
    });

    const providers = await providersApi.getOpenAIProviders();

    expect(providers[0]).toMatchObject({
      name: 'openai-compatible',
      disableCooling: true,
      models: [{ name: 'gpt-image', image: true, thinking: { effort: 'high' } }],
    });
  });

  it('fills missing OpenAI provider disable-cooling from /config fallback', async () => {
    mocks.get.mockImplementation(async (url: string) => {
      if (url === '/openai-compatibility') {
        return {
          'openai-compatibility': [
            {
              name: 'openai-compatible',
              'base-url': 'https://api.example.com/v1',
              'api-key-entries': [],
            },
          ],
        };
      }
      if (url === '/config') {
        return {
          'openai-compatibility': [
            {
              name: 'openai-compatible',
              'base-url': 'https://api.example.com/v1',
              'api-key-entries': [],
              'disable-cooling': true,
            },
          ],
        };
      }
      throw new Error(`unexpected url: ${url}`);
    });

    const providers = await providersApi.getOpenAIProviders();

    expect(providers[0]).toMatchObject({
      name: 'openai-compatible',
      disableCooling: true,
    });
    expect(mocks.get).toHaveBeenNthCalledWith(1, '/openai-compatibility');
    expect(mocks.get).toHaveBeenNthCalledWith(2, '/config');
  });

  it('serializes Claude disable-cooling, rebuild flag, cloak cache, and model metadata without legacy cch writes', async () => {
    mocks.get.mockResolvedValue({
      'claude-api-key': [
        {
          'auth-index': 'auth-4',
          'raw-field': 'keep',
          cloak: { 'raw-cloak-field': 'keep-cloak' },
          models: [{ name: 'claude-sonnet', 'raw-model-field': true }],
        },
      ],
    });
    mocks.put.mockResolvedValue({});

    await providersApi.saveClaudeConfigs([
      {
        apiKey: '',
        authIndex: 'auth-4',
        disableCooling: true,
        experimentalCchSigning: true,
        rebuildMidSystemMessage: true,
        cloak: { mode: 'auto', cacheUserId: true },
        models: [
          {
            name: 'claude-sonnet',
            alias: 'sonnet',
            image: true,
            forceMapping: true,
            thinking: { budget_tokens: 1024 },
          },
        ],
      },
    ]);

    expect(mocks.put).toHaveBeenCalledWith('/claude-api-key', [
      {
        'raw-field': 'keep',
        'auth-index': 'auth-4',
        'disable-cooling': true,
        'rebuild-mid-system-message': true,
        cloak: {
          'raw-cloak-field': 'keep-cloak',
          mode: 'auto',
          'cache-user-id': true,
        },
        models: [
          {
            'raw-model-field': true,
            name: 'claude-sonnet',
            alias: 'sonnet',
            image: true,
            'force-mapping': true,
            thinking: { budget_tokens: 1024 },
          },
        ],
      },
    ]);
  });

  it('serializes Claude fingerprint-profile without creating legacy cch fields', async () => {
    mocks.get.mockResolvedValue({ 'claude-api-key': [] });
    mocks.put.mockResolvedValue({});

    await providersApi.createClaudeConfig({
      apiKey: 'key',
      fingerprintProfile: 'claude-code-cli',
    });

    expect(mocks.put).toHaveBeenLastCalledWith('/claude-api-key', [
      { 'api-key': 'key', 'fingerprint-profile': 'claude-code-cli' },
    ]);
  });

  it('omits fingerprint and legacy cch fields when a new Claude config uses the default profile', async () => {
    mocks.get.mockResolvedValue({ 'claude-api-key': [] });
    mocks.put.mockResolvedValue({});

    await providersApi.createClaudeConfig({
      apiKey: 'key',
      fingerprintProfile: '',
    });

    expect(mocks.put).toHaveBeenLastCalledWith('/claude-api-key', [{ 'api-key': 'key' }]);
  });

  it('preserves raw legacy cch signing when the fingerprint is untouched', async () => {
    mocks.get.mockResolvedValue({
      'claude-api-key': [
        {
          'api-key': 'claude-key',
          'base-url': 'https://api.anthropic.com',
          'experimental-cch-signing': true,
          priority: 1,
        },
      ],
    });
    mocks.put.mockResolvedValue({});

    await providersApi.saveClaudeConfigs([
      { apiKey: 'claude-key', baseUrl: 'https://api.anthropic.com', priority: 10 },
    ]);

    expect(mocks.put).toHaveBeenLastCalledWith('/claude-api-key', [
      {
        'api-key': 'claude-key',
        'base-url': 'https://api.anthropic.com',
        'experimental-cch-signing': true,
        priority: 10,
      },
    ]);
  });

  it('keeps fingerprint and legacy cch when an update leaves the fingerprint untouched', async () => {
    mocks.get.mockResolvedValue({
      'claude-api-key': [
        {
          'api-key': 'claude-key',
          'base-url': 'https://api.anthropic.com',
          'fingerprint-profile': 'claude-code-cli',
          'experimental-cch-signing': true,
          priority: 1,
        },
      ],
    });
    mocks.put.mockResolvedValue({});

    await providersApi.updateClaudeConfig(
      {
        apiKey: 'claude-key',
        baseUrl: 'https://api.anthropic.com',
        fingerprintProfile: 'claude-code-cli',
        priority: 1,
      },
      {
        apiKey: 'claude-key',
        baseUrl: 'https://api.anthropic.com',
        fingerprintProfile: 'claude-code-cli',
        priority: 2,
      }
    );

    expect(mocks.put).toHaveBeenLastCalledWith('/claude-api-key', [
      {
        'api-key': 'claude-key',
        'base-url': 'https://api.anthropic.com',
        'fingerprint-profile': 'claude-code-cli',
        'experimental-cch-signing': true,
        priority: 2,
      },
    ]);
  });

  it('preserves raw legacy cch signing when the user explicitly enables the fingerprint profile', async () => {
    mocks.get.mockResolvedValue({
      'claude-api-key': [
        {
          'api-key': 'claude-key',
          'base-url': 'https://api.anthropic.com',
          'experimental-cch-signing': true,
        },
      ],
    });
    mocks.put.mockResolvedValue({});

    await providersApi.updateClaudeConfig(
      { apiKey: 'claude-key', baseUrl: 'https://api.anthropic.com' },
      {
        apiKey: 'claude-key',
        baseUrl: 'https://api.anthropic.com',
        fingerprintProfile: 'claude-code-cli',
      }
    );

    expect(mocks.put).toHaveBeenLastCalledWith('/claude-api-key', [
      {
        'api-key': 'claude-key',
        'base-url': 'https://api.anthropic.com',
        'fingerprint-profile': 'claude-code-cli',
        'experimental-cch-signing': true,
      },
    ]);
  });

  it('removes fingerprint-profile and legacy cch when the user explicitly selects the default profile', async () => {
    mocks.get.mockResolvedValue({
      'claude-api-key': [
        {
          'api-key': 'claude-key',
          'base-url': 'https://api.anthropic.com',
          'fingerprint-profile': 'claude-code-cli',
          'experimental-cch-signing': true,
        },
      ],
    });
    mocks.put.mockResolvedValue({});

    await providersApi.updateClaudeConfig(
      {
        apiKey: 'claude-key',
        baseUrl: 'https://api.anthropic.com',
        fingerprintProfile: 'claude-code-cli',
      },
      { apiKey: 'claude-key', baseUrl: 'https://api.anthropic.com', fingerprintProfile: '' }
    );

    expect(mocks.put).toHaveBeenLastCalledWith('/claude-api-key', [
      { 'api-key': 'claude-key', 'base-url': 'https://api.anthropic.com' },
    ]);
  });

  it('preserves unknown future fingerprint-profile values on unrelated edits', async () => {
    mocks.get.mockResolvedValue({
      'claude-api-key': [
        {
          'api-key': 'claude-key',
          'base-url': 'https://api.anthropic.com',
          'fingerprint-profile': 'claude-desktop',
          weight: 1,
        },
      ],
    });
    mocks.put.mockResolvedValue({});

    await providersApi.saveClaudeConfigs([
      { apiKey: 'claude-key', baseUrl: 'https://api.anthropic.com', weight: 2 },
    ]);

    expect(mocks.put).toHaveBeenLastCalledWith('/claude-api-key', [
      {
        'api-key': 'claude-key',
        'base-url': 'https://api.anthropic.com',
        'fingerprint-profile': 'claude-desktop',
        weight: 2,
      },
    ]);
  });

  it('serializes Gemini key disable-cooling and OpenAI provider model metadata', async () => {
    mocks.get.mockResolvedValueOnce({ 'gemini-api-key': [] });
    mocks.put.mockResolvedValue({});

    await providersApi.saveGeminiKeys([{ apiKey: 'gemini-key', disableCooling: true }]);

    expect(mocks.put).toHaveBeenLastCalledWith('/gemini-api-key', [
      { 'api-key': 'gemini-key', 'disable-cooling': true },
    ]);

    mocks.get.mockResolvedValueOnce({ 'openai-compatibility': [] });

    await providersApi.saveOpenAIProviders([
      {
        name: 'openai-compatible',
        baseUrl: 'https://api.example.com/v1',
        disableCooling: true,
        apiKeyEntries: [],
        models: [
          {
            name: 'gpt-image',
            image: true,
            forceMapping: true,
            inputModalities: ['text', 'image'],
            outputModalities: ['image'],
            thinking: { mode: 'auto' },
          },
        ],
      },
    ]);

    expect(mocks.put).toHaveBeenLastCalledWith('/openai-compatibility', [
      {
        name: 'openai-compatible',
        'base-url': 'https://api.example.com/v1',
        'api-key-entries': [],
        'disable-cooling': true,
        models: [
          {
            name: 'gpt-image',
            image: true,
            'force-mapping': true,
            'input-modalities': ['text', 'image'],
            'output-modalities': ['image'],
            thinking: { mode: 'auto' },
          },
        ],
      },
    ]);
  });

  it('preserves raw provider fields when section payloads omit them', async () => {
    mocks.put.mockResolvedValue({});

    mocks.get.mockResolvedValueOnce({
      'gemini-api-key': [
        {
          'api-key': 'gemini-key',
          'disable-cooling': true,
          models: [{ name: 'gemini-model', image: true, thinking: { mode: 'auto' } }],
        },
      ],
    });

    await providersApi.saveGeminiKeys([
      {
        apiKey: 'gemini-key',
        models: [{ name: 'gemini-model', alias: 'gemini-alias' }],
      },
    ]);

    expect(mocks.put).toHaveBeenLastCalledWith('/gemini-api-key', [
      {
        'api-key': 'gemini-key',
        'disable-cooling': true,
        models: [
          {
            name: 'gemini-model',
            alias: 'gemini-alias',
            image: true,
            thinking: { mode: 'auto' },
          },
        ],
      },
    ]);

    mocks.get.mockResolvedValueOnce({
      'codex-api-key': [
        {
          'auth-index': 'codex-auth',
          'disable-cooling': true,
          models: [{ name: 'codex-model', image: true, thinking: { budget_tokens: 2048 } }],
        },
      ],
    });

    await providersApi.saveCodexConfigs([
      {
        apiKey: '',
        authIndex: 'codex-auth',
        models: [{ name: 'codex-model', alias: 'codex-alias' }],
      },
    ]);

    expect(mocks.put).toHaveBeenLastCalledWith('/codex-api-key', [
      {
        'auth-index': 'codex-auth',
        'disable-cooling': true,
        models: [
          {
            name: 'codex-model',
            alias: 'codex-alias',
            image: true,
            thinking: { budget_tokens: 2048 },
          },
        ],
      },
    ]);

    mocks.get.mockResolvedValueOnce({
      'claude-api-key': [
        {
          'auth-index': 'claude-auth',
          'disable-cooling': true,
          'experimental-cch-signing': true,
          cloak: { mode: 'auto', 'cache-user-id': true },
          models: [{ name: 'claude-model', image: true, thinking: { enabled: true } }],
        },
      ],
    });

    await providersApi.saveClaudeConfigs([
      {
        apiKey: '',
        authIndex: 'claude-auth',
        cloak: { mode: 'always' },
        models: [{ name: 'claude-model', alias: 'claude-alias' }],
      },
    ]);

    expect(mocks.put).toHaveBeenLastCalledWith('/claude-api-key', [
      {
        'auth-index': 'claude-auth',
        'disable-cooling': true,
        'experimental-cch-signing': true,
        cloak: { mode: 'always', 'cache-user-id': true },
        models: [
          {
            name: 'claude-model',
            alias: 'claude-alias',
            image: true,
            thinking: { enabled: true },
          },
        ],
      },
    ]);

    mocks.get.mockResolvedValueOnce({
      'openai-compatibility': [
        {
          name: 'openai-compatible',
          'base-url': 'https://api.example.com/v1',
          'api-key-entries': [],
          'disable-cooling': true,
          models: [
            {
              name: 'openai-model',
              image: true,
              'force-mapping': true,
              'input-modalities': ['text', 'image'],
              'output-modalities': ['text'],
              thinking: { effort: 'medium' },
            },
          ],
        },
      ],
    });

    await providersApi.saveOpenAIProviders([
      {
        name: 'openai-compatible',
        baseUrl: 'https://api.example.com/v1',
        apiKeyEntries: [],
        models: [{ name: 'openai-model', alias: 'openai-alias' }],
      },
    ]);

    expect(mocks.put).toHaveBeenLastCalledWith('/openai-compatibility', [
      {
        name: 'openai-compatible',
        'base-url': 'https://api.example.com/v1',
        'api-key-entries': [],
        'disable-cooling': true,
        models: [
          {
            name: 'openai-model',
            alias: 'openai-alias',
            image: true,
            'force-mapping': true,
            'input-modalities': ['text', 'image'],
            'output-modalities': ['text'],
            thinking: { effort: 'medium' },
          },
        ],
      },
    ]);
  });

  it('lets explicit empty modality arrays clear preserved raw values', async () => {
    mocks.get.mockResolvedValueOnce({
      'openai-compatibility': [
        {
          name: 'openai-compatible',
          'base-url': 'https://api.example.com/v1',
          'api-key-entries': [{ 'api-key': 'test-key' }],
          models: [
            {
              name: 'openai-model',
              'input-modalities': ['text', 'image'],
              'output-modalities': ['image'],
            },
          ],
        },
      ],
    });
    mocks.put.mockResolvedValue({});

    await providersApi.saveOpenAIProviders([
      {
        name: 'openai-compatible',
        baseUrl: 'https://api.example.com/v1',
        apiKeyEntries: [{ apiKey: 'test-key' }],
        models: [
          {
            name: 'openai-model',
            inputModalities: [],
            outputModalities: [],
          },
        ],
      },
    ]);

    expect(mocks.put).toHaveBeenCalledWith('/openai-compatibility', [
      {
        name: 'openai-compatible',
        'base-url': 'https://api.example.com/v1',
        'api-key-entries': [{ 'api-key': 'test-key' }],
        models: [
          {
            name: 'openai-model',
            'input-modalities': [],
            'output-modalities': [],
          },
        ],
      },
    ]);
  });

  it('merges custom thinking levels without dropping raw-only thinking fields', async () => {
    mocks.get.mockResolvedValueOnce({
      'openai-compatibility': [
        {
          name: 'openai-compatible',
          'base-url': 'https://api.example.com/v1',
          'api-key-entries': [],
          models: [
            {
              name: 'openai-model',
              thinking: {
                levels: ['high'],
                'future-option': { enabled: true },
                'future-value': 123,
              },
            },
          ],
        },
      ],
    });
    mocks.put.mockResolvedValue({});

    await providersApi.saveOpenAIProviders([
      {
        name: 'openai-compatible',
        baseUrl: 'https://api.example.com/v1',
        apiKeyEntries: [],
        models: [
          {
            name: 'openai-model',
            thinking: { levels: ['high', 'max'] },
          },
        ],
      },
    ]);

    expect(mocks.put).toHaveBeenCalledWith('/openai-compatibility', [
      {
        name: 'openai-compatible',
        'base-url': 'https://api.example.com/v1',
        'api-key-entries': [],
        models: [
          {
            name: 'openai-model',
            thinking: {
              levels: ['high', 'max'],
              'future-option': { enabled: true },
              'future-value': 123,
            },
          },
        ],
      },
    ]);
  });

  it('removes only thinking levels when leaving them unconfigured', async () => {
    mocks.get.mockResolvedValueOnce({
      'openai-compatibility': [
        {
          name: 'openai-compatible',
          'base-url': 'https://api.example.com/v1',
          'api-key-entries': [],
          models: [
            {
              name: 'openai-model',
              thinking: { levels: ['high'], 'future-option': { enabled: true } },
            },
          ],
        },
      ],
    });
    mocks.put.mockResolvedValue({});

    await providersApi.saveOpenAIProviders([
      {
        name: 'openai-compatible',
        baseUrl: 'https://api.example.com/v1',
        apiKeyEntries: [],
        models: [markModelThinkingLevelsForClear({ name: 'openai-model' })],
      },
    ]);

    expect(mocks.put).toHaveBeenCalledWith('/openai-compatibility', [
      {
        name: 'openai-compatible',
        'base-url': 'https://api.example.com/v1',
        'api-key-entries': [],
        models: [
          {
            name: 'openai-model',
            thinking: { 'future-option': { enabled: true } },
          },
        ],
      },
    ]);
  });

  it('removes only thinking levels when no future thinking fields remain', async () => {
    mocks.get.mockResolvedValueOnce({
      'openai-compatibility': [
        {
          name: 'openai-compatible',
          'base-url': 'https://api.example.com/v1',
          'api-key-entries': [],
          models: [{ name: 'openai-model', thinking: { levels: ['high'] } }],
        },
      ],
    });
    mocks.put.mockResolvedValue({});

    await providersApi.saveOpenAIProviders([
      {
        name: 'openai-compatible',
        baseUrl: 'https://api.example.com/v1',
        apiKeyEntries: [],
        models: [markModelThinkingLevelsForClear({ name: 'openai-model' })],
      },
    ]);

    expect(mocks.put).toHaveBeenCalledWith('/openai-compatibility', [
      {
        name: 'openai-compatible',
        'base-url': 'https://api.example.com/v1',
        'api-key-entries': [],
        models: [{ name: 'openai-model' }],
      },
    ]);
  });

  it('preserves an explicitly empty thinking container during a normal save', async () => {
    mocks.get.mockResolvedValueOnce({
      'openai-compatibility': [
        {
          name: 'openai-compatible',
          'base-url': 'https://api.example.com/v1',
          'api-key-entries': [],
          models: [{ name: 'openai-model', thinking: {} }],
        },
      ],
    });
    mocks.put.mockResolvedValue({});

    await providersApi.saveOpenAIProviders([
      {
        name: 'openai-compatible',
        baseUrl: 'https://api.example.com/v1',
        apiKeyEntries: [],
        models: [{ name: 'openai-model', thinking: {} }],
      },
    ]);

    expect(mocks.put).toHaveBeenCalledWith('/openai-compatibility', [
      {
        name: 'openai-compatible',
        'base-url': 'https://api.example.com/v1',
        'api-key-entries': [],
        models: [{ name: 'openai-model', thinking: {} }],
      },
    ]);
  });

  it('keeps an explicitly empty thinking container when levels are cleared', async () => {
    mocks.get.mockResolvedValueOnce({
      'openai-compatibility': [
        {
          name: 'openai-compatible',
          'base-url': 'https://api.example.com/v1',
          'api-key-entries': [],
          models: [{ name: 'openai-model', thinking: {} }],
        },
      ],
    });
    mocks.put.mockResolvedValue({});

    await providersApi.saveOpenAIProviders([
      {
        name: 'openai-compatible',
        baseUrl: 'https://api.example.com/v1',
        apiKeyEntries: [],
        models: [markModelThinkingLevelsForClear({ name: 'openai-model', thinking: {} })],
      },
    ]);

    expect(mocks.put).toHaveBeenCalledWith('/openai-compatibility', [
      {
        name: 'openai-compatible',
        'base-url': 'https://api.example.com/v1',
        'api-key-entries': [],
        models: [{ name: 'openai-model', thinking: {} }],
      },
    ]);
  });

  it('preserves unknown thinking levels and future fields during a custom update', async () => {
    mocks.get.mockResolvedValueOnce({
      'openai-compatibility': [
        {
          name: 'openai-compatible',
          'base-url': 'https://api.example.com/v1',
          'api-key-entries': [],
          models: [
            {
              name: 'openai-model',
              thinking: {
                levels: ['high', 'ultra'],
                'future-option': { enabled: true },
              },
            },
          ],
        },
      ],
    });
    mocks.put.mockResolvedValue({});

    await providersApi.saveOpenAIProviders([
      {
        name: 'openai-compatible',
        baseUrl: 'https://api.example.com/v1',
        apiKeyEntries: [],
        models: [{ name: 'openai-model', thinking: { levels: ['max', 'ultra'] } }],
      },
    ]);

    expect(mocks.put).toHaveBeenCalledWith('/openai-compatibility', [
      {
        name: 'openai-compatible',
        'base-url': 'https://api.example.com/v1',
        'api-key-entries': [],
        models: [
          {
            name: 'openai-model',
            thinking: {
              levels: ['max', 'ultra'],
              'future-option': { enabled: true },
            },
          },
        ],
      },
    ]);
  });

  it('does not leak a clear marker or null when raw config loading fails', async () => {
    mocks.get.mockRejectedValueOnce(new Error('forbidden'));
    mocks.put.mockResolvedValue({});

    await providersApi.saveOpenAIProviders([
      {
        name: 'openai-compatible',
        baseUrl: 'https://api.example.com/v1',
        apiKeyEntries: [],
        models: [
          markModelThinkingLevelsForClear({
            name: 'openai-model',
            thinking: { 'future-option': { enabled: true } },
          }),
        ],
      },
    ]);

    expect(mocks.put).toHaveBeenCalledWith('/openai-compatibility', [
      {
        name: 'openai-compatible',
        'base-url': 'https://api.example.com/v1',
        'api-key-entries': [],
        models: [{ name: 'openai-model', thinking: { 'future-option': { enabled: true } } }],
      },
    ]);
    const written = mocks.put.mock.calls[0]?.[1];
    expect(JSON.stringify(written)).not.toContain('null');
    const writtenModel = (written as Array<{ models?: unknown[] }>)[0]?.models?.[0];
    expect(writtenModel && Reflect.ownKeys(writtenModel)).not.toContain(
      MODEL_THINKING_LEVELS_CLEAR_MARKER
    );
  });

  it('serializes custom thinking levels on the OpenAI create path', async () => {
    mocks.get.mockResolvedValueOnce({ 'openai-compatibility': [] });
    mocks.put.mockResolvedValue({});

    await providersApi.createOpenAIProvider({
      name: 'new-openai',
      baseUrl: 'https://api.example.com/v1',
      apiKeyEntries: [],
      models: [
        markModelThinkingLevelsForEdit({
          name: 'new-model',
          thinking: { levels: ['high', 'max'], zero_allowed: true },
        }),
      ],
    });

    expect(mocks.put).toHaveBeenCalledWith('/openai-compatibility', [
      {
        name: 'new-openai',
        'base-url': 'https://api.example.com/v1',
        'api-key-entries': [],
        models: [{ name: 'new-model', thinking: { levels: ['high', 'max'] } }],
      },
    ]);
  });

  it('does not write a legacy raw thinking null value back to CPA', async () => {
    mocks.get.mockResolvedValueOnce({
      'openai-compatibility': [
        {
          name: 'openai-compatible',
          'base-url': 'https://api.example.com/v1',
          'api-key-entries': [],
          models: [{ name: 'openai-model', thinking: null }],
        },
      ],
    });
    mocks.put.mockResolvedValue({});

    await providersApi.saveOpenAIProviders([
      {
        name: 'openai-compatible',
        baseUrl: 'https://api.example.com/v1',
        apiKeyEntries: [],
        models: [{ name: 'openai-model' }],
      },
    ]);

    expect(mocks.put).toHaveBeenCalledWith('/openai-compatibility', [
      {
        name: 'openai-compatible',
        'base-url': 'https://api.example.com/v1',
        'api-key-entries': [],
        models: [{ name: 'openai-model' }],
      },
    ]);
  });

  it('lets explicit false values override preserved raw booleans', async () => {
    mocks.get.mockResolvedValueOnce({
      'openai-compatibility': [
        {
          name: 'openai-compatible',
          'base-url': 'https://api.example.com/v1',
          'api-key-entries': [],
          'disable-cooling': true,
        },
      ],
    });
    mocks.put.mockResolvedValue({});

    await providersApi.saveOpenAIProviders([
      {
        name: 'openai-compatible',
        baseUrl: 'https://api.example.com/v1',
        apiKeyEntries: [],
        disableCooling: false,
      },
    ]);

    expect(mocks.put).toHaveBeenLastCalledWith('/openai-compatibility', [
      {
        name: 'openai-compatible',
        'base-url': 'https://api.example.com/v1',
        'api-key-entries': [],
        'disable-cooling': false,
      },
    ]);
  });

  it('preserves all four transport states and clears an existing override on inherit', async () => {
    mocks.get.mockResolvedValueOnce({
      'gemini-api-key': [
        { 'api-key': 'true', 'disable-cooling': true },
        { 'api-key': 'false', 'disable-cooling': false },
        { 'api-key': 'null', 'disable-cooling': null },
        { 'api-key': 'absent' },
      ],
    });

    await expect(providersApi.getGeminiKeys()).resolves.toEqual([
      expect.objectContaining({ apiKey: 'true', disableCooling: true }),
      expect.objectContaining({ apiKey: 'false', disableCooling: false }),
      expect.objectContaining({ apiKey: 'null', disableCooling: null }),
      expect.not.objectContaining({ disableCooling: expect.anything() }),
    ]);

    mocks.get.mockResolvedValueOnce({
      'gemini-api-key': [{ 'api-key': 'old', disable_cooling: true, 'unknown-field': 'keep' }],
    });
    mocks.put.mockResolvedValue({});
    await providersApi.saveGeminiKeys([{ apiKey: 'old', disableCooling: null }]);

    expect(mocks.put).toHaveBeenLastCalledWith('/gemini-api-key', [
      {
        'api-key': 'old',
        'disable-cooling': null,
        'unknown-field': 'keep',
      },
    ]);
  });

  it('serializes Vertex cooling overrides, including explicit false', async () => {
    mocks.get.mockResolvedValueOnce({ 'vertex-api-key': [] });
    mocks.put.mockResolvedValue({});

    await providersApi.saveVertexConfigs([
      { apiKey: 'vertex-key', baseUrl: 'https://vertex.example.com', disableCooling: false },
    ]);

    expect(mocks.put).toHaveBeenLastCalledWith('/vertex-api-key', [
      {
        'api-key': 'vertex-key',
        'base-url': 'https://vertex.example.com',
        'disable-cooling': false,
      },
    ]);
  });

  it('preserves model metadata by index when model names change', async () => {
    mocks.get.mockResolvedValueOnce({
      'openai-compatibility': [
        {
          name: 'openai-compatible',
          'base-url': 'https://api.example.com/v1',
          'api-key-entries': [],
          models: [
            {
              name: 'old-model',
              image: true,
              thinking: { effort: 'high' },
            },
          ],
        },
      ],
    });
    mocks.put.mockResolvedValue({});

    await providersApi.saveOpenAIProviders([
      {
        name: 'openai-compatible',
        baseUrl: 'https://api.example.com/v1',
        apiKeyEntries: [],
        models: [{ name: 'new-model', alias: 'new-alias' }],
      },
    ]);

    expect(mocks.put).toHaveBeenLastCalledWith('/openai-compatibility', [
      {
        name: 'openai-compatible',
        'base-url': 'https://api.example.com/v1',
        'api-key-entries': [],
        models: [
          {
            name: 'new-model',
            alias: 'new-alias',
            image: true,
            thinking: { effort: 'high' },
          },
        ],
      },
    ]);
  });
});

it('preserves legacy thinking flags when levels are cleared', async () => {
  mocks.get.mockResolvedValueOnce({
    'openai-compatibility': [
      {
        name: 'openai-compatible',
        'base-url': 'https://api.example.com/v1',
        'api-key-entries': [],
        models: [
          {
            name: 'openai-model',
            thinking: {
              levels: ['high'],
              zero_allowed: true,
              'dynamic-allowed': true,
              future: { enabled: true },
            },
          },
        ],
      },
    ],
  });
  mocks.put.mockResolvedValue({});

  await providersApi.saveOpenAIProviders([
    {
      name: 'openai-compatible',
      baseUrl: 'https://api.example.com/v1',
      apiKeyEntries: [],
      models: [
        markModelThinkingLevelsForClear({
          name: 'openai-model',
          thinking: {
            zero_allowed: true,
            'dynamic-allowed': true,
          },
        }),
      ],
    },
  ]);

  expect(mocks.put).toHaveBeenCalledWith('/openai-compatibility', [
    {
      name: 'openai-compatible',
      'base-url': 'https://api.example.com/v1',
      'api-key-entries': [],
      models: [
        {
          name: 'openai-model',
          thinking: {
            zero_allowed: true,
            'dynamic-allowed': true,
            future: { enabled: true },
          },
        },
      ],
    },
  ]);
});

it('preserves untouched thinking level order during an unrelated save', async () => {
  mocks.get.mockResolvedValueOnce({
    'openai-compatibility': [
      {
        name: 'openai-compatible',
        'base-url': 'https://api.example.com/v1',
        'api-key-entries': [],
        models: [{ name: 'openai-model', thinking: { levels: ['high', 'low'] } }],
      },
    ],
  });
  mocks.put.mockResolvedValue({});

  await providersApi.saveOpenAIProviders([
    {
      name: 'openai-compatible',
      baseUrl: 'https://api.example.com/v2',
      apiKeyEntries: [],
      models: [{ name: 'openai-model', thinking: { levels: ['high', 'low'] } }],
    },
  ]);

  expect(mocks.put).toHaveBeenCalledWith('/openai-compatibility', [
    {
      name: 'openai-compatible',
      'base-url': 'https://api.example.com/v2',
      'api-key-entries': [],
      models: [{ name: 'openai-model', thinking: { levels: ['high', 'low'] } }],
    },
  ]);
});

it('uses custom levels as the authority and removes legacy capability aliases', async () => {
  mocks.get.mockResolvedValueOnce({
    'openai-compatibility': [
      {
        name: 'openai-compatible',
        'base-url': 'https://api.example.com/v1',
        'api-key-entries': [],
        models: [
          {
            name: 'openai-model',
            thinking: {
              levels: ['low'],
              zero_allowed: true,
              'zero-allowed': true,
              zeroAllowed: true,
              dynamic_allowed: true,
              'dynamic-allowed': true,
              dynamicAllowed: true,
              future: { enabled: true },
            },
          },
        ],
      },
    ],
  });
  mocks.put.mockResolvedValue({});

  const model = markModelThinkingLevelsForEdit({
    name: 'openai-model',
    thinking: {
      levels: ['high', 'none', 'auto'],
    },
  });
  expect(hasModelThinkingLevelsEditMarker(model)).toBe(true);

  await providersApi.saveOpenAIProviders([
    {
      name: 'openai-compatible',
      baseUrl: 'https://api.example.com/v1',
      apiKeyEntries: [],
      models: [model],
    },
  ]);

  expect(mocks.put).toHaveBeenCalledWith('/openai-compatibility', [
    {
      name: 'openai-compatible',
      'base-url': 'https://api.example.com/v1',
      'api-key-entries': [],
      models: [
        {
          name: 'openai-model',
          thinking: {
            levels: ['high', 'none', 'auto'],
            future: { enabled: true },
          },
        },
      ],
    },
  ]);
});

it('strips edit markers and legacy aliases when raw config is unavailable', async () => {
  mocks.get.mockRejectedValueOnce(new Error('forbidden'));
  mocks.put.mockResolvedValue({});

  const model = markModelThinkingLevelsForEdit({
    name: 'openai-model',
    thinking: {
      levels: ['high', 'max'],
      zeroAllowed: true,
      'dynamic-allowed': true,
    },
  });
  await providersApi.saveOpenAIProviders([
    {
      name: 'openai-compatible',
      baseUrl: 'https://api.example.com/v1',
      apiKeyEntries: [],
      models: [model],
    },
  ]);

  expect(mocks.put).toHaveBeenCalledWith('/openai-compatibility', [
    {
      name: 'openai-compatible',
      'base-url': 'https://api.example.com/v1',
      'api-key-entries': [],
      models: [{ name: 'openai-model', thinking: { levels: ['high', 'max'] } }],
    },
  ]);
  const writtenModel = (mocks.put.mock.calls[0]?.[1] as Array<{ models?: unknown[] }>)[0]
    ?.models?.[0];
  expect(writtenModel && Reflect.ownKeys(writtenModel)).not.toContain(
    MODEL_THINKING_LEVELS_EDIT_MARKER
  );
});

describe('providersApi optimistic provider mutations', () => {
  it('appends to the latest Gemini list without dropping concurrent records', async () => {
    mocks.get.mockResolvedValueOnce({
      'gemini-api-key': [{ 'api-key': 'concurrent-key', 'raw-field': 'keep' }],
    });
    mocks.put.mockResolvedValue({});

    await providersApi.createGeminiKey({ apiKey: 'new-key' });

    expect(mocks.put).toHaveBeenCalledWith('/gemini-api-key', [
      { 'api-key': 'concurrent-key', 'raw-field': 'keep' },
      { 'api-key': 'new-key' },
    ]);
  });

  it('updates only the matching auth-index record and preserves unrelated records', async () => {
    mocks.get.mockResolvedValueOnce({
      'codex-api-key': [
        { 'auth-index': 'auth-1', 'api-key': 'old', 'raw-field': 'keep' },
        { 'api-key': 'concurrent-key', priority: 9 },
      ],
    });
    mocks.put.mockResolvedValue({});

    await providersApi.updateCodexConfig(
      { apiKey: '', authIndex: 'auth-1' },
      { apiKey: '', authIndex: 'auth-1', prefix: 'updated' }
    );

    expect(mocks.put).toHaveBeenCalledWith('/codex-api-key', [
      { 'raw-field': 'keep', 'auth-index': 'auth-1', prefix: 'updated' },
      { 'api-key': 'concurrent-key', priority: 9 },
    ]);
  });

  it('clears a Vertex cooling override through the update path without dropping raw fields', async () => {
    mocks.get.mockResolvedValueOnce({
      'vertex-api-key': [
        {
          'api-key': 'vertex-key',
          'base-url': 'https://vertex.example.com',
          disable_cooling: true,
          'raw-field': 'keep',
        },
        { 'api-key': 'concurrent-key', priority: 9 },
      ],
    });
    mocks.put.mockResolvedValue({});

    await providersApi.updateVertexConfig(
      { apiKey: 'vertex-key', baseUrl: 'https://vertex.example.com' },
      {
        apiKey: 'vertex-key',
        baseUrl: 'https://vertex.example.com',
        disableCooling: null,
      }
    );

    expect(mocks.put).toHaveBeenCalledWith('/vertex-api-key', [
      {
        'api-key': 'vertex-key',
        'base-url': 'https://vertex.example.com',
        'disable-cooling': null,
        'raw-field': 'keep',
      },
      { 'api-key': 'concurrent-key', priority: 9 },
    ]);
  });

  it('rejects an update when the original provider no longer exists', async () => {
    mocks.get.mockResolvedValueOnce({ 'interactions-api-key': [] });

    await expect(
      providersApi.updateInteractionsKey({ apiKey: 'removed' }, { apiKey: 'updated' })
    ).rejects.toThrow('Provider configuration changed; refresh and try again.');
    expect(mocks.put).not.toHaveBeenCalled();
  });

  it('updates OpenAI by original name and index while preserving concurrent records', async () => {
    mocks.get.mockResolvedValueOnce({
      'openai-compatibility': [
        { name: 'target', 'base-url': 'https://old.example/v1', 'raw-field': 'keep' },
        { name: 'concurrent', 'base-url': 'https://other.example/v1' },
      ],
    });
    mocks.put.mockResolvedValue({});

    await providersApi.updateOpenAIProvider('target', 0, {
      name: 'renamed',
      baseUrl: 'https://new.example/v1',
      apiKeyEntries: [],
    });

    expect(mocks.put).toHaveBeenCalledWith('/openai-compatibility', [
      {
        'raw-field': 'keep',
        name: 'renamed',
        'base-url': 'https://new.example/v1',
        'api-key-entries': [],
      },
      { name: 'concurrent', 'base-url': 'https://other.example/v1' },
    ]);
  });

  it('merges thinking fields on the OpenAI update path', async () => {
    mocks.get.mockResolvedValueOnce({
      'openai-compatibility': [
        {
          name: 'target',
          'base-url': 'https://old.example/v1',
          'api-key-entries': [],
          models: [
            {
              name: 'target-model',
              thinking: {
                levels: ['high'],
                'future-option': { enabled: true },
                nested: { value: 1 },
              },
            },
          ],
        },
      ],
    });
    mocks.put.mockResolvedValue({});

    await providersApi.updateOpenAIProvider('target', 0, {
      name: 'target',
      baseUrl: 'https://new.example/v1',
      apiKeyEntries: [],
      models: [
        {
          name: 'target-model',
          thinking: {
            levels: ['high', 'max'],
          },
        },
      ],
    });

    expect(mocks.put).toHaveBeenCalledWith('/openai-compatibility', [
      {
        name: 'target',
        'base-url': 'https://new.example/v1',
        'api-key-entries': [],
        models: [
          {
            name: 'target-model',
            thinking: {
              levels: ['high', 'max'],
              'future-option': { enabled: true },
              nested: { value: 1 },
            },
          },
        ],
      },
    ]);
  });
});

describe('verifyClaudeFingerprintInRawConfig', () => {
  const relayRecords = [
    {
      'api-key': 'same-key',
      'base-url': 'https://relay.example',
      'proxy-url': 'http://proxy-a',
    },
    {
      'api-key': 'same-key',
      'base-url': 'https://relay.example',
      'proxy-url': 'http://proxy-b',
      'fingerprint-profile': 'claude-code-cli',
    },
  ];

  it('verifies a create against the appended record even with duplicate apiKey + baseUrl', () => {
    expect(
      verifyClaudeFingerprintInRawConfig(relayRecords, 'claude-code-cli', {
        mode: 'create',
        apiKey: 'same-key',
        baseUrl: 'https://relay.example',
      })
    ).toBe('confirmed');
  });

  it('verifies a create against the appended record for header-only duplicates', () => {
    const records = [
      { 'base-url': 'https://relay.example', headers: { 'x-api-key': 'A' } },
      {
        'base-url': 'https://relay.example',
        headers: { 'x-api-key': 'B' },
        'fingerprint-profile': 'claude-code-cli',
      },
    ];
    expect(
      verifyClaudeFingerprintInRawConfig(records, 'claude-code-cli', {
        mode: 'create',
        baseUrl: 'https://relay.example',
      })
    ).toBe('confirmed');
  });

  it('reports not-found when the appended create record fails the identity sanity check', () => {
    expect(
      verifyClaudeFingerprintInRawConfig(
        [{ 'api-key': 'other-key', 'base-url': 'https://relay.example' }],
        'claude-code-cli',
        { mode: 'create', apiKey: 'same-key', baseUrl: 'https://relay.example' }
      )
    ).toBe('not-found');
  });

  it('verifies an edit positionally and falls back to the new identity', () => {
    expect(
      verifyClaudeFingerprintInRawConfig(relayRecords, 'claude-code-cli', {
        mode: 'edit',
        index: 1,
        apiKey: 'same-key',
        baseUrl: 'https://relay.example',
      })
    ).toBe('confirmed');
    expect(
      verifyClaudeFingerprintInRawConfig(
        [
          ...relayRecords,
          {
            'api-key': 'moved-key',
            'base-url': 'https://relay.example',
            'fingerprint-profile': 'claude-code-cli',
          },
        ],
        'claude-code-cli',
        { mode: 'edit', index: 0, apiKey: 'moved-key', baseUrl: 'https://relay.example' }
      )
    ).toBe('confirmed');
  });

  it('only confirms an explicit Default when the raw field is really gone', () => {
    expect(
      verifyClaudeFingerprintInRawConfig(
        [{ 'api-key': 'k', 'base-url': 'https://relay.example' }],
        '',
        { mode: 'edit', index: 0 }
      )
    ).toBe('confirmed');
    expect(
      verifyClaudeFingerprintInRawConfig(
        [
          {
            'api-key': 'k',
            'base-url': 'https://relay.example',
            'fingerprint-profile': 'claude-desktop',
          },
        ],
        '',
        { mode: 'edit', index: 0 }
      )
    ).toBe('not-applied');
  });

  it('does not confirm a CLI request when the raw profile is missing or unknown', () => {
    expect(
      verifyClaudeFingerprintInRawConfig(
        [{ 'api-key': 'k', 'base-url': 'https://relay.example' }],
        'claude-code-cli',
        { mode: 'edit', index: 0 }
      )
    ).toBe('not-applied');
    expect(
      verifyClaudeFingerprintInRawConfig(
        [
          {
            'api-key': 'k',
            'base-url': 'https://relay.example',
            'fingerprint-profile': 'claude-desktop',
          },
        ],
        'claude-code-cli',
        { mode: 'edit', index: 0 }
      )
    ).toBe('not-applied');
  });

  it('accepts the canonical oauth-cli alias in the raw read-back', () => {
    expect(
      verifyClaudeFingerprintInRawConfig(
        [
          {
            'api-key': 'k',
            'base-url': 'https://relay.example',
            'fingerprint-profile': 'oauth-cli',
          },
        ],
        'claude-code-cli',
        { mode: 'edit', index: 0 }
      )
    ).toBe('confirmed');
  });
});

describe('providersApi CPA v8 api-keys support', () => {
  it('loads all 7 provider families from v8 /config/api-keys and falls back to v0 on 404/405', async () => {
    mocks.getRawV8.mockResolvedValueOnce({
      data: {
        gemini: [{ name: 'Gemini Studio', keys: [{ 'api-key': 'AIza-1', auth_index: 'gem-1' }] }],
        interactions: [
          {
            name: 'Interactions Relay',
            'base-url': 'https://interactions.example',
            keys: [{ 'api-key': '' }],
          },
        ],
        codex: [{ name: 'Codex Primary', keys: [{ 'api-key': 'sk-codex-1' }] }],
        xai: [{ name: 'Grok Team', keys: [{ 'api-key': 'xai-1' }] }],
        claude: [{ name: 'Claude Main', keys: [{ 'api-key': 'sk-ant-1' }] }],
        vertex: [{ name: 'Vertex Gateway', keys: [{ 'api-key': 'vk-1' }] }],
        'openai-compatibility': [
          {
            name: 'OpenRouter',
            'base-url': 'https://openrouter.ai/api/v1',
            keys: [{ 'api-key': 'sk-or-1' }],
          },
        ],
      },
      headers: { ETag: '"snapshot-etag-1"' },
    });

    const v8Snapshot = await loadAllProvidersV8Snapshot();
    expect(v8Snapshot.v8Supported).toBe(true);
    expect(v8Snapshot.gemini[0]).toMatchObject({
      name: 'Gemini Studio',
      apiKey: 'AIza-1',
      authIndex: 'gem-1',
      v8Source: { family: 'gemini', etag: '"snapshot-etag-1"' },
    });
    expect(v8Snapshot.interactions[0]).toMatchObject({
      name: 'Interactions Relay',
      apiKey: '',
      baseUrl: 'https://interactions.example',
    });
    expect(v8Snapshot.openai[0]).toMatchObject({
      name: 'OpenRouter',
      baseUrl: 'https://openrouter.ai/api/v1',
    });

    mocks.getRawV8.mockRejectedValueOnce(
      Object.assign(new Error('Method Not Allowed'), { status: 405 })
    );
    mocks.get.mockResolvedValue({});
    const fallbackSnapshot = await providersApi.loadAllProvidersV8Snapshot();
    expect(fallbackSnapshot.v8Supported).toBe(false);
    expect(mocks.get).toHaveBeenCalledWith('/gemini-api-key');
    expect(mocks.get).toHaveBeenCalledWith('/openai-compatibility');
  });

  it('overlays v8 /config/api-keys aliases and authIndex in configApi.getConfig()', async () => {
    mocks.get.mockResolvedValueOnce({
      debug: true,
      'api-keys': ['client-secret-1'],
      'claude-api-key': [{ 'api-key': 'sk-ant-1' }],
    });
    mocks.getRawV8.mockResolvedValueOnce({
      data: {
        claude: [
          {
            name: 'Anthropic Alias',
            keys: [{ 'api-key': 'sk-ant-1', auth_index: 'claude-live-idx' }],
          },
        ],
      },
      headers: { etag: '"cfg-etag-1"' },
    });

    const cfg = await configApi.getConfig();
    expect(cfg.apiKeys).toEqual(['client-secret-1']);
    expect(cfg.claudeApiKeys?.[0]).toMatchObject({
      name: 'Anthropic Alias',
      apiKey: 'sk-ant-1',
      authIndex: 'claude-live-idx',
      v8Source: { family: 'claude', etag: '"cfg-etag-1"' },
    });
  });

  it('round-trips native v8 aliases, strips test-model/auth-index/v8Source, auto-generates lowest <family>-<N>, and preserves unknown fields', async () => {
    const existingGroups = [
      {
        name: 'claude-2',
        'base-url': 'https://api.anthropic.com',
        priority: 4,
        'connection-pool': { enabled: true, 'max-idle-conns-per-host': 8 },
        'request-retry': 2,
        'request-scoped-errors': [{ status: 529, action: 'cooldown' }],
        keys: [
          {
            'api-key': 'sk-ant-existing',
            auth_index: 'claude-idx-existing',
            'experimental-cch-signing': true,
            models: [
              {
                name: 'claude-sonnet-4-5',
                thinking: { levels: ['high'], zero_allowed: true, custom_vendor_flag: 'drop' },
              },
            ],
          },
        ],
      },
    ];

    mocks.getRawV8.mockResolvedValueOnce({
      data: existingGroups,
      headers: { etag: '"etag-read-1"' },
    });
    const loaded = await providersApi.getClaudeConfigs();
    expect(loaded).toHaveLength(1);
    expect(loaded[0].name).toBe('claude-2');

    // Create without name should pick claude-1 (lowest unused integer, since claude-2 exists).
    mocks.getRawV8.mockResolvedValueOnce({
      data: existingGroups,
      headers: { etag: '"etag-create-1"' },
    });
    mocks.putV8.mockResolvedValueOnce({ status: 'ok' });

    await providersApi.createClaudeConfig({
      apiKey: 'sk-ant-new',
      authIndex: 'should-not-serialize',
      models: [{ name: 'claude-haiku-4-5', alias: 'haiku', testModel: 'claude-3-haiku' }],
    });

    expect(mocks.putV8).toHaveBeenCalledWith(
      '/config/api-keys/claude',
      [
        {
          name: 'claude-2',
          'base-url': 'https://api.anthropic.com',
          priority: 4,
          'connection-pool': { enabled: true, 'max-idle-conns-per-host': 8 },
          'request-retry': 2,
          'request-scoped-errors': [{ status: 529, action: 'cooldown' }],
          keys: [
            {
              'api-key': 'sk-ant-existing',
              'experimental-cch-signing': true,
              models: [
                {
                  name: 'claude-sonnet-4-5',
                  thinking: { levels: ['high'], 'zero-allowed': true },
                },
              ],
            },
          ],
        },
        {
          name: 'claude-1',
          keys: [
            {
              'api-key': 'sk-ant-new',
              models: [{ name: 'claude-haiku-4-5', alias: 'haiku' }],
            },
          ],
        },
      ],
      { headers: { 'If-Match': '"etag-create-1"' } }
    );

    // Update existing group: rename to 'Anthropic Production', clear thinking.levels via marker,
    // preserve group connection-pool/request-retry and member experimental-cch-signing.
    mocks.getRawV8.mockResolvedValueOnce({
      data: existingGroups,
      headers: { etag: '"etag-read-1"' },
    });
    mocks.putV8.mockResolvedValueOnce({ status: 'ok' });

    const clearedModel = markModelThinkingLevelsForClear({
      name: 'claude-sonnet-4-5',
      testModel: 'ui-only-probe',
      thinking: { levels: ['high'], 'zero-allowed': true, custom_vendor_flag: 'drop' },
    });
    await providersApi.updateClaudeConfig(loaded[0], {
      ...loaded[0],
      name: 'Anthropic Production',
      priority: 9,
      models: [clearedModel],
    });

    expect(mocks.putV8).toHaveBeenLastCalledWith(
      '/config/api-keys/claude',
      [
        {
          name: 'Anthropic Production',
          'base-url': 'https://api.anthropic.com',
          'connection-pool': { enabled: true, 'max-idle-conns-per-host': 8 },
          'request-retry': 2,
          'request-scoped-errors': [{ status: 529, action: 'cooldown' }],
          priority: 9,
          keys: [
            {
              'api-key': 'sk-ant-existing',
              'experimental-cch-signing': true,
              models: [
                {
                  name: 'claude-sonnet-4-5',
                  thinking: { 'zero-allowed': true },
                },
              ],
            },
          ],
        },
      ],
      { headers: { 'If-Match': '"etag-read-1"' } }
    );
  });

  it('preserves sibling keys on multi-key native v8 groups, splits when name/baseUrl changes, and deletes single members cleanly', async () => {
    const multiKeyGroups = [
      {
        name: 'Codex Shared',
        'base-url': 'https://codex.relay.example/v1',
        priority: 3,
        'connection-pool': { enabled: true },
        keys: [
          { 'api-key': 'sk-codex-1', weight: 1, auth_index: 'idx-1' },
          { 'api-key': 'sk-codex-2', weight: 2, 'request-retry': 4, auth_index: 'idx-2' },
        ],
      },
    ];

    mocks.getRawV8.mockResolvedValueOnce({
      data: multiKeyGroups,
      headers: { etag: '"etag-multi-read"' },
    });
    const loaded = await providersApi.getCodexConfigs();
    expect(loaded).toHaveLength(2);

    // In-place member edit (neither name nor baseUrl changed)
    mocks.getRawV8.mockResolvedValueOnce({
      data: multiKeyGroups,
      headers: { etag: '"etag-multi-read"' },
    });
    mocks.putV8.mockResolvedValueOnce({ status: 'ok' });
    await providersApi.updateCodexConfig(loaded[1], {
      ...loaded[1],
      weight: 6,
    });
    expect(mocks.putV8).toHaveBeenLastCalledWith(
      '/config/api-keys/codex',
      [
        {
          name: 'Codex Shared',
          'base-url': 'https://codex.relay.example/v1',
          priority: 3,
          'connection-pool': { enabled: true },
          keys: [
            { 'api-key': 'sk-codex-1', weight: 1 },
            { 'api-key': 'sk-codex-2', weight: 6, 'request-retry': 4 },
          ],
        },
      ],
      { headers: { 'If-Match': '"etag-multi-read"' } }
    );

    // Split member when baseUrl changes (disambiguates colliding group name to Codex Shared-2)
    mocks.getRawV8.mockResolvedValueOnce({
      data: multiKeyGroups,
      headers: { etag: '"etag-multi-read"' },
    });
    mocks.putV8.mockResolvedValueOnce({ status: 'ok' });
    await providersApi.updateCodexConfig(loaded[1], {
      ...loaded[1],
      baseUrl: 'https://dedicated.codex.example/v1',
    });
    expect(mocks.putV8).toHaveBeenLastCalledWith(
      '/config/api-keys/codex',
      [
        {
          name: 'Codex Shared',
          'base-url': 'https://codex.relay.example/v1',
          priority: 3,
          'connection-pool': { enabled: true },
          keys: [{ 'api-key': 'sk-codex-1', weight: 1 }],
        },
        {
          name: 'Codex Shared-2',
          'base-url': 'https://dedicated.codex.example/v1',
          'connection-pool': { enabled: true },
          priority: 3,
          keys: [{ 'api-key': 'sk-codex-2', weight: 2, 'request-retry': 4 }],
        },
      ],
      { headers: { 'If-Match': '"etag-multi-read"' } }
    );

    // Deleting 1 member keeps sibling; deleting last member removes group
    mocks.getRawV8.mockResolvedValueOnce({
      data: multiKeyGroups,
      headers: { etag: '"etag-multi-del-1"' },
    });
    mocks.putV8.mockResolvedValueOnce({ status: 'ok' });
    await providersApi.deleteCodexConfig('sk-codex-1', 'https://codex.relay.example/v1');
    expect(mocks.putV8).toHaveBeenLastCalledWith(
      '/config/api-keys/codex',
      [
        {
          name: 'Codex Shared',
          'base-url': 'https://codex.relay.example/v1',
          priority: 3,
          'connection-pool': { enabled: true },
          keys: [{ 'api-key': 'sk-codex-2', weight: 2, 'request-retry': 4 }],
        },
      ],
      { headers: { 'If-Match': '"etag-multi-del-1"' } }
    );
  });

  it('round-trips openai-compatibility v8 groups while stripping test-model/auth-index and preserving keyless entries', async () => {
    const rawOpenAIGroups = [
      {
        name: 'Gateway Alpha',
        'base-url': 'https://alpha.example/v1',
        'support-prompt-cache-key': true,
        'connection-pool': { enabled: true },
        auth_index: 'oa-group-idx',
        models: [{ name: 'gpt-4o', alias: '4o' }],
        keys: [
          {
            'api-key': '',
            'proxy-url': 'http://proxy:8080',
            'connection-pool': { 'max-idle-conns-per-host': 4 },
            auth_index: 'oa-key-idx',
          },
        ],
      },
    ];

    mocks.getRawV8.mockResolvedValueOnce({
      data: rawOpenAIGroups,
      headers: { etag: '"etag-oa-1"' },
    });
    const loaded = await providersApi.getOpenAIProviders();
    expect(loaded).toHaveLength(1);

    mocks.getRawV8.mockResolvedValueOnce({
      data: rawOpenAIGroups,
      headers: { etag: '"etag-oa-1"' },
    });
    mocks.putV8.mockResolvedValueOnce({ status: 'ok' });

    await providersApi.updateOpenAIProvider('Gateway Alpha', 0, {
      ...loaded[0],
      name: 'Gateway Renamed',
      testModel: 'gpt-4o-mini',
      models: [{ name: 'gpt-4o', alias: '4o', testModel: 'gpt-4o-mini' }],
    });

    expect(mocks.putV8).toHaveBeenLastCalledWith(
      '/config/api-keys/openai-compatibility',
      [
        {
          name: 'Gateway Renamed',
          'base-url': 'https://alpha.example/v1',
          'support-prompt-cache-key': true,
          'connection-pool': { enabled: true },
          models: [{ name: 'gpt-4o', alias: '4o' }],
          keys: [
            {
              'api-key': '',
              'proxy-url': 'http://proxy:8080',
              'connection-pool': { 'max-idle-conns-per-host': 4 },
            },
          ],
        },
      ],
      { headers: { 'If-Match': '"etag-oa-1"' } }
    );
  });

  it('writes first v8 group when empty family returns 404 not_found and falls back to v0 delete on v8 404', async () => {
    mocks.getRawV8.mockRejectedValueOnce(
      Object.assign(new Error('not_found'), {
        status: 404,
        details: { error: 'not_found' },
      })
    );
    mocks.putV8.mockResolvedValueOnce({ status: 'ok' });

    await providersApi.createVertexConfig({
      name: 'Vertex First',
      apiKey: 'vk-first-1',
      baseUrl: 'https://vertex.example/v1',
    });

    expect(mocks.putV8).toHaveBeenCalledWith(
      '/config/api-keys/vertex',
      [
        {
          name: 'Vertex First',
          'base-url': 'https://vertex.example/v1',
          keys: [{ 'api-key': 'vk-first-1' }],
        },
      ],
      undefined
    );

    // Delete on v8 removes last member's group; delete on 404 falls back to v0 DELETE
    mocks.getRawV8.mockResolvedValueOnce({
      data: [{ name: 'OpenRouter', 'base-url': 'https://openrouter.ai/api/v1', keys: [] }],
      headers: { etag: '"etag-del-oa"' },
    });
    mocks.putV8.mockResolvedValueOnce({ status: 'ok' });
    await providersApi.deleteOpenAIProvider('OpenRouter');
    expect(mocks.putV8).toHaveBeenLastCalledWith(
      '/config/api-keys/openai-compatibility',
      [],
      { headers: { 'If-Match': '"etag-del-oa"' } }
    );

    mocks.delete.mockResolvedValueOnce({ status: 'ok' });
    await providersApi.deleteClaudeConfig('sk-ant-legacy', 'https://api.anthropic.com');
    expect(mocks.delete).toHaveBeenCalledWith(
      '/claude-api-key?api-key=sk-ant-legacy&base-url=https%3A%2F%2Fapi.anthropic.com'
    );
  });

  it('treats v8 404 not_found as empty v8 state without calling v0 endpoints across snapshot, get, and delete', async () => {
    const notFoundError = () =>
      Object.assign(new Error('not_found'), {
        status: 404,
        details: { error: 'not_found' },
      });

    mocks.getRawV8.mockRejectedValueOnce(notFoundError());
    const snapshot = await providersApi.loadAllProvidersV8Snapshot();
    expect(snapshot).toEqual({
      gemini: [],
      interactions: [],
      codex: [],
      xai: [],
      claude: [],
      vertex: [],
      meta: [],
      openai: [],
      v8Supported: true,
    });

    mocks.getRawV8.mockRejectedValueOnce(notFoundError());
    await expect(providersApi.getClaudeConfigs()).resolves.toEqual([]);

    mocks.getRawV8.mockRejectedValueOnce(notFoundError());
    await expect(providersApi.getOpenAIProviders()).resolves.toEqual([]);

    mocks.getRawV8.mockRejectedValueOnce(notFoundError());
    await expect(
      providersApi.deleteClaudeConfig('sk-ant-missing', 'https://api.anthropic.com')
    ).resolves.toBeUndefined();

    mocks.getRawV8.mockRejectedValueOnce(notFoundError());
    await expect(providersApi.deleteOpenAIProvider('MissingOpenAI')).resolves.toBeUndefined();

    expect(mocks.get).not.toHaveBeenCalled();
    expect(mocks.delete).not.toHaveBeenCalled();
    expect(mocks.putV8).not.toHaveBeenCalled();
  });

  it('enforces optimistic concurrency If-Match checks and throws 412 before PUT on stale caller ETag', async () => {
    mocks.getRawV8.mockResolvedValueOnce({
      data: [{ name: 'Claude Main', keys: [{ 'api-key': 'sk-ant-1' }] }],
      headers: { etag: '"server-etag-2"' },
    });
    await expect(
      providersApi.updateClaudeConfig(
        {
          name: 'Claude Main',
          apiKey: 'sk-ant-1',
          v8Source: {
            family: 'claude',
            groupIndex: 0,
            keyIndex: 0,
            group: { name: 'Claude Main', keys: [{ 'api-key': 'sk-ant-1' }] },
            etag: '"stale-etag-1"',
          },
        },
        { name: 'Claude Renamed', apiKey: 'sk-ant-1' }
      )
    ).rejects.toMatchObject({
      status: 412,
      details: { error: 'precondition_failed' },
    });

    mocks.getRawV8.mockResolvedValueOnce({
      data: [{ name: 'OpenRouter', 'base-url': 'https://openrouter.ai/api/v1', keys: [] }],
      headers: { etag: '"oa-etag-2"' },
    });
    await expect(
      providersApi.updateOpenAIProvider(
        'OpenRouter',
        0,
        {
          name: 'OpenRouter Next',
          baseUrl: 'https://openrouter.ai/api/v1',
          apiKeyEntries: [],
          v8Source: {
            family: 'openai-compatibility',
            groupIndex: 0,
            group: { name: 'OpenRouter' },
            etag: '"oa-etag-1"',
          },
        }
      )
    ).rejects.toMatchObject({ status: 412, details: { error: 'precondition_failed' } });

    mocks.getRawV8.mockResolvedValueOnce({
      data: [{ name: 'Claude Main', keys: [{ 'api-key': 'sk-ant-1' }] }],
      headers: { etag: '"del-etag-2"' },
    });
    await expect(
      providersApi.deleteClaudeConfig('sk-ant-1', undefined, {
        family: 'claude',
        groupIndex: 0,
        keyIndex: 0,
        group: { name: 'Claude Main', keys: [{ 'api-key': 'sk-ant-1' }] },
        etag: '"del-etag-1"',
      })
    ).rejects.toMatchObject({ status: 412 });

    mocks.getRawV8.mockResolvedValueOnce({
      data: [{ name: 'OpenRouter', 'base-url': 'https://openrouter.ai/api/v1', keys: [] }],
      headers: { etag: '"del-oa-2"' },
    });
    await expect(
      providersApi.deleteOpenAIProvider('OpenRouter', 0, { expectedEtag: '"del-oa-1"' })
    ).rejects.toMatchObject({ status: 412 });

    mocks.getRawV8.mockResolvedValueOnce({
      data: [{ name: 'Codex Main', keys: [{ 'api-key': 'sk-codex-1' }] }],
      headers: { etag: '"save-etag-2"' },
    });
    await expect(
      providersApi.saveCodexConfigs(
        [{ name: 'Codex Main', apiKey: 'sk-codex-1' }],
        { expectedEtag: '"save-etag-1"' }
      )
    ).rejects.toMatchObject({ status: 412 });

    mocks.getRawV8.mockResolvedValueOnce({
      data: [{ name: 'OpenRouter', 'base-url': 'https://openrouter.ai/api/v1', keys: [] }],
      headers: { etag: '"save-oa-2"' },
    });
    await expect(
      providersApi.saveOpenAIProviders(
        [{ name: 'OpenRouter', baseUrl: 'https://openrouter.ai/api/v1', apiKeyEntries: [] }],
        { expectedEtag: '"save-oa-1"' }
      )
    ).rejects.toMatchObject({ status: 412 });

    mocks.getRawV8.mockResolvedValueOnce({
      data: [{ name: 'Codex Main', keys: [{ 'api-key': 'sk-codex-1' }] }],
      headers: { etag: '"create-etag-2"' },
    });
    await expect(
      providersApi.createCodexConfig(
        { name: 'Codex Added', apiKey: 'sk-codex-2' },
        { expectedEtag: '"create-etag-1"' }
      )
    ).rejects.toMatchObject({ status: 412 });

    expect(mocks.putV8).not.toHaveBeenCalled();

    // When family GET returns 404 not_found on create, caller expectedEtag is still forwarded in If-Match
    mocks.getRawV8.mockRejectedValueOnce(
      Object.assign(new Error('not_found'), {
        status: 404,
        details: { error: 'not_found' },
      })
    );
    mocks.putV8.mockResolvedValueOnce({ status: 'ok' });
    await providersApi.createCodexConfig(
      { name: 'Codex First', apiKey: 'sk-codex-first' },
      { expectedEtag: '"root-etag-0"' }
    );
    expect(mocks.putV8).toHaveBeenCalledWith(
      '/config/api-keys/codex',
      [{ name: 'Codex First', keys: [{ 'api-key': 'sk-codex-first' }] }],
      { headers: { 'If-Match': '"root-etag-0"' } }
    );
  });

  it('aligns v8 payloads with CPA KnownFields(true) for Claude cloak, Vertex models, OpenAI keys, and thinking keys', async () => {
    // 1. Claude cloak: preserves empty cloak {} and all CPA CloakConfig fields while stripping unknown keys; removes when disabled
    const rawClaudeGroup = {
      name: 'Claude Cloak',
      keys: [
        {
          'api-key': 'sk-ant-cloak',
          cloak: {
            strictMode: true,
            sensitiveWords: ['internal-token'],
            'cache-user-id': true,
            'sensitive-words-replace': { alpha: 'beta' },
            'sensitive-patterns-replace': [{ pattern: '^foo$', replacement: '' }],
            'x-custom-cloak': 'drop-me',
          },
          models: [
            {
              name: 'claude-sonnet-4-5',
              alias: 'sonnet',
              priority: 9,
              image: true,
              'input-modalities': ['text', 'image'],
              'output-modalities': ['text'],
              'use-max-completion-tokens': true,
              thinking: {
                min: 1024,
                max: 8192,
                zero_allowed: true,
                dynamicAllowed: false,
                disable_type: 'none',
              },
            },
          ],
        },
      ],
    };
    mocks.getRawV8.mockResolvedValueOnce({
      data: [rawClaudeGroup],
      headers: { etag: '"claude-schema-1"' },
    });
    const [loadedClaude] = await providersApi.getClaudeConfigs();

    mocks.getRawV8.mockResolvedValueOnce({
      data: [rawClaudeGroup],
      headers: { etag: '"claude-schema-1"' },
    });
    mocks.putV8.mockResolvedValueOnce({ status: 'ok' });
    await providersApi.updateClaudeConfig(loadedClaude, {
      ...loadedClaude,
      cloak: { ...loadedClaude.cloak, mode: 'strict' },
    });
    expect(mocks.putV8).toHaveBeenLastCalledWith(
      '/config/api-keys/claude',
      [
        {
          name: 'Claude Cloak',
          keys: [
            {
              'api-key': 'sk-ant-cloak',
              models: [
                {
                  name: 'claude-sonnet-4-5',
                  alias: 'sonnet',
                  thinking: {
                    min: 1024,
                    max: 8192,
                    'zero-allowed': true,
                    'dynamic-allowed': false,
                    'disable-type': 'none',
                  },
                },
              ],
              cloak: {
                mode: 'strict',
                'strict-mode': true,
                'sensitive-words': ['internal-token'],
                'cache-user-id': true,
                'sensitive-words-replace': { alpha: 'beta' },
                'sensitive-patterns-replace': [{ pattern: '^foo$', replacement: '' }],
              },
            },
          ],
        },
      ],
      { headers: { 'If-Match': '"claude-schema-1"' } }
    );

    // Empty cloak {} preserved, and clearing cloak deletes cloak
    mocks.getRawV8.mockResolvedValueOnce({
      data: [{ name: 'Claude Empty Cloak', keys: [{ 'api-key': 'sk-ant-empty', cloak: {} }] }],
      headers: { etag: '"claude-empty-1"' },
    });
    const [loadedEmptyCloak] = await providersApi.getClaudeConfigs();
    expect(loadedEmptyCloak.cloak).toEqual({});

    mocks.getRawV8.mockResolvedValueOnce({
      data: [{ name: 'Claude Empty Cloak', keys: [{ 'api-key': 'sk-ant-empty', cloak: {} }] }],
      headers: { etag: '"claude-empty-1"' },
    });
    mocks.putV8.mockResolvedValueOnce({ status: 'ok' });
    await providersApi.updateClaudeConfig(loadedEmptyCloak, { ...loadedEmptyCloak });
    expect(mocks.putV8).toHaveBeenLastCalledWith(
      '/config/api-keys/claude',
      [{ name: 'Claude Empty Cloak', keys: [{ 'api-key': 'sk-ant-empty', cloak: {} }] }],
      { headers: { 'If-Match': '"claude-empty-1"' } }
    );

    mocks.getRawV8.mockResolvedValueOnce({
      data: [{ name: 'Claude Empty Cloak', keys: [{ 'api-key': 'sk-ant-empty', cloak: {} }] }],
      headers: { etag: '"claude-empty-2"' },
    });
    mocks.putV8.mockResolvedValueOnce({ status: 'ok' });
    await providersApi.updateClaudeConfig(
      { ...loadedEmptyCloak, v8Source: { ...loadedEmptyCloak.v8Source!, etag: '"claude-empty-2"' } },
      { name: 'Claude Empty Cloak', apiKey: 'sk-ant-empty', cloak: undefined }
    );
    expect(mocks.putV8).toHaveBeenLastCalledWith(
      '/config/api-keys/claude',
      [{ name: 'Claude Empty Cloak', keys: [{ 'api-key': 'sk-ant-empty' }] }],
      { headers: { 'If-Match': '"claude-empty-2"' } }
    );

    // 2. Vertex models without alias round-trip with display-name, force-mapping, openapi, and thinking while stripping non-Vertex and unknown fields
    const rawVertexGroup = {
      name: 'Vertex Pro',
      keys: [
        {
          'api-key': 'vk-1',
          models: [
            {
              name: 'gemini-2.5-pro',
              'display-name': 'Gemini 2.5 Pro',
              'force-mapping': true,
              openapi: true,
              'max-context-length': 1048576,
              'is-compat': true,
              'support-configuration-update': true,
              priority: 5,
              thinking: { min: 0, max: 32768, zeroAllowed: true },
              'x-vertex-extra': 'drop-me',
            },
          ],
        },
      ],
    };
    mocks.getRawV8.mockResolvedValueOnce({
      data: [rawVertexGroup],
      headers: { etag: '"vx-1"' },
    });
    const [loadedVertex] = await providersApi.getVertexConfigs();
    expect(loadedVertex.models).toHaveLength(1);

    mocks.getRawV8.mockResolvedValueOnce({
      data: [rawVertexGroup],
      headers: { etag: '"vx-1"' },
    });
    mocks.putV8.mockResolvedValueOnce({ status: 'ok' });
    await providersApi.updateVertexConfig(loadedVertex, loadedVertex);
    expect(mocks.putV8).toHaveBeenLastCalledWith(
      '/config/api-keys/vertex',
      [
        {
          name: 'Vertex Pro',
          keys: [
            {
              'api-key': 'vk-1',
              models: [
                {
                  name: 'gemini-2.5-pro',
                  'display-name': 'Gemini 2.5 Pro',
                  'force-mapping': true,
                  openapi: true,
                  thinking: { min: 0, max: 32768, 'zero-allowed': true },
                },
              ],
            },
          ],
        },
      ],
      { headers: { 'If-Match': '"vx-1"' } }
    );

    // 3. openai-compatibility keys[] strips headers and unknown fields, models[] strips priority
    mocks.getRawV8.mockResolvedValueOnce({
      data: [
        {
          name: 'OpenAI Strict',
          'base-url': 'https://api.example/v1',
          models: [{ name: 'gpt-4.1', alias: '4.1', priority: 3, image: true }],
          keys: [
            {
              'api-key': 'sk-oa-1',
              weight: 2,
              'proxy-url': 'http://proxy:8080',
              headers: { 'X-Drop-Me': '1' },
              'x-unknown-key-field': 'drop-me',
              'connection-pool': { 'max-idle-conns-per-host': 8 },
            },
          ],
        },
      ],
      headers: { etag: '"oa-strict-1"' },
    });
    const [loadedOpenAI] = await providersApi.getOpenAIProviders();
    mocks.getRawV8.mockResolvedValueOnce({
      data: [loadedOpenAI.v8Source!.group],
      headers: { etag: '"oa-strict-1"' },
    });
    mocks.putV8.mockResolvedValueOnce({ status: 'ok' });
    await providersApi.updateOpenAIProvider('OpenAI Strict', 0, loadedOpenAI);
    expect(mocks.putV8).toHaveBeenLastCalledWith(
      '/config/api-keys/openai-compatibility',
      [
        {
          name: 'OpenAI Strict',
          'base-url': 'https://api.example/v1',
          models: [{ name: 'gpt-4.1', alias: '4.1', image: true }],
          keys: [
            {
              'api-key': 'sk-oa-1',
              weight: 2,
              'proxy-url': 'http://proxy:8080',
              'connection-pool': { 'max-idle-conns-per-host': 8 },
            },
          ],
        },
      ],
      { headers: { 'If-Match': '"oa-strict-1"' } }
    );
  });

  it('maintains multi-key native v8 group integrity on middle-member split and member disable-cooling reset', async () => {
    const rawPoolGroup = {
      name: 'Pool',
      'base-url': 'https://pool.example/v1',
      'disable-cooling': true,
      keys: [
        { 'api-key': 'k1', auth_index: 'idx-1' },
        { 'api-key': 'k2', auth_index: 'idx-2' },
        { 'api-key': 'k3', auth_index: 'idx-3' },
      ],
    };
    mocks.getRawV8.mockResolvedValueOnce({
      data: [rawPoolGroup],
      headers: { etag: '"pool-etag-1"' },
    });
    const poolItems = await providersApi.getCodexConfigs();
    expect(poolItems).toHaveLength(3);

    // Rename middle member k2 and reset k3 disableCooling to null (inherit global)
    mocks.getRawV8.mockResolvedValueOnce({
      data: [rawPoolGroup],
      headers: { etag: '"pool-etag-1"' },
    });
    mocks.putV8.mockResolvedValueOnce({ status: 'ok' });
    await providersApi.saveCodexConfigs([
      poolItems[0],
      { ...poolItems[1], name: 'Dedicated K2', baseUrl: 'https://dedicated.example/v1' },
      { ...poolItems[2], disableCooling: null },
    ]);

    expect(mocks.putV8).toHaveBeenLastCalledWith(
      '/config/api-keys/codex',
      [
        {
          name: 'Pool',
          'base-url': 'https://pool.example/v1',
          keys: [
            { 'api-key': 'k1', 'disable-cooling': true },
            { 'api-key': 'k3' },
          ],
        },
        {
          name: 'Dedicated K2',
          'base-url': 'https://dedicated.example/v1',
          'disable-cooling': true,
          keys: [{ 'api-key': 'k2' }],
        },
      ],
      { headers: { 'If-Match': '"pool-etag-1"' } }
    );

    // Member-level distinct base-url in raw multi-key group splits cleanly without leaving base-url on keys[]
    const rawMemberBaseUrlGroup = {
      name: 'HybridPool',
      'base-url': 'https://pool.example/v1',
      prefix: 'hp',
      keys: [
        { 'api-key': 'hk-1' },
        { 'api-key': 'hk-2', 'base-url': 'https://override.example/v1' },
        { 'api-key': 'hk-3' },
      ],
    };
    mocks.getRawV8.mockResolvedValueOnce({
      data: [rawMemberBaseUrlGroup],
      headers: { etag: '"hybrid-etag-1"' },
    });
    mocks.putV8.mockResolvedValueOnce({ status: 'ok' });
    await providersApi.deleteCodexConfig('hk-3', 'https://pool.example/v1');
    expect(mocks.putV8).toHaveBeenLastCalledWith(
      '/config/api-keys/codex',
      [
        {
          name: 'HybridPool',
          'base-url': 'https://pool.example/v1',
          prefix: 'hp',
          keys: [{ 'api-key': 'hk-1' }],
        },
        {
          name: 'HybridPool-2',
          'base-url': 'https://override.example/v1',
          prefix: 'hp',
          keys: [{ 'api-key': 'hk-2' }],
        },
      ],
      { headers: { 'If-Match': '"hybrid-etag-1"' } }
    );
  });

  it('captures and propagates latest v8 ETag across GET, putRawV8 PUT, empty-family create, and v0 fallback', async () => {
    const putRawV8Mock = vi.fn();
    Reflect.set(apiClient, 'putRawV8', putRawV8Mock);

    mocks.getRawV8.mockResolvedValueOnce({
      data: {
        claude: [{ name: 'Claude 1', keys: [{ 'api-key': 'sk-c1' }] }],
      },
      headers: { etag: '"etag-root-1"' },
    });

    const snapshot = await loadAllProvidersV8Snapshot();
    expect(snapshot.v8Supported).toBe(true);
    expect(providersApi.getLatestV8ConfigEtag()).toBe('"etag-root-1"');

    // Unscoped empty-family GET returning 404 not_found with no ETag header omits stale module-global
    // If-Match so non-provider config.yaml edits cannot trigger false 412s, while putRawV8 advances ETag.
    mocks.getRawV8.mockRejectedValueOnce(
      Object.assign(new Error('not_found'), {
        status: 404,
        details: { error: 'not_found' },
      })
    );
    putRawV8Mock.mockResolvedValueOnce({
      data: { status: 'ok' },
      headers: { etag: '"etag-after-put-2"' },
    });

    const createResult = await providersApi.createCodexConfig({
      name: 'Codex First',
      apiKey: 'sk-codex-1',
      baseUrl: 'https://codex.example/v1',
    });

    expect(putRawV8Mock).toHaveBeenCalledWith(
      '/config/api-keys/codex',
      [
        {
          name: 'Codex First',
          'base-url': 'https://codex.example/v1',
          keys: [{ 'api-key': 'sk-codex-1' }],
        },
      ],
      undefined
    );
    expect(createResult).toEqual({ status: 'ok', etag: '"etag-after-put-2"' });
    expect(providersApi.getLatestV8ConfigEtag()).toBe('"etag-after-put-2"');

    // Explicit caller expectedEtag (cross-family batch commit) still sends If-Match on missing family.
    mocks.getRawV8.mockRejectedValueOnce(
      Object.assign(new Error('not_found'), {
        status: 404,
        details: { error: 'not_found' },
      })
    );
    putRawV8Mock.mockResolvedValueOnce({
      data: { status: 'ok' },
      headers: { etag: '"etag-after-put-3"' },
    });
    await providersApi.createCodexConfig(
      {
        name: 'Codex Batch',
        apiKey: 'sk-codex-2',
        baseUrl: 'https://codex.example/v1',
      },
      { expectedEtag: '"etag-after-put-2"' }
    );
    expect(putRawV8Mock).toHaveBeenLastCalledWith(
      '/config/api-keys/codex',
      [
        {
          name: 'Codex Batch',
          'base-url': 'https://codex.example/v1',
          keys: [{ 'api-key': 'sk-codex-2' }],
        },
      ],
      { headers: { 'If-Match': '"etag-after-put-2"' } }
    );

    // Missing-family 404 not_found carrying an error ETag header forwards that ETag without callerEtag.
    mocks.getRawV8.mockRejectedValueOnce(
      Object.assign(new Error('not_found'), {
        status: 404,
        details: { error: 'not_found' },
        headers: { etag: 'W/"etag-from-404"' },
      })
    );
    putRawV8Mock.mockResolvedValueOnce({
      data: { status: 'ok' },
      headers: { etag: '"etag-after-put-4"' },
    });
    await providersApi.saveOpenAIProviders([
      {
        name: 'OA First',
        baseUrl: 'https://oa.example/v1',
        apiKeyEntries: [{ apiKey: 'sk-oa-first' }],
      },
    ]);
    expect(putRawV8Mock).toHaveBeenLastCalledWith(
      '/config/api-keys/openai-compatibility',
      [
        {
          name: 'OA First',
          'base-url': 'https://oa.example/v1',
          keys: [{ 'api-key': 'sk-oa-first' }],
        },
      ],
      { headers: { 'If-Match': '"etag-from-404"' } }
    );
    // Manual setter normalizes whitespace and v0 fallback clears latestV8ConfigEtag.
    providersApi.setLatestV8ConfigEtag('  "etag-manual-3"  ');
    expect(providersApi.getLatestV8ConfigEtag()).toBe('"etag-manual-3"');

    mocks.getRawV8.mockRejectedValueOnce(Object.assign(new Error('Not Found'), { status: 404 }));
    mocks.get.mockResolvedValueOnce({ 'codex-api-key': [] });
    await providersApi.getCodexConfigs();
    expect(providersApi.getLatestV8ConfigEtag()).toBeUndefined();
  });

  it('retains empty member overrides across a second edit round-trip on a multi-key native group', async () => {
    const initialGroup = {
      name: 'Shared Claude Pool',
      'base-url': 'https://claude.pool.example',
      priority: 5,
      prefix: 'team-shared',
      'proxy-url': 'http://corp-proxy.example:8080',
      headers: { 'X-Team': 'platform' },
      models: [{ name: 'claude-sonnet-4-5', alias: 'sonnet' }],
      'excluded-models': ['claude-instant-*'],
      keys: [
        { 'api-key': 'sk-member-0', weight: 1 },
        { 'api-key': 'sk-member-1', weight: 2 },
      ],
    };

    mocks.getRawV8.mockResolvedValueOnce({
      data: [initialGroup],
      headers: { etag: '"etag-rt-1"' },
    });
    const loaded1 = await providersApi.getClaudeConfigs();
    expect(loaded1).toHaveLength(2);

    // Edit #1: clear prefix, proxyUrl, headers, models, excludedModels, and set priority=0 on member 0.
    mocks.getRawV8.mockResolvedValueOnce({
      data: [initialGroup],
      headers: { etag: '"etag-rt-1"' },
    });
    mocks.putV8.mockResolvedValueOnce({ status: 'ok' });

    await providersApi.updateClaudeConfig(loaded1[0], {
      ...loaded1[0],
      priority: 0,
      prefix: '',
      proxyUrl: '',
      headers: {},
      models: [],
      excludedModels: [],
    });

    const [, savedAfterEdit1] = mocks.putV8.mock.calls[0] as [
      string,
      Array<Record<string, unknown>>,
    ];
    expect(savedAfterEdit1[0]?.keys).toEqual([
      {
        'api-key': 'sk-member-0',
        weight: 1,
        priority: 0,
        prefix: '',
        'proxy-url': '',
        headers: {},
        models: [],
        'excluded-models': [],
      },
      { 'api-key': 'sk-member-1', weight: 2 },
    ]);

    // Reload after Edit #1 via normalizeV8ProviderGroups, then Edit #2 changes ONLY weight on member 0.
    mocks.getRawV8.mockResolvedValueOnce({
      data: savedAfterEdit1,
      headers: { etag: '"etag-rt-2"' },
    });
    const reloaded = await providersApi.getClaudeConfigs();
    expect(reloaded[0]?.prefix).toBeUndefined();
    expect(reloaded[0]?.headers).toBeUndefined();
    expect(reloaded[0]?.models).toBeUndefined();
    expect(reloaded[0]?.excludedModels).toBeUndefined();

    mocks.getRawV8.mockResolvedValueOnce({
      data: savedAfterEdit1,
      headers: { etag: '"etag-rt-2"' },
    });
    mocks.putV8.mockResolvedValueOnce({ status: 'ok' });

    await providersApi.updateClaudeConfig(reloaded[0], {
      ...reloaded[0],
      weight: 9,
    });

    const [, savedAfterEdit2] = mocks.putV8.mock.calls[1] as [
      string,
      Array<Record<string, unknown>>,
    ];
    expect(savedAfterEdit2[0]).toEqual({
      ...initialGroup,
      keys: [
        {
          'api-key': 'sk-member-0',
          weight: 9,
          priority: 0,
          prefix: '',
          'proxy-url': '',
          headers: {},
          models: [],
          'excluded-models': [],
        },
        { 'api-key': 'sk-member-1', weight: 2 },
      ],
    });
  });

  it('persists subset deletions and baseUrl updates cleanly in saveNativeFamilyV8First', async () => {
    // Case 1: 3-key group [k1, k2, k3] -> delete k1, keep [k2, k3] in same group.
    const threeKeyGroup = {
      name: 'Codex Trio',
      'base-url': 'https://codex-old.example/v1',
      priority: 4,
      'connection-pool': { enabled: true },
      keys: [
        { 'api-key': 'k1', weight: 1 },
        { 'api-key': 'k2', weight: 2, 'request-retry': 3 },
        { 'api-key': 'k3', weight: 3 },
      ],
    };
    mocks.getRawV8.mockResolvedValueOnce({
      data: [threeKeyGroup],
      headers: { etag: '"trio-etag-1"' },
    });
    const trioLoaded = await providersApi.getCodexConfigs();
    expect(trioLoaded).toHaveLength(3);

    mocks.getRawV8.mockResolvedValueOnce({
      data: [threeKeyGroup],
      headers: { etag: '"trio-etag-1"' },
    });
    mocks.putV8.mockResolvedValueOnce({ status: 'ok' });

    await providersApi.saveCodexConfigs([trioLoaded[1], trioLoaded[2]]);
    expect(mocks.putV8).toHaveBeenLastCalledWith(
      '/config/api-keys/codex',
      [
        {
          name: 'Codex Trio',
          'base-url': 'https://codex-old.example/v1',
          priority: 4,
          'connection-pool': { enabled: true },
          keys: [
            { 'api-key': 'k2', weight: 2, 'request-retry': 3 },
            { 'api-key': 'k3', weight: 3 },
          ],
        },
      ],
      { headers: { 'If-Match': '"trio-etag-1"' } }
    );

    // Case 2: 2-key group [k1, k2] -> delete k1 AND update k2.baseUrl.
    const twoKeyGroup = {
      name: 'Codex Duo',
      'base-url': 'https://duo-old.example/v1',
      priority: 2,
      'connection-pool': { enabled: true },
      keys: [
        { 'api-key': 'k1', weight: 1 },
        { 'api-key': 'k2', weight: 5, 'request-retry': 4 },
      ],
    };
    mocks.getRawV8.mockResolvedValueOnce({
      data: [twoKeyGroup],
      headers: { etag: '"duo-etag-1"' },
    });
    const duoLoaded = await providersApi.getCodexConfigs();

    mocks.getRawV8.mockResolvedValueOnce({
      data: [twoKeyGroup],
      headers: { etag: '"duo-etag-1"' },
    });
    mocks.putV8.mockResolvedValueOnce({ status: 'ok' });

    await providersApi.saveCodexConfigs([
      {
        ...duoLoaded[1],
        baseUrl: 'https://duo-new.example/v1',
      },
    ]);

    expect(mocks.putV8).toHaveBeenLastCalledWith(
      '/config/api-keys/codex',
      [
        {
          name: 'Codex Duo',
          'base-url': 'https://duo-new.example/v1',
          priority: 2,
          'connection-pool': { enabled: true },
          keys: [{ 'api-key': 'k2', weight: 5, 'request-retry': 4 }],
        },
      ],
      { headers: { 'If-Match': '"duo-etag-1"' } }
    );
  });

  it('enforces strict per-family KnownFields and expandV8Groups native group allowlists', async () => {
    // 1. Codex: strips Claude-only & Vertex-only fields, keeps websockets & support-configuration-update,
    //    strips openapi on models, strips authIndex/auth_index, and moves group-level websockets to keys[] while dropping unknown group fields.
    mocks.getRawV8.mockResolvedValueOnce({
      data: [
        {
          name: 'Codex Strict',
          'base-url': 'https://codex.example/v1',
          auth_index: 'drop-group-auth-1',
          authIndex: 'drop-group-auth-2',
          websockets: true,
          cloak: { mode: 'auto' },
          'fingerprint-profile': 'claude-code-2-1-89',
          'experimental-cch-signing': true,
          'rebuild-mid-system-message': true,
          'x-unsupported-group-field': 'must-not-break-expandV8Groups',
          models: [
            {
              name: 'gpt-5-codex',
              openapi: true,
              'support-configuration-update': true,
              authIndex: 'drop-model-auth',
            },
          ],
          keys: [
            {
              'api-key': 'sk-codex-1',
              auth_index: 'drop-key-auth-1',
              authIndex: 'drop-key-auth-2',
              cloak: { mode: 'always' },
              'fingerprint-profile': 'claude-code-2-1-89',
              'experimental-cch-signing': true,
              'rebuild-mid-system-message': true,
            },
          ],
        },
      ],
      headers: { etag: '"codex-strict-1"' },
    });
    const codexLoaded = await providersApi.getCodexConfigs();
    mocks.getRawV8.mockResolvedValueOnce({
      data: codexLoaded[0]?.v8Source?.groups,
      headers: { etag: '"codex-strict-1"' },
    });
    mocks.putV8.mockResolvedValueOnce({ status: 'ok' });

    await providersApi.updateCodexConfig(codexLoaded[0], {
      ...codexLoaded[0],
      weight: 3,
    });

    expect(mocks.putV8).toHaveBeenLastCalledWith(
      '/config/api-keys/codex',
      [
        {
          name: 'Codex Strict',
          'base-url': 'https://codex.example/v1',
          models: [
            {
              name: 'gpt-5-codex',
              'support-configuration-update': true,
            },
          ],
          keys: [
            {
              'api-key': 'sk-codex-1',
              weight: 3,
              websockets: true,
            },
          ],
        },
      ],
      { headers: { 'If-Match': '"codex-strict-1"' } }
    );

    // 2. Claude: strips websockets, openapi, and support-configuration-update; pushes misplaced group cloak to keys[].
    mocks.getRawV8.mockResolvedValueOnce({
      data: [
        {
          name: 'Claude Strict',
          cloak: { mode: 'auto' },
          websockets: true,
          'x-bad-group': 42,
          models: [
            {
              name: 'claude-sonnet-4-5',
              openapi: true,
              'support-configuration-update': true,
            },
          ],
          keys: [
            {
              'api-key': 'sk-claude-1',
              websockets: true,
            },
          ],
        },
      ],
      headers: { etag: '"claude-strict-1"' },
    });
    const claudeLoaded = await providersApi.getClaudeConfigs();
    mocks.getRawV8.mockResolvedValueOnce({
      data: claudeLoaded[0]?.v8Source?.groups,
      headers: { etag: '"claude-strict-1"' },
    });
    mocks.putV8.mockResolvedValueOnce({ status: 'ok' });

    await providersApi.updateClaudeConfig(claudeLoaded[0], {
      ...claudeLoaded[0],
      websockets: true,
      weight: 2,
    });

    expect(mocks.putV8).toHaveBeenLastCalledWith(
      '/config/api-keys/claude',
      [
        {
          name: 'Claude Strict',
          models: [{ name: 'claude-sonnet-4-5' }],
          keys: [
            {
              'api-key': 'sk-claude-1',
              weight: 2,
              cloak: { mode: 'auto' },
            },
          ],
        },
      ],
      { headers: { 'If-Match': '"claude-strict-1"' } }
    );

    // 3. Vertex: strips request-scoped-errors, websockets, Claude fields, and support-configuration-update while keeping openapi.
    mocks.getRawV8.mockResolvedValueOnce({
      data: [
        {
          name: 'Vertex Strict',
          'request-scoped-errors': true,
          websockets: true,
          cloak: { mode: 'auto' },
          openapi: true,
          models: [
            {
              name: 'gemini-2.5-pro',
              openapi: true,
              'support-configuration-update': true,
            },
          ],
          keys: [
            {
              'api-key': 'vk-strict-1',
              'request-scoped-errors': true,
              websockets: true,
              'experimental-cch-signing': true,
            },
          ],
        },
      ],
      headers: { etag: '"vertex-strict-1"' },
    });
    const vertexLoaded = await providersApi.getVertexConfigs();
    mocks.getRawV8.mockResolvedValueOnce({
      data: vertexLoaded[0]?.v8Source?.groups,
      headers: { etag: '"vertex-strict-1"' },
    });
    mocks.putV8.mockResolvedValueOnce({ status: 'ok' });

    await providersApi.updateVertexConfig(vertexLoaded[0], {
      ...vertexLoaded[0],
      weight: 4,
    });

    expect(mocks.putV8).toHaveBeenLastCalledWith(
      '/config/api-keys/vertex',
      [
        {
          name: 'Vertex Strict',
          models: [{ name: 'gemini-2.5-pro', openapi: true }],
          keys: [
            {
              'api-key': 'vk-strict-1',
              weight: 4,
              openapi: true,
            },
          ],
        },
      ],
      { headers: { 'If-Match': '"vertex-strict-1"' } }
    );
  });

  it('enforces CPA KnownFields(true) allowlists for openai-compatibility groups and models', async () => {
    const rawOpenAIGroup = {
      name: 'OpenAI Allowlist',
      priority: 5,
      disabled: true,
      prefix: 'oa',
      'base-url': 'https://api.example/v1',
      headers: { 'X-Team': 'core' },
      'support-prompt-cache-key': true,
      'disable-cooling': false,
      'request-retry': 2,
      'request-scoped-errors': [{ status: 503, action: 'retry' }],
      'connection-pool': { enabled: true },
      'excluded-models': ['drop-non-openai-field'],
      'test-model': 'drop-test-model',
      'proxy-url': 'http://drop-group-proxy:8080',
      'x-unknown-group-field': 'drop-me',
      models: [
        {
          name: 'gpt-4.1',
          alias: '4.1',
          displayName: 'GPT 4.1',
          forceMapping: true,
          maxContextLength: 1048576,
          isCompat: true,
          image: true,
          inputModalities: ['text', 'image'],
          outputModalities: ['text'],
          useMaxCompletionTokens: true,
          openapi: true,
          'support-configuration-update': true,
          'x-unknown-model-field': 'drop-me',
          thinking: {
            min: 512,
            max: 16384,
            zeroAllowed: true,
            dynamic_allowed: false,
            disableType: 'disabled',
            levels: ['low', 'high'],
            'x-unknown-thinking': 'drop-me',
          },
        },
      ],
      keys: [
        {
          'api-key': 'sk-oa-strict',
          weight: 3,
          'proxy-url': 'http://key-proxy:8080',
          'connection-pool': { enabled: false },
        },
      ],
    };

    mocks.getRawV8.mockResolvedValueOnce({
      data: [rawOpenAIGroup],
      headers: { etag: '"oa-allowlist-1"' },
    });
    const [loaded] = await providersApi.getOpenAIProviders();

    mocks.getRawV8.mockResolvedValueOnce({
      data: [rawOpenAIGroup],
      headers: { etag: '"oa-allowlist-1"' },
    });
    mocks.putV8.mockResolvedValueOnce({ status: 'ok' });

    await providersApi.updateOpenAIProvider('OpenAI Allowlist', 0, loaded);

    expect(mocks.putV8).toHaveBeenLastCalledWith(
      '/config/api-keys/openai-compatibility',
      [
        {
          name: 'OpenAI Allowlist',
          priority: 5,
          disabled: true,
          prefix: 'oa',
          'base-url': 'https://api.example/v1',
          headers: { 'X-Team': 'core' },
          'support-prompt-cache-key': true,
          'disable-cooling': false,
          'request-retry': 2,
          'request-scoped-errors': [{ status: 503, action: 'retry' }],
          'connection-pool': { enabled: true },
          models: [
            {
              name: 'gpt-4.1',
              alias: '4.1',
              'display-name': 'GPT 4.1',
              'force-mapping': true,
              'max-context-length': 1048576,
              'is-compat': true,
              image: true,
              'input-modalities': ['text', 'image'],
              'output-modalities': ['text'],
              'use-max-completion-tokens': true,
              thinking: {
                min: 512,
                max: 16384,
                'zero-allowed': true,
                'dynamic-allowed': false,
                'disable-type': 'disabled',
                levels: ['low', 'high'],
              },
            },
          ],
          keys: [
            {
              'api-key': 'sk-oa-strict',
              weight: 3,
              'proxy-url': 'http://key-proxy:8080',
              'connection-pool': { enabled: false },
            },
          ],
        },
      ],
      { headers: { 'If-Match': '"oa-allowlist-1"' } }
    );
  });

  it('scopes latestV8ConfigEtag by connection epoch, reads ApiError etag/headers, and clears stale root ETag on 404 not_found', async () => {
    // 1. Root 200 records ETag, then root 404 not_found without ETag clears stale latestV8ConfigEtag.
    mocks.getRawV8.mockResolvedValueOnce({
      data: { claude: [{ name: 'Claude 1', keys: [{ 'api-key': 'sk-1' }] }] },
      headers: { etag: '"etag-before-disk-delete"' },
    });
    await loadAllProvidersV8Snapshot();
    expect(providersApi.getLatestV8ConfigEtag()).toBe('"etag-before-disk-delete"');

    mocks.getRawV8.mockRejectedValueOnce(
      Object.assign(new Error('not_found'), {
        status: 404,
        details: { error: 'not_found' },
      })
    );
    const emptySnapshot = await loadAllProvidersV8Snapshot();
    expect(emptySnapshot.v8Supported).toBe(true);
    expect(providersApi.getLatestV8ConfigEtag()).toBeUndefined();

    // 2. Real ApiError (with .etag / .headers and no .response) updates latestV8ConfigEtag on PUT failure.
    mocks.getRawV8.mockResolvedValueOnce({
      data: [{ name: 'Claude 1', keys: [{ 'api-key': 'sk-1' }] }],
      headers: { etag: '"etag-old"' },
    });
    mocks.putV8.mockRejectedValueOnce(
      Object.assign(new Error('Precondition Failed'), {
        name: 'ApiError',
        status: 412,
        etag: '"etag-from-api-error-412"',
        headers: { etag: '"etag-from-api-error-412"' },
      })
    );
    await expect(
      providersApi.createClaudeConfig({ apiKey: 'sk-2' })
    ).rejects.toMatchObject({ status: 412 });
    expect(providersApi.getLatestV8ConfigEtag()).toBe('"etag-from-api-error-412"');

    // 3. Connection epoch switch invalidates previous server's latestV8ConfigEtag and ignores late completions.
    let resolveLateOldSnapshot!: (value: unknown) => void;
    mocks.getRawV8.mockReturnValueOnce(
      new Promise((resolve) => {
        resolveLateOldSnapshot = resolve;
      })
    );
    const lateSnapshotPromise = loadAllProvidersV8Snapshot();

    mocks.epoch += 1;
    expect(providersApi.getLatestV8ConfigEtag()).toBeUndefined();

    // Empty-family create on new server must NOT send the previous server's If-Match header.
    mocks.getRawV8.mockRejectedValueOnce(
      Object.assign(new Error('not_found'), {
        status: 404,
        details: { error: 'not_found' },
      })
    );
    mocks.putV8.mockResolvedValueOnce({
      status: 'ok',
      headers: { etag: '"new-server-etag-1"' },
    });
    await providersApi.createCodexConfig({
      name: 'New Server Codex',
      apiKey: 'sk-new-server',
    });
    expect(mocks.putV8).toHaveBeenLastCalledWith(
      '/config/api-keys/codex',
      [{ name: 'New Server Codex', keys: [{ 'api-key': 'sk-new-server' }] }],
      undefined
    );
    expect(providersApi.getLatestV8ConfigEtag()).toBe('"new-server-etag-1"');

    // Late completion from old epoch must not overwrite new server's latestV8ConfigEtag.
    resolveLateOldSnapshot({
      data: {},
      headers: { etag: '"stale-old-server-etag"' },
    });
    await lateSnapshotPromise;
    expect(providersApi.getLatestV8ConfigEtag()).toBe('"new-server-etag-1"');
  });

  it('canonicalizes and strips nested connection-pool, request-scoped-errors, and cloak.sensitive-patterns-replace for CPA KnownFields(true)', async () => {
    const rawClaudeGroups = [
      {
        name: 'Claude Nested Strict',
        'base-url': 'https://claude.example',
        connectionPool: {
          enabled: true,
          idleConnTimeout: '45s',
          maxIdleConnsPerHost: 12,
          'max-idle-conns': 99,
          unknownPoolFlag: true,
        },
        requestScopedErrors: [
          {
            status: 429,
            match: ['rate limit'],
            matchRegexr: ['retry-after:\\s*\\d+'],
            action: 'cooldown',
            extraRuleField: 'drop-me',
          },
          {
            status: 503,
            'match-regex': ['overloaded'],
            action: 'retry',
            unknownKey: 1,
          },
        ],
        keys: [
          {
            'api-key': 'sk-claude-nested-1',
            cloak: {
              mode: 'auto',
              sensitivePatternsReplace: [
                {
                  pattern: 'sk-[a-z0-9]+',
                  replace: '[REDACTED]',
                  extraPatternField: 'drop-me',
                },
              ],
            },
            connection_pool: {
              'max-idle-conns': 16,
              onlyUnknown: true,
            },
            request_scoped_errors: [
              {
                status: 400,
                match_regexr: ['invalid_request'],
                action: 'abort',
                extra: 'strip',
              },
            ],
          },
          {
            'api-key': 'sk-claude-nested-2',
          },
        ],
      },
    ];

    mocks.getRawV8.mockResolvedValueOnce({
      data: rawClaudeGroups,
      headers: { etag: '"etag-nested-claude-1"' },
    });
    const [loadedClaude] = await providersApi.getClaudeConfigs();

    mocks.getRawV8.mockResolvedValueOnce({
      data: rawClaudeGroups,
      headers: { etag: '"etag-nested-claude-1"' },
    });
    mocks.putV8.mockResolvedValueOnce({ status: 'ok' });
    await providersApi.updateClaudeConfig(loadedClaude, loadedClaude);

    expect(mocks.putV8).toHaveBeenLastCalledWith(
      '/config/api-keys/claude',
      [
        {
          name: 'Claude Nested Strict',
          'base-url': 'https://claude.example',
          'connection-pool': {
            enabled: true,
            'idle-conn-timeout': '45s',
            'max-idle-conns-per-host': 12,
          },
          'request-scoped-errors': [
            {
              status: 429,
              match: ['rate limit'],
              'match-regexr': ['retry-after:\\s*\\d+'],
              action: 'cooldown',
            },
            {
              status: 503,
              'match-regexr': ['overloaded'],
              action: 'retry',
            },
          ],
          keys: [
            {
              'api-key': 'sk-claude-nested-1',
              cloak: {
                mode: 'auto',
                'sensitive-patterns-replace': [
                  {
                    pattern: 'sk-[a-z0-9]+',
                    replacement: '[REDACTED]',
                  },
                ],
              },
              'request-scoped-errors': [
                {
                  status: 400,
                  'match-regexr': ['invalid_request'],
                  action: 'abort',
                },
              ],
            },
            {
              'api-key': 'sk-claude-nested-2',
            },
          ],
        },
      ],
      { headers: { 'If-Match': '"etag-nested-claude-1"' } }
    );

    // OpenAI compatibility: explicit {} connection-pool preserved; all-unknown connection-pool omitted;
    // camelCase/snake_case keys[].connection-pool canonicalized.
    const rawOpenAIGroups = [
      {
        name: 'OA Nested Pool',
        'base-url': 'https://oa-nested.example/v1',
        'connection-pool': {},
        'request-scoped-errors': [
          {
            status: 502,
            match_regexr: ['bad gateway'],
            extra: true,
          },
        ],
        keys: [
          {
            'api-key': 'sk-oa-k1',
            connectionPool: {
              idle_conn_timeout: '30s',
              max_idle_conns_per_host: 6,
              unknownField: 'drop',
            },
          },
          {
            'api-key': 'sk-oa-k2',
            'connection-pool': {
              'max-idle-conns': 10,
            },
          },
        ],
      },
    ];
    mocks.getRawV8.mockResolvedValueOnce({
      data: rawOpenAIGroups,
      headers: { etag: '"etag-nested-oa-1"' },
    });
    const [loadedOA] = await providersApi.getOpenAIProviders();
    mocks.getRawV8.mockResolvedValueOnce({
      data: rawOpenAIGroups,
      headers: { etag: '"etag-nested-oa-1"' },
    });
    mocks.putV8.mockResolvedValueOnce({ status: 'ok' });
    await providersApi.updateOpenAIProvider('OA Nested Pool', 0, loadedOA);

    expect(mocks.putV8).toHaveBeenLastCalledWith(
      '/config/api-keys/openai-compatibility',
      [
        {
          name: 'OA Nested Pool',
          'base-url': 'https://oa-nested.example/v1',
          'connection-pool': {},
          'request-scoped-errors': [
            {
              status: 502,
              'match-regexr': ['bad gateway'],
            },
          ],
          keys: [
            {
              'api-key': 'sk-oa-k1',
              'connection-pool': {
                'idle-conn-timeout': '30s',
                'max-idle-conns-per-host': 6,
              },
            },
            {
              'api-key': 'sk-oa-k2',
            },
          ],
        },
      ],
      { headers: { 'If-Match': '"etag-nested-oa-1"' } }
    );
  });

  it('normalizes weak ETag W/"sha256-abc" into strong If-Match "sha256-abc" and matches strong caller ETag', async () => {
    mocks.getRawV8.mockResolvedValueOnce({
      data: [{ name: 'Claude Weak', keys: [{ 'api-key': 'sk-weak-1' }] }],
      headers: { etag: 'W/"sha256-abc"' },
    });
    const [loaded] = await providersApi.getClaudeConfigs();
    expect(loaded.v8Source?.etag).toBe('"sha256-abc"');
    expect(providersApi.getLatestV8ConfigEtag()).toBe('"sha256-abc"');

    mocks.getRawV8.mockResolvedValueOnce({
      data: [{ name: 'Claude Weak', keys: [{ 'api-key': 'sk-weak-1' }] }],
      headers: { ETag: 'W/"sha256-abc"' },
    });
    mocks.putV8.mockResolvedValueOnce({ status: 'ok' });

    await providersApi.updateClaudeConfig(
      loaded,
      { ...loaded, name: 'Claude Strong' },
      { expectedEtag: '"sha256-abc"' }
    );

    expect(mocks.putV8).toHaveBeenLastCalledWith(
      '/config/api-keys/claude',
      [{ name: 'Claude Strong', keys: [{ 'api-key': 'sk-weak-1' }] }],
      { headers: { 'If-Match': '"sha256-abc"' } }
    );
  });

  it('binds queued and multi-step writes to originating connection scope and aborts when connection changes', async () => {
    const createScopedV8Mock = vi.fn(() => ({
      baseURL: 'http://conn-a.local:8317/v8/management',
      headers: { Authorization: 'Bearer key-a' },
      cpampScopedRequest: true,
      cpampV8Namespace: true,
    }));
    const createScopedV0Mock = vi.fn(() => ({
      baseURL: 'http://conn-a.local:8317/v0/management',
      headers: { Authorization: 'Bearer key-a' },
      cpampScopedRequest: true,
    }));
    Reflect.set(apiClient, 'createCurrentScopedV8Config', createScopedV8Mock);
    Reflect.set(apiClient, 'createCurrentScopedConfig', createScopedV0Mock);

    try {
      // 1. Scoped config threads originating baseURL + Authorization merged with If-Match.
      mocks.getRawV8.mockResolvedValueOnce({
        data: [{ name: 'Claude A', keys: [{ 'api-key': 'sk-a1' }] }],
        headers: { etag: '"etag-conn-a"' },
      });
      mocks.putV8.mockResolvedValueOnce({ status: 'ok' });
      await providersApi.saveClaudeConfigs([{ name: 'Claude A', apiKey: 'sk-a1' }]);
      expect(mocks.getRawV8).toHaveBeenCalledWith('/config/api-keys/claude', {
        baseURL: 'http://conn-a.local:8317/v8/management',
        headers: { Authorization: 'Bearer key-a' },
        cpampScopedRequest: true,
        cpampV8Namespace: true,
      });
      expect(mocks.putV8).toHaveBeenCalledWith(
        '/config/api-keys/claude',
        [{ name: 'Claude A', keys: [{ 'api-key': 'sk-a1' }] }],
        {
          baseURL: 'http://conn-a.local:8317/v8/management',
          headers: {
            Authorization: 'Bearer key-a',
            'If-Match': '"etag-conn-a"',
          },
          cpampScopedRequest: true,
          cpampV8Namespace: true,
        }
      );

      // 2. Switching connection while write #1 is paused after GET aborts BOTH write #1 (before PUT)
      // and queued write #2 (before any GET/PUT against Connection B).
      mocks.getRawV8.mockClear();
      mocks.putV8.mockClear();
      mocks.put.mockClear();

      let markFirstGetStarted!: () => void;
      const firstGetStarted = new Promise<void>((resolve) => {
        markFirstGetStarted = resolve;
      });
      let resolveFirstGet!: (value: unknown) => void;
      mocks.getRawV8.mockImplementationOnce(() => {
        markFirstGetStarted();
        return new Promise((resolve) => {
          resolveFirstGet = resolve;
        });
      });

      const inFlightWrite = providersApi.createClaudeConfig({
        name: 'Claude A2',
        apiKey: 'sk-a2',
      });
      const queuedWrite = providersApi.deleteClaudeConfig('sk-a1');

      await firstGetStarted;
      // Switch to Connection B while write #1 is paused between GET and PUT and write #2 is queued.
      mocks.epoch += 1;
      resolveFirstGet({
        data: [{ name: 'Claude A', keys: [{ 'api-key': 'sk-a1' }] }],
        headers: { etag: '"etag-conn-a-2"' },
      });

      await expect(inFlightWrite).rejects.toMatchObject({ code: 'CONNECTION_CHANGED' });
      await expect(queuedWrite).rejects.toMatchObject({ code: 'CONNECTION_CHANGED' });
      expect(mocks.getRawV8).toHaveBeenCalledTimes(1);
      expect(mocks.putV8).not.toHaveBeenCalled();
      expect(mocks.put).not.toHaveBeenCalled();
    } finally {
      Reflect.deleteProperty(apiClient, 'createCurrentScopedV8Config');
      Reflect.deleteProperty(apiClient, 'createCurrentScopedConfig');
    }
  });

  it('rethrows PUT 404/405 errors instead of silently downgrading to v0 when GET /v8 already returned 200 OK', async () => {
    const methodNotAllowed = Object.assign(new Error('Method Not Allowed'), { status: 405 });

    mocks.getRawV8.mockResolvedValueOnce({
      data: [{ name: 'Claude 1', keys: [{ 'api-key': 'sk-c1' }] }],
      headers: { etag: '"etag-ro-1"' },
    });
    mocks.putV8.mockRejectedValueOnce(methodNotAllowed);
    await expect(
      providersApi.saveClaudeConfigs([{ name: 'Claude 1', apiKey: 'sk-c1' }])
    ).rejects.toMatchObject({ status: 405 });

    mocks.getRawV8.mockResolvedValueOnce({
      data: [{ name: 'Claude 1', keys: [{ 'api-key': 'sk-c1' }] }],
      headers: { etag: '"etag-ro-1"' },
    });
    mocks.putV8.mockRejectedValueOnce(methodNotAllowed);
    await expect(
      providersApi.createClaudeConfig({ name: 'Claude 2', apiKey: 'sk-c2' })
    ).rejects.toMatchObject({ status: 405 });

    mocks.getRawV8.mockResolvedValueOnce({
      data: [{ name: 'OA 1', 'base-url': 'https://oa.example/v1', keys: [{ 'api-key': 'sk-1' }] }],
      headers: { etag: '"etag-ro-2"' },
    });
    mocks.putV8.mockRejectedValueOnce(Object.assign(new Error('Not Found'), { status: 404 }));
    await expect(providersApi.deleteOpenAIProvider('OA 1', 0)).rejects.toMatchObject({
      status: 404,
    });

    expect(mocks.put).not.toHaveBeenCalled();
    expect(mocks.delete).not.toHaveBeenCalled();
  });

  it('includes meta in loadAllProvidersV8Snapshot v0 fallback and tolerates missing /meta-api-key', async () => {
    mocks.getRawV8.mockRejectedValueOnce(Object.assign(new Error('Not Found'), { status: 404 }));
    mocks.get.mockImplementation(async (path: string) => {
      if (path === '/meta-api-key') {
        return { 'meta-api-key': [{ 'api-key': 'sk-meta-v0', 'base-url': 'https://meta.v0/v1' }] };
      }
      return {};
    });

    const withMeta = await loadAllProvidersV8Snapshot();
    expect(withMeta.v8Supported).toBe(false);
    expect(withMeta.meta).toEqual([{ apiKey: 'sk-meta-v0', baseUrl: 'https://meta.v0/v1' }]);

    mocks.getRawV8.mockRejectedValueOnce(Object.assign(new Error('Not Found'), { status: 404 }));
    mocks.get.mockImplementation(async (path: string) => {
      if (path === '/meta-api-key') {
        throw Object.assign(new Error('Not Found'), { status: 404 });
      }
      return {};
    });

    const withoutMeta = await loadAllProvidersV8Snapshot();
    expect(withoutMeta.v8Supported).toBe(false);
    expect(withoutMeta.meta).toEqual([]);
  });

  it('exposes getMetaConfigs and tolerates 404/405 on optional v0 endpoints in loadAllProvidersV8Snapshot while rethrowing 502', async () => {
    mocks.getRawV8.mockResolvedValueOnce({
      data: [
        {
          name: 'Meta V8',
          'base-url': 'https://meta.v8.example/v1',
          keys: [{ 'api-key': 'sk-meta-v8' }],
        },
      ],
      headers: { etag: '"etag-meta-v8"' },
    });
    await expect(providersApi.getMetaConfigs()).resolves.toMatchObject([
      {
        name: 'Meta V8',
        apiKey: 'sk-meta-v8',
        baseUrl: 'https://meta.v8.example/v1',
      },
    ]);

    mocks.getRawV8.mockRejectedValueOnce(Object.assign(new Error('Not Found'), { status: 404 }));
    mocks.get.mockImplementation(async (path: string) => {
      if (
        path === '/interactions-api-key' ||
        path === '/xai-api-key' ||
        path === '/vertex-api-key'
      ) {
        throw Object.assign(new Error('Not Found'), { status: 404 });
      }
      if (path === '/meta-api-key') {
        throw Object.assign(new Error('Method Not Allowed'), { status: 405 });
      }
      if (path === '/gemini-api-key') {
        return { 'gemini-api-key': [{ 'api-key': 'gem-v0-1' }] };
      }
      if (path === '/codex-api-key') {
        return { 'codex-api-key': [{ 'api-key': 'cx-v0-1' }] };
      }
      if (path === '/claude-api-key') {
        return { 'claude-api-key': [{ 'api-key': 'cl-v0-1' }] };
      }
      if (path === '/openai-compatibility') {
        return {
          'openai-compatibility': [
            { name: 'OA V0', 'base-url': 'https://oa.v0/v1', 'api-key-entries': [] },
          ],
        };
      }
      return {};
    });

    const legacySnapshot = await loadAllProvidersV8Snapshot();
    expect(legacySnapshot.v8Supported).toBe(false);
    expect(legacySnapshot.gemini).toEqual([{ apiKey: 'gem-v0-1' }]);
    expect(legacySnapshot.codex).toEqual([{ apiKey: 'cx-v0-1' }]);
    expect(legacySnapshot.claude).toEqual([{ apiKey: 'cl-v0-1' }]);
    expect(legacySnapshot.interactions).toEqual([]);
    expect(legacySnapshot.xai).toEqual([]);
    expect(legacySnapshot.vertex).toEqual([]);
    expect(legacySnapshot.meta).toEqual([]);
    expect(legacySnapshot.openai).toHaveLength(1);

    mocks.getRawV8.mockRejectedValueOnce(Object.assign(new Error('Not Found'), { status: 404 }));
    mocks.get.mockImplementation(async (path: string) => {
      if (path === '/vertex-api-key') {
        throw Object.assign(new Error('Bad Gateway'), { status: 502 });
      }
      return {};
    });
    await expect(loadAllProvidersV8Snapshot()).rejects.toMatchObject({ status: 502 });
  });

  it('prevents slow passive reads from regressing latestV8ConfigEtag after a same-epoch write and keeps write version isolated from passive reads', async () => {
    const versionBeforeReads = providersApi.getV8EtagMutationVersion();
    mocks.getRawV8.mockResolvedValueOnce({
      data: [{ name: 'Claude Initial', keys: [{ 'api-key': 'sk-c0' }] }],
      headers: { etag: '"etag-passive-0"' },
    });
    await providersApi.getClaudeConfigs();
    expect(providersApi.getLatestV8ConfigEtag()).toBe('"etag-passive-0"');
    expect(providersApi.getV8EtagMutationVersion()).toBe(versionBeforeReads);

    let resolveSlowClaudeRead!: (value: unknown) => void;
    let resolveSlowSnapshotRead!: (value: unknown) => void;
    mocks.getRawV8
      .mockReturnValueOnce(
        new Promise((resolve) => {
          resolveSlowClaudeRead = resolve;
        })
      )
      .mockReturnValueOnce(
        new Promise((resolve) => {
          resolveSlowSnapshotRead = resolve;
        })
      )
      .mockResolvedValueOnce({
        data: [{ name: 'Claude Initial', keys: [{ 'api-key': 'sk-c0' }] }],
        headers: { etag: '"etag-pre-write-1"' },
      });
    mocks.putV8.mockResolvedValueOnce({
      status: 'ok',
      headers: { etag: '"etag-post-write-2"' },
    });

    const slowClaudePromise = providersApi.getClaudeConfigs();
    const slowSnapshotPromise = loadAllProvidersV8Snapshot();

    await providersApi.saveClaudeConfigs([{ name: 'Claude Updated', apiKey: 'sk-c1' }]);
    expect(providersApi.getLatestV8ConfigEtag()).toBe('"etag-post-write-2"');
    expect(providersApi.getV8EtagMutationVersion()).toBeGreaterThan(versionBeforeReads);

    resolveSlowClaudeRead({
      data: [{ name: 'Claude Stale', keys: [{ 'api-key': 'sk-c0' }] }],
      headers: { etag: '"etag-stale-read-0"' },
    });
    resolveSlowSnapshotRead({
      data: { claude: [{ name: 'Claude Stale', keys: [{ 'api-key': 'sk-c0' }] }] },
      headers: { etag: '"etag-stale-snapshot-0"' },
    });

    const [staleClaudeItems, staleSnapshot] = await Promise.all([
      slowClaudePromise,
      slowSnapshotPromise,
    ]);
    expect(staleClaudeItems[0]?.v8Source?.etag).toBe('"etag-stale-read-0"');
    expect(staleSnapshot.claude[0]?.v8Source?.etag).toBe('"etag-stale-snapshot-0"');
    expect(providersApi.getLatestV8ConfigEtag()).toBe('"etag-post-write-2"');
  });

  it('coerces and filters nested connection-pool, request-scoped-errors, cloak fields, and round-trips keyless connection-pool entries', async () => {
    const rawClaudeGroups = [
      {
        name: 'Claude Hardened Types',
        connectionPool: {
          enabled: 'true',
          idleConnTimeout: ' 90s ',
          maxIdleConnsPerHost: '8',
        },
        requestScopedErrors: [
          {
            status: '429',
            match: ['rate limit', 123, '', null],
            matchRegexr: 'not-an-array',
            action: ' cooldown ',
          },
          {
            status: 'not-a-status',
            match: [null, ''],
            action: '   ',
            extraOnly: true,
          },
        ],
        keys: [
          {
            'api-key': 'sk-claude-hardened-1',
            connection_pool: {
              enabled: 123,
              idle_conn_timeout: 45,
              max_idle_conns_per_host: 2.5,
            },
            cloak: {
              mode: '  strict  ',
              strictMode: 'false',
              cacheUserId: 'invalid-bool',
              sensitiveWords: ['  secret-term  ', '', 42, null],
              sensitiveWordsReplace: {
                internal: '',
                codename: 'public',
                invalidNumber: 99,
              },
              sensitivePatternsReplace: [
                { pattern: 'tok_[0-9]+', replace: '' },
                { pattern: '', replace: 'drop-empty-pattern' },
                { pattern: 123, replacement: 'drop-non-string-pattern' },
              ],
            },
          },
          {
            'api-key': 'sk-claude-hardened-sibling-2',
            cloak: {
              mode: 999,
              'strict-mode': 'not-a-bool',
              'cache-user-id': 'true',
              'sensitive-words': [42, '  keep-sibling-word  ', null],
              'sensitive-words-replace': ['not-a-record'],
            },
          },
        ],
      },
    ];

    mocks.getRawV8.mockResolvedValueOnce({
      data: rawClaudeGroups,
      headers: { etag: '"etag-hardened-claude-1"' },
    });
    const [loadedClaude] = await providersApi.getClaudeConfigs();

    mocks.getRawV8.mockResolvedValueOnce({
      data: rawClaudeGroups,
      headers: { etag: '"etag-hardened-claude-1"' },
    });
    mocks.putV8.mockResolvedValueOnce({ status: 'ok' });
    await providersApi.updateClaudeConfig(loadedClaude, loadedClaude);

    expect(mocks.putV8).toHaveBeenLastCalledWith(
      '/config/api-keys/claude',
      [
        {
          name: 'Claude Hardened Types',
          'connection-pool': {
            enabled: true,
            'idle-conn-timeout': '90s',
            'max-idle-conns-per-host': 8,
          },
          'request-scoped-errors': [
            {
              status: 429,
              match: ['rate limit'],
              action: 'cooldown',
            },
          ],
          keys: [
            {
              'api-key': 'sk-claude-hardened-1',
              cloak: {
                mode: 'strict',
                'strict-mode': false,
                'sensitive-words': ['secret-term'],
                'sensitive-words-replace': {
                  internal: '',
                  codename: 'public',
                },
                'sensitive-patterns-replace': [
                  {
                    pattern: 'tok_[0-9]+',
                    replacement: '',
                  },
                ],
              },
            },
            {
              'api-key': 'sk-claude-hardened-sibling-2',
              cloak: {
                'cache-user-id': true,
                'sensitive-words': ['keep-sibling-word'],
              },
            },
          ],
        },
      ],
      { headers: { 'If-Match': '"etag-hardened-claude-1"' } }
    );

    const rawKeylessPoolOpenAI = [
      {
        name: 'Ollama Local Pool',
        disabled: true,
        'base-url': 'http://127.0.0.1:11434/v1',
        keys: [
          {
            'connection-pool': {
              enabled: true,
              'idle-conn-timeout': '120s',
            },
          },
        ],
      },
    ];
    mocks.getRawV8.mockResolvedValueOnce({
      data: rawKeylessPoolOpenAI,
      headers: { etag: '"etag-oa-keyless-pool-1"' },
    });
    const loadedOpenAI = await providersApi.getOpenAIProviders();
    expect(loadedOpenAI[0].apiKeyEntries).toHaveLength(1);

    mocks.getRawV8.mockResolvedValueOnce({
      data: rawKeylessPoolOpenAI,
      headers: { etag: '"etag-oa-keyless-pool-1"' },
    });
    mocks.putV8.mockResolvedValueOnce({ status: 'ok' });
    await providersApi.saveOpenAIProviders(loadedOpenAI);

    expect(mocks.putV8).toHaveBeenLastCalledWith(
      '/config/api-keys/openai-compatibility',
      [
        {
          name: 'Ollama Local Pool',
          disabled: true,
          'base-url': 'http://127.0.0.1:11434/v1',
          keys: [
            {
              'api-key': '',
              'connection-pool': {
                enabled: true,
                'idle-conn-timeout': '120s',
              },
            },
          ],
        },
      ],
      { headers: { 'If-Match': '"etag-oa-keyless-pool-1"' } }
    );
  });
});
