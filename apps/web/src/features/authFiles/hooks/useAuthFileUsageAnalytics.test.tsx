import { useEffect } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { MonitoringAnalyticsResponse } from '@/services/api/usageService';
import { useAuthFileUsageAnalytics } from './useAuthFileUsageAnalytics';

const { mocks } = vi.hoisted(() => ({
  mocks: {
    getAnalytics: vi.fn(),
  },
}));

vi.mock('@/services/api/usageService', () => ({
  monitoringAnalyticsApi: {
    getAnalytics: mocks.getAnalytics,
  },
}));

function Harness({ onRows }: { onRows: (value: { fiveHour: number; weekly: number }) => void }) {
  const { rows, load } = useAuthFileUsageAnalytics({
    managerServiceBase: 'http://manager.local:18317',
    managementKey: 'test-key',
    enabled: true,
    includeRetained: false,
  });

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    onRows({ fiveHour: rows.fiveHour.length, weekly: rows.weekly.length });
  }, [onRows, rows]);

  return null;
}

describe('useAuthFileUsageAnalytics', () => {
  beforeEach(() => {
    (
      globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT?: boolean }
    ).IS_REACT_ACT_ENVIRONMENT = true;
    mocks.getAnalytics.mockReset();
  });

  it('loads only 5-hour and weekly credential stats when retained history is disabled', async () => {
    const response = (id: string): MonitoringAnalyticsResponse => ({
      generated_at_ms: 1_700_000_000_000,
      granularity: 'hour',
      credential_stats: [
        {
          id,
          auth_file_snapshot: 'codex-main.json',
          auth_index: '0',
          calls: 1,
          success_calls: 1,
          failure_calls: 0,
          success_rate: 1,
          input_tokens: 0,
          output_tokens: 0,
          cached_tokens: 0,
          cache_read_tokens: 0,
          cache_creation_tokens: 0,
          total_tokens: 1,
          cost: 0.01,
          average_latency_ms: null,
          last_seen_ms: 0,
        },
      ],
    });
    mocks.getAnalytics
      .mockResolvedValueOnce(response('five-hour'))
      .mockResolvedValueOnce(response('weekly'));
    const onRows = vi.fn();
    let renderer!: ReactTestRenderer;

    await act(async () => {
      renderer = create(<Harness onRows={onRows} />);
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(mocks.getAnalytics).toHaveBeenCalledTimes(2);
    expect(mocks.getAnalytics.mock.calls[0]?.[2].include).toEqual({ credential_stats: true });
    expect(onRows).toHaveBeenLastCalledWith({ fiveHour: 1, weekly: 1 });

    act(() => renderer.unmount());
  });
});
