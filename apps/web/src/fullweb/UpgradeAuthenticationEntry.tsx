import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { FullWebLoginPage, FullWebUpgradeHeader } from './FullWebLoginPage';
import { FullWebIcon } from './FullWebIcon';
import {
  createUpgradeActivationRead,
  createUpgradeWizardRead,
  createUpgradeWizardExecute,
  createUpgradeWizardSaveRouting,
  createUpgradeWizardVerify,
  markBootstrapRetiredRecovery,
  FreshUpgradeFlow,
  markCanonicalUpgradeStatus,
  parseUpgradeStatus,
  UpgradeWizardError,
  type UpgradeStatus,
  type UpgradeWizardExecute,
  type UpgradeWizardRead,
  type UpgradeWizardSaveRouting,
  type UpgradeWizardVerify,
} from './FreshUpgradeFlow';
import { upgradeEntryText as text } from '../i18n/upgradeEntry';
import { isCanonicalPrefix } from './accessPrefix';
import { BOOTSTRAP_UI, type FreshOperation } from './bootstrapTransport';
import { parseAccessInfo, type AccessInfo } from './FreshAccessInfo';
import './upgradeEntry.css';
import { createUpgradeArtifactTransport } from './upgradeArtifactTransport';
import type { ArtifactTransport } from './freshArtifact';
import {
  clearCommittedUpgradeStatus,
  rememberCommittedUpgradeStatus,
  rememberedCommittedUpgradeStatus,
  rememberUpgradeCredential,
  type PersistedCommittedUpgradeStatus,
  rememberedUpgradeCredential,
  UpgradeAuthenticationError,
  type UpgradeEntryObservation,
  verifyUpgradeCredential,
} from './upgradeSession';

export function UpgradeAuthenticationEntry() {
  const { i18n } = useTranslation();
  const language = i18n.resolvedLanguage || i18n.language;
  const [revision, setRevision] = useState('');
  const revisionRef = useRef('');
  const committedRevisionRef = useRef('');
  // Which entry status the proved credential earned. `observation_unavailable`
  // is a verified identity with no public observation: the page must say exactly
  // that and never present it as an upgrade-ready source.
  const [observation, setObservation] =
    useState<UpgradeEntryObservation>('unsupported_upgrade');
  const [error, setError] = useState<{
    reason: UpgradeAuthenticationError['reason'] | 'expired';
    expectedOrigin?: string;
  } | null>(null);
  const [checking, setChecking] = useState(false);
  const checkingRef = useRef(false);
  // Whether the wizard below found a recorded operation. The entry's own copy
  // describes a source with no public observation; once an operation is recorded
  // that description is wrong, because the read-only view below is a migration
  // already under way and this page can act on it.
  const [recorded, setRecorded] = useState(false);
  const [operationSessionKey, setOperationSessionKey] = useState(0);
  const heading = useRef<HTMLHeadingElement>(null);
  const credential = useRef('');
  const request = useRef<AbortController | null>(null);
  const session = useRef<AbortController | null>(null);
  const sequence = useRef(0);
  const restored = useRef(false);
  const expiresAt = useRef(0);
  const executionActiveRef = useRef(false);
  const [executionActive, setExecutionActive] = useState(false);
  const continuationBase = useRef('');
  const lastActivationPrefix = useRef('');
  const committedStatus = useRef<PersistedCommittedUpgradeStatus | undefined>(undefined);
  const canonicalObserved = useRef(false);
  const bootstrapRetired = useRef(false);
  const [activated, setActivated] = useState(false);
  const [canonicalConfirmed, setCanonicalConfirmed] = useState(false);
  const [bootstrapRetiredState, setBootstrapRetiredState] = useState(false);
  const [activationPersisted, setActivationPersisted] = useState(false);
  const retireEnvelope = useCallback(
    (
      message: UpgradeAuthenticationError['reason'] | 'expired' | '' = '',
      expectedOrigin?: string
    ) => {
      ++sequence.current;
      request.current?.abort();
      session.current?.abort();
      session.current = null;
      credential.current = '';
      executionActiveRef.current = false;
      setExecutionActive(false);
      continuationBase.current = '';
      lastActivationPrefix.current = '';
      committedStatus.current = undefined;
      setActivationPersisted(false);
      revisionRef.current = '';
      committedRevisionRef.current = '';
      canonicalObserved.current = false;
      bootstrapRetired.current = false;
      setActivated(false);
      setCanonicalConfirmed(false);
      setBootstrapRetiredState(false);
      setRecorded(false);
      expiresAt.current = 0;
      setRevision('');
      checkingRef.current = false;
      setChecking(false);
      setError(message ? { reason: message, expectedOrigin } : null);
    },
    []
  );
  const revoke = useCallback(
    (
      message: UpgradeAuthenticationError['reason'] | 'expired' | '' = '',
      expectedOrigin?: string
    ) => {
      const hadCredential = Boolean(credential.current) || Boolean(rememberedUpgradeCredential());
      retireEnvelope(message, expectedOrigin);
      if (!message || hadCredential) {
        rememberUpgradeCredential('', false);
      }
      clearCommittedUpgradeStatus();
      committedRevisionRef.current = '';
    },
    [retireEnvelope]
  );
  const hydrateCommittedStatus = useCallback((expectedRevision?: string) => {
    const targetRevision = expectedRevision || committedRevisionRef.current || undefined;
    if (!targetRevision) {
      return;
    }
    if (committedStatus.current) {
      if (
        !committedStatus.current.revision ||
        committedStatus.current.revision !== targetRevision
      ) {
        committedStatus.current = undefined;
        setActivationPersisted(false);
        clearCommittedUpgradeStatus();
      } else {
        return;
      }
    }
    const persisted = rememberedCommittedUpgradeStatus(targetRevision);
    if (!persisted) {
      setActivationPersisted(false);
      return;
    }
    const parsed = parseUpgradeStatus(persisted.snapshot);
    if (
      parsed?.stage === 'complete' &&
      parsed.stageKnown &&
      parsed.routing?.committed === true &&
      isCanonicalPrefix(parsed.routing.controlBasePath) &&
      parsed.routing.controlBasePath === persisted.controlBasePath
    ) {
      committedStatus.current = persisted;
      setActivationPersisted(persisted.activationRequested === true);
    } else {
      clearCommittedUpgradeStatus();
      committedStatus.current = undefined;
      setActivationPersisted(false);
    }
  }, []);
  const recordCommittedStatus = useCallback((payload: unknown) => {
    const status = parseUpgradeStatus(payload);
    if (
      status?.stage === 'complete' &&
      status.stageKnown &&
      status.routing?.committed === true &&
      isCanonicalPrefix(status.routing.controlBasePath)
    ) {
      const doc = payload as Record<string, unknown>;
      const activeRevision = committedRevisionRef.current || undefined;
      const preserveActivation =
        committedStatus.current &&
        committedStatus.current.controlBasePath === status.routing.controlBasePath &&
        (!activeRevision || committedStatus.current.revision === activeRevision)
          ? committedStatus.current.activationRequested
          : undefined;
      const persisted = rememberCommittedUpgradeStatus(doc, {
        ...(activeRevision ? { revision: activeRevision } : {}),
        ...(typeof preserveActivation === 'boolean'
          ? { activationRequested: preserveActivation }
          : {}),
      });
      committedStatus.current = persisted;
      setActivationPersisted(persisted?.activationRequested === true);
    } else if (status) {
      committedStatus.current = undefined;
      setActivationPersisted(false);
      clearCommittedUpgradeStatus();
    }
  }, []);
  const markActivationPersisted = useCallback((expectedPrefix?: string) => {
    const current = committedStatus.current;
    if (!current) return;
    if (expectedPrefix && current.controlBasePath !== expectedPrefix) return;
    const updated = rememberCommittedUpgradeStatus(current.snapshot, {
      ...(committedRevisionRef.current ? { revision: committedRevisionRef.current } : {}),
      activationRequested: true,
    });
    if (updated) {
      committedStatus.current = updated;
      setActivationPersisted(true);
    }
  }, []);
  const logout = useCallback(() => {
    const isPostCutover = canonicalObserved.current || bootstrapRetired.current;
    const savedCommitted = parseUpgradeStatus(committedStatus.current?.snapshot);
    const targetPrefix = [
      lastActivationPrefix.current,
      savedCommitted?.routing?.controlBasePath,
      rememberedCommittedUpgradeStatus(committedRevisionRef.current || undefined)?.controlBasePath,
    ].find((candidate): candidate is string => isCanonicalPrefix(candidate));
    revoke();
    if (
      isPostCutover &&
      targetPrefix &&
      typeof window !== 'undefined' &&
      typeof window.location?.assign === 'function'
    ) {
      window.location.assign(targetPrefix);
    }
  }, [revoke]);

  const authenticate = useCallback(
    async (key: string, remember: boolean, source?: string) => {
      if (source && (canonicalObserved.current || bootstrapRetired.current || continuationBase.current)) {
        return false;
      }
      request.current?.abort();
      const current = ++sequence.current;
      const controller = new AbortController();
      request.current = controller;
      // Renewal keeps the shell mounted, but hides the previous observation
      // and disables its actions until the server has verified it again.
      if (!source) {
        session.current?.abort();
        session.current = null;
        revisionRef.current = '';
        committedRevisionRef.current = '';
        setRecorded(false);
        setRevision('');
      }
      checkingRef.current = true;
      setChecking(true);
      setError(null);
      const timeout = window.setTimeout(() => controller.abort(), 10_000);
      try {
        const verified = await verifyUpgradeCredential(key, controller.signal, source);
        if (sequence.current !== current || controller.signal.aborted) return false;
        if (!session.current || session.current.signal.aborted) {
          session.current = new AbortController();
        }
        const prevCommittedRevision = committedRevisionRef.current;
        const nextCommittedRevision = verified.operationRevision || verified.revision;
        const operationRevisionChanged =
          Boolean(source) &&
          Boolean(verified.operationRevision) &&
          Boolean(prevCommittedRevision) &&
          prevCommittedRevision !== nextCommittedRevision;
        credential.current = key;
        expiresAt.current = Date.now() + 60_000;
        revisionRef.current = verified.revision;
        committedRevisionRef.current = nextCommittedRevision;
        if (
          Boolean(source) &&
          !verified.operationRevision &&
          prevCommittedRevision === source &&
          committedStatus.current &&
          committedStatus.current.revision === prevCommittedRevision &&
          prevCommittedRevision !== nextCommittedRevision
        ) {
          committedStatus.current = rememberCommittedUpgradeStatus(
            committedStatus.current.snapshot,
            {
              revision: nextCommittedRevision,
              ...(typeof committedStatus.current.activationRequested === 'boolean'
                ? { activationRequested: committedStatus.current.activationRequested }
                : {}),
            }
          );
        }
        hydrateCommittedStatus(nextCommittedRevision);
        if (operationRevisionChanged) {
          setRecorded(false);
          if (!canonicalObserved.current && !bootstrapRetired.current) {
            lastActivationPrefix.current = '';
            continuationBase.current = '';
            setActivated(false);
          }
          setOperationSessionKey((currentKey) => currentKey + 1);
        }
        if (!source) rememberUpgradeCredential(key, remember);
        setObservation(verified.observation);
        setRevision(verified.revision);
        return true;
      } catch (err) {
        if (sequence.current === current) {
          // Root jobs and brief worker unavailability (503) must not be mistaken
          // for 401/409 session revocation while status polling stays authoritative.
          if (
            source &&
            (canonicalObserved.current ||
              bootstrapRetired.current ||
              continuationBase.current ||
              (executionActiveRef.current &&
                (!(err instanceof UpgradeAuthenticationError) ||
                  err.reason === 'network' ||
                  err.reason === 'unavailable' ||
                  err.reason === 'source_ownership_unavailable')) ||
              (err instanceof UpgradeAuthenticationError &&
                err.reason === 'unavailable' &&
                Date.now() < expiresAt.current))
          ) {
            return false;
          }
          if (err instanceof UpgradeAuthenticationError) {
            revoke(err.reason, err.expectedOrigin);
          } else {
            revoke('network');
          }
        }
        return false;
      } finally {
        window.clearTimeout(timeout);
        if (sequence.current === current) {
          checkingRef.current = false;
          setChecking(false);
        }
      }
    },
    [hydrateCommittedStatus, revoke]
  );

  const restore = useCallback(async () => {
    if (restored.current) return;
    restored.current = true;
    const candidate = rememberedUpgradeCredential();
    if (candidate) await authenticate(candidate, true);
  }, [authenticate]);

  const adapter = useMemo(
    () => ({
      address: window.location.origin,
      error: error
        ? error.reason === 'origin_rejected' && error.expectedOrigin
          ? text('origin_rejected_url', language)
              .replace('{origin}', error.expectedOrigin)
              .replace('{url}', `${error.expectedOrigin}${BOOTSTRAP_UI}`)
          : text(error.reason, language)
        : '',
      status: checking ? text('checking', language) : undefined,
      connectionHint: text('loginHint', language),
      credentialPlaceholder: text('placeholder', language),
      recoveryHelp: {
        label: text('recoveryLabel', language),
        description: text('recoveryHelp', language),
      },
      restore,
      authenticate: (key: string, remember: boolean) => authenticate(key, remember),
    }),
    [authenticate, checking, error, language, restore]
  );

  const wizardRead = useMemo<UpgradeWizardRead>(() => {
    const bootstrapRead = createUpgradeWizardRead(() => credential.current);
    const canonicalRead = createUpgradeActivationRead(() => credential.current);
    const proveCanonical = async (
      prefix: string,
      snapshot: Record<string, unknown>,
      saved: UpgradeStatus,
      signal: AbortSignal
    ) => {
      const identity = (await canonicalRead(prefix, 'bootstrap/identity', signal)) as
        | (FreshOperation['identity'] & { target: string; revision: number; committed: boolean })
        | undefined;
      if (
        !identity ||
        identity.target !== prefix ||
        identity.committed !== true ||
        !identity.installationId ||
        !identity.generation ||
        !identity.operationId ||
        !Number.isSafeInteger(identity.revision) ||
        identity.revision <= 0
      ) {
        throw new UpgradeWizardError('response');
      }
      bootstrapRetired.current = true;
      setBootstrapRetiredState(true);
      const op = (await canonicalRead(prefix, 'operation', signal)) as FreshOperation | null;
      if (
        !op ||
        op.productComplete !== true ||
        op.adminInitialized !== true ||
        op.identity?.installationId !== identity.installationId ||
        op.identity?.generation !== identity.generation ||
        op.identity?.operationId !== identity.operationId ||
        op.routing?.operationId !== identity.operationId ||
        op.routing?.revision !== identity.revision ||
        op.routing?.target !== prefix ||
        op.routing?.committed !== true ||
        Boolean(op.routing.sharedEntry) !== Boolean(saved.routing?.sharedEntry) ||
        (op.routing.inferenceTarget || '') !== (saved.routing?.inferenceBasePath ?? '')
      ) {
        throw new UpgradeWizardError('response');
      }
      if (signal.aborted) throw new UpgradeWizardError('unavailable');
      canonicalObserved.current = true;
      setActivated(true);
      setCanonicalConfirmed(true);
      window.history?.replaceState?.(null, '', prefix);

      const expectedMode = saved.targetMode === 'external' ? 'connect_external' : 'migrate_legacy';
      const expectedInference = saved.routing?.inferenceBasePath ?? '';
      let access: AccessInfo | null = null;
      try {
        access = parseAccessInfo(await canonicalRead(prefix, 'api-keys/access-info', signal));
      } catch (accessErr) {
        if (
          signal.aborted ||
          !(accessErr instanceof UpgradeWizardError) ||
          accessErr.failure === 'authentication' ||
          accessErr.failure === 'response'
        ) {
          throw accessErr;
        }
        const synthesizedAccess: AccessInfo = {
          mode: expectedMode,
          managementBasePath: prefix,
          inferenceBasePath: expectedMode === 'connect_external' ? '' : expectedInference,
          ...(expectedMode === 'connect_external' && saved.routing?.externalBaseUrl
            ? { externalBaseUrl: saved.routing.externalBaseUrl }
            : {}),
          protocols: [],
          ready: false,
          code: op.gatewayPending
            ? 'gateway_pending'
            : accessErr.code ||
              (expectedMode === 'connect_external'
                ? 'external_target_unavailable'
                : 'routing_unavailable'),
        };
        return markCanonicalUpgradeStatus({
          ...snapshot,
          execution: undefined,
          accessInfo: synthesizedAccess,
        });
      }
      if (
        !access ||
        access.managementBasePath !== prefix ||
        access.mode !== expectedMode ||
        access.inferenceBasePath !== expectedInference ||
        signal.aborted
      ) {
        throw new UpgradeWizardError('response');
      }
      const liveReady = access.ready && op.gatewayPending !== true;
      const normalizedCode =
        !liveReady &&
        op.gatewayPending === true &&
        (!access.code ||
          access.code === 'installation_incomplete' ||
          access.code === 'upgrade_restart_required')
          ? 'gateway_pending'
          : access.code;
      return markCanonicalUpgradeStatus({
        ...snapshot,
        execution: undefined,
        accessInfo: {
          ...access,
          ready: liveReady,
          ...(normalizedCode ? { code: normalizedCode } : {}),
        },
      });
    };
    return async (resource, signal) => {
      const prefix = lastActivationPrefix.current;
      const snapshot = committedStatus.current?.snapshot;
      const saved = parseUpgradeStatus(snapshot);
      if (
        resource === 'installation/upgrade/status' &&
        prefix &&
        saved?.routing?.controlBasePath === prefix &&
        snapshot
      ) {
        try {
          return await proveCanonical(prefix, snapshot, saved, signal);
        } catch (err) {
          // Before cutover only, real Ingress (503) or direct Manager (404) can mean bootstrap is still live.
          const preCutoverUnavailable =
            !canonicalObserved.current &&
            !bootstrapRetired.current &&
            err instanceof UpgradeWizardError &&
            (err.status === 404 || err.status === 503);
          if (!preCutoverUnavailable) throw err;
          try {
            const fallback = await bootstrapRead(resource, signal);
            recordCommittedStatus(fallback);
            return fallback;
          } catch (bootstrapErr) {
            if (
              !signal.aborted &&
              bootstrapErr instanceof UpgradeWizardError &&
              bootstrapErr.status === 404
            ) {
              bootstrapRetired.current = true;
              setBootstrapRetiredState(true);
              setActivated(true);
              markActivationPersisted();
              if (err.status === 503) throw err;
            }
            throw bootstrapErr;
          }
        }
      }
      if (bootstrapRetired.current) {
        throw new UpgradeWizardError('unavailable', 'upgrade_bootstrap_retired', 404);
      }
      try {
        const payload = await bootstrapRead(resource, signal);
        if (resource === 'installation/upgrade/status') {
          recordCommittedStatus(payload);
        }
        return payload;
      } catch (err) {
        if (
          resource === 'installation/upgrade/status' &&
          !signal.aborted &&
          err instanceof UpgradeWizardError &&
          err.status === 404
        ) {
          bootstrapRetired.current = true;
          setBootstrapRetiredState(true);
          markActivationPersisted();
          if (
            snapshot &&
            saved?.stage === 'complete' &&
            saved.routing?.committed &&
            isCanonicalPrefix(saved.routing.controlBasePath)
          ) {
            const targetPrefix = saved.routing.controlBasePath;
            lastActivationPrefix.current = targetPrefix;
            continuationBase.current = `${targetPrefix}/api/v2`;
            setActivated(true);
            try {
              return await proveCanonical(targetPrefix, snapshot, saved, signal);
            } catch (proveErr) {
              if (
                !signal.aborted &&
                proveErr instanceof UpgradeWizardError &&
                proveErr.failure !== 'authentication' &&
                proveErr.failure !== 'response' &&
                (proveErr.failure === 'network' ||
                  proveErr.status === 404 ||
                  proveErr.status === 502 ||
                  proveErr.status === 503 ||
                  proveErr.status === 504)
              ) {
                const fallbackMode =
                  saved.targetMode === 'external' ? 'connect_external' : 'migrate_legacy';
                const fallbackAccess: AccessInfo = saved.accessInfo
                  ? {
                      ...saved.accessInfo,
                      ready: false,
                      code: 'routing_unavailable',
                    }
                  : {
                      mode: fallbackMode,
                      managementBasePath: targetPrefix,
                      inferenceBasePath:
                        fallbackMode === 'connect_external'
                          ? ''
                          : (saved.routing?.inferenceBasePath ?? ''),
                      ...(fallbackMode === 'connect_external' && saved.routing?.externalBaseUrl
                        ? { externalBaseUrl: saved.routing.externalBaseUrl }
                        : {}),
                      protocols: [],
                      ready: false,
                      code: 'routing_unavailable',
                    };
                return markBootstrapRetiredRecovery({
                  ...snapshot,
                  accessInfo: fallbackAccess,
                  execution: {
                    available: false,
                    running: false,
                    managedAvailable: false,
                  },
                });
              }
              throw proveErr;
            }
          }
          setActivated(true);
        }
        throw err;
      }
    };
  }, [markActivationPersisted, recordCommittedStatus]);
  const wizardSaveRouting = useMemo<UpgradeWizardSaveRouting>(() => {
    const bootstrapSaveRouting = createUpgradeWizardSaveRouting(() => credential.current);
    return async (signal, input) => {
      if (canonicalObserved.current || bootstrapRetired.current) {
        throw new UpgradeWizardError('unavailable', 'upgrade_bootstrap_retired', 404);
      }
      return bootstrapSaveRouting(signal, input);
    };
  }, []);
  const wizardVerify = useMemo<UpgradeWizardVerify>(() => {
    const bootstrapVerify = createUpgradeWizardVerify(() => credential.current);
    return async (signal, input) => {
      if (canonicalObserved.current || bootstrapRetired.current) {
        throw new UpgradeWizardError('unavailable', 'upgrade_bootstrap_retired', 404);
      }
      return bootstrapVerify(signal, input);
    };
  }, []);
  const wizardExecute = useMemo<UpgradeWizardExecute>(() => {
    const bootstrapExecute = createUpgradeWizardExecute(() => credential.current);
    return async (signal, input) => {
      if (canonicalObserved.current || bootstrapRetired.current) {
        throw new UpgradeWizardError('unavailable', 'upgrade_bootstrap_retired', 404);
      }
      return bootstrapExecute(signal, input);
    };
  }, []);
  const wizardArtifact = useMemo<ArtifactTransport>(() => {
    const bootstrapArtifact = createUpgradeArtifactTransport(
      () => credential.current,
      () => session.current?.signal
    );
    const guardRetired = () => {
      if (canonicalObserved.current || bootstrapRetired.current) {
        throw { kind: 'http', status: 404, code: 'upgrade_bootstrap_retired' };
      }
    };
    return {
      readStatus: async () => {
        guardRetired();
        return bootstrapArtifact.readStatus();
      },
      probe: async () => {
        guardRetired();
        return bootstrapArtifact.probe();
      },
      acquire: async (input) => {
        guardRetired();
        return bootstrapArtifact.acquire(input);
      },
      upload: async (file) => {
        guardRetired();
        return bootstrapArtifact.upload(file);
      },
    };
  }, []);
  const inJourney = Boolean(revision);
  useEffect(() => {
    // Entering the journey moves focus once. Background checks and appearance
    // changes preserve the user's current focus.
    if (inJourney) heading.current?.focus();
  }, [inJourney]);

  useEffect(
    () => () => {
      ++sequence.current;
      request.current?.abort();
      session.current?.abort();
      session.current = null;
      credential.current = '';
    },
    []
  );

  useEffect(() => {
    if (!revision) return;
    if (activated || continuationBase.current || canonicalObserved.current || bootstrapRetired.current) return;
    let cancelled = false;
    let timer: number | undefined;
    const arm = () => {
      window.clearTimeout(timer);
      if (cancelled || continuationBase.current || canonicalObserved.current || bootstrapRetired.current) return;
      timer = window.setTimeout(() => {
        void runRefresh();
      }, 30_000);
    };
    const runRefresh = async () => {
      window.clearTimeout(timer);
      if (cancelled || continuationBase.current || canonicalObserved.current || bootstrapRetired.current) return;
      if (checkingRef.current) {
        arm();
        return;
      }
      if (!executionActiveRef.current && Date.now() >= expiresAt.current) {
        revoke('expired');
        return;
      }
      await authenticate(credential.current, false, revision);
      if (!cancelled && !continuationBase.current && !canonicalObserved.current && !bootstrapRetired.current) arm();
    };
    arm();
    // BFCache/background returns must never reuse an expired observation.
    const visible = () => {
      if (document.visibilityState === 'visible') void runRefresh();
    };
    document.addEventListener('visibilitychange', visible);
    const hide = () => retireEnvelope();
    window.addEventListener('pagehide', hide);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
      document.removeEventListener('visibilitychange', visible);
      window.removeEventListener('pagehide', hide);
    };
  }, [activated, authenticate, retireEnvelope, revision, revoke]);

  if (!revision) return <FullWebLoginPage adapter={adapter} />;

  return (
    <div className="upgrade-entry" data-testid="upgrade-shell">
      <FullWebUpgradeHeader />
      <main
        className="upgrade-main"
        data-testid={checking ? 'upgrade-revalidating' : 'upgrade-authenticated'}
        aria-busy={checking}
      >
        <div className="upgrade-shell-card">
          <section className="upgrade-topbar" aria-labelledby="upgrade-title">
            <div className="upgrade-card-top">
              <div className="upgrade-heading">
                <h1 id="upgrade-title" ref={heading} tabIndex={-1}>
                  {text('title', language)}
                </h1>
                <span className={checking ? 'badge' : 'badge badge-emerald'}>
                  <FullWebIcon name={checking ? 'refresh' : 'check'} size={13} />
                  {text(checking ? 'checkingShort' : 'verified', language)}
                </span>
              </div>
              <div className="upgrade-session-actions">
                <button
                  className="btn btn-ghost btn-xs"
                  data-testid="upgrade-verify"
                  disabled={checking || executionActive || activated}
                  onClick={() => void authenticate(credential.current, false, revision)}
                >
                  <FullWebIcon name="refresh" size={13} /> {text('verify', language)}
                </button>
                <button className="btn btn-ghost btn-xs" onClick={logout}>
                  {text('logout', language)}
                </button>
              </div>
            </div>
            <div
              className={
                checking || observation === 'observation_unavailable'
                  ? 'upgrade-notice upgrade-entry-banner'
                  : 'upgrade-sr-only'
              }
              role="status"
              aria-live="polite"
              data-testid="upgrade-entry-status"
              data-observation={checking ? 'checking' : observation}
            >
              <FullWebIcon name={checking ? 'refresh' : 'info'} size={16} />
              <div>
                {(!recorded || checking || observation === 'observation_unavailable') && (
                  <h2>
                    {text(
                      checking
                        ? 'checking'
                        : observation === 'observation_unavailable'
                          ? 'observationUnavailable'
                          : 'blocked',
                      language
                    )}
                  </h2>
                )}
                <p>
                  {text(
                    checking
                      ? 'checkingHelp'
                      : observation === 'observation_unavailable'
                        ? recorded
                          ? 'recordedObservationHelp'
                          : 'observationUnavailableHelp'
                        : recorded
                          ? 'recordedBlockedHelp'
                          : 'boundary',
                    language
                  )}
                </p>
              </div>
            </div>
            {!recorded && text('next', language) && (
              <p className="upgrade-sr-only">{text('next', language)}</p>
            )}
          </section>
          <FreshUpgradeFlow
            key={operationSessionKey}
            read={wizardRead}
            saveRouting={wizardSaveRouting}
            verify={wizardVerify}
            execute={wizardExecute}
            artifact={wizardArtifact}
            language={language}
            onRecordedOperation={(value) => { setRecorded(value); expiresAt.current = Date.now() + 60_000; }}
            onExecutionActivity={(active) => {
              executionActiveRef.current = active;
              setExecutionActive(active);
            }}
            canonicalConfirmed={canonicalConfirmed}
            bootstrapRetired={bootstrapRetiredState}
            activationPersisted={activationPersisted}
            onOpenConsole={() => {
              clearCommittedUpgradeStatus();
            }}
            onActivation={(prefix) => {
              if (prefix) {
                if (!isCanonicalPrefix(prefix)) return;
                lastActivationPrefix.current = prefix;
                continuationBase.current = `${prefix}/api/v2`;
                setActivated(true);
                markActivationPersisted(prefix);
                return;
              }
              if (canonicalObserved.current || bootstrapRetired.current) {
                if (lastActivationPrefix.current) {
                  continuationBase.current = `${lastActivationPrefix.current}/api/v2`;
                }
                setActivated(true);
                return;
              }
              lastActivationPrefix.current = '';
              continuationBase.current = '';
              setActivated(false);
              expiresAt.current = Date.now() + 60_000;
              if (committedStatus.current) {
                const updated = rememberCommittedUpgradeStatus(committedStatus.current.snapshot, {
                  ...(committedRevisionRef.current ? { revision: committedRevisionRef.current } : {}),
                  activationRequested: false,
                });
                committedStatus.current = updated;
                setActivationPersisted(false);
              }
            }}
          />
        </div>
      </main>
      <footer className="journey-copyright">{text('footer', language)}</footer>
    </div>
  );
}
