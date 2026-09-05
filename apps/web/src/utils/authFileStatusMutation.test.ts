import { describe, expect, it } from 'vitest';
import type { AuthFileItem } from '@/types';
import {
  getAuthFileStatusIdentityKey,
  getAuthFileStatusMutationLockKeys,
  getAuthFileStatusSelectionKey,
  readAuthFileStatusAccountId,
  readAuthFileStatusAccountIdInvalid,
  readAuthFileStatusAccountSnapshot,
  readAuthFileStatusProvider,
  resolveAuthFileStatusMutationTarget,
} from './authFileStatusMutation';

const sourceFiles = [
  {
    id: 'shared.json',
    name: 'shared.json',
    auth_index: 'auth-1',
    type: 'codex',
    account_id: 'account-1',
  },
  {
    id: 'runtime-auth-2',
    name: 'shared.json',
    auth_index: 'auth-2',
    type: 'codex',
    account_id: 'account-2',
  },
] as AuthFileItem[];

describe('auth file status identity keys', () => {
  it('preserves the legacy indexed key format', () => {
    const file = {
      id: 'runtime-auth-1',
      name: 'shared.json',
      auth_index: 'auth-1',
      type: 'codex',
      account_id: 'account-1',
    } as AuthFileItem;

    expect(getAuthFileStatusIdentityKey(file)).toBe('shared.json::auth-1');
    expect(getAuthFileStatusSelectionKey(file)).toBe('shared.json\u0000auth-1');
  });

  it('separates same-file rows without auth indexes by account identity', () => {
    const first = {
      id: 'runtime-auth-1',
      name: 'shared.json',
      type: 'codex',
      account_id: 'account-1',
      account: 'first@example.com',
    } as AuthFileItem;
    const renamed = { ...first, account: 'renamed@example.com' } as AuthFileItem;
    const second = {
      id: 'runtime-auth-2',
      name: 'shared.json',
      type: 'codex',
      account: 'second@example.com',
    } as AuthFileItem;

    expect(getAuthFileStatusIdentityKey(first)).not.toBe(getAuthFileStatusIdentityKey(renamed));
    expect(getAuthFileStatusIdentityKey(first)).not.toBe(getAuthFileStatusIdentityKey(second));
    expect(getAuthFileStatusSelectionKey(first)).not.toBe(getAuthFileStatusSelectionKey(second));
  });

  it('keeps the existing account-ID precedence for non-Codex providers', () => {
    const original = {
      id: 'runtime-auth-1',
      name: 'shared.json',
      type: 'xai',
      account_id: 'account-1',
      account: 'original@example.com',
    } as AuthFileItem;
    const refreshed = { ...original, account: 'display-changed@example.com' } as AuthFileItem;

    expect(getAuthFileStatusIdentityKey(original)).toBe(getAuthFileStatusIdentityKey(refreshed));
  });

  it('uses a normalized provider plus runtime ID when account metadata is absent', () => {
    const first = {
      id: 'runtime-auth-1',
      name: 'shared.json',
      type: 'x_ai',
    } as AuthFileItem;
    const alias = { ...first, type: 'xai' } as AuthFileItem;
    const second = { ...first, id: 'runtime-auth-2' } as AuthFileItem;

    expect(getAuthFileStatusIdentityKey(first)).toBe(getAuthFileStatusIdentityKey(alias));
    expect(getAuthFileStatusIdentityKey(first)).not.toBe(getAuthFileStatusIdentityKey(second));
  });

  it('does not treat a mutable label as an account snapshot', () => {
    expect(
      readAuthFileStatusAccountSnapshot({
        id: 'runtime-label-only',
        name: 'shared.json',
        type: 'codex',
        label: 'Friendly account',
      } as AuthFileItem)
    ).toBe('');
  });

  it.each([
    ['project_id', 'project-snake'],
    ['projectId', 'project-camel'],
    ['gemini_virtual_project', 'virtual-project-snake'],
    ['geminiVirtualProject', 'virtual-project-camel'],
    ['sub', 'subject-1'],
  ])('reads the generic account identity field %s', (field, expected) => {
    expect(
      readAuthFileStatusAccountId({
        name: 'vertex.json',
        [field]: ` ${expected} `,
      } as AuthFileItem)
    ).toBe(expected);
  });

  it('reads generic account identities from nested metadata and token objects', () => {
    expect(
      readAuthFileStatusAccountId({
        name: 'vertex.json',
        metadata: { idToken: { projectId: 'nested-project' } },
      } as AuthFileItem)
    ).toBe('nested-project');
  });

  it('reads display account aliases and ignores an empty provider before a valid type', () => {
    const file = {
      name: 'vertex.json',
      provider: '   ',
      type: 'vertex',
      display_account: 'project@example.com',
    } as AuthFileItem;

    expect(readAuthFileStatusProvider(file)).toBe('vertex');
    expect(readAuthFileStatusAccountSnapshot(file)).toBe('project@example.com');
  });

  it('marks conflicting Codex Workspace evidence invalid', () => {
    const file = {
      name: 'codex.json',
      type: 'codex',
      account_id: 'workspace-a',
      metadata: { id_token: { account_id: 'workspace-b' } },
    } as AuthFileItem;

    expect(readAuthFileStatusAccountId(file)).toBe('');
    expect(readAuthFileStatusAccountIdInvalid(file)).toBe(true);
  });
});

describe('resolveAuthFileStatusMutationTarget', () => {
  it('fails closed when a previously known runtime ID disappears', () => {
    const resolution = resolveAuthFileStatusMutationTarget(
      [
        {
          id: 'replacement-runtime',
          name: 'shared.json',
          auth_index: 'auth-1',
        },
      ] as AuthFileItem[],
      {
        name: 'shared.json',
        runtimeId: 'original-runtime',
        authIndex: 'auth-1',
        provider: 'codex',
        accountId: 'account-1',
      }
    );

    expect(resolution).toMatchObject({
      target: null,
      scope: 'ambiguous',
      failure: 'runtime-id-changed',
    });
  });

  it('rejects selection-key drift even when the runtime ID is stable', () => {
    const current = {
      id: 'runtime-auth-1',
      name: 'renamed.json',
      auth_index: 'auth-2',
      type: 'codex',
      account_id: 'account-1',
    } as AuthFileItem;
    const resolution = resolveAuthFileStatusMutationTarget([current], {
      name: 'original.json',
      runtimeId: 'runtime-auth-1',
      authIndex: 'auth-1',
      provider: 'codex',
      accountId: 'account-1',
    });

    expect(resolution).toMatchObject({
      target: current,
      scope: 'ambiguous',
      failure: 'identity-changed',
    });
  });

  it('rejects a replacement account when every locator remains unchanged', () => {
    const current = {
      id: 'runtime-auth-1',
      name: 'same.json',
      auth_index: 'auth-1',
      type: 'xai',
      account: 'replacement@example.com',
    } as AuthFileItem;

    const resolution = resolveAuthFileStatusMutationTarget([current], {
      name: 'same.json',
      runtimeId: 'runtime-auth-1',
      authIndex: 'auth-1',
      provider: 'xai',
      accountSnapshot: 'original@example.com',
    });

    expect(resolution).toMatchObject({
      target: current,
      scope: 'ambiguous',
      failure: 'identity-changed',
    });
  });

  it('rejects a Codex member change even when the Workspace ID is unchanged', () => {
    const current = {
      id: 'runtime-auth-1',
      name: 'same.json',
      auth_index: 'auth-1',
      type: 'codex',
      account_id: 'account-1',
      account: 'renamed@example.com',
    } as AuthFileItem;

    expect(
      resolveAuthFileStatusMutationTarget([current], {
        name: 'same.json',
        runtimeId: 'runtime-auth-1',
        authIndex: 'auth-1',
        provider: 'codex',
        accountId: 'account-1',
        accountSnapshot: 'original@example.com',
      })
    ).toMatchObject({ target: current, scope: 'ambiguous', failure: 'identity-changed' });
  });

  it('allows missing Codex Workspace and member evidence for a uniquely located credential', () => {
    const current = {
      id: 'runtime-auth-1',
      name: 'same.json',
      auth_index: 'auth-1',
      type: 'codex',
      disabled: true,
    } as AuthFileItem;

    expect(
      resolveAuthFileStatusMutationTarget([current], {
        name: 'same.json',
        runtimeId: 'runtime-auth-1',
        authIndex: 'auth-1',
        provider: 'codex',
        accountId: 'workspace-1',
        accountSnapshot: 'alice@example.com',
      })
    ).toMatchObject({ target: current, scope: 'credential', failure: null });
  });

  it('checks a strong account/email member on an AuthFileItem target', () => {
    const requested = {
      id: 'runtime-auth-1',
      name: 'same.json',
      auth_index: 'auth-1',
      type: 'codex',
      account_id: 'workspace-1',
      account: 'alice@example.com',
    } as AuthFileItem;
    const current = { ...requested, account: 'bob@example.com' } as AuthFileItem;

    expect(resolveAuthFileStatusMutationTarget([current], requested)).toMatchObject({
      target: current,
      scope: 'ambiguous',
      failure: 'identity-changed',
    });
  });

  it('keeps a duplicate file/auth-index selector ambiguous despite Codex member evidence', () => {
    const alice = {
      id: 'runtime-alice',
      name: 'team.json',
      auth_index: 'auth-1',
      type: 'codex',
      account_id: 'workspace-1',
      account: 'alice@example.com',
    } as AuthFileItem;
    const bob = {
      id: 'runtime-bob',
      name: 'team.json',
      auth_index: 'auth-1',
      type: 'codex',
      account_id: 'workspace-1',
      account: 'bob@example.com',
    } as AuthFileItem;

    expect(
      resolveAuthFileStatusMutationTarget([alice, bob], {
        name: 'team.json',
        authIndex: 'auth-1',
        provider: 'codex',
        accountId: 'workspace-1',
        accountSnapshot: 'ALICE@EXAMPLE.COM',
      })
    ).toMatchObject({ target: null, scope: 'ambiguous', failure: 'ambiguous' });
  });

  it('keeps a shared selector ambiguous when a sibling lacks member evidence', () => {
    const alice = {
      id: 'runtime-alice',
      name: 'team.json',
      auth_index: 'auth-1',
      type: 'codex',
      account_id: 'workspace-1',
      account: 'alice@example.com',
    } as AuthFileItem;
    const unknown = {
      id: 'runtime-unknown',
      name: 'team.json',
      auth_index: 'auth-1',
      type: 'codex',
      account_id: 'workspace-1',
    } as AuthFileItem;

    expect(
      resolveAuthFileStatusMutationTarget([alice, unknown], {
        name: 'team.json',
        authIndex: 'auth-1',
        provider: 'codex',
        accountId: 'workspace-1',
        accountSnapshot: 'alice@example.com',
      })
    ).toMatchObject({ target: null, scope: 'ambiguous', failure: 'ambiguous' });
  });

  it('rejects a Codex mutation when Workspace evidence is invalid even if the auth index matches', () => {
    const current = {
      id: 'runtime-auth-1',
      name: 'same.json',
      auth_index: 'auth-1',
      type: 'codex',
      account_id: 'workspace-a',
      metadata: { id_token: { account_id: 'workspace-b' } },
      account: 'alice@example.com',
    } as AuthFileItem;

    const resolution = resolveAuthFileStatusMutationTarget([current], {
      name: 'same.json',
      runtimeId: 'runtime-auth-1',
      authIndex: 'auth-1',
      provider: 'codex',
      accountId: 'workspace-a',
      accountSnapshot: 'alice@example.com',
    });

    expect(resolution).toMatchObject({
      target: current,
      scope: 'ambiguous',
      failure: 'identity-changed',
    });
  });

  it('fails closed when a Codex member evidence snapshot is conflicting even without a target member', () => {
    const current = {
      id: 'runtime-auth-1',
      name: 'same.json',
      auth_index: 'auth-1',
      type: 'codex',
      account_id: 'workspace-a',
      accountSnapshot: 'alice@example.com',
      account_snapshot: 'bob@example.com',
    } as AuthFileItem;

    expect(
      resolveAuthFileStatusMutationTarget([current], {
        name: 'same.json',
        runtimeId: 'runtime-auth-1',
        authIndex: 'auth-1',
        provider: 'codex',
        accountId: 'workspace-a',
      })
    ).toMatchObject({ target: current, scope: 'ambiguous', failure: 'identity-changed' });
  });

  it('uses the credential locator when a Codex target carries a weak member snapshot', () => {
    const current = {
      id: 'runtime-auth-1',
      name: 'same.json',
      auth_index: 'auth-1',
      type: 'codex',
      account_id: 'account-1',
      account: 'Alice',
    } as AuthFileItem;

    expect(
      resolveAuthFileStatusMutationTarget([current], {
        name: 'same.json',
        runtimeId: 'runtime-auth-1',
        authIndex: 'auth-1',
        provider: 'codex',
        accountId: 'account-1',
        accountSnapshot: 'Alice',
      })
    ).toMatchObject({ target: current, scope: 'credential', failure: null });
  });

  it('does not use Workspace/member alone for a Codex mutation', () => {
    const current = {
      id: 'runtime-auth-1',
      name: 'same.json',
      type: 'codex',
      account_id: 'workspace-1',
      account: 'alice@example.com',
    } as AuthFileItem;

    expect(
      resolveAuthFileStatusMutationTarget([current], {
        name: 'same.json',
        provider: 'codex',
        accountId: 'workspace-1',
        accountSnapshot: 'alice@example.com',
      })
    ).toMatchObject({ target: current, scope: 'ambiguous', failure: 'identity-changed' });
  });

  it('fails closed for a weak Codex member snapshot without a credential locator', () => {
    const current = {
      id: 'runtime-auth-1',
      name: 'same.json',
      auth_index: 'auth-1',
      type: 'codex',
      account_id: 'account-1',
      account: 'Alice',
    } as AuthFileItem;

    expect(
      resolveAuthFileStatusMutationTarget([current], {
        name: 'same.json',
        provider: 'codex',
        accountId: 'account-1',
        accountSnapshot: 'Alice',
      })
    ).toMatchObject({ target: current, scope: 'ambiguous', failure: 'identity-changed' });
  });

  it('rejects a status mutation when either side is missing a real provider', () => {
    const withoutProvider = {
      id: 'runtime-auth-1',
      name: 'same.json',
      auth_index: 'auth-1',
      account_id: 'account-1',
    } as AuthFileItem;
    const codexFile = { ...withoutProvider, type: 'codex' } as AuthFileItem;

    expect(
      resolveAuthFileStatusMutationTarget([withoutProvider], {
        name: 'same.json',
        runtimeId: 'runtime-auth-1',
        authIndex: 'auth-1',
        provider: 'codex',
        accountId: 'account-1',
      })
    ).toMatchObject({ target: withoutProvider, scope: 'ambiguous', failure: 'identity-changed' });
    expect(
      resolveAuthFileStatusMutationTarget([codexFile], {
        name: 'same.json',
        runtimeId: 'runtime-auth-1',
        authIndex: 'auth-1',
        accountId: 'account-1',
      })
    ).toMatchObject({ target: codexFile, scope: 'ambiguous', failure: 'identity-changed' });
  });

  it('accepts an accountless CPA credential when its runtime ID and auth index remain stable', () => {
    const current = {
      id: 'runtime-api-key-1',
      name: 'api-key.json',
      auth_index: 'auth-api-key-1',
      type: 'gemini',
    } as AuthFileItem;

    expect(
      resolveAuthFileStatusMutationTarget([current], {
        name: 'api-key.json',
        runtimeId: 'runtime-api-key-1',
        authIndex: 'auth-api-key-1',
        provider: 'gemini',
      })
    ).toMatchObject({ target: current, scope: 'credential', failure: null });
  });

  it('classifies the source row as a file-level mutation', () => {
    const resolution = resolveAuthFileStatusMutationTarget(sourceFiles, {
      name: 'shared.json',
      runtimeId: 'shared.json',
      authIndex: 'auth-1',
      provider: 'codex',
      accountId: 'account-1',
    });

    expect(resolution.scope).toBe('source-file');
    expect(resolution.failure).toBeNull();
    expect(resolution.affectedFiles).toEqual(sourceFiles);
  });

  it('classifies an expanded child as unsafe for an independent mutation', () => {
    const resolution = resolveAuthFileStatusMutationTarget(sourceFiles, {
      name: 'shared.json',
      runtimeId: 'runtime-auth-2',
      authIndex: 'auth-2',
      provider: 'codex',
      accountId: 'account-2',
    });

    expect(resolution.scope).toBe('expanded-child');
    expect(resolution.failure).toBeNull();
    expect(resolution.affectedFiles).toEqual(sourceFiles);
  });

  it('rejects duplicate runtime IDs', () => {
    const resolution = resolveAuthFileStatusMutationTarget(
      [
        {
          id: 'duplicate',
          name: 'first.json',
          auth_index: 'auth-1',
          type: 'codex',
          account: 'first@example.com',
        },
        {
          id: 'duplicate',
          name: 'second.json',
          auth_index: 'auth-2',
          type: 'codex',
          account: 'second@example.com',
        },
      ] as AuthFileItem[],
      {
        name: 'first.json',
        runtimeId: 'duplicate',
        authIndex: 'auth-1',
        provider: 'codex',
        accountSnapshot: 'first@example.com',
      }
    );

    expect(resolution).toMatchObject({ target: null, scope: 'ambiguous', failure: 'ambiguous' });
  });

  it('rejects a physical filename that collides with another credential runtime ID', () => {
    const current = {
      id: 'runtime-target',
      name: 'shared.json',
      auth_index: 'auth-target',
      type: 'codex',
      account_id: 'account-target',
    } as AuthFileItem;
    const colliding = {
      id: 'shared.json',
      name: 'other.json',
      auth_index: 'auth-other',
      type: 'codex',
      account_id: 'account-other',
    } as AuthFileItem;

    expect(
      resolveAuthFileStatusMutationTarget([current, colliding], {
        name: 'shared.json',
        runtimeId: 'runtime-target',
        authIndex: 'auth-target',
        provider: 'codex',
        accountId: 'account-target',
      })
    ).toMatchObject({ target: current, scope: 'ambiguous', failure: 'ambiguous' });
  });
});

describe('getAuthFileStatusMutationLockKeys', () => {
  it('covers the runtime ID plus both the old and refreshed selection keys', () => {
    const keys = getAuthFileStatusMutationLockKeys(
      [
        {
          id: 'runtime-auth-1',
          name: 'renamed.json',
          auth_index: 'auth-2',
          type: 'codex',
          account_id: 'account-1',
        },
      ] as AuthFileItem[],
      {
        name: 'original.json',
        runtimeId: 'runtime-auth-1',
        authIndex: 'auth-1',
        provider: 'codex',
        accountId: 'account-1',
      }
    );

    expect(keys).toEqual(
      new Set([
        'runtime:runtime-auth-1',
        'selection:original.json\u0000auth-1',
        'selection:renamed.json\u0000auth-2',
      ])
    );
  });

  it('locks the source file and every expanded sibling identity', () => {
    const keys = getAuthFileStatusMutationLockKeys(sourceFiles, {
      name: 'shared.json',
      runtimeId: 'shared.json',
      authIndex: 'auth-1',
      provider: 'codex',
      accountId: 'account-1',
    });

    expect(keys).toEqual(
      new Set([
        'file:shared.json',
        'runtime:shared.json',
        'runtime:runtime-auth-2',
        'selection:shared.json\u0000auth-1',
        'selection:shared.json\u0000auth-2',
      ])
    );
  });
});
