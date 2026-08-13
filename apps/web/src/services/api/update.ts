import { isDemoMode } from '@/features/demo/demoMode';
import { apiClient } from './client';

export type UpdateState =
  | 'downloading'
  | 'verifying'
  | 'staged'
  | 'launching'
  | 'stopping'
  | 'backing_up'
  | 'switching'
  | 'starting'
  | 'validating'
  | 'succeeded'
  | 'rolling_back'
  | 'rolled_back'
  | 'failed'
  | 'manual_recovery_required';

export interface ManagedUpdateCapability {
  supported: boolean;
  reason?: string;
  mode?: string;
  platform: string;
  architecture: string;
  backupSupported: boolean;
  rollbackSupported: boolean;
}

export interface ManagedUpdateCheck {
  currentVersion: string;
  latestVersion: string;
  updateAvailable: boolean;
  comparable: boolean;
  releaseUrl: string;
  publishedAt?: string;
  installable: boolean;
  installReason?: string;
}

export interface ManagedUpdateStatus {
  transactionId: string;
  installId: string;
  currentVersion: string;
  targetVersion: string;
  state: UpdateState;
  message?: string;
  backupPath?: string;
  startedAt: string;
  updatedAt: string;
  finishedAt?: string;
}

export interface ManagedUpdateStatusResponse {
  found: boolean;
  status: ManagedUpdateStatus;
}

const demoCapability = (): ManagedUpdateCapability => ({
  supported: false,
  reason: 'demo_mode',
  platform: 'demo',
  architecture: 'demo',
  backupSupported: false,
  rollbackSupported: false,
});

export const managedUpdateApi = {
  capability: () =>
    __DEMO_SITE__ && isDemoMode()
      ? Promise.resolve(demoCapability())
      : apiClient.get<ManagedUpdateCapability>('/update/capability'),
  check: () => apiClient.get<ManagedUpdateCheck>('/update/check'),
  status: () => apiClient.get<ManagedUpdateStatusResponse>('/update/status'),
  plan: () => apiClient.post<ManagedUpdateStatus>('/update/plan'),
  apply: () => apiClient.post<ManagedUpdateStatus>('/update/apply'),
};
