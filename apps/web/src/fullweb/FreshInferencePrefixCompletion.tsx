import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { bootstrapJourneyText as j, bootstrapPrefixCodeText } from '../i18n/bootstrapJourney';
import { accessInfoText as a } from '../i18n/freshAccessInfo';
import { shellText as s } from '../i18n/consoleShell';
import {
  buildPrefixPreviewUrls,
  generateRandomAccessPrefix,
  resolveBrowserOrigin,
  validatePrefixPair,
} from './accessPrefix';
import type { FreshOperation } from './bootstrapTransport';
import { AccessInfoSummary } from './FreshAccessInfo';
import { FullWebIcon } from './FullWebIcon';
import { PublicApiError, type FullWebCredential, type PublicApiClient } from './publicApi';
import { useFullWebLanguage } from './useFullWebLanguage';
function completionErrorText(err: unknown): string {
  if (err instanceof PublicApiError && err.code) {
    const prefixMessage = bootstrapPrefixCodeText(err.code);
    if (prefixMessage) return prefixMessage;
    if (err.code === 'routing_unavailable') return a('notReadyRoutingDesc');
  }
  return j('setupError');
}
function validOptionalToken(value: unknown): boolean {
  return (
    value === undefined ||
    (typeof value === 'string' && (value.length === 0 || value.trim().length > 0))
  );
}

function validOwnerIdentity(identity: FreshOperation['identity'] | undefined): boolean {
  if (!identity || typeof identity !== 'object') return false;
  if (
    typeof identity.installationId !== 'string' ||
    !identity.installationId.trim() ||
    typeof identity.generation !== 'string' ||
    !identity.generation.trim() ||
    typeof identity.operationId !== 'string' ||
    !identity.operationId.trim() ||
    !validOptionalToken(identity.admissionId) ||
    !validOptionalToken(identity.databasePath)
  ) {
    return false;
  }
  return Boolean(identity.admissionId) === Boolean(identity.databasePath);
}

function sameCompletionOwner(
  a: FreshOperation['identity'] | undefined,
  b: FreshOperation['identity'] | undefined
): boolean {
  if (!validOwnerIdentity(a) || !validOwnerIdentity(b) || !a || !b) return false;
  return (
    a.installationId === b.installationId &&
    a.generation === b.generation &&
    a.operationId === b.operationId &&
    (a.admissionId ?? '') === (b.admissionId ?? '') &&
    (a.databasePath ?? '') === (b.databasePath ?? '')
  );
}

function validCompletionOperationBase(
  next: FreshOperation | null | undefined,
  currentOp: FreshOperation,
  expectedControl: string
): next is FreshOperation {
  const minRoutingRevision = currentOp.routing?.revision ?? 1;
  return (
    !!next &&
    sameCompletionOwner(next.identity, currentOp.identity) &&
    Number.isSafeInteger(next.revision) &&
    next.revision >= currentOp.revision &&
    next.adminInitialized === true &&
    next.productComplete === true &&
    next.routing?.committed === true &&
    next.routing.stage === 'committed' &&
    next.routing.operationId === next.identity.operationId &&
    next.routing.revision !== undefined &&
    Number.isSafeInteger(next.routing.revision) &&
    next.routing.revision >= minRoutingRevision &&
    next.routing.target === expectedControl
  );
}

export interface InferencePrefixCompletionState {
  required: boolean;
  mode: string;
  controlBasePath: string;
  inferenceBasePath: string;
  sharedEntry: boolean;
  inferenceCommitted: boolean;
  suggestedInferenceBasePath?: string;
}

export interface FreshInferencePrefixCompletionProps {
  client: PublicApiClient;
  credential: string;
  operation: FreshOperation;
  onOperation: (next: FreshOperation) => boolean | void;
  onUnauthorized: () => void;
  onContinue: () => void;
}

export function FreshInferencePrefixCompletion({
  client,
  credential,
  operation,
  onOperation,
  onUnauthorized,
  onContinue,
}: FreshInferencePrefixCompletionProps) {
  useFullWebLanguage();
  const [completion, setCompletion] = useState<InferencePrefixCompletionState | null>(null);
  const [inferencePath, setInferencePath] = useState('');
  const [sharedEntry, setSharedEntry] = useState(false);
  const [loading, setLoading] = useState(true);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const alive = useRef(true);
  const inFlight = useRef(false);
  const initializedFromServer = useRef(false);
  const confirmedOperation = useRef(false);
  const unauthorized = useRef(onUnauthorized);
  unauthorized.current = onUnauthorized;
  const onOperationRef = useRef(onOperation);
  onOperationRef.current = onOperation;
  const onContinueRef = useRef(onContinue);
  onContinueRef.current = onContinue;
  const operationRef = useRef(operation);
  if (
    validOwnerIdentity(operation.identity) &&
    (!validOwnerIdentity(operationRef.current.identity) ||
      (sameCompletionOwner(operation.identity, operationRef.current.identity) &&
        Number.isSafeInteger(operation.revision) &&
        operation.revision >= operationRef.current.revision &&
        (operation.routing?.revision ?? 0) >= (operationRef.current.routing?.revision ?? 0)))
  ) {
    operationRef.current = operation;
  }
  const auth = useMemo<FullWebCredential>(
    () => ({ kind: 'admin', value: credential }),
    [credential]
  );

  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);

  const loadCompletion = useCallback(
    async (options?: {
      preserveError?: boolean;
    }): Promise<InferencePrefixCompletionState | null> => {
      setLoading(true);
      if (!options?.preserveError) setError(null);
      try {
        const currentOp = operationRef.current;
        const next = await client.get<InferencePrefixCompletionState>(
          'installation/access/completion',
          auth
        );
        if (!alive.current) return null;
        if (
          !next ||
          typeof next.inferenceCommitted !== 'boolean' ||
          !next.controlBasePath ||
          (currentOp.routing?.target && next.controlBasePath !== currentOp.routing.target) ||
          (next.inferenceCommitted && !next.inferenceBasePath) ||
          (Boolean(next.sharedEntry) &&
            Boolean(next.inferenceBasePath) &&
            next.inferenceBasePath !== next.controlBasePath)
        ) {
          throw new Error('canonical access completion is invalid');
        }
        setCompletion(next);
        const hasPersistedChoice =
          Boolean(next.inferenceBasePath) || next.inferenceCommitted || next.required === false;
        if (!initializedFromServer.current || hasPersistedChoice) {
          setSharedEntry(Boolean(next.sharedEntry));
        }
        if (
          !initializedFromServer.current ||
          Boolean(next.inferenceBasePath) ||
          next.inferenceCommitted
        ) {
          const control = next.controlBasePath || currentOp.routing.target || '';
          const candidate =
            next.inferenceBasePath ||
            next.suggestedInferenceBasePath ||
            currentOp.routing.suggestedInferenceBasePath ||
            generateRandomAccessPrefix(control ? [control] : []);
          setInferencePath(candidate);
          initializedFromServer.current = true;
        }
        return next;
      } catch (err) {
        if (!alive.current) return null;
        if (err instanceof PublicApiError && err.kind === 'authentication') {
          unauthorized.current();
          return null;
        }
        if (!options?.preserveError) setError(j('inferenceCompletionLoadError'));
        return null;
      } finally {
        if (alive.current) setLoading(false);
      }
    },
    [auth, client]
  );

  useEffect(() => {
    void loadCompletion();
  }, [loadCompletion]);

  const controlBasePath = completion?.controlBasePath || operation.routing.target || '';
  const committed = Boolean(completion && (!completion.required || completion.inferenceCommitted));
  const staged = Boolean(completion && completion.inferenceBasePath && !committed);
  const effectiveInferencePath = sharedEntry ? controlBasePath : inferencePath;
  const preview = buildPrefixPreviewUrls(
    resolveBrowserOrigin(),
    controlBasePath,
    committed ? completion?.inferenceBasePath || effectiveInferencePath : effectiveInferencePath
  );

  async function confirmOperation(saved: InferencePrefixCompletionState) {
    const currentOp = operationRef.current;
    const expectedControl = saved.controlBasePath || currentOp.routing.target || '';
    const next = await client.get<FreshOperation>('operation', auth);
    if (
      !validCompletionOperationBase(next, currentOp, expectedControl) ||
      ((saved.required !== false || saved.inferenceCommitted === true) &&
        (next.routing.inferenceCommitted !== true ||
          next.routing.inferenceTarget !== saved.inferenceBasePath ||
          Boolean(next.routing.sharedEntry) !== Boolean(saved.sharedEntry) ||
          next.routing.missingInferenceCompletionRequired === true))
    ) {
      throw new Error('canonical access completion is not confirmed');
    }
    if (!alive.current) return;
    const accepted = onOperationRef.current(next);
    if (accepted === false) {
      throw new Error('canonical access completion is not confirmed');
    }
    operationRef.current = next;
    confirmedOperation.current = true;
  }

  async function enterConsole() {
    if (inFlight.current || !completion) return;
    inFlight.current = true;
    setSubmitting(true);
    setError(null);
    try {
      if (!confirmedOperation.current) await confirmOperation(completion);
      if (alive.current) onContinueRef.current();
    } catch (err) {
      if (!alive.current) return;
      if (err instanceof PublicApiError && err.kind === 'authentication') unauthorized.current();
      else setError(j('inferenceCompletionLoadError'));
    } finally {
      inFlight.current = false;
      if (alive.current) setSubmitting(false);
    }
  }

  async function handleSubmit() {
    if (inFlight.current || !completion || committed) return;
    const check = validatePrefixPair({
      controlBasePath,
      inferenceBasePath: effectiveInferencePath,
      sharedEntry,
      requireInference: true,
    });
    if (!check.valid) {
      const code = check.controlError || check.inferenceError || check.pairError;
      setError(bootstrapPrefixCodeText(code || undefined) || j('invalidSetup'));
      return;
    }

    inFlight.current = true;
    setSubmitting(true);
    setError(null);
    let saved: InferencePrefixCompletionState | null = null;
    try {
      const posted = await client.post<InferencePrefixCompletionState>(
        'installation/access/completion',
        { inferenceBasePath: effectiveInferencePath, sharedEntry },
        auth
      );
      if (
        !posted ||
        posted.inferenceCommitted !== true ||
        !posted.inferenceBasePath ||
        posted.controlBasePath !== controlBasePath
      ) {
        throw new Error('canonical access completion is invalid');
      }
      saved = posted;
      if (!alive.current) return;
      setCompletion(saved);
      setInferencePath(saved.inferenceBasePath);
      setSharedEntry(Boolean(saved.sharedEntry));
      await confirmOperation(saved);
    } catch (err) {
      if (!alive.current) return;
      if (err instanceof PublicApiError && err.kind === 'authentication') {
        unauthorized.current();
        return;
      }
      if (saved !== null) {
        setError(j('inferenceCompletionLoadError'));
        return;
      }
      if (err instanceof PublicApiError && err.code === 'inference_prefix_already_committed') {
        const conflictMessage = bootstrapPrefixCodeText(err.code) || j('setupError');
        setError(conflictMessage);
        await loadCompletion({ preserveError: true });
        return;
      }
      if (
        err instanceof PublicApiError &&
        err.kind === 'http' &&
        err.status !== undefined &&
        err.status >= 400 &&
        err.status < 500 &&
        err.code !== 'routing_conflict'
      ) {
        setError(completionErrorText(err));
        return;
      }
      const isHttpFailure = err instanceof PublicApiError && err.kind === 'http';
      const isVerificationFailed =
        err instanceof PublicApiError && err.code === 'routing_verification_failed';
      const isRoutingConflict =
        err instanceof PublicApiError && err.code === 'routing_conflict';
      const errorMessage = completionErrorText(err);
      if (isHttpFailure) {
        setError(errorMessage);
      }

      const recovered = await loadCompletion({ preserveError: true });
      if (!alive.current) return;
      if (!recovered) {
        if (!isHttpFailure) {
          setError(errorMessage);
        }
        return;
      }

      const recoverCommitted = async (committedState: InferencePrefixCompletionState) => {
        const committedSameIntent =
          committedState.controlBasePath === controlBasePath &&
          committedState.inferenceBasePath === effectiveInferencePath &&
          Boolean(committedState.sharedEntry) === sharedEntry;
        if (isVerificationFailed || isRoutingConflict || (isHttpFailure && committedSameIntent)) {
          setError(errorMessage);
          try {
            await confirmOperation(committedState);
          } catch (confirmErr) {
            if (!alive.current) return;
            if (confirmErr instanceof PublicApiError && confirmErr.kind === 'authentication') {
              unauthorized.current();
              return;
            }
            setError(j('inferenceCompletionLoadError'));
          }
          return;
        }
        if (committedSameIntent) {
          setError(null);
          try {
            await confirmOperation(committedState);
          } catch (confirmErr) {
            if (!alive.current) return;
            if (confirmErr instanceof PublicApiError && confirmErr.kind === 'authentication') {
              unauthorized.current();
              return;
            }
            setError(j('inferenceCompletionLoadError'));
          }
          return;
        }
        setError(
          bootstrapPrefixCodeText('inference_prefix_already_committed') || j('setupError')
        );
      };

      if (recovered.inferenceCommitted === true || recovered.required === false) {
        await recoverCommitted(recovered);
        return;
      }

      const sameIntent =
        recovered.controlBasePath === controlBasePath &&
        recovered.inferenceBasePath === effectiveInferencePath &&
        Boolean(recovered.sharedEntry) === sharedEntry;
      const stagedErrorMessage =
        !isVerificationFailed && !isRoutingConflict && recovered.inferenceBasePath && !sameIntent
          ? bootstrapPrefixCodeText('inference_prefix_already_committed') || j('setupError')
          : errorMessage;

      if (isVerificationFailed || isRoutingConflict || Boolean(recovered.inferenceBasePath)) {
        let stagedOp: FreshOperation | null = null;
        try {
          stagedOp = await client.get<FreshOperation>('operation', auth);
        } catch (opErr) {
          if (!alive.current) return;
          if (opErr instanceof PublicApiError && opErr.kind === 'authentication') {
            unauthorized.current();
            return;
          }
          setError(stagedErrorMessage);
          return;
        }
        if (!alive.current) return;
        const currentOp = operationRef.current;
        const expectedControl = recovered.controlBasePath || currentOp.routing.target || '';
        if (!validCompletionOperationBase(stagedOp, currentOp, expectedControl)) {
          setError(j('inferenceCompletionLoadError'));
          return;
        }
        if (stagedOp.routing.inferenceCommitted === true) {
          const committedRecovery = await loadCompletion({ preserveError: true });
          if (!alive.current) return;
          if (
            committedRecovery &&
            (committedRecovery.inferenceCommitted === true || committedRecovery.required === false)
          ) {
            await recoverCommitted(committedRecovery);
          } else {
            setError(j('inferenceCompletionLoadError'));
          }
          return;
        }
        if (
          stagedOp.routing.inferenceTarget &&
          (stagedOp.routing.inferenceTarget !== recovered.inferenceBasePath ||
            Boolean(stagedOp.routing.sharedEntry) !== Boolean(recovered.sharedEntry))
        ) {
          setError(j('inferenceCompletionLoadError'));
          return;
        }
        const accepted = onOperationRef.current(stagedOp);
        if (accepted === false) {
          setError(j('inferenceCompletionLoadError'));
          return;
        }
        operationRef.current = stagedOp;
        setError(stagedErrorMessage);
        return;
      }

      setError(stagedErrorMessage);
    } finally {
      inFlight.current = false;
      if (alive.current) setSubmitting(false);
    }
  }

  return (
    <section
      className="cpamp-installation"
      data-testid="fresh-inference-prefix-completion"
      aria-busy={loading || submitting}
    >
      <div className="wizard-card">
        <div className="journey-body">
          <div className={`journey-heading ${committed ? 'journey-success-heading' : ''}`}>
            {committed ? (
              <div className="journey-success-symbol">
                <FullWebIcon name="check" size={28} />
              </div>
            ) : (
              <span className="journey-tile-icon">
                <FullWebIcon name="shield" size={30} />
              </span>
            )}
            <div>
              <h1 className="wizard-title">
                {committed ? j('inferenceCompletionCompleteTitle') : j('inferenceCompletionTitle')}
              </h1>
              <p className="wizard-desc">
                {committed ? j('inferenceCompletionCompleteHelp') : j('inferenceCompletionHelp')}
              </p>
            </div>
          </div>

          {error && (
            <p role="alert" className="alert alert-warn" data-testid="inference-completion-error">
              {error}
            </p>
          )}

          {loading && !completion ? (
            <p className="journey-quiet" data-testid="inference-completion-loading">
              {s('pending')}
            </p>
          ) : !completion ? (
            <div className="header-actions">
              <button
                type="button"
                className="btn btn-secondary"
                data-testid="inference-completion-retry"
                onClick={() => void loadCompletion()}
              >
                {s('refresh')}
              </button>
            </div>
          ) : committed ? (
            <>
              <div className="info-card" data-testid="inference-completion-committed-summary">
                <div className="info-row">
                  <span className="info-key">{j('inferenceCompletionControlLabel')}</span>
                  <code className="info-val">{controlBasePath}</code>
                </div>
                <div className="info-row">
                  <span className="info-key">{j('inferencePrefix')}</span>
                  <code className="info-val">{completion.inferenceBasePath || effectiveInferencePath}</code>
                </div>
                <div className="info-row">
                  <span className="info-key">{j('sharedPrefixBadge')}</span>
                  <span className="badge badge-emerald">
                    {completion.sharedEntry ? j('sharedPrefixBadge') : j('independentPrefixBadge')}
                  </span>
                </div>
              </div>
              <AccessInfoSummary client={client} auth={auth} onUnauthorized={onUnauthorized} />
            </>
          ) : (
            <div className="journey-configuration">
              <p className="alert alert-info">{j('inferenceCompletionReadOnlyNote')}</p>

              <div className="journey-field">
                <span className="form-label">{j('inferenceCompletionControlLabel')}</span>
                <div className="info-card" data-testid="inference-completion-control">
                  <div className="info-row">
                    <span className="info-key">{j('managementPrefix')}</span>
                    <code className="info-val">{controlBasePath}</code>
                  </div>
                </div>
              </div>

              <div className="journey-field">
                <label className="journey-checkline" htmlFor="completion-shared-prefix">
                  <input
                    id="completion-shared-prefix"
                    data-testid="inference-completion-shared-checkbox"
                    type="checkbox"
                    checked={sharedEntry}
                    disabled={submitting || staged}
                    onChange={(e) => {
                      const checked = e.target.checked;
                      setSharedEntry(checked);
                      if (!checked && inferencePath === controlBasePath) {
                        setInferencePath(
                          generateRandomAccessPrefix(controlBasePath ? [controlBasePath] : [])
                        );
                      }
                      setError(null);
                    }}
                  />
                  <span>{j('sharedPrefixToggle')}</span>
                </label>
                <p className="journey-help">{j('sharedPrefixHelp')}</p>
              </div>

              <div className="journey-field">
                <label className="form-label" htmlFor="completion-inference-path">
                  {j('inferencePrefix')}
                </label>
                <div className="input-wrap">
                  <input
                    id="completion-inference-path"
                    data-testid="inference-completion-input"
                    className="form-input"
                    value={effectiveInferencePath}
                    disabled={submitting || sharedEntry || staged}
                    onChange={(e) => {
                      setInferencePath(e.target.value);
                      setError(null);
                    }}
                  />
                  <div className="input-actions">
                    <button
                      type="button"
                      className="btn btn-secondary btn-sm"
                      data-testid="inference-completion-randomize"
                      disabled={submitting || sharedEntry || staged}
                      onClick={() => {
                        setInferencePath(
                          generateRandomAccessPrefix(controlBasePath ? [controlBasePath] : [])
                        );
                        setError(null);
                      }}
                    >
                      {j('randomizePrefix')}
                    </button>
                  </div>
                </div>
                <p className="journey-help">{j('inferencePrefixHelp')}</p>
              </div>

              <div className="info-card" data-testid="inference-completion-preview">
                <div className="info-row">
                  <span className="info-key">{j('finalUrlsPreview')}</span>
                  <span className="badge badge-neutral">
                    {sharedEntry ? j('sharedPrefixBadge') : j('independentPrefixBadge')}
                  </span>
                </div>
                <div className="info-row">
                  <span className="info-key">{j('previewConsoleUrl')}</span>
                  <code className="info-val">{preview.consoleUrl}</code>
                </div>
                <div className="info-row">
                  <span className="info-key">{j('previewManagementApiUrl')}</span>
                  <code className="info-val">{preview.managementApiUrl}</code>
                </div>
                <div className="info-row">
                  <span className="info-key">{j('previewInferenceUrl')}</span>
                  <code className="info-val">{preview.inferenceUrl}</code>
                </div>
              </div>
            </div>
          )}
        </div>
        <footer className="journey-footer">
          {committed ? (
            <button
              type="button"
              className="btn btn-primary"
              data-testid="inference-completion-enter-console"
              disabled={submitting}
              onClick={() => void enterConsole()}
            >
              {s('console')}
              <FullWebIcon name="arrowRight" size={20} />
            </button>
          ) : (
            completion && (
              <button
                type="button"
                className="btn btn-primary"
                data-testid="inference-completion-submit"
                disabled={submitting}
                onClick={() => void handleSubmit()}
              >
                {submitting ? j('inferenceCompletionSubmitting') : j('inferenceCompletionSubmit')}
                <FullWebIcon name="arrowRight" size={20} />
              </button>
            )
          )}
        </footer>
      </div>
    </section>
  );
}
