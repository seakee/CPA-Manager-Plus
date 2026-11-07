import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { Trans, useTranslation } from 'react-i18next';
import { authFilesApi, type AuthFilesApiRequestScope } from '@/services/api';
import { useNotificationStore } from '@/stores';
import type { AuthFileItem, OAuthModelAliasEntry } from '@/types';
import type { AuthFileModelItem, OAuthConfigLoadState } from '@/features/authFiles/constants';
import { normalizeProviderKey } from '@/features/authFiles/constants';
import {
  applyOAuthAliasWritePlans,
  createSerialAsyncQueue,
  findChannelMappings,
  getHttpStatusCode,
  isMissingOrMethodNotAllowedStatus,
  mergeOAuthAliasLink,
  normalizeOAuthAliasEntries,
  OAuthAliasRollbackError,
  planOAuthAliasRename,
} from '@/features/authFiles/oauthAliasValidation';

type ViewMode = 'diagram' | 'list';

const EMPTY_EXCLUDED_MODELS: Record<string, string[]> = {};
const EMPTY_MODEL_ALIASES: Record<string, OAuthModelAliasEntry[]> = {};
const EMPTY_PROVIDER_MODELS: Record<string, AuthFileModelItem[]> = {};

class OAuthConnectionScopeChangedError extends Error {}

export type UseAuthFilesOauthResult = {
  excluded: Record<string, string[]>;
  excludedError: OAuthConfigLoadState;
  excludedLoaded: boolean;
  modelAlias: Record<string, OAuthModelAliasEntry[]>;
  modelAliasError: OAuthConfigLoadState;
  modelAliasLoaded: boolean;
  allProviderModels: Record<string, AuthFileModelItem[]>;
  providerList: string[];
  loadExcluded: () => Promise<void>;
  loadModelAlias: () => Promise<void>;
  deleteExcluded: (provider: string) => void;
  deleteModelAlias: (provider: string) => void;
  handleMappingUpdate: (provider: string, sourceModel: string, newAlias: string) => Promise<void>;
  handleDeleteLink: (provider: string, sourceModel: string, alias: string) => void;
  handleToggleFork: (
    provider: string,
    sourceModel: string,
    alias: string,
    fork: boolean
  ) => Promise<void>;
  handleRenameAlias: (oldAlias: string, newAlias: string) => Promise<void>;
  handleDeleteAlias: (aliasName: string) => void;
};

export type UseAuthFilesOauthOptions = {
  viewMode: ViewMode;
  files: AuthFileItem[];
  connectionKey?: string | null;
  requestScope: AuthFilesApiRequestScope;
};

export function useAuthFilesOauth(options: UseAuthFilesOauthOptions): UseAuthFilesOauthResult {
  const { viewMode, files } = options;
  const normalizedConnectionKey = String(options.connectionKey ?? '');
  const requestScope = useMemo<AuthFilesApiRequestScope>(
    () => ({
      apiBase: options.requestScope.apiBase,
      managementKey: options.requestScope.managementKey,
    }),
    [options.requestScope.apiBase, options.requestScope.managementKey]
  );
  const { t } = useTranslation();
  const { showNotification, showConfirmation } = useNotificationStore();

  const [excluded, setExcluded] = useState<Record<string, string[]>>({});
  const [excludedError, setExcludedError] = useState<OAuthConfigLoadState>('loading');
  const [excludedLoaded, setExcludedLoaded] = useState(false);
  const [modelAlias, setModelAlias] = useState<Record<string, OAuthModelAliasEntry[]>>({});
  const [modelAliasError, setModelAliasError] = useState<OAuthConfigLoadState>('loading');
  const [modelAliasLoaded, setModelAliasLoaded] = useState(false);
  const [allProviderModels, setAllProviderModels] = useState<Record<string, AuthFileModelItem[]>>(
    {}
  );
  const [stateConnectionKey, setStateConnectionKey] = useState(normalizedConnectionKey);

  const connectionKeyRef = useRef(normalizedConnectionKey);
  const excludedUnsupportedRef = useRef(false);
  const mappingsUnsupportedRef = useRef(false);
  /**
   * Baseline writes are allowed only after at least one successful GET.
   * Soft refresh after mutations must not clear this, otherwise concurrent
   * diagram writes race with loadModelAlias and get rejected as "not ready".
   */
  const excludedBaselineOkRef = useRef(false);
  const modelAliasBaselineOkRef = useRef(false);
  const excludedLoadRequestRef = useRef(0);
  const modelAliasLoadRequestRef = useRef(0);
  const modelAliasWriteQueueRef = useRef(createSerialAsyncQueue());
  const excludedWriteQueueRef = useRef(createSerialAsyncQueue());

  const isConnectionCurrent = useCallback(
    (connectionKey: string) => connectionKeyRef.current === connectionKey,
    []
  );
  const assertConnectionCurrent = useCallback((connectionKey: string) => {
    if (connectionKeyRef.current !== connectionKey) {
      throw new OAuthConnectionScopeChangedError();
    }
  }, []);

  useLayoutEffect(() => {
    let cancelled = false;
    connectionKeyRef.current = normalizedConnectionKey;
    excludedUnsupportedRef.current = false;
    mappingsUnsupportedRef.current = false;
    excludedBaselineOkRef.current = false;
    modelAliasBaselineOkRef.current = false;
    excludedLoadRequestRef.current += 1;
    modelAliasLoadRequestRef.current += 1;
    modelAliasWriteQueueRef.current = createSerialAsyncQueue();
    excludedWriteQueueRef.current = createSerialAsyncQueue();
    queueMicrotask(() => {
      if (cancelled || connectionKeyRef.current !== normalizedConnectionKey) return;
      setExcluded({});
      setExcludedLoaded(false);
      setExcludedError('loading');
      setModelAlias({});
      setModelAliasLoaded(false);
      setModelAliasError('loading');
      setAllProviderModels({});
      setStateConnectionKey(normalizedConnectionKey);
    });
    return () => {
      cancelled = true;
    };
  }, [normalizedConnectionKey]);

  useEffect(
    () => () => {
      connectionKeyRef.current = '\u0000unmounted';
      excludedBaselineOkRef.current = false;
      modelAliasBaselineOkRef.current = false;
      excludedLoadRequestRef.current += 1;
      modelAliasLoadRequestRef.current += 1;
    },
    []
  );

  const scopedExcluded =
    stateConnectionKey === normalizedConnectionKey ? excluded : EMPTY_EXCLUDED_MODELS;
  const scopedModelAlias =
    stateConnectionKey === normalizedConnectionKey ? modelAlias : EMPTY_MODEL_ALIASES;
  const scopedAllProviderModels =
    stateConnectionKey === normalizedConnectionKey ? allProviderModels : EMPTY_PROVIDER_MODELS;

  const providerList = useMemo(() => {
    const providers = new Set<string>();

    Object.keys(scopedModelAlias).forEach((provider) => {
      const key = provider.trim().toLowerCase();
      if (key) providers.add(key);
    });

    files.forEach((file) => {
      if (typeof file.type === 'string') {
        const key = file.type.trim().toLowerCase();
        if (key) providers.add(key);
      }
      if (typeof file.provider === 'string') {
        const key = file.provider.trim().toLowerCase();
        if (key) providers.add(key);
      }
    });
    return Array.from(providers);
  }, [files, scopedModelAlias]);

  useEffect(() => {
    if (viewMode !== 'diagram') return;

    let cancelled = false;
    const requestConnectionKey = normalizedConnectionKey;

    const loadAllModels = async () => {
      if (providerList.length === 0) {
        if (!cancelled) setAllProviderModels({});
        return;
      }

      const results = await Promise.all(
        providerList.map(async (provider) => {
          try {
            const models = await authFilesApi.getModelDefinitions(provider, requestScope);
            return { provider, models };
          } catch {
            return { provider, models: null };
          }
        })
      );

      if (cancelled || !isConnectionCurrent(requestConnectionKey)) return;

      setAllProviderModels((current) => {
        const nextModels: Record<string, AuthFileModelItem[]> = {};
        results.forEach(({ provider, models }) => {
          const retained = models ?? current[provider];
          if (retained?.length) nextModels[provider] = retained;
        });
        return nextModels;
      });
      if (results.some(({ models }) => models === null)) {
        showNotification(t('notification.refresh_failed'), 'error');
      }
    };

    void loadAllModels();

    return () => {
      cancelled = true;
    };
  }, [
    isConnectionCurrent,
    normalizedConnectionKey,
    providerList,
    requestScope,
    viewMode,
    showNotification,
    t,
  ]);

  const loadExcluded = useCallback(
    async (options?: { soft?: boolean }) => {
      const soft = options?.soft === true;
      const requestId = ++excludedLoadRequestRef.current;
      const requestConnectionKey = normalizedConnectionKey;
      if (!soft) {
        excludedBaselineOkRef.current = false;
        setExcludedError('loading');
      }
      try {
        const res = await authFilesApi.getOauthExcludedModels(requestScope);
        if (
          requestId !== excludedLoadRequestRef.current ||
          !isConnectionCurrent(requestConnectionKey)
        ) {
          return;
        }
        excludedUnsupportedRef.current = false;
        excludedBaselineOkRef.current = true;
        setExcluded(res || {});
        setExcludedLoaded(true);
        setExcludedError('ready');
      } catch (err: unknown) {
        if (
          requestId !== excludedLoadRequestRef.current ||
          !isConnectionCurrent(requestConnectionKey)
        ) {
          return;
        }
        const status = getHttpStatusCode(err);

        if (status === 404) {
          setExcluded({});
          setExcludedError('unsupported');
          excludedBaselineOkRef.current = false;
          if (!excludedUnsupportedRef.current) {
            excludedUnsupportedRef.current = true;
            showNotification(t('oauth_excluded.upgrade_required'), 'warning');
          }
          return;
        }
        setExcludedError('error');
        excludedBaselineOkRef.current = false;
        if (soft) showNotification(t('notification.refresh_failed'), 'warning');
      }
    },
    [isConnectionCurrent, normalizedConnectionKey, requestScope, showNotification, t]
  );

  const loadModelAlias = useCallback(
    async (options?: { soft?: boolean }) => {
      const soft = options?.soft === true;
      const requestId = ++modelAliasLoadRequestRef.current;
      const requestConnectionKey = normalizedConnectionKey;
      if (!soft) {
        modelAliasBaselineOkRef.current = false;
        setModelAliasError('loading');
      }
      try {
        const res = await authFilesApi.getOauthModelAlias(requestScope);
        if (
          requestId !== modelAliasLoadRequestRef.current ||
          !isConnectionCurrent(requestConnectionKey)
        ) {
          return;
        }
        mappingsUnsupportedRef.current = false;
        modelAliasBaselineOkRef.current = true;
        setModelAlias(res || {});
        setModelAliasLoaded(true);
        setModelAliasError('ready');
      } catch (err: unknown) {
        if (
          requestId !== modelAliasLoadRequestRef.current ||
          !isConnectionCurrent(requestConnectionKey)
        ) {
          return;
        }
        const status = getHttpStatusCode(err);

        if (status === 404) {
          setModelAlias({});
          setModelAliasError('unsupported');
          modelAliasBaselineOkRef.current = false;
          if (!mappingsUnsupportedRef.current) {
            mappingsUnsupportedRef.current = true;
            showNotification(t('oauth_model_alias.upgrade_required'), 'warning');
          }
          return;
        }
        setModelAliasError('error');
        modelAliasBaselineOkRef.current = false;
        if (soft) showNotification(t('notification.refresh_failed'), 'warning');
      }
    },
    [isConnectionCurrent, normalizedConnectionKey, requestScope, showNotification, t]
  );

  const showLoadRequired = useCallback(() => {
    showNotification(t('notification.refresh_failed'), 'error');
  }, [showNotification, t]);

  const persistChannelMappings = useCallback(
    async (
      channel: string,
      mappings: OAuthModelAliasEntry[],
      mutationScope: AuthFilesApiRequestScope
    ) => {
      const normalized = normalizeOAuthAliasEntries(mappings);
      if (normalized.accepted.length === 0) {
        await authFilesApi.deleteOauthModelAlias(channel, mutationScope);
        return;
      }
      await authFilesApi.saveOauthModelAlias(channel, normalized.accepted, mutationScope);
    },
    []
  );

  const runModelAliasMutation = useCallback(
    async (
      task: (assertCurrent: () => void, mutationScope: AuthFilesApiRequestScope) => Promise<void>
    ) => {
      const requestConnectionKey = normalizedConnectionKey;
      const mutationScope = requestScope;
      if (!isConnectionCurrent(requestConnectionKey)) return;
      if (!modelAliasBaselineOkRef.current) {
        showLoadRequired();
        return;
      }
      try {
        await modelAliasWriteQueueRef.current(async () => {
          assertConnectionCurrent(requestConnectionKey);
          // Re-check after waiting in queue: hard load failure may have cleared baseline.
          if (!modelAliasBaselineOkRef.current) {
            throw new Error(t('notification.refresh_failed'));
          }
          await task(() => assertConnectionCurrent(requestConnectionKey), mutationScope);
          assertConnectionCurrent(requestConnectionKey);
          await loadModelAlias({ soft: true });
        });
      } catch (err: unknown) {
        if (err instanceof OAuthAliasRollbackError) {
          showNotification(
            t('oauth_model_alias.rollback_failed', {
              providers: err.failedChannels.join(', '),
            }),
            'error'
          );
          if (isConnectionCurrent(requestConnectionKey)) {
            await loadModelAlias({ soft: true });
          }
          return;
        }
        if (
          err instanceof OAuthConnectionScopeChangedError ||
          !isConnectionCurrent(requestConnectionKey)
        ) {
          return;
        }
        const errorMessage = err instanceof Error ? err.message : '';
        showNotification(
          errorMessage
            ? `${t('oauth_model_alias.save_failed')}: ${errorMessage}`
            : t('oauth_model_alias.save_failed'),
          'error'
        );
        await loadModelAlias({ soft: true });
      }
    },
    [
      assertConnectionCurrent,
      isConnectionCurrent,
      loadModelAlias,
      normalizedConnectionKey,
      requestScope,
      showLoadRequired,
      showNotification,
      t,
    ]
  );

  const deleteExcluded = useCallback(
    (provider: string) => {
      const requestConnectionKey = normalizedConnectionKey;
      const mutationScope = requestScope;
      const providerLabel = provider.trim() || provider;
      showConfirmation({
        title: t('oauth_excluded.delete_title', { defaultValue: 'Delete Exclusion' }),
        message: t('oauth_excluded.delete_confirm', { provider: providerLabel }),
        variant: 'danger',
        confirmText: t('common.confirm'),
        onConfirm: async () => {
          if (!isConnectionCurrent(requestConnectionKey)) return;
          if (!excludedBaselineOkRef.current) {
            showLoadRequired();
            return;
          }
          const providerKey = normalizeProviderKey(provider);
          if (!providerKey) {
            showNotification(t('oauth_excluded.provider_required'), 'error');
            return;
          }
          try {
            await excludedWriteQueueRef.current(async () => {
              assertConnectionCurrent(requestConnectionKey);
              if (!excludedBaselineOkRef.current) {
                throw new Error(t('notification.refresh_failed'));
              }
              try {
                await authFilesApi.deleteOauthExcludedEntry(providerKey, mutationScope);
                assertConnectionCurrent(requestConnectionKey);
              } catch (err: unknown) {
                if (err instanceof OAuthConnectionScopeChangedError) throw err;
                assertConnectionCurrent(requestConnectionKey);
                const status = getHttpStatusCode(err);
                if (!isMissingOrMethodNotAllowedStatus(status)) {
                  throw err;
                }
                // Fallback for CPA builds without DELETE: rewrite the full map from latest GET.
                const current = await authFilesApi.getOauthExcludedModels(mutationScope);
                assertConnectionCurrent(requestConnectionKey);
                const next: Record<string, string[]> = {};
                Object.entries(current).forEach(([key, models]) => {
                  if (normalizeProviderKey(key) === providerKey) return;
                  next[key] = models;
                });
                await authFilesApi.replaceOauthExcludedModels(next, mutationScope);
                assertConnectionCurrent(requestConnectionKey);
              }
              await loadExcluded({ soft: true });
              assertConnectionCurrent(requestConnectionKey);
            });
            showNotification(t('oauth_excluded.delete_success'), 'success');
          } catch (fallbackErr: unknown) {
            if (
              fallbackErr instanceof OAuthConnectionScopeChangedError ||
              !isConnectionCurrent(requestConnectionKey)
            ) {
              return;
            }
            const errorMessage = fallbackErr instanceof Error ? fallbackErr.message : '';
            showNotification(`${t('oauth_excluded.delete_failed')}: ${errorMessage}`, 'error');
            await loadExcluded({ soft: true });
          }
        },
      });
    },
    [
      assertConnectionCurrent,
      isConnectionCurrent,
      loadExcluded,
      normalizedConnectionKey,
      requestScope,
      showConfirmation,
      showLoadRequired,
      showNotification,
      t,
    ]
  );

  const deleteModelAlias = useCallback(
    (provider: string) => {
      showConfirmation({
        title: t('oauth_model_alias.delete_title', { defaultValue: 'Delete Mappings' }),
        message: t('oauth_model_alias.delete_confirm', { provider }),
        variant: 'danger',
        confirmText: t('common.confirm'),
        onConfirm: async () => {
          await runModelAliasMutation(async (assertCurrent, mutationScope) => {
            const latest = await authFilesApi.getOauthModelAlias(mutationScope);
            assertCurrent();
            const { channelKey } = findChannelMappings(latest, provider);
            if (!channelKey) return;
            await authFilesApi.deleteOauthModelAlias(channelKey, mutationScope);
            assertCurrent();
            showNotification(t('oauth_model_alias.delete_success'), 'success');
          });
        },
      });
    },
    [runModelAliasMutation, showConfirmation, showNotification, t]
  );

  const handleMappingUpdate = useCallback(
    async (provider: string, sourceModel: string, newAlias: string) => {
      if (!provider || !sourceModel || !newAlias) return;
      const normalizedProvider = normalizeProviderKey(provider);
      if (!normalizedProvider) return;

      const nameTrim = sourceModel.trim();
      const aliasTrim = newAlias.trim();
      if (!nameTrim || !aliasTrim) return;

      if (nameTrim.toLowerCase() === aliasTrim.toLowerCase()) {
        showNotification(t('oauth_model_alias.alias_same_as_name'), 'error');
        return;
      }

      await runModelAliasMutation(async (assertCurrent, mutationScope) => {
        const latest = await authFilesApi.getOauthModelAlias(mutationScope);
        assertCurrent();
        const { mappings: currentMappings } = findChannelMappings(latest, normalizedProvider);
        const mergeResult = mergeOAuthAliasLink(currentMappings, nameTrim, aliasTrim);

        if (mergeResult.kind === 'unchanged') return;
        if (mergeResult.kind === 'rejected') {
          if (mergeResult.reason === 'same_as_name') {
            showNotification(t('oauth_model_alias.alias_same_as_name'), 'error');
            return;
          }
          showNotification(
            t('oauth_model_alias.alias_duplicate', { alias: mergeResult.alias }),
            'error'
          );
          return;
        }

        await persistChannelMappings(normalizedProvider, mergeResult.mappings, mutationScope);
        assertCurrent();
        showNotification(t('oauth_model_alias.save_success'), 'success');
      });
    },
    [persistChannelMappings, runModelAliasMutation, showNotification, t]
  );

  const handleDeleteLink = useCallback(
    (provider: string, sourceModel: string, alias: string) => {
      const nameTrim = sourceModel.trim();
      const aliasTrim = alias.trim();
      if (!provider || !nameTrim || !aliasTrim) return;

      showConfirmation({
        title: t('oauth_model_alias.delete_link_title', { defaultValue: 'Unlink mapping' }),
        message: (
          <Trans
            i18nKey="oauth_model_alias.delete_link_confirm"
            values={{ provider, sourceModel: nameTrim, alias: aliasTrim }}
            components={{ code: <code /> }}
          />
        ),
        variant: 'danger',
        confirmText: t('common.confirm'),
        onConfirm: async () => {
          await runModelAliasMutation(async (assertCurrent, mutationScope) => {
            const normalizedProvider = normalizeProviderKey(provider);
            if (!normalizedProvider) return;
            const latest = await authFilesApi.getOauthModelAlias(mutationScope);
            assertCurrent();
            const { mappings: currentMappings } = findChannelMappings(latest, normalizedProvider);
            const nameKey = nameTrim.toLowerCase();
            const aliasKey = aliasTrim.toLowerCase();
            const nextMappings = currentMappings.filter(
              (mapping) =>
                (mapping.name ?? '').trim().toLowerCase() !== nameKey ||
                (mapping.alias ?? '').trim().toLowerCase() !== aliasKey
            );
            if (nextMappings.length === currentMappings.length) return;
            await persistChannelMappings(normalizedProvider, nextMappings, mutationScope);
            assertCurrent();
            showNotification(t('oauth_model_alias.save_success'), 'success');
          });
        },
      });
    },
    [persistChannelMappings, runModelAliasMutation, showConfirmation, showNotification, t]
  );

  const handleToggleFork = useCallback(
    async (provider: string, sourceModel: string, alias: string, fork: boolean) => {
      const normalizedProvider = normalizeProviderKey(provider);
      if (!normalizedProvider) return;

      await runModelAliasMutation(async (assertCurrent, mutationScope) => {
        const latest = await authFilesApi.getOauthModelAlias(mutationScope);
        assertCurrent();
        const { mappings: currentMappings } = findChannelMappings(latest, normalizedProvider);
        const nameKey = sourceModel.trim().toLowerCase();
        const aliasKey = alias.trim().toLowerCase();
        let changed = false;

        const nextMappings = currentMappings.map((mapping) => {
          const mappingName = (mapping.name ?? '').trim().toLowerCase();
          const mappingAlias = (mapping.alias ?? '').trim().toLowerCase();
          if (mappingName === nameKey && mappingAlias === aliasKey) {
            changed = true;
            if (fork) return { ...mapping, fork: true };
            const next = { ...mapping };
            delete next.fork;
            return next;
          }
          return mapping;
        });

        if (!changed) return;
        await persistChannelMappings(normalizedProvider, nextMappings, mutationScope);
        assertCurrent();
        showNotification(t('oauth_model_alias.save_success'), 'success');
      });
    },
    [persistChannelMappings, runModelAliasMutation, showNotification, t]
  );

  const handleRenameAlias = useCallback(
    async (oldAlias: string, newAlias: string) => {
      const oldTrim = oldAlias.trim();
      const newTrim = newAlias.trim();
      if (!oldTrim || !newTrim || oldTrim === newTrim) return;

      await runModelAliasMutation(async (assertCurrent, mutationScope) => {
        const latest = await authFilesApi.getOauthModelAlias(mutationScope);
        assertCurrent();
        const planResult = planOAuthAliasRename(latest, oldTrim, newTrim);

        if (!planResult.ok) {
          if (planResult.reason === 'duplicate_alias') {
            showNotification(
              t('oauth_model_alias.alias_duplicate', { alias: planResult.alias ?? newTrim }),
              'error'
            );
            return;
          }
          if (planResult.reason === 'same_as_name') {
            showNotification(t('oauth_model_alias.alias_same_as_name'), 'error');
            return;
          }
          return;
        }

        await applyOAuthAliasWritePlans(
          planResult.plans.map((plan) => ({
            ...plan,
            previousMappings: findChannelMappings(latest, plan.channel).mappings,
          })),
          async (channel, mappings) => {
            assertCurrent();
            await persistChannelMappings(channel, mappings, mutationScope);
          },
          (channel, mappings) => persistChannelMappings(channel, mappings, mutationScope)
        );

        assertCurrent();
        showNotification(t('oauth_model_alias.save_success'), 'success');
      });
    },
    [persistChannelMappings, runModelAliasMutation, showNotification, t]
  );

  const handleDeleteAlias = useCallback(
    (aliasName: string) => {
      const aliasTrim = aliasName.trim();
      if (!aliasTrim) return;
      const aliasKey = aliasTrim.toLowerCase();

      showConfirmation({
        title: t('oauth_model_alias.delete_alias_title', { defaultValue: 'Delete Alias' }),
        message: (
          <Trans
            i18nKey="oauth_model_alias.delete_alias_confirm"
            values={{ alias: aliasTrim }}
            components={{ code: <code /> }}
          />
        ),
        variant: 'danger',
        confirmText: t('common.confirm'),
        onConfirm: async () => {
          await runModelAliasMutation(async (assertCurrent, mutationScope) => {
            const latest = await authFilesApi.getOauthModelAlias(mutationScope);
            assertCurrent();
            const providersToUpdate = Object.entries(latest).filter(([, mappings]) =>
              mappings.some((mapping) => (mapping.alias ?? '').trim().toLowerCase() === aliasKey)
            );
            if (providersToUpdate.length === 0) return;

            await applyOAuthAliasWritePlans(
              providersToUpdate.map(([channel, mappings]) => ({
                channel,
                previousMappings: mappings,
                nextMappings: mappings.filter(
                  (mapping) => (mapping.alias ?? '').trim().toLowerCase() !== aliasKey
                ),
              })),
              async (channel, mappings) => {
                assertCurrent();
                await persistChannelMappings(channel, mappings, mutationScope);
              },
              (channel, mappings) => persistChannelMappings(channel, mappings, mutationScope)
            );

            assertCurrent();
            showNotification(t('oauth_model_alias.delete_success'), 'success');
          });
        },
      });
    },
    [persistChannelMappings, runModelAliasMutation, showConfirmation, showNotification, t]
  );

  return {
    excluded: scopedExcluded,
    excludedLoaded: stateConnectionKey === normalizedConnectionKey && excludedLoaded,
    excludedError: stateConnectionKey === normalizedConnectionKey ? excludedError : 'loading',
    modelAlias: scopedModelAlias,
    modelAliasLoaded: stateConnectionKey === normalizedConnectionKey && modelAliasLoaded,
    modelAliasError: stateConnectionKey === normalizedConnectionKey ? modelAliasError : 'loading',
    allProviderModels: scopedAllProviderModels,
    providerList,
    // Return the memoized callbacks directly. Wrapping them in new arrow functions
    // each render breaks credential-workspace init effects that depend on these refs and
    // causes an infinite GET loop (loader setState → re-render → new refs → effect).
    loadExcluded,
    loadModelAlias,
    deleteExcluded,
    deleteModelAlias,
    handleMappingUpdate,
    handleDeleteLink,
    handleToggleFork,
    handleRenameAlias,
    handleDeleteAlias,
  };
}
