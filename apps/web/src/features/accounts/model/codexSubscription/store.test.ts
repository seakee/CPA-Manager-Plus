import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { CODEX_SUBSCRIPTIONS_URL } from '@/utils/quota/constants';
import { apiCallApi } from '@/services/api/apiCall';
import { CODEX_SUBSCRIPTION_TTL_MS } from './types';
import { ensureCodexSubscriptionFresh, useCodexSubscriptionStore } from './store';

vi.mock('@/services/api/apiCall', () => ({
  apiCallApi: {
    request: vi.fn(),
  },
  getApiCallErrorMessage: (result: { statusCode?: number; bodyText?: string }) =>
    `${result.statusCode ?? 0} ${result.bodyText ?? 'failed'}`.trim(),
}));

const ACCOUNT_ID = 'acct_shared';
const NOW_MS = 1_700_000_000_000;

const successBody = {
  plan_type: 'plus',
  active_until: '2026-06-10T02:52:15Z',
  will_renew: true,
};

describe('codexSubscription store', () => {
  beforeEach(() => {
    useCodexSubscriptionStore.getState().clearForTests();
    vi.mocked(apiCallApi.request).mockReset();
  });

  afterEach(() => {
    useCodexSubscriptionStore.getState().clearForTests();
  });

  it('dedupes inflight ensureFresh calls for the same accountId', async () => {
    let release: (value: unknown) => void = () => undefined;
    const pending = new Promise((resolve) => {
      release = resolve;
    });
    vi.mocked(apiCallApi.request).mockImplementation(async () => {
      await pending;
      return {
        statusCode: 200,
        hasStatusCode: true,
        header: {},
        body: successBody,
        bodyText: JSON.stringify(successBody),
      };
    });

    const first = ensureCodexSubscriptionFresh({
      accountId: ACCOUNT_ID,
      authIndex: '1',
      nowMs: NOW_MS,
    });
    const second = ensureCodexSubscriptionFresh({
      accountId: ACCOUNT_ID,
      authIndex: '2',
      nowMs: NOW_MS,
    });
    release(undefined);

    const [firstEntry, secondEntry] = await Promise.all([first, second]);
    expect(apiCallApi.request).toHaveBeenCalledTimes(1);
    expect(firstEntry).toEqual(secondEntry);
    expect(firstEntry.status).toBe('ready');
    expect(vi.mocked(apiCallApi.request).mock.calls[0]?.[0]).toMatchObject({
      authIndex: '1',
      method: 'GET',
      url: `${CODEX_SUBSCRIPTIONS_URL}?account_id=${ACCOUNT_ID}`,
    });
  });

  it('reuses a ready record inside the TTL', async () => {
    vi.mocked(apiCallApi.request).mockResolvedValue({
      statusCode: 200,
      hasStatusCode: true,
      header: {},
      body: successBody,
      bodyText: JSON.stringify(successBody),
    });

    await ensureCodexSubscriptionFresh({ accountId: ACCOUNT_ID, authIndex: '1', nowMs: NOW_MS });
    await ensureCodexSubscriptionFresh({
      accountId: ACCOUNT_ID,
      authIndex: '1',
      nowMs: NOW_MS + CODEX_SUBSCRIPTION_TTL_MS - 1,
    });

    expect(apiCallApi.request).toHaveBeenCalledTimes(1);
  });

  it('refetches after TTL and keeps the prior ready record on soft-fail', async () => {
    vi.mocked(apiCallApi.request)
      .mockResolvedValueOnce({
        statusCode: 200,
        hasStatusCode: true,
        header: {},
        body: successBody,
        bodyText: JSON.stringify(successBody),
      })
      .mockResolvedValueOnce({
        statusCode: 401,
        hasStatusCode: true,
        header: {},
        body: { error: 'Unauthorized' },
        bodyText: 'Unauthorized',
      });

    const ready = await ensureCodexSubscriptionFresh({
      accountId: ACCOUNT_ID,
      authIndex: '1',
      nowMs: NOW_MS,
    });
    const afterFail = await ensureCodexSubscriptionFresh({
      accountId: ACCOUNT_ID,
      authIndex: '1',
      nowMs: NOW_MS + CODEX_SUBSCRIPTION_TTL_MS + 1,
    });

    expect(apiCallApi.request).toHaveBeenCalledTimes(2);
    expect(ready.status).toBe('ready');
    expect(afterFail).toEqual(ready);
    expect(useCodexSubscriptionStore.getState().getEntry(ACCOUNT_ID).status).toBe('ready');
  });

  it('stores soft_failed when there is no prior ready record', async () => {
    vi.mocked(apiCallApi.request).mockResolvedValue({
      statusCode: 500,
      hasStatusCode: true,
      header: {},
      body: {},
      bodyText: 'boom',
    });

    const entry = await ensureCodexSubscriptionFresh({
      accountId: ACCOUNT_ID,
      authIndex: '1',
      nowMs: NOW_MS,
    });

    expect(entry).toMatchObject({
      status: 'soft_failed',
      accountId: ACCOUNT_ID,
      errorKind: 'http',
    });
  });
});
