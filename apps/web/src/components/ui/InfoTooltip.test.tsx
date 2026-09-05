import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it, vi } from 'vitest';
import { InfoTooltip, resolveInfoTooltipPosition } from './InfoTooltip';

describe('InfoTooltip', () => {
  it('opens from keyboard focus with an accessible tooltip', () => {
    let renderer!: ReactTestRenderer;
    act(() => {
      renderer = create(<InfoTooltip ariaLabel="Thinking levels information" content="Details" />);
    });

    const trigger = renderer.root.findByProps({ 'data-info-tooltip-trigger': 'true' });
    expect(trigger.props['aria-describedby']).toBeUndefined();

    act(() => {
      trigger.props.onFocus();
    });

    const tooltip = renderer.root.findByProps({ role: 'tooltip' });
    expect(tooltip.children.join('')).toContain('Details');
    expect(trigger.props['aria-describedby']).toBe(tooltip.props.id);

    act(() => {
      renderer.root.findByProps({ 'data-info-tooltip-trigger': 'true' }).props.onBlur();
    });
    expect(renderer.root.findAllByProps({ role: 'tooltip' })).toHaveLength(0);
    expect(
      renderer.root.findByProps({ 'data-info-tooltip-trigger': 'true' }).props['aria-describedby']
    ).toBeUndefined();
  });

  it('keeps the tooltip open while the pointer moves from the trigger to the tooltip', () => {
    vi.useFakeTimers();
    let renderer!: ReactTestRenderer;
    try {
      act(() => {
        renderer = create(<InfoTooltip ariaLabel="Information" content="Details" />);
      });

      const trigger = renderer.root.findByProps({ 'data-info-tooltip-trigger': 'true' });
      act(() => {
        trigger.props.onMouseEnter();
      });
      expect(renderer.root.findAllByProps({ role: 'tooltip' })).toHaveLength(1);

      const tooltip = renderer.root.findByProps({ role: 'tooltip' });
      act(() => {
        trigger.props.onMouseLeave();
      });
      expect(renderer.root.findAllByProps({ role: 'tooltip' })).toHaveLength(1);

      act(() => {
        tooltip.props.onMouseEnter();
        vi.advanceTimersByTime(120);
      });
      expect(renderer.root.findAllByProps({ role: 'tooltip' })).toHaveLength(1);

      act(() => {
        tooltip.props.onMouseLeave();
        vi.advanceTimersByTime(120);
      });
      expect(renderer.root.findAllByProps({ role: 'tooltip' })).toHaveLength(0);
    } finally {
      vi.useRealTimers();
    }
  });

  it('closes on Escape and prevents the browser default action', () => {
    let renderer!: ReactTestRenderer;
    act(() => {
      renderer = create(<InfoTooltip ariaLabel="Information" content="Details" />);
    });
    const preventDefault = vi.fn();
    const stopPropagation = vi.fn();
    const trigger = renderer.root.findByProps({ 'data-info-tooltip-trigger': 'true' });

    act(() => {
      trigger.props.onFocus();
    });
    act(() => {
      renderer.root.findByProps({ 'data-info-tooltip-trigger': 'true' }).props.onKeyDown({
        key: 'Escape',
        preventDefault,
        stopPropagation,
      });
    });

    expect(preventDefault).toHaveBeenCalledTimes(1);
    expect(stopPropagation).toHaveBeenCalledTimes(1);
    expect(renderer.root.findAllByProps({ role: 'tooltip' })).toHaveLength(0);
  });

  it('consumes Escape before a parent overlay handler', () => {
    const addEventListener = vi.fn();
    const removeEventListener = vi.fn();
    vi.stubGlobal('window', {
      addEventListener,
      removeEventListener,
      innerWidth: 1024,
      innerHeight: 768,
    });
    let renderer!: ReactTestRenderer;
    try {
      act(() => {
        renderer = create(<InfoTooltip ariaLabel="Information" content="Details" />);
      });
      const trigger = renderer.root.findByProps({ 'data-info-tooltip-trigger': 'true' });
      act(() => {
        trigger.props.onMouseEnter();
      });
      const keydownCall = addEventListener.mock.calls.find(
        ([eventName]) => eventName === 'keydown'
      );
      const keydownHandler = keydownCall?.[1] as
        | ((event: {
            key: string;
            preventDefault: () => void;
            stopPropagation: () => void;
          }) => void)
        | undefined;
      expect(keydownCall?.[2]).toBe(true);
      expect(keydownHandler).toBeTypeOf('function');
      const stopPropagation = vi.fn();
      act(() => {
        keydownHandler?.({
          key: 'Escape',
          preventDefault: vi.fn(),
          stopPropagation,
        });
      });

      expect(renderer.root.findAllByProps({ role: 'tooltip' })).toHaveLength(0);
      expect(stopPropagation).toHaveBeenCalledTimes(1);
    } finally {
      act(() => {
        renderer?.unmount();
      });
      expect(removeEventListener).toHaveBeenCalledWith('keydown', expect.any(Function), true);
      vi.unstubAllGlobals();
    }
  });

  it('closes a mouse-open tooltip from a window Escape event', () => {
    if (typeof window === 'undefined') {
      const addEventListener = vi.fn();
      const removeEventListener = vi.fn();
      vi.stubGlobal('window', {
        addEventListener,
        removeEventListener,
        innerWidth: 1024,
        innerHeight: 768,
      });
      let renderer!: ReactTestRenderer;
      try {
        act(() => {
          renderer = create(<InfoTooltip ariaLabel="Information" content="Details" />);
        });
        const trigger = renderer.root.findByProps({ 'data-info-tooltip-trigger': 'true' });
        act(() => {
          trigger.props.onMouseEnter();
        });
        const keydownHandler = addEventListener.mock.calls.find(
          ([eventName]) => eventName === 'keydown'
        )?.[1] as ((event: { key: string; preventDefault: () => void }) => void) | undefined;
        expect(keydownHandler).toBeTypeOf('function');
        act(() => {
          keydownHandler?.({ key: 'Escape', preventDefault: vi.fn() });
        });
        expect(renderer.root.findAllByProps({ role: 'tooltip' })).toHaveLength(0);
      } finally {
        act(() => {
          renderer?.unmount();
        });
        vi.unstubAllGlobals();
      }
      return;
    }
    let renderer!: ReactTestRenderer;
    act(() => {
      renderer = create(<InfoTooltip ariaLabel="Information" content="Details" />);
    });
    const trigger = renderer.root.findByProps({ 'data-info-tooltip-trigger': 'true' });

    act(() => {
      trigger.props.onMouseEnter();
    });
    expect(renderer.root.findAllByProps({ role: 'tooltip' })).toHaveLength(1);

    act(() => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    });
    expect(renderer.root.findAllByProps({ role: 'tooltip' })).toHaveLength(0);
  });

  it('keeps the tooltip within the viewport at narrow widths', () => {
    const position = resolveInfoTooltipPosition(
      { bottom: 100, left: 4, top: 84, width: 16 },
      320,
      640
    );

    expect(position.style.left).toBe(160);
    expect(position.style.maxWidth).toBe(296);
  });
});
