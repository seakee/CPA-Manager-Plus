import { forwardRef, useCallback, useEffect, useImperativeHandle, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Input } from '@/components/ui/Input';
import { usageServiceApi, type ProviderKeyAlias } from '@/services/api/usageService';
import { useAuthStore } from '@/stores';
import { usePanelFeatureAvailability } from '@/hooks/usePanelFeatureAvailability';
import { sha256Hex } from '@/utils/apiKeyHash';

export type ProviderKeyAliasEditorHandle = {
  save: () => Promise<void>;
};

type ProviderKeyAliasEditorProps = {
  apiKey: string;
  provider: string;
  disabled?: boolean;
  onDirtyChange?: (dirty: boolean) => void;
};

export const ProviderKeyAliasEditor = forwardRef<
  ProviderKeyAliasEditorHandle,
  ProviderKeyAliasEditorProps
>(function ProviderKeyAliasEditor(
  { apiKey, provider, disabled = false, onDirtyChange },
  ref
) {
  const { t } = useTranslation();
  const managementKey = useAuthStore((state) => state.managementKey);
  const availability = usePanelFeatureAvailability();
  const [alias, setAlias] = useState('');
  const [savedAlias, setSavedAlias] = useState('');
  const [aliases, setAliases] = useState<ProviderKeyAlias[]>([]);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');

  const apiKeyHash = useMemo(() => sha256Hex(apiKey), [apiKey]);
  const serviceBase = availability.managerServiceBase;
  const serviceAvailable = Boolean(
    availability.panelHostConfirmed &&
      availability.panelHostMode === 'manager_embedded' &&
      availability.managerServiceAvailable &&
      serviceBase
  );
  const isDirty = alias.trim() !== savedAlias;

  useEffect(() => {
    onDirtyChange?.(isDirty);
  }, [isDirty, onDirtyChange]);

  useEffect(() => {
    let cancelled = false;
    setAliases([]);
    setError('');
    if (!serviceAvailable) {
      setLoading(false);
      return;
    }

    setLoading(true);
    void usageServiceApi
      .getProviderKeyAliases(serviceBase, managementKey)
      .then((response) => {
        if (cancelled) return;
        setAliases(Array.isArray(response.items) ? response.items : []);
      })
      .catch(() => {
        if (!cancelled) setError(t('ai_providers.provider_key_alias_load_failed'));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });

    return () => {
      cancelled = true;
    };
  }, [managementKey, serviceAvailable, serviceBase, t]);

  useEffect(() => {
    const match = aliases.find(
      (item) =>
        item.provider.toLowerCase() === provider.toLowerCase() &&
        item.apiKeyHash.toLowerCase() === apiKeyHash.toLowerCase()
    );
    const value = match?.alias?.trim() || '';
    setAlias(value);
    setSavedAlias(value);
  }, [aliases, apiKeyHash, provider]);

  const save = useCallback(async () => {
    const nextAlias = alias.trim();
    if (!isDirty) return;
    if (!apiKeyHash || !serviceAvailable) {
      const saveError = new Error(t('ai_providers.provider_key_alias_unavailable'));
      setError(saveError.message);
      throw saveError;
    }
    if (nextAlias.length > 120) {
      const saveError = new Error(t('ai_providers.provider_key_alias_too_long'));
      setError(saveError.message);
      throw saveError;
    }
    setSaving(true);
    setError('');
    try {
      if (!nextAlias) {
        if (savedAlias) {
          await usageServiceApi.deleteProviderKeyAlias(
            serviceBase,
            provider,
            apiKeyHash,
            managementKey
          );
          setAliases((items) =>
            items.filter(
              (item) =>
                item.provider.toLowerCase() !== provider.toLowerCase() ||
                item.apiKeyHash.toLowerCase() !== apiKeyHash.toLowerCase()
            )
          );
        }
      } else {
        const response = await usageServiceApi.saveProviderKeyAlias(
          serviceBase,
          { provider, apiKeyHash, alias: nextAlias },
          managementKey
        );
        setAliases(Array.isArray(response.items) ? response.items : []);
        const persisted = (response.items || []).find(
          (item: ProviderKeyAlias) =>
            item.provider.toLowerCase() === provider.toLowerCase() &&
            item.apiKeyHash.toLowerCase() === apiKeyHash.toLowerCase()
        );
        setAlias(persisted?.alias?.trim() || nextAlias);
      }
      setSavedAlias(nextAlias);
    } catch (err: unknown) {
      const code = err && typeof err === 'object' && 'code' in err ? String(err.code) : '';
      const message =
        code === 'provider_key_alias_duplicate'
          ? t('ai_providers.provider_key_alias_duplicate')
          : err instanceof Error
            ? err.message
            : t('ai_providers.provider_key_alias_save_failed');
      setError(message);
      throw new Error(message);
    } finally {
      setSaving(false);
    }
  }, [
    alias,
    apiKeyHash,
    isDirty,
    managementKey,
    provider,
    savedAlias,
    serviceAvailable,
    serviceBase,
    t,
  ]);

  useImperativeHandle(ref, () => ({ save }), [save]);

  return (
    <Input
      label={t('ai_providers.provider_key_alias_label')}
      placeholder={t('ai_providers.provider_key_alias_placeholder')}
      hint={t('ai_providers.provider_key_alias_hint')}
      value={alias}
      onChange={(event) => setAlias(event.target.value)}
      disabled={disabled || loading || saving || !apiKeyHash || !serviceAvailable}
      error={error || undefined}
    />
  );
});
