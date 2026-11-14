import { lazy, Suspense, useEffect, useRef, useState } from 'react';
import { fullWebI18n, fullWebText as t } from '../i18n/fullweb';
import type { FreshOperation } from './bootstrapTransport';
import { isCanonicalPrefix } from './accessPrefix';
import { createPublicApiClient, PublicApiError } from './publicApi';
import type { RoutingContext } from './routingContext';
import { FreshManagedRuntime } from './FreshManagedRuntime';
import { FreshExternalConnection } from './FreshExternalConnection';
import { FreshInferencePrefixCompletion } from './FreshInferencePrefixCompletion';
import { FullWebIcon } from './FullWebIcon';
import logo from '../assets/brand/cpamp-logo-horizontal-color.svg?inline';
import logoOnDark from '../assets/brand/cpamp-logo-horizontal-on-dark.svg?inline';
import './consoleWorkspace.css';
import { activateFreshSession, clearFreshSession, rememberedFreshCredential } from './freshSession';
import { bootstrapJourneyText } from '../i18n/bootstrapJourney';
import { useConsoleDocumentTitle } from './useConsoleDocumentTitle';
import { useFullWebLanguage } from './useFullWebLanguage';

const defaultClient = createPublicApiClient();
const LegacyConsolePage = lazy(() =>
  import('./LegacyConsolePage').then((module) => ({ default: module.LegacyConsolePage }))
);
const FullWebLoginPage = lazy(() =>
  import('./FullWebLoginPage').then((module) => ({ default: module.FullWebLoginPage }))
);

function hasNonEmptyToken(value: unknown): value is string {
  return typeof value === 'string' && value.trim().length > 0;
}

function validOptionalToken(value: unknown): boolean {
  return (
    value === undefined ||
    (typeof value === 'string' && (value.length === 0 || value.trim().length > 0))
  );
}

function validConsoleIdentity(identity: FreshOperation['identity'] | undefined): boolean {
  if (!identity || typeof identity !== 'object') return false;
  if (
    !hasNonEmptyToken(identity.installationId) ||
    !hasNonEmptyToken(identity.generation) ||
    !hasNonEmptyToken(identity.operationId) ||
    !validOptionalToken(identity.admissionId) ||
    !validOptionalToken(identity.databasePath)
  ) {
    return false;
  }
  return Boolean(identity.admissionId) === Boolean(identity.databasePath);
}

function sameConsoleOwner(
  a: FreshOperation['identity'] | undefined,
  b: FreshOperation['identity'] | undefined
): boolean {
  if (!validConsoleIdentity(a) || !validConsoleIdentity(b) || !a || !b) return false;
  return (
    a.installationId === b.installationId &&
    a.generation === b.generation &&
    a.operationId === b.operationId &&
    (a.admissionId ?? '') === (b.admissionId ?? '') &&
    (a.databasePath ?? '') === (b.databasePath ?? '')
  );
}

function isValidConsoleOperation(
  next: FreshOperation | null | undefined,
  context: RoutingContext,
  current: FreshOperation | null
): next is FreshOperation {
  if (!next || !validConsoleIdentity(next.identity)) return false;
  if (!Number.isSafeInteger(next.revision) || next.revision <= 0) return false;
  if (next.adminInitialized !== true) return false;
  if (typeof next.gatewayPending !== 'boolean') return false;
  if (!next.routing || next.routing.committed !== true || next.routing.stage !== 'committed') {
    return false;
  }
  if (next.routing.target !== context.basePath) return false;
  if (next.routing.operationId !== next.identity.operationId) return false;
  const routingRevision = next.routing.revision;
  if (
    routingRevision === undefined ||
    !Number.isSafeInteger(routingRevision) ||
    routingRevision <= 0
  ) {
    return false;
  }
  const minRoutingRevision = Math.max(
    context.revision,
    current?.routing?.revision ?? context.revision
  );
  if (routingRevision < minRoutingRevision) return false;
  if (routingRevision > context.revision) {
    const completedInference =
      next.routing.inferenceCommitted === true &&
      next.routing.missingInferenceCompletionRequired !== true &&
      isCanonicalPrefix(next.routing.inferenceTarget);
    const stagedInference =
      context.inferenceCompletionRequired === true &&
      next.routing.missingInferenceCompletionRequired === true &&
      next.routing.inferenceCommitted !== true;
    if (!completedInference && !stagedInference) {
      return false;
    }
  }
  if (current) {
    if (!sameConsoleOwner(next.identity, current.identity)) return false;
    if (next.revision < current.revision) return false;
    if (next.routing.target !== current.routing.target) return false;
    if (current.routing.inferenceCommitted === true) {
      if (
        next.routing.inferenceCommitted !== true ||
        next.routing.missingInferenceCompletionRequired === true ||
        (next.routing.inferenceTarget ?? '') !== (current.routing.inferenceTarget ?? '') ||
        Boolean(next.routing.sharedEntry) !== Boolean(current.routing.sharedEntry)
      ) {
        return false;
      }
    }
  }
  return true;
}

export function FreshConsolePage({
  context,
  client = defaultClient,
}: {
  context: RoutingContext;
  client?: ReturnType<typeof createPublicApiClient>;
}) {
  const [credential, setCredential] = useState('');
  const [rememberPassword, setRememberPassword] = useState(false);
  const [operation, setOperation] = useState<FreshOperation | null>(null);
  const [enteredConsole, setEnteredConsole] = useState(false);
  const [inferenceCompletionActive, setInferenceCompletionActive] = useState(false);
  const [externalSelected, setExternalSelected] = useState(false);
  const [busy, setBusy] = useState(false);
  // Kept as a key: the login form and its language menu must retranslate a
  // failure that is already on screen.
  const [error, setError] = useState<Parameters<typeof t>[0] | null>(null);
  const [light, setLight] = useState(true);
  const installationPendingRef = useRef(false);
  const requestId = useRef(0);
  const operationRef = useRef<FreshOperation | null>(null);
  const language = useFullWebLanguage();
  useConsoleDocumentTitle();
  // The stored failure key is translated at render: subscribing to the Fresh
  // language (not an unrelated re-render) is what retranslates it.
  const errorText = error ? fullWebI18n.getFixedT(language)(error) : '';
  useEffect(
    () => () => {
      requestId.current++;
    },
    []
  );
  useEffect(() => {
    if (!operation || enteredConsole) return;
    document.documentElement.classList.toggle('light', light);
    document.documentElement.setAttribute('data-theme', light ? 'white' : 'dark');
  }, [light, operation, enteredConsole]);

  function acceptOperation(next: FreshOperation): boolean {
    if (!isValidConsoleOperation(next, context, operationRef.current)) {
      return false;
    }
    operationRef.current = next;
    setOperation(next);
    return true;
  }

  function clearAuthentication() {
    requestId.current++;
    installationPendingRef.current = false;
    operationRef.current = null;
    clearFreshSession();
    setCredential('');
    setOperation(null);
    setEnteredConsole(false);
    setInferenceCompletionActive(false);
    setExternalSelected(false);
    setBusy(false);
    setError(null);
  }

  async function readOperation(candidate = credential, remember = rememberPassword) {
    if (busy || installationPendingRef.current || !candidate) return false;
    const currentRequest = ++requestId.current;
    setBusy(true);
    setError(null);
    try {
      const next = await client.get<FreshOperation>('operation', {
        kind: 'admin',
        value: candidate,
      });
      if (currentRequest !== requestId.current) return false;
      if (!isValidConsoleOperation(next, context, operationRef.current)) {
        clearAuthentication();
        setError('invalidOperation');
        return false;
      }
      activateFreshSession(context, candidate, remember);
      setRememberPassword(remember);
      const requiresInferenceCompletion =
        next.productComplete === true && next.routing?.missingInferenceCompletionRequired === true;
      setInferenceCompletionActive(requiresInferenceCompletion);
      setEnteredConsole(next.productComplete === true && !requiresInferenceCompletion);
      setCredential(candidate);
      acceptOperation(next);
      return true;
    } catch (err) {
      if (currentRequest !== requestId.current) return false;
      if (err instanceof PublicApiError && err.kind === 'authentication') {
        clearAuthentication();
        setError('authError');
      } else {
        setError('unavailable');
      }
      return false;
    } finally {
      if (currentRequest === requestId.current) setBusy(false);
    }
  }

  const unauthorized = () => {
    clearAuthentication();
    setError('authError');
  };
  if (!operation) {
    return (
      <Suspense fallback={<div role="status">{t('loggingIn')}</div>}>
        <FullWebLoginPage
          adapter={{
            address: new URL(context.basePath, window.location.origin).toString(),
            credentialPlaceholder: bootstrapJourneyText('loginKeyPlaceholder'),
            error: errorText,
            authenticate: readOperation,
            restore: async () => {
              const candidate = rememberedFreshCredential(context);
              if (candidate) await readOperation(candidate, true);
            },
          }}
        />
      </Suspense>
    );
  }
  const requiresInferenceCompletion =
    operation.productComplete === true &&
    (operation.routing?.missingInferenceCompletionRequired === true || inferenceCompletionActive);
  if (!operation.productComplete || !enteredConsole || requiresInferenceCompletion)
    return (
      <div className="bootstrap-shell">
        <header className="bootstrap-header">
          <a className="console-login-brand" href={context.basePath}>
            <img src={light ? logo : logoOnDark} alt="CPAMP" />
          </a>
          <button className="btn-icon" aria-label={t('theme')} onClick={() => setLight(!light)}>
            <FullWebIcon name={light ? 'moon' : 'sun'} />
          </button>
        </header>
        <main className="bootstrap-main">
          <div className="wizard-container">
            {error && (
              <p className="alert alert-warn" role="alert">
                {errorText}
              </p>
            )}
            {requiresInferenceCompletion ? (
              <FreshInferencePrefixCompletion
                client={client}
                credential={credential}
                operation={operation}
                onOperation={acceptOperation}
                onUnauthorized={unauthorized}
                onContinue={() => {
                  setInferenceCompletionActive(false);
                  setEnteredConsole(true);
                  window.history.replaceState(null, '', context.basePath);
                }}
              />
            ) : operation.strategy?.strategy === 'connect_external' ||
            (!operation.strategy?.strategy && externalSelected) ? (
              <FreshExternalConnection
                client={client}
                credential={credential}
                operation={operation}
                onOperation={acceptOperation}
                onUnauthorized={unauthorized}
                onBack={() => setExternalSelected(false)}
                onContinue={() => {
                  setEnteredConsole(true);
                  window.history.replaceState(null, '', context.basePath);
                }}
                onPending={(pending) => {
                  installationPendingRef.current = pending;
                }}
              />
            ) : (
              <FreshManagedRuntime
                guided
                client={client}
                credential={credential}
                operation={operation}
                onOperation={acceptOperation}
                onUnauthorized={unauthorized}
                onPending={(pending) => {
                  installationPendingRef.current = pending;
                }}
                onContinue={() => {
                  // Completion has been confirmed by the canonical operation.
                  setEnteredConsole(true);
                  window.history.replaceState(null, '', context.basePath);
                }}
                onSelectExternal={() => setExternalSelected(true)}
              />
            )}
          </div>
        </main>
      </div>
    );
  if (operation.productComplete) {
    return (
      <Suspense
        fallback={
          <div className="main-content" role="status">
            <span className="loading-spinner" aria-label={t('unavailable')} />
          </div>
        }
      >
        <LegacyConsolePage
          context={context}
          credential={credential}
          client={client}
          strategy={operation.strategy?.strategy}
          onUnauthorized={unauthorized}
        />
      </Suspense>
    );
  }

  return null;
}
