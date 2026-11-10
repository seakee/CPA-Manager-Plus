import { createElement, useCallback, useEffect, useRef, type ReactNode } from 'react';
import type { Blocker, BlockerFunction } from 'react-router-dom';
import { useBlocker, useLocation } from 'react-router-dom';
import { useNotificationStore } from '@/stores';

type ConfirmationVariant = 'danger' | 'primary' | 'secondary';

export type UnsavedChangesDialog = {
  title: string;
  message: string;
  confirmText: string;
  cancelText: string;
  variant?: ConfirmationVariant;
};

export type UseUnsavedChangesGuardOptions = {
  enabled?: boolean;
  shouldBlock: boolean | BlockerFunction;
  dialog: UnsavedChangesDialog;
  onConfirmNavigation?: () => boolean | void | Promise<boolean | void>;
};

/**
 * The location a blocked blocker is holding, or '' when nothing is pending.
 * Deliberately derived from the blocker's state and location, never from the
 * blocker object identity: react-router hands out a new object on every state
 * change, so identity is not a stable signal to render or compare against.
 */
const blockedLocationKey = (blocker: Blocker): string => {
  if (blocker.state !== 'blocked' || !blocker.location) return '';
  const { pathname, search, hash } = blocker.location;
  return `${pathname}${search}${hash}`;
};

/**
 * Registers the router blocker, and only while the guard is rendered.
 *
 * react-router warns about "a blocker on a POP navigation" for every manual
 * address-bar edit as soon as one blocker is registered, even one that would
 * never block, and a hash router sees those edits as exactly that POP
 * navigation. Registration therefore lives in this child component so a page
 * with nothing unsaved registers nothing at all.
 *
 * The discard prompt also lives here: the blocker never leaves this component,
 * so no render can feed a new blocker value back into state and re-trigger
 * itself.
 */
function BlockerRegistration({
  shouldBlock,
  dialog,
  onConfirmNavigation,
  showConfirmation,
}: {
  shouldBlock: BlockerFunction;
  dialog: UnsavedChangesDialog;
  onConfirmNavigation?: () => boolean | void | Promise<boolean | void>;
  showConfirmation: (options: {
    title?: string;
    message: ReactNode;
    confirmText?: string;
    cancelText?: string;
    variant?: ConfirmationVariant;
    onConfirm: () => void | Promise<void>;
    onCancel?: () => void;
  }) => void;
}) {
  const blocker = useBlocker(shouldBlock);
  const blockedKey = blockedLocationKey(blocker);
  const lastBlockedRef = useRef('');

  useEffect(() => {
    if (!blockedKey) {
      lastBlockedRef.current = '';
      return;
    }
    // One prompt per blocked location: the effect re-runs whenever the router
    // hands out a fresh blocker object, which must not open it twice.
    if (lastBlockedRef.current === blockedKey) return;
    lastBlockedRef.current = blockedKey;

    const settle = (action: 'proceed' | 'reset') => {
      if (blocker.state !== 'blocked') return;
      if (action === 'proceed') {
        blocker.proceed();
        return;
      }
      blocker.reset();
    };

    showConfirmation({
      title: dialog.title,
      message: dialog.message,
      confirmText: dialog.confirmText,
      cancelText: dialog.cancelText,
      variant: dialog.variant ?? 'danger',
      onConfirm: () => {
        void Promise.resolve(onConfirmNavigation?.())
          .then((allowed) => settle(allowed === false ? 'reset' : 'proceed'))
          .catch(() => settle('reset'));
      },
      onCancel: () => settle('reset'),
    });
  }, [blockedKey, blocker, dialog, onConfirmNavigation, showConfirmation]);

  return null;
}

/**
 * Guards a page's unsaved edits against navigation.
 *
 * The caller MUST render the returned `blocker` node: it holds the router
 * registration, and the guard cannot block anything while it is not rendered.
 */
export function useUnsavedChangesGuard(options: UseUnsavedChangesGuardOptions): {
  allowNextNavigation: () => void;
  allowNavigationTo: (target: string) => void;
  blocker: ReactNode;
} {
  const { enabled = true, shouldBlock, dialog, onConfirmNavigation } = options;
  const { showConfirmation } = useNotificationStore();
  const allowNextNavigationUntilRef = useRef(0);
  const allowNextNavigationKeyRef = useRef('');
  const location = useLocation();

  const allowNextNavigation = useCallback(() => {
    // Allow one programmatic navigation after successful save.
    // A short window is used to avoid stale flags lingering when no navigation happens.
    allowNextNavigationUntilRef.current = Date.now() + 2_000;
    allowNextNavigationKeyRef.current = '';
  }, []);

  const allowNavigationTo = useCallback((target: string) => {
    allowNextNavigationUntilRef.current = Date.now() + 2_000;
    allowNextNavigationKeyRef.current = target;
  }, []);

  const shouldBlockFunction = useCallback<BlockerFunction>(
    (args) => {
      if (!enabled) return false;
      const now = Date.now();

      if (allowNextNavigationUntilRef.current > now) {
        const nextKey = `${args.nextLocation.pathname}${args.nextLocation.search}${args.nextLocation.hash}`;
        if (!allowNextNavigationKeyRef.current) {
          allowNextNavigationKeyRef.current = nextKey;
        }
        if (allowNextNavigationKeyRef.current === nextKey) {
          return false;
        }
      } else if (allowNextNavigationUntilRef.current !== 0) {
        allowNextNavigationUntilRef.current = 0;
        allowNextNavigationKeyRef.current = '';
      }

      return typeof shouldBlock === 'function' ? shouldBlock(args) : shouldBlock;
    },
    [enabled, shouldBlock]
  );

  useEffect(() => {
    if (allowNextNavigationUntilRef.current === 0) return;
    allowNextNavigationUntilRef.current = 0;
    allowNextNavigationKeyRef.current = '';
  }, [location.key]);

  const blocker = enabled
    ? // The blocker function reads the one-navigation window only inside router
      // callbacks, never during render; the rule cannot see that boundary.
      // eslint-disable-next-line react-hooks/refs
      createElement(BlockerRegistration, {
        shouldBlock: shouldBlockFunction,
        dialog,
        onConfirmNavigation,
        showConfirmation,
      })
    : null;

  return { allowNextNavigation, allowNavigationTo, blocker };
}
