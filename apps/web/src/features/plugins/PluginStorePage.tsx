import {
  useCallback,
  useEffect,
  useId,
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
import { Modal } from '@/components/ui/Modal';
import { Select } from '@/components/ui/Select';
import {
  IconDownload,
  IconExternalLink,
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
import {
  isPluginStoreInstallSettled,
  pluginVersionMatches,
  waitForPluginStoreState,
} from './pluginPolling';
import { PluginInstallGateModal } from './components/PluginInstallGateModal';
import {
  buildGitHubReleasesPageURL,
  fetchPluginReleaseVersions,
  isValidManualReleaseTag,
  supportsPluginVersionSelection,
  type PluginReleaseVersion,
} from './pluginReleaseVersions';
import styles from './PluginStorePage.module.scss';

type StoreStatusFilter = 'all' | 'installed' | 'notInstalled' | 'updates';
type InstallVersionMode = 'latest' | 'release' | 'manual';

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
const releaseVersionsCache = new Map<string, PluginReleaseVersion[]>();
const formatPluginVersion = (version: string) => {
  const trimmed = version.trim();
  if (!trimmed) return '';
  return /^v/i.test(trimmed) ? trimmed : `v${trimmed}`;
};

const formatReleaseDate = (value: string, locale: string) => {
  if (!value) return '';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '';
  return new Intl.DateTimeFormat(locale, {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
  }).format(date);
};

function StoreLogo({ src }: { src: string }) {
  const [failed, setFailed] = useState(false);
  const showImage = Boolean(src) && !failed;

  return showImage ? (
    <img src={src} alt="" onError={() => setFailed(true)} />
  ) : (
    <IconPlugin size={18} />
  );
}

type PluginInstallOptionsModalProps = {
  entry: PluginStoreEntry | null;
  isUpdate: boolean;
  version: string;
  installing: boolean;
  onVersionChange: (version: string) => void;
  onClose: () => void;
  onConfirm: () => void | Promise<void>;
};

function PluginInstallOptionsModal({
  entry,
  isUpdate,
  version,
  installing,
  onVersionChange,
  onClose,
  onConfirm,
}: PluginInstallOptionsModalProps) {
  const { i18n, t } = useTranslation();
  const versionModeLabelId = useId();
  const entryKey = entry ? getStoreEntryKey(entry) : '';
  const entryRepository = entry?.repository ?? '';
  const supportsVersionSelection = entry
    ? supportsPluginVersionSelection(entry.installType)
    : false;
  const releasePageURL =
    entry && supportsVersionSelection ? buildGitHubReleasesPageURL(entry.repository) : '';
  const releaseCacheKey = entryKey
    ? `${entryKey}|${entryRepository.trim()}|${entry?.installType.trim().toLowerCase() ?? ''}`
    : '';
  const cachedReleaseVersions = releaseCacheKey
    ? releaseVersionsCache.get(releaseCacheKey)
    : undefined;
  const [versionMode, setVersionMode] = useState<InstallVersionMode>('latest');
  const [showPrerelease, setShowPrerelease] = useState(false);
  const [releaseVersions, setReleaseVersions] = useState<PluginReleaseVersion[]>(
    () => cachedReleaseVersions ?? []
  );
  const [releaseLoading, setReleaseLoading] = useState(
    () => Boolean(entryKey && releasePageURL && !cachedReleaseVersions)
  );
  const [releaseError, setReleaseError] = useState('');

  useEffect(() => {
    if (!entryKey || !supportsVersionSelection || !releasePageURL) return;
    if (releaseVersionsCache.has(releaseCacheKey)) return;

    let active = true;

    fetchPluginReleaseVersions(entryRepository)
      .then((releases) => {
        if (!active) return;
        releaseVersionsCache.set(releaseCacheKey, releases);
        setReleaseVersions(releases);
      })
      .catch((err: unknown) => {
        if (!active) return;
        setReleaseError(getErrorMessage(err, t('plugin_store.install_versions_load_failed')));
      })
      .finally(() => {
        if (!active) return;
        setReleaseLoading(false);
      });

    return () => {
      active = false;
    };
  }, [entryKey, entryRepository, releaseCacheKey, releasePageURL, supportsVersionSelection, t]);

  const stableReleaseVersions = useMemo(
    () => releaseVersions.filter((release) => !release.prerelease),
    [releaseVersions]
  );
  const visibleReleaseVersions = useMemo(
    () =>
      showPrerelease
        ? releaseVersions
        : releaseVersions.filter((release) => !release.prerelease),
    [releaseVersions, showPrerelease]
  );
  const latestRelease = stableReleaseVersions[0] ?? releaseVersions[0] ?? null;
  const releaseOptions = useMemo(
    () =>
      visibleReleaseVersions.map((release) => {
        const releaseTitle =
          release.name && release.name !== release.tagName
            ? `${release.tagName} - ${release.name}`
            : release.tagName;
        const releaseDate = formatReleaseDate(release.publishedAt, i18n.language);
        const labelParts = [
          releaseTitle,
          releaseDate,
          release.prerelease ? t('plugin_store.install_version_prerelease_badge') : '',
          release.assetNames.length > 0
            ? t('plugin_store.install_version_assets_count', {
                count: release.assetNames.length,
              })
            : '',
        ].filter(Boolean);
        return {
          value: release.tagName,
          label: labelParts.join(' · '),
        };
      }),
    [i18n.language, t, visibleReleaseVersions]
  );

  useEffect(() => {
    if (!supportsVersionSelection || versionMode !== 'release') return;
    if (visibleReleaseVersions.length === 0) {
      if (version) onVersionChange('');
      return;
    }
    if (visibleReleaseVersions.some((release) => release.tagName === version)) return;
    onVersionChange(visibleReleaseVersions[0].tagName);
  }, [onVersionChange, supportsVersionSelection, version, versionMode, visibleReleaseVersions]);

  if (!entry) return null;

  const requestedVersion =
    !supportsVersionSelection || versionMode === 'latest' ? '' : version.trim();
  const displayVersion = requestedVersion || entry.version;
  const target = displayVersion
    ? `${getStoreEntryTitle(entry)} ${formatPluginVersion(displayVersion)}`
    : getStoreEntryTitle(entry);
  const latestVersionLabel = entry.version
    ? formatPluginVersion(entry.version)
    : latestRelease
      ? formatPluginVersion(latestRelease.tagName)
      : t('plugin_store.install_version_latest');
  const hasPrereleaseVersions = releaseVersions.some((release) => release.prerelease);
  const releaseModeDisabled = installing || (releaseLoading && releaseVersions.length === 0);
  const releaseStatusMessage = supportsVersionSelection && !releasePageURL
    ? t('plugin_store.install_version_non_github')
    : releaseError
      ? `${t('plugin_store.install_versions_load_failed')}: ${releaseError}`
      : '';
  const manualVersionInvalid =
    supportsVersionSelection &&
    versionMode === 'manual' &&
    Boolean(version.trim()) &&
    !isValidManualReleaseTag(version);
  const currentVersionSelected =
    Boolean(requestedVersion) &&
    Boolean(entry.installedVersion) &&
    pluginVersionMatches(entry.installedVersion, requestedVersion);
  const confirmDisabled =
    installing ||
    (supportsVersionSelection && versionMode === 'release' && !requestedVersion) ||
    (supportsVersionSelection && versionMode === 'manual' && !isValidManualReleaseTag(version)) ||
    currentVersionSelected;

  const handleVersionModeChange = (nextMode: InstallVersionMode) => {
    if (installing) return;
    if (!supportsVersionSelection && nextMode !== 'latest') return;
    setVersionMode(nextMode);
    if (nextMode === 'latest') {
      onVersionChange('');
      return;
    }
    if (nextMode === 'release') {
      onVersionChange(visibleReleaseVersions[0]?.tagName ?? '');
      return;
    }
    onVersionChange('');
  };

  const handleClose = () => {
    if (installing) return;
    onClose();
  };

  return (
    <Modal
      open={Boolean(entry)}
      onClose={handleClose}
      closeDisabled={installing}
      width={640}
      title={isUpdate ? t('plugin_store.update_confirm_title') : t('plugin_store.install_confirm_title')}
      footer={
        <>
          <Button variant="secondary" onClick={handleClose} disabled={installing}>
            {t('common.cancel')}
          </Button>
          <Button onClick={onConfirm} loading={installing} disabled={confirmDisabled}>
            {isUpdate ? t('plugin_store.update') : t('plugin_store.install')}
          </Button>
        </>
      }
    >
      <div className={styles.installOptions}>
        <p className={styles.installOptionsMessage}>
          {isUpdate
            ? t('plugin_store.update_confirm_message', { target })
            : t('plugin_store.install_confirm_message', { target })}
        </p>
        <div className={styles.installVersionField}>
          <span className={styles.installVersionLabel} id={versionModeLabelId}>
            {t('plugin_store.install_version_label')}
          </span>
          <div
            className={styles.installVersionModes}
            role="radiogroup"
            aria-labelledby={versionModeLabelId}
          >
            <label
              className={`${styles.installVersionMode} ${
                versionMode === 'latest' ? styles.installVersionModeActive : ''
              } ${installing ? styles.installVersionModeDisabled : ''}`}
            >
              <input
                type="radio"
                name="plugin-store-install-version-mode"
                checked={versionMode === 'latest'}
                onChange={() => handleVersionModeChange('latest')}
                disabled={installing}
              />
              <span className={styles.installVersionModeText}>
                <strong>{t('plugin_store.install_version_latest_mode')}</strong>
                <small>
                  {t('plugin_store.install_version_latest_hint', {
                    version: latestVersionLabel,
                  })}
                </small>
              </span>
            </label>
            {supportsVersionSelection ? (
              <>
                <label
                  className={`${styles.installVersionMode} ${
                    versionMode === 'release' ? styles.installVersionModeActive : ''
                  } ${releaseModeDisabled ? styles.installVersionModeDisabled : ''}`}
                >
                  <input
                    type="radio"
                    name="plugin-store-install-version-mode"
                    checked={versionMode === 'release'}
                    onChange={() => handleVersionModeChange('release')}
                    disabled={releaseModeDisabled}
                  />
                  <span className={styles.installVersionModeText}>
                    <strong>{t('plugin_store.install_version_release_mode')}</strong>
                    <small>
                      {releaseLoading
                        ? t('plugin_store.install_versions_loading')
                        : t('plugin_store.install_version_release_hint')}
                    </small>
                  </span>
                </label>
                <label
                  className={`${styles.installVersionMode} ${
                    versionMode === 'manual' ? styles.installVersionModeActive : ''
                  } ${installing ? styles.installVersionModeDisabled : ''}`}
                >
                  <input
                    type="radio"
                    name="plugin-store-install-version-mode"
                    checked={versionMode === 'manual'}
                    onChange={() => handleVersionModeChange('manual')}
                    disabled={installing}
                  />
                  <span className={styles.installVersionModeText}>
                    <strong>{t('plugin_store.install_version_manual_mode')}</strong>
                    <small>{t('plugin_store.install_version_manual_hint')}</small>
                  </span>
                </label>
              </>
            ) : null}
          </div>

          {supportsVersionSelection && versionMode === 'release' ? (
            <div className={styles.installVersionPanel}>
              {releaseStatusMessage ? (
                <p className={styles.installVersionWarning}>{releaseStatusMessage}</p>
              ) : null}
              {!releaseLoading && !releaseStatusMessage && releaseVersions.length === 0 ? (
                <p className={styles.installVersionHint}>
                  {t('plugin_store.install_versions_empty')}
                </p>
              ) : null}
              {!releaseLoading &&
              !releaseStatusMessage &&
              releaseVersions.length > 0 &&
              visibleReleaseVersions.length === 0 ? (
                <p className={styles.installVersionHint}>
                  {t('plugin_store.install_versions_only_prerelease')}
                </p>
              ) : null}
              <Select
                value={version}
                options={releaseOptions}
                onChange={onVersionChange}
                placeholder={t('plugin_store.install_version_release_placeholder')}
                disabled={installing || releaseLoading || releaseOptions.length === 0}
                ariaLabel={t('plugin_store.install_version_release_select')}
              />
              {hasPrereleaseVersions ? (
                <label className={styles.installVersionCheckbox}>
                  <input
                    type="checkbox"
                    checked={showPrerelease}
                    onChange={(event) => setShowPrerelease(event.target.checked)}
                    disabled={installing}
                  />
                  <span>{t('plugin_store.install_version_show_prerelease')}</span>
                </label>
              ) : null}
            </div>
          ) : null}

          {supportsVersionSelection && versionMode === 'manual' ? (
            <div className={styles.installVersionPanel}>
              <Input
                id="plugin-store-install-version"
                value={version}
                onChange={(event) => onVersionChange(event.target.value)}
                placeholder={t('plugin_store.install_version_manual_placeholder')}
                disabled={installing}
                autoComplete="off"
                spellCheck={false}
                aria-invalid={manualVersionInvalid}
              />
              {manualVersionInvalid ? (
                <p className={styles.installVersionWarning}>
                  {t('plugin_store.install_version_manual_error')}
                </p>
              ) : null}
            </div>
          ) : null}

          {currentVersionSelected ? (
            <p className={styles.installVersionWarning}>
              {t('plugin_store.install_version_current_selected', {
                version: formatPluginVersion(entry.installedVersion),
              })}
            </p>
          ) : null}

          {supportsVersionSelection && releasePageURL ? (
            <a
              className={styles.installReleaseLink}
              href={releasePageURL}
              target="_blank"
              rel="noreferrer"
            >
              <span>{t('plugin_store.install_version_releases_link')}</span>
              <IconExternalLink size={12} />
            </a>
          ) : null}
        </div>
      </div>
    </Modal>
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
  const [gateRequestedVersion, setGateRequestedVersion] = useState('');
  const [installOptionsEntry, setInstallOptionsEntry] = useState<PluginStoreEntry | null>(null);
  const [installOptionsIsUpdate, setInstallOptionsIsUpdate] = useState(false);
  const [installVersion, setInstallVersion] = useState('');

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
    async (entry: PluginStoreEntry, isUpdate: boolean, requestedVersion = '') => {
      const failedKey = isUpdate ? 'plugin_store.update_failed' : 'plugin_store.install_failed';
      const entryKey = getStoreEntryKey(entry);
      const sourceId = entry.sourceId || undefined;
      const version = requestedVersion.trim();

      setInstallingID(entryKey);
      try {
        const result = await pluginStoreApi.install(entry.id, {
          sourceId,
          version: version || undefined,
        });
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
        if (result.restartRequired) {
          await loadStore();
        } else {
          const waitResult = await waitForPluginStoreState(
            entry.id,
            entry.sourceId,
            (plugin) => isPluginStoreInstallSettled(plugin, version)
          );
          setData(waitResult.response);
        }
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
    setInstallOptionsEntry(entry);
    setInstallOptionsIsUpdate(isUpdate);
    setInstallVersion('');
  };

  const handleInstallOptionsClose = useCallback(() => {
    if (installingID) return;
    setInstallOptionsEntry(null);
    setInstallVersion('');
  }, [installingID]);

  const handleInstallOptionsConfirm = useCallback(async () => {
    if (!installOptionsEntry) return;
    const requestedVersion = installVersion.trim();
    if (!isOfficialPlugin(installOptionsEntry)) {
      setGateEntry(installOptionsEntry);
      setGateMode('install');
      setGateIsUpdate(installOptionsIsUpdate);
      setGateRequestedVersion(requestedVersion);
      setInstallOptionsEntry(null);
      setInstallVersion('');
      return;
    }
    try {
      await runInstall(installOptionsEntry, installOptionsIsUpdate, requestedVersion);
      setInstallOptionsEntry(null);
      setInstallVersion('');
    } catch {
      // runInstall has already shown a notification; keep the modal open for correction.
    }
  }, [installOptionsEntry, installOptionsIsUpdate, installVersion, runInstall]);

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
    setGateRequestedVersion('');
  }, []);

  const handleGateConfirm = useCallback(async () => {
    if (!gateEntry) return;
    if (gateMode === 'reinstall') {
      await runReinstall(gateEntry);
    } else {
      await runInstall(gateEntry, gateIsUpdate, gateRequestedVersion);
    }
    setGateEntry(null);
    setGateRequestedVersion('');
  }, [gateEntry, gateIsUpdate, gateMode, gateRequestedVersion, runInstall, runReinstall]);

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
    const platformText =
      entry.platforms.length > 0
        ? t('plugin_store.platforms', {
            platforms: entry.platforms
              .map((platform) => `${platform.goos}/${platform.goarch}`)
              .join(', '),
          })
        : '';
    const authText = entry.authRequired
      ? entry.authConfigured
        ? t('plugin_store.auth_configured')
        : t('plugin_store.auth_required')
      : '';
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
    if (platformText) {
      metaItems.push({
        key: 'platforms',
        label: t('plugin_store.meta_platforms'),
        value: platformText,
        tone: 'source',
      });
    }

    const installingCurrent = installingID === entryKey;
    const reinstallingCurrent = installingID === `${entryKey}:reinstall`;
    const deletingCurrent = installingID === `${entryKey}:delete`;
    const missingAuth = entry.authRequired && !entry.authConfigured;
    const actionDisabled = !connected || missingAuth || Boolean(installingID);
    const actionTitle = missingAuth ? t('plugin_store.auth_required_hint') : undefined;

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
            {entry.authRequired ? (
              <span className={entry.authConfigured ? styles.badge : styles.badgeWarn}>
                {authText}
              </span>
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
                disabled={actionDisabled}
                loading={installingID === entryKey}
                title={actionTitle}
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
                    disabled={actionDisabled}
                    loading={installingCurrent}
                    title={actionTitle}
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
      <PluginInstallOptionsModal
        key={installOptionsEntry ? getStoreEntryKey(installOptionsEntry) : 'closed'}
        entry={installOptionsEntry}
        isUpdate={installOptionsIsUpdate}
        version={installVersion}
        installing={installOptionsEntry ? installingID === getStoreEntryKey(installOptionsEntry) : false}
        onVersionChange={setInstallVersion}
        onClose={handleInstallOptionsClose}
        onConfirm={handleInstallOptionsConfirm}
      />
    </div>
  );
}
