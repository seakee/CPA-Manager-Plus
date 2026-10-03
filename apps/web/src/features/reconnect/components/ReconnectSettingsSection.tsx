import { useCallback, useEffect, useImperativeHandle, useMemo, useState, type Ref } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { IconRefreshCw } from '@/components/ui/icons';
import { Input } from '@/components/ui/Input';
import { Select } from '@/components/ui/Select';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { usePanelFeatureAvailability } from '@/hooks/usePanelFeatureAvailability';
import {
  RECONNECT_PROVIDERS,
  reconnectApi,
  reconnectErrorMessage,
  type ReconnectProvider,
  type ReconnectSettings,
  type ReconnectSummary,
} from '@/services/api/reconnect';
import { useAuthStore, useNotificationStore } from '@/stores';
import { panelUrlFromLocation, providerLabel, timeZoneOptions } from '../model/reconnectFormat';
import { ReconnectRequestsTable } from './ReconnectRequestsTable';
import styles from './ReconnectSettingsSection.module.scss';

const toInt = (value: string) => Number.parseInt(value, 10) || 0;

// Server-enforced ranges, checked before saving so the field shows the problem.
const RANGES = {
  checkIntervalMinutes: [1, 60],
  linkTtlHours: [1, 72],
  followupHours: [1, 6],
} as const;
type RangedField = keyof typeof RANGES;

const inRange = (field: RangedField, value: number) =>
  Number.isInteger(value) && value >= RANGES[field][0] && value <= RANGES[field][1];

/** Lets the configuration page save and reload this section from its floating bar. */
export type ReconnectSettingsHandle = {
  save: () => Promise<boolean>;
  reload: () => Promise<void>;
};

export type ReconnectSettingsPending = { dirty: boolean };

type ReconnectSettingsSectionProps = {
  handleRef?: Ref<ReconnectSettingsHandle>;
  onPendingChange?: (pending: ReconnectSettingsPending) => void;
};

/**
 * Self-service reconnect settings (Manager Server). When a subscription login
 * in CPA can only recover through a new OAuth login, its owner is messaged a
 * one-time link through the notification webhook and reconnects it.
 */
export function ReconnectSettingsSection({
  handleRef,
  onPendingChange,
}: ReconnectSettingsSectionProps) {
  const { t } = useTranslation();
  const managementKey = useAuthStore((state) => state.managementKey);
  const { showNotification, showConfirmation } = useNotificationStore();
  const base = usePanelFeatureAvailability().managerServiceBase;

  const [form, setForm] = useState<ReconnectSettings | null>(null);
  const [saved, setSaved] = useState<ReconnectSettings | null>(null);
  const [webhookDraft, setWebhookDraft] = useState('');
  const [summary, setSummary] = useState<ReconnectSummary[]>([]);
  const [loadError, setLoadError] = useState('');
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [sending, setSending] = useState(false);
  const [testProvider, setTestProvider] = useState<ReconnectProvider>('claude');
  const [testEmail, setTestEmail] = useState('');
  const [tableVersion, setTableVersion] = useState(0);
  const zones = useMemo(() => timeZoneOptions(), []);

  const load = useCallback(async () => {
    if (!base || !managementKey) return;
    setLoading(true);
    setLoadError('');
    try {
      const loaded = await reconnectApi.getSettings(base, managementKey);
      // First time: suggest the address this panel is open at. It is saved with the next save.
      const settings = loaded.publicUrl
        ? loaded
        : { ...loaded, publicUrl: panelUrlFromLocation(window.location) };
      setForm(settings);
      setSaved(settings);
      setWebhookDraft('');
      setSummary(await reconnectApi.getSummary(base, managementKey).catch(() => []));
    } catch (error) {
      const message = reconnectErrorMessage(error);
      setLoadError(message);
      showNotification(`${t('notification.load_failed')}: ${message}`, 'error');
    } finally {
      setLoading(false);
    }
  }, [base, managementKey, showNotification, t]);

  useEffect(() => {
    void load();
  }, [load]);

  const dirty =
    form !== null &&
    saved !== null &&
    (webhookDraft.trim() !== '' || JSON.stringify(form) !== JSON.stringify(saved));
  const busy = loading || saving;

  const fieldLabel = (field: RangedField) => {
    switch (field) {
      case 'checkIntervalMinutes':
        return t('reconnect.check_interval', { defaultValue: 'Check interval (minutes)' });
      case 'linkTtlHours':
        return t('reconnect.link_ttl', { defaultValue: 'Link lifetime (hours)' });
      case 'followupHours':
        return `${t('reconnect.reminders', { defaultValue: 'Reminders' })} · ${t(
          'reconnect.followup_every',
          { defaultValue: 'Every (hours)' }
        )}`;
    }
  };

  // Like the other Manager Server number fields: checked on save and reported in a toast.
  const rangeProblem = () => {
    if (!form?.enabled) return '';
    const field = (Object.keys(RANGES) as RangedField[]).find((key) => !inRange(key, form[key]));
    return field
      ? t('reconnect.range_invalid', {
          defaultValue: '{{label}} must be an integer from {{min}} to {{max}}',
          label: fieldLabel(field),
          min: RANGES[field][0],
          max: RANGES[field][1],
        })
      : '';
  };

  // Like the configuration page: refreshing asks before dropping unsaved edits.
  const refresh = () => {
    if (!dirty) {
      void load();
      return;
    }
    showConfirmation({
      title: t('common.unsaved_changes_title'),
      message: t('config_management.reload_confirm_message'),
      confirmText: t('config_management.reload'),
      cancelText: t('common.cancel'),
      variant: 'danger',
      onConfirm: async () => {
        await load();
      },
    });
  };

  const update = <K extends keyof ReconnectSettings>(key: K, value: ReconnectSettings[K]) =>
    setForm((current) => (current ? { ...current, [key]: value } : current));

  const save = async (): Promise<boolean> => {
    if (!dirty) return true;
    if (!form || !base || saving) return false;
    const problem = rangeProblem();
    if (problem) {
      showNotification(`${t('notification.save_failed')}: ${problem}`, 'error');
      return false;
    }
    setSaving(true);
    try {
      const next = await reconnectApi.updateSettings(base, managementKey, {
        ...form,
        webhookUrl: webhookDraft.trim(),
      });
      setForm(next);
      setSaved(next);
      setWebhookDraft('');
      showNotification(
        t('reconnect.saved', { defaultValue: 'Self-service reconnect settings saved' }),
        'success'
      );
      return true;
    } catch (error) {
      showNotification(
        `${t('notification.save_failed')}: ${reconnectErrorMessage(error)}`,
        'error'
      );
      return false;
    } finally {
      setSaving(false);
    }
  };

  useImperativeHandle(handleRef, () => ({ save, reload: load }));

  useEffect(() => {
    onPendingChange?.({ dirty });
  }, [dirty, onPendingChange]);

  useEffect(() => () => onPendingChange?.({ dirty: false }), [onPendingChange]);

  const sendTest = async () => {
    if (!base) return;
    const email = testEmail.trim();
    if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) {
      showNotification(
        t('reconnect.invalid_email', { defaultValue: 'Enter a valid email address' }),
        'error'
      );
      return;
    }
    setSending(true);
    try {
      const result = await reconnectApi.send(base, managementKey, testProvider, email);
      if (!result.sent) {
        showNotification(
          result.notice ?? t('reconnect.nothing_sent', { defaultValue: 'Nothing was sent.' }),
          'info',
          10000
        );
      } else {
        const message =
          result.purpose === 'test'
            ? t('reconnect.sent_test', {
                defaultValue: 'Test link sent to {{email}} (their login works)',
                email,
              })
            : result.purpose === 'invite'
              ? t('reconnect.sent_invite', {
                  defaultValue: 'Invitation sent to {{email}} (no login of this type yet)',
                  email,
                })
              : t('reconnect.sent_reconnect', {
                  defaultValue: 'Reconnect link sent to {{email}}',
                  email,
                });
        showNotification(message, 'success');
        setTestEmail('');
        setTableVersion((v) => v + 1);
      }
    } catch (error) {
      showNotification(
        `${t('reconnect.send_failed', { defaultValue: 'Send failed' })}: ${reconnectErrorMessage(error)}`,
        'error'
      );
    } finally {
      setSending(false);
    }
  };

  const hourOptions = (from: number, to: number) =>
    Array.from({ length: to - from + 1 }, (_, i) => ({
      value: String(from + i),
      label: `${from + i}:00`,
    }));

  return (
    <section className={styles.section}>
      <div className={styles.sectionHeader}>
        <div className={styles.sectionHeaderText}>
          <h3 className={styles.sectionTitle}>
            {t('reconnect.section_title', { defaultValue: 'Self-Service Reconnect' })}
          </h3>
          <p className={styles.sectionHint}>
            {t('reconnect.section_hint', {
              defaultValue:
                'When a Claude, Codex, Antigravity, xAI or Muse login can only recover through a new sign-in, its owner (matched by the login email) gets a one-time link and reconnects it themselves.',
            })}
          </p>
        </div>
        <Button variant="ghost" size="sm" onClick={refresh} disabled={busy || sending}>
          <IconRefreshCw size={14} />
          {t('common.refresh')}
        </Button>
      </div>

      {loadError ? (
        <div className={styles.errorState} role="alert">
          <strong>{t('reconnect.load_failed', { defaultValue: 'Load failed' })}</strong>
          <span>{loadError}</span>
        </div>
      ) : null}

      {form ? (
        <>
          <ToggleSwitch
            checked={form.enabled}
            onChange={(value) => update('enabled', value)}
            disabled={busy}
            label={t('reconnect.enabled', { defaultValue: 'Enable self-service reconnect' })}
          />

          {summary.some((item) => item.logins > 0) ? (
            <div className={styles.summary}>
              {summary
                .filter((item) => item.logins > 0)
                .map((item) => (
                  <span className={styles.summaryItem} key={item.provider}>
                    {providerLabel(item.provider)}: {item.logins}
                    {item.needingReconnect > 0 ? (
                      <span className={styles.summaryBroken}>
                        {' '}
                        ·{' '}
                        {t('reconnect.summary_broken', {
                          defaultValue: '{{count}} need reconnecting',
                          count: item.needingReconnect,
                        })}
                      </span>
                    ) : null}
                  </span>
                ))}
            </div>
          ) : null}

          {form.enabled ? (
            <>
              <div className={styles.grid}>
                <Input
                  label={t('reconnect.public_url', { defaultValue: 'Public panel URL' })}
                  hint={t('reconnect.public_url_hint', {
                    defaultValue:
                      'Where people open this panel; links point to {{url}}/management.html#/reconnect/…',
                    url: form.publicUrl || 'https://cpamp.example.com',
                  })}
                  placeholder="https://cpamp.example.com"
                  value={form.publicUrl}
                  onChange={(event) => update('publicUrl', event.target.value)}
                  disabled={busy}
                />
                <Input
                  type="password"
                  autoComplete="new-password"
                  label={t('reconnect.webhook_url', { defaultValue: 'Notification webhook URL' })}
                  hint={
                    saved?.webhookConfigured
                      ? t('reconnect.webhook_configured', {
                          defaultValue:
                            'Saved. Leave blank to keep it, or enter a new URL to replace it.',
                        })
                      : t('reconnect.webhook_hint', {
                          defaultValue:
                            'Receives one POST per message with sendTo (email) and body (HTML), e.g. a Teams Power Automate flow or a Slack workflow.',
                        })
                  }
                  placeholder="https://"
                  value={webhookDraft}
                  onChange={(event) => setWebhookDraft(event.target.value)}
                  autoCorrect="off"
                  autoCapitalize="none"
                  spellCheck={false}
                  data-lpignore="true"
                  data-1p-ignore="true"
                  disabled={busy}
                />
                <Input
                  label={t('reconnect.sender_name', { defaultValue: 'Sender name' })}
                  hint={t('reconnect.sender_name_hint', {
                    defaultValue: 'Shown in messages, e.g. “connected to CPA Manager Plus”.',
                  })}
                  value={form.senderName}
                  onChange={(event) => update('senderName', event.target.value)}
                  disabled={busy}
                />
                <Input
                  type="number"
                  min={1}
                  max={60}
                  label={t('reconnect.check_interval', {
                    defaultValue: 'Check interval (minutes)',
                  })}
                  hint={t('reconnect.check_interval_hint', {
                    defaultValue:
                      '1–60. Owners are messaged after a login stays broken for 10 minutes.',
                  })}
                  value={String(form.checkIntervalMinutes)}
                  onChange={(event) => update('checkIntervalMinutes', toInt(event.target.value))}
                  disabled={busy}
                />
                <Input
                  type="number"
                  min={1}
                  max={72}
                  label={t('reconnect.link_ttl', { defaultValue: 'Link lifetime (hours)' })}
                  hint={t('reconnect.link_ttl_hint', {
                    defaultValue: '1–72. When a link expires, a reminder replaces it.',
                  })}
                  value={String(form.linkTtlHours)}
                  onChange={(event) => update('linkTtlHours', toInt(event.target.value))}
                  disabled={busy}
                />
              </div>

              <div className={styles.group}>
                <h4 className={styles.groupTitle}>
                  {t('reconnect.reminders', { defaultValue: 'Reminders' })}
                </h4>
                <p className={styles.sectionHint}>
                  {t('reconnect.reminders_hint', {
                    defaultValue:
                      'Owners who have not reconnected get a reminder with a fresh link every 1–6 hours, only between these hours.',
                  })}
                </p>
                <div className={styles.reminderGrid}>
                  <Input
                    type="number"
                    min={1}
                    max={6}
                    label={t('reconnect.followup_every', { defaultValue: 'Every (hours)' })}
                    value={String(form.followupHours)}
                    onChange={(event) => update('followupHours', toInt(event.target.value))}
                    disabled={busy}
                  />
                  <div className={styles.field}>
                    <span className={styles.fieldLabel}>
                      {t('reconnect.from_hour', { defaultValue: 'From' })}
                    </span>
                    <Select
                      value={String(form.followupStartHour)}
                      options={hourOptions(0, 23)}
                      onChange={(value) => update('followupStartHour', toInt(value))}
                      triggerClassName={styles.selectTrigger}
                      disabled={busy}
                      ariaLabel={t('reconnect.from_hour', { defaultValue: 'From' })}
                    />
                  </div>
                  <div className={styles.field}>
                    <span className={styles.fieldLabel}>
                      {t('reconnect.until_hour', { defaultValue: 'Until' })}
                    </span>
                    <Select
                      value={String(form.followupEndHour)}
                      options={hourOptions(1, 24)}
                      onChange={(value) => update('followupEndHour', toInt(value))}
                      triggerClassName={styles.selectTrigger}
                      disabled={busy}
                      ariaLabel={t('reconnect.until_hour', { defaultValue: 'Until' })}
                    />
                  </div>
                  <div className={styles.field}>
                    <span className={styles.fieldLabel}>
                      {t('reconnect.time_zone', { defaultValue: 'Time zone' })}
                    </span>
                    <Select
                      value={form.followupTimeZone}
                      options={zones.map((zone) => ({ value: zone, label: zone }))}
                      onChange={(value) => update('followupTimeZone', value)}
                      triggerClassName={styles.selectTrigger}
                      disabled={busy}
                      ariaLabel={t('reconnect.time_zone', { defaultValue: 'Time zone' })}
                    />
                  </div>
                </div>
              </div>
            </>
          ) : null}

          {saved?.enabled ? (
            <>
              <div className={styles.group}>
                <h4 className={styles.groupTitle}>
                  {t('reconnect.send_title', { defaultValue: 'Send a Link' })}
                </h4>
                <div className={styles.sendRow}>
                  <Select
                    value={testProvider}
                    options={RECONNECT_PROVIDERS.map((provider) => ({
                      value: provider,
                      label: providerLabel(provider),
                    }))}
                    onChange={(value) => setTestProvider(value as ReconnectProvider)}
                    triggerClassName={styles.selectTrigger}
                    disabled={sending}
                    ariaLabel={t('reconnect.login_type', { defaultValue: 'Login type' })}
                  />
                  <Input
                    type="email"
                    placeholder="name@example.com"
                    aria-label={t('reconnect.email', { defaultValue: 'Email' })}
                    value={testEmail}
                    onChange={(event) => setTestEmail(event.target.value)}
                    onKeyDown={(event) => {
                      if (event.key === 'Enter' && !sending) void sendTest();
                    }}
                    disabled={sending}
                  />
                  <Button
                    variant="secondary"
                    onClick={() => void sendTest()}
                    loading={sending}
                    disabled={!testEmail.trim()}
                  >
                    {t('reconnect.send', { defaultValue: 'Send' })}
                  </Button>
                </div>
                <p className={styles.sectionHint}>
                  {t('reconnect.send_hint', {
                    defaultValue:
                      'Working login: a test link. No login: an invitation. Broken login: a reconnect request, or nothing if they were already notified.',
                  })}
                </p>
              </div>
              <ReconnectRequestsTable
                base={base}
                managementKey={managementKey}
                timeZone={saved.followupTimeZone}
                version={tableVersion}
              />
            </>
          ) : null}
        </>
      ) : null}
    </section>
  );
}
