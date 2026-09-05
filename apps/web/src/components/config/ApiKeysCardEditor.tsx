import { memo, useCallback, useEffect, useId, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Modal } from '@/components/ui/Modal';
import { usageServiceApi, type ApiKeyAlias } from '@/services/api/usageService';
import { useAuthStore, useNotificationStore } from '@/stores';
import { usePanelFeatureAvailability } from '@/hooks/usePanelFeatureAvailability';
import { copyToClipboard } from '@/utils/clipboard';
import { maskApiKey } from '@/utils/format';
import { sha256Hex } from '@/utils/apiKeyHash';
import { isValidApiKeyCharset } from '@/utils/validation';
import styles from './VisualConfigEditor.module.scss';

export type ApiKeyMutation =
  | {
      type: 'create';
      apiKey: string;
    }
  | {
      type: 'replace';
      oldApiKey: string;
      newApiKey: string;
    }
  | {
      type: 'delete';
      apiKey: string;
    };

type OrphanAliasConflict = {
  apiKeyHash: string;
  alias: string;
};

export const ApiKeysCardEditor = memo(function ApiKeysCardEditor({
  value,
  disabled,
  onPersistApiKeyMutation,
  onRefreshApiKeys,
  onApiKeyOperationStart,
  onApiKeyOperationEnd,
}: {
  value: string;
  disabled?: boolean;
  onPersistApiKeyMutation: (mutation: ApiKeyMutation) => Promise<string[]>;
  onRefreshApiKeys: () => Promise<string[]>;
  onApiKeyOperationStart: () => void;
  onApiKeyOperationEnd: () => void;
}) {
  const { t } = useTranslation();
  const showNotification = useNotificationStore((state) => state.showNotification);
  const showConfirmation = useNotificationStore((state) => state.showConfirmation);
  const managementKey = useAuthStore((state) => state.managementKey);
  const featureAvailability = usePanelFeatureAvailability();
  const apiKeys = useMemo(
    () =>
      value
        .split('\n')
        .map((key) => key.trim())
        .filter(Boolean),
    [value]
  );
  const apiKeyInputId = useId();
  const apiKeyHintId = `${apiKeyInputId}-hint`;
  const apiKeyErrorId = `${apiKeyInputId}-error`;
  const keyAliasInputId = `${apiKeyInputId}-alias`;
  const aliasModalInputId = useId();
  const aliasModalErrorId = `${aliasModalInputId}-error`;
  const [modalOpen, setModalOpen] = useState(false);
  const [editingApiKey, setEditingApiKey] = useState<string | null>(null);
  const [inputValue, setInputValue] = useState('');
  const [inputAliasValue, setInputAliasValue] = useState('');
  const [formError, setFormError] = useState('');
  const [apiKeyAliases, setApiKeyAliases] = useState<ApiKeyAlias[]>([]);
  const [aliasesLoading, setAliasesLoading] = useState(false);
  const [aliasesAvailable, setAliasesAvailable] = useState(false);
  const [aliasModalOpen, setAliasModalOpen] = useState(false);
  const [aliasEditingApiKey, setAliasEditingApiKey] = useState<string | null>(null);
  const [aliasInputValue, setAliasInputValue] = useState('');
  const [aliasFormError, setAliasFormError] = useState('');
  const [mutationSaving, setMutationSaving] = useState(false);
  const mutationInFlightRef = useRef(false);

  const aliasByHash = useMemo(() => {
    const map = new Map<string, ApiKeyAlias>();
    apiKeyAliases.forEach((item) => {
      const hash = String(item.apiKeyHash || '')
        .trim()
        .toLowerCase();
      const alias = String(item.alias || '').trim();
      if (!hash || !alias) return;
      map.set(hash, { ...item, apiKeyHash: hash, alias });
    });
    return map;
  }, [apiKeyAliases]);

  const aliasCapabilityChecking = featureAvailability.checking;
  const panelHostUnconfirmed = !featureAvailability.panelHostConfirmed;
  const managerHostedPanel = featureAvailability.panelHostMode === 'manager_embedded';
  const resolveAliasServiceBase = useCallback(
    async (): Promise<string> =>
      !aliasCapabilityChecking &&
      featureAvailability.panelHostConfirmed &&
      managerHostedPanel &&
      featureAvailability.managerServiceAvailable
        ? featureAvailability.managerServiceBase
        : '',
    [
      aliasCapabilityChecking,
      featureAvailability.managerServiceAvailable,
      featureAvailability.managerServiceBase,
      featureAvailability.panelHostConfirmed,
      managerHostedPanel,
    ]
  );
  const aliasServiceAvailable = Boolean(
    !aliasCapabilityChecking &&
    featureAvailability.panelHostConfirmed &&
    managerHostedPanel &&
    featureAvailability.managerServiceAvailable &&
    featureAvailability.managerServiceBase
  );

  useEffect(() => {
    let cancelled = false;

    const loadAliases = async () => {
      setAliasesLoading(true);
      try {
        const serviceBase = await resolveAliasServiceBase();
        if (cancelled) return;
        if (!serviceBase) {
          setAliasesAvailable(false);
          setApiKeyAliases([]);
          return;
        }
        const response = await usageServiceApi.getApiKeyAliases(serviceBase, managementKey);
        if (cancelled) return;
        setAliasesAvailable(true);
        setApiKeyAliases(Array.isArray(response.items) ? response.items : []);
      } catch {
        if (cancelled) return;
        setAliasesAvailable(false);
        setApiKeyAliases([]);
      } finally {
        if (!cancelled) {
          setAliasesLoading(false);
        }
      }
    };

    void loadAliases();

    return () => {
      cancelled = true;
    };
  }, [managementKey, resolveAliasServiceBase]);

  function generateSecureApiKey(): string {
    const charset = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789';
    const array = new Uint8Array(64);
    crypto.getRandomValues(array);
    return 'sk-' + Array.from(array, (b) => charset[b % charset.length]).join('');
  }

  const getApiKeyHash = (apiKey: string) => sha256Hex(apiKey).toLowerCase();

  const getAliasForApiKey = (apiKey: string) => {
    const hash = getApiKeyHash(apiKey);
    return hash ? (aliasByHash.get(hash)?.alias ?? '') : '';
  };

  const collectActiveApiKeyHashes = (keys: string[]) =>
    Array.from(
      new Set(
        keys
          .map((key) => getApiKeyHash(key))
          .map((hash) => hash.trim().toLowerCase())
          .filter(Boolean)
      )
    );

  const normalizeAliasKey = (alias: string) => alias.trim().toLowerCase();

  const isDuplicateAlias = (
    alias: string,
    currentApiKeyHash: string,
    activeApiKeyHashes?: string[]
  ) => {
    const aliasKey = normalizeAliasKey(alias);
    const currentHash = currentApiKeyHash.trim().toLowerCase();
    const activeHashSet =
      activeApiKeyHashes && activeApiKeyHashes.length > 0
        ? new Set(activeApiKeyHashes.map((hash) => hash.trim().toLowerCase()).filter(Boolean))
        : null;
    if (!aliasKey) return false;
    return apiKeyAliases.some((item) => {
      const itemHash = String(item.apiKeyHash || '')
        .trim()
        .toLowerCase();
      if (activeHashSet && !activeHashSet.has(itemHash)) return false;
      return itemHash !== currentHash && normalizeAliasKey(String(item.alias || '')) === aliasKey;
    });
  };

  const findOrphanAliasConflict = (
    alias: string,
    currentApiKeyHash: string,
    activeApiKeyHashes?: string[]
  ): OrphanAliasConflict | null => {
    const aliasKey = normalizeAliasKey(alias);
    const currentHash = currentApiKeyHash.trim().toLowerCase();
    if (!aliasKey || !activeApiKeyHashes || activeApiKeyHashes.length === 0) return null;

    const activeHashSet = new Set(
      activeApiKeyHashes.map((hash) => hash.trim().toLowerCase()).filter(Boolean)
    );

    for (const item of apiKeyAliases) {
      const itemHash = String(item.apiKeyHash || '')
        .trim()
        .toLowerCase();
      const itemAlias = String(item.alias || '').trim();
      if (!itemHash || itemHash === currentHash || activeHashSet.has(itemHash)) continue;
      if (normalizeAliasKey(itemAlias) === aliasKey) {
        return { apiKeyHash: itemHash, alias: itemAlias };
      }
    }

    return null;
  };

  const createAliasUnavailableError = (translationKey: string) => {
    const error = new Error(t(translationKey)) as Error & { code?: string };
    error.code = 'api_key_alias_unavailable';
    return error;
  };

  const requestOrphanAliasCleanup = async (
    alias: string,
    currentApiKeyHash: string,
    activeApiKeyHashes?: string[]
  ): Promise<{ shouldContinue: boolean; allowOrphanAliasCleanup: boolean }> => {
    const conflict = findOrphanAliasConflict(alias, currentApiKeyHash, activeApiKeyHashes);
    if (!conflict) {
      return { shouldContinue: true, allowOrphanAliasCleanup: false };
    }

    const confirmed = await new Promise<boolean>((resolve) => {
      showConfirmation({
        title: t('config_management.visual.api_keys.alias_cleanup_title'),
        message: (
          <>
            <p style={{ margin: '0 0 0.75rem' }}>
              {t('config_management.visual.api_keys.alias_cleanup_confirm', {
                alias: conflict.alias,
              })}
            </p>
            <p style={{ margin: 0 }}>
              {t('config_management.visual.api_keys.alias_cleanup_risk', {
                hash: conflict.apiKeyHash.slice(0, 12),
              })}
            </p>
          </>
        ),
        confirmText: t('config_management.visual.api_keys.alias_cleanup_confirm_action'),
        variant: 'danger',
        onConfirm: () => resolve(true),
        onCancel: () => resolve(false),
      });
    });

    return { shouldContinue: confirmed, allowOrphanAliasCleanup: confirmed };
  };

  const validateAlias = (
    alias: string,
    currentApiKeyHash: string = '',
    activeApiKeyHashes?: string[]
  ) => {
    const trimmed = alias.trim();
    if (!trimmed) {
      return t('config_management.visual.api_keys.alias_error_empty');
    }
    if (Array.from(trimmed).length > 120) {
      return t('config_management.visual.api_keys.alias_error_too_long');
    }
    if (isDuplicateAlias(trimmed, currentApiKeyHash, activeApiKeyHashes)) {
      return t('config_management.visual.api_keys.alias_error_duplicate');
    }
    return '';
  };

  const saveAliasForKey = async (
    apiKey: string,
    alias: string,
    activeApiKeyHashes?: string[],
    allowOrphanAliasCleanup = false
  ) => {
    const apiKeyHash = getApiKeyHash(apiKey);
    const trimmedAlias = alias.trim();
    if (!apiKeyHash) {
      throw new Error(t('config_management.visual.api_keys.error_empty'));
    }
    const validationError = validateAlias(trimmedAlias, apiKeyHash, activeApiKeyHashes);
    if (validationError) {
      throw new Error(validationError);
    }

    const serviceBase = await resolveAliasServiceBase();
    if (!serviceBase) {
      throw createAliasUnavailableError('config_management.visual.api_keys.alias_unavailable');
    }

    const response = await usageServiceApi.saveApiKeyAliases(
      serviceBase,
      [{ apiKeyHash, alias: trimmedAlias }],
      managementKey,
      activeApiKeyHashes,
      allowOrphanAliasCleanup
    );
    setAliasesAvailable(true);
    setApiKeyAliases(Array.isArray(response.items) ? response.items : []);
  };

  const deleteAliasForHash = async (apiKeyHash: string) => {
    const serviceBase = await resolveAliasServiceBase();
    if (!serviceBase) {
      throw createAliasUnavailableError('config_management.visual.api_keys.alias_unavailable');
    }

    await usageServiceApi.deleteApiKeyAlias(serviceBase, apiKeyHash, managementKey);
    setApiKeyAliases((previous) =>
      previous.filter((item) => item.apiKeyHash.toLowerCase() !== apiKeyHash.toLowerCase())
    );
  };

  const getAliasErrorMessage = (error: unknown) => {
    if (
      error &&
      typeof error === 'object' &&
      (error as { code?: unknown }).code === 'api_key_alias_duplicate'
    ) {
      return t('config_management.visual.api_keys.alias_error_duplicate');
    }
    return error instanceof Error ? error.message : String(error);
  };

  const openAddModal = () => {
    setEditingApiKey(null);
    setInputValue('');
    setInputAliasValue('');
    setFormError('');
    setModalOpen(true);
  };

  const openEditModal = (apiKey: string) => {
    setEditingApiKey(apiKey);
    setInputValue(apiKey);
    setInputAliasValue(getAliasForApiKey(apiKey));
    setFormError('');
    setModalOpen(true);
  };

  const closeModal = () => {
    setModalOpen(false);
    setInputValue('');
    setInputAliasValue('');
    setEditingApiKey(null);
    setFormError('');
  };

  const openAliasModal = (apiKey: string) => {
    setAliasEditingApiKey(apiKey);
    setAliasInputValue(getAliasForApiKey(apiKey));
    setAliasFormError('');
    setAliasModalOpen(true);
  };

  const closeAliasModal = () => {
    setAliasModalOpen(false);
    setAliasEditingApiKey(null);
    setAliasInputValue('');
    setAliasFormError('');
  };

  const getPersistenceErrorMessage = (error: unknown) => {
    const code =
      error && typeof error === 'object' && 'code' in error
        ? String((error as { code?: unknown }).code || '')
        : '';
    const message = error instanceof Error ? error.message : String(error);
    if (
      code === 'source_config_dirty' ||
      code === 'api_key_operation_busy' ||
      code === 'api_key_duplicate' ||
      code === 'api_key_ambiguous' ||
      code === 'api_key_state_refresh_failed' ||
      code === 'api_key_mutation_outcome_unknown' ||
      code === 'api_key_alias_unavailable'
    ) {
      return message;
    }
    return `${t('config_management.visual.api_keys.save_failed')}: ${message}`;
  };

  const beginMutation = (onError?: (message: string) => void) => {
    if (mutationInFlightRef.current) return false;
    mutationInFlightRef.current = true;
    setMutationSaving(true);
    try {
      onApiKeyOperationStart();
      return true;
    } catch (error) {
      const message = getPersistenceErrorMessage(error);
      if (onError) {
        onError(message);
      } else {
        showNotification(message, 'error');
      }
      mutationInFlightRef.current = false;
      setMutationSaving(false);
      return false;
    }
  };

  const endMutation = () => {
    if (!mutationInFlightRef.current) return;
    mutationInFlightRef.current = false;
    setMutationSaving(false);
    onApiKeyOperationEnd();
  };

  const hasCanonicalApiKey = (keys: string[], apiKey: string) =>
    keys.some((key) => key.trim() === apiKey.trim());

  const handleDelete = (apiKey: string) => {
    if (disabled || mutationInFlightRef.current) return;
    if (
      aliasCapabilityChecking ||
      panelHostUnconfirmed ||
      (managerHostedPanel && !aliasServiceAvailable)
    ) {
      showNotification(t('config_management.visual.api_keys.alias_state_unavailable'), 'warning');
      return;
    }

    showConfirmation({
      title: t('config_management.visual.api_keys.delete_title'),
      message: t('config_management.visual.api_keys.delete_confirm'),
      confirmText: t('config_management.visual.common.delete'),
      cancelText: t('config_management.visual.common.cancel'),
      variant: 'danger',
      onConfirm: async () => {
        if (!beginMutation()) return;
        try {
          await onPersistApiKeyMutation({ type: 'delete', apiKey });
          const apiKeyHash = getApiKeyHash(apiKey);
          if (apiKeyHash && aliasServiceAvailable) {
            try {
              await deleteAliasForHash(apiKeyHash);
            } catch {
              showNotification(
                t('config_management.visual.api_keys.delete_partial_success'),
                'warning'
              );
              return;
            }
          }
          showNotification(t('config_management.visual.api_keys.deleted'), 'success');
        } catch (error) {
          showNotification(getPersistenceErrorMessage(error), 'error');
        } finally {
          endMutation();
        }
      },
    });
  };

  const handleSave = async () => {
    if (mutationInFlightRef.current) return;
    const trimmed = inputValue.trim();
    const trimmedAlias = inputAliasValue.trim();
    if (!trimmed) {
      setFormError(t('config_management.visual.api_keys.error_empty'));
      return;
    }
    if (!isValidApiKeyCharset(trimmed)) {
      setFormError(t('config_management.visual.api_keys.error_invalid'));
      return;
    }
    const oldApiKey = editingApiKey;
    const isCreate = oldApiKey === null;
    const isReplace = oldApiKey !== null && oldApiKey !== trimmed;
    if (isCreate && apiKeys.some((key) => key === trimmed)) {
      setFormError(t('config_management.visual.api_keys.error_duplicate'));
      return;
    }
    if (isReplace && apiKeys.some((key) => key !== oldApiKey && key === trimmed)) {
      setFormError(t('config_management.visual.api_keys.error_duplicate'));
      return;
    }
    if (
      isReplace &&
      (aliasCapabilityChecking ||
        panelHostUnconfirmed ||
        (managerHostedPanel && (!aliasServiceAvailable || aliasesLoading || !aliasesAvailable)))
    ) {
      const error = createAliasUnavailableError(
        'config_management.visual.api_keys.alias_state_unavailable'
      );
      setFormError(getPersistenceErrorMessage(error));
      return;
    }
    if (trimmedAlias && !aliasesAvailable) {
      setFormError(t('config_management.visual.api_keys.alias_unavailable'));
      return;
    }
    if (!beginMutation(setFormError)) return;

    try {
      const oldAlias = oldApiKey ? getAliasForApiKey(oldApiKey) : '';
      if (!isCreate && !isReplace) {
        const canonicalKeys = await onRefreshApiKeys();
        if (!hasCanonicalApiKey(canonicalKeys, trimmed)) {
          showNotification(t('config_management.visual.api_keys.stale_key_refreshed'), 'warning');
          closeModal();
          return;
        }

        const activeApiKeyHashes = collectActiveApiKeyHashes(canonicalKeys);
        const aliasError = validateAlias(trimmedAlias, getApiKeyHash(trimmed), activeApiKeyHashes);
        if (aliasError) {
          setFormError(aliasError);
          return;
        }
        if (normalizeAliasKey(trimmedAlias) === normalizeAliasKey(oldAlias)) {
          closeModal();
          return;
        }

        const cleanupDecision = await requestOrphanAliasCleanup(
          trimmedAlias,
          getApiKeyHash(trimmed),
          activeApiKeyHashes
        );
        if (!cleanupDecision.shouldContinue) {
          setFormError(t('config_management.visual.api_keys.alias_cleanup_cancelled'));
          return;
        }
        try {
          await saveAliasForKey(
            trimmed,
            trimmedAlias,
            activeApiKeyHashes,
            cleanupDecision.allowOrphanAliasCleanup
          );
          showNotification(t('config_management.visual.api_keys.alias_saved'), 'success');
          closeModal();
        } catch (error) {
          setFormError(getAliasErrorMessage(error));
        }
        return;
      }

      const predictedKeys = isCreate
        ? [...apiKeys, trimmed]
        : apiKeys.map((key) => (key === oldApiKey ? trimmed : key));
      const predictedApiKeyHashes = collectActiveApiKeyHashes(predictedKeys);
      const aliasError = trimmedAlias
        ? validateAlias(trimmedAlias, getApiKeyHash(trimmed), predictedApiKeyHashes)
        : '';
      if (aliasError) {
        setFormError(aliasError);
        return;
      }

      const isExpectedAliasMigration =
        isReplace &&
        Boolean(oldAlias) &&
        normalizeAliasKey(oldAlias) === normalizeAliasKey(trimmedAlias);
      let allowOrphanAliasCleanup = false;
      if (trimmedAlias && !isExpectedAliasMigration) {
        const cleanupDecision = await requestOrphanAliasCleanup(
          trimmedAlias,
          getApiKeyHash(trimmed),
          predictedApiKeyHashes
        );
        if (!cleanupDecision.shouldContinue) {
          setFormError(t('config_management.visual.api_keys.alias_cleanup_cancelled'));
          return;
        }
        allowOrphanAliasCleanup = cleanupDecision.allowOrphanAliasCleanup;
      }

      const canonicalKeys = await onPersistApiKeyMutation(
        isCreate
          ? { type: 'create', apiKey: trimmed }
          : { type: 'replace', oldApiKey: oldApiKey!, newApiKey: trimmed }
      );
      if (!hasCanonicalApiKey(canonicalKeys, trimmed)) {
        setFormError(t('config_management.visual.api_keys.canonical_key_missing'));
        return;
      }

      const activeApiKeyHashes = collectActiveApiKeyHashes(canonicalKeys);
      const newApiKeyHash = getApiKeyHash(trimmed);
      const oldApiKeyHash = oldApiKey ? getApiKeyHash(oldApiKey) : '';
      try {
        if (trimmedAlias) {
          await saveAliasForKey(
            trimmed,
            trimmedAlias,
            activeApiKeyHashes,
            isExpectedAliasMigration || allowOrphanAliasCleanup
          );
        } else if (isReplace && oldApiKeyHash && oldAlias) {
          await deleteAliasForHash(oldApiKeyHash);
        }
        if (
          isReplace &&
          oldApiKeyHash &&
          oldAlias &&
          oldApiKeyHash !== newApiKeyHash &&
          trimmedAlias &&
          !isExpectedAliasMigration
        ) {
          await deleteAliasForHash(oldApiKeyHash);
        }
      } catch {
        showNotification(
          isReplace
            ? t('config_management.visual.api_keys.update_partial_success')
            : t('config_management.visual.api_keys.save_partial_success'),
          'warning'
        );
        closeModal();
        return;
      }

      showNotification(
        t(
          isCreate
            ? 'config_management.visual.api_keys.added'
            : 'config_management.visual.api_keys.updated'
        ),
        'success'
      );
      closeModal();
    } catch (error) {
      const code =
        error && typeof error === 'object' && 'code' in error
          ? String((error as { code?: unknown }).code || '')
          : '';
      if (code === 'api_key_stale') {
        showNotification(t('config_management.visual.api_keys.stale_key_refreshed'), 'warning');
        closeModal();
        return;
      }
      setFormError(getPersistenceErrorMessage(error));
    } finally {
      endMutation();
    }
  };

  const handleAliasSave = async () => {
    if (mutationInFlightRef.current || !aliasEditingApiKey) return;
    const editingKey = aliasEditingApiKey;
    if (!aliasInputValue.trim()) {
      setAliasFormError(t('config_management.visual.api_keys.alias_error_empty'));
      return;
    }
    if (
      aliasCapabilityChecking ||
      panelHostUnconfirmed ||
      (managerHostedPanel && !aliasServiceAvailable)
    ) {
      setAliasFormError(t('config_management.visual.api_keys.alias_state_unavailable'));
      return;
    }
    if (!aliasesAvailable) {
      setAliasFormError(t('config_management.visual.api_keys.alias_unavailable'));
      return;
    }
    if (!beginMutation(setAliasFormError)) return;
    try {
      const canonicalKeys = await onRefreshApiKeys();
      if (!hasCanonicalApiKey(canonicalKeys, editingKey)) {
        showNotification(t('config_management.visual.api_keys.stale_key_refreshed'), 'warning');
        closeAliasModal();
        return;
      }
      const activeApiKeyHashes = collectActiveApiKeyHashes(canonicalKeys);
      const aliasError = validateAlias(
        aliasInputValue,
        getApiKeyHash(editingKey),
        activeApiKeyHashes
      );
      if (aliasError) {
        setAliasFormError(aliasError);
        return;
      }

      const cleanupDecision = await requestOrphanAliasCleanup(
        aliasInputValue,
        getApiKeyHash(editingKey),
        activeApiKeyHashes
      );
      if (!cleanupDecision.shouldContinue) {
        setAliasFormError(t('config_management.visual.api_keys.alias_cleanup_cancelled'));
        return;
      }

      await saveAliasForKey(
        editingKey,
        aliasInputValue,
        activeApiKeyHashes,
        cleanupDecision.allowOrphanAliasCleanup
      );
      showNotification(t('config_management.visual.api_keys.alias_saved'), 'success');
      closeAliasModal();
    } catch (error) {
      setAliasFormError(getAliasErrorMessage(error));
    } finally {
      endMutation();
    }
  };

  const handleAliasDelete = () => {
    const editingKey = aliasEditingApiKey ?? '';
    const apiKeyHash = getApiKeyHash(editingKey);
    if (
      aliasCapabilityChecking ||
      panelHostUnconfirmed ||
      (managerHostedPanel && !aliasServiceAvailable)
    ) {
      setAliasFormError(t('config_management.visual.api_keys.alias_state_unavailable'));
      return;
    }
    if (!apiKeyHash || !aliasByHash.has(apiKeyHash)) return;

    showConfirmation({
      title: t('config_management.visual.api_keys.alias_delete_title'),
      message: t('config_management.visual.api_keys.alias_delete_confirm'),
      confirmText: t('config_management.visual.api_keys.alias_delete'),
      variant: 'danger',
      onConfirm: async () => {
        if (!beginMutation(setAliasFormError)) return;
        try {
          await deleteAliasForHash(apiKeyHash);
          showNotification(t('config_management.visual.api_keys.alias_deleted'), 'success');
          closeAliasModal();
        } catch (error) {
          setAliasFormError(getAliasErrorMessage(error));
        } finally {
          endMutation();
        }
      },
    });
  };

  const handleCopy = async (apiKey: string) => {
    const copied = await copyToClipboard(apiKey);
    showNotification(
      t(copied ? 'notification.link_copied' : 'notification.copy_failed'),
      copied ? 'success' : 'error'
    );
  };

  const handleGenerate = () => {
    setInputValue(generateSecureApiKey());
    setFormError('');
  };

  return (
    <div className="form-group" style={{ marginBottom: 0 }}>
      <div className={styles.blockHeaderRow}>
        <label style={{ margin: 0 }}>{t('config_management.visual.api_keys.label')}</label>
        <Button size="sm" onClick={openAddModal} disabled={disabled || mutationSaving}>
          {t('config_management.visual.api_keys.add')}
        </Button>
      </div>

      {apiKeys.length === 0 ? (
        <div className={styles.emptyState}>{t('config_management.visual.api_keys.empty')}</div>
      ) : (
        <div className="item-list" style={{ marginTop: 4 }}>
          {apiKeys.map((key, index) => {
            const apiKeyHash = getApiKeyHash(key);
            const alias = apiKeyHash ? (aliasByHash.get(apiKeyHash)?.alias ?? '') : '';
            return (
              <div key={`${key}-${index}`} className="item-row">
                <div className="item-meta">
                  <div className="item-title">
                    {alias || t('config_management.visual.api_keys.input_label')}
                  </div>
                  <div className="item-subtitle">{maskApiKey(String(key || ''))}</div>
                </div>
                <div className="item-actions">
                  <Button
                    variant="secondary"
                    size="xs"
                    onClick={() => openAliasModal(key)}
                    disabled={disabled || mutationSaving || aliasesLoading || !aliasesAvailable}
                  >
                    {t('config_management.visual.api_keys.alias_action')}
                  </Button>
                  <Button
                    variant="secondary"
                    size="xs"
                    onClick={() => handleCopy(key)}
                    disabled={disabled || mutationSaving}
                  >
                    {t('common.copy')}
                  </Button>
                  <Button
                    variant="secondary"
                    size="xs"
                    onClick={() => openEditModal(key)}
                    disabled={disabled || mutationSaving}
                  >
                    {t('config_management.visual.common.edit')}
                  </Button>
                  <Button
                    variant="danger"
                    size="xs"
                    onClick={() => handleDelete(key)}
                    disabled={disabled || mutationSaving}
                  >
                    {t('config_management.visual.common.delete')}
                  </Button>
                </div>
              </div>
            );
          })}
        </div>
      )}

      <div className="hint">{t('config_management.visual.api_keys.hint')}</div>
      {!aliasesAvailable && !aliasesLoading ? (
        <div className="hint">{t('config_management.visual.api_keys.alias_unavailable')}</div>
      ) : null}

      <Modal
        open={modalOpen}
        onClose={closeModal}
        closeDisabled={disabled || mutationSaving}
        title={
          editingApiKey !== null
            ? t('config_management.visual.api_keys.edit_title')
            : t('config_management.visual.api_keys.add_title')
        }
        footer={
          <>
            <Button variant="secondary" onClick={closeModal} disabled={disabled || mutationSaving}>
              {t('config_management.visual.common.cancel')}
            </Button>
            <Button
              onClick={handleSave}
              disabled={disabled || mutationSaving}
              loading={mutationSaving}
            >
              {mutationSaving
                ? t('config_management.visual.api_keys.saving')
                : editingApiKey !== null
                  ? t('config_management.visual.common.update')
                  : t('config_management.visual.common.add')}
            </Button>
          </>
        }
      >
        <div className="form-group">
          <label htmlFor={apiKeyInputId}>
            {t('config_management.visual.api_keys.input_label')}
          </label>
          <div className={styles.apiKeyModalInputRow}>
            <input
              id={apiKeyInputId}
              className="input"
              placeholder={t('config_management.visual.api_keys.input_placeholder')}
              value={inputValue}
              onChange={(e) => setInputValue(e.target.value)}
              disabled={disabled || mutationSaving}
              aria-describedby={formError ? `${apiKeyErrorId} ${apiKeyHintId}` : apiKeyHintId}
              aria-invalid={Boolean(formError)}
            />
            <Button
              type="button"
              variant="secondary"
              size="sm"
              onClick={handleGenerate}
              disabled={disabled || mutationSaving}
            >
              {t('config_management.visual.api_keys.generate')}
            </Button>
          </div>
          <div id={apiKeyHintId} className="hint">
            {t('config_management.visual.api_keys.input_hint')}
          </div>
          <div className="form-group">
            <label htmlFor={keyAliasInputId}>
              {t('config_management.visual.api_keys.alias_label')}
            </label>
            <input
              id={keyAliasInputId}
              className="input"
              placeholder={t('config_management.visual.api_keys.alias_placeholder')}
              value={inputAliasValue}
              onChange={(e) => setInputAliasValue(e.target.value)}
              disabled={disabled || mutationSaving || aliasesLoading || !aliasesAvailable}
              maxLength={120}
            />
            <div className="hint">{t('config_management.visual.api_keys.alias_hint')}</div>
          </div>
          {formError && (
            <div id={apiKeyErrorId} className="error-box">
              {formError}
            </div>
          )}
        </div>
      </Modal>

      <Modal
        open={aliasModalOpen}
        onClose={closeAliasModal}
        closeDisabled={disabled || mutationSaving}
        title={t('config_management.visual.api_keys.alias_title')}
        footer={
          <>
            {aliasEditingApiKey && aliasByHash.has(getApiKeyHash(aliasEditingApiKey)) ? (
              <Button
                variant="danger"
                onClick={handleAliasDelete}
                disabled={disabled || mutationSaving}
              >
                {t('config_management.visual.api_keys.alias_delete')}
              </Button>
            ) : null}
            <Button
              variant="secondary"
              onClick={closeAliasModal}
              disabled={disabled || mutationSaving}
            >
              {t('config_management.visual.common.cancel')}
            </Button>
            <Button
              onClick={handleAliasSave}
              disabled={disabled || mutationSaving}
              loading={mutationSaving}
            >
              {mutationSaving
                ? t('config_management.visual.api_keys.saving')
                : t('config_management.visual.common.update')}
            </Button>
          </>
        }
      >
        <div className="form-group">
          <label htmlFor={aliasModalInputId}>
            {t('config_management.visual.api_keys.alias_label')}
          </label>
          <input
            id={aliasModalInputId}
            className="input"
            placeholder={t('config_management.visual.api_keys.alias_placeholder')}
            value={aliasInputValue}
            onChange={(e) => {
              setAliasInputValue(e.target.value);
              setAliasFormError('');
            }}
            disabled={disabled || mutationSaving}
            maxLength={120}
            aria-describedby={aliasFormError ? aliasModalErrorId : undefined}
            aria-invalid={Boolean(aliasFormError)}
          />
          <div className="hint">{t('config_management.visual.api_keys.alias_hint')}</div>
          {aliasFormError && (
            <div id={aliasModalErrorId} className="error-box">
              {aliasFormError}
            </div>
          )}
        </div>
      </Modal>
    </div>
  );
});
