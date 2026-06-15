import { useCallback, useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Card } from '@/components/ui/Card';
import { EmptyState } from '@/components/ui/EmptyState';
import { Input } from '@/components/ui/Input';
import { LoadingSpinner } from '@/components/ui/LoadingSpinner';
import { SegmentedTabs } from '@/components/ui/SegmentedTabs';
import { IconSearch } from '@/components/ui/icons';
import { useNotificationStore } from '@/stores';
import {
  isPluginUnsupportedError,
  isRestartRequiredError,
  pluginApi,
  type PluginStoreItem
} from '@/services/api/plugins';
import styles from './plugins.module.scss';

const isRecord = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === 'object';

const getErrorMessage = (error: unknown): string => {
  // 优先取响应体里的人类可读 message（ApiError.details.message），
  // 避免把 error code（如 plugin_install_failed）当作提示文案。
  if (isRecord(error)) {
    const details = isRecord(error.details) ? error.details : null;
    const detailsMessage = details?.message;
    if (typeof detailsMessage === 'string' && detailsMessage.trim()) {
      return detailsMessage;
    }
    if (typeof error.message === 'string' && error.message.trim()) {
      return error.message;
    }
  }
  if (error instanceof Error) return error.message;
  return typeof error === 'string' ? error : '';
};

type StoreFilter = 'all' | 'installed' | 'not_installed' | 'updates';

const matchesStoreSearch = (item: PluginStoreItem, query: string): boolean => {
  if (!query) return true;
  const haystack = [item.id, item.name, item.author, item.description, ...(item.tags ?? [])]
    .filter(Boolean)
    .join(' ')
    .toLowerCase();
  return haystack.includes(query.toLowerCase());
};

export function PluginStorePage() {
  const { t } = useTranslation();
  const { showNotification } = useNotificationStore();

  const [items, setItems] = useState<PluginStoreItem[]>([]);
  const [pluginsEnabled, setPluginsEnabled] = useState(true);
  const [pluginsDir, setPluginsDir] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [installingId, setInstallingId] = useState<string | null>(null);
  const [search, setSearch] = useState('');
  const [filter, setFilter] = useState<StoreFilter>('all');

  const loadStore = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const data = await pluginApi.listStore();
      setItems(data.plugins);
      setPluginsEnabled(data.plugins_enabled);
      setPluginsDir(data.plugins_dir || '');
    } catch (err: unknown) {
      if (isPluginUnsupportedError(err)) {
        setError(t('plugins.unsupported'));
      } else {
        setError(getErrorMessage(err) || t('plugins.error.store_load_failed'));
      }
    } finally {
      setLoading(false);
    }
  }, [t]);

  useEffect(() => {
    // 首屏加载插件商店；loadStore 内部在异步回调中 setState。
    // eslint-disable-next-line react-hooks/set-state-in-effect
    loadStore();
    // 仅在首屏加载一次；语言切换（t 变化）不需要重新拉取。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const handleInstall = useCallback(
    async (item: PluginStoreItem) => {
      setInstallingId(item.id);
      try {
        const result = await pluginApi.install(item.id);
        showNotification(
          t('plugins.store.installed', {
            name: item.name || item.id,
            version: result.version || item.version || ''
          }),
          'success'
        );
        await loadStore();
      } catch (err: unknown) {
        if (isRestartRequiredError(err)) {
          showNotification(t('plugins.error.restart_required_update'), 'warning');
        } else {
          const message = getErrorMessage(err);
          showNotification(
            `${t('plugins.error.install_failed')}${message ? `: ${message}` : ''}`,
            'error'
          );
        }
      } finally {
        setInstallingId(null);
      }
    },
    [loadStore, showNotification, t]
  );

  const sortedItems = useMemo(
    () =>
      [...items].sort((a, b) => {
        // 未安装排在已安装之后；组内按名称
        if (a.installed !== b.installed) return a.installed ? 1 : -1;
        return (a.name || a.id).localeCompare(b.name || b.id);
      }),
    [items]
  );

  const counts = useMemo(
    () => ({
      all: items.length,
      installed: items.filter((i) => i.installed).length,
      not_installed: items.filter((i) => !i.installed).length,
      updates: items.filter((i) => i.update_available).length
    }),
    [items]
  );

  const filteredItems = useMemo(() => {
    return sortedItems.filter((item) => {
      if (!matchesStoreSearch(item, search)) return false;
      switch (filter) {
        case 'installed':
          return item.installed;
        case 'not_installed':
          return !item.installed;
        case 'updates':
          return item.update_available;
        default:
          return true;
      }
    });
  }, [sortedItems, search, filter]);

  const filterTabs = useMemo(
    () =>
      [
        { id: 'all' as const, label: `${t('plugins.store.filter_all')} (${counts.all})` },
        {
          id: 'installed' as const,
          label: `${t('plugins.store.filter_installed')} (${counts.installed})`
        },
        {
          id: 'not_installed' as const,
          label: `${t('plugins.store.filter_not_installed')} (${counts.not_installed})`
        },
        {
          id: 'updates' as const,
          label: `${t('plugins.store.filter_updates')} (${counts.updates})`
        }
      ],
    [t, counts]
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

  return (
    <div className={styles.page}>
      <div className={styles.toolbar}>
        <Button variant="secondary" size="sm" onClick={loadStore}>
          {t('common.refresh')}
        </Button>
      </div>

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
          <span className={styles.statLabel}>{t('plugins.available_count')}</span>
          <span className={styles.statValue}>{items.length}</span>
        </div>
      </div>

      {error && <div className={styles.errorBox}>{error}</div>}

      {items.length > 0 && (
        <div className={styles.filterBar}>
          <SegmentedTabs
            items={filterTabs}
            activeTab={filter}
            onChange={(tab) => setFilter(tab)}
            ariaLabel={t('plugins.store.filter_aria')}
            equalWidth
          />
          <div className={styles.searchInput}>
            <Input
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder={t('plugins.store.search_placeholder')}
              rightElement={<IconSearch size={16} />}
              aria-label={t('plugins.store.search_label')}
            />
          </div>
        </div>
      )}

      {sortedItems.length === 0 ? (
        <EmptyState
          title={t('plugins.store.empty_title')}
          description={t('plugins.store.empty_desc')}
        />
      ) : filteredItems.length === 0 ? (
        <EmptyState
          title={t('plugins.store.no_match_title')}
          description={t('plugins.store.no_match_desc')}
        />
      ) : (
        <div className={styles.grid}>
          {filteredItems.map((item) => (
            <StoreItemCard
              key={item.id}
              item={item}
              installing={installingId === item.id}
              onInstall={() => handleInstall(item)}
            />
          ))}
        </div>
      )}
    </div>
  );
}

interface StoreItemCardProps {
  item: PluginStoreItem;
  installing: boolean;
  onInstall: () => void;
}

function StoreItemCard({ item, installing, onInstall }: StoreItemCardProps) {
  const { t } = useTranslation();
  const name = item.name || item.id;
  const hasUpdate = Boolean(item.update_available);
  const isInstalled = item.installed;
  const canManage = isInstalled && item.configured;
  const fallback = name.charAt(0).toUpperCase();
  const tags = item.tags ?? [];

  return (
    <Card
      title={
        <span className={styles.cardTitle}>
          {item.logo ? (
            <img
              className={styles.cardLogo}
              src={item.logo}
              alt=""
              onError={(e) => {
                (e.currentTarget as HTMLImageElement).style.display = 'none';
              }}
            />
          ) : (
            <span className={styles.cardLogoFallback}>{fallback}</span>
          )}
          <span style={{ display: 'flex', flexDirection: 'column', minWidth: 0 }}>
            <span className={styles.cardName}>{name}</span>
            <span className={styles.cardId}>{item.id}</span>
          </span>
        </span>
      }
    >
      <div className={styles.badges}>
        {isInstalled ? (
          <span className={`${styles.badge} ${styles.badgeSuccess}`}>
            {t('plugins.store.installed')}
          </span>
        ) : (
          <span className={`${styles.badge} ${styles.badgeMuted}`}>
            {t('plugins.store.not_installed')}
          </span>
        )}
        {hasUpdate && (
          <span className={`${styles.badge} ${styles.badgeWarning}`}>
            {t('plugins.store.update_available')}
          </span>
        )}
        {item.effective_enabled && (
          <span className={`${styles.badge} ${styles.badgeInfo}`}>
            {t('plugins.status_active')}
          </span>
        )}
      </div>

      {(item.version || item.installed_version || item.author) && (
        <div className={styles.versionRow} style={{ marginTop: 8 }}>
          {item.version && (
            <span>
              <span className={styles.versionLabel}>{t('plugins.store.latest')}: </span>
              <span className={styles.versionValue}>{item.version}</span>
            </span>
          )}
          {item.installed_version && (
            <span>
              <span className={styles.versionLabel}>{t('plugins.store.current')}: </span>
              <span className={styles.versionValue}>{item.installed_version}</span>
            </span>
          )}
          {item.author && (
            <span>
              <span className={styles.versionLabel}>{t('plugins.author')}: </span>
              <span className={styles.versionValue}>{item.author}</span>
            </span>
          )}
        </div>
      )}

      {item.description && (
        <div className={styles.description} style={{ marginTop: 8 }}>
          {item.description}
        </div>
      )}

      {tags.length > 0 && (
        <div className={styles.tags} style={{ marginTop: 8 }}>
          {tags.map((tag) => (
            <span key={tag} className={styles.tag}>
              {tag}
            </span>
          ))}
        </div>
      )}

      <div className={styles.metaList} style={{ marginTop: 8 }}>
        {item.repository && (
          <a
            className={styles.metaLink}
            href={item.repository}
            target="_blank"
            rel="noopener noreferrer"
          >
            {item.repository}
          </a>
        )}
        {item.homepage && item.homepage !== item.repository && (
          <a
            className={styles.metaLink}
            href={item.homepage}
            target="_blank"
            rel="noopener noreferrer"
          >
            {item.homepage}
          </a>
        )}
        {item.license && (
          <span>
            {t('plugins.store.license')}: {item.license}
          </span>
        )}
      </div>

      <div className={styles.actions}>
        {canManage && (
          <span className={styles.badge} style={{ border: 'none', marginRight: 'auto' }}>
            {t('plugins.store.manage_hint')}
          </span>
        )}
        <Button
          variant={hasUpdate ? 'primary' : 'secondary'}
          size="sm"
          onClick={onInstall}
          loading={installing}
          disabled={installing}
        >
          {hasUpdate
            ? t('plugins.store.update')
            : isInstalled
              ? t('plugins.store.reinstall')
              : t('plugins.store.install')}
        </Button>
      </div>
    </Card>
  );
}
