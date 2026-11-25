import axios from 'axios';
import { normalizeUsageServiceBase } from '@/services/api/usageService';

export type UpdateChannel = 'auto' | 'stable' | 'rc' | 'beta';
export type DeploymentVariant = 'docker-full' | 'docker-slim' | 'native' | 'unknown';
export type DockerImageRole = 'full' | 'slim';

export interface DockerImageSpec {
  role: DockerImageRole;
  name: string;
  tag: string;
  digest: string;
  platforms: string[];
}

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
}
export async function managerUpdateRequest<T>(
  base: string,
  key: string,
  suffix = '',
  method = 'GET',
  data?: unknown
): Promise<T> {
  if (__PANEL_LITE__) throw new Error('Manager API is unavailable in Panel Lite');
  const response = await axios.request<T>({
    url: `${normalizeUsageServiceBase(base).replace(/\/+$/, '')}/usage-service/updates${suffix}`,
    method,
    data,
    timeout: 30_000,
    headers: { Authorization: `Bearer ${key}` },
  });
  return response.data;
}
