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

const catalog = [effectiveEntry, { slug: 'gpt-5.6-sol', display_name: 'GPT-5.6 Sol' }];

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
    expect(markup).toContain('codex_client_models.field_state_official');
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

  it('remembers that an entry removed by a null override has to be rebuilt', () => {
    const markup = renderEditor({
      slug: 'gpt-5.6-sol',
      effectiveEntry: null,
      patch: null,
    });

    expect(markup).toContain('codex_client_models.editor_removed_notice');
  });

  it('offers a whole-entry inherit source only when the server supports inheritance', () => {
    expect(renderEditor()).toContain('codex_client_models.inherit_root_label');
    expect(renderEditor({ supportsInherit: false })).not.toContain(
      'codex_client_models.inherit_root_label'
    );
  });

  it('seeds a new entry by inheriting the official template', () => {
    const markup = renderEditor({
      mode: 'create',
      slug: 'qwen3-max',
      effectiveEntry: null,
      patch: null,
    });

    expect(markup).toContain('codex_client_models.editor_title_create');
    // 新条目整条继承官方模板，因此面板展示的是模板的取值而不是空表单。
    expect(markup).toContain('272000');
    expect(markup).toContain('gpt-5.5');
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
