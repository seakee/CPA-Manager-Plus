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
  evidenceScope: string;
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
  const apiBase = useAuthStore((state) => state.apiBase ?? '');
  const managementKey = useAuthStore((state) => state.managementKey);
  const connectionScope = useMemo(
    () => JSON.stringify([apiBase, managementKey]),
    [apiBase, managementKey]
  );
  const availability = useRequestMonitoringAvailability();
  const [summary, setSummary] = useState<DashboardSummaryResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [evidenceScope, setEvidenceScope] = useState('');
  const [lastRefreshedAt, setLastRefreshedAt] = useState<Date | null>(null);
  const requestIdRef = useRef(0);

  const rawEnabled = availability.available && Boolean(availability.serviceBase);
  const rawServiceBase = availability.serviceBase;

  const [settledTarget, setSettledTarget] = useState<{
    scope: string;
    enabled: boolean;
    serviceBase: string;
  }>(() => ({
    scope: connectionScope,
    enabled: rawEnabled,
    serviceBase: rawServiceBase,
  }));

  if (settledTarget.scope !== connectionScope) {
    setSettledTarget({
      scope: connectionScope,
      enabled: rawEnabled,
      serviceBase: rawServiceBase,
    });
  } else if (
    (!availability.checking || rawServiceBase !== '') &&
    (settledTarget.enabled !== rawEnabled || settledTarget.serviceBase !== rawServiceBase)
  ) {
    setSettledTarget({
      scope: connectionScope,
      enabled: rawEnabled,
      serviceBase: rawServiceBase,
    });
  }

  const retainReprobeTarget =
    settledTarget.scope === connectionScope &&
    availability.checking &&
    rawServiceBase === '';
  const enabled = retainReprobeTarget ? settledTarget.enabled : rawEnabled;
  const serviceBase = retainReprobeTarget ? settledTarget.serviceBase : rawServiceBase;
  useLayoutEffect(() => {
    requestIdRef.current += 1;
    setSummary(null);
    setLastRefreshedAt(null);
    setError('');
    setEvidenceScope('');
    setLoading(enabled);
    return () => {
      requestIdRef.current += 1;
    };
  }, [connectionScope, enabled, serviceBase]);

  const refresh = useCallback(async () => {
    if (!enabled || !serviceBase) {
      if (!availability.checking) {
        setSummary(null);
        setLastRefreshedAt(null);
        setError('');
        setEvidenceScope('');
        setLoading(false);
      }
      return;
    }

    const requestId = requestIdRef.current + 1;
    requestIdRef.current = requestId;
    const scopeAtStart = connectionScope;
    setLoading(true);

    try {
      const response = await dashboardApi.getSummary(serviceBase, managementKey, {
        todayStartMs: getTodayStartMs(),
        nowMs: Date.now(),
        topModels: 5,
        recentFailures: 5,
      });
      if (requestIdRef.current !== requestId) return;
      setSummary(response);
      setError('');
      setEvidenceScope(scopeAtStart);
      setLastRefreshedAt(new Date());
    } catch (err) {
      if (requestIdRef.current !== requestId) return;
      setError(err instanceof Error ? err.message : String(err));
      setEvidenceScope(scopeAtStart);
    } finally {
      if (requestIdRef.current === requestId) {
        setLoading(false);
      }
    }
  }, [availability.checking, connectionScope, enabled, managementKey, serviceBase]);

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
      evidenceScope,
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
    [
      availability.checking,
      enabled,
      error,
      evidenceScope,
      lastRefreshedAt,
      loading,
      refresh,
      serviceBase,
      summary,
    ]
  );
}
