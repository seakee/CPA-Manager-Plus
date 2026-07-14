import type { AuthFileItem } from '@/types';
import { resolveCodexChatgptAccountId } from '@/utils/quota';
import { normalizeAuthIndex } from '@/utils/authIndex';

const STORAGE_KEY = 'cli-proxy-codex-inspection-disable-ownership-v1';

type DisableOwnershipRecord = {
  fileName: string;
  authIndex: string | null;
  accountId: string | null;
  disabledAtMs: number;
};

type DisableOwnershipStore = Record<string, Record<string, DisableOwnershipRecord>>;

type OwnershipIdentity = {
  fileName: string;
  authIndex?: string | number | null;
  accountId?: string | null;
};

const readStore = (): DisableOwnershipStore => {
  try {
    if (typeof localStorage === 'undefined') return {};
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return {};
    const parsed = JSON.parse(raw) as unknown;
    return parsed && typeof parsed === 'object' && !Array.isArray(parsed)
      ? (parsed as DisableOwnershipStore)
      : {};
  } catch {
    return {};
  }
};

const writeStore = (store: DisableOwnershipStore) => {
  try {
    if (typeof localStorage === 'undefined') return;
    localStorage.setItem(STORAGE_KEY, JSON.stringify(store));
  } catch {
    // Ownership persistence is a safety enhancement. Failed writes leave
    // automatic recovery ineligible instead of blocking the inspection run.
  }
};

const normalizeAccountId = (value: unknown): string | null => {
  const normalized = typeof value === 'string' ? value.trim() : '';
  return normalized || null;
};

const identityFromFile = (file: AuthFileItem): OwnershipIdentity => ({
  fileName: file.name,
  authIndex: normalizeAuthIndex(file['auth_index'] ?? file.authIndex ?? file['auth-index']),
  accountId: resolveCodexChatgptAccountId(file),
});

const matchesIdentity = (record: DisableOwnershipRecord, identity: OwnershipIdentity): boolean => {
  if (record.fileName !== identity.fileName) return false;
  const authIndex = normalizeAuthIndex(identity.authIndex);
  const accountId = normalizeAccountId(identity.accountId);
  if (record.authIndex && record.authIndex !== authIndex) return false;
  if (record.accountId && record.accountId !== accountId) return false;
  return true;
};

export const recordCodexInspectionDisableOwnership = (
  scope: string,
  identity: OwnershipIdentity
) => {
  const normalizedScope = scope.trim();
  const fileName = identity.fileName.trim();
  if (!normalizedScope || !fileName) return;
  const store = readStore();
  store[normalizedScope] = {
    ...(store[normalizedScope] ?? {}),
    [fileName]: {
      fileName,
      authIndex: normalizeAuthIndex(identity.authIndex),
      accountId: normalizeAccountId(identity.accountId),
      disabledAtMs: Date.now(),
    },
  };
  writeStore(store);
};

export const clearCodexInspectionDisableOwnership = (scope: string, fileName: string) => {
  const normalizedScope = scope.trim();
  const normalizedFileName = fileName.trim();
  if (!normalizedScope || !normalizedFileName) return;
  const store = readStore();
  const scoped = store[normalizedScope];
  if (!scoped?.[normalizedFileName]) return;
  delete scoped[normalizedFileName];
  if (Object.keys(scoped).length === 0) delete store[normalizedScope];
  writeStore(store);
};

export const getCodexInspectionOwnedDisableFileNames = (
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
  Object.entries(scoped).forEach(([fileName, record]) => {
    const matches = files.some(
      (file) => file.disabled === true && matchesIdentity(record, identityFromFile(file))
    );
    if (matches) {
      owned.add(fileName);
      return;
    }
    delete scoped[fileName];
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
    if (typeof localStorage !== 'undefined') localStorage.removeItem(STORAGE_KEY);
  } catch {
    // Ignore storage cleanup failures.
  }
};
