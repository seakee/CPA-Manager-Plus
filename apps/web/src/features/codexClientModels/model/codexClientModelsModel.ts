/**
 * Codex 客户端模型目录页面的纯逻辑：行构建、筛选与覆写补丁解析。
 */

import type {
  CodexClientModelEntry,
  CodexClientModelOrigin,
  CodexClientModelsState,
} from '@/services/api/codexClientModels';

/** 页面行状态：目录来源标记之外，额外区分被 null 补丁删除的条目。 */
export type CodexClientModelRowState = CodexClientModelOrigin | 'removed';

/** 新增条目时默认看向的官方模板 slug。 */
export const DEFAULT_MODEL_TEMPLATE_SLUG = 'gpt-5.5';

export interface CodexClientModelRow {
  slug: string;
  displayName: string;
  origin: CodexClientModelRowState;
  contextWindow: number | null;
  visibility: string;
  reasoningLevel: string;
  /** 生效条目；被 null 补丁删除或缺少条目时为空。 */
  entry: CodexClientModelEntry | null;
  /** 覆写文档中的原始补丁值，未覆写时为 undefined。 */
  patch: unknown;
  hasOverride: boolean;
}

export type CodexClientModelFilter = 'all' | CodexClientModelRowState;

export const CODEX_CLIENT_MODEL_FILTERS: ReadonlyArray<CodexClientModelFilter> = [
  'all',
  'override',
  'custom',
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
  state: CodexClientModelsState
): CodexClientModelRow => {
  const hasOverride = Object.prototype.hasOwnProperty.call(state.override, slug);
  const patch = hasOverride ? state.override[slug] : undefined;
  const origin: CodexClientModelRowState = entry
    ? state.origins[slug] || 'base'
    : patch === null
      ? 'removed'
      : 'custom';

  return {
    slug,
    displayName: readString(entry?.display_name) || slug,
    origin,
    contextWindow: readNumber(entry?.context_window),
    visibility: readString(entry?.visibility),
    reasoningLevel: readString(entry?.default_reasoning_level),
    entry,
    patch,
    hasOverride,
  };
};

/**
 * 构建页面行：以生效目录顺序为主，并补上只在覆写文档中出现的 slug
 * （例如被 null 补丁删除、因而不在生效目录里的条目）。
 */
export function buildCodexClientModelRows(state: CodexClientModelsState): CodexClientModelRow[] {
  const rows: CodexClientModelRow[] = [];
  const seen = new Set<string>();

  state.models.forEach((entry) => {
    const slug = readModelSlug(entry);
    if (!slug || seen.has(slug)) return;
    seen.add(slug);
    rows.push(buildRow(slug, entry, state));
  });

  Object.keys(state.override)
    .filter((slug) => !seen.has(slug))
    .sort((a, b) => a.localeCompare(b))
    .forEach((slug) => {
      seen.add(slug);
      rows.push(buildRow(slug, null, state));
    });

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
    custom: 0,
    removed: 0,
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
 * 新增条目的默认继承源条目：优先官方模板，其次是目录里的第一条。
 * 目录为空时返回 null，此时新条目只能完全本地填写。
 */
export function resolveDefaultInheritEntry(
  models: ReadonlyArray<CodexClientModelEntry>
): CodexClientModelEntry | null {
  const entries = models.filter((entry) => readModelSlug(entry).length > 0);
  return (
    entries.find((entry) => readModelSlug(entry) === DEFAULT_MODEL_TEMPLATE_SLUG) ??
    entries[0] ??
    null
  );
}

/** 新增条目的默认继承源 slug；目录为空时返回空串。 */
export const resolveDefaultInheritSource = (models: ReadonlyArray<CodexClientModelEntry>): string =>
  readModelSlug(resolveDefaultInheritEntry(models));

/**
 * 新增条目的补丁：整条继承自来源模型，只把身份字段留给自己填。
 * 比整份复制模板短得多，来源模型更新时新条目也跟着更新。
 *
 * 身份字段不会从继承源取值，而目录校验要求 description 非空，
 * 因此这里从来源条目抄一份初值，用户再改成自己想要的说法。
 */
export function buildInheritedModelPatch(
  source: CodexClientModelEntry | null,
  slug: string
): Record<string, unknown> {
  const normalizedSlug = slug.trim();
  const patch: Record<string, unknown> = { slug: normalizedSlug, display_name: normalizedSlug };
  const description = readString(source?.description).trim();
  if (description) patch.description = description;
  const sourceSlug = readModelSlug(source);
  return sourceSlug ? { $inherit: sourceSlug, ...patch } : patch;
}

/**
 * 以模板条目整份补全新条目，仅替换 slug 与展示名。
 * 只在服务端不支持字段继承时使用：老版本会把整份副本当成普通补丁。
 */
export function buildModelPatchFromTemplate(
  template: CodexClientModelEntry | null,
  slug: string
): Record<string, unknown> {
  const normalizedSlug = slug.trim();
  if (!template) {
    return { slug: normalizedSlug };
  }

  return {
    ...template,
    slug: normalizedSlug,
    display_name: normalizedSlug,
  };
}
