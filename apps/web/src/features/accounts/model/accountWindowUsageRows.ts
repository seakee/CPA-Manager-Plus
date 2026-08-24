import type {
  MonitoringAccountWindowModelScope,
  MonitoringAccountWindowUsageItem,
  MonitoringAccountWindowUsageTarget,
} from '@/services/api/usageService';
import type { AccountRow } from './accountRows';
import { buildAccountHistoryTargetEntries } from './accountHistoryRows';
import {
  buildAccountQuotaUsageRanges,
  type AccountQuotaUsagePeriod,
  type AccountQuotaWindowDefinition,
} from './accountQuotaWindowDefinitions';

export interface AccountWindowUsageWindow {
  key: string;
  fromMs: number | null;
  toMs: number | null;
  providerWindowId?: string;
  period?: AccountQuotaUsagePeriod;
  modelScope?: MonitoringAccountWindowModelScope;
}

export interface AccountWindowUsageTargetEntry {
  rowKey: string;
  windowKey: string;
  providerWindowId: string;
  period: AccountQuotaUsagePeriod;
  requestKey: string;
  target: MonitoringAccountWindowUsageTarget;
}

const modelScopeRequestPart = (scope: MonitoringAccountWindowModelScope | undefined) =>
  JSON.stringify([
    (scope?.kind ?? 'all').trim().toLowerCase(),
    scope?.key?.trim().toLowerCase() ?? '',
    scope?.complete !== false,
    ...Array.from(
      new Set((scope?.models ?? []).map((model) => model.trim().toLowerCase()).filter(Boolean))
    ).sort(),
  ]);

export const accountWindowUsageRequestKey = (
  rowKey: string,
  providerWindowId: string,
  period: AccountQuotaUsagePeriod = 'current',
  scope?: MonitoringAccountWindowModelScope
) => `${rowKey}\u0000${providerWindowId}\u0000${modelScopeRequestPart(scope)}\u0000${period}`;

const isQuotaWindowDefinition = (
  window: AccountWindowUsageWindow | AccountQuotaWindowDefinition
): window is AccountQuotaWindowDefinition => 'windowMode' in window;

const hasQueryableModelScope = (definition: AccountQuotaWindowDefinition): boolean => {
  const scope = definition.modelScope;
  if (scope.complete === false) return false;
  if (scope.kind === 'all') return true;
  if (scope.kind === 'models') return (scope.models?.length ?? 0) > 0;
  return Boolean(scope.key) || (scope.models?.length ?? 0) > 0;
};

type AccountWindowCredentialTarget = Pick<
  MonitoringAccountWindowUsageTarget,
  | 'account_snapshot'
  | 'auth_label_snapshot'
  | 'auth_file_snapshot'
  | 'auth_provider_snapshot'
  | 'auth_project_id_snapshot'
  | 'auth_index'
  | 'source'
>;

const hasCredentialIdentity = (target: AccountWindowCredentialTarget): boolean => {
  const authFile = target.auth_file_snapshot?.trim() ?? '';
  const account = target.account_snapshot?.trim() ?? '';
  const label = target.auth_label_snapshot?.trim() ?? '';
  const source = target.source?.trim() ?? '';
  const provider = target.auth_provider_snapshot?.trim() ?? '';
  if (authFile || (source && source !== account && source !== label)) return Boolean(provider);
  if (!provider) return false;
  return Boolean(
    target.auth_index?.trim() || target.auth_project_id_snapshot?.trim() || account || label
  );
};

export const buildAccountWindowUsageTargetEntries = (
  rows: AccountRow[],
  windowsByRowKey: Map<string, Array<AccountWindowUsageWindow | AccountQuotaWindowDefinition>>,
  nowMs = Date.now()
): AccountWindowUsageTargetEntry[] => {
  const targetByRowKey = new Map(
    buildAccountHistoryTargetEntries(rows).map((entry) => [entry.rowKey, entry.target])
  );
  const entries: AccountWindowUsageTargetEntry[] = [];

  rows.forEach((row) => {
    const accountTarget = targetByRowKey.get(row.selectionKey);
    if (!accountTarget || !hasCredentialIdentity(accountTarget)) return;
    const windows = windowsByRowKey.get(row.selectionKey) ?? [];
    windows.forEach((window) => {
      if (isQuotaWindowDefinition(window) && !hasQueryableModelScope(window)) return;
      const providerWindowId = isQuotaWindowDefinition(window)
        ? window.providerWindowId
        : (window.providerWindowId ?? window.key);
      const modelScope: MonitoringAccountWindowModelScope = isQuotaWindowDefinition(window)
        ? {
            kind: window.modelScope.kind,
            key: window.modelScope.key,
            models: window.modelScope.models,
            complete: window.modelScope.complete !== false,
          }
        : (window.modelScope ?? { kind: 'all', complete: true });
      const ranges = isQuotaWindowDefinition(window)
        ? buildAccountQuotaUsageRanges(window, nowMs)
        : window.fromMs && window.toMs && window.fromMs < window.toMs
          ? [
              {
                period: window.period ?? ('current' as const),
                fromMs: window.fromMs,
                toMs: window.toMs,
              },
            ]
          : [];
      ranges.forEach((range) => {
        const requestKey = accountWindowUsageRequestKey(
          row.selectionKey,
          providerWindowId,
          range.period,
          modelScope
        );
        entries.push({
          rowKey: row.selectionKey,
          windowKey: window.key,
          providerWindowId,
          period: range.period,
          requestKey,
          target: {
            request_key: requestKey,
            row_key: row.selectionKey,
            window_key: window.key,
            provider_window_id: providerWindowId,
            period: range.period,
            from_ms: range.fromMs,
            to_ms: range.toMs,
            model_scope: modelScope,
            account_snapshot: accountTarget.account_snapshot,
            auth_label_snapshot: accountTarget.auth_label_snapshot,
            auth_file_snapshot: accountTarget.auth_file_snapshot,
            auth_provider_snapshot: accountTarget.auth_provider_snapshot,
            auth_project_id_snapshot: accountTarget.auth_project_id_snapshot,
            auth_index: accountTarget.auth_index,
            source: accountTarget.source,
          },
        });
      });
    });
  });

  return entries;
};

export const buildAccountWindowUsageByKey = (
  entries: AccountWindowUsageTargetEntry[],
  items: MonitoringAccountWindowUsageItem[]
): Map<string, MonitoringAccountWindowUsageItem> => {
  const result = new Map<string, MonitoringAccountWindowUsageItem>();
  const matchedEntries = new Set<number>();
  const matchedItems = new Set<number>();
  const normalizedPeriod = (value: MonitoringAccountWindowUsageItem['period']) =>
    value ?? 'current';
  const responseHasRequestKey = (item: MonitoringAccountWindowUsageItem) =>
    Boolean(item.request_key?.trim());

  const uniqueIndexByKey = <T>(
    values: Array<{ index: number; value: T }>,
    getKey: (value: T) => string | null
  ): Map<string, number> => {
    const unique = new Map<string, number>();
    const duplicates = new Set<string>();
    values.forEach(({ index, value }) => {
      const key = getKey(value);
      if (!key || duplicates.has(key)) return;
      if (unique.has(key)) {
        unique.delete(key);
        duplicates.add(key);
        return;
      }
      unique.set(key, index);
    });
    return unique;
  };

  const matchUnique = (
    entryKey: (entry: AccountWindowUsageTargetEntry) => string | null,
    itemKey: (item: MonitoringAccountWindowUsageItem) => string | null
  ) => {
    const availableEntries = entries
      .map((entry, index) => ({ index, value: entry }))
      .filter(({ index }) => !matchedEntries.has(index));
    const availableItems = items
      .map((item, index) => ({ index, value: item }))
      .filter(({ index }) => !matchedItems.has(index));
    const entryIndexes = uniqueIndexByKey(availableEntries, entryKey);
    const itemIndexes = uniqueIndexByKey(availableItems, itemKey);
    entryIndexes.forEach((entryIndex, key) => {
      const itemIndex = itemIndexes.get(key);
      if (itemIndex === undefined) return;
      matchedEntries.add(entryIndex);
      matchedItems.add(itemIndex);
      result.set(entries[entryIndex].requestKey, items[itemIndex]);
    });
  };

  matchUnique(
    (entry) => entry.requestKey.trim() || null,
    (item) => item.request_key?.trim() || null
  );
  matchUnique(
    (entry) =>
      [entry.rowKey.trim(), entry.windowKey.trim(), entry.period].every(Boolean)
        ? [entry.rowKey.trim(), entry.windowKey.trim(), entry.period].join('\u0000')
        : null,
    (item) => {
      if (responseHasRequestKey(item)) return null;
      const rowKey = item.row_key?.trim();
      const windowKey = item.window_key?.trim();
      return rowKey && windowKey
        ? [rowKey, windowKey, normalizedPeriod(item.period)].join('\u0000')
        : null;
    }
  );
  matchUnique(
    (entry) =>
      [entry.rowKey.trim(), entry.providerWindowId.trim(), entry.period].every(Boolean)
        ? [entry.rowKey.trim(), entry.providerWindowId.trim(), entry.period].join('\u0000')
        : null,
    (item) => {
      if (responseHasRequestKey(item) || item.window_key?.trim()) return null;
      const rowKey = item.row_key?.trim();
      const providerWindowId = item.provider_window_id?.trim();
      return rowKey && providerWindowId
        ? [rowKey, providerWindowId, normalizedPeriod(item.period)].join('\u0000')
        : null;
    }
  );

  const remainingEntryIndexes = entries
    .map((_, index) => index)
    .filter((index) => !matchedEntries.has(index));
  const remainingItemIndexes = items
    .map((_, index) => index)
    .filter((index) => !matchedItems.has(index));
  if (remainingEntryIndexes.length === 1 && remainingItemIndexes.length === 1) {
    const entry = entries[remainingEntryIndexes[0]];
    const item = items[remainingItemIndexes[0]];
    const compatible =
      !responseHasRequestKey(item) &&
      (!item.row_key?.trim() || item.row_key.trim() === entry.rowKey) &&
      (!item.window_key?.trim() || item.window_key.trim() === entry.windowKey) &&
      (!item.provider_window_id?.trim() ||
        item.provider_window_id.trim() === entry.providerWindowId) &&
      (!item.period || normalizedPeriod(item.period) === entry.period);
    if (compatible) {
      result.set(entry.requestKey, item);
    }
  }
  return result;
};

export const filterAccountWindowUsageByTargetRanges = (
  entries: AccountWindowUsageTargetEntry[],
  usageByKey: Map<string, MonitoringAccountWindowUsageItem>
): Map<string, MonitoringAccountWindowUsageItem> => {
  const result = new Map<string, MonitoringAccountWindowUsageItem>();
  entries.forEach((entry) => {
    const usage = usageByKey.get(entry.requestKey);
    if (!usage) return;
    if (usage.from_ms !== entry.target.from_ms || usage.to_ms !== entry.target.to_ms) return;
    result.set(entry.requestKey, usage);
  });
  return result;
};
