import { describe, expect, it } from 'vitest';
import type { AuthFileItem } from '@/types';
import type { UsageHeaderSnapshot } from '@/services/api/usageService';
import { buildCodexQuotaWindowInfos } from './quota/codexQuota';
import {
  buildObservedCodexQuotaFromHeaderSnapshot,
  buildUsageHeaderSnapshotLookup,
  getHeaderSnapshotReachedWindowKind,
  getHeaderSnapshotPlanType,
  getHeaderSnapshotSummaryWindowKind,
  getHeaderSnapshotWindowUsedPercent,
  getHighConfidenceUsageHeaderSnapshotForAuthFile,
  getUsageHeaderSnapshotForAuthFile,
  hasUsageHeaderDiagnosticSignal,
  hasUsageHeaderQuotaSignal,
} from './usageHeaderSnapshots';

describe('buildObservedCodexQuotaFromHeaderSnapshot', () => {
  it('normalizes Codex header quota metadata into usage quota windows', () => {
    const snapshot: UsageHeaderSnapshot = {
      event_hash: 'event-test',
      timestamp_ms: 1_700_000_000_000,
      response_metadata: {
        quota: {
          plan_type: 'free',
          active_limit: 'premium',
          credits_has_credits: false,
          credits_unlimited: false,
          rate_limit_reached_type: 'workspace_member_credits_depleted',
          summary_window_kind: 'monthly',
          summary_window_source: 'primary',
          reached_window_kind: 'unknown',
          reached_window_source: 'unknown',
          primary_over_secondary_limit_percent: 20,
          primary: {
            used_percent: 20,
            reset_at_ms: 1_784_805_897_000,
            window_minutes: 43_200,
          },
          secondary: {
            used_percent: 0,
            window_minutes: 0,
          },
        },
      },
    };

    const observed = buildObservedCodexQuotaFromHeaderSnapshot(snapshot);

    expect(observed).toMatchObject({
      planType: 'free',
      activeLimit: 'premium',
      creditsHasCredits: false,
      creditsUnlimited: false,
      rateLimitReachedType: 'workspace_member_credits_depleted',
      summaryWindowKind: 'monthly',
      summaryWindowSource: 'primary',
      reachedWindowKind: 'unknown',
      reachedWindowSource: 'unknown',
      primaryOverSecondaryLimitPercent: 20,
    });
    expect(getHeaderSnapshotSummaryWindowKind(snapshot)).toBe('monthly');
    expect(getHeaderSnapshotReachedWindowKind(snapshot)).toBe('unknown');
    expect(observed?.payload?.rate_limit?.primary_window).toMatchObject({
      used_percent: 20,
      reset_at: 1_784_805_897,
      limit_window_seconds: 2_592_000,
    });
    expect(observed?.payload?.rate_limit?.secondary_window).toBeUndefined();

    const windows = buildCodexQuotaWindowInfos(observed?.payload ?? {});
    expect(windows).toMatchObject([
      {
        id: 'monthly',
        labelKey: 'codex_quota.monthly_window',
        usedPercent: 20,
        limitWindowSeconds: 2_592_000,
      },
    ]);
  });

  it('keeps observed Codex primary and secondary windows separate', () => {
    const snapshot: UsageHeaderSnapshot = {
      event_hash: 'event-multi-window',
      timestamp_ms: 1_700_000_000_000,
      response_metadata: {
        quota: {
          plan_type: 'plus',
          summary_window_kind: 'five_hour',
          summary_window_source: 'primary',
          reached_window_kind: 'five_hour',
          reached_window_source: 'primary',
          primary: {
            used_percent: 100,
            reset_at_ms: 1_700_018_000_000,
            window_minutes: 300,
          },
          secondary: {
            used_percent: 25,
            reset_at_ms: 1_700_604_800_000,
            window_minutes: 10_080,
          },
        },
      },
    };

    const observed = buildObservedCodexQuotaFromHeaderSnapshot(snapshot);

    expect(observed).toMatchObject({
      summaryWindowKind: 'five_hour',
      summaryWindowSource: 'primary',
      reachedWindowKind: 'five_hour',
      reachedWindowSource: 'primary',
    });
    const windows = buildCodexQuotaWindowInfos(observed?.payload ?? {});
    expect(windows).toMatchObject([
      {
        id: 'five-hour',
        labelKey: 'codex_quota.primary_window',
        usedPercent: 100,
        limitWindowSeconds: 18_000,
      },
      {
        id: 'weekly',
        labelKey: 'codex_quota.secondary_window',
        usedPercent: 25,
        limitWindowSeconds: 604_800,
      },
    ]);
  });

  it('reads used percent for the reached quota window', () => {
    const snapshot: UsageHeaderSnapshot = {
      event_hash: 'event-reached-window-percent',
      timestamp_ms: 1_700_000_000_000,
      response_metadata: {
        quota: {
          rate_limit_reached_type: 'secondary',
          reached_window_kind: 'weekly',
          reached_window_source: 'secondary',
          primary: {
            used_percent: 99,
            window_minutes: 300,
          },
          secondary: {
            used_percent: 98,
            window_minutes: 10_080,
          },
        },
      },
    };

    expect(getHeaderSnapshotWindowUsedPercent(snapshot, 'five_hour')).toBe(99);
    expect(getHeaderSnapshotWindowUsedPercent(snapshot, 'weekly')).toBe(98);
    expect(getHeaderSnapshotWindowUsedPercent(snapshot, 'monthly')).toBeNull();
  });

  it('does not treat trace-only metadata as quota evidence', () => {
    const snapshot: UsageHeaderSnapshot = {
      event_hash: 'trace-only',
      timestamp_ms: 1_700_000_000_000,
      response_metadata: {
        trace: {
          primary_trace_id: 'req-trace-only',
        },
      },
      header_trace_id: 'req-trace-only',
    };

    expect(hasUsageHeaderDiagnosticSignal(snapshot)).toBe(true);
    expect(hasUsageHeaderQuotaSignal(snapshot)).toBe(false);
    expect(buildObservedCodexQuotaFromHeaderSnapshot(snapshot)).toBeNull();
  });

  it('requires high-confidence identity matches for auth-file quota fallback', () => {
    const lookup = buildUsageHeaderSnapshotLookup([
      {
        event_hash: 'auth-index-only',
        timestamp_ms: 1_700_000_000_200,
        auth_index: '7',
        response_metadata: {
          quota: {
            plan_type: 'team',
          },
        },
      },
      {
        event_hash: 'file-and-auth-index',
        timestamp_ms: 1_700_000_000_100,
        auth_file_snapshot: 'codex-account.json',
        auth_index: '7',
        response_metadata: {
          quota: {
            plan_type: 'plus',
          },
        },
      },
    ]);
    const file = {
      name: 'codex-account.json',
      provider: 'codex',
      authIndex: '7',
    } as AuthFileItem;

    expect(getUsageHeaderSnapshotForAuthFile(lookup, file)?.event_hash).toBe('file-and-auth-index');
    expect(getHighConfidenceUsageHeaderSnapshotForAuthFile(lookup, file)?.event_hash).toBe(
      'file-and-auth-index'
    );

    const unmatchedFile = {
      name: 'other-codex.json',
      provider: 'codex',
      authIndex: '7',
    } as AuthFileItem;
    expect(getUsageHeaderSnapshotForAuthFile(lookup, unmatchedFile)?.event_hash).toBe(
      'auth-index-only'
    );
    expect(getHighConfidenceUsageHeaderSnapshotForAuthFile(lookup, unmatchedFile)).toBeUndefined();
  });

  it('does not use same-file header snapshots across different auth indexes', () => {
    const lookup = buildUsageHeaderSnapshotLookup([
      {
        event_hash: 'shared-file-index-0',
        timestamp_ms: 1_700_000_000_200,
        auth_file_snapshot: 'shared-codex.json',
        auth_index: '0',
        response_metadata: {
          quota: {
            plan_type: 'plus',
          },
        },
      },
      {
        event_hash: 'shared-file-index-1',
        timestamp_ms: 1_700_000_000_100,
        auth_file_snapshot: 'shared-codex.json',
        auth_index: '1',
        response_metadata: {
          quota: {
            plan_type: 'team',
          },
        },
      },
    ]);
    const file = {
      name: 'shared-codex.json',
      provider: 'codex',
      authIndex: '1',
    } as AuthFileItem;
    const missingIndexFile = {
      name: 'shared-codex.json',
      provider: 'codex',
      authIndex: '2',
    } as AuthFileItem;

    expect(getHighConfidenceUsageHeaderSnapshotForAuthFile(lookup, file)?.event_hash).toBe(
      'shared-file-index-1'
    );
    expect(getHighConfidenceUsageHeaderSnapshotForAuthFile(lookup, missingIndexFile)).toBeUndefined();
  });

  it('does not use same-account header snapshots as high-confidence auth-file quota evidence', () => {
    const lookup = buildUsageHeaderSnapshotLookup([
      {
        event_hash: 'free-workspace-header',
        timestamp_ms: 1_700_000_000_200,
        auth_file_snapshot: 'codex-free.json',
        auth_index: 'free',
        account_snapshot: 'user@example.com',
        response_metadata: {
          quota: {
            plan_type: 'free',
          },
        },
      },
    ]);
    const teamFile = {
      name: 'codex-team.json',
      provider: 'codex',
      authIndex: 'team',
      account: 'user@example.com',
    } as AuthFileItem;

    expect(getUsageHeaderSnapshotForAuthFile(lookup, teamFile)?.event_hash).toBe(
      'free-workspace-header'
    );
    expect(getHighConfidenceUsageHeaderSnapshotForAuthFile(lookup, teamFile)).toBeUndefined();
  });

  it('does not use active limit as the plan type', () => {
    const snapshot: UsageHeaderSnapshot = {
      event_hash: 'active-limit-only',
      timestamp_ms: 1_700_000_000_000,
      response_metadata: {
        quota: {
          active_limit: 'premium',
        },
      },
    };

    expect(getHeaderSnapshotPlanType(snapshot)).toBe('');
    expect(hasUsageHeaderQuotaSignal(snapshot)).toBe(true);
  });
});
