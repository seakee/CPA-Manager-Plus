import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  IconCheck,
  IconFileText,
  IconSatellite,
  IconShield,
  IconTimer,
} from '@/components/ui/icons';
import { useMonitoringAnalytics } from '@/features/monitoring/hooks/useMonitoringAnalytics';
import { logsApi, type ErrorLogFile } from '@/services/api/logs';
import type { DashboardRecentFailure } from '@/services/api/usageService';
import { formatDurationMs } from '@/utils/usage';
import styles from './HealthAlertsCard.module.scss';

interface HealthAlertsCardProps {
  enabled: boolean;
  loading: boolean;
  recentFailures: DashboardRecentFailure[];
  refreshSignal?: number;
}

const REFRESH_INTERVAL_MS = 60_000;

const getTodayWindow = () => {
  const start = new Date();
  start.setHours(0, 0, 0, 0);
  return {
    fromMs: start.getTime(),
    toMs: Date.now(),
  };
};

const shortHash = (value: string) => {
  const trimmed = value.trim();
  if (!trimmed) return '-';
  return trimmed.length <= 12 ? trimmed : `${trimmed.slice(0, 6)}...${trimmed.slice(-4)}`;
};

export function HealthAlertsCard({
  enabled,
  loading,
  recentFailures,
  refreshSignal,
}: HealthAlertsCardProps) {
  const { t, i18n } = useTranslation();
  const [errorLogs, setErrorLogs] = useState<ErrorLogFile[]>([]);
  const [analyticsWindow, setAnalyticsWindow] = useState(getTodayWindow);

  const channelAnalytics = useMonitoringAnalytics({
    fromMs: enabled ? analyticsWindow.fromMs : null,
    toMs: enabled ? analyticsWindow.toMs : null,
    include: { channel_share: true },
    throttleMs: REFRESH_INTERVAL_MS,
  });

  const refreshLogs = useCallback(async () => {
    if (!enabled) {
      setErrorLogs([]);
      return;
    }
    try {
      const response = await logsApi.fetchErrorLogs();
      setErrorLogs(Array.isArray(response.files) ? response.files.slice(0, 3) : []);
    } catch {
      setErrorLogs([]);
    }
  }, [enabled]);

  useEffect(() => {
    void refreshLogs();
  }, [refreshLogs]);

  useEffect(() => {
    if (!refreshSignal) return;
    setAnalyticsWindow(getTodayWindow());
    void refreshLogs();
  }, [refreshLogs, refreshSignal]);

  useEffect(() => {
    if (!enabled) return;
    const timer = window.setInterval(() => {
      setAnalyticsWindow(getTodayWindow());
      void refreshLogs();
    }, REFRESH_INTERVAL_MS);
    return () => window.clearInterval(timer);
  }, [enabled, refreshLogs]);

  const visibleFailures = useMemo(() => recentFailures.slice(0, 3), [recentFailures]);
  const abnormalChannels = useMemo(
    () =>
      (channelAnalytics.data?.channel_share ?? [])
        .map((row) => {
          const calls = Math.max(row.calls, 0);
          const failure = Math.max(row.failure, 0);
          return {
            authIndex: row.auth_index || '-',
            calls,
            failure,
            failureRate: calls > 0 ? failure / calls : 0,
          };
        })
        .filter((row) => row.failure > 0)
        .sort((a, b) => b.failure - a.failure || b.failureRate - a.failureRate || b.calls - a.calls)
        .slice(0, 3),
    [channelAnalytics.data?.channel_share]
  );
  const formatFailureRate = useMemo(
    () =>
      new Intl.NumberFormat(i18n.language, {
        style: 'percent',
        maximumFractionDigits: 1,
      }).format,
    [i18n.language]
  );
  const hasAlerts =
    visibleFailures.length > 0 || abnormalChannels.length > 0 || errorLogs.length > 0;
  const isLoading = loading || channelAnalytics.loading;

  return (
    <section className={styles.card}>
      <div className={styles.header}>
        <h2>{t('dashboard.health_alerts_title')}</h2>
        <span className={hasAlerts ? styles.badgeWarn : styles.badgeOk}>
          {hasAlerts ? (
            visibleFailures.length + abnormalChannels.length + errorLogs.length
          ) : (
            <IconCheck size={14} />
          )}
        </span>
      </div>

      {visibleFailures.length > 0 ? (
        <div className={styles.section}>
          <div className={styles.sectionTitle}>
            <IconShield size={16} />
            <span>{t('dashboard.health_recent_failures')}</span>
          </div>
          {visibleFailures.map((failure) => (
            <div key={`${failure.timestamp_ms}-${failure.source_hash}`} className={styles.alertRow}>
              <div>
                <strong title={failure.model}>{failure.model || '-'}</strong>
                <span>
                  {new Date(failure.timestamp_ms).toLocaleTimeString(i18n.language)} -{' '}
                  {shortHash(failure.source_hash || failure.api_key_hash)}
                </span>
              </div>
              <em>{formatDurationMs(failure.duration_ms, { locale: i18n.language })}</em>
            </div>
          ))}
        </div>
      ) : null}

      {abnormalChannels.length > 0 ? (
        <div className={styles.section}>
          <div className={styles.sectionTitle}>
            <IconSatellite size={16} />
            <span>{t('dashboard.health_abnormal_channels')}</span>
          </div>
          {abnormalChannels.map((channel) => {
            const channelName =
              channel.authIndex === '-'
                ? t('dashboard.health_unlinked_channel')
                : channel.authIndex;
            return (
              <div key={channel.authIndex} className={styles.alertRow}>
                <div>
                  <strong title={channelName}>{channelName}</strong>
                  <span>
                    {t('dashboard.health_channel_failure_summary', {
                      failures: channel.failure,
                      calls: channel.calls,
                      rate: formatFailureRate(channel.failureRate),
                    })}
                  </span>
                </div>
                <em>{formatFailureRate(channel.failureRate)}</em>
              </div>
            );
          })}
        </div>
      ) : null}

      {errorLogs.length > 0 ? (
        <div className={styles.section}>
          <div className={styles.sectionTitle}>
            <IconFileText size={16} />
            <span>{t('dashboard.health_error_logs')}</span>
          </div>
          {errorLogs.map((file) => (
            <div key={file.name} className={styles.alertRow}>
              <div>
                <strong title={file.name}>{file.name}</strong>
                <span>
                  {file.modified
                    ? new Date(file.modified).toLocaleString(i18n.language)
                    : shortHash(file.name)}
                </span>
              </div>
              <IconTimer size={14} />
            </div>
          ))}
        </div>
      ) : null}

      {!hasAlerts ? (
        <div className={styles.emptyState}>
          <IconCheck size={22} />
          <span>{isLoading ? '...' : t('dashboard.health_no_alerts')}</span>
        </div>
      ) : null}
    </section>
  );
}
