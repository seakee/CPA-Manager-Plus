import { beforeEach, describe, expect, it, vi } from 'vitest';

const { mocks } = vi.hoisted(() => ({
  mocks: {
    get: vi.fn(),
    put: vi.fn(),
  },
}));

vi.mock('./client', () => ({
  apiClient: {
    get: mocks.get,
    put: mocks.put,
  },
}));

import { providersApi } from './providers';

beforeEach(() => {
  mocks.get.mockReset();
  mocks.put.mockReset();
});

describe('providersApi auth-index preservation', () => {
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
      {
        name: 'openai-compatible',
        baseUrl: 'https://api.example.com/v1',
        apiKeyEntries: [{ apiKey: '', authIndex: 'auth-2' }],
      },
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

  it('serializes Claude disable-cooling, cch signing, cloak cache, and model metadata', async () => {
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
        'experimental-cch-signing': true,
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
});
