import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Modal } from '@/components/ui/Modal';
import { formatFileSize } from '@/utils/format';
import type { UsageImportProgress } from '@/features/monitoring/services/usageImportSession';
import styles from '../MonitoringCenterPage.module.scss';

type UsageImportProgressModalProps = {
  open: boolean;
  progress: UsageImportProgress | null;
  busy?: boolean;
  onPause: () => void;
  onResume: () => void;
  onCancel: () => void;
  onClose: () => void;
};

const activePhases = new Set(['preparing', 'uploading', 'processing']);

export function UsageImportProgressModal({
  open,
  progress,
  busy = false,
  onPause,
  onResume,
  onCancel,
  onClose,
}: UsageImportProgressModalProps) {
  const { t } = useTranslation();
  if (!progress) return null;

  const active = activePhases.has(progress.phase);
  const canResume =
    progress.phase === 'paused' || (progress.phase === 'failed' && progress.retryable === true);
  const phaseLabel = t(`usage_stats.import_phase_${progress.phase}`);
  const pauseLabel =
    progress.phase === 'processing'
      ? t('usage_stats.import_pause_monitoring')
      : t('usage_stats.import_pause');

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={t('usage_stats.import_progress_title')}
      width={560}
      className={styles.monitorModal}
      closeDisabled={active || busy}
      footer={
        <div className={styles.usageImportModalFooter}>
          {!active ? (
            <Button variant="secondary" size="sm" onClick={onClose} disabled={busy}>
              {t('common.close')}
            </Button>
          ) : null}
          {active ? (
            <Button variant="secondary" size="sm" onClick={onPause} disabled={busy}>
              {pauseLabel}
            </Button>
          ) : null}
          {progress.sessionId &&
          progress.phase !== 'completed' &&
          progress.phase !== 'cancelled' ? (
            <Button variant="danger" size="sm" onClick={onCancel} disabled={busy}>
              {t('usage_stats.import_cancel')}
            </Button>
          ) : null}
          {canResume ? (
            <Button variant="primary" size="sm" onClick={onResume} disabled={busy}>
              {progress.phase === 'failed' ? t('common.retry') : t('usage_stats.import_resume')}
            </Button>
          ) : null}
        </div>
      }
    >
      <div className={styles.usageImportModalBody}>
        <div className={styles.usageImportFileName} title={progress.filename}>
          {progress.filename}
        </div>
        <div className={styles.usageImportStatusRow}>
          <span>{phaseLabel}</span>
          <strong>{progress.percent}%</strong>
        </div>
        <div
          className={styles.usageImportProgressTrack}
          role="progressbar"
          aria-label={t('usage_stats.import_progress_title')}
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={progress.percent}
        >
          <span style={{ width: `${progress.percent}%` }} />
        </div>
        <div className={styles.usageImportBytes}>
          {t('usage_stats.import_progress_bytes', {
            uploaded: formatFileSize(progress.uploadedBytes),
            total: formatFileSize(progress.totalBytes),
          })}
        </div>
        {progress.phase === 'processing' ? (
          <div className={styles.usageImportHint}>{t('usage_stats.import_processing_hint')}</div>
        ) : null}
        {progress.phase === 'paused' ? (
          <div className={styles.usageImportHint}>
            {t(
              progress.status === 'processing'
                ? 'usage_stats.import_processing_paused_hint'
                : 'usage_stats.import_paused_hint'
            )}
          </div>
        ) : null}
        {progress.result && (progress.phase === 'failed' || progress.phase === 'cancelled') ? (
          <div className={styles.usageImportHint}>
            {t('usage_stats.import_partial_result', {
              added: progress.result.added ?? 0,
              skipped: progress.result.skipped ?? 0,
              total: progress.result.total ?? 0,
              failed: progress.result.failed ?? 0,
            })}
          </div>
        ) : null}
        {progress.error ? (
          <div className={styles.usageImportError} role="alert">
            {progress.error}
          </div>
        ) : null}
      </div>
    </Modal>
  );
}
