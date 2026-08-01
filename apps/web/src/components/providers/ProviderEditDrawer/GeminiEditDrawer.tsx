import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Drawer } from '@/components/ui/Drawer';
import { Input } from '@/components/ui/Input';
import { HeaderInputList } from '@/components/ui/HeaderInputList';
import { ModelInputList } from '@/components/ui/ModelInputList';
import { Modal } from '@/components/ui/Modal';
import { SelectionCheckbox } from '@/components/ui/SelectionCheckbox';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { modelsApi, providersApi } from '@/services/api';
import { useConfigStore, useNotificationStore } from '@/stores';
import type { GeminiKeyConfig } from '@/types';
import { buildHeaderObject, headersToEntries, normalizeHeaderEntries } from '@/utils/headers';
import { normalizeAuthIndex } from '@/utils/authIndex';
import {
  areKeyValueEntriesEqual,
  areModelEntriesEqual,
  areStringArraysEqual,
} from '@/utils/compare';
import type { ModelInfo } from '@/utils/models';
import { entriesToModels, modelsToEntries } from '@/components/ui/modelInputListUtils';
import { excludedModelsToText, parseExcludedModels } from '@/components/providers/utils';
import type { GeminiFormState } from '@/components/providers';
import styles from '@/features/aiProviders/AiProvidersPage.module.scss';

interface GeminiEditDrawerProps {
  open: boolean;
  editIndex: number | null;
  disabled: boolean;
  onClose: () => void;
  onSaved: () => void;
  providerKind?: 'gemini' | 'interactions';
}

type GeminiFormBaseline = ReturnType<typeof buildGeminiBaseline>;

const buildEmptyForm = (): GeminiFormState => ({
  apiKey: '',
  priority: undefined,
  prefix: '',
  baseUrl: '',
  proxyUrl: '',
  headers: [],
  modelEntries: [{ name: '', alias: '' }],
  excludedModels: [],
  excludedText: '',
});

const stripGeminiModelResourceName = (value: string) =>
  String(value ?? '')
    .trim()
    .replace(/^\/?models\//i, '');

const normalizeModelEntries = (entries: Array<{ name: string; alias: string }>) =>
  (entries ?? []).reduce<Array<{ name: string; alias: string }>>((acc, entry) => {
    const name = stripGeminiModelResourceName(entry?.name ?? '').trim();
    let alias = String(entry?.alias ?? '').trim();
    if (name && alias === name) alias = '';
    if (!name && !alias) return acc;
    acc.push({ name, alias });
    return acc;
  }, []);

const buildGeminiBaseline = (form: GeminiFormState) => ({
  apiKey: String(form.apiKey ?? '').trim(),
  authIndex: normalizeAuthIndex(form.authIndex) ?? '',
  priority:
    form.priority !== undefined && Number.isFinite(form.priority)
      ? Math.trunc(form.priority)
      : null,
  prefix: String(form.prefix ?? '').trim(),
  baseUrl: String(form.baseUrl ?? '').trim(),
  proxyUrl: String(form.proxyUrl ?? '').trim(),
  disableCooling: Boolean(form.disableCooling),
  headers: normalizeHeaderEntries(form.headers),
  models: normalizeModelEntries(form.modelEntries),
  excludedModels: parseExcludedModels(form.excludedText ?? ''),
});

const getErrorMessage = (err: unknown) => {
  if (err instanceof Error) return err.message;
  if (typeof err === 'string') return err;
  return '';
};

export function GeminiEditDrawer({
  open,
  editIndex,
  disabled,
  onClose,
  onSaved,
  providerKind = 'gemini',
}: GeminiEditDrawerProps) {
  const { t } = useTranslation();
  const { showNotification } = useNotificationStore();
  const fetchConfig = useConfigStore((state) => state.fetchConfig);
  const updateConfigValue = useConfigStore((state) => state.updateConfigValue);
  const clearCache = useConfigStore((state) => state.clearCache);

  const [configs, setConfigs] = useState<GeminiKeyConfig[]>([]);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [form, setForm] = useState<GeminiFormState>(buildEmptyForm);
  const [baseline, setBaseline] = useState<GeminiFormBaseline>(
    buildGeminiBaseline(buildEmptyForm())
  );
  const [loaded, setLoaded] = useState(false);

  const [modelDiscoveryOpen, setModelDiscoveryOpen] = useState(false);
  const [modelDiscoveryFetching, setModelDiscoveryFetching] = useState(false);
  const [modelDiscoveryError, setModelDiscoveryError] = useState('');
  const [discoveredModels, setDiscoveredModels] = useState<ModelInfo[]>([]);
  const [modelDiscoverySearch, setModelDiscoverySearch] = useState('');
  const [modelDiscoverySelected, setModelDiscoverySelected] = useState<Set<string>>(new Set());

  const initialData = useMemo(() => {
    if (editIndex === null) return undefined;
    return configs[editIndex];
  }, [configs, editIndex]);
  const invalidIndex = editIndex !== null && !initialData;

  const isInteractions = providerKind === 'interactions';
  const configSection = isInteractions ? 'interactions-api-key' : 'gemini-api-key';
  const title = isInteractions
    ? t(
        editIndex !== null
          ? 'ai_providers.interactions_edit_modal_title'
          : 'ai_providers.interactions_add_modal_title'
      )
    : t(
        editIndex !== null
          ? 'ai_providers.gemini_edit_modal_title'
          : 'ai_providers.gemini_add_modal_title'
      );

  // Load configs on open
  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    setLoading(true);
    setError('');
    fetchConfig(configSection)
      .then((value) => {
        if (cancelled) return;
        setConfigs(Array.isArray(value) ? (value as GeminiKeyConfig[]) : []);
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        setError(getErrorMessage(err) || t('notification.refresh_failed'));
      })
      .finally(() => {
        if (cancelled) return;
        setLoading(false);
        setLoaded(true);
      });
    return () => {
      cancelled = true;
    };
  }, [configSection, open, fetchConfig, t]);

  // Init form when configs loaded
  useEffect(() => {
    if (!open || !loaded) return;
    if (initialData) {
      const { headers, models, ...rest } = initialData;
      const nextForm: GeminiFormState = {
        ...rest,
        headers: headersToEntries(headers),
        modelEntries: modelsToEntries(models).map((entry) => ({
          ...entry,
          name: stripGeminiModelResourceName(entry.name),
        })),
        excludedText: excludedModelsToText(initialData.excludedModels),
      };
      setForm(nextForm);
      setBaseline(buildGeminiBaseline(nextForm));
    } else {
      const nextForm = buildEmptyForm();
      setForm(nextForm);
      setBaseline(buildGeminiBaseline(nextForm));
    }
  }, [open, loaded, initialData]);

  const canSave = !disabled && !saving && !loading && !invalidIndex;

  const isDirty = useMemo(() => {
    const normalizedPriority =
      form.priority !== undefined && Number.isFinite(form.priority)
        ? Math.trunc(form.priority)
        : null;
    return (
      baseline.apiKey !== form.apiKey.trim() ||
      baseline.authIndex !== (normalizeAuthIndex(form.authIndex) ?? '') ||
      baseline.priority !== normalizedPriority ||
      baseline.prefix !== String(form.prefix ?? '').trim() ||
      baseline.baseUrl !== String(form.baseUrl ?? '').trim() ||
      baseline.proxyUrl !== String(form.proxyUrl ?? '').trim() ||
      baseline.disableCooling !== Boolean(form.disableCooling) ||
      !areKeyValueEntriesEqual(baseline.headers, normalizeHeaderEntries(form.headers)) ||
      !areModelEntriesEqual(baseline.models, normalizeModelEntries(form.modelEntries)) ||
      !areStringArraysEqual(baseline.excludedModels, parseExcludedModels(form.excludedText ?? ''))
    );
  }, [baseline, form]);

  const discoveredModelsFiltered = useMemo(() => {
    const filter = modelDiscoverySearch.trim().toLowerCase();
    if (!filter) return discoveredModels;
    return discoveredModels.filter((model) => {
      const name = (model.name || '').toLowerCase();
      const alias = (model.alias || '').toLowerCase();
      const description = (model.description || '').toLowerCase();
      return name.includes(filter) || alias.includes(filter) || description.includes(filter);
    });
  }, [discoveredModels, modelDiscoverySearch]);

  const configuredModelNames = useMemo(
    () =>
      new Set(
        form.modelEntries
          .map((entry) => stripGeminiModelResourceName(entry.name).trim().toLowerCase())
          .filter(Boolean)
      ),
    [form.modelEntries]
  );

  const visibleDiscoverableModelNames = useMemo(
    () =>
      discoveredModelsFiltered
        .map((model) => stripGeminiModelResourceName(model.name).trim())
        .filter((name) => name && !configuredModelNames.has(name.toLowerCase())),
    [configuredModelNames, discoveredModelsFiltered]
  );

  const allVisibleSelected = useMemo(
    () =>
      visibleDiscoverableModelNames.length > 0 &&
      visibleDiscoverableModelNames.every((name) => modelDiscoverySelected.has(name)),
    [modelDiscoverySelected, visibleDiscoverableModelNames]
  );

  const mergeDiscoveredModels = useCallback(
    (selectedModels: ModelInfo[]) => {
      if (!selectedModels.length) return;
      let addedCount = 0;
      setForm((prev) => {
        const mergedMap = new Map<string, { name: string; alias: string }>();
        prev.modelEntries.forEach((entry) => {
          const name = stripGeminiModelResourceName(entry.name);
          if (!name) return;
          mergedMap.set(name.toLowerCase(), { ...entry, name, alias: entry.alias?.trim() || '' });
        });
        selectedModels.forEach((model) => {
          const name = stripGeminiModelResourceName(model.name);
          const key = name.toLowerCase();
          if (!name || mergedMap.has(key)) return;
          mergedMap.set(key, { name, alias: model.alias ?? '' });
          addedCount += 1;
        });
        const mergedEntries = Array.from(mergedMap.values());
        return {
          ...prev,
          modelEntries: mergedEntries.length ? mergedEntries : [{ name: '', alias: '' }],
        };
      });
      if (addedCount > 0) {
        showNotification(
          t('ai_providers.gemini_models_fetch_added', { count: addedCount }),
          'success'
        );
      }
    },
    [showNotification, t]
  );

  const fetchModelDiscovery = useCallback(async () => {
    setModelDiscoveryFetching(true);
    setModelDiscoveryError('');
    const headerObject = buildHeaderObject(form.headers);
    try {
      const list = await modelsApi.fetchGeminiModelsViaApiCall(
        form.baseUrl ?? '',
        form.apiKey.trim() || undefined,
        headerObject,
        normalizeAuthIndex(form.authIndex) ?? undefined
      );
      setDiscoveredModels(list);
    } catch (err: unknown) {
      setDiscoveredModels([]);
      setModelDiscoveryError(
        `${t('ai_providers.gemini_models_fetch_error')}: ${getErrorMessage(err)}`
      );
    } finally {
      setModelDiscoveryFetching(false);
    }
  }, [form.apiKey, form.authIndex, form.baseUrl, form.headers, t]);

  const handleSave = useCallback(async () => {
    if (!canSave) return;
    const apiKey = form.apiKey.trim();
    if (!apiKey) {
      showNotification(
        t(
          isInteractions
            ? 'ai_providers.interactions_key_required'
            : 'ai_providers.gemini_key_required',
          { defaultValue: 'Please enter an API Key' }
        ),
        'error'
      );
      return;
    }
    setSaving(true);
    setError('');
    try {
      const normalizedModelEntries = form.modelEntries.map((entry) => ({
        ...entry,
        name: stripGeminiModelResourceName(entry.name),
      }));
      const payload: GeminiKeyConfig = {
        apiKey,
        priority: form.priority !== undefined ? Math.trunc(form.priority) : undefined,
        prefix: form.prefix?.trim() || undefined,
        baseUrl: form.baseUrl?.trim() || undefined,
        proxyUrl: form.proxyUrl?.trim() || undefined,
        headers: buildHeaderObject(form.headers),
        models: entriesToModels(normalizedModelEntries),
        excludedModels: parseExcludedModels(form.excludedText),
        authIndex: normalizeAuthIndex(form.authIndex) ?? undefined,
        disableCooling: form.disableCooling,
      };
      if (isInteractions) {
        if (editIndex !== null) {
          await providersApi.updateInteractionsKey(configs[editIndex], payload);
        } else {
          await providersApi.createInteractionsKey(payload);
        }
      } else {
        if (editIndex !== null) {
          await providersApi.updateGeminiKey(configs[editIndex], payload);
        } else {
          await providersApi.createGeminiKey(payload);
        }
      }
      const syncedList = isInteractions
        ? await providersApi.getInteractionsKeys().catch(() =>
            editIndex !== null
              ? configs.map((item, index) => (index === editIndex ? payload : item))
              : [...configs, payload]
          )
        : await providersApi.getGeminiKeys().catch(() =>
            editIndex !== null
              ? configs.map((item, index) => (index === editIndex ? payload : item))
              : [...configs, payload]
          );
      updateConfigValue(configSection, syncedList);
      clearCache(configSection);
      showNotification(
        editIndex !== null
          ? t(
              isInteractions
                ? 'notification.interactions_key_updated'
                : 'notification.gemini_key_updated'
            )
          : t(
              isInteractions
                ? 'notification.interactions_key_added'
                : 'notification.gemini_key_added'
            ),
        'success'
      );
      onSaved();
      onClose();
    } catch (err: unknown) {
      setError(getErrorMessage(err));
      showNotification(`${t('notification.update_failed')}: ${getErrorMessage(err)}`, 'error');
    } finally {
      setSaving(false);
    }
  }, [
    canSave,
    clearCache,
    configs,
    configSection,
    editIndex,
    form,
    isInteractions,
    onClose,
    onSaved,
    showNotification,
    t,
    updateConfigValue,
  ]);

  const handleClose = useCallback(() => {
    if (isDirty && !saving) {
      if (!window.confirm(t('common.unsaved_changes_message'))) return;
    }
    onClose();
  }, [isDirty, onClose, saving, t]);

  useEffect(() => {
    if (!modelDiscoveryOpen) return;
    setDiscoveredModels([]);
    setModelDiscoverySearch('');
    setModelDiscoverySelected(new Set());
    setModelDiscoveryError('');
    void fetchModelDiscovery();
  }, [modelDiscoveryOpen, fetchModelDiscovery]);

  useEffect(() => {
    const availableNames = new Set(
      discoveredModels.map((model) => stripGeminiModelResourceName(model.name).trim())
    );
    setModelDiscoverySelected((prev) => {
      let changed = false;
      const next = new Set<string>();
      prev.forEach((name) => {
        if (availableNames.has(name) && !configuredModelNames.has(name.toLowerCase())) {
          next.add(name);
        } else {
          changed = true;
        }
      });
      return changed ? next : prev;
    });
  }, [configuredModelNames, discoveredModels]);

  const toggleModelDiscoverySelection = (name: string) => {
    const normalizedName = stripGeminiModelResourceName(name).trim();
    if (configuredModelNames.has(normalizedName.toLowerCase())) return;
    setModelDiscoverySelected((prev) => {
      const next = new Set(prev);
      if (next.has(normalizedName)) next.delete(normalizedName);
      else next.add(normalizedName);
      return next;
    });
  };

  const handleSelectVisibleModels = useCallback(() => {
    setModelDiscoverySelected((prev) => {
      const next = new Set(prev);
      visibleDiscoverableModelNames.forEach((name) => next.add(name));
      return next;
    });
  }, [visibleDiscoverableModelNames]);

  const handleClearModelDiscoverySelection = useCallback(() => {
    setModelDiscoverySelected(new Set());
  }, []);

  const canOpenModelDiscovery = !disabled && !saving && !loading && !invalidIndex;
  const canApplyModelDiscovery =
    !disabled && !saving && !modelDiscoveryFetching && modelDiscoverySelected.size > 0;

  const footer = (
    <>
      <Button variant="secondary" size="sm" onClick={handleClose} disabled={saving}>
        {t('common.cancel')}
      </Button>
      <Button size="sm" onClick={handleSave} loading={saving} disabled={!canSave}>
        {t('common.save')}
      </Button>
    </>
  );

  return (
    <Drawer open={open} onClose={handleClose} width={820} footer={footer} title={title}>
      <div className={styles.openaiEditForm}>
        {error && <div className="error-box">{error}</div>}
        {loading && <div className={styles.sectionHint}>{t('common.loading')}</div>}
        {invalidIndex && <div className="hint">{t('common.invalid_provider_index')}</div>}
        {!loading && !invalidIndex && (
          <>
            <Input
              label={t(
                isInteractions
                  ? 'ai_providers.interactions_key_label'
                  : 'ai_providers.gemini_add_modal_key_label'
              )}
              placeholder={t(
                isInteractions
                  ? 'ai_providers.interactions_key_placeholder'
                  : 'ai_providers.gemini_add_modal_key_placeholder'
              )}
              value={form.apiKey}
              onChange={(e) => setForm((prev) => ({ ...prev, apiKey: e.target.value }))}
              disabled={disabled || saving}
              required
            />
            <Input
              label={t('ai_providers.gemini_base_url_label')}
              placeholder={t('ai_providers.gemini_base_url_placeholder')}
              value={form.baseUrl ?? ''}
              onChange={(e) => setForm((prev) => ({ ...prev, baseUrl: e.target.value }))}
              disabled={disabled || saving}
            />
            <Input
              label={t('ai_providers.priority_label')}
              hint={t('ai_providers.priority_hint')}
              type="number"
              step={1}
              value={form.priority ?? ''}
              onChange={(e) => {
                const raw = e.target.value;
                const parsed = raw.trim() === '' ? undefined : Number(raw);
                setForm((prev) => ({
                  ...prev,
                  priority: parsed !== undefined && Number.isFinite(parsed) ? parsed : undefined,
                }));
              }}
              disabled={disabled || saving}
            />
            <Input
              label={t('ai_providers.prefix_label')}
              placeholder={t('ai_providers.prefix_placeholder')}
              value={form.prefix ?? ''}
              onChange={(e) => setForm((prev) => ({ ...prev, prefix: e.target.value }))}
              hint={t('ai_providers.prefix_hint')}
              disabled={disabled || saving}
            />
            <Input
              label={t('ai_providers.gemini_add_modal_proxy_label')}
              placeholder={t('ai_providers.gemini_add_modal_proxy_placeholder')}
              value={form.proxyUrl ?? ''}
              onChange={(e) => setForm((prev) => ({ ...prev, proxyUrl: e.target.value }))}
              disabled={disabled || saving}
            />
            <HeaderInputList
              entries={form.headers}
              onChange={(entries) => setForm((prev) => ({ ...prev, headers: entries }))}
              addLabel={t('common.custom_headers_add')}
              keyPlaceholder={t('common.custom_headers_key_placeholder')}
              valuePlaceholder={t('common.custom_headers_value_placeholder')}
              removeButtonTitle={t('common.delete')}
              removeButtonAriaLabel={t('common.delete')}
              disabled={disabled || saving}
            />
            <div className="form-group">
              <label>{t('ai_providers.disable_cooling_label')}</label>
              <ToggleSwitch
                checked={Boolean(form.disableCooling)}
                onChange={(value) => setForm((prev) => ({ ...prev, disableCooling: value }))}
                disabled={disabled || saving}
                ariaLabel={t('ai_providers.disable_cooling_label')}
              />
              <div className="hint">{t('ai_providers.disable_cooling_hint')}</div>
            </div>

            <div className={styles.modelConfigSection}>
              <div className={styles.modelConfigHeader}>
                <label className={styles.modelConfigTitle}>
                  {t('ai_providers.gemini_models_label')}
                </label>
                <div className={styles.modelConfigToolbar}>
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() =>
                      setForm((prev) => ({
                        ...prev,
                        modelEntries: [...prev.modelEntries, { name: '', alias: '' }],
                      }))
                    }
                    disabled={disabled || saving}
                  >
                    {t('ai_providers.gemini_models_add_btn')}
                  </Button>
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => setModelDiscoveryOpen(true)}
                    disabled={!canOpenModelDiscovery}
                  >
                    {t('ai_providers.gemini_models_fetch_button')}
                  </Button>
                </div>
              </div>
              <div className={styles.sectionHint}>{t('ai_providers.gemini_models_hint')}</div>
              <ModelInputList
                entries={form.modelEntries}
                onChange={(entries) => setForm((prev) => ({ ...prev, modelEntries: entries }))}
                namePlaceholder={t('common.model_name_placeholder')}
                aliasPlaceholder={t('common.model_alias_placeholder')}
                disabled={disabled || saving}
                hideAddButton
                className={styles.modelInputList}
                rowClassName={styles.modelInputRow}
                inputClassName={styles.modelInputField}
                removeButtonClassName={styles.modelRowRemoveButton}
                removeButtonTitle={t('common.delete')}
                removeButtonAriaLabel={t('common.delete')}
              />
            </div>

            <div className="form-group">
              <label>{t('ai_providers.excluded_models_label')}</label>
              <textarea
                className="input"
                placeholder={t('ai_providers.excluded_models_placeholder')}
                value={form.excludedText}
                onChange={(e) => setForm((prev) => ({ ...prev, excludedText: e.target.value }))}
                rows={4}
                disabled={disabled || saving}
              />
              <div className="hint">{t('ai_providers.excluded_models_hint')}</div>
            </div>

            <Modal
              open={modelDiscoveryOpen}
              title={t('ai_providers.gemini_models_fetch_title')}
              onClose={() => setModelDiscoveryOpen(false)}
              width={720}
              footer={
                <>
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => setModelDiscoveryOpen(false)}
                    disabled={modelDiscoveryFetching}
                  >
                    {t('common.cancel')}
                  </Button>
                  <Button
                    size="sm"
                    onClick={() => {
                      const selectedModels = discoveredModels.filter((m) =>
                        modelDiscoverySelected.has(stripGeminiModelResourceName(m.name).trim())
                      );
                      mergeDiscoveredModels(selectedModels);
                      setModelDiscoveryOpen(false);
                    }}
                    disabled={!canApplyModelDiscovery}
                  >
                    {t('ai_providers.gemini_models_fetch_apply')}
                  </Button>
                </>
              }
            >
              <div className={styles.openaiModelsContent}>
                <div className={styles.sectionHint}>
                  {t('ai_providers.gemini_models_fetch_hint')}
                </div>
                <Input
                  label={t('ai_providers.gemini_models_search_label')}
                  placeholder={t('ai_providers.gemini_models_search_placeholder')}
                  value={modelDiscoverySearch}
                  onChange={(e) => setModelDiscoverySearch(e.target.value)}
                  disabled={modelDiscoveryFetching}
                />
                {discoveredModels.length > 0 && (
                  <div className={styles.modelDiscoveryToolbar}>
                    <div className={styles.modelDiscoveryToolbarActions}>
                      <Button
                        variant="secondary"
                        size="sm"
                        onClick={handleSelectVisibleModels}
                        disabled={
                          disabled ||
                          saving ||
                          modelDiscoveryFetching ||
                          visibleDiscoverableModelNames.length === 0 ||
                          allVisibleSelected
                        }
                      >
                        {t('ai_providers.model_discovery_select_visible')}
                      </Button>
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={handleClearModelDiscoverySelection}
                        disabled={
                          disabled ||
                          saving ||
                          modelDiscoveryFetching ||
                          modelDiscoverySelected.size === 0
                        }
                      >
                        {t('ai_providers.model_discovery_clear_selection')}
                      </Button>
                    </div>
                    <div className={styles.modelDiscoverySelectionSummary}>
                      {t('ai_providers.model_discovery_selected_count', {
                        count: modelDiscoverySelected.size,
                      })}
                    </div>
                  </div>
                )}
                {modelDiscoveryError && <div className="error-box">{modelDiscoveryError}</div>}
                {modelDiscoveryFetching ? (
                  <div className={styles.sectionHint}>
                    {t('ai_providers.gemini_models_fetch_loading')}
                  </div>
                ) : discoveredModels.length === 0 ? (
                  <div className={styles.sectionHint}>
                    {t('ai_providers.gemini_models_fetch_empty')}
                  </div>
                ) : discoveredModelsFiltered.length === 0 ? (
                  <div className={styles.sectionHint}>
                    {t('ai_providers.gemini_models_search_empty')}
                  </div>
                ) : (
                  <div className={styles.modelDiscoveryList}>
                    {discoveredModelsFiltered.map((model) => {
                      const normalizedName = stripGeminiModelResourceName(model.name).trim();
                      const checked = modelDiscoverySelected.has(normalizedName);
                      const alreadyConfigured = configuredModelNames.has(
                        normalizedName.toLowerCase()
                      );
                      return (
                        <SelectionCheckbox
                          key={model.name}
                          checked={checked}
                          onChange={() => toggleModelDiscoverySelection(model.name)}
                          disabled={
                            disabled || saving || modelDiscoveryFetching || alreadyConfigured
                          }
                          ariaLabel={model.name}
                          className={`${styles.modelDiscoveryRow} ${checked ? styles.modelDiscoveryRowSelected : ''}`}
                          labelClassName={styles.modelDiscoverySelectionLabel}
                          label={
                            <div className={styles.modelDiscoveryMeta}>
                              <div className={styles.modelDiscoveryName}>
                                <div className={styles.modelDiscoveryNameText}>
                                  {model.name}
                                  {model.alias && (
                                    <span className={styles.modelDiscoveryAlias}>
                                      {model.alias}
                                    </span>
                                  )}
                                </div>
                                {alreadyConfigured && (
                                  <span className={styles.modelDiscoveryAddedBadge}>
                                    {t('ai_providers.model_discovery_already_added')}
                                  </span>
                                )}
                              </div>
                              {model.description && (
                                <div className={styles.modelDiscoveryDesc}>{model.description}</div>
                              )}
                            </div>
                          }
                        />
                      );
                    })}
                  </div>
                )}
              </div>
            </Modal>
          </>
        )}
      </div>
    </Drawer>
  );
}
