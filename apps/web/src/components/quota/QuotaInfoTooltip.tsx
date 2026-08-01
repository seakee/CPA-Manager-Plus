import {
  type CSSProperties,
  type KeyboardEvent,
  type ReactNode,
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
} from 'react';
import { createPortal } from 'react-dom';
import { IconInfo } from '@/components/ui/icons';
import styles from './QuotaInfoTooltip.module.scss';

const TOOLTIP_VIEWPORT_MARGIN = 12;
const TOOLTIP_OFFSET = 8;
const TOOLTIP_MAX_WIDTH = 320;
const TOOLTIP_MAX_HEIGHT = 240;
const TOOLTIP_ESTIMATED_HEIGHT = 72;

type TooltipPlacement = 'above' | 'below';

export type QuotaInfoTooltipRow = {
  key: string;
  label: string;
  value: string;
};

export type QuotaInfoTooltipPosition = {
  placement: TooltipPlacement;
  style: CSSProperties;
};

type TooltipAnchorRect = Pick<DOMRect, 'bottom' | 'height' | 'left' | 'right' | 'top' | 'width'>;

const clampNumber = (value: number, min: number, max: number) =>
  Math.min(Math.max(value, min), max);

/**
 * Position a fixed tooltip relative to a trigger, keeping it fully inside the viewport.
 * Uses the actual measured tooltip size when available so small screens do not over-shift
 * based on the max width estimate.
 */
// eslint-disable-next-line react-refresh/only-export-components
export const resolveQuotaInfoTooltipPosition = (
  rect: TooltipAnchorRect,
  viewportWidth: number,
  viewportHeight: number,
  measuredSize?: { width: number; height: number } | null
): QuotaInfoTooltipPosition => {
  const availableWidth = Math.max(0, viewportWidth - TOOLTIP_VIEWPORT_MARGIN * 2);
  const maxWidth = Math.min(TOOLTIP_MAX_WIDTH, availableWidth);
  const tooltipWidth = clampNumber(
    measuredSize?.width && measuredSize.width > 0 ? measuredSize.width : Math.min(maxWidth, 220),
    0,
    maxWidth
  );
  const tooltipHeight = clampNumber(
    measuredSize?.height && measuredSize.height > 0
      ? measuredSize.height
      : TOOLTIP_ESTIMATED_HEIGHT,
    0,
    TOOLTIP_MAX_HEIGHT
  );

  const spaceAbove = rect.top - TOOLTIP_VIEWPORT_MARGIN - TOOLTIP_OFFSET;
  const spaceBelow = viewportHeight - rect.bottom - TOOLTIP_VIEWPORT_MARGIN - TOOLTIP_OFFSET;
  // On tight vertical space (common on mobile / top of card), prefer the side with more room.
  const placement: TooltipPlacement =
    spaceAbove >= tooltipHeight || (spaceAbove >= spaceBelow && spaceAbove >= 48)
      ? 'above'
      : 'below';
  const availableHeight = Math.max(0, placement === 'above' ? spaceAbove : spaceBelow);
  const maxHeight = Math.min(TOOLTIP_MAX_HEIGHT, availableHeight || TOOLTIP_MAX_HEIGHT);

  // Anchor to trigger center; clamp so the whole box stays on-screen.
  const anchorCenter = rect.left + rect.width / 2;
  const halfWidth = tooltipWidth / 2;
  const minCenter = TOOLTIP_VIEWPORT_MARGIN + halfWidth;
  const maxCenter = Math.max(minCenter, viewportWidth - TOOLTIP_VIEWPORT_MARGIN - halfWidth);
  const centerX = clampNumber(anchorCenter, minCenter, maxCenter);
  const left = centerX - halfWidth;

  const baseStyle: CSSProperties = {
    left,
    width: 'max-content',
    maxWidth,
    maxHeight,
  };

  if (placement === 'above') {
    return {
      placement,
      style: {
        ...baseStyle,
        top: Math.max(
          TOOLTIP_VIEWPORT_MARGIN,
          rect.top - TOOLTIP_OFFSET - Math.min(tooltipHeight, maxHeight)
        ),
      },
    };
  }

  return {
    placement,
    style: {
      ...baseStyle,
      top: Math.min(
        viewportHeight - TOOLTIP_VIEWPORT_MARGIN - Math.min(tooltipHeight, maxHeight),
        rect.bottom + TOOLTIP_OFFSET
      ),
    },
  };
};

export type QuotaInfoTooltipProps = {
  ariaLabel: string;
  rows: QuotaInfoTooltipRow[];
  children?: ReactNode;
};

export function QuotaInfoTooltip(props: QuotaInfoTooltipProps) {
  const { ariaLabel, rows, children } = props;
  const tooltipId = useId();
  const triggerRef = useRef<HTMLSpanElement | null>(null);
  const tooltipRef = useRef<HTMLSpanElement | null>(null);
  const rafRef = useRef<number | null>(null);
  const [open, setOpen] = useState(false);
  const [tooltipPosition, setTooltipPosition] = useState<QuotaInfoTooltipPosition | null>(null);
  const isBrowser = typeof document !== 'undefined';
  const hasRows = rows.length > 0;

  const updateTooltipPosition = useCallback(() => {
    if (!triggerRef.current || typeof window === 'undefined') return;
    const measured =
      tooltipRef.current !== null
        ? {
            width: tooltipRef.current.offsetWidth,
            height: tooltipRef.current.offsetHeight,
          }
        : null;
    setTooltipPosition(
      resolveQuotaInfoTooltipPosition(
        triggerRef.current.getBoundingClientRect(),
        window.innerWidth,
        window.innerHeight,
        measured
      )
    );
  }, []);

  const scheduleTooltipPositionUpdate = useCallback(() => {
    if (typeof window === 'undefined') return;
    if (rafRef.current !== null) window.cancelAnimationFrame(rafRef.current);
    rafRef.current = window.requestAnimationFrame(() => {
      rafRef.current = null;
      updateTooltipPosition();
    });
  }, [updateTooltipPosition]);

  const showTooltip = useCallback(() => {
    if (!hasRows) return;
    updateTooltipPosition();
    setOpen(true);
  }, [hasRows, updateTooltipPosition]);

  const hideTooltip = useCallback(() => setOpen(false), []);

  const handleKeyDown = useCallback((event: KeyboardEvent<HTMLSpanElement>) => {
    if (event.key !== 'Escape') return;
    event.preventDefault();
    setOpen(false);
  }, []);

  // Re-measure after paint so left/top use the real tooltip box size on small screens.
  useLayoutEffect(() => {
    if (!open) return;
    updateTooltipPosition();
  }, [open, rows, updateTooltipPosition]);

  useEffect(() => {
    if (!open || typeof window === 'undefined') return undefined;
    scheduleTooltipPositionUpdate();
    window.addEventListener('resize', scheduleTooltipPositionUpdate);
    window.addEventListener('scroll', scheduleTooltipPositionUpdate, true);

    return () => {
      window.removeEventListener('resize', scheduleTooltipPositionUpdate);
      window.removeEventListener('scroll', scheduleTooltipPositionUpdate, true);
    };
  }, [open, scheduleTooltipPositionUpdate]);

  useEffect(
    () => () => {
      if (rafRef.current !== null && typeof window !== 'undefined') {
        window.cancelAnimationFrame(rafRef.current);
      }
    },
    []
  );

  if (!hasRows) return null;

  const tooltip = (
    <span
      ref={tooltipRef}
      id={tooltipId}
      role="tooltip"
      className={styles.tooltip}
      style={isBrowser ? tooltipPosition?.style : undefined}
      data-placement={tooltipPosition?.placement}
    >
      {rows.map((row) => (
        <span key={row.key} className={styles.row}>
          <span className={styles.label}>{row.label}</span>
          <span className={styles.value}>{row.value}</span>
        </span>
      ))}
    </span>
  );

  return (
    <span
      ref={triggerRef}
      className={styles.trigger}
      tabIndex={0}
      aria-label={ariaLabel}
      aria-describedby={open ? tooltipId : undefined}
      onMouseEnter={showTooltip}
      onMouseLeave={hideTooltip}
      onFocus={showTooltip}
      onBlur={hideTooltip}
      onKeyDown={handleKeyDown}
    >
      {children ?? (
        <IconInfo size={14} className={styles.icon} aria-hidden={true} focusable={false} />
      )}
      {open && !isBrowser ? tooltip : null}
      {open && isBrowser && tooltipPosition ? createPortal(tooltip, document.body) : null}
    </span>
  );
}
