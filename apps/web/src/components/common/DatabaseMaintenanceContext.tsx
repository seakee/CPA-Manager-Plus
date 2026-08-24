import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { usePanelFeatureAvailability } from '@/hooks/usePanelFeatureAvailability';
import { usageServiceApi, type UsageServiceStatus } from '@/services/api/usageService';
import { useAuthStore } from '@/stores';
import {
  DatabaseMaintenanceContext,
  type DatabaseMaintenanceContextValue,
} from './useDatabaseMaintenance';

const DATABASE_MAINTENANCE_REFRESH_INTERVAL_MS = 60_000;

const readErrorMessage = (error: unknown): string =>
  error instanceof Error ? error.message : typeof error === 'string' ? error : String(error);

export function DatabaseMaintenanceProvider({ children }: { children: ReactNode }) {
  const managementKey = useAuthStore((state) => state.managementKey);
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const { checking, managerServiceAvailable, managerServiceBase } = usePanelFeatureAvailability();
  const [status, setStatus] = useState<UsageServiceStatus | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const requestId = useRef(0);

  const refresh = useCallback(async () => {
    const currentRequestId = requestId.current + 1;
    requestId.current = currentRequestId;

    if (
      typeof window === 'undefined' ||
      connectionStatus !== 'connected' ||
      !managerServiceAvailable ||
      !managerServiceBase ||
      !managementKey
    ) {
      setStatus(null);
      setError('');
      setLoading(false);
      return;
    }

    setLoading(true);
    setError('');
    try {
      const nextStatus = await usageServiceApi.getStatus(
        managerServiceBase,
        managementKey,
        'database-maintenance'
      );
      if (requestId.current !== currentRequestId) return;
      setStatus(nextStatus);
    } catch (nextError: unknown) {
      if (requestId.current !== currentRequestId) return;
      // Keep the last known maintenance state visible during a transient status failure. This
      // avoids hiding a warning just because the next metadata-bounded poll was interrupted.
      setError(readErrorMessage(nextError));
    } finally {
      if (requestId.current === currentRequestId) {
        setLoading(false);
      }
    }
  }, [connectionStatus, managementKey, managerServiceAvailable, managerServiceBase]);

  useEffect(() => {
    requestId.current += 1;
    setStatus(null);
    setError('');
    setLoading(false);
  }, [
    checking,
    connectionStatus,
    managementKey,
    managerServiceAvailable,
    managerServiceBase,
  ]);

  useEffect(() => {
    if (checking) return;
    void refresh();
  }, [checking, refresh]);

  useEffect(() => {
    if (
      typeof window === 'undefined' ||
      connectionStatus !== 'connected' ||
      !managerServiceAvailable ||
      !managerServiceBase
    ) {
      return;
    }

    const timer = window.setInterval(() => {
      void refresh();
    }, DATABASE_MAINTENANCE_REFRESH_INTERVAL_MS);
    return () => window.clearInterval(timer);
  }, [connectionStatus, managerServiceAvailable, managerServiceBase, refresh]);

  useEffect(() => {
    if (typeof document === 'undefined') return;
    const handleVisibilityChange = () => {
      if (
        document.visibilityState === 'visible' &&
        connectionStatus === 'connected' &&
        managerServiceAvailable
      ) {
        void refresh();
      }
    };
    document.addEventListener('visibilitychange', handleVisibilityChange);
    return () => document.removeEventListener('visibilitychange', handleVisibilityChange);
  }, [connectionStatus, managerServiceAvailable, refresh]);

  const value = useMemo<DatabaseMaintenanceContextValue>(
    () => ({ status, loading, error, refresh }),
    [error, loading, refresh, status]
  );

  return (
    <DatabaseMaintenanceContext.Provider value={value}>
      {children}
    </DatabaseMaintenanceContext.Provider>
  );
}
