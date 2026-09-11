import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { useLayoutEffect } from 'react';
import { MemoryRouter, useNavigate, type NavigateFunction } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ManagerUpdates, useManagerUpdates } from './ManagerUpdates';
import type { ReleaseInfo, UpdateStatus } from './managerUpdateApi';

const mocks = vi.hoisted(() => ({ request: vi.fn(), available: true }));
vi.mock('@/stores', () => ({
  useAuthStore: (select: (s: unknown) => unknown) =>
    select({ isAuthenticated: true, managementKey: 'test' }),
}));
vi.mock('@/hooks/usePanelFeatureAvailability', () => ({
  usePanelFeatureAvailability: () => ({
    managerServiceAvailable: mocks.available,
    managerServiceBase: 'http://manager.test',
  }),
}));
vi.mock('@/features/demo/demoMode', () => ({ isDemoMode: () => false }));
vi.mock('./managerUpdateApi', () => ({ managerUpdateRequest: mocks.request }));
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key, i18n: { language: 'en' } }),
}));
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
let renderer: ReactTestRenderer | undefined;
let controls: ReturnType<typeof useManagerUpdates>;
let navigate: NavigateFunction;
function Consumer() {
  const value = useManagerUpdates();
  const go = useNavigate();
  useLayoutEffect(() => {
    controls = value;
    navigate = go;
  }, [value, go]);
  return null;
}
const info = (tag: string) =>
  ({
    schema_version: 1,
    release: { version: tag, stage: 'stable', source_commit: 'a'.repeat(40) },
    content: { summary: { en: tag, zh: tag }, notes: {} },
    update: {
      breaking: false,
      migration_required: false,
      minimum_direct_upgrade_version: null,
      upgrade_guide_url: 'https://github.com/seakee/CPA-Manager-Plus/releases/tag/' + tag,
    },
    distribution: {
      docker: { image: 'seakee/cpa-manager-plus', version_tag: tag },
      native: { assets: [] },
    },
    compatibility: { minimum_cpa_version: null },
  }) as ReleaseInfo;
let target: ReleaseInfo;
let claimed: Set<string>;
beforeEach(() => {
  mocks.available = true;
  target = info('v2.0.0');
  claimed = new Set();
  vi.stubGlobal('window', { setInterval: vi.fn(() => 1), clearInterval: vi.fn() });
  vi.stubGlobal('document', {
    visibilityState: 'visible',
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  });
  mocks.request.mockImplementation(async (_base, _key, suffix = '') => {
    if (suffix === '/notification') {
      if (claimed.has(target.release.version)) return null;
      claimed.add(target.release.version);
      return target;
    }
    if (suffix === '/dismiss') return { ok: true };
    return {
      state: 'update_available',
      target,
      stale: false,
      channel_preference: 'stable',
    } as UpdateStatus;
  });
});
afterEach(async () => {
  if (renderer) await act(async () => renderer?.unmount());
  renderer = undefined;
  vi.unstubAllGlobals();
  vi.clearAllMocks();
});
async function mount(initialPath = '/') {
  await act(async () => {
    renderer = create(
      <MemoryRouter initialEntries={[initialPath]}>
        <ManagerUpdates>
          <Consumer />
        </ManagerUpdates>
      </MemoryRouter>
    );
  });
}
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<T>((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { promise, resolve, reject };
}
describe('one-time update notification', () => {
  it('opens the internal update page from the notice and does not repeat on return', async () => {
    await mount();
    const link = renderer!.root.findByType('a');
    expect(link.props.href).toBe('/system/updates');
    expect(link.props.target).toBeUndefined();
    await act(async () => {
      link.props.onClick({ button: 0, defaultPrevented: false, preventDefault: vi.fn() });
    });
    expect(renderer!.root.findAllByType('aside')).toHaveLength(0);
    await act(async () => {
      await navigate('/');
    });
    expect(renderer!.root.findAllByType('aside')).toHaveLength(0);
    expect(window.setInterval).toHaveBeenCalledTimes(1);
  });

  it('records a version viewed on the update page without displaying a notice', async () => {
    await mount('/system/updates');
    expect(claimed.has(target.release.version)).toBe(true);
    expect(renderer!.root.findAllByType('aside')).toHaveLength(0);
    await act(async () => {
      await navigate('/');
      await controls.check();
    });
    expect(renderer!.root.findAllByType('aside')).toHaveLength(0);
  });

  it.each(['/', '/system/updates'])(
    'does not surface a delayed claim after viewing details from %s',
    async (initialPath) => {
      const pending = deferred<ReleaseInfo>();
      mocks.request.mockImplementation(async (_base, _key, suffix = '') =>
        suffix === '/notification'
          ? pending.promise
          : ({ state: 'update_available', target, stale: false } as UpdateStatus)
      );
      await mount(initialPath);
      if (initialPath === '/') {
        await act(async () => {
          await navigate('/system/updates');
        });
      }
      await act(async () => {
        await navigate('/');
      });
      await act(async () => {
        pending.resolve(target);
      });
      expect(renderer!.root.findAllByType('aside')).toHaveLength(0);
    }
  );

  it('dismisses immediately and does not repeat after manual check or remount; a new tag may notify', async () => {
    await mount();
    expect(renderer!.root.findAllByType('aside')).toHaveLength(1);
    await act(async () => {
      renderer!.root.findByType('button').props.onClick();
    });
    expect(renderer!.root.findAllByType('aside')).toHaveLength(0);
    await act(async () => {
      await controls.check();
    });
    expect(renderer!.root.findAllByType('aside')).toHaveLength(0);
    await act(async () => {
      renderer!.unmount();
    });
    await mount();
    expect(renderer!.root.findAllByType('aside')).toHaveLength(0);
    target = info('v2.0.1');
    await act(async () => {
      await controls.check();
    });
    expect(renderer!.root.findAllByType('aside')).toHaveLength(1);
  });
  it('does not consume a notification while hidden or without a Manager', async () => {
    Object.defineProperty(document, 'visibilityState', { value: 'hidden', configurable: true });
    await mount();
    expect(claimed.size).toBe(0);
    expect(renderer!.root.findAllByType('aside')).toHaveLength(0);
    await act(async () => {
      renderer!.unmount();
    });
    mocks.available = false;
    mocks.request.mockClear();
    await mount();
    expect(mocks.request).not.toHaveBeenCalled();
  });
  it('discards a claim from the previous channel and can notify the new target', async () => {
    const pending = deferred<ReleaseInfo>();
    const previous = info('v2.1.0-beta.1');
    target = previous;
    let claims = 0;
    mocks.request.mockImplementation(async (_base, _key, suffix = '') => {
      if (suffix === '/notification') return ++claims === 1 ? pending.promise : target;
      return { state: 'update_available', target, stale: false } as UpdateStatus;
    });
    await mount();
    target = info('v2.0.0');
    await act(async () => {
      await controls.setChannel('stable');
    });
    await act(async () => {
      pending.resolve(previous);
    });
    expect(renderer!.root.findAllByType('aside')).toHaveLength(0);
    await act(async () => {
      await controls.check();
    });
    expect(renderer!.root.findAllByType('aside')).toHaveLength(1);
    expect(renderer!.root.findByType('p').children).toEqual(['v2.0.0']);
  });
  it('retains a valid claim across polling of the same target', async () => {
    const pending = deferred<ReleaseInfo>();
    mocks.request.mockImplementation(async (_base, _key, suffix = '') =>
      suffix === '/notification'
        ? pending.promise
        : ({ state: 'update_available', target, stale: false } as UpdateStatus)
    );
    await mount();
    await act(async () => {
      (vi.mocked(window.setInterval).mock.calls[0][0] as () => void)();
    });
    await act(async () => {
      pending.resolve(target);
    });
    expect(renderer!.root.findAllByType('aside')).toHaveLength(1);
  });
  it('ignores an earlier failed read after a successful channel change', async () => {
    const pending = deferred<UpdateStatus>();
    mocks.request.mockImplementation(async (_base, _key, suffix = '') => {
      if (!suffix) return pending.promise;
      if (suffix === '/notification') return null;
      return { state: 'update_available', target, stale: false } as UpdateStatus;
    });
    await mount();
    await act(async () => {
      await controls.setChannel('beta');
    });
    await act(async () => {
      pending.reject(new Error('old request failed'));
    });
    expect(controls.error).toBe(false);
  });
  it('pauses polling while a channel mutation is pending', async () => {
    await mount();
    const pending = deferred<UpdateStatus>();
    mocks.request.mockImplementation((_base, _key, suffix = '') =>
      suffix === '/channel' ? pending.promise : Promise.resolve(null)
    );
    let changed!: Promise<void>;
    await act(async () => {
      changed = controls.setChannel('beta');
    });
    mocks.request.mockClear();
    await act(async () => {
      (vi.mocked(window.setInterval).mock.calls[0][0] as () => void)();
    });
    expect(mocks.request).not.toHaveBeenCalled();
    await act(async () => {
      pending.resolve({ state: 'update_available', target, stale: false } as UpdateStatus);
      await changed;
    });
    expect(controls.busy).toBe(false);
  });
  it('removes an open notice when cached information becomes stale', async () => {
    await mount();
    mocks.request.mockResolvedValue({
      state: 'update_available',
      target,
      stale: true,
    } as UpdateStatus);
    await act(async () => {
      await controls.check();
    });
    expect(renderer!.root.findAllByType('aside')).toHaveLength(0);
  });

  it('removes an open notice on a failed check and does not repeat it after recovery', async () => {
    await mount();
    mocks.request.mockRejectedValueOnce(new Error('offline'));
    await act(async () => {
      await controls.check();
    });
    expect(controls.error).toBe(true);
    expect(renderer!.root.findAllByType('aside')).toHaveLength(0);
    await act(async () => {
      await controls.check();
    });
    expect(controls.error).toBe(false);
    expect(renderer!.root.findAllByType('aside')).toHaveLength(0);
  });

  it('does not restore an in-flight notice after a newer check fails', async () => {
    const pending = deferred<ReleaseInfo>();
    mocks.request.mockImplementation(async (_base, _key, suffix = '') => {
      if (suffix === '/notification') return pending.promise;
      if (suffix === '/check') throw new Error('offline');
      return { state: 'update_available', target, stale: false } as UpdateStatus;
    });
    await mount();
    await act(async () => {
      await controls.check();
    });
    await act(async () => {
      pending.resolve(target);
    });
    expect(controls.error).toBe(true);
    expect(renderer!.root.findAllByType('aside')).toHaveLength(0);
  });
});
