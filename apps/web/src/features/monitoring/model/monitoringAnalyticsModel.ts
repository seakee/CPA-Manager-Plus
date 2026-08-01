import type {
  MonitoringAnalyticsEventsPageRequest,
  MonitoringAnalyticsInclude,
} from '@/services/api/usageService';
import type { MonitoringDataTab } from '../monitoringCenterUiState';
import { parseMonitoringAccountFilterValue } from './analyticsAdapters';
import type { MonitoringAccountRow, MonitoringTimeRange } from './types';

/**
 * Keep the monitoring request bounded to the data panel that is currently
 * visible.  Summary is deliberately compact because the monitoring page only
 * renders the core counters; the full-only diagnostics are owned by the
 * dedicated analytics page.
 */
export const buildMonitoringCenterAnalyticsInclude = (
  activeDataTab: MonitoringDataTab,
  granularity: 'hour' | 'day',
  eventsPage?: MonitoringAnalyticsEventsPageRequest | null
): MonitoringAnalyticsInclude => {
  const include: MonitoringAnalyticsInclude = {
    summary: true,
    summary_profile: 'compact',
    granularity,
  };

  if (activeDataTab === 'accounts') {
    include.account_stats = true;
  } else if (activeDataTab === 'apiKeys') {
    include.api_key_stats = true;
  }

  if (eventsPage) {
    include.events_page =
      activeDataTab === 'realtime'
        ? eventsPage
        : {
            ...eventsPage,
            before_ms: null,
            before_id: null,
          };
  }

  return include;
};

/**
 * Selector loading is independent from the visible data tab.  Keep
 * filter_options in the request for older Manager Server versions; current
 * servers prioritize filter_selectors and return the inexpensive selector
 * payload instead of recomputing all dimension aggregates.
 */
export const buildMonitoringFilterSelectorsInclude = (): MonitoringAnalyticsInclude => ({
  filter_options: true,
  filter_selectors: true,
});

export const buildMonitoringFilterSelectorsScopeKey = (
  timeRange: MonitoringTimeRange,
  bounds: { startMs: number; endMs: number } | null,
  searchQuery: string,
  searchApiKeyHash?: string
) =>
  JSON.stringify({
    range: timeRange,
    bounds: timeRange === 'custom' ? bounds : null,
    searchQuery,
    searchApiKeyHash,
  });

export const mergeMonitoringAccountOptionRows = (
  ...collections: ReadonlyArray<readonly MonitoringAccountRow[]>
) => {
  const rowsByFilterValue = new Map<string, MonitoringAccountRow>();
  collections.forEach((collection) => {
    collection.forEach((row) => {
      const key = row.filterValue?.trim() || row.id;
      if (!rowsByFilterValue.has(key)) {
        rowsByFilterValue.set(key, row);
      }
    });
  });

  const rows = Array.from(rowsByFilterValue.values());
  const accountsWithIdentitySelectors = new Set(
    rows
      .filter((row) => {
        const criteria = parseMonitoringAccountFilterValue(row.filterValue);
        return criteria.authIndices.length > 0 || criteria.sourceHashes.length > 0;
      })
      .map((row) => row.account.trim().toLowerCase())
      .filter(Boolean)
  );

  return rows.filter((row) => {
    const criteria = parseMonitoringAccountFilterValue(row.filterValue);
    const isPlainAccountSelector =
      row.id === `selector:${row.account.trim().toLowerCase()}` &&
      criteria.accounts.length > 0 &&
      criteria.authIndices.length === 0 &&
      criteria.sourceHashes.length === 0 &&
      criteria.apiKeyHashes.length === 0;
    return (
      !isPlainAccountSelector ||
      !accountsWithIdentitySelectors.has(row.account.trim().toLowerCase())
    );
  });
};

export const resolveMonitoringDimensionCounts = ({
  activeDataTab,
  accountRowCount,
  apiKeyRowCount,
  accountSelectorCount,
  apiKeySelectorCount,
}: {
  activeDataTab: MonitoringDataTab;
  accountRowCount: number;
  apiKeyRowCount: number;
  accountSelectorCount: number;
  apiKeySelectorCount: number;
}) => ({
  accountCount: activeDataTab === 'accounts' ? accountRowCount : accountSelectorCount,
  apiKeyCount: activeDataTab === 'apiKeys' ? apiKeyRowCount : apiKeySelectorCount,
});
