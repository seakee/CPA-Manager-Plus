import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it, vi } from 'vitest';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: Record<string, unknown>) =>
      options ? `${key}${JSON.stringify(options)}` : key,
  }),
}));

vi.mock('@/stores', () => ({
  useThemeStore: (selector: (state: { resolvedTheme: 'light' }) => unknown) =>
    selector({ resolvedTheme: 'light' }),
}));

import { CodexModelInlineEditor, type CodexModelInlineEditorProps } from './CodexModelInlineEditor';

const effectiveEntry = {
  slug: 'gpt-5.5',
  display_name: 'GPT-5.5',
  visibility: 'list',
  context_window: 272000,
  base_instructions: 'You are Codex.\n\nFollow the repo conventions.',
};

const catalog = [
  effectiveEntry,
  { slug: 'gpt-5.6-sol', display_name: 'GPT-5.6 Sol', context_window: 400000 },
];

const renderEditor = (overrides: Partial<CodexModelInlineEditorProps> = {}) =>
  renderToStaticMarkup(
    <CodexModelInlineEditor
      mode="edit"
      slug="gpt-5.5"
      effectiveEntry={effectiveEntry}
      patch={{}}
      catalog={catalog}
      overrideDocument={{}}
      supportsInherit
      isSlugTaken={() => true}
      saving={false}
      serverError=""
      onCancel={() => undefined}
      onSave={() => undefined}
      onDelete={() => undefined}
      {...overrides}
    />
  );

describe('CodexModelInlineEditor', () => {
  it('renders the common fields as a localized panel next to the model', () => {
    const markup = renderEditor();

    expect(markup).toContain('codex_client_models.quick.sections.basic.title');
    expect(markup).toContain('codex_client_models.quick.fields.display_name.label');
    expect(markup).toContain('codex_client_models.quick.fields.context_window.label');
    expect(markup).toContain('codex_client_models.field_state_default');
  });

  it('offers the prompt fields in the common panel instead of hiding them in the tree', () => {
    const markup = renderEditor();

    expect(markup).toContain('codex_client_models.quick.sections.prompt.title');
    expect(markup).toContain('codex_client_models.quick.sections.prompt.description');
  });

  it('shows one collapsed prompt preview and links the two prompt fields by default', () => {
    const markup = renderEditor();

    expect(markup).toContain('codex_client_models.quick_links.instructions.label');
    expect(markup).toContain('codex_client_models.quick.fields.base_instructions.label');
    // 联动开启时只渲染一个输入框，另一个字段的标签不再出现。
    expect(markup).not.toContain(
      'codex_client_models.quick.fields.model_messages.instructions_template.label'
    );
    // 折叠状态给出前几行预览，编辑框要等展开后才挂载。
    expect(markup).toContain('You are Codex.\n\nFollow the repo conventions.');
    expect(markup).toContain('codex_client_models.quick_field_preview_open');
    expect(markup).not.toContain(
      'aria-label="codex_client_models.quick.fields.base_instructions.label"'
    );
  });

  it('keeps the remaining fields and the JSON patch collapsed by default', () => {
    const markup = renderEditor();

    expect(markup).toContain('codex_client_models.editor_advanced_fields');
    expect(markup).toContain('codex_client_models.editor_json_title');
    expect(markup).not.toContain('<details open');
  });

  it('shows the effective value of an inherited multi-line field without escaping newlines', () => {
    const markup = renderEditor({ patch: { display_name: 'Local GPT-5.5' } });

    expect(markup).toContain('Local GPT-5.5');
    expect(markup).toContain('codex_client_models.field_state_override');
  });

  it('says so when there is no default entry to compare a model against', () => {
    const markup = renderEditor({
      slug: 'gpt-5.6-sol',
      effectiveEntry: null,
      patch: null,
    });

    expect(markup).toContain('codex_client_models.editor_no_default_notice');
  });

  it('offers a whole-entry inherit source only when the server supports inheritance', () => {
    expect(renderEditor()).toContain('codex_client_models.inherit_root_label');
    expect(renderEditor({ supportsInherit: false })).not.toContain(
      'codex_client_models.inherit_root_label'
    );
  });

  it('starts a new entry from the slug alone when nothing is served under it', () => {
    const markup = renderEditor({
      mode: 'create',
      slug: 'qwen3-max',
      effectiveEntry: null,
      patch: null,
    });

    expect(markup).toContain('codex_client_models.editor_title_create');
    // 还没有服务端装配这个 slug，因此没有可展示的默认值。
    expect(markup).toContain('codex_client_models.editor_no_default_notice');
    expect(markup).toContain('codex_client_models.inherit_root_none');
  });

  it('shows the assembled values when a new entry names a served model', () => {
    const markup = renderEditor({
      mode: 'create',
      slug: 'gpt-5.6-sol',
      effectiveEntry: null,
      patch: null,
    });

    expect(markup).toContain('value="400000"');
    expect(markup).not.toContain('codex_client_models.editor_no_default_notice');
  });

  it('locks the slug when adopting a served model and starts from its own entry', () => {
    const markup = renderEditor({
      mode: 'adopt',
      slug: 'deepseek-flash',
      effectiveEntry: { ...effectiveEntry, slug: 'deepseek-flash', display_name: 'deepseek-flash' },
      patch: undefined,
    });

    expect(markup).toContain(
      'codex_client_models.editor_title_adopt{&quot;slug&quot;:&quot;deepseek-flash&quot;}'
    );
    expect(markup).toContain('codex_client_models.adopt_notice');
    expect(markup).toContain('codex_client_models.adopt_slug_hint');
    expect(markup).not.toContain('codex_client_models.editor_title_create');
    // 还没有条目，因此没有可删除的覆写。
    expect(markup).not.toContain('codex_client_models.delete_override');
    // 起步补丁只有 slug，取值基准是服务端装配出的默认条目，字段因此都显示为默认值。
    expect(markup).toContain('value="deepseek-flash"');
    expect(markup).not.toContain('codex_client_models.field_state_inherited');
  });

  it('keeps a field only the model supplies out of a whole-entry inherit', () => {
    const markup = renderEditor({
      patch: { $inherit: 'gpt-5.6-sol' },
      catalog: [
        { ...effectiveEntry, context_window: 272000 },
        { slug: 'gpt-5.6-sol', display_name: 'GPT-5.6 Sol', context_window: 400000 },
      ],
    });

    // 上下文窗口属于只由模型自身提供的字段，整条继承不会把它换成来源的取值。
    expect(markup).toContain('value="272000"');
    expect(markup).not.toContain('value="400000"');
  });

  it('reports an inherit directive that points at a missing model', () => {
    const markup = renderEditor({ patch: { $inherit: 'ghost-model' } });

    expect(markup).toContain('codex_client_models.inherit_issue_title');
    expect(markup).toContain('codex_client_models.inherit_issue_unknown_source');
  });

  it('counts the entries that inherit from this model', () => {
    const markup = renderEditor({
      overrideDocument: {
        'gpt-5.5': {},
        'my-sol': { $inherit: 'gpt-5.5' },
        'my-sol-lite': { $inherit: 'gpt-5.5' },
      },
    });

    // React 会把文案里的引号转义，因此断言转义之后的片段。
    expect(markup).toContain('codex_client_models.inherit_usage{&quot;value&quot;:2}');
  });
});
