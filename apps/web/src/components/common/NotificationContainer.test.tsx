import i18next from 'i18next';
import type { i18n as I18nInstance } from 'i18next';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { I18nextProvider } from 'react-i18next';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { Notification } from '@/types';
import en from '@/i18n/locales/en.json';
import ru from '@/i18n/locales/ru.json';
import zhCN from '@/i18n/locales/zh-CN.json';

const { mocks } = vi.hoisted(() => ({
  mocks: {
    notifications: [] as Notification[],
    removeNotification: vi.fn(),
    portalTarget: null as unknown,
  },
}));

vi.mock('react-dom', async () => {
  const actual = await vi.importActual<typeof import('react-dom')>('react-dom');
  return {
    ...actual,
    createPortal: (children: unknown, target: unknown) => {
      mocks.portalTarget = target;
      return children;
    },
  };
});

vi.mock('@/stores', () => ({
  useNotificationStore: (
    selector?: (state: {
      notifications: Notification[];
      removeNotification: typeof mocks.removeNotification;
    }) => unknown
  ) => {
    const state = {
      notifications: mocks.notifications,
      removeNotification: mocks.removeNotification,
    };
    return selector ? selector(state) : state;
  },
}));

vi.mock('@/components/ui/icons', () => ({
  IconX: () => null,
}));

import { NotificationContainer } from './NotificationContainer';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const createI18n = (lng: string): I18nInstance => {
  const instance = i18next.createInstance();
  instance.init({
    resources: { en: { translation: en }, ru: { translation: ru }, 'zh-CN': { translation: zhCN } },
    lng,
    fallbackLng: 'zh-CN',
    initAsync: false,
    interpolation: { escapeValue: false },
  });
  return instance;
};

describe('NotificationContainer', () => {
  let renderer: ReactTestRenderer | undefined;
  let body: Record<string, unknown>;
  let restoreDocument: (() => void) | undefined;

  const render = async (i18n: I18nInstance) => {
    await act(async () => {
      renderer = create(
        <I18nextProvider i18n={i18n}>
          <NotificationContainer />
        </I18nextProvider>
      );
      await Promise.resolve();
    });
  };

  const messageText = () => {
    const node = renderer!.root.find((candidate) => candidate.props.className === 'message');
    const children: unknown = node.props.children;
    if (typeof children !== 'string') throw new Error('toast message must render as one string');
    return children;
  };

  beforeEach(() => {
    vi.useFakeTimers();
    mocks.notifications = [];
    mocks.removeNotification.mockReset();
    mocks.portalTarget = null;

    const previousDocument = Object.getOwnPropertyDescriptor(globalThis, 'document');
    body = {};
    Object.defineProperty(globalThis, 'document', {
      configurable: true,
      value: { body },
    });
    restoreDocument = () => {
      if (previousDocument) {
        Object.defineProperty(globalThis, 'document', previousDocument);
      } else {
        Reflect.deleteProperty(globalThis, 'document');
      }
    };
  });

  afterEach(() => {
    act(() => renderer?.unmount());
    renderer = undefined;
    restoreDocument?.();
    restoreDocument = undefined;
    vi.useRealTimers();
  });

  it('portals notifications to document.body without changing their content or type', async () => {
    mocks.notifications = [
      {
        id: 'notification-1',
        message: '401 Unauthorized',
        type: 'error',
      },
    ];

    await render(createI18n('zh-CN'));

    expect(mocks.portalTarget).toBe(body);
    expect(messageText()).toBe('401 Unauthorized');
    expect(
      renderer!.root.find(
        (node) =>
          typeof node.props.className === 'string' &&
          node.props.className.includes('notification error')
      )
    ).toBeTruthy();
  });

  it('retranslates a toast that carries a renderer when the language changes', async () => {
    const i18n = createI18n('zh-CN');
    mocks.notifications = [
      {
        id: 'notification-1',
        message: i18n.t('notification.login_failed'),
        type: 'error',
        renderMessage: (t) => t('notification.login_failed'),
      },
    ];

    await render(i18n);
    const chinese = messageText();
    expect(chinese).not.toMatch(/^notification\./);

    await act(async () => {
      await i18n.changeLanguage('ru');
    });

    const russian = messageText();
    expect(russian).not.toBe(chinese);
    expect(russian).not.toMatch(/^notification\./);

    await act(async () => {
      await i18n.changeLanguage('zh-CN');
    });

    expect(messageText()).toBe(chinese);
  });

  it('keeps a plain message unchanged across a language change', async () => {
    const i18n = createI18n('zh-CN');
    mocks.notifications = [
      {
        id: 'notification-1',
        message: 'Saved',
        type: 'success',
      },
    ];

    await render(i18n);
    expect(messageText()).toBe('Saved');

    await act(async () => {
      await i18n.changeLanguage('en');
    });

    expect(messageText()).toBe('Saved');
  });

  it('keeps close behavior wired to removeNotification after the exit animation', async () => {
    mocks.notifications = [
      {
        id: 'notification-1',
        message: 'Saved',
        type: 'success',
      },
    ];

    await render(createI18n('zh-CN'));

    await act(async () => {
      renderer!.root.findByProps({ className: 'close-btn' }).props.onClick();
      vi.advanceTimersByTime(300);
    });

    expect(mocks.removeNotification).toHaveBeenCalledWith('notification-1');
  });
});
