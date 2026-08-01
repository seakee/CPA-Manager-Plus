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
    useNotificationStore.getState().showConfirmation({
      message: 'First warning',
      secondConfirmation: { message: 'Final warning' },
      onConfirm: vi.fn(),
    });
    useNotificationStore.getState().advanceConfirmation();

    useNotificationStore.getState().showConfirmation({
      message: 'Replacement warning',
      secondConfirmation: { message: 'Replacement final warning' },
      onConfirm: vi.fn(),
    });

    expect(useNotificationStore.getState().confirmation).toMatchObject({
      isOpen: true,
      isLoading: false,
      step: 1,
    });
    expect(useNotificationStore.getState().confirmation.options?.message).toBe(
      'Replacement warning'
    );
  });
});
