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
import { buildHeaderObject, headersToEntries, normalizeHeaderEntries } from '@/utils/headers';
import { normalizeAuthIndex } from '@/utils/authIndex';
import {
  areKeyValueEntriesEqual,
  areModelEntriesEqual,
  areStringArraysEqual,
} from '@/utils/compare';
import {
  excludedModelsToText,
  parseExcludedModels,
  buildClaudeMessagesEndpoint,
  parseTextList,
} from '@/components/providers/utils';
import { modelsToEntries } from '@/components/ui/modelInputListUtils';
import type { ProviderFormState } from '@/components/providers';
import type { ModelInfo } from '@/utils/models';
import styles from '@/features/aiProviders/AiProvidersPage.module.scss';

interface ClaudeEditDrawerProps {
  open: boolean;
  editIndex: number | null;
  disabled: boolean;
  onClose: () => void;
  onSaved: () => void;
}

type ClaudeFormBaseline = ReturnType<typeof buildClaudeBaseline>;

const CLAUDE_TEST_TIMEOUT_MS = 30_000;
const DEFAULT_ANTHROPIC_VERSION = '2023-06-01';

const buildEmptyForm = (): ProviderFormState => ({
  apiKey: '',
  authIndex: '',
  priority: undefined,
  prefix: '',
  baseUrl: '',
  proxyUrl: '',
  headers: [],
  models: [],
  excludedModels: [],
  modelEntries: [{ name: '', alias: '' }],
  excludedText: '',
});

const normalizeClaudeModelEntries = (entries: Array<{ name: string; alias: string }>) =>
  (entries ?? []).reduce<Array<{ name: string; alias: string }>>((acc, entry) => {
    const name = String(entry?.name ?? '').trim();
    let alias = String(entry?.alias ?? '').trim();
    if (name) alias = alias || name;
    if (!name && !alias) return acc;
    acc.push({ name, alias });
    return acc;
  }, []);

const normalizeCloakConfig = (cloak: ProviderFormState['cloak']) => {
  if (!cloak) return null;
  const mode =
    String(cloak.mode ?? '')
      .trim()
      .toLowerCase() || 'auto';
  const strictMode = Boolean(cloak.strictMode);
  const sensitiveWords = Array.isArray(cloak.sensitiveWords)
    ? cloak.sensitiveWords.map((word) => String(word ?? '').trim()).filter(Boolean)
    : [];
  return { mode, strictMode, sensitiveWords: sensitiveWords.length ? sensitiveWords : null };
};

const areCloakConfigsEqual = (
  left: ReturnType<typeof normalizeCloakConfig>,
  right: ReturnType<typeof normalizeCloakConfig>
) => {
  if (left === right) return true;
  if (!left || !right) return false;
  if (left.mode !== right.mode || left.strictMode !== right.strictMode) return false;
  if (left.sensitiveWords === null || right.sensitiveWords === null)
    return left.sensitiveWords === right.sensitiveWords;
  return areStringArraysEqual(left.sensitiveWords, right.sensitiveWords);
};

const buildClaudeBaseline = (form: ProviderFormState) => ({
  apiKey: String(form.apiKey ?? '').trim(),
  authIndex: normalizeAuthIndex(form.authIndex) ?? '',
  priority:
    form.priority !== undefined && Number.isFinite(form.priority)
      ? Math.trunc(form.priority)
      : null,
  prefix: String(form.prefix ?? '').trim(),
  baseUrl: String(form.baseUrl ?? '').trim(),
  proxyUrl: String(form.proxyUrl ?? '').trim(),
  headers: normalizeHeaderEntries(form.headers),
  models: normalizeClaudeModelEntries(form.modelEntries),
  excludedModels: parseExcludedModels(form.excludedText ?? ''),
  cloak: normalizeCloakConfig(form.cloak),
});

const getErrorMessage = (err: unknown) => {
  if (err instanceof Error) return err.message;
  if (typeof err === 'string') return err;
  return '';
};

const hasHeader = (headers: Record<string, string>, name: string) => {
  const target = name.toLowerCase();
  return Object.keys(headers).some((key) => key.toLowerCase() === target);
};

const resolveBearerTokenFromAuthorization = (headers: Record<string, string>): string => {
  const entry = Object.entries(headers).find(([key]) => key.toLowerCase() === 'authorization');
  if (!entry) return '';
  const value = String(entry[1] ?? '').trim();
  if (!value) return '';
  const match = value.match(/^Bearer\s+(.+)$/i);
  return match?.[1]?.trim() || '';
};

export function ClaudeEditDrawer({
  open,
  editIndex,
  disabled,
  onClose,
  onSaved,
}: ClaudeEditDrawerProps) {
  const { t } = useTranslation();
  const { showNotification } = useNotificationStore();
  const fetchConfig = useConfigStore((state) => state.fetchConfig);
  const updateConfigValue = useConfigStore((state) => state.updateConfigValue);
  const clearCache = useConfigStore((state) => state.clearCache);

  const [configs, setConfigs] = useState<ProviderKeyConfig[]>([]);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [form, setForm] = useState<ProviderFormState>(buildEmptyForm);
  const [baseline, setBaseline] = useState<ClaudeFormBaseline>(
    buildClaudeBaseline(buildEmptyForm())
  );
  const [loaded, setLoaded] = useState(false);
  const [isTesting, setIsTesting] = useState(false);
  const [testModel, setTestModel] = useState('');
  const [testStatus, setTestStatus] = useState<'idle' | 'loading' | 'success' | 'error'>('idle');
  const [testMessage, setTestMessage] = useState('');
  const [modelDiscoveryOpen, setModelDiscoveryOpen] = useState(false);
  const [modelDiscoveryFetching, setModelDiscoveryFetching] = useState(false);
  const [modelDiscoveryError, setModelDiscoveryError] = useState('');
  const [discoveredModels, setDiscoveredModels] = useState<ModelInfo[]>([]);
  const [modelDiscoverySearch, setModelDiscoverySearch] = useState('');
  const [modelDiscoverySelected, setModelDiscoverySelected] = useState<Set<string>>(new Set());
  const lastCloakConfigRef = useRef<typeof form.cloak>(null);

  const initialData = useMemo(() => {
    if (editIndex === null) return undefined;
    return configs[editIndex];
  }, [configs, editIndex]);
  const invalidIndex = editIndex !== null && !initialData;

  const title =
    editIndex !== null
      ? t('ai_providers.claude_edit_modal_title')
      : t('ai_providers.claude_add_modal_title');

  const availableModels = useMemo(
    () => form.modelEntries.map((entry) => entry.name.trim()).filter(Boolean),
    [form.modelEntries]
  );

  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    setLoading(true);
    fetchConfig('claude-api-key')
      .then((value) => {
        if (cancelled) return;
        setConfigs(Array.isArray(value) ? (value as ProviderKeyConfig[]) : []);
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        showNotification(`${t('notification.load_failed')}: ${getErrorMessage(err)}`, 'error');
      })
      .finally(() => {
        if (cancelled) return;
        setLoading(false);
        setLoaded(true);
      });
    return () => {
      cancelled = true;
    };
  }, [open, fetchConfig, showNotification, t]);

  useEffect(() => {
    if (!open || !loaded) return;
    if (initialData) {
      const seededForm: ProviderFormState = {
        ...initialData,
        headers: headersToEntries(initialData.headers),
        modelEntries: modelsToEntries(initialData.models),
        excludedText: excludedModelsToText(initialData.excludedModels),
      };
      setForm(seededForm);
      setBaseline(buildClaudeBaseline(seededForm));
      const available = seededForm.modelEntries.map((entry) => entry.name.trim()).filter(Boolean);
      setTestModel(available[0] || '');
    } else {
      const emptyForm = buildEmptyForm();
      setForm(emptyForm);
      setBaseline(buildClaudeBaseline(emptyForm));
      setTestModel('');
    }
    setTestStatus('idle');
    setTestMessage('');
  }, [open, loaded, initialData]);

  useEffect(() => {
    if (!form.cloak) return;
    lastCloakConfigRef.current = form.cloak;
  }, [form.cloak]);

  const canSave = !disabled && !loading && !saving && !invalidIndex && !isTesting;

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
      !areKeyValueEntriesEqual(baseline.headers, normalizeHeaderEntries(form.headers)) ||
      !areModelEntriesEqual(baseline.models, normalizeClaudeModelEntries(form.modelEntries)) ||
      !areStringArraysEqual(
        baseline.excludedModels,
        parseExcludedModels(form.excludedText ?? '')
      ) ||
      !areCloakConfigsEqual(baseline.cloak, normalizeCloakConfig(form.cloak))
    );
  }, [baseline, form]);

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

  const modelDiscoveryEndpoint = useMemo(
    () => modelsApi.buildClaudeModelsEndpoint(form.baseUrl ?? ''),
    [form.baseUrl]
  );

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

  const visibleModelNames = useMemo(
    () => discoveredModelsFiltered.map((model) => model.name),
    [discoveredModelsFiltered]
  );

  const allVisibleSelected = useMemo(
    () =>
      visibleModelNames.length > 0 &&
      visibleModelNames.every((name) => modelDiscoverySelected.has(name)),
    [modelDiscoverySelected, visibleModelNames]
  );

  const cloakModeOptions = useMemo(
    () => [
      { value: 'auto', label: t('ai_providers.claude_cloak_mode_auto') },
      { value: 'always', label: t('ai_providers.claude_cloak_mode_always') },
      { value: 'never', label: t('ai_providers.claude_cloak_mode_never') },
    ],
    [t]
  );

  const resolvedCloakMode = useMemo(() => {
    const mode = (form.cloak?.mode ?? '').trim().toLowerCase();
    if (!mode) return 'auto';
    if (mode === 'provider') return 'auto';
    if (mode === 'auto' || mode === 'always' || mode === 'never') return mode;
    return 'auto';
  }, [form.cloak?.mode]);

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
          t('ai_providers.claude_models_fetch_added', { count: addedCount }),
          'success'
        );
      }
    },
    [showNotification, t]
  );

  const fetchClaudeModelDiscovery = useCallback(async () => {
    setModelDiscoveryFetching(true);
    setModelDiscoveryError('');
    const headerObject = buildHeaderObject(form.headers);

    try {
      const list = await modelsApi.fetchClaudeModelsViaApiCall(
        form.baseUrl ?? '',
        form.apiKey.trim() || undefined,
        headerObject,
        normalizeAuthIndex(form.authIndex) ?? undefined
      );
      setDiscoveredModels(list);
    } catch (err: unknown) {
      setDiscoveredModels([]);
      const message = getErrorMessage(err);
      const hasCustomXApiKey = Object.keys(headerObject).some(
        (key) => key.toLowerCase() === 'x-api-key'
      );
      const hasAuthorization = Object.keys(headerObject).some(
        (key) => key.toLowerCase() === 'authorization'
      );
      const shouldAttachDiag =
        message.toLowerCase().includes('x-api-key') || message.includes('401');
      const diag = shouldAttachDiag
        ? ` [diag: apiKeyField=${form.apiKey.trim() ? 'yes' : 'no'}, customXApiKey=${
            hasCustomXApiKey ? 'yes' : 'no'
          }, customAuthorization=${hasAuthorization ? 'yes' : 'no'}]`
        : '';
      setModelDiscoveryError(
        `${t('ai_providers.claude_models_fetch_error')}: ${message}${diag}`
      );
    } finally {
      setModelDiscoveryFetching(false);
    }
  }, [form.apiKey, form.authIndex, form.baseUrl, form.headers, t]);

  useEffect(() => {
    if (!modelDiscoveryOpen) return;
    setDiscoveredModels([]);
    setModelDiscoverySearch('');
    setModelDiscoverySelected(new Set());
    setModelDiscoveryError('');
    void fetchClaudeModelDiscovery();
  }, [modelDiscoveryOpen, fetchClaudeModelDiscovery]);

  useEffect(() => {
    const availableNames = new Set(discoveredModels.map((model) => model.name));
    setModelDiscoverySelected((prev) => {
      let changed = false;
      const next = new Set<string>();
      prev.forEach((name) => {
        if (availableNames.has(name)) {
          next.add(name);
        } else {
          changed = true;
        }
      });
      return changed ? next : prev;
    });
  }, [discoveredModels]);

  const toggleModelDiscoverySelection = useCallback((name: string) => {
    setModelDiscoverySelected((prev) => {
      const next = new Set(prev);
      if (next.has(name)) next.delete(name);
      else next.add(name);
      return next;
    });
  }, []);

  const handleSelectVisibleModels = useCallback(() => {
    setModelDiscoverySelected((prev) => {
      const next = new Set(prev);
      visibleModelNames.forEach((name) => next.add(name));
      return next;
    });
  }, [visibleModelNames]);

  const handleClearModelDiscoverySelection = useCallback(() => {
    setModelDiscoverySelected(new Set());
  }, []);

  const canOpenModelDiscovery = !disabled && !loading && !saving && !invalidIndex && !isTesting;
  const canApplyModelDiscovery =
    !disabled && !saving && !modelDiscoveryFetching && modelDiscoverySelected.size > 0;

  const runConnectivityTest = useCallback(async () => {
    if (isTesting) return;
    const modelName = testModel.trim() || availableModels[0] || '';
    if (!modelName) {
      showNotification(t('ai_providers.claude_test_model_required'), 'error');
      return;
    }
    const customHeaders = buildHeaderObject(form.headers);
    const apiKey = form.apiKey.trim();
    const keyAuthIndex = normalizeAuthIndex(form.authIndex) ?? undefined;
    const hasApiKeyHeader = hasHeader(customHeaders, 'x-api-key');
    const apiKeyFromAuthorization = resolveBearerTokenFromAuthorization(customHeaders);
    const resolvedApiKey = apiKey || apiKeyFromAuthorization;
    if (!resolvedApiKey && !hasApiKeyHeader && !keyAuthIndex) {
      showNotification(t('ai_providers.claude_test_key_required'), 'error');
      return;
    }
    const endpoint = buildClaudeMessagesEndpoint(form.baseUrl ?? '');
    if (!endpoint) {
      showNotification(t('ai_providers.claude_test_endpoint_invalid'), 'error');
      return;
    }
    const headers: Record<string, string> = {
      'Content-Type': 'application/json',
      ...customHeaders,
    };
    if (!hasHeader(headers, 'anthropic-version'))
      headers['anthropic-version'] = DEFAULT_ANTHROPIC_VERSION;
    if (!Object.prototype.hasOwnProperty.call(headers, 'Anthropic-Version'))
      headers['Anthropic-Version'] = headers['anthropic-version'] ?? DEFAULT_ANTHROPIC_VERSION;
    const tokenValue = resolvedApiKey || (keyAuthIndex ? '$TOKEN$' : '');
    if (!hasApiKeyHeader && tokenValue) headers['x-api-key'] = tokenValue;
    if (!Object.prototype.hasOwnProperty.call(headers, 'X-Api-Key') && tokenValue)
      headers['X-Api-Key'] = tokenValue;

    setIsTesting(true);
    setTestStatus('loading');
    setTestMessage(t('ai_providers.claude_test_running'));
    try {
      const result = await apiCallApi.request(
        {
          method: 'POST',
          authIndex: keyAuthIndex,
          url: endpoint,
          header: headers,
          data: JSON.stringify({
            model: modelName,
            max_tokens: 8,
            messages: [{ role: 'user', content: 'Hi' }],
          }),
        },
        { timeout: CLAUDE_TEST_TIMEOUT_MS }
      );
      if (result.statusCode < 200 || result.statusCode >= 300)
        throw new Error(getApiCallErrorMessage(result));
      const message = t('ai_providers.claude_test_success');
      setTestStatus('success');
      setTestMessage(message);
      showNotification(message, 'success');
    } catch (err: unknown) {
      const message = getErrorMessage(err);
      const errorCode =
        typeof err === 'object' && err !== null && 'code' in err
          ? String((err as { code?: string }).code)
          : '';
      const isTimeout = errorCode === 'ECONNABORTED' || message.toLowerCase().includes('timeout');
      const resolvedMessage = isTimeout
        ? t('ai_providers.claude_test_timeout', { seconds: CLAUDE_TEST_TIMEOUT_MS / 1000 })
        : `${t('ai_providers.claude_test_failed')}: ${message || t('common.unknown_error')}`;
      setTestStatus('error');
      setTestMessage(resolvedMessage);
      showNotification(resolvedMessage, 'error');
    } finally {
      setIsTesting(false);
    }
  }, [
    availableModels,
    form.apiKey,
    form.authIndex,
    form.baseUrl,
    form.headers,
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
        t('ai_providers.claude_key_required', { defaultValue: 'Please enter a Claude API Key' }),
        'error'
      );
      return;
    }
    const baseUrl = (form.baseUrl ?? '').trim();
    if (!baseUrl) {
      showNotification(
        t('ai_providers.claude_base_url_required', {
          defaultValue: 'Please enter the Claude Base URL',
        }),
        'error'
      );
      return;
    }
    setSaving(true);
    try {
      const payload: ProviderKeyConfig = {
        apiKey: form.apiKey.trim(),
        priority: form.priority !== undefined ? Math.trunc(form.priority) : undefined,
        prefix: form.prefix?.trim() || undefined,
        baseUrl: (form.baseUrl ?? '').trim() || undefined,
        proxyUrl: form.proxyUrl?.trim() || undefined,
        headers: buildHeaderObject(form.headers),
        models: form.modelEntries
          .map((entry) => {
            const name = entry.name.trim();
            if (!name) return null;
            const alias = entry.alias.trim();
            return { ...entry, name, alias: alias || name };
          })
          .filter(Boolean) as ProviderKeyConfig['models'],
        excludedModels: parseExcludedModels(form.excludedText),
        cloak: form.cloak,
        authIndex: normalizeAuthIndex(form.authIndex) ?? undefined,
        disableCooling: form.disableCooling,
        experimentalCchSigning: form.experimentalCchSigning,
      };
      const nextList =
        editIndex !== null
          ? configs.map((item, idx) => (idx === editIndex ? payload : item))
          : [...configs, payload];
      await providersApi.saveClaudeConfigs(nextList);
      updateConfigValue('claude-api-key', nextList);
      clearCache('claude-api-key');
      showNotification(
        editIndex !== null
          ? t('notification.claude_config_updated')
          : t('notification.claude_config_added'),
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
    clearCache,
    configs,
    editIndex,
    form,
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
        {loading && <div className={styles.sectionHint}>{t('common.loading')}</div>}
        {invalidIndex && (
          <div className={styles.sectionHint}>{t('common.invalid_provider_index')}</div>
        )}
        {!loading && !invalidIndex && (
          <>
            <Input
              label={t('ai_providers.claude_add_modal_key_label')}
              value={form.apiKey}
              onChange={(e) => setForm((prev) => ({ ...prev, apiKey: e.target.value }))}
              disabled={saving || disabled || isTesting}
              required
            />
            <Input
              label={t('ai_providers.claude_add_modal_url_label')}
              value={form.baseUrl ?? ''}
              onChange={(e) => setForm((prev) => ({ ...prev, baseUrl: e.target.value }))}
              disabled={saving || disabled || isTesting}
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
              disabled={saving || disabled || isTesting}
            />
            <Input
              label={t('ai_providers.prefix_label')}
              placeholder={t('ai_providers.prefix_placeholder')}
              value={form.prefix ?? ''}
              onChange={(e) => setForm((prev) => ({ ...prev, prefix: e.target.value }))}
              hint={t('ai_providers.prefix_hint')}
              disabled={saving || disabled || isTesting}
            />
            <Input
              label={t('ai_providers.claude_add_modal_proxy_label')}
              value={form.proxyUrl ?? ''}
              onChange={(e) => setForm((prev) => ({ ...prev, proxyUrl: e.target.value }))}
              disabled={saving || disabled || isTesting}
            />
            <HeaderInputList
              entries={form.headers}
              onChange={(entries) => setForm((prev) => ({ ...prev, headers: entries }))}
              addLabel={t('common.custom_headers_add')}
              keyPlaceholder={t('common.custom_headers_key_placeholder')}
              valuePlaceholder={t('common.custom_headers_value_placeholder')}
              removeButtonTitle={t('common.delete')}
              removeButtonAriaLabel={t('common.delete')}
              disabled={saving || disabled || isTesting}
            />

            <div className={styles.modelConfigSection}>
              <div className={styles.modelConfigHeader}>
                <label className={styles.modelConfigTitle}>
                  {t('ai_providers.claude_models_label')}
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
                    disabled={saving || disabled || isTesting}
                  >
                    {t('ai_providers.claude_models_add_btn')}
                  </Button>
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => setModelDiscoveryOpen(true)}
                    disabled={!canOpenModelDiscovery}
                  >
                    {t('ai_providers.claude_models_fetch_button')}
                  </Button>
                </div>
              </div>
              <div className={styles.sectionHint}>{t('ai_providers.claude_models_hint')}</div>
              <ModelInputList
                entries={form.modelEntries}
                onChange={(entries) => setForm((prev) => ({ ...prev, modelEntries: entries }))}
                namePlaceholder={t('common.model_name_placeholder')}
                aliasPlaceholder={t('common.model_alias_placeholder')}
                disabled={saving || disabled || isTesting}
                hideAddButton
                className={styles.modelInputList}
                rowClassName={styles.modelInputRow}
                inputClassName={styles.modelInputField}
                removeButtonClassName={styles.modelRowRemoveButton}
                removeButtonTitle={t('common.delete')}
                removeButtonAriaLabel={t('common.delete')}
              />

              <div className={styles.modelTestPanel}>
                <div className={styles.modelTestMeta}>
                  <label className={styles.modelTestLabel}>
                    {t('ai_providers.claude_test_title')}
                  </label>
                  <span className={styles.modelTestHint}>{t('ai_providers.claude_test_hint')}</span>
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
                        ? t('ai_providers.claude_test_select_placeholder')
                        : t('ai_providers.claude_test_select_empty')
                    }
                    className={styles.openaiTestSelect}
                    ariaLabel={t('ai_providers.claude_test_title')}
                    disabled={
                      saving ||
                      disabled ||
                      isTesting ||
                      testStatus === 'loading' ||
                      availableModels.length === 0
                    }
                  />
                  <Button
                    variant={testStatus === 'error' ? 'danger' : 'secondary'}
                    size="sm"
                    onClick={() => void runConnectivityTest()}
                    loading={testStatus === 'loading'}
                    disabled={
                      saving ||
                      disabled ||
                      isTesting ||
                      testStatus === 'loading' ||
                      availableModels.length === 0
                    }
                    className={styles.modelTestAllButton}
                  >
                    {t('ai_providers.claude_test_action')}
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

            <div className="form-group">
              <label>{t('ai_providers.excluded_models_label')}</label>
              <textarea
                className="input"
                placeholder={t('ai_providers.excluded_models_placeholder')}
                value={form.excludedText}
                onChange={(e) => setForm((prev) => ({ ...prev, excludedText: e.target.value }))}
                rows={4}
                disabled={saving || disabled || isTesting}
              />
              <div className="hint">{t('ai_providers.excluded_models_hint')}</div>
            </div>

            <Modal
              open={modelDiscoveryOpen}
              title={t('ai_providers.claude_models_fetch_title')}
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
                      const selectedModels = discoveredModels.filter((model) =>
                        modelDiscoverySelected.has(model.name)
                      );
                      mergeDiscoveredModels(selectedModels);
                      setModelDiscoveryOpen(false);
                    }}
                    disabled={!canApplyModelDiscovery}
                  >
                    {t('ai_providers.claude_models_fetch_apply')}
                  </Button>
                </>
              }
            >
              <div className={styles.openaiModelsContent}>
                <div className={styles.sectionHint}>
                  {t('ai_providers.claude_models_fetch_hint')}
                </div>
                <div className={styles.openaiModelsEndpointSection}>
                  <label className={styles.openaiModelsEndpointLabel}>
                    {t('ai_providers.claude_models_fetch_url_label')}
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
                      onClick={() => void fetchClaudeModelDiscovery()}
                      loading={modelDiscoveryFetching}
                      disabled={disabled || saving}
                    >
                      {t('ai_providers.claude_models_fetch_refresh')}
                    </Button>
                  </div>
                </div>
                <Input
                  label={t('ai_providers.claude_models_search_label')}
                  placeholder={t('ai_providers.claude_models_search_placeholder')}
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
                          discoveredModelsFiltered.length === 0 ||
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
                    {t('ai_providers.claude_models_fetch_loading')}
                  </div>
                ) : discoveredModels.length === 0 ? (
                  <div className={styles.sectionHint}>
                    {t('ai_providers.claude_models_fetch_empty')}
                  </div>
                ) : discoveredModelsFiltered.length === 0 ? (
                  <div className={styles.sectionHint}>
                    {t('ai_providers.claude_models_search_empty')}
                  </div>
                ) : (
                  <div className={styles.modelDiscoveryList}>
                    {discoveredModelsFiltered.map((model) => {
                      const checked = modelDiscoverySelected.has(model.name);
                      return (
                        <SelectionCheckbox
                          key={model.name}
                          checked={checked}
                          onChange={() => toggleModelDiscoverySelection(model.name)}
                          disabled={disabled || saving || modelDiscoveryFetching}
                          ariaLabel={model.name}
                          className={`${styles.modelDiscoveryRow} ${
                            checked ? styles.modelDiscoveryRowSelected : ''
                          }`}
                          labelClassName={styles.modelDiscoverySelectionLabel}
                          label={
                            <div className={styles.modelDiscoveryMeta}>
                              <div className={styles.modelDiscoveryName}>
                                {model.name}
                                {model.alias && (
                                  <span className={styles.modelDiscoveryAlias}>{model.alias}</span>
                                )}
                              </div>
                              {model.description && (
                                <div className={styles.modelDiscoveryDesc}>
                                  {model.description}
                                </div>
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

            <div className={styles.modelConfigSection}>
              <div className={styles.modelConfigHeader}>
                <label className={styles.modelConfigTitle}>
                  {t('ai_providers.claude_cloak_title')}
                </label>
                <div className={styles.modelConfigToolbar}>
                  <ToggleSwitch
                    checked={Boolean(form.cloak)}
                    onChange={(enabled) =>
                      setForm((prev) => {
                        if (!enabled) {
                          if (prev.cloak) lastCloakConfigRef.current = prev.cloak;
                          return { ...prev, cloak: undefined };
                        }
                        const restored = prev.cloak ??
                          lastCloakConfigRef.current ?? {
                            mode: 'auto',
                            strictMode: false,
                            sensitiveWords: [],
                          };
                        return {
                          ...prev,
                          cloak: {
                            mode: String(restored.mode ?? 'auto').trim() || 'auto',
                            strictMode: restored.strictMode ?? false,
                            sensitiveWords: restored.sensitiveWords ?? [],
                            cacheUserId: restored.cacheUserId,
                          },
                        };
                      })
                    }
                    disabled={saving || disabled || isTesting}
                    ariaLabel={t('ai_providers.claude_cloak_toggle_aria')}
                    label={t('ai_providers.claude_cloak_toggle_label')}
                  />
                </div>
              </div>
              <div className={styles.sectionHint}>{t('ai_providers.claude_cloak_hint')}</div>
              {form.cloak ? (
                <>
                  <div className="form-group">
                    <label>{t('ai_providers.claude_cloak_mode_label')}</label>
                    <Select
                      value={resolvedCloakMode}
                      options={cloakModeOptions}
                      onChange={(value) =>
                        setForm((prev) => ({
                          ...prev,
                          cloak: { ...(prev.cloak ?? {}), mode: value },
                        }))
                      }
                      ariaLabel={t('ai_providers.claude_cloak_mode_label')}
                      disabled={saving || disabled || isTesting}
                    />
                    <div className="hint">{t('ai_providers.claude_cloak_mode_hint')}</div>
                  </div>
                  <div className="form-group">
                    <label>{t('ai_providers.claude_cloak_strict_label')}</label>
                    <ToggleSwitch
                      checked={Boolean(form.cloak.strictMode)}
                      onChange={(value) =>
                        setForm((prev) => ({
                          ...prev,
                          cloak: { ...(prev.cloak ?? {}), strictMode: value },
                        }))
                      }
                      disabled={saving || disabled || isTesting}
                      ariaLabel={t('ai_providers.claude_cloak_strict_label')}
                    />
                    <div className="hint">{t('ai_providers.claude_cloak_strict_hint')}</div>
                  </div>
                  <div className="form-group">
                    <label>{t('ai_providers.claude_cloak_sensitive_words_label')}</label>
                    <textarea
                      className="input"
                      placeholder={t('ai_providers.claude_cloak_sensitive_words_placeholder')}
                      value={(form.cloak.sensitiveWords ?? []).join('\n')}
                      onChange={(e) => {
                        const nextWords = parseTextList(e.target.value);
                        setForm((prev) => ({
                          ...prev,
                          cloak: {
                            ...(prev.cloak ?? {}),
                            sensitiveWords: nextWords.length ? nextWords : undefined,
                          },
                        }));
                      }}
                      rows={3}
                      disabled={saving || disabled || isTesting}
                    />
                    <div className="hint">
                      {t('ai_providers.claude_cloak_sensitive_words_hint')}
                    </div>
                  </div>
                </>
              ) : null}
            </div>
          </>
        )}
      </div>
    </Drawer>
  );
}
