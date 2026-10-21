import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { useRequestMonitoringAvailability } from '@/hooks/useRequestMonitoringAvailability';
import { dashboardApi, type DashboardSummaryResponse } from '@/services/api/usageService';
import { useAuthStore } from '@/stores';

const REFRESH_INTERVAL_MS = 60_000;

const getTodayStartMs = () => {
  const today = new Date();
  today.setHours(0, 0, 0, 0);
  return today.getTime();
};

export interface UseDashboardUsageSummaryReturn {
  enabled: boolean;
  loading: boolean;
  error: string;
  summary: DashboardSummaryResponse | null;
  recentFailures: DashboardSummaryResponse['recent_failures'];
  topModels: DashboardSummaryResponse['top_models_today'];
  modelCostRank: NonNullable<DashboardSummaryResponse['model_cost_rank']>;
  trafficTimeline: NonNullable<DashboardSummaryResponse['traffic_timeline']>;
  hourlyActivity: NonNullable<DashboardSummaryResponse['hourly_activity']>;
  todayRequestHealthTimeline: NonNullable<DashboardSummaryResponse['today_request_health_timeline']> | null;
  tokenMix: NonNullable<DashboardSummaryResponse['token_mix']>;
  channelHealth: NonNullable<DashboardSummaryResponse['channel_health']>;
  failureSources: NonNullable<DashboardSummaryResponse['failure_sources']>;
  lastRefreshedAt: Date | null;
  serviceBase: string;
  refresh: () => Promise<void>;
}

export function useDashboardUsageSummary(): UseDashboardUsageSummaryReturn {
  const managementKey = useAuthStore((state) => state.managementKey);
  const availability = useRequestMonitoringAvailability();
  const [summary, setSummary] = useState<DashboardSummaryResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [lastRefreshedAt, setLastRefreshedAt] = useState<Date | null>(null);
  const requestIdRef = useRef(0);

  const enabled = availability.available && Boolean(availability.serviceBase);
  const serviceBase = availability.serviceBase;

  useLayoutEffect(() => {
    requestIdRef.current += 1;
    setSummary(null);
    setLastRefreshedAt(null);
    setError('');
    setLoading(enabled);
    return () => {
      requestIdRef.current += 1;
    };
  }, [enabled, managementKey, serviceBase]);

  const refresh = useCallback(async () => {
    if (!enabled || !serviceBase) {
      setSummary(null);
      setLastRefreshedAt(null);
      setLoading(false);
      return;
    }

    const requestId = requestIdRef.current + 1;
    requestIdRef.current = requestId;
    setLoading(true);
    setError('');

    try {
      const response = await dashboardApi.getSummary(serviceBase, managementKey, {
        todayStartMs: getTodayStartMs(),
        nowMs: Date.now(),
        topModels: 5,
        recentFailures: 5,
      });
      if (requestIdRef.current !== requestId) return;
      setSummary(response);
      setLastRefreshedAt(new Date());
    } catch (err) {
      if (requestIdRef.current !== requestId) return;
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      if (requestIdRef.current === requestId) {
        setLoading(false);
      }
    }
  }, [enabled, managementKey, serviceBase]);

  useEffect(() => {
    if (availability.checking) {
      return;
    }
    void refresh();
  }, [availability.checking, refresh]);

  useEffect(() => {
    if (!enabled) {
      return;
    }
    const timer = window.setInterval(() => {
      void refresh();
    }, REFRESH_INTERVAL_MS);
    return () => window.clearInterval(timer);
  }, [enabled, refresh]);

  return useMemo(
    () => ({
      enabled,
      loading: availability.checking || loading,
      error,
      summary,
      recentFailures: summary?.recent_failures ?? [],
      topModels: summary?.top_models_today ?? [],
      modelCostRank: summary?.model_cost_rank ?? [],
      trafficTimeline: summary?.traffic_timeline ?? [],
      hourlyActivity: summary?.hourly_activity ?? [],
      todayRequestHealthTimeline: summary?.today_request_health_timeline ?? null,
      tokenMix: summary?.token_mix ?? [],
      channelHealth: summary?.channel_health ?? [],
      failureSources: summary?.failure_sources ?? [],
      lastRefreshedAt,
      serviceBase,
      refresh,
    }),
    [availability.checking, enabled, error, lastRefreshedAt, loading, refresh, serviceBase, summary]
  );
}
