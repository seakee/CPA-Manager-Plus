import axios from 'axios';
import { normalizeUsageServiceBase } from '@/services/api/usageService';

export type UpdateChannel = 'auto' | 'stable' | 'rc' | 'beta';
export interface ReleaseInfo {
  schema_version: 1;
  release: { version: string; stage: string; source_commit: string };
  content: { summary: Record<string, string>; notes: Record<string, string> };
  update: {
    breaking: boolean;
    migration_required: boolean;
    minimum_direct_upgrade_version: string | null;
    upgrade_guide_url: string;
  };
  distribution: { docker: { image: string; version_tag: string }; native: { assets: string[] } };
  compatibility: { minimum_cpa_version: string | null };
}
export interface UpdateStatus {
  current_version: string;
  source_commit: string;
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
  const response = await axios.request<T>({
    url: `${normalizeUsageServiceBase(base).replace(/\/+$/, '')}/usage-service/updates${suffix}`,
    method,
    data,
    timeout: 30_000,
    headers: { Authorization: `Bearer ${key}` },
  });
  return response.data;
}
