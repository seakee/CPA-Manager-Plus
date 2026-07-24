import type {
  MonitoringAnalyticsAccountStatRow,
  MonitoringAnalyticsApiKeyStatRow,
  MonitoringAnalyticsChannelShareRow,
  MonitoringAnalyticsEventRow,
  MonitoringAnalyticsFailureSourceRow,
  MonitoringAnalyticsFilterOptions,
  MonitoringAnalyticsFilters,
  MonitoringAnalyticsHourlyPoint,
  MonitoringAnalyticsModelShareRow,
  MonitoringAnalyticsModelStat,
  MonitoringAnalyticsRecentFailure,
  MonitoringAnalyticsSummary,
  MonitoringAnalyticsTaskBucketRow,
  MonitoringAnalyticsTimelinePoint,
} from '@/services/api/usageService';
import type { CredentialInfo } from '@/types/sourceInfo';
import {
  buildSourceInfoMap,
  resolveSourceDisplay,
  resolveSourceIdentityKey,
} from '@/utils/sourceResolver';
import { normalizeAuthIndex, type UsageDetailWithEndpoint } from '@/utils/usage';
import {
  formatApiKeyHashLabel,
  joinUnique,
  maskAuthIndex,
  maskEmailLike,
  readString,
} from './base';
import { sanitizeApiKeyDisplayText, type ApiKeyDisplayInfo } from './apiKeys';
import { buildDayLabel, buildHourLabel, buildLocalDayKey, padNumber } from './range';
import { buildMonitoringSourceDisplay } from './sourceDisplay';
import type {
  MonitoringAccountModelSpendRow,
  MonitoringAccountRow,
  MonitoringApiKeyRow,
  MonitoringAuthMeta,
  MonitoringChannelMeta,
  MonitoringChannelRow,
  MonitoringFailureRow,
  MonitoringFailureSourceRow,
  MonitoringFilterOptions,
  MonitoringModelRow,
  MonitoringModelShareRow,
  MonitoringScopeFilters,
  MonitoringSummary,
  MonitoringTaskBucketRow,
  MonitoringTimelinePoint,
} from './types';

const isActiveFilterValue = (value: string | null | undefined) =>
  Boolean(value && value.trim() && value !== 'all');

const shortHashLabel = (value: string) => {
  const trimmed = value.trim();
  if (!trimmed) return '-';
  return trimmed.length <= 12 ? trimmed : `${trimmed.slice(0, 6)}...${trimmed.slice(-4)}`;
};

const uniqueReadableValues = (values: Array<string | null | undefined> = []) =>
  Array.from(new Set(values.map(readString).filter((value) => value && value !== '-'))).sort();

const firstReadableValue = (...values: Array<string | null | undefined>) =>
  values.map(readString).find((value) => value && value !== '-') || '';

const buildSourceKeysFromAnalyticsIdentity = (
  authIndices: Array<string | null | undefined> | undefined,
  sources: Array<string | null | undefined> | undefined,
  sourceInfoMap: ReturnType<typeof buildSourceInfoMap>,
  authFileMap: Map<string, CredentialInfo>
) => {
  const keys = new Set<string>();
  const normalizedAuthIndices = uniqueReadableValues(authIndices);
  const normalizedSources = uniqueReadableValues(sources);

  normalizedAuthIndices.forEach((authIndex) => {
    const key = resolveSourceIdentityKey('', authIndex, sourceInfoMap, authFileMap);
    if (key) keys.add(key);
  });

  normalizedSources.forEach((source) => {
    const sourceOnlyKey = resolveSourceIdentityKey(source, '', sourceInfoMap, authFileMap);
    if (sourceOnlyKey) keys.add(sourceOnlyKey);

    normalizedAuthIndices.forEach((authIndex) => {
      const key = resolveSourceIdentityKey(source, authIndex, sourceInfoMap, authFileMap);
      if (key) keys.add(key);
    });
  });

  return Array.from(keys)
    .filter((key) => key && key !== 'source:-')
    .sort();
};

const normalizeFilterText = (value: string | null | undefined) =>
  readString(value).trim().toLowerCase();

const ACCOUNT_FILTER_PREFIXES = {
  auth: 'auth:',
  source: 'source:',
  apiKey: 'api-key:',
  account: 'account:',
} as const;

const NO_MATCH_FILTER_VALUE = '__no_matching_filter_value__';

export type MonitoringAccountFilterCriteria = {
  accounts: string[];
  authIndices: string[];
  sourceHashes: string[];
  apiKeyHashes: string[];
};

const normalizeAccountFilterValues = (values: Array<string | null | undefined> = []) =>
  uniqueReadableValues(values).filter((value) => value !== '-');

const normalizeAuthFilterValues = (values: Array<string | null | undefined> = []) =>
  Array.from(
    new Set(
      values
        .map((value) => normalizeAuthIndex(value))
        .filter((value): value is string => Boolean(value && value !== '-'))
    )
  ).sort();

const normalizeApiKeyHashValues = (values: Array<string | null | undefined> = []) =>
  normalizeAccountFilterValues(values).map((value) => value.toLowerCase());

const encodeAccountFilterValues = (values: string[]) =>
  values.map((value) => encodeURIComponent(value)).join(',');

const decodeAccountFilterValue = (value: string) => {
  try {
    return decodeURIComponent(value);
  } catch {
    return value;
  }
};

const decodeAccountFilterValues = (value: string) =>
  value
    .split(',')
    .map(decodeAccountFilterValue)
    .map(readString)
    .filter((item) => item && item !== '-');

const buildAccountFilterToken = (prefix: string, values: string[]) =>
  values.length > 0 ? `${prefix}${encodeAccountFilterValues(values)}` : '';

export const buildMonitoringAccountFilterValue = ({
  account,
  authIndices,
  sourceHashes,
  apiKeyHashes,
}: {
  account?: string | null;
  authIndices?: Array<string | null | undefined>;
  sourceHashes?: Array<string | null | undefined>;
  apiKeyHashes?: Array<string | null | undefined>;
}) => {
  const normalizedAuthIndices = normalizeAuthFilterValues(authIndices);
  if (normalizedAuthIndices.length > 0) {
    return buildAccountFilterToken(ACCOUNT_FILTER_PREFIXES.auth, normalizedAuthIndices);
  }

  const normalizedSourceHashes = normalizeAccountFilterValues(sourceHashes);
  if (normalizedSourceHashes.length > 0) {
    return buildAccountFilterToken(ACCOUNT_FILTER_PREFIXES.source, normalizedSourceHashes);
  }

  const normalizedApiKeyHashes = normalizeApiKeyHashValues(apiKeyHashes);
  if (normalizedApiKeyHashes.length > 0) {
    return buildAccountFilterToken(ACCOUNT_FILTER_PREFIXES.apiKey, normalizedApiKeyHashes);
  }

  const normalizedAccounts = normalizeAccountFilterValues([account]);
  return buildAccountFilterToken(ACCOUNT_FILTER_PREFIXES.account, normalizedAccounts);
};

export const parseMonitoringAccountFilterValue = (
  value: string | null | undefined
): MonitoringAccountFilterCriteria => {
  const text = readString(value);
  const emptyCriteria: MonitoringAccountFilterCriteria = {
    accounts: [],
    authIndices: [],
    sourceHashes: [],
    apiKeyHashes: [],
  };
  if (!text || text === 'all') return emptyCriteria;

  if (text.startsWith(ACCOUNT_FILTER_PREFIXES.auth)) {
    return {
      ...emptyCriteria,
      authIndices: normalizeAuthFilterValues(
        decodeAccountFilterValues(text.slice(ACCOUNT_FILTER_PREFIXES.auth.length))
      ),
    };
  }

  if (text.startsWith(ACCOUNT_FILTER_PREFIXES.source)) {
    return {
      ...emptyCriteria,
      sourceHashes: normalizeAccountFilterValues(
        decodeAccountFilterValues(text.slice(ACCOUNT_FILTER_PREFIXES.source.length))
      ),
    };
  }

  if (text.startsWith(ACCOUNT_FILTER_PREFIXES.apiKey)) {
    return {
      ...emptyCriteria,
      apiKeyHashes: normalizeApiKeyHashValues(
        decodeAccountFilterValues(text.slice(ACCOUNT_FILTER_PREFIXES.apiKey.length))
      ),
    };
  }

  if (text.startsWith(ACCOUNT_FILTER_PREFIXES.account)) {
    return {
      ...emptyCriteria,
      accounts: normalizeAccountFilterValues([
        decodeAccountFilterValue(text.slice(ACCOUNT_FILTER_PREFIXES.account.length)),
      ]),
    };
  }

  return {
    ...emptyCriteria,
    accounts: normalizeAccountFilterValues([text]),
  };
};

const resolveFirstAuthIndex = (authIndices: string[] | undefined) =>
  normalizeAuthIndex((authIndices || []).find((value) => readString(value))) ?? '-';

const resolveAuthMetas = (
  authIndices: string[] | undefined,
  authMetaMap: Map<string, MonitoringAuthMeta>
) =>
  uniqueReadableValues(authIndices).flatMap((authIndex) => {
    const normalized = normalizeAuthIndex(authIndex) ?? authIndex;
    const meta = authMetaMap.get(normalized);
    return meta ? [meta] : [];
  });

const buildModelSpendRowsFromAnalytics = (
  rows: MonitoringAnalyticsAccountStatRow['models'] = []
): MonitoringAccountModelSpendRow[] =>
  rows
    .map((row) => ({
      model: row.model || '-',
      totalCalls: row.calls,
      successCalls: row.success_calls,
      failureCalls: row.failure_calls,
      successRate: row.success_rate,
      inputTokens: row.input_tokens,
      outputTokens: row.output_tokens,
      cachedTokens: row.cached_tokens,
      cacheReadTokens: row.cache_read_tokens ?? 0,
      cacheCreationTokens: row.cache_creation_tokens ?? 0,
      totalTokens: row.total_tokens,
      totalCost: row.cost,
      lastSeenAt: row.last_seen_ms,
    }))
    .sort((left, right) => right.totalCost - left.totalCost || right.totalCalls - left.totalCalls);

const addAuthIndexConstraint = (
  current: Set<string> | null,
  values: Iterable<string>
): Set<string> | null => {
  const next = new Set(Array.from(values).map(normalizeAuthIndex).filter(Boolean) as string[]);
  if (next.size === 0) return current;
  if (current === null) return next;
  return new Set(Array.from(current).filter((value) => next.has(value)));
};

const addFilterValueConstraint = (
  current: string[] | undefined,
  values: Array<string | null | undefined>
) => {
  const next = normalizeAccountFilterValues(values);
  if (next.length === 0) return current;
  if (!current || current.length === 0) return next;
  const nextSet = new Set(next);
  const constrained = current.filter((value) => nextSet.has(value));
  return constrained.length > 0 ? constrained : [NO_MATCH_FILTER_VALUE];
};

export const buildAnalyticsFilters = (
  scopeFilters: MonitoringScopeFilters | undefined,
  authMetaMap: Map<string, MonitoringAuthMeta>,
  channels: MonitoringChannelMeta[]
): MonitoringAnalyticsFilters => {
  const filters: MonitoringAnalyticsFilters = {};
  if (!scopeFilters) return filters;

  if (isActiveFilterValue(scopeFilters.model)) {
    filters.models = [scopeFilters.model!.trim()];
  }
  if (isActiveFilterValue(scopeFilters.apiKeyHash)) {
    filters.api_key_hashes = [scopeFilters.apiKeyHash!.trim().toLowerCase()];
  }
  if (isActiveFilterValue(scopeFilters.authFile)) {
    filters.auth_files = [scopeFilters.authFile!.trim()];
  }
  if (isActiveFilterValue(scopeFilters.projectId)) {
    filters.project_ids = [scopeFilters.projectId!.trim()];
  }
  if (isActiveFilterValue(scopeFilters.requestType)) {
    filters.request_types = [scopeFilters.requestType!.trim()];
  }
  if (scopeFilters.status === 'success') {
    filters.include_failed = false;
  } else if (scopeFilters.status === 'failed') {
    filters.failed_only = true;
  }
  if (typeof scopeFilters.minLatencyMs === 'number' && scopeFilters.minLatencyMs > 0) {
    filters.min_latency_ms = scopeFilters.minLatencyMs;
  }
  if (isActiveFilterValue(scopeFilters.cacheStatus)) {
    filters.cache_status = scopeFilters.cacheStatus!.trim();
  }
  if (isActiveFilterValue(scopeFilters.headerTraceId)) {
    filters.header_trace_ids = [scopeFilters.headerTraceId!.trim()];
  }

  let authIndices: Set<string> | null = null;
  if (isActiveFilterValue(scopeFilters.account)) {
    const account = scopeFilters.account!.trim();
    const accountCriteria = parseMonitoringAccountFilterValue(account);

    authIndices = addAuthIndexConstraint(authIndices, accountCriteria.authIndices);
    filters.source_hashes = addFilterValueConstraint(
      filters.source_hashes,
      accountCriteria.sourceHashes
    );
    filters.api_key_hashes = addFilterValueConstraint(
      filters.api_key_hashes,
      accountCriteria.apiKeyHashes
    );

    if (
      accountCriteria.authIndices.length === 0 &&
      accountCriteria.sourceHashes.length === 0 &&
      accountCriteria.apiKeyHashes.length === 0
    ) {
      const legacyAccount = accountCriteria.accounts[0] || account;
      const normalizedAccount = normalizeFilterText(legacyAccount);
      const accountAuthIndices = Array.from(authMetaMap.entries())
        .filter(([, meta]) => normalizeFilterText(meta.account) === normalizedAccount)
        .map(([authIndex]) => authIndex);
      authIndices = addAuthIndexConstraint(authIndices, accountAuthIndices);
      if (accountAuthIndices.length === 0) {
        filters.accounts =
          accountCriteria.accounts.length > 0 ? accountCriteria.accounts : [account];
      }
    }
  }
  if (isActiveFilterValue(scopeFilters.provider)) {
    const provider = scopeFilters.provider!.trim();
    const normalizedProvider = normalizeFilterText(provider);
    const providerAuthIndices = Array.from(authMetaMap.entries())
      .filter(([, meta]) => normalizeFilterText(meta.provider) === normalizedProvider)
      .map(([authIndex]) => authIndex);
    authIndices = addAuthIndexConstraint(authIndices, providerAuthIndices);
    if (providerAuthIndices.length === 0) {
      filters.providers = [provider];
    }
  }
  if (isActiveFilterValue(scopeFilters.channel)) {
    const channel = scopeFilters.channel!.trim();
    const channelAuthIndices = channels
      .filter((item) => item.name === channel)
      .flatMap((item) => item.authIndices);
    authIndices = addAuthIndexConstraint(authIndices, channelAuthIndices);
    if (channelAuthIndices.length === 0 && !filters.providers?.includes(channel)) {
      filters.providers = [...(filters.providers || []), channel];
    }
  }
  if (authIndices) {
    filters.auth_indices =
      authIndices.size > 0 ? Array.from(authIndices).sort() : ['__no_matching_auth_index__'];
  }

  return filters;
};

export const buildSummaryFromAnalytics = (
  summary: MonitoringAnalyticsSummary
): MonitoringSummary => ({
  totalCalls: summary.total_calls,
  successCalls: summary.success_calls,
  failureCalls: summary.failure_calls,
  successRate: summary.success_rate,
  inputTokens: summary.input_tokens,
  outputTokens: summary.output_tokens,
  reasoningTokens: summary.reasoning_tokens,
  cachedTokens: summary.cached_tokens,
  cacheReadTokens: summary.cache_read_tokens ?? 0,
  cacheCreationTokens: summary.cache_creation_tokens ?? 0,
  cacheHitRate: summary.cache_hit_rate,
  totalTokens: summary.total_tokens,
  totalCost: summary.total_cost,
  averageLatencyMs: summary.average_latency_ms,
  rpm30m: summary.rpm_30m,
  tpm30m: summary.tpm_30m,
  avgDailyRequests: summary.avg_daily_requests,
  avgDailyTokens: summary.avg_daily_tokens,
  approxTasks: summary.approx_tasks,
  approxTaskFailures: summary.approx_task_failures,
  approxTaskSuccessRate: summary.approx_task_success_rate,
  zeroTokenCalls: summary.zero_token_calls,
  zeroTokenModels: summary.zero_token_models,
});

export const buildTimelineFromAnalytics = (
  points: MonitoringAnalyticsTimelinePoint[],
  granularity: 'hour' | 'day' | string
): MonitoringTimelinePoint[] =>
  points.map((point) => ({
    label:
      granularity === 'hour'
        ? buildHourLabel(point.bucket_ms)
        : buildDayLabel(buildLocalDayKey(point.bucket_ms)),
    requests: point.calls,
    tokens: point.tokens,
    cost: 0,
  }));

export const buildHourlyDistributionFromAnalytics = (
  points: MonitoringAnalyticsHourlyPoint[]
): MonitoringTimelinePoint[] => {
  const buckets = Array.from({ length: 24 }, (_, hour) => ({
    label: `${padNumber(hour)}:00`,
    requests: 0,
    tokens: 0,
    cost: 0,
  }));
  points.forEach((point) => {
    if (point.hour < 0 || point.hour > 23) return;
    buckets[point.hour] = {
      label: `${padNumber(point.hour)}:00`,
      requests: point.calls,
      tokens: point.tokens,
      cost: 0,
    };
  });
  return buckets;
};

export const buildModelShareRowsFromAnalytics = (
  rows: MonitoringAnalyticsModelShareRow[],
  modelStats: MonitoringAnalyticsModelStat[] = []
): MonitoringModelShareRow[] => {
  const successRateByModel = new Map(modelStats.map((row) => [row.model, row.success_rate]));
  return rows.map((row) => ({
    model: row.model,
    requests: row.calls,
    totalTokens: row.tokens,
    totalCost: row.cost,
    successRate: successRateByModel.get(row.model) ?? 1,
  }));
};

export const buildModelRowsFromAnalytics = (
  rows: MonitoringAnalyticsModelStat[]
): MonitoringModelRow[] =>
  rows.map((row) => ({
    model: row.model,
    requests: row.calls,
    failures: row.failure_calls,
    successRate: row.success_rate,
    totalTokens: row.total_tokens,
    totalCost: row.cost,
    averageLatencyMs: null,
    sources: 0,
    channels: 0,
  }));

const resolveChannelMeta = (
  authIndex: string,
  authMetaMap: Map<string, MonitoringAuthMeta>,
  channelByAuthIndex: Map<string, MonitoringChannelMeta>
) => {
  const authMeta = authMetaMap.get(authIndex);
  const channelMeta =
    channelByAuthIndex.get(authIndex) ||
    (authMeta?.authIndex ? channelByAuthIndex.get(authMeta.authIndex) : undefined);
  return { authMeta, channelMeta };
};

export const buildChannelRowsFromAnalytics = (
  rows: MonitoringAnalyticsChannelShareRow[],
  authMetaMap: Map<string, MonitoringAuthMeta>,
  authFileMap: Map<string, CredentialInfo>,
  sourceInfoMap: ReturnType<typeof buildSourceInfoMap>,
  channelByAuthIndex: Map<string, MonitoringChannelMeta>
): MonitoringChannelRow[] =>
  rows
    .map((row) => {
      const authIndex = row.auth_index || '-';
      const { authMeta, channelMeta } = resolveChannelMeta(
        authIndex,
        authMetaMap,
        channelByAuthIndex
      );
      const display = buildMonitoringSourceDisplay(
        {
          source: row.source,
          authIndex,
          accountSnapshot: row.account_snapshot,
          authLabelSnapshot: row.auth_label_snapshot,
          authProviderSnapshot: row.auth_provider_snapshot,
        },
        { authMetaMap, authFileMap, sourceInfoMap, channelByAuthIndex }
      );
      const label = display.primary;
      return {
        id: authIndex,
        label,
        host: display.channelHost,
        provider: display.provider,
        planTypes: authMeta?.planType && authMeta.planType !== '-' ? [authMeta.planType] : [],
        disabled: channelMeta?.disabled || authMeta?.disabled || false,
        authCount: authIndex === '-' ? 0 : 1,
        modelCount: 0,
        requests: row.calls,
        failures: row.failure,
        successRate: row.calls > 0 ? row.success / row.calls : 1,
        totalTokens: row.tokens,
        totalCost: row.cost,
        averageLatencyMs: row.average_latency_ms,
        authLabels: [authMeta?.label || row.auth_label_snapshot || display.sourceLabel].filter(
          (value): value is string => Boolean(value)
        ),
      } satisfies MonitoringChannelRow;
    })
    .sort((left, right) => right.requests - left.requests);

export const buildFailureSourceRowsFromAnalytics = (
  rows: MonitoringAnalyticsFailureSourceRow[],
  authMetaMap: Map<string, MonitoringAuthMeta>,
  authFileMap: Map<string, CredentialInfo>,
  sourceInfoMap: ReturnType<typeof buildSourceInfoMap>,
  channelByAuthIndex: Map<string, MonitoringChannelMeta>
): MonitoringFailureSourceRow[] =>
  rows.map((row) => {
    const display = buildMonitoringSourceDisplay(
      {
        source: row.source,
        sourceHash: row.source_hash,
        authIndex: row.auth_index,
        accountSnapshot: row.account_snapshot,
        authLabelSnapshot: row.auth_label_snapshot,
        authProviderSnapshot: row.auth_provider_snapshot,
      },
      { authMetaMap, authFileMap, sourceInfoMap, channelByAuthIndex }
    );
    return {
      id: `${row.source_hash || '-'}::${row.auth_index || '-'}`,
      label: display.sourceMasked || display.primary,
      channel: display.channel,
      failures: row.failure,
      totalRequests: row.calls,
      failureRate: row.calls > 0 ? row.failure / row.calls : 0,
      lastSeenAt: row.last_seen_ms,
      averageLatencyMs: row.average_latency_ms,
    };
  });

export const buildAccountRowsFromAnalytics = (
  rows: MonitoringAnalyticsAccountStatRow[],
  authMetaMap: Map<string, MonitoringAuthMeta>,
  authFileMap: Map<string, CredentialInfo>,
  sourceInfoMap: ReturnType<typeof buildSourceInfoMap>,
  channelByAuthIndex: Map<string, MonitoringChannelMeta>
): MonitoringAccountRow[] =>
  rows
    .map((row) => {
      const authIndex = resolveFirstAuthIndex(row.auth_indices);
      const authMetas = resolveAuthMetas(row.auth_indices, authMetaMap);
      const channelNames = uniqueReadableValues([
        ...((row.auth_indices || []).map((value) => {
          const normalized = normalizeAuthIndex(value) ?? value;
          return channelByAuthIndex.get(normalized)?.name;
        }) || []),
        ...authMetas.map((meta) => meta.provider),
      ]);
      const display = buildMonitoringSourceDisplay(
        {
          source: row.sources?.[0],
          sourceHash: row.source_hashes?.[0],
          authIndex,
          accountSnapshot: row.account_snapshot,
          authLabelSnapshot: row.auth_label_snapshot,
          authProviderSnapshot: row.auth_provider_snapshot,
          channel: channelNames[0],
        },
        { authMetaMap, authFileMap, sourceInfoMap, channelByAuthIndex }
      );
      const account = firstReadableValue(display.account, row.account_snapshot, row.id);
      const displayAccount = firstReadableValue(display.primary, account);
      const authLabels = uniqueReadableValues([
        ...authMetas.map((meta) => meta.label),
        row.auth_label_snapshot,
        display.sourceLabel,
      ]);
      const channels = uniqueReadableValues([...channelNames, display.channel]);
      const sourceKeys = buildSourceKeysFromAnalyticsIdentity(
        row.auth_indices,
        row.sources,
        sourceInfoMap,
        authFileMap
      );

      return {
        id: account || row.id,
        account,
        displayAccount,
        accountMasked: display.accountMasked || maskEmailLike(account),
        authLabels,
        authIndices: uniqueReadableValues(row.auth_indices),
        sourceKeys,
        channels,
        totalCalls: row.calls,
        successCalls: row.success_calls,
        failureCalls: row.failure_calls,
        successRate: row.success_rate,
        inputTokens: row.input_tokens,
        outputTokens: row.output_tokens,
        cachedTokens: row.cached_tokens,
        cacheReadTokens: row.cache_read_tokens ?? 0,
        cacheCreationTokens: row.cache_creation_tokens ?? 0,
        totalTokens: row.total_tokens,
        totalCost: row.cost,
        averageLatencyMs: row.average_latency_ms,
        lastSeenAt: row.last_seen_ms,
        recentPattern: [],
        filterValue:
          buildMonitoringAccountFilterValue({
            account,
            authIndices: row.auth_indices,
            sourceHashes: row.source_hashes,
          }) || account,
        models: buildModelSpendRowsFromAnalytics(row.models),
      };
    })
    .sort(
      (left, right) =>
        right.lastSeenAt - left.lastSeenAt ||
        right.totalCalls - left.totalCalls ||
        right.totalCost - left.totalCost
    );

export const buildApiKeyRowsFromAnalytics = (
  rows: MonitoringAnalyticsApiKeyStatRow[],
  authMetaMap: Map<string, MonitoringAuthMeta>,
  authFileMap: Map<string, CredentialInfo>,
  sourceInfoMap: ReturnType<typeof buildSourceInfoMap>,
  channelByAuthIndex: Map<string, MonitoringChannelMeta>,
  apiKeyDisplayMap: Map<string, ApiKeyDisplayInfo>
): MonitoringApiKeyRow[] =>
  rows
    .map((row) => {
      const apiKeyHash = readString(row.api_key_hash).toLowerCase();
      const authIndex = resolveFirstAuthIndex(row.auth_indices);
      const authMetas = resolveAuthMetas(row.auth_indices, authMetaMap);
      const channelNames = uniqueReadableValues([
        ...((row.auth_indices || []).map((value) => {
          const normalized = normalizeAuthIndex(value) ?? value;
          return channelByAuthIndex.get(normalized)?.name;
        }) || []),
        ...authMetas.map((meta) => meta.provider),
      ]);
      const display = buildMonitoringSourceDisplay(
        {
          source: row.sources?.[0],
          sourceHash: row.source_hashes?.[0],
          apiKeyHash,
          authIndex,
          accountSnapshot: row.account_snapshot,
          authLabelSnapshot: row.auth_label_snapshot,
          authProviderSnapshot: row.auth_provider_snapshot,
          channel: channelNames[0],
        },
        { authMetaMap, authFileMap, sourceInfoMap, channelByAuthIndex }
      );
      const apiKeyDisplay = apiKeyDisplayMap.get(apiKeyHash);
      const fallbackApiKeyLabel = formatApiKeyHashLabel(apiKeyHash);
      const apiKeyLabel = sanitizeApiKeyDisplayText(
        apiKeyDisplay?.label || fallbackApiKeyLabel,
        fallbackApiKeyLabel
      );
      const apiKeyMasked = sanitizeApiKeyDisplayText(
        apiKeyDisplay?.masked || apiKeyLabel,
        apiKeyLabel
      );
      const isUnknown = !apiKeyHash;

      return {
        id: apiKeyHash || row.id,
        apiKeyHash,
        apiKeyLabel: isUnknown ? '' : apiKeyLabel,
        apiKeyMasked: isUnknown ? '' : apiKeyMasked,
        apiKeyCopyValue: isUnknown ? undefined : apiKeyDisplay?.copyValue,
        isUnknown,
        authLabels: uniqueReadableValues([
          ...authMetas.map((meta) => meta.label),
          row.auth_label_snapshot,
          display.sourceLabel,
        ]),
        sourceLabels: uniqueReadableValues([...(row.sources || []), display.sourceMasked]),
        channels: uniqueReadableValues([...channelNames, display.channel]),
        totalCalls: row.calls,
        successCalls: row.success_calls,
        failureCalls: row.failure_calls,
        successRate: row.success_rate,
        inputTokens: row.input_tokens,
        outputTokens: row.output_tokens,
        cachedTokens: row.cached_tokens,
        cacheReadTokens: row.cache_read_tokens ?? 0,
        cacheCreationTokens: row.cache_creation_tokens ?? 0,
        totalTokens: row.total_tokens,
        totalCost: row.cost,
        averageLatencyMs: row.average_latency_ms,
        lastSeenAt: row.last_seen_ms,
        models: buildModelSpendRowsFromAnalytics(row.models),
      };
    })
    .sort(
      (left, right) =>
        right.lastSeenAt - left.lastSeenAt ||
        right.totalCalls - left.totalCalls ||
        right.totalCost - left.totalCost
    );

export const buildFilterOptionsFromAnalytics = (
  options: MonitoringAnalyticsFilterOptions | undefined,
  authMetaMap: Map<string, MonitoringAuthMeta>,
  authFileMap: Map<string, CredentialInfo>,
  sourceInfoMap: ReturnType<typeof buildSourceInfoMap>,
  channelByAuthIndex: Map<string, MonitoringChannelMeta>,
  apiKeyDisplayMap: Map<string, ApiKeyDisplayInfo>
): MonitoringFilterOptions => {
  if (!options) {
    return {
      accountRows: [],
      apiKeyRows: [],
      providers: [],
      models: [],
      channels: [],
      headerTraceIds: [],
    };
  }

  const resolveAuthIndex = (value: string | undefined) => normalizeAuthIndex(value) ?? value ?? '';
  const resolveAuthMeta = (value: string | undefined) => authMetaMap.get(resolveAuthIndex(value));
  const channelRows = options.channel_share || [];
  const accountRows = buildAccountRowsFromAnalytics(
    options.account_stats || [],
    authMetaMap,
    authFileMap,
    sourceInfoMap,
    channelByAuthIndex
  );
  const knownAccounts = new Set(accountRows.map((row) => normalizeFilterText(row.account)));
  const accountSelectorRows = uniqueReadableValues(options.accounts || [])
    .filter((account) => !knownAccounts.has(normalizeFilterText(account)))
    .map(
      (account): MonitoringAccountRow => ({
        id: `selector:${normalizeFilterText(account)}`,
        account,
        filterValue: buildMonitoringAccountFilterValue({ account }) || account,
        displayAccount: account,
        accountMasked: maskEmailLike(account),
        authLabels: [],
        authIndices: [],
        sourceKeys: [],
        channels: [],
        totalCalls: 0,
        successCalls: 0,
        failureCalls: 0,
        successRate: 1,
        inputTokens: 0,
        outputTokens: 0,
        cachedTokens: 0,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        totalTokens: 0,
        totalCost: 0,
        averageLatencyMs: null,
        lastSeenAt: 0,
        recentPattern: [],
        models: [],
      })
    );
  const apiKeyRows = buildApiKeyRowsFromAnalytics(
    options.api_key_stats || [],
    authMetaMap,
    authFileMap,
    sourceInfoMap,
    channelByAuthIndex,
    apiKeyDisplayMap
  );
  const knownApiKeyHashes = new Set(apiKeyRows.map((row) => row.apiKeyHash));
  const apiKeySelectorRows = uniqueReadableValues(options.api_key_hashes || [])
    .map((value) => value.toLowerCase())
    .filter((apiKeyHash) => !knownApiKeyHashes.has(apiKeyHash))
    .map((apiKeyHash): MonitoringApiKeyRow => {
      const apiKeyDisplay = apiKeyDisplayMap.get(apiKeyHash);
      const fallbackLabel = formatApiKeyHashLabel(apiKeyHash);
      const apiKeyLabel = sanitizeApiKeyDisplayText(
        apiKeyDisplay?.label || fallbackLabel,
        fallbackLabel
      );
      const apiKeyMasked = sanitizeApiKeyDisplayText(
        apiKeyDisplay?.masked || apiKeyLabel,
        apiKeyLabel
      );
      return {
        id: apiKeyHash,
        apiKeyHash,
        apiKeyLabel,
        apiKeyMasked,
        apiKeyCopyValue: apiKeyDisplay?.copyValue,
        isUnknown: false,
        authLabels: [],
        sourceLabels: [],
        channels: [],
        totalCalls: 0,
        successCalls: 0,
        failureCalls: 0,
        successRate: 1,
        inputTokens: 0,
        outputTokens: 0,
        cachedTokens: 0,
        cacheReadTokens: 0,
        cacheCreationTokens: 0,
        totalTokens: 0,
        totalCost: 0,
        averageLatencyMs: null,
        lastSeenAt: 0,
        models: [],
      };
    });

  return {
    accountRows: [...accountRows, ...accountSelectorRows],
    apiKeyRows: [...apiKeyRows, ...apiKeySelectorRows],
    providers: uniqueReadableValues([
      ...(options.providers || []),
      ...channelRows.map((row) => resolveAuthMeta(row.auth_index)?.provider),
      ...channelRows.map((row) => row.auth_provider_snapshot),
      ...(options.account_stats || []).map((row) => row.auth_provider_snapshot),
      ...(options.api_key_stats || []).map((row) => row.auth_provider_snapshot),
    ]),
    models: uniqueReadableValues([
      ...(options.models || []),
      ...(options.model_stats || []).map((row) => row.model),
    ]),
    channels: uniqueReadableValues([
      ...channelRows.map((row) => {
        const authIndex = resolveAuthIndex(row.auth_index);
        return (
          channelByAuthIndex.get(authIndex)?.name ||
          resolveAuthMeta(row.auth_index)?.provider ||
          row.auth_provider_snapshot
        );
      }),
      ...Array.from(channelByAuthIndex.values()).map((channel) => channel.name),
      ...(options.providers || []),
    ]),
    headerTraceIds: uniqueReadableValues(options.header_trace_ids || []),
  };
};

export const buildTaskBucketsFromAnalytics = (
  rows: MonitoringAnalyticsTaskBucketRow[],
  authMetaMap: Map<string, MonitoringAuthMeta>,
  authFileMap: Map<string, CredentialInfo>,
  sourceInfoMap: ReturnType<typeof buildSourceInfoMap>,
  channelByAuthIndex: Map<string, MonitoringChannelMeta>
): MonitoringTaskBucketRow[] =>
  rows.map((row) => {
    const authIndex = normalizeAuthIndex(row.auth_index) ?? '-';
    const authMeta = authMetaMap.get(authIndex);
    const sourceMeta = resolveSourceDisplay(row.source, authIndex, sourceInfoMap, authFileMap);
    const { channelMeta } = resolveChannelMeta(authIndex, authMetaMap, channelByAuthIndex);
    const sourceLabel =
      authMeta?.label || sourceMeta.displayName || shortHashLabel(row.source_hash);
    return {
      id: row.bucket_key,
      timestampMs: row.first_ms,
      timestamp: new Date(row.first_ms).toISOString(),
      source: sourceLabel,
      sourceMasked: maskEmailLike(sourceLabel),
      channel: channelMeta?.name || authMeta?.provider || sourceMeta.type || '-',
      authLabel: authMeta?.label || sourceLabel,
      planType: authMeta?.planType || '-',
      calls: row.total,
      failedCalls: row.failure,
      failed: row.failure > 0,
      modelsText: joinUnique(row.models, 3),
      totalTokens: row.total_tokens,
      cachedTokens: row.cached_tokens,
      cacheReadTokens: row.cache_read_tokens ?? 0,
      cacheCreationTokens: row.cache_creation_tokens ?? 0,
      totalCost: 0,
      averageLatencyMs: row.average_latency_ms,
      maxLatencyMs: row.max_latency_ms,
      endpointsText: joinUnique(row.endpoints, 2),
    };
  });

export const buildFailureRowsFromAnalytics = (
  rows: MonitoringAnalyticsRecentFailure[],
  authMetaMap: Map<string, MonitoringAuthMeta>,
  authFileMap: Map<string, CredentialInfo>,
  sourceInfoMap: ReturnType<typeof buildSourceInfoMap>,
  channelByAuthIndex: Map<string, MonitoringChannelMeta>
): MonitoringFailureRow[] =>
  rows.map((row) => {
    const authIndex = normalizeAuthIndex(row.auth_index) ?? '-';
    const display = buildMonitoringSourceDisplay(
      {
        source: row.source,
        sourceHash: row.source_hash,
        apiKeyHash: row.api_key_hash,
        authIndex,
        accountSnapshot: row.account_snapshot,
        authLabelSnapshot: row.auth_label_snapshot,
        authProviderSnapshot: row.auth_provider_snapshot,
      },
      { authMetaMap, authFileMap, sourceInfoMap, channelByAuthIndex }
    );
    return {
      id: `${row.timestamp_ms}-${row.source_hash}-${row.api_key_hash}-${row.model}`,
      timestampMs: row.timestamp_ms,
      timestamp: new Date(row.timestamp_ms).toISOString(),
      model: row.model,
      source: display.sourceMasked || display.primary,
      channel: display.channel,
      authIndex: maskAuthIndex(authIndex),
      latencyMs: row.duration_ms,
    };
  });

const buildAnalyticsEventKey = (item: MonitoringAnalyticsEventRow) =>
  item.event_hash ||
  [
    item.timestamp_ms,
    item.model,
    item.source_hash,
    item.api_key_hash,
    item.auth_index,
    item.endpoint,
  ].join(':');

export const mergeAnalyticsEventItems = (
  previous: MonitoringAnalyticsEventRow[],
  next: MonitoringAnalyticsEventRow[]
) => {
  if (previous.length === 0) return next;
  const seen = new Set(previous.map(buildAnalyticsEventKey));
  const merged = previous.slice();
  next.forEach((item) => {
    const key = buildAnalyticsEventKey(item);
    if (seen.has(key)) return;
    seen.add(key);
    merged.push(item);
  });
  return merged;
};

export const buildUsageDetailsFromAnalyticsEvents = (
  items: MonitoringAnalyticsEventRow[] = []
): UsageDetailWithEndpoint[] =>
  items.map((item) => ({
    timestamp: new Date(item.timestamp_ms).toISOString(),
    source: readString(item.source),
    auth_index: item.auth_index || null,
    api_key_hash: readString(item.api_key_hash),
    account_snapshot: readString(item.account_snapshot),
    auth_label_snapshot: readString(item.auth_label_snapshot),
    auth_file_snapshot: readString(item.auth_file_snapshot),
    auth_provider_snapshot: readString(item.auth_provider_snapshot),
    auth_project_id_snapshot: readString(item.auth_project_id_snapshot),
    reasoning_effort: readString(item.reasoning_effort),
    service_tier: readString(item.service_tier),
    executor_type: readString(item.executor_type),
    latency_ms: item.latency_ms ?? undefined,
    ttft_ms: item.ttft_ms ?? undefined,
    tokens: {
      input_tokens: item.input_tokens,
      output_tokens: item.output_tokens,
      reasoning_tokens: item.reasoning_tokens,
      cached_tokens: item.cached_tokens,
      cache_read_tokens: item.cache_read_tokens ?? 0,
      cache_creation_tokens: item.cache_creation_tokens ?? 0,
      total_tokens: item.total_tokens,
    },
    failed: item.failed === true,
    fail_status_code: item.fail_status_code ?? null,
    fail_summary: readString(item.fail_summary),
    response_metadata: item.response_metadata,
    header_quota_recover_at_ms: item.header_quota_recover_at_ms ?? null,
    header_quota_used_percent: item.header_quota_used_percent ?? null,
    header_quota_plan_type: readString(item.header_quota_plan_type),
    header_error_kind: readString(item.header_error_kind),
    header_error_code: readString(item.header_error_code),
    header_trace_id: readString(item.header_trace_id),
    __modelName: item.model,
    __resolvedModel: readString(item.resolved_model),
    __endpoint: item.endpoint || `${item.method} ${item.path}`.trim(),
    __endpointMethod: item.method,
    __endpointPath: item.path,
    __timestampMs: item.timestamp_ms,
  }));
