import { useCallback, useLayoutEffect, useRef, useState } from 'react';
import {
  monitoringAnalyticsApi,
  type MonitoringAnalyticsCredentialStatRow,
  type MonitoringAnalyticsInclude,
  type MonitoringAnalyticsResponse,
} from '@/services/api/usageService';

const AUTH_FILE_USAGE_FIVE_HOUR_MS = 5 * 60 * 60 * 1000;
const AUTH_FILE_USAGE_WEEKLY_MS = 7 * 24 * 60 * 60 * 1000;
const AUTH_FILE_USAGE_HISTORY_FROM_MS = 1;
const AUTH_FILE_USAGE_ANALYTICS_INCLUDE = {
  credential_stats: true,
} satisfies MonitoringAnalyticsInclude;

export type AuthFileUsageRows = {
  retained: MonitoringAnalyticsCredentialStatRow[];
  fiveHour: MonitoringAnalyticsCredentialStatRow[];
  weekly: MonitoringAnalyticsCredentialStatRow[];
};

const createEmptyRows = (): AuthFileUsageRows => ({
  retained: [],
  fiveHour: [],
  weekly: [],
});

const readCredentialStats = (
  response: MonitoringAnalyticsResponse | null
): MonitoringAnalyticsCredentialStatRow[] => response?.credential_stats ?? [];

export interface FetchAuthFileUsageRowsOptions {
  managerServiceBase: string;
  managementKey: string;
  includeRetained?: boolean;
}

export async function fetchAuthFileUsageRows({
  managerServiceBase,
  managementKey,
  includeRetained = true,
}: FetchAuthFileUsageRowsOptions): Promise<AuthFileUsageRows> {
  const nowMs = Date.now();
  const buildRequest = (fromMs: number) => ({
    from_ms: fromMs,
    to_ms: nowMs,
    now_ms: nowMs,
    include: AUTH_FILE_USAGE_ANALYTICS_INCLUDE,
  });
  const retainedRequest = includeRetained
    ? monitoringAnalyticsApi.getAnalytics(
        managerServiceBase,
        managementKey,
        buildRequest(AUTH_FILE_USAGE_HISTORY_FROM_MS)
      )
    : Promise.resolve(null);
  const [retained, fiveHour, weekly] = await Promise.all([
    retainedRequest,
    monitoringAnalyticsApi.getAnalytics(
      managerServiceBase,
      managementKey,
      buildRequest(nowMs - AUTH_FILE_USAGE_FIVE_HOUR_MS)
    ),
    monitoringAnalyticsApi.getAnalytics(
      managerServiceBase,
      managementKey,
      buildRequest(nowMs - AUTH_FILE_USAGE_WEEKLY_MS)
    ),
  ]);

  return {
    retained: readCredentialStats(retained),
    fiveHour: readCredentialStats(fiveHour),
    weekly: readCredentialStats(weekly),
  };
}

export interface UseAuthFileUsageAnalyticsOptions {
  managerServiceBase: string;
  managementKey: string;
  enabled: boolean;
  includeRetained?: boolean;
}

export function useAuthFileUsageAnalytics({
  managerServiceBase,
  managementKey,
  enabled,
  includeRetained = true,
}: UseAuthFileUsageAnalyticsOptions) {
  const [rows, setRows] = useState<AuthFileUsageRows>(createEmptyRows);
  const [loading, setLoading] = useState(false);
  const requestIdRef = useRef(0);

  const load = useCallback(async () => {
    if (!managerServiceBase || !enabled) {
      requestIdRef.current += 1;
      setRows(createEmptyRows());
      setLoading(false);
      return;
    }

    const requestId = ++requestIdRef.current;
    setLoading(true);
    try {
      const nextRows = await fetchAuthFileUsageRows({
        managerServiceBase,
        managementKey,
        includeRetained,
      });

      if (requestId !== requestIdRef.current) return;
      setRows(nextRows);
    } catch {
      if (requestId === requestIdRef.current) {
        setRows(createEmptyRows());
      }
    } finally {
      if (requestId === requestIdRef.current) {
        setLoading(false);
      }
    }
  }, [enabled, includeRetained, managementKey, managerServiceBase]);

  useLayoutEffect(() => {
    requestIdRef.current += 1;
    setRows(createEmptyRows());
    setLoading(false);
  }, [enabled, includeRetained, managementKey, managerServiceBase]);

  return { rows, loading, load };
}
