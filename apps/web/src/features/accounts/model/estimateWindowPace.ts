import type { QuotaWindowMode } from '@/types';
import type { AccountQuotaBoundaryAccuracy } from './accountQuotaWindowDefinitions';

/**
 * How the current burn rate compares with the scheduled reset of the window.
 *
 * - `exhausts_before_reset`: the window is projected to reach 100% usage before
 *   the provider resets it, so requests will be rejected for a while.
 * - `lasts_until_reset`: the window is projected to still have quota left when
 *   the provider resets it.
 * - `exhausted`: the window has no quota left; only the reset moment matters.
 * - `no_usage`: nothing was consumed yet, so there is no rate to project.
 */
export type WindowPaceStatus =
  | 'exhausts_before_reset'
  | 'lasts_until_reset'
  | 'exhausted'
  | 'no_usage';

export interface WindowPaceEstimate {
  status: WindowPaceStatus;
  /** Wall-clock time the window is projected to reach 100% usage; null without a rate. */
  exhaustsAtMs: number | null;
  /** Scheduled reset the projection was compared against. */
  resetsAtMs: number;
  /** Percent of the window that was still available at the observation time. */
  remainingPercent: number;
}

export interface EstimateWindowPaceInput {
  usedPercent: number | null | undefined;
  windowMode?: QuotaWindowMode;
  cycleStartMs?: number | null;
  cycleEndMs?: number | null;
  resetAtMs?: number | null;
  limitWindowSeconds?: number | null;
  quotaProgressObservedAtMs?: number | null;
  observedAtMs?: number | null;
  boundaryAccuracy?: AccountQuotaBoundaryAccuracy | null;
  stale?: boolean;
  nowMs?: number;
}

/** Ignore observations that are older than this when projecting the pace. */
export const WINDOW_PACE_MAX_OBSERVATION_AGE_MS = 30 * 60 * 1000;
/** Do not extrapolate from a window that has barely started. */
export const WINDOW_PACE_MIN_ELAPSED_MS = 10 * 60 * 1000;
/** Tolerate small clock skew between the provider and the browser. */
const WINDOW_PACE_CLOCK_SKEW_MS = 60 * 1000;

const isPositiveFinite = (value: number | null | undefined): value is number =>
  typeof value === 'number' && Number.isFinite(value) && value > 0;

const firstPositive = (...values: Array<number | null | undefined>): number | null =>
  values.find(isPositiveFinite) ?? null;

const resolveCycleStartMs = (input: EstimateWindowPaceInput, resetsAtMs: number): number | null => {
  if (isPositiveFinite(input.cycleStartMs) && input.cycleStartMs < resetsAtMs) {
    return input.cycleStartMs;
  }
  // Calendar windows are anchored by the provider, so only fixed windows can
  // derive their start from the advertised window length.
  if (input.windowMode !== 'fixed' || !isPositiveFinite(input.limitWindowSeconds)) return null;
  const derivedStartMs = resetsAtMs - input.limitWindowSeconds * 1000;
  return derivedStartMs > 0 ? derivedStartMs : null;
};

/**
 * Project when a quota window will run out at its current pace and compare that
 * moment with the scheduled reset.
 *
 * The projection assumes the window started empty and has been consumed at a
 * constant rate since `cycleStartMs`; it is the same linear model the quota
 * forecast already uses, only solved for time instead of total usage. Returns
 * `null` whenever the inputs are too weak to be worth showing.
 */
export const estimateWindowPace = (input: EstimateWindowPaceInput): WindowPaceEstimate | null => {
  if (input.stale === true) return null;
  // A reset comparison only makes sense for windows with a scheduled boundary.
  if (input.windowMode !== 'fixed' && input.windowMode !== 'calendar') return null;
  // `unknown` means the provider never told us where the window starts or ends.
  if (input.boundaryAccuracy === 'unknown') return null;

  const usedPercent = input.usedPercent;
  if (typeof usedPercent !== 'number' || !Number.isFinite(usedPercent)) return null;

  const nowMs = isPositiveFinite(input.nowMs) ? input.nowMs : Date.now();
  const resetsAtMs = firstPositive(input.resetAtMs, input.cycleEndMs);
  if (resetsAtMs === null || resetsAtMs <= nowMs) return null;

  // An exhausted window has no rate left to project, but the reset moment is
  // still useful: it is how long the credential stays unavailable.
  if (usedPercent >= 100) {
    return {
      status: 'exhausted',
      exhaustsAtMs: nowMs,
      resetsAtMs,
      remainingPercent: 0,
    };
  }
  if (usedPercent <= 0) {
    return {
      status: 'no_usage',
      exhaustsAtMs: null,
      resetsAtMs,
      remainingPercent: 100,
    };
  }

  const cycleStartMs = resolveCycleStartMs(input, resetsAtMs);
  if (cycleStartMs === null) return null;

  const observedAtMs = firstPositive(input.quotaProgressObservedAtMs, input.observedAtMs) ?? nowMs;
  if (observedAtMs > nowMs + WINDOW_PACE_CLOCK_SKEW_MS) return null;
  if (nowMs - observedAtMs > WINDOW_PACE_MAX_OBSERVATION_AGE_MS) return null;

  const elapsedMs = observedAtMs - cycleStartMs;
  if (elapsedMs < WINDOW_PACE_MIN_ELAPSED_MS) return null;
  if (elapsedMs > resetsAtMs - cycleStartMs) return null;

  const remainingPercent = Math.max(0, Math.min(100, 100 - usedPercent));
  const usedPercentPerMs = usedPercent / elapsedMs;
  const exhaustsAtMs = observedAtMs + remainingPercent / usedPercentPerMs;
  if (!Number.isFinite(exhaustsAtMs)) return null;
  // The projection claims the window should already be empty while the provider
  // still reports spare quota; the pace is not representative, so stay silent.
  if (exhaustsAtMs <= nowMs) return null;

  return {
    status: exhaustsAtMs < resetsAtMs ? 'exhausts_before_reset' : 'lasts_until_reset',
    exhaustsAtMs,
    resetsAtMs,
    remainingPercent,
  };
};
