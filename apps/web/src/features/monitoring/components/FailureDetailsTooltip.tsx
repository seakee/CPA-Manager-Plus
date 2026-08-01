import {
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
  type CSSProperties,
  type FocusEvent,
  type KeyboardEvent,
  type ReactNode,
} from 'react';
import { createPortal } from 'react-dom';
import { IconCopy } from '@/components/ui/icons';
import styles from './FailureDetailsTooltip.module.scss';

const VIEWPORT_MARGIN = 12;
const TOOLTIP_OFFSET = 8;
const TOOLTIP_MAX_WIDTH = 420;
const TOOLTIP_MAX_HEIGHT = 240;
const CLOSE_DELAY_MS = 120;

type TooltipPlacement = 'above' | 'below';

type TooltipPosition = {
  placement: TooltipPlacement;
  style: CSSProperties;
};

type FailureDetailsTooltipProps = {
  children: ReactNode;
  ariaLabel: string;
  statusText?: string;
  detailLines: string[];
  copyText: string;
  copyLabel: string;
  tooltipId?: string;
  onCopy: (text: string) => void;
};

const clampNumber = (value: number, min: number, max: number) =>
  Math.min(Math.max(value, min), max);

const resolveTooltipPosition = (anchor: HTMLElement): TooltipPosition | null => {
  if (typeof window === 'undefined') return null;

  const rect = anchor.getBoundingClientRect();
  const viewportWidth = window.innerWidth;
  const viewportHeight = window.innerHeight;
  const maxWidth = Math.max(
    220,
    Math.min(TOOLTIP_MAX_WIDTH, Math.max(0, viewportWidth - VIEWPORT_MARGIN * 2))
  );
  const left = clampNumber(
    rect.left,
    VIEWPORT_MARGIN,
    Math.max(VIEWPORT_MARGIN, viewportWidth - maxWidth - VIEWPORT_MARGIN)
  );
  const spaceBelow = viewportHeight - rect.bottom - VIEWPORT_MARGIN - TOOLTIP_OFFSET;
  const spaceAbove = rect.top - VIEWPORT_MARGIN - TOOLTIP_OFFSET;
  const placement: TooltipPlacement =
    spaceBelow >= TOOLTIP_MAX_HEIGHT || spaceBelow >= spaceAbove ? 'below' : 'above';
  const availableHeight = Math.max(0, placement === 'below' ? spaceBelow : spaceAbove);
  const maxHeight = Math.min(TOOLTIP_MAX_HEIGHT, availableHeight);
  const baseStyle: CSSProperties = {
    left,
    maxHeight,
    maxWidth,
  };

  return placement === 'below'
    ? {
        placement,
        style: {
          ...baseStyle,
          top: rect.bottom + TOOLTIP_OFFSET,
        },
      }
    : {
        placement,
        style: {
          ...baseStyle,
          bottom: viewportHeight - rect.top + TOOLTIP_OFFSET,
        },
      };
};

const isNodeInside = (element: HTMLElement | null, target: EventTarget | null) => {
  if (!element || typeof Node === 'undefined' || !(target instanceof Node)) return false;
  return element.contains(target);
};

export function FailureDetailsTooltip({
  children,
  ariaLabel,
  statusText,
  detailLines,
  copyText,
  copyLabel,
  tooltipId,
  onCopy,
}: FailureDetailsTooltipProps) {
  const generatedTooltipId = useId();
  const resolvedTooltipId = tooltipId ?? `${generatedTooltipId}-failure-tooltip`;
  const triggerRef = useRef<HTMLSpanElement | null>(null);
  const tooltipRef = useRef<HTMLSpanElement | null>(null);
  const closeTimerRef = useRef<number | null>(null);
  const rafRef = useRef<number | null>(null);
  const [open, setOpen] = useState(false);
  const [tooltipPosition, setTooltipPosition] = useState<TooltipPosition | null>(null);
  const isBrowser = typeof document !== 'undefined';

  const clearCloseTimer = useCallback(() => {
    if (closeTimerRef.current === null || typeof window === 'undefined') return;
    window.clearTimeout(closeTimerRef.current);
    closeTimerRef.current = null;
  }, []);

  const updateTooltipPosition = useCallback(() => {
    if (!triggerRef.current) return;
    const nextPosition = resolveTooltipPosition(triggerRef.current);
    if (nextPosition) {
      setTooltipPosition(nextPosition);
    }
  }, []);

  const scheduleTooltipPositionUpdate = useCallback(() => {
    if (typeof window === 'undefined') return;
    if (rafRef.current !== null) {
      window.cancelAnimationFrame(rafRef.current);
    }
    rafRef.current = window.requestAnimationFrame(() => {
      rafRef.current = null;
      updateTooltipPosition();
    });
  }, [updateTooltipPosition]);

  const showTooltip = useCallback(() => {
    clearCloseTimer();
    updateTooltipPosition();
    setOpen(true);
  }, [clearCloseTimer, updateTooltipPosition]);

  const requestHideTooltip = useCallback(() => {
    clearCloseTimer();
    if (typeof window === 'undefined') {
      setOpen(false);
      return;
    }
    closeTimerRef.current = window.setTimeout(() => {
      closeTimerRef.current = null;
      setOpen(false);
    }, CLOSE_DELAY_MS);
  }, [clearCloseTimer]);

  const handleBlur = useCallback(
    (event: FocusEvent<HTMLElement>) => {
      const nextTarget = event.relatedTarget;
      if (
        isNodeInside(triggerRef.current, nextTarget) ||
        isNodeInside(tooltipRef.current, nextTarget)
      ) {
        return;
      }
      requestHideTooltip();
    },
    [requestHideTooltip]
  );

  const handleKeyDown = useCallback((event: KeyboardEvent<HTMLSpanElement>) => {
    if (event.key !== 'Escape') return;
    event.preventDefault();
    setOpen(false);
  }, []);

  useEffect(() => {
    return () => {
      clearCloseTimer();
      if (rafRef.current !== null && typeof window !== 'undefined') {
        window.cancelAnimationFrame(rafRef.current);
        rafRef.current = null;
      }
    };
  }, [clearCloseTimer]);

  useEffect(() => {
    if (!open || typeof window === 'undefined') return undefined;

    scheduleTooltipPositionUpdate();
    window.addEventListener('resize', scheduleTooltipPositionUpdate);
    window.addEventListener('scroll', scheduleTooltipPositionUpdate, true);

    const handlePointerDown = (event: PointerEvent) => {
      if (
        isNodeInside(triggerRef.current, event.target) ||
        isNodeInside(tooltipRef.current, event.target)
      ) {
        return;
      }
      setOpen(false);
    };
    document.addEventListener('pointerdown', handlePointerDown);

    return () => {
      window.removeEventListener('resize', scheduleTooltipPositionUpdate);
      window.removeEventListener('scroll', scheduleTooltipPositionUpdate, true);
      document.removeEventListener('pointerdown', handlePointerDown);
    };
  }, [open, scheduleTooltipPositionUpdate]);

  const placement = tooltipPosition?.placement ?? 'below';
  const tooltipClassName = [
    styles.tooltip,
    placement === 'above' ? styles.tooltipAbove : styles.tooltipBelow,
    open ? styles.tooltipOpen : '',
  ]
    .filter(Boolean)
    .join(' ');
  const tooltip = (
    <span
      id={resolvedTooltipId}
      ref={tooltipRef}
      role="tooltip"
      className={tooltipClassName}
      style={isBrowser ? tooltipPosition?.style : undefined}
      onMouseEnter={clearCloseTimer}
      onMouseLeave={requestHideTooltip}
      onFocus={showTooltip}
      onBlur={handleBlur}
    >
      <button
        type="button"
        className={styles.copyButton}
        onClick={(event) => {
          event.preventDefault();
          event.stopPropagation();
          onCopy(copyText);
        }}
        title={copyLabel}
        aria-label={copyLabel}
      >
        <IconCopy size={13} />
      </button>
      {statusText ? <span className={styles.status}>{statusText}</span> : null}
      {detailLines.map((line, index) => (
        <span key={`${index}-${line}`} className={styles.body}>
          {line}
        </span>
      ))}
    </span>
  );

  return (
    <span
      ref={triggerRef}
      className={styles.trigger}
      tabIndex={0}
      aria-describedby={resolvedTooltipId}
      aria-label={ariaLabel}
      aria-expanded={open}
      onMouseEnter={showTooltip}
      onMouseLeave={requestHideTooltip}
      onFocus={showTooltip}
      onBlur={handleBlur}
      onClick={showTooltip}
      onKeyDown={handleKeyDown}
    >
      {children}
      {!isBrowser ? tooltip : null}
      {isBrowser && open ? createPortal(tooltip, document.body) : null}
    </span>
  );
}
