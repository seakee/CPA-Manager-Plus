import { useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react';
import { useHref, useInRouterContext, useLocation, useNavigate } from 'react-router-dom';
import { fullWebLanguage, fullWebText as t } from '../i18n/fullweb';
import { keyWorkspaceText as k } from '../i18n/freshKeyWorkspace';
import { externalWorkspaceText as x } from '../i18n/freshExternal';
import { prefixRouteBase } from '../router/routeBase';
import { readRoutingContext } from './routingContext';
import { FullWebIcon } from './FullWebIcon';
import './freshKeys.css';
import { blockerOf } from './externalState';
import { PublicApiError, type PublicApiClient } from './publicApi';
import {
  parseKeyUsage,
  usageCountText,
  usageCoveragePill,
  usageReasonText,
  usageStateText,
  usageTimeText,
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

type UsageWindow = '24h' | '7d' | '30d';

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
}) {
  const [record, setRecord] = useState<Key | null>(null);
  const [usage, setUsage] = useState<KeyUsage | null>(null);
  const [usage24h, setUsage24h] = useState<KeyUsage | null>(null);
  const [usageFailed, setUsageFailed] = useState(false);
  const [usageWindowState, setUsageWindowState] = useState<{
    forId: string;
    window: UsageWindow;
  }>({ forId: id, window: '24h' });
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
  const usageSeq = useRef(0);
  const detailDrawerBodyRef = useRef<HTMLDivElement | null>(null);
  const auth = { kind: 'admin' as const, value: credential };
  const onPendingRef = useRef(onPending);
  onPendingRef.current = onPending;

  useEffect(() => {
    return () => {
      onPendingRef.current(false);
      clearTimeout(idCopyTimerRef.current);
      idCopyTimerRef.current = undefined;
    };
  }, []);
  const resource = 'api-keys/' + id;
  const activeUsageWindow: UsageWindow =
    usageWindowState.forId === id ? usageWindowState.window : '24h';
  const usageCoverage = usage?.coverage ?? {
    state: 'unknown' as UsageState,
    reason: 'usage_read_failed',
  };
  const usageStateClass = usageCoveragePill(usageCoverage.state);

  useEffect(() => {
    active.current = true;
    const ownerAtMount = ++ownerEpoch.current;
    inFlight.current = false;
    applyPendingRef.current = false;
    copyingRef.current = false;
    setBusy(false);
    setCopying(false);
    setRecord(null);
    setUsage(null);
    setUsage24h(null);
    setUsageFailed(false);
    setUsageWindowState({ forId: id, window: '24h' });
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
    if (open) {
      void read(ownerAtMount);
      void readUsage(ownerAtMount, '24h');
    }
    return () => {
      active.current = false;
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
    setDetailTab(initialTab);
  }, [initialTab]);

  useEffect(() => {
    if (initialAction === 'rename' && record && !initialActionApplied.current && !applyPending) {
      initialActionApplied.current = true;
      setEditing(true);
    }
  }, [initialAction, record, applyPending]);

  function validate(value: Key) {
    if (!validKey(value) || value.id !== id) throw new Error();
    return value;
  }

  function failure(err: unknown) {
    if (err instanceof PublicApiError && err.kind === 'authentication') onUnauthorized();
    else setMessage(t('keysUnavailable'));
  }

  async function read(ownerAtStart = ownerEpoch.current) {
    if (inFlight.current || applyPendingRef.current) return;
    inFlight.current = true;
    setBusy(true);
    setMessage('');
    try {
      const value = validate(await client.get<Key>(resource, auth));
      if (!active.current || ownerEpoch.current !== ownerAtStart) return;
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
      if (active.current && ownerEpoch.current === ownerAtStart) failure(err);
    } finally {
      if (ownerEpoch.current === ownerAtStart) {
        inFlight.current = false;
        if (active.current) setBusy(false);
      }
    }
  }

  async function copySavedSecret() {
    if (copyingRef.current || !record || record.lifecycle === 'superseded') return;
    const ownerAtStart = ownerEpoch.current;
    copyingRef.current = true;
    setCopying(true);
    setCopyFeedback('');
    try {
      const value = await client.post<{ secret: string }>(resource + '/secret', {}, auth);
      if (!active.current || ownerEpoch.current !== ownerAtStart) return;
      if (!value || typeof value.secret !== 'string') throw new Error();
      if (!validSavedSecret(record, value.secret)) {
        const latest = validate(await client.get<Key>(resource, auth));
        if (!active.current || ownerEpoch.current !== ownerAtStart) return;
        setRecord(latest);
        onSaved(latest);
        if (latest.lifecycle === 'superseded' || !validSavedSecret(latest, value.secret))
          throw new Error();
      }
      await navigator.clipboard.writeText(value.secret);
      if (active.current && ownerEpoch.current === ownerAtStart) setCopyFeedback('copied');
    } catch (err) {
      if (!active.current || ownerEpoch.current !== ownerAtStart) return;
      if (err instanceof PublicApiError && err.kind === 'authentication') onUnauthorized();
      else {
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
    window: UsageWindow = activeUsageWindow
  ) {
    const seq = ++usageSeq.current;
    let usagePath = `${resource}/usage`;
    if (window === '7d' || window === '30d') {
      const toMs = Date.now();
      const spanMs = window === '7d' ? 7 * 86_400_000 : 30 * 86_400_000;
      const fromMs = Math.max(1, toMs - spanMs);
      usagePath = `${resource}/usage?from_ms=${fromMs}&to_ms=${toMs}`;
    }
    try {
      const parsed = parseKeyUsage(await client.get<unknown>(usagePath, auth), id);
      if (!parsed) throw new Error('Invalid usage response');
      if (!active.current || ownerEpoch.current !== ownerAtStart || seq !== usageSeq.current) {
        return;
      }
      setUsage(parsed);
      if (window === '24h') setUsage24h(parsed);
      setUsageFailed(false);
    } catch (err) {
      if (!active.current || ownerEpoch.current !== ownerAtStart || seq !== usageSeq.current) {
        return;
      }
      if (err instanceof PublicApiError && err.kind === 'authentication') {
        onUnauthorized();
        return;
      }
      setUsage(null);
      if (window === '24h') setUsage24h(null);
      setUsageFailed(true);
    }
  }

  function switchUsageWindow(nextWindow: UsageWindow) {
    if (nextWindow === activeUsageWindow) return;
    setUsageWindowState({ forId: id, window: nextWindow });
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

  const maxBucketCalls =
    usage && usage.series.length > 0 ? Math.max(...usage.series.map((p) => p.calls), 0) : 0;
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
  const overviewLastUsedText =
    overviewCoverageState === 'unknown' || overviewCoverageState === 'unavailable'
      ? '—'
      : (usageTimeText(overviewUsage?.totals?.lastUsedAtMs ?? null) ?? '—');
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
        width="clamp(540px, 45vw, 720px)"
        className="key-detail-drawer"
        bodyRef={detailDrawerBodyRef}
        closeLabel={t('keysCloseDetails')}
        closeDisabled={busy || applyPending || applyUnknown}
        onBeforeClose={() => !editing && canClose()}
        onClose={onClose}
        title={
          record ? (
            <div className="key-drawer-title-identity">
              <span className="key-drawer-title-icon" aria-hidden="true">
                <FullWebIcon name="key" size={22} />
              </span>
              <div className="key-drawer-title-stack">
                <div className="key-drawer-title-row">
                  <h3 className="key-drawer-title-primary" title={displayName}>
                    {displayName}
                  </h3>
                  {!superseded && (
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
                  )}
                  {unifiedStatus && (
                    <span
                      className={'key-state-pill ' + unifiedStatus.pillClass}
                      data-testid="detail-runtime-state"
                    >
                      {k(unifiedStatus.labelKey)}
                    </span>
                  )}
                </div>
                <div className="key-drawer-title-meta">
                  <span className="key-drawer-meta-secret">
                    {!superseded ? (
                      <button
                        type="button"
                        className="key-drawer-meta-secret-copy"
                        data-testid="copy-saved-client-secret"
                        aria-label={k('copyKey')}
                        title={k('copyKey')}
                        disabled={busy || copying || applyPending || applyUnknown}
                        onClick={() => void copySavedSecret()}
                      >
                        <code>{record.maskedPreview}</code>
                        <FullWebIcon
                          name={copyFeedback === 'copied' ? 'check' : 'copy'}
                          size={12}
                        />
                      </button>
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
                  </span>
                  {showOriginInMeta && (
                    <>
                      <span className="key-drawer-meta-sep" aria-hidden="true">
                        ·
                      </span>
                      <span>{originLabel}</span>
                    </>
                  )}
                  {createdTime && (
                    <>
                      <span className="key-drawer-meta-sep" aria-hidden="true">
                        ·
                      </span>
                      <time dateTime={createdTime.iso}>{createdTime.label}</time>
                    </>
                  )}
                </div>
              </div>
            </div>
          ) : (
            <span>{t('keysDetails')}</span>
          )
        }
        footer={
          <div className="key-drawer-actions">
            <div className="key-drawer-footer-left">
              {record && !superseded && (
                <button
                  type="button"
                  className="btn btn-secondary btn-sm"
                  data-testid="detail-copy-secret"
                  disabled={busy || copying || applyPending || applyUnknown}
                  onClick={() => void copySavedSecret()}
                >
                  <FullWebIcon name="copy" size={14} />
                  <span>{k('copyFullKey')}</span>
                </button>
              )}
              <Button
                type="button"
                variant="secondary"
                size="sm"
                aria-label={t('keysReadDetails')}
                disabled={busy || applyPending || applyUnknown}
                onClick={() => {
                  void read(ownerEpoch.current);
                  void readUsage(ownerEpoch.current, activeUsageWindow);
                }}
              >
                <FullWebIcon name="refresh" size={14} />
                {t('keysReadDetails')}
              </Button>
            </div>
            {record && !superseded && onRequestOperation && (
              <div className="key-drawer-footer-right" data-testid="key-detail-operation-entry">
                <button
                  type="button"
                  className="btn btn-secondary btn-sm"
                  data-testid="detail-regenerate-key"
                  disabled={busy || applyPending || applyUnknown || editing || Boolean(operationPending)}
                  onClick={() => onRequestOperation('regenerate')}
                >
                  {k('regenerateKey')}
                </button>
                <button
                  type="button"
                  className="btn btn-sm key-danger"
                  data-testid="detail-revoke-key"
                  disabled={busy || applyPending || applyUnknown || editing || Boolean(operationPending)}
                  onClick={() => onRequestOperation('revoke')}
                >
                  {k('quickRevoke')}
                </button>
              </div>
            )}
          </div>
        }
      >
        <section className="key-detail-workspace key-drawer-body-shell" aria-busy={busy}>
          <div
            className="key-drawer-tabs"
            role="tablist"
            aria-label={t('keysDetails')}
            onKeyDown={(event) => {
              if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
              event.preventDefault();
              const next =
                event.key === 'Home'
                  ? 'overview'
                  : event.key === 'End'
                    ? 'usage'
                    : detailTab === 'overview'
                      ? 'usage'
                      : 'overview';
              switchDetailTab(next);
              event.currentTarget
                .querySelector<HTMLButtonElement>(`[data-testid="key-detail-tab-${next}"]`)
                ?.focus();
            }}
          >
            <button
              type="button"
              role="tab"
              id="key-detail-tab-btn-overview"
              tabIndex={detailTab === 'overview' ? 0 : -1}
              aria-selected={detailTab === 'overview'}
              aria-controls="key-detail-panel-overview"
              className={detailTab === 'overview' ? 'is-active' : ''}
              data-testid="key-detail-tab-overview"
              onClick={() => switchDetailTab('overview')}
            >
              {k('detailTabOverview')}
            </button>
            <button
              type="button"
              role="tab"
              id="key-detail-tab-btn-usage"
              tabIndex={detailTab === 'usage' ? 0 : -1}
              aria-selected={detailTab === 'usage'}
              aria-controls="key-detail-panel-usage"
              className={detailTab === 'usage' ? 'is-active' : ''}
              data-testid="key-detail-tab-usage"
              onClick={() => switchDetailTab('usage')}
            >
              {k('detailTabUsage')}
            </button>
          </div>
          {busy && (
            <p role="status" className="key-notice">
              {t('keysLoading')}
            </p>
          )}
          {message && !editing && (
            <p role="status" className="key-notice key-notice-warning">
              <FullWebIcon name="info" />
              <span>{message}</span>
            </p>
          )}
          {record && (
            <>
              <div
                id="key-detail-panel-overview"
                role="tabpanel"
                aria-labelledby="key-detail-tab-btn-overview"
                className="key-detail-columns key-drawer-tab-panel"
                data-testid="key-detail-panel-overview"
                hidden={detailTab !== 'overview'}
              >
                <section
                  className="key-overview-decision-card"
                  data-overview-tone={
                    revokedOrSuperseded
                      ? 'revoked'
                      : record.runtimeState === 'effective_observed'
                        ? 'active'
                        : 'warning'
                  }
                >
                  <div className="key-overview-kpi-strip" data-testid="key-overview-kpi-strip">
                    <div className="key-overview-kpi-item">
                      <span>{k('overviewCalls24h')}</span>
                      <strong>
                        {usageCountText(overviewUsage?.totals?.calls, overviewCoverageState)}
                      </strong>
                    </div>
                    <div className="key-overview-kpi-item">
                      <span>{k('overviewTokens24h')}</span>
                      <strong>
                        {usageCountText(overviewUsage?.totals?.totalTokens, overviewCoverageState)}
                      </strong>
                    </div>
                    <div className="key-overview-kpi-item">
                      <span>{k('usageLastUsed')}</span>
                      <strong>{overviewLastUsedText}</strong>
                    </div>
                  </div>
                  <dl className="key-detail-meta-grid" data-testid="key-detail-metadata">
                    <div>
                      <dt>{k('created')}</dt>
                      <dd>
                        {createdTime ? (
                          <time dateTime={createdTime.iso}>{createdTime.label}</time>
                        ) : (
                          '—'
                        )}
                      </dd>
                    </div>
                    <div>
                      <dt>{k('originLabel')}</dt>
                      <dd>{k(keyOriginTextKey(record.origin))}</dd>
                    </div>
                    {lastRotatedTime && (
                      <div>
                        <dt>{k('lastRotatedLabel')}</dt>
                        <dd data-testid="detail-last-rotated-at">
                          <time dateTime={lastRotatedTime.iso}>{lastRotatedTime.label}</time>
                        </dd>
                      </div>
                    )}
                    {revokedTime && (
                      <div>
                        <dt>{k('revokedAtLabel')}</dt>
                        <dd>
                          <time dateTime={revokedTime.iso}>{revokedTime.label}</time>
                        </dd>
                      </div>
                    )}
                    <div>
                      <dt>{t('keysIdentity')}</dt>
                      <dd>
                        <div className="key-id-copy-row">
                          <code className="key-id-code" title={id}>
                            {id}
                          </code>
                          <button
                            type="button"
                            className="key-inline-copy-btn"
                            data-testid="detail-copy-key-id"
                            aria-label={`${k('copyKey')}: ${t('keysIdentity')}`}
                            title={t('keysCopy')}
                            onClick={() => void copyKeyId()}
                          >
                            <FullWebIcon
                              name={idCopyFeedback === 'copied' ? 'check' : 'copy'}
                              size={12}
                            />
                          </button>
                        </div>
                      </dd>
                    </div>
                    {record.operationId && (
                      <div className="key-detail-op-meta">
                        <dt>{k('operationIdLabel')}</dt>
                        <dd>
                          <code data-testid="detail-operation-id">{record.operationId}</code>
                        </dd>
                      </div>
                    )}
                  </dl>
                  {record.origin === 'adopted' && (
                    <p className="key-field-help key-adopted-note">{k('adoptedOriginNote')}</p>
                  )}
                </section>
                {revokedOrSuperseded ? (
                  <div className="key-drawer-status-notice is-revoked" role="status">
                    <strong>{t('keysRevokedHistory')}</strong>
                    <p>{k('detailRevokedHelp')}</p>
                  </div>
                ) : gatewayPending && record.runtimeState === 'not_applied' ? (
                  <div className="key-drawer-status-notice is-warning" role="status">
                    <p>{k('connectionPending')}</p>
                  </div>
                ) : record.runtimeState === 'effective_observed' ? null : record.runtimeState ===
                    'effect_unknown' || applyUnknown ? (
                  <div
                    className="key-drawer-status-notice is-warning"
                    role="alert"
                    data-testid="detail-apply-recovery"
                  >
                    <FullWebIcon name="alert" size={16} />
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
                  </div>
                ) : (
                  <div
                    className="key-drawer-status-notice is-warning"
                    role="alert"
                    data-testid="detail-apply-recovery"
                  >
                    <FullWebIcon name="alert" size={16} />
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
                  </div>
                )}
                {!revokedOrSuperseded && (
                  <AccessInfoSummary
                    client={client}
                    auth={auth}
                    onUnauthorized={onUnauthorized}
                    info={accessInfo ?? undefined}
                    reloadToken={reloadToken}
                    compact
                    clientOnly
                    defaultProtocolsCollapsed={false}
                    testIdPrefix="detail-access-info"
                  />
                )}
              </div>
              <div
                id="key-detail-panel-usage"
                role="tabpanel"
                aria-labelledby="key-detail-tab-btn-usage"
                className="key-detail-columns key-drawer-tab-panel"
                data-testid="key-detail-panel-usage"
                hidden={detailTab !== 'usage'}
              >
                <section className="key-detail-section" data-testid="key-detail-usage-policy">
                  <header className="key-usage-toolbar-row">
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
                  </header>
                  <div className="key-detail-section-body">
                    {!usage && !usageFailed ? (
                      <p className="key-detail-description" role="status">
                        {k('usageReading')}
                      </p>
                    ) : (
                      <>
                        {usageCoverage.state !== 'complete' && (
                          <div className="key-usage-coverage" data-testid="key-usage-state">
                            <div>
                              <span className="key-field-label">{k('usageCoverageLabel')}</span>
                              <span className={'key-state-pill ' + usageStateClass}>
                                {usageStateText(usageCoverage.state)}
                              </span>
                            </div>
                            <p data-testid="key-usage-reason">
                              {usageReasonText(usageCoverage.reason)}
                            </p>
                          </div>
                        )}
                        {usage && usage.totals && (
                          <>
                            <p className="key-usage-window-meta">
                              <strong>{k('usageWindowLabel')}:</strong>{' '}
                              {detailTime(usage.fromMs).label} — {detailTime(usage.toMs).label}
                            </p>
                            <dl
                              className="key-usage-stat-grid key-usage-totals"
                              data-testid="key-usage-totals"
                            >
                              <div>
                                <dt>{k('usageCalls')}</dt>
                                <dd>{usageCountText(usage.totals.calls, usage.coverage.state)}</dd>
                              </div>
                              <div>
                                <dt>{k('usageSuccess')}</dt>
                                <dd>
                                  {usageCountText(usage.totals.success, usage.coverage.state)}
                                </dd>
                              </div>
                              <div>
                                <dt>{k('usageFailure')}</dt>
                                <dd>
                                  {usageCountText(usage.totals.failure, usage.coverage.state)}
                                </dd>
                              </div>
                              <div>
                                <dt>{k('usageTotalTokens')}</dt>
                                <dd>
                                  {usageCountText(usage.totals.totalTokens, usage.coverage.state)}
                                </dd>
                              </div>
                              <div>
                                <dt>{k('usageInputTokens')}</dt>
                                <dd>
                                  {usageCountText(usage.totals.inputTokens, usage.coverage.state)}
                                </dd>
                              </div>
                              <div>
                                <dt>{k('usageOutputTokens')}</dt>
                                <dd>
                                  {usageCountText(usage.totals.outputTokens, usage.coverage.state)}
                                </dd>
                              </div>
                              <div>
                                <dt>{k('usageLastUsed')}</dt>
                                <dd>{usageTimeText(usage.totals.lastUsedAtMs) ?? '—'}</dd>
                              </div>
                            </dl>
                            {usage.series.length > 0 && (maxBucketCalls > 0 || isZeroSeries) ? (
                              <div className="key-usage-series" data-testid="key-usage-series">
                                <div className="key-usage-series-head">
                                  <span className="key-field-label">
                                    {k('usageSeriesTitle')}
                                    {isZeroSeries ? ` · ${k('usageNoCalls')}` : ''}
                                  </span>
                                  <span className="key-usage-legend">
                                    <span className="key-usage-legend-item is-success">
                                      {k('usageSuccess')}
                                    </span>
                                    <span className="key-usage-legend-item is-failure">
                                      {k('usageFailure')}
                                    </span>
                                  </span>
                                </div>
                                <div
                                  className="key-usage-bars"
                                  data-testid="key-usage-bars"
                                  role="list"
                                  aria-label={k('usageSeriesTitle')}
                                >
                                  {usage.series.map((point) => {
                                    const totalPct =
                                      maxBucketCalls > 0
                                        ? Math.max(
                                            6,
                                            Math.round((point.calls / maxBucketCalls) * 100)
                                          )
                                        : 0;
                                    const failureCalls = Math.min(point.failure, point.calls);
                                    const successCalls = Math.max(0, point.calls - failureCalls);
                                    const failurePct =
                                      point.calls > 0
                                        ? Math.round((failureCalls / point.calls) * 100)
                                        : 0;
                                    const successPct =
                                      point.calls > 0 ? Math.max(0, 100 - failurePct) : 0;
                                    const timeLabel = detailTime(point.bucketMs).label;
                                    const tooltip = `${timeLabel} · ${usageCountText(point.calls, usage.coverage.state)} ${k('usageCalls')}${failureCalls > 0 ? ` (${usageCountText(failureCalls, usage.coverage.state)} ${k('usageFailure')})` : ''} · ${usageCountText(point.totalTokens, usage.coverage.state)} ${k('usageTotalTokens')}`;
                                    return (
                                      <div
                                        key={point.bucketMs}
                                        className="key-usage-bar-col"
                                        role="listitem"
                                        tabIndex={0}
                                        title={tooltip}
                                        aria-label={tooltip}
                                      >
                                        {point.calls > 0 ? (
                                          <div
                                            className="key-usage-bar-stack"
                                            style={{ height: `${totalPct}%` }}
                                          >
                                            {failureCalls > 0 && (
                                              <span
                                                className="key-usage-bar-failure"
                                                style={{ height: `${failurePct}%` }}
                                              />
                                            )}
                                            {successCalls > 0 && (
                                              <span
                                                className="key-usage-bar-success"
                                                style={{ height: `${successPct}%` }}
                                              />
                                            )}
                                          </div>
                                        ) : (
                                          <div className="key-usage-bar-empty" />
                                        )}
                                      </div>
                                    );
                                  })}
                                </div>
                              </div>
                            ) : usage.coverage.state === 'complete' ||
                              usage.coverage.state === 'partial' ? (
                              <p
                                className="key-usage-empty-state"
                                data-testid="key-usage-empty"
                              >
                                {k('usageNoCalls')}
                              </p>
                            ) : null}
                          </>
                        )}
                      </>
                    )}
                  </div>
                </section>
              </div>
            </>
          )}
        </section>
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
