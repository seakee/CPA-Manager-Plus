import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { useAuthStore } from '@/stores';
import {
  PLUGIN_SWITCH_GROUP,
  PLUGIN_SWITCH_PATH,
  newPluginRequestId,
  readPluginRuntime,
  reconcilePluginSwitch,
  savePluginEnabled,
  type PluginRuntimeView,
} from './pluginRuntimeClient';
import { createPublicApiClient, PublicApiError, type PublicApiClient } from '@/fullweb/publicApi';
import styles from './PluginRuntimeSwitch.module.scss';

const defaultClient = createPublicApiClient();
const SUCCESS_NOTICE_AUTO_HIDE_MS = 4_000;

type PluginNoticeTone = 'success' | 'warning' | 'error';

/** The switch as the Manager's document carries it. */
interface PluginSwitchState {
  /** Whether the project writes the key at all; absent is not an explicit off. */
  configured: boolean;
  enabled: boolean;
}

function pluginSwitchState(view: PluginRuntimeView | null): PluginSwitchState {
  const field = view?.fields.find((entry) => entry.path === PLUGIN_SWITCH_PATH);
  if (!field || field.presence !== 'present' || typeof field.value !== 'boolean') {
    return { configured: false, enabled: false };
  }
  return { configured: true, enabled: field.value };
}

/**
 * The plugin runtime switch: the one control that writes `plugins.enabled` for a
 * managed installation.
 *
 * It is offered only when the Manager reports that the managed authority can write
 * it — every other profile owns its own CPA and is refused server-side, so a
 * control there would be a promise the server keeps by declining. The persisted
 * readout reports the value CPA is serving right now: it is read back from CPA's
 * own configuration document, so it is the effective value rather than a claim
 * the page makes about its last write. There is deliberately no second readout:
 * the Manager never observes an "applied" signal separate from that document, so
 * any second value could only repeat this one or be a constant.
 * An unknown write result is settled by reading the document back from the
 * journaled request ID, never by sending the write again.
 */
export function PluginRuntimeSwitch({
  client = defaultClient,
  credential,
  onApplied,
}: {
  client?: PublicApiClient;
  credential?: string;
  /** Called after the switch is proven persisted, so the plugin list re-reads. */
  onApplied?: () => void;
} = {}) {
  const { t } = useTranslation();
  const sessionCredential = useAuthStore((state) => state.managementKey);
  const adminCredential = credential ?? sessionCredential;
  const [view, setView] = useState<PluginRuntimeView | null>(null);
  const [busy, setBusy] = useState(false);
  const [reconciling, setReconciling] = useState(false);
  const [notice, setNotice] = useState('');
  const [noticeTone, setNoticeTone] = useState<PluginNoticeTone>('warning');
  const mounted = useRef(true);
  const requestSeq = useRef(0);
  const noticeTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const clearNoticeTimer = useCallback(() => {
    if (noticeTimerRef.current !== null) {
      clearTimeout(noticeTimerRef.current);
      noticeTimerRef.current = null;
    }
  }, []);

  const showNotice = useCallback(
    (message: string, tone: PluginNoticeTone, autoHide = false) => {
      clearNoticeTimer();
      setNotice(message);
      setNoticeTone(tone);
      if (autoHide && message) {
        noticeTimerRef.current = setTimeout(() => {
          noticeTimerRef.current = null;
          if (mounted.current) {
            setNotice('');
          }
        }, SUCCESS_NOTICE_AUTO_HIDE_MS);
      }
    },
    [clearNoticeTimer]
  );

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      clearNoticeTimer();
    };
  }, [clearNoticeTimer]);
  const load = useCallback(async () => {
    if (!adminCredential) return null;
    const seq = ++requestSeq.current;
    try {
      const next = await readPluginRuntime(client, { kind: 'admin', value: adminCredential });
      if (seq !== requestSeq.current || !mounted.current) return next;
      setView(next);
      return next;
    } catch {
      // No verified Full Web routing, no plugin authority or a Manager that does
      // not have the route: this page simply has no switch to show.
      if (seq === requestSeq.current && mounted.current) setView(null);
      return null;
    }
  }, [adminCredential, client]);

  useEffect(() => {
    void load();
  }, [load]);

  const group = view?.groups.find((entry) => entry.id === PLUGIN_SWITCH_GROUP);
  const capability = view?.capability;
  const state = pluginSwitchState(view);
  const journaled = group?.operation;
  const unresolved = journaled?.state === 'unknown';
  const writable = Boolean(capability?.writable && group?.writable);

  const reconcile = useCallback(
    async (requestId: string) => {
      if (!adminCredential || !requestId) return;
      setReconciling(true);
      try {
        const result = await reconcilePluginSwitch(
          client,
          { kind: 'admin', value: adminCredential },
          requestId
        );
        if (result.operation.state === 'persisted') {
          onApplied?.();
        }
        const next = await load();
        if (!mounted.current) return;
        if (result.operation.state === 'persisted') {
          if (result.operation.code === 'superseded') {
            showNotice(t('plugin_management.runtime_superseded'), 'warning');
          } else {
            showNotice(t('plugin_management.runtime_reconciled'), 'success', true);
          }
        } else if (result.operation.state === 'failed') {
          showNotice(t('plugin_management.runtime_not_applied'), 'warning');
        } else if (next) {
          showNotice(t('plugin_management.runtime_pending'), 'warning');
        }
      } catch (error) {
        showNotice(
          error instanceof PublicApiError && error.kind === 'routing_unavailable'
            ? ''
            : t('plugin_management.runtime_pending'),
          'warning'
        );
      } finally {
        if (mounted.current) setReconciling(false);
      }
    },
    [adminCredential, client, load, onApplied, showNotice, t]
  );

  const apply = async (enabled: boolean) => {
    if (!view || !group || busy || reconciling) return;
    setBusy(true);
    showNotice('', 'success');
    const requestId = newPluginRequestId();
    try {
      const saved = await savePluginEnabled(
        client,
        { kind: 'admin', value: adminCredential },
        { requestId, generation: view.generation, baseline: group.baseline, enabled }
      );
      onApplied?.();
      await load();
      if (!mounted.current) return;
      if (saved.operation.code === 'superseded') {
        showNotice(t('plugin_management.runtime_superseded'), 'warning');
      } else {
        // CPA triggers an asynchronous hot reload on config write; show a brief
        // non-alarming confirmation while the persisted readout reflects the write.
        showNotice(t('plugin_management.runtime_saved_effect_unknown'), 'success', true);
      }
    } catch (error) {
      // Nothing proves the document is unchanged, so the journaled operation is
      // the only way forward: re-read and settle it through read-back.
      const next = await load();
      const operation = next?.groups.find((entry) => entry.id === PLUGIN_SWITCH_GROUP)?.operation;
      if (operation?.state === 'unknown') {
        await reconcile(operation.requestId);
        return;
      }
      if (!mounted.current) return;
      const code = error instanceof PublicApiError ? error.code ?? '' : '';
      if (code === 'source_or_operation_conflict' || code === 'generation_mismatch') {
        showNotice(t('plugin_management.runtime_conflict'), 'warning');
      } else if (code === 'plugin_switch_requires_managed_install') {
        showNotice(t('plugin_management.runtime_refused'), 'warning');
      } else {
        showNotice(t('plugin_management.runtime_save_failed'), 'error');
      }
    } finally {
      if (mounted.current) setBusy(false);
    }
  };

  // Visible only where the managed authority can write it.
  if (!view || !capability || !capability.writable || capability.mode !== 'managed') return null;

  return (
    <section className={styles.switchSurface} data-testid="plugin-runtime-switch">
      <div className={styles.switchCopy}>
        <div className={styles.switchHeader}>
          <div className={styles.switchTitle}>{t('plugin_management.runtime_title')}</div>
          <span
            className={styles.readoutItem}
            data-tone={state.configured ? (state.enabled ? 'on' : 'off') : 'unset'}
          >
            <span className={styles.readoutLabel}>{t('plugin_management.runtime_persisted')}</span>
            <strong data-testid="plugin-runtime-persisted">
              {state.configured
                ? state.enabled
                  ? t('plugin_management.runtime_on')
                  : t('plugin_management.runtime_off')
                : t('plugin_management.runtime_unset')}
            </strong>
          </span>
        </div>
        <p className={styles.switchDescription}>{t('plugin_management.runtime_description')}</p>
        {notice ? (
          <p
            className={styles.switchNotice}
            data-tone={noticeTone}
            data-testid="plugin-runtime-notice"
            role={noticeTone === 'error' ? 'alert' : 'status'}
          >
            {notice}
          </p>
        ) : null}
      </div>
      <div className={styles.switchControls}>
        {unresolved && journaled ? (
          <Button
            variant="secondary"
            size="sm"
            loading={reconciling}
            onClick={() => void reconcile(journaled.requestId)}
          >
            {t('plugin_management.runtime_reconcile')}
          </Button>
        ) : null}
        <ToggleSwitch
          checked={state.enabled}
          disabled={!writable || busy || reconciling}
          onChange={(next) => void apply(next)}
          ariaLabel={t('plugin_management.runtime_toggle_aria')}
          label={t('plugin_management.runtime_toggle')}
          labelPosition="left"
        />
      </div>
    </section>
  );
}
