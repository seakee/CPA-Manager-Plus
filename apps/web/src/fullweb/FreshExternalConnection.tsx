import { useEffect, useRef, useState } from 'react';
import { fullWebText as t } from '../i18n/fullweb';
import { shellText as s } from '../i18n/consoleShell';
import { externalSetupBlockerKey, externalWorkspaceText as x } from '../i18n/freshExternal';
import { useFullWebLanguage } from './useFullWebLanguage';
import { FullWebIcon } from './FullWebIcon';
import { sameIdentity, type FreshOperation } from './bootstrapTransport';
import { PublicApiError, type createPublicApiClient } from './publicApi';

// Normalize only harmless whitespace/trailing slashes. The server validates the
// target and reads CPA safely; this input is never an authority assertion.
function canonicalExternalURL(raw: string): string | null {
  const value = raw.trim().replace(/\/+$/, '');
  if (!value || /[\\%@?#\s]/.test(value)) return null;
  try {
    const url = new URL(value);
    if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password) return null;
    return url.origin + url.pathname.replace(/\/+$/, '') === value ? value : null;
  } catch {
    return null;
  }
}

export function FreshExternalConnection({
  client,
  credential,
  operation,
  onOperation,
  onPending,
  onUnauthorized,
  onBack,
  onContinue,
}: {
  client: ReturnType<typeof createPublicApiClient>;
  credential: string;
  operation: FreshOperation;
  onOperation: (next: FreshOperation) => void;
  onPending: (pending: boolean) => void;
  onUnauthorized: () => void;
  onBack?: () => void;
  onContinue?: () => void;
}) {
  const [baseURL, setBaseURL] = useState('');
  const [managementKey, setManagementKey] = useState('');
  const [step, setStep] = useState(1);
  const [busy, setBusy] = useState(false);
  const [unknown, setUnknown] = useState(false);
  // The message keeps the key that names it, not the rendered sentence, so a
  // language switch re-renders an error that is already on screen. A guarded
  // refusal is an `external` key; every other failure is a `fullweb` one.
  const [error, setError] = useState<
    | { from: 'external'; key: Parameters<typeof x>[0] }
    | { from: 'fullweb'; key: Parameters<typeof t>[0] }
    | null
  >(null);
  useFullWebLanguage();
  const inflight = useRef(false);
  const alive = useRef(true);
  const saved =
    operation.strategy?.strategy === 'connect_external' && operation.strategy.stage === 'committed';
  const complete = saved && operation.productComplete;
  const activeStep = saved || unknown || busy ? 3 : step;
  const auth = { kind: 'admin' as const, value: credential };

  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);

  function accept(next: FreshOperation) {
    const checkpoint = next?.strategy;
    if (
      !sameIdentity(next?.identity, operation.identity) ||
      !Number.isSafeInteger(next.revision) ||
      next.revision < operation.revision ||
      next.adminInitialized !== true ||
      next.gatewayPending !== true ||
      next.routing?.committed !== true ||
      next.routing.stage !== 'committed' ||
      next.routing.target !== operation.routing.target ||
      next.routing.revision !== operation.routing.revision ||
      next.routing.operationId !== operation.identity.operationId ||
      (checkpoint?.strategy &&
        (checkpoint.strategy !== 'connect_external' ||
          !['validated', 'committed'].includes(checkpoint.stage || '') ||
          !/^external:sha256:[a-f0-9]{64}$/.test(checkpoint.targetIdentity || '') ||
          checkpoint.targetGeneration !== 1 ||
          !!checkpoint.targetArtifactId)) ||
      (next.productComplete && checkpoint?.stage !== 'committed') ||
      (saved &&
        (checkpoint?.stage !== 'committed' ||
          checkpoint.targetIdentity !== operation.strategy?.targetIdentity))
    ) {
      throw new Error('Untrusted External checkpoint');
    }
    if (alive.current) onOperation(next);
    return next;
  }

  async function readCurrent() {
    return accept(await client.get<FreshOperation>('operation', auth));
  }

  function review() {
    const url = canonicalExternalURL(baseURL);
    if (
      !url ||
      !managementKey ||
      managementKey.length > 4096 ||
      Array.from(managementKey).some((c) => (c.codePointAt(0) ?? 0) < 0x21 || c === '\x7f') ||
      /\s/u.test(managementKey)
    ) {
      setError({ from: 'fullweb', key: 'externalInvalid' });
      return;
    }
    setBaseURL(url);
    setError(null);
    setStep(2);
  }

  async function act(action: 'connect' | 'complete' | 'refresh') {
    if (inflight.current) return;
    inflight.current = true;
    setBusy(true);
    setError(null);
    onPending(true);
    let dispatched = false;
    try {
      if (action === 'connect') {
        if (saved || unknown) return;
        const bytes = new Uint8Array(16);
        globalThis.crypto.getRandomValues(bytes);
        const attemptId = Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('');
        const key = managementKey;
        setManagementKey('');
        setStep(3);
        dispatched = true;
        const post = accept(
          await client.post<FreshOperation>(
            'strategy',
            {
              identity: operation.identity,
              expectedRevision: operation.revision,
              attemptId,
              strategy: 'connect_external',
              baseUrl: baseURL,
              managementKey: key,
            },
            auth
          )
        );
        if (post.strategy?.stage !== 'committed') {
          throw new Error('Uncommitted External connection');
        }
      } else if (action === 'complete') {
        if (!saved) return;
        dispatched = true;
        const post = accept(await client.post<FreshOperation>('complete', {}, auth));
        if (!post.productComplete) throw new Error('Incomplete External setup');
      }
      // GET is authoritative for saved configuration after a lost reply/reload.
      // It never claims which browser's typed Management Key was saved.
      await readCurrent();
      if (alive.current) {
        setUnknown(false);
        setStep(1);
      }
    } catch (err) {
      if (!alive.current) return;
      if (err instanceof PublicApiError && err.kind === 'authentication') {
        onUnauthorized();
        return;
      }
      let recovered = false;
      if (dispatched) {
        try {
          const next = await readCurrent();
          if (!alive.current) return;
          recovered = true;
          if (next.strategy?.stage === 'committed') {
            setUnknown(false);
            return;
          }
        } catch (readError) {
          if (!alive.current) return;
          if (readError instanceof PublicApiError && readError.kind === 'authentication') {
            onUnauthorized();
            return;
          }
        }
      }
      setUnknown((dispatched && !recovered) || action === 'refresh');
      setStep(1);
      // A guarded refusal names its cause in the operator's own language. The
      // Manager's English `Action` sentence is never rendered: the page maps the
      // stable code onto the External message that explains the recovery.
      const blocker =
        err instanceof PublicApiError && err.status === 409
          ? externalSetupBlockerKey(err.code)
          : undefined;
      setError(
        blocker
          ? { from: 'external', key: blocker }
          : {
              from: 'fullweb',
              key:
                err instanceof PublicApiError && err.code === 'management_authentication_rejected'
                  ? 'externalSourceAuthRejected'
                  : err instanceof PublicApiError && err.status === 409
                    ? 'externalConflict'
                    : 'externalUnavailable',
            }
      );
    } finally {
      inflight.current = false;
      if (alive.current) {
        setBusy(false);
        onPending(false);
      }
    }
  }

  return (
    <section
      className="installation-flow cpamp-installation"
      data-testid="fresh-external-connection"
      aria-busy={busy}
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
          <div className={complete ? 'journey-heading journey-success-heading' : 'journey-heading'}>
            {complete && (
              <span className="journey-success-symbol">
                <FullWebIcon name="check" size={34} />
              </span>
            )}
            <div>
              <h1 className="wizard-title">
                {complete
                  ? s('ready')
                  : saved
                    ? t('externalConfirmed')
                    : step === 2
                      ? s('reviewTitle')
                      : t('externalTitle')}
              </h1>
              <p className="wizard-desc">{t('externalHelp')}</p>
            </div>
          </div>
          {error && (
            <p role="alert" className="alert alert-warn">
              {error.from === 'external' ? x(error.key) : t(error.key)}
            </p>
          )}
          {unknown && (
            <p role="status" className="alert alert-warn">
              {t('externalUnknown')}
            </p>
          )}
          {!saved && !unknown && !busy && step === 1 && (
            <>
              {operation.strategy?.stage === 'validated' && (
                <p className="alert alert-info">{t('externalHostStop')}</p>
              )}
              <div className="journey-field">
                <label className="form-label" htmlFor="fresh-external-base-url">
                  {t('externalBaseURL')}
                </label>
                <input
                  className="form-input"
                  id="fresh-external-base-url"
                  data-testid="external-base-url"
                  value={baseURL}
                  onChange={(event) => setBaseURL(event.target.value)}
                  placeholder="http://cpa:8317"
                  autoComplete="off"
                  spellCheck={false}
                />
              </div>
              <div className="journey-field">
                <label className="form-label" htmlFor="fresh-external-management-key">
                  {t('externalManagementKey')}
                </label>
                <input
                  className="form-input"
                  id="fresh-external-management-key"
                  type="password"
                  data-testid="external-management-key"
                  value={managementKey}
                  onChange={(event) => setManagementKey(event.target.value)}
                  autoComplete="off"
                  autoCorrect="off"
                  autoCapitalize="off"
                  spellCheck={false}
                />
              </div>
            </>
          )}
          {step === 2 && !saved && !busy && !unknown && (
            <dl className="journey-summary">
              <div className="journey-summary-row">
                <FullWebIcon name="link" size={22} />
                <dt>{t('externalBaseURL')}</dt>
                <dd>{baseURL}</dd>
              </div>
              <div className="journey-summary-row">
                <FullWebIcon name="shield" size={22} />
                <dt>{t('externalManagementKey')}</dt>
                <dd>••••••••</dd>
              </div>
              <div className="journey-summary-row">
                <FullWebIcon name="link" size={22} />
                <dt>{s('consoleAddress')}</dt>
                <dd>{operation.routing.target}</dd>
              </div>
            </dl>
          )}
          {busy && (
            <p role="status" className="journey-quiet">
              <span className="loading-spinner" />
              {t('externalSubmitting')}
            </p>
          )}
          {saved && (
            <p role="status" className="alert alert-info">
              {t('externalExisting')}
            </p>
          )}
          <p className="journey-quiet">
            <FullWebIcon name="info" size={20} />
            {t('externalBoundary')}
          </p>
          <details className="journey-disclosure installation-diagnostics">
            <summary>{s('technical')}</summary>
            <dl className="console-state">
              <dt>{t('operation')}</dt>
              <dd>{operation.identity.operationId}</dd>
              <dt>{t('revision')}</dt>
              <dd>{operation.revision}</dd>
              <dt>{s('destination')}</dt>
              <dd>{operation.strategy?.targetIdentity || '—'}</dd>
              <dt>{s('stage')}</dt>
              <dd data-testid="external-stage">{operation.strategy?.stage || '—'}</dd>
            </dl>
          </details>
        </div>
        <footer className="journey-footer">
          {!saved && !unknown && step === 1 && (
            <>
              <button
                type="button"
                className="btn btn-secondary"
                disabled={busy || !!operation.strategy?.strategy}
                onClick={onBack}
              >
                {s('back')}
              </button>
              <button
                type="button"
                className="btn btn-primary"
                data-testid="external-review"
                disabled={busy || !baseURL.trim() || !managementKey}
                onClick={review}
              >
                {s('next')}
                <FullWebIcon name="arrowRight" size={20} />
              </button>
            </>
          )}
          {!saved && !unknown && step === 2 && (
            <>
              <button
                type="button"
                className="btn btn-secondary"
                disabled={busy}
                onClick={() => setStep(1)}
              >
                {s('back')}
              </button>
              <button
                type="button"
                className="btn btn-primary"
                data-testid="external-connect"
                disabled={busy}
                onClick={() => void act('connect')}
              >
                {t('externalSubmit')}
              </button>
            </>
          )}
          {(saved || unknown || step === 3) && (
            <button
              type="button"
              className="btn btn-secondary"
              data-testid="external-refresh"
              disabled={busy}
              onClick={() => void act('refresh')}
            >
              {t('refresh')}
            </button>
          )}
          {saved && !complete && (
            <button
              type="button"
              className="btn btn-primary"
              data-testid="external-complete"
              disabled={busy || unknown}
              onClick={() => void act('complete')}
            >
              {t('externalComplete')}
            </button>
          )}
          {complete && (
            <button
              type="button"
              className="btn btn-primary"
              data-testid="external-console"
              disabled={busy}
              onClick={onContinue}
            >
              {s('console')}
              <FullWebIcon name="arrowRight" size={20} />
            </button>
          )}
        </footer>
      </div>
    </section>
  );
}
