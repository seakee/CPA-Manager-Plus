import type { TFunction } from 'i18next';
import { describe, expect, it } from 'vitest';
import type { CodexQuotaState } from '@/types';
import { CODEX_SPARK_MODEL_ID } from '@/utils/quota/codexQuota';
import {
  ANTIGRAVITY_CONFIG,
  buildObservedCodexQuotaState,
  buildQuotaFailureState,
  CLAUDE_CONFIG,
  CODEX_CONFIG,
  getCodexQuotaStoreKey,
  KIMI_CONFIG,
  getSortedCodexResetCreditExpiries,
  resolveQuotaDisplayState,
  XAI_CONFIG,
} from './quotaConfigs';

describe('getCodexQuotaStoreKey', () => {
  it('preserves indexed keys and separates same-file rows without auth indexes', () => {
    expect(
      getCodexQuotaStoreKey({
        name: 'shared.json',
        type: 'codex',
        authIndex: 'auth-1',
      })
    ).toBe('shared.json::auth-1');

    const first = getCodexQuotaStoreKey({
      id: 'runtime-1',
      name: 'shared.json',
      type: 'codex',
      account_id: 'account-1',
    });
    const second = getCodexQuotaStoreKey({
      id: 'runtime-2',
      name: 'shared.json',
      type: 'codex',
      account: 'second@example.com',
    });

    expect(first).not.toBe(second);
  });

  it('uses the same credential identity contract for every quota provider', () => {
    const file = {
      name: 'shared.json',
      provider: 'claude',
      authIndex: 'auth-1',
    };
    const configs = [CLAUDE_CONFIG, ANTIGRAVITY_CONFIG, CODEX_CONFIG, KIMI_CONFIG, XAI_CONFIG];

    configs.forEach((config) => {
      expect(config.getStoreKey?.(file)).toBe('shared.json::auth-1');
      expect(config.buildLoadingState(file)).toMatchObject({
        authFileKey: 'shared.json::auth-1',
        authFileName: 'shared.json',
        authIndex: 'auth-1',
        authFileIdentityVerified: true,
      });
    });
  });
});

type TestQuotaState = {
  status: 'idle' | 'loading' | 'success' | 'error';
  errorStatus?: number;
  fetchedAtMs?: number;
  observedAtMs?: number;
  observedFromUsageHeaders?: boolean;
  windows?: unknown[];
};

type FailureTestState = {
  status: 'success' | 'error';
  fetchedAtMs?: number;
  windows?: Array<{ id: string; usedPercent: number }>;
  error?: string;
  lastError?: string;
  errorStatus?: number;
  failedAtMs?: number;
};

describe('buildQuotaFailureState', () => {
  it('lets providers preserve the last successful quota while recording refresh failure', () => {
    const activeState: FailureTestState = {
      status: 'success' as const,
      fetchedAtMs: 1_000,
      windows: [{ id: 'weekly', usedPercent: 25 }],
    };
    const result = buildQuotaFailureState<FailureTestState, unknown>(
      {
        buildErrorState: (message: string, status?: number) => ({
          status: 'error' as const,
          error: message,
          errorStatus: status,
        }),
        buildFailureState: (message, status, _file, previous, failedAtMs) => ({
          ...previous,
          status: 'success' as const,
          lastError: message,
          errorStatus: status,
          failedAtMs,
        }),
      },
      'temporary failure',
      503,
      { name: 'codex.json', type: 'codex' },
      activeState,
      2_000
    );

    expect(result).toEqual({
      ...activeState,
      lastError: 'temporary failure',
      errorStatus: 503,
      failedAtMs: 2_000,
    });
  });
});

describe('getSortedCodexResetCreditExpiries', () => {
  it('filters expired or invalid reset credits and sorts by expiry time', () => {
    const expiries = getSortedCodexResetCreditExpiries(
      [
        {
          id: 'late',
          status: 'available',
          grantedAt: '2026-06-29T00:00:00Z',
          expiresAt: '2026-07-19T00:42:09Z',
        },
        {
          id: 'expired',
          status: 'available',
          grantedAt: '2026-06-29T00:00:00Z',
          expiresAt: '2026-07-17T08:31:33Z',
        },
        {
          id: 'invalid',
          status: 'available',
          grantedAt: '2026-06-29T00:00:00Z',
          expiresAt: 'not-a-date',
        },
        {
          id: 'early',
          status: 'available',
          grantedAt: '2026-06-29T00:00:00Z',
          expiresAt: '2026-07-18T08:31:33Z',
        },
      ],
      new Date('2026-07-18T00:00:00Z').getTime()
    );

    expect(expiries.map((item) => item.id)).toEqual(['early', 'late']);
    expect(expiries.map((item) => item.expiresAtMs)).toEqual([
      new Date('2026-07-18T08:31:33Z').getTime(),
      new Date('2026-07-19T00:42:09Z').getTime(),
    ]);
  });
});

describe('resolveQuotaDisplayState', () => {
  it('uses observed headers when a Codex success response did not identify a quota inventory', () => {
    const activeQuota: CodexQuotaState = {
      status: 'success',
      fetchedAtMs: 2_000,
      quotaInventoryObserved: false,
      planType: 'plus',
      windows: [],
    };
    const observedQuota: CodexQuotaState = {
      status: 'success',
      observedAtMs: 1_000,
      observedFromUsageHeaders: true,
      windows: [
        {
          id: 'five-hour',
          label: '5-hour limit',
          usedPercent: 25,
          resetLabel: '07/01 12:00',
        },
      ],
    };

    const result = resolveQuotaDisplayState(activeQuota, observedQuota) as CodexQuotaState;

    expect(result).toMatchObject({
      status: 'success',
      fetchedAtMs: 2_000,
      observedAtMs: 1_000,
      observedFromUsageHeaders: true,
      planType: 'plus',
      windows: [{ id: 'five-hour', usedPercent: 25 }],
    });
  });

  it('keeps a newer manual quota refresh over an older header snapshot', () => {
    const activeQuota: TestQuotaState = {
      status: 'success',
      fetchedAtMs: 2_000,
      windows: [],
    };
    const observedQuota: TestQuotaState = {
      status: 'success',
      observedAtMs: 1_000,
      observedFromUsageHeaders: true,
      windows: [],
    };

    expect(resolveQuotaDisplayState(activeQuota, observedQuota)).toBe(activeQuota);
  });

  it('keeps a manual Codex inventory when its timestamp equals the Header snapshot', () => {
    const activeQuota: CodexQuotaState = {
      status: 'success',
      fetchedAtMs: 2_000,
      quotaInventoryObserved: true,
      windows: [
        {
          id: 'weekly',
          label: 'Weekly limit',
          usedPercent: 20,
          resetLabel: '07/07 12:00',
        },
      ],
    };
    const observedQuota: CodexQuotaState = {
      status: 'success',
      observedAtMs: 2_000,
      observedFromUsageHeaders: true,
      windows: [
        {
          id: 'weekly',
          label: 'Weekly limit',
          usedPercent: 80,
          resetLabel: '07/07 13:00',
        },
      ],
    };

    expect(resolveQuotaDisplayState(activeQuota, observedQuota)).toBe(activeQuota);
  });

  it('does not append older Header-only windows to a newer complete manual inventory', () => {
    const activeQuota: CodexQuotaState = {
      status: 'success',
      fetchedAtMs: 2_000,
      quotaInventoryObserved: true,
      windows: [
        {
          id: 'weekly',
          label: 'Weekly limit',
          usedPercent: 20,
          resetLabel: '07/07 12:00',
        },
      ],
    };
    const observedQuota: CodexQuotaState = {
      status: 'success',
      observedAtMs: 1_000,
      observedFromUsageHeaders: true,
      windows: [
        {
          id: 'five-hour',
          label: '5-hour limit',
          usedPercent: 80,
          resetLabel: '07/01 13:00',
        },
      ],
    };

    const result = resolveQuotaDisplayState(activeQuota, observedQuota) as CodexQuotaState;

    expect(result).toBe(activeQuota);
    expect(result.windows.map((window) => window.id)).toEqual(['weekly']);
  });

  it('adds missing Header windows to a newer partial Codex inventory without retagging API windows', () => {
    const activeQuota: CodexQuotaState = {
      status: 'success',
      fetchedAtMs: 2_000,
      quotaInventoryObserved: false,
      planType: 'plus',
      windows: [
        {
          id: 'code-review-weekly',
          label: 'Code review weekly',
          usedPercent: 15,
          resetLabel: '07/07 12:00',
          limitWindowSeconds: 604_800,
        },
      ],
    };
    const observedQuota: CodexQuotaState = {
      status: 'success',
      observedAtMs: 1_000,
      observedFromUsageHeaders: true,
      planType: 'free',
      windows: [
        {
          id: 'five-hour',
          label: '5-hour limit',
          usedPercent: 40,
          resetLabel: '07/01 12:00',
          limitWindowSeconds: 18_000,
        },
      ],
    };

    const result = resolveQuotaDisplayState(activeQuota, observedQuota) as CodexQuotaState;

    expect(result.planType).toBe('plus');
    expect(result.windows).toMatchObject([
      {
        id: 'code-review-weekly',
        observationSource: 'api_query',
        observedAtMs: 2_000,
      },
      {
        id: 'five-hour',
        observationSource: 'response_header',
        observedAtMs: 1_000,
      },
    ]);
  });

  it('does not append a legacy Spark alias when the partial inventory already has the canonical window', () => {
    const sparkScope = {
      kind: 'models' as const,
      models: [CODEX_SPARK_MODEL_ID],
      complete: true,
    };
    const activeQuota: CodexQuotaState = {
      status: 'success',
      fetchedAtMs: 2_000,
      quotaInventoryObserved: false,
      windows: [
        {
          id: 'spark-weekly-0',
          label: 'Spark weekly',
          resetLabel: '-',
          usedPercent: 0,
          modelScope: sparkScope,
          providerWindowAliases: ['fast-coding-weekly-0'],
        },
      ],
    };
    const observedQuota: CodexQuotaState = {
      status: 'success',
      observedAtMs: 1_000,
      observedFromUsageHeaders: true,
      observedModelScope: sparkScope,
      windows: [
        {
          id: 'fast-coding-weekly-0',
          label: 'Spark weekly',
          resetLabel: '-',
          usedPercent: 0,
          modelScope: sparkScope,
        },
      ],
    };

    const result = resolveQuotaDisplayState(activeQuota, observedQuota) as CodexQuotaState;

    expect(result.windows).toHaveLength(1);
    expect(result.windows[0].id).toBe('spark-weekly-0');
  });

  it('merges a newer header snapshot into the manual quota refresh', () => {
    const activeQuota: TestQuotaState = {
      status: 'success',
      fetchedAtMs: 1_000,
      windows: [
        {
          id: 'manual',
          label: 'Manual window',
          usedPercent: 10,
          resetLabel: '06/30 12:00',
        },
      ],
    };
    const observedQuota: TestQuotaState = {
      status: 'success',
      observedAtMs: 2_000,
      observedFromUsageHeaders: true,
      windows: [
        {
          id: 'observed',
          label: 'Observed window',
          usedPercent: 20,
          resetLabel: '07/01 12:00',
        },
      ],
    };

    const result = resolveQuotaDisplayState(activeQuota, observedQuota);

    expect(result).not.toBe(activeQuota);
    expect(result).not.toBe(observedQuota);
    expect(result).toMatchObject({
      status: 'success',
      fetchedAtMs: 1_000,
      observedAtMs: 2_000,
      windows: [
        { id: 'manual', usedPercent: 10 },
        { id: 'observed', usedPercent: 20 },
      ],
    });
  });

  it('keeps API-only Codex quota data when merging newer header snapshots', () => {
    const activeQuota: CodexQuotaState = {
      status: 'success',
      fetchedAtMs: 1_000,
      planType: 'plus',
      windows: [
        {
          id: 'five-hour',
          label: '5-hour limit',
          labelKey: 'codex_quota.primary_window',
          usedPercent: 10,
          resetLabel: '06/30 12:00',
          limitWindowSeconds: 18_000,
        },
        {
          id: 'spark-five-hour-0',
          label: 'Spark 5-hour limit',
          labelKey: 'codex_quota.additional_primary_window',
          labelParams: { name: 'spark' },
          usedPercent: 30,
          resetLabel: '07/01 01:00',
          limitWindowSeconds: 18_000,
        },
      ],
      rateLimitResetCreditsAvailableCount: 2,
      rateLimitResetCredits: [
        {
          id: 'credit-1',
          status: 'available',
          grantedAt: '2026-06-29T00:00:00Z',
          expiresAt: '2026-07-19T00:42:09Z',
        },
      ],
      rateLimitResetCreditsError: null,
    };
    const observedQuota: CodexQuotaState = {
      status: 'success',
      observedFromUsageHeaders: true,
      observedResetCreditsUnknown: true,
      observedAtMs: 2_000,
      planType: 'free',
      windows: [
        {
          id: 'five-hour',
          label: '5-hour limit',
          labelKey: 'codex_quota.primary_window',
          usedPercent: 80,
          resetLabel: '07/01 02:00',
          limitWindowSeconds: null,
        },
        {
          id: 'weekly',
          label: 'Weekly limit',
          labelKey: 'codex_quota.secondary_window',
          usedPercent: 40,
          resetLabel: '07/07 02:00',
          limitWindowSeconds: 604_800,
        },
      ],
    };

    const result = resolveQuotaDisplayState(activeQuota, observedQuota) as CodexQuotaState;

    expect(result.planType).toBe('free');
    expect(result.observedAtMs).toBe(2_000);
    expect(result.observedFromUsageHeaders).toBe(true);
    expect(result.observedResetCreditsUnknown).toBeUndefined();
    expect(result.rateLimitResetCreditsAvailableCount).toBe(2);
    expect(result.rateLimitResetCredits).toHaveLength(1);
    expect(result.windows.map((window) => window.id)).toEqual([
      'five-hour',
      'spark-five-hour-0',
      'weekly',
    ]);
    expect(result.windows[0]).toMatchObject({
      id: 'five-hour',
      usedPercent: 80,
      resetLabel: '07/01 02:00',
      limitWindowSeconds: 18_000,
    });
    expect(result.windows[1]).toMatchObject({
      id: 'spark-five-hour-0',
      usedPercent: 30,
      resetLabel: '07/01 01:00',
    });
  });

  it('does not retain an older reset timestamp behind a newer reset label', () => {
    const activeQuota: CodexQuotaState = {
      status: 'success',
      fetchedAtMs: 1_000,
      windows: [
        {
          id: 'five-hour',
          label: '5-hour limit',
          usedPercent: 10,
          resetLabel: '2026-07-01T01:00:00Z',
          resetAtMs: Date.parse('2026-07-01T01:00:00Z'),
          resetAccuracy: 'exact',
        },
      ],
    };
    const observedQuota: CodexQuotaState = {
      status: 'success',
      observedAtMs: 2_000,
      observedFromUsageHeaders: true,
      windows: [
        {
          id: 'five-hour',
          label: '5-hour limit',
          usedPercent: 80,
          resetLabel: 'resets after the next request window',
          resetAtMs: null,
          resetAccuracy: 'unknown',
        },
      ],
    };

    const result = resolveQuotaDisplayState(activeQuota, observedQuota) as CodexQuotaState;

    expect(result.windows[0]).toMatchObject({
      resetLabel: 'resets after the next request window',
      resetAtMs: null,
      resetAccuracy: 'unknown',
    });
  });

  it('keeps 401 quota errors so reauth controls stay visible', () => {
    const activeQuota: TestQuotaState = {
      status: 'error',
      errorStatus: 401,
    };
    const observedQuota: TestQuotaState = {
      status: 'success',
      observedAtMs: 2_000,
    };

    expect(resolveQuotaDisplayState(activeQuota, observedQuota)).toBe(activeQuota);
  });

  it('keeps manual refresh failures over older header snapshots', () => {
    const activeQuota: CodexQuotaState = {
      status: 'error',
      error: 'refresh failed',
      errorStatus: 502,
      failedAtMs: 2_000,
      planType: 'plus',
      windows: [
        {
          id: 'five-hour',
          label: '5-hour limit',
          usedPercent: 10,
          resetLabel: '06/30 12:00',
          limitWindowSeconds: 18_000,
        },
      ],
      rateLimitResetCreditsAvailableCount: 2,
    };
    const observedQuota: CodexQuotaState = {
      status: 'success',
      observedFromUsageHeaders: true,
      observedAtMs: 1_000,
      planType: 'free',
      windows: [
        {
          id: 'five-hour',
          label: '5-hour limit',
          usedPercent: 80,
          resetLabel: '07/01 02:00',
          limitWindowSeconds: null,
        },
      ],
    };

    expect(resolveQuotaDisplayState(activeQuota, observedQuota)).toBe(activeQuota);
  });

  it('recovers manual refresh failures with newer header snapshots without dropping API-only fields', () => {
    const activeQuota: CodexQuotaState = {
      status: 'error',
      error: 'refresh failed',
      errorStatus: 502,
      failedAtMs: 1_000,
      fetchedAtMs: 500,
      planType: 'plus',
      windows: [
        {
          id: 'five-hour',
          label: '5-hour limit',
          labelKey: 'codex_quota.primary_window',
          usedPercent: 10,
          resetLabel: '06/30 12:00',
          limitWindowSeconds: 18_000,
        },
        {
          id: 'spark-five-hour-0',
          label: 'Spark 5-hour limit',
          labelKey: 'codex_quota.additional_primary_window',
          labelParams: { name: 'spark' },
          usedPercent: 30,
          resetLabel: '07/01 01:00',
          limitWindowSeconds: 18_000,
        },
      ],
      rateLimitResetCreditsAvailableCount: 2,
      rateLimitResetCredits: [
        {
          id: 'credit-1',
          status: 'available',
          grantedAt: '2026-06-29T00:00:00Z',
          expiresAt: '2026-07-19T00:42:09Z',
        },
      ],
      rateLimitResetCreditsError: null,
    };
    const observedQuota: CodexQuotaState = {
      status: 'success',
      observedFromUsageHeaders: true,
      observedResetCreditsUnknown: true,
      observedAtMs: 2_000,
      planType: 'free',
      windows: [
        {
          id: 'five-hour',
          label: '5-hour limit',
          labelKey: 'codex_quota.primary_window',
          usedPercent: 80,
          resetLabel: '07/01 02:00',
          limitWindowSeconds: null,
        },
      ],
    };

    const result = resolveQuotaDisplayState(activeQuota, observedQuota) as CodexQuotaState;

    expect(result.status).toBe('success');
    expect(result.error).toBeUndefined();
    expect(result.errorStatus).toBeUndefined();
    expect(result.failedAtMs).toBeUndefined();
    expect(result.observedFromUsageHeaders).toBe(true);
    expect(result.rateLimitResetCreditsAvailableCount).toBe(2);
    expect(result.rateLimitResetCredits).toHaveLength(1);
    expect(result.windows.map((window) => window.id)).toEqual(['five-hour', 'spark-five-hour-0']);
    expect(result.windows[0]).toMatchObject({
      id: 'five-hour',
      usedPercent: 80,
      resetLabel: '07/01 02:00',
      limitWindowSeconds: 18_000,
    });
    expect(result.windows[1]).toMatchObject({
      id: 'spark-five-hour-0',
      usedPercent: 30,
      resetLabel: '07/01 01:00',
    });
  });

  it('does not let scoped Header scalars replace account-wide Codex state', () => {
    const activeQuota: CodexQuotaState = {
      status: 'success',
      fetchedAtMs: 1_000,
      activeLimit: 'main',
      windows: [
        {
          id: 'weekly',
          label: 'Weekly limit',
          usedPercent: 36,
          resetLabel: '07/07 00:00',
          modelScope: { kind: 'family', key: 'codex_main', complete: true },
        },
      ],
    };
    const sparkScope = {
      kind: 'models' as const,
      models: [CODEX_SPARK_MODEL_ID],
      complete: true,
    };
    const observedQuota: CodexQuotaState = {
      status: 'success',
      observedFromUsageHeaders: true,
      observedAtMs: 2_000,
      observedModelScope: sparkScope,
      activeLimit: 'spark',
      windows: [
        {
          id: 'spark-weekly-0',
          label: 'Spark weekly limit',
          usedPercent: 0,
          resetLabel: '07/07 00:00',
          modelScope: sparkScope,
        },
      ],
    };

    const result = resolveQuotaDisplayState(activeQuota, observedQuota) as CodexQuotaState;

    expect(result.activeLimit).toBe('main');
    expect(result.windows.map((window) => window.id)).toEqual(['weekly', 'spark-weekly-0']);
  });

  it('merges a legacy Spark window into the canonical header window instead of duplicating it', () => {
    const sparkScope = {
      kind: 'models' as const,
      models: [CODEX_SPARK_MODEL_ID],
      complete: true,
    };
    const result = resolveQuotaDisplayState(
      {
        status: 'success',
        fetchedAtMs: 1_000,
        quotaInventoryObserved: true,
        windows: [
          {
            id: 'fast-coding-weekly-0',
            usedPercent: 50,
            modelScope: sparkScope,
          },
        ],
      },
      {
        status: 'success',
        observedAtMs: 2_000,
        observedFromUsageHeaders: true,
        observedModelScope: sparkScope,
        windows: [
          {
            id: 'spark-weekly-0',
            usedPercent: 0,
            modelScope: sparkScope,
            providerWindowAliases: ['fast-coding-weekly-0'],
          },
        ],
      }
    ) as CodexQuotaState;

    expect(result.windows).toHaveLength(1);
    expect(result.windows[0]).toMatchObject({ id: 'spark-weekly-0', usedPercent: 0 });
    expect(result.windows[0].providerWindowAliases).toContain('fast-coding-weekly-0');
  });

  it('does not resolve an ambiguous secondary alias between weekly and monthly windows', () => {
    const result = resolveQuotaDisplayState(
      {
        status: 'success',
        fetchedAtMs: 1_000,
        quotaInventoryObserved: true,
        windows: [
          {
            id: 'weekly',
            usedPercent: 36,
            modelScope: { kind: 'family', key: 'codex_main', complete: true },
            providerWindowAliases: ['secondary'],
          },
          {
            id: 'monthly',
            usedPercent: 10,
            modelScope: { kind: 'family', key: 'codex_main', complete: true },
            providerWindowAliases: ['secondary'],
          },
        ],
      },
      {
        status: 'success',
        observedAtMs: 2_000,
        observedFromUsageHeaders: true,
        observedModelScope: { kind: 'family', key: 'codex_main', complete: true },
        windows: [
          {
            id: 'monthly',
            usedPercent: 45,
            modelScope: { kind: 'family', key: 'codex_main', complete: true },
            providerWindowAliases: ['secondary'],
          },
        ],
      }
    ) as CodexQuotaState;

    expect(result.windows).toEqual([
      expect.objectContaining({ id: 'weekly', usedPercent: 36 }),
      expect.objectContaining({ id: 'monthly', usedPercent: 45 }),
    ]);
  });
});

describe('Codex plan precedence', () => {
  const t = ((key: string) => key) as TFunction;

  it('uses a newer observed header plan before the credential token plan', () => {
    const state = buildObservedCodexQuotaState(
      {
        name: 'stale-plan.codex.json',
        type: 'codex',
        id_token: { plan_type: 'plus' },
      },
      {
        event_hash: 'event-1',
        timestamp_ms: 2_000,
        model: 'gpt-5.6-sol',
        header_quota_plan_type: 'free',
        header_quota_used_percent: 25,
      },
      t
    );

    expect(state?.planType).toBe('free');
  });

  it('drops an expired Header 5-hour window while retaining the active weekly window', () => {
    const nowMs = 1_800_000_000_000;
    const state = buildObservedCodexQuotaState(
      { name: 'mixed-window.codex.json', type: 'codex' },
      {
        event_hash: 'mixed-window-event',
        timestamp_ms: nowMs - 60_000,
        model: 'gpt-5.6-sol',
        response_metadata: {
          quota: {
            primary: {
              used_percent: 100,
              reset_at_ms: nowMs - 1,
              window_minutes: 300,
            },
            secondary: {
              used_percent: 40,
              reset_at_ms: nowMs + 60_000,
              window_minutes: 10_080,
            },
          },
        },
      },
      t,
      nowMs
    );

    expect(state?.windows).toMatchObject([
      {
        id: 'weekly',
        usedPercent: 40,
        observationSource: 'response_header',
        observedAtMs: nowMs - 60_000,
      },
    ]);
  });

  it('builds a Spark-scoped Header observation from an alias-resolved request', () => {
    const nowMs = 1_800_000_000_000;
    const state = buildObservedCodexQuotaState(
      { name: 'spark.codex.json', type: 'codex' },
      {
        event_hash: 'spark-alias-event',
        timestamp_ms: nowMs - 1_000,
        model: 'my-spark',
        analytics_model: 'my-spark',
        requested_model: 'my-spark',
        resolved_model: CODEX_SPARK_MODEL_ID,
        response_metadata: {
          quota: {
            primary: {
              used_percent: 0,
              reset_at_ms: nowMs + 7 * 24 * 60 * 60 * 1000,
              window_minutes: 10_080,
            },
          },
        },
      },
      t,
      nowMs
    );

    expect(state?.observedModelScope).toEqual({
      kind: 'models',
      models: [CODEX_SPARK_MODEL_ID],
      complete: true,
    });
    expect(state?.windows).toMatchObject([
      {
        id: 'spark-weekly-0',
        usedPercent: 0,
        modelScope: {
          kind: 'models',
          models: [CODEX_SPARK_MODEL_ID],
          complete: true,
        },
      },
    ]);
  });
});
