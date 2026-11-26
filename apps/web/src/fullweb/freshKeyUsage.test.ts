import { describe, expect, it } from 'vitest';
import {
  parseKeyUsage,
  parseUsageSummary,
  usageCompactNumberText,
  usageCompactUsdText,
} from './freshKeyUsage';

describe('freshKeyUsage summary parsing and compact formatters', () => {
  it('parses input/output/cached token breakdown and per-model cost while ignoring negative or invalid values', () => {
    const parsed = parseUsageSummary({
      fromMs: 1_000,
      toMs: 5_000,
      coverage: { state: 'complete', reason: 'observed' },
      items: [
        {
          keyId: 'a'.repeat(32),
          calls: 29,
          success: 28,
          failure: 1,
          inputTokens: 390_200,
          outputTokens: 55_200,
          cachedTokens: 310_000,
          totalTokens: 445_400,
          estimatedCostUsd: 2.52,
          modelCount: 4,
          lastUsedAtMs: 4_500,
          coverage: { state: 'complete', reason: 'observed' },
          topModels: [
            {
              model: 'claude-sonnet-4-5',
              calls: 20,
              totalTokens: 350_000,
              estimatedCostUsd: 2.1,
            },
            {
              model: 'custom-unpriced',
              calls: 9,
              totalTokens: 95_400,
              estimatedCostUsd: -1,
            },
          ],
        },
      ],
    });

    expect(parsed).not.toBeNull();
    const item = parsed?.items[0];
    expect(item?.inputTokens).toBe(390_200);
    expect(item?.outputTokens).toBe(55_200);
    expect(item?.cachedTokens).toBe(310_000);
    expect(item?.totalTokens).toBe(445_400);
    expect(item?.estimatedCostUsd).toBe(2.52);
    expect(item?.modelCount).toBe(4);
    expect(item?.topModels).toEqual([
      {
        model: 'claude-sonnet-4-5',
        calls: 20,
        totalTokens: 350_000,
        estimatedCostUsd: 2.1,
      },
      {
        model: 'custom-unpriced',
        calls: 9,
        totalTokens: 95_400,
      },
    ]);
    expect(usageCompactNumberText(item?.totalTokens, 'complete')).toBe('445.4K');
    expect(usageCompactUsdText(item?.estimatedCostUsd, 'complete')).toBe('$2.52');
  });

  it('bounds large counts and currency values across K/M/B/T/Q with threshold rollover', () => {
    expect(usageCompactNumberText(999_960, 'complete')).toBe('1M');
    expect(usageCompactNumberText(1_250_000_000, 'complete')).toBe('1.3B');
    expect(usageCompactNumberText(2_400_000_000_000, 'complete')).toBe('2.4T');
    expect(usageCompactNumberText(999_990_000, 'partial')).toBe('≥1B');
    expect(usageCompactUsdText(12_340, 'complete')).toBe('$12.34K');
    expect(usageCompactUsdText(999_999.99, 'complete')).toBe('$1.00M');
  });

  it('returns em-dash for zero under partial coverage and zero/dollar-zero under complete coverage', () => {
    expect(usageCompactNumberText(0, 'partial')).toBe('—');
    expect(usageCompactUsdText(0, 'partial')).toBe('—');
    expect(usageCompactNumberText(0, 'complete')).toBe('0');
    expect(usageCompactUsdText(0, 'complete')).toBe('$0.00');
    expect(usageCompactNumberText(12, 'partial')).toBe('≥12');
    expect(usageCompactUsdText(0.5, 'partial')).toBe('≥$0.50');
  });

  it('parses valid reasoningTokens in parseKeyUsage and fails closed when present and not isCount', () => {
    const keyId = 'a'.repeat(32);
    const base = {
      keyId,
      fromMs: 1_000,
      toMs: 5_000,
      bucketMs: 1_000,
      coverage: {
        state: 'complete',
        reason: 'observed',
        collectingSinceMs: 500,
        lastProcessedEventId: 10,
        targetEventId: 10,
      },
      totals: {
        calls: 4,
        success: 3,
        failure: 1,
        inputTokens: 100,
        outputTokens: 40,
        reasoningTokens: 25,
        totalTokens: 165,
        lastUsedAtMs: 4_000,
      },
      series: [],
    };

    const parsed = parseKeyUsage(base, keyId);
    expect(parsed?.totals?.reasoningTokens).toBe(25);

    expect(
      parseKeyUsage({ ...base, totals: { ...base.totals, reasoningTokens: 0 } }, keyId)?.totals
        ?.reasoningTokens
    ).toBe(0);
    expect(parseKeyUsage({ ...base, totals: { ...base.totals, reasoningTokens: -1 } }, keyId)).toBeNull();
    expect(parseKeyUsage({ ...base, totals: { ...base.totals, reasoningTokens: 1.5 } }, keyId)).toBeNull();
    expect(parseKeyUsage({ ...base, totals: { ...base.totals, reasoningTokens: '25' } }, keyId)).toBeNull();
  });

  it('fails closed in parseUsageSummary on non-positive window or mismatched success/failure and drops inverted slots', () => {
    const keyId = 'b'.repeat(32);
    const validItem = {
      keyId,
      calls: 5,
      success: 4,
      failure: 1,
      lastUsedAtMs: 3_000,
      coverage: { state: 'complete', reason: 'observed' },
    };
    const baseSummary = {
      fromMs: 1_000,
      toMs: 5_000,
      coverage: { state: 'complete', reason: 'observed' },
      items: [validItem],
    };

    expect(parseUsageSummary({ ...baseSummary, toMs: 1_000 })).toBeNull();
    expect(parseUsageSummary({ ...baseSummary, toMs: 500 })).toBeNull();
    expect(
      parseUsageSummary({
        ...baseSummary,
        items: [{ ...validItem, calls: 5, success: 3, failure: 1 }],
      })
    ).toBeNull();
    expect(
      parseUsageSummary({
        ...baseSummary,
        items: [{ ...validItem, calls: 5, success: -1, failure: 6 }],
      })
    ).toBeNull();
    expect(
      parseUsageSummary({
        ...baseSummary,
        items: [{ ...validItem, calls: 5, success: 6, failure: undefined }],
      })
    ).toBeNull();

    const parsedWithSlots = parseUsageSummary({
      ...baseSummary,
      items: [
        {
          ...validItem,
          recentSlots: [
            { startMs: 2_000, endMs: 1_500, success: 1, failure: 0 },
            { startMs: 2_000, endMs: 2_000, success: 2, failure: 0 },
            { startMs: 2_000, endMs: 3_000, success: 2, failure: 1 },
          ],
        },
      ],
    });
    expect(parsedWithSlots?.items[0]?.recentSlots).toEqual([
      { startMs: 2_000, endMs: 2_000, success: 2, failure: 0 },
      { startMs: 2_000, endMs: 3_000, success: 2, failure: 1 },
    ]);
  });
});
