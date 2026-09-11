/**
 * 字段级继承的纯逻辑：读写覆写补丁里的 `$inherit` 指令，并按指令还原「不覆写时会取到哪些值」。
 *
 * 指令形态与后端一致：字符串是「整条继承」的简写，对象则是「点号路径 → 模型 slug」的映射，
 * 其中空路径 `""` 表示整条继承。路径只使用对象键，数组整体替换，不能作为继承目标。
 * 同一路径既有继承指令又有本地值时，本地值胜出，所以界面把「继承来源」与「本地改动」分开表达。
 */

import type { useTranslation } from 'react-i18next';
import {
  INHERIT_KEY,
  cloneJsonValue,
  isPlainObject,
  removeOverrideKey,
  type OverridePath,
} from './codexClientModelsTree';

export { INHERIT_KEY };

/** 模型身份字段：只能本地填写，任何继承指令都不允许指向它们。 */
export const NON_INHERITABLE_FIELDS: ReadonlyArray<string> = [
  'slug',
  'display_name',
  'description',
];

/** 字段路径的点号写法；根路径为空字符串。 */
export const inheritPathKey = (path: OverridePath): string => path.join('.');

const pathSegments = (pathKey: string): string[] => (pathKey ? pathKey.split('.') : []);

/** 数组下标位置无法用点号路径表达，因此不能作为继承目标。 */
export const isInheritablePath = (path: OverridePath): boolean =>
  path.every((segment) => typeof segment === 'string');

/** 身份字段不能继承。 */
export const isNonInheritablePathKey = (pathKey: string): boolean =>
  NON_INHERITABLE_FIELDS.includes(pathKey);

/**
 * 读取补丁里的继承指令。结构非法时按「没有指令」处理，
 * 具体问题交给 validateInheritDirectives 报告，避免渲染时反复抛错。
 */
export function readInheritDirectives(patch: unknown): Map<string, string> {
  const directives = new Map<string, string>();
  if (!isPlainObject(patch)) return directives;

  const raw = patch[INHERIT_KEY];
  if (typeof raw === 'string') {
    const slug = raw.trim();
    if (slug) directives.set('', slug);
    return directives;
  }
  if (!isPlainObject(raw)) return directives;

  Object.entries(raw).forEach(([pathKey, value]) => {
    if (typeof value !== 'string') return;
    const slug = value.trim();
    if (slug) directives.set(pathKey, slug);
  });
  return directives;
}

/**
 * 把指令映射写回补丁，并归一化形态：
 * 只有整条继承时写字符串简写，否则写对象，没有指令时删掉该键。
 */
function withInheritDirectives(
  patch: unknown,
  directives: ReadonlyMap<string, string>
): Record<string, unknown> {
  const rest: Record<string, unknown> = isPlainObject(patch) ? { ...patch } : {};
  delete rest[INHERIT_KEY];

  if (directives.size === 0) return rest;
  if (directives.size === 1 && directives.has('')) {
    return { [INHERIT_KEY]: directives.get(''), ...rest };
  }

  const object: Record<string, string> = {};
  [...directives.keys()].sort().forEach((pathKey) => {
    object[pathKey] = directives.get(pathKey) as string;
  });
  return { [INHERIT_KEY]: object, ...rest };
}

/** 让该路径继承指定模型。 */
export function setInheritSource(
  patch: unknown,
  path: OverridePath,
  slug: string
): Record<string, unknown> {
  const directives = readInheritDirectives(patch);
  const slugValue = slug.trim();
  if (slugValue) directives.set(inheritPathKey(path), slugValue);
  return withInheritDirectives(patch, directives);
}

/** 取消该路径自身的继承指令；上级指令仍然生效。 */
export function clearInheritSource(patch: unknown, path: OverridePath): Record<string, unknown> {
  const directives = readInheritDirectives(patch);
  directives.delete(inheritPathKey(path));
  return withInheritDirectives(patch, directives);
}

/** 清除该字段的全部本地设置：本地补丁与自身声明的继承指令。 */
export function clearFieldOverride(patch: unknown, path: OverridePath): Record<string, unknown> {
  return clearInheritSource(removeOverrideKey(patch, path), path);
}

/** 目录里可以作为继承源的模型 slug，按名称排序。 */
export function collectInheritSources(
  models: ReadonlyArray<Record<string, unknown>>,
  excludeSlug?: string
): string[] {
  const slugs = new Set<string>();
  models.forEach((entry) => {
    const slug = typeof entry.slug === 'string' ? entry.slug.trim() : '';
    if (slug && slug !== excludeSlug) slugs.add(slug);
  });
  return [...slugs].sort((left, right) => left.localeCompare(right));
}

/** 每个模型被多少条覆写当作继承源引用；同一条覆写里的重复引用只算一次。 */
export function countInheritUsage(doc: Record<string, unknown>): Map<string, number> {
  const usage = new Map<string, number>();
  Object.values(doc).forEach((patch) => {
    const seen = new Set<string>();
    readInheritDirectives(patch).forEach((slug) => {
      if (seen.has(slug)) return;
      seen.add(slug);
      usage.set(slug, (usage.get(slug) ?? 0) + 1);
    });
  });
  return usage;
}

/** 继承源在某个路径上的取值。 */
export interface InheritSourceValue {
  found: boolean;
  value: unknown;
}

/** 按「模型 slug + 点号路径」取继承源的取值。 */
export type InheritSourceLookup = (slug: string, pathKey: string) => InheritSourceValue;

const asEntry = (merged: unknown): InheritSourceValue =>
  isPlainObject(merged) ? { found: true, value: merged } : { found: false, value: undefined };

const lookupPath = (entry: Record<string, unknown>, pathKey: string): InheritSourceValue => {
  if (!pathKey) return { found: true, value: entry };
  let current: unknown = entry;
  for (const segment of pathSegments(pathKey)) {
    if (!isPlainObject(current) || !Object.prototype.hasOwnProperty.call(current, segment)) {
      return { found: false, value: undefined };
    }
    current = current[segment];
  }
  return { found: true, value: current };
};

/**
 * 用生效目录建立继承源查询。
 *
 * 目录里的条目已经是服务端解析过的结果，因此这里只需按路径取值，
 * 不需要在前端重放整条继承链。
 */
export function createInheritSourceLookup(
  models: ReadonlyArray<Record<string, unknown>>
): InheritSourceLookup {
  const index = new Map<string, Record<string, unknown>>();
  models.forEach((entry) => {
    const slug = typeof entry.slug === 'string' ? entry.slug.trim() : '';
    if (slug && !index.has(slug)) index.set(slug, entry);
  });
  return (slug, pathKey) => {
    const entry = index.get(slug);
    return entry ? lookupPath(entry, pathKey) : { found: false, value: undefined };
  };
}

/** 身份字段永远来自条目自身，继承源不提供。 */
function withIdentityFields(
  source: Record<string, unknown>,
  base: unknown
): Record<string, unknown> {
  const merged = cloneJsonValue(source);
  NON_INHERITABLE_FIELDS.forEach((field) => {
    if (isPlainObject(base) && Object.prototype.hasOwnProperty.call(base, field)) {
      merged[field] = cloneJsonValue(base[field]);
      return;
    }
    delete merged[field];
  });
  return merged;
}

/** 按路径写值与取值的对象级操作；路径只走对象键，中途不是对象就补成对象。 */
function writePath(target: unknown, segments: ReadonlyArray<string>, value: unknown): unknown {
  const root: Record<string, unknown> = isPlainObject(target) ? cloneJsonValue(target) : {};
  let container = root;
  for (let index = 0; index < segments.length - 1; index += 1) {
    const segment = segments[index];
    const next = container[segment];
    if (!isPlainObject(next)) container[segment] = {};
    container = container[segment] as Record<string, unknown>;
  }
  container[segments[segments.length - 1]] = value;
  return root;
}

/** 指令按路径由浅到深应用：深层指令细化浅层已经替换掉的子树。 */
const inheritPathsShallowFirst = (directives: ReadonlyMap<string, string>): string[] =>
  [...directives.keys()].sort((left, right) => {
    const leftDepth = pathSegments(left).length;
    const rightDepth = pathSegments(right).length;
    return leftDepth !== rightDepth ? leftDepth - rightDepth : left.localeCompare(right);
  });

/**
 * 把继承指令叠加到条目上，得到「这些字段不写本地值时」的取值。
 *
 * 界面用它来预览与播种：字段一旦选了继承源，展示的就不再是自身条目里的旧值，
 * 而是来源模型的取值。来源缺少该字段时保留原值，问题交给 validateInheritDirectives 提示。
 */
export function applyInheritDirectives(
  base: unknown,
  directives: ReadonlyMap<string, string>,
  lookup: InheritSourceLookup
): unknown {
  if (directives.size === 0) return base;

  let current = base;
  inheritPathsShallowFirst(directives).forEach((pathKey) => {
    const slug = directives.get(pathKey) as string;
    const source = lookup(slug, pathKey);
    if (!source.found) return;

    if (!pathKey) {
      const entry = asEntry(source.value);
      if (entry.found) current = withIdentityFields(entry.value as Record<string, unknown>, base);
      return;
    }
    current = writePath(current, pathSegments(pathKey), cloneJsonValue(source.value));
  });
  return current;
}

/**
 * 界面里逐字段的继承来源与操作。配置面板与字段树共用同一套，
 * 因此同一个字段在两种视图里给出的状态、可选项与结果完全一致。
 */
export interface FieldInheritBinding {
  /** 覆写补丁里的继承指令，按点号路径索引。 */
  directives: ReadonlyMap<string, string>;
  /** 可作为继承源的模型 slug。 */
  sources: ReadonlyArray<string>;
  /** 该路径上继承指令的问题描述，供字段旁的警告标签显示。 */
  issueOf: (path: OverridePath) => string | undefined;
  /** 清除本地改动与自身声明的继承指令。 */
  clear: (path: OverridePath) => void;
  /** 让该路径改为继承指定模型，同时丢掉本地值。 */
  inherit: (path: OverridePath, slug: string) => void;
  /** 写入 null，把字段从生效条目里删除。 */
  remove: (path: OverridePath) => void;
}

/** 该路径能不能选继承源：路径必须是纯对象键，且不是身份字段。 */
export const canInheritPath = (path: OverridePath): boolean =>
  isInheritablePath(path) && !isNonInheritablePathKey(inheritPathKey(path));

export type InheritIssueCode =
  | 'invalid_shape'
  | 'invalid_path'
  | 'non_inheritable_field'
  | 'unknown_source'
  | 'self_reference'
  | 'source_missing_path';

export interface InheritIssue {
  /** 出问题的路径；指向整条条目的问题为空字符串。 */
  path: string;
  code: InheritIssueCode;
}

/** 检查路径是否符合后端的点号写法。 */
function pathIssue(pathKey: string): InheritIssueCode | null {
  if (pathKey.includes('[') || pathKey.includes(']')) return 'invalid_path';
  const segments = pathSegments(pathKey);
  if (segments.some((segment) => segment === '' || segment === INHERIT_KEY)) {
    return 'invalid_path';
  }
  if (isNonInheritablePathKey(pathKey)) return 'non_inheritable_field';
  return null;
}

/**
 * 校验补丁里的继承指令。界面用它给字段标出「这条指令后端不会接受」，
 * 例如手写 JSON 时指向了不存在的模型、身份字段，或来源没有这个字段。
 *
 * 指向整条条目的问题用空路径表示，界面放在编辑器顶部提示。
 */
export function validateInheritDirectives(options: {
  slug: string;
  patch: unknown;
  catalog: ReadonlyArray<Record<string, unknown>>;
}): InheritIssue[] {
  const { slug, patch, catalog } = options;
  if (!isPlainObject(patch)) return [];
  const raw = patch[INHERIT_KEY];
  if (raw === undefined || raw === null) return [];

  const entries: Array<[string, unknown]> =
    typeof raw === 'string' ? [['', raw]] : isPlainObject(raw) ? Object.entries(raw) : [];
  if (entries.length === 0) return [{ path: '', code: 'invalid_shape' }];

  const lookup = createInheritSourceLookup(catalog);
  const knownSlugs = new Set(catalog.map((entry) => entry.slug).filter(isString));

  const issues: InheritIssue[] = [];
  entries.forEach(([pathKey, value]) => {
    if (typeof value !== 'string' || !value.trim()) {
      issues.push({ path: pathKey, code: 'invalid_shape' });
      return;
    }
    const issue = pathIssue(pathKey);
    if (issue) {
      issues.push({ path: pathKey, code: issue });
      return;
    }
    const source = value.trim();
    if (source === slug) {
      issues.push({ path: pathKey, code: 'self_reference' });
      return;
    }
    if (!knownSlugs.has(source)) {
      issues.push({ path: pathKey, code: 'unknown_source' });
      return;
    }
    if (pathKey && !lookup(source, pathKey).found) {
      issues.push({ path: pathKey, code: 'source_missing_path' });
    }
  });
  return issues;
}

const isString = (value: unknown): value is string => typeof value === 'string';

type Translate = ReturnType<typeof useTranslation>['t'];

/** 继承指令问题的提示文案。 */
export const describeInheritIssue = (code: InheritIssueCode, t: Translate): string =>
  t(`codex_client_models.inherit_issue_${code}`);
