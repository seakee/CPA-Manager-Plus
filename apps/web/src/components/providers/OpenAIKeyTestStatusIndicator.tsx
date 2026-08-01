import {
  type CSSProperties,
  type KeyboardEvent,
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
} from 'react';
import { createPortal } from 'react-dom';
import { useTranslation } from 'react-i18next';
import styles from '@/features/aiProviders/AiProvidersPage.module.scss';

type OpenAIKeyTestStatus = 'idle' | 'loading' | 'success' | 'error';

const TOOLTIP_VIEWPORT_MARGIN = 12;
const TOOLTIP_OFFSET = 10;
const TOOLTIP_MAX_WIDTH = 420;
const TOOLTIP_MAX_HEIGHT = 360;
const TOOLTIP_HIDE_DELAY_MS = 120;

type TooltipPlacement = 'above' | 'below';

export type OpenAIKeyTestTooltipPosition = {
  placement: TooltipPlacement;
  style: CSSProperties;
};

type TooltipAnchorRect = Pick<DOMRect, 'bottom' | 'height' | 'left' | 'top' | 'width'>;

const clampNumber = (value: number, min: number, max: number) =>
  Math.min(Math.max(value, min), max);

// eslint-disable-next-line react-refresh/only-export-components
export const resolveOpenAIKeyTestTooltipPosition = (
  rect: TooltipAnchorRect,
  viewportWidth: number,
  viewportHeight: number
): OpenAIKeyTestTooltipPosition => {
  const maxWidth = Math.max(
    0,
    Math.min(TOOLTIP_MAX_WIDTH, viewportWidth - TOOLTIP_VIEWPORT_MARGIN * 2)
  );
  const halfMaxWidth = maxWidth / 2;
  const anchorCenter = rect.left + rect.width / 2;
  const left = clampNumber(
    anchorCenter,
    TOOLTIP_VIEWPORT_MARGIN + halfMaxWidth,
    Math.max(
      TOOLTIP_VIEWPORT_MARGIN + halfMaxWidth,
      viewportWidth - TOOLTIP_VIEWPORT_MARGIN - halfMaxWidth
    )
  );
  const spaceAbove = rect.top - TOOLTIP_VIEWPORT_MARGIN - TOOLTIP_OFFSET;
  const spaceBelow = viewportHeight - rect.bottom - TOOLTIP_VIEWPORT_MARGIN - TOOLTIP_OFFSET;
  const placement: TooltipPlacement =
    spaceAbove >= TOOLTIP_MAX_HEIGHT || spaceAbove >= spaceBelow ? 'above' : 'below';
  const availableHeight = Math.max(0, placement === 'above' ? spaceAbove : spaceBelow);
  const baseStyle: CSSProperties = {
    left,
    maxHeight: Math.min(TOOLTIP_MAX_HEIGHT, availableHeight),
    maxWidth,
  };

  return placement === 'above'
    ? {
        placement,
        style: {
          ...baseStyle,
          bottom: viewportHeight - rect.top + TOOLTIP_OFFSET,
        },
      }
    : {
        placement,
        style: {
          ...baseStyle,
          top: rect.bottom + TOOLTIP_OFFSET,
        },
      };
};

interface OpenAIKeyTestStatusIndicatorProps {
  status: OpenAIKeyTestStatus;
  message?: string;
}

function StatusLoadingIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none" className={styles.statusIconSpin}>
      <circle cx="8" cy="8" r="7" stroke="currentColor" strokeOpacity="0.25" strokeWidth="2" />
      <path d="M8 1A7 7 0 0 1 8 15" stroke="currentColor" strokeWidth="2" strokeLinecap="round" />
    </svg>
  );
}

function StatusSuccessIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none">
      <circle cx="8" cy="8" r="8" fill="var(--success-color, #22c55e)" />
      <path
        d="M4.5 8L7 10.5L11.5 6"
        stroke="white"
        strokeWidth="2"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

function StatusErrorIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none">
      <circle cx="8" cy="8" r="8" fill="var(--danger-color, #f56c6c)" />
      <path
        d="M5 5L11 11M11 5L5 11"
        stroke="white"
        strokeWidth="2"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

function StatusIdleIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 16 16" fill="none">
      <circle cx="8" cy="8" r="7" stroke="var(--text-tertiary, #9ca3af)" strokeWidth="2" />
    </svg>
  );
}

function StatusIcon({ status }: { status: OpenAIKeyTestStatus }) {
  switch (status) {
    case 'loading':
      return <StatusLoadingIcon />;
    case 'success':
      return <StatusSuccessIcon />;
    case 'error':
      return <StatusErrorIcon />;
    default:
      return <StatusIdleIcon />;
  }
}

export function OpenAIKeyTestStatusIndicator({
  status,
  message,
}: OpenAIKeyTestStatusIndicatorProps) {
  const { t } = useTranslation();
  const tooltipId = useId();
  const triggerRef = useRef<HTMLSpanElement | null>(null);
  const rafRef = useRef<number | null>(null);
  const hideTimerRef = useRef<number | null>(null);
  const [open, setOpen] = useState(false);
  const [tooltipPosition, setTooltipPosition] = useState<OpenAIKeyTestTooltipPosition | null>(null);
  const isBrowser = typeof document !== 'undefined';

  const trimmedMessage = String(message ?? '').trim();
  const resolvedMessage = trimmedMessage || t('ai_providers.openai_test_failed');
  const hasTooltip = status === 'error' && Boolean(resolvedMessage);

  const updateTooltipPosition = useCallback(() => {
    if (!triggerRef.current || typeof window === 'undefined') return;
    setTooltipPosition(
      resolveOpenAIKeyTestTooltipPosition(
        triggerRef.current.getBoundingClientRect(),
        window.innerWidth,
        window.innerHeight
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

  const clearHideTimer = useCallback(() => {
    if (hideTimerRef.current === null || typeof window === 'undefined') return;
    window.clearTimeout(hideTimerRef.current);
    hideTimerRef.current = null;
  }, []);

  const showTooltip = useCallback(() => {
    clearHideTimer();
    updateTooltipPosition();
    setOpen(true);
  }, [clearHideTimer, updateTooltipPosition]);

  const hideTooltip = useCallback(() => {
    clearHideTimer();
    setOpen(false);
  }, [clearHideTimer]);

  const scheduleHideTooltip = useCallback(() => {
    if (typeof window === 'undefined') {
      setOpen(false);
      return;
    }
    clearHideTimer();
    hideTimerRef.current = window.setTimeout(() => {
      hideTimerRef.current = null;
      setOpen(false);
    }, TOOLTIP_HIDE_DELAY_MS);
  }, [clearHideTimer]);

  const handleKeyDown = useCallback((event: KeyboardEvent<HTMLSpanElement>) => {
    if (event.key !== 'Escape') return;
    event.preventDefault();
    setOpen(false);
  }, []);

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
      clearHideTimer();
    },
    [clearHideTimer]
  );

  const ariaLabel =
    status === 'error'
      ? resolvedMessage
      : status === 'success'
        ? t('ai_providers.openai_test_success')
        : status === 'loading'
          ? t('ai_providers.openai_test_running')
          : 'Idle';

  const tooltip = (
    <span
      id={tooltipId}
      role="tooltip"
      className={styles.keyStatusTooltip}
      style={isBrowser ? tooltipPosition?.style : undefined}
      onMouseEnter={clearHideTimer}
      onMouseLeave={scheduleHideTooltip}
    >
      <span className={styles.keyStatusTooltipText}>{resolvedMessage}</span>
    </span>
  );

  return (
    <span
      ref={triggerRef}
      className={`${styles.keyStatusTrigger} ${hasTooltip ? styles.keyStatusTriggerInteractive : ''}`}
      tabIndex={hasTooltip ? 0 : -1}
      aria-label={ariaLabel}
      aria-describedby={hasTooltip && open ? tooltipId : undefined}
      onMouseEnter={hasTooltip ? showTooltip : undefined}
      onMouseLeave={hasTooltip ? scheduleHideTooltip : undefined}
      onFocus={hasTooltip ? showTooltip : undefined}
      onBlur={hasTooltip ? hideTooltip : undefined}
      onKeyDown={hasTooltip ? handleKeyDown : undefined}
    >
      <StatusIcon status={status} />
      {hasTooltip && open && !isBrowser ? tooltip : null}
      {hasTooltip && open && isBrowser && tooltipPosition
        ? createPortal(tooltip, document.body)
        : null}
    </span>
  );
}
