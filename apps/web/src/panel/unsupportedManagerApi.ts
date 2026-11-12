// Panel-only build replacement for the legacy Manager API module. Existing
// shared screens can retain their imports, but cannot issue a Manager request.
// Type exports disappear during build and do not pull in the legacy transport.
export type * from '../services/api/usageService';
import type * as LegacyManager from '../services/api/usageService';
import { normalizeApiBase } from '../utils/connection';

export const USAGE_SERVICE_ID = 'cpa-manager-plus';
export const LEGACY_USAGE_SERVICE_ID = 'cpa-manager';
export const LEGACY_USAGE_SERVICE_IDS = [LEGACY_USAGE_SERVICE_ID, 'cpa-usage-service'] as const;
export const USAGE_SERVICE_LAST_CPA_BASE_KEY = 'cpa-manager-plus:last-cpa-base';
export const LEGACY_USAGE_SERVICE_LAST_CPA_BASE_KEY = 'cpa-manager:last-cpa-base';
export const LEGACY_USAGE_SERVICE_LAST_CPA_BASE_KEYS = [
  LEGACY_USAGE_SERVICE_LAST_CPA_BASE_KEY,
  'cpa-usage-service:last-cpa-base',
] as const;

export const normalizeUsageServiceBase = normalizeApiBase;
export const isUsageServiceId = () => false;
export const getUsageServiceErrorCode = () => '';

const unsupported = async (): Promise<never> => {
  throw new Error('Manager API is unavailable in Panel Lite');
};

// Keep the existing call signatures without adding endpoint wrappers or a
// second transport. Every legacy Manager operation stops before network I/O.
const unavailable = <T extends object>(): T => new Proxy({} as T, { get: () => unsupported });
export const usageServiceApi = unavailable<typeof LegacyManager.usageServiceApi>();
export const dashboardApi = unavailable<typeof LegacyManager.dashboardApi>();
export const monitoringAnalyticsApi = unavailable<typeof LegacyManager.monitoringAnalyticsApi>();
export const accountQuotaSnapshotApi = unavailable<typeof LegacyManager.accountQuotaSnapshotApi>();
