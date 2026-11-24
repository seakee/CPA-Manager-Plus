import { describe, expect, it, vi } from 'vitest';
import type { AuthFileItem } from '@/types';
import {
  createSiwcStart,
  getSiwcBindingStatus,
  hasSiwcCredentialTokens,
  isSiwcCallbackValid,
  listSiwcTargets,
  readSiwcExpiresAt,
  readSiwcExpiresAtForDisplay,
  supportsSiwcBinding,
} from './siwcBinding';

describe('siwcBinding', () => {
  it('detects supported targets across snake_case and camelCase payloads and sorts by name', () => {
    const files: AuthFileItem[] = [
      { name: 'codex-z.json', provider: 'codex', siwc_bind_supported: true },
      { name: 'codex-virtual.json', provider: 'codex', siwc_bind_supported: false },
      { name: 'claude-a.json', provider: 'claude' },
      { name: 'codex-a.json', provider: 'codex', siwcBindSupported: true },
      { name: '', provider: 'codex', siwc_bind_supported: true },
    ];

    expect(supportsSiwcBinding(files[0])).toBe(true);
    expect(supportsSiwcBinding(files[1])).toBe(false);
    expect(supportsSiwcBinding(files[3])).toBe(true);
    expect(listSiwcTargets(files).map((f) => f.name)).toEqual(['codex-a.json', 'codex-z.json']);
  });

  it('resolves active, invalid, bound, and unbound statuses and hides expiry when unbound', () => {
    const unboundWithStaleExpiry: AuthFileItem = {
      name: 'u.json',
      siwc_bound: false,
      use_siwc: false,
      siwc_expires_at: '2026-01-01T00:00:00Z',
    };
    const invalidEnabled: AuthFileItem = {
      name: 'inv.json',
      siwc_bound: true,
      use_siwc: true,
      siwc_status: 'invalid',
      siwc_expires_at: '2025-01-01T00:00:00Z',
    };
    const invalidUnboundEnabled: AuthFileItem = {
      name: 'inv2.json',
      siwc_bound: false,
      use_siwc: true,
      siwc_status: 'invalid',
    };

    expect(getSiwcBindingStatus(unboundWithStaleExpiry)).toBe('unbound');
    expect(readSiwcExpiresAtForDisplay(unboundWithStaleExpiry)).toBeUndefined();
    expect(getSiwcBindingStatus(invalidEnabled)).toBe('invalid');
    expect(readSiwcExpiresAtForDisplay(invalidEnabled)).toBe('2025-01-01T00:00:00Z');
    expect(getSiwcBindingStatus(invalidUnboundEnabled)).toBe('invalid');
    expect(getSiwcBindingStatus({ name: 'a.json', siwc_bound: true, use_siwc: false })).toBe(
      'bound'
    );
    expect(getSiwcBindingStatus({ name: 'a.json', siwc_bound: true, use_siwc: true })).toBe(
      'active'
    );
    expect(getSiwcBindingStatus({ name: 'a.json', siwcBound: true, useSiwc: true })).toBe(
      'active'
    );
    expect(readSiwcExpiresAt({ name: 'a.json', siwc_expires_at: ' 2027-06-01T00:00:00Z ' })).toBe(
      '2027-06-01T00:00:00Z'
    );
    expect(readSiwcExpiresAt({ name: 'a.json', siwcExpiresAt: '2027-06-02T00:00:00Z' })).toBe(
      '2027-06-02T00:00:00Z'
    );
  });

  it('validates complete nested SIWC credential tokens', () => {
    expect(hasSiwcCredentialTokens(null)).toBe(false);
    expect(hasSiwcCredentialTokens({})).toBe(false);
    expect(hasSiwcCredentialTokens({ siwc: { access_token: 'a', refresh_token: 'r' } })).toBe(false);
    expect(
      hasSiwcCredentialTokens({
        siwc: { access_token: 'a', refresh_token: 'r', client_id: 'c' },
      })
    ).toBe(true);
    expect(
      hasSiwcCredentialTokens({
        siwc: { access_token: ' ', refresh_token: 'r', client_id: 'c' },
      })
    ).toBe(false);
  });

  it('enforces the fixed SIWC callback URL contract strictly', () => {
    const valid =
      'http://127.0.0.1:1455/auth/callback?state=st_123&code=abc_code&client_id=cid_1';
    expect(isSiwcCallbackValid(valid, 'st_123')).toBe(true);
    expect(
      isSiwcCallbackValid(
        'http://127.0.0.1:1455/auth/callback?state=st_123&error=access_denied',
        'st_123'
      )
    ).toBe(true);

    expect(
      isSiwcCallbackValid(
        'http://localhost:1455/auth/callback?state=st_123&code=a&client_id=b',
        'st_123'
      )
    ).toBe(false);
    expect(
      isSiwcCallbackValid(
        'https://127.0.0.1:1455/auth/callback?state=st_123&code=a&client_id=b',
        'st_123'
      )
    ).toBe(false);
    expect(
      isSiwcCallbackValid(
        'http://127.0.0.1:1456/auth/callback?state=st_123&code=a&client_id=b',
        'st_123'
      )
    ).toBe(false);
    expect(isSiwcCallbackValid(`${valid}#frag`, 'st_123')).toBe(false);
    expect(isSiwcCallbackValid(valid, 'st_other')).toBe(false);
    expect(
      isSiwcCallbackValid('http://127.0.0.1:1455/auth/callback?state=st_123&code=abc', 'st_123')
    ).toBe(false);
    expect(isSiwcCallbackValid(`${valid}&code=dup`, 'st_123')).toBe(false);
    expect(isSiwcCallbackValid(`${valid}&state=st_123`, 'st_123')).toBe(false);
  });

  it('cancels late start responses when closed before resolution', async () => {
    let resolveStart!: (value: { url: string; state: string }) => void;
    const start = vi.fn(
      () =>
        new Promise<{ url: string; state: string }>((resolve) => {
          resolveStart = resolve;
        })
    );
    const cancelSession = vi.fn().mockResolvedValue({ status: 'ok', cancelled: true });
    let closed = false;

    const handle = createSiwcStart('codex-alice.json', {
      start,
      cancelSession,
      isClosed: () => closed,
    });

    expect(handle.isCurrent()).toBe(true);
    closed = true;
    expect(handle.isCurrent()).toBe(false);

    resolveStart({ url: 'https://auth.openai.com/x', state: 'st_orphan' });
    await handle.promise;

    expect(cancelSession).toHaveBeenCalledTimes(1);
    expect(cancelSession).toHaveBeenCalledWith('st_orphan');
  });
});
