import { describe, expect, it } from 'vitest';
import type { AuthFileItem } from '@/types';
import type { MonitoringAccountLatestRequest } from '@/services/api';
import type { AccountRow } from './accountRows';
import {
  classifyAccountCredentialStatusEvidence,
  classifyAccountQuotaRefreshEvidence,
  classifyAccountRequestEvidence,
  getAccountRequestCredentialEvidence,
  hasAccountQuotaLimitEvidence,
  isAccountCredentialStatusProblemCurrent,
  isAccountInspectionAuthenticationFailure,
  isAccountInspectionActionable,
  isAccountInspectionHealthyEvidence,
  isAccountInspectionStatusEvidenceCurrent,
  isAccountObservedDiagnosticProblemCurrent,
  isAccountQuotaRefreshProblemCurrent,
  isAccountRequestCredentialEvidenceCurrent,
  isAccountRequestHealthEvidenceCurrent,
  isAccountRequestQuotaEvidenceCurrent,
  mergeAccountRequestEvidenceInputs,
  resolveAccountAuthenticationProblemEvidence,
  resolveAccountExceptionProblemEvidence,
  resolveAccountRequestHealthEvidence,
  resolveAccountRequestQuotaEvidence,
} from './accountHealthEvidence';

const makeRequest = (
  overrides: Partial<MonitoringAccountLatestRequest> = {}
): MonitoringAccountLatestRequest => ({
  timestamp_ms: 2_000,
  failed: false,
  ...overrides,
});

const makeRow = (overrides: Partial<AccountRow> = {}): AccountRow => {
  const raw: AuthFileItem = { name: 'codex.json', type: 'codex' };
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
    },
    usage: { success: 0, failure: 0, successRate: null, recentRequests: [] },
    inspection: null,
    raw,
    ...overrides,
  };
};

describe('accountHealthEvidence', () => {
  it('keeps cancellation and request-shape failures neutral', () => {
    expect(
      classifyAccountRequestEvidence(
        makeRequest({ failed: true, fail_status_code: 499, fail_summary: 'context canceled' })
      )
    ).toBe('neutral');
    expect(
      classifyAccountRequestEvidence(makeRequest({ failed: true, fail_status_code: 422 }))
    ).toBe('neutral');
    expect(
      classifyAccountRequestEvidence(
        makeRequest({ failed: true, fail_summary: 'unexpected EOF from client' })
      )
    ).toBe('neutral');
    expect(
      classifyAccountRequestEvidence(
        makeRequest({ failed: true, fail_summary: 'client closed request' })
      )
    ).toBe('neutral');
    expect(
      classifyAccountRequestEvidence(
        makeRequest({ failed: true, header_error_code: 'invalid_request_error' })
      )
    ).toBe('neutral');
  });

  it('recognizes status-less credential and quota error markers', () => {
    for (const marker of ['invalid_api_key', 'invalid_token', 'authentication_error']) {
      expect(
        classifyAccountRequestEvidence(makeRequest({ failed: true, header_error_code: marker }))
      ).toBe('credential_failure');
    }
    for (const marker of [
      'quota_exceeded',
      'credits_depleted',
      'free_usage_exhausted',
      'rate_limit_reached',
      'usage_limit_reached',
    ]) {
      expect(
        classifyAccountRequestEvidence(makeRequest({ failed: true, header_error_code: marker }))
      ).toBe('quota');
    }
  });

  it('keeps explicit quota status codes authoritative over conflicting error text', () => {
    for (const statusCode of [402, 429]) {
      expect(
        classifyAccountRequestEvidence(
          makeRequest({
            failed: true,
            fail_status_code: statusCode,
            fail_summary: 'invalid_token',
          })
        )
      ).toBe('quota');
    }
  });

  it('does not treat operational quota failures as quota exhaustion', () => {
    for (const marker of ['quota service unavailable', 'quota refresh failed']) {
      expect(
        classifyAccountRequestEvidence(
          makeRequest({ failed: true, fail_summary: marker, header_error_code: marker })
        )
      ).toBe('transient_failure');
    }
  });

  it('keeps timeout and ambiguous transport failures in the transient failure path', () => {
    for (const marker of [
      'context deadline exceeded',
      'connection reset by peer',
      'unexpected EOF from upstream',
      'request error: upstream unavailable',
      'websocket: close 1006 abnormal closure',
    ]) {
      expect(classifyAccountRequestEvidence(makeRequest({ failed: true, fail_summary: marker }))).toBe(
        'transient_failure'
      );
    }
  });

  it('does not treat an unavailable authentication service as invalid credentials', () => {
    expect(
      classifyAccountRequestEvidence(
        makeRequest({ failed: true, fail_summary: 'authentication unavailable' })
      )
    ).toBe('transient_failure');
  });

  it('uses the latest qualified evidence while ignoring a newer 499', () => {
    const evidence = resolveAccountRequestHealthEvidence({
      recentRequests: [
        makeRequest({ timestamp_ms: 3_000, failed: true, fail_status_code: 499 }),
        makeRequest({ timestamp_ms: 2_000, failed: false }),
        makeRequest({ timestamp_ms: 1_000, failed: true, fail_status_code: 401 }),
      ],
    });

    expect(evidence).toMatchObject({
      kind: 'success',
      direction: 'positive',
      request: { timestamp_ms: 2_000 },
    });
  });

  it('lets a newer success replace older request quota evidence', () => {
    const input = {
      recentRequests: [
        makeRequest({ timestamp_ms: 3_000, failed: false }),
        makeRequest({ timestamp_ms: 2_000, failed: true, fail_status_code: 429 }),
      ],
    };
    const evidence = resolveAccountRequestQuotaEvidence(input);

    expect(evidence).toBeNull();
    expect(hasAccountQuotaLimitEvidence(makeRow(), input)).toBe(false);
  });

  it('lets a newer qualified credential failure take precedence over older request quota evidence', () => {
    const input = {
      recentRequests: [
        makeRequest({ timestamp_ms: 3_000, failed: true, fail_status_code: 401 }),
        makeRequest({ timestamp_ms: 2_000, failed: true, fail_status_code: 429 }),
      ],
    };

    expect(resolveAccountRequestQuotaEvidence(input)).toBeNull();
    expect(resolveAccountRequestHealthEvidence(input)).toMatchObject({
      kind: 'credential_failure',
      request: { timestamp_ms: 3_000 },
    });
  });

  it('keeps the latest request quota until newer confirmed quota data replaces it', () => {
    const input = {
      recentRequests: [
        makeRequest({ timestamp_ms: 3_000, failed: true, fail_status_code: 429 }),
        makeRequest({ timestamp_ms: 2_000, failed: false }),
      ],
    };
    const evidence = resolveAccountRequestQuotaEvidence(input);

    expect(evidence).toMatchObject({ kind: 'quota', request: { timestamp_ms: 3_000 } });
    expect(isAccountRequestQuotaEvidenceCurrent(makeRow(), evidence)).toBe(true);
    expect(hasAccountQuotaLimitEvidence(makeRow(), input)).toBe(true);

    const refreshedRow = makeRow({
      quota: { ...makeRow().quota, status: 'ok', fetchedAtMs: 4_000 },
    });
    expect(isAccountRequestQuotaEvidenceCurrent(refreshedRow, evidence)).toBe(false);
    expect(hasAccountQuotaLimitEvidence(refreshedRow, input)).toBe(false);
  });

  it('requires two consecutive transient failures but treats an auth failure as immediate', () => {
    expect(
      resolveAccountRequestHealthEvidence({
        recentRequests: [
          makeRequest({ timestamp_ms: 3_000, failed: true, fail_status_code: 503 }),
          makeRequest({ timestamp_ms: 2_000, failed: false }),
        ],
      })
    ).toMatchObject({ kind: 'success', request: { timestamp_ms: 2_000 } });

    expect(
      resolveAccountRequestHealthEvidence({
        recentRequests: [
          makeRequest({ timestamp_ms: 3_000, failed: true, fail_status_code: 503 }),
          makeRequest({ timestamp_ms: 2_500, failed: true, fail_status_code: 502 }),
          makeRequest({ timestamp_ms: 2_000, failed: false }),
        ],
      })
    ).toMatchObject({
      kind: 'transient_failure',
      consecutiveTransientFailures: 2,
      request: { timestamp_ms: 3_000 },
    });

    expect(
      resolveAccountRequestHealthEvidence({
        latestRequest: makeRequest({ failed: true, fail_status_code: 401 }),
      })
    ).toMatchObject({ kind: 'credential_failure', direction: 'negative' });
  });

  it('uses a quota event as the latest credential-health boundary', () => {
    expect(
      resolveAccountRequestHealthEvidence({
        recentRequests: [
          makeRequest({ timestamp_ms: 4_000, failed: true, fail_status_code: 503 }),
          makeRequest({ timestamp_ms: 3_000, failed: true, fail_status_code: 429 }),
          makeRequest({ timestamp_ms: 2_000, failed: true, fail_status_code: 502 }),
          makeRequest({ timestamp_ms: 1_000, failed: false }),
        ],
      })
    ).toMatchObject({ kind: 'quota', direction: 'positive', request: { timestamp_ms: 3_000 } });

    expect(
      resolveAccountRequestHealthEvidence({
        recentRequests: [
          makeRequest({ timestamp_ms: 3_000, failed: true, fail_status_code: 503 }),
          makeRequest({ timestamp_ms: 2_000, failed: true, fail_status_code: 429 }),
          makeRequest({ timestamp_ms: 1_000, failed: true, fail_status_code: 401 }),
        ],
      })
    ).toMatchObject({ kind: 'quota', direction: 'positive', request: { timestamp_ms: 2_000 } });
  });

  it('keeps authentication validity independent from newer consecutive transient failures', () => {
    for (const authenticatedRequest of [
      makeRequest({ timestamp_ms: 3_000, failed: false }),
      makeRequest({ timestamp_ms: 3_000, failed: true, fail_status_code: 429 }),
    ]) {
      const evidence = resolveAccountRequestHealthEvidence({
        recentRequests: [
          makeRequest({ timestamp_ms: 5_000, failed: true, fail_status_code: 503 }),
          makeRequest({ timestamp_ms: 4_000, failed: true, fail_status_code: 502 }),
          authenticatedRequest,
          makeRequest({ timestamp_ms: 2_000, failed: true, fail_status_code: 401 }),
        ],
      });

      expect(evidence).toMatchObject({
        kind: 'transient_failure',
        direction: 'negative',
        request: { timestamp_ms: 5_000 },
      });
      expect(getAccountRequestCredentialEvidence(evidence)).toMatchObject({
        direction: 'positive',
        request: { timestamp_ms: 3_000 },
      });
      expect(isAccountRequestHealthEvidenceCurrent(makeRow(), evidence)).toBe(true);
      expect(isAccountRequestCredentialEvidenceCurrent(makeRow(), evidence)).toBe(true);
    }
  });

  it('does not revive a raw HTTP 401 already superseded by the row boundary', () => {
    const row = makeRow({
      raw: { name: 'codex.json', type: 'codex', status_code: 401 },
      rawCredentialStatusSuperseded: true,
      statusMessage: '',
    });

    expect(classifyAccountCredentialStatusEvidence(row)).toBe('none');
    expect(resolveAccountAuthenticationProblemEvidence(row)).toBeNull();
  });

  it('ignores request authentication failures at or before the reauthentication boundary', () => {
    const row = makeRow({ authenticationAtMs: 2_000 });
    const requestEvidence = resolveAccountRequestHealthEvidence({
      latestRequest: makeRequest({ timestamp_ms: 1_000, failed: true, fail_status_code: 401 }),
    });

    expect(isAccountRequestCredentialEvidenceCurrent(row, requestEvidence)).toBe(false);
    expect(isAccountRequestHealthEvidenceCurrent(row, requestEvidence)).toBe(false);
    expect(resolveAccountAuthenticationProblemEvidence(row, requestEvidence)).toBeNull();
  });

  it('keeps a request authentication failure newer than reauthentication current', () => {
    const row = makeRow({ authenticationAtMs: 2_000 });
    const requestEvidence = resolveAccountRequestHealthEvidence({
      latestRequest: makeRequest({ timestamp_ms: 3_000, failed: true, fail_status_code: 401 }),
    });

    expect(isAccountRequestCredentialEvidenceCurrent(row, requestEvidence)).toBe(true);
    expect(isAccountRequestHealthEvidenceCurrent(row, requestEvidence)).toBe(true);
    expect(resolveAccountAuthenticationProblemEvidence(row, requestEvidence)).toEqual({
      source: 'request',
      observedAtMs: 3_000,
      statusCode: 401,
    });
  });

  it('does not use a pre-reauth success request as current authentication proof', () => {
    const row = makeRow({ authenticationAtMs: 2_000 });
    const requestEvidence = resolveAccountRequestHealthEvidence({
      latestRequest: makeRequest({ timestamp_ms: 1_000, failed: false }),
    });

    expect(isAccountRequestHealthEvidenceCurrent(row, requestEvidence)).toBe(false);
    expect(isAccountRequestCredentialEvidenceCurrent(row, requestEvidence)).toBe(false);
    expect(resolveAccountAuthenticationProblemEvidence(row, requestEvidence)).toBeNull();
  });

  it('accepts a post-reauth success request as current authentication proof', () => {
    const row = makeRow({ authenticationAtMs: 2_000 });
    const requestEvidence = resolveAccountRequestHealthEvidence({
      latestRequest: makeRequest({ timestamp_ms: 3_000, failed: false }),
    });

    expect(isAccountRequestHealthEvidenceCurrent(row, requestEvidence)).toBe(true);
    expect(isAccountRequestCredentialEvidenceCurrent(row, requestEvidence)).toBe(true);
  });

  it('retains a pre-reauth quota request as current quota evidence', () => {
    const row = makeRow({ authenticationAtMs: 2_000 });
    const requestEvidence = resolveAccountRequestHealthEvidence({
      latestRequest: makeRequest({ timestamp_ms: 1_000, failed: true, fail_status_code: 429 }),
    });

    expect(requestEvidence).toMatchObject({ kind: 'quota', direction: 'positive' });
    expect(isAccountRequestHealthEvidenceCurrent(row, requestEvidence)).toBe(true);
    expect(isAccountRequestCredentialEvidenceCurrent(row, requestEvidence)).toBe(true);
    expect(hasAccountQuotaLimitEvidence(row, { latestRequest: requestEvidence?.request })).toBe(
      true
    );
  });

  it('keeps new transient failures actionable without reviving an old success request', () => {
    const row = makeRow({ authenticationAtMs: 2_000 });
    const requestEvidence = resolveAccountRequestHealthEvidence({
      recentRequests: [
        makeRequest({ timestamp_ms: 3_100, failed: true, fail_status_code: 503 }),
        makeRequest({ timestamp_ms: 3_000, failed: true, fail_status_code: 502 }),
        makeRequest({ timestamp_ms: 1_000, failed: false }),
      ],
    });

    expect(requestEvidence).toMatchObject({
      kind: 'transient_failure',
      request: { timestamp_ms: 3_100 },
    });
    expect(isAccountRequestHealthEvidenceCurrent(row, requestEvidence)).toBe(true);
    expect(resolveAccountExceptionProblemEvidence(row, requestEvidence)).toEqual({
      source: 'request',
      observedAtMs: 3_100,
    });
  });

  it('keeps a new request 401 actionable when an older success precedes recovery', () => {
    const row = makeRow({ authenticationAtMs: 2_000 });
    const requestEvidence = resolveAccountRequestHealthEvidence({
      recentRequests: [
        makeRequest({ timestamp_ms: 3_000, failed: true, fail_status_code: 401 }),
        makeRequest({ timestamp_ms: 1_000, failed: false }),
      ],
    });

    expect(isAccountRequestHealthEvidenceCurrent(row, requestEvidence)).toBe(true);
    expect(resolveAccountAuthenticationProblemEvidence(row, requestEvidence)).toMatchObject({
      source: 'request',
      observedAtMs: 3_000,
      statusCode: 401,
    });
  });

  it('retains new transient failures while retiring an older request authentication failure', () => {
    const row = makeRow({ authenticationAtMs: 2_000 });
    const requestEvidence = resolveAccountRequestHealthEvidence({
      recentRequests: [
        makeRequest({ timestamp_ms: 3_100, failed: true, fail_status_code: 502 }),
        makeRequest({ timestamp_ms: 3_000, failed: true, fail_status_code: 503 }),
        makeRequest({ timestamp_ms: 1_000, failed: true, fail_status_code: 401 }),
      ],
    });

    expect(requestEvidence).toMatchObject({
      kind: 'transient_failure',
      request: { timestamp_ms: 3_100 },
    });
    expect(isAccountRequestCredentialEvidenceCurrent(row, requestEvidence)).toBe(false);
    expect(resolveAccountAuthenticationProblemEvidence(row, requestEvidence)).toBeNull();
    expect(resolveAccountExceptionProblemEvidence(row, requestEvidence)).toEqual({
      source: 'request',
      observedAtMs: 3_100,
    });
  });

  it('leaves a credential unconfirmed when the only request evidence is an old 401', () => {
    const row = makeRow({ authenticationAtMs: 2_000 });
    const requestEvidence = resolveAccountRequestHealthEvidence({
      latestRequest: makeRequest({ timestamp_ms: 1_000, failed: true, fail_status_code: 401 }),
    });

    expect(resolveAccountAuthenticationProblemEvidence(row, requestEvidence)).toBeNull();
    expect(resolveAccountExceptionProblemEvidence(row, requestEvidence)).toBeNull();
  });

  it('selects the latest current authentication failure across request, Header, refresh, and inspection', () => {
    const requestEvidence = resolveAccountRequestHealthEvidence({
      latestRequest: makeRequest({
        timestamp_ms: 2_500,
        failed: true,
        fail_status_code: 401,
      }),
    });
    const row = makeRow({
      statusMessage: 'unauthorized',
      updatedAtMs: 1_000,
      raw: { name: 'codex.json', type: 'codex', status_code: 401 },
      quota: {
        ...makeRow().quota,
        status: 'error',
        error: 'HTTP 401 unauthorized',
        errorStatus: 401,
        failedAtMs: 1_500,
        observedAtMs: 3_000,
        observedErrorKind: 'auth',
        observedErrorCode: 'invalid_api_key',
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
    });

    expect(resolveAccountAuthenticationProblemEvidence(row, requestEvidence)).toEqual({
      source: 'observed_header',
      observedAtMs: 3_000,
      statusCode: null,
    });
    expect(
      resolveAccountAuthenticationProblemEvidence(
        {
          ...row,
          quota: {
            ...row.quota,
            observedAtMs: undefined,
            observedErrorKind: undefined,
            observedErrorCode: undefined,
          },
        },
        requestEvidence
      )
    ).toEqual({ source: 'request', observedAtMs: 2_500, statusCode: 401 });
    expect(
      resolveAccountAuthenticationProblemEvidence(
        {
          ...row,
          quota: {
            ...row.quota,
            observedAtMs: undefined,
            observedErrorKind: undefined,
            observedErrorCode: undefined,
          },
        },
        null
      )
    ).toEqual({ source: 'inspection', observedAtMs: 2_000, statusCode: 401 });
  });

  it('uses the latest timed authentication evidence without dropping unknown-time failures', () => {
    const requestEvidence = resolveAccountRequestHealthEvidence({
      latestRequest: makeRequest({
        timestamp_ms: 2_500,
        failed: true,
        fail_status_code: 401,
      }),
    });
    const row = makeRow({
      updatedAtMs: null,
      raw: { name: 'codex.json', type: 'codex', status_code: 401 },
    });

    expect(resolveAccountAuthenticationProblemEvidence(row, requestEvidence)).toEqual({
      source: 'request',
      observedAtMs: 2_500,
      statusCode: 401,
    });
    expect(
      resolveAccountAuthenticationProblemEvidence(
        row,
        resolveAccountRequestHealthEvidence({
          latestRequest: makeRequest({ timestamp_ms: 3_000, failed: false }),
        })
      )
    ).toEqual({ source: 'credential_status', observedAtMs: null, statusCode: 401 });
  });

  it('does not let unknown-time positive evidence hide a timed authentication failure', () => {
    const requestEvidence = resolveAccountRequestHealthEvidence({
      latestRequest: makeRequest({
        timestamp_ms: 2_500,
        failed: true,
        fail_status_code: 401,
      }),
    });
    const rows = [
      makeRow({
        statusMessage: 'active',
        updatedAtMs: null,
        raw: { name: 'codex.json', type: 'codex', status_code: 200 },
      }),
      makeRow({
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
          resultId: 2,
          createdAtMs: 0,
        },
      }),
    ];

    rows.forEach((row) => {
      expect(isAccountRequestCredentialEvidenceCurrent(row, requestEvidence)).toBe(true);
      expect(resolveAccountAuthenticationProblemEvidence(row, requestEvidence)).toEqual({
        source: 'request',
        observedAtMs: 2_500,
        statusCode: 401,
      });
    });
  });

  it('uses inspection authentication error kinds as negative status evidence', () => {
    const row = makeRow({
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
    });

    expect(isAccountInspectionAuthenticationFailure(row)).toBe(true);
    expect(resolveAccountAuthenticationProblemEvidence(row)).toEqual({
      source: 'inspection',
      observedAtMs: 3_000,
      statusCode: 503,
    });
  });

  it('keeps HTTP 499 inspection results neutral even when stale action metadata says reauth', () => {
    const row = makeRow({
      inspection: {
        source: 'server',
        action: 'reauth',
        actionReason: 'authentication_error: request canceled by client',
        actionStatus: 'pending',
        statusCode: 499,
        usedPercent: null,
        isQuota: false,
        errorKind: 'authentication_error',
        runId: 1,
        resultId: 2,
        createdAtMs: 3_000,
      },
    });

    expect(isAccountInspectionAuthenticationFailure(row)).toBe(false);
    expect(isAccountInspectionActionable(row)).toBe(false);
    expect(isAccountInspectionStatusEvidenceCurrent(row)).toBe(false);
    expect(resolveAccountAuthenticationProblemEvidence(row)).toBeNull();
    expect(resolveAccountExceptionProblemEvidence(row)).toBeNull();
  });

  it('does not let a newer neutral inspection expire qualified request health evidence', () => {
    const row = makeRow({
      inspection: {
        source: 'server',
        action: 'reauth',
        actionReason: 'authentication_error: request canceled by client',
        actionStatus: 'pending',
        statusCode: 499,
        usedPercent: null,
        isQuota: false,
        errorKind: 'authentication_error',
        runId: 1,
        resultId: 3,
        createdAtMs: 3_000,
      },
    });
    const olderSuccess = resolveAccountRequestHealthEvidence({
      latestRequest: makeRequest({ timestamp_ms: 2_000 }),
    });
    const olderConsecutiveFailures = resolveAccountRequestHealthEvidence({
      recentRequests: [
        makeRequest({ timestamp_ms: 2_000, failed: true, fail_status_code: 503 }),
        makeRequest({ timestamp_ms: 1_900, failed: true, fail_status_code: 502 }),
      ],
    });

    expect(isAccountRequestHealthEvidenceCurrent(row, olderSuccess)).toBe(true);
    expect(isAccountRequestHealthEvidenceCurrent(row, olderConsecutiveFailures)).toBe(true);
  });

  it('does not let a newer non-authentication inspection erase request authentication evidence', () => {
    const requestEvidence = resolveAccountRequestHealthEvidence({
      latestRequest: makeRequest({
        timestamp_ms: 2_000,
        failed: true,
        fail_status_code: 401,
      }),
    });
    const row = makeRow({
      inspection: {
        source: 'server',
        action: 'delete',
        actionReason: 'upstream unavailable',
        actionStatus: 'pending',
        statusCode: 503,
        usedPercent: null,
        runId: 1,
        resultId: 2,
        createdAtMs: 3_000,
      },
    });

    expect(isAccountRequestCredentialEvidenceCurrent(row, requestEvidence)).toBe(true);
    expect(resolveAccountAuthenticationProblemEvidence(row, requestEvidence)).toEqual({
      source: 'request',
      observedAtMs: 2_000,
      statusCode: 401,
    });
  });

  it('does not treat a newer keep result with a transient status as authentication recovery', () => {
    const requestEvidence = resolveAccountRequestHealthEvidence({
      latestRequest: makeRequest({
        timestamp_ms: 2_000,
        failed: true,
        fail_status_code: 401,
      }),
    });
    const row = makeRow({
      updatedAtMs: 1_000,
      raw: { name: 'codex.json', type: 'codex', status_code: 401 },
      inspection: {
        source: 'server',
        action: 'keep',
        actionReason: 'upstream unavailable',
        actionStatus: 'none',
        statusCode: 503,
        usedPercent: null,
        isQuota: false,
        errorKind: 'upstream_error',
        runId: 1,
        resultId: 2,
        createdAtMs: 3_000,
      },
    });

    expect(isAccountRequestCredentialEvidenceCurrent(row, requestEvidence)).toBe(true);
    expect(isAccountCredentialStatusProblemCurrent(row, requestEvidence)).toBe(true);
    expect(resolveAccountAuthenticationProblemEvidence(row, requestEvidence)).toEqual({
      source: 'request',
      observedAtMs: 2_000,
      statusCode: 401,
    });
  });

  it('selects the latest current non-authentication exception evidence', () => {
    const row = makeRow({
      quota: {
        ...makeRow().quota,
        status: 'error',
        error: 'upstream unavailable',
        errorStatus: 503,
        failedAtMs: 1_500,
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
    const requestEvidence = resolveAccountRequestHealthEvidence({
      recentRequests: [
        makeRequest({ timestamp_ms: 3_000, failed: true, fail_status_code: 503 }),
        makeRequest({ timestamp_ms: 2_500, failed: true, fail_status_code: 502 }),
      ],
    });

    expect(resolveAccountExceptionProblemEvidence(row)).toEqual({
      source: 'inspection',
      observedAtMs: 2_000,
    });
    expect(resolveAccountExceptionProblemEvidence(row, requestEvidence)).toEqual({
      source: 'request',
      observedAtMs: 3_000,
    });
    expect(
      resolveAccountExceptionProblemEvidence(
        {
          ...row,
          quota: { ...row.quota, failedAtMs: 4_000 },
        },
        requestEvidence
      )
    ).toEqual({ source: 'quota_refresh', observedAtMs: 4_000 });
    expect(
      resolveAccountExceptionProblemEvidence(
        {
          ...row,
          inspection: { ...row.inspection!, createdAtMs: 0 },
        },
        requestEvidence
      )
    ).toEqual({ source: 'request', observedAtMs: 3_000 });
  });

  it('ignores neutral requests without treating them as health recovery', () => {
    expect(
      resolveAccountRequestHealthEvidence({
        recentRequests: [
          makeRequest({ timestamp_ms: 4_000, failed: true, fail_status_code: 503 }),
          makeRequest({ timestamp_ms: 3_000, failed: true, fail_status_code: 499 }),
          makeRequest({ timestamp_ms: 2_000, failed: true, fail_status_code: 502 }),
          makeRequest({ timestamp_ms: 1_000, failed: false }),
        ],
      })
    ).toMatchObject({
      kind: 'transient_failure',
      consecutiveTransientFailures: 2,
      request: { timestamp_ms: 4_000 },
    });
  });

  it('keeps distinct same-millisecond failures while removing the latest-request alias', () => {
    const repeatedFailure = makeRequest({
      timestamp_ms: 3_000,
      failed: true,
      fail_status_code: 503,
      fail_summary: 'upstream unavailable',
    });

    expect(
      resolveAccountRequestHealthEvidence({
        latestRequest: repeatedFailure,
        recentRequests: [repeatedFailure, { ...repeatedFailure }],
      })
    ).toMatchObject({
      kind: 'transient_failure',
      consecutiveTransientFailures: 2,
    });
  });

  it('uses the richer latest-request alias without inventing another failure', () => {
    const sparseFailure = makeRequest({
      timestamp_ms: 3_000,
      failed: true,
      fail_status_code: 503,
    });
    const richFailure = {
      ...sparseFailure,
      fail_summary: 'upstream unavailable',
      header_trace_id: 'trace-1',
    };

    expect(
      resolveAccountRequestHealthEvidence({
        latestRequest: richFailure,
        recentRequests: [sparseFailure],
      })
    ).toBeNull();
  });

  it('merges request sources without multiplying cross-source aliases', () => {
    const repeatedFailure = makeRequest({
      timestamp_ms: 3_000,
      failed: true,
      fail_status_code: 503,
      fail_summary: 'upstream unavailable',
    });
    const secondFailure = makeRequest({
      timestamp_ms: 2_500,
      failed: true,
      fail_status_code: 502,
      fail_summary: 'bad gateway',
    });

    const merged = mergeAccountRequestEvidenceInputs([
      { latestRequest: repeatedFailure, recentRequests: [repeatedFailure] },
      { recentRequests: [repeatedFailure, secondFailure] },
      { latestRequest: repeatedFailure },
    ]);

    expect(merged.recentRequests).toEqual([repeatedFailure, secondFailure]);
    expect(resolveAccountRequestHealthEvidence(merged)).toMatchObject({
      kind: 'transient_failure',
      consecutiveTransientFailures: 2,
      request: { timestamp_ms: 3_000 },
    });
  });

  it('deduplicates richer cross-source aliases while preserving maximum source multiplicity', () => {
    const sparseFailure = makeRequest({
      timestamp_ms: 3_000,
      failed: true,
      fail_status_code: 503,
    });
    const richFailure = makeRequest({
      ...sparseFailure,
      fail_summary: 'upstream unavailable',
      header_error_kind: 'server_error',
      header_trace_id: 'trace-1',
    });

    const merged = mergeAccountRequestEvidenceInputs([
      { recentRequests: [sparseFailure, { ...sparseFailure }] },
      { recentRequests: [richFailure] },
      { latestRequest: sparseFailure },
    ]);

    expect(merged.recentRequests).toHaveLength(2);
    expect(merged.recentRequests?.[0]).toEqual(richFailure);
    expect(merged.recentRequests?.[1]).toEqual(sparseFailure);
  });

  it('lets a newer success supersede only unresolved inspection advice for the same generation', () => {
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
    const success = resolveAccountRequestHealthEvidence({
      latestRequest: makeRequest({ timestamp_ms: 2_000 }),
    });
    const quota = resolveAccountRequestHealthEvidence({
      latestRequest: makeRequest({ timestamp_ms: 2_000, failed: true, fail_status_code: 429 }),
    });

    expect(isAccountInspectionActionable(row, success)).toBe(false);
    expect(isAccountInspectionActionable(row, quota)).toBe(false);
    expect(isAccountRequestHealthEvidenceCurrent(row, success)).toBe(true);
    expect(isAccountRequestHealthEvidenceCurrent(row, quota)).toBe(true);
    expect(
      isAccountInspectionActionable(
        row,
        resolveAccountRequestHealthEvidence({
          latestRequest: makeRequest({
            timestamp_ms: 2_000,
            failed: true,
            fail_status_code: 499,
          }),
        })
      )
    ).toBe(true);
    expect(
      isAccountInspectionActionable(
        { ...row, inspection: { ...row.inspection!, actionStatus: 'success' } },
        null
      )
    ).toBe(false);
    const successPersistedAfterRequestCompletion = resolveAccountRequestHealthEvidence({
      latestRequest: makeRequest({ timestamp_ms: 1_700_000_002_000 }),
    });
    expect(
      isAccountInspectionActionable(
        {
          ...row,
          raw: {
            ...row.raw,
            modtime: 1_700_000_003_000,
          },
        },
        successPersistedAfterRequestCompletion
      )
    ).toBe(false);
  });

  it('keeps healthy inspection status evidence without reviving handled reauth advice', () => {
    const healthyRow = makeRow({
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
        resultId: 2,
        createdAtMs: 2_000,
      },
    });
    const handledReauthRow = makeRow({
      inspection: {
        source: 'server',
        action: 'reauth',
        actionReason: 'expired',
        actionStatus: 'success',
        statusCode: 401,
        usedPercent: null,
        isQuota: false,
        runId: 1,
        resultId: 3,
        createdAtMs: 2_000,
      },
    });

    expect(isAccountInspectionStatusEvidenceCurrent(healthyRow)).toBe(true);
    expect(isAccountInspectionStatusEvidenceCurrent(handledReauthRow)).toBe(false);
  });

  it('does not treat healthy inspection evidence from before reauthentication as current', () => {
    const row = makeRow({
      authenticationAtMs: 2_000,
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
        resultId: 4,
        createdAtMs: 1_000,
      },
    });

    expect(isAccountInspectionHealthyEvidence(row)).toBe(false);
    expect(resolveAccountAuthenticationProblemEvidence(row)).toBeNull();
  });

  it('keeps a keep plus HTTP 401 inspection as authentication evidence until recovery', () => {
    const row = makeRow({
      inspection: {
        source: 'server',
        action: 'keep',
        actionReason: 'legacy inspection preserved the credential after HTTP 401',
        actionStatus: 'none',
        statusCode: 401,
        usedPercent: null,
        isQuota: false,
        runId: 1,
        resultId: 4,
        createdAtMs: 1_000,
      },
    });

    expect(isAccountInspectionActionable(row)).toBe(false);
    expect(isAccountInspectionStatusEvidenceCurrent(row)).toBe(true);
    expect(resolveAccountAuthenticationProblemEvidence(row)).toMatchObject({
      source: 'inspection',
      observedAtMs: 1_000,
      statusCode: 401,
    });

    const newerSuccess = resolveAccountRequestHealthEvidence({
      latestRequest: makeRequest({ timestamp_ms: 2_000 }),
    });
    expect(isAccountInspectionStatusEvidenceCurrent(row, newerSuccess)).toBe(false);
    expect(resolveAccountAuthenticationProblemEvidence(row, newerSuccess)).toBeNull();
  });

  it('lets a newer successful request retire a dated error-kind inspection auth failure', () => {
    const row = makeRow({
      inspection: {
        source: 'server',
        action: 'keep',
        actionReason: 'authentication service rejected the credential',
        actionStatus: 'success',
        statusCode: 503,
        usedPercent: null,
        isQuota: false,
        errorKind: 'authentication_error',
        runId: 1,
        resultId: 4,
        createdAtMs: 1_000,
      },
    });
    const newerSuccess = resolveAccountRequestHealthEvidence({
      latestRequest: makeRequest({ timestamp_ms: 2_000 }),
    });

    expect(isAccountInspectionAuthenticationFailure(row)).toBe(true);
    expect(isAccountInspectionStatusEvidenceCurrent(row, newerSuccess)).toBe(false);
  });

  it('keeps an error-kind inspection auth failure when its observation time is unknown', () => {
    const row = makeRow({
      inspection: {
        source: 'server',
        action: 'keep',
        actionReason: 'authentication service rejected the credential',
        actionStatus: 'success',
        statusCode: 503,
        usedPercent: null,
        isQuota: false,
        errorKind: 'authentication_error',
        runId: 1,
        resultId: 5,
        createdAtMs: 0,
      },
    });
    const newerSuccess = resolveAccountRequestHealthEvidence({
      latestRequest: makeRequest({ timestamp_ms: 2_000 }),
    });

    expect(isAccountInspectionAuthenticationFailure(row)).toBe(true);
    expect(isAccountInspectionStatusEvidenceCurrent(row, newerSuccess)).toBe(true);
  });

  it('does not treat newer negative evidence as resolving a pending inspection action', () => {
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
    const transientFailure = resolveAccountRequestHealthEvidence({
      recentRequests: [
        makeRequest({ timestamp_ms: 3_000, failed: true, fail_status_code: 503 }),
        makeRequest({ timestamp_ms: 2_000, failed: true, fail_status_code: 502 }),
      ],
    });

    expect(isAccountInspectionActionable(row, transientFailure)).toBe(true);
  });

  it('does not clear quota inspection advice after a successful request', () => {
    const row = makeRow({
      inspection: {
        source: 'server',
        action: 'disable',
        actionReason: 'quota threshold reached',
        actionStatus: 'pending',
        statusCode: 200,
        usedPercent: 96,
        isQuota: true,
        runId: 1,
        resultId: 2,
        createdAtMs: 1_000,
      },
    });
    const success = resolveAccountRequestHealthEvidence({
      latestRequest: makeRequest({ timestamp_ms: 2_000 }),
    });

    expect(isAccountInspectionActionable(row, success)).toBe(true);
  });

  it('does not treat CPA persistence timestamps as credential generation boundaries', () => {
    const requestAtMs = 1_700_000_000_000;
    const success = resolveAccountRequestHealthEvidence({
      latestRequest: makeRequest({ timestamp_ms: requestAtMs }),
    });
    const persistedAfterRequest = makeRow({
      updatedAtMs: requestAtMs + 10_000,
      raw: {
        name: 'codex.json',
        type: 'codex',
        modtime: requestAtMs + 10_000,
        updated_at: requestAtMs + 10_000,
      },
    });

    expect(isAccountRequestHealthEvidenceCurrent(persistedAfterRequest, success)).toBe(true);
  });

  it('keeps same-outcome request-start evidence current after CPA records completion time', () => {
    const requestAtMs = 1_700_000_000_000;
    const credentialFailure = resolveAccountRequestHealthEvidence({
      latestRequest: makeRequest({
        timestamp_ms: requestAtMs,
        failed: true,
        fail_status_code: 401,
      }),
    });
    const persistedFailure = makeRow({
      statusMessage: 'unauthorized',
      updatedAtMs: requestAtMs + 10_000,
      raw: {
        name: 'codex.json',
        type: 'codex',
        status_code: 401,
      },
    });

    expect(isAccountRequestHealthEvidenceCurrent(persistedFailure, credentialFailure)).toBe(true);
  });

  it('does not treat diagnostic-only Header timestamps as conflicting health evidence', () => {
    const success = resolveAccountRequestHealthEvidence({
      latestRequest: makeRequest({ timestamp_ms: 1_000 }),
    });
    const row = makeRow({
      quota: {
        ...makeRow().quota,
        observedAtMs: 2_000,
        observedTraceId: 'trace-only',
      },
    });

    expect(isAccountRequestHealthEvidenceCurrent(row, success)).toBe(true);
  });

  it('does not let a single newer transient state invalidate the last success', () => {
    const success = resolveAccountRequestHealthEvidence({
      latestRequest: makeRequest({ timestamp_ms: 2_000 }),
    });
    const runtimeTransient = makeRow({
      statusMessage: 'upstream unavailable',
      updatedAtMs: 3_000,
      raw: { name: 'codex.json', type: 'codex', status_code: 503 },
    });
    const headerTransient = makeRow({
      quota: {
        ...makeRow().quota,
        observedAtMs: 3_000,
        observedErrorKind: 'server_error',
        observedErrorCode: 'upstream_unavailable',
      },
    });

    expect(isAccountRequestHealthEvidenceCurrent(runtimeTransient, success)).toBe(true);
    expect(isAccountRequestHealthEvidenceCurrent(headerTransient, success)).toBe(true);
  });

  it('does not treat a diagnostic Header merged onto cached quota as healthy quota evidence', () => {
    const success = resolveAccountRequestHealthEvidence({
      latestRequest: makeRequest({ timestamp_ms: 1_000 }),
    });
    const row = makeRow({
      quota: {
        ...makeRow().quota,
        status: 'ok',
        source: 'cache',
        fetchedAtMs: 500,
        observedAtMs: 2_000,
        observedErrorKind: 'server_error',
        observedErrorCode: 'upstream_unavailable',
      },
    });

    expect(isAccountRequestHealthEvidenceCurrent(row, success)).toBe(true);
  });

  it('lets a newer conflicting runtime completion supersede older request evidence', () => {
    const requestAtMs = 1_700_000_000_000;
    const success = resolveAccountRequestHealthEvidence({
      latestRequest: makeRequest({ timestamp_ms: requestAtMs }),
    });
    const credentialFailure = resolveAccountRequestHealthEvidence({
      latestRequest: makeRequest({
        timestamp_ms: requestAtMs,
        failed: true,
        fail_status_code: 401,
      }),
    });

    expect(
      isAccountRequestHealthEvidenceCurrent(
        makeRow({
          statusMessage: 'unauthorized',
          updatedAtMs: requestAtMs + 10_000,
          raw: { name: 'codex.json', type: 'codex', status_code: 401 },
        }),
        success
      )
    ).toBe(false);
    expect(
      isAccountRequestHealthEvidenceCurrent(
        makeRow({
          statusMessage: 'active',
          updatedAtMs: requestAtMs + 10_000,
          raw: { name: 'codex.json', type: 'codex', status_code: 200 },
        }),
        credentialFailure
      )
    ).toBe(false);
  });

  it('lets newer healthy quota evidence supersede older negative request evidence', () => {
    const credentialFailure = resolveAccountRequestHealthEvidence({
      latestRequest: makeRequest({
        timestamp_ms: 1_000,
        failed: true,
        fail_status_code: 401,
      }),
    });
    const transientFailure = resolveAccountRequestHealthEvidence({
      recentRequests: [
        makeRequest({ timestamp_ms: 1_000, failed: true, fail_status_code: 503 }),
        makeRequest({ timestamp_ms: 900, failed: true, fail_status_code: 502 }),
      ],
    });

    const providerQuota = makeRow({
      quota: {
        ...makeRow().quota,
        status: 'ok',
        source: 'cache',
        fetchedAtMs: 2_000,
      },
    });
    const observedHeaderQuota = makeRow({
      quota: {
        ...makeRow().quota,
        status: 'low',
        source: 'observed-header',
        observedAtMs: 2_000,
        observedQuotaAtMs: 2_000,
      },
    });

    expect(isAccountRequestHealthEvidenceCurrent(providerQuota, credentialFailure)).toBe(false);
    expect(isAccountRequestHealthEvidenceCurrent(providerQuota, transientFailure)).toBe(false);
    expect(isAccountRequestHealthEvidenceCurrent(observedHeaderQuota, credentialFailure)).toBe(
      false
    );
  });

  it('uses newer positive quota and inspection evidence to retire stale auth conclusions', () => {
    const staleCredentialStatus = makeRow({
      statusMessage: 'unauthorized',
      updatedAtMs: 1_000,
      raw: { name: 'codex.json', type: 'codex', status_code: 401 },
      quota: { ...makeRow().quota, status: 'ok', fetchedAtMs: 2_000 },
    });
    expect(isAccountCredentialStatusProblemCurrent(staleCredentialStatus)).toBe(false);

    const staleObservedAuth = makeRow({
      quota: {
        ...makeRow().quota,
        status: 'ok',
        fetchedAtMs: 2_000,
        observedAtMs: 1_000,
        observedErrorKind: 'auth',
        observedErrorCode: 'invalid_api_key',
      },
    });
    expect(isAccountObservedDiagnosticProblemCurrent(staleObservedAuth)).toBe(false);

    const staleInspection = makeRow({
      quota: { ...makeRow().quota, status: 'ok', fetchedAtMs: 2_000 },
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
    expect(isAccountInspectionActionable(staleInspection)).toBe(false);

    const staleQuotaRefresh = makeRow({
      quota: {
        ...makeRow().quota,
        status: 'error',
        error: 'HTTP 401 unauthorized',
        errorStatus: 401,
        failedAtMs: 1_000,
      },
      inspection: {
        source: 'server',
        action: 'keep',
        actionReason: 'healthy',
        actionStatus: 'success',
        statusCode: 200,
        usedPercent: null,
        runId: 2,
        resultId: 3,
        createdAtMs: 2_000,
      },
    });
    expect(isAccountQuotaRefreshProblemCurrent(staleQuotaRefresh)).toBe(false);
  });

  it('keeps neutral and healthy runtime messages out of credential health', () => {
    for (const row of [
      makeRow({
        statusMessage: 'context canceled',
        updatedAtMs: 2_000,
        raw: { name: 'codex.json', type: 'codex', status_code: 499 },
      }),
      makeRow({ statusMessage: 'invalid_request_error', updatedAtMs: 2_000 }),
      makeRow({ statusMessage: 'active', updatedAtMs: 2_000 }),
    ]) {
      expect(classifyAccountCredentialStatusEvidence(row)).toMatch(/neutral|healthy/);
      expect(isAccountCredentialStatusProblemCurrent(row)).toBe(false);
    }
  });

  it('does not promote a single runtime transient failure to credential health', () => {
    const row = makeRow({
      statusMessage: 'upstream unavailable',
      updatedAtMs: 2_000,
      raw: { name: 'codex.json', type: 'codex', status_code: 503 },
    });

    expect(classifyAccountCredentialStatusEvidence(row)).toBe('transient_failure');
    expect(isAccountCredentialStatusProblemCurrent(row)).toBe(false);
  });

  it('classifies quota refresh outcomes without treating HTTP 499 as an account problem', () => {
    const quotaRefreshRow = (
      errorStatus: number,
      error: string,
      failedAtMs = 2_000
    ): AccountRow =>
      makeRow({
        quota: {
          ...makeRow().quota,
          status: 'error',
          error,
          errorStatus,
          failedAtMs,
        },
      });
    const neutral = quotaRefreshRow(499, 'context canceled');
    const credentialFailure = quotaRefreshRow(401, 'unauthorized');
    const credentialFailureFromText = makeRow({
      quota: {
        ...makeRow().quota,
        status: 'error',
        error: 'quota refresh failed: HTTP 401',
        failedAtMs: 2_000,
      },
    });
    const quotaLimit = quotaRefreshRow(429, 'rate limit exceeded');
    const transientFailure = quotaRefreshRow(503, 'upstream unavailable');

    expect(classifyAccountQuotaRefreshEvidence(neutral)).toBe('neutral');
    expect(isAccountQuotaRefreshProblemCurrent(neutral)).toBe(false);
    expect(classifyAccountQuotaRefreshEvidence(credentialFailure)).toBe('credential_failure');
    expect(classifyAccountQuotaRefreshEvidence(credentialFailureFromText)).toBe(
      'credential_failure'
    );
    expect(isAccountQuotaRefreshProblemCurrent(credentialFailure)).toBe(true);
    expect(classifyAccountQuotaRefreshEvidence(quotaLimit)).toBe('quota');
    expect(isAccountQuotaRefreshProblemCurrent(quotaLimit)).toBe(false);
    expect(hasAccountQuotaLimitEvidence(quotaLimit)).toBe(true);
    expect(classifyAccountQuotaRefreshEvidence(transientFailure)).toBe('transient_failure');
    expect(isAccountQuotaRefreshProblemCurrent(transientFailure)).toBe(true);

    const newerSuccess = resolveAccountRequestHealthEvidence({
      latestRequest: makeRequest({ timestamp_ms: 3_000 }),
    });
    expect(isAccountQuotaRefreshProblemCurrent(credentialFailure, newerSuccess)).toBe(false);
    expect(isAccountQuotaRefreshProblemCurrent(transientFailure, newerSuccess)).toBe(false);
  });

  it('classifies runtime and explicit provider limit signals as quota evidence', () => {
    const staleRuntimeQuota = makeRow({
      statusMessage: 'quota exceeded',
      updatedAtMs: 1_000,
      raw: { name: 'codex.json', type: 'codex', status_code: 429 },
    });
    expect(
      hasAccountQuotaLimitEvidence(
        makeRow({
          statusMessage: 'quota exceeded',
          raw: { name: 'codex.json', type: 'codex', status_code: 429 },
        })
      )
    ).toBe(true);
    expect(
      hasAccountQuotaLimitEvidence(staleRuntimeQuota, {
        latestRequest: makeRequest({ timestamp_ms: 2_000, failed: false }),
      })
    ).toBe(false);
    expect(
      hasAccountQuotaLimitEvidence(
        { ...staleRuntimeQuota, updatedAtMs: null },
        { latestRequest: makeRequest({ timestamp_ms: 2_000, failed: false }) }
      )
    ).toBe(true);
    expect(
      hasAccountQuotaLimitEvidence(
        makeRow({ quota: { ...makeRow().quota, rateLimitReachedType: 'primary' } })
      )
    ).toBe(true);
    expect(
      hasAccountQuotaLimitEvidence(
        makeRow({ quota: { ...makeRow().quota, spendControlReached: true } })
      )
    ).toBe(true);
    expect(
      hasAccountQuotaLimitEvidence(
        makeRow({ quota: { ...makeRow().quota, creditsOverageLimitReached: true } })
      )
    ).toBe(true);
  });

  it('lets only a newer authenticated request clear a real runtime credential failure', () => {
    const row = makeRow({ statusMessage: 'unauthorized', updatedAtMs: 2_000 });
    const neutral = resolveAccountRequestHealthEvidence({
      latestRequest: makeRequest({ timestamp_ms: 3_000, failed: true, fail_status_code: 499 }),
    });
    const olderSuccess = resolveAccountRequestHealthEvidence({
      latestRequest: makeRequest({ timestamp_ms: 1_500 }),
    });
    const newerSuccess = resolveAccountRequestHealthEvidence({
      latestRequest: makeRequest({ timestamp_ms: 3_000 }),
    });
    const newerQuota = resolveAccountRequestHealthEvidence({
      latestRequest: makeRequest({ timestamp_ms: 3_000, failed: true, fail_status_code: 429 }),
    });

    expect(classifyAccountCredentialStatusEvidence(row)).toBe('credential_failure');
    expect(isAccountCredentialStatusProblemCurrent(row, neutral)).toBe(true);
    expect(isAccountCredentialStatusProblemCurrent(row, olderSuccess)).toBe(true);
    expect(isAccountCredentialStatusProblemCurrent(row, newerSuccess)).toBe(false);
    expect(isAccountCredentialStatusProblemCurrent(row, newerQuota)).toBe(false);
  });

  it('does not let dated successes clear credential failures with unknown observation times', () => {
    const success = resolveAccountRequestHealthEvidence({
      latestRequest: makeRequest({ timestamp_ms: 3_000 }),
    });
    const runtimeFailure = makeRow({
      statusMessage: 'unauthorized',
      updatedAtMs: null,
      raw: { name: 'codex.json', type: 'codex', status_code: 401 },
    });
    const quotaRefreshFailure = makeRow({
      quota: {
        ...makeRow().quota,
        status: 'error',
        error: 'HTTP 401 unauthorized',
        errorStatus: 401,
      },
    });
    const observedFailure = makeRow({
      quota: {
        ...makeRow().quota,
        observedErrorKind: 'auth',
        observedErrorCode: 'invalid_api_key',
      },
    });

    expect(isAccountCredentialStatusProblemCurrent(runtimeFailure, success)).toBe(true);
    expect(isAccountQuotaRefreshProblemCurrent(quotaRefreshFailure, success)).toBe(true);
    expect(isAccountObservedDiagnosticProblemCurrent(observedFailure, success)).toBe(true);
  });

  it('lets a newer success clear auth diagnostics but keeps quota diagnostics', () => {
    const success = resolveAccountRequestHealthEvidence({
      latestRequest: makeRequest({ timestamp_ms: 2_000 }),
    });
    const authDiagnostic = makeRow({
      quota: {
        ...makeRow().quota,
        observedAtMs: 1_000,
        observedErrorKind: 'auth',
        observedErrorCode: 'invalid_api_key',
      },
    });
    const quotaDiagnostic = makeRow({
      quota: {
        ...makeRow().quota,
        observedAtMs: 1_000,
        observedErrorKind: 'rate_limit',
        observedErrorCode: 'quota_exceeded',
      },
    });

    expect(isAccountObservedDiagnosticProblemCurrent(authDiagnostic, success)).toBe(false);
    expect(isAccountObservedDiagnosticProblemCurrent(quotaDiagnostic, success)).toBe(true);
    expect(
      isAccountObservedDiagnosticProblemCurrent(
        makeRow({
          quota: {
            ...makeRow().quota,
            observedAtMs: 3_000,
            observedErrorKind: 'server_error',
            observedErrorCode: 'upstream_unavailable',
          },
        })
      )
    ).toBe(false);
    expect(
      isAccountObservedDiagnosticProblemCurrent(
        makeRow({
          quota: {
            ...makeRow().quota,
            observedAtMs: 3_000,
            observedErrorKind: 'transport',
            observedErrorCode: 'context_canceled',
          },
        })
      )
    ).toBe(false);
  });
});
