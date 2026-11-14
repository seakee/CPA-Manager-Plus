import { useEffect, useRef, useState, type FormEvent } from 'react';
import { fullWebLanguage, fullWebText as t } from '../i18n/fullweb';
import { keyWorkspaceText as k } from '../i18n/freshKeyWorkspace';
import { externalWorkspaceText as x } from '../i18n/freshExternal';
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
  isTerminalKeyOperationOutcome,
  keyDisplayName,
  keyLifecyclePillClass,
  keyLifecycleTextKey,
  keyRuntimePillClass,
  keyRuntimeTextKey,
  validKey,
  validKeyOperationResult,
  validSavedSecret,
  type Key,
  type KeyOperationResult,
} from './freshKeyMetadata';
import { Drawer } from '../components/ui/Drawer';
import { Modal } from '../components/ui/Modal';
import { SegmentedTabs } from '../components/ui/SegmentedTabs';

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
  gatewayPending = true,
  accessInfo,
  reloadToken = 0,
}: {
  id: string;
  client: PublicApiClient;
  credential: string;
  onUnauthorized: () => void;
  onPending: (value: boolean) => void;
  onSaved: (key: Key) => void;
  onClose: () => void;
  onRequestOperation?: (mode: 'replace' | 'revoke') => void;
  onRebind?: () => void;
  onBlocker?: (code: string, detail?: string) => void;
  open?: boolean;
  gatewayPending?: boolean;
  accessInfo?: AccessInfo | null;
  reloadToken?: number;
}) {
  const [record, setRecord] = useState<Key | null>(null);
  const [usage, setUsage] = useState<KeyUsage | null>(null);
  const [usageFailed, setUsageFailed] = useState(false);
  const [name, setName] = useState('');
  const [busy, setBusy] = useState(false);
  const [unknown, setUnknown] = useState(false);
  const [applyPending, setApplyPending] = useState(false);
  const [applyUnknown, setApplyUnknown] = useState(false);
  const [applyError, setApplyError] = useState('');
  const [applyErrorCode, setApplyErrorCode] = useState('');
  const [applyErrorDetail, setApplyErrorDetail] = useState('');
  const [applyAborted, setApplyAborted] = useState(false);
  const applyPendingRef = useRef(false);
  const pendingReapplyOpId = useRef('');
  const [rejectedDraft, setRejectedDraft] = useState(false);
  const [message, setMessage] = useState('');
  const [editing, setEditing] = useState(false);
  // The full value is read on demand for the clipboard only. It is never kept in
  // component state, browser storage or the URL.
  const [copying, setCopying] = useState(false);
  const [copyFeedback, setCopyFeedback] = useState<'copied' | 'failed' | ''>('');
  const copyingRef = useRef(false);
  const inFlight = useRef(false);
  const active = useRef(true);
  const ownerEpoch = useRef(0);
  const usageSeq = useRef(0);
  const input = useRef<HTMLInputElement>(null);
  const auth = { kind: 'admin' as const, value: credential };
  const resource = 'api-keys/' + id;
  const dirty = rejectedDraft || (record !== null && name.trim() !== record.name);
  const usageCoverage = usage?.coverage ?? { state: 'unknown' as UsageState, reason: 'usage_read_failed' };
  const usageStateClass = usageCoveragePill(usageCoverage.state);

  // Each mounted record/session owns its replies; ownerEpoch invalidates any
  // in-flight requests from an earlier credential, key ID, or rebind reload.
  useEffect(() => {
    active.current = true;
    const ownerAtMount = ++ownerEpoch.current;
    inFlight.current = false;
    applyPendingRef.current = false;
    copyingRef.current = false;
    setRecord(null);
    setUsage(null);
    setUsageFailed(false);
    setUnknown(false);
    setApplyPending(false);
    setApplyUnknown(false);
    setApplyError('');
    setApplyErrorCode('');
    setApplyErrorDetail('');
    setApplyAborted(false);
    pendingReapplyOpId.current = '';
    setRejectedDraft(false);
    setMessage('');
    setCopyFeedback('');
    const previous = typeof document !== 'undefined' ? document.activeElement : null;
    void read(ownerAtMount, false);
    void readUsage(ownerAtMount);
    return () => {
      active.current = false;
      if (
        typeof HTMLElement !== 'undefined' &&
        previous instanceof HTMLElement &&
        previous.isConnected
      )
        previous.focus();
    };
  }, [id, credential, reloadToken]); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    if (editing && record) input.current?.focus();
  }, [editing, record]);
  function validate(value: Key) {
    if (!validKey(value) || value.id !== id) throw new Error();
    return value;
  }
  function failure(err: unknown) {
    if (err instanceof PublicApiError && err.kind === 'authentication') onUnauthorized();
    else setMessage(t('keysUnavailable'));
  }
  async function read(ownerAtStart = ownerEpoch.current, checkDraft = true) {
    if (inFlight.current || applyPendingRef.current) return;
    if (checkDraft && dirty && !unknown && !window.confirm(t('keysDiscardName'))) return;
    inFlight.current = true;
    setBusy(true);
    setMessage('');
    try {
      const value = validate(await client.get<Key>(resource, auth));
      if (!active.current || ownerEpoch.current !== ownerAtStart) return;
      setRecord(value);
      setName(value.name);
      setRejectedDraft(false);
      onSaved(value);
      if (unknown) setMessage(t('keysNameChecked'));
      setUnknown(false);
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

  // One authenticated read, one clipboard write. The response is not cached and
  // no part of the secret survives this call. Copying exists for every origin,
  // including the keys an adopted CPA already carried, so the check validates the
  // returned secret against this record's own masked preview instead of assuming
  // one of the two canonical shapes.
  async function copySavedSecret() {
    if (copyingRef.current || !record || record.lifecycle === 'superseded') return;
    const ownerAtStart = ownerEpoch.current;
    copyingRef.current = true;
    setCopying(true);
    setCopyFeedback('');
    try {
      const value = await client.post<{ secret: string }>(resource + '/secret', {}, auth);
      if (!active.current || ownerEpoch.current !== ownerAtStart) return;
      if (!value || !validSavedSecret(record, value.secret)) throw new Error();
      await navigator.clipboard.writeText(value.secret);
      if (active.current && ownerEpoch.current === ownerAtStart) setCopyFeedback('copied');
    } catch (err) {
      if (!active.current || ownerEpoch.current !== ownerAtStart) return;
      if (err instanceof PublicApiError && err.kind === 'authentication') onUnauthorized();
      else {
        setCopyFeedback('failed');
        if (err instanceof PublicApiError && err.status === 409) {
          await read(ownerAtStart, false);
        }
      }
    } finally {
      if (ownerEpoch.current === ownerAtStart) {
        copyingRef.current = false;
        if (active.current) setCopying(false);
      }
    }
  }

  async function readUsage(ownerAtStart = ownerEpoch.current) {
    const seq = ++usageSeq.current;
    try {
      const parsed = parseKeyUsage(await client.get<unknown>(resource + '/usage', auth), id);
      if (!parsed) throw new Error('Invalid usage response');
      if (!active.current || ownerEpoch.current !== ownerAtStart || seq !== usageSeq.current)
        return;
      setUsage(parsed);
      setUsageFailed(false);
    } catch (err) {
      if (!active.current || ownerEpoch.current !== ownerAtStart || seq !== usageSeq.current)
        return;
      if (err instanceof PublicApiError && err.kind === 'authentication') {
        onUnauthorized();
        return;
      }
      setUsage(null);
      setUsageFailed(true);
    }
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
        setName(refreshed.name);
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
        setName(canonical.name);
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
          setApplyError(code ? k(effectBlockerTextKey(code)) : '');
        }
        onPending(false);
        return;
      }

      const unsettled: Key = {
        ...baseKey,
        runtimeState: 'effect_unknown',
        operationId: reapplyId,
      };
      setRecord(unsettled);
      setName(unsettled.name);
      onSaved(unsettled);
      setApplyUnknown(true);
      setApplyError(k('createApplyUnknownHelp'));
      onPending(true);
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
          let resolvedDetail = resolvedCode === 'external_management_key_rejected' ? detail : '';
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
          if (!resolvedCode && refreshed.effectCode && refreshed.effectCode !== 'key_operation_unknown') {
            resolvedCode = refreshed.effectCode;
          }
          pendingReapplyOpId.current = '';
          setRecord(refreshed);
          setName(refreshed.name);
          onSaved(refreshed);
          setApplyUnknown(false);
          setApplyAborted(true);
          setApplyErrorCode(resolvedCode);
          setApplyErrorDetail(resolvedDetail);
          setApplyError(resolvedCode ? k(effectBlockerTextKey(resolvedCode)) : k('operationAborted'));
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
          setName(baseKey.name);
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
          setName(nextRecord.name);
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
          setName(nextRecord.name);
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
      unknown ||
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
      setName(updated.name);
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
          setName(updated.name);
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
    if (inFlight.current || applyPendingRef.current || !record || unknown) return;
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
      setName(nextRecord.name);
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

  async function save(event: FormEvent) {
    event.preventDefault();
    if (
      inFlight.current ||
      applyPendingRef.current ||
      !record ||
      unknown ||
      name.trim() === record.name
    )
      return;
    const ownerAtStart = ownerEpoch.current;
    inFlight.current = true;
    setBusy(true);
    setMessage('');
    onPending(true);
    const submitted = name.trim();
    try {
      const value = validate(
        await client.put<Key>(
          resource,
          { name: submitted, nameRevision: record.nameRevision },
          auth
        )
      );
      if (!active.current || ownerEpoch.current !== ownerAtStart) return;
      if (value.name !== submitted || value.nameRevision !== record.nameRevision + 1)
        throw new Error();
      setRecord(value);
      setName(value.name);
      onSaved(value);
      setMessage(t('keysNameSaved'));
      setEditing(false);
      onPending(false);
    } catch (err) {
      if (!active.current || ownerEpoch.current !== ownerAtStart) return;
      if (err instanceof PublicApiError && err.kind === 'authentication') {
        onUnauthorized();
      } else if (err instanceof PublicApiError && [400, 404, 409].includes(err.status || 0)) {
        // These local responses prove rejection. A fresh read is required
        // before another explicit save; preserve the draft until that check.
        setRejectedDraft(true);
        setRecord(null);
        setMessage(t(err.status === 409 ? 'keysNameConflict' : 'keysNameRejected'));
        onPending(false);
      } else {
        setUnknown(true);
        setMessage(t('keysNameUnknown'));
      }
    } finally {
      if (ownerEpoch.current === ownerAtStart) {
        inFlight.current = false;
        if (active.current) setBusy(false);
      }
    }
  }
  function canClose() {
    if (inFlight.current || applyPendingRef.current || applyUnknown || unknown) return false;
    return !dirty || window.confirm(t('keysDiscardName'));
  }
  function close() {
    if (canClose()) onClose();
  }
  return (
    <>
      <Drawer
        open={open}
        title={t('keysDetails')}
        width={680}
        className="key-detail-drawer"
        onBeforeClose={() => !editing && canClose()}
        onClose={onClose}
        footer={
          <div className="key-drawer-actions">
            {record && record.lifecycle !== 'superseded' && onRequestOperation && (
              <div className="key-row-actions" data-testid="key-detail-operation-entry">
                <button
                  type="button"
                  className="btn btn-ghost key-danger"
                  data-testid="detail-revoke-key"
                  disabled={busy || unknown || applyPending || applyUnknown || dirty || editing}
                  onClick={() => onRequestOperation('revoke')}
                >
                  {k('quickRevoke')}
                </button>
                <button
                  type="button"
                  className="btn btn-secondary"
                  data-testid="detail-replace-key"
                  disabled={busy || unknown || applyPending || applyUnknown || dirty || editing}
                  onClick={() => onRequestOperation('replace')}
                >
                  {k('quickReplace')}
                </button>
              </div>
            )}
            <button
              className="btn btn-secondary"
              aria-label={t('keysCloseDetails')}
              disabled={busy || unknown || applyPending || applyUnknown}
              onClick={close}
            >
              {t('keysCloseDetails')}
            </button>
          </div>
        }
      >
        <section className="key-detail-workspace" aria-busy={busy}>
          <SegmentedTabs
            items={[{ id: 'overview', label: k('overview') }]}
            activeTab="overview"
            ariaLabel={t('keysDetails')}
            idBase="key-detail-tab"
            className="key-detail-tabs"
          />
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
              <div className="key-detail-hero">
                <span className="key-resource-icon">
                  <FullWebIcon name="key" size={24} />
                </span>
                <div>
                  <h3>{keyDisplayName(record, k('installerOrigin'), k('adoptedOrigin'), t('keysUnnamed'))}</h3>
                  <p>
                    <code>{record.maskedPreview}</code>
                  </p>
                </div>
                <div className="key-detail-badges">
                  <span
                    className={'key-state-pill ' + keyLifecyclePillClass(record.lifecycle)}
                    data-testid="detail-lifecycle-state"
                  >
                    {k(keyLifecycleTextKey(record.lifecycle))}
                  </span>
                  <span
                    className={'key-state-pill ' + keyRuntimePillClass(record.runtimeState)}
                    data-testid="detail-runtime-state"
                  >
                    {k(keyRuntimeTextKey(record.runtimeState))}
                  </span>
                </div>
              </div>
              <dl className="key-detail-meta-grid" data-testid="key-detail-metadata">
                <div>
                  <dt>{k('created')}</dt>
                  <dd>
                    <time dateTime={detailTime(record.createdAtMs).iso}>
                      {detailTime(record.createdAtMs).label}
                    </time>
                  </dd>
                </div>
                <div>
                  <dt>{k('revisionLabel')}</dt>
                  <dd>v{record.nameRevision}</dd>
                </div>
                <div>
                  <dt>{k('policyScopeLabel')}</dt>
                  <dd>{k('policyGlobalDefault')}</dd>
                </div>
              </dl>
              <div className="key-detail-columns">
                <section className="key-detail-section">
                  <header>
                    <FullWebIcon name="key" />
                    <h4>{k('basic')}</h4>
                  </header>
                  <div className="key-detail-section-body">
                    <p className="key-detail-description">{k('detailHelp')}</p>
                    <div className="key-readonly-field">
                      <span>{k('credential')}</span>
                      <code>{record.maskedPreview}</code>
                      <button
                        className="btn btn-secondary btn-sm"
                        data-testid="copy-saved-client-secret"
                        disabled={busy || unknown || copying || record.lifecycle === 'superseded'}
                        onClick={() => void copySavedSecret()}
                      >
                        {k('copyKey')}
                      </button>
                    </div>
                    {copyFeedback && (
                      <p
                        role="status"
                        className="key-notice"
                        data-testid="copy-saved-client-secret-feedback"
                      >
                        {k(copyFeedback === 'copied' ? 'keyCopied' : 'keyCopyFailed')}
                      </p>
                    )}
                    <div className="key-readonly-field">
                      <span>{t('keysName')}</span>
                      <strong>{keyDisplayName(record, k('installerOrigin'), k('adoptedOrigin'), t('keysUnnamed'))}</strong>
                      <button
                        className="btn btn-secondary btn-sm"
                        disabled={busy || unknown || applyPending}
                        onClick={() => setEditing(true)}
                      >
                        {k('rename')}
                      </button>
                    </div>
                    <div className="key-private-note">
                      <FullWebIcon name="shield" />
                      <p>{k('privateKeyHelp')}</p>
                    </div>
                  </div>
                </section>
                <section className="key-detail-section">
                  <header>
                    <FullWebIcon name="gateway" />
                    <h4>{k('access')}</h4>
                  </header>
                  <div className="key-detail-section-body">
                    {record.lifecycle === 'superseded' || record.runtimeState === 'revoked_observed' ? (
                      <>
                        <p className="key-detail-description">{t('keysRevokedHistory')}</p>
                        <p className="key-field-help">{k('detailRevokedHelp')}</p>
                      </>
                    ) : (
                      <>
                        {gatewayPending && record.runtimeState === 'not_applied' ? (
                          <p className="key-detail-description">{k('connectionPending')}</p>
                        ) : record.runtimeState === 'effective_observed' ? (
                          <p className="key-detail-description" data-testid="detail-effective-help">
                            {k('detailEffectiveHelp')}
                          </p>
                        ) : record.runtimeState === 'effect_unknown' || applyUnknown ? (
                          <div className="key-notice key-notice-warning" data-testid="detail-apply-recovery">
                            <FullWebIcon name="alert" />
                            <div>
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
                                (applyErrorCode || record.effectCode) === 'external_management_key_rejected' && (
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
                                  disabled={busy || unknown || applyPending || dirty}
                                  onClick={() => void reconcileApply()}
                                >
                                  {k('checkOperationResult')}
                                </button>
                                {onRebind &&
                                  ['external_management_key_rejected', 'external_target_moved'].includes(
                                    applyErrorCode || record.effectCode || ''
                                  ) && (
                                    <button
                                      type="button"
                                      className="btn btn-primary btn-sm"
                                      data-testid="detail-rebind-action"
                                      disabled={busy || unknown || applyPending || dirty}
                                      onClick={onRebind}
                                    >
                                      {k('rebindConnection')}
                                    </button>
                                  )}
                              </div>
                            </div>
                          </div>
                        ) : (
                          <div className="key-notice key-notice-warning" data-testid="detail-apply-recovery">
                            <FullWebIcon name="alert" />
                            <div>
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
                                (applyErrorCode || record.effectCode) === 'external_management_key_rejected' && (
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
                                    disabled={busy || unknown || applyPending || dirty}
                                    onClick={() => void retryApply()}
                                  >
                                    {k('retryApplyKey')}
                                  </button>
                                )}
                                {onRebind &&
                                  ['external_management_key_rejected', 'external_target_moved'].includes(
                                    applyErrorCode || record.effectCode || ''
                                  ) && (
                                    <button
                                      type="button"
                                      className="btn btn-primary btn-sm"
                                      data-testid="detail-rebind-action"
                                      disabled={busy || unknown || applyPending || dirty}
                                      onClick={onRebind}
                                    >
                                      {k('rebindConnection')}
                                    </button>
                                  )}
                              </div>
                            </div>
                          </div>
                        )}
                        {record.operationId && (
                          <p className="key-field-help" data-testid="detail-operation-id">
                            {k('detailPendingOperationNote')}: <code>{record.operationId}</code>
                          </p>
                        )}
                        <AccessInfoSummary
                          client={client}
                          auth={auth}
                          onUnauthorized={onUnauthorized}
                          info={accessInfo ?? undefined}
                          reloadToken={reloadToken}
                          compact
                          testIdPrefix="detail-access-info"
                        />
                      </>
                    )}
                  </div>
                </section>
                <section className="key-detail-section" data-testid="key-detail-usage-policy">
                  <header>
                    <FullWebIcon name="dashboard" />
                    <h4>{k('usageSectionTitle')}</h4>
                  </header>
                  <div className="key-detail-section-body">
                    {!usage && !usageFailed ? (
                      <p className="key-detail-description" role="status">{k('usageReading')}</p>
                    ) : (
                      <>
                        <div className="key-usage-coverage" data-testid="key-usage-state">
                          <div>
                            <span className="key-field-label">{k('usageCoverageLabel')}</span>
                            <span className={'key-state-pill ' + usageStateClass}>{usageStateText(usageCoverage.state)}</span>
                          </div>
                          <p data-testid="key-usage-reason">{usageReasonText(usageCoverage.reason)}</p>
                        </div>
                        {usage && usage.totals && (
                          <>
                            <p className="key-field-help">
                              <strong>{k('usageWindowLabel')}:</strong>{' '}
                              {detailTime(usage.fromMs).label} — {detailTime(usage.toMs).label}
                              <br />{k('usageWindowHelp')}
                            </p>
                            <dl className="key-detail-meta-grid key-usage-totals" data-testid="key-usage-totals">
                              <div><dt>{k('usageCalls')}</dt><dd>{usageCountText(usage.totals.calls, usage.coverage.state)}</dd></div>
                              <div><dt>{k('usageSuccess')}</dt><dd>{usageCountText(usage.totals.success, usage.coverage.state)}</dd></div>
                              <div><dt>{k('usageFailure')}</dt><dd>{usageCountText(usage.totals.failure, usage.coverage.state)}</dd></div>
                              <div><dt>{k('usageTotalTokens')}</dt><dd>{usageCountText(usage.totals.totalTokens, usage.coverage.state)}</dd></div>
                              <div><dt>{k('usageInputTokens')}</dt><dd>{usageCountText(usage.totals.inputTokens, usage.coverage.state)}</dd></div>
                              <div><dt>{k('usageOutputTokens')}</dt><dd>{usageCountText(usage.totals.outputTokens, usage.coverage.state)}</dd></div>
                              <div>
                                <dt>{k('usageLastUsed')}</dt>
                                <dd>{usageTimeText(usage.totals.lastUsedAtMs) ?? '—'}</dd>
                              </div>
                            </dl>
                            {usage.series.length > 0 ? (
                              <div className="key-usage-series" data-testid="key-usage-series">
                                <span className="key-field-label">{k('usageSeriesTitle')}</span>
                                <ul>
                                  {usage.series.map((point) => (
                                    <li key={point.bucketMs}>
                                      <time dateTime={detailTime(point.bucketMs).iso}>{detailTime(point.bucketMs).label}</time>
                                      <span>{usageCountText(point.calls, usage.coverage.state)} {k('usageCalls')}</span>
                                      <span>{usageCountText(point.totalTokens, usage.coverage.state)} {k('usageTotalTokens')}</span>
                                    </li>
                                  ))}
                                </ul>
                              </div>
                            ) : usage.coverage.state === 'complete' ? (
                              <p className="key-detail-description">{k('usageNoCalls')}</p>
                            ) : null}
                          </>
                        )}
                      </>
                    )}
                    <p className="key-field-help">{k('policyGlobalHelp')}</p>
                  </div>
                </section>
              </div>
            </>
          )}
          <footer className="key-detail-footer">
            <details className="key-record-identifiers">
              <summary>{k('diagnostics')}</summary>
              <dl>
                <dt>{t('keysIdentity')}</dt>
                <dd>
                  <code>{id}</code>
                </dd>
              </dl>
            </details>
            <button
              className="btn btn-secondary"
              aria-label={t('keysReadDetails')}
              disabled={busy || applyPending}
              onClick={() => {
                void read();
                void readUsage();
              }}
            >
              <FullWebIcon name="refresh" />
              {t('keysReadDetails')}
            </button>
          </footer>
        </section>
      </Drawer>
      <Modal
        open={open && editing}
        title={k('rename')}
        className="key-rename-modal"
        closeDisabled={busy || unknown || applyPending || applyUnknown || dirty}
        onClose={() => {
          if (!canClose()) return;
          setName(record?.name || '');
          setRejectedDraft(false);
          setEditing(false);
        }}
      >
        {message && (
          <p role="status" className="key-notice key-notice-warning">
            {message}
          </p>
        )}
        {record && (
          <form onSubmit={(event) => void save(event)}>
            <label htmlFor="key-name" className="form-label">
              {t('keysName')}
            </label>
            <input
              ref={input}
              id="key-name"
              className="input"
              value={name}
              maxLength={240}
              autoComplete="off"
              disabled={busy || unknown || applyPending}
              onChange={(event) => setName(event.target.value)}
            />
            <p className="key-field-help">{t('keysNameHelp')}</p>
            <div className="key-modal-actions">
              <button
                type="button"
                className="btn btn-secondary"
                disabled={busy || unknown || applyPending}
                onClick={() => {
                  if (canClose()) {
                    setName(record.name);
                    setEditing(false);
                  }
                }}
              >
                {k('cancel')}
              </button>
              <button
                type="submit"
                className="btn btn-primary"
                disabled={
                  busy ||
                  unknown ||
                  applyPending ||
                  name.trim() === record.name ||
                  [...name.trim()].length > 120
                }
              >
                {t('keysSaveName')}
              </button>
            </div>
          </form>
        )}
        {(unknown || rejectedDraft) && (
          <button
            className="btn btn-secondary"
            disabled={busy || applyPending}
            onClick={() => void read()}
          >
            {t('keysReadDetails')}
          </button>
        )}
        {rejectedDraft && !record && (
          <button
            type="button"
            className="btn btn-secondary"
            onClick={() => {
              if (canClose()) {
                setRejectedDraft(false);
                setName('');
                setEditing(false);
              }
            }}
          >
            {k('cancel')}
          </button>
        )}
      </Modal>
    </>
  );
}
