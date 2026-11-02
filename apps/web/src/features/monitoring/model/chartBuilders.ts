import { joinUnique } from './base';
import {
  buildDayLabel,
  buildHourLabel,
  buildLocalDayKey,
  formatHourOffset,
  getRangeBounds,
  HOUR_MS,
  padNumber,
  shouldUseHourlyTimeline,
} from './range';
import type {
  MonitoringChannelRow,
  MonitoringCustomTimeRange,
  MonitoringEventRow,
  MonitoringFailureRow,
  MonitoringFailureSourceRow,
  MonitoringMetadata,
  MonitoringModelRow,
  MonitoringModelShareRow,
  MonitoringStatusChip,
  MonitoringTaskBucketRow,
  MonitoringTimeRange,
  MonitoringTimelinePoint,
} from './types';

export const buildTimeline = (
  rows: MonitoringEventRow[],
  timeRange: MonitoringTimeRange,
  customTimeRange?: MonitoringCustomTimeRange | null,
  nowMs = Date.now()
): { granularity: 'hour' | 'day'; points: MonitoringTimelinePoint[] } => {
  if (timeRange === '24h') {
    const bounds = getRangeBounds(timeRange, nowMs)!;
    const points = Array.from({ length: 25 }, (_, index) => {
      const timestamp = bounds.startMs + index * HOUR_MS;
      return { label: buildHourLabel(timestamp), requests: 0, tokens: 0, cost: 0 };
    });
    // Timestamp indices keep both occurrences of a repeated local hour.
    rows.forEach((row) => {
      const index = Math.floor((row.timestampMs - bounds.startMs) / HOUR_MS);
      const bucket = points[index];
      if (!bucket) return;
      bucket.requests += 1;
      bucket.tokens += row.totalTokens;
      bucket.cost += row.totalCost;
    });
    const labels = points.map((point) => point.label);
    points.forEach((point, index) => {
      if (labels.indexOf(point.label) !== labels.lastIndexOf(point.label)) {
        const timestamp = bounds.startMs + index * HOUR_MS;
        point.label = `${buildDayLabel(buildLocalDayKey(timestamp))} ${point.label} (UTC${formatHourOffset(timestamp)})`;
      }
    });
    return { granularity: 'hour', points };
  }
  if (shouldUseHourlyTimeline(timeRange, customTimeRange)) {
    const map = new Map<string, MonitoringTimelinePoint>();

    for (let hour = 0; hour < 24; hour += 1) {
      const label = `${padNumber(hour)}:00`;
      map.set(label, { label, requests: 0, tokens: 0, cost: 0 });
    }

    rows.forEach((row) => {
      const bucket = map.get(row.hourLabel);
      if (!bucket) return;
      bucket.requests += 1;
      bucket.tokens += row.totalTokens;
      bucket.cost += row.totalCost;
    });

    return { granularity: 'hour', points: Array.from(map.values()) };
  }

  const grouped = new Map<string, MonitoringTimelinePoint>();

  rows.forEach((row) => {
    const existing = grouped.get(row.dayKey) ?? {
      label: buildDayLabel(row.dayKey),
      requests: 0,
      tokens: 0,
      cost: 0,
    };
    existing.requests += 1;
    existing.tokens += row.totalTokens;
    existing.cost += row.totalCost;
    grouped.set(row.dayKey, existing);
  });

  const sortedKeys = Array.from(grouped.keys()).sort((left, right) => left.localeCompare(right));
  const limitedKeys =
    sortedKeys.length > 30 ? sortedKeys.slice(sortedKeys.length - 30) : sortedKeys;

  return {
    granularity: 'day',
    points: limitedKeys.map((key) => grouped.get(key)!).filter(Boolean),
  };
};

export const buildHourlyDistribution = (rows: MonitoringEventRow[]) => {
  const buckets = Array.from({ length: 24 }, (_, hour) => ({
    label: `${padNumber(hour)}:00`,
    requests: 0,
    tokens: 0,
    cost: 0,
  }));

  rows.forEach((row) => {
    const hour = Number(row.hourLabel.slice(0, 2));
    const bucket = Number.isFinite(hour) ? buckets[hour] : null;
    if (!bucket) return;
    bucket.requests += 1;
    bucket.tokens += row.totalTokens;
    bucket.cost += row.totalCost;
  });

  return buckets;
};

export const buildStatusChips = (metadata: MonitoringMetadata): MonitoringStatusChip[] => [
  {
    key: 'credentials',
    label: 'credentials',
    value: `${metadata.activeAuthFiles}/${metadata.totalAuthFiles}`,
    tone:
      metadata.totalAuthFiles === 0 ? 'warn' : metadata.unavailableAuthFiles > 0 ? 'warn' : 'good',
  },
  {
    key: 'channels',
    label: 'channels',
    value: `${metadata.enabledChannels}/${metadata.totalChannels}`,
    tone:
      metadata.enabledChannels === 0
        ? 'bad'
        : metadata.enabledChannels < metadata.totalChannels
          ? 'warn'
          : 'good',
  },
  {
    key: 'runtime_only',
    label: 'runtime_only',
    value: String(metadata.runtimeOnlyAuthFiles),
    tone: metadata.runtimeOnlyAuthFiles > 0 ? 'warn' : 'good',
  },
  {
    key: 'models',
    label: 'models',
    value: String(metadata.configuredModels),
    tone: metadata.configuredModels > 0 ? 'good' : 'warn',
  },
];

export const buildModelShareRows = (rows: MonitoringEventRow[]): MonitoringModelShareRow[] => {
  const grouped = new Map<
    string,
    { model: string; requests: number; failures: number; totalTokens: number; totalCost: number }
  >();

  rows.forEach((row) => {
    const existing = grouped.get(row.model) ?? {
      model: row.model,
      requests: 0,
      failures: 0,
      totalTokens: 0,
      totalCost: 0,
    };
    existing.requests += 1;
    existing.failures += row.failed ? 1 : 0;
    existing.totalTokens += row.totalTokens;
    existing.totalCost += row.totalCost;
    grouped.set(row.model, existing);
  });

  return Array.from(grouped.values())
    .map((item) => ({
      model: item.model,
      requests: item.requests,
      totalTokens: item.totalTokens,
      totalCost: item.totalCost,
      successRate: item.requests > 0 ? (item.requests - item.failures) / item.requests : 1,
    }))
    .sort((left, right) => right.requests - left.requests);
};

export const buildChannelRows = (rows: MonitoringEventRow[]): MonitoringChannelRow[] => {
  const grouped = new Map<
    string,
    {
      id: string;
      label: string;
      host: string;
      provider: string;
      disabled: boolean;
      authLabels: Set<string>;
      planTypes: Set<string>;
      models: Set<string>;
      requests: number;
      failures: number;
      totalTokens: number;
      totalCost: number;
      latencySum: number;
      latencyCount: number;
    }
  >();

  rows.forEach((row) => {
    const key = `${row.channel}::${row.channelHost}`;
    const existing = grouped.get(key) ?? {
      id: key,
      label: row.channel,
      host: row.channelHost,
      provider: row.provider,
      disabled: row.channelDisabled,
      authLabels: new Set<string>(),
      planTypes: new Set<string>(),
      models: new Set<string>(),
      requests: 0,
      failures: 0,
      totalTokens: 0,
      totalCost: 0,
      latencySum: 0,
      latencyCount: 0,
    };
    existing.disabled = existing.disabled || row.channelDisabled;
    existing.authLabels.add(row.authLabel);
    if (row.planType && row.planType !== '-') {
      existing.planTypes.add(row.planType);
    }
    existing.models.add(row.model);
    existing.requests += 1;
    existing.failures += row.failed ? 1 : 0;
    existing.totalTokens += row.totalTokens;
    existing.totalCost += row.totalCost;
    if (row.latencyMs !== null) {
      existing.latencySum += row.latencyMs;
      existing.latencyCount += 1;
    }
    grouped.set(key, existing);
  });

  return Array.from(grouped.values())
    .map((item) => ({
      id: item.id,
      label: item.label,
      host: item.host,
      provider: item.provider,
      planTypes: Array.from(item.planTypes).sort(),
      disabled: item.disabled,
      authCount: item.authLabels.size,
      modelCount: item.models.size,
      requests: item.requests,
      failures: item.failures,
      successRate: item.requests > 0 ? (item.requests - item.failures) / item.requests : 1,
      totalTokens: item.totalTokens,
      totalCost: item.totalCost,
      averageLatencyMs: item.latencyCount > 0 ? item.latencySum / item.latencyCount : null,
      authLabels: Array.from(item.authLabels).sort(),
    }))
    .sort((left, right) => right.requests - left.requests);
};

export const buildModelRows = (rows: MonitoringEventRow[]): MonitoringModelRow[] => {
  const grouped = new Map<
    string,
    {
      model: string;
      requests: number;
      failures: number;
      totalTokens: number;
      totalCost: number;
      latencySum: number;
      latencyCount: number;
      sources: Set<string>;
      channels: Set<string>;
    }
  >();

  rows.forEach((row) => {
    const existing = grouped.get(row.model) ?? {
      model: row.model,
      requests: 0,
      failures: 0,
      totalTokens: 0,
      totalCost: 0,
      latencySum: 0,
      latencyCount: 0,
      sources: new Set<string>(),
      channels: new Set<string>(),
    };

    existing.requests += 1;
    existing.failures += row.failed ? 1 : 0;
    existing.totalTokens += row.totalTokens;
    existing.totalCost += row.totalCost;
    existing.sources.add(row.source);
    existing.channels.add(row.channel);
    if (row.latencyMs !== null) {
      existing.latencySum += row.latencyMs;
      existing.latencyCount += 1;
    }

    grouped.set(row.model, existing);
  });

  return Array.from(grouped.values())
    .map((item) => ({
      model: item.model,
      requests: item.requests,
      failures: item.failures,
      successRate: item.requests > 0 ? (item.requests - item.failures) / item.requests : 1,
      totalTokens: item.totalTokens,
      totalCost: item.totalCost,
      averageLatencyMs: item.latencyCount > 0 ? item.latencySum / item.latencyCount : null,
      sources: item.sources.size,
      channels: item.channels.size,
    }))
    .sort((left, right) => right.requests - left.requests);
};

export const buildFailureSourceRows = (
  rows: MonitoringEventRow[]
): MonitoringFailureSourceRow[] => {
  const grouped = new Map<
    string,
    {
      id: string;
      label: string;
      channel: string;
      failures: number;
      totalRequests: number;
      lastSeenAt: number;
      latencySum: number;
      latencyCount: number;
    }
  >();

  rows.forEach((row) => {
    const key = `${row.source}::${row.channel}`;
    const existing = grouped.get(key) ?? {
      id: key,
      label: row.sourceMasked,
      channel: row.channel,
      failures: 0,
      totalRequests: 0,
      lastSeenAt: 0,
      latencySum: 0,
      latencyCount: 0,
    };

    existing.totalRequests += 1;
    existing.lastSeenAt = Math.max(existing.lastSeenAt, row.timestampMs);
    if (row.failed) {
      existing.failures += 1;
      if (row.latencyMs !== null) {
        existing.latencySum += row.latencyMs;
        existing.latencyCount += 1;
      }
    }

    grouped.set(key, existing);
  });

  return Array.from(grouped.values())
    .filter((item) => item.failures > 0)
    .map((item) => ({
      id: item.id,
      label: item.label,
      channel: item.channel,
      failures: item.failures,
      totalRequests: item.totalRequests,
      failureRate: item.totalRequests > 0 ? item.failures / item.totalRequests : 0,
      lastSeenAt: item.lastSeenAt,
      averageLatencyMs: item.latencyCount > 0 ? item.latencySum / item.latencyCount : null,
    }))
    .sort((left, right) => right.failures - left.failures || right.lastSeenAt - left.lastSeenAt);
};

export const buildTaskBuckets = (rows: MonitoringEventRow[]): MonitoringTaskBucketRow[] => {
  const grouped = new Map<
    string,
    {
      id: string;
      timestampMs: number;
      timestamp: string;
      source: string;
      sourceMasked: string;
      channel: string;
      authLabel: string;
      planType: string;
      calls: number;
      failedCalls: number;
      models: Set<string>;
      endpoints: Set<string>;
      totalTokens: number;
      cachedTokens: number;
      cacheReadTokens: number;
      cacheCreationTokens: number;
      totalCost: number;
      latencySum: number;
      latencyCount: number;
      maxLatencyMs: number | null;
    }
  >();

  rows.forEach((row) => {
    const existing = grouped.get(row.taskKey) ?? {
      id: row.taskKey,
      timestampMs: row.timestampMs,
      timestamp: row.timestamp,
      source: row.source,
      sourceMasked: row.sourceMasked,
      channel: row.channel,
      authLabel: row.authLabel,
      planType: row.planType,
      calls: 0,
      failedCalls: 0,
      models: new Set<string>(),
      endpoints: new Set<string>(),
      totalTokens: 0,
      cachedTokens: 0,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      totalCost: 0,
      latencySum: 0,
      latencyCount: 0,
      maxLatencyMs: null,
    };

    existing.calls += 1;
    existing.failedCalls += row.failed ? 1 : 0;
    existing.models.add(row.model);
    existing.endpoints.add(row.endpointPath || row.endpoint);
    existing.totalTokens += row.totalTokens;
    existing.cachedTokens += row.cachedTokens;
    existing.cacheReadTokens += row.cacheReadTokens;
    existing.cacheCreationTokens += row.cacheCreationTokens;
    existing.totalCost += row.totalCost;
    if (row.latencyMs !== null) {
      existing.latencySum += row.latencyMs;
      existing.latencyCount += 1;
      existing.maxLatencyMs = Math.max(existing.maxLatencyMs ?? 0, row.latencyMs);
    }

    grouped.set(row.taskKey, existing);
  });

  return Array.from(grouped.values())
    .map((item) => ({
      id: item.id,
      timestampMs: item.timestampMs,
      timestamp: item.timestamp,
      source: item.source,
      sourceMasked: item.sourceMasked,
      channel: item.channel,
      authLabel: item.authLabel,
      planType: item.planType,
      calls: item.calls,
      failedCalls: item.failedCalls,
      failed: item.failedCalls > 0,
      modelsText: joinUnique(item.models, 3),
      totalTokens: item.totalTokens,
      cachedTokens: item.cachedTokens,
      cacheReadTokens: item.cacheReadTokens,
      cacheCreationTokens: item.cacheCreationTokens,
      totalCost: item.totalCost,
      averageLatencyMs: item.latencyCount > 0 ? item.latencySum / item.latencyCount : null,
      maxLatencyMs: item.maxLatencyMs,
      endpointsText: joinUnique(item.endpoints, 2),
    }))
    .sort((left, right) => right.timestampMs - left.timestampMs);
};

export const buildFailureRows = (rows: MonitoringEventRow[]): MonitoringFailureRow[] =>
  rows
    .filter((row) => row.failed)
    .map((row) => ({
      id: row.id,
      timestampMs: row.timestampMs,
      timestamp: row.timestamp,
      model: row.model,
      source: row.sourceMasked,
      channel: row.channel,
      authIndex: row.authIndexMasked,
      latencyMs: row.latencyMs,
    }))
    .sort((left, right) => right.timestampMs - left.timestampMs)
    .slice(0, 8);
