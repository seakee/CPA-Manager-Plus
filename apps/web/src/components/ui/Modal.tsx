import {
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
  type PropsWithChildren,
  type ReactNode,
} from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';
import { IconX } from './icons';
import {
  acquireOverlayScrollLock,
  getOverlayFocusableElements,
  isTopmostModal,
  registerOpenModal,
} from './overlayEscapeStack';

interface ModalProps {
  open: boolean;
  title?: ReactNode;
  onClose: () => void;
  footer?: ReactNode;
  width?: number | string;
  className?: string;
  closeDisabled?: boolean;
}

type ModalPhase = 'closed' | 'entering' | 'closing';

const CLOSE_ANIMATION_DURATION = 350;
const CLOSE_RECONCILE_DELAY = 100;

export function Modal({
  open,
  title,
  onClose,
  footer,
  width = 520,
  className,
  closeDisabled = false,
  children,
}: PropsWithChildren<ModalProps>) {
  const { t } = useTranslation();
  const titleId = useId();
  const [phase, setPhase] = useState<ModalPhase>('closed');
  const phaseRef = useRef<ModalPhase>(phase);
  phaseRef.current = phase;
  const closeTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const reconcileTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const internalCloseCompletedRef = useRef(false);
  const openRef = useRef(open);
  openRef.current = open;
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;
  const closeDisabledRef = useRef(closeDisabled);
  closeDisabledRef.current = closeDisabled;
  const modalRef = useRef<HTMLDivElement | null>(null);
  const closeButtonRef = useRef<HTMLButtonElement | null>(null);
  const previouslyFocusedRef = useRef<HTMLElement | null>(null);

  const getFocusableElements = useCallback(() => getOverlayFocusableElements(modalRef.current), []);

  const startClose = useCallback((notifyParent: boolean) => {
    if (closeTimerRef.current !== null || (!openRef.current && phaseRef.current === 'closed')) {
      return;
    }
    if (reconcileTimerRef.current !== null) {
      globalThis.clearTimeout(reconcileTimerRef.current);
      reconcileTimerRef.current = null;
    }
    setPhase('closing');
    closeTimerRef.current = globalThis.setTimeout(() => {
      closeTimerRef.current = null;
      if (notifyParent && closeDisabledRef.current && openRef.current) {
        internalCloseCompletedRef.current = false;
        setPhase('entering');
        return;
      }
      if (notifyParent && openRef.current) {
        // Keep closing state until parent flips `open` to false so async `onClose`
        // never flashes the modal back open or hides prematurely. If the parent
        // declines to close, reconcile back to `entering` after a short grace window.
        internalCloseCompletedRef.current = true;
        try {
          onCloseRef.current();
        } finally {
          if (openRef.current && internalCloseCompletedRef.current) {
            reconcileTimerRef.current = globalThis.setTimeout(() => {
              reconcileTimerRef.current = null;
              if (openRef.current && internalCloseCompletedRef.current) {
                internalCloseCompletedRef.current = false;
                setPhase('entering');
              }
            }, CLOSE_RECONCILE_DELAY);
          }
        }
      } else {
        internalCloseCompletedRef.current = false;
        setPhase('closed');
      }
    }, CLOSE_ANIMATION_DURATION);
  }, []);

  useEffect(() => {
    let cancelled = false;
    if (reconcileTimerRef.current !== null) {
      globalThis.clearTimeout(reconcileTimerRef.current);
      reconcileTimerRef.current = null;
    }

    if (open) {
      internalCloseCompletedRef.current = false;
      if (closeTimerRef.current !== null) {
        globalThis.clearTimeout(closeTimerRef.current);
        closeTimerRef.current = null;
      }
      queueMicrotask(() => {
        if (cancelled) return;
        setPhase('entering');
      });
    } else if (internalCloseCompletedRef.current) {
      internalCloseCompletedRef.current = false;
      queueMicrotask(() => {
        if (cancelled) return;
        setPhase('closed');
      });
    } else if (
      phaseRef.current === 'entering' ||
      (phaseRef.current === 'closing' && closeTimerRef.current === null)
    ) {
      queueMicrotask(() => {
        if (cancelled) return;
        startClose(false);
      });
    }

    return () => {
      cancelled = true;
    };
  }, [open, startClose]);

  useEffect(() => {
    if (!closeDisabled || !open) return;
    let cancelled = false;
    const wasClosing =
      phaseRef.current === 'closing' ||
      closeTimerRef.current !== null ||
      internalCloseCompletedRef.current;
    if (closeTimerRef.current !== null) {
      globalThis.clearTimeout(closeTimerRef.current);
      closeTimerRef.current = null;
    }
    if (reconcileTimerRef.current !== null) {
      globalThis.clearTimeout(reconcileTimerRef.current);
      reconcileTimerRef.current = null;
    }
    internalCloseCompletedRef.current = false;
    if (wasClosing) {
      queueMicrotask(() => {
        if (cancelled || !openRef.current) return;
        setPhase('entering');
      });
    }
    return () => {
      cancelled = true;
    };
  }, [closeDisabled, open]);

  const handleClose = useCallback(() => {
    if (closeDisabled || closeTimerRef.current !== null || internalCloseCompletedRef.current) {
      return;
    }
    startClose(true);
  }, [closeDisabled, startClose]);

  useEffect(() => {
    return () => {
      if (closeTimerRef.current !== null) {
        globalThis.clearTimeout(closeTimerRef.current);
      }
      if (reconcileTimerRef.current !== null) {
        globalThis.clearTimeout(reconcileTimerRef.current);
      }
    };
  }, []);

  const isVisible = phase !== 'closed';
  const isClosing = phase === 'closing';
  const isActive = open || isVisible;

  useEffect(() => {
    if (!isActive) return;
    return acquireOverlayScrollLock('modal');
  }, [isActive]);

  useEffect(() => {
    if (!isActive || typeof document === 'undefined') return;
    previouslyFocusedRef.current =
      document.activeElement instanceof HTMLElement ? document.activeElement : null;
    return () => {
      const target = previouslyFocusedRef.current;
      previouslyFocusedRef.current = null;
      if (typeof target?.focus === 'function') {
        target.focus();
      }
    };
  }, [isActive]);

  useEffect(() => {
    if (!open || typeof document === 'undefined') return;

    const focusTimer = globalThis.setTimeout(() => {
      const firstFocusable = getFocusableElements()[0];
      const target = firstFocusable ?? closeButtonRef.current ?? modalRef.current;
      if (typeof target?.focus === 'function') {
        target.focus();
      }
    }, 0);

    return () => {
      globalThis.clearTimeout(focusTimer);
    };
  }, [getFocusableElements, open]);

  const modalTokenRef = useRef<symbol | null>(null);

  useEffect(() => {
    if (!isActive || typeof document === 'undefined') return;
    const unregisterModal = registerOpenModal();
    modalTokenRef.current = unregisterModal.token;
    return () => {
      modalTokenRef.current = null;
      unregisterModal();
    };
  }, [isActive]);

  useEffect(() => {
    if (!isActive || typeof document === 'undefined') return;

    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        const token = modalTokenRef.current;
        if (!token || !isTopmostModal(token)) return;
        event.preventDefault();
        event.stopImmediatePropagation?.();
        if (closeDisabled || isClosing || !open) return;
        handleClose();
        return;
      }
      if (event.key !== 'Tab') return;

      const token = modalTokenRef.current;
      if (!token || !isTopmostModal(token)) return;
      if (isClosing || !open) {
        event.preventDefault();
        return;
      }

      const focusableElements = getFocusableElements();
      if (focusableElements.length === 0) {
        event.preventDefault();
        modalRef.current?.focus();
        return;
      }

      const firstElement = focusableElements[0];
      const lastElement = focusableElements[focusableElements.length - 1];
      const activeElement = document.activeElement as HTMLElement | null;
      const isInsideModal =
        activeElement !== null &&
        (activeElement === modalRef.current ||
          focusableElements.includes(activeElement) ||
          Boolean(
            typeof modalRef.current?.contains === 'function' &&
              modalRef.current.contains(activeElement)
          ));

      if (event.shiftKey) {
        if (
          !isInsideModal ||
          activeElement === firstElement ||
          activeElement === modalRef.current
        ) {
          event.preventDefault();
          lastElement.focus();
        }
        return;
      }

      if (!isInsideModal || activeElement === lastElement) {
        event.preventDefault();
        firstElement.focus();
      }
    };

    document.addEventListener('keydown', handleKeyDown);
    return () => {
      document.removeEventListener('keydown', handleKeyDown);
    };
  }, [closeDisabled, getFocusableElements, handleClose, isActive, isClosing, open]);

  if (!isActive) return null;
  const overlayClass = `modal-overlay ${isClosing ? 'modal-overlay-closing' : 'modal-overlay-entering'}`;
  const modalClass = `modal ${isClosing ? 'modal-closing' : 'modal-entering'}${className ? ` ${className}` : ''}`;

  const modalContent = (
    <div className={overlayClass}>
      <div
        ref={modalRef}
        className={modalClass}
        style={{ width, maxWidth: '100%' }}
        role="dialog"
        aria-modal="true"
        aria-labelledby={title ? titleId : undefined}
        tabIndex={-1}
      >
        <button
          ref={closeButtonRef}
          type="button"
          className="modal-close-floating"
          onClick={closeDisabled ? undefined : handleClose}
          aria-label={t('common.close')}
          disabled={closeDisabled}
        >
          <IconX size={20} />
        </button>
        <div className="modal-header">
          <div className="modal-title" id={title ? titleId : undefined}>
            {title}
          </div>
        </div>
        <div className="modal-body">{children}</div>
        {footer && <div className="modal-footer">{footer}</div>}
      </div>
    </div>
  );

  if (typeof document === 'undefined') {
    return modalContent;
  }

  return createPortal(modalContent, document.body);
}
