import { describe, expect, it } from 'vitest';
import {
  CODEX_CODE_REVIEW_SCOPE_KEY,
  CODEX_AMBIGUOUS_PROVIDER_WINDOW_PREFIX,
  CODEX_MAIN_QUOTA_SCOPE_KEY,
  CODEX_SPARK_MODEL_ID,
  classifyCodexRateLimitWindows,
  deriveCodexRateLimitUsedPercent,
  isCodexRateLimitReached,
  buildCodexQuotaWindowInfos,
  canonicalizeCodexProviderWindowId,
  inferCodexQuotaScopeFromProviderWindowId,
  isAmbiguousCodexProviderWindowId,
  isCodexCodeReviewProviderWindowId,
  isCodexKnownScopedProviderWindowId,
  isCodexLegacyAllScopeReplacement,
  isCodexMainProviderWindowId,
  isCodexMainQuotaWindow,
  normalizeCodexModelId,
  resolveCodexAdditionalQuotaScope,
  resolveCodexSnapshotQuotaLabel,
  resolveCodexUsageQuotaScope,
} from './codexQuota';
import type { CodexQuotaWindowInfo } from './codexQuota';

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
        scopeDisplayName: 'Code Review Premium',
        usedPercent: 55,
      },
    ]);
  });

  it('keeps a dynamic Additional Rate Limit name separate from its localized label', () => {
    const windows = buildCodexQuotaWindowInfos({
      additional_rate_limits: [
        {
          limit_name: 'gpt-reserve',
          rate_limit: {
            secondary_window: {
              used_percent: 25,
              limit_window_seconds: 604_800,
            },
          },
        },
      ],
    });

    expect(windows).toMatchObject([
      {
        id: 'gpt-reserve-weekly-0',
        labelKey: 'codex_quota.additional_secondary_window',
        labelParams: { name: 'gpt-reserve' },
        scopeDisplayName: 'gpt-reserve',
      },
    ]);
  });

  it('keeps a metered feature as the canonical id when the raw limit name changes', () => {
    const build = (limitName: string) =>
      buildCodexQuotaWindowInfos({
        additional_rate_limits: [
          {
            metered_feature: 'future_feature',
            limit_name: limitName,
            rate_limit: {
              secondary_window: { used_percent: 25, limit_window_seconds: 604_800 },
            },
          },
        ],
      }).find((window) => window.usedPercent === 25);

    const oldWindow = build('Old Name');
    const newWindow = build('New Name');
    expect(oldWindow).toMatchObject({
      id: 'future-feature-weekly-0',
      scopeDisplayName: 'Old Name',
      providerWindowAliases: expect.arrayContaining(['old-name-weekly-0']),
    });
    expect(newWindow).toMatchObject({
      id: 'future-feature-weekly-0',
      scopeDisplayName: 'New Name',
      providerWindowAliases: expect.arrayContaining(['new-name-weekly-0']),
    });
    expect(
      resolveCodexAdditionalQuotaScope({
        metered_feature: 'future_feature',
        limit_name: 'Old Name',
      }).legacyProviderWindowIdPrefixes
    ).toEqual(['old-name']);
  });

  it('falls back to distinct name identities for duplicate metered features', () => {
    const family = (name: string, usedPercent: number, resetAfterSeconds: number) => ({
      metered_feature: 'future_feature',
      limit_name: name,
      rate_limit: {
        secondary_window: {
          used_percent: usedPercent,
          limit_window_seconds: 604_800,
          reset_after_seconds: resetAfterSeconds,
          reset_at: 1_000_000 + resetAfterSeconds,
        },
        allowed: usedPercent < 100,
        limit_reached: usedPercent >= 100,
      },
    });
    const build = (items: ReturnType<typeof family>[]) =>
      buildCodexQuotaWindowInfos({ additional_rate_limits: items });
    const byName = (windows: CodexQuotaWindowInfo[]) =>
      new Map(windows.map((window) => [window.scopeDisplayName, window]));

    const forward = byName(build([family('Quota A', 10, 900), family('Quota B', 80, 600)]));
    const changed = byName(build([family('Quota A', 90, 300), family('Quota B', 20, 1_200)]));
    const reverse = byName(build([family('Quota B', 20, 1_200), family('Quota A', 90, 300)]));

    for (const windows of [forward, changed, reverse]) {
      expect(windows.get('Quota A')).toMatchObject({
        id: 'quota-a-weekly-0',
        modelScope: { kind: 'feature', key: 'future_feature', complete: false },
      });
      expect(windows.get('Quota B')).toMatchObject({
        id: 'quota-b-weekly-0',
        modelScope: { kind: 'feature', key: 'future_feature', complete: false },
      });
      expect(
        [...windows.values()].flatMap((window) => window.providerWindowAliases ?? [])
      ).not.toContain('future-feature-weekly-0');
    }
    expect(changed.get('Quota A')?.id).toBe(forward.get('Quota A')?.id);
    expect(changed.get('Quota B')?.id).toBe(forward.get('Quota B')?.id);
    expect(reverse.get('Quota A')?.id).toBe(forward.get('Quota A')?.id);
    expect(reverse.get('Quota B')?.id).toBe(forward.get('Quota B')?.id);
  });

  it('uses structural identities for duplicate metered features without usable names', () => {
    const family = (seconds: number, usedPercent: number) => ({
      metered_feature: 'future_feature',
      rate_limit: {
        primary_window: { limit_window_seconds: seconds, used_percent: usedPercent },
      },
    });
    const initial = buildCodexQuotaWindowInfos({
      additional_rate_limits: [family(18_000, 10), family(604_800, 80)],
    });
    const changed = buildCodexQuotaWindowInfos({
      additional_rate_limits: [family(18_000, 90), family(604_800, 20)],
    });

    expect(initial.map((window) => window.id)).toEqual([
      'additional-p-18000-s-none-five-hour-0',
      'additional-p-604800-s-none-weekly-0',
    ]);
    expect(changed.map((window) => window.id)).toEqual(initial.map((window) => window.id));
    expect(initial.flatMap((window) => window.providerWindowAliases ?? [])).not.toContain(
      'future-feature-five-hour-0'
    );
  });

  it('keeps fully ambiguous duplicate feature families out of stable-feature identity migration', () => {
    const family = (usedPercent: number, resetAfterSeconds: number) => ({
      metered_feature: 'future_feature',
      limit_name: 'Same Quota',
      rate_limit: {
        secondary_window: {
          used_percent: usedPercent,
          limit_window_seconds: 604_800,
          reset_after_seconds: resetAfterSeconds,
        },
      },
    });
    const initial = buildCodexQuotaWindowInfos({
      additional_rate_limits: [family(10, 900), family(80, 600)],
    });
    const changed = buildCodexQuotaWindowInfos({
      additional_rate_limits: [family(90, 300), family(20, 1_200)],
    });

    expect(initial.map((window) => window.id)).toEqual([
      'cpamp:ambiguous:future-feature-weekly-0',
      'cpamp:ambiguous:future-feature-weekly-1',
    ]);
    expect(changed.map((window) => window.id)).toEqual(initial.map((window) => window.id));
    expect(initial.every((window) => window.identityAmbiguous)).toBe(true);
    expect(changed.every((window) => window.identityAmbiguous)).toBe(true);
    expect(initial.flatMap((window) => window.providerWindowAliases ?? [])).not.toContain(
      'future-feature-weekly-0'
    );
  });

  it('uses metered_feature as the raw display name when limit_name is absent', () => {
    const windows = buildCodexQuotaWindowInfos({
      additional_rate_limits: [
        {
          metered_feature: 'future_feature',
          rate_limit: {
            secondary_window: {
              used_percent: 25,
              limit_window_seconds: 604_800,
            },
          },
        },
      ],
    });

    expect(windows).toMatchObject([
      {
        id: 'future-feature-weekly-0',
        labelKey: 'codex_quota.additional_secondary_window',
        labelParams: { name: 'future_feature' },
        scopeDisplayName: 'future_feature',
        modelScope: { kind: 'feature', key: 'future_feature', complete: false },
      },
    ]);
    expect(
      resolveCodexAdditionalQuotaScope({ metered_feature: 'future_feature' })
        .legacyProviderWindowIdPrefixes
    ).toEqual([]);
  });

  it('does not expose an anonymous structural identity as a display name', () => {
    const [window] = buildCodexQuotaWindowInfos({
      additional_rate_limits: [
        {
          rate_limit: {
            secondary_window: {
              used_percent: 25,
              limit_window_seconds: 604_800,
            },
          },
        },
      ],
    });

    expect(window).toMatchObject({
      id: 'additional-p-none-s-604800-weekly-0',
      modelScope: { kind: 'feature', complete: false },
    });
    expect(window?.scopeDisplayName).toBeUndefined();
  });

  it('keeps a provider feature identity separate from a conflicting raw label', () => {
    const resolution = resolveCodexAdditionalQuotaScope({
      metered_feature: 'future_feature',
      limit_name: 'Spark',
      rate_limit: { secondary_window: { limit_window_seconds: 604_800 } },
    });

    expect(resolution).toMatchObject({
      modelScope: { kind: 'feature', key: 'future_feature', complete: false },
      scopeDisplayName: 'Spark',
    });
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
    expect(byId.get('spark-weekly-0')?.scopeDisplayName).toBeUndefined();
    expect(byId.get('weekly')?.scopeDisplayName).toBeUndefined();
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
      30: 'chat-completions-five-hour-0',
      40: 'code-review-five-hour-0',
      50: 'credits-chat-completions-five-hour-0',
    });
    expect(idsByUsage(reverse)).toEqual(idsByUsage(forward));
  });

  it('keeps ambiguous additional family identity independent from dynamic quota state', () => {
    const family = (usedPercent: number, seconds: number) => ({
      rate_limit: {
        primary_window: {
          used_percent: usedPercent,
          limit_window_seconds: seconds,
        },
      },
    });
    const initial = buildCodexQuotaWindowInfos({
      additional_rate_limits: [family(30, 18_000), family(40, 18_000), family(50, 604_800)],
    });
    const changed = buildCodexQuotaWindowInfos({
      additional_rate_limits: [family(90, 18_000), family(20, 18_000), family(5, 604_800)],
    });

    expect(initial.map((window) => window.id)).toEqual([
      'additional-p-18000-s-none-five-hour-0',
      'additional-p-18000-s-none-five-hour-1',
      'additional-p-604800-s-none-weekly-0',
    ]);
    expect(changed.map((window) => window.id)).toEqual(initial.map((window) => window.id));
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

  it('uses the reserved namespace only for fully indistinguishable families', () => {
    const family = (usedPercent: number) => ({
      metered_feature: 'future_feature',
      limit_name: 'Same Quota',
      rate_limit: {
        secondary_window: {
          used_percent: usedPercent,
          limit_window_seconds: 604_800,
        },
      },
    });
    const windows = buildCodexQuotaWindowInfos({
      additional_rate_limits: [family(10), family(90)],
    });

    expect(windows.map((window) => window.id)).toEqual([
      `${CODEX_AMBIGUOUS_PROVIDER_WINDOW_PREFIX}future-feature-weekly-0`,
      `${CODEX_AMBIGUOUS_PROVIDER_WINDOW_PREFIX}future-feature-weekly-1`,
    ]);
    expect(windows.every((window) => window.identityAmbiguous === true)).toBe(true);
    expect(windows.every((window) => window.providerWindowAliases === undefined)).toBe(true);
    expect(windows.every((window) => window.modelScope?.complete === false)).toBe(true);
    expect(windows.every((window) => isAmbiguousCodexProviderWindowId(window.id))).toBe(true);
  });

  it('does not mistake legitimate ambiguous provider names for synthetic slots', () => {
    const windows = buildCodexQuotaWindowInfos({
      additional_rate_limits: [
        {
          metered_feature: 'ambiguous_feature',
          limit_name: 'My quota',
          rate_limit: {
            secondary_window: { used_percent: 20, limit_window_seconds: 604_800 },
          },
        },
        {
          limit_name: 'Ambiguous Quota',
          rate_limit: {
            secondary_window: { used_percent: 30, limit_window_seconds: 604_800 },
          },
        },
      ],
    });

    expect(windows).toHaveLength(2);
    expect(windows[0]).toMatchObject({
      id: 'ambiguous-feature-weekly-0',
      scopeDisplayName: 'My quota',
      modelScope: { kind: 'feature', key: 'ambiguous_feature', complete: false },
    });
    expect(windows[1]).toMatchObject({
      id: 'ambiguous-quota-weekly-0',
      scopeDisplayName: 'Ambiguous Quota',
      modelScope: { kind: 'feature', key: 'ambiguous_quota', complete: false },
    });
    expect(windows.every((window) => window.identityAmbiguous !== true)).toBe(true);
    expect(windows.every((window) => !isAmbiguousCodexProviderWindowId(window.id))).toBe(true);
  });

  it('preserves the reserved namespace during canonicalization', () => {
    const syntheticID = 'cpamp:ambiguous:future-feature-weekly-0';
    expect(canonicalizeCodexProviderWindowId(syntheticID, 'weekly')).toBe(syntheticID);
    expect(isAmbiguousCodexProviderWindowId(syntheticID)).toBe(true);
    expect(isAmbiguousCodexProviderWindowId('ambiguous-feature-weekly-0')).toBe(false);
  });
});

describe('strict Codex semantic classification', () => {
  it('never promotes a spark-prefixed provider feature into the Spark quota', () => {
    const windows = buildCodexQuotaWindowInfos({
      additional_rate_limits: [
        {
          metered_feature: 'spark_feature',
          limit_name: 'Spark Premium',
          rate_limit: {
            secondary_window: { used_percent: 25, limit_window_seconds: 604_800 },
          },
        },
      ],
    });
    const byId = new Map(windows.map((window) => [window.id, window]));
    expect(byId.get('spark-feature-weekly-0')).toMatchObject({
      modelScope: { kind: 'feature', key: 'spark_feature', complete: false },
      scopeDisplayName: 'Spark Premium',
    });
    expect(byId.has('spark-weekly-0')).toBe(false);

    expect(
      resolveCodexSnapshotQuotaLabel({
        providerWindowId: 'spark-feature-weekly-0',
        windowKind: 'weekly',
        modelScope: { kind: 'feature', key: 'spark_feature', complete: false },
        scopeDisplayName: 'Spark Premium',
        durationSeconds: 604_800,
      })
    ).toEqual({
      labelKey: 'codex_quota.additional_secondary_window',
      labelParams: { name: 'Spark Premium' },
    });
  });

  it('keeps codex_spark_feature an ordinary feature while codex_spark stays verified Spark', () => {
    const featureResolution = resolveCodexAdditionalQuotaScope({
      metered_feature: 'codex_spark_feature',
      rate_limit: { secondary_window: { limit_window_seconds: 604_800 } },
    });
    expect(featureResolution).toMatchObject({
      modelScope: { kind: 'feature', key: 'codex_spark_feature', complete: false },
      providerWindowIdPrefix: 'codex-spark-feature',
    });
    expect(inferCodexQuotaScopeFromProviderWindowId('codex-spark-feature-weekly-0')).toMatchObject({
      kind: 'feature',
      key: 'codex_spark_feature',
      complete: false,
    });

    const sparkResolution = resolveCodexAdditionalQuotaScope({
      metered_feature: 'codex_spark',
      rate_limit: { secondary_window: { limit_window_seconds: 604_800 } },
    });
    expect(sparkResolution.modelScope).toEqual({
      kind: 'models',
      models: [CODEX_SPARK_MODEL_ID],
      complete: true,
    });
  });

  it('canonicalizes strict Spark compatibility ids only and keeps feature ids intact', () => {
    expect(canonicalizeCodexProviderWindowId('spark')).toBe('spark');
    expect(canonicalizeCodexProviderWindowId('gpt-5-3-codex-spark-weekly-0')).toBe(
      'spark-weekly-0'
    );
    expect(canonicalizeCodexProviderWindowId('codex-spark-weekly-0')).toBe('spark-weekly-0');
    expect(canonicalizeCodexProviderWindowId('spark-0-window-7d-0')).toBe('spark-0-window-7d-0');
    expect(canonicalizeCodexProviderWindowId('codex-spark-feature-weekly-0')).toBe(
      'codex-spark-feature-weekly-0'
    );
    expect(canonicalizeCodexProviderWindowId('spark-feature-weekly-0')).toBe(
      'spark-feature-weekly-0'
    );
    expect(canonicalizeCodexProviderWindowId('spark-window-7d-0')).toBe('spark-window-7d-0');
  });

  it('recognizes the fast-coding legacy alias only with a strict generated suffix', () => {
    expect(inferCodexQuotaScopeFromProviderWindowId('fast-coding-weekly-0')).toEqual({
      kind: 'models',
      models: [CODEX_SPARK_MODEL_ID],
      complete: true,
    });
    expect(isCodexKnownScopedProviderWindowId('fast-coding-weekly-0')).toBe(true);
    expect(isCodexKnownScopedProviderWindowId('fast-coding-feature-weekly-0')).toBe(false);
    expect(inferCodexQuotaScopeFromProviderWindowId('fast-coding-feature-weekly-0')).toMatchObject({
      kind: 'feature',
      key: 'fast_coding_feature',
      complete: false,
    });
  });

  it('never re-interprets an explicit fast_coding feature scope as Spark', () => {
    expect(
      resolveCodexSnapshotQuotaLabel({
        providerWindowId: 'fast-coding-weekly-0',
        windowKind: 'weekly',
        modelScope: { kind: 'feature', key: 'fast_coding', complete: false },
        scopeDisplayName: 'Fast Coding Beta',
        durationSeconds: 604_800,
      })
    ).toEqual({
      labelKey: 'codex_quota.additional_secondary_window',
      labelParams: { name: 'Fast Coding Beta' },
    });
    expect(
      resolveCodexSnapshotQuotaLabel({
        providerWindowId: 'fast-coding-weekly-0',
        windowKind: 'weekly',
        modelScope: { kind: 'feature', key: 'fast_coding', complete: false },
      })
    ).toBeUndefined();
    expect(
      resolveCodexSnapshotQuotaLabel({
        providerWindowId: 'fast-coding-weekly-0',
        windowKind: 'weekly',
        modelScope: { kind: 'all', complete: true },
      })
    ).toEqual({
      labelKey: 'codex_quota.additional_secondary_window',
      labelParams: { name: 'Spark' },
    });
  });

  it('keeps window-prefixed provider features ordinary while main generic ids stay Main', () => {
    const windows = buildCodexQuotaWindowInfos({
      additional_rate_limits: [
        {
          metered_feature: 'window_feature',
          rate_limit: { secondary_window: { used_percent: 10, limit_window_seconds: 604_800 } },
        },
      ],
    });
    expect(windows.find((window) => window.id === 'window-feature-weekly-0')).toMatchObject({
      modelScope: { kind: 'feature', key: 'window_feature', complete: false },
    });

    expect(isCodexMainProviderWindowId('window-7d-0')).toBe(true);
    expect(isCodexMainProviderWindowId('window-12h-1')).toBe(true);
    expect(isCodexMainProviderWindowId('window-unknown-0')).toBe(true);
    expect(isCodexMainProviderWindowId('window-feature-weekly-0')).toBe(false);
    expect(isCodexMainProviderWindowId('window-premium-weekly-0')).toBe(false);
    expect(isCodexMainProviderWindowId('window-beta')).toBe(false);
  });

  it('separates top-level Code Review windows from ordinary code_review Additional families', () => {
    expect(isCodexCodeReviewProviderWindowId('code-review')).toBe(true);
    expect(isCodexCodeReviewProviderWindowId('code-review-five-hour')).toBe(true);
    expect(isCodexCodeReviewProviderWindowId('code-review-weekly')).toBe(true);
    expect(isCodexCodeReviewProviderWindowId('code-review-monthly')).toBe(true);
    expect(isCodexCodeReviewProviderWindowId('code-review-window-7d-0')).toBe(true);
    expect(isCodexCodeReviewProviderWindowId('code-review-window-unknown-0')).toBe(true);
    expect(isCodexCodeReviewProviderWindowId('code-review-weekly-0')).toBe(false);
    expect(isCodexCodeReviewProviderWindowId('code-review-premium-weekly-0')).toBe(false);

    const premium = buildCodexQuotaWindowInfos({
      additional_rate_limits: [
        {
          limit_name: 'Code Review Premium',
          rate_limit: { secondary_window: { used_percent: 5, limit_window_seconds: 604_800 } },
        },
      ],
    });
    expect(premium.find((window) => window.id === 'code-review-premium-weekly-0')).toMatchObject({
      modelScope: { kind: 'feature', key: 'code_review_premium', complete: false },
    });

    const ordinary = buildCodexQuotaWindowInfos({
      additional_rate_limits: [
        {
          metered_feature: 'code_review',
          rate_limit: { secondary_window: { used_percent: 5, limit_window_seconds: 604_800 } },
        },
      ],
    });
    expect(ordinary.find((window) => window.id === 'code-review-weekly-0')).toMatchObject({
      modelScope: { kind: 'feature', key: CODEX_CODE_REVIEW_SCOPE_KEY, complete: false },
    });

    expect(inferCodexQuotaScopeFromProviderWindowId('code-review-weekly')).toMatchObject({
      kind: 'feature',
      key: CODEX_CODE_REVIEW_SCOPE_KEY,
      complete: false,
    });
    expect(isCodexKnownScopedProviderWindowId('code-review-weekly')).toBe(true);
    expect(isCodexKnownScopedProviderWindowId('code-review-weekly-0')).toBe(false);
    expect(isCodexKnownScopedProviderWindowId('code-review-premium-weekly-0')).toBe(false);
  });

  it('requires an explicit or legacy scope before applying Code Review snapshot labels', () => {
    expect(
      resolveCodexSnapshotQuotaLabel({
        providerWindowId: 'code-review-weekly',
        windowKind: 'weekly',
        modelScope: { kind: 'feature', key: CODEX_CODE_REVIEW_SCOPE_KEY, complete: false },
      })
    ).toEqual({ labelKey: 'codex_quota.code_review_secondary_window' });
    expect(
      resolveCodexSnapshotQuotaLabel({
        providerWindowId: 'code-review-window-7d-0',
        windowKind: 'weekly',
        modelScope: { kind: 'all', complete: true },
        durationSeconds: 7 * 24 * 60 * 60,
      })
    ).toEqual({
      labelKey: 'codex_quota.code_review_generic_window',
      labelParams: { duration: '7d' },
    });
    expect(
      resolveCodexSnapshotQuotaLabel({
        providerWindowId: 'code-review-weekly',
        windowKind: 'weekly',
        modelScope: { kind: 'feature', key: 'code_review_premium', complete: false },
        scopeDisplayName: 'Code Review Premium',
      })
    ).toEqual({
      labelKey: 'codex_quota.additional_secondary_window',
      labelParams: { name: 'Code Review Premium' },
    });
  });

  it('fails closed for legacy all snapshots with known-prefix impostor ids', () => {
    expect(inferCodexQuotaScopeFromProviderWindowId('spark-feature-weekly-0')).toMatchObject({
      kind: 'feature',
      key: 'spark_feature',
      complete: false,
    });
    expect(inferCodexQuotaScopeFromProviderWindowId('window-feature-weekly-0')).toMatchObject({
      kind: 'feature',
      key: 'window_feature',
      complete: false,
    });
    expect(inferCodexQuotaScopeFromProviderWindowId('code-review-premium-weekly-0')).toMatchObject({
      kind: 'feature',
      key: 'code_review_premium',
      complete: false,
    });
    expect(
      resolveCodexSnapshotQuotaLabel({
        providerWindowId: 'spark-feature-weekly-0',
        windowKind: 'weekly',
        modelScope: { kind: 'all', complete: true },
      })
    ).toBeUndefined();
  });
});
