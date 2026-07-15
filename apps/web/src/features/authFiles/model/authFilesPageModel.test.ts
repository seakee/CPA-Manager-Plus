import { describe, expect, it } from 'vitest';
import type { AuthFileItem, CodexQuotaState } from '@/types';
import type { UsageHeaderSnapshot } from '@/services/api/usageService';
import {
  authFileMatchesCodexPlanFilter,
  authFileMatchesCodexStatusFilter,
  buildAuthFileCodexInspectionMap,
  getAuthFileCodexInspectionKey,
  getAuthFileCodexStatus,
  getAuthFileNameFromSelectionKey,
  getAuthFilePatchTarget,
  getAuthFileScopedCodexQuota,
  getAuthFileSearchValues,
  getAuthFileSelectionKey,
  getFreshAuthFileCodexStatusSources,
  getWholeAuthFileDeleteCandidates,
  hasPartialSharedAuthFileSelection,
  normalizeAuthFilesCodexStatusFilter,
  stringifySearchValue,
  type AuthFileCodexInspectionSnapshot,
} from './authFilesPageModel';

const t = ((key: string, options?: { defaultValue?: string }) =>
  options?.defaultValue ?? key) as never;

const codexFile = (overrides: Partial<AuthFileItem> = {}): AuthFileItem => ({
  name: 'codex-main.json',
  type: 'codex',
  authIndex: 'codex-main',
  ...overrides,
});

const codexQuota = (overrides: Partial<CodexQuotaState> = {}): CodexQuotaState => ({
  status: 'success',
  windows: [
    {
      id: 'five-hour',
      label: '5-hour limit',
      usedPercent: 10,
      resetLabel: '06/01 17:00',
      limitWindowSeconds: 18_000,
    },
    {
      id: 'weekly',
      label: 'Weekly limit',
      usedPercent: 100,
      resetLabel: '06/04 12:00',
      limitWindowSeconds: 604_800,
    },
  ],
  ...overrides,
});

describe('auth file Codex status helpers', () => {
  it('detects weekly-limited Codex quota from the weekly quota window', () => {
    const status = getAuthFileCodexStatus(codexFile(), codexQuota());

    expect(status.isCodex).toBe(true);
    expect(status.isWeeklyLimited).toBe(true);
    expect(authFileMatchesCodexStatusFilter(status, 'weekly_limited')).toBe(true);
    expect(status.badges.map((badge) => badge.kind)).toContain('weekly_limited');
  });

  it('detects five-hour limited Codex quota from the short quota window', () => {
    const status = getAuthFileCodexStatus(
      codexFile(),
      codexQuota({
        windows: [
          {
            id: 'five-hour',
            label: '5-hour limit',
            usedPercent: 100,
            resetLabel: '06/01 17:00',
            limitWindowSeconds: 18_000,
          },
          {
            id: 'weekly',
            label: 'Weekly limit',
            usedPercent: 45,
            resetLabel: '06/04 12:00',
            limitWindowSeconds: 604_800,
          },
        ],
      })
    );

    expect(status.isFiveHourLimited).toBe(true);
    expect(status.isWeeklyLimited).toBe(false);
    expect(status.fiveHourResetLabel).toBe('06/01 17:00');
    expect(authFileMatchesCodexStatusFilter(status, 'five_hour_limited')).toBe(true);
    expect(authFileMatchesCodexStatusFilter(status, 'weekly_limited')).toBe(false);
    expect(status.badges.map((badge) => badge.kind)).toContain('five_hour_limited');
  });

  it('detects monthly-limited Codex quota without treating it as weekly-limited', () => {
    const status = getAuthFileCodexStatus(
      codexFile(),
      codexQuota({
        windows: [
          {
            id: 'monthly',
            label: 'Monthly limit',
            usedPercent: 100,
            resetLabel: '06/30 12:00',
            limitWindowSeconds: 2_592_000,
          },
        ],
      })
    );

    expect(status.isMonthlyLimited).toBe(true);
    expect(status.isWeeklyLimited).toBe(false);
    expect(status.monthlyResetLabel).toBe('06/30 12:00');
    expect(authFileMatchesCodexStatusFilter(status, 'monthly_limited')).toBe(true);
    expect(authFileMatchesCodexStatusFilter(status, 'weekly_limited')).toBe(false);
    expect(status.badges.map((badge) => badge.kind)).toContain('monthly_limited');
  });

  it('detects disabled Codex files with a known quota recovery label', () => {
    const status = getAuthFileCodexStatus(codexFile({ disabled: true }), codexQuota());

    expect(status.hasDisabledRecoveryReset).toBe(true);
    expect(status.weeklyResetLabel).toBe('06/04 12:00');
    expect(status.recoveryResetLabel).toBe('06/04 12:00');
    expect(authFileMatchesCodexStatusFilter(status, 'disabled_with_reset')).toBe(true);
    expect(status.badges.find((badge) => badge.kind === 'disabled_with_reset')).toMatchObject({
      labelParams: { reset: '06/04 12:00' },
    });
  });

  it('uses the five-hour reset label for disabled files when only the short window is full', () => {
    const status = getAuthFileCodexStatus(
      codexFile({ disabled: true }),
      codexQuota({
        windows: [
          {
            id: 'five-hour',
            label: '5-hour limit',
            usedPercent: 100,
            resetLabel: '06/01 17:00',
            limitWindowSeconds: 18_000,
          },
          {
            id: 'weekly',
            label: 'Weekly limit',
            usedPercent: 45,
            resetLabel: '06/04 12:00',
            limitWindowSeconds: 604_800,
          },
        ],
      })
    );

    expect(status.hasDisabledRecoveryReset).toBe(true);
    expect(status.recoveryResetLabel).toBe('06/01 17:00');
    expect(status.badges.find((badge) => badge.kind === 'disabled_with_reset')).toMatchObject({
      labelParams: { reset: '06/01 17:00' },
    });
  });

  it('uses the monthly reset label for disabled files when the monthly window is full', () => {
    const status = getAuthFileCodexStatus(
      codexFile({ disabled: true }),
      codexQuota({
        windows: [
          {
            id: 'monthly',
            label: 'Monthly limit',
            usedPercent: 100,
            resetLabel: '06/30 12:00',
            limitWindowSeconds: 2_592_000,
          },
        ],
      })
    );

    expect(status.hasDisabledRecoveryReset).toBe(true);
    expect(status.recoveryResetLabel).toBe('06/30 12:00');
    expect(status.badges.find((badge) => badge.kind === 'disabled_with_reset')).toMatchObject({
      labelParams: { reset: '06/30 12:00' },
    });
  });

  it('does not mark manually disabled Codex files as waiting recovery when quota is available', () => {
    const status = getAuthFileCodexStatus(
      codexFile({ disabled: true }),
      codexQuota({
        windows: [
          {
            id: 'five-hour',
            label: '5-hour limit',
            usedPercent: 10,
            resetLabel: '06/01 17:00',
            limitWindowSeconds: 18_000,
          },
          {
            id: 'weekly',
            label: 'Weekly limit',
            usedPercent: 45,
            resetLabel: '06/04 12:00',
            limitWindowSeconds: 604_800,
          },
        ],
      })
    );

    expect(status.hasDisabledRecoveryReset).toBe(false);
    expect(authFileMatchesCodexStatusFilter(status, 'disabled_with_reset')).toBe(false);
  });

  it('detects HTTP 401 and reauth needs from the latest inspection result', () => {
    const status = getAuthFileCodexStatus(codexFile(), undefined, {
      fileName: 'codex-main.json',
      authIndex: 'codex-main',
      statusCode: 401,
      action: 'reauth',
      usedPercent: null,
      isQuota: false,
    });

    expect(status.isHttp401).toBe(true);
    expect(status.needsReauth).toBe(true);
    expect(authFileMatchesCodexStatusFilter(status, 'http_401')).toBe(true);
    expect(authFileMatchesCodexStatusFilter(status, 'reauth')).toBe(true);
    expect(status.badges.map((badge) => badge.kind)).toContain('reauth');
  });

  it('does not treat non-quota inspection percentages as weekly quota limits', () => {
    const status = getAuthFileCodexStatus(codexFile(), undefined, {
      fileName: 'codex-main.json',
      authIndex: 'codex-main',
      statusCode: 401,
      action: 'delete',
      usedPercent: 100,
      isQuota: false,
    });

    expect(status.isHttp401).toBe(true);
    expect(status.isWeeklyLimited).toBe(false);
    expect(authFileMatchesCodexStatusFilter(status, 'weekly_limited')).toBe(false);
  });

  it('does not mark legacy quota inspections as monthly-limited without a monthly window', () => {
    const status = getAuthFileCodexStatus(codexFile(), undefined, {
      fileName: 'codex-main.json',
      authIndex: 'codex-main',
      statusCode: 402,
      action: 'disable',
      usedPercent: 100,
      isQuota: true,
    });

    expect(status.isWeeklyLimited).toBe(true);
    expect(status.isMonthlyLimited).toBe(false);
    expect(authFileMatchesCodexStatusFilter(status, 'weekly_limited')).toBe(true);
    expect(authFileMatchesCodexStatusFilter(status, 'monthly_limited')).toBe(false);
  });

  it('treats plain Retry-After headers as diagnostics instead of quota exhaustion', () => {
    const retryAfterSnapshot: UsageHeaderSnapshot = {
      event_hash: 'retry-after-only',
      timestamp_ms: 1_700_000_000_000,
      response_metadata: {
        errors: {
          kind: 'rate_limit',
          code: 'retry_after',
          retry_after_seconds: 60,
          retry_after_recover_at_ms: 1_700_000_060_000,
        },
      },
    };

    const status = getAuthFileCodexStatus(codexFile(), undefined, undefined, retryAfterSnapshot);

    expect(status.isWeeklyLimited).toBe(false);
    expect(status.isMonthlyLimited).toBe(false);
    expect(status.badges.map((badge) => badge.kind)).toContain('observed_error');
    expect(status.badges.map((badge) => badge.kind)).not.toContain('observed_quota');
  });

  it('still treats explicit usage-limit header evidence as observed quota exhaustion', () => {
    const usageLimitSnapshot: UsageHeaderSnapshot = {
      event_hash: 'usage-limit',
      timestamp_ms: 1_700_000_000_000,
      response_metadata: {
        quota: {
          rate_limit_reached_type: 'workspace_member_credits_depleted',
          recover_at_ms: 1_700_000_060_000,
        },
        errors: {
          kind: 'rate_limit',
          code: 'usage_limit_reached',
        },
      },
    };

    const status = getAuthFileCodexStatus(codexFile(), undefined, undefined, usageLimitSnapshot);

    expect(status.isQuotaLimited).toBe(true);
    expect(status.isUnknownQuotaLimited).toBe(true);
    expect(status.isWeeklyLimited).toBe(false);
    expect(authFileMatchesCodexStatusFilter(status, 'quota_limited')).toBe(true);
    expect(authFileMatchesCodexStatusFilter(status, 'weekly_limited')).toBe(false);
    expect(status.badges.map((badge) => badge.kind)).toContain('observed_quota');
  });

  it('uses observed reached window metadata for specific quota status filters', () => {
    const usageLimitSnapshot: UsageHeaderSnapshot = {
      event_hash: 'usage-limit-weekly',
      timestamp_ms: 1_700_000_000_000,
      response_metadata: {
        quota: {
          rate_limit_reached_type: 'secondary',
          reached_window_kind: 'weekly',
          reached_window_source: 'secondary',
          recover_at_ms: 1_700_604_800_000,
        },
        errors: {
          kind: 'rate_limit',
          code: 'usage_limit_reached',
        },
      },
    };

    const status = getAuthFileCodexStatus(codexFile(), undefined, undefined, usageLimitSnapshot);

    expect(status.isQuotaLimited).toBe(true);
    expect(status.isUnknownQuotaLimited).toBe(false);
    expect(status.isWeeklyLimited).toBe(true);
    expect(authFileMatchesCodexStatusFilter(status, 'quota_limited')).toBe(true);
    expect(authFileMatchesCodexStatusFilter(status, 'weekly_limited')).toBe(true);
  });

  it('does not mark observed five-hour quota as limited when the reached window is under 100%', () => {
    const usageLimitSnapshot: UsageHeaderSnapshot = {
      event_hash: 'usage-limit-five-hour-under-limit',
      timestamp_ms: 1_700_000_000_000,
      response_metadata: {
        quota: {
          rate_limit_reached_type: 'primary',
          reached_window_kind: 'five_hour',
          reached_window_source: 'primary',
          primary: {
            used_percent: 99,
            window_minutes: 300,
          },
        },
        errors: {
          kind: 'rate_limit',
          code: 'usage_limit_reached',
        },
      },
    };

    const status = getAuthFileCodexStatus(codexFile(), undefined, undefined, usageLimitSnapshot);

    expect(status.isQuotaLimited).toBe(false);
    expect(status.isFiveHourLimited).toBe(false);
    expect(status.isUnknownQuotaLimited).toBe(false);
    expect(authFileMatchesCodexStatusFilter(status, 'quota_limited')).toBe(false);
    expect(authFileMatchesCodexStatusFilter(status, 'five_hour_limited')).toBe(false);
    expect(status.badges.map((badge) => badge.kind)).not.toContain('observed_quota');
  });

  it('does not mark observed weekly quota as limited when the reached window is under 100%', () => {
    const usageLimitSnapshot: UsageHeaderSnapshot = {
      event_hash: 'usage-limit-weekly-under-limit',
      timestamp_ms: 1_700_000_000_000,
      response_metadata: {
        quota: {
          rate_limit_reached_type: 'secondary',
          reached_window_kind: 'weekly',
          reached_window_source: 'secondary',
          secondary: {
            used_percent: 98,
            window_minutes: 10_080,
          },
        },
        errors: {
          kind: 'rate_limit',
          code: 'usage_limit_reached',
        },
      },
    };

    const status = getAuthFileCodexStatus(codexFile(), undefined, undefined, usageLimitSnapshot);

    expect(status.isQuotaLimited).toBe(false);
    expect(status.isWeeklyLimited).toBe(false);
    expect(status.isUnknownQuotaLimited).toBe(false);
    expect(authFileMatchesCodexStatusFilter(status, 'quota_limited')).toBe(false);
    expect(authFileMatchesCodexStatusFilter(status, 'weekly_limited')).toBe(false);
    expect(status.badges.map((badge) => badge.kind)).not.toContain('observed_quota');
  });

  it('does not mark observed monthly quota as limited when the reached window is under 100%', () => {
    const usageLimitSnapshot: UsageHeaderSnapshot = {
      event_hash: 'usage-limit-monthly-under-limit',
      timestamp_ms: 1_700_000_000_000,
      response_metadata: {
        quota: {
          rate_limit_reached_type: 'secondary',
          reached_window_kind: 'monthly',
          reached_window_source: 'secondary',
          secondary: {
            used_percent: 99,
            window_minutes: 43_200,
          },
        },
        errors: {
          kind: 'rate_limit',
          code: 'usage_limit_reached',
        },
      },
    };

    const status = getAuthFileCodexStatus(codexFile(), undefined, undefined, usageLimitSnapshot);

    expect(status.isQuotaLimited).toBe(false);
    expect(status.isMonthlyLimited).toBe(false);
    expect(status.isUnknownQuotaLimited).toBe(false);
    expect(authFileMatchesCodexStatusFilter(status, 'quota_limited')).toBe(false);
    expect(authFileMatchesCodexStatusFilter(status, 'monthly_limited')).toBe(false);
    expect(status.badges.map((badge) => badge.kind)).not.toContain('observed_quota');
  });

  it('keeps observed quota limited when the reached window is at 100%', () => {
    const usageLimitSnapshot: UsageHeaderSnapshot = {
      event_hash: 'usage-limit-five-hour-full',
      timestamp_ms: 1_700_000_000_000,
      response_metadata: {
        quota: {
          rate_limit_reached_type: 'primary',
          reached_window_kind: 'five_hour',
          reached_window_source: 'primary',
          primary: {
            used_percent: 100,
            window_minutes: 300,
          },
        },
        errors: {
          kind: 'rate_limit',
          code: 'usage_limit_reached',
        },
      },
    };

    const status = getAuthFileCodexStatus(codexFile(), undefined, undefined, usageLimitSnapshot);

    expect(status.isQuotaLimited).toBe(true);
    expect(status.isFiveHourLimited).toBe(true);
    expect(authFileMatchesCodexStatusFilter(status, 'quota_limited')).toBe(true);
    expect(authFileMatchesCodexStatusFilter(status, 'five_hour_limited')).toBe(true);
    expect(status.badges.map((badge) => badge.kind)).toContain('observed_quota');
  });

  it('ignores non-Codex files for Codex-only status filters', () => {
    const status = getAuthFileCodexStatus({ name: 'qwen.json', type: 'qwen' }, codexQuota());

    expect(status.isCodex).toBe(false);
    expect(status.isWeeklyLimited).toBe(false);
    expect(authFileMatchesCodexStatusFilter(status, 'weekly_limited')).toBe(false);
  });

  it('shows provider-aware xAI inspection evidence without using Codex window filters', () => {
    const file: AuthFileItem = { name: 'xai-main.json', type: 'xai', authIndex: 'xai-main' };
    const quotaStatus = getAuthFileCodexStatus(file, undefined, {
      fileName: file.name,
      provider: 'xai',
      authIndex: file.authIndex,
      action: 'disable',
      isQuota: true,
      errorKind: 'free_quota_exhausted',
    });
    const reauthStatus = getAuthFileCodexStatus(file, undefined, {
      fileName: file.name,
      provider: 'xai',
      authIndex: file.authIndex,
      action: 'reauth',
      isQuota: false,
      errorKind: 'auth_invalid',
    });
    const partialStatus = getAuthFileCodexStatus(file, undefined, {
      fileName: file.name,
      provider: 'xai',
      authIndex: file.authIndex,
      action: 'keep',
      isQuota: false,
      errorKind: 'billing_partial',
    });

    expect(quotaStatus).toMatchObject({
      isCodex: false,
      isQuotaLimited: true,
      isUnknownQuotaLimited: true,
      isWeeklyLimited: false,
      isMonthlyLimited: false,
    });
    expect(quotaStatus.badges.map((badge) => badge.kind)).toContain('observed_quota');
    expect(reauthStatus.badges.map((badge) => badge.kind)).toContain('reauth');
    expect(partialStatus.badges.map((badge) => badge.kind)).toContain('observed_error');
    expect(authFileMatchesCodexStatusFilter(quotaStatus, 'quota_limited')).toBe(false);
  });

  it('indexes inspection results by file name and auth index', () => {
    const inspection: AuthFileCodexInspectionSnapshot = {
      fileName: 'codex-main.json',
      authIndex: 'codex-main',
      statusCode: 401,
      action: 'delete',
      usedPercent: null,
      isQuota: false,
    };

    const map = buildAuthFileCodexInspectionMap([inspection]);

    expect(map.get(getAuthFileCodexInspectionKey('codex-main.json', 'codex-main'))).toBe(
      inspection
    );
  });

  it('does not apply a provider-mismatched inspection snapshot to a row', () => {
    const sources = getFreshAuthFileCodexStatusSources(
      codexFile(),
      undefined,
      {
        fileName: 'codex-main.json',
        provider: 'xai',
        authIndex: 'codex-main',
        action: 'disable',
        isQuota: true,
      },
      undefined
    );

    expect(sources.inspection).toBeUndefined();
  });

  it('suppresses older Codex inspection and header status sources after a same-row quota refresh', () => {
    const file = codexFile();
    const quota = codexQuota({
      authFileKey: getAuthFileCodexInspectionKey(file.name, file.authIndex),
      fetchedAtMs: 2_000,
      windows: [
        {
          id: 'five-hour',
          label: '5-hour limit',
          usedPercent: 10,
          resetLabel: '06/01 17:00',
          limitWindowSeconds: 18_000,
        },
      ],
    });
    const inspection: AuthFileCodexInspectionSnapshot = {
      fileName: file.name,
      authIndex: file.authIndex,
      statusCode: 401,
      action: 'reauth',
      usedPercent: null,
      isQuota: false,
      inspectionAtMs: 1_000,
    };
    const headerSnapshot: UsageHeaderSnapshot = {
      event_hash: 'old-auth-error',
      timestamp_ms: 1_000,
      header_error_kind: 'auth',
      header_error_code: 'invalid_api_key',
    };

    const sources = getFreshAuthFileCodexStatusSources(file, quota, inspection, headerSnapshot);
    const status = getAuthFileCodexStatus(file, quota, sources.inspection, sources.headerSnapshot);

    expect(sources.inspection).toBeUndefined();
    expect(sources.headerSnapshot).toBeUndefined();
    expect(status.needsReauth).toBe(false);
    expect(status.badges).toHaveLength(0);
  });

  it('keeps newer Codex inspection and header status sources after an older quota refresh', () => {
    const file = codexFile();
    const quota = codexQuota({
      authFileKey: getAuthFileCodexInspectionKey(file.name, file.authIndex),
      fetchedAtMs: 1_000,
      windows: [],
    });
    const inspection: AuthFileCodexInspectionSnapshot = {
      fileName: file.name,
      authIndex: file.authIndex,
      statusCode: 401,
      action: 'reauth',
      usedPercent: null,
      isQuota: false,
      inspectionAtMs: 2_000,
    };
    const headerSnapshot: UsageHeaderSnapshot = {
      event_hash: 'new-auth-error',
      timestamp_ms: 2_000,
      header_error_kind: 'auth',
      header_error_code: 'invalid_api_key',
    };

    const sources = getFreshAuthFileCodexStatusSources(file, quota, inspection, headerSnapshot);
    const status = getAuthFileCodexStatus(file, quota, sources.inspection, sources.headerSnapshot);

    expect(sources.inspection).toBe(inspection);
    expect(sources.headerSnapshot).toBe(headerSnapshot);
    expect(status.needsReauth).toBe(true);
    expect(status.badges.map((badge) => badge.kind)).toContain('reauth');
  });

  it('suppresses older Codex inspection after a newer same-row header snapshot', () => {
    const file = codexFile();
    const inspection: AuthFileCodexInspectionSnapshot = {
      fileName: file.name,
      authIndex: file.authIndex,
      statusCode: 401,
      action: 'reauth',
      usedPercent: null,
      isQuota: false,
      inspectionAtMs: 1_000,
    };
    const headerSnapshot: UsageHeaderSnapshot = {
      event_hash: 'newer-healthy-header',
      timestamp_ms: 2_000,
      header_trace_id: 'trace-new',
    };

    const sources = getFreshAuthFileCodexStatusSources(file, undefined, inspection, headerSnapshot);
    const status = getAuthFileCodexStatus(
      file,
      undefined,
      sources.inspection,
      sources.headerSnapshot
    );

    expect(sources.inspection).toBeUndefined();
    expect(sources.headerSnapshot).toBe(headerSnapshot);
    expect(status.needsReauth).toBe(false);
  });

  it('suppresses older header diagnostics after a newer Codex inspection', () => {
    const file = codexFile();
    const inspection: AuthFileCodexInspectionSnapshot = {
      fileName: file.name,
      authIndex: file.authIndex,
      statusCode: 200,
      action: null,
      usedPercent: null,
      isQuota: false,
      inspectionAtMs: 2_000,
    };
    const headerSnapshot: UsageHeaderSnapshot = {
      event_hash: 'older-auth-header',
      timestamp_ms: 1_000,
      header_error_kind: 'auth',
      header_error_code: 'invalid_api_key',
    };

    const sources = getFreshAuthFileCodexStatusSources(file, undefined, inspection, headerSnapshot);
    const status = getAuthFileCodexStatus(
      file,
      undefined,
      sources.inspection,
      sources.headerSnapshot
    );

    expect(sources.inspection).toBe(inspection);
    expect(sources.headerSnapshot).toBeUndefined();
    expect(status.needsReauth).toBe(false);
  });

  it('does not suppress older status sources when quota identity is missing', () => {
    const file = codexFile();
    const quota = codexQuota({
      fetchedAtMs: 2_000,
      windows: [],
    });
    const inspection: AuthFileCodexInspectionSnapshot = {
      fileName: file.name,
      authIndex: file.authIndex,
      statusCode: 401,
      action: 'reauth',
      usedPercent: null,
      isQuota: false,
      inspectionAtMs: 1_000,
    };

    const sources = getFreshAuthFileCodexStatusSources(file, quota, inspection);

    expect(sources.inspection).toBe(inspection);
  });

  it('keeps active Codex quota scoped to the matching auth file row', () => {
    const first = codexFile({ name: 'shared-codex.json', authIndex: 0 });
    const second = codexFile({ name: 'shared-codex.json', authIndex: 1 });
    const quota = codexQuota({
      authFileKey: getAuthFileCodexInspectionKey(first.name, first.authIndex),
    });

    expect(getAuthFileScopedCodexQuota(first, quota)).toBe(quota);
    expect(getAuthFileScopedCodexQuota(second, quota)).toBeUndefined();
  });

  it('keeps legacy Codex quota without identity available for files without auth index', () => {
    const quota = codexQuota();

    expect(getAuthFileScopedCodexQuota(codexFile({ authIndex: undefined }), quota)).toBe(quota);
  });

  it('drops legacy Codex quota without identity for auth-indexed files', () => {
    const quota = codexQuota();

    expect(getAuthFileScopedCodexQuota(codexFile(), quota)).toBeUndefined();
  });

  it('keeps plan search fallback while dropping stale header diagnostics from search values', () => {
    const file = codexFile();
    const quota = codexQuota({
      authFileKey: getAuthFileCodexInspectionKey(file.name, file.authIndex),
      fetchedAtMs: 2_000,
      windows: [],
    });
    const headerSnapshot: UsageHeaderSnapshot = {
      event_hash: 'old-header-diagnostics',
      timestamp_ms: 1_000,
      header_quota_plan_type: 'plus',
      header_error_kind: 'auth',
      header_error_code: 'invalid_api_key',
      header_trace_id: 'trace-old',
    };
    const sources = getFreshAuthFileCodexStatusSources(file, quota, undefined, headerSnapshot);
    const status = getAuthFileCodexStatus(file, quota, undefined, sources.headerSnapshot);
    const searchValues = stringifySearchValue(
      getAuthFileSearchValues(file, t, quota, status, sources.headerSnapshot, headerSnapshot)
    );

    expect(searchValues).toContain('plus');
    expect(searchValues).not.toContain('invalid_api_key');
    expect(searchValues).not.toContain('trace-old');
    expect(searchValues).not.toContain('auth_files.codex_status_badge_reauth');
  });

  it('adds derived Codex status labels to searchable values', () => {
    const status = getAuthFileCodexStatus(codexFile(), undefined, {
      fileName: 'codex-main.json',
      authIndex: 'codex-main',
      statusCode: 401,
      action: 'reauth',
      usedPercent: null,
      isQuota: false,
    });

    expect(
      stringifySearchValue(getAuthFileSearchValues(codexFile(), t, undefined, status))
    ).toContain('auth_files.codex_status_badge_reauth');
    expect(normalizeAuthFilesCodexStatusFilter('http_401')).toBe('reauth');
    expect(normalizeAuthFilesCodexStatusFilter('quota_limited')).toBe('quota_limited');
    expect(normalizeAuthFilesCodexStatusFilter('five_hour_limited')).toBe('five_hour_limited');
    expect(normalizeAuthFilesCodexStatusFilter('monthly_limited')).toBe('monthly_limited');
    expect(normalizeAuthFilesCodexStatusFilter('disabled_with_reset')).toBe('disabled_with_reset');
    expect(normalizeAuthFilesCodexStatusFilter('unknown')).toBeNull();
  });
});

describe('auth file Codex plan helpers', () => {
  it('matches Codex files by plan from file metadata or quota fallback', () => {
    expect(
      authFileMatchesCodexPlanFilter(codexFile({ plan_type: 'plus' }), undefined, 'plus')
    ).toBe(true);
    expect(
      authFileMatchesCodexPlanFilter(codexFile({ plan_type: 'plus' }), undefined, 'team')
    ).toBe(false);
    expect(
      authFileMatchesCodexPlanFilter(
        codexFile({ metadata: { planType: 'pro-lite' } }),
        undefined,
        'prolite'
      )
    ).toBe(true);
    expect(
      authFileMatchesCodexPlanFilter(
        codexFile({ name: 'quota-team.json' }),
        codexQuota({ planType: 'team' }),
        'team'
      )
    ).toBe(true);
    expect(authFileMatchesCodexPlanFilter(codexFile(), undefined, 'unknown')).toBe(true);
    expect(
      authFileMatchesCodexPlanFilter(codexFile(), undefined, 'unknown', {
        event_hash: 'active-limit-only',
        timestamp_ms: 1_700_000_000_000,
        response_metadata: {
          quota: {
            active_limit: 'premium',
          },
        },
      })
    ).toBe(true);
    expect(
      authFileMatchesCodexPlanFilter({ name: 'qwen.json', type: 'qwen' }, undefined, 'plus')
    ).toBe(false);
  });

  it('keeps same-file auth rows distinct for selection and patch targets', () => {
    const first = codexFile({ name: 'shared-codex.json', authIndex: 0 });
    const second = codexFile({ name: 'shared-codex.json', authIndex: 1 });
    const firstKey = getAuthFileSelectionKey(first);
    const secondKey = getAuthFileSelectionKey(second);

    expect(firstKey).not.toBe(secondKey);
    expect(getAuthFileNameFromSelectionKey(firstKey)).toBe('shared-codex.json');
    expect(getAuthFilePatchTarget(first)).toEqual({ name: 'shared-codex.json', authIndex: 0 });
    expect(getAuthFilePatchTarget(codexFile({ authIndex: undefined }))).toEqual({
      name: 'codex-main.json',
    });
  });

  it('detects partial selection for shared auth files', () => {
    const first = codexFile({ name: 'shared-codex.json', authIndex: 0 });
    const second = codexFile({ name: 'shared-codex.json', authIndex: 1 });
    const single = codexFile({ name: 'single-codex.json', authIndex: 'single' });

    expect(
      hasPartialSharedAuthFileSelection([first, second, single], [getAuthFileSelectionKey(first)])
    ).toBe(true);
    expect(
      hasPartialSharedAuthFileSelection(
        [first, second, single],
        [first, second].map(getAuthFileSelectionKey)
      )
    ).toBe(false);
    expect(
      hasPartialSharedAuthFileSelection([first, second, single], [getAuthFileSelectionKey(single)])
    ).toBe(false);
  });

  it('returns delete candidates only when every row in a shared auth file is eligible', () => {
    const first = codexFile({ name: 'shared-codex.json', authIndex: 0 });
    const second = codexFile({ name: 'shared-codex.json', authIndex: 1 });
    const single = codexFile({ name: 'single-codex.json', authIndex: 'single' });

    expect(getWholeAuthFileDeleteCandidates([first, second, single], [first, single])).toEqual([
      single,
    ]);
    expect(
      getWholeAuthFileDeleteCandidates([first, second, single], [first, second, single])
    ).toEqual([first, single]);
  });

  it('does not collapse repeated rows that have no auth index', () => {
    const first = codexFile({ name: 'legacy-shared.json', authIndex: undefined });
    const second = codexFile({ name: 'legacy-shared.json', authIndex: undefined });

    expect(getWholeAuthFileDeleteCandidates([first, second], [first])).toEqual([]);
    expect(getWholeAuthFileDeleteCandidates([first, second], [first, second])).toEqual([first]);
  });
});
