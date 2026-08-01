import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Drawer } from '@/components/ui/Drawer';
import { Input } from '@/components/ui/Input';
import { Select } from '@/components/ui/Select';
import { HeaderInputList } from '@/components/ui/HeaderInputList';
import { ModelInputList } from '@/components/ui/ModelInputList';
import { Modal } from '@/components/ui/Modal';
import { SelectionCheckbox } from '@/components/ui/SelectionCheckbox';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { apiCallApi, getApiCallErrorMessage, modelsApi, providersApi } from '@/services/api';
import { useConfigStore, useNotificationStore } from '@/stores';
import type { ProviderKeyConfig } from '@/types';
import {
  buildHeaderObject,
  hasHeader,
  headersToEntries,
  normalizeHeaderEntries,
} from '@/utils/headers';
import { normalizeAuthIndex } from '@/utils/authIndex';
import {
  areKeyValueEntriesEqual,
  areModelEntriesEqual,
  areStringArraysEqual,
} from '@/utils/compare';
import { entriesToModels, modelsToEntries } from '@/components/ui/modelInputListUtils';
import {
  buildCodexResponsesEndpoint,
  excludedModelsToText,
  parseExcludedModels,
} from '@/components/providers/utils';
import type { ProviderFormState } from '@/components/providers';
import type { ModelInfo } from '@/utils/models';
import styles from '@/features/aiProviders/AiProvidersPage.module.scss';

interface CodexEditDrawerProps {
  open: boolean;
  editIndex: number | null;
  disabled: boolean;
  onClose: () => void;
  onSaved: () => void;
  providerKind?: 'codex' | 'xai';
}

type CodexFormBaseline = ReturnType<typeof buildCodexBaseline>;
type TestStatus = 'idle' | 'loading' | 'success' | 'error';

const CODEX_TEST_TIMEOUT_MS = 20_000;
const XAI_API_BASE_URL = 'https://api.x.ai/v1';

const buildEmptyForm = (baseUrl = ''): ProviderFormState => ({
  apiKey: '',
  priority: undefined,
  prefix: '',
  baseUrl,
  websockets: false,
  proxyUrl: '',
  headers: [],
  models: [],
  excludedModels: [],
  modelEntries: [{ name: '', alias: '' }],
  excludedText: '',
});

const normalizeModelEntries = (entries: ProviderFormState['modelEntries']) =>
  (entries ?? []).reduce<ProviderFormState['modelEntries']>((acc, entry) => {
    const name = String(entry?.name ?? '').trim();
    let alias = String(entry?.alias ?? '').trim();
    if (name && alias === name) alias = '';
    if (!name && !alias) return acc;
    acc.push({ ...entry, name, alias });
    return acc;
  }, []);

const buildCodexBaseline = (form: ProviderFormState) => ({
  apiKey: String(form.apiKey ?? '').trim(),
  authIndex: normalizeAuthIndex(form.authIndex) ?? '',
  priority:
    form.priority !== undefined && Number.isFinite(form.priority)
      ? Math.trunc(form.priority)
      : null,
  prefix: String(form.prefix ?? '').trim(),
  baseUrl: String(form.baseUrl ?? '').trim(),
  websockets: Boolean(form.websockets),
  disableCooling: Boolean(form.disableCooling),
  proxyUrl: String(form.proxyUrl ?? '').trim(),
  headers: normalizeHeaderEntries(form.headers),
  models: normalizeModelEntries(form.modelEntries),
  excludedModels: parseExcludedModels(form.excludedText ?? ''),
});

const getErrorMessage = (err: unknown) => {
  if (err instanceof Error) return err.message;
  if (typeof err === 'string') return err;
  return '';
};

export function CodexEditDrawer({
  open,
  editIndex,
  disabled,
  onClose,
  onSaved,
  providerKind = 'codex',
}: CodexEditDrawerProps) {
  const { t } = useTranslation();
  const { showNotification } = useNotificationStore();
  const fetchConfig = useConfigStore((state) => state.fetchConfig);
  const updateConfigValue = useConfigStore((state) => state.updateConfigValue);
  const clearCache = useConfigStore((state) => state.clearCache);
  const isXAI = providerKind === 'xai';
  const providerSection = isXAI ? 'xai-api-key' : 'codex-api-key';
  const defaultBaseUrl = isXAI ? XAI_API_BASE_URL : '';

  const [configs, setConfigs] = useState<ProviderKeyConfig[]>([]);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [form, setForm] = useState<ProviderFormState>(() => buildEmptyForm(defaultBaseUrl));
  const [baseline, setBaseline] = useState<CodexFormBaseline>(() =>
    buildCodexBaseline(buildEmptyForm(defaultBaseUrl))
  );
  const [loaded, setLoaded] = useState(false);

  const [modelDiscoveryOpen, setModelDiscoveryOpen] = useState(false);
  const [modelDiscoveryFetching, setModelDiscoveryFetching] = useState(false);
  const [modelDiscoveryError, setModelDiscoveryError] = useState('');
  const [discoveredModels, setDiscoveredModels] = useState<ModelInfo[]>([]);
  const [modelDiscoverySearch, setModelDiscoverySearch] = useState('');
  const [modelDiscoverySelected, setModelDiscoverySelected] = useState<Set<string>>(new Set());
  const [testModel, setTestModel] = useState('');
  const [testStatus, setTestStatus] = useState<TestStatus>('idle');
  const [testMessage, setTestMessage] = useState('');
  const [isTesting, setIsTesting] = useState(false);

  const initialData = useMemo(() => {
    if (editIndex === null) return undefined;
    return configs[editIndex];
  }, [configs, editIndex]);
  const invalidIndex = loaded && editIndex !== null && !initialData;

  const title =
    editIndex !== null
      ? t(isXAI ? 'ai_providers.xai_edit_modal_title' : 'ai_providers.codex_edit_modal_title')
      : t(isXAI ? 'ai_providers.xai_add_modal_title' : 'ai_providers.codex_add_modal_title');

  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    setLoaded(false);
    setConfigs([]);
    setLoading(true);
    setError('');
    fetchConfig(providerSection)
      .then((value) => {
        if (cancelled) return;
        setConfigs(Array.isArray(value) ? (value as ProviderKeyConfig[]) : []);
        setLoaded(true);
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        setError(getErrorMessage(err) || t('notification.refresh_failed'));
      })
      .finally(() => {
        if (cancelled) return;
        setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [open, fetchConfig, providerSection, t]);

  useEffect(() => {
    if (open) return;
    setLoaded(false);
    setConfigs([]);
    setError('');
  }, [open]);

  useEffect(() => {
    if (!open || !loaded) return;
    if (initialData) {
      const nextForm: ProviderFormState = {
        ...initialData,
        websockets: Boolean(initialData.websockets),
        headers: headersToEntries(initialData.headers),
        modelEntries: modelsToEntries(initialData.models),
        excludedText: excludedModelsToText(initialData.excludedModels),
      };
      setForm(nextForm);
      setBaseline(buildCodexBaseline(nextForm));
      const available = nextForm.modelEntries.map((entry) => entry.name.trim()).filter(Boolean);
      setTestModel(available[0] || '');
    } else {
      const nextForm = buildEmptyForm(defaultBaseUrl);
      setForm(nextForm);
      setBaseline(buildCodexBaseline(nextForm));
      setTestModel('');
    }
    setTestStatus('idle');
    setTestMessage('');
  }, [defaultBaseUrl, open, loaded, initialData]);

  const canSave =
    loaded && !error && !disabled && !saving && !loading && !invalidIndex && !isTesting;

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
      baseline.websockets !== Boolean(form.websockets) ||
      baseline.disableCooling !== Boolean(form.disableCooling) ||
      baseline.proxyUrl !== String(form.proxyUrl ?? '').trim() ||
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
      new Set(form.modelEntries.map((entry) => entry.name.trim().toLowerCase()).filter(Boolean)),
    [form.modelEntries]
  );

  const visibleDiscoverableModelNames = useMemo(
    () =>
      discoveredModelsFiltered
        .map((model) => String(model.name ?? '').trim())
        .filter((name) => name && !configuredModelNames.has(name.toLowerCase())),
    [configuredModelNames, discoveredModelsFiltered]
  );

  const allVisibleSelected = useMemo(
    () =>
      visibleDiscoverableModelNames.length > 0 &&
      visibleDiscoverableModelNames.every((name) => modelDiscoverySelected.has(name)),
    [modelDiscoverySelected, visibleDiscoverableModelNames]
  );

  const availableModels = useMemo(
    () => form.modelEntries.map((entry) => entry.name.trim()).filter(Boolean),
    [form.modelEntries]
  );

  const modelSelectOptions = useMemo(() => {
    const seen = new Set<string>();
    return form.modelEntries.reduce<Array<{ value: string; label: string }>>((acc, entry) => {
      const name = entry.name.trim();
      if (!name || seen.has(name)) return acc;
      seen.add(name);
      const alias = entry.alias.trim();
      acc.push({
        value: name,
        label: alias && alias !== name ? `${name} (${alias})` : name,
      });
      return acc;
    }, []);
  }, [form.modelEntries]);

  const connectivityConfigSignature = useMemo(() => {
    const headersSignature = form.headers
      .map((entry) => `${entry.key.trim()}:${entry.value.trim()}`)
      .join('|');
    const modelsSignature = form.modelEntries
      .map((entry) => `${entry.name.trim()}:${entry.alias.trim()}`)
      .join('|');
    return [
      form.apiKey.trim(),
      normalizeAuthIndex(form.authIndex) ?? '',
      String(form.baseUrl ?? '').trim(),
      testModel.trim(),
      headersSignature,
      modelsSignature,
    ].join('||');
  }, [form.apiKey, form.authIndex, form.baseUrl, form.headers, form.modelEntries, testModel]);
  const previousConnectivityConfigRef = useRef(connectivityConfigSignature);

  useEffect(() => {
    if (previousConnectivityConfigRef.current === connectivityConfigSignature) {
      return;
    }
    previousConnectivityConfigRef.current = connectivityConfigSignature;
    setTestStatus('idle');
    setTestMessage('');
  }, [connectivityConfigSignature]);

  const mergeDiscoveredModels = useCallback(
    (selectedModels: ModelInfo[]) => {
      if (!selectedModels.length) return;
      let addedCount = 0;
      setForm((prev) => {
        const mergedMap = new Map<string, { name: string; alias: string }>();
        prev.modelEntries.forEach((entry) => {
          const name = entry.name.trim();
          if (!name) return;
          mergedMap.set(name.toLowerCase(), { ...entry, name, alias: entry.alias?.trim() || '' });
        });
        selectedModels.forEach((model) => {
          const name = String(model.name ?? '').trim();
          if (!name) return;
          const key = name.toLowerCase();
          if (mergedMap.has(key)) return;
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
          t('ai_providers.codex_models_fetch_added', { count: addedCount }),
          'success'
        );
      }
    },
    [showNotification, t]
  );

  const fetchModelDiscovery = useCallback(async () => {
    setModelDiscoveryFetching(true);
    setModelDiscoveryError('');
    try {
      const headerObject = buildHeaderObject(form.headers);
      const hasCustomAuthorization = Object.keys(headerObject).some(
        (key) => key.toLowerCase() === 'authorization'
      );
      const apiKey = form.apiKey.trim() || undefined;
      const list = await modelsApi.fetchV1ModelsViaApiCall(
        form.baseUrl ?? '',
        hasCustomAuthorization ? undefined : apiKey,
        headerObject,
        normalizeAuthIndex(form.authIndex) ?? undefined
      );
      setDiscoveredModels(list);
    } catch (err: unknown) {
      setDiscoveredModels([]);
      setModelDiscoveryError(
        `${t('ai_providers.codex_models_fetch_error')}: ${getErrorMessage(err)}`
      );
    } finally {
      setModelDiscoveryFetching(false);
    }
  }, [form.apiKey, form.authIndex, form.baseUrl, form.headers, t]);

  const runCodexConnectivityTest = useCallback(async () => {
    if (isTesting) return;

    const endpoint = buildCodexResponsesEndpoint(form.baseUrl ?? '');
    if (!endpoint) {
      const message = t('ai_providers.codex_test_endpoint_invalid');
      setTestStatus('error');
      setTestMessage(message);
      showNotification(message, 'error');
      return;
    }

    const modelName = testModel.trim() || availableModels[0] || '';
    if (!modelName) {
      const message = t('ai_providers.codex_test_model_required');
      setTestStatus('error');
      setTestMessage(message);
      showNotification(message, 'error');
      return;
    }

    const customHeaders = buildHeaderObject(form.headers);
    const apiKey = form.apiKey.trim();
    const keyAuthIndex = normalizeAuthIndex(form.authIndex) ?? undefined;
    const hasAuthorization = hasHeader(customHeaders, 'authorization');

    if (!apiKey && !hasAuthorization && !keyAuthIndex) {
      const message = t(
        isXAI ? 'ai_providers.xai_test_key_required' : 'ai_providers.codex_test_key_required'
      );
      setTestStatus('error');
      setTestMessage(message);
      showNotification(message, 'error');
      return;
    }

    const headers: Record<string, string> = {
      'Content-Type': 'application/json',
      ...customHeaders,
    };
    if (!hasHeader(headers, 'authorization')) {
      headers.Authorization = keyAuthIndex ? 'Bearer $TOKEN$' : `Bearer ${apiKey}`;
    }

    setIsTesting(true);
    setTestStatus('loading');
    setTestMessage(t(isXAI ? 'ai_providers.xai_test_running' : 'ai_providers.codex_test_running'));

    try {
      const result = await apiCallApi.request(
        {
          authIndex: keyAuthIndex,
          method: 'POST',
          url: endpoint,
          header: headers,
          data: JSON.stringify({
            model: modelName,
            input: 'Hi',
            stream: false,
          }),
        },
        { timeout: CODEX_TEST_TIMEOUT_MS }
      );

      if (result.statusCode < 200 || result.statusCode >= 300) {
        throw new Error(getApiCallErrorMessage(result));
      }

      const message = t(
        isXAI ? 'ai_providers.xai_test_success' : 'ai_providers.codex_test_success'
      );
      setTestStatus('success');
      setTestMessage(message);
      showNotification(message, 'success');
    } catch (err: unknown) {
      const failureText = t(
        isXAI ? 'ai_providers.xai_test_failed' : 'ai_providers.codex_test_failed'
      );
      const message = getErrorMessage(err) || failureText;
      setTestStatus('error');
      setTestMessage(message);
      showNotification(`${failureText}: ${message}`, 'error');
    } finally {
      setIsTesting(false);
    }
  }, [
    availableModels,
    form.apiKey,
    form.authIndex,
    form.baseUrl,
    form.headers,
    isXAI,
    isTesting,
    showNotification,
    t,
    testModel,
  ]);

  const handleSave = useCallback(async () => {
    if (!canSave) return;
    const apiKey = form.apiKey.trim();
    if (!apiKey && !normalizeAuthIndex(form.authIndex)) {
      showNotification(
        t(isXAI ? 'ai_providers.xai_key_required' : 'ai_providers.codex_key_required'),
        'error'
      );
      return;
    }
    const trimmedBaseUrl = (form.baseUrl ?? '').trim();
    if (!trimmedBaseUrl) {
      showNotification(
        t(isXAI ? 'notification.xai_base_url_required' : 'notification.codex_base_url_required'),
        'error'
      );
      return;
    }
    setSaving(true);
    setError('');
    try {
      const payload: ProviderKeyConfig = {
        apiKey: form.apiKey.trim(),
        priority: form.priority !== undefined ? Math.trunc(form.priority) : undefined,
        prefix: form.prefix?.trim() || undefined,
        baseUrl: trimmedBaseUrl,
        websockets: Boolean(form.websockets),
        proxyUrl: form.proxyUrl?.trim() || undefined,
        headers: buildHeaderObject(form.headers),
        models: entriesToModels(form.modelEntries),
        excludedModels: parseExcludedModels(form.excludedText),
        authIndex: normalizeAuthIndex(form.authIndex) ?? undefined,
        disableCooling: form.disableCooling,
        experimentalCchSigning: form.experimentalCchSigning,
      };
      if (editIndex !== null) {
        if (isXAI) {
          await providersApi.updateXAIConfig(configs[editIndex], payload);
        } else {
          await providersApi.updateCodexConfig(configs[editIndex], payload);
        }
      } else {
        if (isXAI) {
          await providersApi.createXAIConfig(payload);
        } else {
          await providersApi.createCodexConfig(payload);
        }
      }
      const syncedList = await (
        isXAI ? providersApi.getXAIConfigs() : providersApi.getCodexConfigs()
      ).catch(() =>
        editIndex !== null
          ? configs.map((item, index) => (index === editIndex ? payload : item))
          : [...configs, payload]
      );
      updateConfigValue(providerSection, syncedList);
      clearCache(providerSection);
      showNotification(
        editIndex !== null
          ? t(isXAI ? 'notification.xai_config_updated' : 'notification.codex_config_updated')
          : t(isXAI ? 'notification.xai_config_added' : 'notification.codex_config_added'),
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
    editIndex,
    form,
    isXAI,
    onClose,
    onSaved,
    providerSection,
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
      discoveredModels.map((model) => String(model.name ?? '').trim())
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

  const toggleModelDiscoverySelection = useCallback(
    (name: string) => {
      if (configuredModelNames.has(name.toLowerCase())) return;
      setModelDiscoverySelected((prev) => {
        const next = new Set(prev);
        if (next.has(name)) next.delete(name);
        else next.add(name);
        return next;
      });
    },
    [configuredModelNames]
  );

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

  const canOpenModelDiscovery =
    !disabled && !saving && !loading && !invalidIndex && Boolean((form.baseUrl ?? '').trim());
  const canApplyModelDiscovery =
    !disabled && !saving && !modelDiscoveryFetching && modelDiscoverySelected.size > 0;

  const footer = (
    <>
      <Button variant="secondary" size="sm" onClick={handleClose} disabled={saving || isTesting}>
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
        {!loading && loaded && !invalidIndex && (
          <>
            <Input
              label={t('ai_providers.codex_add_modal_key_label')}
              value={form.apiKey}
              onChange={(e) => setForm((prev) => ({ ...prev, apiKey: e.target.value }))}
              disabled={disabled || saving}
              required
            />
            <Input
              label={t('ai_providers.codex_add_modal_url_label')}
              value={form.baseUrl ?? ''}
              onChange={(e) => setForm((prev) => ({ ...prev, baseUrl: e.target.value }))}
              disabled={disabled || saving}
              required
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
              label={t('ai_providers.codex_add_modal_proxy_label')}
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

            <div className={styles.modelConfigSection}>
              <div className={styles.modelConfigHeader}>
                <label className={styles.modelConfigTitle}>
                  {t('ai_providers.codex_models_label')}
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
                    {t('ai_providers.codex_models_add_btn')}
                  </Button>
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => setModelDiscoveryOpen(true)}
                    disabled={!canOpenModelDiscovery}
                  >
                    {t('ai_providers.codex_models_fetch_button')}
                  </Button>
                </div>
              </div>
              <div className={styles.sectionHint}>{t('ai_providers.codex_models_hint')}</div>
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
                showForceMapping
                forceMappingLabel={t('ai_providers.force_mapping_label')}
              />
              <div className={styles.modelTestPanel}>
                <div className={styles.modelTestMeta}>
                  <label className={styles.modelTestLabel}>
                    {t('ai_providers.codex_test_title')}
                  </label>
                  <span className={styles.modelTestHint}>
                    {t(isXAI ? 'ai_providers.xai_test_hint' : 'ai_providers.codex_test_hint')}
                  </span>
                </div>
                <div className={styles.modelTestControls}>
                  <Select
                    value={testModel}
                    options={modelSelectOptions}
                    onChange={(value) => {
                      setTestModel(value);
                      setTestStatus('idle');
                      setTestMessage('');
                    }}
                    placeholder={
                      availableModels.length
                        ? t('ai_providers.codex_test_select_placeholder')
                        : t('ai_providers.codex_test_select_empty')
                    }
                    className={styles.openaiTestSelect}
                    ariaLabel={t('ai_providers.codex_test_title')}
                    disabled={
                      disabled ||
                      saving ||
                      isTesting ||
                      testStatus === 'loading' ||
                      availableModels.length === 0
                    }
                  />
                  <Button
                    variant={testStatus === 'error' ? 'danger' : 'secondary'}
                    size="sm"
                    onClick={() => void runCodexConnectivityTest()}
                    disabled={
                      disabled ||
                      saving ||
                      loading ||
                      isTesting ||
                      testStatus === 'loading' ||
                      availableModels.length === 0
                    }
                    loading={isTesting}
                    className={styles.modelTestAllButton}
                  >
                    {t('ai_providers.codex_test_button')}
                  </Button>
                </div>
              </div>
              {testMessage && (
                <div
                  className={`status-badge ${
                    testStatus === 'error'
                      ? 'error'
                      : testStatus === 'success'
                        ? 'success'
                        : 'muted'
                  }`}
                >
                  {testMessage}
                </div>
              )}
            </div>

            <div className="form-group">
              <label>{t('ai_providers.codex_websockets_label')}</label>
              <ToggleSwitch
                checked={Boolean(form.websockets)}
                onChange={(value) => setForm((prev) => ({ ...prev, websockets: value }))}
                disabled={disabled || saving}
                ariaLabel={t('ai_providers.codex_websockets_label')}
              />
              <div className="hint">{t('ai_providers.codex_websockets_hint')}</div>
            </div>

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
              title={t('ai_providers.codex_models_fetch_title')}
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
                        modelDiscoverySelected.has(m.name)
                      );
                      mergeDiscoveredModels(selectedModels);
                      setModelDiscoveryOpen(false);
                    }}
                    disabled={!canApplyModelDiscovery}
                  >
                    {t('ai_providers.codex_models_fetch_apply')}
                  </Button>
                </>
              }
            >
              <div className={styles.openaiModelsContent}>
                <div className={styles.sectionHint}>
                  {t('ai_providers.codex_models_fetch_hint')}
                </div>
                <Input
                  label={t('ai_providers.codex_models_search_label')}
                  placeholder={t('ai_providers.codex_models_search_placeholder')}
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
                    {t('ai_providers.codex_models_fetch_loading')}
                  </div>
                ) : discoveredModels.length === 0 ? (
                  <div className={styles.sectionHint}>
                    {t('ai_providers.codex_models_fetch_empty')}
                  </div>
                ) : (
                  <div className={styles.modelDiscoveryList}>
                    {discoveredModelsFiltered.map((model) => {
                      const checked = modelDiscoverySelected.has(model.name);
                      const alreadyConfigured = configuredModelNames.has(
                        model.name.trim().toLowerCase()
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
