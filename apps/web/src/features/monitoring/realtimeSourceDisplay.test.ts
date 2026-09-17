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
    expect(display.title).toContain('Forwarded chain (unverified): 203.0.113.5, 198.51.100.8');
    expect(display.title).toContain('User-Agent: test-client/1.0');
  });
});

describe('buildRealtimeSourceDisplay provider labels', () => {
  it('renders commandcode as Command Code', () => {
    const display = buildRealtimeSourceDisplay(
      {
        source: 'Command Code / 4',
        sourceMasked: 'Command Code / 4',
        account: 'Command Code / 4',
        accountMasked: 'Command Code / 4',
        authLabel: 'Command Code / 4',
        channel: 'commandcode',
        channelHost: '',
        provider: 'commandcode',
      },
      t,
      'full'
    );

    expect(display.primary).toBe('Command Code / 4');
    expect(display.meta).toBe('Provider: Command Code');
  });
});

describe('buildRealtimeSourceDisplay opaque source priority (#781)', () => {
  it('prefers readable host over k:<fingerprint> opaque source (Case 1)', () => {
    const display = buildRealtimeSourceDisplay(
      {
        source: 'k:0123456789abcdef',
        sourceMasked: 'k:0123456789abcdef',
        channelHost: 'readable.example.com',
        channel: 'codex',
        provider: 'codex',
        account: '',
        accountMasked: '',
        authLabel: '',
      },
      t,
      'masked'
    );

    expect(display.primary).toBe('readable.example.com');
    expect(display.primary).not.toContain('k:0123456789abcdef');
    expect(display.meta).toBe('Provider: codex');
    expect(display.title).toContain('k:0123456789abcdef');
  });

  it('prefers readable account/auth label over h:<64 hex> opaque source (Case 2)', () => {
    const validHash = '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef';
    const display = buildRealtimeSourceDisplay(
      {
        source: `h:${validHash}`,
        sourceMasked: `h:${validHash}`,
        authLabel: 'readable-team-account',
        account: 'readable-team-account',
        accountMasked: 'readable-team-account',
        provider: 'codex',
        channel: 'codex',
        channelHost: '',
      },
      t,
      'masked'
    );

    expect(display.primary).toBe('readable-team-account');
    expect(display.primary).not.toContain('h:');
  });

  it('prefers readable channel/config name over m:<masked> source (Case 3)', () => {
    const display = buildRealtimeSourceDisplay(
      {
        source: 'm:sk-1...cdef',
        sourceMasked: 'm:sk-1...cdef',
        channel: 'custom-production-channel',
        channelHost: '',
        provider: 'codex',
        account: '',
        accountMasked: '',
        authLabel: '',
      },
      t,
      'masked'
    );

    expect(display.primary).toBe('custom-production-channel');
    expect(display.primary).not.toContain('m:sk-1...cdef');
  });

  it('falls back to opaque identity when no readable metadata exists (Case 4)', () => {
    const validHash = '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef';
    const hashFallback = buildRealtimeSourceDisplay(
      {
        source: `h:${validHash}`,
        sourceMasked: `h:${validHash}`,
        channel: '',
        channelHost: '',
        provider: '',
        account: '',
        accountMasked: '',
        authLabel: '',
      },
      t,
      'masked'
    );
    expect(hashFallback.primary).toBe(`h:${validHash}`);

    const keyFallback = buildRealtimeSourceDisplay(
      {
        source: 'k:abcdef0123456789',
        sourceMasked: 'k:abcdef0123456789',
        channel: '',
        channelHost: '',
        provider: '',
        account: '',
        accountMasked: '',
        authLabel: '',
      },
      t,
      'masked'
    );
    expect(keyFallback.primary).toBe('k:abcdef0123456789');

    const maskedFallback = buildRealtimeSourceDisplay(
      {
        source: 'm:sk-1...cdef',
        sourceMasked: 'm:sk-1...cdef',
        channel: '',
        channelHost: '',
        provider: '',
        account: '',
        accountMasked: '',
        authLabel: '',
      },
      t,
      'masked'
    );
    expect(maskedFallback.primary).toBe('m:sk-1...cdef');
  });
});

describe('buildRealtimeSourceDisplay dynamic/unknown provider display (#686)', () => {
  it('prefers credential account over unknown provider when channel and source equal provider', () => {
    const display = buildRealtimeSourceDisplay(
      {
        account: 'marscosmo',
        accountMasked: 'marscosmo',
        authLabel: 'workbuddy',
        channel: 'workbuddy',
        channelHost: '',
        provider: 'workbuddy',
        source: 'workbuddy',
        sourceMasked: 'workbuddy',
      },
      t,
      'masked'
    );

    expect(display.primary).toBe('marscosmo');
    expect(display.meta).toBe('Provider: workbuddy');

    const displayFull = buildRealtimeSourceDisplay(
      {
        account: 'marscosmo',
        accountMasked: 'marscosmo',
        authLabel: 'workbuddy',
        channel: 'workbuddy',
        channelHost: '',
        provider: 'workbuddy',
        source: 'workbuddy',
        sourceMasked: 'workbuddy',
      },
      t,
      'full'
    );

    expect(displayFull.primary).toBe('marscosmo');
    expect(displayFull.meta).toBe('Provider: workbuddy');
  });

  it('preserves distinct custom channel name over account for unknown provider', () => {
    const display = buildRealtimeSourceDisplay(
      {
        account: 'marscosmo',
        accountMasked: 'marscosmo',
        authLabel: 'workbuddy',
        channel: 'Team WorkBuddy Relay',
        channelHost: '',
        provider: 'workbuddy',
        source: 'workbuddy',
        sourceMasked: 'workbuddy',
      },
      t,
      'masked'
    );

    expect(display.primary).toBe('Team WorkBuddy Relay');
    expect(display.meta).toBe('Provider: workbuddy');
  });

  it('preserves specific custom source over account for unknown provider', () => {
    const display = buildRealtimeSourceDisplay(
      {
        account: 'marscosmo',
        accountMasked: 'marscosmo',
        authLabel: 'workbuddy',
        channel: 'workbuddy',
        channelHost: '',
        provider: 'workbuddy',
        source: 'Team Credential',
        sourceMasked: 'Team Credential',
      },
      t,
      'masked'
    );

    expect(display.primary).toBe('Team Credential');
    expect(display.meta).toBe('Provider: workbuddy');
  });

  it('falls back to unknown provider rather than opaque hash when no account exists', () => {
    const validHash = '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef';
    const display = buildRealtimeSourceDisplay(
      {
        provider: 'future-provider',
        channel: 'future-provider',
        source: `h:${validHash}`,
        sourceMasked: `h:${validHash}`,
        account: '',
        accountMasked: '',
        authLabel: '',
        channelHost: '',
      },
      t,
      'masked'
    );

    expect(display.primary).toBe('future-provider');
    expect(display.meta).toBe('Provider: future-provider');
  });
});
