import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it, vi } from 'vitest';
import type { AccountQuotaDisplayWindow } from '@/features/accounts/model/accountQuotaDisplayWindows';
import type {
  AccountQuotaLifecycleBarOverride,
  AntigravityQuotaMatrix,
} from '@/features/accounts/model/accountsPagePresentation';
import { AccountQuotaMatrix } from './AccountQuotaMatrix';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

vi.mock('react-i18next', () => ({
  initReactI18next: { type: '3rdParty', init: () => {} },
  useTranslation: () => ({
    t: (key: string) => key,
    i18n: { language: 'en-US' },
  }),
}));

const makeQuotaWindow = (
  overrides: Partial<AccountQuotaDisplayWindow> = {}
): AccountQuotaDisplayWindow =>
  ({
    key: 'test-window',
    label: '5 Hour Limit',
    kind: 'five_hour',
    remainingPercent: 50,
    usedPercent: 50,
    resetLabel: 'in 2 hours',
    resetAccuracy: 'exact',
    limitWindowSeconds: 5 * 60 * 60,
    resetAtMs: Date.now() + 2 * 60 * 60 * 1000,
    fromMs: Date.now() - 3 * 60 * 60 * 1000,
    toMs: Date.now(),
    source: 'antigravity',
    observationSource: 'response_header',
    observedAtMs: Date.now(),
    windowMode: 'fixed',
    groupLabel: 'Gemini Models',
    ...overrides,
  }) as AccountQuotaDisplayWindow;

const renderMatrix = (
  accountKey: string,
  matrix: AntigravityQuotaMatrix,
  lifecycleBarOverride?: AccountQuotaLifecycleBarOverride
): ReactTestRenderer => {
  let renderer!: ReactTestRenderer;
  act(() => {
    renderer = create(
      <AccountQuotaMatrix
        accountKey={accountKey}
        matrix={matrix}
        lifecycleBarOverride={lifecycleBarOverride}
      />
    );
  });
  return renderer;
};

describe('AccountQuotaMatrix', () => {
  it('renders only phrasing-compatible span elements', () => {
    const renderer = renderMatrix('test-account', {
      windowKeys: new Set(['gemini-5h', 'claude-5h']),
      rows: [
        {
          key: 'five_hour',
          label: '5H',
          cells: [
            {
              groupLabel: 'Gemini Models',
              displayLabel: 'Gemini',
              window: makeQuotaWindow({ key: 'gemini-5h', remainingPercent: 80 }),
            },
            {
              groupLabel: 'Claude and GPT models',
              displayLabel: 'Claude',
              window: makeQuotaWindow({ key: 'claude-5h', remainingPercent: 15 }),
            },
          ],
        },
      ],
    });

    expect(renderer.root.findAll((node) => node.type === 'div')).toHaveLength(0);
    expect(renderer.root.findByProps({ 'data-account-quota-matrix': 'test-account' }).type).toBe(
      'span'
    );
  });

  it('renders five-hour and weekly rows with all four matrix cells', () => {
    const renderer = renderMatrix('account-key-1', {
      windowKeys: new Set(['gemini-5h', 'claude-5h', 'gemini-weekly', 'claude-weekly']),
      rows: [
        {
          key: 'five_hour',
          label: '5H',
          cells: [
            {
              groupLabel: 'Gemini Models',
              displayLabel: 'Gemini',
              window: makeQuotaWindow({ key: 'gemini-5h' }),
            },
            {
              groupLabel: 'Claude and GPT models',
              displayLabel: 'Claude',
              window: makeQuotaWindow({ key: 'claude-5h' }),
            },
          ],
        },
        {
          key: 'weekly',
          label: '7D',
          cells: [
            {
              groupLabel: 'Gemini Models',
              displayLabel: 'Gemini',
              window: makeQuotaWindow({ key: 'gemini-weekly', kind: 'weekly' }),
            },
            {
              groupLabel: 'Claude and GPT models',
              displayLabel: 'Claude',
              window: makeQuotaWindow({ key: 'claude-weekly', kind: 'weekly' }),
            },
          ],
        },
      ],
    });

    expect(
      renderer.root.findAllByProps({ 'data-account-quota-matrix-row': 'five_hour' })
    ).toHaveLength(1);
    expect(
      renderer.root.findAllByProps({ 'data-account-quota-matrix-row': 'weekly' })
    ).toHaveLength(1);
    for (const cell of [
      'five_hour:Gemini Models',
      'five_hour:Claude and GPT models',
      'weekly:Gemini Models',
      'weekly:Claude and GPT models',
    ]) {
      expect(renderer.root.findByProps({ 'data-account-quota-matrix-cell': cell })).toBeTruthy();
    }
  });

  it('keeps neutral, bad, warning, and good quota bar tiers', () => {
    const renderer = renderMatrix('color-test', {
      windowKeys: new Set(['neutral', 'bad', 'warn', 'good']),
      rows: [
        {
          key: 'five_hour',
          label: '5H',
          cells: [
            {
              groupLabel: 'Neutral',
              displayLabel: 'Neutral',
              window: makeQuotaWindow({ key: 'neutral', remainingPercent: null }),
            },
            {
              groupLabel: 'Bad',
              displayLabel: 'Bad',
              window: makeQuotaWindow({ key: 'bad', remainingPercent: 0 }),
            },
          ],
        },
        {
          key: 'weekly',
          label: '7D',
          cells: [
            {
              groupLabel: 'Warn',
              displayLabel: 'Warn',
              window: makeQuotaWindow({ key: 'warn', remainingPercent: 19 }),
            },
            {
              groupLabel: 'Good',
              displayLabel: 'Good',
              window: makeQuotaWindow({ key: 'good', remainingPercent: 20 }),
            },
          ],
        },
      ],
    });

    const findBarClass = (cellHook: string) => {
      const cell = renderer.root.findByProps({ 'data-account-quota-matrix-cell': cellHook });
      const bar = cell.find(
        (node) =>
          typeof node.props.className === 'string' &&
          node.props.className.includes('quotaBar') &&
          !node.props.className.includes('quotaTrack')
      );
      return bar.props.className as string;
    };

    expect(findBarClass('five_hour:Neutral')).toContain('quotaBarNeutral');
    expect(findBarClass('five_hour:Bad')).toContain('quotaBarBad');
    expect(findBarClass('weekly:Warn')).toContain('quotaBarWarn');
    expect(findBarClass('weekly:Good')).toContain('quotaBarGood');
  });

  it.each([
    [undefined, 'quotaBarWarn', 'quotaBarGood'],
    ['bad', 'quotaBarBad', 'quotaBarBad'],
    ['neutral', 'quotaBarNeutral', 'quotaBarNeutral'],
  ] as const)(
    'applies %s lifecycle override before window color thresholds',
    (override, lowClass, highClass) => {
      const renderer = renderMatrix(
        `lifecycle-${override ?? 'none'}`,
        {
          windowKeys: new Set(['low', 'high']),
          rows: [
            {
              key: 'five_hour',
              label: '5H',
              cells: [
                {
                  groupLabel: 'Low',
                  displayLabel: 'Low',
                  window: makeQuotaWindow({ key: 'low', remainingPercent: 10 }),
                },
                {
                  groupLabel: 'High',
                  displayLabel: 'High',
                  window: makeQuotaWindow({ key: 'high', remainingPercent: 80 }),
                },
              ],
            },
          ],
        },
        override
      );
      const findBarClass = (groupLabel: string) => {
        const cell = renderer.root.findByProps({
          'data-account-quota-matrix-cell': `five_hour:${groupLabel}`,
        });
        const bar = cell.find(
          (node) =>
            typeof node.props.className === 'string' &&
            node.props.className.includes('quotaBar') &&
            !node.props.className.includes('quotaTrack')
        );
        return bar.props.className as string;
      };

      expect(findBarClass('Low')).toContain(lowClass);
      expect(findBarClass('High')).toContain(highClass);
    }
  );
});
