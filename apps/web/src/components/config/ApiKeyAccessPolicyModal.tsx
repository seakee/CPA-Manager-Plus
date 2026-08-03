import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Modal } from '@/components/ui/Modal';
import {
  nativeKeyPolicyApi,
  type NativeKeyGrant,
  type NativeKeyPolicy
} from '@/services/api/nativeKeyPolicy';

type Props = {
  open: boolean;
  keyHash: string;
  keyLabel: string;
  disabled?: boolean;
  onClose: () => void;
};

const emptyGrant = (): NativeKeyGrant => ({ provider: '', model: '' });

const numberValue = (value: string): number => {
  const parsed = Number.parseInt(value, 10);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : 0;
};

export function ApiKeyAccessPolicyModal({
  open,
  keyHash,
  keyLabel,
  disabled,
  onClose
}: Props) {
  const { t } = useTranslation();
  const [enabled, setEnabled] = useState(true);
  const [grants, setGrants] = useState<NativeKeyGrant[]>([emptyGrant()]);
  const [rpm, setRpm] = useState('0');
  const [dailyCalls, setDailyCalls] = useState('0');
  const [weeklyCalls, setWeeklyCalls] = useState('0');
  const [dailyTokens, setDailyTokens] = useState('0');
  const [weeklyTokens, setWeeklyTokens] = useState('0');
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');

  useEffect(() => {
    if (!open || !keyHash) return;
    let cancelled = false;
    setLoading(true);
    setError('');
    void nativeKeyPolicyApi
      .list()
      .then((policies) => {
        if (cancelled) return;
        const current = policies.find(
          (policy) => policy.key_hash.toLowerCase() === keyHash.toLowerCase()
        );
        setEnabled(current?.enabled ?? true);
        setGrants(current?.grants?.length ? current.grants : [emptyGrant()]);
        setRpm(String(current?.rpm ?? 0));
        setDailyCalls(String(current?.daily_calls ?? 0));
        setWeeklyCalls(String(current?.weekly_calls ?? 0));
        setDailyTokens(String(current?.daily_tokens ?? 0));
        setWeeklyTokens(String(current?.weekly_tokens ?? 0));
      })
      .catch((cause) => {
        if (!cancelled) {
          setError(cause instanceof Error ? cause.message : String(cause));
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [keyHash, open]);

  const updateGrant = (index: number, field: keyof NativeKeyGrant, value: string) => {
    setGrants((previous) =>
      previous.map((grant, grantIndex) =>
        grantIndex === index ? { ...grant, [field]: value } : grant
      )
    );
  };

  const save = async () => {
    const normalizedGrants = grants
      .map((grant) => ({
        provider: grant.provider.trim().toLowerCase(),
        model: grant.model.trim()
      }))
      .filter((grant) => grant.provider && grant.model);
    if (normalizedGrants.length === 0) {
      setError(t('config_management.visual.api_keys.policy_grant_required'));
      return;
    }
    const policy: NativeKeyPolicy = {
      key_hash: keyHash,
      enabled,
      grants: normalizedGrants,
      rpm: numberValue(rpm),
      daily_calls: numberValue(dailyCalls),
      weekly_calls: numberValue(weeklyCalls),
      daily_tokens: numberValue(dailyTokens),
      weekly_tokens: numberValue(weeklyTokens)
    };
    setSaving(true);
    setError('');
    try {
      await nativeKeyPolicyApi.save(policy);
      onClose();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t('config_management.visual.api_keys.policy_title', { name: keyLabel })}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={saving}>
            {t('config_management.visual.common.cancel')}
          </Button>
          <Button onClick={save} disabled={disabled || loading || saving}>
            {t('config_management.visual.api_keys.policy_save')}
          </Button>
        </>
      }
    >
      {loading ? <div className="hint">{t('common.loading')}</div> : null}
      <div className="form-group">
        <label>
          <input
            type="checkbox"
            checked={enabled}
            onChange={(event) => setEnabled(event.target.checked)}
            disabled={disabled || loading || saving}
          />{' '}
          {t('config_management.visual.api_keys.policy_enabled')}
        </label>
        <div className="hint">
          {t('config_management.visual.api_keys.policy_identity_hint')}
        </div>
      </div>

      <div className="form-group">
        <label>{t('config_management.visual.api_keys.policy_grants')}</label>
        {grants.map((grant, index) => (
          <div
            key={`${index}-${grant.provider}-${grant.model}`}
            style={{ display: 'grid', gridTemplateColumns: '1fr 2fr auto', gap: 8, marginBottom: 8 }}
          >
            <input
              className="input"
              value={grant.provider}
              placeholder={t('config_management.visual.api_keys.policy_provider')}
              onChange={(event) => updateGrant(index, 'provider', event.target.value)}
              disabled={disabled || loading || saving}
            />
            <input
              className="input"
              value={grant.model}
              placeholder={t('config_management.visual.api_keys.policy_model')}
              onChange={(event) => updateGrant(index, 'model', event.target.value)}
              disabled={disabled || loading || saving}
            />
            <Button
              variant="danger"
              size="xs"
              onClick={() =>
                setGrants((previous) =>
                  previous.length === 1
                    ? [emptyGrant()]
                    : previous.filter((_, grantIndex) => grantIndex !== index)
                )
              }
              disabled={disabled || loading || saving}
            >
              {t('config_management.visual.common.delete')}
            </Button>
          </div>
        ))}
        <Button
          variant="secondary"
          size="sm"
          onClick={() => setGrants((previous) => [...previous, emptyGrant()])}
          disabled={disabled || loading || saving}
        >
          {t('config_management.visual.api_keys.policy_add_grant')}
        </Button>
      </div>

      <div className="form-group">
        <label>{t('config_management.visual.api_keys.policy_quotas')}</label>
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(2, minmax(0, 1fr))', gap: 8 }}>
          {[
            ['policy_rpm', rpm, setRpm],
            ['policy_daily_calls', dailyCalls, setDailyCalls],
            ['policy_weekly_calls', weeklyCalls, setWeeklyCalls],
            ['policy_daily_tokens', dailyTokens, setDailyTokens],
            ['policy_weekly_tokens', weeklyTokens, setWeeklyTokens]
          ].map(([label, value, setter]) => (
            <label key={String(label)}>
              {t(`config_management.visual.api_keys.${String(label)}`)}
              <input
                className="input"
                type="number"
                min={0}
                value={String(value)}
                onChange={(event) => (setter as (next: string) => void)(event.target.value)}
                disabled={disabled || loading || saving}
              />
            </label>
          ))}
        </div>
        <div className="hint">{t('config_management.visual.api_keys.policy_unlimited_hint')}</div>
      </div>
      {error ? <div className="error-box">{error}</div> : null}
    </Modal>
  );
}
