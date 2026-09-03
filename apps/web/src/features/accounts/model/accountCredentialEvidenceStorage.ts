import type { AccountCredentialEvidenceBoundary } from './accountCredentialEvidence';
import { normalizeAuthFileCredentialStatusCode } from '@/features/authFiles/model/credentialStatus';

const STORAGE_KEY = 'cpa.accounts.credential-evidence-boundaries.v1';
const STORAGE_VERSION = 1;
const MAX_SCOPES = 8;
const MAX_BOUNDARIES_PER_KIND = 256;
const memoryScopes = new Map<string, StoredBoundaryScope>();

interface StoredBoundaryScope {
  updatedAtMs: number;
  evidence: Array<[string, AccountCredentialEvidenceBoundary]>;
  status: Array<[string, AccountCredentialEvidenceBoundary]>;
}

interface StoredBoundaryState {
  version: number;
  scopes: Record<string, StoredBoundaryScope>;
}

export interface AccountCredentialEvidenceBoundaryState {
  evidence: Map<string, AccountCredentialEvidenceBoundary>;
  status: Map<string, AccountCredentialEvidenceBoundary>;
}

const emptyBoundary = (): AccountCredentialEvidenceBoundary => ({
  localAtMs: 0,
  inspectionAtMs: 0,
  inspectionBaselinePending: false,
  headerAtMs: 0,
  headerBaselinePending: false,
  actionAtMs: 0,
  actionBaselinePending: false,
  authenticationActionAtMs: 0,
  authenticationActionBaselinePending: false,
  quotaActionAtMs: 0,
  quotaActionBaselinePending: false,
  cooldownAtMs: 0,
  cooldownBaselinePending: false,
  fallbackInspectionAtMs: 0,
  fallbackInspectionBaselinePending: false,
  fallbackHeaderAtMs: 0,
  fallbackHeaderBaselinePending: false,
  fallbackActionAtMs: 0,
  fallbackActionBaselinePending: false,
  fallbackCooldownAtMs: 0,
  fallbackCooldownBaselinePending: false,
  authenticationAtMs: 0,
  rawStatusAtMs: 0,
  rawStatusMessages: [],
  rawStatusCodes: [],
});

const readTimestamp = (value: unknown): number =>
  typeof value === 'number' && Number.isFinite(value) && value > 0 ? value : 0;

const readBoolean = (value: unknown): boolean => value === true;

const normalizeBoundary = (value: unknown): AccountCredentialEvidenceBoundary | null => {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
  const record = value as Record<string, unknown>;
  return {
    ...emptyBoundary(),
    localAtMs: readTimestamp(record.localAtMs),
    inspectionAtMs: readTimestamp(record.inspectionAtMs),
    inspectionBaselinePending: readBoolean(record.inspectionBaselinePending),
    headerAtMs: readTimestamp(record.headerAtMs),
    headerBaselinePending: readBoolean(record.headerBaselinePending),
    actionAtMs: readTimestamp(record.actionAtMs),
    actionBaselinePending: readBoolean(record.actionBaselinePending),
    authenticationActionAtMs: readTimestamp(record.authenticationActionAtMs),
    authenticationActionBaselinePending: readBoolean(record.authenticationActionBaselinePending),
    quotaActionAtMs: readTimestamp(record.quotaActionAtMs),
    quotaActionBaselinePending: readBoolean(record.quotaActionBaselinePending),
    cooldownAtMs: readTimestamp(record.cooldownAtMs),
    cooldownBaselinePending: readBoolean(record.cooldownBaselinePending),
    fallbackInspectionAtMs: readTimestamp(record.fallbackInspectionAtMs),
    fallbackInspectionBaselinePending: readBoolean(record.fallbackInspectionBaselinePending),
    fallbackHeaderAtMs: readTimestamp(record.fallbackHeaderAtMs),
    fallbackHeaderBaselinePending: readBoolean(record.fallbackHeaderBaselinePending),
    fallbackActionAtMs: readTimestamp(record.fallbackActionAtMs),
    fallbackActionBaselinePending: readBoolean(record.fallbackActionBaselinePending),
    fallbackCooldownAtMs: readTimestamp(record.fallbackCooldownAtMs),
    fallbackCooldownBaselinePending: readBoolean(record.fallbackCooldownBaselinePending),
    authenticationAtMs: readTimestamp(record.authenticationAtMs),
    rawStatusAtMs: readTimestamp(record.rawStatusAtMs),
    rawStatusMessages: Array.isArray(record.rawStatusMessages)
      ? record.rawStatusMessages
          .filter((item): item is string => typeof item === 'string')
          .map((item) => item.trim())
          .filter(Boolean)
          .slice(0, 32)
      : [],
    rawStatusCodes: Array.isArray(record.rawStatusCodes)
      ? Array.from(
          new Set(
            record.rawStatusCodes
              .map(normalizeAuthFileCredentialStatusCode)
              .filter((item): item is number => item !== null)
          )
        ).slice(0, 32)
      : [],
  };
};

const getStorage = (): Storage | null => {
  try {
    return typeof window !== 'undefined' && window.sessionStorage ? window.sessionStorage : null;
  } catch {
    return null;
  }
};

const readStoredState = (): StoredBoundaryState => {
  const storage = getStorage();
  if (!storage) return { version: STORAGE_VERSION, scopes: {} };
  try {
    const raw = storage.getItem(STORAGE_KEY);
    if (!raw) return { version: STORAGE_VERSION, scopes: {} };
    const parsed = JSON.parse(raw) as Partial<StoredBoundaryState>;
    if (parsed.version !== STORAGE_VERSION || !parsed.scopes || typeof parsed.scopes !== 'object') {
      return { version: STORAGE_VERSION, scopes: {} };
    }
    return { version: STORAGE_VERSION, scopes: parsed.scopes };
  } catch {
    return { version: STORAGE_VERSION, scopes: {} };
  }
};

const normalizeEntries = (value: unknown): Map<string, AccountCredentialEvidenceBoundary> => {
  const entries = new Map<string, AccountCredentialEvidenceBoundary>();
  if (!Array.isArray(value)) return entries;
  value.slice(-MAX_BOUNDARIES_PER_KIND).forEach((item) => {
    if (!Array.isArray(item) || item.length !== 2 || typeof item[0] !== 'string') return;
    const key = item[0].trim();
    const boundary = normalizeBoundary(item[1]);
    if (key && boundary) entries.set(key, boundary);
  });
  return entries;
};

const getBoundaryUpdatedAtMs = (boundary: AccountCredentialEvidenceBoundary): number =>
  Math.max(
    boundary.localAtMs,
    boundary.inspectionAtMs,
    boundary.headerAtMs,
    boundary.actionAtMs,
    boundary.authenticationActionAtMs,
    boundary.quotaActionAtMs,
    boundary.cooldownAtMs,
    boundary.fallbackInspectionAtMs,
    boundary.fallbackHeaderAtMs,
    boundary.fallbackActionAtMs,
    boundary.fallbackCooldownAtMs,
    boundary.authenticationAtMs ?? 0,
    boundary.rawStatusAtMs
  );

const serializeBoundaryEntries = (
  entries: ReadonlyMap<string, AccountCredentialEvidenceBoundary>
): Array<[string, AccountCredentialEvidenceBoundary]> =>
  Array.from(entries.entries())
    .sort((left, right) => getBoundaryUpdatedAtMs(left[1]) - getBoundaryUpdatedAtMs(right[1]))
    .slice(-MAX_BOUNDARIES_PER_KIND);

export const loadAccountCredentialEvidenceBoundaryState = (
  scopeKey: string
): AccountCredentialEvidenceBoundaryState => {
  const normalizedScopeKey = scopeKey.trim();
  if (!normalizedScopeKey) return { evidence: new Map(), status: new Map() };
  const scope =
    memoryScopes.get(normalizedScopeKey) ?? readStoredState().scopes[normalizedScopeKey];
  if (!scope || typeof scope !== 'object') return { evidence: new Map(), status: new Map() };
  return {
    evidence: normalizeEntries(scope.evidence),
    status: normalizeEntries(scope.status),
  };
};

export const saveAccountCredentialEvidenceBoundaryState = (
  scopeKey: string,
  state: AccountCredentialEvidenceBoundaryState
): void => {
  const normalizedScopeKey = scopeKey.trim();
  const storage = getStorage();
  if (!normalizedScopeKey) return;
  const nextScope: StoredBoundaryScope = {
    updatedAtMs: Date.now(),
    evidence: serializeBoundaryEntries(state.evidence),
    status: serializeBoundaryEntries(state.status),
  };
  memoryScopes.set(normalizedScopeKey, nextScope);
  Array.from(memoryScopes.entries())
    .sort((left, right) => right[1].updatedAtMs - left[1].updatedAtMs)
    .slice(MAX_SCOPES)
    .forEach(([key]) => memoryScopes.delete(key));
  if (!storage) return;
  const stored = readStoredState();
  stored.scopes[normalizedScopeKey] = nextScope;
  const retainedScopes = Object.entries(stored.scopes)
    .sort(
      (left, right) => readTimestamp(right[1]?.updatedAtMs) - readTimestamp(left[1]?.updatedAtMs)
    )
    .slice(0, MAX_SCOPES);
  try {
    storage.setItem(
      STORAGE_KEY,
      JSON.stringify({ version: STORAGE_VERSION, scopes: Object.fromEntries(retainedScopes) })
    );
  } catch {
    // Evidence boundaries are an in-session hardening layer; storage failures remain non-fatal.
  }
};

export const clearAccountCredentialEvidenceBoundaryStateCache = (): void => {
  memoryScopes.clear();
  try {
    getStorage()?.removeItem(STORAGE_KEY);
  } catch {
    // Ignore unavailable or blocked session storage.
  }
};
