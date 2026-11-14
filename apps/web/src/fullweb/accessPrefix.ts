// Mirrors the manager-server canonical path and reserved-namespace rules so the
// wizard gives immediate feedback before a request is sent. The server remains
// the final authority for every route write.
export const RESERVED_ACCESS_PATHS: readonly string[] = [
  '/.well-known',
  '/.well-known/cpamp-bootstrap',
  '/favicon.ico',
  '/apple-touch-icon.png',
  '/assets',
  '/health',
  '/healthz',
  '/keepalive',
  '/management.html',
  '/models',
  '/setup',
  '/status',
  '/v0',
  '/v1',
  '/v1beta',
  '/v8',
  '/openai/v1',
  '/backend-api/codex',
  '/api/provider',
  '/api/v2',
  '/api/gateway',
  '/callback',
  '/anthropic/callback',
  '/codex/callback',
  '/antigravity/callback',
  '/devin/callback',
  '/usage-service',
  '/internal',
  '/internal/routing/fresh',
  '/internal/routing/fresh-authority',
];

const FIRST_RANDOM_ALPHABET = 'abcdefghijklmnopqrstuvwxyz';
const RANDOM_ALPHABET = 'abcdefghijklmnopqrstuvwxyz0123456789';
const RANDOM_PREFIX_LENGTH = 16;

export type AccessPrefixCode =
  | 'invalid_access_prefix'
  | 'inference_prefix_required'
  | 'external_inference_prefix_forbidden'
  | 'shared_prefix_mismatch'
  | 'shared_prefix_confirmation_required'
  | 'prefix_overlap_forbidden'
  | 'reserved_prefix_collision'
  | 'routing_conflict'
  | 'routing_verification_failed'
  | 'upgrade_access_prefix_required'
  | 'upgrade_prefixes_already_committed'
  | 'inference_prefix_already_committed';

export function isCanonicalPrefix(value: unknown): value is string {
  if (typeof value !== 'string') return false;
  if (!value || value === '/' || value.length > 128) return false;
  if (!/^\/[A-Za-z0-9._~$&+,/:;=@-]+$/.test(value)) return false;
  if (value.endsWith('/') || value.includes('//') || value.includes('%')) return false;
  if (value.includes('?') || value.includes('#') || value.includes('\\')) return false;
  const segments = value.slice(1).split('/');
  return segments.every((segment) => segment.length > 0 && segment !== '.' && segment !== '..');
}

export function isSameOrNestedPrefix(left: string, right: string): boolean {
  return left === right || left.startsWith(`${right}/`) || right.startsWith(`${left}/`);
}

export function isNestedPrefix(left: string, right: string): boolean {
  return left !== right && (left.startsWith(`${right}/`) || right.startsWith(`${left}/`));
}

export function isReservedAccessPrefix(value: string): boolean {
  for (const reserved of RESERVED_ACCESS_PATHS) {
    if (isSameOrNestedPrefix(value, reserved)) return true;
  }
  return false;
}

export function validAccessPrefix(value: unknown): value is string {
  return isCanonicalPrefix(value) && !isReservedAccessPrefix(value);
}

function randomAlphabetChar(cryptoObj: Crypto, alphabet: string): string {
  const limit = 256 - (256 % alphabet.length);
  const buf = new Uint8Array(1);
  while (true) {
    cryptoObj.getRandomValues(buf);
    if (buf[0] < limit) return alphabet[buf[0] % alphabet.length];
  }
}

export function generateRandomAccessPrefix(exclude: readonly string[] = []): string {
  const cryptoObj = globalThis.crypto;
  if (!cryptoObj || typeof cryptoObj.getRandomValues !== 'function') {
    throw new Error('Secure random generator unavailable');
  }
  const blocked = exclude.filter((entry) => typeof entry === 'string' && entry.length > 0);
  for (let attempt = 0; attempt < 32; attempt += 1) {
    let body = randomAlphabetChar(cryptoObj, FIRST_RANDOM_ALPHABET);
    for (let i = 1; i < RANDOM_PREFIX_LENGTH; i += 1) {
      body += randomAlphabetChar(cryptoObj, RANDOM_ALPHABET);
    }
    const candidate = `/${body}`;
    if (!validAccessPrefix(candidate)) continue;
    if (blocked.some((other) => isSameOrNestedPrefix(candidate, other))) continue;
    return candidate;
  }
  throw new Error('Unable to generate distinct random access prefix');
}

export function validatePrefixSingle(
  value: string
): 'invalid_access_prefix' | 'reserved_prefix_collision' | null {
  if (!isCanonicalPrefix(value)) return 'invalid_access_prefix';
  if (isReservedAccessPrefix(value)) {
    return 'reserved_prefix_collision';
  }
  return null;
}

export interface PrefixPairValidationInput {
  controlBasePath: string;
  inferenceBasePath: string;
  sharedEntry: boolean;
  requireInference: boolean;
}

export interface PrefixPairValidationResult {
  controlError: AccessPrefixCode | null;
  inferenceError: AccessPrefixCode | null;
  pairError: AccessPrefixCode | null;
  valid: boolean;
}

export function validatePrefixPair({
  controlBasePath,
  inferenceBasePath,
  sharedEntry,
  requireInference,
}: PrefixPairValidationInput): PrefixPairValidationResult {
  const controlError = validatePrefixSingle(controlBasePath);

  if (!requireInference) {
    const pairError =
      sharedEntry || inferenceBasePath !== '' ? 'external_inference_prefix_forbidden' : null;
    return {
      controlError,
      inferenceError: null,
      pairError,
      valid: !controlError && !pairError,
    };
  }

  if (sharedEntry) {
    const effectiveInference = inferenceBasePath || controlBasePath;
    const pairError =
      !controlError && effectiveInference !== controlBasePath ? 'shared_prefix_mismatch' : null;
    return {
      controlError,
      inferenceError: null,
      pairError,
      valid: !controlError && !pairError,
    };
  }

  if (!inferenceBasePath) {
    return {
      controlError,
      inferenceError: 'inference_prefix_required',
      pairError: null,
      valid: false,
    };
  }

  const inferenceError = validatePrefixSingle(inferenceBasePath);
  let pairError: AccessPrefixCode | null = null;
  if (!controlError && !inferenceError) {
    if (controlBasePath === inferenceBasePath) {
      pairError = 'shared_prefix_confirmation_required';
    } else if (isNestedPrefix(controlBasePath, inferenceBasePath)) {
      pairError = 'prefix_overlap_forbidden';
    }
  }

  return {
    controlError,
    inferenceError,
    pairError,
    valid: !controlError && !inferenceError && !pairError,
  };
}

export function resolveBrowserOrigin(): string {
  if (
    typeof window !== 'undefined' &&
    window.location &&
    typeof window.location.origin === 'string' &&
    window.location.origin !== 'null'
  ) {
    return window.location.origin.replace(/\/+$/, '');
  }
  return '';
}

export interface PrefixPreviewUrls {
  consoleUrl: string;
  managementApiUrl: string;
  inferenceUrl: string;
  externalUrl: string;
}

export function buildPrefixPreviewUrls(
  origin: string,
  controlBasePath: string,
  inferenceBasePath?: string,
  externalBaseUrl?: string
): PrefixPreviewUrls {
  const cleanOrigin = origin.replace(/\/+$/, '');
  const cleanControl = isCanonicalPrefix(controlBasePath) ? controlBasePath : '';
  const cleanInference = inferenceBasePath && isCanonicalPrefix(inferenceBasePath)
    ? inferenceBasePath
    : '';
  const cleanExternal = externalBaseUrl ? externalBaseUrl.trim().replace(/\/+$/, '') : '';
  return {
    consoleUrl: cleanControl ? `${cleanOrigin}${cleanControl}/` : '—',
    managementApiUrl: cleanControl ? `${cleanOrigin}${cleanControl}/api/v2` : '—',
    inferenceUrl: cleanInference ? `${cleanOrigin}${cleanInference}` : '—',
    externalUrl: cleanExternal || '—',
  };
}
