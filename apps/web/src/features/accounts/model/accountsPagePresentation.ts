import type { TFunction } from 'i18next';
import { ANTIGRAVITY_CONFIG } from '@/components/quota';
import { getQuotaWindowShortLabel } from '@/features/accounts/model/accountQuotaDisplayWindows';
import type {
  AccountQuotaWindowKind,
  AccountQuotaDisplayWindow,
} from '@/features/accounts/model/accountQuotaDisplayWindows';
import type {
  AccountRow,
  AccountRowSortDirection,
  AccountRowSortKey,
} from '@/features/accounts/model/accountRows';
import {
  getAuthFilePatchTarget,
  type AuthFileCodexInspectionSnapshot,
} from '@/features/authFiles/model/credentialStatus';
import type { MonitoringAccountHistoryItem, MonitoringAnalyticsEventRow } from '@/services/api';
import { parseQuotaResetLabelMs } from '@/utils/quota/formatters';
import { formatUsd } from '@/utils/usage';
import { getEffectiveAccountInspectionAction } from './accountCredentialEvidence';

export type AccountsView = 'accounts' | 'health' | 'oauth';
export type DetailTab = 'overview' | 'quota' | 'config' | 'models' | 'diagnostics';
export type SortableAccountColumn = Extract<
  AccountRowSortKey,
  'name' | 'plan' | 'note' | 'reset' | 'priority' | 'recent' | 'quota' | 'created'
>;
export type AccountSortFieldValue = 'default' | SortableAccountColumn;
type AntigravityQuotaMatrixWindowKind = Extract<AccountQuotaWindowKind, 'five_hour' | 'weekly'>;

export interface AntigravityQuotaMatrixCell {
  groupLabel: string;
  displayLabel: string;
  window: AccountQuotaDisplayWindow;
}

export interface AntigravityQuotaMatrixRow {
  key: AntigravityQuotaMatrixWindowKind;
  label: string;
  cells: AntigravityQuotaMatrixCell[];
}

export interface AntigravityQuotaMatrix {
  rows: AntigravityQuotaMatrixRow[];
  windowKeys: Set<string>;
}

export const PAGE_SIZE_OPTIONS = [
  { value: '10', label: '10' },
  { value: '20', label: '20' },
  { value: '50', label: '50' },
];

export const DETAIL_EVENTS_RANGE_MS = 7 * 24 * 60 * 60 * 1000;
export const DETAIL_EVENTS_LIMIT = 20;

export const ACCOUNT_SORT_DEFAULT_DIRECTIONS: Record<
  SortableAccountColumn,
  AccountRowSortDirection
> = {
  name: 'asc',
  plan: 'asc',
  note: 'asc',
  reset: 'asc',
  priority: 'desc',
  recent: 'desc',
  quota: 'desc',
  created: 'desc',
};

const DEFAULT_ACCOUNT_SORT_FIELD_OPTION = {
  value: 'default',
  labelKey: 'accounts.sort_default',
} as const;

export const ACCOUNT_SORT_FIELD_OPTIONS: Array<{
  value: AccountSortFieldValue;
  labelKey: string;
}> = [
  DEFAULT_ACCOUNT_SORT_FIELD_OPTION,
  { value: 'name', labelKey: 'accounts.sort_name' },
  { value: 'plan', labelKey: 'accounts.col_plan' },
  { value: 'note', labelKey: 'auth_files.note_label' },
  { value: 'reset', labelKey: 'accounts.col_reset' },
  { value: 'quota', labelKey: 'accounts.col_quota' },
  { value: 'priority', labelKey: 'accounts.col_priority' },
  { value: 'recent', labelKey: 'accounts.col_recent' },
  { value: 'created', labelKey: 'accounts.col_created' },
];

export const getAccountSortFieldOption = (value: AccountSortFieldValue) =>
  ACCOUNT_SORT_FIELD_OPTIONS.find((option) => option.value === value) ??
  DEFAULT_ACCOUNT_SORT_FIELD_OPTION;

export const getProviderLabel = (provider: string, t: TFunction) => {
  const key = `auth_files.filter_${provider}`;
  const translated = t(key);
  if (translated !== key) return translated;
  if (provider === 'all') return t('accounts.filter_all');
  if (provider === 'iflow') return 'iFlow';
  if (provider === 'xai') return 'xAI';
  return provider.charAt(0).toUpperCase() + provider.slice(1);
};

export const formatPercent = (value: number | null | undefined, digits = 0) =>
  typeof value === 'number' && Number.isFinite(value) ? `${value.toFixed(digits)}%` : '-';

export const formatMoney = (value: number) => formatUsd(value);

export const formatHistoryNumber = (value: number, locale: string) => {
  const numberValue = Number(value);
  if (!Number.isFinite(numberValue)) return '-';
  return new Intl.NumberFormat(locale || undefined).format(numberValue);
};

export const formatHistorySuccessRate = (value: number | null | undefined, digits = 1) =>
  typeof value === 'number' && Number.isFinite(value) ? formatPercent(value * 100, digits) : '-';

export const getAccountHistoryTitle = (
  t: TFunction,
  item: MonitoringAccountHistoryItem | null,
  loading: boolean,
  error: string,
  locale = 'en-US'
) => {
  if (error) return t('accounts.history_unavailable');
  if (loading && !item) return t('accounts.history_loading');
  if (!item || !item.matched) return t('accounts.history_empty');
  if (item.sync_status === 'pending') return t('accounts.history_pending_title');
  return t('accounts.history_title', {
    requests: formatHistoryNumber(item.total_requests, locale),
    tokens: formatHistoryNumber(item.total_tokens, locale),
    cost: formatMoney(item.total_cost),
    rate: formatHistorySuccessRate(item.success_rate, 2),
  });
};

export const parsePriorityValue = (value: string) => {
  const trimmed = value.trim();
  if (!/^-?\d+$/.test(trimmed)) return null;
  const parsed = Number(trimmed);
  return Number.isSafeInteger(parsed) ? parsed : null;
};

const resolveValidTimestampDate = (value: number | null | undefined): Date | null => {
  if (typeof value !== 'number' || !Number.isFinite(value) || value === 0) return null;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? null : date;
};

const padTimestampPart = (value: number) => String(value).padStart(2, '0');

const formatNumericTimestamp = (date: Date, includeSeconds = false) => {
  const base = `${padTimestampPart(date.getMonth() + 1)}/${padTimestampPart(
    date.getDate()
  )} ${padTimestampPart(date.getHours())}:${padTimestampPart(date.getMinutes())}`;
  return includeSeconds ? `${base}:${padTimestampPart(date.getSeconds())}` : base;
};

export const formatTimestamp = (value: number | null, _locale: string, includeSeconds = false) => {
  const date = resolveValidTimestampDate(value);
  if (!date) return '-';
  return formatNumericTimestamp(date, includeSeconds);
};

export const formatTimestampTitle = (
  value: number | null | undefined,
  locale: string
): string | undefined => {
  const date = resolveValidTimestampDate(value);
  if (!date) return undefined;
  try {
    return new Intl.DateTimeFormat(locale, {
      dateStyle: 'medium',
      timeStyle: 'medium',
    }).format(date);
  } catch {
    return undefined;
  }
};

export const formatQuotaResetTimestamp = (value: number | null | undefined, _locale?: string) => {
  if (typeof value !== 'number' || !Number.isFinite(value) || value <= 0) return '-';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '-';
  return formatNumericTimestamp(date);
};

export const formatQuotaResetDisplay = (
  resetAtMs: number | null | undefined,
  resetLabel: string | null | undefined,
  locale?: string
) => {
  const normalizedResetAt = formatQuotaResetTimestamp(resetAtMs, locale);
  if (normalizedResetAt !== '-') return normalizedResetAt;

  const normalizedLabel = resetLabel?.trim() ?? '';
  if (!normalizedLabel || normalizedLabel === '-') return '-';
  const parsedLabel = formatQuotaResetTimestamp(parseQuotaResetLabelMs(normalizedLabel), locale);
  if (parsedLabel !== '-') return parsedLabel;
  return normalizedLabel;
};

export const formatQuotaResetTooltipParams = (
  params: Record<string, string | number>,
  resetAtMs: number | null | undefined,
  locale?: string,
  recoverAtMs?: number | null
) => {
  let formatted = params;
  if (Object.prototype.hasOwnProperty.call(params, 'resetAt')) {
    const resetAt = formatQuotaResetDisplay(resetAtMs, String(params.resetAt ?? ''), locale);
    if (resetAt !== params.resetAt) formatted = { ...formatted, resetAt };
  }
  if (Object.prototype.hasOwnProperty.call(params, 'recoverAt')) {
    const recoverAt = formatQuotaResetDisplay(recoverAtMs, String(params.recoverAt ?? ''), locale);
    if (recoverAt !== params.recoverAt) formatted = { ...formatted, recoverAt };
  }
  return formatted;
};

const normalizeDetailToken = (value: string | number | null | undefined) =>
  String(value ?? '')
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '_')
    .replace(/^_+|_+$/g, '');

export const translateDetailEnum = (
  t: TFunction,
  prefix: string,
  value: string | number | null | undefined
) => {
  const raw = String(value ?? '').trim();
  if (!raw) return '-';
  const token = normalizeDetailToken(raw);
  if (!token) return raw;
  return t(`${prefix}${token}`, { defaultValue: raw });
};

export const formatDurationMs = (value: number | null | undefined) => {
  if (value === null || value === undefined) return '-';
  return `${Math.round(value)} ms`;
};

export const getEventFailureReason = (event: MonitoringAnalyticsEventRow) =>
  event.fail_summary ||
  event.header_error_code ||
  event.header_error_kind ||
  event.header_trace_id ||
  '';

export const getEventStatusText = (event: MonitoringAnalyticsEventRow, t: TFunction) => {
  if (!event.failed) return t('accounts.detail_event_success');
  if (event.fail_status_code) {
    return t('accounts.detail_event_failed_with_code', {
      code: event.fail_status_code,
      defaultValue: `${t('accounts.detail_event_failed')} ${event.fail_status_code}`,
    });
  }
  return t('accounts.detail_event_failed');
};

export const quotaStatusLabelKey = (status: AccountRow['quota']['status']) => {
  switch (status) {
    case 'ok':
      return 'accounts.quota_status_ok';
    case 'low':
      return 'accounts.quota_status_low';
    case 'exhausted':
      return 'accounts.quota_status_exhausted';
    case 'error':
      return 'accounts.quota_status_error';
    case 'loading':
      return 'accounts.quota_status_loading';
    case 'disabled':
      return 'accounts.quota_status_disabled';
    case 'unknown':
    default:
      return 'accounts.quota_status_unknown';
  }
};

export type AccountQuotaLifecycleBarOverride = 'bad' | 'neutral' | null;

export const getAccountQuotaLifecycleBarOverride = (
  status: AccountRow['quota']['status']
): AccountQuotaLifecycleBarOverride => {
  switch (status) {
    case 'error':
      return 'bad';
    case 'loading':
    case 'disabled':
    case 'unknown':
      return 'neutral';
    case 'ok':
    case 'low':
    case 'exhausted':
    default:
      return null;
  }
};

const selectXaiQuotaListFallbackWindows = (
  windows: AccountQuotaDisplayWindow[]
): AccountQuotaDisplayWindow[] => {
  const billing =
    windows.find((window) => window.source === 'xai' && window.key === 'billing') ??
    windows.find(
      (window) =>
        window.source === 'xai' && window.key === 'credits-period' && window.kind === 'billing'
    );
  const payg = windows.find((window) => window.source === 'xai' && window.key === 'pay-as-you-go');

  return [billing, payg].filter((window): window is AccountQuotaDisplayWindow => Boolean(window));
};

export const selectAccountQuotaListWindows = (
  row: AccountRow,
  quotaWindows: AccountQuotaDisplayWindow[],
  standardQuotaWindows: AccountQuotaDisplayWindow[]
): AccountQuotaDisplayWindow[] => {
  if (standardQuotaWindows.length > 0) return standardQuotaWindows;

  switch (row.provider) {
    case 'codex':
      return [];
    case 'xai':
      return selectXaiQuotaListFallbackWindows(quotaWindows);
    case 'antigravity':
      return quotaWindows.slice(0, 2);
    case 'kimi': {
      const summary = quotaWindows.find(
        (window) => window.source === 'kimi' && window.key === 'summary'
      );
      return summary ? [summary] : [];
    }
    case 'claude':
      return [];
    default:
      return [];
  }
};

const getAntigravityGroupRank = (label: string) => {
  const normalized = label.toLowerCase();
  if (normalized.includes('claude') || normalized.includes('gpt')) return 0;
  if (normalized.includes('gemini')) return 1;
  return 2;
};

const getAntigravityMatrixGroupDisplayLabel = (label: string) => {
  const normalized = label.toLowerCase();
  if (normalized.includes('claude') || normalized.includes('gpt')) return 'Claude';
  if (normalized.includes('gemini')) return 'Gemini';
  return label;
};

export const getAccountQuotaFallbackVisibleScopeLabel = (
  row: AccountRow,
  window: AccountQuotaDisplayWindow
): string | null => {
  if (row.provider !== ANTIGRAVITY_CONFIG.type || window.source !== 'antigravity') return null;
  const groupLabel = window.groupLabel?.trim();
  return groupLabel ? getAntigravityMatrixGroupDisplayLabel(groupLabel) : null;
};

export const buildAntigravityQuotaMatrix = (
  row: AccountRow,
  windows: AccountQuotaDisplayWindow[]
): AntigravityQuotaMatrix | null => {
  if (row.provider !== ANTIGRAVITY_CONFIG.type) return null;

  const matrixWindows = windows.filter(
    (window) =>
      window.source === 'antigravity' &&
      Boolean(window.groupLabel) &&
      (window.kind === 'five_hour' || window.kind === 'weekly')
  );
  const groupOrder = new Map<string, number>();
  matrixWindows.forEach((window) => {
    const groupLabel = window.groupLabel ?? '';
    if (groupLabel && !groupOrder.has(groupLabel)) {
      groupOrder.set(groupLabel, groupOrder.size);
    }
  });

  const selectedGroupLabels = [...groupOrder.keys()]
    .sort((first, second) => {
      const rankDiff = getAntigravityGroupRank(first) - getAntigravityGroupRank(second);
      if (rankDiff !== 0) return rankDiff;
      return (groupOrder.get(first) ?? 0) - (groupOrder.get(second) ?? 0);
    })
    .slice(0, 2);
  if (selectedGroupLabels.length < 2) return null;

  const windowsByKindAndGroup = new Map<string, AccountQuotaDisplayWindow>();
  matrixWindows.forEach((window) => {
    if (!window.kind || !window.groupLabel) return;
    windowsByKindAndGroup.set(`${window.kind}\u0000${window.groupLabel}`, window);
  });

  const windowKeys = new Set<string>();
  const rows: AntigravityQuotaMatrixRow[] = [];
  for (const kind of ['five_hour', 'weekly'] satisfies AntigravityQuotaMatrixWindowKind[]) {
    const cells = selectedGroupLabels.map((groupLabel) => {
      const window = windowsByKindAndGroup.get(`${kind}\u0000${groupLabel}`);
      return window
        ? {
            groupLabel,
            displayLabel: getAntigravityMatrixGroupDisplayLabel(groupLabel),
            window,
          }
        : null;
    });
    if (cells.some((cell) => cell === null)) continue;
    cells.forEach((cell) => {
      if (cell) windowKeys.add(cell.window.key);
    });
    rows.push({
      key: kind,
      label: getQuotaWindowShortLabel(cells[0]!.window),
      cells: cells as AntigravityQuotaMatrixCell[],
    });
  }

  if (rows.length === 0) return null;
  return { rows, windowKeys };
};

export const toAuthFileCodexInspectionSnapshot = (
  row: AccountRow
): AuthFileCodexInspectionSnapshot | undefined => {
  if (!row.inspection) return undefined;
  const identity = getAuthFilePatchTarget(row.raw);
  return {
    fileName: row.fileName,
    runtimeId: identity.runtimeId,
    provider: identity.provider,
    authIndex: row.authIndex || null,
    accountId: identity.accountId,
    accountSnapshot: identity.accountSnapshot,
    statusCode: row.inspection.statusCode,
    action: getEffectiveAccountInspectionAction(row.inspection),
    actionStatus: row.inspection.actionStatus,
    executedAction: row.inspection.executedAction,
    usedPercent: row.inspection.usedPercent,
    isQuota:
      row.inspection.isQuota ??
      (row.inspection.usedPercent !== null ||
      getEffectiveAccountInspectionAction(row.inspection) === 'disable'
        ? true
        : null),
    inspectionAtMs: row.inspection.createdAtMs,
  };
};
