import { useEffect } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { dashboardApi, type DashboardSummaryResponse } from '@/services/api/usageService';
import {
  useDashboardUsageSummary,
  type UseDashboardUsageSummaryReturn,
} from './useDashboardUsageSummary';

const state = vi.hoisted(() => ({
  apiBase: 'http://gw-a:8317',
  key: 'a',
  base: 'http://a',
  available: true,
  checking: false,
}));
vi.mock('@/stores', () => ({
  useAuthStore: (selector: (value: { apiBase: string; managementKey: string }) => unknown) =>
    selector({ apiBase: state.apiBase, managementKey: state.key }),
}));
vi.mock('@/hooks/useRequestMonitoringAvailability', () => ({
  useRequestMonitoringAvailability: () => ({
    available: state.available,
    checking: state.checking,
    serviceBase: state.base,
  }),
}));
vi.mock('@/services/api/usageService', () => ({ dashboardApi: { getSummary: vi.fn() } }));
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const deferred = () => {
  let resolve!: (value: DashboardSummaryResponse) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<DashboardSummaryResponse>((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { promise, resolve, reject };
};

let renderer: ReactTestRenderer;
let latest: UseDashboardUsageSummaryReturn;
function Harness() {
  const result = useDashboardUsageSummary();
  useEffect(() => {
    latest = result;
  }, [result]);
  return <span>{result.summary?.generated_at_ms}</span>;
}

afterEach(() => {
  act(() => renderer?.unmount());
  vi.unstubAllGlobals();
  vi.clearAllMocks();
  state.apiBase = 'http://gw-a:8317';
  state.key = 'a';
  state.base = 'http://a';
  state.available = true;
  state.checking = false;
});

describe('dashboard summary revalidation', () => {
  it('retains a successful snapshot and active error across an in-flight retry until the retry resolves (P1-3)', async () => {
    vi.stubGlobal('window', { setInterval, clearInterval });
    const snapshot = { generated_at_ms: 1 } as DashboardSummaryResponse;
    vi.mocked(dashboardApi.getSummary).mockResolvedValueOnce(snapshot);
    await act(async () => {
      renderer = create(<Harness />);
    });
    expect(latest.evidenceScope).toBe(JSON.stringify(['http://gw-a:8317', 'a']));

    const failedPoll = deferred();
    vi.mocked(dashboardApi.getSummary).mockReturnValueOnce(failedPoll.promise);
    let pendingFailure!: Promise<void>;
    act(() => {
      pendingFailure = latest.refresh();
    });
    expect(latest.loading).toBe(true);
    expect(renderer.root.findByType('span').children).toEqual(['1']);
    await act(async () => {
      failedPoll.reject(new Error('offline'));
      await pendingFailure;
    });
    expect(latest.error).toBe('offline');
    expect(renderer.root.findByType('span').children).toEqual(['1']);

    const retryPoll = deferred();
    vi.mocked(dashboardApi.getSummary).mockReturnValueOnce(retryPoll.promise);
    let pendingRetry!: Promise<void>;
    act(() => {
      pendingRetry = latest.refresh();
    });
    // P1-3: error MUST remain 'offline' while retry is still in flight
    expect(latest.loading).toBe(true);
    expect(latest.error).toBe('offline');

    await act(async () => {
      retryPoll.resolve({ generated_at_ms: 2 } as DashboardSummaryResponse);
      await pendingRetry;
    });
    expect(latest.error).toBe('');
    expect(renderer.root.findByType('span').children).toEqual(['2']);
  });

  it('retains same-connection snapshot and error during transient capability re-probe (checking=true)', async () => {
    vi.stubGlobal('window', { setInterval, clearInterval });
    vi.mocked(dashboardApi.getSummary)
      .mockResolvedValueOnce({ generated_at_ms: 10 } as DashboardSummaryResponse)
      .mockRejectedValueOnce(new Error('summary 502'));

    await act(async () => {
      renderer = create(<Harness />);
    });
    await act(async () => {
      await latest.refresh();
    });
    expect(latest.summary?.generated_at_ms).toBe(10);
    expect(latest.error).toBe('summary 502');

    // Simulate refreshPanelFeatureAvailability() on the same gateway connection
    state.checking = true;
    state.available = false;
    state.base = '';
    await act(async () => {
      renderer.update(<Harness />);
    });
    expect(latest.enabled).toBe(true);
    expect(latest.summary?.generated_at_ms).toBe(10);
    expect(latest.error).toBe('summary 502');
    expect(latest.evidenceScope).toBe(JSON.stringify(['http://gw-a:8317', 'a']));
  });

  it('never lets a previous connection populate a new one', async () => {
    vi.stubGlobal('window', { setInterval, clearInterval });
    const old = deferred(),
      next = deferred();
    vi.mocked(dashboardApi.getSummary)
      .mockReturnValueOnce(old.promise)
      .mockReturnValueOnce(next.promise);
    await act(async () => {
      renderer = create(<Harness />);
    });
    state.apiBase = 'http://gw-b:8317';
    state.key = 'b';
    state.base = 'http://b';
    await act(async () => {
      renderer.update(<Harness />);
    });
    await act(async () => {
      old.resolve({ generated_at_ms: 1 } as DashboardSummaryResponse);
    });
    expect(latest.summary).toBeNull();
    await act(async () => {
      next.resolve({ generated_at_ms: 2 } as DashboardSummaryResponse);
    });
    expect(latest.summary?.generated_at_ms).toBe(2);
    expect(latest.evidenceScope).toBe(JSON.stringify(['http://gw-b:8317', 'b']));
  });
});
