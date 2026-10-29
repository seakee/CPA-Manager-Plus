import { useEffect, useRef, useState } from 'react';
import { fullWebText as t } from '../i18n/fullweb';
import { shellText as s } from '../i18n/consoleShell';
import { bootstrapJourneyText as journey } from '../i18n/bootstrapJourney';
import { FullWebIcon, type FullWebIconName } from './FullWebIcon';
import { sameIdentity, type FreshOperation } from './bootstrapTransport';
import { parseAdoptionSummary } from './freshAdoption';
import { FreshAdoptFlow, type AdoptCarrier } from './FreshAdoptFlow';
import { PublicApiError, type createPublicApiClient } from './publicApi';
import { parseRuntimeJSON, validRuntimeGeneration } from './runtimeGeneration';

// Host observation is a fence for the existing strategy, never qualification
// or authority to select a different target after the checkpoint is persisted.
function hostTarget(value: string) {
  const status = parseRuntimeJSON(value) as {
    runtimeIdentity?: unknown;
    runtimeGeneration?: unknown;
    activeGatewayArtifact?: { artifactId?: string };
  } | null;
  if (
    !status ||
    typeof status.runtimeIdentity !== 'string' ||
    !status.runtimeIdentity.trim() ||
    status.runtimeIdentity.length > 256 ||
    !validRuntimeGeneration(status.runtimeGeneration) ||
    !/^sha256:[a-f0-9]{64}$/.test(status.activeGatewayArtifact?.artifactId || '')
  )
    throw new Error('Invalid host observation');
  return {
    expectedRuntimeIdentity: status.runtimeIdentity,
    expectedRuntimeGeneration: status.runtimeGeneration,
    expectedArtifactId: status.activeGatewayArtifact!.artifactId as string,
  };
}

type PendingManagedWrite = {
  revision: number;
  action: 'stage' | 'start' | 'complete';
  previousStage?: string;
};

function confirmsManagedWrite(next: FreshOperation, pending: PendingManagedWrite): boolean {
  if (next.revision <= pending.revision) return false;
  if (pending.action === 'complete') return next.productComplete === true;
  if (!validManagedCheckpoint(next.strategy)) return false;
  if (pending.action === 'stage') return true;
  // Re-reading the same staged/starting state is not a receipt for Start.
  return pending.previousStage === 'starting'
    ? next.strategy?.stage === 'committed'
    : next.strategy?.stage === 'starting' || next.strategy?.stage === 'committed';
}

function validManagedCheckpoint(checkpoint: FreshOperation['strategy']) {
  return (
    checkpoint?.strategy === 'new_managed' &&
    typeof checkpoint.targetIdentity === 'string' &&
    !!checkpoint.targetIdentity.trim() &&
    validRuntimeGeneration(checkpoint.targetGeneration) &&
    /^sha256:[a-f0-9]{64}$/.test(checkpoint.targetArtifactId || '') &&
    ['staged', 'starting', 'committed'].includes(checkpoint.stage || '')
  );
}

export function FreshManagedRuntime({
  client,
  credential,
  operation,
  onOperation,
  onUnauthorized,
  onPending,
  guided = false,
  onContinue,
  onSelectExternal,
}: {
  client: ReturnType<typeof createPublicApiClient>;
  credential: string;
  operation: FreshOperation;
  onOperation: (operation: FreshOperation) => void;
  onUnauthorized: () => void;
  onPending: (pending: boolean) => void;
  guided?: boolean;
  onContinue?: () => void;
  onSelectExternal?: () => void;
}) {
  const [observation, setObservation] = useState('');
  const [busy, setBusy] = useState(false);
  const [unknown, setUnknown] = useState(false);
  const [conflict, setConflict] = useState(false);
  const [error, setError] = useState('');
  const [step, setStep] = useState(operation.strategy?.strategy ? 3 : 0);
  const [acknowledged, setAcknowledged] = useState(false);
  const [running, setRunning] = useState(false);
  const [readiness, setReadiness] = useState(false);
  // The plugin opt-in belongs to the destination this installation stages. It
  // starts from the persisted decision and is submitted with the same operation
  // that writes the destination, so a retry cannot silently change it.
  const [plugins, setPlugins] = useState(operation.strategy?.pluginsEnabled === true);
  const [checking, setChecking] = useState(false);
  const runningRef = useRef(false);
  const inFlight = useRef(false);
  const alive = useRef(true);
  const expectedTarget = useRef<ReturnType<typeof hostTarget> | null>(null);
  const pendingWrite = useRef<PendingManagedWrite | null>(null);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);
  const auth = { kind: 'admin' as const, value: credential };
  const checkpoint = operation.strategy;
  const foreign = !!checkpoint?.strategy && checkpoint.strategy !== 'new_managed';
  const invalidCheckpoint = !!checkpoint?.strategy && !validManagedCheckpoint(checkpoint);

  function accept(next: FreshOperation) {
    if (
      !sameIdentity(next?.identity, operation.identity) ||
      next.adminInitialized !== true ||
      !Number.isSafeInteger(next.revision) ||
      next.revision < operation.revision ||
      next.gatewayPending !== true ||
      next.routing?.committed !== true ||
      next.routing.stage !== 'committed' ||
      next.routing.target !== operation.routing.target ||
      next.routing.revision !== operation.routing.revision ||
      next.routing.operationId !== operation.identity.operationId ||
      (next.strategy?.strategy && !validManagedCheckpoint(next.strategy)) ||
      (next.productComplete && next.strategy?.stage !== 'committed') ||
      (expectedTarget.current &&
        next.strategy?.strategy &&
        (next.strategy.targetIdentity !== expectedTarget.current.expectedRuntimeIdentity ||
          next.strategy.targetGeneration !== expectedTarget.current.expectedRuntimeGeneration ||
          next.strategy.targetArtifactId !== expectedTarget.current.expectedArtifactId)) ||
      (checkpoint?.strategy &&
        (next.strategy?.strategy !== checkpoint.strategy ||
          next.strategy.targetIdentity !== checkpoint.targetIdentity ||
          next.strategy.targetGeneration !== checkpoint.targetGeneration ||
          next.strategy.targetArtifactId !== checkpoint.targetArtifactId))
    )
      throw new Error('Invalid installation response');
    // A successful GET can still be a pre-write snapshot. Only the original
    // stage/start/complete transition may discharge an ambiguous POST.
    if (pendingWrite.current) {
      if (!confirmsManagedWrite(next, pendingWrite.current)) {
        throw new Error('Original installation write has not been confirmed');
      }
      pendingWrite.current = null;
    }
    onOperation(next);
    setUnknown(false);
    onPending(false);
  }

  async function reconcile() {
    const next = await client.get<FreshOperation>('operation', auth);
    if (alive.current) accept(next);
    return next;
  }

  async function act(write: boolean, current = operation) {
    if (
      inFlight.current ||
      (write && (unknown || conflict || foreign || invalidCheckpoint || current.productComplete))
    )
      return;
    let body: Record<string, unknown> = {};
    const checkpoint = current.strategy;
    if (write && checkpoint?.stage !== 'committed') {
      try {
        if (!checkpoint?.strategy && !expectedTarget.current)
          expectedTarget.current = hostTarget(observation);
        body = {
          identity: current.identity,
          expectedRevision: current.revision,
          strategy: 'new_managed',
          ...(checkpoint?.strategy
            ? {
                expectedRuntimeIdentity: checkpoint.targetIdentity,
                expectedRuntimeGeneration: checkpoint.targetGeneration,
                expectedArtifactId: checkpoint.targetArtifactId,
                // A replay states the decision the checkpoint recorded.
                ...(checkpoint.pluginsEnabled ? { pluginsEnabled: true } : {}),
              }
            : {
                ...expectedTarget.current,
                ...(plugins ? { pluginsEnabled: true } : {}),
              }),
        };
      } catch {
        setError(t('installObservationInvalid'));
        return;
      }
    }
    inFlight.current = true;
    setBusy(true);
    setError('');
    onPending(true);
    try {
      if (write) {
        pendingWrite.current = {
          revision: current.revision,
          action:
            checkpoint?.stage === 'committed'
              ? 'complete'
              : checkpoint?.strategy
                ? 'start'
                : 'stage',
          previousStage: checkpoint?.stage,
        };
        setUnknown(true);
        await client.post<FreshOperation>(
          checkpoint?.stage === 'committed' ? 'complete' : 'strategy',
          body,
          auth
        );
        // Read the durable checkpoint even when the write reply was received.
      }
      return await reconcile();
    } catch (err) {
      if (!alive.current) return;
      if (err instanceof PublicApiError && err.kind === 'authentication') {
        onUnauthorized();
        return;
      }
      const stopped = err instanceof PublicApiError && err.status === 409;
      if (stopped && write) setConflict(true);
      setError(t(stopped ? 'installConflict' : 'installUnavailable'));
      try {
        if (!write) throw err;
        const next = await reconcile();
        if (!stopped) setError('');
        if (!stopped) return next;
      } catch (readError) {
        if (!alive.current) return;
        if (readError instanceof PublicApiError && readError.kind === 'authentication')
          onUnauthorized();
        else if (write) {
          setUnknown(true);
          onPending(true);
        } else {
          // A failed read alone is not an ambiguous write.
          onPending(unknown);
        }
      }
    } finally {
      if (alive.current) {
        inFlight.current = false;
        setBusy(false);
      }
    }
  }

  async function inspect() {
    if (checking || running) return;
    setChecking(true);
    setError('');
    try {
      const status = await client.get<unknown>('installation/runtime', auth);
      const raw = JSON.stringify(status);
      const target = hostTarget(raw);
      if (alive.current) {
        expectedTarget.current = target;
        setObservation(raw);
        setStep(2);
      }
    } catch (err) {
      if (alive.current) {
        if (err instanceof PublicApiError && err.kind === 'authentication') onUnauthorized();
        else setError(s('unavailable'));
      }
    } finally {
      if (alive.current) setChecking(false);
    }
  }

  async function run() {
    if (runningRef.current || inFlight.current || unknown || conflict) return;
    runningRef.current = true;
    setRunning(true);
    setStep(3);
    let current = operation;
    try {
      // Every next action consumes a confirmed durable read of this operation.
      // A lost reply with no confirming read pauses, never retries the write.
      for (let attempt = 0; attempt < 4 && alive.current && !current.productComplete; attempt++) {
        if (current.strategy?.stage === 'committed') {
          let ready = false;
          for (let probe = 0; probe < 30 && alive.current; probe++) {
            const status = await client.get<{ state: string }>('installation/runtime', auth);
            const target = hostTarget(JSON.stringify(status));
            if (
              target.expectedRuntimeIdentity !== current.strategy.targetIdentity ||
              target.expectedRuntimeGeneration !== current.strategy.targetGeneration ||
              target.expectedArtifactId !== current.strategy.targetArtifactId
            )
              throw new Error('Runtime identity changed');
            if (status.state === 'ready') {
              ready = true;
              break;
            }
            await new Promise((resolve) => setTimeout(resolve, 1000));
          }
          if (!alive.current) return;
          if (!ready) throw new Error('Readiness unavailable');
          setReadiness(true);
        }
        const next = await act(true, current);
        if (!next || !alive.current) return;
        current = next;
        if (current.strategy?.stage === 'starting') return;
      }
    } catch (err) {
      if (alive.current) {
        if (err instanceof PublicApiError && err.kind === 'authentication') onUnauthorized();
        else setError(s('failureHelp'));
      }
    } finally {
      runningRef.current = false;
      if (alive.current) setRunning(false);
    }
  }

  // An adopted installation is a journey of its own. It is never `null`: an
  // adopt checkpoint used to fall into the foreign branch and render nothing, so
  // a browser that had committed an adoption showed an empty card.
  if (checkpoint?.strategy === 'adopt_existing') {
    const guarded = async <T,>(call: () => Promise<T>): Promise<T> => {
      try {
        return await call();
      } catch (err) {
        if (err instanceof PublicApiError && err.kind === 'authentication') onUnauthorized();
        throw err;
      }
    };
    const adoptCarrier: AdoptCarrier = {
      readOperation: () => guarded(() => client.get<FreshOperation>('operation', auth)),
      observeTarget: async () => {
        const observed = hostTarget(
          JSON.stringify(await guarded(() => client.get<unknown>('installation/runtime', auth)))
        );
        return {
          runtimeIdentity: observed.expectedRuntimeIdentity,
          runtimeGeneration: observed.expectedRuntimeGeneration,
          artifactId: observed.expectedArtifactId,
        };
      },
      // The canonical carrier sends no identity of its own, so the flow's body
      // gets the operation's fence exactly where the bootstrap carrier adds it.
      writeStrategy: (body) =>
        guarded(() =>
          client.post<FreshOperation>(
            'strategy',
            { identity: operation.identity, expectedRevision: operation.revision, ...body },
            auth
          )
        ),
      complete: (body) => guarded(() => client.post<FreshOperation>('complete', body, auth)),
      readAdoption: async () =>
        parseAdoptionSummary(
          await guarded(() => client.get<unknown>('installation/adoption', auth))
        ),
    };
    return (
      <FreshAdoptFlow
        operation={operation}
        carrier={adoptCarrier}
        onOperation={onOperation}
        onComplete={onContinue}
        onPending={onPending}
      />
    );
  }

  if (foreign) return null;

  if (guided) {
    const complete = operation.productComplete === true;
    const progress = complete
      ? 4
      : checkpoint?.stage === 'committed'
        ? readiness
          ? 3
          : 2
        : checkpoint?.stage === 'starting'
          ? 1
          : checkpoint?.stage === 'staged'
            ? 1
            : 0;
    const activeStep = complete ? 3 : step;
    const summaryRows: { icon: FullWebIconName; label: string; value: string }[] = complete
      ? [
          { icon: 'server', label: 'CPA', value: journey('cpaReady') },
          { icon: 'shield', label: journey('key'), value: journey('keepKey') },
          { icon: 'link', label: s('consoleAddress'), value: operation.routing.target || '' },
        ]
      : [
          { icon: 'server', label: s('choiceTitle'), value: s('newManaged') },
          { icon: 'shield', label: journey('key'), value: journey('keyHidden') },
          { icon: 'link', label: s('consoleAddress'), value: operation.routing.target || '' },
          { icon: 'installation', label: journey('creates'), value: s('version') },
        ];
    return (
      <section
        className="installation-flow cpamp-installation"
        data-testid="fresh-managed-runtime"
        aria-busy={running || checking}
      >
        <div className="wizard-card">
          <ol className="journey-steps" aria-label={s('installation')}>
            {(['choose', 'configure', 'confirm', 'execute'] as const).map((label, index) => (
              <li
                key={label}
                className={
                  complete || index < activeStep ? 'done' : index === activeStep ? 'active' : ''
                }
                aria-current={activeStep === index ? 'step' : undefined}
              >
                <span className="journey-step-dot">
                  {complete || index < activeStep ? (
                    <FullWebIcon name="check" size={22} />
                  ) : (
                    index + 1
                  )}
                </span>
                <span>{s(label)}</span>
              </li>
            ))}
          </ol>
          <div className="journey-body">
            <div className={`journey-heading ${complete ? 'journey-success-heading' : ''}`}>
              {complete && (
                <span className="journey-success-symbol">
                  <FullWebIcon name="check" size={34} />
                </span>
              )}
              <div>
                <h1 className="wizard-title">
                  {complete
                    ? s('ready')
                    : step === 2
                      ? s('reviewTitle')
                      : step === 3
                        ? s(unknown ? 'unknown' : running ? 'installing' : 'failure')
                        : step === 1
                          ? journey('configureHeading')
                          : journey('welcome')}
                </h1>
                <p className="wizard-desc">
                  {complete
                    ? journey('successHelp')
                    : step === 2
                      ? s('reviewHelp')
                      : step === 3
                        ? journey('progressHelp')
                        : step === 1
                          ? journey('configurationSaved')
                          : journey('chooseHelp')}
                </p>
              </div>
            </div>
            {error && (
              <div className="alert alert-warn" role="alert">
                <FullWebIcon name="alert" size={22} />
                <p>{error}</p>
              </div>
            )}
            {unknown && (
              <div role="status" className="alert alert-warn">
                <FullWebIcon name="alert" size={22} />
                <div>
                  <strong>{s('unknown')}</strong>
                  <p>{s('unknownHelp')}</p>
                </div>
              </div>
            )}
            {invalidCheckpoint && <p role="alert">{s('failureHelp')}</p>}
            {!complete && step === 0 && (
              <div className="journey-choices">
                <div className="journey-choice selected">
                  <span className="journey-radio" aria-hidden="true" />
                  <span className="journey-tile-icon">
                    <FullWebIcon name="server" size={34} />
                  </span>
                  <div>
                    <h2>
                      {s('newManaged')}
                      <span className="badge badge-indigo">{journey('recommended')}</span>
                    </h2>
                    <p>{s('newDetail')}</p>
                  </div>
                </div>
                <div className="journey-choice is-unavailable" aria-disabled="true">
                  <span className="journey-radio" aria-hidden="true" />
                  <span className="journey-tile-icon">
                    <FullWebIcon name="copy" size={34} />
                  </span>
                  <div>
                    <h2>
                      {s('adopt')}
                      <span className="badge badge-neutral">{journey('unavailable')}</span>
                    </h2>
                    <p>{s('adoptHelp')}</p>
                  </div>
                </div>
                <details className="journey-disclosure">
                  <summary>{journey('advanced')}</summary>
                  <button
                    type="button"
                    className="journey-choice"
                    aria-label={s('external')}
                    onClick={onSelectExternal}
                    disabled={!onSelectExternal}
                    data-testid="choose-external"
                  >
                    <span className="journey-tile-icon">
                      <FullWebIcon name="link" size={30} />
                    </span>
                    <div>
                      <h2>{s('external')}</h2>
                      <p>{s('externalHelp')}</p>
                    </div>
                  </button>
                  <p className="journey-help">{s('upgradeHelp')}</p>
                </details>
              </div>
            )}
            {!complete && step === 1 && (
              <>
                <div className="journey-key-strip">
                  <FullWebIcon name="shield" size={24} />
                  <strong>{journey('key')}</strong>
                  <span>{journey('keyHidden')}</span>
                </div>
                <div className="journey-field">
                  <span className="form-label">{s('consoleAddress')}</span>
                  <div className="journey-readonly">
                    <FullWebIcon name="link" size={22} />
                    <span>{operation.routing.target}</span>
                    <FullWebIcon name="check" size={20} />
                  </div>
                  <p className="journey-help">{s('configHelp')}</p>
                </div>
                <div className="journey-preview">
                  <FullWebIcon name="server" size={22} />
                  <div>
                    <strong>
                      {s('host')} · {s('version')}
                    </strong>
                    <p>{journey('createsHelp')}</p>
                  </div>
                </div>
                <div className="journey-field">
                  <label className="journey-checkline" htmlFor="runtime-plugin-optin">
                    <input
                      id="runtime-plugin-optin"
                      data-testid="plugin-optin-checkbox"
                      type="checkbox"
                      checked={checkpoint?.pluginsEnabled === true || plugins}
                      disabled={!!checkpoint?.strategy}
                      onChange={(event) => setPlugins(event.target.checked)}
                    />
                    <span>{journey('pluginsOptIn')}</span>
                  </label>
                  <p className="journey-help">{journey('pluginsOptInHelp')}</p>
                </div>
              </>
            )}
            {((!complete && step === 2) || complete) && (
              <>
                <dl className="journey-summary">
                  {summaryRows.map((row) => (
                    <div className="journey-summary-row" key={row.label}>
                      <FullWebIcon name={row.icon} size={22} />
                      <dt>{row.label}</dt>
                      <dd>{row.value}</dd>
                    </div>
                  ))}
                </dl>
                {complete ? (
                  <>
                    <p className="journey-quiet">
                      <FullWebIcon name="info" size={20} />
                      {journey('bookmark')}
                    </p>
                    <div className="alert alert-info">
                      <FullWebIcon name="shield" size={22} />
                      <div>
                        <strong>{journey('gatewayBlocked')}</strong>
                        <p>{s('readyHelp')}</p>
                      </div>
                    </div>
                  </>
                ) : (
                  <>
                    <div className="alert alert-info">
                      <FullWebIcon name="info" size={22} />
                      <p>{journey('noMigration')}</p>
                    </div>
                    <label className="journey-checkline">
                      <input
                        type="checkbox"
                        checked={acknowledged}
                        onChange={(event) => setAcknowledged(event.target.checked)}
                      />
                      {s('acknowledgement')}
                    </label>
                  </>
                )}
              </>
            )}
            {step === 3 && !complete && (
              <>
                <ol className="journey-stages" aria-live="polite">
                  {(['stage', 'start', 'verify', 'complete'] as const).map((label, index) => (
                    <li
                      key={label}
                      className={
                        index < progress
                          ? 'done'
                          : index === progress
                            ? unknown || error || conflict
                              ? 'warn'
                              : running
                                ? 'active'
                                : ''
                            : ''
                      }
                    >
                      <span
                        className={`journey-stage-mark ${running && index === progress && !unknown && !error ? 'is-spinning' : ''}`}
                        aria-hidden="true"
                      >
                        {index < progress ? (
                          <FullWebIcon name="check" size={20} />
                        ) : index === progress && (unknown || error || conflict) ? (
                          <FullWebIcon name="alert" size={22} />
                        ) : null}
                      </span>
                      <div>
                        <strong>{s(label)}</strong>
                        <p>
                          {journey(
                            (
                              [
                                'stagePrepare',
                                'stageStart',
                                'stageVerify',
                                'stageComplete',
                              ] as const
                            )[index]
                          )}
                        </p>
                      </div>
                      <small>
                        {s(
                          index < progress
                            ? 'done'
                            : running && index === progress
                              ? 'active'
                              : 'pending'
                        )}
                      </small>
                    </li>
                  ))}
                </ol>
                <p className="journey-quiet" data-testid="install-stage">
                  <FullWebIcon name="info" size={20} />
                  {unknown ? s('unknown') : running ? s('installing') : s('failure')}
                </p>
              </>
            )}
            {complete && (
              <p className="journey-quiet" data-testid="install-stage">
                {t('installComplete')}
              </p>
            )}
            <details className="journey-disclosure installation-diagnostics">
              <summary>{s('technical')}</summary>
              <dl className="console-state">
                <dt>{t('operation')}</dt>
                <dd>{operation.identity.operationId}</dd>
                <dt>{t('revision')}</dt>
                <dd>{operation.revision}</dd>
                <dt>{s('destination')}</dt>
                <dd>
                  {checkpoint?.targetIdentity ||
                    expectedTarget.current?.expectedRuntimeIdentity ||
                    '—'}
                </dd>
              </dl>
            </details>
          </div>
          <footer className="journey-footer">
            {!complete && step === 0 && (
              <>
                <span className="journey-quiet">
                  <FullWebIcon name="info" size={20} />
                  {journey('installNotStarted')}
                </span>
                <button
                  className="btn btn-primary"
                  data-testid="choose-installation"
                  onClick={() => setStep(1)}
                >
                  {s('next')}
                  <FullWebIcon name="arrowRight" size={20} />
                </button>
              </>
            )}
            {!complete && step === 1 && (
              <>
                <button
                  className="btn btn-secondary"
                  disabled={checking}
                  onClick={() => setStep(0)}
                >
                  <FullWebIcon name="arrowLeft" size={20} />
                  {s('back')}
                </button>
                <button
                  className="btn btn-primary"
                  data-testid="inspect-installation"
                  disabled={checking}
                  onClick={() => void inspect()}
                >
                  {s(checking ? 'inspecting' : 'inspect')}
                  <FullWebIcon name="arrowRight" size={20} />
                </button>
              </>
            )}
            {!complete && step === 2 && (
              <>
                <button className="btn btn-secondary" onClick={() => setStep(1)}>
                  <FullWebIcon name="arrowLeft" size={20} />
                  {s('back')}
                </button>
                <button
                  className="btn btn-primary"
                  data-testid="install-runtime"
                  disabled={!acknowledged || running}
                  onClick={() => void run()}
                >
                  {s('submit')}
                  <FullWebIcon name="arrowRight" size={20} />
                </button>
              </>
            )}
            {step === 3 && !complete && (
              <>
                <button
                  className="btn btn-secondary"
                  data-testid="refresh-installation"
                  disabled={running || busy}
                  onClick={() => void act(false)}
                >
                  <FullWebIcon name="refresh" size={20} />
                  {s('refresh')}
                </button>
                <button
                  className="btn btn-primary"
                  data-testid="install-runtime"
                  disabled={running || busy || unknown || conflict || invalidCheckpoint}
                  onClick={() => void run()}
                >
                  {s(running ? 'installing' : 'recover')}
                  <FullWebIcon name="arrowRight" size={20} />
                </button>
              </>
            )}
            {complete && (
              <>
                <span className="journey-quiet">
                  <FullWebIcon name="shield" size={20} />
                  {journey('keepKey')}
                </span>
                <button className="btn btn-primary" onClick={onContinue}>
                  {s('console')}
                  <FullWebIcon name="arrowRight" size={20} />
                </button>
              </>
            )}
          </footer>
        </div>
      </section>
    );
  }

  return (
    <section className="info-card" data-testid="fresh-managed-runtime">
      <h2>{t('installTitle')}</h2>
      <p>{t('installHelp')}</p>
      <p data-testid="install-stage">
        {operation.productComplete
          ? t('installComplete')
          : checkpoint?.stage || t('installNotStarted')}
      </p>
      {error && (
        <p role="alert" className="alert alert-warn">
          {error}
        </p>
      )}
      {invalidCheckpoint && (
        <p role="alert" className="alert alert-warn">
          {t('installConflict')}
        </p>
      )}
      {unknown && (
        <p role="status" className="alert alert-warn">
          {t('installUnknown')}
        </p>
      )}
      {!checkpoint?.strategy && !operation.productComplete && (
        <label className="form-label">
          {t('installObservation')}
          <textarea
            className="form-input"
            data-testid="runtime-observation"
            value={observation}
            onChange={(event) => {
              if (!inFlight.current && !unknown && !expectedTarget.current)
                setObservation(event.target.value);
            }}
            disabled={busy || unknown || conflict || !!expectedTarget.current}
            autoComplete="off"
            spellCheck={false}
            rows={5}
          />
        </label>
      )}
      <div className="wizard-card-footer">
        {!operation.productComplete && !foreign && (
          <button
            className="btn btn-primary"
            data-testid="install-runtime"
            onClick={() => void act(true)}
            disabled={busy || unknown || conflict || invalidCheckpoint}
          >
            {t(
              checkpoint?.stage === 'committed'
                ? 'installFinish'
                : checkpoint?.strategy
                  ? 'installStart'
                  : 'installStage'
            )}
          </button>
        )}
        <button
          className="btn btn-secondary"
          data-testid="refresh-installation"
          onClick={() => void act(false)}
          disabled={busy}
        >
          {t('refresh')}
        </button>
      </div>
    </section>
  );
}
