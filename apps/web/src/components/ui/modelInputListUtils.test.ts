import { describe, expect, it } from 'vitest';
import {
  buildThinkingWithLevels,
  entriesToModels,
  getKnownThinkingLevels,
  getEffectiveThinkingLevels,
  getEffectiveThinkingLevelsForEntry,
  getEffectiveThinkingKnownLevels,
  getUnknownThinkingLevels,
  hasInvalidThinkingLevels,
  modelsToEntries,
  normalizeKnownThinkingLevel,
  normalizeThinkingLevelsPreservingOrder,
  rebuildThinkingLevelsDeterministically,
} from './modelInputListUtils';
import {
  THINKING_DYNAMIC_ALLOWED_FIELDS,
  THINKING_ZERO_ALLOWED_FIELDS,
  MODEL_THINKING_LEVELS_CLEAR_MARKER,
  hasModelThinkingLevelsEditMarker,
  hasModelThinkingLevelsClearMarker,
  markModelThinkingLevelsForClear,
  markModelThinkingLevelsForEdit,
  stripModelThinkingLevelsMarkers,
} from '@/types';

describe('modelInputListUtils', () => {
  it('preserves explicit empty modality arrays', () => {
    expect(
      entriesToModels([
        {
          name: 'image-model',
          alias: '',
          inputModalities: [],
          outputModalities: [],
        },
      ])
    ).toEqual([
      {
        name: 'image-model',
        inputModalities: [],
        outputModalities: [],
      },
    ]);
  });

  it('keeps untouched modality fields undefined', () => {
    expect(entriesToModels([{ name: 'text-model', alias: '' }])).toEqual([{ name: 'text-model' }]);
  });

  it('round-trips configured thinking levels through model entries', () => {
    const entries = modelsToEntries([
      { name: 'thinking-model', thinking: { levels: ['low', 'high'] } },
    ]);

    expect(entries[0]?.thinking).toEqual({ levels: ['low', 'high'] });
    expect(entriesToModels(entries)).toEqual([
      { name: 'thinking-model', thinking: { levels: ['low', 'high'] } },
    ]);
  });

  it('preserves unknown thinking fields during round-trip', () => {
    const thinking = {
      levels: ['high'],
      'future-option': { enabled: true },
    };
    const entries = modelsToEntries([{ name: 'future-model', thinking }]);

    expect(entriesToModels(entries)).toEqual([{ name: 'future-model', thinking }]);
  });

  it('keeps unknown thinking levels in their original order', () => {
    expect(getUnknownThinkingLevels(['high', 'ultra', 'experimental'])).toEqual([
      'ultra',
      'experimental',
    ]);
  });

  it('normalizes known thinking levels while preserving unknown values', () => {
    const levels = ['HIGH', ' medium ', 'Low', 'ultra'];

    expect(getKnownThinkingLevels(levels)).toEqual(['high', 'medium', 'low']);
    expect(getUnknownThinkingLevels(levels)).toEqual(['ultra']);
    expect(normalizeKnownThinkingLevel(' High ')).toBe('high');
    expect(normalizeKnownThinkingLevel('experimental')).toBeUndefined();
  });

  it('deduplicates equivalent known levels when rebuilding thinking levels', () => {
    const levels = ['high', 'HIGH', ' high ', 'ultra'];

    expect(
      buildThinkingWithLevels(
        undefined,
        getKnownThinkingLevels(levels),
        getUnknownThinkingLevels(levels)
      )
    ).toEqual({ levels: ['high', 'ultra'] });
  });

  it('normalizes levels in place without changing first-occurrence order', () => {
    expect(
      normalizeThinkingLevelsPreservingOrder(['HIGH', 'future-level', 'low', ' high '])
    ).toEqual(['high', 'future-level', 'low']);
    expect(buildThinkingWithLevels(undefined, ['high', 'low', 'max'], [], ['high', 'low'])).toEqual(
      { levels: ['low', 'high', 'max'] }
    );
  });

  it('rebuilds selected known levels in deterministic serialized order', () => {
    expect(rebuildThinkingLevelsDeterministically([], ['max', 'high'])).toEqual(['high', 'max']);
    expect(
      rebuildThinkingLevelsDeterministically(['HIGH', 'future', 'low'], ['max', 'high', 'low'])
    ).toEqual(['low', 'future', 'high', 'max']);
    expect(
      rebuildThinkingLevelsDeterministically(
        ['high', 'future-a', 'low', 'future-b'],
        ['low', 'high']
      )
    ).toEqual(['low', 'future-a', 'high', 'future-b']);
  });

  it('keeps legacy zero and dynamic flags in the effective selection', () => {
    expect(
      getEffectiveThinkingKnownLevels({
        thinking: { levels: ['low', 'high'], zero_allowed: true, dynamic_allowed: true },
      })
    ).toEqual(['low', 'high', 'none', 'auto']);
    expect(
      getEffectiveThinkingLevelsForEntry({
        thinking: { levels: ['low'], 'zero-allowed': true, 'dynamic-allowed': true },
      })
    ).toEqual(['low', 'none', 'auto']);
    expect(
      getEffectiveThinkingLevelsForEntry({
        thinking: { levels: ['low'], zeroAllowed: true, dynamic_allowed: true },
      })
    ).toEqual(['low', 'none', 'auto']);
    expect(THINKING_ZERO_ALLOWED_FIELDS).toContain('zeroAllowed');
    expect(THINKING_DYNAMIC_ALLOWED_FIELDS).toContain('dynamic-allowed');
  });

  it('uses levels only after an entry is explicitly edited', () => {
    const edited = markModelThinkingLevelsForEdit({
      name: 'model',
      alias: '',
      thinking: { levels: ['low'], zero_allowed: true, dynamic_allowed: true },
    });

    expect(getEffectiveThinkingKnownLevels(edited)).toEqual(['low']);
  });

  it('ignores blank levels when calculating effective levels', () => {
    expect(getEffectiveThinkingLevels(['   '])).toEqual([]);
    expect(getEffectiveThinkingLevels(['   ', 'ultra'])).toEqual(['ultra']);
  });

  it('uses effective levels for model validation and ignores blank model rows', () => {
    expect(hasInvalidThinkingLevels([{ name: '', alias: '', thinking: { levels: ['   '] } }])).toBe(
      false
    );
    expect(
      hasInvalidThinkingLevels([{ name: 'model-a', alias: '', thinking: { levels: ['   '] } }])
    ).toBe(true);
    expect(
      hasInvalidThinkingLevels([
        { name: 'model-a', alias: '', thinking: { levels: ['   ', 'ultra'] } },
      ])
    ).toBe(false);
  });

  it('keeps explicit empty thinking containers through entry conversion', () => {
    const entries = modelsToEntries([{ name: 'empty-thinking-model', thinking: {} }]);

    expect(entriesToModels(entries)).toEqual([{ name: 'empty-thinking-model', thinking: {} }]);
  });

  it('clears known variants while retaining unknown levels', () => {
    const levels = ['HIGH', 'ultra'];

    expect(
      buildThinkingWithLevels({ futureOption: true }, [], getUnknownThinkingLevels(levels))
    ).toEqual({ futureOption: true, levels: ['ultra'] });
  });

  it('preserves the thinking-level clear marker through entry conversion', () => {
    const model = markModelThinkingLevelsForClear({
      name: 'default-model',
      thinking: { futureOption: true },
    });

    const entries = modelsToEntries([model]);
    const roundTripped = entriesToModels(entries)[0];

    expect(hasModelThinkingLevelsClearMarker(entries[0])).toBe(true);
    expect(hasModelThinkingLevelsClearMarker(roundTripped)).toBe(true);
    expect(Reflect.ownKeys(roundTripped ?? {})).toContain(MODEL_THINKING_LEVELS_CLEAR_MARKER);
    expect(
      Object.getOwnPropertyDescriptor(roundTripped ?? {}, MODEL_THINKING_LEVELS_CLEAR_MARKER)
        ?.enumerable
    ).toBe(false);
    expect(JSON.stringify(roundTripped)).not.toContain('model-thinking-levels-clear');
  });

  it('preserves and round-trips the thinking-level edit marker', () => {
    const model = markModelThinkingLevelsForEdit({
      name: 'edited-model',
      thinking: { levels: ['none', 'auto'], zero_allowed: true },
    });

    const entries = modelsToEntries([model]);
    const roundTripped = entriesToModels(entries)[0];

    expect(hasModelThinkingLevelsEditMarker(entries[0])).toBe(true);
    expect(hasModelThinkingLevelsEditMarker(roundTripped)).toBe(true);
    expect(JSON.stringify(roundTripped)).not.toContain('model-thinking-levels-edit');
  });

  it('strips the clear marker into a committed model without mutating the source', () => {
    const model = markModelThinkingLevelsForClear({
      name: 'default-model',
      alias: 'default',
      thinking: { futureOption: { enabled: true } },
      futureModelOption: 123,
    });
    const committed = stripModelThinkingLevelsMarkers(model);

    expect(hasModelThinkingLevelsClearMarker(model)).toBe(true);
    expect(hasModelThinkingLevelsClearMarker(committed)).toBe(false);
    expect(committed).toEqual({
      name: 'default-model',
      alias: 'default',
      thinking: model.thinking,
      futureModelOption: 123,
    });
    expect(committed.thinking).toBe(model.thinking);
  });
});
