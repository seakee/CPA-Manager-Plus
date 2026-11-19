import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { IconDatabaseZap } from '@/components/ui/icons';
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

const normalizeBytes = (value: number | undefined) =>
  Number.isFinite(value) && Number(value) > 0 ? Number(value) : 0;

const formatSharePercent = (part: number, total: number): string => {
  if (total <= 0 || part <= 0) return '0%';
  const pct = (part / total) * 100;
  if (pct < 1) return '<1%';
  return `${Math.round(pct)}%`;
};

const formatWalUsagePercent = (walBytes: number | undefined, limitBytes: number | undefined) => {
  if (
    !Number.isFinite(walBytes) ||
    !Number.isFinite(limitBytes) ||
    Number(walBytes) < 0 ||
    Number(limitBytes) <= 0
  ) {
    return '';
  }
  const pct = (Number(walBytes) / Number(limitBytes)) * 100;
  if (pct === 0) return '0%';
  if (pct < 0.1) return '<0.1%';
  return `${pct >= 10 ? Math.round(pct) : pct.toFixed(1)}%`;
};

export function DatabaseStatusCard({ status, loading, error }: DatabaseStatusCardProps) {
  const { t, i18n } = useTranslation();
  const database = status?.database;
  const databaseMaintenance = status?.databaseMaintenance;
  const checkpoint = database?.checkpoint;
  const checkpointError = checkpoint?.error;
  const statusError = !database ? error : '';
  const databaseUnavailable = !loading && !database;
  const checkpointModeRaw = checkpoint?.mode?.trim().toLowerCase();
  const checkpointUnobserved = checkpointModeRaw === 'unobserved';
  const checkpointUnavailable = Boolean(database && (!checkpoint || !checkpointModeRaw));
  const checkpointBehind =
    !checkpointUnobserved &&
    Number.isFinite(checkpoint?.logFrames) &&
    Number.isFinite(checkpoint?.checkpointedFrames) &&
    Number(checkpoint?.checkpointedFrames) < Number(checkpoint?.logFrames);
  const checkpointPending = checkpointModeRaw === 'pending';
  const walOverLimit =
    Number.isFinite(database?.walBytes) &&
    Number.isFinite(database?.journalSizeLimitBytes) &&
    Number(database?.walBytes) > Number(database?.journalSizeLimitBytes);
  const checkpointBusyCount = checkpointUnobserved ? undefined : checkpoint?.busy;
  const checkpointBusyAlert = !checkpointUnobserved && Number(checkpoint?.busy) > 0;
  const checkpointWarning = Boolean(
    statusError ||
    databaseUnavailable ||
    checkpointUnavailable ||
    checkpointError ||
    checkpointBusyAlert ||
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
    switch (checkpointModeRaw) {
      case 'passive':
        return t('system_info.database_checkpoint_passive');
      case 'truncate':
        return t('system_info.database_checkpoint_truncate');
      case 'pending':
        return t('system_info.database_checkpoint_pending');
      case 'unobserved':
        return t('system_info.database_checkpoint_unobserved', {
          defaultValue: t('system_info.database_checkpoint_unavailable'),
        });
      case undefined:
      case '':
        return t('system_info.database_checkpoint_unavailable');
      default:
        return checkpoint?.mode || t('system_info.database_checkpoint_unavailable');
    }
  })();

  const checkpointProgress =
    !checkpointUnobserved &&
    Number.isFinite(checkpoint?.logFrames) &&
    Number.isFinite(checkpoint?.checkpointedFrames)
      ? t('system_info.database_checkpoint_frames', {
          checkpointed: formatCount(checkpoint?.checkpointedFrames, i18n.language),
          log: formatCount(checkpoint?.logFrames, i18n.language),
        })
      : '-';

  const checkpointTime = !checkpoint
    ? t('system_info.database_checkpoint_unavailable')
    : checkpointUnobserved
      ? '-'
      : checkpoint.executedAtMs && Number.isFinite(checkpoint.executedAtMs)
        ? t('system_info.database_checkpoint_time', {
            time: new Date(checkpoint.executedAtMs).toLocaleString(i18n.language),
            duration: formatCount(checkpoint.durationMs, i18n.language),
          })
        : t('system_info.database_checkpoint_pending');

  const dbBytes = normalizeBytes(database?.databaseBytes);
  const walBytes = normalizeBytes(database?.walBytes);
  const shmBytes = normalizeBytes(database?.shmBytes);
  const partsSum = dbBytes + walBytes + shmBytes;

  const walLimitLabel = formatBytes(database?.journalSizeLimitBytes);
  const walUsagePercent = formatWalUsagePercent(
    database?.walBytes,
    database?.journalSizeLimitBytes
  );
  const subtitleText =
    !initialLoading && walUsagePercent
      ? t('system_info.database_wal_usage', {
          defaultValue: 'WAL limit {{limit}} · {{percent}} used',
          limit: walLimitLabel,
          percent: walUsagePercent,
        })
      : `${t('system_info.database_journal_size_limit')} ${initialLoading ? '...' : walLimitLabel}`;

  const breakdownItems = [
    {
      key: 'db',
      label: t('system_info.database_file'),
      value: formatBytes(database?.databaseBytes),
      share: formatSharePercent(dbBytes, partsSum),
      bytes: dbBytes,
      segmentClass: styles.segmentDb,
      dotClass: styles.dotDb,
    },
    {
      key: 'wal',
      label: t('system_info.database_wal_file'),
      value: formatBytes(database?.walBytes),
      share: formatSharePercent(walBytes, partsSum),
      bytes: walBytes,
      segmentClass: walOverLimit ? styles.segmentDanger : styles.segmentWal,
      dotClass: walOverLimit ? styles.dotDanger : styles.dotWal,
    },
    {
      key: 'shm',
      label: t('system_info.database_shm_file'),
      value: formatBytes(database?.shmBytes),
      share: formatSharePercent(shmBytes, partsSum),
      bytes: shmBytes,
      segmentClass: styles.segmentShm,
      dotClass: styles.dotShm,
    },
  ];

  const statusTone = initialLoading
    ? styles.statusPending
    : checkpointWarning
      ? styles.statusWarn
      : checkpointUnobserved
        ? styles.statusPending
        : styles.statusOk;

  const syncTone =
    checkpointBehind || checkpointPending || checkpointUnavailable
      ? styles.valueWarn
      : checkpointUnobserved || initialLoading
        ? ''
        : styles.valueOk;

  return (
    <Card className={styles.card}>
      <div className={styles.header}>
        <div className={styles.heading}>
          <span className={styles.iconBox} aria-hidden="true">
            <IconDatabaseZap size={18} />
          </span>
          <div className={styles.titles}>
            <h2>{t('system_info.database_status_title')}</h2>
            <small className={walOverLimit ? styles.subtitleWarn : undefined}>{subtitleText}</small>
          </div>
        </div>
        <span className={`${styles.statusBadge} ${statusTone}`}>
          <span className={styles.statusDot} aria-hidden="true" />
          {initialLoading ? t('common.loading') : checkpointMode}
        </span>
      </div>

      <div className={styles.heroRow}>
        <div className={styles.heroTotal}>
          <span className={styles.label}>{t('system_info.database_total_size')}</span>
          <strong className={styles.heroValue}>
            {initialLoading ? '...' : formatBytes(database?.totalBytes)}
          </strong>
        </div>
        <div className={styles.syncStats}>
          <div className={styles.syncItem}>
            <span className={styles.label}>{t('system_info.database_checkpoint_progress')}</span>
            <strong className={`${styles.syncValue} ${syncTone}`}>
              {initialLoading ? '...' : checkpointProgress}
            </strong>
          </div>
          <div className={styles.syncItem}>
            <span className={styles.label}>{t('system_info.database_checkpoint_busy')}</span>
            <strong
              className={`${styles.syncValue} ${checkpointBusyAlert ? styles.valueWarn : ''}`}
            >
              {initialLoading ? '...' : formatCount(checkpointBusyCount, i18n.language)}
            </strong>
          </div>
        </div>
      </div>

      <div className={styles.barTrack} role="presentation" aria-hidden="true">
        {partsSum > 0 ? (
          breakdownItems.map((item) =>
            item.bytes > 0 ? (
              <span
                key={item.key}
                className={`${styles.barSegment} ${item.segmentClass}`}
                style={{ flexGrow: item.bytes }}
                title={`${item.label}: ${item.value} (${item.share})`}
              />
            ) : null
          )
        ) : (
          <span className={styles.barEmpty} />
        )}
      </div>

      <div className={styles.breakdownGrid}>
        {breakdownItems.map((item) => (
          <div key={item.key} className={styles.breakdownTile}>
            <div className={styles.breakdownHeader}>
              <span className={`${styles.legendDot} ${item.dotClass}`} aria-hidden="true" />
              <span className={styles.label}>{item.label}</span>
            </div>
            <div className={styles.breakdownValueRow}>
              <strong className={styles.breakdownValue} title={item.value}>
                {initialLoading ? '...' : item.value}
              </strong>
              {!initialLoading && partsSum > 0 ? (
                <span className={styles.shareText}>{item.share}</span>
              ) : null}
            </div>
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

      <footer className={styles.footer}>
        <div className={styles.footerItem}>
          <span className={styles.label}>{t('system_info.database_checkpoint_last_run')}</span>
          <strong className={styles.footerValue} title={checkpointTime}>
            {initialLoading ? '...' : checkpointTime}
          </strong>
        </div>
        <div className={styles.footerItem}>
          <span className={styles.label}>{t('system_info.database_journal_size_limit')}</span>
          <strong
            className={`${styles.footerValue} ${walOverLimit ? styles.valueWarn : ''}`}
            title={walLimitLabel}
          >
            {initialLoading ? '...' : walLimitLabel}
          </strong>
        </div>
      </footer>
    </Card>
  );
}
