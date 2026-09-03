import { describe, expect, it, vi } from 'vitest';
import type { TFunction } from 'i18next';
import type { AuthFileItem, CodexQuotaState, CredentialScopedQuotaState } from '@/types';
import type { UsageHeaderSnapshot } from '@/services/api/usageService';
import {
  getAuthFileCodexInspectionKeyForIdentity,
  getAuthFileCodexStatus,
  getAuthFileSelectionKey,
} from '@/features/authFiles/model/credentialStatus';
import {
  buildAccountInspectionBySelectionKey,
  buildAccountMetrics,
  buildAccountRows as buildAccountRowsBase,
  filterSuppressedAccountInspectionResults,
  findAccountRowForInspectionTarget,
  filterAccountRows,
  getAccountInspectionResultSnapshotKey,
  getHandledAccountInspectionResultKeys,
  getPlanOptionLabel,
  getPlanOptionValue,
  getPlanOptions,
  sortAccountRows,
  type AccountInspectionResult,
  type AccountRow,
  type AccountQuotaStores,
} from './accountRows';
import {
  buildQuotaCredentialIdentity,
  getQuotaCredentialStoreKey,
} from '@/utils/quota/credentialScope';
import type { AccountCredentialEvidenceBoundary } from './accountCredentialEvidence';

const CODEX_MAIN_SCOPE = { kind: 'family', key: 'codex_main', complete: true } as const;

const emptyStores = (): AccountQuotaStores => ({
  antigravityQuota: {},
  claudeQuota: {},
  codexQuota: {},
  kimiQuota: {},
  xaiQuota: {},
});

const evidenceBoundary = (
  overrides: Partial<AccountCredentialEvidenceBoundary> = {}
): AccountCredentialEvidenceBoundary => ({
  localAtMs: 0,
  inspectionAtMs: 0,
  headerAtMs: 0,
  actionAtMs: 0,
  authenticationActionAtMs: 0,
  quotaActionAtMs: 0,
  cooldownAtMs: 0,
  fallbackInspectionAtMs: 0,
  fallbackHeaderAtMs: 0,
  fallbackActionAtMs: 0,
  fallbackCooldownAtMs: 0,
  authenticationAtMs: 0,
  rawStatusAtMs: 0,
  rawStatusMessages: [] as string[],
  rawStatusCodes: [] as number[],
  ...overrides,
});

const scopeTestQuotaStores = (files: AuthFileItem[], stores: AccountQuotaStores) => {
  const fileNameCounts = files.reduce((counts, file) => {
    counts.set(file.name, (counts.get(file.name) ?? 0) + 1);
    return counts;
  }, new Map<string, number>());
  const records = [
    stores.antigravityQuota,
    stores.claudeQuota,
    stores.codexQuota,
    stores.kimiQuota,
    stores.xaiQuota,
  ] as Array<Record<string, CredentialScopedQuotaState>>;

  files.forEach((file) => {
    records.forEach((record) => {
      const legacy = record[file.name];
      if (!legacy || (!legacy.authFileKey && fileNameCounts.get(file.name) !== 1)) return;
      const identity = buildQuotaCredentialIdentity(file);
      const storeKey = legacy.authFileKey || getQuotaCredentialStoreKey(file);
      record[storeKey] = { ...legacy, ...identity, authFileKey: storeKey };
    });
  });
  return stores;
};

const buildAccountRows = (
  files: AuthFileItem[],
  stores: AccountQuotaStores,
  inspectionResults?: Parameters<typeof buildAccountRowsBase>[2],
  overrides?: Parameters<typeof buildAccountRowsBase>[3],
  inspectionBySelectionKey?: Parameters<typeof buildAccountRowsBase>[4],
  evidenceBoundaryBySelectionKey?: Parameters<typeof buildAccountRowsBase>[5],
  statusBoundaryBySelectionKey?: Parameters<typeof buildAccountRowsBase>[6]
) =>
  buildAccountRowsBase(
    files,
    scopeTestQuotaStores(files, stores),
    inspectionResults,
    overrides,
    inspectionBySelectionKey,
    evidenceBoundaryBySelectionKey,
    statusBoundaryBySelectionKey
  );

describe('accountRows', () => {
  it('suppresses only handled inspection authentication results for the reauthenticated identity', () => {
    const authenticationResult: AccountInspectionResult = {
      id: 1,
      runId: 10,
      accountKey: 'codex.json',
      fileName: 'codex.json',
      displayAccount: 'codex.json',
      provider: 'codex',
      authIndex: 'auth-1',
      disabled: false,
      action: 'keep',
      actionReason: 'token rejected',
      actionStatus: 'success',
      statusCode: 503,
      usedPercent: undefined,
      isQuota: false,
      errorKind: 'authentication_error',
      createdAtMs: 1_000,
      inspectionSource: 'server',
    };
    const newerResult: AccountInspectionResult = {
      ...authenticationResult,
      id: 2,
      runId: 11,
      actionReason: 'healthy',
      statusCode: 200,
      errorKind: 'inference_healthy',
      createdAtMs: 2_000,
    };
    const targetIdentityKey = getAuthFileCodexInspectionKeyForIdentity(authenticationResult);
    const handledKeys = getHandledAccountInspectionResultKeys(
      [authenticationResult, newerResult],
      targetIdentityKey,
      authenticationResult.fileName,
      [{ name: 'codex.json', type: 'codex', authIndex: 'auth-1' }]
    );

    expect(handledKeys).toEqual([getAccountInspectionResultSnapshotKey(authenticationResult)]);
    expect(
      filterSuppressedAccountInspectionResults(
        [authenticationResult, newerResult],
        new Set(handledKeys)
      )
    ).toEqual([newerResult]);
  });

  it('allows a newer local inspection run even when synthetic result IDs are reused', () => {
    const handled: AccountInspectionResult = {
      id: -1,
      runId: 0,
      accountKey: 'codex.json',
      fileName: 'codex.json',
      displayAccount: 'codex@example.com',
      provider: 'codex',
      disabled: false,
      action: 'reauth',
      actionReason: 'expired token',
      statusCode: 401,
      isQuota: false,
      createdAtMs: 1_000,
      inspectionSource: 'local',
    };
    const newerRun: AccountInspectionResult = {
      ...handled,
      createdAtMs: 2_000,
    };
    const handledKeys = getHandledAccountInspectionResultKeys(
      [handled],
      getAuthFileCodexInspectionKeyForIdentity(handled),
      handled.fileName,
      [{ name: handled.fileName, type: 'codex' }]
    );

    expect(filterSuppressedAccountInspectionResults([handled], new Set(handledKeys))).toEqual([]);
    expect(filterSuppressedAccountInspectionResults([newerRun], new Set(handledKeys))).toEqual([
      newerRun,
    ]);
  });

  it('allows a newer server inspection result when run and result IDs are reused', () => {
    const handled: AccountInspectionResult = {
      id: 1,
      runId: 10,
      accountKey: 'codex.json',
      fileName: 'codex.json',
      displayAccount: 'codex@example.com',
      provider: 'codex',
      authIndex: 'auth-1',
      disabled: false,
      action: 'reauth',
      actionReason: 'expired token',
      statusCode: 401,
      isQuota: false,
      createdAtMs: 1_000,
      inspectionSource: 'server',
    };
    const newerRun: AccountInspectionResult = {
      ...handled,
      createdAtMs: 2_000,
    };
    const handledKeys = getHandledAccountInspectionResultKeys(
      [handled],
      getAuthFileCodexInspectionKeyForIdentity(handled),
      handled.fileName,
      [{ name: handled.fileName, type: 'codex', authIndex: 'auth-1' }]
    );

    expect(filterSuppressedAccountInspectionResults([handled], new Set(handledKeys))).toEqual([]);
    expect(filterSuppressedAccountInspectionResults([newerRun], new Set(handledKeys))).toEqual([
      newerRun,
    ]);
  });

  it('suppresses filename-only authentication results only for a unique current file', () => {
    const result: AccountInspectionResult = {
      id: 1,
      runId: 10,
      accountKey: 'shared.codex.json',
      fileName: 'shared.codex.json',
      displayAccount: 'shared@example.com',
      provider: 'codex',
      disabled: false,
      action: 'reauth',
      actionReason: 'expired token',
      statusCode: 401,
      isQuota: false,
      createdAtMs: 1_000,
      inspectionSource: 'server',
    };
    const targetIdentityKey = getAuthFileCodexInspectionKeyForIdentity({
      ...result,
      authIndex: 'auth-1',
    });

    expect(
      getHandledAccountInspectionResultKeys([result], targetIdentityKey, result.fileName, [
        { name: result.fileName, type: 'codex', authIndex: 'auth-1' },
      ])
    ).toEqual([getAccountInspectionResultSnapshotKey(result)]);
    expect(
      getHandledAccountInspectionResultKeys([result], targetIdentityKey, result.fileName, [
        { name: result.fileName, type: 'codex', authIndex: 'auth-1' },
        { name: result.fileName, type: 'codex', authIndex: 'auth-2' },
      ])
    ).toEqual([]);
  });

  it('normalizes Codex quota usage into remaining percent and risk status', () => {
    const files: AuthFileItem[] = [
      {
        name: 'codex-low.json',
        type: 'codex',
        authIndex: '1',
      },
    ];
    const rows = buildAccountRows(files, {
      ...emptyStores(),
      codexQuota: {
        'codex-low.json': {
          status: 'success',
          planType: 'plus',
          windows: [
            {
              id: 'weekly',
              label: 'Weekly',
              usedPercent: 87,
              resetLabel: 'Mon',
            },
          ],
        },
      },
    });

    expect(rows[0].quota.remainingPercent).toBe(13);
    expect(rows[0].quota.usedPercent).toBe(87);
    expect(rows[0].quota.status).toBe('low');
    expect(rows[0].planType).toBe('plus');
  });

  it.each([
    {
      label: 'Claude',
      file: { name: 'claude.json', type: 'claude', authIndex: 'auth-1' },
      stores: {
        ...emptyStores(),
        claudeQuota: {
          'claude.json': {
            status: 'success',
            fetchedAtMs: 2_000,
            windows: [{ id: 'weekly', label: 'Weekly', usedPercent: 25, resetLabel: 'Mon' }],
          },
        },
      },
    },
    {
      label: 'Antigravity',
      file: { name: 'antigravity.json', type: 'antigravity', authIndex: 'auth-1' },
      stores: {
        ...emptyStores(),
        antigravityQuota: {
          'antigravity.json': {
            status: 'success',
            fetchedAtMs: 2_000,
            groups: [
              {
                id: 'primary',
                label: 'Primary',
                buckets: [
                  {
                    id: 'weekly',
                    label: 'Weekly',
                    remainingFraction: 0.75,
                    resetTime: 'Mon',
                  },
                ],
              },
            ],
          },
        },
      },
    },
    {
      label: 'Kimi',
      file: { name: 'kimi.json', type: 'kimi', authIndex: 'auth-1' },
      stores: {
        ...emptyStores(),
        kimiQuota: {
          'kimi.json': {
            status: 'success',
            fetchedAtMs: 2_000,
            rows: [{ id: 'weekly', used: 25, limit: 100, resetHint: 'Mon' }],
          },
        },
      },
    },
    {
      label: 'xAI',
      file: { name: 'xai.json', type: 'xai', authIndex: 'auth-1' },
      stores: {
        ...emptyStores(),
        xaiQuota: {
          'xai.json': {
            status: 'success',
            fetchedAtMs: 2_000,
            billing: {
              periodType: 'weekly',
              usagePercent: 25,
              periodEnd: '2026-08-17T00:00:00Z',
              productUsage: [],
              monthlyLimitCents: null,
              usedCents: null,
              includedUsedCents: null,
              onDemandCapCents: null,
              onDemandUsedCents: null,
              onDemandUsedPercent: null,
              usedPercent: null,
            },
          },
        },
      },
    },
  ] satisfies Array<{ label: string; file: AuthFileItem; stores: AccountQuotaStores }>)(
    'propagates $label quota freshness so newer success retires old auth evidence',
    ({ file, stores }) => {
      const inspection: AccountInspectionResult = {
        id: 1,
        runId: 10,
        accountKey: file.name,
        fileName: file.name,
        displayAccount: file.name,
        provider: String(file.type ?? ''),
        authIndex: 'auth-1',
        disabled: false,
        action: 'reauth',
        actionReason: 'expired token',
        statusCode: 401,
        isQuota: false,
        createdAtMs: 1_000,
        inspectionSource: 'server',
      };
      const [row] = buildAccountRows([file], stores, [inspection]);

      expect(row.quota).toMatchObject({ status: 'ok', fetchedAtMs: 2_000 });
      expect(
        filterAccountRows([row], {
          provider: 'all',
          status: 'problem',
          plan: 'all',
          quotaBand: 'all',
          search: '',
        })
      ).toHaveLength(0);
      expect(buildAccountMetrics([row])).toMatchObject({ available: 1, needsAttention: 0 });
    }
  );

  it('reads the Codex plan from a nested ID token payload', () => {
    const [row] = buildAccountRows(
      [
        {
          name: 'nested-plan.codex.json',
          type: 'codex',
          metadata: {
            id_token: JSON.stringify({ plan_type: 'plus' }),
          },
        },
      ],
      emptyStores()
    );

    expect(row.planType).toBe('plus');
    expect(row.quota.planType).toBe('plus');
  });

  it('uses the latest recovery across equally exhausted Codex windows', () => {
    const earlierResetAtMs = Date.parse('2026-07-30T04:00:00Z');
    const laterResetAtMs = Date.parse('2026-07-30T06:00:00Z');
    const rows = buildAccountRows([{ name: 'codex.json', type: 'codex' }], {
      ...emptyStores(),
      codexQuota: {
        'codex.json': {
          status: 'success',
          windows: [
            {
              id: 'weekly-base',
              label: 'Weekly base',
              usedPercent: 100,
              resetLabel: '2026-07-30T04:00:00Z',
              resetAtMs: earlierResetAtMs,
              resetAccuracy: 'exact',
              modelScope: CODEX_MAIN_SCOPE,
            },
            {
              id: 'weekly-model',
              label: 'Weekly model',
              usedPercent: 100,
              resetLabel: '2026-07-30T06:00:00Z',
              resetAtMs: laterResetAtMs,
              resetAccuracy: 'exact',
              modelScope: CODEX_MAIN_SCOPE,
            },
          ],
        },
      },
    });

    expect(rows[0].quota).toMatchObject({
      status: 'exhausted',
      resetLabel: '2026-07-30T06:00:00Z',
      resetAtMs: laterResetAtMs,
      resetAccuracy: 'exact',
    });
  });

  it('does not promise recovery when an equally exhausted Codex window has no reset time', () => {
    const rows = buildAccountRows([{ name: 'codex.json', type: 'codex' }], {
      ...emptyStores(),
      codexQuota: {
        'codex.json': {
          status: 'success',
          windows: [
            {
              id: 'weekly-known',
              label: 'Weekly known',
              usedPercent: 100,
              resetLabel: '2026-07-30T04:00:00Z',
              resetAtMs: Date.parse('2026-07-30T04:00:00Z'),
              resetAccuracy: 'exact',
              modelScope: CODEX_MAIN_SCOPE,
            },
            {
              id: 'weekly-unknown',
              label: 'Weekly unknown',
              usedPercent: 100,
              resetLabel: '-',
              resetAtMs: null,
              resetAccuracy: 'unknown',
              modelScope: CODEX_MAIN_SCOPE,
            },
          ],
        },
      },
    });

    expect(rows[0].quota).toMatchObject({
      status: 'exhausted',
      resetLabel: '-',
      resetAtMs: null,
      resetAccuracy: 'unknown',
    });
  });

  it('rejects an out-of-range cached reset timestamp and recovers from its ISO label', () => {
    const resetAtMs = Date.parse('2026-07-30T04:00:00Z');
    const [row] = buildAccountRows([{ name: 'codex.json', type: 'codex' }], {
      ...emptyStores(),
      codexQuota: {
        'codex.json': {
          status: 'success',
          windows: [
            {
              id: 'weekly',
              label: 'Weekly',
              usedPercent: 100,
              resetLabel: '2026-07-30T04:00:00Z',
              resetAtMs: Number.MAX_VALUE,
              resetAccuracy: 'exact',
            },
          ],
        },
      },
    });

    expect(row.quota).toMatchObject({
      resetAtMs,
      resetAccuracy: 'unknown',
    });
  });

  it('keeps the last successful Codex windows visible after a refresh failure', () => {
    const rows = buildAccountRows([{ name: 'codex-stale.json', type: 'codex', authIndex: '1' }], {
      ...emptyStores(),
      codexQuota: {
        'codex-stale.json': {
          status: 'error',
          error: 'temporary failure',
          errorStatus: 503,
          fetchedAtMs: 1_000,
          failedAtMs: 2_000,
          windows: [
            {
              id: 'weekly',
              label: 'Weekly',
              usedPercent: 25,
              resetLabel: 'Mon',
            },
          ],
        },
      },
    });

    expect(rows[0].quota).toMatchObject({
      status: 'ok',
      remainingPercent: 75,
      error: 'temporary failure',
      errorStatus: 503,
      fetchedAtMs: 1_000,
      failedAtMs: 2_000,
    });
    expect(
      filterAccountRows(rows, {
        provider: 'all',
        status: 'problem',
        plan: 'all',
        quotaBand: 'all',
        search: '',
      })
    ).toHaveLength(1);
    expect(buildAccountMetrics(rows).available).toBe(0);
    expect(
      filterAccountRows(rows, {
        provider: 'all',
        status: 'available',
        plan: 'all',
        quotaBand: 'all',
        search: '',
      })
    ).toHaveLength(0);
  });

  it('keeps neutral quota refresh failures out of problem state and clears stale failures on success', () => {
    const files: AuthFileItem[] = [
      { name: 'neutral.json', type: 'codex', authIndex: 'neutral' },
      { name: 'recovered.json', type: 'codex', authIndex: 'recovered' },
    ];
    const rows = buildAccountRows(files, {
      ...emptyStores(),
      codexQuota: {
        'neutral.json': {
          status: 'error',
          error: 'context canceled',
          errorStatus: 499,
          fetchedAtMs: 1_000,
          failedAtMs: 2_000,
          windows: [{ id: 'weekly', label: 'Weekly', usedPercent: 25, resetLabel: 'Mon' }],
        },
        'recovered.json': {
          status: 'error',
          error: 'upstream unavailable',
          errorStatus: 503,
          fetchedAtMs: 1_000,
          failedAtMs: 2_000,
          windows: [{ id: 'weekly', label: 'Weekly', usedPercent: 25, resetLabel: 'Mon' }],
        },
      },
    });
    const neutralRow = rows.find((row) => row.fileName === 'neutral.json');
    const recoveredRow = rows.find((row) => row.fileName === 'recovered.json');
    expect(neutralRow).toBeDefined();
    expect(recoveredRow).toBeDefined();

    const baseFilters = {
      provider: 'all',
      plan: 'all',
      quotaBand: 'all' as const,
      search: '',
    };
    expect(filterAccountRows([neutralRow!], { ...baseFilters, status: 'problem' })).toHaveLength(0);
    expect(buildAccountMetrics([neutralRow!])).toMatchObject({
      available: 1,
      needsAttention: 0,
    });

    const requestEvidenceBySelectionKey = new Map([
      [recoveredRow!.selectionKey, { latestRequest: { timestamp_ms: 3_000, failed: false } }],
    ]);
    expect(
      filterAccountRows([recoveredRow!], {
        ...baseFilters,
        status: 'problem',
        requestEvidenceBySelectionKey,
      })
    ).toHaveLength(0);
    expect(
      filterAccountRows([recoveredRow!], {
        ...baseFilters,
        status: 'available',
        requestEvidenceBySelectionKey,
      })
    ).toHaveLength(1);
    expect(buildAccountMetrics([recoveredRow!], { requestEvidenceBySelectionKey })).toMatchObject({
      available: 1,
      needsAttention: 0,
    });
  });

  it('requires post-reauth request success for available metrics', () => {
    const file: AuthFileItem = {
      name: 'post-reauth.json',
      type: 'codex',
      authIndex: 'auth-1',
      account_id: 'space-a',
    };
    const selectionKey = getAuthFileSelectionKey(file);
    const [row] = buildAccountRows(
      [file],
      emptyStores(),
      undefined,
      undefined,
      undefined,
      new Map([[selectionKey, evidenceBoundary({ authenticationAtMs: 2_000 })]])
    );

    expect(
      buildAccountMetrics([row], {
        requestEvidenceBySelectionKey: new Map([
          [selectionKey, { latestRequest: { timestamp_ms: 1_000, failed: false } }],
        ]),
      })
    ).toMatchObject({ available: 0, unconfirmed: 1 });
    expect(
      buildAccountMetrics([row], {
        requestEvidenceBySelectionKey: new Map([
          [selectionKey, { latestRequest: { timestamp_ms: 3_000, failed: false } }],
        ]),
      })
    ).toMatchObject({ available: 1, unconfirmed: 0 });
  });

  it('marks observed Codex usage header quota and searches header diagnostics', () => {
    const rows = buildAccountRows(
      [
        {
          name: 'codex-observed.json',
          type: 'codex',
          authIndex: '2',
          account: 'observed@example.com',
        },
      ],
      {
        ...emptyStores(),
        codexQuota: {
          'codex-observed.json': {
            status: 'success',
            planType: 'plus',
            windows: [
              {
                id: 'usage-header-observed',
                label: 'Latest request',
                usedPercent: 100,
                resetLabel: '2026-06-25 10:00',
                modelScope: CODEX_MAIN_SCOPE,
              },
            ],
            observedFromUsageHeaders: true,
            observedModelScope: CODEX_MAIN_SCOPE,
            observedAtMs: 1000,
            observedTraceId: 'trace-observed',
            observedErrorKind: 'rate_limit',
            observedErrorCode: 'usage_limit',
            activeLimit: 'primary',
            rateLimitReachedType: 'primary',
          },
        },
      }
    );

    expect(rows[0].quota.source).toBe('observed-header');
    expect(rows[0].quota.status).toBe('exhausted');
    expect(rows[0].quota.observedQuotaAtMs).toBe(1000);
    expect(rows[0].quota.observedTraceId).toBe('trace-observed');
    expect(rows[0].quota.observedErrorCode).toBe('usage_limit');

    expect(
      filterAccountRows(rows, {
        provider: 'all',
        status: 'all',
        plan: 'all',
        quotaBand: 'all',
        search: 'trace-observed',
      }).map((row) => row.fileName)
    ).toEqual(['codex-observed.json']);

    expect(
      filterAccountRows(rows, {
        provider: 'all',
        status: 'all',
        plan: 'all',
        quotaBand: 'all',
        search: 'usage_limit',
      }).map((row) => row.fileName)
    ).toEqual(['codex-observed.json']);
  });

  it('supports wildcard search across account notes', () => {
    const rows = buildAccountRows(
      [
        {
          name: 'primary-codex.json',
          type: 'codex',
          account: 'primary@example.com',
          note: 'Production Team Alpha',
        },
        {
          name: 'backup-codex.json',
          type: 'codex',
          account: 'backup@example.com',
          note: 'Standby Team Beta',
        },
      ],
      emptyStores()
    );

    expect(
      filterAccountRows(rows, {
        provider: 'all',
        status: 'all',
        plan: 'all',
        quotaBand: 'all',
        search: 'prod*alpha',
      }).map((row) => row.fileName)
    ).toEqual(['primary-codex.json']);
  });

  it('builds selection keys with auth indexes for shared auth rows', () => {
    const rows = buildAccountRows(
      [
        { name: 'shared.codex.json', type: 'codex', authIndex: '0' },
        { name: 'plain.codex.json', type: 'codex' },
      ],
      emptyStores()
    );

    expect(rows[0].selectionKey).toBe('shared.codex.json\u00000');
    expect(rows[1].selectionKey).toBe(
      getAuthFileSelectionKey({ name: 'plain.codex.json', type: 'codex' })
    );
  });

  it('uses selection-key Codex quota overrides for shared auth rows', () => {
    const rows = buildAccountRows(
      [
        { name: 'shared.codex.json', type: 'codex', authIndex: '0' },
        { name: 'shared.codex.json', type: 'codex', authIndex: '1' },
      ],
      emptyStores(),
      undefined,
      {
        codexQuotaBySelectionKey: new Map<string, CodexQuotaState>([
          [
            'shared.codex.json\u00000',
            {
              status: 'success',
              windows: [
                {
                  id: 'a',
                  label: 'A',
                  usedPercent: 10,
                  resetLabel: 'A reset',
                  modelScope: CODEX_MAIN_SCOPE,
                },
              ],
            },
          ],
          [
            'shared.codex.json\u00001',
            {
              status: 'success',
              windows: [
                {
                  id: 'b',
                  label: 'B',
                  usedPercent: 90,
                  resetLabel: 'B reset',
                  modelScope: CODEX_MAIN_SCOPE,
                },
              ],
              observedFromUsageHeaders: true,
              observedModelScope: CODEX_MAIN_SCOPE,
              observedTraceId: 'trace-auth-index-1',
            },
          ],
        ]),
      }
    );

    expect(rows[0].quota.usedPercent).toBe(10);
    expect(rows[0].quota.source).toBe('cache');
    expect(rows[1].quota.usedPercent).toBe(90);
    expect(rows[1].quota.source).toBe('observed-header');
    expect(rows[1].quota.observedTraceId).toBe('trace-auth-index-1');
  });

  it('finds inspection targets exactly and only falls back for unique file names', () => {
    const sharedRows = buildAccountRows(
      [
        { name: 'shared.codex.json', type: 'codex', authIndex: '0' },
        { name: 'shared.codex.json', type: 'codex', authIndex: '1' },
      ],
      emptyStores()
    );
    const uniqueRows = buildAccountRows(
      [{ name: 'unique.codex.json', type: 'codex', authIndex: '2' }],
      emptyStores()
    );

    expect(
      findAccountRowForInspectionTarget(sharedRows, {
        fileName: 'shared.codex.json',
        authIndex: '1',
      })?.selectionKey
    ).toBe('shared.codex.json\u00001');
    expect(
      findAccountRowForInspectionTarget(sharedRows, {
        fileName: 'shared.codex.json',
        authIndex: null,
      })
    ).toBeNull();
    expect(
      findAccountRowForInspectionTarget(uniqueRows, {
        fileName: 'unique.codex.json',
        provider: 'codex',
        authIndex: null,
      })?.selectionKey
    ).toBe('unique.codex.json\u00002');
  });

  it('matches Codex inspection results by auth index for shared auth rows', () => {
    const inspection: AccountInspectionResult = {
      id: 10,
      runId: 1,
      accountKey: 'second',
      fileName: 'shared.codex.json',
      displayAccount: 'second@example.com',
      authIndex: '1',
      provider: 'codex',
      disabled: false,
      action: 'reauth',
      actionReason: 'expired',
      statusCode: 401,
      isQuota: false,
      quotaInventoryObserved: true,
      createdAtMs: 1000,
      inspectionSource: 'server',
    };
    const rows = buildAccountRows(
      [
        { name: 'shared.codex.json', type: 'codex', authIndex: '0' },
        { name: 'shared.codex.json', type: 'codex', authIndex: '1' },
      ],
      emptyStores(),
      [inspection]
    );

    expect(rows[0].inspection).toBeNull();
    expect(rows[1].inspection?.action).toBe('reauth');
    expect(rows[1].inspection?.statusCode).toBe(401);
    expect(rows[1].inspection?.source).toBe('server');
    expect(rows[1].inspection?.quotaInventoryObserved).toBe(true);
  });

  it('invalidates inspection evidence for only the targeted shared-file credential', () => {
    const files: AuthFileItem[] = [
      { name: 'shared.codex.json', type: 'codex', authIndex: '0' },
      { name: 'shared.codex.json', type: 'codex', authIndex: '1' },
    ];
    const inspections: AccountInspectionResult[] = files.map((file, index) => ({
      id: index + 1,
      runId: 1,
      accountKey: `account-${index}`,
      fileName: file.name,
      displayAccount: `account-${index}`,
      authIndex: String(file.authIndex),
      provider: 'codex',
      disabled: false,
      action: 'reauth',
      actionReason: 'expired',
      actionStatus: 'pending',
      statusCode: 401,
      isQuota: false,
      createdAtMs: 1_000,
      inspectionSource: 'server',
    }));
    const boundaries = new Map([
      [getAuthFileSelectionKey(files[0]), evidenceBoundary({ inspectionAtMs: 2_000 })],
    ]);
    const inspectionBySelectionKey = buildAccountInspectionBySelectionKey(
      files,
      inspections,
      boundaries
    );

    const rows = buildAccountRows(
      files,
      emptyStores(),
      inspections,
      undefined,
      inspectionBySelectionKey
    );

    expect(rows[0].inspection).toBeNull();
    expect(rows[1].inspection?.action).toBe('reauth');
  });

  it('suppresses inspection evidence while a post-mutation baseline is still pending', () => {
    const file: AuthFileItem = {
      name: 'pending-baseline.json',
      type: 'codex',
      authIndex: 'auth-1',
    };
    const inspection: AccountInspectionResult = {
      id: 1,
      runId: 9,
      accountKey: 'pending-baseline',
      fileName: file.name,
      displayAccount: 'pending@example.com',
      authIndex: 'auth-1',
      provider: 'codex',
      disabled: false,
      action: 'reauth',
      actionReason: 'expired',
      actionStatus: 'pending',
      statusCode: 401,
      isQuota: false,
      createdAtMs: Number.MAX_SAFE_INTEGER - 1,
      inspectionSource: 'server',
    };
    const boundaries = new Map([
      [getAuthFileSelectionKey(file), evidenceBoundary({ inspectionBaselinePending: true })],
    ]);

    expect(buildAccountInspectionBySelectionKey([file], [inspection], boundaries).size).toBe(0);
  });

  it('suppresses filename-only inspection evidence while its fallback baseline is pending', () => {
    const file: AuthFileItem = {
      name: 'pending-fallback.json',
      type: 'codex',
      authIndex: 'auth-1',
    };
    const inspection: AccountInspectionResult = {
      id: 1,
      runId: 9,
      accountKey: 'pending-fallback',
      fileName: file.name,
      displayAccount: 'pending@example.com',
      provider: 'codex',
      disabled: false,
      action: 'reauth',
      actionReason: 'expired',
      actionStatus: 'pending',
      statusCode: 401,
      isQuota: false,
      createdAtMs: Number.MAX_SAFE_INTEGER - 1,
      inspectionSource: 'server',
    };
    const boundaries = new Map([
      [
        getAuthFileSelectionKey(file),
        evidenceBoundary({ fallbackInspectionBaselinePending: true }),
      ],
    ]);

    expect(buildAccountInspectionBySelectionKey([file], [inspection], boundaries).size).toBe(0);
  });

  it('uses an exact status boundary without hiding a shared-file sibling', () => {
    const files: AuthFileItem[] = [
      {
        name: 'shared.codex.json',
        type: 'codex',
        authIndex: '0',
        disabled: false,
        statusMessage: 'token_expired',
      },
      {
        name: 'shared.codex.json',
        type: 'codex',
        authIndex: '1',
        disabled: false,
        statusMessage: 'token_expired',
      },
    ];
    const inspections: AccountInspectionResult[] = files.map((file, index) => ({
      id: index + 1,
      runId: 1,
      accountKey: `account-${index}`,
      fileName: file.name,
      displayAccount: `account-${index}`,
      authIndex: String(file.authIndex),
      provider: 'codex',
      disabled: true,
      action: 'reauth',
      actionReason: 'expired',
      actionStatus: 'pending',
      statusCode: 401,
      isQuota: false,
      createdAtMs: 1_000,
      inspectionSource: 'server',
    }));
    const statusBoundaries = new Map([
      [
        getAuthFileSelectionKey(files[0]),
        evidenceBoundary({
          localAtMs: 2_000,
          inspectionAtMs: 2_000,
          rawStatusMessages: ['token_expired'],
        }),
      ],
    ]);

    const rows = buildAccountRows(
      files,
      emptyStores(),
      inspections,
      undefined,
      undefined,
      undefined,
      statusBoundaries
    );

    expect(rows[0]).toMatchObject({ disabled: false, statusMessage: '', inspection: null });
    expect(rows[1]).toMatchObject({
      disabled: true,
      statusMessage: 'token_expired',
      inspection: expect.objectContaining({ action: 'reauth' }),
    });
  });

  it('allows later or changed raw status evidence past a status boundary', () => {
    const updatedFile: AuthFileItem = {
      name: 'status-updated.codex.json',
      type: 'codex',
      authIndex: 'updated',
      statusMessage: 'service_unavailable',
      updatedAtMs: 3_000,
    };
    const changedFile: AuthFileItem = {
      name: 'status-changed.codex.json',
      type: 'codex',
      authIndex: 'changed',
      statusMessage: 'service_unavailable',
    };
    const boundaries = new Map([
      [
        getAuthFileSelectionKey(updatedFile),
        evidenceBoundary({
          localAtMs: 2_000,
          rawStatusMessages: ['token_expired'],
        }),
      ],
      [
        getAuthFileSelectionKey(changedFile),
        evidenceBoundary({
          localAtMs: 2_000,
          rawStatusMessages: ['token_expired'],
        }),
      ],
    ]);

    const rows = buildAccountRows(
      [updatedFile, changedFile],
      emptyStores(),
      undefined,
      undefined,
      undefined,
      undefined,
      boundaries
    );

    expect(rows.map((row) => row.statusMessage)).toEqual([
      'service_unavailable',
      'service_unavailable',
    ]);
  });

  it('does not reattach reauth inspection evidence older than the credential refresh', () => {
    const file: AuthFileItem = {
      name: 'reauthorized.codex.json',
      type: 'codex',
      authIndex: 'reauthorized',
      last_refresh: 1_700_000_001_000,
    };
    const inspection: AccountInspectionResult = {
      id: 1,
      runId: 1,
      accountKey: 'reauthorized',
      fileName: file.name,
      displayAccount: 'reauthorized@example.com',
      authIndex: String(file.authIndex),
      provider: 'codex',
      disabled: false,
      action: 'reauth',
      actionReason: 'expired',
      actionStatus: 'pending',
      statusCode: 401,
      isQuota: false,
      createdAtMs: 1_700_000_001_000,
      inspectionSource: 'server',
    };

    const rows = buildAccountRows([file], emptyStores(), [inspection]);

    expect(rows[0].inspection).toBeNull();
  });

  it('uses a nearby OAuth file write to cover second-precision refresh timestamps', () => {
    const refreshAtMs = 1_700_000_001_000;
    const file: AuthFileItem = {
      name: 'reauthorized.xai.json',
      type: 'xai',
      authIndex: 'reauthorized-xai',
      last_refresh: refreshAtMs / 1000,
      modtime: refreshAtMs + 800,
    };
    const inspection: AccountInspectionResult = {
      id: 2,
      runId: 1,
      accountKey: 'reauthorized-xai',
      fileName: file.name,
      displayAccount: 'reauthorized-xai@example.com',
      authIndex: String(file.authIndex),
      provider: 'xai',
      disabled: false,
      action: 'reauth',
      actionReason: 'expired',
      actionStatus: 'pending',
      statusCode: 401,
      isQuota: false,
      createdAtMs: refreshAtMs + 500,
      inspectionSource: 'server',
    };

    const [row] = buildAccountRows([file], emptyStores(), [inspection]);

    expect(row.inspection).toBeNull();
  });

  it('does not treat a much later file edit as OAuth refresh completion', () => {
    const refreshAtMs = 1_700_000_001_000;
    const file: AuthFileItem = {
      name: 'edited-after-refresh.xai.json',
      type: 'xai',
      authIndex: 'edited-after-refresh',
      last_refresh: refreshAtMs / 1000,
      modtime: refreshAtMs + 10_000,
    };
    const inspection: AccountInspectionResult = {
      id: 3,
      runId: 1,
      accountKey: 'edited-after-refresh',
      fileName: file.name,
      displayAccount: 'edited-after-refresh@example.com',
      authIndex: String(file.authIndex),
      provider: 'xai',
      disabled: false,
      action: 'reauth',
      actionReason: 'expired',
      actionStatus: 'pending',
      statusCode: 401,
      isQuota: false,
      createdAtMs: refreshAtMs + 5_000,
      inspectionSource: 'server',
    };

    const [row] = buildAccountRows([file], emptyStores(), [inspection]);

    expect(row.inspection).toEqual(expect.objectContaining({ action: 'reauth' }));
  });

  it('lets same-timestamp healthy inspection state supersede raw disabled state', () => {
    const observedAtMs = 1_700_000_001_000;
    const file: AuthFileItem = {
      name: 'enabled-at-tie.codex.json',
      type: 'codex',
      authIndex: 'enabled-at-tie',
      disabled: true,
      updatedAtMs: observedAtMs,
    };
    const inspection: AccountInspectionResult = {
      id: 1,
      runId: 1,
      accountKey: 'enabled-at-tie',
      fileName: file.name,
      displayAccount: 'enabled@example.com',
      authIndex: String(file.authIndex),
      provider: 'codex',
      disabled: false,
      action: 'enable',
      actionReason: 'recovered',
      actionStatus: 'success',
      executedAction: 'enable',
      statusCode: 200,
      isQuota: false,
      createdAtMs: observedAtMs,
      inspectionSource: 'server',
    };

    const [row] = buildAccountRows([file], emptyStores(), [inspection]);

    expect(row.disabled).toBe(false);
  });

  it('matches same-file inspection results by canonical identity without auth indexes', () => {
    const files: AuthFileItem[] = [
      {
        id: 'runtime-first',
        name: 'shared.codex.json',
        type: 'codex',
        provider: 'codex',
        account: 'first@example.com',
      },
      {
        id: 'runtime-second',
        name: 'shared.codex.json',
        type: 'codex',
        provider: 'codex',
        account: 'second@example.com',
      },
    ];
    const inspection: AccountInspectionResult = {
      id: 11,
      runId: 2,
      accountKey: 'second',
      fileName: 'shared.codex.json',
      displayAccount: 'second@example.com',
      runtimeId: 'runtime-second',
      provider: 'codex',
      accountSnapshot: 'second@example.com',
      disabled: false,
      action: 'reauth',
      actionReason: 'expired',
      statusCode: 401,
      isQuota: false,
      createdAtMs: 1000,
      inspectionSource: 'server',
    };
    const rows = buildAccountRows(files, emptyStores(), [inspection]);

    expect(rows[0].inspection).toBeNull();
    expect(rows[1].inspection).toMatchObject({ action: 'reauth', statusCode: 401 });
    expect(
      findAccountRowForInspectionTarget(rows, {
        fileName: 'shared.codex.json',
        runtimeId: 'runtime-second',
        provider: 'codex',
        accountSnapshot: 'second@example.com',
      })?.selectionKey
    ).toBe(rows[1].selectionKey);
  });

  it('uses missing-auth-index inspection results only for unique file names', () => {
    const inspection: AccountInspectionResult = {
      id: -1,
      runId: 0,
      accountKey: 'local-only',
      fileName: 'shared.codex.json',
      displayAccount: 'local@example.com',
      provider: 'codex',
      disabled: false,
      action: 'disable',
      actionReason: 'local reason',
      statusCode: 429,
      isQuota: true,
      createdAtMs: 1000,
      inspectionSource: 'local',
    };
    const uniqueRows = buildAccountRows(
      [{ name: 'shared.codex.json', type: 'codex', authIndex: '0' }],
      emptyStores(),
      [inspection]
    );
    const sharedRows = buildAccountRows(
      [
        { name: 'shared.codex.json', type: 'codex', authIndex: '0' },
        { name: 'shared.codex.json', type: 'codex', authIndex: '1' },
      ],
      emptyStores(),
      [inspection]
    );

    expect(uniqueRows[0].inspection).toMatchObject({ action: 'disable', source: 'local' });
    expect(sharedRows[0].inspection).toBeNull();
    expect(sharedRows[1].inspection).toBeNull();
  });

  it('uses the latest safely matched inspection across exact and filename-only identities', () => {
    const file: AuthFileItem = {
      name: 'unique.codex.json',
      type: 'codex',
      authIndex: 'auth-1',
    };
    const exactOlder: AccountInspectionResult = {
      id: 1,
      runId: 10,
      accountKey: file.name,
      fileName: file.name,
      displayAccount: file.name,
      provider: 'codex',
      authIndex: 'auth-1',
      disabled: false,
      action: 'reauth',
      actionReason: 'expired token',
      statusCode: 401,
      isQuota: false,
      createdAtMs: 1_000,
      inspectionSource: 'server',
    };
    const filenameOnlyNewer: AccountInspectionResult = {
      ...exactOlder,
      id: 2,
      runId: 11,
      authIndex: undefined,
      action: 'keep',
      actionReason: 'healthy',
      statusCode: 200,
      errorKind: 'inference_healthy',
      createdAtMs: 2_000,
    };

    const [row] = buildAccountRows([file], emptyStores(), [exactOlder, filenameOnlyNewer]);

    expect(row.inspection).toMatchObject({
      action: 'keep',
      statusCode: 200,
      createdAtMs: 2_000,
    });
  });

  it('surfaces diagnostic-only Codex header snapshots without quota cache', () => {
    const snapshot: UsageHeaderSnapshot = {
      event_hash: 'diagnostic-only',
      timestamp_ms: 1700000000000,
      header_trace_id: 'trace-diagnostic-only',
      header_error_kind: 'rate_limit',
      header_error_code: 'usage_limit_reached',
    };
    const rows = buildAccountRows(
      [
        {
          name: 'codex-diagnostic.json',
          type: 'codex',
          authIndex: '2',
          account: 'diagnostic@example.com',
        },
      ],
      emptyStores(),
      undefined,
      {
        codexHeaderSnapshotBySelectionKey: new Map<string, UsageHeaderSnapshot>([
          ['codex-diagnostic.json\u00002', snapshot],
        ]),
      }
    );

    expect(rows[0].quota.source).toBe('observed-header');
    expect(rows[0].quota.status).toBe('unknown');
    expect(rows[0].quota.usedPercent).toBeNull();
    expect(rows[0].quota.observedAtMs).toBe(1700000000000);
    expect(rows[0].quota.observedQuotaAtMs).toBeUndefined();
    expect(rows[0].quota.observedTraceId).toBe('trace-diagnostic-only');
    expect(rows[0].quota.observedErrorKind).toBe('rate_limit');
    expect(rows[0].quota.observedErrorCode).toBe('usage_limit_reached');

    expect(
      filterAccountRows(rows, {
        provider: 'all',
        status: 'all',
        plan: 'all',
        quotaBand: 'all',
        search: 'trace-diagnostic-only',
      }).map((row) => row.fileName)
    ).toEqual(['codex-diagnostic.json']);

    expect(
      filterAccountRows(rows, {
        provider: 'all',
        status: 'all',
        plan: 'all',
        quotaBand: 'all',
        search: 'usage_limit_reached',
      }).map((row) => row.fileName)
    ).toEqual(['codex-diagnostic.json']);
  });

  it('uses Antigravity quota buckets and subscription plan in account rows', () => {
    const rows = buildAccountRows([{ name: 'antigravity.json', type: 'antigravity' }], {
      ...emptyStores(),
      antigravityQuota: {
        'antigravity.json': {
          status: 'success',
          subscription: {
            plan: 'pro',
            tierName: 'Pro',
            tierId: 'pro',
          },
          groups: [
            {
              id: 'primary',
              label: 'Primary',
              buckets: [
                {
                  id: 'weekly',
                  label: 'Weekly',
                  remainingFraction: 0.42,
                  resetTime: '07-11 12:00',
                },
              ],
            },
          ],
        },
      },
    });

    expect(rows[0].planType).toBe('pro');
    expect(rows[0].quota.remainingPercent).toBe(42);
    expect(rows[0].quota.usedPercent).toBe(58);
    expect(rows[0].quota.resetLabel).toBe('07-11 12:00');
  });

  it('normalizes legacy yearless reset labels against the next recovery year', () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(2026, 11, 31, 23, 0, 0, 0));
    try {
      const [row] = buildAccountRows([{ name: 'legacy-reset.json', type: 'codex' }], {
        ...emptyStores(),
        codexQuota: {
          'legacy-reset.json': {
            status: 'success',
            windows: [
              {
                id: 'weekly',
                label: 'Weekly',
                usedPercent: 50,
                resetLabel: '01/01 01:30',
              },
            ],
          },
        },
      });

      expect(row.quota.resetAtMs).toBe(new Date(2027, 0, 1, 1, 30, 0, 0).getTime());
      expect(row.quota.resetAccuracy).toBe('unknown');
    } finally {
      vi.useRealTimers();
    }
  });

  it('keeps Antigravity available while at least one model group can serve requests', () => {
    const rows = buildAccountRows(
      [
        { name: 'codex-healthy.json', type: 'codex' },
        {
          name: 'antigravity-mixed.json',
          type: 'antigravity',
          status: 'cooldown',
          statusMessage: 'Gemini 5-hour pool exhausted; waiting for Antigravity reset',
        },
      ],
      {
        ...emptyStores(),
        codexQuota: {
          'codex-healthy.json': {
            status: 'success',
            windows: [{ id: 'weekly', label: 'Weekly', usedPercent: 20, resetLabel: 'Mon' }],
          },
        },
        antigravityQuota: {
          'antigravity-mixed.json': {
            status: 'success',
            groups: [
              {
                id: 'gemini',
                label: 'Gemini models',
                buckets: [
                  {
                    id: 'five-hour',
                    label: 'Five hour',
                    remainingFraction: 0,
                    resetTime: '2026-07-30T02:00:00Z',
                  },
                  {
                    id: 'weekly',
                    label: 'Weekly',
                    remainingFraction: 0.44,
                    resetTime: '2026-08-02T02:00:00Z',
                  },
                ],
              },
              {
                id: 'claude-gpt',
                label: 'Claude and GPT models',
                buckets: [
                  {
                    id: 'five-hour',
                    label: 'Five hour',
                    remainingFraction: 0.82,
                    resetTime: '2026-07-30T01:00:00Z',
                  },
                  {
                    id: 'weekly',
                    label: 'Weekly',
                    remainingFraction: 0.66,
                    resetTime: '2026-08-04T02:00:00Z',
                  },
                ],
              },
            ],
          },
        },
      }
    );
    const row = rows.find((candidate) => candidate.fileName === 'antigravity-mixed.json');

    expect(row?.quota).toMatchObject({
      status: 'ok',
      remainingPercent: 66,
      usedPercent: 34,
      resetLabel: '2026-07-30T02:00:00Z',
      groupedAvailabilityState: 'partial',
    });
    expect(buildAccountMetrics(rows)).toMatchObject({
      available: 1,
      needsAttention: 0,
      quotaRisk: 1,
    });
    expect(
      filterAccountRows(rows, {
        provider: 'all',
        status: 'available',
        plan: 'all',
        quotaBand: 'all',
        search: '',
      }).map((candidate) => candidate.fileName)
    ).toEqual(['codex-healthy.json', 'antigravity-mixed.json']);
    expect(
      filterAccountRows(rows, {
        provider: 'all',
        status: 'problem',
        plan: 'all',
        quotaBand: 'all',
        search: '',
      })
    ).toHaveLength(0);
    expect(sortAccountRows(rows).map((candidate) => candidate.fileName)).toEqual([
      'antigravity-mixed.json',
      'codex-healthy.json',
    ]);
  });

  it('uses the latest blocking window when summarizing an unavailable Antigravity group', () => {
    const [row] = buildAccountRows([{ name: 'antigravity-recovery.json', type: 'antigravity' }], {
      ...emptyStores(),
      antigravityQuota: {
        'antigravity-recovery.json': {
          status: 'success',
          groups: [
            {
              id: 'gemini',
              label: 'Gemini models',
              buckets: [
                {
                  id: 'five-hour',
                  label: 'Five hour',
                  remainingFraction: 0,
                  resetTime: '2026-07-30T02:00:00Z',
                },
                {
                  id: 'weekly',
                  label: 'Weekly',
                  remainingFraction: 0,
                  resetTime: '2026-08-02T02:00:00Z',
                },
              ],
            },
            {
              id: 'claude-gpt',
              label: 'Claude and GPT models',
              buckets: [
                {
                  id: 'weekly',
                  label: 'Weekly',
                  remainingFraction: 0.66,
                  resetTime: '2026-08-04T02:00:00Z',
                },
              ],
            },
          ],
        },
      },
    });

    expect(row.quota).toMatchObject({
      status: 'ok',
      remainingPercent: 66,
      resetLabel: '2026-08-02T02:00:00Z',
    });
  });

  it('marks Antigravity exhausted only after every known model group is exhausted', () => {
    const rows = buildAccountRows([{ name: 'antigravity-empty.json', type: 'antigravity' }], {
      ...emptyStores(),
      antigravityQuota: {
        'antigravity-empty.json': {
          status: 'success',
          groups: [
            {
              id: 'gemini',
              label: 'Gemini models',
              buckets: [
                { id: 'weekly', label: 'Weekly', remainingFraction: 0, resetTime: 'later' },
              ],
            },
            {
              id: 'claude-gpt',
              label: 'Claude and GPT models',
              buckets: [
                { id: 'weekly', label: 'Weekly', remainingFraction: 0, resetTime: 'later' },
              ],
            },
          ],
        },
      },
    });

    expect(rows[0].quota.status).toBe('exhausted');
    expect(rows[0].quota.remainingPercent).toBe(0);
  });

  it('keeps credential update time separate and uses the latest update signal', () => {
    const [row] = buildAccountRows(
      [
        {
          name: 'updated.json',
          type: 'codex',
          updatedAtMs: 1_700_000_000_000,
          modified: 1_700_000_100_000,
          lastRefresh: 1_700_000_200_000,
        },
      ],
      emptyStores()
    );

    expect(row.updatedAtMs).toBe(1_700_000_200_000);
  });

  it('uses the tightest Kimi quota row for account summary and reset label', () => {
    const rows = buildAccountRows([{ name: 'kimi.json', type: 'kimi' }], {
      ...emptyStores(),
      kimiQuota: {
        'kimi.json': {
          status: 'success',
          rows: [
            {
              id: 'daily',
              label: 'Daily',
              used: 1,
              limit: 10,
              resetHint: '1d',
            },
            {
              id: 'weekly',
              label: 'Weekly',
              used: 9,
              limit: 10,
              resetHint: '6d',
            },
          ],
        },
      },
    });

    expect(rows[0].quota.remainingPercent).toBe(10);
    expect(rows[0].quota.usedPercent).toBe(90);
    expect(rows[0].quota.resetLabel).toBe('6d');
    expect(rows[0].quota.status).toBe('low');
  });

  it('keeps xAI account available while pay-as-you-go quota remains', () => {
    const rows = buildAccountRows([{ name: 'xai.json', type: 'xai' }], {
      ...emptyStores(),
      xaiQuota: {
        'xai.json': {
          status: 'success',
          billing: {
            periodType: 'monthly',
            usagePercent: null,
            productUsage: [],
            monthlyLimitCents: 10_000,
            usedCents: 12_500,
            includedUsedCents: 10_000,
            onDemandCapCents: 5_000,
            onDemandUsedCents: 2_500,
            onDemandUsedPercent: 50,
            billingPeriodEnd: '2026-07-31T00:00:00Z',
            usedPercent: 100,
          },
        },
      },
    });

    expect(rows[0].quota.remainingPercent).toBeCloseTo(16.667, 2);
    expect(rows[0].quota.usedPercent).toBeCloseTo(83.333, 2);
    expect(rows[0].quota.resetLabel).toBe('2026-07-31T00:00:00Z');
    expect(rows[0].quota.status).toBe('low');
  });

  it('keeps an official-API-only xAI credential available without inventing quota', () => {
    const rows = buildAccountRows([{ name: 'paid-xai.json', type: 'xai' }], {
      ...emptyStores(),
      xaiQuota: {
        'paid-xai.json': {
          status: 'success',
          billing: {
            periodType: 'unknown',
            usagePercent: null,
            productUsage: [],
            monthlyLimitCents: null,
            usedCents: null,
            includedUsedCents: null,
            onDemandCapCents: null,
            onDemandUsedCents: null,
            onDemandUsedPercent: null,
            usedPercent: null,
            officialApiHealth: {
              source: 'api.x.ai/v1/me',
              userId: 'user-1',
              teamId: 'team-1',
              teamBlocked: false,
            },
          },
        },
      },
    });

    expect(rows[0].quota).toMatchObject({
      status: 'unknown',
      remainingPercent: null,
      usedPercent: null,
    });
    expect(rows[0].quota).not.toHaveProperty('error');
    expect(buildAccountMetrics(rows)).toMatchObject({ available: 0, unconfirmed: 1 });
    expect(
      filterAccountRows(rows, {
        provider: 'all',
        status: 'available',
        plan: 'all',
        quotaBand: 'all',
        search: '',
      })
    ).toHaveLength(0);
  });

  it('does not treat a non-authentication keep result as confirmed availability', () => {
    const rows = buildAccountRows([{ name: 'codex.json', type: 'codex' }], emptyStores(), [
      {
        id: 1,
        runId: 1,
        accountKey: 'codex.json',
        fileName: 'codex.json',
        displayAccount: 'codex.json',
        provider: 'codex',
        disabled: false,
        action: 'keep',
        actionReason: 'upstream unavailable',
        actionStatus: 'none',
        statusCode: 503,
        usedPercent: undefined,
        isQuota: false,
        errorKind: 'upstream_error',
        createdAtMs: 2_000,
        inspectionSource: 'server',
      },
    ]);

    expect(buildAccountMetrics(rows)).toMatchObject({ available: 0, unconfirmed: 1 });
    expect(
      filterAccountRows(rows, {
        provider: 'all',
        status: 'available',
        plan: 'all',
        quotaBand: 'all',
        search: '',
      })
    ).toHaveLength(0);
  });

  it('keeps inspection error-kind authentication failures consistent across metrics and filters', () => {
    const rows = buildAccountRows(
      [{ name: 'codex-auth-error.json', type: 'codex' }],
      {
        ...emptyStores(),
        codexQuota: {
          'codex-auth-error.json': {
            status: 'success',
            fetchedAtMs: 1_000,
            windows: [{ id: 'weekly', label: 'Weekly', usedPercent: 20, resetLabel: 'Mon' }],
          },
        },
      },
      [
        {
          id: 1,
          runId: 1,
          accountKey: 'codex-auth-error.json',
          fileName: 'codex-auth-error.json',
          displayAccount: 'codex-auth-error.json',
          provider: 'codex',
          disabled: false,
          action: 'keep',
          actionReason: 'token rejected',
          actionStatus: 'success',
          statusCode: 503,
          usedPercent: undefined,
          isQuota: false,
          errorKind: 'authentication_error',
          createdAtMs: 2_000,
          inspectionSource: 'server',
        },
      ]
    );
    const filters = {
      provider: 'all',
      plan: 'all',
      quotaBand: 'all' as const,
      search: '',
    };

    expect(buildAccountMetrics(rows)).toMatchObject({ available: 0, needsAttention: 1 });
    expect(filterAccountRows(rows, { ...filters, status: 'available' })).toHaveLength(0);
    expect(filterAccountRows(rows, { ...filters, status: 'problem' })).toHaveLength(1);
  });

  it('uses xAI weekly credits when they are the tightest quota window', () => {
    const rows = buildAccountRows([{ name: 'xai.json', type: 'xai' }], {
      ...emptyStores(),
      xaiQuota: {
        'xai.json': {
          status: 'success',
          billing: {
            periodType: 'weekly',
            usagePercent: 92,
            periodEnd: '2026-07-08T00:00:00Z',
            productUsage: [{ product: 'Grok Code Fast', usagePercent: 92 }],
            monthlyLimitCents: 10_000,
            usedCents: 2_000,
            includedUsedCents: 2_000,
            onDemandCapCents: null,
            onDemandUsedCents: null,
            onDemandUsedPercent: null,
            billingPeriodEnd: '2026-07-31T00:00:00Z',
            usedPercent: 20,
          },
        },
      },
    });

    expect(rows[0].quota.remainingPercent).toBe(8);
    expect(rows[0].quota.usedPercent).toBe(92);
    expect(rows[0].quota.resetLabel).toBe('2026-07-08T00:00:00Z');
    expect(rows[0].quota.status).toBe('low');
  });

  it('uses xAI product usage when period usage is not available', () => {
    const rows = buildAccountRows([{ name: 'xai.json', type: 'xai' }], {
      ...emptyStores(),
      xaiQuota: {
        'xai.json': {
          status: 'success',
          billing: {
            periodType: 'weekly',
            usagePercent: null,
            periodEnd: '2026-07-08T00:00:00Z',
            productUsage: [{ product: 'Grok Code Fast', usagePercent: 100 }],
            monthlyLimitCents: 10_000,
            usedCents: 2_000,
            includedUsedCents: 2_000,
            onDemandCapCents: null,
            onDemandUsedCents: null,
            onDemandUsedPercent: null,
            billingPeriodEnd: '2026-07-31T00:00:00Z',
            usedPercent: 20,
          },
        },
      },
    });

    expect(rows[0].quota.remainingPercent).toBe(0);
    expect(rows[0].quota.usedPercent).toBe(100);
    expect(rows[0].quota.resetLabel).toBe('2026-07-08T00:00:00Z');
    expect(rows[0].quota.status).toBe('exhausted');
  });

  it('keeps cached Codex quota source while appending header diagnostics', () => {
    const rows = buildAccountRows(
      [
        {
          name: 'codex-cache.json',
          type: 'codex',
          authIndex: 'auth-cache',
        },
      ],
      {
        ...emptyStores(),
        codexQuota: {
          'codex-cache.json': {
            status: 'success',
            planType: 'plus',
            windows: [
              {
                id: 'weekly',
                label: 'Weekly',
                usedPercent: 25,
                resetLabel: 'Mon',
              },
            ],
          },
        },
      },
      undefined,
      {
        codexHeaderSnapshotBySelectionKey: new Map<string, UsageHeaderSnapshot>([
          [
            'codex-cache.json\u0000auth-cache',
            {
              event_hash: 'cache-diagnostic',
              timestamp_ms: 1700000000100,
              header_trace_id: 'trace-cache-diagnostic',
              header_error_code: 'quota_warning',
            },
          ],
        ]),
      }
    );

    expect(rows[0].quota.source).toBe('cache');
    expect(rows[0].quota.usedPercent).toBe(25);
    expect(rows[0].quota.observedQuotaAtMs).toBeUndefined();
    expect(rows[0].quota.observedTraceId).toBe('trace-cache-diagnostic');
    expect(rows[0].quota.observedErrorCode).toBe('quota_warning');
  });

  it('builds account metrics from quota, disabled state, usage, and inspection results', () => {
    const files: AuthFileItem[] = [
      {
        name: 'codex-low.json',
        type: 'codex',
        recent_requests: [{ success: 9, failed: 1 }],
      },
      {
        name: 'codex-disabled.json',
        type: 'codex',
        disabled: true,
        recent_requests: [{ success: 0, failed: 2 }],
      },
    ];
    const inspection: AccountInspectionResult[] = [
      {
        id: 10,
        runId: 1,
        accountKey: 'codex-low.json',
        fileName: 'codex-low.json',
        displayAccount: 'codex-low.json',
        provider: 'codex',
        disabled: false,
        action: 'disable',
        actionReason: 'low quota',
        actionStatus: 'pending',
        statusCode: 200,
        usedPercent: 96,
        isQuota: true,
        createdAtMs: 1000,
        inspectionSource: 'server',
      },
    ];

    const rows = buildAccountRows(
      files,
      {
        ...emptyStores(),
        codexQuota: {
          'codex-low.json': {
            status: 'success',
            windows: [{ id: 'weekly', label: 'Weekly', usedPercent: 96, resetLabel: 'Mon' }],
          },
        },
      },
      inspection
    );

    expect(rows[0].inspection?.action).toBe('disable');
    expect(rows[1].quota.status).toBe('disabled');

    const metrics = buildAccountMetrics(rows);
    expect(metrics.total).toBe(2);
    expect(metrics.needsAttention).toBe(1);
    expect(metrics.quotaRisk).toBe(0);
    expect(metrics.disabled).toBe(1);
    expect(metrics.unconfirmed).toBe(0);
    expect(metrics.available).toBe(0);
    expect(metrics.needsInspectionAction).toBe(1);

    const successfulRequestEvidence = new Map([
      [rows[0].selectionKey, { latestRequest: { timestamp_ms: 2_000, failed: false } }],
    ]);
    expect(
      buildAccountMetrics(rows, {
        requestEvidenceBySelectionKey: successfulRequestEvidence,
      })
    ).toMatchObject({
      needsAttention: 1,
      quotaRisk: 0,
      needsInspectionAction: 1,
    });
    expect(
      filterAccountRows(rows, {
        provider: 'all',
        status: 'inspection',
        plan: 'all',
        quotaBand: 'all',
        search: '',
        requestEvidenceBySelectionKey: successfulRequestEvidence,
      })
    ).toHaveLength(1);

    const neutralRequestEvidence = new Map([
      [
        rows[0].selectionKey,
        {
          latestRequest: {
            timestamp_ms: 2_000,
            failed: true,
            fail_status_code: 499,
          },
        },
      ],
    ]);
    expect(
      buildAccountMetrics(rows, {
        requestEvidenceBySelectionKey: neutralRequestEvidence,
      }).needsInspectionAction
    ).toBe(1);
  });

  it('removes stale auth header diagnostics from health filters while retaining quota diagnostics', () => {
    const [baseRow] = buildAccountRows(
      [{ name: 'header-diagnostic.json', type: 'codex' }],
      emptyStores()
    );
    const authDiagnosticRow: typeof baseRow = {
      ...baseRow,
      quota: {
        ...baseRow.quota,
        status: 'ok',
        observedAtMs: 1_000,
        observedErrorKind: 'auth',
        observedErrorCode: 'invalid_api_key',
      },
    };
    const quotaDiagnosticRow: typeof baseRow = {
      ...authDiagnosticRow,
      quota: {
        ...authDiagnosticRow.quota,
        observedErrorKind: 'rate_limit',
        observedErrorCode: 'quota_exceeded',
      },
    };
    const requestEvidenceBySelectionKey = new Map([
      [baseRow.selectionKey, { latestRequest: { timestamp_ms: 2_000, failed: false } }],
    ]);
    const filters = {
      provider: 'all',
      plan: 'all',
      quotaBand: 'all' as const,
      search: '',
      requestEvidenceBySelectionKey,
    };

    expect(
      buildAccountMetrics([authDiagnosticRow], { requestEvidenceBySelectionKey })
    ).toMatchObject({ available: 1, needsAttention: 0 });
    expect(
      filterAccountRows([authDiagnosticRow], { ...filters, status: 'available' })
    ).toHaveLength(1);
    expect(filterAccountRows([authDiagnosticRow], { ...filters, status: 'problem' })).toHaveLength(
      0
    );
    expect(
      buildAccountMetrics([quotaDiagnosticRow], { requestEvidenceBySelectionKey })
    ).toMatchObject({ available: 0, needsAttention: 0, quotaRisk: 1 });
    expect(filterAccountRows([quotaDiagnosticRow], { ...filters, status: 'problem' })).toHaveLength(
      0
    );

    const runtimeQuotaRow: typeof baseRow = {
      ...baseRow,
      statusMessage: 'quota exceeded',
      updatedAtMs: 1_000,
      raw: {
        ...baseRow.raw,
        status_code: 429,
        status_message: 'quota exceeded',
      },
    };
    expect(buildAccountMetrics([runtimeQuotaRow])).toMatchObject({
      available: 0,
      needsAttention: 0,
      quotaRisk: 1,
    });
    expect(filterAccountRows([runtimeQuotaRow], { ...filters, status: 'problem' })).toHaveLength(0);
    expect(buildAccountMetrics([runtimeQuotaRow], { requestEvidenceBySelectionKey })).toMatchObject(
      { available: 1, needsAttention: 0, quotaRisk: 0 }
    );
    expect(filterAccountRows([runtimeQuotaRow], { ...filters, status: 'available' })).toHaveLength(
      1
    );

    const requestQuotaEvidenceBySelectionKey = new Map([
      [
        baseRow.selectionKey,
        {
          latestRequest: {
            timestamp_ms: 2_000,
            failed: true,
            fail_status_code: 429,
            fail_summary: 'rate limit exceeded',
          },
        },
      ],
    ]);
    expect(
      buildAccountMetrics([baseRow], {
        requestEvidenceBySelectionKey: requestQuotaEvidenceBySelectionKey,
      })
    ).toMatchObject({ available: 0, needsAttention: 0, quotaRisk: 1 });
    expect(
      filterAccountRows([baseRow], {
        ...filters,
        status: 'available',
        requestEvidenceBySelectionKey: requestQuotaEvidenceBySelectionKey,
      })
    ).toHaveLength(0);
    expect(
      filterAccountRows([baseRow], {
        ...filters,
        status: 'problem',
        requestEvidenceBySelectionKey: requestQuotaEvidenceBySelectionKey,
      })
    ).toHaveLength(0);

    for (const quota of [
      { rateLimitReachedType: 'primary' },
      { spendControlReached: true },
      { creditsOverageLimitReached: true },
    ]) {
      const explicitQuotaRow: typeof baseRow = {
        ...baseRow,
        quota: { ...baseRow.quota, ...quota },
      };
      expect(buildAccountMetrics([explicitQuotaRow])).toMatchObject({
        available: 0,
        needsAttention: 0,
        quotaRisk: 1,
      });
    }

    const neutralRuntimeRow: typeof baseRow = {
      ...baseRow,
      statusMessage: 'context canceled',
      updatedAtMs: 2_000,
      raw: {
        ...baseRow.raw,
        status_code: 499,
        status_message: 'context canceled',
      },
      quota: {
        ...baseRow.quota,
        status: 'ok',
      },
    };
    expect(
      buildAccountMetrics([neutralRuntimeRow], { requestEvidenceBySelectionKey })
    ).toMatchObject({ available: 1, needsAttention: 0 });
    expect(filterAccountRows([neutralRuntimeRow], { ...filters, status: 'problem' })).toHaveLength(
      0
    );

    const singleTransientRuntimeRow: typeof baseRow = {
      ...baseRow,
      statusMessage: 'upstream unavailable',
      updatedAtMs: 2_000,
      raw: {
        ...baseRow.raw,
        status_code: 503,
        status_message: 'upstream unavailable',
      },
      quota: {
        ...baseRow.quota,
        status: 'ok',
      },
    };
    expect(buildAccountMetrics([singleTransientRuntimeRow])).toMatchObject({
      available: 1,
      needsAttention: 0,
      quotaRisk: 0,
    });
    expect(
      filterAccountRows([singleTransientRuntimeRow], { ...filters, status: 'problem' })
    ).toHaveLength(0);
  });

  it('builds an exclusive six-card status summary with operational context', () => {
    const rows = buildAccountRows(
      [
        { name: 'available.json', type: 'codex', authIndex: 'available' },
        { name: 'attention.json', type: 'codex', authIndex: 'attention' },
        { name: 'low.json', type: 'codex', authIndex: 'low' },
        { name: 'cooldown.json', type: 'codex', authIndex: 'cooldown' },
        { name: 'disabled.json', type: 'codex', authIndex: 'disabled', disabled: true },
        { name: 'unconfirmed.json', type: 'gemini', authIndex: 'unconfirmed' },
      ],
      {
        ...emptyStores(),
        codexQuota: {
          'available.json': {
            status: 'success',
            windows: [{ id: 'weekly', label: 'Weekly', usedPercent: 25, resetLabel: 'Mon' }],
          },
          'low.json': {
            status: 'success',
            windows: [{ id: 'weekly', label: 'Weekly', usedPercent: 90, resetLabel: 'Mon' }],
          },
        },
      }
    );
    const byName = new Map(rows.map((row) => [row.fileName, row]));
    const attentionKey = byName.get('attention.json')?.selectionKey ?? '';
    const cooldownKey = byName.get('cooldown.json')?.selectionKey ?? '';
    const disabledKey = byName.get('disabled.json')?.selectionKey ?? '';

    const operationalContext = {
      pendingActionsByRowKey: new Map([
        [attentionKey, [{ id: 1 }]],
        [disabledKey, [{ id: 2 }]],
      ]),
      quotaCooldownsByRowKey: new Map([
        [cooldownKey, [{ id: 3 }]],
        [disabledKey, [{ id: 4 }]],
      ]),
    };
    const metrics = buildAccountMetrics(rows, operationalContext);

    expect(metrics).toEqual({
      total: 6,
      available: 1,
      needsAttention: 1,
      quotaRisk: 2,
      disabled: 1,
      unconfirmed: 1,
      needsInspectionAction: 0,
    });
    expect(
      metrics.available +
        metrics.needsAttention +
        metrics.quotaRisk +
        metrics.disabled +
        metrics.unconfirmed
    ).toBe(metrics.total);
    expect(
      filterAccountRows(rows, {
        provider: 'all',
        status: 'unconfirmed',
        plan: 'all',
        quotaBand: 'all',
        search: '',
        ...operationalContext,
      }).map((row) => row.selectionKey)
    ).toEqual([byName.get('unconfirmed.json')?.selectionKey]);
  });

  it('filters rows by quota band and search text', () => {
    const rows = buildAccountRows(
      [
        { name: 'codex-low.json', type: 'codex', email: 'low@example.com' },
        { name: 'claude-ok.json', type: 'claude', email: 'ok@example.com' },
      ],
      {
        ...emptyStores(),
        codexQuota: {
          'codex-low.json': {
            status: 'success',
            windows: [{ id: 'weekly', label: 'Weekly', usedPercent: 90, resetLabel: 'Mon' }],
          },
        },
        claudeQuota: {
          'claude-ok.json': {
            status: 'success',
            windows: [{ id: 'weekly', label: 'Weekly', usedPercent: 25, resetLabel: 'Mon' }],
          },
        },
      }
    );

    expect(
      filterAccountRows(rows, {
        provider: 'all',
        status: 'all',
        plan: 'all',
        quotaBand: 'lt20',
        search: '',
      }).map((row) => row.fileName)
    ).toEqual(['codex-low.json']);

    expect(
      filterAccountRows(rows, {
        provider: 'all',
        status: 'all',
        plan: 'all',
        quotaBand: 'all',
        search: 'ok@example',
      }).map((row) => row.fileName)
    ).toEqual(['claude-ok.json']);
  });

  it('filters rows by credential-scoped Codex status evidence', () => {
    const weeklyQuota: CodexQuotaState = {
      status: 'success',
      windows: [
        {
          id: 'weekly',
          label: 'Weekly',
          usedPercent: 100,
          resetLabel: 'Mon',
        },
      ],
    };
    const rows = buildAccountRows(
      [
        { name: 'weekly.json', type: 'codex', authIndex: 'weekly' },
        { name: 'reauth.json', type: 'codex', authIndex: 'reauth' },
      ],
      emptyStores()
    );
    const codexStatusBySelectionKey = new Map([
      [rows[0].selectionKey, getAuthFileCodexStatus(rows[0].raw, weeklyQuota)],
      [
        rows[1].selectionKey,
        getAuthFileCodexStatus(rows[1].raw, undefined, {
          fileName: rows[1].fileName,
          authIndex: rows[1].authIndex,
          statusCode: 401,
          action: 'reauth',
        }),
      ],
    ]);

    expect(
      filterAccountRows(rows, {
        provider: 'all',
        status: 'weekly_limited',
        plan: 'all',
        quotaBand: 'all',
        search: '',
        codexStatusBySelectionKey,
      }).map((row) => row.fileName)
    ).toEqual(['weekly.json']);
    expect(
      filterAccountRows(rows, {
        provider: 'all',
        status: 'reauth',
        plan: 'all',
        quotaBand: 'all',
        search: '',
        codexStatusBySelectionKey,
      }).map((row) => row.fileName)
    ).toEqual(['reauth.json']);
    expect(
      filterAccountRows(rows, {
        provider: 'all',
        status: 'reauth',
        plan: 'all',
        quotaBand: 'all',
        search: '',
        codexStatusBySelectionKey,
        requestEvidenceBySelectionKey: new Map([
          [
            rows[1].selectionKey,
            {
              latestRequest: {
                timestamp_ms: 2_000,
                failed: true,
                fail_status_code: 429,
              },
            },
          ],
        ]),
      })
    ).toHaveLength(0);
    for (const authenticatedRequest of [
      { timestamp_ms: 3_000, failed: false },
      { timestamp_ms: 3_000, failed: true, fail_status_code: 429 },
    ]) {
      expect(
        filterAccountRows(rows, {
          provider: 'all',
          status: 'reauth',
          plan: 'all',
          quotaBand: 'all',
          search: '',
          codexStatusBySelectionKey,
          requestEvidenceBySelectionKey: new Map([
            [
              rows[1].selectionKey,
              {
                recentRequests: [
                  { timestamp_ms: 5_000, failed: true, fail_status_code: 503 },
                  { timestamp_ms: 4_000, failed: true, fail_status_code: 502 },
                  authenticatedRequest,
                  { timestamp_ms: 2_000, failed: true, fail_status_code: 401 },
                ],
              },
            ],
          ]),
        })
      ).toHaveLength(0);
    }
    expect(
      filterAccountRows(rows, {
        provider: 'all',
        status: 'reauth',
        plan: 'all',
        quotaBand: 'all',
        search: '',
        codexStatusBySelectionKey,
        requestEvidenceBySelectionKey: new Map([
          [rows[1].selectionKey, { latestRequest: { timestamp_ms: 2_000, failed: false } }],
        ]),
      })
    ).toHaveLength(0);
    expect(
      filterAccountRows(rows, {
        provider: 'all',
        status: 'reauth',
        plan: 'all',
        quotaBand: 'all',
        search: '',
        codexStatusBySelectionKey,
        requestEvidenceBySelectionKey: new Map([
          [
            rows[1].selectionKey,
            {
              latestRequest: {
                timestamp_ms: 2_000,
                failed: true,
                fail_status_code: 499,
              },
            },
          ],
        ]),
      }).map((row) => row.fileName)
    ).toEqual(['reauth.json']);
    expect(
      filterAccountRows(rows, {
        provider: 'all',
        status: 'quota_limited',
        plan: 'all',
        quotaBand: 'all',
        search: '',
      })
    ).toHaveLength(0);
  });

  it('exposes unknown plans and orders Codex plans by tier with unknown last', () => {
    const rows = buildAccountRows(
      [
        { name: 'pro.json', type: 'codex', planType: 'pro' },
        { name: 'pro-lite.json', type: 'codex', planType: 'prolite' },
        { name: 'team.json', type: 'codex', planType: 'team' },
        { name: 'plus.json', type: 'codex', planType: 'plus' },
        { name: 'free.json', type: 'codex', planType: 'free' },
        { name: 'enterprise.json', type: 'codex', planType: 'enterprise' },
        { name: 'unknown.json', type: 'codex' },
      ],
      emptyStores()
    );
    const plusRow = rows.find((row) => row.fileName === 'plus.json');
    if (!plusRow) throw new Error('Plus plan row not found');
    plusRow.planType = ' plus ';

    expect(getPlanOptions(rows)).toEqual([
      { value: 'enterprise', label: 'Enterprise' },
      { value: 'free', label: 'Free' },
      { value: 'plus', label: 'Plus' },
      { value: 'pro_5x', label: 'Pro 5x' },
      { value: 'pro_20x', label: 'Pro 20x' },
      { value: 'team', label: 'Team' },
      { value: 'unknown', label: 'Unknown plan' },
    ]);
    expect(
      sortAccountRows(rows, { key: 'plan', direction: 'asc' }).map((row) => row.fileName)
    ).toEqual([
      'enterprise.json',
      'free.json',
      'plus.json',
      'team.json',
      'pro-lite.json',
      'pro.json',
      'unknown.json',
    ]);
    expect(
      sortAccountRows(rows, { key: 'plan', direction: 'desc' }).map((row) => row.fileName)
    ).toEqual([
      'pro.json',
      'pro-lite.json',
      'team.json',
      'plus.json',
      'free.json',
      'enterprise.json',
      'unknown.json',
    ]);
    expect(
      filterAccountRows(rows, {
        provider: 'all',
        status: 'all',
        plan: 'plus',
        quotaBand: 'all',
        search: '',
      }).map((row) => row.fileName)
    ).toEqual(['plus.json']);
  });

  it('reads non-Codex planType values from nested token metadata', () => {
    const [row] = buildAccountRows(
      [
        {
          name: 'claude.json',
          type: 'claude',
          id_token: { planType: 'plan_pro' },
        },
      ],
      emptyStores()
    );

    expect(row.planType).toBe('plan_pro');
    expect(row.canonicalPlanType).toBe('pro');
  });

  it('aggregates Codex plan aliases by canonical identity for filtering', () => {
    const rows = buildAccountRows(
      [
        { name: 'prolite.json', type: 'codex', planType: 'prolite' },
        { name: 'pro-lite.json', type: 'codex', planType: 'pro-lite' },
        { name: 'pro_lite.json', type: 'codex', planType: 'pro_lite' },
      ],
      emptyStores()
    );

    expect(getPlanOptions(rows)).toEqual([{ value: 'pro_5x', label: 'Pro 5x' }]);
    expect(getPlanOptionLabel(rows, 'pro-lite')).toBe('Pro 5x');
    expect(
      filterAccountRows(rows, {
        provider: 'all',
        status: 'all',
        plan: 'pro_5x',
        quotaBand: 'all',
        search: '',
      }).map((row) => row.fileName)
    ).toEqual(['prolite.json', 'pro-lite.json', 'pro_lite.json']);
  });

  it('sorts rows by priority, recent requests, and reset label', () => {
    const rows = buildAccountRows(
      [
        {
          name: 'low.json',
          type: 'codex',
          priority: -1,
          createdAtMs: 1000,
          recent_requests: [{ success: 1, failed: 0 }],
        },
        {
          name: 'middle.json',
          type: 'codex',
          priority: 2,
          createdAtMs: 3000,
          recent_requests: [{ success: 3, failed: 2 }],
        },
        {
          name: 'high.json',
          type: 'codex',
          priority: 10,
          createdAtMs: 2000,
          recent_requests: [{ success: 2, failed: 1 }],
        },
      ],
      {
        ...emptyStores(),
        codexQuota: {
          'low.json': {
            status: 'success',
            windows: [{ id: 'weekly', label: 'Weekly', usedPercent: 90, resetLabel: '2026-01-10' }],
          },
          'middle.json': {
            status: 'success',
            windows: [{ id: 'weekly', label: 'Weekly', usedPercent: 40, resetLabel: '2026-01-02' }],
          },
          'high.json': {
            status: 'success',
            windows: [{ id: 'weekly', label: 'Weekly', usedPercent: 10, resetLabel: '-' }],
          },
        },
      }
    );

    expect(
      sortAccountRows(rows, { key: 'priority', direction: 'desc' }).map((row) => row.fileName)
    ).toEqual(['high.json', 'middle.json', 'low.json']);
    expect(
      sortAccountRows(rows, { key: 'recent', direction: 'desc' }).map((row) => row.fileName)
    ).toEqual(['middle.json', 'high.json', 'low.json']);
    expect(
      sortAccountRows(rows, { key: 'reset', direction: 'asc' }).map((row) => row.fileName)
    ).toEqual(['middle.json', 'low.json', 'high.json']);
    expect(
      sortAccountRows(rows, { key: 'quota', direction: 'desc' }).map((row) => row.fileName)
    ).toEqual(['high.json', 'middle.json', 'low.json']);
    expect(
      sortAccountRows(rows, { key: 'quota', direction: 'asc' }).map((row) => row.fileName)
    ).toEqual(['low.json', 'middle.json', 'high.json']);
    expect(
      sortAccountRows(rows, { key: 'created', direction: 'desc' }).map((row) => row.fileName)
    ).toEqual(['middle.json', 'high.json', 'low.json']);
    expect(
      sortAccountRows(rows, { key: 'created', direction: 'asc' }).map((row) => row.fileName)
    ).toEqual(['low.json', 'high.json', 'middle.json']);
  });

  it('sorts the name column by account label instead of credential file name', () => {
    const rows = buildAccountRows(
      [
        { name: 'a-file.json', type: 'codex', account: 'Zulu Account' },
        { name: 'z-file.json', type: 'codex', account: 'Alpha Account' },
      ],
      emptyStores()
    );

    expect(
      sortAccountRows(rows, { key: 'name', direction: 'asc' }).map((row) => row.fileName)
    ).toEqual(['z-file.json', 'a-file.json']);
  });

  it('uses a credential replacement boundary to suppress stale raw status messages', () => {
    const file: AuthFileItem = {
      name: 'stale-status.codex.json',
      type: 'codex',
      authIndex: 'stale-status',
      statusMessage: 'token_expired',
      updatedAtMs: 1_700_000_000_000,
    };
    const selectionKey = getAuthFileSelectionKey(file);
    const [row] = buildAccountRows(
      [file],
      emptyStores(),
      undefined,
      undefined,
      undefined,
      new Map([
        [
          selectionKey,
          evidenceBoundary({
            localAtMs: 1_600_000_000_000,
            rawStatusAtMs: 1_700_000_000_000,
            rawStatusMessages: ['token_expired'],
          }),
        ],
      ])
    );

    expect(row.statusMessage).toBe('');
  });

  it('supersedes a stale raw status message and all matching HTTP status codes', () => {
    const rawStatusAtMs = 1_700_000_001_000;
    const authenticationAtMs = rawStatusAtMs + 1_000;
    const file: AuthFileItem = {
      name: 'stale-status-code.codex.json',
      type: 'codex',
      authIndex: 'stale-status-code',
      statusMessage: 'token_expired',
      status_code: 401,
      error_status: 401,
      updatedAtMs: rawStatusAtMs,
    };
    const [row] = buildAccountRows(
      [file],
      emptyStores(),
      undefined,
      undefined,
      undefined,
      new Map([
        [
          getAuthFileSelectionKey(file),
          evidenceBoundary({
            authenticationAtMs,
            rawStatusAtMs,
            rawStatusMessages: ['token_expired'],
            rawStatusCodes: [401],
          }),
        ],
      ])
    );

    expect(row).toMatchObject({
      statusMessage: '',
      rawCredentialStatusSuperseded: true,
      authenticationAtMs,
    });
  });

  it('keeps the same HTTP 401 when its raw snapshot is newer than authentication recovery', () => {
    const authenticationAtMs = 1_700_000_001_000;
    const file: AuthFileItem = {
      name: 'new-status-code.codex.json',
      type: 'codex',
      authIndex: 'new-status-code',
      statusMessage: 'token_expired',
      status_code: 401,
      error_status: 401,
      updatedAtMs: authenticationAtMs + 1_000,
    };
    const [row] = buildAccountRows(
      [file],
      emptyStores(),
      undefined,
      undefined,
      undefined,
      new Map([
        [
          getAuthFileSelectionKey(file),
          evidenceBoundary({
            localAtMs: authenticationAtMs + 5_000,
            authenticationAtMs,
            rawStatusAtMs: authenticationAtMs - 1_000,
            rawStatusMessages: ['token_expired'],
            rawStatusCodes: [401],
          }),
        ],
      ])
    );

    expect(row).toMatchObject({
      statusMessage: 'token_expired',
      rawCredentialStatusSuperseded: false,
    });
  });

  it('does not hide a changed raw snapshot when its timestamp predates authentication recovery', () => {
    const authenticationAtMs = 1_700_000_002_000;
    const file: AuthFileItem = {
      name: 'changed-status-code.codex.json',
      type: 'codex',
      authIndex: 'changed-status-code',
      statusMessage: 'invalid_token',
      status_code: 403,
      error_status: 403,
      updatedAtMs: authenticationAtMs - 1_000,
    };
    const [row] = buildAccountRows(
      [file],
      emptyStores(),
      undefined,
      undefined,
      undefined,
      new Map([
        [
          getAuthFileSelectionKey(file),
          evidenceBoundary({
            authenticationAtMs,
            rawStatusAtMs: authenticationAtMs - 1_000,
            rawStatusMessages: ['token_expired'],
            rawStatusCodes: [401],
          }),
        ],
      ])
    );

    expect(row).toMatchObject({
      statusMessage: 'invalid_token',
      rawCredentialStatusSuperseded: false,
    });
  });

  it('does not restore timestamp-only suppression after a recovery boundary releases raw values', () => {
    const authenticationAtMs = 1_700_000_002_000;
    const file: AuthFileItem = {
      name: 'released-status-code.codex.json',
      type: 'codex',
      authIndex: 'released-status-code',
      statusMessage: 'invalid_token',
      status_code: 403,
      updatedAtMs: authenticationAtMs - 1_000,
    };
    const [row] = buildAccountRows(
      [file],
      emptyStores(),
      undefined,
      undefined,
      undefined,
      new Map([
        [
          getAuthFileSelectionKey(file),
          evidenceBoundary({ authenticationAtMs, rawStatusAtMs: 0 }),
        ],
      ])
    );

    expect(row).toMatchObject({
      statusMessage: 'invalid_token',
      rawCredentialStatusSuperseded: false,
    });
  });

  it('supersedes a code-only raw credential status snapshot', () => {
    const rawStatusAtMs = 1_700_000_001_000;
    const file: AuthFileItem = {
      name: 'code-only-status.codex.json',
      type: 'codex',
      authIndex: 'code-only-status',
      statusMessage: '',
      status_code: 401,
      error_status: 401,
      updatedAtMs: rawStatusAtMs,
    };
    const [row] = buildAccountRows(
      [file],
      emptyStores(),
      undefined,
      undefined,
      undefined,
      new Map([
        [
          getAuthFileSelectionKey(file),
          evidenceBoundary({
            authenticationAtMs: rawStatusAtMs + 1_000,
            rawStatusAtMs,
            rawStatusMessages: [],
            rawStatusCodes: [401],
          }),
        ],
      ])
    );

    expect(row).toMatchObject({ statusMessage: '', rawCredentialStatusSuperseded: true });
  });

  it('uses the completed mutation time when a refreshed credential repeats the stale status', () => {
    const file: AuthFileItem = {
      name: 'reauthorized-status.codex.json',
      type: 'codex',
      authIndex: 'reauthorized-status',
      statusMessage: 'token_expired',
      updatedAtMs: 1_700_000_005_000,
    };
    const selectionKey = getAuthFileSelectionKey(file);
    const [row] = buildAccountRows(
      [file],
      emptyStores(),
      undefined,
      undefined,
      undefined,
      new Map([
        [
          selectionKey,
          evidenceBoundary({
            localAtMs: 1_700_000_010_000,
            rawStatusAtMs: 1_700_000_000_000,
            rawStatusMessages: ['token_expired'],
          }),
        ],
      ])
    );

    expect(row.statusMessage).toBe('');
  });
  it('suppresses raw status at the inclusive credential-refresh boundary', () => {
    const refreshedAtMs = 1_700_000_010_000;
    const file: AuthFileItem = {
      name: 'refresh-tie.codex.json',
      type: 'codex',
      authIndex: 'refresh-tie',
      statusMessage: 'token_expired',
      updatedAtMs: refreshedAtMs,
      last_refresh: refreshedAtMs,
    };

    const [row] = buildAccountRows([file], emptyStores());

    expect(row.statusMessage).toBe('');
  });
  it('does not let cached healthy quota hide an undated raw authentication failure', () => {
    const file: AuthFileItem = {
      name: 'undated-auth-failure.codex.json',
      type: 'codex',
      authIndex: 'undated-auth-failure',
      statusMessage: 'token_expired',
    };
    const [row] = buildAccountRows(
      [file],
      scopeTestQuotaStores([file], {
        ...emptyStores(),
        codexQuota: {
          [file.name]: {
            status: 'success',
            windows: [],
            quotaInventoryObserved: true,
            fetchedAtMs: 2_000,
          },
        },
      })
    );

    expect(row.statusMessage).toBe('token_expired');
  });
  it('treats an explicitly empty Codex inventory as healthy quota evidence', () => {
    const [row] = buildAccountRows([{ name: 'codex-empty.json', type: 'codex' }], {
      ...emptyStores(),
      codexQuota: {
        'codex-empty.json': {
          status: 'success',
          windows: [],
          quotaInventoryObserved: true,
          observedAtMs: 1_700_000_000_000,
        },
      },
    });

    expect(row.quota).toMatchObject({
      status: 'ok',
      remainingPercent: null,
      usedPercent: null,
      source: 'cache',
      observedAtMs: 1_700_000_000_000,
    });
  });

  it('keeps cross-provider canonical plan filtering mutually exclusive', () => {
    const rows = buildAccountRows(
      [
        { name: 'codex-pro.json', type: 'codex', planType: 'pro' },
        { name: 'codex-prolite.json', type: 'codex', planType: 'prolite' },
        { name: 'claude-pro.json', type: 'claude', id_token: { planType: 'plan_pro' } },
        { name: 'antigravity-pro.json', type: 'antigravity', planType: 'pro' },
      ],
      emptyStores()
    );

    const planValues = getPlanOptions(rows).map((option) => option.value);
    expect(planValues).toContain('pro');
    expect(planValues).toContain('pro_20x');
    expect(planValues).toContain('pro_5x');
    expect(getPlanOptions(rows)).toEqual(
      expect.arrayContaining([
        { value: 'pro', label: 'Pro' },
        { value: 'pro_20x', label: 'Pro 20x' },
        { value: 'pro_5x', label: 'Pro 5x' },
      ])
    );

    const baseFilters = {
      provider: 'all',
      status: 'all' as const,
      quotaBand: 'all' as const,
      search: '',
    };
    expect(
      filterAccountRows(rows, { ...baseFilters, plan: 'pro' }).map((row) => row.fileName)
    ).toEqual(['claude-pro.json', 'antigravity-pro.json']);
    expect(
      filterAccountRows(rows, { ...baseFilters, plan: 'pro_20x' }).map((row) => row.fileName)
    ).toEqual(['codex-pro.json']);
    expect(
      filterAccountRows(rows, { ...baseFilters, plan: 'pro_5x' }).map((row) => row.fileName)
    ).toEqual(['codex-prolite.json']);
  });

  it('uses a canonical filter label independent of provider row order', () => {
    const rows = buildAccountRows(
      [
        { name: 'claude-pro.json', type: 'claude', id_token: { planType: 'plan_pro' } },
        { name: 'antigravity-pro.json', type: 'antigravity', planType: 'pro' },
      ],
      emptyStores()
    );
    const zhT = ((key: string, options?: { defaultValue?: string }) => {
      if (key === 'plans.claude.pro') return '专业版';
      if (key === 'plans.antigravity.pro') return 'Pro';
      return options?.defaultValue ?? key;
    }) as TFunction;

    expect(getPlanOptions(rows, zhT)).toEqual([{ value: 'pro', label: 'Pro' }]);
    expect(getPlanOptions([...rows].reverse(), zhT)).toEqual([
      { value: 'pro', label: 'Pro' },
    ]);
  });

  it('keeps a selected canonical pro filter stable when rows change', () => {
    const initialRows = buildAccountRows(
      [
        { name: 'claude-pro.json', type: 'claude', id_token: { planType: 'plan_pro' } },
        { name: 'codex-pro.json', type: 'codex', planType: 'pro' },
      ],
      emptyStores()
    );
    const updatedRows = initialRows.filter((row) => row.provider === 'codex');

    expect(getPlanOptionValue(initialRows, 'pro')).toBe('pro');
    expect(getPlanOptionValue(updatedRows, 'pro')).toBe('pro');
    expect(getPlanOptionLabel(updatedRows, 'pro')).toBe('Pro');
    expect(
      filterAccountRows(updatedRows, {
        provider: 'all',
        status: 'all',
        plan: 'pro',
        quotaBand: 'all',
        search: '',
      })
    ).toEqual([]);
    expect(
      filterAccountRows(updatedRows, {
        provider: 'all',
        status: 'all',
        plan: 'pro_20x',
        quotaBand: 'all',
        search: '',
      }).map((row) => row.fileName)
    ).toEqual(['codex-pro.json']);
  });

  it('keeps a stale known plan label without remapping it to another provider', () => {
    const initialRows = buildAccountRows(
      [
        { name: 'claude-pro.json', type: 'claude', id_token: { planType: 'plan_pro' } },
        { name: 'codex-pro.json', type: 'codex', planType: 'pro' },
      ],
      emptyStores()
    );
    const updatedRows = initialRows.filter((row) => row.provider === 'codex');

    expect(getPlanOptionValue(initialRows, 'pro')).toBe('pro');
    expect(getPlanOptionValue(updatedRows, 'pro')).toBe('pro');
    expect(getPlanOptionLabel(updatedRows, 'pro')).toBe('Pro');
    expect(getPlanOptionLabel(updatedRows, 'pro')).not.toBe('Pro 20x');
  });

  it('normalizes legacy raw plan filter aliases without using current rows', () => {
    const rows: AccountRow[] = [];
    expect(getPlanOptionValue(rows, 'prolite')).toBe('pro_5x');
    expect(getPlanOptionValue(rows, 'pro-lite')).toBe('pro_5x');
    expect(getPlanOptionValue(rows, 'pro_lite')).toBe('pro_5x');
    expect(getPlanOptionValue(rows, 'self_serve_business_prolite')).toBe(
      'business_premium_5x'
    );
    expect(getPlanOptionValue(rows, 'enterprise_cbp_automation')).toBe('enterprise_automation');
  });

  it('scopes same-named unknown plans to their providers', () => {
    const rows = buildAccountRows(
      [
        { name: 'antigravity-future.json', type: 'antigravity', planType: 'future_plan_x' },
        { name: 'kimi-future.json', type: 'kimi', planType: 'future_plan_x' },
      ],
      emptyStores()
    );

    expect(getPlanOptions(rows)).toEqual([
      { value: 'unknown:antigravity:future_plan_x', label: 'future_plan_x' },
      { value: 'unknown:kimi:future_plan_x', label: 'future_plan_x' },
    ]);
  });

  it('uses the current raw label for a scoped unknown plan', () => {
    const rows = buildAccountRows(
      [{ name: 'antigravity-future.json', type: 'antigravity', planType: 'Antigravity Future' }],
      emptyStores()
    );

    expect(getPlanOptionLabel(rows, 'unknown:antigravity:antigravity future')).toBe(
      'Antigravity Future'
    );
  });

  it('falls back to a readable scoped unknown plan label after its row disappears', () => {
    expect(
      getPlanOptionLabel([], 'unknown:antigravity:antigravity future')
    ).toBe('antigravity future');
    expect(getPlanOptionLabel([], 'unknown:provider:future:premium')).toBe('future:premium');
    expect(getPlanOptionLabel([], 'unknown:provider:future:premium')).not.toContain(
      'unknown:provider:'
    );
  });

  it('keeps the unknown plan filter label stable regardless of row order', () => {
    const zhT = ((key: string, options?: { defaultValue?: string }) =>
      key === 'auth_files.codex_plan_filter_unknown' ? '未知套餐' : (options?.defaultValue ?? key)) as TFunction;

    const baseFile = { type: 'codex' } as AuthFileItem;
    const nullPlanRow = buildAccountRows(
      [{ ...baseFile, name: 'missing-plan.json' }],
      emptyStores()
    )[0]!;
    const explicitUnknownRow = buildAccountRows(
      [{ ...baseFile, name: 'explicit-unknown.json', planType: 'unknown' }],
      emptyStores()
    )[0]!;

    for (const ordered of [
      [nullPlanRow, explicitUnknownRow],
      [explicitUnknownRow, nullPlanRow],
    ]) {
      const options = getPlanOptions(ordered, zhT);
      expect(options).toHaveLength(1);
      expect(options[0]).toEqual({ value: 'unknown', label: '未知套餐' });
    }
  });
});
