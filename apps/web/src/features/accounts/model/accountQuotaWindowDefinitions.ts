import type { QuotaModelScope, QuotaObservationSource, QuotaWindowMode } from '@/types';
import type {
  AccountQuotaDisplayWindow,
  AccountQuotaWindowKind,
  AccountQuotaWindowSource,
} from './accountQuotaDisplayWindows';

export type AccountQuotaBoundaryAccuracy = 'exact' | 'derived' | 'estimated' | 'unknown';
export type AccountQuotaUsagePeriod = 'current' | 'previous' | 'previous_equal_range';

export interface AccountQuotaCycleDefinition {
  id: number;
  activationId: number;
  state: string;
  scheduledStartMs: number | null;
  scheduledEndMs: number | null;
  actualStartMs: number;
  actualEndMs: number | null;
  durationSeconds: number | null;
  boundaryAccuracy: AccountQuotaBoundaryAccuracy;
  endReason: string;
  parentCycleId: number | null;
  forecastEligible: boolean;
}

export interface AccountQuotaWindowDefinition {
  key: string;
  providerWindowId: string;
  provider: AccountQuotaWindowSource;
  label: string;
  kind: AccountQuotaWindowKind;
  windowMode: QuotaWindowMode;
  modelScope: QuotaModelScope;
  providerWindowAliases?: string[];
  observationSource: QuotaObservationSource;
  observedAtMs: number | null;
  quotaProgressObservedAtMs: number | null;
  boundaryAccuracy: AccountQuotaBoundaryAccuracy;
  cycleStartMs: number | null;
  cycleEndMs: number | null;
  durationSeconds: number | null;
  remainingPercent: number | null;
  usedPercent: number | null;
  stale: boolean;
  logicalWindowId?: number;
  activationGeneration?: number;
  availability?: string;
  relationshipKind?: string;
  containerProviderWindowId?: string;
  firstSeenAtMs?: number;
  lastSeenAtMs?: number;
  missingSinceMs?: number | null;
  deactivatedAtMs?: number | null;
  currentCycle?: AccountQuotaCycleDefinition | null;
  previousCycle?: AccountQuotaCycleDefinition | null;
  display: AccountQuotaDisplayWindow;
}

export interface AccountQuotaUsageRange {
  period: AccountQuotaUsagePeriod;
  fromMs: number;
  toMs: number;
}

const isReliableBoundary = (accuracy: AccountQuotaBoundaryAccuracy) =>
  accuracy === 'exact' || accuracy === 'derived';

const hasLifecycleEvidence = (definition: AccountQuotaWindowDefinition): boolean =>
  definition.availability !== undefined ||
  definition.currentCycle !== undefined ||
  definition.previousCycle !== undefined;

const isValidUsageRange = (range: AccountQuotaUsageRange): boolean =>
  Number.isFinite(range.fromMs) &&
  Number.isFinite(range.toMs) &&
  range.fromMs > 0 &&
  range.fromMs < range.toMs;

const resolveBoundaryAccuracy = (
  window: AccountQuotaDisplayWindow
): AccountQuotaBoundaryAccuracy => {
  if (window.resetAccuracy === 'exact') return 'exact';
  if (
    window.resetAccuracy === 'estimated' &&
    window.limitWindowSeconds !== null &&
    window.cycleStartMs !== null &&
    window.cycleEndMs !== null &&
    window.observedAtMs !== null
  ) {
    return 'derived';
  }
  return window.resetAccuracy;
};

const definitionSortRank = (definition: AccountQuotaWindowDefinition): number => {
  if (definition.windowMode === 'non_window' || definition.windowMode === 'unknown') {
    return Number.MAX_SAFE_INTEGER;
  }
  return definition.durationSeconds ?? Number.MAX_SAFE_INTEGER - 1;
};

export const buildAccountQuotaWindowDefinitions = (
  windows: AccountQuotaDisplayWindow[],
  nowMs = Date.now()
): AccountQuotaWindowDefinition[] =>
  windows
    .map((window): AccountQuotaWindowDefinition => {
      const boundaryAccuracy = resolveBoundaryAccuracy(window);
      const stale =
        (window.windowMode === 'fixed' || window.windowMode === 'calendar') &&
        typeof window.cycleEndMs === 'number' &&
        window.cycleEndMs <= nowMs;
      return {
        key: window.key,
        providerWindowId: window.key,
        provider: window.source ?? 'summary',
        label: window.label,
        kind: window.kind ?? 'unknown',
        windowMode: window.windowMode ?? 'unknown',
        modelScope: window.modelScope ?? { kind: 'all', complete: false },
        providerWindowAliases: window.providerWindowAliases,
        observationSource: window.observationSource ?? 'api_query',
        observedAtMs: window.observedAtMs ?? null,
        quotaProgressObservedAtMs: window.quotaProgressObservedAtMs ?? null,
        boundaryAccuracy,
        cycleStartMs: window.cycleStartMs ?? null,
        cycleEndMs: window.cycleEndMs ?? null,
        durationSeconds: window.limitWindowSeconds,
        remainingPercent: window.remainingPercent,
        usedPercent: window.usedPercent,
        stale,
        display: window,
      };
    })
    .sort((left, right) => {
      const rank = definitionSortRank(left) - definitionSortRank(right);
      if (rank !== 0) return rank;
      return left.providerWindowId.localeCompare(right.providerWindowId);
    });

export const buildAccountQuotaUsageRanges = (
  definition: AccountQuotaWindowDefinition,
  nowMs = Date.now()
): AccountQuotaUsageRange[] => {
  const durationSeconds = definition.durationSeconds ?? 0;
  const durationMs = durationSeconds * 1000;
  if (
    !Number.isFinite(durationSeconds) ||
    !Number.isFinite(durationMs) ||
    !Number.isFinite(durationMs * 2) ||
    durationMs <= 0
  ) {
    return [];
  }

  if (definition.windowMode === 'rolling') {
    const ranges: AccountQuotaUsageRange[] = [
      { period: 'current', fromMs: nowMs - durationMs, toMs: nowMs },
      {
        period: 'previous_equal_range',
        fromMs: nowMs - 2 * durationMs,
        toMs: nowMs - durationMs,
      },
    ];
    return ranges.filter((range) => range.fromMs > 0 && range.fromMs < range.toMs);
  }

  if (definition.windowMode !== 'fixed' && definition.windowMode !== 'calendar') {
    return [];
  }

  if (hasLifecycleEvidence(definition)) {
    const ranges: AccountQuotaUsageRange[] = [];
    const currentCycle = definition.currentCycle;
    const currentEndMs =
      currentCycle?.scheduledEndMs ??
      (isReliableBoundary(definition.boundaryAccuracy) ? definition.cycleEndMs : null);
    if (
      currentCycle &&
      !definition.stale &&
      (definition.availability === undefined || definition.availability === 'active') &&
      currentCycle.state === 'active' &&
      currentCycle.actualEndMs === null &&
      isReliableBoundary(currentCycle.boundaryAccuracy) &&
      Number.isFinite(currentCycle.actualStartMs) &&
      typeof currentEndMs === 'number' &&
      Number.isFinite(currentEndMs)
    ) {
      const currentEnd = Math.min(nowMs, currentEndMs);
      ranges.push({
        period: 'current',
        fromMs: currentCycle.actualStartMs,
        toMs: currentEnd,
      });
    }

    const previousCycle = definition.previousCycle;
    if (
      previousCycle &&
      isReliableBoundary(previousCycle.boundaryAccuracy) &&
      Number.isFinite(previousCycle.actualStartMs) &&
      typeof previousCycle.actualEndMs === 'number' &&
      Number.isFinite(previousCycle.actualEndMs) &&
      previousCycle.actualStartMs < previousCycle.actualEndMs
    ) {
      ranges.push({
        period: 'previous',
        fromMs: previousCycle.actualStartMs,
        toMs: previousCycle.actualEndMs,
      });
    }

    return ranges.filter(isValidUsageRange);
  }

  if (
    !isReliableBoundary(definition.boundaryAccuracy) ||
    definition.stale ||
    definition.cycleStartMs === null ||
    definition.cycleEndMs === null
  ) {
    return [];
  }

  const currentEnd = Math.min(nowMs, definition.cycleEndMs);
  const ranges: AccountQuotaUsageRange[] = [];
  if (definition.cycleStartMs < currentEnd) {
    ranges.push({
      period: 'current',
      fromMs: definition.cycleStartMs,
      toMs: currentEnd,
    });
  }
  ranges.push({
    period: 'previous',
    fromMs: definition.cycleStartMs - durationMs,
    toMs: definition.cycleStartMs,
  });
  return ranges.filter(isValidUsageRange);
};
