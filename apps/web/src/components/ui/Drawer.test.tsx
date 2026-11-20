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

  const mountDrawer = async (
    onClose: () => void,
    onBeforeClose?: () => boolean | Promise<boolean>
  ) => {
    await act(async () => {
      renderer = create(
        <Drawer open title="Test drawer" onClose={onClose} onBeforeClose={onBeforeClose}>
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

  it('waits for the close guard before starting the animation', async () => {
    const onClose = vi.fn();
    let resolveGuard!: (allowed: boolean) => void;
    const onBeforeClose = vi.fn(
      () =>
        new Promise<boolean>((resolve) => {
          resolveGuard = resolve;
        })
    );
    const mounted = await mountDrawer(onClose, onBeforeClose);
    const closeButton = mounted.root.findByProps({ 'aria-label': 'common.close' });

    await act(async () => {
      closeButton.props.onClick();
      closeButton.props.onClick();
      vi.advanceTimersByTime(CLOSE_ANIMATION_DURATION);
    });

    expect(onBeforeClose).toHaveBeenCalledTimes(1);
    expect(onClose).not.toHaveBeenCalled();

    await act(async () => {
      resolveGuard(true);
      await Promise.resolve();
    });
    expect(onClose).not.toHaveBeenCalled();

    await act(async () => {
      vi.advanceTimersByTime(CLOSE_ANIMATION_DURATION);
    });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('keeps the drawer open when the close guard rejects the request', async () => {
    const onClose = vi.fn();
    const mounted = await mountDrawer(onClose, () => false);
    const closeButton = mounted.root.findByProps({ 'aria-label': 'common.close' });

    await act(async () => {
      closeButton.props.onClick();
      await Promise.resolve();
      vi.advanceTimersByTime(CLOSE_ANIMATION_DURATION);
    });

    expect(onClose).not.toHaveBeenCalled();
    expect(findOverlay(mounted).props.className).toContain('overlayEntering');
  });

  it('does not let an old close guard close a drawer reopened for a new target', async () => {
    const onClose = vi.fn();
    let resolveGuard!: (allowed: boolean) => void;
    const onBeforeClose = vi.fn(
      () =>
        new Promise<boolean>((resolve) => {
          resolveGuard = resolve;
        })
    );

    await mountDrawer(onClose, onBeforeClose);
    const closeButton = renderer!.root.findByProps({ 'aria-label': 'common.close' });
    await act(async () => {
      closeButton.props.onClick();
      await Promise.resolve();
    });

    await act(async () => {
      renderer!.update(
        <Drawer open={false} title="Test drawer" onClose={onClose} onBeforeClose={onBeforeClose}>
          <input aria-label="field" />
        </Drawer>
      );
      await Promise.resolve();
    });

    await act(async () => {
      renderer!.update(
        <Drawer open title="Reopened drawer" onClose={onClose} onBeforeClose={onBeforeClose}>
          <input aria-label="field" />
        </Drawer>
      );
      await Promise.resolve();
    });

    await act(async () => {
      resolveGuard(true);
      await Promise.resolve();
      vi.advanceTimersByTime(CLOSE_ANIMATION_DURATION);
    });

    expect(onClose).not.toHaveBeenCalled();
    expect(findOverlay(renderer!).props.className).toContain('overlayEntering');
  });
  it('uses closeLabel and blocks button, Escape, and overlay closing when closeDisabled is true', async () => {
    const onClose = vi.fn();
    const onBeforeClose = vi.fn(() => true);
    const keydownHandlers: Array<(event: { key: string; preventDefault: () => void }) => void> = [];
    const addSpy = vi
      .spyOn(document, 'addEventListener')
      .mockImplementation((type, listener) => {
        if (type === 'keydown' && typeof listener === 'function') {
          keydownHandlers.push(
            listener as unknown as (event: { key: string; preventDefault: () => void }) => void
          );
        }
      });
    try {
      await act(async () => {
        renderer = create(
          <Drawer
            open
            title="Locked drawer"
            closeLabel="Close details"
            closeDisabled
            onClose={onClose}
            onBeforeClose={onBeforeClose}
          >
            <input aria-label="field" />
          </Drawer>
        );
        await Promise.resolve();
      });

      const closeButton = renderer!.root.findByProps({ 'aria-label': 'Close details' });
      expect(closeButton.props.title).toBe('Close details');
      expect(closeButton.props.disabled).toBe(true);

      const overlay = findOverlay(renderer!);
      await act(async () => {
        closeButton.props.onClick();
        overlay.props.onPointerDown(createPointerEvent(overlay, overlay, { pointerId: 1 }));
        overlay.props.onPointerUp(createPointerEvent(overlay, overlay, { pointerId: 1 }));
        for (const handler of keydownHandlers) {
          handler({ key: 'Escape', preventDefault: vi.fn() });
        }
        await Promise.resolve();
        vi.advanceTimersByTime(CLOSE_ANIMATION_DURATION);
      });

      expect(onBeforeClose).not.toHaveBeenCalled();
      expect(onClose).not.toHaveBeenCalled();
    } finally {
      addSpy.mockRestore();
    }
  });
});
