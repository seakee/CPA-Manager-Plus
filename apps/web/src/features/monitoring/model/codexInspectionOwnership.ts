import type { AuthFileItem } from '@/types';
import { normalizeAuthIndex } from '@/utils/authIndex';
import {
  readAuthFileStatusAccountId,
  readAuthFileStatusAccountSnapshot,
  readAuthFileStatusProvider,
} from '@/utils/authFileStatusMutation';

const STORAGE_KEY = 'cli-proxy-codex-inspection-disable-ownership-v2';
const LEGACY_STORAGE_KEY = 'cli-proxy-codex-inspection-disable-ownership-v1';

type DisableOwnershipRecord = {
  fileName: string;
  provider?: string | null;
  authIndex: string | null;
  accountId: string | null;
  accountSnapshot?: string | null;
  disabledAtMs: number;
};

type DisableOwnershipStore = Record<string, Record<string, DisableOwnershipRecord>>;
type RawDisableOwnershipStore = Record<string, Record<string, Record<string, unknown>>>;
type DisableOwnershipReadResult = {
  store: DisableOwnershipStore;
  readable: boolean;
};

export type CodexInspectionOwnershipIdentity = {
  fileName: string;
  provider?: string | null;
  authIndex?: string | number | null;
  accountId?: string | null;
  accountSnapshot?: string | null;
};

const normalizeAccountId = (value: unknown): string | null => {
  const normalized = typeof value === 'string' ? value.trim() : '';
  return normalized || null;
};

const normalizeProvider = (value: unknown): string => {
  const normalized = typeof value === 'string' ? value.trim().toLowerCase().replace(/_/g, '-') : '';
  if (normalized === 'x-ai' || normalized === 'grok') return 'xai';
  return normalized;
};

const normalizeAccountSnapshot = (value: unknown, fileName: string): string | null => {
  const normalized = typeof value === 'string' ? value.trim() : '';
  return normalized && normalized !== fileName ? normalized : null;
};

type NormalizedOwnershipIdentity = {
  fileName: string;
  provider: string;
  authIndex: string | null;
  accountId: string | null;
  accountSnapshot: string | null;
};

const hasStableLocator = (identity: NormalizedOwnershipIdentity): boolean =>
  Boolean(identity.authIndex || identity.accountId || identity.accountSnapshot);

const normalizeIdentity = (identity: unknown): NormalizedOwnershipIdentity => {
  const source =
    identity && typeof identity === 'object' && !Array.isArray(identity)
      ? (identity as Record<string, unknown>)
      : {};
  const fileName = typeof source.fileName === 'string' ? source.fileName.trim() : '';
  const accountId = normalizeAccountId(source.accountId);
  return {
    fileName,
    provider: normalizeProvider(source.provider),
    authIndex: normalizeAuthIndex(source.authIndex),
    accountId,
    accountSnapshot: normalizeAccountSnapshot(source.accountSnapshot, fileName),
  };
};

export const getCodexInspectionOwnershipIdentityKey = (
  identity: CodexInspectionOwnershipIdentity
): string => {
  const normalized = normalizeIdentity(identity);
  return JSON.stringify([
    normalized.fileName,
    normalized.provider,
    normalized.authIndex,
    normalized.accountId,
    normalized.accountId ? null : normalized.accountSnapshot,
  ]);
};

export const hasCodexInspectionStableIdentity = (
  identity: CodexInspectionOwnershipIdentity
): boolean => {
  const normalized = normalizeIdentity(identity);
  return Boolean(normalized.fileName && normalized.provider && hasStableLocator(normalized));
};

const readObject = (value: unknown): Record<string, unknown> | null =>
  value && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;

const parseRawStore = (raw: string | null): RawDisableOwnershipStore => {
  if (!raw) return {};
  try {
    const parsed = JSON.parse(raw) as unknown;
    const root = readObject(parsed);
    if (!root) return {};
    const store: RawDisableOwnershipStore = {};
    Object.entries(root).forEach(([scope, records]) => {
      const normalizedScope = scope.trim();
      const scoped = readObject(records);
      if (!normalizedScope || !scoped) return;
      const sanitizedRecords: Record<string, Record<string, unknown>> = {};
      Object.entries(scoped).forEach(([key, record]) => {
        const normalizedKey = key.trim();
        const value = readObject(record);
        if (!normalizedKey || !value) return;
        sanitizedRecords[normalizedKey] = value;
      });
      if (Object.keys(sanitizedRecords).length > 0) store[normalizedScope] = sanitizedRecords;
    });
    return store;
  } catch {
    return {};
  }
};

const sanitizeOwnershipRecord = (
  record: Record<string, unknown>
): DisableOwnershipRecord | null => {
  const normalized = normalizeIdentity(record);
  const disabledAtMs = record.disabledAtMs;
  if (
    !normalized.fileName ||
    !normalized.provider ||
    !hasStableLocator(normalized) ||
    typeof disabledAtMs !== 'number' ||
    !Number.isFinite(disabledAtMs)
  ) {
    return null;
  }
  return {
    fileName: normalized.fileName,
    provider: normalized.provider,
    authIndex: normalized.authIndex,
    accountId: normalized.accountId,
    accountSnapshot: normalized.accountSnapshot,
    disabledAtMs,
  };
};

const parseStore = (raw: string | null): DisableOwnershipStore => {
  const store: DisableOwnershipStore = {};
  Object.entries(parseRawStore(raw)).forEach(([scope, records]) => {
    const scoped: Record<string, DisableOwnershipRecord> = {};
    Object.values(records).forEach((record) => {
      const sanitized = sanitizeOwnershipRecord(record);
      if (!sanitized) return;
      scoped[getCodexInspectionOwnershipIdentityKey(sanitized)] = sanitized;
    });
    if (Object.keys(scoped).length > 0) store[scope] = scoped;
  });
  return store;
};

const writeStore = (store: DisableOwnershipStore): boolean => {
  try {
    if (typeof localStorage === 'undefined') return false;
    localStorage.setItem(STORAGE_KEY, JSON.stringify(store));
    return true;
  } catch {
    // Ownership persistence is a safety enhancement. Failed writes leave
    // automatic recovery ineligible instead of blocking the inspection run.
    return false;
  }
};

const readStoreResult = (): DisableOwnershipReadResult => {
  try {
    if (typeof localStorage === 'undefined') return { store: {}, readable: false };
    const rawStore = localStorage.getItem(STORAGE_KEY);
    if (rawStore !== null) {
      const store = parseStore(rawStore);
      if (JSON.stringify(store) !== rawStore) writeStore(store);
      try {
        localStorage.removeItem(LEGACY_STORAGE_KEY);
      } catch {
        // A present v2 key is authoritative even when stale v1 cleanup fails.
      }
      return { store, readable: true };
    }

    const store: DisableOwnershipStore = {};
    const legacyStore = parseRawStore(localStorage.getItem(LEGACY_STORAGE_KEY));
    let migrated = false;

    Object.entries(legacyStore).forEach(([scope, records]) => {
      if (!records || typeof records !== 'object' || Array.isArray(records)) return;
      const scoped = { ...(store[scope] ?? {}) };
      Object.entries(records).forEach(([legacyFileName, record]) => {
        if (!record || typeof record !== 'object' || Array.isArray(record)) return;
        migrated = true;
        const accountId = normalizeAccountId(record.accountId);
        const normalized = normalizeIdentity({
          fileName: String(record.fileName ?? legacyFileName),
          provider: normalizeProvider(record.provider) || 'codex',
          authIndex: record.authIndex,
          accountId,
          accountSnapshot: record.accountSnapshot,
        });
        if (!normalized.fileName || !normalized.provider || !hasStableLocator(normalized)) return;
        const key = getCodexInspectionOwnershipIdentityKey(normalized);
        if (!scoped[key]) {
          scoped[key] = {
            fileName: normalized.fileName,
            provider: normalized.provider,
            authIndex: normalizeAuthIndex(normalized.authIndex),
            accountId: normalizeAccountId(normalized.accountId),
            accountSnapshot: normalized.accountSnapshot,
            disabledAtMs:
              typeof record.disabledAtMs === 'number' && Number.isFinite(record.disabledAtMs)
                ? record.disabledAtMs
                : Date.now(),
          };
        }
      });
      if (Object.keys(scoped).length > 0) store[scope] = scoped;
    });

    if (migrated && writeStore(store)) {
      try {
        localStorage.removeItem(LEGACY_STORAGE_KEY);
      } catch {
        // The v2 copy is already authoritative; a stale v1 key is harmless.
      }
    }
    return { store, readable: true };
  } catch {
    return { store: {}, readable: false };
  }
};

const readStore = (): DisableOwnershipStore => readStoreResult().store;

export const getCodexInspectionOwnershipIdentityForFile = (
  file: AuthFileItem
): CodexInspectionOwnershipIdentity => ({
  fileName: file.name,
  provider: readAuthFileStatusProvider(file),
  authIndex: normalizeAuthIndex(file['auth_index'] ?? file.authIndex ?? file['auth-index']),
  accountId: readAuthFileStatusAccountId(file),
  accountSnapshot: readAuthFileStatusAccountSnapshot(file),
});

const matchesIdentityForRecovery = (
  record: DisableOwnershipRecord,
  identity: CodexInspectionOwnershipIdentity
): boolean => {
  const normalizedRecord = normalizeIdentity(record);
  const normalizedIdentity = normalizeIdentity(identity);
  if (
    !normalizedRecord.provider ||
    !normalizedIdentity.provider ||
    normalizedRecord.fileName !== normalizedIdentity.fileName ||
    normalizedRecord.provider !== normalizedIdentity.provider
  )
    return false;
  if (normalizedRecord.authIndex && normalizedRecord.authIndex !== normalizedIdentity.authIndex)
    return false;
  if (normalizedRecord.accountId) {
    return normalizedRecord.accountId === normalizedIdentity.accountId;
  }
  if (normalizedRecord.accountSnapshot) {
    return normalizedRecord.accountSnapshot === normalizedIdentity.accountSnapshot;
  }
  return normalizedRecord.authIndex !== null;
};

const matchesIdentityForCleanup = (
  record: DisableOwnershipRecord,
  identity: CodexInspectionOwnershipIdentity
): boolean => {
  const normalizedRecord = normalizeIdentity(record);
  const normalizedIdentity = normalizeIdentity(identity);
  if (normalizedRecord.fileName !== normalizedIdentity.fileName) return false;
  if (normalizedRecord.provider && normalizedRecord.provider !== normalizedIdentity.provider)
    return false;
  if (normalizedRecord.authIndex && normalizedRecord.authIndex !== normalizedIdentity.authIndex)
    return false;
  if (normalizedRecord.accountId && normalizedRecord.accountId !== normalizedIdentity.accountId)
    return false;
  if (normalizedRecord.accountId) return true;
  if (
    normalizedRecord.accountSnapshot &&
    normalizedRecord.accountSnapshot !== normalizedIdentity.accountSnapshot
  )
    return false;
  return true;
};

export const recordCodexInspectionDisableOwnership = (
  scope: string,
  identity: CodexInspectionOwnershipIdentity
): boolean => {
  const normalizedScope = scope.trim();
  const normalizedIdentity = normalizeIdentity(identity);
  if (
    !normalizedScope ||
    !normalizedIdentity.fileName ||
    !normalizedIdentity.provider ||
    !hasStableLocator(normalizedIdentity)
  )
    return false;
  const { store, readable } = readStoreResult();
  if (!readable) return false;
  const key = getCodexInspectionOwnershipIdentityKey(normalizedIdentity);
  store[normalizedScope] = {
    ...(store[normalizedScope] ?? {}),
    [key]: {
      fileName: normalizedIdentity.fileName,
      provider: normalizedIdentity.provider,
      authIndex: normalizeAuthIndex(normalizedIdentity.authIndex),
      accountId: normalizeAccountId(normalizedIdentity.accountId),
      accountSnapshot: normalizedIdentity.accountSnapshot,
      disabledAtMs: Date.now(),
    },
  };
  return writeStore(store);
};

export const replaceCodexInspectionDisableOwnershipForFile = (
  scope: string,
  fileName: string,
  identities: CodexInspectionOwnershipIdentity[]
): boolean => {
  const normalizedScope = scope.trim();
  const normalizedFileName = fileName.trim();
  const normalizedIdentities = identities.map(normalizeIdentity);
  if (
    !normalizedScope ||
    !normalizedFileName ||
    normalizedIdentities.length === 0 ||
    normalizedIdentities.some(
      (identity) =>
        identity.fileName !== normalizedFileName ||
        !identity.provider ||
        !hasStableLocator(identity)
    )
  ) {
    return false;
  }

  const { store, readable } = readStoreResult();
  if (!readable) return false;
  const scoped = { ...(store[normalizedScope] ?? {}) };
  Object.entries(scoped).forEach(([key, record]) => {
    if (record.fileName === normalizedFileName) delete scoped[key];
  });
  normalizedIdentities.forEach((identity) => {
    scoped[getCodexInspectionOwnershipIdentityKey(identity)] = {
      fileName: identity.fileName,
      provider: identity.provider,
      authIndex: identity.authIndex,
      accountId: identity.accountId,
      accountSnapshot: identity.accountSnapshot,
      disabledAtMs: Date.now(),
    };
  });
  store[normalizedScope] = scoped;
  return writeStore(store);
};

export const clearCodexInspectionDisableOwnership = (
  scope: string,
  identity: CodexInspectionOwnershipIdentity
) => {
  const normalizedScope = scope.trim();
  if (!normalizedScope || !identity.fileName.trim()) return;
  const store = readStore();
  const scoped = store[normalizedScope];
  if (!scoped) return;
  const normalizedIdentity = normalizeIdentity(identity);
  let changed = false;
  Object.entries(scoped).forEach(([key, record]) => {
    if (!matchesIdentityForCleanup(record, normalizedIdentity)) return;
    delete scoped[key];
    changed = true;
  });
  if (!changed) return;
  if (Object.keys(scoped).length === 0) delete store[normalizedScope];
  writeStore(store);
};

export const clearCodexInspectionDisableOwnershipForFile = (scope: string, fileName: string) => {
  const normalizedScope = scope.trim();
  const normalizedFileName = fileName.trim();
  if (!normalizedScope || !normalizedFileName) return;
  const store = readStore();
  const scoped = store[normalizedScope];
  if (!scoped) return;
  let changed = false;
  Object.entries(scoped).forEach(([key, record]) => {
    if (record.fileName !== normalizedFileName) return;
    delete scoped[key];
    changed = true;
  });
  if (!changed) return;
  if (Object.keys(scoped).length === 0) delete store[normalizedScope];
  writeStore(store);
};

export const getCodexInspectionOwnedDisableIdentityKeys = (
  scope: string,
  files: AuthFileItem[]
): Set<string> => {
  const normalizedScope = scope.trim();
  if (!normalizedScope) return new Set();
  const store = readStore();
  const scoped = store[normalizedScope];
  if (!scoped) return new Set();

  const owned = new Set<string>();
  let changed = false;
  Object.entries(scoped).forEach(([key, record]) => {
    const matches = files.filter(
      (file) =>
        file.disabled === true &&
        matchesIdentityForRecovery(record, getCodexInspectionOwnershipIdentityForFile(file))
    );
    if (matches.length === 1) {
      owned.add(
        getCodexInspectionOwnershipIdentityKey(
          getCodexInspectionOwnershipIdentityForFile(matches[0])
        )
      );
      return;
    }
    delete scoped[key];
    changed = true;
  });
  if (changed) {
    if (Object.keys(scoped).length === 0) delete store[normalizedScope];
    writeStore(store);
  }
  return owned;
};

export const clearAllCodexInspectionDisableOwnership = () => {
  try {
    if (typeof localStorage === 'undefined') return;
    localStorage.removeItem(STORAGE_KEY);
    localStorage.removeItem(LEGACY_STORAGE_KEY);
  } catch {
    // Ignore storage cleanup failures.
  }
};
