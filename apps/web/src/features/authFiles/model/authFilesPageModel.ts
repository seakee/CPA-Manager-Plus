import type { TFunction } from 'i18next';
import type { AuthFileItem, CodexQuotaState } from '@/types';
import type { UsageHeaderSnapshot } from '@/services/api/usageService';
import {
  normalizePlanType,
  resolveCodexChatgptAccountId,
  resolveCodexPlanType,
} from '@/utils/quota';
import {
  getHeaderSnapshotErrorCode,
  getHeaderSnapshotErrorKind,
  getHeaderSnapshotPlanType,
  getHeaderSnapshotReachedWindowKind,
  getHeaderSnapshotRecoverAtMs,
  getHeaderSnapshotSummaryWindowKind,
  getHeaderSnapshotTraceId,
  getHeaderSnapshotUsedPercent,
  getHeaderSnapshotWindowUsedPercent,
} from '@/utils/usageHeaderSnapshots';
import {
  getTypeLabel,
  isRuntimeOnlyAuthFile,
  normalizeProviderKey,
  parsePriorityValue,
} from '@/features/authFiles/constants';

export const easePower3Out = (progress: number) => 1 - (1 - progress) ** 4;
export const easePower2In = (progress: number) => progress ** 3;
export const BATCH_BAR_BASE_TRANSFORM = 'translateX(-50%)';
export const BATCH_BAR_HIDDEN_TRANSFORM = 'translateX(-50%) translateY(56px)';
export const DEFAULT_REGULAR_PAGE_SIZE = 9;
export const DEFAULT_COMPACT_PAGE_SIZE = 12;

const escapeWildcardSearchSegment = (value: string) => value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

export const buildWildcardSearch = (value: string): RegExp | null => {
  if (!value.includes('*')) return null;
  const pattern = value.split('*').map(escapeWildcardSearchSegment).join('.*');
  return new RegExp(pattern, 'i');
};

const PREMIUM_CODEX_PLAN_TYPES = new Set(['pro', 'prolite', 'pro-lite', 'pro_lite']);
const CODEX_FIVE_HOUR_WINDOW_SECONDS = 18_000;
const CODEX_WEEKLY_WINDOW_SECONDS = 604_800;
const CODEX_MONTHLY_WINDOW_SECONDS = 2_592_000;
const UNKNOWN_AUTH_INDEX_KEY = '-';
const AUTH_FILE_SELECTION_KEY_SEPARATOR = '\u0000';

export const AUTH_FILES_CODEX_STATUS_FILTERS = [
  'all',
  // Legacy URL/query value. The Auth Files UI now presents 401 as "needs reauth".
  'http_401',
  'reauth',
  'quota_limited',
  'five_hour_limited',
  'weekly_limited',
  'monthly_limited',
  'disabled_with_reset',
] as const;
export const AUTH_FILES_CODEX_PLAN_FILTERS = [
  'all',
  'free',
  'plus',
  'team',
  'prolite',
  'pro',
  'unknown',
] as const;

export type AuthFilesCodexStatusFilter = (typeof AUTH_FILES_CODEX_STATUS_FILTERS)[number];
export type AuthFilesCodexPlanFilter = (typeof AUTH_FILES_CODEX_PLAN_FILTERS)[number];
export type AuthFileCodexStatusBadgeTone = 'danger' | 'warning' | 'info';
export type AuthFileCodexStatusBadgeKind =
  | 'reauth'
  | 'five_hour_limited'
  | 'weekly_limited'
  | 'monthly_limited'
  | 'disabled_with_reset'
  | 'observed_quota'
  | 'observed_error';

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
  weeklyResetLabel: string | null;
  monthlyResetLabel: string | null;
  recoveryResetLabel: string | null;
  fiveHourUsedPercent: number | null;
  weeklyUsedPercent: number | null;
  monthlyUsedPercent: number | null;
  badges: AuthFileCodexStatusBadge[];
};

export type AuthFileCodexInspectionSnapshot = {
  fileName: string;
  provider?: string | null;
  authIndex?: string | number | null;
  statusCode?: number | string | null;
  action?: string | null;
  usedPercent?: number | string | null;
  isQuota?: boolean | null;
  errorKind?: string | null;
  inspectionAtMs?: number | null;
};

export type AuthFileCodexStatusSources = {
  inspection?: AuthFileCodexInspectionSnapshot;
  headerSnapshot?: UsageHeaderSnapshot;
};

export type AuthFilePatchTarget = {
  name: string;
  authIndex?: string | number | null;
};

const CODEX_STATUS_FILTER_SET = new Set<AuthFilesCodexStatusFilter>(
  AUTH_FILES_CODEX_STATUS_FILTERS
);
const CODEX_PLAN_FILTER_SET = new Set<AuthFilesCodexPlanFilter>(AUTH_FILES_CODEX_PLAN_FILTERS);

export const compareAuthFileName = (left: { name: string }, right: { name: string }) =>
  left.name.localeCompare(right.name, undefined, { numeric: true, sensitivity: 'base' });

const normalizeNumber = (value: unknown): number | null => {
  if (typeof value === 'number') return Number.isFinite(value) ? value : null;
  if (typeof value !== 'string') return null;
  const trimmed = value.trim();
  if (!trimmed) return null;
  const parsed = Number(trimmed);
  return Number.isFinite(parsed) ? parsed : null;
};

const formatObservedRecoverLabel = (value: number | null) => {
  if (!value) return null;
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return null;
  return date.toLocaleString();
};

const isObservedAuthError = (kind: string, code: string) => {
  const text = `${kind} ${code}`.toLowerCase();
  return (
    text.includes('auth') ||
    text.includes('unauthorized') ||
    text.includes('invalid') ||
    text.includes('expired') ||
    text.includes('revoked')
  );
};

const isObservedQuotaLimitError = (kind: string, code: string) => {
  const text = `${kind} ${code}`.toLowerCase();
  return (
    text.includes('usage_limit_reached') ||
    text.includes('quota_exceeded') ||
    text.includes('quota_depleted') ||
    text.includes('credits_depleted')
  );
};

const isUnderQuotaLimit = (value: number | null): boolean => value !== null && value < 100;

const normalizeAuthIndexKey = (value: unknown): string => {
  if (value === undefined || value === null) return UNKNOWN_AUTH_INDEX_KEY;
  const normalized = String(value).trim();
  return normalized || UNKNOWN_AUTH_INDEX_KEY;
};

const readFiniteTimestamp = (value: unknown): number | null =>
  typeof value === 'number' && Number.isFinite(value) ? value : null;

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
  const windows = quota?.windows ?? [];
  return (
    windows.find(preferredMatch) ??
    windows.find(
      (window) => normalizeWindowSeconds(window.limitWindowSeconds) === limitWindowSeconds
    ) ??
    null
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

export const normalizeAuthFilesCodexStatusFilter = (
  value: unknown
): AuthFilesCodexStatusFilter | null => {
  if (value === 'http_401') return 'reauth';
  return CODEX_STATUS_FILTER_SET.has(value as AuthFilesCodexStatusFilter)
    ? (value as AuthFilesCodexStatusFilter)
    : null;
};

export const normalizeAuthFilesCodexPlanFilter = (
  value: unknown
): AuthFilesCodexPlanFilter | null =>
  CODEX_PLAN_FILTER_SET.has(value as AuthFilesCodexPlanFilter)
    ? (value as AuthFilesCodexPlanFilter)
    : null;

export const getAuthFileCodexInspectionKey = (fileName: string, authIndex?: unknown) =>
  `${fileName}::${normalizeAuthIndexKey(authIndex)}`;

export const getAuthFileCodexInspectionKeyForFile = (file: AuthFileItem) =>
  getAuthFileCodexInspectionKey(file.name, readAuthFileAuthIndex(file));

export const getAuthFileSelectionKey = (file: AuthFileItem): string =>
  [file.name, normalizeAuthIndexKey(readAuthFileAuthIndex(file))].join(
    AUTH_FILE_SELECTION_KEY_SEPARATOR
  );

export const getAuthFileNameFromSelectionKey = (key: string): string =>
  key.split(AUTH_FILE_SELECTION_KEY_SEPARATOR, 1)[0] ?? '';

export const getAuthFilePatchTarget = (file: AuthFileItem): AuthFilePatchTarget => {
  const authIndex = readAuthFileAuthIndex(file);
  return authIndex === null || authIndex === undefined || String(authIndex).trim() === ''
    ? { name: file.name }
    : { name: file.name, authIndex };
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
    map.set(getAuthFileCodexInspectionKey(item.fileName, item.authIndex), item);
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
  if (!quota.authFileKey) return readAuthFileAuthIndex(file) == null ? quota : undefined;
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

export const getFreshAuthFileCodexStatusSources = (
  file: AuthFileItem,
  quota: CodexQuotaState | undefined,
  inspection?: AuthFileCodexInspectionSnapshot,
  headerSnapshot?: UsageHeaderSnapshot
): AuthFileCodexStatusSources => ({
  inspection:
    inspection?.provider &&
    normalizeProviderKey(inspection.provider) !== normalizeProviderKey(file.type ?? file.provider ?? '')
      ? undefined
      : shouldSuppressOlderCodexStatusSource(
    file,
    quota,
    inspection?.inspectionAtMs,
    headerSnapshot?.timestamp_ms
  )
        ? undefined
        : inspection,
  headerSnapshot: shouldSuppressOlderCodexStatusSource(
    file,
    quota,
    headerSnapshot?.timestamp_ms,
    inspection?.inspectionAtMs
  )
    ? undefined
    : headerSnapshot,
});

export const getAuthFileCodexStatus = (
  file: AuthFileItem,
  quota?: CodexQuotaState,
  inspection?: AuthFileCodexInspectionSnapshot,
  headerSnapshot?: UsageHeaderSnapshot
): AuthFileCodexStatusSummary => {
  const provider = normalizeProviderKey(file.type ?? file.provider ?? '');
  const isCodex = isCodexAuthFile(file);
  const isXai = provider === 'xai';
  if (!isCodex && !isXai) {
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
      weeklyResetLabel: null,
      monthlyResetLabel: null,
      recoveryResetLabel: null,
      fiveHourUsedPercent: null,
      weeklyUsedPercent: null,
      monthlyUsedPercent: null,
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
  const observedUsedPercent = getHeaderSnapshotUsedPercent(headerSnapshot);
  const observedRecoverAtMS = getHeaderSnapshotRecoverAtMs(headerSnapshot);
  const observedRecoverLabel = formatObservedRecoverLabel(observedRecoverAtMS);
  const observedErrorKind = getHeaderSnapshotErrorKind(headerSnapshot);
  const observedErrorCode = getHeaderSnapshotErrorCode(headerSnapshot);
  const observedTraceID = getHeaderSnapshotTraceId(headerSnapshot);
  const observedReachedWindowKind = getHeaderSnapshotReachedWindowKind(headerSnapshot);
  const observedSummaryWindowKind = getHeaderSnapshotSummaryWindowKind(headerSnapshot);
  const observedRateLimitReachedType =
    typeof headerSnapshot?.response_metadata?.quota?.rate_limit_reached_type === 'string'
      ? headerSnapshot.response_metadata.quota.rate_limit_reached_type.trim()
      : '';
  const observedQuotaLimited =
    (observedUsedPercent !== null && observedUsedPercent >= 100) ||
    Boolean(observedRateLimitReachedType) ||
    isObservedQuotaLimitError(observedErrorKind, observedErrorCode);
  const observedLimitWindowKind =
    observedReachedWindowKind ??
    (observedUsedPercent !== null && observedUsedPercent >= 100 ? observedSummaryWindowKind : null);
  const getObservedWindowUsedPercent = (windowKind: 'five_hour' | 'weekly' | 'monthly') =>
    getHeaderSnapshotWindowUsedPercent(headerSnapshot, windowKind) ??
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
  const fiveHourResetLabel = isKnownResetLabel(fiveHourWindow?.resetLabel)
    ? fiveHourWindow.resetLabel.trim()
    : null;
  const weeklyResetLabel = isKnownResetLabel(weeklyWindow?.resetLabel)
    ? weeklyWindow.resetLabel.trim()
    : null;
  const monthlyResetLabel = isKnownResetLabel(monthlyWindow?.resetLabel)
    ? monthlyWindow.resetLabel.trim()
    : null;
  const statusCode =
    normalizeNumber(inspection?.statusCode) ??
    normalizeNumber(
      file.errorStatus ?? file['error_status'] ?? file.statusCode ?? file['status_code']
    ) ??
    normalizeNumber(quota?.errorStatus);
  const action = typeof inspection?.action === 'string' ? inspection.action : '';
  const inspectionErrorKind =
    typeof inspection?.errorKind === 'string' ? inspection.errorKind.trim() : '';
  const isHttp401 = statusCode === 401;
  const needsReauth =
    action === 'reauth' || isHttp401 || isObservedAuthError(observedErrorKind, observedErrorCode);
  const inspectionReachedQuota =
    inspection?.isQuota === true &&
    (action === 'disable' ||
      (longWindowUsedPercent !== null && longWindowUsedPercent >= 100) ||
      (file.disabled === true && action === 'keep'));
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
    (observedQuotaLimitedStatus && !isFiveHourLimited && !isWeeklyLimited && !isMonthlyLimited);
  const isQuotaLimited =
    isFiveHourLimited || isWeeklyLimited || isMonthlyLimited || isUnknownQuotaLimited;
  const recoveryResetLabel =
    (isMonthlyLimited && monthlyResetLabel) ||
    (isWeeklyLimited && weeklyResetLabel) ||
    (isFiveHourLimited && fiveHourResetLabel) ||
    (observedQuotaLimitedStatus && observedRecoverLabel) ||
    null;
  const hasDisabledRecoveryReset = file.disabled === true && recoveryResetLabel !== null;
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
  } else if (
    isXai &&
    inspectionErrorKind &&
    inspectionErrorKind !== 'billing_healthy' &&
    !needsReauth
  ) {
    badges.push({
      kind: 'observed_error',
      tone: 'info',
      labelKey: 'auth_files.provider_inspection_badge_error',
      defaultLabel: 'Inspection warning',
      titleKey: 'auth_files.provider_inspection_badge_error_title',
      defaultTitle: `Latest xAI inspection reported ${inspectionErrorKind}.`,
      labelParams: { provider: 'xAI', kind: inspectionErrorKind },
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
    weeklyResetLabel,
    monthlyResetLabel,
    recoveryResetLabel,
    fiveHourUsedPercent,
    weeklyUsedPercent,
    monthlyUsedPercent,
    badges,
  };
};

export const authFileMatchesCodexStatusFilter = (
  status: AuthFileCodexStatusSummary,
  filter: AuthFilesCodexStatusFilter
): boolean => {
  if (filter === 'all') return true;
  if (!status.isCodex) return false;
  if (filter === 'http_401') return status.isHttp401;
  if (filter === 'reauth') return status.needsReauth || status.isHttp401;
  if (filter === 'quota_limited') return status.isQuotaLimited;
  if (filter === 'five_hour_limited') return status.isFiveHourLimited;
  if (filter === 'weekly_limited') return status.isWeeklyLimited;
  if (filter === 'monthly_limited') return status.isMonthlyLimited;
  if (filter === 'disabled_with_reset') return status.hasDisabledRecoveryReset;
  return true;
};

const getAuthFileCodexStatusSearchValues = (
  status: AuthFileCodexStatusSummary | undefined,
  t: TFunction
) =>
  status?.badges.flatMap((badge) => [
    badge.kind,
    badge.labelKey,
    badge.defaultLabel,
    t(badge.labelKey, { defaultValue: badge.defaultLabel, ...badge.labelParams }),
    badge.defaultTitle,
    badge.titleKey
      ? t(badge.titleKey, { defaultValue: badge.defaultTitle ?? badge.defaultLabel })
      : null,
  ]) ?? [];

const getAuthFileNoteValue = (file: AuthFileItem): string => {
  const raw = file.note ?? file['note'];
  if (raw === undefined || raw === null) return '';
  return String(raw).trim();
};

export const compareAuthFileNote = (
  left: AuthFileItem,
  right: AuthFileItem,
  direction: 'asc' | 'desc'
) => {
  const leftNote = getAuthFileNoteValue(left);
  const rightNote = getAuthFileNoteValue(right);
  const leftKnown = leftNote.length > 0;
  const rightKnown = rightNote.length > 0;

  if (leftKnown || rightKnown) {
    if (!leftKnown) return 1;
    if (!rightKnown) return -1;
    const diff = leftNote.localeCompare(rightNote, undefined, {
      numeric: true,
      sensitivity: 'base',
    });
    if (diff !== 0) return direction === 'asc' ? diff : -diff;
  }

  return compareAuthFileName(left, right);
};

export const compareAuthFilePriority = (
  left: AuthFileItem,
  right: AuthFileItem,
  direction: 'asc' | 'desc'
) => {
  const leftPriority = parsePriorityValue(left.priority ?? left['priority']);
  const rightPriority = parsePriorityValue(right.priority ?? right['priority']);
  const leftKnown = leftPriority !== undefined;
  const rightKnown = rightPriority !== undefined;

  if (leftKnown || rightKnown) {
    if (!leftKnown) return 1;
    if (!rightKnown) return -1;
    const leftValue = leftPriority ?? 0;
    const rightValue = rightPriority ?? 0;
    const diff = direction === 'desc' ? rightValue - leftValue : leftValue - rightValue;
    if (diff !== 0) return diff;
  }

  return compareAuthFileName(left, right);
};

export const stringifySearchValue = (value: unknown): string[] => {
  if (value === undefined || value === null) return [];
  if (Array.isArray(value)) return value.flatMap(stringifySearchValue);
  if (typeof value === 'string') return value.trim() ? [value] : [];
  if (typeof value === 'number' || typeof value === 'boolean') return [String(value)];
  return [];
};

const getCodexPlanLabel = (planType: string | null | undefined, t: TFunction): string | null => {
  const normalized = normalizePlanType(planType);
  if (!normalized) return null;
  if (normalized === 'pro') return t('codex_quota.plan_pro');
  if (PREMIUM_CODEX_PLAN_TYPES.has(normalized) && normalized !== 'pro') {
    return t('codex_quota.plan_prolite');
  }
  if (normalized === 'plus') return t('codex_quota.plan_plus');
  if (normalized === 'team') return t('codex_quota.plan_team');
  if (normalized === 'free') return t('codex_quota.plan_free');
  return planType || normalized;
};

const getAuthFilePlanType = (
  file: AuthFileItem,
  quota?: CodexQuotaState,
  headerSnapshot?: UsageHeaderSnapshot
): string | null =>
  resolveCodexPlanType(file) ??
  quota?.planType ??
  getHeaderSnapshotPlanType(headerSnapshot) ??
  null;

const getCodexPlanFilterValue = (
  file: AuthFileItem,
  quota?: CodexQuotaState,
  headerSnapshot?: UsageHeaderSnapshot
): AuthFilesCodexPlanFilter | null => {
  const normalized = normalizePlanType(getAuthFilePlanType(file, quota, headerSnapshot));
  if (!normalized) return null;
  if (normalized === 'free') return 'free';
  if (normalized === 'plus') return 'plus';
  if (normalized === 'team') return 'team';
  if (normalized === 'pro') return 'pro';
  if (PREMIUM_CODEX_PLAN_TYPES.has(normalized) && normalized !== 'pro') return 'prolite';
  return null;
};

export const authFileMatchesCodexPlanFilter = (
  file: AuthFileItem,
  quota: CodexQuotaState | undefined,
  filter: AuthFilesCodexPlanFilter,
  headerSnapshot?: UsageHeaderSnapshot
): boolean => {
  if (filter === 'all') return true;
  if (!isCodexAuthFile(file)) return false;

  const planFilterValue = getCodexPlanFilterValue(file, quota, headerSnapshot);
  if (filter === 'unknown') return planFilterValue === null;
  return planFilterValue === filter;
};

export const getAuthFilePlanSortRank = (
  file: AuthFileItem,
  quota?: CodexQuotaState,
  headerSnapshot?: UsageHeaderSnapshot
): number | null => {
  const normalized = normalizePlanType(getAuthFilePlanType(file, quota, headerSnapshot));
  if (!normalized) return null;
  if (normalized === 'pro') return 50;
  if (PREMIUM_CODEX_PLAN_TYPES.has(normalized) && normalized !== 'pro') return 40;
  if (normalized === 'team') return 30;
  if (normalized === 'plus') return 20;
  if (normalized === 'free') return 10;
  return 0;
};

export const getAuthFileSearchValues = (
  file: AuthFileItem,
  t: TFunction,
  quota?: CodexQuotaState,
  codexStatus?: AuthFileCodexStatusSummary,
  headerSnapshot?: UsageHeaderSnapshot,
  planHeaderSnapshot: UsageHeaderSnapshot | undefined = headerSnapshot
) => {
  const planType = getAuthFilePlanType(file, quota, planHeaderSnapshot);
  const planLabel = getCodexPlanLabel(planType, t);
  const accountId = resolveCodexChatgptAccountId(file);
  const type = file.type || file.provider;
  const headerErrorKind = getHeaderSnapshotErrorKind(headerSnapshot);
  const headerErrorCode = getHeaderSnapshotErrorCode(headerSnapshot);
  const headerTraceID = getHeaderSnapshotTraceId(headerSnapshot);
  const headerUsedPercent = getHeaderSnapshotUsedPercent(headerSnapshot);
  const headerRecoverAtMS = getHeaderSnapshotRecoverAtMs(headerSnapshot);

  return [
    file.name,
    file.type,
    file.provider,
    type ? getTypeLabel(t, String(type)) : null,
    file.authIndex,
    file['auth_index'],
    file.status,
    file.state,
    file.statusMessage,
    file['status_message'],
    file.error,
    file.errorStatus,
    file['error_status'],
    quota?.status,
    quota?.error,
    quota?.errorStatus,
    planType,
    planLabel,
    headerErrorKind,
    headerErrorCode,
    headerTraceID,
    headerUsedPercent,
    headerRecoverAtMS,
    accountId,
    getAuthFileCodexStatusSearchValues(codexStatus, t),
  ];
};
