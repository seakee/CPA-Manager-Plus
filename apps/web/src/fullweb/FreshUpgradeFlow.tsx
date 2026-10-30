import { useCallback, useEffect, useMemo, useState } from 'react';
import { fullWebLanguage } from '../i18n/fullweb';
import {
  blockedCodeText,
  checkNameText,
  checkOutcomeText,
  domainText,
  nextActionText,
  stageText,
  upgradeWizardText as t,
} from '../i18n/upgradeWizard';
import { BOOTSTRAP_ROOT } from './bootstrapTransport';
import { FullWebIcon } from './FullWebIcon';
import { readRoutingContext } from './routingContext';

// The wizard is a reader. The two routes it uses only report what the operation
// recorded: the read-only preflight report and the operation row projection.
// Nothing here starts, advances or repairs an operation, and the copy-in itself
// is started from the Manager host (`cpa-manager-plus upgrade run`).
export const UPGRADE_WIZARD_CONTRACT = 'cpamp.upgrade-wizard.v1';

export type UpgradeWizardResource =
  | 'installation/upgrade/preflight'
  | 'installation/upgrade/status';

// A reader resolves one wizard resource and rejects with UpgradeWizardError.
// It is injected so the steps can be driven without a server, and so the entry
// owns the one credential this surface ever holds.
export type UpgradeWizardRead = (
  resource: UpgradeWizardResource,
  signal: AbortSignal
) => Promise<unknown>;

export type UpgradeWizardFailure = 'authentication' | 'network' | 'unavailable' | 'response';

export class UpgradeWizardError extends Error {
  constructor(
    readonly failure: UpgradeWizardFailure,
    readonly code = '',
    readonly status = 0
  ) {
    super(failure);
    this.name = 'UpgradeWizardError';
  }
}

interface PreflightCheck {
  name: string;
  status: string;
  code: string;
  detail: string;
}

interface PreflightGaps {
  deletedEventRefs: number;
  missingProjection: number;
  mismatchedProjection: number;
  pastWatermark: number;
  projectionStateUsable: boolean;
  coverageDaysWithGaps: number;
  affectedDates: string[];
}

export interface UpgradePreflight {
  sourceKind: string;
  sourceVersion: string;
  fingerprint: string;
  checks: PreflightCheck[];
  parityGaps: PreflightGaps;
  blocked: string[];
  ready: boolean;
}

export interface UpgradeStatus {
  recorded: boolean;
  stage: string;
  stageKnown: boolean;
  nextAction: string;
  domainsDone: string[];
  sourceKind: string;
  sourceVersion: string;
  mismatchDays: string[];
  manualItems: string[];
  errorCode: string;
  errorDetail: string;
}

const PREFLIGHT_TIMEOUT_MS = 45_000;
const STATUS_TIMEOUT_MS = 15_000;
// Same rule as the Adopt flow: three consecutive read failures stop the poll
// and ask the operator, instead of retrying a surface that is not answering.
const MAX_CONSECUTIVE_FAILURES = 3;
const DEFAULT_POLL_DELAYS = [1000, 2000, 4000, 8000, 15000];
const TERMINAL_STAGES = ['complete', 'failed', 'rolled_back'];

function text(value: unknown): string {
  return typeof value === 'string' ? value : '';
}

function strings(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === 'string') : [];
}

function count(value: unknown): number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? value : 0;
}

// Only a document this build can read is rendered. A reply that carries the
// wrong contract, a contradictory readiness flag or missing fields is a read
// failure, never a page built from guesses.
export function parseUpgradePreflight(value: unknown): UpgradePreflight | null {
  if (!value || typeof value !== 'object') return null;
  const document = value as Record<string, unknown>;
  if (document.contractVersion !== UPGRADE_WIZARD_CONTRACT) return null;
  if (!Array.isArray(document.checks)) return null;
  if (typeof document.ready !== 'boolean') return null;
  const checks: PreflightCheck[] = [];
  for (const item of document.checks) {
    if (!item || typeof item !== 'object') return null;
    const check = item as Record<string, unknown>;
    if (typeof check.name !== 'string' || typeof check.status !== 'string') return null;
    checks.push({
      name: check.name,
      status: check.status,
      code: text(check.code),
      detail: text(check.detail),
    });
  }
  const gaps = (document.parityGaps ?? {}) as Record<string, unknown>;
  const blocked = strings(document.blocked);
  // The report cannot be ready while it names a blocking code.
  if (document.ready !== (blocked.length === 0)) return null;
  return {
    sourceKind: text(document.sourceKind),
    sourceVersion: text(document.sourceVersion),
    fingerprint: text(document.fingerprint),
    checks,
    parityGaps: {
      deletedEventRefs: count(gaps.deletedEventRefs),
      missingProjection: count(gaps.missingProjection),
      mismatchedProjection: count(gaps.mismatchedProjection),
      pastWatermark: count(gaps.pastWatermark),
      projectionStateUsable: gaps.projectionStateUsable === true,
      coverageDaysWithGaps: count(gaps.coverageDaysWithGaps),
      affectedDates: strings(gaps.affectedDates),
    },
    blocked,
    ready: document.ready,
  };
}

export function parseUpgradeStatus(value: unknown): UpgradeStatus | null {
  if (!value || typeof value !== 'object') return null;
  const document = value as Record<string, unknown>;
  if (document.contractVersion !== UPGRADE_WIZARD_CONTRACT) return null;
  if (typeof document.recorded !== 'boolean') return null;
  if (typeof document.stage !== 'string' || typeof document.stageKnown !== 'boolean') return null;
  if (typeof document.nextAction !== 'string') return null;
  const reconciliation = (document.reconciliation ?? {}) as Record<string, unknown>;
  return {
    recorded: document.recorded,
    stage: document.stage,
    stageKnown: document.stageKnown,
    nextAction: document.nextAction,
    domainsDone: strings(document.domainsDone),
    sourceKind: text(document.sourceKind),
    sourceVersion: text(document.sourceVersion),
    mismatchDays: strings(reconciliation.mismatchDays),
    manualItems: strings(document.manualItems),
    errorCode: text(document.errorCode),
    errorDetail: text(document.errorDetail),
  };
}

// The canonical continuation prefix owns these reads once routing is committed;
// before that the fixed Bootstrap API does. Neither is discovered from browser
// state beyond the verified routing context the Manager itself served.
function upgradeAPIBase(): string {
  try {
    const context = readRoutingContext(document);
    if (context) return context.apiBase;
  } catch {
    /* No document: the fixed Bootstrap API owns the read. */
  }
  return `${BOOTSTRAP_ROOT}/api/v2`;
}

// The credential is a getter so the entry keeps it in its own ref: the wizard
// never stores, renders or copies the key.
export function createUpgradeWizardRead(
  credential: () => string,
  transport: typeof fetch = globalThis.fetch
): UpgradeWizardRead {
  return async (resource, signal) => {
    const base = upgradeAPIBase();
    const target = new URL(`${base}/${resource}`, window.location.origin);
    if (target.origin !== window.location.origin || target.pathname !== `${base}/${resource}`) {
      throw new UpgradeWizardError('unavailable');
    }
    const key = credential();
    if (!key) throw new UpgradeWizardError('authentication');
    const controller = new AbortController();
    const abort = () => controller.abort();
    signal.addEventListener('abort', abort);
    const timer = window.setTimeout(
      abort,
      resource === 'installation/upgrade/preflight' ? PREFLIGHT_TIMEOUT_MS : STATUS_TIMEOUT_MS
    );
    try {
      let response: Response;
      try {
        response = await transport(target.href, {
          method: 'GET',
          headers: { Accept: 'application/json', Authorization: `Bearer ${key}` },
          signal: controller.signal,
          mode: 'same-origin',
          credentials: 'omit',
          redirect: 'error',
          cache: 'no-store',
        });
      } catch {
        throw new UpgradeWizardError(signal.aborted ? 'unavailable' : 'network');
      }
      if (response.status === 401 || response.status === 403) {
        throw new UpgradeWizardError('authentication', '', response.status);
      }
      if (!response.ok) {
        // The routes answer a typed code instead of a bare status. Only that
        // bounded envelope is read; any other body keeps the status alone.
        let code = '';
        try {
          const payload = (await response.json()) as { error?: { code?: unknown } };
          if (typeof payload?.error?.code === 'string') code = payload.error.code;
        } catch {
          /* A failure without an envelope stays a plain status failure. */
        }
        throw new UpgradeWizardError('unavailable', code, response.status);
      }
      try {
        return await response.json();
      } catch {
        throw new UpgradeWizardError('response');
      }
    } finally {
      window.clearTimeout(timer);
      signal.removeEventListener('abort', abort);
    }
  };
}

function asWizardError(error: unknown): UpgradeWizardError {
  return error instanceof UpgradeWizardError ? error : new UpgradeWizardError('network');
}

function errorText(error: UpgradeWizardError | undefined, language: string): string {
  switch (error?.failure) {
    case 'authentication':
      return t('errorAuthentication', language);
    case 'network':
      return t('errorNetwork', language);
    case 'response':
      return t('errorResponse', language);
    case 'unavailable':
      return error.code ? blockedCodeText(error.code, language) : t('errorUnavailable', language);
    default:
      return t('errorUnavailable', language);
  }
}

// A recorded stage settles the page only when the operation itself says it is
// over, or when this build cannot name the stage. A stage this build does not
// know is shown verbatim instead of being treated as progress.
function operationSettled(status: UpgradeStatus | undefined): boolean {
  if (!status) return false;
  if (!status.stageKnown) return true;
  return TERMINAL_STAGES.includes(status.stage);
}

type Step = 'preflight' | 'confirm' | 'progress' | 'result';
const STEP_ORDER: Step[] = ['preflight', 'confirm', 'progress', 'result'];
const STEP_TEXT = {
  preflight: 'stepPreflight',
  confirm: 'stepConfirm',
  progress: 'stepProgress',
  result: 'stepResult',
} as const;
const CONFIRM_MANUAL = [
  'confirmManualKeys',
  'confirmManualCredentials',
  'confirmManualGateway',
  'confirmManualHistory',
] as const;

type PreflightState =
  | { phase: 'idle' }
  | { phase: 'ok'; value: UpgradePreflight }
  | { phase: 'failed'; error: UpgradeWizardError };

type StatusState = {
  phase: 'idle' | 'live' | 'stalled';
  value?: UpgradeStatus;
  error?: UpgradeWizardError;
  failures: number;
};

/**
 * The Full Web Upgrade wizard. Four steps over the two read-only routes:
 * preflight, confirm, progress and result. Every sentence about what the
 * upgrade does is written as a description of the operation, because this page
 * can only read it.
 */
export function FreshUpgradeFlow({
  read,
  language = fullWebLanguage(),
  pollDelays = DEFAULT_POLL_DELAYS,
}: {
  read: UpgradeWizardRead;
  language?: string;
  pollDelays?: number[];
}) {
  const [step, setStep] = useState<Step>('preflight');
  const [preflight, setPreflight] = useState<PreflightState>({ phase: 'idle' });
  const [acknowledged, setAcknowledged] = useState(false);
  const [status, setStatus] = useState<StatusState>({ phase: 'idle', failures: 0 });
  const [attempt, setAttempt] = useState(0);

  const delayFor = useCallback(
    (index: number) => pollDelays[Math.min(index, pollDelays.length - 1)] ?? 15_000,
    [pollDelays]
  );

  // Step 1 reads the preflight once. A retry resets the observation to `idle`,
  // which is what re-runs this effect.
  useEffect(() => {
    if (step !== 'preflight' || preflight.phase !== 'idle') return;
    const controller = new AbortController();
    void (async () => {
      try {
        const raw = await read('installation/upgrade/preflight', controller.signal);
        const parsed = parseUpgradePreflight(raw);
        if (controller.signal.aborted) return;
        if (!parsed) throw new UpgradeWizardError('response');
        setPreflight({ phase: 'ok', value: parsed });
      } catch (error) {
        if (controller.signal.aborted) return;
        setPreflight({ phase: 'failed', error: asWizardError(error) });
      }
    })();
    return () => controller.abort();
  }, [preflight.phase, read, step]);

  // Step 3 polls the operation row with a growing delay. It stops when the
  // operation settles, when this build cannot name the stage, or after three
  // consecutive failures; a retry starts a fresh observation.
  useEffect(() => {
    if (step !== 'progress') return;
    let stopped = false;
    let timer: number | undefined;
    let delayIndex = 0;
    let failures = 0;
    const controller = new AbortController();
    const tick = async () => {
      try {
        const raw = await read('installation/upgrade/status', controller.signal);
        const parsed = parseUpgradeStatus(raw);
        if (stopped) return;
        if (!parsed) throw new UpgradeWizardError('response');
        failures = 0;
        delayIndex = 0;
        setStatus({ phase: 'live', value: parsed, failures: 0 });
        if (operationSettled(parsed)) return;
        timer = window.setTimeout(tick, delayFor(delayIndex++));
      } catch (error) {
        if (stopped) return;
        failures += 1;
        const failure = asWizardError(error);
        setStatus((current) => ({
          phase: failures >= MAX_CONSECUTIVE_FAILURES ? 'stalled' : current.phase,
          value: current.value,
          error: failure,
          failures,
        }));
        if (failures >= MAX_CONSECUTIVE_FAILURES) return;
        timer = window.setTimeout(tick, delayFor(delayIndex++));
      }
    };
    void tick();
    return () => {
      stopped = true;
      if (timer !== undefined) window.clearTimeout(timer);
      controller.abort();
    };
  }, [attempt, delayFor, read, step]);

  const shownStep: Step =
    step === 'progress' && operationSettled(status.value) ? 'result' : step;
  const record = status.value;
  const preflightReport = preflight.phase === 'ok' ? preflight.value : undefined;

  const reread = () => {
    setStatus({ phase: 'idle', failures: 0 });
    setAttempt((current) => current + 1);
  };

  return (
    <section className="upgrade-flow" data-testid="upgrade-flow" aria-label={t('flowLabel', language)}>
      <ol className="upgrade-steps" data-testid="upgrade-flow-steps">
        {STEP_ORDER.map((key, index) => {
          const current = STEP_ORDER.indexOf(shownStep);
          return (
            <li
              key={key}
              data-testid={`upgrade-step-${key}`}
              data-state={index < current ? 'done' : key === shownStep ? 'current' : 'pending'}
              aria-current={key === shownStep ? 'step' : undefined}
            >
              <span className="upgrade-step-index" aria-hidden="true">
                {index + 1}
              </span>
              <span>{t(STEP_TEXT[key], language)}</span>
            </li>
          );
        })}
      </ol>

      {shownStep === 'preflight' && (
        <div className="upgrade-step-body" data-testid="upgrade-preflight">
          <h2>{t('preflightTitle', language)}</h2>
          <p className="upgrade-step-help">{t('preflightHelp', language)}</p>
          {preflight.phase === 'idle' && (
            <p className="upgrade-step-status" role="status" data-testid="upgrade-preflight-reading">
              <FullWebIcon name="refresh" size={18} />
              {t('preflightReading', language)}
            </p>
          )}
          {preflight.phase === 'failed' && (
            <div className="upgrade-notice" role="alert" data-testid="upgrade-preflight-error">
              <FullWebIcon name="alert" size={24} />
              <div>
                <h3>{t('errorUnavailable', language)}</h3>
                <p>{errorText(preflight.error, language)}</p>
                {preflight.error.code && <code>{preflight.error.code}</code>}
              </div>
            </div>
          )}
          {preflightReport && (
            <>
              <dl className="upgrade-facts">
                <div>
                  <dt>{t('preflightSourceKind', language)}</dt>
                  <dd data-testid="upgrade-preflight-kind">
                    {preflightReport.sourceKind || t('preflightUnknown', language)}
                  </dd>
                </div>
                <div>
                  <dt>{t('preflightSourceVersion', language)}</dt>
                  <dd data-testid="upgrade-preflight-version">
                    {preflightReport.sourceVersion || t('preflightUnknown', language)}
                  </dd>
                </div>
                <div>
                  <dt>{t('preflightFingerprint', language)}</dt>
                  <dd data-testid="upgrade-preflight-fingerprint">
                    {preflightReport.fingerprint || t('preflightUnknown', language)}
                  </dd>
                </div>
              </dl>
              <h3>{t('preflightChecks', language)}</h3>
              <ul className="upgrade-checks">
                {preflightReport.checks.map((check) => (
                  <li
                    key={check.name}
                    data-testid={`upgrade-check-${check.name}`}
                    data-status={check.status}
                  >
                    <span className="upgrade-check-name">{checkNameText(check.name, language)}</span>
                    <span className={`upgrade-check-outcome is-${check.status}`}>
                      {checkOutcomeText(check.status, language)}
                    </span>
                    {check.code && <code>{check.code}</code>}
                    {check.detail && <span className="upgrade-check-detail">{check.detail}</span>}
                  </li>
                ))}
              </ul>
              {preflightReport.blocked.length > 0 ? (
                <div className="upgrade-notice" role="alert" data-testid="upgrade-preflight-blocked">
                  <FullWebIcon name="alert" size={24} />
                  <div>
                    <h3>{t('preflightBlockedTitle', language)}</h3>
                    <p>{t('preflightBlockedHelp', language)}</p>
                    <ul>
                      {preflightReport.blocked.map((code) => (
                        <li key={code} data-testid={`upgrade-blocked-${code}`}>
                          <span>{blockedCodeText(code, language)}</span>
                          <code>{code}</code>
                        </li>
                      ))}
                    </ul>
                  </div>
                </div>
              ) : (
                <div
                  className="upgrade-notice upgrade-notice-ready"
                  role="status"
                  data-testid="upgrade-preflight-ready"
                >
                  <FullWebIcon name="check" size={24} />
                  <div>
                    <h3>{t('preflightReadyTitle', language)}</h3>
                    <p>{t('preflightReadyHelp', language)}</p>
                  </div>
                </div>
              )}
              <ParityGaps gaps={preflightReport.parityGaps} language={language} />
            </>
          )}
          <div className="upgrade-actions">
            <button
              className="btn btn-secondary"
              data-testid="upgrade-preflight-retry"
              disabled={preflight.phase === 'idle'}
              onClick={() => setPreflight({ phase: 'idle' })}
            >
              <FullWebIcon name="refresh" size={18} /> {t('preflightRetry', language)}
            </button>
            <button
              className="btn btn-primary"
              data-testid="upgrade-continue"
              disabled={!preflightReport?.ready}
              onClick={() => setStep('confirm')}
            >
              {t('continue', language)} <FullWebIcon name="arrowRight" size={18} />
            </button>
          </div>
        </div>
      )}

      {shownStep === 'confirm' && (
        <div className="upgrade-step-body" data-testid="upgrade-confirm">
          <h2>{t('confirmTitle', language)}</h2>
          <p className="upgrade-step-help">{t('confirmHelp', language)}</p>
          <dl className="upgrade-facts">
            <div>
              <dt>{t('confirmSourceKind', language)}</dt>
              <dd data-testid="upgrade-confirm-kind">
                {preflightReport?.sourceKind || t('preflightUnknown', language)}
              </dd>
            </div>
            <div>
              <dt>{t('confirmSourceVersion', language)}</dt>
              <dd data-testid="upgrade-confirm-version">
                {preflightReport?.sourceVersion || t('preflightUnknown', language)}
              </dd>
            </div>
          </dl>
          <div className="upgrade-notice upgrade-notice-ready">
            <FullWebIcon name="shield" size={24} />
            <div>
              <h3>{t('confirmReadOnlyTitle', language)}</h3>
              <p>{t('confirmReadOnlyHelp', language)}</p>
            </div>
          </div>
          <div className="upgrade-notice upgrade-notice-ready">
            <FullWebIcon name="server" size={24} />
            <div>
              <h3>{t('confirmBackupTitle', language)}</h3>
              <p>{t('confirmBackupHelp', language)}</p>
            </div>
          </div>
          <h3>{t('confirmManualTitle', language)}</h3>
          <p className="upgrade-step-help">{t('confirmManualHelp', language)}</p>
          <ul className="upgrade-list" data-testid="upgrade-confirm-manual">
            {CONFIRM_MANUAL.map((key) => (
              <li key={key}>{t(key, language)}</li>
            ))}
          </ul>
          <label className="upgrade-ack">
            <input
              type="checkbox"
              data-testid="upgrade-confirm-ack"
              checked={acknowledged}
              onChange={(event) => setAcknowledged(event.target.checked)}
            />
            <span>{t('confirmAck', language)}</span>
          </label>
          <div className="upgrade-actions">
            <button
              className="btn btn-secondary"
              data-testid="upgrade-confirm-back"
              onClick={() => setStep('preflight')}
            >
              <FullWebIcon name="arrowLeft" size={18} /> {t('back', language)}
            </button>
            <button
              className="btn btn-primary"
              data-testid="upgrade-confirm-continue"
              disabled={!acknowledged}
              onClick={() => setStep('progress')}
            >
              {t('confirmContinue', language)} <FullWebIcon name="arrowRight" size={18} />
            </button>
          </div>
        </div>
      )}

      {shownStep === 'progress' && (
        <div className="upgrade-step-body" data-testid="upgrade-progress">
          <h2>{t('progressTitle', language)}</h2>
          <p className="upgrade-step-help">{t('progressHelp', language)}</p>
          {!record && (
            <p className="upgrade-step-status" role="status" data-testid="upgrade-progress-reading">
              <FullWebIcon name="refresh" size={18} />
              {t('progressReading', language)}
            </p>
          )}
          {record && !record.recorded && (
            <p className="upgrade-step-status" role="status" data-testid="upgrade-progress-unrecorded">
              <FullWebIcon name="clock" size={18} />
              {t('progressUnrecorded', language)}
            </p>
          )}
          {record?.recorded && (
            <>
              <StageLine status={record} language={language} testid="upgrade-progress-stage" />
              <h3>{t('progressDomains', language)}</h3>
              {record.domainsDone.length === 0 ? (
                <p className="upgrade-step-help" data-testid="upgrade-progress-domains">
                  {t('progressDomainsNone', language)}
                </p>
              ) : (
                <ul className="upgrade-domains" data-testid="upgrade-progress-domains">
                  {record.domainsDone.map((domain) => (
                    <li key={domain}>{domainText(domain, language)}</li>
                  ))}
                </ul>
              )}
              <Reconciliation status={record} language={language} />
              <ManualItems status={record} language={language} />
            </>
          )}
          {record && status.phase !== 'stalled' && (
            <p className="upgrade-step-help" data-testid="upgrade-progress-waiting">
              {t('progressWaiting', language)}
            </p>
          )}
          {status.phase === 'stalled' && (
            <div className="upgrade-notice" role="alert" data-testid="upgrade-progress-stalled">
              <FullWebIcon name="alert" size={24} />
              <div>
                <h3>{t('progressStalledTitle', language)}</h3>
                <p>{t('progressStalledHelp', language)}</p>
                <p>{errorText(status.error, language)}</p>
                {status.error?.code && <code>{status.error.code}</code>}
              </div>
            </div>
          )}
          <div className="upgrade-actions">
            <button
              className="btn btn-primary"
              data-testid="upgrade-progress-retry"
              onClick={reread}
            >
              <FullWebIcon name="refresh" size={18} /> {t('progressRetry', language)}
            </button>
          </div>
        </div>
      )}

      {shownStep === 'result' && (
        <div className="upgrade-step-body" data-testid="upgrade-result">
          <ResultHeadline status={record} language={language} />
          <p className="upgrade-step-help">
            {record
              ? record.stage === 'complete'
                ? t('resultCompleteHelp', language)
                : t('resultIncompleteHelp', language)
              : t('resultIncompleteHelp', language)}
          </p>
          {record && (
            <>
              <StageLine status={record} language={language} testid="upgrade-result-stage" />
              {record.stage === 'rolled_back' && (
                <div className="upgrade-notice" role="alert" data-testid="upgrade-result-rollback">
                  <FullWebIcon name="alert" size={24} />
                  <div>
                    <h3>{t('resultRolledBackTitle', language)}</h3>
                    <p>
                      {t('resultRollbackReason', language)}:{' '}
                      {record.errorDetail || t('resultRollbackReasonUnknown', language)}
                    </p>
                    {record.errorDetail && record.errorCode && <code>{record.errorCode}</code>}
                    <p>{t('resultRollbackRestart', language)}</p>
                  </div>
                </div>
              )}
              {record.stage === 'failed' && (
                <div className="upgrade-notice" role="alert" data-testid="upgrade-result-failed">
                  <FullWebIcon name="alert" size={24} />
                  <div>
                    <h3>{t('resultFailedTitle', language)}</h3>
                    <p>{t('resultFailedHelp', language)}</p>
                    {record.errorDetail && <p>{record.errorDetail}</p>}
                    {record.errorCode && <code>{record.errorCode}</code>}
                  </div>
                </div>
              )}
              {!record.stageKnown && (
                <div className="upgrade-notice" data-testid="upgrade-result-unreadable">
                  <FullWebIcon name="info" size={24} />
                  <div>
                    <h3>{t('resultUnreadableTitle', language)}</h3>
                    <p>{t('resultUnreadableHelp', language)}</p>
                  </div>
                </div>
              )}
              {(record.sourceKind || record.sourceVersion) && (
                <dl className="upgrade-facts">
                  <div>
                    <dt>{t('resultSource', language)}</dt>
                    <dd data-testid="upgrade-result-source">
                      {[record.sourceKind, record.sourceVersion].filter(Boolean).join(' · ')}
                    </dd>
                  </div>
                </dl>
              )}
              <Reconciliation status={record} language={language} />
              <ManualItems status={record} language={language} />
              {nextActionText(record.nextAction, language) && (
                <p className="upgrade-step-help" data-testid="upgrade-result-next">
                  {t('resultNext', language)}: {nextActionText(record.nextAction, language)}
                </p>
              )}
            </>
          )}
          <div className="upgrade-actions">
            <button className="btn btn-primary" data-testid="upgrade-result-reread" onClick={reread}>
              <FullWebIcon name="refresh" size={18} /> {t('resultReread', language)}
            </button>
          </div>
        </div>
      )}
    </section>
  );
}

// The success headline is rendered for `complete` and nothing else: a failed,
// rolled-back or unrecognised operation always gets the incomplete headline.
function ResultHeadline({ status, language }: { status?: UpgradeStatus; language: string }) {
  if (status?.stage === 'complete' && status.stageKnown) {
    return (
      <h2 className="upgrade-result-headline is-complete" data-testid="upgrade-result-complete">
        {t('resultCompleteTitle', language)}
      </h2>
    );
  }
  return (
    <h2 className="upgrade-result-headline" data-testid="upgrade-result-incomplete">
      {t('resultIncompleteTitle', language)}
    </h2>
  );
}

function StageLine({
  status,
  language,
  testid,
}: {
  status: UpgradeStatus;
  language: string;
  testid: string;
}) {
  const label = stageText(status.stage, language);
  return (
    <div className="upgrade-stage" data-testid={testid}>
      <span className="upgrade-stage-label">{t('progressStage', language)}</span>
      <span className="upgrade-stage-value">
        {label ? label : status.stage ? <code>{status.stage}</code> : t('preflightUnknown', language)}
      </span>
      <span className="upgrade-stage-note">{t('progressStageNote', language)}</span>
    </div>
  );
}

function ParityGaps({ gaps, language }: { gaps: PreflightGaps; language: string }) {
  const total =
    gaps.deletedEventRefs +
    gaps.missingProjection +
    gaps.mismatchedProjection +
    gaps.pastWatermark +
    gaps.coverageDaysWithGaps;
  if (total === 0 && gaps.affectedDates.length === 0) return null;
  return (
    <div className="upgrade-gaps" data-testid="upgrade-preflight-gaps">
      <h3>{t('preflightGapsTitle', language)}</h3>
      <p className="upgrade-step-help">{t('preflightGapsHelp', language)}</p>
      <p>
        {t('preflightGapsDays', language)}: {gaps.coverageDaysWithGaps}
      </p>
      {gaps.affectedDates.length > 0 && (
        <ul className="upgrade-days">
          {gaps.affectedDates.map((day) => (
            <li key={day}>
              <code>{day}</code>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function Reconciliation({ status, language }: { status: UpgradeStatus; language: string }) {
  return (
    <div className="upgrade-reconciliation" data-testid="upgrade-reconciliation">
      <h3>{t('resultReconciliationTitle', language)}</h3>
      <p className="upgrade-step-help">{t('resultReconciliationHelp', language)}</p>
      {status.mismatchDays.length === 0 ? (
        <p data-testid="upgrade-reconciliation-none">{t('resultReconciliationNone', language)}</p>
      ) : (
        <ul className="upgrade-days" data-testid="upgrade-mismatch-days">
          {status.mismatchDays.map((day) => (
            <li key={day} data-testid={`upgrade-mismatch-${day}`}>
              <code>{day}</code>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

// Manual items are recorded by the operation in its own vocabulary. They are
// listed exactly as recorded: this page has no mapping for a value it has not
// seen, and dropping one would hide work the operator still owes.
function ManualItems({ status, language }: { status: UpgradeStatus; language: string }) {
  return (
    <div className="upgrade-manual" data-testid="upgrade-manual-items">
      <h3>{t('resultManualTitle', language)}</h3>
      {status.manualItems.length === 0 ? (
        <p data-testid="upgrade-manual-none">{t('resultManualNone', language)}</p>
      ) : (
        <ul className="upgrade-list">
          {status.manualItems.map((item) => (
            <li key={item}>{item}</li>
          ))}
        </ul>
      )}
    </div>
  );
}
