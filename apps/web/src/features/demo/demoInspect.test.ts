import { describe, expect, it, vi } from 'vitest';
import {
  getDemoAuthFiles,
  getDemoQuotaStoreState,
  getDemoAccountWindowUsage,
  resetDemoEvidenceEpoch,
} from './demoFixtures';
import { buildAccountRows, sortAccountRows } from '@/features/accounts/model/accountRows';
import {
  buildAccountQuotaDisplayWindows,
  type BuildAccountQuotaDisplayWindowsOptions,
} from '@/features/accounts/model/accountQuotaDisplayWindows';
import { buildAccountQuotaWindowDefinitions } from '@/features/accounts/model/accountQuotaWindowDefinitions';
import { selectAccountQuotaMainListWindows } from '@/features/accounts/model/accountsPagePresentation';
import { buildAccountSubscriptionPresentation } from '@/features/accounts/model/accountSubscriptionPresentation';
import { buildAccountWindowUsageTargetEntries } from '@/features/accounts/model/accountWindowUsageRows';
import { resolveAccountQuotaWindowUsageAndForecast } from '@/features/accounts/model/accountQuotaWindowUsagePresentation';
import type { AccountRow } from '@/features/accounts/model/accountRows';
import type { MonitoringAccountWindowUsageItem } from '@/services/api/usageService';

describe('Demo accounts quota & usage presentation regression', () => {
  it('correctly presents subscription plans and remaining days for demo accounts', () => {
    const authFiles = getDemoAuthFiles().files;
    const quotaState = getDemoQuotaStoreState();
    const rows = buildAccountRows(authFiles, quotaState);

    expect(rows.length).toBe(23);

    const proRow = rows.find((r) => r.fileName === 'codex-pro-20x-01.json');
    expect(proRow).toBeDefined();
    const proCodexQuota = Object.values(quotaState.codexQuota).find(
      (q) => q?.authFileName === proRow?.fileName
    );
    const proSub = buildAccountSubscriptionPresentation({
      row: proRow!,
      codexQuota: proCodexQuota,
    });
    expect(proSub.isPaidCodex).toBe(true);
    expect(proSub.effectivePlanType).toBe('pro');
    expect(proSub.remainingDays).toBeGreaterThan(0);

    const plusRow = rows.find((r) => r.fileName === 'codex-email-user.json');
    expect(plusRow).toBeDefined();
    const plusCodexQuota = Object.values(quotaState.codexQuota).find(
      (q) => q?.authFileName === plusRow?.fileName
    );
    const plusSub = buildAccountSubscriptionPresentation({
      row: plusRow!,
      codexQuota: plusCodexQuota,
    });
    expect(plusSub.isPaidCodex).toBe(true);
    expect(plusSub.effectivePlanType).toBe('plus');
    expect(plusSub.remainingDays).toBeGreaterThan(0);
  });

  it('sorts demo Codex accounts by paid subscription remaining time', () => {
    const authFiles = getDemoAuthFiles().files;
    const quotaState = getDemoQuotaStoreState();
    const rows = buildAccountRows(authFiles, quotaState).filter((row) => row.provider === 'codex');
    const sorted = sortAccountRows(rows, { key: 'remaining', direction: 'asc' });
    const known = sorted.filter((row) => typeof row.subscriptionUntilMs === 'number');
    const unknown = sorted.filter((row) => typeof row.subscriptionUntilMs !== 'number');

    expect(known.length).toBeGreaterThan(1);
    for (let index = 1; index < known.length; index += 1) {
      expect(known[index].subscriptionUntilMs ?? 0).toBeGreaterThanOrEqual(
        known[index - 1].subscriptionUntilMs ?? 0
      );
    }
    expect(unknown.every((row) => sorted.indexOf(row) > sorted.indexOf(known[known.length - 1]))).toBe(
      true
    );
    expect(sortAccountRows(rows).map((row) => row.fileName)).not.toEqual(
      sorted.map((row) => row.fileName)
    );
  });

  it('selects valid quota list windows and produces reliable actual usage and forecasts', () => {
    const authFiles = getDemoAuthFiles().files;
    const quotaState = getDemoQuotaStoreState();
    const rows = buildAccountRows(authFiles, quotaState);

    const options: BuildAccountQuotaDisplayWindowsOptions = {
      stores: quotaState,
      getDisplayCodexQuota: (raw: { name?: string }) =>
        Object.values(quotaState.codexQuota).find((q) => q?.authFileName === raw.name),
      translateQuotaWindowLabel: (label?: string, key?: string) => label || key || '',
      t: ((k: string) => k) as unknown as BuildAccountQuotaDisplayWindowsOptions['t'],
    };

    const windowsByRowKey = new Map();
    rows.forEach((row) => {
      const displayWindows = buildAccountQuotaDisplayWindows(row, options);
      const definitions = buildAccountQuotaWindowDefinitions(displayWindows);
      windowsByRowKey.set(row.selectionKey, definitions);
    });

    const targetEntries = buildAccountWindowUsageTargetEntries(rows, windowsByRowKey);
    expect(targetEntries.length).toBeGreaterThan(0);

    const response = getDemoAccountWindowUsage({
      windows: targetEntries.map((e) => e.target),
    });
    expect(response.items.length).toBe(targetEntries.length);
    expect(response.items.every((i) => i.matched)).toBe(true);

    const usageByKey = new Map<string, MonitoringAccountWindowUsageItem>();
    response.items.forEach((item) => {
      if (item.request_key) {
        usageByKey.set(item.request_key, item);
      }
    });

    const checkProviderPresentation = (
      provider: AccountRow['provider'],
      fileNameFilter?: string
    ) => {
      const matchedRows = rows.filter(
        (r) => r.provider === provider && (!fileNameFilter || r.fileName === fileNameFilter)
      );
      expect(matchedRows.length).toBeGreaterThan(0);

      matchedRows.forEach((row) => {
        const displayWindows = buildAccountQuotaDisplayWindows(row, options);
        const mainWindows = selectAccountQuotaMainListWindows(row, displayWindows);
        expect(mainWindows.length).toBeGreaterThan(0);
        expect(mainWindows.length).toBeLessThanOrEqual(2);

        mainWindows.forEach((w) => {
          expect(w.windowMode).not.toBe('unknown');
          expect(w.quotaProgressObservedAtMs).toBeTypeOf('number');

          const usageData = resolveAccountQuotaWindowUsageAndForecast(row, w, usageByKey);
          expect(usageData.hasTrustedCurrentActual).toBe(true);
          expect(usageData.currentCost).toBeTypeOf('number');
          expect(usageData.currentTokens).toBeTypeOf('number');

          // Ensure linear extrapolation forecast successfully computes without falling back to null
          expect(usageData.forecast).not.toBeNull();
          expect(usageData.forecastCost).toBeTypeOf('number');
          expect(usageData.forecastTokens).toBeTypeOf('number');
        });
      });
    };

    // Verify key representative accounts across quota-bearing providers
    checkProviderPresentation('codex', 'codex-pro-20x-01.json');
    checkProviderPresentation('codex', 'codex-email-user.json');
    checkProviderPresentation('claude', 'claude-team-01.json');
    checkProviderPresentation('antigravity', 'antigravity-builder.json');
    checkProviderPresentation('kimi', 'kimi-coding.json');

    const xaiFreeRow = rows.find((r) => r.fileName === 'xai-ops.json');
    expect(xaiFreeRow).toBeDefined();
    expect(buildAccountQuotaDisplayWindows(xaiFreeRow!, options)).toEqual([]);
  });

  it('provides verifiable rate limit reset credits across demo codex accounts', () => {
    const quotaState = getDemoQuotaStoreState();
    const findQuota = (fileName: string) =>
      Object.values(quotaState.codexQuota).find((q) => q?.authFileName === fileName);

    const teamQuota = findQuota('codex-team-01.json');
    expect(teamQuota).toBeDefined();
    expect(teamQuota?.rateLimitResetCreditsAvailableCount).toBe(2);
    expect(teamQuota?.rateLimitResetCredits).toHaveLength(2);

    const proQuota = findQuota('codex-pro-20x-01.json');
    expect(proQuota).toBeDefined();
    expect(proQuota?.rateLimitResetCreditsAvailableCount).toBe(3);
    expect(proQuota?.rateLimitResetCredits).toHaveLength(3);

    const fallbackQuota = findQuota('codex-fallback-02.json');
    expect(fallbackQuota).toBeDefined();
    expect(fallbackQuota?.rateLimitResetCreditsAvailableCount).toBe(1);
    expect(fallbackQuota?.rateLimitResetCredits).toHaveLength(1);

    const emailQuota = findQuota('codex-email-user.json');
    expect(emailQuota).toBeDefined();
    expect(emailQuota?.rateLimitResetCreditsAvailableCount).toBe(0);
    expect(emailQuota?.rateLimitResetCredits).toEqual([]);
  });

  it('anchors demo evidence epoch and maintains valid forecasts across fake timer progression', () => {
    resetDemoEvidenceEpoch();
    vi.useFakeTimers();
    try {
      vi.setSystemTime(new Date('2026-09-10T12:00:00Z'));
      const quotaState = getDemoQuotaStoreState();
      const authFiles = getDemoAuthFiles().files;
      const rows = buildAccountRows(authFiles, quotaState);

      const proRow = rows.find((r) => r.fileName === 'codex-pro-20x-01.json')!;
      const proQuota = Object.values(quotaState.codexQuota).find(
        (q) => q?.authFileName === proRow.fileName
      );
      expect(proQuota?.resetCreditsEvidenceAtMs).toBeTypeOf('number');
      expect(proQuota?.resetCreditsEvidenceAtMs).toBeGreaterThan(0);

      const options: BuildAccountQuotaDisplayWindowsOptions = {
        stores: quotaState,
        getDisplayCodexQuota: (raw: { name?: string }) =>
          Object.values(quotaState.codexQuota).find((q) => q?.authFileName === raw.name),
        translateQuotaWindowLabel: (label?: string, key?: string) => label || key || '',
        t: ((k: string) => k) as unknown as BuildAccountQuotaDisplayWindowsOptions['t'],
      };

      const displayWindows = buildAccountQuotaDisplayWindows(proRow, options);
      const definitions = buildAccountQuotaWindowDefinitions(displayWindows);
      const windowsByRowKey = new Map([[proRow.selectionKey, definitions]]);
      const targetEntries = buildAccountWindowUsageTargetEntries([proRow], windowsByRowKey);

      // Advance time by 30 minutes
      vi.advanceTimersByTime(30 * 60 * 1000);

      const response = getDemoAccountWindowUsage({
        windows: targetEntries.map((e) => e.target),
      });
      const usageByKey = new Map<string, MonitoringAccountWindowUsageItem>();
      response.items.forEach((item) => {
        if (item.request_key) usageByKey.set(item.request_key, item);
      });

      const mainWindows = selectAccountQuotaMainListWindows(proRow, displayWindows);
      mainWindows.forEach((w) => {
        const usageData = resolveAccountQuotaWindowUsageAndForecast(proRow, w, usageByKey);
        expect(usageData.forecast).not.toBeNull();
      });
    } finally {
      vi.useRealTimers();
      resetDemoEvidenceEpoch();
    }
  });
});
