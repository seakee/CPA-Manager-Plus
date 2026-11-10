import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import '../../i18n';
import i18n from '../../i18n';
import { setFullWebLanguage } from '../../i18n/fullweb';
import { useAuthStore } from '../../stores/useAuthStore';
import { useLanguageStore } from '../../stores/useLanguageStore';
import { useNotificationStore } from '../../stores/useNotificationStore';
import { LoginPage } from './LoginPage';
import { NotificationContainer } from '../../components/common/NotificationContainer';

const { navigate } = vi.hoisted(() => ({ navigate: vi.fn() }));

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual<typeof import('react-router-dom')>('react-router-dom');
  return {
    ...actual,
    useNavigate: () => navigate,
    useLocation: () => ({ pathname: '/login', search: '', hash: '', state: null }),
    Navigate: () => null,
  };
});

vi.mock('react-dom', async () => {
  const actual = await vi.importActual<typeof import('react-dom')>('react-dom');
  // react-test-renderer cannot mount into a DOM node; keep the toast inside the tree.
  return { ...actual, createPortal: (children: unknown) => children };
});

vi.mock('../../services/api/usageService', async () => {
  const actual =
    await vi.importActual<typeof import('../../services/api/usageService')>(
      '../../services/api/usageService'
    );
  return {
    ...actual,
    usageServiceApi: {
      ...actual.usageServiceApi,
      getInfo: vi.fn().mockRejectedValue(new Error('usage service is not reachable')),
    },
  };
});

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const childText = (children: unknown): string => {
  if (typeof children === 'string') return children;
  if (Array.isArray(children)) return children.map(String).join('');
  return '';
};

describe('LoginPage language switches', () => {
  let renderer: ReactTestRenderer | undefined;
  let login: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    vi.stubGlobal('window', {
      location: new URL('https://cpa.example/login'),
      history: { pushState: vi.fn() },
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      matchMedia: () => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }),
      setTimeout: globalThis.setTimeout,
      clearTimeout: globalThis.clearTimeout,
    });
    vi.stubGlobal('document', {
      documentElement: {
        lang: 'zz',
        classList: { add: vi.fn(), remove: vi.fn(), toggle: vi.fn() },
        setAttribute: vi.fn(),
        style: { setProperty: vi.fn(), removeProperty: vi.fn() },
      },
      body: { style: {} },
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    });
    vi.stubGlobal('localStorage', {
      getItem: vi.fn(() => null),
      setItem: vi.fn(),
      removeItem: vi.fn(),
    });
    vi.stubGlobal('navigator', { language: 'en-US', languages: ['en-US'] });

    login = vi.fn().mockRejectedValue({ status: 401, message: 'Request failed with status 401' });
    useAuthStore.setState({
      isAuthenticated: false,
      apiBase: 'http://cpa.example',
      managementKey: '',
      login: login as never,
      restoreSession: (async () => null) as never,
    });
    useLanguageStore.setState({ language: 'en' });
    void i18n.changeLanguage('en');
    setFullWebLanguage('en');
    useNotificationStore.setState({ notifications: [] });
  });

  afterEach(async () => {
    if (renderer) await act(async () => renderer!.unmount());
    renderer = undefined;
    useNotificationStore.setState({ notifications: [] });
    useAuthStore.setState({
      isAuthenticated: false,
      login: (async () => {
        throw new Error('unused');
      }) as never,
      restoreSession: (async () => null) as never,
    });
    useLanguageStore.setState({ language: 'en' });
    await i18n.changeLanguage('en');
    setFullWebLanguage('en');
    vi.unstubAllGlobals();
  });

  it('keeps the failure alert and its toast on the same language after a switch', async () => {
    await act(async () => {
      renderer = create(
        <>
          <LoginPage />
          <NotificationContainer />
        </>
      );
      await Promise.resolve();
    });

    const form = renderer!.root.findByType('form');
    const credential = renderer!.root
      .findAllByType('input')
      .find((node) => node.props.type === 'password');
    expect(credential).toBeDefined();

    await act(async () => {
      credential!.props.onChange({ target: { value: 'wrong-admin-key' } });
      await Promise.resolve();
    });
    await act(async () => {
      form.props.onSubmit({ preventDefault: () => {} });
      await Promise.resolve();
    });

    expect(login).toHaveBeenCalled();
    await vi.waitFor(() =>
      expect(renderer!.root.findAll((node) => node.props?.role === 'alert').length).toBeGreaterThan(0)
    );

    const alertText = () =>
      childText(renderer!.root.findAll((node) => node.props?.role === 'alert')[0].props.children);
    const toastText = () =>
      childText(renderer!.root.find((node) => node.props?.className === 'message').props.children);

    const englishAlert = alertText();
    const englishToast = toastText();
    expect(englishAlert.length).toBeGreaterThan(0);
    expect(englishToast).toContain(englishAlert);
    expect(englishToast).not.toMatch(/(login|notification)\./);

    // The login page switches language from its own menu, like a user does.
    await act(async () => {
      renderer!.root.findByProps({ 'aria-haspopup': 'menu' }).props.onClick();
      await Promise.resolve();
    });
    const options = renderer!.root.findAll((node) => node.props?.role === 'menuitemradio');
    expect(options.length).toBeGreaterThan(1);
    await act(async () => {
      options[1].props.onClick();
      await vi.dynamicImportSettled();
    });

    const switchedAlert = alertText();
    const switchedToast = toastText();
    expect(switchedAlert).not.toBe(englishAlert);
    expect(switchedToast).not.toBe(englishToast);
    // A toast left on screen follows the box it repeats.
    expect(switchedToast).toContain(switchedAlert);
    expect(switchedToast).not.toContain(englishAlert);
    expect(renderer!.root.findAll((node) => node.children[0] === englishToast)).toEqual([]);
  });
});
