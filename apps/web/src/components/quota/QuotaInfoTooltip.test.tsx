import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it, vi } from 'vitest';
import {
  QuotaInfoTooltip,
  resolveQuotaInfoTooltipPosition,
} from './QuotaInfoTooltip';

vi.mock('@/components/ui/icons', () => ({
  IconInfo: (props: Record<string, unknown>) => <span data-testid="icon-info" {...props} />,
}));

describe('resolveQuotaInfoTooltipPosition', () => {
  it('centers on the trigger and keeps a measured box inside the right edge', () => {
    const position = resolveQuotaInfoTooltipPosition(
      { bottom: 140, height: 16, left: 900, right: 916, top: 124, width: 16 },
      960,
      720,
      { width: 200, height: 64 }
    );

    expect(position.placement).toBe('above');
    // center 908 is past maxCenter 848 → clamped; left = 848 - 100
    expect(position.style.left).toBe(748);
    expect(position.style.maxWidth).toBe(320);
    // top = rect.top - offset - height = 124 - 8 - 64
    expect(position.style.top).toBe(52);
  });

  it('clamps to the left margin on a narrow phone viewport', () => {
    const position = resolveQuotaInfoTooltipPosition(
      { bottom: 220, height: 16, left: 8, right: 24, top: 204, width: 16 },
      360,
      640,
      { width: 220, height: 64 }
    );

    expect(position.placement).toBe('above');
    // minCenter = 12 + 110 = 122 → left = 12
    expect(position.style.left).toBe(12);
    expect(position.style.maxWidth).toBe(320);
  });

  it('places the tooltip below when there is more room under the trigger', () => {
    const position = resolveQuotaInfoTooltipPosition(
      { bottom: 40, height: 16, left: 160, right: 176, top: 24, width: 16 },
      360,
      640,
      { width: 180, height: 64 }
    );

    expect(position.placement).toBe('below');
    expect(position.style.top).toBe(48);
  });

  it('right-edge clamp uses measured width instead of maxWidth', () => {
    const position = resolveQuotaInfoTooltipPosition(
      { bottom: 300, height: 16, left: 330, right: 346, top: 284, width: 16 },
      360,
      640,
      { width: 180, height: 56 }
    );

    // center 338 exceeds maxCenter 258 → clamp; left = 258 - 90
    expect(position.style.left).toBe(168);
  });
});

describe('QuotaInfoTooltip', () => {
  it('renders an accessible info trigger for quota detail rows', () => {
    let renderer!: ReactTestRenderer;

    act(() => {
      renderer = create(
        <QuotaInfoTooltip
          ariaLabel="reset credits"
          rows={[
            { key: '1', label: '第 1 次', value: '2026/07/26 12:08' },
            { key: '2', label: '第 2 次', value: '2026/08/01 09:00' },
          ]}
        />
      );
    });

    const trigger = renderer.root.find(
      (node) => node.type === 'span' && node.props['aria-label'] === 'reset credits'
    );
    expect(trigger.props.tabIndex).toBe(0);
    expect(trigger.props.onMouseEnter).toEqual(expect.any(Function));
    expect(trigger.props.onFocus).toEqual(expect.any(Function));
  });
});
