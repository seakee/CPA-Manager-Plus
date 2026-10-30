import { apiClient, createScopedApiRequestConfig, type ApiClientRequestScope } from './client';

export interface CredentialLabel {
  keyHash: string;
  label: string;
  updatedAtMs: number;
}

const path = '/credential-labels';
export const credentialLabelsApi = {
  list: (scope: ApiClientRequestScope) =>
    apiClient.get<{ items: CredentialLabel[] }>(path, createScopedApiRequestConfig(scope)),
  set: (scope: ApiClientRequestScope, keyHash: string, label: string) =>
    apiClient.put<CredentialLabel>(
      `${path}/${encodeURIComponent(keyHash)}`,
      { label },
      createScopedApiRequestConfig(scope)
    ),
};
