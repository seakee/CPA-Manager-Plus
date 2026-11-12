import { act, type ReactNode } from 'react';
import { create, type ReactTestRenderer } from 'react-test-renderer';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { Button } from '@/components/ui/Button';
import { useNotificationStore } from '@/stores';
import { ConfirmationModal } from './ConfirmationModal';

vi.mock('react-i18next', () => ({
  initReactI18next: {
    type: '3rdParty',
    init: vi.fn(),
  },
  useTranslation: () => ({
    t: (key: string) => key,
  }),
}));

vi.mock('@/components/ui/Modal', () => ({
  Modal: ({ children, open }: { children: ReactNode; open: boolean }) =>
    open ? <div>{children}</div> : null,
}));

const renderModal = () => {
  let renderer!: ReactTestRenderer;
  act(() => {
    renderer = create(<ConfirmationModal />);
  });
  return renderer;
};

const clickButton = async (renderer: ReactTestRenderer, label: string) => {
  const button = renderer.root
    .findAllByType(Button)
    .find((candidate) => candidate.props.children === label);
  if (!button) throw new Error(`Button not found: ${label}`);
  await act(async () => {
    await button.props.onClick();
  });
};

const findButton = (renderer: ReactTestRenderer, label: string) => {
  const button = renderer.root
    .findAllByType(Button)
    .find((candidate) => candidate.props.children === label);
  if (!button) throw new Error(`Button not found: ${label}`);
  return button;
};

/** A promise the test settles by hand, so "still awaiting" is observable. */
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

/**
 * Shows a prompt that replaces whatever is on screen. A loading prompt refuses
 * to be replaced, so the caller takes it down first — which is what a guard's
 * own cancel path does — and then the next action asks its question.
 */
function replaceWithAnotherPrompt() {
  const nextConfirm = vi.fn();
  const nextCancel = vi.fn();
  act(() => {
    useNotificationStore.getState().showConfirmation({
      title: 'Delete key?',
      message: 'Another action is waiting.',
      confirmText: 'Delete',
      cancelText: 'Cancel',
      onConfirm: nextConfirm,
      onCancel: nextCancel,
    });
  });
  return { options: useNotificationStore.getState().confirmation.options, nextConfirm, nextCancel };
}

beforeEach(() => {
  useNotificationStore.setState({
    confirmation: {
      isOpen: false,
      isLoading: false,
      step: 1,
      options: null,
    },
  });
});

/**
 * The modal's handlers outlive the prompt that created them: a page that asked
 * can unmount, and another action can put its own question on screen while the
 * first `onConfirm` is still running. Whatever the first one settles to, it
 * must not touch the prompt that replaced it.
 */
describe('a confirmation whose prompt was replaced', () => {
  it.each([
    ['resolves', (pending: ReturnType<typeof deferred<void>>) => pending.resolve()],
    [
      'rejects',
      (pending: ReturnType<typeof deferred<void>>) => pending.reject(new Error('discard failed')),
    ],
    [
      'throws',
      (pending: ReturnType<typeof deferred<void>>) => pending.reject(new Error('threw')),
    ],
  ])('leaves the replacement usable when the first one %s', async (_label, settle) => {
    const pending = deferred<void>();
    const firstConfirm = vi.fn(() => pending.promise);
    useNotificationStore.getState().showConfirmation({
      title: 'Unsaved',
      message: 'Leave?',
      confirmText: 'Leave',
      cancelText: 'Stay',
      onConfirm: firstConfirm,
    });
    const renderer = renderModal();

    // Click without awaiting: the handler is deliberately still in flight.
    const button = findButton(renderer, 'Leave');
    await act(async () => {
      void button.props.onClick();
      await Promise.resolve();
    });
    expect(firstConfirm).toHaveBeenCalledTimes(1);
    expect(useNotificationStore.getState().confirmation.isLoading).toBe(true);

    // The loading prompt cannot be replaced, so it is taken down first.
    act(() => {
      useNotificationStore.getState().hideConfirmation();
    });
    const replacement = replaceWithAnotherPrompt();
    expect(useNotificationStore.getState().confirmation.isLoading).toBe(false);

    // Put the replacement into its own in-flight state, so the replaced
    // handler's cleanup is visible if it touches a prompt it no longer owns:
    // clearing that flag would re-enable buttons whose action is still running.
    act(() => {
      useNotificationStore.getState().setConfirmationLoading(true);
    });

    await act(async () => {
      settle(pending);
      await pending.promise.catch(() => undefined);
      await Promise.resolve();
      await Promise.resolve();
    });

    const shown = useNotificationStore.getState().confirmation;
    expect(shown.isOpen).toBe(true);
    expect(shown.options).toBe(replacement.options);
    expect(shown.isLoading).toBe(true);
    expect(replacement.nextConfirm).not.toHaveBeenCalled();
    expect(replacement.nextCancel).not.toHaveBeenCalled();
  });

  it('does not run a stale handler or mark the replacement loading', async () => {
    const firstConfirm = vi.fn();
    useNotificationStore.getState().showConfirmation({
      title: 'Unsaved',
      message: 'Leave?',
      confirmText: 'Leave',
      cancelText: 'Stay',
      onConfirm: firstConfirm,
    });
    const renderer = renderModal();
    // The click handler this prompt handed to its button, kept past its life.
    const staleClick = findButton(renderer, 'Leave').props.onClick as () => Promise<void>;

    act(() => {
      useNotificationStore.getState().hideConfirmation();
    });
    const replacement = replaceWithAnotherPrompt();

    await act(async () => {
      void staleClick();
      await Promise.resolve();
      await Promise.resolve();
    });

    const shown = useNotificationStore.getState().confirmation;
    expect(firstConfirm).not.toHaveBeenCalled();
    expect(shown.isOpen).toBe(true);
    expect(shown.options).toBe(replacement.options);
    expect(shown.isLoading).toBe(false);
    expect(replacement.nextConfirm).not.toHaveBeenCalled();
    expect(replacement.nextCancel).not.toHaveBeenCalled();
  });
});

describe('ConfirmationModal', () => {
  it('executes ordinary confirmations after one click', async () => {
    const onConfirm = vi.fn();
    useNotificationStore.getState().showConfirmation({
      message: 'Confirm once',
      confirmText: 'Run',
      onConfirm,
    });
    const renderer = renderModal();

    await clickButton(renderer, 'Run');

    expect(onConfirm).toHaveBeenCalledTimes(1);
    expect(useNotificationStore.getState().confirmation.isOpen).toBe(false);
  });

  it('requires the second confirmation before executing the action', async () => {
    const onConfirm = vi.fn();
    useNotificationStore.getState().showConfirmation({
      message: 'First warning',
      confirmText: 'Continue',
      secondConfirmation: {
        message: 'Final warning',
        confirmText: 'Delete permanently',
        variant: 'danger',
      },
      onConfirm,
    });
    const renderer = renderModal();

    await clickButton(renderer, 'Continue');

    expect(onConfirm).not.toHaveBeenCalled();
    expect(useNotificationStore.getState().confirmation.step).toBe(2);
    expect(renderer.root.findByProps({ children: 'Final warning' })).toBeTruthy();

    await clickButton(renderer, 'Delete permanently');

    expect(onConfirm).toHaveBeenCalledTimes(1);
    expect(useNotificationStore.getState().confirmation.isOpen).toBe(false);
  });

  it('cancels from the second step without executing the action', async () => {
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    useNotificationStore.getState().showConfirmation({
      message: 'First warning',
      confirmText: 'Continue',
      secondConfirmation: {
        message: 'Final warning',
        confirmText: 'Delete permanently',
      },
      onConfirm,
      onCancel,
    });
    const renderer = renderModal();

    await clickButton(renderer, 'Continue');
    await clickButton(renderer, 'common.cancel');

    expect(onConfirm).not.toHaveBeenCalled();
    expect(onCancel).toHaveBeenCalledTimes(1);
    expect(useNotificationStore.getState().confirmation).toMatchObject({
      isOpen: false,
      step: 1,
      options: null,
    });
  });

  it('starts a replacement confirmation from the first step', async () => {
    const replacedOnCancel = vi.fn();
    useNotificationStore.getState().showConfirmation({
      message: 'First warning',
      secondConfirmation: { message: 'Final warning' },
      onConfirm: vi.fn(),
      onCancel: replacedOnCancel,
    });
    useNotificationStore.getState().advanceConfirmation();

    useNotificationStore.getState().showConfirmation({
      message: 'Replacement warning',
      secondConfirmation: { message: 'Replacement final warning' },
      onConfirm: vi.fn(),
    });

    expect(replacedOnCancel).toHaveBeenCalledTimes(1);
    expect(useNotificationStore.getState().confirmation).toMatchObject({
      isOpen: true,
      isLoading: false,
      step: 1,
    });
    expect(useNotificationStore.getState().confirmation.options?.message).toBe(
      'Replacement warning'
    );
  });

  it('keeps a loading confirmation and cancels its replacement request', () => {
    const activeOnCancel = vi.fn();
    const replacementOnCancel = vi.fn();
    const activeOptions = {
      message: 'Running action',
      onConfirm: vi.fn(),
      onCancel: activeOnCancel,
    };
    useNotificationStore.getState().showConfirmation(activeOptions);
    useNotificationStore.getState().setConfirmationLoading(true);

    useNotificationStore.getState().showConfirmation({
      message: 'Replacement warning',
      onConfirm: vi.fn(),
      onCancel: replacementOnCancel,
    });

    expect(activeOnCancel).not.toHaveBeenCalled();
    expect(replacementOnCancel).toHaveBeenCalledTimes(1);
    expect(useNotificationStore.getState().confirmation).toMatchObject({
      isOpen: true,
      isLoading: true,
      step: 1,
      options: activeOptions,
    });
  });
});
