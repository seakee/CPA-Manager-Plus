import { fullWebLanguage } from '../i18n/fullweb';
import { keyWorkspaceText as k } from '../i18n/freshKeyWorkspace';

// Real per-key usage as read from the Fresh canonical identity projection.
// Every number is paired with the coverage that bounds it: a zero is only
// shown when Manager can prove the window was fully observed.
export const usageStates = ['complete', 'partial', 'unknown', 'unavailable'] as const;
export type UsageState = (typeof usageStates)[number];

export interface UsageCoverageState {
  state: UsageState;
  reason: string;
}

export interface UsageCoverage extends UsageCoverageState {
  collectingSinceMs: number;
  lastProcessedEventId: number;
  targetEventId: number;
}

export interface UsageTotals {
  calls: number;
  success: number;
  failure: number;
  inputTokens: number;
  outputTokens: number;
  totalTokens: number;
  lastUsedAtMs: number | null;
}

export interface UsagePoint {
  bucketMs: number;
  calls: number;
  failure: number;
  totalTokens: number;
}

export interface KeyUsage {
  keyId: string;
  fromMs: number;
  toMs: number;
  bucketMs: number;
  coverage: UsageCoverage;
  totals: UsageTotals | null;
  series: UsagePoint[];
}

export interface UsageSummaryItem {
  keyId: string;
  calls: number;
  lastUsedAtMs: number | null;
  coverage: UsageCoverageState;
}

export interface UsageSummary {
  fromMs: number;
  toMs: number;
  coverage: UsageCoverageState;
  items: UsageSummaryItem[];
}

const isCount = (value: unknown): value is number =>
  typeof value === 'number' && Number.isSafeInteger(value) && value >= 0;

const isMillis = (value: unknown): value is number =>
  typeof value === 'number' && Number.isSafeInteger(value) && value > 0;

const isOptionalMillis = (value: unknown): value is number | null =>
  value === null || isMillis(value);

const isState = (value: unknown): value is UsageState =>
  typeof value === 'string' && (usageStates as readonly string[]).includes(value);

// Reads one field of a value already narrowed to an object. The result stays
// unknown and every caller validates it before use.
function field(source: object, name: string): unknown {
  return Object.prototype.hasOwnProperty.call(source, name) ? Reflect.get(source, name) : undefined;
}

function parseCoverageState(value: unknown): UsageCoverageState | null {
  if (!value || typeof value !== 'object') return null;
  const state = field(value, 'state');
  const reason = field(value, 'reason');
  if (!isState(state) || typeof reason !== 'string') return null;
  return { state, reason };
}

// Malformed usage is never repaired into a plausible number: the caller shows
// an unknown coverage instead.
export function parseKeyUsage(value: unknown, keyId: string): KeyUsage | null {
  if (!value || typeof value !== 'object') return null;
  const rawCoverage = field(value, 'coverage');
  const coverage = parseCoverageState(rawCoverage);
  const collectingSinceMs = rawCoverage && typeof rawCoverage === 'object' ? field(rawCoverage, 'collectingSinceMs') : undefined;
  const lastProcessedEventId = rawCoverage && typeof rawCoverage === 'object' ? field(rawCoverage, 'lastProcessedEventId') : undefined;
  const targetEventId = rawCoverage && typeof rawCoverage === 'object' ? field(rawCoverage, 'targetEventId') : undefined;
  const fromMs = field(value, 'fromMs');
  const toMs = field(value, 'toMs');
  const bucketMs = field(value, 'bucketMs');
  if (
    field(value, 'keyId') !== keyId ||
    !isMillis(fromMs) ||
    !isMillis(toMs) ||
    toMs <= fromMs ||
    !isMillis(bucketMs) ||
    !coverage ||
    !isCount(collectingSinceMs) ||
    !isCount(lastProcessedEventId) ||
    !isCount(targetEventId)
  )
    return null;
  const totals = field(value, 'totals');
  let parsedTotals: UsageTotals | null = null;
  if (totals !== null) {
    if (!totals || typeof totals !== 'object') return null;
    const calls = field(totals, 'calls');
    const success = field(totals, 'success');
    const failure = field(totals, 'failure') ?? field(totals, 'failedCalls');
    const inputTokens = field(totals, 'inputTokens');
    const outputTokens = field(totals, 'outputTokens');
    const totalTokens = field(totals, 'totalTokens');
    const lastUsedAtMs = field(totals, 'lastUsedAtMs');
    if (
      !isCount(calls) ||
      !isCount(success) ||
      !isCount(failure) ||
      !isCount(inputTokens) ||
      !isCount(outputTokens) ||
      !isCount(totalTokens) ||
      !isOptionalMillis(lastUsedAtMs) ||
      success + failure !== calls
    )
      return null;
    parsedTotals = { calls, success, failure, inputTokens, outputTokens, totalTokens, lastUsedAtMs };
  }
  const rawSeries = field(value, 'series');
  if (!Array.isArray(rawSeries)) return null;
  const series: UsagePoint[] = [];
  for (const point of rawSeries) {
    if (!point || typeof point !== 'object') return null;
    const bucket = field(point, 'bucketMs');
    const calls = field(point, 'calls');
    const failure = field(point, 'failure');
    const totalTokens = field(point, 'totalTokens');
    if (!isMillis(bucket) || !isCount(calls) || !isCount(failure) || !isCount(totalTokens) || failure > calls)
      return null;
    series.push({ bucketMs: bucket, calls, failure, totalTokens });
  }
  return {
    keyId,
    fromMs,
    toMs,
    bucketMs,
    coverage: {
      state: coverage.state,
      reason: coverage.reason,
      collectingSinceMs,
      lastProcessedEventId,
      targetEventId,
    },
    totals: parsedTotals,
    series,
  };
}

export function parseUsageSummary(value: unknown): UsageSummary | null {
  if (!value || typeof value !== 'object') return null;
  const coverage = parseCoverageState(field(value, 'coverage'));
  const fromMs = field(value, 'fromMs');
  const toMs = field(value, 'toMs');
  const rawItems = field(value, 'items');
  if (!isMillis(fromMs) || !isMillis(toMs) || !coverage || !Array.isArray(rawItems)) return null;
  const items: UsageSummaryItem[] = [];
  for (const item of rawItems) {
    if (!item || typeof item !== 'object') return null;
    const keyId = field(item, 'keyId');
    const calls = field(item, 'calls');
    const lastUsedAtMs = field(item, 'lastUsedAtMs');
    const itemCoverage = parseCoverageState(field(item, 'coverage'));
    if (
      typeof keyId !== 'string' ||
      !/^[a-f0-9]{32}$/.test(keyId) ||
      !isCount(calls) ||
      !isOptionalMillis(lastUsedAtMs) ||
      !itemCoverage
    )
      return null;
    items.push({ keyId, calls, lastUsedAtMs, coverage: itemCoverage });
  }
  return { fromMs, toMs, coverage, items };
}

// The pill colour of a coverage state; only complete is a proven window.
export function usageCoveragePill(state: UsageState | undefined): string {
  if (state === 'complete') return 'is-saved';
  if (state === 'partial') return 'is-pending';
  return 'is-muted';
}

export function usageStateText(state: UsageState): string {
  switch (state) {
    case 'complete':
      return k('usageStateComplete');
    case 'partial':
      return k('usageStatePartial');
    case 'unknown':
      return k('usageStateUnknown');
    default:
      return k('usageStateUnavailable');
  }
}

export function usageReasonText(reason: string): string {
  switch (reason) {
    case 'observed':
      return k('usageReasonObserved');
    case 'binding_missing':
      return k('usageReasonBindingMissing');
    case 'binding_conflict':
      return k('usageReasonBindingConflict');
    case 'collection_unproven':
      return k('usageReasonCollectionUnproven');
    case 'collection_stopped':
      return k('usageReasonCollectionStopped');
    case 'collection_error':
      return k('usageReasonCollectionError');
    case 'collection_starting':
      return k('usageReasonCollectionStarting');
    case 'projection_unavailable':
      return k('usageReasonProjectionUnavailable');
    case 'projection_catching_up':
      return k('usageReasonProjectionCatchingUp');
    case 'projection_failed':
      return k('usageReasonProjectionFailed');
    case 'identity_unmapped':
      return k('usageReasonIdentityUnmapped');
    case 'raw_deleted':
      return k('usageReasonRawDeleted');
    case 'window_partial':
      return k('usageReasonWindowPartial');
    case 'window_unobserved':
      return k('usageReasonWindowUnobserved');
    case 'upgrade_incomplete':
      return k('usageReasonUpgradeIncomplete');
    case 'usage_read_failed':
      return k('usageReadFailed');
    default:
      return k('usageReasonUnknown');
  }
}

// A count is only printed as an exact number when the window is complete.
// Partial windows are lower bounds; unproven windows print nothing.
export function usageCountText(value: number | null | undefined, state: UsageState): string {
  if (state === 'unknown' || state === 'unavailable' || value === null || value === undefined)
    return '—';
  if (state === 'partial') return value === 0 ? '—' : `≥${value}`;
  return String(value);
}

// Timestamps in the workspace follow the selected UI language, like the detail
// metadata beside them.
export function usageTimeText(value: number | null | undefined): string | null {
  if (value === null || value === undefined) return null;
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return null;
  return date.toLocaleString(fullWebLanguage(), {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  });
}
