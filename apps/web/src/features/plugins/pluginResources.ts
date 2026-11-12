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

// Plugin management stays reachable whenever the deployment can serve plugins,
// including while the global switch is off. That page is where the switch lives,
// and its disabled state renders an explicit "the global switch is off" notice,
// so hiding the entry would leave an administrator with no way back on besides
// editing config by hand. Only the plugin's own menus depend on the switch.
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

export const PLUGIN_RESOURCE_PATH_PREFIX = '/v0/resource/plugins/';

// A plugin page may only be loaded from this deployment's own plugin resource
// namespace. External http(s), data:, blob: and bare relative targets are
// unsupported: the sandbox document is served with an opaque-origin CSP, so
// only a same-origin prefix URL keeps the response under Manager control.
export const isSupportedPluginResourcePath = (value: string): boolean => {
  const trimmed = value.trim();
  if (!trimmed.startsWith(PLUGIN_RESOURCE_PATH_PREFIX)) return false;
  // Reject scheme-relative and backslash aliases, encoded separators, and any
  // dot segment that would escape the namespace after normalization. Percent
  // encoding is refused outright: the Manager's plugin resource predicate only
  // admits paths whose escaped form equals the decoded form, so an encoded
  // segment could never be served and only exists to smuggle a traversal.
  if (/[\\\s%]/.test(trimmed)) return false;
  if (trimmed.includes('//') || trimmed.includes('/./') || trimmed.includes('/../')) return false;
  if (trimmed.endsWith('/..') || trimmed.endsWith('/.')) return false;
  // One plugin id segment must be present.
  const rest = trimmed.slice(PLUGIN_RESOURCE_PATH_PREFIX.length);
  if (!rest) return false;
  // The namespace is served from the origin root, not below the Manager API
  // prefix, so the remainder must stay a plain relative path.
  return !/^[a-z][a-z0-9+.-]*:/i.test(rest) && !rest.startsWith('/');
};

const documentOrigin = (): string => {
  if (typeof window === 'undefined') return '';
  const origin = window.location?.origin;
  return typeof origin === 'string' ? origin : '';
};

// Plugin resources are answered at the origin root by the Manager proxy. The
// console's configured API base therefore contributes only its origin: the
// `/api/v2/cpamp` namespace never serves plugin documents, and a cross-origin
// base cannot satisfy the admin document's `frame-src 'self'` policy or the
// Manager's opaque-origin sandbox response contract.
export const resolvePluginAssetURL = (value: string, apiBase: string) => {
  const trimmed = value.trim();
  if (!isSupportedPluginResourcePath(trimmed)) return '';

  const origin = documentOrigin();
  if (!origin) return '';
  const base = normalizeApiBase(apiBase);
  if (!base) return `${origin}${trimmed}`;
  try {
    if (new URL(base).origin !== origin) return '';
  } catch {
    return '';
  }
  return `${origin}${trimmed}`;
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
