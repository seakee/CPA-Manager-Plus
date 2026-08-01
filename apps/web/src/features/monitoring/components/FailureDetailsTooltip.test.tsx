import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it, vi } from 'vitest';
import styles from './FailureDetailsTooltip.module.scss';
import { FailureDetailsTooltip } from './FailureDetailsTooltip';

const renderTooltip = (onCopy = vi.fn()) => {
  let renderer: ReactTestRenderer;
  act(() => {
    renderer = create(
      <FailureDetailsTooltip
        ariaLabel="Failed · HTTP 402 · spending limit"
        statusText="HTTP 402"
        detailLines={['Request failed', '{"code":"spending-limit"}']}
        copyText={'HTTP 402\nRequest failed\n{"code":"spending-limit"}'}
        copyLabel="Copy"
        onCopy={onCopy}
      >
        <span>Failed</span>
      </FailureDetailsTooltip>
    );
  });
  return renderer!;
};

describe('FailureDetailsTooltip', () => {
  it('opens from click, copies the complete details, and closes with Escape', () => {
    const onCopy = vi.fn();
    const renderer = renderTooltip(onCopy);
    const trigger = renderer.root.findByProps({
      'aria-label': 'Failed · HTTP 402 · spending limit',
    });

    act(() => trigger.props.onClick());
    expect(renderer.root.findByProps({ role: 'tooltip' }).props.className).toContain(
      styles.tooltipOpen
    );

    const copyButton = renderer.root.findByProps({ 'aria-label': 'Copy' });
    act(() =>
      copyButton.props.onClick({
        preventDefault: vi.fn(),
        stopPropagation: vi.fn(),
      })
    );
    expect(onCopy).toHaveBeenCalledWith('HTTP 402\nRequest failed\n{"code":"spending-limit"}');

    act(() => trigger.props.onKeyDown({ key: 'Escape', preventDefault: vi.fn() }));
    expect(renderer.root.findByProps({ role: 'tooltip' }).props.className).not.toContain(
      styles.tooltipOpen
    );
  });
});
