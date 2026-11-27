import { createElement } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { apiKeyUsageApi } from '@/services/api';
import { useAuthStore } from '@/stores';
import {
  createProviderRecentRequestsCacheController,
  useProviderRecentRequests,
} from './useProviderRecentRequests';

describe('provider recent request cache isolation', () => {
  it('creates a fresh cache when the connection scope changes', () => {
    const controller = createProviderRecentRequestsCacheController();
    const serverA = controller.forScope('scope-a');
    serverA.cachedUsageByProvider = new Map([['provider-a', new Map()]]);
    serverA.cachedAt = Date.now();
    serverA.inFlightRequest = Promise.resolve(serverA.cachedUsageByProvider);

    const serverB = controller.forScope('scope-b');

    expect(serverB).not.toBe(serverA);
    expect(serverB.cachedUsageByProvider.size).toBe(0);
    expect(serverB.cachedAt).toBe(0);
    expect(serverB.inFlightRequest).toBeNull();

    serverA.cachedUsageByProvider = new Map([['late-provider-a', new Map()]]);
    expect(controller.forScope('scope-b')).toBe(serverB);
    expect(serverB.cachedUsageByProvider.size).toBe(0);
  });

  it('reuses the cache only within the same hashed connection scope', () => {
    const controller = createProviderRecentRequestsCacheController();
    const first = controller.forScope('scope-a');

    expect(controller.forScope('scope-a')).toBe(first);
    expect(controller.forScope('scope-b')).not.toBe(first);
  });
});

describe('useProviderRecentRequests short-window revalidation', () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('reuses cache within initialMaxAgeMs and revalidates after cooldown on re-entry', async () => {
    const storage = new Map<string, string>();
    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
    vi.stubGlobal('localStorage', {
      getItem: (key: string) => storage.get(key) ?? null,
      setItem: (key: string, value: string) => {
        storage.set(key, value);
      },
      removeItem: (key: string) => {
        storage.delete(key);
      },
      clear: () => {
        storage.clear();
      },
    });
    const nowSpy = vi.spyOn(Date, 'now');
    const getUsageSpy = vi.spyOn(apiKeyUsageApi, 'getUsage');
    useAuthStore.setState({
      apiBase: 'http://reentry-test.local:8317',
      managementKey: 'mgmt-reentry-key',
    });
    function Harness({ initialMaxAgeMs }: { initialMaxAgeMs: number }) {
      const { usageByProvider } = useProviderRecentRequests({ enabled: true, initialMaxAgeMs });
      return createElement('span', {
        'data-success': usageByProvider.get('openai')?.get('row-1')?.success ?? null,
      });
    }

    try {
      nowSpy.mockReturnValue(2_000_000);
      getUsageSpy.mockResolvedValueOnce({
        openai: {
          'row-1': { success: 1, failed: 0, recent_requests: [] },
        },
      });

      let renderer!: ReactTestRenderer;
      await act(async () => {
        renderer = create(createElement(Harness, { initialMaxAgeMs: 5_000 }));
        await Promise.resolve();
      });
      expect(getUsageSpy).toHaveBeenCalledTimes(1);
      expect(renderer.root.findByType('span').props['data-success']).toBe(1);

      await act(async () => {
        renderer.unmount();
      });

      // Re-enter within 5s: reuses cache without issuing another network request.
      nowSpy.mockReturnValue(2_003_000);
      await act(async () => {
        renderer = create(createElement(Harness, { initialMaxAgeMs: 5_000 }));
        await Promise.resolve();
      });
      expect(getUsageSpy).toHaveBeenCalledTimes(1);
      expect(renderer.root.findByType('span').props['data-success']).toBe(1);

      await act(async () => {
        renderer.unmount();
      });

      // Re-enter after 5s (still well under the 4-minute polling interval): revalidates once.
      nowSpy.mockReturnValue(2_006_000);
      getUsageSpy.mockResolvedValueOnce({
        openai: {
          'row-1': { success: 4, failed: 0, recent_requests: [] },
        },
      });
      await act(async () => {
        renderer = create(createElement(Harness, { initialMaxAgeMs: 5_000 }));
        await Promise.resolve();
      });
      expect(getUsageSpy).toHaveBeenCalledTimes(2);
      expect(renderer.root.findByType('span').props['data-success']).toBe(4);
      await act(async () => {
        renderer.unmount();
      });
    } finally {
      vi.unstubAllGlobals();
    }
  });
});
