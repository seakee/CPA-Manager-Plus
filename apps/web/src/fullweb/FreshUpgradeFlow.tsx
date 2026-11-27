import { useCallback, useEffect, useRef, useState } from 'react';
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
  upgradeExecutionText as e,
  upgradeWizardText as t,
} from '../i18n/upgradeWizard';
import {
  buildPrefixPreviewUrls,
  generateRandomAccessPrefix,
  isCanonicalPrefix,
  isSameOrNestedPrefix,
  resolveBrowserOrigin,
  validatePrefixPair,
} from './accessPrefix';
import { BOOTSTRAP_ROOT } from './bootstrapTransport';
import {
  AccessInfoSummary,
  type AccessInfo,
  type AccessMode,
  type AccessProtocol,
} from './FreshAccessInfo';
import { FreshArtifactStep } from './FreshArtifactStep';
import type { ArtifactTransportLike } from './freshArtifact';
import { FullWebIcon } from './FullWebIcon';
import { isValidExactHeaderCredentialValue, PublicApiError, type PublicApiClient } from './publicApi';
import { readRoutingContext } from './routingContext';
// Recorded operations and worker observations are authoritative; accepted jobs
// never advance a local stage or imply that the unique verification gate passed.
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
    return `${t('checkPending', language)} (${report.expectedVersion})`;
  }
  return t('preflightUnknown', language);
}

function formatPreflightCpaBaseline(artifactId: string): string {
  if (artifactId.startsWith('sha256:') && artifactId.length > 19) {
    return `sha256:${artifactId.slice(7, 19)}…`;
  }
  return artifactId;
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

// Routing input submitted either to save the draft prefixes or together with verification.
export interface UpgradeRoutingInput {
  controlBasePath: string;
  inferenceBasePath: string;
  sharedEntry: boolean;
}

export interface UpgradeRoutingState {
  configured: boolean;
  committed: boolean;
  existingPrefixesPreserved: boolean;
  controlBasePath: string;
  inferenceBasePath: string;
  sharedEntry: boolean;
  externalBaseUrl: string;
  suggestedControlBasePath: string;
  suggestedInferenceBasePath: string;
}

// Save route for first-upgrade access prefixes (`POST installation/upgrade/routing`).
export type UpgradeWizardSaveRouting = (
  signal: AbortSignal,
  input: UpgradeRoutingInput
) => Promise<unknown>;

// The action takes no resource argument: this surface has exactly one, and a
// caller that could name its own path could post somewhere nothing proves.
export type UpgradeWizardVerify = (
  signal: AbortSignal,
  input?: UpgradeRoutingInput
) => Promise<unknown>;

export type UpgradeExecutionInput =
  | {
      action: 'reconcile';
      acknowledgeDays?: string[];
      acknowledgeAllMismatches?: boolean;
      acknowledgeParityGaps?: boolean;
    }
  | { action: 'cpa-import'; pluginsEnabled: boolean }
  | { action: 'external-connect'; baseUrl?: string; managementKey?: string }
  | { action: 'verify' }
  | { action: 'activate' };
export type UpgradeWizardExecute = (signal: AbortSignal, input: UpgradeExecutionInput) => Promise<unknown>;
export interface UpgradeExecution {
  available: boolean;
  running: boolean;
  action?: UpgradeExecutionInput['action'];
  code?: string;
  managedAvailable: boolean;
}
const EXECUTION_ACTIONS = ['reconcile', 'cpa-import', 'external-connect', 'verify', 'activate'] as const;
export function parseUpgradeExecution(value: unknown): UpgradeExecution | null {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
  const doc = value as Record<string, unknown>;
  if (typeof doc.available !== 'boolean' || typeof doc.running !== 'boolean' ||
      typeof doc.managedAvailable !== 'boolean') return null;
  if (doc.action !== undefined && !EXECUTION_ACTIONS.includes(doc.action as UpgradeExecutionInput['action'])) return null;
  if (doc.code !== undefined && (typeof doc.code !== 'string' || !/^[a-zA-Z0-9_:-]{0,96}$/.test(doc.code))) return null;
  return {
    available: doc.available, running: doc.running, managedAvailable: doc.managedAvailable,
    ...(doc.action ? { action: doc.action as UpgradeExecutionInput['action'] } : {}),
    ...(doc.code ? { code: doc.code as string } : {}),
  };
}

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
  qualifiedCpaArtifactId?: string;
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
  hasParityGaps?: boolean;
  manualItems: string[];
  errorCode: string;
  errorDetail: string;
  targetMode?: string;
  routing?: UpgradeRoutingState;
  accessInfo?: AccessInfo;
  canonicalConfirmed?: boolean;
  bootstrapRetiredRecovery?: boolean;
  execution?: UpgradeExecution;
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
const CANONICAL_CONFIRMED_MARKER = Symbol('cpamp.upgrade.canonicalConfirmed');
const BOOTSTRAP_RETIRED_RECOVERY_MARKER = Symbol('cpamp.upgrade.bootstrapRetiredRecovery');

export function markCanonicalUpgradeStatus<T extends Record<string, unknown>>(value: T): T {
  Object.defineProperty(value, CANONICAL_CONFIRMED_MARKER, {
    value: true,
    enumerable: false,
    configurable: false,
  });
  return value;
}

function hasCanonicalUpgradeStatusMarker(value: unknown): boolean {
  return Boolean(
    value &&
      typeof value === 'object' &&
      (value as Record<symbol, unknown>)[CANONICAL_CONFIRMED_MARKER] === true
  );
}

export function markBootstrapRetiredRecovery<T extends Record<string, unknown>>(value: T): T {
  Object.defineProperty(value, BOOTSTRAP_RETIRED_RECOVERY_MARKER, {
    value: true,
    enumerable: false,
    configurable: false,
  });
  return value;
}

function hasBootstrapRetiredRecoveryMarker(value: unknown): boolean {
  return Boolean(
    value &&
      typeof value === 'object' &&
      (value as Record<symbol, unknown>)[BOOTSTRAP_RETIRED_RECOVERY_MARKER] === true
  );
}

function isResumableInFlightActivation(
  parsed: UpgradeStatus,
  activationPersisted = false
): boolean {
  if (
    !parsed.recorded ||
    parsed.stage !== 'complete' ||
    !parsed.stageKnown ||
    parsed.routing?.committed !== true ||
    parsed.accessInfo?.ready ||
    !parsed.execution
  ) {
    return false;
  }
  if (parsed.execution.action === 'activate' && !parsed.execution.code) {
    return true;
  }
  if (!activationPersisted && !parsed.bootstrapRetiredRecovery) {
    return false;
  }
  return Boolean(
    parsed.execution.available === false &&
      (!parsed.execution.code || parsed.execution.code === 'upgrade_worker_unavailable')
  );
}

function text(value: unknown): string {
  return typeof value === 'string' ? value : '';
}

function strings(value: unknown): string[] {
  return Array.isArray(value) ? value.filter((item): item is string => typeof item === 'string') : [];
}

function count(value: unknown): number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? value : 0;
}

function parseUpgradeRouting(value: unknown): UpgradeRoutingState | undefined {
  if (!value || typeof value !== 'object') return undefined;
  const doc = value as Record<string, unknown>;
  return {
    configured: doc.configured === true,
    committed: doc.committed === true,
    existingPrefixesPreserved: doc.existingPrefixesPreserved === true,
    controlBasePath: text(doc.controlBasePath),
    inferenceBasePath: text(doc.inferenceBasePath),
    sharedEntry: doc.sharedEntry === true,
    externalBaseUrl: text(doc.externalBaseUrl),
    suggestedControlBasePath: text(doc.suggestedControlBasePath),
    suggestedInferenceBasePath: text(doc.suggestedInferenceBasePath),
  };
}

function normalizeUpgradeAccessMode(rawMode: string, targetMode: string): AccessMode {
  if (rawMode === 'managed' || rawMode === 'migrate_legacy') return 'migrate_legacy';
  if (rawMode === 'external' || rawMode === 'connect_external') return 'connect_external';
  if (rawMode === 'new_managed' || rawMode === 'adopt_existing') return rawMode;
  if (targetMode === 'external') return 'connect_external';
  if (targetMode === 'managed') return 'migrate_legacy';
  return '';
}

function parseUpgradeAccessInfo(value: unknown, targetMode: string): AccessInfo | undefined {
  if (!value || typeof value !== 'object') return undefined;
  const doc = value as Record<string, unknown>;
  const mode = normalizeUpgradeAccessMode(text(doc.mode), targetMode);
  const externalBaseUrl = text(doc.externalBaseUrl);
  const rawProtocols: AccessProtocol[] = Array.isArray(doc.protocols)
    ? doc.protocols
        .filter((item): item is Record<string, unknown> => Boolean(item && typeof item === 'object'))
        .map((item) => ({
          id: text(item.id),
          label: text(item.label),
          basePath: text(item.basePath),
          examplePath: text(item.examplePath),
        }))
        .filter((item) => item.id !== '' || item.basePath !== '' || item.examplePath !== '')
    : [];
  const protocols = mode === 'connect_external' && !externalBaseUrl ? [] : rawProtocols;
  return {
    mode,
    managementBasePath: text(doc.managementBasePath),
    inferenceBasePath: mode === 'connect_external' ? '' : text(doc.inferenceBasePath),
    ...(mode !== 'connect_external' && doc.sharedEntry === true ? { sharedEntry: true } : {}),
    ...(externalBaseUrl ? { externalBaseUrl } : {}),
    protocols,
    ready: doc.ready === true,
    code: text(doc.code),
  };
}

const FAIL_CLOSED_ACCESS_CLIENT: PublicApiClient = {
  get: async () => {
    throw new PublicApiError('routing_unavailable');
  },
  post: async () => {
    throw new PublicApiError('routing_unavailable');
  },
  put: async () => {
    throw new PublicApiError('routing_unavailable');
  },
};
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
  let qualifiedCpaArtifactId = '';
  if (document.qualifiedCpaArtifactId !== undefined) {
    if (
      typeof document.qualifiedCpaArtifactId !== 'string' ||
      !/^sha256:[0-9a-f]{64}$/.test(document.qualifiedCpaArtifactId)
    ) {
      return null;
    }
    qualifiedCpaArtifactId = document.qualifiedCpaArtifactId;
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
    ...(qualifiedCpaArtifactId ? { qualifiedCpaArtifactId } : {}),
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
  const hasParityGaps = reconciliation.hasParityGaps === true;
  // The bound is part of the document: the total can never be smaller than the
  // list it stands for, and the flag is exactly the difference between them. A
  // reply that contradicts this is not a history this build can present.
  if (mismatchDaysTotal < mismatchDays.length) return null;
  if (mismatchDaysTruncated !== mismatchDaysTotal > mismatchDays.length) return null;
  const targetMode = text(document.targetMode);
  const routing = parseUpgradeRouting(document.routing);
  const accessInfo = parseUpgradeAccessInfo(document.accessInfo, targetMode);
  const execution = document.execution === undefined ? undefined : parseUpgradeExecution(document.execution);
  if (execution === null) return null;
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
    ...(hasParityGaps ? { hasParityGaps: true } : {}),
    manualItems: strings(document.manualItems),
    errorCode: text(document.errorCode),
    errorDetail: text(document.errorDetail),
    ...(targetMode ? { targetMode } : {}),
    ...(routing ? { routing } : {}),
    ...(accessInfo ? { accessInfo } : {}),
    ...(execution ? { execution } : {}),
    ...(hasCanonicalUpgradeStatusMarker(value) ? { canonicalConfirmed: true } : {}),
    ...(hasBootstrapRetiredRecoveryMarker(value) ? { bootstrapRetiredRecovery: true } : {}),
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
  routing?: UpgradeRoutingState;
  accessInfo?: AccessInfo;
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
  const routing = parseUpgradeRouting(document.routing);
  const accessInfo = parseUpgradeAccessInfo(document.accessInfo, targetMode);
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
    ...(routing ? { routing } : {}),
    ...(accessInfo ? { accessInfo } : {}),
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
function createUpgradeWizardRequest(
  credential: () => string, transport: typeof fetch, continuationBase?: () => string
) {
  return async (
    resource:
      | UpgradeWizardResource
      | 'bootstrap/identity'
      | 'operation'
      | 'api-keys/access-info'
      | 'installation/upgrade/verify'
      | 'installation/upgrade/routing'
      | 'installation/upgrade/execute',
    method: 'GET' | 'POST',
    signal: AbortSignal,
    body?: unknown
  ) => {
    const rawBase = continuationBase?.() || upgradeAPIBase();
    const base = rawBase.startsWith('/') ? rawBase.replace(/\/+$/, '') : rawBase;
    const target = new URL(`${base}/${resource}`, window.location.origin);
    if (target.origin !== window.location.origin || target.pathname !== `${base}/${resource}`) {
      throw new UpgradeWizardError('unavailable');
    }
    const key = resource === 'bootstrap/identity' ? '' : credential();
    if (!key && resource !== 'bootstrap/identity') throw new UpgradeWizardError('authentication');
    if (resource !== 'bootstrap/identity' && !isValidExactHeaderCredentialValue(key)) {
      throw new UpgradeWizardError('authentication', '', 401);
    }
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
      const headers: Record<string, string> = {
        Accept: 'application/json',
        ...(key ? { Authorization: `Bearer ${key}` } : {}),
      };
      if (body !== undefined) {
        headers['Content-Type'] = 'application/json';
      }
      try {
        response = await transport(target.href, {
          method,
          headers,
          ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
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
          const payload = (await response.json()) as {
            error?: { code?: unknown };
            code?: unknown;
            Code?: unknown;
          };
          if (typeof payload?.error?.code === 'string') code = payload.error.code;
          else if (typeof payload?.code === 'string') code = payload.code;
          else if (typeof payload?.Code === 'string') code = payload.Code;
          if (!/^[a-zA-Z0-9_:-]{1,96}$/.test(code)) code = '';
        } catch {
          /* A failure without an envelope stays a plain status failure. */
        }
        throw new UpgradeWizardError('unavailable', code, response.status);
      }
      if (resource === 'bootstrap/identity' && response.headers.get('X-CPAMP-Route-Owner') !== 'manager') {
        throw new UpgradeWizardError('response');
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
  transport: typeof fetch = globalThis.fetch,
  continuationBase?: () => string
): UpgradeWizardRead {
  const request = createUpgradeWizardRequest(credential, transport, continuationBase);
  return (resource, signal) => request(resource, 'GET', signal);
}

export function createUpgradeActivationRead(credential: () => string) {
  return (prefix: string, resource: 'bootstrap/identity' | 'operation' | 'api-keys/access-info', signal: AbortSignal) => {
    if (!isCanonicalPrefix(prefix)) throw new UpgradeWizardError('response');
    return createUpgradeWizardRequest(credential, globalThis.fetch, () => `${prefix}/api/v2`)(resource, 'GET', signal);
  };
}

// The action's transport. It is a POST to the one verification route: the route
// refuses every other verb, so a bookmark or a prefetch of this address cannot
// record anything.
export function createUpgradeWizardSaveRouting(
  credential: () => string,
  transport: typeof fetch = globalThis.fetch,
  continuationBase?: () => string
): UpgradeWizardSaveRouting {
  const request = createUpgradeWizardRequest(credential, transport, continuationBase);
  return (signal, input) => request('installation/upgrade/routing', 'POST', signal, input);
}

export function createUpgradeWizardVerify(
  credential: () => string,
  transport: typeof fetch = globalThis.fetch,
  continuationBase?: () => string
): UpgradeWizardVerify {
  const request = createUpgradeWizardRequest(credential, transport, continuationBase);
  return (signal, input) => request('installation/upgrade/verify', 'POST', signal, input);
}

export function createUpgradeWizardExecute(
  credential: () => string,
  transport: typeof fetch = globalThis.fetch,
  continuationBase?: () => string
): UpgradeWizardExecute {
  const request = createUpgradeWizardRequest(credential, transport, continuationBase);
  return (signal, input) => request('installation/upgrade/execute', 'POST', signal, input);
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
// Existing operations resume without repeating script-owned confirmation or copy-in.
export function FreshUpgradeFlow({
  read,
  verify,
  execute,
  saveRouting,
  artifact,
  language = fullWebLanguage(),
  pollDelays = DEFAULT_POLL_DELAYS,
  onRecordedOperation,
  onExecutionActivity,
  onActivation,
  canonicalConfirmed: canonicalConfirmedProp = false,
  bootstrapRetired = false,
  activationPersisted = false,
  onOpenConsole,
}: {
  read: UpgradeWizardRead;
  execute?: UpgradeWizardExecute;
  verify?: UpgradeWizardVerify;
  saveRouting?: UpgradeWizardSaveRouting;
  artifact?: ArtifactTransportLike;
  language?: string;
  pollDelays?: number[];
  // The host page needs the one fact the read establishes — whether an operation
  // is recorded — to keep its own copy honest: a recorded operation is not a
  // source with no migration path.
  onRecordedOperation?: (recorded: boolean) => void;
  onExecutionActivity?: (active: boolean) => void;
  onActivation?: (controlBasePath: string) => void;
  canonicalConfirmed?: boolean;
  bootstrapRetired?: boolean;
  activationPersisted?: boolean;
  onOpenConsole?: () => void;
}) {
  const [step, setStep] = useState<Step>('preflight');
  const [boot, setBoot] = useState<BootState>({ phase: 'reading' });
  const [preflight, setPreflight] = useState<PreflightState>({ phase: 'idle' });
  const [acknowledged, setAcknowledged] = useState(false);
  const [status, setStatus] = useState<StatusState>({ phase: 'idle', failures: 0 });
  const [verification, setVerification] = useState<VerifyState>({ phase: 'idle' });
  const [artifactReady, setArtifactReady] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const routingInitializedRef = useRef(false);
  const prevTargetModeRef = useRef<string | undefined>(undefined);
  const routingWriteEpochRef = useRef(0);
  const pendingWritesRef = useRef(0);
  const onRecordedOperationRef = useRef(onRecordedOperation);
  onRecordedOperationRef.current = onRecordedOperation;
  const [localAction, setLocalAction] = useState<UpgradeExecutionInput['action'] | null>(null);
  const actionPendingRef = useRef(false);
  const [executionError, setExecutionError] = useState<UpgradeWizardError>();
  const restartUntilRef = useRef(0);
  const restartingRef = useRef(false);
  const [restarting, setRestarting] = useState(false);
  const workerReadUntilRef = useRef(0);
  const activateDeliveryUnknownRef = useRef(false);
  const activateDeliveryFailureRef = useRef<UpgradeWizardError | undefined>(undefined);
  const postCutoverPollUntilRef = useRef(0);
  const bootstrapRetiredRef = useRef(bootstrapRetired);
  bootstrapRetiredRef.current = bootstrapRetired;
  const activationPersistedRef = useRef(activationPersisted);
  activationPersistedRef.current = activationPersisted;
  const resumedActivationRef = useRef(false);
  const [canonicalConfirmedState, setCanonicalConfirmedState] = useState(canonicalConfirmedProp);
  const canonicalConfirmedRef = useRef(canonicalConfirmedProp);
  const activityCallbackRef = useRef(onExecutionActivity);
  activityCallbackRef.current = onExecutionActivity;
  const onActivationRef = useRef(onActivation);
  onActivationRef.current = onActivation;
  const [controlPrefix, setControlPrefix] = useState('');
  const [inferencePrefix, setInferencePrefix] = useState('');
  const [sharedPrefix, setSharedPrefix] = useState(false);
  const [routingDirty, setRoutingDirty] = useState(false);
  const [routingSaving, setRoutingSaving] = useState(false);
  const [routingSaved, setRoutingSaved] = useState(false);
  const [routingError, setRoutingError] = useState('');
  const [routingDrawerOpen, setRoutingDrawerOpen] = useState(false);
  const [copiedPreviewId, setCopiedPreviewId] = useState<string | null>(null);
  const copyPreviewUrl = (id: string, value: string) => {
    if (!value || value === '—') return;
    if (typeof navigator !== 'undefined' && navigator.clipboard?.writeText) {
      void navigator.clipboard.writeText(value);
    }
    setCopiedPreviewId(id);
    if (typeof window !== 'undefined' && typeof window.setTimeout === 'function') {
      window.setTimeout(() => setCopiedPreviewId((cur) => (cur === id ? null : cur)), 1800);
    }
  };
  const delayFor = useCallback(
    (index: number) => pollDelays[Math.min(index, pollDelays.length - 1)] ?? 15_000,
    [pollDelays]
  );
  useEffect(() => {
    if (canonicalConfirmedProp) {
      canonicalConfirmedRef.current = true;
      setCanonicalConfirmedState(true);
    }
  }, [canonicalConfirmedProp]);
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
        onRecordedOperationRef.current?.(parsed.recorded);
        const parsedCanonicalConfirmed =
          canonicalConfirmedRef.current || Boolean(parsed.canonicalConfirmed);
        if (parsedCanonicalConfirmed) {
          canonicalConfirmedRef.current = true;
          setCanonicalConfirmedState(true);
          restartingRef.current = false;
          setRestarting(false);
          restartUntilRef.current = 0;
          activateDeliveryUnknownRef.current = false;
          activateDeliveryFailureRef.current = undefined;
          setExecutionError(undefined);
        }
        if (parsed.recorded) {
          if (!parsedCanonicalConfirmed && !restartingRef.current && !resumedActivationRef.current && isResumableInFlightActivation(parsed, activationPersistedRef.current)) {
            resumedActivationRef.current = true;
            activateDeliveryUnknownRef.current = false;
            activateDeliveryFailureRef.current = undefined;
            restartingRef.current = true;
            setRestarting(true);
            restartUntilRef.current = Date.now() + 120_000;
            const prefix =
              parsed.routing?.controlBasePath || parsed.accessInfo?.managementBasePath || '';
            if (isCanonicalPrefix(prefix)) onActivationRef.current?.(prefix);
          } else if (
            !parsedCanonicalConfirmed &&
            activationPersistedRef.current &&
            !isResumableInFlightActivation(parsed, true) &&
            !parsed.accessInfo?.ready &&
            (parsed.execution?.code ||
              (parsed.execution?.available &&
                !parsed.execution.running &&
                parsed.execution.action !== 'activate'))
          ) {
            onActivationRef.current?.('');
          }
          // The first answer is the resume: the poll effect continues from it
          // instead of asking the operator to qualify the source again.
          setStatus({ phase: 'live', value: parsed, failures: 0 });
          setStep('progress');
        }
        setBoot({ phase: 'ready', resumed: parsed.recorded });
      } catch (error) {
        if (stopped) return;
        const failure = asWizardError(error);
        if (
          bootstrapRetiredRef.current &&
          failure.failure !== 'authentication' &&
          failure.failure !== 'response' &&
          (failure.failure === 'network' ||
            (!failure.code && failure.status === 404) ||
            failure.status === 502 ||
            failure.status === 503 ||
            failure.status === 504 ||
            failure.code === 'upgrade_bootstrap_retired')
        ) {
          restartingRef.current = true;
          setRestarting(true);
          restartUntilRef.current = Date.now() + 120_000;
          setStep('progress');
          setBoot({ phase: 'ready', resumed: true });
          return;
        }
        setBoot({ phase: 'failed', error: failure });
      }
    })();
    return () => {
      stopped = true;
      controller.abort();
    };
  }, [boot.phase, read]);

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
      const routingEpoch = routingWriteEpochRef.current;
      try {
        const raw = await read('installation/upgrade/status', controller.signal);
        const parsed = parseUpgradeStatus(raw);
        if (stopped) return;
        if (pendingWritesRef.current > 0 || routingEpoch !== routingWriteEpochRef.current) {
          timer = window.setTimeout(tick, delayFor(0));
          return;
        }
        if (!parsed) throw new UpgradeWizardError('response');
        failures = 0;
        delayIndex = 0;
        setStatus({ phase: 'live', value: parsed, failures: 0 });
        onRecordedOperationRef.current?.(parsed.recorded);
        actionPendingRef.current = false;
        setLocalAction(null);
        workerReadUntilRef.current = parsed.execution?.running ? Date.now() + 120_000 : 0;
        const parsedCanonicalConfirmed =
          canonicalConfirmedRef.current || Boolean(parsed.canonicalConfirmed);
        const expectedShutdownWorkerUnavailable =
          (restartingRef.current ||
            activationPersistedRef.current ||
            Boolean(parsed.bootstrapRetiredRecovery)) &&
          parsed.stage === 'complete' &&
          parsed.routing?.committed === true &&
          parsed.execution?.code === 'upgrade_worker_unavailable';
        if (parsed.execution?.action === 'activate' && !parsed.execution.code) {
          activateDeliveryUnknownRef.current = false;
          activateDeliveryFailureRef.current = undefined;
        }
        if (parsedCanonicalConfirmed) {
          canonicalConfirmedRef.current = true;
          setCanonicalConfirmedState(true);
          restartingRef.current = false;
          setRestarting(false);
          restartUntilRef.current = 0;
          resumedActivationRef.current = false;
          activateDeliveryUnknownRef.current = false;
          activateDeliveryFailureRef.current = undefined;
          setExecutionError(undefined);
        } else if (
          !restartingRef.current &&
          !resumedActivationRef.current &&
          isResumableInFlightActivation(parsed, activationPersistedRef.current)
        ) {
          resumedActivationRef.current = true;
          activateDeliveryUnknownRef.current = false;
          activateDeliveryFailureRef.current = undefined;
          restartingRef.current = true;
          setRestarting(true);
          restartUntilRef.current = Date.now() + 120_000;
          setExecutionError(undefined);
          const prefix =
            parsed.routing?.controlBasePath || parsed.accessInfo?.managementBasePath || '';
          if (isCanonicalPrefix(prefix)) onActivationRef.current?.(prefix);
        } else if (
          (restartingRef.current || activationPersistedRef.current) &&
          !expectedShutdownWorkerUnavailable &&
          !parsed.accessInfo?.ready &&
          (parsed.execution?.code ||
            (parsed.execution?.available &&
              !parsed.execution.running &&
              parsed.execution.action !== 'activate'))
        ) {
          const wasRestarting = restartingRef.current;
          const hadCode = Boolean(parsed.execution?.code);
          const wasDeliveryUnknown = activateDeliveryUnknownRef.current;
          const deliveryFailure = activateDeliveryFailureRef.current;
          restartingRef.current = false;
          setRestarting(false);
          restartUntilRef.current = 0;
          resumedActivationRef.current = false;
          activateDeliveryUnknownRef.current = false;
          activateDeliveryFailureRef.current = undefined;
          onActivationRef.current?.('');
          if (wasRestarting && !hadCode && wasDeliveryUnknown) {
            setExecutionError(deliveryFailure ?? new UpgradeWizardError('network'));
          }
        } else if (parsed.accessInfo?.ready) {
          restartingRef.current = false;
          setRestarting(false);
          restartUntilRef.current = 0;
          resumedActivationRef.current = false;
          activateDeliveryUnknownRef.current = false;
          activateDeliveryFailureRef.current = undefined;
          setExecutionError(undefined);
        }
        const postCutoverPendingCode = parsed.accessInfo?.code ?? '';
        const postCutoverPollable =
          parsedCanonicalConfirmed &&
          parsed.stage === 'complete' &&
          !parsed.accessInfo?.ready &&
          (postCutoverPendingCode === 'gateway_pending' ||
            postCutoverPendingCode === 'routing_unavailable' ||
            postCutoverPendingCode === 'external_target_unavailable' ||
            postCutoverPendingCode === 'upgrade_authority_unavailable' ||
            postCutoverPendingCode === 'installation_incomplete');
        let postCutoverShouldPoll = false;
        if (postCutoverPollable) {
          const budget = postCutoverPendingCode === 'gateway_pending' ? 300_000 : 120_000;
          if (!postCutoverPollUntilRef.current) {
            postCutoverPollUntilRef.current = Date.now() + budget;
          } else if (postCutoverPendingCode !== 'gateway_pending') {
            postCutoverPollUntilRef.current = Math.min(
              postCutoverPollUntilRef.current,
              Date.now() + 120_000
            );
          }
          postCutoverShouldPoll = Date.now() < postCutoverPollUntilRef.current;
        } else if (parsedCanonicalConfirmed) {
          postCutoverPollUntilRef.current = 0;
        }
        const shouldContinuePolling =
          !operationSettled(parsed) ||
          Boolean(parsed.execution?.running) ||
          Boolean(
            parsed.stage === 'complete' &&
              !parsed.accessInfo?.ready &&
              (parsedCanonicalConfirmed
                ? postCutoverShouldPoll
                : Boolean(parsed.execution || restartingRef.current))
          );
        if (!shouldContinuePolling) {
          return;
        }
        if (!parsedCanonicalConfirmed && restartingRef.current && Date.now() >= restartUntilRef.current) {
          restartingRef.current = false;
          setRestarting(false);
          restartUntilRef.current = 0;
          activateDeliveryUnknownRef.current = false;
          activateDeliveryFailureRef.current = undefined;
          onActivationRef.current?.('');
          setExecutionError(new UpgradeWizardError('unavailable', 'upgrade_activation_timeout'));
          setStatus((current) => ({ ...current, phase: 'stalled' }));
          return;
        }
        timer = window.setTimeout(tick, delayFor(delayIndex++));
      } catch (error) {
        if (stopped) return;
        if (pendingWritesRef.current > 0 || routingEpoch !== routingWriteEpochRef.current) {
          timer = window.setTimeout(tick, delayFor(0));
          return;
        }
        failures += 1;
        const failure = asWizardError(error);
        const recovering = ((restartingRef.current && Date.now() < restartUntilRef.current) ||
          Date.now() < workerReadUntilRef.current) &&
          failure.failure !== 'authentication' && failure.failure !== 'response';
        setStatus((current) => ({
          phase: !recovering && failures >= MAX_CONSECUTIVE_FAILURES ? 'stalled' : current.phase,
          value: current.value, error: failure, failures,
        }));
        if (!recovering && failures >= MAX_CONSECUTIVE_FAILURES) {
          actionPendingRef.current = false;
          setLocalAction(null);
          if (restartingRef.current) {
            restartingRef.current = false;
            setRestarting(false);
            restartUntilRef.current = 0;
            activateDeliveryUnknownRef.current = false;
            activateDeliveryFailureRef.current = undefined;
            onActivationRef.current?.('');
            setExecutionError(
              failure.failure === 'authentication' || failure.failure === 'response'
                ? failure
                : new UpgradeWizardError('unavailable', 'upgrade_activation_timeout')
            );
          }
          return;
        }
        timer = window.setTimeout(tick, recovering ? 2000 : delayFor(delayIndex++));
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
  const canonicalConfirmed =
    canonicalConfirmedState || canonicalConfirmedProp || Boolean(status.value?.canonicalConfirmed);
  const bootstrapIsRetired = bootstrapRetired || canonicalConfirmed;
  const workerMode = record?.execution !== undefined;
  const executionAvailable = Boolean(execute && record?.execution?.available);
  const executionBusy = Boolean(localAction || record?.execution?.running);
  const isActivating = Boolean(
    !canonicalConfirmed &&
      (restarting ||
        localAction === 'activate' ||
        (record?.execution?.running && record.execution.action === 'activate'))
  );
  const isNonActivateBusy = Boolean(executionBusy && !isActivating);
  const expectedShutdownWorkerUnavailable = Boolean(
    !canonicalConfirmed &&
      (restarting ||
        localAction === 'activate' ||
        Boolean(activationPersisted) ||
        Boolean(record?.bootstrapRetiredRecovery)) &&
      record?.stage === 'complete' &&
      record.routing?.committed === true &&
      record.execution?.code === 'upgrade_worker_unavailable'
  );
  const hasExecutionFailure = Boolean(
    !canonicalConfirmed &&
      record?.execution &&
      !record.execution.running &&
      record.execution.code &&
      !expectedShutdownWorkerUnavailable
  );
  const isCleanComplete = Boolean(
    record?.stage === 'complete' &&
      record.stageKnown &&
      !hasExecutionFailure &&
      !isNonActivateBusy
  );
  const showArtifactStep =
    Boolean(artifact) &&
    Boolean(record?.recorded) &&
    (record?.nextAction === 'cpa_import' ||
      (workerMode &&
        record?.nextAction === 'external_connect' &&
        record.execution?.managedAvailable === true)) &&
    (!workerMode || record.execution?.managedAvailable === true);
  useEffect(() => {
    activityCallbackRef.current?.(status.phase !== 'stalled' && (executionBusy || restarting));
    return () => activityCallbackRef.current?.(false);
  }, [executionBusy, record, restarting, status.phase]);

  const [guideMode, setGuideMode] = useState<'managed' | 'external'>('managed');
  const guideModeTouchedRef = useRef(false);

  useEffect(() => {
    if (guideModeTouchedRef.current) return;
    if (workerMode) {
      if (
        record?.targetMode === 'external' ||
        (record?.nextAction === 'external_connect' && record.errorCode.startsWith('external_'))
      ) {
        setGuideMode('external');
        return;
      }
      setGuideMode(record?.execution?.managedAvailable ? 'managed' : 'external');
      return;
    }
    setGuideMode(record?.nextAction === 'external_connect' ? 'external' : 'managed');
  }, [workerMode, record?.execution?.managedAvailable, record?.nextAction, record?.targetMode, record?.errorCode]);
  useEffect(() => {
    if (!showArtifactStep || guideMode !== 'managed') {
      setArtifactReady(false);
    }
  }, [showArtifactStep, guideMode]);
  const artifactStepContent =
    showArtifactStep && artifact && guideMode === 'managed' && !executionBusy ? (
      <div className="upgrade-artifact" data-testid="upgrade-cpa-import-artifact">
        {!artifactReady ? (
          <div className="upgrade-artifact-card" data-testid="upgrade-artifact-prep">
            <div className="upgrade-artifact-head">
              <span className="upgrade-artifact-badge">
                {t('slimEngineStepBadge', language)}
              </span>
              <h3>{t('slimEngineTitle', language)}</h3>
              <p className="upgrade-step-help">{t('slimEngineHelp', language)}</p>
            </div>
            <FreshArtifactStep
              hideHeader
              transport={artifact}
              onReady={() => setArtifactReady(true)}
            />
          </div>
        ) : (
          <div
            className="upgrade-ready-strip"
            role="status"
            data-testid="upgrade-cpa-import-ready"
          >
            <FullWebIcon name="check" size={18} />
            <div className="upgrade-ready-strip-text">
              <h3>{t('cpaImportReadyTitle', language)}</h3>
              <p>{t('cpaImportReadyHelp', language)}</p>
            </div>
          </div>
        )}
      </div>
    ) : null;
  useEffect(() => {
    if (!record?.recorded) return;
    const routing = record.routing;
    const mode = record.targetMode ?? '';
    if (prevTargetModeRef.current !== undefined && prevTargetModeRef.current !== mode) {
      prevTargetModeRef.current = mode;
      routingInitializedRef.current = false;
      setRoutingDirty(false);
      setRoutingSaved(false);
      setRoutingError('');
    } else if (prevTargetModeRef.current === undefined) {
      prevTargetModeRef.current = mode;
    }
    if (!routing && mode !== 'managed' && mode !== 'external') return;
    if (!routingInitializedRef.current) {
      routingInitializedRef.current = true;
      if (routing && (routing.configured || routing.committed || routing.existingPrefixesPreserved)) {
        setControlPrefix(routing.controlBasePath);
        setInferencePrefix(mode === 'external' ? '' : routing.inferenceBasePath);
        setSharedPrefix(mode === 'managed' && routing.sharedEntry);
        return;
      }
      const initialControl =
        routing && isCanonicalPrefix(routing.controlBasePath)
          ? routing.controlBasePath
          : routing && isCanonicalPrefix(routing.suggestedControlBasePath)
            ? routing.suggestedControlBasePath
            : generateRandomAccessPrefix();
      const initialInference =
        mode === 'external'
          ? ''
          : [routing?.inferenceBasePath, routing?.suggestedInferenceBasePath].find(
              (value): value is string => Boolean(value && isCanonicalPrefix(value) && !isSameOrNestedPrefix(value, initialControl))
            ) ?? generateRandomAccessPrefix([initialControl]);
      setControlPrefix(initialControl);
      setInferencePrefix(initialInference);
      setSharedPrefix(mode === 'managed' && Boolean(routing?.sharedEntry));
      return;
    }
    if (routing && (routing.configured || routing.committed || routing.existingPrefixesPreserved) && !routingDirty) {
      setControlPrefix(routing.controlBasePath);
      setInferencePrefix(mode === 'external' ? '' : routing.inferenceBasePath);
      setSharedPrefix(mode === 'managed' && routing.sharedEntry);
    }
  }, [record, routingDirty]);

  const hasEditableTargetMode =
    record?.targetMode === 'managed' || record?.targetMode === 'external';
  const preservedRouting = record?.routing?.existingPrefixesPreserved === true;
  const committedRouting = record?.routing?.committed === true;
  const showPreservedRouting = Boolean(record?.recorded && preservedRouting);
  const showRoutingEditor = Boolean(
    record?.recorded &&
      !preservedRouting &&
      !committedRouting &&
      hasEditableTargetMode &&
      (record?.nextAction === 'verify' || saveRouting)
  );
  const isManagedTarget = record?.targetMode === 'managed';
  const effectiveShared = isManagedTarget && sharedPrefix;
  const effectiveInference = isManagedTarget
    ? effectiveShared
      ? controlPrefix
      : inferencePrefix
    : '';
  const currentRoutingPayload: UpgradeRoutingInput = {
    controlBasePath: controlPrefix.trim(),
    inferenceBasePath: effectiveInference.trim(),
    sharedEntry: effectiveShared,
  };
  const verifiedAccessInfo =
    record?.accessInfo ??
    (verification.phase === 'done' ? verification.value.accessInfo : undefined);
  const effectiveExternalBaseUrl = verifiedAccessInfo
    ? verifiedAccessInfo.externalBaseUrl ?? ''
    : record?.routing?.externalBaseUrl ||
      (verification.phase === 'done' ? verification.value.routing?.externalBaseUrl || '' : '');
  const externalProtocols =
    record?.targetMode === 'external' &&
    verifiedAccessInfo?.mode === 'connect_external' &&
    effectiveExternalBaseUrl
      ? verifiedAccessInfo.protocols
      : [];
  const previewOrigin = resolveBrowserOrigin();
  const previewUrls = buildPrefixPreviewUrls(
    previewOrigin,
    currentRoutingPayload.controlBasePath,
    currentRoutingPayload.inferenceBasePath,
    effectiveExternalBaseUrl
  );
  const resultAccessInfo = verifiedAccessInfo;
  const reread = () => {
    postCutoverPollUntilRef.current = 0;
    setStatus((current) => ({ phase: 'idle', value: current.value, failures: 0 }));
    setAttempt((current) => current + 1);
  };

  const saveRoutingSnapshot = async (signal: AbortSignal) => {
    const raw = await saveRouting!(signal, currentRoutingPayload);
    if (!raw || typeof raw !== 'object') throw new UpgradeWizardError('response');
    const document = raw as Record<string, unknown>;
    const saved = parseUpgradeRouting(document.routing);
    const targetMode = text(document.targetMode) || record?.targetMode || '';
    if (
      document.contractVersion !== UPGRADE_WIZARD_CONTRACT ||
      !saved?.configured || targetMode !== record?.targetMode ||
      !validatePrefixPair({
        controlBasePath: saved.controlBasePath,
        inferenceBasePath: saved.inferenceBasePath,
        sharedEntry: saved.sharedEntry,
        requireInference: targetMode === 'managed',
      }).valid
    ) throw new UpgradeWizardError('response');
    const accessInfo = parseUpgradeAccessInfo(document.accessInfo, targetMode);
    setStatus((current) => current.value ? {
      phase: 'live', failures: 0,
      value: { ...current.value, targetMode, routing: saved, accessInfo },
    } : current);
    setRoutingDirty(false);
    setRoutingSaved(true);
  };

  // The action runs once per press and never retries itself: a verification
  // writes the store, so a silent retry would record a verdict the operator did
  // not ask for twice. Its answer is presented as the action's own result, and
  // the recorded row is re-read afterwards so the page shows what the store
  // holds instead of what this reply happened to say.
  const saveUpgradeRouting = async () => {
    if (!saveRouting || executionBusy || actionPendingRef.current || pendingWritesRef.current > 0 || routingSaving || verification.phase === 'running' || showPreservedRouting || committedRouting) return;
    const check = validatePrefixPair({
      controlBasePath: currentRoutingPayload.controlBasePath,
      inferenceBasePath: currentRoutingPayload.inferenceBasePath,
      sharedEntry: currentRoutingPayload.sharedEntry,
      requireInference: isManagedTarget,
    });
    if (!check.valid) {
      const code = check.controlError || check.inferenceError || check.pairError || 'invalid_access_prefix';
      setRoutingSaved(false);
      setRoutingError(blockedCodeText(code, language));
      return;
    }
    setRoutingSaving(true);
    setRoutingError('');
    setRoutingSaved(false);
    pendingWritesRef.current += 1;
    routingWriteEpochRef.current += 1;
    try {
      await saveRoutingSnapshot(new AbortController().signal);
    } catch (error) {
      setRoutingError(errorText(asWizardError(error), language));
    } finally {
      pendingWritesRef.current = Math.max(0, pendingWritesRef.current - 1);
      routingWriteEpochRef.current += 1;
      reread();
      setRoutingSaving(false);
    }
  };

  const runVerification = async () => {
    if ((!verify && !execute) || executionBusy || actionPendingRef.current || (workerMode && !executionAvailable) || pendingWritesRef.current > 0 || routingSaving || verification.phase === 'running') return;
    if (showRoutingEditor) {
      const check = validatePrefixPair({
        controlBasePath: currentRoutingPayload.controlBasePath,
        inferenceBasePath: currentRoutingPayload.inferenceBasePath,
        sharedEntry: currentRoutingPayload.sharedEntry,
        requireInference: isManagedTarget,
      });
      if (!check.valid) {
        const code = check.controlError || check.inferenceError || check.pairError || 'invalid_access_prefix';
        setRoutingSaved(false);
        setRoutingError(blockedCodeText(code, language));
        return;
      }
    }
    setRoutingError('');
    setVerification({ phase: 'running' });
    pendingWritesRef.current += 1;
    routingWriteEpochRef.current += 1;
    try {
      const signal = new AbortController().signal;
      if (showRoutingEditor && (!record?.routing?.configured || routingDirty)) {
        if (saveRouting) await saveRoutingSnapshot(signal);
        else if (workerMode) throw new UpgradeWizardError('unavailable');
      }
      if (workerMode) {
        await dispatchExecution({ action: 'verify' });
        setVerification({ phase: 'idle' });
      } else {
        const raw = showRoutingEditor
          ? await verify!(signal, currentRoutingPayload)
          : await verify!(signal);
        const parsed = parseUpgradeVerify(raw);
        if (!parsed) throw new UpgradeWizardError('response');
        setVerification({ phase: 'done', value: parsed });
      }
    } catch (error) {
      setVerification({ phase: 'failed', error: asWizardError(error) });
    } finally {
      pendingWritesRef.current = Math.max(0, pendingWritesRef.current - 1);
      routingWriteEpochRef.current += 1;
      reread();
    }
  };

  const dispatchExecution = async (input: UpgradeExecutionInput): Promise<boolean> => {
    if (
      !execute ||
      !executionAvailable ||
      actionPendingRef.current ||
      record?.execution?.running ||
      bootstrapIsRetired
    ) {
      return false;
    }
    actionPendingRef.current = true;
    setLocalAction(input.action);
    setExecutionError(undefined);
    pendingWritesRef.current += 1;
    routingWriteEpochRef.current += 1;
    workerReadUntilRef.current = Date.now() + 120_000;
    if (input.action === 'activate') {
      resumedActivationRef.current = true;
      activateDeliveryUnknownRef.current = false;
      activateDeliveryFailureRef.current = undefined;
    }
    let mayRestart = false;
    try {
      const raw = await execute(new AbortController().signal, input);
      if (
        !raw ||
        typeof raw !== 'object' ||
        (raw as Record<string, unknown>).contractVersion !== 'cpamp.upgrade-execution.v1' ||
        !parseUpgradeExecution(raw)
      ) {
        throw new UpgradeWizardError('response');
      }
      // This is acceptance only. The next status read owns both position and busy state.
      mayRestart = true;
      if (input.action === 'activate') {
        activateDeliveryUnknownRef.current = false;
        activateDeliveryFailureRef.current = undefined;
      }
      return true;
    } catch (error) {
      const failure = asWizardError(error);
      mayRestart =
        failure.failure === 'network' ||
        (input.action === 'activate' &&
          ((failure.failure === 'unavailable' &&
            (failure.code === 'upgrade_worker_unavailable' ||
              failure.code === 'upgrade_bootstrap_retired' ||
              (!failure.code &&
                (failure.status === 0 ||
                  failure.status === 404 ||
                  failure.status === 502 ||
                  failure.status === 503 ||
                  failure.status === 504)))) ||
            (failure.status === 404 && !failure.code)));
      if (input.action === 'activate' && mayRestart) {
        activateDeliveryUnknownRef.current = true;
        activateDeliveryFailureRef.current = failure;
      } else {
        if (input.action === 'activate') {
          resumedActivationRef.current = false;
          activateDeliveryUnknownRef.current = false;
          activateDeliveryFailureRef.current = undefined;
        }
        setExecutionError(failure);
      }
      actionPendingRef.current = false;
      setLocalAction(null);
      return false;
    } finally {
      if (input.action === 'activate' && mayRestart) {
        restartingRef.current = true;
        setRestarting(true);
        restartUntilRef.current = Date.now() + 120_000;
        const prefix = record?.routing?.controlBasePath || record?.accessInfo?.managementBasePath || '';
        if (isCanonicalPrefix(prefix)) onActivationRef.current?.(prefix);
      }
      pendingWritesRef.current = Math.max(0, pendingWritesRef.current - 1);
      routingWriteEpochRef.current += 1;
      reread();
    }
  };

  return (
    <section className="upgrade-flow" data-testid="upgrade-flow" aria-label={t('flowLabel', language)}>
      {boot.phase === 'ready' && executionBusy && (
        <p className="upgrade-step-status" role="status" data-testid="upgrade-execution-running">
          <FullWebIcon name="refresh" size={18} />
          {e('running', language)}
        </p>
      )}
      {boot.phase === 'ready' && executionError && (
        <div className="upgrade-notice" role="alert" data-testid="upgrade-execution-error">
          <FullWebIcon name="alert" size={20} />
          <div>
            <h3>{e('actionFailed', language)}</h3>
            <p>
              {executionError.code === 'upgrade_activation_timeout'
                ? e('activationTimeout', language)
                : executionError.code
                  ? blockedCodeText(executionError.code, language)
                  : executionError.status === 409
                    ? e('conflict', language)
                    : errorText(executionError, language)}
            </p>
            {executionError.code && <code>{executionError.code}</code>}
          </div>
        </div>
      )}
      {boot.phase === 'ready' &&
        record?.execution?.available &&
        !record.execution.running &&
        record.execution.code && (
          <div className="upgrade-notice" role="alert" data-testid="upgrade-execution-status-error">
            <FullWebIcon name="alert" size={20} />
            <div>
              <h3>{e('executionStatusFailedTitle', language)}</h3>
              <p>{blockedCodeText(record.execution.code, language)}</p>
              <code>{record.execution.code}</code>
            </div>
          </div>
        )}
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
              const isDone = index < current;
              const isCompleteResult = key === 'result' && shownStep === 'result' && isCleanComplete;
              return (
                <li
                  key={key}
                  data-testid={`upgrade-step-${key}`}
                  data-state={isDone ? 'done' : key === shownStep ? 'current' : 'pending'}
                  data-complete={isCompleteResult ? 'true' : undefined}
                  aria-current={key === shownStep ? 'step' : undefined}
                >
                  <span className="upgrade-step-index" aria-hidden="true">
                    {isDone || isCompleteResult ? (
                      <FullWebIcon name="check" size={15} />
                    ) : (
                      index + 1
                    )}
                  </span>
                  <span>{t(STEP_TEXT[key], language)}</span>
                </li>
              );
            })}
          </ol>
          {boot.resumed && (
            <p className="upgrade-sr-only" data-testid="upgrade-resumed">
              {t('resumeNotice', language)}
            </p>
          )}
        </>
      )}

      {boot.phase === 'ready' && shownStep === 'preflight' && (
        <div className="upgrade-step-body" data-testid="upgrade-preflight">
          <div className="upgrade-step-head">
            <h2>{t('preflightTitle', language)}</h2>
            <p className="upgrade-step-help">{t('preflightHelp', language)}</p>
          </div>
          {preflight.phase === 'idle' && (
            <p className="upgrade-step-status" role="status" data-testid="upgrade-preflight-reading">
              <FullWebIcon name="refresh" size={18} />
              {t('preflightReading', language)}
            </p>
          )}
          {preflight.phase === 'failed' && (
            <div className="upgrade-notice" role="alert" data-testid="upgrade-preflight-error">
              <FullWebIcon name="alert" size={20} />
              <div>
                <h3>{t('errorUnavailable', language)}</h3>
                <p>{errorText(preflight.error, language)}</p>
                {preflight.error.code && <code>{preflight.error.code}</code>}
              </div>
            </div>
          )}
          {preflightReport && (
            <>
              <dl className="upgrade-facts upgrade-facts-inline">
                <div>
                  <dt>{t('preflightSourceKind', language)}</dt>
                  <dd data-testid="upgrade-preflight-kind">
                    {formatSourceKind(preflightReport.sourceKind, language)}
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
                  <dd className="upgrade-fact-mono" data-testid="upgrade-preflight-fingerprint">
                    {preflightReport.fingerprint || t('preflightUnknown', language)}
                  </dd>
                </div>
                {preflightReport.qualifiedCpaArtifactId && (
                  <div>
                    <dt>{t('preflightCpaBaseline', language)}</dt>
                    <dd
                      className="upgrade-fact-mono"
                      data-testid="upgrade-preflight-cpa-baseline"
                      title={preflightReport.qualifiedCpaArtifactId}
                    >
                      {formatPreflightCpaBaseline(preflightReport.qualifiedCpaArtifactId)}
                    </dd>
                  </div>
                )}
              </dl>
              {preflightReport.pendingDeclaration && (
                <div
                  className={preflightReport.ready ? 'upgrade-sr-only' : 'upgrade-pending'}
                  role="note"
                  data-testid="upgrade-preflight-pending-declaration"
                >
                  <h3>{t('preflightPendingTitle', language)}</h3>
                  <p>{t('preflightPendingHelp', language)}</p>
                  <p>{t('preflightPendingTail', language)}</p>
                </div>
              )}
              <div className="upgrade-checks-card">
                <ul className="upgrade-checks">
                  {preflightReport.checks.map((check) => {
                    const summary = summarizePreflightCheck(check, language);
                    return (
                      <li
                        key={check.name}
                        className={`upgrade-check-row is-${check.status}`}
                        data-testid={`upgrade-check-${check.name}`}
                        data-status={check.status}
                      >
                        <div className="upgrade-check-left">
                          <span className={`upgrade-check-dot is-${check.status}`} aria-hidden="true">
                            <FullWebIcon
                              name={
                                check.status === 'pass'
                                  ? 'check'
                                  : check.status === 'pending'
                                    ? 'clock'
                                    : 'alert'
                              }
                              size={13}
                            />
                          </span>
                          <span className="upgrade-check-name">{checkNameText(check.name, language)}</span>
                        </div>
                        <div className="upgrade-check-right">
                          {summary && <span className="upgrade-check-summary">{summary}</span>}
                          <span className={`upgrade-check-outcome is-${check.status}`}>
                            {checkOutcomeText(check.status, language)}
                          </span>
                        </div>
                        {!summary && (check.code || check.detail) && (
                          <div className="upgrade-check-issue">
                            {check.code && <code>{check.code}</code>}
                            {check.detail && <span className="upgrade-check-detail">{check.detail}</span>}
                          </div>
                        )}
                      </li>
                    );
                  })}
                </ul>
                {preflightReport.checks.some((c) => c.code || c.detail) && (
                  <details className="upgrade-raw-drawer">
                    <summary>{t('techDetails', language)}</summary>
                    <div className="upgrade-raw-list">
                      {preflightReport.checks.map(
                        (check) =>
                          (check.code || check.detail) && (
                            <div key={check.name} className="upgrade-raw-item">
                              <strong>{checkNameText(check.name, language)}</strong>
                              {check.code && <code>{check.code}</code>}
                              {check.detail && <span>{check.detail}</span>}
                            </div>
                          )
                      )}
                    </div>
                  </details>
                )}
              </div>
              {preflightReport.blocked.length > 0 ? (
                <div className="upgrade-notice" role="alert" data-testid="upgrade-preflight-blocked">
                  <FullWebIcon name="alert" size={20} />
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
                  className="upgrade-ready-strip"
                  role="status"
                  data-testid="upgrade-preflight-ready"
                >
                  <FullWebIcon name="check" size={18} />
                  <div className="upgrade-ready-strip-text">
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
              <FullWebIcon name="refresh" size={16} /> {t('preflightRetry', language)}
            </button>
            <button
              className="btn btn-primary"
              data-testid="upgrade-continue"
              disabled={!preflightReport?.ready}
              onClick={() => setStep('confirm')}
            >
              {t('continue', language)} <FullWebIcon name="arrowRight" size={16} />
            </button>
          </div>
        </div>
      )}

      {boot.phase === 'ready' && shownStep === 'confirm' && (
        <div className="upgrade-step-body" data-testid="upgrade-confirm">
          <div className="upgrade-step-head">
            <h2>{t('confirmTitle', language)}</h2>
            <p className="upgrade-step-help">{t('confirmHelp', language)}</p>
          </div>
          <dl className="upgrade-facts upgrade-facts-inline">
            <div>
              <dt>{t('confirmSourceKind', language)}</dt>
              <dd data-testid="upgrade-confirm-kind">
                {formatSourceKind(preflightReport?.sourceKind, language)}
              </dd>
            </div>
            <div>
              <dt>{t('confirmSourceVersion', language)}</dt>
              <dd data-testid="upgrade-confirm-version">
                {formatPreflightSourceVersion(preflightReport, language)}
              </dd>
            </div>
          </dl>
          <div className="upgrade-promise-card">
            <div className="upgrade-promise-row">
              <span className="upgrade-promise-icon" aria-hidden="true">
                <FullWebIcon name="shield" size={16} />
              </span>
              <div>
                <strong>{t('confirmReadOnlyTitle', language)}</strong>
                <p>{t('confirmReadOnlyHelp', language)}</p>
              </div>
            </div>
            <div className="upgrade-promise-row">
              <span className="upgrade-promise-icon" aria-hidden="true">
                <FullWebIcon name="server" size={16} />
              </span>
              <div>
                <strong>{t('confirmBackupTitle', language)}</strong>
                <p>{t('confirmBackupHelp', language)}</p>
              </div>
            </div>
            <div className="upgrade-promise-row">
              <span className="upgrade-promise-icon" aria-hidden="true">
                <FullWebIcon name="check" size={16} />
              </span>
              <div>
                <strong>{t('confirmAutoTitle', language)}</strong>
                <p>{t('confirmAutoHelp', language)}</p>
              </div>
            </div>
            <details className="upgrade-raw-drawer">
              <summary>{t('confirmEdgeToggle', language)}</summary>
              <div className="upgrade-edge-body">
                <ul className="upgrade-list" data-testid="upgrade-confirm-manual">
                  {CONFIRM_MANUAL.map((key) => (
                    <li key={key}>{t(key, language)}</li>
                  ))}
                </ul>
              </div>
            </details>
          </div>
          <label className="upgrade-ack" htmlFor="upgrade-confirm-ack-input">
            <span className="journey-checkbox-control">
              <input
                id="upgrade-confirm-ack-input"
                type="checkbox"
                data-testid="upgrade-confirm-ack"
                checked={acknowledged}
                onChange={(event) => setAcknowledged(event.target.checked)}
              />
              <span className="journey-checkbox-box" aria-hidden="true">
                <FullWebIcon name="check" size={13} />
              </span>
            </span>
            <span>{t('confirmAck', language)}</span>
          </label>
          <div className="upgrade-actions">
            <button
              className="btn btn-secondary"
              data-testid="upgrade-confirm-back"
              onClick={() => setStep('preflight')}
            >
              <FullWebIcon name="arrowLeft" size={16} /> {t('back', language)}
            </button>
            <button
              className="btn btn-primary"
              data-testid="upgrade-confirm-continue"
              disabled={!acknowledged}
              onClick={() => setStep('progress')}
            >
              {t('confirmContinue', language)} <FullWebIcon name="arrowRight" size={16} />
            </button>
          </div>
        </div>
      )}

      {boot.phase === 'ready' && shownStep === 'progress' && (() => {
        const isReadyToVerify = Boolean(record?.recorded && record.nextAction === 'verify');
        const needsPreparation = Boolean(
          record &&
            (!record.recorded ||
              record.nextAction === 'reconcile' ||
              record.nextAction === 'cpa_import' ||
              record.nextAction === 'external_connect' ||
              (executionAvailable &&
                record.stage === 'copy_in' &&
                record.domainsDone.includes('source_copy')))
        );
        const hasHardProgressAnomalies = Boolean(
          record && (record.mismatchDays.length > 0 || Boolean(record.errorCode))
        );
        return (
          <div className="upgrade-step-body" data-testid="upgrade-progress">
            <div className="upgrade-step-head">
              <h2>
                {t(isReadyToVerify ? 'progressVerifyTitle' : 'progressTitle', language)}
              </h2>
              <p className="upgrade-step-help">
                {t(isReadyToVerify ? 'progressVerifySubtitle' : 'progressHelp', language)}
              </p>
            </div>
            {!record && (
              <p className="upgrade-step-status" role="status" data-testid="upgrade-progress-reading">
                <FullWebIcon name="refresh" size={18} />
                {t('progressReading', language)}
              </p>
            )}
            {record && !record.recorded && (
              <p
                className="upgrade-step-status"
                role="status"
                data-testid="upgrade-progress-unrecorded"
              >
                <FullWebIcon name="clock" size={18} />
                {t('progressUnrecorded', language)}
              </p>
            )}
            {record?.recorded && (
              !hasHardProgressAnomalies && (isReadyToVerify || needsPreparation) ? (
                <>
                  <details className="upgrade-summary-disclosure">
                    <summary>
                      <span className="upgrade-summary-main">
                        <FullWebIcon name="check" size={15} />
                        <span>
                          {t(
                            record.manualItems.length > 0
                              ? 'migrationReadyWithManual'
                              : 'migrationReadySummary',
                            language
                          ).replace('{count}', String(record.domainsDone.length))}
                        </span>
                      </span>
                      <span className="upgrade-summary-toggle">
                        {t('migrationDetailsToggle', language)}
                      </span>
                    </summary>
                    <div className="upgrade-summary-body">
                      <StageLine status={record} language={language} testid="upgrade-progress-stage" />
                      {record.domainsDone.length === 0 ? (
                        <p className="upgrade-step-help" data-testid="upgrade-progress-domains">
                          {t('progressDomainsNone', language)}
                        </p>
                      ) : (
                        <ul className="upgrade-domains" data-testid="upgrade-progress-domains">
                          {record.domainsDone.map((domain) => (
                            <li key={domain}>
                              <FullWebIcon name="check" size={13} />
                              <span>{domainText(domain, language)}</span>
                            </li>
                          ))}
                        </ul>
                      )}
                      <Reconciliation status={record} language={language} />
                      {record.manualItems.length === 0 && (
                        <ManualItems status={record} language={language} />
                      )}
                    </div>
                  </details>
                  {record.manualItems.length > 0 && (
                    <ManualItems status={record} language={language} collapsible />
                  )}
                </>
              ) : (
                <>
                  <StageLine status={record} language={language} testid="upgrade-progress-stage" />
                  {record.domainsDone.length === 0 ? (
                    <p className="upgrade-step-help" data-testid="upgrade-progress-domains">
                      {t('progressDomainsNone', language)}
                    </p>
                  ) : (
                    <ul className="upgrade-domains" data-testid="upgrade-progress-domains">
                      {record.domainsDone.map((domain) => (
                        <li key={domain}>
                          <FullWebIcon name="check" size={13} />
                          <span>{domainText(domain, language)}</span>
                        </li>
                      ))}
                    </ul>
                  )}
                  <Reconciliation status={record} language={language} />
                  <ManualItems status={record} language={language} />
                  {record.errorCode && (
                    <div
                      className="upgrade-notice"
                      role="status"
                      data-testid="upgrade-progress-reason"
                    >
                      <FullWebIcon name="info" size={20} />
                      <div>
                        <h3>{t('verifyReasonLabel', language)}</h3>
                        <p>{record.errorDetail}</p>
                        <code>{record.errorCode}</code>
                      </div>
                    </div>
                  )}
                </>
              )
            )}
            {artifactStepContent}
            {needsPreparation && record && (
              executionAvailable ? (
                <UpgradeExecutionActions record={record} language={language} busy={executionBusy}
                  mode={guideMode} onModeChange={(mode) => { guideModeTouchedRef.current = true; setGuideMode(mode); }}
                  waitingForEngine={showArtifactStep && !artifactReady} execute={dispatchExecution} />
              ) : <ExecutionRecovery language={language} code={record.execution?.code} />
            )}
            {showPreservedRouting && record?.routing && (
              <div className="upgrade-notice upgrade-notice-info" data-testid="upgrade-preserved-routing">
                <FullWebIcon name="shield" size={18} />
                <div>
                  <h3>{t('routingPreservedTitle', language)}</h3>
                  <p>{t('routingPreservedHelp', language)}</p>
                  <dl className="upgrade-facts upgrade-facts-inline">
                    <div>
                      <dt>{t('routingControlLabel', language)}</dt>
                      <dd data-testid="upgrade-preserved-control">
                        <code>{record.routing.controlBasePath}</code>
                      </dd>
                    </div>
                    <div>
                      <dt>
                        {record.targetMode === 'external'
                          ? t('routingExternalInferenceLabel', language)
                          : t('routingInferenceLabel', language)}
                      </dt>
                      <dd data-testid="upgrade-preserved-inference">
                        <code>
                          {record.targetMode === 'external'
                            ? effectiveExternalBaseUrl || t('preflightUnknown', language)
                            : record.routing.inferenceBasePath}
                        </code>
                      </dd>
                    </div>
                    {record.targetMode === 'managed' && (
                      <div>
                        <dt>{t('routingSharedToggle', language)}</dt>
                        <dd data-testid="upgrade-preserved-mode">
                          {record.routing.sharedEntry
                            ? t('routingSharedLabel', language)
                            : t('routingIndependentLabel', language)}
                        </dd>
                      </div>
                    )}
                  </dl>
                  {record.targetMode === 'external' && externalProtocols.length > 0 && (
                    <ul className="upgrade-sr-only" data-testid="upgrade-external-protocols">
                      {externalProtocols.map((protocol) => (
                        <li key={protocol.id || protocol.label || protocol.basePath}>
                          <span>{protocol.label || protocol.id}</span>{' '}
                          <code>{protocol.basePath}</code>{' '}
                          {protocol.examplePath && <code>{protocol.examplePath}</code>}
                        </li>
                      ))}
                    </ul>
                  )}
                </div>
              </div>
            )}
            {showRoutingEditor && (
              <div className="upgrade-routing-section" data-testid="upgrade-routing-config">
                <div className="upgrade-url-card" data-testid="upgrade-prefix-url-preview">
                  <div className="upgrade-url-card-head">
                    <span>{t('routingPreviewTitle', language)}</span>
                    <span className="badge badge-neutral">
                      {isManagedTarget
                        ? t('modeBadgeManaged', language)
                        : t('modeBadgeExternal', language)}
                    </span>
                  </div>
                  <div className="upgrade-url-rows">
                    <div className="upgrade-url-row is-primary">
                      <span className="upgrade-url-key">{t('routingPreviewConsole', language)}</span>
                      <span className="upgrade-url-val" data-testid="upgrade-preview-console">
                        <code>{previewUrls.consoleUrl || '—'}</code>
                        {previewUrls.consoleUrl && previewUrls.consoleUrl !== '—' && (
                          <button
                            type="button"
                            className="btn btn-ghost btn-xs btn-icon-only"
                            aria-label={t('copyAction', language)}
                            onClick={() => copyPreviewUrl('console', previewUrls.consoleUrl)}
                          >
                            <FullWebIcon
                              name={copiedPreviewId === 'console' ? 'check' : 'copy'}
                              size={14}
                            />
                          </button>
                        )}
                      </span>
                    </div>
                    {isManagedTarget ? (
                      <div className="upgrade-url-row">
                        <span className="upgrade-url-key">{t('routingPreviewInference', language)}</span>
                        <span className="upgrade-url-val" data-testid="upgrade-preview-inference">
                          <code>{previewUrls.inferenceUrl || '—'}</code>
                          {previewUrls.inferenceUrl && previewUrls.inferenceUrl !== '—' && (
                            <button
                              type="button"
                              className="btn btn-ghost btn-xs btn-icon-only"
                              aria-label={t('copyAction', language)}
                              onClick={() => copyPreviewUrl('inference', previewUrls.inferenceUrl)}
                            >
                              <FullWebIcon
                                name={copiedPreviewId === 'inference' ? 'check' : 'copy'}
                                size={14}
                              />
                            </button>
                          )}
                        </span>
                      </div>
                    ) : (
                      <div className="upgrade-url-row">
                        <span className="upgrade-url-key">{t('routingPreviewExternal', language)}</span>
                        <span className="upgrade-url-val" data-testid="upgrade-preview-external">
                          <code>{previewUrls.externalUrl || '—'}</code>
                          {previewUrls.externalUrl && previewUrls.externalUrl !== '—' && (
                            <button
                              type="button"
                              className="btn btn-ghost btn-xs btn-icon-only"
                              aria-label={t('copyAction', language)}
                              onClick={() => copyPreviewUrl('external', previewUrls.externalUrl)}
                            >
                              <FullWebIcon
                                name={copiedPreviewId === 'external' ? 'check' : 'copy'}
                                size={14}
                              />
                            </button>
                          )}
                        </span>
                      </div>
                    )}
                  </div>
                </div>

                <details
                  className="upgrade-routing-drawer"
                  open={routingDrawerOpen || Boolean(routingError)}
                >
                  <summary
                    onClick={(event) => {
                      event.preventDefault();
                      setRoutingDrawerOpen((prev) => !prev);
                    }}
                  >
                    <span className="upgrade-drawer-title">
                      <FullWebIcon
                        name="chevronRight"
                        size={15}
                        className="upgrade-drawer-chevron"
                      />
                      {t('routingCustomizeToggle', language)}
                    </span>
                    <code className="upgrade-drawer-meta">
                      {isManagedTarget
                        ? `${controlPrefix || '—'} · ${effectiveInference || '—'}`
                        : controlPrefix || '—'}
                    </code>
                  </summary>
                  <div className="upgrade-routing-drawer-body">
                    <div className="upgrade-prefix-group">
                      <label htmlFor="upgrade-control-prefix">{t('routingControlLabel', language)}</label>
                      <div className="upgrade-prefix-row">
                        <input
                          id="upgrade-control-prefix"
                          className="form-input form-input-mono"
                          data-testid="upgrade-control-prefix-input"
                          value={controlPrefix}
                          onChange={(event) => {
                            setControlPrefix(event.target.value);
                            setRoutingDirty(true);
                            setRoutingSaved(false);
                            setRoutingError('');
                          }}
                          disabled={executionBusy || routingSaving || verification.phase === 'running'}
                        />
                        <button
                          type="button"
                          className="btn btn-secondary btn-sm"
                          data-testid="upgrade-randomize-control-prefix"
                          disabled={executionBusy || routingSaving || verification.phase === 'running'}
                          onClick={() => {
                            const next = generateRandomAccessPrefix([controlPrefix, inferencePrefix]);
                            setControlPrefix(next);
                            if (effectiveShared) setInferencePrefix(next);
                            setRoutingDirty(true);
                            setRoutingSaved(false);
                            setRoutingError('');
                          }}
                        >
                          <FullWebIcon name="refresh" size={14} />
                          {t('routingRandomize', language)}
                        </button>
                      </div>
                    </div>
                    {isManagedTarget ? (
                      <>
                        <div className="upgrade-prefix-group">
                          <label htmlFor="upgrade-inference-prefix">
                            {t('routingInferenceLabel', language)}
                          </label>
                          <div className="upgrade-prefix-row">
                            <input
                              id="upgrade-inference-prefix"
                              className="form-input form-input-mono"
                              data-testid="upgrade-inference-prefix-input"
                              value={effectiveShared ? controlPrefix : inferencePrefix}
                              onChange={(event) => {
                                setInferencePrefix(event.target.value);
                                setRoutingDirty(true);
                                setRoutingSaved(false);
                                setRoutingError('');
                              }}
                              disabled={
                                executionBusy || effectiveShared || routingSaving || verification.phase === 'running'
                              }
                            />
                            <button
                              type="button"
                              className="btn btn-secondary btn-sm"
                              data-testid="upgrade-randomize-inference-prefix"
                              disabled={
                                executionBusy || effectiveShared || routingSaving || verification.phase === 'running'
                              }
                              onClick={() => {
                                setInferencePrefix(generateRandomAccessPrefix([controlPrefix, inferencePrefix]));
                                setRoutingDirty(true);
                                setRoutingSaved(false);
                                setRoutingError('');
                              }}
                            >
                              <FullWebIcon name="refresh" size={14} />
                              {t('routingRandomize', language)}
                            </button>
                          </div>
                        </div>
                        <label className="upgrade-ack" htmlFor="upgrade-shared-prefix">
                          <span className="journey-checkbox-control">
                            <input
                              id="upgrade-shared-prefix"
                              type="checkbox"
                              data-testid="upgrade-shared-prefix-checkbox"
                              checked={sharedPrefix}
                              disabled={executionBusy || routingSaving || verification.phase === 'running'}
                              onChange={(event) => {
                                const nextShared = event.target.checked;
                                setSharedPrefix(nextShared);
                                if (nextShared) setInferencePrefix(controlPrefix);
                                else if (
                                  (!isCanonicalPrefix(inferencePrefix) ||
                                    isSameOrNestedPrefix(inferencePrefix, controlPrefix))
                                ) {
                                  setInferencePrefix(generateRandomAccessPrefix([controlPrefix, inferencePrefix]));
                                }
                                setRoutingDirty(true);
                                setRoutingSaved(false);
                                setRoutingError('');
                              }}
                            />
                            <span className="journey-checkbox-box" aria-hidden="true">
                              <FullWebIcon name="check" size={13} />
                            </span>
                          </span>
                          <span>{t('routingSharedToggle', language)}</span>
                        </label>
                      </>
                    ) : (
                      externalProtocols.length > 0 && (
                        <div className="upgrade-sr-only" data-testid="upgrade-external-inference-info">
                          <ul data-testid="upgrade-external-protocols">
                            {externalProtocols.map((protocol) => (
                              <li key={protocol.id || protocol.label || protocol.basePath}>
                                <span>{protocol.label || protocol.id}</span>{' '}
                                <code>{protocol.basePath}</code>{' '}
                                {protocol.examplePath && <code>{protocol.examplePath}</code>}
                              </li>
                            ))}
                          </ul>
                        </div>
                      )
                    )}
                    <div className="upgrade-drawer-foot">
                      {routingSaved ? (
                        <span className="upgrade-saved-hint" data-testid="upgrade-routing-saved">
                          <FullWebIcon name="check" size={14} /> {t('routingSavedNotice', language)}
                        </span>
                      ) : !isManagedTarget ? (
                        <span className="upgrade-drawer-note">
                          {t('routingExternalInferenceHelp', language)}
                        </span>
                      ) : (
                        <span />
                      )}
                      {saveRouting && (
                        <button
                          type="button"
                          className="btn btn-secondary btn-sm"
                          data-testid="upgrade-save-routing"
                          disabled={executionBusy || routingSaving || verification.phase === 'running'}
                          onClick={saveUpgradeRouting}
                        >
                          {routingSaving ? t('routingSaving', language) : t('routingSave', language)}
                        </button>
                      )}
                    </div>
                  </div>
                </details>

                {routingError && (
                  <div className="upgrade-notice" role="alert" data-testid="upgrade-routing-error">
                    <FullWebIcon name="alert" size={20} />
                    <div>
                      <p>{routingError}</p>
                    </div>
                  </div>
                )}
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
                <FullWebIcon name="check" size={20} />
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
                <FullWebIcon name="alert" size={20} />
                <div>
                  <h3>{t('verifyFailedTitle', language)}</h3>
                  <p>{errorText(verification.error, language)}</p>
                  {verification.error.code && <code>{verification.error.code}</code>}
                </div>
              </div>
            )}
            {isReadyToVerify && (workerMode ? executionAvailable : verify) && (
              <div className="upgrade-verify-footer">
                <p className="upgrade-verify-hint" data-testid="upgrade-verify-offer">
                  <FullWebIcon name="info" size={15} />
                  <span>{t('verifyInlineHint', language)}</span>
                </p>
                <button
                  className="btn btn-primary"
                  data-testid="upgrade-verify-action"
                  disabled={executionBusy || routingSaving || verification.phase === 'running'}
                  onClick={runVerification}
                >
                  {verification.phase === 'done'
                    ? t('verifyRetry', language)
                    : t('verifyAction', language)}
                  <FullWebIcon name="arrowRight" size={16} />
                </button>
              </div>
            )}
            {isReadyToVerify && workerMode && !executionAvailable && (
              <ExecutionRecovery language={language} code={record?.execution?.code} />
            )}
            {record && status.phase !== 'stalled' && (
              isReadyToVerify ? (
                <p className="upgrade-sr-only" data-testid="upgrade-progress-waiting">
                  {t('progressWaiting', language)}
                </p>
              ) : (
                <div className="upgrade-poll-footer">
                  <p className="upgrade-poll-caption" data-testid="upgrade-progress-waiting">
                    <FullWebIcon name="clock" size={14} />
                    <span>{t('progressWaiting', language)}</span>
                  </p>
                  <div className="upgrade-actions">
                    {!record.recorded && (
                      <button
                        type="button"
                        className="btn btn-secondary"
                        data-testid="upgrade-progress-back"
                        onClick={() => setStep('confirm')}
                      >
                        <FullWebIcon name="arrowLeft" size={16} /> {t('back', language)}
                      </button>
                    )}
                    <button
                      type="button"
                      className="btn btn-primary"
                      data-testid="upgrade-progress-refresh"
                      onClick={reread}
                    >
                      <FullWebIcon name="refresh" size={16} /> {t('progressRefreshNow', language)}
                    </button>
                  </div>
                </div>
              )
            )}
            {status.phase === 'stalled' && (
              <div className="upgrade-notice" role="alert" data-testid="upgrade-progress-stalled">
                <FullWebIcon name="alert" size={20} />
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
                  <FullWebIcon name="refresh" size={16} /> {t('progressRetry', language)}
                </button>
              </div>
            )}
          </div>
        );
      })()}

      {boot.phase === 'ready' && shownStep === 'result' && (() => {
        const hasResultAnomalies = Boolean(
          record && (record.mismatchDays.length > 0 || record.manualItems.length > 0)
        );
        const pendingRestartAccess = Boolean(
          isCleanComplete &&
            !canonicalConfirmed &&
            ((resultAccessInfo && !resultAccessInfo.ready) || (!resultAccessInfo && executionAvailable))
        );
        const consolePrefix =
          resultAccessInfo?.managementBasePath ||
          record?.routing?.controlBasePath ||
          '';
        const fallbackAccessMode: AccessMode =
          record?.targetMode === 'external' ? 'connect_external' : 'migrate_legacy';
        const fallbackInferencePrefix =
          fallbackAccessMode === 'connect_external'
            ? ''
            : isCanonicalPrefix(record?.routing?.inferenceBasePath)
              ? record!.routing!.inferenceBasePath
              : '';
        const canonicalPendingTarget = Boolean(
          isCleanComplete &&
            canonicalConfirmed &&
            ((resultAccessInfo && !resultAccessInfo.ready) ||
              (!resultAccessInfo && isCanonicalPrefix(consolePrefix)))
        );
        const canonicalPendingExternal = Boolean(
          canonicalPendingTarget &&
            (resultAccessInfo
              ? resultAccessInfo.mode === 'connect_external' ||
                resultAccessInfo.code === 'external_target_unavailable'
              : fallbackAccessMode === 'connect_external')
        );
        const canonicalPendingGateway = Boolean(
          canonicalPendingTarget && !canonicalPendingExternal
        );
        const displayedResultAccessInfo: AccessInfo | undefined =
          resultAccessInfo ??
          (isCleanComplete && isCanonicalPrefix(consolePrefix)
            ? {
                mode: fallbackAccessMode,
                managementBasePath: consolePrefix,
                inferenceBasePath: fallbackInferencePrefix,
                ...(fallbackAccessMode === 'connect_external' && effectiveExternalBaseUrl
                  ? { externalBaseUrl: effectiveExternalBaseUrl }
                  : {}),
                protocols: [],
                ready: false,
                code: canonicalConfirmed
                  ? fallbackAccessMode === 'connect_external'
                    ? 'external_target_unavailable'
                    : 'gateway_pending'
                  : record?.routing?.existingPrefixesPreserved
                    ? 'routing_unavailable'
                    : 'upgrade_restart_required',
              }
            : undefined);
        const sourceSummary = record
          ? [formatSourceKind(record.sourceKind, language), record.sourceVersion]
              .filter(Boolean)
              .join(' · ')
          : '';
        return (
          <div className="upgrade-step-body" data-testid="upgrade-result">
            <ResultHeadline status={record} cleanComplete={isCleanComplete} language={language} />
            <p className="upgrade-step-help" data-testid="upgrade-result-help">
              {pendingRestartAccess
                ? isActivating
                  ? e('activationWaiting', language)
                  : executionAvailable
                    ? e('resultPendingRestartHelp', language)
                    : t('resultAccessPendingRestart', language)
                : canonicalPendingExternal
                  ? e('resultCanonicalExternalPendingHelp', language)
                  : canonicalPendingGateway
                    ? e('resultCanonicalGatewayPendingHelp', language)
                    : isCleanComplete && !hasResultAnomalies
                      ? t('resultCompleteSummary', language).replace(
                          '{source}',
                          sourceSummary || t('sourceKindLegacyManager', language)
                        )
                      : isCleanComplete
                        ? t('resultCompleteHelp', language)
                        : t('resultIncompleteHelp', language)}
            </p>
            {record && (
              <>
                {record.stage === 'rolled_back' && (
                  <div className="upgrade-notice" role="alert" data-testid="upgrade-result-rollback">
                    <FullWebIcon name="alert" size={20} />
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
                    <FullWebIcon name="alert" size={20} />
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
                    <FullWebIcon name="info" size={20} />
                    <div>
                      <h3>{t('resultUnreadableTitle', language)}</h3>
                      <p>{t('resultUnreadableHelp', language)}</p>
                    </div>
                  </div>
                )}
                {isCleanComplete ? (
                  <>
                    {hasResultAnomalies && (
                      <>
                        {record.mismatchDays.length > 0 && (
                          <Reconciliation status={record} language={language} />
                        )}
                        {record.manualItems.length > 0 && (
                          <ManualItems status={record} language={language} collapsible />
                        )}
                      </>
                    )}
                    <details className="upgrade-summary-disclosure is-neutral">
                      <summary>
                        <span className="upgrade-summary-main">
                          <span>{t('resultAuditToggle', language)}</span>
                        </span>
                        <span className="upgrade-summary-toggle">
                          {sourceSummary || t('migrationDetailsToggle', language)}
                        </span>
                      </summary>
                      <div className="upgrade-summary-body">
                        <StageLine status={record} language={language} testid="upgrade-result-stage" />
                        {(record.sourceKind || record.sourceVersion) && (
                          <dl className="upgrade-facts upgrade-facts-inline">
                            <div>
                              <dt>{t('resultSource', language)}</dt>
                              <dd data-testid="upgrade-result-source">{sourceSummary}</dd>
                            </div>
                          </dl>
                        )}
                        {record.mismatchDays.length === 0 && (
                          <Reconciliation status={record} language={language} />
                        )}
                        {record.manualItems.length === 0 && (
                          <ManualItems status={record} language={language} />
                        )}
                        {nextActionText(record.nextAction, language) && (
                          <p className="upgrade-sr-only" data-testid="upgrade-result-next">
                            {t('resultNext', language)}: {nextActionText(record.nextAction, language)}
                          </p>
                        )}
                      </div>
                    </details>
                  </>
                ) : (
                  <>
                    <StageLine status={record} language={language} testid="upgrade-result-stage" />
                    {(record.sourceKind || record.sourceVersion) && (
                      <dl className="upgrade-facts upgrade-facts-inline">
                        <div>
                          <dt>{t('resultSource', language)}</dt>
                          <dd data-testid="upgrade-result-source">{sourceSummary}</dd>
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
                {isCleanComplete && displayedResultAccessInfo && (
                  <div className="upgrade-result-access-wrap" data-testid="upgrade-result-access">
                    {displayedResultAccessInfo.ready ? (
                      <p className="upgrade-sr-only" data-testid="upgrade-access-active">
                        {t('resultAccessActive', language)}
                      </p>
                    ) : (
                      !canonicalConfirmed &&
                      (displayedResultAccessInfo.code === 'upgrade_restart_required' ||
                        !displayedResultAccessInfo.code) && (
                        <p className="upgrade-sr-only" data-testid="upgrade-access-pending-restart">
                          {t('resultAccessPendingRestart', language)}
                        </p>
                      )
                    )}
                    {canonicalPendingGateway && (
                      <p className="upgrade-step-status" role="status" data-testid="upgrade-gateway-waiting">
                        <FullWebIcon name="clock" size={18} />
                        {e('gatewayWaiting', language)}
                      </p>
                    )}
                    <div
                      data-testid={
                        isCanonicalPrefix(consolePrefix)
                          ? 'upgrade-result-console-banner'
                          : undefined
                      }
                    >
                      <AccessInfoSummary
                        client={FAIL_CLOSED_ACCESS_CLIENT}
                        auth={{ kind: 'admin', value: '' }}
                        info={displayedResultAccessInfo}
                        origin={previewOrigin || undefined}
                        defaultProtocolsCollapsed
                        completionRecovery
                        pendingRestart={
                          !canonicalConfirmed &&
                          !displayedResultAccessInfo.ready &&
                          displayedResultAccessInfo.code === 'upgrade_restart_required'
                        }
                      />
                    </div>
                  </div>
                )}
              </>
            )}
            {record?.stage === 'complete' && record.stageKnown && !resultAccessInfo?.ready && !canonicalConfirmed && (
              <div className="upgrade-promise-card" data-testid="upgrade-activation">
                <h3>{e('activateTitle', language)}</h3>
                <p>{bootstrapIsRetired ? e('activateBootstrapRetiredHelp', language) : e('activateHelp', language)}</p>
                {!bootstrapIsRetired &&
                  (executionAvailable ? (
                    <button
                      className="btn btn-primary"
                      data-testid="upgrade-execute-activate"
                      disabled={executionBusy || restarting}
                      onClick={() => void dispatchExecution({ action: 'activate' })}
                    >
                      {e('activateAction', language)}
                    </button>
                  ) : (
                    !isActivating && (
                      <ExecutionRecovery language={language} code={record?.execution?.code} />
                    )
                  ))}
                {bootstrapIsRetired && !isActivating && (
                  <p className="upgrade-step-help" role="status" data-testid="upgrade-bootstrap-retired-notice">
                    {e('activateBootstrapRetiredNote', language)}
                  </p>
                )}
                {isActivating && (
                  <p role="status" data-testid="upgrade-activation-waiting">
                    {e('activationWaiting', language)}
                  </p>
                )}
              </div>
            )}
            {!isCleanComplete && record?.stageKnown ? artifactStepContent : null}
            {!isCleanComplete &&
              record?.stageKnown &&
              (record.nextAction === 'reconcile' ||
                record.nextAction === 'cpa_import' ||
                record.nextAction === 'external_connect') &&
              (executionAvailable ? (
                <UpgradeExecutionActions
                  record={record}
                  language={language}
                  busy={executionBusy}
                  mode={guideMode}
                  onModeChange={(mode) => {
                    guideModeTouchedRef.current = true;
                    setGuideMode(mode);
                  }}
                  waitingForEngine={showArtifactStep && !artifactReady}
                  execute={dispatchExecution}
                />
              ) : (
                <ExecutionRecovery language={language} code={record.execution?.code} />
              ))}
            {status.phase === 'stalled' && status.error && !restarting && (
              <div className="upgrade-notice" role="alert" data-testid="upgrade-result-stalled">
                <FullWebIcon name="alert" size={20} />
                <div>
                  <h3>{t('progressStalledTitle', language)}</h3>
                  <p>{errorText(status.error, language)}</p>
                  {status.error.code && <code>{status.error.code}</code>}
                </div>
              </div>
            )}
            <div className="upgrade-actions">
              {isCleanComplete && (resultAccessInfo?.ready || canonicalConfirmed) && isCanonicalPrefix(consolePrefix) && (
                <a className="btn btn-primary" data-testid="upgrade-open-console"
                  href={buildPrefixPreviewUrls(previewOrigin, consolePrefix).consoleUrl}
                  onClick={() => onOpenConsole?.()}>
                  {e('openConsole', language)}
                </a>
              )}
              <button
                className={isCleanComplete ? 'btn btn-secondary btn-sm' : 'btn btn-primary'}
                data-testid="upgrade-result-reread"
                onClick={reread}
              >
                <FullWebIcon name="refresh" size={15} /> {t('resultReread', language)}
              </button>
            </div>
          </div>
        );
      })()}
    </section>
  );
}

// The success headline is rendered for `complete` and nothing else: a failed,
// rolled-back or unrecognised operation always gets the incomplete headline.
function ResultHeadline({
  status,
  cleanComplete,
  language,
}: {
  status?: UpgradeStatus;
  cleanComplete: boolean;
  language: string;
}) {
  if (cleanComplete && status?.stage === 'complete' && status.stageKnown) {
    return (
      <div className="upgrade-result-hero">
        <span className="upgrade-complete-badge" aria-hidden="true">
          <FullWebIcon name="check" size={20} />
        </span>
        <h2 className="upgrade-result-headline is-complete" data-testid="upgrade-result-complete">
          {t('resultCompleteTitle', language)}
        </h2>
        {(status.targetMode === 'managed' || status.targetMode === 'external') && (
          <span className="badge badge-neutral">
            {status.targetMode === 'external'
              ? t('modeBadgeExternal', language)
              : t('modeBadgeManaged', language)}
          </span>
        )}
      </div>
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
  const note = t('progressStageNote', language);
  return (
    <div className="upgrade-stage" data-testid={testid}>
      <span className="upgrade-stage-label">{t('progressStage', language)}</span>
      <span className="upgrade-stage-value">
        {label ? label : status.stage ? <code>{status.stage}</code> : t('preflightUnknown', language)}
      </span>
      {note && <span className="upgrade-stage-note">{note}</span>}
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
  const isEmpty = status.mismatchDays.length === 0;
  return (
    <div
      className={`upgrade-reconciliation ${isEmpty ? 'is-empty' : ''}`.trim()}
      data-testid="upgrade-reconciliation"
    >
      <h3>{t('resultReconciliationTitle', language)}</h3>
      {isEmpty ? (
        <p data-testid="upgrade-reconciliation-none">{t('resultReconciliationNone', language)}</p>
      ) : (
        <>
          <p className="upgrade-step-help">{t('resultReconciliationHelp', language)}</p>
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
function formatManualItem(item: string, language: string) {
  if (language.startsWith('en')) return <span>{item}</span>;
  const match = item.match(/^(section|config|credential|key):([^:]+):\s*(.+?)\s*\(([a-z0-9_]+)\)$/i);
  if (!match) return <span>{item}</span>;
  const [, , targetName, , reasonCode] = match;
  let reasonLabel = '';
  if (reasonCode === 'not_in_import_scope') {
    reasonLabel = t('manualReasonNotInScope', language);
  } else if (reasonCode === 'reconfigure_after_adoption') {
    reasonLabel = t('manualReasonReconfigure', language);
  } else if (
    reasonCode === 'reauthenticate_after_adoption' ||
    reasonCode === 'runtime_only_credential'
  ) {
    reasonLabel = t('manualReasonReauth', language);
  } else if (reasonCode === 'removed_upstream') {
    reasonLabel = t('manualReasonRemoved', language);
  }
  if (!reasonLabel) return <span>{item}</span>;
  return (
    <span>
      <code>{targetName.trim()}</code> — {reasonLabel}
      <span className="upgrade-sr-only"> {item}</span>
    </span>
  );
}

function ManualItems({
  status,
  language,
  collapsible = false,
}: {
  status: UpgradeStatus;
  language: string;
  collapsible?: boolean;
}) {
  const isEmpty = status.manualItems.length === 0;
  if (!isEmpty && collapsible) {
    return (
      <details className="upgrade-summary-disclosure is-neutral" data-testid="upgrade-manual-items">
        <summary>
          <span className="upgrade-summary-main">
            <span>
              {t('manualDisclosureSummary', language).replace(
                '{count}',
                String(status.manualItems.length)
              )}
            </span>
          </span>
          <span className="upgrade-summary-toggle">{t('migrationDetailsToggle', language)}</span>
        </summary>
        <div className="upgrade-summary-body">
          <ul className="upgrade-list">
            {status.manualItems.map((item) => (
              <li key={item}>{formatManualItem(item, language)}</li>
            ))}
          </ul>
        </div>
      </details>
    );
  }
  return (
    <div
      className={`upgrade-manual ${isEmpty ? 'is-empty' : ''}`.trim()}
      data-testid="upgrade-manual-items"
    >
      <h3>{t('resultManualTitle', language)}</h3>
      {isEmpty ? (
        <p data-testid="upgrade-manual-none">{t('resultManualNone', language)}</p>
      ) : (
        <ul className="upgrade-list">
          {status.manualItems.map((item) => (
            <li key={item}>{formatManualItem(item, language)}</li>
          ))}
        </ul>
      )}
    </div>
  );
}

function formatSourceKind(kind: string | undefined, language: string): string {
  if (!kind) return t('preflightUnknown', language);
  if (kind === 'legacy-manager') return t('sourceKindLegacyManager', language);
  if (kind === 'upgraded-legacy') return t('sourceKindUpgradedLegacy', language);
  return kind;
}

function formatHumanBytes(rawBytes: number): string {
  if (!Number.isFinite(rawBytes) || rawBytes < 0) return String(rawBytes);
  if (rawBytes < 1024) return `${rawBytes} B`;
  const kb = rawBytes / 1024;
  if (kb < 1024) return `${kb.toFixed(1)} KB`;
  const mb = kb / 1024;
  if (mb < 1024) return `${mb.toFixed(1)} MB`;
  const gb = mb / 1024;
  return `${gb.toFixed(2)} GB`;
}

function summarizePreflightCheck(check: PreflightCheck, language: string): string | null {
  if (check.status !== 'pass' && check.status !== 'pending') return null;
  const detail = check.detail || '';
  if (check.name === 'source_ownership' && check.status === 'pass') {
    return t('checkSummaryOwnershipPass', language);
  }
  if (check.name === 'source_version') {
    if (check.status === 'pending') {
      return t('checkPending', language);
    }
    return t('checkSummaryVersionPass', language);
  }
  if (check.name === 'fingerprint' && check.status === 'pass') {
    const m = detail.match(/sourceBytes=(\d+)/);
    if (m) {
      return t('checkSummaryFingerprintPass', language).replace(
        '{size}',
        formatHumanBytes(Number(m[1]))
      );
    }
    return t('checkSummaryFingerprintPlain', language);
  }
  if (check.name === 'disk' && check.status === 'pass') {
    const req = detail.match(/requiredBytes=(\d+)/);
    const avail = detail.match(/availableBytes=(\d+)/);
    if (req && avail) {
      return t('checkSummaryDiskPass', language)
        .replace('{required}', formatHumanBytes(Number(req[1])))
        .replace('{available}', formatHumanBytes(Number(avail[1])));
    }
    return null;
  }
  if (check.name === 'cpa_classification' && check.status === 'pass') {
    if (detail.includes('kind=external')) return t('checkSummaryCpaExternal', language);
    if (detail.includes('kind=managed_stack')) return t('checkSummaryCpaManaged', language);
    return null;
  }
  if (check.name === 'parity' && check.status === 'pass') {
    if (
      detail.includes('deletedEventRefs=0') &&
      detail.includes('missingProjection=0') &&
      detail.includes('mismatchedProjection=0') &&
      detail.includes('coverageDaysWithGaps=0')
    ) {
      return t('checkSummaryParityPass', language);
    }
    return null;
  }
  return null;
}

function ExecutionRecovery({ language, code }: { language: string; code?: string }) {
  return (
    <div className="upgrade-notice" role="status" data-testid="upgrade-execution-recovery">
      <FullWebIcon name="info" size={20} />
      <div>
        <h3>{e('recoveryTitle', language)}</h3>
        <p>{e('recoveryHelp', language)}</p>
        {code && <p>{blockedCodeText(code, language)}</p>}
        {code && <code>{code}</code>}
      </div>
    </div>
  );
}

function UpgradeExecutionActions({
  record,
  language,
  busy,
  mode,
  onModeChange,
  waitingForEngine,
  execute,
}: {
  record: UpgradeStatus;
  language: string;
  busy: boolean;
  mode: 'managed' | 'external';
  onModeChange: (mode: 'managed' | 'external') => void;
  waitingForEngine: boolean;
  execute: (input: UpgradeExecutionInput) => Promise<boolean>;
}) {
  const [acknowledgedDays, setAcknowledgedDays] = useState<string[]>([]);
  const [acknowledgedTruncatedTotal, setAcknowledgedTruncatedTotal] = useState<number | null>(null);
  const acknowledgedAllTruncated =
    Boolean(record.mismatchDaysTruncated) &&
    acknowledgedTruncatedTotal === record.mismatchDaysTotal;
  const [acknowledgedParity, setAcknowledgedParity] = useState(false);
  const [pluginsChoice, setPluginsChoice] = useState<'' | 'disabled' | 'enabled'>('');
  const [externalOverride, setExternalOverride] = useState(false);
  const [externalBaseUrl, setExternalBaseUrl] = useState('');
  const externalKeyRef = useRef<HTMLInputElement | null>(null);
  const managedAvailable = Boolean(record.execution?.managedAvailable);
  const activeMode = managedAvailable ? mode : 'external';
  const externalSecretScope = `${activeMode}:${record.stage}`;
  const externalSecretEditable =
    activeMode === 'external' &&
    record.stage !== 'external_connect' &&
    record.stage !== 'verify' &&
    record.stage !== 'complete';
  const [externalKeyScope, setExternalKeyScope] = useState<string | null>(null);
  const externalHasKey = externalSecretEditable && externalKeyScope === externalSecretScope;
  const bindExternalKeyRef = useCallback((node: HTMLInputElement | null) => {
    if (node === null && externalKeyRef.current) {
      externalKeyRef.current.value = '';
    }
    externalKeyRef.current = node;
  }, []);
  const clearExternalSecret = useCallback(() => {
    if (externalKeyRef.current) externalKeyRef.current.value = '';
    setExternalKeyScope(null);
  }, []);
  useEffect(() => {
    if (!externalSecretEditable && externalKeyRef.current) {
      externalKeyRef.current.value = '';
    }
  }, [externalSecretEditable]);
  useEffect(
    () => () => {
      if (externalKeyRef.current) externalKeyRef.current.value = '';
    },
    [],
  );
  if (record.nextAction === 'reconcile' || (record.stage === 'copy_in' && record.domainsDone.includes('source_copy'))) {
    const days = record.mismatchDays;
    const allDaysChecked = days.every((day) => acknowledgedDays.includes(day));
    const daysSatisfied = record.mismatchDaysTruncated ? acknowledgedAllTruncated : allDaysChecked;
    const needsParityAck =
      Boolean(record.hasParityGaps) ||
      (days.length === 0 && record.execution?.code === 'upgrade_reconcile_mismatch');
    const blocked = busy || !daysSatisfied || (needsParityAck && !acknowledgedParity);
    return (
      <div className="upgrade-execution-panel" data-testid="upgrade-execution-actions">
        <div className="upgrade-execution-section">
          <h3>{e('reconcileTitle', language)}</h3>
          <p>{e('reconcileHelp', language)}</p>
          {record.mismatchDaysTruncated && (
            <div className="upgrade-notice" role="alert" data-testid="upgrade-truncated-days">
              <FullWebIcon name="alert" size={18} /><p>{e('truncatedDays', language)}</p>
            </div>
          )}
          {days.length > 0 && (
            <ul className="upgrade-days">{days.map((day) => (
              <li key={day}>
                <label className="upgrade-ack" htmlFor={`upgrade-ack-day-${day}-input`}>
                  <span className="journey-checkbox-control">
                    <input
                      id={`upgrade-ack-day-${day}-input`}
                      type="checkbox"
                      data-testid={`upgrade-ack-day-${day}`}
                      checked={acknowledgedDays.includes(day)}
                      disabled={busy}
                      onChange={(ev) => setAcknowledgedDays((cur) => ev.target.checked ? [...cur, day] : cur.filter((d) => d !== day))}
                    />
                    <span className="journey-checkbox-box" aria-hidden="true">
                      <FullWebIcon name="check" size={13} />
                    </span>
                  </span>
                  <span>{e('acknowledgeDay', language).replace('{day}', day)}</span>
                </label>
              </li>
            ))}</ul>
          )}
          {record.mismatchDaysTruncated && (
            <label className="upgrade-ack" htmlFor="upgrade-ack-all-truncated-input">
              <span className="journey-checkbox-control">
                <input
                  id="upgrade-ack-all-truncated-input"
                  type="checkbox"
                  data-testid="upgrade-ack-all-truncated"
                  checked={acknowledgedAllTruncated}
                  disabled={busy}
                  onChange={(ev) =>
                    setAcknowledgedTruncatedTotal(ev.target.checked ? record.mismatchDaysTotal : null)
                  }
                />
                <span className="journey-checkbox-box" aria-hidden="true">
                  <FullWebIcon name="check" size={13} />
                </span>
              </span>
              <span>{e('acknowledgeAllTruncated', language).replace('{total}', String(record.mismatchDaysTotal))}</span>
            </label>
          )}
          <label className="upgrade-ack" htmlFor="upgrade-ack-parity-input">
            <span className="journey-checkbox-control">
              <input
                id="upgrade-ack-parity-input"
                type="checkbox"
                data-testid="upgrade-ack-parity"
                checked={acknowledgedParity}
                disabled={busy}
                onChange={(ev) => setAcknowledgedParity(ev.target.checked)}
              />
              <span className="journey-checkbox-box" aria-hidden="true">
                <FullWebIcon name="check" size={13} />
              </span>
            </span>
            <span>{e('acknowledgeParity', language)}</span>
          </label>
        </div>
        <div className="upgrade-actions">
          <button className="btn btn-primary" data-testid="upgrade-execute-reconcile" disabled={blocked}
            onClick={() => {
              const selectedDays = acknowledgedAllTruncated
                ? days
                : days.filter((d) => acknowledgedDays.includes(d));
              void execute({
                action: 'reconcile',
                ...(selectedDays.length > 0 ? { acknowledgeDays: selectedDays } : {}),
                ...(record.mismatchDaysTruncated && acknowledgedAllTruncated
                  ? { acknowledgeAllMismatches: true }
                  : {}),
                ...(acknowledgedParity ? { acknowledgeParityGaps: true } : {}),
              });
            }}>{e('reconcileAction', language)}</button>
        </div>
      </div>
    );
  }
  if (record.nextAction !== 'cpa_import' && record.nextAction !== 'external_connect') return null;
  // activeMode is derived above before hooks so the secret-clear effect observes mode switches.
  const externalPairInvalid = externalOverride && (!externalBaseUrl.trim() || !externalHasKey);
  return (
    <div className="upgrade-execution-panel" data-testid="upgrade-execution-actions">
      <div className="upgrade-execution-section">
        <h3>{e('targetTitle', language)}</h3>
        <p>{e('targetHelp', language)}</p>
        {!managedAvailable && (
          <div className="upgrade-notice" data-testid="upgrade-managed-unavailable">
            <FullWebIcon name="info" size={18} /><p>{e('managedUnavailable', language)}</p>
          </div>
        )}
        <div className="upgrade-mode-switch">
          {managedAvailable && (
            <button type="button" className={`upgrade-mode-card${activeMode === 'managed' ? ' is-selected' : ''}`}
              data-testid="upgrade-mode-managed" aria-pressed={activeMode === 'managed'} disabled={busy}
              onClick={() => onModeChange('managed')}>{e('managedRecommended', language)}</button>
          )}
          <button type="button" className={`upgrade-mode-card${activeMode === 'external' ? ' is-selected' : ''}`}
            data-testid="upgrade-mode-external" aria-pressed={activeMode === 'external'} disabled={busy}
            onClick={() => onModeChange('external')}>{e('externalAlternate', language)}</button>
        </div>
      </div>
      {activeMode === 'managed' ? (
        <div className="upgrade-execution-section">
          <label className="upgrade-field" htmlFor="upgrade-plugins-select"><span>{e('pluginsLabel', language)}</span>
            <select id="upgrade-plugins-select" data-testid="upgrade-plugins" value={pluginsChoice} disabled={busy}
              onChange={(ev) => setPluginsChoice(ev.target.value as '' | 'disabled' | 'enabled')}>
              <option value="">{e('pluginsChoose', language)}</option>
              <option value="disabled">{e('pluginsDisabled', language)}</option>
              <option value="enabled">{e('pluginsEnabled', language)}</option>
            </select></label>
          <p className="upgrade-step-help">{e('pluginsHelp', language)}</p>
          <button className="btn btn-primary" data-testid="upgrade-execute-import"
            disabled={busy || waitingForEngine || !pluginsChoice}
            onClick={() => void execute({ action: 'cpa-import', pluginsEnabled: pluginsChoice === 'enabled' })}>{e('importAction', language)}</button>
        </div>
      ) : (
        <div className="upgrade-execution-section">
          <p>{e('externalReuse', language)}</p>
          <label className="upgrade-ack" htmlFor="upgrade-external-override-input">
            <span className="journey-checkbox-control">
              <input
                id="upgrade-external-override-input"
                type="checkbox"
                data-testid="upgrade-external-override"
                checked={externalOverride}
                disabled={busy}
                onChange={(ev) => {
                  setExternalOverride(ev.target.checked);
                  if (!ev.target.checked) clearExternalSecret();
                }}
              />
              <span className="journey-checkbox-box" aria-hidden="true">
                <FullWebIcon name="check" size={13} />
              </span>
            </span>
            <span>{e('externalOverride', language)}</span>
          </label>
          {externalOverride && (
            <div className="upgrade-execution-fields">
              <label className="upgrade-field"><span>{e('externalUrl', language)}</span>
                <input type="url" data-testid="upgrade-external-url" value={externalBaseUrl} disabled={busy}
                  onChange={(ev) => setExternalBaseUrl(ev.target.value)} /></label>
              <label className="upgrade-field"><span>{e('externalKey', language)}</span>
                <input ref={bindExternalKeyRef} type="password" autoComplete="off" data-testid="upgrade-external-key"
                  disabled={busy} onChange={(ev) => setExternalKeyScope(ev.target.value.trim() !== '' ? externalSecretScope : null)} /></label>
              {externalPairInvalid && (
                <p className="upgrade-step-help" data-testid="upgrade-external-pair-hint">
                  {e('externalOverridePairRequired', language)}
                </p>
              )}
            </div>
          )}
          <button className="btn btn-primary" data-testid="upgrade-execute-external"
            disabled={busy || externalPairInvalid}
            onClick={() => {
              if (!externalOverride) {
                void execute({ action: 'external-connect' });
                return;
              }
              const url = externalBaseUrl.trim();
              const key = externalKeyRef.current?.value.trim() || '';
              if (!url || !key) return;
              void execute({ action: 'external-connect', baseUrl: url, managementKey: key });
            }}>{e('externalAction', language)}</button>
        </div>
      )}
    </div>
  );
}
