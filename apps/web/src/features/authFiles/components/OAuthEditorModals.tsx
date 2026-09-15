import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { AutocompleteInput } from '@/components/ui/AutocompleteInput';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { Input } from '@/components/ui/Input';
import { LoadingSpinner } from '@/components/ui/LoadingSpinner';
import { Modal } from '@/components/ui/Modal';
import { SelectionCheckbox } from '@/components/ui/SelectionCheckbox';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { IconInfo, IconX } from '@/components/ui/icons';
import { authFilesApi } from '@/services/api';
import type { AuthFilesApiRequestScope } from '@/services/api/authFiles';
import { useNotificationStore } from '@/stores';
import type { AuthFileItem, OAuthModelAliasEntry } from '@/types';
import {
  getTypeLabel,
  normalizeProviderKey,
  type AuthFileModelItem,
} from '@/features/authFiles/constants';
import { isOAuthAliasDraftDirty } from '@/features/authFiles/oauthEditorState';
import { normalizeOAuthAliasEntries } from '@/features/authFiles/oauthAliasValidation';
import {
  addOAuthExcludedRule,
  serializeOAuthExcludedRules,
} from '@/features/authFiles/oauthExcludedRules';
import {
  buildOAuthAliasModelOptions,
  buildOAuthExcludedModelOptions,
} from '@/features/authFiles/oauthProviderModelOptions';
import { generateId } from '@/utils/helpers';
import styles from './OAuthEditorModals.module.scss';

type OAuthModelMappingFormEntry = OAuthModelAliasEntry & { id: string };
type ProviderModelsError = 'unsupported' | 'failed' | null;
type ProviderModelsSnapshot = {
  requestScopeKey: string;
  providerKey: string;
  models: AuthFileModelItem[];
  loading: boolean;
  error: ProviderModelsError;
};

type OAuthEditorBaseProps = {
  open: boolean;
  provider?: string;
  files: AuthFileItem[];
  excluded: Record<string, string[]>;
  modelAlias: Record<string, OAuthModelAliasEntry[]>;
  requestScope: AuthFilesApiRequestScope;
  disabled?: boolean;
  unsupported?: boolean;
  onClose: () => void;
  onSaved: () => Promise<void> | void;
};

const OAUTH_PROVIDER_PRESETS = [
  'vertex',
  'aistudio',
  'antigravity',
  'claude',
  'codex',
  'xai',
  'qwen',
  'kimi',
  'iflow',
  'qoder',
];

const OAUTH_PROVIDER_EXCLUDES = new Set(['all', 'unknown', 'empty']);

const buildEmptyMappingEntry = (): OAuthModelMappingFormEntry => ({
  id: generateId(),
  name: '',
  alias: '',
  fork: true,
  displayName: '',
  forceMapping: false,
});

const normalizeMappingEntries = (
  entries?: OAuthModelAliasEntry[]
): OAuthModelMappingFormEntry[] => {
  if (!Array.isArray(entries) || entries.length === 0) {
    return [buildEmptyMappingEntry()];
  }
  return entries.map((entry) => ({
    id: generateId(),
    name: entry.name ?? '',
    alias: entry.alias ?? '',
    fork: Boolean(entry.fork),
    displayName: entry.displayName ?? '',
    forceMapping: Boolean(entry.forceMapping),
  }));
};

const findProviderEntries = <T,>(record: Record<string, T>, providerKey: string): T | undefined => {
  const entry = Object.entries(record).find(
    ([provider]) => normalizeProviderKey(provider) === providerKey
  );
  return entry?.[1];
};

const readErrorStatus = (err: unknown) =>
  typeof err === 'object' && err !== null && 'status' in err
    ? (err as { status?: unknown }).status
    : undefined;

function useProviderOptions({
  files,
  excluded,
  modelAlias,
}: {
  files: AuthFileItem[];
  excluded: Record<string, string[]>;
  modelAlias: Record<string, OAuthModelAliasEntry[]>;
}) {
  return useMemo(() => {
    const extraProviders = new Set<string>();
    Object.keys(excluded).forEach((value) => extraProviders.add(value));
    Object.keys(modelAlias).forEach((value) => extraProviders.add(value));
    files.forEach((file) => {
      if (typeof file.type === 'string') {
        extraProviders.add(file.type);
      }
      if (typeof file.provider === 'string') {
        extraProviders.add(file.provider);
      }
    });

    const normalizedExtras = Array.from(extraProviders)
      .map((value) => value.trim())
      .filter((value) => value && !OAUTH_PROVIDER_EXCLUDES.has(value.toLowerCase()));

    const baseSet = new Set(OAUTH_PROVIDER_PRESETS.map((value) => value.toLowerCase()));
    const extraList = normalizedExtras
      .filter((value) => !baseSet.has(value.toLowerCase()))
      .sort((a, b) => a.localeCompare(b));

    return [...OAUTH_PROVIDER_PRESETS, ...extraList];
  }, [excluded, files, modelAlias]);
}

function useProviderModels({
  open,
  providerKey,
  requestScope,
  disabled,
  unsupported,
}: {
  open: boolean;
  providerKey: string;
  requestScope: AuthFilesApiRequestScope;
  disabled?: boolean;
  unsupported?: boolean;
}) {
  const { t } = useTranslation();
  const showNotification = useNotificationStore((state) => state.showNotification);
  const requestScopeKey = `${requestScope.apiBase}\u0000${requestScope.managementKey}`;
  const [snapshot, setSnapshot] = useState<ProviderModelsSnapshot>({
    requestScopeKey: '',
    providerKey: '',
    models: [],
    loading: false,
    error: null,
  });
  const active = open && Boolean(providerKey) && !unsupported && !disabled;

  useEffect(() => {
    if (!active) return;

    let cancelled = false;

    const loadModels = async () => {
      try {
        const items = await authFilesApi.getModelDefinitions(providerKey, requestScope);
        if (cancelled) return;
        setSnapshot({ requestScopeKey, providerKey, models: items, loading: false, error: null });
      } catch (err: unknown) {
        if (cancelled) return;
        if (readErrorStatus(err) === 404) {
          setSnapshot({
            requestScopeKey,
            providerKey,
            models: [],
            loading: false,
            error: 'unsupported',
          });
          return;
        }
        setSnapshot({ requestScopeKey, providerKey, models: [], loading: false, error: 'failed' });
        const message = err instanceof Error ? err.message : '';
        showNotification(`${t('notification.load_failed')}: ${message}`, 'error');
      }
    };

    void loadModels();

    return () => {
      cancelled = true;
    };
  }, [active, providerKey, requestScope, requestScopeKey, showNotification, t]);

  if (!active) {
    return { models: [], loading: false, error: null };
  }
  if (snapshot.requestScopeKey !== requestScopeKey || snapshot.providerKey !== providerKey) {
    return { models: [], loading: true, error: null };
  }
  return {
    models: snapshot.models,
    loading: snapshot.loading,
    error: snapshot.error,
  };
}

export function OAuthExcludedEditorModal({
  open,
  provider: initialProvider = '',
  files,
  excluded,
  modelAlias,
  requestScope,
  disabled = false,
  unsupported = false,
  onClose,
  onSaved,
}: OAuthEditorBaseProps) {
  const { t } = useTranslation();
  const showNotification = useNotificationStore((state) => state.showNotification);
  const showConfirmation = useNotificationStore((state) => state.showConfirmation);
  const [provider, setProvider] = useState(initialProvider);
  const [selectedModels, setSelectedModels] = useState<Set<string>>(new Set());
  const [customRule, setCustomRule] = useState('');
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (!open) return;
    setProvider(initialProvider);
    setCustomRule('');
  }, [initialProvider, open]);

  const providerOptions = useProviderOptions({ files, excluded, modelAlias });
  const resolvedProviderKey = useMemo(() => normalizeProviderKey(provider), [provider]);
  const existingRules = useMemo(
    () =>
      serializeOAuthExcludedRules(
        resolvedProviderKey ? (findProviderEntries(excluded, resolvedProviderKey) ?? []) : []
      ),
    [excluded, resolvedProviderKey]
  );
  const isEditing = resolvedProviderKey ? existingRules.length > 0 : false;
  const { models, loading, error } = useProviderModels({
    open,
    providerKey: resolvedProviderKey,
    requestScope,
    disabled,
    unsupported,
  });

  useEffect(() => {
    if (!open) return;
    setSelectedModels(new Set(existingRules));
    setCustomRule('');
  }, [existingRules, open]);

  const modelOptions = useMemo(
    () => buildOAuthExcludedModelOptions(models, existingRules),
    [existingRules, models]
  );
  const currentRules = useMemo(() => serializeOAuthExcludedRules(selectedModels), [selectedModels]);
  const draftRules = useMemo(
    () => serializeOAuthExcludedRules(addOAuthExcludedRule(selectedModels, customRule)),
    [customRule, selectedModels]
  );
  const unlistedRules = useMemo(() => {
    const optionIds = new Set(modelOptions.map((model) => model.id));
    return currentRules.filter((rule) => !optionIds.has(rule));
  }, [currentRules, modelOptions]);

  const toggleModel = useCallback((modelId: string, checked: boolean) => {
    setSelectedModels((prev) => {
      const next = new Set(prev);
      if (checked) {
        next.add(modelId);
      } else {
        next.delete(modelId);
      }
      return next;
    });
  }, []);

  const hasSelectionChanged = useMemo(() => {
    const current = draftRules;
    const existing = existingRules;
    return (
      current.length !== existing.length ||
      current.some((value, index) => value !== existing[index])
    );
  }, [draftRules, existingRules]);

  const handleProviderChange = useCallback(
    (value: string) => {
      if (hasSelectionChanged) return;
      const providerKey = normalizeProviderKey(value);
      const nextRules = serializeOAuthExcludedRules(
        providerKey ? (findProviderEntries(excluded, providerKey) ?? []) : []
      );
      setProvider(value);
      setSelectedModels(new Set(nextRules));
      setCustomRule('');
    },
    [excluded, hasSelectionChanged]
  );

  const requestClose = useCallback(() => {
    if (!hasSelectionChanged) {
      onClose();
      return;
    }
    showConfirmation({
      title: t('common.unsaved_changes_title'),
      message: t('common.unsaved_changes_message'),
      confirmText: t('common.leave'),
      cancelText: t('common.stay'),
      variant: 'danger',
      onConfirm: onClose,
    });
  }, [hasSelectionChanged, onClose, showConfirmation, t]);

  const handleSave = useCallback(async () => {
    const providerKey = normalizeProviderKey(provider);
    if (!providerKey) {
      showNotification(t('oauth_excluded.provider_required'), 'error');
      return;
    }

    setSaving(true);
    try {
      const modelIds = draftRules;
      if (modelIds.length > 0) {
        await authFilesApi.saveOauthExcludedModels(providerKey, modelIds, requestScope);
      } else {
        await authFilesApi.deleteOauthExcludedEntry(providerKey, requestScope);
      }
      await onSaved();
      showNotification(t('oauth_excluded.save_success'), 'success');
      onClose();
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : '';
      showNotification(`${t('oauth_excluded.save_failed')}: ${message}`, 'error');
    } finally {
      setSaving(false);
    }
  }, [draftRules, onClose, onSaved, provider, requestScope, showNotification, t]);

  const canSave =
    !disabled && !saving && !unsupported && Boolean(resolvedProviderKey) && hasSelectionChanged;
  const title = isEditing
    ? t('oauth_excluded.edit_title', { provider: provider.trim() || resolvedProviderKey })
    : t('oauth_excluded.add_title');

  return (
    <Modal
      open={open}
      onClose={requestClose}
      closeDisabled={saving}
      title={title}
      width={820}
      footer={
        <>
          <Button variant="secondary" onClick={requestClose} disabled={saving}>
            {t('common.cancel')}
          </Button>
          <Button onClick={() => void handleSave()} loading={saving} disabled={!canSave}>
            {t('oauth_excluded.save')}
          </Button>
        </>
      }
    >
      {unsupported ? (
        <EmptyState
          title={t('oauth_excluded.upgrade_required_title')}
          description={t('oauth_excluded.upgrade_required_desc')}
        />
      ) : (
        <div className={styles.editorBody}>
          <section className={styles.settingsBlock}>
            <div className={styles.settingsHeader}>
              <div className={styles.settingsHeaderTitle}>
                <IconInfo size={16} />
                <span>{t('oauth_excluded.title')}</span>
              </div>
              <div className={styles.settingsHeaderHint}>{t('oauth_excluded.description')}</div>
            </div>
            <div className={styles.settingsSection}>
              <div className={styles.settingsRow}>
                <div className={styles.settingsInfo}>
                  <div className={styles.settingsLabel}>{t('oauth_excluded.provider_label')}</div>
                  <div className={styles.settingsDesc}>{t('oauth_excluded.provider_hint')}</div>
                </div>
                <div className={styles.settingsControl}>
                  <AutocompleteInput
                    id="accounts-oauth-excluded-provider"
                    placeholder={t('oauth_excluded.provider_placeholder')}
                    value={provider}
                    onChange={handleProviderChange}
                    options={providerOptions}
                    disabled={disabled || saving || hasSelectionChanged}
                    wrapperStyle={{ marginBottom: 0 }}
                  />
                </div>
              </div>
              <div className={styles.tagList}>
                {providerOptions.map((option) => {
                  const active = normalizeProviderKey(provider) === normalizeProviderKey(option);
                  return (
                    <button
                      key={option}
                      type="button"
                      className={`${styles.tag} ${active ? styles.tagActive : ''}`}
                      onClick={() => handleProviderChange(option)}
                      disabled={disabled || saving || hasSelectionChanged}
                    >
                      {getTypeLabel(t, option)}
                    </button>
                  );
                })}
              </div>
            </div>
          </section>

          <section className={styles.settingsBlock}>
            <div className={styles.settingsHeader}>
              <div className={styles.settingsHeaderTitle}>{t('oauth_excluded.models_label')}</div>
              {resolvedProviderKey ? (
                <div className={styles.modelsHint}>
                  {loading ? (
                    <>
                      <LoadingSpinner size={14} />
                      <span>{t('oauth_excluded.models_loading')}</span>
                    </>
                  ) : error ? (
                    <span>
                      {error === 'unsupported'
                        ? t('oauth_excluded.models_unsupported')
                        : t('oauth_excluded.models_failed')}
                    </span>
                  ) : models.length > 0 ? (
                    <span>{t('oauth_excluded.models_loaded', { count: models.length })}</span>
                  ) : (
                    <span>{t('oauth_excluded.no_models_available')}</span>
                  )}
                </div>
              ) : null}
            </div>
            {modelOptions.length > 0 ? (
              <div className={styles.modelList}>
                {modelOptions.map((model) => (
                  <SelectionCheckbox
                    key={model.id}
                    checked={selectedModels.has(model.id)}
                    disabled={disabled || saving || !resolvedProviderKey}
                    onChange={(value) => toggleModel(model.id, value)}
                    className={styles.modelItem}
                    labelClassName={styles.modelText}
                    label={
                      <>
                        <span className={styles.modelId}>{model.id}</span>
                        {model.display_name && model.display_name !== model.id ? (
                          <span className={styles.modelDisplayName}>{model.display_name}</span>
                        ) : null}
                      </>
                    }
                  />
                ))}
              </div>
            ) : loading ? (
              <div className={styles.loadingModels}>
                <LoadingSpinner size={16} />
                <span>{t('common.loading')}</span>
              </div>
            ) : resolvedProviderKey ? (
              <div className={styles.emptyModels}>
                {error === 'unsupported'
                  ? t('oauth_excluded.models_unsupported')
                  : error === 'failed'
                    ? t('oauth_excluded.models_failed')
                    : t('oauth_excluded.no_models_available')}
              </div>
            ) : (
              <div className={styles.emptyModels}>{t('oauth_excluded.provider_required')}</div>
            )}
            <div className={styles.settingsSection}>
              <div className={styles.settingsRow}>
                <div className={styles.settingsInfo}>
                  <div className={styles.settingsLabel}>
                    {t('oauth_excluded.custom_rule_label')}
                  </div>
                  <div className={styles.settingsDesc}>{t('oauth_excluded.custom_rule_hint')}</div>
                </div>
                <div className={styles.settingsControl}>
                  <Input
                    value={customRule}
                    placeholder={t('oauth_excluded.custom_rule_placeholder')}
                    disabled={disabled || saving || !resolvedProviderKey}
                    onChange={(event) => setCustomRule(event.target.value)}
                    onKeyDown={(event) => {
                      if (event.key !== 'Enter') return;
                      event.preventDefault();
                      setSelectedModels((current) => addOAuthExcludedRule(current, customRule));
                      setCustomRule('');
                    }}
                  />
                </div>
              </div>
              <div className={styles.tagList}>
                {unlistedRules.map((rule) => (
                  <button
                    key={rule}
                    type="button"
                    className={styles.tag}
                    onClick={() => toggleModel(rule, false)}
                    disabled={disabled || saving}
                  >
                    {rule} <IconX size={12} />
                  </button>
                ))}
              </div>
            </div>
          </section>
        </div>
      )}
    </Modal>
  );
}

export function OAuthModelAliasEditorModal({
  open,
  provider: initialProvider = '',
  files,
  excluded,
  modelAlias,
  requestScope,
  disabled = false,
  unsupported = false,
  onClose,
  onSaved,
}: OAuthEditorBaseProps) {
  const { t } = useTranslation();
  const showNotification = useNotificationStore((state) => state.showNotification);
  const showConfirmation = useNotificationStore((state) => state.showConfirmation);
  const [provider, setProvider] = useState(initialProvider);
  const [mappings, setMappings] = useState<OAuthModelMappingFormEntry[]>([
    buildEmptyMappingEntry(),
  ]);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (!open) return;
    setProvider(initialProvider);
  }, [initialProvider, open]);

  const providerOptions = useProviderOptions({ files, excluded, modelAlias });
  const resolvedProviderKey = useMemo(() => normalizeProviderKey(provider), [provider]);
  const existingMappings = useMemo(
    () => (resolvedProviderKey ? (findProviderEntries(modelAlias, resolvedProviderKey) ?? []) : []),
    [modelAlias, resolvedProviderKey]
  );
  const { models, loading, error } = useProviderModels({
    open,
    providerKey: resolvedProviderKey,
    requestScope,
    disabled,
    unsupported,
  });
  const modelOptions = useMemo(
    () => buildOAuthAliasModelOptions(models, existingMappings),
    [existingMappings, models]
  );

  useEffect(() => {
    if (!open) return;
    setMappings(normalizeMappingEntries(existingMappings));
  }, [existingMappings, open]);

  const headerHint = useMemo(() => {
    if (!provider.trim()) {
      return t('oauth_model_alias.provider_hint');
    }
    if (loading) {
      return t('oauth_model_alias.model_source_loading');
    }
    if (error) {
      return error === 'unsupported'
        ? t('oauth_model_alias.model_source_unsupported')
        : t('oauth_model_alias.model_source_failed');
    }
    return t('oauth_model_alias.model_source_loaded', { count: models.length });
  }, [error, loading, models.length, provider, t]);

  const hasMappingsChanged = useMemo(
    () => isOAuthAliasDraftDirty(mappings, existingMappings),
    [existingMappings, mappings]
  );

  const handleProviderChange = useCallback(
    (value: string) => {
      if (hasMappingsChanged) return;
      const providerKey = normalizeProviderKey(value);
      const nextMappings = providerKey ? (findProviderEntries(modelAlias, providerKey) ?? []) : [];
      setProvider(value);
      setMappings(normalizeMappingEntries(nextMappings));
    },
    [hasMappingsChanged, modelAlias]
  );

  const requestClose = useCallback(() => {
    if (!hasMappingsChanged) {
      onClose();
      return;
    }
    showConfirmation({
      title: t('common.unsaved_changes_title'),
      message: t('common.unsaved_changes_message'),
      confirmText: t('common.leave'),
      cancelText: t('common.stay'),
      variant: 'danger',
      onConfirm: onClose,
    });
  }, [hasMappingsChanged, onClose, showConfirmation, t]);

  const updateMappingEntry = useCallback(
    (index: number, field: keyof OAuthModelAliasEntry, value: string | boolean) => {
      setMappings((prev) =>
        prev.map((entry, idx) => (idx === index ? { ...entry, [field]: value } : entry))
      );
    },
    []
  );

  const addMappingEntry = useCallback(() => {
    setMappings((prev) => [...prev, buildEmptyMappingEntry()]);
  }, []);

  const removeMappingEntry = useCallback((index: number) => {
    setMappings((prev) => {
      const next = prev.filter((_, idx) => idx !== index);
      return next.length ? next : [buildEmptyMappingEntry()];
    });
  }, []);

  const handleSave = useCallback(async () => {
    const providerKey = normalizeProviderKey(provider);
    if (!providerKey) {
      showNotification(t('oauth_model_alias.provider_required'), 'error');
      return;
    }

    const normalization = normalizeOAuthAliasEntries(mappings);
    const firstIssue = normalization.issues[0];
    if (firstIssue) {
      if (firstIssue.code === 'same_as_name') {
        showNotification(t('oauth_model_alias.alias_same_as_name'), 'error');
        return;
      }
      if (firstIssue.code === 'duplicate_alias') {
        showNotification(
          t('oauth_model_alias.alias_duplicate', { alias: firstIssue.alias ?? '' }),
          'error'
        );
        return;
      }
      showNotification(t('oauth_model_alias.alias_incomplete'), 'error');
      return;
    }

    const normalizedMappings = normalization.accepted;
    if (
      normalizedMappings.length === 0 &&
      mappings.some((entry) => entry.name.trim() || entry.alias.trim())
    ) {
      showNotification(t('oauth_model_alias.alias_incomplete'), 'error');
      return;
    }

    setSaving(true);
    try {
      if (normalizedMappings.length > 0) {
        await authFilesApi.saveOauthModelAlias(providerKey, normalizedMappings, requestScope);
      } else {
        await authFilesApi.deleteOauthModelAlias(providerKey, requestScope);
      }
      await onSaved();
      showNotification(t('oauth_model_alias.save_success'), 'success');
      onClose();
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : '';
      showNotification(`${t('oauth_model_alias.save_failed')}: ${message}`, 'error');
    } finally {
      setSaving(false);
    }
  }, [mappings, onClose, onSaved, provider, requestScope, showNotification, t]);

  const canSave =
    !disabled && !saving && !unsupported && Boolean(resolvedProviderKey) && hasMappingsChanged;

  return (
    <Modal
      open={open}
      onClose={requestClose}
      closeDisabled={saving}
      title={t('oauth_model_alias.add_title')}
      width={940}
      footer={
        <>
          <Button variant="secondary" onClick={requestClose} disabled={saving}>
            {t('common.cancel')}
          </Button>
          <Button onClick={() => void handleSave()} loading={saving} disabled={!canSave}>
            {t('oauth_model_alias.save')}
          </Button>
        </>
      }
    >
      {unsupported ? (
        <EmptyState
          title={t('oauth_model_alias.upgrade_required_title')}
          description={t('oauth_model_alias.upgrade_required_desc')}
        />
      ) : (
        <div className={styles.editorBody}>
          <section className={styles.settingsBlock}>
            <div className={styles.settingsHeader}>
              <div className={styles.settingsHeaderTitle}>
                <IconInfo size={16} />
                <span>{t('oauth_model_alias.title')}</span>
              </div>
              <div className={styles.settingsHeaderHint}>{headerHint}</div>
            </div>
            <div className={styles.settingsSection}>
              <div className={styles.settingsRow}>
                <div className={styles.settingsInfo}>
                  <div className={styles.settingsLabel}>
                    {t('oauth_model_alias.provider_label')}
                  </div>
                  <div className={styles.settingsDesc}>{t('oauth_model_alias.provider_hint')}</div>
                </div>
                <div className={styles.settingsControl}>
                  <AutocompleteInput
                    id="accounts-oauth-model-alias-provider"
                    placeholder={t('oauth_model_alias.provider_placeholder')}
                    value={provider}
                    onChange={handleProviderChange}
                    options={providerOptions}
                    disabled={disabled || saving || hasMappingsChanged}
                    wrapperStyle={{ marginBottom: 0 }}
                  />
                </div>
              </div>
              <div className={styles.tagList}>
                {providerOptions.map((option) => {
                  const active = normalizeProviderKey(provider) === normalizeProviderKey(option);
                  return (
                    <button
                      key={option}
                      type="button"
                      className={`${styles.tag} ${active ? styles.tagActive : ''}`}
                      onClick={() => handleProviderChange(option)}
                      disabled={disabled || saving || hasMappingsChanged}
                    >
                      {getTypeLabel(t, option)}
                    </button>
                  );
                })}
              </div>
            </div>
          </section>

          <section className={styles.settingsBlock}>
            <div className={styles.mappingsHeader}>
              <div className={styles.mappingsTitle}>{t('oauth_model_alias.alias_label')}</div>
              <Button
                variant="secondary"
                size="sm"
                onClick={addMappingEntry}
                disabled={disabled || saving || unsupported || !resolvedProviderKey}
              >
                {t('oauth_model_alias.add_alias')}
              </Button>
            </div>
            <div className={styles.mappingsBody}>
              <div className={styles.mappingUsageHint}>{t('oauth_model_alias.usage_hint')}</div>
              {mappings.map((entry, index) => (
                <div key={entry.id} className={styles.mappingRow}>
                  <div className={styles.mappingCore}>
                    <AutocompleteInput
                      wrapperStyle={{ flex: 1, marginBottom: 0 }}
                      placeholder={t('oauth_model_alias.alias_name_placeholder')}
                      value={entry.name}
                      onChange={(value) => updateMappingEntry(index, 'name', value)}
                      disabled={disabled || saving || !resolvedProviderKey}
                      options={modelOptions.map((model) => ({
                        value: model.id,
                        label:
                          model.display_name && model.display_name !== model.id
                            ? model.display_name
                            : undefined,
                      }))}
                    />
                    <span className={styles.mappingSeparator}>-&gt;</span>
                    <input
                      className={`input ${styles.mappingAliasInput}`}
                      placeholder={t('oauth_model_alias.alias_placeholder')}
                      value={entry.alias}
                      onChange={(event) => updateMappingEntry(index, 'alias', event.target.value)}
                      disabled={disabled || saving || !resolvedProviderKey}
                    />
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => removeMappingEntry(index)}
                      disabled={disabled || saving || !resolvedProviderKey}
                      title={t('common.delete')}
                      aria-label={t('common.delete')}
                    >
                      <IconX size={14} />
                    </Button>
                  </div>
                  <div className={styles.mappingOptions}>
                    <input
                      className={`input ${styles.mappingDisplayNameInput}`}
                      placeholder={t('oauth_model_alias.alias_display_name_placeholder')}
                      aria-label={t('oauth_model_alias.alias_display_name_label')}
                      value={entry.displayName ?? ''}
                      onChange={(event) =>
                        updateMappingEntry(index, 'displayName', event.target.value)
                      }
                      disabled={disabled || saving || !resolvedProviderKey}
                    />
                    <div className={styles.mappingToggles}>
                      <div className={styles.mappingFork}>
                        <ToggleSwitch
                          label={t('oauth_model_alias.alias_fork_label')}
                          labelPosition="left"
                          checked={Boolean(entry.fork)}
                          onChange={(value) => updateMappingEntry(index, 'fork', value)}
                          disabled={disabled || saving || !resolvedProviderKey}
                        />
                      </div>
                      <div className={styles.mappingFork}>
                        <ToggleSwitch
                          label={t('oauth_model_alias.alias_force_mapping_label')}
                          labelPosition="left"
                          checked={Boolean(entry.forceMapping)}
                          onChange={(value) => updateMappingEntry(index, 'forceMapping', value)}
                          disabled={disabled || saving || !resolvedProviderKey}
                        />
                      </div>
                    </div>
                  </div>
                </div>
              ))}
            </div>
          </section>
        </div>
      )}
    </Modal>
  );
}
