export type StatusBlockState = 'success' | 'failure' | 'mixed' | 'idle';

export interface StatusBlockDetail {
  success: number;
  failure: number;
  rate: number;
  startTime: number;
  endTime: number;
}

export interface StatusBarData {
  blocks: StatusBlockState[];
  blockDetails: StatusBlockDetail[];
  successRate: number;
  totalSuccess: number;
  totalFailure: number;
}

export interface RecentRequestBucket {
  time?: string;
  bucketStartMs?: number;
  success: number;
  failed: number;
}

export interface RecentRequestUsageEntry {
  success: number;
  failed: number;
  recentRequests: RecentRequestBucket[];
}

export type ApiKeyUsageResponse = Record<
  string,
  Record<
    string,
    {
      success?: unknown;
      failed?: unknown;
      recent_requests?: unknown;
      recentRequests?: unknown;
    }
  >
>;

const RECENT_REQUEST_BLOCK_COUNT = 20;
export const RECENT_REQUEST_BUCKET_DURATION_MS = 10 * 60 * 1000;
const RECENT_REQUEST_BLOCK_DURATION_MS = RECENT_REQUEST_BUCKET_DURATION_MS;

const toFiniteNumber = (value: unknown): number => {
  const numberValue = typeof value === 'number' ? value : Number(value);
  return Number.isFinite(numberValue) ? numberValue : 0;
};

export function normalizeUsageTotal(value: unknown): number {
  if (typeof value === 'number') {
    return Number.isFinite(value) ? value : 0;
  }
  if (typeof value === 'string') {
    const trimmed = value.trim();
    if (!trimmed) {
      return 0;
    }
    const numberValue = Number(trimmed);
    return Number.isFinite(numberValue) ? numberValue : 0;
  }
  return 0;
}

export function buildRecentRequestCompositeKey(baseUrl: unknown, apiKey: unknown): string {
  const normalizedBaseUrl = String(baseUrl ?? '').trim();
  const normalizedApiKey = String(apiKey ?? '').trim();
  return `${normalizedBaseUrl}|${normalizedApiKey}`;
}

export function normalizeRecentRequestAuthIndex(value: unknown): string | null {
  if (typeof value === 'number' && Number.isFinite(value)) {
    return value.toString();
  }
  if (typeof value === 'string') {
    const trimmed = value.trim();
    return trimmed ? trimmed : null;
  }
  return null;
}

export function normalizeRecentRequestBuckets(input: unknown): RecentRequestBucket[] {
  if (!Array.isArray(input)) {
    return [];
  }

  return input.slice(-RECENT_REQUEST_BLOCK_COUNT).map((item) => {
    const record = item && typeof item === 'object' ? (item as Record<string, unknown>) : {};
    const time = typeof record.time === 'string' ? record.time : undefined;

    const rawStartMs = record.bucketStartMs ?? record.bucket_start_ms;
    const bucketStartMs =
      typeof rawStartMs === 'number' && Number.isFinite(rawStartMs) && rawStartMs > 0
        ? rawStartMs
        : undefined;
    return {
      ...(time ? { time } : {}),
      ...(bucketStartMs !== undefined ? { bucketStartMs } : {}),
      success: toFiniteNumber(record.success),
      failed: toFiniteNumber(record.failed),
    };
  });
}

export function normalizeRecentRequestUsageEntry(input: unknown): RecentRequestUsageEntry {
  if (!input || typeof input !== 'object' || Array.isArray(input)) {
    return {
      success: 0,
      failed: 0,
      recentRequests: [],
    };
  }

  const record = input as Record<string, unknown>;

  return {
    success: normalizeUsageTotal(record.success),
    failed: normalizeUsageTotal(record.failed),
    recentRequests: normalizeRecentRequestBuckets(record.recent_requests ?? record.recentRequests),
  };
}

const snapBucketStartMs = (timestampMs: number): number =>
  Math.floor(timestampMs / RECENT_REQUEST_BLOCK_DURATION_MS) * RECENT_REQUEST_BLOCK_DURATION_MS;

const parseBucketLabelStartMinute = (label?: string): number | null => {
  if (!label) return null;
  const match = /^(\d{2}):(\d{2})-/.exec(label.trim());
  if (!match) return null;
  const minute = Number(match[2]);
  return Number.isInteger(minute) && minute >= 0 && minute < 60 ? minute : null;
};

/**
 * Anchors a sequence of contiguous 10-minute buckets onto absolute UTC bucket
 * start timestamps (`bucketStartMs`). Buckets that already carry `bucketStartMs`
 * are snapped to 10-minute boundaries; label-only CPA buckets are anchored to
 * `referenceNowMs` with ±1-bucket boundary-skew alignment when the local clock
 * minute matches the label's start minute.
 */
export function anchorRecentRequestBuckets(
  buckets: RecentRequestBucket[],
  referenceNowMs = Date.now()
): RecentRequestBucket[] {
  const normalized = normalizeRecentRequestBuckets(buckets);
  if (normalized.length === 0) return [];

  if (normalized.every((bucket) => bucket.bucketStartMs !== undefined)) {
    return normalized.map((bucket) => ({
      ...bucket,
      bucketStartMs: snapBucketStartMs(bucket.bucketStartMs as number),
    }));
  }

  const baseLastStartMs = snapBucketStartMs(referenceNowMs);
  const lastLabelMinute = parseBucketLabelStartMinute(normalized[normalized.length - 1]?.time);
  let resolvedLastStartMs = baseLastStartMs;
  if (lastLabelMinute !== null) {
    for (const offset of [0, -1, 1]) {
      const candidateStartMs = baseLastStartMs + offset * RECENT_REQUEST_BLOCK_DURATION_MS;
      if (new Date(candidateStartMs).getMinutes() === lastLabelMinute) {
        resolvedLastStartMs = candidateStartMs;
        break;
      }
    }
  }

  return normalized.map((bucket, index) => ({
    ...bucket,
    bucketStartMs:
      bucket.bucketStartMs !== undefined
        ? snapBucketStartMs(bucket.bucketStartMs)
        : resolvedLastStartMs - (normalized.length - 1 - index) * RECENT_REQUEST_BLOCK_DURATION_MS,
  }));
}

export function mergeRecentRequestBucketGroups(
  groups: RecentRequestBucket[][]
): RecentRequestBucket[] {
  const normalizedGroups = groups
    .map((group) => normalizeRecentRequestBuckets(group))
    .filter((group) => group.length > 0);

  if (normalizedGroups.length === 0) {
    return [];
  }

  const allTimestamped = normalizedGroups.every((group) =>
    group.every((bucket) => bucket.bucketStartMs !== undefined)
  );
  if (allTimestamped) {
    const byStart = new Map<number, RecentRequestBucket>();
    normalizedGroups.forEach((group) => {
      group.forEach((bucket) => {
        const startMs = snapBucketStartMs(bucket.bucketStartMs as number);
        const existing = byStart.get(startMs);
        if (existing) {
          existing.success += bucket.success;
          existing.failed += bucket.failed;
          if (!existing.time && bucket.time) {
            existing.time = bucket.time;
          }
          return;
        }
        byStart.set(startMs, {
          ...(bucket.time ? { time: bucket.time } : {}),
          bucketStartMs: startMs,
          success: bucket.success,
          failed: bucket.failed,
        });
      });
    });
    const sortedStarts = Array.from(byStart.keys()).sort((a, b) => a - b);
    const tailStarts = sortedStarts.slice(-RECENT_REQUEST_BLOCK_COUNT);
    return tailStarts.map((startMs) => byStart.get(startMs)!);
  }

  const mergedLength = Math.min(
    RECENT_REQUEST_BLOCK_COUNT,
    Math.max(...normalizedGroups.map((group) => group.length))
  );
  const merged: RecentRequestBucket[] = Array.from({ length: mergedLength }, () => ({
    success: 0,
    failed: 0,
  }));

  normalizedGroups.forEach((group) => {
    const tail = group.slice(-mergedLength);
    const offset = mergedLength - tail.length;

    tail.forEach((bucket, index) => {
      const target = merged[offset + index];
      target.success += bucket.success;
      target.failed += bucket.failed;
      if (!target.time && bucket.time) {
        target.time = bucket.time;
      }
      if (target.bucketStartMs === undefined && bucket.bucketStartMs !== undefined) {
        target.bucketStartMs = bucket.bucketStartMs;
      }
    });
  });

  return merged;
}

export function sumRecentRequests(
  buckets: RecentRequestBucket[]
): { success: number; failure: number } {
  return normalizeRecentRequestBuckets(buckets).reduce(
    (total, bucket) => ({
      success: total.success + bucket.success,
      failure: total.failure + bucket.failed,
    }),
    { success: 0, failure: 0 }
  );
}

export function statusBarDataFromRecentRequests(buckets: RecentRequestBucket[]): StatusBarData {
  const normalizedBuckets = normalizeRecentRequestBuckets(buckets);
  const emptyBucketCount = Math.max(0, RECENT_REQUEST_BLOCK_COUNT - normalizedBuckets.length);
  const blockStats = [
    ...Array.from(
      { length: emptyBucketCount },
      (): RecentRequestBucket => ({ success: 0, failed: 0 })
    ),
    ...normalizedBuckets.slice(-RECENT_REQUEST_BLOCK_COUNT),
  ];

  const now = Date.now();
  const windowStart = now - RECENT_REQUEST_BLOCK_COUNT * RECENT_REQUEST_BLOCK_DURATION_MS;

  const blocks: StatusBlockState[] = [];
  const blockDetails: StatusBarData['blockDetails'] = [];
  let totalSuccess = 0;
  let totalFailure = 0;

  blockStats.forEach((bucket, index) => {
    const success = bucket.success;
    const failure = bucket.failed;
    const total = success + failure;

    totalSuccess += success;
    totalFailure += failure;

    if (total === 0) {
      blocks.push('idle');
    } else if (failure === 0) {
      blocks.push('success');
    } else if (success === 0) {
      blocks.push('failure');
    } else {
      blocks.push('mixed');
    }

    const blockStartTime =
      bucket.bucketStartMs ?? windowStart + index * RECENT_REQUEST_BLOCK_DURATION_MS;
    blockDetails.push({
      success,
      failure,
      rate: total > 0 ? success / total : -1,
      startTime: blockStartTime,
      endTime: blockStartTime + RECENT_REQUEST_BLOCK_DURATION_MS,
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

export function compressStatusBarData(
  statusData: StatusBarData,
  targetBuckets = 10
): StatusBarData {
  const details = statusData.blockDetails;
  if (targetBuckets <= 0 || details.length <= targetBuckets) {
    return statusData;
  }

  const blocks: StatusBlockState[] = [];
  const blockDetails: StatusBarData['blockDetails'] = [];

  for (let idx = 0; idx < targetBuckets; idx += 1) {
    const start = Math.floor((idx * details.length) / targetBuckets);
    const end = Math.floor(((idx + 1) * details.length) / targetBuckets);
    const group = details.slice(start, Math.max(start + 1, end));
    const success = group.reduce((sum, item) => sum + item.success, 0);
    const failure = group.reduce((sum, item) => sum + item.failure, 0);
    const total = success + failure;

    if (total === 0) {
      blocks.push('idle');
    } else if (failure === 0) {
      blocks.push('success');
    } else if (success === 0) {
      blocks.push('failure');
    } else {
      blocks.push('mixed');
    }

    blockDetails.push({
      success,
      failure,
      rate: total > 0 ? success / total : -1,
      startTime: group[0]?.startTime ?? 0,
      endTime: group[group.length - 1]?.endTime ?? 0,
    });
  }

  return {
    blocks,
    blockDetails,
    successRate: statusData.successRate,
    totalSuccess: statusData.totalSuccess,
    totalFailure: statusData.totalFailure,
  };
}
