import { describe, expect, it } from 'vitest';
import type { AuthFileItem } from '@/types';
import {
  AUTH_FILE_CONFIGURATION_TARGET_NOT_FOUND,
  XAI_OFFICIAL_API_BASE_URL,
  buildAuthFileConfigurationDraft,
  buildAuthFileConfigurationPatch,
  buildRedactedAuthFileConfigurationText,
  getAuthFileConfigurationCapabilities,
  parseAuthFileConfigurationSource,
  type AuthFileConfigurationDraft,
} from './authFileConfiguration';

const makeFile = (overrides: Partial<AuthFileItem> = {}): AuthFileItem =>
  ({
    name: 'shared.json',
    type: 'codex',
    provider: 'codex',
    authIndex: 'auth-1',
    account: 'one@example.com',
    ...overrides,
  }) as AuthFileItem;

describe('authFileConfiguration provider capabilities', () => {
  it('keeps provider-only OAuth fields distinct', () => {
    expect(getAuthFileConfigurationCapabilities('codex')).toEqual({
      websockets: true,
      xaiRouting: false,
      claudeCloak: false,
    });
    expect(getAuthFileConfigurationCapabilities('grok')).toEqual({
      websockets: true,
      xaiRouting: true,
      claudeCloak: false,
    });
    expect(getAuthFileConfigurationCapabilities('claude')).toEqual({
      websockets: false,
      xaiRouting: false,
      claudeCloak: true,
    });
    expect(getAuthFileConfigurationCapabilities('gemini')).toEqual({
      websockets: false,
      xaiRouting: false,
      claudeCloak: false,
    });
  });
});

describe('parseAuthFileConfigurationSource', () => {
  it('selects one record from a shared source by explicit auth index', () => {
    const parsed = parseAuthFileConfigurationSource(
      JSON.stringify([
        { type: 'codex', auth_index: 'auth-1', account: 'one@example.com' },
        { type: 'codex', auth_index: 'auth-2', account: 'two@example.com' },
      ]),
      makeFile({ authIndex: 'auth-2', account: 'two@example.com' })
    );

    expect(parsed.recordIndex).toBe(1);
    expect(parsed.record).toMatchObject({ auth_index: 'auth-2' });
    expect(parsed.providerKey).toBe('codex');
  });

  it('uses the array position when a shared source has no explicit auth index', () => {
    const parsed = parseAuthFileConfigurationSource(
      JSON.stringify([
        { type: 'claude', account: 'one@example.com' },
        { type: 'claude', account: 'two@example.com' },
      ]),
      makeFile({ type: 'claude', provider: 'claude', authIndex: '1', account: 'two@example.com' })
    );

    expect(parsed.recordIndex).toBe(1);
    expect(parsed.record).toMatchObject({ account: 'two@example.com' });
  });

  it('fails closed when the selected shared credential cannot be matched uniquely', () => {
    expect(() =>
      parseAuthFileConfigurationSource(
        JSON.stringify([
          { type: 'codex', account: 'same@example.com' },
          { type: 'codex', account: 'same@example.com' },
        ]),
        makeFile({ authIndex: 'missing', account: 'same@example.com' })
      )
    ).toThrow(AUTH_FILE_CONFIGURATION_TARGET_NOT_FOUND);
  });

  it('accepts the CPA gemini and gemini-cli provider aliases as one identity', () => {
    const parsed = parseAuthFileConfigurationSource(
      JSON.stringify({ type: 'gemini', auth_index: 'auth-1' }),
      makeFile({ type: 'gemini-cli', provider: 'gemini-cli' })
    );

    expect(parsed.providerKey).toBe('gemini');
  });

  it('rejects an explicit single-record provider or account conflict', () => {
    expect(() =>
      parseAuthFileConfigurationSource(
        JSON.stringify({
          type: 'claude',
          auth_index: 'auth-1',
          account_id: 'different-account',
        }),
        makeFile({ account_id: 'expected-account' })
      )
    ).toThrow(AUTH_FILE_CONFIGURATION_TARGET_NOT_FOUND);
  });

  it('allows a single record that omits display-only identity fields', () => {
    const parsed = parseAuthFileConfigurationSource(
      JSON.stringify({ type: 'codex', note: 'source without account metadata' }),
      makeFile()
    );

    expect(parsed.record).toMatchObject({ note: 'source without account metadata' });
  });

  it('rejects an array auth-index match when its account identity conflicts', () => {
    expect(() =>
      parseAuthFileConfigurationSource(
        JSON.stringify([
          { type: 'codex', auth_index: 'auth-1', account_id: 'different-account' },
          { type: 'codex', auth_index: 'auth-2', account_id: 'other-account' },
        ]),
        makeFile({ account_id: 'expected-account' })
      )
    ).toThrow(AUTH_FILE_CONFIGURATION_TARGET_NOT_FOUND);
  });

  it('does not fall back to a weaker sparse record after an indexed identity conflict', () => {
    expect(() =>
      parseAuthFileConfigurationSource(
        JSON.stringify([
          { type: 'codex', auth_index: 'auth-1', account_id: 'replacement-account' },
          { type: 'codex', note: 'sparse unrelated record' },
        ]),
        makeFile({ account_id: 'expected-account' })
      )
    ).toThrow(AUTH_FILE_CONFIGURATION_TARGET_NOT_FOUND);
  });

  it('matches Codex shared records by member when the auth index is absent', () => {
    const parsed = parseAuthFileConfigurationSource(
      JSON.stringify([
        { type: 'codex', account_id: 'workspace-1', account: 'alice@example.com' },
        { type: 'codex', account_id: 'workspace-1', account: 'bob@example.com' },
      ]),
      makeFile({ authIndex: 'missing', account_id: 'workspace-1', account: 'bob@example.com' })
    );

    expect(parsed.recordIndex).toBe(1);
    expect(parsed.record).toMatchObject({ account: 'bob@example.com' });
  });

  it('rejects a Codex record with conflicting explicit member evidence', () => {
    expect(() =>
      parseAuthFileConfigurationSource(
        JSON.stringify({
          type: 'codex',
          account_id: 'workspace-1',
          account_snapshot: 'bob@example.com',
          account: 'alice@example.com',
        }),
        makeFile({ account_id: 'workspace-1', account: 'alice@example.com' })
      )
    ).toThrow(AUTH_FILE_CONFIGURATION_TARGET_NOT_FOUND);
  });

  it('rejects a Codex record with a conflicting auth index before member fallback', () => {
    expect(() =>
      parseAuthFileConfigurationSource(
        JSON.stringify({
          type: 'codex',
          auth_index: 'auth-a',
          account_id: 'workspace-1',
          account: 'alice@example.com',
        }),
        makeFile({ authIndex: 'auth-b', account_id: 'workspace-1', account: 'alice@example.com' })
      )
    ).toThrow(AUTH_FILE_CONFIGURATION_TARGET_NOT_FOUND);
  });
});

describe('buildAuthFileConfigurationPatch', () => {
  it.each([
    [{ disable_cooling: true }, 'disabled'],
    [{ disable_cooling: false }, 'enabled'],
    [{ disable_cooling: null }, 'inherit'],
    [{}, 'inherit'],
  ] as const)('normalizes %j to the %s policy', (record, expected) => {
    expect(buildAuthFileConfigurationDraft(record, 'codex').disableCooling).toBe(expected);
  });

  it.each([
    [{ disable_cooling: null, 'disable-cooling': true }, 'disabled'],
    [{ disable_cooling: 'invalid', 'disable-cooling': false }, 'enabled'],
    [{ disable_cooling: 't' }, 'disabled'],
    [{ disable_cooling: 'f' }, 'enabled'],
    [{ disable_cooling: 'yes' }, 'inherit'],
    [{ disableCooling: true }, 'inherit'],
  ] as const)('matches CPA credential alias parsing for %j', (record, expected) => {
    expect(buildAuthFileConfigurationDraft(record, 'codex').disableCooling).toBe(expected);
  });

  it.each([
    ['inherit', 'enabled', false],
    ['inherit', 'disabled', true],
    ['enabled', 'inherit', null],
    ['disabled', 'inherit', null],
    ['enabled', 'disabled', true],
    ['disabled', 'enabled', false],
  ] as const)('writes %s -> %s as an explicit CPA override', (from, to, expected) => {
    const sourceRecord = from === 'inherit' ? {} : { disable_cooling: from === 'disabled' };
    const original = buildAuthFileConfigurationDraft(sourceRecord, 'codex');
    const result = buildAuthFileConfigurationPatch(sourceRecord, 'codex', original, {
      ...original,
      disableCooling: to,
    });

    expect(result.errors).toEqual({});
    expect(result.patch.disable_cooling).toBe(expected);
  });

  it('normalizes stored zero priority to the empty default representation', () => {
    expect(buildAuthFileConfigurationDraft({ priority: 0 }, 'codex').priority).toBe('');
    expect(buildAuthFileConfigurationDraft({ priority: '-0' }, 'codex').priority).toBe('');
    expect(buildAuthFileConfigurationDraft({ priority: -1 }, 'codex').priority).toBe('-1');
  });

  it('builds a minimal common patch while preserving explicit zero values', () => {
    const record = {
      type: 'gemini',
      prefix: 'old',
      priority: 2,
      weight: 5,
      request_retry: 3,
      excluded_models: ['model-a'],
      headers: { 'X-Keep': 'one', 'X-Remove': 'two' },
    };
    const original = buildAuthFileConfigurationDraft(record, 'gemini');
    const next: AuthFileConfigurationDraft = {
      ...original,
      prefix: 'team',
      priority: '',
      weight: '-4',
      requestRetry: '0',
      excludedModelsText: 'MODEL-B\nmodel-a\nmodel-b',
      headersText: JSON.stringify({ 'X-Keep': 'updated' }),
      disableCooling: 'disabled',
    };

    expect(buildAuthFileConfigurationPatch(record, 'gemini', original, next)).toEqual({
      errors: {},
      patch: {
        prefix: 'team',
        priority: 0,
        weight: 0,
        request_retry: 0,
        'excluded-models': ['model-a', 'model-b'],
        excluded_models: null,
        headers: { 'X-Keep': 'updated', 'X-Remove': '' },
        disable_cooling: true,
      },
    });
  });

  it('matches CPA runtime precedence when both excluded-model aliases exist', () => {
    const record = {
      type: 'codex',
      'excluded-models': ['canonical-model'],
      excluded_models: ['legacy-model'],
    };
    const original = buildAuthFileConfigurationDraft(record, 'codex');

    expect(original.excludedModelsText).toBe('legacy-model');
  });

  it('switches xAI routing fields atomically without erasing an inactive URL on unrelated edits', () => {
    const grokRecord = {
      type: 'xai',
      using_api: false,
      base_url: 'https://stored.example/v1',
      note: 'old',
    };
    const grokOriginal = buildAuthFileConfigurationDraft(grokRecord, 'xai');

    expect(
      buildAuthFileConfigurationPatch(grokRecord, 'xai', grokOriginal, {
        ...grokOriginal,
        note: 'updated',
      }).patch
    ).toEqual({ note: 'updated' });

    expect(
      buildAuthFileConfigurationPatch(grokRecord, 'xai', grokOriginal, {
        ...grokOriginal,
        xaiRoutingMode: 'official-api',
        baseUrl: '',
      }).patch
    ).toEqual({ using_api: true, base_url: XAI_OFFICIAL_API_BASE_URL });

    const apiRecord = {
      type: 'xai',
      using_api: true,
      base_url: 'https://api.x.ai/v1',
    };
    const apiOriginal = buildAuthFileConfigurationDraft(apiRecord, 'xai');
    expect(
      buildAuthFileConfigurationPatch(apiRecord, 'xai', apiOriginal, {
        ...apiOriginal,
        xaiRoutingMode: 'grok-build',
      }).patch
    ).toEqual({ using_api: false, base_url: '' });
  });

  it('exposes and edits a custom xAI gateway while Grok Build remains selected', () => {
    const record = {
      type: 'xai',
      using_api: false,
      base_url: 'https://gateway.example/v1',
    };
    const original = buildAuthFileConfigurationDraft(record, 'xai');

    expect(original.baseUrl).toBe('https://gateway.example/v1');
    expect(
      buildAuthFileConfigurationPatch(record, 'xai', original, {
        ...original,
        baseUrl: 'https://gateway.example/v2',
      })
    ).toEqual({
      errors: {},
      patch: { base_url: 'https://gateway.example/v2' },
    });
  });

  it('serializes Claude-only cloak fields using executor-compatible values', () => {
    const record = { type: 'claude' };
    const original = buildAuthFileConfigurationDraft(record, 'claude');
    const result = buildAuthFileConfigurationPatch(record, 'claude', original, {
      ...original,
      cloakMode: 'always',
      cloakStrictMode: true,
      cloakSensitiveWordsText: 'secret\ninternal,secret',
      cloakCacheUserId: true,
      toolPrefixDisabled: true,
    });

    expect(result).toEqual({
      errors: {},
      patch: {
        cloak_mode: 'always',
        cloak_strict_mode: 'true',
        cloak_sensitive_words: 'secret,internal',
        cloak_cache_user_id: 'true',
        tool_prefix_disabled: true,
      },
    });
  });

  it('tombstones legacy aliases when their canonical configuration changes', () => {
    const record = {
      type: 'claude',
      proxyUrl: 'http://legacy-proxy.local',
      excludedModels: ['model-a'],
      excluded_models: ['model-b'],
      'request-retry': 2,
      'tool-prefix-disabled': true,
    };
    const original = buildAuthFileConfigurationDraft(record, 'claude');

    expect(
      buildAuthFileConfigurationPatch(record, 'claude', original, {
        ...original,
        proxyUrl: '',
        excludedModelsText: '',
        requestRetry: '',
        toolPrefixDisabled: false,
      })
    ).toEqual({
      errors: {},
      patch: {
        proxy_url: '',
        proxyUrl: null,
        'excluded-models': [],
        excludedModels: null,
        excluded_models: null,
        request_retry: null,
        'request-retry': null,
        tool_prefix_disabled: false,
        'tool-prefix-disabled': null,
      },
    });
  });

  it('preserves a custom xAI base URL when routing returns to Grok Build', () => {
    const record = {
      type: 'xai',
      using_api: true,
      baseUrl: 'https://legacy.example/v1',
    };
    const original = buildAuthFileConfigurationDraft(record, 'xai');

    expect(
      buildAuthFileConfigurationPatch(record, 'xai', original, {
        ...original,
        xaiRoutingMode: 'grok-build',
      })
    ).toEqual({
      errors: {},
      patch: { using_api: false, base_url: 'https://legacy.example/v1', baseUrl: null },
    });
  });

  it('reports validation errors without emitting invalid fields', () => {
    const record = { type: 'xai', using_api: false };
    const original = buildAuthFileConfigurationDraft(record, 'xai');
    const result = buildAuthFileConfigurationPatch(record, 'xai', original, {
      ...original,
      priority: '1.5',
      weight: '1000001',
      requestRetry: '-1',
      xaiRoutingMode: 'official-api',
      baseUrl: 'file:///tmp/xai',
    });

    expect(result.patch).toEqual({ using_api: true });
    expect(result.errors).toEqual({
      priority: 'accounts.config_error_priority_integer',
      weight: 'accounts.config_error_weight_range',
      requestRetry: 'accounts.config_error_request_retry_integer',
      baseUrl: 'accounts.config_error_xai_base_url',
    });
  });
});

describe('buildRedactedAuthFileConfigurationText', () => {
  it('preserves provider-specific structure while redacting credentials and Header values', () => {
    const preview = JSON.parse(
      buildRedactedAuthFileConfigurationText({
        type: 'xai',
        using_api: true,
        base_url: 'https://api.x.ai/v1',
        access_token: 'access-value',
        session_key: 'session-value',
        nested: { refreshToken: 'refresh-value', safe: 'visible' },
        headers: { Authorization: 'Bearer secret', 'X-Team': 'internal' },
      })
    ) as Record<string, unknown>;

    expect(preview).toMatchObject({
      type: 'xai',
      using_api: true,
      base_url: 'https://api.x.ai/v1',
      access_token: '[redacted]',
      session_key: '[redacted]',
      nested: { refreshToken: '[redacted]', safe: 'visible' },
      headers: { Authorization: '[redacted]', 'X-Team': '[redacted]' },
    });
  });

  it('redacts OAuth handoff and session identifiers in nested records', () => {
    const preview = JSON.parse(
      buildRedactedAuthFileConfigurationText({
        code_verifier: 'verifier-secret',
        authorization_code: 'authorization-secret',
        session_id: 'session-secret',
        safe: 'visible',
      })
    ) as Record<string, unknown>;

    expect(preview).toMatchObject({
      code_verifier: '[redacted]',
      authorization_code: '[redacted]',
      session_id: '[redacted]',
      safe: 'visible',
    });
  });

  it('redacts plural token and key collections before serializing raw data', () => {
    const preview = JSON.parse(
      buildRedactedAuthFileConfigurationText({
        access_tokens: ['token-one', 'token-two'],
        signing_keys: [{ kid: 'one', value: 'private-key-material' }],
        nested: { refresh_tokens: ['refresh-one'] },
        safe_values: ['visible'],
      })
    ) as Record<string, unknown>;

    expect(preview).toEqual({
      access_tokens: '[redacted]',
      signing_keys: '[redacted]',
      nested: { refresh_tokens: '[redacted]' },
      safe_values: ['visible'],
    });
  });

  it('removes credentials, query parameters, and fragments from proxy URLs', () => {
    const preview = JSON.parse(
      buildRedactedAuthFileConfigurationText({
        proxy_url: 'socks5://username:password@proxy.example:1080/path?token=secret#private',
        proxyUrl: 'not-a-safe-proxy-value',
      })
    ) as Record<string, unknown>;

    expect(preview).toEqual({
      proxy_url: 'socks5://proxy.example:1080/path',
      proxyUrl: '[redacted]',
    });
  });

  it('sanitizes credentials and request metadata from every parseable URL string', () => {
    const preview = JSON.parse(
      buildRedactedAuthFileConfigurationText({
        base_url: 'https://user:password@api.example/v1?api_key=secret#private',
        nested: {
          endpoint: 'wss://socket-user:socket-pass@stream.example/events?ticket=secret#trace',
          callback_url: 'not-a-valid-url?token=secret',
          note: 'keep this non-URL value?yes',
        },
      })
    ) as Record<string, unknown>;

    expect(preview).toEqual({
      base_url: 'https://api.example/v1',
      nested: {
        endpoint: 'wss://stream.example/events',
        callback_url: '[redacted]',
        note: 'keep this non-URL value?yes',
      },
    });
  });
});
