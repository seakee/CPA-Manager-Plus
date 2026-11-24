import type { AuthFileItem } from '@/types';
import type { SiwcBindStartResponse } from '@/services/api/oauth';

export type SiwcBindingStatus = 'active' | 'invalid' | 'bound' | 'unbound';

const SIWC_CALLBACK_PROTOCOL = 'http:';
const SIWC_CALLBACK_HOSTNAME = '127.0.0.1';
const SIWC_CALLBACK_PORT = '1455';
const SIWC_CALLBACK_PATH = '/auth/callback';

const readBooleanField = (file: AuthFileItem, ...keys: string[]): boolean => {
  for (const key of keys) {
    const value = file[key];
    if (typeof value === 'boolean') return value;
  }
  return false;
};

const readStringField = (record: Record<string, unknown>, ...keys: string[]): string => {
  for (const key of keys) {
    const value = record[key];
    if (typeof value === 'string') {
      const trimmed = value.trim();
      if (trimmed) return trimmed;
    }
  }
  return '';
};

export function readSiwcStatus(file: AuthFileItem): string {
  return readStringField(file, 'siwc_status', 'siwcStatus').toLowerCase();
}

export function readSiwcExpiresAt(file: AuthFileItem): string | undefined {
  const value = readStringField(file, 'siwc_expires_at', 'siwcExpiresAt');
  return value || undefined;
}

export function supportsSiwcBinding(file: AuthFileItem): boolean {
  return readBooleanField(file, 'siwc_bind_supported', 'siwcBindSupported');
}

export function isSiwcBound(file: AuthFileItem): boolean {
  return readBooleanField(file, 'siwc_bound', 'siwcBound');
}

/**
 * Matches CPA's server-side binding predicate (`auth_files.go:870-874`):
 * `siwc` nested object must contain non-empty `access_token`, `refresh_token`, and `client_id`.
 */
export function hasSiwcCredentialTokens(record: Record<string, unknown> | null | undefined): boolean {
  if (!record || typeof record !== 'object' || Array.isArray(record)) return false;
  const section = record.siwc;
  if (!section || typeof section !== 'object' || Array.isArray(section)) return false;
  const siwc = section as Record<string, unknown>;
  return (
    readStringField(siwc, 'access_token', 'accessToken').length > 0 &&
    readStringField(siwc, 'refresh_token', 'refreshToken').length > 0 &&
    readStringField(siwc, 'client_id', 'clientId').length > 0
  );
}

export function listSiwcTargets(files: AuthFileItem[]): AuthFileItem[] {
  return files
    .filter((file) => Boolean(file.name) && supportsSiwcBinding(file))
    .slice()
    .sort((a, b) => a.name.localeCompare(b.name));
}

export function getSiwcBindingStatus(file: AuthFileItem): SiwcBindingStatus {
  const enabled = readBooleanField(file, 'use_siwc', 'useSiwc');
  const rawStatus = readSiwcStatus(file);
  if (enabled && rawStatus === 'invalid') {
    return 'invalid';
  }
  const bound = isSiwcBound(file);
  if (!bound) return 'unbound';
  return enabled ? 'active' : 'bound';
}

export function readSiwcExpiresAtForDisplay(file: AuthFileItem): string | undefined {
  if (getSiwcBindingStatus(file) === 'unbound') return undefined;
  return readSiwcExpiresAt(file);
}

export function isSiwcCallbackValid(rawUrl: string, expectedState: string): boolean {
  const trimmed = rawUrl.trim();
  if (!trimmed || !expectedState) return false;
  let parsed: URL;
  try {
    parsed = new URL(trimmed);
  } catch {
    return false;
  }
  if (
    parsed.protocol !== SIWC_CALLBACK_PROTOCOL ||
    parsed.hostname !== SIWC_CALLBACK_HOSTNAME ||
    parsed.port !== SIWC_CALLBACK_PORT ||
    parsed.pathname !== SIWC_CALLBACK_PATH ||
    parsed.hash
  ) {
    return false;
  }

  const stateValues = parsed.searchParams.getAll('state');
  if (stateValues.length !== 1 || stateValues[0] !== expectedState) return false;

  const errorValues = parsed.searchParams.getAll('error');
  const codeValues = parsed.searchParams.getAll('code');
  const clientIdValues = parsed.searchParams.getAll('client_id');

  const hasError = errorValues.length === 1 && errorValues[0].trim().length > 0;
  if (errorValues.length > 1) return false;
  if (hasError) return true;

  if (codeValues.length !== 1 || codeValues[0].trim().length === 0) return false;
  if (clientIdValues.length !== 1 || clientIdValues[0].trim().length === 0) return false;
  return true;
}

export interface SiwcStartHandle {
  readonly promise: Promise<SiwcBindStartResponse>;
  isCurrent: () => boolean;
  cancel: () => void;
}

export interface SiwcStartDeps {
  start: (name: string) => Promise<SiwcBindStartResponse>;
  cancelSession: (state: string) => Promise<unknown>;
  isClosed: () => boolean;
}

/**
 * Starts a SIWC binding session without aborting the in-flight HTTP request.
 * If the dialog closes or is cancelled before the response lands, the late
 * response immediately calls `cancelSession(state)` on the same captured
 * transport so no orphan SIWC session remains active on CPA.
 */
export function createSiwcStart(name: string, deps: SiwcStartDeps): SiwcStartHandle {
  let cancelled = false;
  const isCurrent = () => !cancelled && !deps.isClosed();

  const promise = deps.start(name).then((response) => {
    if (!isCurrent() && response?.state) {
      void deps.cancelSession(response.state).catch(() => {});
    }
    return response;
  });

  return {
    promise,
    isCurrent,
    cancel: () => {
      cancelled = true;
    },
  };
}
