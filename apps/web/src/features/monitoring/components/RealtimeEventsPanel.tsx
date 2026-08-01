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
import type { TFunction } from 'i18next';
import { Button } from '@/components/ui/Button';
import { IconCopy, IconEye, IconEyeOff, IconFilter } from '@/components/ui/icons';
import {
  PaginationControls,
  RecentPattern,
} from '@/features/monitoring/components/MonitoringShared';
import { MonitoringPanel } from '@/features/monitoring/components/MonitoringPanel';
import { formatPercent } from '@/features/monitoring/components/accountOverviewPresentation';
import { buildRealtimeSourceDisplay } from '@/features/monitoring/realtimeSourceDisplay';
import type { MonitoringEventRow } from '@/features/monitoring/hooks/useMonitoringData';
import type { AccountDisplayMode } from '@/features/monitoring/accountOverviewState';
import { useNotificationStore } from '@/stores';
import { copyToClipboard } from '@/utils/clipboard';
import { maskSensitiveText, truncateText } from '@/utils/format';
import { formatCompactNumber, formatUsd } from '@/utils/usage';
import styles from '../MonitoringCenterPage.module.scss';

type RealtimeLogRow = MonitoringEventRow & {
  requestCount: number;
  successRate: number;
  streamKey: string;
  recentPattern: boolean[];
};

type PaginationState<T> = {
  currentPage: number;
  totalPages: number;
  pageItems: T[];
  startItem: number;
  endItem: number;
};

type RealtimeEventsPanelProps = {
  embedded?: boolean;
  rows: RealtimeLogRow[];
  pagination: PaginationState<RealtimeLogRow>;
  pageSize: number;
  scopedFailureCount: number;
  failedOnlyActive: boolean;
  eventsHasMore: boolean;
  eventsLoadingMore: boolean;
  eventsRetentionLimited: boolean;
  eventsTotalCount: number;
  eventsLoadedCount: number;
  overallLoading: boolean;
  hasPrices: boolean;
  accountDisplayMode: AccountDisplayMode;
  locale: string;
  emptyState: ReactNode;
  t: TFunction;
  onToggleFailedOnly: () => void;
  onAccountDisplayModeChange: (mode: AccountDisplayMode) => void;
  onPageChange: (page: number) => void;
  onPageSizeChange: (pageSize: number) => void;
  onLoadMoreEvents: () => void;
};

export type RealtimeEventsPanelActionsProps = {
  rowCount: number;
  scopedFailureCount: number;
  failedOnlyActive: boolean;
  accountDisplayMode: AccountDisplayMode;
  t: TFunction;
  onToggleFailedOnly: () => void;
  onAccountDisplayModeChange: (mode: AccountDisplayMode) => void;
};

const REALTIME_PAGE_SIZE_OPTIONS = [10, 50, 100, 150, 300] as const;
const FAILURE_TOOLTIP_VIEWPORT_MARGIN = 12;
const FAILURE_TOOLTIP_OFFSET = 8;
const FAILURE_TOOLTIP_MAX_WIDTH = 420;
const FAILURE_TOOLTIP_MAX_HEIGHT = 240;
const FAILURE_TOOLTIP_CLOSE_DELAY_MS = 120;
const USAGE_TOOLTIP_WIDTH = 184;
const USAGE_TOOLTIP_ESTIMATED_HEIGHT = 170;

type FailureTooltipPlacement = 'above' | 'below';

type FailureTooltipPosition = {
  placement: FailureTooltipPlacement;
  style: CSSProperties;
};

type UsageTooltipPlacement = 'left-above' | 'left-below';

type UsageTooltipPosition = {
  placement: UsageTooltipPlacement;
  style: CSSProperties;
};

const formatOptionalText = (value: string | null | undefined) => {
  const trimmed = String(value || '').trim();
  return trimmed || '-';
};

const formatReadableText = (value: string | null | undefined) => {
  const trimmed = String(value || '').trim();
  return trimmed && trimmed !== '-' ? trimmed : '';
};

const shortLabel = (
  t: TFunction,
  shortKey: string,
  fallbackKey: string,
  fallbackDefault?: string
) => {
  const fallback = t(fallbackKey, fallbackDefault ? { defaultValue: fallbackDefault } : undefined);
  const label = t(shortKey, { defaultValue: fallback });
  return label === shortKey ? (fallbackDefault ?? fallback) : label;
};

const formatShortHash = (value: string | null | undefined) => {
  const trimmed = formatReadableText(value);
  return trimmed ? `#${trimmed.slice(0, 8)}` : '';
};

const clampNumber = (value: number, min: number, max: number) =>
  Math.min(Math.max(value, min), max);

const resolveFailureTooltipPosition = (anchor: HTMLElement): FailureTooltipPosition | null => {
  if (typeof window === 'undefined') return null;

  const rect = anchor.getBoundingClientRect();
  const viewportWidth = window.innerWidth;
  const viewportHeight = window.innerHeight;
  const maxWidth = Math.max(
    220,
    Math.min(
      FAILURE_TOOLTIP_MAX_WIDTH,
      Math.max(0, viewportWidth - FAILURE_TOOLTIP_VIEWPORT_MARGIN * 2)
    )
  );
  const left = clampNumber(
    rect.left,
    FAILURE_TOOLTIP_VIEWPORT_MARGIN,
    Math.max(
      FAILURE_TOOLTIP_VIEWPORT_MARGIN,
      viewportWidth - maxWidth - FAILURE_TOOLTIP_VIEWPORT_MARGIN
    )
  );
  const spaceBelow =
    viewportHeight - rect.bottom - FAILURE_TOOLTIP_VIEWPORT_MARGIN - FAILURE_TOOLTIP_OFFSET;
  const spaceAbove = rect.top - FAILURE_TOOLTIP_VIEWPORT_MARGIN - FAILURE_TOOLTIP_OFFSET;
  const placement: FailureTooltipPlacement =
    spaceBelow >= FAILURE_TOOLTIP_MAX_HEIGHT || spaceBelow >= spaceAbove ? 'below' : 'above';
  const availableHeight = Math.max(0, placement === 'below' ? spaceBelow : spaceAbove);
  const maxHeight = Math.min(FAILURE_TOOLTIP_MAX_HEIGHT, availableHeight);
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
          top: rect.bottom + FAILURE_TOOLTIP_OFFSET,
        },
      }
    : {
        placement,
        style: {
          ...baseStyle,
          bottom: viewportHeight - rect.top + FAILURE_TOOLTIP_OFFSET,
        },
      };
};

const resolveUsageTooltipPosition = (anchor: HTMLElement): UsageTooltipPosition | null => {
  if (typeof window === 'undefined') return null;

  const rect = anchor.getBoundingClientRect();
  const viewportWidth = window.innerWidth;
  const viewportHeight = window.innerHeight;
  const maxWidth = Math.max(0, viewportWidth - FAILURE_TOOLTIP_VIEWPORT_MARGIN * 2);
  const availableLeftWidth = Math.max(
    0,
    rect.left - FAILURE_TOOLTIP_VIEWPORT_MARGIN - FAILURE_TOOLTIP_OFFSET
  );
  const width = Math.min(USAGE_TOOLTIP_WIDTH, maxWidth, availableLeftWidth);
  const spaceBelow =
    viewportHeight - rect.bottom - FAILURE_TOOLTIP_VIEWPORT_MARGIN - FAILURE_TOOLTIP_OFFSET;
  const spaceAbove = rect.top - FAILURE_TOOLTIP_VIEWPORT_MARGIN - FAILURE_TOOLTIP_OFFSET;
  const placement: UsageTooltipPlacement =
    spaceAbove >= USAGE_TOOLTIP_ESTIMATED_HEIGHT || spaceAbove >= spaceBelow
      ? 'left-above'
      : 'left-below';
  const availableHeight = Math.max(0, placement === 'left-above' ? spaceAbove : spaceBelow);
  const left = Math.max(
    FAILURE_TOOLTIP_VIEWPORT_MARGIN,
    rect.left - FAILURE_TOOLTIP_OFFSET - width
  );
  const baseStyle: CSSProperties = {
    left,
    width,
    maxHeight: Math.min(USAGE_TOOLTIP_ESTIMATED_HEIGHT, availableHeight),
  };

  return placement === 'left-below'
    ? {
        placement,
        style: {
          ...baseStyle,
          top: rect.bottom + FAILURE_TOOLTIP_OFFSET,
        },
      }
    : {
        placement,
        style: {
          ...baseStyle,
          bottom: viewportHeight - rect.top + FAILURE_TOOLTIP_OFFSET,
        },
      };
};

const buildRealtimeApiKeyDisplay = (row: MonitoringEventRow, t: TFunction) => {
  const label = formatReadableText(row.apiKeyLabel);
  const masked = formatReadableText(row.apiKeyMasked);
  const hash = formatReadableText(row.apiKeyHash);
  const shortHash = formatShortHash(hash);
  const display = label || masked || shortHash;

  if (!display) {
    return null;
  }

  const titleParts = [
    `${t('monitoring.realtime_api_key_label')}: ${display}`,
    masked && masked !== display ? `${t('monitoring.realtime_api_key_masked')}: ${masked}` : '',
    hash ? `${t('monitoring.realtime_api_key_hash')}: ${hash}` : '',
    formatReadableText(row.executorType)
      ? `${shortLabel(t, 'monitoring.executor_type_short', 'monitoring.executor_type')}: ${formatReadableText(row.executorType)}`
      : '',
  ].filter(Boolean);

  return {
    display,
    title: titleParts.join('\n'),
  };
};

const formatTokensPerSecond = (value: number | null | undefined, locale: string) => {
  if (typeof value !== 'number' || !Number.isFinite(value) || value <= 0) return '--';

  const absValue = Math.abs(value);
  const maximumFractionDigits = absValue < 1 ? 2 : absValue < 10 ? 1 : 0;
  try {
    return new Intl.NumberFormat(locale, {
      maximumFractionDigits,
      minimumFractionDigits: 0,
    }).format(value);
  } catch {
    return value.toFixed(maximumFractionDigits);
  }
};

const formatRealtimeCompactDuration = (value: number | null | undefined, locale: string) => {
  if (value === null || value === undefined) return '--';

  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed < 0) return '--';

  const formatNumber = (numberValue: number, maximumFractionDigits: number) => {
    try {
      return new Intl.NumberFormat(locale, {
        maximumFractionDigits,
        minimumFractionDigits: 0,
      }).format(numberValue);
    } catch {
      return numberValue.toFixed(maximumFractionDigits);
    }
  };

  if (parsed < 1000) return `${formatNumber(Math.round(parsed), 0)} ms`;

  const seconds = parsed / 1000;
  return `${formatNumber(seconds, seconds < 10 ? 2 : 1)} s`;
};

const getRealtimeDurationToneClass = (value: number | null | undefined) => {
  if (value === null || value === undefined) return undefined;

  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed < 0) return undefined;

  if (parsed >= 30000) return styles.badText;
  if (parsed >= 15000) return styles.warnText;
  return styles.goodText;
};

const formatRealtimeDateParts = (timestampMs: number, locale: string) => {
  const date = new Date(timestampMs);
  return {
    date: date.toLocaleDateString(locale, {
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
    }),
    time: date.toLocaleTimeString(locale, {
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
      hour12: false,
    }),
  };
};

const formatHeaderRecoverAt = (value: number | null | undefined, locale: string) => {
  if (!value || !Number.isFinite(value)) return '';
  return new Date(value).toLocaleString(locale, {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  });
};

const buildHeaderDiagnosticParts = (
  row: MonitoringEventRow,
  t: TFunction,
  locale: string
): string[] => {
  const parts: string[] = [];
  const formatCount = (value: number): string =>
    new Intl.NumberFormat(locale, { maximumFractionDigits: 0 }).format(value);
  const compactSignal = (label: string, value: string | number | null | undefined, limit = 42) => {
    const normalized =
      typeof value === 'number'
        ? Number.isFinite(value)
          ? String(value)
          : ''
        : formatReadableText(value);
    return normalized ? `${label} ${truncateText(normalized, limit)}` : '';
  };
  const errorCode = row.headerErrorCode || row.responseMetadata?.errors?.code || '';
  const errorKind = row.headerErrorKind || row.responseMetadata?.errors?.kind || '';
  if (errorCode || errorKind) {
    parts.push(
      `${t('monitoring.header_error')}: ${[errorKind, errorCode].filter(Boolean).join(' / ')}`
    );
  }
  const shouldRetry = row.responseMetadata?.errors?.should_retry;
  if (typeof shouldRetry === 'boolean') {
    parts.push(
      `${t('monitoring.header_should_retry', { defaultValue: 'Should retry' })}: ${shouldRetry ? t('common.yes', { defaultValue: 'Yes' }) : t('common.no', { defaultValue: 'No' })}`
    );
  }
  const traceId = row.headerTraceId || row.responseMetadata?.trace?.primary_trace_id || '';
  if (traceId) {
    parts.push(`${t('monitoring.header_trace')}: ${truncateText(traceId, 42)}`);
  }
  const traceparent = row.responseMetadata?.trace?.traceparent || '';
  if (traceparent && traceparent !== traceId) {
    parts.push(`traceparent: ${truncateText(traceparent, 64)}`);
  }
  const providerUsage = row.responseMetadata?.provider_usage;
  if (
    providerUsage?.provider === 'xai' &&
    providerUsage.kind === 'included_free_usage' &&
    providerUsage.state === 'exhausted'
  ) {
    const usageParts: string[] = [];
    if (providerUsage.model) usageParts.push(providerUsage.model);
    if (typeof providerUsage.actual === 'number' && typeof providerUsage.limit === 'number') {
      usageParts.push(
        `${formatCount(providerUsage.actual)} / ${formatCount(providerUsage.limit)} ${providerUsage.unit || 'tokens'}`
      );
    }
    if (typeof providerUsage.remaining === 'number') {
      usageParts.push(
        `${t('monitoring.provider_usage_remaining', { defaultValue: 'Remaining' })} ${formatCount(providerUsage.remaining)}`
      );
    }
    if (typeof providerUsage.overage === 'number' && providerUsage.overage > 0) {
      usageParts.push(
        `${t('monitoring.provider_usage_overage', { defaultValue: 'Overage' })} ${formatCount(providerUsage.overage)}`
      );
    }
    if (providerUsage.window_kind === 'rolling_24h') {
      usageParts.push(
        t('monitoring.provider_usage_rolling_24h', { defaultValue: 'Rolling 24-hour window' })
      );
    }
    if (providerUsage.recover_at_ms) {
      usageParts.push(
        `${providerUsage.recover_at_estimated ? t('monitoring.provider_usage_estimated_recovery', { defaultValue: 'Estimated recovery' }) : t('monitoring.header_recover_at')} ${formatHeaderRecoverAt(providerUsage.recover_at_ms, locale)}`
      );
    }
    parts.push(
      `${t('monitoring.provider_usage_xai_exhausted', { defaultValue: 'xAI included free usage exhausted' })}${usageParts.length > 0 ? `: ${usageParts.join(' · ')}` : ''}`
    );
  }
  const quotaParts: string[] = [];
  const planType =
    row.headerQuotaPlanType ||
    row.responseMetadata?.quota?.plan_type ||
    row.responseMetadata?.quota?.active_limit ||
    '';
  if (planType) quotaParts.push(planType);
  const usedPercent =
    row.headerQuotaUsedPercent ?? row.responseMetadata?.quota?.used_percent ?? null;
  if (typeof usedPercent === 'number' && Number.isFinite(usedPercent)) {
    quotaParts.push(formatPercent(usedPercent / 100));
  }
  const recoverAt = formatHeaderRecoverAt(
    row.headerQuotaRecoverAtMs ?? row.responseMetadata?.quota?.recover_at_ms,
    locale
  );
  if (recoverAt) {
    quotaParts.push(`${t('monitoring.header_recover_at')} ${recoverAt}`);
  }
  if (quotaParts.length > 0) {
    parts.push(`${t('monitoring.header_quota')}: ${quotaParts.join(' · ')}`);
  }
  const rateLimit = row.responseMetadata?.rate_limit;
  const rateLimitParts = [
    rateLimit?.requests?.limit !== undefined || rateLimit?.requests?.remaining !== undefined
      ? `${t('monitoring.provider_rate_limit_requests', { defaultValue: 'Requests' })} ${rateLimit.requests?.remaining ?? '-'} / ${rateLimit.requests?.limit ?? '-'}`
      : '',
    rateLimit?.tokens?.limit !== undefined || rateLimit?.tokens?.remaining !== undefined
      ? `${t('monitoring.provider_rate_limit_tokens', { defaultValue: 'Tokens' })} ${rateLimit.tokens?.remaining ?? '-'} / ${rateLimit.tokens?.limit ?? '-'}`
      : '',
  ].filter(Boolean);
  if (rateLimitParts.length > 0) {
    parts.push(
      `${t('monitoring.provider_rate_limit', { defaultValue: 'API rate limit' })}: ${rateLimitParts.join(' · ')}`
    );
  }
  const dataPolicy = row.responseMetadata?.data_policy;
  const dataPolicyParts = [
    dataPolicy?.retention_mode || '',
    typeof dataPolicy?.zero_retention === 'boolean'
      ? `${t('monitoring.provider_zero_retention', { defaultValue: 'Zero retention' })}: ${dataPolicy.zero_retention ? t('common.yes', { defaultValue: 'Yes' }) : t('common.no', { defaultValue: 'No' })}`
      : '',
  ].filter(Boolean);
  if (dataPolicyParts.length > 0) {
    parts.push(
      `${t('monitoring.provider_data_policy', { defaultValue: 'Data policy' })}: ${dataPolicyParts.join(' · ')}`
    );
  }
  const routing = row.responseMetadata?.routing;
  const routingParts = [
    compactSignal('server', routing?.server),
    compactSignal('via', routing?.via),
    compactSignal('cf', routing?.cf_cache_status),
    compactSignal('site', routing?.site_cache_status),
    compactSignal('mife', routing?.mife_upstream_status),
  ].filter(Boolean);
  if (routingParts.length > 0) {
    parts.push(
      `${t('monitoring.header_routing', { defaultValue: 'Routing' })}: ${routingParts.join(' · ')}`
    );
  }
  const providers = row.responseMetadata?.providers;
  const providerParts = [
    compactSignal('antigravity', providers?.antigravity_trace_id),
    compactSignal('oneapi', providers?.oneapi_request_id),
    compactSignal('cf-ray', providers?.cloudflare_ray),
    compactSignal('cf-cache', providers?.cloudflare_cache_status),
  ].filter(Boolean);
  if (providerParts.length > 0) {
    parts.push(
      `${t('monitoring.header_provider', { defaultValue: 'Provider' })}: ${providerParts.join(' · ')}`
    );
  }
  const response = row.responseMetadata?.response;
  const contentType = response?.content_type || '';
  const responseParts = [
    row.failed && contentType && !contentType.includes('event-stream')
      ? truncateText(contentType, 48)
      : '',
    compactSignal('len', response?.content_length, 16),
    compactSignal('timing', response?.server_timing, 64),
  ].filter(Boolean);
  if (responseParts.length > 0) {
    parts.push(`${t('monitoring.header_response')}: ${responseParts.join(' · ')}`);
  }
  return parts;
};

const buildRequestDiagnosticMetaText = (row: MonitoringEventRow, t: TFunction, locale: string) => {
  const parts: string[] = [];
  if (row.failed && row.failStatusCode) {
    parts.push(
      `${shortLabel(t, 'monitoring.fail_status_code_short', 'monitoring.fail_status_code')} ${row.failStatusCode}`
    );
  } else {
    parts.push(t(row.failed ? 'monitoring.result_failed' : 'monitoring.result_success'));
  }
  const body = row.failed ? maskSensitiveText(row.failSummary || '') : '';
  if (body) {
    parts.push(truncateText(body, 96));
  }
  parts.push(...buildHeaderDiagnosticParts(row, t, locale).map((part) => truncateText(part, 96)));
  return parts.join(' · ');
};

const buildRequestDiagnosticDetails = (row: MonitoringEventRow, t: TFunction, locale: string) => {
  if (!row.failed) return null;

  const summary = maskSensitiveText(row.failSummary || '');
  const diagnostics = buildHeaderDiagnosticParts(row, t, locale);
  if (!row.failStatusCode && !summary && diagnostics.length === 0) return null;
  const statusText = row.failStatusCode
    ? `${shortLabel(t, 'monitoring.fail_status_code_short', 'monitoring.fail_status_code')} ${row.failStatusCode}`
    : t('monitoring.result_failed');
  return {
    failed: row.failed,
    statusCode: row.failStatusCode,
    statusText,
    summary,
    diagnostics,
    label: buildRequestDiagnosticMetaText(row, t, locale),
    copyText: [statusText, summary, ...diagnostics].filter(Boolean).join('\n'),
  };
};

type RealtimeRequestDiagnosticDetails = NonNullable<
  ReturnType<typeof buildRequestDiagnosticDetails>
>;

type RealtimeRequestDiagnosticStatusProps = {
  details: RealtimeRequestDiagnosticDetails;
  tooltipId: string;
  t: TFunction;
  onCopy: (text: string) => void;
};

const isNodeInside = (element: HTMLElement | null, target: EventTarget | null) => {
  if (!element || typeof Node === 'undefined' || !(target instanceof Node)) return false;
  return element.contains(target);
};

function RealtimeRequestDiagnosticStatus({
  details,
  tooltipId,
  t,
  onCopy,
}: RealtimeRequestDiagnosticStatusProps) {
  const triggerRef = useRef<HTMLSpanElement | null>(null);
  const tooltipRef = useRef<HTMLSpanElement | null>(null);
  const closeTimerRef = useRef<number | null>(null);
  const rafRef = useRef<number | null>(null);
  const [open, setOpen] = useState(false);
  const [tooltipPosition, setTooltipPosition] = useState<FailureTooltipPosition | null>(null);
  const isBrowser = typeof document !== 'undefined';

  const clearCloseTimer = useCallback(() => {
    if (closeTimerRef.current === null || typeof window === 'undefined') return;
    window.clearTimeout(closeTimerRef.current);
    closeTimerRef.current = null;
  }, []);

  const updateTooltipPosition = useCallback(() => {
    if (!triggerRef.current) return;
    const nextPosition = resolveFailureTooltipPosition(triggerRef.current);
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
    }, FAILURE_TOOLTIP_CLOSE_DELAY_MS);
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

    return () => {
      window.removeEventListener('resize', scheduleTooltipPositionUpdate);
      window.removeEventListener('scroll', scheduleTooltipPositionUpdate, true);
    };
  }, [open, scheduleTooltipPositionUpdate]);

  const placement = tooltipPosition?.placement ?? 'below';
  const tooltipClassName = [
    styles.realtimeFailureTooltip,
    placement === 'above' ? styles.realtimeFailureTooltipAbove : styles.realtimeFailureTooltipBelow,
    open ? styles.realtimeFailureTooltipOpen : '',
  ]
    .filter(Boolean)
    .join(' ');
  const tooltip = (
    <span
      id={tooltipId}
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
        className={styles.realtimeFailureCopyButton}
        onClick={(event) => {
          event.preventDefault();
          event.stopPropagation();
          onCopy(details.copyText);
        }}
        title={t('common.copy')}
        aria-label={t('common.copy')}
      >
        <IconCopy size={13} />
      </button>
      <span className={styles.realtimeFailureTooltipStatus}>{details.statusText}</span>
      {details.summary ? (
        <span className={styles.realtimeFailureTooltipBody}>{details.summary}</span>
      ) : null}
      {details.diagnostics.map((item) => (
        <span key={item} className={styles.realtimeFailureTooltipBody}>
          {item}
        </span>
      ))}
    </span>
  );

  return (
    <span
      ref={triggerRef}
      className={styles.realtimeFailureStatus}
      tabIndex={0}
      aria-describedby={tooltipId}
      aria-label={details.label}
      onMouseEnter={showTooltip}
      onMouseLeave={requestHideTooltip}
      onFocus={showTooltip}
      onBlur={handleBlur}
      onKeyDown={handleKeyDown}
    >
      <span
        className={`${styles.realtimeRequestStatus} ${
          details.failed ? styles.realtimeRequestStatusBad : styles.realtimeRequestStatusGood
        }`}
      >
        {t(details.failed ? 'monitoring.result_failed' : 'monitoring.result_success')}
      </span>
      {!isBrowser ? tooltip : null}
      {isBrowser && open ? createPortal(tooltip, document.body) : null}
    </span>
  );
}

type RealtimeTokenUsageDetails = {
  total: string;
  input: string;
  output: string;
  fields: Array<{ label: string; value: string }>;
  ariaLabel: string;
};

const buildRealtimeTokenUsageDetails = (row: MonitoringEventRow, t: TFunction) => {
  const fields = [
    { label: t('monitoring.realtime_usage_total_label'), value: formatCompactNumber(row.totalTokens) },
    { label: t('monitoring.realtime_usage_input_label'), value: formatCompactNumber(row.inputTokens) },
    { label: t('monitoring.realtime_usage_output_label'), value: formatCompactNumber(row.outputTokens) },
    {
      label: t('monitoring.realtime_usage_reasoning_label'),
      value: formatCompactNumber(row.reasoningTokens),
    },
    { label: t('monitoring.realtime_usage_cached_label'), value: formatCompactNumber(row.cachedTokens) },
    {
      label: t('monitoring.realtime_usage_cache_read_label'),
      value: formatCompactNumber(row.cacheReadTokens),
    },
    {
      label: t('monitoring.realtime_usage_cache_creation_label'),
      value: formatCompactNumber(row.cacheCreationTokens),
    },
  ];

  return {
    total: fields[0].value,
    input: fields[1].value,
    output: fields[2].value,
    fields,
    ariaLabel: fields.map((field) => `${field.label}: ${field.value}`).join(', '),
  } satisfies RealtimeTokenUsageDetails;
};

type RealtimeTokenUsageProps = {
  details: RealtimeTokenUsageDetails;
  tooltipId: string;
};

function RealtimeTokenUsage({ details, tooltipId }: RealtimeTokenUsageProps) {
  const triggerRef = useRef<HTMLDivElement | null>(null);
  const tooltipRef = useRef<HTMLDivElement | null>(null);
  const closeTimerRef = useRef<number | null>(null);
  const rafRef = useRef<number | null>(null);
  const [open, setOpen] = useState(false);
  const [tooltipPosition, setTooltipPosition] = useState<UsageTooltipPosition | null>(null);
  const isBrowser = typeof document !== 'undefined';

  const clearCloseTimer = useCallback(() => {
    if (closeTimerRef.current === null || typeof window === 'undefined') return;
    window.clearTimeout(closeTimerRef.current);
    closeTimerRef.current = null;
  }, []);

  const updateTooltipPosition = useCallback(() => {
    if (!triggerRef.current) return;
    const nextPosition = resolveUsageTooltipPosition(triggerRef.current);
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
    }, FAILURE_TOOLTIP_CLOSE_DELAY_MS);
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

  const handleKeyDown = useCallback((event: KeyboardEvent<HTMLDivElement>) => {
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

    return () => {
      window.removeEventListener('resize', scheduleTooltipPositionUpdate);
      window.removeEventListener('scroll', scheduleTooltipPositionUpdate, true);
    };
  }, [open, scheduleTooltipPositionUpdate]);

  const placement = tooltipPosition?.placement ?? 'left-below';
  const tooltip = (
    <div
      id={tooltipId}
      ref={tooltipRef}
      role="tooltip"
      className={[
        styles.realtimeUsageTooltip,
        placement === 'left-above'
          ? styles.realtimeUsageTooltipLeftAbove
          : styles.realtimeUsageTooltipLeftBelow,
        open ? styles.realtimeUsageTooltipOpen : '',
      ]
        .filter(Boolean)
        .join(' ')}
      style={isBrowser ? tooltipPosition?.style : undefined}
      onMouseEnter={clearCloseTimer}
      onMouseLeave={requestHideTooltip}
    >
      {details.fields.map((field) => (
        <span key={field.label} className={styles.realtimeUsageTooltipLine}>
          <span className={styles.realtimeUsageTooltipLabel}>{field.label}</span>
          <span className={styles.realtimeUsageTooltipValue}>{field.value}</span>
        </span>
      ))}
    </div>
  );

  return (
    <div
      ref={triggerRef}
      className={styles.realtimeUsageCell}
      tabIndex={0}
      aria-describedby={tooltipId}
      aria-label={details.ariaLabel}
      onMouseEnter={showTooltip}
      onMouseLeave={requestHideTooltip}
      onFocus={showTooltip}
      onBlur={handleBlur}
      onKeyDown={handleKeyDown}
    >
      <span className={styles.realtimeUsageTotal}>{details.total}</span>
      <span className={styles.realtimeUsageFlow}>
        <span className={styles.realtimeUsageMetric}>
          <span aria-hidden="true">↑</span>
          {details.input}
        </span>
        <span className={styles.realtimeUsageMetric}>
          <span aria-hidden="true">↓</span>
          {details.output}
        </span>
      </span>
      {!isBrowser ? tooltip : null}
      {isBrowser && open ? createPortal(tooltip, document.body) : null}
    </div>
  );
}

export function RealtimeEventsPanelActions({
  rowCount,
  scopedFailureCount,
  failedOnlyActive,
  accountDisplayMode,
  t,
  onToggleFailedOnly,
  onAccountDisplayModeChange,
}: RealtimeEventsPanelActionsProps) {
  const nextAccountDisplayMode: AccountDisplayMode =
    accountDisplayMode === 'masked' ? 'full' : 'masked';
  const AccountDisplayIcon = accountDisplayMode === 'masked' ? IconEyeOff : IconEye;
  const logRowsLabel = shortLabel(t, 'monitoring.log_rows_short', 'monitoring.log_rows');
  const recentFailuresLabel = shortLabel(
    t,
    'monitoring.recent_failures_short',
    'monitoring.recent_failures'
  );
  const failedOnlyLabel = shortLabel(
    t,
    'monitoring.filter_status_failed_short',
    'monitoring.filter_status_failed'
  );
  const accountDisplayHint = t(
    accountDisplayMode === 'masked'
      ? 'monitoring.account_overview_show_full_accounts_hint'
      : 'monitoring.account_overview_show_masked_accounts_hint'
  );

  return (
    <div className={`${styles.inlineMetrics} ${styles.realtimeHeaderActions}`}>
      <span title={t('monitoring.log_rows')}>{`${logRowsLabel}: ${rowCount}`}</span>
      <span title={t('monitoring.recent_failures')}>
        {`${recentFailuresLabel}: ${scopedFailureCount}`}
      </span>
      <button
        type="button"
        className={[
          styles.accountOverviewToolButton,
          accountDisplayMode === 'full' ? styles.accountDisplayModeButtonActive : '',
        ]
          .filter(Boolean)
          .join(' ')}
        onClick={() => onAccountDisplayModeChange(nextAccountDisplayMode)}
        title={accountDisplayHint}
        aria-label={accountDisplayHint}
      >
        <AccountDisplayIcon size={15} aria-hidden="true" />
        <span>
          {t(
            accountDisplayMode === 'masked'
              ? 'monitoring.account_overview_account_display_masked'
              : 'monitoring.account_overview_account_display_full'
          )}
        </span>
      </button>
      <button
        type="button"
        className={[styles.filterToggleChip, failedOnlyActive ? styles.filterToggleChipActive : '']
          .filter(Boolean)
          .join(' ')}
        onClick={onToggleFailedOnly}
        title={t('monitoring.filter_status_failed')}
      >
        <IconFilter size={14} aria-hidden="true" />
        {failedOnlyLabel}
      </button>
    </div>
  );
}

export function RealtimeEventsPanel({
  embedded = false,
  rows,
  pagination,
  pageSize,
  scopedFailureCount,
  failedOnlyActive,
  eventsHasMore,
  eventsLoadingMore,
  eventsRetentionLimited,
  eventsTotalCount,
  eventsLoadedCount,
  overallLoading,
  hasPrices,
  accountDisplayMode,
  locale,
  emptyState,
  t,
  onToggleFailedOnly,
  onAccountDisplayModeChange,
  onPageChange,
  onPageSizeChange,
  onLoadMoreEvents,
}: RealtimeEventsPanelProps) {
  const tooltipIdPrefix = useId();
  const showNotification = useNotificationStore((state) => state.showNotification);
  const sourceApiKeyLabel = shortLabel(
    t,
    'monitoring.column_source_api_key_short',
    'monitoring.column_source_api_key'
  );
  const reasoningServiceLabel = t('monitoring.reasoning_service_short');
  const recentStatusLabel = shortLabel(
    t,
    'monitoring.recent_status_short',
    'monitoring.recent_status'
  );
  const requestStatusLabel = shortLabel(
    t,
    'monitoring.request_status_short',
    'monitoring.request_status'
  );
  const successRateLabel = shortLabel(
    t,
    'monitoring.column_success_rate_short',
    'monitoring.column_success_rate'
  );
  const totalCallsLabel = shortLabel(
    t,
    'monitoring.total_calls_short',
    'monitoring.total_calls',
    'Calls'
  );
  const usageLabel = shortLabel(
    t,
    'monitoring.this_call_usage_short',
    'monitoring.this_call_usage'
  );
  const costLabel = shortLabel(t, 'monitoring.this_call_cost_short', 'monitoring.this_call_cost');
  const handleCopyFailureDetails = async (text: string) => {
    const copied = await copyToClipboard(text);
    showNotification(
      t(copied ? 'notification.link_copied' : 'notification.copy_failed'),
      copied ? 'success' : 'error'
    );
  };
  const actions = (
    <RealtimeEventsPanelActions
      rowCount={rows.length}
      scopedFailureCount={scopedFailureCount}
      failedOnlyActive={failedOnlyActive}
      accountDisplayMode={accountDisplayMode}
      t={t}
      onToggleFailedOnly={onToggleFailedOnly}
      onAccountDisplayModeChange={onAccountDisplayModeChange}
    />
  );
  const content = (
    <>
      <div className={styles.tableWrapper}>
        <table className={`${styles.table} ${styles.realtimeTable}`}>
          <colgroup>
            <col />
            <col />
            <col />
            <col />
            <col />
            <col />
            <col />
            <col />
            <col />
            <col />
            <col />
            <col />
          </colgroup>
          <thead>
            <tr>
              <th>{sourceApiKeyLabel}</th>
              <th>{t('monitoring.column_model')}</th>
              <th className={styles.realtimeSettingsColumn}>{reasoningServiceLabel}</th>
              <th className={styles.realtimeCenteredColumn}>{recentStatusLabel}</th>
              <th className={styles.realtimeCenteredColumn}>{requestStatusLabel}</th>
              <th className={styles.realtimeCenteredColumn}>{successRateLabel}</th>
              <th className={styles.realtimeCenteredColumn}>{totalCallsLabel}</th>
              <th className={styles.realtimeTpsColumn}>{t('monitoring.column_output_tps')}</th>
              <th className={styles.realtimeLatencyColumn}>{t('monitoring.elapsed_short')}</th>
              <th className={styles.realtimeTimeColumn}>{t('monitoring.column_time')}</th>
              <th>{usageLabel}</th>
              <th>{costLabel}</th>
            </tr>
          </thead>
          <tbody>
            {pagination.pageItems.map((row) => {
              const sourceDisplay = buildRealtimeSourceDisplay(row, t, accountDisplayMode);
              const apiKeyDisplay = buildRealtimeApiKeyDisplay(row, t);
              const showResolvedModel =
                row.resolvedModel &&
                row.resolvedModel.trim() &&
                row.resolvedModel.trim() !== row.model;
              const reasoningEffort = formatOptionalText(row.reasoningEffort);
              const serviceTier = formatOptionalText(row.serviceTier);
              const requestServiceTier = formatOptionalText(row.requestServiceTier);
              const responseServiceTier = formatOptionalText(row.responseServiceTier);
              const effectiveServiceTier =
                requestServiceTier !== '-'
                  ? requestServiceTier
                  : serviceTier !== '-'
                    ? serviceTier
                    : responseServiceTier;
              const requestDiagnosticDetails = buildRequestDiagnosticDetails(row, t, locale);
              const requestDiagnosticTooltipId = requestDiagnosticDetails
                ? `${tooltipIdPrefix}-request-diagnostic-tooltip-${row.id}`
                : undefined;
              const timeParts = formatRealtimeDateParts(row.timestampMs, locale);
              const hasTtftMs = row.ttftMs !== null && row.ttftMs !== undefined;
              const ttftToneClass = getRealtimeDurationToneClass(row.ttftMs);
              const latencyToneClass = getRealtimeDurationToneClass(row.latencyMs);
              const tokenUsage = buildRealtimeTokenUsageDetails(row, t);
              return (
                <tr key={row.id} className={row.failed ? styles.logRowFailed : undefined}>
                  <td>
                    <div className={styles.logTypeCell}>
                      <div className={styles.primaryCell} title={sourceDisplay.title}>
                        <span>{sourceDisplay.primary}</span>
                        {sourceDisplay.meta ? <small>{sourceDisplay.meta}</small> : null}
                        {apiKeyDisplay ? (
                          <small className={styles.realtimeApiKeyLine} title={apiKeyDisplay.title}>
                            {`${t('monitoring.realtime_api_key_label')}: ${apiKeyDisplay.display}`}
                          </small>
                        ) : null}
                      </div>
                    </div>
                  </td>
                  <td>
                    <div
                      className={`${styles.primaryCell} ${styles.realtimeModelCell}`}
                      title={[row.model, showResolvedModel ? row.resolvedModel : '']
                        .filter(Boolean)
                        .join('\n')}
                    >
                      <span className={`${styles.monoCell} ${styles.realtimeModelText}`}>
                        {row.model}
                      </span>
                      {showResolvedModel ? (
                        <small className={`${styles.monoCell} ${styles.realtimeModelText}`}>
                          {row.resolvedModel}
                        </small>
                      ) : null}
                    </div>
                  </td>
                  <td className={styles.realtimeSettingsColumn}>
                    <div className={styles.realtimeSettingsCell}>
                      <span className={styles.realtimeSettingLine}>
                        <span className={styles.realtimeSettingLabel}>
                          {t('monitoring.realtime_reasoning_label')}
                        </span>
                        <span
                          className={`${styles.realtimeSettingValue} ${styles.realtimeReasoningValue}`}
                        >
                          {reasoningEffort}
                        </span>
                      </span>
                      <span className={styles.realtimeSettingLine}>
                        <span className={styles.realtimeSettingLabel}>
                          {t('monitoring.realtime_service_label')}
                        </span>
                        <span
                          className={`${styles.realtimeSettingValue} ${styles.realtimeServiceValue}`}
                        >
                          {effectiveServiceTier}
                        </span>
                      </span>
                    </div>
                  </td>
                  <td className={styles.realtimeCenteredColumn}>
                    <div className={styles.recentStatusCell}>
                      <RecentPattern pattern={row.recentPattern} variant="plain" />
                    </div>
                  </td>
                  <td className={styles.realtimeCenteredColumn}>
                    <div className={styles.primaryCell}>
                      {requestDiagnosticDetails ? (
                        <RealtimeRequestDiagnosticStatus
                          details={requestDiagnosticDetails}
                          tooltipId={
                            requestDiagnosticTooltipId ??
                            `${tooltipIdPrefix}-request-diagnostic-tooltip`
                          }
                          t={t}
                          onCopy={handleCopyFailureDetails}
                        />
                      ) : (
                        <span
                          className={[
                            styles.realtimeRequestStatus,
                            row.failed
                              ? styles.realtimeRequestStatusBad
                              : styles.realtimeRequestStatusGood,
                          ]
                            .filter(Boolean)
                            .join(' ')}
                        >
                          {row.failed
                            ? t('monitoring.result_failed')
                            : t('monitoring.result_success')}
                        </span>
                      )}
                    </div>
                  </td>
                  <td
                    className={[
                      styles.realtimeCenteredColumn,
                      row.successRate >= 0.95
                        ? styles.goodText
                        : row.successRate >= 0.85
                          ? styles.warnText
                          : styles.badText,
                    ].join(' ')}
                  >
                    {formatPercent(row.successRate)}
                  </td>
                  <td className={styles.realtimeCenteredColumn}>
                    {formatCompactNumber(row.requestCount)}
                  </td>
                  <td className={styles.realtimeTpsColumn}>
                    <span className={styles.realtimeTpsCell}>
                      {formatTokensPerSecond(row.tokensPerSecond, locale)}
                    </span>
                  </td>
                  <td className={styles.realtimeLatencyColumn}>
                    <div className={styles.realtimeMetricCell}>
                      <span className={styles.realtimeMetricLine}>
                        <span className={styles.realtimeMetricLabel}>
                          {t('monitoring.ttft_short')}
                        </span>
                        <span
                          className={[styles.realtimeMetricText, ttftToneClass]
                            .filter(Boolean)
                            .join(' ')}
                        >
                          {hasTtftMs ? formatRealtimeCompactDuration(row.ttftMs, locale) : '--'}
                        </span>
                      </span>
                      <span className={styles.realtimeMetricLine}>
                        <span className={styles.realtimeMetricLabel}>
                          {t('monitoring.elapsed_short')}
                        </span>
                        <span
                          className={[styles.realtimeMetricText, latencyToneClass]
                            .filter(Boolean)
                            .join(' ')}
                        >
                          {formatRealtimeCompactDuration(row.latencyMs, locale)}
                        </span>
                      </span>
                    </div>
                  </td>
                  <td className={styles.realtimeTimeColumn}>
                    <div className={styles.realtimeTimeCell}>
                      <span className={styles.realtimeTimeLine}>{timeParts.date}</span>
                      <span className={styles.realtimeTimeLine}>{timeParts.time}</span>
                    </div>
                  </td>
                  <td>
                    <RealtimeTokenUsage
                      details={tokenUsage}
                      tooltipId={`${tooltipIdPrefix}-token-usage-tooltip-${row.id}`}
                    />
                  </td>
                  <td>{hasPrices ? formatUsd(row.totalCost) : '--'}</td>
                </tr>
              );
            })}
            {rows.length === 0 ? (
              <tr>
                <td colSpan={12}>{emptyState}</td>
              </tr>
            ) : null}
          </tbody>
        </table>
      </div>
      <PaginationControls
        count={rows.length}
        currentPage={pagination.currentPage}
        totalPages={pagination.totalPages}
        startItem={pagination.startItem}
        endItem={pagination.endItem}
        pageSize={pageSize}
        pageSizeOptions={REALTIME_PAGE_SIZE_OPTIONS}
        onPageChange={onPageChange}
        onPageSizeChange={onPageSizeChange}
        t={t}
      />
      {rows.length > 0 ? (
        <div className={styles.loadMoreEventsBar}>
          <span className={styles.loadMoreEventsSummary}>
            {eventsRetentionLimited
              ? t('monitoring.events_retention_limited', {
                  loaded: eventsLoadedCount,
                  total: eventsTotalCount,
                })
              : eventsHasMore
                ? t('monitoring.events_loaded_summary', {
                    loaded: eventsLoadedCount,
                    total: eventsTotalCount,
                  })
                : t('monitoring.events_all_loaded', { total: eventsTotalCount })}
          </span>
          {eventsHasMore ? (
            <Button
              variant="secondary"
              size="sm"
              onClick={onLoadMoreEvents}
              disabled={eventsLoadingMore || overallLoading}
            >
              {eventsLoadingMore ? t('common.loading') : t('monitoring.load_more_events')}
            </Button>
          ) : null}
        </div>
      ) : null}
    </>
  );

  if (embedded) {
    return content;
  }

  return (
    <MonitoringPanel
      title={t('monitoring.realtime_table_title')}
      subtitle={t('monitoring.realtime_table_desc')}
      className={styles.realtimePanel}
      extra={actions}
    >
      {content}
    </MonitoringPanel>
  );
}
