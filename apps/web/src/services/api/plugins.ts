import { apiClient } from './client';
import type {
  ApiError,
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
import { sha256Hex } from '@/utils/apiKeyHash';
import { isRecord } from '@/utils/helpers';

/** Public key metadata from cpa-key-policy GET /keys (safe fields only). */
export type KeyPolicyKeyPublicMeta = {
  id: string;
  name: string;
  enabled: boolean;
  keyPreview: string;
  /** SHA-256(trim(id)), aligns with usage_events.api_key_hash for plugin keys. */
  apiKeyHash: string;
  source: 'plugin:cpa-key-policy';
};

export const KEY_POLICY_PLUGIN_ID = 'cpa-key-policy';
export const KEY_POLICY_KEY_SOURCE = 'plugin:cpa-key-policy' as const;

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

/**
 * Normalize cpa-key-policy public key list. Only safe fields (id/name/enabled/
 * key_preview) are kept; secrets (plain_key / key_hash) are never read or stored.
 * apiKeyHash is always SHA-256(trim(id)) to match usage_events.api_key_hash.
 */
const readKeyPolicyKeyItems = (value: unknown): unknown[] | null => {
  if (Array.isArray(value)) return value;
  if (isRecord(value) && Array.isArray(value.keys)) return value.keys;
  return null;
};

const isValidKeyPolicyKeyItem = (value: unknown): value is Record<string, unknown> =>
  isRecord(value) && Boolean(asString(value.id).trim());

/**
 * Statuses that mean "this deployment does not expose a cpa-key-policy key
 * catalog": the plugin is missing, disabled, or the CPA build predates plugin
 * management. They are indistinguishable from the Manager proxy and all lead to
 * the same conclusion — there are no plugin caller keys to show.
 */
export const isKeyPolicyCatalogAbsentStatus = (status: number | undefined): boolean =>
  status === 404 || status === 501;

export type KeyPolicyKeyCatalogResult =
  | { status: 'ready'; keys: KeyPolicyKeyPublicMeta[] }
  | { status: 'absent' };

export const normalizeKeyPolicyKeys = (value: unknown): KeyPolicyKeyPublicMeta[] => {
  const rawKeys = readKeyPolicyKeyItems(value) ?? [];

  const out: KeyPolicyKeyPublicMeta[] = [];
  const seen = new Set<string>();
  for (const item of rawKeys) {
    if (!isRecord(item)) continue;
    const id = asString(item.id).trim();
    if (!id) continue;
    const apiKeyHash = sha256Hex(id).toLowerCase();
    if (!apiKeyHash || seen.has(apiKeyHash)) continue;
    seen.add(apiKeyHash);
    out.push({
      id,
      name: asString(item.name).trim(),
      enabled: item.enabled !== false,
      keyPreview: asString(item.key_preview ?? item.keyPreview).trim(),
      apiKeyHash,
      source: KEY_POLICY_KEY_SOURCE,
    });
  }
  return out;
};

export const pluginsApi = {
  async list(): Promise<PluginListResponse> {
    const data = await apiClient.get('/plugins');
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

  /**
   * Read-only public key metadata from cpa-key-policy via the existing management
   * proxy. Does not persist results; caller keeps them in memory for display.
   *
   * `absent` means the deployment simply has no cpa-key-policy endpoint (plugin
   * not installed or disabled) — an expected state, not a failure. Anything else
   * rejects, so a real outage is never reported as "this deployment has no keys".
   */
  async listKeyPolicyKeys(): Promise<KeyPolicyKeyCatalogResult> {
    let data: unknown;
    try {
      data = await apiClient.get(`/plugins/${encodeURIComponent(KEY_POLICY_PLUGIN_ID)}/keys`);
    } catch (error) {
      if (isKeyPolicyCatalogAbsentStatus((error as ApiError)?.status)) {
        return { status: 'absent' };
      }
      throw error;
    }
    const rawKeys = readKeyPolicyKeyItems(data);
    if (rawKeys === null || rawKeys.some((item) => !isValidKeyPolicyKeyItem(item))) {
      throw new Error('Invalid cpa-key-policy key catalog response');
    }
    return { status: 'ready', keys: normalizeKeyPolicyKeys(data) };
  },
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
