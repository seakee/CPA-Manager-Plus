/**
 * Quota configuration definitions.
 */

import React from 'react';
import type { ReactNode } from 'react';
import type { TFunction } from 'i18next';
import type {
  AntigravityQuotaState,
  AntigravityQuotaSubscription,
  AuthFileItem,
  ClaudeExtraUsage,
  ClaudeQuotaState,
  ClaudeQuotaWindow,
  CodexQuotaState,
  CodexRateLimitResetCredit,
  CodexQuotaWindow,
  KimiQuotaRow,
  KimiQuotaState,
  XaiBillingSummary,
  XaiQuotaState,
} from '@/types';
import type { UsageHeaderSnapshot } from '@/services/api/usageService';
import type { AntigravityQuotaData, CodexQuotaData } from '@/utils/quota';
import { IconInfo } from '@/components/ui/icons';
import { resetCodexQuota } from '@/services/api/codexQuota';
import {
  normalizePlanType,
  resolveCodexChatgptAccountId,
  resolveCodexPlanType,
  formatQuotaResetTime,
  formatKimiResetHint,
  fetchAntigravityQuota,
  fetchClaudeQuota,
  fetchCodexQuota,
  fetchKimiQuota,
  fetchXaiQuota,
  buildCodexQuotaWindows,
  isAntigravityFile,
  isClaudeFile,
  isCodexFile,
  isDisabledAuthFile,
  isKimiFile,
  isXaiFile,
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
import { normalizeAuthIndex } from '@/utils/authIndex';
import type { QuotaRenderHelpers } from './QuotaCard';
import styles from '@/features/quota/QuotaPage.module.scss';

type QuotaUpdater<T> = T | ((prev: T) => T);

type QuotaType = 'antigravity' | 'claude' | 'codex' | 'kimi' | 'xai';
export type QuotaSortMode = 'default' | 'name-asc' | 'plan-desc' | 'plan-asc';

const QUOTA_PROGRESS_HIGH_THRESHOLD = 70;
const QUOTA_PROGRESS_MEDIUM_THRESHOLD = 30;
const CODEX_INFO_WINDOW_IDS = new Set(['five-hour', 'weekly', 'monthly']);
export interface QuotaStore {
  antigravityQuota: Record<string, AntigravityQuotaState>;
  claudeQuota: Record<string, ClaudeQuotaState>;
  codexQuota: Record<string, CodexQuotaState>;
  kimiQuota: Record<string, KimiQuotaState>;
  xaiQuota: Record<string, XaiQuotaState>;
  setAntigravityQuota: (updater: QuotaUpdater<Record<string, AntigravityQuotaState>>) => void;
  setClaudeQuota: (updater: QuotaUpdater<Record<string, ClaudeQuotaState>>) => void;
  setCodexQuota: (updater: QuotaUpdater<Record<string, CodexQuotaState>>) => void;
  setKimiQuota: (updater: QuotaUpdater<Record<string, KimiQuotaState>>) => void;
  setXaiQuota: (updater: QuotaUpdater<Record<string, XaiQuotaState>>) => void;
  clearQuotaCache: () => void;
}

export interface QuotaConfig<TState, TData> {
  type: QuotaType;
  i18nPrefix: string;
  cardIdleMessageKey?: string;
  filterFn: (file: AuthFileItem) => boolean;
  fetchQuota: (file: AuthFileItem, t: TFunction) => Promise<TData>;
  storeSelector: (state: QuotaStore) => Record<string, TState>;
  storeSetter: keyof QuotaStore;
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
  cardClassName: string;
  controlsClassName: string;
  controlClassName: string;
  gridClassName: string;
  getSearchText?: (file: AuthFileItem, quota: TState | undefined, t: TFunction) => unknown[];
  getPlanSortRank?: (file: AuthFileItem, quota: TState | undefined) => number | null;
  buildObservedState?: (
    file: AuthFileItem,
    snapshot: UsageHeaderSnapshot | undefined,
    t: TFunction
  ) => TState | undefined;
  resetQuota?: (file: AuthFileItem, t: TFunction) => Promise<TData>;
  canResetQuota?: (file: AuthFileItem, quota: TState | undefined) => boolean;
  renderQuotaItems: (quota: TState, t: TFunction, helpers: QuotaRenderHelpers) => ReactNode;
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
): TState =>
  config.buildFailureState
    ? config.buildFailureState(message, status, file, activeState, failedAtMs)
    : config.buildErrorState(message, status, file);

const formatAntigravityDuration = (t: TFunction, deltaMs: number): string => {
  const totalMinutes = Math.max(0, Math.ceil(deltaMs / 60000));
  const days = Math.floor(totalMinutes / 1440);
  const hours = Math.floor((totalMinutes % 1440) / 60);
  const minutes = totalMinutes % 60;

  if (days > 0) {
    return t('antigravity_quota.duration_day_hour', { days, hours });
  }
  if (hours > 0) {
    return t('antigravity_quota.duration_hour_minute', { hours, minutes });
  }
  if (minutes > 0) {
    return t('antigravity_quota.duration_minute', { minutes });
  }
  return t('antigravity_quota.duration_less_than_minute');
};

const formatAntigravityResetLabel = (
  resetTime: string | undefined,
  t: TFunction,
  nowMs: number
): string => {
  if (!resetTime) return '-';
  const resetMs = new Date(resetTime).getTime();
  if (Number.isNaN(resetMs)) return formatQuotaResetTime(resetTime);
  const deltaMs = resetMs - nowMs;
  if (deltaMs <= 0) return t('antigravity_quota.refresh_available');
  return t('antigravity_quota.refreshes_in', {
    duration: formatAntigravityDuration(t, deltaMs),
  });
};

const ANTIGRAVITY_GROUP_LABEL_KEYS = new Map<string, string>([
  ['gemini models', 'group_gemini_models'],
  ['claude and gpt models', 'group_claude_gpt_models'],
]);

const ANTIGRAVITY_BUCKET_LABEL_KEYS = new Map<string, string>([
  ['weekly limit', 'weekly_limit'],
  ['daily limit', 'daily_limit'],
  ['5 hour limit', 'five_hour_limit'],
  ['5-hour limit', 'five_hour_limit'],
  ['five hour limit', 'five_hour_limit'],
  ['monthly limit', 'monthly_limit'],
]);

const normalizeAntigravityQuotaText = (value: string): string =>
  value.trim().toLowerCase().replace(/\s+/g, ' ');

const translateAntigravityQuotaLabel = (
  value: string,
  keys: Map<string, string>,
  t: TFunction
): string => {
  const key = keys.get(normalizeAntigravityQuotaText(value));
  return key ? t(`antigravity_quota.${key}`) : value;
};

const translateAntigravityQuotaDescription = (
  value: string | undefined,
  t: TFunction
): string | undefined => {
  if (!value) return undefined;
  const modelsMatch = value.match(/^models within this group:\s*(.+)$/i);
  if (modelsMatch) {
    return t('antigravity_quota.group_models_description', {
      models: modelsMatch[1].trim(),
    });
  }
  return value;
};

const getAntigravityPlanLabel = (
  subscription: AntigravityQuotaSubscription | null | undefined,
  t: TFunction
): string | null => {
  if (!subscription) return null;
  if (subscription.plan === 'free') return t('antigravity_subscription.plan_free');
  if (subscription.plan === 'pro') return t('antigravity_subscription.plan_pro');
  if (subscription.plan === 'ultra') return t('antigravity_subscription.plan_ultra');
  if (subscription.plan === 'ultra-lite') return t('antigravity_subscription.plan_ultra_lite');
  return (
    subscription.tierName ||
    subscription.tierId ||
    (subscription.plan === 'unknown' ? t('antigravity_subscription.plan_unknown') : null)
  );
};

const renderAntigravityItems = (
  quota: AntigravityQuotaState,
  t: TFunction,
  helpers: QuotaRenderHelpers
): ReactNode => {
  const { styles: styleMap, QuotaProgressBar } = helpers;
  const { createElement: h, Fragment } = React;
  const groups = quota.groups ?? [];
  const nodes: ReactNode[] = [];
  const planLabel = getAntigravityPlanLabel(quota.subscription, t);
  const normalizedPlan = quota.subscription?.plan?.toLowerCase() ?? '';
  const isPremiumPlan =
    normalizedPlan === 'pro' || normalizedPlan === 'ultra' || normalizedPlan === 'ultra-lite';

  if (planLabel) {
    nodes.push(
      h(
        'div',
        { key: 'plan', className: styleMap.codexPlan },
        h('span', { className: styleMap.codexPlanLabel }, t('antigravity_quota.plan_label')),
        h(
          'span',
          { className: isPremiumPlan ? styleMap.premiumPlanValue : styleMap.codexPlanValue },
          planLabel
        )
      )
    );
  }

  if (groups.length === 0) {
    nodes.push(
      h(
        'div',
        { key: 'empty', className: styleMap.quotaMessage },
        t('antigravity_quota.empty_models')
      )
    );
    return h(Fragment, null, ...nodes);
  }

  const nowMs = Date.now() + (quota.serverTimeOffsetMs ?? 0);

  nodes.push(
    ...groups.flatMap((group) => {
      const groupLabel = translateAntigravityQuotaLabel(
        group.label,
        ANTIGRAVITY_GROUP_LABEL_KEYS,
        t
      );
      const groupDescription = translateAntigravityQuotaDescription(group.description, t);
      const groupHeader = h(
        'div',
        { key: `${group.id}-header`, className: styleMap.quotaMessage },
        groupDescription
          ? h('span', { title: groupDescription }, groupLabel)
          : h('span', null, groupLabel)
      );

      return [
        groupHeader,
        ...group.buckets.map((bucket) => {
          const clamped = Math.max(0, Math.min(1, bucket.remainingFraction));
          const percent = clamped * 100;
          const percentLabel =
            bucket.remainingFraction === 1
              ? t('antigravity_quota.quota_available')
              : t('antigravity_quota.remaining_percent', {
                  percent: Math.round(percent),
                });
          const resetLabel = formatAntigravityResetLabel(bucket.resetTime, t, nowMs);
          const bucketLabel = translateAntigravityQuotaLabel(
            bucket.label,
            ANTIGRAVITY_BUCKET_LABEL_KEYS,
            t
          );
          const bucketDescription = translateAntigravityQuotaDescription(bucket.description, t);

          return h(
            'div',
            { key: `${group.id}-${bucket.id}`, className: styleMap.quotaRow },
            h(
              'div',
              { className: styleMap.quotaRowHeader },
              h('span', { className: styleMap.quotaModel, title: bucketDescription }, bucketLabel),
              h(
                'div',
                { className: styleMap.quotaMeta },
                h('span', { className: styleMap.quotaPercent }, percentLabel),
                h('span', { className: styleMap.quotaReset }, resetLabel)
              )
            ),
            h(QuotaProgressBar, {
              percent,
              highThreshold: QUOTA_PROGRESS_HIGH_THRESHOLD,
              mediumThreshold: QUOTA_PROGRESS_MEDIUM_THRESHOLD,
            })
          );
        }),
      ];
    })
  );

  return h(Fragment, null, ...nodes);
};

const PREMIUM_CODEX_PLAN_TYPES = new Set(['pro', 'prolite', 'pro-lite', 'pro_lite']);

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

const getCodexEffectivePlanType = (file: AuthFileItem, quota?: CodexQuotaState): string | null =>
  resolveCodexPlanType(file) ?? quota?.planType ?? null;

const getCodexPlanSortRank = (file: AuthFileItem, quota?: CodexQuotaState): number | null => {
  const normalized = normalizePlanType(getCodexEffectivePlanType(file, quota));
  if (!normalized) return null;
  if (normalized === 'pro') return 50;
  if (PREMIUM_CODEX_PLAN_TYPES.has(normalized) && normalized !== 'pro') return 40;
  if (normalized === 'team') return 30;
  if (normalized === 'plus') return 20;
  if (normalized === 'free') return 10;
  return 0;
};

const getCodexSearchText = (
  file: AuthFileItem,
  quota: CodexQuotaState | undefined,
  t: TFunction
): unknown[] => {
  const planType = getCodexEffectivePlanType(file, quota);
  const planLabel = getCodexPlanLabel(planType, t);
  const accountId = resolveCodexChatgptAccountId(file);
  return [
    planType,
    planLabel,
    accountId,
    quota?.observedErrorKind,
    quota?.observedErrorCode,
    quota?.observedTraceId,
    quota?.activeLimit,
    quota?.creditsHasCredits,
    quota?.creditsUnlimited,
    quota?.creditsBalance,
    quota?.rateLimitReachedType,
    quota?.primaryOverSecondaryLimitPercent,
    quota?.observedAtMs,
  ];
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

const hasHeaderValue = (value: unknown): boolean => {
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

const mergeCodexQuotaWindow = (
  activeWindow: CodexQuotaWindow,
  observedWindow: CodexQuotaWindow
): CodexQuotaWindow => ({
  ...activeWindow,
  ...(hasHeaderValue(observedWindow.label) ? { label: observedWindow.label } : {}),
  ...(hasHeaderValue(observedWindow.labelKey) ? { labelKey: observedWindow.labelKey } : {}),
  ...(observedWindow.labelParams && Object.keys(observedWindow.labelParams).length > 0
    ? { labelParams: observedWindow.labelParams }
    : {}),
  ...(observedWindow.usedPercent !== null &&
  observedWindow.usedPercent !== undefined &&
  Number.isFinite(observedWindow.usedPercent)
    ? { usedPercent: observedWindow.usedPercent }
    : {}),
  ...(hasKnownResetLabel(observedWindow.resetLabel)
    ? { resetLabel: observedWindow.resetLabel }
    : {}),
  ...(observedWindow.limitWindowSeconds !== null &&
  observedWindow.limitWindowSeconds !== undefined &&
  observedWindow.limitWindowSeconds > 0
    ? { limitWindowSeconds: observedWindow.limitWindowSeconds }
    : {}),
});

const mergeCodexQuotaWindows = (
  activeWindows: CodexQuotaWindow[] | undefined,
  observedWindows: CodexQuotaWindow[] | undefined
): CodexQuotaWindow[] | undefined => {
  if (!observedWindows || observedWindows.length === 0) return activeWindows;
  if (!activeWindows || activeWindows.length === 0) return observedWindows;

  const observedById = new Map(observedWindows.map((window) => [window.id, window]));
  const mergedWindows = activeWindows.map((window) => {
    const observedWindow = observedById.get(window.id);
    if (!observedWindow) return window;
    observedById.delete(window.id);
    return mergeCodexQuotaWindow(window, observedWindow);
  });

  return [...mergedWindows, ...observedById.values()];
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

  const scalarKeys: Array<keyof CodexQuotaMergeState> = [
    'status',
    'planType',
    'activeLimit',
    'creditsHasCredits',
    'creditsUnlimited',
    'creditsBalance',
    'rateLimitReachedType',
    'primaryOverSecondaryLimitPercent',
    'observedAtMs',
    'observedTraceId',
    'observedErrorKind',
    'observedErrorCode',
  ];

  scalarKeys.forEach((key) => {
    const value = observed[key];
    if (hasHeaderValue(value)) {
      (merged as Record<string, unknown>)[key] = value;
    }
  });

  merged.windows = mergeCodexQuotaWindows(active.windows, observed.windows);
  if (observed.observedFromUsageHeaders === true) {
    merged.observedFromUsageHeaders = true;
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

const buildCodexQuotaAuthIdentity = (file: AuthFileItem | undefined) => {
  if (!file?.name) return {};
  const authIndex = normalizeAuthIndex(file['auth_index'] ?? file.authIndex ?? file['auth-index']);
  return {
    authFileKey: `${file.name}::${authIndex ?? '-'}`,
    authFileName: file.name,
    authIndex,
  };
};

export const getCodexQuotaStoreKey = (file: AuthFileItem): string =>
  buildCodexQuotaAuthIdentity(file).authFileKey ?? file.name;

const scopeCodexQuotaStateToAuthFile = (
  file: AuthFileItem,
  state: CodexQuotaState | undefined
): CodexQuotaState | undefined => {
  if (!state) return undefined;
  const identity = buildCodexQuotaAuthIdentity(file);
  if (!state.authFileKey) return identity.authIndex === null ? state : undefined;
  return state.authFileKey === identity.authFileKey ? state : undefined;
};

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
    ...buildCodexQuotaAuthIdentity(file),
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
      const fetchedAtMs = readFiniteTimestamp(activeQuota.fetchedAtMs);
      const observedAtMs = readFiniteTimestamp(observedQuota.observedAtMs);
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
  t: TFunction
): CodexQuotaState | undefined => {
  if (!hasUsageHeaderQuotaSignal(snapshot)) return undefined;
  const observedQuota = buildObservedCodexQuotaFromHeaderSnapshot(snapshot);
  const usedPercent = getHeaderSnapshotUsedPercent(snapshot);
  const recoverAtMS = getHeaderSnapshotRecoverAtMs(snapshot);
  const recoverLabel = recoverAtMS ? new Date(recoverAtMS).toLocaleString() : '-';
  const headerPlanType = observedQuota?.planType || getHeaderSnapshotPlanType(snapshot);
  const planType = resolveCodexPlanType(file) ?? (headerPlanType || null);
  const observedWindows = observedQuota?.payload
    ? buildCodexQuotaWindows(observedQuota.payload, t, planType)
    : [];
  const windows: CodexQuotaWindow[] =
    observedWindows.length > 0
      ? observedWindows
      : usedPercent !== null || recoverAtMS
        ? [
            {
              id: 'usage-header-observed',
              label: t('codex_quota.observed_window', {
                defaultValue: 'Latest request',
              }),
              usedPercent,
              resetLabel: recoverLabel,
            },
          ]
        : [];

  return {
    status: 'success',
    windows,
    planType,
    activeLimit: observedQuota?.activeLimit ?? null,
    creditsHasCredits: observedQuota?.creditsHasCredits ?? null,
    creditsUnlimited: observedQuota?.creditsUnlimited ?? null,
    creditsBalance: observedQuota?.creditsBalance ?? null,
    rateLimitReachedType: observedQuota?.rateLimitReachedType ?? null,
    primaryOverSecondaryLimitPercent: observedQuota?.primaryOverSecondaryLimitPercent ?? null,
    observedFromUsageHeaders: true,
    observedResetCreditsUnknown: true,
    observedAtMs: snapshot?.timestamp_ms,
    observedTraceId: getHeaderSnapshotTraceId(snapshot),
    observedErrorKind: getHeaderSnapshotErrorKind(snapshot),
    observedErrorCode: getHeaderSnapshotErrorCode(snapshot),
  };
};

type CodexQuotaTooltipRow = {
  key: string;
  label: string;
  value: string;
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

const formatCodexResetCreditExpiryTime = (expiresAt: string): string => {
  const expiresAtMs = new Date(expiresAt).getTime();
  if (!Number.isFinite(expiresAtMs)) return '-';
  return new Date(expiresAtMs).toLocaleString(undefined, {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  });
};

const renderCodexResetCreditExpiryInfo = (
  quota: CodexQuotaState,
  t: TFunction,
  styleMap: QuotaRenderHelpers['styles']
): ReactNode => {
  const creditExpiries = getSortedCodexResetCreditExpiries(quota.rateLimitResetCredits);
  if (creditExpiries.length === 0) return null;

  const { createElement: h, Fragment } = React;
  const earliestExpiryLabel = formatCodexResetCreditExpiryTime(creditExpiries[0].expiresAt);
  const rows = creditExpiries.map((credit, index) => ({
    key: `${credit.id}-${credit.expiresAt}`,
    label: t('codex_quota.reset_credit_expiry_item', { index: index + 1 }),
    value: formatCodexResetCreditExpiryTime(credit.expiresAt),
  }));

  return h(
    Fragment,
    null,
    h(
      'span',
      { key: 'reset-expiry-summary', className: styleMap.codexResetCreditExpiry },
      t('codex_quota.reset_credits_earliest_expiry', { time: earliestExpiryLabel })
    ),
    h(
      'span',
      {
        key: 'reset-expiry-info',
        className: styleMap.quotaInfoTrigger,
        tabIndex: 0,
        'aria-label': t('codex_quota.reset_credits_expiry_label'),
      },
      h(IconInfo, {
        key: 'icon',
        size: 14,
        className: styleMap.quotaInfoIcon,
        'aria-hidden': true,
        focusable: false,
      }),
      h(
        'span',
        { key: 'tooltip', className: styleMap.quotaInfoTooltip, role: 'tooltip' },
        ...rows.map((row) =>
          h(
            'span',
            { key: row.key, className: styleMap.quotaInfoTooltipRow },
            h('span', { className: styleMap.quotaInfoTooltipLabel }, row.label),
            h('span', { className: styleMap.quotaInfoTooltipValue }, row.value)
          )
        )
      )
    )
  );
};

const buildCodexWindowTooltipRows = (
  quota: CodexQuotaState,
  t: TFunction
): CodexQuotaTooltipRow[] => {
  const fromUsageHeaders = quota.observedFromUsageHeaders === true;
  const timestampMs = fromUsageHeaders ? quota.observedAtMs : quota.fetchedAtMs;
  const fetchedAt =
    timestampMs && Number.isFinite(timestampMs) ? new Date(timestampMs).toLocaleString() : '--';

  return [
    {
      key: 'source',
      label: t('codex_quota.tooltip_source_label'),
      value: fromUsageHeaders
        ? t('codex_quota.tooltip_source_header')
        : t('codex_quota.tooltip_source_api'),
    },
    {
      key: 'fetched-at',
      label: t('codex_quota.tooltip_fetched_at_label'),
      value: fetchedAt,
    },
  ];
};

const renderCodexWindowInfo = (
  quota: CodexQuotaState,
  window: CodexQuotaWindow,
  windowLabel: string,
  t: TFunction,
  styleMap: QuotaRenderHelpers['styles']
): ReactNode => {
  if (!CODEX_INFO_WINDOW_IDS.has(window.id)) return null;

  const { createElement: h } = React;
  const rows = buildCodexWindowTooltipRows(quota, t);

  return h(
    'span',
    {
      className: styleMap.quotaInfoTrigger,
      tabIndex: 0,
      'aria-label': t('codex_quota.tooltip_label', { label: windowLabel }),
    },
    h(IconInfo, {
      key: 'icon',
      size: 14,
      className: styleMap.quotaInfoIcon,
      'aria-hidden': true,
      focusable: false,
    }),
    h(
      'span',
      { key: 'tooltip', className: styleMap.quotaInfoTooltip, role: 'tooltip' },
      ...rows.map((row) =>
        h(
          'span',
          { key: row.key, className: styleMap.quotaInfoTooltipRow },
          h('span', { className: styleMap.quotaInfoTooltipLabel }, row.label),
          h('span', { className: styleMap.quotaInfoTooltipValue }, row.value)
        )
      )
    )
  );
};

const renderCodexItems = (
  quota: CodexQuotaState,
  t: TFunction,
  helpers: QuotaRenderHelpers
): ReactNode => {
  const { styles: styleMap, QuotaProgressBar } = helpers;
  const { createElement: h, Fragment } = React;
  const windows = quota.windows ?? [];
  const planType = quota.planType ?? null;
  const planLabel = getCodexPlanLabel(planType, t);
  const isPremiumPlan = PREMIUM_CODEX_PLAN_TYPES.has(normalizePlanType(planType) ?? '');
  const resetCreditsAvailableCount = quota.rateLimitResetCreditsAvailableCount;
  const hasResetCreditsAvailableCount =
    typeof resetCreditsAvailableCount === 'number' && Number.isFinite(resetCreditsAvailableCount);
  const nodes: ReactNode[] = [];

  if (planLabel || hasResetCreditsAvailableCount || quota.observedResetCreditsUnknown) {
    const valueClass = isPremiumPlan ? styleMap.premiumPlanValue : styleMap.codexPlanValue;
    const planNodes: ReactNode[] = [];

    if (planLabel) {
      planNodes.push(
        h(
          'span',
          { key: 'plan-label', className: styleMap.codexPlanLabel },
          t('codex_quota.plan_label')
        ),
        h('span', { key: 'plan-value', className: valueClass }, planLabel)
      );
    }

    if (hasResetCreditsAvailableCount || quota.observedResetCreditsUnknown) {
      if (planNodes.length > 0) {
        planNodes.push(
          h('span', { key: 'reset-separator', className: styleMap.codexPlanLabel }, '|')
        );
      }
      planNodes.push(
        h(
          'span',
          { key: 'reset-label', className: styleMap.codexPlanLabel },
          t('codex_quota.reset_credits_label')
        ),
        h(
          'span',
          { key: 'reset-value', className: styleMap.codexPlanValue },
          hasResetCreditsAvailableCount
            ? String(resetCreditsAvailableCount)
            : t('codex_quota.reset_credits_unknown')
        ),
        renderCodexResetCreditExpiryInfo(quota, t, styleMap)
      );
    }

    nodes.push(h('div', { key: 'plan', className: styleMap.codexPlan }, ...planNodes));
  }

  if (windows.length === 0) {
    nodes.push(
      h('div', { key: 'empty', className: styleMap.quotaMessage }, t('codex_quota.empty_windows'))
    );
    return h(Fragment, null, ...nodes);
  }

  nodes.push(
    ...windows.map((window) => {
      const used = window.usedPercent;
      const clampedUsed = used === null ? null : Math.max(0, Math.min(100, used));
      const remaining = clampedUsed === null ? null : Math.max(0, Math.min(100, 100 - clampedUsed));
      const percentLabel = remaining === null ? '--' : `${Math.round(remaining)}%`;
      const windowLabel = window.labelKey
        ? t(window.labelKey, window.labelParams as Record<string, string | number>)
        : window.label;
      const infoIcon = renderCodexWindowInfo(
        quota,
        window,
        windowLabel,
        t,
        styleMap
      );

      return h(
        'div',
        { key: window.id, className: styleMap.quotaRow },
        h(
          'div',
          { className: styleMap.quotaRowHeader },
          h(
            'span',
            { className: styleMap.quotaWindowLabel },
            h('span', { className: styleMap.quotaModel }, windowLabel),
            infoIcon
          ),
          h(
            'div',
            { className: styleMap.quotaMeta },
            h('span', { className: styleMap.quotaPercent }, percentLabel),
            h('span', { className: styleMap.quotaReset }, window.resetLabel)
          )
        ),
        h(QuotaProgressBar, {
          percent: remaining,
          highThreshold: QUOTA_PROGRESS_HIGH_THRESHOLD,
          mediumThreshold: QUOTA_PROGRESS_MEDIUM_THRESHOLD,
        })
      );
    })
  );

  return h(Fragment, null, ...nodes);
};

const renderClaudeItems = (
  quota: ClaudeQuotaState,
  t: TFunction,
  helpers: QuotaRenderHelpers
): ReactNode => {
  const { styles: styleMap, QuotaProgressBar } = helpers;
  const { createElement: h, Fragment } = React;
  const windows = quota.windows ?? [];
  const extraUsage = quota.extraUsage ?? null;
  const planType = quota.planType ?? null;
  const nodes: ReactNode[] = [];

  if (planType) {
    nodes.push(
      h(
        'div',
        { key: 'plan', className: styleMap.codexPlan },
        h('span', { className: styleMap.codexPlanLabel }, t('claude_quota.plan_label')),
        h('span', { className: styleMap.codexPlanValue }, t(`claude_quota.${planType}`))
      )
    );
  }

  if (extraUsage && extraUsage.is_enabled) {
    const usedLabel = `$${(extraUsage.used_credits / 100).toFixed(2)} / $${(extraUsage.monthly_limit / 100).toFixed(2)}`;
    nodes.push(
      h(
        'div',
        { key: 'extra', className: styleMap.codexPlan },
        h('span', { className: styleMap.codexPlanLabel }, t('claude_quota.extra_usage_label')),
        h('span', { className: styleMap.codexPlanValue }, usedLabel)
      )
    );
  }

  if (windows.length === 0) {
    nodes.push(
      h('div', { key: 'empty', className: styleMap.quotaMessage }, t('claude_quota.empty_windows'))
    );
    return h(Fragment, null, ...nodes);
  }

  nodes.push(
    ...windows.map((window) => {
      const used = window.usedPercent;
      const clampedUsed = used === null ? null : Math.max(0, Math.min(100, used));
      const remaining = clampedUsed === null ? null : Math.max(0, Math.min(100, 100 - clampedUsed));
      const percentLabel = remaining === null ? '--' : `${Math.round(remaining)}%`;
      const windowLabel = window.labelKey ? t(window.labelKey) : window.label;

      return h(
        'div',
        { key: window.id, className: styleMap.quotaRow },
        h(
          'div',
          { className: styleMap.quotaRowHeader },
          h('span', { className: styleMap.quotaModel }, windowLabel),
          h(
            'div',
            { className: styleMap.quotaMeta },
            h('span', { className: styleMap.quotaPercent }, percentLabel),
            h('span', { className: styleMap.quotaReset }, window.resetLabel)
          )
        ),
        h(QuotaProgressBar, {
          percent: remaining,
          highThreshold: QUOTA_PROGRESS_HIGH_THRESHOLD,
          mediumThreshold: QUOTA_PROGRESS_MEDIUM_THRESHOLD,
        })
      );
    })
  );

  return h(Fragment, null, ...nodes);
};

export const CLAUDE_CONFIG: QuotaConfig<
  ClaudeQuotaState,
  { windows: ClaudeQuotaWindow[]; extraUsage?: ClaudeExtraUsage | null; planType?: string | null }
> = {
  type: 'claude',
  i18nPrefix: 'claude_quota',
  cardIdleMessageKey: 'quota_management.card_idle_hint',
  filterFn: (file) => isClaudeFile(file) && !isDisabledAuthFile(file),
  fetchQuota: fetchClaudeQuota,
  storeSelector: (state) => state.claudeQuota,
  storeSetter: 'setClaudeQuota',
  buildLoadingState: () => ({ status: 'loading', windows: [] }),
  buildSuccessState: (data) => ({
    status: 'success',
    windows: data.windows,
    extraUsage: data.extraUsage,
    planType: data.planType,
  }),
  buildErrorState: (message, status) => ({
    status: 'error',
    windows: [],
    error: message,
    errorStatus: status,
  }),
  cardClassName: styles.claudeCard,
  controlsClassName: styles.claudeControls,
  controlClassName: styles.claudeControl,
  gridClassName: styles.claudeGrid,
  renderQuotaItems: renderClaudeItems,
};

export const ANTIGRAVITY_CONFIG: QuotaConfig<AntigravityQuotaState, AntigravityQuotaData> = {
  type: 'antigravity',
  i18nPrefix: 'antigravity_quota',
  cardIdleMessageKey: 'quota_management.card_idle_hint',
  filterFn: (file) => isAntigravityFile(file) && !isDisabledAuthFile(file),
  fetchQuota: fetchAntigravityQuota,
  storeSelector: (state) => state.antigravityQuota,
  storeSetter: 'setAntigravityQuota',
  buildLoadingState: () => ({
    status: 'loading',
    groups: [],
    subscription: null,
    serverTimeOffsetMs: null,
  }),
  buildSuccessState: (data) => ({
    status: 'success',
    groups: data.groups,
    subscription: data.subscription ?? null,
    serverTimeOffsetMs: data.serverTimeOffsetMs,
  }),
  buildErrorState: (message, status) => ({
    status: 'error',
    groups: [],
    subscription: null,
    serverTimeOffsetMs: null,
    error: message,
    errorStatus: status,
  }),
  cardClassName: styles.antigravityCard,
  controlsClassName: styles.antigravityControls,
  controlClassName: styles.antigravityControl,
  gridClassName: styles.antigravityGrid,
  renderQuotaItems: renderAntigravityItems,
};

export const CODEX_CONFIG: QuotaConfig<CodexQuotaState, CodexQuotaData> = {
  type: 'codex',
  i18nPrefix: 'codex_quota',
  cardIdleMessageKey: 'quota_management.card_idle_hint',
  filterFn: (file) => isCodexFile(file) && !isDisabledAuthFile(file),
  fetchQuota: fetchCodexQuota,
  storeSelector: (state) => state.codexQuota,
  storeSetter: 'setCodexQuota',
  getStoreKey: getCodexQuotaStoreKey,
  buildLoadingState: (file) => ({
    status: 'loading',
    windows: [],
    ...buildCodexQuotaAuthIdentity(file),
  }),
  buildSuccessState: (data, file) => ({
    status: 'success',
    windows: data.windows,
    planType: data.planType,
    subscriptionActiveUntil: data.subscriptionActiveUntil,
    rateLimitResetCreditsAvailableCount: data.rateLimitResetCreditsAvailableCount,
    rateLimitResetCredits: data.rateLimitResetCredits,
    rateLimitResetCreditsError: data.rateLimitResetCreditsError,
    ...buildCodexQuotaAuthIdentity(file),
    fetchedAtMs: Date.now(),
  }),
  buildErrorState: (message, status, file) => ({
    status: 'error',
    windows: [],
    error: message,
    errorStatus: status,
    failedAtMs: Date.now(),
    ...buildCodexQuotaAuthIdentity(file),
  }),
  buildFailureState: buildCodexQuotaFailureState,
  scopeState: scopeCodexQuotaStateToAuthFile,
  cardClassName: styles.codexCard,
  controlsClassName: styles.codexControls,
  controlClassName: styles.codexControl,
  gridClassName: styles.codexGrid,
  getSearchText: getCodexSearchText,
  getPlanSortRank: getCodexPlanSortRank,
  buildObservedState: buildObservedCodexQuotaState,
  resetQuota: resetCodexQuota,
  canResetQuota: (_file, quota) =>
    quota?.status === 'success' && (quota.rateLimitResetCreditsAvailableCount ?? 0) > 0,
  renderQuotaItems: renderCodexItems,
};

const renderKimiItems = (
  quota: KimiQuotaState,
  t: TFunction,
  helpers: QuotaRenderHelpers
): ReactNode => {
  const { styles: styleMap, QuotaProgressBar } = helpers;
  const { createElement: h } = React;
  const rows = quota.rows ?? [];

  if (rows.length === 0) {
    return h('div', { className: styleMap.quotaMessage }, t('kimi_quota.empty_data'));
  }

  return rows.map((row) => {
    const limit = row.limit;
    const used = row.used;
    const remaining =
      limit > 0
        ? Math.max(0, Math.min(100, Math.round(((limit - used) / limit) * 100)))
        : used > 0
          ? 0
          : null;
    const percentLabel = remaining === null ? '--' : `${remaining}%`;
    const rowLabel = row.labelKey
      ? t(row.labelKey, (row.labelParams ?? {}) as Record<string, string | number>)
      : (row.label ?? '');
    const resetLabel = formatKimiResetHint(t, row.resetHint);

    return h(
      'div',
      { key: row.id, className: styleMap.quotaRow },
      h(
        'div',
        { className: styleMap.quotaRowHeader },
        h('span', { className: styleMap.quotaModel }, rowLabel),
        h(
          'div',
          { className: styleMap.quotaMeta },
          h('span', { className: styleMap.quotaPercent }, percentLabel),
          resetLabel ? h('span', { className: styleMap.quotaReset }, resetLabel) : null
        )
      ),
      h(QuotaProgressBar, {
        percent: remaining,
        highThreshold: QUOTA_PROGRESS_HIGH_THRESHOLD,
        mediumThreshold: QUOTA_PROGRESS_MEDIUM_THRESHOLD,
      })
    );
  });
};

export const KIMI_CONFIG: QuotaConfig<KimiQuotaState, KimiQuotaRow[]> = {
  type: 'kimi',
  i18nPrefix: 'kimi_quota',
  cardIdleMessageKey: 'quota_management.card_idle_hint',
  filterFn: (file) => isKimiFile(file) && !isDisabledAuthFile(file),
  fetchQuota: fetchKimiQuota,
  storeSelector: (state) => state.kimiQuota,
  storeSetter: 'setKimiQuota',
  buildLoadingState: () => ({ status: 'loading', rows: [] }),
  buildSuccessState: (rows) => ({ status: 'success', rows }),
  buildErrorState: (message, status) => ({
    status: 'error',
    rows: [],
    error: message,
    errorStatus: status,
  }),
  cardClassName: styles.kimiCard,
  controlsClassName: styles.kimiControls,
  controlClassName: styles.kimiControl,
  gridClassName: styles.kimiGrid,
  renderQuotaItems: renderKimiItems,
};

const formatXaiCurrency = (value: number | null): string => {
  if (value === null) return '--';
  return `$${(value / 100).toFixed(2)}`;
};

const formatXaiRemainingAmount = (billing: XaiBillingSummary): string => {
  const remainingCents =
    billing.monthlyLimitCents !== null && billing.includedUsedCents !== null
      ? Math.max(0, billing.monthlyLimitCents - billing.includedUsedCents)
      : null;
  return `${formatXaiCurrency(remainingCents)} / ${formatXaiCurrency(billing.monthlyLimitCents)}`;
};

const formatXaiOnDemandAmount = (billing: XaiBillingSummary): string => {
  const remainingCents =
    billing.onDemandCapCents !== null && billing.onDemandUsedCents !== null
      ? Math.max(0, billing.onDemandCapCents - billing.onDemandUsedCents)
      : null;
  return `${formatXaiCurrency(remainingCents)} / ${formatXaiCurrency(billing.onDemandCapCents)}`;
};

const XAI_SUPERGROK_LIMIT_CENTS = 15_000;
const XAI_SUPERGROK_HEAVY_LIMIT_CENTS = 150_000;

const resolveXaiPlan = (
  monthlyLimitCents: number | null
): { labelKey: string; premium: boolean } | null => {
  if (monthlyLimitCents === XAI_SUPERGROK_LIMIT_CENTS) {
    return { labelKey: 'plan_supergrok', premium: false };
  }
  if (monthlyLimitCents === XAI_SUPERGROK_HEAVY_LIMIT_CENTS) {
    return { labelKey: 'plan_supergrok_heavy', premium: true };
  }
  return null;
};

const renderXaiItems = (
  quota: XaiQuotaState,
  t: TFunction,
  helpers: QuotaRenderHelpers
): ReactNode => {
  const { styles: styleMap, QuotaProgressBar } = helpers;
  const { createElement: h } = React;
  const billing = quota.billing;

  if (!billing) {
    return h('div', { className: styleMap.quotaMessage }, t('xai_quota.empty_data'));
  }

  const clampedUsed =
    billing.usedPercent === null ? null : Math.max(0, Math.min(100, billing.usedPercent));
  const remaining = clampedUsed === null ? null : Math.max(0, Math.min(100, 100 - clampedUsed));
  const percentLabel = remaining === null ? '--' : `${Math.round(remaining)}%`;
  const amountLabel = formatXaiRemainingAmount(billing);
  const resetLabel = billing.billingPeriodEnd
    ? formatQuotaResetTime(billing.billingPeriodEnd)
    : t('xai_quota.reset_unknown');
  const onDemandCap = billing.onDemandCapCents ?? 0;
  const clampedOnDemandUsed =
    billing.onDemandUsedPercent === null
      ? null
      : Math.max(0, Math.min(100, billing.onDemandUsedPercent));
  const onDemandRemaining =
    clampedOnDemandUsed === null ? null : Math.max(0, Math.min(100, 100 - clampedOnDemandUsed));
  const onDemandPercentLabel =
    onDemandRemaining === null ? '--' : `${Math.round(onDemandRemaining)}%`;
  const onDemandAmountLabel = formatXaiOnDemandAmount(billing);
  const plan = resolveXaiPlan(billing.monthlyLimitCents);

  const nodes: ReactNode[] = [
    plan
      ? h(
          'div',
          { key: 'plan', className: styleMap.codexPlan },
          h('span', { className: styleMap.codexPlanLabel }, t('xai_quota.plan_label')),
          h(
            'span',
            { className: plan.premium ? styleMap.premiumPlanValue : styleMap.codexPlanValue },
            t(`xai_quota.${plan.labelKey}`)
          )
        )
      : null,
    onDemandCap > 0
      ? h(
          'div',
          { key: 'pay-as-you-go', className: styleMap.quotaRow },
          h(
            'div',
            { className: styleMap.quotaRowHeader },
            h('span', { className: styleMap.quotaModel }, t('xai_quota.pay_as_you_go_label')),
            h(
              'div',
              { className: styleMap.quotaMeta },
              h('span', { className: styleMap.quotaPercent }, onDemandPercentLabel),
              h('span', { className: styleMap.quotaAmount }, onDemandAmountLabel)
            )
          ),
          h(QuotaProgressBar, {
            percent: onDemandRemaining,
            highThreshold: QUOTA_PROGRESS_HIGH_THRESHOLD,
            mediumThreshold: QUOTA_PROGRESS_MEDIUM_THRESHOLD,
          })
        )
      : h(
          'div',
          { key: 'pay-as-you-go', className: styleMap.codexPlan },
          h('span', { className: styleMap.codexPlanLabel }, t('xai_quota.pay_as_you_go_label')),
          h('span', { className: styleMap.codexPlanValue }, t('xai_quota.pay_as_you_go_disabled'))
        ),
    h(
      'div',
      { key: 'billing', className: styleMap.quotaRow },
      h(
        'div',
        { className: styleMap.quotaRowHeader },
        h('span', { className: styleMap.quotaModel }, t('xai_quota.monthly_credits')),
        h(
          'div',
          { className: styleMap.quotaMeta },
          h('span', { className: styleMap.quotaPercent }, percentLabel),
          h('span', { className: styleMap.quotaAmount }, amountLabel),
          h('span', { className: styleMap.quotaReset }, resetLabel)
        )
      ),
      h(QuotaProgressBar, {
        percent: remaining,
        highThreshold: QUOTA_PROGRESS_HIGH_THRESHOLD,
        mediumThreshold: QUOTA_PROGRESS_MEDIUM_THRESHOLD,
      })
    ),
  ];

  return h(React.Fragment, null, ...nodes);
};

export const XAI_CONFIG: QuotaConfig<XaiQuotaState, XaiBillingSummary> = {
  type: 'xai',
  i18nPrefix: 'xai_quota',
  cardIdleMessageKey: 'quota_management.card_idle_hint',
  filterFn: (file) => isXaiFile(file) && !isDisabledAuthFile(file),
  fetchQuota: fetchXaiQuota,
  storeSelector: (state) => state.xaiQuota,
  storeSetter: 'setXaiQuota',
  buildLoadingState: () => ({ status: 'loading', billing: null }),
  buildSuccessState: (billing) => ({ status: 'success', billing }),
  buildErrorState: (message, status) => ({
    status: 'error',
    billing: null,
    error: message,
    errorStatus: status,
  }),
  cardClassName: styles.kimiCard,
  controlsClassName: styles.kimiControls,
  controlClassName: styles.kimiControl,
  gridClassName: styles.kimiGrid,
  renderQuotaItems: renderXaiItems,
};
