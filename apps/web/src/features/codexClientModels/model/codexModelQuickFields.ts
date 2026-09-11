/**
 * 常用字段的纯逻辑：把「生效条目 + 覆写补丁」整理成配置面板可以直接渲染的分组与状态。
 *
 * 这里只管「哪些字段常用、什么控件类型、选项从哪来」，控件本身在
 * CodexModelQuickFields 里渲染；逐字段的继承 / 覆写 / 删除语义与字段树共用同一套。
 * 字段用路径而不是键描述，因此提示词这类嵌在 model_messages 里的配置也能直接编辑；
 * 分组字段的子项不写死在描述里，而是按目录与补丁里真实出现的键递归展开。
 */

import {
  isPlainObject,
  isStringListArray,
  resolveInheritSource,
  type OverrideFieldState,
  type OverridePath,
  type OverrideTreeNode,
} from './codexClientModelsTree';
import { formatArrayText } from './draftValues';

/** 常用字段的控件类型；分组字段的子项由目录内容决定，因此没有固定路径。 */
export type QuickFieldKind =
  | 'text'
  | 'multiline'
  | 'number'
  | 'boolean'
  | 'select'
  | 'string-list'
  | 'array'
  | 'group';

export interface QuickFieldDescriptor {
  /** 从条目根算起的字段路径；分组子项可能落在数组下标上。 */
  path: OverridePath;
  kind: QuickFieldKind;
  /** 多行提示词这类长文本占满整行。 */
  wide?: boolean;
  /** 同一个联动的路径可以合并成一个输入框同时覆写。 */
  link?: string;
  /** 分组字段默认展开，打开面板就能看到第一层子项。 */
  defaultOpen?: boolean;
}

/** 可以合并编辑的一组字段。 */
export interface QuickLinkDescriptor {
  id: string;
  /** 参与联动的字段路径，按显示顺序排列。 */
  paths: ReadonlyArray<ReadonlyArray<string>>;
}

/**
 * 内容往往一致、需要一起改写的字段。
 * 基础提示词与指令模板在目录里通常只是占位变量的差别，分开改容易漏掉一处。
 */
export const QUICK_FIELD_LINKS: ReadonlyArray<QuickLinkDescriptor> = [
  {
    id: 'instructions',
    paths: [['base_instructions'], ['model_messages', 'instructions_template']],
  },
];

export type QuickSectionId = 'basic' | 'context' | 'capability' | 'tools' | 'prompt';

export interface QuickSectionDescriptor {
  id: QuickSectionId;
  fields: ReadonlyArray<QuickFieldDescriptor>;
}

/** 配置面板里直接可见的常用字段；其余字段留在高级字段树里。 */
export const QUICK_FIELD_SECTIONS: ReadonlyArray<QuickSectionDescriptor> = [
  {
    id: 'basic',
    fields: [
      { path: ['display_name'], kind: 'text' },
      { path: ['description'], kind: 'text' },
      { path: ['visibility'], kind: 'select' },
      { path: ['priority'], kind: 'number' },
    ],
  },
  {
    id: 'context',
    fields: [
      { path: ['context_window'], kind: 'number' },
      { path: ['max_context_window'], kind: 'number' },
      { path: ['default_reasoning_level'], kind: 'select' },
      { path: ['default_reasoning_summary'], kind: 'select' },
      { path: ['default_verbosity'], kind: 'select' },
      { path: ['support_verbosity'], kind: 'boolean' },
    ],
  },
  {
    id: 'capability',
    fields: [
      { path: ['supported_in_api'], kind: 'boolean' },
      { path: ['supports_parallel_tool_calls'], kind: 'boolean' },
      { path: ['supports_reasoning_summaries'], kind: 'boolean' },
      { path: ['supports_search_tool'], kind: 'boolean' },
      { path: ['prefer_websockets'], kind: 'boolean' },
      { path: ['use_responses_lite'], kind: 'boolean' },
    ],
  },
  {
    id: 'tools',
    fields: [
      { path: ['input_modalities'], kind: 'string-list' },
      { path: ['apply_patch_tool_type'], kind: 'select' },
      { path: ['shell_type'], kind: 'select' },
      { path: ['web_search_tool_type'], kind: 'select' },
      { path: ['multi_agent_version'], kind: 'select' },
      { path: ['multi_agent_reasoning_effort'], kind: 'text' },
    ],
  },
  {
    id: 'prompt',
    fields: [
      { path: ['base_instructions'], kind: 'multiline', wide: true, link: 'instructions' },
      // model_messages 整棵子树都在面板里编辑，子项按目录内容多级展开。
      {
        path: ['model_messages'],
        kind: 'group',
        wide: true,
        defaultOpen: true,
      },
    ],
  },
];

const QUICK_FIELD_DESCRIPTORS: ReadonlyArray<QuickFieldDescriptor> = QUICK_FIELD_SECTIONS.flatMap(
  (section) => section.fields
);

/** 字段路径的点号写法，同时用作 i18n 键与视图查询键。 */
export const quickFieldKey = (path: OverridePath): string => path.join('.');

/**
 * 面板要展示状态的路径：声明的常用字段，加上联动组里由面板负责的那几条路径。
 * 联动组的路径不在字段列表里，但状态同样要从字段树上读。
 */
const QUICK_VIEW_PATHS: ReadonlyArray<OverridePath> = [
  ...QUICK_FIELD_DESCRIPTORS.map((field) => field.path),
  ...QUICK_FIELD_LINKS.flatMap((link) => link.paths),
];

/** 由配置面板接管的字段路径，其余字段留在高级字段树里。 */
export const QUICK_FIELD_KEYS: ReadonlySet<string> = new Set(QUICK_VIEW_PATHS.map(quickFieldKey));

const readPathValue = (source: unknown, path: OverridePath): unknown => {
  let cursor: unknown = source;
  for (const segment of path) {
    if (!isPlainObject(cursor)) return undefined;
    cursor = cursor[segment];
  }
  return cursor;
};
/** 需要从现有目录里收集候选值的字段。 */
const ENUM_FIELD_KEYS: ReadonlyArray<OverridePath> = QUICK_FIELD_DESCRIPTORS.filter(
  (field) => field.kind === 'select'
).map((field) => field.path);

/**
 * 由编辑器顶部管理的键：slug 既是覆写文档的键，也是条目里的标识，
 * 在字段树里改写它只会让两者不一致，所以不放进可编辑字段。
 */
export const EDITOR_MANAGED_FIELD_KEYS: ReadonlySet<string> = new Set(['slug']);

/**
 * 从生效目录收集枚举字段的候选值。
 * 候选值全部来自真实条目，避免把不存在的取值当成合法选项。
 */
export function collectCatalogFieldOptions(
  models: ReadonlyArray<Record<string, unknown>>
): Map<string, string[]> {
  const collected = new Map<string, Set<string>>();
  ENUM_FIELD_KEYS.forEach((path) => collected.set(quickFieldKey(path), new Set()));

  models.forEach((entry) => {
    ENUM_FIELD_KEYS.forEach((path) => {
      const value = readPathValue(entry, path);
      if (typeof value === 'string' && value) collected.get(quickFieldKey(path))?.add(value);
    });
  });

  const options = new Map<string, string[]>();
  collected.forEach((values, key) => {
    options.set(
      key,
      [...values].sort((left, right) => left.localeCompare(right))
    );
  });
  return options;
}

/** 单个常用字段的展示状态。 */
export interface QuickFieldView {
  state: OverrideFieldState;
  /** 展示值：已覆写时取补丁值，否则取生效值。 */
  value: unknown;
  effective: unknown;
  inEffective: boolean;
  inPatch: boolean;
  /** 覆盖本字段的继承来源：自身指令或最近的上级指令。 */
  source: string | null;
  /** 本字段自身声明的继承来源。 */
  declared: string | null;
}

export const EMPTY_QUICK_FIELD_VIEW: QuickFieldView = {
  state: 'inherit',
  value: undefined,
  effective: undefined,
  inEffective: false,
  inPatch: false,
  source: null,
  declared: null,
};

/** 把字段树节点转成面板用的展示状态。 */
export const quickFieldViewOf = (node: OverrideTreeNode): QuickFieldView => ({
  state: node.state,
  value: node.value,
  effective: node.effective,
  inEffective: node.inEffective,
  inPatch: node.inPatch,
  source: node.inheritSource,
  declared: node.inheritDeclared,
});

/** 按路径在字段树里定位节点；只按对象键匹配，数组下标不是常用字段。 */
export const findQuickTreeNode = (
  nodes: ReadonlyArray<OverrideTreeNode>,
  path: OverridePath
): OverrideTreeNode | undefined => {
  let level = nodes;
  let found: OverrideTreeNode | undefined;
  for (const segment of path) {
    found = level.find((node) => node.key === segment);
    if (!found) return undefined;
    level = found.children;
  }
  return found;
};

/**
 * 按字段路径汇总常用字段的状态；条目里没有的字段回落到「继承」。
 *
 * 字段树里没有这个路径时，继承来源仍要从指令里读出来：
 * 例如只写了 model_messages 的指令时，条目里还没有对应的子键。
 */
export function buildQuickFieldViews(
  nodes: ReadonlyArray<OverrideTreeNode>,
  directives?: ReadonlyMap<string, string>
): Map<string, QuickFieldView> {
  const views = new Map<string, QuickFieldView>();
  QUICK_VIEW_PATHS.forEach((path) => {
    const node = findQuickTreeNode(nodes, path);
    if (node) {
      views.set(quickFieldKey(path), quickFieldViewOf(node));
      return;
    }
    const inherited = directives
      ? resolveInheritSource(directives, path)
      : { source: null, declared: null };
    views.set(quickFieldKey(path), { ...EMPTY_QUICK_FIELD_VIEW, ...inherited });
  });
  return views;
}

/** 分组字段的子项：字段描述加上它在字段树里的节点。 */
export interface QuickGroupChild {
  node: OverrideTreeNode;
  field: QuickFieldDescriptor;
}

/** 字段树节点到面板控件的映射：对象继续分组，长文本折叠预览，其余按取值类型选控件。 */
const toGroupField = (node: OverrideTreeNode): QuickFieldDescriptor => {
  switch (node.kind) {
    case 'object':
      return { path: node.path, kind: 'group' };
    case 'array':
      return { path: node.path, kind: isStringListArray(node.value) ? 'string-list' : 'array' };
    case 'multiline':
      return { path: node.path, kind: 'multiline' };
    case 'number':
      return { path: node.path, kind: 'number' };
    case 'boolean':
      return { path: node.path, kind: 'boolean' };
    default:
      return { path: node.path, kind: 'text' };
  }
};

/**
 * 展开一个分组字段：子项来自生效条目与补丁里真实出现的键，没有声明过的键沿用原始键名，
 * 因此目录里新增或深层的字段都不需要改代码。
 *
 * 只有「空值继承」的子项会被跳过：条目里写着 null、补丁也没碰过的键既没有内容可改，
 * 又会在面板里排出一列空输入框，需要新增它们时走高级字段树。hiddenPaths 则用来跳过
 * 已由别处编辑的路径，例如被联动合并走的指令模板。
 */
export function buildQuickGroupChildren(
  node: OverrideTreeNode,
  hiddenPaths?: ReadonlySet<string>
): ReadonlyArray<QuickGroupChild> {
  if (node.kind !== 'object') return [];
  return node.children
    .filter((child) => !hiddenPaths?.has(child.id))
    .filter((child) => !(child.kind === 'empty' && child.state === 'inherit'))
    .map((child) => ({ node: child, field: toGroupField(child) }));
}

/** 联动组在面板里的展示状态：一组路径合并成一个输入框。 */
export interface QuickLinkView {
  /** 合并后的状态：任意路径有覆写即为覆写，其次是被删除，最后是继承。 */
  state: OverrideFieldState;
  /** 展示值：优先取已覆写路径的补丁值，其次是代表路径的生效值。 */
  value: unknown;
  effective: unknown;
  /** 两条路径来源一致时才有值：来源不同说明这组需要分开处理。 */
  source: string | null;
  declared: string | null;
  /** 成员各自有来源但彼此不同：界面要如实说明，而不是当成目录值。 */
  mixed: boolean;
}

/** 一组字段共有的取值：成员不一致时返回 null。 */
const commonMemberValue = (
  members: ReadonlyArray<QuickFieldView>,
  pick: (view: QuickFieldView) => string | null
): string | null => {
  if (members.length === 0) return null;
  const first = pick(members[0]);
  return members.every((member) => pick(member) === first) ? first : null;
};

/**
 * 把一组联动路径的状态合并成一个视图。
 *
 * 代表路径是联动组里的第一个路径：它既是标签的来源，也是继承状态下预览的取值，
 * 因此面板上的单个输入框始终对应同一个位置的内容。
 */
export function buildQuickLinkView(
  link: QuickLinkDescriptor,
  views: ReadonlyMap<string, QuickFieldView>
): QuickLinkView {
  const members = link.paths.map(
    (path) => views.get(quickFieldKey(path)) ?? EMPTY_QUICK_FIELD_VIEW
  );
  const overridden = members.find((view) => view.state === 'override');
  const state: OverrideFieldState = overridden
    ? 'override'
    : members.some((view) => view.state === 'removed')
      ? 'removed'
      : 'inherit';
  const primary = members.find((view) => view.inEffective) ?? members[0] ?? EMPTY_QUICK_FIELD_VIEW;
  const source = commonMemberValue(members, (view) => view.source);
  return {
    state,
    value: overridden ? overridden.value : state === 'removed' ? null : primary.value,
    effective: primary.effective,
    source,
    declared: commonMemberValue(members, (view) => view.declared),
    // 合并框写回所有路径，因此来源不一致时不能假装它是单一来源。
    mixed: source === null && members.some((view) => view.source !== null),
  };
}

/** 字段树里要隐藏的节点：常用字段由配置面板负责，slug 由编辑器顶部负责。 */
export function collectHiddenFieldKeys(): ReadonlySet<string> {
  const keys = new Set<string>(EDITOR_MANAGED_FIELD_KEYS);
  QUICK_FIELD_DESCRIPTORS.forEach((field) => keys.add(quickFieldKey(field.path)));
  return keys;
}

/** 高级字段数量：生效条目与补丁里出现的、配置面板之外的顶层字段。 */
export function countAdvancedFields(effective: unknown, patch: unknown): number {
  const keys = new Set<string>();
  const collect = (source: unknown) => {
    if (!isPlainObject(source)) return;
    Object.keys(source).forEach((key) => {
      if (!QUICK_FIELD_KEYS.has(key) && !EDITOR_MANAGED_FIELD_KEYS.has(key)) keys.add(key);
    });
  };
  collect(effective);
  collect(patch);
  return keys.size;
}

/** 把常用字段的取值转成输入框文本；字符串数组按「一行一项」呈现。 */
export function formatQuickFieldText(kind: QuickFieldKind, value: unknown): string {
  if (kind === 'group') return '';
  if (value === undefined || value === null) return '';
  if (kind === 'array') return formatArrayText(value);
  if (kind === 'string-list') {
    return Array.isArray(value) ? value.filter((item) => typeof item === 'string').join('\n') : '';
  }
  if (typeof value === 'string') return value;
  if (typeof value === 'number' || typeof value === 'boolean') return String(value);
  return '';
}

/** 多行文本的输入框高度：跟随行数，但不至于把页面撑得过长。 */
export const multilineRows = (value: unknown, min = 14, max = 30): number => {
  const lines = typeof value === 'string' ? value.split('\n').length : min;
  return Math.min(Math.max(lines, min), max);
};

/** 折叠预览里最多显示几行；其余内容由展开后的编辑框承载。 */
export const QUICK_FIELD_PREVIEW_LINES = 4;

export interface QuickFieldPreview {
  /** 折叠时展示的文本，最多 QUICK_FIELD_PREVIEW_LINES 行。 */
  text: string;
  /** 取值实际有多少行，用来提示还有多少内容没有显示。 */
  lineCount: number;
  /** 未显示的行数。 */
  hiddenLines: number;
  /** 取值没有可展示内容，面板改为显示空占位。 */
  empty: boolean;
}

/** 折叠状态下展示的取值预览；多行文本按真实换行保留，不做转义。 */
export function buildQuickFieldPreview(
  kind: QuickFieldKind,
  value: unknown,
  limit = QUICK_FIELD_PREVIEW_LINES
): QuickFieldPreview {
  const lines = formatQuickFieldText(kind, value).split('\n');
  return {
    text: lines.slice(0, limit).join('\n'),
    lineCount: lines.length,
    hiddenLines: Math.max(lines.length - limit, 0),
    empty: lines.every((line) => line.trim() === ''),
  };
}
