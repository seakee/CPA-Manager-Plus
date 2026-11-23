import { useEffect, useRef, useState } from 'react';
import { fullWebText as t } from '../i18n/fullweb';
import { keyWorkspaceText as k } from '../i18n/freshKeyWorkspace';
import { externalWorkspaceText as x } from '../i18n/freshExternal';
import { FullWebIcon } from './FullWebIcon';
import { blockerOf } from './externalState';
import { PublicApiError, type PublicApiClient } from './publicApi';
import {
  effectBlockerTextKey,
  isTerminalKeyOperationOutcome,
  keyDisplayName,
  validKey,
  validKeyOperationResult,
  validSavedSecret,
  type Key,
  type KeyOperation,
  type KeyOperationMode,
  type KeyOperationResult,
} from './freshKeyMetadata';
import { Modal } from '../components/ui/Modal';
import { Select } from '../components/ui/Select';

function randomRequestId(): string {
  return Array.from(crypto.getRandomValues(new Uint8Array(16)), (b) =>
    b.toString(16).padStart(2, '0')
  ).join('');
}

function operationModeTitle(mode: KeyOperationMode): string {
  switch (mode) {
    case 'add':
      return k('pendingBannerOpAdd');
    case 'rotate':
      return k('pendingBannerOpRotate');
    case 'replace':
      return k('pendingBannerOpReplace');
    case 'revoke':
    default:
      return k('pendingBannerOpRevoke');
  }
}

export function FreshKeyRevocation({
  keys,
  client,
  credential,
  onUnauthorized,
  onPending,
  onChanged,
  onLatestOperation,
  onBlocker,
  onExternalError,
  disabled,
  mode: displayMode,
  preset,
  open = true,
  visible = true,
  onClose,
  onCreateReplacement,
  onRegenerate,
  onRebind,
  reloadToken = 0,
}: {
  keys: Key[];
  client: PublicApiClient;
  credential: string;
  onUnauthorized: () => void;
  onPending: (value: boolean) => void;
  onChanged: () => void;
  onLatestOperation?: (value: KeyOperationResult | null) => void;
  onBlocker?: (code: string, detail?: string) => void;
  onExternalError?: (code: string, detail?: string) => void;
  disabled: boolean;
  mode?: 'inline' | 'modal';
  preset?: { targetId: string; mode: 'revoke' | 'replace'; seq: number } | null;
  open?: boolean;
  visible?: boolean;
  onClose?: () => void;
  onCreateReplacement?: () => void;
  onRegenerate?: (targetId: string) => void;
  onRebind?: () => void;
  reloadToken?: number;
}) {
  const isPureModal = displayMode === 'modal';
  const legacyModal = !isPureModal && displayMode !== 'inline' && !!onClose;
  const [target, setTarget] = useState('');
  const [keeper, setKeeper] = useState('');
  const [mode, setMode] = useState<'revoke' | 'replace'>('revoke');
  const [result, setResult] = useState<KeyOperationResult | null>(null);
  const [request, setRequest] = useState<{
    id: string;
    target: string;
    keeper: string;
    mode: string;
  } | null>(null);
  const [busy, setBusy] = useState(false);
  const [checked, setChecked] = useState(false);
  // The last preflight verdict, tagged with the selection it answered for, so a
  // stale reply can never unlock or block a different choice.
  const [preflight, setPreflight] = useState<{
    selection: string;
    ready: boolean;
    reason: string;
  } | null>(null);
  const preflightSeq = useRef(0);
  const [message, setMessage] = useState('');
  const [blockerCode, setBlockerCode] = useState('');
  const [blockerDetail, setBlockerDetail] = useState('');
  const [showGuide, setShowGuide] = useState(false);
  const [settledPresetSeq, setSettledPresetSeq] = useState<number | null>(null);
  const [abortedPresetSeq, setAbortedPresetSeq] = useState<number | null>(null);
  const [lastKeyBlocked, setLastKeyBlocked] = useState(false);
  const [modalDone, setModalDone] = useState<'' | 'revoked' | 'rotated' | 'replaced' | 'added'>('');
  const [modalDoneTargetId, setModalDoneTargetId] = useState('');
  const [recoveredTargetKey, setRecoveredTargetKey] = useState<Key | null>(null);
  const [unverifiedModalOp, setUnverifiedModalOp] = useState<KeyOperation | null>(null);
  const [modalSecret, setModalSecret] = useState('');
  const [modalCopyFeedback, setModalCopyFeedback] = useState<'' | 'copied' | 'failed'>('');
  const prevPresetSeq = useRef<number | null>(null);
  const prevModalOpen = useRef(false);
  const prevModalPresetSeq = useRef<number | null>(null);
  const modalRequestIdRef = useRef('');
  const modalSessionEpoch = useRef(0);
  const initialModalMountRef = useRef(true);
  const prevModalCredentialRef = useRef(credential);
  const latestResult = useRef<KeyOperationResult | null>(null);
  const active = useRef(true);
  const flight = useRef(false);
  const ownerEpoch = useRef(0);
  const prevReloadToken = useRef(reloadToken);
  const auth = { kind: 'admin' as const, value: credential };
  const available = keys.filter((keyItem) => keyItem.lifecycle !== 'superseded');
  const effectiveKeys = available.filter((keyItem) => keyItem.runtimeState === 'effective_observed');

  useEffect(() => {
    active.current = true;
    const ownerAtMount = ++ownerEpoch.current;
    latestResult.current = null;
    setResult(null);
    setRequest(null);
    setPreflight(null);
    setMessage('');
    setBlockerCode('');
    setBlockerDetail('');
    setSettledPresetSeq(null);
    setAbortedPresetSeq(null);
    setLastKeyBlocked(false);
    const modalEpochAtMount = ++modalSessionEpoch.current;
    setModalDone('');
    setModalDoneTargetId('');
    setRecoveredTargetKey(null);
    setUnverifiedModalOp(null);
    setModalSecret('');
    setModalCopyFeedback('');
    prevPresetSeq.current = null;
    onLatestOperation?.(null);
    if (!isPureModal || !open) void read(ownerAtMount, true, false, modalEpochAtMount);
    return () => {
      active.current = false;
      prevModalOpen.current = false;
      prevModalPresetSeq.current = null;
      initialModalMountRef.current = true;
      modalSessionEpoch.current += 1;
    };
  }, [credential]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (!isPureModal) return;
    const wasInitialMount = initialModalMountRef.current;
    initialModalMountRef.current = false;
    const credentialChanged = prevModalCredentialRef.current !== credential;
    prevModalCredentialRef.current = credential;
    const prevOpen = prevModalOpen.current;
    const justOpened = open && !prevOpen;
    const justClosed = !open && prevOpen;
    const nextSeq = preset?.seq ?? null;
    const presetChanged = open && nextSeq !== prevModalPresetSeq.current;
    prevModalOpen.current = open;
    prevModalPresetSeq.current = open ? nextSeq : null;
    if (!open) {
      if (justClosed && !credentialChanged) {
        modalSessionEpoch.current += 1;
      }
      flight.current = false;
      setBusy(false);
      modalRequestIdRef.current = '';
      setModalDone('');
      setModalDoneTargetId('');
      setRecoveredTargetKey(null);
      setUnverifiedModalOp(null);
      setModalSecret('');
      setModalCopyFeedback('');
      setLastKeyBlocked(false);
      if (
        !latestResult.current?.operation ||
        isTerminalKeyOperationOutcome(latestResult.current.operation.outcome)
      ) {
        latestResult.current = null;
        setResult(null);
      }
      return;
    }
    if (justOpened || presetChanged || credentialChanged) {
      const modalEpoch = ++modalSessionEpoch.current;
      flight.current = false;
      setBusy(false);
      modalRequestIdRef.current = randomRequestId();
      setModalDone('');
      setModalDoneTargetId('');
      setRecoveredTargetKey(null);
      setUnverifiedModalOp(null);
      setModalSecret('');
      setModalCopyFeedback('');
      setLastKeyBlocked(false);
      setMessage('');
      setBlockerCode('');
      setBlockerDetail('');
      if (!preset) {
        latestResult.current = null;
        setResult(null);
        setRequest(null);
        void read(ownerEpoch.current, true, true, modalEpoch);
      } else {
        if (
          latestResult.current?.operation &&
          isTerminalKeyOperationOutcome(latestResult.current.operation.outcome)
        ) {
          latestResult.current = null;
          setResult(null);
        }
        if (wasInitialMount || credentialChanged) {
          void read(ownerEpoch.current, true, false, modalEpoch);
        }
      }
    }
  }, [isPureModal, open, preset?.seq, preset?.targetId, credential]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (prevReloadToken.current === reloadToken) return;
    prevReloadToken.current = reloadToken;
    setPreflight(null);
    if (!flight.current && !modalDone) {
      setBlockerCode('');
      setBlockerDetail('');
      void read(ownerEpoch.current, false, !preset && isPureModal && open, modalSessionEpoch.current);
    }
  }, [reloadToken]); // eslint-disable-line react-hooks/exhaustive-deps

  const committedAddOnly =
    result?.operation?.mode === 'add' && result.operation.outcome === 'committed' && !request;
  const unresolved =
    !!request ||
    (!!result?.operation && !isTerminalKeyOperationOutcome(result.operation.outcome));
  const presetSettled =
    !!preset &&
    !unresolved &&
    !committedAddOnly &&
    settledPresetSeq === preset.seq &&
    result?.operation?.outcome === 'committed';
  useEffect(() => {
    if (
      !preset ||
      request ||
      (result?.operation && !isTerminalKeyOperationOutcome(result.operation.outcome))
    )
      return;
    if (settledPresetSeq === preset.seq || abortedPresetSeq === preset.seq) return;
    const isNewPreset = prevPresetSeq.current !== preset.seq;
    prevPresetSeq.current = preset.seq;
    setMessage('');
    if (isNewPreset) {
      setBlockerCode('');
      setBlockerDetail('');
    }
    if (available.some((item) => item.id === preset.targetId)) {
      setTarget(preset.targetId);
      setMode(preset.mode);
      if (isNewPreset) setShowGuide(preset.mode === 'replace');
      const fallbackKeeper =
        available.find(
          (item) => item.id !== preset.targetId && item.runtimeState === 'effective_observed'
        )?.id ||
        available.find((item) => item.id !== preset.targetId)?.id ||
        '';
      setKeeper((previous) =>
        previous && previous !== preset.targetId && available.some((item) => item.id === previous)
          ? previous
          : fallbackKeeper
      );
    } else {
      setTarget('');
    }
  }, [preset, keys, settledPresetSeq, abortedPresetSeq]); // eslint-disable-line react-hooks/exhaustive-deps

  const validTarget = !!target && available.some((item) => item.id === target);
  const validKeeper =
    mode === 'revoke'
      ? keeper === '' || (keeper !== target && available.some((item) => item.id === keeper))
      : !!keeper && keeper !== target && available.some((item) => item.id === keeper);
  const currentSelection =
    validTarget &&
    validKeeper &&
    available.length > 1 &&
    !presetSettled &&
    !(result?.operation && !isTerminalKeyOperationOutcome(result.operation.outcome))
      ? `${target}|${keeper}|${mode}`
      : '';

  // Every remaining limit is a fact about the destination or about the
  // installation, so it is asked of the server for the exact selection on screen.
  // The answer is what makes the submit meaningful: a limit is named here instead
  // of surfacing as a conflict after the click.
  useEffect(() => {
    if (isPureModal || !open || !currentSelection || disabled || !checked) return;
    const seq = ++preflightSeq.current;
    const ownerAtStart = ownerEpoch.current;
    const [targetId, keeperId, selectionMode] = currentSelection.split('|');
    const preflightPath =
      keeperId === '' && selectionMode === 'revoke'
        ? `api-keys/${targetId}/revocation/revoke`
        : `api-keys/${targetId}/revocation/${keeperId}/${selectionMode}`;
    void (async () => {
      try {
        const value = await client.get<{ ready: boolean; reason: string }>(preflightPath, auth);
        if (!active.current || seq !== preflightSeq.current || ownerAtStart !== ownerEpoch.current)
          return;
        if (typeof value?.ready !== 'boolean') throw new Error();
        setPreflight({
          selection: currentSelection,
          ready: value.ready,
          reason: typeof value.reason === 'string' ? value.reason : '',
        });
      } catch (err) {
        if (!active.current || seq !== preflightSeq.current || ownerAtStart !== ownerEpoch.current)
          return;
        if (err instanceof PublicApiError && err.kind === 'authentication') {
          onUnauthorized();
          return;
        }
        setPreflight({ selection: currentSelection, ready: false, reason: 'key_operation_unknown' });
      }
    })();
  }, [currentSelection, disabled, checked, reloadToken]); // eslint-disable-line react-hooks/exhaustive-deps

  function preflightText(reason: string): string {
    if (reason === 'key_operation_source_conflict') return k('operationBlockedSourceConflict');
    if (reason === 'key_operation_conflict') return k('operationBlockedInstalledKey');
    // The destination refused the stored Management Key. Only a rebind resolves
    // it, so the two refusals that name that path are read as one instruction
    // rather than as something to retry.
    if (reason === 'external_management_key_rejected' || reason === 'external_target_moved')
      return k('operationBlockedCredentialRejected');
    if (reason === 'last_effective_key') return k('lastKeyProtectionHelp');
    if (
      reason === 'key_operation_host_proof_required' ||
      reason === 'key_operation_generation_mismatch' ||
      reason === 'external_cpa_version_unsupported' ||
      reason === 'external_cpa_authority_unavailable'
    ) {
      return k(effectBlockerTextKey(reason));
    }
    return k('operationBlockedUnavailable');
  }

  async function loadRecoveredTargetKey(
    targetId: string,
    ownerAtStart: number,
    modalEpoch: number
  ): Promise<Key | null> {
    try {
      const latest = await client.get<Key>(`api-keys/${targetId}`, auth);
      if (
        !active.current ||
        ownerEpoch.current !== ownerAtStart ||
        modalSessionEpoch.current !== modalEpoch
      ) {
        return null;
      }
      if (validKey(latest) && latest.id === targetId) {
        setRecoveredTargetKey(latest);
        return latest;
      }
      return null;
    } catch (err) {
      if (
        active.current &&
        ownerEpoch.current === ownerAtStart &&
        modalSessionEpoch.current === modalEpoch &&
        err instanceof PublicApiError &&
        err.kind === 'authentication'
      ) {
        onUnauthorized();
      }
      return null;
    }
  }

  function accept(
    value: KeyOperationResult,
    expected?: string,
    selection = request,
    observed = true,
    ownerAtStart = ownerEpoch.current,
    modalEpoch = modalSessionEpoch.current,
    fromRecoveryOpen = false
  ) {
    const prevResult = latestResult.current;
    const pendingUnrecorded =
      observed &&
      !!selection &&
      selection.id === expected &&
      value.operation === null &&
      (!prevResult?.operation ||
        (isTerminalKeyOperationOutcome(prevResult.operation.outcome) &&
          prevResult.operation.id !== expected));
    if (
      !validKeyOperationResult(value) ||
      (expected && !pendingUnrecorded && (!value.operation || value.operation.id !== expected))
    )
      throw new Error();
    if (
      selection &&
      value.operation &&
      (value.operation.id !== selection.id ||
        value.operation.targetId !== selection.target ||
        value.operation.keeperId !== selection.keeper ||
        value.operation.mode !== selection.mode)
    )
      throw new Error();
    if (prevResult?.operation) {
      if (!value.operation) {
        if (
          !pendingUnrecorded &&
          (!isTerminalKeyOperationOutcome(prevResult.operation.outcome) ||
            !expected ||
            expected === prevResult.operation.id)
        ) {
          throw new Error();
        }
      } else if (value.operation.id === prevResult.operation.id) {
        const rank = { unattempted: 0, attempted: 1, observed: 2 };
        if (
          value.operation.targetId !== prevResult.operation.targetId ||
          value.operation.keeperId !== prevResult.operation.keeperId ||
          value.operation.mode !== prevResult.operation.mode ||
          value.operation.revision < prevResult.operation.revision ||
          (prevResult.operation.outcome === 'committed' &&
            value.operation.outcome !== 'committed') ||
          (prevResult.operation.outcome === 'aborted' && value.operation.outcome !== 'aborted') ||
          value.operation.steps.some((step, i) => {
            const previous = prevResult.operation!.steps[i];
            return (
              rank[step.state] < rank[previous.state] || (previous.returned && !step.returned)
            );
          })
        ) {
          throw new Error();
        }
      }
    }
    if (
      !active.current ||
      ownerAtStart !== ownerEpoch.current ||
      (isPureModal && modalEpoch !== modalSessionEpoch.current)
    )
      return;
    latestResult.current = value;
    setResult(value);
    onLatestOperation?.(value);
    if (pendingUnrecorded) {
      setChecked(true);
      setRequest(null);
      setPreflight(null);
      setBlockerCode('');
      setBlockerDetail('');
      setMessage(k('operationNotRecorded'));
      onPending(false);
      return;
    }
    const isTerminal =
      !!value.operation && isTerminalKeyOperationOutcome(value.operation.outcome);
    const keepChecked = observed || value.operation?.outcome === 'aborted';
    setChecked(keepChecked);
    if (observed) {
      setBlockerCode('');
      setBlockerDetail('');
    }
    setMessage(
      value.operation?.outcome === 'aborted'
        ? expected ||
          prevResult?.operation?.id === value.operation.id ||
          (isPureModal && !preset && fromRecoveryOpen)
          ? k('operationAborted')
          : ''
        : observed
          ? ''
          : t('keysOperationUnknown')
    );
    setRequest(null);
    onPending(!keepChecked || (!!value.operation && !isTerminal));
    const shouldNotifyChanged =
      (observed &&
        value.operation?.outcome === 'committed' &&
        (value.operation.mode !== 'add' ||
          !!expected ||
          (prevResult?.operation?.id === value.operation.id &&
            prevResult.operation.outcome !== 'committed'))) ||
      (value.operation?.outcome === 'aborted' &&
        (!!expected ||
          (prevResult?.operation?.id === value.operation.id &&
            prevResult.operation.outcome !== 'aborted')));
    if (shouldNotifyChanged) {
      if (preset && expected === value.operation!.id) {
        if (
          value.operation!.outcome === 'committed' &&
          value.operation!.targetId === preset.targetId &&
          value.operation!.mode === preset.mode
        ) {
          setSettledPresetSeq(preset.seq);
        }
        if (value.operation!.outcome === 'aborted') setAbortedPresetSeq(preset.seq);
      }
      onChanged();
    }
    if (isPureModal && !preset && fromRecoveryOpen && value.operation) {
      if (value.operation.outcome === 'aborted') {
        onPending(false);
        if (!shouldNotifyChanged) onChanged();
      } else if (observed && value.operation.outcome === 'committed') {
        if (value.operation.mode === 'rotate') {
          setModalDone('rotated');
          setModalDoneTargetId(value.operation.targetId);
          void loadRecoveredTargetKey(
            value.operation.targetId,
            ownerAtStart,
            modalEpoch
          );
          onPending(false);
          if (!shouldNotifyChanged) onChanged();
        } else {
          setModalDone(
            value.operation.mode === 'replace'
              ? 'replaced'
              : value.operation.mode === 'add'
                ? 'added'
                : 'revoked'
          );
          setModalDoneTargetId(value.operation.targetId);
          onPending(false);
          if (!shouldNotifyChanged) onChanged();
        }
      }
    }
  }
  function failure(
    err: unknown,
    expected?: string,
    selection = request,
    ownerAtStart = ownerEpoch.current,
    allowPrepareConflict = false,
    modalEpoch = modalSessionEpoch.current,
    fromRecoveryOpen = false
  ) {
    if (
      !active.current ||
      ownerAtStart !== ownerEpoch.current ||
      (isPureModal && modalEpoch !== modalSessionEpoch.current)
    )
      return;
    if (err instanceof PublicApiError && err.kind === 'authentication') {
      onUnauthorized();
      return;
    }
    const code = err instanceof PublicApiError ? err.code || '' : '';
    const detail = err instanceof PublicApiError ? err.detail || '' : '';
    if (
      allowPrepareConflict &&
      err instanceof PublicApiError &&
      err.status === 409 &&
      code === 'key_operation_conflict' &&
      err.operationResult !== undefined
    ) {
      try {
        const recovered = err.operationResult as KeyOperationResult;
        if (
          !validKeyOperationResult(recovered) ||
          !recovered.operation ||
          recovered.runtimeState === 'effective_observed' ||
          isTerminalKeyOperationOutcome(recovered.operation.outcome) ||
          recovered.operation.id === expected
        ) {
          throw new Error();
        }
        accept(recovered, recovered.operation.id, null, false, ownerAtStart, modalEpoch, fromRecoveryOpen);
        setBlockerCode(code);
        setBlockerDetail('');
        setMessage(t('keysOperationRejected'));
        return;
      } catch {
        // Fall through to standard selection/id checks or fail-closed lock.
      }
    }
    if (err instanceof PublicApiError && err.operationResult !== undefined) {
      try {
        const recovered = err.operationResult as KeyOperationResult;
        if (!recovered?.operation || recovered.runtimeState === 'effective_observed')
          throw new Error();
        accept(recovered, expected, selection, false, ownerAtStart, modalEpoch, fromRecoveryOpen);
        if (recovered.operation.outcome === 'aborted') {
          const preservedCode = code === 'key_operation_conflict' ? '' : code;
          setBlockerCode(preservedCode);
          setBlockerDetail(preservedCode === 'external_management_key_rejected' ? detail : '');
          setMessage(k('operationAborted'));
        } else {
          setBlockerCode(code);
          setBlockerDetail(code === 'external_management_key_rejected' ? detail : '');
          if (err.status === 409) setMessage(t('keysOperationRejected'));
        }
        return;
      } catch {
        // Invalid or regressed failed-response snapshots never unlock a write.
      }
    }
    if (code && blockerOf(code)) {
      onBlocker?.(code, detail);
      onExternalError?.(code, detail);
    }
    setBlockerCode(code);
    setBlockerDetail(code === 'external_management_key_rejected' ? detail : '');
    setMessage(
      t(
        err instanceof PublicApiError && err.status === 409
          ? 'keysOperationRejected'
          : 'keysOperationUnknown'
      )
    );
    setChecked(false);
    const hasExistingUnsettled =
      !!selection ||
      (!!latestResult.current?.operation &&
        !isTerminalKeyOperationOutcome(latestResult.current.operation.outcome));
    if (!hasExistingUnsettled) {
      onPending(false);
    }
  }
  async function run(
    action: () => Promise<KeyOperationResult>,
    expected?: string,
    selection = request,
    ownerAtStart = ownerEpoch.current,
    allowPrepareConflict = false,
    modalEpoch = modalSessionEpoch.current,
    fromRecoveryOpen = false
  ) {
    if (flight.current) return;
    flight.current = true;
    setBusy(true);
    try {
      accept(await action(), expected, selection, true, ownerAtStart, modalEpoch, fromRecoveryOpen);
    } catch (err) {
      failure(err, expected, selection, ownerAtStart, allowPrepareConflict, modalEpoch, fromRecoveryOpen);
    } finally {
      if (
        ownerAtStart === ownerEpoch.current &&
        (!isPureModal || modalEpoch === modalSessionEpoch.current)
      ) {
        flight.current = false;
        if (active.current) setBusy(false);
      }
    }
  }
  async function read(
    ownerAtStart = ownerEpoch.current,
    forceLatest = false,
    fromRecoveryOpen = false,
    modalEpoch = modalSessionEpoch.current
  ) {
    const id = forceLatest ? 'latest' : request?.id || result?.operation?.id || 'latest';
    await run(
      () => client.get<KeyOperationResult>('api-keys/operations/' + id, auth),
      id === 'latest' ? undefined : id,
      forceLatest ? null : request,
      ownerAtStart,
      false,
      modalEpoch,
      fromRecoveryOpen
    );
  }
  async function prepare() {
    if (
      flight.current ||
      disabled ||
      !checked ||
      request ||
      (result?.operation && !isTerminalKeyOperationOutcome(result.operation.outcome)) ||
      !validTarget ||
      !validKeeper ||
      available.length <= 1 ||
      !currentSelection ||
      !preflight ||
      preflight.selection !== currentSelection ||
      !preflight.ready
    )
      return;
    const ownerAtStart = ownerEpoch.current;
    const id = Array.from(crypto.getRandomValues(new Uint8Array(16)), (b) =>
      b.toString(16).padStart(2, '0')
    ).join('');
    const selection = { id, target, keeper, mode };
    setMessage('');
    setBlockerCode('');
    setBlockerDetail('');
    setAbortedPresetSeq(null);
    setRequest(selection);
    setChecked(false);
    onPending(true);
    if (!preset) {
      await run(
        async () => {
          const value = await client.post<KeyOperationResult>(
            'api-keys/' + target + '/revocation',
            { requestId: id, keeperId: keeper, mode },
            auth
          );
          if (!value.operation) throw new Error();
          return value;
        },
        id,
        selection,
        ownerAtStart,
        true
      );
      return;
    }
    flight.current = true;
    setBusy(true);
    let currentId: string | undefined = id;
    let activeSelection: typeof selection | null = selection;
    let inPreparePost = true;
    try {
      let current = await client.post<KeyOperationResult>(
        'api-keys/' + target + '/revocation',
        { requestId: id, keeperId: keeper, mode },
        auth
      );
      inPreparePost = false;
      if (!current.operation) throw new Error();
      accept(current, id, selection, true, ownerAtStart);
      activeSelection = null;
      currentId = current.operation.id;
      const maxSteps = current.operation.steps.length;
      for (
        let step = 0;
        active.current &&
        ownerAtStart === ownerEpoch.current &&
        current.operation &&
        !isTerminalKeyOperationOutcome(current.operation.outcome) &&
        !!current.nextStep &&
        step < maxSteps;
        step += 1
      ) {
        setChecked(false);
        onPending(true);
        const next = await client.post<KeyOperationResult>(
          'api-keys/operations/' + current.operation.id + '/continue',
          {},
          auth
        );
        accept(next, current.operation.id, null, true, ownerAtStart);
        current = next;
      }
    } catch (err) {
      failure(err, currentId, activeSelection, ownerAtStart, inPreparePost);
    } finally {
      flight.current = false;
      if (active.current && ownerAtStart === ownerEpoch.current) setBusy(false);
    }
  }
  async function proceed() {
    const o = result?.operation;
    if (
      flight.current ||
      disabled ||
      !checked ||
      !o ||
      !result.nextStep ||
      isTerminalKeyOperationOutcome(o.outcome) ||
      request
    )
      return;
    const ownerAtStart = ownerEpoch.current;
    setChecked(false);
    onPending(true);
    await run(
      () => client.post<KeyOperationResult>('api-keys/operations/' + o.id + '/continue', {}, auth),
      o.id,
      request,
      ownerAtStart
    );
  }

  const inModal = isPureModal || legacyModal;
  const activeUnsettledOp =
    result?.operation && !isTerminalKeyOperationOutcome(result.operation.outcome)
      ? result.operation
      : unverifiedModalOp;
  const modalTargetKey = preset
    ? available.find((item) => item.id === preset.targetId) ??
      (modalDone
        ? keys.find((item) => item.id === (modalDoneTargetId || preset.targetId))
        : undefined)
    : activeUnsettledOp
      ? keys.find((item) => item.id === activeUnsettledOp.targetId)
      : modalDoneTargetId
        ? recoveredTargetKey ?? keys.find((item) => item.id === modalDoneTargetId)
        : undefined;
  const modalTargetName = modalTargetKey
    ? keyDisplayName(modalTargetKey, k('installerOrigin'), k('adoptedOrigin'), t('keysUnnamed'))
    : t('keysUnnamed');
  const isLastEffectiveBlocked =
    lastKeyBlocked ||
    (Boolean(modalTargetKey) &&
      modalTargetKey?.runtimeState === 'effective_observed' &&
      effectiveKeys.length <= 1);

  async function executePureRevoke() {
    if (flight.current || busy || disabled || !modalTargetKey || isLastEffectiveBlocked) return;
    const ownerAtStart = ownerEpoch.current;
    const modalEpoch = modalSessionEpoch.current;
    const targetId = modalTargetKey.id;
    const id = modalRequestIdRef.current || randomRequestId();
    modalRequestIdRef.current = id;
    flight.current = true;
    setBusy(true);
    setMessage('');
    setBlockerCode('');
    setBlockerDetail('');
    try {
      let current = await client.post<KeyOperationResult>(
        `api-keys/${modalTargetKey.id}/revocation`,
        { requestId: id, keeperId: '', mode: 'revoke' },
        auth
      );
      if (!validKeyOperationResult(current) || !current.operation) {
        throw new Error('invalid revocation response');
      }
      if (!active.current || ownerAtStart !== ownerEpoch.current || modalEpoch !== modalSessionEpoch.current) return;
      latestResult.current = current;
      setResult(current);
      onLatestOperation?.(current);
      if (current.operation.outcome === 'committed') {
        modalRequestIdRef.current = '';
        onPending(false);
        onChanged();
        setModalDone('revoked');
        setModalDoneTargetId(modalTargetKey.id);
        return;
      }
      if (current.operation.outcome === 'aborted') {
        modalRequestIdRef.current = randomRequestId();
        onPending(false);
        onChanged();
        setMessage(k('operationAborted'));
        return;
      }
      onPending(true);
      if (current.nextStep) {
        current = await client.post<KeyOperationResult>(
          `api-keys/operations/${current.operation.id}/continue`,
          {},
          auth
        );
        if (!validKeyOperationResult(current)) {
          throw new Error('invalid continue response');
        }
        if (!active.current || ownerAtStart !== ownerEpoch.current || modalEpoch !== modalSessionEpoch.current) return;
        latestResult.current = current;
        setResult(current);
        onLatestOperation?.(current);
        if (current.operation?.outcome === 'committed') {
          modalRequestIdRef.current = '';
          onPending(false);
          onChanged();
          setModalDone('revoked');
          setModalDoneTargetId(modalTargetKey.id);
          return;
        }
        if (current.operation?.outcome === 'aborted') {
          modalRequestIdRef.current = randomRequestId();
          onPending(false);
          onChanged();
          setMessage(k('operationAborted'));
          return;
        }
      }
      onPending(true);
      onChanged();
      setMessage(
        current.operation?.outcome === 'unknown'
          ? t('keysOperationUnknown')
          : t('keysOperationPending')
      );
    } catch (err) {
      if (!active.current || ownerAtStart !== ownerEpoch.current || modalEpoch !== modalSessionEpoch.current) return;
      if (err instanceof PublicApiError && err.kind === 'authentication') {
        onUnauthorized();
        return;
      }
      const code = err instanceof PublicApiError ? err.code || '' : '';
      const detail = err instanceof PublicApiError ? err.detail || '' : '';
      if (code && blockerOf(code)) {
        onBlocker?.(code, detail);
        onExternalError?.(code, detail);
      }
      if (code === 'last_effective_key') {
        setLastKeyBlocked(true);
        modalRequestIdRef.current = randomRequestId();
        onPending(false);
        setBlockerCode(code);
        setMessage(k('effectBlockerLastKey'));
        return;
      }
      if (err instanceof PublicApiError && err.operationResult !== undefined) {
        const recovered = err.operationResult as KeyOperationResult;
        if (validKeyOperationResult(recovered) && recovered.operation) {
          latestResult.current = recovered;
          setResult(recovered);
          onLatestOperation?.(recovered);
          if (recovered.operation.outcome === 'aborted') {
            modalRequestIdRef.current = randomRequestId();
            onPending(false);
            onChanged();
            setBlockerCode(code === 'key_operation_conflict' ? '' : code);
            setBlockerDetail(code === 'external_management_key_rejected' ? detail : '');
            setMessage(k('operationAborted'));
            return;
          }
          if (!isTerminalKeyOperationOutcome(recovered.operation.outcome)) {
            onPending(true);
            onChanged();
            setBlockerCode(code);
            setBlockerDetail(code === 'external_management_key_rejected' ? detail : '');
            setMessage(
              err.status === 409 ? t('keysOperationRejected') : t('keysOperationUnknown')
            );
            return;
          }
        }
      }
      if (err instanceof PublicApiError && err.status === 400) {
        modalRequestIdRef.current = randomRequestId();
        setBlockerCode(code);
        setBlockerDetail(code === 'external_management_key_rejected' ? detail : '');
        setMessage(t('keysOperationUnknown'));
        return;
      }
      if (!(err instanceof PublicApiError) || err.status === undefined || err.status >= 500) {
        setUnverifiedModalOp({
          id,
          targetId,
          keeperId: '',
          mode: 'revoke',
          outcome: 'unknown',
          revision: 1,
          steps: [{ name: 'revoke', state: 'attempted', returned: false }],
        });
        onPending(true);
        onChanged();
      }
      setBlockerCode(code);
      setBlockerDetail(code === 'external_management_key_rejected' ? detail : '');
      setMessage(
        t(
          err instanceof PublicApiError && err.status === 409
            ? 'keysOperationRejected'
            : 'keysOperationUnknown'
        )
      );
    } finally {
      if (ownerEpoch.current === ownerAtStart && modalSessionEpoch.current === modalEpoch) {
        flight.current = false;
        if (active.current) setBusy(false);
      }
    }
  }

  async function checkAndContinueModalOperation() {
    if (flight.current || busy || !activeUnsettledOp) return;
    const ownerAtStart = ownerEpoch.current;
    const modalEpoch = modalSessionEpoch.current;
    const opId = activeUnsettledOp.id;
    const opMode = activeUnsettledOp.mode;
    const opTargetId = activeUnsettledOp.targetId;
    flight.current = true;
    setBusy(true);
    setMessage('');
    setBlockerCode('');
    setBlockerDetail('');
    try {
      let current = await client.get<KeyOperationResult>(`api-keys/operations/${opId}`, auth);
      if (!validKeyOperationResult(current)) {
        throw new Error('invalid operation response');
      }
      if (
        !active.current ||
        ownerAtStart !== ownerEpoch.current ||
        modalEpoch !== modalSessionEpoch.current
      ) {
        return;
      }
      setUnverifiedModalOp(null);
      latestResult.current = current;
      setResult(current);
      onLatestOperation?.(current);
      if (!current.operation) {
        modalRequestIdRef.current = opId;
        onPending(false);
        setMessage(k('operationNotRecorded'));
        return;
      }
      if (current.operation.outcome === 'aborted') {
        modalRequestIdRef.current = randomRequestId();
        onPending(false);
        onChanged();
        setMessage(k('operationAborted'));
        return;
      }
      if (current.operation.outcome === 'committed') {
        if (opMode === 'rotate') {
          await loadRecoveredTargetKey(opTargetId, ownerAtStart, modalEpoch);
          if (
            !active.current ||
            ownerAtStart !== ownerEpoch.current ||
            modalEpoch !== modalSessionEpoch.current
          ) {
            return;
          }
        }
        onPending(false);
        onChanged();
        setModalDone(
          opMode === 'rotate'
            ? 'rotated'
            : opMode === 'replace'
              ? 'replaced'
              : opMode === 'add'
                ? 'added'
                : 'revoked'
        );
        setModalDoneTargetId(opTargetId);
        return;
      }
      if (current.nextStep) {
        current = await client.post<KeyOperationResult>(
          `api-keys/operations/${opId}/continue`,
          {},
          auth
        );
        if (!validKeyOperationResult(current)) {
          throw new Error('invalid continue response');
        }
        if (
          !active.current ||
          ownerAtStart !== ownerEpoch.current ||
          modalEpoch !== modalSessionEpoch.current
        ) {
          return;
        }
        latestResult.current = current;
        setResult(current);
        onLatestOperation?.(current);
        if (current.operation?.outcome === 'committed') {
          if (opMode === 'rotate') {
            await loadRecoveredTargetKey(opTargetId, ownerAtStart, modalEpoch);
            if (
              !active.current ||
              ownerAtStart !== ownerEpoch.current ||
              modalEpoch !== modalSessionEpoch.current
            ) {
              return;
            }
          }
          onPending(false);
          onChanged();
          setModalDone(
            opMode === 'rotate'
              ? 'rotated'
              : opMode === 'replace'
                ? 'replaced'
                : opMode === 'add'
                  ? 'added'
                  : 'revoked'
          );
          setModalDoneTargetId(opTargetId);
          return;
        }
        if (current.operation?.outcome === 'aborted') {
          modalRequestIdRef.current = randomRequestId();
          onPending(false);
          onChanged();
          setMessage(k('operationAborted'));
          return;
        }
      }
      onPending(true);
      onChanged();
      setMessage(
        current.operation?.outcome === 'unknown'
          ? t('keysOperationUnknown')
          : t('keysOperationPending')
      );
    } catch (err) {
      if (!active.current || ownerAtStart !== ownerEpoch.current || modalEpoch !== modalSessionEpoch.current) return;
      if (err instanceof PublicApiError && err.kind === 'authentication') {
        onUnauthorized();
        return;
      }
      if (unverifiedModalOp && !result?.operation && err instanceof PublicApiError && err.status === 404) {
        modalRequestIdRef.current = opId;
        setUnverifiedModalOp(null);
        onPending(false);
        setMessage(k('operationNotRecorded'));
        return;
      }
      const code = err instanceof PublicApiError ? err.code || '' : '';
      const detail = err instanceof PublicApiError ? err.detail || '' : '';
      if (code && blockerOf(code)) {
        onBlocker?.(code, detail);
        onExternalError?.(code, detail);
      }
      if (code === 'last_effective_key') {
        setLastKeyBlocked(true);
      }
      if (err instanceof PublicApiError && err.operationResult !== undefined) {
        const recovered = err.operationResult as KeyOperationResult;
        if (validKeyOperationResult(recovered) && recovered.operation) {
          latestResult.current = recovered;
          setResult(recovered);
          onLatestOperation?.(recovered);
          if (recovered.operation.outcome === 'aborted') {
            modalRequestIdRef.current = randomRequestId();
            onPending(false);
            onChanged();
            setBlockerCode(code === 'key_operation_conflict' ? '' : code);
            setBlockerDetail(code === 'external_management_key_rejected' ? detail : '');
            setMessage(k('operationAborted'));
            return;
          }
          if (!isTerminalKeyOperationOutcome(recovered.operation.outcome)) {
            onPending(true);
            onChanged();
            setBlockerCode(code);
            setBlockerDetail(code === 'external_management_key_rejected' ? detail : '');
            setMessage(
              err.status === 409 ? t('keysOperationRejected') : t('keysOperationUnknown')
            );
            return;
          }
        }
      }
      setBlockerCode(code);
      setBlockerDetail(code === 'external_management_key_rejected' ? detail : '');
      setMessage(
        t(
          err instanceof PublicApiError && err.status === 409
            ? 'keysOperationRejected'
            : 'keysOperationUnknown'
        )
      );
    } finally {
      if (ownerEpoch.current === ownerAtStart && modalSessionEpoch.current === modalEpoch) {
        flight.current = false;
        if (active.current) setBusy(false);
      }
    }
  }

  async function copyRecoveredRotatedSecret() {
    if (!modalDoneTargetId || flight.current || busy) return;
    const ownerAtStart = ownerEpoch.current;
    const modalEpoch = modalSessionEpoch.current;
    const targetId = modalDoneTargetId;
    flight.current = true;
    setBusy(true);
    setModalCopyFeedback('');
    try {
      let basisKey =
        recoveredTargetKey &&
        recoveredTargetKey.id === targetId &&
        (recoveredTargetKey.lastRotatedAtMs ?? 0) > 0
          ? recoveredTargetKey
          : null;
      if (!basisKey) {
        basisKey = await loadRecoveredTargetKey(targetId, ownerAtStart, modalEpoch);
        if (
          !active.current ||
          ownerEpoch.current !== ownerAtStart ||
          modalSessionEpoch.current !== modalEpoch
        ) {
          return;
        }
      }
      const reply = await client.post<{ secret: string }>(
        `api-keys/${targetId}/secret`,
        {},
        auth
      );
      if (
        !active.current ||
        ownerEpoch.current !== ownerAtStart ||
        modalSessionEpoch.current !== modalEpoch
      ) {
        return;
      }
      const fallbackKey = keys.find((item) => item.id === targetId) ?? null;
      const validationKey =
        basisKey ??
        (fallbackKey && (fallbackKey.lastRotatedAtMs ?? 0) > 0 ? fallbackKey : null);
      if (
        !validationKey ||
        !reply ||
        typeof reply.secret !== 'string' ||
        !validSavedSecret(validationKey, reply.secret)
      ) {
        throw new Error('invalid secret response');
      }
      setModalSecret(reply.secret);
      try {
        await navigator.clipboard.writeText(reply.secret);
        if (
          active.current &&
          ownerEpoch.current === ownerAtStart &&
          modalSessionEpoch.current === modalEpoch
        ) {
          setModalCopyFeedback('copied');
        }
      } catch {
        if (
          active.current &&
          ownerEpoch.current === ownerAtStart &&
          modalSessionEpoch.current === modalEpoch
        ) {
          setModalCopyFeedback('failed');
        }
      }
    } catch (err) {
      if (
        !active.current ||
        ownerEpoch.current !== ownerAtStart ||
        modalSessionEpoch.current !== modalEpoch
      ) {
        return;
      }
      if (err instanceof PublicApiError && err.kind === 'authentication') {
        onUnauthorized();
        return;
      }
      setModalCopyFeedback('failed');
    } finally {
      if (ownerEpoch.current === ownerAtStart && modalSessionEpoch.current === modalEpoch) {
        flight.current = false;
        if (active.current) setBusy(false);
      }
    }
  }

  const guiding = showGuide && !!onCreateReplacement && checked && !unresolved && !presetSettled;
  const presetKey = preset ? keys.find((item) => item.id === preset.targetId) : undefined;
  const showOperationStatus =
    !!result?.operation &&
    !committedAddOnly &&
    (!preset ||
      unresolved ||
      presetSettled ||
      (result.operation.outcome === 'aborted' && abortedPresetSeq === preset.seq));
  const canPrepare =
    !disabled &&
    !busy &&
    !unresolved &&
    checked &&
    validTarget &&
    validKeeper &&
    available.length > 1 &&
    !!currentSelection &&
    !!preflight &&
    preflight.selection === currentSelection &&
    preflight.ready;
  const formatKeyOptionLabel = (key: Key) =>
    `${keyDisplayName(key, k('installerOrigin'), k('adoptedOrigin'), t('keysUnnamed'))} · ${key.maskedPreview}`;
  const keeperPlaceholder =
    mode === 'revoke' && available.length > 1
      ? k('anySurvivingKeeperOption')
      : t('keysOperationSelect');
  const keeperSelectOptions = [
    { value: '', label: keeperPlaceholder },
    ...available
      .filter((key) => key.id !== target)
      .map((key) => ({ value: key.id, label: formatKeyOptionLabel(key) })),
  ];
  const replaceSteps = [k('replaceStep1'), k('replaceStep2'), k('replaceStep3')].map((text) =>
    text.replace(/^\d+\.\s*/, '')
  );
  const outcomeBannerClass =
    result?.operation?.outcome === 'committed'
      ? 'is-committed'
      : result?.operation?.outcome === 'aborted'
        ? 'is-aborted'
        : 'is-pending';
  const outcomeBannerIcon =
    result?.operation?.outcome === 'committed'
      ? 'check'
      : result?.operation?.outcome === 'aborted'
        ? 'alert'
        : 'clock';
  const outcomeBannerTitle =
    result?.operation?.outcome === 'committed'
      ? result.operation.mode === 'replace'
        ? k('operationReplaceCommittedTitle')
        : k('operationRevokeCommittedTitle')
      : result?.operation?.outcome === 'aborted'
        ? k('operationAbortedTitle')
        : k('operationPendingTitle');

  const content = (
    <section
      className={
        inModal
          ? 'key-revocation-workspace is-modal'
          : 'console-key-result key-revocation-workspace'
      }
      aria-busy={busy}
    >
      {/* CI browser proofs locate controls via this heading's direct parent. */}
      <h3>{t('keysRevokeTitle')}</h3>
      {preset && (
        <div className="key-operation-resource" data-testid="key-operation-target-card">
          <span className="key-resource-icon">
            <FullWebIcon name="key" size={16} />
          </span>
          <div className="key-operation-resource-body">
            <span className="key-operation-resource-label">{k('operationTargetLabel')}</span>
            <strong>
              {presetKey
                ? keyDisplayName(presetKey, k('installerOrigin'), k('adoptedOrigin'), t('keysUnnamed'))
                : t('keysUnnamed')}
            </strong>
            {presetKey?.maskedPreview && <code>{presetKey.maskedPreview}</code>}
          </div>
        </div>
      )}
      {(!preset || guiding || (showGuide && !onCreateReplacement)) && (
        <div className="key-replace-guide" data-testid="key-replacement-guide">
          <strong className="key-replace-guide-title">{k('replaceGuideTitle')}</strong>
          <ol className="key-replace-steps">
            {replaceSteps.map((stepText, idx) => (
              <li key={idx}>
                <span className="key-replace-step-index" aria-hidden="true">
                  {idx + 1}
                </span>
                <span>{stepText}</span>
              </li>
            ))}
          </ol>
          {!inModal && showGuide && onCreateReplacement && (
            <div className="key-modal-actions">
              <button
                className="btn btn-secondary"
                disabled={busy || unresolved || !checked}
                onClick={() => setShowGuide(false)}
              >
                {k('replacementReady')}
              </button>
              <button
                className="btn btn-primary"
                disabled={busy || unresolved || !checked}
                onClick={onCreateReplacement}
              >
                {k('createReplacement')}
              </button>
            </div>
          )}
        </div>
      )}
      {available.length <= 1 && guiding && (
        <div className="key-notice key-notice-warning" data-testid="last-key-protection-notice">
          <FullWebIcon name="shield" />
          <div>
            <strong>{k('lastKeyProtectionTitle')}</strong>
            <p>{k('lastKeyProtectionHelp')}</p>
          </div>
        </div>
      )}
      {!guiding && (
        <>
          {!preset && <p className="key-field-help">{t('keysOperationHelp')}</p>}
          {!presetSettled && !unresolved && available.length <= 1 && (
            <div className="key-notice key-notice-warning" data-testid="last-key-protection-notice">
              <FullWebIcon name="shield" />
              <div>
                <strong>{k('lastKeyProtectionTitle')}</strong>
                <p>{onRegenerate && preset ? k('regenerateLastKeyHint') : k('lastKeyProtectionHelp')}</p>
                {onRegenerate && preset && (
                  <div className="key-notice-inline-action">
                    <button
                      type="button"
                      className="btn btn-primary btn-sm"
                      data-testid="revoke-switch-to-regenerate"
                      disabled={busy}
                      onClick={() => onRegenerate(preset.targetId)}
                    >
                      {k('regenerateKey')}
                    </button>
                  </div>
                )}
              </div>
            </div>
          )}
          {message && (
            <p role="alert" className="key-notice key-notice-warning">
              <FullWebIcon name="alert" />
              <span>{message}</span>
            </p>
          )}
          {blockerCode && blockerCode !== 'key_operation_unknown' && (
            <p className="key-field-help" data-testid="key-operation-blocker-detail">
              {k(effectBlockerTextKey(blockerCode))}
            </p>
          )}
          {blockerCode === 'external_management_key_rejected' && blockerDetail && (
            <details
              className="fresh-external-blocker-detail"
              data-testid="key-operation-rebind-detail"
            >
              <summary>{x('details')}</summary>
              <p>{blockerDetail}</p>
            </details>
          )}
          {onRebind &&
            ['external_management_key_rejected', 'external_target_moved'].includes(
              blockerCode || preflight?.reason || ''
            ) && (
              <div className="key-modal-actions">
                <button
                  type="button"
                  className="btn btn-secondary"
                  data-testid="operation-rebind-action"
                  disabled={busy}
                  onClick={onRebind}
                >
                  {k('rebindConnection')}
                </button>
              </div>
            )}
          {request && (
            <p className="key-operation-request-id">
              <span>{t('keysOperationId')}:</span> <code>{request.id}</code>
            </p>
          )}
          {showOperationStatus && result?.operation && (
            <div className="key-operation-status" role="status">
              <div className={`key-operation-banner ${outcomeBannerClass}`}>
                <span className="key-operation-banner-icon">
                  <FullWebIcon name={outcomeBannerIcon} size={18} />
                </span>
                <div className="key-operation-banner-body">
                  <strong>{outcomeBannerTitle}</strong>
                  <p data-testid="key-operation-outcome">
                    {result.operation.outcome === 'committed'
                      ? t('keysOperationCommitted')
                      : result.operation.outcome === 'aborted'
                        ? k('operationAborted')
                        : t('keysOperationPending')}
                  </p>
                </div>
              </div>
              <ul className="key-operation-steps" data-testid="key-operation-steps">
                {result.operation.steps.map((step) => (
                  <li
                    key={step.name}
                    className={`key-operation-step is-${step.state}`}
                    data-testid={`key-operation-step-${step.name}`}
                    data-state={step.state}
                  >
                    <span className="key-operation-step-icon">
                      <FullWebIcon
                        name={
                          step.state === 'observed'
                            ? 'check'
                            : step.state === 'attempted'
                              ? 'clock'
                              : 'info'
                        }
                        size={14}
                      />
                    </span>
                    <span className="key-operation-step-label">
                      {k(
                        step.name === 'add'
                          ? 'operationStepAdd'
                          : step.name === 'rotate'
                            ? 'operationStepRotate'
                            : 'operationStepRevoke'
                      )}
                    </span>
                    <span className={`key-operation-step-badge is-${step.state}`}>
                      {k(
                        step.state === 'observed'
                          ? 'operationStepObserved'
                          : step.state === 'attempted'
                            ? 'operationStepAttempted'
                            : 'operationStepUnattempted'
                      )}
                    </span>
                  </li>
                ))}
              </ul>
              {result.nextStep && (
                <p className="key-field-help">
                  {t(
                    result.nextStep === 'add' ? 'keysOperationAddNext' : 'keysOperationRevokeNext'
                  )}
                </p>
              )}
              <details className="key-record-identifiers">
                <summary>
                  <FullWebIcon name="chevron" size={14} className="key-record-identifiers-chevron" />
                  <span>{k('diagnostics')}</span>
                </summary>
                <dl className="key-identifier-list">
                  <div>
                    <dt>{t('keysOperationId')}</dt>
                    <dd>
                      <code>{result.operation.id}</code>
                    </dd>
                  </div>
                  <div>
                    <dt>{t('keysOperationTarget')}</dt>
                    <dd>
                      <code>{result.operation.targetId}</code>
                    </dd>
                  </div>
                  {result.operation.keeperId !== '' && (
                    <div>
                      <dt>{t('keysOperationKeeper')}</dt>
                      <dd>
                        <code>{result.operation.keeperId}</code>
                      </dd>
                    </div>
                  )}
                </dl>
              </details>
              {!inModal && !presetSettled && result.operation.outcome !== 'aborted' && (
                <div className="key-modal-actions">
                  <button
                    className="btn btn-primary"
                    disabled={
                      disabled ||
                      busy ||
                      !checked ||
                      !result.nextStep ||
                      !!request ||
                      isTerminalKeyOperationOutcome(result.operation.outcome)
                    }
                    onClick={() => void proceed()}
                  >
                    {t('keysOperationContinue')}
                  </button>
                </div>
              )}
            </div>
          )}
          {!presetSettled && (
            <>
              <fieldset
                className="form-group console-key-operation-fields"
                disabled={disabled || busy || unresolved || !checked}
              >
                {preset ? (
                  <div className="key-operation-field">
                    <label className="form-label" htmlFor="key-operation-keeper">
                      {t('keysOperationKeeper')}
                    </label>
                    <Select
                      id="key-operation-keeper"
                      value={keeper}
                      options={keeperSelectOptions}
                      onChange={(nextValue) => setKeeper(nextValue)}
                      ariaLabel={t('keysOperationKeeper')}
                      disabled={disabled || busy || unresolved || !checked}
                      fullWidth
                    />
                  </div>
                ) : (
                  <>
                    <label className="form-group">
                      {t('keysOperationTarget')}
                      <span className="input-wrap">
                        <select
                          className="form-input"
                          value={target}
                          onChange={(e) => setTarget(e.target.value)}
                        >
                          <option value="">{t('keysOperationSelect')}</option>
                          {available.map((key) => (
                            <option key={key.id} value={key.id}>
                              {formatKeyOptionLabel(key)}
                            </option>
                          ))}
                        </select>
                      </span>
                    </label>
                    <label className="form-group">
                      {t('keysOperationKeeper')}
                      <span className="input-wrap">
                        <select
                          className="form-input"
                          value={keeper}
                          onChange={(e) => setKeeper(e.target.value)}
                        >
                          <option value="">{keeperPlaceholder}</option>
                          {available
                            .filter((key) => key.id !== target)
                            .map((key) => (
                              <option key={key.id} value={key.id}>
                                {formatKeyOptionLabel(key)}
                              </option>
                            ))}
                        </select>
                      </span>
                    </label>
                    <label className="form-group">
                      {t('keysOperationMode')}
                      <span className="input-wrap">
                        <select
                          className="form-input"
                          value={mode}
                          onChange={(e) => setMode(e.target.value as 'revoke' | 'replace')}
                        >
                          <option value="revoke">{t('keysOperationRevoke')}</option>
                          <option value="replace">{t('keysOperationReplace')}</option>
                        </select>
                      </span>
                    </label>
                  </>
                )}
                {!!currentSelection && !!preflight && preflight.selection === currentSelection && !preflight.ready && (
                  <div
                    role="alert"
                    className="key-notice key-notice-warning"
                    data-testid="key-operation-preflight"
                  >
                    <FullWebIcon name="alert" />
                    <div>
                      <p>{preflightText(preflight.reason)}</p>
                    </div>
                  </div>
                )}
                <div
                  className={
                    preset ? 'key-notice key-notice-danger key-operation-caution' : 'key-field-help'
                  }
                >
                  {preset && <FullWebIcon name="alert" size={16} />}
                  <p>{t('keysOperationConfirm')}</p>
                </div>
                {!inModal && (
                  <div className="key-modal-actions">
                    <button
                      className={preset ? 'btn btn-danger' : 'btn btn-primary'}
                      disabled={!canPrepare}
                      onClick={() => void prepare()}
                    >
                      {preset ? k('confirmOperation') : t('keysOperationPrepare')}
                    </button>
                  </div>
                )}
              </fieldset>
              {!inModal && (
                <button className="btn btn-secondary" disabled={busy} onClick={() => void read()}>
                  {t('keysOperationCheck')}
                </button>
              )}
            </>
          )}
        </>
      )}
    </section>
  );

  const closeModal = onClose ?? (() => {});
  const pureModalFooter = isPureModal ? (
    modalDone ? (
      <div className="key-modal-footer">
        <button type="button" className="btn btn-primary" onClick={closeModal} disabled={busy}>
          {t('keysDismiss')}
        </button>
      </div>
    ) : activeUnsettledOp ? (
      <div className="key-modal-footer">
        <button type="button" className="btn btn-secondary" onClick={closeModal} disabled={busy}>
          {k('closeDialog')}
        </button>
        <button
          type="button"
          className="btn btn-primary"
          data-testid="continue-recovery-operation"
          disabled={busy}
          onClick={() => void checkAndContinueModalOperation()}
        >
          {busy ? k('regenerateRunningTitle') : k('checkAndContinueAction')}
        </button>
      </div>
    ) : !modalTargetKey ? (
      <div className="key-modal-footer">
        <button type="button" className="btn btn-secondary" onClick={closeModal} disabled={busy}>
          {k('closeDialog')}
        </button>
        <button
          type="button"
          className="btn btn-primary"
          disabled={busy}
          onClick={() => void read(ownerEpoch.current, !preset, !preset, modalSessionEpoch.current)}
        >
          {t('keysOperationCheck')}
        </button>
      </div>
    ) : isLastEffectiveBlocked ? (
      <div className="key-modal-footer">
        <button type="button" className="btn btn-secondary" onClick={closeModal} disabled={busy}>
          {k('closeDialog')}
        </button>
        {onRegenerate && (
          <button
            type="button"
            className="btn btn-primary"
            data-testid="revoke-switch-to-regenerate"
            disabled={busy}
            onClick={() => onRegenerate(modalTargetKey.id)}
          >
            {k('regenerateKey')}
          </button>
        )}
      </div>
    ) : (
      <div className="key-modal-footer">
        <button type="button" className="btn btn-secondary" onClick={closeModal} disabled={busy}>
          {k('cancel')}
        </button>
        <button
          type="button"
          className="btn btn-danger"
          data-testid="confirm-pure-revoke"
          disabled={busy || disabled}
          onClick={() => void executePureRevoke()}
        >
          {busy ? k('revokingAction') : k('confirmRevokeAction')}
        </button>
      </div>
    )
  ) : undefined;

  const pureModalBody = isPureModal ? (
    <section className="key-revocation-workspace is-modal" aria-busy={busy}>
      {modalTargetKey && (
        <div className="key-operation-resource" data-testid="key-operation-target-card">
          <span className="key-resource-icon">
            <FullWebIcon name="key" size={16} />
          </span>
          <div className="key-operation-resource-body">
            <span className="key-operation-resource-label">{k('operationTargetLabel')}</span>
            <strong>{modalTargetName}</strong>
            <code>{modalTargetKey.maskedPreview}</code>
          </div>
        </div>
      )}

      {(modalDone === 'revoked' || modalDone === 'replaced' || modalDone === 'added') && (
        <div
          className="key-operation-banner is-committed"
          role="status"
          data-testid={
            modalDone === 'replaced'
              ? 'modal-replace-done'
              : modalDone === 'added'
                ? 'modal-add-done'
                : 'modal-revoke-done'
          }
        >
          <span className="key-operation-banner-icon">
            <FullWebIcon name="check" size={18} />
          </span>
          <div className="key-operation-banner-body">
            <strong>
              {k(
                modalDone === 'replaced'
                  ? 'operationReplaceCommittedTitle'
                  : modalDone === 'added'
                    ? 'operationAddCommittedTitle'
                    : 'operationRevokeCommittedTitle'
              )}
            </strong>
            <p data-testid="key-operation-outcome">{t('keysOperationCommitted')}</p>
          </div>
        </div>
      )}

      {modalDone === 'rotated' && (
        <div className="key-create-result-stack" role="status" data-testid="modal-rotate-done">
          <div className="key-operation-banner is-committed">
            <span className="key-operation-banner-icon">
              <FullWebIcon name="check" size={18} />
            </span>
            <div className="key-operation-banner-body">
              <strong>{k('regenerateSuccessTitle')}</strong>
              <p>{k('regenerateReplayHelp')}</p>
            </div>
          </div>
          <div className="key-secret-card">
            {modalSecret && (
              <div className="key-secret-value">
                <code data-testid="recovered-rotated-secret">{modalSecret}</code>
              </div>
            )}
            <div className="key-secret-card-actions">
              <button
                type="button"
                className="btn btn-primary btn-sm"
                data-testid="copy-recovered-rotate-secret"
                disabled={busy}
                onClick={() => void copyRecoveredRotatedSecret()}
              >
                <FullWebIcon name="copy" size={14} />
                {k('regenerateCopyNewSecret')}
              </button>
              {modalCopyFeedback && (
                <span
                  role="status"
                  className={`key-copy-inline-status ${
                    modalCopyFeedback === 'copied' ? 'is-copied' : 'is-failed'
                  }`}
                >
                  {t(modalCopyFeedback === 'copied' ? 'keysCopied' : 'keysCopyFailed')}
                </span>
              )}
            </div>
          </div>
        </div>
      )}

      {!modalDone && activeUnsettledOp && (
        <div className="key-operation-status" role="status" data-testid="key-operation-recovery">
          <div className="key-operation-banner is-pending">
            <span className="key-operation-banner-icon">
              <FullWebIcon name="clock" size={18} />
            </span>
            <div className="key-operation-banner-body">
              <strong>{operationModeTitle(activeUnsettledOp.mode)}</strong>
              <p data-testid="key-operation-outcome">{t('keysOperationPending')}</p>
            </div>
          </div>
          <ul className="key-operation-steps" data-testid="key-operation-steps">
            {activeUnsettledOp.steps.map((step) => (
              <li
                key={step.name}
                className={`key-operation-step is-${step.state}`}
                data-testid={`key-operation-step-${step.name}`}
                data-state={step.state}
              >
                <span className="key-operation-step-icon">
                  <FullWebIcon
                    name={
                      step.state === 'observed'
                        ? 'check'
                        : step.state === 'attempted'
                          ? 'clock'
                          : 'info'
                    }
                    size={14}
                  />
                </span>
                <span className="key-operation-step-label">
                  {k(
                    step.name === 'add'
                      ? activeUnsettledOp.mode === 'add'
                        ? 'operationStepAddCreate'
                        : 'operationStepAdd'
                      : step.name === 'rotate'
                        ? 'operationStepRotate'
                        : 'operationStepRevoke'
                  )}
                </span>
                <span className={`key-operation-step-badge is-${step.state}`}>
                  {k(
                    step.state === 'observed'
                      ? 'operationStepObserved'
                      : step.state === 'attempted'
                        ? 'operationStepAttempted'
                        : 'operationStepUnattempted'
                  )}
                </span>
              </li>
            ))}
          </ul>
          <details className="key-record-identifiers">
            <summary>
              <FullWebIcon name="chevron" size={14} className="key-record-identifiers-chevron" />
              <span>{k('diagnostics')}</span>
            </summary>
            <dl className="key-identifier-list">
              <div>
                <dt>{t('keysOperationId')}</dt>
                <dd>
                  <code>{activeUnsettledOp.id}</code>
                </dd>
              </div>
              <div>
                <dt>{t('keysOperationTarget')}</dt>
                <dd>
                  <code>{activeUnsettledOp.targetId}</code>
                </dd>
              </div>
            </dl>
          </details>
        </div>
      )}

      {!modalDone && !activeUnsettledOp && modalTargetKey && isLastEffectiveBlocked && (
        <div className="key-notice key-notice-warning" data-testid="last-key-protection-notice">
          <FullWebIcon name="shield" />
          <div>
            <strong>{k('lastKeyProtectionTitle')}</strong>
            <p>{k('lastKeyProtectionActionHelp')}</p>
          </div>
        </div>
      )}

      {!modalDone && !activeUnsettledOp && modalTargetKey && !isLastEffectiveBlocked && (
        <div className="key-notice key-notice-danger key-operation-caution" role="note">
          <FullWebIcon name="alert" size={16} />
          <div>
            <p>{k('revokeDangerNotice')}</p>
          </div>
        </div>
      )}

      {!modalDone && !activeUnsettledOp && !modalTargetKey && !message && (
        <p className="key-field-help">{k('operationNotRecorded')}</p>
      )}

      {message && (
        <div role="alert" className="key-notice key-notice-warning">
          <FullWebIcon name="alert" />
          <div>
            <p>{message}</p>
            {blockerCode && blockerCode !== 'key_operation_unknown' && (
              <p className="key-field-help" data-testid="key-operation-blocker-detail">
                {k(effectBlockerTextKey(blockerCode))}
                {blockerDetail ? ` (${blockerDetail})` : ''}
              </p>
            )}
            {onRebind &&
              ['external_management_key_rejected', 'external_target_moved'].includes(blockerCode) && (
                <div className="key-modal-actions">
                  <button
                    type="button"
                    className="btn btn-secondary btn-sm"
                    data-testid="operation-rebind-action"
                    disabled={busy}
                    onClick={onRebind}
                  >
                    {k('rebindConnection')}
                  </button>
                </div>
              )}
          </div>
        </div>
      )}
    </section>
  ) : null;

  const legacyModalFooter = legacyModal ? (
    <div className="key-modal-footer key-modal-footer-split">
      <button
        type="button"
        className="btn btn-ghost btn-sm key-check-quiet"
        disabled={busy}
        onClick={() => void read()}
      >
        {t('keysOperationCheck')}
      </button>
      <div className="key-modal-footer-right">
        <button
          type="button"
          className="btn btn-secondary"
          disabled={busy || unresolved}
          onClick={closeModal}
        >
          {k('closeDialog')}
        </button>
        {showOperationStatus &&
          result?.operation &&
          !presetSettled &&
          result.operation.outcome !== 'aborted' && (
            <button
              type="button"
              className="btn btn-primary"
              disabled={
                disabled ||
                busy ||
                !checked ||
                !result.nextStep ||
                !!request ||
                isTerminalKeyOperationOutcome(result.operation.outcome)
              }
              onClick={() => void proceed()}
            >
              {t('keysOperationContinue')}
            </button>
          )}
        {!presetSettled && (!preset || !showOperationStatus || result?.operation?.outcome === 'aborted') && (
          <button
            type="button"
            className={preset ? 'btn btn-danger' : 'btn btn-primary'}
            disabled={!canPrepare}
            onClick={() => void prepare()}
          >
            {preset ? k('confirmOperation') : t('keysOperationPrepare')}
          </button>
        )}
      </div>
    </div>
  ) : undefined;

  if (isPureModal) {
    return (
      <Modal
        open={open && visible}
        onClose={closeModal}
        title={
          modalDone === 'rotated'
            ? k('regenerateModalTitle')
            : modalDone === 'replaced'
              ? k('replaceModalTitle')
              : activeUnsettledOp || !preset
                ? k('operationRecoveryModalTitle')
                : k('revokeModalTitle')
        }
        closeDisabled={busy}
        className="key-operation-modal"
        width={520}
        footer={pureModalFooter}
      >
        {pureModalBody}
      </Modal>
    );
  }
  if (legacyModal) {
    return (
      <Modal
        open={open && visible}
        onClose={closeModal}
        title={t('keysRevokeTitle')}
        closeDisabled={busy || unresolved}
        className="key-operation-modal"
        width={520}
        footer={legacyModalFooter}
      >
        {content}
      </Modal>
    );
  }
  return content;
}
