import { afterEach, describe, expect, it, vi } from 'vitest';
import { buildRangeFilteredRows } from './rowBuilders';
import { buildMonitoringEventsScopeKey } from '../hooks/useMonitoringData';
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

describe('hour-aligned live-window presentation', () => {
  it('includes start and current-hour events but excludes the refresh instant in fallback rows', () => {
    const end = new Date(2026, 10, 3, 10).getTime();
    const rows = [end - 86_400_001, end - 86_400_000, end - 1, end, end + 9_999, end + 10_000].map(
      row
    );
    expect(
      buildRangeFilteredRows(rows, '24h', null, '', undefined, end + 10_000).map(
        (item) => item.timestampMs
      )
    ).toEqual([end - 86_400_000, end - 1, end, end + 9_999]);
  });

  it('renders 25 timestamp-keyed chronological buckets across the repeated DST hour', () => {
    vi.stubEnv('TZ', 'America/New_York');
    const now = Date.parse('2026-11-01T12:32:00-05:00');
    const first = Date.parse('2026-11-01T01:12:00-04:00');
    const second = Date.parse('2026-11-01T01:12:00-05:00');
    const result = buildTimeline([row(first), row(second), row(now - 1)], '24h', null, now);
    expect(result.granularity).toBe('hour');
    expect(result.points).toHaveLength(25);
    expect(result.points[0].label).toContain('13:00');
    const occupied = result.points.filter((point) => point.requests);
    expect(occupied.map((point) => point.requests)).toEqual([1, 1, 1]);
    expect(new Set(occupied.map((point) => point.label)).size).toBe(3);
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

  it('distinguishes yesterday and today when their hour and UTC offset match', () => {
    vi.stubEnv('TZ', 'Asia/Shanghai');
    const result = buildTimeline([], '24h', null, Date.parse('2026-11-03T10:32:00+08:00'));
    expect(result.points).toHaveLength(25);
    expect(new Set(result.points.map((point) => point.label)).size).toBe(25);
    expect(result.points[0].label).toContain('11/02');
    expect(result.points[24].label).toContain('11/03');
    const backend = buildTimelineFromAnalytics(
      [Date.parse('2026-11-02T10:00:00+08:00'), Date.parse('2026-11-03T10:00:00+08:00')].map(
        (bucket_ms) => ({ bucket_ms, label: '', calls: 1, tokens: 1, success: 1, failure: 0 })
      ),
      'hour'
    );
    expect(new Set(backend.map((point) => point.label)).size).toBe(2);
    expect(backend[0].label).toContain('11/02');
    expect(backend[1].label).toContain('11/03');
  });

  it('invalidates selector scope when the historical start advances', () => {
    expect(buildMonitoringFilterSelectorsScopeKey('24h', { startMs: 1, endMs: 2 }, '')).not.toBe(
      buildMonitoringFilterSelectorsScopeKey('24h', { startMs: 2, endMs: 3 }, '')
    );
  });

  it('keeps event, presentation and selector scopes stable while only the live end advances', () => {
    const first = { startMs: 1, endMs: 2 };
    const next = { startMs: 1, endMs: 3 };
    expect(buildMonitoringEventsScopeKey('24h', first, '', undefined, {}, 'hour')).toBe(
      buildMonitoringEventsScopeKey('24h', next, '', undefined, {}, 'hour')
    );
    expect(buildMonitoringFilterSelectorsScopeKey('24h', first, '')).toBe(
      buildMonitoringFilterSelectorsScopeKey('24h', next, '')
    );
    expect(buildMonitoringEventsScopeKey('custom', first, '', undefined, {}, 'hour')).not.toBe(
      buildMonitoringEventsScopeKey('custom', next, '', undefined, {}, 'hour')
    );
  });

  it('displays the resolved half-open scope through the last included minute', () => {
    vi.stubEnv('TZ', 'Asia/Shanghai');
    const bounds = {
      startMs: Date.parse('2026-11-02T10:00:00+08:00'),
      endMs: Date.parse('2026-11-03T10:32:00+08:00'),
    };
    const display = getRangeDisplayBounds('24h', bounds);
    expect(display).toEqual(bounds);
    const translate = ((_key: string, options: { range: string }) => options.range) as TFunction;
    const text = formatAccountOverviewScopeText(display, 'en', translate);
    expect(text).toContain('10:32');
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
