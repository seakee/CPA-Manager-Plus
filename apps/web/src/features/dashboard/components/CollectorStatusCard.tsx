import { useTranslation } from 'react-i18next';
import type { UsageServiceStatus } from '@/services/api/usageService';
import { resolveCollectorErrorHintKey } from '@/utils/collectorErrorHint';
import styles from './CollectorStatusCard.module.scss';

interface CollectorStatusCardProps {
  enabled: boolean;
  serviceBase: string;
  managementKey?: string;
  refreshSignal?: number;
  status: UsageServiceStatus | null;
  loading: boolean;
  error: string;
}

const formatCount = (value: number | undefined) =>
  Number.isFinite(value) ? Number(value).toLocaleString() : '-';

const formatTimestamp = (value: number | undefined, locale: string) => {
  if (!value || !Number.isFinite(value)) return '-';
  return new Date(value).toLocaleTimeString(locale, {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  });
};

export function CollectorStatusCard({
  enabled,
  status,
  loading,
  error,
}: CollectorStatusCardProps) {
  const { t, i18n } = useTranslation();
  const collector = status?.collector;
  const lastError = collector?.lastError || error;
  const lastErrorHintKey = resolveCollectorErrorHintKey(lastError);
  const queueStatus = !enabled
    ? t('dashboard.health_status_disabled')
    : error
      ? t('dashboard.collector_unavailable')
      : lastError
        ? t('dashboard.health_status_warning')
        : status
          ? t('dashboard.health_status_normal')
          : '-';

  const rows = [
    { label: t('dashboard.collector_mode'), value: collector?.mode || collector?.collector || '-' },
    { label: t('dashboard.health_queue_status'), value: queueStatus, isStatus: true },
    { label: t('dashboard.collector_events'), value: formatCount(status?.events) },
    { label: t('dashboard.collector_dead_letters'), value: formatCount(status?.deadLetters ?? collector?.deadLetters) },
    { label: t('dashboard.collector_last_consumed'), value: formatTimestamp(collector?.lastConsumedAt, i18n.language) },
    { label: t('dashboard.collector_last_inserted'), value: formatTimestamp(collector?.lastInsertedAt, i18n.language) },
    { label: t('dashboard.collector_total_inserted'), value: formatCount(collector?.totalInserted) },
    { label: t('dashboard.collector_total_skipped'), value: formatCount(collector?.totalSkipped) },
  ];

  return (
    <section className={styles.dataCard}>
      <div className={styles.cardHeader}>
        <h3>{t('dashboard.collector_status_title')}</h3>
      </div>
      <div className={styles.statusList}>
        {rows.map((row, i) => (
          <div key={i} className={styles.statusItem}>
            <span className={styles.label}>{row.label}</span>
            <span
              className={`${styles.value} ${
                row.isStatus
                  ? `${styles.statusText} ${
                      !enabled || error || lastError ? styles.statusWarn : styles.statusOk
                    }`
                  : ''
              }`}
            >
              {loading && !status ? '...' : row.value}
              {row.isStatus && <span className={styles.statusDot} />}
            </span>
          </div>
        ))}
      </div>
      {lastError ? (
        <div className={styles.errorLine}>
          <span>{t('dashboard.collector_last_error')}</span>
          {lastErrorHintKey ? (
            <>
              <strong>{t(lastErrorHintKey)}</strong>
              <code className={styles.errorRaw}>{lastError}</code>
            </>
          ) : (
            <strong>{lastError}</strong>
          )}
        </div>
      ) : null}
    </section>
  );
}
