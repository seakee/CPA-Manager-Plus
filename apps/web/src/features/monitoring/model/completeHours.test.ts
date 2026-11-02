import { afterEach, describe, expect, it, vi } from 'vitest';
import { buildRangeFilteredRows } from './rowBuilders';
import { getRangeDisplayBounds } from './range';
import { formatAccountOverviewScopeText } from './monitoringCenterPageModel';
import type { TFunction } from 'i18next';
import { buildTimeline } from './chartBuilders';
import { buildTimelineFromAnalytics } from './analyticsAdapters';
import { buildMonitoringFilterSelectorsScopeKey } from './monitoringAnalyticsModel';
import type { MonitoringEventRow } from './types';
import en from '@/i18n/locales/en.json';
import ru from '@/i18n/locales/ru.json';
import zhCN from '@/i18n/locales/zh-CN.json';
import zhTW from '@/i18n/locales/zh-TW.json';
afterEach(() => {
  vi.unstubAllEnvs();
});
const row = (timestampMs: number): MonitoringEventRow =>
  ({
    timestampMs,
    dayKey: '2026-11-01',
    hourLabel: `${String(new Date(timestampMs).getHours()).padStart(2, '0')}:00`,
    totalTokens: 7,
    totalCost: 0.1,
    searchText: '',
  }) as MonitoringEventRow;

describe('complete-hour presentation', () => {
  it('includes start and excludes current-hour boundary in fallback rows', () => {
    const end = new Date(2026, 10, 3, 10).getTime();
    const rows = [end - 86_400_001, end - 86_400_000, end - 1, end].map(row);
    expect(
      buildRangeFilteredRows(rows, '24h', null, '', undefined, end + 10_000).map(
        (item) => item.timestampMs
      )
    ).toEqual([end - 86_400_000, end - 1]);
  });

  it('renders 24 timestamp-keyed chronological buckets across the repeated DST hour', () => {
    vi.stubEnv('TZ', 'America/New_York');
    const now = Date.parse('2026-11-01T12:32:00-05:00');
    const first = Date.parse('2026-11-01T01:12:00-04:00');
    const second = Date.parse('2026-11-01T01:12:00-05:00');
    const result = buildTimeline([row(first), row(second)], '24h', null, now);
    expect(result.granularity).toBe('hour');
    expect(result.points).toHaveLength(24);
    expect(result.points[0].label).toContain('13:00');
    const occupied = result.points.filter((point) => point.requests);
    expect(occupied.map((point) => point.requests)).toEqual([1, 1]);
    expect(new Set(occupied.map((point) => point.label)).size).toBe(2);
    const backend = buildTimelineFromAnalytics(
      [
        { bucket_ms: first - 720_000, calls: 1, tokens: 7, label: '01:00', success: 1, failure: 0 },
        {
          bucket_ms: second - 720_000,
          calls: 1,
          tokens: 7,
          label: '01:00',
          success: 1,
          failure: 0,
        },
      ],
      'hour'
    );
    expect(new Set(backend.map((point) => point.label)).size).toBe(2);
  });

  it('invalidates selector scope when complete-hour bounds advance', () => {
    expect(buildMonitoringFilterSelectorsScopeKey('24h', { startMs: 1, endMs: 2 }, '')).not.toBe(
      buildMonitoringFilterSelectorsScopeKey('24h', { startMs: 2, endMs: 3 }, '')
    );
  });

  it('displays the resolved half-open scope through the last included minute', () => {
    vi.stubEnv('TZ', 'Asia/Shanghai');
    const bounds = {
      startMs: Date.parse('2026-11-02T10:00:00+08:00'),
      endMs: Date.parse('2026-11-03T10:00:00+08:00'),
    };
    const display = getRangeDisplayBounds('24h', bounds);
    expect(display).toEqual({ ...bounds, endMs: bounds.endMs - 1 });
    const translate = ((_key: string, options: { range: string }) => options.range) as TFunction;
    const text = formatAccountOverviewScopeText(display, 'en', translate);
    expect(text).toContain('09:59');
    expect(getRangeDisplayBounds('today', bounds)).toEqual(bounds);
  });

  it('has localized 24-hour labels in all supported locales', () => {
    expect([en, ru, zhCN, zhTW].map((locale) => locale.monitoring.range_24h)).toEqual([
      '24 hours',
      '24 часа',
      '24 小时',
      '24 小時',
    ]);
  });
});
