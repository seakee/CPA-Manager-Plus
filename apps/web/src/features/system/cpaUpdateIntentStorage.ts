import { normalizeUsageServiceBase } from '@/services/api/usageService';
import { sha256RawTextHex } from '@/utils/apiKeyHash';
import { isCPAUpdateRequest, type CPAUpdatePhase, type CPAUpdateRequest } from './cpaUpdateApi';

export interface CPAUpdateIntent extends CPAUpdateRequest {
  schema_version: 1;
  phase: CPAUpdatePhase;
  client_stage: 'submitted' | 'prepared';
  created_at_ms: number;
  updated_at_ms: number;
}

// Kept only in memory to bind Forget to the exact record the user saw,
// including records which cannot be decoded as an intent.
export interface CPAUpdateRecordSnapshot {
  readonly raw: string | null;
}

export class CPAUpdateStorageError extends Error {
  constructor(
    readonly kind:
      | 'unavailable'
      | 'corrupt'
      | 'changed'
      | 'secure_random_unavailable'
      | 'coordination_unavailable'
  ) {
    super(kind);
  }
}

const fields: (keyof CPAUpdateIntent)[] = [
  'schema_version',
  'request_id',
  'target_version',
  'expected_active_artifact_id',
  'phase',
  'client_stage',
  'created_at_ms',
  'updated_at_ms',
];

export function cpaUpdateStorageKey(base: string, managementKey: string): string {
  return (
    'cpamp:cpa-update-intent:v1:' +
    sha256RawTextHex(JSON.stringify([normalizeUsageServiceBase(base), managementKey]))
  );
}

function parseIntent(value: unknown): CPAUpdateIntent {
  if (
    !isCPAUpdateRequest(value) ||
    Object.keys(value).length !== fields.length ||
    !fields.every((field) => Object.prototype.hasOwnProperty.call(value, field))
  ) {
    throw new CPAUpdateStorageError('corrupt');
  }
  const intent = value as CPAUpdateIntent;
  if (
    intent.schema_version !== 1 ||
    !['prepare', 'activate'].includes(intent.phase) ||
    !['submitted', 'prepared'].includes(intent.client_stage) ||
    (intent.client_stage === 'prepared' && intent.phase !== 'prepare') ||
    !Number.isSafeInteger(intent.created_at_ms) ||
    intent.created_at_ms <= 0 ||
    !Number.isSafeInteger(intent.updated_at_ms) ||
    intent.updated_at_ms < intent.created_at_ms
  ) {
    throw new CPAUpdateStorageError('corrupt');
  }
  return intent;
}

function storage(): Storage {
  try {
    return localStorage;
  } catch {
    throw new CPAUpdateStorageError('unavailable');
  }
}

export function readCPAUpdateRecord(key: string): CPAUpdateRecordSnapshot {
  try {
    return { raw: storage().getItem(key) };
  } catch (error) {
    if (error instanceof CPAUpdateStorageError) throw error;
    throw new CPAUpdateStorageError('unavailable');
  }
}

export function parseCPAUpdateRecord({ raw }: CPAUpdateRecordSnapshot): CPAUpdateIntent | null {
  if (raw === null) return null;
  if (raw.length > 4096) throw new CPAUpdateStorageError('corrupt');
  let value: unknown;
  try {
    value = JSON.parse(raw);
  } catch {
    throw new CPAUpdateStorageError('corrupt');
  }
  return parseIntent(value);
}

export function readCPAUpdateIntent(key: string): CPAUpdateIntent | null {
  return parseCPAUpdateRecord(readCPAUpdateRecord(key));
}

export async function withCPAUpdateIntentLock<T>(
  key: string,
  work: () => T | Promise<T>,
  signal?: AbortSignal
): Promise<T> {
  if (typeof navigator === 'undefined' || !navigator.locks?.request) {
    throw new CPAUpdateStorageError('coordination_unavailable');
  }
  // All cooperating tabs use this same auth/service-scoped Web Lock. Storage
  // read-back verifies durability; it is not a cross-tab compare-and-swap.
  return navigator.locks.request(key, { mode: 'exclusive', signal }, work);
}

export function sameCPAUpdateIntent(
  left: CPAUpdateIntent | null,
  right: CPAUpdateIntent | null
): boolean {
  return (
    left === right || (!!left && !!right && fields.every((field) => left[field] === right[field]))
  );
}

function persistLockedIntent(
  key: string,
  intent: CPAUpdateIntent,
  previous: CPAUpdateIntent | null
): void {
  parseIntent(intent);
  if (
    !sameCPAUpdateIntent(readCPAUpdateIntent(key), previous) ||
    (previous &&
      (previous.request_id !== intent.request_id ||
        previous.target_version !== intent.target_version ||
        previous.expected_active_artifact_id !== intent.expected_active_artifact_id))
  ) {
    throw new CPAUpdateStorageError('changed');
  }
  try {
    const raw = JSON.stringify(intent);
    storage().setItem(key, raw);
    if (storage().getItem(key) !== raw || !sameCPAUpdateIntent(readCPAUpdateIntent(key), intent)) {
      throw new CPAUpdateStorageError('unavailable');
    }
  } catch (error) {
    if (error instanceof CPAUpdateStorageError) throw error;
    throw new CPAUpdateStorageError('unavailable');
  }
}

export function persistCPAUpdateIntent(
  key: string,
  intent: CPAUpdateIntent,
  previous: CPAUpdateIntent | null,
  signal?: AbortSignal
): Promise<void> {
  return withCPAUpdateIntentLock(key, () => persistLockedIntent(key, intent, previous), signal);
}

export function claimCPAUpdateIntent(
  key: string,
  create: () => CPAUpdateIntent,
  signal?: AbortSignal
): Promise<{ intent: CPAUpdateIntent; claimed: boolean }> {
  return withCPAUpdateIntentLock(
    key,
    () => {
      const existing = readCPAUpdateIntent(key);
      if (existing) return { intent: existing, claimed: false };
      // Generate the request ID only after winning the exclusive claim.
      const intent = create();
      persistLockedIntent(key, intent, null);
      return { intent, claimed: true };
    },
    signal
  );
}

export function clearCPAUpdateIntent(
  key: string,
  expected: CPAUpdateIntent | CPAUpdateRecordSnapshot,
  signal?: AbortSignal
): Promise<void> {
  return withCPAUpdateIntentLock(
    key,
    () => {
      const current = readCPAUpdateRecord(key);
      // Another observer may already have cleaned a confirmed terminal result.
      // Forget still requires its exact raw snapshot, even when now absent.
      if (!('raw' in expected) && current.raw === null) return;
      const matches =
        'raw' in expected
          ? current.raw === expected.raw
          : sameCPAUpdateIntent(parseCPAUpdateRecord(current), expected);
      if (!matches) throw new CPAUpdateStorageError('changed');
      try {
        storage().removeItem(key);
        if (storage().getItem(key) !== null) throw new CPAUpdateStorageError('unavailable');
      } catch (error) {
        if (error instanceof CPAUpdateStorageError) throw error;
        throw new CPAUpdateStorageError('unavailable');
      }
    },
    signal
  );
}

export function createCPAUpdateRequestID(): string {
  if (typeof crypto === 'undefined') throw new CPAUpdateStorageError('secure_random_unavailable');
  if (typeof crypto.randomUUID === 'function') return crypto.randomUUID();
  if (typeof crypto.getRandomValues !== 'function') {
    throw new CPAUpdateStorageError('secure_random_unavailable');
  }
  return Array.from(crypto.getRandomValues(new Uint8Array(16)), (value) =>
    value.toString(16).padStart(2, '0')
  ).join('');
}
