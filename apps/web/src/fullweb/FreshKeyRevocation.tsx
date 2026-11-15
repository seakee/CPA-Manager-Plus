import { useEffect, useRef, useState } from 'react';
import { fullWebText as t } from '../i18n/fullweb';
import { keyWorkspaceText as k } from '../i18n/freshKeyWorkspace';
import { externalWorkspaceText as x } from '../i18n/freshExternal';
import { FullWebIcon } from './FullWebIcon';
import { PublicApiError, type createPublicApiClient } from './publicApi';
import {
  effectBlockerTextKey,
  isTerminalKeyOperationOutcome,
  keyDisplayName,
  validKeyOperationResult,
  type Key,
  type KeyOperationResult,
} from './freshKeyMetadata';
import { Modal } from '../components/ui/Modal';

export function FreshKeyRevocation({
  keys,
  client,
  credential,
  onUnauthorized,
  onPending,
  onChanged,
  disabled,
  preset,
  open = true,
  onClose,
  onCreateReplacement,
  onRebind,
  reloadToken = 0,
}: {
  keys: Key[];
  client: ReturnType<typeof createPublicApiClient>;
  credential: string;
  onUnauthorized: () => void;
  onPending: (value: boolean) => void;
  onChanged: () => void;
  disabled: boolean;
  preset?: { targetId: string; mode: 'revoke' | 'replace'; seq: number } | null;
  open?: boolean;
  onClose?: () => void;
  onCreateReplacement?: () => void;
  onRebind?: () => void;
  reloadToken?: number;
}) {
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
  const prevPresetSeq = useRef<number | null>(null);
  const latestResult = useRef<KeyOperationResult | null>(null);
  const active = useRef(true);
  const flight = useRef(false);
  const ownerEpoch = useRef(0);
  const prevReloadToken = useRef(reloadToken);
  const auth = { kind: 'admin' as const, value: credential };
  const available = keys.filter((k) => k.lifecycle !== 'superseded');

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
    prevPresetSeq.current = null;
    onPending(true);
    void read(ownerAtMount);
    return () => {
      active.current = false;
    };
  }, [credential]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (prevReloadToken.current === reloadToken) return;
    prevReloadToken.current = reloadToken;
    setPreflight(null);
    if (!flight.current) {
      setBlockerCode('');
      setBlockerDetail('');
      void read(ownerEpoch.current);
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
    if (!currentSelection || disabled || !checked) return;
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

  function accept(
    value: KeyOperationResult,
    expected?: string,
    selection = request,
    observed = true,
    ownerAtStart = ownerEpoch.current
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
    if (!active.current || ownerAtStart !== ownerEpoch.current) return;
    latestResult.current = value;
    setResult(value);
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
        ? expected || prevResult?.operation?.id === value.operation.id
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
  }
  function failure(
    err: unknown,
    expected?: string,
    selection = request,
    ownerAtStart = ownerEpoch.current,
    allowPrepareConflict = false
  ) {
    if (!active.current || ownerAtStart !== ownerEpoch.current) return;
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
        accept(recovered, recovered.operation.id, null, false, ownerAtStart);
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
        accept(recovered, expected, selection, false, ownerAtStart);
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
  }
  async function run(
    action: () => Promise<KeyOperationResult>,
    expected?: string,
    selection = request,
    ownerAtStart = ownerEpoch.current,
    allowPrepareConflict = false
  ) {
    if (flight.current) return;
    flight.current = true;
    setBusy(true);
    try {
      accept(await action(), expected, selection, true, ownerAtStart);
    } catch (err) {
      failure(err, expected, selection, ownerAtStart, allowPrepareConflict);
    } finally {
      flight.current = false;
      if (active.current && ownerAtStart === ownerEpoch.current) setBusy(false);
    }
  }
  async function read(ownerAtStart = ownerEpoch.current) {
    const id = request?.id || result?.operation?.id || 'latest';
    await run(
      () => client.get<KeyOperationResult>('api-keys/operations/' + id, auth),
      id === 'latest' ? undefined : id,
      request,
      ownerAtStart
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

  const guiding = showGuide && !!onCreateReplacement && checked && !unresolved && !presetSettled;
  const presetKey = preset ? keys.find((item) => item.id === preset.targetId) : undefined;
  const content = (
    <section className="console-key-result key-revocation-workspace" aria-busy={busy}>
      {/* CI browser proofs locate controls via this heading's direct parent. */}
      <h3>{t('keysRevokeTitle')}</h3>
      {(!preset || guiding || (showGuide && !onCreateReplacement)) && (
        <div className="key-replace-guide" data-testid="key-replacement-guide">
          <strong>{k('replaceGuideTitle')}</strong>
          <ol className="key-replace-steps">
            <li>{k('replaceStep1')}</li>
            <li>{k('replaceStep2')}</li>
            <li>{k('replaceStep3')}</li>
          </ol>
          {showGuide && onCreateReplacement && (
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
      {preset && (
        <div className="key-operation-resource">
          <strong>
            {presetKey
              ? keyDisplayName(presetKey, k('installerOrigin'), k('adoptedOrigin'), t('keysUnnamed'))
              : t('keysUnnamed')}
          </strong>
          <code>{presetKey?.maskedPreview}</code>
        </div>
      )}
      {available.length <= 1 && guiding && (
        <div className="key-notice key-notice-warning" data-testid="last-key-protection-notice">
          {k('lastKeyProtectionHelp')}
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
                <p>{k('lastKeyProtectionHelp')}</p>
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
            <p>
              {t('keysOperationId')}: <code>{request.id}</code>
            </p>
          )}
          {result?.operation &&
            !committedAddOnly &&
            (!preset ||
              unresolved ||
              presetSettled ||
              (result.operation.outcome === 'aborted' && abortedPresetSeq === preset.seq)) && (
              <div role="status">
                <details className="key-record-identifiers">
                  <summary>{k('diagnostics')}</summary>
                  <p>
                    {t('keysOperationId')}: <code>{result.operation.id}</code>
                  </p>
                  <p>
                    {t('keysOperationTarget')}: <code>{result.operation.targetId}</code>
                  </p>
                  {result.operation.keeperId !== '' && (
                    <p>
                      {t('keysOperationKeeper')}: <code>{result.operation.keeperId}</code>
                    </p>
                  )}
                </details>
                <p data-testid="key-operation-outcome">
                  {result.operation.outcome === 'committed'
                    ? t('keysOperationCommitted')
                    : result.operation.outcome === 'aborted'
                      ? k('operationAborted')
                      : t('keysOperationPending')}
                </p>
                <ul className="key-operation-steps" data-testid="key-operation-steps">
                  {result.operation.steps.map((step) => (
                    <li
                      key={step.name}
                      data-testid={`key-operation-step-${step.name}`}
                      data-state={step.state}
                    >
                      <span>{k(step.name === 'add' ? 'operationStepAdd' : 'operationStepRevoke')}</span>
                      {' — '}
                      <span>
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
                  <p>
                    {t(
                      result.nextStep === 'add' ? 'keysOperationAddNext' : 'keysOperationRevokeNext'
                    )}
                  </p>
                )}
                {!presetSettled && result.operation.outcome !== 'aborted' && (
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
                )}
                {presetSettled && onClose && (
                  <div className="key-modal-actions">
                    <button className="btn btn-primary" type="button" onClick={onClose}>
                      {k('closeDialog')}
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
                <label className="form-group" hidden={!!preset}>
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
                          {keyDisplayName(key, k('installerOrigin'), k('adoptedOrigin'), t('keysUnnamed'))} — {key.maskedPreview} ({key.id})
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
                      <option value="">
                        {mode === 'revoke' && available.length > 1
                          ? k('anySurvivingKeeperOption')
                          : t('keysOperationSelect')}
                      </option>
                      {available
                        .filter((key) => key.id !== target)
                        .map((key) => (
                          <option key={key.id} value={key.id}>
                            {keyDisplayName(key, k('installerOrigin'), k('adoptedOrigin'), t('keysUnnamed'))} — {key.maskedPreview} ({key.id})
                          </option>
                        ))}
                    </select>
                  </span>
                </label>
                <label className="form-group" hidden={!!preset}>
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
                <p className="key-field-help">{t('keysOperationConfirm')}</p>
                <button
                  className="btn btn-primary"
                  disabled={
                    !validTarget ||
                    !validKeeper ||
                    available.length <= 1 ||
                    !currentSelection ||
                    !preflight ||
                    preflight.selection !== currentSelection ||
                    !preflight.ready
                  }
                  onClick={() => void prepare()}
                >
                  {preset ? k('confirmOperation') : t('keysOperationPrepare')}
                </button>
              </fieldset>
              <button className="btn btn-secondary" disabled={busy} onClick={() => void read()}>
                {t('keysOperationCheck')}
              </button>
            </>
          )}
        </>
      )}
    </section>
  );
  return onClose ? (
    <Modal
      open={open}
      onClose={onClose}
      title={preset?.mode === 'replace' ? k('quickReplace') : k('quickRevoke')}
      closeDisabled={busy || unresolved || !checked}
      className="key-operation-modal"
      width={560}
    >
      {content}
    </Modal>
  ) : (
    content
  );
}
