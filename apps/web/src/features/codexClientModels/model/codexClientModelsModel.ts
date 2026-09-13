/**
 * Codex 客户端模型目录页面的纯逻辑：行构建、筛选与覆写补丁解析。
 */

import type {
  CodexClientModelEntry,
  CodexClientModelOrigin,
  CodexClientModelsState,
  CodexClientServedModel,
} from '@/services/api/codexClientModels';

/**
 * 页面行状态就是服务端给出的来源标记：base 与 served 都没有本地覆写，override 已经改过条目内
 * 字段，removed 被 null 补丁隐藏，unserved 是当前没有下发、因而不会生效的覆写。
 */
export type CodexClientModelRowState = CodexClientModelOrigin;

export interface CodexClientModelRow {
  slug: string;
  displayName: string;
  origin: CodexClientModelRowState;
  contextWindow: number | null;
  visibility: string;
  reasoningLevel: string;
  /** 服务端装配出的默认条目，也是编辑器的默认值基线；目录里没有该 slug 时为 null。 */
  entry: CodexClientModelEntry | null;
  /** 覆写文档中的原始补丁值，未覆写时为 undefined。 */
  patch: unknown;
  hasOverride: boolean;
  /** 服务端下发的摘要；null 表示该模型当前没有下发给客户端。 */
  served: CodexClientServedModel | null;
}

export type CodexClientModelFilter = 'all' | CodexClientModelRowState;

export const CODEX_CLIENT_MODEL_FILTERS: ReadonlyArray<CodexClientModelFilter> = [
  'all',
  'served',
  'override',
  'unserved',
  'removed',
  'base',
];

const readString = (value: unknown): string => (typeof value === 'string' ? value : '');

const readNumber = (value: unknown): number | null => {
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : null;
};

export const readModelSlug = (entry: CodexClientModelEntry | null): string =>
  readString(entry?.slug).trim();

const buildRow = (
  slug: string,
  entry: CodexClientModelEntry | null,
  state: CodexClientModelsState,
  served: CodexClientServedModel | null
): CodexClientModelRow => {
  const hasOverride = Object.prototype.hasOwnProperty.call(state.override, slug);
  const patch = hasOverride ? state.override[slug] : undefined;
  // 来源由服务端给出：基础、自动装配、已覆写、已隐藏，以及没有下发因而不生效的覆写。
  // 缺少标记时按目录条目与否兜底，旧接口下页面仍然可用。
  const origin: CodexClientModelRowState = state.origins[slug] ?? (entry ? 'base' : 'unserved');

  // 列表上的值是客户端实际拿到的值。服务端摘要是装配结果叠好覆写层之后的条目，因此摘要
  // 优先于默认条目：编辑器拿默认条目当基线，列表则要跟着覆写变。没有下发的模型没有摘要，
  // 退回默认条目。
  const displayName = served ? served.displayName : readString(entry?.display_name);

  return {
    slug,
    displayName: displayName || slug,
    origin,
    contextWindow: served ? served.contextWindow : readNumber(entry?.context_window),
    visibility: served ? served.visibility : readString(entry?.visibility),
    reasoningLevel: served ? served.reasoningLevel : readString(entry?.default_reasoning_level),
    entry,
    patch,
    hasOverride,
    served,
  };
};

/**
 * 构建页面行：先按服务端下发的顺序列出客户端能看到的模型，再补上目录里其余条目，
 * 最后补上只在覆写文档中出现的 slug（例如被 null 补丁删除的条目）。
 */
export function buildCodexClientModelRows(state: CodexClientModelsState): CodexClientModelRow[] {
  const rows: CodexClientModelRow[] = [];
  const seen = new Set<string>();
  const servedBySlug = new Map<string, CodexClientServedModel>();
  (state.servedModels ?? []).forEach((served) => {
    const slug = served.slug.trim();
    if (slug && !servedBySlug.has(slug)) servedBySlug.set(slug, served);
  });

  const push = (slug: string, entry: CodexClientModelEntry | null) => {
    if (!slug || seen.has(slug)) return;
    seen.add(slug);
    rows.push(buildRow(slug, entry, state, servedBySlug.get(slug) ?? null));
  };

  (state.servedModels ?? []).forEach((served) => {
    push(served.slug.trim(), findModelEntry(state.models, served.slug));
  });
  state.models.forEach((entry) => push(readModelSlug(entry), entry));
  Object.keys(state.override)
    .sort((a, b) => a.localeCompare(b))
    .forEach((slug) => push(slug, null));

  return rows;
}

export function filterCodexClientModelRows(
  rows: ReadonlyArray<CodexClientModelRow>,
  filter: CodexClientModelFilter,
  query: string
): CodexClientModelRow[] {
  const keyword = query.trim().toLowerCase();
  return rows.filter((row) => {
    if (filter !== 'all' && row.origin !== filter) return false;
    if (!keyword) return true;
    return (
      row.slug.toLowerCase().includes(keyword) || row.displayName.toLowerCase().includes(keyword)
    );
  });
}

export function countCodexClientModelRows(
  rows: ReadonlyArray<CodexClientModelRow>
): Record<CodexClientModelFilter, number> {
  const counts: Record<CodexClientModelFilter, number> = {
    all: rows.length,
    base: 0,
    override: 0,
    unserved: 0,
    removed: 0,
    served: 0,
  };
  rows.forEach((row) => {
    counts[row.origin] += 1;
  });
  return counts;
}

export type PatchParseError = 'empty' | 'invalid_json' | 'not_object';

export type PatchParseResult =
  | { ok: true; patch: Record<string, unknown> | null }
  | { ok: false; error: PatchParseError; detail?: string };

/**
 * 解析单个 slug 的覆写补丁输入。补丁必须是 JSON 对象，或 null
 * （表示把该条目从生效目录中删除）。
 */
export function parseOverridePatchInput(text: string): PatchParseResult {
  const trimmed = text.trim();
  if (!trimmed) {
    return { ok: false, error: 'empty' };
  }

  let parsed: unknown;
  try {
    parsed = JSON.parse(trimmed);
  } catch (error) {
    return {
      ok: false,
      error: 'invalid_json',
      detail: error instanceof Error ? error.message : undefined,
    };
  }

  if (parsed === null) return { ok: true, patch: null };
  if (typeof parsed !== 'object' || Array.isArray(parsed)) {
    return { ok: false, error: 'not_object' };
  }

  return { ok: true, patch: parsed as Record<string, unknown> };
}

export function formatOverridePatch(patch: unknown): string {
  if (patch === undefined) return '{}';
  return JSON.stringify(patch, null, 2) ?? 'null';
}

/** 在生效目录里按 slug 取条目。 */
export function findModelEntry(
  models: ReadonlyArray<CodexClientModelEntry>,
  slug: string
): CodexClientModelEntry | null {
  const entry = models.find((candidate) => readModelSlug(candidate) === slug.trim());
  return entry ?? null;
}
/**
 * 本地条目的起步补丁：只声明自己的 slug。
 *
 * 字段取值的基准是服务端装配出的默认条目，覆写层叠在它之上，因此新条目不需要先抄一份
 * 上游配置：改到哪个字段，哪个字段才成为覆写。
 */
export function buildModelIdentityPatch(slug: string): Record<string, unknown> {
  return { slug: slug.trim() };
}
