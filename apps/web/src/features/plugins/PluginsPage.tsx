import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useNavigate } from 'react-router-dom';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { EmptyState } from '@/components/ui/EmptyState';
import { Input } from '@/components/ui/Input';
import { LoadingSpinner } from '@/components/ui/LoadingSpinner';
import { Modal } from '@/components/ui/Modal';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { IconExternalLink, IconSearch } from '@/components/ui/icons';
import { useNotificationStore } from '@/stores';
import { sleep } from '@/utils/helpers';
import {
  detectPluginSupport,
  isPluginUnsupportedError,
  isRestartRequiredError,
  pluginApi,
  type PluginConfig,
  type PluginListItem,
  type PluginsListResponse
} from '@/services/api/plugins';
import { PluginConfigModal } from './PluginConfigModal';
import styles from './plugins.module.scss';

const isRecord = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === 'object';

const getErrorMessage = (error: unknown): string => {
  if (error instanceof Error) return error.message;
  if (isRecord(error) && typeof error.message === 'string') return error.message;
  return typeof error === 'string' ? error : '';
};

const getPluginDisplayName = (plugin: PluginListItem): string =>
  plugin.metadata?.name || plugin.id;

const PLUGIN_TOGGLE_SETTLE_TIMEOUT_MS = 5000;
const PLUGIN_TOGGLE_SETTLE_INTERVAL_MS = 300;

const matchesPluginSearch = (plugin: PluginListItem, query: string): boolean => {
  if (!query) return true;
  const haystack = [
    plugin.id,
    plugin.metadata?.name,
    plugin.metadata?.author,
    plugin.metadata?.version
  ]
    .filter(Boolean)
    .join(' ')
    .toLowerCase();
  return haystack.includes(query.toLowerCase());
};

export function PluginsPage() {
  const { t } = useTranslation();
  const { showNotification } = useNotificationStore();
  const navigate = useNavigate();

  const [pluginsEnabled, setPluginsEnabled] = useState(true);
  const [plugins, setPlugins] = useState<PluginListItem[]>([]);
  const [pluginsDir, setPluginsDir] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [search, setSearch] = useState('');
  const [supported, setSupported] = useState(true);

  // 编辑配置
  const [editingPlugin, setEditingPlugin] = useState<PluginListItem | null>(null);
  const [editingConfig, setEditingConfig] = useState<PluginConfig | null>(null);
  const [configLoading, setConfigLoading] = useState(false);
  const [configSaving, setConfigSaving] = useState(false);
  const [configError, setConfigError] = useState('');

  // 启停 in-flight
  const [togglingId, setTogglingId] = useState<string | null>(null);

  // 删除确认
  const [deletingPlugin, setDeletingPlugin] = useState<PluginListItem | null>(null);
  const [deleteSaving, setDeleteSaving] = useState(false);
  const [deleteRestartRequired, setDeleteRestartRequired] = useState(false);

  const applyPluginsResponse = useCallback((data: PluginsListResponse, options?: { silent?: boolean }) => {
    setPluginsEnabled(data.plugins_enabled);
    setPluginsDir(data.plugins_dir || '');
    setPlugins((prev) => {
      if (!options?.silent) return data.plugins;
      // 静默刷新（如 toggle 后）：服务端可能在热重载中途返回瞬时态，
      // 此时 registered 会短暂为 false，且 metadata/logo/config_fields/menus
      // 等「仅已注册时有值」的字段会一并丢失。对原本已注册的插件，
      // 保留其完整旧数据，仅合并服务端返回的动态状态字段。
      const prevMap = new Map(prev.map((p) => [p.id, p]));
      return data.plugins.map((p) => {
        const old = prevMap.get(p.id);
        if (p.enabled && old && old.registered && !p.registered) {
          return {
            ...old,
            configured: p.configured,
            enabled: p.enabled,
            effective_enabled: p.enabled
          };
        }
        return p;
      });
    });
  }, []);

  const loadPlugins = useCallback(
    async (options?: { silent?: boolean }) => {
      if (!options?.silent) {
        setLoading(true);
        setError('');
      }
      try {
        const data = await pluginApi.list();
        applyPluginsResponse(data, options);
      } catch (err: unknown) {
        if (isPluginUnsupportedError(err)) {
          setSupported(false);
          setPlugins([]);
        } else if (!options?.silent) {
          setError(getErrorMessage(err) || t('plugins.error.load_failed'));
        }
      } finally {
        if (!options?.silent) {
          setLoading(false);
        }
      }
    },
    [applyPluginsResponse, t]
  );

  const waitForPluginToggleSettled = useCallback(
    async (pluginId: string, nextEnabled: boolean) => {
      const deadline = Date.now() + PLUGIN_TOGGLE_SETTLE_TIMEOUT_MS;
      let latest: PluginsListResponse | null = null;

      while (Date.now() < deadline) {
        const data = await pluginApi.list();
        latest = data;
        applyPluginsResponse(data, { silent: true });

        const item = data.plugins.find((p) => p.id === pluginId);
        if (item) {
          const settled = nextEnabled
            ? item.enabled && (!data.plugins_enabled || (item.registered && item.effective_enabled))
            : !item.enabled && !item.registered && !item.effective_enabled;
          if (settled) return;
        }

        await sleep(PLUGIN_TOGGLE_SETTLE_INTERVAL_MS);
      }

      if (latest) {
        applyPluginsResponse(latest, { silent: true });
      } else {
        await loadPlugins({ silent: true });
      }
    },
    [applyPluginsResponse, loadPlugins]
  );

  // 进入页面：先做能力探测，再加载列表
  useEffect(() => {
    let cancelled = false;
    (async () => {
      const ok = await detectPluginSupport();
      if (cancelled) return;
      setSupported(ok);
      if (ok) {
        await loadPlugins();
      } else {
        setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
    // 仅首屏探测一次；loadPlugins 在操作完成后单独触发
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const handleRefresh = useCallback(() => {
    if (!supported) {
      return;
    }
    loadPlugins();
  }, [loadPlugins, supported]);

  // 编辑配置：先拉取完整原始配置
  const handleOpenConfig = useCallback(
    async (plugin: PluginListItem) => {
      setEditingPlugin(plugin);
      setEditingConfig(null);
      setConfigError('');
      setConfigLoading(true);
      try {
        const config = await pluginApi.getConfig(plugin.id);
        setEditingConfig(config);
      } catch (err: unknown) {
        setConfigError(getErrorMessage(err) || t('plugins.error.config_load_failed'));
        setEditingConfig({});
      } finally {
        setConfigLoading(false);
      }
    },
    [t]
  );

  const handleCloseConfig = useCallback(() => {
    setEditingPlugin(null);
    setEditingConfig(null);
    setConfigError('');
    setConfigLoading(false);
    setConfigSaving(false);
  }, []);

  const handleSaveConfig = useCallback(
    async (config: PluginConfig) => {
      const id = editingPlugin?.id;
      if (!id) return;
      setConfigSaving(true);
      setConfigError('');
      try {
        await pluginApi.putConfig(id, config);
        showNotification(t('plugins.config.saved'), 'success');
        handleCloseConfig();
        await loadPlugins();
      } catch (err: unknown) {
        const message = getErrorMessage(err);
        setConfigError(message || t('plugins.error.config_save_failed'));
        showNotification(
          `${t('plugins.error.config_save_failed')}${message ? `: ${message}` : ''}`,
          'error'
        );
      } finally {
        setConfigSaving(false);
      }
    },
    [editingPlugin, handleCloseConfig, loadPlugins, showNotification, t]
  );

  const handleToggleEnabled = useCallback(
    async (plugin: PluginListItem, nextEnabled: boolean) => {
      setTogglingId(plugin.id);
      // 乐观更新：仅翻转 enabled 与 effective_enabled，保留 registered 等字段。
      // 注意：不在乐观阶段推断 registered——那是服务端加载态，前端无法准确预判。
      // 真实状态以随后的静默刷新为准（见下方 loadPlugins({ silent: true })）。
      setPlugins((prev) =>
        prev.map((p) =>
          p.id === plugin.id
            ? { ...p, enabled: nextEnabled, effective_enabled: nextEnabled && p.registered }
            : p
        )
      );
      try {
        await pluginApi.setEnabled(plugin.id, nextEnabled);
        await waitForPluginToggleSettled(plugin.id, nextEnabled);
        showNotification(t('plugins.status_updated'), 'success');
      } catch (err: unknown) {
        const message = getErrorMessage(err);
        // 失败：回滚乐观更新
        setPlugins((prev) =>
          prev.map((p) =>
            p.id === plugin.id
              ? {
                  ...p,
                  enabled: !nextEnabled,
                  effective_enabled: !nextEnabled && p.registered
                }
              : p
          )
        );
        showNotification(
          `${t('plugins.error.toggle_failed')}${message ? `: ${message}` : ''}`,
          'error'
        );
      } finally {
        setTogglingId(null);
      }
    },
    [showNotification, t, waitForPluginToggleSettled]
  );

  const handleRequestDelete = useCallback((plugin: PluginListItem) => {
    setDeletingPlugin(plugin);
    setDeleteRestartRequired(false);
  }, []);

  const handleCancelDelete = useCallback(() => {
    setDeletingPlugin(null);
    setDeleteSaving(false);
    setDeleteRestartRequired(false);
  }, []);

  const handleConfirmDelete = useCallback(async () => {
    const id = deletingPlugin?.id;
    if (!id) return;
    setDeleteSaving(true);
    try {
      await pluginApi.remove(id);
      showNotification(
        t('plugins.deleted', { name: deletingPlugin ? getPluginDisplayName(deletingPlugin) : id }),
        'success'
      );
      handleCancelDelete();
      await loadPlugins();
    } catch (err: unknown) {
      if (isRestartRequiredError(err)) {
        setDeleteRestartRequired(true);
        showNotification(t('plugins.error.restart_required'), 'warning');
      } else {
        const message = getErrorMessage(err);
        showNotification(
          `${t('plugins.error.delete_failed')}${message ? `: ${message}` : ''}`,
          'error'
        );
      }
    } finally {
      setDeleteSaving(false);
    }
  }, [deletingPlugin, handleCancelDelete, loadPlugins, showNotification, t]);

  const sortedPlugins = useMemo(
    () => [...plugins].sort((a, b) => a.id.localeCompare(b.id)),
    [plugins]
  );

  const filteredPlugins = useMemo(
    () => sortedPlugins.filter((plugin) => matchesPluginSearch(plugin, search)),
    [sortedPlugins, search]
  );

  // 生效插件：registered && effective_enabled
  const effectiveCount = useMemo(
    () => plugins.filter((p) => p.registered && p.effective_enabled).length,
    [plugins]
  );

  if (loading) {
    return (
      <div className={styles.page}>
        <div className={styles.centerCell}>
          <LoadingSpinner size={28} />
          <span>{t('common.loading')}</span>
        </div>
      </div>
    );
  }

  if (!supported) {
    return (
      <div className={styles.page}>
        <div className={`${styles.banner} ${styles.bannerWarning}`}>
          <span className={styles.bannerIcon}>⚠️</span>
          <span>{t('plugins.unsupported')}</span>
        </div>
      </div>
    );
  }

  return (
    <div className={styles.page}>
      <div className={styles.toolbar}>
        <Button variant="secondary" size="sm" onClick={() => navigate('/plugins-store')}>
          <IconExternalLink size={14} />
          <span>{t('plugins.go_to_store')}</span>
        </Button>
        <Button variant="secondary" size="sm" onClick={handleRefresh}>
          {t('common.refresh')}
        </Button>
      </div>

      {!pluginsEnabled && (
        <div className={`${styles.banner} ${styles.bannerWarning}`}>
          <span className={styles.bannerIcon}>⚠️</span>
          <span>{t('plugins.global_disabled_notice')}</span>
        </div>
      )}

      <div className={styles.statsBar}>
        <div className={styles.statItem}>
          <span className={styles.statLabel}>{t('plugins.global_status')}</span>
          <span
            className={`${styles.statValue} ${
              pluginsEnabled ? styles.statValueSuccess : styles.statValueMuted
            }`}
          >
            {pluginsEnabled ? t('common.enabled') : t('common.disabled')}
          </span>
        </div>
        <div className={styles.statItem}>
          <span className={styles.statLabel}>{t('plugins.plugins_dir')}</span>
          <span className={styles.statValue} title={pluginsDir || undefined}>
            {pluginsDir || t('plugins.plugins_dir_unknown')}
          </span>
        </div>
        <div className={styles.statItem}>
          <span className={styles.statLabel}>{t('plugins.discovered_count')}</span>
          <span className={styles.statValue}>{plugins.length}</span>
        </div>
        <div className={styles.statItem}>
          <span className={styles.statLabel}>{t('plugins.effective_count')}</span>
          <span className={styles.statValue}>
            {t('plugins.effective_count_value', {
              effective: effectiveCount,
              total: plugins.length
            })}
          </span>
        </div>
      </div>

      {error && <div className={styles.errorBox}>{error}</div>}

      {sortedPlugins.length > 0 && (
        <div className={styles.filterBar}>
          <div className={styles.searchInput}>
            <Input
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder={t('plugins.search_placeholder')}
              rightElement={<IconSearch size={16} />}
              aria-label={t('plugins.search_label')}
            />
          </div>
        </div>
      )}

      {sortedPlugins.length === 0 ? (
        <EmptyState
          title={t('plugins.empty_title')}
          description={t('plugins.empty_desc')}
        />
      ) : filteredPlugins.length === 0 ? (
        <EmptyState
          title={t('plugins.no_match_title')}
          description={t('plugins.no_match_desc', { defaultValue: '' })}
        />
      ) : (
        <div className={styles.grid}>
          {filteredPlugins.map((plugin) => (
            <PluginCard
              key={plugin.id}
              plugin={plugin}
              pluginsEnabled={pluginsEnabled}
              toggling={togglingId === plugin.id}
              onToggle={(next) => handleToggleEnabled(plugin, next)}
              onEdit={() => handleOpenConfig(plugin)}
              onDelete={() => handleRequestDelete(plugin)}
            />
          ))}
        </div>
      )}

      {editingPlugin && (
        <PluginConfigModal
          open={Boolean(editingPlugin)}
          pluginId={editingPlugin.id}
          pluginName={getPluginDisplayName(editingPlugin)}
          config={editingConfig ?? {}}
          fields={
            editingPlugin.metadata?.config_fields ?? editingPlugin.config_fields
          }
          saving={configSaving}
          error={configError}
          onClose={handleCloseConfig}
          onSave={handleSaveConfig}
        />
      )}
      {configLoading && (
        <div className={styles.centerCell}>
          <LoadingSpinner size={20} />
          <span>{t('plugins.config.loading')}</span>
        </div>
      )}

      {deletingPlugin && (
        <DeleteConfirmDialog
          plugin={deletingPlugin}
          saving={deleteSaving}
          restartRequired={deleteRestartRequired}
          onCancel={handleCancelDelete}
          onConfirm={handleConfirmDelete}
        />
      )}
    </div>
  );
}

interface PluginCardProps {
  plugin: PluginListItem;
  pluginsEnabled: boolean;
  toggling: boolean;
  onToggle: (next: boolean) => void;
  onEdit: () => void;
  onDelete: () => void;
}

function PluginCard({
  plugin,
  pluginsEnabled,
  toggling,
  onToggle,
  onEdit,
  onDelete
}: PluginCardProps) {
  const { t } = useTranslation();
  const name = getPluginDisplayName(plugin);
  const version = plugin.metadata?.version;
  const author = plugin.metadata?.author;
  const repo = plugin.metadata?.github_repository;
  const menus = plugin.menus ?? [];
  const supportsOauth = Boolean(plugin.supports_oauth);
  const globallyDisabled = !pluginsEnabled;
  const notEffective = !plugin.effective_enabled;

  return (
    <Card
      title={
        <span className={styles.cardTitle}>
          <PluginLogo plugin={plugin} />
          <span style={{ display: 'flex', flexDirection: 'column', minWidth: 0 }}>
            <span className={styles.cardName}>{name}</span>
            <span className={styles.cardId}>{plugin.id}</span>
          </span>
        </span>
      }
      extra={
        <ToggleSwitch
          checked={plugin.enabled}
          onChange={onToggle}
          disabled={toggling}
          ariaLabel={t('plugins.toggle_aria', { name })}
        />
      }
    >
      <div className={styles.badges}>
        {plugin.registered ? (
          <span className={`${styles.badge} ${styles.badgeSuccess}`}>
            {t('plugins.status_registered')}
          </span>
        ) : (
          <span className={`${styles.badge} ${styles.badgeMuted}`}>
            {t('plugins.status_not_registered')}
          </span>
        )}
        {plugin.configured && (
          <span className={`${styles.badge} ${styles.badgeInfo}`}>
            {t('plugins.status_configured')}
          </span>
        )}
        {supportsOauth && (
          <span className={`${styles.badge} ${styles.badgeInfo}`}>
            {t('plugins.status_oauth')}
          </span>
        )}
        {plugin.enabled ? (
          <span className={`${styles.badge} ${styles.badgeSuccess}`}>
            {t('common.enabled')}
          </span>
        ) : (
          <span className={`${styles.badge} ${styles.badgeMuted}`}>
            {t('common.disabled')}
          </span>
        )}
      </div>

      {(version || author || repo) && (
        <div className={styles.versionRow} style={{ marginTop: 8 }}>
          {version && (
            <span>
              <span className={styles.versionLabel}>{t('plugins.version')}: </span>
              <span className={styles.versionValue}>{version}</span>
            </span>
          )}
          {author && (
            <span>
              <span className={styles.versionLabel}>{t('plugins.author')}: </span>
              <span className={styles.versionValue}>{author}</span>
            </span>
          )}
        </div>
      )}

      {notEffective && (
        <div className={styles.disabledNotice} style={{ marginTop: 8 }}>
          {globallyDisabled
            ? t('plugins.not_effective_global')
            : !plugin.registered
              ? t('plugins.not_effective_not_registered')
              : t('plugins.not_effective_disabled')}
        </div>
      )}

      {repo && (
        <div className={styles.metaList} style={{ marginTop: 6 }}>
          <a
            className={styles.metaLink}
            href={repo}
            target="_blank"
            rel="noopener noreferrer"
          >
            {repo}
          </a>
        </div>
      )}

      {menus.length > 0 && (
        <div className={styles.menuLinks} style={{ marginTop: 10 }}>
          {menus.map((menu) => (
            <a
              key={menu.path}
              className={styles.menuLink}
              href={menu.path}
              target="_blank"
              rel="noopener noreferrer"
              title={menu.description}
            >
              {menu.menu}
            </a>
          ))}
        </div>
      )}

      <div className={styles.actions}>
        <Button variant="secondary" size="sm" onClick={onEdit}>
          {t('plugins.edit_config')}
        </Button>
        <Button variant="danger" size="sm" onClick={onDelete}>
          {t('common.delete')}
        </Button>
      </div>
    </Card>
  );
}

function PluginLogo({ plugin }: { plugin: PluginListItem }) {
  const logo = plugin.logo ?? plugin.metadata?.logo;
  const fallback = (plugin.metadata?.name || plugin.id).charAt(0).toUpperCase();
  if (logo) {
    return (
      <img
        className={styles.cardLogo}
        src={logo}
        alt=""
        onError={(e) => {
          (e.currentTarget as HTMLImageElement).style.display = 'none';
        }}
      />
    );
  }
  return <span className={styles.cardLogoFallback}>{fallback}</span>;
}

interface DeleteConfirmDialogProps {
  plugin: PluginListItem;
  saving: boolean;
  restartRequired: boolean;
  onCancel: () => void;
  onConfirm: () => void;
}

function DeleteConfirmDialog({
  plugin,
  saving,
  restartRequired,
  onCancel,
  onConfirm
}: DeleteConfirmDialogProps) {
  const { t } = useTranslation();
  return (
    <Modal
      open
      title={t('plugins.delete_title')}
      onClose={onCancel}
      width={440}
      closeDisabled={saving}
      footer={
        restartRequired ? (
          <Button variant="secondary" onClick={onCancel}>
            {t('common.close')}
          </Button>
        ) : (
          <>
            <Button variant="secondary" onClick={onCancel} disabled={saving}>
              {t('common.cancel')}
            </Button>
            <Button variant="danger" onClick={onConfirm} loading={saving}>
              {t('common.delete')}
            </Button>
          </>
        )
      }
    >
      {restartRequired ? (
        <div className={styles.disabledNotice}>
          {t('plugins.delete_restart_required')}
        </div>
      ) : (
        <p style={{ margin: 0, color: 'var(--text-primary)', fontSize: 14, lineHeight: 1.6 }}>
          {t('plugins.delete_confirm', {
            name: getPluginDisplayName(plugin),
            id: plugin.id
          })}
        </p>
      )}
    </Modal>
  );
}
