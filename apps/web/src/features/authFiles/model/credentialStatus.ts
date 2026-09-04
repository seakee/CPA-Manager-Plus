import type { AuthFileItem, CodexQuotaState, CodexQuotaWindow, QuotaResetAccuracy } from '@/types';
import type { UsageHeaderSnapshot } from '@/services/api/usageService';
import { getXaiProbeIssueKey } from '@/utils/quota/xaiPresentation';
import { formatQuotaResetTime, isValidQuotaResetAtMs } from '@/utils/quota/formatters';
import {
  getHeaderSnapshotErrorCode,
  getHeaderSnapshotErrorKind,
  getHeaderSnapshotReachedWindowKind,
  getHeaderSnapshotRecoverAtMs,
  getHeaderSnapshotSummaryWindowKind,
  getHeaderSnapshotTraceId,
  getHeaderSnapshotUsedPercent,
  getHeaderSnapshotWindowUsedPercent,
  hasUsageHeaderQuotaSignal,
} from '@/utils/usageHeaderSnapshots';
import {
  isCodexMainQuotaModelScope,
  isCodexMainQuotaWindow,
  resolveCodexUsageQuotaScope,
} from '@/utils/quota/codexQuota';
import {
  getAuthFileStatusMessage,
  isHealthyAuthFileStatusMessage,
  isRuntimeOnlyAuthFile,
  normalizeProviderKey,
} from '@/features/authFiles/constants';
import {
  getAuthFileStatusIdentityKey,
  getAuthFileStatusSelectionKey,
  readAuthFileStatusAccountId,
  readAuthFileStatusAccountSnapshot,
  readAuthFileStatusCodexMember,
  readAuthFileStatusProvider,
} from '@/utils/authFileStatusMutation';

const CODEX_FIVE_HOUR_WINDOW_SECONDS = 18_000;
const CODEX_WEEKLY_WINDOW_SECONDS = 604_800;
const CODEX_MONTHLY_WINDOW_SECONDS = 2_592_000;
const UNKNOWN_AUTH_INDEX_KEY = '-';
const AUTH_FILE_SELECTION_KEY_SEPARATOR = '\u0000';

export type AuthFileCodexStatusFilter =
  | 'all'
  | 'http_401'
  | 'reauth'
  | 'quota_limited'
  | 'five_hour_limited'
  | 'weekly_limited'
  | 'monthly_limited'
  | 'disabled_with_reset';
export type AuthFileCodexStatusBadgeTone = 'danger' | 'warning' | 'info';
export type AuthFileCodexStatusBadgeKind =
  | 'reauth'
  | 'five_hour_limited'
  | 'weekly_limited'
  | 'monthly_limited'
  | 'disabled_with_reset'
  | 'observed_quota'
  | 'observed_error'
  | 'inspection_error';

export type AuthFileCodexStatusBadge = {
  kind: AuthFileCodexStatusBadgeKind;
  tone: AuthFileCodexStatusBadgeTone;
  labelKey: string;
  defaultLabel: string;
  titleKey?: string;
  defaultTitle?: string;
  labelParams?: Record<string, string | number>;
};

export type AuthFileCodexStatusSummary = {
  isCodex: boolean;
  isHttp401: boolean;
  needsReauth: boolean;
  isQuotaLimited: boolean;
  isUnknownQuotaLimited: boolean;
  isFiveHourLimited: boolean;
  isWeeklyLimited: boolean;
  isMonthlyLimited: boolean;
  hasDisabledRecoveryReset: boolean;
  fiveHourResetLabel: string | null;
  fiveHourResetAtMs?: number | null;
  fiveHourResetAccuracy?: QuotaResetAccuracy;
  weeklyResetLabel: string | null;
  weeklyResetAtMs?: number | null;
  weeklyResetAccuracy?: QuotaResetAccuracy;
  monthlyResetLabel: string | null;
  monthlyResetAtMs?: number | null;
  monthlyResetAccuracy?: QuotaResetAccuracy;
  recoveryResetLabel: string | null;
  recoveryResetAtMs?: number | null;
  recoveryResetAccuracy?: QuotaResetAccuracy;
  fiveHourUsedPercent: number | null;
  weeklyUsedPercent: number | null;
  monthlyUsedPercent: number | null;
  hasRawStatusWarning: boolean;
  badges: AuthFileCodexStatusBadge[];
};

export type AuthFileCodexInspectionSnapshot = {
  fileName: string;
  runtimeId?: string | null;
  provider?: string | null;
  authIndex?: string | number | null;
  accountId?: string | null;
  accountSnapshot?: string | null;
  statusCode?: number | string | null;
  action?: string | null;
  actionStatus?: string | null;
  executedAction?: string | null;
  usedPercent?: number | string | null;
  isQuota?: boolean | null;
  errorKind?: string | null;
  inspectionAtMs?: number | null;
  runId?: number | string | null;
  resultId?: number | string | null;
};

export type AuthFileCodexStatusSources = {
  inspection?: AuthFileCodexInspectionSnapshot;
  headerSnapshot?: UsageHeaderSnapshot;
};

export type AuthFileCodexStatusOptions = {
  ignoreRawStatusCode?: boolean;
  effectiveDisabled?: boolean;
};

export type AuthFilePatchTarget = {
  name: string;
  runtimeId?: string | null;
  authIndex?: string | number | null;
  provider?: string | null;
  accountId?: string | null;
  accountSnapshot?: string | null;
};

const normalizeNumber = (value: unknown): number | null => {
  if (typeof value === 'number') return Number.isFinite(value) ? value : null;
  if (typeof value !== 'string') return null;
  const trimmed = value.trim();
  if (!trimmed) return null;
  const parsed = Number(trimmed);
  return Number.isFinite(parsed) ? parsed : null;
};

export const normalizeAuthFileCredentialStatusCode = (value: unknown): number | null => {
  const normalized = normalizeNumber(value);
  return normalized !== null &&
    Number.isInteger(normalized) &&
    normalized >= 100 &&
    normalized <= 599
    ? normalized
    : null;
};

export const getAuthFileCredentialStatusCodes = (file: AuthFileItem): number[] => {
  const statusCodes = new Set<number>();
  for (const value of [
    file.errorStatus,
    file['error_status'],
    file.statusCode,
    file['status_code'],
  ]) {
    const statusCode = normalizeAuthFileCredentialStatusCode(value);
    if (statusCode !== null) statusCodes.add(statusCode);
  }
  return Array.from(statusCodes);
};

export const getAuthFileCredentialStatusCode = (file: AuthFileItem): number | null =>
  getAuthFileCredentialStatusCodes(file)[0] ?? null;

const HANDLED_CODEX_INSPECTION_ACTION_STATUSES = new Set([
  'success',
  'skipped',
  'executed',
  'resolved',
]);

type CodexInspectionAuthenticationEvidence = Pick<
  AuthFileCodexInspectionSnapshot,
  'action' | 'actionStatus' | 'executedAction' | 'statusCode' | 'errorKind'
>;

export const hasActiveCodexInspectionAuthenticationFailure = (
  inspection: CodexInspectionAuthenticationEvidence | null | undefined
): boolean => {
  if (!inspection) return false;
  const actionStatus = String(inspection.actionStatus ?? '')
    .trim()
    .toLowerCase();
  if (HANDLED_CODEX_INSPECTION_ACTION_STATUSES.has(actionStatus)) return false;
  if (inspection.action === 'reauth' && inspection.executedAction === 'delete') return false;
  const errorKind = typeof inspection.errorKind === 'string' ? inspection.errorKind.trim() : '';
  return (
    inspection.action === 'reauth' ||
    normalizeNumber(inspection.statusCode) === 401 ||
    isObservedCodexAuthenticationError(errorKind, '')
  );
};

const formatObservedRecoverLabel = (value: number | null) => {
  if (!value) return null;
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return null;
  return date.toLocaleString();
};

type CodexStatusReset = {
  label: string | null;
  resetAtMs: number | null;
  resetAccuracy: QuotaResetAccuracy;
};

const resolveCodexStatusReset = (window?: CodexQuotaWindow | null): CodexStatusReset => {
  const resetAtMs = isValidQuotaResetAtMs(window?.resetAtMs) ? (window?.resetAtMs ?? null) : null;
  const legacyLabel = isKnownResetLabel(window?.resetLabel) ? window.resetLabel.trim() : null;
  const canonicalLabel = resetAtMs === null ? null : formatQuotaResetTime(resetAtMs);
  return {
    label: canonicalLabel && canonicalLabel !== '-' ? canonicalLabel : legacyLabel,
    resetAtMs,
    resetAccuracy: resetAtMs === null ? 'unknown' : (window?.resetAccuracy ?? 'unknown'),
  };
};

const OBSERVED_AUTH_ERROR_PATTERNS = [
  /^(?:auth|oauth|authentication|authorization)$/,
  /\b(?:auth|authentication|authorization|oauth)[_ -]?(?:error|failed|failure|invalid|required|expired|revoked)\b/,
  /\bunauthenticated\b/,
  /\bunauthorized\b/,
  /\binvalid[_ -]?(?:api[_ -]?key|token|refresh[_ -]?token|credentials?|grant)\b/,
  /\b(?:api[_ -]?key|token|refresh[_ -]?token|credentials?)[_ -]?(?:expired|invalid|invalidated|revoked)\b/,
  /\b(?:expired|invalid|invalidated|revoked)[_ -]?(?:api[_ -]?key|token|refresh[_ -]?token|credentials?)\b/,
  /\brefresh[_ -]?token[_ -]?reused\b/,
  /\bbad[_ -]?credentials?\b/,
  /\bno[_ -]?auth[_ -]?context\b/,
] as const;

export const isObservedCodexAuthenticationError = (kind: string, code: string) => {
  const text = `${kind} ${code}`.trim().toLowerCase();
  return OBSERVED_AUTH_ERROR_PATTERNS.some((pattern) => pattern.test(text));
};

const isObservedAuthError = isObservedCodexAuthenticationError;

const isObservedQuotaLimitError = (kind: string, code: string) => {
  const text = `${kind} ${code}`.toLowerCase().replace(/[_-]+/g, ' ');
  return (
    /\binsufficient quota\b/.test(text) ||
    /\brate limit(?:ed| (?:reached|exceeded))\b/.test(text) ||
    /\b(?:usage|billing|spending) limit(?:ed| (?:reached|exceeded))\b/.test(text) ||
    /\bquota(?: limit)? (?:reached|exceeded|depleted|exhausted|limited)\b/.test(text) ||
    /\bfree usage(?: limit)? (?:reached|exceeded|depleted|exhausted|limited)\b/.test(text) ||
    /\bcredits?(?: limit)? (?:reached|exceeded|depleted|exhausted|limited)\b/.test(text)
  );
};

const isUnderQuotaLimit = (value: number | null): boolean => value !== null && value < 100;

const normalizeAuthIndexKey = (value: unknown): string => {
  if (value === undefined || value === null) return UNKNOWN_AUTH_INDEX_KEY;
  const normalized = String(value).trim();
  return normalized || UNKNOWN_AUTH_INDEX_KEY;
};

const readFiniteTimestamp = (value: unknown): number | null =>
  typeof value === 'number' && Number.isFinite(value) && value > 0 ? value : null;

const readExplicitTimestampMs = (value: unknown): number | null => {
  const timestamp = typeof value === 'number' ? value : Number(String(value ?? '').trim());
  return Number.isFinite(timestamp) && timestamp > 0 ? timestamp : null;
};

const readLooseTimestampMs = (value: unknown): number | null => {
  if (value === undefined || value === null || value === '') return null;
  const numeric = typeof value === 'number' ? value : Number(String(value).trim());
  if (Number.isFinite(numeric)) {
    const timestamp = numeric < 1e12 ? numeric * 1000 : numeric;
    return timestamp > 0 ? timestamp : null;
  }
  const parsed = Date.parse(String(value));
  return Number.isFinite(parsed) && parsed > 0 ? parsed : null;
};

const getAuthFileStatusObservedAtMs = (file: AuthFileItem): number | null => {
  const timestamps = [
    readExplicitTimestampMs(file['updatedAtMs']),
    readExplicitTimestampMs(file['updated_at_ms']),
    readLooseTimestampMs(file['updatedAt']),
    readLooseTimestampMs(file['updated_at']),
    readLooseTimestampMs(file.modified),
    readLooseTimestampMs(file['modtime']),
    readLooseTimestampMs(file.lastRefresh),
    readLooseTimestampMs(file['last_refresh']),
  ].filter((value): value is number => value !== null);
  return timestamps.length > 0 ? Math.max(...timestamps) : null;
};

const readHttpStatusCodeFromText = (value: unknown): number | null => {
  if (typeof value !== 'string') return null;
  const match = value.match(/\b([1-5][0-9]{2})\b/);
  return match ? normalizeNumber(match[1]) : null;
};

const readAuthFileAuthIndex = (file: AuthFileItem): string | number | null =>
  (file.authIndex ?? file['auth_index'] ?? file['auth-index'] ?? null) as string | number | null;

const isCodexAuthFile = (file: AuthFileItem): boolean =>
  normalizeProviderKey(String(file.type ?? file.provider ?? '')) === 'codex';

const isKnownResetLabel = (value: unknown): value is string => {
  if (typeof value !== 'string') return false;
  const trimmed = value.trim();
  return trimmed.length > 0 && trimmed !== '-';
};

const normalizeWindowSeconds = (value: unknown): number | null => normalizeNumber(value);

const findCodexQuotaWindow = (
  quota: CodexQuotaState | undefined,
  preferredMatch: (window: CodexQuotaState['windows'][number]) => boolean,
  limitWindowSeconds: number
) => {
  const windows = (quota?.windows ?? []).filter(isCodexMainQuotaWindow);
  return (
    windows.find(preferredMatch) ??
    windows.find(
      (window) => normalizeWindowSeconds(window.limitWindowSeconds) === limitWindowSeconds
    ) ??
    null
  );
};

const isMainCodexHeaderQuota = (snapshot: UsageHeaderSnapshot | undefined): boolean => {
  if (!snapshot) return true;
  const hasModelIdentity = [
    snapshot.model,
    snapshot.analytics_model,
    snapshot.requested_model,
    snapshot.resolved_model,
  ].some((value) => typeof value === 'string' && value.trim() !== '');
  if (!hasModelIdentity) {
    // Preserve neutral diagnostics such as Retry-After/request errors, but do
    // not let any quota-bearing or quota-limit header without model identity
    // become account-wide Codex evidence.
    const hasQuotaMetadata =
      (snapshot.header_quota_used_percent !== null &&
        snapshot.header_quota_used_percent !== undefined) ||
      (snapshot.header_quota_recover_at_ms !== null &&
        snapshot.header_quota_recover_at_ms !== undefined) ||
      Boolean(snapshot.header_quota_plan_type?.trim()) ||
      Boolean(snapshot.response_metadata?.quota) ||
      Boolean(snapshot.response_metadata?.rate_limit) ||
      Boolean(snapshot.response_metadata?.provider_usage);
    return (
      !hasQuotaMetadata &&
      !isObservedQuotaLimitError(
        getHeaderSnapshotErrorKind(snapshot),
        getHeaderSnapshotErrorCode(snapshot)
      )
    );
  }
  return isCodexMainQuotaModelScope(
    resolveCodexUsageQuotaScope({
      model: snapshot.model,
      analyticsModel: snapshot.analytics_model,
      requestedModel: snapshot.requested_model,
      resolvedModel: snapshot.resolved_model,
    }).modelScope
  );
};

const findCodexFiveHourQuotaWindow = (quota?: CodexQuotaState) =>
  findCodexQuotaWindow(
    quota,
    (window) => window.id === 'five-hour' || window.labelKey === 'codex_quota.primary_window',
    CODEX_FIVE_HOUR_WINDOW_SECONDS
  );

const findCodexWeeklyQuotaWindow = (quota?: CodexQuotaState) =>
  findCodexQuotaWindow(
    quota,
    (window) => window.id === 'weekly' || window.labelKey === 'codex_quota.secondary_window',
    CODEX_WEEKLY_WINDOW_SECONDS
  );

const findCodexMonthlyQuotaWindow = (quota?: CodexQuotaState) =>
  findCodexQuotaWindow(
    quota,
    (window) => window.id === 'monthly' || window.labelKey === 'codex_quota.monthly_window',
    CODEX_MONTHLY_WINDOW_SECONDS
  );

export const getAuthFileCodexInspectionKey = (fileName: string, authIndex?: unknown) =>
  getAuthFileStatusIdentityKey({
    name: fileName,
    authIndex:
      normalizeAuthIndexKey(authIndex) === UNKNOWN_AUTH_INDEX_KEY
        ? null
        : normalizeAuthIndexKey(authIndex),
  });

export const getAuthFileCodexInspectionKeyForIdentity = (
  identity: Pick<
    AuthFileCodexInspectionSnapshot,
    'fileName' | 'runtimeId' | 'provider' | 'authIndex' | 'accountId' | 'accountSnapshot'
  >
) =>
  getAuthFileStatusIdentityKey({
    name: identity.fileName,
    runtimeId: identity.runtimeId,
    provider: identity.provider,
    authIndex: identity.authIndex,
    accountId: identity.accountId,
    accountSnapshot: identity.accountSnapshot,
  });

export const getAuthFileInspectionSnapshotKey = (
  snapshot: AuthFileCodexInspectionSnapshot
): string => {
  const identityKey = getAuthFileCodexInspectionKeyForIdentity(snapshot);
  const runId =
    typeof snapshot.runId === 'number' && Number.isFinite(snapshot.runId)
      ? String(snapshot.runId)
      : typeof snapshot.runId === 'string' && snapshot.runId.trim()
        ? snapshot.runId.trim()
        : null;
  const resultId =
    typeof snapshot.resultId === 'number' && Number.isFinite(snapshot.resultId)
      ? String(snapshot.resultId)
      : typeof snapshot.resultId === 'string' && snapshot.resultId.trim()
        ? snapshot.resultId.trim()
        : null;

  if (runId !== null && resultId !== null) {
    return JSON.stringify([
      identityKey,
      'result',
      runId,
      resultId,
      normalizeNumber(snapshot.inspectionAtMs),
    ]);
  }

  return JSON.stringify([
    identityKey,
    'fallback',
    normalizeNumber(snapshot.inspectionAtMs),
    normalizeNumber(snapshot.statusCode),
    typeof snapshot.action === 'string' ? snapshot.action.trim().toLowerCase() : '',
    typeof snapshot.errorKind === 'string' ? snapshot.errorKind.trim().toLowerCase() : '',
    snapshot.isQuota ?? null,
  ]);
};

export const getHandledAuthFileInspectionSnapshotKeys = (
  snapshots: AuthFileCodexInspectionSnapshot[],
  targetIdentityKey: string
): string[] =>
  snapshots
    .filter(
      (snapshot) =>
        getAuthFileCodexInspectionKeyForIdentity(snapshot) === targetIdentityKey &&
        isAuthFileInspectionAuthenticationFailure(snapshot)
    )
    .map(getAuthFileInspectionSnapshotKey);

export const filterSuppressedAuthFileInspectionSnapshots = (
  snapshots: AuthFileCodexInspectionSnapshot[],
  suppressedSnapshotKeys: ReadonlySet<string>
): AuthFileCodexInspectionSnapshot[] => {
  if (suppressedSnapshotKeys.size === 0) return snapshots;
  const filtered = snapshots.filter(
    (snapshot) => !suppressedSnapshotKeys.has(getAuthFileInspectionSnapshotKey(snapshot))
  );
  return filtered.length === snapshots.length ? snapshots : filtered;
};

export const getAuthFileCodexInspectionKeyForFile = (file: AuthFileItem) =>
  getAuthFileStatusIdentityKey(file);

export const getAuthFileSelectionKey = (file: AuthFileItem): string =>
  getAuthFileStatusSelectionKey(file);

export const getAuthFileNameFromSelectionKey = (key: string): string =>
  key.split(AUTH_FILE_SELECTION_KEY_SEPARATOR, 1)[0] ?? '';

export const getAuthFilePatchTarget = (file: AuthFileItem): AuthFilePatchTarget => {
  const runtimeId = typeof file.id === 'string' ? file.id.trim() : '';
  const authIndex = readAuthFileAuthIndex(file);
  const provider = normalizeProviderKey(readAuthFileStatusProvider(file));
  const accountId = readAuthFileStatusAccountId(file);
  const accountSnapshot =
    provider === 'codex'
      ? readAuthFileStatusCodexMember(file)
      : readAuthFileStatusAccountSnapshot(file);
  return {
    name: file.name,
    ...(runtimeId ? { runtimeId } : {}),
    ...(authIndex === null || authIndex === undefined || String(authIndex).trim() === ''
      ? {}
      : { authIndex }),
    ...(provider ? { provider } : {}),
    ...(accountId ? { accountId } : {}),
    ...(accountSnapshot ? { accountSnapshot } : {}),
  };
};

export const hasPartialSharedAuthFileSelection = (
  files: AuthFileItem[],
  selectedKeys: Iterable<string>
): boolean => {
  const selectableRowsByName = new Map<string, number>();
  files.forEach((file) => {
    if (isRuntimeOnlyAuthFile(file)) return;
    const name = String(file.name ?? '').trim();
    if (!name) return;
    selectableRowsByName.set(name, (selectableRowsByName.get(name) ?? 0) + 1);
  });

  const selectedRowsByName = new Map<string, number>();
  Array.from(selectedKeys).forEach((key) => {
    const name = getAuthFileNameFromSelectionKey(key).trim();
    if (!name) return;
    selectedRowsByName.set(name, (selectedRowsByName.get(name) ?? 0) + 1);
  });

  return Array.from(selectedRowsByName.entries()).some(([name, selectedCount]) => {
    const totalCount = selectableRowsByName.get(name) ?? 0;
    return totalCount > 1 && selectedCount > 0 && selectedCount < totalCount;
  });
};

export const getWholeAuthFileDeleteCandidates = (
  files: AuthFileItem[],
  eligibleRows: AuthFileItem[]
): AuthFileItem[] => {
  const allRowCountByName = new Map<string, number>();
  files.forEach((file) => {
    if (isRuntimeOnlyAuthFile(file)) return;
    const name = String(file.name ?? '').trim();
    if (!name) return;
    allRowCountByName.set(name, (allRowCountByName.get(name) ?? 0) + 1);
  });

  const eligibleRowCountByName = new Map<string, number>();
  eligibleRows.forEach((file) => {
    if (isRuntimeOnlyAuthFile(file)) return;
    const name = String(file.name ?? '').trim();
    if (!name) return;
    eligibleRowCountByName.set(name, (eligibleRowCountByName.get(name) ?? 0) + 1);
  });

  const emittedNames = new Set<string>();
  return eligibleRows.filter((file) => {
    const name = String(file.name ?? '').trim();
    if (!name || emittedNames.has(name)) return false;
    const allCount = allRowCountByName.get(name) ?? 0;
    const eligibleCount = eligibleRowCountByName.get(name) ?? 0;
    if (allCount === 0 || eligibleCount !== allCount) return false;
    emittedNames.add(name);
    return true;
  });
};

export const buildAuthFileCodexInspectionMap = (
  items: AuthFileCodexInspectionSnapshot[]
): Map<string, AuthFileCodexInspectionSnapshot> => {
  const map = new Map<string, AuthFileCodexInspectionSnapshot>();
  items.forEach((item) => {
    if (!item.fileName) return;
    map.set(getAuthFileCodexInspectionKeyForIdentity(item), item);
  });
  return map;
};

const activeCodexQuotaMatchesAuthFile = (
  file: AuthFileItem,
  quota: CodexQuotaState | undefined
): boolean => {
  const quotaAuthFileKey = typeof quota?.authFileKey === 'string' ? quota.authFileKey.trim() : '';
  if (!quotaAuthFileKey) return false;
  return quotaAuthFileKey === getAuthFileCodexInspectionKeyForFile(file);
};

export const getAuthFileScopedCodexQuota = (
  file: AuthFileItem,
  quota: CodexQuotaState | undefined
): CodexQuotaState | undefined => {
  if (!quota) return undefined;
  if (!quota.authFileKey) return undefined;
  return activeCodexQuotaMatchesAuthFile(file, quota) ? quota : undefined;
};

const shouldSuppressOlderCodexStatusSource = (
  file: AuthFileItem,
  quota: CodexQuotaState | undefined,
  sourceAtMs: unknown,
  newerSourceAtMs?: unknown
): boolean => {
  const normalizedSourceAtMs = readFiniteTimestamp(sourceAtMs);
  if (normalizedSourceAtMs === null) return false;
  const fetchedAtMs =
    quota?.status === 'success' && activeCodexQuotaMatchesAuthFile(file, quota)
      ? readFiniteTimestamp(quota.fetchedAtMs)
      : null;
  const normalizedNewerSourceAtMs = readFiniteTimestamp(newerSourceAtMs);
  return (
    (fetchedAtMs !== null && fetchedAtMs >= normalizedSourceAtMs) ||
    (normalizedNewerSourceAtMs !== null && normalizedNewerSourceAtMs > normalizedSourceAtMs)
  );
};

const isObservedQuotaErrorValue = (value: string): boolean => isObservedQuotaLimitError('', value);

const GENERIC_OBSERVED_ERROR_VALUES = new Set([
  'error',
  'failed',
  'failure',
  'unknown',
  'unknown_error',
]);

const isGenericObservedErrorValue = (value: string): boolean =>
  GENERIC_OBSERVED_ERROR_VALUES.has(value.trim().toLowerCase());

type HeaderProviderUsage = NonNullable<UsageHeaderSnapshot['response_metadata']>['provider_usage'];

const getProviderUsageText = (providerUsage: HeaderProviderUsage): string =>
  [providerUsage?.kind, providerUsage?.state, providerUsage?.code]
    .filter(Boolean)
    .join(' ')
    .trim()
    .toLowerCase();

const isProviderUsageQuotaLimited = (
  providerUsage: HeaderProviderUsage,
  nowMs = Date.now()
): boolean => {
  if (!providerUsage) return false;
  const recoverAtMs = normalizeNumber(providerUsage.recover_at_ms);
  const referenceNowMs = Number.isFinite(nowMs) ? nowMs : Date.now();
  if (recoverAtMs !== null && recoverAtMs > 0 && recoverAtMs <= referenceNowMs) return false;
  const text = getProviderUsageText(providerUsage);
  const actual = normalizeNumber(providerUsage.actual);
  const limit = normalizeNumber(providerUsage.limit);
  const remaining = normalizeNumber(providerUsage.remaining);
  const overage = normalizeNumber(providerUsage.overage);
  return (
    isObservedQuotaErrorValue(text) ||
    /\b(?:exhausted|depleted|limited|limit[_ -]?reached|quota[_ -]?exceeded)\b/.test(text) ||
    (remaining !== null && remaining <= 0) ||
    (actual !== null && limit !== null && actual >= limit) ||
    (overage !== null && overage > 0)
  );
};

const isHealthyProviderUsage = (providerUsage: HeaderProviderUsage, nowMs: number): boolean => {
  if (!providerUsage) return false;
  const text = getProviderUsageText(providerUsage);
  if (
    isProviderUsageQuotaLimited(providerUsage, nowMs) ||
    isObservedAuthError('', text) ||
    /\b(?:blocked|error|failed|failure|unavailable|unknown)\b/.test(text)
  ) {
    return false;
  }
  const actual = normalizeNumber(providerUsage.actual);
  const limit = normalizeNumber(providerUsage.limit);
  const remaining = normalizeNumber(providerUsage.remaining);
  return (
    (remaining !== null && remaining > 0) ||
    (actual !== null && limit !== null && actual < limit) ||
    /\b(?:active|available|healthy|ok|success|observed)\b/.test(providerUsage.state ?? '')
  );
};

const hasDepletedRawHeaderQuotaWindow = (headerSnapshot: UsageHeaderSnapshot): boolean =>
  [
    headerSnapshot.response_metadata?.quota?.primary,
    headerSnapshot.response_metadata?.quota?.secondary,
  ].some((window) => {
    const usedPercent = normalizeNumber(window?.used_percent);
    return usedPercent !== null && usedPercent >= 100;
  });

const sanitizeSupersededAuthErrorValue = (value: string | undefined): string | undefined => {
  if (!value || isObservedQuotaErrorValue(value)) return value;
  if (isObservedAuthError('', value) || isGenericObservedErrorValue(value)) return undefined;
  return value;
};

const OBSERVED_NEUTRAL_ERROR_PATTERNS = [
  /\b(?:invalid|bad|malformed)[_ -]?request(?:[_ -]?error)?\b/,
  /\brequest[_ -]?(?:invalid|cancel(?:ed|led)|error|too[_ -]?large)\b/,
  /\b(?:context[_ -]?cancel(?:ed|led)|client[_ -]?(?:disconnect(?:ed)?|closed[_ -]?(?:request|connection))|unexpected[_ -]?eof|broken[_ -]?pipe)\b/,
  /\b499\b/,
] as const;

const isNeutralAuthFileRuntimeStatus = (statusCode: number | null, statusText: string): boolean => {
  if (statusCode !== null && statusCode >= 400 && statusCode < 500) return true;
  if (statusCode !== null && statusCode >= 500) return true;
  return OBSERVED_NEUTRAL_ERROR_PATTERNS.some((pattern) => pattern.test(statusText));
};

const isQualifiedHeaderCredentialStatusSource = (
  headerSnapshot: UsageHeaderSnapshot | undefined,
  nowMs: number
): boolean => {
  if (!headerSnapshot) return false;
  const errorKind = getHeaderSnapshotErrorKind(headerSnapshot);
  const errorCode = getHeaderSnapshotErrorCode(headerSnapshot);
  const errorText = `${errorKind} ${errorCode}`.trim().toLowerCase();
  if (isObservedQuotaErrorValue(errorText)) return true;
  if (OBSERVED_NEUTRAL_ERROR_PATTERNS.some((pattern) => pattern.test(errorText))) return false;
  if (errorText) return isObservedAuthError(errorKind, errorCode);

  const usedPercent = getHeaderSnapshotUsedPercent(headerSnapshot);
  const hasDepletedQuotaWindow = (['five_hour', 'weekly', 'monthly', 'unknown'] as const).some(
    (windowKind) => (getHeaderSnapshotWindowUsedPercent(headerSnapshot, windowKind) ?? 0) >= 100
  );
  const hasQuotaLimitOutcome =
    (usedPercent !== null && usedPercent >= 100) ||
    hasDepletedQuotaWindow ||
    hasDepletedRawHeaderQuotaWindow(headerSnapshot) ||
    (usedPercent === null && getHeaderSnapshotRecoverAtMs(headerSnapshot) !== null) ||
    Boolean(headerSnapshot.response_metadata?.quota?.rate_limit_reached_type);
  if (hasQuotaLimitOutcome) return true;

  if (isProviderUsageQuotaLimited(headerSnapshot.response_metadata?.provider_usage, nowMs)) {
    return true;
  }

  return (
    hasUsageHeaderQuotaSignal(headerSnapshot) ||
    isHealthyProviderUsage(headerSnapshot.response_metadata?.provider_usage, nowMs)
  );
};

type AuthFileCredentialEvidenceDirection = 'positive' | 'negative';

type AuthFileCredentialEvidence = {
  direction: AuthFileCredentialEvidenceDirection;
  observedAtMs: number | null;
  statusCode: number | null;
};

export const isAuthFileInspectionAuthenticationFailure = (
  inspection: AuthFileCodexInspectionSnapshot | undefined
): boolean => {
  if (!inspection) return false;
  const action =
    typeof inspection.action === 'string' ? inspection.action.trim().toLowerCase() : '';
  const statusCode = normalizeNumber(inspection.statusCode);
  const errorKind = typeof inspection.errorKind === 'string' ? inspection.errorKind.trim() : '';
  if (statusCode === 499) return false;
  return action === 'reauth' || statusCode === 401 || isObservedAuthError('', errorKind);
};

const classifyInspectionCredentialEvidence = (
  inspection: AuthFileCodexInspectionSnapshot | undefined
): AuthFileCredentialEvidenceDirection | null => {
  if (!inspection) return null;
  const action =
    typeof inspection.action === 'string' ? inspection.action.trim().toLowerCase() : '';
  const statusCode = normalizeNumber(inspection.statusCode);
  const errorKind = typeof inspection.errorKind === 'string' ? inspection.errorKind.trim() : '';
  if (statusCode === 499) return null;

  if (hasActiveCodexInspectionAuthenticationFailure(inspection)) {
    return 'negative';
  }
  if (
    inspection.isQuota === true ||
    statusCode === 402 ||
    statusCode === 429 ||
    (statusCode !== null && statusCode >= 200 && statusCode < 400) ||
    action === 'enable' ||
    /(?:^|_)healthy$/i.test(errorKind)
  ) {
    return 'positive';
  }
  return null;
};

const isQualifiedInspectionCredentialStatusSource = (
  inspection: AuthFileCodexInspectionSnapshot | undefined
): boolean => classifyInspectionCredentialEvidence(inspection) !== null;

const HEALTHY_CREDENTIAL_STATE_VALUES = new Set([
  'active',
  'available',
  'enabled',
  'healthy',
  'ok',
  'ready',
  'success',
  'successful',
]);

const readCredentialText = (...values: unknown[]): string =>
  values
    .map((value) => (typeof value === 'string' ? value.trim().toLowerCase() : ''))
    .filter(Boolean)
    .join(' ');

const getAuthFileCredentialEvidence = (file: AuthFileItem): AuthFileCredentialEvidence | null => {
  const statusCode = getAuthFileCredentialStatusCode(file);
  const statusValues = [
    file.statusMessage,
    file['status_message'],
    file.status,
    file.state,
    file.error,
  ];
  const statusText = readCredentialText(...statusValues);
  const observedAtMs = getAuthFileStatusObservedAtMs(file);
  if (statusCode === 499) return null;
  if (statusCode === 401 || isObservedAuthError('', statusText)) {
    return { direction: 'negative', observedAtMs, statusCode };
  }
  const hasHealthyState = statusValues.some(
    (value) =>
      typeof value === 'string' && HEALTHY_CREDENTIAL_STATE_VALUES.has(value.trim().toLowerCase())
  );
  if (
    (statusCode !== null && statusCode >= 200 && statusCode < 400) ||
    statusCode === 402 ||
    statusCode === 429 ||
    hasHealthyState
  ) {
    return { direction: 'positive', observedAtMs, statusCode };
  }
  return null;
};

const getInspectionCredentialEvidence = (
  inspection: AuthFileCodexInspectionSnapshot | undefined
): AuthFileCredentialEvidence | null => {
  const direction = classifyInspectionCredentialEvidence(inspection);
  if (!inspection || direction === null) return null;
  return {
    direction,
    observedAtMs: readFiniteTimestamp(inspection.inspectionAtMs),
    statusCode: normalizeNumber(inspection.statusCode),
  };
};

const getQuotaCredentialEvidence = (
  file: AuthFileItem,
  quota: CodexQuotaState | undefined
): AuthFileCredentialEvidence | null => {
  if (!quota) return null;
  const statusCode = normalizeNumber(quota.errorStatus) ?? readHttpStatusCodeFromText(quota.error);
  const errorText = typeof quota.error === 'string' ? quota.error.trim() : '';
  const failureAtMs = Math.max(
    readFiniteTimestamp(quota.failedAtMs) ?? 0,
    readFiniteTimestamp(quota.fetchedAtMs) ?? 0
  );
  if (statusCode === 499) return null;
  if (statusCode === 401 || isObservedAuthError('', errorText)) {
    return {
      direction: 'negative',
      observedAtMs: failureAtMs > 0 ? failureAtMs : null,
      statusCode,
    };
  }
  if (
    (quota.status === 'success' && activeCodexQuotaMatchesAuthFile(file, quota)) ||
    statusCode === 402 ||
    statusCode === 429 ||
    isObservedQuotaLimitError('', errorText)
  ) {
    return {
      direction: 'positive',
      observedAtMs:
        readFiniteTimestamp(quota.fetchedAtMs) ?? (failureAtMs > 0 ? failureAtMs : null),
      statusCode,
    };
  }
  return null;
};

const getHeaderCredentialEvidence = (
  headerSnapshot: UsageHeaderSnapshot | undefined,
  nowMs: number,
  isCodex: boolean
): AuthFileCredentialEvidence | null => {
  if (!headerSnapshot) return null;
  const errorKind = getHeaderSnapshotErrorKind(headerSnapshot);
  const errorCode = getHeaderSnapshotErrorCode(headerSnapshot);
  const authorizationError =
    headerSnapshot.response_metadata?.errors?.authorization_error?.trim() ?? '';
  const statusCode = readHttpStatusCodeFromText(`${errorKind} ${errorCode} ${authorizationError}`);
  const observedAtMs = readFiniteTimestamp(headerSnapshot.timestamp_ms);
  if (statusCode === 499) return null;
  if (authorizationError || isObservedAuthError(errorKind, `${errorCode} ${authorizationError}`)) {
    return { direction: 'negative', observedAtMs, statusCode };
  }
  // A scoped Codex response can legitimately report its own quota state. It
  // must not become positive credential evidence or suppress a real account
  // failure; explicit authentication errors above remain credential-wide.
  if (isCodex && !isMainCodexHeaderQuota(headerSnapshot)) return null;
  if (isQualifiedHeaderCredentialStatusSource(headerSnapshot, nowMs)) {
    return { direction: 'positive', observedAtMs, statusCode };
  }
  return null;
};

const selectCurrentAuthenticationFailure = (
  evidences: Array<AuthFileCredentialEvidence | null>
): AuthFileCredentialEvidence | null => {
  const availableEvidence = evidences.filter(
    (evidence): evidence is AuthFileCredentialEvidence => evidence !== null
  );
  const negativeEvidence = availableEvidence.filter(
    (evidence) => evidence.direction === 'negative'
  );
  const unknownNegativeEvidence = negativeEvidence.filter(
    (evidence) => evidence.observedAtMs === null
  );
  if (unknownNegativeEvidence.length > 0) {
    return (
      unknownNegativeEvidence.find((evidence) => evidence.statusCode === 401) ??
      unknownNegativeEvidence[0]
    );
  }

  const latestNegativeAtMs = Math.max(
    0,
    ...negativeEvidence.map((evidence) => evidence.observedAtMs ?? 0)
  );
  if (latestNegativeAtMs <= 0) return null;
  const latestPositiveAtMs = Math.max(
    0,
    ...availableEvidence
      .filter((evidence) => evidence.direction === 'positive')
      .map((evidence) => evidence.observedAtMs ?? 0)
  );
  if (latestPositiveAtMs > latestNegativeAtMs) return null;

  const latestNegativeEvidence = negativeEvidence.filter(
    (evidence) => evidence.observedAtMs === latestNegativeAtMs
  );
  return (
    latestNegativeEvidence.find((evidence) => evidence.statusCode === 401) ??
    latestNegativeEvidence[0] ??
    null
  );
};

const hasCurrentAuthFileRawStatusWarning = (
  file: AuthFileItem,
  evidences: Array<AuthFileCredentialEvidence | null>,
  currentAuthenticationFailure: AuthFileCredentialEvidence | null
): boolean => {
  const statusMessage = getAuthFileStatusMessage(file);
  if (!statusMessage || isHealthyAuthFileStatusMessage(statusMessage)) return false;

  const statusCode =
    getAuthFileCredentialStatusCode(file) ?? readHttpStatusCodeFromText(statusMessage);
  const statusText = readCredentialText(statusMessage, file.status, file.state, file.error);
  if (statusCode === 401 || isObservedAuthError('', statusText)) {
    return currentAuthenticationFailure !== null;
  }

  const observedAtMs = getAuthFileStatusObservedAtMs(file);
  const latestPositiveAtMs = Math.max(
    0,
    ...evidences
      .filter(
        (evidence): evidence is AuthFileCredentialEvidence => evidence?.direction === 'positive'
      )
      .map((evidence) => evidence.observedAtMs ?? 0)
  );
  const supersededByPositiveEvidence = observedAtMs !== null && latestPositiveAtMs > observedAtMs;
  if (statusCode === 402 || statusCode === 429 || isObservedQuotaLimitError('', statusText)) {
    return !supersededByPositiveEvidence;
  }
  if (statusCode !== null && statusCode >= 200 && statusCode < 400) return false;
  if (isNeutralAuthFileRuntimeStatus(statusCode, statusText)) return false;
  return !supersededByPositiveEvidence;
};

export const sanitizeSupersededAuthQuotaState = (
  quota: CodexQuotaState | undefined,
  newerSuccessfulRequestAtMs: unknown,
  options: { allowUnknownFailureTimestamp?: boolean } = {}
): CodexQuotaState | undefined => {
  if (!quota) return quota;
  const successfulRequestAtMs = readFiniteTimestamp(newerSuccessfulRequestAtMs);
  if (successfulRequestAtMs === null) return quota;

  const quotaFailureAtMs = Math.max(
    readFiniteTimestamp(quota.failedAtMs) ?? 0,
    readFiniteTimestamp(quota.fetchedAtMs) ?? 0
  );
  const supersedesQuotaFailure =
    quota.status === 'error' &&
    ((quotaFailureAtMs > 0 && successfulRequestAtMs > quotaFailureAtMs) ||
      (options.allowUnknownFailureTimestamp === true && quotaFailureAtMs === 0)) &&
    (quota.errorStatus === 401 ||
      readHttpStatusCodeFromText(quota.error) === 401 ||
      isObservedAuthError('', quota.error ?? ''));
  const observedAtMs = readFiniteTimestamp(quota.observedAtMs);
  const supersedesObservedAuth =
    isObservedAuthError(quota.observedErrorKind ?? '', quota.observedErrorCode ?? '') &&
    ((observedAtMs !== null && successfulRequestAtMs > observedAtMs) ||
      (options.allowUnknownFailureTimestamp === true && observedAtMs === null));
  if (!supersedesQuotaFailure && !supersedesObservedAuth) return quota;

  return {
    ...quota,
    ...(supersedesQuotaFailure
      ? {
          status: 'success' as const,
          error: undefined,
          errorStatus: undefined,
          ...(quota.windows.length === 0 && quota.quotaInventoryObserved === undefined
            ? { quotaInventoryObserved: false }
            : {}),
        }
      : {}),
    ...(supersedesObservedAuth
      ? {
          observedErrorKind: sanitizeSupersededAuthErrorValue(quota.observedErrorKind),
          observedErrorCode: sanitizeSupersededAuthErrorValue(quota.observedErrorCode),
        }
      : {}),
  };
};

export const sanitizeSupersededAuthHeaderSnapshot = (
  headerSnapshot: UsageHeaderSnapshot | undefined,
  newerSuccessfulRequestAtMs: unknown
): UsageHeaderSnapshot | undefined => {
  const headerAtMs = readFiniteTimestamp(headerSnapshot?.timestamp_ms);
  const successfulRequestAtMs = readFiniteTimestamp(newerSuccessfulRequestAtMs);
  if (headerAtMs === null || successfulRequestAtMs === null || successfulRequestAtMs < headerAtMs) {
    return headerSnapshot;
  }

  if (!headerSnapshot) return headerSnapshot;
  const nestedErrors = headerSnapshot.response_metadata?.errors;
  const hasExplicitAuthorizationError = Boolean(nestedErrors?.authorization_error?.trim());
  const errorKind = getHeaderSnapshotErrorKind(headerSnapshot);
  const errorCode = getHeaderSnapshotErrorCode(headerSnapshot);
  if (!isObservedAuthError(errorKind, errorCode) && !hasExplicitAuthorizationError) {
    return headerSnapshot;
  }

  const sanitizedNestedErrors = nestedErrors
    ? {
        ...nestedErrors,
        kind: sanitizeSupersededAuthErrorValue(nestedErrors.kind),
        code: sanitizeSupersededAuthErrorValue(nestedErrors.code),
        authorization_error: undefined,
        ide_error_code: sanitizeSupersededAuthErrorValue(nestedErrors.ide_error_code),
        ide_root_error_code: sanitizeSupersededAuthErrorValue(nestedErrors.ide_root_error_code),
      }
    : undefined;
  const hasNestedErrorEvidence = Boolean(
    sanitizedNestedErrors &&
    Object.values(sanitizedNestedErrors).some(
      (value) => value !== undefined && value !== null && value !== ''
    )
  );
  const sanitizedSnapshot: UsageHeaderSnapshot = {
    ...headerSnapshot,
    header_error_kind: sanitizeSupersededAuthErrorValue(headerSnapshot.header_error_kind),
    header_error_code: sanitizeSupersededAuthErrorValue(headerSnapshot.header_error_code),
    response_metadata: headerSnapshot.response_metadata
      ? {
          ...headerSnapshot.response_metadata,
          errors: hasNestedErrorEvidence ? sanitizedNestedErrors : undefined,
        }
      : undefined,
  };
  const hasRemainingStatusEvidence = Boolean(
    getHeaderSnapshotErrorKind(sanitizedSnapshot) ||
    getHeaderSnapshotErrorCode(sanitizedSnapshot) ||
    getHeaderSnapshotUsedPercent(sanitizedSnapshot) !== null ||
    getHeaderSnapshotRecoverAtMs(sanitizedSnapshot) !== null ||
    getHeaderSnapshotTraceId(sanitizedSnapshot) ||
    sanitizedSnapshot.header_quota_plan_type ||
    sanitizedSnapshot.response_metadata?.quota ||
    sanitizedSnapshot.response_metadata?.rate_limit ||
    sanitizedSnapshot.response_metadata?.provider_usage ||
    hasNestedErrorEvidence
  );
  return hasRemainingStatusEvidence ? sanitizedSnapshot : undefined;
};

export const getFreshAuthFileCodexStatusSources = (
  file: AuthFileItem,
  quota: CodexQuotaState | undefined,
  inspection?: AuthFileCodexInspectionSnapshot,
  headerSnapshot?: UsageHeaderSnapshot,
  newerSuccessfulRequestAtMs?: number | null,
  nowMs = Date.now()
): AuthFileCodexStatusSources => {
  const successfulQuotaAtMs =
    quota?.status === 'success' &&
    quota.observedFromUsageHeaders !== true &&
    activeCodexQuotaMatchesAuthFile(file, quota)
      ? readFiniteTimestamp(quota.fetchedAtMs)
      : null;
  const latestHealthyEvidenceAtMs = Math.max(
    readFiniteTimestamp(newerSuccessfulRequestAtMs) ?? 0,
    successfulQuotaAtMs ?? 0
  );
  const sanitizedHeaderSnapshot = sanitizeSupersededAuthHeaderSnapshot(
    headerSnapshot,
    latestHealthyEvidenceAtMs || undefined
  );
  const matchedInspection =
    inspection?.provider &&
    normalizeProviderKey(inspection.provider) !==
      normalizeProviderKey(file.type ?? file.provider ?? '')
      ? undefined
      : inspection;
  const newerInspectionSourceAtMs = Math.max(
    isQualifiedHeaderCredentialStatusSource(sanitizedHeaderSnapshot, nowMs)
      ? (readFiniteTimestamp(sanitizedHeaderSnapshot?.timestamp_ms) ?? 0)
      : 0,
    matchedInspection?.isQuota === true ? 0 : (readFiniteTimestamp(newerSuccessfulRequestAtMs) ?? 0)
  );
  const effectiveInspection = shouldSuppressOlderCodexStatusSource(
    file,
    quota,
    matchedInspection?.inspectionAtMs,
    newerInspectionSourceAtMs || undefined
  )
    ? undefined
    : matchedInspection;
  const quotaForHeaderFreshness =
    quota?.status === 'success' && quota.quotaInventoryObserved === false ? undefined : quota;
  return {
    inspection: effectiveInspection,
    headerSnapshot: shouldSuppressOlderCodexStatusSource(
      file,
      quotaForHeaderFreshness,
      sanitizedHeaderSnapshot?.timestamp_ms,
      isQualifiedInspectionCredentialStatusSource(effectiveInspection)
        ? effectiveInspection?.inspectionAtMs
        : undefined
    )
      ? undefined
      : sanitizedHeaderSnapshot,
  };
};

export const getAuthFileCodexStatus = (
  file: AuthFileItem,
  quota?: CodexQuotaState,
  inspection?: AuthFileCodexInspectionSnapshot,
  headerSnapshot?: UsageHeaderSnapshot,
  nowMsOrOptions: number | AuthFileCodexStatusOptions = Date.now(),
  optionsOverride?: AuthFileCodexStatusOptions
): AuthFileCodexStatusSummary => {
  const nowMs = typeof nowMsOrOptions === 'number' ? nowMsOrOptions : Date.now();
  const options = typeof nowMsOrOptions === 'number' ? (optionsOverride ?? {}) : nowMsOrOptions;
  const provider = normalizeProviderKey(file.type ?? file.provider ?? '');
  const isCodex = isCodexAuthFile(file);
  const isXai = provider === 'xai';
  if (!isCodex && !isXai) {
    const credentialEvidence = getAuthFileCredentialEvidence(file);
    const credentialEvidences = [credentialEvidence];
    const currentAuthenticationFailure = selectCurrentAuthenticationFailure(credentialEvidences);
    return {
      isCodex: false,
      isHttp401: false,
      needsReauth: false,
      isQuotaLimited: false,
      isUnknownQuotaLimited: false,
      isFiveHourLimited: false,
      isWeeklyLimited: false,
      isMonthlyLimited: false,
      hasDisabledRecoveryReset: false,
      fiveHourResetLabel: null,
      fiveHourResetAtMs: null,
      fiveHourResetAccuracy: 'unknown',
      weeklyResetLabel: null,
      weeklyResetAtMs: null,
      weeklyResetAccuracy: 'unknown',
      monthlyResetLabel: null,
      monthlyResetAtMs: null,
      monthlyResetAccuracy: 'unknown',
      recoveryResetLabel: null,
      recoveryResetAtMs: null,
      recoveryResetAccuracy: 'unknown',
      fiveHourUsedPercent: null,
      weeklyUsedPercent: null,
      monthlyUsedPercent: null,
      hasRawStatusWarning: hasCurrentAuthFileRawStatusWarning(
        file,
        credentialEvidences,
        currentAuthenticationFailure
      ),
      badges: [],
    };
  }

  const fiveHourWindow = findCodexFiveHourQuotaWindow(quota);
  const weeklyWindow = findCodexWeeklyQuotaWindow(quota);
  const monthlyWindow = findCodexMonthlyQuotaWindow(quota);
  const fiveHourUsedPercent = normalizeNumber(fiveHourWindow?.usedPercent);
  const weeklyWindowUsedPercent = normalizeNumber(weeklyWindow?.usedPercent);
  const monthlyWindowUsedPercent = normalizeNumber(monthlyWindow?.usedPercent);
  const inspectionUsedPercent =
    inspection?.isQuota === true ? normalizeNumber(inspection?.usedPercent) : null;
  const action = typeof inspection?.action === 'string' ? inspection.action : '';
  const accountHeaderSnapshot =
    !isCodex || isMainCodexHeaderQuota(headerSnapshot) ? headerSnapshot : undefined;
  const providerUsage = accountHeaderSnapshot?.response_metadata?.provider_usage;
  const observedProviderUsageLimited = isProviderUsageQuotaLimited(providerUsage, nowMs);
  const observedUsedPercent = getHeaderSnapshotUsedPercent(accountHeaderSnapshot);
  const observedRecoverAtMS =
    getHeaderSnapshotRecoverAtMs(accountHeaderSnapshot) ??
    (observedProviderUsageLimited ? normalizeNumber(providerUsage?.recover_at_ms) : null);
  const observedRecoverLabel = formatObservedRecoverLabel(observedRecoverAtMS);
  const observedErrorKind = getHeaderSnapshotErrorKind(accountHeaderSnapshot);
  const observedErrorCode = getHeaderSnapshotErrorCode(accountHeaderSnapshot);
  const observedTraceID = getHeaderSnapshotTraceId(accountHeaderSnapshot);
  const observedAccountQuotaErrorKind = getHeaderSnapshotErrorKind(accountHeaderSnapshot);
  const observedAccountQuotaErrorCode = getHeaderSnapshotErrorCode(accountHeaderSnapshot);
  const observedReachedWindowKind = getHeaderSnapshotReachedWindowKind(accountHeaderSnapshot);
  const observedSummaryWindowKind = getHeaderSnapshotSummaryWindowKind(accountHeaderSnapshot);
  const observedRateLimitReachedType =
    typeof accountHeaderSnapshot?.response_metadata?.quota?.rate_limit_reached_type === 'string'
      ? accountHeaderSnapshot.response_metadata.quota.rate_limit_reached_type.trim()
      : '';
  const observedUnknownUsedPercent = getHeaderSnapshotWindowUsedPercent(
    accountHeaderSnapshot,
    'unknown'
  );
  const observedQuotaLimited =
    (observedUsedPercent !== null && observedUsedPercent >= 100) ||
    (observedUnknownUsedPercent !== null && observedUnknownUsedPercent >= 100) ||
    observedProviderUsageLimited ||
    Boolean(observedRateLimitReachedType) ||
    isObservedQuotaLimitError(observedAccountQuotaErrorKind, observedAccountQuotaErrorCode);
  const observedLimitWindowKind =
    observedReachedWindowKind ??
    (observedUsedPercent !== null && observedUsedPercent >= 100 ? observedSummaryWindowKind : null);
  const getObservedWindowUsedPercent = (windowKind: 'five_hour' | 'weekly' | 'monthly') =>
    getHeaderSnapshotWindowUsedPercent(accountHeaderSnapshot, windowKind) ??
    (observedLimitWindowKind === windowKind ? observedUsedPercent : null);
  const observedFiveHourUsedPercent = getObservedWindowUsedPercent('five_hour');
  const observedWeeklyUsedPercent = getObservedWindowUsedPercent('weekly');
  const observedMonthlyUsedPercent = getObservedWindowUsedPercent('monthly');
  const observedFiveHourUnderLimit = isUnderQuotaLimit(observedFiveHourUsedPercent);
  const observedWeeklyUnderLimit = isUnderQuotaLimit(observedWeeklyUsedPercent);
  const observedMonthlyUnderLimit = isUnderQuotaLimit(observedMonthlyUsedPercent);
  const observedSpecificLimitSuppressed =
    (observedLimitWindowKind === 'five_hour' && observedFiveHourUnderLimit) ||
    (observedLimitWindowKind === 'weekly' && observedWeeklyUnderLimit) ||
    (observedLimitWindowKind === 'monthly' && observedMonthlyUnderLimit);
  const observedFiveHourLimited =
    observedQuotaLimited && observedLimitWindowKind === 'five_hour' && !observedFiveHourUnderLimit;
  const observedWeeklyLimited =
    observedQuotaLimited && observedLimitWindowKind === 'weekly' && !observedWeeklyUnderLimit;
  const observedMonthlyLimited =
    observedQuotaLimited && observedLimitWindowKind === 'monthly' && !observedMonthlyUnderLimit;
  const observedQuotaLimitedStatus = observedQuotaLimited && !observedSpecificLimitSuppressed;
  const monthlyUsedPercent =
    monthlyWindowUsedPercent ?? (monthlyWindow ? inspectionUsedPercent : null);
  const longWindowUsedPercent = weeklyWindowUsedPercent ?? monthlyUsedPercent;
  const weeklyUsedPercent =
    weeklyWindowUsedPercent ?? (!monthlyWindow ? inspectionUsedPercent : null);
  const fiveHourReset = resolveCodexStatusReset(fiveHourWindow);
  const weeklyReset = resolveCodexStatusReset(weeklyWindow);
  const monthlyReset = resolveCodexStatusReset(monthlyWindow);
  const fiveHourResetLabel = fiveHourReset.label;
  const weeklyResetLabel = weeklyReset.label;
  const monthlyResetLabel = monthlyReset.label;
  const credentialEvidences = [
    options.ignoreRawStatusCode ? null : getAuthFileCredentialEvidence(file),
    getInspectionCredentialEvidence(inspection),
    getQuotaCredentialEvidence(file, quota),
    getHeaderCredentialEvidence(headerSnapshot, nowMs, isCodex),
  ];
  const currentAuthenticationFailure = selectCurrentAuthenticationFailure(credentialEvidences);
  const inspectionErrorKind =
    typeof inspection?.errorKind === 'string' ? inspection.errorKind.trim() : '';
  const isHttp401 = currentAuthenticationFailure?.statusCode === 401;
  const needsReauth = currentAuthenticationFailure !== null;
  const effectiveDisabled = options.effectiveDisabled ?? file.disabled === true;
  const inspectionReachedQuota =
    inspection?.isQuota === true &&
    (action === 'disable' ||
      (longWindowUsedPercent !== null && longWindowUsedPercent >= 100) ||
      (effectiveDisabled && action === 'keep'));
  const isWeeklyLimited =
    isCodex &&
    ((weeklyUsedPercent !== null && weeklyUsedPercent >= 100) ||
      (inspectionReachedQuota && !monthlyWindow) ||
      observedWeeklyLimited);
  const isMonthlyLimited =
    isCodex &&
    ((monthlyUsedPercent !== null && monthlyUsedPercent >= 100) ||
      (inspectionReachedQuota && monthlyWindow !== null && !weeklyWindow) ||
      observedMonthlyLimited);
  const isFiveHourLimited =
    isCodex &&
    ((fiveHourUsedPercent !== null && fiveHourUsedPercent >= 100) || observedFiveHourLimited);
  const isUnknownQuotaLimited =
    (isXai && inspectionReachedQuota) ||
    observedProviderUsageLimited ||
    (observedUnknownUsedPercent !== null && observedUnknownUsedPercent >= 100) ||
    (observedQuotaLimitedStatus && !isFiveHourLimited && !isWeeklyLimited && !isMonthlyLimited);
  const isQuotaLimited =
    isFiveHourLimited || isWeeklyLimited || isMonthlyLimited || isUnknownQuotaLimited;
  const recoveryReset =
    (isMonthlyLimited && monthlyResetLabel ? monthlyReset : null) ||
    (isWeeklyLimited && weeklyResetLabel ? weeklyReset : null) ||
    (isFiveHourLimited && fiveHourResetLabel ? fiveHourReset : null) ||
    (observedQuotaLimitedStatus && observedRecoverLabel
      ? {
          label: observedRecoverLabel,
          resetAtMs: observedRecoverAtMS,
          resetAccuracy: 'exact' as const,
        }
      : null);
  const recoveryResetLabel = recoveryReset?.label ?? null;
  const recoveryResetAtMs = recoveryReset?.resetAtMs ?? null;
  const recoveryResetAccuracy = recoveryReset?.resetAccuracy ?? 'unknown';
  const hasDisabledRecoveryReset = effectiveDisabled && recoveryResetLabel !== null;
  const badges: AuthFileCodexStatusBadge[] = [];

  if (needsReauth) {
    badges.push({
      kind: 'reauth',
      tone: 'danger',
      labelKey: isXai
        ? 'auth_files.provider_inspection_badge_reauth'
        : 'auth_files.codex_status_badge_reauth',
      defaultLabel: 'Needs reauth',
      titleKey: isXai
        ? 'auth_files.provider_inspection_badge_reauth_title'
        : 'auth_files.codex_status_badge_reauth_title',
      defaultTitle: 'Latest Codex check returned 401 or suggested reauthorization.',
      labelParams: { provider: isXai ? 'xAI' : 'Codex' },
    });
  }

  if (isFiveHourLimited) {
    badges.push({
      kind: 'five_hour_limited',
      tone: 'warning',
      labelKey: 'auth_files.codex_status_badge_five_hour_limited',
      defaultLabel: '5h quota full',
      titleKey: 'auth_files.codex_status_badge_five_hour_limited_title',
      defaultTitle: 'The Codex 5-hour quota window is at or above the limit.',
    });
  }

  if (isWeeklyLimited) {
    badges.push({
      kind: 'weekly_limited',
      tone: 'warning',
      labelKey: 'auth_files.codex_status_badge_weekly_limited',
      defaultLabel: '7d quota full',
      titleKey: 'auth_files.codex_status_badge_weekly_limited_title',
      defaultTitle: 'The Codex 7-day quota window is at or above the limit.',
    });
  }

  if (isMonthlyLimited) {
    badges.push({
      kind: 'monthly_limited',
      tone: 'warning',
      labelKey: 'auth_files.codex_status_badge_monthly_limited',
      defaultLabel: 'Monthly quota full',
      titleKey: 'auth_files.codex_status_badge_monthly_limited_title',
      defaultTitle: 'The Codex monthly quota window is at or above the limit.',
    });
  }

  if (hasDisabledRecoveryReset && recoveryResetLabel) {
    badges.push({
      kind: 'disabled_with_reset',
      tone: 'info',
      labelKey: 'auth_files.codex_status_badge_disabled_reset',
      defaultLabel: `Restores ${recoveryResetLabel}`,
      titleKey: 'auth_files.codex_status_badge_disabled_reset_title',
      defaultTitle: `This disabled Codex account has a known quota recovery time: ${recoveryResetLabel}`,
      labelParams: { reset: recoveryResetLabel },
    });
  }

  if (isXai && inspectionReachedQuota) {
    badges.push({
      kind: 'observed_quota',
      tone: 'warning',
      labelKey: 'auth_files.provider_inspection_badge_quota',
      defaultLabel: 'Quota unavailable',
      titleKey: 'auth_files.provider_inspection_badge_quota_title',
      defaultTitle: 'Latest xAI inspection reported an exhausted quota or spending limit.',
      labelParams: { provider: 'xAI' },
    });
  } else if (observedQuotaLimitedStatus) {
    badges.push({
      kind: 'observed_quota',
      tone: 'warning',
      labelKey: 'auth_files.codex_status_badge_observed_quota',
      defaultLabel:
        observedUsedPercent !== null
          ? `Observed quota ${Math.round(observedUsedPercent)}%`
          : 'Observed quota issue',
      titleKey: 'auth_files.codex_status_badge_observed_quota_title',
      defaultTitle: [
        'Latest usage response headers reported a Codex quota issue.',
        observedRecoverLabel ? `Recover at: ${observedRecoverLabel}.` : '',
        observedTraceID ? `Trace: ${observedTraceID}.` : '',
      ]
        .filter(Boolean)
        .join(' '),
      labelParams: {
        percent: observedUsedPercent !== null ? Math.round(observedUsedPercent) : '--',
      },
    });
  } else if (observedErrorKind || observedErrorCode) {
    badges.push({
      kind: 'observed_error',
      tone: 'info',
      labelKey: 'auth_files.codex_status_badge_observed_error',
      defaultLabel: 'Observed header error',
      titleKey: 'auth_files.codex_status_badge_observed_error_title',
      defaultTitle: [
        'Latest usage response headers reported an error.',
        [observedErrorKind, observedErrorCode].filter(Boolean).join(' / '),
        observedTraceID ? `Trace: ${observedTraceID}.` : '',
      ]
        .filter(Boolean)
        .join(' '),
    });
  }

  if (
    isXai &&
    !inspectionReachedQuota &&
    inspectionErrorKind &&
    inspectionErrorKind !== 'billing_healthy' &&
    inspectionErrorKind !== 'billing_partial' &&
    inspectionErrorKind !== 'inference_healthy' &&
    inspectionErrorKind !== 'identity_healthy' &&
    inspectionErrorKind !== 'official_api_healthy' &&
    !needsReauth
  ) {
    const issueTitleKey = getXaiProbeIssueKey(inspectionErrorKind);
    badges.push({
      kind: 'inspection_error',
      tone: 'info',
      labelKey: 'auth_files.provider_inspection_badge_error',
      defaultLabel: 'Inspection warning',
      titleKey: issueTitleKey ?? 'auth_files.provider_inspection_badge_error_title',
      defaultTitle: 'The latest xAI inspection found an issue. Review the inspection details.',
      labelParams: issueTitleKey ? undefined : { provider: 'xAI' },
    });
  }

  return {
    isCodex,
    isHttp401,
    needsReauth,
    isQuotaLimited,
    isUnknownQuotaLimited,
    isFiveHourLimited,
    isWeeklyLimited,
    isMonthlyLimited,
    hasDisabledRecoveryReset,
    fiveHourResetLabel,
    fiveHourResetAtMs: fiveHourReset.resetAtMs,
    fiveHourResetAccuracy: fiveHourReset.resetAccuracy,
    weeklyResetLabel,
    weeklyResetAtMs: weeklyReset.resetAtMs,
    weeklyResetAccuracy: weeklyReset.resetAccuracy,
    monthlyResetLabel,
    monthlyResetAtMs: monthlyReset.resetAtMs,
    monthlyResetAccuracy: monthlyReset.resetAccuracy,
    recoveryResetLabel,
    recoveryResetAtMs,
    recoveryResetAccuracy,
    fiveHourUsedPercent,
    weeklyUsedPercent,
    monthlyUsedPercent,
    hasRawStatusWarning: hasCurrentAuthFileRawStatusWarning(
      file,
      credentialEvidences,
      currentAuthenticationFailure
    ),
    badges,
  };
};

export const authFileMatchesCodexStatusFilter = (
  status: AuthFileCodexStatusSummary,
  filter: AuthFileCodexStatusFilter
): boolean => {
  if (filter === 'all') return true;
  if (filter === 'http_401') return status.isHttp401;
  if (filter === 'reauth') return status.needsReauth || status.isHttp401;
  if (filter === 'quota_limited') return status.isQuotaLimited;
  if (filter === 'five_hour_limited') return status.isFiveHourLimited;
  if (filter === 'weekly_limited') return status.isWeeklyLimited;
  if (filter === 'monthly_limited') return status.isMonthlyLimited;
  if (filter === 'disabled_with_reset') return status.hasDisabledRecoveryReset;
  return true;
};
export const hasAuthFileCodexProblemBadge = (
  status: AuthFileCodexStatusSummary | null | undefined
): boolean => status?.badges.some((badge) => badge.kind !== 'observed_error') ?? false;

export const hasAuthFileStatusProblem = (
  status: AuthFileCodexStatusSummary | null | undefined
): boolean => Boolean(status?.hasRawStatusWarning) || hasAuthFileCodexProblemBadge(status);
