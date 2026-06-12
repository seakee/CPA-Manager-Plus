import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Drawer } from '@/components/ui/Drawer';
import { Input } from '@/components/ui/Input';
import { HeaderInputList } from '@/components/ui/HeaderInputList';
import { ModelInputList } from '@/components/ui/ModelInputList';
import { providersApi } from '@/services/api';
import { useConfigStore, useNotificationStore } from '@/stores';
import type { ProviderKeyConfig } from '@/types';
import { buildHeaderObject, headersToEntries, normalizeHeaderEntries } from '@/utils/headers';
import { areKeyValueEntriesEqual, areModelEntriesEqual, areStringArraysEqual } from '@/utils/compare';
import { excludedModelsToText, parseExcludedModels } from '@/components/providers/utils';
import type { VertexFormState } from '@/components/providers';
import styles from '@/features/aiProviders/AiProvidersPage.module.scss';

interface VertexEditDrawerProps {
  open: boolean;
  editIndex: number | null;
  disabled: boolean;
  onClose: () => void;
  onSaved: () => void;
}

type VertexFormBaseline = ReturnType<typeof buildVertexBaseline>;

const buildEmptyForm = (): VertexFormState => ({
  apiKey: '',
  prefix: '',
  baseUrl: '',
  proxyUrl: '',
  headers: [],
  models: [],
  excludedModels: [],
  modelEntries: [{ name: '', alias: '' }],
  excludedText: '',
});

const normalizeModelEntries = (entries: Array<{ name: string; alias: string }>) =>
  (entries ?? []).reduce<Array<{ name: string; alias: string }>>((acc, entry) => {
    const name = String(entry?.name ?? '').trim();
    const alias = String(entry?.alias ?? '').trim();
    if (!name && !alias) return acc;
    acc.push({ name, alias });
    return acc;
  }, []);

const buildVertexBaseline = (form: VertexFormState) => ({
  apiKey: String(form.apiKey ?? '').trim(),
  priority: form.priority !== undefined && Number.isFinite(form.priority) ? Math.trunc(form.priority) : null,
  prefix: String(form.prefix ?? '').trim(),
  baseUrl: String(form.baseUrl ?? '').trim(),
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

export function VertexEditDrawer({ open, editIndex, disabled, onClose, onSaved }: VertexEditDrawerProps) {
  const { t } = useTranslation();
  const { showNotification } = useNotificationStore();
  const fetchConfig = useConfigStore((state) => state.fetchConfig);
  const updateConfigValue = useConfigStore((state) => state.updateConfigValue);
  const clearCache = useConfigStore((state) => state.clearCache);

  const [configs, setConfigs] = useState<ProviderKeyConfig[]>([]);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [form, setForm] = useState<VertexFormState>(buildEmptyForm);
  const [baseline, setBaseline] = useState<VertexFormBaseline>(buildVertexBaseline(buildEmptyForm()));
  const [loaded, setLoaded] = useState(false);

  const initialData = useMemo(() => {
    if (editIndex === null) return undefined;
    return configs[editIndex];
  }, [configs, editIndex]);
  const invalidIndex = editIndex !== null && !initialData;

  const title = editIndex !== null ? t('ai_providers.vertex_edit_modal_title') : t('ai_providers.vertex_add_modal_title');

  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    setLoading(true);
    setError('');
    Promise.all([fetchConfig('vertex-api-key'), providersApi.getVertexConfigs()])
      .then(([configResult, vertexResult]) => {
        if (cancelled) return;
        const list = Array.isArray(vertexResult) ? (vertexResult as ProviderKeyConfig[])
          : Array.isArray(configResult) ? (configResult as ProviderKeyConfig[]) : [];
        setConfigs(list);
        updateConfigValue('vertex-api-key', list);
        clearCache('vertex-api-key');
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
    return () => { cancelled = true; };
  }, [open, clearCache, fetchConfig, t, updateConfigValue]);

  useEffect(() => {
    if (!open || !loaded) return;
    if (initialData) {
      const nextForm: VertexFormState = {
        ...initialData,
        headers: headersToEntries(initialData.headers),
        modelEntries: initialData.models?.map((m) => ({ name: m.name, alias: m.alias ?? '' })) ?? [{ name: '', alias: '' }],
        excludedText: excludedModelsToText(initialData.excludedModels),
      };
      setForm(nextForm);
      setBaseline(buildVertexBaseline(nextForm));
    } else {
      const nextForm = buildEmptyForm();
      setForm(nextForm);
      setBaseline(buildVertexBaseline(nextForm));
    }
  }, [open, loaded, initialData]);

  const canSave = !disabled && !saving && !loading && !invalidIndex;

  const isDirty = useMemo(() => {
    const normalizedPriority = form.priority !== undefined && Number.isFinite(form.priority) ? Math.trunc(form.priority) : null;
    return (
      baseline.apiKey !== form.apiKey.trim() ||
      baseline.priority !== normalizedPriority ||
      baseline.prefix !== String(form.prefix ?? '').trim() ||
      baseline.baseUrl !== String(form.baseUrl ?? '').trim() ||
      baseline.proxyUrl !== String(form.proxyUrl ?? '').trim() ||
      !areKeyValueEntriesEqual(baseline.headers, normalizeHeaderEntries(form.headers)) ||
      !areModelEntriesEqual(baseline.models, normalizeModelEntries(form.modelEntries)) ||
      !areStringArraysEqual(baseline.excludedModels, parseExcludedModels(form.excludedText ?? ''))
    );
  }, [baseline, form]);

  const handleSave = useCallback(async () => {
    if (!canSave) return;
    const apiKey = form.apiKey.trim();
    if (!apiKey) {
      showNotification(t('ai_providers.vertex_key_required', { defaultValue: 'Please enter a Vertex API Key' }), 'error');
      return;
    }
    const baseUrl = (form.baseUrl ?? '').trim();
    if (!baseUrl) {
      showNotification(t('ai_providers.vertex_base_url_required', { defaultValue: 'Please enter the Vertex Base URL' }), 'error');
      return;
    }
    setSaving(true);
    setError('');
    try {
      const payload: ProviderKeyConfig = {
        apiKey: form.apiKey.trim(),
        priority: form.priority !== undefined && Number.isFinite(form.priority) ? Math.trunc(form.priority) : undefined,
        prefix: form.prefix?.trim() || undefined,
        baseUrl: (form.baseUrl ?? '').trim() || undefined,
        proxyUrl: form.proxyUrl?.trim() || undefined,
        headers: buildHeaderObject(form.headers),
        models: form.modelEntries.map((entry) => {
          const name = entry.name.trim();
          const alias = entry.alias.trim();
          if (!name || !alias) return null;
          return { name, alias };
        }).filter(Boolean) as ProviderKeyConfig['models'],
        excludedModels: parseExcludedModels(form.excludedText),
      };
      const nextList = editIndex !== null
        ? configs.map((item, idx) => (idx === editIndex ? payload : item))
        : [...configs, payload];
      await providersApi.saveVertexConfigs(nextList);
      updateConfigValue('vertex-api-key', nextList);
      clearCache('vertex-api-key');
      showNotification(editIndex !== null ? t('notification.vertex_config_updated') : t('notification.vertex_config_added'), 'success');
      onSaved();
      onClose();
    } catch (err: unknown) {
      setError(getErrorMessage(err));
      showNotification(`${t('notification.update_failed')}: ${getErrorMessage(err)}`, 'error');
    } finally {
      setSaving(false);
    }
  }, [canSave, clearCache, configs, editIndex, form, onClose, onSaved, showNotification, t, updateConfigValue]);

  const handleClose = useCallback(() => {
    if (isDirty && !saving) {
      if (!window.confirm(t('common.unsaved_changes_message'))) return;
    }
    onClose();
  }, [isDirty, onClose, saving, t]);

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
            <Input label={t('ai_providers.vertex_add_modal_key_label')} placeholder={t('ai_providers.vertex_add_modal_key_placeholder')}
              value={form.apiKey} onChange={(e) => setForm((prev) => ({ ...prev, apiKey: e.target.value }))}
              disabled={disabled || saving} required />
            <Input label={t('ai_providers.vertex_add_modal_url_label')} placeholder={t('ai_providers.vertex_add_modal_url_placeholder')}
              value={form.baseUrl ?? ''} onChange={(e) => setForm((prev) => ({ ...prev, baseUrl: e.target.value }))}
              disabled={disabled || saving} required />
            <Input label={t('ai_providers.prefix_label')} placeholder={t('ai_providers.prefix_placeholder')}
              value={form.prefix ?? ''} onChange={(e) => setForm((prev) => ({ ...prev, prefix: e.target.value }))}
              hint={t('ai_providers.prefix_hint')} disabled={disabled || saving} />
            <Input label={t('ai_providers.vertex_add_modal_proxy_label')} placeholder={t('ai_providers.vertex_add_modal_proxy_placeholder')}
              value={form.proxyUrl ?? ''} onChange={(e) => setForm((prev) => ({ ...prev, proxyUrl: e.target.value }))}
              disabled={disabled || saving} />
            <HeaderInputList entries={form.headers}
              onChange={(entries) => setForm((prev) => ({ ...prev, headers: entries }))}
              addLabel={t('common.custom_headers_add')} keyPlaceholder={t('common.custom_headers_key_placeholder')}
              valuePlaceholder={t('common.custom_headers_value_placeholder')}
              removeButtonTitle={t('common.delete')} removeButtonAriaLabel={t('common.delete')}
              disabled={disabled || saving} />
            <div className="form-group">
              <label>{t('ai_providers.vertex_models_label')}</label>
              <ModelInputList entries={form.modelEntries}
                onChange={(entries) => setForm((prev) => ({ ...prev, modelEntries: entries }))}
                addLabel={t('ai_providers.vertex_models_add_btn')}
                namePlaceholder={t('common.model_name_placeholder')} aliasPlaceholder={t('common.model_alias_placeholder')}
                removeButtonTitle={t('common.delete')} removeButtonAriaLabel={t('common.delete')}
                disabled={disabled || saving} />
            </div>
            <div className="form-group">
              <label>{t('ai_providers.excluded_models_label')}</label>
              <textarea className="input" placeholder={t('ai_providers.excluded_models_placeholder')}
                value={form.excludedText} onChange={(e) => setForm((prev) => ({ ...prev, excludedText: e.target.value }))}
                rows={4} disabled={disabled || saving} />
              <div className="hint">{t('ai_providers.excluded_models_hint')}</div>
            </div>
          </>
        )}
      </div>
    </Drawer>
  );
}
