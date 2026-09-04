import { beforeEach, describe, expect, it } from 'vitest';
import type { AuthFileItem } from '@/types';
import {
  acknowledgePendingAccountDirectReauths,
  clearPendingAccountDirectReauthsForTests,
  confirmAccountDirectReauth,
  createAccountDirectReauthBaseline,
  listPendingAccountDirectReauths,
  reconcileAccountDirectReauth,
  recordPendingAccountDirectReauth,
} from './accountDirectReauth';

const createStorage = () => {
  const values = new Map<string, string>();
  return {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => values.set(key, value),
  };
};

const makeFile = (overrides: Partial<AuthFileItem> = {}): AuthFileItem =>
  ({
    id: 'runtime-1',
    name: 'codex-old.json',
    provider: 'codex',
    type: 'codex',
    authIndex: 'auth-1',
    account: 'alice@example.com',
    account_id: 'account-1',
    last_refresh: 1_000,
    modified: 1_100,
    status_message: 'token_expired',
    ...overrides,
  }) as AuthFileItem;

const makeBaseline = (startedAtMs = Date.now()) =>
  createAccountDirectReauthBaseline({
    target: {
      account: 'alice@example.com',
      fileName: 'codex-old.json',
      runtimeId: 'runtime-1',
      provider: 'codex',
      authIndex: 'auth-1',
      accountId: 'account-1',
      accountSnapshot: 'alice@example.com',
    },
    file: makeFile(),
    resultKeys: ['result-1'],
    startedAtMs,
  })!;

describe('accountDirectReauth', () => {
  beforeEach(() => {
    clearPendingAccountDirectReauthsForTests();
  });

  it('confirms only the same target after newer credential evidence appears', () => {
    const baseline = makeBaseline();
    expect(
      confirmAccountDirectReauth(baseline, [
        makeFile({ last_refresh: 2_500, modified: 2_550, status_message: '' }),
      ])
    ).not.toBeNull();
    expect(
      confirmAccountDirectReauth(baseline, [
        makeFile({ account_id: 'account-2', account: 'bob@example.com', last_refresh: 2_500 }),
      ])
    ).toBeNull();
    expect(confirmAccountDirectReauth(baseline, [makeFile()])).toBeNull();
  });

  it('accepts an explicit healthy status transition when timestamps are unavailable', () => {
    const baseline = makeBaseline();
    expect(
      confirmAccountDirectReauth(baseline, [
        makeFile({ last_refresh: undefined, modified: undefined, status_message: 'ready' }),
      ])
    ).not.toBeNull();
  });

  it('confirms the same account when OAuth replaces the physical file name', () => {
    const baseline = makeBaseline();
    expect(
      confirmAccountDirectReauth(baseline, [
        makeFile({
          name: 'codex-alice-plus-account-1.json',
          id: 'runtime-2',
          authIndex: 'auth-2',
          last_refresh: Date.now(),
          modified: Date.now(),
          status_message: '',
        }),
      ])
    ).not.toBeNull();
  });

  it('never confirms a different account ID even when the email is unchanged', () => {
    const baseline = makeBaseline();
    const result = reconcileAccountDirectReauth(baseline, [
      makeFile({
        account_id: 'account-2',
        account: 'alice@example.com',
        last_refresh: 2_500,
        modified: 2_550,
        status_message: '',
      }),
    ]);

    expect(result).toMatchObject({
      status: 'identity-changed',
      observedAccountId: 'account-2',
    });
    expect(
      confirmAccountDirectReauth(baseline, [makeFile({ account_id: 'account-2' })])
    ).toBeNull();
  });

  it('fails closed when multiple credentials share the expected account ID', () => {
    const baseline = makeBaseline();
    const result = reconcileAccountDirectReauth(baseline, [
      makeFile({ name: 'codex-a.json', id: 'runtime-a', authIndex: 'auth-a' }),
      makeFile({ name: 'codex-b.json', id: 'runtime-b', authIndex: 'auth-b' }),
    ]);

    expect(result).toEqual({ status: 'ambiguous' });
  });

  it('confirms Alice without treating Bob in the same Workspace as ambiguous', () => {
    const alice = makeFile({
      name: 'codex-alice.json',
      id: 'runtime-alice',
      authIndex: 'auth-alice',
      account: 'alice@example.com',
      account_id: 'workspace-1',
      last_refresh: 1_000,
      modified: 1_100,
    });
    const bob = makeFile({
      name: 'codex-bob.json',
      id: 'runtime-bob',
      authIndex: 'auth-bob',
      account: 'bob@example.com',
      account_id: 'workspace-1',
      last_refresh: 1_000,
      modified: 1_100,
    });
    const baseline = createAccountDirectReauthBaseline({
      target: {
        account: 'alice@example.com',
        fileName: alice.name,
        runtimeId: alice.id,
        provider: 'codex',
        authIndex: alice.authIndex,
        accountId: 'workspace-1',
        accountSnapshot: 'Alice@Example.com',
      },
      file: alice,
      files: [alice, bob],
      resultKeys: [],
    })!;

    expect(
      reconcileAccountDirectReauth(baseline, [
        { ...alice, last_refresh: 2_000, modified: 2_100, status_message: '' },
        bob,
      ])
    ).toMatchObject({ status: 'confirmed' });
  });

  it("reports a newly observed Bob credential as identity-changed in Alice's Workspace", () => {
    const baseline = makeBaseline();
    expect(
      reconcileAccountDirectReauth(baseline, [
        makeFile({
          name: 'codex-bob.json',
          id: 'runtime-bob',
          authIndex: 'auth-bob',
          account: 'bob@example.com',
          last_refresh: 2_500,
          modified: 2_550,
          status_message: '',
        }),
      ])
    ).toMatchObject({ status: 'identity-changed', observedAccountId: 'account-1' });
  });

  it('reports a newly observed Alice credential in another Workspace as identity-changed', () => {
    const baseline = makeBaseline();
    expect(
      reconcileAccountDirectReauth(baseline, [
        makeFile({
          name: 'codex-other-workspace.json',
          id: 'runtime-other-workspace',
          authIndex: 'auth-other-workspace',
          account_id: 'workspace-2',
          account: 'alice@example.com',
          last_refresh: 2_500,
          modified: 2_550,
          status_message: '',
        }),
      ])
    ).toMatchObject({ status: 'identity-changed', observedAccountId: 'workspace-2' });
  });

  it('does not reconcile a Workspace-only or weak-member candidate', () => {
    const baseline = makeBaseline();
    expect(
      reconcileAccountDirectReauth(baseline, [
        makeFile({ account: 'Alice', last_refresh: 2_500, modified: 2_550, status_message: '' }),
      ])
    ).toEqual({ status: 'unconfirmed' });
    expect(
      reconcileAccountDirectReauth(
        {
          ...baseline,
          target: { ...baseline.target, accountSnapshot: null },
        },
        [makeFile({ last_refresh: 2_500, modified: 2_550, status_message: '' })]
      )
    ).toEqual({ status: 'unconfirmed' });
  });

  it('does not report recovery when the original credential is unchanged and a new Space appears', () => {
    const original = makeFile();
    const baseline = createAccountDirectReauthBaseline({
      target: {
        account: 'alice@example.com',
        fileName: original.name,
        runtimeId: 'runtime-1',
        provider: 'codex',
        authIndex: 'auth-1',
        accountId: 'account-1',
        accountSnapshot: 'alice@example.com',
      },
      file: original,
      files: [original],
      resultKeys: [],
    })!;
    const newSpace = makeFile({
      id: 'runtime-2',
      name: 'codex-new.json',
      authIndex: 'auth-2',
      account_id: 'account-2',
      account: 'alice@example.com',
      last_refresh: 2_500,
      modified: 2_550,
      status_message: '',
    });

    expect(reconcileAccountDirectReauth(baseline, [original, newSpace])).toMatchObject({
      status: 'identity-changed',
      observedAccountId: 'account-2',
    });
  });

  it('ignores timestamp-only refreshes on unrelated existing Codex credentials', () => {
    const original = makeFile();
    const unrelated = makeFile({
      id: 'runtime-b',
      name: 'codex-b.json',
      authIndex: 'auth-b',
      account_id: 'account-b',
      account: 'bob@example.com',
      last_refresh: 900,
      modified: 950,
      status_message: '',
    });
    const baseline = createAccountDirectReauthBaseline({
      target: {
        account: 'alice@example.com',
        fileName: original.name,
        runtimeId: 'runtime-1',
        provider: 'codex',
        authIndex: 'auth-1',
        accountId: 'account-1',
        accountSnapshot: 'alice@example.com',
      },
      file: original,
      files: [original, unrelated],
      resultKeys: [],
    })!;

    expect(
      reconcileAccountDirectReauth(baseline, [
        original,
        { ...unrelated, last_refresh: 5_000, modified: 5_100, status_message: 'ready' },
      ])
    ).toEqual({ status: 'unconfirmed' });
  });

  it('does not auto-confirm by email when the baseline lacks a trusted account id', () => {
    const original = makeFile({ account_id: undefined });
    const baseline = createAccountDirectReauthBaseline({
      target: {
        account: 'alice@example.com',
        fileName: original.name,
        runtimeId: 'runtime-1',
        provider: 'codex',
        authIndex: 'auth-1',
        accountSnapshot: 'alice@example.com',
      },
      file: original,
      files: [original],
      resultKeys: [],
    })!;

    expect(
      reconcileAccountDirectReauth(baseline, [
        makeFile({
          account_id: 'space-b',
          account: 'alice@example.com',
          last_refresh: 5_000,
          modified: 5_100,
          status_message: '',
        }),
      ])
    ).toEqual({ status: 'unconfirmed' });
  });

  it('persists pending retries by connection and acknowledges exact records', () => {
    const storage = createStorage();
    const startedAtMs = Date.now();
    const baseline = makeBaseline(startedAtMs);
    const first = recordPendingAccountDirectReauth({
      connectionFingerprint: 'connection-a',
      baseline,
      storage,
    });
    expect(first).not.toBeNull();
    const stored = JSON.parse(storage.getItem('cpa.accounts.direct-reauth.v3') ?? '{}');
    expect(stored.version).toBe(3);
    expect(stored.items[0].providerCredentials[0].accountSnapshot).toBe('alice@example.com');

    const persisted = recordPendingAccountDirectReauth({
      connectionFingerprint: 'connection-a',
      baseline: { ...baseline, startedAtMs: startedAtMs + 1 },
      storage,
    });
    expect(persisted).not.toBeNull();

    expect(listPendingAccountDirectReauths('connection-a', storage, startedAtMs + 2)).toEqual([
      persisted,
    ]);
    acknowledgePendingAccountDirectReauths([persisted!.id], storage);
    expect(listPendingAccountDirectReauths('connection-a', storage, startedAtMs + 2)).toEqual([]);
    expect(listPendingAccountDirectReauths('connection-b', storage, startedAtMs + 2)).toEqual([]);
  });
});
