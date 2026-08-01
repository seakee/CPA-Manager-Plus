import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { handleDemoApiRequest } from './demoApi';
import { resetDemoCredentialRefresh } from './demoFixtures';
import type { AuthFilesResponse } from '@/types/authFile';

const DEMO_AUTH_ID = 'codex-upgrade-demo-runtime';
const DEMO_AUTH_NAME = 'codex-upgrade-demo.json';
const FORCE_REFRESH_TIMESTAMP = '2000-01-01T00:00:00Z';

const getUpgradeDemoAuth = async () => {
  const response = await handleDemoApiRequest<AuthFilesResponse>('get', '/auth-files');
  const target = response.files.find((file) => file.id === DEMO_AUTH_ID);
  if (!target) throw new Error('missing Codex upgrade demo auth file');
  return target;
};

describe('auth file credential refresh demo API', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-07-22T09:30:00+08:00'));
    resetDemoCredentialRefresh();
  });

  afterEach(() => {
    resetDemoCredentialRefresh();
    vi.useRealTimers();
  });

  it('simulates a delayed Free to Plus refresh for the runtime auth ID', async () => {
    resetDemoCredentialRefresh();
    const initial = await getUpgradeDemoAuth();
    const initialLastRefresh = initial.last_refresh;

    expect(initial).toMatchObject({
      id: DEMO_AUTH_ID,
      name: DEMO_AUTH_NAME,
      plan_type: 'free',
      id_token: { plan_type: 'free' },
    });

    await handleDemoApiRequest('patch', '/auth-files/fields', {
      name: DEMO_AUTH_ID,
      expired: FORCE_REFRESH_TIMESTAMP,
      last_refresh: FORCE_REFRESH_TIMESTAMP,
    });

    const firstPoll = await getUpgradeDemoAuth();
    expect(firstPoll.plan_type).toBe('free');
    expect(firstPoll.last_refresh).toBe(initialLastRefresh);

    const completed = await getUpgradeDemoAuth();
    expect(completed).toMatchObject({
      plan_type: 'plus',
      id_token: { plan_type: 'plus' },
      statusMessage: 'Ready',
    });
    expect(completed.last_refresh).toBe('2026-07-22T01:30:00.000Z');
    expect(completed.last_refresh).not.toBe(initialLastRefresh);
  });

  it('does not upgrade the demo account for an ordinary fields patch', async () => {
    await handleDemoApiRequest('patch', '/auth-files/fields', {
      name: DEMO_AUTH_NAME,
      note: 'Demo note',
    });

    await getUpgradeDemoAuth();
    const target = await getUpgradeDemoAuth();

    expect(target.plan_type).toBe('free');
  });

  it('resets the upgraded account back to Free', async () => {
    await handleDemoApiRequest('patch', '/auth-files/fields', {
      name: DEMO_AUTH_NAME,
      expired: FORCE_REFRESH_TIMESTAMP,
      last_refresh: FORCE_REFRESH_TIMESTAMP,
    });
    await getUpgradeDemoAuth();
    expect((await getUpgradeDemoAuth()).plan_type).toBe('plus');

    resetDemoCredentialRefresh();

    expect((await getUpgradeDemoAuth()).plan_type).toBe('free');
  });
});
