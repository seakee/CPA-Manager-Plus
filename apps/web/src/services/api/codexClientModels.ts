/**
 * Codex 客户端模型目录 API（/codex-client-models）
 *
 * 目录由 base（内置或远端）与本地覆写层组成。覆写层是 JSON Merge Patch（RFC 7386），
 * 顶层键为模型 slug，值为该 slug 的字段补丁。
 *
 * models 是服务端为每个可服务模型装配出的默认条目：不写覆写时客户端收到的就是它们。
 * 覆写层叠在装配结果之上，因此任何字段都能覆写，界面上「默认值」指的就是这里的取值。
 */

import { apiClient } from './client';

export type CodexClientModelOrigin = 'base' | 'override' | 'served' | 'removed' | 'unserved';

export type CodexClientModelEntry = Record<string, unknown>;

/**
 * 服务端下发给 Codex 客户端的单个模型摘要。
 *
 * served_models 描述客户端真正能请求到的模型：装配结果再叠上覆写层之后的样子，
 * 附带来源、模板与优先级等列表信息。
 */
export interface CodexClientServedModel {
  /** 客户端请求该模型时使用的标识。 */
  slug: string;
  /** 该模型当前生效条目所基于的目录条目；没有专属条目时是默认模板。 */
  templateSlug: string;
  /** 该模型没有专属目录条目，正在复用默认模板。 */
  defaultTemplate: boolean;
  /** 当前提供该模型的服务端标识。 */
  providers: string[];
  displayName: string;
  description: string;
  contextWindow: number | null;
  maxContextWindow: number | null;
  visibility: string;
  reasoningLevel: string;
  /** 该模型下发时提供的推理强度，顺序与下发内容一致。 */
  reasoningLevels: CodexClientServedReasoningLevel[];
  /** 该模型当前的下发位置；排序在目录条目之后。 */
  priority: number | null;
}

/** 服务端下发给客户端的一个推理强度。 */
export interface CodexClientServedReasoningLevel {
  /** 客户端请求该强度时使用的名称，例如 high。 */
  effort: string;
  /** 该强度的说明文字；下发内容可能省略。 */
  description: string;
}
/** 一条未能生效的覆写：目录保留基线条目，自定义条目则不会出现在目录里。 */
export interface CodexClientModelOverrideError {
  slug: string;
  /** 出问题的字段路径；整条条目出问题时为空。 */
  path: string;
  error: string;
}

export interface CodexClientModelsState {
  /** base 目录来源：embed 或来源 URL。 */
  source: string;
  /** 生效目录内容变化时递增的修订号。 */
  revision: number;
  /** 下发到 Codex 客户端的生效条目。 */
  models: CodexClientModelEntry[];
  /** 生效 slug 的来源标记。 */
  origins: Record<string, CodexClientModelOrigin>;
  /** 本地覆写文档，键为 slug。 */
  override: Record<string, unknown>;
  /** 覆写文件位置，未知时为空字符串。 */
  overridePath: string;
  /** 磁盘上的覆写文件被拒绝时的原因。 */
  overrideError: string;
  /** 逐条降级的原因：文件可解析，但其中某几条覆写没能生效。 */
  overrideErrors: CodexClientModelOverrideError[];
  /** 当前下发给客户端的模型摘要；服务端未提供该信息时为 null。 */
  servedModels: CodexClientServedModel[] | null;
}

const ORIGINS: CodexClientModelOrigin[] = ['base', 'override', 'served', 'removed', 'unserved'];

const isRecord = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value);

const readString = (value: unknown): string => (typeof value === 'string' ? value : '');

const readNumber = (value: unknown): number => {
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : 0;
};

const readReasoningLevels = (value: unknown): CodexClientServedReasoningLevel[] =>
  Array.isArray(value)
    ? value
        .filter(isRecord)
        .map((level) => ({
          effort: readString(level.effort).trim(),
          description: readString(level.description),
        }))
        .filter((level) => level.effort.length > 0)
    : [];

const readPositiveNumber = (value: unknown): number | null => {
  const parsed = Number(value);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : null;
};

export function normalizeOrigin(value: unknown): CodexClientModelOrigin {
  return typeof value === 'string' && (ORIGINS as string[]).includes(value)
    ? (value as CodexClientModelOrigin)
    : 'base';
}

export function normalizeCodexClientModelsState(raw: unknown): CodexClientModelsState {
  const record = isRecord(raw) ? raw : {};
  const models = Array.isArray(record.models) ? record.models.filter(isRecord) : [];
  const originsSource = isRecord(record.origins) ? record.origins : {};
  const origins: Record<string, CodexClientModelOrigin> = {};
  for (const [slug, origin] of Object.entries(originsSource)) {
    origins[slug] = normalizeOrigin(origin);
  }

  const overrideErrors = Array.isArray(record.override_errors)
    ? record.override_errors
        .filter(isRecord)
        .map((issue) => ({
          slug: readString(issue.slug),
          path: readString(issue.path),
          error: readString(issue.error),
        }))
        .filter((issue) => issue.slug.length > 0 && issue.error.length > 0)
    : [];

  return {
    source: readString(record.source),
    revision: readNumber(record.revision),
    models,
    origins,
    override: isRecord(record.override) ? { ...record.override } : {},
    overridePath: readString(record.override_path),
    overrideError: readString(record.override_error),
    overrideErrors,
    servedModels: normalizeServedModels(record.served_models),
  };
}

const normalizeServedModels = (value: unknown): CodexClientServedModel[] | null => {
  if (!Array.isArray(value)) return null;
  return value
    .filter(isRecord)
    .map((entry) => ({
      slug: readString(entry.slug).trim(),
      templateSlug: readString(entry.template_slug).trim(),
      defaultTemplate: entry.default_template === true,
      providers: Array.isArray(entry.providers)
        ? entry.providers.filter((provider): provider is string => typeof provider === 'string')
        : [],
      displayName: readString(entry.display_name),
      description: readString(entry.description),
      contextWindow: readPositiveNumber(entry.context_window),
      maxContextWindow: readPositiveNumber(entry.max_context_window),
      visibility: readString(entry.visibility),
      reasoningLevel: readString(entry.default_reasoning_level),
      reasoningLevels: readReasoningLevels(entry.supported_reasoning_levels),
      priority: Number.isFinite(Number(entry.priority)) ? Number(entry.priority) : null,
    }))
    .filter((entry) => entry.slug.length > 0);
};

const overrideEndpoint = (slug: string) =>
  `/codex-client-models/override/${encodeURIComponent(slug)}`;

export const codexClientModelsApi = {
  async getState(signal?: AbortSignal): Promise<CodexClientModelsState> {
    const raw = await apiClient.get('/codex-client-models', { signal });
    return normalizeCodexClientModelsState(raw);
  },

  /** 整体替换本地覆写文档；空对象等同于清空覆写。 */
  async replaceOverride(document: Record<string, unknown>): Promise<CodexClientModelsState> {
    const raw = await apiClient.put('/codex-client-models/override', document);
    return normalizeCodexClientModelsState(raw);
  },

  /** 清空本地覆写文档及其文件。 */
  async clearOverride(): Promise<CodexClientModelsState> {
    const raw = await apiClient.delete('/codex-client-models/override');
    return normalizeCodexClientModelsState(raw);
  },

  /** 替换单个 slug 的覆写补丁，其他 slug 保持不变。 */
  async setOverrideEntry(slug: string, patch: unknown): Promise<CodexClientModelsState> {
    const raw = await apiClient.put(overrideEndpoint(slug), patch);
    return normalizeCodexClientModelsState(raw);
  },

  /** 移除单个 slug 的覆写补丁。 */
  async deleteOverrideEntry(slug: string): Promise<CodexClientModelsState> {
    const raw = await apiClient.delete(overrideEndpoint(slug));
    return normalizeCodexClientModelsState(raw);
  },
};
