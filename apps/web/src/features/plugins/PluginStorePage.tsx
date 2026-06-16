import {
  useCallback,
  useEffect,
  useMemo,
  useState,
  type KeyboardEvent,
  type ReactNode,
} from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router-dom';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { Input } from '@/components/ui/Input';
import { LoadingSpinner } from '@/components/ui/LoadingSpinner';
import {
  IconDownload,
  IconPlugin,
  IconRefreshCw,
  IconSearch,
  IconSettings,
  IconShield,
  IconTrash2,
} from '@/components/ui/icons';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { pluginsApi, pluginStoreApi } from '@/services/api';
import { useAuthStore, useConfigStore, useNotificationStore } from '@/stores';
import { getErrorMessage, isRecord } from '@/utils/helpers';
import type { PluginStoreEntry, PluginStoreResponse } from '@/types';
import {
  isDefaultPluginStoreSource,
  isOfficialPlugin,
  notifyPluginResourcesChanged,
  PLUGIN_RESOURCES_SETTLE_REFRESH_DELAY_MS,
  resolvePluginAssetURL,
} from './pluginResources';
import { PluginInstallGateModal } from './components/PluginInstallGateModal';
import styles from './PluginStorePage.module.scss';

type StoreStatusFilter = 'all' | 'installed' | 'notInstalled' | 'updates';

const STORE_STATUS_FILTER_ORDER: StoreStatusFilter[] = [
  'all',
  'installed',
  'notInstalled',
  'updates',
];

interface StoreLoadError {
  kind: 'unsupported' | 'registry' | 'generic';
  message: string;
}

type PluginStorePageProps = {
  active?: boolean;
  tabsControl?: ReactNode;
  onManageInstalled?: () => void;
};

const getErrorStatus = (error: unknown): number | undefined =>
  isRecord(error) && typeof error.status === 'number' ? error.status : undefined;

const getErrorDetailMessage = (error: unknown): string => {
  if (!isRecord(error) || !isRecord(error.details)) return '';
  const message = error.details.message;
  return typeof message === 'string' ? message.trim() : '';
};

const getErrorDetailCode = (error: unknown): string => {
  if (!isRecord(error) || !isRecord(error.details)) return '';
  const code = error.details.error;
  return typeof code === 'string' ? code.trim() : '';
};

const hasRestartRequired = (error: unknown) =>
  isRecord(error) && isRecord(error.data) && error.data.restart_required === true;

const getStoreEntryTitle = (entry: PluginStoreEntry) => entry.name || entry.id;
const getStoreEntryKey = (entry: PluginStoreEntry) =>
  entry.storeId || [entry.sourceId, entry.id].filter(Boolean).join('/') || entry.id;

function StoreLogo({ src }: { src: string }) {
  const [failed, setFailed] = useState(false);
  const showImage = Boolean(src) && !failed;

  return showImage ? (
    <img src={src} alt="" onError={() => setFailed(true)} />
  ) : (
    <IconPlugin size={18} />
  );
}

export function PluginStorePage({
  active = true,
  tabsControl,
  onManageInstalled,
}: PluginStorePageProps = {}) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const apiBase = useAuthStore((state) => state.apiBase);
  const supportsPlugin = useAuthStore((state) => state.supportsPlugin);
  const clearConfigCache = useConfigStore((state) => state.clearCache);
  const showNotification = useNotificationStore((state) => state.showNotification);
  const showConfirmation = useNotificationStore((state) => state.showConfirmation);

  const [data, setData] = useState<PluginStoreResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<StoreLoadError | null>(null);
  const [filter, setFilter] = useState('');
  const [statusFilter, setStatusFilter] = useState<StoreStatusFilter>('all');
  const [installingID, setInstallingID] = useState('');
  const [restartRequiredIDs, setRestartRequiredIDs] = useState<string[]>([]);
  const [gateEntry, setGateEntry] = useState<PluginStoreEntry | null>(null);
  const [gateMode, setGateMode] = useState<'install' | 'reinstall'>('install');
  const [gateIsUpdate, setGateIsUpdate] = useState(false);

  const connected = connectionStatus === 'connected';

  const handleManageInstalled = useCallback(() => {
    if (onManageInstalled) {
      onManageInstalled();
      return;
    }
    navigate('/plugins');
  }, [navigate, onManageInstalled]);

  const loadStore = useCallback(async () => {
    if (!connected) {
      setLoading(false);
      setError({ kind: 'generic', message: t('notification.connection_required') });
      return;
    }
    if (!supportsPlugin) {
      setLoading(false);
      setError({ kind: 'unsupported', message: t('plugin_store.unsupported_backend') });
      return;
    }

    setLoading(true);
    setError(null);
    try {
      const store = await pluginStoreApi.list();
      setData(store);
    } catch (err: unknown) {
      const status = getErrorStatus(err);
      if (status === 404) {
        setError({ kind: 'unsupported', message: t('plugin_store.unsupported_backend') });
      } else if (status === 502) {
        const detail = getErrorDetailMessage(err);
        setError({
          kind: 'registry',
          message: detail
            ? `${t('plugin_store.registry_failed')}: ${detail}`
            : t('plugin_store.registry_failed'),
        });
      } else {
        setError({
          kind: 'generic',
          message: getErrorMessage(err, t('plugin_store.load_failed')),
        });
      }
    } finally {
      setLoading(false);
    }
  }, [connected, supportsPlugin, t]);

  useHeaderRefresh(loadStore, active && connected && supportsPlugin);

  useEffect(() => {
    if (!active) return;
    void loadStore();
  }, [active, loadStore]);

  const stats = useMemo(() => {
    const plugins = data?.plugins ?? [];
    const installed = plugins.filter((plugin) => plugin.installed).length;
    return {
      total: plugins.length,
      installed,
      notInstalled: plugins.length - installed,
      updates: plugins.filter((plugin) => plugin.installed && plugin.updateAvailable).length,
    };
  }, [data?.plugins]);

  const visiblePlugins = useMemo(() => {
    const plugins = data?.plugins ?? [];
    const byStatus = plugins.filter((plugin) => {
      if (statusFilter === 'installed') return plugin.installed;
      if (statusFilter === 'notInstalled') return !plugin.installed;
      if (statusFilter === 'updates') return plugin.installed && plugin.updateAvailable;
      return true;
    });

    const query = filter.trim().toLowerCase();
    if (!query) return byStatus;

    return byStatus.filter((plugin) => {
      const haystack = [
        plugin.id,
        plugin.name,
        plugin.description,
        plugin.author,
        plugin.repository,
        plugin.license,
        plugin.sourceId,
        plugin.sourceName,
        plugin.sourceUrl,
        ...plugin.tags,
      ]
        .filter(Boolean)
        .join(' ')
        .toLowerCase();
      return haystack.includes(query);
    });
  }, [data?.plugins, filter, statusFilter]);

  const statusFilters: Array<{ key: StoreStatusFilter; label: string; count: number }> = [
    { key: 'all', label: t('plugin_store.filter_all'), count: stats.total },
    { key: 'installed', label: t('plugin_store.filter_installed'), count: stats.installed },
    {
      key: 'notInstalled',
      label: t('plugin_store.filter_not_installed'),
      count: stats.notInstalled,
    },
    { key: 'updates', label: t('plugin_store.filter_updates'), count: stats.updates },
  ];

  const restartNames = restartRequiredIDs.map((key) => {
    const entry = data?.plugins.find((plugin) => getStoreEntryKey(plugin) === key);
    return entry ? getStoreEntryTitle(entry) : key;
  });

  const hasActiveFilters = Boolean(filter.trim()) || statusFilter !== 'all';
  const initialLoading = loading && !data;

  const focusStatusFilter = useCallback((key: StoreStatusFilter) => {
    window.requestAnimationFrame(() => {
      document.getElementById(`plugin-store-filter-${key}`)?.focus();
    });
  }, []);

  const handleStatusFilterKeyDown = useCallback(
    (event: KeyboardEvent<HTMLButtonElement>, currentKey: StoreStatusFilter) => {
      const currentIndex = STORE_STATUS_FILTER_ORDER.indexOf(currentKey);
      if (currentIndex === -1) return;

      let nextIndex = currentIndex;
      if (event.key === 'ArrowRight' || event.key === 'ArrowDown') {
        nextIndex = (currentIndex + 1) % STORE_STATUS_FILTER_ORDER.length;
      } else if (event.key === 'ArrowLeft' || event.key === 'ArrowUp') {
        nextIndex =
          (currentIndex - 1 + STORE_STATUS_FILTER_ORDER.length) % STORE_STATUS_FILTER_ORDER.length;
      } else if (event.key === 'Home') {
        nextIndex = 0;
      } else if (event.key === 'End') {
        nextIndex = STORE_STATUS_FILTER_ORDER.length - 1;
      } else {
        return;
      }

      event.preventDefault();
      const nextKey = STORE_STATUS_FILTER_ORDER[nextIndex];
      setStatusFilter(nextKey);
      focusStatusFilter(nextKey);
    },
    [focusStatusFilter]
  );

  const runInstall = useCallback(
    async (entry: PluginStoreEntry, isUpdate: boolean) => {
      const failedKey = isUpdate ? 'plugin_store.update_failed' : 'plugin_store.install_failed';
      const entryKey = getStoreEntryKey(entry);
      const sourceId = entry.sourceId || undefined;

      setInstallingID(entryKey);
      try {
        const result = await pluginStoreApi.install(entry.id, { sourceId });
        showNotification(
          isUpdate ? t('plugin_store.update_success') : t('plugin_store.install_success'),
          'success'
        );
        if (result.restartRequired) {
          setRestartRequiredIDs((current) =>
            current.includes(entryKey) ? current : [...current, entryKey]
          );
          showNotification(t('plugin_store.restart_required_notice'), 'warning');
        }
        clearConfigCache();
        await loadStore();
        notifyPluginResourcesChanged();
        if (!result.restartRequired) {
          notifyPluginResourcesChanged({
            delayMs: PLUGIN_RESOURCES_SETTLE_REFRESH_DELAY_MS,
          });
        }
      } catch (err: unknown) {
        const sourceRequired = getErrorDetailCode(err) === 'plugin_store_source_required';
        showNotification(
          sourceRequired
            ? t('plugin_store.source_required')
            : `${t(failedKey)}: ${getErrorMessage(err, t(failedKey))}`,
          sourceRequired ? 'warning' : 'error'
        );
        throw err;
      } finally {
        setInstallingID('');
      }
    },
    [clearConfigCache, loadStore, showNotification, t]
  );

  const handleInstall = (entry: PluginStoreEntry) => {
    const isUpdate = entry.installed && entry.updateAvailable;
    if (!isOfficialPlugin(entry)) {
      setGateEntry(entry);
      setGateMode('install');
      setGateIsUpdate(isUpdate);
      return;
    }

    const title = getStoreEntryTitle(entry);
    const target = entry.version ? `${title} v${entry.version}` : title;
    const confirmationMessage = isUpdate ? (
      t('plugin_store.update_confirm_message', { target })
    ) : (
      <div className={styles.installConfirmContent}>
        <p className={styles.installConfirmMessage}>
          {t('plugin_store.install_confirm_message', { target })}
        </p>
        <div className={styles.installSecurityWarning}>
          <IconShield className={styles.installSecurityWarningIcon} size={18} aria-hidden="true" />
          <div className={styles.installSecurityWarningBody}>
            <strong className={styles.installSecurityWarningTitle}>
              {t('plugin_store.install_security_warning_title')}
            </strong>
            <p className={styles.installSecurityWarningText}>
              {t('plugin_store.install_security_warning_message')}
            </p>
          </div>
        </div>
      </div>
    );

    showConfirmation({
      title: isUpdate
        ? t('plugin_store.update_confirm_title')
        : t('plugin_store.install_confirm_title'),
      message: confirmationMessage,
      confirmText: isUpdate ? t('plugin_store.update') : t('plugin_store.install'),
      variant: 'primary',
      onConfirm: () => runInstall(entry, isUpdate),
    });
  };

  const handleDeleteInstalled = (entry: PluginStoreEntry) => {
    if (installingID) return;

    const entryKey = getStoreEntryKey(entry);
    const title = getStoreEntryTitle(entry);

    showConfirmation({
      title: t('plugin_store.delete_confirm_title'),
      message: t('plugin_store.delete_confirm_message', { name: title }),
      confirmText: t('plugin_store.delete_plugin'),
      cancelText: t('common.cancel'),
      variant: 'danger',
      onConfirm: async () => {
        setInstallingID(`${entryKey}:delete`);
        try {
          const result = await pluginsApi.deletePlugin(entry.id);
          clearConfigCache();
          if (result.restartRequired) {
            setRestartRequiredIDs((current) =>
              current.includes(entryKey) ? current : [...current, entryKey]
            );
          }
          await loadStore();
          notifyPluginResourcesChanged();
          showNotification(
            result.restartRequired
              ? t('plugin_store.delete_restart_required')
              : t('plugin_store.delete_success'),
            result.restartRequired ? 'warning' : 'success'
          );
        } catch (err: unknown) {
          showNotification(
            hasRestartRequired(err)
              ? t('plugin_store.delete_restart_required')
              : `${t('plugin_store.delete_failed')}: ${getErrorMessage(
                  err,
                  t('plugin_store.delete_failed')
                )}`,
            hasRestartRequired(err) ? 'warning' : 'error'
          );
          throw err;
        } finally {
          setInstallingID('');
        }
      },
    });
  };

  const runReinstall = useCallback(
    async (entry: PluginStoreEntry) => {
      const entryKey = getStoreEntryKey(entry);
      const sourceId = entry.sourceId || undefined;

      setInstallingID(`${entryKey}:reinstall`);
      try {
        const deleteResult = await pluginsApi.deletePlugin(entry.id);
        clearConfigCache();
        if (deleteResult.restartRequired) {
          setRestartRequiredIDs((current) =>
            current.includes(entryKey) ? current : [...current, entryKey]
          );
          await loadStore();
          notifyPluginResourcesChanged();
          showNotification(t('plugin_store.reinstall_delete_restart_required'), 'warning');
          return;
        }

        const installResult = await pluginStoreApi.install(entry.id, { sourceId });
        if (installResult.restartRequired) {
          setRestartRequiredIDs((current) =>
            current.includes(entryKey) ? current : [...current, entryKey]
          );
          showNotification(t('plugin_store.restart_required_notice'), 'warning');
        }
        await loadStore();
        notifyPluginResourcesChanged();
        if (!installResult.restartRequired) {
          notifyPluginResourcesChanged({
            delayMs: PLUGIN_RESOURCES_SETTLE_REFRESH_DELAY_MS,
          });
        }
        showNotification(t('plugin_store.reinstall_success'), 'success');
      } catch (err: unknown) {
        const sourceRequired = getErrorDetailCode(err) === 'plugin_store_source_required';
        showNotification(
          sourceRequired
            ? t('plugin_store.source_required')
            : hasRestartRequired(err)
              ? t('plugin_store.reinstall_delete_restart_required')
              : `${t('plugin_store.reinstall_failed')}: ${getErrorMessage(
                  err,
                  t('plugin_store.reinstall_failed')
                )}`,
          sourceRequired || hasRestartRequired(err) ? 'warning' : 'error'
        );
        throw err;
      } finally {
        setInstallingID('');
      }
    },
    [clearConfigCache, loadStore, showNotification, t]
  );

  const handleReinstall = (entry: PluginStoreEntry) => {
    if (installingID) return;

    if (!isOfficialPlugin(entry)) {
      setGateEntry(entry);
      setGateMode('reinstall');
      setGateIsUpdate(false);
      return;
    }

    const title = getStoreEntryTitle(entry);
    const target = entry.version ? `${title} v${entry.version}` : title;

    showConfirmation({
      title: t('plugin_store.reinstall_confirm_title'),
      message: t('plugin_store.reinstall_confirm_message', { target }),
      confirmText: t('plugin_store.reinstall_plugin'),
      cancelText: t('common.cancel'),
      variant: 'danger',
      onConfirm: () => runReinstall(entry),
    });
  };

  const handleGateClose = useCallback(() => {
    setGateEntry(null);
  }, []);

  const handleGateConfirm = useCallback(async () => {
    if (!gateEntry) return;
    if (gateMode === 'reinstall') {
      await runReinstall(gateEntry);
    } else {
      await runInstall(gateEntry, gateIsUpdate);
    }
    setGateEntry(null);
  }, [gateEntry, gateIsUpdate, gateMode, runInstall, runReinstall]);

  const renderCard = (entry: PluginStoreEntry) => {
    const entryKey = getStoreEntryKey(entry);
    const logo = resolvePluginAssetURL(entry.logo, apiBase);
    const homepageURL = /^https?:\/\//i.test(entry.homepage) ? entry.homepage : '';
    const isUpdate = entry.installed && entry.updateAvailable;
    const isOfficial = isOfficialPlugin(entry);
    const versionText =
      isUpdate && entry.installedVersion && entry.version
        ? t('plugin_store.version_arrow', { from: entry.installedVersion, to: entry.version })
        : entry.installed && entry.installedVersion
          ? `v${entry.installedVersion}`
          : entry.version
            ? `v${entry.version}`
            : '';
    const sourceLabel = isDefaultPluginStoreSource(entry)
      ? t('plugin_store.cli_proxy_api_source')
      : entry.sourceName || entry.sourceId;
    const metaItems: Array<{
      key: string;
      label: string;
      value: string;
      title?: string;
      tone: 'version' | 'author' | 'license' | 'source';
    }> = [];

    if (versionText) {
      metaItems.push({
        key: 'version',
        label: t('plugin_store.meta_version'),
        value: versionText,
        tone: 'version',
      });
    }
    if (entry.author) {
      metaItems.push({
        key: 'author',
        label: t('plugin_store.meta_author'),
        value: entry.author,
        tone: 'author',
      });
    }
    if (entry.license) {
      metaItems.push({
        key: 'license',
        label: t('plugin_store.meta_license'),
        value: entry.license,
        tone: 'license',
      });
    }
    if (sourceLabel) {
      metaItems.push({
        key: 'source',
        label: t('plugin_store.meta_source'),
        value: sourceLabel,
        title: entry.sourceUrl || sourceLabel,
        tone: 'source',
      });
    }

    const installingCurrent = installingID === entryKey;
    const reinstallingCurrent = installingID === `${entryKey}:reinstall`;
    const deletingCurrent = installingID === `${entryKey}:delete`;

    return (
      <article key={entryKey} className={styles.card}>
        <div className={styles.cardHeader}>
          <div className={styles.logoBox} aria-hidden="true">
            <StoreLogo src={logo} />
          </div>
          <div className={styles.cardTitleBlock}>
            <h2>
              {homepageURL ? (
                <a
                  className={styles.titleLink}
                  href={homepageURL}
                  target="_blank"
                  rel="noreferrer"
                  title={t('plugin_store.open_homepage')}
                  aria-label={t('plugin_store.open_homepage')}
                >
                  {getStoreEntryTitle(entry)}
                </a>
              ) : (
                getStoreEntryTitle(entry)
              )}
            </h2>
            <span>{entry.id}</span>
          </div>
          <div className={styles.badges}>
            {!isOfficial ? (
              <span className={styles.badgeThirdParty}>
                <IconShield size={12} />
                {t('plugin_store.badge_untrusted')}
              </span>
            ) : null}
            {!entry.installed ? (
              <span className={styles.badgeMuted}>{t('plugin_store.badge_not_installed')}</span>
            ) : isUpdate ? (
              <span className={styles.badgeWarn}>{t('plugin_store.badge_update')}</span>
            ) : (
              <span className={styles.badgeOn}>{t('plugin_store.badge_installed')}</span>
            )}
            {entry.installed && entry.effectiveEnabled ? (
              <span className={styles.badge}>{t('plugin_store.badge_effective')}</span>
            ) : null}
          </div>
        </div>

        {entry.description ? <p className={styles.description}>{entry.description}</p> : null}

        {entry.tags.length > 0 ? (
          <div className={styles.tags}>
            {entry.tags.map((tag) => (
              <span key={`${entry.id}-tag-${tag}`}>{tag}</span>
            ))}
          </div>
        ) : null}

        <div className={styles.cardDetails}>
          {metaItems.length > 0 ? (
            <div className={styles.meta}>
              {metaItems.map((item) => (
                <span
                  key={`${entry.id}-meta-${item.key}`}
                  className={styles.metaItem}
                  data-tone={item.tone}
                  title={item.title}
                >
                  <span className={styles.metaLabel}>{item.label}</span>
                  <span className={styles.metaValue}>{item.value}</span>
                </span>
              ))}
            </div>
          ) : null}
        </div>

        <div className={styles.cardFooter}>
          <div className={styles.cardActions}>
            {!entry.installed ? (
              <Button
                size="sm"
                onClick={() => handleInstall(entry)}
                disabled={!connected || Boolean(installingID)}
                loading={installingID === entryKey}
              >
                <IconDownload size={14} />
                {t('plugin_store.install')}
              </Button>
            ) : (
              <>
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={() => handleReinstall(entry)}
                  disabled={!connected || Boolean(installingID)}
                  loading={reinstallingCurrent}
                >
                  <IconRefreshCw size={14} />
                  {t('plugin_store.reinstall_plugin')}
                </Button>
                {entry.updateAvailable ? (
                  <Button
                    size="sm"
                    onClick={() => handleInstall(entry)}
                    disabled={!connected || Boolean(installingID)}
                    loading={installingCurrent}
                  >
                    <IconRefreshCw size={14} />
                    {t('plugin_store.update')}
                  </Button>
                ) : null}
                <Button variant="secondary" size="sm" onClick={handleManageInstalled}>
                  <IconSettings size={14} />
                  {t('plugin_store.manage')}
                </Button>
              </>
            )}
          </div>
          {entry.installed ? (
            <div className={styles.deleteActions}>
              <Button
                variant="secondary"
                size="sm"
                iconOnly
                className={styles.dangerIconButton}
                onClick={() => handleDeleteInstalled(entry)}
                disabled={!connected || Boolean(installingID)}
                loading={deletingCurrent}
                title={t('plugin_store.delete_plugin')}
                aria-label={t('plugin_store.delete_plugin')}
              >
                <IconTrash2 size={14} />
              </Button>
            </div>
          ) : null}
        </div>
      </article>
    );
  };

  return (
    <div className={styles.page}>
      {error ? (
        <div className={styles.errorBox}>
          <span>{error.message}</span>
          {error.kind !== 'unsupported' ? (
            <Button variant="secondary" size="sm" onClick={loadStore} disabled={loading}>
              {t('plugin_store.retry')}
            </Button>
          ) : null}
        </div>
      ) : null}

      {data && !data.pluginsEnabled ? (
        <div className={styles.warningBox}>{t('plugin_store.global_disabled_hint')}</div>
      ) : null}

      {restartNames.length > 0 ? (
        <div className={styles.warningBox}>
          {t('plugin_store.restart_required_banner', { plugins: restartNames.join(', ') })}
        </div>
      ) : null}

      {data && data.sourceErrors.length > 0 ? (
        <section className={styles.sourceErrorList}>
          <strong>{t('plugin_store.source_errors_title')}</strong>
          {data.sourceErrors.map((sourceError) => (
            <span key={`${sourceError.sourceId || sourceError.sourceUrl}-${sourceError.message}`}>
              {sourceError.sourceName ||
                sourceError.sourceId ||
                sourceError.sourceUrl ||
                t('plugin_store.unknown_source')}
              {sourceError.message ? `: ${sourceError.message}` : ''}
            </span>
          ))}
        </section>
      ) : null}

      <section className={styles.tabSurface}>
        <div className={styles.controlPanel}>
          <div className={styles.controlHeader}>
            {tabsControl ? <div className={styles.summaryTabs}>{tabsControl}</div> : null}
            {data ? (
              <div className={styles.summaryMetrics}>
                <span
                  className={styles.summaryMetric}
                  data-tone={data.pluginsEnabled ? 'enabled' : 'disabled'}
                >
                  <span className={styles.summaryMetricLabel}>
                    {t('plugin_store.global_status')}
                  </span>
                  <strong>
                    {data.pluginsEnabled
                      ? t('plugin_store.global_enabled')
                      : t('plugin_store.global_disabled')}
                  </strong>
                </span>
                <span className={styles.summaryMetric}>
                  <span className={styles.summaryMetricLabel}>{t('plugin_store.plugins_dir')}</span>
                  <strong>{data.pluginsDir || 'plugins'}</strong>
                </span>
                <span className={styles.summaryMetric} data-tone="available">
                  <span className={styles.summaryMetricLabel}>
                    {t('plugin_store.stat_available')}
                  </span>
                  <strong>{stats.total}</strong>
                </span>
                <span className={styles.summaryMetric}>
                  <span className={styles.summaryMetricLabel}>{t('plugin_store.sources')}</span>
                  <strong>{data.sources.length}</strong>
                </span>
              </div>
            ) : null}
          </div>

          <div className={styles.controlToolbar}>
            <Input
              type="search"
              value={filter}
              onChange={(event) => setFilter(event.target.value)}
              placeholder={t('plugin_store.search_placeholder')}
              aria-label={t('plugin_store.search_label')}
              rightElement={<IconSearch size={16} />}
            />
            <div className={styles.toolbarActions}>
              <Button variant="secondary" size="sm" onClick={handleManageInstalled}>
                <IconSettings size={16} />
                {t('plugin_store.manage')}
              </Button>
              <Button
                variant="secondary"
                size="sm"
                onClick={loadStore}
                disabled={!connected || !supportsPlugin || loading}
                loading={loading}
              >
                {loading ? null : <IconRefreshCw size={16} />}
                {t('plugin_store.refresh')}
              </Button>
            </div>
          </div>
        </div>
      </section>

      <section className={styles.storeContentSurface}>
        {!initialLoading ? (
          <div
            className={styles.filters}
            role="tablist"
            aria-label={t('plugin_store.filter_label')}
          >
            {statusFilters.map((item) => (
              <button
                key={item.key}
                id={`plugin-store-filter-${item.key}`}
                type="button"
                role="tab"
                className={`${styles.filterChip} ${
                  statusFilter === item.key ? styles.filterChipActive : ''
                }`}
                onClick={() => setStatusFilter(item.key)}
                onKeyDown={(event) => handleStatusFilterKeyDown(event, item.key)}
                aria-selected={statusFilter === item.key}
                tabIndex={statusFilter === item.key ? 0 : -1}
              >
                {item.label}
                <span>{item.count}</span>
              </button>
            ))}
          </div>
        ) : null}

        {initialLoading ? (
          <section className={styles.loadingPanel} aria-busy="true" aria-live="polite">
            <LoadingSpinner size={22} className={styles.loadingSpinner} />
            <div className={styles.loadingText}>
              <strong>{t('plugin_store.loading_title')}</strong>
              <span>{t('plugin_store.loading_desc')}</span>
            </div>
          </section>
        ) : visiblePlugins.length === 0 ? (
          !error ? (
            stats.total === 0 ? (
              <EmptyState
                title={t('plugin_store.no_plugins')}
                description={t('plugin_store.no_plugins_desc')}
                action={
                  <Button variant="secondary" size="sm" onClick={loadStore} disabled={!connected}>
                    <IconRefreshCw size={16} />
                    {t('plugin_store.refresh')}
                  </Button>
                }
              />
            ) : (
              <EmptyState
                title={t('plugin_store.no_matches')}
                description={t('plugin_store.no_matches_desc')}
                action={
                  hasActiveFilters ? (
                    <Button
                      variant="secondary"
                      size="sm"
                      onClick={() => {
                        setFilter('');
                        setStatusFilter('all');
                      }}
                    >
                      {t('plugin_store.clear_filters')}
                    </Button>
                  ) : undefined
                }
              />
            )
          ) : null
        ) : (
          <div className={styles.grid}>{visiblePlugins.map((entry) => renderCard(entry))}</div>
        )}
      </section>
      <PluginInstallGateModal
        open={Boolean(gateEntry)}
        entry={gateEntry}
        isUpdate={gateIsUpdate}
        installing={
          gateEntry
            ? installingID === getStoreEntryKey(gateEntry) ||
              installingID === `${getStoreEntryKey(gateEntry)}:reinstall`
            : false
        }
        onClose={handleGateClose}
        onConfirm={handleGateConfirm}
      />
    </div>
  );
}
