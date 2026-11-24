import type * as ReactDomModule from 'react-dom';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string) => key,
  }),
}));

vi.mock('react-dom', async () => {
  const actual = await vi.importActual<typeof ReactDomModule>('react-dom');
  return {
    ...actual,
    createPortal: (children: unknown) => children,
  };
});

vi.mock('../Drawer.module.scss', () => ({
  default: {
    overlay: 'drawerOverlay',
    overlayClosing: 'drawerOverlayClosing',
    overlayEntering: 'drawerOverlayEntering',
    panel: 'drawerPanel',
    panelClosing: 'drawerPanelClosing',
    panelEntering: 'drawerPanelEntering',
    header: 'drawerHeader',
    title: 'drawerTitle',
    closeButton: 'drawerCloseButton',
    body: 'drawerBody',
    footer: 'drawerFooter',
  },
}));

vi.mock('../Modal.module.scss', () => ({
  default: {
    overlay: 'modalOverlay',
    overlayClosing: 'modalOverlayClosing',
    overlayEntering: 'modalOverlayEntering',
    modal: 'modalPanel',
    modalClosing: 'modalPanelClosing',
    modalEntering: 'modalPanelEntering',
    header: 'modalHeader',
    title: 'modalTitle',
    closeButton: 'modalCloseButton',
    body: 'modalBody',
    footer: 'modalFooter',
  },
}));

vi.mock('../icons', () => ({
  IconX: () => null,
}));

import { Drawer } from '../Drawer';
import { Modal } from '../Modal';
import { acquireOverlayScrollLock } from '../overlayEscapeStack';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const CLOSE_ANIMATION_DURATION = 400;

interface MockFocusElement {
  focus: ReturnType<typeof vi.fn>;
  hasAttribute: () => boolean;
  tabIndex: number;
  querySelectorAll?: () => MockFocusElement[];
  contains?: (node: unknown) => boolean;
}

interface EscapeDomEnv {
  bodyStyle: { position: string; top: string; left: string; right: string; width: string; overflow: string };
  htmlStyle: { overflow: string };
  bodyClasses: Set<string>;
  htmlClasses: Set<string>;
  windowScrollTo: ReturnType<typeof vi.fn>;
  contentScrollTo: ReturnType<typeof vi.fn>;
  getContentScrollTop: () => number;
  setScrollPositions: (windowY: number, contentTop?: number) => void;
  createFocusElement: (options?: { focusable?: MockFocusElement[] }) => MockFocusElement;
  setActiveElement: (el: unknown) => void;
  dispatchKey: (key: string, options?: { shiftKey?: boolean }) => KeyboardEvent;
  dispatchEscape: () => KeyboardEvent;
  restore: () => void;
}

const installEscapeDom = (): EscapeDomEnv => {
  const previousHTMLElement = Object.getOwnPropertyDescriptor(globalThis, 'HTMLElement');
  const previousDocument = Object.getOwnPropertyDescriptor(globalThis, 'document');
  const previousWindow = Object.getOwnPropertyDescriptor(globalThis, 'window');
  const bodyStyle = { position: '', top: '', left: '', right: '', width: '', overflow: '' };
  const htmlStyle = { overflow: '' };
  const bodyClasses = new Set<string>();
  const htmlClasses = new Set<string>();
  const listeners = new Map<string, Set<EventListenerOrEventListenerObject>>();
  const windowScrollTo = vi.fn();
  const contentScrollTo = vi.fn();

  class HTMLElementMock {
    scrollTop = 0;
    focus = vi.fn();
    scrollTo = vi.fn();
    hasAttribute() {
      return false;
    }
    get tabIndex() {
      return 0;
    }
    querySelectorAll(): unknown[] {
      return [];
    }
    contains(_node?: unknown): boolean {
      return false;
    }
  }

  const contentEl = new HTMLElementMock();
  contentEl.scrollTo = contentScrollTo;

  const documentMock = {
    body: {
      style: bodyStyle,
      classList: {
        add: vi.fn((cls: string) => {
          bodyClasses.add(cls);
        }),
        remove: vi.fn((cls: string) => {
          bodyClasses.delete(cls);
        }),
      },
    },
    documentElement: {
      style: htmlStyle,
      scrollTop: 0,
      classList: {
        add: vi.fn((cls: string) => {
          htmlClasses.add(cls);
        }),
        remove: vi.fn((cls: string) => {
          htmlClasses.delete(cls);
        }),
      },
    },
    activeElement: null as unknown,
    querySelector: (selector: string) => (selector === '.content' ? contentEl : null),
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

  const windowMock = Object.assign(Object.create(globalThis), {
    scrollY: 0,
    pageYOffset: 0,
    scrollTo: windowScrollTo,
  });
  Object.defineProperty(globalThis, 'window', {
    configurable: true,
    writable: true,
    value: windowMock,
  });

  const createFocusElement = (options?: { focusable?: MockFocusElement[] }): MockFocusElement => {
    const el = new HTMLElementMock();
    el.focus = vi.fn(() => {
      documentMock.activeElement = el;
    });
    if (options?.focusable) {
      el.querySelectorAll = () => options.focusable!;
      el.contains = (node: unknown) => node === el || options.focusable!.includes(node as MockFocusElement);
    }
    return el as unknown as MockFocusElement;
  };

  const dispatchKey = (key: string, options: { shiftKey?: boolean } = {}) => {
    let immediateStopped = false;
    let defaultPrevented = false;
    const preventDefault = vi.fn(() => {
      defaultPrevented = true;
    });
    const event = {
      key,
      shiftKey: Boolean(options.shiftKey),
      get defaultPrevented() {
        return defaultPrevented;
      },
      preventDefault,
      stopImmediatePropagation: () => {
        immediateStopped = true;
      },
    } as unknown as KeyboardEvent;

    for (const listener of Array.from(listeners.get('keydown') ?? [])) {
      if (immediateStopped) break;
      if (typeof listener === 'function') {
        listener(event);
      } else {
        listener.handleEvent(event);
      }
    }
    return event;
  };

  const dispatchEscape = () => dispatchKey('Escape');

  return {
    bodyStyle,
    htmlStyle,
    bodyClasses,
    htmlClasses,
    windowScrollTo,
    contentScrollTo,
    getContentScrollTop: () => contentEl.scrollTop,
    setScrollPositions: (windowY: number, contentTop = 0) => {
      windowMock.scrollY = windowY;
      windowMock.pageYOffset = windowY;
      contentEl.scrollTop = contentTop;
    },
    createFocusElement,
    setActiveElement: (el: unknown) => {
      documentMock.activeElement = el;
    },
    dispatchKey,
    dispatchEscape,
    restore: () => {
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
      if (previousWindow) {
        Object.defineProperty(globalThis, 'window', previousWindow);
      } else {
        Reflect.deleteProperty(globalThis, 'window');
      }
    },
  };
};

describe('Overlay Escape stack (Modal inside Drawer & stacked Drawers)', () => {
  let renderer: ReactTestRenderer | undefined;
  let env: EscapeDomEnv | undefined;

  beforeEach(() => {
    vi.useFakeTimers();
    env = installEscapeDom();
  });

  afterEach(() => {
    act(() => {
      renderer?.unmount();
    });
    renderer = undefined;
    env?.restore();
    env = undefined;
    vi.useRealTimers();
  });

  it('closes only the topmost Modal on Escape and keeps the underlying Drawer open', async () => {
    const onDrawerClose = vi.fn();
    const onModalClose = vi.fn();

    await act(async () => {
      renderer = create(
        <Drawer open title="Workspace Drawer" onClose={onDrawerClose}>
          <Modal open title="Confirm Modal" onClose={onModalClose}>
            <div>modal body</div>
          </Modal>
        </Drawer>
      );
    });
    await act(async () => {
      await Promise.resolve();
    });

    await act(async () => {
      env!.dispatchEscape();
      vi.advanceTimersByTime(CLOSE_ANIMATION_DURATION);
    });

    expect(onModalClose).toHaveBeenCalledTimes(1);
    expect(onDrawerClose).not.toHaveBeenCalled();

    // Once Modal closes, a subsequent Escape closes the underlying Drawer
    await act(async () => {
      renderer!.update(
        <Drawer open title="Workspace Drawer" onClose={onDrawerClose}>
          <Modal open={false} title="Confirm Modal" onClose={onModalClose}>
            <div>modal body</div>
          </Modal>
        </Drawer>
      );
    });
    await act(async () => {
      await Promise.resolve();
      vi.advanceTimersByTime(CLOSE_ANIMATION_DURATION);
    });

    await act(async () => {
      env!.dispatchEscape();
      vi.advanceTimersByTime(CLOSE_ANIMATION_DURATION);
    });

    expect(onDrawerClose).toHaveBeenCalledTimes(1);
  });

  it('closes only the topmost stacked Drawer on Escape', async () => {
    const onBaseClose = vi.fn();
    const onTopClose = vi.fn();

    await act(async () => {
      renderer = create(
        <>
          <Drawer open title="Base Drawer" onClose={onBaseClose}>
            <div>base</div>
          </Drawer>
          <Drawer open title="Top Drawer" onClose={onTopClose}>
            <div>top</div>
          </Drawer>
        </>
      );
    });
    await act(async () => {
      await Promise.resolve();
    });

    await act(async () => {
      env!.dispatchEscape();
      vi.advanceTimersByTime(CLOSE_ANIMATION_DURATION);
    });

    expect(onTopClose).toHaveBeenCalledTimes(1);
    expect(onBaseClose).not.toHaveBeenCalled();
  });

  it('preserves stack order when a lower Drawer or Modal re-renders with new callback props', async () => {
    const onBaseDrawerCloseA = vi.fn();
    const onBaseDrawerCloseB = vi.fn();
    const onTopDrawerClose = vi.fn();
    const onBaseModalCloseA = vi.fn();
    const onBaseModalCloseB = vi.fn();
    const onTopModalClose = vi.fn();

    await act(async () => {
      renderer = create(
        <>
          <Drawer open title="Base Drawer" onClose={onBaseDrawerCloseA}>
            <div>base</div>
          </Drawer>
          <Drawer open title="Top Drawer" onClose={onTopDrawerClose}>
            <div>top</div>
          </Drawer>
          <Modal open title="Base Modal" onClose={onBaseModalCloseA}>
            <div>base modal</div>
          </Modal>
          <Modal open title="Top Modal" onClose={onTopModalClose}>
            <div>top modal</div>
          </Modal>
        </>
      );
      await Promise.resolve();
    });

    // Re-render lower overlays with fresh inline/changed callbacks and closeDisabled toggles
    await act(async () => {
      renderer!.update(
        <>
          <Drawer open title="Base Drawer" closeDisabled onClose={onBaseDrawerCloseB}>
            <div>base updated</div>
          </Drawer>
          <Drawer open title="Top Drawer" onClose={onTopDrawerClose}>
            <div>top</div>
          </Drawer>
          <Modal open title="Base Modal" onClose={onBaseModalCloseB}>
            <div>base modal updated</div>
          </Modal>
          <Modal open title="Top Modal" onClose={onTopModalClose}>
            <div>top modal</div>
          </Modal>
        </>
      );
      await Promise.resolve();
    });

    await act(async () => {
      env!.dispatchEscape();
      vi.advanceTimersByTime(CLOSE_ANIMATION_DURATION);
    });

    expect(onTopModalClose).toHaveBeenCalledTimes(1);
    expect(onBaseModalCloseA).not.toHaveBeenCalled();
    expect(onBaseModalCloseB).not.toHaveBeenCalled();
    expect(onTopDrawerClose).not.toHaveBeenCalled();
    expect(onBaseDrawerCloseB).not.toHaveBeenCalled();
  });

  it('keeps unified scroll lock intact when Modal closes before Drawer, then restores snapshot once Drawer closes', async () => {
    env!.bodyStyle.overflow = 'auto';
    env!.htmlStyle.overflow = 'scroll';
    env!.setScrollPositions(240, 120);

    await act(async () => {
      renderer = create(
        <Drawer open title="Workspace Drawer" onClose={vi.fn()}>
          <Modal open title="Confirm Modal" onClose={vi.fn()}>
            <div>modal body</div>
          </Modal>
        </Drawer>
      );
    });
    await act(async () => {
      await Promise.resolve();
    });

    expect(env!.bodyClasses.has('modal-open')).toBe(true);
    expect(env!.bodyStyle.position).toBe('fixed');
    expect(env!.bodyStyle.top).toBe('-240px');
    expect(env!.bodyStyle.overflow).toBe('hidden');
    expect(env!.htmlStyle.overflow).toBe('hidden');

    // Simulate browser viewport/content reset while fixed positioning is active, then close Modal while Drawer stays open
    env!.setScrollPositions(0, 0);
    await act(async () => {
      renderer!.update(
        <Drawer open title="Workspace Drawer" onClose={vi.fn()}>
          <Modal open={false} title="Confirm Modal" onClose={vi.fn()}>
            <div>modal body</div>
          </Modal>
        </Drawer>
      );
    });
    await act(async () => {
      await Promise.resolve();
      vi.advanceTimersByTime(CLOSE_ANIMATION_DURATION);
    });

    expect(env!.bodyClasses.has('modal-open')).toBe(false);
    expect(env!.bodyStyle.position).toBe('');
    expect(env!.bodyStyle.top).toBe('');
    expect(env!.bodyStyle.overflow).toBe('hidden');
    expect(env!.htmlStyle.overflow).toBe('hidden');
    expect(env!.windowScrollTo).toHaveBeenCalledTimes(1);
    expect(env!.windowScrollTo).toHaveBeenCalledWith(0, 240);
    expect(env!.getContentScrollTop()).toBe(120);

    // Now close Drawer -> pre-overlay snapshot and scroll position restore cleanly
    await act(async () => {
      renderer!.update(
        <Drawer open={false} title="Workspace Drawer" onClose={vi.fn()}>
          <Modal open={false} title="Confirm Modal" onClose={vi.fn()}>
            <div>modal body</div>
          </Modal>
        </Drawer>
      );
    });
    await act(async () => {
      await Promise.resolve();
      vi.advanceTimersByTime(CLOSE_ANIMATION_DURATION);
    });

    expect(env!.bodyStyle.overflow).toBe('auto');
    expect(env!.htmlStyle.overflow).toBe('scroll');
    expect(env!.windowScrollTo).toHaveBeenCalledTimes(2);
    expect(env!.windowScrollTo).toHaveBeenLastCalledWith({ top: 240, left: 0, behavior: 'auto' });
    expect(env!.contentScrollTo).toHaveBeenCalledTimes(1);
    expect(env!.contentScrollTo).toHaveBeenCalledWith({ top: 120, left: 0, behavior: 'auto' });
  });

  it('keeps modal fixed scroll lock when Drawer closes before Modal, then restores snapshot once Modal closes', async () => {
    env!.bodyStyle.overflow = 'auto';
    env!.htmlStyle.overflow = 'scroll';
    env!.setScrollPositions(300, 80);

    await act(async () => {
      renderer = create(
        <>
          <Drawer open title="Workspace Drawer" onClose={vi.fn()}>
            <div>drawer body</div>
          </Drawer>
          <Modal open title="Confirm Modal" onClose={vi.fn()}>
            <div>modal body</div>
          </Modal>
        </>
      );
    });
    await act(async () => {
      await Promise.resolve();
    });

    expect(env!.bodyClasses.has('modal-open')).toBe(true);
    expect(env!.bodyStyle.position).toBe('fixed');
    expect(env!.bodyStyle.top).toBe('-300px');

    // Close Drawer first while Modal stays open
    await act(async () => {
      renderer!.update(
        <>
          <Drawer open={false} title="Workspace Drawer" onClose={vi.fn()}>
            <div>drawer body</div>
          </Drawer>
          <Modal open title="Confirm Modal" onClose={vi.fn()}>
            <div>modal body</div>
          </Modal>
        </>
      );
    });
    await act(async () => {
      await Promise.resolve();
      vi.advanceTimersByTime(CLOSE_ANIMATION_DURATION);
    });

    expect(env!.bodyClasses.has('modal-open')).toBe(true);
    expect(env!.bodyStyle.position).toBe('fixed');
    expect(env!.bodyStyle.top).toBe('-300px');
    expect(env!.bodyStyle.overflow).toBe('hidden');
    expect(env!.windowScrollTo).not.toHaveBeenCalled();

    // Close Modal -> snapshot restores cleanly
    await act(async () => {
      renderer!.update(
        <>
          <Drawer open={false} title="Workspace Drawer" onClose={vi.fn()}>
            <div>drawer body</div>
          </Drawer>
          <Modal open={false} title="Confirm Modal" onClose={vi.fn()}>
            <div>modal body</div>
          </Modal>
        </>
      );
    });
    await act(async () => {
      await Promise.resolve();
      vi.advanceTimersByTime(CLOSE_ANIMATION_DURATION);
    });

    expect(env!.bodyClasses.has('modal-open')).toBe(false);
    expect(env!.bodyStyle.position).toBe('');
    expect(env!.bodyStyle.overflow).toBe('auto');
    expect(env!.htmlStyle.overflow).toBe('scroll');
    expect(env!.windowScrollTo).toHaveBeenCalledWith({ top: 300, left: 0, behavior: 'auto' });
    expect(env!.contentScrollTo).toHaveBeenCalledWith({ top: 80, left: 0, behavior: 'auto' });
  });

  it('makes scroll lock release functions idempotent so double release never underflows', () => {
    env!.bodyStyle.overflow = 'auto';
    const releaseDrawer = acquireOverlayScrollLock('drawer');
    const releaseModal = acquireOverlayScrollLock('modal');

    releaseModal();
    releaseModal();
    expect(env!.bodyStyle.overflow).toBe('hidden');
    expect(env!.bodyClasses.has('modal-open')).toBe(false);

    releaseDrawer();
    releaseDrawer();
    expect(env!.bodyStyle.overflow).toBe('auto');
  });

  it('blocks Escape from leaking to underlying Drawer during Modal close fade-out animation', async () => {
    const onDrawerClose = vi.fn();
    const onModalClose = vi.fn();

    await act(async () => {
      renderer = create(
        <Drawer open title="Workspace Drawer" onClose={onDrawerClose}>
          <Modal open title="Confirm Modal" onClose={onModalClose}>
            <div>modal body</div>
          </Modal>
        </Drawer>
      );
    });
    await act(async () => {
      await Promise.resolve();
    });

    // Parent initiates Modal fade-out
    await act(async () => {
      renderer!.update(
        <Drawer open title="Workspace Drawer" onClose={onDrawerClose}>
          <Modal open={false} title="Confirm Modal" onClose={onModalClose}>
            <div>modal body</div>
          </Modal>
        </Drawer>
      );
    });
    await act(async () => {
      await Promise.resolve();
      vi.advanceTimersByTime(100);
    });

    // Press Escape while Modal is still mid-fade-out (100ms < 350ms)
    let midFadeEvent!: KeyboardEvent;
    await act(async () => {
      midFadeEvent = env!.dispatchEscape();
      vi.advanceTimersByTime(100);
    });

    expect(midFadeEvent.preventDefault).toHaveBeenCalled();
    expect(onDrawerClose).not.toHaveBeenCalled();
    expect(onModalClose).not.toHaveBeenCalled();

    // Complete Modal fade-out; next Escape should close Drawer
    await act(async () => {
      vi.advanceTimersByTime(CLOSE_ANIMATION_DURATION);
    });

    await act(async () => {
      env!.dispatchEscape();
      vi.advanceTimersByTime(CLOSE_ANIMATION_DURATION);
    });

    expect(onDrawerClose).toHaveBeenCalledTimes(1);
  });

  it('keeps Modal in closing state without flashing back open when onClose updates open asynchronously', async () => {
    const onClose = vi.fn(() => {
      globalThis.setTimeout(() => {
        renderer?.update(
          <Modal open={false} title="Async Modal" onClose={onClose}>
            <div>modal body</div>
          </Modal>
        );
      }, 16);
    });

    await act(async () => {
      renderer = create(
        <Modal open title="Async Modal" onClose={onClose}>
          <div>modal body</div>
        </Modal>
      );
    });
    await act(async () => {
      await Promise.resolve();
    });

    await act(async () => {
      env!.dispatchEscape();
      vi.advanceTimersByTime(350);
    });

    expect(onClose).toHaveBeenCalledTimes(1);
    const closingOverlay = renderer!.root.find((node) =>
      String(node.props.className ?? '').includes('modal-overlay')
    );
    expect(closingOverlay.props.className).toContain('modal-overlay-closing');
    expect(closingOverlay.props.className).not.toContain('modal-overlay-entering');

    await act(async () => {
      vi.advanceTimersByTime(16);
    });
    await act(async () => {
      await Promise.resolve();
    });

    expect(renderer!.root.findAllByProps({ role: 'dialog' })).toHaveLength(0);
  });

  it('preserves original pre-open focus target across rapid open true -> false -> true toggles for Modal and Drawer', async () => {
    const modalTrigger = env!.createFocusElement();
    const modalInner = env!.createFocusElement();
    env!.setActiveElement(modalTrigger);

    await act(async () => {
      renderer = create(
        <Modal open title="Rapid Modal" onClose={vi.fn()}>
          <div>body</div>
        </Modal>
      );
    });
    await act(async () => {
      await Promise.resolve();
      vi.advanceTimersByTime(10);
    });

    // Simulate focus having moved inside Modal
    env!.setActiveElement(modalInner);

    // Rapid close -> reopen before fade-out finishes
    await act(async () => {
      renderer!.update(
        <Modal open={false} title="Rapid Modal" onClose={vi.fn()}>
          <div>body</div>
        </Modal>
      );
    });
    await act(async () => {
      await Promise.resolve();
      vi.advanceTimersByTime(100);
    });

    await act(async () => {
      renderer!.update(
        <Modal open title="Rapid Modal" onClose={vi.fn()}>
          <div>body</div>
        </Modal>
      );
    });
    await act(async () => {
      await Promise.resolve();
      vi.advanceTimersByTime(10);
    });

    // Now fully close Modal
    await act(async () => {
      renderer!.update(
        <Modal open={false} title="Rapid Modal" onClose={vi.fn()}>
          <div>body</div>
        </Modal>
      );
    });
    await act(async () => {
      await Promise.resolve();
      vi.advanceTimersByTime(CLOSE_ANIMATION_DURATION);
    });

    expect(modalTrigger.focus).toHaveBeenCalledTimes(1);
    expect(modalInner.focus).not.toHaveBeenCalled();

    // Repeat for Drawer
    const drawerTrigger = env!.createFocusElement();
    const drawerInner = env!.createFocusElement();
    env!.setActiveElement(drawerTrigger);

    await act(async () => {
      renderer!.update(
        <Drawer open title="Rapid Drawer" onClose={vi.fn()}>
          <div>body</div>
        </Drawer>
      );
    });
    await act(async () => {
      await Promise.resolve();
      vi.advanceTimersByTime(10);
    });

    env!.setActiveElement(drawerInner);

    await act(async () => {
      renderer!.update(
        <Drawer open={false} title="Rapid Drawer" onClose={vi.fn()}>
          <div>body</div>
        </Drawer>
      );
    });
    await act(async () => {
      await Promise.resolve();
      vi.advanceTimersByTime(100);
    });

    await act(async () => {
      renderer!.update(
        <Drawer open title="Rapid Drawer" onClose={vi.fn()}>
          <div>body</div>
        </Drawer>
      );
    });
    await act(async () => {
      await Promise.resolve();
      vi.advanceTimersByTime(10);
    });

    await act(async () => {
      renderer!.update(
        <Drawer open={false} title="Rapid Drawer" onClose={vi.fn()}>
          <div>body</div>
        </Drawer>
      );
    });
    await act(async () => {
      await Promise.resolve();
      vi.advanceTimersByTime(CLOSE_ANIMATION_DURATION);
    });

    expect(drawerTrigger.focus).toHaveBeenCalledTimes(1);
    expect(drawerInner.focus).not.toHaveBeenCalled();
  });

  it('gates Modal Tab focus trapping to the topmost stacked Modal', async () => {
    const baseFirst = env!.createFocusElement();
    const baseLast = env!.createFocusElement();
    const topFirst = env!.createFocusElement();
    const topLast = env!.createFocusElement();
    const basePanel = env!.createFocusElement({ focusable: [baseFirst, baseLast] });
    const topPanel = env!.createFocusElement({ focusable: [topFirst, topLast] });
    let dialogIndex = 0;

    await act(async () => {
      renderer = create(
        <>
          <Modal open title="Base Modal" onClose={vi.fn()}>
            <div>base</div>
          </Modal>
          <Modal open title="Top Modal" onClose={vi.fn()}>
            <div>top</div>
          </Modal>
        </>,
        {
          createNodeMock: (element) => {
            if (
              element.type === 'div' &&
              typeof element.props === 'object' &&
              element.props !== null &&
              'role' in element.props &&
              element.props.role === 'dialog'
            ) {
              dialogIndex += 1;
              return dialogIndex === 1 ? basePanel : topPanel;
            }
            return null;
          },
        }
      );
      await Promise.resolve();
    });

    env!.setActiveElement(topLast);
    let tabEvent!: KeyboardEvent;
    await act(async () => {
      tabEvent = env!.dispatchKey('Tab');
    });

    expect(tabEvent.preventDefault).toHaveBeenCalled();
    expect(topFirst.focus).toHaveBeenCalledTimes(1);
    expect(baseFirst.focus).not.toHaveBeenCalled();
    expect(baseLast.focus).not.toHaveBeenCalled();
  });

  it('reaches closed phase and releases scroll lock and key traps across closing -> open=true -> open=false before microtask flush', async () => {
    env!.bodyStyle.overflow = 'auto';
    env!.htmlStyle.overflow = 'scroll';

    await act(async () => {
      renderer = create(
        <Modal open title="Toggle Modal" onClose={vi.fn()}>
          <div>modal body</div>
        </Modal>
      );
    });
    await act(async () => {
      await Promise.resolve();
    });

    // Transition to closing with an active close timer
    await act(async () => {
      renderer!.update(
        <Modal open={false} title="Toggle Modal" onClose={vi.fn()}>
          <div>modal body</div>
        </Modal>
      );
    });
    await act(async () => {
      await Promise.resolve();
      vi.advanceTimersByTime(100);
    });

    // Flip open true -> false before the queued entering microtask flushes
    act(() => {
      renderer!.update(
        <Modal open title="Toggle Modal" onClose={vi.fn()}>
          <div>modal body</div>
        </Modal>
      );
      renderer!.update(
        <Modal open={false} title="Toggle Modal" onClose={vi.fn()}>
          <div>modal body</div>
        </Modal>
      );
    });

    await act(async () => {
      await Promise.resolve();
      vi.advanceTimersByTime(CLOSE_ANIMATION_DURATION);
    });

    expect(renderer!.root.findAllByProps({ role: 'dialog' })).toHaveLength(0);
    expect(env!.bodyClasses.has('modal-open')).toBe(false);
    expect(env!.bodyStyle.overflow).toBe('auto');
    expect(env!.htmlStyle.overflow).toBe('scroll');

    const escapeEvent = env!.dispatchEscape();
    const tabEvent = env!.dispatchKey('Tab');
    expect(escapeEvent.preventDefault).not.toHaveBeenCalled();
    expect(tabEvent.preventDefault).not.toHaveBeenCalled();
  });

  it('recovers Modal back to entering when closeDisabled flips true mid-fadeout', async () => {
    const onClose = vi.fn();

    await act(async () => {
      renderer = create(
        <Modal open title="Lockable Modal" onClose={onClose}>
          <div>modal body</div>
        </Modal>
      );
    });
    await act(async () => {
      await Promise.resolve();
    });

    // Start fade-out via Escape
    await act(async () => {
      env!.dispatchEscape();
      vi.advanceTimersByTime(100);
    });
    expect(
      renderer!.root.find((node) => String(node.props.className ?? '').includes('modal-overlay'))
        .props.className
    ).toContain('modal-overlay-closing');

    // Parent locks closing mid-fadeout while keeping open=true
    await act(async () => {
      renderer!.update(
        <Modal open closeDisabled title="Lockable Modal" onClose={onClose}>
          <div>modal body</div>
        </Modal>
      );
    });
    await act(async () => {
      await Promise.resolve();
      vi.advanceTimersByTime(CLOSE_ANIMATION_DURATION);
    });

    expect(onClose).not.toHaveBeenCalled();
    const recoveredOverlay = renderer!.root.find((node) =>
      String(node.props.className ?? '').includes('modal-overlay')
    );
    expect(recoveredOverlay.props.className).toContain('modal-overlay-entering');
    expect(recoveredOverlay.props.className).not.toContain('modal-overlay-closing');

    // Re-enabling close lets subsequent Escape close normally
    await act(async () => {
      renderer!.update(
        <Modal open closeDisabled={false} title="Lockable Modal" onClose={onClose}>
          <div>modal body</div>
        </Modal>
      );
      await Promise.resolve();
    });
    await act(async () => {
      env!.dispatchEscape();
      vi.advanceTimersByTime(CLOSE_ANIMATION_DURATION);
    });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('recovers Modal back to entering when onClose declines to set open=false', async () => {
    let allowClose = false;
    const onClose = vi.fn(() => {
      if (!allowClose) return;
      renderer?.update(
        <Modal open={false} title="Guarded Modal" onClose={onClose}>
          <div>modal body</div>
        </Modal>
      );
    });

    await act(async () => {
      renderer = create(
        <Modal open title="Guarded Modal" onClose={onClose}>
          <div>modal body</div>
        </Modal>
      );
    });
    await act(async () => {
      await Promise.resolve();
    });

    // Escape triggers close animation, onClose declines to flip open=false
    await act(async () => {
      env!.dispatchEscape();
      vi.advanceTimersByTime(350);
    });
    expect(onClose).toHaveBeenCalledTimes(1);

    // After reconcile grace window, Modal recovers to entering instead of stranding in closing
    await act(async () => {
      vi.advanceTimersByTime(150);
      await Promise.resolve();
    });
    const recoveredOverlay = renderer!.root.find((node) =>
      String(node.props.className ?? '').includes('modal-overlay')
    );
    expect(recoveredOverlay.props.className).toContain('modal-overlay-entering');
    expect(recoveredOverlay.props.className).not.toContain('modal-overlay-closing');

    // Subsequent close attempt works once parent allows it
    allowClose = true;
    await act(async () => {
      env!.dispatchEscape();
      vi.advanceTimersByTime(350);
      await Promise.resolve();
    });
    expect(onClose).toHaveBeenCalledTimes(2);
    expect(renderer!.root.findAllByProps({ role: 'dialog' })).toHaveLength(0);
  });
});
