import {
  apiClient,
  createScopedApiRequestConfig,
  createScopedV8ApiRequestConfig,
  type ApiClientRequestScope,
  type ScopedApiRequestConfig,
} from './client';

export type BuiltInOAuthProvider =
  | 'codex'
  | 'anthropic'
  | 'antigravity'
  | 'kimi'
  | 'kimi-ai'
  | 'xai'
  | 'devin'
  | 'meta';
export type OAuthProvider = BuiltInOAuthProvider | (string & {});

export interface OAuthStartResponse {
  url: string;
  state?: string;
  user_code?: string;
  flow?: string;
  expires_in?: number;
}

export interface OAuthCallbackResponse {
  status: 'ok';
}

export interface OAuthCancelResponse {
  status: 'ok';
  cancelled: boolean;
}

export interface OAuthStatusResponse {
  status: 'ok' | 'wait' | 'error';
  error?: string;
}

export interface SiwcBindStartResponse {
  status?: string;
  url: string;
  state: string;
  flow?: 'siwc_bind';
  expires_in?: number;
}

export interface SiwcBindingApi {
  start: (name: string, signal?: AbortSignal) => Promise<SiwcBindStartResponse>;
  getStatus: (state: string, signal?: AbortSignal) => Promise<OAuthStatusResponse>;
  submitCallback: (
    provider: string,
    redirectUrl: string,
    signal?: AbortSignal
  ) => Promise<OAuthCallbackResponse>;
  cancelSession: (state: string, signal?: AbortSignal) => Promise<OAuthCancelResponse>;
}

const createV8RequestConfig = (
  requestScope?: ApiClientRequestScope,
  signal?: AbortSignal,
  params?: Record<string, string>
): ScopedApiRequestConfig => ({
  ...(requestScope ? createScopedV8ApiRequestConfig(requestScope) : { cpampV8Namespace: true }),
  ...(params ? { params } : {}),
  ...(signal ? { signal } : {}),
});

const WEBUI_SUPPORTED: Record<string, true> = {
  codex: true,
  anthropic: true,
  antigravity: true,
  xai: true,
  devin: true,
};

export const oauthApi = {
  startAuth: (
    provider: OAuthProvider,
    requestScope?: ApiClientRequestScope,
    signal?: AbortSignal
  ) => {
    const params: Record<string, string | boolean> = {};
    if (Object.prototype.hasOwnProperty.call(WEBUI_SUPPORTED, provider)) {
      params.is_webui = true;
    }
    return apiClient.get<OAuthStartResponse>(`/${provider}-auth-url`, {
      ...(requestScope ? createScopedApiRequestConfig(requestScope) : {}),
      params: Object.keys(params).length ? params : undefined,
      ...(signal ? { signal } : {}),
    });
  },

  getAuthStatus: (
    state: string,
    requestScope?: ApiClientRequestScope,
    signal?: AbortSignal
  ) =>
    apiClient.get<OAuthStatusResponse>(`/get-auth-status`, {
      ...(requestScope ? createScopedApiRequestConfig(requestScope) : {}),
      params: { state },
      ...(signal ? { signal } : {}),
    }),

  cancelSession: (
    state: string,
    requestScope?: ApiClientRequestScope,
    signal?: AbortSignal
  ) =>
    apiClient.delete<OAuthCancelResponse>('/oauth-session', {
      ...(requestScope ? createScopedApiRequestConfig(requestScope) : {}),
      params: { state },
      ...(signal ? { signal } : {}),
    }),

  submitCallback: (
    provider: OAuthProvider,
    redirectUrl: string,
    requestScope?: ApiClientRequestScope,
    signal?: AbortSignal
  ) => {
    const config =
      requestScope || signal
        ? {
            ...(requestScope ? createScopedApiRequestConfig(requestScope) : {}),
            ...(signal ? { signal } : {}),
          }
        : undefined;
    return apiClient.post<OAuthCallbackResponse>(
      '/oauth-callback',
      {
        provider,
        redirect_url: redirectUrl,
      },
      config
    );
  },

  startSiwcBind: (
    name: string,
    requestScope?: ApiClientRequestScope,
    signal?: AbortSignal
  ) =>
    apiClient.post<SiwcBindStartResponse>(
      '/oauth/siwc/bind',
      { name },
      createV8RequestConfig(requestScope, signal)
    ),

  getV8OAuthStatus: (
    state: string,
    requestScope?: ApiClientRequestScope,
    signal?: AbortSignal
  ) =>
    apiClient.get<OAuthStatusResponse>(
      '/oauth/status',
      createV8RequestConfig(requestScope, signal, { state })
    ),

  submitV8OAuthCallback: (
    provider: string,
    redirectUrl: string,
    requestScope?: ApiClientRequestScope,
    signal?: AbortSignal
  ) =>
    apiClient.post<OAuthCallbackResponse>(
      '/oauth/callback',
      {
        provider,
        redirect_url: redirectUrl,
      },
      createV8RequestConfig(requestScope, signal)
    ),

  cancelV8OAuthSession: (
    state: string,
    requestScope?: ApiClientRequestScope,
    signal?: AbortSignal
  ) =>
    apiClient.delete<OAuthCancelResponse>(
      '/oauth/session',
      createV8RequestConfig(requestScope, signal, { state })
    ),
};

export const captureSiwcBindingApi = (requestScope?: ApiClientRequestScope): SiwcBindingApi => ({
  start: (name, signal) => oauthApi.startSiwcBind(name, requestScope, signal),
  getStatus: (state, signal) => oauthApi.getV8OAuthStatus(state, requestScope, signal),
  submitCallback: (provider, redirectUrl, signal) =>
    oauthApi.submitV8OAuthCallback(provider, redirectUrl, requestScope, signal),
  cancelSession: (state, signal) => oauthApi.cancelV8OAuthSession(state, requestScope, signal),
});
