import type { AuthFileItem, CodexQuotaState, CodexQuotaWindow } from '@/types';
import type { CodexInspectionQuotaWindow } from '@/services/api/usageService';
import { resolveQuotaDisplayState } from '@/components/quota';
import { buildQuotaCredentialIdentity } from '@/utils/quota/credentialScope';
import {
  canonicalizeCodexProviderWindowId,
  inferCodexQuotaScopeFromProviderWindowId,
} from '@/utils/quota/codexQuota';
import { hasActiveCodexInspectionAuthenticationFailure } from '@/features/authFiles/model/credentialStatus';

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
  rawStatusAtMs: number;
  rawStatusMessages: readonly string[];
}

const readFiniteTimestamp = (value: unknown): number | null =>
  typeof value === 'number' && Number.isFinite(value) && value > 0 ? value : null;

const readFinitePercent = (value: unknown): number | null =>
  typeof value === 'number' && Number.isFinite(value) ? value : null;

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
  event: CodexQuotaEvidenceEvent
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
  if (event.source !== 'header' && event.state.quotaInventoryObserved === true) {
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

export const reconcileCodexQuotaEvidence = ({
  providerQuota,
  headerQuota,
  inspectionQuota,
  boundaryAtMs = 0,
  credentialRefreshAtMs = 0,
}: {
  providerQuota?: CodexQuotaState;
  headerQuota?: CodexQuotaState;
  inspectionQuota?: CodexQuotaState;
  boundaryAtMs?: number;
  credentialRefreshAtMs?: number;
}): CodexQuotaState | undefined => {
  const events = [
    toEvidenceEvent('provider', providerQuota, boundaryAtMs, credentialRefreshAtMs),
    toEvidenceEvent('header', headerQuota, boundaryAtMs, credentialRefreshAtMs),
    toEvidenceEvent('inspection', inspectionQuota, boundaryAtMs, credentialRefreshAtMs),
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
  const signal = `${quota?.observedErrorKind ?? ''} ${quota?.observedErrorCode ?? ''}`
    .trim()
    .toLowerCase();
  return (
    signal.includes('auth') ||
    signal.includes('unauthorized') ||
    signal.includes('invalid') ||
    signal.includes('expired') ||
    signal.includes('revoked')
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
  credentialRefreshAtMs = 0,
}: {
  providerQuota?: CodexQuotaState;
  headerQuota?: CodexQuotaState;
  inspection?: AccountInspectionSummary | null;
  boundaryAtMs?: number;
  credentialRefreshAtMs?: number;
}): AccountCredentialEvidenceCutoffs => {
  let authenticationAtMs = Math.max(0, boundaryAtMs, credentialRefreshAtMs);
  let healthyQuotaAtMs = Math.max(0, boundaryAtMs);
  for (const [source, quota] of [
    ['provider', providerQuota],
    ['header', headerQuota],
  ] as const) {
    const quotaAtMs = getCodexQuotaEvidenceAtMs(quota);
    if (codexQuotaProvesAuthentication(quota, source) && quotaAtMs !== null) {
      authenticationAtMs = Math.max(authenticationAtMs, quotaAtMs);
      if (isKnownHealthyCodexQuota(quota)) {
        healthyQuotaAtMs = Math.max(healthyQuotaAtMs, quotaAtMs);
      }
    }
  }
  if (inspection && inspectionProvesAuthentication(inspection)) {
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
