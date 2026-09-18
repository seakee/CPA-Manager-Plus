import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { Modal } from '@/components/ui/Modal';
import { IconRefreshCw } from '@/components/ui/icons';
import type { CPAUpdateIntent, CPAUpdateRecordSnapshot } from './cpaUpdateIntentStorage';
import type { CPAUpdateFlow } from './useCPAUpdateFlow';
import styles from './ManagerUpdatePage.module.scss';

type Confirmation = {
  kind: 'activate' | 'retry_activate' | 'forget';
  scope: string;
  intent: CPAUpdateIntent | null;
  record: CPAUpdateRecordSnapshot | null;
};

export function CPAUpdateCard({ flow }: { flow: CPAUpdateFlow }) {
  const { t } = useTranslation();
  const [confirmation, setConfirmation] = useState<Confirmation | null>(null);
  const { status, intent } = flow;
  const external = status?.mode === 'external';
  const state = flow.demo
    ? 'demo'
    : !flow.available
      ? 'unavailable'
      : !flow.initialized
        ? 'loading'
        : flow.statusError
          ? 'status_unavailable'
          : external
            ? 'managed_externally'
            : status?.state || 'status_unavailable';
  const showFlow = !!intent && !external;
  const confirm = (kind: Confirmation['kind']) => {
    setConfirmation({ kind, scope: flow.scope, intent, record: flow.recoveryRecord });
  };
  const activeConfirmation = confirmation?.scope === flow.scope ? confirmation : null;
  const forgetting = activeConfirmation?.kind === 'forget';
  const confirmationAllowed =
    activeConfirmation?.kind === 'activate'
      ? flow.canActivate
      : activeConfirmation?.kind === 'retry_activate'
        ? flow.canRetry
        : !flow.busy && !!activeConfirmation?.record;
  const confirmAction = () => {
    if (!activeConfirmation || !confirmationAllowed) return;
    setConfirmation(null);
    if (activeConfirmation.kind === 'forget') {
      if (activeConfirmation.record) void flow.forget(activeConfirmation.record);
    } else if (activeConfirmation.intent) {
      if (activeConfirmation.kind === 'activate') void flow.activate(activeConfirmation.intent);
      else void flow.retry(activeConfirmation.intent);
    }
  };
  const phaseState = (phase: 'prepare' | 'activate') => {
    if (!intent) return 'pending';
    if (phase === intent.phase) return flow.stage;
    return phase === 'prepare' ? 'prepared' : 'pending';
  };

  return (
    <section className={styles.productSection} aria-labelledby="cpa-update-title">
      <div className={styles.heading}>
        <div>
          <h2 id="cpa-update-title">{t('cpa_updates.title')}</h2>
          {!external && <p className={styles.subtitle}>{t('cpa_updates.description')}</p>}
        </div>
        {!external && (
          <Button
            type="button"
            variant="secondary"
            disabled={!flow.canCheck}
            onClick={() => void flow.check()}
          >
            <IconRefreshCw size={15} aria-hidden="true" />
            {t('cpa_updates.check')}
          </Button>
        )}
      </div>
      <Card className={styles.release}>
        <div aria-live="polite" aria-atomic="true">
          <p className={styles.eyebrow}>
            {status ? t('cpa_updates.' + status.mode) : t('cpa_updates.title')}
          </p>
          <h3 className={styles.stateTitle}>{t('cpa_updates.' + state)}</h3>
        </div>
        <dl className={styles.runningVersions}>
          <div>
            <dt>{t('cpa_updates.current_version')}</dt>
            <dd>{status?.current_version || t('dashboard.version_unknown')}</dd>
          </div>
          {!external && (
            <div>
              <dt>{t('cpa_updates.latest_stable')}</dt>
              <dd>{status?.target_version || t('cpa_updates.not_checked')}</dd>
            </div>
          )}
        </dl>
        {!external &&
          status &&
          (status.last_error || (status.stale && status.state !== 'never_checked')) && (
            <p className={styles.warning} role="status">
              {t(status.last_error ? 'cpa_updates.check_failed' : 'cpa_updates.stale')}
            </p>
          )}
        {flow.storageError && (
          <p className={styles.warning} role="alert">
            {t('cpa_updates.storage_' + flow.storageError)}
          </p>
        )}
        {showFlow && (
          <div className={styles.cpaFlow}>
            <p className={styles.flowTarget}>
              {t('cpa_updates.flow_target', { version: intent.target_version })}
            </p>
            <dl className={styles.phaseStates} aria-live="polite" aria-atomic="true">
              {(['prepare', 'activate'] as const).map((phase) => (
                <div key={phase}>
                  <dt>{t('cpa_updates.' + phase)}</dt>
                  <dd>{t('cpa_updates.flow.' + phaseState(phase))}</dd>
                </div>
              ))}
            </dl>
            {flow.stage === 'confirmed_failed' && (
              <p className={styles.flowHint}>
                {t('cpa_updates.failures.' + flow.failureCode, {
                  defaultValue: t('cpa_updates.failure_hint'),
                })}
              </p>
            )}
            {flow.unresolved && <p className={styles.flowHint}>{t('cpa_updates.leave_hint')}</p>}
          </div>
        )}
        {flow.stage === 'context_changed' && !showFlow && (
          <p className={styles.warning} role="status">
            {t('cpa_updates.flow.context_changed')}
          </p>
        )}
        {flow.unresolved && !external && (
          <p className={styles.flowHint}>{t('cpa_updates.check_locked')}</p>
        )}
        <div className={styles.actions}>
          {!external && status?.state === 'update_available' && !flow.unresolved && (
            <Button type="button" disabled={!flow.canPrepare} onClick={() => void flow.prepare()}>
              {t(
                flow.stage === 'confirmed_failed'
                  ? 'cpa_updates.new_attempt'
                  : 'cpa_updates.prepare_action'
              )}
            </Button>
          )}
          {!external && flow.stage === 'prepared' && (
            <Button type="button" disabled={!flow.canActivate} onClick={() => confirm('activate')}>
              {t('cpa_updates.activate_action')}
            </Button>
          )}
          {!external && flow.stage === 'not_found' && intent && (
            <Button
              type="button"
              disabled={!flow.canRetry}
              onClick={() => {
                if (intent.phase === 'activate') confirm('retry_activate');
                else void flow.retry(intent);
              }}
            >
              {t('cpa_updates.retry_same')}
            </Button>
          )}
          {(flow.unresolved || flow.storageError || flow.statusError) && (
            <Button
              type="button"
              variant="secondary"
              disabled={!flow.available || !flow.initialized}
              loading={flow.busy}
              onClick={() => void flow.refresh()}
            >
              {t('cpa_updates.requery')}
            </Button>
          )}
          {(flow.unresolved || flow.storageError) && (
            <Button
              type="button"
              variant="ghost"
              disabled={flow.busy || !flow.recoveryRecord}
              onClick={() => confirm('forget')}
            >
              {t('cpa_updates.forget')}
            </Button>
          )}
        </div>
      </Card>
      <Modal
        open={!!activeConfirmation}
        className={styles.cpaConfirmation}
        onClose={() => setConfirmation(null)}
        title={t(forgetting ? 'cpa_updates.forget_title' : 'cpa_updates.activate_title')}
        footer={
          <>
            <Button type="button" variant="ghost" onClick={() => setConfirmation(null)}>
              {t('common.cancel')}
            </Button>
            <Button
              type="button"
              variant={forgetting ? 'danger' : 'primary'}
              disabled={!confirmationAllowed}
              onClick={confirmAction}
            >
              {t(forgetting ? 'cpa_updates.forget_confirm' : 'cpa_updates.activate_confirm')}
            </Button>
          </>
        }
      >
        <p className={styles.confirmationText}>
          {t(forgetting ? 'cpa_updates.forget_warning' : 'cpa_updates.activate_warning', {
            version: activeConfirmation?.intent?.target_version,
          })}
        </p>
      </Modal>
    </section>
  );
}
