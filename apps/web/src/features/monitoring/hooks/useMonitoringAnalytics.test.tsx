import { useEffect } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  monitoringAnalyticsApi,
  type MonitoringAnalyticsFilters,
  type MonitoringAnalyticsInclude,
  type MonitoringAnalyticsResponse,
} from '@/services/api/usageService';
import {
  useMonitoringAnalytics,
  type UseMonitoringAnalyticsReturn,
} from './useMonitoringAnalytics';

vi.mock('@/hooks/useRequestMonitoringAvailability', () => ({
  useRequestMonitoringAvailability: () => ({
    checking: false,
    available: true,
    managerServiceAvailable: true,
    modelPricesAvailable: true,
    serviceBase: 'http://manager.local',
    reason: '',
  }),
}));

vi.mock('@/stores', () => ({
  useAuthStore: (selector: (state: { managementKey: string }) => unknown) =>
    selector({ managementKey: 'admin-key' }),
}));

vi.mock('@/services/api/usageService', () => ({
  monitoringAnalyticsApi: {
    getAnalytics: vi.fn(),
  },
}));

const getAnalyticsMock = vi.mocked(monitoringAnalyticsApi.getAnalytics);

interface Deferred<T> {
  promise: Promise<T>;
  resolve: (value: T) => void;
  reject: (error: unknown) => void;
}

interface HarnessProps {
  dataScopeKey?: string;
  fromMs?: number;
  toMs?: number;
  nowMs?: number;
  searchQuery?: string;
  filters?: MonitoringAnalyticsFilters;
  include?: MonitoringAnalyticsInclude;
}

const flushPromises = async () => {
  await Promise.resolve();
  await Promise.resolve();
};

const createDeferred = <T,>(): Deferred<T> => {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
};

const createResponse = (generatedAtMS: number): MonitoringAnalyticsResponse => ({
  generated_at_ms: generatedAtMS,
  granularity: 'hour',
});

const getExpectedTimeZonePayload = () => {
  const timeZone = Intl.DateTimeFormat().resolvedOptions().timeZone || '';
  return timeZone ? { time_zone: timeZone } : {};
};

const installDeferredAnalyticsMock = () => {
  const requests: Array<Deferred<MonitoringAnalyticsResponse>> = [];
  getAnalyticsMock.mockImplementation(() => {
    const request = createDeferred<MonitoringAnalyticsResponse>();
    requests.push(request);
    return request.promise;
  });
  return requests;
};

describe('useMonitoringAnalytics', () => {
  let renderer: ReactTestRenderer | null = null;
  let latestResult: UseMonitoringAnalyticsReturn | null = null;

  afterEach(() => {
    renderer?.unmount();
    renderer = null;
    latestResult = null;
    getAnalyticsMock.mockReset();
  });

  function Harness({
    dataScopeKey = 'today',
    fromMs = 1,
    toMs = 10_000,
    nowMs = toMs,
    searchQuery,
    filters,
    include = { summary: true },
  }: HarnessProps) {
    const result = useMonitoringAnalytics({
      fromMs,
      toMs,
      nowMs,
      dataScopeKey,
      searchQuery,
      filters,
      include,
      throttleMs: 0,
    });
    useEffect(() => {
      latestResult = result;
    }, [result]);
    return null;
  }

  it('does not supersede an in-flight refresh for the same data scope', async () => {
    const requests = installDeferredAnalyticsMock();

    await act(async () => {
      renderer = create(<Harness nowMs={10_000} />);
    });

    expect(getAnalyticsMock).toHaveBeenCalledTimes(1);

    await act(async () => {
      renderer?.update(<Harness nowMs={15_000} />);
      await flushPromises();
    });

    expect(getAnalyticsMock).toHaveBeenCalledTimes(1);

    await act(async () => {
      requests[0]?.resolve(createResponse(1));
      await flushPromises();
    });

    await act(async () => {
      renderer?.update(<Harness nowMs={20_000} />);
      await flushPromises();
    });

    expect(getAnalyticsMock).toHaveBeenCalledTimes(2);
  });

  it('starts a new request when the analytics scope changes', async () => {
    installDeferredAnalyticsMock();

    await act(async () => {
      renderer = create(<Harness dataScopeKey="range:today" />);
    });

    expect(getAnalyticsMock).toHaveBeenCalledTimes(1);

    await act(async () => {
      renderer?.update(
        <Harness
          dataScopeKey="search:error|range:7d|model:gpt-5"
          fromMs={2}
          toMs={20_000}
          nowMs={20_000}
          searchQuery=" error "
          filters={{ models: ['gpt-5'] }}
          include={{ summary: true, granularity: 'day' }}
        />
      );
      await flushPromises();
    });

    expect(getAnalyticsMock).toHaveBeenCalledTimes(2);
    expect((getAnalyticsMock.mock.calls[0]?.[3] as AbortSignal | undefined)?.aborted).toBe(true);
    expect(getAnalyticsMock.mock.calls[1]?.[2]).toEqual({
      from_ms: 2,
      to_ms: 20_000,
      ...getExpectedTimeZonePayload(),
      now_ms: 20_000,
      search_query: 'error',
      filters: { models: ['gpt-5'] },
      include: { summary: true, granularity: 'day' },
    });
  });

  it('allows a root events page refresh to supersede in-flight pagination', async () => {
    const requests = installDeferredAnalyticsMock();
    const scopeKey = 'range:today';

    await act(async () => {
      renderer = create(
        <Harness
          dataScopeKey={scopeKey}
          include={{
            summary: true,
            events_page: { limit: 500, before_ms: 12_345, before_id: 99 },
          }}
        />
      );
    });

    expect(getAnalyticsMock).toHaveBeenCalledTimes(1);

    await act(async () => {
      renderer?.update(
        <Harness
          dataScopeKey={scopeKey}
          nowMs={15_000}
          include={{
            summary: true,
            events_page: { limit: 500, before_ms: null, before_id: null },
          }}
        />
      );
      await flushPromises();
    });

    expect(getAnalyticsMock).toHaveBeenCalledTimes(2);
    expect(getAnalyticsMock.mock.calls[0]?.[2].include?.events_page).toEqual({
      limit: 500,
      before_ms: 12_345,
      before_id: 99,
    });
    expect(getAnalyticsMock.mock.calls[1]?.[2].include?.events_page).toEqual({
      limit: 500,
      before_ms: null,
      before_id: null,
    });

    await act(async () => {
      requests[1]?.resolve(createResponse(2));
      await flushPromises();
    });

    expect(latestResult?.loading).toBe(false);
    expect(latestResult?.data?.generated_at_ms).toBe(2);

    await act(async () => {
      requests[0]?.resolve(createResponse(1));
      await flushPromises();
    });

    expect(latestResult?.loading).toBe(false);
    expect(latestResult?.data?.generated_at_ms).toBe(2);
  });

  it('ignores stale responses that resolve after a newer scope', async () => {
    const requests = installDeferredAnalyticsMock();

    await act(async () => {
      renderer = create(<Harness dataScopeKey="range:today" />);
    });

    await act(async () => {
      renderer?.update(<Harness dataScopeKey="range:7d" fromMs={2} toMs={20_000} />);
      await flushPromises();
    });

    expect(getAnalyticsMock).toHaveBeenCalledTimes(2);

    await act(async () => {
      requests[1]?.resolve(createResponse(2));
      await flushPromises();
    });

    expect(latestResult?.loading).toBe(false);
    expect(latestResult?.data?.generated_at_ms).toBe(2);

    await act(async () => {
      requests[0]?.resolve(createResponse(1));
      await flushPromises();
    });

    expect(latestResult?.loading).toBe(false);
    expect(latestResult?.data?.generated_at_ms).toBe(2);
  });

  it('clears in-flight state after a failed request and allows retry', async () => {
    const requests = installDeferredAnalyticsMock();

    await act(async () => {
      renderer = create(<Harness dataScopeKey="range:today" />);
    });

    expect(getAnalyticsMock).toHaveBeenCalledTimes(1);

    await act(async () => {
      requests[0]?.reject(new Error('network failed'));
      await flushPromises();
    });

    expect(latestResult?.loading).toBe(false);
    expect(latestResult?.error).toBe('network failed');

    await act(async () => {
      void latestResult?.refresh({ force: true });
      await flushPromises();
    });

    expect(getAnalyticsMock).toHaveBeenCalledTimes(2);
    expect(latestResult?.loading).toBe(true);
    expect(latestResult?.error).toBe('');

    await act(async () => {
      requests[1]?.resolve(createResponse(2));
      await flushPromises();
    });

    expect(latestResult?.loading).toBe(false);
    expect(latestResult?.data?.generated_at_ms).toBe(2);
  });
});
