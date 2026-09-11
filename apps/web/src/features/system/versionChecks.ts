import type { ManagerLatestRelease } from '@/services/api/version';

type VersionPayload = Record<string, unknown> | undefined | null;

export const readManagerLatestTag = (data: ManagerLatestRelease | VersionPayload): string => {
  if (!data) return '';
  const raw = data.tag_name ?? data.name ?? data.latest_version ?? data.latest;
  return typeof raw === 'string' ? raw : raw == null ? '' : String(raw);
};

export const readApiLatestVersion = (data: VersionPayload): string => {
  if (!data) return '';
  const raw = data['latest-version'] ?? data.latest_version ?? data.latest;
  return typeof raw === 'string' ? raw : raw == null ? '' : String(raw);
};

export const readManagerStableVersion = (data: unknown): string | null => {
  if (typeof data !== 'object' || data === null || Array.isArray(data)) {
    throw new Error('Invalid update index: payload must be an object');
  }

  const record = data as Record<string, unknown>;
  if (record.schema_version !== 1) {
    throw new Error('Invalid update index: schema_version must be 1');
  }

  if (
    typeof record.channels !== 'object' ||
    record.channels === null ||
    Array.isArray(record.channels)
  ) {
    throw new Error('Invalid update index: channels must be an object');
  }

  const channels = record.channels as Record<string, unknown>;
  if (!('stable' in channels)) {
    throw new Error('Invalid update index: stable channel is missing');
  }

  const stable = channels.stable;
  if (stable === null) {
    return null;
  }

  if (typeof stable === 'object' && !Array.isArray(stable)) {
    const stableRecord = stable as Record<string, unknown>;
    if (typeof stableRecord.version === 'string' && stableRecord.version.trim().length > 0) {
      return stableRecord.version.trim();
    }
    throw new Error('Invalid update index: stable version must be a non-empty string');
  }

  throw new Error('Invalid update index: stable channel must be an object or null');
};
