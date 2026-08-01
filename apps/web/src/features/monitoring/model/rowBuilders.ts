import { formatApiKeyHashLabel } from './base';
import { calculateCacheHitRateFromTotals, getCacheHitTotals } from '@/utils/usage';
import {
  sanitizeApiKeyDisplayText,
  shouldPreferApiKeyAlias,
  type ApiKeyDisplayInfo,
} from './apiKeys';
import {
  buildMonitoringAccountFilterValue,
  parseMonitoringAccountFilterValue,
} from './analyticsAdapters';
import { getRangeBounds } from './range';
import type {
  MonitoringAccountRow,
  MonitoringApiKeyRow,
  MonitoringCustomTimeRange,
  MonitoringEventRow,
  MonitoringRealtimeRow,
  MonitoringScopeFilters,
  MonitoringSummary,
  MonitoringTimeRange,
} from './types';

const UNKNOWN_API_KEY_GROUP_PREFIX = 'unknown-client-api-key';

const isEffectiveLabel = (value: string) => {
  const trimmed = value.trim();
  return Boolean(trimmed) && trimmed !== '-';
};

const looksLikeMaskedUsageSource = (value: string) => {
  const trimmed = value.trim();
  return trimmed.startsWith('m:') || trimmed.startsWith('k:');
};

const resolveAccountDisplayName = (account: string, channels: Iterable<string>) => {
  const channelLabels = Array.from(new Set(Array.from(channels).filter(isEffectiveLabel)));
  if (looksLikeMaskedUsageSource(account) && channelLabels.length === 1) {
    return channelLabels[0];
  }
  return account || channelLabels[0] || '-';
};

export const shouldIncludeInStats = (
  row: Pick<MonitoringEventRow, 'failed' | 'inputTokens' | 'outputTokens'>
) => row.failed || row.inputTokens > 0 || row.outputTokens > 0;

export const buildRangeFilteredRows = (
  rows: MonitoringEventRow[],
  timeRange: MonitoringTimeRange,
  customTimeRange: MonitoringCustomTimeRange | null | undefined,
  searchQuery: string,
  searchApiKeyHash?: string
) => {
  const nowMs = Date.now();
  const bounds = getRangeBounds(timeRange, nowMs, customTimeRange);
  const normalizedQuery = searchQuery.trim().toLowerCase();
  const normalizedSearchApiKeyHash = String(searchApiKeyHash || '')
    .trim()
    .toLowerCase();
  if (!bounds) return [];

  return rows.filter((row) => {
    if (row.timestampMs < bounds.startMs || row.timestampMs > bounds.endMs) {
      return false;
    }

    if (
      normalizedQuery &&
      !row.searchText.includes(normalizedQuery) &&
      !(normalizedSearchApiKeyHash && row.apiKeyHash === normalizedSearchApiKeyHash)
    ) {
      return false;
    }

    return true;
  });
};

const isActiveScopeFilterValue = (value: string | null | undefined) =>
  Boolean(value && value.trim() && value !== 'all');

const normalizeScopeValue = (value: string | null | undefined) =>
  String(value || '')
    .trim()
    .toLowerCase();

const hasCacheActivity = (
  row: Pick<MonitoringEventRow, 'cachedTokens' | 'cacheReadTokens' | 'cacheCreationTokens'>,
  mode: string
) => {
  const cachedTokens = row.cachedTokens || 0;
  const cacheReadTokens = row.cacheReadTokens || 0;
  const cacheCreationTokens = row.cacheCreationTokens || 0;
  if (mode === 'read') return cacheReadTokens > 0;
  if (mode === 'creation') return cacheCreationTokens > 0;
  return cachedTokens > 0 || cacheReadTokens > 0 || cacheCreationTokens > 0;
};

export const buildScopeFilteredRows = (
  rows: MonitoringEventRow[],
  scopeFilters?: MonitoringScopeFilters
) => {
  if (!scopeFilters) return rows;

  const accountCriteria = parseMonitoringAccountFilterValue(scopeFilters.account);
  const account = normalizeScopeValue(accountCriteria.accounts[0] || scopeFilters.account);
  const accountAuthIndices = new Set(accountCriteria.authIndices.map(normalizeScopeValue));
  const accountApiKeyHashes = new Set(accountCriteria.apiKeyHashes.map(normalizeScopeValue));
  const hasAccountSourceHashFilter = accountCriteria.sourceHashes.length > 0;
  const provider = normalizeScopeValue(scopeFilters.provider);
  const authFile = normalizeScopeValue(scopeFilters.authFile);
  const projectId = normalizeScopeValue(scopeFilters.projectId);
  const requestType = normalizeScopeValue(scopeFilters.requestType);
  const model = normalizeScopeValue(scopeFilters.model);
  const channel = normalizeScopeValue(scopeFilters.channel);
  const apiKeyHash = normalizeScopeValue(scopeFilters.apiKeyHash);
  const status = scopeFilters.status;
  const minLatencyMs =
    typeof scopeFilters.minLatencyMs === 'number' && scopeFilters.minLatencyMs > 0
      ? scopeFilters.minLatencyMs
      : null;
  const cacheStatus = normalizeScopeValue(scopeFilters.cacheStatus);
  const headerTraceId = normalizeScopeValue(scopeFilters.headerTraceId);

  return rows.filter((row) => {
    if (isActiveScopeFilterValue(scopeFilters.account)) {
      if (accountAuthIndices.size > 0) {
        if (!accountAuthIndices.has(normalizeScopeValue(row.authIndex))) return false;
      } else if (accountApiKeyHashes.size > 0) {
        if (!accountApiKeyHashes.has(normalizeScopeValue(row.apiKeyHash))) return false;
      } else if (hasAccountSourceHashFilter) {
        // source_hash is only available in analytics payloads, so avoid dropping rows after
        // the backend has already applied the exact source_hash filter.
      } else {
        const rowAccountValues = [
          row.account,
          row.accountMasked,
          row.authLabel,
          row.source,
          row.sourceMasked,
          row.authIndex,
        ].map(normalizeScopeValue);
        if (!rowAccountValues.includes(account)) return false;
      }
    }

    if (
      isActiveScopeFilterValue(scopeFilters.provider) &&
      normalizeScopeValue(row.provider) !== provider
    ) {
      return false;
    }

    if (
      isActiveScopeFilterValue(scopeFilters.authFile) &&
      normalizeScopeValue(row.source) !== authFile &&
      normalizeScopeValue(row.sourceMasked) !== authFile &&
      !normalizeScopeValue(row.searchText).includes(authFile)
    ) {
      return false;
    }

    if (
      isActiveScopeFilterValue(scopeFilters.projectId) &&
      normalizeScopeValue(row.projectId) !== projectId
    ) {
      return false;
    }

    if (
      isActiveScopeFilterValue(scopeFilters.requestType) &&
      normalizeScopeValue(row.executorType) !== requestType
    ) {
      return false;
    }

    if (isActiveScopeFilterValue(scopeFilters.model) && normalizeScopeValue(row.model) !== model) {
      return false;
    }

    if (
      isActiveScopeFilterValue(scopeFilters.channel) &&
      normalizeScopeValue(row.channel) !== channel
    ) {
      return false;
    }

    if (
      isActiveScopeFilterValue(scopeFilters.apiKeyHash) &&
      normalizeScopeValue(row.apiKeyHash) !== apiKeyHash
    ) {
      return false;
    }

    if (status === 'failed' && !row.failed) return false;
    if (status === 'success' && row.failed) return false;
    if (minLatencyMs !== null && (row.latencyMs === null || row.latencyMs < minLatencyMs)) {
      return false;
    }
    if (cacheStatus === 'hit' && !hasCacheActivity(row, cacheStatus)) return false;
    if (cacheStatus === 'miss' && hasCacheActivity(row, cacheStatus)) return false;
    if (
      (cacheStatus === 'read' || cacheStatus === 'creation') &&
      !hasCacheActivity(row, cacheStatus)
    ) {
      return false;
    }
    if (
      isActiveScopeFilterValue(scopeFilters.headerTraceId) &&
      normalizeScopeValue(row.headerTraceId) !== headerTraceId
    ) {
      return false;
    }

    return true;
  });
};

const buildRecentPattern = (rows: MonitoringEventRow[], limit = 10) =>
  rows
    .slice()
    .sort((left, right) => right.timestampMs - left.timestampMs)
    .slice(0, limit)
    .reverse()
    .map((row) => !row.failed);

export const buildMonitoringSummary = (rows: MonitoringEventRow[]): MonitoringSummary => {
  const totalCalls = rows.length;
  const failureCalls = rows.filter((row) => row.failed).length;
  const successCalls = Math.max(totalCalls - failureCalls, 0);
  const inputTokens = rows.reduce((sum, row) => sum + row.inputTokens, 0);
  const outputTokens = rows.reduce((sum, row) => sum + row.outputTokens, 0);
  const reasoningTokens = rows.reduce((sum, row) => sum + row.reasoningTokens, 0);
  const cachedTokens = rows.reduce((sum, row) => sum + row.cachedTokens, 0);
  const cacheReadTokens = rows.reduce((sum, row) => sum + (row.cacheReadTokens ?? 0), 0);
  const cacheCreationTokens = rows.reduce((sum, row) => sum + (row.cacheCreationTokens ?? 0), 0);
  const cacheHitTotals = rows.reduce(
    (totals, row) => {
      const rowTotals = getCacheHitTotals({
        modelName: row.resolvedModel || row.model,
        inputTokens: row.inputTokens,
        cachedTokens: row.cachedTokens,
        cacheReadTokens: row.cacheReadTokens,
        cacheCreationTokens: row.cacheCreationTokens,
      });
      totals.hitTokens += rowTotals.hitTokens;
      totals.inputTokens += rowTotals.inputTokens;
      return totals;
    },
    { hitTokens: 0, inputTokens: 0 }
  );
  const totalTokens = rows.reduce((sum, row) => sum + row.totalTokens, 0);
  const totalCost = rows.reduce((sum, row) => sum + row.totalCost, 0);

  let latencySum = 0;
  let latencyCount = 0;
  rows.forEach((row) => {
    if (row.latencyMs === null) return;
    latencySum += row.latencyMs;
    latencyCount += 1;
  });

  const taskMap = new Map<string, boolean>();
  rows.forEach((row) => {
    const existing = taskMap.get(row.taskKey) ?? false;
    taskMap.set(row.taskKey, existing || row.failed);
  });

  const approxTasks = taskMap.size;
  const approxTaskFailures = Array.from(taskMap.values()).filter(Boolean).length;
  const zeroTokenRows = rows.filter((row) => row.totalTokens === 0);

  const activeDays = new Set(rows.map((row) => row.dayKey));
  const activeDayCount = Math.max(activeDays.size, 1);
  const nowMs = Date.now();
  const windowStart = nowMs - 30 * 60 * 1000;
  const recentRows = rows.filter(
    (row) => row.timestampMs >= windowStart && row.timestampMs <= nowMs
  );
  const recentTokens = recentRows.reduce((sum, row) => sum + row.totalTokens, 0);

  return {
    totalCalls,
    successCalls,
    failureCalls,
    successRate: totalCalls > 0 ? successCalls / totalCalls : 1,
    inputTokens,
    outputTokens,
    reasoningTokens,
    cachedTokens,
    cacheReadTokens,
    cacheCreationTokens,
    cacheHitRate: calculateCacheHitRateFromTotals(
      cacheHitTotals.hitTokens,
      cacheHitTotals.inputTokens
    ),
    totalTokens,
    totalCost,
    averageLatencyMs: latencyCount > 0 ? latencySum / latencyCount : null,
    rpm30m: recentRows.length / 30,
    tpm30m: recentTokens / 30,
    avgDailyRequests: totalCalls / activeDayCount,
    avgDailyTokens: totalTokens / activeDayCount,
    approxTasks,
    approxTaskFailures,
    approxTaskSuccessRate:
      approxTasks > 0 ? Math.max(approxTasks - approxTaskFailures, 0) / approxTasks : 1,
    zeroTokenCalls: zeroTokenRows.length,
    zeroTokenModels: Array.from(new Set(zeroTokenRows.map((row) => row.model))).sort(),
  };
};

export const buildAccountRows = (rows: MonitoringEventRow[]): MonitoringAccountRow[] => {
  const grouped = new Map<
    string,
    {
      id: string;
      account: string;
      accountMasked: string;
      authLabels: Set<string>;
      authIndices: Set<string>;
      sourceKeys: Set<string>;
      apiKeyHashes: Set<string>;
      channels: Set<string>;
      modelMap: Map<
        string,
        {
          model: string;
          totalCalls: number;
          successCalls: number;
          failureCalls: number;
          inputTokens: number;
          outputTokens: number;
          cachedTokens: number;
          cacheReadTokens: number;
          cacheCreationTokens: number;
          totalTokens: number;
          totalCost: number;
          lastSeenAt: number;
        }
      >;
      rows: MonitoringEventRow[];
      totalCalls: number;
      successCalls: number;
      failureCalls: number;
      inputTokens: number;
      outputTokens: number;
      cachedTokens: number;
      cacheReadTokens: number;
      cacheCreationTokens: number;
      totalTokens: number;
      totalCost: number;
      latencySum: number;
      latencyCount: number;
      lastSeenAt: number;
    }
  >();

  rows.forEach((row) => {
    const accountKey = row.account || row.authLabel || row.source;
    const existing = grouped.get(accountKey) ?? {
      id: accountKey,
      account: row.account,
      accountMasked: row.accountMasked,
      authLabels: new Set<string>(),
      authIndices: new Set<string>(),
      sourceKeys: new Set<string>(),
      apiKeyHashes: new Set<string>(),
      channels: new Set<string>(),
      modelMap: new Map(),
      rows: [] as MonitoringEventRow[],
      totalCalls: 0,
      successCalls: 0,
      failureCalls: 0,
      inputTokens: 0,
      outputTokens: 0,
      cachedTokens: 0,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      totalTokens: 0,
      totalCost: 0,
      latencySum: 0,
      latencyCount: 0,
      lastSeenAt: 0,
    };

    existing.rows.push(row);
    existing.authLabels.add(row.authLabel);
    existing.authIndices.add(row.authIndex);
    if (row.sourceKey) {
      existing.sourceKeys.add(row.sourceKey);
    }
    existing.apiKeyHashes.add(row.apiKeyHash);
    existing.channels.add(row.channel);
    existing.totalCalls += 1;
    existing.successCalls += row.failed ? 0 : 1;
    existing.failureCalls += row.failed ? 1 : 0;
    existing.inputTokens += row.inputTokens;
    existing.outputTokens += row.outputTokens;
    existing.cachedTokens += row.cachedTokens;
    existing.cacheReadTokens += row.cacheReadTokens;
    existing.cacheCreationTokens += row.cacheCreationTokens;
    existing.totalTokens += row.totalTokens;
    existing.totalCost += row.totalCost;
    existing.lastSeenAt = Math.max(existing.lastSeenAt, row.timestampMs);

    if (row.latencyMs !== null) {
      existing.latencySum += row.latencyMs;
      existing.latencyCount += 1;
    }

    const modelEntry = existing.modelMap.get(row.model) ?? {
      model: row.model,
      totalCalls: 0,
      successCalls: 0,
      failureCalls: 0,
      inputTokens: 0,
      outputTokens: 0,
      cachedTokens: 0,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      totalTokens: 0,
      totalCost: 0,
      lastSeenAt: 0,
    };

    modelEntry.totalCalls += 1;
    modelEntry.successCalls += row.failed ? 0 : 1;
    modelEntry.failureCalls += row.failed ? 1 : 0;
    modelEntry.inputTokens += row.inputTokens;
    modelEntry.outputTokens += row.outputTokens;
    modelEntry.cachedTokens += row.cachedTokens;
    modelEntry.cacheReadTokens += row.cacheReadTokens;
    modelEntry.cacheCreationTokens += row.cacheCreationTokens;
    modelEntry.totalTokens += row.totalTokens;
    modelEntry.totalCost += row.totalCost;
    modelEntry.lastSeenAt = Math.max(modelEntry.lastSeenAt, row.timestampMs);
    existing.modelMap.set(row.model, modelEntry);

    grouped.set(accountKey, existing);
  });

  return Array.from(grouped.values())
    .map((item) => {
      const channels = Array.from(item.channels).sort();
      const authIndices = Array.from(item.authIndices).sort();
      const sourceKeys = Array.from(item.sourceKeys).sort();
      const apiKeyHashes = Array.from(item.apiKeyHashes).sort();
      return {
        id: item.id,
        account: item.account,
        filterValue:
          buildMonitoringAccountFilterValue({
            account: item.account,
            authIndices,
            apiKeyHashes,
          }) || item.account,
        displayAccount: resolveAccountDisplayName(item.account, channels),
        accountMasked: item.accountMasked,
        authLabels: Array.from(item.authLabels).sort(),
        authIndices,
        sourceKeys,
        channels,
        totalCalls: item.totalCalls,
        successCalls: item.successCalls,
        failureCalls: item.failureCalls,
        successRate: item.totalCalls > 0 ? item.successCalls / item.totalCalls : 1,
        inputTokens: item.inputTokens,
        outputTokens: item.outputTokens,
        cachedTokens: item.cachedTokens,
        cacheReadTokens: item.cacheReadTokens,
        cacheCreationTokens: item.cacheCreationTokens,
        totalTokens: item.totalTokens,
        totalCost: item.totalCost,
        averageLatencyMs: item.latencyCount > 0 ? item.latencySum / item.latencyCount : null,
        lastSeenAt: item.lastSeenAt,
        recentPattern: buildRecentPattern(item.rows),
        models: Array.from(item.modelMap.values())
          .map((model) => ({
            ...model,
            successRate: model.totalCalls > 0 ? model.successCalls / model.totalCalls : 1,
          }))
          .sort(
            (left, right) => right.totalCost - left.totalCost || right.totalCalls - left.totalCalls
          ),
      };
    })
    .sort(
      (left, right) =>
        right.lastSeenAt - left.lastSeenAt ||
        right.totalCalls - left.totalCalls ||
        right.totalCost - left.totalCost
    );
};

export const buildApiKeyRows = (
  rows: MonitoringEventRow[],
  apiKeyDisplayMap?: ReadonlyMap<string, ApiKeyDisplayInfo>
): MonitoringApiKeyRow[] => {
  const grouped = new Map<
    string,
    {
      id: string;
      apiKeyHash: string;
      apiKeyLabel: string;
      apiKeyMasked: string;
      apiKeyCopyValue?: string;
      isUnknown: boolean;
      authLabels: Set<string>;
      sourceLabels: Set<string>;
      channels: Set<string>;
      modelMap: Map<
        string,
        {
          model: string;
          totalCalls: number;
          successCalls: number;
          failureCalls: number;
          inputTokens: number;
          outputTokens: number;
          cachedTokens: number;
          cacheReadTokens: number;
          cacheCreationTokens: number;
          totalTokens: number;
          totalCost: number;
          lastSeenAt: number;
        }
      >;
      totalCalls: number;
      successCalls: number;
      failureCalls: number;
      inputTokens: number;
      outputTokens: number;
      cachedTokens: number;
      cacheReadTokens: number;
      cacheCreationTokens: number;
      totalTokens: number;
      totalCost: number;
      latencySum: number;
      latencyCount: number;
      lastSeenAt: number;
    }
  >();

  rows.forEach((row) => {
    const hasKnownApiKey = Boolean(
      row.apiKeyHash ||
      (row.apiKeyLabel && row.apiKeyLabel !== '-') ||
      (row.apiKeyMasked && row.apiKeyMasked !== '-')
    );
    const apiKeyGroupKey = hasKnownApiKey
      ? row.apiKeyHash || row.apiKeyLabel || row.apiKeyMasked
      : `${UNKNOWN_API_KEY_GROUP_PREFIX}:${row.sourceKey}:${row.authIndex || row.authLabel || '-'}:${row.channel || '-'}:${row.provider || '-'}`;
    const existing = grouped.get(apiKeyGroupKey) ?? {
      id: apiKeyGroupKey,
      apiKeyHash: row.apiKeyHash,
      apiKeyLabel: sanitizeApiKeyDisplayText(row.apiKeyLabel),
      apiKeyMasked: sanitizeApiKeyDisplayText(row.apiKeyMasked),
      apiKeyCopyValue: row.apiKeyHash
        ? apiKeyDisplayMap?.get(row.apiKeyHash.toLowerCase())?.copyValue
        : undefined,
      isUnknown: !hasKnownApiKey,
      authLabels: new Set<string>(),
      sourceLabels: new Set<string>(),
      channels: new Set<string>(),
      modelMap: new Map(),
      totalCalls: 0,
      successCalls: 0,
      failureCalls: 0,
      inputTokens: 0,
      outputTokens: 0,
      cachedTokens: 0,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      totalTokens: 0,
      totalCost: 0,
      latencySum: 0,
      latencyCount: 0,
      lastSeenAt: 0,
    };

    if (!existing.apiKeyHash && row.apiKeyHash) {
      existing.apiKeyHash = row.apiKeyHash;
    }
    if (!existing.apiKeyCopyValue && existing.apiKeyHash) {
      existing.apiKeyCopyValue = apiKeyDisplayMap?.get(
        existing.apiKeyHash.toLowerCase()
      )?.copyValue;
    }
    if (!existing.apiKeyMasked && row.apiKeyMasked) {
      existing.apiKeyMasked = sanitizeApiKeyDisplayText(row.apiKeyMasked);
    }
    if (
      shouldPreferApiKeyAlias(row.apiKeyLabel, row.apiKeyMasked) &&
      !shouldPreferApiKeyAlias(existing.apiKeyLabel, existing.apiKeyMasked)
    ) {
      existing.apiKeyLabel = sanitizeApiKeyDisplayText(row.apiKeyLabel, existing.apiKeyLabel);
    }
    existing.authLabels.add(row.authLabel);
    existing.sourceLabels.add(row.sourceMasked || row.source);
    existing.channels.add(row.channel);

    existing.totalCalls += 1;
    existing.successCalls += row.failed ? 0 : 1;
    existing.failureCalls += row.failed ? 1 : 0;
    existing.inputTokens += row.inputTokens;
    existing.outputTokens += row.outputTokens;
    existing.cachedTokens += row.cachedTokens;
    existing.cacheReadTokens += row.cacheReadTokens;
    existing.cacheCreationTokens += row.cacheCreationTokens;
    existing.totalTokens += row.totalTokens;
    existing.totalCost += row.totalCost;
    existing.lastSeenAt = Math.max(existing.lastSeenAt, row.timestampMs);

    if (row.latencyMs !== null) {
      existing.latencySum += row.latencyMs;
      existing.latencyCount += 1;
    }

    const modelEntry = existing.modelMap.get(row.model) ?? {
      model: row.model,
      totalCalls: 0,
      successCalls: 0,
      failureCalls: 0,
      inputTokens: 0,
      outputTokens: 0,
      cachedTokens: 0,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      totalTokens: 0,
      totalCost: 0,
      lastSeenAt: 0,
    };

    modelEntry.totalCalls += 1;
    modelEntry.successCalls += row.failed ? 0 : 1;
    modelEntry.failureCalls += row.failed ? 1 : 0;
    modelEntry.inputTokens += row.inputTokens;
    modelEntry.outputTokens += row.outputTokens;
    modelEntry.cachedTokens += row.cachedTokens;
    modelEntry.cacheReadTokens += row.cacheReadTokens;
    modelEntry.cacheCreationTokens += row.cacheCreationTokens;
    modelEntry.totalTokens += row.totalTokens;
    modelEntry.totalCost += row.totalCost;
    modelEntry.lastSeenAt = Math.max(modelEntry.lastSeenAt, row.timestampMs);
    existing.modelMap.set(row.model, modelEntry);

    grouped.set(apiKeyGroupKey, existing);
  });

  return Array.from(grouped.values())
    .map((item) => ({
      id: item.id,
      apiKeyHash: item.apiKeyHash,
      apiKeyLabel: item.apiKeyLabel || item.apiKeyMasked || formatApiKeyHashLabel(item.apiKeyHash),
      apiKeyMasked: item.apiKeyMasked || item.apiKeyLabel || formatApiKeyHashLabel(item.apiKeyHash),
      apiKeyCopyValue: item.apiKeyCopyValue,
      isUnknown: item.isUnknown,
      authLabels: Array.from(item.authLabels).filter(Boolean).sort(),
      sourceLabels: Array.from(item.sourceLabels).filter(Boolean).sort(),
      channels: Array.from(item.channels).filter(Boolean).sort(),
      totalCalls: item.totalCalls,
      successCalls: item.successCalls,
      failureCalls: item.failureCalls,
      successRate: item.totalCalls > 0 ? item.successCalls / item.totalCalls : 1,
      inputTokens: item.inputTokens,
      outputTokens: item.outputTokens,
      cachedTokens: item.cachedTokens,
      cacheReadTokens: item.cacheReadTokens,
      cacheCreationTokens: item.cacheCreationTokens,
      totalTokens: item.totalTokens,
      totalCost: item.totalCost,
      averageLatencyMs: item.latencyCount > 0 ? item.latencySum / item.latencyCount : null,
      lastSeenAt: item.lastSeenAt,
      models: Array.from(item.modelMap.values())
        .map((model) => ({
          ...model,
          successRate: model.totalCalls > 0 ? model.successCalls / model.totalCalls : 1,
        }))
        .sort(
          (left, right) => right.totalCost - left.totalCost || right.totalCalls - left.totalCalls
        ),
    }))
    .sort(
      (left, right) =>
        right.lastSeenAt - left.lastSeenAt ||
        right.totalCalls - left.totalCalls ||
        right.totalCost - left.totalCost
    );
};

export const buildRealtimeMonitorRows = (rows: MonitoringEventRow[]): MonitoringRealtimeRow[] => {
  const grouped = new Map<
    string,
    {
      id: string;
      account: string;
      accountMasked: string;
      authLabel: string;
      authIndexMasked: string;
      provider: string;
      requestType: string;
      model: string;
      channel: string;
      rows: MonitoringEventRow[];
      latestFailed: boolean;
      successCalls: number;
      failureCalls: number;
      inputTokens: number;
      outputTokens: number;
      cachedTokens: number;
      cacheReadTokens: number;
      cacheCreationTokens: number;
      totalTokens: number;
      totalCost: number;
      latencySum: number;
      latencyCount: number;
      latestLatencyMs: number | null;
      lastSeenAt: number;
    }
  >();

  rows.forEach((row) => {
    const requestType = `${row.endpointMethod} ${row.endpointPath}`.trim();
    const key = [
      row.account || row.authLabel || row.source,
      row.authIndexMasked,
      row.provider,
      row.model,
      row.channel,
      requestType,
    ].join('::');

    const existing = grouped.get(key) ?? {
      id: key,
      account: row.account,
      accountMasked: row.accountMasked,
      authLabel: row.authLabel,
      authIndexMasked: row.authIndexMasked,
      provider: row.provider,
      requestType,
      model: row.model,
      channel: row.channel,
      rows: [] as MonitoringEventRow[],
      latestFailed: row.failed,
      successCalls: 0,
      failureCalls: 0,
      inputTokens: 0,
      outputTokens: 0,
      cachedTokens: 0,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      totalTokens: 0,
      totalCost: 0,
      latencySum: 0,
      latencyCount: 0,
      latestLatencyMs: null,
      lastSeenAt: 0,
    };

    existing.rows.push(row);
    existing.successCalls += row.failed ? 0 : 1;
    existing.failureCalls += row.failed ? 1 : 0;
    existing.inputTokens += row.inputTokens;
    existing.outputTokens += row.outputTokens;
    existing.cachedTokens += row.cachedTokens;
    existing.cacheReadTokens += row.cacheReadTokens;
    existing.cacheCreationTokens += row.cacheCreationTokens;
    existing.totalTokens += row.totalTokens;
    existing.totalCost += row.totalCost;

    if (row.timestampMs >= existing.lastSeenAt) {
      existing.lastSeenAt = row.timestampMs;
      existing.latestFailed = row.failed;
      existing.latestLatencyMs = row.latencyMs;
    }

    if (row.latencyMs !== null) {
      existing.latencySum += row.latencyMs;
      existing.latencyCount += 1;
    }

    grouped.set(key, existing);
  });

  return Array.from(grouped.values())
    .map((item) => {
      const totalCalls = item.successCalls + item.failureCalls;
      return {
        id: item.id,
        account: item.account,
        accountMasked: item.accountMasked,
        authLabel: item.authLabel,
        authIndexMasked: item.authIndexMasked,
        provider: item.provider,
        requestType: item.requestType,
        model: item.model,
        channel: item.channel,
        latestFailed: item.latestFailed,
        successRate: totalCalls > 0 ? item.successCalls / totalCalls : 1,
        totalCalls,
        successCalls: item.successCalls,
        failureCalls: item.failureCalls,
        averageLatencyMs: item.latencyCount > 0 ? item.latencySum / item.latencyCount : null,
        latestLatencyMs: item.latestLatencyMs,
        inputTokens: item.inputTokens,
        outputTokens: item.outputTokens,
        cachedTokens: item.cachedTokens,
        cacheReadTokens: item.cacheReadTokens,
        cacheCreationTokens: item.cacheCreationTokens,
        totalTokens: item.totalTokens,
        totalCost: item.totalCost,
        lastSeenAt: item.lastSeenAt,
        recentPattern: buildRecentPattern(item.rows),
      };
    })
    .sort(
      (left, right) => right.lastSeenAt - left.lastSeenAt || right.totalCalls - left.totalCalls
    );
};
