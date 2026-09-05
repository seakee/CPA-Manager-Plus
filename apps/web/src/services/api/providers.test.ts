import { beforeEach, describe, expect, it, vi } from 'vitest';
import {
  MODEL_THINKING_LEVELS_CLEAR_MARKER,
  MODEL_THINKING_LEVELS_EDIT_MARKER,
  hasModelThinkingLevelsEditMarker,
  markModelThinkingLevelsForClear,
  markModelThinkingLevelsForEdit,
} from '@/types';

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

import { providersApi, verifyClaudeFingerprintInRawConfig } from './providers';

beforeEach(() => {
  mocks.get.mockReset();
  mocks.put.mockReset();
});

describe('providersApi auth-index preservation', () => {
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
