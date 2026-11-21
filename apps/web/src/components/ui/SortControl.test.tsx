import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it } from 'vitest';
import { SortControl, type SortControlOption } from './SortControl';

const options: ReadonlyArray<SortControlOption<'created' | 'recent' | 'calls'>> = [
  { value: 'created', label: 'Created' },
  { value: 'recent', label: 'Last used' },
  { value: 'calls', label: 'Calls (24h)' },
];

describe('SortControl', () => {
  it('renders current field label, opens listbox, selects option, and toggles direction', () => {
    const selected: string[] = [];
    let toggled = 0;
    let renderer: ReactTestRenderer;

    act(() => {
      renderer = create(
        <SortControl
          options={options}
          value="created"
          direction="desc"
          onChangeValue={(next) => selected.push(next)}
          onToggleDirection={() => {
            toggled += 1;
          }}
          ariaLabel="Sort by: Created"
          directionLabel="Sort descending"
        />
      );
    });

    const trigger = renderer!.root.findByProps({ 'aria-label': 'Sort by: Created' });
    expect(trigger.props['aria-expanded']).toBe(false);
    expect(renderer!.root.findAllByProps({ role: 'listbox' })).toHaveLength(0);

    act(() => {
      trigger.props.onClick();
    });

    expect(
      renderer!.root.findByProps({ 'aria-label': 'Sort by: Created' }).props['aria-expanded']
    ).toBe(true);
    const listItems = renderer!.root.findAllByProps({ role: 'option' });
    expect(listItems).toHaveLength(3);
    expect(listItems[0].props['aria-selected']).toBe(true);

    act(() => {
      listItems[2].props.onClick();
    });

    expect(selected).toEqual(['calls']);
    expect(renderer!.root.findAllByProps({ role: 'listbox' })).toHaveLength(0);

    act(() => {
      renderer!.root.findByProps({ 'aria-label': 'Sort descending' }).props.onClick();
    });
    expect(toggled).toBe(1);
  });

  it('respects directionDisabled when default order has no direction toggle', () => {
    let toggled = 0;
    let renderer: ReactTestRenderer;

    act(() => {
      renderer = create(
        <SortControl
          options={options}
          value="created"
          direction="desc"
          onChangeValue={() => {}}
          onToggleDirection={() => {
            toggled += 1;
          }}
          ariaLabel="Sort by: Created"
          directionLabel="Sort descending"
          directionDisabled
        />
      );
    });

    const directionButton = renderer!.root.findByProps({ 'aria-label': 'Sort descending' });
    expect(directionButton.props.disabled).toBe(true);
    expect(toggled).toBe(0);
  });
});
