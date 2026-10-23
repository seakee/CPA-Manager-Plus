import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { configurationText as c } from '../i18n/configurationWorkspace';
import { referenceText as r } from '../i18n/configurationReference';
import { workspaceText } from '../i18n/consoleWorkspace';
import { PublicApiError, type createPublicApiClient } from './publicApi';

type RetryTextKey =
  | 'retryPresenceMissing'
  | 'retryPresenceNull'
  | 'retryPresencePresent'
  | 'retryPresenceUnknown'
  | 'retryLoading'
  | 'retryReadUnavailable'
  | 'retryReadConflict'
  | 'retrySaveRejected'
  | 'retrySaveUnknown'
  | 'retrySaved'
  | 'retrySavedReadUnknown'
  | 'retryReconciled'
  | 'retryRecovered'
  | 'retryOperationUnknown'
  | 'retryOperationNotFound'
  | 'retryConflict'
  | 'retrySourceUnavailable'
  | 'retryWriterNotIsolated'
  | 'retryJournalInvalid'
  | 'retryNeedsReconciliation'
  | 'retryPermission'
  | 'retryValueRequired'
  | 'retryValueInvalid'
  | 'retryRequestIdUnavailable'
  | 'retryIntervalTitle'
  | 'retryIntervalHelp'
  | 'retryIntervalEditLabel'
  | 'retryIntervalEditHint'
  | 'retryIntervalValueRequired'
  | 'retryIntervalValueInvalid';

type RetryPresence = 'missing' | 'null' | 'present' | 'unknown';
type RetryOperationState = 'unknown' | 'persisted' | 'recovered';

interface RetryExpected {
  sourceId: string;
  generation: string;
  revision: number;
  sha256: string;
}

interface RetryOperation {
  requestId: string;
  field?: 'max-retry-interval';
  value: number;
  sourceId: string;
  generation: string;
  revision: number;
  sha256: string;
  state: RetryOperationState;
}

interface RetryView {
  field: 'routing.retry.request-retry' | 'routing.retry.max-retry-interval';
  presence: RetryPresence;
  value: number | null;
  source: string;
  authority: string;
  editable: boolean;
  reason: string;
  persisted: string;
  effective: 'unknown';
  concurrentProtection: string;
  expected?: RetryExpected;
  operation?: RetryOperation;
}

interface RetryOperationResponse {
  operation: RetryOperation;
  effective: 'unknown';
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value);
}

function isHex(value: unknown, length: number): value is string {
  return typeof value === 'string' && new RegExp(`^[a-f0-9]{${length}}$`).test(value);
}

function isRetryValue(value: unknown, variant: 'count' | 'interval' = 'count'): value is number {
  return (
    typeof value === 'number' &&
    Number.isSafeInteger(value) &&
    value >= 0 &&
    value <= (variant === 'interval' ? 300 : 10)
  );
}

function isRevision(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value > 0;
}

function isOperationState(value: unknown): value is RetryOperationState {
  return value === 'unknown' || value === 'persisted' || value === 'recovered';
}

function parseExpected(value: unknown): RetryExpected {
  if (
    !isRecord(value) ||
    typeof value.sourceId !== 'string' ||
    value.sourceId.length === 0 ||
    value.sourceId.length > 256 ||
    typeof value.generation !== 'string' ||
    value.generation.length === 0 ||
    value.generation.length > 64 ||
    !isRevision(value.revision) ||
    !isHex(value.sha256, 64)
  )
    throw new Error('Invalid retry source expectation');
  return {
    sourceId: value.sourceId,
    generation: value.generation,
    revision: value.revision,
    sha256: value.sha256,
  };
}

function parseOperation(value: unknown, variant: 'count' | 'interval' = 'count'): RetryOperation {
  if (
    !isRecord(value) ||
    !isHex(value.requestId, 32) ||
    !isRetryValue(value.value, variant) ||
    (variant === 'interval' ? value.field !== 'max-retry-interval' : value.field !== undefined) ||
    typeof value.sourceId !== 'string' ||
    value.sourceId.length === 0 ||
    value.sourceId.length > 256 ||
    typeof value.generation !== 'string' ||
    value.generation.length === 0 ||
    value.generation.length > 64 ||
    !isRevision(value.revision) ||
    !isHex(value.sha256, 64) ||
    !isOperationState(value.state)
  )
    throw new Error('Invalid retry operation');
  return {
    requestId: value.requestId,
    ...(variant === 'interval' ? { field: 'max-retry-interval' as const } : {}),
    value: value.value,
    sourceId: value.sourceId,
    generation: value.generation,
    revision: value.revision,
    sha256: value.sha256,
    state: value.state,
  };
}

function parseRetryView(value: unknown, variant: 'count' | 'interval' = 'count'): RetryView {
  if (!isRecord(value)) throw new Error('Invalid retry view');
  const presence = value.presence;
  if (
    presence !== 'missing' &&
    presence !== 'null' &&
    presence !== 'present' &&
    presence !== 'unknown'
  )
    throw new Error('Invalid retry presence');
  const parsedValue = presence === 'present' ? value.value : null;
  if (
    (presence === 'present' && !isRetryValue(parsedValue, variant)) ||
    value.field !==
      (variant === 'interval'
        ? 'routing.retry.max-retry-interval'
        : 'routing.retry.request-retry') ||
    typeof value.source !== 'string' ||
    typeof value.authority !== 'string' ||
    typeof value.editable !== 'boolean' ||
    typeof value.reason !== 'string' ||
    typeof value.persisted !== 'string' ||
    value.effective !== 'unknown' ||
    typeof value.concurrentProtection !== 'string'
  )
    throw new Error('Invalid retry view');
  const safeValue: number | null = presence === 'present' ? (parsedValue as number) : null;
  let expected: RetryExpected | undefined;
  if (value.expected !== undefined) expected = parseExpected(value.expected);
  let operation: RetryOperation | undefined;
  if (value.operation !== undefined && value.operation !== null)
    operation = parseOperation(value.operation, variant);
  return {
    field:
      variant === 'interval' ? 'routing.retry.max-retry-interval' : 'routing.retry.request-retry',
    presence,
    value: safeValue,
    source: value.source,
    authority: value.authority,
    editable: value.editable,
    reason: value.reason,
    persisted: value.persisted,
    effective: 'unknown',
    concurrentProtection: value.concurrentProtection,
    ...(expected ? { expected } : {}),
    ...(operation ? { operation } : {}),
  };
}

function parseRetryOperationResponse(
  value: unknown,
  variant: 'count' | 'interval' = 'count'
): RetryOperationResponse {
  if (!isRecord(value) || value.effective !== 'unknown')
    throw new Error('Invalid retry operation result');
  return { operation: parseOperation(value.operation, variant), effective: 'unknown' };
}

function createRequestId(issued: Set<string>): string {
  const bytes = new Uint8Array(16);
  if (!globalThis.crypto?.getRandomValues) throw new Error('Secure random unavailable');
  globalThis.crypto.getRandomValues(bytes);
  const id = Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('');
  if (!isHex(id, 32) || issued.has(id)) throw new Error('Unable to create a unique request ID');
  issued.add(id);
  return id;
}

function parseDraft(value: string, variant: 'count' | 'interval' = 'count'): number | null {
  if (!/^(?:0|[1-9][0-9]*)$/.test(value)) return null;
  const parsed = Number(value);
  return isRetryValue(parsed, variant) ? parsed : null;
}

function lockReasonKey(reason: string, code?: string): RetryTextKey {
  switch (code || reason) {
    case 'private_source_unavailable':
    case 'private_source_not_authorized':
      return 'retrySourceUnavailable';
    case 'config_key_writer_not_isolated':
      return 'retryWriterNotIsolated';
    case 'configuration_journal_invalid':
      return 'retryJournalInvalid';
    case 'original_operation_or_source_requires_reconciliation':
    case 'original_operation_unknown':
      return 'retryNeedsReconciliation';
    case 'source_or_operation_conflict':
      return 'retryConflict';
    default:
      return 'retryPermission';
  }
}

function responseOperation(
  error: unknown,
  variant: 'count' | 'interval',
  expectedId?: string
): RetryOperation | null {
  if (!(error instanceof PublicApiError) || !error.operationResult) return null;
  try {
    const operation = parseRetryOperationResponse(error.operationResult, variant).operation;
    return !expectedId || operation.requestId === expectedId ? operation : null;
  } catch {
    return null;
  }
}

export interface RetryDraftDiff {
  path: string;
  label: string;
  oldVal: string;
  newVal: string;
  dirty: boolean;
  invalid: boolean;
}

function RetryScalarEditor({
  client,
  credential,
  onUnauthorized,
  onPending,
  navigationRequest = 0,
  variant,
  onCommitted,
  refreshEpoch,
  siblingPending,
  discardEpoch = 0,
  onDraftChange,
  visible = true,
  compact = false,
}: {
  client: ReturnType<typeof createPublicApiClient>;
  credential: string;
  onUnauthorized: () => void;
  onPending: (pending: boolean) => void;
  navigationRequest?: number;
  variant: 'count' | 'interval';
  onCommitted: () => void;
  refreshEpoch: number;
  siblingPending: boolean;
  discardEpoch?: number;
  onDraftChange?: (diff: RetryDraftDiff) => void;
  visible?: boolean;
  compact?: boolean;
}) {
  const { i18n, t: translate } = useTranslation();
  const t = (key: string) => translate('fullweb.' + key);
  const w = (key: Parameters<typeof workspaceText>[0]) => workspaceText(key, i18n.resolvedLanguage);
  const consumedDiscardEpoch = useRef(discardEpoch);
  const retryResource =
    variant === 'interval' ? 'gateway/retry/max-retry-interval' : 'gateway/retry/request-retry';
  const retryOperationResource =
    variant === 'interval' ? 'gateway/retry/interval-operations/' : 'gateway/retry/operations/';
  const testPrefix = variant === 'interval' ? 'retry-interval' : 'retry';
  const [view, setView] = useState<RetryView | null>(null);
  const [draft, setDraft] = useState('');
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<RetryTextKey | ''>('');
  const [error, setError] = useState<RetryTextKey | ''>('');
  const [lockCode, setLockCode] = useState('');
  const [operation, setOperation] = useState<RetryOperation | null>(null);
  const [pendingId, setPendingId] = useState('');
  const [operationUnknown, setOperationUnknown] = useState(false);
  const [readUnavailable, setReadUnavailable] = useState(false);
  const alive = useRef(true);
  const flight = useRef(false);
  const pendingIdRef = useRef('');
  const issuedIds = useRef(new Set<string>());
  const dirtyRef = useRef(false);
  const sectionRef = useRef<HTMLElement>(null);
  const titleRef = useRef<HTMLHeadingElement>(null);
  const auth = { kind: 'admin' as const, value: credential };

  function setPendingState(value: boolean) {
    if (alive.current) onPending(value);
  }

  function acceptOperation(next: RetryOperation) {
    if (!alive.current) return;
    setOperation(next);
    if (next.state === 'unknown') {
      pendingIdRef.current = next.requestId;
      setPendingId(next.requestId);
      setOperationUnknown(true);
      setPendingState(true);
      return;
    }
    pendingIdRef.current = '';
    setPendingId('');
    setOperationUnknown(false);
    setPendingState(false);
  }

  function acceptView(next: RetryView) {
    if (!alive.current) return;
    setView(next);
    if (!dirtyRef.current)
      setDraft(next.presence === 'present' && next.value !== null ? String(next.value) : '');
    setReadUnavailable(false);
    setLockCode('');
    const existingId = pendingIdRef.current || pendingId;
    if (next.operation && (!existingId || next.operation.requestId === existingId)) {
      acceptOperation(next.operation);
    } else if (existingId) {
      pendingIdRef.current = existingId;
      setPendingId(existingId);
      setOperationUnknown(true);
      setPendingState(true);
    } else if (!operationUnknown) setPendingState(false);
  }

  function reportReadFailure(err: unknown) {
    if (!alive.current) return;
    if (err instanceof PublicApiError && err.kind === 'authentication') {
      onUnauthorized();
      return;
    }
    const recovered = responseOperation(
      err,
      variant,
      pendingIdRef.current || pendingId || undefined
    );
    if (recovered) acceptOperation(recovered);
    if (err instanceof PublicApiError && err.status === 409) {
      setLockCode(err.code || 'source_or_operation_conflict');
      setError('retryReadConflict');
    } else {
      setError('retryReadUnavailable');
    }
    setReadUnavailable(true);
  }

  async function readView(): Promise<boolean> {
    try {
      const next = parseRetryView(await client.get(retryResource, auth), variant);
      acceptView(next);
      return true;
    } catch (err) {
      reportReadFailure(err);
      return false;
    }
  }

  function beginFlight(): boolean {
    if (flight.current) return false;
    flight.current = true;
    setBusy(true);
    return true;
  }

  async function refresh() {
    if (!beginFlight()) return;
    setMessage('');
    setError('');
    await readView();
    if (alive.current) {
      flight.current = false;
      setBusy(false);
    }
  }

  function keepOriginalUnknown(id: string, text: RetryTextKey) {
    pendingIdRef.current = id;
    setPendingId(id);
    setOperationUnknown(true);
    setPendingState(true);
    setMessage(text);
  }

  async function handleMutationFailure(err: unknown, id: string) {
    if (err instanceof PublicApiError && err.kind === 'authentication') {
      onUnauthorized();
      return;
    }
    if (
      err instanceof PublicApiError &&
      err.status === 400 &&
      err.code === 'invalid_retry_request'
    ) {
      pendingIdRef.current = '';
      setPendingId('');
      setOperationUnknown(false);
      setPendingState(false);
      setMessage('retrySaveRejected');
      return;
    }
    const recovered = responseOperation(err, variant, id);
    if (recovered) {
      acceptOperation(recovered);
      if (recovered.state !== 'unknown') {
        if (
          await (async () => {
            dirtyRef.current = false;
            return readView();
          })()
        ) {
          onCommitted();
          setMessage(recovered.state === 'recovered' ? 'retryRecovered' : 'retryReconciled');
        } else setMessage('retrySavedReadUnknown');
        return;
      }
    } else {
      keepOriginalUnknown(id, 'retrySaveUnknown');
    }
    if (err instanceof PublicApiError && err.status === 409) setMessage('retryConflict');
    else setMessage('retrySaveUnknown');
  }

  async function save() {
    if (
      !view ||
      siblingPending ||
      !view.editable ||
      view.presence === 'unknown' ||
      view.persisted !== 'persisted' ||
      view.reason !== '' ||
      !view.expected ||
      operationUnknown ||
      readUnavailable ||
      draft === '' ||
      parseDraft(draft, variant) === null ||
      (view.presence === 'present' && String(view.value) === draft) ||
      !beginFlight()
    )
      return;
    const value = parseDraft(draft, variant)!;
    let id = pendingIdRef.current;
    if (!id) {
      try {
        id = createRequestId(issuedIds.current);
      } catch {
        setError('retryRequestIdUnavailable');
        flight.current = false;
        setBusy(false);
        return;
      }
      pendingIdRef.current = id;
      setPendingId(id);
    }
    // A new intent must not display the previous operation's terminal result.
    setOperation(null);
    // Protect the original ID before dispatch, including same-turn logout.
    setPendingState(true);
    setMessage('');
    setError('');
    try {
      const result = parseRetryOperationResponse(
        await client.put(retryResource, { requestId: id, value, expected: view.expected }, auth),
        variant
      );
      if (result.operation.requestId !== id) throw new Error('Retry operation identity changed');
      acceptOperation(result.operation);
      if (result.operation.state === 'unknown') {
        setMessage('retryOperationUnknown');
      } else if (
        await (async () => {
          dirtyRef.current = false;
          return readView();
        })()
      ) {
        onCommitted();
        setMessage(result.operation.state === 'recovered' ? 'retryRecovered' : 'retrySaved');
      } else {
        setMessage('retrySavedReadUnknown');
      }
    } catch (err) {
      await handleMutationFailure(err, id);
    } finally {
      if (alive.current) {
        flight.current = false;
        setBusy(false);
      }
    }
  }

  async function inspectOriginal(reconcile: boolean) {
    const id = pendingIdRef.current || pendingId;
    if (!id || !beginFlight()) return;
    setError('');
    setMessage('');
    try {
      const result = parseRetryOperationResponse(
        reconcile
          ? await client.post(retryOperationResource + id + '/reconcile', {}, auth)
          : await client.get(retryOperationResource + id, auth),
        variant
      );
      if (result.operation.requestId !== id) throw new Error('Retry operation identity changed');
      acceptOperation(result.operation);
      if (result.operation.state === 'unknown') {
        setMessage('retryOperationUnknown');
      } else if (
        await (async () => {
          dirtyRef.current = false;
          return readView();
        })()
      ) {
        onCommitted();
        setMessage(result.operation.state === 'recovered' ? 'retryRecovered' : 'retryReconciled');
      } else {
        setMessage('retrySavedReadUnknown');
      }
    } catch (err) {
      if (err instanceof PublicApiError && err.kind === 'authentication') {
        onUnauthorized();
      } else {
        const recovered = responseOperation(err, variant, id);
        if (recovered) acceptOperation(recovered);
        else keepOriginalUnknown(id, reconcile ? 'retrySaveUnknown' : 'retryOperationUnknown');
        setMessage(
          err instanceof PublicApiError && err.status === 404
            ? 'retryOperationNotFound'
            : reconcile
              ? 'retrySaveUnknown'
              : 'retryOperationUnknown'
        );
        setPendingState(true);
      }
    } finally {
      if (alive.current) {
        flight.current = false;
        setBusy(false);
      }
    }
  }

  useEffect(() => {
    alive.current = true;
    setPendingState(false);
    void refresh();
    return () => {
      alive.current = false;
      onPending(false);
    };
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (refreshEpoch > 0 && !flight.current && !pendingIdRef.current) void refresh();
  }, [refreshEpoch]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (consumedDiscardEpoch.current === discardEpoch) return;
    consumedDiscardEpoch.current = discardEpoch;
    // Discard only local edits, never disturb an in-flight or uncertain operation.
    if (flight.current || pendingIdRef.current) return;
    if (view) {
      dirtyRef.current = false;
      setDraft(view.presence === 'present' && view.value !== null ? String(view.value) : '');
      setMessage('');
    }
  }, [discardEpoch, view]);

  useEffect(() => {
    if (!onDraftChange) return;
    const path =
      variant === 'interval' ? 'routing.retry.max-retry-interval' : 'routing.retry.request-retry';
    const label = r(variant === 'interval' ? 'interval' : 'count');
    const savedStr = view?.presence === 'present' && view.value !== null ? String(view.value) : '';
    const isDirty = dirtyRef.current && draft !== savedStr;
    onDraftChange({
      path,
      label,
      oldVal: savedStr !== '' ? `${savedStr}${variant === 'interval' ? ' s' : ''}` : c('unset'),
      newVal: draft !== '' ? `${draft}${variant === 'interval' ? ' s' : ''}` : c('cleared'),
      dirty: isDirty,
      invalid: isDirty && parseDraft(draft, variant) === null,
    });
  }, [draft, view, onDraftChange, variant, i18n.resolvedLanguage]);

  useEffect(() => {
    if (!navigationRequest || variant !== 'count') return;
    titleRef.current?.focus({ preventScroll: true });
    sectionRef.current?.scrollIntoView({ block: 'start' });
  }, [navigationRequest, variant]);

  const currentValue =
    view?.presence === 'present' && view.value !== null
      ? String(view.value) + (variant === 'interval' ? ' s' : '')
      : view?.presence === 'missing'
        ? t('retryPresenceMissing')
        : view?.presence === 'null'
          ? t('retryPresenceNull')
          : t('retryPresenceUnknown');
  const canEdit =
    !!view?.editable &&
    !siblingPending &&
    view.presence !== 'unknown' &&
    view.persisted === 'persisted' &&
    view.reason === '' &&
    !!view.expected &&
    !operationUnknown &&
    !readUnavailable;
  const inputError =
    draft === ''
      ? variant === 'interval'
        ? 'retryIntervalValueRequired'
        : 'retryValueRequired'
      : parseDraft(draft, variant) === null
        ? variant === 'interval'
          ? 'retryIntervalValueInvalid'
          : 'retryValueInvalid'
        : '';
  const lockReason = view ? lockReasonKey(view.reason, lockCode) : '';
  const Heading = compact ? 'h3' : 'h2';
  const fieldPath =
    variant === 'interval' ? 'routing.retry.max-retry-interval' : 'routing.retry.request-retry';
  const errorId = `${testPrefix}-input-error`;
  const hintId = `${testPrefix}-input-hint`;

  return (
    <section
      ref={sectionRef}
      id={variant === 'interval' ? 'gateway-retry-interval' : 'gateway-retry'}
      className={'console-keys retry-editor' + (compact ? ' retry-editor-compact' : '')}
      hidden={!visible}
      aria-labelledby={
        variant === 'interval' ? 'gateway-retry-interval-title' : 'gateway-retry-title'
      }
      aria-busy={busy}
      data-testid={`${testPrefix}-editor`}
    >
      <div className="console-keys-heading">
        <div>
          {!compact && <p className="console-section-kicker">{w('connection')}</p>}
          <Heading
            id={variant === 'interval' ? 'gateway-retry-interval-title' : 'gateway-retry-title'}
            ref={titleRef}
            tabIndex={-1}
          >
            {compact
              ? r(variant === 'interval' ? 'interval' : 'count')
              : t(variant === 'interval' ? 'retryIntervalTitle' : 'retryTitle')}
          </Heading>
        </div>
        <span className={`badge ${canEdit ? 'badge-emerald' : 'badge-amber'}`}>
          {busy ? t('retryBusy') : canEdit ? t('retryEditable') : t('retryReadonly')}
        </span>
      </div>
      <p className={compact ? 'retry-field-help' : undefined}>
        {compact
          ? c(variant === 'interval' ? 'retryIntervalMeaning' : 'retryCountMeaning')
          : w('connectionHelp')}
      </p>
      {compact && <code className="retry-field-path">{fieldPath}</code>}
      {message && (
        <p className="alert alert-info" role="status">
          {t(message)}
        </p>
      )}
      {error && (
        <p className="alert alert-warn" role="alert">
          {t(error)}
        </p>
      )}
      {readUnavailable && lockCode && (
        <p className="alert alert-warn" data-testid={`${testPrefix}-read-lock-reason`}>
          {t(lockReasonKey('', lockCode))}
        </p>
      )}
      {view ? (
        <>
          {compact && (
            <small className="retry-field-source">
              {t('retrySource')}: {view.source}
            </small>
          )}
          <div className="retry-current-value">
            <span>{t('retryCurrentValue')}</span>
            <strong>{currentValue}</strong>
            <span>
              {view.persisted === 'persisted'
                ? t('retryPersistedConfirmed')
                : view.persisted === 'recovered'
                  ? t('retryPersistedRecovered')
                  : t('retryStatusUnknown')}
            </span>
          </div>
          {compact && <small className="retry-field-effect">{t('retryEffectiveUnknown')}</small>}
          <details className="retry-diagnostics">
            <summary>{w('diagnostic')}</summary>
            <div className="info-card retry-facts">
              <div className="info-row">
                <span className="info-key">{t('retryField')}</span>
                <code className="info-val">{view.field}</code>
              </div>
              <div className="info-row">
                <span className="info-key">{t('retryCurrentValue')}</span>
                <span className="info-val">{currentValue}</span>
              </div>
              <div className="info-row">
                <span className="info-key">{t('retryPresence')}</span>
                <span className="info-val">
                  {t(
                    `retryPresence${view.presence[0].toUpperCase()}${view.presence.slice(1)}` as RetryTextKey
                  )}
                </span>
              </div>
              <div className="info-row">
                <span className="info-key">{t('retrySource')}</span>
                <span className="info-val">{view.source}</span>
              </div>
              <div className="info-row">
                <span className="info-key">{t('retryAuthority')}</span>
                <span className="info-val">{view.authority}</span>
              </div>
              <div className="info-row">
                <span className="info-key">{t('retryPermission')}</span>
                <span className="info-val">
                  {view.editable ? t('retryPermissionEditable') : t('retryPermissionReadonly')}
                </span>
              </div>
              <div className="info-row">
                <span className="info-key">{t('retryPersisted')}</span>
                <span className="info-val">
                  {view.persisted === 'persisted'
                    ? t('retryPersistedConfirmed')
                    : view.persisted === 'recovered'
                      ? t('retryPersistedRecovered')
                      : t('retryStatusUnknown')}
                </span>
              </div>
              <div className="info-row">
                <span className="info-key">{t('retryEffective')}</span>
                <span className="info-val">{t('retryEffectiveUnknown')}</span>
              </div>
              <div className="info-row">
                <span className="info-key">{t('retryProtection')}</span>
                <span className="info-val">{view.concurrentProtection}</span>
              </div>
            </div>
          </details>
          {!canEdit && !readUnavailable && !operationUnknown && (
            <p className="alert alert-warn">{t(lockReason || 'retryPermission')}</p>
          )}
          {view.operation?.state === 'unknown' && (
            <p className="alert alert-warn">{t('retryOperationUnknown')}</p>
          )}
          <div className="form-group">
            <div className="form-label">
              <label
                htmlFor={
                  variant === 'interval' ? 'gateway-retry-interval-value' : 'gateway-retry-value'
                }
              >
                {t(variant === 'interval' ? 'retryIntervalEditLabel' : 'retryEditLabel')}
              </label>
              <span className="form-hint" id={hintId}>
                {t(variant === 'interval' ? 'retryIntervalEditHint' : 'retryEditHint')}
              </span>
            </div>
            <div className="input-wrap">
              <input
                id={variant === 'interval' ? 'gateway-retry-interval-value' : 'gateway-retry-value'}
                data-testid={`${testPrefix}-value`}
                className="form-input"
                type="text"
                inputMode="numeric"
                value={draft}
                disabled={busy || !canEdit}
                aria-invalid={canEdit && !!inputError}
                aria-describedby={hintId + (canEdit && inputError ? ' ' + errorId : '')}
                onChange={(event) => {
                  dirtyRef.current = true;
                  setDraft(event.target.value);
                  setMessage('');
                }}
              />
            </div>
            {canEdit && inputError && (
              <span
                id={errorId}
                className="form-hint retry-input-error"
                role="status"
                data-testid={`${testPrefix}-input-error`}
              >
                {t(inputError)}
              </span>
            )}
          </div>
          <div className="wizard-card-footer">
            <button
              className="btn btn-primary"
              data-testid={`${testPrefix}-save`}
              disabled={
                busy ||
                !canEdit ||
                !!inputError ||
                (view.presence === 'present' && String(view.value) === draft)
              }
              onClick={() => void save()}
            >
              {busy ? t('retrySaving') : t('retrySave')}
            </button>
            <button
              className="btn btn-secondary"
              data-testid={`${testPrefix}-refresh`}
              disabled={busy}
              onClick={() => void refresh()}
            >
              {t('retryRefresh')}
            </button>
          </div>
        </>
      ) : (
        !error && <p role="status">{t('retryLoading')}</p>
      )}
      {(operation || (operationUnknown && pendingId)) && (
        <div className="console-key-result retry-operation" data-testid={`${testPrefix}-operation`}>
          <h3>{t('retryOperationTitle')}</h3>
          <p>
            {t('retryOperationId')}:{' '}
            <code data-testid={`${testPrefix}-operation-id`}>
              {pendingId || operation?.requestId}
            </code>
          </p>
          {operation ? (
            <p>
              {t('retryOperationState')}:{' '}
              {operationUnknown || operation.state === 'unknown'
                ? t('retryOperationUnknown')
                : operation.state === 'recovered'
                  ? t('retryOperationRecovered')
                  : t('retryOperationPersisted')}
            </p>
          ) : (
            <p>{t('retryOperationUnknown')}</p>
          )}
          {operationUnknown && <p className="alert alert-warn">{t('retryOperationNoPutReplay')}</p>}
          {operationUnknown && (
            <div className="wizard-card-footer">
              <button
                className="btn btn-secondary"
                data-testid={`${testPrefix}-check-operation`}
                disabled={busy}
                onClick={() => void inspectOriginal(false)}
              >
                {t('retryCheckOperation')}
              </button>
              <button
                className="btn btn-primary"
                data-testid={`${testPrefix}-reconcile`}
                disabled={busy}
                onClick={() => void inspectOriginal(true)}
              >
                {t('retryReconcile')}
              </button>
            </div>
          )}
        </div>
      )}
    </section>
  );
}

export function FreshRetryConfiguration(props: {
  client: ReturnType<typeof createPublicApiClient>;
  credential: string;
  onUnauthorized: () => void;
  onPending: (pending: boolean) => void;
  navigationRequest?: number;
  discardEpoch?: number;
  onDraftChange?: (diffs: RetryDraftDiff[]) => void;
  countVisible?: boolean;
  intervalVisible?: boolean;
  compact?: boolean;
}) {
  const { onDraftChange, onPending } = props;
  const pending = useRef({ count: false, interval: false });
  const [busyOwners, setBusyOwners] = useState({ count: false, interval: false });
  const [countRefreshEpoch, setCountRefreshEpoch] = useState(0);
  const [intervalRefreshEpoch, setIntervalRefreshEpoch] = useState(0);
  const countDiffRef = useRef<RetryDraftDiff | null>(null);
  const intervalDiffRef = useRef<RetryDraftDiff | null>(null);

  const reportDiffs = useCallback(() => {
    if (!onDraftChange) return;
    const diffs: RetryDraftDiff[] = [];
    if (countDiffRef.current) diffs.push(countDiffRef.current);
    if (intervalDiffRef.current) diffs.push(intervalDiffRef.current);
    onDraftChange(diffs);
  }, [onDraftChange]);

  const handleCountDraftChange = useCallback(
    (diff: RetryDraftDiff) => {
      countDiffRef.current = diff;
      reportDiffs();
    },
    [reportDiffs]
  );

  const handleIntervalDraftChange = useCallback(
    (diff: RetryDraftDiff) => {
      intervalDiffRef.current = diff;
      reportDiffs();
    },
    [reportDiffs]
  );

  const setPending = (which: 'count' | 'interval', value: boolean) => {
    if (pending.current[which] === value) return;
    pending.current[which] = value;
    setBusyOwners({ ...pending.current });
    onPending(pending.current.count || pending.current.interval);
  };
  return (
    <>
      <RetryScalarEditor
        {...props}
        variant="count"
        refreshEpoch={countRefreshEpoch}
        siblingPending={busyOwners.interval}
        visible={props.countVisible}
        onCommitted={() => setIntervalRefreshEpoch((n) => n + 1)}
        onPending={(value) => setPending('count', value)}
        discardEpoch={props.discardEpoch}
        onDraftChange={handleCountDraftChange}
      />
      <RetryScalarEditor
        {...props}
        variant="interval"
        refreshEpoch={intervalRefreshEpoch}
        siblingPending={busyOwners.count}
        visible={props.intervalVisible}
        onCommitted={() => setCountRefreshEpoch((n) => n + 1)}
        onPending={(value) => setPending('interval', value)}
        discardEpoch={props.discardEpoch}
        onDraftChange={handleIntervalDraftChange}
      />
    </>
  );
}
