import { act, createElement, useLayoutEffect } from 'react';
import { create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useCredentialLabels } from './useCredentialLabels';

const mocks = vi.hoisted(() => ({
  auth: {
    apiBase: 'http://manager-a.local',
    managementKey: 'test-admin',
    sessionMode: 'manager_embedded',
    connectionStatus: 'connected',
  },
  list: vi.fn(),
  set: vi.fn(),
}));
vi.mock('@/stores', () => ({
  useAuthStore: (selector: (state: typeof mocks.auth) => unknown) => selector(mocks.auth),
}));
vi.mock('@/services/api/credentialLabels', () => ({
  credentialLabelsApi: { list: mocks.list, set: mocks.set },
}));
const deferred = <T,>() => {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((a, b) => {
    resolve = a;
    reject = b;
  });
  return { promise, resolve, reject };
};
const a = 'a'.repeat(64),
  b = 'b'.repeat(64);
const item = (keyHash = a, label = 'account@example.com') => ({ keyHash, label, updatedAtMs: 1 });
let latest: ReturnType<typeof useCredentialLabels>;
let renderer: ReactTestRenderer;
function Harness() {
  const result = useCredentialLabels();
  useLayoutEffect(() => {
    latest = result;
  });
  return createElement('output', null, JSON.stringify(result.labels));
}
const mount = async () => {
  await act(async () => {
    renderer = create(createElement(Harness));
  });
};
const rerender = async () => {
  await act(async () => {
    renderer.update(createElement(Harness));
  });
};
beforeEach(() => {
  mocks.auth = {
    apiBase: 'http://manager-a.local',
    managementKey: 'test-admin',
    sessionMode: 'manager_embedded',
    connectionStatus: 'connected',
  };
  mocks.list.mockReset().mockResolvedValue({ items: [item()] });
  mocks.set.mockReset();
});
afterEach(() => {
  act(() => renderer?.unmount());
});

describe('credential label scope and refresh', () => {
  it('keeps the last good snapshot during refresh and failure, then accepts an empty snapshot', async () => {
    const first = deferred<{ items: ReturnType<typeof item>[] }>();
    mocks.list.mockReturnValueOnce(first.promise);
    await mount();
    expect(latest.labels).toEqual({});
    expect(latest.ready).toBe(false);
    await act(async () => first.resolve({ items: [item()] }));
    expect(latest.ready).toBe(true);
    const refresh = deferred<{ items: ReturnType<typeof item>[] }>();
    mocks.list.mockReturnValueOnce(refresh.promise);
    let pending!: Promise<void>;
    act(() => {
      pending = latest.refresh();
    });
    expect(latest.labels[a]).toBe('account@example.com');
    await act(async () => {
      refresh.reject(new Error('offline'));
      await pending;
    });
    expect(latest.labels[a]).toBe('account@example.com');
    expect(latest.ready).toBe(false);
    expect(latest.error).toBe('offline');
    mocks.list.mockResolvedValueOnce({ items: [] });
    await act(async () => latest.refresh());
    expect(latest.labels).toEqual({});
    expect(latest.ready).toBe(true);
  });
  it('rejects out-of-order reads and a read that overlaps a successful edit', async () => {
    await mount();
    const old = deferred<{ items: ReturnType<typeof item>[] }>();
    mocks.list.mockReturnValueOnce(old.promise);
    let pending!: Promise<void>;
    act(() => {
      pending = latest.refresh();
    });
    mocks.set.mockResolvedValueOnce(item(a, 'Updated'));
    await act(async () => {
      expect(await latest.setLabel(a, 'Updated')).toBe(true);
    });
    await act(async () => {
      old.resolve({ items: [item(a, 'Stale')] });
      await pending;
    });
    expect(latest.labels[a]).toBe('Updated');
    mocks.set.mockResolvedValueOnce(item(a, ''));
    await act(async () => latest.setLabel(a, ''));
    expect(latest.labels).toEqual({});
  });
  it('does not lose an unrelated label and suppresses duplicate submissions', async () => {
    mocks.list.mockResolvedValueOnce({ items: [item(), item(b, 'Other')] });
    await mount();
    const write = deferred<ReturnType<typeof item>>();
    mocks.set.mockReturnValueOnce(write.promise);
    let pending!: Promise<boolean>;
    act(() => {
      pending = latest.setLabel(a, 'Updated');
    });
    await act(async () => {
      expect(await latest.setLabel(a, 'Duplicate')).toBe(false);
    });
    expect(mocks.set).toHaveBeenCalledTimes(1);
    await act(async () => {
      write.resolve(item(a, 'Updated'));
      await pending;
    });
    expect(latest.labels[b]).toBe('Other');
  });
  it('hides old labels immediately and ignores late reads after changing servers', async () => {
    const old = deferred<{ items: ReturnType<typeof item>[] }>();
    mocks.list.mockReturnValueOnce(old.promise);
    await mount();
    mocks.auth = { ...mocks.auth, apiBase: 'http://manager-b.local' };
    mocks.list.mockResolvedValueOnce({ items: [item(b, 'Account B')] });
    await rerender();
    await act(async () => old.resolve({ items: [item(a, 'Account A')] }));
    expect(latest.labels).toEqual({ [b]: 'Account B' });
    expect(mocks.list.mock.calls[1][0].apiBase).toBe('http://manager-b.local');
  });
  it('does not revive a mutation after logout and return to the same connection', async () => {
    await mount();
    const write = deferred<ReturnType<typeof item>>();
    mocks.set.mockReturnValueOnce(write.promise);
    let pending!: Promise<boolean>;
    act(() => {
      pending = latest.setLabel(a, 'Old operation');
    });
    mocks.auth = { ...mocks.auth, connectionStatus: 'disconnected' };
    await rerender();
    expect(latest.labels).toEqual({});
    mocks.auth = { ...mocks.auth, connectionStatus: 'connected' };
    await rerender();
    await act(async () => {
      write.resolve(item(a, 'Old operation'));
      expect(await pending).toBe(false);
    });
    expect(latest.labels[a]).toBe('account@example.com');
    expect(latest.saving).toBe(false);
  });
  it('does not call Manager-only endpoints in lightweight mode and tolerates old servers', async () => {
    mocks.auth = { ...mocks.auth, sessionMode: 'external_panel' };
    await mount();
    expect(mocks.list).not.toHaveBeenCalled();
    mocks.auth = { ...mocks.auth, sessionMode: 'manager_embedded' };
    mocks.list.mockRejectedValueOnce({ status: 404 });
    await rerender();
    expect(latest.available).toBe(false);
    expect(latest.error).toBe('');
  });
});
