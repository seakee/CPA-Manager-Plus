import { act, create, type ReactTestRenderer } from 'react-test-renderer';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { NavigationType, type BlockerFunction } from 'react-router-dom';
import { useUnsavedChangesGuard } from './useUnsavedChangesGuard';

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const { mocks } = vi.hoisted(() => ({
  mocks: {
    location: { pathname: '/accounts', search: '', hash: '', key: 'current', state: null },
    blocker: {
      state: 'unblocked' as 'unblocked' | 'blocked' | 'proceeding',
      location: undefined as
        { pathname: string; search: string; hash: string; key: string; state: null } | undefined,
      proceed: vi.fn(),
      reset: vi.fn(),
    },
    useBlocker: vi.fn<(shouldBlock: BlockerFunction) => void>(),
    showConfirmation: vi.fn(),
  },
}));

vi.mock('react-router-dom', () => ({
  NavigationType: { Pop: 'POP', Push: 'PUSH', Replace: 'REPLACE' },
  useLocation: () => mocks.location,
  // The same blocker object on every render, like a router that did not change
  // state; the guard must not re-prompt or re-render off that identity.
  useBlocker: (shouldBlock: BlockerFunction) => {
    mocks.useBlocker(shouldBlock);
    return mocks.blocker;
  },
}));

vi.mock('@/stores', () => ({
  useNotificationStore: () => ({ showConfirmation: mocks.showConfirmation }),
}));

const dialog = {
  title: 'Unsaved',
  message: 'Leave?',
  confirmText: 'Leave',
  cancelText: 'Stay',
};

function Harness({
  onConfirmNavigation,
  enabled,
  shouldBlock = true,
}: {
  onConfirmNavigation?: () => boolean | void | Promise<boolean | void>;
  enabled?: boolean;
  shouldBlock?: boolean;
}) {
  const { blocker } = useUnsavedChangesGuard({
    enabled,
    shouldBlock,
    dialog,
    onConfirmNavigation,
  });
  return <>{blocker}</>;
}

const rerender = async (
  renderer: ReactTestRenderer,
  onConfirmNavigation?: () => boolean | void | Promise<boolean | void>
) => {
  await act(async () => {
    renderer.update(<Harness onConfirmNavigation={onConfirmNavigation} />);
    await Promise.resolve();
  });
};

const blockNavigation = async (
  renderer: ReactTestRenderer,
  onConfirmNavigation?: () => boolean | void | Promise<boolean | void>
) => {
  mocks.blocker.state = 'blocked';
  mocks.blocker.location = {
    pathname: '/settings',
    search: '',
    hash: '',
    key: 'next',
    state: null,
  };
  await rerender(renderer, onConfirmNavigation);
  const confirmation = mocks.showConfirmation.mock.calls[0]?.[0] as {
    onConfirm: () => void;
  };
  if (!confirmation) throw new Error('confirmation missing');
  return confirmation;
};

describe('useUnsavedChangesGuard', () => {
  beforeEach(() => {
    mocks.blocker.state = 'unblocked';
    mocks.blocker.location = undefined;
    mocks.blocker.proceed.mockClear();
    mocks.blocker.reset.mockClear();
    mocks.useBlocker.mockClear();
    mocks.showConfirmation.mockClear();
  });

  it('runs the discard callback before proceeding', async () => {
    const onConfirmNavigation = vi.fn(() => true);
    let renderer!: ReactTestRenderer;
    act(() => {
      renderer = create(<Harness onConfirmNavigation={onConfirmNavigation} />);
    });
    const confirmation = await blockNavigation(renderer, onConfirmNavigation);

    await act(async () => {
      confirmation.onConfirm();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(onConfirmNavigation).toHaveBeenCalledTimes(1);
    expect(mocks.blocker.proceed).toHaveBeenCalledTimes(1);
    expect(mocks.blocker.reset).not.toHaveBeenCalled();
  });

  it('resets the blocker when the discard callback refuses or fails', async () => {
    for (const onConfirmNavigation of [
      vi.fn(() => false),
      vi.fn(async () => {
        throw new Error('discard failed');
      }),
    ]) {
      mocks.blocker.state = 'unblocked';
      mocks.blocker.location = undefined;
      mocks.blocker.reset.mockClear();
      mocks.showConfirmation.mockClear();
      let renderer!: ReactTestRenderer;
      act(() => {
        renderer = create(<Harness onConfirmNavigation={onConfirmNavigation} />);
      });
      const confirmation = await blockNavigation(renderer, onConfirmNavigation);

      await act(async () => {
        confirmation.onConfirm();
        await Promise.resolve();
        await Promise.resolve();
      });

      expect(mocks.blocker.reset).toHaveBeenCalledTimes(1);
      expect(mocks.blocker.proceed).not.toHaveBeenCalled();
      act(() => renderer.unmount());
    }
  });

  it('prompts once per blocked location, not once per render', async () => {
    let renderer!: ReactTestRenderer;
    act(() => {
      renderer = create(<Harness />);
    });
    await blockNavigation(renderer);
    expect(mocks.showConfirmation).toHaveBeenCalledTimes(1);

    // A router that re-renders without changing state must not re-prompt, and
    // must not loop: nothing here feeds a blocker value back into state.
    await rerender(renderer);
    await rerender(renderer);
    expect(mocks.showConfirmation).toHaveBeenCalledTimes(1);
    act(() => renderer.unmount());
  });

  it('registers no router blocker while the guard is disabled', () => {
    let renderer!: ReactTestRenderer;
    act(() => {
      renderer = create(<Harness enabled={false} />);
    });

    // A registered blocker alone makes react-router warn on every manual hash
    // edit, so a clean page must not register one at all.
    expect(mocks.useBlocker).not.toHaveBeenCalled();
    expect(mocks.showConfirmation).not.toHaveBeenCalled();
    act(() => renderer.unmount());
  });

  it('registers the router blocker once the page has unsaved changes', () => {
    let renderer!: ReactTestRenderer;
    act(() => {
      renderer = create(<Harness enabled />);
    });

    expect(mocks.useBlocker).toHaveBeenCalled();
    act(() => renderer.unmount());
  });

  it('never blocks when the page reports nothing unsaved', () => {
    let renderer!: ReactTestRenderer;
    act(() => {
      renderer = create(<Harness enabled shouldBlock={false} />);
    });

    const shouldBlockFunction = mocks.useBlocker.mock.calls[0]?.[0];
    expect(shouldBlockFunction).toBeDefined();
    expect(
      shouldBlockFunction?.({
        currentLocation: mocks.location,
        nextLocation: { ...mocks.location, pathname: '/codex-inspection' },
        historyAction: NavigationType.Pop,
      })
    ).toBe(false);
    act(() => renderer.unmount());
  });
});
