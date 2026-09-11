import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it, vi } from 'vitest';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: Record<string, unknown>) =>
      options ? `${key}${JSON.stringify(options)}` : key,
  }),
}));

import type { FieldInheritBinding } from '../model/codexClientModelsInherit';
import { OverrideTreeEditor } from './OverrideTreeEditor';

const effectiveEntry = {
  slug: 'gpt-5.5',
  display_name: 'GPT-5.5',
  base_instructions: 'You are Codex.\n\nFollow the repo conventions.',
  model_messages: { notes: 'keep in sync' },
  available_in_plans: ['plus', 'pro'],
  supported_reasoning_levels: [{ description: 'Fast', effort: 'low' }],
};

const makeBinding = (overrides: Partial<FieldInheritBinding> = {}): FieldInheritBinding => ({
  directives: new Map(),
  sources: ['gpt-5.6-sol'],
  issueOf: () => undefined,
  clear: () => undefined,
  inherit: () => undefined,
  remove: () => undefined,
  ...overrides,
});

const renderEditor = (
  patch: Record<string, unknown> | null,
  sourceBinding: FieldInheritBinding = makeBinding()
) =>
  renderToStaticMarkup(
    <OverrideTreeEditor
      effective={effectiveEntry}
      patch={patch}
      sourceBinding={sourceBinding}
      onChange={() => undefined}
    />
  );

describe('OverrideTreeEditor', () => {
  it('shows inherited multi-line values with real newlines instead of escape sequences', () => {
    const markup = renderEditor({});

    expect(markup).toContain('You are Codex.\n\nFollow the repo conventions.');
    expect(markup).not.toContain('\\n');
  });

  it('marks every field state so an override can be told apart from the effective value', () => {
    const markup = renderEditor({ display_name: 'Local GPT-5.5', available_in_plans: null });

    expect(markup).toContain('codex_client_models.field_state_official');
    expect(markup).toContain('codex_client_models.field_state_override');
    expect(markup).toContain('codex_client_models.field_state_removed');
    // 状态标签本身就是「改成别的来源」的入口。
    expect(markup).toContain('codex_client_models.field_source_menu_label');
  });

  it('names the model a field inherits from', () => {
    const markup = renderEditor(
      {},
      makeBinding({ directives: new Map([['base_instructions', 'gpt-5.6-sol']]) })
    );

    expect(markup).toContain('codex_client_models.field_state_inherited');
    expect(markup).toContain('gpt-5.6-sol');
  });

  it('flags a field whose inherit directive the server would reject', () => {
    const markup = renderEditor(
      { $inherit: { display_name: 'ghost-model' } },
      makeBinding({ issueOf: () => 'codex_client_models.inherit_issue_unknown_source' })
    );

    expect(markup).toContain('codex_client_models.field_source_issue');
  });

  it('edits overridden arrays through a whole-array editor', () => {
    const markup = renderEditor({ available_in_plans: ['plus', 'pro', 'team'] });

    expect(markup).toContain('codex_client_models.tree_array_note');
    expect(markup).toContain('plus\npro\nteam');
  });

  it('explains the empty catalog of a removed entry instead of rendering a blank panel', () => {
    const markup = renderToStaticMarkup(
      <OverrideTreeEditor
        effective={null}
        patch={null}
        sourceBinding={makeBinding()}
        onChange={() => undefined}
      />
    );

    expect(markup).toContain('codex_client_models.tree_empty_no_fields');
  });
});
