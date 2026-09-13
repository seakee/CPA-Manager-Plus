import { describe, expect, it } from 'vitest';
import { normalizeCodexClientModelsState, normalizeOrigin } from './codexClientModels';

describe('normalizeOrigin', () => {
  it('keeps known origins and defaults everything else to base', () => {
    expect(normalizeOrigin('override')).toBe('override');
    expect(normalizeOrigin('custom')).toBe('custom');
    expect(normalizeOrigin('base')).toBe('base');
    expect(normalizeOrigin('mystery')).toBe('base');
    expect(normalizeOrigin(undefined)).toBe('base');
  });
});

describe('normalizeCodexClientModelsState', () => {
  it('maps the snake_case payload onto the client shape', () => {
    const state = normalizeCodexClientModelsState({
      source: 'embed',
      revision: 3,
      models: [{ slug: 'gpt-5.5' }, 'ignored'],
      origins: { 'gpt-5.5': 'base', 'x-model': 'nope' },
      override: { 'x-model': { display_name: 'X' } },
      override_path: '/opt/cpa/codex_client_models_override.json',
      override_error: 'rejected',
      override_errors: [
        { slug: 'x-model', path: 'context_window', error: 'unknown path' },
        { slug: 'y-model', path: '', error: 'cycle' },
        { slug: '', path: 'a', error: 'missing slug' },
        { slug: 'z-model', path: 'a', error: '' },
        'ignored',
      ],
    });

    expect(state).toEqual({
      source: 'embed',
      revision: 3,
      models: [{ slug: 'gpt-5.5' }],
      origins: { 'gpt-5.5': 'base', 'x-model': 'base' },
      override: { 'x-model': { display_name: 'X' } },
      overridePath: '/opt/cpa/codex_client_models_override.json',
      overrideError: 'rejected',
      overrideErrors: [
        { slug: 'x-model', path: 'context_window', error: 'unknown path' },
        { slug: 'y-model', path: '', error: 'cycle' },
      ],
      servedModels: null,
    });
  });

  it('normalizes the served model summaries', () => {
    const state = normalizeCodexClientModelsState({
      served_models: [
        {
          slug: ' deepseek-flash ',
          template_slug: 'gpt-5.5',
          default_template: true,
          providers: ['openai-compatibility', 7],
          display_name: 'deepseek-flash',
          description: 'DeepSeek Flash',
          context_window: 272000,
          max_context_window: 272000,
          visibility: 'list',
          default_reasoning_level: 'medium',
          supported_reasoning_levels: [
            { effort: ' low ', description: 'Fast responses with lighter reasoning' },
            { effort: 'high' },
            { effort: '' },
            'ignored',
          ],
          served_fields: { context_window: 400000, max_context_window: 400000 },
          priority: 143,
        },
        { slug: '', template_slug: 'gpt-5.5' },
        {
          slug: 'gpt-image-2',
          context_window: 'nope',
          max_context_window: 'nope',
          providers: 'nope',
        },
        'ignored',
      ],
    });

    expect(state.servedModels).toEqual([
      {
        slug: 'deepseek-flash',
        templateSlug: 'gpt-5.5',
        defaultTemplate: true,
        providers: ['openai-compatibility'],
        displayName: 'deepseek-flash',
        description: 'DeepSeek Flash',
        contextWindow: 272000,
        maxContextWindow: 272000,
        visibility: 'list',
        reasoningLevel: 'medium',
        reasoningLevels: [
          { effort: 'low', description: 'Fast responses with lighter reasoning' },
          { effort: 'high', description: '' },
        ],
        servedFields: { context_window: 400000, max_context_window: 400000 },
        priority: 143,
      },
      {
        slug: 'gpt-image-2',
        templateSlug: '',
        defaultTemplate: false,
        providers: [],
        displayName: '',
        description: '',
        contextWindow: null,
        maxContextWindow: null,
        visibility: '',
        reasoningLevel: '',
        reasoningLevels: [],
        servedFields: {},
        priority: null,
      },
    ]);
  });

  it('tolerates missing or malformed fields', () => {
    expect(normalizeCodexClientModelsState(null)).toEqual({
      source: '',
      revision: 0,
      models: [],
      origins: {},
      override: {},
      overridePath: '',
      overrideError: '',
      overrideErrors: [],
      servedModels: null,
    });
    expect(normalizeCodexClientModelsState({ models: 'nope', revision: 'abc' })).toMatchObject({
      models: [],
      revision: 0,
    });
  });
});
