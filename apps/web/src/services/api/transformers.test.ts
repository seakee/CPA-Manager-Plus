import { describe, expect, it } from 'vitest';
import { normalizeConfigResponse } from './transformers';

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
