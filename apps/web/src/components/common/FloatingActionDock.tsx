import { useEffect, useLayoutEffect, useRef, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { usePageTransitionLayer } from './PageTransitionLayer';
import styles from './FloatingActionDock.module.scss';

const DOCK_HEIGHT_VAR = '--page-floating-dock-height';
const activeDockHeights = new Map<symbol, number>();

function syncGlobalDockHeight() {
  if (typeof document === 'undefined' || !document.documentElement?.style) return;
  let maxHeight = 0;
  for (const height of activeDockHeights.values()) {
    if (height > maxHeight) maxHeight = height;
  }
  if (maxHeight > 0) {
    document.documentElement.style.setProperty(DOCK_HEIGHT_VAR, `${Math.ceil(maxHeight)}px`);
  } else {
    document.documentElement.style.removeProperty(DOCK_HEIGHT_VAR);
  }
}

const useIsomorphicLayoutEffect = typeof window !== 'undefined' ? useLayoutEffect : useEffect;

export type FloatingActionDockProps = {
  open: boolean;
  leading?: ReactNode;
  children?: ReactNode;
  variant?: 'bar' | 'compact';
  className?: string;
  surfaceClassName?: string;
  role?: 'region' | 'toolbar' | 'status';
  ariaLabel?: string;
  testId?: string;
};

export function FloatingActionDock({
  open,
  leading,
  children,
  variant = 'bar',
  className = '',
  surfaceClassName = '',
  role = 'region',
  ariaLabel,
  testId,
}: FloatingActionDockProps) {
  const pageTransitionLayer = usePageTransitionLayer();
  const isCurrentLayer = pageTransitionLayer ? pageTransitionLayer.isCurrentLayer : true;
  const shouldRender = open && isCurrentLayer;
  const surfaceRef = useRef<HTMLDivElement | null>(null);
  const idRef = useRef<symbol | null>(null);
  if (!idRef.current) {
    idRef.current = Symbol('floating-action-dock');
  }

  useIsomorphicLayoutEffect(() => {
    const dockId = idRef.current!;
    if (!shouldRender) {
      if (activeDockHeights.delete(dockId)) {
        syncGlobalDockHeight();
      }
      return;
    }

    const updateHeight = () => {
      const measured = surfaceRef.current?.getBoundingClientRect().height ?? 56;
      activeDockHeights.set(dockId, measured > 0 ? measured + 16 : 64);
      syncGlobalDockHeight();
    };

    updateHeight();
    if (typeof window !== 'undefined' && typeof window.addEventListener === 'function') {
      window.addEventListener('resize', updateHeight);
    }
    const element = surfaceRef.current;
    const resizeObserver =
      typeof ResizeObserver !== 'undefined' && element ? new ResizeObserver(updateHeight) : null;
    resizeObserver?.observe(element!);

    return () => {
      resizeObserver?.disconnect();
      if (typeof window !== 'undefined' && typeof window.removeEventListener === 'function') {
        window.removeEventListener('resize', updateHeight);
      }
      activeDockHeights.delete(dockId);
      syncGlobalDockHeight();
    };
  }, [shouldRender]);

  if (!shouldRender) {
    return null;
  }

  const containerClasses = [
    styles.container,
    variant === 'bar' ? styles.containerBar : styles.containerCompact,
    className,
  ]
    .filter(Boolean)
    .join(' ');
  const surfaceClasses = [
    styles.surface,
    variant === 'bar' ? styles.surfaceBar : styles.surfaceCompact,
    surfaceClassName,
  ]
    .filter(Boolean)
    .join(' ');

  const node = (
    <div className={containerClasses} role={role} aria-label={ariaLabel} data-testid={testId}>
      <div className={surfaceClasses} ref={surfaceRef}>
        {leading !== null && leading !== undefined && leading !== false ? (
          <div className={styles.leading}>{leading}</div>
        ) : null}
        {children !== null && children !== undefined && children !== false ? (
          <div className={styles.actions}>{children}</div>
        ) : null}
      </div>
    </div>
  );

  return typeof document !== 'undefined' && document.body
    ? createPortal(node, document.body)
    : node;
}
