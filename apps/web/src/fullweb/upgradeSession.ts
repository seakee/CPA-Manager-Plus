import { obfuscatedStorage } from '../services/storage/secureStorage';
import { useAuthStore } from '../stores/useAuthStore';
import { STORAGE_KEY_AUTH } from '../utils/constants';

export const UPGRADE_AUTH_PATH = '/.well-known/cpamp-bootstrap/upgrade/authenticate';
export const UPGRADE_AUTH_CONTRACT = 'cpamp.upgrade-auth.v1';

const managerGeneratedKey = (key: string) =>
  key.startsWith('cpamp_') || key.startsWith('cmp_admin_');

// A legacy base must explicitly identify this origin's root Manager endpoint.
// Never infer it from location, logged-in flags, a CPA URL, or a missing base.
function currentManagerBase(value: unknown): boolean {
  if (typeof value !== 'string') return false;
  try {
    const url = new URL(value);
    return (
      url.origin === window.location.origin &&
      /^\/*$/.test(url.pathname) &&
      !url.username &&
      !url.password &&
      !url.search &&
      !url.hash
    );
  } catch {
    return false;
  }
}

export function rememberedUpgradeCredential(): string {
  try {
    const saved = obfuscatedStorage.getItem<{ state?: Record<string, unknown> }>(
      STORAGE_KEY_AUTH
    )?.state;
    if (
      saved &&
      currentManagerBase(saved.apiBase) &&
      typeof saved.managementKey === 'string' &&
      saved.rememberPassword !== false &&
      (saved.sessionMode === 'manager_embedded' ||
        (!saved.sessionMode && managerGeneratedKey(saved.managementKey))) &&
      (!saved.sessionPanelBase || currentManagerBase(saved.sessionPanelBase))
    ) {
      return saved.managementKey;
    }
    if (saved) return '';
    // Old standalone keys have no runtime tag. Only an explicit same-Manager
    // base and Manager-generated key prefix make them eligible for auto-restore.
    const base = obfuscatedStorage.getItem('apiBase') || obfuscatedStorage.getItem('apiUrl');
    const key = obfuscatedStorage.getItem('managementKey');
    return currentManagerBase(base) && typeof key === 'string' && managerGeneratedKey(key)
      ? key
      : '';
  } catch {
    return '';
  }
}

export function rememberUpgradeCredential(credential: string, remember: boolean) {
  try {
    // Preserve the original auth store's persistence format, without activating
    // normal Console authentication or fetching configuration from the old DB.
    useAuthStore.setState({
      isAuthenticated: false,
      apiBase: window.location.origin,
      managementKey: credential,
      rememberPassword: remember,
      sessionMode: 'manager_embedded',
      sessionPanelBase: window.location.origin,
      connectionStatus: 'disconnected',
    });
  } catch {
    /* A blocked/full browser store must not prevent manual login. */
  }
}

export class UpgradeAuthenticationError extends Error {
  constructor(
    public readonly reason:
      | 'invalid'
      | 'unavailable'
      | 'changed'
      | 'network'
      | 'origin_rejected'
      | 'source_ownership_unavailable',
    public readonly expectedOrigin?: string
  ) {
    super(reason);
  }
}

function normalizeExpectedOrigin(value: unknown): string | undefined {
  if (typeof value !== 'string' || !value.trim()) return undefined;
  try {
    const url = new URL(value);
    if (
      (url.protocol !== 'http:' && url.protocol !== 'https:') ||
      url.pathname !== '/' ||
      url.username ||
      url.password ||
      url.search ||
      url.hash
    ) {
      return undefined;
    }
    return url.origin;
  } catch {
    return undefined;
  }
}

// The entry status the server answered for a proved credential. `unsupported_upgrade`
// is the public observation of a source this build can classify. `observation_unavailable`
// means the credential was proved but the public observation refused the source (a
// live WAL is recovery-owned): it authenticates the operator read-only and is never
// a migration permission, and it is never a credential verdict.
export type UpgradeEntryObservation = 'unsupported_upgrade' | 'observation_unavailable';

export interface VerifiedUpgradeCredential {
  revision: string;
  observation: UpgradeEntryObservation;
  operationRevision?: string;
}

export async function verifyUpgradeCredential(
  credential: string,
  signal: AbortSignal,
  source?: string
): Promise<VerifiedUpgradeCredential> {
  const headers: Record<string, string> = {
    Accept: 'application/json',
    Authorization: `Bearer ${credential}`,
  };
  if (source) headers['X-CPAMP-Upgrade-Source'] = source;
  const response = await fetch(UPGRADE_AUTH_PATH, {
    method: 'POST',
    mode: 'same-origin',
    credentials: 'omit',
    redirect: 'error',
    cache: 'no-store',
    headers,
    signal,
  });
  if (response.status === 403) {
    let payload: unknown = null;
    try {
      payload = await response.json();
    } catch {
      /* Non-JSON 403 falls back to generic unavailability below. */
    }
    if (payload && typeof payload === 'object') {
      const envelope = payload as Record<string, unknown>;
      const err =
        envelope.error && typeof envelope.error === 'object'
          ? (envelope.error as Record<string, unknown>)
          : null;
      if (
        envelope.contract_version === UPGRADE_AUTH_CONTRACT &&
        err?.code === 'origin_rejected'
      ) {
        throw new UpgradeAuthenticationError(
          'origin_rejected',
          normalizeExpectedOrigin(err.expected_origin)
        );
      }
    }
    throw new UpgradeAuthenticationError('unavailable');
  }
  if (response.status === 503) {
    let payload: unknown = null;
    try {
      payload = await response.json();
    } catch {
      /* Non-JSON 503 falls back to generic unavailability below. */
    }
    if (payload && typeof payload === 'object') {
      const envelope = payload as Record<string, unknown>;
      const err =
        envelope.error && typeof envelope.error === 'object'
          ? (envelope.error as Record<string, unknown>)
          : null;
      if (
        envelope.contract_version === UPGRADE_AUTH_CONTRACT &&
        err?.code === 'source_ownership_unavailable'
      ) {
        throw new UpgradeAuthenticationError('source_ownership_unavailable');
      }
    }
    throw new UpgradeAuthenticationError('unavailable');
  }
  if (response.status !== 200) {
    throw new UpgradeAuthenticationError(
      response.status === 401 ? 'invalid' : response.status === 409 ? 'changed' : 'unavailable'
    );
  }
  const data: unknown = await response.json();
  if (!data || typeof data !== 'object') throw new UpgradeAuthenticationError('network');
  const result = data as Record<string, unknown>;
  // A proved credential answers one of exactly two entry statuses. Anything else
  // is a reply this build cannot read, never a status it guesses at.
  const observation = result.status;
  if (observation !== 'unsupported_upgrade' && observation !== 'observation_unavailable') {
    throw new UpgradeAuthenticationError('network');
  }
  if (
    result.contract_version !== UPGRADE_AUTH_CONTRACT ||
    result.authenticated !== true ||
    result.migration_available !== false ||
    result.expires_in_seconds !== 60 ||
    typeof result.source_revision !== 'string' ||
    !/^[a-f0-9]{64}$/.test(result.source_revision) ||
    (result.operation_revision !== undefined &&
      (typeof result.operation_revision !== 'string' ||
        !/^[a-f0-9]{64}$/.test(result.operation_revision)))
  ) {
    throw new UpgradeAuthenticationError('network');
  }
  return {
    revision: result.source_revision,
    observation,
    ...(typeof result.operation_revision === 'string'
      ? { operationRevision: result.operation_revision }
      : {}),
  };
}

export const UPGRADE_COMMITTED_STATUS_KEY = 'cpamp.upgrade.committed_status.v1';

export interface PersistedCommittedUpgradeStatus {
  controlBasePath: string;
  revision?: string;
  activationRequested?: boolean;
  snapshot: Record<string, unknown>;
}

export interface RememberCommittedUpgradeStatusOptions {
  revision?: string;
  activationRequested?: boolean;
}

function normalizeCommittedRevision(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined;
  const trimmed = value.trim();
  if (!trimmed || trimmed !== value || /\s/.test(trimmed) || trimmed.length > 128) {
    return undefined;
  }
  return trimmed;
}

function committedStatusStorage(): Storage | null {
  try {
    if (typeof sessionStorage !== 'undefined' && sessionStorage) {
      return sessionStorage;
    }
    if (typeof window !== 'undefined' && window.sessionStorage) {
      return window.sessionStorage;
    }
  } catch {
    return null;
  }
  return null;
}

function isSafeCommittedPrefix(value: unknown): value is string {
  if (typeof value !== 'string') return false;
  const trimmed = value.trim();
  if (!trimmed || trimmed !== value) return false;
  if (!trimmed.startsWith('/') || trimmed === '/' || trimmed.endsWith('/')) return false;
  if (trimmed.includes('//') || /[\s?#%]/.test(trimmed)) return false;
  return true;
}

function stripUrlCredentials(value: unknown): string | undefined {
  if (typeof value !== 'string' || !value.trim()) return undefined;
  try {
    const parsed = new URL(value);
    if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') return undefined;
    parsed.username = '';
    parsed.password = '';
    parsed.search = '';
    parsed.hash = '';
    return parsed.toString().replace(/\/+$/, '') || parsed.origin;
  } catch {
    return undefined;
  }
}

function sanitizeCommittedRouting(raw: unknown): Record<string, unknown> | undefined {
  if (!raw || typeof raw !== 'object') return undefined;
  const doc = raw as Record<string, unknown>;
  if (doc.committed !== true || !isSafeCommittedPrefix(doc.controlBasePath)) return undefined;
  const routing: Record<string, unknown> = {
    configured: Boolean(doc.configured),
    committed: true,
    controlBasePath: doc.controlBasePath,
  };
  if (typeof doc.existingPrefixesPreserved === 'boolean') {
    routing.existingPrefixesPreserved = doc.existingPrefixesPreserved;
  }
  if (typeof doc.inferenceBasePath === 'string') {
    routing.inferenceBasePath = doc.inferenceBasePath;
  }
  if (typeof doc.sharedEntry === 'boolean') {
    routing.sharedEntry = doc.sharedEntry;
  }
  const safeExternalUrl = stripUrlCredentials(doc.externalBaseUrl);
  if (safeExternalUrl) {
    routing.externalBaseUrl = safeExternalUrl;
  }
  if (typeof doc.suggestedControlBasePath === 'string') {
    routing.suggestedControlBasePath = doc.suggestedControlBasePath;
  }
  if (typeof doc.suggestedInferenceBasePath === 'string') {
    routing.suggestedInferenceBasePath = doc.suggestedInferenceBasePath;
  }
  return routing;
}

function sanitizeCommittedAccessInfo(raw: unknown): Record<string, unknown> | undefined {
  if (!raw || typeof raw !== 'object') return undefined;
  const doc = raw as Record<string, unknown>;
  if (typeof doc.mode !== 'string' || typeof doc.managementBasePath !== 'string') return undefined;
  const accessInfo: Record<string, unknown> = {
    mode: doc.mode,
    managementBasePath: doc.managementBasePath,
    inferenceBasePath: typeof doc.inferenceBasePath === 'string' ? doc.inferenceBasePath : '',
    protocols: Array.isArray(doc.protocols) ? doc.protocols : [],
    ready: Boolean(doc.ready),
  };
  const safeExternalUrl = stripUrlCredentials(doc.externalBaseUrl);
  if (safeExternalUrl) {
    accessInfo.externalBaseUrl = safeExternalUrl;
  }
  if (typeof doc.sharedEntry === 'boolean') {
    accessInfo.sharedEntry = doc.sharedEntry;
  }
  if (typeof doc.code === 'string' && doc.code) {
    accessInfo.code = doc.code;
  }
  return accessInfo;
}

function sanitizeCommittedSnapshot(
  raw: unknown
): PersistedCommittedUpgradeStatus | undefined {
  if (!raw || typeof raw !== 'object') return undefined;
  const doc = raw as Record<string, unknown>;
  if (doc.stage !== 'complete' || doc.stageKnown !== true) return undefined;
  const routing = sanitizeCommittedRouting(doc.routing);
  if (!routing) return undefined;
  const controlBasePath = routing.controlBasePath as string;
  const snapshot: Record<string, unknown> = {
    contractVersion: doc.contractVersion,
    recorded: Boolean(doc.recorded),
    stage: 'complete',
    stageKnown: true,
    nextAction: typeof doc.nextAction === 'string' ? doc.nextAction : 'complete',
    domainsDone: Array.isArray(doc.domainsDone) ? doc.domainsDone : [],
    sourceKind: typeof doc.sourceKind === 'string' ? doc.sourceKind : '',
    sourceVersion: typeof doc.sourceVersion === 'string' ? doc.sourceVersion : '',
    reconciliation:
      doc.reconciliation && typeof doc.reconciliation === 'object' ? doc.reconciliation : {},
    manualItems: Array.isArray(doc.manualItems) ? doc.manualItems : [],
    routing,
  };
  if (typeof doc.errorCode === 'string') snapshot.errorCode = doc.errorCode;
  if (typeof doc.errorDetail === 'string') snapshot.errorDetail = doc.errorDetail;
  if (typeof doc.targetMode === 'string') snapshot.targetMode = doc.targetMode;
  if (typeof doc.updatedAtMs === 'number') snapshot.updatedAtMs = doc.updatedAtMs;
  const accessInfo = sanitizeCommittedAccessInfo(doc.accessInfo);
  if (accessInfo) snapshot.accessInfo = accessInfo;
  return { controlBasePath, snapshot };
}

export function rememberCommittedUpgradeStatus(
  snapshot: Record<string, unknown>,
  options?: RememberCommittedUpgradeStatusOptions
): PersistedCommittedUpgradeStatus | undefined {
  const sanitized = sanitizeCommittedSnapshot(snapshot);
  if (!sanitized) return undefined;
  const storage = committedStatusStorage();
  const existing = storage ? rememberedCommittedUpgradeStatus() : undefined;
  const explicitRevision = normalizeCommittedRevision(options?.revision);
  const sameScope = Boolean(
    existing &&
      existing.controlBasePath === sanitized.controlBasePath &&
      existing.revision === explicitRevision
  );
  const activationRequested =
    typeof options?.activationRequested === 'boolean'
      ? options.activationRequested
      : sameScope
        ? existing?.activationRequested
        : undefined;
  const next: PersistedCommittedUpgradeStatus = {
    controlBasePath: sanitized.controlBasePath,
    ...(explicitRevision ? { revision: explicitRevision } : {}),
    ...(typeof activationRequested === 'boolean' ? { activationRequested } : {}),
    snapshot: sanitized.snapshot,
  };
  if (storage) {
    try {
      storage.setItem(UPGRADE_COMMITTED_STATUS_KEY, JSON.stringify(next));
    } catch {
      /* Ignore storage quota or private-mode failures. */
    }
  }
  return next;
}

export function rememberedCommittedUpgradeStatus(
  expectedRevision?: string
): PersistedCommittedUpgradeStatus | undefined {
  const storage = committedStatusStorage();
  if (!storage) return undefined;
  try {
    const raw = storage.getItem(UPGRADE_COMMITTED_STATUS_KEY);
    if (!raw) return undefined;
    const parsed: unknown = JSON.parse(raw);
    if (!parsed || typeof parsed !== 'object') {
      clearCommittedUpgradeStatus();
      return undefined;
    }
    const envelope = parsed as Record<string, unknown>;
    const sanitized = sanitizeCommittedSnapshot(envelope.snapshot);
    if (!sanitized || envelope.controlBasePath !== sanitized.controlBasePath) {
      clearCommittedUpgradeStatus();
      return undefined;
    }
    if (envelope.revision !== undefined && !normalizeCommittedRevision(envelope.revision)) {
      clearCommittedUpgradeStatus();
      return undefined;
    }
    const revision = normalizeCommittedRevision(envelope.revision);
    const activationRequested =
      typeof envelope.activationRequested === 'boolean'
        ? envelope.activationRequested
        : undefined;
    if (expectedRevision) {
      const expected = normalizeCommittedRevision(expectedRevision);
      if (!expected || !revision || revision !== expected) {
        clearCommittedUpgradeStatus();
        return undefined;
      }
    }
    return {
      controlBasePath: sanitized.controlBasePath,
      ...(revision ? { revision } : {}),
      ...(typeof activationRequested === 'boolean' ? { activationRequested } : {}),
      snapshot: sanitized.snapshot,
    };
  } catch {
    return undefined;
  }
}

export function clearCommittedUpgradeStatus(): void {
  const storage = committedStatusStorage();
  if (!storage) return;
  try {
    storage.removeItem(UPGRADE_COMMITTED_STATUS_KEY);
  } catch {
    /* Ignore storage cleanup failures. */
  }
}
