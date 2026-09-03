import type { AuthFileItem, CodexQuotaState, CodexQuotaWindow } from '@/types';
import type { CodexInspectionQuotaWindow } from '@/services/api/usageService';
import { resolveQuotaDisplayState } from '@/components/quota';
import { buildQuotaCredentialIdentity } from '@/utils/quota/credentialScope';
import {
  canonicalizeCodexProviderWindowId,
  inferCodexQuotaScopeFromProviderWindowId,
} from '@/utils/quota/codexQuota';
import {
  hasActiveCodexInspectionAuthenticationFailure,
  isObservedCodexAuthenticationError,
  sanitizeSupersededAuthQuotaState,
} from '@/features/authFiles/model/credentialStatus';

export interface AccountInspectionSummary {
  source: 'local' | 'server';
  disabled?: boolean;
  action: string;
  actionReason: string;
  actionStatus: string;
  executedAction?: string;
  statusCode: number | null;
  usedPercent: number | null;
  isQuota?: boolean | null;
  planType?: string | null;
  quotaWindows?: CodexInspectionQuotaWindow[];
  quotaInventoryObserved?: boolean;
  error?: string;
  errorKind?: string;
  runId: number;
  resultId: number;
  createdAtMs: number;
}

type CodexQuotaEvidenceSource = 'provider' | 'header' | 'inspection';

interface CodexQuotaEvidenceEvent {
  source: CodexQuotaEvidenceSource;
  atMs: number;
  state: CodexQuotaState;
}

export interface AccountCredentialEvidenceCutoffs {
  authenticationAtMs: number;
  healthyQuotaAtMs: number;
}

export interface AccountCredentialEvidenceBoundary {
  localAtMs: number;
  inspectionAtMs: number;
  inspectionBaselinePending?: boolean;
  headerAtMs: number;
  headerBaselinePending?: boolean;
  actionAtMs: number;
  actionBaselinePending?: boolean;
  authenticationActionAtMs: number;
  authenticationActionBaselinePending?: boolean;
  quotaActionAtMs: number;
  quotaActionBaselinePending?: boolean;
  cooldownAtMs: number;
  cooldownBaselinePending?: boolean;
  fallbackInspectionAtMs: number;
  fallbackInspectionBaselinePending?: boolean;
  fallbackHeaderAtMs: number;
  fallbackHeaderBaselinePending?: boolean;
  fallbackActionAtMs: number;
  fallbackActionBaselinePending?: boolean;
  fallbackCooldownAtMs: number;
  fallbackCooldownBaselinePending?: boolean;
  authenticationAtMs: number;
  rawStatusAtMs: number;
  rawStatusMessages: readonly string[];
  rawStatusCodes: readonly number[];
}

const readFiniteTimestamp = (value: unknown): number | null =>
  typeof value === 'number' && Number.isFinite(value) && value > 0 ? value : null;

const readFinitePercent = (value: unknown): number | null =>
  typeof value === 'number' && Number.isFinite(value) ? value : null;

const hasObservedValue = (value: unknown): boolean => {
  if (value === undefined || value === null) return false;
  if (typeof value === 'string') return value.trim() !== '';
  if (typeof value === 'number') return Number.isFinite(value);
  return true;
};

const isHandledInspectionStatus = (status: string): boolean =>
  status === 'success' || status === 'skipped' || status === 'executed' || status === 'resolved';

const hasInspectionQuotaEvidence = (inspection: AccountInspectionSummary): boolean =>
  inspection.quotaInventoryObserved === true ||
  (inspection.quotaWindows?.length ?? 0) > 0 ||
  inspection.usedPercent !== null ||
  inspection.isQuota === true;

const inspectionProvidesCodexQuotaEvidence = (inspection: AccountInspectionSummary): boolean => {
  if (!hasInspectionQuotaEvidence(inspection)) return false;
  if (inspection.statusCode === null) return false;
  return (
    (inspection.statusCode >= 200 && inspection.statusCode < 400) ||
    inspection.statusCode === 402 ||
    inspection.statusCode === 429
  );
};

export const hasPendingAccountInspectionAction = (
  inspection: AccountInspectionSummary | null | undefined
): boolean => {
  if (!inspection || inspection.action === 'keep') return false;
  if (inspection.action === 'reauth' && inspection.executedAction === 'delete') return false;
  return !isHandledInspectionStatus(inspection.actionStatus);
};

export const getEffectiveAccountInspectionAction = (
  inspection: AccountInspectionSummary | null | undefined
): string =>
  hasPendingAccountInspectionAction(inspection) ? inspection?.action || 'keep' : 'keep';

export const stripSupersededAccountInspectionStatus = (
  inspection: AccountInspectionSummary,
  statusChangedAtMs: number
): AccountInspectionSummary => {
  if (statusChangedAtMs <= 0 || inspection.createdAtMs > statusChangedAtMs) return inspection;
  return {
    ...inspection,
    disabled: undefined,
    action: 'keep',
    actionReason: '',
    actionStatus: 'resolved',
    executedAction: '',
  };
};

const buildInspectionQuotaWindows = (inspection: AccountInspectionSummary): CodexQuotaWindow[] => {
  const observedAtMs = readFiniteTimestamp(inspection.createdAtMs);
  const windows = (inspection.quotaWindows ?? []).map((window) => {
    const id = canonicalizeCodexProviderWindowId(window.id);
    return {
      id,
      label: window.labelKey || id,
      labelKey: window.labelKey || undefined,
      labelParams: window.labelParams,
      usedPercent: readFinitePercent(window.usedPercent),
      resetLabel: window.resetLabel || '-',
      resetAtMs: readFiniteTimestamp(window.resetAtMs),
      resetAccuracy:
        window.resetAccuracy === 'derived' ? 'estimated' : (window.resetAccuracy ?? 'unknown'),
      limitWindowSeconds: readFinitePercent(window.limitWindowSeconds),
      observationSource: 'inspection' as const,
      observedAtMs,
      modelScope: window.modelScope ?? inferCodexQuotaScopeFromProviderWindowId(id),
      providerWindowAliases: window.providerWindowAliases,
    };
  });
  if (windows.length > 0 || inspection.usedPercent === null) return windows;
  return [
    {
      id: 'inspection-summary',
      label: 'codex_quota.observed_window',
      labelKey: 'codex_quota.observed_window',
      usedPercent: inspection.usedPercent,
      resetLabel: '-',
      resetAtMs: null,
      resetAccuracy: 'unknown',
      observationSource: 'inspection',
      observedAtMs,
      modelScope: {
        kind: 'feature',
        key: 'inspection_summary',
        complete: false,
      },
    },
  ];
};

const inspectionProvesAuthentication = (inspection: AccountInspectionSummary): boolean => {
  if (hasActiveCodexInspectionAuthenticationFailure(inspection)) return false;
  if (inspection.statusCode === 401) return false;
  if (inspection.statusCode === null) return false;
  return (
    (inspection.statusCode >= 200 && inspection.statusCode < 400) ||
    ((inspection.statusCode === 402 || inspection.statusCode === 429) &&
      hasInspectionQuotaEvidence(inspection))
  );
};

export const buildInspectionCodexQuotaState = (
  file: AuthFileItem,
  inspection: AccountInspectionSummary | null | undefined
): CodexQuotaState | undefined => {
  if (!inspection) return undefined;
  const atMs = readFiniteTimestamp(inspection.createdAtMs);
  if (atMs === null) return undefined;
  const identity = buildQuotaCredentialIdentity(file);
  if (hasActiveCodexInspectionAuthenticationFailure(inspection)) {
    return {
      status: 'error',
      windows: [],
      error: inspection.error || inspection.actionReason || 'HTTP 401',
      errorStatus: 401,
      failedAtMs: atMs,
      ...identity,
    };
  }
  const quotaWindows = buildInspectionQuotaWindows(inspection);
  if (
    inspection.statusCode !== null &&
    (inspection.statusCode < 200 || inspection.statusCode >= 400) &&
    inspection.statusCode !== 401 &&
    inspection.isQuota !== true
  ) {
    return {
      status: 'error',
      windows: quotaWindows,
      error: inspection.error || inspection.actionReason || `HTTP ${inspection.statusCode}`,
      errorStatus: inspection.statusCode,
      failedAtMs: atMs,
      quotaInventoryObserved: inspection.quotaInventoryObserved,
      planType: inspection.planType ?? null,
      ...identity,
    };
  }
  const providesQuotaEvidence = inspectionProvidesCodexQuotaEvidence(inspection);
  if (!inspectionProvesAuthentication(inspection) && !providesQuotaEvidence) {
    return undefined;
  }
  return {
    status: 'success',
    windows: quotaWindows,
    quotaInventoryObserved:
      inspection.quotaInventoryObserved ??
      (providesQuotaEvidence && (inspection.quotaWindows?.length ?? 0) > 0),
    planType: inspection.planType ?? null,
    observedAtMs: atMs,
    ...(inspection.isQuota === true ? { rateLimitReachedType: 'inspection' } : {}),
    ...identity,
  };
};

export const getCodexQuotaEvidenceAtMs = (
  quota: CodexQuotaState | null | undefined
): number | null => {
  if (!quota) return null;
  if (quota.status === 'success') {
    const candidates = [
      readFiniteTimestamp(quota.fetchedAtMs),
      readFiniteTimestamp(quota.observedAtMs),
      ...quota.windows.map((window) => readFiniteTimestamp(window.observedAtMs)),
    ].filter((value): value is number => value !== null);
    return candidates.length > 0 ? Math.max(...candidates) : null;
  }
  if (quota.status === 'error') return readFiniteTimestamp(quota.failedAtMs);
  return null;
};

const stampQuotaEvidenceWindows = (
  state: CodexQuotaState,
  source: CodexQuotaEvidenceSource,
  atMs: number
): CodexQuotaWindow[] =>
  state.windows.map((window) => ({
    ...window,
    observationSource:
      window.observationSource ??
      (source === 'provider'
        ? 'api_query'
        : source === 'header'
          ? 'response_header'
          : 'inspection'),
    observedAtMs: readFiniteTimestamp(window.observedAtMs) ?? atMs,
  }));

const toEvidenceEvent = (
  source: CodexQuotaEvidenceSource,
  state: CodexQuotaState | null | undefined,
  boundaryAtMs: number,
  authenticationBoundaryAtMs: number,
  credentialRefreshAtMs: number
): CodexQuotaEvidenceEvent | null => {
  if (!state) return null;
  const recordedAtMs = getCodexQuotaEvidenceAtMs(state);
  if (
    credentialRefreshAtMs > 0 &&
    hasCodexAuthenticationErrorSignal(state) &&
    (recordedAtMs === null || recordedAtMs <= credentialRefreshAtMs)
  ) {
    return null;
  }
  if (
    authenticationBoundaryAtMs > 0 &&
    recordedAtMs !== null &&
    recordedAtMs <= authenticationBoundaryAtMs &&
    hasCodexAuthenticationErrorSignal(state)
  ) {
    return null;
  }
  const atMs = recordedAtMs ?? (source === 'provider' && boundaryAtMs === 0 ? 1 : null);
  if (atMs === null || atMs <= boundaryAtMs) return null;
  return { source, atMs, state };
};

const evidenceSourceRank: Record<CodexQuotaEvidenceSource, number> = {
  header: 1,
  inspection: 2,
  provider: 3,
};

const mergeQuotaErrorState = (
  current: CodexQuotaState | undefined,
  event: CodexQuotaEvidenceEvent
): CodexQuotaState => ({
  ...(current ?? {}),
  ...event.state,
  status: 'error',
  windows:
    event.state.windows.length > 0
      ? stampQuotaEvidenceWindows(event.state, event.source, event.atMs)
      : (current?.windows ?? []),
  failedAtMs: event.atMs,
});

const clearSupersededQuotaFailureMetadata = (state: CodexQuotaState): CodexQuotaState => {
  const next = { ...state };
  delete next.error;
  delete next.errorStatus;
  delete next.failedAtMs;
  delete next.observedFromUsageHeaders;
  delete next.observedResetCreditsUnknown;
  delete next.observedTraceId;
  delete next.observedErrorKind;
  delete next.observedErrorCode;
  delete next.activeLimit;
  delete next.creditsOverageLimitReached;
  delete next.spendControlReached;
  delete next.rateLimitReachedType;
  delete next.primaryOverSecondaryLimitPercent;
  delete next.rateLimitResetCreditsError;
  return next;
};

const mergeQuotaSuccessState = (
  current: CodexQuotaState | undefined,
  currentAtMs: number,
  event: CodexQuotaEvidenceEvent,
  replaceCompleteInventory = true
): CodexQuotaState => {
  const observedState: CodexQuotaState = {
    ...event.state,
    status: 'success',
    windows: stampQuotaEvidenceWindows(event.state, event.source, event.atMs),
    observedAtMs: event.atMs,
  };
  if (!current) {
    return { ...observedState, fetchedAtMs: event.atMs };
  }
  const comparisonAtMs = event.atMs === currentAtMs ? Math.max(0, event.atMs - 1) : currentAtMs;
  const activeState: CodexQuotaState = {
    ...clearSupersededQuotaFailureMetadata(current),
    fetchedAtMs: comparisonAtMs,
  };
  if (current.status === 'error') activeState.failedAtMs = comparisonAtMs;
  const merged = resolveQuotaDisplayState(activeState, observedState) ?? observedState;
  const next: CodexQuotaState = {
    ...merged,
    status: 'success',
    fetchedAtMs: event.atMs,
    observedAtMs: event.atMs,
  };
  if (
    replaceCompleteInventory &&
    event.source !== 'header' &&
    event.state.quotaInventoryObserved === true
  ) {
    next.windows = observedState.windows;
  }
  delete next.error;
  delete next.errorStatus;
  delete next.failedAtMs;
  if (event.state.quotaInventoryObserved !== undefined) {
    next.quotaInventoryObserved = event.state.quotaInventoryObserved;
  }
  return next;
};

const CODEX_QUOTA_FACT_KEYS = [
  'quotaInventoryObserved',
  'planType',
  'activeLimit',
  'creditsHasCredits',
  'creditsUnlimited',
  'creditsBalance',
  'creditsOverageLimitReached',
  'creditsApproxLocalMessages',
  'creditsApproxCloudMessages',
  'spendControlReached',
  'spendControlIndividualLimit',
  'rateLimitReachedType',
  'primaryOverSecondaryLimitPercent',
  'subscriptionActiveUntil',
  'rateLimitResetCreditsAvailableCount',
  'rateLimitResetCredits',
  'rateLimitResetCreditsError',
  'observedFromUsageHeaders',
  'observedModelScope',
  'observedResetCreditsUnknown',
  'observedAtMs',
  'observedTraceId',
  'observedErrorKind',
  'observedErrorCode',
] as const satisfies readonly (keyof CodexQuotaState)[];

const mergeConservativeCodexQuotaFacts = (
  current: CodexQuotaState,
  observed: CodexQuotaState,
  previous: CodexQuotaState | undefined
): CodexQuotaState => {
  const next = { ...current };
  const apply = <K extends (typeof CODEX_QUOTA_FACT_KEYS)[number]>(key: K): void => {
    const value = observed[key];
    const previousValue = previous?.[key];
    if (!hasObservedValue(value)) {
      if (hasObservedValue(previousValue)) {
        next[key] = previousValue as CodexQuotaState[K];
      }
      return;
    }
    if (
      observed.status === 'error' &&
      Array.isArray(value) &&
      value.length === 0 &&
      Array.isArray(previousValue) &&
      previousValue.length > 0
    ) {
      next[key] = previousValue as CodexQuotaState[K];
      return;
    }
    next[key] = value as CodexQuotaState[K];
  };
  CODEX_QUOTA_FACT_KEYS.forEach(apply);
  return next;
};

const getCodexQuotaFactAtMs = (quota: CodexQuotaState | null | undefined): number | null => {
  if (!quota) return null;
  const candidates = [
    readFiniteTimestamp(quota.fetchedAtMs),
    readFiniteTimestamp(quota.observedAtMs),
    ...quota.windows.map((window) => readFiniteTimestamp(window.observedAtMs)),
  ].filter((value): value is number => value !== null);
  return candidates.length > 0 ? Math.max(...candidates) : null;
};

const hasCodexQuotaFactPayload = (state: CodexQuotaState): boolean =>
  state.windows.length > 0 || CODEX_QUOTA_FACT_KEYS.some((key) => hasObservedValue(state[key]));

const getCodexQuotaFactAuthority = (
  state: CodexQuotaState,
  factAtMs: number | null
): ConfirmedReauthCodexQuotaFactAuthority => {
  if (factAtMs !== null) return 'provider_snapshot';
  return hasCodexQuotaFactPayload(state) ? 'conservative_cached' : 'none';
};

const clearSupersededAuthenticationLifecycle = (state: CodexQuotaState): CodexQuotaState => {
  const next = { ...state };
  next.error = undefined;
  next.errorStatus = undefined;
  next.failedAtMs = undefined;
  return next;
};

type ConfirmedReauthCodexQuotaLifecycleKind = 'error' | 'success' | 'other';
type ConfirmedReauthCodexQuotaFactAuthority = 'provider_snapshot' | 'conservative_cached' | 'none';

interface ConfirmedReauthCodexQuotaEvent {
  order: number;
  source: CodexQuotaEvidenceSource;
  state: CodexQuotaState;
  factState: CodexQuotaState;
  factAtMs: number | null;
  factAuthority: ConfirmedReauthCodexQuotaFactAuthority;
  lifecycleAtMs: number | null;
  lifecycleKind: ConfirmedReauthCodexQuotaLifecycleKind | null;
  authoritativeInventory: boolean;
  authenticationFailureWasSuperseded: boolean;
}

const getCodexQuotaLifecycleAtMs = (
  quota: CodexQuotaState,
  factAtMs: number | null
): number | null => {
  if (quota.status === 'error') return readFiniteTimestamp(quota.failedAtMs) ?? factAtMs;
  if (quota.status === 'success') return factAtMs;
  return factAtMs;
};

const getConfirmedReauthCodexQuotaEvent = (
  state: CodexQuotaState | undefined,
  order: number,
  authenticationAtMs: number
): ConfirmedReauthCodexQuotaEvent | null => {
  if (!state) return null;
  const sanitizedState = sanitizeSupersededAuthQuotaState(state, authenticationAtMs, {
    allowUnknownFailureTimestamp: true,
  });
  if (!sanitizedState) return null;

  const authenticationFailureWasSuperseded =
    state.status === 'error' && sanitizedState.status === 'success';
  const factState: CodexQuotaState = authenticationFailureWasSuperseded
    ? { ...sanitizedState, status: 'error' }
    : sanitizedState;
  const factAtMs = getCodexQuotaFactAtMs(factState);
  const factAuthority = getCodexQuotaFactAuthority(factState, factAtMs);
  const lifecycleKind: ConfirmedReauthCodexQuotaLifecycleKind | null =
    authenticationFailureWasSuperseded
      ? null
      : state.status === 'error'
        ? 'error'
        : state.status === 'success'
          ? 'success'
          : 'other';

  return {
    order,
    source: 'provider',
    state: sanitizedState,
    factState,
    factAtMs,
    factAuthority,
    lifecycleAtMs: lifecycleKind === null ? null : getCodexQuotaLifecycleAtMs(state, factAtMs),
    lifecycleKind,
    authoritativeInventory:
      factAuthority === 'provider_snapshot' && state.quotaInventoryObserved === true,
    authenticationFailureWasSuperseded,
  };
};

const applyProviderQuotaSnapshot = (
  current: CodexQuotaState | undefined,
  event: ConfirmedReauthCodexQuotaEvent
): CodexQuotaState => {
  const factAtMs = event.factAtMs;
  const observedAtMs = readFiniteTimestamp(event.state.observedAtMs) ?? factAtMs;
  const fetchedAtMs = readFiniteTimestamp(event.state.fetchedAtMs) ?? event.factAtMs;
  const next: CodexQuotaState = {
    ...(current ?? {}),
    ...event.state,
    status: 'success',
    windows:
      event.state.quotaInventoryObserved === true || event.state.windows.length > 0
        ? factAtMs === null
          ? event.state.windows
          : stampQuotaEvidenceWindows(event.state, event.source, factAtMs)
        : (current?.windows ?? []),
  };
  const apply = <K extends (typeof CODEX_QUOTA_FACT_KEYS)[number]>(key: K): void => {
    next[key] = event.state[key] as CodexQuotaState[K];
  };
  CODEX_QUOTA_FACT_KEYS.forEach(apply);
  if (observedAtMs !== null) next.observedAtMs = observedAtMs;
  else delete next.observedAtMs;
  if (fetchedAtMs !== null) next.fetchedAtMs = fetchedAtMs;
  else delete next.fetchedAtMs;
  next.error = undefined;
  next.errorStatus = undefined;
  next.failedAtMs = undefined;
  return next;
};

const mergeConservativeReauthQuotaFacts = (
  current: CodexQuotaState,
  observed: CodexQuotaState,
  factAtMs: number
): CodexQuotaState => {
  const previous = current;
  const next = mergeConservativeCodexQuotaFacts(current, observed, previous);
  if (observed.windows.length > 0) {
    next.windows = stampQuotaEvidenceWindows(observed, 'provider', factAtMs);
  }

  const currentFetchedAtMs = readFiniteTimestamp(current.fetchedAtMs);
  const observedFetchedAtMs = readFiniteTimestamp(observed.fetchedAtMs);
  if (
    observedFetchedAtMs !== null &&
    (currentFetchedAtMs === null || observedFetchedAtMs >= currentFetchedAtMs)
  ) {
    next.fetchedAtMs = observedFetchedAtMs;
  }

  const currentObservedAtMs = readFiniteTimestamp(current.observedAtMs);
  const observedAtMs = readFiniteTimestamp(observed.observedAtMs);
  if (
    currentObservedAtMs !== null &&
    (observedAtMs === null || observedAtMs < currentObservedAtMs)
  ) {
    next.observedAtMs = currentObservedAtMs;
  }

  if (
    observed.status === 'error' &&
    current.quotaInventoryObserved === true &&
    observed.quotaInventoryObserved !== true
  ) {
    next.quotaInventoryObserved = true;
  }
  return next;
};

/**
 * Merge the old and replacement credential states during a confirmed reauth.
 *
 * A replacement error is a newer lifecycle fact, but an empty or inherited
 * error payload is not a newer quota inventory. A failed refresh can also
 * carry a cached Provider snapshot, so fact authority must not be inferred
 * from its outer lifecycle status. Keep those concerns separate so
 * limit/error metadata can coexist with previously observed quota facts,
 * while an authoritative Provider inventory can still replace them.
 */
export const mergeConfirmedReauthCodexQuotaStates = (
  sourceState: CodexQuotaState | undefined,
  replacementState: CodexQuotaState | undefined,
  authenticationAtMs: number
): CodexQuotaState | undefined => {
  const events = [
    getConfirmedReauthCodexQuotaEvent(sourceState, 0, authenticationAtMs),
    getConfirmedReauthCodexQuotaEvent(replacementState, 1, authenticationAtMs),
  ].filter((event): event is ConfirmedReauthCodexQuotaEvent => event !== null);
  if (events.length === 0) return undefined;

  const baseEvent = events[0];
  let current = baseEvent.authoritativeInventory
    ? applyProviderQuotaSnapshot(undefined, baseEvent)
    : baseEvent.authenticationFailureWasSuperseded
      ? clearSupersededAuthenticationLifecycle(baseEvent.state)
      : { ...baseEvent.state };
  let currentFactAtMs = baseEvent.factAtMs;

  const factEvents = [...events]
    .filter((event) => event.factAtMs !== null)
    .sort(
      (left, right) => (left.factAtMs ?? 0) - (right.factAtMs ?? 0) || left.order - right.order
    );
  for (const event of factEvents) {
    if (event === baseEvent || event.factAtMs === null) continue;
    if (currentFactAtMs !== null && event.factAtMs < currentFactAtMs) continue;
    if (event.factAuthority === 'provider_snapshot') {
      const factMergeBase =
        event.lifecycleKind === 'success' ? clearSupersededQuotaFailureMetadata(current) : current;
      current = applyProviderQuotaSnapshot(factMergeBase, event);
    } else {
      current = mergeConservativeReauthQuotaFacts(current, event.factState, event.factAtMs);
    }
    currentFactAtMs = event.factAtMs;
  }

  const lifecycleEvents = events
    .filter(
      (event): event is ConfirmedReauthCodexQuotaEvent & { lifecycleAtMs: number } =>
        event.lifecycleKind !== null && event.lifecycleAtMs !== null
    )
    .sort((left, right) => left.lifecycleAtMs - right.lifecycleAtMs || left.order - right.order);
  const latestLifecycle = lifecycleEvents[lifecycleEvents.length - 1];
  if (!latestLifecycle) return current;

  if (latestLifecycle.lifecycleKind === 'error') {
    return {
      ...current,
      status: 'error',
      error: latestLifecycle.state.error,
      errorStatus: latestLifecycle.state.errorStatus,
      failedAtMs: latestLifecycle.state.failedAtMs ?? latestLifecycle.lifecycleAtMs,
    };
  }
  if (latestLifecycle.lifecycleKind === 'success') {
    const next = { ...current };
    next.status = 'success';
    next.error = undefined;
    next.errorStatus = undefined;
    next.failedAtMs = undefined;
    return next;
  }
  return {
    ...current,
    status: latestLifecycle.state.status,
  };
};

export const reconcileCodexQuotaEvidence = ({
  providerQuota,
  headerQuota,
  inspectionQuota,
  boundaryAtMs = 0,
  authenticationBoundaryAtMs = 0,
  credentialRefreshAtMs = 0,
}: {
  providerQuota?: CodexQuotaState;
  headerQuota?: CodexQuotaState;
  inspectionQuota?: CodexQuotaState;
  boundaryAtMs?: number;
  authenticationBoundaryAtMs?: number;
  credentialRefreshAtMs?: number;
}): CodexQuotaState | undefined => {
  const events = [
    toEvidenceEvent(
      'provider',
      providerQuota,
      boundaryAtMs,
      authenticationBoundaryAtMs,
      credentialRefreshAtMs
    ),
    toEvidenceEvent(
      'header',
      headerQuota,
      boundaryAtMs,
      authenticationBoundaryAtMs,
      credentialRefreshAtMs
    ),
    toEvidenceEvent(
      'inspection',
      inspectionQuota,
      boundaryAtMs,
      authenticationBoundaryAtMs,
      credentialRefreshAtMs
    ),
  ]
    .filter((event): event is CodexQuotaEvidenceEvent => event !== null)
    .sort(
      (left, right) =>
        left.atMs - right.atMs || evidenceSourceRank[left.source] - evidenceSourceRank[right.source]
    );

  let current: CodexQuotaState | undefined;
  let currentAtMs = boundaryAtMs;
  for (const event of events) {
    if (
      current &&
      event.source === 'header' &&
      event.state.status === 'success' &&
      !codexQuotaProvesAuthentication(event.state, 'header')
    ) {
      continue;
    }
    current =
      event.state.status === 'error'
        ? mergeQuotaErrorState(current, event)
        : mergeQuotaSuccessState(current, currentAtMs, event);
    currentAtMs = event.atMs;
  }
  if (!current) return undefined;

  const providerSuccessAtMs =
    providerQuota?.status === 'success' ? getCodexQuotaEvidenceAtMs(providerQuota) : null;
  if (providerSuccessAtMs !== null && providerSuccessAtMs > boundaryAtMs) {
    current.fetchedAtMs = providerSuccessAtMs;
  } else {
    delete current.fetchedAtMs;
  }
  return current;
};

export const isKnownHealthyCodexQuota = (quota: CodexQuotaState | null | undefined): boolean => {
  if (quota?.status !== 'success') return false;
  if (
    quota.rateLimitReachedType ||
    quota.spendControlReached === true ||
    quota.creditsOverageLimitReached === true
  ) {
    return false;
  }
  const knownWindows = quota.windows
    .map((window) => readFinitePercent(window.usedPercent))
    .filter((value): value is number => value !== null);
  if (knownWindows.length > 0) return knownWindows.every((value) => value < 100);
  return quota.quotaInventoryObserved === true;
};

function hasCodexAuthenticationErrorSignal(quota: CodexQuotaState | null | undefined): boolean {
  if (quota?.errorStatus === 401) return true;
  return (
    isObservedCodexAuthenticationError(
      quota?.observedErrorKind ?? '',
      quota?.observedErrorCode ?? ''
    ) || isObservedCodexAuthenticationError(quota?.error ?? '', '')
  );
}

const getCodexObservedErrorSignal = (quota: CodexQuotaState | null | undefined): string =>
  `${quota?.observedErrorKind ?? ''} ${quota?.observedErrorCode ?? ''}`.trim().toLowerCase();

const hasCodexExplicitQuotaLimitEvidence = (quota: CodexQuotaState | null | undefined): boolean => {
  if (quota?.status !== 'success') return false;
  if (
    quota.rateLimitReachedType ||
    quota.spendControlReached === true ||
    quota.creditsOverageLimitReached === true ||
    quota.windows.some((window) => (readFinitePercent(window.usedPercent) ?? 0) >= 100)
  ) {
    return true;
  }
  const signal = getCodexObservedErrorSignal(quota);
  return (
    signal.includes('usage_limit') ||
    signal.includes('quota_exceeded') ||
    signal.includes('quota_depleted') ||
    signal.includes('credits_depleted')
  );
};

const codexQuotaProvesAuthentication = (
  quota: CodexQuotaState | null | undefined,
  source: 'provider' | 'header'
): boolean => {
  if (quota?.status !== 'success') return false;
  if (source === 'provider') return true;
  if (hasCodexAuthenticationErrorSignal(quota)) return false;
  const hasQuotaLimitEvidence = hasCodexExplicitQuotaLimitEvidence(quota);
  if (getCodexObservedErrorSignal(quota) && !hasQuotaLimitEvidence) return false;
  return isKnownHealthyCodexQuota(quota) || hasQuotaLimitEvidence;
};

export const getAccountCredentialEvidenceCutoffs = ({
  providerQuota,
  headerQuota,
  inspection,
  boundaryAtMs = 0,
  authenticationBoundaryAtMs = 0,
  credentialRefreshAtMs = 0,
}: {
  providerQuota?: CodexQuotaState;
  headerQuota?: CodexQuotaState;
  inspection?: AccountInspectionSummary | null;
  boundaryAtMs?: number;
  authenticationBoundaryAtMs?: number;
  credentialRefreshAtMs?: number;
}): AccountCredentialEvidenceCutoffs => {
  let authenticationAtMs = Math.max(
    0,
    boundaryAtMs,
    authenticationBoundaryAtMs,
    credentialRefreshAtMs
  );
  let healthyQuotaAtMs = Math.max(0, boundaryAtMs);
  for (const [source, quota] of [
    ['provider', providerQuota],
    ['header', headerQuota],
  ] as const) {
    const quotaAtMs = getCodexQuotaEvidenceAtMs(quota);
    const isPostAuthenticationBoundary =
      authenticationBoundaryAtMs <= 0 ||
      (quotaAtMs !== null && quotaAtMs > authenticationBoundaryAtMs);
    if (
      codexQuotaProvesAuthentication(quota, source) &&
      quotaAtMs !== null &&
      isPostAuthenticationBoundary
    ) {
      authenticationAtMs = Math.max(authenticationAtMs, quotaAtMs);
      if (isKnownHealthyCodexQuota(quota)) {
        healthyQuotaAtMs = Math.max(healthyQuotaAtMs, quotaAtMs);
      }
    }
  }
  const inspectionAtMs = inspection?.createdAtMs ?? 0;
  if (
    inspection &&
    inspectionProvesAuthentication(inspection) &&
    (authenticationBoundaryAtMs <= 0 || inspectionAtMs > authenticationBoundaryAtMs)
  ) {
    authenticationAtMs = Math.max(authenticationAtMs, inspection.createdAtMs);
    if (
      isKnownHealthyCodexQuota(buildInspectionCodexQuotaState({ name: 'inspection' }, inspection))
    ) {
      healthyQuotaAtMs = Math.max(healthyQuotaAtMs, inspection.createdAtMs);
    }
  }
  return { authenticationAtMs, healthyQuotaAtMs };
};

export const isEvidenceOlderThan = (value: unknown, cutoffAtMs: number): boolean => {
  const atMs = readFiniteTimestamp(value);
  return cutoffAtMs > 0 && atMs !== null && atMs <= cutoffAtMs;
};
