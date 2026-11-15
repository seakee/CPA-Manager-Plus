import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import type { ReactNode } from 'react';
import { MemoryRouter } from 'react-router-dom';
import { Button } from '../components/ui/Button';
import i18n from '../i18n';
import { apiKeysApi } from '../services/api/apiKeys';
import { configApi } from '../services/api/config';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { FreshConsolePage } from './FreshConsolePage';
import { FreshInferencePrefixCompletion } from './FreshInferencePrefixCompletion';
import { FreshManagedRuntime } from './FreshManagedRuntime';
import { FreshExternalConnection } from './FreshExternalConnection';
import { createPublicApiClient } from './publicApi';
import type { FreshOperation } from './bootstrapTransport';
import { fullWebLanguage, fullWebText as t, setFullWebLanguage } from '../i18n/fullweb';
import { LegacyConsolePage } from './LegacyConsolePage';
import { LoginPage } from '../features/login/LoginPage';
import { useAuthStore } from '../stores/useAuthStore';
import { useConfigStore } from '../stores/useConfigStore';
import { useLanguageStore } from '../stores/useLanguageStore';
import { obfuscatedStorage } from '../services/storage/secureStorage';
import { STORAGE_KEY_AUTH } from '../utils/constants';
import { usageServiceApi } from '../services/api/usageService';
import { ManagerUpdates } from '../features/system/ManagerUpdates';
import { ConfirmationModal } from '../components/common/ConfirmationModal';
import { NotificationContainer } from '../components/common/NotificationContainer';
import { MainLayout } from '../components/layout/MainLayout';
import { FreshManagedResources } from './FreshManagedResources';
import { SystemPage } from '../features/system/SystemPage';
import { FreshSystemWorkspace } from './FreshSystemWorkspace';
import { useNotificationStore } from '../stores/useNotificationStore';
import { configurationText as c } from '../i18n/configurationWorkspace';
import { gatewayViewFixture } from './gatewayViewFixture';
vi.mock('../components/ui/Modal', () => ({
  Modal: ({ open, children, footer }: { open: boolean; children: ReactNode; footer: ReactNode }) =>
    open ? (
      <div role="dialog">
        {children}
        {footer}
      </div>
    ) : null,
}));
vi.mock('../components/common/NotificationContainer', () => ({
  NotificationContainer: () => null,
}));
const context = { basePath: '/console', apiBase: '/console/api/v2', revision: 1 };
const operation: FreshOperation = {
  identity: {
    admissionId: 'admission',
    installationId: 'installation',
    generation: 'generation',
    databasePath: '/data/usage.sqlite',
    operationId: 'fresh-console-operation',
  },
  revision: 6,
  adminInitialized: true,
  gatewayPending: true,
  productComplete: true,
  routing: {
    operationId: 'fresh-console-operation',
    target: '/console',
    revision: 1,
    stage: 'committed',
    committed: true,
  },
};
const key = 'Cpamp_Test_Only_Console_Admin_123456';
describe('Fresh installation and Console authority boundary', () => {
  let renderer: ReactTestRenderer;
  let location: URL;
  let historyState: unknown = null;
  let read: ReturnType<typeof vi.fn<typeof fetch>>;
  const persist = vi.fn((_key: string, _value: string) => undefined);
  const storage = new Map<string, string>();
  beforeEach(() => {
    (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
    location = new URL('https://manager.example/console');
    historyState = null;
    const stubWindow = {
      location,
      history: {
        get state() {
          return historyState;
        },
        pushState: (state: unknown, _title: string, path?: string | URL | null) => {
          historyState = state;
          if (typeof path === 'string' && path.length > 0) {
            location.href = new URL(path, location).href;
          } else if (path instanceof URL) {
            location.href = path.href;
          }
        },
      },
      addEventListener: (event: string, handler: () => void) => {
        void event;
        void handler;
      },
      removeEventListener: vi.fn(),
      setTimeout: globalThis.setTimeout,
      clearTimeout: globalThis.clearTimeout,
      setInterval: globalThis.setInterval,
      clearInterval: globalThis.clearInterval,
      matchMedia: () => ({
        matches: false,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
      }),
    };
    vi.stubGlobal('window', stubWindow);
    vi.stubGlobal('document', {
      defaultView: stubWindow,
      documentElement: {
        classList: { toggle: vi.fn() },
        setAttribute: vi.fn(),
        style: { setProperty: vi.fn(), removeProperty: vi.fn() },
      },
      body: { style: { overflow: '' } },
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    });
    storage.clear();
    vi.stubGlobal('localStorage', {
      getItem: vi.fn((name: string) => storage.get(name) ?? null),
      setItem: (name: string, value: string) => {
        persist(name, value);
        storage.set(name, value);
      },
      removeItem: (name: string) => storage.delete(name),
    });
    vi.stubGlobal('sessionStorage', {
      getItem: vi.fn(() => null),
      setItem: persist,
      removeItem: vi.fn(),
    });
    useAuthStore.getState().logout();
    useAuthStore.setState({ rememberPassword: false });
    persist.mockClear();
    useNotificationStore.getState().hideConfirmation();
    useNotificationStore.getState().clearAll();
    vi.spyOn(apiKeysApi, 'list').mockResolvedValue([]);
    read = vi
      .fn<typeof fetch>()
      .mockImplementation(() => Promise.resolve(Response.json(operation)));
    // CI runs with an English browser: make the language chain start from 'en'
    // instead of whatever locale the developer machine happens to have.
    vi.stubGlobal('navigator', { language: 'en-US', languages: ['en-US'] });
    useLanguageStore.setState({ language: 'en' });
    setFullWebLanguage('en');
    void i18n.changeLanguage('en');
  });
  afterEach(async () => {
    if (renderer) await act(async () => renderer.unmount());
    useAuthStore.getState().logout();
    useNotificationStore.getState().hideConfirmation();
    vi.restoreAllMocks();
    useLanguageStore.setState({ language: 'en' });
    setFullWebLanguage('en');
    await i18n.changeLanguage('en');
    vi.unstubAllGlobals();
  });
  const find = (id: string) => renderer.root.findByProps({ 'data-testid': id });
  const has = (id: string) => renderer.root.findAllByProps({ 'data-testid': id }).length > 0;
  const hasLogin = () => renderer.root.findAllByType(LoginPage).length > 0;
  const input = () =>
    renderer.root.findAllByType('input').find((node) => node.props.id === 'console-admin-key')!;
  const logout = () =>
    renderer.root
      .findAllByType('button')
      .find((button) =>
        [t('logout'), 'Logout', '登出', 'Выйти'].includes(button.props['aria-label'])
      )!;
  async function mount(retryTransport?: typeof fetch, mountContext = context) {
    await act(async () => {
      renderer = create(
        <FreshConsolePage
          context={mountContext}
          client={createPublicApiClient(
            () => mountContext,
            async (url, init) => {
              const target = String(url);
              if (target.endsWith('/api-keys'))
                return Response.json({
                  keys: [],
                  gatewayPending: true,
                  revocationAvailable: false,
                });
              if (target.endsWith('/settings/update-channel'))
                return Response.json({ channel_preference: 'auto' });
              if (target.endsWith('/installation/runtime'))
                return Response.json({
                  runtimeIdentity: 'runtime',
                  runtimeGeneration: '17486582467214426535',
                  activeGatewayArtifact: { artifactId: 'sha256:' + 'a'.repeat(64) },
                  state: 'ready',
                });
              if (target.includes('/gateway/config'))
                return retryTransport
                  ? retryTransport(url, init)
                  : new Response('', { status: 503 });
              return read(url, init);
            }
          )}
        />
      );
    });
    await act(async () => {
      await vi.dynamicImportSettled();
    });
  }
  async function login(remember = false) {
    await act(async () => input().props.onChange({ target: { value: key } }));
    await act(async () =>
      renderer.root
        .findByProps({ type: 'checkbox' })
        .props.onChange({ target: { checked: remember } })
    );
    await act(async () => find('console-login').props.onSubmit({ preventDefault: vi.fn() }));
    await act(async () => {
      await vi.dynamicImportSettled();
    });
  }
  async function navigate(route: string) {
    const section =
      ['/accounts', '/oauth'].includes(route) || route.startsWith('/ai-providers')
        ? 'resources'
        : route === '/api-keys'
          ? 'clients'
          : 'system';
    if (!renderer.root.findAllByType('a').some((link) => link.props.href === '/console' + route))
      await act(async () =>
        renderer.root
          .findAllByType('button')
          .find((button) => button.props['aria-controls'] === `nav-${section}`)!
          .props.onClick({ currentTarget: null })
      );
    await act(async () =>
      renderer.root
        .findAllByType('a')
        .find((link) => link.props.href === '/console' + route)!
        .props.onClick({
          button: 0,
          preventDefault: vi.fn(),
          currentTarget: null,
          target: null,
          metaKey: false,
          altKey: false,
          ctrlKey: false,
          shiftKey: false,
        })
    );
  }
  it('uses the original login page and persists no credential without remember-login', async () => {
    await mount();
    expect(hasLogin()).toBe(true);
    await login();
    expect(has('legacy-console')).toBe(true);
    expect(renderer.root.findByType(LegacyConsolePage)).toBeTruthy();
    expect(renderer.root.findAllByType(ManagerUpdates)).toHaveLength(1);
    expect(renderer.root.findAllByType(ConfirmationModal)).toHaveLength(1);
    expect(renderer.root.findAllByType(NotificationContainer)).toHaveLength(1);
    expect(read.mock.calls[0][0]).toBe('https://manager.example/console/api/v2/operation');
    expect(new Headers(read.mock.calls[0][1]?.headers).get('Authorization')).toBe('Bearer ' + key);
    await act(async () => logout().props.onClick());
    expect(input().props.value).toBe('');
    await act(async () => renderer.unmount());
    await mount();
    expect(hasLogin()).toBe(true);
    // The legacy layout clears unrelated cached stores on logout. Verify the
    // Fresh administrator credential itself never appears in a persistence
    // payload while allowing those normal cache writes.
    expect(persist.mock.calls.flat().some((value) => String(value).includes(key))).toBe(false);
    expect(
      obfuscatedStorage.getItem<{ state: { managementKey?: string } }>(STORAGE_KEY_AUTH)?.state
        .managementKey
    ).toBeUndefined();
  });
  it('keeps clear-login locked on both System route spellings while an editor needs recovery', async () => {
    await mount();
    await login();
    await act(async () => renderer.root.findByType(FreshManagedResources).props.onPending(true));
    const renderRoute = renderer.root.findByType(MainLayout).props.renderRoute;
    for (const path of ['/system', '/system/']) {
      const page = renderRoute(path);
      expect(page.type).toBe(SystemPage);
      expect(page.props.logoutDisabled).toBe(true);
      expect(page.props.workspace.type).toBe(FreshSystemWorkspace);
    }
    expect(renderRoute('/api-keys/')).toBeNull();
  });
  const retryTransport: typeof fetch = async () => Response.json(gatewayViewFixture());
  async function editGateway() {
    await mount(retryTransport);
    await login();
    await navigate('/config');
    await act(async () => find('gateway-tab-retry').props.onClick());
    await act(async () =>
      find('gateway-field-routing.retry.request-retry')
        .findByType('input')
        .props.onChange({ target: { value: '4' } })
    );
  }
  it('keeps existing system information and session tools in a collapsed section below product settings', async () => {
    await mount();
    await login();
    await navigate('/system');
    const system = renderer.root.findByType(SystemPage);
    const details = system.findByProps({ className: 'system-additional-info' });
    expect(details.type).toBe('details');
    expect(details.props.open).not.toBe(true);
    expect(details.findByType('summary').props.children).toBe(c('additionalInfo'));
    expect(details.findAllByType(FreshSystemWorkspace)).toHaveLength(0);
    expect(system.findAllByType(FreshSystemWorkspace)).toHaveLength(1);
    expect(
      details
        .findAllByType(Button)
        .some((button) => button.props.children === i18n.t('system_info.clear_login_button'))
    ).toBe(true);
  });
  it('keeps a real gateway draft on logout cancellation and clears the session only after confirmation', async () => {
    await editGateway();
    expect(renderer.root.findByType(MainLayout).props.logoutDisabled).toBe(false);
    await navigate('/system');
    expect(renderer.root.findByType(SystemPage).props.hasUnsavedChanges).toBe(true);
    await act(async () => logout().props.onClick());
    expect(has('legacy-console')).toBe(true);
    expect(useNotificationStore.getState().confirmation.options?.message).toBe(c('leaveHelp'));
    const keep = renderer.root
      .findAllByType(Button)
      .find((button) => button.props.children === c('keep'))!;
    await act(async () => keep.props.onClick());
    await navigate('/config');
    expect(find('gateway-field-routing.retry.request-retry').findByType('input').props.value).toBe('4');
    await act(async () => logout().props.onClick());
    await act(async () => {
      await useNotificationStore.getState().confirmation.options!.onConfirm();
    });
    expect(has('legacy-console')).toBe(false);
  });
  it('rechecks the operation lock when confirming a previously opened logout dialog', async () => {
    await editGateway();
    await act(async () => logout().props.onClick());
    const confirm = useNotificationStore.getState().confirmation.options!.onConfirm;
    await act(async () => renderer.root.findByType(FreshManagedResources).props.onPending(true));
    await act(async () => {
      await confirm();
    });
    expect(has('legacy-console')).toBe(true);
    expect(find('gateway-field-routing.retry.request-retry').findByType('input').props.value).toBe('4');
  });
  it('warns before clearing login storage and rechecks a newly started operation', async () => {
    await editGateway();
    await navigate('/system');
    const clearButton = renderer.root
      .findByType(SystemPage)
      .findAllByType(Button)
      .find((button) => button.props.children === i18n.t('system_info.clear_login_button'))!;
    await act(async () => clearButton.props.onClick());
    expect(String(useNotificationStore.getState().confirmation.options?.message)).toContain(
      c('leaveHelp')
    );
    const confirm = useNotificationStore.getState().confirmation.options!.onConfirm;
    await act(async () => renderer.root.findByType(FreshManagedResources).props.onPending(true));
    await act(async () => {
      await confirm();
    });
    expect(has('legacy-console')).toBe(true);
    await act(async () => renderer.root.findByType(FreshManagedResources).props.onPending(false));
    await act(async () => clearButton.props.onClick());
    await act(async () => {
      await useNotificationStore.getState().confirmation.options!.onConfirm();
    });
    expect(has('legacy-console')).toBe(false);
  });
  it('does not block credential revocation on a local dirty draft', async () => {
    await editGateway();
    await act(async () => renderer.root.findByType(LegacyConsolePage).props.onUnauthorized());
    expect(has('legacy-console')).toBe(false);
    expect(input().props.value).toBe('');
  });
  it('never calls legacy discovery or legacy login/restore before the business Console is admitted', async () => {
    const discovery = vi
      .spyOn(usageServiceApi, 'getInfo')
      .mockRejectedValue(new Error('Legacy discovery is forbidden'));
    const legacyLogin = vi
      .spyOn(useAuthStore.getState(), 'login')
      .mockRejectedValue(new Error('Legacy login is forbidden'));
    const legacyRestore = vi
      .spyOn(useAuthStore.getState(), 'restoreSession')
      .mockResolvedValue(false);
    read.mockImplementation(() =>
      Promise.resolve(Response.json({ ...operation, productComplete: false }))
    );
    await mount();
    await login(true);
    expect(has('legacy-console')).toBe(false);
    expect(renderer.root.findByType(FreshManagedRuntime).props.guided).toBe(true);
    expect(discovery).not.toHaveBeenCalled();
    expect(legacyLogin).not.toHaveBeenCalled();
    expect(legacyRestore).not.toHaveBeenCalled();
    expect(read.mock.calls[0][0]).toBe('https://manager.example/console/api/v2/operation');
  });
  it('restores a remembered session through canonical authentication after refresh and clears it on logout', async () => {
    await mount();
    await login(true);
    expect(useAuthStore.getState().rememberPassword).toBe(true);
    expect(
      obfuscatedStorage.getItem<{ state: { managementKey?: string } }>(STORAGE_KEY_AUTH)?.state
        .managementKey
    ).toBe(key);
    await act(async () => renderer.unmount());
    // Rehydrate persisted state as a new document would; no authenticated flag
    // is persisted and no management page may bypass the operation check.
    useAuthStore.setState({ isAuthenticated: false });
    await useAuthStore.persist.rehydrate();
    read.mockClear();
    await mount();
    expect(read).toHaveBeenCalledTimes(1);
    expect(has('legacy-console')).toBe(true);
    await act(async () => logout().props.onClick());
    await act(async () => renderer.unmount());
    read.mockClear();
    await mount();
    expect(hasLogin()).toBe(true);
    expect(read).not.toHaveBeenCalled();
    expect(useAuthStore.getState().managementKey).toBe('');
  });
  it('rejects and removes a remembered credential when the server revokes it', async () => {
    await mount();
    await login(true);
    await act(async () => renderer.unmount());
    read.mockImplementation(() => Promise.resolve(new Response('', { status: 401 })));
    await mount();
    expect(hasLogin()).toBe(true);
    expect(has('legacy-console')).toBe(false);
    expect(useAuthStore.getState().managementKey).toBe('');
  });
  it('retains a remembered credential across a temporary server failure without mounting management pages', async () => {
    await mount();
    await login(true);
    await act(async () => renderer.unmount());
    read.mockImplementation(() => Promise.resolve(new Response('', { status: 503 })));
    await mount();
    expect(hasLogin()).toBe(true);
    expect(has('legacy-console')).toBe(false);
    expect(useAuthStore.getState().managementKey).toBe(key);
    await act(async () => renderer.unmount());
    read.mockImplementation(() => Promise.resolve(Response.json(operation)));
    await mount();
    expect(has('legacy-console')).toBe(true);
  });
  it('does not send credentials remembered for a different deployment prefix', async () => {
    useAuthStore.setState({
      rememberPassword: true,
      managementKey: key,
      apiBase: 'https://manager.example/foreign/api/v2/cpamp',
      sessionPanelBase: 'https://manager.example/foreign/api/v2/cpamp',
      sessionMode: 'manager_embedded',
    });
    await mount();
    expect(hasLogin()).toBe(true);
    expect(read).not.toHaveBeenCalled();
  });
  it('can sign in and revoke in-memory authority when browser storage is full', async () => {
    await mount();
    const original = localStorage.setItem;
    localStorage.setItem = () => {
      throw new DOMException('Storage full', 'QuotaExceededError');
    };
    try {
      await login(true);
      expect(has('legacy-console')).toBe(true);
      expect(useAuthStore.getState().isAuthenticated).toBe(true);
      expect(useAuthStore.getState().rememberPassword).toBe(false);
      await act(async () => renderer.root.findByType(LegacyConsolePage).props.onUnauthorized());
      expect(hasLogin()).toBe(true);
      expect(has('legacy-console')).toBe(false);
      expect(useAuthStore.getState().managementKey).toBe('');
      expect(useAuthStore.getState().isAuthenticated).toBe(false);
    } finally {
      localStorage.setItem = original;
    }
  });
  it('continues incomplete Fresh in its guided journey without mounting management pages', async () => {
    read.mockImplementation(() =>
      Promise.resolve(Response.json({ ...operation, productComplete: false }))
    );
    await mount();
    await login();
    expect(renderer.root.findByType(FreshManagedRuntime).props.guided).toBe(true);
    expect(has('legacy-console')).toBe(false);
    expect(read.mock.calls.every(([, init]) => init?.method === 'GET')).toBe(true);
  });
  it('requires confirmed completion and enters the original Console from the wizard', async () => {
    read.mockImplementation(() =>
      Promise.resolve(Response.json({ ...operation, productComplete: false }))
    );
    await mount();
    await login();
    // A view callback is not authority to admit the business Console.
    // Keep the non-DOM router harness while observing its browser navigation.
    const history = window.history;
    Object.defineProperty(window, 'history', {
      value: { ...history, replaceState: vi.fn() },
      configurable: true,
    });
    await act(async () => renderer.root.findByType(FreshManagedRuntime).props.onContinue());
    Object.defineProperty(window, 'history', { value: history, configurable: true });
    expect(has('legacy-console')).toBe(false);
    expect(renderer.root.findByType(FreshManagedRuntime)).toBeTruthy();
    await act(async () =>
      renderer.root.findByType(FreshManagedRuntime).props.onOperation(operation)
    );
    expect(has('legacy-console')).toBe(true);
  });
  it('opens External from the existing journey and can return without selecting a strategy', async () => {
    read.mockResolvedValue(Response.json({ ...operation, productComplete: false }));
    await mount();
    await login();
    await act(async () => renderer.root.findByType(FreshManagedRuntime).props.onSelectExternal());
    expect(renderer.root.findByType(FreshExternalConnection)).toBeTruthy();
    expect(has('legacy-console')).toBe(false);
    await act(async () => renderer.root.findByType(FreshExternalConnection).props.onBack());
    expect(renderer.root.findByType(FreshManagedRuntime)).toBeTruthy();
    expect(read.mock.calls.every(([, init]) => init?.method === 'GET')).toBe(true);
  });

  it('restores External setup and enters the original Console only after completion', async () => {
    const external = {
      ...operation,
      productComplete: false,
      strategy: {
        strategy: 'connect_external',
        stage: 'committed',
        targetGeneration: 1,
        targetIdentity: 'external:sha256:' + 'a'.repeat(64),
      },
    };
    read.mockImplementation(() => Promise.resolve(Response.json(external)));
    await mount();
    await login();
    expect(renderer.root.findByType(FreshExternalConnection)).toBeTruthy();
    expect(has('legacy-console')).toBe(false);
    await act(async () =>
      renderer.root
        .findByType(FreshExternalConnection)
        .props.onOperation({ ...external, productComplete: true, revision: 7 })
    );
    expect(has('legacy-console')).toBe(false);
    const history = window.history;
    Object.defineProperty(window, 'history', {
      value: { ...history, replaceState: vi.fn() },
      configurable: true,
    });
    await act(async () => {
      renderer.root.findByType(FreshExternalConnection).props.onContinue();
      Object.defineProperty(window, 'history', { value: history, configurable: true });
    });
    expect(has('legacy-console')).toBe(true);
  });
  it.each([401, 403, 409, 503])('refuses authentication on HTTP %s', async (status) => {
    read.mockImplementation(() => Promise.resolve(new Response('', { status })));
    await mount();
    await login();
    expect(has('legacy-console')).toBe(false);
  });
  it('retranslates a console sign-in error after the language changes', async () => {
    // The console switches language through its own store, which is what keeps
    // i18n, the Fresh shell and the persisted preference in step.
    await act(async () => {
      useLanguageStore.getState().setLanguage('zh-CN');
    });
    read.mockImplementation(() => Promise.resolve(new Response('', { status: 401 })));
    await mount();
    expect(hasLogin()).toBe(true);
    await login();
    // The user reads the alert region, not the adapter object behind it.
    const alertText = () => {
      const children: unknown = renderer.root.findByProps({ id: 'console-login-error' }).props
        .children;
      if (typeof children !== 'string') throw new Error('sign-in alert must render one string');
      return children;
    };
    const chineseAlert = alertText();
    expect(chineseAlert.length).toBeGreaterThan(0);
    expect(chineseAlert).not.toMatch(/fullweb\./);

    await act(async () => {
      useLanguageStore.getState().setLanguage('en');
      await vi.dynamicImportSettled();
    });
    const englishAlert = alertText();
    // The visible alert follows the language instead of keeping the old sentence.
    expect(englishAlert).not.toBe(chineseAlert);
    expect(englishAlert).not.toContain(chineseAlert);
    expect(englishAlert).not.toMatch(/fullweb\./);

    await act(async () => {
      useLanguageStore.getState().setLanguage('zh-CN');
      await vi.dynamicImportSettled();
    });
    expect(alertText()).toBe(chineseAlert);
  });

  it('keeps the console tab title and <html lang> on the language the console renders', async () => {
    const page = () =>
      globalThis.document as unknown as { title: string; documentElement: { lang: string } };

    await mount();
    await login();
    expect(has('legacy-console')).toBe(true);
    const englishTitle = page().title;
    expect(englishTitle.length).toBeGreaterThan(0);
    expect(englishTitle).not.toBe('consoleTitle');

    await act(async () => {
      useLanguageStore.getState().setLanguage('ru');
      await vi.dynamicImportSettled();
    });
    await vi.waitFor(() => expect(page().title).not.toBe(englishTitle));
    const russianTitle = page().title;
    expect(russianTitle.length).toBeGreaterThan(0);
    expect(russianTitle).not.toBe('consoleTitle');
    // The Console bridge carried the choice into the Fresh instance, so the tab
    // title and the document language agree.
    expect(fullWebLanguage()).toBe('ru');
    expect(page().documentElement.lang).toBe(fullWebLanguage());

    await act(async () => {
      useLanguageStore.getState().setLanguage('en');
      await vi.dynamicImportSettled();
    });
    await vi.waitFor(() => expect(page().title).toBe(englishTitle));
    expect(fullWebLanguage()).toBe('en');
    expect(page().documentElement.lang).toBe(fullWebLanguage());
  });

  it.each([
    { adminInitialized: false },
    { revision: 0 },
    { identity: { ...operation.identity, installationId: '' } },
    { routing: { ...operation.routing, target: '/foreign' } },
    { routing: { ...operation.routing, revision: 2 } },
    { routing: { ...operation.routing, committed: false } },
    { routing: { ...operation.routing, operationId: 'foreign' } },
  ])('refuses malformed or mismatched operation %j', async (patch) => {
    read.mockImplementation(() => Promise.resolve(Response.json({ ...operation, ...patch })));
    await mount();
    await login();
    expect(has('legacy-console')).toBe(false);
  });
  it('keeps the legacy CPAMP pages mounted across real routes', async () => {
    await mount();
    await login();
    await navigate('/ai-providers');
    expect(has('legacy-console')).toBe(true);
    expect(
      renderer.root
        .findAllByType('a')
        .some(
          (link) =>
            link.props.href === '/console/ai-providers' &&
            String(link.props.className).includes('active')
        )
    ).toBe(true);
    await navigate('/system');
    expect(has('legacy-console')).toBe(true);
    expect(
      renderer.root
        .findAllByType('a')
        .some(
          (link) =>
            link.props.href === '/console/system' && String(link.props.className).includes('active')
        )
    ).toBe(true);
  });
  it('retires rejected authority from an already mounted editor', async () => {
    await mount();
    await login();
    await act(async () => renderer.root.findByType(LegacyConsolePage).props.onUnauthorized());
    expect(has('legacy-console')).toBe(false);
    expect(input().props.value).toBe('');
  });
  it('propagates non-writable External logs capability through FreshConsolePage into LogsPage without passing authorityClient', async () => {
    useConfigStore.getState().clearCache();
    vi.spyOn(configApi, 'getConfig').mockResolvedValue({ loggingToFile: false });
    const capabilityRequests: string[] = [];
    await mount(async (url) => {
      capabilityRequests.push(String(url));
      return Response.json({
        capability: {
          mode: 'external',
          readable: true,
          writable: false,
          rulesWritable: false,
          reason: 'external_cpa_version_unsupported',
        },
      });
    });
    await login();
    await navigate('/logs');
    await act(async () => {
      await vi.dynamicImportSettled();
    });
    await vi.waitFor(() => {
      expect(capabilityRequests.some((url) => url.endsWith('/gateway/config/logs-capability'))).toBe(
        true
      );
      expect(find('logs-empty-enable-button').props.disabled).toBe(true);
      expect(has('logs-empty-authority-notice')).toBe(true);
    });
  });
  it('follows the Gateway observability.logs.* ownership link into LogsPage and writes a credited field', async () => {
    useConfigStore.getState().clearCache();
    vi.spyOn(configApi, 'getConfig').mockResolvedValue({
      loggingToFile: false,
      debug: false,
      requestLog: false,
      logsMaxTotalSizeMb: 0,
      errorLogsMaxFiles: 10,
    });
    const updateDebugSpy = vi.spyOn(configApi, 'updateDebug').mockResolvedValue({ ok: true });
    await mount(async (url) => {
      if (String(url).endsWith('/gateway/config/logs-capability')) {
        return Response.json({
          capability: {
            mode: 'managed',
            readable: true,
            writable: true,
            rulesWritable: true,
          },
        });
      }
      return Response.json(gatewayViewFixture());
    });
    await login();
    await navigate('/config');
    await act(async () => {
      await vi.dynamicImportSettled();
    });
    await act(async () => {
      find('gateway-tab-owners').props.onClick();
    });
    const ownerLink = find('gateway-owner-link-/logs').findByType('a');
    expect(ownerLink.props.href).toBe('/console/logs');
    await act(async () => {
      ownerLink.props.onClick({
        button: 0,
        preventDefault: vi.fn(),
        currentTarget: null,
        target: null,
        metaKey: false,
        altKey: false,
        ctrlKey: false,
        shiftKey: false,
      });
    });
    await act(async () => {
      await vi.dynamicImportSettled();
    });
    await act(async () => {
      find('logs-tab-settings').props.onClick();
    });
    await vi.waitFor(() => {
      expect(
        find('logs-setting-row-debug').findByType('input').props.disabled
      ).toBe(false);
    });
    await act(async () => {
      find('logs-setting-row-debug')
        .findByType('input')
        .props.onChange({ target: { checked: true } });
    });
    expect(updateDebugSpy).toHaveBeenCalledWith(true);
    expect(useConfigStore.getState().config?.debug).toBe(true);
  });
  it('gates an installed v2 session with missingInferenceCompletionRequired through FreshInferencePrefixCompletion before entering LegacyConsolePage', async () => {
    const incompleteInferenceOp: FreshOperation = {
      ...operation,
      productComplete: true,
      routing: {
        ...operation.routing,
        inferenceTarget: '',
        sharedEntry: false,
        inferenceCommitted: false,
        missingInferenceCompletionRequired: true,
        suggestedInferenceBasePath: '/inf1234567890abcd',
      },
    };
    let currentOp = incompleteInferenceOp;
    read.mockImplementation(async (url, init) => {
      const target = String(url);
      if (target.endsWith('/api/v2/operation')) {
        return Response.json(currentOp);
      }
      if (target.endsWith('/api/v2/installation/access/completion')) {
        if (init?.method === 'POST') {
          const payload = JSON.parse(String(init.body));
          currentOp = {
            ...incompleteInferenceOp,
            revision: incompleteInferenceOp.revision + 1,
            routing: {
              ...incompleteInferenceOp.routing,
              inferenceTarget: payload.inferenceBasePath,
              sharedEntry: Boolean(payload.sharedEntry),
              inferenceCommitted: true,
              missingInferenceCompletionRequired: false,
            },
          };
          return Response.json({
            required: true,
            mode: 'new_managed',
            controlBasePath: '/console',
            inferenceBasePath: payload.inferenceBasePath,
            sharedEntry: Boolean(payload.sharedEntry),
            inferenceCommitted: true,
          });
        }
        return Response.json({
          required: true,
          mode: 'new_managed',
          controlBasePath: '/console',
          inferenceBasePath: '',
          sharedEntry: false,
          inferenceCommitted: false,
          suggestedInferenceBasePath: '/inf1234567890abcd',
        });
      }
      if (target.endsWith('/api/v2/api-keys/access-info')) {
        return Response.json({
          mode: 'new_managed',
          managementBasePath: '/console',
          inferenceBasePath: currentOp.routing.inferenceTarget || '/inf1234567890abcd',
          protocols: [
            {
              id: 'openai',
              label: 'OpenAI',
              basePath: '/inf1234567890abcd/v1',
              examplePath: '/inf1234567890abcd/v1/chat/completions',
            },
          ],
          ready: true,
          code: '',
        });
      }
      return Response.json(currentOp);
    });

    await mount();
    await login();

    expect(has('legacy-console')).toBe(false);
    expect(renderer.root.findByType(FreshInferencePrefixCompletion)).toBeTruthy();
    await vi.waitFor(() => {
      expect(renderer.root.findByProps({ 'data-testid': 'inference-completion-submit' })).toBeTruthy();
    });

    await act(async () => {
      await renderer.root
        .findByProps({ 'data-testid': 'inference-completion-submit' })
        .props.onClick();
    });

    // Stays on the completion summary so the user can review AccessInfoSummary before entering.
    expect(has('legacy-console')).toBe(false);
    expect(
      renderer.root.findByProps({ 'data-testid': 'inference-completion-enter-console' })
    ).toBeTruthy();
    expect(renderer.root.findByProps({ 'data-testid': 'access-info-summary' })).toBeTruthy();

    const history = window.history;
    Object.defineProperty(window, 'history', {
      value: { ...history, replaceState: vi.fn() },
      configurable: true,
    });
    await act(async () => {
      renderer.root
        .findByProps({ 'data-testid': 'inference-completion-enter-console' })
        .props.onClick();
      Object.defineProperty(window, 'history', { value: history, configurable: true });
    });
    expect(has('legacy-console')).toBe(true);
  });
  it('accepts Upgrade canonical operation lacking Fresh-only admissionId/databasePath while rejecting partial identity', async () => {
    const upgradeOp: FreshOperation = {
      version: 1,
      identity: {
        admissionId: '',
        installationId: 'upgrade-installation-1',
        generation: 'upgrade-generation-1',
        databasePath: '',
        operationId: 'upgrade:sha256:1234',
      },
      revision: 3,
      adminInitialized: true,
      productComplete: true,
      gatewayPending: false,
      routing: {
        operationId: 'upgrade:sha256:1234',
        target: '/console',
        revision: 1,
        stage: 'committed',
        committed: true,
      },
      strategy: {
        strategy: 'migrate_legacy',
        stage: 'committed',
      },
    };
    read.mockImplementation(() => Promise.resolve(Response.json(upgradeOp)));
    await mount();
    await login();
    expect(has('legacy-console')).toBe(true);

    await act(async () => renderer.unmount());
    const omittedFreshFieldsOp: FreshOperation = {
      ...upgradeOp,
      identity: {
        installationId: upgradeOp.identity.installationId,
        generation: upgradeOp.identity.generation,
        operationId: upgradeOp.identity.operationId,
      } as FreshOperation['identity'],
    };
    read.mockImplementation(() => Promise.resolve(Response.json(omittedFreshFieldsOp)));
    await mount();
    await login();
    expect(has('legacy-console')).toBe(true);

    await act(async () => renderer.unmount());
    const halfFreshOp: FreshOperation = {
      ...upgradeOp,
      identity: {
        ...upgradeOp.identity,
        admissionId: 'stray-admission',
        databasePath: '',
      },
    };
    read.mockImplementation(() => Promise.resolve(Response.json(halfFreshOp)));
    await mount();
    await login();
    expect(has('legacy-console')).toBe(false);
  });
  it('allows verified monotonic routing revision after one-time completion (including re-login against older HTML) and blocks owner drift or regression', async () => {
    const upgradeIncomplete: FreshOperation = {
      version: 1,
      identity: {
        admissionId: '',
        installationId: 'upgrade-installation-2',
        generation: 'upgrade-generation-2',
        databasePath: '',
        operationId: 'upgrade:sha256:5678',
      },
      revision: 5,
      adminInitialized: true,
      productComplete: true,
      gatewayPending: false,
      routing: {
        operationId: 'upgrade:sha256:5678',
        target: '/console',
        revision: 1,
        inferenceTarget: '',
        sharedEntry: false,
        inferenceCommitted: false,
        missingInferenceCompletionRequired: true,
        suggestedInferenceBasePath: '/inf1234567890abcd',
        stage: 'committed',
        committed: true,
      },
      strategy: {
        strategy: 'migrate_legacy',
        stage: 'committed',
      },
    };
    let currentOp = upgradeIncomplete;
    read.mockImplementation(async (url, init) => {
      const target = String(url);
      if (target.endsWith('/api/v2/operation')) return Response.json(currentOp);
      if (target.endsWith('/api/v2/installation/access/completion')) {
        if (init?.method === 'POST') {
          const payload = JSON.parse(String(init.body));
          currentOp = {
            ...upgradeIncomplete,
            revision: upgradeIncomplete.revision + 1,
            routing: {
              ...upgradeIncomplete.routing,
              revision: 2,
              inferenceTarget: payload.inferenceBasePath,
              sharedEntry: Boolean(payload.sharedEntry),
              inferenceCommitted: true,
              missingInferenceCompletionRequired: false,
              suggestedInferenceBasePath: '',
            },
          };
          return Response.json({
            required: true,
            mode: 'migrate_legacy',
            controlBasePath: '/console',
            inferenceBasePath: payload.inferenceBasePath,
            sharedEntry: Boolean(payload.sharedEntry),
            inferenceCommitted: true,
          });
        }
        return Response.json({
          required: true,
          mode: 'migrate_legacy',
          controlBasePath: '/console',
          inferenceBasePath: '',
          sharedEntry: false,
          inferenceCommitted: false,
          suggestedInferenceBasePath: '/inf1234567890abcd',
        });
      }
      if (target.endsWith('/api/v2/api-keys/access-info')) {
        return Response.json({
          mode: 'migrate_legacy',
          managementBasePath: '/console',
          inferenceBasePath: currentOp.routing.inferenceTarget || '/inf1234567890abcd',
          protocols: [],
          ready: true,
          code: '',
        });
      }
      return Response.json(currentOp);
    });

    await mount();
    await login();
    await vi.waitFor(() => {
      expect(renderer.root.findByProps({ 'data-testid': 'inference-completion-submit' })).toBeTruthy();
    });
    await act(async () => {
      await renderer.root
        .findByProps({ 'data-testid': 'inference-completion-submit' })
        .props.onClick();
    });
    expect(renderer.root.findByProps({ 'data-testid': 'inference-completion-enter-console' })).toBeTruthy();

    // Foreign owner callback cannot mutate the accepted Console operation.
    await act(async () => {
      const rejected = renderer.root
        .findByType(FreshInferencePrefixCompletion)
        .props.onOperation({
          ...currentOp,
          identity: { ...currentOp.identity, installationId: 'foreign-installation' },
        });
      expect(rejected).toBe(false);
    });

    const history = window.history;
    Object.defineProperty(window, 'history', {
      value: { ...history, replaceState: vi.fn() },
      configurable: true,
    });
    await act(async () => {
      renderer.root
        .findByProps({ 'data-testid': 'inference-completion-enter-console' })
        .props.onClick();
      Object.defineProperty(window, 'history', { value: history, configurable: true });
    });
    expect(has('legacy-console')).toBe(true);

    // Re-logging in against older HTML context.revision === 1 succeeds when backend routing.revision === 2 after completion.
    await act(async () => logout().props.onClick());
    expect(hasLogin()).toBe(true);
    await login();
    expect(has('legacy-console')).toBe(true);

    // Regressive routing revision below HTML context.revision is rejected even after completion.
    await act(async () => renderer.unmount());
    read.mockImplementation(async () => Response.json(currentOp));
    await mount(undefined, { ...context, revision: 3 });
    await login();
    expect(has('legacy-console')).toBe(false);
  });

  it('normalizes /ui preview entry to console root and preserves /api-keys deep link across unremembered reload and re-authentication without plaintext storage', async () => {
    location.href = 'https://manager.example/console/ui';
    const replaceState = vi.fn(
      (state: unknown, _title: string, path?: string | URL | null) => {
        historyState = state;
        if (typeof path === 'string' && path.length > 0) {
          location.href = new URL(path, location).href;
        } else if (path instanceof URL) {
          location.href = path.href;
        }
      }
    );
    Object.defineProperty(window.history, 'replaceState', {
      value: replaceState,
      configurable: true,
    });

    await mount();
    await login(false);
    expect(replaceState).toHaveBeenCalledWith(null, '', '/console');
    expect(location.pathname).toBe('/console');
    expect(has('legacy-console')).toBe(true);
    expect(has('fresh-route-keys')).toBe(false);
    expect(has('fresh-managed-keys')).toBe(false);
    expect(
      renderer.root
        .findAllByType('a')
        .some(
          (link) =>
            link.props.href === '/console' && String(link.props.className).includes('active')
        )
    ).toBe(true);
    expect(
      persist.mock.calls.some(([, value]) => typeof value === 'string' && value.includes(key))
    ).toBe(false);

    await navigate('/api-keys');
    expect(location.pathname).toBe('/console/api-keys');
    expect(find('fresh-route-keys').props.hidden).toBe(false);
    expect(has('fresh-managed-keys')).toBe(true);

    // Simulate browser reload on /console/api-keys with remember=false.
    await act(async () => renderer.unmount());
    useAuthStore.setState({ isAuthenticated: false, managementKey: '' });
    await useAuthStore.persist.rehydrate();
    read.mockClear();
    await mount();
    expect(hasLogin()).toBe(true);
    expect(has('legacy-console')).toBe(false);
    expect(read).not.toHaveBeenCalled();
    expect(
      Array.from(storage.values()).some((value) => value.includes(key))
    ).toBe(false);
    expect(
      obfuscatedStorage.getItem<{ state: { managementKey?: string } }>(STORAGE_KEY_AUTH)?.state
        .managementKey
    ).toBeUndefined();

    await login(false);
    expect(has('legacy-console')).toBe(true);
    expect(location.pathname).toBe('/console/api-keys');
    expect(find('fresh-route-keys').props.hidden).toBe(false);
    expect(has('fresh-managed-keys')).toBe(true);
    expect(
      persist.mock.calls.some(([, value]) => typeof value === 'string' && value.includes(key))
    ).toBe(false);

    // Also verify inRouter mounting context normalizes /ui and reaches /api-keys.
    await act(async () => renderer.unmount());
    location.href = 'https://manager.example/console/ui';
    await act(async () => {
      renderer = create(
        <MemoryRouter basename="/console" initialEntries={['/console/ui']}>
          <LegacyConsolePage
            context={context}
            credential={key}
            client={createPublicApiClient(
              () => context,
              async (url, init) => {
                if (String(url).endsWith('/api-keys')) {
                  return Response.json({
                    keys: [],
                    gatewayPending: true,
                    revocationAvailable: false,
                  });
                }
                return read(url, init);
              }
            )}
            onUnauthorized={vi.fn()}
          />
        </MemoryRouter>
      );
    });
    expect(location.pathname).toBe('/console');
    expect(has('legacy-console')).toBe(true);
    expect(has('fresh-route-keys')).toBe(false);
    await navigate('/api-keys');
    expect(find('fresh-route-keys').props.hidden).toBe(false);
    expect(has('fresh-managed-keys')).toBe(true);
  });

  it('preserves /api-keys deep link when one-time inference completion continues into the console', async () => {
    location.href = 'https://manager.example/console/api-keys';
    const replaceState = vi.fn(
      (state: unknown, _title: string, path?: string | URL | null) => {
        historyState = state;
        if (typeof path === 'string' && path.length > 0) {
          location.href = new URL(path, location).href;
        } else if (path instanceof URL) {
          location.href = path.href;
        }
      }
    );
    Object.defineProperty(window.history, 'replaceState', {
      value: replaceState,
      configurable: true,
    });
    const upgradeIncomplete: FreshOperation = {
      ...operation,
      productComplete: true,
      gatewayPending: true,
      routing: {
        ...operation.routing,
        revision: 1,
        inferenceCommitted: false,
        missingInferenceCompletionRequired: true,
        suggestedInferenceBasePath: '/inf1234567890abcd',
      },
    };
    let currentOp = upgradeIncomplete;
    read.mockImplementation(async (url, init) => {
      const target = String(url);
      if (target.endsWith('/api/v2/operation')) return Response.json(currentOp);
      if (target.endsWith('/api/v2/installation/access/completion')) {
        if (init?.method === 'POST') {
          const payload = JSON.parse(String(init.body));
          currentOp = {
            ...upgradeIncomplete,
            revision: upgradeIncomplete.revision + 1,
            routing: {
              ...upgradeIncomplete.routing,
              revision: 2,
              inferenceTarget: payload.inferenceBasePath,
              sharedEntry: Boolean(payload.sharedEntry),
              inferenceCommitted: true,
              missingInferenceCompletionRequired: false,
              suggestedInferenceBasePath: '',
            },
          };
          return Response.json({
            required: true,
            mode: 'migrate_legacy',
            controlBasePath: '/console',
            inferenceBasePath: payload.inferenceBasePath,
            sharedEntry: Boolean(payload.sharedEntry),
            inferenceCommitted: true,
          });
        }
        return Response.json({
          required: true,
          mode: 'migrate_legacy',
          controlBasePath: '/console',
          inferenceBasePath: '',
          sharedEntry: false,
          inferenceCommitted: false,
          suggestedInferenceBasePath: '/inf1234567890abcd',
        });
      }
      if (target.endsWith('/api/v2/api-keys/access-info')) {
        return Response.json({
          mode: 'migrate_legacy',
          managementBasePath: '/console',
          inferenceBasePath: currentOp.routing.inferenceTarget || '/inf1234567890abcd',
          protocols: [],
          ready: true,
          code: '',
        });
      }
      return Response.json(currentOp);
    });

    await mount();
    await login(false);
    await vi.waitFor(() => {
      expect(renderer.root.findByProps({ 'data-testid': 'inference-completion-submit' })).toBeTruthy();
    });
    await act(async () => {
      await renderer.root
        .findByProps({ 'data-testid': 'inference-completion-submit' })
        .props.onClick();
    });
    await act(async () => {
      renderer.root
        .findByProps({ 'data-testid': 'inference-completion-enter-console' })
        .props.onClick();
    });
    await act(async () => {
      await vi.dynamicImportSettled();
    });

    expect(replaceState).toHaveBeenCalledWith(null, '', '/console/api-keys');
    expect(location.pathname).toBe('/console/api-keys');
    expect(has('legacy-console')).toBe(true);
    expect(find('fresh-route-keys').props.hidden).toBe(false);
    expect(has('fresh-managed-keys')).toBe(true);
  });
});
