import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it } from 'vitest';
import { ModelInputList } from './ModelInputList';
import type { ModelEntry } from './modelInputListUtils';

describe('ModelInputList', () => {
  it('updates and clears modalities without waiting for blur', () => {
    let entries: ModelEntry[] = [
      {
        name: 'image-model',
        alias: '',
        inputModalities: ['text', 'image'],
        outputModalities: ['image'],
      },
    ];
    let renderer!: ReactTestRenderer;

    const render = () => (
      <ModelInputList
        entries={entries}
        onChange={(next) => {
          entries = next;
          renderer.update(render());
        }}
        showModalities
        inputModalitiesPlaceholder="Input modalities"
        outputModalitiesPlaceholder="Output modalities"
      />
    );

    act(() => {
      renderer = create(render());
    });

    const input = renderer.root.findByProps({ 'aria-label': 'Input modalities' });
    act(() => {
      input.props.onChange({ target: { value: 'text, audio' } });
    });
    expect(entries[0]?.inputModalities).toEqual(['text', 'audio']);

    const updatedInput = renderer.root.findByProps({ 'aria-label': 'Input modalities' });
    act(() => {
      updatedInput.props.onChange({ target: { value: '' } });
    });
    expect(entries[0]?.inputModalities).toEqual([]);
    expect(
      renderer.root.findByProps({ 'aria-label': 'Input modalities' }).props.value
    ).toBe('');
  });
});
