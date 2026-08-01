import { describe, expect, it } from 'vitest';
import { createProviderRecentRequestsCacheController } from './useProviderRecentRequests';

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
