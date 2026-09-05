import type { AuthFileItem } from '@/types';
import { getAuthFileStatusMessage } from '@/features/authFiles/constants';
import {
  readAuthFileCredentialRefreshAtMs,
  readAuthFileUpdatedAtMs,
} from '@/features/accounts/model/accountQuotaSummary';
import {
  readAuthFileStatusAccountId,
  readAuthFileStatusAuthIndex,
  readAuthFileStatusPhysicalName,
  readAuthFileStatusProvider,
  readAuthFileStatusRuntimeId,
} from '@/utils/authFileCredentialIdentity';

const STORAGE_KEY = 'cpa.accounts.credential-mutation-markers.v3';
const STORAGE_VERSION = 3;
const MAX_MARKERS = 32;
const MAX_MARKER_AGE_MS = 24 * 60 * 60 * 1000;

export interface AccountCredentialMutationMarker {
  id: string;
  connectionFingerprint: string;
  provider: string;
  createdAtMs: number;
  requireObservedMutation: boolean;
  baseline?: AccountCredentialMutationBaseline;
}

export interface AccountCredentialMutationEvidence {
  identityKey: string;
  credentialRefreshAtMs: number;
  updatedAtMs: number;
  statusMessage: string;
}

export interface AccountCredentialMutationBaseline {
  provider: string;
  credentials: AccountCredentialMutationEvidence[];
}

interface StoredAccountCredentialMutationMarkers {
  version: number;
  markers: AccountCredentialMutationMarker[];
}

let memoryMarkers: AccountCredentialMutationMarker[] = [];
let markerSequence = 0;

const normalizeProvider = (value: string): string => {
  const provider = value.trim().toLowerCase().replace(/_/g, '-');
  if (provider === 'x-ai' || provider === 'grok') return 'xai';
  if (provider === 'anthropic') return 'claude';
  return provider;
};

const readTimestamp = (value: unknown): number =>
  typeof value === 'number' && Number.isFinite(value) && value > 0 ? Math.trunc(value) : 0;

const buildCredentialIdentityKey = (file: AuthFileItem): string =>
  JSON.stringify([
    readAuthFileStatusProvider(file),
    readAuthFileStatusAccountId(file),
    readAuthFileStatusPhysicalName(file),
    readAuthFileStatusRuntimeId(file),
    readAuthFileStatusAuthIndex(file) ?? '',
  ]);

const buildCredentialEvidence = (file: AuthFileItem): AccountCredentialMutationEvidence => ({
  identityKey: buildCredentialIdentityKey(file),
  credentialRefreshAtMs: readAuthFileCredentialRefreshAtMs(file) ?? 0,
  updatedAtMs: readAuthFileUpdatedAtMs(file) ?? 0,
  statusMessage: getAuthFileStatusMessage(file),
});

const normalizeEvidence = (value: unknown): AccountCredentialMutationEvidence | null => {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
  const record = value as Record<string, unknown>;
  const identityKey = typeof record.identityKey === 'string' ? record.identityKey.trim() : '';
  if (!identityKey) return null;
  return {
    identityKey,
    credentialRefreshAtMs: readTimestamp(record.credentialRefreshAtMs),
    updatedAtMs: readTimestamp(record.updatedAtMs),
    statusMessage: typeof record.statusMessage === 'string' ? record.statusMessage.trim() : '',
  };
};

const normalizeBaseline = (value: unknown): AccountCredentialMutationBaseline | null => {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
  const record = value as Record<string, unknown>;
  const provider = typeof record.provider === 'string' ? normalizeProvider(record.provider) : '';
  if (!provider || !Array.isArray(record.credentials)) return null;
  return {
    provider,
    credentials: record.credentials
      .map(normalizeEvidence)
      .filter((item): item is AccountCredentialMutationEvidence => item !== null),
  };
};

const normalizeMarker = (value: unknown): AccountCredentialMutationMarker | null => {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
  const record = value as Record<string, unknown>;
  const id = typeof record.id === 'string' ? record.id.trim() : '';
  const connectionFingerprint =
    typeof record.connectionFingerprint === 'string' ? record.connectionFingerprint.trim() : '';
  const provider = typeof record.provider === 'string' ? normalizeProvider(record.provider) : '';
  const createdAtMs = readTimestamp(record.createdAtMs);
  const requireObservedMutation = record.requireObservedMutation === true;
  const baseline = normalizeBaseline(record.baseline);
  if (!id || !connectionFingerprint || !provider || createdAtMs <= 0) return null;
  if (requireObservedMutation && (!baseline || baseline.provider !== provider)) return null;
  return {
    id,
    connectionFingerprint,
    provider,
    createdAtMs,
    requireObservedMutation,
    ...(baseline ? { baseline } : {}),
  };
};

const getStorage = (): Storage | null => {
  try {
    return typeof window !== 'undefined' && window.sessionStorage ? window.sessionStorage : null;
  } catch {
    return null;
  }
};

const pruneMarkers = (
  markers: readonly AccountCredentialMutationMarker[],
  nowMs = Date.now()
): AccountCredentialMutationMarker[] => {
  const oldestAllowedAtMs = nowMs - MAX_MARKER_AGE_MS;
  const unique = new Map<string, AccountCredentialMutationMarker>();
  markers.forEach((marker) => {
    if (marker.createdAtMs < oldestAllowedAtMs) return;
    unique.set(marker.id, marker);
  });
  return Array.from(unique.values())
    .sort((left, right) => left.createdAtMs - right.createdAtMs || left.id.localeCompare(right.id))
    .slice(-MAX_MARKERS);
};

const readStoredMarkers = (): AccountCredentialMutationMarker[] => {
  const storage = getStorage();
  if (!storage) return [];
  try {
    const raw = storage.getItem(STORAGE_KEY);
    if (!raw) return [];
    const parsed = JSON.parse(raw) as Partial<StoredAccountCredentialMutationMarkers>;
    if (parsed.version !== STORAGE_VERSION || !Array.isArray(parsed.markers)) return [];
    return pruneMarkers(
      parsed.markers
        .map(normalizeMarker)
        .filter((marker): marker is AccountCredentialMutationMarker => marker !== null)
    );
  } catch {
    return [];
  }
};

const loadMarkers = (): AccountCredentialMutationMarker[] =>
  pruneMarkers([...readStoredMarkers(), ...memoryMarkers]);

const saveMarkers = (markers: readonly AccountCredentialMutationMarker[]): void => {
  const next = pruneMarkers(markers);
  memoryMarkers = next;
  const storage = getStorage();
  if (!storage) return;
  try {
    storage.setItem(STORAGE_KEY, JSON.stringify({ version: STORAGE_VERSION, markers: next }));
  } catch {
    // Mutation markers are an in-session synchronization aid; storage failures remain non-fatal.
  }
};

export const recordAccountCredentialMutationMarker = ({
  connectionFingerprint,
  provider,
  createdAtMs = Date.now(),
  baseline,
  requireObservedMutation = false,
}: {
  connectionFingerprint: string;
  provider: string;
  createdAtMs?: number;
  baseline?: AccountCredentialMutationBaseline | null;
  requireObservedMutation?: boolean;
}): AccountCredentialMutationMarker | null => {
  const normalizedConnectionFingerprint = connectionFingerprint.trim();
  const normalizedProvider = normalizeProvider(provider);
  const normalizedCreatedAtMs = readTimestamp(createdAtMs);
  if (!normalizedConnectionFingerprint || !normalizedProvider || normalizedCreatedAtMs <= 0) {
    return null;
  }
  const normalizedBaseline = normalizeBaseline(baseline);
  if (
    requireObservedMutation &&
    (!normalizedBaseline || normalizedBaseline.provider !== normalizedProvider)
  ) {
    return null;
  }
  markerSequence += 1;
  const marker: AccountCredentialMutationMarker = {
    id: `${normalizedCreatedAtMs.toString(36)}-${markerSequence.toString(36)}`,
    connectionFingerprint: normalizedConnectionFingerprint,
    provider: normalizedProvider,
    createdAtMs: normalizedCreatedAtMs,
    requireObservedMutation,
    ...(normalizedBaseline ? { baseline: normalizedBaseline } : {}),
  };
  saveMarkers([...loadMarkers(), marker]);
  return marker;
};

export const createAccountCredentialMutationBaseline = (
  files: readonly AuthFileItem[],
  provider: string
): AccountCredentialMutationBaseline | null => {
  const normalizedProvider = normalizeProvider(provider);
  if (!normalizedProvider) return null;
  return {
    provider: normalizedProvider,
    credentials: files
      .filter((file) => normalizeProvider(readAuthFileStatusProvider(file)) === normalizedProvider)
      .map(buildCredentialEvidence),
  };
};

export const resolveAccountCredentialMutationFiles = (
  marker: AccountCredentialMutationMarker,
  files: readonly AuthFileItem[]
): AuthFileItem[] => {
  if (!marker.requireObservedMutation) return [];
  if (!marker.baseline || marker.baseline.provider !== marker.provider) return [];
  const baselineIdentityKeys = new Set(
    marker.baseline.credentials.map((credential) => credential.identityKey)
  );
  return files
    .filter((file) => normalizeProvider(readAuthFileStatusProvider(file)) === marker.provider)
    .filter((file) => !baselineIdentityKeys.has(buildCredentialIdentityKey(file)));
};

export const hasAccountCredentialMutationEvidence = (
  marker: AccountCredentialMutationMarker,
  files: readonly AuthFileItem[]
): boolean => {
  if (!marker.requireObservedMutation) return true;
  // A provider-wide timestamp/status change is not attributable to the OAuth
  // operation that created this marker. Only a newly observed credential
  // identity is causal evidence without a target key.
  return resolveAccountCredentialMutationFiles(marker, files).length > 0;
};

export const listAccountCredentialMutationMarkers = (
  connectionFingerprint: string
): AccountCredentialMutationMarker[] => {
  const normalizedConnectionFingerprint = connectionFingerprint.trim();
  if (!normalizedConnectionFingerprint) return [];
  return loadMarkers().filter(
    (marker) => marker.connectionFingerprint === normalizedConnectionFingerprint
  );
};

export const acknowledgeAccountCredentialMutationMarkers = (ids: readonly string[]): void => {
  const acknowledgedIds = new Set(ids.map((id) => id.trim()).filter(Boolean));
  if (acknowledgedIds.size === 0) return;
  saveMarkers(loadMarkers().filter((marker) => !acknowledgedIds.has(marker.id)));
};

export const clearAccountCredentialMutationMarkersForTests = (): void => {
  memoryMarkers = [];
  markerSequence = 0;
  try {
    getStorage()?.removeItem(STORAGE_KEY);
  } catch {
    // Ignore unavailable or blocked session storage.
  }
};
