import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { Button } from '@/components/ui/Button';
import {
  IconCheck,
  IconChevronRight,
  IconExternalLink,
  IconInfo,
  IconRefreshCw,
  IconSatellite,
  IconSettings,
  IconTimer,
} from '@/components/ui/icons';
import { useNotificationStore } from '@/stores';
import { versionApi } from '@/services/api';
import type { UsageServiceStatus } from '@/services/api/usageService';
import type { ConnectionStatus } from '@/types';
import { compareVersions, type VersionComparison } from '@/utils/version';
import { readApiLatestVersion, readManagerStableVersion } from '@/features/system/versionChecks';
import { usePanelFeatureAvailability } from '@/hooks/usePanelFeatureAvailability';
import { useManagerUpdates } from '@/features/system/ManagerUpdates';
import { buildDashboardVersionReleaseURL } from '@/features/dashboard/versionReleaseLinks';
import type { DashboardDisplayMode } from '../dashboardUiState';
import {
  evaluateDashboardHealthSummary,
  evaluateDashboardVersionUpdates,
} from '../model/dashboardHealthEvaluator';
import styles from './VersionCard.module.scss';

interface VersionCardProps {
  appVersion: string;
  apiVersion: string;
  cpaBase: string;
  serverBuildDate?: string;
  connectionStatus: ConnectionStatus;
  refreshSignal?: number;
  usageEnabled: boolean;
  usageLoading: boolean;
  usageLoaded?: boolean;
  usageError?: string;
  collectorStatus: UsageServiceStatus | null;
  collectorLoading: boolean;
  collectorError?: string;
  errorLogCount: number;
  errorLogsLoading: boolean;
  errorLogsError?: string;
  errorLogsLoaded: boolean;
  healthMetaError?: string;
  currentScope?: string;
  evidenceScope?: string;
  usageEvidenceScope?: string;
  displayMode?: DashboardDisplayMode;
  detailsExpanded?: boolean;
  onToggleDetails?: () => void;
}

interface LatestVersions {
  latestApi: string;
}

type HealthTone = 'ok' | 'warn' | 'error' | 'muted';

interface HealthItem {
  label: string;
  value: string;
  tone: HealthTone;
  icon: ReactNode;
  to?: string;
}

type ExternalStableLoadResult =
  | {
      current: true;
      version: string | null;
    }
  | {
      current: false;
    };

interface VersionBadge {
  label: string;
  className: string;
  releaseUrl?: string;
}

const renderBadge = (
  comparison: VersionComparison,
  latest: string,
  releaseUrl: string,
  t: TFunction
): VersionBadge | null => {
  if (comparison === null) return null;
  if (comparison > 0) {
    const display = latest.trim().replace(/^[vV]+/, '');
    return {
      label: t('dashboard.version_update_available', { version: `v${display}` }),
      className: styles.badgeUpdate,
      releaseUrl: releaseUrl || undefined,
    };
  }
  if (comparison === 0) {
    return { label: t('dashboard.version_is_latest'), className: styles.badgeLatest };
  }
  return null;
};

const renderVersionValue = (value: string, releaseUrl: string): ReactNode => {
  if (!releaseUrl) {
    return (
      <span className={styles.value} title={value}>
        {value}
      </span>
    );
  }

  return (
    <a className={styles.versionLink} href={releaseUrl} target="_blank" rel="noopener noreferrer">
      <span className={styles.value} title={value}>
        {value}
      </span>
      <IconExternalLink size={12} />
    </a>
  );
};

const renderBadgeValue = (badge: VersionBadge | null): ReactNode => {
  if (!badge) return null;

  const className = `${styles.badge} ${badge.className}`;
  if (!badge.releaseUrl) {
    return <span className={className}>{badge.label}</span>;
  }

  return (
    <a className={className} href={badge.releaseUrl} target="_blank" rel="noopener noreferrer">
      {badge.label}
    </a>
  );
};

export function VersionCard({
  appVersion,
  apiVersion,
  cpaBase,
  serverBuildDate,
  connectionStatus,
  refreshSignal,
  usageEnabled,
  usageLoading,
  usageLoaded,
  usageError,
  collectorStatus,
  collectorLoading,
  collectorError,
  errorLogCount,
  errorLogsLoading,
  errorLogsError,
  errorLogsLoaded,
  healthMetaError,
  currentScope,
  evidenceScope,
  usageEvidenceScope,
  displayMode = 'advanced',
  detailsExpanded = false,
  onToggleDetails,
}: VersionCardProps) {
  const { t, i18n } = useTranslation();
  const showNotification = useNotificationStore((state) => state.showNotification);
  const updates = useManagerUpdates();
  const managerVersion = updates.status?.current_version || appVersion;
  const featureAvailability = usePanelFeatureAvailability();
  const externalManagerUpdateFallback =
    featureAvailability.panelHostConfirmed &&
    featureAvailability.panelHostMode === 'external_panel' &&
    !featureAvailability.managerServiceAvailable;

  const [externalStableVersion, setExternalStableVersion] =
    useState<string | null | undefined>(undefined);
  const [externalStableError, setExternalStableError] = useState(false);
  const [checkingManagerVersion, setCheckingManagerVersion] = useState(false);
  const externalRequestSequenceRef = useRef(0);

  const [latest, setLatest] = useState<LatestVersions>({ latestApi: '' });
  const [checkingApiVersion, setCheckingApiVersion] = useState(false);

  const loadExternalManagerStable =
    useCallback(async (): Promise<ExternalStableLoadResult> => {
      const requestId = ++externalRequestSequenceRef.current;

      try {
        const data = await versionApi.checkManagerUpdateIndex();
        const version = readManagerStableVersion(data);

        if (requestId !== externalRequestSequenceRef.current) {
          return { current: false };
        }

        setExternalStableVersion(version);
        setExternalStableError(false);

        return {
          current: true,
          version,
        };
      } catch (error) {
        if (requestId !== externalRequestSequenceRef.current) {
          return { current: false };
        }

        setExternalStableVersion(undefined);
        setExternalStableError(true);

        throw error;
      }
    }, []);

  useEffect(() => {
    if (!externalManagerUpdateFallback) {
      externalRequestSequenceRef.current += 1;
      setExternalStableVersion(undefined);
      setExternalStableError(false);
      return;
    }

    loadExternalManagerStable().catch(() => {
      // Silent background check; toast is reserved for manual user checks.
    });

    return () => {
      externalRequestSequenceRef.current += 1;
    };
  }, [externalManagerUpdateFallback, refreshSignal, loadExternalManagerStable]);

  const handleExternalManagerCheck = useCallback(async () => {
    setCheckingManagerVersion(true);
    try {
      const result = await loadExternalManagerStable();
      if (!result.current) {
        return;
      }

      const version = result.version;
      if (version === null) {
        showNotification(t('manager_updates.no_candidate'), 'info');
        return;
      }

      const comparison = compareVersions(version, appVersion);
      if (comparison === null) {
        showNotification(t('system_info.manager_version_current_missing'), 'warning');
        return;
      }

      if (comparison > 0) {
        showNotification(
          t('system_info.manager_version_update_available', { version }),
          'warning'
        );
      } else {
        showNotification(t('system_info.manager_version_is_latest'), 'success');
      }
    } catch (error: unknown) {
      const message =
        error instanceof Error ? error.message : typeof error === 'string' ? error : '';
      const suffix = message ? `: ${message}` : '';
      showNotification(`${t('system_info.manager_version_check_error')}${suffix}`, 'error');
    } finally {
      setCheckingManagerVersion(false);
    }
  }, [appVersion, loadExternalManagerStable, showNotification, t]);

  useEffect(() => {
    let cancelled = false;

    const tasks: Array<Promise<Partial<LatestVersions>>> = [];

    if (connectionStatus === 'connected') {
      tasks.push(
        versionApi
          .checkLatest()
          .then((data) => ({ latestApi: readApiLatestVersion(data) }))
          .catch(() => ({}))
      );
    }

    Promise.all(tasks).then((results) => {
      if (cancelled) return;
      const merged = results.reduce<LatestVersions>(
        (acc, partial) => ({
          latestApi: partial.latestApi ?? acc.latestApi,
        }),
        { latestApi: '' }
      );
      setLatest(merged);
    });

    return () => {
      cancelled = true;
    };
  }, [connectionStatus, refreshSignal]);

  const handleApiVersionCheck = useCallback(async () => {
    setCheckingApiVersion(true);
    try {
      const data = await versionApi.checkLatest();
      const latestApi = readApiLatestVersion(data);
      const comparison = compareVersions(latestApi, apiVersion);
      setLatest((prev) => ({ ...prev, latestApi }));

      if (!latestApi) {
        showNotification(t('system_info.version_check_error'), 'error');
        return;
      }

      if (comparison === null) {
        showNotification(t('system_info.version_current_missing'), 'warning');
        return;
      }

      if (comparison > 0) {
        showNotification(
          t('system_info.version_update_available', { version: latestApi }),
          'warning'
        );
      } else {
        showNotification(t('system_info.version_is_latest'), 'success');
      }
    } catch (error: unknown) {
      const message =
        error instanceof Error ? error.message : typeof error === 'string' ? error : '';
      const suffix = message ? `: ${message}` : '';
      showNotification(`${t('system_info.version_check_error')}${suffix}`, 'error');
    } finally {
      setCheckingApiVersion(false);
    }
  }, [apiVersion, showNotification, t]);

  const appReleaseUrl = useMemo(
    () => buildDashboardVersionReleaseURL('manager', managerVersion),
    [managerVersion]
  );
  const apiReleaseUrl = useMemo(
    () => buildDashboardVersionReleaseURL('core', apiVersion),
    [apiVersion]
  );
  const latestApiReleaseUrl = useMemo(
    () => buildDashboardVersionReleaseURL('core', latest.latestApi),
    [latest.latestApi]
  );

  const versionUpdateEval = useMemo(
    () =>
      evaluateDashboardVersionUpdates({
        appVersion,
        apiVersion,
        latestApiVersion: latest.latestApi,
        managerServiceAvailable: Boolean(featureAvailability.managerServiceAvailable),
        managerUpdatesAvailable: Boolean(updates.available),
        managerUpdatesError: Boolean(updates.error),
        managerUpdateStatus: updates.status,
        externalManagerUpdateFallback,
        externalStableVersion,
        externalStableError,
      }),
    [
      apiVersion,
      appVersion,
      externalManagerUpdateFallback,
      externalStableError,
      externalStableVersion,
      featureAvailability.managerServiceAvailable,
      latest.latestApi,
      updates.available,
      updates.error,
      updates.status,
    ]
  );

  const managerUpdateAvailable = versionUpdateEval.managerHostedUpdateAvailable;
  const apiBadge = useMemo(
    () =>
      renderBadge(
        versionUpdateEval.coreApiUpdateComparison,
        latest.latestApi,
        latestApiReleaseUrl,
        t
      ),
    [latest.latestApi, latestApiReleaseUrl, t, versionUpdateEval.coreApiUpdateComparison]
  );
  const externalReleaseUrl = useMemo(
    () =>
      externalStableVersion
        ? buildDashboardVersionReleaseURL('manager', externalStableVersion)
        : '',
    [externalStableVersion]
  );
  const externalBadge = useMemo(() => {
    if (!externalManagerUpdateFallback || !externalStableVersion || externalStableError) {
      return null;
    }

    return renderBadge(
      versionUpdateEval.externalManagerUpdateComparison,
      externalStableVersion,
      externalReleaseUrl,
      t
    );
  }, [
    externalManagerUpdateFallback,
    externalStableVersion,
    externalStableError,
    externalReleaseUrl,
    t,
    versionUpdateEval.externalManagerUpdateComparison,
  ]);

  const buildTimeDisplay = serverBuildDate
    ? new Date(serverBuildDate).toLocaleString(i18n.language)
    : t('dashboard.version_unknown');

  const resolvedCurrentScope = currentScope ?? '__standalone__';
  const resolvedEvidenceScope =
    evidenceScope ?? (currentScope === undefined ? '__standalone__' : '');
  const resolvedUsageEvidenceScope =
    usageEvidenceScope ?? (currentScope === undefined ? '__standalone__' : resolvedEvidenceScope);
  const resolvedUsageLoaded = usageLoaded ?? (!usageLoading && Boolean(usageEnabled));

  const healthSummaryEval = useMemo(
    () =>
      evaluateDashboardHealthSummary({
        panelHostMode: featureAvailability.panelHostMode ?? 'manager_embedded',
        managerServiceAvailable: Boolean(featureAvailability.managerServiceAvailable),
        featureChecking: Boolean(featureAvailability.checking),
        featureReason: featureAvailability.reason ?? '',
        usageEnabled,
        currentScope: resolvedCurrentScope,
        evidenceScope: resolvedEvidenceScope,
        usageEvidenceScope: resolvedUsageEvidenceScope,
        status: collectorStatus,
        loading: collectorLoading,
        error: collectorError,
        connectionStatus,
        usageLoaded: resolvedUsageLoaded,
        usageLoading,
        usageError,
        errorLogsLoaded,
        errorLogsLoading,
        errorLogCount,
        errorLogsError,
        healthMetaError,
      }),
    [
      collectorError,
      collectorLoading,
      collectorStatus,
      connectionStatus,
      errorLogCount,
      errorLogsError,
      errorLogsLoaded,
      errorLogsLoading,
      featureAvailability.checking,
      featureAvailability.managerServiceAvailable,
      featureAvailability.panelHostMode,
      featureAvailability.reason,
      healthMetaError,
      resolvedCurrentScope,
      resolvedEvidenceScope,
      resolvedUsageEvidenceScope,
      resolvedUsageLoaded,
      usageEnabled,
      usageError,
      usageLoading,
    ]
  );

  const scopeMatches =
    resolvedCurrentScope !== '' && resolvedCurrentScope === resolvedEvidenceScope;
  const usageScopeMatches =
    resolvedCurrentScope !== '' && resolvedCurrentScope === resolvedUsageEvidenceScope;
  const effectiveUsageLoaded = usageScopeMatches && resolvedUsageLoaded;
  const effectiveUsageError = usageScopeMatches ? usageError : '';
  const effectiveCollectorStatus = scopeMatches ? collectorStatus : null;
  const effectiveErrorLogsLoaded = scopeMatches && errorLogsLoaded;
  const effectiveErrorLogsError = scopeMatches ? errorLogsError : '';
  const effectiveErrorLogCount = scopeMatches ? errorLogCount : 0;

  const collector = effectiveCollectorStatus?.collector;
  const collectorEval = healthSummaryEval.collector;

  const usageState: HealthItem = usageEnabled
    ? effectiveUsageError
      ? {
          label: t('dashboard.health_usage_monitor'),
          value: t('dashboard.health_status_problem'),
          tone: 'error',
          icon: <IconInfo size={16} />,
        }
      : !effectiveUsageLoaded
        ? {
            label: t('dashboard.health_usage_monitor'),
            value: usageLoading ? '...' : t('dashboard.collector_state_unconfirmed_short'),
            tone: 'muted',
            icon: <IconInfo size={16} />,
          }
        : {
            label: t('dashboard.health_usage_monitor'),
            value: t('dashboard.health_status_normal'),
            tone: 'ok',
            icon: <IconCheck size={16} />,
          }
    : collectorEval.state === 'attention_required' &&
        collectorEval.reasons.includes('service_unavailable')
      ? {
          label: t('dashboard.health_usage_monitor'),
          value: t('dashboard.collector_unavailable'),
          tone: 'error',
          icon: <IconInfo size={16} />,
        }
      : {
          label: t('dashboard.health_usage_monitor'),
          value: t('dashboard.health_status_disabled'),
          tone: 'muted',
          icon: <IconInfo size={16} />,
        };

  let collectorState: HealthItem;
  if (collectorEval.state === 'attention_required') {
    if (
      collectorEval.reasons.includes('service_unavailable') ||
      collectorEval.reasons.includes('status_fetch_error')
    ) {
      collectorState = {
        label: t('dashboard.collector_status_title'),
        value: t('dashboard.collector_unavailable'),
        tone: 'error',
        icon: <IconInfo size={16} />,
      };
    } else if (collectorEval.reasons.includes('collector_phase_error')) {
      collectorState = {
        label: t('dashboard.collector_status_title'),
        value: t('dashboard.health_status_problem'),
        tone: 'error',
        icon: <IconInfo size={16} />,
      };
    } else if (collectorEval.reasons.includes('collector_stopped')) {
      collectorState = {
        label: t('dashboard.collector_status_title'),
        value: t('dashboard.collector_state_stopped'),
        tone: 'warn',
        icon: <IconInfo size={16} />,
      };
    } else {
      collectorState = {
        label: t('dashboard.collector_status_title'),
        value: t('dashboard.health_status_warning'),
        tone: 'warn',
        icon: <IconInfo size={16} />,
      };
    }
  } else if (!usageEnabled) {
    collectorState = {
      label: t('dashboard.collector_status_title'),
      value: t('dashboard.health_status_disabled'),
      tone: 'muted',
      icon: <IconInfo size={16} />,
    };
  } else if (collectorEval.state === 'confirmed_healthy') {
    collectorState = {
      label: t('dashboard.collector_status_title'),
      value: t('dashboard.health_status_normal'),
      tone: 'ok',
      icon: <IconCheck size={16} />,
    };
  } else if (collectorEval.unconfirmedReason === 'collector_starting') {
    collectorState = {
      label: t('dashboard.collector_status_title'),
      value: t('dashboard.collector_state_starting'),
      tone: 'muted',
      icon: <IconInfo size={16} />,
    };
  } else {
    collectorState = {
      label: t('dashboard.collector_status_title'),
      value:
        collectorLoading && !effectiveCollectorStatus
          ? '...'
          : t('dashboard.collector_state_unconfirmed_short'),
      tone: 'muted',
      icon: <IconInfo size={16} />,
    };
  }

  let queueState: HealthItem;
  if (collectorEval.state === 'attention_required') {
    if (
      collectorEval.reasons.includes('service_unavailable') ||
      collectorEval.reasons.includes('status_fetch_error')
    ) {
      queueState = {
        label: t('dashboard.health_queue_status'),
        value: t('dashboard.collector_unavailable'),
        tone: 'error',
        icon: <IconInfo size={16} />,
      };
    } else {
      const tone: HealthTone = collectorEval.reasons.includes('collector_phase_error')
        ? 'error'
        : 'warn';
      const fallbackText = collectorEval.reasons.includes('collector_phase_error')
        ? t('dashboard.health_status_problem')
        : collectorEval.reasons.includes('collector_stopped')
          ? t('dashboard.collector_state_stopped')
          : t('dashboard.health_status_warning');
      queueState = {
        label: t('dashboard.health_queue_status'),
        value: collector?.queue ? `${collector.queue} · ${fallbackText}` : fallbackText,
        tone,
        icon: <IconInfo size={16} />,
      };
    }
  } else if (!usageEnabled) {
    queueState = {
      label: t('dashboard.health_queue_status'),
      value: t('dashboard.health_status_disabled'),
      tone: 'muted',
      icon: <IconInfo size={16} />,
    };
  } else if (collectorEval.state === 'confirmed_healthy') {
    queueState = {
      label: t('dashboard.health_queue_status'),
      value: collector?.queue || t('dashboard.health_status_normal'),
      tone: 'ok',
      icon: <IconCheck size={16} />,
    };
  } else {
    queueState = {
      label: t('dashboard.health_queue_status'),
      value:
        collector?.queue ||
        (collectorLoading && !effectiveCollectorStatus
          ? '...'
          : collectorEval.unconfirmedReason === 'collector_starting'
            ? t('dashboard.collector_state_starting')
            : t('dashboard.collector_state_unconfirmed_short')),
      tone: 'muted',
      icon: <IconInfo size={16} />,
    };
  }

  const effectiveErrorLogsPending = errorLogsLoading && !effectiveErrorLogsLoaded;
  const errorLogValue = effectiveErrorLogsPending
    ? '...'
    : effectiveErrorLogCount > 0
      ? t('dashboard.health_error_log_count', { count: effectiveErrorLogCount })
      : t('dashboard.health_status_normal');
  const errorLogState: HealthItem = {
    label: t('dashboard.health_error_logs'),
    value:
      effectiveErrorLogsError && !effectiveErrorLogsLoaded
        ? t('notification.load_failed')
        : effectiveErrorLogsError
          ? `${errorLogValue} · ${t('notification.refresh_failed')}`
          : errorLogValue,
    tone: effectiveErrorLogsError
      ? 'error'
      : effectiveErrorLogsPending
        ? 'muted'
        : effectiveErrorLogCount > 0
          ? 'warn'
          : 'ok',
    icon:
      effectiveErrorLogsError || effectiveErrorLogCount > 0 ? (
        <IconInfo size={16} />
      ) : effectiveErrorLogsPending ? (
        <IconInfo size={16} />
      ) : (
        <IconCheck size={16} />
      ),
    to: '/logs?tab=errors',
  };

  const healthItems = [usageState, collectorState, queueState, errorLogState];

  if (displayMode === 'simple' && !detailsExpanded) {
    const isActivelyChecking =
      connectionStatus === 'connecting' ||
      Boolean(featureAvailability.checking) ||
      (usageEnabled &&
        ((usageLoading && !effectiveUsageLoaded) ||
          (collectorLoading && !effectiveCollectorStatus) ||
          effectiveErrorLogsPending ||
          collectorEval.unconfirmedReason === 'capability_checking' ||
          collectorEval.unconfirmedReason === 'collector_starting'));

    const unavailableCopy =
      healthSummaryEval.unavailableVariant === 'panel_mode_not_applicable'
        ? t('dashboard.compact_health_panel_mode')
        : healthSummaryEval.unavailableVariant === 'monitoring_disabled'
          ? t('dashboard.compact_health_monitoring_disabled')
          : t('dashboard.compact_health_usage_not_configured');

    const attentionHealthItems = healthItems.filter(
      (item, index) =>
        (item.tone === 'warn' || item.tone === 'error') &&
        !(index === 2 && item.value === collectorState.value && item.tone === collectorState.tone)
    );

    return (
      <section className={styles.compactBar} data-dashboard-overview="compact">
        <div className={styles.compactStatusGroup}>
          {healthSummaryEval.status === 'confirmed_healthy' ? (
            <span className={`${styles.compactStatusPill} ${styles.compactStatusOk}`}>
              <IconCheck size={14} />
              <span>{t('dashboard.compact_health_healthy')}</span>
            </span>
          ) : healthSummaryEval.status === 'loading_unconfirmed' ? (
            <span className={`${styles.compactStatusPill} ${styles.compactStatusMuted}`}>
              <IconInfo size={14} />
              <span>
                {isActivelyChecking
                  ? t('dashboard.compact_health_checking')
                  : t('dashboard.compact_health_unconfirmed')}
              </span>
            </span>
          ) : healthSummaryEval.status === 'unavailable_not_applicable' ? (
            <span className={`${styles.compactStatusPill} ${styles.compactStatusMuted}`}>
              <IconInfo size={14} />
              <span>{unavailableCopy}</span>
            </span>
          ) : (
            <>
              <span className={`${styles.compactStatusPill} ${styles.compactStatusError}`}>
                <IconInfo size={14} />
                <span>{t('dashboard.compact_health_attention')}</span>
              </span>
              {healthSummaryEval.hasConnectionAttention ? (
                <span className={`${styles.compactAlertPill} ${styles.compactAlertError}`}>
                  {t('common.connection_status')}: {t('common.disconnected')}
                </span>
              ) : null}
              {attentionHealthItems.map((item) => {
                const pillToneClass =
                  item.tone === 'error' ? styles.compactAlertError : styles.compactAlertWarn;
                const pillText =
                  item === collectorState && collectorEval.deadLetters > 0
                    ? `${item.label}: ${item.value} (${t('dashboard.collector_dead_letters')}: ${collectorEval.deadLetters})`
                    : `${item.label}: ${item.value}`;

                return item.to ? (
                  <Link
                    key={item.label}
                    to={item.to}
                    className={`${styles.compactAlertPill} ${styles.compactAlertLink} ${pillToneClass}`}
                  >
                    {pillText}
                  </Link>
                ) : (
                  <span
                    key={item.label}
                    className={`${styles.compactAlertPill} ${pillToneClass}`}
                  >
                    {pillText}
                  </span>
                );
              })}
              {healthSummaryEval.hasHealthMetaAttention && healthMetaError ? (
                <span className={`${styles.compactAlertPill} ${styles.compactAlertWarn}`}>
                  {healthMetaError}
                </span>
              ) : null}
            </>
          )}
        </div>

        <div className={styles.compactMetaGroup}>
          <div className={styles.compactVersions}>
            <span className={styles.compactVersionItem}>
              <span className={styles.compactVersionLabel}>{t('title.abbr')}</span>
              {renderVersionValue(
                managerVersion || t('dashboard.version_unknown'),
                appReleaseUrl
              )}
              {managerUpdateAvailable ? (
                <Link
                  to="/system/updates"
                  className={`${styles.badge} ${styles.managerUpdateBadge}`}
                  title={t('manager_updates.view_version', {
                    version: updates.status?.target?.release.version,
                  })}
                  aria-label={t('manager_updates.view_version', {
                    version: updates.status?.target?.release.version,
                  })}
                >
                  {t('manager_updates.available_badge')}
                  <IconChevronRight size={12} aria-hidden="true" />
                </Link>
              ) : null}
              {versionUpdateEval.externalManagerUpdateAvailable
                ? renderBadgeValue(externalBadge)
                : null}
            </span>
            <span className={styles.compactDivider} aria-hidden="true">
              ·
            </span>
            <span className={styles.compactVersionItem}>
              <span className={styles.compactVersionLabel}>{t('dashboard.api_version')}</span>
              {renderVersionValue(apiVersion || t('dashboard.version_unknown'), apiReleaseUrl)}
              {versionUpdateEval.coreApiUpdateAvailable ? renderBadgeValue(apiBadge) : null}
            </span>
          </div>

          {onToggleDetails ? (
            <button
              type="button"
              className={styles.detailsToggleBtn}
              aria-expanded={detailsExpanded}
              onClick={onToggleDetails}
            >
              {t('dashboard.expand_system_details')}
            </button>
          ) : null}
        </div>
      </section>
    );
  }

  return (
    <div className={styles.container} data-dashboard-overview="expanded">
      <section className={styles.section}>
        <div className={styles.sectionHeader}>
          <h2 className={styles.heading}>{t('dashboard.system_overview')}</h2>
          {displayMode === 'simple' && onToggleDetails ? (
            <button
              type="button"
              className={styles.detailsToggleBtn}
              aria-expanded={detailsExpanded}
              onClick={onToggleDetails}
            >
              {t('dashboard.collapse_system_details')}
            </button>
          ) : null}
        </div>
        <div className={`${styles.grid} ${styles.systemGrid}`}>
          <div className={styles.item}>
            <div className={styles.icon}>
              <IconSettings size={18} />
            </div>
            <div className={styles.content}>
              <div className={styles.versionHeader}>
                <div
                  className={styles.label}
                  title={t(
                    updates.status?.current_version
                      ? 'manager_updates.server_version'
                      : 'dashboard.app_version'
                  )}
                >
                  {t('title.abbr')}
                </div>
                {externalManagerUpdateFallback && (
                  <Button
                    type="button"
                    variant="ghost"
                    size="xs"
                    iconOnly
                    className={styles.versionAction}
                    onClick={() => void handleExternalManagerCheck()}
                    loading={checkingManagerVersion}
                    title={t('system_info.version_check_button')}
                    aria-label={t('system_info.version_check_button')}
                  >
                    {!checkingManagerVersion && <IconRefreshCw size={14} />}
                  </Button>
                )}
                {managerUpdateAvailable && (
                  <Link
                    to="/system/updates"
                    className={`${styles.badge} ${styles.managerUpdateBadge}`}
                    title={t('manager_updates.view_version', {
                      version: updates.status?.target?.release.version,
                    })}
                    aria-label={t('manager_updates.view_version', {
                      version: updates.status?.target?.release.version,
                    })}
                  >
                    {t('manager_updates.available_badge')}
                    <IconChevronRight size={12} aria-hidden="true" />
                  </Link>
                )}
              </div>
              <div className={styles.valueWrap}>
                {renderVersionValue(
                  managerVersion || t('dashboard.version_unknown'),
                  appReleaseUrl
                )}
                {renderBadgeValue(externalBadge)}
              </div>
            </div>
          </div>

          <div className={styles.item}>
            <div className={styles.icon}>
              <IconSatellite size={18} />
            </div>
            <div className={styles.content}>
              <div className={styles.versionHeader}>
                <div className={styles.label}>{t('dashboard.api_version')}</div>
                <Button
                  type="button"
                  variant="ghost"
                  size="xs"
                  iconOnly
                  className={styles.versionAction}
                  onClick={() => void handleApiVersionCheck()}
                  loading={checkingApiVersion}
                  title={t('system_info.version_check_button')}
                  aria-label={t('system_info.version_check_button')}
                >
                  {!checkingApiVersion && <IconRefreshCw size={14} />}
                </Button>
              </div>
              <div className={styles.valueWrap}>
                {renderVersionValue(apiVersion || t('dashboard.version_unknown'), apiReleaseUrl)}
                {renderBadgeValue(apiBadge)}
              </div>
            </div>
          </div>

          <div className={styles.item}>
            <div className={styles.icon}>
              <IconTimer size={18} />
            </div>
            <div className={styles.content}>
              <div className={styles.label}>{t('dashboard.build_time')}</div>
              <div className={styles.value}>{buildTimeDisplay}</div>
            </div>
          </div>

          <div className={styles.item}>
            <div className={styles.icon}>
              <IconExternalLink size={18} />
            </div>
            <div className={styles.content}>
              <div className={styles.label}>{t('dashboard.cpa_base')}</div>
              <div className={styles.value}>{cpaBase || '-'}</div>
            </div>
          </div>
        </div>
      </section>

      <section className={styles.section}>
        <div className={styles.sectionHeader}>
          <h2 className={styles.heading}>{t('dashboard.health_status')}</h2>
        </div>
        <div className={`${styles.grid} ${styles.healthGrid}`}>
          {healthItems.map((item) => {
            const content = (
              <>
                <div className={`${styles.healthIcon} ${styles[item.tone]}`}>{item.icon}</div>
                <div className={styles.content}>
                  <div className={styles.label}>{item.label}</div>
                  <div className={`${styles.value} ${styles[`${item.tone}Text`]}`}>
                    {item.value}
                  </div>
                </div>
              </>
            );

            return item.to ? (
              <Link
                key={item.label}
                to={item.to}
                className={`${styles.healthItem} ${styles.healthLink}`}
              >
                {content}
              </Link>
            ) : (
              <div key={item.label} className={styles.healthItem}>
                {content}
              </div>
            );
          })}
        </div>
      </section>
    </div>
  );
}
