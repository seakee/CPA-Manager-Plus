import type { AuthFileItem } from '@/types';
import { normalizeAuthIndex } from '@/utils/authIndex';
import { resolveCodexChatgptAccountId } from '@/utils/quota/resolvers';
import { resolveAuthProvider } from '@/utils/quota/validators';

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

export const readAuthFileStatusRuntimeId = (file: AuthFileItem): string =>
  typeof file.id === 'string' ? file.id.trim() : '';

export const readAuthFileStatusPhysicalName = (file: AuthFileItem): string =>
  String(file.name ?? '').trim();

export const readAuthFileStatusAuthIndex = (file: AuthFileItem): string | null =>
  normalizeAuthIndex(file.authIndex ?? file['auth_index'] ?? file['auth-index']);

const normalizeProvider = (value: unknown): string => {
  const normalized = String(value ?? '')
    .trim()
    .toLowerCase()
    .replace(/_/g, '-');
  if (normalized === 'x-ai' || normalized === 'grok') return 'xai';
  return normalized;
};

const normalizeIdentityValue = (value: unknown): string =>
  typeof value === 'string' ? value.trim() : '';

const firstNonEmptyIdentityValue = (...values: unknown[]): string => {
  for (const value of values) {
    const normalized = normalizeIdentityValue(value);
    if (normalized) return normalized;
  }
  return '';
};

export const readAuthFileStatusProvider = (file: AuthFileItem): string =>
  normalizeProvider(
    firstNonEmptyIdentityValue(file.provider, file.type, file.typo) || resolveAuthProvider(file)
  );

const readRecord = (value: unknown): Record<string, unknown> | null =>
  value && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;

const ACCOUNT_ID_FIELD_NAMES = [
  'account_id',
  'accountId',
  'chatgpt_account_id',
  'chatgptAccountId',
  'project_id',
  'projectId',
  'gemini_virtual_project',
  'geminiVirtualProject',
  'sub',
] as const;

const readAccountIdFromValue = (value: unknown, seen = new Set<object>()): string => {
  const record = readRecord(value);
  if (!record || seen.has(record)) return '';
  seen.add(record);

  for (const field of ACCOUNT_ID_FIELD_NAMES) {
    const normalized = normalizeIdentityValue(record[field]);
    if (normalized) return normalized;
  }
  for (const field of ['id_token', 'idToken'] as const) {
    const nested = readAccountIdFromValue(record[field], seen);
    if (nested) return nested;
  }
  return '';
};

export const readAuthFileStatusAccountId = (file: AuthFileItem): string => {
  const direct = readAccountIdFromValue(file);
  if (direct) return direct;
  for (const field of ['id_token', 'idToken', 'metadata', 'attributes'] as const) {
    const nested = readAccountIdFromValue(file[field]);
    if (nested) return nested;
  }
  return normalizeIdentityValue(resolveCodexChatgptAccountId(file));
};

export const readAuthFileStatusAccountSnapshot = (file: AuthFileItem): string => {
  for (const value of [file.account, file.email, file.display_account, file.displayAccount]) {
    const normalized = normalizeIdentityValue(value);
    if (normalized) return normalized;
  }
  return '';
};

const normalizeIdentityTarget = (target: AuthFileStatusIdentityTarget) => {
  const record = target as AuthFileItem & AuthFileStatusMutationTarget;
  const name = String(record.name ?? '').trim();
  const directAccountSnapshot = normalizeIdentityValue(record.accountSnapshot);
  return {
    name,
    runtimeId: normalizeIdentityValue(record.runtimeId) || readAuthFileStatusRuntimeId(record),
    authIndex: normalizeAuthIndex(record.authIndex ?? record['auth_index'] ?? record['auth-index']),
    provider: readAuthFileStatusProvider(record),
    accountId: normalizeIdentityValue(record.accountId) || readAuthFileStatusAccountId(record),
    accountSnapshot: directAccountSnapshot || readAuthFileStatusAccountSnapshot(record),
  };
};

const normalizeTarget = (target: AuthFileStatusMutationTarget) => normalizeIdentityTarget(target);

export const getAuthFileStatusIdentityKey = (target: AuthFileStatusIdentityTarget): string => {
  const normalized = normalizeIdentityTarget(target);
  if (normalized.authIndex) return `${normalized.name}::${normalized.authIndex}`;

  const accountSnapshot =
    normalized.accountSnapshot && normalized.accountSnapshot !== normalized.name
      ? normalized.accountSnapshot
      : '';
  if (normalized.accountId || accountSnapshot) {
    return `${normalized.name}::-::${JSON.stringify([
      normalized.name,
      normalized.provider,
      null,
      normalized.accountId || null,
      normalized.accountId ? null : accountSnapshot || null,
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
  if (!provider || !target.provider || provider !== target.provider) return false;

  if (target.accountId) {
    return readAuthFileStatusAccountId(file) === target.accountId;
  }

  if (target.accountSnapshot && target.accountSnapshot !== target.name) {
    return readAuthFileStatusAccountSnapshot(file) === target.accountSnapshot;
  }

  // CPA auth entries are allowed to omit account metadata. A stable auth index,
  // together with the already-checked runtime ID, physical name, and provider,
  // still identifies the current credential without falling back to a filename-only match.
  return target.authIndex !== null;
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
