import { describe, expect, it } from 'vitest';
import {
  buildAntigravityQuotaMatrix,
  formatCompactNumber,
  formatHistorySuccessRate,
  formatMoney,
  formatQuotaResetDisplay,
  formatQuotaResetTimestamp,
  formatQuotaResetTooltipParams,
  formatTimestamp,
  formatTimestampTitle,
  parsePriorityValue,
  quotaStatusLabelKey,
} from './accountsPagePresentation';
import type { AccountRow } from './accountRows';
import type { AccountQuotaDisplayWindow } from './accountQuotaDisplayWindows';

describe('accountsPagePresentation', () => {
  it('keeps account sort and metric formatting semantics stable', () => {
    expect(parsePriorityValue(' -12 ')).toBe(-12);
    expect(parsePriorityValue('1.2')).toBeNull();
    expect(formatCompactNumber(999)).toBe('999');
    expect(formatCompactNumber(12_500)).toBe('12.5K');
    expect(formatHistorySuccessRate(0.975)).toBe('97.5%');
    expect(formatMoney(12.34)).toBe('$12.340');
    expect(quotaStatusLabelKey('exhausted')).toBe('accounts.quota_status_exhausted');
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
