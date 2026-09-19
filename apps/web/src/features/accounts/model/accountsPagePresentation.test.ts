import type { TFunction } from 'i18next';
import { describe, expect, it } from 'vitest';
import type { MonitoringAccountHistoryItem } from '@/services/api';
import {
  buildAntigravityQuotaMatrix,
  formatHistorySuccessRate,
  formatMoney,
  formatQuotaRemainingPercentDisplay,
  formatQuotaRemainingPercentParts,
  getQuotaRemainingPercentLabel,
  formatQuotaResetDisplay,
  formatQuotaResetRelative,
  getQuotaResetRemainingDays,
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
  selectAccountQuotaMainListWindows,
  getQuotaWindowReadableLabel,
  getAccountQuotaWindowGroupKey,
  isAccountQuotaWindowIgnored,
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

  it('calculates reset-credit remaining days with an inclusive countdown boundary', () => {
    const nowMs = new Date(2026, 8, 11, 6, 33).getTime();

    expect(getQuotaResetRemainingDays(nowMs + 10 * 24 * 60 * 60 * 1000, nowMs)).toBe(10);
    expect(getQuotaResetRemainingDays(nowMs + 10 * 24 * 60 * 60 * 1000 - 1, nowMs)).toBe(10);
    expect(getQuotaResetRemainingDays(nowMs - 1, nowMs)).toBe(0);
    expect(getQuotaResetRemainingDays(null, nowMs)).toBeNull();
  });

  it('formats relative quota resets with day, hour, and minute resolutions', () => {
    const nowMs = new Date(2026, 8, 9, 10, 0, 0, 0).getTime();

    // Default long format (zh-CN)
    // 5 days later -> 5 天后
    expect(formatQuotaResetRelative(nowMs + 5 * 24 * 60 * 60 * 1000, null, nowMs)).toBe('5 天后');
    // 23 hours later -> 23 小时后
    expect(formatQuotaResetRelative(nowMs + 23 * 60 * 60 * 1000 + 10 * 60 * 1000, null, nowMs)).toBe('23 小时后');
    // 59 minutes later -> 59 分钟后
    expect(formatQuotaResetRelative(nowMs + 59 * 60 * 1000 + 30 * 1000, null, nowMs)).toBe('59 分钟后');
    // 30 seconds later -> <1 分钟后
    expect(formatQuotaResetRelative(nowMs + 30 * 1000, null, nowMs)).toBe('<1 分钟后');
    // Expired or exact zero -> empty string
    expect(formatQuotaResetRelative(nowMs, null, nowMs)).toBe('');
    expect(formatQuotaResetRelative(nowMs - 5000, null, nowMs)).toBe('');

    // From string label fallback
    expect(formatQuotaResetRelative(null, '5d', nowMs)).toBe('5 天后');
    expect(formatQuotaResetRelative(null, '2h 18m', nowMs)).toBe('2 小时后');
    expect(formatQuotaResetRelative(null, '2d 20h', nowMs)).toBe('2 天后');
    expect(formatQuotaResetRelative(null, 'resets in 2d', nowMs)).toBe('2 天后');
    expect(formatQuotaResetRelative(null, null, nowMs)).toBe('');
    expect(formatQuotaResetRelative(null, '-', nowMs)).toBe('');

    // Short style mode
    expect(formatQuotaResetRelative(nowMs + 5 * 24 * 60 * 60 * 1000, null, nowMs, { style: 'short' })).toBe('5d');
    expect(formatQuotaResetRelative(nowMs + 23 * 60 * 60 * 1000, null, nowMs, { style: 'short' })).toBe('23h');
    expect(formatQuotaResetRelative(nowMs, null, nowMs, { style: 'short' })).toBe('');
    expect(formatQuotaResetRelative(nowMs - 5000, null, nowMs, { style: 'short' })).toBe('');
    expect(formatQuotaResetRelative(null, '5d', nowMs, { style: 'short' })).toBe('5d');

    // Localization support: future <1m vs expired
    expect(formatQuotaResetRelative(nowMs + 30 * 1000, null, nowMs, 'en')).toBe('in <1 min');
    expect(formatQuotaResetRelative(nowMs, null, nowMs, 'en')).toBe('');
    expect(formatQuotaResetRelative(nowMs - 5000, null, nowMs, 'en')).toBe('');

    // Localization support
    expect(formatQuotaResetRelative(nowMs + 5 * 24 * 60 * 60 * 1000, null, nowMs, 'en')).toBe('in 5 days');
    expect(formatQuotaResetRelative(nowMs + 1 * 60 * 60 * 1000, null, nowMs, 'en')).toBe('in 1 hour');
    expect(formatQuotaResetRelative(nowMs + 30 * 1000, null, nowMs, 'en')).toBe('in <1 min');
    expect(formatQuotaResetRelative(nowMs + 5 * 24 * 60 * 60 * 1000, null, nowMs, 'zh-TW')).toBe('5 天後');
    expect(formatQuotaResetRelative(nowMs + 2 * 60 * 60 * 1000, null, nowMs, 'zh-TW')).toBe('2 小時後');
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

  it('selects Codex main quota with or without duration while excluding scoped quota', () => {
    // Case D: full duration main 5H fixed + main 7D fixed -> [5H, 7D]
    const main5hFixed = makeQuotaWindow({
      key: 'five-hour',
      kind: 'five_hour',
      source: 'codex',
      windowMode: 'fixed',
      limitWindowSeconds: 18000,
      modelScope: { kind: 'family', key: 'codex_main', complete: true },
    });
    const main7dFixed = makeQuotaWindow({
      key: 'weekly',
      kind: 'weekly',
      source: 'codex',
      windowMode: 'fixed',
      limitWindowSeconds: 604800,
      modelScope: { kind: 'family', key: 'codex_main', complete: true },
    });
    expect(
      selectAccountQuotaListWindows(
        makeAccountRow('codex'),
        [main5hFixed, main7dFixed],
        [main5hFixed, main7dFixed]
      )
    ).toEqual([main5hFixed, main7dFixed]);

    // Case E: Weekly duration missing (main 5H fixed + main Weekly unknown) -> [5H, 7D]
    const mainWeeklyUnknown = makeQuotaWindow({
      key: 'weekly',
      kind: 'weekly',
      source: 'codex',
      windowMode: 'unknown',
      limitWindowSeconds: null,
      modelScope: { kind: 'family', key: 'codex_main', complete: true },
    });
    expect(
      selectAccountQuotaListWindows(
        makeAccountRow('codex'),
        [main5hFixed, mainWeeklyUnknown],
        [main5hFixed]
      )
    ).toEqual([main5hFixed, mainWeeklyUnknown]);

    // Case F: both duration missing -> [5H, 7D]
    const main5hUnknown = makeQuotaWindow({
      key: 'five-hour',
      kind: 'five_hour',
      source: 'codex',
      windowMode: 'unknown',
      limitWindowSeconds: null,
      modelScope: { kind: 'family', key: 'codex_main', complete: true },
    });
    expect(
      selectAccountQuotaListWindows(
        makeAccountRow('codex'),
        [main5hUnknown, mainWeeklyUnknown],
        []
      )
    ).toEqual([main5hUnknown, mainWeeklyUnknown]);

    // Case G: scoped quota (Spark, additional, code-review, model-scoped) do not enter list
    const spark = makeQuotaWindow({
      key: 'spark',
      kind: 'five_hour',
      source: 'codex',
      modelScope: { kind: 'models', models: ['gpt-5.3-codex-spark'], complete: true },
    });
    const codeReview = makeQuotaWindow({
      key: 'code-review',
      kind: 'weekly',
      source: 'codex',
      modelScope: { kind: 'feature', key: 'code_review', complete: false },
    });
    const additional = makeQuotaWindow({
      key: 'additional-unknown',
      kind: 'five_hour',
      source: 'codex',
      modelScope: { kind: 'feature', key: 'additional_unknown', complete: false },
    });
    expect(
      selectAccountQuotaListWindows(
        makeAccountRow('codex'),
        [main5hFixed, spark, codeReview, additional],
        [main5hFixed]
      )
    ).toEqual([main5hFixed]);
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

  it('selects Kimi top-level 5H and 7D in order, hides scoped quota, and exposes summary-only data', () => {
    // Case A: 5H standard + top-level 7D -> [5H, 7D]
    const fiveHour = makeQuotaWindow({ key: 'five-hour', kind: 'five_hour', source: 'kimi' });
    const topLevelWeekly = makeQuotaWindow({ key: 'summary', kind: 'weekly', source: 'kimi' });
    expect(
      selectAccountQuotaListWindows(makeAccountRow('kimi'), [topLevelWeekly, fiveHour], [fiveHour])
    ).toEqual([fiveHour, topLevelWeekly]);

    // Case B: only top-level Weekly -> [7D]
    const summaryOnly = [makeQuotaWindow({ key: 'summary', kind: 'weekly', source: 'kimi' })];
    expect(selectAccountQuotaListWindows(makeAccountRow('kimi'), summaryOnly, [])).toEqual(
      summaryOnly
    );

    // Case C: top-level 5H, top-level 7D, usage-0 scoped 5H, usage-0 scoped Weekly -> [top-level 5H, top-level 7D]
    const scoped5H = makeQuotaWindow({ key: 'usage-0-limit-0', kind: 'five_hour', source: 'kimi' });
    const scopedWeekly = makeQuotaWindow({ key: 'usage-0-summary', kind: 'weekly', source: 'kimi' });
    expect(
      selectAccountQuotaListWindows(
        makeAccountRow('kimi'),
        [fiveHour, topLevelWeekly, scoped5H, scopedWeekly],
        [fiveHour]
      )
    ).toEqual([fiveHour, topLevelWeekly]);
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

  describe('selectAccountQuotaMainListWindows', () => {
    const makeRow = (provider = 'codex') => makeAccountRow(provider);

    it('selects 5h + weekly when 5h, weekly, and monthly are present (ascending duration)', () => {
      const fiveHour = makeQuotaWindow({
        key: '5h',
        kind: 'five_hour',
        limitWindowSeconds: 18000,
        remainingPercent: 10,
      });
      const weekly = makeQuotaWindow({
        key: '7d',
        kind: 'weekly',
        limitWindowSeconds: 604800,
        remainingPercent: 80,
      });
      const monthly = makeQuotaWindow({
        key: '30d',
        kind: 'monthly',
        limitWindowSeconds: 2592000,
        remainingPercent: 5,
      });

      const selected = selectAccountQuotaMainListWindows(makeRow('codex'), [
        monthly,
        fiveHour,
        weekly,
      ]);
      expect(selected).toEqual([fiveHour, weekly]);
    });

    it('selects weekly + monthly when only weekly and monthly are present', () => {
      const weekly = makeQuotaWindow({
        key: '7d',
        kind: 'weekly',
        limitWindowSeconds: 604800,
      });
      const monthly = makeQuotaWindow({
        key: '30d',
        kind: 'monthly',
        limitWindowSeconds: 2592000,
      });

      const selected = selectAccountQuotaMainListWindows(makeRow('codex'), [monthly, weekly]);
      expect(selected).toEqual([weekly, monthly]);
    });

    it('selects 5h + monthly when only 5h and monthly are present', () => {
      const fiveHour = makeQuotaWindow({
        key: '5h',
        kind: 'five_hour',
        limitWindowSeconds: 18000,
      });
      const monthly = makeQuotaWindow({
        key: '30d',
        kind: 'monthly',
        limitWindowSeconds: 2592000,
      });

      const selected = selectAccountQuotaMainListWindows(makeRow('codex'), [monthly, fiveHour]);
      expect(selected).toEqual([fiveHour, monthly]);
    });

    it('returns single window when only one window is available without stretching or padding', () => {
      const fiveHour = makeQuotaWindow({
        key: '5h',
        kind: 'five_hour',
        limitWindowSeconds: 18000,
      });

      const selected = selectAccountQuotaMainListWindows(makeRow('codex'), [fiveHour]);
      expect(selected).toEqual([fiveHour]);
    });

    it('defaults main-list selection to maximum 2 windows', () => {
      const w1 = makeQuotaWindow({
        key: 'w1',
        kind: 'five_hour',
        limitWindowSeconds: 18000,
        windowMode: 'fixed',
        source: 'claude',
      });
      const w2 = makeQuotaWindow({
        key: 'w2',
        kind: 'daily',
        limitWindowSeconds: 86400,
        windowMode: 'fixed',
        source: 'claude',
      });
      const w3 = makeQuotaWindow({
        key: 'w3',
        kind: 'weekly',
        limitWindowSeconds: 604800,
        windowMode: 'fixed',
        source: 'claude',
      });
      const w4 = makeQuotaWindow({
        key: 'w4',
        kind: 'monthly',
        limitWindowSeconds: 2592000,
        windowMode: 'fixed',
        source: 'claude',
      });

      const selected = selectAccountQuotaMainListWindows(makeRow('claude'), [w4, w3, w2, w1]);
      expect(selected).toHaveLength(2);
      expect(selected).toEqual([w1, w2]);
    });

    it('supports an explicit maximum of 4 windows while preserving duration order', () => {
      const fiveHour = makeQuotaWindow({
        key: '5h',
        kind: 'five_hour',
        limitWindowSeconds: 18000,
        remainingPercent: 95,
        windowMode: 'fixed',
        source: 'claude',
      });
      const daily = makeQuotaWindow({
        key: '24h',
        kind: 'daily',
        limitWindowSeconds: 86400,
        remainingPercent: 5,
        windowMode: 'fixed',
        source: 'claude',
      });
      const weekly = makeQuotaWindow({
        key: '7d',
        kind: 'weekly',
        limitWindowSeconds: 604800,
        remainingPercent: 80,
        windowMode: 'fixed',
        source: 'claude',
      });
      const monthly = makeQuotaWindow({
        key: '30d',
        kind: 'monthly',
        limitWindowSeconds: 2592000,
        remainingPercent: 10,
        windowMode: 'fixed',
        source: 'claude',
      });
      const longer = makeQuotaWindow({
        key: '90d',
        kind: 'monthly',
        limitWindowSeconds: 7776000,
        remainingPercent: 99,
        windowMode: 'fixed',
        source: 'claude',
      });

      const selected = selectAccountQuotaMainListWindows(
        makeRow('claude'),
        [longer, weekly, monthly, fiveHour, daily],
        4
      );

      expect(selected).toHaveLength(4);
      expect(selected).toEqual([fiveHour, daily, weekly, monthly]);
    });

    it('orders Antigravity windows by duration and stable model group rank', () => {
      const claude5h = makeQuotaWindow({
        key: 'claude-5h',
        kind: 'five_hour',
        limitWindowSeconds: 18000,
        source: 'antigravity',
        windowMode: 'fixed',
        groupLabel: 'Claude and GPT models',
        modelScope: { kind: 'family', key: 'claude_gpt', complete: true },
      });
      const gemini5h = makeQuotaWindow({
        key: 'gemini-5h',
        kind: 'five_hour',
        limitWindowSeconds: 18000,
        source: 'antigravity',
        windowMode: 'fixed',
        groupLabel: 'Gemini models',
        modelScope: { kind: 'family', key: 'gemini', complete: true },
      });
      const claude7d = makeQuotaWindow({
        key: 'claude-7d',
        kind: 'weekly',
        limitWindowSeconds: 604800,
        source: 'antigravity',
        windowMode: 'fixed',
        groupLabel: 'Claude and GPT models',
        modelScope: { kind: 'family', key: 'claude_gpt', complete: true },
      });
      const gemini7d = makeQuotaWindow({
        key: 'gemini-7d',
        kind: 'weekly',
        limitWindowSeconds: 604800,
        source: 'antigravity',
        windowMode: 'fixed',
        groupLabel: 'Gemini models',
        modelScope: { kind: 'family', key: 'gemini', complete: true },
      });

      const selected = selectAccountQuotaMainListWindows(
        makeRow('antigravity'),
        [gemini7d, claude5h, claude7d, gemini5h],
        4
      );

      expect(selected).toEqual([claude5h, gemini5h, claude7d, gemini7d]);
    });

    it('prioritizes known duration over unknown duration', () => {
      const known = makeQuotaWindow({
        key: 'known-weekly',
        kind: 'weekly',
        limitWindowSeconds: 604800,
        windowMode: 'fixed',
        source: 'claude',
      });
      const unknown = makeQuotaWindow({
        key: 'unknown-window',
        kind: 'unknown',
        limitWindowSeconds: null,
        windowMode: 'fixed',
        source: 'claude',
      });

      const selected = selectAccountQuotaMainListWindows(makeRow('claude'), [unknown, known]);
      expect(selected).toEqual([known, unknown]);
    });

    it('maintains stable order when durations are identical', () => {
      const first = makeQuotaWindow({
        key: 'first-5h',
        kind: 'five_hour',
        limitWindowSeconds: 18000,
        windowMode: 'fixed',
        source: 'claude',
      });
      const second = makeQuotaWindow({
        key: 'second-5h',
        kind: 'five_hour',
        limitWindowSeconds: 18000,
        windowMode: 'fixed',
        source: 'claude',
      });

      const selected = selectAccountQuotaMainListWindows(makeRow('claude'), [first, second]);
      expect(selected).toEqual([first, second]);
    });

    it('does not sort by remaining percent', () => {
      const lowPercentLongDuration = makeQuotaWindow({
        key: 'weekly',
        kind: 'weekly',
        limitWindowSeconds: 604800,
        remainingPercent: 5,
        windowMode: 'fixed',
        source: 'codex',
      });
      const highPercentShortDuration = makeQuotaWindow({
        key: '5h',
        kind: 'five_hour',
        limitWindowSeconds: 18000,
        remainingPercent: 95,
        windowMode: 'fixed',
        source: 'codex',
      });

      // 5h must come first despite 95% > 5%
      const selected = selectAccountQuotaMainListWindows(makeRow('codex'), [
        lowPercentLongDuration,
        highPercentShortDuration,
      ]);
      expect(selected).toEqual([highPercentShortDuration, lowPercentLongDuration]);
    });

    it('preserves provider-specific candidate eligibility (e.g. excludes scoped Codex windows)', () => {
      const main5h = makeQuotaWindow({
        key: 'main-5h',
        kind: 'five_hour',
        limitWindowSeconds: 18000,
        modelScope: { kind: 'family', key: 'codex_main', complete: true },
        windowMode: 'fixed',
        source: 'codex',
      });
      const sparkScoped = makeQuotaWindow({
        key: 'spark',
        kind: 'five_hour',
        limitWindowSeconds: 18000,
        modelScope: { kind: 'models', models: ['spark'], complete: true },
        windowMode: 'fixed',
        source: 'codex',
      });

      const selected = selectAccountQuotaMainListWindows(makeRow('codex'), [sparkScoped, main5h]);
      expect(selected).toEqual([main5h]);
    });
  });

  describe('getQuotaWindowReadableLabel', () => {
    it('formats known window kinds with standard uppercase words in English', () => {
      expect(getQuotaWindowReadableLabel(makeQuotaWindow({ kind: 'five_hour' }))).toBe('5h');
      expect(getQuotaWindowReadableLabel(makeQuotaWindow({ kind: 'daily' }))).toBe('24h');
      expect(getQuotaWindowReadableLabel(makeQuotaWindow({ kind: 'weekly' }))).toBe('Weekly');
      expect(getQuotaWindowReadableLabel(makeQuotaWindow({ kind: 'monthly' }))).toBe('Monthly');
      expect(getQuotaWindowReadableLabel(makeQuotaWindow({ kind: 'billing' }))).toBe('Billing');
      expect(getQuotaWindowReadableLabel(makeQuotaWindow({ kind: 'payg' }))).toBe('Pay-As-You-Go');
    });

    it('preserves already localized labels for zh-CN without replacing them with English', () => {
      expect(
        getQuotaWindowReadableLabel(makeQuotaWindow({ kind: 'weekly', label: '周额度' }))
      ).toBe('周额度');
      expect(
        getQuotaWindowReadableLabel(makeQuotaWindow({ kind: 'monthly', label: '月额度' }))
      ).toBe('月额度');
      expect(
        getQuotaWindowReadableLabel(makeQuotaWindow({ kind: 'billing', label: '月度积分' }))
      ).toBe('月度积分');
      expect(
        getQuotaWindowReadableLabel(makeQuotaWindow({ kind: 'payg', label: '按需用量' }))
      ).toBe('按需用量');
      expect(
        getQuotaWindowReadableLabel(makeQuotaWindow({ kind: 'weekly', label: '周积分' }))
      ).toBe('周积分');
    });

    it('preserves already localized labels for ru without replacing them with English', () => {
      expect(
        getQuotaWindowReadableLabel(makeQuotaWindow({ kind: 'weekly', label: 'Недельный лимит' }))
      ).toBe('Недельный лимит');
      expect(
        getQuotaWindowReadableLabel(
          makeQuotaWindow({ kind: 'billing', label: 'Ежемесячные кредиты' })
        )
      ).toBe('Ежемесячные кредиты');
      expect(
        getQuotaWindowReadableLabel(makeQuotaWindow({ kind: 'payg', label: 'Оплата по факту' }))
      ).toBe('Оплата по факту');
    });

    it('uses locale translation function t when window label is not already localized', () => {
      const zhT = ((key: string) => {
        if (key === 'accounts.detail_snapshot_window_weekly') return '周额度';
        if (key === 'accounts.detail_snapshot_window_monthly') return '月额度';
        if (key === 'accounts.col_quota') return '额度';
        return key;
      }) as unknown as TFunction;

      const ruT = ((key: string) => {
        if (key === 'accounts.detail_snapshot_window_weekly') return 'Недельная квота';
        if (key === 'accounts.detail_snapshot_window_monthly') return 'Месячная квота';
        if (key === 'accounts.col_quota') return 'Квота';
        return key;
      }) as unknown as TFunction;

      expect(getQuotaWindowReadableLabel(makeQuotaWindow({ kind: 'weekly', label: '' }), zhT)).toBe(
        '周额度'
      );
      expect(
        getQuotaWindowReadableLabel(makeQuotaWindow({ kind: 'monthly', label: '' }), zhT)
      ).toBe('月额度');
      expect(getQuotaWindowReadableLabel(makeQuotaWindow({ kind: 'weekly', label: '' }), ruT)).toBe(
        'Недельная квота'
      );
      expect(
        getQuotaWindowReadableLabel(makeQuotaWindow({ kind: 'monthly', label: '' }), ruT)
      ).toBe('Месячная квота');
      expect(
        getQuotaWindowReadableLabel(makeQuotaWindow({ label: '7-day limit' }), zhT)
      ).toBe('周额度');
    });

    it('formats custom/unknown labels with capitalized first letter', () => {
      expect(getQuotaWindowReadableLabel(makeQuotaWindow({ label: 'custom limit' }))).toBe(
        'Custom limit'
      );
    });
  });

  describe('formatQuotaRemainingPercentDisplay', () => {
    it('formats remaining percent across all supported locales with Plan B unified prefix', () => {
      expect(formatQuotaRemainingPercentDisplay('48%', 'zh-CN')).toBe('剩余 48%');
      expect(formatQuotaRemainingPercentDisplay('48%', 'zh-TW')).toBe('剩餘 48%');
      expect(formatQuotaRemainingPercentDisplay('48%', 'en')).toBe('Rem 48%');
      expect(formatQuotaRemainingPercentDisplay('48%', 'ru')).toBe('Ост. 48%');
    });

    it('formats remaining percent parts for visual font-size separation', () => {
      expect(formatQuotaRemainingPercentParts('48%', 'zh-CN')).toEqual({
        prefix: '剩余',
        percent: '48%',
      });
      expect(formatQuotaRemainingPercentParts('48%', 'en')).toEqual({
        prefix: 'Rem',
        percent: '48%',
      });
      expect(formatQuotaRemainingPercentParts('-', 'zh-CN')).toBeNull();
      expect(formatQuotaRemainingPercentParts('', 'en')).toBeNull();
      expect(getQuotaRemainingPercentLabel('en')).toBe('Rem');
    });

    it('handles fallback and invalid/dash percent values', () => {
      expect(formatQuotaRemainingPercentDisplay('-', 'zh-CN')).toBe('-');
      expect(formatQuotaRemainingPercentDisplay('', 'zh-CN')).toBe('-');
      expect(formatQuotaRemainingPercentDisplay('80%')).toBe('剩余 80%');
    });
  });

  describe('isAccountQuotaWindowIgnored & getAccountQuotaWindowGroupKey', () => {
    it('resolves antigravity group key to Claude or Gemini', () => {
      const claudeWindow = makeQuotaWindow({
        key: 'claude-gpt-models:3p-5h',
        source: 'antigravity',
        groupLabel: 'Claude and GPT models',
      });
      const geminiWindow = makeQuotaWindow({
        key: 'gemini-models:gemini-5h',
        source: 'antigravity',
        groupLabel: 'Gemini Models',
      });

      expect(getAccountQuotaWindowGroupKey(claudeWindow)).toBe('Claude');
      expect(getAccountQuotaWindowGroupKey(geminiWindow)).toBe('Gemini');
    });

    it('correctly identifies ignored quota windows by group or key', () => {
      const claude5h = makeQuotaWindow({
        key: 'claude-gpt-models:3p-5h',
        source: 'antigravity',
        groupLabel: 'Claude and GPT models',
      });
      const gemini5h = makeQuotaWindow({
        key: 'gemini-models:gemini-5h',
        source: 'antigravity',
        groupLabel: 'Gemini Models',
      });

      expect(isAccountQuotaWindowIgnored(claude5h, ['Claude'])).toBe(true);
      expect(isAccountQuotaWindowIgnored(gemini5h, ['Claude'])).toBe(false);
      expect(isAccountQuotaWindowIgnored(claude5h, ['claude-gpt-models:3p-5h'])).toBe(true);
      expect(isAccountQuotaWindowIgnored(claude5h, [])).toBe(false);
      expect(isAccountQuotaWindowIgnored(claude5h, undefined)).toBe(false);
    });
  });
});
