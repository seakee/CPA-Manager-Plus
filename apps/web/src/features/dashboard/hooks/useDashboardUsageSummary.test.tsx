import { useEffect } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { dashboardApi, type DashboardSummaryResponse } from '@/services/api/usageService';
import { useDashboardUsageSummary } from './useDashboardUsageSummary';

const state = vi.hoisted(() => ({ key: 'a', base: 'http://a' }));
vi.mock('@/stores', () => ({
  useAuthStore: (selector: (value: { managementKey: string }) => unknown) =>
    selector({ managementKey: state.key }),
}));
vi.mock('@/hooks/useRequestMonitoringAvailability', () => ({
  useRequestMonitoringAvailability: () => ({
    available: true,
    checking: false,
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
let latest: ReturnType<typeof useDashboardUsageSummary>;
function Harness() {
  const result = useDashboardUsageSummary();
  useEffect(() => { latest = result; }, [result]);
  return <span>{result.summary?.generated_at_ms}</span>;
}
afterEach(() => {
  act(() => renderer?.unmount());
  vi.unstubAllGlobals();
  vi.clearAllMocks();
  state.key = 'a';
  state.base = 'http://a';
});

describe('dashboard summary revalidation', () => {
  it('retains a successful snapshot through pending/failure and replaces it on success', async () => {
    vi.stubGlobal('window', { setInterval, clearInterval });
    const snapshot = { generated_at_ms: 1 } as DashboardSummaryResponse;
    vi.mocked(dashboardApi.getSummary).mockResolvedValueOnce(snapshot);
    await act(async () => {
      renderer = create(<Harness />);
    });
    const next = deferred();
    vi.mocked(dashboardApi.getSummary).mockReturnValueOnce(next.promise);
    let pending!: Promise<void>;
    act(() => {
      pending = latest.refresh();
    });
    expect(latest.loading).toBe(true);
    expect(renderer.root.findByType('span').children).toEqual(['1']);
    await act(async () => {
      next.reject(new Error('offline'));
      await pending;
    });
    expect(latest.error).toBe('offline');
    expect(renderer.root.findByType('span').children).toEqual(['1']);
    vi.mocked(dashboardApi.getSummary).mockResolvedValueOnce({
      generated_at_ms: 2,
    } as DashboardSummaryResponse);
    await act(async () => {
      await latest.refresh();
    });
    expect(renderer.root.findByType('span').children).toEqual(['2']);
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
  });
});
