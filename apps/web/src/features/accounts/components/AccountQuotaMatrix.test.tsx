import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { describe, expect, it, vi } from 'vitest';
import type { AccountQuotaDisplayWindow } from '@/features/accounts/model/accountQuotaDisplayWindows';
import type { AntigravityQuotaMatrix } from '@/features/accounts/model/accountsPagePresentation';
import { AccountQuotaMatrix } from './AccountQuotaMatrix';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

vi.mock('react-i18next', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-i18next')>();
  return {
    ...actual,
    useTranslation: () => ({
      t: (key: string) => key,
      i18n: { language: 'en-US' },
    }),
  };
});

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
    limitWindowSeconds: 18000,
    resetAtMs: Date.now() + 7200000,
    fromMs: Date.now() - 10800000,
    toMs: Date.now() + 7200000,
    source: 'antigravity',
    observationSource: 'response_header',
    observedAtMs: Date.now(),
    windowMode: 'fixed',
    groupLabel: 'Gemini Models',
    ...overrides,
  }) as AccountQuotaDisplayWindow;

const renderMatrix = (accountKey: string, matrix: AntigravityQuotaMatrix): ReactTestRenderer => {
  let renderer!: ReactTestRenderer;
  act(() => {
    renderer = create(<AccountQuotaMatrix accountKey={accountKey} matrix={matrix} />);
  });
  return renderer;
};

describe('AccountQuotaMatrix', () => {
  it('renders with span-based phrasing content without any div elements', () => {
    const matrix: AntigravityQuotaMatrix = {
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
    };

    const renderer = renderMatrix('test-account', matrix);

    const divs = renderer.root.findAll((node) => node.type === 'div');
    expect(divs).toHaveLength(0);

    const matrixRoot = renderer.root.findByProps({ 'data-account-quota-matrix': 'test-account' });
    expect(matrixRoot.type).toBe('span');
  });

  it('renders row and cell data hooks matching the matrix layout', () => {
    const matrix: AntigravityQuotaMatrix = {
      windowKeys: new Set(['gemini-5h', 'claude-5h', 'gemini-weekly', 'claude-weekly']),
      rows: [
        {
          key: 'five_hour',
          label: '5H',
          cells: [
            {
              groupLabel: 'Gemini Models',
              displayLabel: 'Gemini',
              window: makeQuotaWindow({ key: 'gemini-5h', remainingPercent: 90 }),
            },
            {
              groupLabel: 'Claude and GPT models',
              displayLabel: 'Claude',
              window: makeQuotaWindow({ key: 'claude-5h', remainingPercent: 40 }),
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
              window: makeQuotaWindow({ key: 'gemini-weekly', remainingPercent: 30 }),
            },
            {
              groupLabel: 'Claude and GPT models',
              displayLabel: 'Claude',
              window: makeQuotaWindow({ key: 'claude-weekly', remainingPercent: 60 }),
            },
          ],
        },
      ],
    };

    const renderer = renderMatrix('account-key-1', matrix);

    expect(
      renderer.root.findAllByProps({ 'data-account-quota-matrix': 'account-key-1' })
    ).toHaveLength(1);
    expect(
      renderer.root.findAllByProps({ 'data-account-quota-matrix-row': 'five_hour' })
    ).toHaveLength(1);
    expect(
      renderer.root.findAllByProps({ 'data-account-quota-matrix-row': 'weekly' })
    ).toHaveLength(1);

    expect(
      renderer.root.findByProps({
        'data-account-quota-matrix-cell': 'five_hour:Gemini Models',
      })
    ).toBeTruthy();
    expect(
      renderer.root.findByProps({
        'data-account-quota-matrix-cell': 'five_hour:Claude and GPT models',
      })
    ).toBeTruthy();
    expect(
      renderer.root.findByProps({
        'data-account-quota-matrix-cell': 'weekly:Gemini Models',
      })
    ).toBeTruthy();
    expect(
      renderer.root.findByProps({
        'data-account-quota-matrix-cell': 'weekly:Claude and GPT models',
      })
    ).toBeTruthy();
  });

  it('assigns color tier classes to quota bars based on remaining percent thresholds', () => {
    const matrix: AntigravityQuotaMatrix = {
      windowKeys: new Set(['w-neutral', 'w-bad', 'w-warn', 'w-good']),
      rows: [
        {
          key: 'five_hour',
          label: '5H',
          cells: [
            {
              groupLabel: 'Group Neutral',
              displayLabel: 'Neutral',
              window: makeQuotaWindow({ key: 'w-neutral', remainingPercent: null }),
            },
            {
              groupLabel: 'Group Bad',
              displayLabel: 'Bad',
              window: makeQuotaWindow({ key: 'w-bad', remainingPercent: 0 }),
            },
          ],
        },
        {
          key: 'weekly',
          label: '7D',
          cells: [
            {
              groupLabel: 'Group Warn',
              displayLabel: 'Warn',
              window: makeQuotaWindow({ key: 'w-warn', remainingPercent: 19 }),
            },
            {
              groupLabel: 'Group Good',
              displayLabel: 'Good',
              window: makeQuotaWindow({ key: 'w-good', remainingPercent: 20 }),
            },
          ],
        },
      ],
    };

    const renderer = renderMatrix('color-test', matrix);

    const findBarForCell = (cellHook: string) => {
      const cell = renderer.root.findByProps({ 'data-account-quota-matrix-cell': cellHook });
      const bar = cell.findAll(
        (node) =>
          typeof node.props.className === 'string' &&
          node.props.className.includes('quotaBar') &&
          !node.props.className.includes('quotaTrack')
      )[0];
      if (!bar) throw new Error(`Bar not found for ${cellHook}`);
      return bar.props.className as string;
    };

    expect(findBarForCell('five_hour:Group Neutral')).toContain('quotaBarNeutral');
    expect(findBarForCell('five_hour:Group Bad')).toContain('quotaBarBad');
    expect(findBarForCell('weekly:Group Warn')).toContain('quotaBarWarn');
    expect(findBarForCell('weekly:Group Good')).toContain('quotaBarGood');
  });
});
