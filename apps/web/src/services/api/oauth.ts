/**
 * OAuth 与设备码登录相关 API
 */

import { apiClient, createScopedApiRequestConfig, type ApiClientRequestScope } from './client';

export type BuiltInOAuthProvider =
  | 'codex'
  | 'anthropic'
  | 'antigravity'
  | 'kimi'
  | 'xai'
  | 'devin';
export type OAuthProvider = BuiltInOAuthProvider | (string & {});

export interface OAuthStartResponse {
  url: string;
  state?: string;
}

export interface OAuthCallbackResponse {
  status: 'ok';
}

export interface OAuthCancelResponse {
  status: 'ok';
  cancelled: boolean;
}

const WEBUI_SUPPORTED: string[] = ['codex', 'anthropic', 'antigravity', 'xai', 'devin'];

export const oauthApi = {
  startAuth: (provider: OAuthProvider, requestScope?: ApiClientRequestScope) => {
    const params: Record<string, string | boolean> = {};
    if (WEBUI_SUPPORTED.includes(provider)) {
      params.is_webui = true;
    }
    return apiClient.get<OAuthStartResponse>(`/${provider}-auth-url`, {
      ...(requestScope ? createScopedApiRequestConfig(requestScope) : {}),
      params: Object.keys(params).length ? params : undefined,
    });
  },

  getAuthStatus: (state: string, requestScope?: ApiClientRequestScope) =>
    apiClient.get<{ status: 'ok' | 'wait' | 'error'; error?: string }>(`/get-auth-status`, {
      ...(requestScope ? createScopedApiRequestConfig(requestScope) : {}),
      params: { state },
    }),

  submitCallback: (
    provider: OAuthProvider,
    redirectUrl: string,
    requestScope?: ApiClientRequestScope
  ) => {
    return apiClient.post<OAuthCallbackResponse>(
      '/oauth-callback',
      {
        provider,
        redirect_url: redirectUrl,
      },
      requestScope ? createScopedApiRequestConfig(requestScope) : undefined
    );
  },

  cancelSession: (state: string, requestScope?: ApiClientRequestScope) =>
    apiClient.delete<OAuthCancelResponse>('/oauth-session', {
      ...(requestScope ? createScopedApiRequestConfig(requestScope) : {}),
      params: { state },
    }),
};
