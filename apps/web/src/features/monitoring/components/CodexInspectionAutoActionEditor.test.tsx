import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it, vi } from 'vitest';
import { CodexInspectionAutoActionEditor } from './CodexInspectionAutoActionEditor';

const t = ((key: string) => key) as never;

describe('CodexInspectionAutoActionEditor', () => {
  it('enables safe recovery when auto execution is enabled without another strategy', () => {
    const onChange = vi.fn();
    const onAutoRecoverChange = vi.fn();
    let renderer: ReactTestRenderer;

    act(() => {
      renderer = create(
        <CodexInspectionAutoActionEditor
          value="none"
          autoRecoverEnabled={false}
          t={t}
          onChange={onChange}
          onAutoRecoverChange={onAutoRecoverChange}
        />
      );
    });

    const buttons = renderer!.root.findAllByType('button');
    act(() => buttons[1].props.onClick());

    expect(onAutoRecoverChange).toHaveBeenCalledWith(true);
    expect(onChange).toHaveBeenCalledWith('enable');
  });

  it('treats legacy enable mode without recovery as disabled automation', () => {
    let renderer: ReactTestRenderer;
    act(() => {
      renderer = create(
        <CodexInspectionAutoActionEditor
          value="enable"
          autoRecoverEnabled={false}
          t={t}
          onChange={vi.fn()}
          onAutoRecoverChange={vi.fn()}
        />
      );
    });

    const buttons = renderer!.root.findAllByType('button');
    expect(buttons[0].props['aria-pressed']).toBe(true);
    expect(buttons[1].props['aria-pressed']).toBe(false);
  });
});
