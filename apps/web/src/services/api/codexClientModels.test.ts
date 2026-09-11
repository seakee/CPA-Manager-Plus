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
    });
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
    });
    expect(normalizeCodexClientModelsState({ models: 'nope', revision: 'abc' })).toMatchObject({
      models: [],
      revision: 0,
    });
  });
});
