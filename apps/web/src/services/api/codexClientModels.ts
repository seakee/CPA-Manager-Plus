/**
 * Codex 客户端模型目录 API（/codex-client-models）
 *
 * 目录由 base（内置或远端）与本地覆写层组成。覆写层是 JSON Merge Patch（RFC 7386），
 * 顶层键为模型 slug，值为该 slug 的字段补丁。
 */

import { apiClient } from './client';

export type CodexClientModelOrigin = 'base' | 'override' | 'custom';

export type CodexClientModelEntry = Record<string, unknown>;

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
}

const ORIGINS: CodexClientModelOrigin[] = ['base', 'override', 'custom'];

const isRecord = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value);

const readString = (value: unknown): string => (typeof value === 'string' ? value : '');

const readNumber = (value: unknown): number => {
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : 0;
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
  };
}

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
