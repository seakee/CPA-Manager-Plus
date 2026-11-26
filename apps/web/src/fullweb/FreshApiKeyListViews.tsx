import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
  type MouseEvent as ReactMouseEvent,
  type ReactNode,
} from 'react';
import { createPortal } from 'react-dom';
import { Button } from '../components/ui/Button';
import { DropdownMenu, type DropdownMenuItem } from '../components/ui/DropdownMenu';
import {
  IconBinary,
  IconCheck,
  IconCopy,
  IconDollarSign,
  IconKey,
  IconRotateCcw,
  IconSend,
} from '../components/ui/icons';
import { interpolateStatusBarColor } from '../utils/statusBarColor';
import { fullWebText as t } from '../i18n/fullweb';
import {
  formatKeyModelsPopoverTitle,
  formatKeyModelsSummaryLine,
  formatKeyOtherModelsSummary,
  formatKeyUsageColumnHeader,
  keyWorkspaceText as k,
} from '../i18n/freshKeyWorkspace';
import { FullWebIcon } from './FullWebIcon';
import { keyDisplayName, keyUnifiedStatus, type Key } from './freshKeyMetadata';
import {
  usageCompactNumberText,
  usageCompactTimeText,
  usageCompactUsdText,
  usageCountText,
  usageCoveragePill,
  usageReasonText,
  usageStateText,
  usageTimeText,
  type UsageModelCount,
  type UsageState,
  type UsageStatusSlot,
  type UsageSummaryItem,
} from './freshKeyUsage';

export type KeyListUsageWindow = '24h' | '7d' | '30d';

export type RowCopyFeedbackState = {
  id: string;
  status: 'copying' | 'copied' | 'failed';
} | null;

export interface FreshApiKeyListViewsProps {
  viewMode: 'table' | 'cards';
  usageWindow?: KeyListUsageWindow;
  keys: Key[];
  usageById: ReadonlyMap<string, UsageSummaryItem>;
  selectedId: string;
  locked: boolean;
  operationPending: boolean;
  revocationAvailable: boolean;
  rowCopyState: RowCopyFeedbackState;
  onOpenDetails: (
    keyId: string,
    options?: { tab?: 'overview' | 'usage'; action?: 'rename'; window?: KeyListUsageWindow }
  ) => void;
  onCopySecret: (keyItem: Key) => void;
  onRename: (keyItem: Key) => void;
  onRegenerate: (keyId: string) => void;
  onRevoke: (keyId: string) => void;
}

function formatUsageCostText(
  cost: number | undefined,
  state: UsageState
): string | null {
  if (cost === undefined || !Number.isFinite(cost) || cost < 0) return null;
  if (state === 'unknown' || state === 'unavailable') return null;
  if (cost === 0) return state === 'complete' ? '$0.00' : null;
  const formatted = usageCompactUsdText(cost, state);
  return formatted === '—' ? null : formatted;
}

function computeLargestRemainderPercentages(weights: number[], total: number): number[] {
  if (weights.length === 0 || total <= 0) {
    return weights.map(() => 0);
  }
  const safeWeights = weights.map((w) => (Number.isFinite(w) && w > 0 ? w : 0));
  const sumWeights = safeWeights.reduce((sum, w) => sum + w, 0);
  if (sumWeights <= 0) {
    return weights.map(() => 0);
  }
  const denom = Math.max(total, sumWeights);
  const unshown = Math.max(0, denom - sumWeights);
  const allWeights = unshown > 0 ? [...safeWeights, unshown] : safeWeights;
  const raw = allWeights.map((w, index) => {
    const exact = (w / denom) * 100;
    const floored = Math.floor(exact);
    return { index, floored, remainder: exact - floored };
  });
  let remaining = 100 - raw.reduce((sum, entry) => sum + entry.floored, 0);
  const byRemainder = [...raw].sort((a, b) => {
    if (Math.abs(b.remainder - a.remainder) > 1e-12) return b.remainder - a.remainder;
    return a.index - b.index;
  });
  const result = raw.map((entry) => entry.floored);
  for (const entry of byRemainder) {
    if (remaining <= 0) break;
    if (entry.remainder <= 0) break;
    result[entry.index] += 1;
    remaining -= 1;
  }
  return result.slice(0, weights.length);
}

function resolveKeyOriginText(origin: Key['origin']): string | null {
  switch (origin) {
    case 'installer':
      return k('installerOriginInline');
    case 'adopted':
      return k('adoptedOriginInline');
    default:
      return null;
  }
}

function isInteractiveChildTarget(target: EventTarget | null): boolean {
  if (!target || typeof Element === 'undefined' || !(target instanceof Element)) {
    return false;
  }
  return Boolean(
    target.closest(
      'button, a, input, select, textarea, [role="menu"], [data-row-interactive="true"]'
    )
  );
}

function handleSurfaceActivateKeyDown(
  event: ReactKeyboardEvent<HTMLElement>,
  disabled: boolean,
  activate: () => void
) {
  if (disabled) return;
  if (event.target !== event.currentTarget) return;
  if (event.key === 'Enter' || event.key === ' ') {
    event.preventDefault();
    activate();
  }
}

function stopInteractiveRowClick(event?: ReactMouseEvent<HTMLElement>) {
  event?.stopPropagation();
}

function KeyFloatingPopover({
  open,
  anchorEl,
  testId,
  className,
  onMouseEnter,
  onMouseLeave,
  onDismiss,
  children,
}: {
  open: boolean;
  anchorEl: HTMLElement | null;
  testId?: string;
  className: string;
  onMouseEnter?: () => void;
  onMouseLeave?: () => void;
  onDismiss: () => void;
  children: ReactNode;
}) {
  const popRef = useRef<HTMLDivElement | null>(null);
  const onDismissRef = useRef(onDismiss);
  const anchorElRef = useRef(anchorEl);
  const [coords, setCoords] = useState<{ top: number; left: number } | null>(null);

  useLayoutEffect(() => {
    onDismissRef.current = onDismiss;
    anchorElRef.current = anchorEl;
  }, [onDismiss, anchorEl]);

  const recomputePosition = useCallback(() => {
    if (
      typeof window === 'undefined' ||
      !anchorEl ||
      typeof anchorEl.getBoundingClientRect !== 'function'
    ) {
      return;
    }
    const rect = anchorEl.getBoundingClientRect();
    const popWidth = popRef.current?.offsetWidth || 280;
    const popHeight = popRef.current?.offsetHeight || 120;
    const pad = 10;

    let left = rect.left + rect.width / 2 - popWidth / 2;
    left = Math.max(pad, Math.min(left, window.innerWidth - popWidth - pad));

    let top = rect.bottom + 8;
    if (top + popHeight + pad > window.innerHeight && rect.top - popHeight - 8 >= pad) {
      top = rect.top - popHeight - 8;
    }
    setCoords({ top, left });
  }, [anchorEl]);

  useLayoutEffect(() => {
    if (!open) {
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setCoords(null);
      return;
    }
    recomputePosition();
  }, [open, recomputePosition]);

  useEffect(() => {
    if (!open || typeof window === 'undefined' || typeof window.addEventListener !== 'function') {
      return;
    }
    const handleScrollOrResize = () => onDismissRef.current();
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onDismissRef.current();
    };
    const handlePointerDown = (event: PointerEvent) => {
      const target = event.target;
      if (typeof Node === 'undefined' || !(target instanceof Node)) {
        return;
      }
      if (popRef.current?.contains(target) || anchorElRef.current?.contains(target)) {
        return;
      }
      onDismissRef.current();
    };
    window.addEventListener('scroll', handleScrollOrResize, true);
    window.addEventListener('resize', handleScrollOrResize);
    window.addEventListener('keydown', handleKeyDown);
    window.addEventListener('pointerdown', handlePointerDown, true);
    return () => {
      window.removeEventListener('scroll', handleScrollOrResize, true);
      window.removeEventListener('resize', handleScrollOrResize);
      window.removeEventListener('keydown', handleKeyDown);
      window.removeEventListener('pointerdown', handlePointerDown, true);
    };
  }, [open]);
  if (!open) return null;

  const content = (
    <div
      ref={popRef}
      role="tooltip"
      data-testid={testId}
      data-row-interactive="true"
      className={className}
      style={
        coords
          ? { top: `${coords.top}px`, left: `${coords.left}px` }
          : { top: '-9999px', left: '-9999px' }
      }
      onMouseEnter={onMouseEnter}
      onMouseLeave={onMouseLeave}
      onClick={stopInteractiveRowClick}
    >
      {children}
    </div>
  );

  if (typeof document !== 'undefined' && document.body) {
    return createPortal(content, document.body);
  }
  return content;
}

function KeyModelsInteractive({
  keyId,
  usageWindow,
  item,
  maxVisible = 2,
  showSummaryLine = true,
  emptyFallback,
}: {
  keyId: string;
  usageWindow: KeyListUsageWindow;
  item: UsageSummaryItem | undefined;
  maxVisible?: number;
  showSummaryLine?: boolean;
  emptyFallback?: ReactNode;
}) {
  const [open, setOpen] = useState(false);
  const [anchorEl, setAnchorEl] = useState<HTMLElement | null>(null);
  const closeTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const pointerDownRef = useRef(false);

  const clearCloseTimer = () => {
    if (closeTimerRef.current !== null) {
      clearTimeout(closeTimerRef.current);
      closeTimerRef.current = null;
    }
  };

  const state: UsageState = item?.coverage.state ?? 'unknown';
  const proven = state === 'complete' || state === 'partial';
  const topModels: UsageModelCount[] = proven ? (item?.topModels ?? []) : [];
  const hasModels = proven && topModels.length > 0;

  useEffect(() => () => clearCloseTimer(), []);
  useEffect(() => {
    if (!hasModels) {
      clearCloseTimer();
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setOpen(false);
    }
  }, [hasModels]);

  const totalModels = proven ? Math.max(item?.modelCount ?? topModels.length, topModels.length) : 0;

  if (!hasModels) {
    return (
      <>
        {emptyFallback ?? (
          <span className="key-muted-dash" aria-label={k('noModelsInWindow')}>
            —
          </span>
        )}
      </>
    );
  }

  const visibleModels = topModels.slice(0, maxVisible);
  const extraCount = Math.max(0, totalModels - visibleModels.length);
  const totalCalls = Math.max(
    item?.calls ?? 0,
    topModels.reduce((sum, entry) => sum + entry.calls, 0),
    1
  );
  const popoverPcts = computeLargestRemainderPercentages(
    topModels.map((entry) => entry.calls),
    totalCalls
  );

  const handleOpen = (target: HTMLElement | null) => {
    clearCloseTimer();
    if (target) setAnchorEl(target);
    setOpen(true);
  };

  const handleScheduleClose = () => {
    clearCloseTimer();
    closeTimerRef.current = setTimeout(() => {
      setOpen(false);
      closeTimerRef.current = null;
    }, 110);
  };

  return (
    <div className="key-models-wrap">
      <button
        ref={setAnchorEl}
        type="button"
        className="key-models-cell"
        data-testid={`key-models-trigger-${keyId}`}
        data-row-interactive="true"
        aria-expanded={open}
        aria-label={`${formatKeyModelsPopoverTitle(usageWindow)}: ${formatKeyModelsSummaryLine(totalModels)}`}
        onPointerDown={() => {
          pointerDownRef.current = true;
        }}
        onMouseEnter={(event) => handleOpen(event.currentTarget)}
        onMouseLeave={handleScheduleClose}
        onFocus={(event) => {
          if (pointerDownRef.current) return;
          handleOpen(event.currentTarget);
        }}
        onBlur={() => {
          pointerDownRef.current = false;
          handleScheduleClose();
        }}
        onClick={(event) => {
          event?.stopPropagation();
          pointerDownRef.current = false;
          clearCloseTimer();
          if (open) {
            setOpen(false);
          } else {
            handleOpen(event?.currentTarget ?? anchorEl);
          }
        }}
      >
        <span className="key-models-top">
          {visibleModels.map((entry) => (
            <span
              key={entry.model}
              className={'key-model-chip' + (visibleModels.length > 1 ? ' is-pair' : '')}
            >
              <span className="key-model-chip-name">{entry.model}</span>
            </span>
          ))}
          {extraCount > 0 && <span className="key-model-more">+{extraCount}</span>}
        </span>
        {showSummaryLine && (
          <span className="key-models-secondary">{formatKeyModelsSummaryLine(totalModels)}</span>
        )}
      </button>

      <KeyFloatingPopover
        open={open}
        anchorEl={anchorEl}
        testId={`key-models-popover-${keyId}`}
        className="key-floating-popover key-models-popover"
        onMouseEnter={() => handleOpen(anchorEl)}
        onMouseLeave={handleScheduleClose}
        onDismiss={() => setOpen(false)}
      >
        <div className="key-models-popover-header">
          <span>{formatKeyModelsPopoverTitle(usageWindow)}</span>
          <span>{formatKeyModelsSummaryLine(totalModels)}</span>
        </div>
        <div className="key-models-popover-list">
          {topModels.map((entry, idx) => {
            const sharePct = popoverPcts[idx] ?? 0;
            const entryCost = formatUsageCostText(entry.estimatedCostUsd, state);
            return (
              <div key={entry.model} className="key-models-popover-row">
                <code className="key-models-popover-name">{entry.model}</code>
                <span className="key-models-popover-stats">
                  <strong>
                    {usageCountText(entry.calls, state)} {k('callsUnit')}
                  </strong>
                  <small>({sharePct}%)</small>
                  {entryCost && <span className="key-models-popover-cost">{entryCost}</span>}
                </span>
              </div>
            );
          })}
          {totalModels > topModels.length && (
            <div className="key-models-popover-more-note">
              +{totalModels - topModels.length} {k('modelsTotalSuffix')}
            </div>
          )}
        </div>
      </KeyFloatingPopover>
    </div>
  );
}

function KeyUsageBreakdownPopover({
  open,
  anchorEl,
  keyId,
  usageWindow,
  item,
  onMouseEnter,
  onMouseLeave,
  onDismiss,
}: {
  open: boolean;
  anchorEl: HTMLElement | null;
  keyId: string;
  usageWindow: KeyListUsageWindow;
  item: UsageSummaryItem;
  onMouseEnter: () => void;
  onMouseLeave: () => void;
  onDismiss: () => void;
}) {
  const state = item.coverage.state;
  const calls = item.calls;
  const failure = item.failure ?? 0;
  const success = item.success ?? Math.max(0, calls - failure);
  const inputTokens = item.inputTokens;
  const outputTokens = item.outputTokens;
  const cachedTokens = item.cachedTokens ?? 0;
  const costText = formatUsageCostText(item.estimatedCostUsd, state);

  return (
    <KeyFloatingPopover
      open={open}
      anchorEl={anchorEl}
      testId={`key-usage-popover-${keyId}`}
      className="key-floating-popover key-usage-popover"
      onMouseEnter={onMouseEnter}
      onMouseLeave={onMouseLeave}
      onDismiss={onDismiss}
    >
      <div className="key-models-popover-header">
        <span>{formatKeyUsageColumnHeader(usageWindow)}</span>
        <span className={'key-state-pill key-state-pill-compact ' + usageCoveragePill(state)}>
          {usageStateText(state)}
        </span>
      </div>
      <div className="key-usage-popover-grid">
        <div className="key-usage-popover-row">
          <span className="key-usage-popover-label">{k('usageCallsBreakdownLabel')}</span>
          <span className="key-usage-popover-val">
            <strong>
              {usageCountText(calls, state)} {k('callsUnit')}
            </strong>
            <small>
              {k('usageSuccessShort')} {usageCountText(success, state)} · {k('usageFailureShort')}{' '}
              {usageCountText(failure, state)}
            </small>
          </span>
        </div>
        {inputTokens !== undefined && (
          <div className="key-usage-popover-row">
            <span className="key-usage-popover-label">{k('usageInputTokensLabel')}</span>
            <span className="key-usage-popover-val is-mono">
              {usageCountText(inputTokens, state)}
            </span>
          </div>
        )}
        {outputTokens !== undefined && (
          <div className="key-usage-popover-row">
            <span className="key-usage-popover-label">{k('usageOutputTokensLabel')}</span>
            <span className="key-usage-popover-val is-mono">
              {usageCountText(outputTokens, state)}
            </span>
          </div>
        )}
        {cachedTokens > 0 && (
          <div className="key-usage-popover-row">
            <span className="key-usage-popover-label">{k('usageCachedTokensLabel')}</span>
            <span className="key-usage-popover-val is-mono">
              {usageCountText(cachedTokens, state)}
            </span>
          </div>
        )}
        {costText && costText !== '—' && (
          <div className="key-usage-popover-row">
            <span className="key-usage-popover-label">{k('sortFieldCost')}</span>
            <span className="key-usage-popover-val is-mono">
              <strong>{costText}</strong>
            </span>
          </div>
        )}
      </div>
      {state !== 'complete' && item.coverage.reason && (
        <div className="key-usage-popover-footnote">{usageReasonText(item.coverage.reason)}</div>
      )}
    </KeyFloatingPopover>
  );
}

interface KeyHistoryMetricView {
  key: 'requests' | 'tokens' | 'cost' | 'success';
  toneClass: 'is-requests' | 'is-tokens' | 'is-cost' | 'is-success';
  label: string;
  icon: ReactNode;
  value: string;
  exact: string;
  muted: boolean;
  valueToneClass?: string;
}

function buildKeyHistoryMetrics(
  item: UsageSummaryItem | undefined,
  card: boolean
): KeyHistoryMetricView[] {
  const state: UsageState = item?.coverage.state ?? 'unknown';
  const proven = state === 'complete' || state === 'partial';
  const emptySymbol = proven ? '-' : '—';
  const calls = item?.calls ?? 0;
  const hasCalls = proven && calls > 0;
  const failure = item?.failure ?? 0;
  const success = item?.success ?? Math.max(0, calls - failure);
  const totalTokens = item?.totalTokens;
  const estimatedCostUsd = item?.estimatedCostUsd;
  const iconSize = card ? 12 : 13;

  const requestsValue = hasCalls ? usageCompactNumberText(calls, state) : emptySymbol;
  const requestsExact = hasCalls ? usageCountText(calls, state) : emptySymbol;

  const hasTokens = hasCalls && totalTokens !== undefined && totalTokens > 0;
  const tokensValue = hasTokens ? usageCompactNumberText(totalTokens, state) : emptySymbol;
  const tokensExact =
    hasCalls && totalTokens !== undefined ? usageCountText(totalTokens, state) : emptySymbol;

  const formattedCost = hasCalls ? formatUsageCostText(estimatedCostUsd, state) : null;
  const costValue =
    formattedCost !== null
      ? formattedCost
      : hasCalls && state === 'partial' && estimatedCostUsd === 0
        ? '—'
        : emptySymbol;
  const costExact = costValue;
  const successRatio = hasCalls ? success / calls : 0;
  const successValue = hasCalls
    ? successRatio >= 0.9995
      ? '100%'
      : `${(successRatio * 100).toFixed(1)}%`
    : emptySymbol;
  const successExact = hasCalls ? `${(successRatio * 100).toFixed(2)}%` : emptySymbol;

  return [
    {
      key: 'requests',
      toneClass: 'is-requests',
      label: k('cardMetricRequests'),
      icon: <IconSend size={iconSize} />,
      value: requestsValue,
      exact: requestsExact,
      muted: !hasCalls,
    },
    {
      key: 'tokens',
      toneClass: 'is-tokens',
      label: k('cardMetricTokens'),
      icon: <IconBinary size={iconSize} />,
      value: tokensValue,
      exact: tokensExact,
      muted: !hasTokens,
    },
    {
      key: 'cost',
      toneClass: 'is-cost',
      label: k('cardMetricCost'),
      icon: <IconDollarSign size={iconSize} />,
      value: costValue,
      exact: costExact,
      muted: formattedCost === null,
    },
    {
      key: 'success',
      toneClass: 'is-success',
      label: k('cardMetricSuccessRate'),
      icon: <IconCheck size={iconSize} />,
      value: successValue,
      exact: successExact,
      muted: !hasCalls,
      valueToneClass: hasCalls
        ? successRatio >= 0.95
          ? 'is-tone-good'
          : successRatio >= 0.85
            ? 'is-tone-warn'
            : 'is-tone-bad'
        : undefined,
    },
  ];
}

function KeyTableUsageCell({
  keyId,
  usageWindow,
  item,
  disabled,
  onActivate,
}: {
  keyId: string;
  usageWindow: KeyListUsageWindow;
  item: UsageSummaryItem | undefined;
  disabled: boolean;
  onActivate: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [anchorEl, setAnchorEl] = useState<HTMLElement | null>(null);
  const closeTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const clearCloseTimer = () => {
    if (closeTimerRef.current !== null) {
      clearTimeout(closeTimerRef.current);
      closeTimerRef.current = null;
    }
  };

  useEffect(() => () => clearCloseTimer(), []);

  const state: UsageState = item?.coverage.state ?? 'unknown';
  const proven = state === 'complete' || state === 'partial';
  const calls = item?.calls ?? 0;
  const reasonTitle = item ? usageReasonText(item.coverage.reason) : k('usageStateUnknown');
  const metrics = buildKeyHistoryMetrics(item, false);
  const canHoverBreakdown = Boolean(item && proven && calls > 0);

  useEffect(() => {
    if (!canHoverBreakdown) {
      clearCloseTimer();
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setOpen(false);
    }
  }, [canHoverBreakdown]);
  const handleOpen = (target: HTMLElement | null) => {
    if (!canHoverBreakdown) return;
    clearCloseTimer();
    if (target) setAnchorEl(target);
    setOpen(true);
  };

  const handleScheduleClose = () => {
    clearCloseTimer();
    closeTimerRef.current = setTimeout(() => {
      setOpen(false);
      closeTimerRef.current = null;
    }, 110);
  };

  return (
    <div className="key-usage-interactive-wrap">
      <button
        ref={setAnchorEl}
        type="button"
        className="key-usage-trigger"
        data-testid={'open-key-usage-' + keyId}
        disabled={disabled}
        title={canHoverBreakdown ? undefined : reasonTitle}
        onMouseEnter={(event) => handleOpen(event.currentTarget)}
        onMouseLeave={handleScheduleClose}
        onFocus={(event) => handleOpen(event.currentTarget)}
        onBlur={handleScheduleClose}
        onClick={(event) => {
          event?.stopPropagation();
          setOpen(false);
          onActivate();
        }}
      >
        <div className="key-usage-col-stack">
          <span className="key-history-grid">
            {metrics.map((metric) => (
              <span
                key={metric.key}
                className={`key-history-metric ${metric.toneClass}`}
                title={`${metric.label}: ${metric.exact}`}
                aria-label={`${metric.label}: ${metric.exact}`}
              >
                <span className="key-history-icon" aria-hidden="true">
                  {metric.icon}
                </span>
                <strong
                  className={
                    [metric.key === 'requests' ? 'key-stat-total' : '', metric.muted ? 'is-muted' : '']
                      .filter(Boolean)
                      .join(' ') || undefined
                  }
                >
                  {metric.value}
                </strong>
              </span>
            ))}
          </span>
          {state !== 'complete' && (
            <div className="key-usage-secondary-line">
              <span className={'key-state-pill key-state-pill-compact ' + usageCoveragePill(state)}>
                {usageStateText(state)}
              </span>
            </div>
          )}
        </div>
      </button>
      {canHoverBreakdown && item && (
        <KeyUsageBreakdownPopover
          open={open}
          anchorEl={anchorEl}
          keyId={keyId}
          usageWindow={usageWindow}
          item={item}
          onMouseEnter={() => handleOpen(anchorEl)}
          onMouseLeave={handleScheduleClose}
          onDismiss={() => setOpen(false)}
        />
      )}
    </div>
  );
}

function KeyCardUsageSection({
  keyId,
  usageWindow,
  item,
  disabled,
  onOpenUsage,
}: {
  keyId: string;
  usageWindow: KeyListUsageWindow;
  item: UsageSummaryItem | undefined;
  disabled: boolean;
  onOpenUsage: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [anchorEl, setAnchorEl] = useState<HTMLElement | null>(null);
  const closeTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const clearCloseTimer = () => {
    if (closeTimerRef.current !== null) {
      clearTimeout(closeTimerRef.current);
      closeTimerRef.current = null;
    }
  };

  useEffect(() => () => clearCloseTimer(), []);

  const state: UsageState = item?.coverage.state ?? 'unknown';
  const proven = state === 'complete' || state === 'partial';
  const calls = item?.calls ?? 0;
  const reasonTitle = item ? usageReasonText(item.coverage.reason) : k('usageStateUnknown');
  const metrics = buildKeyHistoryMetrics(item, true);
  const canHoverBreakdown = Boolean(item && proven && calls > 0);

  useEffect(() => {
    if (!canHoverBreakdown) {
      clearCloseTimer();
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setOpen(false);
    }
  }, [canHoverBreakdown]);
  const handleOpen = (target: HTMLElement | null) => {
    if (!canHoverBreakdown) return;
    clearCloseTimer();
    if (target) setAnchorEl(target);
    setOpen(true);
  };

  const handleScheduleClose = () => {
    clearCloseTimer();
    closeTimerRef.current = setTimeout(() => {
      setOpen(false);
      closeTimerRef.current = null;
    }, 110);
  };

  return (
    <div className="key-card-history" data-testid={'key-card-history-' + keyId}>
      <div className="key-card-section-header">
        <strong className="key-card-history-title">
          {formatKeyUsageColumnHeader(usageWindow)}
        </strong>
        {!proven && (
          <span className={'key-state-pill key-state-pill-compact ' + usageCoveragePill(state)}>
            {usageStateText(state)}
          </span>
        )}
      </div>

      <button
        ref={setAnchorEl}
        type="button"
        className="key-card-kpi-trigger"
        data-testid={'open-card-usage-' + keyId}
        disabled={disabled}
        title={canHoverBreakdown ? undefined : reasonTitle}
        onMouseEnter={(event) => handleOpen(event.currentTarget)}
        onMouseLeave={handleScheduleClose}
        onFocus={(event) => handleOpen(event.currentTarget)}
        onBlur={handleScheduleClose}
        onClick={(event) => {
          event?.stopPropagation();
          setOpen(false);
          onOpenUsage();
        }}
      >
        <span className="key-history-grid is-card">
          {metrics.map((metric) => (
            <span
              key={metric.key}
              className={`key-history-metric ${metric.toneClass}`}
              title={`${metric.label}: ${metric.exact}`}
              aria-label={`${metric.label}: ${metric.exact}`}
            >
              <span className="key-history-icon" aria-hidden="true">
                {metric.icon}
              </span>
              <span className="key-history-label">{metric.label}</span>
              <strong className={metric.muted ? 'is-muted' : undefined}>{metric.value}</strong>
            </span>
          ))}
        </span>
      </button>

      {canHoverBreakdown && item && (
        <KeyUsageBreakdownPopover
          open={open}
          anchorEl={anchorEl}
          keyId={keyId}
          usageWindow={usageWindow}
          item={item}
          onMouseEnter={() => handleOpen(anchorEl)}
          onMouseLeave={handleScheduleClose}
          onDismiss={() => setOpen(false)}
        />
      )}
    </div>
  );
}

const CARD_STATUS_SLOT_COUNT = 20;

function buildCardStatusSlots(item: UsageSummaryItem | undefined): UsageStatusSlot[] {
  const state = item?.coverage.state ?? 'unavailable';
  const proven = state === 'complete' || state === 'partial';
  if (proven && item?.recentSlots && item.recentSlots.length > 0) {
    if (item.recentSlots.length === CARD_STATUS_SLOT_COUNT) {
      return item.recentSlots;
    }
    if (item.recentSlots.length > CARD_STATUS_SLOT_COUNT) {
      return item.recentSlots.slice(-CARD_STATUS_SLOT_COUNT);
    }
    const missing = CARD_STATUS_SLOT_COUNT - item.recentSlots.length;
    return [
      ...Array.from({ length: missing }, () => ({
        startMs: 0,
        endMs: 0,
        success: 0,
        failure: 0,
      })),
      ...item.recentSlots,
    ];
  }
  return Array.from({ length: CARD_STATUS_SLOT_COUNT }, () => ({
    startMs: 0,
    endMs: 0,
    success: 0,
    failure: 0,
  }));
}

function formatSlotRangeLabel(slot: UsageStatusSlot, usageWindow: KeyListUsageWindow): string | null {
  if (slot.startMs <= 0 || slot.endMs <= 0) return null;
  const mode = usageWindow === '24h' ? 'recent' : 'created';
  const start = usageCompactTimeText(slot.startMs, mode);
  const end = usageCompactTimeText(slot.endMs, mode);
  if (!start) return null;
  if (!end || start === end) return start;
  return `${start} – ${end}`;
}

function KeyCardRecentStatusSection({
  keyId,
  usageWindow,
  item,
  disabled,
  onOpenUsage,
}: {
  keyId: string;
  usageWindow: KeyListUsageWindow;
  item: UsageSummaryItem | undefined;
  disabled: boolean;
  onOpenUsage: () => void;
}) {
  const [activeSlotIdx, setActiveSlotIdx] = useState<number | null>(null);
  const state = item?.coverage.state ?? 'unavailable';
  const proven = state === 'complete' || state === 'partial';
  const calls = proven ? (item?.calls ?? 0) : 0;
  const failureCount = proven ? (item?.failure ?? 0) : 0;
  const successCount = proven ? (item?.success ?? Math.max(0, calls - failureCount)) : 0;
  const totalCount = successCount + failureCount;
  const successRatePct = totalCount > 0 ? (successCount / totalCount) * 100 : null;
  const rateText =
    successRatePct !== null
      ? successRatePct >= 99.95
        ? '100%'
        : `${successRatePct.toFixed(1).replace(/\.0$/, '')}%`
      : '--';
  const rateToneClass =
    successRatePct === null
      ? ''
      : successRatePct >= 90
        ? 'is-high'
        : successRatePct >= 50
          ? 'is-medium'
          : 'is-low';
  const slots = buildCardStatusSlots(item);

  return (
    <div
      className="key-card-recent-status"
      data-testid={'key-card-recent-status-' + keyId}
      data-row-interactive="true"
      role={disabled ? undefined : 'button'}
      tabIndex={disabled ? undefined : 0}
      aria-label={
        disabled
          ? undefined
          : `${k('cardRecentStatusTitle')}: ${formatKeyUsageColumnHeader(usageWindow)}`
      }
      onClick={
        disabled
          ? undefined
          : (event) => {
              event.stopPropagation();
              onOpenUsage();
            }
      }
      onKeyDown={
        disabled
          ? undefined
          : (event) => {
              if (event.key === 'Enter' || event.key === ' ') {
                event.preventDefault();
                event.stopPropagation();
                onOpenUsage();
              }
            }
      }
    >
      <div className="key-card-recent-header">
        <strong className="key-card-recent-title">{k('cardRecentStatusTitle')}</strong>
        <div className="key-card-recent-metrics">
          <span
            className="key-card-recent-pill-success"
            title={`${k('usageSuccessShort')}: ${usageCountText(successCount, state)}`}
          >
            <span className="key-metric-dot" aria-hidden="true" />
            <strong>{proven ? usageCompactNumberText(successCount, state) : '—'}</strong>
          </span>
          <span
            className={'key-card-recent-pill-failure' + (failureCount > 0 ? ' has-failure' : '')}
            title={`${k('usageFailureShort')}: ${usageCountText(failureCount, state)}`}
          >
            <span className="key-metric-dot" aria-hidden="true" />
            <strong>{proven ? usageCompactNumberText(failureCount, state) : '—'}</strong>
          </span>
        </div>
      </div>

      <div className="key-card-status-bar">
        <div className="key-status-blocks">
          {slots.map((slot, idx) => {
            const slotTotal = slot.success + slot.failure;
            const isIdle = slotTotal === 0;
            const slotRate = isIdle ? -1 : slot.success / slotTotal;
            const blockStyle = isIdle
              ? undefined
              : { backgroundColor: interpolateStatusBarColor(slotRate) };
            const isActive = activeSlotIdx === idx;
            const posClass =
              idx <= 2 ? ' is-left' : idx >= slots.length - 3 ? ' is-right' : '';
            const timeLabel = formatSlotRangeLabel(slot, usageWindow);

            return (
              <div
                key={idx}
                className={'key-status-block-wrap' + (isActive ? ' is-active' : '')}
                onPointerEnter={(e) => {
                  if (e.pointerType === 'mouse') setActiveSlotIdx(idx);
                }}
                onPointerLeave={(e) => {
                  if (e.pointerType === 'mouse') setActiveSlotIdx(null);
                }}
              >
                <div
                  className={'key-status-block' + (isIdle ? ' is-idle' : '')}
                  style={blockStyle}
                />
                {isActive && (
                  <div className={'key-status-tooltip' + posClass}>
                    {timeLabel && <span className="key-status-tooltip-time">{timeLabel}</span>}
                    {slotTotal > 0 ? (
                      <span className="key-status-tooltip-stats">
                        <span className="key-status-tooltip-ok">
                          {k('usageSuccessShort')} {usageCountText(slot.success, state)}
                        </span>
                        <span className="key-status-tooltip-err">
                          {k('usageFailureShort')} {usageCountText(slot.failure, state)}
                        </span>
                        <span className="key-status-tooltip-rate">
                          ({(slotRate * 100).toFixed(1).replace(/\.0$/, '')}%)
                        </span>
                      </span>
                    ) : (
                      <span className="key-status-tooltip-stats">{k('noUsageInWindow')}</span>
                    )}
                  </div>
                )}
              </div>
            );
          })}
        </div>
        <strong className={'key-status-rate ' + rateToneClass}>{rateText}</strong>
      </div>
    </div>
  );
}

function KeyCardModelsSection({
  keyId,
  usageWindow,
  item,
  disabled,
  onOpenUsage,
}: {
  keyId: string;
  usageWindow: KeyListUsageWindow;
  item: UsageSummaryItem | undefined;
  disabled: boolean;
  onOpenUsage: () => void;
}) {
  const [popoverOpen, setPopoverOpen] = useState(false);
  const [anchorEl, setAnchorEl] = useState<HTMLElement | null>(null);
  const closeTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const pointerDownRef = useRef(false);

  const clearCloseTimer = () => {
    if (closeTimerRef.current !== null) {
      clearTimeout(closeTimerRef.current);
      closeTimerRef.current = null;
    }
  };

  const state = item?.coverage.state ?? 'unavailable';
  const proven = state === 'complete' || state === 'partial';
  const rawModels = proven ? (item?.topModels ?? []) : [];
  const hasRankedModels = rawModels.length > 0;

  useEffect(() => {
    return () => {
      if (closeTimerRef.current !== null) clearTimeout(closeTimerRef.current);
    };
  }, []);

  useEffect(() => {
    if (!hasRankedModels) {
      if (closeTimerRef.current !== null) {
        clearTimeout(closeTimerRef.current);
        closeTimerRef.current = null;
      }
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setPopoverOpen(false);
    }
  }, [hasRankedModels]);

  const totalCalls = Math.max(
    proven ? (item?.calls ?? 0) : 0,
    rawModels.reduce((sum, entry) => sum + entry.calls, 0)
  );
  const totalCost = Math.max(
    proven && item?.estimatedCostUsd !== undefined ? item.estimatedCostUsd : 0,
    rawModels.reduce((sum, entry) => sum + (entry.estimatedCostUsd ?? 0), 0)
  );
  const rankByCost = totalCost > 0;
  const rankedModels = [...rawModels].sort((a, b) => {
    if (rankByCost) {
      const costDiff = (b.estimatedCostUsd ?? 0) - (a.estimatedCostUsd ?? 0);
      if (Math.abs(costDiff) > 1e-9) return costDiff;
    }
    if (b.calls !== a.calls) return b.calls - a.calls;
    return a.model.localeCompare(b.model);
  });
  const visibleModels = rankedModels.slice(0, 3);
  const totalModelCount = Math.max(item?.modelCount ?? rankedModels.length, rankedModels.length);
  const extraCount = Math.max(0, totalModelCount - visibleModels.length);
  const visibleCostSum = visibleModels.reduce((sum, entry) => sum + (entry.estimatedCostUsd ?? 0), 0);
  const visibleCallsSum = visibleModels.reduce((sum, entry) => sum + entry.calls, 0);
  const rawOtherCost = totalCost - visibleCostSum;
  const otherCost = rawOtherCost > 1e-9 ? rawOtherCost : 0;
  const otherCalls = Math.max(0, totalCalls - visibleCallsSum);
  const otherShareRatio = rankByCost
    ? totalCost > 0
      ? otherCost / totalCost
      : 0
    : totalCalls > 0
      ? otherCalls / totalCalls
      : 0;
  const segmentWeights = [
    ...visibleModels.map((entry) => (rankByCost ? (entry.estimatedCostUsd ?? 0) : entry.calls)),
    ...(extraCount > 0 ? [rankByCost ? otherCost : otherCalls] : []),
  ];
  const segmentPcts = computeLargestRemainderPercentages(
    segmentWeights,
    rankByCost ? totalCost : totalCalls
  );
  const visibleSharePcts = segmentPcts.slice(0, visibleModels.length);
  const otherSharePct = extraCount > 0 ? (segmentPcts[visibleModels.length] ?? 0) : 0;
  const popoverPcts = computeLargestRemainderPercentages(
    rankedModels.map((entry) => entry.calls),
    totalCalls
  );
  const otherSummaryText =
    extraCount > 0
      ? rankByCost && otherCost > 0
        ? formatKeyOtherModelsSummary({
            count: extraCount,
            amount: formatUsageCostText(otherCost, state),
            pct: otherSharePct,
          })
        : rankByCost && otherCalls > 0
          ? formatKeyOtherModelsSummary({
              count: extraCount,
              callsText: `${usageCompactNumberText(otherCalls, state)} ${k('callsUnit')}`,
            })
          : formatKeyOtherModelsSummary({
              count: extraCount,
              pct: otherSharePct,
            })
      : null;
  const handleOpenPopover = (target: HTMLElement | null) => {
    if (rankedModels.length === 0) return;
    clearCloseTimer();
    if (target) setAnchorEl(target);
    setPopoverOpen(true);
  };

  const handleScheduleClose = () => {
    clearCloseTimer();
    closeTimerRef.current = setTimeout(() => {
      setPopoverOpen(false);
      closeTimerRef.current = null;
    }, 110);
  };

  return (
    <div className="key-card-models-quota" data-testid={'key-card-models-' + keyId}>
      <div className="key-card-recent-header">
        <strong className="key-card-recent-title">{k('cardTopModelsTitle')}</strong>
        {rankedModels.length > 0 && (
          <button
            ref={setAnchorEl}
            type="button"
            className="key-card-models-more-trigger"
            data-testid={'key-models-trigger-' + keyId}
            data-row-interactive="true"
            aria-expanded={popoverOpen}
            onPointerDown={() => {
              pointerDownRef.current = true;
            }}
            onMouseEnter={(e) => handleOpenPopover(e.currentTarget)}
            onMouseLeave={handleScheduleClose}
            onFocus={(e) => {
              if (pointerDownRef.current) return;
              handleOpenPopover(e.currentTarget);
            }}
            onBlur={() => {
              pointerDownRef.current = false;
              handleScheduleClose();
            }}
            onClick={(e) => {
              e?.stopPropagation();
              pointerDownRef.current = false;
              clearCloseTimer();
              if (popoverOpen) setPopoverOpen(false);
              else handleOpenPopover(e?.currentTarget ?? anchorEl);
            }}
          >
            <span className="key-card-models-total">
              {formatKeyModelsSummaryLine(totalModelCount)}
            </span>
            {extraCount > 0 && <strong className="key-model-more">+{extraCount}</strong>}
          </button>
        )}
      </div>

      {visibleModels.length > 0 ? (
        <div
          className="key-card-model-list"
          data-row-interactive="true"
          role={disabled ? undefined : 'button'}
          tabIndex={disabled ? undefined : 0}
          aria-label={
            disabled
              ? undefined
              : `${k('cardTopModelsTitle')}: ${formatKeyUsageColumnHeader(usageWindow)}`
          }
          onClick={
            disabled
              ? undefined
              : (event) => {
                  event.stopPropagation();
                  onOpenUsage();
                }
          }
          onKeyDown={
            disabled
              ? undefined
              : (event) => {
                  if (event.key === 'Enter' || event.key === ' ') {
                    event.preventDefault();
                    event.stopPropagation();
                    onOpenUsage();
                  }
                }
          }
        >
          <div className="key-model-mix-bar" aria-hidden="true">
            {visibleModels.map((entry, idx) => {
              const shareRatio = rankByCost
                ? totalCost > 0
                  ? (entry.estimatedCostUsd ?? 0) / totalCost
                  : 0
                : totalCalls > 0
                  ? entry.calls / totalCalls
                  : 0;
              const segPct = visibleSharePcts[idx] ?? 0;
              return (
                <span
                  key={entry.model}
                  className={`key-model-mix-seg is-seg-${idx}`}
                  style={{ flexGrow: Math.max(0.04, shareRatio) }}
                  title={`${entry.model}: ${segPct}%`}
                />
              );
            })}
            {extraCount > 0 && otherShareRatio > 0.005 && (
              <span
                className="key-model-mix-seg is-seg-other"
                style={{ flexGrow: Math.max(0.04, otherShareRatio) }}
                title={otherSummaryText ?? undefined}
              />
            )}
          </div>

          <div className="key-model-mix-rows">
            {visibleModels.map((entry, idx) => {
              const sharePct = visibleSharePcts[idx] ?? 0;
              const entryCost = formatUsageCostText(entry.estimatedCostUsd, state);
              const subMetaParts: string[] = [
                `${usageCompactNumberText(entry.calls, state)} ${k('callsUnit')}`,
              ];
              if (entry.totalTokens !== undefined) {
                subMetaParts.push(usageCompactNumberText(entry.totalTokens, state));
              }

              return (
                <div key={entry.model} className="key-model-mix-row">
                  <span className="key-model-mix-name-wrap">
                    <span className={`key-model-mix-dot is-seg-${idx}`} aria-hidden="true" />
                    <span className="key-model-mix-name" title={entry.model}>
                      {entry.model}
                    </span>
                  </span>
                  <span className="key-model-mix-stats">
                    {entryCost !== null ? (
                      <>
                        <span className="key-model-mix-sub">{subMetaParts.join(' · ')}</span>
                        <strong className="key-model-mix-primary">{entryCost}</strong>
                      </>
                    ) : (
                      <>
                        {entry.totalTokens !== undefined && (
                          <span className="key-model-mix-sub">
                            {usageCompactNumberText(entry.totalTokens, state)}
                          </span>
                        )}
                        <strong className="key-model-mix-primary">
                          {usageCompactNumberText(entry.calls, state)} {k('callsUnit')}
                        </strong>
                      </>
                    )}
                    <span className="key-model-mix-pct">{sharePct}%</span>
                  </span>
                </div>
              );
            })}
          </div>

          {otherSummaryText && (
            <div className="key-model-mix-other">
              <span className="key-model-mix-dot is-seg-other" aria-hidden="true" />
              <span>{otherSummaryText}</span>
            </div>
          )}
        </div>
      ) : (
        <div className="key-card-models-empty">{k('cardModelsEmpty')}</div>
      )}

      {rankedModels.length > 0 && (
        <KeyFloatingPopover
          open={popoverOpen}
          anchorEl={anchorEl}
          testId={`key-models-popover-${keyId}`}
          className="key-floating-popover key-models-popover"
          onMouseEnter={() => handleOpenPopover(anchorEl)}
          onMouseLeave={handleScheduleClose}
          onDismiss={() => setPopoverOpen(false)}
        >
          <div className="key-models-popover-header">
            <span>{formatKeyModelsPopoverTitle(usageWindow)}</span>
            <span>{formatKeyModelsSummaryLine(totalModelCount)}</span>
          </div>
          <div className="key-models-popover-list">
            {rankedModels.map((entry, idx) => {
              const sharePct = popoverPcts[idx] ?? 0;
              const entryCost = formatUsageCostText(entry.estimatedCostUsd, state);
              return (
                <div key={entry.model} className="key-models-popover-row">
                  <code className="key-models-popover-name">{entry.model}</code>
                  <span className="key-models-popover-stats">
                    <strong>
                      {usageCountText(entry.calls, state)} {k('callsUnit')}
                    </strong>
                    <small>({sharePct}%)</small>
                    {entryCost && <span className="key-models-popover-cost">{entryCost}</span>}
                  </span>
                </div>
              );
            })}
            {totalModelCount > rankedModels.length && (
              <div className="key-models-popover-more-note">
                +{totalModelCount - rankedModels.length} {k('modelsTotalSuffix')}
              </div>
            )}
          </div>
        </KeyFloatingPopover>
      )}
    </div>
  );
}

function buildKeyActionMenuItems({
  keyItem,
  locked,
  operationPending,
  revocationAvailable,
  isRowCopying,
  onCopySecret,
  onRename,
  onRegenerate,
  onRevoke,
}: {
  keyItem: Key;
  locked: boolean;
  operationPending: boolean;
  revocationAvailable: boolean;
  isRowCopying: boolean;
  onCopySecret: (keyItem: Key) => void;
  onRename: (keyItem: Key) => void;
  onRegenerate: (keyId: string) => void;
  onRevoke: (keyId: string) => void;
}): DropdownMenuItem[] {
  const isSuperseded = keyItem.lifecycle === 'superseded';
  const items: DropdownMenuItem[] = [];

  if (!isSuperseded) {
    items.push({
      key: 'copy',
      label: k('copyFullKey'),
      icon: <FullWebIcon name="copy" size={14} />,
      disabled: locked || isRowCopying,
      onClick: () => onCopySecret(keyItem),
    });
  }

  items.push({
    key: 'rename',
    label: k('rename'),
    icon: <FullWebIcon name="edit" size={14} />,
    disabled: locked,
    onClick: () => onRename(keyItem),
  });

  if (revocationAvailable && !isSuperseded) {
    items.push({
      key: 'regenerate',
      label: k('regenerateKey'),
      icon: <FullWebIcon name="rotate" size={14} />,
      disabled: locked || operationPending,
      onClick: () => onRegenerate(keyItem.id),
    });
    items.push({
      key: 'revoke',
      label: k('revokeModalTitle'),
      icon: <FullWebIcon name="ban" size={14} />,
      tone: 'danger',
      disabled: locked || operationPending,
      onClick: () => onRevoke(keyItem.id),
    });
  }

  return items;
}

export function FreshApiKeyListViews({
  viewMode,
  usageWindow = '24h',
  keys,
  usageById,
  selectedId,
  locked,
  operationPending,
  revocationAvailable,
  rowCopyState,
  onOpenDetails,
  onCopySecret,
  onRename,
  onRegenerate,
  onRevoke,
}: FreshApiKeyListViewsProps) {
  if (viewMode === 'cards') {
    return (
      <div className="key-card-grid" data-testid="key-card-grid">
        {keys.map((keyItem) => {
          const isSuperseded = keyItem.lifecycle === 'superseded';
          const isSelected = selectedId === keyItem.id;
          const displayName = keyDisplayName(
            keyItem,
            k('installerOrigin'),
            k('adoptedOrigin'),
            t('keysUnnamed')
          );
          const originText = resolveKeyOriginText(keyItem.origin);
          const statusInfo = keyUnifiedStatus(keyItem);
          const usageItem = usageById.get(keyItem.id);
          const isRowCopying =
            rowCopyState?.id === keyItem.id && rowCopyState.status === 'copying';
          const rowCopied =
            rowCopyState?.id === keyItem.id && rowCopyState.status === 'copied';
          const rowCopyFailed =
            rowCopyState?.id === keyItem.id && rowCopyState.status === 'failed';
          const createdCompactText = usageCompactTimeText(keyItem.createdAtMs, 'created');
          const createdFullText = usageTimeText(keyItem.createdAtMs);
          const rotatedCompactText = keyItem.lastRotatedAtMs
            ? usageCompactTimeText(keyItem.lastRotatedAtMs, 'created')
            : null;
          const rotatedFullText = keyItem.lastRotatedAtMs
            ? usageTimeText(keyItem.lastRotatedAtMs)
            : null;
          const revokedCompactText = keyItem.revokedAtMs
            ? usageCompactTimeText(keyItem.revokedAtMs, 'recent')
            : null;
          const revokedFullText = keyItem.revokedAtMs
            ? usageTimeText(keyItem.revokedAtMs)
            : null;

          const menuItems = buildKeyActionMenuItems({
            keyItem,
            locked,
            operationPending,
            revocationAvailable,
            isRowCopying,
            onCopySecret,
            onRename,
            onRegenerate,
            onRevoke,
          });

          return (
            <article
              key={keyItem.id}
              data-testid={'key-card-' + keyItem.id}
              data-interactive={!locked}
              tabIndex={locked ? -1 : 0}
              className={
                'key-card' +
                (isSelected ? ' is-selected' : '') +
                (isSuperseded ? ' is-superseded' : '')
              }
              onClick={(event) => {
                if (locked || isInteractiveChildTarget(event.target)) return;
                onOpenDetails(keyItem.id);
              }}
              onKeyDown={(event) =>
                handleSurfaceActivateKeyDown(event, locked, () => onOpenDetails(keyItem.id))
              }
            >
              {/* 1. Refined Two-Row Identity Header + Meta Badges (Credential Card Style) */}
              <div className="key-card-header">
                <div className="key-card-head-left">
                  <span className="key-card-emblem" aria-hidden="true">
                    <IconKey size={18} />
                  </span>
                  <div className="key-card-identity">
                    <div className="key-card-copy-line">
                      <button
                        type="button"
                        className="key-card-title"
                        title={displayName}
                        disabled={locked}
                        onClick={(event) => {
                          event?.stopPropagation();
                          onOpenDetails(keyItem.id);
                        }}
                      >
                        <strong>{displayName}</strong>
                      </button>
                    </div>
                    <div className="key-card-copy-line" data-row-interactive="true">
                      {!isSuperseded ? (
                        <button
                          type="button"
                          className="key-card-secret-line"
                          data-testid={'copy-card-secret-' + keyItem.id}
                          aria-label={`${k('copyKey')}: ${displayName}`}
                          title={rowCopied ? k('copiedShort') : k('copyFullKey')}
                          disabled={locked || isRowCopying}
                          onClick={(event) => {
                            event?.stopPropagation();
                            onCopySecret(keyItem);
                          }}
                        >
                          <span className="key-card-secret-file">{keyItem.maskedPreview}</span>
                        </button>
                      ) : (
                        <span className="key-card-secret-file">{keyItem.maskedPreview}</span>
                      )}
                      {rowCopied && (
                        <strong className="key-card-copy-hint is-copied">{k('copiedShort')}</strong>
                      )}
                      {rowCopyFailed && (
                        <strong className="key-card-copy-hint is-failed">
                          {k('copyFailedShort')}
                        </strong>
                      )}
                    </div>
                  </div>
                </div>
                <div className="key-card-head-right">
                  <strong
                    className={'key-card-status-badge ' + statusInfo.pillClass}
                    data-testid={'key-status-' + keyItem.id}
                  >
                    {k(statusInfo.labelKey)}
                  </strong>
                </div>
              </div>

              <div className="key-card-meta-badges">
                <strong className="key-card-origin-badge">
                  {originText ?? k('originConsoleShort')}
                </strong>
                {isSuperseded && revokedCompactText ? (
                  <span className="key-card-meta-pill" title={revokedFullText ?? undefined}>
                    {k('timeRevokedShort')} {revokedCompactText}
                  </span>
                ) : !isSuperseded && rotatedCompactText ? (
                  <span
                    className="key-card-meta-pill"
                    title={
                      rotatedFullText
                        ? `${k('timeRotatedFullPrefix')}: ${rotatedFullText}`
                        : undefined
                    }
                  >
                    <IconRotateCcw size={11} />
                    {k('timeRotatedShort')} {rotatedCompactText}
                  </span>
                ) : createdCompactText ? (
                  <span
                    className="key-card-meta-pill"
                    title={createdFullText ? `${k('created')}: ${createdFullText}` : undefined}
                  >
                    {k('timeCreatedShort')} {createdCompactText}
                  </span>
                ) : null}
                {usageItem &&
                  (usageItem.coverage.state === 'complete' ||
                    usageItem.coverage.state === 'partial') &&
                  (usageItem.lastUsedAtMs ? (
                    <span
                      className="key-card-meta-pill"
                      title={`${k('usageLastUsed')}: ${usageTimeText(usageItem.lastUsedAtMs) ?? ''}`}
                    >
                      {k('timeActiveShort')}{' '}
                      {usageCompactTimeText(usageItem.lastUsedAtMs, 'recent')}
                    </span>
                  ) : (
                    <span className="key-card-meta-pill">{k('neverUsedShort')}</span>
                  ))}
              </div>

              {/* 2. Crisp 1x4 White Usage Mini-Cards */}
              <KeyCardUsageSection
                keyId={keyItem.id}
                usageWindow={usageWindow}
                item={usageItem}
                disabled={locked}
                onOpenUsage={() => onOpenDetails(keyItem.id, { tab: 'usage', window: usageWindow })}
              />

              {/* 3. 20-Slot Recent Status Timeline Bar */}
              <KeyCardRecentStatusSection
                keyId={keyItem.id}
                usageWindow={usageWindow}
                item={usageItem}
                disabled={locked}
                onOpenUsage={() => onOpenDetails(keyItem.id, { tab: 'usage', window: usageWindow })}
              />

              {/* 4. Top-2 Model Consumption Progress Rows */}
              <KeyCardModelsSection
                keyId={keyItem.id}
                usageWindow={usageWindow}
                item={usageItem}
                disabled={locked}
                onOpenUsage={() => onOpenDetails(keyItem.id, { tab: 'usage', window: usageWindow })}
              />

              {/* 5. Soft-Tinted Borderless Action Footer */}
              <div className="key-card-footer" data-row-interactive="true">
                <div className="key-card-footer-left">
                  {revocationAvailable && !isSuperseded && (
                    <Button
                      type="button"
                      variant="secondary"
                      size="xs"
                      iconOnly
                      className="key-card-soft-btn is-danger"
                      data-testid={'revoke-card-key-' + keyItem.id}
                      aria-label={`${k('revokeModalTitle')}: ${displayName}`}
                      title={k('revokeModalTitle')}
                      disabled={locked || operationPending}
                      onClick={(event) => {
                        event?.stopPropagation();
                        onRevoke(keyItem.id);
                      }}
                    >
                      <FullWebIcon name="ban" size={14} />
                    </Button>
                  )}
                  {!isSuperseded && (
                    <Button
                      type="button"
                      variant="secondary"
                      size="xs"
                      iconOnly
                      className="key-card-soft-btn is-neutral"
                      data-testid={'footer-copy-card-secret-' + keyItem.id}
                      aria-label={`${k('copyKey')}: ${displayName}`}
                      title={rowCopied ? k('copiedShort') : k('copyFullKey')}
                      disabled={locked || isRowCopying}
                      onClick={(event) => {
                        event?.stopPropagation();
                        onCopySecret(keyItem);
                      }}
                    >
                      {rowCopied ? <IconCheck size={14} /> : <IconCopy size={14} />}
                    </Button>
                  )}
                </div>
                <div className="key-card-footer-right">
                  <Button
                    type="button"
                    variant="secondary"
                    size="xs"
                    iconOnly
                    className="key-card-soft-btn is-primary-tint"
                    data-testid={'card-rename-' + keyItem.id}
                    aria-label={`${k('rename')}: ${displayName}`}
                    title={k('rename')}
                    disabled={locked}
                    onClick={(event) => {
                      event?.stopPropagation();
                      onRename(keyItem);
                    }}
                  >
                    <FullWebIcon name="edit" size={14} />
                  </Button>
                  {revocationAvailable && !isSuperseded && (
                    <Button
                      type="button"
                      variant="secondary"
                      size="xs"
                      iconOnly
                      className="key-card-soft-btn is-primary-tint"
                      data-testid={'card-rotate-' + keyItem.id}
                      aria-label={`${k('regenerateKey')}: ${displayName}`}
                      title={k('regenerateKey')}
                      disabled={locked || operationPending}
                      onClick={(event) => {
                        event?.stopPropagation();
                        onRegenerate(keyItem.id);
                      }}
                    >
                      <IconRotateCcw size={14} />
                    </Button>
                  )}
                  {!isSuperseded && menuItems.length > 0 && (
                    <DropdownMenu
                      items={menuItems}
                      align="end"
                      ariaLabel={`${k('moreActions')}: ${displayName}`}
                      triggerTitle={k('moreActions')}
                      triggerClassName="key-card-soft-btn is-primary-tint key-quick-btn-more"
                      triggerIcon={<FullWebIcon name="moreVertical" size={15} />}
                      disabled={locked}
                    />
                  )}
                  <Button
                    type="button"
                    variant="secondary"
                    size="xs"
                    className="key-card-detail-soft-btn"
                    data-testid={'open-key-' + keyItem.id}
                    disabled={locked}
                    onClick={(event) => {
                      event?.stopPropagation();
                      onOpenDetails(keyItem.id);
                    }}
                  >
                    <strong>{k('rowDetails')}</strong>
                  </Button>
                </div>
              </div>
            </article>
          );
        })}
      </div>
    );
  }

  return (
    <div className="key-table-scroll">
      <table className="key-table" role="table" aria-label={k('scope')} data-testid="key-table">
        <thead>
          <tr role="row">
            <th scope="col" role="columnheader" className="key-col-identity">
              {k('credential')}
            </th>
            <th scope="col" role="columnheader" className="key-col-status">
              {k('lifecycle')}
            </th>
            <th scope="col" role="columnheader" className="key-col-models">
              {k('modelsColumn')}
            </th>
            <th scope="col" role="columnheader" className="key-col-usage">
              {formatKeyUsageColumnHeader(usageWindow)}
            </th>
            <th scope="col" role="columnheader" className="key-col-time">
              {k('timeColumn')}
            </th>
            <th scope="col" role="columnheader" className="key-col-actions">
              {k('actions')}
            </th>
          </tr>
        </thead>
        <tbody>
          {keys.map((keyItem) => {
            const isSuperseded = keyItem.lifecycle === 'superseded';
            const isSelected = selectedId === keyItem.id;
            const displayName = keyDisplayName(
              keyItem,
              k('installerOrigin'),
              k('adoptedOrigin'),
              t('keysUnnamed')
            );
            const originText = resolveKeyOriginText(keyItem.origin);
            const statusInfo = keyUnifiedStatus(keyItem);
            const usageItem = usageById.get(keyItem.id);
            const usageState: UsageState = usageItem?.coverage.state ?? 'unknown';
            const usageProven = usageState === 'complete' || usageState === 'partial';
            const lastUsedCompactText = usageItem
              ? usageCompactTimeText(usageItem.lastUsedAtMs, 'recent')
              : null;
            const lastUsedFullText = usageItem
              ? usageTimeText(usageItem.lastUsedAtMs)
              : null;
            const isRowCopying =
              rowCopyState?.id === keyItem.id && rowCopyState.status === 'copying';
            const rowCopied =
              rowCopyState?.id === keyItem.id && rowCopyState.status === 'copied';
            const createdCompactText =
              usageCompactTimeText(keyItem.createdAtMs, 'created') || '—';
            const createdFullText = usageTimeText(keyItem.createdAtMs);
            const rotatedCompactText = keyItem.lastRotatedAtMs
              ? usageCompactTimeText(keyItem.lastRotatedAtMs, 'created')
              : null;
            const rotatedFullText = keyItem.lastRotatedAtMs
              ? usageTimeText(keyItem.lastRotatedAtMs)
              : null;
            const revokedCompactText = keyItem.revokedAtMs
              ? usageCompactTimeText(keyItem.revokedAtMs, 'recent')
              : null;
            const revokedFullText = keyItem.revokedAtMs
              ? usageTimeText(keyItem.revokedAtMs)
              : null;

            const menuItems = buildKeyActionMenuItems({
              keyItem,
              locked,
              operationPending,
              revocationAvailable,
              isRowCopying,
              onCopySecret,
              onRename,
              onRegenerate,
              onRevoke,
            });

            return (
              <tr
                key={keyItem.id}
                data-testid={'key-row-' + keyItem.id}
                data-interactive={!locked}
                tabIndex={locked ? -1 : 0}
                className={
                  (isSelected ? 'is-selected ' : '') + (isSuperseded ? 'is-superseded' : '')
                }
                onClick={(event) => {
                  if (locked || isInteractiveChildTarget(event.target)) return;
                  onOpenDetails(keyItem.id);
                }}
                onKeyDown={(event) =>
                  handleSurfaceActivateKeyDown(event, locked, () => onOpenDetails(keyItem.id))
                }
              >
                <td className="key-col-identity">
                  <div className="key-name-cell">
                    <div>
                      <div className="key-name-title-row">
                        <button
                          type="button"
                          className="key-name-link"
                          title={displayName}
                          disabled={locked}
                          onClick={(event) => {
                            event?.stopPropagation();
                            onOpenDetails(keyItem.id);
                          }}
                        >
                          {displayName}
                        </button>
                        {originText && <span className="key-origin-pill">{originText}</span>}
                      </div>
                      <small className="key-secret-inline" data-row-interactive="true">
                        <code>{keyItem.maskedPreview}</code>
                        {!isSuperseded && (
                          <button
                            type="button"
                            className="key-inline-copy-btn"
                            data-testid={'copy-row-secret-' + keyItem.id}
                            aria-label={`${k('copyKey')}: ${displayName}`}
                            title={rowCopied ? k('copiedShort') : k('copyFullKey')}
                            disabled={locked || isRowCopying}
                            onClick={(event) => {
                              event?.stopPropagation();
                              onCopySecret(keyItem);
                            }}
                          >
                            <FullWebIcon name={rowCopied ? 'check' : 'copy'} size={13} />
                          </button>
                        )}
                        {rowCopyState?.id === keyItem.id && rowCopyState.status !== 'copying' && (
                          <span
                            className={
                              'key-inline-copy-feedback ' +
                              (rowCopyState.status === 'copied' ? 'is-copied' : 'is-failed')
                            }
                            data-testid={'copy-row-secret-feedback-' + keyItem.id}
                            role="status"
                          >
                            {rowCopyState.status === 'copied'
                              ? k('copiedShort')
                              : k('copyFailedShort')}
                          </span>
                        )}
                      </small>
                    </div>
                  </div>
                </td>
                <td className="key-col-status">
                  <div className="key-state-stack">
                    <span
                      className={'key-state-pill ' + statusInfo.pillClass}
                      data-testid={'key-status-' + keyItem.id}
                    >
                      {k(statusInfo.labelKey)}
                    </span>
                  </div>
                </td>
                <td className="key-col-models" data-testid={'key-models-' + keyItem.id}>
                  <KeyModelsInteractive
                    keyId={keyItem.id}
                    usageWindow={usageWindow}
                    item={usageItem}
                    maxVisible={2}
                    showSummaryLine
                  />
                </td>
                <td className="key-col-usage" data-testid={'key-usage-' + keyItem.id}>
                  <KeyTableUsageCell
                    keyId={keyItem.id}
                    usageWindow={usageWindow}
                    item={usageItem}
                    disabled={locked}
                    onActivate={() =>
                      onOpenDetails(keyItem.id, { tab: 'usage', window: usageWindow })
                    }
                  />
                </td>
                <td className="key-col-time" data-testid={'key-time-' + keyItem.id}>
                  <div className="key-time-stack">
                    {lastUsedCompactText ? (
                      <span
                        className="key-time-primary is-active"
                        data-testid={'key-last-used-' + keyItem.id}
                        title={
                          lastUsedFullText
                            ? `${k('usageLastUsed')}: ${lastUsedFullText}`
                            : undefined
                        }
                      >
                        <span className="key-time-prefix">{k('timeActiveShort')}</span>
                        <span className="key-time-val">{lastUsedCompactText}</span>
                      </span>
                    ) : usageProven ? (
                      <span
                        className="key-time-primary is-unused"
                        data-testid={'key-last-used-' + keyItem.id}
                      >
                        {k('neverUsedShort')}
                      </span>
                    ) : (
                      <span
                        className="key-time-primary is-unused"
                        data-testid={'key-last-used-' + keyItem.id}
                      >
                        <span className="key-time-prefix">{k('timeActiveShort')}</span>
                        <span className="key-time-val">—</span>
                      </span>
                    )}

                    {isSuperseded && revokedCompactText ? (
                      <span
                        className="key-time-sub is-revoked"
                        data-testid={'key-revoked-at-' + keyItem.id}
                        title={
                          revokedFullText
                            ? `${k('revokedAtLabel')}: ${revokedFullText}${
                                createdFullText ? ` · ${k('created')}: ${createdFullText}` : ''
                              }`
                            : undefined
                        }
                      >
                        <span className="key-time-prefix">{k('timeRevokedShort')}</span>
                        <span className="key-time-val">{revokedCompactText}</span>
                      </span>
                    ) : rotatedCompactText ? (
                      <span
                        className="key-time-sub is-rotated"
                        data-testid={'key-rotated-at-' + keyItem.id}
                        title={
                          rotatedFullText
                            ? `${k('timeRotatedFullPrefix')}: ${rotatedFullText}${
                                createdFullText ? ` · ${k('created')}: ${createdFullText}` : ''
                              }`
                            : undefined
                        }
                      >
                        <span className="key-time-prefix">{k('timeRotatedShort')}</span>
                        <span className="key-time-val">{rotatedCompactText}</span>
                      </span>
                    ) : (
                      <span
                        className="key-time-sub"
                        title={createdFullText ? `${k('created')}: ${createdFullText}` : undefined}
                      >
                        <span className="key-time-prefix">{k('timeCreatedShort')}</span>
                        <span className="key-time-val">{createdCompactText}</span>
                      </span>
                    )}
                  </div>
                </td>
                <td className="key-col-actions">
                  <div className="key-row-actions" data-row-interactive="true">
                    <div className="key-quick-actions">
                      <Button
                        type="button"
                        variant="secondary"
                        size="xs"
                        iconOnly
                        className="key-quick-btn key-quick-btn-rename"
                        data-testid={'row-rename-' + keyItem.id}
                        aria-label={`${k('rename')}: ${displayName}`}
                        title={k('rename')}
                        disabled={locked}
                        onClick={(event) => {
                          event?.stopPropagation();
                          onRename(keyItem);
                        }}
                      >
                        <FullWebIcon name="edit" size={14} />
                      </Button>
                      {!isSuperseded && menuItems.length > 0 && (
                        <DropdownMenu
                          items={menuItems}
                          align="end"
                          ariaLabel={`${k('moreActions')}: ${displayName}`}
                          triggerTitle={k('moreActions')}
                          triggerClassName="key-quick-btn key-quick-btn-more"
                          triggerIcon={<FullWebIcon name="moreVertical" size={15} />}
                          disabled={locked}
                        />
                      )}
                    </div>
                    <span className="key-actions-divider" aria-hidden="true" />
                    <Button
                      type="button"
                      variant="secondary"
                      size="xs"
                      className="key-row-detail-btn"
                      data-testid={'open-key-' + keyItem.id}
                      disabled={locked}
                      onClick={(event) => {
                        event?.stopPropagation();
                        onOpenDetails(keyItem.id);
                      }}
                    >
                      {k('rowDetails')}
                    </Button>
                  </div>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
