import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { usageServiceApi, type ProviderKeyAlias } from '@/services/api/usageService';
import { useAuthStore, useNotificationStore } from '@/stores';
import { usePanelFeatureAvailability } from '@/hooks/usePanelFeatureAvailability';
import { sha256Hex } from '@/utils/apiKeyHash';

type ProviderKeyAliasEditorProps = {
  apiKey: string;
  provider: string;
  disabled?: boolean;
};

export function ProviderKeyAliasEditor({
  apiKey,
  provider,
  disabled = false,
}: ProviderKeyAliasEditorProps) {
  const { t } = useTranslation();
  const managementKey = useAuthStore((state) => state.managementKey);
  const showNotification = useNotificationStore((state) => state.showNotification);
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
    if (!apiKeyHash || !serviceAvailable) {
      setError(t('ai_providers.provider_key_alias_unavailable'));
      return;
    }
    if (nextAlias.length > 120) {
      setError(t('ai_providers.provider_key_alias_too_long'));
      return;
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
      showNotification(t('ai_providers.provider_key_alias_saved'), 'success');
    } catch (err: unknown) {
      const code = err && typeof err === 'object' && 'code' in err ? String(err.code) : '';
      setError(
        code === 'provider_key_alias_duplicate'
          ? t('ai_providers.provider_key_alias_duplicate')
          : err instanceof Error
            ? err.message
            : t('ai_providers.provider_key_alias_save_failed')
      );
    } finally {
      setSaving(false);
    }
  }, [
    alias,
    apiKeyHash,
    managementKey,
    provider,
    savedAlias,
    serviceAvailable,
    serviceBase,
    showNotification,
    t,
  ]);

  return (
    <div>
      <Input
        label={t('ai_providers.provider_key_alias_label')}
        placeholder={t('ai_providers.provider_key_alias_placeholder')}
        hint={t('ai_providers.provider_key_alias_hint')}
        value={alias}
        onChange={(event) => setAlias(event.target.value)}
        disabled={disabled || loading || saving || !apiKeyHash || !serviceAvailable}
        error={error || undefined}
      />
      <Button
        type="button"
        variant="secondary"
        size="sm"
        onClick={() => void save()}
        disabled={
          disabled || loading || saving || !apiKeyHash || !serviceAvailable || alias === savedAlias
        }
        loading={saving}
      >
        {t('ai_providers.provider_key_alias_save')}
      </Button>
    </div>
  );
}
