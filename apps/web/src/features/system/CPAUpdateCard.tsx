import { useState, type ChangeEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { Modal } from '@/components/ui/Modal';
import { SegmentedTabs } from '@/components/ui/SegmentedTabs';
import {
  IconArrowUpFromLine,
  IconCheck,
  IconChevronRight,
  IconInfo,
  IconRefreshCw,
  IconTriangleAlert,
} from '@/components/ui/icons';
import { CPA_UPDATE_MAX_UPLOAD_BYTES, isValidHttpsCDNPrefix } from './cpaUpdateApi';
import type { CPAUpdateIntent, CPAUpdateRecordSnapshot } from './cpaUpdateIntentStorage';
import type { CPAUpdateFlow } from './useCPAUpdateFlow';
import styles from './ManagerUpdatePage.module.scss';
type Confirmation = {
  kind: 'activate' | 'retry_activate' | 'forget' | 'adopt_baseline';
  scope: string;
  intent: CPAUpdateIntent | null;
  record: CPAUpdateRecordSnapshot | null;
};
export function CPAUpdateCard({ flow }: { flow: CPAUpdateFlow }) {
  const { t } = useTranslation();
  const [confirmation, setConfirmation] = useState<Confirmation | null>(null);
  const [sourceMode, setSourceMode] = useState<'github' | 'cdn' | 'upload'>('github');
  const [cdnPrefix, setCdnPrefix] = useState('');
  const [uploadFile, setUploadFile] = useState<File | null>(null);
  const [dismissedTerminalKey, setDismissedTerminalKey] = useState<string | null>(null);
  const { status, intent } = flow;
  const isUploadPrepare = intent?.phase === 'prepare' && intent?.source_mode === 'upload';
  const unknownPrepareSource = intent?.phase === 'prepare' && intent?.source_mode === undefined;
  const external = status?.mode === 'external';
  const state = !flow.available
    ? 'unavailable'
    : !flow.initialized
      ? 'loading'
      : flow.statusError
        ? 'status_unavailable'
        : external
          ? 'managed_externally'
          : status?.state || 'status_unavailable';
  const isActiveInPage =
    !flow.recoveredSession && ['submitting', 'accepted', 'running'].includes(flow.stage);
  const isRecoveryState =
    flow.recoveredSession ||
    ['recovering', 'outcome_unknown', 'not_found', 'context_changed'].includes(flow.stage) ||
    !!flow.storageError;
  const terminalCompleted = flow.stage === 'succeeded' || flow.stage === 'already_applied';
  const completionKey = terminalCompleted
    ? `${flow.scope}:${flow.stage}:${intent?.target_version ?? status?.current_version ?? ''}`
    : null;
  const showCompletedBanner =
    !external && terminalCompleted && completionKey !== dismissedTerminalKey;
  const showFlow = !!intent && !external && !terminalCompleted;
  const confirm = (kind: Confirmation['kind']) => {
    setConfirmation({ kind, scope: flow.scope, intent, record: flow.recoveryRecord });
  };
  const activeConfirmation = confirmation?.scope === flow.scope ? confirmation : null;
  const forgetting = activeConfirmation?.kind === 'forget';
  const adoptingBaseline = activeConfirmation?.kind === 'adopt_baseline';
  const confirmationAllowed =
    activeConfirmation?.kind === 'activate'
      ? flow.canActivate
      : activeConfirmation?.kind === 'retry_activate'
        ? flow.canRetry
        : activeConfirmation?.kind === 'adopt_baseline'
          ? !flow.busy && !!status?.baseline?.adopt_baseline_available
          : !flow.busy && !!activeConfirmation?.record;
  const confirmAction = () => {
    if (!activeConfirmation || !confirmationAllowed) return;
    setConfirmation(null);
    if (activeConfirmation.kind === 'adopt_baseline') {
      void flow.adoptBaseline?.();
      return;
    }
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
  const phaseToneClass = (stageValue: string) => {
    if (stageValue === 'prepared') return styles['is-ready'];
    if (stageValue === 'succeeded' || stageValue === 'already_applied') return styles['is-success'];
    if (stageValue === 'confirmed_failed') return styles['is-danger'];
    if (['outcome_unknown', 'not_found', 'context_changed'].includes(stageValue))
      return styles['is-warning'];
    if (['recovering', 'submitting', 'accepted', 'running'].includes(stageValue))
      return styles['is-active'];
    return '';
  };
  const recoveryGuide = (() => {
    if (external) return null;
    if (flow.statusError && !flow.unresolved) {
      return {
        tone: 'is-warning' as const,
        title: t('cpa_updates.recovery_title_attention'),
        text: t('cpa_updates.recovery_guide_status_error'),
      };
    }
    if (!intent || !flow.unresolved) return null;
    switch (flow.stage) {
      case 'recovering':
      case 'submitting':
      case 'accepted':
      case 'running':
        if (intent.phase === 'prepare' && !flow.recoveredSession) return null;
        return {
          tone: 'is-info' as const,
          title: t('cpa_updates.recovery_title_running'),
          text: t(
            intent.phase === 'activate'
              ? 'cpa_updates.recovery_guide_running_activate'
              : isUploadPrepare
                ? 'cpa_updates.recovery_guide_running_upload'
                : 'cpa_updates.recovery_guide_running_prepare'
          ),
        };
      case 'prepared':
        return {
          tone: 'is-info' as const,
          title: t('cpa_updates.prepare'),
          text: t('cpa_updates.recovery_guide_prepared'),
        };
      case 'outcome_unknown':
        return {
          tone: 'is-warning' as const,
          title: t('cpa_updates.recovery_title_outcome_unknown'),
          text: t('cpa_updates.recovery_guide_outcome_unknown'),
        };
      case 'not_found':
        return {
          tone: 'is-warning' as const,
          title: t('cpa_updates.recovery_title_not_found'),
          text: t(
            isUploadPrepare
              ? 'cpa_updates.recovery_guide_not_found_upload'
              : unknownPrepareSource
                ? 'cpa_updates.recovery_guide_not_found_unknown_source'
                : 'cpa_updates.recovery_guide_not_found'
          ),
        };
      case 'context_changed':
        return {
          tone: 'is-danger' as const,
          title: t('cpa_updates.recovery_title_context_changed'),
          text: t('cpa_updates.recovery_guide_context_changed'),
        };
      default:
        return null;
    }
  })();
  const prioritizeRequery =
    flow.stage === 'outcome_unknown' ||
    flow.stage === 'context_changed' ||
    (flow.statusError && !flow.canRetry && !flow.canActivate);
  const trimmedPrefix = cdnPrefix.trim();
  const cdnPrefixValid = isValidHttpsCDNPrefix(trimmedPrefix);
  const showCdnError = sourceMode === 'cdn' && trimmedPrefix.length > 0 && !cdnPrefixValid;
  const uploadErrorKey = !uploadFile
    ? null
    : !Number.isFinite(uploadFile.size) || uploadFile.size <= 0
      ? 'cpa_updates.upload_file_empty'
      : uploadFile.size > CPA_UPDATE_MAX_UPLOAD_BYTES
        ? 'cpa_updates.upload_file_too_large'
        : null;
  const prepareBlockedBySource =
    (sourceMode === 'cdn' && !cdnPrefixValid) ||
    (sourceMode === 'upload' && (!uploadFile || !!uploadErrorKey));
  const showPrepareProgress =
    showFlow &&
    intent?.phase === 'prepare' &&
    ['submitting', 'accepted', 'running'].includes(flow.stage);
  const progressReceived = flow.prepareProgress?.receivedBytes ?? 0;
  const progressTotal = flow.prepareProgress?.totalBytes ?? 0;
  const progressPercent =
    progressTotal > 0 ? Math.min(100, Math.round((progressReceived / progressTotal) * 100)) : null;
  const progressPhaseKey =
    flow.prepareProgress?.phase &&
    ['resolving', 'downloading', 'uploading', 'verifying', 'staging'].includes(
      flow.prepareProgress.phase
    )
      ? `cpa_updates.progress_${flow.prepareProgress.phase}`
      : 'cpa_updates.progress_downloading';

  const handleFileChange = (event: ChangeEvent<HTMLInputElement>) => {
    const picked = event.target.files?.[0] ?? null;
    setUploadFile(picked);
    try {
      event.target.value = '';
    } catch {
      // Ignore read-only synthetic event targets in tests.
    }
  };

  const handlePrepare = () => {
    if (sourceMode === 'upload') {
      if (uploadFile && !uploadErrorKey) {
        const fileToUpload = uploadFile;
        setUploadFile(null);
        void flow.prepareUpload(fileToUpload);
      }
      return;
    }
    if (sourceMode === 'cdn') {
      if (cdnPrefixValid) void flow.prepare({ sourceMode: 'cdn', cdnPrefix: trimmedPrefix });
      return;
    }
    void flow.prepare();
  };
  const pillTone = external
    ? 'is-neutral'
    : status?.last_error || status?.stale
      ? 'is-warning'
      : state === 'update_available'
        ? 'is-info'
        : 'is-neutral';
  const pillLabel = status ? t('cpa_updates.' + status.mode) : t('cpa_updates.title');

  return (
    <section className={styles.productSection} aria-labelledby="cpa-update-title">
      <Card className={styles.release}>
        <div className={styles.cardHead}>
          <div className={styles.cardTitleGroup}>
            <div className={styles.cardTitleRow}>
              <h2 id="cpa-update-title">{t('cpa_updates.title')}</h2>
              <span className={`status-pill ${pillTone}`}>
                <span className={`status-dot ${pillTone}`} aria-hidden="true" />
                {pillLabel}
              </span>
            </div>
            {!external && <p className={styles.subtitle}>{t('cpa_updates.description')}</p>}
          </div>
          {!external && (
            <Button
              type="button"
              variant="secondary"
              className={styles.checkButton}
              disabled={!flow.canCheck}
              onClick={() => void flow.check()}
            >
              <IconRefreshCw size={15} aria-hidden="true" />
              {t('cpa_updates.check')}
            </Button>
          )}
        </div>

        <div className={styles.statusBody} aria-live="polite" aria-atomic="true">
          <h3 className={styles.stateTitle}>{t('cpa_updates.' + state)}</h3>
          {external && <p className={styles.summary}>{t('cpa_updates.external_hint')}</p>}
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
        {!external && status?.baseline?.baseline_upgrade_available && (
          <p className={styles.flowHint} role="status">
            {t('cpa_updates.baseline_upgrade_hint', {
              version: status.baseline.pinned_version,
            })}
          </p>
        )}
        {!external && flow.baselineAdoptedVersion && (
          <div className={styles.completedBanner} role="status" aria-live="polite">
            <div className={styles.completedText}>
              <IconCheck size={16} aria-hidden="true" />
              <p>
                {t('cpa_updates.adopt_baseline_succeeded', {
                  version: flow.baselineAdoptedVersion,
                })}
              </p>
            </div>
          </div>
        )}
        {!external && flow.baselineErrorCode && (
          <p className={styles.warning} role="alert">
            {t('cpa_updates.adopt_baseline_failed', { code: flow.baselineErrorCode })}
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
              {(['prepare', 'activate'] as const).map((phase) => {
                const currentStage = phaseState(phase);
                const stepDetail =
                  phase === 'prepare' &&
                  showPrepareProgress &&
                  flow.prepareProgress?.phase
                    ? progressPercent !== null
                      ? `${t(progressPhaseKey)} (${progressPercent}%)`
                      : t(progressPhaseKey)
                    : t('cpa_updates.flow.' + currentStage);
                return (
                  <div key={phase} className={`${styles.phaseCard} ${phaseToneClass(currentStage)}`}>
                    <dt>{t('cpa_updates.' + phase)}</dt>
                    <dd>{stepDetail}</dd>
                  </div>
                );
              })}
            </dl>
            {showPrepareProgress && (
              <div className={styles.progressBox} role="status" aria-live="polite">
                <div className={styles.progressMeta}>
                  <span>{t(progressPhaseKey)}</span>
                  <span>
                    {progressTotal > 0
                      ? `${progressPercent}% · ${formatMB(progressReceived)} / ${formatMB(progressTotal)}`
                      : ''}
                  </span>
                </div>
                <div
                  className={styles.progressTrack}
                  role="progressbar"
                  aria-valuemin={0}
                  aria-valuemax={100}
                  aria-valuenow={progressPercent ?? undefined}
                >
                  <div
                    className={`${styles.progressFill} ${
                      progressPercent === null ? styles['is-indeterminate'] : ''
                    }`}
                    style={progressPercent !== null ? { width: `${progressPercent}%` } : undefined}
                  />
                </div>
              </div>
            )}
            {recoveryGuide && (
              <div
                className={`${styles.recoveryBanner} ${
                  recoveryGuide.tone === 'is-warning'
                    ? styles['is-warning']
                    : recoveryGuide.tone === 'is-danger'
                      ? styles['is-danger']
                      : ''
                }`}
                role="status"
              >
                {recoveryGuide.tone === 'is-info' ? (
                  <IconInfo size={17} aria-hidden="true" />
                ) : (
                  <IconTriangleAlert size={17} aria-hidden="true" />
                )}
                <div>
                  <p className={styles.recoveryBannerTitle}>{recoveryGuide.title}</p>
                  <p className={styles.recoveryBannerText}>{recoveryGuide.text}</p>
                </div>
              </div>
            )}
            {flow.stage === 'confirmed_failed' && (
              <p className={styles.flowHint}>
                {t('cpa_updates.failures.' + flow.failureCode, {
                  defaultValue: t('cpa_updates.failure_hint'),
                })}
              </p>
            )}
            {flow.unresolved && !recoveryGuide && (
              <p className={styles.flowHint}>
                {t(isUploadPrepare ? 'cpa_updates.upload_leave_hint' : 'cpa_updates.leave_hint')}
              </p>
            )}
          </div>
        )}
        {showCompletedBanner && (
          <div className={styles.completedBanner} role="status" aria-live="polite">
            <div className={styles.completedText}>
              <IconCheck size={16} aria-hidden="true" />
              <div>
                <p>
                  {t('cpa_updates.completed_summary', {
                    version: intent?.target_version ?? status?.current_version ?? '',
                  })}
                </p>
                <span className={styles.completedMeta}>
                  {t('cpa_updates.flow.' + flow.stage)}
                </span>
              </div>
            </div>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              onClick={() => setDismissedTerminalKey(completionKey)}
            >
              {t('cpa_updates.dismiss')}
            </Button>
          </div>
        )}
        {!showFlow && recoveryGuide && (
          <div
            className={`${styles.recoveryBanner} ${
              recoveryGuide.tone === 'is-warning'
                ? styles['is-warning']
                : recoveryGuide.tone === 'is-danger'
                  ? styles['is-danger']
                  : ''
            }`}
            role="status"
          >
            <IconTriangleAlert size={17} aria-hidden="true" />
            <div>
              <p className={styles.recoveryBannerTitle}>{recoveryGuide.title}</p>
              <p className={styles.recoveryBannerText}>{recoveryGuide.text}</p>
            </div>
          </div>
        )}
        {flow.stage === 'context_changed' && !showFlow && (
          <p className={styles.warning} role="status">
            {t('cpa_updates.flow.context_changed')}
          </p>
        )}
        {!external && status?.state === 'update_available' && !flow.unresolved && (
          <div className={styles.sourcePanel}>
            <div className={styles.sourceHeader}>
              <span className={styles.sourceTitle}>{t('cpa_updates.source_label')}</span>
              <SegmentedTabs
                idBase="cpa-prepare-source"
                items={[
                  { id: 'github', label: t('cpa_updates.source_github') },
                  { id: 'cdn', label: t('cpa_updates.source_cdn') },
                  { id: 'upload', label: t('cpa_updates.source_upload') },
                ]}
                activeTab={sourceMode}
                onChange={setSourceMode}
                ariaLabel={t('cpa_updates.source_label')}
              />
            </div>
            {sourceMode === 'cdn' && (
              <>
                <label className={styles.sourceTitle} htmlFor="cpa-cdn-prefix">
                  {t('cpa_updates.cdn_prefix_label')}
                </label>
                <input
                  id="cpa-cdn-prefix"
                  type="url"
                  className={styles.sourceInput}
                  value={cdnPrefix}
                  placeholder={t('cpa_updates.cdn_prefix_placeholder')}
                  onChange={(event) => setCdnPrefix(event.target.value)}
                />
                {showCdnError && (
                  <p className={styles.sourceError} role="alert">
                    {t('cpa_updates.cdn_prefix_invalid')}
                  </p>
                )}
                <p className={styles.sourceHint}>{t('cpa_updates.cdn_prefix_hint')}</p>
              </>
            )}
            {sourceMode === 'upload' && (
              <>
                <div className={styles.uploadRow}>
                  <input
                    id="cpa-upload-archive"
                    type="file"
                    accept=".tar.gz,application/gzip,application/x-gzip"
                    className={styles.uploadFileInput}
                    onChange={handleFileChange}
                  />
                  <label htmlFor="cpa-upload-archive" className={styles.uploadFileLabel}>
                    <IconArrowUpFromLine size={14} aria-hidden="true" />
                    {t('cpa_updates.upload_choose_file')}
                  </label>
                  {uploadFile && <span className={styles.uploadFileName}>{uploadFile.name}</span>}
                </div>
                {uploadErrorKey && (
                  <p className={styles.sourceError} role="alert">
                    {t(uploadErrorKey)}
                  </p>
                )}
                <p className={styles.sourceHint}>{t('cpa_updates.upload_file_hint')}</p>
              </>
            )}
          </div>
        )}
        {flow.unresolved && !external && !isActiveInPage && (
          <p className={styles.flowHint}>
            {t(
              flow.stage === 'prepared' && !isRecoveryState
                ? 'cpa_updates.prepared_check_hint'
                : 'cpa_updates.check_locked'
            )}
          </p>
        )}
        <div className={styles.actions}>
          {!external && status?.state === 'update_available' && !flow.unresolved && (
            <Button
              type="button"
              disabled={!flow.canPrepare || prepareBlockedBySource}
              loading={flow.busy && flow.stage === 'submitting'}
              onClick={handlePrepare}
            >
              {t(
                sourceMode === 'upload'
                  ? 'cpa_updates.upload_action'
                  : flow.stage === 'confirmed_failed'
                    ? 'cpa_updates.new_attempt'
                    : 'cpa_updates.prepare_action'
              )}
            </Button>
          )}
          {!external && flow.stage === 'prepared' && (
            <Button
              type="button"
              disabled={!flow.canActivate}
              loading={flow.busy && intent?.phase === 'activate'}
              onClick={() => confirm('activate')}
            >
              {t('cpa_updates.activate_action')}
            </Button>
          )}
          {!external && flow.stage === 'not_found' && intent &&
            !isUploadPrepare && !unknownPrepareSource && (
            <Button
              type="button"
              disabled={!flow.canRetry}
              loading={flow.busy}
              onClick={() => {
                if (intent.phase === 'activate') confirm('retry_activate');
                else void flow.retry(intent);
              }}
            >
              {t('cpa_updates.retry_same')}
            </Button>
          )}
          {((isRecoveryState && (flow.unresolved || flow.storageError)) || flow.statusError) && (
            <Button
              type="button"
              variant={prioritizeRequery ? 'primary' : 'secondary'}
              disabled={!flow.available || !flow.initialized}
              loading={flow.busy}
              onClick={() => void flow.refresh()}
            >
              {t('cpa_updates.requery')}
            </Button>
          )}
          {!external && status?.baseline?.adopt_baseline_available && !flow.unresolved && (
            <Button
              type="button"
              variant="secondary"
              disabled={flow.busy || !flow.available}
              loading={!!flow.baselineBusy}
              onClick={() => confirm('adopt_baseline')}
            >
              {t('cpa_updates.adopt_baseline_action', {
                version: status.baseline.pinned_version,
              })}
            </Button>
          )}
        </div>
        {((flow.unresolved && !isActiveInPage) || !!flow.storageError) && (
          <details className={styles.recoveryDangerZone}>
            <summary>
              <IconChevronRight size={14} aria-hidden="true" />
              {t(
                flow.stage === 'prepared' && !isRecoveryState
                  ? 'cpa_updates.prepared_controls_toggle'
                  : 'cpa_updates.recovery_advanced_toggle'
              )}
            </summary>
            <div className={styles.recoveryDangerBody}>
              <p>
                {t(
                  flow.stage === 'prepared' && !isRecoveryState
                    ? 'cpa_updates.prepared_controls_hint'
                    : 'cpa_updates.recovery_advanced_hint'
                )}
              </p>
              {flow.stage === 'prepared' && !isRecoveryState && (
                <Button
                  type="button"
                  variant="secondary"
                  size="sm"
                  disabled={!flow.available || !flow.initialized}
                  loading={flow.busy}
                  onClick={() => void flow.refresh()}
                >
                  {t('cpa_updates.requery')}
                </Button>
              )}
              <Button
                type="button"
                variant="ghost"
                size="sm"
                disabled={flow.busy || !flow.recoveryRecord}
                onClick={() => confirm('forget')}
              >
                {t('cpa_updates.forget')}
              </Button>
            </div>
          </details>
        )}
      </Card>
      <Modal
        open={!!activeConfirmation}
        className={styles.cpaConfirmation}
        onClose={() => setConfirmation(null)}
        title={t(
          forgetting
            ? 'cpa_updates.forget_title'
            : adoptingBaseline
              ? 'cpa_updates.adopt_baseline_title'
              : 'cpa_updates.activate_title'
        )}
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
              {t(
                forgetting
                  ? 'cpa_updates.forget_confirm'
                  : adoptingBaseline
                    ? 'cpa_updates.adopt_baseline_confirm'
                    : 'cpa_updates.activate_confirm'
              )}
            </Button>
          </>
        }
      >
        <p className={styles.confirmationText}>
          {t(
            forgetting
              ? 'cpa_updates.forget_warning'
              : adoptingBaseline
                ? 'cpa_updates.adopt_baseline_warning'
                : 'cpa_updates.activate_warning',
            {
              version: adoptingBaseline
                ? (status?.baseline?.pinned_version ?? '')
                : activeConfirmation?.intent?.target_version,
            }
          )}
        </p>
      </Modal>
    </section>
  );
}

function formatMB(bytes: number): string {
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}
