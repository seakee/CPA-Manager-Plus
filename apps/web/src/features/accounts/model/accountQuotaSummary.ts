import type {
  AntigravityQuotaState,
  AuthFileItem,
  ClaudeQuotaState,
  CodexQuotaState,
  KimiQuotaState,
  QuotaResetAccuracy,
  XaiBillingSummary,
  XaiQuotaState,
} from '@/types';
import {
  isValidQuotaResetAtMs,
  parseQuotaResetLabelMs,
  resolveAbsoluteQuotaReset,
} from '@/utils/quota/formatters';
import type { UsageHeaderSnapshot } from '@/services/api/usageService';
import { getAuthFileSelectionKey } from '@/features/authFiles/model/credentialStatus';
import {
  buildObservedCodexQuotaFromHeaderSnapshot,
  getHeaderSnapshotErrorCode,
  getHeaderSnapshotErrorKind,
  getHeaderSnapshotPlanType,
  getHeaderSnapshotTraceId,
  hasUsageHeaderDiagnosticSignal,
} from '@/utils/usageHeaderSnapshots';
import { resolveCodexPlanType } from '@/utils/quota/resolvers';
import { getCredentialScopedQuotaState } from '@/utils/quota/credentialScope';
import { isCodexMainQuotaModelScope, isCodexMainQuotaWindow } from '@/utils/quota/codexQuota';

export type AccountQuotaStatus =
  | 'unknown'
  | 'loading'
  | 'ok'
  | 'low'
  | 'exhausted'
  | 'error'
  | 'disabled';
export type AccountQuotaSource = 'cache' | 'observed-header' | 'none';
export type AccountQuotaSortDirection = 'asc' | 'desc';

export interface AccountQuotaSummary {
  status: AccountQuotaStatus;
  remainingPercent: number | null;
  usedPercent: number | null;
  resetLabel: string;
  resetAtMs: number | null;
  resetAccuracy: QuotaResetAccuracy;
  groupedAvailabilityState?: AccountGroupedQuotaAvailabilityState;
  planType: string | null;
  source: AccountQuotaSource;
  error?: string;
  errorStatus?: number;
  observedAtMs?: number;
  observedQuotaAtMs?: number;
  fetchedAtMs?: number;
  failedAtMs?: number;
  observedTraceId?: string;
  observedErrorKind?: string;
  observedErrorCode?: string;
  activeLimit?: string | null;
  creditsBalance?: string | null;
  creditsHasCredits?: boolean | null;
  creditsUnlimited?: boolean | null;
  creditsOverageLimitReached?: boolean | null;
  creditsApproxLocalMessages?: number | null;
  creditsApproxCloudMessages?: number | null;
  spendControlReached?: boolean | null;
  spendControlIndividualLimit?: number | null;
  rateLimitReachedType?: string | null;
  primaryOverSecondaryLimitPercent?: number | null;
}

export interface AccountQuotaStores {
  antigravityQuota: Record<string, AntigravityQuotaState>;
  claudeQuota: Record<string, ClaudeQuotaState>;
  codexQuota: Record<string, CodexQuotaState>;
  kimiQuota: Record<string, KimiQuotaState>;
  xaiQuota: Record<string, XaiQuotaState>;
}

export interface AccountQuotaOverrides {
  codexQuotaBySelectionKey?: Map<string, CodexQuotaState>;
  codexHeaderSnapshotBySelectionKey?: Map<string, UsageHeaderSnapshot>;
}

export type AccountGroupedQuotaAvailabilityState = 'available' | 'partial' | 'exhausted';

export interface AccountGroupedQuotaWindowInput {
  groupLabel?: string;
  kind?: string;
  remainingPercent: number | null;
  resetLabel?: string;
  resetAtMs?: number | null;
  resetAccuracy?: QuotaResetAccuracy;
}

export interface AccountGroupedQuotaGroupSummary {
  label: string;
  remainingPercent: number;
  resetLabel: string;
  resetAtMs: number | null;
  resetAccuracy: QuotaResetAccuracy;
  resetKind?: string;
}

export interface AccountGroupedQuotaAvailabilitySummary {
  state: AccountGroupedQuotaAvailabilityState;
  availableGroupCount: number;
  limitedGroupCount: number;
  totalGroupCount: number;
  remainingPercent: number;
  resetLabel: string;
  resetAtMs: number | null;
  resetAccuracy: QuotaResetAccuracy;
  resetKind?: string;
  groups: AccountGroupedQuotaGroupSummary[];
}

const QUOTA_LOW_THRESHOLD = 20;
const CREDENTIAL_REFRESH_FILE_WRITE_SKEW_MS = 5_000;

type AccountQuotaObservationFields = Partial<
  Pick<
    AccountQuotaSummary,
    | 'source'
    | 'observedAtMs'
    | 'observedQuotaAtMs'
    | 'fetchedAtMs'
    | 'observedTraceId'
    | 'observedErrorKind'
    | 'observedErrorCode'
    | 'activeLimit'
    | 'creditsBalance'
    | 'creditsHasCredits'
    | 'creditsUnlimited'
    | 'creditsOverageLimitReached'
    | 'creditsApproxLocalMessages'
    | 'creditsApproxCloudMessages'
    | 'spendControlReached'
    | 'spendControlIndividualLimit'
    | 'rateLimitReachedType'
    | 'primaryOverSecondaryLimitPercent'
  >
>;

const clampPercent = (value: number) => Math.max(0, Math.min(100, value));

const readString = (value: unknown): string => {
  if (typeof value === 'string') return value.trim();
  if (typeof value === 'number' && Number.isFinite(value)) return String(value);
  return '';
};

const readTimestampMs = (value: unknown): number | null => {
  if (typeof value === 'number' && Number.isFinite(value)) {
    const timestampMs = value < 1e12 ? value * 1000 : value;
    return isValidQuotaResetAtMs(timestampMs) ? timestampMs : null;
  }
  if (typeof value !== 'string') return null;
  const trimmed = value.trim();
  if (!trimmed) return null;
  const numeric = Number(trimmed);
  if (Number.isFinite(numeric)) {
    const timestampMs = numeric < 1e12 ? numeric * 1000 : numeric;
    return isValidQuotaResetAtMs(timestampMs) ? timestampMs : null;
  }
  const parsed = Date.parse(trimmed);
  return isValidQuotaResetAtMs(parsed) ? parsed : null;
};

export const readAuthFileCreatedAtMs = (file: AuthFileItem): number | null => {
  const candidates = [
    file['createdAtMs'],
    file['created_at_ms'],
    file['createdAt'],
    file['created_at'],
    file['created'],
    file['uploadedAtMs'],
    file['uploaded_at_ms'],
    file['uploadedAt'],
    file['uploaded_at'],
    file['modtime'],
    file.modified,
    file['updatedAt'],
    file['updated_at'],
    file.lastRefresh,
    file['last_refresh'],
  ];
  for (const value of candidates) {
    const timestamp = readTimestampMs(value);
    if (timestamp !== null) return timestamp;
  }
  return null;
};

export const readAuthFileUpdatedAtMs = (file: AuthFileItem): number | null => {
  const timestamps = [
    file['updatedAtMs'],
    file['updated_at_ms'],
    file['updatedAt'],
    file['updated_at'],
    file.modified,
    file['modtime'],
    file.lastRefresh,
    file['last_refresh'],
  ]
    .map(readTimestampMs)
    .filter((value): value is number => value !== null);
  return timestamps.length > 0 ? Math.max(...timestamps) : null;
};

export const readAuthFileCredentialRefreshAtMs = (file: AuthFileItem): number | null => {
  const refreshTimestamps = [
    file.lastRefresh,
    file['last_refresh'],
    file['lastRefreshedAt'],
    file['last_refreshed_at'],
  ]
    .map(readTimestampMs)
    .filter((value): value is number => value !== null);
  if (refreshTimestamps.length === 0) return null;

  const refreshAtMs = Math.max(...refreshTimestamps);
  const nearbyFileWriteTimestamps = [
    file['updatedAtMs'],
    file['updated_at_ms'],
    file['updatedAt'],
    file['updated_at'],
    file.modified,
    file['modtime'],
  ]
    .map(readTimestampMs)
    .filter(
      (value): value is number =>
        value !== null &&
        value > refreshAtMs &&
        value - refreshAtMs <= CREDENTIAL_REFRESH_FILE_WRITE_SKEW_MS
    );

  return nearbyFileWriteTimestamps.length > 0
    ? Math.max(...nearbyFileWriteTimestamps)
    : refreshAtMs;
};

export const normalizeAccountProvider = (file: AuthFileItem): string => {
  const raw = readString(file.provider) || readString(file.type) || 'unknown';
  const key = raw.toLowerCase().replace(/_/g, '-');
  if (key === 'x-ai' || key === 'grok') return 'xai';
  return key || 'unknown';
};

const readPlanType = (file: AuthFileItem): string | null => {
  if (normalizeAccountProvider(file) === 'codex') {
    const codexPlanType = resolveCodexPlanType(file);
    if (codexPlanType) return codexPlanType;
  }
  const idToken = file.id_token;
  const idTokenPlan =
    idToken && typeof idToken === 'object' && !Array.isArray(idToken)
      ? readString((idToken as Record<string, unknown>).plan_type)
      : '';
  const raw =
    idTokenPlan || readString(file.planType ?? file.plan_type ?? file.tier ?? file.subscription);
  return raw ? raw.toLowerCase() : null;
};

const getQuotaStatusFromRemaining = (remainingPercent: number | null): AccountQuotaStatus => {
  if (remainingPercent === null) return 'unknown';
  if (remainingPercent <= 0) return 'exhausted';
  if (remainingPercent < QUOTA_LOW_THRESHOLD) return 'low';
  return 'ok';
};

const remainingPercentFromUsed = (value: number | null | undefined) =>
  typeof value === 'number' && Number.isFinite(value) ? clampPercent(100 - value) : null;

const hasMeaningfulResetLabel = (value: string): boolean => Boolean(value && value !== '-');

type NormalizedQuotaReset = {
  resetLabel: string;
  resetAtMs: number | null;
  resetAccuracy: QuotaResetAccuracy;
};

const normalizeQuotaReset = ({
  resetLabel,
  resetAtMs,
  resetAccuracy,
}: Pick<
  AccountGroupedQuotaWindowInput,
  'resetLabel' | 'resetAtMs' | 'resetAccuracy'
>): NormalizedQuotaReset => {
  const label = readString(resetLabel);
  if (isValidQuotaResetAtMs(resetAtMs)) {
    return {
      resetLabel: label,
      resetAtMs,
      resetAccuracy: resetAccuracy ?? 'unknown',
    };
  }
  return {
    resetLabel: label,
    resetAtMs: parseQuotaResetLabelMs(label),
    resetAccuracy: 'unknown',
  };
};

const combineBlockingResetAccuracy = (
  windows: Array<{ resetAccuracy: QuotaResetAccuracy }>
): QuotaResetAccuracy => {
  if (windows.some((window) => window.resetAccuracy === 'unknown')) return 'unknown';
  if (windows.some((window) => window.resetAccuracy === 'estimated')) return 'estimated';
  return 'exact';
};

export const summarizeGroupedQuotaAvailability = (
  windows: AccountGroupedQuotaWindowInput[]
): AccountGroupedQuotaAvailabilitySummary | null => {
  const windowsByGroup = new Map<string, AccountGroupedQuotaWindowInput[]>();
  windows.forEach((window) => {
    const groupLabel = readString(window.groupLabel);
    if (!groupLabel) return;
    const groupWindows = windowsByGroup.get(groupLabel) ?? [];
    groupWindows.push(window);
    windowsByGroup.set(groupLabel, groupWindows);
  });

  const groups = [...windowsByGroup.entries()]
    .map(([label, groupWindows]): AccountGroupedQuotaGroupSummary | null => {
      const knownWindows = groupWindows
        .map((window) => {
          if (
            typeof window.remainingPercent !== 'number' ||
            !Number.isFinite(window.remainingPercent)
          ) {
            return null;
          }
          const reset = normalizeQuotaReset(window);
          return {
            kind: readString(window.kind) || undefined,
            remainingPercent: clampPercent(window.remainingPercent),
            ...reset,
          };
        })
        .filter(
          (
            window
          ): window is {
            remainingPercent: number;
            resetLabel: string;
            resetAtMs: number | null;
            resetAccuracy: QuotaResetAccuracy;
            kind: string | undefined;
          } => window !== null
        );
      if (knownWindows.length === 0) return null;

      const limitingWindow = knownWindows.reduce((current, next) =>
        next.remainingPercent < current.remainingPercent ? next : current
      );
      const blockingWindows = knownWindows.filter((window) => window.remainingPercent <= 0);
      const blockingWindowWithoutTime = blockingWindows.find((window) => window.resetAtMs === null);
      const timedBlockingRecovery = blockingWindowWithoutTime
        ? null
        : blockingWindows
            .filter(
              (window): window is (typeof knownWindows)[number] & { resetAtMs: number } =>
                window.resetAtMs !== null
            )
            .sort((left, right) => right.resetAtMs - left.resetAtMs)[0];
      const resetSource = blockingWindowWithoutTime ?? timedBlockingRecovery ?? limitingWindow;
      const resetAccuracy =
        blockingWindows.length > 0
          ? blockingWindowWithoutTime
            ? 'unknown'
            : combineBlockingResetAccuracy(blockingWindows)
          : resetSource.resetAccuracy;
      const resetLabel = blockingWindowWithoutTime
        ? hasMeaningfulResetLabel(blockingWindowWithoutTime.resetLabel)
          ? blockingWindowWithoutTime.resetLabel
          : '-'
        : (hasMeaningfulResetLabel(resetSource.resetLabel) ? resetSource.resetLabel : '') ||
          knownWindows.find((window) => hasMeaningfulResetLabel(window.resetLabel))?.resetLabel ||
          '-';
      return {
        label,
        remainingPercent: limitingWindow.remainingPercent,
        resetLabel,
        resetAtMs: blockingWindowWithoutTime ? null : resetSource.resetAtMs,
        resetAccuracy,
        resetKind: resetSource.kind,
      };
    })
    .filter((group): group is AccountGroupedQuotaGroupSummary => group !== null);

  if (groups.length === 0) return null;

  const availableGroups = groups.filter((group) => group.remainingPercent > 0);
  const limitedGroups = groups.filter((group) => group.remainingPercent <= 0);
  const bestAvailableGroup = groups.reduce((current, next) =>
    next.remainingPercent > current.remainingPercent ? next : current
  );
  const timedRecovery = limitedGroups
    .filter(
      (group): group is AccountGroupedQuotaGroupSummary & { resetAtMs: number } =>
        group.resetAtMs !== null
    )
    .sort((left, right) => left.resetAtMs - right.resetAtMs)[0];
  const recoveryGroup =
    timedRecovery ?? limitedGroups.find((group) => hasMeaningfulResetLabel(group.resetLabel));
  const resetSource = limitedGroups.length > 0 ? recoveryGroup : bestAvailableGroup;

  return {
    state:
      availableGroups.length === 0
        ? 'exhausted'
        : limitedGroups.length > 0
          ? 'partial'
          : 'available',
    availableGroupCount: availableGroups.length,
    limitedGroupCount: limitedGroups.length,
    totalGroupCount: groups.length,
    remainingPercent: bestAvailableGroup.remainingPercent,
    resetLabel: resetSource?.resetLabel || '-',
    resetAtMs: resetSource?.resetAtMs ?? null,
    resetAccuracy: resetSource?.resetAccuracy ?? 'unknown',
    resetKind: resetSource?.resetKind,
    groups,
  };
};

const quotaFromRemainingWindows = (
  windows: Array<{
    remainingPercent: number | null;
    usedPercent?: number | null;
    resetLabel?: string;
    resetAtMs?: number | null;
    resetAccuracy?: QuotaResetAccuracy;
  }>,
  planType: string | null,
  options: AccountQuotaObservationFields = {}
): AccountQuotaSummary => {
  const source = options.source ?? 'cache';
  const candidates = windows
    .map((window) => {
      const remainingPercent =
        typeof window.remainingPercent === 'number' && Number.isFinite(window.remainingPercent)
          ? clampPercent(window.remainingPercent)
          : remainingPercentFromUsed(window.usedPercent);
      if (remainingPercent === null) return null;
      const reset = normalizeQuotaReset(window);
      return {
        remainingPercent,
        usedPercent: clampPercent(100 - remainingPercent),
        ...reset,
      };
    })
    .filter(
      (
        window
      ): window is {
        remainingPercent: number;
        usedPercent: number;
        resetLabel: string;
        resetAtMs: number | null;
        resetAccuracy: QuotaResetAccuracy;
      } => window !== null
    );

  if (candidates.length === 0) {
    return {
      status: 'unknown',
      remainingPercent: null,
      usedPercent: null,
      resetLabel: '-',
      resetAtMs: null,
      resetAccuracy: 'unknown',
      planType,
      ...options,
      source,
    };
  }

  const minimumRemaining = candidates.reduce((current, next) =>
    next.remainingPercent < current.remainingPercent ? next : current
  ).remainingPercent;
  const limitingCandidates = candidates.filter(
    (candidate) => candidate.remainingPercent === minimumRemaining
  );
  let selected = limitingCandidates[0];
  let selectedResetAccuracy = selected.resetAccuracy;
  if (minimumRemaining <= 0 && limitingCandidates.length > 1) {
    const unknownReset = limitingCandidates.find((candidate) => candidate.resetAtMs === null);
    if (unknownReset) {
      selected = unknownReset;
      selectedResetAccuracy = 'unknown';
    } else {
      selected = limitingCandidates.reduce((current, next) =>
        (next.resetAtMs ?? 0) > (current.resetAtMs ?? 0) ? next : current
      );
      selectedResetAccuracy = combineBlockingResetAccuracy(limitingCandidates);
    }
  }
  const resetLabel = selected.resetLabel || '-';
  return {
    status: getQuotaStatusFromRemaining(selected.remainingPercent),
    remainingPercent: selected.remainingPercent,
    usedPercent: selected.usedPercent,
    resetLabel,
    resetAtMs: selected.resetAtMs,
    resetAccuracy: selectedResetAccuracy,
    planType,
    ...options,
    source,
  };
};

const quotaFromUsedWindows = (
  windows: Array<{
    usedPercent: number | null;
    resetLabel?: string;
    resetAtMs?: number | null;
    resetAccuracy?: QuotaResetAccuracy;
  }>,
  planType: string | null,
  options: AccountQuotaObservationFields = {}
): AccountQuotaSummary =>
  quotaFromRemainingWindows(
    windows.map((window) => ({
      remainingPercent: remainingPercentFromUsed(window.usedPercent),
      usedPercent: window.usedPercent,
      resetLabel: window.resetLabel,
      resetAtMs: window.resetAtMs,
      resetAccuracy: window.resetAccuracy,
    })),
    planType,
    options
  );

const codexMainQuotaWindows = (quota: CodexQuotaState) =>
  quota.windows.filter(isCodexMainQuotaWindow);

const quotaFromXaiBilling = (
  billing: XaiBillingSummary | null | undefined,
  planType: string | null,
  options: AccountQuotaObservationFields = {}
): AccountQuotaSummary => {
  if (!billing) {
    return quotaFromRemainingWindows([{ remainingPercent: null }], planType, options);
  }
  if (billing.officialApiHealth) {
    return quotaFromRemainingWindows([{ remainingPercent: null }], planType, options);
  }

  const resetLabel = billing.billingPeriodEnd ?? '-';
  const periodResetLabel = billing.periodEnd ?? resetLabel;
  const billingReset = resolveAbsoluteQuotaReset(billing.billingPeriodEnd);
  const periodReset = resolveAbsoluteQuotaReset(billing.periodEnd ?? billing.billingPeriodEnd);
  const billingResetFields = {
    resetLabel,
    resetAtMs: billingReset.resetAtMs,
    resetAccuracy: billingReset.resetAccuracy,
  };
  const periodResetFields = {
    resetLabel: periodResetLabel,
    resetAtMs: periodReset.resetAtMs,
    resetAccuracy: periodReset.resetAccuracy,
  };
  const periodRemainingPercent =
    billing.periodType === 'weekly' ? remainingPercentFromUsed(billing.usagePercent) : null;
  const productRemainingWindows =
    billing.productUsage
      ?.map((product) => ({
        remainingPercent: remainingPercentFromUsed(product.usagePercent),
        usedPercent: product.usagePercent,
        ...periodResetFields,
      }))
      .filter((window) => window.remainingPercent !== null || window.usedPercent !== null) ?? [];
  const monthlyLimitCents = billing.monthlyLimitCents;
  const monthlyRemainingCents =
    monthlyLimitCents !== null && billing.includedUsedCents !== null
      ? Math.max(0, monthlyLimitCents - billing.includedUsedCents)
      : null;
  const onDemandEnabled = billing.onDemandCapCents !== null && billing.onDemandCapCents > 0;
  const onDemandRemainingCents =
    onDemandEnabled && billing.onDemandUsedCents !== null && billing.onDemandCapCents !== null
      ? Math.max(0, billing.onDemandCapCents - billing.onDemandUsedCents)
      : null;
  const hasMonthlyComponent = monthlyLimitCents !== null && monthlyLimitCents > 0;
  const monthlyComponentKnown = !hasMonthlyComponent || monthlyRemainingCents !== null;
  const onDemandComponentKnown = !onDemandEnabled || onDemandRemainingCents !== null;
  const totalLimitCents =
    (monthlyLimitCents ?? 0) + (onDemandEnabled ? (billing.onDemandCapCents ?? 0) : 0);
  const totalRemainingCents =
    (monthlyRemainingCents ?? 0) + (onDemandEnabled ? (onDemandRemainingCents ?? 0) : 0);

  if (totalLimitCents > 0 && monthlyComponentKnown && onDemandComponentKnown) {
    return quotaFromRemainingWindows(
      [
        ...(periodRemainingPercent !== null
          ? [
              {
                remainingPercent: periodRemainingPercent,
                usedPercent: billing.usagePercent,
                ...periodResetFields,
              },
            ]
          : []),
        ...productRemainingWindows,
        {
          remainingPercent: (totalRemainingCents / totalLimitCents) * 100,
          ...billingResetFields,
        },
      ],
      planType,
      options
    );
  }

  if (onDemandEnabled) {
    const onDemandRemainingPercent = remainingPercentFromUsed(billing.onDemandUsedPercent);
    if (onDemandRemainingPercent !== null) {
      return quotaFromRemainingWindows(
        [
          ...(periodRemainingPercent !== null
            ? [
                {
                  remainingPercent: periodRemainingPercent,
                  usedPercent: billing.usagePercent,
                  ...periodResetFields,
                },
              ]
            : []),
          ...productRemainingWindows,
          {
            remainingPercent: onDemandRemainingPercent,
            usedPercent: billing.onDemandUsedPercent,
            ...billingResetFields,
          },
        ],
        planType,
        options
      );
    }

    const monthlyRemainingPercent = remainingPercentFromUsed(billing.usedPercent);
    if (monthlyRemainingPercent !== null && monthlyRemainingPercent <= 0) {
      return quotaFromRemainingWindows(
        [{ remainingPercent: null, ...billingResetFields }],
        planType,
        options
      );
    }
  }

  return quotaFromRemainingWindows(
    [
      ...(periodRemainingPercent !== null
        ? [
            {
              remainingPercent: periodRemainingPercent,
              usedPercent: billing.usagePercent,
              ...periodResetFields,
            },
          ]
        : []),
      ...productRemainingWindows,
      {
        remainingPercent: remainingPercentFromUsed(billing.usedPercent),
        usedPercent: billing.usedPercent,
        ...billingResetFields,
      },
    ],
    planType,
    options
  );
};

const quotaObservationFields = (quota: CodexQuotaState): AccountQuotaObservationFields => {
  const scopedHeaderObservation =
    quota.observedFromUsageHeaders === true &&
    (quota.observedModelScope === undefined ||
      !isCodexMainQuotaModelScope(quota.observedModelScope));
  const hasProviderSnapshot = quota.fetchedAtMs !== undefined;
  const suppressAccountFields = scopedHeaderObservation && !hasProviderSnapshot;
  return {
    source: scopedHeaderObservation
      ? hasProviderSnapshot
        ? 'cache'
        : 'none'
      : quota.observedFromUsageHeaders
        ? 'observed-header'
        : 'cache',
    fetchedAtMs: quota.fetchedAtMs,
    observedAtMs: quota.observedAtMs,
    observedQuotaAtMs:
      quota.observedFromUsageHeaders && !scopedHeaderObservation ? quota.observedAtMs : undefined,
    observedTraceId: quota.observedTraceId,
    observedErrorKind: quota.observedErrorKind,
    observedErrorCode: quota.observedErrorCode,
    activeLimit: suppressAccountFields ? undefined : quota.activeLimit,
    creditsBalance: suppressAccountFields ? undefined : quota.creditsBalance,
    creditsHasCredits: suppressAccountFields ? undefined : quota.creditsHasCredits,
    creditsUnlimited: suppressAccountFields ? undefined : quota.creditsUnlimited,
    creditsOverageLimitReached: suppressAccountFields
      ? undefined
      : quota.creditsOverageLimitReached,
    creditsApproxLocalMessages: suppressAccountFields
      ? undefined
      : quota.creditsApproxLocalMessages,
    creditsApproxCloudMessages: suppressAccountFields
      ? undefined
      : quota.creditsApproxCloudMessages,
    spendControlReached: suppressAccountFields ? undefined : quota.spendControlReached,
    spendControlIndividualLimit: suppressAccountFields
      ? undefined
      : quota.spendControlIndividualLimit,
    rateLimitReachedType: suppressAccountFields ? undefined : quota.rateLimitReachedType,
    primaryOverSecondaryLimitPercent: suppressAccountFields
      ? undefined
      : quota.primaryOverSecondaryLimitPercent,
  };
};

const quotaObservationFieldsFromSnapshot = (
  snapshot: UsageHeaderSnapshot | undefined
): AccountQuotaObservationFields => {
  if (!hasUsageHeaderDiagnosticSignal(snapshot)) return {};
  const observedQuota = buildObservedCodexQuotaFromHeaderSnapshot(snapshot);
  const accountQuotaObservation =
    observedQuota !== null && isCodexMainQuotaModelScope(observedQuota.quotaScope.modelScope);
  return {
    source: observedQuota === null || accountQuotaObservation ? 'observed-header' : undefined,
    observedAtMs: snapshot?.timestamp_ms,
    observedQuotaAtMs: accountQuotaObservation ? snapshot?.timestamp_ms : undefined,
    observedTraceId: getHeaderSnapshotTraceId(snapshot) || undefined,
    observedErrorKind: getHeaderSnapshotErrorKind(snapshot) || undefined,
    observedErrorCode: getHeaderSnapshotErrorCode(snapshot) || undefined,
    activeLimit: accountQuotaObservation ? (observedQuota?.activeLimit ?? undefined) : undefined,
    creditsBalance: accountQuotaObservation
      ? (observedQuota?.creditsBalance ?? undefined)
      : undefined,
    creditsHasCredits: accountQuotaObservation
      ? (observedQuota?.creditsHasCredits ?? undefined)
      : undefined,
    creditsUnlimited: accountQuotaObservation
      ? (observedQuota?.creditsUnlimited ?? undefined)
      : undefined,
    rateLimitReachedType: accountQuotaObservation
      ? (observedQuota?.rateLimitReachedType ?? undefined)
      : undefined,
    primaryOverSecondaryLimitPercent: accountQuotaObservation
      ? (observedQuota?.primaryOverSecondaryLimitPercent ?? undefined)
      : undefined,
  };
};

const hasObservedQuotaFields = (fields: AccountQuotaObservationFields): boolean =>
  Object.values(fields).some((value) => value !== undefined);

const mergeQuotaObservationFields = (
  summary: AccountQuotaSummary,
  fields: AccountQuotaObservationFields
): AccountQuotaSummary => {
  if (!hasObservedQuotaFields(fields)) return summary;
  const merged: AccountQuotaSummary = { ...summary };
  if (fields.observedAtMs !== undefined) merged.observedAtMs = fields.observedAtMs;
  if (fields.observedQuotaAtMs !== undefined) {
    merged.observedQuotaAtMs = fields.observedQuotaAtMs;
  }
  if (fields.fetchedAtMs !== undefined) merged.fetchedAtMs = fields.fetchedAtMs;
  if (fields.observedTraceId !== undefined) merged.observedTraceId = fields.observedTraceId;
  if (fields.observedErrorKind !== undefined) {
    merged.observedErrorKind = fields.observedErrorKind;
  }
  if (fields.observedErrorCode !== undefined) {
    merged.observedErrorCode = fields.observedErrorCode;
  }
  if (fields.activeLimit !== undefined) merged.activeLimit = fields.activeLimit;
  if (fields.creditsBalance !== undefined) merged.creditsBalance = fields.creditsBalance;
  if (fields.creditsHasCredits !== undefined) merged.creditsHasCredits = fields.creditsHasCredits;
  if (fields.creditsUnlimited !== undefined) merged.creditsUnlimited = fields.creditsUnlimited;
  if (fields.creditsOverageLimitReached !== undefined) {
    merged.creditsOverageLimitReached = fields.creditsOverageLimitReached;
  }
  if (fields.creditsApproxLocalMessages !== undefined) {
    merged.creditsApproxLocalMessages = fields.creditsApproxLocalMessages;
  }
  if (fields.creditsApproxCloudMessages !== undefined) {
    merged.creditsApproxCloudMessages = fields.creditsApproxCloudMessages;
  }
  if (fields.spendControlReached !== undefined) {
    merged.spendControlReached = fields.spendControlReached;
  }
  if (fields.spendControlIndividualLimit !== undefined) {
    merged.spendControlIndividualLimit = fields.spendControlIndividualLimit;
  }
  if (fields.rateLimitReachedType !== undefined) {
    merged.rateLimitReachedType = fields.rateLimitReachedType;
  }
  if (fields.primaryOverSecondaryLimitPercent !== undefined) {
    merged.primaryOverSecondaryLimitPercent = fields.primaryOverSecondaryLimitPercent;
  }
  if (summary.source === 'none' && fields.source) {
    merged.source = fields.source;
  }
  return merged;
};

const quotaFromError = (
  error: string | undefined,
  planType: string | null,
  errorStatus?: number,
  failedAtMs?: number
): AccountQuotaSummary => ({
  status: 'error',
  remainingPercent: null,
  usedPercent: null,
  resetLabel: '-',
  resetAtMs: null,
  resetAccuracy: 'unknown',
  planType,
  source: 'cache',
  error,
  errorStatus,
  failedAtMs,
});

const emptyQuota = (planType: string | null): AccountQuotaSummary => ({
  status: 'unknown',
  remainingPercent: null,
  usedPercent: null,
  resetLabel: '-',
  resetAtMs: null,
  resetAccuracy: 'unknown',
  planType,
  source: 'none',
});

const loadingQuota = (planType: string | null): AccountQuotaSummary => ({
  status: 'loading',
  remainingPercent: null,
  usedPercent: null,
  resetLabel: '-',
  resetAtMs: null,
  resetAccuracy: 'unknown',
  planType,
  source: 'cache',
});

export const resolveAccountQuota = (
  file: AuthFileItem,
  stores: AccountQuotaStores,
  overrides?: AccountQuotaOverrides
): AccountQuotaSummary => {
  const provider = normalizeAccountProvider(file);
  const filePlanType = readPlanType(file);
  if (file.disabled === true) {
    return {
      status: 'disabled',
      remainingPercent: null,
      usedPercent: null,
      resetLabel: '-',
      resetAtMs: null,
      resetAccuracy: 'unknown',
      planType: filePlanType,
      source: 'none',
    };
  }

  if (provider === 'codex') {
    const selectionKey = getAuthFileSelectionKey(file);
    const quota =
      overrides?.codexQuotaBySelectionKey?.get(selectionKey) ??
      getCredentialScopedQuotaState(stores.codexQuota, file);
    const headerSnapshot = overrides?.codexHeaderSnapshotBySelectionKey?.get(selectionKey);
    const headerObservationFields = quotaObservationFieldsFromSnapshot(headerSnapshot);
    const headerPlanType = readString(getHeaderSnapshotPlanType(headerSnapshot)).toLowerCase();
    const observedPlanType = headerPlanType || filePlanType;
    if (!quota) {
      return mergeQuotaObservationFields(emptyQuota(observedPlanType), headerObservationFields);
    }
    if (quota.status === 'loading') {
      return mergeQuotaObservationFields(
        loadingQuota(quota.planType ?? observedPlanType),
        headerObservationFields
      );
    }
    if (quota.status === 'error') {
      if (quota.windows.length > 0) {
        return mergeQuotaObservationFields(
          {
            ...quotaFromUsedWindows(
              codexMainQuotaWindows(quota),
              quota.planType ?? observedPlanType
            ),
            error: quota.error,
            errorStatus: quota.errorStatus,
            fetchedAtMs: quota.fetchedAtMs,
            failedAtMs: quota.failedAtMs,
          },
          headerObservationFields
        );
      }
      return mergeQuotaObservationFields(
        quotaFromError(
          quota.error,
          quota.planType ?? observedPlanType,
          quota.errorStatus,
          quota.failedAtMs
        ),
        headerObservationFields
      );
    }
    if (
      quota.quotaInventoryObserved === true &&
      quota.windows.length === 0 &&
      !quota.rateLimitReachedType &&
      quota.spendControlReached !== true &&
      quota.creditsOverageLimitReached !== true
    ) {
      return mergeQuotaObservationFields(
        {
          ...quotaFromUsedWindows(
            codexMainQuotaWindows(quota),
            quota.planType ?? observedPlanType,
            quotaObservationFields(quota)
          ),
          status: 'ok',
        },
        headerObservationFields
      );
    }
    return mergeQuotaObservationFields(
      quotaFromUsedWindows(
        codexMainQuotaWindows(quota),
        quota.planType ?? observedPlanType,
        quotaObservationFields(quota)
      ),
      headerObservationFields
    );
  }

  if (provider === 'claude') {
    const quota = getCredentialScopedQuotaState(stores.claudeQuota, file);
    if (!quota) return emptyQuota(filePlanType);
    if (quota.status === 'loading') return loadingQuota(quota.planType ?? filePlanType);
    if (quota.status === 'error')
      return quotaFromError(
        quota.error,
        quota.planType ?? filePlanType,
        quota.errorStatus,
        quota.failedAtMs
      );
    return quotaFromUsedWindows(quota.windows, quota.planType ?? filePlanType, {
      fetchedAtMs: quota.fetchedAtMs,
    });
  }

  if (provider === 'antigravity') {
    const quota = getCredentialScopedQuotaState(stores.antigravityQuota, file);
    if (!quota) return emptyQuota(filePlanType);
    const subscriptionPlan =
      readString(quota.subscription?.plan) ||
      readString(quota.subscription?.tierName) ||
      readString(quota.subscription?.tierId);
    const planType = filePlanType ?? (subscriptionPlan ? subscriptionPlan.toLowerCase() : null);
    if (quota.status === 'loading') return loadingQuota(planType);
    if (quota.status === 'error') {
      return quotaFromError(quota.error, planType, quota.errorStatus, quota.failedAtMs);
    }
    const availability = summarizeGroupedQuotaAvailability(
      quota.groups.flatMap((group) =>
        group.buckets.map((bucket) => {
          const reset = resolveAbsoluteQuotaReset(bucket.resetTime);
          return {
            groupLabel: group.label || group.id,
            remainingPercent:
              typeof bucket.remainingFraction === 'number' &&
              Number.isFinite(bucket.remainingFraction)
                ? bucket.remainingFraction * 100
                : null,
            resetLabel: bucket.resetTime,
            resetAtMs: reset.resetAtMs,
            resetAccuracy: reset.resetAccuracy,
          };
        })
      )
    );
    if (!availability) {
      return {
        ...emptyQuota(planType),
        fetchedAtMs: quota.fetchedAtMs,
      };
    }
    return {
      status: getQuotaStatusFromRemaining(availability.remainingPercent),
      remainingPercent: availability.remainingPercent,
      usedPercent: clampPercent(100 - availability.remainingPercent),
      resetLabel: availability.resetLabel,
      resetAtMs: availability.resetAtMs,
      resetAccuracy: availability.resetAccuracy,
      groupedAvailabilityState: availability.state,
      planType,
      source: 'cache',
      fetchedAtMs: quota.fetchedAtMs,
    };
  }

  if (provider === 'kimi') {
    const quota = getCredentialScopedQuotaState(stores.kimiQuota, file);
    if (!quota) return emptyQuota(filePlanType);
    if (quota.status === 'loading') return loadingQuota(filePlanType);
    if (quota.status === 'error')
      return quotaFromError(quota.error, filePlanType, quota.errorStatus, quota.failedAtMs);
    return quotaFromRemainingWindows(
      quota.rows.map((row) => ({
        remainingPercent:
          row.limit > 0 ? (Math.max(0, row.limit - row.used) / row.limit) * 100 : null,
        resetLabel: row.resetHint,
        resetAtMs: row.resetAtMs,
        resetAccuracy: row.resetAccuracy,
      })),
      filePlanType,
      { fetchedAtMs: quota.fetchedAtMs }
    );
  }

  if (provider === 'xai') {
    const quota = getCredentialScopedQuotaState(stores.xaiQuota, file);
    if (!quota) return emptyQuota(filePlanType);
    if (quota.status === 'loading') return loadingQuota(filePlanType);
    if (quota.status === 'error')
      return quotaFromError(quota.error, filePlanType, quota.errorStatus, quota.failedAtMs);
    return quotaFromXaiBilling(quota.billing, filePlanType, {
      fetchedAtMs: quota.fetchedAtMs,
    });
  }

  return emptyQuota(filePlanType);
};

export const compareQuotaResetLabels = (
  leftRaw: string,
  rightRaw: string,
  direction: AccountQuotaSortDirection
) => {
  const left = normalizeResetSortLabel(leftRaw);
  const right = normalizeResetSortLabel(rightRaw);
  if (!left && !right) return 0;
  if (!left) return 1;
  if (!right) return -1;

  const result = left.localeCompare(right, undefined, {
    numeric: true,
    sensitivity: 'base',
  });
  return direction === 'asc' ? result : -result;
};

export const compareQuotaResets = (
  left: Pick<AccountQuotaSummary, 'resetAtMs' | 'resetLabel'>,
  right: Pick<AccountQuotaSummary, 'resetAtMs' | 'resetLabel'>,
  direction: AccountQuotaSortDirection
) => {
  const leftAtMs = isValidQuotaResetAtMs(left.resetAtMs) ? left.resetAtMs : null;
  const rightAtMs = isValidQuotaResetAtMs(right.resetAtMs) ? right.resetAtMs : null;
  if (leftAtMs !== null || rightAtMs !== null) {
    if (leftAtMs === null) return 1;
    if (rightAtMs === null) return -1;
    const result = leftAtMs - rightAtMs;
    return direction === 'asc' ? result : -result;
  }
  return compareQuotaResetLabels(left.resetLabel, right.resetLabel, direction);
};

const normalizeResetSortLabel = (value: string) => {
  const label = readString(value);
  const normalized = label.toLowerCase();
  if (!label || label === '-' || normalized.includes('unknown') || label.includes('未知')) {
    return null;
  }
  return label;
};
