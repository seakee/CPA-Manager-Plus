import { useEffect, useRef, useState } from 'react';
import { fullWebText as t } from '../i18n/fullweb';
import { keyWorkspaceText as k } from '../i18n/freshKeyWorkspace';
import { Modal } from '../components/ui/Modal';
import { FullWebIcon } from './FullWebIcon';
import { AccessInfoSummary, type AccessInfo } from './FreshAccessInfo';
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
  type KeyOperationResult,
} from './freshKeyMetadata';

interface Props {
  open: boolean;
  visible?: boolean;
  target: Key | null;
  client: PublicApiClient;
  credential: string;
  accessInfo?: AccessInfo;
  reloadToken?: number;
  onUnauthorized: () => void;
  onPending?: (pending: boolean) => void;
  onLatestOperation?: (result: KeyOperationResult | null) => void;
  onRebind?: () => void;
  onBlocker?: (code: string, detail?: string) => void;
  onExternalError?: (code: string, detail?: string) => void;
  onChanged: () => void;
  onClose: () => void;
}

type Stage = 'confirm' | 'recovery' | 'done';

interface RegenerateReply {
  key: Key;
  createdSecret?: string;
}

interface PersistedSecretReply {
  secret: string;
}

function randomRequestId(): string {
  return Array.from(crypto.getRandomValues(new Uint8Array(16)), (b) =>
    b.toString(16).padStart(2, '0')
  ).join('');
}

function parseRegenerateReply(raw: unknown): RegenerateReply | null {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return null;
  const candidate = raw as { key?: unknown; createdSecret?: unknown };
  if (!candidate.key || typeof candidate.key !== 'object' || !validKey(candidate.key as Key)) {
    return null;
  }
  const key = candidate.key as Key;
  if (candidate.createdSecret !== undefined) {
    if (typeof candidate.createdSecret !== 'string') return null;
    if (candidate.createdSecret !== '' && !validSavedSecret(key, candidate.createdSecret)) {
      return null;
    }
    return { key, createdSecret: candidate.createdSecret || undefined };
  }
  return { key };
}

export function FreshKeyRegenerate({
  open,
  visible = true,
  target,
  client,
  credential,
  accessInfo,
  reloadToken,
  onUnauthorized,
  onPending,
  onLatestOperation,
  onRebind,
  onBlocker,
  onExternalError,
  onChanged,
  onClose,
}: Props) {
  const [stage, setStage] = useState<Stage>('confirm');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [blockerCode, setBlockerCode] = useState('');
  const [recoveryResult, setRecoveryResult] = useState<KeyOperationResult | null>(null);
  const [doneKey, setDoneKey] = useState<Key | null>(null);
  const [newSecret, setNewSecret] = useState('');
  const [unverifiedOpId, setUnverifiedOpId] = useState('');
  const [copyFeedback, setCopyFeedback] = useState<'' | 'copied' | 'failed'>('');
  const alive = useRef(true);
  const activeRun = useRef(false);
  const sessionEpochRef = useRef(0);
  const requestIdRef = useRef('');
  const prevOpenRef = useRef(false);
  const prevTargetIdRef = useRef('');
  const prevCredentialRef = useRef(credential);
  const auth = { kind: 'admin' as const, value: credential };

  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
      sessionEpochRef.current += 1;
    };
  }, []);

  useEffect(() => {
    const targetId = target?.id ?? '';
    const credentialChanged = credential !== prevCredentialRef.current;
    const justOpened = open && !prevOpenRef.current;
    const switchedTarget = open && targetId !== prevTargetIdRef.current;
    prevCredentialRef.current = credential;
    prevOpenRef.current = open;
    prevTargetIdRef.current = targetId;

    if (!open) {
      sessionEpochRef.current += 1;
      activeRun.current = false;
      requestIdRef.current = '';
      setUnverifiedOpId('');
      setStage('confirm');
      setBusy(false);
      setError('');
      setBlockerCode('');
      setRecoveryResult(null);
      setDoneKey(null);
      setNewSecret('');
      setCopyFeedback('');
      return;
    }

    if (justOpened || switchedTarget || credentialChanged) {
      sessionEpochRef.current += 1;
      activeRun.current = false;
      requestIdRef.current = randomRequestId();
      setUnverifiedOpId('');
      setStage('confirm');
      setBusy(false);
      setError('');
      setBlockerCode('');
      setRecoveryResult(null);
      setDoneKey(null);
      setNewSecret('');
      setCopyFeedback('');
    }
  }, [open, target?.id, credential]);

  const displayKey = doneKey ?? target;
  const targetDisplayName = displayKey
    ? keyDisplayName(displayKey, k('installerOrigin'), k('adoptedOrigin'), t('keysUnnamed'))
    : t('keysUnnamed');

  async function loadFreshKey(targetId: string, epoch: number): Promise<Key | null> {
    try {
      const latest = await client.get<Key>(`api-keys/${targetId}`, auth);
      if (!alive.current || sessionEpochRef.current !== epoch) return null;
      return validKey(latest) && latest.id === targetId ? latest : null;
    } catch (err) {
      if (
        alive.current &&
        sessionEpochRef.current === epoch &&
        err instanceof PublicApiError &&
        err.kind === 'authentication'
      ) {
        onUnauthorized();
      }
      return null;
    }
  }

  function applyFailureEnvelope(
    err: unknown,
    fallbackMessage: string,
    attemptedRequestId?: string,
    preserveRecovery = false
  ) {
    if (!(err instanceof PublicApiError)) {
      if (attemptedRequestId) {
        setUnverifiedOpId(attemptedRequestId);
        setStage('recovery');
        onPending?.(true);
        onChanged();
        setError(k('regenerateNetworkUncertainHelp'));
        return;
      }
      setError(fallbackMessage);
      return;
    }
    if (err.kind === 'authentication') {
      onUnauthorized();
      return;
    }
    const code = err.code ?? '';
    if (code) {
      setBlockerCode(code);
      if (blockerOf(code)) {
        onExternalError?.(code, err.detail);
        onBlocker?.(code, err.detail);
      }
    }
    const parsedOp =
      err.operationResult && validKeyOperationResult(err.operationResult as KeyOperationResult)
        ? (err.operationResult as KeyOperationResult)
        : null;
    const parsedKey =
      err.key && typeof err.key === 'object' && validKey(err.key as Key) ? (err.key as Key) : null;

    if (parsedOp?.operation && parsedOp.operation.mode === 'rotate') {
      const outcome = parsedOp.operation.outcome;
      onLatestOperation?.(parsedOp);
      if (outcome === 'aborted') {
        requestIdRef.current = randomRequestId();
        setUnverifiedOpId('');
        setRecoveryResult(null);
        setStage('confirm');
        onPending?.(false);
        onChanged();
        setError(code ? k(effectBlockerTextKey(code)) : k('regenerateAbortedHelp'));
        return;
      }
      if (outcome === 'committed') {
        requestIdRef.current = '';
        setUnverifiedOpId('');
        setRecoveryResult(null);
        setDoneKey(parsedKey ?? target);
        setNewSecret('');
        setStage('done');
        onPending?.(false);
        onChanged();
        return;
      }
      // Unsettled rotate operation ('pending' or 'unknown'): keep requestIdRef.current stable.
      setUnverifiedOpId('');
      setRecoveryResult(parsedOp);
      if (parsedKey) setDoneKey(parsedKey);
      setStage('recovery');
      onPending?.(true);
      onChanged();
      setError(
        code
          ? k(effectBlockerTextKey(code))
          : outcome === 'unknown'
            ? t('keysOperationUnknown')
            : t('keysOperationPending')
      );
      return;
    }

    if (err.status === 400 && !preserveRecovery && stage === 'confirm' && !recoveryResult) {
      // Pre-write validation error: safe to rotate requestId for next submit.
      requestIdRef.current = randomRequestId();
      setUnverifiedOpId('');
      onPending?.(false);
      setError(code ? k(effectBlockerTextKey(code)) : fallbackMessage);
      return;
    }

    if (attemptedRequestId && (err.status === undefined || err.status >= 500)) {
      setUnverifiedOpId(attemptedRequestId);
      setStage('recovery');
      onPending?.(true);
      onChanged();
      setError(code ? k(effectBlockerTextKey(code)) : k('regenerateNetworkUncertainHelp'));
      return;
    }

    setError(code ? k(effectBlockerTextKey(code)) : fallbackMessage);
  }

  async function startRegenerate() {
    if (!target || activeRun.current) return;
    if (!requestIdRef.current) {
      requestIdRef.current = randomRequestId();
    }
    const epoch = sessionEpochRef.current;
    const targetId = target.id;
    const requestId = requestIdRef.current;
    activeRun.current = true;
    setBusy(true);
    setError('');
    setBlockerCode('');
    setCopyFeedback('');

    try {
      const raw = await client.post<RegenerateReply>(
        `api-keys/${targetId}/regenerate`,
        { requestId },
        auth
      );
      if (!alive.current || sessionEpochRef.current !== epoch) return;
      const parsed = parseRegenerateReply(raw);
      if (!parsed || parsed.key.id !== targetId) {
        throw new Error('invalid regenerate response');
      }
      requestIdRef.current = '';
      setUnverifiedOpId('');
      setRecoveryResult(null);
      setDoneKey(parsed.key);
      setNewSecret(parsed.createdSecret ?? '');
      setStage('done');
      onLatestOperation?.(null);
      onPending?.(false);
      onChanged();
    } catch (err) {
      if (!alive.current || sessionEpochRef.current !== epoch) return;
      applyFailureEnvelope(err, k('regenerateCreateFailed'), requestId);
    } finally {
      if (sessionEpochRef.current === epoch) {
        activeRun.current = false;
        if (alive.current) setBusy(false);
      }
    }
  }

  async function checkAndContinue() {
    if (!target || activeRun.current) return;
    const opId = recoveryResult?.operation?.id || unverifiedOpId;
    if (!opId) {
      await startRegenerate();
      return;
    }
    const epoch = sessionEpochRef.current;
    const targetId = target.id;
    activeRun.current = true;
    setBusy(true);
    setError('');
    setBlockerCode('');
    let observedChecked = Boolean(recoveryResult);
    try {
      const checked = await client.get<KeyOperationResult>(
        `api-keys/operations/${opId}`,
        auth
      );
      if (!alive.current || sessionEpochRef.current !== epoch) return;
      if (!validKeyOperationResult(checked)) {
        throw new Error('invalid operation response');
      }
      onLatestOperation?.(checked);
      if (!checked.operation) {
        requestIdRef.current = opId;
        setUnverifiedOpId('');
        setRecoveryResult(null);
        setStage('confirm');
        onPending?.(false);
        setError(k('operationNotRecorded'));
        return;
      }
      if (checked.operation.outcome === 'aborted') {
        requestIdRef.current = randomRequestId();
        setUnverifiedOpId('');
        setRecoveryResult(null);
        setStage('confirm');
        onPending?.(false);
        onChanged();
        setError(k('regenerateAbortedHelp'));
        return;
      }
      if (checked.operation.outcome === 'committed') {
        const freshKey = await loadFreshKey(targetId, epoch);
        if (!alive.current || sessionEpochRef.current !== epoch) return;
        requestIdRef.current = '';
        setUnverifiedOpId('');
        setRecoveryResult(null);
        setDoneKey(freshKey);
        setNewSecret('');
        setStage('done');
        onPending?.(false);
        onChanged();
        return;
      }
      observedChecked = true;
      setUnverifiedOpId('');
      setRecoveryResult(checked);
      if (checked.nextStep) {
        const continued = await client.post<KeyOperationResult>(
          `api-keys/operations/${opId}/continue`,
          {},
          auth
        );
        if (!alive.current || sessionEpochRef.current !== epoch) return;
        if (!validKeyOperationResult(continued)) {
          throw new Error('invalid continue response');
        }
        onLatestOperation?.(continued);
        if (continued.operation?.outcome === 'committed') {
          const freshKey = await loadFreshKey(targetId, epoch);
          if (!alive.current || sessionEpochRef.current !== epoch) return;
          requestIdRef.current = '';
          setUnverifiedOpId('');
          setRecoveryResult(null);
          setDoneKey(freshKey);
          setNewSecret('');
          setStage('done');
          onPending?.(false);
          onChanged();
          return;
        }
        if (continued.operation?.outcome === 'aborted') {
          requestIdRef.current = randomRequestId();
          setUnverifiedOpId('');
          setRecoveryResult(null);
          setStage('confirm');
          onPending?.(false);
          onChanged();
          setError(k('regenerateAbortedHelp'));
          return;
        }
        setUnverifiedOpId('');
        setRecoveryResult(continued);
        setStage('recovery');
        onPending?.(true);
        onChanged();
        setError(
          continued.operation?.outcome === 'unknown'
            ? t('keysOperationUnknown')
            : t('keysOperationPending')
        );
        return;
      }
      setUnverifiedOpId('');
      setRecoveryResult(checked);
      setStage('recovery');
      onPending?.(true);
      setError(
        checked.operation.outcome === 'unknown'
          ? t('keysOperationUnknown')
          : t('keysOperationPending')
      );
    } catch (err) {
      if (!alive.current || sessionEpochRef.current !== epoch) return;
      if (!observedChecked && unverifiedOpId && err instanceof PublicApiError && err.status === 404) {
        requestIdRef.current = opId;
        setUnverifiedOpId('');
        setRecoveryResult(null);
        setStage('confirm');
        onPending?.(false);
        setError(k('operationNotRecorded'));
        return;
      }
      applyFailureEnvelope(err, t('keysUnavailable'), undefined, observedChecked);
    } finally {
      if (sessionEpochRef.current === epoch) {
        activeRun.current = false;
        if (alive.current) setBusy(false);
      }
    }
  }

  async function copyKnownSecret() {
    if (!newSecret) return;
    const epoch = sessionEpochRef.current;
    try {
      await navigator.clipboard.writeText(newSecret);
      if (alive.current && sessionEpochRef.current === epoch) setCopyFeedback('copied');
    } catch {
      if (alive.current && sessionEpochRef.current === epoch) setCopyFeedback('failed');
    }
  }

  async function fetchAndCopyNewSecret() {
    if (!target || activeRun.current || busy) return;
    const epoch = sessionEpochRef.current;
    const targetId = target.id;
    activeRun.current = true;
    setBusy(true);
    setCopyFeedback('');
    setError('');
    try {
      let basisKey = doneKey && doneKey.id === targetId && (doneKey.lastRotatedAtMs ?? 0) > 0 ? doneKey : null;
      if (!basisKey) {
        basisKey = await loadFreshKey(targetId, epoch);
        if (!alive.current || sessionEpochRef.current !== epoch) return;
        if (basisKey) setDoneKey(basisKey);
      }
      const response = await client.post<PersistedSecretReply>(
        `api-keys/${targetId}/secret`,
        {},
        auth
      );
      if (!alive.current || sessionEpochRef.current !== epoch) return;
      const validationTarget = basisKey ?? (target.id === targetId && (target.lastRotatedAtMs ?? 0) > 0 ? target : null);
      if (
        !validationTarget ||
        !response ||
        typeof response.secret !== 'string' ||
        !validSavedSecret(validationTarget, response.secret)
      ) {
        throw new Error('invalid secret response');
      }
      setNewSecret(response.secret);
      try {
        await navigator.clipboard.writeText(response.secret);
        if (alive.current && sessionEpochRef.current === epoch) setCopyFeedback('copied');
      } catch {
        if (alive.current && sessionEpochRef.current === epoch) setCopyFeedback('failed');
      }
    } catch (err) {
      if (!alive.current || sessionEpochRef.current !== epoch) return;
      if (err instanceof PublicApiError && err.kind === 'authentication') {
        onUnauthorized();
        return;
      }
      setCopyFeedback('failed');
    } finally {
      if (sessionEpochRef.current === epoch) {
        activeRun.current = false;
        if (alive.current) setBusy(false);
      }
    }
  }

  const canRebind =
    (blockerCode === 'external_management_key_rejected' ||
      blockerCode === 'external_target_moved') &&
    Boolean(onRebind);

  const modalFooter =
    stage === 'confirm' ? (
      <>
        <button type="button" className="btn btn-secondary" onClick={onClose} disabled={busy}>
          {k('cancel')}
        </button>
        <button
          type="button"
          className="btn btn-primary"
          data-testid="confirm-regenerate-key"
          onClick={() => void startRegenerate()}
          disabled={busy || !target}
        >
          {busy ? k('regenerateRunningTitle') : k('regenerateConfirmAction')}
        </button>
      </>
    ) : stage === 'recovery' ? (
      <>
        <button type="button" className="btn btn-secondary" onClick={onClose} disabled={busy}>
          {k('closeDialog')}
        </button>
        <button
          type="button"
          className="btn btn-primary"
          data-testid="regenerate-check-continue"
          onClick={() => void checkAndContinue()}
          disabled={busy}
        >
          {busy ? k('regenerateRunningTitle') : k('checkAndContinueAction')}
        </button>
      </>
    ) : (
      <button type="button" className="btn btn-primary" onClick={onClose} disabled={busy}>
        {t('keysDismiss')}
      </button>
    );

  return (
    <Modal
      open={open && visible && !!target}
      onClose={onClose}
      closeDisabled={busy}
      title={k('regenerateModalTitle')}
      width={520}
      footer={modalFooter}
    >
      <section className="key-revocation-workspace is-modal" aria-busy={busy}>
        {displayKey && (
          <div className="key-operation-resource" data-testid="key-regenerate-target-card">
            <span className="key-resource-icon" aria-hidden="true">
              <FullWebIcon name="key" size={16} />
            </span>
            <div className="key-operation-resource-body">
              <span className="key-operation-resource-label">{k('operationTargetLabel')}</span>
              <strong>{targetDisplayName}</strong>
              <code>{displayKey.maskedPreview}</code>
            </div>
          </div>
        )}

        {stage === 'confirm' && (
          <div className="key-notice key-notice-warning" role="note">
            <FullWebIcon name="alert" />
            <div>
              <p>{k('regenerateWarning')}</p>
            </div>
          </div>
        )}

        {stage === 'recovery' && (
          <div
            className="key-operation-status is-pending"
            role="status"
            data-testid="regenerate-recovery-state"
          >
            <div className="key-operation-banner is-aborted">
              <span className="key-operation-banner-icon">
                <FullWebIcon name="alert" size={18} />
              </span>
              <div className="key-operation-banner-body">
                <strong>{k('regenerateRecoveryTitle')}</strong>
                <p>{k('regenerateRecoveryHelp')}</p>
              </div>
            </div>
            {recoveryResult?.operation &&
              !isTerminalKeyOperationOutcome(recoveryResult.operation.outcome) && (
                <ul className="key-operation-steps">
                  {recoveryResult.operation.steps.map((step) => (
                    <li
                      key={step.name}
                      className={`key-operation-step is-${step.state}`}
                      data-testid={`regenerate-step-${step.name}`}
                      data-state={step.state}
                    >
                      <span className="key-operation-step-label">
                        {k(step.name === 'rotate' ? 'operationStepRotate' : 'operationStepRevoke')}
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
              )}
            {((recoveryResult?.operation &&
              !isTerminalKeyOperationOutcome(recoveryResult.operation.outcome) &&
              recoveryResult.operation.id) ||
              unverifiedOpId) && (
              <p className="key-field-help">
                {t('keysOperationId')}:{' '}
                <code>{recoveryResult?.operation?.id || unverifiedOpId}</code>
              </p>
            )}
          </div>
        )}

        {error && (
          <div className="key-notice key-notice-error" role="alert">
            <FullWebIcon name="alert" />
            <div>
              <p>{error}</p>
              {canRebind && (
                <div className="key-operation-actions">
                  <button
                    type="button"
                    className="btn btn-secondary btn-sm"
                    onClick={onRebind}
                    disabled={busy}
                  >
                    {k('rebindConnection')}
                  </button>
                </div>
              )}
            </div>
          </div>
        )}

        {stage === 'done' && (
          <div className="key-create-result-stack" role="status" data-testid="regenerate-done-state">
            <div className="key-operation-banner is-committed">
              <span className="key-operation-banner-icon">
                <FullWebIcon name="check" size={18} />
              </span>
              <div className="key-operation-banner-body">
                <strong>{k('regenerateSuccessTitle')}</strong>
                <p>{newSecret ? k('regenerateSuccessHelp') : k('regenerateReplayHelp')}</p>
              </div>
            </div>

            <div className="key-secret-card">
              {newSecret && (
                <div className="key-secret-value">
                  <code data-testid="regenerated-client-secret">{newSecret}</code>
                </div>
              )}
              <div className="key-secret-card-actions">
                {newSecret ? (
                  <button
                    type="button"
                    className="btn btn-primary btn-sm"
                    aria-label={t('keysCopy')}
                    data-testid="copy-regenerated-client-secret"
                    onClick={() => void copyKnownSecret()}
                  >
                    <FullWebIcon name="copy" size={14} />
                    {t('keysCopy')}
                  </button>
                ) : (
                  <button
                    type="button"
                    className="btn btn-primary btn-sm"
                    aria-label={k('regenerateCopyNewSecret')}
                    data-testid="fetch-regenerated-client-secret"
                    disabled={busy}
                    onClick={() => void fetchAndCopyNewSecret()}
                  >
                    <FullWebIcon name="copy" size={14} />
                    {k('regenerateCopyNewSecret')}
                  </button>
                )}
                {copyFeedback && (
                  <span
                    role="status"
                    className={`key-copy-inline-status ${
                      copyFeedback === 'copied' ? 'is-copied' : 'is-failed'
                    }`}
                    data-testid="copy-regenerated-client-secret-feedback"
                  >
                    {t(copyFeedback === 'copied' ? 'keysCopied' : 'keysCopyFailed')}
                  </span>
                )}
              </div>
            </div>

            <div className="key-create-access-panel">
              <AccessInfoSummary
                client={client}
                auth={auth}
                onUnauthorized={onUnauthorized}
                info={accessInfo}
                reloadToken={reloadToken}
                compact
                clientOnly
                defaultProtocolsCollapsed
                testIdPrefix="regenerate-access-info"
              />
            </div>
          </div>
        )}
      </section>
    </Modal>
  );
}
