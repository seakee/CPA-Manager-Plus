import { describe, expect, it } from 'vitest';
import type { AuthFileItem } from '@/types';
import {
  normalizeCodexMemberSnapshot,
  readAuthFileStatusCodexMember,
  readAuthFileStatusCodexMemberInvalid,
  resolveCredentialIdentity,
} from './authFileCredentialIdentity';

describe('credentialIdentity', () => {
  it('normalizes the complete credential identity from one auth-file row', () => {
    expect(
      resolveCredentialIdentity({
        id: ' runtime-1 ',
        name: ' shared.json ',
        provider: 'x_ai',
        authIndex: 42,
        project_id: ' project-1 ',
        email: ' user@example.com ',
        label: ' Primary ',
      })
    ).toEqual({
      physicalName: 'shared.json',
      runtimeId: 'runtime-1',
      authIndex: '42',
      provider: 'xai',
      accountId: 'project-1',
      accountSnapshot: 'user@example.com',
      authLabelSnapshot: 'Primary',
    });
  });

  it('honors explicit identity snapshots when the display account is weak', () => {
    expect(
      resolveCredentialIdentity({
        name: 'shared.json',
        type: 'codex',
        account: 'Stale display account',
        accountSnapshot: 'current@example.com',
        authLabelSnapshot: 'Current label',
        accountId: 'account-current',
        authIndex: 'auth-current',
      })
    ).toMatchObject({
      physicalName: 'shared.json',
      provider: 'codex',
      accountId: 'account-current',
      accountSnapshot: 'current@example.com',
      authLabelSnapshot: 'Current label',
      authIndex: 'auth-current',
    });
  });

  it('prefers an explicit Codex member snapshot over a weak display account', () => {
    expect(
      readAuthFileStatusCodexMember({
        name: 'shared.json',
        provider: 'codex',
        accountSnapshot: ' Alice@Example.com ',
        account: 'Stale display account',
      } as AuthFileItem)
    ).toBe('alice@example.com');
  });

  it('fails closed when a strong explicit snapshot conflicts with account or email', () => {
    for (const field of ['account', 'email'] as const) {
      const file = {
        name: 'shared.json',
        provider: 'codex',
        accountSnapshot: 'alice@example.com',
        [field]: 'bob@example.com',
      } as AuthFileItem;
      expect(readAuthFileStatusCodexMember(file)).toBe('');
      expect(readAuthFileStatusCodexMemberInvalid(file)).toBe(true);
    }
  });

  it('fails closed when explicit Codex member snapshots conflict', () => {
    const file = {
      name: 'shared.json',
      provider: 'codex',
      accountSnapshot: 'alice@example.com',
      account_snapshot: 'bob@example.com',
    } as AuthFileItem;
    expect(readAuthFileStatusCodexMember(file)).toBe('');
    expect(readAuthFileStatusCodexMemberInvalid(file)).toBe(true);
  });

  it('does not treat generic Codex project or sub fields as a ChatGPT account id', () => {
    expect(
      resolveCredentialIdentity({
        name: 'codex.json',
        provider: 'codex',
        authIndex: 'auth-1',
        project_id: 'project-x',
        sub: 'user-x',
      })
    ).toMatchObject({ provider: 'codex', accountId: '' });
  });

  it('reads an explicit Codex ChatGPT account id from token metadata', () => {
    expect(
      resolveCredentialIdentity({
        name: 'codex.json',
        provider: 'codex',
        authIndex: 'auth-1',
        metadata: { id_token: { chatgpt_account_id: ' account-a ' } },
      })
    ).toMatchObject({ provider: 'codex', accountId: 'account-a' });
  });

  it('normalizes only strong Codex email member evidence', () => {
    expect(normalizeCodexMemberSnapshot(' Alice@Example.com ')).toBe('alice@example.com');
    expect(normalizeCodexMemberSnapshot('Alice')).toBe('');
    expect(normalizeCodexMemberSnapshot('alice@example.com@other')).toBe('');
    expect(normalizeCodexMemberSnapshot('alice @example.com')).toBe('');
    expect(normalizeCodexMemberSnapshot('álîce@example.com')).toBe('');
    expect(normalizeCodexMemberSnapshot('alice\u000e@example.com')).toBe('');
    expect(normalizeCodexMemberSnapshot('alice\u007f@example.com')).toBe('');
  });

  it('finds a valid Codex email without promoting a weak display value', () => {
    expect(
      readAuthFileStatusCodexMember({
        name: 'codex.json',
        provider: 'codex',
        account: 'Alice',
        email: ' Alice@Example.com ',
        label: 'Primary',
      })
    ).toBe('alice@example.com');
    expect(
      readAuthFileStatusCodexMember({
        name: 'codex.json',
        provider: 'codex',
        account: 'Alice',
        label: 'Primary',
      })
    ).toBe('');
  });

  it('falls through a weak explicit snapshot to a strong account/email field', () => {
    expect(
      readAuthFileStatusCodexMember({
        name: 'codex.json',
        provider: 'codex',
        accountSnapshot: 'Pro 20x Workspace',
        account: 'Alice',
        email: ' Alice@Example.com ',
      } as AuthFileItem)
    ).toBe('alice@example.com');
  });

  it('ignores a weak explicit Codex snapshot instead of blocking credential identity', () => {
    expect(
      resolveCredentialIdentity({
        name: 'codex.json',
        provider: 'codex',
        accountSnapshot: 'Pro 20x Workspace',
        account_id: 'workspace-1',
        auth_index: 'auth-1',
      })
    ).toMatchObject({
      provider: 'codex',
      accountId: 'workspace-1',
      accountSnapshot: '',
      authIndex: 'auth-1',
    });
    expect(
      readAuthFileStatusCodexMemberInvalid({
        name: 'codex.json',
        provider: 'codex',
        account_snapshot: 'Pro 20x Workspace',
      } as AuthFileItem)
    ).toBe(false);
  });

  it('uses the strong Codex member when the account field is only a display label', () => {
    expect(
      resolveCredentialIdentity({
        name: 'codex.json',
        provider: 'codex',
        account: 'Alice',
        email: ' Alice@Example.com ',
        account_id: 'workspace-1',
        auth_index: 'auth-1',
      })
    ).toMatchObject({
      provider: 'codex',
      accountId: 'workspace-1',
      accountSnapshot: 'alice@example.com',
    });
  });
});
