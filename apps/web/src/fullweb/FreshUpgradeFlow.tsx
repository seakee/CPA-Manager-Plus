import { useCallback, useEffect, useState } from 'react';
import { fullWebLanguage } from '../i18n/fullweb';
import {
  blockedCodeText,
  checkNameText,
  checkOutcomeText,
  domainText,
  nextActionText,
  reconciliationBoundText,
  reconciliationTotalText,
  stageText,
  upgradeWizardText as t,
} from '../i18n/upgradeWizard';
import { BOOTSTRAP_ROOT } from './bootstrapTransport';
import { FreshArtifactStep } from './FreshArtifactStep';
import type { ArtifactTransportLike } from './freshArtifact';
import { FullWebIcon } from './FullWebIcon';
import { readRoutingContext } from './routingContext';
// The wizard reads the operation and runs the one action that can advance it.
// The reads only report what the operation recorded: the read-only preflight
// report and the operation row projection. The action is the same completion gate
// the command line runs, so the page can never declare the upgrade finished on
// its own. The copy-in, the reconciliation, the CPA import and the connection are
// started from the Manager host.
export const UPGRADE_WIZARD_CONTRACT = 'cpamp.upgrade-wizard.v1';
const SOURCE_VERSION_UNKNOWN = 'unknown';

function formatPreflightSourceVersion(
  report: UpgradePreflight | undefined,
  language: string | undefined
): string {
  const reported = report?.sourceVersion ?? '';
  if (reported && reported !== SOURCE_VERSION_UNKNOWN) {
    return reported;
  }
  if (report?.pendingDeclaration && report.expectedVersion) {
    return `${t('checkPending', language)} (--source-version ${report.expectedVersion})`;
  }
  return t('preflightUnknown', language);
}

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

// The action takes no resource argument: this surface has exactly one, and a
// caller that could name its own path could post somewhere nothing proves.
export type UpgradeWizardVerify = (signal: AbortSignal) => Promise<unknown>;

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
  // The declaration this read cannot make. Present on every document, so
  // readiness is never inferred from a missing field, and deliberately separate
  // from the CLI's exact-equality acceptance: the check behind it is pending,
  // not passed.
  pendingDeclaration: boolean;
  expectedVersion: string;
}

export interface UpgradeStatus {
  recorded: boolean;
  stage: string;
  stageKnown: boolean;
  nextAction: string;
  domainsDone: string[];
  sourceKind: string;
  sourceVersion: string;
  // The listed days are a window: total counts every recorded mismatch day, so a
  // cut list is rendered as a window instead of reading as the whole history.
  mismatchDays: string[];
  mismatchDaysTotal: number;
  mismatchDaysTruncated: boolean;
  manualItems: string[];
  errorCode: string;
  errorDetail: string;
}

const PREFLIGHT_TIMEOUT_MS = 45_000;
const STATUS_TIMEOUT_MS = 15_000;
// The action outlives the reads on purpose: the server holds the source fence,
// fingerprints the volume twice and commits a transaction inside its own bound,
// and this client bound only exists so a hung connection does not leave the page
// waiting forever.
const VERIFY_TIMEOUT_MS = 60_000;
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
  // A pending declaration is a fact the document states, not one to infer: it
  // must agree with the one row it can describe, and it is only usable when it
  // carries the version the copy-in needs. Other checks may still block the same
  // source — the wizard never declares a version, so a pending row can coexist
  // with a disk, parity or ownership refusal, and hiding that refusal would hide
  // the diagnosis the operator opened this step for.
  if (typeof document.pendingDeclaration !== 'boolean') return null;
  const pendingDeclaration = document.pendingDeclaration;
  const expectedVersion = text(document.expectedVersion);
  const rowPending = checks.some((check) => check.name === 'source_version' && check.status === 'pending');
  if (pendingDeclaration !== rowPending) return null;
  if (pendingDeclaration) {
    if (expectedVersion === '') return null;
    // The row is pending, so the same check cannot also be settled or refused.
    if (
      checks.some(
        (check) =>
          check.name === 'source_version' && (check.status === 'blocked' || check.status === 'pass')
      )
    ) {
      return null;
    }
    // A declaration still owed is not a refusal: those codes describe the
    // blocked answer, not the pending one.
    if (
      blocked.includes('upgrade_source_version_undeclared') ||
      blocked.includes('upgrade_source_version_unsupported')
    ) {
      return null;
    }
  } else if (expectedVersion !== '') {
    return null;
  }
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
    pendingDeclaration,
    expectedVersion,
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
  const mismatchDays = strings(reconciliation.mismatchDays);
  const mismatchDaysTotal = count(reconciliation.mismatchDaysTotal);
  const mismatchDaysTruncated = reconciliation.mismatchDaysTruncated === true;
  // The bound is part of the document: the total can never be smaller than the
  // list it stands for, and the flag is exactly the difference between them. A
  // reply that contradicts this is not a history this build can present.
  if (mismatchDaysTotal < mismatchDays.length) return null;
  if (mismatchDaysTruncated !== mismatchDaysTotal > mismatchDays.length) return null;
  return {
    recorded: document.recorded,
    stage: document.stage,
    stageKnown: document.stageKnown,
    nextAction: document.nextAction,
    domainsDone: strings(document.domainsDone),
    sourceKind: text(document.sourceKind),
    sourceVersion: text(document.sourceVersion),
    mismatchDays,
    mismatchDaysTotal,
    mismatchDaysTruncated,
    manualItems: strings(document.manualItems),
    errorCode: text(document.errorCode),
    errorDetail: text(document.errorDetail),
  };
}

// The action's answer. It reports the position the gate recorded in the same
// vocabulary the status projection uses, so the page presents the action's own
// result and then re-reads the row instead of inventing a stage in between.
export interface UpgradeVerifyOutcome {
  stage: string;
  stageKnown: boolean;
  complete: boolean;
  targetMode: string;
  domainsDone: string[];
  reasonCode: string;
  reason: string;
  manualItems: string[];
  alreadyVerified: boolean;
  updatedAtMs: number;
}

// Only a document this build can read is presented. The action wrote something
// when it answered 200, so a reply that does not describe a position is a read
// failure: the page then re-reads the row rather than guessing what happened.
// The gate completes both target modes, so the only position a reply may carry is
// `complete`; there is no waiting position to present.
export function parseUpgradeVerify(value: unknown): UpgradeVerifyOutcome | null {
  if (!value || typeof value !== 'object') return null;
  const document = value as Record<string, unknown>;
  if (document.contractVersion !== UPGRADE_WIZARD_CONTRACT) return null;
  if (typeof document.recorded !== 'boolean' || document.recorded !== true) return null;
  if (typeof document.stage !== 'string' || typeof document.stageKnown !== 'boolean') return null;
  if (typeof document.complete !== 'boolean') return null;
  if (typeof document.alreadyVerified !== 'boolean') return null;
  // The action only ever answers from a stage it can name, and only the two
  // stages the gate records are positions this page may present.
  if (document.stageKnown !== true) return null;
  if (document.stage !== 'complete' && document.stage !== 'verify') return null;
  if (document.complete !== (document.stage === 'complete')) return null;
  const targetMode = text(document.targetMode);
  if (targetMode !== 'external' && targetMode !== 'managed') return null;
  const reasonCode = text(document.reasonCode);
  // Both target modes complete through the gate, and this build has no waiting
  // position: a reply that is not a completion is a position it never records.
  if (!document.complete) return null;
  return {
    stage: document.stage,
    stageKnown: document.stageKnown,
    complete: document.complete,
    targetMode,
    domainsDone: strings(document.domainsDone),
    reasonCode,
    reason: text(document.reason),
    manualItems: strings(document.manualItems),
    alreadyVerified: document.alreadyVerified,
    updatedAtMs: count(document.updatedAtMs),
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
function createUpgradeWizardRequest(credential: () => string, transport: typeof fetch) {
  return async (resource: UpgradeWizardResource | 'installation/upgrade/verify', method: 'GET' | 'POST', signal: AbortSignal) => {
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
    // The action is bounded by the server's own verification deadline, so this
    // client bound is larger: a premature client abort would hide the typed
    // verdict the server was about to answer with.
    const timer = window.setTimeout(
      abort,
      method === 'POST'
        ? VERIFY_TIMEOUT_MS
        : resource === 'installation/upgrade/preflight'
          ? PREFLIGHT_TIMEOUT_MS
          : STATUS_TIMEOUT_MS
    );
    try {
      let response: Response;
      try {
        response = await transport(target.href, {
          method,
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

export function createUpgradeWizardRead(
  credential: () => string,
  transport: typeof fetch = globalThis.fetch
): UpgradeWizardRead {
  const request = createUpgradeWizardRequest(credential, transport);
  return (resource, signal) => request(resource, 'GET', signal);
}

// The action's transport. It is a POST to the one verification route: the route
// refuses every other verb, so a bookmark or a prefetch of this address cannot
// record anything.
export function createUpgradeWizardVerify(
  credential: () => string,
  transport: typeof fetch = globalThis.fetch
): UpgradeWizardVerify {
  const request = createUpgradeWizardRequest(credential, transport);
  return (signal) => request('installation/upgrade/verify', 'POST', signal);
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

type VerifyState =
  | { phase: 'idle' }
  | { phase: 'running' }
  | { phase: 'done'; value: UpgradeVerifyOutcome }
  | { phase: 'failed'; error: UpgradeWizardError };

// The wizard boots on the recorded operation, not on the preflight. An operation
// that already recorded a stage must stay readable after a refresh, after the
// 1.x volume was unmounted, and after a rollback handed the source back to its
// old writer; requiring a ready preflight before the status could be read is what
// stranded exactly those cases. The preflight therefore runs only when the store
// says there is no operation to resume.
type BootState =
  | { phase: 'reading' }
  | { phase: 'failed'; error: UpgradeWizardError }
  | { phase: 'ready'; resumed: boolean };
/**
 * The Full Web Upgrade wizard. Four steps over the two read-only routes:
 * preflight, confirm, progress and result. Every sentence about what the
 * upgrade does is written as a description of the operation, because this page
 * can only read it.
 *
 * It reads the recorded operation before anything else. An operation that has
 * already recorded a stage resumes at that stage (progress, or the result when it
 * is terminal) without a preflight; the preflight is the first step only when the
 * store says nothing is recorded. A status read that fails is a typed failure
 * with its own retry — it is not evidence that no operation exists.
 */
export function FreshUpgradeFlow({
  read,
  verify,
  artifact,
  language = fullWebLanguage(),
  pollDelays = DEFAULT_POLL_DELAYS,
  onRecordedOperation,
}: {
  read: UpgradeWizardRead;
  // The one action of this surface. It is injected exactly like the reads, so a
  // wizard without it offers no way to advance the operation and the page stays
  // a reader.
  verify?: UpgradeWizardVerify;
  artifact?: ArtifactTransportLike;
  language?: string;
  pollDelays?: number[];
  // The host page needs the one fact the read establishes — whether an operation
  // is recorded — to keep its own copy honest: a recorded operation is not a
  // source with no migration path.
  onRecordedOperation?: (recorded: boolean) => void;
}) {
  const [step, setStep] = useState<Step>('preflight');
  const [boot, setBoot] = useState<BootState>({ phase: 'reading' });
  const [preflight, setPreflight] = useState<PreflightState>({ phase: 'idle' });
  const [acknowledged, setAcknowledged] = useState(false);
  const [status, setStatus] = useState<StatusState>({ phase: 'idle', failures: 0 });
  const [verification, setVerification] = useState<VerifyState>({ phase: 'idle' });
  const [artifactReady, setArtifactReady] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const delayFor = useCallback(
    (index: number) => pollDelays[Math.min(index, pollDelays.length - 1)] ?? 15_000,
    [pollDelays]
  );

  // Boot reads the recorded operation once, before any preflight. A recorded
  // operation is resumed where it stands; only an empty record asks for
  // preflight. The read is left in `reading` until it answers, so a retry is one
  // state change and a failure is never mistaken for "nothing recorded".
  useEffect(() => {
    if (boot.phase !== 'reading') return;
    const controller = new AbortController();
    let stopped = false;
    void (async () => {
      try {
        const raw = await read('installation/upgrade/status', controller.signal);
        const parsed = parseUpgradeStatus(raw);
        if (stopped) return;
        if (!parsed) throw new UpgradeWizardError('response');
        onRecordedOperation?.(parsed.recorded);
        if (parsed.recorded) {
          // The first answer is the resume: the poll effect continues from it
          // instead of asking the operator to qualify the source again.
          setStatus({ phase: 'live', value: parsed, failures: 0 });
          setStep('progress');
        }
        setBoot({ phase: 'ready', resumed: parsed.recorded });
      } catch (error) {
        if (stopped) return;
        setBoot({ phase: 'failed', error: asWizardError(error) });
      }
    })();
    return () => {
      stopped = true;
      controller.abort();
    };
  }, [boot.phase, read, onRecordedOperation]);

  // The preflight runs for a source that has no operation yet, and only then. A
  // retry resets the observation to `idle`, which is what re-runs this effect.
  useEffect(() => {
    if (boot.phase !== 'ready' || step !== 'preflight' || preflight.phase !== 'idle') return;
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
  }, [boot.phase, preflight.phase, read, step]);

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
  const showArtifactStep =
    Boolean(artifact) && Boolean(record?.recorded) && record?.nextAction === 'cpa_import';

  useEffect(() => {
    if (!showArtifactStep) {
      setArtifactReady(false);
    }
  }, [showArtifactStep]);
  const reread = () => {
    setStatus({ phase: 'idle', failures: 0 });
    setAttempt((current) => current + 1);
  };

  // The action runs once per press and never retries itself: a verification
  // writes the store, so a silent retry would record a verdict the operator did
  // not ask for twice. Its answer is presented as the action's own result, and
  // the recorded row is re-read afterwards so the page shows what the store
  // holds instead of what this reply happened to say.
  const runVerification = async () => {
    if (!verify || verification.phase === 'running') return;
    setVerification({ phase: 'running' });
    try {
      const parsed = parseUpgradeVerify(await verify(new AbortController().signal));
      if (!parsed) throw new UpgradeWizardError('response');
      setVerification({ phase: 'done', value: parsed });
      reread();
    } catch (error) {
      setVerification({ phase: 'failed', error: asWizardError(error) });
    }
  };

  return (
    <section className="upgrade-flow" data-testid="upgrade-flow" aria-label={t('flowLabel', language)}>
      {boot.phase === 'reading' && (
        <div className="upgrade-step-body" data-testid="upgrade-boot">
          <h2>{t('bootTitle', language)}</h2>
          <p className="upgrade-step-help">{t('bootHelp', language)}</p>
          <p className="upgrade-step-status" role="status" data-testid="upgrade-boot-reading">
            <FullWebIcon name="refresh" size={18} />
            {t('bootReading', language)}
          </p>
        </div>
      )}
      {boot.phase === 'failed' && (
        <div className="upgrade-step-body" data-testid="upgrade-boot-failed">
          <h2>{t('bootTitle', language)}</h2>
          <p className="upgrade-step-help">{t('bootHelp', language)}</p>
          <div className="upgrade-notice" role="alert">
            <FullWebIcon name="alert" size={24} />
            <div>
              <h3>{t('bootFailedTitle', language)}</h3>
              <p>{errorText(boot.error, language)}</p>
              {boot.error.code && <code>{boot.error.code}</code>}
            </div>
          </div>
          <div className="upgrade-actions">
            <button
              className="btn btn-primary"
              data-testid="upgrade-boot-retry"
              onClick={() => setBoot({ phase: 'reading' })}
            >
              <FullWebIcon name="refresh" size={18} /> {t('bootRetry', language)}
            </button>
          </div>
        </div>
      )}
      {boot.phase === 'ready' && (
        <>
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
          {boot.resumed && (
            <p className="upgrade-step-help" data-testid="upgrade-resumed">
              {t('resumeNotice', language)}
            </p>
          )}
        </>
      )}

      {boot.phase === 'ready' && shownStep === 'preflight' && (
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
                    {formatPreflightSourceVersion(preflightReport, language)}
                  </dd>
                </div>
                <div>
                  <dt>{t('preflightFingerprint', language)}</dt>
                  <dd data-testid="upgrade-preflight-fingerprint">
                    {preflightReport.fingerprint || t('preflightUnknown', language)}
                  </dd>
                </div>
              </dl>
              {preflightReport.pendingDeclaration && (
                <div className="upgrade-pending" role="note" data-testid="upgrade-preflight-pending-declaration">
                  <h3>{t('preflightPendingTitle', language)}</h3>
                  <p>
                    {t('preflightPendingHelp', language)}{' '}
                    <code>{`--source-version ${preflightReport.expectedVersion}`}</code>
                  </p>
                  <p>{t('preflightPendingTail', language)}</p>
                </div>
              )}
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
                    <h3>
                      {t(
                        preflightReport.pendingDeclaration ||
                          preflightReport.checks.some((check) => check.status === 'pending')
                          ? 'preflightReadyPendingTitle'
                          : 'preflightReadyTitle',
                        language
                      )}
                    </h3>
                    <p>
                      {t(
                        preflightReport.pendingDeclaration ||
                          preflightReport.checks.some((check) => check.status === 'pending')
                          ? 'preflightReadyPendingHelp'
                          : 'preflightReadyHelp',
                        language
                      )}
                    </p>
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

      {boot.phase === 'ready' && shownStep === 'confirm' && (
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
                {formatPreflightSourceVersion(preflightReport, language)}
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

      {boot.phase === 'ready' && shownStep === 'progress' && (
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
              {record.errorCode && (
                // The recorded refusal is the operation's own statement about
                // why it stopped. The verification of a Managed target records
                // one here too — the position it may not promote — so the
                // operator reads the reason from the row, not from a page.
                <div
                  className="upgrade-notice"
                  role="status"
                  data-testid="upgrade-progress-reason"
                >
                  <FullWebIcon name="info" size={24} />
                  <div>
                    <h3>{t('verifyReasonLabel', language)}</h3>
                    <p>{record.errorDetail}</p>
                    <code>{record.errorCode}</code>
                  </div>
                </div>
              )}
            </>
          )}
          {showArtifactStep && artifact && (
            <div className="upgrade-artifact" data-testid="upgrade-cpa-import-artifact">
              <FreshArtifactStep
                transport={artifact}
                onReady={() => setArtifactReady(true)}
              />
              {artifactReady && (
                <div
                  className="upgrade-notice upgrade-notice-ready"
                  role="status"
                  data-testid="upgrade-cpa-import-ready"
                >
                  <FullWebIcon name="check" size={24} />
                  <div>
                    <h3>{t('cpaImportReadyTitle', language)}</h3>
                    <p>
                      {t('cpaImportReadyHelp', language)} <code>upgrade cpa-import</code>
                    </p>
                  </div>
                </div>
              )}
            </div>
          )}
          {record?.recorded && record.nextAction === 'verify' && verify && (
            <div className="upgrade-notice" data-testid="upgrade-verify-offer">
              <FullWebIcon name="shield" size={24} />
              <div>
                <h3>{t('verifyTitle', language)}</h3>
                <p>{t('verifyHelp', language)}</p>
                <p>{t('verifySourceRequired', language)}</p>
              </div>
            </div>
          )}
          {verification.phase === 'running' && (
            <p className="upgrade-step-status" role="status" data-testid="upgrade-verify-running">
              <FullWebIcon name="refresh" size={18} />
              {t('verifyRunning', language)}
            </p>
          )}
          {verification.phase === 'done' && (
            <div
              className="upgrade-notice upgrade-notice-ready"
              role="status"
              data-testid="upgrade-verify-done"
            >
              <FullWebIcon name="check" size={24} />
              <div>
                <h3>{t('verifyDoneTitle', language)}</h3>
                <p data-testid="upgrade-verify-outcome">{t('verifyComplete', language)}</p>
                {verification.value.alreadyVerified && (
                  <p data-testid="upgrade-verify-already">{t('verifyAlready', language)}</p>
                )}
                {verification.value.targetMode && (
                  <p data-testid="upgrade-verify-target">
                    {verification.value.targetMode === 'external'
                      ? t('verifyTargetExternal', language)
                      : t('verifyTargetManaged', language)}
                  </p>
                )}
              </div>
            </div>
          )}
          {verification.phase === 'failed' && (
            <div className="upgrade-notice" role="alert" data-testid="upgrade-verify-failed">
              <FullWebIcon name="alert" size={24} />
              <div>
                <h3>{t('verifyFailedTitle', language)}</h3>
                <p>{errorText(verification.error, language)}</p>
                {verification.error.code && <code>{verification.error.code}</code>}
              </div>
            </div>
          )}
          {record?.recorded && record.nextAction === 'verify' && verify && (
            <div className="upgrade-actions">
              <button
                className="btn btn-primary"
                data-testid="upgrade-verify-action"
                disabled={verification.phase === 'running'}
                onClick={runVerification}
              >
                <FullWebIcon name="shield" size={18} />{' '}
                {verification.phase === 'done'
                  ? t('verifyRetry', language)
                  : t('verifyAction', language)}
              </button>
            </div>
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
          {status.phase === 'stalled' && (
            <div className="upgrade-actions">
              <button
                className="btn btn-primary"
                data-testid="upgrade-progress-retry"
                onClick={reread}
              >
                <FullWebIcon name="refresh" size={18} /> {t('progressRetry', language)}
              </button>
            </div>
          )}
        </div>
      )}

      {boot.phase === 'ready' && shownStep === 'result' && (
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
        <>
          {/* The total is rendered independently of the list: a window is never
              presented as the whole history. */}
          <p className="upgrade-step-help" data-testid="upgrade-reconciliation-total">
            {reconciliationTotalText(status.mismatchDaysTotal, language)}
          </p>
          {status.mismatchDaysTruncated && (
            <p className="upgrade-step-help" data-testid="upgrade-reconciliation-bound">
              {reconciliationBoundText(status.mismatchDays.length, language)}
            </p>
          )}
          <ul className="upgrade-days" data-testid="upgrade-mismatch-days">
            {status.mismatchDays.map((day) => (
              <li key={day} data-testid={`upgrade-mismatch-${day}`}>
                <code>{day}</code>
              </li>
            ))}
          </ul>
        </>
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
