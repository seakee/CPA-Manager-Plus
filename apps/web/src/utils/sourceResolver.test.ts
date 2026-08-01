import { describe, expect, it } from 'vitest';

import {
  buildSourceInfoMap,
  buildSourceProviderStateMap,
  resolveSourceDisplay,
} from './sourceResolver';

describe('source resolver', () => {
  it('resolves CPA masked Codex API key sources to readable base URL hosts', () => {
    const sourceInfoMap = buildSourceInfoMap({
      codexApiKeys: [
        {
          apiKey: 'sk-1234567890abcdef',
          baseUrl: 'https://api.first.example/v1',
        },
      ],
    });

    const resolved = resolveSourceDisplay('m:sk-1...cdef', '', sourceInfoMap, new Map());

    expect(resolved.displayName).toBe('api.first.example');
    expect(resolved.type).toBe('codex');
    expect(resolved.identityKey).toBe('codex:0');
  });

  it('resolves xAI API key sources independently from xAI OAuth credentials', () => {
    const sourceInfoMap = buildSourceInfoMap({
      xaiApiKeys: [
        {
          apiKey: 'xai-1234567890abcdef',
          authIndex: 'xai-api-key-1',
          prefix: 'Grok API',
          baseUrl: 'https://api.x.ai/v1',
        },
      ],
    });

    const resolved = resolveSourceDisplay('', 'xai-api-key-1', sourceInfoMap, new Map());

    expect(resolved.displayName).toBe('Grok API');
    expect(resolved.type).toBe('xai');
    expect(resolved.identityKey).toBe('xai:0');
  });

  it('keeps shared upstream names when one key is registered under Codex and Claude', () => {
    const sharedKey = 'sk-shared1234567890abcdef';
    const sourceInfoMap = buildSourceInfoMap({
      codexApiKeys: [
        {
          apiKey: sharedKey,
          prefix: 'Shared Relay',
          baseUrl: 'https://api.shared.example/v1',
        },
      ],
      claudeApiKeys: [
        {
          apiKey: sharedKey,
          prefix: 'Shared Relay',
          baseUrl: 'https://api.shared.example/v1',
        },
      ],
    });

    const resolved = resolveSourceDisplay('m:sk-s...cdef', '', sourceInfoMap, new Map());

    expect(resolved.displayName).toBe('Shared Relay');
    expect(resolved.type).toBe('');
    expect(resolved.identityKey).toBe('shared:m:sk-s...cdef');
    expect(resolved.displayName).not.toContain('sk-shared');
  });

  it('distinguishes multiple keys from the same base URL without exposing raw keys', () => {
    const sourceInfoMap = buildSourceInfoMap({
      codexApiKeys: [
        {
          apiKey: 'sk-111111111111aaaa',
          baseUrl: 'https://api.same.example/v1',
        },
        {
          apiKey: 'sk-222222222222bbbb',
          baseUrl: 'https://api.same.example/v1',
        },
      ],
    });

    const first = resolveSourceDisplay('m:sk-1...aaaa', '', sourceInfoMap, new Map());
    const second = resolveSourceDisplay('m:sk-2...bbbb', '', sourceInfoMap, new Map());

    expect(first.displayName).toBe('api.same.example #1');
    expect(second.displayName).toBe('api.same.example #2');
    expect(first.displayName).not.toContain('111111111111');
    expect(second.displayName).not.toContain('222222222222');
  });

  it('uses explicit provider names before base URL fallbacks for OpenAI compatible providers', () => {
    const sourceInfoMap = buildSourceInfoMap({
      openaiCompatibility: [
        {
          name: 'Primary Gateway',
          baseUrl: 'https://api.openai-compatible.example/v1',
          apiKeyEntries: [{ apiKey: 'sk-openai1234567890' }],
        },
      ],
    });

    const resolved = resolveSourceDisplay('m:sk-o...7890', '', sourceInfoMap, new Map());

    expect(resolved.displayName).toBe('Primary Gateway');
    expect(resolved.type).toBe('openai');
  });

  it('distinguishes OpenAI compatible providers that share the same base URL', () => {
    const sourceInfoMap = buildSourceInfoMap({
      openaiCompatibility: [
        {
          name: '',
          baseUrl: 'https://relay.same.example/v1',
          apiKeyEntries: [{ apiKey: 'sk-openai111111aaaa' }],
        },
        {
          name: '',
          baseUrl: 'https://relay.same.example/v1',
          apiKeyEntries: [{ apiKey: 'sk-openai222222bbbb' }],
        },
      ],
    });

    const first = resolveSourceDisplay('m:sk-o...aaaa', '', sourceInfoMap, new Map());
    const second = resolveSourceDisplay('m:sk-o...bbbb', '', sourceInfoMap, new Map());

    expect(first.displayName).toBe('relay.same.example #1');
    expect(second.displayName).toBe('relay.same.example #2');
    expect(first.type).toBe('openai');
    expect(second.type).toBe('openai');
  });

  it('distinguishes multiple keys under one OpenAI compatible provider', () => {
    const sourceInfoMap = buildSourceInfoMap({
      openaiCompatibility: [
        {
          name: '',
          baseUrl: 'https://relay.keys.example/v1',
          apiKeyEntries: [{ apiKey: 'sk-openai111111aaaa' }, { apiKey: 'sk-openai222222bbbb' }],
        },
      ],
    });

    const first = resolveSourceDisplay('m:sk-o...aaaa', '', sourceInfoMap, new Map());
    const second = resolveSourceDisplay('m:sk-o...bbbb', '', sourceInfoMap, new Map());

    expect(first.displayName).toBe('relay.keys.example #1');
    expect(second.displayName).toBe('relay.keys.example #2');
  });

  it('prefers OpenAI compatible key metadata when provider and key auth indices overlap', () => {
    const sourceInfoMap = buildSourceInfoMap({
      openaiCompatibility: [
        {
          name: 'Shared Relay',
          baseUrl: 'https://relay.auth.example/v1',
          authIndex: 'same-auth',
          apiKeyEntries: [{ apiKey: 'sk-openai111111aaaa', authIndex: 'same-auth' }],
        },
      ],
    });

    const resolved = resolveSourceDisplay('', 'same-auth', sourceInfoMap, new Map());

    expect(resolved.displayName).toBe('Shared Relay');
    expect(resolved.identityKey).toBe('openai:0:0');
  });

  it('does not echo unsafe m-prefixed raw secrets as source fallback', () => {
    const sourceInfoMap = buildSourceInfoMap({});

    const resolved = resolveSourceDisplay('m:sk-realsecret', '', sourceInfoMap, new Map());

    expect(resolved.displayName).toMatch(/^k:/);
    expect(resolved.displayName).not.toContain('sk-realsecret');
  });

  it('resolves legacy UI-masked usage source IDs without treating them as raw secrets', () => {
    const sourceInfoMap = buildSourceInfoMap({
      codexApiKeys: [
        {
          apiKey: 'sk-1234567890abcdef',
          baseUrl: 'https://api.first.example/v1',
        },
      ],
    });

    const resolved = resolveSourceDisplay('m:sk******ef', '', sourceInfoMap, new Map());

    expect(resolved.displayName).toBe('api.first.example');
    expect(resolved.type).toBe('codex');
  });

  it('keeps raw source fallback when no provider candidate matches', () => {
    const sourceInfoMap = buildSourceInfoMap({
      codexApiKeys: [
        {
          apiKey: 'sk-1234567890abcdef',
          baseUrl: 'https://api.first.example/v1',
        },
      ],
    });

    const resolved = resolveSourceDisplay('m:sk-x...zzzz', '', sourceInfoMap, new Map());

    expect(resolved.displayName).toBe('m:sk-x...zzzz');
    expect(resolved.identityKey).toBe('source:m:sk-x...zzzz');
  });

  it('exposes enabled and disabled OpenAI compatible provider states', () => {
    const sourceInfoMap = buildSourceInfoMap({
      openaiCompatibility: [
        {
          name: 'Enabled Relay',
          baseUrl: 'https://enabled.example/v1',
          authIndex: 'enabled-openai',
          apiKeyEntries: [],
        },
        {
          name: 'Disabled Relay',
          baseUrl: 'https://disabled.example/v1',
          authIndex: 'disabled-openai',
          apiKeyEntries: [],
          disabled: true,
        },
      ],
    });

    const stateMap = buildSourceProviderStateMap(sourceInfoMap);

    expect(stateMap.get('openai:0')).toBe('enabled');
    expect(stateMap.get('openai:1')).toBe('disabled');
  });

  it('exposes disabled non-OpenAI provider state from disable-all excluded models', () => {
    const sourceInfoMap = buildSourceInfoMap({
      codexApiKeys: [
        {
          apiKey: 'sk-disabled-codex',
          excludedModels: ['*'],
        },
      ],
    });

    const stateMap = buildSourceProviderStateMap(sourceInfoMap);

    expect(stateMap.get('codex:0')).toBe('disabled');
  });

  it('marks shared provider identities as mixed when matched providers differ', () => {
    const sharedKey = 'sk-shared-provider-state';
    const sourceInfoMap = buildSourceInfoMap({
      codexApiKeys: [
        {
          apiKey: sharedKey,
          prefix: 'Shared Relay',
        },
      ],
      claudeApiKeys: [
        {
          apiKey: sharedKey,
          prefix: 'Shared Relay',
          excludedModels: ['*'],
        },
      ],
    });

    const resolved = resolveSourceDisplay('m:sk-s...tate', '', sourceInfoMap, new Map());
    const stateMap = buildSourceProviderStateMap(sourceInfoMap);
    const identityKey = resolved.identityKey || '';

    expect(identityKey).toBe('shared:m:sk-s...tate');
    expect(stateMap.get(identityKey)).toBe('mixed');
  });
});
