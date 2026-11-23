import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { useEffect, type ReactNode } from 'react';
import type * as ReactDomModule from 'react-dom';
import { MemoryRouter, useNavigate } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { MainLayout } from './MainLayout';
import { getPinnedNavStorageKey } from './navPreferences';
import { STORAGE_KEY_SIDEBAR } from '@/utils/constants';

const state = vi.hoisted(() => ({
  fetchConfig: vi.fn().mockResolvedValue({}),
  pluginsList: vi.fn().mockResolvedValue({ pluginsEnabled: false, plugins: [] }),
  monitoring: true,
  prices: true,
  monitoringReason: '',
  supportsPlugin: false,
  connectionStatus: 'disconnected',
  lastGetRouteOrder: null as ((pathname: string) => number | null) | null,
}));
vi.mock('react-dom', async () => {
  const actual = await vi.importActual<typeof ReactDomModule>('react-dom');
  return { ...actual, createPortal: (node: ReactNode) => node };
});
vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: { label?: string }) =>
      options?.label ? `${key}:${options.label}` : key,
  }),
}));
vi.mock('@/stores', () => {
  const store = (selector: (state: object) => unknown = (value) => value) =>
    selector({
      connectionStatus: state.connectionStatus,
      supportsPlugin: state.supportsPlugin,
      apiBase: '',
      config: {},
      fetchConfig: state.fetchConfig,
      clearCache: vi.fn(),
      logout: vi.fn(),
      theme: 'white',
      setTheme: vi.fn(),
      mode: 'full',
      setMode: vi.fn(),
      language: 'en',
      setLanguage: vi.fn(),
      showNotification: vi.fn(),
    });
  return {
    useAuthStore: store,
    useConfigStore: store,
    useThemeStore: store,
    useVisualEffectsStore: store,
    useLanguageStore: store,
    useNotificationStore: store,
  };
});
vi.mock('@/hooks/usePanelFeatureAvailability', () => ({
  usePanelFeatureAvailability: () => ({
    requestMonitoringAvailable: state.monitoring,
    modelPricesAvailable: state.prices,
    reason: state.monitoringReason,
  }),
}));
vi.mock('@/services/api', () => ({ pluginsApi: { list: state.pluginsList } }));
vi.mock('@/router/MainRoutes', () => ({ MainRoutes: () => null }));
vi.mock('@/components/common/PageTransition', () => ({
  PageTransition: ({
    getRouteOrder,
  }: {
    getRouteOrder?: (pathname: string) => number | null;
  }) => {
    state.lastGetRouteOrder = getRouteOrder ?? null;
    return null;
  },
}));
vi.mock('@/components/common/DatabaseMaintenanceBanner', () => ({
  DatabaseMaintenanceBanner: () => null,
}));
vi.mock('@/components/common/DatabaseMaintenanceContext', () => ({
  DatabaseMaintenanceProvider: ({ children }: { children: React.ReactNode }) => children,
}));

let renderer: ReactTestRenderer | undefined;
let navigate: ReturnType<typeof useNavigate>;
function Navigation() {
  const routerNavigate = useNavigate();
  useEffect(() => {
    navigate = routerNavigate;
  }, [routerNavigate]);
  return null;
}
const memory = new Map<string, string>();
const windowListeners = new Map<string, Set<EventListener>>();
const focusedLabels: string[] = [];
async function mount(
  path: string,
  hostRoutes = true,
  options: { enablePinnedNav?: boolean; routeBase?: string } = {}
) {
  const routeBase = options.routeBase ?? '';
  const initialEntry = routeBase ? `${routeBase}${path === '/' ? '' : path}` : path;
  await act(async () => {
    renderer = create(
      <MemoryRouter initialEntries={[initialEntry]}>
        <Navigation />
        <MainLayout
          routeBase={routeBase}
          navigationBase={routeBase}
          enablePinnedNav={options.enablePinnedNav}
          renderRoute={hostRoutes ? () => undefined : undefined}
        />
      </MemoryRouter>,
      {
        createNodeMock: (element) => {
          if (element.type === 'button' || element.type === 'a') {
            const props = element.props as Record<string, unknown>;
            const label = String(props['aria-label'] ?? props.href ?? element.type);
            return {
              focus: () => {
                focusedLabels.push(label);
              },
              contains: () => false,
            };
          }
          return null;
        },
      }
    );
  });
}
const section = (label: string) =>
  renderer!.root
    .findAllByType('button')
    .find((node) => node.props['aria-label'] === `nav.group_${label}`)!;
const maybeSection = (label: string) =>
  renderer!.root
    .findAllByType('button')
    .find((node) => node.props['aria-label'] === `nav.group_${label}`);
const pinButton = (action: 'pin_item' | 'unpin_item', itemLabel: string) =>
  renderer!.root
    .findAllByType('button')
    .find((node) => node.props['aria-label'] === `nav.${action}:${itemLabel}`);
const navLinks = () =>
  renderer!.root.findAllByType('a').filter((node) => node.props.className?.startsWith('nav-item'));

beforeEach(() => {
  memory.clear();
  windowListeners.clear();
  focusedLabels.length = 0;
  state.fetchConfig.mockClear();
  state.pluginsList.mockClear();
  state.pluginsList.mockResolvedValue({ pluginsEnabled: false, plugins: [] });
  state.monitoring = true;
  state.prices = true;
  state.monitoringReason = '';
  state.supportsPlugin = false;
  state.connectionStatus = 'disconnected';
  state.lastGetRouteOrder = null;
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
  vi.stubGlobal('localStorage', {
    getItem: (key: string) => memory.get(key) ?? null,
    setItem: (key: string, value: string) => memory.set(key, value),
  });
  vi.stubGlobal('document', {
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    activeElement: null,
    body: { style: { overflow: '' } },
    documentElement: { style: { setProperty: vi.fn(), removeProperty: vi.fn() } },
  });
  vi.stubGlobal('window', {
    innerHeight: 900,
    addEventListener: vi.fn((event: string, listener: EventListener) => {
      const set = windowListeners.get(event) ?? new Set<EventListener>();
      set.add(listener);
      windowListeners.set(event, set);
    }),
    removeEventListener: vi.fn((event: string, listener: EventListener) => {
      windowListeners.get(event)?.delete(listener);
    }),
    setTimeout,
    clearTimeout,
    matchMedia: () => ({ matches: false }),
  });
});
afterEach(async () => {
  if (renderer) await act(async () => renderer?.unmount());
  renderer = undefined;
  vi.unstubAllGlobals();
});

describe('original MainLayout console navigation', () => {
  it('releases the mobile focus trap and scroll lock when resizing to desktop', async () => {
    let onViewportChange: ((event: { matches: boolean }) => void) | undefined;
    const mobileViewport = {
      matches: true,
      addEventListener: vi.fn((_event: string, listener: (event: { matches: boolean }) => void) => {
        onViewportChange = listener;
      }),
      removeEventListener: vi.fn(),
    };
    vi.spyOn(window, 'matchMedia').mockReturnValue(mobileViewport as unknown as MediaQueryList);
    const previousFocus = { isConnected: true, focus: vi.fn() };
    Object.assign(document, { activeElement: previousFocus });
    await mount('/accounts');
    await act(async () =>
      renderer!.root.findByProps({ className: 'hamburger-container' }).props.onClick()
    );
    expect(document.body.style.overflow).toBe('hidden');
    expect(renderer!.root.findByType('aside').props.className.split(/\s+/)).toContain('open');
    const keyboardListener = vi
      .mocked(document.addEventListener)
      .mock.calls.find(([event]) => event === 'keydown')?.[1];
    mobileViewport.matches = false;
    await act(async () => onViewportChange?.({ matches: false }));
    expect(document.body.style.overflow).toBe('');
    expect(renderer!.root.findByType('aside').props.className.split(/\s+/)).not.toContain('open');
    expect(document.removeEventListener).toHaveBeenCalledWith('keydown', keyboardListener);
    expect(mobileViewport.removeEventListener).toHaveBeenCalledWith('change', onViewportChange);
    expect(previousFocus.focus).toHaveBeenCalledWith({ preventScroll: true });
  });

  it('opens only the current group and keeps dashboard directly reachable', async () => {
    await mount('/accounts');
    expect(section('resources').props['aria-expanded']).toBe(true);
    expect(section('system').props['aria-expanded']).toBe(false);
    expect(navLinks().map((node) => node.props.href)).toEqual([
      '/',
      '/accounts',
      '/ai-providers',
      '/oauth',
    ]);
    expect(
      renderer!.root
        .findAllByType('button')
        .filter((node) => node.props.title === 'header.refresh_all')
    ).toHaveLength(1);
  });

  it('persists group choices and opens the destination group after route navigation', async () => {
    await mount('/accounts');
    await act(async () => section('observe').props.onClick({}));
    expect(JSON.parse(memory.get(`${STORAGE_KEY_SIDEBAR}-sections`)!)).toMatchObject({
      resources: true,
      observe: true,
    });
    await act(async () => section('observe').props.onClick({}));
    expect(section('observe').props['aria-expanded']).toBe(false);
    await act(async () => navigate('/usage-analytics'));
    expect(section('observe').props['aria-expanded']).toBe(true);
    expect(
      navLinks()
        .filter((node) => node.props['aria-current'] === 'page')
        .map((node) => node.props.href)
    ).toEqual(['/usage-analytics']);
  });

  it('selects updates without marking the system overview current', async () => {
    await mount('/system/updates');
    expect(
      navLinks()
        .filter((node) => node.props['aria-current'] === 'page')
        .map((node) => node.props.href)
    ).toEqual(['/system/updates']);
    expect(navLinks().map((node) => node.props.href)).toEqual([
      '/',
      '/system',
      '/system/runtime',
      '/usage-maintenance',
      '/config',
      '/system/updates',
      '/logs',
    ]);
  });

  it('keeps monitoring and model pricing gates and hides the host-only key entry in Panel', async () => {
    state.monitoring = false;
    state.prices = false;
    await mount('/', false);
    const labels = renderer!.root.findAllByType('button').map((node) => node.props['aria-label']);
    expect(labels).not.toContain('nav.group_observe');
    expect(labels).not.toContain('nav.group_clients');
  });

  it('keeps the monitoring entry when only the collector is switched off', async () => {
    state.monitoring = false;
    state.prices = false;
    state.monitoringReason = 'monitoring_disabled';
    await mount('/monitoring');

    expect(navLinks().map((node) => node.props.href)).toContain('/monitoring');
    expect(
      renderer!.root
        .findAllByType('button')
        .map((node) => node.props['aria-label'])
    ).toContain('nav.group_observe');
  });

  it('shows the nested provider task after its existing parent breadcrumb', async () => {
    await mount('/ai-providers/openai/new/models');
    const breadcrumb = renderer!.root.findByProps({ className: 'app-breadcrumb' });
    expect(breadcrumb.findAllByType('span').map((node) => node.children.join(''))).toContain(
      'nav.provider_models'
    );
    expect(breadcrumb.findByType('a').props.href).toBe('/ai-providers');
  });
});

describe('Full Web pinned navigation (Option A multi-expand)', () => {
  it('keeps Panel Lite free of pin buttons and Pinned section', async () => {
    await mount('/accounts', false, { enablePinnedNav: false });
    expect(maybeSection('pinned')).toBeUndefined();
    expect(
      renderer!.root
        .findAllByType('button')
        .filter((node) => String(node.props.className ?? '').includes('nav-pin-button'))
    ).toHaveLength(0);
  });

  it('pins items in order without navigating or fetching, hides empty source groups, and restores on unpin', async () => {
    await mount('/accounts', true, { enablePinnedNav: true, routeBase: '/console' });
    expect(maybeSection('pinned')).toBeUndefined();
    const initialFetchCalls = state.fetchConfig.mock.calls.length;
    const initialPluginCalls = state.pluginsList.mock.calls.length;

    // Expand clients (single item: api-keys) alongside resources (Option A multi-expand)
    await act(async () => section('clients').props.onClick({}));
    expect(section('resources').props['aria-expanded']).toBe(true);
    expect(section('clients').props['aria-expanded']).toBe(true);

    // Pin Accounts first: Pinned appears & auto-expands, resources stays open, focus follows pin button
    focusedLabels.length = 0;
    await act(async () => pinButton('pin_item', 'nav.accounts')!.props.onClick());
    expect(section('pinned').props['aria-expanded']).toBe(true);
    expect(section('resources').props['aria-expanded']).toBe(true);
    expect(focusedLabels[focusedLabels.length - 1]).toBe('nav.unpin_item:nav.accounts');

    // Pin API Keys second: clients becomes empty and hides automatically
    await act(async () => pinButton('pin_item', 'nav.api_keys')!.props.onClick());
    expect(maybeSection('clients')).toBeUndefined();

    // Pinned children preserve chronological pin order, no duplicates in sidebar
    const pinnedContainer = renderer!.root.findByProps({ id: 'nav-pinned' });
    expect(pinnedContainer.findAllByType('a').map((node) => node.props.href)).toEqual([
      '/console/accounts',
      '/console/api-keys',
    ]);
    expect(navLinks().filter((node) => node.props.href === '/console/accounts')).toHaveLength(1);

    // Breadcrumb and canonical route transition order remain anchored to original groups
    const breadcrumb = renderer!.root.findByProps({ className: 'app-breadcrumb' });
    expect(breadcrumb.findAllByType('span').map((node) => node.children.join(''))).toContain(
      'nav.group_resources'
    );
    expect(breadcrumb.findAllByType('span').map((node) => node.children.join(''))).not.toContain(
      'nav.group_pinned'
    );
    const accountsOrder = state.lastGetRouteOrder!('/console/accounts');
    const apiKeysOrder = state.lastGetRouteOrder!('/console/api-keys');
    expect(accountsOrder).not.toBeNull();
    expect(apiKeysOrder).not.toBeNull();
    expect(accountsOrder!).toBeLessThan(apiKeysOrder!);

    // Persistence uses scoped key with version + ordered stable IDs only
    const scopedKey = getPinnedNavStorageKey('/console', 'fullweb');
    expect(JSON.parse(memory.get(scopedKey)!)).toEqual({
      version: 1,
      pinnedIds: ['resources.accounts', 'clients.api_keys'],
    });

    // Pinning triggers zero extra network calls
    expect(state.fetchConfig.mock.calls.length).toBe(initialFetchCalls);
    expect(state.pluginsList.mock.calls.length).toBe(initialPluginCalls);

    // Unpin API Keys: clients group reappears, focus moves to remaining Pinned sibling
    focusedLabels.length = 0;
    await act(async () => pinButton('unpin_item', 'nav.api_keys')!.props.onClick());
    expect(maybeSection('clients')).toBeDefined();
    expect(focusedLabels[focusedLabels.length - 1]).toBe('nav.unpin_item:nav.accounts');

    // Unpin last item (Accounts): Pinned hides completely, focus restores onto Accounts pin button
    focusedLabels.length = 0;
    await act(async () => pinButton('unpin_item', 'nav.accounts')!.props.onClick());
    expect(maybeSection('pinned')).toBeUndefined();
    expect(focusedLabels[focusedLabels.length - 1]).toBe('nav.pin_item:nav.accounts');
    expect(JSON.parse(memory.get(scopedKey)!)).toEqual({ version: 1, pinnedIds: [] });
  });

  it('keeps canonical breadcrumb on nested drill-down routes when parent entry is pinned', async () => {
    const scopedKey = getPinnedNavStorageKey('/console', 'fullweb');
    memory.set(scopedKey, JSON.stringify({ version: 1, pinnedIds: ['resources.ai_providers'] }));
    await mount('/ai-providers/openai/new/models', true, {
      enablePinnedNav: true,
      routeBase: '/console',
    });

    expect(section('pinned').props['aria-expanded']).toBe(true);
    const breadcrumb = renderer!.root.findByProps({ className: 'app-breadcrumb' });
    const crumbText = breadcrumb.findAllByType('span').map((node) => node.children.join(''));
    expect(crumbText).toContain('nav.group_resources');
    expect(crumbText).toContain('nav.provider_models');
    expect(crumbText).not.toContain('nav.group_pinned');
  });

  it('highlights a manually collapsed section containing the active route and resets Pinned collapse after empty', async () => {
    await mount('/accounts', true, { enablePinnedNav: true, routeBase: '/console' });
    await act(async () => pinButton('pin_item', 'nav.accounts')!.props.onClick());
    expect(section('pinned').props.className).not.toContain('section-has-active');

    // Manually collapse Pinned while active route is inside it
    await act(async () => section('pinned').props.onClick({}));
    expect(section('pinned').props['aria-expanded']).toBe(false);
    expect(section('pinned').props.className).toContain('section-has-active');

    // Re-expand and unpin last item, then pin again -> Pinned opens automatically
    await act(async () => section('pinned').props.onClick({}));
    await act(async () => pinButton('unpin_item', 'nav.accounts')!.props.onClick());
    expect(maybeSection('pinned')).toBeUndefined();
    await act(async () => pinButton('pin_item', 'nav.accounts')!.props.onClick());
    expect(section('pinned').props['aria-expanded']).toBe(true);
  });

  it('closes the collapsed-sidebar flyout cleanly with focus restoration and syncs storage events', async () => {
    memory.set(STORAGE_KEY_SIDEBAR, 'true');
    await mount('/accounts', true, { enablePinnedNav: true, routeBase: '/console' });

    // Open flyout for single-item clients section
    await act(async () =>
      section('clients').props.onClick({
        currentTarget: { getBoundingClientRect: () => ({ top: 120, right: 54 }) },
      })
    );
    expect(renderer!.root.findAllByProps({ className: 'nav-flyout' })).toHaveLength(1);

    // Pin API Keys inside flyout -> clients vanishes, flyout closes cleanly, focus lands on Pinned toggle
    focusedLabels.length = 0;
    await act(async () => pinButton('pin_item', 'nav.api_keys')!.props.onClick());
    expect(renderer!.root.findAllByProps({ className: 'nav-flyout' })).toHaveLength(0);
    expect(maybeSection('clients')).toBeUndefined();
    expect(maybeSection('pinned')).toBeDefined();
    expect(focusedLabels[focusedLabels.length - 1]).toBe('nav.group_pinned');

    // Cross-tab storage event updates pinned state without polling
    const scopedKey = getPinnedNavStorageKey('/console', 'fullweb');
    const storageListeners = Array.from(windowListeners.get('storage') ?? []);
    await act(async () => {
      for (const listener of storageListeners) {
        listener({
          key: scopedKey,
          newValue: JSON.stringify({ version: 1, pinnedIds: ['system.logs'] }),
        } as StorageEvent);
      }
    });
    expect(maybeSection('clients')).toBeDefined();
    expect(maybeSection('pinned')).toBeDefined();
  });

  it('toggles mobile/touch pin edit mode cleanly and skips sticky focus restoration on pointer clicks', async () => {
    await mount('/accounts', true, { enablePinnedNav: true, routeBase: '/console' });
    const aside = () => renderer!.root.findByType('aside');
    const editToggle = () =>
      renderer!.root
        .findAllByType('button')
        .find((node) => node.props['aria-label'] === 'nav.manage_pinned_aria')!;

    expect(aside().props.className).not.toContain('pin-edit-mode');
    expect(editToggle().props['aria-pressed']).toBe(false);

    await act(async () => editToggle().props.onClick());
    expect(aside().props.className).toContain('pin-edit-mode');
    expect(editToggle().props['aria-pressed']).toBe(true);

    // Pointer click (detail: 1) pins item without leaving sticky focus on the moved pin button
    focusedLabels.length = 0;
    await act(async () => pinButton('pin_item', 'nav.accounts')!.props.onClick({ detail: 1 }));
    expect(focusedLabels).toHaveLength(0);
    expect(aside().props.className).toContain('pin-edit-mode');

    // Closing edit mode exits pin-edit-mode
    await act(async () => editToggle().props.onClick());
    expect(aside().props.className).not.toContain('pin-edit-mode');

    // Opening mobile drawer, enabling edit mode, then closing drawer auto-exits pin-edit-mode
    const mobileViewport = {
      matches: true,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    };
    vi.spyOn(window, 'matchMedia').mockReturnValue(mobileViewport as unknown as MediaQueryList);
    await act(async () =>
      renderer!.root.findByProps({ className: 'hamburger-container' }).props.onClick()
    );
    expect(aside().props.className.split(/\s+/)).toContain('open');
    await act(async () => editToggle().props.onClick());
    expect(aside().props.className.split(/\s+/)).toContain('pin-edit-mode');
    await act(async () =>
      renderer!.root.findByProps({ className: 'sidebar-backdrop visible' }).props.onClick()
    );
    expect(aside().props.className.split(/\s+/)).not.toContain('open');
    expect(aside().props.className.split(/\s+/)).not.toContain('pin-edit-mode');
  });
});
