import { createContext, useContext } from 'react';
import type { UsageServiceStatus } from '@/services/api/usageService';

export interface DatabaseMaintenanceContextValue {
  status: UsageServiceStatus | null;
  loading: boolean;
  error: string;
  refresh: () => Promise<void>;
}

export const DatabaseMaintenanceContext = createContext<DatabaseMaintenanceContextValue | null>(
  null
);

export function useDatabaseMaintenance(): DatabaseMaintenanceContextValue {
  const context = useContext(DatabaseMaintenanceContext);
  if (context) return context;

  return {
    status: null,
    loading: false,
    error: '',
    refresh: async () => {},
  };
}
