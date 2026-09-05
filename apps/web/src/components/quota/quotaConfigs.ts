import type { TFunction } from 'i18next';
import type {
  AntigravityQuotaState,
  AuthFileItem,
  ClaudeQuotaState,
  CodexQuotaState,
  CodexQuotaWindow,
  CodexRateLimitResetCredit,
  CredentialScopedQuotaState,
  KimiQuotaState,
  XaiBillingSummary,
  XaiQuotaState,
} from '@/types';
import type { UsageHeaderSnapshot } from '@/services/api/usageService';
import type { AuthFilesApiRequestScope } from '@/services/api/authFiles';
import type {
  AntigravityQuotaData,
  ClaudeQuotaData,
  CodexQuotaData,
  KimiQuotaData,
} from '@/utils/quota';
import {
  buildCodexQuotaWindows,
  fetchAntigravityQuota,
  fetchClaudeQuota,
  fetchCodexQuota,
  fetchKimiQuota,
  fetchXaiQuota,
  filterFreshCodexQuotaWindows,
  findCodexProviderWindowMatch,
  isCodexMainQuotaWindow,
  isCodexMainQuotaModelScope,
  isValidQuotaResetAtMs,
  resolveCodexUsageQuotaScope,
  resolveCodexPlanType,
  shouldClearInheritedCodexQuotaProgress,
} from '@/utils/quota';
import {
  buildObservedCodexQuotaFromHeaderSnapshot,
  getHeaderSnapshotErrorCode,
  getHeaderSnapshotErrorKind,
  getHeaderSnapshotPlanType,
  getHeaderSnapshotRecoverAtMs,
  getHeaderSnapshotTraceId,
  getHeaderSnapshotUsedPercent,
  hasUsageHeaderQuotaSignal,
} from '@/utils/usageHeaderSnapshots';
import {
  buildQuotaCredentialIdentity,
  getQuotaCredentialStoreKey,
  scopeQuotaStateToCredential,
} from '@/utils/quota/credentialScope';

type QuotaType = 'antigravity' | 'claude' | 'codex' | 'kimi' | 'xai';

export interface QuotaConfig<TState, TData> {
  type: QuotaType;
  i18nPrefix: string;
  fetchQuota: (
    file: AuthFileItem,
    t: TFunction,
    requestScope?: AuthFilesApiRequestScope
  ) => Promise<TData>;
  getStoreKey?: (file: AuthFileItem) => string;
  buildLoadingState: (file?: AuthFileItem) => TState;
  buildSuccessState: (data: TData, file?: AuthFileItem) => TState;
  buildErrorState: (message: string, status?: number, file?: AuthFileItem) => TState;
  buildFailureState?: (
    message: string,
    status: number | undefined,
    file: AuthFileItem | undefined,
    activeState: TState | undefined,
    failedAtMs: number
  ) => TState;
  scopeState?: (file: AuthFileItem, state: TState | undefined) => TState | undefined;
  buildObservedState?: (
    file: AuthFileItem,
    snapshot: UsageHeaderSnapshot | undefined,
    t: TFunction
  ) => TState | undefined;
}

export const getQuotaStoreKey = <TState, TData>(
  config: Pick<QuotaConfig<TState, TData>, 'getStoreKey'>,
  file: AuthFileItem
): string => config.getStoreKey?.(file) ?? file.name;

export const getScopedQuotaState = <TState, TData>(
  config: Pick<QuotaConfig<TState, TData>, 'getStoreKey' | 'scopeState'>,
  states: Record<string, TState>,
  file: AuthFileItem
): TState | undefined => {
  const storeKey = getQuotaStoreKey(config, file);
  const activeQuota = states[storeKey];
  const scopedQuota = config.scopeState ? config.scopeState(file, activeQuota) : activeQuota;
  if (scopedQuota || storeKey === file.name) return scopedQuota;
  const legacyQuota = states[file.name];
  return config.scopeState ? config.scopeState(file, legacyQuota) : legacyQuota;
};

export const buildQuotaFailureState = <TState, TData>(
  config: Pick<QuotaConfig<TState, TData>, 'buildErrorState' | 'buildFailureState'>,
  message: string,
  status: number | undefined,
  file: AuthFileItem | undefined,
  activeState: TState | undefined,
  failedAtMs = Date.now()
): TState => {
  if (config.buildFailureState) {
    return config.buildFailureState(message, status, file, activeState, failedAtMs);
  }

  const errorState = config.buildErrorState(message, status, file);
  if (!activeState || typeof activeState !== 'object') {
    return { ...errorState, failedAtMs } as TState;
  }

  // Provider states own the payload shape. Preserve it on a transient refresh
  // failure while replacing only the lifecycle/error metadata from the failed
  // request. This keeps non-Codex windows/groups/rows/billing visible too.
  return {
    ...errorState,
    ...activeState,
    status: 'error',
    error: message,
    errorStatus: status,
    failedAtMs,
  } as TState;
};

type DisplayQuotaState = {
  status?: 'idle' | 'loading' | 'success' | 'error';
  error?: string;
  errorStatus?: number | null;
  fetchedAtMs?: number;
  failedAtMs?: number;
  observedAtMs?: number;
};

type CodexQuotaMergeState = DisplayQuotaState & Partial<CodexQuotaState>;

const readFiniteTimestamp = (value: unknown): number | null =>
  typeof value === 'number' && Number.isFinite(value) ? value : null;

const readPositiveTimestamp = (value: unknown): number | null =>
  typeof value === 'number' && Number.isFinite(value) && value > 0 ? value : null;

const hasObservedValue = (value: unknown): boolean => {
  if (value === undefined || value === null) return false;
  if (typeof value === 'string') return value.trim() !== '';
  if (typeof value === 'number') return Number.isFinite(value);
  return true;
};

const hasKnownResetLabel = (value: unknown): value is string => {
  if (typeof value !== 'string') return false;
  const trimmed = value.trim();
  return trimmed !== '' && trimmed !== '-';
};

const readCodexWindowDurationMs = (window: CodexQuotaWindow): number | null => {
  const seconds = window.limitWindowSeconds;
  if (typeof seconds !== 'number' || !Number.isFinite(seconds) || seconds <= 0) return null;
  const durationMs = seconds * 1000;
  return Number.isFinite(durationMs) && durationMs > 0 ? durationMs : null;
};

const hasReliableCodexWindowEvidence = (window: CodexQuotaWindow): boolean =>
  readCodexWindowDurationMs(window) !== null ||
  (isValidQuotaResetAtMs(window.resetAtMs) && window.resetAccuracy === 'exact');

const isCodexZeroOnlyObservedQuotaPlaceholder = (window: CodexQuotaWindow): boolean =>
  window.observationSource === 'response_header' &&
  window.usedPercent === 0 &&
  isCodexMainQuotaWindow(window) &&
  !hasReliableCodexWindowEvidence(window);

const stampCodexQuotaWindows = (
  windows: CodexQuotaWindow[] | undefined,
  observationSource: NonNullable<CodexQuotaWindow['observationSource']>,
  observedAtMs: number | null
): CodexQuotaWindow[] | undefined =>
  windows?.map((window) => {
    const stampedObservedAtMs = readFiniteTimestamp(window.observedAtMs) ?? observedAtMs;
    const hasQuotaProgress =
      typeof window.usedPercent === 'number' && Number.isFinite(window.usedPercent);
    const quotaProgressObservedAtMs = !hasQuotaProgress
      ? null
      : window.quotaProgressObservedAtMs !== undefined
        ? readPositiveTimestamp(window.quotaProgressObservedAtMs)
        : stampedObservedAtMs;
    return {
      ...window,
      observationSource: window.observationSource ?? observationSource,
      observedAtMs: stampedObservedAtMs,
      quotaProgressObservedAtMs,
    };
  });

const mergeCodexQuotaWindow = (
  activeWindow: CodexQuotaWindow,
  observedWindow: CodexQuotaWindow
): CodexQuotaWindow => {
  const hasObservedResetLabel = hasKnownResetLabel(observedWindow.resetLabel);
  const hasObservedResetAt = isValidQuotaResetAtMs(observedWindow.resetAtMs);
  const hasObservedUsedPercent =
    typeof observedWindow.usedPercent === 'number' && Number.isFinite(observedWindow.usedPercent);
  const observedQuotaProgressObservedAtMs = hasObservedUsedPercent
    ? observedWindow.quotaProgressObservedAtMs !== undefined
      ? readPositiveTimestamp(observedWindow.quotaProgressObservedAtMs)
      : readPositiveTimestamp(observedWindow.observedAtMs)
    : null;
  const confirmedCycleRollover = shouldClearInheritedCodexQuotaProgress(
    {
      providerWindowId: activeWindow.id,
      endMs: activeWindow.resetAtMs,
      durationSeconds: activeWindow.limitWindowSeconds,
      boundaryAccuracy: activeWindow.resetAccuracy,
    },
    {
      providerWindowId: observedWindow.id,
      endMs: observedWindow.resetAtMs,
      durationSeconds: observedWindow.limitWindowSeconds,
      boundaryAccuracy: observedWindow.resetAccuracy,
    }
  );
  const resetMetadata = hasObservedResetAt
    ? {
        resetLabel: hasObservedResetLabel ? observedWindow.resetLabel : '-',
        resetAtMs: observedWindow.resetAtMs ?? null,
        resetAccuracy: observedWindow.resetAccuracy ?? 'unknown',
      }
    : hasObservedResetLabel
      ? {
          resetLabel: observedWindow.resetLabel,
          resetAtMs: null,
          resetAccuracy: 'unknown' as const,
        }
      : {};

  return {
    ...activeWindow,
    ...(confirmedCycleRollover ? { usedPercent: null, quotaProgressObservedAtMs: null } : {}),
    ...(hasObservedValue(observedWindow.label) ? { label: observedWindow.label } : {}),
    ...(hasObservedValue(observedWindow.labelKey) ? { labelKey: observedWindow.labelKey } : {}),
    ...(observedWindow.labelParams && Object.keys(observedWindow.labelParams).length > 0
      ? { labelParams: observedWindow.labelParams }
      : {}),
    ...(hasObservedUsedPercent ? { usedPercent: observedWindow.usedPercent } : {}),
    ...(hasObservedUsedPercent
      ? { quotaProgressObservedAtMs: observedQuotaProgressObservedAtMs }
      : {}),
    ...resetMetadata,
    ...(observedWindow.limitWindowSeconds !== null &&
    observedWindow.limitWindowSeconds !== undefined &&
    observedWindow.limitWindowSeconds > 0
      ? { limitWindowSeconds: observedWindow.limitWindowSeconds }
      : {}),
    ...(observedWindow.observationSource
      ? { observationSource: observedWindow.observationSource }
      : {}),
    ...(readFiniteTimestamp(observedWindow.observedAtMs) !== null
      ? { observedAtMs: observedWindow.observedAtMs }
      : {}),
    ...(observedWindow.modelScope ? { modelScope: observedWindow.modelScope } : {}),
    ...(observedWindow.providerWindowAliases
      ? { providerWindowAliases: observedWindow.providerWindowAliases }
      : {}),
  };
};

const mergeCodexQuotaWindows = (
  activeWindows: CodexQuotaWindow[] | undefined,
  observedWindows: CodexQuotaWindow[] | undefined
): CodexQuotaWindow[] | undefined => {
  if (!observedWindows || observedWindows.length === 0) return activeWindows;
  if (!activeWindows || activeWindows.length === 0) return observedWindows;

  const usedObserved = new Set<number>();
  const mergedWindows = activeWindows.map((window, activeIndex) => {
    const observedIndex = findCodexProviderWindowMatch(
      activeWindows,
      observedWindows,
      activeIndex,
      usedObserved
    );
    if (observedIndex < 0) return window;
    usedObserved.add(observedIndex);
    const observedWindow = observedWindows[observedIndex];
    const merged = mergeCodexQuotaWindow(window, observedWindow);
    const aliases = Array.from(
      new Set([
        ...(window.providerWindowAliases ?? []),
        ...(observedWindow.providerWindowAliases ?? []),
        window.id,
      ])
    ).filter((alias) => alias && alias !== observedWindow.id);
    return {
      ...merged,
      id: observedWindow.id,
      ...(aliases.length > 0 ? { providerWindowAliases: aliases } : {}),
    };
  });
  return [...mergedWindows, ...observedWindows.filter((_, index) => !usedObserved.has(index))];
};

const hasKnownResetCreditCount = (quota: CodexQuotaMergeState): boolean => {
  const value = quota.rateLimitResetCreditsAvailableCount;
  return typeof value === 'number' && Number.isFinite(value);
};

const mergeObservedQuotaIntoActive = <TState extends DisplayQuotaState>(
  activeQuota: TState,
  observedQuota: TState
): TState => {
  const active = activeQuota as CodexQuotaMergeState;
  const observed = observedQuota as CodexQuotaMergeState;
  const merged: CodexQuotaMergeState = { ...active };
  const activeWindows = stampCodexQuotaWindows(
    active.windows,
    'api_query',
    readFiniteTimestamp(active.fetchedAtMs)
  );
  const observedWindows = stampCodexQuotaWindows(
    observed.windows,
    'response_header',
    readFiniteTimestamp(observed.observedAtMs)
  )?.filter((window) => !isCodexZeroOnlyObservedQuotaPlaceholder(window));
  const scopedObservation =
    observed.observedFromUsageHeaders === true &&
    (observed.observedModelScope === undefined ||
      !isCodexMainQuotaModelScope(observed.observedModelScope));
  const scalarKeys: Array<keyof CodexQuotaMergeState> = [
    'status',
    'planType',
    'observedAtMs',
    'observedTraceId',
    'observedErrorKind',
    'observedErrorCode',
  ];
  if (!scopedObservation) {
    scalarKeys.push(
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
      'primaryOverSecondaryLimitPercent'
    );
  }
  scalarKeys.forEach((key) => {
    const value = observed[key];
    if (hasObservedValue(value)) {
      (merged as Record<string, unknown>)[key] = value;
    }
  });
  merged.windows = mergeCodexQuotaWindows(activeWindows, observedWindows);
  if (observed.observedFromUsageHeaders === true) merged.observedFromUsageHeaders = true;
  if (observed.observedModelScope) merged.observedModelScope = observed.observedModelScope;
  if (observed.observedResetCreditsUnknown === true && !hasKnownResetCreditCount(active)) {
    merged.observedResetCreditsUnknown = true;
  }
  return merged as TState;
};

const appendMissingObservedQuotaWindows = <TState extends DisplayQuotaState>(
  activeQuota: TState,
  observedQuota: TState
): TState => {
  const active = activeQuota as CodexQuotaMergeState;
  const observed = observedQuota as CodexQuotaMergeState;
  const activeWindows =
    stampCodexQuotaWindows(active.windows, 'api_query', readFiniteTimestamp(active.fetchedAtMs)) ??
    [];
  const observedWindows =
    stampCodexQuotaWindows(
      observed.windows,
      'response_header',
      readFiniteTimestamp(observed.observedAtMs)
    )?.filter((window) => !isCodexZeroOnlyObservedQuotaPlaceholder(window)) ?? [];
  const isAlreadyRepresented = (observedIndex: number): boolean => {
    return activeWindows.some(
      (_, activeIndex) =>
        findCodexProviderWindowMatch(
          activeWindows,
          observedWindows,
          activeIndex,
          new Set<number>()
        ) === observedIndex
    );
  };
  const missingWindows = observedWindows.filter(
    (_, observedIndex) => !isAlreadyRepresented(observedIndex)
  );
  if (missingWindows.length === 0) return activeQuota;
  const merged: CodexQuotaMergeState = {
    ...active,
    windows: [...activeWindows, ...missingWindows],
    observedFromUsageHeaders: true,
    observedModelScope: observed.observedModelScope,
  };
  const observedAtMs = readFiniteTimestamp(observed.observedAtMs);
  if (observedAtMs !== null) merged.observedAtMs = observedAtMs;
  if (hasObservedValue(observed.observedTraceId)) merged.observedTraceId = observed.observedTraceId;
  if (hasObservedValue(observed.observedErrorKind)) {
    merged.observedErrorKind = observed.observedErrorKind;
  }
  if (hasObservedValue(observed.observedErrorCode)) {
    merged.observedErrorCode = observed.observedErrorCode;
  }
  if (observed.observedResetCreditsUnknown === true && !hasKnownResetCreditCount(active)) {
    merged.observedResetCreditsUnknown = true;
  }
  return merged as TState;
};

const clearQuotaFailureForObservedRecovery = <TState extends DisplayQuotaState>(
  quota: TState
): TState => {
  const recovered = { ...quota };
  delete recovered.error;
  delete recovered.errorStatus;
  delete recovered.failedAtMs;
  return recovered;
};

const isObservedQuotaNewerThanFailure = <TState extends DisplayQuotaState>(
  activeQuota: TState,
  observedQuota: TState | undefined
): observedQuota is TState => {
  if (observedQuota?.status !== 'success') return false;
  const failedAtMs = readFiniteTimestamp(activeQuota.failedAtMs);
  const observedAtMs = readFiniteTimestamp(observedQuota.observedAtMs);
  return failedAtMs !== null && observedAtMs !== null && observedAtMs > failedAtMs;
};

export const getCodexQuotaStoreKey = (file: AuthFileItem): string =>
  getQuotaCredentialStoreKey(file);

const scopeCredentialQuotaState = <TState extends CredentialScopedQuotaState>(
  file: AuthFileItem,
  state: TState | undefined
): TState | undefined => scopeQuotaStateToCredential(file, state);

const buildCodexQuotaFailureState = (
  message: string,
  status: number | undefined,
  file: AuthFileItem | undefined,
  activeState: CodexQuotaState | undefined,
  failedAtMs: number
): CodexQuotaState => {
  const preservedState = activeState ? { ...activeState } : null;
  return {
    ...(preservedState ?? { windows: [] }),
    status: 'error',
    windows: preservedState?.windows ?? [],
    error: message,
    errorStatus: status,
    failedAtMs,
    ...buildQuotaCredentialIdentity(file),
  };
};

export const resolveQuotaDisplayState = <TState extends DisplayQuotaState>(
  activeQuota: TState | undefined,
  observedQuota: TState | undefined
): TState | undefined => {
  if (activeQuota?.status === 'error') {
    if (isObservedQuotaNewerThanFailure(activeQuota, observedQuota)) {
      return clearQuotaFailureForObservedRecovery(
        mergeObservedQuotaIntoActive(activeQuota, observedQuota)
      );
    }
    return activeQuota;
  }
  if (activeQuota && activeQuota.status !== 'idle') {
    if (activeQuota.status === 'success' && observedQuota?.status === 'success') {
      const activeCodexQuota = activeQuota as CodexQuotaMergeState;
      const fetchedAtMs = readFiniteTimestamp(activeQuota.fetchedAtMs);
      const observedAtMs = readFiniteTimestamp(observedQuota.observedAtMs);
      if (activeCodexQuota.quotaInventoryObserved === false) {
        if (fetchedAtMs !== null && observedAtMs !== null && observedAtMs > fetchedAtMs) {
          return mergeObservedQuotaIntoActive(activeQuota, observedQuota);
        }
        return appendMissingObservedQuotaWindows(activeQuota, observedQuota);
      }
      if (fetchedAtMs !== null && observedAtMs !== null && observedAtMs > fetchedAtMs) {
        return mergeObservedQuotaIntoActive(activeQuota, observedQuota);
      }
    }
    return activeQuota;
  }
  return observedQuota ?? activeQuota;
};

export const buildObservedCodexQuotaState = (
  file: AuthFileItem,
  snapshot: UsageHeaderSnapshot | undefined,
  t: TFunction,
  nowMs = Date.now()
): CodexQuotaState | undefined => {
  if (!hasUsageHeaderQuotaSignal(snapshot)) return undefined;
  const observedQuota = buildObservedCodexQuotaFromHeaderSnapshot(snapshot);
  const observedScope =
    observedQuota?.quotaScope ??
    resolveCodexUsageQuotaScope({
      model: snapshot?.model,
      analyticsModel: snapshot?.analytics_model,
      requestedModel: snapshot?.requested_model,
      resolvedModel: snapshot?.resolved_model,
    });
  const usedPercent = getHeaderSnapshotUsedPercent(snapshot);
  const recoverAtMS = getHeaderSnapshotRecoverAtMs(snapshot);
  const recoverLabel = recoverAtMS ? new Date(recoverAtMS).toLocaleString() : '-';
  const headerPlanType = observedQuota?.planType || getHeaderSnapshotPlanType(snapshot);
  const planType = headerPlanType || resolveCodexPlanType(file) || null;
  const rawObservedWindows = observedQuota?.payload
    ? buildCodexQuotaWindows(
        observedQuota.payload,
        t,
        planType,
        snapshot?.timestamp_ms ?? nowMs,
        'response_header',
        observedScope
      )
    : [];
  const observedWindows = filterFreshCodexQuotaWindows(rawObservedWindows, nowMs);
  const fallbackExpired = recoverAtMS !== null && recoverAtMS <= nowMs;
  const fallbackUsedPercent = fallbackExpired ? null : usedPercent;
  const fallbackRecoverAtMS = fallbackExpired ? null : recoverAtMS;
  const windows: CodexQuotaWindow[] =
    rawObservedWindows.length > 0
      ? observedWindows
      : fallbackUsedPercent !== null || fallbackRecoverAtMS
        ? [
            {
              id: observedScope.providerWindowIdPrefix
                ? `${observedScope.providerWindowIdPrefix}-observed`
                : 'usage-header-observed',
              label: t('codex_quota.observed_window', { defaultValue: 'Latest request' }),
              usedPercent: fallbackUsedPercent,
              resetLabel: fallbackRecoverAtMS ? recoverLabel : '-',
              resetAtMs: fallbackRecoverAtMS,
              resetAccuracy: fallbackRecoverAtMS ? 'estimated' : 'unknown',
              observationSource: 'response_header',
              observedAtMs: snapshot?.timestamp_ms ?? null,
              quotaProgressObservedAtMs:
                fallbackUsedPercent !== null ? readFiniteTimestamp(snapshot?.timestamp_ms) : null,
              modelScope: observedScope.modelScope,
            },
          ]
        : [];
  return {
    status: 'success',
    windows,
    planType,
    ...buildQuotaCredentialIdentity(file),
    activeLimit: observedQuota?.activeLimit ?? null,
    creditsHasCredits: observedQuota?.creditsHasCredits ?? null,
    creditsUnlimited: observedQuota?.creditsUnlimited ?? null,
    creditsBalance: observedQuota?.creditsBalance ?? null,
    rateLimitReachedType: observedQuota?.rateLimitReachedType ?? null,
    primaryOverSecondaryLimitPercent: observedQuota?.primaryOverSecondaryLimitPercent ?? null,
    observedFromUsageHeaders: true,
    observedModelScope: observedScope.modelScope,
    observedResetCreditsUnknown: true,
    observedAtMs: snapshot?.timestamp_ms,
    observedTraceId: getHeaderSnapshotTraceId(snapshot),
    observedErrorKind: getHeaderSnapshotErrorKind(snapshot),
    observedErrorCode: getHeaderSnapshotErrorCode(snapshot),
  };
};

export type CodexResetCreditExpiryInfo = {
  id: string;
  expiresAt: string;
  expiresAtMs: number;
};

export const getSortedCodexResetCreditExpiries = (
  credits: CodexRateLimitResetCredit[] | undefined,
  nowMs = Date.now()
): CodexResetCreditExpiryInfo[] =>
  (credits ?? [])
    .map((credit, index) => {
      const expiresAt = String(credit.expiresAt ?? '').trim();
      const expiresAtMs = expiresAt ? new Date(expiresAt).getTime() : Number.NaN;
      if (!Number.isFinite(expiresAtMs) || expiresAtMs <= nowMs) return null;
      return {
        id: String(credit.id || index),
        expiresAt,
        expiresAtMs,
      };
    })
    .filter((credit): credit is CodexResetCreditExpiryInfo => Boolean(credit))
    .sort((left, right) => left.expiresAtMs - right.expiresAtMs || left.id.localeCompare(right.id));

export const CLAUDE_CONFIG: QuotaConfig<ClaudeQuotaState, ClaudeQuotaData> = {
  type: 'claude',
  i18nPrefix: 'claude_quota',
  fetchQuota: fetchClaudeQuota,
  getStoreKey: getQuotaCredentialStoreKey,
  buildLoadingState: (file) => ({
    status: 'loading',
    windows: [],
    ...buildQuotaCredentialIdentity(file),
  }),
  buildSuccessState: (data, file) => ({
    status: 'success',
    windows: data.windows,
    quotaInventoryObserved: data.quotaInventoryObserved,
    extraUsage: data.extraUsage,
    planType: data.planType,
    ...buildQuotaCredentialIdentity(file),
    fetchedAtMs: Date.now(),
  }),
  buildErrorState: (message, status, file) => ({
    status: 'error',
    windows: [],
    error: message,
    errorStatus: status,
    ...buildQuotaCredentialIdentity(file),
    failedAtMs: Date.now(),
  }),
  scopeState: scopeCredentialQuotaState,
};

export const ANTIGRAVITY_CONFIG: QuotaConfig<AntigravityQuotaState, AntigravityQuotaData> = {
  type: 'antigravity',
  i18nPrefix: 'antigravity_quota',
  fetchQuota: fetchAntigravityQuota,
  getStoreKey: getQuotaCredentialStoreKey,
  buildLoadingState: (file) => ({
    status: 'loading',
    groups: [],
    subscription: null,
    serverTimeOffsetMs: null,
    ...buildQuotaCredentialIdentity(file),
  }),
  buildSuccessState: (data, file) => ({
    status: 'success',
    groups: data.groups,
    quotaInventoryObserved: data.quotaInventoryObserved,
    subscription: data.subscription ?? null,
    serverTimeOffsetMs: data.serverTimeOffsetMs,
    ...buildQuotaCredentialIdentity(file),
    fetchedAtMs: Date.now(),
  }),
  buildErrorState: (message, status, file) => ({
    status: 'error',
    groups: [],
    subscription: null,
    serverTimeOffsetMs: null,
    error: message,
    errorStatus: status,
    ...buildQuotaCredentialIdentity(file),
    failedAtMs: Date.now(),
  }),
  scopeState: scopeCredentialQuotaState,
};

export const CODEX_CONFIG: QuotaConfig<CodexQuotaState, CodexQuotaData> = {
  type: 'codex',
  i18nPrefix: 'codex_quota',
  fetchQuota: fetchCodexQuota,
  getStoreKey: getCodexQuotaStoreKey,
  buildLoadingState: (file) => ({
    status: 'loading',
    windows: [],
    ...buildQuotaCredentialIdentity(file),
  }),
  buildSuccessState: (data, file) => ({
    status: 'success',
    windows: data.windows,
    quotaInventoryObserved: data.quotaInventoryObserved,
    planType: data.planType,
    subscriptionActiveUntil: data.subscriptionActiveUntil,
    creditsHasCredits: data.creditsHasCredits,
    creditsUnlimited: data.creditsUnlimited,
    creditsBalance: data.creditsBalance,
    creditsOverageLimitReached: data.creditsOverageLimitReached,
    creditsApproxLocalMessages: data.creditsApproxLocalMessages,
    creditsApproxCloudMessages: data.creditsApproxCloudMessages,
    spendControlReached: data.spendControlReached,
    spendControlIndividualLimit: data.spendControlIndividualLimit,
    rateLimitResetCreditsAvailableCount: data.rateLimitResetCreditsAvailableCount,
    rateLimitResetCredits: data.rateLimitResetCredits,
    rateLimitResetCreditsError: data.rateLimitResetCreditsError,
    ...buildQuotaCredentialIdentity(file),
    fetchedAtMs:
      readFiniteTimestamp(data.observedAtMs) ??
      readFiniteTimestamp(data.windows[0]?.observedAtMs) ??
      Date.now(),
  }),
  buildErrorState: (message, status, file) => ({
    status: 'error',
    windows: [],
    error: message,
    errorStatus: status,
    failedAtMs: Date.now(),
    ...buildQuotaCredentialIdentity(file),
  }),
  buildFailureState: buildCodexQuotaFailureState,
  scopeState: scopeCredentialQuotaState,
  buildObservedState: buildObservedCodexQuotaState,
};

export const KIMI_CONFIG: QuotaConfig<KimiQuotaState, KimiQuotaData> = {
  type: 'kimi',
  i18nPrefix: 'kimi_quota',
  fetchQuota: fetchKimiQuota,
  getStoreKey: getQuotaCredentialStoreKey,
  buildLoadingState: (file) => ({
    status: 'loading',
    rows: [],
    ...buildQuotaCredentialIdentity(file),
  }),
  buildSuccessState: (data, file) => ({
    status: 'success',
    rows: data.rows,
    quotaInventoryObserved: data.quotaInventoryObserved,
    ...buildQuotaCredentialIdentity(file),
    fetchedAtMs: Date.now(),
  }),
  buildErrorState: (message, status, file) => ({
    status: 'error',
    rows: [],
    error: message,
    errorStatus: status,
    ...buildQuotaCredentialIdentity(file),
    failedAtMs: Date.now(),
  }),
  scopeState: scopeCredentialQuotaState,
};

export const XAI_CONFIG: QuotaConfig<XaiQuotaState, XaiBillingSummary> = {
  type: 'xai',
  i18nPrefix: 'xai_quota',
  fetchQuota: fetchXaiQuota,
  getStoreKey: getQuotaCredentialStoreKey,
  buildLoadingState: (file) => ({
    status: 'loading',
    billing: null,
    ...buildQuotaCredentialIdentity(file),
  }),
  buildSuccessState: (billing, file) => ({
    status: 'success',
    billing,
    ...buildQuotaCredentialIdentity(file),
    fetchedAtMs: Date.now(),
  }),
  buildErrorState: (message, status, file) => ({
    status: 'error',
    billing: null,
    error: message,
    errorStatus: status,
    ...buildQuotaCredentialIdentity(file),
    failedAtMs: Date.now(),
  }),
  scopeState: scopeCredentialQuotaState,
};
