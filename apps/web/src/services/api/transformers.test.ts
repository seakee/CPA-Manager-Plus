import { describe, expect, it } from 'vitest';
import {
  normalizeConfigResponse,
  normalizeGeminiKeyConfig,
  normalizeProviderKeyConfig,
  normalizeV8ProviderGroups,
} from './transformers';

describe('normalizeConfigResponse xAI API keys', () => {
  it('normalizes the xai-api-key contract using the provider-key shape', () => {
    const config = normalizeConfigResponse({
      'xai-api-key': [
        {
          'api-key': 'xai-key',
          'auth-index': 'xai-auth',
          'base-url': 'https://api.x.ai/v1',
          prefix: 'team-xai',
          websockets: true,
          'disable-cooling': true,
          models: [{ name: 'grok-4.5', alias: 'grok-latest' }],
        },
      ],
    });

    expect(config.xaiApiKeys).toEqual([
      expect.objectContaining({
        apiKey: 'xai-key',
        authIndex: 'xai-auth',
        baseUrl: 'https://api.x.ai/v1',
        prefix: 'team-xai',
        websockets: true,
        disableCooling: true,
        models: [{ name: 'grok-4.5', alias: 'grok-latest' }],
      }),
    ]);
  });

  it('normalizes the xAI inference inspection switch aliases', () => {
    const config = normalizeConfigResponse({
      clean: {
        xai_inference_enabled: true,
        xai_inference_user_agent: 'xai-custom-agent',
        xai_inference_model: 'grok-custom',
        xai_inference_prompt: 'Reply OK.',
      },
    });

    expect(config.clean).toMatchObject({
      xaiInferenceEnabled: true,
      xaiInferenceUserAgent: 'xai-custom-agent',
      xaiInferenceModel: 'grok-custom',
      xaiInferencePrompt: 'Reply OK.',
    });
  });
});

describe('normalizeConfigResponse cooling overrides', () => {
  it.each([
    [true, true],
    [false, false],
    [null, null],
  ] as const)('preserves %j instead of applying a boolean default', (value, expected) => {
    const config = normalizeConfigResponse({
      'gemini-api-key': [{ 'api-key': 'gemini-key', 'disable-cooling': value }],
    });

    expect(config.geminiApiKeys?.[0]?.disableCooling).toBe(expected);
  });

  it('leaves a missing cooling override unset', () => {
    const config = normalizeConfigResponse({
      'gemini-api-key': [{ 'api-key': 'gemini-key' }],
    });

    expect(config.geminiApiKeys?.[0]).not.toHaveProperty('disableCooling');
  });
});

describe('normalizeConfigResponse Claude fingerprint profile', () => {
  it('normalizes fingerprint-profile into the typed config', () => {
    const config = normalizeConfigResponse({
      'claude-api-key': [{ 'api-key': 'claude-secret', 'fingerprint-profile': 'claude-code-cli' }],
    });

    expect(config.claudeApiKeys?.[0]?.fingerprintProfile).toBe('claude-code-cli');
  });

  it('canonicalizes the legacy oauth-cli alias to claude-code-cli', () => {
    const config = normalizeConfigResponse({
      'claude-api-key': [
        { 'api-key': 'claude-secret', 'fingerprint-profile': '  OAuth-CLI  ' },
        { 'api-key': 'claude-secret', fingerprintProfile: 'oauth-cli' },
        { 'api-key': 'claude-secret', fingerprint_profile: 'claude-code-cli' },
      ],
    });

    expect(config.claudeApiKeys?.map((key) => key.fingerprintProfile)).toEqual([
      'claude-code-cli',
      'claude-code-cli',
      'claude-code-cli',
    ]);
  });

  it('leaves unknown fingerprint profiles unset in the typed config', () => {
    const config = normalizeConfigResponse({
      'claude-api-key': [{ 'api-key': 'claude-secret', 'fingerprint-profile': 'claude-desktop' }],
    });

    expect(config.claudeApiKeys?.[0]).not.toHaveProperty('fingerprintProfile');
  });
});

describe('normalizeConfigResponse observability logs and pprof fields', () => {
  it('normalizes flat management config fields for logs retention and pprof', () => {
    const config = normalizeConfigResponse({
      debug: true,
      'request-log': true,
      'logging-to-file': false,
      'logs-max-total-size-mb': 128,
      'error-logs-max-files': 25,
      pprof: { enable: false, addr: '127.0.0.1:8316' },
    });

    expect(config.debug).toBe(true);
    expect(config.requestLog).toBe(true);
    expect(config.loggingToFile).toBe(false);
    expect(config.logsMaxTotalSizeMb).toBe(128);
    expect(config.errorLogsMaxFiles).toBe(25);
    expect(config.pprofEnable).toBe(false);
    expect(config.pprofAddr).toBe('127.0.0.1:8316');
  });

  it('falls back to nested v8 observability.logs and observability.pprof fields', () => {
    const config = normalizeConfigResponse({
      observability: {
        logs: {
          debug: false,
          'logging-to-file': true,
          'request-log': false,
          'logs-max-total-size-mb': 64,
          'error-logs-max-files': 0,
        },
        pprof: {
          enable: true,
          addr: '127.0.0.1:9000',
        },
      },
    });

    expect(config.debug).toBe(false);
    expect(config.loggingToFile).toBe(true);
    expect(config.requestLog).toBe(false);
    expect(config.logsMaxTotalSizeMb).toBe(64);
    expect(config.errorLogsMaxFiles).toBe(0);
    expect(config.pprofEnable).toBe(true);
    expect(config.pprofAddr).toBe('127.0.0.1:9000');
  });
});

describe('CPA v8 provider group normalization and alias fields', () => {
  it('normalizes trimmed name aliases on native Gemini and provider key configs', () => {
    expect(
      normalizeGeminiKeyConfig({
        name: '  Gemini Studio  ',
        'api-key': 'AIza-test',
      })
    ).toMatchObject({
      name: 'Gemini Studio',
      apiKey: 'AIza-test',
    });

    expect(
      normalizeProviderKeyConfig({
        name: '   ',
        'api-key': 'sk-ant-test',
      })
    ).toEqual({
      apiKey: 'sk-ant-test',
    });
  });

  it('flattens native v8 multi-key and endpoint-only groups with v8Source metadata', () => {
    const rawTree = {
      claude: [
        {
          name: 'Claude Cluster',
          'base-url': 'https://relay.anthropic.example',
          priority: 5,
          prefix: 'cc',
          'disable-cooling': true,
          keys: [
            {
              'api-key': 'sk-ant-1',
              weight: 2,
              auth_index: 'claude-idx-1',
              'fingerprint-profile': 'claude-code-cli',
            },
            {
              'api-key': 'sk-ant-2',
              weight: 4,
              'proxy-url': 'http://proxy.local:8080',
              auth_index: 'claude-idx-2',
            },
          ],
        },
        {
          name: 'Claude Endpoint Gateway',
          'base-url': 'https://gateway-only.example',
          keys: [{ 'api-key': '' }],
        },
      ],
    };

    const result = normalizeV8ProviderGroups(rawTree, 'claude', '"etag-123"');
    expect(result).toHaveLength(3);
    expect(result[0]).toMatchObject({
      name: 'Claude Cluster',
      apiKey: 'sk-ant-1',
      baseUrl: 'https://relay.anthropic.example',
      priority: 5,
      prefix: 'cc',
      weight: 2,
      disableCooling: true,
      fingerprintProfile: 'claude-code-cli',
      authIndex: 'claude-idx-1',
      v8Source: {
        family: 'claude',
        groupIndex: 0,
        keyIndex: 0,
        etag: '"etag-123"',
      },
    });
    expect(result[1]).toMatchObject({
      name: 'Claude Cluster',
      apiKey: 'sk-ant-2',
      proxyUrl: 'http://proxy.local:8080',
      weight: 4,
      authIndex: 'claude-idx-2',
      v8Source: {
        family: 'claude',
        groupIndex: 0,
        keyIndex: 1,
        etag: '"etag-123"',
      },
    });
    expect(result[2]).toMatchObject({
      name: 'Claude Endpoint Gateway',
      apiKey: '',
      baseUrl: 'https://gateway-only.example',
      v8Source: {
        family: 'claude',
        groupIndex: 1,
        keyIndex: 0,
        etag: '"etag-123"',
      },
    });
  });

  it('normalizes openai-compatibility v8 groups and nested api-keys maps in normalizeConfigResponse', () => {
    const config = normalizeConfigResponse({
      access: {
        'api-keys': ['client-key-1'],
      },
      'api-keys': {
        interactions: [
          {
            name: 'Interactions Primary',
            'base-url': 'https://interactions.example',
            keys: [{ 'api-key': '', auth_index: 'inter-idx-1' }],
          },
        ],
        'openai-compatibility': [
          {
            name: 'OpenRouter Main',
            'base-url': 'https://openrouter.ai/api/v1',
            prefix: 'or',
            priority: 3,
            disabled: false,
            'disable-cooling': true,
            'support-prompt-cache-key': true,
            auth_index: 'oa-group-idx',
            keys: [
              {
                'api-key': 'sk-or-1',
                'proxy-url': 'http://proxy:8080',
                weight: 5,
                auth_index: 'oa-key-idx-1',
              },
            ],
          },
        ],
      },
    });

    expect(config.apiKeys).toEqual(['client-key-1']);
    expect(config.interactionsApiKeys).toHaveLength(1);
    expect(config.interactionsApiKeys?.[0]).toMatchObject({
      name: 'Interactions Primary',
      apiKey: '',
      baseUrl: 'https://interactions.example',
      authIndex: 'inter-idx-1',
    });
    expect(config.openaiCompatibility).toHaveLength(1);
    expect(config.openaiCompatibility?.[0]).toMatchObject({
      name: 'OpenRouter Main',
      baseUrl: 'https://openrouter.ai/api/v1',
      prefix: 'or',
      priority: 3,
      disabled: false,
      disableCooling: true,
      supportPromptCacheKey: true,
      authIndex: 'oa-group-idx',
      apiKeyEntries: [
        {
          apiKey: 'sk-or-1',
          proxyUrl: 'http://proxy:8080',
          weight: 5,
          authIndex: 'oa-key-idx-1',
        },
      ],
      v8Source: {
        family: 'openai-compatibility',
        groupIndex: 0,
      },
    });
  });

  it('preserves empty Claude cloak, replacement maps/rules, extended model fields, and endpoint-only meta groups', () => {
    const emptyCloak = normalizeProviderKeyConfig({
      'api-key': 'sk-ant-empty-cloak',
      cloak: {},
    });
    expect(emptyCloak?.cloak).toEqual({});

    const fullCloak = normalizeProviderKeyConfig({
      'api-key': 'sk-ant-full-cloak',
      models: [
        {
          name: 'claude-sonnet-4-5',
          'display-name': 'Claude Sonnet 4.5',
          'max-context-length': 200000,
          'is-compat': true,
          openapi: true,
          'support-configuration-update': true,
          'use-max-completion-tokens': false,
        },
      ],
      cloak: {
        mode: 'strict',
        'strict-mode': true,
        'cache-user-id': true,
        'sensitive-words-replace': { internal: '', codename: 'public' },
        'sensitive-patterns-replace': [{ pattern: '(?m)^secret.*$', replacement: '' }],
      },
    });
    expect(fullCloak?.cloak).toEqual({
      mode: 'strict',
      strictMode: true,
      cacheUserId: true,
      sensitiveWordsReplace: { internal: '', codename: 'public' },
      sensitivePatternsReplace: [{ pattern: '(?m)^secret.*$', replacement: '' }],
    });
    expect(fullCloak?.models).toEqual([
      {
        name: 'claude-sonnet-4-5',
        displayName: 'Claude Sonnet 4.5',
        maxContextLength: 200000,
        isCompat: true,
        openapi: true,
        supportConfigurationUpdate: true,
        useMaxCompletionTokens: false,
      },
    ]);

    const v8Meta = normalizeV8ProviderGroups(
      {
        meta: [
          {
            name: 'Meta Gateway',
            'base-url': 'https://meta-gateway.example/v1',
            keys: [{ 'api-key': '', auth_index: 'meta-gw-1' }],
          },
        ],
      },
      'meta',
      '"meta-etag-1"'
    );
    expect(v8Meta).toHaveLength(1);
    expect(v8Meta[0]).toMatchObject({
      name: 'Meta Gateway',
      apiKey: '',
      baseUrl: 'https://meta-gateway.example/v1',
      authIndex: 'meta-gw-1',
      v8Source: { family: 'meta', etag: '"meta-etag-1"' },
    });

    const rawMetaConfig = normalizeConfigResponse({
      'meta-api-key': [{ 'api-key': '', 'base-url': 'https://meta-v0.example/v1', prefix: 'llama' }],
    });
    expect(rawMetaConfig.metaApiKeys).toEqual([
      {
        apiKey: '',
        baseUrl: 'https://meta-v0.example/v1',
        prefix: 'llama',
      },
    ]);
  });

  it('preserves keyless openai-compatibility entries with connection-pool records and strips weak ETag prefixes in normalizeV8ProviderGroups', () => {
    const normalizedOpenAI = normalizeV8ProviderGroups(
      [
        {
          name: 'Disabled Keyless Pool',
          disabled: true,
          'base-url': 'http://localhost:11434/v1',
          keys: [
            {
              'connection-pool': {
                enabled: true,
                'idle-conn-timeout': '60s',
              },
            },
            {
              connectionPool: {},
            },
            {
              connection_pool: { 'max-idle-conns-per-host': 4 },
            },
            {
              'connection-pool': null,
            },
            {},
          ],
        },
      ],
      'openai-compatibility',
      '  W/"oa-weak-etag-1"  '
    );

    expect(normalizedOpenAI).toHaveLength(1);
    expect(normalizedOpenAI[0].apiKeyEntries).toHaveLength(3);
    expect(normalizedOpenAI[0].apiKeyEntries.map((entry) => entry.apiKey)).toEqual(['', '', '']);
    expect(normalizedOpenAI[0].v8Source?.etag).toBe('"oa-weak-etag-1"');

    const normalizedClaude = normalizeV8ProviderGroups(
      [{ name: 'Claude Weak', keys: [{ 'api-key': 'sk-ant-weak' }] }],
      'claude',
      'w/"claude-weak-etag-2"'
    );
    expect(normalizedClaude).toHaveLength(1);
    expect(normalizedClaude[0].v8Source?.etag).toBe('"claude-weak-etag-2"');
  });
});
