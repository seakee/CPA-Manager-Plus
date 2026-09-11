/**
 * 覆写树的纯逻辑：把「生效条目 + 覆写补丁」展开成可以逐字段覆写的树。
 *
 * 结构直接对应后端的 JSON Merge Patch（RFC 7386）语义：
 * - 对象逐字段递归合并，所以对象字段能各自继承 / 覆写 / 删除；
 * - 数组整体替换，数组只有一个覆写状态，元素随整个数组一起写入补丁；
 * - 补丁里缺少某个键表示继承生效值，值为 null 表示把该字段从生效条目里删除。
 */

/**
 * 覆写补丁里的保留键：字段级继承指令。它不是条目字段，因此不进字段树。
 * 指令形态见 codexClientModelsInherit。
 */
export const INHERIT_KEY = '$inherit';

export type OverrideNodeKind =
  | 'object'
  | 'array'
  | 'string'
  | 'multiline'
  | 'number'
  | 'boolean'
  | 'empty';

/** 单个字段相对补丁的状态。 */
export type OverrideFieldState = 'inherit' | 'override' | 'removed';

/** 树里的路径段：对象用字段名，数组用下标。 */
export type OverridePath = ReadonlyArray<string | number>;

export interface OverrideTreeNode {
  /** 稳定 id，用于 React 列表与展开状态。 */
  id: string;
  key: string;
  path: OverridePath;
  kind: OverrideNodeKind;
  state: OverrideFieldState;
  /** 当前展示值：已覆写时取补丁值，否则取生效值。 */
  value: unknown;
  /** 生效条目上的值（数组元素为对应下标的值）。 */
  effective: unknown;
  /** 补丁在该路径上的值，state 为 inherit 时为 undefined。 */
  patchValue: unknown;
  inEffective: boolean;
  inPatch: boolean;
  /** 数组元素没有独立的继承状态，状态由所属数组节点持有；界面按只读展示。 */
  insideArray: boolean;
  /** 覆盖本字段的继承来源：自身指令或最近的上级指令，没有继承时为空。 */
  inheritSource: string | null;
  /** 本字段自身声明的继承来源；与 inheritSource 不同时说明来源来自上级。 */
  inheritDeclared: string | null;
  children: OverrideTreeNode[];
}

export const isPlainObject = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value);

/** 深拷贝 JSON 取值，用于把生效值搬进补丁。 */
export function cloneJsonValue<Value>(value: Value): Value {
  if (value === undefined) return value;
  return JSON.parse(JSON.stringify(value)) as Value;
}

/** 依据取值推断编辑控件类型；字符串带换行时按多行文本处理。 */
export const classifyOverrideValue = (value: unknown): OverrideNodeKind => {
  if (value === null || value === undefined) return 'empty';
  if (Array.isArray(value)) return 'array';
  if (isPlainObject(value)) return 'object';
  if (typeof value === 'boolean') return 'boolean';
  if (typeof value === 'number') return 'number';
  if (typeof value === 'string') return value.includes('\n') ? 'multiline' : 'string';
  return 'string';
};

/** 读取补丁在指定路径上的值。 */
export const readOverrideValue = (
  patch: unknown,
  path: OverridePath
): { present: boolean; value: unknown } => {
  let cursor: unknown = patch;
  for (const segment of path) {
    if (Array.isArray(cursor)) {
      if (typeof segment !== 'number' || segment < 0 || segment >= cursor.length) {
        return { present: false, value: undefined };
      }
      cursor = cursor[segment];
      continue;
    }
    if (!isPlainObject(cursor) || !Object.prototype.hasOwnProperty.call(cursor, segment)) {
      return { present: false, value: undefined };
    }
    cursor = cursor[segment as string];
  }
  return { present: true, value: cursor };
};

/**
 * 覆盖某条路径的继承来源：本路径自己的指令优先，否则取最近的上级指令。
 * 字段树与配置面板都用它，因此同一个字段在两种视图里显示的来源始终一致。
 */
export function resolveInheritSource(
  directives: ReadonlyMap<string, string>,
  path: OverridePath
): { source: string | null; declared: string | null } {
  const declared = directives.get(path.join('.')) ?? null;
  if (declared) return { source: declared, declared };

  for (let end = path.length - 1; end >= 0; end -= 1) {
    const ancestor = directives.get(path.slice(0, end).join('.'));
    if (ancestor) return { source: ancestor, declared: null };
  }
  return { source: null, declared: null };
}

/** 数组只有在元素是对象或数组时才展开成子节点；标量数组交给数组编辑器。 */
const arrayHasStructuredItems = (value: unknown): boolean =>
  Array.isArray(value) && value.some((item) => isPlainObject(item) || Array.isArray(item));

const makeNode = (options: {
  key: string;
  path: OverridePath;
  effective: unknown;
  patchValue: unknown;
  inEffective: boolean;
  inPatch: boolean;
  insideArray: boolean;
  inheritSource: string | null;
  inheritDeclared: string | null;
  children: OverrideTreeNode[];
}): OverrideTreeNode => {
  const value = options.inPatch ? options.patchValue : options.effective;
  const state: OverrideFieldState = !options.inPatch
    ? 'inherit'
    : options.patchValue === null
      ? 'removed'
      : 'override';

  return {
    id: options.path.join('.'),
    key: options.key,
    path: options.path,
    kind: classifyOverrideValue(value),
    state,
    value,
    effective: options.effective,
    patchValue: options.patchValue,
    inEffective: options.inEffective,
    inPatch: options.inPatch,
    insideArray: options.insideArray,
    inheritSource: options.inheritSource,
    inheritDeclared: options.inheritDeclared,
    children: options.children,
  };
};

function buildNodes(
  effective: unknown,
  patch: unknown,
  path: OverridePath,
  insideArray: boolean,
  directives: ReadonlyMap<string, string>
): OverrideTreeNode[] {
  if (Array.isArray(effective) || Array.isArray(patch)) {
    const items = Array.isArray(patch) ? patch : Array.isArray(effective) ? effective : [];
    if (!arrayHasStructuredItems(items)) return [];
    return items.map((_item, index) => {
      const childPath: OverridePath = [...path, index];
      const inherited = resolveInheritSource(directives, childPath);
      return makeNode({
        key: `[${index}]`,
        path: childPath,
        effective: Array.isArray(effective) ? effective[index] : undefined,
        patchValue: Array.isArray(patch) ? patch[index] : undefined,
        inEffective: Array.isArray(effective) && index < effective.length,
        inPatch: Array.isArray(patch) && index < patch.length,
        insideArray: true,
        inheritSource: inherited.source,
        inheritDeclared: inherited.declared,
        children: buildNodes(
          Array.isArray(effective) ? effective[index] : undefined,
          Array.isArray(patch) ? patch[index] : undefined,
          childPath,
          true,
          directives
        ),
      });
    });
  }

  const effectiveSource = isPlainObject(effective) ? effective : {};
  const patchSource = isPlainObject(patch) ? patch : {};
  const keys = Object.keys(effectiveSource);
  Object.keys(patchSource).forEach((key) => {
    if (!keys.includes(key)) keys.push(key);
  });

  return (
    keys
      // $inherit 是保留键，不是条目字段，因此不出现在字段树里。
      .filter((key) => key !== INHERIT_KEY)
      .map((key) => {
        const childPath = [...path, key];
        const inEffective = Object.prototype.hasOwnProperty.call(effectiveSource, key);
        const inPatch = Object.prototype.hasOwnProperty.call(patchSource, key);
        const childEffective = effectiveSource[key];
        const childPatch = inPatch ? patchSource[key] : undefined;
        const nestedSource = inPatch ? childPatch : childEffective;
        const hasChildren = isPlainObject(nestedSource) || arrayHasStructuredItems(nestedSource);
        const inherited = resolveInheritSource(directives, childPath);

        return makeNode({
          key,
          path: childPath,
          effective: childEffective,
          patchValue: childPatch,
          inEffective,
          inPatch,
          insideArray,
          inheritSource: inherited.source,
          inheritDeclared: inherited.declared,
          children: hasChildren
            ? buildNodes(childEffective, childPatch, childPath, insideArray, directives)
            : [],
        });
      })
  );
}

/** 构建条目根层级的字段树。 */
export function buildOverrideTree(options: {
  effective: unknown;
  patch: unknown;
  /** `$inherit` 指令，按点号路径索引；缺省表示字段都不带继承来源。 */
  inherit?: ReadonlyMap<string, string>;
}): OverrideTreeNode[] {
  return buildNodes(options.effective, options.patch, [], false, options.inherit ?? new Map());
}

const clonePatch = (patch: unknown): Record<string, unknown> => {
  if (!isPlainObject(patch)) return {};
  return cloneJsonValue(patch);
};

const readContainerChild = (container: unknown, segment: string | number): unknown => {
  if (Array.isArray(container)) return typeof segment === 'number' ? container[segment] : undefined;
  if (isPlainObject(container)) return container[segment as string];
  return undefined;
};

const writeContainerChild = (
  container: unknown,
  segment: string | number,
  value: unknown
): void => {
  if (Array.isArray(container) && typeof segment === 'number') {
    container[segment] = value;
    return;
  }
  if (isPlainObject(container)) {
    container[segment as string] = value;
  }
};

/**
 * 在补丁上写入指定路径的值，必要时补出中间对象。
 * 路径穿过数组时，先把生效值里的数组整体搬进补丁（数组是整体替换的）。
 */
export function setOverrideValue(
  patch: unknown,
  path: OverridePath,
  value: unknown,
  effective?: unknown
): Record<string, unknown> {
  const next = clonePatch(patch);
  if (path.length === 0) return next;

  let container: unknown = next;
  // 与 container 同步推进的生效值，用来在补丁缺少中间层时补齐数组。
  let source: unknown = effective;

  for (let index = 0; index < path.length - 1; index += 1) {
    const segment = path[index];
    // 下一段是下标时这一层必须是数组，否则是对象。
    const childMustBeArray = typeof path[index + 1] === 'number';
    const childEffective = readContainerChild(source, segment);
    let child = readContainerChild(container, segment);

    if (childMustBeArray ? !Array.isArray(child) : !isPlainObject(child)) {
      child =
        childMustBeArray && Array.isArray(childEffective)
          ? cloneJsonValue(childEffective)
          : childMustBeArray
            ? []
            : {};
      writeContainerChild(container, segment, child);
    }

    source = childEffective;
    container = child;
  }

  writeContainerChild(container, path[path.length - 1], value);
  return next;
}

/** 把显式 null 写进补丁，表示把该字段从生效条目里删除。 */
export const setOverrideRemoved = (
  patch: unknown,
  path: OverridePath,
  effective?: unknown
): Record<string, unknown> => setOverrideValue(patch, path, null, effective);

/**
 * 删除补丁中该路径的键，使字段回到继承状态。
 * 顺带清理因此变空的中间对象：空对象在 merge patch 里本来就是无操作。
 */
export function removeOverrideKey(patch: unknown, path: OverridePath): Record<string, unknown> {
  const next = clonePatch(patch);
  if (path.length === 0) return next;

  const ancestors: Array<{ parent: Record<string, unknown>; key: string }> = [];
  let cursor: Record<string, unknown> = next;
  for (let index = 0; index < path.length - 1; index += 1) {
    const segment = path[index] as string;
    const child = cursor[segment];
    if (!isPlainObject(child)) return next;
    ancestors.push({ parent: cursor, key: segment });
    cursor = child;
  }

  delete cursor[path[path.length - 1] as string];

  for (let index = ancestors.length - 1; index >= 0; index -= 1) {
    const { parent, key } = ancestors[index];
    const value = parent[key];
    if (isPlainObject(value) && Object.keys(value).length === 0) {
      delete parent[key];
    }
  }

  return next;
}

/** 单行预览文本；多行字符串保留真实换行，由组件决定如何渲染。 */
export const formatOverridePreview = (value: unknown): string => {
  if (value === undefined) return '';
  if (value === null) return 'null';
  if (typeof value === 'string') return value;
  if (typeof value === 'number' || typeof value === 'boolean') return String(value);
  try {
    return JSON.stringify(value, null, 2) ?? '';
  } catch {
    return '';
  }
};

export const countTextLines = (value: string): number => value.split('\n').length;

/** 补丁里写了多少个取值位置；对象递归计数，数组与标量各算一个。 */
export function countOverrideNodes(patch: unknown): number {
  if (patch === undefined) return 0;
  if (!isPlainObject(patch)) return 1;
  return Object.keys(patch)
    .filter((key) => key !== INHERIT_KEY)
    .reduce((total, key) => total + countOverrideNodes(patch[key]), 0);
}

/**
 * 开始本地覆写时的初值：从生效值复制一份，避免把生效值本身写进补丁。
 * 生效条目里没有这个字段时，给一个与取值类型相符的空值。
 */
export function seedOverrideValue(
  node: Pick<OverrideTreeNode, 'kind' | 'inEffective' | 'effective'>
): unknown {
  const source = node.inEffective ? node.effective : undefined;
  switch (node.kind) {
    case 'array':
      return Array.isArray(source) ? cloneJsonValue(source) : [];
    case 'object':
      return isPlainObject(source) ? cloneJsonValue(source) : {};
    case 'boolean':
      return typeof source === 'boolean' ? source : true;
    case 'number':
      return typeof source === 'number' ? source : 0;
    case 'string':
    case 'multiline':
      return typeof source === 'string' ? source : '';
    default:
      return '';
  }
}

/** 数组元素是否全是字符串：这种数组用「一行一项」的文本框更好用。 */
export const isStringListArray = (value: unknown): value is string[] =>
  Array.isArray(value) && value.every((item) => typeof item === 'string');

export const formatStringList = (values: ReadonlyArray<string>): string => values.join('\n');

export const parseStringList = (text: string): string[] =>
  text
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter((line) => line.length > 0);

/** 收集所有可展开节点的 id，供「展开全部」使用。 */
export function collectExpandableIds(nodes: ReadonlyArray<OverrideTreeNode>): string[] {
  return nodes.flatMap((node) =>
    node.children.length > 0 ? [node.id, ...collectExpandableIds(node.children)] : []
  );
}

export interface OverrideTreeFilterOptions {
  query: string;
  onlyOverridden: boolean;
}

const nodeMatchesQuery = (node: OverrideTreeNode, keyword: string): boolean => {
  if (!keyword) return true;
  return node.id.toLowerCase().includes(keyword) || node.key.toLowerCase().includes(keyword);
};

/**
 * 过滤覆写树：保留命中的节点及其祖先，并裁掉不命中的子孙。
 * 没有任何过滤条件时原样返回，避免无谓的重建。
 */
export function filterOverrideTree(
  nodes: ReadonlyArray<OverrideTreeNode>,
  options: OverrideTreeFilterOptions
): OverrideTreeNode[] {
  const keyword = options.query.trim().toLowerCase();
  const { onlyOverridden } = options;
  if (!keyword && !onlyOverridden) return [...nodes];

  const visit = (
    node: OverrideTreeNode
  ): { node: OverrideTreeNode | null; overridden: boolean } => {
    const visitedChildren = node.children.map(visit);
    const children = visitedChildren
      .map((entry) => entry.node)
      .filter((entry): entry is OverrideTreeNode => entry !== null);
    const overridden =
      node.state !== 'inherit' || visitedChildren.some((entry) => entry.overridden);

    const selfMatched = nodeMatchesQuery(node, keyword);
    if (!(selfMatched || children.length > 0)) return { node: null, overridden };
    if (onlyOverridden && !overridden) return { node: null, overridden };

    const childrenUnchanged =
      children.length === node.children.length &&
      children.every((child, index) => child === node.children[index]);

    return { node: childrenUnchanged ? node : { ...node, children }, overridden };
  };

  return nodes
    .map((node) => visit(node).node)
    .filter((node): node is OverrideTreeNode => node !== null);
}
