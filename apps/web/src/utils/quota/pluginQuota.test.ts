import { describe, expect, it } from 'vitest';
import type { AuthFileItem } from '@/types';
import { parsePluginQuotaContract } from './pluginQuota';

const NOW_MS = Date.parse('2026-08-28T12:00:00Z');

const baseContract = (version: 1 | 2) => ({
  schema: 'cliproxy.plugin.quota',
  version,
  provider: 'example-plugin',
  availability: 'available',
  observed_at: '2026-08-28T11:55:00Z',
  ttl_seconds: 3600,
});

describe('parsePluginQuotaContract', () => {
  it('parses v1 cent-denominated spend, daily usage, and quota windows', () => {
    const file = {
      name: 'example.json',
      plugin_quota: {
        ...baseContract(1),
        spend: {
          currency: 'USD',
          metered_cents: 12345,
          today_cents: 345,
          period_cents: 45678,
          latest_tokens: 1200,
          period_tokens: 34000,
          period_days: 30,
        },
        daily: [{ date: '2026-08-28', cost_cents: 345, tokens: 1200 }],
        windows: [
          {
            id: 'monthly',
            label: 'Monthly allowance',
            kind: 'monthly',
            unit: 'requests',
            used: 25,
            limit: 100,
            remaining: 75,
            window_start: '2026-08-01T00:00:00Z',
            window_end: '2026-09-01T00:00:00Z',
            reset_at: '2026-09-01T00:00:00Z',
            reset_accuracy: 'exact',
          },
        ],
      },
    } as AuthFileItem;

    expect(parsePluginQuotaContract(file, NOW_MS)).toMatchObject({
      availability: 'available',
      stale: false,
      currency: 'USD',
      minorUnit: 2,
      spend: {
        meteredMinorUnits: 12345,
        todayMinorUnits: 345,
      },
      daily: [{ date: '2026-08-28', costMinorUnits: 345, tokens: 1200 }],
      windows: [
        {
          id: 'monthly',
          kind: 'monthly',
          usedPercent: 25,
          resetAccuracy: 'exact',
        },
      ],
    });
  });

  it('parses v2 generic minor units without assuming USD or two decimals', () => {
    const file = {
      name: 'generic.json',
      plugin_quota: {
        ...baseContract(2),
        spend: {
          currency: 'JPY',
          minor_unit: 0,
          metered_minor_units: 98655,
          today_minor_units: 458,
          period_minor_units: 124717,
          latest_tokens: 1901,
          period_tokens: 245002,
          period_days: 30,
        },
        daily: [{ date: '2026-08-28', cost_minor_units: 458, tokens: 1901 }],
        top_model: 'model-one',
        provenance: ['usage_summary', 'usage_events'],
      },
    } as AuthFileItem;

    expect(parsePluginQuotaContract(file, NOW_MS)).toMatchObject({
      currency: 'JPY',
      minorUnit: 0,
      spend: { meteredMinorUnits: 98655 },
      daily: [{ date: '2026-08-28', costMinorUnits: 458 }],
      topModel: 'model-one',
      provenance: ['usage_summary', 'usage_events'],
    });
  });

  it('decodes host-escaped contract text exactly once before applying text bounds', () => {
    const boundedLabel = `${'a'.repeat(121)}&lt;&gt;&amp;&#34;&#39;xy`;
    const file = {
      name: 'escaped.json',
      plugin_quota: {
        ...baseContract(2),
        windows: [
          {
            id: 'id&lt;&gt;&amp;&#34;&#39;&amp;lt;',
            label: boundedLabel,
            unit: 'unit&lt;&gt;&amp;&#34;&#39;',
            used_percent: 25,
          },
        ],
        top_model: 'model&lt;&gt;&amp;&#34;&#39;&amp;lt;',
        provenance: ['source&lt;&gt;&amp;&#34;&#39;&amp;lt;'],
      },
    } as AuthFileItem;

    expect(parsePluginQuotaContract(file, NOW_MS)).toMatchObject({
      windows: [
        {
          id: `id<>&"'&lt;`,
          label: `${'a'.repeat(121)}<>&"'xy`,
          unit: `unit<>&"'`,
        },
      ],
      topModel: `model<>&"'&lt;`,
      provenance: [`source<>&"'&lt;`],
    });
  });

  it('does not invent a currency when a v1 spend payload omits it', () => {
    const file = {
      name: 'currencyless.json',
      plugin_quota: {
        ...baseContract(1),
        spend: { metered_cents: 100 },
      },
    } as AuthFileItem;

    expect(parsePluginQuotaContract(file, NOW_MS)).toMatchObject({
      currency: null,
      minorUnit: 2,
      spend: null,
    });
  });

  it('derives window utilization from remaining capacity when used is absent', () => {
    const file = {
      name: 'remaining.json',
      plugin_quota: {
        ...baseContract(2),
        windows: [{ id: 'daily', remaining: 75, limit: 100 }],
      },
    } as AuthFileItem;

    expect(parsePluginQuotaContract(file, NOW_MS)?.windows[0]).toMatchObject({
      used: 25,
      remaining: 75,
      limit: 100,
      usedPercent: 25,
    });
  });

  it('reconciles bounded counters and rejects contradictory or over-limit windows', () => {
    const file = {
      name: 'counters.json',
      plugin_quota: {
        ...baseContract(2),
        windows: [
          { id: 'used-only', used: 25, limit: 100, used_percent: 99 },
          { id: 'remaining-only', remaining: 60, limit: 100, used_percent: 99 },
          { id: 'consistent', used: 20, remaining: 80, limit: 100, used_percent: 99 },
          { id: 'contradictory', used: 20, remaining: 70, limit: 100 },
          { id: 'used-over', used: 101, limit: 100 },
          { id: 'remaining-over', remaining: 101, limit: 100 },
        ],
      },
    } as AuthFileItem;

    expect(parsePluginQuotaContract(file, NOW_MS)?.windows).toEqual([
      expect.objectContaining({
        id: 'used-only',
        used: 25,
        remaining: 75,
        usedPercent: 25,
      }),
      expect.objectContaining({
        id: 'remaining-only',
        used: 40,
        remaining: 60,
        usedPercent: 40,
      }),
      expect.objectContaining({
        id: 'consistent',
        used: 20,
        remaining: 80,
        usedPercent: 20,
      }),
    ]);
  });

  it('accepts strict RFC3339 timestamps and rejects zone-less or impossible values', () => {
    const file = {
      name: 'timestamps.json',
      plugin_quota: {
        ...baseContract(2),
        observed_at: '2026-08-28T13:55:00+02:00',
        windows: [
          { id: 'offset', used_percent: 10, reset_at: '2026-08-28T14:00:00+02:00' },
          { id: 'fraction', used_percent: 20, reset_at: '2026-08-28T12:00:00.123456Z' },
          { id: 'zone-less', used_percent: 30, reset_at: '2026-08-28T12:00:00' },
          { id: 'impossible', used_percent: 40, reset_at: '2026-02-30T12:00:00Z' },
        ],
      },
    } as AuthFileItem;

    expect(parsePluginQuotaContract(file, NOW_MS)).toMatchObject({
      observedAtMs: Date.parse('2026-08-28T11:55:00Z'),
      windows: [
        expect.objectContaining({ id: 'offset', resetAtMs: Date.parse('2026-08-28T12:00:00Z') }),
        expect.objectContaining({ id: 'fraction', resetAtMs: 1787918400123 }),
        expect.objectContaining({ id: 'zone-less', resetAtMs: null }),
        expect.objectContaining({ id: 'impossible', resetAtMs: null }),
      ],
    });
  });

  it('accepts only safe numeric millisecond timestamps within supported bounds', () => {
    const min = Date.UTC(2000, 0, 1);
    const max = Date.UTC(2100, 0, 1);
    const parseObserved = (observedAt: number) =>
      parsePluginQuotaContract(
        {
          name: 'numeric.json',
          plugin_quota: { ...baseContract(2), observed_at: observedAt },
        } as AuthFileItem,
        min
      )?.observedAtMs;

    expect(parseObserved(min)).toBe(min);
    expect(parseObserved(max)).toBe(max);
    expect(parseObserved(min - 1)).toBeNull();
    expect(parseObserved(max + 1)).toBeNull();
    expect(parseObserved(Number.MAX_SAFE_INTEGER + 1)).toBeNull();
    expect(parseObserved(min + 0.5)).toBeNull();
  });

  it('rejects nested, unsupported, oversized, and provider-invalid payload data', () => {
    const nested = {
      name: 'nested.json',
      metadata: { plugin_quota: baseContract(2) },
    } as AuthFileItem;
    const unsupported = {
      name: 'future.json',
      plugin_quota: { ...baseContract(2), version: 3 },
    } as AuthFileItem;
    const oversizedWindows = Array.from({ length: 33 }, (_, index) => ({
      id: `window-${index}`,
      used_percent: 50,
    }));
    const providerNamedPayload = {
      name: 'neutral.json',
      plugin_quota: {
        ...baseContract(2),
        provider: 'any-vendor-name',
        windows: oversizedWindows,
        spend: { currency: 'dollars', minor_unit: 2, metered_minor_units: 1 },
      },
    } as AuthFileItem;

    expect(parsePluginQuotaContract(nested, NOW_MS)).toBeNull();
    expect(parsePluginQuotaContract(unsupported, NOW_MS)).toBeNull();
    expect(parsePluginQuotaContract(providerNamedPayload, NOW_MS)).toMatchObject({
      windows: [],
      spend: null,
    });
  });

  it('marks expired observations unavailable and strips their usage payload', () => {
    const file = {
      name: 'stale.json',
      plugin_quota: {
        ...baseContract(2),
        observed_at: '2026-08-28T10:00:00Z',
        ttl_seconds: 60,
        spend: { currency: 'EUR', minor_unit: 2, metered_minor_units: 100 },
        daily: [{ date: '2026-08-28', cost_minor_units: 100 }],
        windows: [{ id: 'daily', used_percent: 10 }],
      },
    } as AuthFileItem;

    expect(parsePluginQuotaContract(file, NOW_MS)).toMatchObject({
      availability: 'unavailable',
      stale: true,
      spend: null,
      daily: [],
      windows: [],
    });
  });

  it('salvages valid bounded entries and rejects impossible UTC dates', () => {
    const file = {
      name: 'daily.json',
      plugin_quota: {
        ...baseContract(2),
        spend: { currency: 'EUR', minor_unit: 3, metered_minor_units: 1 },
        daily: [
          { date: '2026-02-30', cost_minor_units: 1 },
          { date: '2026-02-28', cost_minor_units: 2 },
          { date: '2026-02-28', cost_minor_units: 3 },
        ],
      },
    } as AuthFileItem;

    expect(parsePluginQuotaContract(file, NOW_MS)?.daily).toEqual([
      { date: '2026-02-28', costMinorUnits: 2, tokens: null },
    ]);
  });

  it('sanitizes stray money metadata on a valid non-currency window', () => {
    const file = {
      name: 'stray.json',
      plugin_quota: {
        ...baseContract(2),
        windows: [
          {
            id: 'requests',
            unit: 'requests',
            currency: 'EUR',
            minor_unit: 2,
            used_percent: 40,
          },
        ],
      },
    } as AuthFileItem;

    expect(parsePluginQuotaContract(file, NOW_MS)?.windows).toEqual([
      expect.objectContaining({ id: 'requests', unit: 'requests', usedPercent: 40 }),
    ]);
    expect(parsePluginQuotaContract(file, NOW_MS)?.windows[0]).not.toHaveProperty('currency');
  });

  it('does not create spend metrics from period metadata alone', () => {
    const file = {
      name: 'period-only.json',
      plugin_quota: {
        ...baseContract(2),
        spend: { currency: 'EUR', minor_unit: 2, period_days: 30 },
      },
    } as AuthFileItem;

    expect(parsePluginQuotaContract(file, NOW_MS)).toMatchObject({
      currency: 'EUR',
      minorUnit: 2,
      spend: null,
    });
  });
});
