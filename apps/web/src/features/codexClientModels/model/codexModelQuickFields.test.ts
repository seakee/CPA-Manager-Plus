import { describe, expect, it } from 'vitest';
import { buildOverrideTree } from './codexClientModelsTree';
import {
  QUICK_FIELD_KEYS,
  QUICK_FIELD_LINKS,
  QUICK_FIELD_PREVIEW_LINES,
  QUICK_FIELD_SECTIONS,
  buildQuickFieldPreview,
  buildQuickFieldViews,
  buildQuickGroupChildren,
  buildQuickLinkView,
  collectCatalogFieldOptions,
  collectHiddenFieldKeys,
  countAdvancedFields,
  findQuickTreeNode,
  formatQuickFieldText,
  multilineRows,
  quickFieldKey,
  quickFieldViewOf,
} from './codexModelQuickFields';

const effectiveEntry = {
  slug: 'gpt-5.5',
  display_name: 'GPT-5.5',
  context_window: 272000,
  visibility: 'list',
  supports_search_tool: true,
  input_modalities: ['text', 'image'],
  base_instructions: 'You are Codex.',
  model_messages: {
    instructions_template: 'Template body',
    instructions_variables: { personality_friendly: 'Be warm.' },
    tools: { web_search: true },
  },
  truncation_policy: { mode: 'tokens' },
};

const viewsOf = (effective: unknown, patch: unknown) =>
  buildQuickFieldViews(buildOverrideTree({ effective, patch }));

describe('codexModelQuickFields', () => {
  it('covers every section field exactly once and adds the linked paths', () => {
    const keys = QUICK_FIELD_SECTIONS.flatMap((section) =>
      section.fields.map((field) => quickFieldKey(field.path))
    );
    const linkedPaths = QUICK_FIELD_LINKS.flatMap((link) => link.paths.map(quickFieldKey));

    expect(new Set(keys).size).toBe(keys.length);
    expect(new Set([...keys, ...linkedPaths])).toEqual(new Set(QUICK_FIELD_KEYS));
  });

  it('keeps the prompt fields in the common panel, with model_messages as a group', () => {
    const promptSection = QUICK_FIELD_SECTIONS.find((section) => section.id === 'prompt');
    const promptFields = promptSection?.fields ?? [];

    expect(promptFields.map((field) => quickFieldKey(field.path))).toEqual([
      'base_instructions',
      'model_messages',
    ]);
    expect(promptFields[1]).toMatchObject({ kind: 'group', defaultOpen: true });
    promptFields.forEach((field) =>
      expect(QUICK_FIELD_KEYS.has(quickFieldKey(field.path))).toBe(true)
    );
  });

  it('keeps the tools section on the fields that are worth editing there', () => {
    const tools = QUICK_FIELD_SECTIONS.find((section) => section.id === 'tools');
    const keys = tools?.fields.map((field) => quickFieldKey(field.path));

    expect(keys).toEqual([
      'input_modalities',
      'apply_patch_tool_type',
      'shell_type',
      'web_search_tool_type',
      'multi_agent_version',
      'multi_agent_reasoning_effort',
    ]);
  });

  it('marks base instructions as the visible half of the link group', () => {
    const link = QUICK_FIELD_LINKS.find((candidate) => candidate.id === 'instructions');
    expect(link?.paths).toEqual([
      ['base_instructions'],
      ['model_messages', 'instructions_template'],
    ]);

    const linked = QUICK_FIELD_SECTIONS.flatMap((section) => section.fields)
      .filter((field) => field.link === 'instructions')
      .map((field) => quickFieldKey(field.path));
    expect(linked).toEqual(['base_instructions']);
    expect(QUICK_FIELD_KEYS.has('model_messages.instructions_template')).toBe(true);
  });

  it('merges a link group into one view, following the first path as the representative', () => {
    const link = QUICK_FIELD_LINKS[0];

    expect(buildQuickLinkView(link, viewsOf(effectiveEntry, {}))).toMatchObject({
      state: 'inherit',
      value: 'You are Codex.',
      effective: 'You are Codex.',
    });

    const overriddenByBase = buildQuickLinkView(
      link,
      viewsOf(effectiveEntry, { base_instructions: 'Shared prompt' })
    );
    expect(overriddenByBase).toMatchObject({ state: 'override', value: 'Shared prompt' });

    const overriddenByTemplate = buildQuickLinkView(
      link,
      viewsOf(effectiveEntry, {
        model_messages: { instructions_template: 'Shared prompt' },
      })
    );
    expect(overriddenByTemplate).toMatchObject({
      state: 'override',
      value: 'Shared prompt',
      effective: 'You are Codex.',
    });

    const removed = buildQuickLinkView(
      link,
      viewsOf(effectiveEntry, { model_messages: { instructions_template: null } })
    );
    expect(removed).toMatchObject({ state: 'removed', value: null });
  });

  it('reports the inherited, overridden and removed state of each quick field', () => {
    const views = viewsOf(effectiveEntry, {
      display_name: 'Local GPT-5.5',
      context_window: 128000,
      supports_search_tool: null,
      base_instructions: 'Local prompt',
    });

    expect(views.get('display_name')).toMatchObject({
      state: 'override',
      value: 'Local GPT-5.5',
      effective: 'GPT-5.5',
    });
    expect(views.get('context_window')).toMatchObject({ state: 'override', value: 128000 });
    expect(views.get('supports_search_tool')).toMatchObject({
      state: 'removed',
      value: null,
      effective: true,
    });
    expect(views.get('visibility')).toMatchObject({ state: 'inherit', value: 'list' });
    expect(views.get('base_instructions')).toMatchObject({
      state: 'override',
      value: 'Local prompt',
    });
  });

  it('reads the state of prompt fields nested in model_messages', () => {
    const nodes = buildOverrideTree({
      effective: effectiveEntry,
      patch: {
        model_messages: {
          instructions_template: 'Local template',
          instructions_variables: { personality_friendly: null },
        },
      },
    });
    const views = buildQuickFieldViews(nodes);
    // 分组字段的子项不在扁平视图里，状态直接读字段树节点。
    const viewAt = (fieldPath: string[]) =>
      quickFieldViewOf(findQuickTreeNode(nodes, fieldPath) as never);

    expect(views.get('model_messages.instructions_template')).toMatchObject({
      state: 'override',
      value: 'Local template',
      effective: 'Template body',
    });
    expect(
      viewAt(['model_messages', 'instructions_variables', 'personality_friendly'])
    ).toMatchObject({
      state: 'removed',
      effective: 'Be warm.',
    });
    // 分组子项只列出目录与补丁里真实存在的键，不存在的不生成空行。
    expect(
      findQuickTreeNode(nodes, [
        'model_messages',
        'instructions_variables',
        'personality_pragmatic',
      ])
    ).toBeUndefined();
  });

  it('returns a view for quick fields the entry does not define, so the panel stays stable', () => {
    const views = viewsOf(effectiveEntry, {});

    expect(views.size).toBe(QUICK_FIELD_KEYS.size);
    expect(views.get('prefer_websockets')).toMatchObject({
      state: 'inherit',
      inEffective: false,
      value: undefined,
    });
  });

  it('reports which model covers each field, taking the nearest directive', () => {
    const directives = new Map([
      ['', 'gpt-5.6-sol'],
      ['model_messages', 'gpt-5.5'],
    ]);
    const nodes = buildOverrideTree({ effective: effectiveEntry, patch: {}, inherit: directives });
    const views = buildQuickFieldViews(nodes, directives);

    // 整条指令来自祖先，因此只报来源，不报字段自己声明的来源。
    expect(views.get('display_name')).toMatchObject({
      source: 'gpt-5.6-sol',
      declared: null,
    });
    // 深层字段继承自最近的上级指令，而不是整条指令。
    expect(views.get('model_messages.instructions_template')).toMatchObject({
      source: 'gpt-5.5',
      declared: null,
    });
    // 两条路径来源不一致时联动框不谎报单一来源，也不假装是目录值。
    expect(buildQuickLinkView(QUICK_FIELD_LINKS[0], views)).toMatchObject({
      source: null,
      declared: null,
      mixed: true,
    });
  });

  it('reports a link group as consistent when both paths follow the same model', () => {
    const directives = new Map([['', 'gpt-5.6-sol']]);
    const nodes = buildOverrideTree({ effective: effectiveEntry, patch: {}, inherit: directives });
    const views = buildQuickFieldViews(nodes, directives);

    expect(buildQuickLinkView(QUICK_FIELD_LINKS[0], views)).toMatchObject({
      source: 'gpt-5.6-sol',
      mixed: false,
    });
    // 两条路径都跟随目录时同样不是「来源不一致」。
    expect(buildQuickLinkView(QUICK_FIELD_LINKS[0], viewsOf(effectiveEntry, {}))).toMatchObject({
      source: null,
      mixed: false,
    });
  });

  it('still reports the source of a field the tree cannot show', () => {
    const directives = new Map([['model_messages', 'gpt-5.5']]);
    const views = buildQuickFieldViews([], directives);

    expect(views.get('model_messages.instructions_template')).toMatchObject({
      state: 'inherit',
      source: 'gpt-5.5',
      declared: null,
    });
  });

  it('counts the top level fields that stay in the advanced tree', () => {
    // model_messages 现在也在配置面板里，只剩 truncation_policy 留在高级字段树。
    expect(countAdvancedFields(effectiveEntry, {})).toBe(1);
    expect(countAdvancedFields(effectiveEntry, { truncation_policy: { mode: 'chars' } })).toBe(1);
    expect(countAdvancedFields(effectiveEntry, { service_tiers: [] })).toBe(2);
    expect(countAdvancedFields(effectiveEntry, { display_name: 'x' })).toBe(1);
    expect(countAdvancedFields(null, null)).toBe(0);
  });

  it('hides the common fields and the managed slug from the advanced tree', () => {
    const hidden = collectHiddenFieldKeys();

    expect(hidden.has('slug')).toBe(true);
    expect(hidden.has('display_name')).toBe(true);
    // 隐藏的是分组本身，子节点随分组一起从字段树里剪掉。
    expect(hidden.has('model_messages')).toBe(true);
    expect(hidden.has('model_messages.instructions_template')).toBe(false);
    expect(hidden.has('truncation_policy')).toBe(false);
  });

  it('collects enum options from the catalog without inventing values', () => {
    const options = collectCatalogFieldOptions([
      { visibility: 'list', shell_type: 'shell_command', display_name: 'GPT-5.5' },
      { visibility: 'hide', shell_type: 'shell_command' },
      { visibility: 'list' },
    ]);

    expect(options.get('visibility')).toEqual(['hide', 'list']);
    expect(options.get('shell_type')).toEqual(['shell_command']);
    expect(options.has('display_name')).toBe(false);
    expect(options.get('default_verbosity')).toEqual([]);
  });

  it('renders string lists one item per line and drops non string entries', () => {
    expect(formatQuickFieldText('string-list', ['text', 'image'])).toBe('text\nimage');
    expect(formatQuickFieldText('string-list', ['text', 3, null])).toBe('text');
    expect(formatQuickFieldText('string-list', undefined)).toBe('');
    expect(formatQuickFieldText('text', 'gpt-5.5')).toBe('gpt-5.5');
    expect(formatQuickFieldText('multiline', 'line one\nline two')).toBe('line one\nline two');
    expect(formatQuickFieldText('multiline', { a: 1 })).toBe('');
  });

  it('previews only the first lines of a long value', () => {
    const value = Array.from({ length: 6 }, (_, index) => `line ${index + 1}`).join('\n');

    expect(QUICK_FIELD_PREVIEW_LINES).toBe(4);
    expect(buildQuickFieldPreview('multiline', value)).toEqual({
      text: 'line 1\nline 2\nline 3\nline 4',
      lineCount: 6,
      hiddenLines: 2,
      empty: false,
    });
  });

  it('flags empty values so the panel shows a placeholder instead of a blank preview', () => {
    expect(buildQuickFieldPreview('multiline', undefined)).toMatchObject({
      text: '',
      lineCount: 1,
      hiddenLines: 0,
      empty: true,
    });
    expect(buildQuickFieldPreview('multiline', 'one line')).toMatchObject({
      text: 'one line',
      empty: false,
      hiddenLines: 0,
    });
  });

  it('expands a group from the catalog, keeping key order and shapes', () => {
    const nodes = buildOverrideTree({ effective: effectiveEntry, patch: {} });
    const group = findQuickTreeNode(nodes, ['model_messages']);
    const children = buildQuickGroupChildren(group as never);

    expect(children.map((child) => quickFieldKey(child.field.path))).toEqual([
      'model_messages.instructions_template',
      'model_messages.instructions_variables',
      'model_messages.tools',
    ]);
    expect(children.map((child) => child.field.kind)).toEqual(['text', 'group', 'group']);
    expect(children[1].node.children.map((child) => child.key)).toEqual(['personality_friendly']);
  });

  it('expands keys that only exist in the patch and drops the ones edited elsewhere', () => {
    const nodes = buildOverrideTree({
      effective: effectiveEntry,
      patch: { model_messages: { approvals: { never: 'no' } } },
    });
    const group = findQuickTreeNode(nodes, ['model_messages']);

    expect(
      buildQuickGroupChildren(
        group as never,
        new Set(['model_messages.instructions_template'])
      ).map((child) => quickFieldKey(child.field.path))
    ).toEqual([
      'model_messages.instructions_variables',
      'model_messages.tools',
      'model_messages.approvals',
    ]);

    const approvals = buildQuickGroupChildren(group as never).find(
      (child) => child.field.path.join('.') === 'model_messages.approvals'
    );
    expect(approvals?.field.kind).toBe('group');
    expect(
      buildQuickGroupChildren(approvals?.node as never).map((child) => child.field.kind)
    ).toEqual(['text']);
  });

  it('maps every value shape inside a group to its own control', () => {
    const nodes = buildOverrideTree({
      effective: {
        group: {
          list: ['text', 'image'],
          mixed: [1, 2],
          count: 3,
          flag: true,
          single: 'one line',
          long: 'two\nlines',
        },
      },
      patch: {},
    });

    const children = buildQuickGroupChildren(findQuickTreeNode(nodes, ['group']) as never);
    expect(children.map((child) => [child.field.path.join('.'), child.field.kind])).toEqual([
      ['group.list', 'string-list'],
      ['group.mixed', 'array'],
      ['group.count', 'number'],
      ['group.flag', 'boolean'],
      ['group.single', 'text'],
      ['group.long', 'multiline'],
    ]);
  });

  it('skips sub-fields that are only null in the entry, but keeps the touched ones', () => {
    const untouched = buildOverrideTree({
      effective: { group: { empty: null, kept: 'text' } },
      patch: {},
    });
    expect(
      buildQuickGroupChildren(findQuickTreeNode(untouched, ['group']) as never).map(
        (child) => child.node.key
      )
    ).toEqual(['kept']);

    // 补丁里显式写 null 的子项是用户刚做的删除，要留在面板上让他能还原。
    const removed = buildOverrideTree({
      effective: { group: { gone: 'text' } },
      patch: { group: { gone: null } },
    });
    const removedChild = buildQuickGroupChildren(findQuickTreeNode(removed, ['group']) as never)[0];
    expect(removedChild.node.key).toBe('gone');
    expect(removedChild.node.state).toBe('removed');
  });

  it('renders non string arrays as JSON text', () => {
    expect(formatQuickFieldText('array', [1, 2])).toBe('[\n  1,\n  2\n]');
    expect(formatQuickFieldText('array', ['a', 'b'])).toBe('a\nb');
    expect(formatQuickFieldText('group', { a: 1 })).toBe('');
  });

  it('sizes multi-line editors after the content but keeps them bounded', () => {
    expect(multilineRows('one\ntwo')).toBe(14);
    expect(multilineRows(Array.from({ length: 40 }, () => 'x').join('\n'))).toBe(30);
    expect(multilineRows(undefined)).toBe(14);
  });
});
