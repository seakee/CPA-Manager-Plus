import { describe, expect, it } from 'vitest';
import type { TFunction } from 'i18next';
import { buildRealtimeSourceDisplay } from './realtimeSourceDisplay';

const labels: Record<string, string> = {
  'monitoring.filter_provider': 'Provider',
  'monitoring.column_host': 'Host',
  'monitoring.source': 'Source',
  'monitoring.client_ip': 'Client IP',
  'monitoring.x_forwarded_for_unverified': 'Forwarded chain (unverified)',
  'monitoring.user_agent': 'User-Agent',
};

const t = ((key: string) => labels[key] || key) as TFunction;

const row = {
  account: 'alice@example.com',
  accountMasked: 'ali***@example.com',
  apiKeyLabel: '-',
  apiKeyMasked: '-',
  authLabel: 'alice',
  channel: 'codex',
  channelHost: 'api.openai.com',
  clientIp: '192.0.2.10',
  provider: 'codex',
  source: 'alice@example.com',
  sourceMasked: 'ali***@example.com',
  userAgent: 'test-client/1.0',
  xForwardedFor: '203.0.113.5, 198.51.100.8',
};

describe('buildRealtimeSourceDisplay request metadata', () => {
  it('does not render request metadata in masked mode', () => {
    const display = buildRealtimeSourceDisplay(row, t, 'masked');

    expect(display.requestMetadataTitle).toBe('');
    expect(display.title).not.toContain('192.0.2.10');
    expect(display.title).not.toContain('203.0.113.5');
    expect(display.title).not.toContain('test-client/1.0');
  });

  it('renders labeled request metadata in full mode', () => {
    const display = buildRealtimeSourceDisplay(row, t, 'full');

    expect(display.requestMetadataTitle).toBe(
      [
        'Client IP: 192.0.2.10',
        'Forwarded chain (unverified): 203.0.113.5, 198.51.100.8',
        'User-Agent: test-client/1.0',
      ].join('\n')
    );
    expect(display.title).toContain('Client IP: 192.0.2.10');
    expect(display.title).toContain(
      'Forwarded chain (unverified): 203.0.113.5, 198.51.100.8'
    );
    expect(display.title).toContain('User-Agent: test-client/1.0');
  });
});

describe('buildRealtimeSourceDisplay source identity', () => {
  it('uses a configured API key alias instead of an internal source fingerprint', () => {
    const fingerprint = 'k:0123456789abcdef';
    const display = buildRealtimeSourceDisplay(
      {
        ...row,
        account: fingerprint,
        accountMasked: fingerprint,
        apiKeyLabel: 'Automation client',
        apiKeyMasked: 'sk-...cdef',
        authLabel: fingerprint,
        channel: 'codex',
        channelHost: '-',
        provider: 'codex',
        source: fingerprint,
        sourceMasked: fingerprint,
      },
      t
    );

    expect(display.primary).toBe('Automation client');
    expect(display.meta).toBe('Provider: codex');
    expect(display.title).toContain(fingerprint);
  });

  it('keeps an internal source fingerprint as the final fallback', () => {
    const fingerprint = 'h:0123456789abcdef';
    const display = buildRealtimeSourceDisplay(
      {
        ...row,
        account: fingerprint,
        accountMasked: fingerprint,
        authLabel: fingerprint,
        channel: '-',
        channelHost: '-',
        provider: '-',
        source: fingerprint,
        sourceMasked: fingerprint,
      },
      t
    );

    expect(display.primary).toBe(fingerprint);
  });
});
