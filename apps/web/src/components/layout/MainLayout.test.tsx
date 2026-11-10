import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { useEffect } from 'react';
import { MemoryRouter, useNavigate } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { MainLayout } from './MainLayout';
import { STORAGE_KEY_SIDEBAR } from '@/utils/constants';

const state = vi.hoisted(() => ({
  fetchConfig: vi.fn().mockResolvedValue({}),
  monitoring: true,
  prices: true,
  monitoringReason: '',
}));
vi.mock('react-i18next', () => ({ useTranslation: () => ({ t: (key: string) => key }) }));
vi.mock('@/stores', () => {
  const store = (selector: (state: object) => unknown = (value) => value) =>
    selector({
      connectionStatus: 'disconnected',
      supportsPlugin: false,
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
vi.mock('@/services/api', () => ({ pluginsApi: { list: vi.fn() } }));
vi.mock('@/router/MainRoutes', () => ({ MainRoutes: () => null }));
vi.mock('@/components/common/PageTransition', () => ({ PageTransition: () => null }));
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
async function mount(path: string, hostRoutes = true) {
  await act(async () => {
    renderer = create(
      <MemoryRouter initialEntries={[path]}>
        <Navigation />
        <MainLayout renderRoute={hostRoutes ? () => undefined : undefined} />
      </MemoryRouter>
    );
  });
}
const section = (label: string) =>
  renderer!.root
    .findAllByType('button')
    .find((node) => node.props['aria-label'] === `nav.group_${label}`)!;
const navLinks = () =>
  renderer!.root.findAllByType('a').filter((node) => node.props.className?.startsWith('nav-item'));

beforeEach(() => {
  memory.clear();
  state.monitoring = true;
  state.prices = true;
  state.monitoringReason = '';
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
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
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
    expect(renderer!.root.findByType('aside').props.className).toContain(' open ');
    const keyboardListener = vi
      .mocked(document.addEventListener)
      .mock.calls.find(([event]) => event === 'keydown')?.[1];
    mobileViewport.matches = false;
    await act(async () => onViewportChange?.({ matches: false }));
    expect(document.body.style.overflow).toBe('');
    expect(renderer!.root.findByType('aside').props.className).not.toContain(' open ');
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
      '/config',
      '/logs',
      '/system/updates',
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
