import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { Button } from '@/components/ui/Button';
import { Modal } from '@/components/ui/Modal';
import {
  IconCheck,
  IconExternalLink,
  IconInfo,
  IconRefreshCw,
  IconSatellite,
  IconSettings,
  IconTimer,
  IconDownload,
} from '@/components/ui/icons';
import { useNotificationStore } from '@/stores';
import {
  managedUpdateApi,
  versionApi,
  type ManagedUpdateCapability,
  type ManagedUpdateCheck,
  type ManagedUpdateStatus,
} from '@/services/api';
import type { UsageServiceStatus } from '@/services/api/usageService';
import type { ConnectionStatus } from '@/types';
import { compareVersions, type VersionComparison } from '@/utils/version';
import { readApiLatestVersion, readManagerLatestTag } from '@/features/system/versionChecks';
import { buildDashboardVersionReleaseURL } from '@/features/dashboard/versionReleaseLinks';
import { shouldRestoreManagedUpdateStatus } from '../managedUpdateState';
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
  usageError?: string;
  collectorStatus: UsageServiceStatus | null;
  collectorLoading: boolean;
  collectorError?: string;
  errorLogCount: number;
  errorLogsLoading: boolean;
  managerEmbedded: boolean;
}

interface LatestVersions {
  latestApp: string;
  latestAppUrl: string;
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

const renderBadge = (
  comparison: VersionComparison,
  latest: string,
  t: TFunction
): { label: string; className: string } | null => {
  if (comparison === null) return null;
  if (comparison > 0) {
    const display = latest.trim().replace(/^[vV]+/, '');
    return {
      label: t('dashboard.version_update_available', { version: `v${display}` }),
      className: styles.badgeUpdate,
    };
  }
  if (comparison === 0) {
    return { label: t('dashboard.version_is_latest'), className: styles.badgeLatest };
  }
  return null;
};

const renderVersionValue = (value: string, releaseUrl: string): ReactNode => {
  if (!releaseUrl) {
    return <span className={styles.value}>{value}</span>;
  }

  return (
    <a className={styles.versionLink} href={releaseUrl} target="_blank" rel="noopener noreferrer">
      <span className={styles.value}>{value}</span>
      <IconExternalLink size={12} />
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
  usageError,
  collectorStatus,
  collectorLoading,
  collectorError,
  errorLogCount,
  errorLogsLoading,
  managerEmbedded,
}: VersionCardProps) {
  const { t, i18n } = useTranslation();
  const showNotification = useNotificationStore((state) => state.showNotification);
  const [latest, setLatest] = useState<LatestVersions>({
    latestApp: '',
    latestAppUrl: '',
    latestApi: '',
  });
  const [checkingAppVersion, setCheckingAppVersion] = useState(false);
  const [checkingApiVersion, setCheckingApiVersion] = useState(false);
  const [updateCapability, setUpdateCapability] = useState<ManagedUpdateCapability | null>(null);
  const [updateCheck, setUpdateCheck] = useState<ManagedUpdateCheck | null>(null);
  const [updateStatus, setUpdateStatus] = useState<ManagedUpdateStatus | null>(null);
  const [updateModalOpen, setUpdateModalOpen] = useState(false);
  const [updateBusy, setUpdateBusy] = useState(false);
  const applyInitiatedRef = useRef(false);

  useEffect(() => {
    let cancelled = false;

    const tasks: Array<Promise<Partial<LatestVersions>>> = [
      versionApi
        .checkManagerLatest()
        .then((data) => ({
          latestApp: readManagerLatestTag(data),
          latestAppUrl: typeof data.html_url === 'string' ? data.html_url : '',
        }))
        .catch(() => ({})),
    ];

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
      const merged = results.reduce<LatestVersions>((acc, partial) => ({ ...acc, ...partial }), {
        latestApp: '',
        latestAppUrl: '',
        latestApi: '',
      });
      setLatest(merged);
    });

    return () => {
      cancelled = true;
    };
  }, [connectionStatus, refreshSignal]);

  useEffect(() => {
    let cancelled = false;
    if (!managerEmbedded || connectionStatus !== 'connected') {
      setUpdateCapability(null);
      setUpdateCheck(null);
      setUpdateStatus(null);
      return () => {
        cancelled = true;
      };
    }

    managedUpdateApi
      .capability()
      .then(async (capability) => {
        if (cancelled) return;
        setUpdateCapability(capability);
        if (!capability.supported) return;
        const [check, status] = await Promise.all([
          managedUpdateApi.check().catch(() => null),
          managedUpdateApi.status().catch(() => null),
        ]);
        if (cancelled) return;
        setUpdateCheck(check);
        if (status?.found) {
          setUpdateStatus(
            shouldRestoreManagedUpdateStatus(check, status.status) ? status.status : null
          );
        }
      })
      .catch(() => {
        if (!cancelled) setUpdateCapability(null);
      });

    return () => {
      cancelled = true;
    };
  }, [connectionStatus, managerEmbedded, refreshSignal]);

  useEffect(() => {
    if (
      !updateModalOpen ||
      !updateStatus ||
      ['succeeded', 'rolled_back', 'failed', 'manual_recovery_required'].includes(
        updateStatus.state
      )
    ) {
      return;
    }
    let cancelled = false;
    const poll = async () => {
      try {
        const response = await managedUpdateApi.status();
        if (!cancelled && response.found) setUpdateStatus(response.status);
      } catch {
        // The service is expected to disappear briefly while the detached updater switches versions.
      }
    };
    const timer = window.setInterval(() => void poll(), 1500);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [updateModalOpen, updateStatus]);

  useEffect(() => {
    if (updateStatus?.state !== 'succeeded' || !applyInitiatedRef.current) return;
    applyInitiatedRef.current = false;
    showNotification(t('dashboard.managed_update_succeeded_reload'), 'success', 4000);
    const timer = window.setTimeout(() => window.location.reload(), 800);
    return () => window.clearTimeout(timer);
  }, [showNotification, t, updateStatus?.state]);

  const handlePrepareUpdate = useCallback(async () => {
    setUpdateBusy(true);
    try {
      const status = await managedUpdateApi.plan();
      setUpdateStatus(status);
      showNotification(t('dashboard.managed_update_prepared'), 'success');
    } catch (error) {
      const message = error instanceof Error ? error.message : t('dashboard.managed_update_failed');
      showNotification(message, 'error');
    } finally {
      setUpdateBusy(false);
    }
  }, [showNotification, t]);

  const handleApplyUpdate = useCallback(async () => {
    setUpdateBusy(true);
    try {
      applyInitiatedRef.current = true;
      const status = await managedUpdateApi.apply();
      setUpdateStatus(status);
      showNotification(t('dashboard.managed_update_restarting'), 'info', 8000);
    } catch (error) {
      applyInitiatedRef.current = false;
      const message = error instanceof Error ? error.message : t('dashboard.managed_update_failed');
      showNotification(message, 'error');
    } finally {
      setUpdateBusy(false);
    }
  }, [showNotification, t]);

  const handleAppVersionCheck = useCallback(async () => {
    setCheckingAppVersion(true);
    try {
      const data = await versionApi.checkManagerLatest();
      const latestApp = readManagerLatestTag(data);
      const comparison = compareVersions(latestApp, appVersion);
      setLatest((prev) => ({
        ...prev,
        latestApp,
        latestAppUrl: typeof data.html_url === 'string' ? data.html_url : '',
      }));

      if (!latestApp) {
        showNotification(t('system_info.manager_version_check_error'), 'error');
        return;
      }

      if (comparison === null) {
        showNotification(t('system_info.manager_version_current_missing'), 'warning');
        return;
      }

      if (comparison > 0) {
        showNotification(
          t('system_info.manager_version_update_available', { version: latestApp }),
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
      setCheckingAppVersion(false);
    }
  }, [appVersion, showNotification, t]);

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

  const appBadge = useMemo(
    () => renderBadge(compareVersions(latest.latestApp, appVersion), latest.latestApp, t),
    [appVersion, latest.latestApp, t]
  );
  const apiBadge = useMemo(
    () => renderBadge(compareVersions(latest.latestApi, apiVersion), latest.latestApi, t),
    [apiVersion, latest.latestApi, t]
  );
  const appReleaseUrl = useMemo(
    () => buildDashboardVersionReleaseURL('manager', appVersion),
    [appVersion]
  );
  const apiReleaseUrl = useMemo(
    () => buildDashboardVersionReleaseURL('core', apiVersion),
    [apiVersion]
  );

  const buildTimeDisplay = serverBuildDate
    ? new Date(serverBuildDate).toLocaleString(i18n.language)
    : t('dashboard.version_unknown');

  const collector = collectorStatus?.collector;
  const collectorLastError = collector?.lastError?.trim() || '';
  const usageState: HealthItem = usageEnabled
    ? usageError
      ? {
          label: t('dashboard.health_usage_monitor'),
          value: t('dashboard.health_status_problem'),
          tone: 'error',
          icon: <IconInfo size={16} />,
        }
      : {
          label: t('dashboard.health_usage_monitor'),
          value: usageLoading ? '...' : t('dashboard.health_status_normal'),
          tone: usageLoading ? 'muted' : 'ok',
          icon: <IconCheck size={16} />,
        }
    : {
        label: t('dashboard.health_usage_monitor'),
        value: t('dashboard.health_status_disabled'),
        tone: 'muted',
        icon: <IconInfo size={16} />,
      };

  const collectorState: HealthItem = !usageEnabled
    ? {
        label: t('dashboard.collector_status_title'),
        value: t('dashboard.health_status_disabled'),
        tone: 'muted',
        icon: <IconInfo size={16} />,
      }
    : collectorError
      ? {
          label: t('dashboard.collector_status_title'),
          value: t('dashboard.collector_unavailable'),
          tone: 'error',
          icon: <IconInfo size={16} />,
        }
      : collectorLastError
        ? {
            label: t('dashboard.collector_status_title'),
            value: t('dashboard.health_status_warning'),
            tone: 'warn',
            icon: <IconInfo size={16} />,
          }
        : {
            label: t('dashboard.collector_status_title'),
            value:
              collectorLoading && !collectorStatus ? '...' : t('dashboard.health_status_normal'),
            tone: collectorLoading && !collectorStatus ? 'muted' : 'ok',
            icon: <IconCheck size={16} />,
          };

  const queueState: HealthItem = !usageEnabled
    ? {
        label: t('dashboard.health_queue_status'),
        value: t('dashboard.health_status_disabled'),
        tone: 'muted',
        icon: <IconInfo size={16} />,
      }
    : collectorError
      ? {
          label: t('dashboard.health_queue_status'),
          value: t('dashboard.collector_unavailable'),
          tone: 'error',
          icon: <IconInfo size={16} />,
        }
      : {
          label: t('dashboard.health_queue_status'),
          value:
            collector?.queue ||
            (collectorLoading && !collectorStatus ? '...' : t('dashboard.health_status_normal')),
          tone: collectorLoading && !collectorStatus ? 'muted' : 'ok',
          icon: <IconCheck size={16} />,
        };

  const errorLogState: HealthItem = {
    label: t('dashboard.health_error_logs'),
    value: errorLogsLoading
      ? '...'
      : errorLogCount > 0
        ? t('dashboard.health_error_log_count', { count: errorLogCount })
        : t('dashboard.health_status_normal'),
    tone: errorLogsLoading ? 'muted' : errorLogCount > 0 ? 'warn' : 'ok',
    icon: errorLogCount > 0 ? <IconInfo size={16} /> : <IconCheck size={16} />,
    to: '/logs?tab=errors',
  };

  const healthItems = [usageState, collectorState, queueState, errorLogState];
  const activeUpdateVisible = updateStatus
    ? !['succeeded', 'failed'].includes(updateStatus.state)
    : false;
  const managedUpdateVisible =
    updateCapability?.supported === true &&
    ((updateCheck?.updateAvailable === true && updateCheck.installable === true) ||
      activeUpdateVisible);
  const staged = updateStatus?.state === 'staged';
  const updateTerminal = updateStatus
    ? ['succeeded', 'rolled_back', 'failed', 'manual_recovery_required'].includes(
        updateStatus.state
      )
    : false;
  const canPrepareUpdate = !updateStatus || ['failed', 'rolled_back'].includes(updateStatus.state);
  const displayedAppBadge =
    managedUpdateVisible && updateCheck ? renderBadge(1, updateCheck.latestVersion, t) : appBadge;
  const manualReleaseVisible =
    !managedUpdateVisible &&
    compareVersions(latest.latestApp, appVersion) === 1 &&
    latest.latestAppUrl.startsWith('https://github.com/seakee/CPA-Manager-Plus/releases/');

  return (
    <div className={styles.container}>
      <section className={styles.section}>
        <h2 className={styles.heading}>{t('dashboard.system_overview')}</h2>
        <div className={`${styles.grid} ${styles.systemGrid}`}>
          <div className={styles.item}>
            <div className={styles.icon}>
              <IconSettings size={18} />
            </div>
            <div className={styles.content}>
              <div className={styles.versionHeader}>
                <div className={styles.label}>{t('dashboard.app_version')}</div>
                <Button
                  type="button"
                  variant="ghost"
                  size="xs"
                  iconOnly
                  className={styles.versionAction}
                  onClick={() => void handleAppVersionCheck()}
                  loading={checkingAppVersion}
                  title={t('system_info.version_check_button')}
                  aria-label={t('system_info.version_check_button')}
                >
                  {!checkingAppVersion && <IconRefreshCw size={14} />}
                </Button>
              </div>
              <div className={styles.valueWrap}>
                {renderVersionValue(
                  appVersion || t('dashboard.version_unknown'),
                  appReleaseUrl
                )}
                {displayedAppBadge && (
                  <span className={`${styles.badge} ${displayedAppBadge.className}`}>
                    {displayedAppBadge.label}
                  </span>
                )}
                {managedUpdateVisible && (
                  <Button
                    type="button"
                    variant="ghost"
                    size="xs"
                    className={styles.updateAction}
                    onClick={() => setUpdateModalOpen(true)}
                  >
                    <IconDownload size={13} />
                    {t('dashboard.managed_update_action')}
                  </Button>
                )}
                {manualReleaseVisible && (
                  <a
                    className={styles.releaseAction}
                    href={latest.latestAppUrl}
                    target="_blank"
                    rel="noreferrer"
                  >
                    <IconExternalLink size={13} />
                    {t('dashboard.managed_update_view_release')}
                  </a>
                )}
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
                {renderVersionValue(
                  apiVersion || t('dashboard.version_unknown'),
                  apiReleaseUrl
                )}
                {apiBadge && (
                  <span className={`${styles.badge} ${apiBadge.className}`}>{apiBadge.label}</span>
                )}
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
        <h2 className={styles.heading}>{t('dashboard.health_status')}</h2>
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

      <Modal
        open={updateModalOpen}
        onClose={() => setUpdateModalOpen(false)}
        title={t('dashboard.managed_update_title')}
        closeDisabled={updateBusy}
        width={500}
        footer={
          <div className={styles.updateFooter}>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => setUpdateModalOpen(false)}
              disabled={updateBusy}
            >
              {t(updateTerminal ? 'common.close' : 'common.cancel')}
            </Button>
            {!staged && canPrepareUpdate && (
              <Button size="sm" onClick={() => void handlePrepareUpdate()} loading={updateBusy}>
                {t(
                  updateStatus
                    ? 'dashboard.managed_update_retry'
                    : 'dashboard.managed_update_prepare'
                )}
              </Button>
            )}
            {staged && (
              <Button
                size="sm"
                onClick={() => void handleApplyUpdate()}
                loading={updateBusy}
                variant="primary"
              >
                {t('dashboard.managed_update_apply')}
              </Button>
            )}
          </div>
        }
      >
        <div className={styles.updateModalBody}>
          <div className={styles.updateVersionRow}>
            <span>{t('dashboard.managed_update_version')}</span>
            <strong>
              {appVersion || t('dashboard.version_unknown')}
              <span aria-hidden="true"> → </span>
              {updateCheck?.latestVersion || t('dashboard.version_unknown')}
            </strong>
          </div>
          <div className={styles.updateNotice}>
            <IconInfo size={17} />
            <div>
              <strong>{t('dashboard.managed_update_notice_title')}</strong>
              <span>{t('dashboard.managed_update_description')}</span>
            </div>
          </div>
          {updateStatus && (
            <div
              className={`${styles.updateStatus} ${styles[`updateStatus_${updateStatus.state}`] || ''}`}
            >
              <strong>{t(`dashboard.managed_update_state_${updateStatus.state}`)}</strong>
              {updateStatus.backupPath && (
                <span>{t('dashboard.managed_update_backup_created')}</span>
              )}
            </div>
          )}
          {staged && (
            <p className={styles.updateWarning}>{t('dashboard.managed_update_apply_warning')}</p>
          )}
        </div>
      </Modal>
    </div>
  );
}
