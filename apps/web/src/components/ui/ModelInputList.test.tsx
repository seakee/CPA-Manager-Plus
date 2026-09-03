import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it } from 'vitest';
import { MODEL_THINKING_LEVELS_CLEAR_MARKER } from '@/types';
import { ModelInputList } from './ModelInputList';
import { entriesToModels, type ModelEntry } from './modelInputListUtils';

const findInputByLabelSuffix = (renderer: ReactTestRenderer, suffix: string) =>
  renderer.root
    .findAllByType('input')
    .find((input) => String(input.props['aria-label'] ?? '').endsWith(suffix));

const findButtonByText = (renderer: ReactTestRenderer, text: string) =>
  renderer.root
    .findAllByType('button')
    .find((button) => button.findAllByType('span').some((span) => span.children.join('') === text));

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
    expect(renderer.root.findByProps({ 'aria-label': 'Input modalities' }).props.value).toBe('');
  });

  it('leaves levels unconfigured and creates an empty custom levels array on selection', () => {
    let entries: ModelEntry[] = [{ name: 'thinking-model', alias: '' }];
    let renderer!: ReactTestRenderer;

    const render = () => (
      <ModelInputList
        entries={entries}
        onChange={(next) => {
          entries = next;
          renderer.update(render());
        }}
        showThinkingLevels
        thinkingLabel="Thinking levels"
        thinkingTooltip="Thinking help"
        thinkingDefaultLabel="Do not explicitly configure levels"
        thinkingCustomLabel="Custom levels"
        thinkingAllowedLevelsLabel="Allowed thinking levels"
        thinkingRequiredError="Select at least one thinking level"
      />
    );

    act(() => {
      renderer = create(render());
    });

    expect(
      findInputByLabelSuffix(renderer, 'Do not explicitly configure levels')?.props.checked
    ).toBe(true);
    expect(findInputByLabelSuffix(renderer, 'Custom levels')?.props.checked).toBe(false);

    act(() => {
      findInputByLabelSuffix(renderer, 'Custom levels')?.props.onChange();
    });

    expect(entries[0]?.thinking).toEqual({ levels: [] });
    expect(renderer.root.findByProps({ role: 'alert' }).children.join('')).toContain(
      'Select at least one thinking level'
    );
  });

  it('updates known levels while preserving unknown levels and thinking fields', () => {
    let entries: ModelEntry[] = [
      {
        name: 'future-model',
        alias: '',
        thinking: {
          levels: ['HIGH', ' medium ', 'ultra'],
          'future-option': { enabled: true },
        },
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
        showThinkingLevels
        thinkingDefaultLabel="Do not explicitly configure levels"
        thinkingCustomLabel="Custom levels"
        thinkingAllowedLevelsLabel="Allowed thinking levels"
      />
    );

    act(() => {
      renderer = create(render());
    });

    expect(findInputByLabelSuffix(renderer, ' high')?.props.checked).toBe(true);
    expect(findInputByLabelSuffix(renderer, ' medium')?.props.checked).toBe(true);
    expect(
      renderer.root.findAllByType('span').some((node) => node.children.join('') === 'ultra')
    ).toBe(true);
    expect(
      renderer.root.findAllByType('span').some((node) => node.children.join('') === 'HIGH')
    ).toBe(false);
    expect(
      renderer.root.findAllByType('span').some((node) => node.children.join('') === ' medium ')
    ).toBe(false);

    act(() => {
      findInputByLabelSuffix(renderer, ' high')?.props.onChange({
        target: { checked: false },
      });
    });
    expect(entries[0]?.thinking).toEqual({
      levels: ['medium', 'ultra'],
      'future-option': { enabled: true },
    });

    act(() => {
      findInputByLabelSuffix(renderer, ' max')?.props.onChange({
        target: { checked: true },
      });
    });
    expect(entries[0]?.thinking).toEqual({
      levels: ['medium', 'ultra', 'max'],
      'future-option': { enabled: true },
    });
  });

  it('preserves an explicitly empty thinking container through a mode round-trip', () => {
    let entries: ModelEntry[] = [{ name: 'empty-thinking-model', alias: '', thinking: {} }];
    let renderer!: ReactTestRenderer;

    const render = () => (
      <ModelInputList
        entries={entries}
        onChange={(next) => {
          entries = next;
          renderer.update(render());
        }}
        showThinkingLevels
        thinkingDefaultLabel="Do not explicitly configure levels"
        thinkingCustomLabel="Custom levels"
      />
    );

    act(() => {
      renderer = create(render());
    });
    act(() => {
      findInputByLabelSuffix(renderer, 'Custom levels')?.props.onChange();
    });
    act(() => {
      findInputByLabelSuffix(renderer, 'Do not explicitly configure levels')?.props.onChange();
    });

    expect(entries[0]?.thinking).toEqual({});
    expect(entriesToModels(entries)).toEqual([{ name: 'empty-thinking-model', thinking: {} }]);
  });

  it('preserves existing order when selecting all and clearing known levels', () => {
    let entries: ModelEntry[] = [
      {
        name: 'ordered-model',
        alias: '',
        thinking: { levels: ['high', 'future-a', 'low', 'future-b'] },
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
        showThinkingLevels
        thinkingDefaultLabel="Do not explicitly configure levels"
        thinkingCustomLabel="Custom levels"
        thinkingSelectAllLabel="Select all"
        thinkingClearLabel="Clear known levels"
      />
    );

    act(() => {
      renderer = create(render());
    });
    act(() => {
      findButtonByText(renderer, 'Select all')?.props.onClick();
    });
    expect(entries[0]?.thinking?.levels).toEqual([
      'minimal',
      'future-a',
      'low',
      'future-b',
      'medium',
      'high',
      'xhigh',
      'max',
      'none',
      'auto',
    ]);

    act(() => {
      findButtonByText(renderer, 'Clear known levels')?.props.onClick();
    });
    expect(entries[0]?.thinking?.levels).toEqual(['future-a', 'future-b']);
  });

  it('does not show or block thinking validation for a blank model row', () => {
    let entries: ModelEntry[] = [{ name: '', alias: '', thinking: { levels: [] } }];
    let renderer!: ReactTestRenderer;

    const render = () => (
      <ModelInputList
        entries={entries}
        onChange={(next) => {
          entries = next;
          renderer.update(render());
        }}
        showThinkingLevels
        thinkingRequiredError="Select at least one thinking level"
      />
    );

    act(() => {
      renderer = create(render());
    });
    expect(renderer.root.findAllByProps({ role: 'alert' })).toHaveLength(0);

    act(() => {
      renderer.root.findAllByType('input')[0]?.props.onChange({ target: { value: 'model-a' } });
    });
    expect(renderer.root.findAllByProps({ role: 'alert' })).toHaveLength(1);
  });

  it('ignores whitespace-only levels for unknown display and validation', () => {
    let entries: ModelEntry[] = [
      { name: 'whitespace-model', alias: '', thinking: { levels: ['   '] } },
    ];
    let renderer!: ReactTestRenderer;

    const render = () => (
      <ModelInputList
        entries={entries}
        onChange={(next) => {
          entries = next;
          renderer.update(render());
        }}
        showThinkingLevels
        thinkingDefaultLabel="Do not explicitly configure levels"
        thinkingCustomLabel="Custom levels"
        thinkingRequiredError="Select at least one thinking level"
      />
    );

    act(() => {
      renderer = create(render());
    });

    expect(renderer.root.findAllByProps({ role: 'alert' })).toHaveLength(1);
    expect(
      renderer.root.findAllByType('span').some((node) => node.children.join('') === '   ')
    ).toBe(false);
  });

  it('clears known level variants while retaining unknown levels', () => {
    let entries: ModelEntry[] = [
      { name: 'future-model', alias: '', thinking: { levels: ['HIGH', 'ultra'] } },
    ];
    let renderer!: ReactTestRenderer;

    const render = () => (
      <ModelInputList
        entries={entries}
        onChange={(next) => {
          entries = next;
          renderer.update(render());
        }}
        showThinkingLevels
        thinkingDefaultLabel="Do not explicitly configure levels"
        thinkingCustomLabel="Custom levels"
        thinkingClearLabel="Clear known levels"
      />
    );

    act(() => {
      renderer = create(render());
    });
    act(() => {
      findButtonByText(renderer, 'Clear known levels')?.props.onClick();
    });

    expect(entries[0]?.thinking).toEqual({ levels: ['ultra'] });
  });

  it('uses deterministic order regardless of known-level click order', () => {
    const run = (first: 'high' | 'max', second: 'high' | 'max') => {
      let entries: ModelEntry[] = [{ name: 'ordered-model', alias: '', thinking: { levels: [] } }];
      let renderer!: ReactTestRenderer;

      const render = () => (
        <ModelInputList
          entries={entries}
          onChange={(next) => {
            entries = next;
            renderer.update(render());
          }}
          showThinkingLevels
          thinkingCustomLabel="Custom levels"
        />
      );

      act(() => {
        renderer = create(render());
      });
      act(() => {
        findInputByLabelSuffix(renderer, ` ${first}`)?.props.onChange({
          target: { checked: true },
        });
      });
      act(() => {
        findInputByLabelSuffix(renderer, ` ${second}`)?.props.onChange({
          target: { checked: true },
        });
      });
      const result = entries[0]?.thinking?.levels;
      renderer.unmount();
      return result;
    };

    expect(run('max', 'high')).toEqual(['high', 'max']);
    expect(run('high', 'max')).toEqual(['high', 'max']);
  });

  it('includes legacy zero and dynamic flags until custom levels are edited', () => {
    let entries: ModelEntry[] = [
      {
        name: 'legacy-model',
        alias: '',
        thinking: { levels: ['low'], zero_allowed: true, dynamicAllowed: true },
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
        showThinkingLevels
        thinkingCustomLabel="Custom levels"
      />
    );

    act(() => {
      renderer = create(render());
    });
    expect(findInputByLabelSuffix(renderer, ' none')?.props.checked).toBe(true);
    expect(findInputByLabelSuffix(renderer, ' auto')?.props.checked).toBe(true);

    act(() => {
      findInputByLabelSuffix(renderer, ' none')?.props.onChange({ target: { checked: false } });
    });
    expect(findInputByLabelSuffix(renderer, ' none')?.props.checked).toBe(false);
    expect(entries[0]?.thinking).toEqual({
      levels: ['low', 'auto'],
      zero_allowed: true,
      dynamicAllowed: true,
    });
  });

  it('turns legacy-only capabilities into explicit levels when entering custom mode', () => {
    let entries: ModelEntry[] = [
      {
        name: 'legacy-only-model',
        alias: '',
        thinking: { zero_allowed: true, dynamic_allowed: true, future: 123 },
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
        showThinkingLevels
        thinkingDefaultLabel="Do not explicitly configure levels"
        thinkingCustomLabel="Custom levels"
      />
    );

    act(() => {
      renderer = create(render());
    });
    expect(
      findInputByLabelSuffix(renderer, 'Do not explicitly configure levels')?.props.checked
    ).toBe(true);

    act(() => {
      findInputByLabelSuffix(renderer, 'Custom levels')?.props.onChange();
    });

    expect(entries[0]?.thinking).toEqual({
      levels: ['none', 'auto'],
      zero_allowed: true,
      dynamic_allowed: true,
      future: 123,
    });
    expect(renderer.root.findAllByProps({ role: 'alert' })).toHaveLength(0);
  });

  it('removes only levels when leaving them unconfigured and marks an empty thinking clear', () => {
    let entries: ModelEntry[] = [
      { name: 'future-model', alias: '', thinking: { levels: ['high'], future: true } },
    ];
    let renderer!: ReactTestRenderer;

    const render = () => (
      <ModelInputList
        entries={entries}
        onChange={(next) => {
          entries = next;
          renderer.update(render());
        }}
        showThinkingLevels
        thinkingDefaultLabel="Do not explicitly configure levels"
        thinkingCustomLabel="Custom levels"
      />
    );

    act(() => {
      renderer = create(render());
    });
    act(() => {
      findInputByLabelSuffix(renderer, 'Do not explicitly configure levels')?.props.onChange();
    });

    expect(entries[0]?.thinking).toEqual({ future: true });
    expect(entries[0]?.[MODEL_THINKING_LEVELS_CLEAR_MARKER]).toBe(true);

    entries = [{ name: 'configured-model', alias: '', thinking: { levels: ['high'] } }];
    act(() => {
      renderer.update(render());
    });
    act(() => {
      findInputByLabelSuffix(renderer, 'Do not explicitly configure levels')?.props.onChange();
    });

    expect(entries[0]?.thinking).toBeUndefined();
    expect(entries[0]?.[MODEL_THINKING_LEVELS_CLEAR_MARKER]).toBe(true);
  });

  it('gives each model thinking control a distinct accessible name', () => {
    let renderer!: ReactTestRenderer;
    act(() => {
      renderer = create(
        <ModelInputList
          entries={[
            { name: 'model-a', alias: '', thinking: { levels: ['high'] } },
            { name: 'model-b', alias: '', thinking: { levels: ['high'] } },
          ]}
          onChange={() => {}}
          showThinkingLevels
        />
      );
    });

    const groups = renderer.root.findAllByProps({ role: 'radiogroup' });
    expect(groups).toHaveLength(2);
    expect(groups[0]?.props['aria-label']).not.toBe(groups[1]?.props['aria-label']);

    const customRadios = renderer.root
      .findAllByType('input')
      .filter(
        (input) =>
          input.props.type === 'radio' && input.props['aria-label'].endsWith('Custom levels')
      );
    expect(customRadios.map((input) => input.props['aria-label'])).toEqual([
      expect.stringContaining('model-a'),
      expect.stringContaining('model-b'),
    ]);

    const highCheckboxes = renderer.root
      .findAllByType('input')
      .filter(
        (input) => input.props.type === 'checkbox' && input.props['aria-label'].endsWith(' high')
      );
    expect(highCheckboxes.map((input) => input.props['aria-label'])).toEqual([
      expect.stringContaining('model-a'),
      expect.stringContaining('model-b'),
    ]);
  });

  it('uses a non-empty accessible fallback for a blank model row', () => {
    let renderer!: ReactTestRenderer;
    act(() => {
      renderer = create(
        <ModelInputList
          entries={[{ name: '', alias: '' }]}
          onChange={() => {}}
          showThinkingLevels
        />
      );
    });

    const group = renderer.root.findByProps({ role: 'radiogroup' });
    expect(group.props['aria-label']).toContain('Model 1');
    expect(
      renderer.root
        .findAllByType('input')
        .filter((input) => input.props.type === 'radio')
        .every((input) => String(input.props['aria-label']).trim().length > 0)
    ).toBe(true);
  });
});
