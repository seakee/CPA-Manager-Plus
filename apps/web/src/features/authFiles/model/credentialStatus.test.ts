import { describe, expect, it } from 'vitest';
import type { AuthFileItem, CodexQuotaState } from '@/types';
import type { UsageHeaderSnapshot } from '@/services/api/usageService';
import { formatQuotaResetTime } from '@/utils/quota/formatters';
import { CODEX_SPARK_MODEL_ID } from '@/utils/quota/codexQuota';
import {
  authFileMatchesCodexStatusFilter,
  buildAuthFileCodexInspectionMap,
  filterSuppressedAuthFileInspectionSnapshots,
  getAuthFileCodexInspectionKey,
  getAuthFileCodexInspectionKeyForIdentity,
  getAuthFileInspectionSnapshotKey,
  getAuthFileCodexStatus,
  getHandledAuthFileInspectionSnapshotKeys,
  hasAuthFileCodexProblemBadge,
  hasAuthFileStatusProblem,
  getAuthFileNameFromSelectionKey,
  getAuthFilePatchTarget,
  getAuthFileScopedCodexQuota,
  getAuthFileSelectionKey,
  getFreshAuthFileCodexStatusSources,
  getWholeAuthFileDeleteCandidates,
  hasPartialSharedAuthFileSelection,
  isAuthFileInspectionAuthenticationFailure,
  sanitizeSupersededAuthQuotaState,
  sanitizeSupersededAuthHeaderSnapshot,
  type AuthFileCodexInspectionSnapshot,
} from './credentialStatus';

const codexFile = (overrides: Partial<AuthFileItem> = {}): AuthFileItem => ({
  name: 'codex-main.json',
  type: 'codex',
  authIndex: 'codex-main',
  ...overrides,
});

const CODEX_MAIN_MODEL = 'gpt-5.6-sol';
const CODEX_MAIN_SCOPE = { kind: 'family', key: 'codex_main', complete: true } as const;

const codexQuota = (overrides: Partial<CodexQuotaState> = {}): CodexQuotaState => ({
  status: 'success',
  windows: [
    {
      id: 'five-hour',
      label: '5-hour limit',
      usedPercent: 10,
      resetLabel: '06/01 17:00',
      limitWindowSeconds: 18_000,
      modelScope: CODEX_MAIN_SCOPE,
    },
    {
      id: 'weekly',
      label: 'Weekly limit',
      usedPercent: 100,
      resetLabel: '06/04 12:00',
      limitWindowSeconds: 604_800,
      modelScope: CODEX_MAIN_SCOPE,
    },
  ],
  ...overrides,
});

describe('auth file Codex status helpers', () => {
  it('keeps a handled server inspection result suppressed while allowing a newer result', () => {
    const handled: AuthFileCodexInspectionSnapshot = {
      fileName: 'codex-main.json',
      provider: 'codex',
      authIndex: 'codex-main',
      runId: 10,
      resultId: 20,
      inspectionAtMs: 1_000,
      statusCode: 401,
      action: 'reauth',
      errorKind: 'invalid_token',
      isQuota: false,
    };
    const sameResultWithUpdatedFields: AuthFileCodexInspectionSnapshot = {
      ...handled,
      action: 'keep',
      errorKind: 'authentication_error',
    };
    const newerResult: AuthFileCodexInspectionSnapshot = {
      ...handled,
      runId: 11,
      resultId: 21,
      inspectionAtMs: 2_000,
    };
    const handledKeys = getHandledAuthFileInspectionSnapshotKeys(
      [handled],
      getAuthFileCodexInspectionKeyForIdentity(handled)
    );
    const suppressedKeys = new Set(handledKeys);

    expect(handledKeys).toEqual([getAuthFileInspectionSnapshotKey(handled)]);
    expect(
      filterSuppressedAuthFileInspectionSnapshots(
        [sameResultWithUpdatedFields, newerResult],
        suppressedKeys
      )
    ).toEqual([newerResult]);
  });

  it('uses the local inspection fingerprint when run and result IDs are unavailable', () => {
    const handled: AuthFileCodexInspectionSnapshot = {
      fileName: 'codex-main.json',
      provider: 'codex',
      authIndex: 'codex-main',
      inspectionAtMs: 1_000,
      statusCode: 401,
      action: 'reauth',
      errorKind: 'invalid_token',
      isQuota: false,
    };
    const newerResult: AuthFileCodexInspectionSnapshot = {
      ...handled,
      inspectionAtMs: 2_000,
    };
    const handledKeys = getHandledAuthFileInspectionSnapshotKeys(
      [handled],
      getAuthFileCodexInspectionKeyForIdentity(handled)
    );

    expect(filterSuppressedAuthFileInspectionSnapshots([handled], new Set(handledKeys))).toEqual(
      []
    );
    expect(
      filterSuppressedAuthFileInspectionSnapshots([newerResult], new Set(handledKeys))
    ).toEqual([newerResult]);
  });

  it('allows a newer server inspection when run and result IDs are reused', () => {
    const handled: AuthFileCodexInspectionSnapshot = {
      fileName: 'codex-main.json',
      provider: 'codex',
      authIndex: 'codex-main',
      runId: 10,
      resultId: 20,
      inspectionAtMs: 1_000,
      statusCode: 401,
      action: 'reauth',
      errorKind: 'invalid_token',
      isQuota: false,
    };
    const newerResult: AuthFileCodexInspectionSnapshot = {
      ...handled,
      inspectionAtMs: 2_000,
    };
    const handledKeys = getHandledAuthFileInspectionSnapshotKeys(
      [handled],
      getAuthFileCodexInspectionKeyForIdentity(handled)
    );

    expect(filterSuppressedAuthFileInspectionSnapshots([handled], new Set(handledKeys))).toEqual(
      []
    );
    expect(
      filterSuppressedAuthFileInspectionSnapshots([newerResult], new Set(handledKeys))
    ).toEqual([newerResult]);
  });

  it('ignores a superseded raw 401 status code when newer evidence is authenticated', () => {
    const file = codexFile({ errorStatus: 401, statusCode: 401 });
    const healthyQuota = codexQuota({
      windows: [
        {
          id: 'weekly',
          label: 'Weekly limit',
          usedPercent: 30,
          resetLabel: '06/04 12:00',
          limitWindowSeconds: 604_800,
        },
      ],
    });

    expect(getAuthFileCodexStatus(file, healthyQuota).needsReauth).toBe(true);
    expect(
      getAuthFileCodexStatus(file, healthyQuota, undefined, undefined, {
        ignoreRawStatusCode: true,
      }).needsReauth
    ).toBe(false);
  });

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

  it('uses the absolute reset timestamp for disabled recovery status display', () => {
    const resetAtMs = Date.parse('2026-08-20T03:40:00Z');
    const status = getAuthFileCodexStatus(
      codexFile({ disabled: true }),
      codexQuota({
        windows: [
          {
            id: 'five-hour',
            label: '5-hour limit',
            usedPercent: 100,
            resetLabel: '08/20 03:40',
            resetAtMs,
            resetAccuracy: 'exact',
            limitWindowSeconds: 18_000,
          },
        ],
      })
    );

    expect(status.fiveHourResetAtMs).toBe(resetAtMs);
    expect(status.fiveHourResetAccuracy).toBe('exact');
    expect(status.fiveHourResetLabel).toBe(formatQuotaResetTime(resetAtMs));
    expect(status.recoveryResetAtMs).toBe(resetAtMs);
    expect(status.recoveryResetAccuracy).toBe('exact');
    expect(status.recoveryResetLabel).toBe(formatQuotaResetTime(resetAtMs));
    expect(status.badges.find((badge) => badge.kind === 'disabled_with_reset')).toMatchObject({
      labelParams: { reset: formatQuotaResetTime(resetAtMs) },
    });
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

  it('uses the effective disabled state after newer Accounts evidence enables a credential', () => {
    const status = getAuthFileCodexStatus(
      codexFile({ disabled: true }),
      codexQuota(),
      undefined,
      undefined,
      { effectiveDisabled: false }
    );

    expect(status.hasDisabledRecoveryReset).toBe(false);
    expect(authFileMatchesCodexStatusFilter(status, 'disabled_with_reset')).toBe(false);
    expect(status.badges.map((badge) => badge.kind)).not.toContain('disabled_with_reset');
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

  it.each(['success', 'skipped'])(
    'does not expose a handled inspection 401 as reauth when action status is %s',
    (actionStatus) => {
      const status = getAuthFileCodexStatus(codexFile(), undefined, {
        fileName: 'codex-main.json',
        authIndex: 'codex-main',
        statusCode: 401,
        action: 'reauth',
        actionStatus,
        executedAction: 'reauth',
        usedPercent: null,
        isQuota: false,
      });

      expect(status.isHttp401).toBe(false);
      expect(status.needsReauth).toBe(false);
      expect(status.badges.map((badge) => badge.kind)).not.toContain('reauth');
    }
  );

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
      model: CODEX_MAIN_MODEL,
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

  it.each(['insufficient_quota', 'rate_limited', 'billing_limit_reached'])(
    'classifies %s Header errors as quota evidence',
    (errorCode) => {
      const status = getAuthFileCodexStatus(codexFile(), undefined, undefined, {
        event_hash: `quota-error-${errorCode}`,
        timestamp_ms: 1_700_000_000_000,
        model: CODEX_MAIN_MODEL,
        header_error_kind: 'rate_limit',
        header_error_code: errorCode,
      });

      expect(status.isQuotaLimited).toBe(true);
      expect(status.badges.map((badge) => badge.kind)).toContain('observed_quota');
    }
  );

  it('does not promote Spark-only quota headers to account credential status', () => {
    const status = getAuthFileCodexStatus(codexFile(), undefined, undefined, {
      event_hash: 'spark-quota-error',
      timestamp_ms: 1_700_000_000_000,
      model: CODEX_SPARK_MODEL_ID,
      header_error_kind: 'rate_limit',
      header_error_code: 'insufficient_quota',
    });

    expect(status.isQuotaLimited).toBe(false);
    expect(status.badges.map((badge) => badge.kind)).not.toContain('observed_quota');
    expect(status.badges.map((badge) => badge.kind)).not.toContain('observed_error');
  });

  it('does not promote quota metadata without model identity to account credential status', () => {
    const status = getAuthFileCodexStatus(codexFile(), undefined, undefined, {
      event_hash: 'unknown-scope-quota-error',
      timestamp_ms: 1_700_000_000_000,
      header_quota_used_percent: 100,
      header_quota_recover_at_ms: 1_700_000_060_000,
      header_error_kind: 'rate_limit',
      header_error_code: 'insufficient_quota',
    });

    expect(status.isQuotaLimited).toBe(false);
    expect(status.badges.map((badge) => badge.kind)).not.toContain('observed_quota');
    expect(status.badges.map((badge) => badge.kind)).not.toContain('observed_error');
  });

  it('uses observed reached window metadata for specific quota status filters', () => {
    const usageLimitSnapshot: UsageHeaderSnapshot = {
      event_hash: 'usage-limit-weekly',
      timestamp_ms: 1_700_000_000_000,
      model: CODEX_MAIN_MODEL,
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
      model: CODEX_MAIN_MODEL,
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
      model: CODEX_MAIN_MODEL,
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
      model: CODEX_MAIN_MODEL,
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
      model: CODEX_MAIN_MODEL,
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

  it('applies current raw-status evidence rules to non-Codex providers', () => {
    for (const file of [
      { name: 'qwen-cancelled.json', type: 'qwen', status_code: 499, status_message: 'cancelled' },
      {
        name: 'claude-transient.json',
        type: 'claude',
        status_code: 503,
        status_message: 'upstream unavailable',
      },
    ]) {
      const status = getAuthFileCodexStatus(file);

      expect(status.hasRawStatusWarning).toBe(false);
      expect(hasAuthFileStatusProblem(status)).toBe(false);
    }

    const authenticationFailure = getAuthFileCodexStatus({
      name: 'qwen-auth.json',
      type: 'qwen',
      status_code: 503,
      status_message: 'authentication_error: invalid token',
    });

    expect(authenticationFailure.hasRawStatusWarning).toBe(true);
    expect(hasAuthFileStatusProblem(authenticationFailure)).toBe(true);
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
    const officialApiStatus = getAuthFileCodexStatus(file, undefined, {
      fileName: file.name,
      provider: 'xai',
      authIndex: file.authIndex,
      action: 'keep',
      isQuota: false,
      errorKind: 'official_api_healthy',
    });
    const legacyIdentityStatus = getAuthFileCodexStatus(file, undefined, {
      fileName: file.name,
      provider: 'xai',
      authIndex: file.authIndex,
      action: 'keep',
      isQuota: false,
      errorKind: 'identity_healthy',
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
    expect(partialStatus.badges.map((badge) => badge.kind)).not.toContain('observed_error');
    expect(officialApiStatus.badges.map((badge) => badge.kind)).not.toContain('observed_error');
    expect(legacyIdentityStatus.badges.map((badge) => badge.kind)).not.toContain('observed_error');
    expect(authFileMatchesCodexStatusFilter(quotaStatus, 'quota_limited')).toBe(true);
  });

  it('does not expose unknown xAI inspection classifications in auth file badges', () => {
    const file: AuthFileItem = { name: 'xai-main.json', type: 'xai', authIndex: 'xai-main' };
    const status = getAuthFileCodexStatus(file, undefined, {
      fileName: file.name,
      provider: 'xai',
      authIndex: file.authIndex,
      action: 'keep',
      isQuota: false,
      errorKind: 'future_xai_failure',
    });

    const badge = status.badges.find((item) => item.kind === 'inspection_error');
    expect(badge).toMatchObject({
      titleKey: 'auth_files.provider_inspection_badge_error_title',
      labelParams: { provider: 'xAI' },
    });
    expect(JSON.stringify(badge)).not.toContain('future_xai_failure');
    expect(hasAuthFileCodexProblemBadge(status)).toBe(true);
  });

  it('keeps xAI inspection problems visible alongside neutral Header diagnostics', () => {
    const file: AuthFileItem = { name: 'xai-main.json', type: 'xai', authIndex: 'xai-main' };
    const inspection: AuthFileCodexInspectionSnapshot = {
      fileName: file.name,
      provider: 'xai',
      authIndex: file.authIndex,
      action: 'keep',
      isQuota: false,
      errorKind: 'future_xai_failure',
      inspectionAtMs: 1_000,
    };
    const headerSnapshot: UsageHeaderSnapshot = {
      event_hash: 'xai-neutral-header-warning',
      timestamp_ms: 2_000,
      header_error_kind: 'request',
      header_error_code: 'invalid_request_error',
    };

    const sources = getFreshAuthFileCodexStatusSources(
      file,
      undefined,
      inspection,
      headerSnapshot,
      undefined,
      2_500
    );
    const status = getAuthFileCodexStatus(
      file,
      undefined,
      sources.inspection,
      sources.headerSnapshot,
      2_500
    );

    expect(sources.inspection).toBe(inspection);
    expect(status.badges.map((badge) => badge.kind)).toEqual([
      'observed_error',
      'inspection_error',
    ]);
    expect(hasAuthFileCodexProblemBadge(status)).toBe(true);
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

  it('keeps same-file inspection snapshots without auth indexes distinct', () => {
    const first: AuthFileCodexInspectionSnapshot = {
      fileName: 'shared-codex.json',
      provider: 'codex',
      accountId: 'account-1',
      accountSnapshot: 'first@example.com',
      action: 'reauth',
    };
    const second: AuthFileCodexInspectionSnapshot = {
      fileName: 'shared-codex.json',
      provider: 'codex',
      accountSnapshot: 'second@example.com',
      action: 'disable',
    };

    const map = buildAuthFileCodexInspectionMap([first, second]);

    expect(map.size).toBe(2);
    expect(map.get(getAuthFileCodexInspectionKeyForIdentity(first))).toBe(first);
    expect(map.get(getAuthFileCodexInspectionKeyForIdentity(second))).toBe(second);
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

  it('suppresses an older inspection when a newer successful request exists', () => {
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

    const sources = getFreshAuthFileCodexStatusSources(
      file,
      undefined,
      inspection,
      undefined,
      2_000
    );

    expect(sources.inspection).toBeUndefined();
  });

  it('keeps an older quota inspection after an ordinary successful request', () => {
    const file = codexFile();
    const inspection: AuthFileCodexInspectionSnapshot = {
      fileName: file.name,
      authIndex: file.authIndex,
      statusCode: 429,
      action: 'disable',
      usedPercent: 100,
      isQuota: true,
      inspectionAtMs: 1_000,
    };

    const sources = getFreshAuthFileCodexStatusSources(
      file,
      undefined,
      inspection,
      undefined,
      2_000
    );

    expect(sources.inspection).toBe(inspection);
  });

  it('suppresses an older auth header after a newer successful request', () => {
    const file = codexFile();
    const headerSnapshot: UsageHeaderSnapshot = {
      event_hash: 'older-auth-header',
      timestamp_ms: 1_000,
      header_error_kind: 'auth',
      header_error_code: 'invalid_api_key',
    };

    const sources = getFreshAuthFileCodexStatusSources(
      file,
      undefined,
      undefined,
      headerSnapshot,
      2_000
    );
    const status = getAuthFileCodexStatus(file, undefined, undefined, sources.headerSnapshot);

    expect(sources.headerSnapshot).toBeUndefined();
    expect(status.needsReauth).toBe(false);
  });

  it('does not classify request-shape Header errors as reauthentication failures', () => {
    const file = codexFile();
    const headerSnapshot: UsageHeaderSnapshot = {
      event_hash: 'invalid-request-header',
      timestamp_ms: 1_000,
      header_error_kind: 'request',
      header_error_code: 'invalid_request_error',
    };

    const status = getAuthFileCodexStatus(file, undefined, undefined, headerSnapshot);

    expect(status.needsReauth).toBe(false);
    expect(status.badges.map((badge) => badge.kind)).toEqual(['observed_error']);
    expect(hasAuthFileCodexProblemBadge(status)).toBe(false);
  });

  it('keeps actionable credential badges in Auth Files problem filtering', () => {
    const status = getAuthFileCodexStatus(codexFile(), undefined, {
      fileName: 'codex-main.json',
      authIndex: 'codex-main',
      statusCode: 401,
      action: 'reauth',
    });

    expect(hasAuthFileCodexProblemBadge(status)).toBe(true);
  });

  it('keeps client cancellations and single transient failures out of current problems', () => {
    for (const file of [
      codexFile({ status_code: 499, status_message: 'context canceled' }),
      codexFile({ status_code: 503, status_message: 'upstream unavailable' }),
    ]) {
      const status = getAuthFileCodexStatus(file);

      expect(status.hasRawStatusWarning).toBe(false);
      expect(hasAuthFileStatusProblem(status)).toBe(false);
    }
  });

  it('keeps HTTP 499 neutral across raw, quota, inspection, and Header status sources', () => {
    const file = codexFile({
      status_code: 499,
      status_message: 'authentication_error: request canceled by client',
    });
    const quota = codexQuota({
      status: 'error',
      windows: [],
      errorStatus: 499,
      error: 'authentication_error: request canceled by client',
      failedAtMs: 2_000,
    });
    const inspection: AuthFileCodexInspectionSnapshot = {
      fileName: file.name,
      authIndex: file.authIndex,
      statusCode: 499,
      action: 'reauth',
      errorKind: 'authentication_error',
      inspectionAtMs: 3_000,
    };
    const headerSnapshot: UsageHeaderSnapshot = {
      event_hash: 'cancelled-auth-looking-header',
      timestamp_ms: 4_000,
      header_error_kind: 'authentication_error',
      header_error_code: 'HTTP 499',
    };

    const status = getAuthFileCodexStatus(file, quota, inspection, headerSnapshot);

    expect(isAuthFileInspectionAuthenticationFailure(inspection)).toBe(false);
    expect(status.needsReauth).toBe(false);
    expect(status.hasRawStatusWarning).toBe(false);
    expect(hasAuthFileStatusProblem(status)).toBe(false);
  });

  it('keeps authentication evidence actionable even when it arrives with HTTP 503', () => {
    const status = getAuthFileCodexStatus(
      codexFile({
        status_code: 503,
        status_message: 'authentication_error: invalid token',
      })
    );

    expect(status.needsReauth).toBe(true);
    expect(status.hasRawStatusWarning).toBe(true);
    expect(hasAuthFileStatusProblem(status)).toBe(true);
  });

  it('retires a dated raw 401 after newer authenticated quota evidence', () => {
    const file = codexFile({
      status_code: 401,
      status_message: 'unauthorized',
      updated_at_ms: 1_700_000_000_000,
    });
    const headerSnapshot: UsageHeaderSnapshot = {
      event_hash: 'newer-authenticated-quota',
      timestamp_ms: 1_700_000_001_000,
      model: CODEX_MAIN_MODEL,
      header_quota_used_percent: 100,
      header_quota_recover_at_ms: 1_700_000_100_000,
    };

    const status = getAuthFileCodexStatus(file, undefined, undefined, headerSnapshot);

    expect(status.needsReauth).toBe(false);
    expect(status.isQuotaLimited).toBe(true);
    expect(status.hasRawStatusWarning).toBe(false);
  });

  it('keeps a raw 401 conservative when its observation time is unknown', () => {
    const status = getAuthFileCodexStatus(
      codexFile({ status_code: 401, status_message: 'unauthorized' }),
      undefined,
      undefined,
      {
        event_hash: 'newer-authenticated-quota-with-unknown-file-time',
        timestamp_ms: 1_700_000_001_000,
        header_quota_used_percent: 100,
      }
    );

    expect(status.needsReauth).toBe(true);
    expect(status.hasRawStatusWarning).toBe(true);
  });

  it('does not let a newer non-authentication inspection hide a raw 401', () => {
    const file = codexFile({ status_code: 401, updated_at_ms: 1_000 });
    const inspection: AuthFileCodexInspectionSnapshot = {
      fileName: file.name,
      authIndex: file.authIndex,
      statusCode: 503,
      action: 'keep',
      usedPercent: null,
      isQuota: false,
      errorKind: 'upstream_error',
      inspectionAtMs: 2_000,
    };

    const status = getAuthFileCodexStatus(file, undefined, inspection);

    expect(status.needsReauth).toBe(true);
    expect(status.isHttp401).toBe(true);
  });

  it('does not let an older healthy inspection hide a newer raw 401', () => {
    const file = codexFile({ status_code: 401, updated_at_ms: 3_000 });
    const inspection: AuthFileCodexInspectionSnapshot = {
      fileName: file.name,
      authIndex: file.authIndex,
      statusCode: 200,
      action: 'keep',
      usedPercent: 20,
      isQuota: false,
      errorKind: 'inference_healthy',
      inspectionAtMs: 2_000,
    };

    const status = getAuthFileCodexStatus(file, undefined, inspection);

    expect(status.needsReauth).toBe(true);
    expect(status.isHttp401).toBe(true);
  });

  it('keeps unknown-time inspection authentication failures conservative', () => {
    const file = codexFile();
    const inspection: AuthFileCodexInspectionSnapshot = {
      fileName: file.name,
      authIndex: file.authIndex,
      statusCode: 401,
      action: 'reauth',
      usedPercent: null,
      isQuota: false,
      inspectionAtMs: 0,
    };

    const sources = getFreshAuthFileCodexStatusSources(
      file,
      undefined,
      inspection,
      undefined,
      2_000
    );
    const status = getAuthFileCodexStatus(file, undefined, sources.inspection);

    expect(sources.inspection).toBe(inspection);
    expect(status.needsReauth).toBe(true);
  });

  it('uses inspection authentication error kinds without requiring HTTP 401', () => {
    const file = codexFile();
    const status = getAuthFileCodexStatus(file, undefined, {
      fileName: file.name,
      authIndex: file.authIndex,
      statusCode: 503,
      action: 'keep',
      usedPercent: null,
      isQuota: false,
      errorKind: 'authentication_error',
      inspectionAtMs: 2_000,
    });

    expect(status.needsReauth).toBe(true);
    expect(status.isHttp401).toBe(false);
    expect(
      isAuthFileInspectionAuthenticationFailure({
        fileName: file.name,
        authIndex: file.authIndex,
        statusCode: 503,
        action: 'keep',
        isQuota: false,
        errorKind: 'authentication_error',
      })
    ).toBe(true);
    expect(
      isAuthFileInspectionAuthenticationFailure({
        fileName: file.name,
        authIndex: file.authIndex,
        statusCode: 503,
        action: 'keep',
        isQuota: false,
        errorKind: 'upstream_error',
      })
    ).toBe(false);
    for (const errorKind of ['refresh_token_reused', 'token_invalidated']) {
      expect(
        isAuthFileInspectionAuthenticationFailure({
          fileName: file.name,
          authIndex: file.authIndex,
          statusCode: 503,
          action: 'keep',
          isQuota: false,
          errorKind,
        })
      ).toBe(true);
    }
  });

  it('does not let a newer neutral Header suppress unresolved inspection advice', () => {
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
      event_hash: 'newer-invalid-request',
      timestamp_ms: 2_000,
      header_error_kind: 'request',
      header_error_code: 'invalid_request_error',
      header_trace_id: 'trace-neutral',
    };

    const sources = getFreshAuthFileCodexStatusSources(file, undefined, inspection, headerSnapshot);

    expect(sources.inspection).toBe(inspection);
    expect(sources.headerSnapshot).toBe(headerSnapshot);
  });

  it('treats underscore rate-limit outcomes as quota evidence', () => {
    const status = getAuthFileCodexStatus(codexFile(), undefined, undefined, {
      event_hash: 'rate-limit-reached',
      timestamp_ms: 1_000,
      model: CODEX_MAIN_MODEL,
      header_error_kind: 'rate_limit',
      header_error_code: 'rate_limit_reached',
    });

    expect(status.isQuotaLimited).toBe(true);
    expect(status.needsReauth).toBe(false);
  });

  it('treats explicit quota-limit markers as quota evidence', () => {
    for (const code of [
      'quota_limit_reached',
      'credits_limit_reached',
      'free_usage_limit_reached',
    ]) {
      const status = getAuthFileCodexStatus(codexFile(), undefined, undefined, {
        event_hash: code,
        timestamp_ms: 1_000,
        model: CODEX_MAIN_MODEL,
        header_error_kind: 'rate_limit',
        header_error_code: code,
      });

      expect(status.isQuotaLimited).toBe(true);
      expect(status.needsReauth).toBe(false);
    }
  });

  it('removes superseded auth diagnostics while preserving quota evidence from the same Header', () => {
    const file = codexFile();
    const headerSnapshot: UsageHeaderSnapshot = {
      event_hash: 'mixed-auth-quota-header',
      timestamp_ms: 1_000,
      model: CODEX_MAIN_MODEL,
      header_error_kind: 'auth',
      header_error_code: 'invalid_api_key',
      header_quota_used_percent: 100,
      header_quota_recover_at_ms: 9_000,
      response_metadata: {
        quota: {
          rate_limit_reached_type: 'secondary',
          recover_at_ms: 9_000,
        },
        errors: {
          kind: 'authentication_error',
          code: 'invalid_token',
          retry_after_seconds: 8,
        },
      },
    };

    const sources = getFreshAuthFileCodexStatusSources(
      file,
      undefined,
      undefined,
      headerSnapshot,
      2_000
    );
    const status = getAuthFileCodexStatus(file, undefined, undefined, sources.headerSnapshot);

    expect(sources.headerSnapshot).toBeDefined();
    expect(sources.headerSnapshot).not.toBe(headerSnapshot);
    expect(sources.headerSnapshot).toMatchObject({
      header_error_kind: undefined,
      header_error_code: undefined,
      header_quota_used_percent: 100,
      header_quota_recover_at_ms: 9_000,
      response_metadata: {
        quota: {
          rate_limit_reached_type: 'secondary',
          recover_at_ms: 9_000,
        },
        errors: {
          kind: undefined,
          code: undefined,
          retry_after_seconds: 8,
        },
      },
    });
    expect(headerSnapshot.header_error_kind).toBe('auth');
    expect(headerSnapshot.response_metadata?.errors?.code).toBe('invalid_token');
    expect(status.needsReauth).toBe(false);
    expect(status.isQuotaLimited).toBe(true);
    expect(status.badges.map((badge) => badge.kind)).not.toContain('reauth');
  });

  it('removes generic auth companions while preserving trace and meaningful non-auth diagnostics', () => {
    const headerSnapshot: UsageHeaderSnapshot = {
      event_hash: 'auth-with-generic-companions',
      timestamp_ms: 1_000,
      header_error_kind: 'error',
      header_trace_id: 'trace-1',
      response_metadata: {
        errors: {
          kind: 'server_error',
          authorization_error: 'invalid_api_key',
          ide_error_code: 'upstream_unavailable',
          should_retry: true,
        },
      },
    };

    const sanitized = sanitizeSupersededAuthHeaderSnapshot(headerSnapshot, 2_000);

    expect(sanitized).toMatchObject({
      header_error_kind: undefined,
      header_trace_id: 'trace-1',
      response_metadata: {
        errors: {
          kind: 'server_error',
          authorization_error: undefined,
          ide_error_code: 'upstream_unavailable',
          should_retry: true,
        },
      },
    });
    expect(headerSnapshot.header_error_kind).toBe('error');
    expect(headerSnapshot.response_metadata?.errors?.authorization_error).toBe('invalid_api_key');
  });

  it('keeps an older quota header after a newer successful request', () => {
    const file = codexFile();
    const headerSnapshot: UsageHeaderSnapshot = {
      event_hash: 'older-quota-header',
      timestamp_ms: 1_000,
      model: CODEX_MAIN_MODEL,
      header_error_kind: 'rate_limit',
      header_error_code: 'quota_exceeded',
    };

    const sources = getFreshAuthFileCodexStatusSources(
      file,
      undefined,
      undefined,
      headerSnapshot,
      2_000
    );
    const status = getAuthFileCodexStatus(file, undefined, undefined, sources.headerSnapshot);

    expect(sources.headerSnapshot).toBe(headerSnapshot);
    expect(status.isQuotaLimited).toBe(true);
  });

  it('keeps partial Header quota while removing auth diagnostics superseded by Provider success', () => {
    const file = codexFile();
    const quota = codexQuota({
      authFileKey: getAuthFileCodexInspectionKey(file.name, file.authIndex),
      fetchedAtMs: 2_000,
      quotaInventoryObserved: false,
      windows: [],
    });
    const headerSnapshot: UsageHeaderSnapshot = {
      event_hash: 'same-time-mixed-header',
      timestamp_ms: 2_000,
      model: CODEX_MAIN_MODEL,
      header_error_kind: 'auth',
      header_error_code: 'invalid_api_key',
      header_quota_used_percent: 100,
      header_quota_recover_at_ms: 9_000,
    };

    const sources = getFreshAuthFileCodexStatusSources(file, quota, undefined, headerSnapshot);
    const status = getAuthFileCodexStatus(file, quota, undefined, sources.headerSnapshot);

    expect(sources.headerSnapshot).toMatchObject({
      header_error_kind: undefined,
      header_error_code: undefined,
      header_quota_used_percent: 100,
    });
    expect(status.needsReauth).toBe(false);
    expect(status.isQuotaLimited).toBe(true);
  });

  it('uses exhausted provider usage as authenticated quota evidence', () => {
    const file = codexFile({ type: 'xai' });
    const inspection: AuthFileCodexInspectionSnapshot = {
      fileName: file.name,
      authIndex: file.authIndex,
      provider: 'xai',
      statusCode: 401,
      action: 'reauth',
      usedPercent: null,
      isQuota: false,
      inspectionAtMs: 1_000,
    };
    const headerSnapshot: UsageHeaderSnapshot = {
      event_hash: 'xai-exhausted-provider-usage',
      timestamp_ms: 2_000,
      response_metadata: {
        provider_usage: {
          provider: 'xai',
          state: 'exhausted',
          actual: 100,
          limit: 100,
          remaining: 0,
        },
      },
    };

    const sources = getFreshAuthFileCodexStatusSources(file, undefined, inspection, headerSnapshot);
    const status = getAuthFileCodexStatus(
      file,
      undefined,
      sources.inspection,
      sources.headerSnapshot
    );

    expect(sources.inspection).toBeUndefined();
    expect(status.needsReauth).toBe(false);
    expect(status.isQuotaLimited).toBe(true);
    expect(status.isUnknownQuotaLimited).toBe(true);
  });

  it('expires provider usage quota evidence after its recovery time', () => {
    const file = codexFile({ type: 'xai' });
    const headerSnapshot: UsageHeaderSnapshot = {
      event_hash: 'xai-expired-provider-usage',
      timestamp_ms: 1_000,
      response_metadata: {
        provider_usage: {
          provider: 'xai',
          state: 'exhausted',
          actual: 100,
          limit: 100,
          remaining: 0,
          recover_at_ms: 2_000,
        },
      },
    };

    expect(
      getAuthFileCodexStatus(file, undefined, undefined, headerSnapshot, 1_500).isQuotaLimited
    ).toBe(true);
    expect(
      getAuthFileCodexStatus(file, undefined, undefined, headerSnapshot, 2_000).isQuotaLimited
    ).toBe(false);
  });

  it('keeps exhausted provider usage limited when the recovery time is unknown', () => {
    const file = codexFile({ type: 'xai' });
    const headerSnapshot: UsageHeaderSnapshot = {
      event_hash: 'xai-unknown-provider-recovery',
      timestamp_ms: 1_000,
      response_metadata: {
        provider_usage: {
          provider: 'xai',
          state: 'exhausted',
          actual: 100,
          limit: 100,
          remaining: 0,
          recover_at_ms: 0,
        },
      },
    };

    expect(
      getAuthFileCodexStatus(file, undefined, undefined, headerSnapshot, 10_000).isQuotaLimited
    ).toBe(true);
  });

  it('uses the explicit current time when deciding whether recovered Header evidence supersedes inspection', () => {
    const file = codexFile({ type: 'xai' });
    const inspection: AuthFileCodexInspectionSnapshot = {
      fileName: file.name,
      provider: 'xai',
      authIndex: file.authIndex,
      action: 'keep',
      isQuota: false,
      errorKind: 'future_xai_failure',
      inspectionAtMs: 1_000,
    };
    const headerSnapshot: UsageHeaderSnapshot = {
      event_hash: 'xai-recovering-provider-usage',
      timestamp_ms: 2_000,
      response_metadata: {
        provider_usage: {
          provider: 'xai',
          state: 'exhausted',
          actual: 100,
          limit: 100,
          remaining: 0,
          recover_at_ms: 3_000,
        },
      },
    };

    const beforeRecovery = getFreshAuthFileCodexStatusSources(
      file,
      undefined,
      inspection,
      headerSnapshot,
      undefined,
      2_999
    );
    const afterRecovery = getFreshAuthFileCodexStatusSources(
      file,
      undefined,
      inspection,
      headerSnapshot,
      undefined,
      3_000
    );

    expect(beforeRecovery.inspection).toBeUndefined();
    expect(afterRecovery.inspection).toBe(inspection);
  });

  it('uses provider usage as healthy evidence only when capacity remains', () => {
    const file = codexFile({ type: 'xai' });
    const inspection: AuthFileCodexInspectionSnapshot = {
      fileName: file.name,
      authIndex: file.authIndex,
      provider: 'xai',
      statusCode: 401,
      action: 'reauth',
      usedPercent: null,
      isQuota: false,
      inspectionAtMs: 1_000,
    };
    const headerSnapshot: UsageHeaderSnapshot = {
      event_hash: 'xai-healthy-provider-usage',
      timestamp_ms: 2_000,
      response_metadata: {
        provider_usage: {
          provider: 'xai',
          state: 'available',
          actual: 20,
          limit: 100,
          remaining: 80,
        },
      },
    };

    const sources = getFreshAuthFileCodexStatusSources(file, undefined, inspection, headerSnapshot);

    expect(sources.inspection).toBeUndefined();
    expect(sources.headerSnapshot).toBe(headerSnapshot);
  });

  it('uses a depleted unclassified Header window as authenticated quota evidence', () => {
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
      event_hash: 'unknown-window-exhausted',
      timestamp_ms: 2_000,
      model: CODEX_MAIN_MODEL,
      response_metadata: {
        quota: {
          primary: { used_percent: 100, window_minutes: 60 },
        },
      },
    };

    const sources = getFreshAuthFileCodexStatusSources(file, undefined, inspection, headerSnapshot);
    const status = getAuthFileCodexStatus(
      file,
      undefined,
      sources.inspection,
      sources.headerSnapshot
    );

    expect(sources.inspection).toBeUndefined();
    expect(status.needsReauth).toBe(false);
    expect(status.isQuotaLimited).toBe(true);
    expect(status.isUnknownQuotaLimited).toBe(true);
  });

  it('marks an auth-only quota failure with no retained windows as partial after recovery', () => {
    const quota = codexQuota({
      status: 'error',
      fetchedAtMs: 1_000,
      failedAtMs: 1_000,
      error: 'HTTP 401 unauthorized',
      errorStatus: 401,
      windows: [],
    });

    const sanitized = sanitizeSupersededAuthQuotaState(quota, 2_000);

    expect(sanitized).toMatchObject({
      status: 'success',
      quotaInventoryObserved: false,
      windows: [],
    });
    expect(quota.quotaInventoryObserved).toBeUndefined();
  });

  it('clears an explicitly scoped unknown-time auth quota failure without dropping quota windows', () => {
    const quota = codexQuota({
      status: 'error',
      error: 'HTTP 401 unauthorized',
      errorStatus: 401,
      fetchedAtMs: undefined,
      failedAtMs: undefined,
    });

    const sanitized = sanitizeSupersededAuthQuotaState(quota, 2_000, {
      allowUnknownFailureTimestamp: true,
    });

    expect(sanitized).toMatchObject({
      status: 'success',
      error: undefined,
      errorStatus: undefined,
      windows: quota.windows,
    });
    expect(sanitizeSupersededAuthQuotaState(quota, 2_000)).toBe(quota);
  });

  it('recognizes and clears text-only HTTP 401 quota refresh failures', () => {
    const quota = codexQuota({
      status: 'error',
      fetchedAtMs: 1_000,
      failedAtMs: 1_000,
      error: 'quota refresh failed: HTTP 401',
      errorStatus: undefined,
      windows: [],
    });

    expect(getAuthFileCodexStatus(codexFile(), quota).needsReauth).toBe(true);
    expect(sanitizeSupersededAuthQuotaState(quota, 2_000)).toMatchObject({
      status: 'success',
      error: undefined,
    });
  });

  it('removes superseded auth-only quota failures without mutating retained quota data', () => {
    const quota = codexQuota({
      status: 'error',
      fetchedAtMs: 1_000,
      failedAtMs: 1_000,
      error: 'HTTP 401 unauthorized',
      errorStatus: 401,
      observedAtMs: 900,
      observedErrorKind: 'error',
      observedErrorCode: 'invalid_api_key',
      windows: [
        {
          id: 'weekly',
          label: 'Weekly',
          usedPercent: 20,
          resetLabel: 'Mon',
        },
      ],
    });

    const sanitized = sanitizeSupersededAuthQuotaState(quota, 2_000);

    expect(sanitized).toMatchObject({
      status: 'success',
      error: undefined,
      errorStatus: undefined,
      observedErrorKind: undefined,
      observedErrorCode: undefined,
      windows: quota.windows,
    });
    expect(quota.status).toBe('error');
    expect(quota.errorStatus).toBe(401);
    expect(quota.observedErrorCode).toBe('invalid_api_key');
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

  it('does not suppress older Codex inspection after a newer trace-only header snapshot', () => {
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
      header_quota_used_percent: 30,
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

  it('does not suppress older Codex inspection after a newer transient header failure', () => {
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
      event_hash: 'newer-server-error',
      timestamp_ms: 2_000,
      header_error_kind: 'server_error',
      header_error_code: 'upstream_unavailable',
      header_trace_id: 'trace-server-error',
    };

    const sources = getFreshAuthFileCodexStatusSources(file, undefined, inspection, headerSnapshot);

    expect(sources.inspection).toBe(inspection);
    expect(sources.headerSnapshot).toBe(headerSnapshot);
  });

  it('suppresses older Codex inspection after a newer explicit healthy quota header', () => {
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
      event_hash: 'newer-healthy-quota',
      timestamp_ms: 2_000,
      header_quota_used_percent: 20,
      header_quota_plan_type: 'plus',
      header_trace_id: 'trace-healthy-quota',
    };

    const sources = getFreshAuthFileCodexStatusSources(file, undefined, inspection, headerSnapshot);

    expect(sources.inspection).toBeUndefined();
    expect(sources.headerSnapshot).toBe(headerSnapshot);
  });

  it('does not let a newer null-status inspection hide an older Header auth error', () => {
    const file = codexFile();
    const inspection: AuthFileCodexInspectionSnapshot = {
      fileName: file.name,
      authIndex: file.authIndex,
      statusCode: null,
      action: 'keep',
      usedPercent: null,
      isQuota: false,
      inspectionAtMs: 2_000,
    };
    const headerSnapshot: UsageHeaderSnapshot = {
      event_hash: 'older-auth-error',
      timestamp_ms: 1_000,
      header_error_kind: 'auth_invalid',
      header_error_code: 'token_expired',
    };

    const sources = getFreshAuthFileCodexStatusSources(file, undefined, inspection, headerSnapshot);
    const status = getAuthFileCodexStatus(
      file,
      undefined,
      sources.inspection,
      sources.headerSnapshot
    );

    expect(sources.inspection).toBe(inspection);
    expect(sources.headerSnapshot).toBe(headerSnapshot);
    expect(status.needsReauth).toBe(true);
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

  it('does not let a newer non-authentication inspection suppress an older auth header', () => {
    const file = codexFile();
    const inspection: AuthFileCodexInspectionSnapshot = {
      fileName: file.name,
      authIndex: file.authIndex,
      statusCode: 503,
      action: 'keep',
      usedPercent: null,
      isQuota: false,
      errorKind: 'upstream_error',
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
    expect(sources.headerSnapshot).toBe(headerSnapshot);
    expect(status.needsReauth).toBe(true);
  });

  it('does not let a provider-mismatched inspection suppress a matching header snapshot', () => {
    const file = codexFile();
    const inspection: AuthFileCodexInspectionSnapshot = {
      fileName: file.name,
      provider: 'xai',
      authIndex: file.authIndex,
      statusCode: 200,
      action: null,
      usedPercent: null,
      isQuota: false,
      inspectionAtMs: 2_000,
    };
    const headerSnapshot: UsageHeaderSnapshot = {
      event_hash: 'matching-header',
      timestamp_ms: 1_000,
      header_error_kind: 'auth',
      header_error_code: 'invalid_api_key',
    };

    const sources = getFreshAuthFileCodexStatusSources(file, undefined, inspection, headerSnapshot);

    expect(sources.inspection).toBeUndefined();
    expect(sources.headerSnapshot).toBe(headerSnapshot);
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
    const first = codexFile({
      id: 'runtime-shared-0',
      name: 'shared-codex.json',
      authIndex: 0,
      account_id: 'account-shared-0',
    });
    const second = codexFile({ name: 'shared-codex.json', authIndex: 1 });
    const quota = codexQuota({
      authFileKey: getAuthFileCodexInspectionKey(first.name, first.authIndex),
    });

    expect(getAuthFileScopedCodexQuota(first, quota)).toBe(quota);
    expect(getAuthFileScopedCodexQuota(second, quota)).toBeUndefined();
  });

  it('drops legacy Codex quota without identity for files without auth index', () => {
    const quota = codexQuota();

    expect(getAuthFileScopedCodexQuota(codexFile({ authIndex: undefined }), quota)).toBeUndefined();
  });

  it('drops legacy Codex quota without identity for auth-indexed files', () => {
    const quota = codexQuota();

    expect(getAuthFileScopedCodexQuota(codexFile(), quota)).toBeUndefined();
  });
});

describe('credential identity helpers', () => {
  it('keeps same-file auth rows distinct for selection and patch targets', () => {
    const first = codexFile({
      id: 'runtime-shared-0',
      name: 'shared-codex.json',
      authIndex: 0,
      account_id: 'account-shared-0',
    });
    const second = codexFile({ name: 'shared-codex.json', authIndex: 1 });
    const firstKey = getAuthFileSelectionKey(first);
    const secondKey = getAuthFileSelectionKey(second);

    expect(firstKey).not.toBe(secondKey);
    expect(getAuthFileNameFromSelectionKey(firstKey)).toBe('shared-codex.json');
    expect(getAuthFilePatchTarget(first)).toEqual({
      name: 'shared-codex.json',
      runtimeId: 'runtime-shared-0',
      authIndex: 0,
      provider: 'codex',
      accountId: 'account-shared-0',
    });
    expect(
      getAuthFilePatchTarget(codexFile({ authIndex: undefined, account: 'codex-main@example.com' }))
    ).toEqual({
      name: 'codex-main.json',
      provider: 'codex',
      accountSnapshot: 'codex-main@example.com',
    });
    expect(
      getAuthFilePatchTarget(
        codexFile({
          id: 'runtime-team-member',
          account_id: 'workspace-1',
          account: 'Alice',
          email: 'Alice@Example.com',
        })
      )
    ).toMatchObject({
      provider: 'codex',
      accountId: 'workspace-1',
      accountSnapshot: 'alice@example.com',
    });
    expect(
      getAuthFilePatchTarget(
        codexFile({
          id: 'runtime-weak-member',
          account_id: 'workspace-1',
          account: 'Alice',
          email: undefined,
        })
      )
    ).not.toHaveProperty('accountSnapshot');
    expect(
      getAuthFilePatchTarget({
        id: 'runtime-unknown',
        name: 'unknown.json',
        account: 'unknown@example.com',
      } as AuthFileItem)
    ).toEqual({
      name: 'unknown.json',
      runtimeId: 'runtime-unknown',
      accountSnapshot: 'unknown@example.com',
    });
    expect(
      getAuthFilePatchTarget({
        id: 'runtime-xai',
        name: 'xai.json',
        type: '',
        provider: 'xai',
        account: 'xai@example.com',
      } as AuthFileItem)
    ).toEqual({
      name: 'xai.json',
      runtimeId: 'runtime-xai',
      provider: 'xai',
      accountSnapshot: 'xai@example.com',
    });
  });

  it('keeps same-file rows without auth indexes distinct by stable account identity', () => {
    const first = codexFile({
      id: 'runtime-shared-0',
      name: 'shared-codex.json',
      authIndex: undefined,
      account_id: 'account-shared-0',
      account: 'first@example.com',
    });
    const renamed = codexFile({
      ...first,
      account: 'renamed@example.com',
    });
    const second = codexFile({
      id: 'runtime-shared-1',
      name: 'shared-codex.json',
      authIndex: undefined,
      account: 'second@example.com',
    });

    expect(getAuthFileSelectionKey(first)).not.toBe(getAuthFileSelectionKey(renamed));
    expect(getAuthFileSelectionKey(first)).not.toBe(getAuthFileSelectionKey(second));
    expect(getAuthFileNameFromSelectionKey(getAuthFileSelectionKey(second))).toBe(
      'shared-codex.json'
    );
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
