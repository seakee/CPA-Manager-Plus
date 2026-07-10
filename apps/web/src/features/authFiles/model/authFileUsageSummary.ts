import type { MonitoringAnalyticsCredentialStatRow } from '@/services/api/usageService';
import type { AuthFileItem, CodexQuotaState, CodexQuotaWindow } from '@/types';
import { normalizeAuthIndex } from '@/utils/authIndex';

const UNKNOWN_AUTH_INDEX_KEY = '-';
const CODEX_FIVE_HOUR_WINDOW_SECONDS = 18_000;
const CODEX_WEEKLY_WINDOW_SECONDS = 604_800;

export type AuthFileUsageSummary = {
  estimatedCost: number;
  totalTokens: number;
  codexFiveHourLimitTokens: number | null;
  codexFiveHourLimitCost: number | null;
  codexFiveHourRemainingTokens: number | null;
  codexFiveHourRemainingCost: number | null;
  codexWeeklyLimitTokens: number | null;
  codexWeeklyLimitCost: number | null;
  codexWeeklyRemainingTokens: number | null;
  codexWeeklyRemainingCost: number | null;
};

export type AuthFileUsageSummaryInput = {
  retainedRows: MonitoringAnalyticsCredentialStatRow[];
  fiveHourRows: MonitoringAnalyticsCredentialStatRow[];
  weeklyRows: MonitoringAnalyticsCredentialStatRow[];
  codexQuota?: CodexQuotaState;
};

export type AuthFileUsageSummaryMapInput = Omit<AuthFileUsageSummaryInput, 'codexQuota'> & {
  codexQuotaByKey: Map<string, CodexQuotaState | undefined>;
};

const normalizeKey = (value: unknown): string => String(value ?? '').trim().toLowerCase();

const getAuthFileAuthIndex = (file: AuthFileItem): string | null =>
  normalizeAuthIndex(file.authIndex ?? file['auth_index'] ?? file['auth-index']);

const normalizeAuthIndexKey = (value: unknown): string =>
  normalizeAuthIndex(value) ?? UNKNOWN_AUTH_INDEX_KEY;

export const getAuthFileUsageSummaryKey = (file: AuthFileItem): string =>
  `${file.name}::${normalizeAuthIndexKey(getAuthFileAuthIndex(file))}`;

const rowMatchesAuthFile = (
  file: AuthFileItem,
  row: MonitoringAnalyticsCredentialStatRow
): boolean => {
  if (normalizeKey(row.auth_file_snapshot) !== normalizeKey(file.name)) return false;
  return normalizeAuthIndex(row.auth_index) === getAuthFileAuthIndex(file);
};

const sumMatchingRows = (
  file: AuthFileItem,
  rows: MonitoringAnalyticsCredentialStatRow[]
): { totalTokens: number; estimatedCost: number } =>
  rows.filter((row) => rowMatchesAuthFile(file, row)).reduce(
    (total, row) => ({
      totalTokens: total.totalTokens + normalizeFiniteNumber(row.total_tokens),
      estimatedCost: total.estimatedCost + normalizeFiniteNumber(row.cost),
    }),
    { totalTokens: 0, estimatedCost: 0 }
  );

const normalizeFiniteNumber = (value: unknown): number => {
  const numberValue = typeof value === 'number' ? value : Number(value);
  return Number.isFinite(numberValue) ? numberValue : 0;
};

const normalizeWindowSeconds = (value: unknown): number | null => {
  const numberValue = normalizeFiniteNumber(value);
  return numberValue > 0 ? numberValue : null;
};

const findCodexQuotaWindow = (
  quota: CodexQuotaState | undefined,
  preferredMatch: (window: CodexQuotaWindow) => boolean,
  limitWindowSeconds: number
): CodexQuotaWindow | null => {
  const windows = quota?.windows ?? [];
  return (
    windows.find(preferredMatch) ??
    windows.find(
      (window) => normalizeWindowSeconds(window.limitWindowSeconds) === limitWindowSeconds
    ) ??
    null
  );
};

const findCodexFiveHourWindow = (quota: CodexQuotaState | undefined) =>
  findCodexQuotaWindow(
    quota,
    (window) => window.id === 'five-hour' || window.labelKey === 'codex_quota.primary_window',
    CODEX_FIVE_HOUR_WINDOW_SECONDS
  );

const findCodexWeeklyWindow = (quota: CodexQuotaState | undefined) =>
  findCodexQuotaWindow(
    quota,
    (window) => window.id === 'weekly' || window.labelKey === 'codex_quota.secondary_window',
    CODEX_WEEKLY_WINDOW_SECONDS
  );

const estimateLimitValue = (
  value: number,
  usedPercent: unknown,
  options: { allowZero?: boolean } = {}
): number | null => {
  const normalizedValue = normalizeFiniteNumber(value);
  const normalizedPercent = normalizeFiniteNumber(usedPercent);
  if (normalizedPercent <= 0) return null;
  if (options.allowZero ? normalizedValue < 0 : normalizedValue <= 0) return null;
  return normalizedValue / (normalizedPercent / 100);
};

const estimateTokenLimit = (tokens: number, usedPercent: unknown): number | null => {
  const estimate = estimateLimitValue(tokens, usedPercent);
  return estimate === null ? null : Math.round(estimate);
};

const roundCurrency = (value: number): number => Math.round(value * 100) / 100;

const estimateCostLimit = (
  cost: number,
  tokens: number,
  usedPercent: unknown
): number | null => {
  if (normalizeFiniteNumber(tokens) <= 0) return null;
  const estimate = estimateLimitValue(cost, usedPercent, { allowZero: true });
  return estimate === null ? null : roundCurrency(estimate);
};

const estimateRemainingTokens = (limit: number | null, used: number): number | null =>
  limit === null ? null : Math.max(0, Math.round(limit - normalizeFiniteNumber(used)));

const estimateRemainingCost = (limit: number | null, used: number): number | null =>
  limit === null
    ? null
    : roundCurrency(Math.max(0, limit - normalizeFiniteNumber(used)));

export const buildAuthFileUsageSummary = (
  file: AuthFileItem,
  input: AuthFileUsageSummaryInput
): AuthFileUsageSummary | undefined => {
  const retained = sumMatchingRows(file, input.retainedRows);
  const fiveHour = sumMatchingRows(file, input.fiveHourRows);
  const weekly = sumMatchingRows(file, input.weeklyRows);
  const fiveHourQuotaWindow = findCodexFiveHourWindow(input.codexQuota);
  const weeklyQuotaWindow = findCodexWeeklyWindow(input.codexQuota);
  const fiveHourLimitTokens = estimateTokenLimit(
    fiveHour.totalTokens,
    fiveHourQuotaWindow?.usedPercent
  );
  const fiveHourLimitCost = estimateCostLimit(
    fiveHour.estimatedCost,
    fiveHour.totalTokens,
    fiveHourQuotaWindow?.usedPercent
  );
  const fiveHourRemainingTokens = estimateRemainingTokens(
    fiveHourLimitTokens,
    fiveHour.totalTokens
  );
  const fiveHourRemainingCost = estimateRemainingCost(
    fiveHourLimitCost,
    fiveHour.estimatedCost
  );
  const weeklyLimitTokens = estimateTokenLimit(weekly.totalTokens, weeklyQuotaWindow?.usedPercent);
  const weeklyLimitCost = estimateCostLimit(
    weekly.estimatedCost,
    weekly.totalTokens,
    weeklyQuotaWindow?.usedPercent
  );
  const weeklyRemainingTokens = estimateRemainingTokens(weeklyLimitTokens, weekly.totalTokens);
  const weeklyRemainingCost = estimateRemainingCost(weeklyLimitCost, weekly.estimatedCost);

  if (
    retained.totalTokens <= 0 &&
    retained.estimatedCost <= 0 &&
    fiveHourLimitTokens === null &&
    fiveHourLimitCost === null &&
    fiveHourRemainingTokens === null &&
    fiveHourRemainingCost === null &&
    weeklyLimitTokens === null &&
    weeklyLimitCost === null &&
    weeklyRemainingTokens === null &&
    weeklyRemainingCost === null
  ) {
    return undefined;
  }

  return {
    estimatedCost: retained.estimatedCost,
    totalTokens: retained.totalTokens,
    codexFiveHourLimitTokens: fiveHourLimitTokens,
    codexFiveHourLimitCost: fiveHourLimitCost,
    codexFiveHourRemainingTokens: fiveHourRemainingTokens,
    codexFiveHourRemainingCost: fiveHourRemainingCost,
    codexWeeklyLimitTokens: weeklyLimitTokens,
    codexWeeklyLimitCost: weeklyLimitCost,
    codexWeeklyRemainingTokens: weeklyRemainingTokens,
    codexWeeklyRemainingCost: weeklyRemainingCost,
  };
};

export const buildAuthFileUsageSummaryMap = (
  files: AuthFileItem[],
  input: AuthFileUsageSummaryMapInput
): Map<string, AuthFileUsageSummary> => {
  const summaries = new Map<string, AuthFileUsageSummary>();
  files.forEach((file) => {
    const key = getAuthFileUsageSummaryKey(file);
    const summary = buildAuthFileUsageSummary(file, {
      retainedRows: input.retainedRows,
      fiveHourRows: input.fiveHourRows,
      weeklyRows: input.weeklyRows,
      codexQuota: input.codexQuotaByKey.get(key),
    });
    if (summary) summaries.set(key, summary);
  });
  return summaries;
};
