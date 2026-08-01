import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import {
  OpenAIKeyTestStatusIndicator,
  resolveOpenAIKeyTestTooltipPosition,
} from './OpenAIKeyTestStatusIndicator';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string) => key,
  }),
}));

describe('OpenAIKeyTestStatusIndicator', () => {
  let renderer: ReactTestRenderer | null = null;

  beforeEach(() => {
    renderer?.unmount();
    renderer = null;
  });

  it('shows a tooltip on focus when an error message exists', () => {
    act(() => {
      renderer = create(<OpenAIKeyTestStatusIndicator status="error" message="invalid api key" />);
    });

    const trigger = renderer!.root.find(
      (node) =>
        typeof node.type === 'string' &&
        node.type === 'span' &&
        typeof node.props.className === 'string' &&
        node.props.className.includes('keyStatusTriggerInteractive')
    );

    act(() => {
      trigger.props.onFocus();
    });

    const tooltip = renderer!.root.findByProps({ role: 'tooltip' });
    const tooltipText = tooltip.find(
      (node) =>
        typeof node.type === 'string' &&
        node.type === 'span' &&
        typeof node.props.className === 'string' &&
        node.props.className.includes('keyStatusTooltipText')
    );
    expect(tooltipText.children.join('')).toContain('invalid api key');
  });

  it('preserves multiline response details in the tooltip', () => {
    const message = '401 Forbidden\n\nBody:\n{\n  "error": {\n    "code": 16\n  }\n}';
    act(() => {
      renderer = create(<OpenAIKeyTestStatusIndicator status="error" message={message} />);
    });

    const trigger = renderer!.root.find(
      (node) =>
        typeof node.type === 'string' &&
        node.type === 'span' &&
        typeof node.props.className === 'string' &&
        node.props.className.includes('keyStatusTriggerInteractive')
    );

    act(() => {
      trigger.props.onFocus();
    });

    const tooltip = renderer!.root.findByProps({ role: 'tooltip' });
    const tooltipText = tooltip.find(
      (node) =>
        typeof node.type === 'string' &&
        node.type === 'span' &&
        typeof node.props.className === 'string' &&
        node.props.className.includes('keyStatusTooltipText')
    );
    expect(tooltipText.children.join('')).toBe(message);
    expect(tooltip.props.onMouseEnter).toBeTypeOf('function');
    expect(tooltip.props.onMouseLeave).toBeTypeOf('function');
  });

  it('keeps the idle icon non-interactive before any test runs', () => {
    act(() => {
      renderer = create(<OpenAIKeyTestStatusIndicator status="idle" message="" />);
    });

    const trigger = renderer!.root.find(
      (node) =>
        typeof node.type === 'string' &&
        node.type === 'span' &&
        typeof node.props.className === 'string' &&
        node.props.className.includes('keyStatusTrigger')
    );
    expect(trigger.props.tabIndex).toBe(-1);
    expect(() => renderer!.root.findByProps({ role: 'tooltip' })).toThrow();
  });

  it('keeps the success icon non-interactive even without a message', () => {
    act(() => {
      renderer = create(<OpenAIKeyTestStatusIndicator status="success" message="" />);
    });

    const trigger = renderer!.root.find(
      (node) =>
        typeof node.type === 'string' &&
        node.type === 'span' &&
        typeof node.props.className === 'string' &&
        node.props.className.includes('keyStatusTrigger')
    );
    expect(trigger.props.tabIndex).toBe(-1);
    expect(() => renderer!.root.findByProps({ role: 'tooltip' })).toThrow();
  });

  it('keeps a long tooltip inside the viewport near the left edge', () => {
    const position = resolveOpenAIKeyTestTooltipPosition(
      { bottom: 120, height: 16, left: 20, top: 104, width: 16 },
      320,
      640
    );

    expect(position.placement).toBe('below');
    expect(position.style.left).toBe(160);
    expect(position.style.maxWidth).toBe(296);
    expect(position.style.top).toBe(130);
  });

  it('places the tooltip above when there is more room above the trigger', () => {
    const position = resolveOpenAIKeyTestTooltipPosition(
      { bottom: 620, height: 16, left: 292, top: 604, width: 16 },
      320,
      640
    );

    expect(position.placement).toBe('above');
    expect(position.style.left).toBe(160);
    expect(position.style.bottom).toBe(46);
    expect(position.style.maxHeight).toBe(360);
  });

  it('uses a readable desktop width for structured errors', () => {
    const position = resolveOpenAIKeyTestTooltipPosition(
      { bottom: 120, height: 16, left: 492, top: 104, width: 16 },
      1024,
      768
    );

    expect(position.style.maxWidth).toBe(420);
  });
});
