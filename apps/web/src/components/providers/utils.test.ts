import { describe, expect, it } from 'vitest';
import {
  hasModelThinkingLevelsClearMarker,
  hasModelThinkingLevelsEditMarker,
  markModelThinkingLevelsForClear,
  markModelThinkingLevelsForEdit,
  toCommittedModelThinkingSnapshot,
} from '@/types';
import {
  buildApiKeyEntry,
  buildCodexResponsesEndpoint,
  resolveClaudeFingerprintSelection,
  toCommittedOpenAIProviderSnapshot,
} from './utils';

describe('provider utils', () => {
  it('builds Codex responses endpoints from common base URL forms', () => {
    expect(buildCodexResponsesEndpoint('https://api.example.test')).toBe(
      'https://api.example.test/v1/responses'
    );
    expect(buildCodexResponsesEndpoint('https://api.example.test/v1')).toBe(
      'https://api.example.test/v1/responses'
    );
    expect(buildCodexResponsesEndpoint('https://api.example.test/v1/models')).toBe(
      'https://api.example.test/v1/responses'
    );
    expect(buildCodexResponsesEndpoint('https://api.example.test/v1/responses')).toBe(
      'https://api.example.test/v1/responses'
    );
  });

  it('preserves an explicit zero weight when building an OpenAI key entry', () => {
    expect(buildApiKeyEntry({ apiKey: 'key', weight: 0 })).toMatchObject({
      apiKey: 'key',
      weight: 0,
    });
    expect(buildApiKeyEntry()).toHaveProperty('weight', undefined);
  });

  it('strips one-shot model markers from committed OpenAI provider snapshots', () => {
    const markedModel = markModelThinkingLevelsForClear({
      name: 'model',
      thinking: { futureOption: { enabled: true } },
      futureModelOption: 123,
    });
    const editedModel = markModelThinkingLevelsForEdit({
      name: 'edited-model',
      thinking: { levels: ['high'], zero_allowed: true, 'future-option': { enabled: true } },
    });
    const provider = {
      name: 'openai',
      baseUrl: 'https://api.example.com/v1',
      apiKeyEntries: [{ apiKey: 'key' }],
      models: [markedModel, editedModel],
      futureProviderOption: { enabled: true },
    };

    const committed = toCommittedOpenAIProviderSnapshot(provider);

    expect(committed).not.toBe(provider);
    expect(committed.models).not.toBe(provider.models);
    expect(provider.models?.[0]).toBe(markedModel);
    expect(hasModelThinkingLevelsClearMarker(markedModel)).toBe(true);
    expect(hasModelThinkingLevelsEditMarker(editedModel)).toBe(true);
    expect(hasModelThinkingLevelsClearMarker(committed.models?.[0])).toBe(false);
    expect(hasModelThinkingLevelsEditMarker(committed.models?.[1])).toBe(false);
    expect(committed.models?.[1]).toMatchObject({
      name: 'edited-model',
      thinking: { levels: ['high'], 'future-option': { enabled: true } },
    });
    expect(committed.models?.[1]?.thinking?.zero_allowed).toBeUndefined();
    expect(committed.models?.[0]).toMatchObject({
      name: 'model',
      thinking: markedModel.thinking,
      futureModelOption: 123,
    });
    expect(committed.futureProviderOption).toBe(provider.futureProviderOption);
  });

  it('materializes edit semantics while preserving future Thinking fields', () => {
    const markedModel = markModelThinkingLevelsForEdit({
      name: 'thinking-model',
      thinking: {
        levels: ['low'],
        zero_allowed: true,
        'zero-allowed': true,
        zeroAllowed: true,
        dynamic_allowed: true,
        'dynamic-allowed': true,
        dynamicAllowed: true,
        future_option: { enabled: true },
      },
    });

    const committed = toCommittedModelThinkingSnapshot(markedModel);

    expect(committed).toEqual({
      name: 'thinking-model',
      thinking: {
        levels: ['low'],
        future_option: { enabled: true },
      },
    });
    expect(hasModelThinkingLevelsEditMarker(markedModel)).toBe(true);
    expect(hasModelThinkingLevelsEditMarker(committed)).toBe(false);
    expect(hasModelThinkingLevelsClearMarker(committed)).toBe(false);
    expect(markedModel.thinking).toHaveProperty('zero_allowed', true);
    expect(markedModel.thinking).toHaveProperty('dynamicAllowed', true);
  });

  it('keeps legacy Thinking aliases when materializing a clear command', () => {
    const markedModel = markModelThinkingLevelsForClear({
      name: 'thinking-model',
      thinking: {
        zero_allowed: true,
        dynamic_allowed: true,
        future_option: 1,
      },
    });

    const committed = toCommittedModelThinkingSnapshot(markedModel);

    expect(committed).toEqual({
      name: 'thinking-model',
      thinking: {
        zero_allowed: true,
        dynamic_allowed: true,
        future_option: 1,
      },
    });
    expect(hasModelThinkingLevelsClearMarker(committed)).toBe(false);
    expect(hasModelThinkingLevelsEditMarker(committed)).toBe(false);
  });

  it('does not normalize untouched Thinking data', () => {
    const model = {
      name: 'thinking-model',
      thinking: {
        levels: ['HIGH', 'future-level'],
        zero_allowed: true,
        future_option: 123,
      },
    };

    const committed = toCommittedModelThinkingSnapshot(model);

    expect(committed).not.toBe(model);
    expect(committed.thinking).toBe(model.thinking);
    expect(committed.thinking).toEqual(model.thinking);
  });

  it('removes markers from a model without Thinking data', () => {
    const markedModel = markModelThinkingLevelsForEdit({ name: 'thinking-model' });

    const committed = toCommittedModelThinkingSnapshot(markedModel);

    expect(committed).toEqual({ name: 'thinking-model' });
    expect(hasModelThinkingLevelsEditMarker(committed)).toBe(false);
  });
});

describe('resolveClaudeFingerprintSelection', () => {
  it('keeps an untouched fingerprint untouched when Default is re-picked', () => {
    expect(resolveClaudeFingerprintSelection(undefined, '')).toBeUndefined();
    expect(resolveClaudeFingerprintSelection(undefined, 'claude-code-cli')).toBe('claude-code-cli');
  });

  it('only reaches an explicit Default through Claude Code CLI first', () => {
    expect(resolveClaudeFingerprintSelection('claude-code-cli', '')).toBe('');
    expect(resolveClaudeFingerprintSelection('', '')).toBe('');
    expect(resolveClaudeFingerprintSelection('claude-code-cli', 'claude-code-cli')).toBe(
      'claude-code-cli'
    );
  });
});
