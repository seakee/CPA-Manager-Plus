import { describe, expect, it } from 'vitest';
import type { AccountRow } from './accountRows';
import type { CodexQuotaState } from '@/types';
import { emptyCodexSubscriptionExtras, type CodexSubscriptionRecord } from './codexSubscription';
import {
  buildAccountSubscriptionPresentation,
  parseValidSubscriptionUntilMs,
  resolveCodexSubscriptionUntilMs,
} from './accountSubscriptionPresentation';

const makeAccountRow = (overrides: Partial<AccountRow> = {}): AccountRow =>
  ({
    key: 'test:row:1',
    selectionKey: 'test:row:1',
    fileName: 'codex-test.json',
    provider: 'codex',
    accountLabel: 'test@example.com',
    planType: 'plus',
    priority: 0,
    disabled: false,
    runtimeOnly: false,
    authIndex: '1',
    updatedAtMs: 1000,
    statusMessage: '',
    raw: {
      name: 'codex-test.json',
      type: 'codex',
    },
    usage: {
      success: 0,
      failure: 0,
      successRate: 100,
      recentRequests: [],
    },
    quota: {
      status: 'ok',
      remainingPercent: 100,
      usedPercent: 0,
      resetLabel: '-',
      resetAtMs: null,
      resetAccuracy: 'unknown',
      planType: 'plus',
      source: 'none',
    },
    ...overrides,
  }) as AccountRow;

const makeCodexQuota = (overrides: Record<string, unknown> = {}): CodexQuotaState =>
  ({
    status: 'success',
    windows: [],
    ...overrides,
  }) as unknown as CodexQuotaState;

describe('accountSubscriptionPresentation', () => {
  const FIXED_NOW_MS = 1_700_000_000_000; // e.g. base now

  describe('parseValidSubscriptionUntilMs', () => {
    it('supports seconds epoch (< 1e12)', () => {
      const seconds = 1_700_000_000;
      expect(parseValidSubscriptionUntilMs(seconds)).toBe(1_700_000_000_000);
      expect(parseValidSubscriptionUntilMs(`${seconds}`)).toBe(1_700_000_000_000);
    });

    it('supports milliseconds epoch (>= 1e12)', () => {
      const ms = 1_700_000_000_000;
      expect(parseValidSubscriptionUntilMs(ms)).toBe(ms);
      expect(parseValidSubscriptionUntilMs(`${ms}`)).toBe(ms);
    });

    it('supports ISO timestamp', () => {
      const iso = '2026-10-31T23:59:59Z';
      const expected = Date.parse(iso);
      expect(parseValidSubscriptionUntilMs(iso)).toBe(expected);
    });

    it('returns null for invalid/expired/negative inputs', () => {
      expect(parseValidSubscriptionUntilMs(null)).toBeNull();
      expect(parseValidSubscriptionUntilMs(undefined)).toBeNull();
      expect(parseValidSubscriptionUntilMs('')).toBeNull();
      expect(parseValidSubscriptionUntilMs('not-a-date')).toBeNull();
      expect(parseValidSubscriptionUntilMs(0)).toBeNull();
      expect(parseValidSubscriptionUntilMs(-100)).toBeNull();
    });
  });

  describe('resolveCodexSubscriptionUntilMs', () => {
    it('uses live codexQuota.subscriptionActiveUntil', () => {
      const row = makeAccountRow();
      const quota = makeCodexQuota({
        planType: 'plus',
        subscriptionActiveUntil: 1_700_100_000,
      });
      const result = resolveCodexSubscriptionUntilMs(row, quota);
      expect(result.liveSubscriptionUntilMs).toBe(1_700_100_000_000);
      expect(result.subscriptionUntilMs).toBe(1_700_100_000_000);
    });

    it('prioritizes live quota over token claim', () => {
      const row = makeAccountRow({
        raw: {
          name: 'codex-test.json',
          type: 'codex',
          metadata: {
            id_token: `header.${btoa(JSON.stringify({ chatgpt_subscription_active_until: 1_600_000_000 }))}.sig`,
          },
        },
      });
      const quota = makeCodexQuota({
        planType: 'plus',
        subscriptionActiveUntil: 1_800_000_000,
      });
      const result = resolveCodexSubscriptionUntilMs(row, quota);
      expect(result.liveSubscriptionUntilMs).toBe(1_800_000_000_000);
      expect(result.tokenSubscriptionUntilMs).toBe(1_600_000_000_000);
      expect(result.subscriptionUntilMs).toBe(1_800_000_000_000);
    });

    it('falls back to token claim when live quota is missing', () => {
      const row = makeAccountRow({
        raw: {
          name: 'codex-test.json',
          type: 'codex',
          id_token: `header.${btoa(JSON.stringify({ chatgpt_subscription_active_until: 1_750_000_000 }))}.sig`,
        },
      });
      const result = resolveCodexSubscriptionUntilMs(row, null);
      expect(result.liveSubscriptionUntilMs).toBeNull();
      expect(result.tokenSubscriptionUntilMs).toBe(1_750_000_000_000);
      expect(result.subscriptionUntilMs).toBe(1_750_000_000_000);
    });

    it('prioritizes subscriptions.activeUntilMs over wham and JWT', () => {
      const row = makeAccountRow({
        raw: {
          name: 'codex-test.json',
          type: 'codex',
          id_token: `header.${btoa(JSON.stringify({ chatgpt_subscription_active_until: 1_600_000_000 }))}.sig`,
        },
      });
      const quota = makeCodexQuota({
        planType: 'plus',
        subscriptionActiveUntil: 1_700_000_000,
      });
      const subscriptionsRecord: CodexSubscriptionRecord = {
        accountId: 'acct_plus',
        planType: 'plus',
        activeStartMs: 1_750_000_000_000,
        activeUntilMs: 1_800_000_000_000,
        billingPeriod: 'monthly',
        willRenew: true,
        fetchedAtMs: FIXED_NOW_MS,
        source: 'subscriptions',
        extras: emptyCodexSubscriptionExtras(),
      };
      const result = resolveCodexSubscriptionUntilMs(row, quota, subscriptionsRecord);
      expect(result.liveSubscriptionUntilMs).toBe(1_800_000_000_000);
      expect(result.tokenSubscriptionUntilMs).toBe(1_600_000_000_000);
      expect(result.subscriptionUntilMs).toBe(1_800_000_000_000);
    });

    it('returns all nulls for non-Codex provider', () => {
      const row = makeAccountRow({ provider: 'claude' });
      const quota = makeCodexQuota({
        subscriptionActiveUntil: 1_800_000_000,
      });
      const result = resolveCodexSubscriptionUntilMs(row, quota);
      expect(result.subscriptionUntilMs).toBeNull();
    });
  });

  describe('buildAccountSubscriptionPresentation', () => {
    it('calculates remainingDays correctly for paid Codex with future expiry', () => {
      const futureMs = FIXED_NOW_MS + 23 * 86_400_000;
      const row = makeAccountRow();
      const quota = makeCodexQuota({
        planType: 'plus',
        subscriptionActiveUntil: futureMs,
      });
      const result = buildAccountSubscriptionPresentation({
        row,
        codexQuota: quota,
        nowMs: FIXED_NOW_MS,
      });

      expect(result.isPaidCodex).toBe(true);
      expect(result.subscriptionUntilMs).toBe(futureMs);
      expect(result.remainingDays).toBe(23);
      expect(result.planPresentation?.shortLabel).toBe('Plus');
    });

    it('does not display remainingDays when expired', () => {
      const pastMs = FIXED_NOW_MS - 1000;
      const row = makeAccountRow();
      const quota = makeCodexQuota({
        planType: 'plus',
        subscriptionActiveUntil: pastMs,
      });
      const result = buildAccountSubscriptionPresentation({
        row,
        codexQuota: quota,
        nowMs: FIXED_NOW_MS,
      });

      expect(result.isPaidCodex).toBe(true);
      expect(result.subscriptionUntilMs).toBe(pastMs);
      expect(result.remainingDays).toBeNull();
    });

    it('does not display remainingDays for Free plan', () => {
      const futureMs = FIXED_NOW_MS + 30 * 86_400_000;
      const row = makeAccountRow({ planType: 'free' });
      const quota = makeCodexQuota({
        planType: 'free',
        subscriptionActiveUntil: futureMs,
      });
      const result = buildAccountSubscriptionPresentation({
        row,
        codexQuota: quota,
        nowMs: FIXED_NOW_MS,
      });

      expect(result.isPaidCodex).toBe(false);
      expect(result.subscriptionUntilMs).toBeNull();
      expect(result.remainingDays).toBeNull();
    });

    it('does not display remainingDays for invalid expiry', () => {
      const row = makeAccountRow();
      const quota = makeCodexQuota({
        planType: 'team',
        subscriptionActiveUntil: 'invalid-date',
      });
      const result = buildAccountSubscriptionPresentation({
        row,
        codexQuota: quota,
        nowMs: FIXED_NOW_MS,
      });

      expect(result.isPaidCodex).toBe(true);
      expect(result.subscriptionUntilMs).toBeNull();
      expect(result.remainingDays).toBeNull();
    });

    it('uses subscriptions remainingDays for list and sort consumers', () => {
      const subscriptionsUntil = FIXED_NOW_MS + 11 * 86_400_000;
      const result = buildAccountSubscriptionPresentation({
        row: makeAccountRow(),
        codexQuota: makeCodexQuota({
          planType: 'plus',
          subscriptionActiveUntil: FIXED_NOW_MS + 3 * 86_400_000,
        }),
        subscriptionsRecord: {
          accountId: 'acct_plus',
          planType: 'plus',
          activeStartMs: FIXED_NOW_MS - 20 * 86_400_000,
          activeUntilMs: subscriptionsUntil,
          billingPeriod: 'monthly',
          willRenew: true,
          fetchedAtMs: FIXED_NOW_MS,
          source: 'subscriptions',
          extras: emptyCodexSubscriptionExtras(),
        },
        nowMs: FIXED_NOW_MS,
      });

      expect(result.subscriptionUntilMs).toBe(subscriptionsUntil);
      expect(result.remainingDays).toBe(11);
    });

    it('does not display remainingDays for non-Codex provider', () => {
      const row = makeAccountRow({ provider: 'claude', planType: 'pro' });
      const result = buildAccountSubscriptionPresentation({
        row,
        nowMs: FIXED_NOW_MS,
      });

      expect(result.isPaidCodex).toBe(false);
      expect(result.remainingDays).toBeNull();
    });
  });
});
