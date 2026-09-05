import { describe, expect, it } from 'vitest';
import { markModelThinkingLevelsForClear, markModelThinkingLevelsForEdit } from '@/types';
import { areModelEntriesEqual } from './compare';

const baseline = [
  {
    name: 'model',
    alias: 'model',
    thinking: { levels: ['low', 'high'], future: { enabled: true } },
  },
];

describe('areModelEntriesEqual', () => {
  it('detects thinking level additions and removals', () => {
    expect(
      areModelEntriesEqual(baseline, [
        {
          ...baseline[0],
          thinking: { levels: ['low', 'high', 'max'], future: { enabled: true } },
        },
      ])
    ).toBe(false);

    expect(
      areModelEntriesEqual(baseline, [{ ...baseline[0], thinking: { future: { enabled: true } } }])
    ).toBe(false);
  });

  it('compares nested unknown thinking fields by value', () => {
    expect(
      areModelEntriesEqual(baseline, [
        {
          ...baseline[0],
          thinking: { future: { enabled: false }, levels: ['low', 'high'] },
        },
      ])
    ).toBe(false);

    expect(
      areModelEntriesEqual(baseline, [
        {
          ...baseline[0],
          thinking: { future: { enabled: true }, levels: ['low', 'high'] },
        },
      ])
    ).toBe(true);
  });

  it('detects a newly configured levels array', () => {
    expect(
      areModelEntriesEqual(
        [{ name: 'model', alias: '' }],
        [{ name: 'model', alias: '', thinking: { levels: ['medium'] } }]
      )
    ).toBe(false);
  });

  it('ignores object key order but distinguishes array order and JSON-like types', () => {
    expect(
      areModelEntriesEqual(
        [{ name: 'model', alias: '', thinking: { future: { enabled: true }, levels: ['low'] } }],
        [{ name: 'model', alias: '', thinking: { levels: ['low'], future: { enabled: true } } }]
      )
    ).toBe(true);

    expect(
      areModelEntriesEqual(
        [{ name: 'model', alias: '', thinking: { levels: ['low', 'high'] } }],
        [{ name: 'model', alias: '', thinking: { levels: ['high', 'low'] } }]
      )
    ).toBe(false);
    expect(
      areModelEntriesEqual(
        [{ name: 'model', alias: '', thinking: null }],
        [{ name: 'model', alias: '' }]
      )
    ).toBe(false);
    expect(
      areModelEntriesEqual(
        [{ name: 'model', alias: '', thinking: { levels: ['low'] } }],
        [{ name: 'model', alias: '', thinking: ['low'] }]
      )
    ).toBe(false);
  });

  it('ignores a clear marker when the effective thinking config is unchanged', () => {
    const unchanged = markModelThinkingLevelsForClear({ name: 'model', alias: '' });
    expect(areModelEntriesEqual([{ name: 'model', alias: '' }], [unchanged])).toBe(true);
    expect(
      areModelEntriesEqual(
        [{ name: 'model', alias: '', thinking: { levels: ['high'] } }],
        [unchanged]
      )
    ).toBe(false);
  });

  it('treats an edited legacy capability selection as dirty when aliases would be removed', () => {
    const edited = markModelThinkingLevelsForEdit({
      name: 'model',
      alias: '',
      thinking: { levels: ['low'], zero_allowed: true },
    });

    expect(
      areModelEntriesEqual(
        [{ name: 'model', alias: '', thinking: { levels: ['low'], zero_allowed: true } }],
        [edited]
      )
    ).toBe(false);
  });

  it('does not make an edit marker alone dirty when its committed meaning is unchanged', () => {
    const edited = markModelThinkingLevelsForEdit({
      name: 'model',
      alias: '',
      thinking: { levels: ['low'] },
    });

    expect(
      areModelEntriesEqual([{ name: 'model', alias: '', thinking: { levels: ['low'] } }], [edited])
    ).toBe(true);
  });
});
