import type { PluginListEntry, PluginMenu, PluginStoreEntry } from '@/types';
import { normalizeApiBase } from '@/utils/connection';

export const PLUGIN_RESOURCES_REFRESH_EVENT = 'plugin-resources-refresh';
export const PLUGIN_RESOURCES_SETTLE_REFRESH_DELAY_MS = 1600;

export const notifyPluginResourcesChanged = (options?: { delayMs?: number }) => {
  const dispatch = () => window.dispatchEvent(new Event(PLUGIN_RESOURCES_REFRESH_EVENT));
  const delayMs = options?.delayMs ?? 0;
  if (delayMs > 0) {
    window.setTimeout(dispatch, delayMs);
    return;
  }
  dispatch();
};

export const isPluginManagementNavVisible = ({
  supportsPlugin,
}: {
  supportsPlugin: boolean;
}) => supportsPlugin;

export const isPluginResourceNavVisible = ({
  supportsPlugin,
  pluginsEnabled,
}: {
  supportsPlugin: boolean;
  pluginsEnabled?: boolean | null;
}) => supportsPlugin && pluginsEnabled === true;

export interface PluginResourceEntry {
  pluginID: string;
  pluginTitle: string;
  pluginLogo: string;
  menuIndex: number;
  menu: PluginMenu;
  label: string;
  description: string;
  route: string;
}

export const getPluginTitle = (plugin: PluginListEntry) =>
  plugin.metadata?.name.trim() || plugin.id;

export const buildPluginResourceRoute = (pluginID: string, menuIndex: number) =>
  `/plugin-pages/${encodeURIComponent(pluginID)}/${menuIndex}`;

export const resolvePluginAssetURL = (value: string, apiBase: string) => {
  const trimmed = value.trim();
  if (!trimmed) return '';
  if (/^(https?:|data:|blob:)/i.test(trimmed)) return trimmed;
  if (!trimmed.startsWith('/')) return trimmed;
  const base = normalizeApiBase(apiBase);
  return base ? `${base}${trimmed}` : trimmed;
};

export const buildRepositoryURL = (repository: string) => {
  const trimmed = repository.trim();
  if (!trimmed) return '';
  if (/^https?:\/\//i.test(trimmed)) return trimmed;
  return `https://github.com/${trimmed.replace(/^\/+/, '')}`;
};

export const OFFICIAL_PLUGIN_REPO_PREFIX = 'https://github.com/router-for-me/';
export const DEFAULT_PLUGIN_STORE_SOURCE_ID = 'official';
const DEFAULT_PLUGIN_STORE_SOURCE_NAME = 'official';

export const getPluginRepositorySlug = (repository: string): string => {
  const trimmed = repository.trim();
  if (!trimmed) return '';
  const withoutHost = /^https?:\/\/[^/]+\/(.+)$/i.exec(trimmed)?.[1] ?? trimmed;
  const [owner = '', repo = ''] = withoutHost.replace(/^\/+/, '').split('/');
  if (!owner) return '';
  return repo ? `${owner}/${repo.replace(/\.git$/i, '')}` : owner;
};

export const isOfficialRepository = (repository: string): boolean =>
  buildRepositoryURL(repository).toLowerCase().startsWith(OFFICIAL_PLUGIN_REPO_PREFIX);

export const isOfficialPlugin = (entry: PluginStoreEntry): boolean =>
  entry.sourceId.trim().toLowerCase() === DEFAULT_PLUGIN_STORE_SOURCE_ID &&
  isOfficialRepository(entry.repository);

export const isDefaultPluginStoreSource = (
  entry: Pick<PluginStoreEntry, 'sourceId' | 'sourceName'>
): boolean =>
  entry.sourceId.trim().toLowerCase() === DEFAULT_PLUGIN_STORE_SOURCE_ID ||
  entry.sourceName.trim().toLowerCase() === DEFAULT_PLUGIN_STORE_SOURCE_NAME;

export const getPluginConfirmToken = (entry: PluginStoreEntry): string =>
  getPluginRepositorySlug(entry.repository) || entry.id;

export const collectPluginResourceEntries = (
  plugins: PluginListEntry[]
): PluginResourceEntry[] =>
  plugins.flatMap((plugin) => {
    if (!plugin.effectiveEnabled) return [];

    const pluginTitle = getPluginTitle(plugin);
    const pluginLogo = plugin.logo || plugin.metadata?.logo || '';

    return plugin.menus
      .map((menu, menuIndex): PluginResourceEntry | null => {
        const path = menu.path.trim();
        if (!path) return null;

        const menuLabel = menu.menu.trim();
        return {
          pluginID: plugin.id,
          pluginTitle,
          pluginLogo,
          menuIndex,
          menu: { ...menu, path },
          label: menuLabel || pluginTitle,
          description: menu.description.trim() || pluginTitle,
          route: buildPluginResourceRoute(plugin.id, menuIndex),
        };
      })
      .filter((entry): entry is PluginResourceEntry => Boolean(entry));
  });
