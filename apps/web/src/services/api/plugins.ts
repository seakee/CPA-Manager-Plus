import { apiClient, createScopedApiRequestConfig, type ApiClientRequestScope } from './client';
import type {
  PluginConfigField,
  PluginConfigObject,
  PluginDeleteResult,
  PluginListEntry,
  PluginListResponse,
  PluginMetadata,
  PluginMenu,
  PluginStoreEntry,
  PluginStoreInstallResult,
  PluginStorePlatform,
  PluginStoreResponse,
  PluginStoreSource,
  PluginStoreSourceError,
} from '@/types';
import { isRecord } from '@/utils/helpers';

const asString = (value: unknown): string => {
  if (value === undefined || value === null) return '';
  return String(value);
};

const asBoolean = (value: unknown): boolean => value === true;

const hasOwn = (source: Record<string, unknown>, key: string): boolean =>
  Object.prototype.hasOwnProperty.call(source, key);

const normalizePluginOAuthProvider = (value: unknown): string | undefined => {
  const provider = asString(value).trim();
  return provider || undefined;
};

const normalizeConfigField = (value: unknown): PluginConfigField | null => {
  if (!isRecord(value)) return null;
  const name = asString(value.name).trim();
  if (!name) return null;
  const enumValues = Array.isArray(value.enum_values)
    ? value.enum_values.map((item) => asString(item).trim()).filter(Boolean)
    : Array.isArray(value.enumValues)
      ? value.enumValues.map((item) => asString(item).trim()).filter(Boolean)
      : [];

  return {
    name,
    type: asString(value.type).trim() || 'string',
    enumValues,
    description: asString(value.description).trim(),
    defaultValue: value.default,
  };
};

const normalizeConfigFields = (value: unknown): PluginConfigField[] =>
  Array.isArray(value)
    ? value
        .map((item) => normalizeConfigField(item))
        .filter((field): field is PluginConfigField => Boolean(field))
    : [];

const normalizeMetadata = (value: unknown): PluginMetadata | null => {
  if (!isRecord(value)) return null;
  const name = asString(value.name).trim();
  const version = asString(value.version).trim();
  const author = asString(value.author).trim();
  const githubRepository = asString(value.github_repository ?? value.githubRepository).trim();
  const logo = asString(value.logo).trim();
  const configFields = normalizeConfigFields(value.config_fields ?? value.configFields);

  if (!name && !version && !author && !githubRepository && !logo && configFields.length === 0) {
    return null;
  }

  return {
    name,
    version,
    author,
    githubRepository,
    logo,
    configFields,
  };
};

const normalizeMenu = (value: unknown): PluginMenu | null => {
  if (!isRecord(value)) return null;
  const path = asString(value.path).trim();
  const menu = asString(value.menu).trim();
  if (!path && !menu) return null;
  return {
    path,
    menu,
    description: asString(value.description).trim(),
  };
};

const normalizeMenus = (value: unknown): PluginMenu[] =>
  Array.isArray(value)
    ? value.map((item) => normalizeMenu(item)).filter((menu): menu is PluginMenu => Boolean(menu))
    : [];

const normalizePluginEntry = (value: unknown): PluginListEntry | null => {
  if (!isRecord(value)) return null;
  const id = asString(value.id).trim();
  if (!id) return null;

  const metadata = normalizeMetadata(value.metadata);
  const configFields = normalizeConfigFields(value.config_fields ?? value.configFields);
  const supportsOAuth = asBoolean(value.supports_oauth ?? value.supportsOAuth);
  const oauthProvider = normalizePluginOAuthProvider(value.oauth_provider ?? value.oauthProvider);
  const legacyOAuthProvider =
    supportsOAuth && !hasOwn(value, 'oauth_provider') && !hasOwn(value, 'oauthProvider')
      ? normalizePluginOAuthProvider(id)
      : undefined;

  return {
    id,
    oauthProvider: oauthProvider ?? legacyOAuthProvider,
    path: asString(value.path).trim(),
    configured: asBoolean(value.configured),
    registered: asBoolean(value.registered),
    enabled: value.enabled !== false,
    effectiveEnabled: asBoolean(value.effective_enabled ?? value.effectiveEnabled),
    supportsOAuth,
    logo: asString(value.logo || metadata?.logo).trim(),
    configFields: configFields.length > 0 ? configFields : (metadata?.configFields ?? []),
    menus: normalizeMenus(value.menus),
    metadata,
  };
};

export const normalizePluginList = (value: unknown): PluginListResponse => {
  const source = isRecord(value) ? value : {};
  const plugins = Array.isArray(source.plugins)
    ? source.plugins
        .map((item) => normalizePluginEntry(item))
        .filter((plugin): plugin is PluginListEntry => Boolean(plugin))
    : [];

  return {
    pluginsEnabled: asBoolean(source.plugins_enabled ?? source.pluginsEnabled),
    pluginsDir: asString(source.plugins_dir ?? source.pluginsDir).trim() || 'plugins',
    plugins,
  };
};

const normalizePluginConfig = (value: unknown): PluginConfigObject =>
  isRecord(value) ? { ...value } : {};

export const normalizePluginDeleteResult = (value: unknown): PluginDeleteResult => {
  const source = isRecord(value) ? value : {};
  return {
    status: asString(source.status).trim(),
    id: asString(source.id).trim(),
    path: asString(source.path).trim(),
    fileDeleted: asBoolean(source.file_deleted ?? source.fileDeleted),
    configuredRemoved: asBoolean(source.configured_removed ?? source.configuredRemoved),
    restartRequired: asBoolean(source.restart_required ?? source.restartRequired),
  };
};

const normalizeStoreEntry = (value: unknown): PluginStoreEntry | null => {
  if (!isRecord(value)) return null;
  const id = asString(value.id).trim();
  if (!id) return null;

  const tags = Array.isArray(value.tags)
    ? value.tags.map((item) => asString(item).trim()).filter(Boolean)
    : [];
  const platforms = Array.isArray(value.platforms)
    ? (value.platforms
        .map((item): PluginStorePlatform | null => {
          if (!isRecord(item)) return null;
          const goos = asString(item.goos).trim();
          const goarch = asString(item.goarch).trim();
          return goos || goarch ? { goos, goarch } : null;
        })
        .filter(Boolean) as PluginStorePlatform[])
    : [];

  return {
    storeId: asString(value.store_id ?? value.storeId).trim(),
    sourceId: asString(value.source_id ?? value.sourceId).trim(),
    sourceName: asString(value.source_name ?? value.sourceName).trim(),
    sourceUrl: asString(value.source_url ?? value.sourceUrl).trim(),
    id,
    name: asString(value.name).trim(),
    description: asString(value.description).trim(),
    author: asString(value.author).trim(),
    version: asString(value.version).trim(),
    repository: asString(value.repository).trim(),
    installType: asString(value.install_type ?? value.installType).trim(),
    authRequired: asBoolean(value.auth_required ?? value.authRequired),
    authConfigured: asBoolean(value.auth_configured ?? value.authConfigured),
    platforms,
    logo: asString(value.logo).trim(),
    homepage: asString(value.homepage).trim(),
    license: asString(value.license).trim(),
    tags,
    installed: asBoolean(value.installed),
    installedVersion: asString(value.installed_version ?? value.installedVersion).trim(),
    path: asString(value.path).trim(),
    configured: asBoolean(value.configured),
    registered: asBoolean(value.registered),
    enabled: asBoolean(value.enabled),
    effectiveEnabled: asBoolean(value.effective_enabled ?? value.effectiveEnabled),
    updateAvailable: asBoolean(value.update_available ?? value.updateAvailable),
  };
};

const normalizeStoreSource = (value: unknown): PluginStoreSource | null => {
  if (!isRecord(value)) return null;
  const id = asString(value.id).trim();
  const url = asString(value.url).trim();
  if (!id && !url) return null;
  return {
    id,
    name: asString(value.name).trim(),
    url,
  };
};

const normalizeStoreSources = (value: unknown): PluginStoreSource[] =>
  Array.isArray(value)
    ? value
        .map((item) => normalizeStoreSource(item))
        .filter((source): source is PluginStoreSource => Boolean(source))
    : [];

const normalizeStoreSourceError = (value: unknown): PluginStoreSourceError | null => {
  if (!isRecord(value)) return null;
  const message = asString(value.message).trim();
  const sourceId = asString(value.source_id ?? value.sourceId).trim();
  const sourceUrl = asString(value.source_url ?? value.sourceUrl).trim();
  if (!message && !sourceId && !sourceUrl) return null;
  return {
    sourceId,
    sourceName: asString(value.source_name ?? value.sourceName).trim(),
    sourceUrl,
    message,
  };
};

const normalizeStoreSourceErrors = (value: unknown): PluginStoreSourceError[] =>
  Array.isArray(value)
    ? value
        .map((item) => normalizeStoreSourceError(item))
        .filter((sourceError): sourceError is PluginStoreSourceError => Boolean(sourceError))
    : [];

export const normalizePluginStoreList = (value: unknown): PluginStoreResponse => {
  const source = isRecord(value) ? value : {};
  const plugins = Array.isArray(source.plugins)
    ? source.plugins
        .map((item) => normalizeStoreEntry(item))
        .filter((plugin): plugin is PluginStoreEntry => Boolean(plugin))
    : [];

  return {
    pluginsEnabled: asBoolean(source.plugins_enabled ?? source.pluginsEnabled),
    pluginsDir: asString(source.plugins_dir ?? source.pluginsDir).trim() || 'plugins',
    sources: normalizeStoreSources(source.sources),
    sourceErrors: normalizeStoreSourceErrors(source.source_errors ?? source.sourceErrors),
    plugins,
  };
};

export const normalizePluginStoreInstallResult = (value: unknown): PluginStoreInstallResult => {
  const source = isRecord(value) ? value : {};
  return {
    status: asString(source.status).trim(),
    sourceId: asString(source.source_id ?? source.sourceId).trim(),
    sourceName: asString(source.source_name ?? source.sourceName).trim(),
    sourceUrl: asString(source.source_url ?? source.sourceUrl).trim(),
    id: asString(source.id).trim(),
    version: asString(source.version).trim(),
    installType: asString(source.install_type ?? source.installType).trim(),
    path: asString(source.path).trim(),
    pluginsEnabled: asBoolean(source.plugins_enabled ?? source.pluginsEnabled),
    restartRequired: asBoolean(source.restart_required ?? source.restartRequired),
  };
};

export interface PluginStoreInstallOptions {
  sourceId?: string;
  version?: string;
}

export const pluginsApi = {
  async list(requestScope?: ApiClientRequestScope): Promise<PluginListResponse> {
    const data = await apiClient.get(
      '/plugins',
      requestScope ? createScopedApiRequestConfig(requestScope) : undefined
    );
    return normalizePluginList(data);
  },

  updateEnabled: (id: string, enabled: boolean) =>
    apiClient.patch(`/plugins/${encodeURIComponent(id)}/enabled`, { enabled }),

  async deletePlugin(id: string): Promise<PluginDeleteResult> {
    const data = await apiClient.delete(`/plugins/${encodeURIComponent(id)}`);
    return normalizePluginDeleteResult(data);
  },

  async getConfig(id: string): Promise<PluginConfigObject> {
    const data = await apiClient.get(`/plugins/${encodeURIComponent(id)}/config`);
    return normalizePluginConfig(data);
  },

  putConfig: (id: string, config: PluginConfigObject) =>
    apiClient.put(`/plugins/${encodeURIComponent(id)}/config`, config),

  patchConfig: (id: string, patch: PluginConfigObject) =>
    apiClient.patch(`/plugins/${encodeURIComponent(id)}/config`, patch),
};

export const pluginStoreApi = {
  async list(): Promise<PluginStoreResponse> {
    const data = await apiClient.get('/plugin-store');
    return normalizePluginStoreList(data);
  },

  async install(
    id: string,
    options: PluginStoreInstallOptions = {}
  ): Promise<PluginStoreInstallResult> {
    const sourceId = options?.sourceId?.trim();
    const version = options?.version?.trim();
    const params: Record<string, string> = {};
    if (sourceId) params.source = sourceId;
    if (version) params.version = version;
    const data = await apiClient.post(
      `/plugin-store/${encodeURIComponent(id)}/install`,
      version ? { version } : undefined,
      {
        params: Object.keys(params).length > 0 ? params : undefined,
      }
    );
    return normalizePluginStoreInstallResult(data);
  },
};
