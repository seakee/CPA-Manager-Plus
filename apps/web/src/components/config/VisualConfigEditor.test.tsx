import type { ReactNode } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { DEFAULT_VISUAL_VALUES } from '@/types/visualConfig';
import styles from './VisualConfigEditor.module.scss';
import { VisualConfigEditor } from './VisualConfigEditor';

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string) => key,
  }),
}));

vi.mock('react-dom', () => ({
  createPortal: (children: ReactNode) => children,
}));

vi.mock('@/hooks/usePanelFeatureAvailability', () => ({
  usePanelFeatureAvailability: () => ({
    checking: false,
    panelHostConfirmed: true,
    panelHostMode: 'cpa_panel',
    managerServiceAvailable: false,
    managerServiceBase: '',
  }),
}));

vi.mock('@/stores', () => ({
  useAuthStore: (selector: (state: { managementKey: string }) => unknown) =>
    selector({ managementKey: 'management-key' }),
  useNotificationStore: (
    selector: (state: {
      showNotification: () => void;
      showConfirmation: () => void;
    }) => unknown
  ) =>
    selector({
      showNotification: vi.fn(),
      showConfirmation: vi.fn(),
    }),
}));

type RectInit = {
  top: number;
  left: number;
  width: number;
  height: number;
};

const createDomRect = ({ top, left, width, height }: RectInit): DOMRect =>
  ({
    x: left,
    y: top,
    top,
    left,
    width,
    height,
    right: left + width,
    bottom: top + height,
    toJSON: () => ({}),
  }) as DOMRect;

class MockStyleDeclaration {
  transform = '';
  width = '';
  maxHeight = '';
  opacity = '';
  pointerEvents = '';
  clipPath = '';

  removeProperty(property: string): void {
    switch (property) {
      case 'transform':
        this.transform = '';
        break;
      case 'width':
        this.width = '';
        break;
      case 'max-height':
      case 'maxHeight':
        this.maxHeight = '';
        break;
      case 'opacity':
        this.opacity = '';
        break;
      case 'pointer-events':
      case 'pointerEvents':
        this.pointerEvents = '';
        break;
      case 'clip-path':
      case 'clipPath':
        this.clipPath = '';
        break;
      default:
        break;
    }
  }
}

type MutationSubscription = {
  target: MockHTMLElement;
  options?: MutationObserverInit;
  callback: MutationCallback;
  observer: MutationObserver;
};

type ObservedSize = {
  width: number;
  height: number;
};

type ResizeSubscription = {
  targets: Map<Element, ObservedSize>;
  callback: ResizeObserverCallback;
  observer: ResizeObserver;
};

const activeMutationSubscriptions = new Set<MutationSubscription>();
const pendingMutationCallbacks = new Set<() => void>();
const activeResizeSubscriptions = new Set<ResizeSubscription>();
const pendingResizeCallbacks = new Set<() => void>();

class MockHTMLElement {
  parentElement: MockHTMLElement | null = null;
  children: MockHTMLElement[] = [];
  style = new MockStyleDeclaration();
  addEventListener = vi.fn();
  removeEventListener = vi.fn();
  scrollIntoView = vi.fn();
  scrollTo = vi.fn();
  getBoundingClientRect: () => DOMRect;
  private readonly classes: Set<string>;

  constructor(className = '', initialRect: RectInit = { top: 0, left: 0, width: 0, height: 0 }) {
    this.classes = new Set(className.split(/\s+/).filter(Boolean));
    const currentRect = createDomRect(initialRect);
    this.getBoundingClientRect = () => currentRect;
  }

  readonly classList = {
    contains: (token: string) => this.classes.has(token),
  };

  stubBoundingClientRect(rectOrGetter: RectInit | (() => RectInit)): void {
    this.getBoundingClientRect =
      typeof rectOrGetter === 'function'
        ? () => createDomRect(rectOrGetter())
        : () => createDomRect(rectOrGetter);
  }

  appendChild(child: MockHTMLElement): MockHTMLElement {
    if (child.parentElement) {
      child.parentElement.removeChild(child);
    }
    child.parentElement = this;
    this.children.push(child);
    this.notifyChildListMutation();
    return child;
  }

  prepend(child: MockHTMLElement): void {
    if (child.parentElement) {
      child.parentElement.removeChild(child);
    }
    child.parentElement = this;
    this.children.unshift(child);
    this.notifyChildListMutation();
  }

  removeChild(child: MockHTMLElement): MockHTMLElement {
    const index = this.children.indexOf(child);
    if (index >= 0) {
      this.children.splice(index, 1);
      child.parentElement = null;
      this.notifyChildListMutation();
    }
    return child;
  }

  notifyResize(): void {
    const target = this as unknown as Element;
    const rect = this.getBoundingClientRect();
    for (const subscription of activeResizeSubscriptions) {
      const lastSize = subscription.targets.get(target);
      if (!lastSize) continue;
      if (lastSize.width === rect.width && lastSize.height === rect.height) {
        continue;
      }
      subscription.targets.set(target, { width: rect.width, height: rect.height });
      pendingResizeCallbacks.add(() => {
        subscription.callback([], subscription.observer);
      });
    }
  }

  private notifyChildListMutation(): void {
    for (const subscription of activeMutationSubscriptions) {
      if (subscription.target === this && subscription.options?.childList) {
        pendingMutationCallbacks.add(() => {
          subscription.callback([], subscription.observer);
        });
      }
    }
  }
}

class MockMutationObserver {
  private readonly callback: MutationCallback;
  private readonly subscriptions: MutationSubscription[] = [];

  constructor(callback: MutationCallback) {
    this.callback = callback;
  }

  observe(target: MockHTMLElement, options?: MutationObserverInit): void {
    const subscription: MutationSubscription = {
      target,
      options,
      callback: this.callback,
      observer: this as unknown as MutationObserver,
    };
    this.subscriptions.push(subscription);
    activeMutationSubscriptions.add(subscription);
  }

  disconnect(): void {
    for (const subscription of this.subscriptions.splice(0)) {
      activeMutationSubscriptions.delete(subscription);
    }
  }

  takeRecords(): MutationRecord[] {
    return [];
  }
}

class MockResizeObserver {
  private readonly subscription: ResizeSubscription;

  constructor(callback: ResizeObserverCallback) {
    this.subscription = {
      targets: new Map<Element, ObservedSize>(),
      callback,
      observer: this as unknown as ResizeObserver,
    };
  }

  observe(target: Element): void {
    const rect = target.getBoundingClientRect();
    this.subscription.targets.set(target, { width: rect.width, height: rect.height });
    activeResizeSubscriptions.add(this.subscription);
  }

  unobserve(target: Element): void {
    this.subscription.targets.delete(target);
  }

  disconnect(): void {
    this.subscription.targets.clear();
    activeResizeSubscriptions.delete(this.subscription);
  }
}

describe('VisualConfigEditor floating sidebar positioning', () => {
  let renderer: ReactTestRenderer | undefined;
  let nextFrameId = 1;
  const queuedAnimationFrames = new Map<number, FrameRequestCallback>();
  let cancelAnimationFrameSpy = vi.fn<(id: number) => void>();

  const flushObserversOnly = () => {
    while (pendingMutationCallbacks.size > 0 || pendingResizeCallbacks.size > 0) {
      const mutationTasks = Array.from(pendingMutationCallbacks);
      pendingMutationCallbacks.clear();
      for (const task of mutationTasks) {
        task();
      }

      const resizeTasks = Array.from(pendingResizeCallbacks);
      pendingResizeCallbacks.clear();
      for (const task of resizeTasks) {
        task();
      }
    }
  };

  const flushObserversAndAnimationFrames = () => {
    while (
      pendingMutationCallbacks.size > 0 ||
      pendingResizeCallbacks.size > 0 ||
      queuedAnimationFrames.size > 0
    ) {
      flushObserversOnly();

      const frameEntries = Array.from(queuedAnimationFrames.entries());
      queuedAnimationFrames.clear();
      for (const [, callback] of frameEntries) {
        callback(16);
      }
    }
  };

  beforeEach(() => {
    nextFrameId = 1;
    queuedAnimationFrames.clear();
    activeMutationSubscriptions.clear();
    pendingMutationCallbacks.clear();
    activeResizeSubscriptions.clear();
    pendingResizeCallbacks.clear();

    const requestAnimationFrameStub = (callback: FrameRequestCallback): number => {
      const id = nextFrameId++;
      queuedAnimationFrames.set(id, callback);
      return id;
    };

    const cancelAnimationFrameStub = vi.fn((id: number): void => {
      queuedAnimationFrames.delete(id);
    });
    cancelAnimationFrameSpy = cancelAnimationFrameStub;

    const headerElement = new MockHTMLElement('main-header', {
      top: 0,
      left: 0,
      width: 1440,
      height: 64,
    });
    const contentScroller = new MockHTMLElement('content', {
      top: 64,
      left: 0,
      width: 1440,
      height: 836,
    });

    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
    vi.stubGlobal('HTMLElement', MockHTMLElement);
    vi.stubGlobal('MutationObserver', MockMutationObserver);
    vi.stubGlobal('ResizeObserver', MockResizeObserver);
    vi.stubGlobal('requestAnimationFrame', requestAnimationFrameStub);
    vi.stubGlobal('cancelAnimationFrame', cancelAnimationFrameStub);
    vi.stubGlobal('getComputedStyle', () => ({
      getPropertyValue: (property: string) => (property === '--header-height' ? '64px' : ''),
    }));
    vi.stubGlobal('document', {
      body: {},
      documentElement: {
        style: {
          getPropertyValue: (property: string) => (property === '--header-height' ? '64px' : ''),
        },
      },
      querySelector: (selector: string) => {
        if (selector === '.main-header') return headerElement;
        if (selector === '.content') return contentScroller;
        return null;
      },
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
    });
    vi.stubGlobal('window', {
      innerWidth: 1440,
      innerHeight: 900,
      requestAnimationFrame: requestAnimationFrameStub,
      cancelAnimationFrame: cancelAnimationFrameStub,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      matchMedia: (query: string) => ({
        matches: query === '(min-width: 1281px)',
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
      }),
    });
  });

  afterEach(() => {
    if (renderer) {
      act(() => {
        renderer?.unmount();
      });
      renderer = undefined;
    }
    vi.unstubAllGlobals();
  });

  it('offsets the floating sidebar below a direct-child error banner and stops updating after unmount', () => {
    const editorContainer = new MockHTMLElement('content');
    editorContainer.stubBoundingClientRect({
      top: 72,
      left: 24,
      width: 1200,
      height: 800,
    });

    const visualEditorElement = new MockHTMLElement(styles.visualEditor, {
      top: 96,
      left: 24,
      width: 1200,
      height: 720,
    });

    const workspaceElement = new MockHTMLElement(styles.workspace);
    workspaceElement.stubBoundingClientRect({
      top: 96,
      left: 24,
      width: 1200,
      height: 720,
    });

    const sidebarAnchorElement = new MockHTMLElement(styles.sidebar);
    sidebarAnchorElement.stubBoundingClientRect({
      top: 96,
      left: 24,
      width: 240,
      height: 600,
    });

    const floatingSidebarElement = new MockHTMLElement(styles.floatingSidebarContainer);
    floatingSidebarElement.stubBoundingClientRect({
      top: 0,
      left: 0,
      width: 240,
      height: 220,
    });

    editorContainer.appendChild(visualEditorElement);
    visualEditorElement.appendChild(workspaceElement);

    act(() => {
      renderer = create(
        <VisualConfigEditor
          values={DEFAULT_VISUAL_VALUES}
          onChange={vi.fn()}
          onPersistApiKeyMutation={vi.fn(async () => [])}
          onRefreshApiKeys={vi.fn(async () => [])}
          onApiKeyOperationStart={vi.fn()}
          onApiKeyOperationEnd={vi.fn()}
        />,
        {
          createNodeMock: (element) => {
            const className =
              typeof element.props === 'object' &&
              element.props !== null &&
              'className' in element.props &&
              typeof element.props.className === 'string'
                ? element.props.className
                : '';
            if (className === styles.workspace) return workspaceElement;
            if (className === styles.sidebar) return sidebarAnchorElement;
            if (className === styles.floatingSidebarContainer) return floatingSidebarElement;
            return new MockHTMLElement(className);
          },
        }
      );
    });
    flushObserversAndAnimationFrames();

    // 1. Initial render with no banner: sidebar top equals the workspace anchor (96px).
    expect(floatingSidebarElement.style.transform).toBe('translate3d(24px, 96px, 0)');

    // 2. Insert a direct-child .error-box above the workspace with its bottom (140px) below the anchor (96px).
    const errorBanner = new MockHTMLElement('error-box');
    errorBanner.stubBoundingClientRect({
      top: 76,
      left: 24,
      width: 1200,
      height: 64,
    });

    act(() => {
      editorContainer.prepend(errorBanner);
      flushObserversAndAnimationFrames();
    });

    // Moves to banner bottom (140px) + 8px gap = 148px without any scroll event.
    expect(floatingSidebarElement.style.transform).toBe('translate3d(24px, 148px, 0)');

    // 3. Remove the banner and flush: top returns to the workspace anchor (96px).
    act(() => {
      editorContainer.removeChild(errorBanner);
      flushObserversAndAnimationFrames();
    });

    expect(floatingSidebarElement.style.transform).toBe('translate3d(24px, 96px, 0)');

    // 4. Unmount, then mutate the container again: nothing throws and no style writes occur on the detached node.
    act(() => {
      renderer?.unmount();
      renderer = undefined;
    });

    expect(floatingSidebarElement.style.transform).toBe('');

    expect(() => {
      act(() => {
        editorContainer.prepend(errorBanner);
        editorContainer.notifyResize();
        flushObserversAndAnimationFrames();
      });
    }).not.toThrow();

    expect(floatingSidebarElement.style.transform).toBe('');
    expect(floatingSidebarElement.style.width).toBe('');
    expect(floatingSidebarElement.style.maxHeight).toBe('');
    expect(floatingSidebarElement.style.opacity).toBe('');
    expect(floatingSidebarElement.style.pointerEvents).toBe('');
    expect(floatingSidebarElement.style.clipPath).toBe('');
  });

  it('cancels a pending animation frame on unmount before the scheduled callback can update layout', () => {
    const editorContainer = new MockHTMLElement('content');
    editorContainer.stubBoundingClientRect({
      top: 72,
      left: 24,
      width: 1200,
      height: 800,
    });

    const visualEditorElement = new MockHTMLElement(styles.visualEditor, {
      top: 96,
      left: 24,
      width: 1200,
      height: 720,
    });

    const workspaceElement = new MockHTMLElement(styles.workspace, {
      top: 96,
      left: 24,
      width: 1200,
      height: 720,
    });

    const sidebarAnchorElement = new MockHTMLElement(styles.sidebar, {
      top: 96,
      left: 24,
      width: 240,
      height: 600,
    });

    const floatingSidebarElement = new MockHTMLElement(styles.floatingSidebarContainer, {
      top: 0,
      left: 0,
      width: 240,
      height: 220,
    });

    editorContainer.appendChild(visualEditorElement);
    visualEditorElement.appendChild(workspaceElement);

    act(() => {
      renderer = create(
        <VisualConfigEditor
          values={DEFAULT_VISUAL_VALUES}
          onChange={vi.fn()}
          onPersistApiKeyMutation={vi.fn(async () => [])}
          onRefreshApiKeys={vi.fn(async () => [])}
          onApiKeyOperationStart={vi.fn()}
          onApiKeyOperationEnd={vi.fn()}
        />,
        {
          createNodeMock: (element) => {
            const className =
              typeof element.props === 'object' &&
              element.props !== null &&
              'className' in element.props &&
              typeof element.props.className === 'string'
                ? element.props.className
                : '';
            if (className === styles.workspace) return workspaceElement;
            if (className === styles.sidebar) return sidebarAnchorElement;
            if (className === styles.floatingSidebarContainer) return floatingSidebarElement;
            return new MockHTMLElement(className);
          },
        }
      );
    });
    flushObserversAndAnimationFrames();

    expect(floatingSidebarElement.style.transform).toBe('translate3d(24px, 96px, 0)');
    cancelAnimationFrameSpy.mockClear();

    const errorBanner = new MockHTMLElement('error-box', {
      top: 76,
      left: 24,
      width: 1200,
      height: 64,
    });

    // Fire the observer so requestPositionUpdate schedules a RAF, but do not run the RAF yet.
    act(() => {
      editorContainer.prepend(errorBanner);
      flushObserversOnly();
    });

    expect(queuedAnimationFrames.size).toBe(1);
    const [scheduledFrameId] = Array.from(queuedAnimationFrames.keys());
    expect(floatingSidebarElement.style.transform).toBe('translate3d(24px, 96px, 0)');

    // Unmount while the scheduled RAF is still pending.
    act(() => {
      renderer?.unmount();
      renderer = undefined;
    });

    expect(cancelAnimationFrameSpy).toHaveBeenCalledWith(scheduledFrameId);
    expect(queuedAnimationFrames.has(scheduledFrameId)).toBe(false);

    // Flushing remaining frames after unmount must never update layout on the unmounted element.
    act(() => {
      flushObserversAndAnimationFrames();
    });

    expect(floatingSidebarElement.style.transform).toBe('');
    expect(floatingSidebarElement.style.width).toBe('');
    expect(floatingSidebarElement.style.maxHeight).toBe('');
    expect(floatingSidebarElement.style.opacity).toBe('');
    expect(floatingSidebarElement.style.pointerEvents).toBe('');
    expect(floatingSidebarElement.style.clipPath).toBe('');
  });

  it('repositions the floating sidebar via ResizeObserver when an existing .error-box grows in height without DOM child mutations', () => {
    const editorContainer = new MockHTMLElement('content');
    editorContainer.stubBoundingClientRect({
      top: 72,
      left: 24,
      width: 1200,
      height: 800,
    });

    const visualEditorElement = new MockHTMLElement(styles.visualEditor, {
      top: 96,
      left: 24,
      width: 1200,
      height: 720,
    });

    const workspaceElement = new MockHTMLElement(styles.workspace, {
      top: 96,
      left: 24,
      width: 1200,
      height: 720,
    });

    const sidebarAnchorElement = new MockHTMLElement(styles.sidebar, {
      top: 96,
      left: 24,
      width: 240,
      height: 600,
    });

    const floatingSidebarElement = new MockHTMLElement(styles.floatingSidebarContainer, {
      top: 0,
      left: 0,
      width: 240,
      height: 220,
    });

    const errorBanner = new MockHTMLElement('error-box', {
      top: 76,
      left: 24,
      width: 1200,
      height: 64,
    });

    editorContainer.appendChild(errorBanner);
    editorContainer.appendChild(visualEditorElement);
    visualEditorElement.appendChild(workspaceElement);

    act(() => {
      renderer = create(
        <VisualConfigEditor
          values={DEFAULT_VISUAL_VALUES}
          onChange={vi.fn()}
          onPersistApiKeyMutation={vi.fn(async () => [])}
          onRefreshApiKeys={vi.fn(async () => [])}
          onApiKeyOperationStart={vi.fn()}
          onApiKeyOperationEnd={vi.fn()}
        />,
        {
          createNodeMock: (element) => {
            const className =
              typeof element.props === 'object' &&
              element.props !== null &&
              'className' in element.props &&
              typeof element.props.className === 'string'
                ? element.props.className
                : '';
            if (className === styles.workspace) return workspaceElement;
            if (className === styles.sidebar) return sidebarAnchorElement;
            if (className === styles.floatingSidebarContainer) return floatingSidebarElement;
            return new MockHTMLElement(className);
          },
        }
      );
    });
    flushObserversAndAnimationFrames();

    // Initial banner bottom is 76 + 64 = 140px -> sidebar top is 140 + 8 = 148px.
    expect(floatingSidebarElement.style.transform).toBe('translate3d(24px, 148px, 0)');

    // Negative check: growing only the unobserved .error-box without resizing any observed target
    // does not fire the size-aware ResizeObserver mock.
    errorBanner.stubBoundingClientRect({
      top: 76,
      left: 24,
      width: 1200,
      height: 112,
    });
    act(() => {
      errorBanner.notifyResize();
      editorContainer.notifyResize();
      workspaceElement.notifyResize();
      sidebarAnchorElement.notifyResize();
      expect(pendingMutationCallbacks.size).toBe(0);
      expect(pendingResizeCallbacks.size).toBe(0);
      flushObserversAndAnimationFrames();
    });
    expect(floatingSidebarElement.style.transform).toBe('translate3d(24px, 148px, 0)');

    // In auto-height layout (ConfigPage .content flex column), the 48px banner growth (64px -> 112px)
    // expands the enclosing observed editorContainer height (800px -> 848px) with no childList mutation.
    editorContainer.stubBoundingClientRect({
      top: 72,
      left: 24,
      width: 1200,
      height: 848,
    });

    act(() => {
      editorContainer.notifyResize();
      expect(pendingMutationCallbacks.size).toBe(0);
      expect(pendingResizeCallbacks.size).toBe(1);
      flushObserversAndAnimationFrames();
    });

    // Sidebar re-measures via ResizeObserver alone to 188 + 8 = 196px.
    expect(floatingSidebarElement.style.transform).toBe('translate3d(24px, 196px, 0)');
  });
});
