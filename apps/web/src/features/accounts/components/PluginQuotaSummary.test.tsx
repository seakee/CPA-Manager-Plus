import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it, vi } from 'vitest';
import type { PluginQuotaSummaryItem } from '@/types';
import { PluginQuotaSummary } from './PluginQuotaSummary';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string) => key,
    i18n: { language: 'en' },
  }),
}));

const makeItem = (overrides: Partial<PluginQuotaSummaryItem>): PluginQuotaSummaryItem => ({
  key: 'item',
  label: 'Item',
  value: null,
  unit: null,
  format: null,
  currency: null,
  ...overrides,
});

const renderSummary = (
  items: PluginQuotaSummaryItem[],
  variant: 'compact' | 'detail' = 'compact'
) => {
  let renderer!: ReactTestRenderer;
  act(() => {
    renderer = create(<PluginQuotaSummary items={items} variant={variant} locale="en" />);
  });
  return renderer;
};

describe('PluginQuotaSummary', () => {
  it('renders declared label/value facts in compact mode', () => {
    const tree = renderSummary([
      makeItem({ key: 'usage_percent', label: 'Usage (rounded)', value: 32, unit: '%', format: 'number' }),
      makeItem({
        key: 'extra_credits',
        label: 'Extra balance',
        value: 12.5,
        format: 'currency',
        currency: 'USD',
      }),
      makeItem({ key: 'credits', label: 'Credits left', value: 1234, unit: 'credits', format: 'number' }),
    ]);
    const text = JSON.stringify(tree.toJSON());
    expect(text).toContain('Usage (rounded)');
    expect(text).toContain('32%');
    expect(text).toContain('Extra balance');
    expect(text).toContain('$12.50');
    expect(text).toContain('1,234 credits');
    tree.unmount();
  });

  it('renders the same facts in detail mode', () => {
    const tree = renderSummary(
      [
        makeItem({ key: 'usage_percent', label: 'Usage (rounded)', value: 32, unit: '%', format: 'number' }),
        makeItem({ key: 'zero', label: 'Extra balance', value: 0, format: 'currency', currency: 'USD' }),
      ],
      'detail'
    );
    const text = JSON.stringify(tree.toJSON());
    expect(text).toContain('Usage (rounded)');
    expect(text).toContain('32%');
    expect(text).toContain('$0.00');
    tree.unmount();
  });

  it('keeps a valid zero visible instead of an empty placeholder', () => {
    const tree = renderSummary([
      makeItem({ key: 'zero', label: 'Extra balance', value: 0, format: 'currency', currency: 'USD' }),
    ]);
    expect(JSON.stringify(tree.toJSON())).toContain('$0.00');
    tree.unmount();
  });

  it('renders tiny nonzero values distinctly from a reported zero', () => {
    const tree = renderSummary([
      makeItem({ key: 'tiny-credits', label: 'Credits left', value: 0.0004, unit: 'credits', format: 'number' }),
      makeItem({ key: 'tiny-usd', label: 'Extra balance', value: 0.004, format: 'currency', currency: 'USD' }),
      makeItem({ key: 'zero-credits', label: 'Zero credits', value: 0, unit: 'credits', format: 'number' }),
    ]);
    const text = JSON.stringify(tree.toJSON());
    expect(text).toContain('0.0004 credits');
    expect(text).toContain('$0.004');
    // The genuine zero stays visible while tiny values keep their magnitude.
    expect(text).toContain('Zero credits');
    expect(text).toContain('0 credits');
    tree.unmount();
  });

  it('presents missing or invalid values as unknown, never as a fabricated zero', () => {
    const tree = renderSummary([
      makeItem({ key: 'missing', label: 'Missing metric', value: null }),
      makeItem({ key: 'invalid', label: 'Broken metric', value: Number.NaN }),
    ]);
    const text = JSON.stringify(tree.toJSON());
    expect(text).toContain('Missing metric');
    expect(text).toContain('Broken metric');
    expect(text).not.toContain('$0.00');
    tree.unmount();
  });

  it('falls back to plain text for unsupported currency metadata without crashing', () => {
    const tree = renderSummary([
      makeItem({ key: 'odd', label: 'Odd balance', value: 7.25, format: 'currency', currency: 'NOPE' }),
    ]);
    const text = JSON.stringify(tree.toJSON());
    expect(text).toContain('7.25');
    expect(text).toContain('NOPE');
    tree.unmount();
  });

  it('keeps untrusted labels as inert text without creating hostile elements', () => {
    const hostileLabel = '<img src=x onerror="alert(1)">';
    const tree = renderSummary([
      makeItem({
        key: 'hostile',
        label: hostileLabel,
        value: 5,
        unit: '%',
      }),
    ]);
    const item = tree.root.findByProps({ 'data-plugin-quota-summary-item': 'hostile' });
    const collectText = (node: unknown): string => {
      if (typeof node === 'string' || typeof node === 'number') return String(node);
      if (Array.isArray(node)) return node.map(collectText).join('');
      if (node && typeof node === 'object' && 'props' in node) {
        return collectText((node as { props: { children?: unknown } }).props?.children);
      }
      return '';
    };
    // The hostile label stays an inert text child, rendered verbatim.
    expect(collectText(item.props.children)).toBe(`${hostileLabel}5%`);
    // No element may be derived from the label.
    expect(
      tree.root.findAll((node) => typeof node.type === 'string' && node.type !== 'span')
    ).toHaveLength(0);
    tree.unmount();
  });

  it('adds no interactive elements inside inline detail triggers', () => {
    const tree = renderSummary([
      makeItem({ key: 'usage_percent', label: 'Usage (rounded)', value: 32, unit: '%' }),
      makeItem({ key: 'extra', label: 'Extra balance', value: 1, format: 'currency', currency: 'USD' }),
    ]);
    const interactive = tree.root.findAll(
      (node) =>
        node.type === 'button' ||
        node.type === 'a' ||
        node.type === 'input' ||
        node.type === 'select' ||
        node.type === 'textarea'
    );
    expect(interactive).toHaveLength(0);
    tree.unmount();
  });
});
