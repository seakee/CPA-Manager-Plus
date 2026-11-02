import { useEffect } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  monitoringAnalyticsApi,
  type MonitoringAnalyticsRequest,
} from '@/services/api/usageService';
import { useMonitoringData, type UseMonitoringDataReturn } from './useMonitoringData';
import type { MonitoringTimeRange } from '../model/types';

vi.mock('@/hooks/useRequestMonitoringAvailability', () => ({
  useRequestMonitoringAvailability: () => ({
    checking: false,
    available: true,
    serviceBase: 'http://manager.test',
    reason: '',
  }),
}));
vi.mock('@/stores', () => ({
  useAuthStore: (select: (state: unknown) => unknown) =>
    select({ apiBase: 'http://cpa.test', managementKey: 'test-only' }),
}));
vi.mock('../services/monitoringMetaService', () => ({
  loadMonitoringMetaPayload: async () => ({
    authFiles: [],
    channels: [],
    authFilesLoaded: true,
    channelsLoaded: true,
    error: '',
  }),
}));
vi.mock('@/services/api/usageService', async (original) => ({
  ...(await original<typeof import('@/services/api/usageService')>()),
  monitoringAnalyticsApi: { getAnalytics: vi.fn() },
}));

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
const prices = {};
const api = vi.mocked(monitoringAnalyticsApi.getAnalytics);
const end = new Date(2026, 10, 3, 10).getTime();
let latest: UseMonitoringDataReturn;
let renderer: ReactTestRenderer | undefined;
let doc: EventTarget & { visibilityState: string };
const requests = () => api.mock.calls.map((call) => call[2]);
function Harness({ range = '24h' }: { range?: MonitoringTimeRange }) {
  const result = useMonitoringData({
    config: null,
    modelPrices: prices,
    timeRange: range,
    searchQuery: '',
    activeDataTab: 'realtime',
  });
  useEffect(() => {
    latest = result;
  }, [result]);
  return null;
}
const mount = async (range: MonitoringTimeRange = '24h') => {
  await act(async () => {
    renderer = create(<Harness range={range} />);
  });
};

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['Date', 'setTimeout', 'clearTimeout'] });
  vi.setSystemTime(end + 32 * 60_000);
  doc = Object.assign(new EventTarget(), { visibilityState: 'visible' });
  vi.stubGlobal('document', doc);
  api.mockImplementation(async (_base, _key, request) => ({
    generated_at_ms: Date.now(),
    granularity: 'hour',
    ...(request.include?.filter_selectors
      ? { filter_options: { providers: ['codex'] } }
      : {
          events: {
            items: [
              {
                event_hash: String(request.from_ms),
                timestamp_ms: request.from_ms,
                model: 'gpt-test',
                input_tokens: 7,
                output_tokens: 0,
                total_tokens: 7,
                failed: false,
                endpoint: '/v1/responses',
                method: 'POST',
                path: '/v1/responses',
                auth_index: 'test',
                source: 'test.json',
                source_hash: 'test',
                api_key_hash: 'test',
                account_snapshot: 'test',
                auth_label_snapshot: 'test',
                auth_provider_snapshot: 'codex',
                cached_tokens: 0,
                cache_read_tokens: 0,
                cache_creation_tokens: 0,
                reasoning_tokens: 0,
                latency_ms: 100,
              },
            ],
            has_more: true,
            total_count: 2,
            next_before_ms: request.to_ms - 1000,
            next_before_id: 42,
          },
        }),
  }));
});
afterEach(async () => {
  await act(async () => {
    renderer?.unmount();
  });
  renderer = undefined;
  vi.useRealTimers();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  api.mockReset();
});

const expectWindow = (request: MonitoringAnalyticsRequest, boundary: number) => {
  expect(request.from_ms).toBe(boundary - 86_400_000);
  expect(request.to_ms).toBe(boundary);
};

describe('complete-hour monitoring integration without auto-refresh', () => {
  it('requests identical half-open bounds for events/summary/selectors and resets pagination every hour', async () => {
    await mount();
    requests().forEach((request) => expectWindow(request, end));
    expect(latest.timelineGranularity).toBe('hour');
    expect(latest.filteredRows).toHaveLength(1);
    expect(latest.summary.totalCalls).toBe(1);
    await act(async () => {
      latest.loadMoreEvents();
    });
    expect(requests()[requests().length - 1]?.include?.events_page?.before_id).toBe(42);
    const count = api.mock.calls.length;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(27 * 60_000);
    });
    expect(api.mock.calls.length).toBe(count);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    const newRequests = requests().slice(count);
    expect(newRequests).toHaveLength(2);
    newRequests.forEach((request) => expectWindow(request, end + 3_600_000));
    expect(
      newRequests.find((request) => request.include?.events_page)?.include?.events_page?.before_ms
    ).toBeNull();
    expect(newRequests.find((request) => request.include?.events_page)?.include?.granularity).toBe(
      'hour'
    );
    expect(latest.filteredRows).toHaveLength(1);
    expect(latest.summary.totalCalls).toBe(1);
    expect(latest.rangeBounds).toEqual({ startMs: end - 82_800_000, endMs: end + 3_600_000 });
  });

  it('keeps the displayed bounds with retained data during a delayed or rejected hourly refresh', async () => {
    await mount();
    let rejectRefresh!: (error: Error) => void;
    const response = api.getMockImplementation()!;
    api.mockImplementation((base, key, request, signal) =>
      request.include?.filter_selectors
        ? response(base, key, request, signal)
        : new Promise((_resolve, reject) => {
            rejectRefresh = reject;
          })
    );
    await act(async () => {
      await vi.advanceTimersByTimeAsync(28 * 60_000);
    });
    expect(latest.isTransitioningScope).toBe(true);
    expect(latest.summary.totalCalls).toBe(1);
    expect(latest.rangeBounds).toEqual({ startMs: end - 86_400_000, endMs: end });
    await act(async () => {
      rejectRefresh(new Error('refresh failed'));
    });
    expect(latest.rangeBounds).toEqual({ startMs: end - 86_400_000, endMs: end });
    expect(latest.error).toContain('refresh failed');
  });

  it('ignores an old-window response that completes after the hour advances', async () => {
    let resolveOld!: (
      value: Awaited<ReturnType<typeof monitoringAnalyticsApi.getAnalytics>>
    ) => void;
    const normalResponse = api.getMockImplementation()!;
    api.mockImplementation((base, key, request, signal) => {
      if (!request.include?.filter_selectors && request.to_ms === end) {
        return new Promise((resolve) => {
          resolveOld = resolve;
        });
      }
      return normalResponse(base, key, request, signal);
    });
    await mount();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(28 * 60_000);
    });
    expect(latest.filteredRows[0].timestampMs).toBe(end - 82_800_000);
    await act(async () => {
      resolveOld({
        generated_at_ms: end,
        granularity: 'hour',
        events: { items: [], has_more: false, total_count: 0, next_before_ms: 0 },
      });
    });
    expect(latest.filteredRows).toHaveLength(1);
    expect(latest.filteredRows[0].timestampMs).toBe(end - 82_800_000);
  });

  it('catches up immediately on visibility resume and does not duplicate within the hour', async () => {
    await mount();
    const count = api.mock.calls.length;
    doc.visibilityState = 'hidden';
    vi.setSystemTime(end + 4 * 3_600_000 + 10_000);
    await act(async () => {
      doc.dispatchEvent(new Event('visibilitychange'));
    });
    expect(api.mock.calls.length).toBe(count);
    doc.visibilityState = 'visible';
    await act(async () => {
      doc.dispatchEvent(new Event('visibilitychange'));
    });
    expect(requests().slice(count)).toHaveLength(2);
    requests()
      .slice(count)
      .forEach((request) => expectWindow(request, end + 4 * 3_600_000));
    const resumedCount = api.mock.calls.length;
    await act(async () => {
      doc.dispatchEvent(new Event('visibilitychange'));
    });
    expect(api.mock.calls.length).toBe(resumedCount);
  });

  it('catches up when selecting 24h after another preset and removes the clock on unmount', async () => {
    await mount('today');
    vi.setSystemTime(end + 2 * 3_600_000 + 10_000);
    await act(async () => {
      renderer!.update(<Harness />);
    });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    requests()
      .slice(-2)
      .forEach((request) => expectWindow(request, end + 2 * 3_600_000));
    await act(async () => {
      renderer!.unmount();
    });
    renderer = undefined;
    const count = api.mock.calls.length;
    await vi.advanceTimersByTimeAsync(3_600_000);
    doc.dispatchEvent(new Event('visibilitychange'));
    expect(api.mock.calls.length).toBe(count);
    expect(vi.getTimerCount()).toBe(0);
  });
});
