import { useCallback, useEffect, useMemo, useState } from 'react';
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
import { OpenAIKeyTestStatusIndicator } from '@/components/providers';
import { apiCallApi, getApiCallErrorDetails, modelsApi, providersApi } from '@/services/api';
import { useConfigStore, useNotificationStore } from '@/stores';
import type { ApiKeyEntry, OpenAIProviderConfig } from '@/types';
import { buildHeaderObject, headersToEntries, normalizeHeaderEntries } from '@/utils/headers';
import { normalizeAuthIndex } from '@/utils/authIndex';
import { areKeyValueEntriesEqual, areModelEntriesEqual } from '@/utils/compare';
import { entriesToModels, modelsToEntries } from '@/components/ui/modelInputListUtils';
import { buildApiKeyEntry, buildOpenAIChatCompletionsEndpoint } from '@/components/providers/utils';
import {
  appendIdleKeyTestStatus,
  removeKeyTestStatusAtIndex,
} from '@/features/aiProviders/model/keyTestStatuses';
import type { ModelInfo } from '@/utils/models';
import type { OpenAIFormState } from '@/components/providers';
import styles from '@/features/aiProviders/AiProvidersPage.module.scss';

interface OpenAIEditDrawerProps {
  open: boolean;
  editIndex: number | null;
  disabled: boolean;
  onClose: () => void;
  onSaved: () => void;
}

type OpenAIFormBaseline = ReturnType<typeof buildOpenAIBaseline>;

const OPENAI_TEST_TIMEOUT_MS = 30_000;

const buildEmptyForm = (): OpenAIFormState => ({
  name: '',
  priority: undefined,
  prefix: '',
  baseUrl: '',
  headers: [],
  apiKeyEntries: [buildApiKeyEntry()],
  modelEntries: [{ name: '', alias: '' }],
  testModel: undefined,
});

const normalizeModelEntries = (entries: OpenAIFormState['modelEntries']) =>
  (entries ?? []).reduce<OpenAIFormState['modelEntries']>((acc, entry) => {
    const name = String(entry?.name ?? '').trim();
    let alias = String(entry?.alias ?? '').trim();
    if (name && (alias === '' || alias === name)) alias = '';
    if (!name && !alias) return acc;
    acc.push({ ...entry, name, alias });
    return acc;
  }, []);

const normalizeKeyHeaders = (headers: ApiKeyEntry['headers']) => {
  if (!headers || typeof headers !== 'object') return [];
  return Object.entries(headers)
    .map(([key, value]) => ({ key: String(key ?? '').trim(), value: String(value ?? '').trim() }))
    .filter((entry) => entry.key || entry.value)
    .sort((a, b) => {
      const byKey = a.key.toLowerCase().localeCompare(b.key.toLowerCase());
      return byKey !== 0 ? byKey : a.value.localeCompare(b.value);
    });
};

const normalizeApiKeyEntries = (entries: ApiKeyEntry[]) =>
  (entries ?? []).reduce<
    Array<{
      apiKey: string;
      proxyUrl: string;
      authIndex: string;
      headers: ReturnType<typeof normalizeKeyHeaders>;
    }>
  >((acc, entry) => {
    const apiKey = String(entry?.apiKey ?? '').trim();
    const proxyUrl = String(entry?.proxyUrl ?? '').trim();
    const authIndex = normalizeAuthIndex(entry?.authIndex) ?? '';
    const headers = normalizeKeyHeaders(entry?.headers);
    if (!apiKey && !proxyUrl && !authIndex && headers.length === 0) return acc;
    acc.push({ apiKey, proxyUrl, authIndex, headers });
    return acc;
  }, []);

const buildOpenAIBaseline = (form: OpenAIFormState) => ({
  name: String(form.name ?? '').trim(),
  priority:
    form.priority !== undefined && Number.isFinite(form.priority)
      ? Math.trunc(form.priority)
      : null,
  prefix: String(form.prefix ?? '').trim(),
  baseUrl: String(form.baseUrl ?? '').trim(),
  disableCooling: Boolean(form.disableCooling),
  headers: normalizeHeaderEntries(form.headers),
  apiKeyEntries: normalizeApiKeyEntries(form.apiKeyEntries),
  models: normalizeModelEntries(form.modelEntries),
});

const areNormalizedApiKeyEntriesEqual = (
  a: ReturnType<typeof normalizeApiKeyEntries>,
  b: ReturnType<typeof normalizeApiKeyEntries>
) => {
  if (a === b) return true;
  if (a.length !== b.length) return false;
  for (let i = 0; i < a.length; i += 1) {
    const left = a[i];
    const right = b[i];
    if (!left || !right) return false;
    if (
      left.apiKey !== right.apiKey ||
      left.proxyUrl !== right.proxyUrl ||
      left.authIndex !== right.authIndex
    )
      return false;
    if (!areKeyValueEntriesEqual(left.headers, right.headers)) return false;
  }
  return true;
};

const getErrorMessage = (err: unknown) => {
  if (err instanceof Error) return err.message;
  if (typeof err === 'string') return err;
  return '';
};

const hasHeader = (headers: Record<string, string>, name: string) => {
  const target = name.toLowerCase();
  return Object.keys(headers).some((key) => key.toLowerCase() === target);
};

export function OpenAIEditDrawer({
  open,
  editIndex,
  disabled,
  onClose,
  onSaved,
}: OpenAIEditDrawerProps) {
  const { t } = useTranslation();
  const { showNotification } = useNotificationStore();
  const config = useConfigStore((state) => state.config);
  const updateConfigValue = useConfigStore((state) => state.updateConfigValue);

  const [providers, setProviders] = useState<OpenAIProviderConfig[]>(
    () => config?.openaiCompatibility ?? []
  );
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [form, setForm] = useState<OpenAIFormState>(buildEmptyForm);
  const [baseline, setBaseline] = useState<OpenAIFormBaseline>(
    buildOpenAIBaseline(buildEmptyForm())
  );
  const [loaded, setLoaded] = useState(false);
  const [isTestingKeys, setIsTestingKeys] = useState(false);
  const [testModel, setTestModel] = useState('');
  const [testStatus, setTestStatus] = useState<'idle' | 'loading' | 'success' | 'error'>('idle');
  const [testMessage, setTestMessage] = useState('');
  const [keyTestStatuses, setKeyTestStatuses] = useState<
    Array<{ status: string; message: string }>
  >([]);

  const [modelDiscoveryOpen, setModelDiscoveryOpen] = useState(false);
  const [modelDiscoveryFetching, setModelDiscoveryFetching] = useState(false);
  const [modelDiscoveryError, setModelDiscoveryError] = useState('');
  const [discoveredModels, setDiscoveredModels] = useState<ModelInfo[]>([]);
  const [modelDiscoverySearch, setModelDiscoverySearch] = useState('');
  const [modelDiscoverySelected, setModelDiscoverySelected] = useState<Set<string>>(new Set());

  const initialData = useMemo(() => {
    if (editIndex === null) return undefined;
    return providers[editIndex];
  }, [editIndex, providers]);
  const invalidIndex = editIndex !== null && !initialData;

  const title =
    editIndex !== null
      ? t('ai_providers.openai_edit_modal_title')
      : t('ai_providers.openai_add_modal_title');

  const availableModels = useMemo(
    () => form.modelEntries.map((e) => e.name.trim()).filter(Boolean),
    [form.modelEntries]
  );
  const hasConfiguredModels = form.modelEntries.some((entry) => entry.name.trim());
  const hasTestableKeys = form.apiKeyEntries.some(
    (entry) => entry.apiKey?.trim() || normalizeAuthIndex(entry.authIndex)
  );

  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    setLoading(true);
    setError('');
    providersApi
      .getOpenAIProviders()
      .then((value) => {
        if (cancelled) return;
        const nextProviders = value || [];
        setProviders(nextProviders);
        updateConfigValue('openai-compatibility', nextProviders);
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
  }, [open, t, updateConfigValue]);

  useEffect(() => {
    if (!open || !loaded) return;
    if (initialData) {
      const modelEntries = modelsToEntries(initialData.models);
      const seededForm: OpenAIFormState = {
        name: initialData.name,
        priority: initialData.priority,
        prefix: initialData.prefix ?? '',
        baseUrl: initialData.baseUrl,
        headers: headersToEntries(initialData.headers),
        modelEntries,
        apiKeyEntries: initialData.apiKeyEntries?.length
          ? initialData.apiKeyEntries
          : [buildApiKeyEntry()],
        disableCooling: initialData.disableCooling,
      };
      setForm(seededForm);
      setBaseline(buildOpenAIBaseline(seededForm));
      const available = modelEntries.map((e) => e.name.trim()).filter(Boolean);
      const initialTestModel =
        initialData.testModel && available.includes(initialData.testModel)
          ? initialData.testModel
          : available[0] || '';
      setTestModel(initialTestModel);
    } else {
      const emptyForm = buildEmptyForm();
      setForm(emptyForm);
      setBaseline(buildOpenAIBaseline(emptyForm));
      setTestModel('');
    }
    setTestStatus('idle');
    setTestMessage('');
    setKeyTestStatuses([]);
  }, [open, loaded, initialData]);

  useEffect(() => {
    if (
      loaded &&
      availableModels.length > 0 &&
      (!testModel || !availableModels.includes(testModel))
    ) {
      setTestModel(availableModels[0]);
    }
  }, [availableModels, loaded, testModel]);

  const canSave = !disabled && !loading && !saving && !invalidIndex && !isTestingKeys;

  const isDirty = useMemo(() => {
    const normalizedPriority =
      form.priority !== undefined && Number.isFinite(form.priority)
        ? Math.trunc(form.priority)
        : null;
    return (
      baseline.name !== form.name.trim() ||
      baseline.priority !== normalizedPriority ||
      baseline.prefix !== form.prefix.trim() ||
      baseline.baseUrl !== form.baseUrl.trim() ||
      baseline.disableCooling !== Boolean(form.disableCooling) ||
      !areKeyValueEntriesEqual(baseline.headers, normalizeHeaderEntries(form.headers)) ||
      !areNormalizedApiKeyEntriesEqual(
        baseline.apiKeyEntries,
        normalizeApiKeyEntries(form.apiKeyEntries)
      ) ||
      !areModelEntriesEqual(baseline.models, normalizeModelEntries(form.modelEntries))
    );
  }, [baseline, form]);

  const handleClose = useCallback(() => {
    if (isDirty && !saving) {
      if (!window.confirm(t('common.unsaved_changes_message'))) return;
    }
    onClose();
  }, [isDirty, onClose, saving, t]);

  // Model discovery
  const fetchModelDiscovery = useCallback(async () => {
    setModelDiscoveryFetching(true);
    setModelDiscoveryError('');
    const headerObject = buildHeaderObject(form.headers);
    try {
      const firstKey = form.apiKeyEntries[0];
      const keyAuthIndex = normalizeAuthIndex(firstKey?.authIndex) ?? undefined;
      const list = await modelsApi.fetchModelsViaApiCall(
        form.baseUrl.trim(),
        firstKey?.apiKey?.trim() || undefined,
        headerObject,
        keyAuthIndex
      );
      setDiscoveredModels(list);
    } catch (err: unknown) {
      setDiscoveredModels([]);
      setModelDiscoveryError(
        `${t('ai_providers.openai_models_fetch_error')}: ${getErrorMessage(err)}`
      );
    } finally {
      setModelDiscoveryFetching(false);
    }
  }, [form.apiKeyEntries, form.baseUrl, form.headers, t]);

  useEffect(() => {
    if (!modelDiscoveryOpen) return;
    setDiscoveredModels([]);
    setModelDiscoverySearch('');
    setModelDiscoverySelected(new Set());
    setModelDiscoveryError('');
    void fetchModelDiscovery();
  }, [modelDiscoveryOpen, fetchModelDiscovery]);

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
          .map((entry) => entry.name.trim().toLowerCase())
          .filter(Boolean)
      ),
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

  const mergeDiscoveredModels = useCallback(
    (selectedModels: ModelInfo[]) => {
      if (!selectedModels.length) return;
      let addedCount = 0;
      setForm((prev) => {
        const mergedMap = new Map<string, { name: string; alias: string }>();
        prev.modelEntries.forEach((entry) => {
          const name = entry.name.trim();
          if (!name) return;
          mergedMap.set(name.toLowerCase(), {
            ...entry,
            name,
            alias: entry.alias?.trim() || '',
          });
        });
        selectedModels.forEach((model) => {
          const name = model.name.trim();
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
      if (addedCount > 0)
        showNotification(
          t('ai_providers.openai_models_fetch_added', { count: addedCount }),
          'success'
        );
    },
    [showNotification, t]
  );

  useEffect(() => {
    const availableNames = new Set(discoveredModels.map((model) => String(model.name ?? '').trim()));
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

  // Key testing
  const runSingleKeyTest = useCallback(
    async (keyIndex: number): Promise<boolean> => {
      const baseUrl = form.baseUrl.trim();
      if (!baseUrl) {
        showNotification(t('notification.openai_test_url_required'), 'error');
        return false;
      }
      const endpoint = buildOpenAIChatCompletionsEndpoint(baseUrl);
      if (!endpoint) {
        showNotification(t('notification.openai_test_url_required'), 'error');
        return false;
      }
      const keyEntry = form.apiKeyEntries[keyIndex];
      const keyAuthIndex = normalizeAuthIndex(keyEntry?.authIndex) ?? undefined;
      if (!keyEntry?.apiKey?.trim() && !keyAuthIndex) {
        setKeyTestStatuses((prev) => {
          const next = [...prev];
          next[keyIndex] = { status: 'error', message: t('notification.openai_test_key_required') };
          return next;
        });
        return false;
      }
      const modelName = testModel.trim() || availableModels[0] || '';
      if (!modelName) {
        showNotification(t('notification.openai_test_model_required'), 'error');
        return false;
      }
      const customHeaders = buildHeaderObject(form.headers);
      const headers: Record<string, string> = {
        'Content-Type': 'application/json',
        ...customHeaders,
      };
      if (!hasHeader(headers, 'authorization')) {
        headers.Authorization = keyAuthIndex
          ? 'Bearer $TOKEN$'
          : `Bearer ${keyEntry.apiKey.trim()}`;
      }
      setKeyTestStatuses((prev) => {
        const next = [...prev];
        next[keyIndex] = { status: 'loading', message: '' };
        return next;
      });
      try {
        const result = await apiCallApi.request(
          {
            authIndex: keyAuthIndex,
            method: 'POST',
            url: endpoint,
            header: Object.keys(headers).length ? headers : undefined,
            data: JSON.stringify({
              model: modelName,
              messages: [{ role: 'user', content: 'Hi' }],
              stream: false,
              max_tokens: 5,
            }),
          },
          { timeout: OPENAI_TEST_TIMEOUT_MS }
        );
        if (result.statusCode < 200 || result.statusCode >= 300)
          throw new Error(getApiCallErrorDetails(result));
        setKeyTestStatuses((prev) => {
          const next = [...prev];
          next[keyIndex] = { status: 'success', message: '' };
          return next;
        });
        return true;
      } catch (err: unknown) {
        const message = getErrorMessage(err);
        const errorCode =
          typeof err === 'object' && err !== null && 'code' in err
            ? String((err as { code?: string }).code)
            : '';
        const isTimeout = errorCode === 'ECONNABORTED' || message.toLowerCase().includes('timeout');
        setKeyTestStatuses((prev) => {
          const next = [...prev];
          next[keyIndex] = {
            status: 'error',
            message: isTimeout
              ? t('ai_providers.openai_test_timeout', { seconds: OPENAI_TEST_TIMEOUT_MS / 1000 })
              : message,
          };
          return next;
        });
        return false;
      }
    },
    [
      availableModels,
      form.apiKeyEntries,
      form.baseUrl,
      form.headers,
      showNotification,
      t,
      testModel,
    ]
  );

  const testSingleKey = useCallback(
    async (keyIndex: number): Promise<boolean> => {
      if (isTestingKeys) return false;
      setIsTestingKeys(true);
      try {
        return await runSingleKeyTest(keyIndex);
      } finally {
        setIsTestingKeys(false);
      }
    },
    [isTestingKeys, runSingleKeyTest]
  );

  const testAllKeys = useCallback(async () => {
    if (isTestingKeys) return;
    const baseUrl = form.baseUrl.trim();
    if (!baseUrl) {
      showNotification(t('notification.openai_test_url_required'), 'error');
      return;
    }
    const modelName = testModel.trim() || availableModels[0] || '';
    if (!modelName) {
      showNotification(t('ai_providers.openai_test_model_required'), 'error');
      return;
    }
    const validKeyIndexes = form.apiKeyEntries
      .map((entry, index) =>
        entry.apiKey?.trim() || normalizeAuthIndex(entry.authIndex) ? index : -1
      )
      .filter((index) => index >= 0);
    if (validKeyIndexes.length === 0) {
      showNotification(t('notification.openai_test_key_required'), 'error');
      return;
    }
    setIsTestingKeys(true);
    setTestStatus('loading');
    setTestMessage(t('ai_providers.openai_test_running'));
    setKeyTestStatuses([]);
    try {
      const results = await Promise.all(validKeyIndexes.map((index) => runSingleKeyTest(index)));
      const successCount = results.filter(Boolean).length;
      const failCount = validKeyIndexes.length - successCount;
      if (failCount === 0) {
        setTestStatus('success');
        setTestMessage(t('ai_providers.openai_test_all_success', { count: successCount }));
      } else if (successCount === 0) {
        setTestStatus('error');
        setTestMessage(t('ai_providers.openai_test_all_failed', { count: failCount }));
      } else {
        setTestStatus('error');
        setTestMessage(
          t('ai_providers.openai_test_all_partial', { success: successCount, failed: failCount })
        );
      }
    } finally {
      setIsTestingKeys(false);
    }
  }, [
    availableModels,
    form.apiKeyEntries,
    form.baseUrl,
    isTestingKeys,
    runSingleKeyTest,
    showNotification,
    t,
    testModel,
  ]);

  const handleSave = useCallback(async () => {
    const name = form.name.trim();
    const baseUrl = form.baseUrl.trim();
    if (!name || !baseUrl) {
      showNotification(t('notification.openai_provider_required'), 'error');
      return;
    }
    const hasValidKey = form.apiKeyEntries.some(
      (entry) => entry.apiKey?.trim() || normalizeAuthIndex(entry.authIndex)
    );
    if (!hasValidKey) {
      showNotification(
        t('ai_providers.openai_key_required', { defaultValue: 'Please add at least one API key' }),
        'error'
      );
      return;
    }
    if (!canSave) return;
    setSaving(true);
    try {
      const payload: OpenAIProviderConfig = {
        name,
        prefix: form.prefix?.trim() || undefined,
        baseUrl,
        headers: buildHeaderObject(form.headers),
        apiKeyEntries: form.apiKeyEntries.map((entry: ApiKeyEntry) => ({
          apiKey: entry.apiKey.trim(),
          proxyUrl: entry.proxyUrl?.trim() || undefined,
          authIndex: normalizeAuthIndex(entry.authIndex) ?? undefined,
          headers: entry.headers,
        })),
      };
      if (form.priority !== undefined && Number.isFinite(form.priority))
        payload.priority = Math.trunc(form.priority);
      if (form.disableCooling !== undefined) payload.disableCooling = form.disableCooling;
      if (initialData?.disabled !== undefined) payload.disabled = initialData.disabled;
      const resolvedTestModel = testModel.trim();
      if (resolvedTestModel) payload.testModel = resolvedTestModel;
      const models = entriesToModels(form.modelEntries);
      if (models.length) payload.models = models;
      const nextList =
        editIndex !== null
          ? providers.map((item, idx) => (idx === editIndex ? payload : item))
          : [...providers, payload];
      if (editIndex !== null) {
        await providersApi.updateOpenAIProvider(providers[editIndex].name, editIndex, payload);
      } else {
        await providersApi.createOpenAIProvider(payload);
      }
      let syncedProviders = nextList;
      try {
        syncedProviders = await providersApi.getOpenAIProviders();
      } catch {
        /* fallback */
      }
      setProviders(syncedProviders);
      updateConfigValue('openai-compatibility', syncedProviders);
      showNotification(
        editIndex !== null
          ? t('notification.openai_provider_updated')
          : t('notification.openai_provider_added'),
        'success'
      );
      onSaved();
      onClose();
    } catch (err: unknown) {
      showNotification(`${t('notification.update_failed')}: ${getErrorMessage(err)}`, 'error');
    } finally {
      setSaving(false);
    }
  }, [
    canSave,
    editIndex,
    form,
    initialData?.disabled,
    onClose,
    onSaved,
    providers,
    showNotification,
    t,
    testModel,
    updateConfigValue,
  ]);

  const modelSelectOptions = useMemo(() => {
    const seen = new Set<string>();
    return form.modelEntries.reduce<Array<{ value: string; label: string }>>((acc, entry) => {
      const name = entry.name.trim();
      if (!name || seen.has(name)) return acc;
      seen.add(name);
      const alias = entry.alias.trim();
      acc.push({ value: name, label: alias && alias !== name ? `${name} (${alias})` : name });
      return acc;
    }, []);
  }, [form.modelEntries]);

  const renderKeyEntries = () => {
    const list = form.apiKeyEntries.length ? form.apiKeyEntries : [buildApiKeyEntry()];
    const updateEntry = (idx: number, field: keyof ApiKeyEntry, value: string) => {
      const next = list.map((entry, i) => (i === idx ? { ...entry, [field]: value } : entry));
      setForm((prev) => ({ ...prev, apiKeyEntries: next }));
      setKeyTestStatuses((prev) => {
        const nextStatuses = [...prev];
        nextStatuses[idx] = { status: 'idle', message: '' };
        return nextStatuses;
      });
    };
    const removeEntry = (idx: number) => {
      const next = list.filter((_, i) => i !== idx);
      setForm((prev) => ({ ...prev, apiKeyEntries: next.length ? next : [buildApiKeyEntry()] }));
      setKeyTestStatuses((prev) => removeKeyTestStatusAtIndex(prev, idx, list.length));
    };
    const addEntry = () => {
      setForm((prev) => ({ ...prev, apiKeyEntries: [...list, buildApiKeyEntry()] }));
      setKeyTestStatuses((prev) => appendIdleKeyTestStatus(prev, list.length));
    };

    return (
      <div className={styles.keyEntriesList}>
        <div className={styles.keyEntriesToolbar}>
          <span className={styles.keyEntriesCount}>
            {t('ai_providers.openai_keys_count')}: {list.length}
          </span>
          <Button
            variant="secondary"
            size="sm"
            onClick={addEntry}
            disabled={saving || disabled || isTestingKeys}
            className={styles.addKeyButton}
          >
            {t('ai_providers.openai_keys_add_btn')}
          </Button>
        </div>
        <div className={styles.keyTableShell}>
          <div className={styles.keyTableHeader}>
            <div className={styles.keyTableColIndex}>#</div>
            <div className={styles.keyTableColStatus}>{t('common.status')}</div>
            <div className={styles.keyTableColKey}>{t('common.api_key')}</div>
            <div className={styles.keyTableColProxy}>{t('common.proxy_url')}</div>
            <div className={styles.keyTableColAction}>{t('common.action')}</div>
          </div>
          {list.map((entry, index) => {
            const keyStatus = keyTestStatuses[index]?.status ?? 'idle';
            const canTestKey =
              Boolean(entry.apiKey?.trim() || normalizeAuthIndex(entry.authIndex)) &&
              hasConfiguredModels;
            return (
              <div key={index} className={styles.keyTableRow}>
                <div className={styles.keyTableColIndex}>{index + 1}</div>
                <div className={styles.keyTableColStatus}>
                  <OpenAIKeyTestStatusIndicator
                    status={keyStatus as 'idle' | 'loading' | 'success' | 'error'}
                    message={keyTestStatuses[index]?.message || ''}
                  />
                </div>
                <div className={styles.keyTableColKey}>
                  <input
                    type="text"
                    value={entry.apiKey}
                    onChange={(e) => updateEntry(index, 'apiKey', e.target.value)}
                    disabled={saving || disabled || isTestingKeys}
                    className={`input ${styles.keyTableInput}`}
                    placeholder={t('ai_providers.openai_key_placeholder')}
                  />
                </div>
                <div className={styles.keyTableColProxy}>
                  <input
                    type="text"
                    value={entry.proxyUrl ?? ''}
                    onChange={(e) => updateEntry(index, 'proxyUrl', e.target.value)}
                    disabled={saving || disabled || isTestingKeys}
                    className={`input ${styles.keyTableInput}`}
                    placeholder={t('ai_providers.openai_proxy_placeholder')}
                  />
                </div>
                <div className={styles.keyTableColAction}>
                  <Button
                    variant="secondary"
                    size="xs"
                    onClick={() => void testSingleKey(index)}
                    disabled={saving || disabled || isTestingKeys || !canTestKey}
                    loading={keyStatus === 'loading'}
                  >
                    {t('ai_providers.openai_test_single_action')}
                  </Button>
                  <Button
                    variant="ghost"
                    size="xs"
                    onClick={() => removeEntry(index)}
                    disabled={saving || disabled || isTestingKeys || list.length <= 1}
                  >
                    {t('common.delete')}
                  </Button>
                </div>
              </div>
            );
          })}
        </div>
      </div>
    );
  };

  const canOpenModelDiscovery =
    !disabled && !saving && !isTestingKeys && Boolean(form.baseUrl?.trim());
  const canApplyModelDiscovery =
    !disabled && !saving && !modelDiscoveryFetching && modelDiscoverySelected.size > 0;

  const footer = (
    <>
      <Button
        variant="secondary"
        size="sm"
        onClick={handleClose}
        disabled={saving || isTestingKeys}
      >
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
        {invalidIndex && (
          <div className={styles.sectionHint}>{t('common.invalid_provider_index')}</div>
        )}
        {!loading && !invalidIndex && (
          <>
            <Input
              label={t('ai_providers.openai_add_modal_name_label')}
              value={form.name}
              onChange={(e) => setForm((prev) => ({ ...prev, name: e.target.value }))}
              disabled={saving || disabled || isTestingKeys}
              required
            />
            <Input
              label={t('ai_providers.openai_add_modal_url_label')}
              value={form.baseUrl}
              onChange={(e) => setForm((prev) => ({ ...prev, baseUrl: e.target.value }))}
              disabled={saving || disabled || isTestingKeys}
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
              disabled={saving || disabled || isTestingKeys}
            />
            <Input
              label={t('ai_providers.prefix_label')}
              placeholder={t('ai_providers.prefix_placeholder')}
              value={form.prefix ?? ''}
              onChange={(e) => setForm((prev) => ({ ...prev, prefix: e.target.value }))}
              hint={t('ai_providers.prefix_hint')}
              disabled={saving || disabled || isTestingKeys}
            />
            <HeaderInputList
              entries={form.headers}
              onChange={(entries) => setForm((prev) => ({ ...prev, headers: entries }))}
              addLabel={t('common.custom_headers_add')}
              keyPlaceholder={t('common.custom_headers_key_placeholder')}
              valuePlaceholder={t('common.custom_headers_value_placeholder')}
              removeButtonTitle={t('common.delete')}
              removeButtonAriaLabel={t('common.delete')}
              disabled={saving || disabled || isTestingKeys}
            />
            <div className="form-group">
              <label>{t('ai_providers.disable_cooling_label')}</label>
              <ToggleSwitch
                checked={Boolean(form.disableCooling)}
                onChange={(value) => setForm((prev) => ({ ...prev, disableCooling: value }))}
                disabled={saving || disabled || isTestingKeys}
                ariaLabel={t('ai_providers.disable_cooling_label')}
              />
              <div className="hint">{t('ai_providers.disable_cooling_hint')}</div>
            </div>

            <div className={styles.keyEntriesSection}>
              <div className={styles.keyEntriesHeader}>
                <label className={styles.keyEntriesTitle}>
                  {t('ai_providers.openai_add_modal_keys_label')}
                </label>
                <span className={styles.keyEntriesHint}>{t('ai_providers.openai_keys_hint')}</span>
              </div>
              {renderKeyEntries()}
            </div>

            <div className={styles.modelConfigSection}>
              <div className={styles.modelConfigHeader}>
                <label className={styles.modelConfigTitle}>
                  {editIndex !== null
                    ? t('ai_providers.openai_edit_modal_models_label')
                    : t('ai_providers.openai_add_modal_models_label')}
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
                    disabled={saving || disabled || isTestingKeys}
                  >
                    {t('ai_providers.openai_models_add_btn')}
                  </Button>
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => setModelDiscoveryOpen(true)}
                    disabled={!canOpenModelDiscovery}
                  >
                    {t('ai_providers.openai_models_fetch_button')}
                  </Button>
                </div>
              </div>
              <div className={styles.sectionHint}>{t('ai_providers.openai_models_hint')}</div>
              <ModelInputList
                entries={form.modelEntries}
                onChange={(entries) => setForm((prev) => ({ ...prev, modelEntries: entries }))}
                namePlaceholder={t('common.model_name_placeholder')}
                aliasPlaceholder={t('common.model_alias_placeholder')}
                disabled={saving || disabled || isTestingKeys}
                hideAddButton
                className={styles.modelInputList}
                rowClassName={styles.modelInputRow}
                inputClassName={styles.modelInputField}
                removeButtonClassName={styles.modelRowRemoveButton}
                removeButtonTitle={t('common.delete')}
                removeButtonAriaLabel={t('common.delete')}
                showForceMapping
                showModalities
                forceMappingLabel={t('ai_providers.force_mapping_label')}
                inputModalitiesPlaceholder={t('ai_providers.input_modalities_placeholder')}
                outputModalitiesPlaceholder={t('ai_providers.output_modalities_placeholder')}
              />
              <div className={styles.modelTestPanel}>
                <div className={styles.modelTestMeta}>
                  <label className={styles.modelTestLabel}>
                    {t('ai_providers.openai_test_title')}
                  </label>
                  <span className={styles.modelTestHint}>{t('ai_providers.openai_test_hint')}</span>
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
                        ? t('ai_providers.openai_test_select_placeholder')
                        : t('ai_providers.openai_test_select_empty')
                    }
                    className={styles.openaiTestSelect}
                    ariaLabel={t('ai_providers.openai_test_title')}
                    disabled={
                      saving ||
                      disabled ||
                      isTestingKeys ||
                      testStatus === 'loading' ||
                      availableModels.length === 0
                    }
                  />
                  <Button
                    variant={testStatus === 'error' ? 'danger' : 'secondary'}
                    size="sm"
                    onClick={() => void testAllKeys()}
                    loading={testStatus === 'loading'}
                    disabled={
                      saving ||
                      disabled ||
                      isTestingKeys ||
                      testStatus === 'loading' ||
                      !hasConfiguredModels ||
                      !hasTestableKeys
                    }
                    className={styles.modelTestAllButton}
                  >
                    {t('ai_providers.openai_test_all_action')}
                  </Button>
                </div>
              </div>
              {testMessage && (
                <div
                  className={`status-badge ${testStatus === 'error' ? 'error' : testStatus === 'success' ? 'success' : 'muted'}`}
                >
                  {testMessage}
                </div>
              )}
            </div>

            <Modal
              open={modelDiscoveryOpen}
              title={t('ai_providers.openai_models_fetch_title')}
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
                      const selected = discoveredModels.filter((m) =>
                        modelDiscoverySelected.has(m.name)
                      );
                      mergeDiscoveredModels(selected);
                      setModelDiscoveryOpen(false);
                    }}
                    disabled={!canApplyModelDiscovery}
                  >
                    {t('ai_providers.openai_models_fetch_apply')}
                  </Button>
                </>
              }
            >
              <div className={styles.openaiModelsContent}>
                <div className={styles.sectionHint}>
                  {t('ai_providers.openai_models_fetch_hint')}
                </div>
                <Input
                  label={t('ai_providers.openai_models_search_label')}
                  placeholder={t('ai_providers.openai_models_search_placeholder')}
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
                    {t('ai_providers.openai_models_fetch_loading')}
                  </div>
                ) : discoveredModels.length === 0 ? (
                  <div className={styles.sectionHint}>
                    {t('ai_providers.openai_models_fetch_empty')}
                  </div>
                ) : discoveredModelsFiltered.length === 0 ? (
                  <div className={styles.sectionHint}>
                    {t('ai_providers.openai_models_search_empty')}
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
                            saving || disabled || modelDiscoveryFetching || alreadyConfigured
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
