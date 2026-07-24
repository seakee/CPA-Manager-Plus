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

const createPointerEvent = (target: unknown, currentTarget: unknown, button = 0) => ({
  target,
  currentTarget,
  button,
});

const installMinimalDom = () => {
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

  if (typeof globalThis.window === 'undefined') {
    Object.defineProperty(globalThis, 'window', {
      configurable: true,
      writable: true,
      value: globalThis,
    });
  }
};

describe('Drawer overlay close guard', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    installMinimalDom();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it('closes when pointer starts and ends on overlay', async () => {
    const onClose = vi.fn();
    let renderer: ReactTestRenderer;

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

    const overlay = renderer!.root.find((node) =>
      String(node.props.className ?? '').includes('overlay')
    );

    await act(async () => {
      overlay.props.onPointerDown(createPointerEvent(overlay, overlay));
      overlay.props.onPointerUp(createPointerEvent(overlay, overlay));
    });

    expect(onClose).not.toHaveBeenCalled();

    await act(async () => {
      vi.advanceTimersByTime(CLOSE_ANIMATION_DURATION);
    });

    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('does not close when pointer starts inside drawer and ends on overlay', async () => {
    const onClose = vi.fn();
    let renderer: ReactTestRenderer;

    await act(async () => {
      renderer = create(
        <Drawer open title="Test drawer" onClose={onClose}>
          <input aria-label="field" />
        </Drawer>
      );
    });

    await act(async () => {
      await Promise.resolve();
    });

    const overlay = renderer!.root.find((node) =>
      String(node.props.className ?? '').includes('overlay')
    );
    const panel = renderer!.root.findByProps({ role: 'dialog' });

    await act(async () => {
      // 模拟：在面板内按下，在遮罩上释放（拖选文字场景）
      overlay.props.onPointerDown(createPointerEvent(panel, overlay));
      overlay.props.onPointerUp(createPointerEvent(overlay, overlay));
    });

    await act(async () => {
      vi.advanceTimersByTime(CLOSE_ANIMATION_DURATION);
    });

    expect(onClose).not.toHaveBeenCalled();
  });

  it('does not close when pointer starts on overlay and ends inside drawer', async () => {
    const onClose = vi.fn();
    let renderer: ReactTestRenderer;

    await act(async () => {
      renderer = create(
        <Drawer open title="Test drawer" onClose={onClose}>
          <input aria-label="field" />
        </Drawer>
      );
    });

    await act(async () => {
      await Promise.resolve();
    });

    const overlay = renderer!.root.find((node) =>
      String(node.props.className ?? '').includes('overlay')
    );
    const panel = renderer!.root.findByProps({ role: 'dialog' });

    await act(async () => {
      overlay.props.onPointerDown(createPointerEvent(overlay, overlay));
      overlay.props.onPointerUp(createPointerEvent(panel, overlay));
    });

    await act(async () => {
      vi.advanceTimersByTime(CLOSE_ANIMATION_DURATION);
    });

    expect(onClose).not.toHaveBeenCalled();
  });
});
