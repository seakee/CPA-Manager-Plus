import { describe, expect, it } from 'vitest';
import { formatQuotaResetTime } from '@/utils/quota/formatters';
import { formatAccountQuotaResetDisplay } from './accountOverviewPresentation';

describe('accountOverviewPresentation reset display', () => {
  it('prefers the canonical timestamp over a legacy reset label', () => {
    const resetAtMs = Date.parse('2026-08-20T03:40:00Z');

    expect(
      formatAccountQuotaResetDisplay(resetAtMs, '08/20 03:40', {
        locale: 'zh-CN',
        timeZone: 'Asia/Shanghai',
      })
    ).toBe('08/20 11:40');
    expect(formatQuotaResetTime(resetAtMs, { locale: 'zh-CN', timeZone: 'UTC' })).toBe(
      '08/20 03:40'
    );
  });

  it('preserves legacy relative text when no timestamp is available', () => {
    expect(formatAccountQuotaResetDisplay(null, '2h 18m')).toBe('2h 18m');
  });

  it('formats an ISO legacy label when no canonical timestamp is available', () => {
    const resetLabel = '2026-08-20T03:40:00Z';

    expect(formatAccountQuotaResetDisplay(null, resetLabel)).toBe(formatQuotaResetTime(resetLabel));
  });

  it('returns the empty reset marker when neither source has a value', () => {
    expect(formatAccountQuotaResetDisplay(null, '-')).toBe('-');
    expect(formatAccountQuotaResetDisplay(null, null)).toBe('-');
  });
});
