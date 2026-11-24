import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  publishAccountCredentialMutationRevision,
  useAuthStore,
  useNotificationStore,
} from '@/stores';
import {
  authFilesApi,
  oauthApi,
  pluginsApi,
  type OAuthProvider,
  type OAuthStatusResponse,
} from '@/services/api';
import type { ApiClientRequestScope } from '@/services/api/client';
import type { PluginListEntry } from '@/types';
import { createConnectionFingerprint } from '@/utils/connectionFingerprint';
import {
  completeAccountOAuthReauthSessionFromSearch,
  readAccountOAuthReauthSessionId,
} from '@/entities/accounts/reauthSession';
import {
  createAccountCredentialMutationBaseline,
  recordAccountCredentialMutationMarker,
  type AccountCredentialMutationBaseline,
} from '@/features/accounts/model/accountCredentialMutationMarker';
import { resolveCallbackUrl, validateDevinCallback } from '../callbackUrl';
import type { ProviderFlowState } from '../flowView';
import { createOAuthAttempts, type OAuthAttempt } from '../oauthAttempts';
import {
  isOAuthPollingScopeCurrent,
  isOAuthProviderAttemptCurrent,
  type OAuthPollingScope,
  type OAuthProviderAttempt,
} from '../oauthProviderHelpers';
import {
  BUILT_IN_OAUTH_PROVIDER_MAP,
  BUILT_IN_OAUTH_PROVIDERS,
  buildPluginOAuthProviderCards,
  getBuiltInProviderI18nKey,
  type OAuthProviderCard,
} from '../providers';

const POLL_INTERVAL_MS = 3000;
const SUCCESS_RESET_DELAY_MS = 5000;

interface ScopedProviderAttempt extends OAuthProviderAttempt {
  handle: OAuthAttempt;
  requestScope: ApiClientRequestScope;
  credentialBaseline?: AccountCredentialMutationBaseline;
}

interface UseOAuthFlowsOptions {
  search: string;
  isFocused: (provider: string) => boolean;
}

function getErrorMessage(error: unknown): string {
  if (error instanceof Error) return error.message;
  if (typeof error === 'object' && error !== null && 'message' in error) {
    if (typeof error.message === 'string') return error.message;
  }
  return typeof error === 'string' ? error : '';
}

function getErrorStatus(error: unknown): number | undefined {
  if (typeof error !== 'object' || error === null || !('status' in error)) return undefined;
  return typeof error.status === 'number' ? error.status : undefined;
}
export function useOAuthFlows({ search, isFocused }: UseOAuthFlowsOptions) {
  const { t } = useTranslation();
  const tRef = useRef(t);
  const showNotification = useNotificationStore((state) => state.showNotification);
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const apiBase = useAuthStore((state) => state.apiBase);
  const managementKey = useAuthStore((state) => state.managementKey);
  const supportsPlugin = useAuthStore((state) => state.supportsPlugin);

  const requestScope = useMemo<ApiClientRequestScope>(
    () => ({ apiBase, managementKey }),
    [apiBase, managementKey]
  );
  const pluginOAuthAvailable = connectionStatus === 'connected' && supportsPlugin;
  const connectionFingerprint = useMemo(
    () => createConnectionFingerprint(apiBase, managementKey),
    [apiBase, managementKey]
  );
  const accountReauthSessionId = useMemo(() => readAccountOAuthReauthSessionId(search), [search]);
  const oauthPollingScope = useMemo<OAuthPollingScope>(
    () => ({
      connectionFingerprint,
      accountReauthSessionId,
      search,
    }),
    [accountReauthSessionId, connectionFingerprint, search]
  );

  const scopeKey = `${connectionFingerprint ?? ''}\u0000${accountReauthSessionId ?? ''}`;

  const [statesEntry, setStatesEntry] = useState<{
    scopeKey: string;
    value: Record<string, ProviderFlowState>;
  }>(() => ({ scopeKey, value: {} }));
  const [connectionState, setConnectionState] = useState<{
    fingerprint: string | null;
    addedCount: number;
    plugins: PluginListEntry[];
    pluginsLoading: boolean;
    pluginLoadError: string | null;
  }>(() => ({
    fingerprint: connectionFingerprint,
    addedCount: 0,
    plugins: [],
    pluginsLoading: false,
    pluginLoadError: null,
  }));

  const states = useMemo(
    () => (statesEntry.scopeKey === scopeKey ? statesEntry.value : {}),
    [scopeKey, statesEntry]
  );
  const isSameConnection = connectionState.fingerprint === connectionFingerprint;
  const addedCount = isSameConnection ? connectionState.addedCount : 0;
  const pluginOAuthPlugins = useMemo(
    () => (isSameConnection && pluginOAuthAvailable ? connectionState.plugins : []),
    [connectionState.plugins, isSameConnection, pluginOAuthAvailable]
  );
  const pluginsLoading = isSameConnection && pluginOAuthAvailable ? connectionState.pluginsLoading : false;
  const pluginLoadError = isSameConnection && pluginOAuthAvailable ? connectionState.pluginLoadError : null;

  const statesRef = useRef(states);
  const isFocusedRef = useRef(isFocused);
  const oauthPollingScopeRef = useRef(oauthPollingScope);
  const connectionFingerprintRef = useRef(connectionFingerprint);

  useLayoutEffect(() => {
    tRef.current = t;
    statesRef.current = states;
    isFocusedRef.current = isFocused;
    connectionFingerprintRef.current = connectionFingerprint;
  }, [connectionFingerprint, isFocused, states, t]);

  const providerAttemptsRef = useRef(createOAuthAttempts());
  const callbackAttemptsRef = useRef(createOAuthAttempts());
  const successResetAttemptsRef = useRef(createOAuthAttempts());
  const providerAttemptVersionsRef = useRef<Partial<Record<string, number>>>({});
  const callbackAttemptVersionsRef = useRef<Partial<Record<string, number>>>({});
  const providerScopedAttemptsRef = useRef<Partial<Record<string, ScopedProviderAttempt>>>({});
  const pluginLoadGenerationRef = useRef(0);

  const invalidateAllFlowAttempts = useCallback(() => {
    providerAttemptsRef.current.invalidateAll();
    callbackAttemptsRef.current.invalidateAll();
    successResetAttemptsRef.current.invalidateAll();
    providerAttemptVersionsRef.current = {};
    callbackAttemptVersionsRef.current = {};
    providerScopedAttemptsRef.current = {};
  }, []);

  useLayoutEffect(() => {
    const previousScope = oauthPollingScopeRef.current;
    oauthPollingScopeRef.current = oauthPollingScope;
    if (isOAuthPollingScopeCurrent(previousScope, oauthPollingScope)) return;

    const connectionChanged =
      previousScope.connectionFingerprint !== oauthPollingScope.connectionFingerprint;

    invalidateAllFlowAttempts();
    if (connectionChanged) {
      pluginLoadGenerationRef.current += 1;
    }
  }, [invalidateAllFlowAttempts, oauthPollingScope]);

  useEffect(() => {
    return () => {
      pluginLoadGenerationRef.current += 1;
      invalidateAllFlowAttempts();
    };
  }, [invalidateAllFlowAttempts]);

  const fetchPluginProviders = useCallback(async () => {
    if (!pluginOAuthAvailable) return;
    const loadConnectionFingerprint = connectionFingerprint;
    const generation = pluginLoadGenerationRef.current + 1;
    pluginLoadGenerationRef.current = generation;
    try {
      const response = await pluginsApi.list(requestScope);
      if (
        connectionFingerprintRef.current !== loadConnectionFingerprint ||
        pluginLoadGenerationRef.current !== generation
      ) {
        return;
      }
      setConnectionState((prev) => ({
        fingerprint: loadConnectionFingerprint,
        addedCount: prev.fingerprint === loadConnectionFingerprint ? prev.addedCount : 0,
        plugins: response.plugins.filter((plugin) => plugin.supportsOAuth),
        pluginsLoading: false,
        pluginLoadError: null,
      }));
    } catch (err: unknown) {
      if (
        connectionFingerprintRef.current !== loadConnectionFingerprint ||
        pluginLoadGenerationRef.current !== generation
      ) {
        return;
      }
      const message = getErrorMessage(err) || tRef.current('auth_login.plugins_load_failed');
      setConnectionState((prev) => ({
        fingerprint: loadConnectionFingerprint,
        addedCount: prev.fingerprint === loadConnectionFingerprint ? prev.addedCount : 0,
        plugins: prev.fingerprint === loadConnectionFingerprint ? prev.plugins : [],
        pluginsLoading: false,
        pluginLoadError: message,
      }));
    }
  }, [connectionFingerprint, pluginOAuthAvailable, requestScope]);

  const loadPluginProviders = useCallback(async () => {
    if (!pluginOAuthAvailable) return;
    const loadConnectionFingerprint = connectionFingerprint;
    setConnectionState((prev) => ({
      fingerprint: loadConnectionFingerprint,
      addedCount: prev.fingerprint === loadConnectionFingerprint ? prev.addedCount : 0,
      plugins: prev.fingerprint === loadConnectionFingerprint ? prev.plugins : [],
      pluginsLoading: true,
      pluginLoadError: null,
    }));
    await fetchPluginProviders();
  }, [connectionFingerprint, fetchPluginProviders, pluginOAuthAvailable]);

  useEffect(() => {
    if (!pluginOAuthAvailable) return;
    const loadConnectionFingerprint = connectionFingerprint;
    const generation = pluginLoadGenerationRef.current + 1;
    pluginLoadGenerationRef.current = generation;
    let cancelled = false;
    pluginsApi
      .list(requestScope)
      .then((response) => {
        if (
          cancelled ||
          connectionFingerprintRef.current !== loadConnectionFingerprint ||
          pluginLoadGenerationRef.current !== generation
        ) {
          return;
        }
        setConnectionState((prev) => ({
          fingerprint: loadConnectionFingerprint,
          addedCount: prev.fingerprint === loadConnectionFingerprint ? prev.addedCount : 0,
          plugins: response.plugins.filter((plugin) => plugin.supportsOAuth),
          pluginsLoading: false,
          pluginLoadError: null,
        }));
      })
      .catch((err: unknown) => {
        if (
          cancelled ||
          connectionFingerprintRef.current !== loadConnectionFingerprint ||
          pluginLoadGenerationRef.current !== generation
        ) {
          return;
        }
        const message = getErrorMessage(err) || tRef.current('auth_login.plugins_load_failed');
        setConnectionState((prev) => ({
          fingerprint: loadConnectionFingerprint,
          addedCount: prev.fingerprint === loadConnectionFingerprint ? prev.addedCount : 0,
          plugins: prev.fingerprint === loadConnectionFingerprint ? prev.plugins : [],
          pluginsLoading: false,
          pluginLoadError: message,
        }));
      });
    return () => {
      cancelled = true;
    };
  }, [connectionFingerprint, pluginOAuthAvailable, requestScope]);

  const pluginProviders = useMemo(
    () =>
      pluginOAuthAvailable ? buildPluginOAuthProviderCards(pluginOAuthPlugins, apiBase) : [],
    [apiBase, pluginOAuthAvailable, pluginOAuthPlugins]
  );

  const providerCards = useMemo<OAuthProviderCard[]>(
    () => [...BUILT_IN_OAUTH_PROVIDERS, ...pluginProviders],
    [pluginProviders]
  );

  const recordCredentialAdded = useCallback(() => {
    const fingerprint = connectionFingerprintRef.current;
    setConnectionState((prev) => ({
      fingerprint,
      addedCount: (prev.fingerprint === fingerprint ? prev.addedCount : 0) + 1,
      plugins: prev.fingerprint === fingerprint ? prev.plugins : [],
      pluginsLoading: prev.fingerprint === fingerprint ? prev.pluginsLoading : false,
      pluginLoadError: prev.fingerprint === fingerprint ? prev.pluginLoadError : null,
    }));
  }, []);
  const getProviderCard = useCallback(
    (provider: OAuthProvider) => providerCards.find((item) => item.id === provider),
    [providerCards]
  );

  const providerText = useCallback(
    (provider: OAuthProvider, suffix: string): string => {
      const card = getProviderCard(provider);
      if (
        card?.kind === 'builtin' ||
        (!card && Object.prototype.hasOwnProperty.call(BUILT_IN_OAUTH_PROVIDER_MAP, provider))
      ) {
        return t(getBuiltInProviderI18nKey(provider, suffix));
      }
      const pluginName = card?.kind === 'plugin' ? card.title : provider;
      return t(`auth_login.plugin_${suffix}`, {
        plugin: pluginName,
        name: pluginName,
      });
    },
    [getProviderCard, t]
  );

  const updateProviderState = useCallback(
    (provider: OAuthProvider, next: Partial<ProviderFlowState>) => {
      setStatesEntry((prev) => {
        const current = prev.scopeKey === scopeKey ? prev.value : {};
        return {
          scopeKey,
          value: {
            ...current,
            [provider]: { ...(current[provider] ?? {}), ...next },
          },
        };
      });
    },
    [scopeKey]
  );

  const isConnectionActive = (fingerprint: string | null): boolean => {
    if (!fingerprint) return false;
    const currentAuth = useAuthStore.getState();
    const currentConnectionFingerprint = createConnectionFingerprint(
      currentAuth.apiBase,
      currentAuth.managementKey
    );
    return (
      currentConnectionFingerprint === fingerprint &&
      connectionFingerprintRef.current === fingerprint
    );
  };

  const isProviderAttemptCurrent = useCallback(
    (provider: OAuthProvider, attempt: ScopedProviderAttempt): boolean =>
      attempt.handle.isCurrent() &&
      isConnectionActive(attempt.scope.connectionFingerprint) &&
      isOAuthProviderAttemptCurrent(
        attempt,
        oauthPollingScopeRef.current,
        providerAttemptVersionsRef.current[provider]
      ),
    []
  );

  const isCallbackAttemptCurrent = useCallback(
    (provider: OAuthProvider, attempt: ScopedProviderAttempt): boolean =>
      attempt.handle.isCurrent() &&
      isConnectionActive(attempt.scope.connectionFingerprint) &&
      isOAuthProviderAttemptCurrent(
        attempt,
        oauthPollingScopeRef.current,
        callbackAttemptVersionsRef.current[provider]
      ),
    []
  );

  const beginProviderAttempt = useCallback(
    (provider: OAuthProvider): ScopedProviderAttempt => {
      successResetAttemptsRef.current.invalidate(provider);
      callbackAttemptsRef.current.invalidate(provider);
      delete callbackAttemptVersionsRef.current[provider];

      const version = (providerAttemptVersionsRef.current[provider] ?? 0) + 1;
      providerAttemptVersionsRef.current[provider] = version;
      const handle = providerAttemptsRef.current.begin(provider);
      const scoped: ScopedProviderAttempt = {
        scope: oauthPollingScopeRef.current,
        version,
        handle,
        requestScope,
      };
      providerScopedAttemptsRef.current[provider] = scoped;
      return scoped;
    },
    [requestScope]
  );

  const finishProviderAttempt = useCallback(
    (provider: OAuthProvider, attempt: ScopedProviderAttempt) => {
      if (providerAttemptVersionsRef.current[provider] === attempt.version) {
        delete providerAttemptVersionsRef.current[provider];
        delete providerScopedAttemptsRef.current[provider];
      }
      attempt.handle.invalidate();
    },
    []
  );

  const beginCallbackAttempt = useCallback(
    (provider: OAuthProvider): ScopedProviderAttempt => {
      const version = (callbackAttemptVersionsRef.current[provider] ?? 0) + 1;
      callbackAttemptVersionsRef.current[provider] = version;
      const handle = callbackAttemptsRef.current.begin(provider);
      return {
        scope: oauthPollingScopeRef.current,
        version,
        handle,
        requestScope,
      };
    },
    [requestScope]
  );

  const finishCallbackAttempt = useCallback(
    (provider: OAuthProvider, attempt: ScopedProviderAttempt) => {
      if (callbackAttemptVersionsRef.current[provider] === attempt.version) {
        delete callbackAttemptVersionsRef.current[provider];
      }
      attempt.handle.invalidate();
    },
    []
  );

  const resetProviderAttempt = useCallback(
    (provider: OAuthProvider) => {
      providerAttemptsRef.current.invalidate(provider);
      callbackAttemptsRef.current.invalidate(provider);
      successResetAttemptsRef.current.invalidate(provider);
      delete providerAttemptVersionsRef.current[provider];
      delete callbackAttemptVersionsRef.current[provider];
      delete providerScopedAttemptsRef.current[provider];
      setStatesEntry((prev) => {
        const current = prev.scopeKey === scopeKey ? prev.value : {};
        return {
          scopeKey,
          value: { ...current, [provider]: {} },
        };
      });
    },
    [scopeKey]
  );
  const dismissSuccess = useCallback(
    (provider: OAuthProvider) => {
      if (statesRef.current[provider]?.status === 'success') {
        resetProviderAttempt(provider);
      }
    },
    [resetProviderAttempt]
  );

  const completeProviderAuth = useCallback(
    (provider: OAuthProvider, attempt: ScopedProviderAttempt): boolean => {
      if (!isProviderAttemptCurrent(provider, attempt)) return false;
      const currentScope = oauthPollingScopeRef.current;
      const completionConnectionFingerprint = attempt.scope.connectionFingerprint;
      if (
        !completionConnectionFingerprint ||
        !isOAuthPollingScopeCurrent(attempt.scope, currentScope)
      ) {
        return false;
      }

      const reauthCompleted = completeAccountOAuthReauthSessionFromSearch(
        currentScope.search,
        provider,
        completionConnectionFingerprint
      );
      recordAccountCredentialMutationMarker({
        connectionFingerprint: completionConnectionFingerprint,
        provider,
        baseline: attempt.credentialBaseline,
        requireObservedMutation: true,
      });
      publishAccountCredentialMutationRevision({
        connectionFingerprint: completionConnectionFingerprint,
        provider,
        kind: 'oauth',
      });

      finishProviderAttempt(provider, attempt);
      callbackAttemptsRef.current.invalidate(provider);
      delete callbackAttemptVersionsRef.current[provider];
      recordCredentialAdded();

      updateProviderState(provider, {
        url: undefined,
        userCode: undefined,
        flow: undefined,
        expiresAtMs: undefined,
        state: undefined,
        status: 'success',
        error: undefined,
        polling: false,
        pollingError: undefined,
        cancelling: false,
        cancelError: undefined,
        callbackUrl: '',
        callbackSubmitting: false,
        callbackStatus: undefined,
        callbackError: undefined,
        callbackValidation: undefined,
        reauthCompleted,
      });

      // Keep the success receipt visible while the dialog is focused; when completed
      // in the background, briefly show "Added" on the tile before returning to idle.
      const resetHandle = successResetAttemptsRef.current.begin(provider);
      resetHandle.schedule(() => {
        if (!isFocusedRef.current(provider)) {
          resetProviderAttempt(provider);
        }
      }, SUCCESS_RESET_DELAY_MS);
      return true;
    },
    [
      finishProviderAttempt,
      isProviderAttemptCurrent,
      recordCredentialAdded,
      resetProviderAttempt,
      updateProviderState,
    ]
  );

  const handleProviderAuthStatus = useCallback(
    (
      provider: OAuthProvider,
      response: OAuthStatusResponse,
      attempt: ScopedProviderAttempt,
      successMessage: string
    ): 'completed' | 'waiting' | 'error' | 'stale' => {
      if (!isProviderAttemptCurrent(provider, attempt)) return 'stale';
      if (response.status === 'ok') {
        if (!completeProviderAuth(provider, attempt)) return 'stale';
        showNotification(successMessage, 'success');
        return 'completed';
      }
      if (response.status === 'error') {
        finishProviderAttempt(provider, attempt);
        callbackAttemptsRef.current.invalidate(provider);
        delete callbackAttemptVersionsRef.current[provider];
        updateProviderState(provider, {
          url: undefined,
          userCode: undefined,
          state: undefined,
          expiresAtMs: undefined,
          status: 'error',
          error: response.error,
          polling: false,
          pollingError: undefined,
          cancelling: false,
          callbackUrl: '',
          callbackSubmitting: false,
          callbackStatus: 'error',
          callbackError: response.error,
          callbackValidation: undefined,
        });
        showNotification(
          `${providerText(provider, 'oauth_status_error')} ${response.error || ''}`.trim(),
          'error'
        );
        return 'error';
      }
      updateProviderState(provider, { pollingError: undefined });
      return 'waiting';
    },
    [
      completeProviderAuth,
      finishProviderAttempt,
      isProviderAttemptCurrent,
      providerText,
      showNotification,
      updateProviderState,
    ]
  );

  const startPolling = useCallback(
    (provider: OAuthProvider, state: string, attempt: ScopedProviderAttempt) => {
      if (!isProviderAttemptCurrent(provider, attempt)) return;
      updateProviderState(provider, { polling: true, pollingError: undefined });
      attempt.handle.poll(
        () => oauthApi.getAuthStatus(state, attempt.requestScope, attempt.handle.signal),
        (res) => {
          const result = handleProviderAuthStatus(
            provider,
            res,
            attempt,
            providerText(provider, 'oauth_status_success')
          );
          return result === 'waiting';
        },
        (err) => {
          if (!isProviderAttemptCurrent(provider, attempt)) return;
          // Preserve the live attempt and session state so users can retry status
          // checks or submit a manual callback URL after a transient network glitch.
          updateProviderState(provider, {
            polling: false,
            pollingError: getErrorMessage(err) || providerText(provider, 'oauth_status_error'),
          });
        },
        POLL_INTERVAL_MS
      );
    },
    [handleProviderAuthStatus, isProviderAttemptCurrent, providerText, updateProviderState]
  );

  const resumePolling = useCallback(
    (provider: OAuthProvider) => {
      const currentState = statesRef.current[provider];
      const attempt = providerScopedAttemptsRef.current[provider];
      if (!currentState?.state || !attempt || !isProviderAttemptCurrent(provider, attempt)) {
        return;
      }
      startPolling(provider, currentState.state, attempt);
    },
    [isProviderAttemptCurrent, startPolling]
  );

  const startAuth = useCallback(
    async (provider: OAuthProvider) => {
      // Devin sessions hold a server-side callback listener; require cancelling an
      // active Devin session before starting a replacement.
      if (
        provider === 'devin' &&
        statesRef.current[provider]?.state &&
        statesRef.current[provider]?.status !== 'success'
      ) {
        return;
      }

      const attempt = beginProviderAttempt(provider);
      updateProviderState(provider, {
        url: undefined,
        userCode: undefined,
        flow: undefined,
        expiresAtMs: undefined,
        state: undefined,
        status: 'waiting',
        error: undefined,
        polling: true,
        pollingError: undefined,
        cancelling: false,
        cancelError: undefined,
        callbackUrl: '',
        callbackSubmitting: false,
        callbackStatus: undefined,
        callbackError: undefined,
        callbackValidation: undefined,
        reauthCompleted: false,
      });

      try {
        try {
          const response = await authFilesApi.list(attempt.requestScope);
          if (!isProviderAttemptCurrent(provider, attempt)) return;
          attempt.credentialBaseline =
            createAccountCredentialMutationBaseline(response.files, provider) ?? undefined;
        } catch {
          if (!isProviderAttemptCurrent(provider, attempt)) return;
          // OAuth remains available, but completion will fail closed without a mutation marker.
        }

        const res = await oauthApi.startAuth(provider, attempt.requestScope, attempt.handle.signal);
        if (!isProviderAttemptCurrent(provider, attempt)) return;

        if (!res.state) {
          const message = t('auth_login.missing_state');
          finishProviderAttempt(provider, attempt);
          updateProviderState(provider, {
            url: undefined,
            userCode: undefined,
            state: undefined,
            status: 'error',
            error: message,
            polling: false,
          });
          showNotification(message, 'error');
          return;
        }

        const expiresIn =
          typeof res.expires_in === 'number' && Number.isFinite(res.expires_in) && res.expires_in > 0
            ? res.expires_in
            : undefined;

        updateProviderState(provider, {
          url: res.url,
          userCode: res.user_code?.trim() || undefined,
          flow: res.flow?.trim() || undefined,
          expiresAtMs: expiresIn ? Date.now() + expiresIn * 1000 : undefined,
          state: res.state,
          status: 'waiting',
          polling: true,
          pollingError: undefined,
        });
        startPolling(provider, res.state, attempt);
      } catch (err: unknown) {
        if (!isProviderAttemptCurrent(provider, attempt)) return;
        const message = getErrorMessage(err);
        finishProviderAttempt(provider, attempt);
        updateProviderState(provider, {
          url: undefined,
          userCode: undefined,
          state: undefined,
          status: 'error',
          error: message,
          polling: false,
        });
        showNotification(
          `${providerText(provider, 'oauth_start_error')}${message ? ` ${message}` : ''}`,
          'error'
        );
      }
    },
    [
      beginProviderAttempt,
      finishProviderAttempt,
      isProviderAttemptCurrent,
      providerText,
      showNotification,
      startPolling,
      t,
      updateProviderState,
    ]
  );

  const cancelAuth = useCallback(
    async (provider: OAuthProvider): Promise<boolean> => {
      const currentState = statesRef.current[provider];
      const state = currentState?.state;
      if (!state) {
        resetProviderAttempt(provider);
        return true;
      }
      if (currentState?.cancelling) return false;
      const previousBaseline = providerScopedAttemptsRef.current[provider]?.credentialBaseline;
      const attempt = beginProviderAttempt(provider);
      attempt.credentialBaseline = previousBaseline;
      updateProviderState(provider, {
        cancelling: true,
        cancelError: undefined,
        polling: true,
        callbackSubmitting: false,
      });

      try {
        const result = await oauthApi.cancelSession(
          state,
          attempt.requestScope,
          attempt.handle.signal
        );
        if (!isProviderAttemptCurrent(provider, attempt)) return false;
        if (result.cancelled) {
          resetProviderAttempt(provider);
          showNotification(t('auth_login.oauth_session_cancelled'), 'info');
          return true;
        }
        // Session already completed or expired server-side: probe real status before giving up.
        try {
          const statusRes = await oauthApi.getAuthStatus(
            state,
            attempt.requestScope,
            attempt.handle.signal
          );
          if (!isProviderAttemptCurrent(provider, attempt)) return false;
          const outcome = handleProviderAuthStatus(
            provider,
            statusRes,
            attempt,
            providerText(provider, 'oauth_status_success')
          );
          if (outcome === 'completed' || outcome === 'error') return false;
        } catch {
          if (!isProviderAttemptCurrent(provider, attempt)) return false;
        }
      } catch (err: unknown) {
        if (!isProviderAttemptCurrent(provider, attempt)) return false;
        const status = getErrorStatus(err);
        // Older CPA servers without DELETE /oauth-session return 404/405:
        // stop polling locally so users can always cancel or restart cleanly.
        if (status === 404 || status === 405) {
          resetProviderAttempt(provider);
          showNotification(t('auth_login.oauth_session_cancelled'), 'info');
          return true;
        }
        const message = getErrorMessage(err);
        updateProviderState(provider, {
          cancelling: false,
          cancelError: message || t('auth_login.oauth_cancel_error'),
        });
        showNotification(
          `${t('auth_login.oauth_cancel_error')}${message ? ` ${message}` : ''}`,
          'error'
        );
        startPolling(provider, state, attempt);
        return false;
      }

      updateProviderState(provider, {
        cancelling: false,
        status: 'waiting',
        error: undefined,
      });
      startPolling(provider, state, attempt);
      return false;
    },
    [
      beginProviderAttempt,
      handleProviderAuthStatus,
      isProviderAttemptCurrent,
      providerText,
      resetProviderAttempt,
      showNotification,
      startPolling,
      t,
      updateProviderState,
    ]
  );

  const setCallbackInput = useCallback(
    (provider: OAuthProvider, value: string) => {
      updateProviderState(provider, {
        callbackUrl: value,
        callbackStatus: undefined,
        callbackError: undefined,
        callbackValidation: undefined,
      });
    },
    [updateProviderState]
  );

  const submitCallback = useCallback(
    async (provider: OAuthProvider) => {
      const providerState = statesRef.current[provider];
      const callbackInput = (providerState?.callbackUrl || '').trim();
      if (!callbackInput) {
        const message = t(
          provider === 'xai'
            ? 'auth_login.xai_callback_required'
            : 'auth_login.oauth_callback_required'
        );
        updateProviderState(provider, { callbackValidation: message });
        showNotification(message, 'warning');
        return;
      }

      const state = providerState?.state;
      const providerAttempt = providerScopedAttemptsRef.current[provider];

      if (provider === 'devin') {
        const devinError = validateDevinCallback(callbackInput, state);
        if (devinError) {
          const message = t(
            devinError === 'state_mismatch'
              ? 'auth_login.devin_callback_state_mismatch'
              : 'auth_login.devin_callback_invalid'
          );
          updateProviderState(provider, { callbackValidation: message });
          showNotification(message, 'warning');
          return;
        }
      }

      const redirectUrl = resolveCallbackUrl(provider, callbackInput, state);
      if (!redirectUrl) {
        const message = t(
          provider === 'xai' ? 'auth_login.xai_callback_state_missing' : 'auth_login.missing_state'
        );
        updateProviderState(provider, { callbackValidation: message });
        showNotification(message, 'warning');
        return;
      }

      if (!state || !providerAttempt || !isProviderAttemptCurrent(provider, providerAttempt)) {
        const message = t('auth_login.missing_state');
        updateProviderState(provider, { callbackValidation: message });
        showNotification(message, 'warning');
        return;
      }

      const callbackAttempt = beginCallbackAttempt(provider);
      updateProviderState(provider, {
        callbackSubmitting: true,
        callbackStatus: undefined,
        callbackError: undefined,
        callbackValidation: undefined,
      });

      const probeStatus = async () => {
        const response = await oauthApi.getAuthStatus(
          state,
          providerAttempt.requestScope,
          callbackAttempt.handle.signal
        );
        if (
          !isCallbackAttemptCurrent(provider, callbackAttempt) ||
          !isProviderAttemptCurrent(provider, providerAttempt)
        ) {
          return;
        }
        finishCallbackAttempt(provider, callbackAttempt);
        const outcome = handleProviderAuthStatus(
          provider,
          response,
          providerAttempt,
          providerText(provider, 'oauth_status_success')
        );
        if (outcome === 'waiting' && !providerAttempt.handle.isPolling()) {
          startPolling(provider, state, providerAttempt);
        }
      };

      try {
        await oauthApi.submitCallback(
          provider,
          redirectUrl,
          callbackAttempt.requestScope,
          callbackAttempt.handle.signal
        );
        if (!isCallbackAttemptCurrent(provider, callbackAttempt)) return;
        if (!isProviderAttemptCurrent(provider, providerAttempt)) return;
        updateProviderState(provider, {
          callbackSubmitting: false,
          callbackStatus: 'success',
          callbackError: undefined,
        });
        showNotification(t('auth_login.oauth_callback_success'), 'success');
        try {
          await probeStatus();
        } catch {
          if (isCallbackAttemptCurrent(provider, callbackAttempt)) {
            finishCallbackAttempt(provider, callbackAttempt);
          }
        }
      } catch (err: unknown) {
        if (!isCallbackAttemptCurrent(provider, callbackAttempt)) return;
        const status = getErrorStatus(err);
        if (status === 409 && isProviderAttemptCurrent(provider, providerAttempt)) {
          updateProviderState(provider, {
            callbackSubmitting: false,
            callbackStatus: 'success',
            callbackError: undefined,
          });
          try {
            await probeStatus();
          } catch (probeError: unknown) {
            if (!isCallbackAttemptCurrent(provider, callbackAttempt)) return;
            finishCallbackAttempt(provider, callbackAttempt);
            updateProviderState(provider, {
              callbackSubmitting: false,
              callbackStatus: 'error',
              callbackError: getErrorMessage(probeError) || undefined,
            });
          }
          return;
        }
        const message = getErrorMessage(err);
        const errorMessage =
          status === 404
            ? t('auth_login.oauth_callback_upgrade_hint', {
                defaultValue: 'Please update CLI Proxy API or check the connection.',
              })
            : message || undefined;
        finishCallbackAttempt(provider, callbackAttempt);
        updateProviderState(provider, {
          callbackSubmitting: false,
          callbackStatus: 'error',
          callbackError: errorMessage,
        });
        const notificationMessage = errorMessage
          ? `${t('auth_login.oauth_callback_error')} ${errorMessage}`
          : t('auth_login.oauth_callback_error');
        showNotification(notificationMessage, 'error');
      }
    },
    [
      beginCallbackAttempt,
      finishCallbackAttempt,
      handleProviderAuthStatus,
      isCallbackAttemptCurrent,
      isProviderAttemptCurrent,
      providerText,
      showNotification,
      startPolling,
      t,
      updateProviderState,
    ]
  );

  return {
    apiBase,
    connectionStatus,
    connectionFingerprint,
    requestScope,
    accountReauthSessionId,
    pluginOAuthAvailable,
    builtInProviders: BUILT_IN_OAUTH_PROVIDERS,
    pluginProviders,
    providerCards,
    pluginsLoading,
    pluginLoadError,
    states,
    addedCount,
    recordCredentialAdded,
    loadPluginProviders,
    providerText,
    startAuth,
    cancelAuth,
    resumePolling,
    setCallbackInput,
    submitCallback,
    dismissSuccess,
  };
}
