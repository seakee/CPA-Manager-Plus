import { describe, expect, it } from 'vitest';
import type {
  CodexClientModelsState,
  CodexClientServedModel,
} from '@/services/api/codexClientModels';
import {
  buildAdoptedModelPatch,
  buildCodexClientModelRows,
  buildInheritedModelPatch,
  buildModelPatchFromTemplate,
  countCodexClientModelRows,
  filterCodexClientModelRows,
  findModelEntry,
  formatOverridePatch,
  parseOverridePatchInput,
  resolveDefaultInheritEntry,
  resolveDefaultInheritSource,
  resolveServedInheritEntry,
} from './codexClientModelsModel';

const buildState = (overrides: Partial<CodexClientModelsState> = {}): CodexClientModelsState => ({
  source: 'https://example.com/models.json',
  revision: 7,
  models: [
    {
      slug: 'gpt-5.5',
      display_name: 'GPT-5.5',
      context_window: 272000,
      visibility: 'public',
      default_reasoning_level: 'medium',
    },
    { slug: 'deepseek-chat', display_name: 'DeepSeek Chat' },
  ],
  origins: { 'gpt-5.5': 'base', 'deepseek-chat': 'override' },
  override: { 'deepseek-chat': { display_name: 'DeepSeek Chat' } },
  overridePath: '/opt/cpa/codex_client_models_override.json',
  overrideError: '',
  overrideErrors: [],
  servedModels: null,
  ...overrides,
});

const servedModel = (overrides: Partial<CodexClientServedModel> = {}): CodexClientServedModel => ({
  slug: 'deepseek-flash',
  templateSlug: 'gpt-5.5',
  defaultTemplate: true,
  providers: ['openai-compatibility'],
  displayName: 'deepseek-flash',
  description: 'deepseek-flash',
  contextWindow: 272000,
  maxContextWindow: 272000,
  visibility: 'public',
  reasoningLevel: 'medium',
  priority: 143,
  ...overrides,
});

describe('buildCodexClientModelRows', () => {
  it('keeps the effective catalog order and reads origin from the state', () => {
    const rows = buildCodexClientModelRows(buildState());
    expect(rows.map((row) => row.slug)).toEqual(['gpt-5.5', 'deepseek-chat']);
    expect(rows.map((row) => row.origin)).toEqual(['base', 'override']);
    expect(rows[0].contextWindow).toBe(272000);
    expect(rows[1].hasOverride).toBe(true);
    expect(rows[1].patch).toEqual({ display_name: 'DeepSeek Chat' });
  });

  it('appends override-only slugs and marks null patches as removed', () => {
    const rows = buildCodexClientModelRows(
      buildState({
        override: {
          'deepseek-chat': { display_name: 'DeepSeek Chat' },
          'zeta-model': null,
          'alpha-model': { slug: 'alpha-model' },
        },
      })
    );
    expect(rows.map((row) => row.slug)).toEqual([
      'gpt-5.5',
      'deepseek-chat',
      'alpha-model',
      'zeta-model',
    ]);
    expect(rows[2].origin).toBe('custom');
    expect(rows[2].entry).toBeNull();
    expect(rows[3].origin).toBe('removed');
    expect(rows[3].entry).toBeNull();
  });

  it('falls back to base when the catalog has no origin marker for a slug', () => {
    const rows = buildCodexClientModelRows(buildState({ origins: {} }));
    expect(rows.every((row) => row.origin === 'base')).toBe(true);
  });

  it('skips catalog entries without a usable slug', () => {
    const rows = buildCodexClientModelRows(
      buildState({
        models: [{ display_name: 'No slug' }, { slug: '  ' }, { slug: 'kept' }],
        override: {},
      })
    );
    expect(rows.map((row) => row.slug)).toEqual(['kept']);
  });
});

describe('filterCodexClientModelRows and counts', () => {
  const rows = buildCodexClientModelRows(
    buildState({
      override: {
        'deepseek-chat': { display_name: 'DeepSeek Chat' },
        'gone-model': null,
      },
    })
  );

  it('counts every row state', () => {
    expect(countCodexClientModelRows(rows)).toEqual({
      all: 3,
      base: 1,
      override: 1,
      custom: 0,
      removed: 1,
      served: 0,
    });
  });

  it('filters by state and keyword', () => {
    expect(filterCodexClientModelRows(rows, 'removed', '').map((row) => row.slug)).toEqual([
      'gone-model',
    ]);
    expect(filterCodexClientModelRows(rows, 'all', 'deep').map((row) => row.slug)).toEqual([
      'deepseek-chat',
    ]);
    expect(filterCodexClientModelRows(rows, 'base', 'deep')).toHaveLength(0);
  });
});

describe('served models', () => {
  const buildServedState = (overrides: Partial<CodexClientModelsState> = {}) =>
    buildState({
      servedModels: [
        servedModel(),
        servedModel({
          slug: 'deepseek-chat',
          templateSlug: 'deepseek-chat',
          defaultTemplate: false,
          displayName: 'DeepSeek Chat',
          contextWindow: 128000,
          reasoningLevel: 'high',
        }),
      ],
      ...overrides,
    });

  it('lists served models first and reads their values from the summary', () => {
    const rows = buildCodexClientModelRows(buildServedState());
    expect(rows.map((row) => row.slug)).toEqual(['deepseek-flash', 'deepseek-chat', 'gpt-5.5']);
    expect(rows[0].origin).toBe('served');
    expect(rows[0].entry).toBeNull();
    expect(rows[0].displayName).toBe('deepseek-flash');
    expect(rows[0].contextWindow).toBe(272000);
    expect(rows[0].served?.templateSlug).toBe('gpt-5.5');
  });

  it('prefers the catalog entry for served models that have one', () => {
    const rows = buildCodexClientModelRows(buildServedState());
    expect(rows[1].origin).toBe('override');
    expect(rows[1].entry).toMatchObject({ slug: 'deepseek-chat' });
    expect(rows[1].contextWindow).toBeNull();
    expect(rows[1].served?.defaultTemplate).toBe(false);
    expect(rows[2].origin).toBe('base');
    expect(rows[2].served).toBeNull();
  });

  it('keeps counting a served model as removed while its null patch stands', () => {
    const rows = buildCodexClientModelRows(
      buildServedState({ override: { 'deepseek-chat': { display_name: 'DeepSeek Chat' }, 'deepseek-flash': null } })
    );
    const flash = rows.find((row) => row.slug === 'deepseek-flash');
    expect(flash?.origin).toBe('removed');
    expect(flash?.served?.slug).toBe('deepseek-flash');
  });

  it('counts and filters the served rows', () => {
    const rows = buildCodexClientModelRows(buildServedState());
    expect(countCodexClientModelRows(rows)).toMatchObject({ all: 3, served: 1, base: 1, override: 1 });
    expect(filterCodexClientModelRows(rows, 'served', '').map((row) => row.slug)).toEqual([
      'deepseek-flash',
    ]);
  });

  it('keeps the catalog order when the server does not report served models', () => {
    const rows = buildCodexClientModelRows(buildState());
    expect(rows.map((row) => row.slug)).toEqual(['gpt-5.5', 'deepseek-chat']);
    expect(rows.every((row) => row.served === null)).toBe(true);
  });

  it('adopts a served model by inheriting its template and pinning what clients see', () => {
    const patch = buildAdoptedModelPatch(buildState().models[0], servedModel());
    expect(patch).toEqual({
      $inherit: 'gpt-5.5',
      slug: 'deepseek-flash',
      display_name: 'deepseek-flash',
      description: 'deepseek-flash',
      context_window: 272000,
      max_context_window: 272000,
      priority: 143,
    });
  });

  it('leaves the fields the summary omits to the inherited template', () => {
    const patch = buildAdoptedModelPatch(
      buildState().models[0],
      servedModel({
        displayName: '',
        description: '',
        contextWindow: null,
        maxContextWindow: null,
        priority: null,
      })
    );
    expect(patch).toEqual({
      $inherit: 'gpt-5.5',
      slug: 'deepseek-flash',
      display_name: 'deepseek-flash',
    });
  });

  it('resolves the inherit source of a served model through its template', () => {
    const models = buildState().models;
    expect(resolveServedInheritEntry(models, 'gpt-5.5')).toBe(models[0]);
    expect(resolveServedInheritEntry(models, 'ghost-model')).toBe(models[0]);
    expect(resolveServedInheritEntry([{ slug: 'other-model' }], 'ghost-model')).toMatchObject({
      slug: 'other-model',
    });
    expect(resolveServedInheritEntry([], 'ghost-model')).toBeNull();
  });
});

describe('parseOverridePatchInput', () => {
  it('accepts objects and null patches', () => {
    expect(parseOverridePatchInput('{"display_name":"Local"}')).toEqual({
      ok: true,
      patch: { display_name: 'Local' },
    });
    expect(parseOverridePatchInput('null')).toEqual({ ok: true, patch: null });
  });

  it('rejects empty, malformed, and non-object patches', () => {
    expect(parseOverridePatchInput('   ')).toEqual({ ok: false, error: 'empty' });
    expect(parseOverridePatchInput('{')).toMatchObject({ ok: false, error: 'invalid_json' });
    expect(parseOverridePatchInput('[]')).toEqual({ ok: false, error: 'not_object' });
    expect(parseOverridePatchInput('"scalar"')).toEqual({ ok: false, error: 'not_object' });
  });
});

describe('new model patches', () => {
  const models = buildState().models;

  it('prefers the official template as the default inherit source', () => {
    expect(resolveDefaultInheritEntry(models)).toBe(models[0]);
    expect(resolveDefaultInheritSource(models)).toBe('gpt-5.5');
  });

  it('falls back to the first catalog entry and then to nothing', () => {
    expect(resolveDefaultInheritSource([{ slug: 'other-model' }])).toBe('other-model');
    expect(resolveDefaultInheritEntry([])).toBeNull();
    expect(resolveDefaultInheritSource([])).toBe('');
  });

  it('inherits a whole entry and keeps only the identity fields local', () => {
    expect(buildInheritedModelPatch(models[0], ' qwen3-max ')).toEqual({
      $inherit: 'gpt-5.5',
      slug: 'qwen3-max',
      display_name: 'qwen3-max',
    });
  });

  it('seeds the description the catalog requires from the source entry', () => {
    expect(
      buildInheritedModelPatch({ slug: 'base-model', description: ' 官方描述 ' }, 'qwen3-max')
    ).toEqual({
      $inherit: 'base-model',
      slug: 'qwen3-max',
      display_name: 'qwen3-max',
      description: '官方描述',
    });
  });

  it('omits the directive when there is no source to inherit from', () => {
    expect(buildInheritedModelPatch(null, 'qwen3-max')).toEqual({
      slug: 'qwen3-max',
      display_name: 'qwen3-max',
    });
  });

  it('copies the template verbatim when inheritance is unavailable', () => {
    expect(buildModelPatchFromTemplate(models[0], 'qwen3-max')).toEqual({
      ...models[0],
      slug: 'qwen3-max',
      display_name: 'qwen3-max',
    });
    expect(buildModelPatchFromTemplate(null, 'qwen3-max')).toEqual({ slug: 'qwen3-max' });
  });

  it('finds a catalog entry by trimmed slug', () => {
    expect(findModelEntry(models, ' deepseek-chat ')).toMatchObject({ slug: 'deepseek-chat' });
    expect(findModelEntry(models, 'ghost')).toBeNull();
  });
});

describe('formatOverridePatch', () => {
  it('renders an empty object for missing patches and keeps explicit nulls', () => {
    expect(formatOverridePatch(undefined)).toBe('{}');
    expect(formatOverridePatch(null)).toBe('null');
    expect(formatOverridePatch({ a: 1 })).toBe('{\n  "a": 1\n}');
  });
});
