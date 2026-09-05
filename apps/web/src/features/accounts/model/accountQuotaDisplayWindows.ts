import type { TFunction } from 'i18next';
import { getCredentialScopedQuotaState } from '@/utils/quota/credentialScope';
import type {
  AuthFileItem,
  ClaudeExtraUsage,
  CodexQuotaState,
  QuotaModelScope,
  QuotaObservationSource,
  QuotaResetAccuracy,
  QuotaWindowMode,
  XaiBillingSummary,
} from '@/types';
import {
  formatKimiResetHint,
  formatQuotaResetTime,
  isValidQuotaResetAtMs,
  parseQuotaResetLabelMs,
  resolveAbsoluteQuotaReset,
} from '@/utils/quota/formatters';
import {
  inferCodexQuotaScopeFromProviderWindowId,
  isCodexMainQuotaModelScope,
} from '@/utils/quota/codexQuota';
import type { AccountRow } from './accountRows';
import type { AccountQuotaStores } from './accountQuotaSummary';

export type AccountQuotaWindowKind =
  | 'five_hour'
  | 'daily'
  | 'weekly'
  | 'monthly'
  | 'billing'
  | 'payg'
  | 'product'
  | 'summary'
  | 'unknown';

export type AccountQuotaWindowSource =
  | 'codex'
  | 'claude'
  | 'antigravity'
  | 'kimi'
  | 'xai'
  | 'summary';

export interface AccountQuotaDisplayWindow {
  key: string;
  label: string;
  kind?: AccountQuotaWindowKind;
  remainingPercent: number | null;
  usedPercent: number | null;
  resetLabel: string;
  resetAccuracy: QuotaResetAccuracy;
  limitWindowSeconds: number | null;
  resetAtMs: number | null;
  fromMs: number | null;
  toMs: number | null;
  amountLabel?: string;
  description?: string;
  groupLabel?: string;
  source?: AccountQuotaWindowSource;
  observationSource?: QuotaObservationSource;
  observedAtMs?: number | null;
  quotaProgressObservedAtMs?: number | null;
  windowMode?: QuotaWindowMode;
  cycleStartMs?: number | null;
  cycleEndMs?: number | null;
  modelScope?: QuotaModelScope;
  providerWindowAliases?: string[];
}

export type TranslateQuotaWindowLabel = (
  label: string | undefined,
  labelKey?: string,
  labelParams?: Record<string, string | number>
) => string;

export interface BuildAccountQuotaDisplayWindowsOptions {
  stores: AccountQuotaStores;
  getDisplayCodexQuota?: (file: AuthFileItem) => CodexQuotaState | undefined;
  translateQuotaWindowLabel: TranslateQuotaWindowLabel;
  t: TFunction;
  nowMs?: number;
}

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

export const clampDisplayPercent = (value: number) => Math.max(0, Math.min(100, value));

export const remainingPercentFromUsed = (value: number | null | undefined) =>
  typeof value === 'number' && Number.isFinite(value) ? clampDisplayPercent(100 - value) : null;

export { parseQuotaResetLabelMs };

export const isIntervalAccountQuotaWindow = (
  window: Pick<AccountQuotaDisplayWindow, 'windowMode'>
): boolean =>
  window.windowMode === 'fixed' ||
  window.windowMode === 'calendar' ||
  window.windowMode === 'rolling';

export const isModelScopedAccountQuotaWindow = (
  window: Pick<AccountQuotaDisplayWindow, 'modelScope' | 'source'>
): boolean =>
  window.modelScope?.complete === false ||
  (window.modelScope?.kind !== undefined &&
    window.modelScope.kind !== 'all' &&
    !(window.source === 'codex' && isCodexMainQuotaModelScope(window.modelScope)));

export const isStandardAccountQuotaListWindow = (
  window: Pick<AccountQuotaDisplayWindow, 'kind' | 'windowMode' | 'modelScope' | 'source'>
): boolean =>
  isIntervalAccountQuotaWindow(window) &&
  !isModelScopedAccountQuotaWindow(window) &&
  window.kind !== 'billing' &&
  window.kind !== 'payg' &&
  window.kind !== 'product' &&
  window.kind !== 'summary';

const normalizeText = (value: string): string => value.trim().toLowerCase().replace(/\s+/g, ' ');

const parseAntigravityWindowSeconds = (value: string | undefined): number | null => {
  const normalized = value?.trim().toLowerCase().replace(/_/g, '-') ?? '';
  if (['5h', '5-hour', 'five-hour'].includes(normalized)) return 5 * 60 * 60;
  if (['24h', 'daily', 'day'].includes(normalized)) return 24 * 60 * 60;
  if (['7d', 'weekly', 'week'].includes(normalized)) return 7 * 24 * 60 * 60;
  const match = normalized.match(/^(\d+)\s*(m|h|d|w)$/);
  if (!match) return null;
  const amount = Number(match[1]);
  const multiplier = { m: 60, h: 3600, d: 86400, w: 604800 }[match[2]];
  return Number.isFinite(amount) && multiplier ? amount * multiplier : null;
};

const buildAntigravityWindowModelScope = (
  groupId: string,
  groupLabel: string,
  models: string[] | undefined
): QuotaModelScope => {
  if (models?.length) return { kind: 'models', models, complete: true };
  const normalized = `${groupId} ${groupLabel}`.toLowerCase();
  if (/gemini/.test(normalized)) return { kind: 'family', key: 'gemini', complete: true };
  if (/(claude|gpt|external)/.test(normalized)) {
    return { kind: 'family', key: 'claude_gpt', complete: true };
  }
  return { kind: 'all', complete: false };
};

const translateAntigravityQuotaLabel = (
  value: string,
  keys: Map<string, string>,
  t: TFunction
): string => {
  const key = keys.get(normalizeText(value));
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

const formatDisplayResetTime = (value: string | undefined): string => {
  if (!value) return '-';
  const formatted = formatQuotaResetTime(value);
  return formatted === '-' ? value : formatted;
};

const formatXaiCurrency = (value: number | null): string => {
  if (value === null) return '--';
  return `$${(value / 100).toFixed(2)}`;
};

const formatClaudeExtraUsageAmount = (extraUsage: ClaudeExtraUsage): string =>
  `$${(extraUsage.used_credits / 100).toFixed(2)} / $${(extraUsage.monthly_limit / 100).toFixed(2)}`;

const getClaudeExtraUsageUsedPercent = (extraUsage: ClaudeExtraUsage): number | null => {
  if (typeof extraUsage.utilization === 'number' && Number.isFinite(extraUsage.utilization)) {
    return clampDisplayPercent(extraUsage.utilization);
  }
  if (extraUsage.monthly_limit > 0) {
    return clampDisplayPercent((extraUsage.used_credits / extraUsage.monthly_limit) * 100);
  }
  return null;
};

const formatXaiMonthlyAmount = (billing: XaiBillingSummary, t: TFunction): string => {
  const remainingCents =
    billing.monthlyLimitCents !== null && billing.includedUsedCents !== null
      ? Math.max(0, billing.monthlyLimitCents - billing.includedUsedCents)
      : null;
  return t('xai_quota.usage_amount', {
    remaining: formatXaiCurrency(remainingCents),
    limit: formatXaiCurrency(billing.monthlyLimitCents),
  });
};

const formatXaiPaygAmount = (billing: XaiBillingSummary, t: TFunction): string => {
  const remainingCents =
    billing.onDemandCapCents !== null && billing.onDemandUsedCents !== null
      ? Math.max(0, billing.onDemandCapCents - billing.onDemandUsedCents)
      : null;
  return t('xai_quota.usage_amount', {
    remaining: formatXaiCurrency(remainingCents),
    limit: formatXaiCurrency(billing.onDemandCapCents),
  });
};

const getXaiPeriodWindowLabel = (billing: XaiBillingSummary, t: TFunction): string => {
  if (billing.periodType === 'weekly') return t('xai_quota.weekly_credits');
  if (billing.periodType === 'monthly') return t('xai_quota.monthly_credits');
  return t('xai_quota.monthly_credits');
};

const durationKindFromSeconds = (
  value: number | null | undefined
): AccountQuotaWindowKind | null => {
  if (typeof value !== 'number' || !Number.isFinite(value) || value <= 0) return null;
  if (value >= 4 * 60 * 60 && value <= 6 * 60 * 60) return 'five_hour';
  if (value >= 23 * 60 * 60 && value <= 25 * 60 * 60) return 'daily';
  if (value >= 6 * 24 * 60 * 60 && value <= 8 * 24 * 60 * 60) return 'weekly';
  if (value >= 27 * 24 * 60 * 60 && value <= 32 * 24 * 60 * 60) return 'monthly';
  return null;
};

export const inferAccountQuotaWindowKind = ({
  key,
  label,
  limitWindowSeconds,
}: {
  key: string;
  label: string;
  limitWindowSeconds?: number | null;
}): AccountQuotaWindowKind | null => {
  const durationKind = durationKindFromSeconds(limitWindowSeconds);
  if (durationKind) return durationKind;

  const text = `${key} ${label}`.toLowerCase();
  if (/(pay-as-you-go|payg|on[-_\s]?demand|按量|按需)/.test(text)) return 'payg';
  if (/(billing|账单|帳單)/.test(text)) return 'billing';
  if (/(product|model|模型|产品|產品)/.test(text)) return 'product';
  if (/(month|monthly|30d|31d|月)/.test(text)) return 'monthly';
  if (/(week|weekly|7d|7 day|seven|周|週)/.test(text)) return 'weekly';
  if (/(day|daily|24h|24 h|日)/.test(text)) return 'daily';
  if (/(five|5h|5 h|5-hour|5_hour|five-hour|5小时|5 小时|五小时|primary)/.test(text)) {
    return 'five_hour';
  }
  if (/(summary|quota|额度|額度)/.test(text)) return 'summary';
  return null;
};

export const getQuotaWindowShortLabel = (window: AccountQuotaDisplayWindow) => {
  const kind =
    window.kind ??
    inferAccountQuotaWindowKind({
      key: window.key,
      label: window.label,
      limitWindowSeconds: window.limitWindowSeconds,
    });

  if (kind === 'five_hour') return '5H';
  if (kind === 'daily') return '24H';
  if (kind === 'weekly') return '7D';
  if (kind === 'monthly' || kind === 'billing') return '30D';
  if (kind === 'payg') return 'PAYG';
  if (kind === 'product') return 'PROD';
  if (kind === 'summary') return 'SUM';
  return window.label.slice(0, 3).toUpperCase();
};

export const buildQuotaWindowRange = (
  resetLabel: string,
  limitWindowSeconds: number | null | undefined,
  nowMs = Date.now(),
  normalizedResetAtMs?: number | null
) => {
  const resetAtMs = isValidQuotaResetAtMs(normalizedResetAtMs)
    ? normalizedResetAtMs
    : parseQuotaResetLabelMs(resetLabel, nowMs);
  if (!limitWindowSeconds || limitWindowSeconds <= 0) {
    return { resetAtMs, fromMs: null, toMs: null };
  }
  if (!resetAtMs) return { resetAtMs: null, fromMs: null, toMs: null };
  const durationMs = Math.round(limitWindowSeconds * 1000);
  const fromMs = resetAtMs - durationMs;
  const toMs = Math.min(nowMs, resetAtMs);
  if (fromMs <= 0 || toMs <= fromMs) {
    return { resetAtMs, fromMs: null, toMs: null };
  }
  return { resetAtMs, fromMs, toMs };
};

const resolveQuotaProgressObservedAtMs = ({
  usedPercent,
  quotaProgressObservedAtMs,
  observedAtMs,
}: {
  usedPercent: number | null;
  quotaProgressObservedAtMs: number | null | undefined;
  observedAtMs: number | null | undefined;
}): number | null => {
  if (typeof usedPercent !== 'number' || !Number.isFinite(usedPercent)) return null;
  if (quotaProgressObservedAtMs !== undefined) {
    return typeof quotaProgressObservedAtMs === 'number' &&
      Number.isFinite(quotaProgressObservedAtMs) &&
      quotaProgressObservedAtMs > 0
      ? quotaProgressObservedAtMs
      : null;
  }
  return typeof observedAtMs === 'number' && Number.isFinite(observedAtMs) && observedAtMs > 0
    ? observedAtMs
    : null;
};

export const buildAccountQuotaDisplayWindow = ({
  key,
  label,
  kind,
  remainingPercent,
  usedPercent,
  resetLabel,
  resetAtMs,
  resetAccuracy = 'unknown',
  limitWindowSeconds = null,
  amountLabel,
  description,
  groupLabel,
  source,
  observationSource = 'api_query',
  observedAtMs = null,
  quotaProgressObservedAtMs,
  windowMode,
  cycleStartMs,
  cycleEndMs,
  modelScope = { kind: 'all', complete: true },
  providerWindowAliases,
  nowMs,
}: {
  key: string;
  label: string;
  kind?: AccountQuotaWindowKind;
  remainingPercent: number | null;
  usedPercent: number | null;
  resetLabel: string;
  resetAtMs?: number | null;
  resetAccuracy?: QuotaResetAccuracy;
  limitWindowSeconds?: number | null;
  amountLabel?: string;
  description?: string;
  groupLabel?: string;
  source?: AccountQuotaWindowSource;
  observationSource?: QuotaObservationSource;
  observedAtMs?: number | null;
  quotaProgressObservedAtMs?: number | null;
  windowMode?: QuotaWindowMode;
  cycleStartMs?: number | null;
  cycleEndMs?: number | null;
  modelScope?: QuotaModelScope;
  providerWindowAliases?: string[];
  nowMs?: number;
}): AccountQuotaDisplayWindow => {
  const normalizedResetLabel = resetLabel || '-';
  const normalizedLimitWindowSeconds = limitWindowSeconds ?? null;
  const hasNormalizedResetAt = isValidQuotaResetAtMs(resetAtMs);
  const range = buildQuotaWindowRange(
    normalizedResetLabel,
    normalizedLimitWindowSeconds,
    nowMs,
    resetAtMs
  );
  const resolvedKind =
    kind ??
    inferAccountQuotaWindowKind({
      key,
      label,
      limitWindowSeconds: normalizedLimitWindowSeconds,
    }) ??
    undefined;
  const resolvedMode =
    windowMode ??
    (range.fromMs !== null && range.resetAtMs !== null
      ? 'fixed'
      : resolvedKind === 'billing' ||
          resolvedKind === 'payg' ||
          resolvedKind === 'product' ||
          resolvedKind === 'summary'
        ? 'non_window'
        : 'unknown');
  return {
    key,
    label,
    kind: resolvedKind,
    remainingPercent,
    usedPercent,
    resetLabel: normalizedResetLabel,
    resetAccuracy: range.resetAtMs === null || !hasNormalizedResetAt ? 'unknown' : resetAccuracy,
    limitWindowSeconds: normalizedLimitWindowSeconds,
    amountLabel,
    description,
    groupLabel,
    source,
    observationSource,
    observedAtMs,
    quotaProgressObservedAtMs: resolveQuotaProgressObservedAtMs({
      usedPercent,
      quotaProgressObservedAtMs,
      observedAtMs,
    }),
    windowMode: resolvedMode,
    cycleStartMs: cycleStartMs ?? range.fromMs,
    cycleEndMs: cycleEndMs ?? range.resetAtMs,
    modelScope,
    providerWindowAliases,
    ...range,
  };
};

const buildCodexQuotaDisplayWindows = (
  row: AccountRow,
  options: BuildAccountQuotaDisplayWindowsOptions
): AccountQuotaDisplayWindow[] => {
  const quota =
    options.getDisplayCodexQuota?.(row.raw) ??
    getCredentialScopedQuotaState(options.stores.codexQuota, row.raw);
  if (!quota?.windows?.length) return [];
  return quota.windows.map((window) =>
    buildAccountQuotaDisplayWindow({
      key: window.id,
      label: options.translateQuotaWindowLabel(window.label, window.labelKey, window.labelParams),
      remainingPercent: remainingPercentFromUsed(window.usedPercent),
      usedPercent: window.usedPercent,
      resetLabel: window.resetLabel || '-',
      resetAtMs: window.resetAtMs,
      resetAccuracy: window.resetAccuracy,
      limitWindowSeconds: window.limitWindowSeconds ?? null,
      modelScope: window.modelScope ?? inferCodexQuotaScopeFromProviderWindowId(window.id),
      providerWindowAliases: window.providerWindowAliases,
      source: 'codex',
      observationSource:
        window.observationSource ??
        (quota.observedFromUsageHeaders && quota.fetchedAtMs === undefined
          ? 'response_header'
          : 'api_query'),
      observedAtMs: window.observedAtMs ?? quota.observedAtMs ?? quota.fetchedAtMs ?? null,
      quotaProgressObservedAtMs: window.quotaProgressObservedAtMs,
      nowMs: options.nowMs,
    })
  );
};

const buildClaudeQuotaDisplayWindows = (
  row: AccountRow,
  options: BuildAccountQuotaDisplayWindowsOptions
): AccountQuotaDisplayWindow[] => {
  const quota = getCredentialScopedQuotaState(options.stores.claudeQuota, row.raw);
  if (!quota) return [];
  const windows =
    quota.windows?.map((window) =>
      buildAccountQuotaDisplayWindow({
        key: window.id,
        label: options.translateQuotaWindowLabel(window.label, window.labelKey),
        remainingPercent: remainingPercentFromUsed(window.usedPercent),
        usedPercent: window.usedPercent,
        resetLabel: window.resetLabel || '-',
        resetAtMs: window.resetAtMs,
        resetAccuracy: window.resetAccuracy,
        limitWindowSeconds: window.limitWindowSeconds ?? null,
        modelScope: window.modelScope ?? { kind: 'all', complete: true },
        source: 'claude',
        observedAtMs: quota.fetchedAtMs ?? null,
        nowMs: options.nowMs,
      })
    ) ?? [];

  if (quota.extraUsage?.is_enabled) {
    const usedPercent = getClaudeExtraUsageUsedPercent(quota.extraUsage);
    windows.push(
      buildAccountQuotaDisplayWindow({
        key: 'extra-usage',
        label: options.t('claude_quota.extra_usage_label'),
        kind: 'monthly',
        remainingPercent: remainingPercentFromUsed(usedPercent),
        usedPercent,
        resetLabel: '-',
        amountLabel: formatClaudeExtraUsageAmount(quota.extraUsage),
        source: 'claude',
        nowMs: options.nowMs,
      })
    );
  }

  return windows;
};

const buildAntigravityQuotaDisplayWindows = (
  row: AccountRow,
  options: BuildAccountQuotaDisplayWindowsOptions
): AccountQuotaDisplayWindow[] => {
  const quota = getCredentialScopedQuotaState(options.stores.antigravityQuota, row.raw);
  const groups = quota?.groups ?? [];
  return groups.flatMap((group) => {
    const groupLabel = translateAntigravityQuotaLabel(
      group.label,
      ANTIGRAVITY_GROUP_LABEL_KEYS,
      options.t
    );
    const groupDescription = translateAntigravityQuotaDescription(group.description, options.t);

    return group.buckets.map((bucket) => {
      const reset = resolveAbsoluteQuotaReset(bucket.resetTime);
      const remainingPercent = clampDisplayPercent(bucket.remainingFraction * 100);
      const label = translateAntigravityQuotaLabel(
        bucket.label || bucket.id,
        ANTIGRAVITY_BUCKET_LABEL_KEYS,
        options.t
      );
      const description =
        translateAntigravityQuotaDescription(bucket.description, options.t) ?? groupDescription;
      const kind =
        inferAccountQuotaWindowKind({
          key: bucket.window ?? '',
          label,
        }) ??
        inferAccountQuotaWindowKind({
          key: bucket.id,
          label,
        }) ??
        undefined;
      return buildAccountQuotaDisplayWindow({
        key: `${group.id}:${bucket.id}`,
        label,
        kind,
        remainingPercent,
        usedPercent: clampDisplayPercent(100 - remainingPercent),
        resetLabel: formatDisplayResetTime(bucket.resetTime),
        resetAtMs: reset.resetAtMs,
        resetAccuracy: reset.resetAccuracy,
        limitWindowSeconds: parseAntigravityWindowSeconds(bucket.window),
        description,
        groupLabel,
        modelScope: buildAntigravityWindowModelScope(group.id, group.label, group.models),
        source: 'antigravity',
        observedAtMs: quota?.fetchedAtMs ?? null,
        nowMs: options.nowMs,
      });
    });
  });
};

const buildKimiQuotaDisplayWindows = (
  row: AccountRow,
  options: BuildAccountQuotaDisplayWindowsOptions
): AccountQuotaDisplayWindow[] => {
  const quota = getCredentialScopedQuotaState(options.stores.kimiQuota, row.raw);
  if (!quota?.rows?.length) return [];
  return quota.rows.map((quotaRow) => {
    const remainingPercent =
      quotaRow.limit > 0
        ? clampDisplayPercent(((quotaRow.limit - quotaRow.used) / quotaRow.limit) * 100)
        : null;
    const label = options.translateQuotaWindowLabel(
      quotaRow.label,
      quotaRow.labelKey,
      quotaRow.labelParams
    );
    return buildAccountQuotaDisplayWindow({
      key: quotaRow.id,
      label,
      remainingPercent,
      usedPercent: remainingPercent === null ? null : clampDisplayPercent(100 - remainingPercent),
      resetLabel: formatKimiResetHint(options.t, quotaRow.resetHint) || '-',
      resetAtMs: quotaRow.resetAtMs,
      resetAccuracy: quotaRow.resetAccuracy,
      limitWindowSeconds: quotaRow.limitWindowSeconds ?? null,
      // Kimi's scope is a product/feature label, not an explicit model set.
      // Aggregate the credential as a whole until the provider returns models.
      modelScope: { kind: 'all', complete: true },
      amountLabel: `${quotaRow.used} / ${quotaRow.limit}`,
      source: 'kimi',
      observedAtMs: quota.fetchedAtMs ?? null,
      nowMs: options.nowMs,
    });
  });
};

const buildXaiQuotaDisplayWindows = (
  row: AccountRow,
  options: BuildAccountQuotaDisplayWindowsOptions
): AccountQuotaDisplayWindow[] => {
  const quota = getCredentialScopedQuotaState(options.stores.xaiQuota, row.raw);
  const billing = quota?.billing;
  if (!billing || billing.officialApiHealth) return [];

  const resetLabel = billing.billingPeriodEnd
    ? formatDisplayResetTime(billing.billingPeriodEnd)
    : '-';
  const billingReset = resolveAbsoluteQuotaReset(billing.billingPeriodEnd);
  const periodResetValue = billing.periodEnd ?? billing.billingPeriodEnd;
  const periodResetLabel = periodResetValue ? formatDisplayResetTime(periodResetValue) : '-';
  const periodReset = resolveAbsoluteQuotaReset(periodResetValue);
  const periodStart = resolveAbsoluteQuotaReset(billing.periodStart);
  const periodDurationSeconds =
    periodStart.resetAtMs && periodReset.resetAtMs && periodReset.resetAtMs > periodStart.resetAtMs
      ? Math.round((periodReset.resetAtMs - periodStart.resetAtMs) / 1000)
      : null;
  const windows: AccountQuotaDisplayWindow[] = [];
  const periodUsedPercent =
    typeof billing.usagePercent === 'number' && Number.isFinite(billing.usagePercent)
      ? clampDisplayPercent(billing.usagePercent)
      : null;

  if (billing.periodType === 'weekly' || (billing.productUsage?.length ?? 0) > 0) {
    windows.push(
      buildAccountQuotaDisplayWindow({
        key: 'credits-period',
        label: getXaiPeriodWindowLabel(billing, options.t),
        kind: billing.periodType === 'weekly' ? 'weekly' : 'billing',
        remainingPercent: remainingPercentFromUsed(periodUsedPercent),
        usedPercent: periodUsedPercent,
        resetLabel: periodResetLabel,
        resetAtMs: periodReset.resetAtMs,
        resetAccuracy: periodReset.resetAccuracy,
        limitWindowSeconds: periodDurationSeconds,
        cycleStartMs: periodStart.resetAtMs,
        cycleEndMs: periodReset.resetAtMs,
        windowMode: periodDurationSeconds ? 'fixed' : 'unknown',
        source: 'xai',
        observedAtMs: quota?.fetchedAtMs ?? null,
        nowMs: options.nowMs,
      })
    );
  }

  const monthlyUsedPercent =
    typeof billing.usedPercent === 'number' && Number.isFinite(billing.usedPercent)
      ? clampDisplayPercent(billing.usedPercent)
      : null;

  if (monthlyUsedPercent !== null || billing.monthlyLimitCents !== null) {
    windows.push(
      buildAccountQuotaDisplayWindow({
        key: 'billing',
        label: options.t('xai_quota.monthly_credits'),
        kind: 'billing',
        remainingPercent: remainingPercentFromUsed(monthlyUsedPercent),
        usedPercent: monthlyUsedPercent,
        resetLabel,
        resetAtMs: billingReset.resetAtMs,
        resetAccuracy: billingReset.resetAccuracy,
        amountLabel: formatXaiMonthlyAmount(billing, options.t),
        source: 'xai',
        observedAtMs: quota?.fetchedAtMs ?? null,
        nowMs: options.nowMs,
      })
    );
  }

  const onDemandCap = billing.onDemandCapCents ?? 0;
  if (onDemandCap > 0) {
    const paygUsedPercent =
      typeof billing.onDemandUsedPercent === 'number' &&
      Number.isFinite(billing.onDemandUsedPercent)
        ? clampDisplayPercent(billing.onDemandUsedPercent)
        : null;
    windows.push(
      buildAccountQuotaDisplayWindow({
        key: 'pay-as-you-go',
        label: options.t('xai_quota.pay_as_you_go_label'),
        kind: 'payg',
        remainingPercent: remainingPercentFromUsed(paygUsedPercent),
        usedPercent: paygUsedPercent,
        resetLabel,
        resetAtMs: billingReset.resetAtMs,
        resetAccuracy: billingReset.resetAccuracy,
        amountLabel: formatXaiPaygAmount(billing, options.t),
        source: 'xai',
        observedAtMs: quota?.fetchedAtMs ?? null,
        nowMs: options.nowMs,
      })
    );
  }

  billing.productUsage?.forEach((product, index) => {
    const productUsedPercent =
      typeof product.usagePercent === 'number' && Number.isFinite(product.usagePercent)
        ? clampDisplayPercent(product.usagePercent)
        : null;
    windows.push(
      buildAccountQuotaDisplayWindow({
        key: `product-${index}-${product.product
          .toLowerCase()
          .replace(/[^a-z0-9]+/g, '-')
          .replace(/^-+|-+$/g, '')}`,
        label: product.product,
        kind: 'product',
        remainingPercent: remainingPercentFromUsed(productUsedPercent),
        usedPercent: productUsedPercent,
        resetLabel: periodResetLabel,
        resetAtMs: periodReset.resetAtMs,
        resetAccuracy: periodReset.resetAccuracy,
        source: 'xai',
        observedAtMs: quota?.fetchedAtMs ?? null,
        nowMs: options.nowMs,
      })
    );
  });

  return windows;
};

const buildSummaryQuotaDisplayWindow = (
  row: AccountRow,
  options: BuildAccountQuotaDisplayWindowsOptions
): AccountQuotaDisplayWindow[] => {
  if (row.quota.remainingPercent === null && row.quota.usedPercent === null) return [];
  return [
    buildAccountQuotaDisplayWindow({
      key: 'summary',
      label: options.t('accounts.col_quota'),
      kind: 'summary',
      remainingPercent: row.quota.remainingPercent,
      usedPercent: row.quota.usedPercent,
      resetLabel: row.quota.resetLabel,
      resetAtMs: row.quota.resetAtMs,
      resetAccuracy: row.quota.resetAccuracy,
      source: 'summary',
      nowMs: options.nowMs,
    }),
  ];
};

export const buildAccountQuotaDisplayWindows = (
  row: AccountRow,
  options: BuildAccountQuotaDisplayWindowsOptions
): AccountQuotaDisplayWindow[] => {
  if (row.provider === 'codex') {
    const windows = buildCodexQuotaDisplayWindows(row, options);
    if (windows.length) return windows;
  }

  if (row.provider === 'claude') {
    const windows = buildClaudeQuotaDisplayWindows(row, options);
    if (windows.length) return windows;
  }

  if (row.provider === 'antigravity') {
    const windows = buildAntigravityQuotaDisplayWindows(row, options);
    if (windows.length) return windows;
  }

  if (row.provider === 'kimi') {
    const windows = buildKimiQuotaDisplayWindows(row, options);
    if (windows.length) return windows;
  }

  if (row.provider === 'xai') {
    const windows = buildXaiQuotaDisplayWindows(row, options);
    if (windows.length) return windows;
  }

  return buildSummaryQuotaDisplayWindow(row, options);
};
