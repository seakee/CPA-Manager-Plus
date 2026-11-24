/**
 * Vertex credential import API
 */

import { apiClient, createScopedApiRequestConfig, type ApiClientRequestScope } from './client';

export interface VertexImportResponse {
  status: 'ok';
  project_id?: string;
  email?: string;
  location?: string;
  'auth-file'?: string;
  auth_file?: string;
}

export const vertexApi = {
  importCredential: (
    file: File,
    location?: string,
    requestScope?: ApiClientRequestScope,
    signal?: AbortSignal
  ) => {
    const formData = new FormData();
    formData.append('file', file);
    if (location) {
      formData.append('location', location);
    }
    const config =
      requestScope || signal
        ? {
            ...(requestScope ? createScopedApiRequestConfig(requestScope) : {}),
            ...(signal ? { signal } : {}),
          }
        : undefined;
    return apiClient.postForm<VertexImportResponse>('/vertex/import', formData, config);
  },
};
