import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it, vi } from 'vitest';
import { AccountMetricsGrid } from './AccountMetricsGrid';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
}));

describe('AccountMetricsGrid', () => {
  it('renders the six credential status cards in operational order', () => {
    let renderer: ReactTestRenderer;

    act(() => {
      renderer = create(
        <AccountMetricsGrid
          metrics={{
            total: 12,
            available: 6,
            needsAttention: 2,
            quotaRisk: 1,
            disabled: 2,
            unconfirmed: 1,
            needsInspectionAction: 3,
          }}
        />
      );
    });

    const cards = renderer!.root.findAll(
      (node) => typeof node.props['data-summary-icon'] === 'string'
    );
    expect(cards.map((card) => card.props['data-summary-icon'])).toEqual([
      'credential',
      'available',
      'attention',
      'quota-risk',
      'disabled',
      'unconfirmed',
    ]);
    expect(cards.map((card) => card.findByType('strong').children.join(''))).toEqual([
      '12',
      '6',
      '2',
      '1',
      '2',
      '1',
    ]);
    expect(renderer!.root.findAllByType('svg')).toHaveLength(6);
    expect(
      renderer!.root.findAll(
        (node) => node.type === 'svg' && node.props.preserveAspectRatio === 'none'
      )
    ).toHaveLength(0);
    expect(renderer!.root.findByProps({ 'aria-label': 'accounts.metrics_label' })).toBeTruthy();
  });
});
