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

export class CPAUpdateStorageError extends Error {
  constructor(readonly kind: 'unavailable' | 'corrupt' | 'changed' | 'secure_random_unavailable') {
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

export function readCPAUpdateIntent(key: string): CPAUpdateIntent | null {
  try {
    const raw = storage().getItem(key);
    if (raw === null) return null;
    if (raw.length > 4096) throw new CPAUpdateStorageError('corrupt');
    let value: unknown;
    try {
      value = JSON.parse(raw);
    } catch {
      throw new CPAUpdateStorageError('corrupt');
    }
    return parseIntent(value);
  } catch (error) {
    if (error instanceof CPAUpdateStorageError) throw error;
    throw new CPAUpdateStorageError('unavailable');
  }
}

export function sameCPAUpdateIntent(
  left: CPAUpdateIntent | null,
  right: CPAUpdateIntent | null
): boolean {
  return (
    left === right || (!!left && !!right && fields.every((field) => left[field] === right[field]))
  );
}

export function persistCPAUpdateIntent(
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

// Omit expected only for the user's explicitly confirmed Forget action.
// Terminal cleanup must still match the record whose result was observed.
export function clearCPAUpdateIntent(key: string, expected?: CPAUpdateIntent): void {
  if (expected && !sameCPAUpdateIntent(readCPAUpdateIntent(key), expected)) {
    throw new CPAUpdateStorageError('changed');
  }
  try {
    storage().removeItem(key);
    if (storage().getItem(key) !== null) throw new CPAUpdateStorageError('unavailable');
  } catch (error) {
    if (error instanceof CPAUpdateStorageError) throw error;
    throw new CPAUpdateStorageError('unavailable');
  }
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
