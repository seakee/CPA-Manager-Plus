import { describe, expect, it } from 'vitest';
import { buildMonitoringAccountRowId, normalizeMonitoringProvider } from './accountIdentity';

describe('monitoring account identity', () => {
  it('uses provider-scoped logical account identity without auth-index splitting', () => {
    const codexA = buildMonitoringAccountRowId({
      provider: 'codex',
      account: 'same@example.com',
      authIndex: 'auth-a',
    });
    const codexB = buildMonitoringAccountRowId({
      provider: 'codex',
      account: 'same@example.com',
      authIndex: 'auth-b',
    });
    const antigravity = buildMonitoringAccountRowId({
      provider: 'antigravity',
      account: 'same@example.com',
      authIndex: 'auth-a',
    });

    expect(codexA).toBe(codexB);
    expect(codexA).not.toBe(antigravity);
    expect(codexA).toBe('monitoring-account:1:account:636F646578:73616D65406578616D706C652E636F6D');
  });

  it('normalizes provider values without alias folding and avoids delimiter collisions', () => {
    expect(normalizeMonitoringProvider(' CODEX ')).toBe('codex');
    expect(normalizeMonitoringProvider('foo_bar')).toBe('foo_bar');
    expect(normalizeMonitoringProvider(' Provider_Name ')).toBe('provider_name');
    // Monitoring identity deliberately does NOT fold x-ai/grok -> xai.
    expect(normalizeMonitoringProvider('x-ai')).toBe('x-ai');
    expect(normalizeMonitoringProvider('grok')).toBe('grok');
    expect(normalizeMonitoringProvider('x-ai')).not.toBe(normalizeMonitoringProvider('grok'));
    expect(buildMonitoringAccountRowId({ provider: 'x-ai', authLabel: 'a:b' })).not.toBe(
      buildMonitoringAccountRowId({ provider: 'grok', authLabel: 'a:b' })
    );
    expect(buildMonitoringAccountRowId({ provider: 'a:b', authLabel: 'c' })).not.toBe(
      buildMonitoringAccountRowId({ provider: 'a', authLabel: 'b:c' })
    );
  });
});
