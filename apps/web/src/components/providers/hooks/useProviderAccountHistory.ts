import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useInterval } from '@/hooks/useInterval';
import { usePanelFeatureAvailability } from '@/hooks/usePanelFeatureAvailability';
import {
  monitoringAnalyticsApi,
  type MonitoringAccountHistoryItem,
  type MonitoringAccountHistoryTarget,
  type MonitoringAccountLatestRequest,
} from '@/services/api/usageService';
import { useAuthStore } from '@/stores';
import type { GeminiKeyConfig, OpenAIProviderConfig, ProviderKeyConfig } from '@/types';
import type { StatusBarData, StatusBlockState } from '@/utils/recentRequests';
import type { ProviderKind } from '../ProviderTable/rowData';
import { getOpenAIProviderKey, getProviderConfigKey } from '../utils';

const PROVIDER_ACCOUNT_HISTORY_STALE_TIME_MS = 240_000;
const ACCOUNT_HISTORY_BATCH_SIZE = 100;
const HISTORY_STATUS_BLOCK_COUNT = 10;

export interface ProviderHistoryStats {
  matched: boolean;
  totalRequests: number;
  successCalls: number;
  failureCalls: number;
  totalTokens: number;
  totalCost: number | null;
  successRate: number | null;
  lastSeenMs: number | null;
  recentRequests: MonitoringAccountLatestRequest[];
  recentStatusData: StatusBarData;
}

export type ProviderHistoryByRowKey = Map<string, ProviderHistoryStats>;

const EMPTY_PROVIDER_HISTORY_MAP: ProviderHistoryByRowKey = new Map();

const PROVIDER_SNAPSHOT_BY_KIND: Record<ProviderKind, string> = {
  gemini: 'gemini',
  interactions: 'gemini-interactions',
  codex: 'codex',
  xai: 'xai',
  claude: 'claude',
  vertex: 'vertex',
  openai: 'openai-compatibility',
};

export interface ProviderHistoryTargetEntry {
  providerRowKey: string;
  target: MonitoringAccountHistoryTarget;
}

export interface ProviderHistoryListsInput {
  gemini: GeminiKeyConfig[];
  interactions?: GeminiKeyConfig[];
  codex: ProviderKeyConfig[];
  xai?: ProviderKeyConfig[];
  claude: ProviderKeyConfig[];
  vertex: ProviderKeyConfig[];
  openai: OpenAIProviderConfig[];
}

export function buildProviderHistoryStatusBarData(
  recentRequests: readonly MonitoringAccountLatestRequest[]
): StatusBarData {
  const ordered = [...recentRequests]
    .filter((item) => Number.isFinite(item.timestamp_ms))
    .sort((a, b) => a.timestamp_ms - b.timestamp_ms)
    .slice(-HISTORY_STATUS_BLOCK_COUNT);

  const emptySlotCount = Math.max(0, HISTORY_STATUS_BLOCK_COUNT - ordered.length);
  const blocks: StatusBlockState[] = [];
  const blockDetails: StatusBarData['blockDetails'] = [];

  for (let i = 0; i < emptySlotCount; i += 1) {
    blocks.push('idle');
    blockDetails.push({
      success: 0,
      failure: 0,
      rate: -1,
      startTime: 0,
      endTime: 0,
    });
  }

  let totalSuccess = 0;
  let totalFailure = 0;

  ordered.forEach((req) => {
    const success = req.failed ? 0 : 1;
    const failure = req.failed ? 1 : 0;
    totalSuccess += success;
    totalFailure += failure;
    blocks.push(req.failed ? 'failure' : 'success');
    blockDetails.push({
      success,
      failure,
      rate: req.failed ? 0 : 1,
      startTime: req.timestamp_ms,
      endTime: req.timestamp_ms,
    });
  });

  const total = totalSuccess + totalFailure;
  return {
    blocks,
    blockDetails,
    successRate: total > 0 ? (totalSuccess / total) * 100 : 100,
    totalSuccess,
    totalFailure,
  };
}

export function buildProviderHistoryTargets(
  lists: ProviderHistoryListsInput
): ProviderHistoryTargetEntry[] {
  const entries: ProviderHistoryTargetEntry[] = [];
  const seenTargets = new Set<string>();

  const pushTarget = (
    providerRowKey: string,
    authIndex: unknown,
    authProviderSnapshot: string
  ) => {
    const normalizedAuthIndex = String(authIndex ?? '').trim();
    if (!normalizedAuthIndex) return;
    const dedupeKey = `${providerRowKey}::${authProviderSnapshot}::${normalizedAuthIndex.toLowerCase()}`;
    if (seenTargets.has(dedupeKey)) return;
    seenTargets.add(dedupeKey);
    entries.push({
      providerRowKey,
      target: {
        row_key: dedupeKey,
        auth_index: normalizedAuthIndex,
        auth_provider_snapshot: authProviderSnapshot,
      },
    });
  };

  const singleKinds: Array<{
    kind: Exclude<ProviderKind, 'openai'>;
    items: Array<GeminiKeyConfig | ProviderKeyConfig>;
  }> = [
    { kind: 'gemini', items: lists.gemini },
    { kind: 'interactions', items: lists.interactions ?? [] },
    { kind: 'codex', items: lists.codex },
    { kind: 'xai', items: lists.xai ?? [] },
    { kind: 'claude', items: lists.claude },
    { kind: 'vertex', items: lists.vertex },
  ];

  singleKinds.forEach(({ kind, items }) => {
    const providerSnapshot = PROVIDER_SNAPSHOT_BY_KIND[kind];
    items.forEach((config, index) => {
      const rowKey = `${kind}:${getProviderConfigKey(config, index)}`;
      pushTarget(rowKey, config.authIndex, providerSnapshot);
    });
  });

  lists.openai.forEach((provider, index) => {
    const rowKey = `openai:${getOpenAIProviderKey(provider, index)}`;
    const providerSnapshot = PROVIDER_SNAPSHOT_BY_KIND.openai;
    pushTarget(rowKey, provider.authIndex, providerSnapshot);
    (provider.apiKeyEntries ?? []).forEach((entry) => {
      pushTarget(rowKey, entry.authIndex, providerSnapshot);
    });
  });

  return entries;
}

export function aggregateProviderHistoryItems(
  entries: readonly ProviderHistoryTargetEntry[],
  items: readonly MonitoringAccountHistoryItem[]
): ProviderHistoryByRowKey {
  if (entries.length === 0 || items.length === 0) {
    return EMPTY_PROVIDER_HISTORY_MAP;
  }

  const rowKeyByTargetRowKey = new Map<string, string>();
  entries.forEach((entry) => {
    const targetRowKey = String(entry.target.row_key ?? '').trim();
    if (targetRowKey) {
      rowKeyByTargetRowKey.set(targetRowKey, entry.providerRowKey);
    }
  });

  type MutableAccumulator = {
    matched: boolean;
    totalRequests: number;
    successCalls: number;
    failureCalls: number;
    totalTokens: number;
    totalCost: number;
    hasCost: boolean;
    lastSeenMs: number | null;
    recentRequests: MonitoringAccountLatestRequest[];
  };

  const accByRowKey = new Map<string, MutableAccumulator>();

  items.forEach((item) => {
    const targetRowKey = String(item.row_key ?? '').trim();
    const providerRowKey = rowKeyByTargetRowKey.get(targetRowKey);
    if (!providerRowKey) return;

    const current = accByRowKey.get(providerRowKey) ?? {
      matched: false,
      totalRequests: 0,
      successCalls: 0,
      failureCalls: 0,
      totalTokens: 0,
      totalCost: 0,
      hasCost: false,
      lastSeenMs: null,
      recentRequests: [],
    };

    if (item.matched) {
      current.matched = true;
    }
    current.totalRequests += item.total_requests ?? 0;
    current.successCalls += item.success_calls ?? 0;
    current.failureCalls += item.failure_calls ?? 0;
    current.totalTokens += item.total_tokens ?? 0;
    if (typeof item.total_cost === 'number' && Number.isFinite(item.total_cost)) {
      current.totalCost += item.total_cost;
      current.hasCost = true;
    }
    if (
      typeof item.last_seen_ms === 'number' &&
      Number.isFinite(item.last_seen_ms) &&
      (current.lastSeenMs === null || item.last_seen_ms > current.lastSeenMs)
    ) {
      current.lastSeenMs = item.last_seen_ms;
    }
    if (Array.isArray(item.recent_requests) && item.recent_requests.length > 0) {
      current.recentRequests.push(...item.recent_requests);
    } else if (item.latest_request) {
      current.recentRequests.push(item.latest_request);
    }
    accByRowKey.set(providerRowKey, current);
  });

  const result: ProviderHistoryByRowKey = new Map();
  accByRowKey.forEach((acc, rowKey) => {
    const dedupedRecent = new Map<string, MonitoringAccountLatestRequest>();
    acc.recentRequests.forEach((req) => {
      const key = `${req.timestamp_ms}:${req.failed ? 1 : 0}:${req.fail_status_code ?? ''}:${req.header_trace_id ?? ''}`;
      dedupedRecent.set(key, req);
    });
    const sortedRecent = Array.from(dedupedRecent.values())
      .sort((a, b) => b.timestamp_ms - a.timestamp_ms)
      .slice(0, HISTORY_STATUS_BLOCK_COUNT);

    const resolvedTotal = acc.totalRequests;
    const resolvedSuccessRate =
      resolvedTotal > 0 ? (acc.successCalls / resolvedTotal) * 100 : null;

    result.set(rowKey, {
      matched: acc.matched || resolvedTotal > 0 || sortedRecent.length > 0,
      totalRequests: resolvedTotal,
      successCalls: acc.successCalls,
      failureCalls: acc.failureCalls,
      totalTokens: acc.totalTokens,
      totalCost: acc.hasCost ? acc.totalCost : null,
      successRate: resolvedSuccessRate,
      lastSeenMs: acc.lastSeenMs,
      recentRequests: sortedRecent,
      recentStatusData: buildProviderHistoryStatusBarData(sortedRecent),
    });
  });

  return result;
}

export interface UseProviderAccountHistoryOptions extends ProviderHistoryListsInput {
  enabled?: boolean;
}

export function useProviderAccountHistory(options: UseProviderAccountHistoryOptions) {
  const enabled = options.enabled ?? true;
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const managementKey = useAuthStore((state) => state.managementKey);
  const { managerServiceAvailable, managerServiceBase } = usePanelFeatureAvailability();

  const targets = useMemo(
    () =>
      buildProviderHistoryTargets({
        gemini: options.gemini,
        interactions: options.interactions,
        codex: options.codex,
        xai: options.xai,
        claude: options.claude,
        vertex: options.vertex,
        openai: options.openai,
      }),
    [
      options.claude,
      options.codex,
      options.gemini,
      options.interactions,
      options.openai,
      options.vertex,
      options.xai,
    ]
  );

  const targetsSignature = useMemo(
    () =>
      targets
        .map(
          (entry) =>
            `${entry.providerRowKey}:${entry.target.auth_provider_snapshot}:${entry.target.auth_index}`
        )
        .join('|'),
    [targets]
  );

  const [historyByRowKey, setHistoryByRowKey] = useState<ProviderHistoryByRowKey>(
    EMPTY_PROVIDER_HISTORY_MAP
  );
  const abortControllerRef = useRef<AbortController | null>(null);

  const loadAccountHistory = useCallback(async () => {
    if (
      !enabled ||
      connectionStatus !== 'connected' ||
      !managerServiceAvailable ||
      !managerServiceBase ||
      !managementKey ||
      targets.length === 0
    ) {
      setHistoryByRowKey((current) => (current.size === 0 ? current : EMPTY_PROVIDER_HISTORY_MAP));
      return EMPTY_PROVIDER_HISTORY_MAP;
    }

    abortControllerRef.current?.abort();
    const controller = new AbortController();
    abortControllerRef.current = controller;

    try {
      const batches: ProviderHistoryTargetEntry[][] = [];
      for (let i = 0; i < targets.length; i += ACCOUNT_HISTORY_BATCH_SIZE) {
        batches.push(targets.slice(i, i + ACCOUNT_HISTORY_BATCH_SIZE));
      }

      const collectedItems: MonitoringAccountHistoryItem[] = [];
      for (const batch of batches) {
        if (controller.signal.aborted) return EMPTY_PROVIDER_HISTORY_MAP;
        const response = await monitoringAnalyticsApi.getAccountHistory(
          managerServiceBase,
          managementKey,
          {
            accounts: batch.map((entry) => entry.target),
          },
          controller.signal
        );
        if (Array.isArray(response?.items)) {
          collectedItems.push(...response.items);
        }
      }

      if (controller.signal.aborted) return EMPTY_PROVIDER_HISTORY_MAP;
      const aggregated = aggregateProviderHistoryItems(targets, collectedItems);
      setHistoryByRowKey(aggregated);
      return aggregated;
    } catch {
      return EMPTY_PROVIDER_HISTORY_MAP;
    } finally {
      if (abortControllerRef.current === controller) {
        abortControllerRef.current = null;
      }
    }
  }, [
    connectionStatus,
    enabled,
    managementKey,
    managerServiceAvailable,
    managerServiceBase,
    targets,
  ]);

  useEffect(() => {
    void loadAccountHistory();
    return () => {
      abortControllerRef.current?.abort();
    };
  }, [loadAccountHistory, targetsSignature]);

  useInterval(
    () => {
      void loadAccountHistory();
    },
    enabled && managerServiceAvailable && targets.length > 0
      ? PROVIDER_ACCOUNT_HISTORY_STALE_TIME_MS
      : null
  );

  return {
    historyByRowKey: enabled ? historyByRowKey : EMPTY_PROVIDER_HISTORY_MAP,
    refreshAccountHistory: loadAccountHistory,
  };
}
