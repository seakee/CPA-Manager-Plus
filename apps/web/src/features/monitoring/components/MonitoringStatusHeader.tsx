import { Link } from 'react-router-dom';
import type { TFunction } from 'i18next';
import { LoadingSpinner } from '@/components/ui/LoadingSpinner';
import type { MonitoringStatusTone } from '@/features/monitoring/hooks/useMonitoringData';
import { formatCompactNumber } from '@/utils/usage';
import styles from '../MonitoringCenterPage.module.scss';

type MonitoringStatusHeaderProps = {
  showLoadingOverlay: boolean;
  monitoringUnavailable: boolean;
  monitoringUnavailableTitle: string;
  monitoringUnavailableBody: string;
  t: TFunction;
};

type MonitoringStatusSummaryProps = {
  connectionTone: MonitoringStatusTone;
  connectionLabel: string;
  lastRefreshedAt: Date | null;
  locale: string;
  scopedFailureCount: number;
  totalCalls: number;
  t: TFunction;
};

const shortLabel = (t: TFunction, shortKey: string, fallbackKey: string) => {
  const fallback = t(fallbackKey);
  const label = t(shortKey, { defaultValue: fallback });
  return label === shortKey ? fallback : label;
};

export function MonitoringStatusSummary({
  connectionTone,
  connectionLabel,
  lastRefreshedAt,
  locale,
  scopedFailureCount,
  totalCalls,
  t,
}: MonitoringStatusSummaryProps) {
  const lastSyncLabel = shortLabel(t, 'monitoring.last_sync_short', 'monitoring.last_sync');
  const recentFailuresLabel = shortLabel(
    t,
    'monitoring.recent_failures_short',
    'monitoring.recent_failures'
  );
  const totalCallsLabel = shortLabel(t, 'monitoring.total_calls_short', 'monitoring.total_calls');

  return (
    <div className={styles.statusBar}>
      <span className={`${styles.statusBadge} ${styles[`tone${connectionTone}`]}`}>
        <span className={styles.statusDot} aria-hidden="true" />
        {connectionLabel}
      </span>
      <div className={styles.statusMeta}>
        <span title={t('monitoring.last_sync')}>
          {lastSyncLabel}:{' '}
          {lastRefreshedAt ? lastRefreshedAt.toLocaleTimeString(locale) : '--'}
        </span>
        <span
          className={scopedFailureCount > 0 ? styles.statusMetaWarn : undefined}
          title={t('monitoring.recent_failures')}
        >
          {`${recentFailuresLabel}: ${scopedFailureCount}`}
        </span>
        <span title={t('monitoring.total_calls')}>
          {`${totalCallsLabel}: ${formatCompactNumber(totalCalls)}`}
        </span>
      </div>
    </div>
  );
}

export function MonitoringStatusHeader({
  showLoadingOverlay,
  monitoringUnavailable,
  monitoringUnavailableTitle,
  monitoringUnavailableBody,
  t,
}: MonitoringStatusHeaderProps) {
  return (
    <>
      {showLoadingOverlay ? (
        <div className={styles.loadingOverlay} aria-busy="true">
          <div className={styles.loadingOverlayContent}>
            <LoadingSpinner size={28} />
            <span>{t('common.loading')}</span>
          </div>
        </div>
      ) : null}

      {monitoringUnavailable ? (
        <div className={styles.callout}>
          <strong>{monitoringUnavailableTitle}</strong>
          <span>{monitoringUnavailableBody}</span>
          <Link
            to="/config"
            className={styles.configLink}
            onClick={() => localStorage.setItem('config-management:tab', 'manager')}
          >
            {t('monitoring.open_manager_config')}
          </Link>
        </div>
      ) : null}
    </>
  );
}
