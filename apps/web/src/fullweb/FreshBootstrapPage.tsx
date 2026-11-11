import { lazy, Suspense, useCallback, useEffect, useRef, useState } from 'react';
import {
  bootstrapDiscoveryText as discoveryText,
  type BootstrapDiscoveryTextKey as BootstrapTextKey,
} from '../i18n/bootstrapDiscovery';
import { bootstrapJourneyText as journey } from '../i18n/bootstrapJourney';
import { shellText } from '../i18n/consoleShell';
import { bootstrapText, type FreshBootstrapTextKey } from '../i18n/freshBootstrap';
import { fullWebLanguage, setFullWebLanguage } from '../i18n/fullweb';
import {
  FRESH_LANGUAGES,
  isFreshLanguage,
  persistFreshLanguage,
} from './freshLanguage';
import type { Language } from '../types';
import logo from '../assets/brand/cpamp-logo-horizontal-color.svg?inline';
import logoOnDark from '../assets/brand/cpamp-logo-horizontal-on-dark.svg?inline';
import { FullWebIcon } from './FullWebIcon';
import {
  BootstrapTransportClient,
  BootstrapTransportError,
  FreshOperation,
  Handoff,
} from './bootstrapTransport';

import { FreshSetupWizard } from './FreshSetupWizard';
const UpgradeAuthenticationEntry = lazy(() => import('./UpgradeAuthenticationEntry').then(
  (module) => ({ default: module.UpgradeAuthenticationEntry })
));
export interface FreshBootstrapPageProps {
  handoff: Handoff | null;
  transport: BootstrapTransportClient;
}

type BootPhase = 'no_handoff' | 'connecting' | 'ready' | 'init_error';

// A state keeps the copy keys rather than the rendered sentence, so a language
// switch re-renders the same failure in the newly selected language.
interface ActionFeedback {
  tone: 'danger' | 'warn' | 'info' | 'success';
  title: FreshBootstrapTextKey;
  detail: FreshBootstrapTextKey;
  /** HTTP status interpolated into a `{status}` title, when the server reported one. */
  status?: number;
}

function feedbackText(feedback: ActionFeedback, part: 'title' | 'detail'): string {
  return bootstrapText(
    feedback[part],
    feedback.status === undefined ? undefined : { status: feedback.status }
  );
}

// Every language names itself, exactly as the Console header menu does.
const LANGUAGE_NAME_KEYS: Record<Language, FreshBootstrapTextKey> = {
  'zh-CN': 'languageZhCN',
  'zh-TW': 'languageZhTW',
  en: 'languageEnglish',
  ru: 'languageRussian',
};

// Public discovery never confers write authority. Non-Fresh statuses only
// enter either the existing-Admin gate or host intervention, never Fresh claim.
type DiscoveryGuidance =
  | (ActionFeedback & { flow: 'fresh' | 'host' | 'upgrade'; retryable?: true })
  | {
      tone: ActionFeedback['tone'];
      flow: 'ownership';
      retryable: true;
      // copyKey names the dictionary entries a card must re-derive at render time.
      // The discovery answer is fetched once and kept in state, so a card built from
      // resolved strings keeps the language it was fetched in; a card that names its
      // copy follows the page's language switcher instead.
      copyKey: { title: BootstrapTextKey; detail: BootstrapTextKey };
    };

// guidanceCopy resolves the one card this page renders. Copy that names its
// dictionary entries is resolved here, on every render, so a language switch
// applies to a payload that was fetched once.
function guidanceCopy(note: DiscoveryGuidance): { title: string; detail: string } {
  if (note.flow === 'ownership') {
    return {
      title: discoveryText(note.copyKey.title),
      detail: discoveryText(note.copyKey.detail),
    };
  }
  return {
    title: feedbackText(note, 'title'),
    detail: feedbackText(note, 'detail'),
  };
}

function publicDiscoveryGuidance(payload: unknown): DiscoveryGuidance | null {
  if (!payload || typeof payload !== 'object') return null;
  const value = payload as Record<string, unknown>;
  if (value.contract_version !== 'cpamp.phase4-01.discovery.v1') return null;
  if (
    value.status === 'fresh_claim_required' &&
    value.mode === 'fresh' &&
    value.auth_entry === 'fresh_claim' &&
    value.reason === 'fresh_claim_required' &&
    value.authentication_required === true
  ) {
    return {
      tone: 'info',
      flow: 'fresh',
      title: 'guidanceFreshTitle',
      detail: 'guidanceFreshDetail',
    };
  }
  if (
    value.status === 'unsupported_upgrade' &&
    value.mode === 'unsupported_upgrade' &&
    value.auth_entry === 'admin_session' &&
    value.reason === 'qualification_unavailable' &&
    value.authentication_required === true
  ) {
    return {
      tone: 'warn',
      flow: 'upgrade',
      title: 'guidanceUpgradeTitle',
      detail: 'guidanceUpgradeDetail',
    };
  }
  // A 1.x volume mounted at /data is the one recovery state this build also
  // names a host action for: the source itself is intact, only the file
  // ownership stops v2 from fencing it.
  if (
    value.status === 'recovery_required' &&
    value.mode === 'recovery' &&
    value.auth_entry === 'host_intervention' &&
    value.reason === 'source_ownership_unavailable' &&
    value.authentication_required === false
  ) {
    return {
      tone: 'warn',
      flow: 'ownership',
      retryable: true,
      copyKey: { title: 'ownershipGuidanceTitle', detail: 'ownershipGuidanceDetail' },
    };
  }
  if (
    value.status === 'recovery_required' &&
    value.mode === 'recovery' &&
    value.auth_entry === 'host_intervention' &&
    value.reason === 'recovery_host_intervention' &&
    value.authentication_required === false
  ) {
    return {
      tone: 'warn',
      flow: 'host',
      title: 'guidanceRecoveryTitle',
      detail: 'guidanceRecoveryDetail',
    };
  }
  if (
    value.status === 'unavailable' &&
    (value.mode === undefined || value.mode === null) &&
    value.auth_entry === 'unavailable' &&
    value.reason === 'observation_unavailable' &&
    value.authentication_required === false
  ) {
    return {
      tone: 'warn',
      flow: 'host',
      title: 'guidanceUnavailableTitle',
      retryable: true,
      detail: 'guidanceUnavailableDetail',
    };
  }
  return null;
}

function describeBootstrapError(
  err: unknown,
  stage: 'start' | 'generate' | 'commit' | 'refresh'
): ActionFeedback {
  if (err instanceof BootstrapTransportError) {
    if (err.kind === 'transport') {
      return {
        tone: 'warn',
        title: 'errorTransportTitle',
        detail:
          stage === 'commit'
            ? 'errorTransportCommitDetail'
            : stage === 'start'
              ? 'errorTransportStartDetail'
              : 'errorTransportOtherDetail',
      };
    }
    if (err.kind === 'rejected' || err.kind === 'authority') {
      return {
        tone: 'danger',
        title: 'errorRejectedTitle',
        detail: stage === 'start' ? 'errorRejectedStartDetail' : 'errorRejectedOtherDetail',
      };
    }
    if (err.kind === 'mismatch') {
      return {
        tone: 'danger',
        title: 'errorMismatchTitle',
        detail: 'errorMismatchDetail',
      };
    }
    if (err.kind === 'bad_request') {
      return {
        tone: 'danger',
        title: 'errorBadRequestTitle',
        detail: 'errorBadRequestDetail',
      };
    }
    if (err.kind === 'conflict') {
      return {
        tone: 'warn',
        title: 'errorConflictTitle',
        detail: 'errorConflictDetail',
      };
    }
    return {
      tone: 'danger',
      title: err.status ? 'errorServerTitleStatus' : 'errorServerTitle',
      detail: 'errorServerDetail',
      status: err.status,
    };
  }
  return {
    tone: 'danger',
    title: 'errorUnexpectedTitle',
    detail: 'errorUnexpectedDetail',
  };
}

export function FreshBootstrapPage({ handoff, transport }: FreshBootstrapPageProps) {
  const [lightTheme, setLightTheme] = useState(true);
  const [phase, setPhase] = useState<BootPhase>(handoff ? 'connecting' : 'no_handoff');
  const [operation, setOperation] = useState<FreshOperation | null>(null);
  const [initError, setInitError] = useState<ActionFeedback | null>(null);
  const [initErrorKind, setInitErrorKind] = useState<string | null>(null);
  const [discoveryNote, setDiscoveryNote] = useState<DiscoveryGuidance | null>(null);
  const [discoveryChecked, setDiscoveryChecked] = useState(false);
  const [discoveryAttempt, setDiscoveryAttempt] = useState(0);
  // Full Web starts on the Console's saved language (see fullweb/freshLanguage),
  // so this is the same choice the Console header menu shows. Selecting here
  // re-renders every Bootstrap and wizard string and writes that same store.
  const [language, setLanguage] = useState<Language>(() => {
    const current = fullWebLanguage();
    return isFreshLanguage(current) ? current : 'zh-CN';
  });
  const startedRef = useRef(false);
  const changeLanguage = useCallback((next: string) => {
    if (!isFreshLanguage(next)) return;
    // Set the table before the state update so the re-render already reads it.
    setFullWebLanguage(next);
    setLanguage(next);
    persistFreshLanguage(next);
  }, []);
  useEffect(() => {
    document.documentElement.classList.toggle('light', lightTheme);
    document.documentElement.setAttribute('data-theme', lightTheme ? 'white' : 'dark');
  }, [lightTheme]);
  const executeStart = useCallback(
    async (targetHandoff: Handoff) => {
      setPhase('connecting');
      setInitError(null);
      setInitErrorKind(null);
      try {
        const op = await transport.start(targetHandoff);
        setOperation(op);
        setPhase('ready');
      } catch (err) {
        const kind = err instanceof BootstrapTransportError ? err.kind : 'unknown';
        setInitErrorKind(kind);
        setInitError(describeBootstrapError(err, 'start'));
        setPhase('init_error');
      }
    },
    [transport]
  );

  useEffect(() => {
    if (handoff) return;
    let active = true;
    void fetch('/.well-known/cpamp-bootstrap', {
      method: 'GET',
      mode: 'same-origin',
      credentials: 'omit',
      redirect: 'error',
      cache: 'no-store',
      headers: { Accept: 'application/json' },
    })
      .then(async (response) => {
        if (response.status !== 200 && response.status !== 503) return null;
        const payload: unknown = await response.json();
        if (!payload || typeof payload !== 'object') return null;
        // Public discovery uses 200 for classified sources and 503 only for
        // unavailable. A mismatched HTTP status must never suggest Fresh setup.
        const unavailable = (payload as Record<string, unknown>).status === 'unavailable';
        return unavailable === (response.status === 503) ? payload : null;
      })
      .then((payload: unknown) => {
        if (!active) return;
        setDiscoveryNote(publicDiscoveryGuidance(payload));
        setDiscoveryChecked(true);
      })
      .catch(() => {
        // An unverified source must not receive Fresh setup guidance.
        if (active) setDiscoveryChecked(true);
      });
    return () => {
      active = false;
    };
  }, [handoff, discoveryAttempt]);

  useEffect(() => {
    // main.tsx remounts this boundary for each handoff/hash change. The
    // initial phase already covers a missing handoff without another render.
    if (!handoff) return;
    if (startedRef.current) {
      return;
    }
    startedRef.current = true;
    // A newly mounted host handoff starts one external claim exchange. The
    // callback also owns the pending/error transitions used by manual retry.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void executeStart(handoff);
  }, [handoff, executeStart]);

  if (!handoff && discoveryNote?.flow === 'upgrade') return <Suspense fallback={<></>}><UpgradeAuthenticationEntry /></Suspense>;

  return (
    <div className="bootstrap-shell" data-testid="fresh-bootstrap-shell">
      <header className="bootstrap-header">
        <div className="brand-group">
          <img className="journey-logo" src={lightTheme ? logo : logoOnDark} alt="CPAMP" />
          <span className="badge badge-indigo">
            {phase === 'no_handoff' && discoveryNote?.flow !== 'fresh'
              ? journey('preflight')
              : journey('preview')}
          </span>
          {operation && (
            <span
              className={`badge ${operation.productComplete ? 'badge-emerald' : 'badge-amber'}`}
              data-testid="gateway-status-badge"
            >
              {operation.productComplete ? shellText('ready') : journey('pending')}
            </span>
          )}
        </div>
        <div className="header-actions">
          <label className="bootstrap-language">
            <FullWebIcon name="globe" size={22} />
            <select
              className="bootstrap-language-select"
              data-testid="bootstrap-language"
              aria-label={bootstrapText('languageSwitch')}
              title={bootstrapText('languageSwitch')}
              value={language}
              onChange={(event) => changeLanguage(event.target.value)}
            >
              {FRESH_LANGUAGES.map((value) => (
                <option key={value} value={value}>
                  {bootstrapText(LANGUAGE_NAME_KEYS[value])}
                </option>
              ))}
            </select>
          </label>
          <button
            type="button"
            className="btn btn-ghost btn-icon-only"
            onClick={() => setLightTheme((prev) => !prev)}
            aria-label={journey('theme')}
            title={journey('theme')}
          >
            <FullWebIcon name={lightTheme ? 'moon' : 'sun'} size={24} />
          </button>
          <span className="journey-version">v2</span>
        </div>
      </header>

      <main className="bootstrap-main">
        <div className={`wizard-container ${phase === 'ready' ? 'wizard-container-setup' : ''}`}>
          {phase === 'no_handoff' && (
            <section className="wizard-card" data-testid="bootstrap-no-handoff">
              <div className="wizard-card-header">
                <div className="wizard-step-tag">
                  <span>
                    {discoveryNote?.flow === 'ownership'
                      ? discoveryText('ownershipStepTag')
                      : bootstrapText(
                          discoveryNote?.flow === 'fresh' ? 'gateTagBootstrap' : 'gateTagPreflight'
                        )}
                  </span>
                </div>
                <h1 className="wizard-title">
                  {discoveryNote?.flow === 'ownership'
                    ? discoveryText('ownershipPageTitle')
                    : discoveryNote?.flow === 'host'
                      ? bootstrapText('gateHostTitle')
                      : discoveryNote?.flow === 'fresh'
                        ? journey('claimRequired')
                        : bootstrapText(
                            discoveryChecked ? 'gateUnconfirmedTitle' : 'gateCheckingTitle'
                          )}
                </h1>
                <p className="wizard-desc">
                  {discoveryNote?.flow === 'ownership'
                    ? discoveryText('ownershipPageDesc')
                    : discoveryNote?.flow === 'host'
                      ? bootstrapText('gateHostDesc')
                      : discoveryNote?.flow === 'fresh'
                        ? journey('claimRequiredHelp')
                        : bootstrapText('gateUnconfirmedDesc')}
                </p>
              </div>
              <div className="wizard-card-body">
                {discoveryNote && (
                  <div
                    className={`alert ${discoveryNote.tone === 'warn' ? 'alert-warn' : 'alert-info'}`}
                    data-testid="bootstrap-discovery-guidance"
                    role="status"
                  >
                    <div className="alert-content">
                      <div className="alert-title">{guidanceCopy(discoveryNote).title}</div>
                      <div className="alert-desc">{guidanceCopy(discoveryNote).detail}</div>
                    </div>
                  </div>
                )}

                {discoveryNote?.flow === 'ownership' ? (
                  <div
                    className="legacy-ownership-steps"
                    data-testid="legacy-ownership-instructions"
                  >
                    <div className="alert alert-info">
                      <div className="alert-content">
                        <div className="alert-title">{discoveryText('ownershipUpgradeTitle')}</div>
                        <div className="alert-desc">{discoveryText('ownershipUpgradeDesc')}</div>
                        <div className="code-block" data-testid="legacy-upgrade-preflight-command">
                          {`docker run --rm --entrypoint cpa-manager-plus -v <1x_volume>:/legacy:ro -v <v2_volume>:/data seakee/cpa-manager-plus:${__APP_VERSION__} upgrade preflight --legacy /legacy --source-version v1.14.10 --target-dir /data --json`}
                        </div>
                      </div>
                    </div>
                    <div className="alert alert-warn">
                      <div className="alert-content">
                        <div className="alert-title">{discoveryText('ownershipChownTitle')}</div>
                        <div className="alert-desc">{discoveryText('ownershipChownDesc')}</div>
                        <div className="code-block" data-testid="legacy-ownership-command">
                          {`docker stop <v2_container>
docker run --rm -u 0:0 --entrypoint sh -v <1x_volume>:/data seakee/cpa-manager-plus:${__APP_VERSION__} -c 'chown -h 10002:10002 /data/usage.sqlite* /data/data.key'
docker start <v2_container>`}
                        </div>
                      </div>
                    </div>
                  </div>
                ) : discoveryNote?.flow === 'host' ? (
                  <div className="alert alert-warn" data-testid="host-preflight-instructions">
                    <div className="alert-content">
                      <div className="alert-title">{bootstrapText('preflightTitle')}</div>
                      <div className="alert-desc">{bootstrapText('preflightDesc')}</div>
                      <div className="code-block" data-testid="host-preflight-command">
                        cpa-manager-plus legacy-preflight --db /path/to/usage.sqlite --summary
                      </div>
                    </div>
                  </div>
                ) : discoveryNote?.flow === 'fresh' ? (
                  <>
                    <div className="alert alert-warn">
                      <FullWebIcon name="alert" size={22} />
                      <div className="alert-content">
                        <div className="alert-title">{bootstrapText('handoffHelpTitle')}</div>
                        <div className="alert-desc">
                          {bootstrapText('handoffHelpLead')}
                          <code>#handoff=...</code>
                          {bootstrapText('handoffHelpTail')}
                        </div>
                      </div>
                    </div>
                    <div className="code-block" data-testid="cli-handoff-command">
                      cpa-manager-plus initialize
                    </div>
                  </>
                ) : (
                  <div
                    className="alert alert-warn"
                    data-testid="bootstrap-discovery-unconfirmed"
                    role="status"
                  >
                    <div className="alert-content">
                      <div className="alert-title">
                        {bootstrapText(discoveryChecked ? 'unconfirmedTitle' : 'checkingTitle')}
                      </div>
                      <div className="alert-desc">{bootstrapText('unconfirmedDesc')}</div>
                    </div>
                  </div>
                )}
                {discoveryChecked && (!discoveryNote || discoveryNote.retryable) && (
                  <button
                    className="btn btn-secondary"
                    type="button"
                    data-testid="bootstrap-recheck-discovery"
                    onClick={() => {
                      // Reissue only the public read-only discovery GET.
                      // An unverified result never grants a claim or write authority.
                      setDiscoveryChecked(false);
                      setDiscoveryNote(null);
                      setDiscoveryAttempt((attempt) => attempt + 1);
                    }}
                  >
                    {discoveryNote?.flow === 'ownership'
                      ? discoveryText('recheckButton')
                      : bootstrapText('recheck')}
                  </button>
                )}
              </div>
            </section>
          )}

          {phase === 'connecting' && (
            <section className="wizard-card" data-testid="bootstrap-connecting">
              <div className="wizard-card-header">
                <div className="wizard-step-tag">
                  <span>{journey('claim')}</span>
                </div>
                <h1 className="wizard-title">
                  {handoff?.resume ? journey('resuming') : journey('claiming')}
                </h1>
                <p className="wizard-desc">{journey('claimingHelp')}</p>
              </div>
              <div className="wizard-card-body">
                <details>
                  <summary>{journey('diagnostics')}</summary>
                  <div className="info-card">
                    <div className="info-row">
                      <span className="info-key">{bootstrapText('diagnosticsMode')}</span>
                      <span className="info-val">
                        {bootstrapText(
                          handoff?.resume ? 'diagnosticsModeResume' : 'diagnosticsModeClaim'
                        )}
                      </span>
                    </div>
                    {handoff && (
                      <>
                        <div className="info-row">
                          <span className="info-key">{bootstrapText('diagnosticsOperationId')}</span>
                          <span className="info-val">{handoff.identity.operationId}</span>
                        </div>
                        <div className="info-row">
                          <span className="info-key">{bootstrapText('diagnosticsInstallation')}</span>
                          <span className="info-val">
                            {handoff.identity.installationId} · {handoff.identity.generation}
                          </span>
                        </div>
                      </>
                    )}
                  </div>
                </details>
              </div>
            </section>
          )}

          {phase === 'init_error' && initError && (
            <section className="wizard-card" data-testid="bootstrap-authority-error">
              <div className="wizard-card-header">
                <div className="wizard-step-tag">
                  <span>{bootstrapText('authorityFailedTag')}</span>
                </div>
                <h1 className="wizard-title">{bootstrapText('authorityFailedTitle')}</h1>
                <p className="wizard-desc">{bootstrapText('authorityFailedDesc')}</p>
              </div>
              <div className="wizard-card-body">
                <div
                  className={`alert ${initError.tone === 'warn' ? 'alert-warn' : 'alert-danger'}`}
                  role="alert"
                >
                  <FullWebIcon name="alert" size={22} />
                  <div className="alert-content">
                    <div className="alert-title" data-testid="init-error-title">
                      {feedbackText(initError, 'title')}
                    </div>
                    <div className="alert-desc" data-testid="init-error-detail">
                      {feedbackText(initError, 'detail')}
                    </div>
                  </div>
                </div>

                {handoff && (
                  <div className="info-card">
                    <div className="info-row">
                      <span className="info-key">{bootstrapText('diagnosticsOperationId')}</span>
                      <span className="info-val">{handoff.identity.operationId}</span>
                    </div>
                    <div className="info-row">
                      <span className="info-key">{bootstrapText('claimGeneration')}</span>
                      <span className="info-val">#{handoff.claimGeneration}</span>
                    </div>
                    <div className="info-row">
                      <span className="info-key">{bootstrapText('handoffMode')}</span>
                      <span className="info-val">
                        {bootstrapText(handoff.resume ? 'handoffModeResume' : 'handoffModeFresh')}
                      </span>
                    </div>
                  </div>
                )}

                <div className="code-block">cpa-manager-plus initialize</div>
              </div>
              {handoff && (initErrorKind === 'transport' || initErrorKind === 'unavailable') && (
                <div className="wizard-card-footer">
                  <span className="bootstrap-retry-note">{bootstrapText('reconnectNote')}</span>
                  <button
                    type="button"
                    className="btn btn-primary"
                    data-testid="retry-start-btn"
                    onClick={() => void executeStart(handoff)}
                  >
                    {bootstrapText('retryBootstrap')}
                  </button>
                </div>
              )}
            </section>
          )}

          {phase === 'ready' && operation && (
            <FreshSetupWizard
              initialOperation={operation}
              transport={transport}
              onOperation={setOperation}
            />
          )}
        </div>
      </main>
      <footer className="journey-copyright">{journey('footer')}</footer>
    </div>
  );
}
