import { apiClient } from './client';

export type NativeKeyGrant = {
  provider: string;
  model: string;
};

export type NativeKeyPolicy = {
  key_hash: string;
  enabled: boolean;
  grants: NativeKeyGrant[];
  rpm?: number;
  daily_calls?: number;
  weekly_calls?: number;
  daily_tokens?: number;
  weekly_tokens?: number;
};

type PolicyListResponse = {
  policies?: NativeKeyPolicy[];
};

export const nativeKeyPolicyApi = {
  async isNativeAccessAvailable(): Promise<boolean> {
    const response = await apiClient.get<{ mode?: string }>(
      '/plugins/cpa-key-policy/status'
    );
    return response?.mode === 'native-access';
  },

  async list(): Promise<NativeKeyPolicy[]> {
    const response = await apiClient.get<PolicyListResponse>(
      '/plugins/cpa-key-policy/policies'
    );
    return Array.isArray(response?.policies) ? response.policies : [];
  },

  save(policy: NativeKeyPolicy): Promise<{ policy: NativeKeyPolicy }> {
    return apiClient.put('/plugins/cpa-key-policy/policies', policy);
  },

  applyBulk(
    policies: NativeKeyPolicy[],
    options: { mode?: 'merge' | 'replace'; dryRun?: boolean } = {}
  ): Promise<{ policies: NativeKeyPolicy[]; mode: string; dry_run: boolean }> {
    return apiClient.put('/plugins/cpa-key-policy/policies/bulk', {
      policies,
      mode: options.mode ?? 'merge',
      dry_run: options.dryRun ?? false
    });
  }
};
