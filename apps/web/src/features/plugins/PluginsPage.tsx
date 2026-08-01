import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ChangeEvent,
  type ReactNode,
} from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { Button } from '@/components/ui/Button';
import { Drawer } from '@/components/ui/Drawer';
import { DropdownMenu, type DropdownMenuItem } from '@/components/ui/DropdownMenu';
import { EmptyState } from '@/components/ui/EmptyState';
import { Input } from '@/components/ui/Input';
import { Select } from '@/components/ui/Select';
import { SegmentedTabs, type SegmentedTabItem } from '@/components/ui/SegmentedTabs';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import {
  IconGithub,
  IconKey,
  IconMoreVertical,
  IconPlugin,
  IconPlus,
  IconRefreshCw,
  IconSearch,
  IconSettings,
  IconTrash2,
} from '@/components/ui/icons';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { pluginsApi, pluginStoreApi } from '@/services/api';
import { useAuthStore, useConfigStore, useNotificationStore } from '@/stores';
import { getErrorMessage, isRecord } from '@/utils/helpers';
import type {
  PluginConfigField,
  PluginListEntry,
  PluginListResponse,
} from '@/types';
import {
  buildRepositoryURL,
  getPluginTitle,
  notifyPluginResourcesChanged,
  resolvePluginAssetURL,
} from './pluginResources';
import { waitForPluginState } from './pluginPolling';
import { buildPluginDisplay, type PluginStatusTone } from './pluginDisplay';
import { PluginStorePage } from './PluginStorePage';
import {
  buildPluginConfigPatch,
  createPluginConfigDraft,
  type PluginConfigDraftState,
} from './pluginConfigDraft';
import styles from './PluginsPage.module.scss';

type PluginPageTab = 'installed' | 'store';
type PluginConfigDraft = PluginConfigDraftState;

const PLUGIN_ENABLE_REFRESH_DELAY_MS = 1600;

const resolvePluginPageTab = (value: string | null): PluginPageTab =>
  value === 'store' ? 'store' : 'installed';

const wait = (ms: number) =>
  new Promise<void>((resolve) => {
    window.setTimeout(resolve, ms);
  });

const hasStatus = (error: unknown, status: number) => isRecord(error) && error.status === status;

const hasRestartRequired = (error: unknown) =>
  isRecord(error) && isRecord(error.data) && error.data.restart_required === true;

const getErrorDetailCode = (error: unknown): string => {
  if (!isRecord(error) || !isRecord(error.details)) return '';
  const code = error.details.error;
  return typeof code === 'string' ? code.trim() : '';
};

const normalizeFieldType = (field: PluginConfigField) => field.type.trim().toLowerCase();

function PluginLogo({ src }: { src: string }) {
  const [failed, setFailed] = useState(false);
  const showImage = Boolean(src) && !failed;

  return showImage ? (
    <img src={src} alt="" onError={() => setFailed(true)} />
  ) : (
    <IconPlugin size={18} />
  );
}

const getStatusBadgeClassName = (tone: PluginStatusTone) => {
  if (tone === 'success') return styles.badgeOn;
  if (tone === 'warning') return styles.badgeWarn;
  return styles.badgeMuted;
};

const openExternalURL = (url: string) => {
  if (typeof window === 'undefined') return;
  window.open(url, '_blank', 'noreferrer');
};

export function PluginsPage() {
  const { t } = useTranslation();
  const [searchParams, setSearchParams] = useSearchParams();
  const [activeTab, setActiveTab] = useState<PluginPageTab>(() =>
    resolvePluginPageTab(searchParams.get('tab'))
  );
  const [storeMounted, setStoreMounted] = useState(activeTab === 'store');

  const tabs = useMemo<ReadonlyArray<SegmentedTabItem<PluginPageTab>>>(
    () => [
      { id: 'installed', label: t('plugin_management.tab_installed') },
      { id: 'store', label: t('plugin_management.tab_store') },
    ],
    [t]
  );

  const handleTabChange = useCallback(
    (tab: PluginPageTab) => {
      setActiveTab(tab);
      if (tab === 'store') {
        setStoreMounted(true);
      }
      const next = new URLSearchParams(
        typeof window === 'undefined' ? searchParams.toString() : window.location.search
      );
      if (tab === 'store') {
        next.set('tab', 'store');
      } else {
        next.delete('tab');
      }
      setSearchParams(next, { replace: true });
    },
    [searchParams, setSearchParams]
  );

  const tabsControl = (
    <SegmentedTabs
      items={tabs}
      activeTab={activeTab}
      onChange={handleTabChange}
      ariaLabel={t('plugin_management.tabs_aria_label')}
    />
  );

  return (
    <>
      {activeTab === 'installed' ? (
        <InstalledPluginsView
          tabsControl={tabsControl}
          onOpenStore={() => handleTabChange('store')}
        />
      ) : null}
      {storeMounted ? (
        <div hidden={activeTab !== 'store'}>
          <PluginStorePage
            active={activeTab === 'store'}
            tabsControl={tabsControl}
            onManageInstalled={() => handleTabChange('installed')}
          />
        </div>
      ) : null}
    </>
  );
}

function InstalledPluginsView({
  tabsControl,
  onOpenStore,
}: {
  tabsControl: ReactNode;
  onOpenStore: () => void;
}) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const apiBase = useAuthStore((state) => state.apiBase);
  const supportsPlugin = useAuthStore((state) => state.supportsPlugin);
  const clearConfigCache = useConfigStore((state) => state.clearCache);
  const showNotification = useNotificationStore((state) => state.showNotification);
  const showConfirmation = useNotificationStore((state) => state.showConfirmation);

  const [data, setData] = useState<PluginListResponse | null>(null);
  const [filter, setFilter] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [editingPlugin, setEditingPlugin] = useState<PluginListEntry | null>(null);
  const [draft, setDraft] = useState<PluginConfigDraft | null>(null);
  const [mutatingID, setMutatingID] = useState('');
  const [openingConfigID, setOpeningConfigID] = useState('');
  const configRequestSeq = useRef(0);

  const connected = connectionStatus === 'connected';

  const loadPlugins = useCallback(async () => {
    if (!connected) {
      setLoading(false);
      setError(t('notification.connection_required'));
      return;
    }
    if (!supportsPlugin) {
      setLoading(false);
      setError(t('plugin_management.unsupported_backend'));
      return;
    }

    setLoading(true);
    setError('');
    try {
      const plugins = await pluginsApi.list();
      setData(plugins);
    } catch (err: unknown) {
      setError(
        hasStatus(err, 404)
          ? t('plugin_management.unsupported_backend')
          : getErrorMessage(err, t('plugin_management.load_failed'))
      );
    } finally {
      setLoading(false);
    }
  }, [connected, supportsPlugin, t]);

  const loadPluginsAfterMutation = useCallback(
    async (
      waitForRegistration: boolean,
      pluginID?: string,
      predicate?: (plugin: PluginListEntry, response: PluginListResponse) => boolean
    ) => {
      if (waitForRegistration && pluginID && predicate) {
        const result = await waitForPluginState(pluginID, predicate);
        setData(result.response);
        return;
      }
      if (waitForRegistration) {
        await wait(PLUGIN_ENABLE_REFRESH_DELAY_MS);
      }
      await loadPlugins();
    },
    [loadPlugins]
  );

  useHeaderRefresh(loadPlugins, connected && supportsPlugin);

  useEffect(() => {
    void loadPlugins();
  }, [loadPlugins]);

  const pluginStats = useMemo(() => {
    const plugins = data?.plugins ?? [];
    return {
      installed: plugins.length,
      effective: plugins.filter((plugin) => plugin.effectiveEnabled).length,
    };
  }, [data?.plugins]);

  const visiblePlugins = useMemo(() => {
    const query = filter.trim().toLowerCase();
    const plugins = data?.plugins ?? [];
    if (!query) return plugins;

    return plugins.filter((plugin) => {
      const haystack = [
        plugin.id,
        plugin.path,
        plugin.metadata?.name,
        plugin.metadata?.author,
        plugin.metadata?.version,
        plugin.metadata?.githubRepository,
        ...plugin.menus.map((menu) => `${menu.menu} ${menu.path} ${menu.description}`),
      ]
        .filter(Boolean)
        .join(' ')
        .toLowerCase();
      return haystack.includes(query);
    });
  }, [data?.plugins, filter]);

  const resolvePluginAsset = useCallback(
    (value: string) => resolvePluginAssetURL(value, apiBase),
    [apiBase]
  );

  const openConfigDrawer = async (plugin: PluginListEntry) => {
    if (openingConfigID || mutatingID) return;

    const requestSeq = configRequestSeq.current + 1;
    configRequestSeq.current = requestSeq;
    setOpeningConfigID(plugin.id);
    setEditingPlugin(plugin);
    setDraft(null);

    try {
      const currentConfig = await pluginsApi.getConfig(plugin.id);
      if (configRequestSeq.current !== requestSeq) return;

      setDraft(createPluginConfigDraft(plugin.configFields, currentConfig, plugin.enabled));
    } catch (err: unknown) {
      if (configRequestSeq.current !== requestSeq) return;

      setEditingPlugin(null);
      setDraft(null);
      showNotification(
        hasStatus(err, 404)
          ? t('plugin_management.config_not_found')
          : `${t('plugin_management.config_load_failed')}: ${getErrorMessage(
              err,
              t('plugin_management.config_load_failed')
            )}`,
        'error'
      );
    } finally {
      if (configRequestSeq.current === requestSeq) {
        setOpeningConfigID('');
      }
    }
  };

  const closeConfigDrawer = () => {
    if (mutatingID || openingConfigID) return;
    setEditingPlugin(null);
    setDraft(null);
  };

  const updateDraft = (updater: (current: PluginConfigDraft) => PluginConfigDraft) => {
    setDraft((current) => (current ? updater(current) : current));
  };

  const handleTogglePlugin = async (plugin: PluginListEntry, enabled: boolean) => {
    setMutatingID(plugin.id);
    try {
      await pluginsApi.updateEnabled(plugin.id, enabled);
      clearConfigCache();
      await loadPluginsAfterMutation(true, plugin.id, (item) => item.enabled === enabled);
      notifyPluginResourcesChanged();
      showNotification(t('plugin_management.toggle_success'), 'success');
    } catch (err: unknown) {
      showNotification(
        `${t('plugin_management.toggle_failed')}: ${getErrorMessage(
          err,
          t('plugin_management.toggle_failed')
        )}`,
        'error'
      );
    } finally {
      setMutatingID('');
    }
  };

  const handleDeletePlugin = (plugin: PluginListEntry) => {
    if (openingConfigID || mutatingID) return;

    showConfirmation({
      title: t('plugin_management.delete_confirm_title'),
      message: t('plugin_management.delete_confirm_message', {
        name: getPluginTitle(plugin),
      }),
      confirmText: t('plugin_management.delete_plugin'),
      cancelText: t('common.cancel'),
      variant: 'danger',
      onConfirm: async () => {
        setMutatingID(plugin.id);
        try {
          const result = await pluginsApi.deletePlugin(plugin.id);
          clearConfigCache();
          if (editingPlugin?.id === plugin.id) {
            setEditingPlugin(null);
            setDraft(null);
          }
          await loadPluginsAfterMutation(false);
          notifyPluginResourcesChanged();
          showNotification(
            result.restartRequired
              ? t('plugin_management.delete_restart_required')
              : t('plugin_management.delete_success'),
            result.restartRequired ? 'warning' : 'success'
          );
        } catch (err: unknown) {
          showNotification(
            hasStatus(err, 409) && hasRestartRequired(err)
              ? t('plugin_management.delete_restart_required')
              : `${t('plugin_management.delete_failed')}: ${getErrorMessage(
                  err,
                  t('plugin_management.delete_failed')
                )}`,
            hasStatus(err, 409) && hasRestartRequired(err) ? 'warning' : 'error'
          );
        } finally {
          setMutatingID('');
        }
      },
    });
  };

  const handleReinstallPlugin = (plugin: PluginListEntry) => {
    if (openingConfigID || mutatingID) return;

    showConfirmation({
      title: t('plugin_management.reinstall_confirm_title'),
      message: t('plugin_management.reinstall_confirm_message', {
        name: getPluginTitle(plugin),
      }),
      confirmText: t('plugin_management.reinstall_plugin'),
      cancelText: t('common.cancel'),
      variant: 'danger',
      onConfirm: async () => {
        const mutationKey = `${plugin.id}:reinstall`;
        setMutatingID(mutationKey);
        try {
          const store = await pluginStoreApi.list();
          const candidates = store.plugins.filter((entry) => entry.id === plugin.id);
          const storeEntry =
            candidates.find((entry) => entry.installed) ??
            (candidates.length === 1 ? candidates[0] : null);

          if (!storeEntry) {
            showNotification(t('plugin_management.reinstall_store_entry_not_found'), 'warning');
            return;
          }

          const deleteResult = await pluginsApi.deletePlugin(plugin.id);
          clearConfigCache();
          if (editingPlugin?.id === plugin.id) {
            setEditingPlugin(null);
            setDraft(null);
          }
          if (deleteResult.restartRequired) {
            await loadPluginsAfterMutation(false);
            notifyPluginResourcesChanged();
            showNotification(t('plugin_management.reinstall_delete_restart_required'), 'warning');
            return;
          }

          const sourceId = storeEntry.sourceId || undefined;
          const installResult = await pluginStoreApi.install(storeEntry.id, { sourceId });
          clearConfigCache();
          await loadPluginsAfterMutation(
            !installResult.restartRequired,
            plugin.id,
            (item) => item.registered || item.configured || item.enabled
          );
          notifyPluginResourcesChanged();
          if (installResult.restartRequired) {
            showNotification(t('plugin_management.reinstall_restart_required'), 'warning');
          }
          showNotification(t('plugin_management.reinstall_success'), 'success');
        } catch (err: unknown) {
          const sourceRequired = getErrorDetailCode(err) === 'plugin_store_source_required';
          showNotification(
            sourceRequired
              ? t('plugin_management.reinstall_source_required')
              : hasRestartRequired(err)
                ? t('plugin_management.reinstall_delete_restart_required')
                : `${t('plugin_management.reinstall_failed')}: ${getErrorMessage(
                    err,
                    t('plugin_management.reinstall_failed')
                  )}`,
            sourceRequired || hasRestartRequired(err) ? 'warning' : 'error'
          );
          throw err;
        } finally {
          setMutatingID('');
        }
      },
    });
  };

  const handleSaveConfig = async () => {
    if (!editingPlugin || !draft || openingConfigID || mutatingID) return;
    const { patch, errors } = buildPluginConfigPatch(draft, editingPlugin.configFields, t);

    if (Object.keys(errors).length > 0) {
      setDraft({ ...draft, errors });
      showNotification(t('plugin_management.validation_failed'), 'warning');
      return;
    }

    setMutatingID(editingPlugin.id);
    try {
      await pluginsApi.patchConfig(editingPlugin.id, patch);
      clearConfigCache();
      await loadPluginsAfterMutation(
        patch.enabled === true && editingPlugin.enabled !== true,
        editingPlugin.id,
        (item) => item.enabled === true
      );
      notifyPluginResourcesChanged();
      setEditingPlugin(null);
      setDraft(null);
      showNotification(t('plugin_management.save_success'), 'success');
    } catch (err: unknown) {
      showNotification(
        `${t('plugin_management.save_failed')}: ${getErrorMessage(
          err,
          t('plugin_management.save_failed')
        )}`,
        'error'
      );
    } finally {
      setMutatingID('');
    }
  };

  const handleFieldTextChange =
    (fieldName: string) => (event: ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) => {
      const value = event.target.value;
      updateDraft((current) => ({
        ...current,
        values: { ...current.values, [fieldName]: value },
        touched: new Set(current.touched).add(fieldName),
        errors: { ...current.errors, [fieldName]: '' },
      }));
    };

  const handleFieldBooleanChange = (fieldName: string, value: boolean) => {
    updateDraft((current) => ({
      ...current,
      values: { ...current.values, [fieldName]: value },
      touched: new Set(current.touched).add(fieldName),
      errors: { ...current.errors, [fieldName]: '' },
    }));
  };

  const handlePriorityChange = (event: ChangeEvent<HTMLInputElement>) => {
    const value = event.target.value;
    updateDraft((current) => ({
      ...current,
      priority: value,
      touched: new Set(current.touched).add('priority'),
      errors: { ...current.errors, priority: '' },
    }));
  };

  const renderFieldEditor = (field: PluginConfigField) => {
    if (!draft) return null;
    const fieldType = normalizeFieldType(field);
    const value = draft.values[field.name];
    const textValue = typeof value === 'string' ? value : '';
    const errorText = draft.errors[field.name];

    if (fieldType === 'boolean') {
      return (
        <div key={field.name} className={styles.fieldRow}>
          <div className={styles.fieldText}>
            <div className={styles.fieldLabel}>{field.name}</div>
            {field.description ? (
              <div className={styles.fieldDescription}>{field.description}</div>
            ) : null}
          </div>
          <ToggleSwitch
            checked={value === true}
            onChange={(nextValue) => handleFieldBooleanChange(field.name, nextValue)}
            ariaLabel={field.name}
          />
        </div>
      );
    }

    if (fieldType === 'enum' && field.enumValues.length > 0) {
      return (
        <div key={field.name} className={styles.formField}>
          <label htmlFor={`plugin-field-${field.name}`}>{field.name}</label>
          <Select
            id={`plugin-field-${field.name}`}
            value={textValue}
            options={field.enumValues.map((item) => ({ value: item, label: item }))}
            onChange={(nextValue) =>
              updateDraft((current) => ({
                ...current,
                values: { ...current.values, [field.name]: nextValue },
                touched: new Set(current.touched).add(field.name),
                errors: { ...current.errors, [field.name]: '' },
              }))
            }
            placeholder={t('plugin_management.select_placeholder')}
          />
          {field.description ? <div className={styles.fieldHint}>{field.description}</div> : null}
          {errorText ? <div className={styles.fieldError}>{errorText}</div> : null}
        </div>
      );
    }

    if (fieldType === 'array') {
      return (
        <div key={field.name} className={styles.formField}>
          <label htmlFor={`plugin-field-${field.name}`}>{field.name}</label>
          <textarea
            id={`plugin-field-${field.name}`}
            className={styles.textarea}
            value={textValue}
            onChange={handleFieldTextChange(field.name)}
            placeholder="[]"
            spellCheck={false}
          />
          {field.description ? <div className={styles.fieldHint}>{field.description}</div> : null}
          {errorText ? <div className={styles.fieldError}>{errorText}</div> : null}
        </div>
      );
    }

    if (fieldType === 'object') {
      return (
        <div key={field.name} className={styles.formField}>
          <label htmlFor={`plugin-field-${field.name}`}>{field.name}</label>
          <textarea
            id={`plugin-field-${field.name}`}
            className={styles.textarea}
            value={textValue}
            onChange={handleFieldTextChange(field.name)}
            placeholder="{}"
            spellCheck={false}
          />
          {field.description ? <div className={styles.fieldHint}>{field.description}</div> : null}
          {errorText ? <div className={styles.fieldError}>{errorText}</div> : null}
        </div>
      );
    }

    return (
      <Input
        key={field.name}
        id={`plugin-field-${field.name}`}
        label={field.name}
        value={textValue}
        onChange={handleFieldTextChange(field.name)}
        inputMode={fieldType === 'integer' || fieldType === 'number' ? 'decimal' : undefined}
        hint={field.description || undefined}
        error={errorText || undefined}
      />
    );
  };

  const savingConfig = Boolean(editingPlugin && mutatingID === editingPlugin.id);

  return (
    <div className={styles.page}>
      {error ? <div className={styles.errorBox}>{error}</div> : null}

      {data && !data.pluginsEnabled ? (
        <div className={styles.warningBox}>{t('plugin_management.global_disabled_hint')}</div>
      ) : null}

      <section className={styles.tabSurface}>
        <div className={styles.controlPanel}>
          <div className={styles.controlHeader}>
            <div className={styles.summaryTabs}>{tabsControl}</div>
            {data ? (
              <div className={styles.summaryMetrics}>
                <span
                  className={styles.summaryMetric}
                  data-tone={data.pluginsEnabled ? 'enabled' : 'disabled'}
                >
                  <span className={styles.summaryMetricLabel}>
                    {t('plugin_management.global_status')}
                  </span>
                  <strong>
                    {data.pluginsEnabled
                      ? t('plugin_management.global_enabled')
                      : t('plugin_management.global_disabled')}
                  </strong>
                </span>
                <span className={styles.summaryMetric}>
                  <span className={styles.summaryMetricLabel}>
                    {t('plugin_management.installed_count')}
                  </span>
                  <strong>{pluginStats.installed}</strong>
                </span>
                <span className={styles.summaryMetric} data-tone="active">
                  <span className={styles.summaryMetricLabel}>
                    {t('plugin_management.effective_count')}
                  </span>
                  <strong>{pluginStats.effective}</strong>
                </span>
                <span className={styles.summaryMetric}>
                  <span className={styles.summaryMetricLabel}>
                    {t('plugin_management.plugins_dir')}
                  </span>
                  <strong>{data.pluginsDir || 'plugins'}</strong>
                </span>
              </div>
            ) : null}
          </div>

          <div className={styles.controlToolbar}>
            <Input
              type="search"
              value={filter}
              onChange={(event) => setFilter(event.target.value)}
              placeholder={t('plugin_management.search_placeholder')}
              aria-label={t('plugin_management.search_label')}
              rightElement={<IconSearch size={16} />}
            />
            <div className={styles.toolbarActions}>
              <Button variant="secondary" size="sm" onClick={onOpenStore}>
                <IconPlus size={16} />
                {t('plugin_management.install_plugin')}
              </Button>
              <Button
                variant="secondary"
                size="sm"
                onClick={loadPlugins}
                disabled={!connected || !supportsPlugin || loading || Boolean(mutatingID)}
                loading={loading}
              >
                {loading ? null : <IconRefreshCw size={16} />}
                {t('plugin_management.refresh')}
              </Button>
            </div>
          </div>
        </div>
      </section>

      {loading ? (
        <div className={styles.list} aria-busy="true">
          {Array.from({ length: 4 }, (_, index) => (
            <div key={index} className={styles.skeletonRow} />
          ))}
        </div>
      ) : visiblePlugins.length === 0 ? (
        <section className={styles.emptySurface}>
          <EmptyState
            title={t('plugin_management.no_plugins')}
            description={t('plugin_management.no_plugins_desc')}
            action={
              <Button
                variant="secondary"
                size="sm"
                onClick={loadPlugins}
                disabled={!connected || !supportsPlugin}
              >
                <IconRefreshCw size={16} />
                {t('plugin_management.refresh')}
              </Button>
            }
          />
        </section>
      ) : (
        <div
          className={styles.list}
          role="table"
          aria-label={t('plugin_management.installed_table_aria_label')}
        >
          <div className={styles.tableHeader} role="row">
            <span role="columnheader">{t('plugin_management.plugin_col')}</span>
            <span role="columnheader">{t('plugin_management.status_col')}</span>
            <span role="columnheader">{t('plugin_management.capability_col')}</span>
            <span role="columnheader">{t('plugin_management.path_col')}</span>
            <span role="columnheader">{t('plugin_management.enabled_col')}</span>
            <span role="columnheader">{t('plugin_management.actions_col')}</span>
          </div>
          {visiblePlugins.map((plugin) => {
            const display = buildPluginDisplay(plugin, {
              pluginsEnabled: data?.pluginsEnabled ?? true,
            });
            const logo = resolvePluginAsset(plugin.logo || plugin.metadata?.logo || '');
            const repositoryURL = buildRepositoryURL(plugin.metadata?.githubRepository ?? '');
            const openingConfig = openingConfigID === plugin.id;
            const actionBusy = Boolean(mutatingID || openingConfigID);
            const subtitle = display.subtitleParts.join(' / ');
            const menuItems: DropdownMenuItem[] = [
              ...(repositoryURL
                ? [
                    {
                      key: 'repository',
                      label: t('plugin_management.open_repository'),
                      icon: <IconGithub size={14} />,
                      onClick: () => openExternalURL(repositoryURL),
                    },
                  ]
                : []),
              ...(plugin.supportsOAuth
                ? [
                    {
                      key: 'oauth',
                      label: t('plugin_management.oauth_login'),
                      icon: <IconKey size={14} />,
                      disabled: !connected || actionBusy,
                      onClick: () => navigate('/oauth'),
                    },
                  ]
                : []),
              {
                key: 'reinstall',
                label: t('plugin_management.reinstall_plugin'),
                icon: <IconRefreshCw size={14} />,
                disabled: !connected || actionBusy,
                onClick: () => handleReinstallPlugin(plugin),
              },
              {
                key: 'delete',
                label: t('plugin_management.delete_plugin'),
                icon: <IconTrash2 size={14} />,
                disabled: !connected || actionBusy,
                tone: 'danger',
                onClick: () => handleDeletePlugin(plugin),
              },
            ];

            return (
              <div key={plugin.id} className={styles.row} role="row">
                <div className={`${styles.cell} ${styles.pluginCell}`} role="cell">
                  <div className={styles.logoBox} aria-hidden="true">
                    <PluginLogo src={logo} />
                  </div>
                  <div className={styles.pluginIdentity}>
                    <h2 title={display.title}>{display.title}</h2>
                    <div className={styles.pluginSubtitle} title={subtitle || plugin.id}>
                      {subtitle || plugin.id}
                    </div>
                  </div>
                </div>

                <div className={`${styles.cell} ${styles.statusCell}`} role="cell">
                  <span className={styles.cellCaption}>{t('plugin_management.status_col')}</span>
                  <span className={getStatusBadgeClassName(display.status.tone)}>
                    {t(display.status.labelKey)}
                  </span>
                  <div className={styles.statusDetails}>
                    <span data-tone={plugin.registered ? 'muted' : 'warning'}>
                      {plugin.registered
                        ? t('plugin_management.registered')
                        : t('plugin_management.not_registered')}
                    </span>
                    <span data-tone={plugin.configured ? 'muted' : 'warning'}>
                      {plugin.configured
                        ? t('plugin_management.configured')
                        : t('plugin_management.not_configured')}
                    </span>
                  </div>
                </div>

                <div className={`${styles.cell} ${styles.capabilityCell}`} role="cell">
                  <span className={styles.cellCaption}>
                    {t('plugin_management.capability_col')}
                  </span>
                  <div className={styles.capabilityLine}>
                    <span className={styles.capabilityBadge}>
                      {display.menuCount > 0
                        ? t('plugin_management.menu_count', { count: display.menuCount })
                        : t('plugin_management.no_menu_entry')}
                    </span>
                    {plugin.supportsOAuth ? (
                      <span className={styles.inlineBadge}>{t('plugin_management.oauth')}</span>
                    ) : null}
                  </div>
                  {display.primaryMenuLabel ? (
                    <div className={styles.capabilityTitle} title={display.primaryMenuLabel}>
                      {display.primaryMenuLabel}
                    </div>
                  ) : null}
                  {display.primaryMenuDescription ? (
                    <div
                      className={styles.capabilityDescription}
                      title={display.primaryMenuDescription}
                    >
                      {display.primaryMenuDescription}
                    </div>
                  ) : null}
                  {display.configFieldCount > 0 ? (
                    <div className={styles.capabilityDescription}>
                      {t('plugin_management.config_field_count', {
                        count: display.configFieldCount,
                      })}
                    </div>
                  ) : null}
                </div>

                <div className={`${styles.cell} ${styles.pathCell}`} role="cell">
                  <span className={styles.cellCaption}>{t('plugin_management.path_col')}</span>
                  <span className={styles.pathText} title={plugin.path || undefined}>
                    {plugin.path || '-'}
                  </span>
                </div>

                <div className={`${styles.cell} ${styles.toggleCell}`} role="cell">
                  <span className={styles.cellCaption}>{t('plugin_management.enabled_col')}</span>
                  <ToggleSwitch
                    checked={plugin.enabled}
                    onChange={(enabled) => handleTogglePlugin(plugin, enabled)}
                    disabled={!connected || actionBusy}
                    ariaLabel={t('plugin_management.enabled_for', { name: display.title })}
                  />
                </div>

                <div className={`${styles.cell} ${styles.actions}`} role="cell">
                  <Button
                    variant="secondary"
                    size="sm"
                    onClick={() => openConfigDrawer(plugin)}
                    disabled={!connected || actionBusy}
                    loading={openingConfig}
                    title={t('plugin_management.edit_config')}
                    aria-label={t('plugin_management.edit_config')}
                  >
                    <IconSettings size={14} />
                    {t('plugin_management.edit_config')}
                  </Button>
                  <DropdownMenu
                    items={menuItems}
                    ariaLabel={t('plugin_management.more_actions_for', { name: display.title })}
                    triggerIcon={<IconMoreVertical size={16} />}
                    triggerClassName={styles.moreButton}
                    disabled={actionBusy}
                  />
                </div>
              </div>
            );
          })}
        </div>
      )}

      <Drawer
        open={Boolean(editingPlugin && draft)}
        onClose={closeConfigDrawer}
        width={560}
        title={
          editingPlugin
            ? t('plugin_management.config_title', { name: getPluginTitle(editingPlugin) })
            : t('plugin_management.edit_config')
        }
        footer={
          <div className={styles.drawerFooter}>
            <Button variant="secondary" onClick={closeConfigDrawer} disabled={savingConfig}>
              {t('common.cancel')}
            </Button>
            <Button onClick={handleSaveConfig} loading={savingConfig}>
              {t('common.save')}
            </Button>
          </div>
        }
      >
        {draft && editingPlugin ? (
          <div className={styles.form}>
            <section className={styles.formSection}>
              <h3>{t('plugin_management.base_settings')}</h3>
              <div className={styles.fieldRow}>
                <div className={styles.fieldText}>
                  <div className={styles.fieldLabel}>{t('plugin_management.enabled')}</div>
                  <div className={styles.fieldDescription}>
                    {t('plugin_management.enabled_hint')}
                  </div>
                </div>
                <ToggleSwitch
                  checked={draft.enabled}
                  onChange={(enabled) =>
                    updateDraft((current) => ({
                      ...current,
                      enabled,
                      touched: new Set(current.touched).add('enabled'),
                    }))
                  }
                  ariaLabel={t('plugin_management.enabled')}
                />
              </div>
              <Input
                label={t('plugin_management.priority')}
                value={draft.priority}
                onChange={handlePriorityChange}
                inputMode="numeric"
                error={draft.errors.priority || undefined}
              />
            </section>

            <section className={styles.formSection}>
              <h3>{t('plugin_management.config_fields')}</h3>
              {editingPlugin.configFields.length > 0 ? (
                editingPlugin.configFields.map((field) => renderFieldEditor(field))
              ) : (
                <div className={styles.emptyConfig}>{t('plugin_management.no_config_fields')}</div>
              )}
            </section>
          </div>
        ) : null}
      </Drawer>
    </div>
  );
}
