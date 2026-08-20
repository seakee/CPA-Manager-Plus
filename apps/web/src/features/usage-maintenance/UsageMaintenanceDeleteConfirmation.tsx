import { useTranslation } from 'react-i18next';
import type { UsageArchiveRunSummary } from '@/services/api/usageService';
import { formatDateTime } from '@/utils/format';
import styles from './UsageMaintenanceDeleteConfirmation.module.scss';

type Props = {
  run: UsageArchiveRunSummary;
  deletionEnabled: boolean;
};

export function UsageMaintenanceDeleteConfirmation({ run, deletionEnabled }: Props) {
  const { t, i18n } = useTranslation();
  const remainingEventCount = Math.max(0, run.event_count - run.deleted_event_count);
  const verified =
    run.status === 'verified' ||
    run.status === 'deleting' ||
    run.status === 'completed' ||
    run.resume_status === 'deleting';

  return (
    <div className={styles.content}>
      <p className={styles.lead}>
        {t('usage_maintenance.delete_confirm_lead', {
          defaultValue:
            'The archive files and identity ledger remain, but deleting online raw data cannot be undone.',
        })}
      </p>
      <dl className={styles.details}>
        <div>
          <dt>{t('usage_maintenance.technical_run_id', { defaultValue: 'Task ID' })}</dt>
          <dd className={styles.mono}>{run.id}</dd>
        </div>
        <div>
          <dt>
            {t('usage_maintenance.delete_strict_cutoff', {
              defaultValue: 'Cutoff (strictly before)',
            })}
          </dt>
          <dd>{formatDateTime(new Date(run.cutoff_timestamp_ms), i18n.language)}</dd>
        </div>
        <div>
          <dt>
            {t('usage_maintenance.delete_remaining_count', {
              defaultValue: 'Remaining online raw events',
            })}
          </dt>
          <dd>{remainingEventCount.toLocaleString(i18n.language)}</dd>
        </div>
        <div>
          <dt>
            {t('usage_maintenance.delete_archived_count', {
              defaultValue: 'Total events protected by this archive',
            })}
          </dt>
          <dd>{run.event_count.toLocaleString(i18n.language)}</dd>
        </div>
        <div>
          <dt>
            {t('usage_maintenance.delete_verified_state', {
              defaultValue: 'Archive verification',
            })}
          </dt>
          <dd>
            <span className={`${styles.pill} ${verified ? styles.success : styles.warning}`}>
              {verified
                ? t('usage_maintenance.run_status_verified', { defaultValue: 'Verified' })
                : t('usage_maintenance.run_status_verifying', { defaultValue: 'Verifying' })}
            </span>
          </dd>
        </div>
        <div>
          <dt>
            {t('usage_maintenance.delete_capability_state', {
              defaultValue: 'Deletion capability',
            })}
          </dt>
          <dd>
            <span className={`${styles.pill} ${deletionEnabled ? styles.danger : styles.disabled}`}>
              {deletionEnabled
                ? t('common.enabled', { defaultValue: 'Enabled' })
                : t('common.disabled', { defaultValue: 'Disabled' })}
            </span>
          </dd>
        </div>
      </dl>
      <p className={styles.warningNote}>
        {t('usage_maintenance.delete_batch_recheck_note', {
          defaultValue:
            'The public API exposes summary readiness only. During deletion, the server rechecks exact coverage before every batch and can stop with a coverage-incomplete conflict.',
        })}
      </p>
      <p className={styles.infoNote}>
        {t('usage_maintenance.delete_raw_limitations_note', {
          defaultValue:
            'Core aggregates and long-term statistics remain available. Raw-dependent event details, failure diagnostics, latency distributions, and search may have coverage gaps; missing detail must not be interpreted as zero usage.',
        })}
      </p>
    </div>
  );
}
