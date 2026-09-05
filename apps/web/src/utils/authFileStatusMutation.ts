import type { AuthFileItem } from '@/types';
import {
  readAuthFileStatusAccountId,
  readAuthFileStatusAccountIdInvalid,
  readAuthFileStatusAccountSnapshot,
  readAuthFileStatusAuthIndex,
  readAuthFileStatusCodexMember,
  readAuthFileStatusCodexMemberInvalid,
  readAuthFileStatusPhysicalName,
  readAuthFileStatusProvider,
  readAuthFileStatusRuntimeId,
  normalizeCodexMemberSnapshot,
  resolveCredentialIdentity,
} from '@/utils/authFileCredentialIdentity';

export {
  readAuthFileStatusAccountId,
  readAuthFileStatusAccountIdInvalid,
  readAuthFileStatusAccountSnapshot,
  readAuthFileStatusAuthIndex,
  readAuthFileStatusCodexMember,
  readAuthFileStatusCodexMemberInvalid,
  readAuthFileStatusPhysicalName,
  readAuthFileStatusProvider,
  readAuthFileStatusRuntimeId,
  normalizeCodexMemberSnapshot,
} from '@/utils/authFileCredentialIdentity';

const SELECTION_KEY_SEPARATOR = '\u0000';

export type AuthFileStatusMutationTarget = {
  name: string;
  runtimeId?: string | null;
  authIndex?: string | number | null;
  provider?: string | null;
  accountId?: string | null;
  accountSnapshot?: string | null;
};

export type AuthFileStatusIdentityTarget = AuthFileItem | AuthFileStatusMutationTarget;

export type AuthFileStatusMutationScope =
  | 'credential'
  | 'source-file'
  | 'expanded-child'
  | 'ambiguous';

export type AuthFileStatusMutationFailure =
  | 'not-found'
  | 'runtime-id-changed'
  | 'identity-changed'
  | 'ambiguous'
  | null;

export type AuthFileStatusMutationResolution = {
  target: AuthFileItem | null;
  scope: AuthFileStatusMutationScope;
  affectedFiles: AuthFileItem[];
  failure: AuthFileStatusMutationFailure;
};

const normalizeIdentityTarget = (target: AuthFileStatusIdentityTarget) => {
  const identity = resolveCredentialIdentity(target as AuthFileItem & AuthFileStatusMutationTarget);
  const requestedAccountSnapshot =
    typeof target.accountSnapshot === 'string' ? target.accountSnapshot.trim() : '';
  return {
    name: identity.physicalName,
    runtimeId: identity.runtimeId,
    authIndex: identity.authIndex,
    provider: identity.provider,
    accountId: identity.accountId,
    accountSnapshot: identity.accountSnapshot,
    accountSnapshotProvided:
      requestedAccountSnapshot.length > 0 || Boolean(identity.accountSnapshot),
  };
};

const normalizeTarget = (target: AuthFileStatusMutationTarget) => normalizeIdentityTarget(target);

const hasCredentialLocator = (target: ReturnType<typeof normalizeTarget>): boolean =>
  Boolean(target.runtimeId || target.authIndex);

export const getAuthFileStatusIdentityKey = (target: AuthFileStatusIdentityTarget): string => {
  const normalized = normalizeIdentityTarget(target);
  if (normalized.authIndex) return `${normalized.name}::${normalized.authIndex}`;

  const accountSnapshot =
    normalized.provider === 'codex'
      ? normalizeCodexMemberSnapshot(normalized.accountSnapshot)
      : normalized.accountSnapshot && normalized.accountSnapshot !== normalized.name
        ? normalized.accountSnapshot
        : '';
  const fallbackAccountSnapshot =
    normalized.accountId && normalized.provider !== 'codex' ? '' : accountSnapshot;
  const stableAccountId =
    normalized.provider === 'codex' && !accountSnapshot ? '' : normalized.accountId;
  if (stableAccountId || fallbackAccountSnapshot) {
    return `${normalized.name}::-::${JSON.stringify([
      normalized.name,
      normalized.provider,
      null,
      stableAccountId || null,
      fallbackAccountSnapshot || null,
    ])}`;
  }

  if (normalized.runtimeId && normalized.runtimeId !== normalized.name) {
    return `${normalized.name}::-::runtime:${JSON.stringify([
      normalized.provider,
      normalized.runtimeId,
    ])}`;
  }
  return `${normalized.name}::-`;
};

const authFileMatchesRequestedIdentity = (
  file: AuthFileItem,
  target: ReturnType<typeof normalizeTarget>
): boolean => {
  const provider = readAuthFileStatusProvider(file);
  const credentialLocatorPresent = hasCredentialLocator(target);
  if (!provider || !target.provider || provider !== target.provider) return false;
  if (provider === 'codex' && readAuthFileStatusAccountIdInvalid(file)) return false;
  if (provider === 'codex' && readAuthFileStatusCodexMemberInvalid(file)) return false;

  if (target.accountId) {
    if (target.provider === 'codex') {
      const currentWorkspace = readAuthFileStatusAccountId(file);
      if (currentWorkspace && currentWorkspace !== target.accountId) return false;
      const expectedMember = target.accountSnapshotProvided
        ? normalizeCodexMemberSnapshot(target.accountSnapshot)
        : '';
      // Workspace/member is verification evidence, not a credential locator.
      // Require runtime ID or auth index before any Codex mutation can use it.
      if (!credentialLocatorPresent) return false;
      const currentMember = readAuthFileStatusCodexMember(file);
      return !expectedMember || !currentMember || currentMember === expectedMember;
    }
    if (readAuthFileStatusAccountId(file) !== target.accountId) return false;
    return true;
  }

  if (target.accountSnapshot && target.accountSnapshot !== target.name) {
    if (target.provider === 'codex') {
      const expectedMember = normalizeCodexMemberSnapshot(target.accountSnapshot);
      // Member email without its Workspace is not a complete Codex identity;
      // use it only as an additional check on an exact credential locator.
      if (!credentialLocatorPresent || !expectedMember) return false;
      const currentMember = readAuthFileStatusCodexMember(file);
      return !currentMember || currentMember === expectedMember;
    }
    return readAuthFileStatusAccountSnapshot(file) === target.accountSnapshot;
  }

  // CPA auth entries are allowed to omit account metadata. A stable auth index,
  // together with the already-checked runtime ID, physical name, and provider,
  // still identifies the current credential without falling back to a filename-only match.
  return credentialLocatorPresent;
};

export const getAuthFileStatusSelectionKey = (target: AuthFileStatusIdentityTarget): string => {
  const normalized = normalizeIdentityTarget(target);
  const legacyKey = `${normalized.name}${SELECTION_KEY_SEPARATOR}${normalized.authIndex ?? '-'}`;
  if (normalized.authIndex) return legacyKey;
  return `${legacyKey}${SELECTION_KEY_SEPARATOR}${getAuthFileStatusIdentityKey(target)}`;
};

const runtimeLockKey = (runtimeId: string) => `runtime:${runtimeId}`;
const selectionLockKey = (selectionKey: string) => `selection:${selectionKey}`;
const physicalFileLockKey = (fileName: string) => `file:${fileName}`;

const findTargetWithoutRuntimeId = (
  files: AuthFileItem[],
  target: ReturnType<typeof normalizeTarget>
): AuthFileItem[] =>
  files.filter((file) => {
    if (readAuthFileStatusPhysicalName(file) !== target.name) return false;
    if (target.authIndex === null) return true;
    return readAuthFileStatusAuthIndex(file) === target.authIndex;
  });

export const resolveAuthFileStatusMutationTarget = (
  files: AuthFileItem[],
  requestedTarget: AuthFileStatusMutationTarget
): AuthFileStatusMutationResolution => {
  const target = normalizeTarget(requestedTarget);
  if (!target.name && !target.runtimeId) {
    return { target: null, scope: 'ambiguous', affectedFiles: [], failure: 'not-found' };
  }

  let matches: AuthFileItem[];
  if (target.runtimeId) {
    matches = files.filter((file) => readAuthFileStatusRuntimeId(file) === target.runtimeId);
    if (matches.length === 0) {
      return {
        target: null,
        scope: 'ambiguous',
        affectedFiles: [],
        failure: 'runtime-id-changed',
      };
    }
  } else {
    matches = findTargetWithoutRuntimeId(files, target);
    if (matches.length === 0) {
      return { target: null, scope: 'ambiguous', affectedFiles: [], failure: 'not-found' };
    }
  }

  if (matches.length !== 1) {
    return { target: null, scope: 'ambiguous', affectedFiles: matches, failure: 'ambiguous' };
  }

  const currentTarget = matches[0];
  const runtimeId = readAuthFileStatusRuntimeId(currentTarget);
  const physicalName = readAuthFileStatusPhysicalName(currentTarget);
  const siblings = files.filter((file) => readAuthFileStatusPhysicalName(file) === physicalName);
  if (
    (target.name && physicalName !== target.name) ||
    (target.authIndex !== null &&
      readAuthFileStatusAuthIndex(currentTarget) !== target.authIndex) ||
    !authFileMatchesRequestedIdentity(currentTarget, target)
  ) {
    return {
      target: currentTarget,
      scope: 'ambiguous',
      affectedFiles: siblings.length > 0 ? siblings : [currentTarget],
      failure: 'identity-changed',
    };
  }
  if (!runtimeId || !physicalName) {
    return {
      target: currentTarget,
      scope: 'ambiguous',
      affectedFiles: siblings.length > 0 ? siblings : [currentTarget],
      failure: 'ambiguous',
    };
  }

  if (files.filter((file) => readAuthFileStatusRuntimeId(file) === runtimeId).length !== 1) {
    return {
      target: currentTarget,
      scope: 'ambiguous',
      affectedFiles: siblings,
      failure: 'ambiguous',
    };
  }

  if (
    files.some(
      (file) =>
        file !== currentTarget &&
        readAuthFileStatusRuntimeId(file) === physicalName &&
        readAuthFileStatusPhysicalName(file) !== physicalName
    )
  ) {
    return {
      target: currentTarget,
      scope: 'ambiguous',
      affectedFiles: siblings,
      failure: 'ambiguous',
    };
  }

  if (siblings.length <= 1) {
    return {
      target: currentTarget,
      scope: 'credential',
      affectedFiles: [currentTarget],
      failure: null,
    };
  }

  const sourceRows = siblings.filter((file) => readAuthFileStatusRuntimeId(file) === physicalName);
  if (sourceRows.length > 1) {
    return {
      target: currentTarget,
      scope: 'ambiguous',
      affectedFiles: siblings,
      failure: 'ambiguous',
    };
  }
  if (sourceRows.length === 1) {
    return {
      target: currentTarget,
      scope: runtimeId === physicalName ? 'source-file' : 'expanded-child',
      affectedFiles: siblings,
      failure: null,
    };
  }

  return {
    target: currentTarget,
    scope: 'credential',
    affectedFiles: [currentTarget],
    failure: null,
  };
};

export const getAuthFileStatusMutationLockKeys = (
  files: AuthFileItem[],
  requestedTarget: AuthFileStatusMutationTarget
): Set<string> => {
  const target = normalizeTarget(requestedTarget);
  const keys = new Set<string>();
  if (target.name) {
    keys.add(selectionLockKey(getAuthFileStatusSelectionKey(target)));
    if (files.filter((file) => readAuthFileStatusPhysicalName(file) === target.name).length > 1) {
      keys.add(physicalFileLockKey(target.name));
    }
  }
  if (target.runtimeId) keys.add(runtimeLockKey(target.runtimeId));

  const resolution = resolveAuthFileStatusMutationTarget(files, requestedTarget);
  if (!resolution.target) return keys;

  const currentName = readAuthFileStatusPhysicalName(resolution.target);
  keys.add(selectionLockKey(getAuthFileStatusSelectionKey(resolution.target)));
  const currentRuntimeId = readAuthFileStatusRuntimeId(resolution.target);
  if (currentRuntimeId) keys.add(runtimeLockKey(currentRuntimeId));

  if (resolution.affectedFiles.length > 1 && currentName) {
    keys.add(physicalFileLockKey(currentName));
  }
  if (resolution.scope === 'source-file') {
    resolution.affectedFiles.forEach((file) => {
      keys.add(selectionLockKey(getAuthFileStatusSelectionKey(file)));
      const runtimeId = readAuthFileStatusRuntimeId(file);
      if (runtimeId) keys.add(runtimeLockKey(runtimeId));
    });
  }
  return keys;
};

export const authFileStatusMutationLockSetsOverlap = (
  left: Iterable<string>,
  right: Iterable<string>
): boolean => {
  const rightKeys = right instanceof Set ? right : new Set(right);
  for (const key of left) {
    if (rightKeys.has(key)) return true;
  }
  return false;
};
