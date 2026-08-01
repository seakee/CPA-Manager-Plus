import { describe, expect, it } from 'vitest';
import { entriesToModels } from './modelInputListUtils';

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
    expect(entriesToModels([{ name: 'text-model', alias: '' }])).toEqual([
      { name: 'text-model' },
    ]);
  });
});
