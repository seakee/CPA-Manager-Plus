import type { TFunction } from 'i18next';
import { describe, expect, it } from 'vitest';
import type { MonitoringAccountHistoryItem } from '@/services/api';
import {
  buildAntigravityQuotaMatrix,
  formatHistorySuccessRate,
  formatMoney,
  formatQuotaResetDisplay,
  formatQuotaResetTimestamp,
  formatQuotaResetTooltipParams,
  formatTimestamp,
  formatTimestampTitle,
  getAccountQuotaLifecycleBarOverride,
  getAccountQuotaFallbackVisibleScopeLabel,
  getAccountHistoryTitle,
  parsePriorityValue,
  quotaStatusLabelKey,
  selectAccountQuotaListWindows,
} from './accountsPagePresentation';
import type { AccountRow } from './accountRows';
import type { AccountQuotaDisplayWindow } from './accountQuotaDisplayWindows';

const makeQuotaWindow = (
  overrides: Partial<AccountQuotaDisplayWindow> = {}
): AccountQuotaDisplayWindow =>
  ({
    key: 'quota-window',
    label: 'Quota window',
    kind: 'unknown',
    remainingPercent: 50,
    usedPercent: 50,
    resetLabel: '-',
    resetAccuracy: 'unknown',
    limitWindowSeconds: null,
    resetAtMs: null,
    fromMs: null,
    toMs: null,
    source: 'summary',
    ...overrides,
  }) as AccountQuotaDisplayWindow;

const makeAccountRow = (provider: string): AccountRow => ({ provider }) as AccountRow;

describe('accountsPagePresentation', () => {
  it('keeps account sort and metric formatting semantics stable', () => {
    expect(parsePriorityValue(' -12 ')).toBe(-12);
    expect(parsePriorityValue('1.2')).toBeNull();
    expect(formatHistorySuccessRate(0.975)).toBe('97.5%');
    expect(formatMoney(12.34)).toBe('$12.34');
    expect(quotaStatusLabelKey('exhausted')).toBe('accounts.quota_status_exhausted');
  });

  it.each([
    ['error', 'bad'],
    ['loading', 'neutral'],
    ['disabled', 'neutral'],
    ['unknown', 'neutral'],
    ['ok', null],
    ['low', null],
    ['exhausted', null],
  ] as const)(
    'maps %s lifecycle status to the expected fallback bar override',
    (status, expected) => {
      expect(getAccountQuotaLifecycleBarOverride(status)).toBe(expected);
    }
  );

  it('uses exact values in the account history summary title', () => {
    const item = {
      matched: true,
      total_requests: 1_234_567,
      total_tokens: 1_000_190_000,
      total_cost: 12_345.67,
      success_rate: 0.98321,
      sync_status: 'ready',
    } as MonitoringAccountHistoryItem;
    const t = ((key: string, options?: Record<string, unknown>) =>
      `${key}:${options?.requests ?? ''}:${options?.tokens ?? ''}:${options?.cost ?? ''}:${options?.rate ?? ''}`) as TFunction;

    const title = getAccountHistoryTitle(t, item, false, '', 'en-US');

    expect(title).toContain('1,234,567');
    expect(title).toContain('1,000,190,000');
    expect(title).toContain('$12,345.67');
    expect(title).toContain('98.32%');
    expect(title).not.toContain('1.2M');
    expect(title).not.toContain('1000.2M');
  });

  it('formats detail timestamps with optional seconds using a numeric local format', () => {
    const timestamp = new Date(2026, 7, 26, 17, 44, 5, 0).getTime();

    expect(formatTimestamp(timestamp, 'zh-CN')).toBe('08/26 17:44');
    expect(formatTimestamp(timestamp, 'en', true)).toBe('08/26 17:44:05');
  });

  it('formats normalized quota resets consistently and preserves legacy text fallbacks', () => {
    const resetAtMs = new Date(2026, 6, 30, 10, 5, 0, 0).getTime();
    const recoverAtMs = new Date(2026, 6, 31, 11, 15, 0, 0).getTime();

    expect(formatQuotaResetTimestamp(resetAtMs, 'zh-CN')).toBe('07/30 10:05');
    expect(formatQuotaResetDisplay(resetAtMs, '2h', 'en')).toBe('07/30 10:05');
    expect(formatQuotaResetTimestamp(new Date(2026, 0, 1, 1, 1, 0, 0).getTime(), 'en')).toBe(
      '01/01 01:01'
    );
    expect(formatQuotaResetDisplay(null, 'resets in 2d', 'en')).toBe('resets in 2d');
    expect(
      formatQuotaResetTooltipParams(
        { resetAt: '2h', recoverAt: 'later' },
        resetAtMs,
        'en',
        recoverAtMs
      )
    ).toEqual({ resetAt: '07/30 10:05', recoverAt: '07/31 11:15' });
  });

  it('keeps standard quota windows as the only list selection when available', () => {
    const standardQuotaWindows = [
      makeQuotaWindow({ key: 'five-hour', kind: 'five_hour' }),
      makeQuotaWindow({ key: 'weekly', kind: 'weekly' }),
    ];
    const quotaWindows = [
      ...standardQuotaWindows,
      makeQuotaWindow({ key: 'model', kind: 'product' }),
      makeQuotaWindow({ key: 'billing', kind: 'billing' }),
      makeQuotaWindow({ key: 'pay-as-you-go', kind: 'payg' }),
      makeQuotaWindow({ key: 'summary', kind: 'summary' }),
    ];

    expect(
      selectAccountQuotaListWindows(makeAccountRow('xai'), quotaWindows, standardQuotaWindows)
    ).toBe(standardQuotaWindows);
  });

  it('does not add a fallback for Codex model-only quota', () => {
    const quotaWindows = [makeQuotaWindow({ key: 'codex-spark', kind: 'product' })];

    expect(selectAccountQuotaListWindows(makeAccountRow('codex'), quotaWindows, [])).toEqual([]);
  });

  it('preserves Claude standard ordering and keeps non-standard-only quota in details', () => {
    const standardQuotaWindows = [
      makeQuotaWindow({ key: 'five-hour', kind: 'five_hour' }),
      makeQuotaWindow({ key: 'weekly', kind: 'weekly' }),
    ];
    const quotaWindows = [
      ...standardQuotaWindows,
      makeQuotaWindow({ key: 'extra', kind: 'monthly' }),
    ];
    expect(
      selectAccountQuotaListWindows(makeAccountRow('claude'), quotaWindows, standardQuotaWindows)
    ).toBe(standardQuotaWindows);

    const nonStandardQuotaWindows = [
      makeQuotaWindow({ key: 'extra-1', kind: 'monthly' }),
      makeQuotaWindow({ key: 'extra-2', kind: 'summary' }),
      makeQuotaWindow({ key: 'extra-3', kind: 'product' }),
    ];
    expect(
      selectAccountQuotaListWindows(makeAccountRow('claude'), nonStandardQuotaWindows, [])
    ).toEqual([]);
  });

  it('keeps Kimi standard windows ahead of summary data and exposes summary-only data', () => {
    const standardQuotaWindows = [makeQuotaWindow({ key: 'five-hour', kind: 'five_hour' })];
    const quotaWindows = [
      makeQuotaWindow({ key: 'summary', kind: 'summary' }),
      ...standardQuotaWindows,
    ];
    expect(
      selectAccountQuotaListWindows(makeAccountRow('kimi'), quotaWindows, standardQuotaWindows)
    ).toBe(standardQuotaWindows);

    const summaryOnly = [makeQuotaWindow({ key: 'summary', kind: 'summary', source: 'kimi' })];
    expect(selectAccountQuotaListWindows(makeAccountRow('kimi'), summaryOnly, [])).toEqual(
      summaryOnly
    );

    const scopedSummary = [
      makeQuotaWindow({ key: 'usage-0-summary', kind: 'summary', source: 'kimi' }),
    ];
    expect(selectAccountQuotaListWindows(makeAccountRow('kimi'), scopedSummary, [])).toEqual([]);
  });

  it('normalizes Antigravity fallback scope labels without labeling other providers', () => {
    const antigravityRow = makeAccountRow('antigravity');
    expect(
      getAccountQuotaFallbackVisibleScopeLabel(
        antigravityRow,
        makeQuotaWindow({ source: 'antigravity', groupLabel: 'Gemini Models' })
      )
    ).toBe('Gemini');
    expect(
      getAccountQuotaFallbackVisibleScopeLabel(
        antigravityRow,
        makeQuotaWindow({ source: 'antigravity', groupLabel: 'Claude and GPT models' })
      )
    ).toBe('Claude');
    expect(
      getAccountQuotaFallbackVisibleScopeLabel(
        antigravityRow,
        makeQuotaWindow({ source: 'antigravity', groupLabel: 'Custom group' })
      )
    ).toBe('Custom group');

    for (const provider of ['xai', 'kimi', 'codex', 'claude']) {
      expect(
        getAccountQuotaFallbackVisibleScopeLabel(
          makeAccountRow(provider),
          makeQuotaWindow({
            source: provider as AccountQuotaDisplayWindow['source'],
            groupLabel: 'Gemini',
          })
        )
      ).toBeNull();
    }
  });

  it('selects xAI billing and PAYG while excluding product windows', () => {
    const billing = makeQuotaWindow({ key: 'billing', kind: 'billing', source: 'xai' });
    const payg = makeQuotaWindow({ key: 'pay-as-you-go', kind: 'payg', source: 'xai' });
    const quotaWindows = [
      makeQuotaWindow({ key: 'credits-period', kind: 'billing', source: 'xai' }),
      makeQuotaWindow({ key: 'product-grok-code-fast', kind: 'product', source: 'xai' }),
      billing,
      payg,
    ];

    expect(selectAccountQuotaListWindows(makeAccountRow('xai'), quotaWindows, [])).toEqual([
      billing,
      payg,
    ]);
  });

  it('uses xAI credits-period billing only when the dedicated billing window is absent', () => {
    const creditsPeriod = makeQuotaWindow({
      key: 'credits-period',
      kind: 'billing',
      source: 'xai',
    });
    const payg = makeQuotaWindow({ key: 'pay-as-you-go', kind: 'payg', source: 'xai' });

    expect(selectAccountQuotaListWindows(makeAccountRow('xai'), [creditsPeriod, payg], [])).toEqual(
      [creditsPeriod, payg]
    );
  });

  it('keeps xAI weekly credits standard-first without adding monthly or PAYG windows', () => {
    const weekly = makeQuotaWindow({ key: 'credits-period', kind: 'weekly', source: 'xai' });
    const billing = makeQuotaWindow({ key: 'billing', kind: 'billing', source: 'xai' });
    const payg = makeQuotaWindow({ key: 'pay-as-you-go', kind: 'payg', source: 'xai' });
    const standardQuotaWindows = [weekly];

    expect(
      selectAccountQuotaListWindows(
        makeAccountRow('xai'),
        [weekly, billing, payg],
        standardQuotaWindows
      )
    ).toBe(standardQuotaWindows);
  });

  it('rejects timestamps outside the JavaScript date range', () => {
    expect(formatTimestamp(Number.MAX_VALUE, 'en')).toBe('-');
    expect(formatTimestampTitle(Number.MAX_VALUE, 'en')).toBeUndefined();
  });

  it('builds the two-provider-group Antigravity quota matrix in stable order', () => {
    const row = { provider: 'antigravity' } as AccountRow;
    const windows = [
      ['weekly-gemini', 'weekly', 'Gemini models'],
      ['five-gemini', 'five_hour', 'Gemini models'],
      ['weekly-claude', 'weekly', 'Claude and GPT models'],
      ['five-claude', 'five_hour', 'Claude and GPT models'],
    ].map(
      ([key, kind, groupLabel]) =>
        ({
          key,
          kind,
          groupLabel,
          source: 'antigravity',
          label: kind,
        }) as AccountQuotaDisplayWindow
    );

    const matrix = buildAntigravityQuotaMatrix(row, windows);

    expect(matrix?.rows).toHaveLength(2);
    expect(matrix?.rows[0]?.cells.map((cell) => cell.displayLabel)).toEqual(['Claude', 'Gemini']);
    expect(matrix?.windowKeys).toEqual(
      new Set(['five-claude', 'five-gemini', 'weekly-claude', 'weekly-gemini'])
    );
  });
});
