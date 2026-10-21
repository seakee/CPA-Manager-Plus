import { beforeEach, describe, expect, it, vi } from 'vitest';
import axios from 'axios';
import { apiCallApi } from '@/services/api/apiCall';
import { fetchXaiSubscription } from './xaiSubscription';
import { XAI_CLI_CHAT_PROXY_BASE_URL } from './constants';

vi.mock('@/services/api/apiCall', () => ({ apiCallApi: { request: vi.fn() } }));
const file = { name: 'xai.json', type: 'xai', authIndex: 'xai-1' };
const result = (body: unknown, statusCode = 200) => ({
  statusCode,
  hasStatusCode: true,
  header: {},
  body,
  bodyText: '',
});
const requests = vi.mocked(apiCallApi.request);

describe('xAI subscription lookup', () => {
  beforeEach(() => {
    requests.mockReset();
  });

  it('uses credential-scoped GETs and resolves the reported Heavy account', async () => {
    requests
      .mockResolvedValueOnce(result({ subscriptionTier: 'SuperGrokPro' }))
      .mockResolvedValueOnce(result({ subscription_tier_display: 'SuperGrok Heavy' }));
    const subscription = await fetchXaiSubscription(file);
    expect(subscription).toMatchObject({ planType: 'SuperGrok Heavy', source: 'user' });
    expect(subscription?.observedAtMs).toBeGreaterThan(0);
    expect(requests).toHaveBeenCalledTimes(1);
    expect(requests.mock.calls.map(([request]) => request)).toEqual([
      expect.objectContaining({
        authIndex: 'xai-1',
        method: 'GET',
        url: `${XAI_CLI_CHAT_PROXY_BASE_URL}/user?include=subscription`,
      }),
    ]);
    expect(requests.mock.calls[0][0].header?.Authorization).toBe('Bearer $TOKEN$');
  });

  it.each([
    ['GrokPro', 'SuperGrok'],
    ['SuperGrokLite', 'SuperGrok Lite'],
    ['SuperGrokPlus', 'SuperGrok Plus'],
    ['XBasic', 'X Basic'],
    ['XPremium', 'X Premium'],
    ['XPremiumPlus', 'X Premium+'],
    ['Free', 'Free'],
  ])('normalizes the live tier %s', async (tier, planType) => {
    requests
      .mockResolvedValueOnce(result({ subscription_tier: tier }))
      .mockResolvedValueOnce(result({}));
    expect(await fetchXaiSubscription(file)).toMatchObject({ planType, source: 'user' });
  });

  it('lets live Free override a stale Heavy settings label', async () => {
    requests
      .mockResolvedValueOnce(result({ subscriptionTier: 'Free' }))
      .mockResolvedValueOnce(result({ subscription_tier_display: 'SuperGrok Heavy' }));
    expect(await fetchXaiSubscription(file)).toMatchObject({ planType: 'Free', source: 'user' });
  });

  it.each([null, '', 'FuturePlan', 'constructor'])(
    'does not promote a null/unknown live tier %s',
    async (tier) => {
      requests
        .mockResolvedValueOnce(result({ subscriptionTier: tier }))
        .mockResolvedValueOnce(result({ subscription_tier_display: 'SuperGrok Heavy' }));
      expect(await fetchXaiSubscription(file)).toMatchObject({ planType: null, source: 'user' });
    }
  );

  it('uses a settings display name when the live request is unavailable', async () => {
    requests
      .mockRejectedValueOnce(new Error('network unavailable'))
      .mockResolvedValueOnce(
        result(JSON.stringify({ subscriptionTierDisplay: 'SuperGrok Heavy' }))
      );
    expect(await fetchXaiSubscription(file)).toMatchObject({
      planType: 'SuperGrok Heavy',
      source: 'settings',
    });
    expect(requests.mock.calls[1][0].url).toBe(`${XAI_CLI_CHAT_PROXY_BASE_URL}/settings`);
  });

  it('treats rejected/malformed metadata as optional', async () => {
    requests
      .mockResolvedValueOnce(result({ subscriptionTier: 'SuperGrokPro' }, 403))
      .mockResolvedValueOnce(result('invalid json'));
    expect(await fetchXaiSubscription(file)).toBeNull();
  });

  it('forwards the captured scope and cancellation signal, with a bounded timeout', async () => {
    const signal = new AbortController().signal;
    requests.mockResolvedValue(result({}));
    await fetchXaiSubscription(file, { signal, timeout: 2000 });
    expect(requests.mock.calls[0][1]).toMatchObject({ signal, timeout: 2000 });
    await fetchXaiSubscription(file, { signal, timeout: 12000 });
    expect(requests.mock.calls[2][1]).toMatchObject({ signal, timeout: 8000 });
  });

  it('discards responses after cancellation', async () => {
    const controller = new AbortController();
    requests.mockImplementation(async () => {
      controller.abort();
      return result({ subscriptionTier: 'SuperGrokPro' });
    });
    expect(await fetchXaiSubscription(file, { signal: controller.signal })).toBeNull();
  });

  it('does not send requests without a credential or after cancellation', async () => {
    expect(await fetchXaiSubscription({ name: 'missing.json' })).toBeNull();
    const controller = new AbortController();
    controller.abort();
    expect(await fetchXaiSubscription(file, { signal: controller.signal })).toBeNull();
    expect(requests).not.toHaveBeenCalled();
  });

  it('bounds background requests across multiple batches and skips stale queued work', async () => {
    const pending: Array<(value: ReturnType<typeof result>) => void> = [];
    requests.mockImplementation(() => new Promise((resolve) => pending.push(resolve)));
    const first = fetchXaiSubscription(file);
    const second = fetchXaiSubscription({ ...file, authIndex: 'xai-2' });
    let active = true;
    const third = fetchXaiSubscription({ ...file, authIndex: 'xai-3' }, undefined, () => active);
    await vi.waitFor(() => expect(requests).toHaveBeenCalledTimes(1));
    active = false;
    pending[0](result({ subscriptionTier: 'SuperGrokPro' }));
    await vi.waitFor(() => expect(requests).toHaveBeenCalledTimes(2));
    pending[1](result({ subscriptionTier: 'SuperGrokPro' }));
    expect(
      (await Promise.all([first, second, third])).map((value) => value?.planType ?? null)
    ).toEqual(['SuperGrok Heavy', 'SuperGrok Heavy', null]);
    expect(requests).toHaveBeenCalledTimes(2);
  });

  const scope = {
    baseURL: 'https://cpa.example/v0/management',
    headers: { Authorization: 'Bearer fixture-key' },
    cpampScopedRequest: true,
  };

  it('shares the actual result for repeated scoped lookups while a request is in flight', async () => {
    let finish!: (value: ReturnType<typeof result>) => void;
    requests.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        })
    );
    const first = fetchXaiSubscription(file, scope);
    await vi.waitFor(() => expect(requests).toHaveBeenCalledTimes(1));
    const second = fetchXaiSubscription({ ...file }, { ...scope });
    finish(result({ subscriptionTier: 'SuperGrokPro' }));
    expect((await Promise.all([first, second])).map((value) => value?.planType)).toEqual([
      'SuperGrok Heavy',
      'SuperGrok Heavy',
    ]);
    expect(requests).toHaveBeenCalledTimes(1);
    requests.mockResolvedValueOnce(result({ subscriptionTier: 'Free' }));
    expect((await fetchXaiSubscription(file, scope))?.planType).toBe('Free');
    expect(requests).toHaveBeenCalledTimes(2);
  });

  it('does not let the first consumer cancellation cancel another consumer', async () => {
    let finish!: (value: ReturnType<typeof result>) => void;
    const controller = new AbortController();
    requests.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        })
    );
    const first = fetchXaiSubscription(file, { ...scope, signal: controller.signal });
    await vi.waitFor(() => expect(requests).toHaveBeenCalledTimes(1));
    const second = fetchXaiSubscription(file, scope);
    controller.abort();
    expect(requests.mock.calls[0][1]?.signal?.aborted).toBe(false);
    finish(result({ subscriptionTier: 'SuperGrokPro' }));
    expect(await first).toBeNull();
    expect((await second)?.planType).toBe('SuperGrok Heavy');
    expect(requests).toHaveBeenCalledTimes(1);
  });

  it('does not let a stale first generation suppress a current coalesced consumer', async () => {
    let finish!: (value: ReturnType<typeof result>) => void;
    requests.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        })
    );
    let active = true;
    const first = fetchXaiSubscription(file, scope, () => active);
    const second = fetchXaiSubscription(file, scope);
    active = false;
    await vi.waitFor(() => expect(requests).toHaveBeenCalledTimes(1));
    finish(result({ subscriptionTier: 'SuperGrokPro' }));
    expect(await first).toBeNull();
    expect((await second)?.planType).toBe('SuperGrok Heavy');
  });

  it('coalesces queued work and skips it if all consumers become stale', async () => {
    let finish!: (value: ReturnType<typeof result>) => void;
    requests.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          finish = resolve;
        })
    );
    const blocking = fetchXaiSubscription({ ...file, authIndex: 'blocking' }, scope);
    await vi.waitFor(() => expect(requests).toHaveBeenCalledTimes(1));
    let active = true;
    const first = fetchXaiSubscription(file, scope, () => active);
    const second = fetchXaiSubscription(file, scope, () => active);
    active = false;
    finish(result({ subscriptionTier: 'Free' }));
    expect(await blocking).toMatchObject({ planType: 'Free' });
    expect(await Promise.all([first, second])).toEqual([null, null]);
    expect(requests).toHaveBeenCalledTimes(1);
  });

  it('coalesces queued consumers and returns the same result to both', async () => {
    let finish!: (value: ReturnType<typeof result>) => void;
    requests
      .mockImplementationOnce(
        () =>
          new Promise((resolve) => {
            finish = resolve;
          })
      )
      .mockResolvedValueOnce(result({ subscriptionTier: 'SuperGrokPro' }));
    const blocking = fetchXaiSubscription({ ...file, authIndex: 'blocking' }, scope);
    await vi.waitFor(() => expect(requests).toHaveBeenCalledTimes(1));
    const first = fetchXaiSubscription(file, scope);
    const second = fetchXaiSubscription(file, scope);
    finish(result({ subscriptionTier: 'Free' }));
    await blocking;
    expect((await Promise.all([first, second])).map((value) => value?.planType)).toEqual([
      'SuperGrok Heavy',
      'SuperGrok Heavy',
    ]);
    expect(requests).toHaveBeenCalledTimes(2);
  });

  it('does not join a skipped task during its completion microtasks', async () => {
    let active = true;
    let retry: ReturnType<typeof fetchXaiSubscription> | undefined;
    let scheduled = false;
    requests.mockResolvedValue(result({ subscriptionTier: 'SuperGrokPro' }));
    const skipped = fetchXaiSubscription(file, scope, () => {
      if (!active && !scheduled) {
        scheduled = true;
        queueMicrotask(() => {
          active = true;
          retry = fetchXaiSubscription(file, scope);
        });
      }
      return active;
    });
    active = false;
    expect(await skipped).toBeNull();
    expect(retry).toBeDefined();
    expect(await retry).toMatchObject({ planType: 'SuperGrok Heavy' });
    expect(requests).toHaveBeenCalledTimes(1);
  });

  it('does not share work without a captured connection scope', async () => {
    requests.mockResolvedValue(result({ subscriptionTier: 'SuperGrokPro' }));
    await Promise.all([fetchXaiSubscription(file), fetchXaiSubscription(file)]);
    expect(requests).toHaveBeenCalledTimes(2);
  });

  it('keeps legacy cancel-token requests independent rather than sharing their cancellation', async () => {
    const source = axios.CancelToken.source();
    const config = { ...scope, cancelToken: source.token };
    requests.mockResolvedValue(result({ subscriptionTier: 'Free' }));
    await Promise.all([fetchXaiSubscription(file, config), fetchXaiSubscription(file, config)]);
    expect(requests).toHaveBeenCalledTimes(2);
    expect(requests.mock.calls[0][1]?.cancelToken).toBe(source.token);
  });

  it('cancels shared transport when all consumers cancel and does not reuse it', async () => {
    const firstController = new AbortController();
    const secondController = new AbortController();
    requests
      .mockImplementationOnce(
        (_request, config) =>
          new Promise((_resolve, reject) => {
            config?.signal?.addEventListener?.('abort', () => reject(new Error('cancelled')));
          })
      )
      .mockResolvedValueOnce(result({ subscriptionTier: 'Free' }));
    const first = fetchXaiSubscription(file, { ...scope, signal: firstController.signal });
    const second = fetchXaiSubscription(file, { ...scope, signal: secondController.signal });
    await vi.waitFor(() => expect(requests).toHaveBeenCalledTimes(1));
    firstController.abort();
    expect(requests.mock.calls[0][1]?.signal?.aborted).toBe(false);
    secondController.abort();
    expect(requests.mock.calls[0][1]?.signal?.aborted).toBe(true);
    const next = fetchXaiSubscription(file, scope);
    expect(await Promise.all([first, second])).toEqual([null, null]);
    expect((await next)?.planType).toBe('Free');
    expect(requests).toHaveBeenCalledTimes(2);
  });

  it.each(['connection', 'managementKey', 'credential', 'credentialRefresh'])(
    'does not coalesce a different %s',
    async (change) => {
      requests.mockResolvedValue(result({ subscriptionTier: 'SuperGrokPro' }));
      const otherScope =
        change === 'connection'
          ? { ...scope, baseURL: 'https://other.example' }
          : change === 'managementKey'
            ? { ...scope, headers: { Authorization: 'Bearer other-key' } }
            : scope;
      const otherFile =
        change === 'credential'
          ? { ...file, authIndex: 'other' }
          : change === 'credentialRefresh'
            ? { ...file, last_refresh: '2026-10-19T10:00:00Z' }
            : file;
      await Promise.all([
        fetchXaiSubscription(file, scope),
        fetchXaiSubscription(otherFile, otherScope),
      ]);
      expect(requests).toHaveBeenCalledTimes(2);
    }
  );

  it('does not reuse a request started before a newer quota snapshot', async () => {
    let finish!: (value: ReturnType<typeof result>) => void;
    const clock = vi.spyOn(Date, 'now').mockReturnValue(1000);
    try {
      requests
        .mockImplementationOnce(
          () =>
            new Promise((resolve) => {
              finish = resolve;
            })
        )
        .mockResolvedValueOnce(result({ subscriptionTier: 'Free' }));
      const first = fetchXaiSubscription(file, scope);
      await vi.waitFor(() => expect(requests).toHaveBeenCalledTimes(1));
      clock.mockReturnValue(2000);
      const newer = fetchXaiSubscription(file, scope, () => true, 2000);
      finish(result({ subscriptionTier: 'SuperGrokPro' }));
      expect((await first)?.planType).toBe('SuperGrok Heavy');
      expect((await newer)?.planType).toBe('Free');
      expect(requests).toHaveBeenCalledTimes(2);
    } finally {
      clock.mockRestore();
    }
  });

  it('does not accept a payload without an explicit upstream success status', async () => {
    requests.mockResolvedValue({
      ...result({ subscriptionTier: 'SuperGrokPro' }),
      hasStatusCode: false,
    });
    expect(await fetchXaiSubscription(file)).toBeNull();
  });

  it('timestamps the request start so an in-flight response cannot cross a credential refresh', async () => {
    const started = 1792380000000;
    let clock = started;
    const now = vi.spyOn(Date, 'now').mockImplementation(() => clock);
    try {
      requests.mockImplementation(async () => {
        clock += 30_000;
        return result({ subscriptionTier: 'SuperGrokPro' });
      });
      expect(await fetchXaiSubscription(file)).toMatchObject({ observedAtMs: started });
    } finally {
      now.mockRestore();
    }
  });
});
