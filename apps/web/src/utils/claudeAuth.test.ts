import { describe, expect, it } from 'vitest';
import {
  buildClaudeRequestHeaders,
  formatClaudeAuthDiagnostic,
  isAnthropicFirstPartyUrl,
} from './claudeAuth';

describe('Claude request authentication parity', () => {
  it.each([
    ['https://api.anthropic.com/v1/models', true],
    ['https://API.ANTHROPIC.COM/v1/models', true],
    ['https://api.anthropic.com:443/v1/models', true],
    ['https://api.anthropic.com:/v1/models', true],
    ['https://api.anthropic.com.evil.example/v1/models', false],
    ['https://api.anthropic.com./v1/models', false],
    ['https://api.anthropic.com:0443/v1/models', false],
    ['https://api.anthropic.com:00443/v1/models', false],
    ['https://api%2eanthropic.com/v1/models', false],
    ['https://api.anthropic.com\\v1/models', false],
    ['http://api.anthropic.com/v1/models', false],
    ['https://api.anthropic.com:8443/v1/models', false],
    ['https://user@api.anthropic.com/v1/models', false],
    ['https://@api.anthropic.com/v1/models', false],
  ] as const)('classifies Anthropic first-party URL %s', (url, expected) => {
    expect(isAnthropicFirstPartyUrl(url)).toBe(expected);
  });

  it('uses x-api-key for a normal API key on Anthropic first-party', () => {
    const result = buildClaudeRequestHeaders({
      url: 'https://api.anthropic.com/v1/models',
      apiKey: 'api-key',
    });
    expect(result.headers['x-api-key']).toBe('api-key');
    expect(result.effectiveAuthorization).toBe(false);
  });

  it('uses Bearer auth for a custom Claude upstream', () => {
    const result = buildClaudeRequestHeaders({
      url: 'https://gateway.example.com/v1/models',
      apiKey: 'api-key',
    });
    expect(result.headers.Authorization).toBe('Bearer api-key');
    expect(result.effectiveXApiKey).toBe(false);
  });

  it('uses Bearer auth for Claude OAuth tokens even on Anthropic first-party', () => {
    const result = buildClaudeRequestHeaders({
      url: 'https://api.anthropic.com/v1/models',
      apiKey: 'sk-ant-oat-test-token',
    });
    expect(result.headers.Authorization).toBe('Bearer sk-ant-oat-test-token');
    expect(result.headers['anthropic-beta']).toBe('oauth-2025-04-20');
    expect(result.effectiveXApiKey).toBe(false);
  });

  it('protects the OAuth beta from custom headers on Anthropic first-party', () => {
    const result = buildClaudeRequestHeaders({
      url: 'https://api.anthropic.com/v1/messages',
      apiKey: 'sk-ant-oat-test-token',
      customHeaders: { 'Anthropic-Beta': 'custom-beta' },
    });
    expect(result.headers['anthropic-beta']).toBe('oauth-2025-04-20');
    expect(result.headers).not.toHaveProperty('Anthropic-Beta');
    expect(
      Object.keys(result.headers).filter((key) => key.toLowerCase() === 'anthropic-beta')
    ).toHaveLength(1);
  });

  it('drops provider-level Anthropic-Beta for first-party API-key probes', () => {
    const result = buildClaudeRequestHeaders({
      url: 'https://api.anthropic.com/v1/messages',
      apiKey: 'api-key',
      customHeaders: { 'Anthropic-Beta': 'custom-beta' },
    });
    expect(
      Object.keys(result.headers).filter((key) => key.toLowerCase() === 'anthropic-beta')
    ).toHaveLength(0);
  });

  it('keeps custom Anthropic-Beta overrides for third-party Claude gateways', () => {
    const result = buildClaudeRequestHeaders({
      url: 'https://gateway.example.com/v1/messages',
      apiKey: 'sk-ant-oat-test-token',
      customHeaders: { 'Anthropic-Beta': 'custom-beta' },
    });
    expect(result.headers['Anthropic-Beta']).toBe('custom-beta');
    expect(
      Object.keys(result.headers).filter((key) => key.toLowerCase() === 'anthropic-beta')
    ).toHaveLength(1);
  });

  it('maps auth-index placeholders by upstream type', () => {
    const official = buildClaudeRequestHeaders({
      url: 'https://api.anthropic.com/v1/models',
      authIndex: 'auth-official',
    });
    const custom = buildClaudeRequestHeaders({
      url: 'https://gateway.example.com/v1/models',
      authIndex: 'auth-custom',
    });
    expect(official.headers['x-api-key']).toBe('$TOKEN$');
    expect(custom.headers.Authorization).toBe('Bearer $TOKEN$');
  });

  it('preserves header-only credentials without synthesizing the other auth header', () => {
    const authorization = buildClaudeRequestHeaders({
      url: 'https://gateway.example.com/v1/models',
      authIndex: 'auth-index',
      customHeaders: { authorization: 'Bearer custom-token' },
    });
    const apiKey = buildClaudeRequestHeaders({
      url: 'https://api.anthropic.com/v1/models',
      authIndex: 'auth-index',
      customHeaders: { 'X-Api-Key': 'custom-key' },
    });
    expect(authorization.headers.authorization).toBe('Bearer custom-token');
    expect(authorization.effectiveXApiKey).toBe(false);
    expect(apiKey.headers['X-Api-Key']).toBe('custom-key');
    expect(apiKey.effectiveAuthorization).toBe(false);
  });

  it('lets same-name custom headers override defaults case-insensitively', () => {
    const result = buildClaudeRequestHeaders({
      url: 'https://api.anthropic.com/v1/models',
      apiKey: 'api-key',
      customHeaders: {
        'X-API-KEY': 'override-key',
        'Anthropic-Version': '2099-01-01',
      },
    });
    expect(result.headers['X-API-KEY']).toBe('override-key');
    expect(result.headers['Anthropic-Version']).toBe('2099-01-01');
    expect(
      Object.keys(result.headers).filter((key) => key.toLowerCase() === 'x-api-key')
    ).toHaveLength(1);
    expect(
      Object.keys(result.headers).filter((key) => key.toLowerCase() === 'anthropic-version')
    ).toHaveLength(1);
  });

  it('keeps a different custom auth header in addition to the CPA default', () => {
    const result = buildClaudeRequestHeaders({
      url: 'https://gateway.example.com/v1/models',
      apiKey: 'api-key',
      customHeaders: { 'x-api-key': 'gateway-key' },
    });
    expect(result.headers.Authorization).toBe('Bearer api-key');
    expect(result.headers['x-api-key']).toBe('gateway-key');
    expect(result.effectiveAuthorization).toBe(true);
    expect(result.effectiveXApiKey).toBe(true);
  });

  it('reports effective auth without exposing credential values', () => {
    const result = buildClaudeRequestHeaders({
      url: 'https://gateway.example.com/v1/models',
      apiKey: 'secret-value',
    });
    const diagnostic = formatClaudeAuthDiagnostic(result, 'secret-value', 'auth-1');
    expect(diagnostic).toBe(
      '[diag: apiKeyField=yes, authIndex=yes, baseType=custom, effectiveXApiKey=no, effectiveAuthorization=yes]'
    );
    expect(diagnostic).not.toContain('secret-value');
  });
});
