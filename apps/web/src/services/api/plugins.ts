/**
 * 插件管理相关 API
 * 对接 CLIProxyAPI `/v0/management/plugins` 与 `/v0/management/plugin-store` 系列接口。
 * 文档版本：CLIProxyAPI v7.2.0（commit 2a050dc9）。
 */

import { apiClient } from './client';

/** 插件配置字段声明类型 */
export type PluginConfigFieldType =
  | 'string'
  | 'number'
  | 'integer'
  | 'boolean'
  | 'enum'
  | 'array'
  | 'object';

export interface PluginConfigField {
  name: string;
  type: PluginConfigFieldType;
  description?: string;
  /** 仅 type === 'enum' 时有值 */
  enum_values?: string[];
  /** 字段默认值（可选，由插件声明） */
  default?: unknown;
  required?: boolean;
}

/** 插件动态菜单/资源入口 */
export interface PluginMenu {
  /** 资源路由，形如 `/v0/resource/plugins/<id>/<subpath>` 或管理路由 */
  path: string;
  menu: string;
  description?: string;
}

/** 插件元数据（仅已注册时有值） */
export interface PluginMetadata {
  name?: string;
  version?: string;
  author?: string;
  github_repository?: string;
  logo?: string;
  config_fields?: PluginConfigField[];
}

/** GET /plugins 单个插件条目 */
export interface PluginListItem {
  id: string;
  path?: string;
  configured: boolean;
  registered: boolean;
  enabled: boolean;
  /** 实际生效：global plugins.enabled && enabled && registered */
  effective_enabled: boolean;
  supports_oauth?: boolean;
  logo?: string;
  config_fields?: PluginConfigField[];
  menus?: PluginMenu[];
  metadata?: PluginMetadata;
}

export interface PluginsListResponse {
  plugins_enabled: boolean;
  plugins_dir: string;
  plugins: PluginListItem[];
}

/** GET /plugin-store 商店条目 */
export interface PluginStoreItem {
  id: string;
  name?: string;
  description?: string;
  author?: string;
  /** 商店最新版本（优先取 release 版本） */
  version?: string;
  repository?: string;
  logo?: string;
  homepage?: string;
  license?: string;
  tags?: string[];
  installed: boolean;
  installed_version?: string;
  path?: string;
  configured: boolean;
  registered: boolean;
  enabled: boolean;
  effective_enabled: boolean;
  update_available: boolean;
}

export interface PluginStoreResponse {
  plugins_enabled: boolean;
  plugins_dir: string;
  plugins: PluginStoreItem[];
}

export interface PluginDeleteResponse {
  status: 'deleted';
  id: string;
  path?: string;
  file_deleted: boolean;
  configured_removed: boolean;
  restart_required: boolean;
}

export interface PluginInstallResponse {
  status: 'installed';
  id: string;
  version?: string;
  path?: string;
  plugins_enabled: boolean;
  restart_required: boolean;
}

/** 插件原始配置（任意 JSON 对象，含 enabled/priority 等自定义字段） */
export type PluginConfig = Record<string, unknown>;

/** PATCH 合并配置体：值为 null 表示删除该字段 */
export type PluginConfigPatch = Record<string, unknown>;

const isRecord = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value);

const readHeader = (
  headers: Record<string, unknown> | undefined,
  keys: string[]
): string | null => {
  if (!headers) return null;
  const getter = (headers as { get?: (name: string) => unknown }).get;
  if (typeof getter === 'function') {
    for (const key of keys) {
      const raw = getter.call(headers, key);
      if (raw !== undefined && raw !== null && String(raw).trim()) {
        return String(raw);
      }
    }
  }
  const normalized = Object.fromEntries(
    Object.entries(headers).map(([k, v]) => [k.toLowerCase(), v])
  );
  for (const key of keys) {
    const raw = normalized[key.toLowerCase()];
    if (raw !== undefined && raw !== null && String(raw).trim()) {
      return String(raw);
    }
  }
  return null;
};

/**
 * 服务端能力探测（文档 §1.3 / §5.1）。
 * 通过响应头 `X-CPA-SUPPORT-PLUGIN` 判断是否支持插件：
 * `1` = CGO 编译，支持动态插件；`0` = 不支持，应禁用插件相关 UI。
 *
 * 即便不支持，GET /plugins 仍会返回 200 空列表，因此独立探测头。
 */
export async function detectPluginSupport(): Promise<boolean> {
  try {
    const response = await apiClient.getRaw('/plugins', {
      // 仅做能力探测，尽量减少等待
      headers: { Accept: 'application/json' }
    });
    const support = readHeader(
      response.headers as Record<string, unknown> | undefined,
      ['x-cpa-support-plugin']
    );
    return support !== '0';
  } catch {
    // 探测失败时假设支持，交由列表加载阶段的错误展示来兜底
    return true;
  }
}

export const pluginApi = {
  /** GET /plugins — 列出所有插件（本地 + 配置 + 已注册） */
  list: () => apiClient.get<PluginsListResponse>('/plugins'),

  /** GET /plugins/:id/config — 获取插件原始配置 */
  getConfig: (id: string) => apiClient.get<PluginConfig>(`/plugins/${encodeURIComponent(id)}/config`),

  /** PUT /plugins/:id/config — 整体替换插件配置 */
  putConfig: (id: string, config: PluginConfig) =>
    apiClient.put<void>(`/plugins/${encodeURIComponent(id)}/config`, config),

  /** PATCH /plugins/:id/config — 浅合并（null 删除字段） */
  patchConfig: (id: string, patch: PluginConfigPatch) =>
    apiClient.patch<void>(`/plugins/${encodeURIComponent(id)}/config`, patch),

  /** PATCH /plugins/:id/enabled — 切换启用状态 */
  setEnabled: (id: string, enabled: boolean) =>
    apiClient.patch<void>(`/plugins/${encodeURIComponent(id)}/enabled`, { enabled }),

  /** DELETE /plugins/:id — 删除插件文件及配置 */
  remove: (id: string) =>
    apiClient.delete<PluginDeleteResponse>(`/plugins/${encodeURIComponent(id)}`),

  /** GET /plugin-store — 列出插件商店 */
  listStore: () => apiClient.get<PluginStoreResponse>('/plugin-store'),

  /** POST /plugin-store/:id/install — 安装/更新插件 */
  install: (id: string) =>
    apiClient.post<PluginInstallResponse>(`/plugin-store/${encodeURIComponent(id)}/install`)
};

/**
 * 识别 409 需重启的冲突（文档 §3.6 / §3.8）。
 * 删除或更新已加载插件时，Windows 上无法热替换，返回 409。
 *
 * 注意：ApiError.code 是 axios 错误码（如 ERR_NETWORK），
 * 真正的 API 错误码位于响应体 error.details.error。
 */
export function isRestartRequiredError(error: unknown): boolean {
  if (!isRecord(error)) return false;
  const status = typeof error.status === 'number' ? error.status : undefined;
  if (status !== 409) return false;
  const details = isRecord(error.details) ? error.details : null;
  const detailCode = details?.error;
  return (
    typeof detailCode === 'string' &&
    (detailCode === 'plugin_delete_requires_restart' ||
      detailCode === 'plugin_update_requires_restart')
  );
}

/** 判断错误是否为服务端不支持插件（403 remote management key not set 等）的兜底 */
export function isPluginUnsupportedError(error: unknown): boolean {
  if (!isRecord(error)) return false;
  return error.status === 403;
}
