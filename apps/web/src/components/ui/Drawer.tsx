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

const CLOSE_ANIMATION_DURATION = 280;

let activeDrawerCount = 0;
const drawerScrollSnapshot = { bodyOverflow: '', htmlOverflow: '' };

const lockScroll = () => {
  if (typeof document === 'undefined') return;
  if (activeDrawerCount === 0) {
    drawerScrollSnapshot.bodyOverflow = document.body.style.overflow;
    drawerScrollSnapshot.htmlOverflow = document.documentElement.style.overflow;
    document.body.style.overflow = 'hidden';
    document.documentElement.style.overflow = 'hidden';
  }
  activeDrawerCount += 1;
};

const unlockScroll = () => {
  if (typeof document === 'undefined') return;
  activeDrawerCount = Math.max(0, activeDrawerCount - 1);
  if (activeDrawerCount === 0) {
    document.body.style.overflow = drawerScrollSnapshot.bodyOverflow;
    document.documentElement.style.overflow = drawerScrollSnapshot.htmlOverflow;
  }
};

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
  const [isVisible, setIsVisible] = useState(false);
  const [isClosing, setIsClosing] = useState(false);
  const closeTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const closeRequestPendingRef = useRef(false);
  const closeRequestTokenRef = useRef(0);
  const previousOpenRef = useRef(open);
  const panelRef = useRef<HTMLDivElement | null>(null);
  const previouslyFocusedRef = useRef<HTMLElement | null>(null);
  const overlayPointerIdsRef = useRef<Set<number>>(new Set());

  const startClose = useCallback(
    (notifyParent: boolean) => {
      if (closeTimerRef.current !== null) return;
      setIsClosing(true);
      closeTimerRef.current = globalThis.setTimeout(() => {
        setIsVisible(false);
        setIsClosing(false);
        closeTimerRef.current = null;
        if (notifyParent) {
          onClose();
        }
      }, CLOSE_ANIMATION_DURATION);
    },
    [onClose]
  );

  useEffect(() => {
    let cancelled = false;

    if (previousOpenRef.current !== open) {
      previousOpenRef.current = open;
      closeRequestTokenRef.current += 1;
      closeRequestPendingRef.current = false;
      overlayPointerIdsRef.current.clear();
    }

    if (open) {
      if (closeTimerRef.current !== null) {
        globalThis.clearTimeout(closeTimerRef.current);
        closeTimerRef.current = null;
      }
      queueMicrotask(() => {
        if (cancelled) return;
        setIsVisible(true);
        setIsClosing(false);
      });
    } else if (isVisible) {
      queueMicrotask(() => {
        if (cancelled) return;
        startClose(false);
      });
    }

    return () => {
      cancelled = true;
    };
  }, [open, isVisible, startClose]);

  useEffect(() => {
    return () => {
      closeRequestTokenRef.current += 1;
      if (closeTimerRef.current !== null) {
        globalThis.clearTimeout(closeTimerRef.current);
      }
    };
  }, []);

  const handleClose = useCallback(async () => {
    if (closeDisabled || closeTimerRef.current !== null || closeRequestPendingRef.current) return;

    closeRequestPendingRef.current = true;
    const requestToken = closeRequestTokenRef.current + 1;
    closeRequestTokenRef.current = requestToken;
    try {
      const allowed = onBeforeClose ? await onBeforeClose() : true;
      if (closeRequestTokenRef.current !== requestToken || allowed === false) return;
      startClose(true);
    } catch {
      // 关闭前检查失败时保持抽屉打开，避免意外丢失用户输入。
    } finally {
      if (closeRequestTokenRef.current === requestToken) {
        closeRequestPendingRef.current = false;
      }
    }
  }, [closeDisabled, onBeforeClose, startClose]);

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

  const shouldLockScroll = open || isVisible;

  useEffect(() => {
    if (!shouldLockScroll) return;
    lockScroll();
    return () => unlockScroll();
  }, [shouldLockScroll]);

  useEffect(() => {
    if (!open || typeof document === 'undefined' || typeof window === 'undefined') return;

    previouslyFocusedRef.current =
      document.activeElement instanceof HTMLElement ? document.activeElement : null;

    const focusTimer = globalThis.setTimeout(() => {
      panelRef.current?.focus();
    }, 0);

    return () => globalThis.clearTimeout(focusTimer);
  }, [open]);

  useEffect(() => {
    if (open || isVisible || typeof document === 'undefined') return;
    previouslyFocusedRef.current?.focus();
    previouslyFocusedRef.current = null;
  }, [isVisible, open]);

  useEffect(() => {
    if (!open || typeof document === 'undefined') return;

    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault();
        void handleClose();
      }
    };

    document.addEventListener('keydown', handleKeyDown);
    return () => document.removeEventListener('keydown', handleKeyDown);
  }, [handleClose, open]);

  if (!open && !isVisible) return null;

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
