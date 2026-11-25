import {
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
  type PointerEvent as ReactPointerEvent,
  type PropsWithChildren,
  type Ref,
  type ReactNode,
} from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';
import { IconX } from './icons';
import {
  acquireOverlayScrollLock,
  getOverlayFocusableElements,
  hasOpenModal,
  isTopmostDrawer,
  registerOpenDrawer,
} from './overlayEscapeStack';
import styles from './Drawer.module.scss';

interface DrawerProps {
  open: boolean;
  title?: ReactNode;
  onClose: () => void;
  /** 返回 false 时取消本次关闭；异步检查会在关闭动画开始前完成。 */
  onBeforeClose?: () => boolean | Promise<boolean>;
  footer?: ReactNode;
  /** 桌面端面板宽度，移动端自动转为底部全宽弹层 */
  width?: number | string;
  className?: string;
  /** 可选的正文滚动容器引用，供需要在内容切换时归零滚动位置的调用方使用。 */
  bodyRef?: Ref<HTMLDivElement>;
  closeLabel?: string;
  closeDisabled?: boolean;
}

type DrawerPhase = 'closed' | 'entering' | 'closing';

const CLOSE_ANIMATION_DURATION = 280;

export function Drawer({
  open,
  title,
  onClose,
  onBeforeClose,
  footer,
  width = 420,
  className,
  bodyRef,
  closeLabel,
  closeDisabled = false,
  children,
}: PropsWithChildren<DrawerProps>) {
  const { t } = useTranslation();
  const titleId = useId();
  const resolvedCloseLabel = closeLabel || t('common.close');
  const [phase, setPhase] = useState<DrawerPhase>('closed');
  const phaseRef = useRef<DrawerPhase>(phase);
  phaseRef.current = phase;
  const closeTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const closeRequestPendingRef = useRef(false);
  const closeRequestTokenRef = useRef(0);
  const internalCloseCompletedRef = useRef(false);
  const previousOpenRef = useRef(open);
  const openRef = useRef(open);
  openRef.current = open;
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;
  const onBeforeCloseRef = useRef(onBeforeClose);
  onBeforeCloseRef.current = onBeforeClose;
  const panelRef = useRef<HTMLDivElement | null>(null);
  const previouslyFocusedRef = useRef<HTMLElement | null>(null);
  const overlayPointerIdsRef = useRef<Set<number>>(new Set());

  const getFocusableElements = useCallback(() => getOverlayFocusableElements(panelRef.current), []);

  const startClose = useCallback((notifyParent: boolean) => {
    if (closeTimerRef.current !== null || phaseRef.current === 'closed') return;
    setPhase('closing');
    closeTimerRef.current = globalThis.setTimeout(() => {
      closeTimerRef.current = null;
      if (notifyParent && openRef.current) {
        // Keep closing state until parent flips `open` to false so async `onClose`
        // (such as URL search-param updates) never flashes the drawer back open.
        internalCloseCompletedRef.current = true;
        onCloseRef.current();
      } else {
        internalCloseCompletedRef.current = false;
        setPhase('closed');
      }
    }, CLOSE_ANIMATION_DURATION);
  }, []);

  useEffect(() => {
    let cancelled = false;

    if (previousOpenRef.current !== open) {
      previousOpenRef.current = open;
      closeRequestTokenRef.current += 1;
      closeRequestPendingRef.current = false;
      overlayPointerIdsRef.current.clear();
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
    return () => {
      closeRequestTokenRef.current += 1;
      if (closeTimerRef.current !== null) {
        globalThis.clearTimeout(closeTimerRef.current);
      }
    };
  }, []);

  const handleClose = useCallback(async () => {
    if (
      closeDisabled ||
      closeTimerRef.current !== null ||
      closeRequestPendingRef.current ||
      internalCloseCompletedRef.current
    ) {
      return;
    }

    closeRequestPendingRef.current = true;
    const requestToken = closeRequestTokenRef.current + 1;
    closeRequestTokenRef.current = requestToken;
    try {
      const guard = onBeforeCloseRef.current;
      const allowed = guard ? await guard() : true;
      if (closeRequestTokenRef.current !== requestToken || allowed === false) return;
      startClose(true);
    } catch {
      // 关闭前检查失败时保持抽屉打开，避免意外丢失用户输入。
    } finally {
      if (closeRequestTokenRef.current === requestToken) {
        closeRequestPendingRef.current = false;
      }
    }
  }, [closeDisabled, startClose]);

  // 按 pointerId 配对：仅当同一指针在遮罩上按下且在遮罩上释放时才关闭。
  // 避免「面板内拖选到遮罩释放」与多指针交错状态互相覆盖。
  const handleOverlayPointerDown = useCallback((event: ReactPointerEvent<HTMLDivElement>) => {
    if (event.target === event.currentTarget && event.button === 0) {
      overlayPointerIdsRef.current.add(event.pointerId);
    } else {
      overlayPointerIdsRef.current.delete(event.pointerId);
    }
  }, []);

  const handleOverlayPointerUp = useCallback(
    (event: ReactPointerEvent<HTMLDivElement>) => {
      const startedOnOverlay = overlayPointerIdsRef.current.delete(event.pointerId);

      if (startedOnOverlay && event.target === event.currentTarget && event.button === 0) {
        void handleClose();
      }
    },
    [handleClose]
  );

  const handleOverlayPointerCancel = useCallback((event: ReactPointerEvent<HTMLDivElement>) => {
    overlayPointerIdsRef.current.delete(event.pointerId);
  }, []);

  const isVisible = phase !== 'closed';
  const isClosing = phase === 'closing';
  const isActive = open || isVisible;

  useEffect(() => {
    if (!isActive) return;
    return acquireOverlayScrollLock('drawer');
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
    if (!open || typeof document === 'undefined' || typeof window === 'undefined') return;

    const focusTimer = globalThis.setTimeout(() => {
      if (typeof panelRef.current?.focus === 'function') {
        panelRef.current.focus();
      }
    }, 0);

    return () => globalThis.clearTimeout(focusTimer);
  }, [open]);

  const drawerTokenRef = useRef<symbol | null>(null);

  useEffect(() => {
    if (!isActive || typeof document === 'undefined') return;
    const unregisterDrawer = registerOpenDrawer();
    drawerTokenRef.current = unregisterDrawer.token;
    return () => {
      drawerTokenRef.current = null;
      unregisterDrawer();
    };
  }, [isActive]);

  useEffect(() => {
    if (!isActive || typeof document === 'undefined') return;

    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        const token = drawerTokenRef.current;
        if (
          !token ||
          hasOpenModal() ||
          event.defaultPrevented ||
          !isTopmostDrawer(token)
        ) {
          return;
        }
        event.preventDefault();
        event.stopImmediatePropagation?.();
        if (isClosing || !open) return;
        void handleClose();
        return;
      }

      if (event.key !== 'Tab') return;

      const token = drawerTokenRef.current;
      if (!token || hasOpenModal() || !isTopmostDrawer(token)) return;
      if (isClosing || !open) {
        event.preventDefault();
        return;
      }

      const focusableElements = getFocusableElements();
      if (focusableElements.length === 0) {
        event.preventDefault();
        if (typeof panelRef.current?.focus === 'function') {
          panelRef.current.focus();
        }
        return;
      }

      const firstElement = focusableElements[0];
      const lastElement = focusableElements[focusableElements.length - 1];
      const activeElement = document.activeElement as HTMLElement | null;
      const isInsideDrawer =
        activeElement !== null &&
        (activeElement === panelRef.current ||
          focusableElements.includes(activeElement) ||
          Boolean(
            typeof panelRef.current?.contains === 'function' &&
              panelRef.current.contains(activeElement)
          ));

      if (event.shiftKey) {
        if (
          !isInsideDrawer ||
          activeElement === firstElement ||
          activeElement === panelRef.current
        ) {
          event.preventDefault();
          lastElement.focus();
        }
        return;
      }

      if (!isInsideDrawer || activeElement === lastElement) {
        event.preventDefault();
        firstElement.focus();
      }
    };

    document.addEventListener('keydown', handleKeyDown);
    return () => {
      document.removeEventListener('keydown', handleKeyDown);
    };
  }, [getFocusableElements, handleClose, isActive, isClosing, open]);
  if (!isVisible) return null;

  const overlayClass = [
    styles.overlay,
    isClosing ? styles.overlayClosing : styles.overlayEntering,
  ].join(' ');
  const panelClass = [
    styles.panel,
    isClosing ? styles.panelClosing : styles.panelEntering,
    className,
  ]
    .filter(Boolean)
    .join(' ');

  const drawerContent = (
    <div
      className={overlayClass}
      onPointerDown={handleOverlayPointerDown}
      onPointerUp={handleOverlayPointerUp}
      onPointerCancel={handleOverlayPointerCancel}
    >
      <div
        ref={panelRef}
        className={panelClass}
        style={{ width }}
        role="dialog"
        aria-modal="true"
        aria-labelledby={title ? titleId : undefined}
        tabIndex={-1}
        onClick={(event) => event.stopPropagation()}
      >
        <div className={styles.header}>
          <div className={styles.title} id={title ? titleId : undefined}>
            {title}
          </div>
          <button
            type="button"
            className={styles.closeButton}
            disabled={closeDisabled}
            onClick={() => void handleClose()}
            aria-label={resolvedCloseLabel}
            title={resolvedCloseLabel}
          >
            <IconX size={18} />
          </button>
        </div>
        <div ref={bodyRef} className={styles.body}>
          {children}
        </div>
        {footer && <div className={styles.footer}>{footer}</div>}
      </div>
    </div>
  );

  if (typeof document === 'undefined') {
    return drawerContent;
  }

  return createPortal(drawerContent, document.body);
}
