import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it, vi } from 'vitest';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

import type { FieldInheritBinding } from '../model/codexClientModelsInherit';
import { CodexModelQuickFields } from './CodexModelQuickFields';

/** 面板只需要读来源与写回调，测试里用一组空实现即可。 */
const sourceBinding: FieldInheritBinding = {
  directives: new Map(),
  sources: [],
  issueOf: () => undefined,
  clear: () => undefined,
  inherit: () => undefined,
  remove: () => undefined,
};

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

const renderPanel = (patch: Record<string, unknown> | null = {}) => {
  const onChange = vi.fn();
  let renderer!: ReactTestRenderer;
  act(() => {
    renderer = create(
      <CodexModelQuickFields
        effective={effectiveEntry}
        patch={patch}
        sourceBinding={sourceBinding}
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
    // 联动开启时提示词只有一个编辑入口：合并框 + 展开的 model_messages 分组 + 一个子分组。
    expect(collapsibleHeaders(renderer)).toHaveLength(3);
    expect(textareas(renderer, LINK_LABEL)).toHaveLength(0);
    // 模板被合并框接管，不在分组里重复出现。
    expect(hasField(renderer, 'model_messages')).toBe(true);
    expect(hasField(renderer, 'model_messages.instructions_template')).toBe(false);
  });

  it('replaces the preview with the real editor once the field is expanded', () => {
    const { renderer } = renderPanel();

    const [instructions] = collapsibleHeaders(renderer);
    act(() => instructions.props.onClick());

    const [editor] = textareas(renderer, LINK_LABEL);
    expect(editor.props.value).toBe('You are Codex.\n\nFollow the repo conventions.');
    expect(editor.props.rows).toBeGreaterThan(4);
  });

  it('writes one edit into both prompt fields', () => {
    const { renderer, onChange } = renderPanel();

    act(() => collapsibleHeaders(renderer)[0].props.onClick());
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
    expect(collapsibleHeaders(renderer)).toHaveLength(4);
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
