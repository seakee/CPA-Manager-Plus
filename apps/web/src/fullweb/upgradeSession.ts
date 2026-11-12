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
    public readonly reason: 'invalid' | 'unavailable' | 'changed' | 'network' | 'origin_rejected',
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
    (source && result.source_revision !== source)
  ) {
    throw new UpgradeAuthenticationError('network');
  }
  return { revision: result.source_revision, observation };
}
