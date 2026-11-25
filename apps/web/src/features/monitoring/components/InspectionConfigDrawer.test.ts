import { createElement, type ReactNode, type RefObject } from 'react';
import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { afterEach, describe, expect, it, vi } from 'vitest';

type MockDrawerProps = {
  open: boolean;
  width?: number | string;
  closeLabel?: string;
  onClose: () => void;
  onBeforeClose?: () => boolean | Promise<boolean>;
  title?: ReactNode;
  footer?: ReactNode;
  bodyRef?: RefObject<HTMLDivElement | null>;
  children?: ReactNode;
};

const drawerPropsSpy = vi.fn<(props: MockDrawerProps) => void>();

vi.mock('@/components/ui/Drawer', () => ({
  Drawer: (props: MockDrawerProps) => {
    drawerPropsSpy(props);
    if (!props.open) return null;
    return createElement('div', { 'data-testid': 'mock-drawer' }, props.title, props.children, props.footer);
  },
}));

import { DRAWER_WIDTH } from '@/components/common/WorkspaceDrawer';
import { InspectionConfigDrawer } from './InspectionConfigDrawer';
import { getInspectionConfigFocusTarget } from './inspectionConfigFocus';

Reflect.set(globalThis, 'IS_REACT_ACT_ENVIRONMENT', true);

describe('getInspectionConfigFocusTarget', () => {
  it('keeps a directly focusable target', () => {
    const target = {
      matches: vi.fn(() => true),
      querySelector: vi.fn(),
    } as unknown as HTMLElement;

    expect(getInspectionConfigFocusTarget(target)).toBe(target);
    expect(target.querySelector).not.toHaveBeenCalled();
  });

  it('finds a focusable control inside a field wrapper', () => {
    const nested = {} as HTMLElement;
    const target = {
      matches: vi.fn(() => false),
      querySelector: vi.fn(() => nested),
    } as unknown as HTMLElement;

    expect(getInspectionConfigFocusTarget(target)).toBe(nested);
  });
});

describe('InspectionConfigDrawer', () => {
  afterEach(() => {
    drawerPropsSpy.mockReset();
    vi.useRealTimers();
  });

  it('composes standard Drawer with WorkspaceDrawer header, body shell, and footer', () => {
    const onClose = vi.fn();
    let renderer!: ReactTestRenderer;
    act(() => {
      renderer = create(
        createElement(
          InspectionConfigDrawer,
          {
            open: true,
            title: 'Inspection Settings',
            description: 'Configure thresholds and concurrency',
            closeLabel: 'Close',
            onClose,
            footer: createElement('button', { type: 'button' }, 'Save'),
          },
          createElement('div', { id: 'sampleSize' }, 'Field content')
        )
      );
    });

    const passedProps = drawerPropsSpy.mock.lastCall?.[0];
    expect(passedProps?.width).toBe(DRAWER_WIDTH.MD);
    expect(passedProps?.closeLabel).toBe('Close');
    expect(passedProps?.onBeforeClose?.()).toBe(false);
    expect(onClose).toHaveBeenCalledTimes(1);
    const renderedJson = JSON.stringify(renderer.toJSON());
    expect(renderedJson).toContain('Inspection Settings');
    expect(renderedJson).toContain('Configure thresholds and concurrency');
    expect(renderedJson).toContain('Field content');
    expect(renderedJson).toContain('Save');
    act(() => renderer.unmount());
  });

  it('expands ancestor details and focuses focusField inside drawerBodyRef', () => {
    vi.useFakeTimers();
    const previousWindow = Object.getOwnPropertyDescriptor(globalThis, 'window');
    const focus = vi.fn();
    const scrollIntoView = vi.fn();
    const setAttribute = vi.fn();
    const targetEl = {
      matches: vi.fn(() => false),
      querySelector: vi.fn(() => ({ focus })),
      closest: vi.fn(() => ({ setAttribute })),
      scrollIntoView,
    };
    const bodyElement = {
      querySelector: vi.fn((selector: string) => (selector === '#workers' ? targetEl : null)),
    } as unknown as HTMLDivElement;

    Object.defineProperty(globalThis, 'window', {
      configurable: true,
      writable: true,
      value: {
        setTimeout: globalThis.setTimeout.bind(globalThis),
        clearTimeout: globalThis.clearTimeout.bind(globalThis),
        requestAnimationFrame: (cb: FrameRequestCallback) => {
          cb(0);
          return 1;
        },
        cancelAnimationFrame: vi.fn(),
      },
    });

    let renderer: ReactTestRenderer | undefined;
    try {
      act(() => {
        renderer = create(
          createElement(
            InspectionConfigDrawer,
            {
              open: true,
              title: 'Inspection Settings',
              closeLabel: 'Close',
              focusField: 'workers',
              onClose: vi.fn(),
            },
            createElement('div', null, 'Body')
          )
        );
      });

      const passedProps = drawerPropsSpy.mock.lastCall?.[0];
      if (passedProps?.bodyRef) {
        passedProps.bodyRef.current = bodyElement;
      }
      act(() => {
        vi.runAllTimers();
      });

      expect(setAttribute).toHaveBeenCalledWith('open', '');
      expect(scrollIntoView).toHaveBeenCalledWith({ block: 'center', behavior: 'smooth' });
      expect(focus).toHaveBeenCalledWith({ preventScroll: true });
    } finally {
      if (renderer) {
        const activeRenderer = renderer;
        act(() => activeRenderer.unmount());
      }
      if (previousWindow) {
        Object.defineProperty(globalThis, 'window', previousWindow);
      } else {
        Reflect.deleteProperty(globalThis, 'window');
      }
    }
  });
});
