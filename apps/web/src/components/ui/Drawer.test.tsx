import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string) => key,
  }),
}));

vi.mock('react-dom', async () => {
  const actual = await vi.importActual<typeof import('react-dom')>('react-dom');
  return {
    ...actual,
    createPortal: (children: unknown) => children,
  };
});

vi.mock('./Drawer.module.scss', () => ({
  default: {
    overlay: 'overlay',
    overlayClosing: 'overlayClosing',
    overlayEntering: 'overlayEntering',
    panel: 'panel',
    panelClosing: 'panelClosing',
    panelEntering: 'panelEntering',
    header: 'header',
    title: 'title',
    closeButton: 'closeButton',
    body: 'body',
    footer: 'footer',
  },
}));

vi.mock('./icons', () => ({
  IconX: () => null,
}));

import { Drawer } from './Drawer';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const CLOSE_ANIMATION_DURATION = 280;

const createPointerEvent = (
  target: unknown,
  currentTarget: unknown,
  options: { button?: number; pointerId?: number } = {}
) => ({
  target,
  currentTarget,
  button: options.button ?? 0,
  pointerId: options.pointerId ?? 1,
});

const installMinimalDom = () => {
  const previousHTMLElement = Object.getOwnPropertyDescriptor(globalThis, 'HTMLElement');
  const previousDocument = Object.getOwnPropertyDescriptor(globalThis, 'document');
  const previousWindow = Object.getOwnPropertyDescriptor(globalThis, 'window');
  const installedWindow = typeof globalThis.window === 'undefined';

  const bodyStyle = { overflow: '' };
  const htmlStyle = { overflow: '' };
  const listeners = new Map<string, Set<EventListenerOrEventListenerObject>>();

  class HTMLElementMock {}

  const documentMock = {
    body: { style: bodyStyle },
    documentElement: { style: htmlStyle },
    activeElement: null as unknown,
    addEventListener: (type: string, listener: EventListenerOrEventListenerObject) => {
      const set = listeners.get(type) ?? new Set();
      set.add(listener);
      listeners.set(type, set);
    },
    removeEventListener: (type: string, listener: EventListenerOrEventListenerObject) => {
      listeners.get(type)?.delete(listener);
    },
  };

  Object.defineProperty(globalThis, 'HTMLElement', {
    configurable: true,
    writable: true,
    value: HTMLElementMock,
  });

  Object.defineProperty(globalThis, 'document', {
    configurable: true,
    writable: true,
    value: documentMock,
  });

  if (installedWindow) {
    Object.defineProperty(globalThis, 'window', {
      configurable: true,
      writable: true,
      value: globalThis,
    });
  }

  return () => {
    if (previousHTMLElement) {
      Object.defineProperty(globalThis, 'HTMLElement', previousHTMLElement);
    } else {
      Reflect.deleteProperty(globalThis, 'HTMLElement');
    }

    if (previousDocument) {
      Object.defineProperty(globalThis, 'document', previousDocument);
    } else {
      Reflect.deleteProperty(globalThis, 'document');
    }

    if (installedWindow) {
      if (previousWindow) {
        Object.defineProperty(globalThis, 'window', previousWindow);
      } else {
        Reflect.deleteProperty(globalThis, 'window');
      }
    }
  };
};

describe('Drawer overlay close guard', () => {
  let renderer: ReactTestRenderer | undefined;
  let restoreDom: (() => void) | undefined;

  beforeEach(() => {
    vi.useFakeTimers();
    restoreDom = installMinimalDom();
  });

  afterEach(() => {
    act(() => {
      renderer?.unmount();
    });
    renderer = undefined;
    restoreDom?.();
    restoreDom = undefined;
    vi.useRealTimers();
  });

  const mountDrawer = async (onClose: () => void) => {
    await act(async () => {
      renderer = create(
        <Drawer open title="Test drawer" onClose={onClose}>
          <input aria-label="field" />
        </Drawer>
      );
    });

    // open effect 通过 queueMicrotask 切换可见态
    await act(async () => {
      await Promise.resolve();
    });

    return renderer!;
  };

  const findOverlay = (root: ReactTestRenderer) =>
    root.root.find((node) => String(node.props.className ?? '').includes('overlay'));

  it('closes when pointer starts and ends on overlay', async () => {
    const onClose = vi.fn();
    const mounted = await mountDrawer(onClose);
    const overlay = findOverlay(mounted);

    await act(async () => {
      overlay.props.onPointerDown(createPointerEvent(overlay, overlay, { pointerId: 1 }));
      overlay.props.onPointerUp(createPointerEvent(overlay, overlay, { pointerId: 1 }));
    });

    expect(onClose).not.toHaveBeenCalled();

    await act(async () => {
      vi.advanceTimersByTime(CLOSE_ANIMATION_DURATION);
    });

    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('does not close when pointer starts inside drawer and ends on overlay', async () => {
    const onClose = vi.fn();
    const mounted = await mountDrawer(onClose);
    const overlay = findOverlay(mounted);
    const panel = mounted.root.findByProps({ role: 'dialog' });

    await act(async () => {
      // 模拟：在面板内按下，在遮罩上释放（拖选文字场景）
      overlay.props.onPointerDown(createPointerEvent(panel, overlay, { pointerId: 1 }));
      overlay.props.onPointerUp(createPointerEvent(overlay, overlay, { pointerId: 1 }));
    });

    await act(async () => {
      vi.advanceTimersByTime(CLOSE_ANIMATION_DURATION);
    });

    expect(onClose).not.toHaveBeenCalled();
  });

  it('does not close when pointer starts on overlay and ends inside drawer', async () => {
    const onClose = vi.fn();
    const mounted = await mountDrawer(onClose);
    const overlay = findOverlay(mounted);
    const panel = mounted.root.findByProps({ role: 'dialog' });

    await act(async () => {
      overlay.props.onPointerDown(createPointerEvent(overlay, overlay, { pointerId: 1 }));
      overlay.props.onPointerUp(createPointerEvent(panel, overlay, { pointerId: 1 }));
    });

    await act(async () => {
      vi.advanceTimersByTime(CLOSE_ANIMATION_DURATION);
    });

    expect(onClose).not.toHaveBeenCalled();
  });

  it('does not close when interleaved pointers mix panel and overlay starts', async () => {
    const onClose = vi.fn();
    const mounted = await mountDrawer(onClose);
    const overlay = findOverlay(mounted);
    const panel = mounted.root.findByProps({ role: 'dialog' });

    await act(async () => {
      // 指针 1：面板内按下；指针 2：遮罩上按下；释放指针 1 在遮罩上 → 不应关闭
      overlay.props.onPointerDown(createPointerEvent(panel, overlay, { pointerId: 1 }));
      overlay.props.onPointerDown(createPointerEvent(overlay, overlay, { pointerId: 2 }));
      overlay.props.onPointerUp(createPointerEvent(overlay, overlay, { pointerId: 1 }));
    });

    await act(async () => {
      vi.advanceTimersByTime(CLOSE_ANIMATION_DURATION);
    });

    expect(onClose).not.toHaveBeenCalled();
  });
});
