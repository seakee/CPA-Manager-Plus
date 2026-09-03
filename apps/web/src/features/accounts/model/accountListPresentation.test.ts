import { describe, expect, it } from 'vitest';
import type { AuthFileItem } from '@/types';
import type { QuotaCooldownInfo } from '@/services/api';
import type { AuthFileCodexStatusSummary } from '@/features/authFiles/model/credentialStatus';
import type { AccountRow } from './accountRows';
import { buildAccountListItem, buildRecommendationBySelectionKey } from './accountListPresentation';
import { summarizeGroupedQuotaAvailability } from './accountQuotaSummary';
import type { AccountRecommendation } from './quotaRecommendations';

const CODEX_MAIN_SCOPE = { kind: 'family', key: 'codex_main', complete: true } as const;

type AccountRowOverrides = Omit<Partial<AccountRow>, 'quota'> & {
  quota?: Partial<AccountRow['quota']>;
};

const makeRow = (overrides: AccountRowOverrides = {}): AccountRow => {
  const { quota: quotaOverrides, ...rowOverrides } = overrides;
  const raw: AuthFileItem = {
    name: overrides.fileName ?? 'codex-1.json',
    type: overrides.provider ?? 'codex',
  };

  return {
    key: raw.name,
    selectionKey: `${raw.name}\u0000${overrides.authIndex ?? '-'}`,
    fileName: raw.name,
    accountLabel: raw.name,
    provider: 'codex',
    planType: null,
    disabled: false,
    runtimeOnly: false,
    statusMessage: '',
    authIndex: '',
    projectId: '',
    priority: null,
    createdAtMs: null,
    updatedAtMs: null,
    authenticationAtMs: 0,
    rawCredentialStatusSuperseded: false,
    quota: {
      status: 'ok',
      remainingPercent: 80,
      usedPercent: 20,
      resetLabel: '-',
      resetAtMs: null,
      resetAccuracy: 'unknown',
      planType: null,
      source: 'cache',
      ...quotaOverrides,
    },
    usage: {
      success: 0,
      failure: 0,
      successRate: null,
      recentRequests: [],
    },
    inspection: null,
    raw,
    ...rowOverrides,
  };
};

const makeRecommendation = (
  row: AccountRow,
  overrides: Partial<AccountRecommendation> = {}
): AccountRecommendation => ({
  row,
  action: 'refresh',
  priority: 'high',
  reasonKey: 'accounts.recommend_reason_low',
  ...overrides,
});

const makeCodexStatus = (
  overrides: Partial<AuthFileCodexStatusSummary> = {}
): AuthFileCodexStatusSummary => ({
  isCodex: true,
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
  hasRawStatusWarning: false,
  badges: [],
  ...overrides,
});

describe('accountListPresentation', () => {
  it('attaches compact and full plan presentation to account list identity', () => {
    const item = buildAccountListItem(
      makeRow({
        provider: 'codex',
        planType: 'self_serve_business_prolite',
      })
    );

    expect(item.identity.planPresentation).toMatchObject({
      rawPlanType: 'self_serve_business_prolite',
      canonicalPlanType: 'business_premium_5x',
      shortLabel: 'Business 5x',
      fullLabel: 'Business Premium 5x',
      known: true,
    });
  });

  it('prioritizes re-authentication over quota state', () => {
    const row = makeRow({
      quota: {
        status: 'low',
        remainingPercent: 5,
        usedPercent: 95,
        resetLabel: 'later',
        planType: null,
        source: 'cache',
      },
      inspection: {
        source: 'server',
        action: 'reauth',
        actionReason: 'expired',
        actionStatus: 'pending',
        statusCode: 401,
        usedPercent: null,
        runId: 1,
        resultId: 2,
        createdAtMs: 3,
      },
    });
    const recommendation = makeRecommendation(row, {
      action: 'reauth',
      priority: 'critical',
      reasonKey: 'accounts.recommend_reason_inspection',
    });

    const item = buildAccountListItem(row, { recommendation });

    expect(item.health.status).toBe('reauth');
    expect(item.health.labelKey).toBe('accounts.health_reauth');
    expect(item.health.reasonKey).toBe('accounts.health_reason_reauth_inspection');
    expect(item.health.reasonParams).toEqual({ detail: 'HTTP 401' });
    expect(item.health.reasonTone).toBe('danger');
    expect(item.recommendation.actionLabelKey).toBe('accounts.recommend_action_reauth');
  });

  it('uses Codex status reset timestamps when quota windows are unavailable', () => {
    const resetAtMs = Date.parse('2026-08-20T03:40:00Z');
    const item = buildAccountListItem(makeRow(), {
      codexStatus: makeCodexStatus({
        isFiveHourLimited: true,
        isQuotaLimited: true,
        fiveHourResetLabel: '08/20 03:40',
        fiveHourResetAtMs: resetAtMs,
        fiveHourResetAccuracy: 'exact',
      }),
    });

    expect(item.health).toMatchObject({
      status: 'five_hour_exhausted',
      resetAtMs,
    });
    expect(item.health.tooltipParams.resetAt).toBe('08/20 03:40');
  });

  it('lets a newer successful request clear stale inspection health and advice', () => {
    const row = makeRow({
      inspection: {
        source: 'server',
        action: 'reauth',
        actionReason: 'expired',
        actionStatus: 'pending',
        statusCode: 401,
        usedPercent: null,
        runId: 1,
        resultId: 2,
        createdAtMs: 1_000,
      },
    });
    const staleRecommendation = makeRecommendation(row, {
      action: 'reauth',
      priority: 'critical',
      reasonKey: 'accounts.recommend_reason_inspection',
    });

    const item = buildAccountListItem(row, {
      recommendation: staleRecommendation,
      codexStatus: makeCodexStatus({ needsReauth: true, isHttp401: true }),
      requestEvidence: {
        latestRequest: { timestamp_ms: 2_000, failed: false },
      },
    });

    expect(item.health).toMatchObject({
      status: 'available',
      reasonKey: 'accounts.health_reason_available_request',
      basisLabelKey: 'accounts.latest_request_time_title',
      observedAtMs: 2_000,
    });
    expect(item.recommendation.hasRecommendation).toBe(false);
  });

  it('does not surface a pre-reauth success request as available', () => {
    const row = makeRow({ authenticationAtMs: 2_000 });

    expect(
      buildAccountListItem(row, {
        requestEvidence: { latestRequest: { timestamp_ms: 1_000, failed: false } },
      }).health.status
    ).toBe('raw');
    expect(
      buildAccountListItem(row, {
        requestEvidence: { latestRequest: { timestamp_ms: 3_000, failed: false } },
      }).health.status
    ).toBe('available');
  });

  it('lets a newer successful request clear stale quota refresh failure advice', () => {
    const row = makeRow({
      quota: {
        status: 'error',
        error: 'upstream unavailable',
        errorStatus: 503,
        failedAtMs: 1_000,
      },
    });
    const staleRecommendation = makeRecommendation(row, {
      action: 'refresh',
      priority: 'medium',
      reasonKey: 'accounts.recommend_reason_error',
    });

    const item = buildAccountListItem(row, {
      recommendation: staleRecommendation,
      requestEvidence: {
        latestRequest: { timestamp_ms: 2_000, failed: false },
      },
    });

    expect(item.health).toMatchObject({
      status: 'available',
      reasonKey: 'accounts.health_reason_available_request',
      observedAtMs: 2_000,
    });
    expect(item.recommendation.hasRecommendation).toBe(false);
  });

  it('lets a newer success clear auth header diagnostics but not quota diagnostics', () => {
    const requestEvidence = {
      latestRequest: { timestamp_ms: 2_000, failed: false },
    };
    const authItem = buildAccountListItem(
      makeRow({
        quota: {
          observedAtMs: 1_000,
          observedErrorKind: 'auth',
          observedErrorCode: 'invalid_api_key',
        },
      }),
      { requestEvidence }
    );
    const quotaItem = buildAccountListItem(
      makeRow({
        quota: {
          observedAtMs: 1_000,
          observedErrorKind: 'rate_limit',
          observedErrorCode: 'quota_exceeded',
        },
      }),
      { requestEvidence }
    );

    expect(authItem.health.status).toBe('available');
    expect(quotaItem.health.status).toBe('limited');
  });

  it('treats current credential and Header authentication failures as reauth', () => {
    const credentialItem = buildAccountListItem(
      makeRow({
        raw: { name: 'codex-1.json', type: 'codex', status_code: 401 },
        updatedAtMs: 2_000,
      })
    );
    const headerItem = buildAccountListItem(
      makeRow({
        quota: {
          observedAtMs: 3_000,
          observedErrorKind: 'auth',
          observedErrorCode: 'invalid_api_key',
        },
      })
    );

    expect(credentialItem.health).toMatchObject({
      status: 'reauth',
      reasonKey: 'accounts.health_reason_reauth_auth',
    });
    expect(credentialItem.recommendation).toMatchObject({
      item: { action: 'reauth' },
      reasonKey: 'accounts.recommend_reason_credential_auth',
    });
    expect(headerItem.health).toMatchObject({
      status: 'reauth',
      reasonKey: 'accounts.health_reason_reauth_auth',
      basisLabelKey: 'accounts.quota_source_observed_header',
      observedAtMs: 3_000,
    });
    expect(headerItem.recommendation.reasonKey).toBe('accounts.recommend_reason_credential_auth');
  });

  it('treats inspection authentication error kinds as reauthentication evidence', () => {
    const item = buildAccountListItem(
      makeRow({
        inspection: {
          source: 'server',
          action: 'keep',
          actionReason: 'token rejected',
          actionStatus: 'success',
          statusCode: 503,
          usedPercent: null,
          isQuota: false,
          errorKind: 'authentication_error',
          runId: 1,
          resultId: 2,
          createdAtMs: 3_000,
        },
      })
    );

    expect(item.health).toMatchObject({
      status: 'reauth',
      reasonKey: 'accounts.health_reason_reauth_inspection',
      tooltipParams: { detail: 'token rejected' },
    });
    expect(item.recommendation.reasonKey).toBe('accounts.recommend_reason_inspection');
  });

  it('attributes authentication health to newer inspection evidence over an older refresh error', () => {
    const item = buildAccountListItem(
      makeRow({
        quota: {
          status: 'error',
          error: 'HTTP 401 unauthorized',
          errorStatus: 401,
          failedAtMs: 1_000,
        },
        inspection: {
          source: 'server',
          action: 'reauth',
          actionReason: 'expired',
          actionStatus: 'pending',
          statusCode: 401,
          usedPercent: null,
          runId: 1,
          resultId: 2,
          createdAtMs: 2_000,
        },
      })
    );

    expect(item.health).toMatchObject({
      status: 'reauth',
      reasonKey: 'accounts.health_reason_reauth_inspection',
      tooltipParams: { detail: 'HTTP 401' },
    });
    expect(item.recommendation.reasonKey).toBe('accounts.recommend_reason_inspection');
  });

  it('uses the latest exception source instead of a fixed refresh-inspection priority', () => {
    const baseRow = makeRow({
      quota: {
        status: 'error',
        error: 'upstream unavailable',
        errorStatus: 503,
        failedAtMs: 1_000,
      },
      inspection: {
        source: 'server',
        action: 'delete',
        actionReason: 'credential malformed',
        actionStatus: 'pending',
        statusCode: 400,
        usedPercent: null,
        runId: 1,
        resultId: 2,
        createdAtMs: 2_000,
      },
    });

    const inspectionItem = buildAccountListItem(baseRow);
    expect(inspectionItem.health).toMatchObject({
      status: 'exception',
      reasonKey: 'accounts.health_reason_exception_inspection',
      tooltipParams: { detail: 'credential malformed' },
    });
    expect(inspectionItem.recommendation.reasonKey).toBe('accounts.recommend_reason_inspection');

    const refreshItem = buildAccountListItem({
      ...baseRow,
      quota: { ...baseRow.quota, failedAtMs: 3_000 },
    });
    expect(refreshItem.health).toMatchObject({
      status: 'exception',
      reasonKey: 'accounts.health_reason_exception_quota',
      tooltipParams: { detail: 'upstream unavailable' },
    });
    expect(refreshItem.recommendation.reasonKey).toBe('accounts.recommend_reason_error');
  });

  it('presents runtime and explicit provider quota signals as limited', () => {
    const runtimeQuotaItem = buildAccountListItem(
      makeRow({
        statusMessage: 'quota exceeded',
        raw: { name: 'codex-1.json', type: 'codex', status_code: 429 },
      })
    );
    expect(runtimeQuotaItem.health).toMatchObject({
      status: 'limited',
      tooltipParams: { detail: 'quota exceeded' },
    });

    for (const quota of [
      { rateLimitReachedType: 'primary' },
      { spendControlReached: true },
      { creditsOverageLimitReached: true },
    ]) {
      expect(
        buildAccountListItem(
          makeRow({
            quota: { ...makeRow().quota, ...quota },
          })
        ).health.status
      ).toBe('limited');
    }
  });

  it('lets a newer successful request retire an older runtime 429 conclusion', () => {
    const item = buildAccountListItem(
      makeRow({
        statusMessage: 'quota exceeded',
        updatedAtMs: 1_000,
        raw: { name: 'codex-1.json', type: 'codex', status_code: 429 },
      }),
      {
        requestEvidence: {
          latestRequest: { timestamp_ms: 2_000, failed: false },
        },
      }
    );

    expect(item.health.status).toBe('available');
    expect(item.recommendation.hasRecommendation).toBe(false);
  });

  it('presents request 429 evidence as limited with request provenance', () => {
    const item = buildAccountListItem(makeRow(), {
      requestEvidence: {
        recentRequests: [
          {
            timestamp_ms: 3_000,
            failed: true,
            fail_status_code: 429,
            fail_summary: 'rate limit exceeded',
          },
          { timestamp_ms: 2_000, failed: false },
        ],
      },
    });

    expect(item.health).toMatchObject({
      status: 'limited',
      reasonKey: 'accounts.health_reason_limited_request',
      tooltipParams: { detail: 'rate limit exceeded' },
      basisLabelKey: 'accounts.latest_request_time_title',
      observedAtMs: 3_000,
    });
    expect(item.recommendation).toMatchObject({
      hasRecommendation: true,
      actionLabelKey: 'accounts.recommend_action_refresh',
      reasonKey: 'accounts.recommend_reason_quota_limited',
      priority: 'high',
    });
  });

  it('uses the latest authenticated request to replace stale auth and quota conclusions', () => {
    const staleAuthRow = makeRow({
      statusMessage: 'unauthorized',
      updatedAtMs: 1_000,
      raw: { name: 'codex.json', type: 'codex', status_code: 401 },
      inspection: {
        source: 'server',
        action: 'reauth',
        actionReason: 'expired',
        actionStatus: 'pending',
        statusCode: 401,
        usedPercent: null,
        runId: 1,
        resultId: 1,
        createdAtMs: 1_000,
      },
    });
    const quotaItem = buildAccountListItem(staleAuthRow, {
      requestEvidence: {
        recentRequests: [
          {
            timestamp_ms: 2_000,
            failed: true,
            fail_status_code: 429,
            fail_summary: 'rate limit exceeded',
          },
          { timestamp_ms: 1_500, failed: true, fail_status_code: 401 },
        ],
      },
    });
    const recoveredItem = buildAccountListItem(makeRow(), {
      requestEvidence: {
        recentRequests: [
          { timestamp_ms: 3_000, failed: false },
          { timestamp_ms: 2_000, failed: true, fail_status_code: 429 },
        ],
      },
    });

    expect(quotaItem.health.status).toBe('limited');
    expect(quotaItem.recommendation.reasonKey).toBe('accounts.recommend_reason_quota_limited');
    expect(recoveredItem.health.status).toBe('available');
    expect(recoveredItem.recommendation.hasRecommendation).toBe(false);
  });

  it('attributes Provider rate-limit fields to quota and newer Header fields to Header', () => {
    const providerItem = buildAccountListItem(
      makeRow({
        quota: {
          rateLimitReachedType: 'primary',
          fetchedAtMs: 2_000,
          observedAtMs: 1_000,
        },
      })
    );
    const headerItem = buildAccountListItem(
      makeRow({
        quota: {
          rateLimitReachedType: 'primary',
          fetchedAtMs: 1_000,
          observedAtMs: 2_000,
        },
      })
    );

    expect(providerItem.health.reasonKey).toBe('accounts.health_reason_limited_quota');
    expect(headerItem.health.reasonKey).toBe('accounts.health_reason_limited_header');
  });

  it('uses localized tooltip keys for boolean-only quota limits', () => {
    expect(
      buildAccountListItem(
        makeRow({
          statusMessage: 'generic quota state',
          quota: { creditsOverageLimitReached: true },
        })
      ).health
    ).toMatchObject({
      tooltipKey: 'accounts.health_tip_limited_credits_overage',
      tooltipParams: {},
    });
    expect(
      buildAccountListItem(
        makeRow({
          statusMessage: 'generic quota state',
          quota: { spendControlReached: true },
        })
      ).health
    ).toMatchObject({
      tooltipKey: 'accounts.health_tip_limited_spend_control',
      tooltipParams: {},
    });

    expect(
      buildAccountListItem(
        makeRow({
          statusMessage: 'generic quota state',
          quota: { creditsOverageLimitReached: true },
        }),
        {
          requestEvidence: {
            latestRequest: {
              timestamp_ms: 2_000,
              failed: true,
              fail_status_code: 429,
              fail_summary: 'latest request quota evidence',
            },
          },
        }
      ).health
    ).toMatchObject({
      tooltipKey: 'accounts.health_tip_limited',
      tooltipParams: { detail: 'latest request quota evidence' },
    });
  });

  it('uses xAI provider usage limit status in account health', () => {
    const item = buildAccountListItem(makeRow({ provider: 'xai' }), {
      codexStatus: makeCodexStatus({
        isCodex: false,
        isQuotaLimited: true,
        isUnknownQuotaLimited: true,
      }),
    });

    expect(item.health.status).toBe('limited');
  });

  it('keeps HTTP 499 neutral and applies qualified request failures', () => {
    expect(
      buildAccountListItem(makeRow(), {
        requestEvidence: {
          latestRequest: { timestamp_ms: 2_000, failed: true, fail_status_code: 499 },
        },
      }).health.status
    ).toBe('available');
    expect(
      buildAccountListItem(
        makeRow({
          statusMessage: 'context canceled',
          updatedAtMs: 2_000,
          raw: {
            name: 'codex-1.json',
            type: 'codex',
            status_code: 499,
            status_message: 'context canceled',
          },
        }),
        {
          requestEvidence: {
            latestRequest: {
              timestamp_ms: 2_000,
              failed: true,
              fail_status_code: 499,
              fail_summary: 'context canceled',
            },
          },
        }
      ).health.status
    ).toBe('available');

    const quotaFailureWithNeutralRuntimeMessage = buildAccountListItem(
      makeRow({
        statusMessage: 'context canceled',
        raw: { name: 'codex-1.json', type: 'codex', status_code: 499 },
        quota: {
          status: 'error',
          error: 'context canceled',
          errorStatus: 499,
          failedAtMs: 2_000,
        },
      })
    );
    expect(quotaFailureWithNeutralRuntimeMessage.health).toMatchObject({
      status: 'raw',
      tooltipParams: { detail: 'context canceled' },
    });

    const transientQuotaRefreshFailure = buildAccountListItem(
      makeRow({
        quota: {
          status: 'error',
          error: 'quota refresh failed',
          errorStatus: 503,
          failedAtMs: 2_000,
        },
      })
    );
    expect(transientQuotaRefreshFailure.health).toMatchObject({
      status: 'exception',
      tooltipParams: { detail: 'quota refresh failed' },
    });

    expect(
      buildAccountListItem(
        makeRow({
          statusMessage: 'upstream unavailable',
          updatedAtMs: 2_000,
          raw: { name: 'codex-1.json', type: 'codex', status_code: 503 },
        })
      ).health.status
    ).toBe('available');

    const authFailure = buildAccountListItem(makeRow(), {
      requestEvidence: {
        latestRequest: { timestamp_ms: 2_000, failed: true, fail_status_code: 401 },
      },
    });
    expect(authFailure.health).toMatchObject({
      status: 'reauth',
      reasonKey: 'accounts.health_reason_reauth_auth',
      basisLabelKey: 'accounts.latest_request_time_title',
      observedAtMs: 2_000,
    });

    const transientFailure = buildAccountListItem(makeRow(), {
      requestEvidence: {
        recentRequests: [
          { timestamp_ms: 3_000, failed: true, fail_status_code: 503 },
          { timestamp_ms: 2_000, failed: true, fail_status_code: 502 },
        ],
      },
    });
    expect(transientFailure.health).toMatchObject({
      status: 'exception',
      reasonKey: 'accounts.health_reason_exception_request',
      observedAtMs: 3_000,
    });
  });

  it('does not revive stale reauth after authenticated evidence followed by transient failures', () => {
    for (const authenticatedRequest of [
      { timestamp_ms: 3_000, failed: false },
      { timestamp_ms: 3_000, failed: true, fail_status_code: 429 },
    ]) {
      const item = buildAccountListItem(makeRow(), {
        codexStatus: makeCodexStatus({ needsReauth: true, isHttp401: true }),
        requestEvidence: {
          recentRequests: [
            { timestamp_ms: 5_000, failed: true, fail_status_code: 503 },
            { timestamp_ms: 4_000, failed: true, fail_status_code: 502 },
            authenticatedRequest,
            { timestamp_ms: 2_000, failed: true, fail_status_code: 401 },
          ],
        },
      });

      expect(item.health).toMatchObject({
        status: 'exception',
        reasonKey: 'accounts.health_reason_exception_request',
        observedAtMs: 5_000,
      });
      expect(item.recommendation).toMatchObject({
        hasRecommendation: true,
        item: { action: 'review' },
        reasonKey: 'accounts.recommend_reason_request_failure',
      });
    }
  });

  it('keeps unresolved inspection health when newer request evidence remains negative', () => {
    const row = makeRow({
      inspection: {
        source: 'server',
        action: 'reauth',
        actionReason: 'expired',
        actionStatus: 'pending',
        statusCode: 401,
        usedPercent: null,
        runId: 1,
        resultId: 2,
        createdAtMs: 1_000,
      },
    });

    const item = buildAccountListItem(row, {
      requestEvidence: {
        recentRequests: [
          { timestamp_ms: 3_000, failed: true, fail_status_code: 503 },
          { timestamp_ms: 2_000, failed: true, fail_status_code: 502 },
        ],
      },
    });

    expect(item.health).toMatchObject({
      status: 'reauth',
      reasonKey: 'accounts.health_reason_reauth_inspection',
    });
    expect(item.recommendation).toMatchObject({
      hasRecommendation: true,
      item: { action: 'reauth' },
      reasonKey: 'accounts.recommend_reason_inspection',
    });
  });

  it('summarizes quota refresh 401 as a quota refresh reauth reason', () => {
    const item = buildAccountListItem(
      makeRow({
        quota: {
          status: 'error',
          remainingPercent: null,
          usedPercent: null,
          resetLabel: '-',
          planType: null,
          source: 'cache',
          error:
            '额度获取失败：401 Your authentication token has been invalidated. Please try signing in again.',
        },
      })
    );

    expect(item.health.status).toBe('reauth');
    expect(item.health.reasonKey).toBe('accounts.health_reason_reauth_quota_refresh');
    expect(item.health.reasonParams).toEqual({ code: '401' });
    expect(item.health.tooltipParams.detail).toBe(
      '额度获取失败：401 Your authentication token has been invalidated. Please try signing in again.'
    );
    expect(item.recommendation).toMatchObject({
      actionLabelKey: 'accounts.recommend_action_reauth',
      reasonKey: 'accounts.recommend_reason_quota_auth',
      priority: 'critical',
    });
  });

  it('lets newer healthy quota evidence replace stale reauth inspection advice', () => {
    const item = buildAccountListItem(
      makeRow({
        quota: { status: 'ok', fetchedAtMs: 2_000 },
        inspection: {
          source: 'server',
          action: 'reauth',
          actionReason: 'expired',
          actionStatus: 'pending',
          statusCode: 401,
          usedPercent: null,
          runId: 1,
          resultId: 2,
          createdAtMs: 1_000,
        },
      })
    );

    expect(item.health.status).toBe('available');
    expect(item.recommendation.hasRecommendation).toBe(false);
  });

  it('keeps pre-recovery healthy quota displayable without marking the account available', () => {
    const item = buildAccountListItem(
      makeRow({
        authenticationAtMs: 2_000,
        quota: {
          status: 'ok',
          remainingPercent: 70,
          usedPercent: 30,
          fetchedAtMs: 1_000,
        },
      })
    );

    expect(item.quota).toMatchObject({ statusLabelKey: 'accounts.quota_status_ok' });
    expect(item.health.status).toBe('raw');
  });

  it('does not use pre-recovery healthy inspection as available evidence', () => {
    const item = buildAccountListItem(
      makeRow({
        authenticationAtMs: 2_000,
        quota: {
          status: 'unknown',
          remainingPercent: null,
          usedPercent: null,
          fetchedAtMs: undefined,
          source: 'none',
        },
        inspection: {
          source: 'server',
          action: 'keep',
          actionReason: 'healthy',
          actionStatus: 'success',
          statusCode: 200,
          usedPercent: 20,
          isQuota: false,
          errorKind: 'inference_healthy',
          runId: 1,
          resultId: 3,
          createdAtMs: 1_000,
        },
      })
    );

    expect(item.health.status).toBe('raw');
  });

  it('does not let stale derived Codex status revive a raw 401 after Provider recovery', () => {
    const item = buildAccountListItem(
      makeRow({
        statusMessage: 'unauthorized',
        updatedAtMs: 1_000,
        raw: { name: 'codex-1.json', type: 'codex', status_code: 401 },
        quota: { status: 'ok', fetchedAtMs: 2_000 },
      }),
      { codexStatus: makeCodexStatus({ needsReauth: true, isHttp401: true }) }
    );

    expect(item.health.status).toBe('available');
    expect(item.recommendation.hasRecommendation).toBe(false);
  });

  it('keeps xAI Header authentication status as a compatibility fallback', () => {
    const item = buildAccountListItem(makeRow({ provider: 'xai' }), {
      codexStatus: makeCodexStatus({ isCodex: false, needsReauth: true, isHttp401: true }),
    });

    expect(item.health.status).toBe('reauth');
  });

  it('does not let stale derived xAI status revive superseded row authentication evidence', () => {
    const item = buildAccountListItem(
      makeRow({
        provider: 'xai',
        statusMessage: 'unauthorized',
        updatedAtMs: 1_000,
        raw: { name: 'xai.json', type: 'xai', status_code: 401 },
        quota: { status: 'ok', fetchedAtMs: 2_000 },
      }),
      {
        codexStatus: makeCodexStatus({ isCodex: false, needsReauth: true, isHttp401: true }),
      }
    );

    expect(item.health.status).toBe('available');
  });

  it('shows window cooldown ahead of exhausted and disabled states', () => {
    const row = makeRow({
      disabled: true,
      quota: {
        status: 'exhausted',
        remainingPercent: 0,
        usedPercent: 100,
        resetLabel: 'later',
        planType: null,
        source: 'cache',
      },
    });
    const quotaCooldown: QuotaCooldownInfo = {
      authFileName: row.fileName,
      recoverAtMs: 1700000000000,
    };

    const item = buildAccountListItem(row, {
      quotaCooldown,
      codexStatus: makeCodexStatus({
        isQuotaLimited: true,
        isFiveHourLimited: true,
        fiveHourResetLabel: 'later',
      }),
    });

    expect(item.health.status).toBe('five_hour_cooldown');
    expect(item.health.reasonKey).toBe('accounts.health_reason_cooldown');
    expect(item.health.reasonTone).toBe('warning');
    expect(item.health.cooldown).toBe(quotaCooldown);
  });

  it('classifies quota and account fallback states', () => {
    const weeklyExhaustedItem = buildAccountListItem(
      makeRow({
        quota: {
          status: 'exhausted',
          remainingPercent: 0,
          usedPercent: 100,
          resetLabel: '-',
          planType: null,
          source: 'cache',
        },
      }),
      {
        quotaWindows: [
          {
            key: 'weekly',
            label: 'Weekly quota',
            remainingPercent: 0,
            usedPercent: 100,
            resetLabel: '-',
            modelScope: CODEX_MAIN_SCOPE,
          },
        ],
      }
    );
    expect(weeklyExhaustedItem.health.status).toBe('weekly_exhausted');
    expect(weeklyExhaustedItem.health.reasonKey).toBe('accounts.health_reason_weekly_exhausted');
    expect(weeklyExhaustedItem.health.reasonTone).toBe('warning');

    const explicitMonthlyItem = buildAccountListItem(
      makeRow({
        quota: {
          status: 'exhausted',
          remainingPercent: 0,
          usedPercent: 100,
          resetLabel: '-',
          planType: null,
          source: 'cache',
        },
      }),
      {
        quotaWindows: [
          {
            key: 'opaque-window',
            label: 'Allowance',
            kind: 'monthly',
            remainingPercent: 0,
            usedPercent: 100,
            resetLabel: 'month-end',
            modelScope: CODEX_MAIN_SCOPE,
          },
        ],
      }
    );
    expect(explicitMonthlyItem.health.status).toBe('monthly_exhausted');

    const dailyExhaustedItem = buildAccountListItem(
      makeRow({
        quota: {
          status: 'exhausted',
          remainingPercent: 0,
          usedPercent: 100,
          resetLabel: '-',
          planType: null,
          source: 'cache',
        },
      }),
      {
        quotaWindows: [
          {
            key: 'daily',
            label: 'Daily limit',
            kind: 'daily',
            remainingPercent: 0,
            usedPercent: 100,
            resetLabel: 'tomorrow',
          },
        ],
      }
    );
    expect(dailyExhaustedItem.health.status).toBe('limited');

    const xaiPaygAvailableItem = buildAccountListItem(
      makeRow({
        provider: 'xai',
        quota: {
          status: 'low',
          remainingPercent: 16.667,
          usedPercent: 83.333,
          resetLabel: 'month-end',
          planType: null,
          source: 'cache',
        },
      }),
      {
        quotaWindows: [
          {
            key: 'billing',
            label: 'Monthly credits',
            kind: 'billing',
            remainingPercent: 0,
            usedPercent: 100,
            resetLabel: 'month-end',
          },
          {
            key: 'pay-as-you-go',
            label: 'Pay-as-you-go',
            kind: 'payg',
            remainingPercent: 50,
            usedPercent: 50,
            resetLabel: 'month-end',
          },
        ],
      }
    );
    expect(xaiPaygAvailableItem.health.status).toBe('available');

    const lowQuotaItem = buildAccountListItem(
      makeRow({
        quota: {
          status: 'low',
          remainingPercent: 12,
          usedPercent: 88,
          resetLabel: '-',
          planType: null,
          source: 'cache',
        },
      })
    );
    expect(lowQuotaItem.health.status).toBe('available');
    expect(lowQuotaItem.health.reasonKey).toBe('accounts.health_reason_available');
    expect(lowQuotaItem.health.reasonTone).toBe('muted');

    const unqualifiedRuntimeItem = buildAccountListItem(
      makeRow({ statusMessage: 'custom problem' })
    );
    expect(unqualifiedRuntimeItem.health.status).toBe('available');
    expect(unqualifiedRuntimeItem.health.reasonKey).toBe('accounts.health_reason_available');
    expect(unqualifiedRuntimeItem.health.reasonTone).toBe('muted');

    const disabledItem = buildAccountListItem(
      makeRow({
        disabled: true,
        quota: {
          status: 'disabled',
          remainingPercent: null,
          usedPercent: null,
          resetLabel: '-',
          planType: null,
          source: 'none',
        },
      })
    );
    expect(disabledItem.health.status).toBe('disabled');
    expect(disabledItem.health.reasonKey).toBe('accounts.health_reason_disabled');
    expect(disabledItem.health.reasonTone).toBe('muted');

    expect(buildAccountListItem(makeRow()).health.status).toBe('available');
    expect(
      buildAccountListItem(
        makeRow({
          quota: {
            status: 'exhausted',
            remainingPercent: 0,
            usedPercent: 100,
            resetLabel: '-',
            planType: null,
            source: 'cache',
          },
        })
      ).health.status
    ).toBe('limited');
    const rawItem = buildAccountListItem(
      makeRow({
        quota: {
          status: 'unknown',
          remainingPercent: null,
          usedPercent: null,
          resetLabel: '-',
          planType: null,
          source: 'none',
        },
      })
    );
    expect(rawItem.health.status).toBe('raw');
    expect(rawItem.health.reasonKey).toBe('accounts.health_reason_raw');
    expect(rawItem.health.reasonTone).toBe('muted');
  });

  it('uses the latest known recovery for multiple exhausted windows of the same kind', () => {
    const earlierResetAtMs = Date.parse('2026-07-30T04:00:00Z');
    const laterResetAtMs = Date.parse('2026-07-30T06:00:00Z');
    const item = buildAccountListItem(
      makeRow({
        quota: {
          status: 'exhausted',
          remainingPercent: 0,
          usedPercent: 100,
          resetLabel: '2026-07-30T04:00:00Z',
          resetAtMs: earlierResetAtMs,
          resetAccuracy: 'exact',
        },
      }),
      {
        quotaWindows: [
          {
            key: 'weekly-base',
            label: 'Weekly base',
            kind: 'weekly',
            remainingPercent: 0,
            usedPercent: 100,
            resetLabel: '2026-07-30T04:00:00Z',
            resetAtMs: earlierResetAtMs,
            resetAccuracy: 'exact',
            modelScope: CODEX_MAIN_SCOPE,
          },
          {
            key: 'weekly-model',
            label: 'Weekly model',
            kind: 'weekly',
            remainingPercent: 0,
            usedPercent: 100,
            resetLabel: '2026-07-30T06:00:00Z',
            resetAtMs: laterResetAtMs,
            resetAccuracy: 'exact',
            modelScope: CODEX_MAIN_SCOPE,
          },
        ],
      }
    );

    expect(item.health.status).toBe('weekly_exhausted');
    expect(item.health.tooltipParams).toEqual({ resetAt: '2026-07-30T06:00:00Z' });
    expect(item.health.resetAtMs).toBe(laterResetAtMs);
  });

  it('does not promise recovery when one matching exhausted window has no reset time', () => {
    const item = buildAccountListItem(
      makeRow({
        quota: {
          status: 'exhausted',
          remainingPercent: 0,
          usedPercent: 100,
          resetLabel: '2026-07-30T04:00:00Z',
          resetAtMs: Date.parse('2026-07-30T04:00:00Z'),
          resetAccuracy: 'exact',
        },
      }),
      {
        quotaWindows: [
          {
            key: 'weekly-known',
            label: 'Weekly known',
            kind: 'weekly',
            remainingPercent: 0,
            usedPercent: 100,
            resetLabel: '2026-07-30T04:00:00Z',
            resetAtMs: Date.parse('2026-07-30T04:00:00Z'),
            resetAccuracy: 'exact',
            modelScope: CODEX_MAIN_SCOPE,
          },
          {
            key: 'weekly-unknown',
            label: 'Weekly unknown',
            kind: 'weekly',
            remainingPercent: 0,
            usedPercent: 100,
            resetLabel: '-',
            resetAtMs: null,
            resetAccuracy: 'unknown',
            modelScope: CODEX_MAIN_SCOPE,
          },
        ],
      }
    );

    expect(item.health.status).toBe('weekly_exhausted');
    expect(item.health.tooltipParams).toEqual({ resetAt: '-' });
    expect(item.health.resetAtMs).toBeNull();
  });

  it('marks mixed Antigravity model groups as partially available', () => {
    const item = buildAccountListItem(
      makeRow({
        provider: 'antigravity',
        statusMessage: 'Gemini 5-hour pool exhausted; waiting for Antigravity reset',
        quota: {
          status: 'ok',
          remainingPercent: 66,
          usedPercent: 34,
          resetLabel: 'later',
          planType: 'pro',
          source: 'cache',
        },
      }),
      {
        quotaWindows: [
          {
            key: 'gemini:five-hour',
            label: 'Five hour',
            kind: 'five_hour',
            remainingPercent: 0,
            usedPercent: 100,
            resetLabel: 'later',
            groupLabel: 'Gemini models',
          },
          {
            key: 'claude:five-hour',
            label: 'Five hour',
            kind: 'five_hour',
            remainingPercent: 82,
            usedPercent: 18,
            resetLabel: 'later',
            groupLabel: 'Claude and GPT models',
          },
          {
            key: 'claude:weekly',
            label: 'Weekly',
            kind: 'weekly',
            remainingPercent: 66,
            usedPercent: 34,
            resetLabel: 'later',
            groupLabel: 'Claude and GPT models',
          },
        ],
      }
    );

    expect(item.health.status).toBe('partial');
    expect(item.health.labelKey).toBe('accounts.health_partial');
    expect(item.health.reasonKey).toBe('accounts.health_reason_partial');
    expect(item.health.reasonParams).toEqual({ available: 1, total: 2 });
    expect(item.health.tooltipParams).toEqual({
      available: 1,
      total: 2,
      limited: 'Gemini models',
    });
  });

  it('keeps the exhausted state when every Antigravity model group is blocked', () => {
    const geminiFiveHourResetAtMs = Date.parse('2026-07-30T04:00:00Z');
    const geminiWeeklyResetAtMs = Date.parse('2026-08-02T08:00:00Z');
    const claudeFiveHourResetAtMs = Date.parse('2026-07-30T06:00:00Z');
    const item = buildAccountListItem(
      makeRow({
        provider: 'antigravity',
        quota: {
          status: 'exhausted',
          remainingPercent: 0,
          usedPercent: 100,
          resetLabel: '2026-07-30T06:00:00Z',
          resetAtMs: claudeFiveHourResetAtMs,
          resetAccuracy: 'exact',
          planType: 'pro',
          source: 'cache',
        },
      }),
      {
        quotaWindows: [
          {
            key: 'gemini:five-hour',
            label: 'Five hour',
            kind: 'five_hour',
            remainingPercent: 0,
            usedPercent: 100,
            resetLabel: '2026-07-30T04:00:00Z',
            resetAtMs: geminiFiveHourResetAtMs,
            resetAccuracy: 'exact',
            groupLabel: 'Gemini models',
          },
          {
            key: 'gemini:weekly',
            label: 'Weekly',
            kind: 'weekly',
            remainingPercent: 0,
            usedPercent: 100,
            resetLabel: '2026-08-02T08:00:00Z',
            resetAtMs: geminiWeeklyResetAtMs,
            resetAccuracy: 'exact',
            groupLabel: 'Gemini models',
          },
          {
            key: 'claude:five-hour',
            label: 'Five hour',
            kind: 'five_hour',
            remainingPercent: 0,
            usedPercent: 100,
            resetLabel: '2026-07-30T06:00:00Z',
            resetAtMs: claudeFiveHourResetAtMs,
            resetAccuracy: 'exact',
            groupLabel: 'Claude and GPT models',
          },
        ],
      }
    );

    expect(item.health.status).toBe('five_hour_exhausted');
    expect(item.health.tooltipParams).toEqual({ resetAt: '2026-07-30T06:00:00Z' });
    expect(item.health.resetAtMs).toBe(claudeFiveHourResetAtMs);
  });

  it('does not promise a group recovery while one blocking bucket has no reset time', () => {
    const summary = summarizeGroupedQuotaAvailability([
      {
        groupLabel: 'Gemini models',
        kind: 'five_hour',
        remainingPercent: 0,
        resetLabel: '2026-07-30T04:00:00Z',
        resetAtMs: Date.parse('2026-07-30T04:00:00Z'),
        resetAccuracy: 'exact',
      },
      {
        groupLabel: 'Gemini models',
        kind: 'weekly',
        remainingPercent: 0,
        resetLabel: '-',
        resetAtMs: null,
        resetAccuracy: 'unknown',
      },
    ]);

    expect(summary?.groups[0]).toMatchObject({
      resetLabel: '-',
      resetAtMs: null,
      resetAccuracy: 'unknown',
      resetKind: 'weekly',
    });
  });

  it('does not substitute an available-group reset for an unknown limited-group recovery', () => {
    const summary = summarizeGroupedQuotaAvailability([
      {
        groupLabel: 'Gemini models',
        kind: 'weekly',
        remainingPercent: 0,
        resetLabel: '-',
        resetAtMs: null,
        resetAccuracy: 'unknown',
      },
      {
        groupLabel: 'Claude and GPT models',
        kind: 'five_hour',
        remainingPercent: 60,
        resetLabel: '2026-07-30T05:00:00Z',
        resetAtMs: Date.parse('2026-07-30T05:00:00Z'),
        resetAccuracy: 'exact',
      },
    ]);

    expect(summary).toMatchObject({
      state: 'partial',
      resetLabel: '-',
      resetAtMs: null,
      resetAccuracy: 'unknown',
    });
  });

  it('degrades grouped recovery accuracy when any blocking reset is estimated', () => {
    const laterExactResetAtMs = Date.parse('2026-07-30T06:00:00Z');
    const summary = summarizeGroupedQuotaAvailability([
      {
        groupLabel: 'Gemini models',
        kind: 'five_hour',
        remainingPercent: 0,
        resetLabel: '2026-07-30T05:00:00Z',
        resetAtMs: Date.parse('2026-07-30T05:00:00Z'),
        resetAccuracy: 'estimated',
      },
      {
        groupLabel: 'Gemini models',
        kind: 'weekly',
        remainingPercent: 0,
        resetLabel: '2026-07-30T06:00:00Z',
        resetAtMs: laterExactResetAtMs,
        resetAccuracy: 'exact',
      },
    ]);

    expect(summary?.groups[0]).toMatchObject({
      resetAtMs: laterExactResetAtMs,
      resetAccuracy: 'estimated',
      resetKind: 'weekly',
    });
  });

  it('builds identity and activity summaries for list rendering', () => {
    const item = buildAccountListItem(
      makeRow({
        fileName: 'shared-codex.json',
        authIndex: 'auth-2',
        projectId: 'project-a',
        priority: -5,
        usage: {
          success: 3,
          failure: 1,
          successRate: 75,
          recentRequests: [],
        },
      })
    );

    expect(item.identity.subtitle).toBe('shared-codex.json · #auth-2 · project-a');
    expect(item.identity.priority).toBe(-5);
    expect(item.identity.priorityIsNegative).toBe(true);
    expect(item.activity.recentTotal).toBe(4);
    expect(item.activity.successCount).toBe(3);
    expect(item.activity.failureCount).toBe(1);
    expect(item.activity.successRate).toBe(75);
    expect(item.activity.hasHealthData).toBe(true);
    expect(item.activity.estimatedValue).toBeCloseTo(0.072);
  });

  it('uses monitoring activity when provided for list summaries', () => {
    const item = buildAccountListItem(
      makeRow({
        usage: {
          success: 1,
          failure: 0,
          successRate: 100,
          recentRequests: [],
        },
      }),
      {
        activity: {
          requests: 31,
          successRate: 96.8,
          inputTokens: 1200,
          outputTokens: 300,
          estimatedCost: 0.42,
          lastSeenMs: 1700000000000,
          source: 'monitoring',
        },
      }
    );

    expect(item.activity.recentTotal).toBe(31);
    expect(item.activity.successCount).toBe(30);
    expect(item.activity.failureCount).toBe(1);
    expect(item.activity.successRate).toBe(96.8);
    expect(item.activity.totalTokens).toBe(1500);
    expect(item.activity.estimatedValue).toBe(0.42);
    expect(item.activity.source).toBe('monitoring');
    expect(item.activity.hasHealthData).toBe(true);
  });

  it('maps recommendations by auth-file selection key', () => {
    const first = makeRow({ fileName: 'shared.json', authIndex: 'auth-1' });
    const second = makeRow({ fileName: 'shared.json', authIndex: 'auth-2' });
    const secondRecommendation = makeRecommendation(second, { action: 'disable' });

    const map = buildRecommendationBySelectionKey([
      makeRecommendation(first),
      secondRecommendation,
    ]);

    expect(map.get(first.selectionKey)?.row.authIndex).toBe('auth-1');
    expect(map.get(second.selectionKey)).toBe(secondRecommendation);
  });

  it('does not show a handled inspection 401 as requiring re-authentication', () => {
    const item = buildAccountListItem(
      makeRow({
        inspection: {
          source: 'server',
          action: 'reauth',
          actionReason: 're-authentication completed',
          actionStatus: 'success',
          executedAction: 'reauth',
          statusCode: 401,
          usedPercent: null,
          runId: 1,
          resultId: 2,
          createdAtMs: 3,
        },
      })
    );

    expect(item.health.status).toBe('available');
    expect(item.health.labelKey).toBe('accounts.health_available');
    expect(item.health.reasonKey).toBe('accounts.health_reason_available');
  });
});
