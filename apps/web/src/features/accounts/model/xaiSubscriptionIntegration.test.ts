import { describe, expect, it } from 'vitest';
import type { AuthFileItem, XaiBillingSummary, XaiSubscriptionInfo } from '@/types';
import { buildQuotaFailureState, XAI_CONFIG } from '@/components/quota/quotaConfigs';
import {
  buildQuotaCredentialIdentity,
  getQuotaCredentialStoreKey,
} from '@/utils/quota/credentialScope';
import { buildAccountRows } from './accountRows';
import { buildAccountListItem } from './accountListPresentation';
import { buildAccountDetailViewModel } from './accountDetailViewModel';
import type { AccountQuotaStores } from './accountQuotaSummary';

const refreshedAt = Date.parse('2026-10-19T04:00:00Z');
const observedAt = refreshedAt + 60_000;
const quotaWindows = [
  {
    key: 'credits-period',
    label: 'Weekly credits',
    kind: 'weekly' as const,
    remainingPercent: 43,
    usedPercent: 57,
    resetLabel: '2026-10-22T07:11:51Z',
    resetAtMs: Date.parse('2026-10-22T07:11:51Z'),
    resetAccuracy: 'exact' as const,
  },
];

function makeRow(
  subscription?: XaiSubscriptionInfo,
  options: {
    filePlan?: string;
    quotaAt?: number;
    cachedAuthIndex?: string;
    refreshFailed?: boolean;
  } = {}
) {
  const file: AuthFileItem = {
    name: 'xai.json',
    type: 'xai',
    authIndex: 'current',
    last_refresh: new Date(refreshedAt).toISOString(),
    planType: options.filePlan,
  };
  const cachedFile = options.cachedAuthIndex
    ? { ...file, authIndex: options.cachedAuthIndex }
    : file;
  const billing: XaiBillingSummary = {
    periodType: 'weekly',
    usagePercent: 57,
    productUsage: [{ product: 'GrokBuild', usagePercent: 57 }],
    periodEnd: '2026-10-22T07:11:51Z',
    monthlyLimitCents: null,
    usedCents: null,
    includedUsedCents: null,
    onDemandCapCents: null,
    onDemandUsedCents: null,
    onDemandUsedPercent: null,
    usedPercent: null,
    subscription,
  };
  const successState = {
    ...buildQuotaCredentialIdentity(cachedFile),
    status: 'success' as const,
    billing,
    fetchedAtMs: options.quotaAt ?? observedAt,
  };
  const quotaState = options.refreshFailed
    ? buildQuotaFailureState(
        XAI_CONFIG,
        'quota refresh failed',
        503,
        cachedFile,
        successState,
        (options.quotaAt ?? observedAt) + 120_000
      )
    : successState;
  const stores: AccountQuotaStores = {
    antigravityQuota: {},
    claudeQuota: {},
    codexQuota: {},
    kimiQuota: {},
    devinQuota: {},
    metaQuota: {},
    xaiQuota: {
      [getQuotaCredentialStoreKey(cachedFile)]: quotaState,
    },
  };
  return buildAccountRows([file], stores)[0];
}

describe('xAI subscription data reaches account presentation', () => {
  it('shows Heavy and uses the existing quota rules without editing the auth file', () => {
    const row = makeRow({ planType: 'SuperGrok Heavy', source: 'user', observedAtMs: observedAt });
    expect(row.raw.planType).toBeUndefined();
    expect(row.planType).toBe('SuperGrok Heavy');
    const list = buildAccountListItem(row, { quotaWindows });
    expect(list.identity.planPresentation?.fullLabel).toBe('SuperGrok Heavy');
    expect(list.health.status).toBe('available');
    expect(buildAccountDetailViewModel(row, { quotaWindows }).overview.capacity).toMatchObject({
      hasData: true,
      remainingPercent: 43,
    });
  });

  it('keeps observed quota visible and explains why the plan is still unknown', () => {
    const row = makeRow();
    const list = buildAccountListItem(row, { quotaWindows });
    expect(list.health.status).toBe('raw');
    expect(list.health.reasonKey).toBe('accounts.health_reason_xai_plan_unknown');
    expect(row.quota.remainingPercent).toBeNull();
    expect(list.quota).toMatchObject({ remainingPercent: 43, usedPercent: 57 });
    const detail = buildAccountDetailViewModel(row, { quotaWindows });
    expect(detail.overview.decision.reasonKey).toBe('accounts.health_reason_xai_plan_unknown');
    expect(detail.overview.capacity).toMatchObject({
      hasData: true,
      remainingPercent: 43,
      descriptionKey: 'accounts.detail_overview_capacity_desc',
    });
    expect(detail.strategy.recommendation).toBeNull();
  });

  it('does not fall back to stale file metadata after an explicit unknown live tier', () => {
    const row = makeRow(
      { planType: null, source: 'user', observedAtMs: observedAt },
      { filePlan: 'SuperGrok Heavy' }
    );
    expect(row.planType).toBeNull();
    expect(buildAccountListItem(row, { quotaWindows }).health.status).toBe('raw');
  });

  it('cannot combine an older paid subscription with a newer quota observation', () => {
    const row = makeRow(
      { planType: 'SuperGrok Heavy', source: 'user', observedAtMs: observedAt },
      { quotaAt: observedAt + 1, filePlan: 'SuperGrok Heavy' }
    );
    expect(row.planType).toBeNull();
    expect(row.quota.remainingPercent).toBeNull();
    expect(buildAccountListItem(row, { quotaWindows }).health.status).toBe('raw');
    expect(buildAccountListItem(row, { quotaWindows }).quota.remainingPercent).toBe(43);
  });

  it.each([0, 43])(
    'displays %s%% informational quota without promoting health or recommendations',
    (remainingPercent) => {
      const row = makeRow();
      const windows = [
        { ...quotaWindows[0], remainingPercent, usedPercent: 100 - remainingPercent },
      ];
      const list = buildAccountListItem(row, { quotaWindows: windows });
      const detail = buildAccountDetailViewModel(row, { quotaWindows: windows });
      expect(list.quota.remainingPercent).toBe(remainingPercent);
      expect(row.quota.status).toBe('unknown');
      expect(list.health.status).toBe('raw');
      expect(detail.overview.decision.reasonKey).toBe('accounts.health_reason_xai_plan_unknown');
      expect(detail.strategy.recommendation).toBeNull();
    }
  );

  it('explains unconfirmed availability consistently for a Free plan in list and detail', () => {
    const row = makeRow({ planType: 'Free', source: 'user', observedAtMs: observedAt });
    expect(buildAccountListItem(row, { quotaWindows }).health.reasonKey).toBe(
      'accounts.health_reason_xai_quota_unconfirmed'
    );
    expect(buildAccountDetailViewModel(row, { quotaWindows }).overview.decision.reasonKey).toBe(
      'accounts.health_reason_xai_quota_unconfirmed'
    );
  });

  it.each(['disabled', 'error', 'loading'] as const)(
    'does not replace a %s row with a cached informational percentage',
    (state) => {
      const row = makeRow();
      const changed = {
        ...row,
        disabled: state === 'disabled',
        quota: { ...row.quota, status: state },
      };
      expect(buildAccountListItem(changed, { quotaWindows }).quota.remainingPercent).toBeNull();
    }
  );

  it('does not override an intentionally unknown paid-plan quota summary', () => {
    const row = makeRow();
    row.planType = 'SuperGrok Heavy';
    expect(buildAccountListItem(row, { quotaWindows }).quota.remainingPercent).toBeNull();
  });

  it('does not reuse subscription evidence from before credential refresh', () => {
    const row = makeRow({
      planType: 'SuperGrok Heavy',
      source: 'settings',
      observedAtMs: refreshedAt - 1,
    });
    expect(row.planType).toBeNull();
    expect(buildAccountListItem(row, { quotaWindows }).health.status).toBe('raw');
  });

  it('does not promote old quota just because a fresh Heavy label was found', () => {
    const row = makeRow(
      { planType: 'SuperGrok Heavy', source: 'user', observedAtMs: observedAt },
      { quotaAt: refreshedAt - 1 }
    );
    expect(row.planType).toBe('SuperGrok Heavy');
    expect(buildAccountListItem(row, { quotaWindows }).health.status).toBe('raw');
  });

  it('does not reuse a plan from another credential with the same file name', () => {
    const row = makeRow(
      { planType: 'SuperGrok Heavy', source: 'user', observedAtMs: observedAt },
      { cachedAuthIndex: 'old-account' }
    );
    expect(row.planType).toBeNull();
  });

  it('retains a supplied file plan when there is no matching quota entry', () => {
    const row = makeRow(undefined, { filePlan: 'SuperGrok Heavy', cachedAuthIndex: 'old-account' });
    expect(row.planType).toBe('SuperGrok Heavy');
  });

  it('keeps a live Free plan over a stale Heavy file plan after a quota refresh failure', () => {
    const row = makeRow(
      { planType: 'Free', source: 'user', observedAtMs: observedAt },
      { filePlan: 'SuperGrok Heavy', refreshFailed: true }
    );
    expect(row.quota.status).toBe('error');
    expect(row.planType).toBe('Free');
    expect(buildAccountListItem(row, { quotaWindows }).identity.planPresentation?.fullLabel).toBe(
      'Free'
    );
  });

  it('does not infer paid availability from a Free subscription with informational quota', () => {
    const row = makeRow({ planType: 'Free', source: 'user', observedAtMs: observedAt });
    expect(row.planType).toBe('Free');
    expect(buildAccountListItem(row, { quotaWindows }).health.status).toBe('raw');
    expect(buildAccountListItem(row, { quotaWindows }).health.reasonKey).toBe(
      'accounts.health_reason_xai_quota_unconfirmed'
    );
  });
});
