import type { GeminiKeyConfig, ModelAlias, OpenAIProviderConfig, ProviderKeyConfig } from '@/types';
import { maskApiKey } from '@/utils/format';
import { compressStatusBarData, type StatusBarData } from '@/utils/recentRequests';
import {
  getOpenAIKeyCount,
  getOpenAIUsageStatsCoverage,
  type OpenAIUsageStatsCoverage,
} from '@/utils/openAIKeyEntries';
import {
  getOpenAIProviderKey,
  getOpenAIProviderRecentStatusData,
  getOpenAIProviderTotalStats,
  getProviderConfigKey,
  getProviderRecentStatusData,
  getProviderTotalStats,
  hasDisableAllModelsRule,
  type ProviderRecentUsageMap,
} from '../utils';
import type { ProviderHistoryByRowKey, ProviderHistoryStats } from '../hooks/useProviderAccountHistory';

export type ProviderKind =
  | 'gemini'
  | 'interactions'
  | 'codex'
  | 'xai'
  | 'claude'
  | 'vertex'
  | 'openai';

export const PROVIDER_KINDS: readonly ProviderKind[] = [
  'gemini',
  'interactions',
  'codex',
  'xai',
  'claude',
  'vertex',
  'openai',
];

export interface ProviderModelEntry {
  name: string;
  alias?: string;
  displayName?: string;
}

export interface ProviderV8GroupKeyMeta {
  index: number;
  total: number;
}

export interface ProviderLatestFailure {
  statusCode?: number;
  errorCode?: string;
  summary?: string;
  timestampMs: number;
  isMostRecent: boolean;
}

export type ProviderStatsSource = 'history' | 'live' | 'none';

interface ProviderRowBase {
  /** 稳定渲染 key（kind + 配置自身的复合键） */
  key: string;
  kind: ProviderKind;
  /** 在所属 provider 原始数组中的索引，编辑/删除/启停回调使用 */
  originalIndex: number;
  /** 主展示名称：优先提供商别名 name，回退脱敏密钥或 auth-index */
  label: string;
  /** 提供商别名（CPA v8 api-keys.<family>[].name 或 OpenAI name） */
  alias: string;
  /** 单密钥渠道的脱敏密钥预览；OpenAI 为空串 */
  maskedKey: string;
  /** 模型路由前缀（不含末尾斜杠） */
  prefix: string;
  /** 提取后的上游 Host（便于双行紧凑展示） */
  hostLabel: string;
  /** 名称排序依据：优先 name，其次 prefix/baseUrl/proxyUrl/authIndex */
  sortName: string;
  baseUrl: string;
  /** 去除默认 https:// 协议头与根末尾斜杠后的紧凑端点展示文本 */
  displayEndpoint: string;
  /** 是否为当前协议族默认或标准官方 HTTPS 端点 */
  isOfficialEndpoint: boolean;
  /** 非 OpenAI 的 CPA v8 同组多密钥序号信息（仅当组内密钥数 > 1 时存在） */
  v8GroupKeyMeta?: ProviderV8GroupKeyMeta;
  priority?: number;
  weight?: number;
  hasProxy: boolean;
  websockets?: boolean;
  cloakMode?: string;
  disableCooling?: boolean | null;
  modelNames: string[];
  modelEntries: ProviderModelEntry[];
  /** 用于模型筛选匹配的完整键集合（含上游模型名与网关别名） */
  modelFilterKeys: string[];
  modelCount: number;
  excludedModelCount: number;
  excludedModelNames: string[];
  keyCount: number;
  enabled: boolean;
  stats: { success: number; failure: number };
  /** OpenAI keyless 请求无法由 CPA 的 api-key-usage 覆盖。 */
  usageStatsCoverage: OpenAIUsageStatsCoverage;
  /** 最近窗口内成功数，供 recent-success 排序使用 */
  recentSuccess: number;
  /** 最近窗口内失败数，供健康筛选使用 */
  recentFailure: number;
  statusData: StatusBarData;
  /** 当前汇总与状态条的数据口径来源 */
  statsSource: ProviderStatsSource;
  /** 来自 Manager SQLite 的持久历史统计与最近请求（仅 Full Web / Manager 模式可用） */
  history?: ProviderHistoryStats;
  /** 最近 10 笔请求内最新一次失败的状态码与摘要 */
  latestFailure?: ProviderLatestFailure;
  lastSeenMs?: number | null;
  /** 关键字搜索匹配串（小写） */
  haystack: string;
}
export type ProviderRow =
  | (ProviderRowBase & { kind: 'gemini'; raw: GeminiKeyConfig })
  | (ProviderRowBase & { kind: 'interactions'; raw: GeminiKeyConfig })
  | (ProviderRowBase & { kind: 'codex' | 'xai' | 'claude' | 'vertex'; raw: ProviderKeyConfig })
  | (ProviderRowBase & { kind: 'openai'; raw: OpenAIProviderConfig });

export interface BuildProviderRowsInput {
  gemini: GeminiKeyConfig[];
  interactions?: GeminiKeyConfig[];
  codex: ProviderKeyConfig[];
  xai?: ProviderKeyConfig[];
  claude: ProviderKeyConfig[];
  vertex: ProviderKeyConfig[];
  openai: OpenAIProviderConfig[];
  usageByProvider: ProviderRecentUsageMap;
  historyByRowKey?: ProviderHistoryByRowKey;
}
const collectModelEntries = (models?: ModelAlias[]): ProviderModelEntry[] => {
  if (!models || models.length === 0) return [];
  const entries: ProviderModelEntry[] = [];
  models.forEach((model) => {
    const name = String(model.name ?? '').trim();
    if (!name) return;
    const rawAlias = String(model.alias ?? '').trim();
    const alias = rawAlias && rawAlias !== name ? rawAlias : undefined;
    const rawDisplayName = String(model.displayName ?? '').trim();
    const displayName = rawDisplayName || undefined;
    entries.push({
      name,
      ...(alias ? { alias } : {}),
      ...(displayName ? { displayName } : {}),
    });
  });
  return entries;
};

const collectModelFilterKeys = (entries: readonly ProviderModelEntry[]): string[] => {
  const seen = new Set<string>();
  entries.forEach((entry) => {
    seen.add(entry.name);
    if (entry.alias) {
      seen.add(entry.alias);
    }
  });
  return Array.from(seen);
};

const buildHaystack = (parts: Array<string | undefined>): string =>
  parts
    .map((part) =>
      String(part ?? '')
        .trim()
        .toLowerCase()
    )
    .filter(Boolean)
    .join('\n');

const extractHostLabel = (baseUrl?: string): string => {
  const trimmed = String(baseUrl ?? '').trim();
  if (!trimmed) return '';
  try {
    return new URL(trimmed).host || trimmed;
  } catch {
    return trimmed.replace(/^https?:\/\//i, '').split('/')[0] || trimmed;
  }
};

const formatDisplayEndpoint = (baseUrl?: string): string => {
  const trimmed = String(baseUrl ?? '').trim();
  if (!trimmed) return '';
  try {
    const parsed = new URL(trimmed);
    const isHttps = parsed.protocol.toLowerCase() === 'https:';
    const normalizedPath = parsed.pathname.replace(/\/+$/, '');
    const hostAndPath = `${parsed.host}${normalizedPath}${parsed.search}`;
    return isHttps ? hostAndPath : `${parsed.protocol}//${hostAndPath}`;
  } catch {
    if (/^https:\/\//i.test(trimmed)) {
      return trimmed.replace(/^https:\/\//i, '').replace(/\/+$/, '');
    }
    return trimmed.replace(/\/+$/, '');
  }
};

const checkOfficialEndpoint = (kind: ProviderKind, baseUrl?: string): boolean => {
  const trimmed = String(baseUrl ?? '').trim();
  if (!trimmed) return true;
  try {
    const parsed = new URL(trimmed);
    if (parsed.protocol.toLowerCase() !== 'https:') return false;
    const host = parsed.hostname.toLowerCase();
    switch (kind) {
      case 'claude':
        return host === 'api.anthropic.com';
      case 'codex':
        return host === 'api.openai.com' || host === 'chatgpt.com';
      case 'gemini':
      case 'interactions':
        return host === 'generativelanguage.googleapis.com';
      case 'vertex':
        return host === 'aiplatform.googleapis.com' || host.endsWith('-aiplatform.googleapis.com');
      case 'xai':
        return host === 'api.x.ai';
      case 'openai':
        return host === 'api.openai.com';
      default:
        return false;
    }
  } catch {
    return false;
  }
};

const extractV8GroupKeyMeta = (
  config: GeminiKeyConfig | ProviderKeyConfig
): ProviderV8GroupKeyMeta | undefined => {
  const v8Source = config.v8Source;
  if (!v8Source || typeof v8Source.keyIndex !== 'number') return undefined;
  const rawKeys = v8Source.group?.keys;
  if (!Array.isArray(rawKeys) || rawKeys.length <= 1) return undefined;
  return {
    index: v8Source.keyIndex + 1,
    total: rawKeys.length,
  };
};

const resolveLatestFailure = (
  history: ProviderHistoryStats | undefined
): ProviderLatestFailure | undefined => {
  if (!history || history.recentRequests.length === 0) return undefined;
  const failedIndex = history.recentRequests.findIndex((item) => item.failed);
  if (failedIndex < 0) return undefined;
  const failed = history.recentRequests[failedIndex];
  const statusCode =
    typeof failed.fail_status_code === 'number' &&
    Number.isFinite(failed.fail_status_code) &&
    failed.fail_status_code > 0
      ? failed.fail_status_code
      : undefined;
  const errorCode = String(failed.header_error_code ?? '').trim() || undefined;
  const summary = String(failed.fail_summary ?? '').trim() || undefined;
  return {
    ...(statusCode !== undefined ? { statusCode } : {}),
    ...(errorCode ? { errorCode } : {}),
    ...(summary ? { summary } : {}),
    timestampMs: failed.timestamp_ms,
    isMostRecent: failedIndex === 0,
  };
};
const resolveLastSeenMs = (
  history: ProviderHistoryStats | undefined,
  statusData: StatusBarData
): number | null => {
  if (typeof history?.lastSeenMs === 'number' && Number.isFinite(history.lastSeenMs)) {
    return history.lastSeenMs;
  }
  for (let i = statusData.blockDetails.length - 1; i >= 0; i -= 1) {
    const detail = statusData.blockDetails[i];
    if (detail && detail.success + detail.failure > 0 && Number.isFinite(detail.endTime)) {
      return detail.endTime;
    }
  }
  return null;
};

function buildKeyConfigRow(
  kind: 'gemini' | 'interactions' | 'codex' | 'xai' | 'claude' | 'vertex',
  config: GeminiKeyConfig | ProviderKeyConfig,
  originalIndex: number,
  usageByProvider: ProviderRecentUsageMap,
  historyByRowKey?: ProviderHistoryByRowKey
): ProviderRow {
  const modelEntries = collectModelEntries(config.models);
  const modelNames = modelEntries.map((entry) => entry.name);
  const modelFilterKeys = collectModelFilterKeys(modelEntries);
  const alias = String(config.name ?? '').trim();
  const rawApiKey = String(config.apiKey ?? '').trim();
  const maskedKey = rawApiKey ? maskApiKey(rawApiKey) : '';
  const authIndexLabel = String(config.authIndex ?? '').trim();
  const prefix = String(config.prefix ?? '').trim();
  const baseUrl = config.baseUrl ?? '';
  const hostLabel = extractHostLabel(baseUrl);
  const sortName =
    [config.name, config.prefix, config.baseUrl, config.proxyUrl, config.authIndex]
      .map((value) => String(value ?? '').trim())
      .find(Boolean) ?? '';
  const excludedModelNames = (config.excludedModels ?? [])
    .map((entry) => String(entry ?? '').trim())
    .filter((entry) => Boolean(entry) && entry !== '*');
  const excludedModelCount = excludedModelNames.length;
  const providerExtra = config as ProviderKeyConfig;
  const cloakMode = providerExtra.cloak
    ? String(providerExtra.cloak.mode ?? 'auto').trim() || 'auto'
    : undefined;
  const rowKey = `${kind}:${getProviderConfigKey(config, originalIndex)}`;
  const history = historyByRowKey?.get(rowKey);
  const liveStats = getProviderTotalStats(usageByProvider, kind, config.apiKey, config.baseUrl);
  const liveTotal = liveStats.success + liveStats.failure;
  const hasLiveStats = liveTotal > 0;
  const historyTotal = history?.matched ? history.totalRequests : 0;
  const useHistorySource = Boolean(
    history?.matched &&
      (historyTotal > 0 || history.recentRequests.length > 0) &&
      (!hasLiveStats || historyTotal >= liveTotal)
  );
  const stats = useHistorySource && history
    ? { success: history.successCalls, failure: history.failureCalls }
    : liveStats;
  const liveStatusData = compressStatusBarData(
    getProviderRecentStatusData(usageByProvider, kind, config.apiKey, config.baseUrl),
    10
  );
  const hasLiveStatusBlocks = liveStatusData.totalSuccess + liveStatusData.totalFailure > 0;
  const hasHistoryStatusBlocks = Boolean(
    useHistorySource &&
      history &&
      history.recentStatusData.totalSuccess + history.recentStatusData.totalFailure > 0
  );
  const statusData =
    !hasLiveStatusBlocks && hasHistoryStatusBlocks && history
      ? history.recentStatusData
      : liveStatusData;
  const effectiveRecentSuccess = statusData.totalSuccess;
  const effectiveRecentFailure = statusData.totalFailure;
  const statsSource: ProviderStatsSource = useHistorySource
    ? 'history'
    : hasLiveStats
      ? 'live'
      : 'none';
  const v8GroupKeyMeta = extractV8GroupKeyMeta(config);
  const latestFailure = useHistorySource ? resolveLatestFailure(history) : undefined;

  return {
    key: rowKey,
    kind,
    originalIndex,
    label: alias || maskedKey || (authIndexLabel ? `#${authIndexLabel}` : hostLabel || kind),
    alias,
    maskedKey,
    prefix,
    hostLabel,
    sortName,
    baseUrl,
    displayEndpoint: formatDisplayEndpoint(baseUrl),
    isOfficialEndpoint: checkOfficialEndpoint(kind, baseUrl),
    ...(v8GroupKeyMeta ? { v8GroupKeyMeta } : {}),
    priority: config.priority,
    weight: config.weight,
    hasProxy: Boolean(String(config.proxyUrl ?? '').trim()),
    websockets: providerExtra.websockets,
    cloakMode,
    disableCooling: config.disableCooling,
    modelNames,
    modelEntries,
    modelFilterKeys,
    modelCount: modelNames.length,
    excludedModelCount,
    excludedModelNames,
    keyCount: rawApiKey ? 1 : 0,
    enabled: !hasDisableAllModelsRule(config.excludedModels),
    stats,
    usageStatsCoverage: 'full',
    recentSuccess: effectiveRecentSuccess,
    recentFailure: effectiveRecentFailure,
    statusData,
    statsSource,
    history,
    ...(latestFailure ? { latestFailure } : {}),
    lastSeenMs: resolveLastSeenMs(history, statusData),
    haystack: buildHaystack([
      alias,
      maskedKey,
      config.apiKey,
      prefix,
      baseUrl,
      config.proxyUrl,
      config.authIndex,
      ...modelFilterKeys,
      ...modelEntries.map((entry) => entry.displayName),
    ]),
    raw: config,
  } as ProviderRow;
}

function buildOpenAIRow(
  provider: OpenAIProviderConfig,
  originalIndex: number,
  usageByProvider: ProviderRecentUsageMap,
  historyByRowKey?: ProviderHistoryByRowKey
): ProviderRow {
  const apiKeyEntries = provider.apiKeyEntries ?? [];
  const modelEntries = collectModelEntries(provider.models);
  const modelNames = modelEntries.map((entry) => entry.name);
  const modelFilterKeys = collectModelFilterKeys(modelEntries);
  const prefix = String(provider.prefix ?? '').trim();
  const baseUrl = provider.baseUrl ?? '';
  const hostLabel = extractHostLabel(baseUrl);
  const rowKey = `openai:${getOpenAIProviderKey(provider, originalIndex)}`;
  const history = historyByRowKey?.get(rowKey);
  const liveStats = getOpenAIProviderTotalStats(provider, usageByProvider);
  const liveTotal = liveStats.success + liveStats.failure;
  const hasLiveStats = liveTotal > 0;
  const historyTotal = history?.matched ? history.totalRequests : 0;
  const useHistorySource = Boolean(
    history?.matched &&
      (historyTotal > 0 || history.recentRequests.length > 0) &&
      (!hasLiveStats || historyTotal >= liveTotal)
  );
  const stats = useHistorySource && history
    ? { success: history.successCalls, failure: history.failureCalls }
    : liveStats;
  const liveStatusData = compressStatusBarData(
    getOpenAIProviderRecentStatusData(provider, usageByProvider),
    10
  );
  const hasLiveStatusBlocks = liveStatusData.totalSuccess + liveStatusData.totalFailure > 0;
  const hasHistoryStatusBlocks = Boolean(
    useHistorySource &&
      history &&
      history.recentStatusData.totalSuccess + history.recentStatusData.totalFailure > 0
  );
  const statusData =
    !hasLiveStatusBlocks && hasHistoryStatusBlocks && history
      ? history.recentStatusData
      : liveStatusData;
  const effectiveRecentSuccess = statusData.totalSuccess;
  const effectiveRecentFailure = statusData.totalFailure;
  const statsSource: ProviderStatsSource = useHistorySource
    ? 'history'
    : hasLiveStats
      ? 'live'
      : 'none';
  const latestFailure = useHistorySource ? resolveLatestFailure(history) : undefined;
  const nonEmptyApiKeys = apiKeyEntries
    .map((entry) => String(entry.apiKey ?? '').trim())
    .filter(Boolean);
  const singleMaskedKey = nonEmptyApiKeys.length === 1 ? maskApiKey(nonEmptyApiKeys[0]) : '';

  return {
    key: rowKey,
    kind: 'openai',
    originalIndex,
    label: provider.name || hostLabel || provider.baseUrl,
    alias: String(provider.name ?? '').trim(),
    maskedKey: singleMaskedKey,
    prefix,
    hostLabel,
    sortName: provider.name,
    baseUrl,
    displayEndpoint: formatDisplayEndpoint(baseUrl),
    isOfficialEndpoint: checkOfficialEndpoint('openai', baseUrl),
    priority: provider.priority,
    hasProxy: apiKeyEntries.some((entry) => Boolean(String(entry.proxyUrl ?? '').trim())),
    disableCooling: provider.disableCooling,
    modelNames,
    modelEntries,
    modelFilterKeys,
    modelCount: modelNames.length,
    excludedModelCount: 0,
    excludedModelNames: [],
    keyCount: getOpenAIKeyCount(apiKeyEntries),
    enabled: provider.disabled !== true,
    stats,
    usageStatsCoverage: getOpenAIUsageStatsCoverage(apiKeyEntries),
    recentSuccess: effectiveRecentSuccess,
    recentFailure: effectiveRecentFailure,
    statusData,
    statsSource,
    history,
    ...(latestFailure ? { latestFailure } : {}),
    lastSeenMs: resolveLastSeenMs(history, statusData),
    haystack: buildHaystack([
      provider.name,
      prefix,
      baseUrl,
      provider.authIndex,
      ...modelFilterKeys,
      ...modelEntries.map((entry) => entry.displayName),
      ...apiKeyEntries.flatMap((entry) => {
        const rawEntryKey = String(entry.apiKey ?? '').trim();
        return [rawEntryKey ? maskApiKey(rawEntryKey) : '', entry.apiKey, entry.proxyUrl];
      }),
    ]),
    raw: provider,
  };
}

export function buildProviderRows({
  gemini,
  interactions = [],
  codex,
  xai = [],
  claude,
  vertex,
  openai,
  usageByProvider,
  historyByRowKey,
}: BuildProviderRowsInput): ProviderRow[] {
  return [
    ...gemini.map((config, index) =>
      buildKeyConfigRow('gemini', config, index, usageByProvider, historyByRowKey)
    ),
    ...interactions.map((config, index) =>
      buildKeyConfigRow('interactions', config, index, usageByProvider, historyByRowKey)
    ),
    ...codex.map((config, index) =>
      buildKeyConfigRow('codex', config, index, usageByProvider, historyByRowKey)
    ),
    ...xai.map((config, index) =>
      buildKeyConfigRow('xai', config, index, usageByProvider, historyByRowKey)
    ),
    ...claude.map((config, index) =>
      buildKeyConfigRow('claude', config, index, usageByProvider, historyByRowKey)
    ),
    ...vertex.map((config, index) =>
      buildKeyConfigRow('vertex', config, index, usageByProvider, historyByRowKey)
    ),
    ...openai.map((provider, index) =>
      buildOpenAIRow(provider, index, usageByProvider, historyByRowKey)
    ),
  ];
}
