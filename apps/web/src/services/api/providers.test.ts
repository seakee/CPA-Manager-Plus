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

    await providersApi.saveGeminiKeys([{ apiKey: '', authIndex: 'auth-3' }]);

    expect(mocks.put).toHaveBeenCalledWith('/gemini-api-key', [{ 'auth-index': 'auth-3' }]);
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
              'force-mapping': true,
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
      models: [
        { name: 'gpt-image', forceMapping: true, image: true, thinking: { effort: 'high' } },
      ],
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
        cloak: { mode: 'auto', cacheUserId: true },
        models: [
          {
            name: 'claude-sonnet',
            alias: 'sonnet',
            forceMapping: true,
            image: true,
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
            'force-mapping': true,
            image: true,
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
        models: [{ name: 'gpt-image', image: true, thinking: { mode: 'auto' } }],
      },
    ]);

    expect(mocks.put).toHaveBeenLastCalledWith('/openai-compatibility', [
      {
        name: 'openai-compatible',
        'base-url': 'https://api.example.com/v1',
        'api-key-entries': [],
        'disable-cooling': true,
        models: [{ name: 'gpt-image', image: true, thinking: { mode: 'auto' } }],
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
          models: [{ name: 'openai-model', image: true, thinking: { effort: 'medium' } }],
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
            thinking: { effort: 'medium' },
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
          models: [{ name: 'openai-model', 'force-mapping': true }],
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
        models: [{ name: 'openai-model', forceMapping: false }],
      },
    ]);

    expect(mocks.put).toHaveBeenLastCalledWith('/openai-compatibility', [
      {
        name: 'openai-compatible',
        'base-url': 'https://api.example.com/v1',
        'api-key-entries': [],
        'disable-cooling': false,
        models: [{ name: 'openai-model', 'force-mapping': false }],
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
