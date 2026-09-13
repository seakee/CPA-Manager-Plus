import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it, vi } from 'vitest';

vi.mock('react-i18next', () => ({
  // 继承来源标签把 slug 一起带出来，断言才能区分「继承自某个模型」与其它文案。
  useTranslation: () => ({
    t: (key: string, options?: Record<string, unknown>) =>
      typeof options?.slug === 'string' ? `${key}{"slug":"${options.slug}"}` : key,
  }),
}));

import type { FieldInheritBinding } from '../model/codexClientModelsInherit';
import { CodexModelQuickFields } from './CodexModelQuickFields';

/** 面板只需要读来源与写回调，测试里用一组空实现即可。 */
const bindingOf = (overrides: Partial<FieldInheritBinding> = {}): FieldInheritBinding => ({
  directives: new Map(),
  sources: [],
  heldFields: new Set(),
  issueOf: () => undefined,
  clear: () => undefined,
  inherit: () => undefined,
  remove: () => undefined,
  ...overrides,
});

const LINK_LABEL = 'codex_client_models.quick_links.instructions.label';

const effectiveEntry = {
  slug: 'gpt-5.5',
  display_name: 'GPT-5.5',
  base_instructions: 'You are Codex.\n\nFollow the repo conventions.',
  model_messages: {
    instructions_template: 'You are Codex.\n\nFollow the repo conventions.',
    instructions_variables: { personality_friendly: 'Be warm.' },
  },
};

const renderPanel = (
  patch: Record<string, unknown> | null = {},
  options: {
    effective?: Record<string, unknown>;
    binding?: FieldInheritBinding;
  } = {}
) => {
  const onChange = vi.fn();
  let renderer!: ReactTestRenderer;
  act(() => {
    renderer = create(
      <CodexModelQuickFields
        effective={options.effective ?? effectiveEntry}
        patch={patch}
        sourceBinding={options.binding ?? bindingOf()}
        onChange={onChange}
      />
    );
  });
  return { renderer, onChange };
};

/** 字段与分组的折叠开关；来源标签也是 aria-expanded 按钮，用 aria-haspopup 排除掉。 */
const collapsibleHeaders = (renderer: ReactTestRenderer) =>
  renderer.root.findAll(
    (node) =>
      node.type === 'button' &&
      node.props['aria-expanded'] !== undefined &&
      node.props['aria-haspopup'] === undefined
  );

const textareas = (renderer: ReactTestRenderer, ariaLabel: string) =>
  renderer.root.findAll(
    (node) => node.type === 'textarea' && node.props['aria-label'] === ariaLabel
  );

const linkToggle = (renderer: ReactTestRenderer) =>
  renderer.root.findAll(
    (node) => node.type === 'input' && node.props['aria-label'] === LINK_LABEL
  )[0];

/** 展开某个字段：折叠状态下看不到里面的控件。 */
const expandField = (renderer: ReactTestRenderer, path: string) =>
  act(() => fieldToggle(renderer, path)?.props.onClick());

/** 字段标签文本；i18n 在测试里被替换成键名本身。 */
const quickFieldLabel = (path: string) => `codex_client_models.quick.fields.${path}.label`;

/** 面板里是否出现了某个字段的行。 */
const hasField = (renderer: ReactTestRenderer, path: string) =>
  renderer.root.findAll(
    (node) => node.type === 'span' && node.children.join('') === quickFieldLabel(path)
  ).length > 0;

/** 某个字段的折叠开关按钮。 */
const fieldToggle = (renderer: ReactTestRenderer, path: string) =>
  collapsibleHeaders(renderer).find(
    (button) =>
      button.findAll((child) => child.children.join('') === quickFieldLabel(path)).length > 0
  );

describe('CodexModelQuickFields prompt section', () => {
  it('merges the two prompt fields into one collapsed box by default', () => {
    const { renderer } = renderPanel();

    expect(linkToggle(renderer).props.checked).toBe(true);
    // 可折叠的是合并框、推理强度列表，以及 model_messages 的两层分组。
    expect(collapsibleHeaders(renderer)).toHaveLength(4);
    expect(textareas(renderer, LINK_LABEL)).toHaveLength(0);
    // 模板被合并框接管，不在分组里重复出现。
    expect(hasField(renderer, 'model_messages')).toBe(true);
    expect(hasField(renderer, 'model_messages.instructions_template')).toBe(false);
  });

  it('replaces the preview with the real editor once the field is expanded', () => {
    const { renderer } = renderPanel();

    expandField(renderer, 'base_instructions');

    const [editor] = textareas(renderer, LINK_LABEL);
    expect(editor.props.value).toBe('You are Codex.\n\nFollow the repo conventions.');
    expect(editor.props.rows).toBeGreaterThan(4);
  });

  it('writes one edit into both prompt fields', () => {
    const { renderer, onChange } = renderPanel();

    expandField(renderer, 'base_instructions');
    const [editor] = textareas(renderer, LINK_LABEL);
    act(() => editor.props.onChange({ target: { value: 'Shared prompt' } }));

    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange.mock.calls[0][0]).toEqual({
      base_instructions: 'Shared prompt',
      model_messages: { instructions_template: 'Shared prompt' },
    });
  });

  it('splits the merged box back into two fields when the link is switched off', () => {
    const { renderer } = renderPanel();

    act(() => linkToggle(renderer).props.onChange({ target: { checked: false } }));

    // 关闭后合并框消失，基础提示词与模板各自回到自己的字段。
    expect(collapsibleHeaders(renderer)).toHaveLength(5);
    expect(hasField(renderer, 'base_instructions')).toBe(true);
    expect(hasField(renderer, 'model_messages.instructions_template')).toBe(true);
  });

  it('expands model_messages level by level', () => {
    const { renderer } = renderPanel();

    // 第一层默认展开：直接能看到分组里的子项。
    expect(hasField(renderer, 'model_messages.instructions_variables')).toBe(true);
    // 第二层默认收起：展开后才出现人格片段。
    expect(hasField(renderer, 'model_messages.instructions_variables.personality_friendly')).toBe(
      false
    );

    const toggle = fieldToggle(renderer, 'model_messages.instructions_variables');
    expect(toggle).toBeDefined();
    act(() => toggle?.props.onClick());

    expect(hasField(renderer, 'model_messages.instructions_variables.personality_friendly')).toBe(
      true
    );
  });
});

describe('CodexModelQuickFields context section', () => {
  const levelsEffective = {
    ...effectiveEntry,
    supported_reasoning_levels: [{ effort: 'low', description: 'Fast' }],
  };

  const removeButtons = (renderer: ReactTestRenderer) =>
    renderer.root.findAll(
      (node) =>
        node.type === 'button' &&
        node.props['aria-label'] === 'codex_client_models.quick_levels_remove'
    );

  /** 带某段文本的按钮：新增按钮只有图标和文字，没有 aria-label。 */
  const buttonWithText = (renderer: ReactTestRenderer, text: string) =>
    renderer.root.findAll(
      (node) =>
        node.type === 'button' &&
        node.findAll((child) => Array.isArray(child.children) && child.children.includes(text))
          .length > 0
    )[0];

  it('keeps the reasoning levels collapsed until the field is expanded', () => {
    const { renderer } = renderPanel({}, { effective: levelsEffective });

    // 折叠时只给预览，展开后才出现逐项编辑。
    expect(removeButtons(renderer)).toHaveLength(0);
    expandField(renderer, 'supported_reasoning_levels');

    expect(removeButtons(renderer)).toHaveLength(1);
  });

  it('writes the whole list back when a level is removed', () => {
    const { renderer, onChange } = renderPanel({}, { effective: levelsEffective });
    expandField(renderer, 'supported_reasoning_levels');

    act(() => removeButtons(renderer)[0].props.onClick());

    expect(onChange.mock.calls[0][0]).toEqual({ supported_reasoning_levels: [] });
  });

  it('adds the next unused effort to the list', () => {
    const { renderer, onChange } = renderPanel({}, { effective: levelsEffective });
    expandField(renderer, 'supported_reasoning_levels');

    act(() => buttonWithText(renderer, 'codex_client_models.quick_levels_add').props.onClick());

    expect(onChange.mock.calls[0][0]).toEqual({
      supported_reasoning_levels: [
        { effort: 'low', description: 'Fast' },
        { effort: 'none', description: '' },
      ],
    });
  });

  it('marks a field the server decides as a default rather than an inherit source', () => {
    const chips = (renderer: ReactTestRenderer) =>
      renderer.root.findAll(
        (node) =>
          node.type === 'span' &&
          typeof node.props.className === 'string' &&
          node.props.className.includes('chip_')
      );

    const { renderer } = renderPanel(
      {},
      {
        binding: bindingOf({
          directives: new Map([['', 'gpt-5.6-sol']]),
          sources: ['gpt-5.6-sol'],
          heldFields: new Set(['context_window']),
        }),
      }
    );

    const labels = chips(renderer).map((node) => node.children.join(''));
    // 服务端自己决定的字段仍显示默认值，其余字段跟随整条继承。
    expect(labels).toContain('codex_client_models.field_state_default');
    expect(labels).toContain('codex_client_models.field_state_inherited{"slug":"gpt-5.6-sol"}');
  });
});
