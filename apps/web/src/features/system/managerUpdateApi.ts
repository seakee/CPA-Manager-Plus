import axios from 'axios';
import { normalizeUsageServiceBase } from '@/services/api/usageService';
import { cpaUpdateApi, type CPABaselineAdoptResult } from './cpaUpdateApi';

export type UpdateChannel = 'auto' | 'stable' | 'rc' | 'beta';
export type DeploymentVariant = 'docker-full' | 'docker-slim' | 'native' | 'unknown';
export type DockerImageRole = 'full' | 'slim';

export const MANAGER_SLOT_MAX_UPLOAD_BYTES = 192 * 1024 * 1024;
export const DEFAULT_SLOT_CDN_PRESETS = ['https://ghfast.top/', 'https://gh-proxy.com/'] as const;

export interface DockerImageSpec {
  role: DockerImageRole;
  name: string;
  tag: string;
  digest: string;
  platforms: string[];
}

export interface SlotAsset {
  name: string;
  platform: 'linux/amd64' | 'linux/arm64' | string;
  sha256: string;
  size: number;
  min_base_epoch: number;
  schema_generation: number;
}

export interface SlotDistribution {
  assets: SlotAsset[];
}

export interface SlotPrepareOperation {
  id: string;
  target_version: string;
  phase: 'downloading' | 'verifying' | 'staging' | 'succeeded' | 'failed';
  received_bytes: number;
  total_bytes: number;
  error_code: string;
}

export interface SelfUpdateStatus {
  supported: boolean;
  state: 'none' | 'trial' | 'committed' | 'rolled_back' | 'recovery_required' | string;
  active_slot: string;
  base_version: string;
  base_epoch?: number;
  running_version?: string;
  rollback_available: boolean;
  rollback_target_version: string;
  rollback_same_schema: boolean;
  rollback_blocked_reason: string;
  staged_slots: string[];
  prepare_operation: SlotPrepareOperation | null;
  last_failure_code: string;
  last_rolled_back_slot: string;
  active_attempt_id?: string;
  last_attempt_id?: string;
  cdn_presets: string[];
}

export interface SlotActionAccepted {
  accepted: boolean;
  target_version: string;
  trial_timeout_seconds?: number;
  attempt_id?: string;
}

export interface SlotActivateAccepted extends SlotActionAccepted {
  attempt_id: string;
}

export type SlotRollbackAccepted = SlotActionAccepted;

export interface ReleaseInfo {
  schema_version: 2;
  release: { version: string; stage: string; source_commit: string };
  content: { summary: Record<string, string>; notes: Record<string, string> };
  update: {
    breaking: boolean;
    migration_required: boolean;
    minimum_direct_upgrade_version: string | null;
    upgrade_guide_url: string;
  };
  distribution: {
    docker: {
      version_tag: string;
      images: DockerImageSpec[];
    };
    native: { assets: string[] };
    compose?: { filename: string; sha256: string };
    slot?: SlotDistribution;
  };
  compatibility: { minimum_cpa_version: string | null };
  supported_modes?: string[];
  supported_platforms?: Record<string, string[]>;
  pairing?: {
    filename: string;
    source_commit: string;
    fullweb_manifest_sha256: string;
    api_contract_sha256: string;
    compose_sha256: string;
  };
}

export interface UpdateStatus {
  current_version: string;
  source_commit: string;
  deployment_variant?: DeploymentVariant;
  base_image_version?: string;
  active_slot?: string;
  channel_preference: UpdateChannel;
  channel: string;
  automatic: boolean;
  state:
    | 'unknown_version'
    | 'never_checked'
    | 'up_to_date'
    | 'update_available'
    | 'ahead_of_channel'
    | 'no_candidate';
  stale: boolean;
  last_error?: string;
  last_success_at: string;
  target?: ReleaseInfo;
  upgrade_action?: 'direct' | 'migration_guide';
  self_update?: SelfUpdateStatus;
}

export class ManagerSelfUpdateError extends Error {
  constructor(
    readonly code: string,
    readonly messageText: string,
    readonly httpStatus?: number
  ) {
    super(messageText || code);
  }
}

function parseSelfUpdateError(error: unknown): ManagerSelfUpdateError {
  if (error instanceof ManagerSelfUpdateError) return error;
  if (axios.isAxiosError(error)) {
    const status = error.response?.status;
    const data = error.response?.data as { code?: unknown; message?: unknown } | undefined;
    const code = typeof data?.code === 'string' && data.code.trim() ? data.code.trim() : '';
    const message =
      typeof data?.message === 'string' && data.message.trim() ? data.message.trim() : '';
    if (code) {
      return new ManagerSelfUpdateError(code, message || code, status);
    }
    if (status === 413) {
      return new ManagerSelfUpdateError(
        'archive_too_large',
        'Slot archive exceeds 192 MiB upload limit',
        413
      );
    }
    if (status === 502 || status === 503 || status === 504) {
      return new ManagerSelfUpdateError('launcher_unavailable', error.message, status);
    }
    return new ManagerSelfUpdateError('request_failed', error.message, status);
  }
  return new ManagerSelfUpdateError(
    'request_failed',
    error instanceof Error ? error.message : 'Unknown error'
  );
}

export async function managerUpdateRequest<T>(
  base: string,
  key: string,
  suffix = '',
  method = 'GET',
  data?: unknown,
  signal?: AbortSignal
): Promise<T> {
  if (__PANEL_LITE__) throw new Error('Manager API is unavailable in Panel Lite');
  const response = await axios.request<T>({
    url: `${normalizeUsageServiceBase(base).replace(/\/+$/, '')}/usage-service/updates${suffix}`,
    method,
    data,
    timeout: 30_000,
    signal,
    headers: { Authorization: `Bearer ${key}` },
  });
  return response.data;
}

export async function fetchManagerUpdateStatus(
  base: string,
  key: string,
  signal?: AbortSignal
): Promise<UpdateStatus> {
  return managerUpdateRequest<UpdateStatus>(base, key, '', 'GET', undefined, signal);
}

export async function prepareManagerSlot(
  base: string,
  key: string,
  input: { version: string; source: 'github' | 'cdn'; cdn_prefix?: string },
  signal?: AbortSignal
): Promise<SelfUpdateStatus> {
  if (__PANEL_LITE__) throw new Error('Manager API is unavailable in Panel Lite');
  try {
    const response = await axios.request<SelfUpdateStatus>({
      url: `${normalizeUsageServiceBase(base).replace(/\/+$/, '')}/usage-service/updates/slot/prepare`,
      method: 'POST',
      data: input,
      timeout: 900_000,
      signal,
      headers: { Authorization: `Bearer ${key}` },
    });
    return response.data;
  } catch (error) {
    throw parseSelfUpdateError(error);
  }
}

export async function uploadManagerSlot(
  base: string,
  key: string,
  version: string,
  file: Blob,
  onProgress?: (loaded: number, total: number) => void,
  signal?: AbortSignal
): Promise<SelfUpdateStatus> {
  if (__PANEL_LITE__) throw new Error('Manager API is unavailable in Panel Lite');
  if (!file || file.size <= 0) {
    throw new ManagerSelfUpdateError('invalid_request', 'Slot archive is empty', 400);
  }
  if (file.size > MANAGER_SLOT_MAX_UPLOAD_BYTES) {
    throw new ManagerSelfUpdateError(
      'archive_too_large',
      'Slot archive exceeds 192 MiB upload limit',
      413
    );
  }
  try {
    const response = await axios.request<SelfUpdateStatus>({
      url: `${normalizeUsageServiceBase(base).replace(/\/+$/, '')}/usage-service/updates/slot/prepare-upload`,
      method: 'PUT',
      params: { version },
      data: file,
      timeout: 900_000,
      signal,
      headers: {
        Authorization: `Bearer ${key}`,
        'Content-Type': 'application/octet-stream',
      },
      onUploadProgress: onProgress
        ? (event) => {
            onProgress(event.loaded ?? 0, event.total ?? file.size ?? 0);
          }
        : undefined,
    });
    return response.data;
  } catch (error) {
    throw parseSelfUpdateError(error);
  }
}

export async function activateManagerSlot(
  base: string,
  key: string,
  version: string,
  signal?: AbortSignal
): Promise<SlotActivateAccepted> {
  if (__PANEL_LITE__) throw new Error('Manager API is unavailable in Panel Lite');
  try {
    const response = await axios.request<SlotActivateAccepted>({
      url: `${normalizeUsageServiceBase(base).replace(/\/+$/, '')}/usage-service/updates/slot/activate`,
      method: 'POST',
      data: { version },
      timeout: 180_000,
      signal,
      headers: { Authorization: `Bearer ${key}` },
    });
    const data = response.data;
    const attemptId = typeof data?.attempt_id === 'string' ? data.attempt_id.trim() : '';
    if (!attemptId) {
      throw new ManagerSelfUpdateError(
        'invalid_response',
        'Slot activation response is missing attempt_id',
        response.status
      );
    }
    return {
      ...data,
      attempt_id: attemptId,
    };
  } catch (error) {
    throw parseSelfUpdateError(error);
  }
}

export async function rollbackManagerSlot(
  base: string,
  key: string,
  expectedActiveSlot: string,
  signal?: AbortSignal
): Promise<SlotRollbackAccepted> {
  if (__PANEL_LITE__) throw new Error('Manager API is unavailable in Panel Lite');
  try {
    const response = await axios.request<SlotRollbackAccepted>({
      url: `${normalizeUsageServiceBase(base).replace(/\/+$/, '')}/usage-service/updates/slot/rollback`,
      method: 'POST',
      data: { expected_active_slot: expectedActiveSlot },
      timeout: 180_000,
      signal,
      headers: { Authorization: `Bearer ${key}` },
    });
    return response.data;
  } catch (error) {
    throw parseSelfUpdateError(error);
  }
}

export function adoptCPABaseline(
  base: string,
  key: string,
  expectedActiveArtifactId: string,
  signal?: AbortSignal
): Promise<CPABaselineAdoptResult> {
  return cpaUpdateApi.adoptBaseline({ base, key }, expectedActiveArtifactId, signal);
}
