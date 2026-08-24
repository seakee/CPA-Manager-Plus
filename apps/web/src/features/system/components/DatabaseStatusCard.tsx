import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import type { UsageServiceStatus } from '@/services/api/usageService';
import { formatFileSize } from '@/utils/format';
import styles from './DatabaseStatusCard.module.scss';

interface DatabaseStatusCardProps {
  status: UsageServiceStatus | null;
  loading: boolean;
  error: string;
}

const formatBytes = (value: number | undefined) =>
  Number.isFinite(value) && Number(value) >= 0 ? formatFileSize(Number(value)) : '-';

const formatCount = (value: number | undefined, locale: string) =>
  Number.isFinite(value) ? Number(value).toLocaleString(locale) : '-';

const normalizeMaintenanceCount = (value: number | undefined) =>
  Number.isFinite(value) && Number(value) >= 0 ? Number(value) : 0;

export function DatabaseStatusCard({ status, loading, error }: DatabaseStatusCardProps) {
  const { t, i18n } = useTranslation();
  const database = status?.database;
  const databaseMaintenance = status?.databaseMaintenance;
  const checkpoint = database?.checkpoint;
  const checkpointError = checkpoint?.error;
  const statusError = !database ? error : '';
  const databaseUnavailable = !loading && !database;
  const checkpointUnavailable = Boolean(database && !checkpoint);
  const checkpointBehind =
    Number.isFinite(checkpoint?.logFrames) &&
    Number.isFinite(checkpoint?.checkpointedFrames) &&
    Number(checkpoint?.checkpointedFrames) < Number(checkpoint?.logFrames);
  const checkpointPending = checkpoint?.mode?.trim().toLowerCase() === 'pending';
  const walOverLimit =
    Number.isFinite(database?.walBytes) &&
    Number.isFinite(database?.journalSizeLimitBytes) &&
    Number(database?.walBytes) > Number(database?.journalSizeLimitBytes);
  const checkpointWarning = Boolean(
    statusError ||
    databaseUnavailable ||
    checkpointUnavailable ||
    checkpointError ||
    Number(checkpoint?.busy) > 0 ||
    checkpointBehind ||
    checkpointPending ||
    walOverLimit
  );
  const initialLoading = loading && !database;
  const maintenanceDeferredIndexes = normalizeMaintenanceCount(
    databaseMaintenance?.deferredIndexes
  );
  const maintenanceOfflineJobs = normalizeMaintenanceCount(databaseMaintenance?.offlineJobs);
  const maintenanceDeferredIndexLabel = t('database_maintenance.query_index_count', {
    count: maintenanceDeferredIndexes,
    formattedCount: formatCount(maintenanceDeferredIndexes, i18n.language),
  });
  const maintenanceOfflineJobLabel = t('database_maintenance.offline_job_count', {
    count: maintenanceOfflineJobs,
    formattedCount: formatCount(maintenanceOfflineJobs, i18n.language),
  });

  const checkpointMode = (() => {
    switch (checkpoint?.mode?.trim().toLowerCase()) {
      case 'passive':
        return t('system_info.database_checkpoint_passive');
      case 'truncate':
        return t('system_info.database_checkpoint_truncate');
      case 'pending':
        return t('system_info.database_checkpoint_pending');
      case undefined:
      case '':
        return t('system_info.database_checkpoint_unavailable');
      default:
        return checkpoint?.mode || t('system_info.database_checkpoint_unavailable');
    }
  })();

  const checkpointProgress =
    Number.isFinite(checkpoint?.logFrames) && Number.isFinite(checkpoint?.checkpointedFrames)
      ? t('system_info.database_checkpoint_frames', {
          checkpointed: formatCount(checkpoint?.checkpointedFrames, i18n.language),
          log: formatCount(checkpoint?.logFrames, i18n.language),
        })
      : '-';

  const checkpointTime = !checkpoint
    ? t('system_info.database_checkpoint_unavailable')
    : checkpoint.executedAtMs && Number.isFinite(checkpoint.executedAtMs)
      ? t('system_info.database_checkpoint_time', {
          time: new Date(checkpoint.executedAtMs).toLocaleString(i18n.language),
          duration: formatCount(checkpoint.durationMs, i18n.language),
        })
      : t('system_info.database_checkpoint_pending');

  const summaryItems = [
    {
      label: t('system_info.database_total_size'),
      value: formatBytes(database?.totalBytes),
      emphasis: true,
    },
    { label: t('system_info.database_file'), value: formatBytes(database?.databaseBytes) },
    { label: t('system_info.database_wal_file'), value: formatBytes(database?.walBytes) },
    { label: t('system_info.database_shm_file'), value: formatBytes(database?.shmBytes) },
  ];

  const detailItems = [
    {
      label: t('system_info.database_journal_size_limit'),
      value: formatBytes(database?.journalSizeLimitBytes),
    },
    {
      label: t('system_info.database_checkpoint_mode'),
      value: checkpointMode,
      isStatus: true,
    },
    { label: t('system_info.database_checkpoint_progress'), value: checkpointProgress },
    {
      label: t('system_info.database_checkpoint_busy'),
      value: formatCount(checkpoint?.busy, i18n.language),
    },
    { label: t('system_info.database_checkpoint_last_run'), value: checkpointTime },
  ];

  const statusTone = initialLoading
    ? styles.statusPending
    : checkpointWarning
      ? styles.statusWarn
      : styles.statusOk;

  return (
    <Card
      title={t('system_info.database_status_title')}
      extra={
        <span className={`${styles.statusBadge} ${statusTone}`}>
          <span className={styles.statusDot} />
          {initialLoading ? t('common.loading') : checkpointMode}
        </span>
      }
      className={styles.card}
    >
      <div className={styles.summaryGrid}>
        {summaryItems.map((item) => (
          <div
            key={item.label}
            className={`${styles.summaryItem} ${item.emphasis ? styles.summaryEmphasis : ''}`}
          >
            <span className={styles.label}>{item.label}</span>
            <strong className={styles.summaryValue}>{initialLoading ? '...' : item.value}</strong>
          </div>
        ))}
      </div>

      <div className={styles.detailsGrid}>
        {detailItems.map((item) => (
          <div key={item.label} className={styles.detailItem}>
            <span className={styles.label}>{item.label}</span>
            <span className={`${styles.value} ${item.isStatus ? statusTone : ''}`}>
              {initialLoading ? '...' : item.value}
              {item.isStatus && database ? <span className={styles.statusDot} /> : null}
            </span>
          </div>
        ))}
      </div>

      {databaseMaintenance?.required ? (
        <div className={styles.maintenanceNotice} role="status" aria-live="polite">
          <strong>{t('system_info.database_maintenance_title')}</strong>
          <span>
            {t('system_info.database_maintenance_counts', {
              indexes: maintenanceDeferredIndexLabel,
              jobs: maintenanceOfflineJobLabel,
            })}
          </span>
          <span>{t('system_info.database_maintenance_hint')}</span>
        </div>
      ) : null}

      {checkpointError || statusError ? (
        <div className={styles.errorStack}>
          {checkpointError ? (
            <div className={styles.errorLine}>
              <span>{t('system_info.database_checkpoint_error')}</span>
              <strong>{checkpointError}</strong>
            </div>
          ) : null}
          {statusError ? (
            <div className={styles.errorLine}>
              <span>{t('system_info.database_status_error')}</span>
              <strong>{statusError}</strong>
            </div>
          ) : null}
        </div>
      ) : null}
    </Card>
  );
}
