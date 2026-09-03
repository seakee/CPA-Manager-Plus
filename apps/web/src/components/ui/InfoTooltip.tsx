import {
  type CSSProperties,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactNode,
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
} from 'react';
import { createPortal } from 'react-dom';
import { IconInfo } from './icons';
import styles from './InfoTooltip.module.scss';

const VIEWPORT_MARGIN = 12;
const TOOLTIP_OFFSET = 8;
const TOOLTIP_MAX_WIDTH = 420;
const TOOLTIP_MAX_HEIGHT = 360;

type TooltipPlacement = 'above' | 'below';

type TooltipAnchorRect = Pick<DOMRect, 'bottom' | 'left' | 'top' | 'width'>;

export type InfoTooltipPosition = {
  placement: TooltipPlacement;
  style: CSSProperties;
};

const clamp = (value: number, min: number, max: number) => Math.min(Math.max(value, min), max);

// eslint-disable-next-line react-refresh/only-export-components
export const resolveInfoTooltipPosition = (
  rect: TooltipAnchorRect,
  viewportWidth: number,
  viewportHeight: number
): InfoTooltipPosition => {
  const maxWidth = Math.max(0, Math.min(TOOLTIP_MAX_WIDTH, viewportWidth - VIEWPORT_MARGIN * 2));
  const halfWidth = maxWidth / 2;
  const left = clamp(
    rect.left + rect.width / 2,
    VIEWPORT_MARGIN + halfWidth,
    Math.max(VIEWPORT_MARGIN + halfWidth, viewportWidth - VIEWPORT_MARGIN - halfWidth)
  );
  const spaceAbove = rect.top - VIEWPORT_MARGIN - TOOLTIP_OFFSET;
  const spaceBelow = viewportHeight - rect.bottom - VIEWPORT_MARGIN - TOOLTIP_OFFSET;
  const placement: TooltipPlacement =
    spaceAbove >= TOOLTIP_MAX_HEIGHT || spaceAbove >= spaceBelow ? 'above' : 'below';
  const availableHeight = Math.max(0, placement === 'above' ? spaceAbove : spaceBelow);

  return placement === 'above'
    ? {
        placement,
        style: {
          left,
          bottom: viewportHeight - rect.top + TOOLTIP_OFFSET,
          maxWidth,
          maxHeight: Math.min(TOOLTIP_MAX_HEIGHT, availableHeight),
        },
      }
    : {
        placement,
        style: {
          left,
          top: rect.bottom + TOOLTIP_OFFSET,
          maxWidth,
          maxHeight: Math.min(TOOLTIP_MAX_HEIGHT, availableHeight),
        },
      };
};

interface InfoTooltipProps {
  content: ReactNode;
  ariaLabel: string;
  className?: string;
}

export function InfoTooltip({ content, ariaLabel, className = '' }: InfoTooltipProps) {
  const tooltipId = useId();
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const [open, setOpen] = useState(false);
  const [position, setPosition] = useState<InfoTooltipPosition | null>(null);
  const closeTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const triggerHoveredRef = useRef(false);
  const tooltipHoveredRef = useRef(false);
  const triggerFocusedRef = useRef(false);
  const isBrowser = typeof document !== 'undefined';

  const updatePosition = useCallback(() => {
    if (!triggerRef.current || typeof window === 'undefined') return;
    setPosition(
      resolveInfoTooltipPosition(
        triggerRef.current.getBoundingClientRect(),
        window.innerWidth,
        window.innerHeight
      )
    );
  }, []);

  const clearCloseTimer = useCallback(() => {
    if (closeTimerRef.current === null) return;
    clearTimeout(closeTimerRef.current);
    closeTimerRef.current = null;
  }, []);

  const showTooltip = useCallback(() => {
    clearCloseTimer();
    updatePosition();
    setOpen(true);
  }, [clearCloseTimer, updatePosition]);

  const closeTooltip = useCallback(() => {
    clearCloseTimer();
    setOpen(false);
  }, [clearCloseTimer]);

  const scheduleHide = useCallback(() => {
    clearCloseTimer();
    closeTimerRef.current = setTimeout(() => {
      closeTimerRef.current = null;
      if (!triggerHoveredRef.current && !tooltipHoveredRef.current && !triggerFocusedRef.current) {
        setOpen(false);
      }
    }, 120);
  }, [clearCloseTimer]);

  const handleTriggerMouseEnter = useCallback(() => {
    triggerHoveredRef.current = true;
    showTooltip();
  }, [showTooltip]);

  const handleTriggerMouseLeave = useCallback(() => {
    triggerHoveredRef.current = false;
    scheduleHide();
  }, [scheduleHide]);

  const handleTriggerFocus = useCallback(() => {
    triggerFocusedRef.current = true;
    showTooltip();
  }, [showTooltip]);

  const handleTriggerBlur = useCallback(() => {
    triggerFocusedRef.current = false;
    if (!triggerHoveredRef.current && !tooltipHoveredRef.current) {
      closeTooltip();
    }
  }, [closeTooltip]);

  const handleEscape = useCallback(
    (event: { key: string; preventDefault: () => void; stopPropagation?: () => void }) => {
      if (event.key !== 'Escape') return;
      event.preventDefault();
      event.stopPropagation?.();
      closeTooltip();
    },
    [closeTooltip]
  );

  const handleKeyDown = useCallback(
    (event: ReactKeyboardEvent<HTMLButtonElement>) => handleEscape(event),
    [handleEscape]
  );

  const handleWindowKeyDown = useCallback(
    (event: KeyboardEvent) => handleEscape(event),
    [handleEscape]
  );

  useEffect(() => {
    if (!open || typeof window === 'undefined') return undefined;
    window.addEventListener('resize', updatePosition);
    window.addEventListener('scroll', updatePosition, true);
    window.addEventListener('keydown', handleWindowKeyDown, true);
    return () => {
      window.removeEventListener('resize', updatePosition);
      window.removeEventListener('scroll', updatePosition, true);
      window.removeEventListener('keydown', handleWindowKeyDown, true);
    };
  }, [handleWindowKeyDown, open, updatePosition]);

  useEffect(
    () => () => {
      clearCloseTimer();
    },
    [clearCloseTimer]
  );

  const tooltip = (
    <span
      id={tooltipId}
      role="tooltip"
      className={styles.tooltip}
      style={isBrowser ? position?.style : undefined}
      data-placement={position?.placement}
      data-info-tooltip-content="true"
      onMouseEnter={() => {
        tooltipHoveredRef.current = true;
        clearCloseTimer();
      }}
      onMouseLeave={() => {
        tooltipHoveredRef.current = false;
        scheduleHide();
      }}
    >
      {content}
    </span>
  );

  return (
    <>
      <button
        ref={triggerRef}
        type="button"
        className={[styles.trigger, className].filter(Boolean).join(' ')}
        aria-label={ariaLabel}
        aria-expanded={open}
        aria-describedby={open ? tooltipId : undefined}
        onMouseEnter={handleTriggerMouseEnter}
        onMouseLeave={handleTriggerMouseLeave}
        onFocus={handleTriggerFocus}
        onBlur={handleTriggerBlur}
        onKeyDown={handleKeyDown}
        data-info-tooltip-trigger="true"
      >
        <IconInfo size={15} />
      </button>
      {open && !isBrowser ? tooltip : null}
      {open && isBrowser && position ? createPortal(tooltip, document.body) : null}
    </>
  );
}
