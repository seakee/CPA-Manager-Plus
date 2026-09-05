import { describe, expect, it } from 'vitest';
import {
  CODEX_CODE_REVIEW_SCOPE_KEY,
  CODEX_MAIN_QUOTA_SCOPE_KEY,
  CODEX_SPARK_MODEL_ID,
  classifyCodexRateLimitWindows,
  deriveCodexRateLimitUsedPercent,
  isCodexRateLimitReached,
  buildCodexQuotaWindowInfos,
  canonicalizeCodexProviderWindowId,
  inferCodexQuotaScopeFromProviderWindowId,
  isCodexLegacyAllScopeReplacement,
  isCodexMainQuotaWindow,
  normalizeCodexModelId,
  resolveCodexUsageQuotaScope,
  shouldClearInheritedCodexQuotaProgress,
} from './codexQuota';
import type { CodexQuotaCycleEvidence, CodexQuotaWindowInfo } from './codexQuota';

describe('buildCodexQuotaWindowInfos', () => {
  it('distinguishes exact absolute resets from relative estimates anchored to observation time', () => {
    const observedAtMs = Date.parse('2026-07-29T10:00:00Z');
    const exactResetAtMs = Date.parse('2026-07-29T12:00:00Z');
    const windows = buildCodexQuotaWindowInfos(
      {
        rate_limit: {
          primary_window: {
            used_percent: 10,
            limit_window_seconds: 18_000,
            reset_at: exactResetAtMs / 1000,
          },
          secondary_window: {
            used_percent: 20,
            limit_window_seconds: 604_800,
            reset_after_seconds: 7_200,
          },
        },
      },
      { observedAtMs }
    );

    expect(windows.find((window) => window.id === 'five-hour')).toMatchObject({
      resetAtMs: exactResetAtMs,
      resetAccuracy: 'exact',
    });
    expect(windows.find((window) => window.id === 'weekly')).toMatchObject({
      resetAtMs: observedAtMs + 7_200_000,
      resetAccuracy: 'estimated',
    });
  });

  it('keeps provider API absolute reset evidence exact when a relative reset is also present', () => {
    const observedAtMs = Date.parse('2026-08-05T10:00:00.638Z');
    const storedResetAtMs = Date.parse('2026-09-04T09:59:59.000Z');
    const windows = buildCodexQuotaWindowInfos(
      {
        rate_limit: {
          primary_window: {
            used_percent: 1,
            limit_window_seconds: 30 * 24 * 60 * 60,
            reset_at: storedResetAtMs / 1000,
            reset_after_seconds: 30 * 24 * 60 * 60,
          },
        },
      },
      { observedAtMs }
    );

    expect(windows.find((window) => window.id === 'monthly')).toMatchObject({
      resetAtMs: storedResetAtMs,
      resetAccuracy: 'exact',
    });
  });

  it('marks synthesized Header absolute resets as estimated when relative evidence is present', () => {
    const observedAtMs = Date.parse('2026-08-05T10:00:00.638Z');
    const storedResetAtMs = Date.parse('2026-09-04T09:59:59.000Z');
    const windows = buildCodexQuotaWindowInfos(
      {
        rate_limit: {
          primary_window: {
            used_percent: 1,
            limit_window_seconds: 30 * 24 * 60 * 60,
            reset_at: storedResetAtMs / 1000,
            reset_after_seconds: 30 * 24 * 60 * 60,
          },
        },
      },
      { observedAtMs, source: 'response_header' }
    );

    expect(windows.find((window) => window.id === 'monthly')).toMatchObject({
      resetAtMs: storedResetAtMs,
      resetAccuracy: 'estimated',
    });
  });

  it('accepts Codex absolute resets as Unix milliseconds or ISO timestamps', () => {
    const millisecondResetAtMs = Date.parse('2026-07-29T12:00:00Z');
    const isoResetAt = '2026-08-05T12:00:00Z';
    const windows = buildCodexQuotaWindowInfos({
      rate_limit: {
        primary_window: {
          used_percent: 10,
          limit_window_seconds: 18_000,
          reset_at: millisecondResetAtMs,
        },
        secondary_window: {
          used_percent: 20,
          limit_window_seconds: 604_800,
          reset_at: isoResetAt,
        },
      },
    });

    expect(windows.find((window) => window.id === 'five-hour')).toMatchObject({
      resetAtMs: millisecondResetAtMs,
      resetAccuracy: 'exact',
    });
    expect(windows.find((window) => window.id === 'weekly')).toMatchObject({
      resetAtMs: Date.parse(isoResetAt),
      resetAccuracy: 'exact',
    });
  });

  it('rejects reset timestamps that exceed the JavaScript date range', () => {
    const windows = buildCodexQuotaWindowInfos(
      {
        rate_limit: {
          primary_window: {
            used_percent: 10,
            limit_window_seconds: 18_000,
            reset_at: Number.MAX_VALUE,
          },
          secondary_window: {
            used_percent: 20,
            limit_window_seconds: 604_800,
            reset_after_seconds: Number.MAX_VALUE,
          },
        },
      },
      { observedAtMs: Date.parse('2026-07-29T10:00:00Z') }
    );

    expect(windows).toMatchObject([
      {
        id: 'five-hour',
        resetLabel: '-',
        resetAtMs: null,
        resetAccuracy: 'unknown',
      },
      {
        id: 'weekly',
        resetLabel: '-',
        resetAtMs: null,
        resetAccuracy: 'unknown',
      },
    ]);
  });

  it('classifies Codex primary and weekly windows by duration', () => {
    const windows = buildCodexQuotaWindowInfos({
      rate_limit: {
        primary_window: {
          used_percent: 10,
          limit_window_seconds: 604_800,
          reset_after_seconds: 60,
        },
        secondary_window: {
          used_percent: 30,
          limit_window_seconds: 18_000,
          reset_after_seconds: 120,
        },
      },
    });

    expect(windows.map((window) => [window.id, window.usedPercent])).toEqual([
      ['five-hour', 30],
      ['weekly', 10],
    ]);
  });

  it('marks reached windows as fully used when usage percent is absent', () => {
    const windows = buildCodexQuotaWindowInfos({
      rate_limit: {
        limit_reached: true,
        primary_window: {
          limit_window_seconds: 18_000,
          reset_after_seconds: 300,
        },
      },
    });

    expect(windows[0]).toMatchObject({
      id: 'five-hour',
      usedPercent: 100,
    });
  });

  it('classifies current Codex monthly-only quota without falling back to five-hour', () => {
    const payload = {
      user_id: 'user-test',
      account_id: 'acct-test',
      email: 'user@example.test',
      plan_type: 'free',
      rate_limit: {
        allowed: true,
        limit_reached: false,
        primary_window: {
          used_percent: 5,
          limit_window_seconds: 2_592_000,
          reset_after_seconds: 2_592_000,
          reset_at: 1_782_895_966,
        },
        secondary_window: null,
      },
      code_review_rate_limit: null,
      additional_rate_limits: null,
      credits: {
        has_credits: false,
        unlimited: false,
        overage_limit_reached: false,
        balance: null,
      },
      spend_control: {
        reached: false,
        individual_limit: null,
      },
      rate_limit_reset_credits: {
        available_count: 0,
      },
    };

    const windows = buildCodexQuotaWindowInfos(payload);
    const classified = classifyCodexRateLimitWindows(payload.rate_limit);

    expect(windows).toMatchObject([
      {
        id: 'monthly',
        labelKey: 'codex_quota.monthly_window',
        usedPercent: 5,
        limitWindowSeconds: 2_592_000,
      },
    ]);
    expect(classified.fiveHourWindow).toBeNull();
    expect(classified.weeklyWindow).toBeNull();
    expect(classified.monthlyWindow?.used_percent).toBe(5);
    expect(classified.longWindow).toBe(classified.monthlyWindow);
    expect(deriveCodexRateLimitUsedPercent(payload.rate_limit)).toBe(5);
    expect(isCodexRateLimitReached(payload.rate_limit)).toBe(false);
  });

  it('classifies 28 to 31 day windows as monthly quota', () => {
    const monthLikeDurations = [2_419_200, 2_505_600, 2_592_000, 2_678_400];

    monthLikeDurations.forEach((duration) => {
      const classified = classifyCodexRateLimitWindows({
        primary_window: {
          used_percent: 20,
          limit_window_seconds: duration,
        },
      });

      expect(classified.monthlyWindow?.limit_window_seconds).toBe(duration);
      expect(classified.weeklyWindow).toBeNull();
    });
  });

  it('does not classify windows longer than 31 days as monthly quota', () => {
    const classified = classifyCodexRateLimitWindows({
      primary_window: {
        used_percent: 20,
        limit_window_seconds: 2_764_800,
      },
    });

    expect(classified.monthlyWindow).toBeNull();
    expect(classified.longWindow?.limit_window_seconds).toBe(2_764_800);
  });

  it('treats a Team secondary window without duration as monthly quota', () => {
    const windows = buildCodexQuotaWindowInfos(
      {
        plan_type: 'team',
        rate_limit: {
          primary_window: {
            used_percent: 10,
            reset_after_seconds: 60,
          },
          secondary_window: {
            used_percent: 70,
            reset_after_seconds: 120,
          },
        },
      },
      { planType: 'team' }
    );

    expect(windows.map((window) => [window.id, window.labelKey, window.usedPercent])).toEqual([
      ['five-hour', 'codex_quota.primary_window', 10],
      ['monthly', 'codex_quota.monthly_window', 70],
    ]);
  });

  it('builds a Team weekly bonus window with its structural monthly cadence suffix', () => {
    const windows = buildCodexQuotaWindowInfos({
      plan_type: 'team',
      additional_rate_limits: [
        {
          limit_name: 'Weekly Bonus',
          rate_limit: {
            secondary_window: { used_percent: 80 },
          },
        },
      ],
    });

    expect(windows).toMatchObject([
      {
        id: 'weekly-bonus-monthly-0',
        usedPercent: 80,
        limitWindowSeconds: null,
      },
    ]);
  });

  it('builds a non-Team five hour bonus window with its structural weekly cadence suffix', () => {
    const windows = buildCodexQuotaWindowInfos({
      plan_type: 'plus',
      additional_rate_limits: [
        {
          limit_name: 'Five Hour Bonus',
          rate_limit: {
            secondary_window: { used_percent: 80 },
          },
        },
      ],
    });

    expect(windows).toMatchObject([
      {
        id: 'five-hour-bonus-weekly-0',
        usedPercent: 80,
        limitWindowSeconds: null,
      },
    ]);
  });

  it('normalizes additional rate limit labels into stable ids and params', () => {
    const windows = buildCodexQuotaWindowInfos({
      additional_rate_limits: [
        {
          limit_name: 'Code Review Premium',
          rate_limit: {
            primary_window: {
              used_percent: 45,
              limit_window_seconds: 18_000,
              reset_after_seconds: 600,
            },
            secondary_window: {
              used_percent: 55,
              limit_window_seconds: 604_800,
              reset_after_seconds: 1_200,
            },
          },
        },
      ],
    });

    expect(windows).toMatchObject([
      {
        id: 'code-review-premium-five-hour-0',
        labelKey: 'codex_quota.additional_primary_window',
        labelParams: { name: 'Code Review Premium' },
        usedPercent: 45,
      },
      {
        id: 'code-review-premium-weekly-0',
        labelKey: 'codex_quota.additional_secondary_window',
        labelParams: { name: 'Code Review Premium' },
        usedPercent: 55,
      },
    ]);
  });

  it('assigns account-wide, model, and fail-closed feature scopes', () => {
    const windows = buildCodexQuotaWindowInfos({
      rate_limit: {
        secondary_window: { used_percent: 36, limit_window_seconds: 604_800 },
      },
      code_review_rate_limit: {
        secondary_window: { used_percent: 20, limit_window_seconds: 604_800 },
      },
      additional_rate_limits: [
        {
          limit_name: 'Spark',
          metered_feature: 'codex_spark',
          rate_limit: {
            secondary_window: { used_percent: 0, limit_window_seconds: 604_800 },
          },
        },
        {
          limit_name: 'Future Feature',
          metered_feature: 'future_feature',
          rate_limit: {
            secondary_window: { used_percent: 10, limit_window_seconds: 604_800 },
          },
        },
      ],
    });
    const byId = new Map(windows.map((window) => [window.id, window]));

    expect(byId.get('weekly')?.modelScope).toEqual({
      kind: 'family',
      key: CODEX_MAIN_QUOTA_SCOPE_KEY,
      complete: true,
    });
    expect(byId.get('weekly')?.providerWindowAliases).toContain('secondary');
    expect(byId.get('spark-weekly-0')?.modelScope).toEqual({
      kind: 'models',
      models: [CODEX_SPARK_MODEL_ID],
      complete: true,
    });
    expect(byId.get('spark-weekly-0')?.providerWindowAliases).toContain('codex-spark-weekly-0');
    expect(byId.get('code-review-weekly')?.modelScope).toEqual({
      kind: 'feature',
      key: CODEX_CODE_REVIEW_SCOPE_KEY,
      complete: false,
    });
    expect(byId.get('future-feature-weekly-0')?.modelScope).toEqual({
      kind: 'feature',
      key: 'future_feature',
      complete: false,
    });
  });

  it('keeps the legacy display-label id as a Spark snapshot alias', () => {
    const [window] = buildCodexQuotaWindowInfos({
      additional_rate_limits: [
        {
          limit_name: 'Fast coding',
          metered_feature: 'codex_spark',
          rate_limit: {
            secondary_window: { used_percent: 0, limit_window_seconds: 604_800 },
          },
        },
      ],
    });

    expect(window).toMatchObject({
      id: 'spark-weekly-0',
      providerWindowAliases: expect.arrayContaining(['fast-coding-weekly-0']),
    });
  });

  it('emits the legacy primary alias for the Codex five-hour window', () => {
    const [window] = buildCodexQuotaWindowInfos({
      rate_limit: {
        primary_window: { used_percent: 0, limit_window_seconds: 18_000 },
      },
    });

    expect(window).toMatchObject({
      id: 'five-hour',
      providerWindowAliases: expect.arrayContaining(['primary']),
    });
  });

  it('restores a legacy fast-coding window as Spark without rewriting its identity', () => {
    expect(inferCodexQuotaScopeFromProviderWindowId('fast-coding-weekly-0')).toEqual({
      kind: 'models',
      models: [CODEX_SPARK_MODEL_ID],
      complete: true,
    });
  });

  it('canonicalizes legacy primary and secondary ids without confusing team monthly windows', () => {
    expect(canonicalizeCodexProviderWindowId('primary')).toBe('five-hour');
    expect(canonicalizeCodexProviderWindowId('secondary', 'weekly')).toBe('weekly');
    expect(canonicalizeCodexProviderWindowId('secondary', 'monthly')).toBe('monthly');
  });

  it('replaces non-main legacy all scopes even when the replacement remains incomplete', () => {
    expect(
      isCodexLegacyAllScopeReplacement('future-feature-weekly-0', {
        kind: 'all',
        complete: false,
      })
    ).toBe(true);
    expect(
      isCodexLegacyAllScopeReplacement('future-feature-weekly-0', {
        kind: 'all',
        complete: true,
      })
    ).toBe(false);
    expect(
      isCodexLegacyAllScopeReplacement('weekly', {
        kind: 'all',
        complete: false,
      })
    ).toBe(false);
  });

  it('does not treat an explicitly incomplete main-shaped scope as account-wide', () => {
    expect(
      isCodexMainQuotaWindow({
        id: 'weekly',
        modelScope: { kind: 'all', complete: false },
      })
    ).toBe(false);
  });

  it('resolves direct and aliased Spark usage from the full model identity', () => {
    expect(resolveCodexUsageQuotaScope({ model: CODEX_SPARK_MODEL_ID })).toMatchObject({
      providerWindowIdPrefix: 'spark',
      modelScope: { kind: 'models', models: [CODEX_SPARK_MODEL_ID], complete: true },
    });
    expect(
      resolveCodexUsageQuotaScope({
        model: 'my-spark',
        analyticsModel: 'my-spark',
        requestedModel: 'my-spark',
        resolvedModel: CODEX_SPARK_MODEL_ID,
      })
    ).toMatchObject({
      providerWindowIdPrefix: 'spark',
      modelScope: { kind: 'models', models: [CODEX_SPARK_MODEL_ID], complete: true },
    });
    expect(
      resolveCodexUsageQuotaScope({
        model: 'my-codex',
        requestedModel: 'my-codex',
        resolvedModel: 'gpt-5.6-sol',
      })
    ).toEqual({
      providerWindowIdPrefix: '',
      modelScope: { kind: 'family', key: CODEX_MAIN_QUOTA_SCOPE_KEY, complete: true },
    });
    expect(
      resolveCodexUsageQuotaScope({
        model: CODEX_SPARK_MODEL_ID,
        analyticsModel: CODEX_SPARK_MODEL_ID,
        requestedModel: CODEX_SPARK_MODEL_ID,
        resolvedModel: 'gpt-5.6-sol',
      })
    ).toEqual({
      providerWindowIdPrefix: '',
      modelScope: { kind: 'family', key: CODEX_MAIN_QUOTA_SCOPE_KEY, complete: true },
    });
    expect(resolveCodexUsageQuotaScope({}).modelScope).toEqual({
      kind: 'feature',
      key: 'request_scope_unknown',
      complete: false,
    });
  });

  it('uses the shared analytics model normalizer for scoped model identity', () => {
    expect(normalizeCodexModelId(`${CODEX_SPARK_MODEL_ID}(+12)`)).toBe(CODEX_SPARK_MODEL_ID);
    expect(normalizeCodexModelId('custom-model(9223372036854775808)')).toBe(
      'custom-model(9223372036854775808)'
    );
  });

  it('lets a stable provider feature override a conflicting Spark display label', () => {
    const windows = buildCodexQuotaWindowInfos({
      additional_rate_limits: [
        {
          limit_name: 'Spark',
          metered_feature: 'future_feature',
          rate_limit: {
            secondary_window: { used_percent: 10, limit_window_seconds: 604_800 },
          },
        },
      ],
    });

    expect(windows).toHaveLength(1);
    expect(windows[0]).toMatchObject({
      id: 'future-feature-weekly-0',
      modelScope: { kind: 'feature', key: 'future_feature', complete: false },
    });
  });

  it('keeps generic windows unique across main, code-review, and repeated additional families', () => {
    const genericWindow = (usedPercent: number) => ({
      primary_window: {
        used_percent: usedPercent,
        limit_window_seconds: 2 * 24 * 60 * 60,
      },
    });
    const windows = buildCodexQuotaWindowInfos({
      rate_limit: genericWindow(10),
      code_review_rate_limit: genericWindow(20),
      additional_rate_limits: [
        { limit_name: 'Credits', rate_limit: genericWindow(30) },
        { limit_name: 'Credits', rate_limit: genericWindow(40) },
      ],
    });

    expect(windows.map((window) => [window.id, window.usedPercent])).toEqual([
      ['window-2d-0', 10],
      ['code-review-window-2d-0', 20],
      ['credits-0-window-2d-0', 30],
      ['credits-1-window-2d-0', 40],
    ]);
    expect(new Set(windows.map((window) => window.id)).size).toBe(windows.length);
  });

  it('keeps distinct additional family ids stable when the provider reorders the array', () => {
    const family = (limitName: string, usedPercent: number) => ({
      limit_name: limitName,
      rate_limit: {
        primary_window: {
          used_percent: usedPercent,
          limit_window_seconds: 18_000,
        },
      },
    });
    const forward = buildCodexQuotaWindowInfos({
      additional_rate_limits: [family('Credits', 30), family('Review Premium', 40)],
    });
    const reverse = buildCodexQuotaWindowInfos({
      additional_rate_limits: [family('Review Premium', 40), family('Credits', 30)],
    });

    const idsByUsage = (windows: CodexQuotaWindowInfo[]) =>
      Object.fromEntries(windows.map((window) => [window.usedPercent, window.id]));
    expect(idsByUsage(forward)).toEqual({
      30: 'credits-five-hour-0',
      40: 'review-premium-five-hour-0',
    });
    expect(idsByUsage(reverse)).toEqual(idsByUsage(forward));
  });

  it('uses metered feature to keep duplicate additional names stable across reorder', () => {
    const family = (meteredFeature: string, usedPercent: number) => ({
      limit_name: 'Credits',
      metered_feature: meteredFeature,
      rate_limit: {
        primary_window: {
          used_percent: usedPercent,
          limit_window_seconds: 18_000,
        },
      },
    });
    const forward = buildCodexQuotaWindowInfos({
      additional_rate_limits: [
        family('chat_completions', 30),
        family('code_review', 40),
        {
          limit_name: 'Credits Chat Completions',
          rate_limit: {
            primary_window: { used_percent: 50, limit_window_seconds: 18_000 },
          },
        },
      ],
    });
    const reverse = buildCodexQuotaWindowInfos({
      additional_rate_limits: [
        {
          limit_name: 'Credits Chat Completions',
          rate_limit: {
            primary_window: { used_percent: 50, limit_window_seconds: 18_000 },
          },
        },
        family('code_review', 40),
        family('chat_completions', 30),
      ],
    });

    const idsByUsage = (windows: CodexQuotaWindowInfo[]) =>
      Object.fromEntries(windows.map((window) => [window.usedPercent, window.id]));
    expect(idsByUsage(forward)).toEqual({
      30: 'credits--chat-completions-five-hour-0',
      40: 'credits--code-review-five-hour-0',
      50: 'credits-chat-completions-five-hour-0',
    });
    expect(idsByUsage(reverse)).toEqual(idsByUsage(forward));
  });

  it('keeps anonymous and otherwise ambiguous additional limits stable across reorder', () => {
    const family = (usedPercent: number, seconds: number) => ({
      rate_limit: {
        primary_window: {
          used_percent: usedPercent,
          limit_window_seconds: seconds,
        },
      },
    });
    const forward = buildCodexQuotaWindowInfos({
      additional_rate_limits: [family(30, 18_000), family(40, 18_000), family(50, 604_800)],
    });
    const reverse = buildCodexQuotaWindowInfos({
      additional_rate_limits: [family(50, 604_800), family(40, 18_000), family(30, 18_000)],
    });

    const idsByUsage = (windows: CodexQuotaWindowInfo[]) =>
      Object.fromEntries(windows.map((window) => [window.usedPercent, window.id]));
    expect(idsByUsage(forward)).toEqual({
      30: 'additional-p-18000-s-none-five-hour-0',
      40: 'additional-p-18000-s-none-five-hour-1',
      50: 'additional-p-604800-s-none-weekly-0',
    });
    expect(idsByUsage(reverse)).toEqual(idsByUsage(forward));
  });

  it('shares rate-limit helpers used by Codex inspection', () => {
    const rateLimit = {
      allowed: true,
      primary_window: {
        used_percent: 65,
        limit_window_seconds: 604_800,
      },
      secondary_window: {
        used_percent: 100,
        limit_window_seconds: 18_000,
      },
    };

    const classified = classifyCodexRateLimitWindows(rateLimit);

    expect(classified.fiveHourWindow?.used_percent).toBe(100);
    expect(classified.weeklyWindow?.used_percent).toBe(65);
    expect(deriveCodexRateLimitUsedPercent(rateLimit)).toBe(100);
    expect(isCodexRateLimitReached(rateLimit)).toBe(true);
  });
});

describe('shouldClearInheritedCodexQuotaProgress', () => {
  const base = (overrides: Partial<CodexQuotaCycleEvidence> = {}): CodexQuotaCycleEvidence => ({
    providerWindowId: 'five-hour',
    endMs: 1_000_000,
    durationSeconds: 18_000,
    boundaryAccuracy: 'exact',
    ...overrides,
  });

  it('keeps the same boundary within the 60 second jitter', () => {
    expect(
      shouldClearInheritedCodexQuotaProgress(
        base(),
        base({ endMs: 1_030_000, boundaryAccuracy: 'estimated' })
      )
    ).toBe(false);
  });

  it.each([
    ['five-hour next cycle', 18_000_000, 'five-hour'],
    ['skipped fixed cycles', 36_000_000, 'five-hour'],
    ['weekly next cycle', 604_800_000, 'weekly'],
    ['skipped weekly cycles', 1_209_600_000, 'weekly'],
  ])('%s clears inherited progress', (_label, deltaMs, providerWindowId) => {
    const durationSeconds = providerWindowId === 'weekly' ? 604_800 : 18_000;
    expect(
      shouldClearInheritedCodexQuotaProgress(
        base({ providerWindowId, durationSeconds }),
        base({
          providerWindowId,
          endMs: 1_000_000 + deltaMs,
          durationSeconds,
        })
      )
    ).toBe(true);
  });

  it.each([
    [28 * 24 * 60 * 60, 31 * 24 * 60 * 60],
    [30 * 24 * 60 * 60, 31 * 24 * 60 * 60],
  ])(
    'accepts monthly calendar variation from %sd to %sd as rollover',
    (activeDays, observedDays) => {
      expect(
        shouldClearInheritedCodexQuotaProgress(
          base({ providerWindowId: 'monthly', durationSeconds: activeDays }),
          base({
            providerWindowId: 'monthly',
            endMs: 1_000_000 + observedDays * 1000,
            durationSeconds: observedDays,
          })
        )
      ).toBe(true);
    }
  );

  it('uses the observed duration when the active duration is missing', () => {
    expect(
      shouldClearInheritedCodexQuotaProgress(
        base({ providerWindowId: 'weekly', durationSeconds: null }),
        base({ providerWindowId: 'weekly', endMs: 1_000_000 + 604_800_000 })
      )
    ).toBe(true);
  });

  it('uses the active duration when the observed duration is missing', () => {
    expect(
      shouldClearInheritedCodexQuotaProgress(
        base({ durationSeconds: 18_000 }),
        base({ endMs: 1_000_000 + 18_000_000, durationSeconds: null })
      )
    ).toBe(true);
  });

  it('uses standard cadence when both durations are missing', () => {
    expect(
      shouldClearInheritedCodexQuotaProgress(
        base({ durationSeconds: null }),
        base({ endMs: 1_000_000 + 18_000_000, durationSeconds: null })
      )
    ).toBe(true);
  });

  it.each([
    ['indexed five-hour', 'spark-five-hour-0', 5 * 60 * 60 * 1000],
    ['indexed weekly', 'spark-weekly-0', 7 * 24 * 60 * 60 * 1000],
    ['indexed monthly', 'spark-monthly-0', 31 * 24 * 60 * 60 * 1000],
    ['indexed generic weekly', 'future-feature-weekly-0', 7 * 24 * 60 * 60 * 1000],
  ])(
    '%s uses the provider window cadence when duration is missing',
    (_label, providerWindowId, deltaMs) => {
      expect(
        shouldClearInheritedCodexQuotaProgress(
          base({ providerWindowId, durationSeconds: null, boundaryAccuracy: 'estimated' }),
          base({
            providerWindowId,
            endMs: 1_000_000 + deltaMs,
            durationSeconds: null,
            boundaryAccuracy: 'estimated',
          })
        )
      ).toBe(true);
    }
  );

  it('keeps an indexed scoped window compatible within boundary jitter when duration is missing', () => {
    expect(
      shouldClearInheritedCodexQuotaProgress(
        base({ providerWindowId: 'spark-weekly-0', durationSeconds: null }),
        base({
          providerWindowId: 'spark-weekly-0',
          endMs: 1_030_000,
          durationSeconds: null,
          boundaryAccuracy: 'estimated',
        })
      )
    ).toBe(false);
  });

  it.each([
    ['weekly bonus monthly', 'weekly-bonus-monthly-0', 30 * 24 * 60 * 60 * 1000],
    ['five hour bonus weekly', 'five-hour-bonus-weekly-0', 7 * 24 * 60 * 60 * 1000],
    [
      'cadence-like prefix with five-hour suffix',
      'monthly-preview-five-hour-3',
      5 * 60 * 60 * 1000,
    ],
  ])(
    '%s clears inherited progress from the structural cadence suffix',
    (_label, providerWindowId, deltaMs) => {
      expect(
        shouldClearInheritedCodexQuotaProgress(
          base({ providerWindowId, durationSeconds: null, boundaryAccuracy: 'estimated' }),
          base({
            providerWindowId,
            endMs: 1_000_000 + deltaMs,
            durationSeconds: null,
            boundaryAccuracy: 'estimated',
          })
        )
      ).toBe(true);
    }
  );

  it('does not infer cadence from a feature prefix without a structural suffix', () => {
    expect(
      shouldClearInheritedCodexQuotaProgress(
        base({
          providerWindowId: 'weekly-bonus',
          durationSeconds: null,
          boundaryAccuracy: 'estimated',
        }),
        base({
          providerWindowId: 'weekly-bonus',
          endMs: 1_000_000 + 7 * 24 * 60 * 60 * 1000,
          durationSeconds: null,
          boundaryAccuracy: 'estimated',
        })
      )
    ).toBe(false);
  });

  it('keeps trusted duration authoritative over a conflicting provider id suffix', () => {
    expect(
      shouldClearInheritedCodexQuotaProgress(
        base({ providerWindowId: 'weekly-bonus-monthly-0', durationSeconds: 604_800 }),
        base({
          providerWindowId: 'weekly-bonus-monthly-0',
          endMs: 1_000_000 + 7 * 24 * 60 * 60 * 1000,
          durationSeconds: 604_800,
          boundaryAccuracy: 'estimated',
        })
      )
    ).toBe(true);

    expect(
      shouldClearInheritedCodexQuotaProgress(
        base({ providerWindowId: 'weekly-bonus-monthly-0', durationSeconds: 604_800 }),
        base({
          providerWindowId: 'weekly-bonus-monthly-0',
          endMs: 1_000_000 + 30 * 24 * 60 * 60 * 1000,
          durationSeconds: 604_800,
          boundaryAccuracy: 'estimated',
        })
      )
    ).toBe(false);
  });

  it('uses strong exact boundary evidence when cadence is unavailable', () => {
    expect(
      shouldClearInheritedCodexQuotaProgress(
        base({ providerWindowId: 'future-window', durationSeconds: null }),
        base({
          providerWindowId: 'future-window',
          endMs: 1_120_000,
          durationSeconds: null,
          boundaryAccuracy: 'exact',
        })
      )
    ).toBe(true);
  });

  it('keeps a small estimated boundary drift conservative', () => {
    expect(
      shouldClearInheritedCodexQuotaProgress(
        base({ boundaryAccuracy: 'estimated' }),
        base({ endMs: 1_120_000, boundaryAccuracy: 'estimated' })
      )
    ).toBe(false);
  });

  it('clears a materially backward boundary', () => {
    expect(
      shouldClearInheritedCodexQuotaProgress(
        base(),
        base({ endMs: 900_000, boundaryAccuracy: 'estimated' })
      )
    ).toBe(true);
  });

  it('clears an incompatible duration class', () => {
    expect(
      shouldClearInheritedCodexQuotaProgress(
        base({ providerWindowId: 'five-hour', durationSeconds: 18_000 }),
        base({ providerWindowId: 'five-hour', durationSeconds: 604_800 })
      )
    ).toBe(true);
  });
});
