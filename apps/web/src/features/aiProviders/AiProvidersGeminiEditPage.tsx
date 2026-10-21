import { useAuthStore as useDiscoveryAuthStore } from '@/stores/useAuthStore';
import { useLayoutEffect, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useLocation, useNavigate, useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { HeaderInputList } from '@/components/ui/HeaderInputList';
import { ModelInputList } from '@/components/ui/ModelInputList';
import { Modal } from '@/components/ui/Modal';
import { SelectionCheckbox } from '@/components/ui/SelectionCheckbox';
import { CoolingPolicySelect } from '@/components/providers/CoolingPolicySelect';
import { useEdgeSwipeBack } from '@/hooks/useEdgeSwipeBack';
import { useUnsavedChangesGuard } from '@/hooks/useUnsavedChangesGuard';
import { SecondaryScreenShell } from '@/components/common/SecondaryScreenShell';
import { modelsApi, providersApi } from '@/services/api';
import { useAuthStore, useConfigStore, useNotificationStore } from '@/stores';
import {
  coolingPolicyFromOverride,
  coolingPolicyToOverride,
  type GeminiKeyConfig,
  type CoolingPolicy,
} from '@/types';
import { buildHeaderObject, headersToEntries, normalizeHeaderEntries } from '@/utils/headers';
import { normalizeAuthIndex } from '@/utils/authIndex';
import {
  areKeyValueEntriesEqual,
  areModelEntriesEqual,
  areStringArraysEqual,
} from '@/utils/compare';
import { modelDisplayLabel, type ModelInfo } from '@/utils/models';
import { entriesToModels, modelsToEntries } from '@/components/ui/modelInputListUtils';
import { excludedModelsToText, parseExcludedModels } from '@/components/providers/utils';
import { CredentialWeightInput, type GeminiFormState } from '@/components/providers';
import {
  getCredentialWeightComparisonValue,
  getCredentialWeightError,
  normalizeCredentialWeight,
} from '@/utils/credentialWeight';
import { parseProviderIndexParam } from '@/features/aiProviders/model/routeParams';
import layoutStyles from './AiProvidersEditLayout.module.scss';
import styles from './AiProvidersPage.module.scss';

type LocationState = { fromAiProviders?: boolean } | null;

const buildEmptyForm = (): GeminiFormState => ({
  apiKey: '',
  priority: undefined,
  weight: undefined,
  prefix: '',
  baseUrl: '',
  proxyUrl: '',
  headers: [],
  modelEntries: [{ name: '', alias: '' }],
  excludedModels: [],
  excludedText: '',
  disableCooling: 'inherit',
});

const stripGeminiModelResourceName = (value: string) => {
  return String(value ?? '')
    .trim()
    .replace(/^\/?models\//i, '');
};

const normalizeModelEntries = (entries: Array<{ name: string; alias: string }>) =>
  (entries ?? []).reduce<Array<{ name: string; alias: string }>>((acc, entry) => {
    const name = stripGeminiModelResourceName(entry?.name ?? '').trim();
    let alias = String(entry?.alias ?? '').trim();
    if (name && alias === name) {
      alias = '';
    }
    if (!name && !alias) return acc;
    acc.push({ name, alias });
    return acc;
  }, []);

type GeminiFormBaseline = {
  apiKey: string;
  authIndex: string;
  priority: number | null;
  weight: number | null;
  prefix: string;
  baseUrl: string;
  proxyUrl: string;
  disableCooling: CoolingPolicy;
  headers: ReturnType<typeof normalizeHeaderEntries>;
  models: ReturnType<typeof normalizeModelEntries>;
  excludedModels: string[];
};

const buildGeminiBaseline = (form: GeminiFormState): GeminiFormBaseline => ({
  apiKey: String(form.apiKey ?? '').trim(),
  authIndex: normalizeAuthIndex(form.authIndex) ?? '',
  priority:
    form.priority !== undefined && Number.isFinite(form.priority)
      ? Math.trunc(form.priority)
      : null,
  weight: normalizeCredentialWeight(form.weight) ?? null,
  prefix: String(form.prefix ?? '').trim(),
  baseUrl: String(form.baseUrl ?? '').trim(),
  proxyUrl: String(form.proxyUrl ?? '').trim(),
  disableCooling: form.disableCooling,
  headers: normalizeHeaderEntries(form.headers),
  models: normalizeModelEntries(form.modelEntries),
  excludedModels: parseExcludedModels(form.excludedText ?? ''),
});

export function AiProvidersGeminiEditPage() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const location = useLocation();
  const params = useParams<{ index?: string }>();

  const { showNotification } = useNotificationStore();
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const disableControls = connectionStatus !== 'connected';

  const fetchConfig = useConfigStore((state) => state.fetchConfig);
  const updateConfigValue = useConfigStore((state) => state.updateConfigValue);
  const clearCache = useConfigStore((state) => state.clearCache);

  const [configs, setConfigs] = useState<GeminiKeyConfig[]>([]);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [form, setForm] = useState<GeminiFormState>(() => buildEmptyForm());
  const [baseline, setBaseline] = useState(() => buildGeminiBaseline(buildEmptyForm()));

  const [modelDiscoveryOpen, setModelDiscoveryOpen] = useState(false);
  const [modelDiscoveryEndpoint, setModelDiscoveryEndpoint] = useState('');
  const [discoveredModels, setDiscoveredModels] = useState<ModelInfo[]>([]);
  const [modelsLoaded, setModelsLoaded] = useState(false);
  const [modelDiscoveryFetching, setModelDiscoveryFetching] = useState(false);
  const [modelDiscoveryError, setModelDiscoveryError] = useState('');
  const [modelDiscoverySearch, setModelDiscoverySearch] = useState('');
  const [modelDiscoverySelected, setModelDiscoverySelected] = useState<Set<string>>(new Set());
  const autoFetchSignatureRef = useRef<string>('');
  const modelDiscoveryRequestIdRef = useRef(0);

  const hasIndexParam = typeof params.index === 'string';
  const editIndex = useMemo(() => parseProviderIndexParam(params.index), [params.index]);
  const invalidIndexParam = hasIndexParam && editIndex === null;

  const initialData = useMemo(() => {
    if (editIndex === null) return undefined;
    return configs[editIndex];
  }, [configs, editIndex]);

  const invalidIndex = editIndex !== null && !initialData;

  const title =
    editIndex !== null
      ? t('ai_providers.gemini_edit_modal_title')
      : t('ai_providers.gemini_add_modal_title');

  const handleBack = useCallback(() => {
    const state = location.state as LocationState;
    if (state?.fromAiProviders) {
      navigate(-1);
      return;
    }
    navigate('/ai-providers', { replace: true });
  }, [location.state, navigate]);

  const swipeRef = useEdgeSwipeBack({ onBack: handleBack });

  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        handleBack();
      }
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [handleBack]);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError('');

    fetchConfig('gemini-api-key')
      .then((value) => {
        if (cancelled) return;
        setConfigs(Array.isArray(value) ? (value as GeminiKeyConfig[]) : []);
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        const message = err instanceof Error ? err.message : '';
        setError(message || t('notification.refresh_failed'));
      })
      .finally(() => {
        if (cancelled) return;
        setLoading(false);
      });

    return () => {
      cancelled = true;
    };
  }, [fetchConfig, t]);

  useEffect(() => {
    if (loading) return;

    if (initialData) {
      const { headers, models, ...rest } = initialData;
      const nextForm: GeminiFormState = {
        ...rest,
        disableCooling: coolingPolicyFromOverride(initialData.disableCooling),
        headers: headersToEntries(headers),
        modelEntries: modelsToEntries(models).map((entry) => ({
          ...entry,
          name: stripGeminiModelResourceName(entry.name),
        })),
        excludedText: excludedModelsToText(initialData.excludedModels),
      };
      setForm(nextForm);
      setBaseline(buildGeminiBaseline(nextForm));
      return;
    }
    const nextForm = buildEmptyForm();
    setForm(nextForm);
    setBaseline(buildGeminiBaseline(nextForm));
  }, [initialData, loading]);

  const canSave =
    !disableControls &&
    !saving &&
    !loading &&
    !invalidIndexParam &&
    !invalidIndex &&
    !getCredentialWeightError(form.weight);
  const normalizedHeaders = useMemo(() => normalizeHeaderEntries(form.headers), [form.headers]);
  const normalizedModels = useMemo(
    () => normalizeModelEntries(form.modelEntries),
    [form.modelEntries]
  );
  const normalizedExcludedModels = useMemo(
    () => parseExcludedModels(form.excludedText ?? ''),
    [form.excludedText]
  );
  const normalizedPriority = useMemo(() => {
    return form.priority !== undefined && Number.isFinite(form.priority)
      ? Math.trunc(form.priority)
      : null;
  }, [form.priority]);
  const comparableWeight = getCredentialWeightComparisonValue(form.weight);

  const discoveredModelsFiltered = useMemo(() => {
    const filter = modelDiscoverySearch.trim().toLowerCase();
    if (!filter) return discoveredModels;
    return discoveredModels.filter((model) => {
      const name = (model.name || '').toLowerCase();
      const label = modelDisplayLabel(model).toLowerCase();
      const description = (model.description || '').toLowerCase();
      return name.includes(filter) || label.includes(filter) || description.includes(filter);
    });
  }, [discoveredModels, modelDiscoverySearch]);
  const configuredModelNames = useMemo(
    () =>
      new Set(
        normalizedModels.map((entry) => entry.name.trim().toLowerCase()).filter(Boolean)
      ),
    [normalizedModels]
  );
  const visibleDiscoveredModelNames = useMemo(
    () =>
      discoveredModelsFiltered
        .map((model) => stripGeminiModelResourceName(model.name).trim())
        .filter((name) => name && !configuredModelNames.has(name.toLowerCase())),
    [configuredModelNames, discoveredModelsFiltered]
  );
  const allVisibleDiscoveredSelected = useMemo(
    () =>
      visibleDiscoveredModelNames.length > 0 &&
      visibleDiscoveredModelNames.every((name) => modelDiscoverySelected.has(name)),
    [modelDiscoverySelected, visibleDiscoveredModelNames]
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
    [setForm, showNotification, t]
  );

  const discoveryConnection = useDiscoveryAuthStore((state) =>
    JSON.stringify([state.apiBase, state.managementKey])
  );
  const discoveryScope = JSON.stringify([
    discoveryConnection,
    modelDiscoveryOpen,
    editIndex,
    form.baseUrl,
    form.apiKey,
    form.authIndex,
    form.proxyUrl,
    buildHeaderObject(form.headers),
  ]);
  const activeDiscoveryScopeRef = useRef(discoveryScope);
  activeDiscoveryScopeRef.current = discoveryScope;
  useLayoutEffect(() => {
    modelDiscoveryRequestIdRef.current += 1;
    setDiscoveredModels([]);
    setModelsLoaded(false);
    setModelDiscoveryError('');
    setModelDiscoveryFetching(false);
    return () => {
      modelDiscoveryRequestIdRef.current += 1;
    };
  }, [discoveryScope]);

  const fetchGeminiModelDiscovery = useCallback(async () => {
    const requestId = (modelDiscoveryRequestIdRef.current += 1);
    const requestScope = discoveryScope;
    const isCurrentDiscovery = () =>
      modelDiscoveryRequestIdRef.current === requestId &&
      activeDiscoveryScopeRef.current === requestScope;
    setModelDiscoveryFetching(true);
    setModelDiscoveryError('');
    const headerObject = buildHeaderObject(form.headers);
    try {
      const list = await modelsApi.fetchGeminiModelsViaApiCall(
        form.baseUrl ?? '',
        form.apiKey.trim() || undefined,
        headerObject,
        normalizeAuthIndex(form.authIndex) ?? undefined,
        form.proxyUrl
      );
      if (!isCurrentDiscovery()) return;
      setDiscoveredModels(list);
      setModelsLoaded(true);
    } catch (err: unknown) {
      if (!isCurrentDiscovery()) return;

      const message = err instanceof Error ? err.message : typeof err === 'string' ? err : '';
      const hasCustomXGoogApiKey = Object.keys(headerObject).some(
        (key) => key.toLowerCase() === 'x-goog-api-key'
      );
      const hasAuthorization = Object.keys(headerObject).some(
        (key) => key.toLowerCase() === 'authorization'
      );
      const shouldAttachDiag = message.toLowerCase().includes('api key') || message.includes('401');
      const diag = shouldAttachDiag
        ? ` [diag: apiKeyField=${form.apiKey.trim() ? 'yes' : 'no'}, customXGoogApiKey=${
            hasCustomXGoogApiKey ? 'yes' : 'no'
          }, customAuthorization=${hasAuthorization ? 'yes' : 'no'}]`
        : '';
      setModelDiscoveryError(`${t('ai_providers.gemini_models_fetch_error')}: ${message}${diag}`);
    } finally {
      if (isCurrentDiscovery()) setModelDiscoveryFetching(false);
    }
  }, [discoveryScope, form.apiKey, form.authIndex, form.baseUrl, form.headers, form.proxyUrl, t]);

  useEffect(() => {
    if (!modelDiscoveryOpen) {
      autoFetchSignatureRef.current = '';
      modelDiscoveryRequestIdRef.current += 1;
      setModelDiscoveryFetching(false);
      return;
    }

    const nextEndpoint = modelsApi.buildGeminiModelsEndpoint(form.baseUrl ?? '');
    setModelDiscoveryEndpoint(nextEndpoint);
    setModelDiscoverySearch('');
    setModelDiscoverySelected(new Set());
    setModelDiscoveryError('');

    const headerObject = buildHeaderObject(form.headers);
    const hasCustomXGoogApiKey = Object.keys(headerObject).some(
      (key) => key.toLowerCase() === 'x-goog-api-key'
    );
    const hasAuthorization = Object.keys(headerObject).some(
      (key) => key.toLowerCase() === 'authorization'
    );
    const hasApiKeyField = Boolean(form.apiKey.trim());
    const hasAuthIndex = Boolean(normalizeAuthIndex(form.authIndex));
    const canAutoFetch = hasApiKeyField || hasCustomXGoogApiKey || hasAuthorization || hasAuthIndex;

    if (!canAutoFetch) return;

    const headerSignature = Object.entries(headerObject)
      .sort(([a], [b]) => a.toLowerCase().localeCompare(b.toLowerCase()))
      .map(([key, value]) => `${key}:${value}`)
      .join('|');
    const signature = `${nextEndpoint}||${form.apiKey.trim()}||${normalizeAuthIndex(form.authIndex) ?? ''}||${form.proxyUrl?.trim() ?? ''}||${headerSignature}`;
    if (autoFetchSignatureRef.current === signature) return;
    autoFetchSignatureRef.current = signature;

    void fetchGeminiModelDiscovery();
  }, [
    fetchGeminiModelDiscovery,
    form.apiKey,
    form.authIndex,
    form.baseUrl,
    form.headers,
    form.proxyUrl,
    modelDiscoveryOpen,
  ]);

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
      if (next.has(normalizedName)) {
        next.delete(normalizedName);
      } else {
        next.add(normalizedName);
      }
      return next;
    });
  };

  const handleSelectVisibleDiscoveredModels = useCallback(() => {
    setModelDiscoverySelected((prev) => {
      const next = new Set(prev);
      visibleDiscoveredModelNames.forEach((name) => next.add(name));
      return next;
    });
  }, [visibleDiscoveredModelNames]);

  const handleClearDiscoveredModelSelection = useCallback(() => {
    setModelDiscoverySelected(new Set());
  }, []);

  const handleApplyDiscoveredModels = () => {
    const selectedModels = discoveredModels.filter((model) =>
      modelDiscoverySelected.has(stripGeminiModelResourceName(model.name).trim())
    );
    if (selectedModels.length) {
      mergeDiscoveredModels(selectedModels);
    }
    setModelDiscoveryOpen(false);
  };
  const isHeadersDirty = useMemo(
    () => !areKeyValueEntriesEqual(baseline.headers, normalizedHeaders),
    [baseline.headers, normalizedHeaders]
  );
  const isModelsDirty = useMemo(
    () => !areModelEntriesEqual(baseline.models, normalizedModels),
    [baseline.models, normalizedModels]
  );
  const isExcludedModelsDirty = useMemo(
    () => !areStringArraysEqual(baseline.excludedModels, normalizedExcludedModels),
    [baseline.excludedModels, normalizedExcludedModels]
  );
  const isDirty =
    baseline.apiKey !== form.apiKey.trim() ||
    baseline.authIndex !== (normalizeAuthIndex(form.authIndex) ?? '') ||
    baseline.priority !== normalizedPriority ||
    baseline.weight !== comparableWeight ||
    baseline.prefix !== String(form.prefix ?? '').trim() ||
    baseline.baseUrl !== String(form.baseUrl ?? '').trim() ||
    baseline.proxyUrl !== String(form.proxyUrl ?? '').trim() ||
    baseline.disableCooling !== form.disableCooling ||
    isHeadersDirty ||
    isModelsDirty ||
    isExcludedModelsDirty;
  const canGuard = !loading && !saving && !invalidIndexParam && !invalidIndex;

  const { allowNextNavigation } = useUnsavedChangesGuard({
    enabled: canGuard,
    shouldBlock: ({ currentLocation, nextLocation }) =>
      isDirty && currentLocation.pathname !== nextLocation.pathname,
    dialog: {
      title: t('common.unsaved_changes_title'),
      message: t('common.unsaved_changes_message'),
      confirmText: t('common.leave'),
      cancelText: t('common.stay'),
      variant: 'danger',
    },
  });

  const handleSave = useCallback(async () => {
    if (!canSave) return;

    setSaving(true);
    setError('');
    try {
      const normalizedModelEntries = form.modelEntries.map((entry) => ({
        ...entry,
        name: stripGeminiModelResourceName(entry.name),
      }));

      const payload: GeminiKeyConfig = {
        apiKey: form.apiKey.trim(),
        priority: form.priority !== undefined ? Math.trunc(form.priority) : undefined,
        weight: normalizeCredentialWeight(form.weight),
        prefix: form.prefix?.trim() || undefined,
        baseUrl: form.baseUrl?.trim() || undefined,
        proxyUrl: form.proxyUrl?.trim() || undefined,
        headers: buildHeaderObject(form.headers),
        models: entriesToModels(normalizedModelEntries),
        excludedModels: parseExcludedModels(form.excludedText),
        authIndex: normalizeAuthIndex(form.authIndex) ?? undefined,
        disableCooling: coolingPolicyToOverride(form.disableCooling),
      };

      if (editIndex !== null) {
        await providersApi.updateGeminiKey(configs[editIndex], payload);
      } else {
        await providersApi.createGeminiKey(payload);
      }
      const syncedList = await providersApi.getGeminiKeys().catch(() =>
        editIndex !== null
          ? configs.map((item, index) => (index === editIndex ? payload : item))
          : [...configs, payload]
      );
      updateConfigValue('gemini-api-key', syncedList);
      clearCache('gemini-api-key');
      showNotification(
        editIndex !== null
          ? t('notification.gemini_key_updated')
          : t('notification.gemini_key_added'),
        'success'
      );
      allowNextNavigation();
      setBaseline(buildGeminiBaseline(form));
      handleBack();
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : '';
      setError(message);
      showNotification(`${t('notification.update_failed')}: ${message}`, 'error');
    } finally {
      setSaving(false);
    }
  }, [
    allowNextNavigation,
    canSave,
    clearCache,
    configs,
    editIndex,
    form,
    handleBack,
    showNotification,
    t,
    updateConfigValue,
  ]);

  const canOpenModelDiscovery =
    !disableControls && !saving && !loading && !invalidIndexParam && !invalidIndex;
  const canApplyModelDiscovery =
    !disableControls && !saving && !modelDiscoveryFetching && modelDiscoverySelected.size > 0;

  return (
    <SecondaryScreenShell
      ref={swipeRef}
      contentClassName={layoutStyles.content}
      title={title}
      onBack={handleBack}
      backLabel={t('common.back')}
      backAriaLabel={t('common.back')}
      hideTopBarBackButton
      hideTopBarRightAction
      floatingAction={
        <div className={layoutStyles.floatingActions}>
          <Button
            variant="secondary"
            size="sm"
            onClick={handleBack}
            className={layoutStyles.floatingBackButton}
          >
            {t('common.back')}
          </Button>
          <Button
            size="sm"
            onClick={handleSave}
            loading={saving}
            disabled={!canSave}
            className={layoutStyles.floatingSaveButton}
          >
            {t('common.save')}
          </Button>
        </div>
      }
      isLoading={loading}
      loadingLabel={t('common.loading')}
    >
      <Card>
        {error && <div className="error-box">{error}</div>}
        {invalidIndexParam || invalidIndex ? (
          <div className="hint">{t('common.invalid_provider_index')}</div>
        ) : (
          <>
            <Input
              label={t('ai_providers.gemini_add_modal_key_label')}
              placeholder={t('ai_providers.gemini_add_modal_key_placeholder')}
              value={form.apiKey}
              onChange={(e) => setForm((prev) => ({ ...prev, apiKey: e.target.value }))}
              disabled={disableControls || saving}
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
              disabled={disableControls || saving}
            />
            <CredentialWeightInput
              value={form.weight}
              onChange={(weight) => setForm((prev) => ({ ...prev, weight }))}
              disabled={disableControls || saving}
            />
            <Input
              label={t('ai_providers.prefix_label')}
              placeholder={t('ai_providers.prefix_placeholder')}
              value={form.prefix ?? ''}
              onChange={(e) => setForm((prev) => ({ ...prev, prefix: e.target.value }))}
              hint={t('ai_providers.prefix_hint')}
              disabled={disableControls || saving}
            />
            <Input
              label={t('ai_providers.gemini_base_url_label')}
              placeholder={t('ai_providers.gemini_base_url_placeholder')}
              value={form.baseUrl ?? ''}
              onChange={(e) => setForm((prev) => ({ ...prev, baseUrl: e.target.value }))}
              disabled={disableControls || saving}
            />
            <Input
              label={t('ai_providers.gemini_add_modal_proxy_label')}
              placeholder={t('ai_providers.gemini_add_modal_proxy_placeholder')}
              value={form.proxyUrl ?? ''}
              onChange={(e) => setForm((prev) => ({ ...prev, proxyUrl: e.target.value }))}
              hint={t('ai_providers.model_discovery_proxy_version_hint')}
              disabled={disableControls || saving}
            />
            <HeaderInputList
              entries={form.headers}
              onChange={(entries) => setForm((prev) => ({ ...prev, headers: entries }))}
              addLabel={t('common.custom_headers_add')}
              keyPlaceholder={t('common.custom_headers_key_placeholder')}
              valuePlaceholder={t('common.custom_headers_value_placeholder')}
              removeButtonTitle={t('common.delete')}
              removeButtonAriaLabel={t('common.delete')}
              disabled={disableControls || saving}
            />
            <CoolingPolicySelect
              value={form.disableCooling}
              onChange={(value) => setForm((prev) => ({ ...prev, disableCooling: value }))}
              disabled={disableControls || saving}
              id="gemini-page-cooling-policy"
            />

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
                    disabled={disableControls || saving}
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
                disabled={disableControls || saving}
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
                disabled={disableControls || saving}
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
                    onClick={handleApplyDiscoveredModels}
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
                <div className={styles.openaiModelsEndpointSection}>
                  <label className={styles.openaiModelsEndpointLabel}>
                    {t('ai_providers.gemini_models_fetch_url_label')}
                  </label>
                  <div className={styles.openaiModelsEndpointControls}>
                    <input
                      className={`input ${styles.openaiModelsEndpointInput}`}
                      readOnly
                      value={modelDiscoveryEndpoint}
                    />
                    <Button
                      variant="secondary"
                      size="sm"
                      onClick={() => void fetchGeminiModelDiscovery()}
                      loading={modelDiscoveryFetching}
                      disabled={disableControls || saving}
                    >
                      {t('ai_providers.gemini_models_fetch_refresh')}
                    </Button>
                  </div>
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
                        onClick={handleSelectVisibleDiscoveredModels}
                        disabled={
                          disableControls ||
                          saving ||
                          modelDiscoveryFetching ||
                          discoveredModelsFiltered.length === 0 ||
                          allVisibleDiscoveredSelected
                        }
                      >
                        {t('ai_providers.model_discovery_select_visible')}
                      </Button>
                      <Button
                        variant="ghost"
                        size="sm"
                        onClick={handleClearDiscoveredModelSelection}
                        disabled={
                          disableControls ||
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
                {modelDiscoveryFetching && !modelsLoaded ? (
                  <div className={styles.sectionHint}>
                    {t('ai_providers.gemini_models_fetch_loading')}
                  </div>
                ) : !modelsLoaded ? null : discoveredModels.length === 0 ? (
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
                      const discoveryLabel = modelDisplayLabel(model);
                      const alreadyConfigured = configuredModelNames.has(
                        normalizedName.toLowerCase()
                      );
                      return (
                        <SelectionCheckbox
                          key={model.name}
                          checked={checked}
                          onChange={() => toggleModelDiscoverySelection(model.name)}
                          disabled={
                            disableControls || saving || modelDiscoveryFetching || alreadyConfigured
                          }
                          ariaLabel={model.name}
                          className={`${styles.modelDiscoveryRow} ${
                            checked ? styles.modelDiscoveryRowSelected : ''
                          }`}
                          labelClassName={styles.modelDiscoverySelectionLabel}
                          label={
                            <div className={styles.modelDiscoveryMeta}>
                              <div className={styles.modelDiscoveryName}>
                                <div className={styles.modelDiscoveryNameText}>
                                  {model.name}
                                  {discoveryLabel && (
                                    <span className={styles.modelDiscoveryAlias}>
                                      {discoveryLabel}
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
      </Card>
    </SecondaryScreenShell>
  );
}
