import { useEffect, useMemo, useRef, useState, type CSSProperties, type FormEvent, type ReactNode } from 'react';
import { useHref, useInRouterContext, useLocation, useNavigate } from 'react-router-dom';
import type { EChartsCoreOption } from 'echarts/core';
import { fullWebLanguage, fullWebText as t } from '../i18n/fullweb';
import { formatKeyModelsSummaryLine, keyWorkspaceText as k } from '../i18n/freshKeyWorkspace';
import { externalWorkspaceText as x } from '../i18n/freshExternal';
import { prefixRouteBase } from '../router/routeBase';
import { readRoutingContext } from './routingContext';
import { useFullWebLanguage } from './useFullWebLanguage';
import { FullWebIcon } from './FullWebIcon';
import { IconBinary, IconBot } from '../components/ui/icons';
import './freshKeys.css';
import { blockerOf } from './externalState';
import { PublicApiError, type PublicApiClient } from './publicApi';
import {
  parseKeyUsage,
  usageCompactNumberText,
  usageCompactTimeText,
  usageCompactUsdText,
  usageCountText,
  usageReasonText,
  usageStateText,
  type KeyUsage,
  type UsageState,
} from './freshKeyUsage';
import { AccessInfoSummary, type AccessInfo } from './FreshAccessInfo';
import {
  effectBlockerTextKey,
  isDefaultUnnamedOriginKey,
  isTerminalKeyOperationOutcome,
  keyDisplayName,
  keyOriginTextKey,
  keyRenameDraftValue,
  keyUnifiedStatus,
  mergeRenamedKey,
  validKey,
  validKeyNameInput,
  validKeyOperationResult,
  validSavedSecret,
  type Key,
  type KeyOperationResult,
} from './freshKeyMetadata';
import { Drawer } from '../components/ui/Drawer';
import { Modal } from '../components/ui/Modal';
import { SegmentedTabs } from '../components/ui/SegmentedTabs';
import { Button } from '../components/ui/Button';
import { MetricSummaryGrid } from '../components/common/MetricSummaryCard';
import { EChartsView } from '../components/charts/EChartsView';
import { useThemeStore } from '../stores';
import { getDataPalette, hexToRgba, type DataPalette } from '../utils/dataPalette';
import {
  DRAWER_WIDTH,
  DrawerActionFooter,
  DrawerBodyShell,
  DrawerCopyableMeta,
  DrawerDangerButton,
  DrawerDefinitionList,
  DrawerDlRow,
  DrawerIdentityHeader,
  DrawerNotice,
  DrawerScopePill,
  DrawerSectionCard,
  DrawerStatusPill,
  DrawerTabBar,
  DrawerTabPanel,
  DrawerViewport,
  type DrawerStatusTone,
} from '../components/common/WorkspaceDrawer';

type UsageWindow = '24h' | '7d' | '30d';

interface DisplayUsageBucket {
  bucketMs: number;
  calls: number;
  failure: number;
  totalTokens: number;
}

function escapeTooltipHtml(raw: string): string {
  return raw
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;');
}

function buildKeySummaryKpiCards(
  totals: KeyUsage['totals'] | null | undefined,
  state: UsageState
) {
  const proven = (state === 'complete' || state === 'partial') && Boolean(totals);
  const calls = totals?.calls;
  const success = totals?.success ?? 0;
  const failure = totals?.failure ?? 0;
  const totalTokens = totals?.totalTokens;
  const estimatedCostUsd = totals?.estimatedCostUsd;
  const successRate = proven && (calls ?? 0) > 0 ? success / (calls ?? 1) : null;
  const lastUsedCompact =
    proven && totals?.lastUsedAtMs
      ? (usageCompactTimeText(totals.lastUsedAtMs, 'recent') ?? '—')
      : null;
  const callsMeta = lastUsedCompact
    ? `${k('usageLastUsed')} ${lastUsedCompact}`
    : proven
      ? k('noUsageInWindow')
      : '—';
  const successMeta = proven
    ? `${k('usageSuccessShort')} ${usageCountText(success, state)} · ${k('usageFailureShort')} ${usageCountText(failure, state)}`
    : '—';
  const avgTokensMeta =
    proven && (calls ?? 0) > 0 && totalTokens !== undefined
      ? `${k('usageAvgPerCall')} ${usageCompactNumberText(Math.round(totalTokens / (calls ?? 1)), 'complete')} / ${k('callsUnit')}`
      : '—';
  const avgCostMeta =
    proven && estimatedCostUsd !== undefined && (calls ?? 0) > 0
      ? `${k('usageAvgPerCall')} ${usageCompactUsdText(estimatedCostUsd / (calls ?? 1), 'complete')} / ${k('callsUnit')}`
      : '—';

  return [
    {
      label: k('usageCalls'),
      value: usageCompactNumberText(calls, state),
      valueTitle: usageCountText(calls, state),
      meta: callsMeta,
      icon: 'calls' as const,
      accent: 'blue' as const,
    },
    {
      label: k('cardMetricSuccessRate'),
      value:
        successRate !== null
          ? successRate >= 0.9995
            ? '100%'
            : `${(successRate * 100).toFixed(1)}%`
          : '—',
      tone:
        successRate !== null
          ? successRate >= 0.95
            ? ('good' as const)
            : successRate >= 0.85
              ? ('warn' as const)
              : ('bad' as const)
          : undefined,
      meta: successMeta,
      icon: 'success' as const,
      accent: 'green' as const,
    },
    {
      label: k('usageTotalTokens'),
      value: usageCompactNumberText(totalTokens, state),
      valueTitle: usageCountText(totalTokens, state),
      meta: avgTokensMeta,
      icon: 'tokens' as const,
      accent: 'indigo' as const,
    },
    {
      label: k('sortFieldCost'),
      value:
        proven && estimatedCostUsd !== undefined
          ? usageCompactUsdText(estimatedCostUsd, state)
          : '—',
      meta: avgCostMeta,
      icon: 'cost' as const,
      accent: 'amber' as const,
    },
  ];
}

function formatTrendAxisBucket(bucketMs: number, spanMs: number): string {
  const d = new Date(bucketMs);
  if (Number.isNaN(d.getTime())) return '';
  const pad = (n: number) => String(n).padStart(2, '0');
  const HOUR_MS = 3_600_000;
  const DAY_MS = 86_400_000;
  if (spanMs <= 30 * HOUR_MS) {
    return `${pad(d.getHours())}:${pad(d.getMinutes())}`;
  }
  if (spanMs <= 8 * DAY_MS) {
    return `${pad(d.getMonth() + 1)}/${pad(d.getDate())} ${pad(d.getHours())}:00`;
  }
  return `${pad(d.getMonth() + 1)}/${pad(d.getDate())}`;
}

function buildKeyUsageTrendOption({
  series,
  spanMs,
  state,
  dataPalette,
  language,
}: {
  series: DisplayUsageBucket[];
  spanMs: number;
  state: UsageState;
  dataPalette: DataPalette;
  language: string;
}): EChartsCoreOption {
  const labels = series.map((pt) => formatTrendAxisBucket(pt.bucketMs, spanMs));
  const labelStep = Math.max(0, Math.ceil(series.length / 6) - 1);
  const successLabel = k('usageSuccess');
  const failureLabel = k('usageFailure');
  const tokensLabel = k('cardMetricTokens');

  return {
    animationDuration: 220,
    backgroundColor: 'transparent',
    grid: {
      top: 14,
      right: 6,
      bottom: 4,
      left: 4,
      containLabel: true,
    },
    legend: { show: false },
    tooltip: {
      trigger: 'axis',
      appendToBody: true,
      backgroundColor: dataPalette.surface.tooltipBackground,
      borderColor: dataPalette.surface.tooltipBorder,
      borderWidth: 1,
      padding: [8, 10],
      textStyle: {
        color: dataPalette.surface.tooltipText,
        fontSize: 12,
      },
      axisPointer: {
        type: 'shadow',
      },
      formatter: (params: unknown) => {
        const list = Array.isArray(params) ? params : [];
        const first = list[0] as { dataIndex?: number } | undefined;
        const idx = typeof first?.dataIndex === 'number' ? first.dataIndex : -1;
        const pt = idx >= 0 ? series[idx] : undefined;
        if (!pt) return '';
        const fail = Math.min(pt.failure, pt.calls);
        const ok = Math.max(0, pt.calls - fail);
        const bucketDate = new Date(pt.bucketMs);
        const timeText = escapeTooltipHtml(
          Number.isFinite(bucketDate.getTime())
            ? bucketDate.toLocaleString(language, {
                year: 'numeric',
                month: '2-digit',
                day: '2-digit',
                hour: '2-digit',
                minute: '2-digit',
              })
            : ''
        );
        const okText = escapeTooltipHtml(usageCountText(ok, state));
        const failText = escapeTooltipHtml(usageCountText(fail, state));
        const tokText = escapeTooltipHtml(usageCountText(pt.totalTokens, state));
        return [
          `<div style="font-weight:600;margin-bottom:4px;">${timeText}</div>`,
          `<div style="display:flex;align-items:center;justify-content:space-between;gap:14px;"><span><span style="display:inline-block;width:7px;height:7px;border-radius:999px;background:${dataPalette.blue};margin-right:6px;"></span>${escapeTooltipHtml(successLabel)}</span><strong>${okText}</strong></div>`,
          `<div style="display:flex;align-items:center;justify-content:space-between;gap:14px;"><span><span style="display:inline-block;width:7px;height:7px;border-radius:999px;background:${dataPalette.red};margin-right:6px;"></span>${escapeTooltipHtml(failureLabel)}</span><strong>${failText}</strong></div>`,
          `<div style="display:flex;align-items:center;justify-content:space-between;gap:14px;"><span><span style="display:inline-block;width:7px;height:7px;border-radius:999px;background:${dataPalette.emerald};margin-right:6px;"></span>${escapeTooltipHtml(tokensLabel)}</span><strong>${tokText}</strong></div>`,
        ].join('');
      },
    },
    xAxis: {
      type: 'category',
      data: labels,
      axisTick: { show: false },
      axisLine: { lineStyle: { color: dataPalette.surface.axisLine } },
      axisLabel: {
        color: dataPalette.surface.axisLabel,
        fontSize: 10,
        interval: labelStep,
        hideOverlap: true,
      },
    },
    yAxis: [
      {
        type: 'value',
        min: 0,
        minInterval: 1,
        splitNumber: 3,
        axisLabel: {
          color: dataPalette.surface.axisLabel,
          fontSize: 10,
          formatter: (v: number) => usageCompactNumberText(v, 'complete'),
        },
        splitLine: {
          lineStyle: { color: dataPalette.surface.splitLine, type: 'dashed' },
        },
      },
      {
        type: 'value',
        position: 'right',
        min: 0,
        splitNumber: 3,
        axisLabel: {
          color: dataPalette.emerald,
          fontSize: 10,
          formatter: (v: number) => usageCompactNumberText(v, 'complete'),
        },
        splitLine: { show: false },
      },
    ],
    series: [
      {
        name: successLabel,
        type: 'bar',
        stack: 'calls',
        yAxisIndex: 0,
        barMaxWidth: 14,
        data: series.map((pt) => {
          const fail = Math.min(pt.failure, pt.calls);
          const ok = Math.max(0, pt.calls - fail);
          return {
            value: ok,
            itemStyle: {
              color: dataPalette.blue,
              borderRadius: fail > 0 ? [0, 0, 0, 0] : [3, 3, 0, 0],
            },
          };
        }),
      },
      {
        name: failureLabel,
        type: 'bar',
        stack: 'calls',
        yAxisIndex: 0,
        barMaxWidth: 14,
        data: series.map((pt) => {
          const fail = Math.min(pt.failure, pt.calls);
          return {
            value: fail,
            itemStyle: {
              color: dataPalette.red,
              borderRadius: [3, 3, 0, 0],
            },
          };
        }),
      },
      {
        name: tokensLabel,
        type: 'line',
        yAxisIndex: 1,
        smooth: 0.24,
        showSymbol: series.length <= 14,
        symbol: 'circle',
        symbolSize: 4,
        lineStyle: {
          color: dataPalette.emerald,
          width: 2,
        },
        itemStyle: {
          color: dataPalette.emerald,
        },
        areaStyle: {
          color: {
            type: 'linear',
            x: 0,
            y: 0,
            x2: 0,
            y2: 1,
            colorStops: [
              { offset: 0, color: hexToRgba(dataPalette.emerald, 0.16) },
              { offset: 1, color: hexToRgba(dataPalette.emerald, 0) },
            ],
          },
        },
        data: series.map((pt) => pt.totalTokens),
      },
    ],
  };
}

function statusPillTone(pillClass: string): DrawerStatusTone {
  switch (pillClass) {
    case 'is-effective':
      return 'success';
    case 'is-muted':
      return 'neutral';
    case 'is-pending':
    default:
      return 'warning';
  }
}

function usageCoverageTone(state: UsageState): DrawerStatusTone {
  switch (state) {
    case 'complete':
      return 'success';
    case 'partial':
      return 'warning';
    case 'unavailable':
      return 'danger';
    case 'unknown':
    default:
      return 'neutral';
  }
}

const USAGE_STARTUP_RETRY_DELAYS_MS = [800, 1500, 3000, 5000] as const;

function isTransientStartupUsageReason(reason: string | undefined): boolean {
  return reason === 'collection_starting' || reason === 'projection_catching_up';
}

function detailTime(value: number) {
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return { iso: undefined, label: '—' };
  return {
    iso: date.toISOString(),
    label: date.toLocaleString(fullWebLanguage(), {
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
    }),
  };
}

function extractErrorKey(err: PublicApiError, expectedId: string): Key | null {
  const raw = err.key;
  if (!raw || typeof raw !== 'object') return null;
  const candidate = raw as Key;
  return validKey(candidate) && candidate.id === expectedId ? candidate : null;
}

function randomRequestId(): string {
  return Array.from(crypto.getRandomValues(new Uint8Array(16)), (b) =>
    b.toString(16).padStart(2, '0')
  ).join('');
}

function validScopedOperationSnapshot(
  raw: unknown,
  expectedOperationId: string,
  expectedTargetId: string
): KeyOperationResult | null {
  if (!raw || typeof raw !== 'object') return null;
  const candidate = raw as KeyOperationResult;
  if (!validKeyOperationResult(candidate)) return null;
  if (
    candidate.operation &&
    (candidate.operation.id !== expectedOperationId ||
      (candidate.operation.targetId !== expectedTargetId &&
        candidate.operation.keeperId !== expectedTargetId))
  ) {
    return null;
  }
  return candidate;
}

function isCommittedMissingAddResponse(
  err: PublicApiError,
  updatedKey: Key,
  expectedId: string
): boolean {
  if (err.code !== 'key_operation_unknown' || updatedKey.runtimeState !== 'not_applied') {
    return false;
  }
  if (!err.operationResult || typeof err.operationResult !== 'object') return false;
  const res = err.operationResult as KeyOperationResult;
  if (!validKeyOperationResult(res) || !res.operation) return false;
  return (
    res.operation.mode === 'add' &&
    res.operation.outcome === 'committed' &&
    res.operation.targetId === expectedId &&
    res.operation.keeperId === ''
  );
}

function isExternalOrSourceCode(code: string): boolean {
  return [
    'external_management_key_rejected',
    'external_target_moved',
    'external_cpa_version_unsupported',
    'external_cpa_authority_unavailable',
    'key_operation_source_conflict',
    'key_operation_conflict',
    'key_operation_generation_mismatch',
    'key_operation_host_proof_required',
    'config_authority_unavailable',
  ].includes(code);
}

function RouterNavLinkInner({
  to,
  prefixedHref,
  basePath,
  className,
  testId,
  onNavigate,
  children,
}: {
  to: string;
  prefixedHref: string;
  basePath: string;
  className: string;
  testId: string;
  onNavigate: () => void;
  children: ReactNode;
}) {
  const navigate = useNavigate();
  const { pathname } = useLocation();
  const routerHref = useHref(to);
  const pathnameIncludesBase =
    Boolean(basePath) && (pathname === basePath || pathname.startsWith(`${basePath}/`));
  const target = pathnameIncludesBase ? prefixedHref : to;
  const resolvedHref = pathnameIncludesBase ? prefixedHref : routerHref;
  return (
    <a
      href={resolvedHref}
      className={className}
      data-testid={testId}
      onClick={(event) => {
        if (
          event.defaultPrevented ||
          event.button !== 0 ||
          event.metaKey ||
          event.ctrlKey ||
          event.shiftKey ||
          event.altKey
        ) {
          return;
        }
        event.preventDefault();
        onNavigate();
        navigate(target);
      }}
    >
      {children}
    </a>
  );
}

function CrossPageLink({
  route,
  apiKeyId,
  className,
  testId,
  disabled = false,
  onNavigate,
  children,
}: {
  route: '/monitoring' | '/usage-analytics';
  apiKeyId: string;
  className: string;
  testId: string;
  disabled?: boolean;
  onNavigate: () => void;
  children: ReactNode;
}) {
  const inRouter = useInRouterContext();
  const basePath =
    typeof document !== 'undefined' ? (readRoutingContext(document)?.basePath ?? '') : '';
  const query = `apiKeyId=${encodeURIComponent(apiKeyId)}`;
  const to = `${route}?${query}`;
  const prefixedHref = `${prefixRouteBase(route, basePath)}?${query}`;
  if (disabled) {
    return (
      <button type="button" className={className} data-testid={testId} disabled>
        {children}
      </button>
    );
  }
  if (inRouter) {
    return (
      <RouterNavLinkInner
        to={to}
        prefixedHref={prefixedHref}
        basePath={basePath}
        className={className}
        testId={testId}
        onNavigate={onNavigate}
      >
        {children}
      </RouterNavLinkInner>
    );
  }
  return (
    <a href={prefixedHref} className={className} data-testid={testId}>
      {children}
    </a>
  );
}

export interface FreshKeyRenameModalProps {
  open: boolean;
  visible?: boolean;
  record: Key | null;
  client: PublicApiClient;
  credential: string;
  onUnauthorized: () => void;
  onSaved: (updated: Key) => void;
  onPending?: (pending: boolean) => void;
  onClose: () => void;
}

export function FreshKeyRenameModal({
  open,
  visible = true,
  record,
  client,
  credential,
  onUnauthorized,
  onSaved,
  onPending,
  onClose,
}: FreshKeyRenameModalProps) {
  const [activeRecord, setActiveRecord] = useState<Key | null>(record);
  const [draft, setDraft] = useState('');
  const [busy, setBusy] = useState(false);
  const [errorKind, setErrorKind] = useState<'' | 'conflict' | 'rejected' | 'unknown'>('');
  const [errorMessage, setErrorMessage] = useState('');
  const inputRef = useRef<HTMLInputElement>(null);
  const alive = useRef(true);
  const inFlight = useRef(false);
  const sessionEpochRef = useRef(0);
  const prevOpen = useRef(false);
  const prevId = useRef('');
  const prevCredential = useRef(credential);
  const auth = { kind: 'admin' as const, value: credential };

  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
      sessionEpochRef.current += 1;
    };
  }, []);

  useEffect(() => {
    const nextId = record?.id ?? '';
    const credentialChanged = credential !== prevCredential.current;
    const justOpened = open && !prevOpen.current;
    const switchedRecord = open && nextId !== prevId.current;
    prevCredential.current = credential;
    prevOpen.current = open;
    prevId.current = nextId;

    if (!open) {
      sessionEpochRef.current += 1;
      inFlight.current = false;
      setBusy(false);
      setErrorKind('');
      setErrorMessage('');
      setActiveRecord(null);
      return;
    }

    if (justOpened || switchedRecord || credentialChanged) {
      sessionEpochRef.current += 1;
      inFlight.current = false;
      setActiveRecord(record);
      setDraft(record ? keyRenameDraftValue(record) : '');
      setBusy(false);
      setErrorKind('');
      setErrorMessage('');
    } else if (record && !errorKind) {
      setActiveRecord(record);
    }
  }, [open, record, errorKind, credential]);

  const activeRecordId = activeRecord?.id;
  useEffect(() => {
    if (open && activeRecordId) {
      inputRef.current?.focus();
    }
  }, [open, activeRecordId]);

  const currentRecord = activeRecord ?? record;
  const trimmed = draft.trim();
  const isDefaultOrigin = currentRecord ? isDefaultUnnamedOriginKey(currentRecord) : false;
  const nameValid = validKeyNameInput(trimmed);
  const canSubmit =
    Boolean(currentRecord) &&
    nameValid &&
    (isDefaultOrigin ? trimmed !== '' : trimmed !== currentRecord!.name);
  const currentDisplayName = currentRecord
    ? keyDisplayName(
        currentRecord,
        k('installerOrigin'),
        k('adoptedOrigin'),
        t('keysUnnamed')
      )
    : '';
  const defaultPlaceholder = currentRecord
    ? isDefaultOrigin
      ? currentDisplayName
      : k('renamePlaceholder')
    : '';
  async function submitWithRecord(baseRecord: Key, submittedName: string) {
    const updated = await client.put<Key>(
      `api-keys/${baseRecord.id}`,
      { name: submittedName, nameRevision: baseRecord.nameRevision },
      auth
    );
    if (!validKey(updated) || updated.id !== baseRecord.id) {
      throw new Error('invalid key response');
    }
    if (updated.name !== submittedName || updated.nameRevision !== baseRecord.nameRevision + 1) {
      throw new Error('invalid key revision');
    }
    return updated;
  }

  async function handleSave(event: FormEvent) {
    event.preventDefault();
    if (inFlight.current || !currentRecord || !canSubmit) return;
    const epoch = sessionEpochRef.current;
    inFlight.current = true;
    setBusy(true);
    setErrorKind('');
    setErrorMessage('');
    onPending?.(true);
    const submitted = trimmed;
    const baseRecord = currentRecord;
    try {
      const updated = await submitWithRecord(baseRecord, submitted);
      if (!alive.current || sessionEpochRef.current !== epoch) return;
      const merged = mergeRenamedKey(baseRecord, updated);
      setActiveRecord(merged);
      onSaved(merged);
      onPending?.(false);
      onClose();
    } catch (err) {
      if (!alive.current || sessionEpochRef.current !== epoch) return;
      if (err instanceof PublicApiError && err.kind === 'authentication') {
        onPending?.(false);
        onUnauthorized();
        return;
      }
      if (err instanceof PublicApiError && err.status === 409) {
        setErrorKind('conflict');
        setErrorMessage(t('keysNameConflict'));
        onPending?.(false);
      } else if (err instanceof PublicApiError && [400, 404].includes(err.status || 0)) {
        setErrorKind('rejected');
        setErrorMessage(t('keysNameRejected'));
        onPending?.(false);
      } else {
        setErrorKind('unknown');
        setErrorMessage(t('keysNameUnknown'));
      }
    } finally {
      if (sessionEpochRef.current === epoch) {
        inFlight.current = false;
        if (alive.current) setBusy(false);
      }
    }
  }

  async function handleRefreshAndRetry() {
    if (inFlight.current || !currentRecord) return;
    const epoch = sessionEpochRef.current;
    const baseRecord = currentRecord;
    inFlight.current = true;
    setBusy(true);
    setErrorMessage('');
    const submitted = trimmed;
    try {
      const latest = await client.get<Key>(`api-keys/${baseRecord.id}`, auth);
      if (!alive.current || sessionEpochRef.current !== epoch) return;
      if (!validKey(latest) || latest.id !== baseRecord.id) {
        throw new Error('invalid key response');
      }
      setActiveRecord(latest);
      onSaved(latest);

      if (
        errorKind === 'unknown' &&
        latest.name === submitted &&
        latest.nameRevision > baseRecord.nameRevision
      ) {
        setErrorKind('');
        onPending?.(false);
        onClose();
        return;
      }

      const defaultLatest = isDefaultUnnamedOriginKey(latest);
      const canRetryDraft =
        validKeyNameInput(submitted) &&
        (defaultLatest ? submitted !== '' : submitted !== latest.name);
      if (errorKind !== 'rejected' && canRetryDraft) {
        onPending?.(true);
        const updated = await submitWithRecord(latest, submitted);
        if (!alive.current || sessionEpochRef.current !== epoch) return;
        const merged = mergeRenamedKey(latest, updated);
        setActiveRecord(merged);
        onSaved(merged);
        setErrorKind('');
        onPending?.(false);
        onClose();
        return;
      }

      setErrorKind('');
      onPending?.(false);
    } catch (err) {
      if (!alive.current || sessionEpochRef.current !== epoch) return;
      if (err instanceof PublicApiError && err.kind === 'authentication') {
        onPending?.(false);
        onUnauthorized();
        return;
      }
      if (err instanceof PublicApiError && err.status === 409) {
        setErrorKind('conflict');
        setErrorMessage(t('keysNameConflict'));
        onPending?.(false);
      } else if (err instanceof PublicApiError && [400, 404].includes(err.status || 0)) {
        setErrorKind('rejected');
        setErrorMessage(t('keysNameRejected'));
        onPending?.(false);
      } else {
        setErrorKind('unknown');
        setErrorMessage(t('keysUnavailable'));
      }
    } finally {
      if (sessionEpochRef.current === epoch) {
        inFlight.current = false;
        if (alive.current) setBusy(false);
      }
    }
  }

  function handleClose() {
    if (busy) return;
    if (errorKind === 'unknown') {
      onPending?.(false);
    }
    onClose();
  }

  return (
    <Modal
      open={open && visible && Boolean(currentRecord)}
      title={k('renameModalTitle')}
      closeDisabled={busy}
      className="key-rename-modal"
      width={460}
      onClose={handleClose}
      footer={
        <>
          <button type="button" className="btn btn-secondary" disabled={busy} onClick={handleClose}>
            {k('cancel')}
          </button>
          <button
            type="submit"
            form="rename-key-form"
            className="btn btn-primary"
            disabled={busy || !canSubmit}
          >
            {busy ? k('renameSavingAction') : k('renameSaveAction')}
          </button>
        </>
      }
    >
      {currentRecord && (
        <form id="rename-key-form" className="key-modal-form" onSubmit={handleSave}>
          <div className="key-operation-resource">
            <span className="key-resource-icon" aria-hidden="true">
              <FullWebIcon name="key" size={16} />
            </span>
            <div className="key-operation-resource-body">
              <span className="key-operation-resource-label">{k('operationTargetLabel')}</span>
              <strong>{currentDisplayName}</strong>
              <code>{currentRecord.maskedPreview}</code>
            </div>
          </div>
          <div className="key-field-group">
            <label className="key-field-label" htmlFor="key-name">
              {t('keysName')}
            </label>
            <input
              ref={inputRef}
              id="key-name"
              className="input"
              maxLength={120}
              disabled={busy}
              placeholder={defaultPlaceholder}
              value={draft}
              onChange={(event) => setDraft(event.target.value)}
            />
            <p className="key-field-help">
              {isDefaultOrigin ? k('renameDefaultOriginHelp') : k('renameClearHelp')}
            </p>
          </div>
          {errorMessage && (
            <div className="key-notice key-notice-warning" role="alert" data-testid="rename-error-alert">
              <FullWebIcon name="alert" />
              <div>
                <p>{errorMessage}</p>
                <div className="key-modal-actions">
                  <button
                    type="button"
                    className="btn btn-secondary btn-sm"
                    data-testid="rename-refresh-retry"
                    disabled={busy}
                    onClick={() => void handleRefreshAndRetry()}
                  >
                    {k('renameRefreshRetry')}
                  </button>
                </div>
              </div>
            </div>
          )}
        </form>
      )}
    </Modal>
  );
}

export function FreshKeyDetails({
  id,
  client,
  credential,
  onUnauthorized,
  onPending,
  onSaved,
  onClose,
  onRequestOperation,
  onRebind,
  onBlocker,
  open = true,
  visible = true,
  gatewayPending = true,
  operationPending = false,
  accessInfo,
  reloadToken = 0,
  initialTab = 'overview',
  initialAction,
  initialWindow = '24h',
}: {
  id: string;
  client: PublicApiClient;
  credential: string;
  onUnauthorized: () => void;
  onPending: (value: boolean) => void;
  onSaved: (key: Key) => void;
  onClose: () => void;
  onRequestOperation?: (mode: 'replace' | 'revoke' | 'regenerate') => void;
  onRebind?: () => void;
  onBlocker?: (code: string, detail?: string) => void;
  open?: boolean;
  visible?: boolean;
  gatewayPending?: boolean;
  operationPending?: boolean;
  accessInfo?: AccessInfo | null;
  reloadToken?: number;
  initialTab?: 'overview' | 'usage';
  initialAction?: 'rename';
  initialWindow?: UsageWindow;
}) {
  const [record, setRecord] = useState<Key | null>(null);
  const [usage, setUsage] = useState<KeyUsage | null>(null);
  const [usage24h, setUsage24h] = useState<KeyUsage | null>(null);
  const [usageFailed, setUsageFailed] = useState(false);
  const [usageWindowState, setUsageWindowState] = useState<{
    forId: string;
    window: UsageWindow;
  }>({ forId: id, window: initialWindow });
  const [busy, setBusy] = useState(false);
  const [applyPending, setApplyPending] = useState(false);
  const [applyUnknown, setApplyUnknown] = useState(false);
  const [applyError, setApplyError] = useState('');
  const [applyErrorCode, setApplyErrorCode] = useState('');
  const [applyErrorDetail, setApplyErrorDetail] = useState('');
  const [applyAborted, setApplyAborted] = useState(false);
  const applyPendingRef = useRef(false);
  const pendingReapplyOpId = useRef('');
  const [message, setMessage] = useState('');
  const [editing, setEditing] = useState(false);
  const [detailTab, setDetailTab] = useState<'overview' | 'usage'>(initialTab);
  const initialActionApplied = useRef(false);
  // The full value is read on demand for the clipboard only. It is never kept in
  // component state, browser storage or the URL.
  const [copying, setCopying] = useState(false);
  const [copyFeedback, setCopyFeedback] = useState<'copied' | 'failed' | ''>('');
  const [idCopyFeedback, setIdCopyFeedback] = useState<'copied' | 'failed' | ''>('');
  const idCopyTimerRef = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const idCopySeq = useRef(0);
  const copyingRef = useRef(false);
  const inFlight = useRef(false);
  const active = useRef(true);
  const ownerEpoch = useRef(0);
  const readSeq = useRef(0);
  const usageSeq = useRef(0);
  const usage24hSeq = useRef(0);
  const usageRef = useRef<KeyUsage | null>(null);
  const usage24hRef = useRef<KeyUsage | null>(null);
  const latestServerToMsRef = useRef(0);
  const visibleRef = useRef(visible);
  visibleRef.current = visible;
  const openRef = useRef(open);
  openRef.current = open;
  const recordRef = useRef<Key | null>(record);
  recordRef.current = record;
  const usageFailedRef = useRef(usageFailed);
  usageFailedRef.current = usageFailed;
  const hiddenAuthSuppressedRef = useRef(false);
  const wasOpenVisibleRef = useRef(open && visible);
  const usageStartupTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const usageStartupAttemptRef = useRef(0);
  const wasActiveForUsageRetryRef = useRef(open && visible);
  const [usageStartupTick, setUsageStartupTick] = useState(0);
  const [usageReadsInFlight, setUsageReadsInFlight] = useState(0);
  const prevInitialWindowRef = useRef(initialWindow);
  const detailDrawerBodyRef = useRef<HTMLDivElement | null>(null);
  const auth = { kind: 'admin' as const, value: credential };
  const onPendingRef = useRef(onPending);
  onPendingRef.current = onPending;

  function clearUsageStartupRetry(resetAttempt = false) {
    if (usageStartupTimerRef.current !== null) {
      clearTimeout(usageStartupTimerRef.current);
      usageStartupTimerRef.current = null;
    }
    if (resetAttempt) {
      usageStartupAttemptRef.current = 0;
    }
  }
  const resource = 'api-keys/' + id;
  const activeUsageWindow: UsageWindow =
    usageWindowState.forId === id ? usageWindowState.window : initialWindow;
  const usageCoverage = usage?.coverage ?? {
    state: 'unknown' as UsageState,
    reason: 'usage_read_failed',
  };
  const activeUsageWindowRef = useRef<UsageWindow>(activeUsageWindow);
  activeUsageWindowRef.current = activeUsageWindow;


  useEffect(() => {
    return () => {
      onPendingRef.current(false);
      clearTimeout(idCopyTimerRef.current);
      idCopyTimerRef.current = undefined;
      clearUsageStartupRetry(true);
    };
  }, []);
  useEffect(() => {
    prevInitialWindowRef.current = initialWindow;
    active.current = true;
    const ownerAtMount = ++ownerEpoch.current;
    inFlight.current = false;
    applyPendingRef.current = false;
    copyingRef.current = false;
    setBusy(false);
    setCopying(false);
    setRecord(null);
    recordRef.current = null;
    setUsage(null);
    setUsage24h(null);
    usageRef.current = null;
    usage24hRef.current = null;
    latestServerToMsRef.current = 0;
    readSeq.current += 1;
    usageSeq.current += 1;
    usage24hSeq.current += 1;
    hiddenAuthSuppressedRef.current = false;
    wasOpenVisibleRef.current = open && visible;
    setUsageFailed(false);
    usageFailedRef.current = false;
    setUsageReadsInFlight(0);
    clearUsageStartupRetry(true);
    setUsageWindowState({ forId: id, window: initialWindow });
    setApplyPending(false);
    setApplyUnknown(false);
    setApplyError('');
    setApplyErrorCode('');
    setApplyErrorDetail('');
    setApplyAborted(false);
    pendingReapplyOpId.current = '';
    setMessage('');
    setEditing(false);
    setCopyFeedback('');
    setIdCopyFeedback('');
    idCopySeq.current += 1;
    clearTimeout(idCopyTimerRef.current);
    idCopyTimerRef.current = undefined;
    setDetailTab(initialTab);
    initialActionApplied.current = false;
    const previous = typeof document !== 'undefined' ? document.activeElement : null;
    if (open && visible) {
      void read(ownerAtMount);
      void readUsage(ownerAtMount, initialWindow);
      if (initialWindow !== '24h') {
        void readUsage24h(ownerAtMount);
      }
    }
    return () => {
      active.current = false;
      clearUsageStartupRetry(true);
      if (
        typeof HTMLElement !== 'undefined' &&
        previous instanceof HTMLElement &&
        previous.isConnected
      ) {
        previous.focus();
      }
    };
  }, [id, credential, reloadToken, open]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    const openVisible = open && visible;
    const becameOpenVisible = openVisible && !wasOpenVisibleRef.current;
    wasOpenVisibleRef.current = openVisible;
    if (!openVisible) {
      readSeq.current += 1;
      inFlight.current = false;
      setBusy(false);
      usageSeq.current += 1;
      usage24hSeq.current += 1;
      return;
    }
    if (becameOpenVisible) {
      const retryHiddenAuth = hiddenAuthSuppressedRef.current;
      hiddenAuthSuppressedRef.current = false;
      const ownerAtStart = ownerEpoch.current;
      if (retryHiddenAuth || !recordRef.current) {
        void read(ownerAtStart);
      }
      if (retryHiddenAuth || !usageRef.current || usageFailedRef.current) {
        void readUsage(ownerAtStart, activeUsageWindowRef.current);
      }
      if (
        activeUsageWindowRef.current !== '24h' &&
        (retryHiddenAuth || !usage24hRef.current)
      ) {
        void readUsage24h(ownerAtStart);
      }
    }
  }, [open, visible]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    const prev = prevInitialWindowRef.current;
    prevInitialWindowRef.current = initialWindow;
    if (!open || prev === initialWindow) return;
    if (!visible) {
      clearUsageStartupRetry(true);
      activeUsageWindowRef.current = initialWindow;
      setUsageWindowState({ forId: id, window: initialWindow });
      usageRef.current = null;
      setUsage(null);
      setUsageFailed(false);
      return;
    }
    switchUsageWindow(initialWindow);
  }, [initialWindow, open]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    setDetailTab(initialTab);
  }, [initialTab]);

  useEffect(() => {
    if (initialAction === 'rename' && record && !initialActionApplied.current && !applyPending) {
      initialActionApplied.current = true;
      setEditing(true);
    }
  }, [initialAction, record, applyPending]);

  useEffect(() => {
    const isActive = open && visible;
    const becameActive = isActive && !wasActiveForUsageRetryRef.current;
    wasActiveForUsageRetryRef.current = isActive;
    clearUsageStartupRetry(false);
    if (becameActive) {
      usageStartupAttemptRef.current = 0;
    }
    const hasTransientUsage =
      isTransientStartupUsageReason(usage?.coverage.reason) ||
      (activeUsageWindow !== '24h' && isTransientStartupUsageReason(usage24h?.coverage.reason));
    if (!isActive || !hasTransientUsage || usageReadsInFlight > 0) {
      if (!hasTransientUsage) {
        usageStartupAttemptRef.current = 0;
      }
      return;
    }
    const attempt = usageStartupAttemptRef.current;
    if (attempt >= USAGE_STARTUP_RETRY_DELAYS_MS.length) {
      return;
    }
    const delayMs = USAGE_STARTUP_RETRY_DELAYS_MS[attempt];
    const ownerAtSchedule = ownerEpoch.current;
    const windowAtSchedule = activeUsageWindowRef.current;
    usageStartupTimerRef.current = setTimeout(() => {
      usageStartupTimerRef.current = null;
      if (
        !active.current ||
        !openRef.current ||
        !visibleRef.current ||
        ownerEpoch.current !== ownerAtSchedule ||
        activeUsageWindowRef.current !== windowAtSchedule
      ) {
        return;
      }
      usageStartupAttemptRef.current = attempt + 1;
      const reloadActive = isTransientStartupUsageReason(usageRef.current?.coverage.reason);
      const reload24h =
        windowAtSchedule !== '24h' &&
        isTransientStartupUsageReason(usage24hRef.current?.coverage.reason);
      void Promise.all([
        reloadActive
          ? readUsage(ownerAtSchedule, windowAtSchedule, { preserveOnError: true })
          : Promise.resolve('ok' as const),
        reload24h
          ? readUsage24h(ownerAtSchedule, { preserveOnError: true })
          : Promise.resolve('ok' as const),
      ]).then(([activeOutcome, dailyOutcome]) => {
        if (
          (activeOutcome === 'error' || dailyOutcome === 'error') &&
          active.current &&
          openRef.current &&
          visibleRef.current &&
          ownerEpoch.current === ownerAtSchedule &&
          activeUsageWindowRef.current === windowAtSchedule
        ) {
          setUsageStartupTick((tick) => tick + 1);
        }
      });
    }, delayMs);
    return () => {
      clearUsageStartupRetry(false);
    };
  }, [usage, usage24h, usageReadsInFlight, usageStartupTick, open, visible, id, credential, reloadToken, activeUsageWindow]); // eslint-disable-line react-hooks/exhaustive-deps

  function validate(value: Key) {
    if (!validKey(value) || value.id !== id) throw new Error();
    return value;
  }

  function failure(err: unknown) {
    if (err instanceof PublicApiError && err.kind === 'authentication') {
      if (visibleRef.current && openRef.current) {
        onUnauthorized();
      } else if (openRef.current) {
        hiddenAuthSuppressedRef.current = true;
      }
    } else setMessage(t('keysUnavailable'));
  }

  async function read(ownerAtStart = ownerEpoch.current) {
    if (inFlight.current || applyPendingRef.current) return;
    const seq = ++readSeq.current;
    inFlight.current = true;
    setBusy(true);
    setMessage('');
    try {
      const value = validate(await client.get<Key>(resource, auth));
      if (!active.current || ownerEpoch.current !== ownerAtStart || seq !== readSeq.current) return;
      setRecord(value);
      onSaved(value);
      if (value.runtimeState === 'effective_observed' || value.runtimeState === 'revoked_observed') {
        setApplyUnknown(false);
        setApplyError('');
        setApplyErrorCode('');
        setApplyErrorDetail('');
        setApplyAborted(false);
        pendingReapplyOpId.current = '';
      }
      onPending(false);
    } catch (err) {
      if (active.current && ownerEpoch.current === ownerAtStart) {
        if (seq !== readSeq.current) {
          if (
            err instanceof PublicApiError &&
            err.kind === 'authentication' &&
            openRef.current &&
            !recordRef.current &&
            !inFlight.current
          ) {
            hiddenAuthSuppressedRef.current = true;
          }
          return;
        }
        failure(err);
      }
    } finally {
      if (ownerEpoch.current === ownerAtStart && seq === readSeq.current) {
        inFlight.current = false;
        if (active.current) setBusy(false);
      }
    }
  }

  async function resolveVerifiedSecret(ownerAtStart: number): Promise<string | null> {
    if (!record || record.lifecycle === 'superseded') return null;
    const value = await client.post<{ secret: string }>(resource + '/secret', {}, auth);
    if (!active.current || ownerEpoch.current !== ownerAtStart) return null;
    if (!value || typeof value.secret !== 'string') throw new Error();
    if (!validSavedSecret(record, value.secret)) {
      const latest = validate(await client.get<Key>(resource, auth));
      if (!active.current || ownerEpoch.current !== ownerAtStart) return null;
      setRecord(latest);
      onSaved(latest);
      if (latest.lifecycle === 'superseded' || !validSavedSecret(latest, value.secret)) {
        throw new Error();
      }
    }
    return value.secret;
  }

  async function copySavedSecret() {
    if (copyingRef.current || !record || record.lifecycle === 'superseded') return;
    const ownerAtStart = ownerEpoch.current;
    copyingRef.current = true;
    setCopying(true);
    setCopyFeedback('');
    try {
      const secret = await resolveVerifiedSecret(ownerAtStart);
      if (!secret) return;
      await navigator.clipboard.writeText(secret);
      if (active.current && ownerEpoch.current === ownerAtStart) setCopyFeedback('copied');
    } catch (err) {
      if (!active.current || ownerEpoch.current !== ownerAtStart) return;
      if (err instanceof PublicApiError && err.kind === 'authentication') {
        if (visibleRef.current && openRef.current) onUnauthorized();
        else if (openRef.current) hiddenAuthSuppressedRef.current = true;
      } else {
        setCopyFeedback('failed');
        if (err instanceof PublicApiError && err.status === 409) {
          await read(ownerAtStart);
        }
      }
    } finally {
      if (ownerEpoch.current === ownerAtStart) {
        copyingRef.current = false;
        if (active.current) setCopying(false);
      }
    }
  }

  async function copySavedAuthHeader(
    headerPrefix: string
  ): Promise<'copied' | 'failed' | 'ignored'> {
    if (copyingRef.current || !record || record.lifecycle === 'superseded') return 'ignored';
    const ownerAtStart = ownerEpoch.current;
    copyingRef.current = true;
    setCopying(true);
    try {
      const secret = await resolveVerifiedSecret(ownerAtStart);
      if (!secret) return 'ignored';
      if (
        typeof navigator === 'undefined' ||
        !navigator.clipboard ||
        typeof navigator.clipboard.writeText !== 'function'
      ) {
        throw new Error('clipboard_unavailable');
      }
      await navigator.clipboard.writeText(`${headerPrefix}${secret}`);
      if (!active.current || ownerEpoch.current !== ownerAtStart) return 'ignored';
      return 'copied';
    } catch (err) {
      if (!active.current || ownerEpoch.current !== ownerAtStart) return 'ignored';
      if (err instanceof PublicApiError && err.kind === 'authentication') {
        if (visibleRef.current && openRef.current) onUnauthorized();
        else if (openRef.current) hiddenAuthSuppressedRef.current = true;
        return 'ignored';
      }
      if (err instanceof PublicApiError && err.status === 409) {
        await read(ownerAtStart);
        if (!active.current || ownerEpoch.current !== ownerAtStart) return 'ignored';
      }
      return 'failed';
    } finally {
      if (ownerEpoch.current === ownerAtStart) {
        copyingRef.current = false;
        if (active.current) setCopying(false);
      }
    }
  }

  async function copyKeyId() {
    if (!id) return;
    const ownerAtStart = ownerEpoch.current;
    const seq = ++idCopySeq.current;
    try {
      if (
        typeof navigator === 'undefined' ||
        !navigator.clipboard ||
        typeof navigator.clipboard.writeText !== 'function'
      ) {
        throw new Error('clipboard_unavailable');
      }
      await navigator.clipboard.writeText(id);
      if (!active.current || ownerEpoch.current !== ownerAtStart || idCopySeq.current !== seq) {
        return;
      }
      setIdCopyFeedback('copied');
    } catch {
      if (!active.current || ownerEpoch.current !== ownerAtStart || idCopySeq.current !== seq) {
        return;
      }
      setIdCopyFeedback('failed');
    }
    clearTimeout(idCopyTimerRef.current);
    idCopyTimerRef.current = setTimeout(() => {
      if (active.current && ownerEpoch.current === ownerAtStart && idCopySeq.current === seq) {
        setIdCopyFeedback('');
      }
      idCopyTimerRef.current = undefined;
    }, 1800);
  }

  async function readUsage(
    ownerAtStart = ownerEpoch.current,
    window: UsageWindow = activeUsageWindow,
    options?: { preserveOnError?: boolean }
  ): Promise<'ok' | 'error' | 'stale'> {
    const seq = ++usageSeq.current;
    const seq24h = window === '24h' ? ++usage24hSeq.current : 0;
    let usagePath = `${resource}/usage`;
    if (window === '7d' || window === '30d') {
      const toMs = Math.max(Date.now(), latestServerToMsRef.current);
      const spanMs = window === '7d' ? 7 * 86_400_000 : 30 * 86_400_000;
      const fromMs = Math.max(1, toMs - spanMs);
      usagePath = `${resource}/usage?from_ms=${fromMs}&to_ms=${toMs}`;
    }
    setUsageReadsInFlight((n) => n + 1);
    try {
      const parsed = parseKeyUsage(await client.get<unknown>(usagePath, auth), id);
      if (!parsed) throw new Error('Invalid usage response');
      if (!active.current || ownerEpoch.current !== ownerAtStart) {
        return 'stale';
      }
      if (seq !== usageSeq.current && !(window === '24h' && seq24h === usage24hSeq.current)) {
        return 'stale';
      }
      latestServerToMsRef.current = Math.max(latestServerToMsRef.current, parsed.toMs);
      if (window === '24h' && seq24h === usage24hSeq.current) {
        usage24hRef.current = parsed;
        setUsage24h(parsed);
      }
      if (seq !== usageSeq.current || window !== activeUsageWindowRef.current) {
        return 'stale';
      }
      usageRef.current = parsed;
      setUsage(parsed);
      setUsageFailed(false);
      return 'ok';
    } catch (err) {
      if (!active.current || ownerEpoch.current !== ownerAtStart) {
        return 'stale';
      }
      if (err instanceof PublicApiError && err.kind === 'authentication') {
        if (seq === usageSeq.current || (window === '24h' && seq24h === usage24hSeq.current)) {
          if (visibleRef.current && openRef.current) {
            onUnauthorized();
          } else if (openRef.current) {
            hiddenAuthSuppressedRef.current = true;
          }
        }
        return 'stale';
      }
      const isCurrentActive = seq === usageSeq.current;
      const isCurrent24h = window === '24h' && seq24h === usage24hSeq.current;
      if (!options?.preserveOnError) {
        if (isCurrent24h) {
          usage24hRef.current = null;
          setUsage24h(null);
        }
        if (isCurrentActive) {
          usageRef.current = null;
          setUsage(null);
          setUsageFailed(true);
        }
      }
      return isCurrentActive || isCurrent24h ? 'error' : 'stale';
    } finally {
      if (ownerEpoch.current === ownerAtStart && active.current) {
        setUsageReadsInFlight((n) => Math.max(0, n - 1));
      }
    }
  }

  async function readUsage24h(
    ownerAtStart = ownerEpoch.current,
    options?: { preserveOnError?: boolean }
  ): Promise<'ok' | 'error' | 'stale'> {
    const seq = ++usage24hSeq.current;
    setUsageReadsInFlight((n) => n + 1);
    try {
      const parsed = parseKeyUsage(await client.get<unknown>(`${resource}/usage`, auth), id);
      if (!parsed) throw new Error('Invalid usage response');
      if (!active.current || ownerEpoch.current !== ownerAtStart || seq !== usage24hSeq.current) {
        return 'stale';
      }
      latestServerToMsRef.current = Math.max(latestServerToMsRef.current, parsed.toMs);
      usage24hRef.current = parsed;
      setUsage24h(parsed);
      return 'ok';
    } catch (err) {
      if (!active.current || ownerEpoch.current !== ownerAtStart || seq !== usage24hSeq.current) {
        return 'stale';
      }
      if (err instanceof PublicApiError && err.kind === 'authentication') {
        if (visibleRef.current && openRef.current) {
          onUnauthorized();
        } else if (openRef.current) {
          hiddenAuthSuppressedRef.current = true;
        }
        return 'stale';
      }
      if (!options?.preserveOnError) {
        usage24hRef.current = null;
        setUsage24h(null);
      }
      return 'error';
    } finally {
      if (ownerEpoch.current === ownerAtStart && active.current) {
        setUsageReadsInFlight((n) => Math.max(0, n - 1));
      }
    }
  }

  function switchUsageWindow(nextWindow: UsageWindow) {
    if (nextWindow === activeUsageWindow) return;
    clearUsageStartupRetry(true);
    setUsageWindowState({ forId: id, window: nextWindow });
    usageRef.current = null;
    setUsage(null);
    setUsageFailed(false);
    void readUsage(ownerEpoch.current, nextWindow);
  }

  async function executeReapplyAdd(baseKey: Key, ownerAtStart: number) {
    const reapplyId = randomRequestId();
    pendingReapplyOpId.current = reapplyId;
    let preparedOk = false;
    try {
      const prep = await client.post<KeyOperationResult>(
        'api-keys/' + id + '/revocation',
        { requestId: reapplyId, keeperId: '', mode: 'add' },
        auth
      );
      if (!active.current || ownerEpoch.current !== ownerAtStart) return;
      if (
        !validKeyOperationResult(prep) ||
        !prep.operation ||
        prep.operation.id !== reapplyId ||
        prep.operation.targetId !== id ||
        prep.operation.keeperId !== '' ||
        prep.operation.mode !== 'add'
      ) {
        throw new Error();
      }
      preparedOk = true;
      let settledOp = prep;
      if (prep.operation.outcome === 'pending' && prep.nextStep === 'add') {
        const cont = await client.post<KeyOperationResult>(
          'api-keys/operations/' + reapplyId + '/continue',
          {},
          auth
        );
        if (!active.current || ownerEpoch.current !== ownerAtStart) return;
        if (
          !validKeyOperationResult(cont) ||
          !cont.operation ||
          cont.operation.id !== reapplyId ||
          cont.operation.targetId !== id ||
          cont.operation.keeperId !== '' ||
          cont.operation.mode !== 'add'
        ) {
          throw new Error();
        }
        settledOp = cont;
      }

      if (settledOp.operation!.outcome === 'aborted') {
        let refreshed = baseKey;
        try {
          const canonical = validate(await client.get<Key>(resource, auth));
          if (!active.current || ownerEpoch.current !== ownerAtStart) return;
          if (canonical.requestId === baseKey.requestId) refreshed = canonical;
        } catch (refreshErr) {
          if (refreshErr instanceof PublicApiError && refreshErr.kind === 'authentication') {
            throw refreshErr;
          }
          if (!active.current || ownerEpoch.current !== ownerAtStart) return;
        }
        pendingReapplyOpId.current = '';
        const retired =
          refreshed.lifecycle === 'superseded' || refreshed.runtimeState === 'revoked_observed';
        const resolvedCode = retired ? '' : refreshed.effectCode || '';
        setRecord(refreshed);
        onSaved(refreshed);
        setApplyUnknown(false);
        setApplyAborted(!retired);
        setApplyErrorCode(resolvedCode);
        setApplyErrorDetail('');
        setApplyError(
          retired
            ? ''
            : resolvedCode
              ? k(effectBlockerTextKey(resolvedCode))
              : k('operationAborted')
        );
        if (resolvedCode && blockerOf(resolvedCode)) {
          onBlocker?.(resolvedCode);
        }
        onPending(false);
        return;
      }

      if (
        settledOp.operation!.outcome === 'committed' &&
        settledOp.runtimeState === 'effective_observed'
      ) {
        const canonical = validate(await client.get<Key>(resource, auth));
        if (!active.current || ownerEpoch.current !== ownerAtStart) return;
        if (canonical.requestId !== baseKey.requestId) throw new Error();
        pendingReapplyOpId.current = '';
        setRecord(canonical);
        onSaved(canonical);
        setApplyUnknown(false);
        if (canonical.runtimeState === 'effective_observed') {
          setApplyError('');
          setApplyErrorCode('');
          setApplyErrorDetail('');
          setApplyAborted(false);
        } else {
          const code = canonical.effectCode || '';
          setApplyErrorCode(code);
          setApplyErrorDetail('');
          setApplyAborted(false);
          setApplyError(code ? k(effectBlockerTextKey(code)) : '');
          if (code && blockerOf(code)) {
            onBlocker?.(code);
          }
        }
        onPending(false);
        return;
      }

      setApplyUnknown(true);
      setApplyAborted(false);
      setApplyError('');
      setApplyErrorCode('');
      setApplyErrorDetail('');
    } catch (err) {
      if (!active.current || ownerEpoch.current !== ownerAtStart) return;
      if (err instanceof PublicApiError && err.kind === 'authentication') {
        onPending(false);
        onUnauthorized();
        return;
      }
      if (err instanceof PublicApiError) {
        const code = err.code || '';
        const detail = err.detail || '';
        const rawSnap =
          (err.status === 409 || err.status === 503) && err.operationResult !== undefined
            ? (err.operationResult as KeyOperationResult)
            : null;
        const validSnap =
          rawSnap && validKeyOperationResult(rawSnap) && rawSnap.operation ? rawSnap : null;
        const ownOp =
          validSnap?.operation &&
          validSnap.operation.id === reapplyId &&
          validSnap.operation.targetId === id &&
          validSnap.operation.mode === 'add'
            ? validSnap
            : null;
        if (ownOp?.operation?.outcome === 'aborted') {
          let refreshed = baseKey;
          let resolvedCode = code === 'key_operation_unknown' ? '' : code;
          const resolvedDetail = resolvedCode === 'external_management_key_rejected' ? detail : '';
          try {
            const canonical = validate(await client.get<Key>(resource, auth));
            if (!active.current || ownerEpoch.current !== ownerAtStart) return;
            if (canonical.requestId === baseKey.requestId) refreshed = canonical;
          } catch (refreshErr) {
            if (refreshErr instanceof PublicApiError && refreshErr.kind === 'authentication') {
              onPending(false);
              onUnauthorized();
              return;
            }
            if (!active.current || ownerEpoch.current !== ownerAtStart) return;
          }
          const retired =
            refreshed.lifecycle === 'superseded' || refreshed.runtimeState === 'revoked_observed';
          if (retired) {
            resolvedCode = '';
          } else if (
            !resolvedCode &&
            refreshed.effectCode &&
            refreshed.effectCode !== 'key_operation_unknown'
          ) {
            resolvedCode = refreshed.effectCode;
          }
          pendingReapplyOpId.current = '';
          setRecord(refreshed);
          onSaved(refreshed);
          setApplyUnknown(false);
          setApplyAborted(!retired);
          setApplyErrorCode(resolvedCode);
          setApplyErrorDetail(retired ? '' : resolvedDetail);
          setApplyError(
            retired
              ? ''
              : resolvedCode
                ? k(effectBlockerTextKey(resolvedCode))
                : k('operationAborted')
          );
          if (resolvedCode && blockerOf(resolvedCode)) {
            onBlocker?.(resolvedCode, resolvedDetail || undefined);
          }
          onPending(false);
          return;
        }
        if (
          !preparedOk &&
          code === 'key_operation_conflict' &&
          validSnap?.operation &&
          validSnap.operation.id !== reapplyId &&
          !isTerminalKeyOperationOutcome(validSnap.operation.outcome)
        ) {
          pendingReapplyOpId.current = '';
          setRecord(baseKey);
          onSaved(baseKey);
          setApplyUnknown(false);
          setApplyErrorCode(code);
          setApplyError(k(effectBlockerTextKey(code)));
          onPending(false);
          return;
        }
        if (ownOp?.operation) {
          const blocker = isExternalOrSourceCode(code);
          const nextUnknown = !blocker;
          const nextRecord: Key = {
            ...baseKey,
            runtimeState: 'effect_unknown',
            operationId: reapplyId,
            effectCode: code || baseKey.effectCode,
          };
          setRecord(nextRecord);
          onSaved(nextRecord);
          setApplyUnknown(nextUnknown);
          setApplyErrorCode(code);
          setApplyErrorDetail(code === 'external_management_key_rejected' ? detail : '');
          setApplyError(
            code && code !== 'key_operation_unknown'
              ? k(effectBlockerTextKey(code))
              : k('createApplyUnknownHelp')
          );
          if (code && blockerOf(code)) {
            onBlocker?.(code, detail || undefined);
          }
          onPending(nextUnknown);
          return;
        }
        if (isExternalOrSourceCode(code) || [400, 404, 409].includes(err.status || 0)) {
          if (!preparedOk) pendingReapplyOpId.current = '';
          const nextRecord: Key = preparedOk
            ? {
                ...baseKey,
                runtimeState: 'effect_unknown',
                operationId: reapplyId,
                effectCode: code || baseKey.effectCode,
              }
            : {
                ...baseKey,
                effectCode: code || baseKey.effectCode,
              };
          setRecord(nextRecord);
          onSaved(nextRecord);
          setApplyUnknown(false);
          setApplyErrorCode(code);
          setApplyErrorDetail(code === 'external_management_key_rejected' ? detail : '');
          setApplyError(code ? k(effectBlockerTextKey(code)) : t('keysUnavailable'));
          if (code && blockerOf(code)) {
            onBlocker?.(code, detail || undefined);
          }
          onPending(false);
          return;
        }
      }
      setApplyUnknown(true);
      setApplyError(k('createApplyUnknownHelp'));
      onPending(true);
    }
  }

  async function retryApply() {
    if (
      inFlight.current ||
      applyPendingRef.current ||
      !record ||
      applyUnknown ||
      record.lifecycle === 'superseded' ||
      (record.runtimeState !== 'not_applied' && record.runtimeState !== 'applying')
    ) {
      return;
    }
    const ownerAtStart = ownerEpoch.current;
    applyPendingRef.current = true;
    setApplyPending(true);
    setApplyError('');
    setApplyErrorCode('');
    setApplyErrorDetail('');
    setApplyAborted(false);
    onPending(true);
    try {
      const res = await client.post<{ key: Key; operation?: KeyOperationResult['operation'] }>(
        'api-keys',
        { requestId: record.requestId },
        auth
      );
      if (!active.current || ownerEpoch.current !== ownerAtStart) return;
      const updated = validate(res?.key);
      if (updated.requestId !== record.requestId) throw new Error();
      pendingReapplyOpId.current = '';
      setRecord(updated);
      onSaved(updated);
      setApplyUnknown(false);
      const code = updated.runtimeState === 'effective_observed' ? '' : updated.effectCode || '';
      setApplyErrorCode(code);
      setApplyError(
        updated.runtimeState === 'effective_observed'
          ? ''
          : k(effectBlockerTextKey(updated.effectCode))
      );
      onPending(false);
    } catch (err) {
      if (!active.current || ownerEpoch.current !== ownerAtStart) return;
      if (err instanceof PublicApiError && err.kind === 'authentication') {
        onPending(false);
        onUnauthorized();
        return;
      }
      if (err instanceof PublicApiError) {
        const rawUpdated = extractErrorKey(err, id);
        const updated =
          rawUpdated && rawUpdated.requestId === record.requestId ? rawUpdated : null;
        if (updated && isCommittedMissingAddResponse(err, updated, id)) {
          await executeReapplyAdd(updated, ownerAtStart);
          return;
        }
        if (updated) {
          const errorCode = err.code || updated.effectCode || '';
          const errorDetail = err.detail || '';
          const opSnap =
            err.operationResult && validKeyOperationResult(err.operationResult as KeyOperationResult)
              ? (err.operationResult as KeyOperationResult)
              : null;
          const aborted =
            !!opSnap?.operation &&
            opSnap.operation.outcome === 'aborted' &&
            (opSnap.operation.targetId === id || opSnap.operation.keeperId === id);
          const externalOrSourceBlocker = !!errorCode && isExternalOrSourceCode(errorCode);
          const nextUnknown = updated.runtimeState === 'effect_unknown' && !externalOrSourceBlocker;
          setRecord(updated);
          onSaved(updated);
          setApplyUnknown(nextUnknown);
          setApplyAborted(aborted);
          setApplyErrorCode(errorCode);
          setApplyErrorDetail(errorCode === 'external_management_key_rejected' ? errorDetail : '');
          setApplyError(
            errorCode && errorCode !== 'key_operation_unknown'
              ? k(effectBlockerTextKey(errorCode))
              : aborted
                ? k('operationAborted')
                : k(effectBlockerTextKey(errorCode))
          );
          if (errorCode && blockerOf(errorCode)) {
            onBlocker?.(errorCode, errorDetail || undefined);
          }
          onPending(nextUnknown);
          return;
        }
        if (err.code && blockerOf(err.code)) {
          setApplyUnknown(true);
          setApplyErrorCode(err.code);
          setApplyErrorDetail(
            err.code === 'external_management_key_rejected' ? err.detail || '' : ''
          );
          setApplyError(k(effectBlockerTextKey(err.code)));
          onBlocker?.(err.code, err.detail || undefined);
          onPending(true);
          return;
        }
      }
      setApplyUnknown(true);
      setApplyError(k('createApplyUnknownHelp'));
      onPending(true);
    } finally {
      if (ownerEpoch.current === ownerAtStart) {
        applyPendingRef.current = false;
        if (active.current) setApplyPending(false);
      }
    }
  }

  async function reconcileApply() {
    if (inFlight.current || applyPendingRef.current || !record) return;
    const ownerAtStart = ownerEpoch.current;
    applyPendingRef.current = true;
    setApplyPending(true);
    setApplyError('');
    setApplyErrorCode('');
    setApplyErrorDetail('');
    setApplyAborted(false);
    try {
      const current = validate(
        await client.get<Key>('api-keys/requests/' + record.requestId, auth)
      );
      if (!active.current || ownerEpoch.current !== ownerAtStart) return;
      if (current.requestId !== record.requestId) throw new Error();
      let nextRecord = current;
      let operationErrorCode = '';
      let operationErrorDetail = '';
      let abortedOperation = false;
      const opIdToCheck = current.operationId || pendingReapplyOpId.current;
      if (opIdToCheck && (current.runtimeState === 'effect_unknown' || !!pendingReapplyOpId.current)) {
        let snapshot: KeyOperationResult | null = null;
        try {
          const opRes = await client.get<KeyOperationResult>(
            'api-keys/operations/' + opIdToCheck,
            auth
          );
          if (!active.current || ownerEpoch.current !== ownerAtStart) return;
          snapshot = validScopedOperationSnapshot(opRes, opIdToCheck, current.id);
        } catch (opErr) {
          if (opErr instanceof PublicApiError && opErr.kind === 'authentication') throw opErr;
          if (!active.current || ownerEpoch.current !== ownerAtStart) return;
          if (opErr instanceof PublicApiError) {
            operationErrorCode = opErr.code || '';
            operationErrorDetail = opErr.detail || '';
            if (
              (opErr.status === 409 || opErr.status === 503) &&
              opErr.operationResult !== undefined
            ) {
              snapshot = validScopedOperationSnapshot(
                opErr.operationResult,
                opIdToCheck,
                current.id
              );
            }
          }
        }
        if (
          snapshot &&
          snapshot.operation === null &&
          current.runtimeState === 'not_applied' &&
          !current.operationId
        ) {
          pendingReapplyOpId.current = '';
        }
        if (
          snapshot &&
          (snapshot.runtimeState === 'effective_observed' ||
            snapshot.operation?.outcome === 'aborted' ||
            (snapshot.operation?.outcome === 'committed' && !!pendingReapplyOpId.current))
        ) {
          abortedOperation = snapshot.operation?.outcome === 'aborted';
          if (operationErrorCode === 'key_operation_unknown') {
            operationErrorCode = '';
          }
          try {
            const refreshed = validate(await client.get<Key>(resource, auth));
            if (!active.current || ownerEpoch.current !== ownerAtStart) return;
            if (refreshed.requestId === current.requestId) nextRecord = refreshed;
          } catch (refreshErr) {
            if (refreshErr instanceof PublicApiError && refreshErr.kind === 'authentication') {
              throw refreshErr;
            }
            if (!active.current || ownerEpoch.current !== ownerAtStart) return;
            if (refreshErr instanceof PublicApiError && refreshErr.code) {
              operationErrorCode = refreshErr.code;
              operationErrorDetail = refreshErr.detail || operationErrorDetail;
            }
          }
        }
      }
      const retired =
        nextRecord.lifecycle === 'superseded' || nextRecord.runtimeState === 'revoked_observed';
      if (nextRecord.runtimeState === 'effective_observed' || abortedOperation || retired) {
        pendingReapplyOpId.current = '';
      }
      let resolvedCode =
        nextRecord.runtimeState === 'effective_observed' || retired
          ? ''
          : operationErrorCode || nextRecord.effectCode || '';
      if (
        resolvedCode === 'key_operation_unknown' &&
        abortedOperation &&
        nextRecord.runtimeState === 'not_applied'
      ) {
        resolvedCode = '';
      }
      const resolvedDetail =
        resolvedCode === 'external_management_key_rejected' ? operationErrorDetail : '';
      setRecord(nextRecord);
      onSaved(nextRecord);
      setApplyUnknown(false);
      setApplyAborted(
        abortedOperation && nextRecord.runtimeState !== 'effective_observed' && !retired
      );
      setApplyErrorCode(resolvedCode);
      setApplyErrorDetail(resolvedDetail);
      setApplyError(
        nextRecord.runtimeState === 'effective_observed' || retired
          ? ''
          : resolvedCode
            ? k(effectBlockerTextKey(resolvedCode))
            : abortedOperation && nextRecord.runtimeState === 'not_applied'
              ? k('operationAborted')
              : ''
      );
      if (resolvedCode && blockerOf(resolvedCode)) {
        onBlocker?.(resolvedCode, resolvedDetail || undefined);
      }
      onPending(false);
    } catch (err) {
      if (!active.current || ownerEpoch.current !== ownerAtStart) return;
      if (err instanceof PublicApiError && err.kind === 'authentication') {
        onPending(false);
        onUnauthorized();
        return;
      }
      setApplyError(t('keysUnavailable'));
    } finally {
      if (ownerEpoch.current === ownerAtStart) {
        applyPendingRef.current = false;
        if (active.current) setApplyPending(false);
      }
    }
  }

  function canClose() {
    return !(inFlight.current || applyPendingRef.current || applyUnknown);
  }

  function close() {
    if (canClose()) onClose();
  }

  const language = useFullWebLanguage();
  const storeTheme = useThemeStore((state) => state.resolvedTheme);
  const dataPalette = useMemo(() => getDataPalette(storeTheme), [storeTheme]);
  const displaySeries = useMemo<DisplayUsageBucket[]>(() => {
    if (!usage || usage.series.length === 0) return [];
    const spanMs = usage.toMs - usage.fromMs;
    // Preserve tiny synthetic test windows verbatim
    if (spanMs <= 60_000) return usage.series;
    const HOUR_MS = 3_600_000;
    const DAY_MS = 86_400_000;
    const targetStepMs =
      spanMs <= 30 * HOUR_MS
        ? HOUR_MS
        : spanMs <= 8 * DAY_MS
          ? 6 * HOUR_MS
          : DAY_MS;
    const baseStepMs = Math.max(usage.bucketMs || HOUR_MS, targetStepMs);
    const lastSourceMs = Math.max(usage.toMs - 1, usage.fromMs);
    let stepMs = baseStepMs;
    let startBucket = Math.floor(usage.fromMs / stepMs) * stepMs;
    let endBucket = Math.floor(lastSourceMs / stepMs) * stepMs;
    let slotCount = Math.max(1, Math.floor((endBucket - startBucket) / stepMs) + 1);
    if (slotCount > 32) {
      const minStepMs = Math.max(stepMs, Math.ceil(Math.max(1, lastSourceMs - usage.fromMs) / 31));
      const factor = Math.max(2, Math.ceil(minStepMs / baseStepMs));
      stepMs = baseStepMs * factor;
      startBucket = Math.floor(usage.fromMs / stepMs) * stepMs;
      endBucket = Math.floor(lastSourceMs / stepMs) * stepMs;
      slotCount = Math.max(1, Math.floor((endBucket - startBucket) / stepMs) + 1);
      while (slotCount > 32) {
        stepMs += baseStepMs;
        startBucket = Math.floor(usage.fromMs / stepMs) * stepMs;
        endBucket = Math.floor(lastSourceMs / stepMs) * stepMs;
        slotCount = Math.max(1, Math.floor((endBucket - startBucket) / stepMs) + 1);
      }
    }
    const map = new Map<number, DisplayUsageBucket>();
    for (let i = 0; i < slotCount; i += 1) {
      const b = startBucket + i * stepMs;
      map.set(b, { bucketMs: b, calls: 0, failure: 0, totalTokens: 0 });
    }
    for (const pt of usage.series) {
      const rawB = Math.floor(pt.bucketMs / stepMs) * stepMs;
      const clampedB = Math.min(endBucket, Math.max(startBucket, rawB));
      const slot = map.get(clampedB);
      if (slot) {
        slot.calls += pt.calls;
        slot.failure += pt.failure;
        slot.totalTokens += pt.totalTokens;
      }
    }
    return Array.from(map.values());
  }, [usage]);
  const maxBucketCalls =
    displaySeries.length > 0 ? Math.max(...displaySeries.map((p) => p.calls), 0) : 0;
  const usageSpanMs = usage ? Math.max(0, usage.toMs - usage.fromMs) : 0;
  const usageCoverageStateForChart: UsageState = usage?.coverage.state ?? 'unknown';
  const usageTrendOption = useMemo(
    () =>
      buildKeyUsageTrendOption({
        series: displaySeries,
        spanMs: usageSpanMs,
        state: usageCoverageStateForChart,
        dataPalette,
        language,
      }),
    [dataPalette, displaySeries, language, usageCoverageStateForChart, usageSpanMs]
  );
  const unifiedStatus = record ? keyUnifiedStatus(record) : null;
  const superseded = !!record && record.lifecycle === 'superseded';
  const revokedOrSuperseded =
    !!record && (superseded || record.runtimeState === 'revoked_observed');
  const displayName = record
    ? keyDisplayName(record, k('installerOrigin'), k('adoptedOrigin'), t('keysUnnamed'))
    : '';
  const originLabel = record ? k(keyOriginTextKey(record.origin)) : '';
  const showOriginInMeta = Boolean(record) && displayName !== originLabel;
  const createdTime = record ? detailTime(record.createdAtMs) : null;
  const lastRotatedTime =
    record && record.lastRotatedAtMs && record.lastRotatedAtMs > 0
      ? detailTime(record.lastRotatedAtMs)
      : null;
  const revokedTime =
    record && record.revokedAtMs && record.revokedAtMs > 0
      ? detailTime(record.revokedAtMs)
      : null;
  const overviewUsage = usage24h ?? (activeUsageWindow === '24h' ? usage : null);
  const overviewCoverageState: UsageState = overviewUsage?.coverage.state ?? 'unknown';
  const overviewProven =
    overviewCoverageState === 'complete' || overviewCoverageState === 'partial';
  const overviewLastActiveTime =
    overviewProven && overviewUsage?.totals?.lastUsedAtMs
      ? detailTime(overviewUsage.totals.lastUsedAtMs)
      : null;
  const overviewTopModels = overviewProven ? (overviewUsage?.topModels ?? []) : [];
  const overviewModelCount = overviewProven
    ? Math.max(overviewUsage?.modelCount ?? overviewTopModels.length, overviewTopModels.length)
    : 0;
  const isZeroSeries =
    !!usage &&
    usage.series.length > 0 &&
    usage.totals?.calls === 0 &&
    usage.series.every((p) => p.calls === 0);

  function switchDetailTab(nextTab: 'overview' | 'usage') {
    setDetailTab(nextTab);
    if (detailDrawerBodyRef.current) {
      detailDrawerBodyRef.current.scrollTop = 0;
    }
  }

  return (
    <>
      <Drawer
        open={open && visible}
        width={DRAWER_WIDTH.MD}
        className="key-detail-drawer"
        bodyRef={detailDrawerBodyRef}
        closeLabel={t('keysCloseDetails')}
        closeDisabled={busy || applyPending || applyUnknown}
        onBeforeClose={() => !editing && canClose()}
        onClose={onClose}
        title={
          record ? (
            <DrawerIdentityHeader
              icon={<FullWebIcon name="key" size={18} />}
              iconBoxed
              title={displayName}
              titleAs="h3"
              titleTrailing={
                !superseded ? (
                  <button
                    type="button"
                    className="key-drawer-title-edit-btn"
                    data-testid="detail-edit-name"
                    aria-label={k('rename')}
                    title={k('rename')}
                    disabled={busy || applyPending || applyUnknown}
                    onClick={() => setEditing(true)}
                  >
                    <FullWebIcon name="edit" size={14} />
                  </button>
                ) : undefined
              }
              metaItems={[
                unifiedStatus ? (
                  <DrawerStatusPill key="status" tone={statusPillTone(unifiedStatus.pillClass)} dot>
                    <span data-testid="detail-runtime-state">{k(unifiedStatus.labelKey)}</span>
                  </DrawerStatusPill>
                ) : null,
                <span key="secret" className="key-drawer-meta-secret">
                  {!superseded ? (
                    <DrawerCopyableMeta
                      onCopy={copySavedSecret}
                      copied={copyFeedback === 'copied'}
                      ariaLabel={k('copyKey')}
                      title={k('copyKey')}
                      disabled={busy || copying || applyPending || applyUnknown}
                      testId="copy-saved-client-secret"
                    >
                      <code>{record.maskedPreview}</code>
                    </DrawerCopyableMeta>
                  ) : (
                    <code>{record.maskedPreview}</code>
                  )}
                  {copyFeedback && (
                    <span
                      role="status"
                      className={`key-copy-inline-status ${
                        copyFeedback === 'copied' ? 'is-copied' : 'is-failed'
                      }`}
                      data-testid="copy-saved-client-secret-feedback"
                    >
                      {k(copyFeedback === 'copied' ? 'keyCopied' : 'keyCopyFailed')}
                    </span>
                  )}
                </span>,
                showOriginInMeta ? <span key="origin">{originLabel}</span> : null,
                createdTime ? (
                  <time key="created" dateTime={createdTime.iso}>
                    {createdTime.label}
                  </time>
                ) : null,
              ]}
            />
          ) : (
            <DrawerIdentityHeader
              icon={<FullWebIcon name="key" size={18} />}
              iconBoxed
              title={t('keysDetails')}
              titleAs="h3"
            />
          )
        }
        footer={
          <DrawerActionFooter
            rightTestId={
              record && !superseded && onRequestOperation ? 'key-detail-operation-entry' : undefined
            }
            left={
              <>
                {record && !superseded && onRequestOperation && (
                  <DrawerDangerButton
                    type="button"
                    data-testid="detail-revoke-key"
                    disabled={
                      busy || applyPending || applyUnknown || editing || Boolean(operationPending)
                    }
                    onClick={() => onRequestOperation('revoke')}
                  >
                    {k('quickRevoke')}
                  </DrawerDangerButton>
                )}
                <Button
                  type="button"
                  variant="secondary"
                  size="sm"
                  aria-label={t('keysReadDetails')}
                  disabled={busy || applyPending || applyUnknown}
                  onClick={() => {
                    clearUsageStartupRetry(true);
                    void read(ownerEpoch.current);
                    void readUsage(ownerEpoch.current, activeUsageWindow);
                    if (activeUsageWindow !== '24h') {
                      void readUsage24h(ownerEpoch.current);
                    }
                  }}
                >
                  <FullWebIcon name="refresh" size={14} />
                  {t('keysReadDetails')}
                </Button>
              </>
            }
            right={
              record && !superseded ? (
                <>
                  {onRequestOperation && (
                    <Button
                      type="button"
                      variant="secondary"
                      size="sm"
                      data-testid="detail-regenerate-key"
                      disabled={
                        busy || applyPending || applyUnknown || editing || Boolean(operationPending)
                      }
                      onClick={() => onRequestOperation('regenerate')}
                    >
                      {k('regenerateKey')}
                    </Button>
                  )}
                  <Button
                    type="button"
                    variant="primary"
                    size="sm"
                    data-testid="detail-copy-secret"
                    disabled={busy || copying || applyPending || applyUnknown}
                    onClick={() => void copySavedSecret()}
                  >
                    <FullWebIcon name={copyFeedback === 'copied' ? 'check' : 'copy'} size={14} />
                    {k('copyFullKey')}
                  </Button>
                </>
              ) : (
                <Button
                  type="button"
                  variant="secondary"
                  size="sm"
                  disabled={!canClose()}
                  onClick={close}
                >
                  {k('closeDialog')}
                </Button>
              )
            }
          />
        }
      >
        <DrawerBodyShell
          className="key-detail-workspace"
          ariaBusy={busy}
          tabs={
            <DrawerTabBar
              ariaLabel={t('keysDetails')}
              activeTab={detailTab}
              onChange={switchDetailTab}
              scrollContainerRef={detailDrawerBodyRef}
              items={[
                {
                  id: 'overview',
                  label: k('detailTabOverview'),
                  testId: 'key-detail-tab-overview',
                  tabDomId: 'key-detail-tab-btn-overview',
                  panelId: 'key-detail-panel-overview',
                },
                {
                  id: 'usage',
                  label: k('detailTabUsage'),
                  testId: 'key-detail-tab-usage',
                  tabDomId: 'key-detail-tab-btn-usage',
                  panelId: 'key-detail-panel-usage',
                },
              ]}
            />
          }
        >
          <DrawerViewport>
            {busy && (
              <DrawerNotice tone="info" icon={<FullWebIcon name="clock" size={16} />} role="status">
                {t('keysLoading')}
              </DrawerNotice>
            )}
            {message && !editing && (
              <DrawerNotice tone="warning" icon={<FullWebIcon name="info" size={16} />} role="status">
                {message}
              </DrawerNotice>
            )}
            {record && (
              <>
                <DrawerTabPanel
                  id="key-detail-panel-overview"
                  labelledBy="key-detail-tab-btn-overview"
                  asTabPanel
                  testId="key-detail-panel-overview"
                  hidden={detailTab !== 'overview'}
                >
                  {revokedOrSuperseded ? (
                    <DrawerNotice tone="neutral" title={t('keysRevokedHistory')} role="status">
                      {k('detailRevokedHelp')}
                    </DrawerNotice>
                  ) : gatewayPending && record.runtimeState === 'not_applied' ? (
                    <DrawerNotice
                      tone="warning"
                      icon={<FullWebIcon name="alert" size={16} />}
                      role="status"
                    >
                      {k('connectionPending')}
                    </DrawerNotice>
                  ) : record.runtimeState === 'effective_observed' ? null : record.runtimeState ===
                      'effect_unknown' || applyUnknown ? (
                    <DrawerNotice
                      tone="warning"
                      icon={<FullWebIcon name="alert" size={16} />}
                      role="alert"
                      testId="detail-apply-recovery"
                    >
                      <div className="key-drawer-status-notice-body">
                        <p>{k('detailUnknownHelp')}</p>
                        {applyAborted && applyError !== k('operationAborted') && (
                          <p data-testid="detail-apply-aborted">{k('operationAborted')}</p>
                        )}
                        {(applyError || record.effectCode) && (
                          <p data-testid="detail-apply-blocker-reason">
                            {applyError || k(effectBlockerTextKey(record.effectCode))}
                          </p>
                        )}
                        {applyErrorDetail &&
                          (applyErrorCode || record.effectCode) ===
                            'external_management_key_rejected' && (
                            <details
                              className="fresh-external-blocker-detail"
                              data-testid="detail-apply-blocker-detail"
                            >
                              <summary>{x('details')}</summary>
                              <p>{applyErrorDetail}</p>
                            </details>
                          )}
                        <div className="key-toolbar-actions">
                          <button
                            type="button"
                            className="btn btn-secondary btn-sm"
                            data-testid="detail-reconcile-apply"
                            disabled={busy || applyPending}
                            onClick={() => void reconcileApply()}
                          >
                            {k('checkOperationResult')}
                          </button>
                          {onRebind &&
                            [
                              'external_management_key_rejected',
                              'external_target_moved',
                            ].includes(applyErrorCode || record.effectCode || '') && (
                              <button
                                type="button"
                                className="btn btn-primary btn-sm"
                                data-testid="detail-rebind-action"
                                disabled={busy || applyPending}
                                onClick={onRebind}
                              >
                                {k('rebindConnection')}
                              </button>
                            )}
                        </div>
                      </div>
                    </DrawerNotice>
                  ) : (
                    <DrawerNotice
                      tone="warning"
                      icon={<FullWebIcon name="alert" size={16} />}
                      role="alert"
                      testId="detail-apply-recovery"
                    >
                      <div className="key-drawer-status-notice-body">
                        <p>{k('detailApplyHelp')}</p>
                        {applyAborted && applyError !== k('operationAborted') && (
                          <p data-testid="detail-apply-aborted">{k('operationAborted')}</p>
                        )}
                        {(applyError || record.effectCode) && (
                          <p data-testid="detail-apply-blocker-reason">
                            {applyError || k(effectBlockerTextKey(record.effectCode))}
                          </p>
                        )}
                        {applyErrorDetail &&
                          (applyErrorCode || record.effectCode) ===
                            'external_management_key_rejected' && (
                            <details
                              className="fresh-external-blocker-detail"
                              data-testid="detail-apply-blocker-detail"
                            >
                              <summary>{x('details')}</summary>
                              <p>{applyErrorDetail}</p>
                            </details>
                          )}
                        <div className="key-toolbar-actions">
                          {![
                            'external_management_key_rejected',
                            'external_target_moved',
                            'external_cpa_version_unsupported',
                          ].includes(applyErrorCode) && (
                            <button
                              type="button"
                              className="btn btn-secondary btn-sm"
                              data-testid="detail-retry-apply"
                              disabled={busy || applyPending}
                              onClick={() => void retryApply()}
                            >
                              {k('retryApplyKey')}
                            </button>
                          )}
                          {onRebind &&
                            [
                              'external_management_key_rejected',
                              'external_target_moved',
                            ].includes(applyErrorCode || record.effectCode || '') && (
                              <button
                                type="button"
                                className="btn btn-primary btn-sm"
                                data-testid="detail-rebind-action"
                                disabled={busy || applyPending}
                                onClick={onRebind}
                              >
                                {k('rebindConnection')}
                              </button>
                            )}
                        </div>
                      </div>
                    </DrawerNotice>
                  )}
                  <DrawerSectionCard
                    variant="default"
                    icon={<FullWebIcon name="chart" size={15} />}
                    title={k('overviewSnapshotTitle')}
                    titleBadge={<DrawerScopePill>{k('usageWindow24h')}</DrawerScopePill>}
                    metaActions={
                      <Button
                        type="button"
                        variant="ghost"
                        size="xs"
                        onClick={() => switchDetailTab('usage')}
                      >
                        {k('viewUsage')} →
                      </Button>
                    }
                  >
                    <div className="key-overview-kpi-strip" data-testid="key-overview-kpi-strip">
                      <MetricSummaryGrid
                        columns={4}
                        cards={buildKeySummaryKpiCards(
                          overviewUsage?.totals,
                          overviewCoverageState
                        )}
                      />
                    </div>
                  </DrawerSectionCard>
                  <DrawerSectionCard
                    variant="default"
                    icon={<FullWebIcon name="info" size={15} />}
                    title={k('overviewKeyInfoTitle')}
                  >
                    <DrawerDefinitionList testId="key-detail-metadata">
                      <DrawerDlRow
                        label={t('keysIdentity')}
                        mono
                        onCopy={copyKeyId}
                        copied={idCopyFeedback === 'copied'}
                        copyAriaLabel={`${k('copyKey')}: ${t('keysIdentity')}`}
                        copyTitle={t('keysCopy')}
                        copyTestId="detail-copy-key-id"
                      >
                        <code className="key-id-code" title={id}>
                          {id}
                        </code>
                      </DrawerDlRow>
                      <DrawerDlRow
                        label={k('overviewLastActiveLabel')}
                        muted={overviewProven && !overviewLastActiveTime}
                        valueTestId="detail-last-active"
                      >
                        {overviewLastActiveTime ? (
                          <time dateTime={overviewLastActiveTime.iso}>
                            {overviewLastActiveTime.label}
                          </time>
                        ) : overviewProven ? (
                          k('noUsageInWindow')
                        ) : (
                          '—'
                        )}
                      </DrawerDlRow>
                      {overviewTopModels.length > 0 && (
                        <DrawerDlRow
                          label={k('overviewActiveModelsLabel')}
                          valueTestId="detail-active-models"
                        >
                          <span className="key-overview-models-inline">
                            {overviewTopModels.slice(0, 3).map((entry) => (
                              <span key={entry.model} className="key-model-chip is-pair">
                                <span className="key-model-chip-name">{entry.model}</span>
                              </span>
                            ))}
                            {overviewModelCount > 3 && (
                              <span className="key-model-more">+{overviewModelCount - 3}</span>
                            )}
                            {overviewModelCount > 1 && (
                              <span className="key-overview-models-summary">
                                {formatKeyModelsSummaryLine(overviewModelCount)}
                              </span>
                            )}
                          </span>
                        </DrawerDlRow>
                      )}
                      {lastRotatedTime && (
                        <DrawerDlRow
                          label={k('lastRotatedLabel')}
                          valueTestId="detail-last-rotated-at"
                        >
                          <time dateTime={lastRotatedTime.iso}>{lastRotatedTime.label}</time>
                        </DrawerDlRow>
                      )}
                      {revokedTime && (
                        <DrawerDlRow label={k('revokedAtLabel')}>
                          <time dateTime={revokedTime.iso}>{revokedTime.label}</time>
                        </DrawerDlRow>
                      )}
                      {record.operationId && (
                        <DrawerDlRow label={k('operationIdLabel')} mono>
                          <code data-testid="detail-operation-id">{record.operationId}</code>
                        </DrawerDlRow>
                      )}
                    </DrawerDefinitionList>
                    {record.origin === 'adopted' &&
                      !revokedOrSuperseded &&
                      !(record.lastRotatedAtMs && record.lastRotatedAtMs > 0) && (
                        <p className="key-field-help key-adopted-note">{k('adoptedOriginNote')}</p>
                      )}
                  </DrawerSectionCard>
                  {!revokedOrSuperseded && (
                    <AccessInfoSummary
                      client={client}
                      auth={auth}
                      onUnauthorized={onUnauthorized}
                      info={accessInfo ?? undefined}
                      reloadToken={reloadToken}
                      visible={open && visible}
                      compact
                      clientOnly
                      drawerEmbed
                      maskedKeyPreview={record.maskedPreview}
                      onCopyAuthHeader={copySavedAuthHeader}
                      authHeaderCopyDisabled={busy || copying || applyPending || applyUnknown}
                      defaultProtocolsCollapsed={false}
                      testIdPrefix="detail-access-info"
                    />
                  )}
                </DrawerTabPanel>
                <DrawerTabPanel
                  id="key-detail-panel-usage"
                  labelledBy="key-detail-tab-btn-usage"
                  asTabPanel
                  testId="key-detail-panel-usage"
                  hidden={detailTab !== 'usage'}
                >
                  <section
                    className="key-usage-workspace"
                    data-testid="key-detail-usage-policy"
                  >
                    <div className="key-usage-control-card">
                      <div className="key-usage-filter-row">
                        <div className="key-usage-filter-left">
                          <div data-testid="usage-window-tabs">
                            <SegmentedTabs
                              items={[
                                { id: '24h', label: k('usageWindow24h') },
                                { id: '7d', label: k('usageWindow7d') },
                                { id: '30d', label: k('usageWindow30d') },
                              ]}
                              activeTab={activeUsageWindow}
                              onChange={(next) => switchUsageWindow(next as UsageWindow)}
                              ariaLabel={k('usageWindowLabel')}
                              idBase="key-usage-window"
                              className="key-usage-window-tabs"
                              size="sm"
                              responsiveFullWidth={false}
                            />
                          </div>
                          {usage && usage.totals && (
                            <span
                              className="key-usage-window-inline"
                              title={`${detailTime(usage.fromMs).label} — ${detailTime(usage.toMs).label}`}
                            >
                              {usageCompactTimeText(usage.fromMs, 'created') ?? detailTime(usage.fromMs).label} —{' '}
                              {usageCompactTimeText(usage.toMs, 'created') ?? detailTime(usage.toMs).label}
                            </span>
                          )}
                        </div>
                        <div className="key-usage-links" data-testid="key-usage-links">
                          <CrossPageLink
                            route="/monitoring"
                            apiKeyId={id}
                            className="btn btn-secondary btn-sm"
                            testId="detail-link-monitoring"
                            disabled={!canClose()}
                            onNavigate={close}
                          >
                            {k('usageLinkMonitoring')}
                          </CrossPageLink>
                          <CrossPageLink
                            route="/usage-analytics"
                            apiKeyId={id}
                            className="btn btn-secondary btn-sm"
                            testId="detail-link-analytics"
                            disabled={!canClose()}
                            onNavigate={close}
                          >
                            {k('usageLinkAnalytics')}
                          </CrossPageLink>
                        </div>
                      </div>
                    </div>

                    {!usage && !usageFailed ? (
                      <p className="key-field-help" role="status">
                        {k('usageReading')}
                      </p>
                    ) : (
                      <>
                        {usageCoverage.state !== 'complete' && (
                          <DrawerNotice
                            tone={usageCoverage.state === 'unavailable' ? 'danger' : 'warning'}
                            icon={<FullWebIcon name="alert" size={16} />}
                            role="status"
                            testId="key-usage-state"
                            title={
                              <span className="key-usage-coverage-title">
                                <span>{k('usageCoverageLabel')}</span>
                                <DrawerStatusPill tone={usageCoverageTone(usageCoverage.state)} dot>
                                  {usageStateText(usageCoverage.state)}
                                </DrawerStatusPill>
                              </span>
                            }
                          >
                            <p data-testid="key-usage-reason">
                              {usageReasonText(usageCoverage.reason)}
                            </p>
                          </DrawerNotice>
                        )}
                        {usage && usage.totals && (
                          <>
                            <div className="key-usage-totals" data-testid="key-usage-totals">
                              <MetricSummaryGrid
                                columns={4}
                                cards={buildKeySummaryKpiCards(usage.totals, usage.coverage.state)}
                              />
                            </div>
                            {!(
                              displaySeries.length > 0 &&
                              (maxBucketCalls > 0 || isZeroSeries)
                            ) &&
                              (usage.coverage.state === 'complete' ||
                                usage.coverage.state === 'partial') && (
                                <p
                                  className="key-usage-empty-state"
                                  data-testid="key-usage-empty"
                                >
                                  {k('usageNoCalls')}
                                </p>
                              )}
                          </>
                        )}
                      </>
                    )}

                    {usage &&
                      usage.totals &&
                      displaySeries.length > 0 &&
                      (maxBucketCalls > 0 || isZeroSeries) && (
                        <DrawerSectionCard
                          variant="default"
                          icon={<FullWebIcon name="trendingUp" size={15} />}
                          title={`${k('usageTrendTitle')}${isZeroSeries ? ` · ${k('usageNoCalls')}` : ''}`}
                          testId="key-usage-series"
                          metaActions={
                            <span className="key-usage-legend">
                              <span className="key-usage-legend-item is-success">
                                {k('usageSuccess')}
                              </span>
                              <span className="key-usage-legend-item is-failure">
                                {k('usageFailure')}
                              </span>
                              <span className="key-usage-legend-item is-tokens">
                                {k('cardMetricTokens')}
                              </span>
                            </span>
                          }
                        >
                          <div
                            className="key-usage-bars"
                            data-testid="key-usage-bars"
                          >
                            <EChartsView
                              option={usageTrendOption}
                              className="key-usage-echarts"
                              ariaLabel={k('usageTrendTitle')}
                            />
                          </div>
                        </DrawerSectionCard>
                      )}

                    {usage &&
                      usage.totals &&
                      (usage.coverage.state === 'complete' ||
                        usage.coverage.state === 'partial') &&
                      usage.totals.totalTokens > 0 && (
                        <DrawerSectionCard
                          variant="default"
                          icon={<IconBinary size={15} />}
                          title={k('usageTokenMixTitle')}
                          testId="key-usage-token-mix"
                          metaActions={
                            usage.totals.cachedTokens !== undefined &&
                            usage.totals.inputTokens > 0 ? (
                              <span className="key-usage-cache-pill">
                                {k('usageInputCacheHitRate')}{' '}
                                {Math.min(
                                  100,
                                  (Math.max(0, usage.totals.cachedTokens) /
                                    usage.totals.inputTokens) *
                                    100
                                ).toFixed(1)}%
                              </span>
                            ) : undefined
                          }
                        >
                          {(() => {
                            const state = usage.coverage.state;
                            const inputTok = Math.max(0, usage.totals.inputTokens);
                            const cachedTok = Math.min(
                              inputTok,
                              Math.max(0, usage.totals.cachedTokens ?? 0)
                            );
                            const uncachedInput = Math.max(0, inputTok - cachedTok);
                            const outputTok = Math.max(0, usage.totals.outputTokens);
                            const reasoningTok = Math.max(0, usage.totals.reasoningTokens ?? 0);
                            const segmentTotal = Math.max(
                              1,
                              uncachedInput + cachedTok + outputTok + reasoningTok
                            );
                            const segments = [
                              {
                                key: 'uncached',
                                label: k('usageUncachedInput'),
                                tokens: uncachedInput,
                                tone: 'is-uncached',
                              },
                              ...(cachedTok > 0
                                ? [
                                    {
                                      key: 'cached',
                                      label: k('usageCachedTokensLabel'),
                                      tokens: cachedTok,
                                      tone: 'is-cached',
                                    },
                                  ]
                                : []),
                              {
                                key: 'output',
                                label: k('usageOutputTokens'),
                                tokens: outputTok,
                                tone: 'is-output',
                              },
                              ...(reasoningTok > 0
                                ? [
                                    {
                                      key: 'reasoning',
                                      label: k('usageReasoningTokens'),
                                      tokens: reasoningTok,
                                      tone: 'is-reasoning',
                                    },
                                  ]
                                : []),
                            ];
                            return (
                              <div className="key-token-mix-body">
                                <div className="key-token-mix-track" aria-hidden="true">
                                  {segments
                                    .filter((seg) => seg.tokens > 0)
                                    .map((seg) => (
                                      <span
                                        key={seg.key}
                                        className={`key-token-mix-seg ${seg.tone}`}
                                        style={{
                                          width: `${((seg.tokens / segmentTotal) * 100).toFixed(2)}%`,
                                        }}
                                      />
                                    ))}
                                </div>
                                <div className="key-token-mix-legend">
                                  {segments.map((seg) => {
                                    const pct = ((seg.tokens / segmentTotal) * 100).toFixed(1);
                                    return (
                                      <div
                                        key={seg.key}
                                        className="key-token-mix-item"
                                        title={`${seg.label}: ${usageCountText(seg.tokens, state)} (${pct}%)`}
                                      >
                                        <span className={`key-token-mix-dot ${seg.tone}`} aria-hidden="true" />
                                        <span className="key-token-mix-name">{seg.label}</span>
                                        <strong className="key-token-mix-val">
                                          {usageCompactNumberText(seg.tokens, state)}
                                        </strong>
                                        <span className="key-token-mix-pct">({pct}%)</span>
                                      </div>
                                    );
                                  })}
                                </div>
                              </div>
                            );
                          })()}
                        </DrawerSectionCard>
                      )}

                    {usage &&
                      usage.topModels &&
                      usage.topModels.length > 0 &&
                      (usage.coverage.state === 'complete' ||
                        usage.coverage.state === 'partial') && (
                        <DrawerSectionCard
                          variant="default"
                          icon={<IconBot size={15} />}
                          title={k('usageModelBreakdownTitle')}
                          testId="key-usage-models"
                          metaActions={
                            <span className="key-usage-models-count">
                              {formatKeyModelsSummaryLine(
                                Math.max(usage.modelCount ?? usage.topModels.length, usage.topModels.length)
                              )}
                            </span>
                          }
                        >
                          {(() => {
                            const state = usage.coverage.state;
                            const hasAnyCost = usage.topModels.some(
                              (m) => m.estimatedCostUsd !== undefined && m.estimatedCostUsd > 0
                            );
                            const sorted = [...usage.topModels].sort((a, b) => {
                              if (hasAnyCost) {
                                const ca = a.estimatedCostUsd ?? -1;
                                const cb = b.estimatedCostUsd ?? -1;
                                if (cb !== ca) return cb - ca;
                              }
                              if (b.calls !== a.calls) return b.calls - a.calls;
                              return a.model.localeCompare(b.model);
                            });
                            const maxMetric = Math.max(
                              ...sorted.map((m) =>
                                hasAnyCost ? (m.estimatedCostUsd ?? 0) : m.calls
                              ),
                              0.000001
                            );
                            return (
                              <div className="key-usage-models-list">
                                {sorted.map((m) => {
                                  const sharePct = Math.min(
                                    100,
                                    Math.max(
                                      3,
                                      ((hasAnyCost ? (m.estimatedCostUsd ?? 0) : m.calls) / maxMetric) * 100
                                    )
                                  );
                                  return (
                                    <div key={m.model} className="key-usage-model-row">
                                      <div className="key-usage-model-top">
                                        <span className="key-usage-model-name" title={m.model}>
                                          {m.model}
                                        </span>
                                        <span className="key-usage-model-metrics">
                                          <span>
                                            {usageCompactNumberText(m.calls, state)} {k('callsUnit')}
                                          </span>
                                          {m.totalTokens !== undefined && m.totalTokens > 0 && (
                                            <>
                                              <span className="key-usage-model-sep">·</span>
                                              <span>
                                                {usageCompactNumberText(m.totalTokens, state)}
                                              </span>
                                            </>
                                          )}
                                          {m.estimatedCostUsd !== undefined && (
                                            <>
                                              <span className="key-usage-model-sep">·</span>
                                              <strong>
                                                {usageCompactUsdText(m.estimatedCostUsd, state)}
                                              </strong>
                                            </>
                                          )}
                                        </span>
                                      </div>
                                      <div className="key-usage-model-bar-track" aria-hidden="true">
                                        <span
                                          className="key-usage-model-bar-fill"
                                          style={{ width: `${sharePct.toFixed(1)}%` } as CSSProperties}
                                        />
                                      </div>
                                    </div>
                                  );
                                })}
                              </div>
                            );
                          })()}
                        </DrawerSectionCard>
                      )}
                  </section>
                </DrawerTabPanel>
              </>
            )}
          </DrawerViewport>
        </DrawerBodyShell>
      </Drawer>
      <FreshKeyRenameModal
        open={open && editing}
        visible={visible}
        record={record}
        client={client}
        credential={credential}
        onUnauthorized={onUnauthorized}
        onPending={onPending}
        onSaved={(updated) => {
          setRecord(updated);
          onSaved(updated);
        }}
        onClose={() => setEditing(false)}
      />
    </>
  );
}
