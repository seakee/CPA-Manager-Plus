import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { TFunction } from 'i18next';
import type { XaiBillingSummary, XaiSubscriptionInfo } from '@/types';
import { fetchXaiQuota } from '@/utils/quota';
import { fetchXaiSubscription } from '@/utils/quota/xaiSubscription';
import { getQuotaCredentialStoreKey } from '@/utils/quota/credentialScope';
import { useQuotaStore } from '@/stores/useQuotaStore';
import { XAI_CONFIG } from './quotaConfigs';
import { refreshQuotaWithConfig } from './quotaRefresh';
import { buildAccountRows } from '@/features/accounts/model/accountRows';

vi.mock('@/utils/quota', async (original) => ({
  ...(await original<typeof import('@/utils/quota')>()),
  fetchXaiQuota: vi.fn(),
}));
vi.mock('@/utils/quota/xaiSubscription', () => ({ fetchXaiSubscription: vi.fn() }));

const file = { name: 'xai.json', type: 'xai', authIndex: 'xai-1' };
const key = getQuotaCredentialStoreKey(file);
const t = ((value: string) => value) as TFunction;
const billing = (used = 57): XaiBillingSummary => ({
  periodType: 'weekly',
  usagePercent: used,
  productUsage: [],
  monthlyLimitCents: null,
  usedCents: null,
  includedUsedCents: null,
  onDemandCapCents: null,
  onDemandUsedCents: null,
  onDemandUsedPercent: null,
  usedPercent: null,
});
const heavy = (): XaiSubscriptionInfo => ({
  planType: 'SuperGrok Heavy',
  source: 'user',
  observedAtMs: Date.now(),
});
const deferred = <T>() => {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
};
const current = () => useQuotaStore.getState().xaiQuota[key];
const flushAsyncWork = () => new Promise((resolve) => setTimeout(resolve, 0));
const refresh = (isCurrent = () => true) =>
  refreshQuotaWithConfig({
    config: XAI_CONFIG,
    file,
    t,
    isCurrent,
    currentState: current(),
    setQuota: useQuotaStore.getState().setXaiQuota,
  });

describe('background xAI quota subscription enrichment', () => {
  beforeEach(() => {
    useQuotaStore.getState().clearQuotaCache();
    vi.mocked(fetchXaiQuota).mockReset().mockResolvedValue(billing());
    vi.mocked(fetchXaiSubscription).mockReset();
  });

  it('publishes quota before optional metadata and preserves its original observation time', async () => {
    const pending = deferred<XaiSubscriptionInfo>();
    vi.mocked(fetchXaiSubscription).mockReturnValueOnce(pending.promise);
    const result = await refresh();
    expect(result?.status).toBe('success');
    expect(current().billing?.usagePercent).toBe(57);
    expect(current().billing?.subscription).toBeUndefined();
    const quotaAt = current().fetchedAtMs;
    const subscription = heavy();
    pending.resolve(subscription);
    await vi.waitFor(() => expect(current().billing?.subscription).toEqual(subscription));
    expect(current().fetchedAtMs).toBe(quotaAt);
  });

  it('does not renew previous paid evidence when a new quota succeeds but subscription fails', async () => {
    const subscription = heavy();
    const oldState = XAI_CONFIG.buildSuccessState({ ...billing(20), subscription }, file);
    useQuotaStore.getState().setXaiQuota({ [key]: oldState });
    vi.mocked(fetchXaiSubscription).mockRejectedValueOnce(new Error('optional timeout'));
    const result = await refresh();
    expect(result?.status).toBe('success');
    expect(current().billing?.usagePercent).toBe(57);
    expect(current().billing?.subscription).toBeUndefined();
    await flushAsyncWork();
    expect(current().status).toBe('success');
    const row = buildAccountRows([file], useQuotaStore.getState())[0];
    expect(row.planType).toBeNull();
    expect(row.quota.status).toBe('unknown');
  });

  it.each(['Free', 'SuperGrok Heavy'])(
    // A file label must not replace a newer live observation during refresh.
    'does not resurrect a Heavy file label after an old %s observation',
    async (oldPlan) => {
      const fileWithPlan = { ...file, planType: 'SuperGrok Heavy' };
      const oldState = XAI_CONFIG.buildSuccessState(
        { ...billing(), subscription: { ...heavy(), planType: oldPlan } },
        fileWithPlan
      );
      useQuotaStore.getState().setXaiQuota({ [key]: oldState });
      const run = () =>
        refreshQuotaWithConfig({
          config: XAI_CONFIG,
          file: fileWithPlan,
          t,
          isCurrent: () => true,
          currentState: current(),
          setQuota: useQuotaStore.getState().setXaiQuota,
        });
      const pending = deferred<XaiSubscriptionInfo | null>();
      vi.mocked(fetchXaiSubscription).mockReturnValueOnce(pending.promise);
      await run();
      const checkUnknown = () => {
        const row = buildAccountRows([fileWithPlan], useQuotaStore.getState())[0];
        expect(row.planType).toBeNull();
        expect(row.quota.status).toBe('unknown');
        expect(current().billing?.subscriptionSuperseded).toBe(true);
      };
      checkUnknown(); // Pending query must not fall back to the file plan.
      pending.resolve(null);
      await flushAsyncWork();
      checkUnknown();
      vi.mocked(fetchXaiSubscription).mockRejectedValueOnce(new Error('optional timeout'));
      await run();
      await flushAsyncWork();
      checkUnknown(); // The superseded marker survives consecutive failed lookups.
      vi.mocked(fetchXaiSubscription).mockImplementationOnce(async () => heavy());
      await run();
      await vi.waitFor(() =>
        expect(buildAccountRows([fileWithPlan], useQuotaStore.getState())[0].quota.status).toBe(
          'ok'
        )
      );
    }
  );

  it.each(['Free', 'SuperGrok Heavy'])(
    'uses only the new %s observation after an older Heavy plan',
    async (planType) => {
      const oldState = XAI_CONFIG.buildSuccessState(
        { ...billing(20), subscription: heavy() },
        file
      );
      useQuotaStore.getState().setXaiQuota({ [key]: oldState });
      const pending = deferred<XaiSubscriptionInfo>();
      vi.mocked(fetchXaiSubscription).mockReturnValueOnce(pending.promise);
      await refresh();
      expect(buildAccountRows([file], useQuotaStore.getState())[0].quota.status).toBe('unknown');
      pending.resolve({ planType, source: 'user', observedAtMs: Date.now() });
      await vi.waitFor(() => expect(current().billing?.subscription?.planType).toBe(planType));
      const row = buildAccountRows([file], useQuotaStore.getState())[0];
      expect(row.planType).toBe(planType);
      expect(row.quota.status).toBe(planType === 'Free' ? 'unknown' : 'ok');
    }
  );

  it('does not commit metadata after a connection or credential scope becomes stale', async () => {
    const pending = deferred<XaiSubscriptionInfo>();
    vi.mocked(fetchXaiSubscription).mockReturnValueOnce(pending.promise);
    let active = true;
    await refresh(() => active);
    const state = current();
    active = false;
    pending.resolve(heavy());
    await flushAsyncWork();
    expect(current()).toBe(state);
    expect(current().billing?.subscription).toBeUndefined();
  });

  it('does not resurrect file entitlement across a quota success, failure, and recovery', async () => {
    const fileWithPlan = { ...file, planType: 'SuperGrok Heavy' };
    const oldState = XAI_CONFIG.buildSuccessState(
      {
        ...billing(),
        subscription: { ...heavy(), planType: 'Free' },
      },
      fileWithPlan
    );
    useQuotaStore.getState().setXaiQuota({ [key]: oldState });
    const run = () =>
      refreshQuotaWithConfig({
        config: XAI_CONFIG,
        file: fileWithPlan,
        t,
        isCurrent: () => true,
        currentState: current(),
        setQuota: useQuotaStore.getState().setXaiQuota,
      });
    vi.mocked(fetchXaiQuota).mockRejectedValueOnce(new Error('quota unavailable'));
    await run();
    expect(current().status).toBe('error');
    expect(current().billing?.subscription?.planType).toBe('Free');
    vi.mocked(fetchXaiSubscription).mockResolvedValueOnce(null);
    await run();
    await flushAsyncWork();
    expect(current().status).toBe('success');
    expect(current().billing?.subscriptionSuperseded).toBe(true);
    const row = buildAccountRows([fileWithPlan], useQuotaStore.getState())[0];
    expect(row.planType).toBeNull();
    expect(row.quota.status).toBe('unknown');
  });

  it('does not repopulate a cleared connection cache', async () => {
    const pending = deferred<XaiSubscriptionInfo>();
    vi.mocked(fetchXaiSubscription).mockReturnValueOnce(pending.promise);
    await refresh();
    useQuotaStore.getState().clearQuotaCache();
    pending.resolve(heavy());
    await flushAsyncWork();
    expect(current()).toBeUndefined();
  });

  it('does not let an older lookup overwrite a newer quota refresh', async () => {
    const old = deferred<XaiSubscriptionInfo>();
    const newer = deferred<XaiSubscriptionInfo>();
    vi.mocked(fetchXaiSubscription)
      .mockReturnValueOnce(old.promise)
      .mockReturnValueOnce(newer.promise);
    await refresh();
    vi.mocked(fetchXaiQuota).mockResolvedValueOnce(billing(70));
    await refresh();
    const free = { planType: 'Free', source: 'user', observedAtMs: Date.now() } as const;
    newer.resolve(free);
    await vi.waitFor(() => expect(current().billing?.subscription).toEqual(free));
    old.resolve(heavy());
    await flushAsyncWork();
    expect(current().billing).toMatchObject({ usagePercent: 70, subscription: free });
  });

  it('does not overwrite a snapshot replaced by another operation', async () => {
    const pending = deferred<XaiSubscriptionInfo>();
    vi.mocked(fetchXaiSubscription).mockReturnValueOnce(pending.promise);
    await refresh();
    const replacement = XAI_CONFIG.buildSuccessState(billing(99), file);
    useQuotaStore.getState().setXaiQuota({ [key]: replacement });
    pending.resolve(heavy());
    await flushAsyncWork();
    expect(current()).toBe(replacement);
  });

  it('never carries an old account plan into a replacement credential', () => {
    const old = XAI_CONFIG.buildSuccessState({ ...billing(), subscription: heavy() }, file);
    const replacement = { ...file, authIndex: 'replacement' };
    const next = XAI_CONFIG.buildSuccessState(billing(), replacement, old);
    expect(next.authIndex).toBe('replacement');
    expect(next.billing?.subscription).toBeUndefined();
  });

  it('keeps the captured connection for asynchronous subscription calls', async () => {
    const scope = { apiBase: 'https://captured.example', managementKey: 'fixture-key' };
    vi.mocked(fetchXaiSubscription).mockResolvedValueOnce(heavy());
    await refreshQuotaWithConfig({
      config: XAI_CONFIG,
      file,
      t,
      isCurrent: () => true,
      requestScope: scope,
      setQuota: useQuotaStore.getState().setXaiQuota,
    });
    expect(fetchXaiSubscription).toHaveBeenCalledWith(
      file,
      expect.objectContaining({
        baseURL: 'https://captured.example/v0/management',
        headers: { Authorization: 'Bearer fixture-key' },
        cpampScopedRequest: true,
      }),
      expect.any(Function),
      current().fetchedAtMs
    );
    await vi.waitFor(() =>
      expect(current().billing?.subscription?.planType).toBe('SuperGrok Heavy')
    );
  });
});
