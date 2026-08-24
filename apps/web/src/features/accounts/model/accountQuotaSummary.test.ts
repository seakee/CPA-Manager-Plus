import { describe, expect, it } from 'vitest';
import type { AuthFileItem, CodexQuotaState } from '@/types';
import { getAuthFileSelectionKey } from '@/features/authFiles/model/credentialStatus';
import { CODEX_SPARK_MODEL_ID } from '@/utils/quota/codexQuota';
import { resolveAccountQuota, type AccountQuotaStores } from './accountQuotaSummary';

const emptyStores = (): AccountQuotaStores => ({
  antigravityQuota: {},
  claudeQuota: {},
  codexQuota: {},
  kimiQuota: {},
  xaiQuota: {},
});

describe('resolveAccountQuota', () => {
  it('keeps the account summary on Codex Main when Spark is more constrained', () => {
    const file = {
      name: 'codex.json',
      type: 'codex',
      authIndex: 'auth-1',
    } as AuthFileItem;
    const quota: CodexQuotaState = {
      status: 'success',
      windows: [
        {
          id: 'weekly',
          label: 'Weekly',
          usedPercent: 36,
          resetLabel: 'main-reset',
          modelScope: { kind: 'family', key: 'codex_main', complete: true },
        },
        {
          id: 'spark-weekly-0',
          label: 'Spark Weekly',
          usedPercent: 95,
          resetLabel: 'spark-reset',
          modelScope: {
            kind: 'models',
            models: [CODEX_SPARK_MODEL_ID],
            complete: true,
          },
        },
      ],
    };

    const summary = resolveAccountQuota(file, emptyStores(), {
      codexQuotaBySelectionKey: new Map([[getAuthFileSelectionKey(file), quota]]),
    });

    expect(summary).toMatchObject({
      usedPercent: 36,
      remainingPercent: 64,
      resetLabel: 'main-reset',
    });
  });

  it('does not treat a scoped Header observation as fresh account-wide quota evidence', () => {
    const file = {
      name: 'codex.json',
      type: 'codex',
      authIndex: 'auth-1',
    } as AuthFileItem;
    const quota: CodexQuotaState = {
      status: 'success',
      fetchedAtMs: 1_000,
      observedAtMs: 2_000,
      observedFromUsageHeaders: true,
      observedModelScope: {
        kind: 'models',
        models: [CODEX_SPARK_MODEL_ID],
        complete: true,
      },
      observedTraceId: 'spark-trace',
      activeLimit: 'main',
      windows: [
        {
          id: 'weekly',
          label: 'Weekly',
          usedPercent: 36,
          resetLabel: 'main-reset',
          modelScope: { kind: 'family', key: 'codex_main', complete: true },
        },
        {
          id: 'spark-weekly-0',
          label: 'Spark Weekly',
          usedPercent: 0,
          resetLabel: 'spark-reset',
          modelScope: {
            kind: 'models',
            models: [CODEX_SPARK_MODEL_ID],
            complete: true,
          },
        },
      ],
    };

    const summary = resolveAccountQuota(file, emptyStores(), {
      codexQuotaBySelectionKey: new Map([[getAuthFileSelectionKey(file), quota]]),
    });

    expect(summary).toMatchObject({
      source: 'cache',
      fetchedAtMs: 1_000,
      observedAtMs: 2_000,
      observedTraceId: 'spark-trace',
      activeLimit: 'main',
      usedPercent: 36,
      remainingPercent: 64,
    });
    expect(summary.observedQuotaAtMs).toBeUndefined();
  });
});
