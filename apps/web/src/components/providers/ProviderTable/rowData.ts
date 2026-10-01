import type { GeminiKeyConfig, OpenAIProviderConfig, ProviderKeyConfig } from '@/types';
import type { ProviderKeyAlias } from '@/services/api/usageService';
import { sha256Hex } from '@/utils/apiKeyHash';
import { maskApiKey } from '@/utils/format';
import type { StatusBarData } from '@/utils/recentRequests';
import {
  getOpenAIProviderKey,
  getOpenAIProviderRecentStatusData,
  getOpenAIProviderRecentWindowStats,
  getOpenAIProviderTotalStats,
  getProviderConfigKey,
  getProviderRecentStatusData,
  getProviderRecentWindowStats,
  getProviderTotalStats,
  hasDisableAllModelsRule,
  type ProviderRecentUsageMap,
} from '../utils';

export type ProviderKind =
  | 'gemini'
  | 'interactions'
  | 'codex'
  | 'xai'
  | 'meta'
  | 'claude'
  | 'vertex'
  | 'openai';

export const PROVIDER_KINDS: readonly ProviderKind[] = [
  'gemini',
  'interactions',
  'codex',
  'xai',
  'meta',
  'claude',
  'vertex',
  'openai',
];

interface ProviderRowBase {
  /** 稳定渲染 key（kind + 配置自身的复合键） */
  key: string;
  kind: ProviderKind;
  /** 在所属 provider 原始数组中的索引，编辑/删除/启停回调使用 */
  originalIndex: number;
  /** 表格标识列：OpenAI 为名称，其余为掩码密钥 */
  label: string;
  /** 名称排序依据：OpenAI 为名称，其余沿用 prefix/baseUrl/proxyUrl/authIndex 首个非空值 */
  sortName: string;
  baseUrl: string;
  priority?: number;
  modelNames: string[];
  modelCount: number;
  /** OpenAI 为密钥条目数，其余固定为 1 */
  keyCount: number;
  enabled: boolean;
  stats: { success: number; failure: number };
  /** 最近窗口内成功数，供 recent-success 排序使用 */
  recentSuccess: number;
  statusData: StatusBarData;
  /** 关键字搜索匹配串（小写） */
  haystack: string;
}

export type ProviderRow =
  | (ProviderRowBase & { kind: 'gemini'; raw: GeminiKeyConfig })
  | (ProviderRowBase & { kind: 'interactions'; raw: GeminiKeyConfig })
  | (ProviderRowBase & {
      kind: 'codex' | 'xai' | 'meta' | 'claude' | 'vertex';
      raw: ProviderKeyConfig;
    })
  | (ProviderRowBase & { kind: 'openai'; raw: OpenAIProviderConfig });

export interface BuildProviderRowsInput {
  gemini: GeminiKeyConfig[];
  interactions?: GeminiKeyConfig[];
  codex: ProviderKeyConfig[];
  xai?: ProviderKeyConfig[];
  meta?: ProviderKeyConfig[];
  claude: ProviderKeyConfig[];
  vertex: ProviderKeyConfig[];
  openai: OpenAIProviderConfig[];
  usageByProvider: ProviderRecentUsageMap;
  providerKeyAliases?: ProviderKeyAlias[];
}

const collectModelNames = (models?: { name: string }[]): string[] =>
  (models ?? []).map((model) => model.name).filter(Boolean);

const buildHaystack = (parts: Array<string | undefined>): string =>
  parts
    .map((part) =>
      String(part ?? '')
        .trim()
        .toLowerCase()
    )
    .filter(Boolean)
    .join('\n');

const getKeyConfigSortName = (config: GeminiKeyConfig | ProviderKeyConfig): string =>
  [config.prefix, config.baseUrl, config.proxyUrl, config.authIndex]
    .map((value) => String(value ?? '').trim())
    .find(Boolean) ?? '';

const getCodexAlias = (apiKey: string, providerKeyAliases: ProviderKeyAlias[]): string => {
  if (!apiKey) return '';
  const apiKeyHash = sha256Hex(apiKey).toLowerCase();
  return (
    providerKeyAliases
      .find(
        (item) =>
          item.provider.trim().toLowerCase() === 'codex' &&
          item.apiKeyHash.trim().toLowerCase() === apiKeyHash
      )
      ?.alias.trim() ?? ''
  );
};

function buildKeyConfigRow(
  kind: 'gemini' | 'interactions' | 'codex' | 'xai' | 'meta' | 'claude' | 'vertex',
  config: GeminiKeyConfig | ProviderKeyConfig,
  originalIndex: number,
  usageByProvider: ProviderRecentUsageMap,
  providerKeyAliases: ProviderKeyAlias[]
): ProviderRow {
  const modelNames = collectModelNames(config.models);
  const label = kind === 'codex' ? getCodexAlias(config.apiKey, providerKeyAliases) : '';
  return {
    key: `${kind}:${getProviderConfigKey(config, originalIndex)}`,
    kind,
    originalIndex,
    label: label || maskApiKey(config.apiKey),
    sortName: getKeyConfigSortName(config),
    baseUrl: config.baseUrl ?? '',
    priority: config.priority,
    modelNames,
    modelCount: modelNames.length,
    keyCount: 1,
    enabled: !hasDisableAllModelsRule(config.excludedModels),
    stats: getProviderTotalStats(usageByProvider, kind, config.apiKey, config.baseUrl),
    recentSuccess: getProviderRecentWindowStats(
      usageByProvider,
      kind,
      config.apiKey,
      config.baseUrl
    ).success,
    statusData: getProviderRecentStatusData(usageByProvider, kind, config.apiKey, config.baseUrl),
    haystack: buildHaystack([
      label,
      config.apiKey,
      config.prefix,
      config.baseUrl,
      config.proxyUrl,
      config.authIndex,
    ]),
    raw: config,
  } as ProviderRow;
}

function buildOpenAIRow(
  provider: OpenAIProviderConfig,
  originalIndex: number,
  usageByProvider: ProviderRecentUsageMap
): ProviderRow {
  const modelNames = collectModelNames(provider.models);
  const apiKeyEntries = provider.apiKeyEntries ?? [];
  return {
    key: `openai:${getOpenAIProviderKey(provider, originalIndex)}`,
    kind: 'openai',
    originalIndex,
    label: provider.name,
    sortName: provider.name,
    baseUrl: provider.baseUrl ?? '',
    priority: provider.priority,
    modelNames,
    modelCount: modelNames.length,
    keyCount: apiKeyEntries.length,
    enabled: provider.disabled !== true,
    stats: getOpenAIProviderTotalStats(provider, usageByProvider),
    recentSuccess: getOpenAIProviderRecentWindowStats(provider, usageByProvider).success,
    statusData: getOpenAIProviderRecentStatusData(provider, usageByProvider),
    haystack: buildHaystack([
      provider.name,
      provider.prefix,
      provider.baseUrl,
      provider.authIndex,
      ...apiKeyEntries.flatMap((entry) => [entry.apiKey, entry.proxyUrl]),
    ]),
    raw: provider,
  };
}

export function buildProviderRows({
  gemini,
  interactions = [],
  codex,
  xai = [],
  meta = [],
  claude,
  vertex,
  openai,
  usageByProvider,
  providerKeyAliases = [],
}: BuildProviderRowsInput): ProviderRow[] {
  return [
    ...gemini.map((config, index) =>
      buildKeyConfigRow('gemini', config, index, usageByProvider, providerKeyAliases)
    ),
    ...interactions.map((config, index) =>
      buildKeyConfigRow('interactions', config, index, usageByProvider, providerKeyAliases)
    ),
    ...codex.map((config, index) =>
      buildKeyConfigRow('codex', config, index, usageByProvider, providerKeyAliases)
    ),
    ...xai.map((config, index) =>
      buildKeyConfigRow('xai', config, index, usageByProvider, providerKeyAliases)
    ),
    ...meta.map((config, index) =>
      buildKeyConfigRow('meta', config, index, usageByProvider, providerKeyAliases)
    ),
    ...claude.map((config, index) =>
      buildKeyConfigRow('claude', config, index, usageByProvider, providerKeyAliases)
    ),
    ...vertex.map((config, index) =>
      buildKeyConfigRow('vertex', config, index, usageByProvider, providerKeyAliases)
    ),
    ...openai.map((provider, index) => buildOpenAIRow(provider, index, usageByProvider)),
  ];
}
