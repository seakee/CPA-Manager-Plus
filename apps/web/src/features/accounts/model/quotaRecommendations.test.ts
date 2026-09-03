import { describe, expect, it } from 'vitest';
import type { AuthFileItem } from '@/types';
import type { AccountRow } from './accountRows';
import {
  buildAccountRecommendation,
  buildAccountRecommendations,
  getRecommendationRank,
} from './quotaRecommendations';

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
    selectionKey: `${raw.name}\u0000-`,
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

describe('quotaRecommendations', () => {
  it('disables active exhausted accounts with critical priority', () => {
    const recommendation = buildAccountRecommendation(
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
    );

    expect(recommendation?.action).toBe('disable');
    expect(recommendation?.priority).toBe('critical');
    expect(recommendation?.reasonKey).toBe('accounts.recommend_reason_exhausted');
  });

  it('refreshes low quota accounts with high priority', () => {
    const recommendation = buildAccountRecommendation(
      makeRow({
        quota: {
          status: 'low',
          remainingPercent: 12,
          usedPercent: 88,
          resetLabel: 'Mon',
          planType: 'plus',
          source: 'cache',
        },
      })
    );

    expect(recommendation?.action).toBe('refresh');
    expect(recommendation?.priority).toBe('high');
    expect(recommendation?.reasonKey).toBe('accounts.recommend_reason_low');
  });

  it('refreshes accounts whose last quota is preserved after a failed refresh', () => {
    const recommendation = buildAccountRecommendation(
      makeRow({
        quota: {
          status: 'ok',
          remainingPercent: 80,
          usedPercent: 20,
          resetLabel: 'Mon',
          planType: 'plus',
          source: 'cache',
          error: 'temporary failure',
        },
      })
    );

    expect(recommendation?.action).toBe('refresh');
    expect(recommendation?.priority).toBe('medium');
    expect(recommendation?.reasonKey).toBe('accounts.recommend_reason_error');
  });

  it('reauthenticates accounts when quota refresh returns a current HTTP 401', () => {
    const recommendation = buildAccountRecommendation(
      makeRow({
        quota: {
          status: 'error',
          error: 'quota refresh failed: HTTP 401',
          failedAtMs: 2_000,
        },
      })
    );

    expect(recommendation).toMatchObject({
      action: 'reauth',
      priority: 'critical',
      reasonKey: 'accounts.recommend_reason_quota_auth',
    });
  });

  it('does not recommend refresh for neutral or superseded quota refresh failures', () => {
    expect(
      buildAccountRecommendation(
        makeRow({
          quota: {
            status: 'error',
            error: 'context canceled',
            errorStatus: 499,
            failedAtMs: 2_000,
          },
        })
      )
    ).toBeNull();

    expect(
      buildAccountRecommendation(
        makeRow({
          quota: {
            status: 'error',
            error: 'upstream unavailable',
            errorStatus: 503,
            failedAtMs: 1_000,
          },
        }),
        { latestRequest: { timestamp_ms: 2_000, failed: false } }
      )
    ).toBeNull();
  });

  it('reauthenticates current credential and Header authentication failures', () => {
    for (const row of [
      makeRow({
        raw: { name: 'codex-1.json', type: 'codex', status_code: 401 },
        updatedAtMs: 2_000,
      }),
      makeRow({
        quota: {
          status: 'ok',
          observedAtMs: 2_000,
          observedErrorKind: 'auth',
          observedErrorCode: 'invalid_api_key',
        },
      }),
    ]) {
      expect(buildAccountRecommendation(row)).toMatchObject({
        action: 'reauth',
        priority: 'critical',
        reasonKey: 'accounts.recommend_reason_credential_auth',
      });
    }
  });

  it('enables disabled accounts after quota recovery', () => {
    const recommendation = buildAccountRecommendation(
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
      }),
      { latestRequest: { timestamp_ms: 2_000, failed: false } }
    );

    expect(recommendation?.action).toBe('enable');
    expect(recommendation?.priority).toBe('medium');
    expect(recommendation?.reasonKey).toBe('accounts.recommend_reason_recovered');
  });

  it('classifies a healthy inspection enable action as recovered', () => {
    const recommendation = buildAccountRecommendation(
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
        inspection: {
          source: 'server',
          action: 'enable',
          actionReason: 'recovered',
          actionStatus: 'pending',
          statusCode: 200,
          usedPercent: 20,
          isQuota: false,
          runId: 1,
          resultId: 2,
          createdAtMs: 2_000,
        },
      })
    );

    expect(recommendation).toMatchObject({
      action: 'enable',
      priority: 'medium',
      reasonKey: 'accounts.recommend_reason_recovered',
    });
  });

  it('restores negative priority to default when the account is otherwise healthy', () => {
    const recommendation = buildAccountRecommendation(makeRow({ priority: -5 }));

    expect(recommendation?.action).toBe('restore-default');
    expect(recommendation?.priority).toBe('low');
    expect(recommendation?.reasonKey).toBe('accounts.recommend_reason_priority');
  });

  it('lets inspection advice override quota state', () => {
    const recommendation = buildAccountRecommendation(
      makeRow({
        quota: {
          status: 'ok',
          remainingPercent: 90,
          usedPercent: 10,
          resetLabel: '-',
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
          createdAtMs: 1000,
        },
      })
    );

    expect(recommendation?.action).toBe('reauth');
    expect(recommendation?.priority).toBe('critical');
    expect(recommendation?.reasonKey).toBe('accounts.recommend_reason_inspection');
  });

  it('uses newer inspection authentication evidence over an older quota refresh failure', () => {
    const recommendation = buildAccountRecommendation(
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

    expect(recommendation).toMatchObject({
      action: 'reauth',
      priority: 'critical',
      reasonKey: 'accounts.recommend_reason_inspection',
    });
  });

  it('drops handled or superseded inspection advice', () => {
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

    expect(
      buildAccountRecommendation(row, {
        latestRequest: { timestamp_ms: 2_000, failed: false },
      })
    ).toBeNull();
    expect(
      buildAccountRecommendation({
        ...row,
        inspection: { ...row.inspection!, actionStatus: 'success' },
      })
    ).toBeNull();
  });

  it('keeps unresolved inspection advice when newer request evidence is still negative', () => {
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

    expect(
      buildAccountRecommendation(row, {
        recentRequests: [
          { timestamp_ms: 3_000, failed: true, fail_status_code: 503 },
          { timestamp_ms: 2_000, failed: true, fail_status_code: 502 },
        ],
      })
    ).toMatchObject({
      action: 'reauth',
      priority: 'critical',
      reasonKey: 'accounts.recommend_reason_inspection',
    });
  });

  it('creates request-backed recommendations only for qualified failures', () => {
    const row = makeRow();

    expect(
      buildAccountRecommendation(row, {
        latestRequest: { timestamp_ms: 2_000, failed: true, fail_status_code: 499 },
      })
    ).toBeNull();
    expect(
      buildAccountRecommendation(row, {
        latestRequest: { timestamp_ms: 2_000, failed: true, fail_status_code: 401 },
      })
    ).toMatchObject({
      action: 'reauth',
      priority: 'critical',
      reasonKey: 'accounts.recommend_reason_request_auth',
    });
    expect(
      buildAccountRecommendation(row, {
        recentRequests: [
          { timestamp_ms: 3_000, failed: true, fail_status_code: 503 },
          { timestamp_ms: 2_000, failed: true, fail_status_code: 502 },
        ],
      })
    ).toMatchObject({
      action: 'review',
      priority: 'medium',
      reasonKey: 'accounts.recommend_reason_request_failure',
    });
  });

  it('keeps authentication evidence separate from a newer transient health failure', () => {
    for (const authenticatedRequest of [
      { timestamp_ms: 3_000, failed: false },
      { timestamp_ms: 3_000, failed: true, fail_status_code: 429 },
    ]) {
      expect(
        buildAccountRecommendation(makeRow(), {
          recentRequests: [
            { timestamp_ms: 5_000, failed: true, fail_status_code: 503 },
            { timestamp_ms: 4_000, failed: true, fail_status_code: 502 },
            authenticatedRequest,
            { timestamp_ms: 2_000, failed: true, fail_status_code: 401 },
          ],
        })
      ).toMatchObject({
        action: 'review',
        priority: 'medium',
        reasonKey: 'accounts.recommend_reason_request_failure',
      });
    }

    expect(
      buildAccountRecommendation(makeRow(), {
        recentRequests: [
          { timestamp_ms: 5_000, failed: true, fail_status_code: 503 },
          { timestamp_ms: 4_000, failed: true, fail_status_code: 502 },
          { timestamp_ms: 2_000, failed: true, fail_status_code: 401 },
        ],
      })
    ).toMatchObject({
      action: 'reauth',
      priority: 'critical',
      reasonKey: 'accounts.recommend_reason_request_auth',
    });
  });

  it('clears request quota risk after a later success', () => {
    const recommendation = buildAccountRecommendation(makeRow(), {
      recentRequests: [
        { timestamp_ms: 3_000, failed: false },
        { timestamp_ms: 2_000, failed: true, fail_status_code: 429 },
      ],
    });

    expect(recommendation).toBeNull();
  });

  it('clears an older runtime 429 recommendation after a later success', () => {
    const recommendation = buildAccountRecommendation(
      makeRow({
        statusMessage: 'quota exceeded',
        updatedAtMs: 1_000,
        raw: { name: 'codex-1.json', type: 'codex', status_code: 429 },
      }),
      {
        latestRequest: { timestamp_ms: 2_000, failed: false },
      }
    );

    expect(recommendation).toBeNull();
  });

  it('prioritizes newer quota risk over older transient failures', () => {
    const recommendation = buildAccountRecommendation(makeRow(), {
      recentRequests: [
        { timestamp_ms: 4_000, failed: true, fail_status_code: 429 },
        { timestamp_ms: 3_000, failed: true, fail_status_code: 503 },
        { timestamp_ms: 2_000, failed: true, fail_status_code: 502 },
      ],
    });

    expect(recommendation).toMatchObject({
      action: 'refresh',
      priority: 'high',
      reasonKey: 'accounts.recommend_reason_quota_limited',
    });
  });

  it('sorts recommendations by priority rank and then account name', () => {
    const rows = [
      makeRow({
        fileName: 'z-low.json',
        quota: {
          status: 'low',
          remainingPercent: 10,
          usedPercent: 90,
          resetLabel: '-',
          planType: null,
          source: 'cache',
        },
      }),
      makeRow({
        fileName: 'a-exhausted.json',
        quota: {
          status: 'exhausted',
          remainingPercent: 0,
          usedPercent: 100,
          resetLabel: '-',
          planType: null,
          source: 'cache',
        },
      }),
      makeRow({
        fileName: 'b-low.json',
        quota: {
          status: 'low',
          remainingPercent: 15,
          usedPercent: 85,
          resetLabel: '-',
          planType: null,
          source: 'cache',
        },
      }),
    ];

    expect(buildAccountRecommendations(rows).map((item) => item.row.fileName)).toEqual([
      'a-exhausted.json',
      'b-low.json',
      'z-low.json',
    ]);
    expect(getRecommendationRank('critical')).toBeGreaterThan(getRecommendationRank('high'));
  });
});
