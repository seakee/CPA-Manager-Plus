import type {
  CodexAdditionalRateLimit,
  CodexRateLimitInfo,
  CodexUsagePayload,
  CodexUsageWindow,
  QuotaModelScope,
} from '@/types';
import {
  formatCodexResetLabel,
  resolveCodexQuotaReset,
  type CodexQuotaResetSource,
} from './formatters';
import { normalizeNumberValue, normalizePlanType, normalizeStringValue } from './parsers';
import { normalizeAnalyticsModel } from '@/utils/analyticsModel';

const FIVE_HOUR_SECONDS = 18_000;
const WEEK_SECONDS = 604_800;
const MONTH_SECONDS = 2_592_000;
const MIN_MONTH_SECONDS = 28 * 24 * 60 * 60;
const MAX_MONTH_SECONDS = 31 * 24 * 60 * 60;

export const CODEX_QUOTA_BOUNDARY_JITTER_MS = 60_000;

export const CODEX_MAIN_QUOTA_SCOPE_KEY = 'codex_main';
export const CODEX_SPARK_MODEL_ID = 'gpt-5.3-codex-spark';
export const CODEX_CODE_REVIEW_SCOPE_KEY = 'code_review';
export const CODEX_UNKNOWN_REQUEST_SCOPE_KEY = 'request_scope_unknown';

const CODEX_SPARK_PROVIDER_WINDOW_PREFIX = 'spark';
const CODEX_SPARK_QUOTA_IDENTIFIERS = new Set(['spark', 'codex_spark', 'gpt_5_3_codex_spark']);
const CODEX_SPARK_PROVIDER_WINDOW_PREFIXES = [
  'gpt-5-3-codex-spark',
  'codex-spark',
  CODEX_SPARK_PROVIDER_WINDOW_PREFIX,
];
const CODEX_SPARK_LEGACY_PROVIDER_WINDOW_PREFIXES = ['fast-coding'];
const CODEX_MAIN_PROVIDER_WINDOW_IDS = new Set([
  'five-hour',
  'weekly',
  'monthly',
  'primary',
  'secondary',
]);

type CodexQuotaWindowMeta = {
  id: string;
  labelKey: string;
};

const CODEX_WINDOW_META = {
  codeFiveHour: { id: 'five-hour', labelKey: 'codex_quota.primary_window' },
  codeWeekly: { id: 'weekly', labelKey: 'codex_quota.secondary_window' },
  codeMonthly: { id: 'monthly', labelKey: 'codex_quota.monthly_window' },
  codeReviewFiveHour: {
    id: 'code-review-five-hour',
    labelKey: 'codex_quota.code_review_primary_window',
  },
  codeReviewWeekly: {
    id: 'code-review-weekly',
    labelKey: 'codex_quota.code_review_secondary_window',
  },
  codeReviewMonthly: {
    id: 'code-review-monthly',
    labelKey: 'codex_quota.code_review_monthly_window',
  },
} as const satisfies Record<string, CodexQuotaWindowMeta>;

export type CodexQuotaWindowInfo = {
  id: string;
  labelKey: string;
  labelParams?: Record<string, string | number>;
  usedPercent: number | null;
  resetLabel: string;
  resetAtMs: number | null;
  resetAccuracy: 'exact' | 'estimated' | 'unknown';
  limitWindowSeconds: number | null;
  modelScope: QuotaModelScope;
  providerWindowAliases?: string[];
};

export type CodexQuotaScopeResolution = {
  modelScope: QuotaModelScope;
  providerWindowIdPrefix: string;
  legacyProviderWindowIdPrefixes?: string[];
  labelName?: string;
};

export type CodexUsageModelIdentity = {
  model?: string | null;
  analyticsModel?: string | null;
  requestedModel?: string | null;
  resolvedModel?: string | null;
};

export type CodexQuotaWindowBuildOptions = {
  planType?: string | null;
  observedAtMs?: number;
  source?: CodexQuotaResetSource;
  rateLimitScope?: CodexQuotaScopeResolution;
};

export const isCodexQuotaWindowExpired = (
  window: { resetAtMs?: number | null },
  nowMs = Date.now()
): boolean =>
  typeof window.resetAtMs === 'number' &&
  Number.isFinite(window.resetAtMs) &&
  window.resetAtMs > 0 &&
  window.resetAtMs <= nowMs;

export const filterFreshCodexQuotaWindows = <T extends { resetAtMs?: number | null }>(
  windows: T[],
  nowMs = Date.now()
): T[] => windows.filter((window) => !isCodexQuotaWindowExpired(window, nowMs));

const getWindowSeconds = (window?: CodexUsageWindow | null): number | null => {
  if (!window) return null;
  return normalizeNumberValue(window.limit_window_seconds ?? window.limitWindowSeconds);
};

export const getCodexQuotaWindowUsedPercent = (window?: CodexUsageWindow | null): number | null =>
  normalizeNumberValue(window?.used_percent ?? window?.usedPercent);

const normalizeWindowId = (raw: string) =>
  raw
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '');

const normalizeFeatureKey = (raw: string | null | undefined) =>
  (raw ?? '')
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '_')
    .replace(/^_+|_+$/g, '');

const codexAdditionalWindowIdentityPart = (window?: CodexUsageWindow | null): string => {
  if (!window) return 'none';
  const seconds = getWindowSeconds(window);
  return seconds === null ? 'unknown' : String(seconds);
};

const anonymousCodexAdditionalIdentity = (limitItem: CodexAdditionalRateLimit): string => {
  const rateInfo = limitItem.rate_limit ?? limitItem.rateLimit;
  return [
    'additional',
    'p',
    codexAdditionalWindowIdentityPart(rateInfo?.primary_window ?? rateInfo?.primaryWindow),
    's',
    codexAdditionalWindowIdentityPart(rateInfo?.secondary_window ?? rateInfo?.secondaryWindow),
  ].join('-');
};

const codexAdditionalWindowSortKey = (window?: CodexUsageWindow | null): string => {
  if (!window) return 'none';
  const values = [
    getWindowSeconds(window),
    normalizeNumberValue(window.used_percent ?? window.usedPercent),
    normalizeNumberValue(window.reset_after_seconds ?? window.resetAfterSeconds),
    normalizeNumberValue(window.reset_at ?? window.resetAt),
  ];
  return values.map((value) => (value === null ? '' : String(value))).join(':');
};

const codexAdditionalRateLimitSortKey = (rateInfo: CodexRateLimitInfo): string =>
  [
    codexAdditionalWindowSortKey(rateInfo.primary_window ?? rateInfo.primaryWindow),
    codexAdditionalWindowSortKey(rateInfo.secondary_window ?? rateInfo.secondaryWindow),
    rateInfo.allowed === undefined ? '' : String(rateInfo.allowed),
    String(rateInfo.limit_reached === true || rateInfo.limitReached === true),
  ].join('|');

const mainCodexQuotaScope = (): QuotaModelScope => ({
  kind: 'family',
  key: CODEX_MAIN_QUOTA_SCOPE_KEY,
  complete: true,
});

const sparkCodexQuotaScope = (): QuotaModelScope => ({
  kind: 'models',
  models: [CODEX_SPARK_MODEL_ID],
  complete: true,
});

const incompleteCodexFeatureScope = (key: string): QuotaModelScope => ({
  kind: 'feature',
  key: normalizeFeatureKey(key) || 'unknown',
  complete: false,
});

const normalizedUniqueWindowIds = (values: Array<string | null | undefined>): string[] =>
  Array.from(new Set(values.map((value) => normalizeWindowId(value ?? '')).filter(Boolean))).sort();

export const normalizeCodexModelId = (value: string | null | undefined): string => {
  return normalizeAnalyticsModel(value ?? '')
    .trim()
    .toLowerCase();
};

export const canonicalizeCodexProviderWindowId = (value: string, windowKind?: string): string => {
  const id = value.trim().toLowerCase();
  const normalizedWindowKind = windowKind?.trim().toLowerCase().replace(/_/g, '-');
  if (id === 'primary') return 'five-hour';
  if (id === 'secondary') return normalizedWindowKind === 'monthly' ? 'monthly' : 'weekly';
  for (const prefix of CODEX_SPARK_PROVIDER_WINDOW_PREFIXES) {
    if (id === prefix) return CODEX_SPARK_PROVIDER_WINDOW_PREFIX;
    if (id.startsWith(`${prefix}-`)) {
      return `${CODEX_SPARK_PROVIDER_WINDOW_PREFIX}${id.slice(prefix.length)}`;
    }
  }
  return id;
};

export type CodexQuotaCycleEvidence = {
  providerWindowId?: string | null;
  endMs?: number | null;
  durationSeconds?: number | null;
  boundaryAccuracy?: 'exact' | 'derived' | 'estimated' | 'unknown' | null;
};

type CodexQuotaCadenceClass = 'five_hour' | 'weekly' | 'monthly';

const normalizeCodexProviderWindowToken = (value: string): string => value.trim().toLowerCase();

const inferCodexCadenceToken = (
  value: string | null | undefined
): CodexQuotaCadenceClass | null => {
  const id = normalizeCodexProviderWindowToken(value ?? '');
  const match = id.match(/(?:^|-)(five-hour|weekly|monthly)(?:-\d+)?$/);
  switch (match?.[1]) {
    case 'five-hour':
      return 'five_hour';
    case 'weekly':
      return 'weekly';
    case 'monthly':
      return 'monthly';
    default:
      return null;
  }
};

const isFinitePositive = (value: number | null | undefined): value is number =>
  typeof value === 'number' && Number.isFinite(value) && value > 0;

const codexQuotaCadenceClass = (
  evidence: CodexQuotaCycleEvidence
): CodexQuotaCadenceClass | null => {
  const durationSeconds = evidence.durationSeconds;
  if (isFinitePositive(durationSeconds)) {
    if (Math.abs(durationSeconds - FIVE_HOUR_SECONDS) <= CODEX_QUOTA_BOUNDARY_JITTER_MS / 1000) {
      return 'five_hour';
    }
    if (Math.abs(durationSeconds - WEEK_SECONDS) <= CODEX_QUOTA_BOUNDARY_JITTER_MS / 1000) {
      return 'weekly';
    }
    if (durationSeconds >= MIN_MONTH_SECONDS && durationSeconds <= MAX_MONTH_SECONDS) {
      return 'monthly';
    }
  }

  const providerWindowId = evidence.providerWindowId?.trim().toLowerCase() ?? '';
  const canonicalID = providerWindowId ? canonicalizeCodexProviderWindowId(providerWindowId) : '';
  return inferCodexCadenceToken(canonicalID);
};

const readCodexCycleDurationMs = (evidence: CodexQuotaCycleEvidence): number | null => {
  if (!isFinitePositive(evidence.durationSeconds)) return null;
  const durationMs = evidence.durationSeconds * 1000;
  return Number.isFinite(durationMs) && durationMs > 0 ? durationMs : null;
};

const isCodexCadenceDelta = (deltaMs: number, cadenceMs: number): boolean => {
  const cycleCount = Math.round(deltaMs / cadenceMs);
  return (
    cycleCount >= 1 && Math.abs(deltaMs - cycleCount * cadenceMs) <= CODEX_QUOTA_BOUNDARY_JITTER_MS
  );
};

/**
 * Returns true only when newer Codex window evidence proves that an inherited
 * quota percentage belongs to an older cycle. Boundary jitter and uncertain
 * forward movement remain compatible so metadata-only observations do not
 * erase usable quota evidence.
 */
export const shouldClearInheritedCodexQuotaProgress = (
  active: CodexQuotaCycleEvidence,
  observed: CodexQuotaCycleEvidence
): boolean => {
  const activeClass = codexQuotaCadenceClass(active);
  const observedClass = codexQuotaCadenceClass(observed);
  const activeDurationMs = readCodexCycleDurationMs(active);
  const observedDurationMs = readCodexCycleDurationMs(observed);

  if (activeClass !== null && observedClass !== null && activeClass !== observedClass) {
    return true;
  }

  const activeEndMs = active.endMs;
  const observedEndMs = observed.endMs;
  if (!isFinitePositive(activeEndMs) || !isFinitePositive(observedEndMs)) return false;

  const deltaMs = observedEndMs - activeEndMs;
  if (deltaMs < -CODEX_QUOTA_BOUNDARY_JITTER_MS) return true;
  if (Math.abs(deltaMs) <= CODEX_QUOTA_BOUNDARY_JITTER_MS) return false;

  const cadenceMs =
    activeDurationMs ??
    observedDurationMs ??
    (activeClass === 'five_hour' || observedClass === 'five_hour'
      ? FIVE_HOUR_SECONDS * 1000
      : activeClass === 'weekly' || observedClass === 'weekly'
        ? WEEK_SECONDS * 1000
        : null);
  if (cadenceMs !== null && isCodexCadenceDelta(deltaMs, cadenceMs)) return true;

  if (activeClass === 'monthly' && observedClass === 'monthly') {
    return deltaMs >= MIN_MONTH_SECONDS * 1000 - CODEX_QUOTA_BOUNDARY_JITTER_MS;
  }

  return observed.boundaryAccuracy === 'exact' || observed.boundaryAccuracy === 'derived';
};

export type CodexProviderWindowIdentity = {
  id: string;
  providerWindowAliases?: string[];
};

const inferCodexProviderWindowKind = (value: string): string | undefined => {
  const id = normalizeCodexProviderWindowToken(value);
  if (id === 'primary') return 'five_hour';
  return inferCodexCadenceToken(id) ?? undefined;
};

const codexProviderWindowIdsEquivalent = (left: string, right: string): boolean => {
  const leftToken = normalizeCodexProviderWindowToken(left);
  const rightToken = normalizeCodexProviderWindowToken(right);
  if (!leftToken || !rightToken) return false;
  if (leftToken === rightToken) return true;

  const kinds = Array.from(
    new Set([
      inferCodexProviderWindowKind(leftToken),
      inferCodexProviderWindowKind(rightToken),
      undefined,
    ])
  );
  return kinds.some(
    (kind) =>
      canonicalizeCodexProviderWindowId(leftToken, kind) ===
      canonicalizeCodexProviderWindowId(rightToken, kind)
  );
};

const CODEX_AMBIGUOUS_PROVIDER_WINDOW_ALIASES = new Set(['secondary']);

const providerWindowAliasMatches = (
  active: CodexProviderWindowIdentity,
  observed: CodexProviderWindowIdentity
): string[] => {
  const activeID = normalizeCodexProviderWindowToken(active.id);
  const observedID = normalizeCodexProviderWindowToken(observed.id);
  const activeAliases = new Set(
    (active.providerWindowAliases ?? []).map(normalizeCodexProviderWindowToken).filter(Boolean)
  );
  const observedAliases = new Set(
    (observed.providerWindowAliases ?? []).map(normalizeCodexProviderWindowToken).filter(Boolean)
  );
  const matches = new Set<string>();

  activeAliases.forEach((alias) => {
    if (alias === observedID || observedAliases.has(alias)) matches.add(alias);
  });
  observedAliases.forEach((alias) => {
    if (alias === activeID || activeAliases.has(alias)) matches.add(alias);
  });
  return Array.from(matches);
};

/**
 * Finds a safe active/observed Codex window match. Provider aliases are only
 * used after exact/canonical IDs fail, and an alias must identify exactly one
 * active and one observed window. In particular, `secondary` is deliberately
 * treated as ambiguous because both weekly and Team monthly windows used it.
 */
export const findCodexProviderWindowMatch = (
  activeWindows: readonly CodexProviderWindowIdentity[],
  observedWindows: readonly CodexProviderWindowIdentity[],
  activeIndex: number,
  usedObserved: ReadonlySet<number> = new Set()
): number => {
  const activeWindow = activeWindows[activeIndex];
  if (!activeWindow) return -1;
  const available = observedWindows
    .map((window, index) => ({ window, index }))
    .filter(({ index }) => !usedObserved.has(index));

  const exactCandidates = available.filter(
    ({ window }) =>
      normalizeCodexProviderWindowToken(window.id) ===
      normalizeCodexProviderWindowToken(activeWindow.id)
  );
  if (exactCandidates.length === 1) return exactCandidates[0].index;

  const canonicalCandidates = available.filter(({ window }) =>
    codexProviderWindowIdsEquivalent(activeWindow.id, window.id)
  );
  if (canonicalCandidates.length === 1) {
    const observed = canonicalCandidates[0].window;
    const matchingActiveWindows = activeWindows.filter((candidate) =>
      codexProviderWindowIdsEquivalent(candidate.id, observed.id)
    );
    if (matchingActiveWindows.length === 1) return canonicalCandidates[0].index;
  }

  const aliasCandidates = available
    .map(({ window, index }) => ({
      window,
      index,
      aliases: providerWindowAliasMatches(activeWindow, window),
    }))
    .filter(({ aliases }) =>
      aliases.some((alias) => !CODEX_AMBIGUOUS_PROVIDER_WINDOW_ALIASES.has(alias))
    );
  if (aliasCandidates.length !== 1) return -1;

  const candidate = aliasCandidates[0];
  const usableAliases = candidate.aliases.filter(
    (alias) => !CODEX_AMBIGUOUS_PROVIDER_WINDOW_ALIASES.has(alias)
  );
  const matchingActiveWindows = activeWindows.filter((window) =>
    usableAliases.some((alias) =>
      providerWindowAliasMatches(window, candidate.window).includes(alias)
    )
  );
  if (matchingActiveWindows.length !== 1) return -1;
  return candidate.index;
};

const isCodexSparkProviderWindowId = (value: string): boolean => {
  const raw = value.trim().toLowerCase();
  const id = canonicalizeCodexProviderWindowId(raw);
  return (
    id === CODEX_SPARK_PROVIDER_WINDOW_PREFIX ||
    id.startsWith(`${CODEX_SPARK_PROVIDER_WINDOW_PREFIX}-`) ||
    CODEX_SPARK_LEGACY_PROVIDER_WINDOW_PREFIXES.some(
      (prefix) => raw === prefix || raw.startsWith(`${prefix}-`)
    )
  );
};

export const isCodexMainQuotaModelScope = (scope: QuotaModelScope | null | undefined): boolean =>
  scope?.kind === 'family' &&
  scope.key?.trim().toLowerCase() === CODEX_MAIN_QUOTA_SCOPE_KEY &&
  scope.complete !== false;

export const isCodexSparkModelScope = (scope: QuotaModelScope | null | undefined): boolean =>
  scope?.kind === 'models' &&
  scope.complete !== false &&
  (scope.models ?? []).some((model) => normalizeCodexModelId(model) === CODEX_SPARK_MODEL_ID);

export const canonicalizeCodexProviderWindowIdForScope = (
  providerWindowId: string,
  windowKind?: string,
  modelScope?: QuotaModelScope
): string => {
  const raw = providerWindowId.trim().toLowerCase();
  const canonical = canonicalizeCodexProviderWindowId(raw, windowKind);
  if (isCodexSparkModelScope(modelScope) && raw.startsWith('fast-coding-')) {
    return `spark${raw.slice('fast-coding'.length)}`;
  }
  return canonical;
};

const codexMainProviderWindowAliases = (
  providerWindowId: string,
  modelScope: QuotaModelScope
): string[] => {
  if (!isCodexMainQuotaModelScope(modelScope)) return [];
  switch (canonicalizeCodexProviderWindowId(providerWindowId)) {
    case 'five-hour':
      return ['primary'];
    case 'weekly':
    case 'monthly':
      return ['secondary'];
    default:
      return [];
  }
};

export const isCodexMainProviderWindowId = (providerWindowId: string): boolean => {
  const id = canonicalizeCodexProviderWindowId(providerWindowId);
  return CODEX_MAIN_PROVIDER_WINDOW_IDS.has(id) || id.startsWith('window-');
};

export const isCodexLegacyAllScopeReplacement = (
  providerWindowId: string,
  modelScope: QuotaModelScope
): boolean => {
  if (isCodexMainProviderWindowId(providerWindowId)) return false;
  return modelScope.kind !== 'all' || modelScope.complete === false;
};

export const isCodexMainQuotaWindow = (window: {
  id?: string;
  key?: string;
  providerWindowId?: string;
  modelScope?: QuotaModelScope;
}): boolean => {
  if (isCodexMainQuotaModelScope(window.modelScope)) return true;
  // An explicitly incomplete scope must never regain account-wide meaning
  // merely because a legacy/provider window id happens to be `five-hour`,
  // `weekly`, or `monthly`.
  if (window.modelScope?.complete === false) return false;
  if (window.modelScope && window.modelScope.kind !== 'all') return false;
  const providerWindowId = canonicalizeCodexProviderWindowId(
    window.providerWindowId ?? window.id ?? window.key ?? ''
  );
  return isCodexMainProviderWindowId(providerWindowId);
};

export const resolveCodexAdditionalQuotaScope = (
  limitItem: CodexAdditionalRateLimit
): CodexQuotaScopeResolution => {
  const meteredFeature = normalizeStringValue(
    limitItem?.metered_feature ?? limitItem?.meteredFeature
  );
  const limitName = normalizeStringValue(limitItem?.limit_name ?? limitItem?.limitName);
  const featureKey = normalizeFeatureKey(meteredFeature);
  const nameKey = normalizeFeatureKey(limitName);
  if (
    CODEX_SPARK_QUOTA_IDENTIFIERS.has(featureKey) ||
    (!featureKey && CODEX_SPARK_QUOTA_IDENTIFIERS.has(nameKey))
  ) {
    return {
      modelScope: sparkCodexQuotaScope(),
      providerWindowIdPrefix: CODEX_SPARK_PROVIDER_WINDOW_PREFIX,
      legacyProviderWindowIdPrefixes: normalizedUniqueWindowIds([
        meteredFeature,
        limitName,
        ...CODEX_SPARK_PROVIDER_WINDOW_PREFIXES,
        ...CODEX_SPARK_LEGACY_PROVIDER_WINDOW_PREFIXES,
      ]),
      labelName: limitName ?? meteredFeature ?? CODEX_SPARK_MODEL_ID,
    };
  }

  const anonymousIdentity = anonymousCodexAdditionalIdentity(limitItem);
  const key =
    featureKey || nameKey || normalizeFeatureKey(anonymousIdentity) || 'additional_unknown';
  const conflictingKnownName = Boolean(featureKey && CODEX_SPARK_QUOTA_IDENTIFIERS.has(nameKey));
  return {
    modelScope: incompleteCodexFeatureScope(key),
    providerWindowIdPrefix:
      (conflictingKnownName ? normalizeWindowId(meteredFeature ?? '') : '') ||
      normalizeWindowId(limitName ?? '') ||
      normalizeWindowId(meteredFeature ?? '') ||
      normalizeWindowId(anonymousIdentity) ||
      'additional-unknown',
    labelName: limitName ?? meteredFeature ?? anonymousIdentity,
  };
};

export const resolveCodexUsageQuotaScope = (
  identity: CodexUsageModelIdentity
): CodexQuotaScopeResolution => {
  const resolvedModel = normalizeCodexModelId(identity.resolvedModel);
  const effectiveModel =
    resolvedModel ||
    normalizeCodexModelId(identity.analyticsModel) ||
    normalizeCodexModelId(identity.requestedModel) ||
    normalizeCodexModelId(identity.model);
  if (effectiveModel === CODEX_SPARK_MODEL_ID) {
    return {
      modelScope: sparkCodexQuotaScope(),
      providerWindowIdPrefix: CODEX_SPARK_PROVIDER_WINDOW_PREFIX,
      legacyProviderWindowIdPrefixes: CODEX_SPARK_LEGACY_PROVIDER_WINDOW_PREFIXES,
      labelName: CODEX_SPARK_MODEL_ID,
    };
  }
  if (!effectiveModel) {
    return {
      modelScope: incompleteCodexFeatureScope(CODEX_UNKNOWN_REQUEST_SCOPE_KEY),
      providerWindowIdPrefix: 'request-scope-unknown',
      labelName: CODEX_UNKNOWN_REQUEST_SCOPE_KEY,
    };
  }
  return { modelScope: mainCodexQuotaScope(), providerWindowIdPrefix: '' };
};

export const inferCodexQuotaScopeFromProviderWindowId = (
  providerWindowId: string
): QuotaModelScope => {
  const id = canonicalizeCodexProviderWindowId(providerWindowId);
  if (isCodexMainProviderWindowId(id)) {
    return mainCodexQuotaScope();
  }
  if (isCodexSparkProviderWindowId(providerWindowId)) {
    return sparkCodexQuotaScope();
  }
  if (id === 'code-review' || id.startsWith('code-review-')) {
    return incompleteCodexFeatureScope(CODEX_CODE_REVIEW_SCOPE_KEY);
  }
  const familyMatch = id.match(/^(.*)-(?:five-hour|weekly|monthly)-(\d+)$/);
  const genericMatch = id.match(/^(.*)-window-/);
  return incompleteCodexFeatureScope(familyMatch?.[1] ?? genericMatch?.[1] ?? id);
};

export const isCodexKnownScopedProviderWindowId = (providerWindowId: string): boolean => {
  const raw = providerWindowId.trim().toLowerCase();
  const id = canonicalizeCodexProviderWindowId(raw);
  return (
    isCodexSparkProviderWindowId(raw) ||
    id === 'code-review' ||
    id.startsWith('code-review-') ||
    CODEX_SPARK_LEGACY_PROVIDER_WINDOW_PREFIXES.some(
      (prefix) => raw === prefix || raw.startsWith(`${prefix}-`)
    )
  );
};

const formatWindowDuration = (seconds: number | null): string => {
  if (seconds === null || seconds <= 0) return 'unknown';
  const daySeconds = 86_400;
  const hourSeconds = 3_600;
  if (seconds % daySeconds === 0) {
    const days = seconds / daySeconds;
    return `${days}d`;
  }
  if (seconds % hourSeconds === 0) {
    const hours = seconds / hourSeconds;
    return `${hours}h`;
  }
  return `${seconds}s`;
};

const hasExplicitWindowSeconds = (window?: CodexUsageWindow | null): boolean =>
  getWindowSeconds(window) !== null;

const isMonthlyWindow = (window?: CodexUsageWindow | null): boolean => {
  const seconds = getWindowSeconds(window);
  return seconds !== null && seconds >= MIN_MONTH_SECONDS && seconds <= MAX_MONTH_SECONDS;
};

const pickClassifiedWindows = (
  limitInfo?: CodexRateLimitInfo | null,
  options?: { allowOrderFallback?: boolean; teamPlan?: boolean }
): {
  fiveHourWindow: CodexUsageWindow | null;
  weeklyWindow: CodexUsageWindow | null;
  monthlyWindow: CodexUsageWindow | null;
  longWindow: CodexUsageWindow | null;
  windows: CodexUsageWindow[];
} => {
  const allowOrderFallback = options?.allowOrderFallback ?? true;
  const teamPlan = options?.teamPlan ?? false;
  const primaryWindow = limitInfo?.primary_window ?? limitInfo?.primaryWindow ?? null;
  const secondaryWindow = limitInfo?.secondary_window ?? limitInfo?.secondaryWindow ?? null;
  const rawWindows = [primaryWindow, secondaryWindow];

  let fiveHourWindow: CodexUsageWindow | null = null;
  let weeklyWindow: CodexUsageWindow | null = null;
  let monthlyWindow: CodexUsageWindow | null = null;
  let genericLongWindow: CodexUsageWindow | null = null;
  const windows: CodexUsageWindow[] = [];

  for (const window of rawWindows) {
    if (!window) continue;
    windows.push(window);
    const seconds = getWindowSeconds(window);
    if (seconds === FIVE_HOUR_SECONDS && !fiveHourWindow) {
      fiveHourWindow = window;
    } else if (seconds === WEEK_SECONDS && !weeklyWindow) {
      weeklyWindow = window;
    } else if ((seconds === MONTH_SECONDS || isMonthlyWindow(window)) && !monthlyWindow) {
      monthlyWindow = window;
    } else if (seconds !== null && seconds > FIVE_HOUR_SECONDS && !genericLongWindow) {
      genericLongWindow = window;
    }
  }

  if (allowOrderFallback) {
    const shouldFallbackPrimary = primaryWindow && !hasExplicitWindowSeconds(primaryWindow);
    const shouldFallbackSecondary = secondaryWindow && !hasExplicitWindowSeconds(secondaryWindow);
    if (!fiveHourWindow) {
      fiveHourWindow =
        shouldFallbackPrimary && primaryWindow !== weeklyWindow ? primaryWindow : null;
    }
    if (!weeklyWindow) {
      if (teamPlan) {
        monthlyWindow =
          !monthlyWindow && shouldFallbackSecondary && secondaryWindow !== fiveHourWindow
            ? secondaryWindow
            : monthlyWindow;
      } else {
        weeklyWindow =
          shouldFallbackSecondary && secondaryWindow !== fiveHourWindow ? secondaryWindow : null;
      }
    }
  }

  return {
    fiveHourWindow,
    weeklyWindow,
    monthlyWindow,
    longWindow: weeklyWindow ?? monthlyWindow ?? genericLongWindow,
    windows,
  };
};

export const classifyCodexRateLimitWindows = pickClassifiedWindows;

export const getCodexRateLimitWindows = (rateLimit?: CodexRateLimitInfo | null) => [
  rateLimit?.primary_window ?? rateLimit?.primaryWindow ?? null,
  rateLimit?.secondary_window ?? rateLimit?.secondaryWindow ?? null,
];

export const deriveCodexRateLimitUsedPercent = (
  rateLimit?: CodexRateLimitInfo | null
): number | null => {
  const values = getCodexRateLimitWindows(rateLimit)
    .map((window) => getCodexQuotaWindowUsedPercent(window))
    .filter((value): value is number => value !== null);
  if (!values.length) return null;
  return Math.max(...values);
};

export const isCodexRateLimitReached = (rateLimit?: CodexRateLimitInfo | null): boolean => {
  if (!rateLimit) return false;
  if (rateLimit.allowed === false) return true;
  if (rateLimit.limit_reached === true || rateLimit.limitReached === true) return true;
  return getCodexRateLimitWindows(rateLimit).some((window) => {
    const value = getCodexQuotaWindowUsedPercent(window);
    return value !== null && value >= 100;
  });
};

const addCodexWindowInfo = (
  windows: CodexQuotaWindowInfo[],
  id: string,
  labelKey: string,
  labelParams: Record<string, string | number> | undefined,
  modelScope: QuotaModelScope,
  providerWindowAliases: string[] | undefined,
  window?: CodexUsageWindow | null,
  limitReached?: boolean,
  allowed?: boolean,
  observedAtMs = Date.now(),
  source: CodexQuotaResetSource = 'provider_api'
) => {
  if (!window) return;

  const reset = resolveCodexQuotaReset(window, observedAtMs, source);
  const resetLabel = formatCodexResetLabel(window, observedAtMs, source);
  const usedPercentRaw = getCodexQuotaWindowUsedPercent(window);
  const isLimitReached = Boolean(limitReached) || allowed === false;
  const usedPercent = usedPercentRaw ?? (isLimitReached && resetLabel !== '-' ? 100 : null);
  const aliases = Array.from(
    new Set([...(providerWindowAliases ?? []), ...codexMainProviderWindowAliases(id, modelScope)])
  ).sort();

  windows.push({
    id,
    labelKey,
    labelParams,
    usedPercent,
    resetLabel,
    resetAtMs: reset.resetAtMs,
    resetAccuracy: reset.resetAccuracy,
    limitWindowSeconds: getWindowSeconds(window),
    modelScope,
    ...(aliases.length ? { providerWindowAliases: aliases } : {}),
  });
};

const addCodexRateLimitWindows = (
  windows: CodexQuotaWindowInfo[],
  limitInfo: CodexRateLimitInfo | null | undefined,
  fiveHourMeta: CodexQuotaWindowMeta,
  weeklyMeta: CodexQuotaWindowMeta,
  monthlyMeta: CodexQuotaWindowMeta,
  genericLabelKey: string,
  genericLabelParams?: Record<string, string | number>,
  options?: {
    teamPlan?: boolean;
    observedAtMs?: number;
    source?: CodexQuotaResetSource;
    genericIdPrefix?: string;
    modelScope: QuotaModelScope;
    providerWindowAliasesById?: ReadonlyMap<string, string[]>;
    providerWindowAliasPrefixes?: string[];
    providerWindowAliasBasePrefix?: string;
  }
) => {
  const limitReached = limitInfo?.limit_reached ?? limitInfo?.limitReached;
  const allowed = limitInfo?.allowed;
  const classified = pickClassifiedWindows(limitInfo, { teamPlan: options?.teamPlan });
  const added = new Set<CodexUsageWindow>();
  const aliasesForWindow = (id: string): string[] | undefined => {
    const explicit = options?.providerWindowAliasesById?.get(id);
    if (explicit !== undefined) return explicit;
    const basePrefix = normalizeWindowId(options?.providerWindowAliasBasePrefix ?? '');
    const aliasPrefixes = (options?.providerWindowAliasPrefixes ?? [])
      .map((prefix) => normalizeWindowId(prefix))
      .filter((prefix) => prefix && prefix !== basePrefix);
    if (!basePrefix || !aliasPrefixes.length || !id.startsWith(basePrefix)) return undefined;
    const suffix = id.slice(basePrefix.length);
    return normalizedUniqueWindowIds(aliasPrefixes.map((prefix) => `${prefix}${suffix}`));
  };

  addCodexWindowInfo(
    windows,
    fiveHourMeta.id,
    fiveHourMeta.labelKey,
    genericLabelParams,
    options?.modelScope ?? incompleteCodexFeatureScope('scope_unknown'),
    aliasesForWindow(fiveHourMeta.id),
    classified.fiveHourWindow,
    limitReached,
    allowed,
    options?.observedAtMs,
    options?.source
  );
  if (classified.fiveHourWindow) added.add(classified.fiveHourWindow);
  addCodexWindowInfo(
    windows,
    weeklyMeta.id,
    weeklyMeta.labelKey,
    genericLabelParams,
    options?.modelScope ?? incompleteCodexFeatureScope('scope_unknown'),
    aliasesForWindow(weeklyMeta.id),
    classified.weeklyWindow,
    limitReached,
    allowed,
    options?.observedAtMs,
    options?.source
  );
  if (classified.weeklyWindow) added.add(classified.weeklyWindow);
  addCodexWindowInfo(
    windows,
    monthlyMeta.id,
    monthlyMeta.labelKey,
    genericLabelParams,
    options?.modelScope ?? incompleteCodexFeatureScope('scope_unknown'),
    aliasesForWindow(monthlyMeta.id),
    classified.monthlyWindow,
    limitReached,
    allowed,
    options?.observedAtMs,
    options?.source
  );
  if (classified.monthlyWindow) added.add(classified.monthlyWindow);

  classified.windows.forEach((window, index) => {
    if (added.has(window)) return;
    const seconds = getWindowSeconds(window);
    const duration = formatWindowDuration(seconds);
    const genericIdPrefix = normalizeWindowId(options?.genericIdPrefix ?? '');
    addCodexWindowInfo(
      windows,
      `${genericIdPrefix ? `${genericIdPrefix}-` : ''}window-${duration}-${index}`,
      genericLabelKey,
      { ...genericLabelParams, duration },
      options?.modelScope ?? incompleteCodexFeatureScope('scope_unknown'),
      aliasesForWindow(
        `${genericIdPrefix ? `${genericIdPrefix}-` : ''}window-${duration}-${index}`
      ),
      window,
      limitReached,
      allowed,
      options?.observedAtMs,
      options?.source
    );
  });
};

const addAdditionalRateLimitWindows = (
  windows: CodexQuotaWindowInfo[],
  additionalRateLimits: CodexAdditionalRateLimit[] | null | undefined,
  options?: { teamPlan?: boolean; observedAtMs?: number; source?: CodexQuotaResetSource }
) => {
  if (!Array.isArray(additionalRateLimits)) return;

  const families = additionalRateLimits.flatMap((limitItem, index) => {
    const rateInfo = limitItem?.rate_limit ?? limitItem?.rateLimit ?? null;
    if (!rateInfo) return [];
    const meteredFeature = normalizeStringValue(
      limitItem?.metered_feature ?? limitItem?.meteredFeature
    );
    const limitName =
      normalizeStringValue(limitItem?.limit_name ?? limitItem?.limitName) ??
      meteredFeature ??
      `additional-${index + 1}`;
    const legacyBaseIdPrefix = normalizeWindowId(limitName) || `additional-${index + 1}`;
    const scopeResolution = resolveCodexAdditionalQuotaScope(limitItem);
    return [
      {
        sourceIndex: index,
        rateInfo,
        limitName,
        modelScope: scopeResolution.modelScope,
        baseIdPrefix:
          scopeResolution.providerWindowIdPrefix ||
          normalizeWindowId(limitName) ||
          `additional-${index + 1}`,
        featureIdPrefix: normalizeWindowId(meteredFeature ?? ''),
        legacyBaseIdPrefix,
        sortKey: codexAdditionalRateLimitSortKey(rateInfo),
        legacyIdPrefixes: scopeResolution.legacyProviderWindowIdPrefixes ?? [],
      },
    ];
  });
  const baseIdPrefixCounts = new Map<string, number>();
  const legacyBaseIdPrefixCounts = new Map<string, number>();
  families.forEach(({ baseIdPrefix }) => {
    baseIdPrefixCounts.set(baseIdPrefix, (baseIdPrefixCounts.get(baseIdPrefix) ?? 0) + 1);
  });
  families.forEach(({ legacyBaseIdPrefix }) => {
    legacyBaseIdPrefixCounts.set(
      legacyBaseIdPrefix,
      (legacyBaseIdPrefixCounts.get(legacyBaseIdPrefix) ?? 0) + 1
    );
  });
  const resolvedFamilies = families.map((family) => ({
    ...family,
    idPrefix:
      (baseIdPrefixCounts.get(family.baseIdPrefix) ?? 0) > 1 &&
      family.featureIdPrefix &&
      family.featureIdPrefix !== family.baseIdPrefix
        ? `${family.baseIdPrefix}--${family.featureIdPrefix}`
        : family.baseIdPrefix,
    legacyIdPrefixes: [
      ...family.legacyIdPrefixes,
      (legacyBaseIdPrefixCounts.get(family.legacyBaseIdPrefix) ?? 0) > 1 &&
      family.featureIdPrefix &&
      family.featureIdPrefix !== family.legacyBaseIdPrefix
        ? `${family.legacyBaseIdPrefix}--${family.featureIdPrefix}`
        : family.legacyBaseIdPrefix,
    ],
  }));
  const familiesByIdPrefix = new Map<string, typeof resolvedFamilies>();
  resolvedFamilies.forEach((family) => {
    const group = familiesByIdPrefix.get(family.idPrefix) ?? [];
    group.push(family);
    familiesByIdPrefix.set(family.idPrefix, group);
  });
  const familyIndexes = new Map<number, number>();
  familiesByIdPrefix.forEach((group) => {
    [...group]
      .sort((left, right) => {
        if (left.sortKey !== right.sortKey) return left.sortKey.localeCompare(right.sortKey);
        return left.sourceIndex - right.sourceIndex;
      })
      .forEach((family, familyIndex) => familyIndexes.set(family.sourceIndex, familyIndex));
  });
  resolvedFamilies.forEach(
    ({ sourceIndex, rateInfo, limitName, modelScope, idPrefix, legacyIdPrefixes }) => {
      const familyIndex = familyIndexes.get(sourceIndex) ?? 0;
      const aliasPrefixes = normalizedUniqueWindowIds(legacyIdPrefixes).filter(
        (prefix) => prefix !== idPrefix
      );
      const providerWindowAliasesById = new Map<string, string[]>();
      const addAliases = (id: string) => {
        if (!aliasPrefixes.length || !id.startsWith(idPrefix)) return;
        const suffix = id.slice(idPrefix.length);
        providerWindowAliasesById.set(
          id,
          aliasPrefixes.map((prefix) => `${prefix}${suffix}`)
        );
      };
      const fiveHourId = `${idPrefix}-five-hour-${familyIndex}`;
      const weeklyId = `${idPrefix}-weekly-${familyIndex}`;
      const monthlyId = `${idPrefix}-monthly-${familyIndex}`;
      addAliases(fiveHourId);
      addAliases(weeklyId);
      addAliases(monthlyId);
      const classified = pickClassifiedWindows(rateInfo, { teamPlan: options?.teamPlan });
      classified.windows.forEach((window, index) => {
        if (
          window === classified.fiveHourWindow ||
          window === classified.weeklyWindow ||
          window === classified.monthlyWindow
        ) {
          return;
        }
        const duration = formatWindowDuration(getWindowSeconds(window));
        addAliases(`${idPrefix}-${familyIndex}-window-${duration}-${index}`);
      });

      addCodexRateLimitWindows(
        windows,
        rateInfo,
        {
          id: fiveHourId,
          labelKey: 'codex_quota.additional_primary_window',
        },
        {
          id: weeklyId,
          labelKey: 'codex_quota.additional_secondary_window',
        },
        {
          id: monthlyId,
          labelKey: 'codex_quota.additional_monthly_window',
        },
        'codex_quota.additional_generic_window',
        { name: limitName },
        {
          ...options,
          genericIdPrefix: `${idPrefix}-${familyIndex}`,
          modelScope,
          providerWindowAliasesById,
        }
      );
    }
  );
};

export const buildCodexQuotaWindowInfos = (
  payload: CodexUsagePayload,
  options?: CodexQuotaWindowBuildOptions
): CodexQuotaWindowInfo[] => {
  const windows: CodexQuotaWindowInfo[] = [];
  const rateLimit = payload.rate_limit ?? payload.rateLimit ?? undefined;
  const codeReviewLimit =
    payload.code_review_rate_limit ?? payload.codeReviewRateLimit ?? undefined;
  const additionalRateLimits = payload.additional_rate_limits ?? payload.additionalRateLimits;
  const planType = normalizePlanType(options?.planType ?? payload.plan_type ?? payload.planType);
  const teamPlan = planType === 'team';
  const observedAtMs = options?.observedAtMs ?? Date.now();
  const source = options?.source ?? 'provider_api';
  const rateLimitScope = options?.rateLimitScope ?? {
    modelScope: mainCodexQuotaScope(),
    providerWindowIdPrefix: '',
  };
  const scopedRateLimit = Boolean(rateLimitScope.providerWindowIdPrefix);
  const rateLimitPrefix = normalizeWindowId(rateLimitScope.providerWindowIdPrefix);
  const rateLimitLabelParams = scopedRateLimit
    ? { name: rateLimitScope.labelName ?? rateLimitScope.providerWindowIdPrefix }
    : undefined;

  addCodexRateLimitWindows(
    windows,
    rateLimit,
    scopedRateLimit
      ? {
          id: `${rateLimitPrefix}-five-hour-0`,
          labelKey: 'codex_quota.additional_primary_window',
        }
      : CODEX_WINDOW_META.codeFiveHour,
    scopedRateLimit
      ? {
          id: `${rateLimitPrefix}-weekly-0`,
          labelKey: 'codex_quota.additional_secondary_window',
        }
      : CODEX_WINDOW_META.codeWeekly,
    scopedRateLimit
      ? {
          id: `${rateLimitPrefix}-monthly-0`,
          labelKey: 'codex_quota.additional_monthly_window',
        }
      : CODEX_WINDOW_META.codeMonthly,
    scopedRateLimit ? 'codex_quota.additional_generic_window' : 'codex_quota.generic_window',
    rateLimitLabelParams,
    {
      teamPlan,
      observedAtMs,
      source,
      genericIdPrefix: scopedRateLimit ? `${rateLimitPrefix}-0` : undefined,
      modelScope: rateLimitScope.modelScope,
      providerWindowAliasPrefixes: scopedRateLimit
        ? rateLimitScope.legacyProviderWindowIdPrefixes
        : undefined,
      providerWindowAliasBasePrefix: scopedRateLimit ? rateLimitPrefix : undefined,
    }
  );
  addCodexRateLimitWindows(
    windows,
    codeReviewLimit,
    CODEX_WINDOW_META.codeReviewFiveHour,
    CODEX_WINDOW_META.codeReviewWeekly,
    CODEX_WINDOW_META.codeReviewMonthly,
    'codex_quota.code_review_generic_window',
    undefined,
    {
      teamPlan,
      observedAtMs,
      source,
      genericIdPrefix: 'code-review',
      modelScope: incompleteCodexFeatureScope(CODEX_CODE_REVIEW_SCOPE_KEY),
    }
  );
  addAdditionalRateLimitWindows(windows, additionalRateLimits, { teamPlan, observedAtMs, source });

  return windows;
};
