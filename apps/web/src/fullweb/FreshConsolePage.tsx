import { lazy, Suspense, useEffect, useRef, useState } from 'react';
import { fullWebI18n, fullWebText as t } from '../i18n/fullweb';
import { sameIdentity, type FreshOperation } from './bootstrapTransport';
import { createPublicApiClient, PublicApiError } from './publicApi';
import type { RoutingContext } from './routingContext';
import { FreshManagedRuntime } from './FreshManagedRuntime';
import { FreshExternalConnection } from './FreshExternalConnection';
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
  const [externalSelected, setExternalSelected] = useState(false);
  const [busy, setBusy] = useState(false);
  // Kept as a key: the login form and its language menu must retranslate a
  // failure that is already on screen.
  const [error, setError] = useState<Parameters<typeof t>[0] | null>(null);
  const [light, setLight] = useState(true);
  const installationPendingRef = useRef(false);
  const requestId = useRef(0);
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

  function acceptOperation(next: FreshOperation) {
    setOperation((current) => {
      // Keep newer durable state if an earlier Console read completes later.
      if (
        current &&
        (!sameIdentity(next.identity, current.identity) || next.revision < current.revision)
      )
        return current;
      return next;
    });
  }

  function clearAuthentication() {
    requestId.current++;
    installationPendingRef.current = false;
    clearFreshSession();
    setCredential('');
    setOperation(null);
    setEnteredConsole(false);
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
      if (
        !next?.identity ||
        !(
          ['admissionId', 'installationId', 'generation', 'databasePath', 'operationId'] as const
        ).every(
          (field) => typeof next.identity[field] === 'string' && next.identity[field].length > 0
        ) ||
        !next.identity.operationId ||
        !Number.isSafeInteger(next.revision) ||
        next.revision <= 0 ||
        next.adminInitialized !== true ||
        typeof next.gatewayPending !== 'boolean' ||
        next.routing?.committed !== true ||
        next.routing.stage !== 'committed' ||
        next.routing.target !== context.basePath ||
        next.routing.revision !== context.revision ||
        next.routing.operationId !== next.identity.operationId ||
        (operation && !sameIdentity(next.identity, operation.identity))
      ) {
        clearAuthentication();
        setError('invalidOperation');
        return false;
      }
      activateFreshSession(context, candidate, remember);
      setRememberPassword(remember);
      setEnteredConsole(next.productComplete === true);
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
  if (!operation.productComplete || !enteredConsole)
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
            {operation.strategy?.strategy === 'connect_external' ||
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
