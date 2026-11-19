import { useEffect, useMemo, useRef, useState } from 'react';
import { bootstrapJourneyText as j, type BootstrapJourneyKey } from '../i18n/bootstrapJourney';
import { useFullWebLanguage } from './useFullWebLanguage';
import { shellText as s } from '../i18n/consoleShell';
import { fullWebText as t } from '../i18n/fullweb';
import { keyWorkspaceText as k } from '../i18n/freshKeyWorkspace';
import {
  BootstrapTransportError,
  validateFreshAdminKey,
  type BootstrapTransportClient,
  type FreshOperation,
} from './bootstrapTransport';
import {
  buildPrefixPreviewUrls,
  generateRandomAccessPrefix,
  isSameOrNestedPrefix,
  resolveBrowserOrigin,
  validAccessPrefix,
  validatePrefixPair,
} from './accessPrefix';
import { AccessInfoSummary } from './FreshAccessInfo';
import { PublicApiError, type FullWebCredential, type PublicApiClient } from './publicApi';
import { validRuntimeGeneration, type RuntimeGeneration } from './runtimeGeneration';
import { adoptGuidanceText, adoptReasonText, adoptText } from '../i18n/freshAdopt';
import {
  adoptionCompletionState,
  needsCredentialFileNote,
  normalizeCPAAddress,
  validManagementKey,
  type AdoptionSummary,
  type AdoptRuntimeTarget,
} from './freshAdoption';
import { externalSetupBlockerKey, externalWorkspaceText as x } from '../i18n/freshExternal';
import { FreshAdoptFlow, type AdoptCarrier } from './FreshAdoptFlow';
import { FullWebIcon } from './FullWebIcon';
import { FreshArtifactStep } from './FreshArtifactStep';
import {
  formatArtifactError,
  formatArtifactPinnedMismatch,
  isArtifactPinnedMismatch,
  isArtifactReady,
  isArtifactRecoveryRequired,
  mapArtifactErrorCode,
  readArtifactStatus,
  type ArtifactStatus,
  type MappedArtifactError,
} from './freshArtifact';
import { artifactText } from '../i18n/freshArtifact';

type Strategy = 'new_managed' | 'connect_external' | 'adopt_existing';
type Target = {
  runtimeIdentity: string;
  runtimeGeneration: RuntimeGeneration;
  activeGatewayArtifact: { artifactId: string };
  state: string;
};
function managedTarget(value: unknown): Target {
  const target = value as Target;
  if (
    !target ||
    typeof target.runtimeIdentity !== 'string' ||
    !target.runtimeIdentity.trim() ||
    !validRuntimeGeneration(target.runtimeGeneration) ||
    !/^sha256:[a-f0-9]{64}$/.test(target.activeGatewayArtifact?.artifactId || '')
  )
    throw new Error('Invalid installation target');
  return target;
}
// One address rule for every CPA this wizard talks to: the External form and the
// Adopt flow normalize the same way, and the server normalizes again.
const externalURL = normalizeCPAAddress;
type Pending = {
  revision: number;
  stage: 'admin' | 'strategy' | 'route' | 'complete';
  previous?: string;
};
class WizardArtifactError extends Error {
  constructor(readonly mapped: MappedArtifactError) {
    super(mapped.code);
    this.name = 'WizardArtifactError';
  }
}
function attemptID() {
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('');
}
// A message keeps the key (and its interpolation values) rather than the
// rendered sentence, so switching language re-renders what is already on screen
// in the newly selected language.
type WizardMessage =
  | { from: 'journey'; key: BootstrapJourneyKey }
  | { from: 'shell'; key: Parameters<typeof s>[0] }
  | { from: 'external'; key: Parameters<typeof x>[0] }
  | { from: 'artifact'; mapped: MappedArtifactError };
function wizardMessageText(message: WizardMessage): string {
  switch (message.from) {
    case 'journey':
      return j(message.key);
    case 'shell':
      return s(message.key);
    case 'external':
      return x(message.key);
    case 'artifact': {
      const { messageKey, remedyKey, vars } = message.mapped;
      return `${artifactText(messageKey, vars)} ${artifactText(remedyKey, vars)}`;
    }
  }
}
function prefixCodeToJourneyKey(code?: string | null): BootstrapJourneyKey | null {
  switch (code) {
    case 'invalid_access_prefix':
      return 'prefixErrorInvalid';
    case 'inference_prefix_required':
      return 'prefixErrorInferenceRequired';
    case 'external_inference_prefix_forbidden':
      return 'prefixErrorExternalInferenceForbidden';
    case 'shared_prefix_mismatch':
      return 'prefixErrorSharedMismatch';
    case 'shared_prefix_confirmation_required':
      return 'prefixErrorSharedConfirmRequired';
    case 'prefix_overlap_forbidden':
      return 'prefixErrorOverlapForbidden';
    case 'reserved_prefix_collision':
      return 'prefixErrorReservedCollision';
    case 'routing_conflict':
      return 'prefixErrorRoutingConflict';
    case 'inference_prefix_already_committed':
      return 'prefixErrorAlreadyCommitted';
    default:
      return null;
  }
}
// One message for every failed step. A typed refusal from the Manager names the
// cause it recorded; only when nothing names it does the journey fall back to
// the sentence that covers any unfinished setup.
function setupErrorMessage(err: unknown): WizardMessage {
  if (err instanceof WizardArtifactError) return { from: 'artifact', mapped: err.mapped };
  if (err instanceof BootstrapTransportError) {
    if (err.code === 'managed_destination_verification_required')
      return { from: 'shell', key: 'runtimeVerifyRequired' };
    // The routing layer refuses with this code when the published address did not
    // answer as required; that cause is actionable, unlike a bare 503.
    if (err.code === 'routing_verification_failed')
      return { from: 'shell', key: 'routingVerificationFailed' };
    const prefixKey = prefixCodeToJourneyKey(err.code);
    if (prefixKey) return { from: 'journey', key: prefixKey };
    const blocker = externalSetupBlockerKey(err.code);
    if (blocker) return { from: 'external', key: blocker };
  }
  return { from: 'journey', key: 'setupError' };
}
function confirmed(op: FreshOperation, pending: Pending) {
  if (op.revision <= pending.revision) return false;
  if (pending.stage === 'admin') return op.adminInitialized;
  if (pending.stage === 'route') return op.routing.committed;
  if (pending.stage === 'complete') return op.productComplete === true;
  return (
    !!op.strategy?.stage &&
    (pending.previous === 'staged'
      ? ['starting', 'committed'].includes(op.strategy.stage)
      : pending.previous === 'starting'
        ? op.strategy.stage === 'committed'
        : true)
  );
}

// One product journey in the existing Full Web shell. Configuration stays in
// memory until confirmation; the Fresh operation, not browser storage, owns
// execution and recovery. Console authentication happens only after completion.
export function FreshSetupWizard({
  initialOperation,
  transport,
  onOperation,
}: {
  initialOperation: FreshOperation;
  transport: BootstrapTransportClient;
  onOperation?: (operation: FreshOperation) => void;
}) {
  // Messages on this surface are keys rendered through the Full Web text
  // helpers, so the language switch itself must re-render the wizard: otherwise
  // an error or note that is already on screen keeps the old language.
  useFullWebLanguage();
  const [operation, setOperation] = useState(initialOperation);
  // A resumed installation keeps the strategy it recorded, including an
  // adoption whose flow owns the rest of the journey.
  const [strategy, setStrategy] = useState<Strategy>(
    initialOperation.strategy?.strategy === 'connect_external'
      ? 'connect_external'
      : initialOperation.strategy?.strategy === 'adopt_existing'
        ? 'adopt_existing'
        : 'new_managed'
  );
  const [step, setStep] = useState(initialOperation.strategy?.strategy ? 3 : 0);
  const [key, setKey] = useState('');
  const [visible, setVisible] = useState(false);
  const [path, setPath] = useState(
    () =>
      initialOperation.routing.target ||
      initialOperation.routing.draftControlBasePath ||
      initialOperation.routing.suggestedControlBasePath ||
      generateRandomAccessPrefix()
  );
  const [inferencePath, setInferencePath] = useState(() => {
    if (initialOperation.routing.inferenceTarget) return initialOperation.routing.inferenceTarget;
    if (initialOperation.routing.draftInferenceBasePath) {
      return initialOperation.routing.draftInferenceBasePath;
    }
    const suggested = initialOperation.routing.suggestedInferenceBasePath;
    if (suggested && (!path || !isSameOrNestedPrefix(suggested, path))) {
      return suggested;
    }
    return generateRandomAccessPrefix(path ? [path] : []);
  });
  const [sharedEntry, setSharedEntry] = useState(() =>
    Boolean(
      initialOperation.routing.target
        ? initialOperation.routing.sharedEntry
        : (initialOperation.routing.draftSharedEntry ?? initialOperation.routing.sharedEntry)
    )
  );
  const [accessSessionExpired, setAccessSessionExpired] = useState(false);
  const [baseURL, setBaseURL] = useState('');
  const [cpaKey, setCPAKey] = useState('');
  const [externalKeyVisible, setExternalKeyVisible] = useState(false);
  const [advancedOpen, setAdvancedOpen] = useState(() => Boolean(initialOperation.adminInitialized));
  const [copiedTarget, setCopiedTarget] = useState<string | null>(null);
  const [savedKey, setSavedKey] = useState(false);
  // The plugin opt-in is the operator's explicit decision, so it starts off and
  // is submitted with the operation that owns it. The Supervisor only ever reads
  // this persisted value, so a retry cannot silently turn plugins on.
  const [plugins, setPlugins] = useState(initialOperation.strategy?.pluginsEnabled === true);
  const [target, setTarget] = useState<Target | null>(null);
  const [artifactGate, setArtifactGate] = useState<ArtifactStatus | null>(null);
  const [busy, setBusy] = useState(false);
  // The install-generated Client Key is shown once, on the completion screen of
  // the run that committed the destination. It lives in memory only: never in
  // browser storage, and never in the summary or the URL.
  const [installKey, setInstallKey] = useState('');
  const [installKeyFeedback, setInstallKeyFeedback] = useState<'copied' | 'failed' | ''>('');
  const [error, setError] = useState<WizardMessage | null>(null);
  const [note, setNote] = useState<WizardMessage | null>(null);
  const [unknown, setUnknown] = useState(false);
  // The adoption's own step inside this wizard's four-step chrome: the confirm
  // step is the inventory and the execute step is the migration.
  const [adoptStep, setAdoptStep] = useState(2);
  // The recorded summary of the adoption that just completed. It is the flow's
  // last read of the run, kept so this wizard's own completion step can state
  // what the copy left behind instead of claiming a clean installation.
  const [adoptSummary, setAdoptSummary] = useState<AdoptionSummary | null>(null);
  const inflight = useRef(false);
  const alive = useRef(true);
  const pending = useRef<Pending | null>(null);
  const commitID = useRef(`commit_${attemptID()}`);
  const copyTimer = useRef<number | undefined>(undefined);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
      clearTimeout(copyTimer.current);
    };
  }, []);
  function flashCopied(targetId: string, noteKey: 'copied' | 'urlCopied') {
    clearTimeout(copyTimer.current);
    setCopiedTarget(targetId);
    setNote({ from: 'journey', key: noteKey });
    copyTimer.current = setTimeout(() => {
      if (alive.current) setCopiedTarget(null);
    }, 2000) as unknown as number;
  }
  const accessClient = useMemo<PublicApiClient>(
    () => ({
      get: async <T = unknown>(resource: string, credential?: FullWebCredential) => {
        if (
          resource === 'api-keys/access-info' &&
          operation.routing.target &&
          credential?.kind === 'session'
        ) {
          return (await transport.readAccessInfo(operation.routing.target)) as T;
        }
        throw new PublicApiError('routing_unavailable');
      },
      post: () => Promise.reject(new PublicApiError('routing_unavailable')),
      put: () => Promise.reject(new PublicApiError('routing_unavailable')),
    }),
    [transport, operation.routing.target]
  );


  const effectiveInferencePath =
    strategy === 'connect_external' ? '' : sharedEntry ? path : inferencePath;
  const prefixesLocked = !!operation.routing.target || operation.routing.committed;
  const keyValidation = validateFreshAdminKey(key);
  const keyRules = [
    {
      key: 'keyRuleLength' as const,
      met: key.length > 0 && keyValidation.byteLength >= 24 && keyValidation.byteLength <= 256,
    },
    {
      key: 'keyRuleNoSpaces' as const,
      met: key.length > 0 && !keyValidation.hasForbiddenChar,
    },
    {
      key: 'keyRuleClasses' as const,
      met: key.length > 0 && keyValidation.classCount >= 3,
    },
    {
      key: 'keyRuleDistinct' as const,
      met: key.length > 0 && keyValidation.distinctCount >= 12,
    },
  ];
  const unmetKeyRules = keyRules.filter((rule) => !rule.met).map((rule) => j(rule.key));
  const keyInvalidVisible =
    !operation.adminInitialized &&
    !keyValidation.valid &&
    (key.length > 0 || (error?.from === 'journey' && error.key === 'invalidSetup'));
  const livePrefixValidation = validatePrefixPair({
    controlBasePath: path,
    inferenceBasePath: effectiveInferencePath,
    sharedEntry: strategy === 'connect_external' ? false : sharedEntry,
    requireInference: strategy !== 'connect_external',
  });
  const livePrefixErrorKey = !livePrefixValidation.valid
    ? prefixCodeToJourneyKey(
        livePrefixValidation.controlError ||
          livePrefixValidation.inferenceError ||
          livePrefixValidation.pairError
      )
    : null;
  const advancedSummary =
    strategy === 'connect_external'
      ? `${j('managementPrefix')} · ${path}`
      : strategy === 'new_managed'
        ? `${j(sharedEntry ? 'sharedPrefixBadge' : 'independentPrefixBadge')} · ${j(
            plugins ? 'pluginsOnSummary' : 'pluginsOffSummary'
          )}`
        : j(sharedEntry ? 'sharedPrefixBadge' : 'independentPrefixBadge');

  function accept(next: FreshOperation) {
    if (!alive.current) throw new Error('Installation closed');
    if (next.strategy?.strategy && next.strategy.strategy !== strategy)
      throw new Error('Installation strategy changed');
    if (next.routing.target && next.routing.target !== path)
      throw new Error('Installation route changed');
    if (
      strategy !== 'connect_external' &&
      next.routing.inferenceTarget &&
      next.routing.inferenceTarget !== effectiveInferencePath
    )
      throw new Error('Installation inference route changed');
    setOperation(next);
    if (
      next.strategy?.strategy === 'new_managed' &&
      target &&
      (next.strategy.targetIdentity !== target.runtimeIdentity ||
        next.strategy.targetGeneration !== target.runtimeGeneration ||
        next.strategy.targetArtifactId !== target.activeGatewayArtifact.artifactId)
    )
      throw new Error('Installation target changed');
    onOperation?.(next);
    return next;
  }
  async function read(allowRouteRecovery = false) {
    const next = accept(await transport.fetchOperation());
    // An explicit refresh may unlock the original routing operation after a
    // successful checkpoint read. Activate itself inspects Ingress before any
    // apply, so the next user-initiated resume reconciles that same owner; it
    // neither treats a partial route as success nor automatically replays it.
    const recoverableRoute =
      allowRouteRecovery &&
      pending.current?.stage === 'route' &&
      next.routing.target === path &&
      next.routing.operationId === next.identity.operationId &&
      ['intent_persisted', 'route_activated', 'health_verified'].includes(next.routing.stage || '');
    if (pending.current && !confirmed(next, pending.current) && !recoverableRoute)
      throw new Error('Original write is unconfirmed');
    pending.current = null;
    setUnknown(false);
    if (next.adminInitialized) setKey('');
    return next;
  }
  async function write(
    current: FreshOperation,
    stage: Pending['stage'],
    action: () => Promise<FreshOperation>
  ) {
    pending.current = { revision: current.revision, stage, previous: current.strategy?.stage };
    try {
      const next = accept(await action());
      if (!confirmed(next, pending.current)) throw new Error('Original write is unconfirmed');
      pending.current = null;
      setUnknown(false);
      return next;
    } catch (err) {
      try {
        return await read();
      } catch {
        /* Keep the original uncertain write, never replay it. */
      }
      // A typed strategy refusal is not an unconfirmed write. The adapter
      // refused before it recorded any checkpoint, so the installation stays
      // exactly where it is and the same operation may continue on request.
      if (
        stage === 'strategy' &&
        err instanceof BootstrapTransportError &&
        err.code === 'managed_destination_verification_required'
      ) {
        pending.current = null;
        setUnknown(false);
        throw err;
      }
      if (
        err instanceof BootstrapTransportError &&
        (err.kind === 'bad_request' ||
          (strategy === 'connect_external' && stage === 'strategy' && err.kind === 'conflict'))
      ) {
        pending.current = null;
        setStep(1);
      }
      setUnknown(pending.current !== null);
      throw err;
    }
  }
  async function task(action: () => Promise<void>) {
    if (inflight.current) return;
    inflight.current = true;
    setBusy(true);
    setError(null);
    setNote(null);
    try {
      await action();
    } catch (err) {
      if (alive.current) {
        setError(setupErrorMessage(err));
        setUnknown(pending.current !== null);
      }
    } finally {
      inflight.current = false;
      if (alive.current) setBusy(false);
    }
  }
  async function persistPrefixDraft(nextControl: string, nextInference: string, nextShared: boolean) {
    const check = validatePrefixPair({
      controlBasePath: nextControl,
      inferenceBasePath: strategy === 'connect_external' ? '' : nextShared ? nextControl : nextInference,
      sharedEntry: strategy === 'connect_external' ? false : nextShared,
      requireInference: strategy !== 'connect_external',
    });
    if (!check.valid) {
      const code = check.controlError || check.inferenceError || check.pairError;
      setError({ from: 'journey', key: prefixCodeToJourneyKey(code) || 'invalidSetup' });
      return;
    }
    await task(async () => {
      try {
        const next = accept(
          await transport.saveRoutingDraft(
            nextControl,
            strategy === 'connect_external'
              ? undefined
              : { inferenceTarget: nextShared ? nextControl : nextInference, sharedEntry: nextShared }
          )
        );
        if (next.routing.draftControlBasePath) {
          setPath(next.routing.draftControlBasePath);
          setSharedEntry(Boolean(next.routing.draftSharedEntry));
        }
        if (next.routing.draftInferenceBasePath) setInferencePath(next.routing.draftInferenceBasePath);
        setNote({ from: 'journey', key: 'prefixDraftSaved' });
      } catch (err) {
        if (err instanceof BootstrapTransportError && prefixCodeToJourneyKey(err.code)) throw err;
        throw new BootstrapTransportError('Prefix draft unavailable', 'unavailable');
      }
    });
  }

  async function review() {
    if (!operation.adminInitialized && !validateFreshAdminKey(key).valid) {
      setError({ from: 'journey', key: 'invalidSetup' });
      return;
    }
    const prefixValidation = validatePrefixPair({
      controlBasePath: path,
      inferenceBasePath: effectiveInferencePath,
      sharedEntry: strategy === 'connect_external' ? false : sharedEntry,
      requireInference: strategy !== 'connect_external',
    });
    if (!prefixValidation.valid) {
      const code =
        prefixValidation.controlError || prefixValidation.inferenceError || prefixValidation.pairError;
      setError({ from: 'journey', key: prefixCodeToJourneyKey(code) || 'invalidSetup' });
      return;
    }
    // The External form is refused here, in the operator's words: nothing has
    // been sent yet, so an unusable address or key never becomes the generic
    // "setup did not finish" sentence.
    let address = '';
    if (strategy === 'connect_external') {
      try {
        address = externalURL(baseURL);
      } catch {
        setError({ from: 'external', key: 'setupAddressInvalid' });
        return;
      }
      if (!validManagementKey(cpaKey)) {
        setError({ from: 'external', key: 'setupKeyInvalid' });
        return;
      }
    }
    await task(async () => {
      if (strategy === 'new_managed' || strategy === 'adopt_existing') {
        let artifactStatus: ArtifactStatus;
        try {
          artifactStatus = await readArtifactStatus(transport);
        } catch (err) {
          throw new WizardArtifactError(formatArtifactError(err));
        }
        if (!isArtifactReady(artifactStatus)) {
          setArtifactGate(artifactStatus);
          setSavedKey(false);
          setStep(2);
          if (isArtifactRecoveryRequired(artifactStatus)) {
            setError({
              from: 'artifact',
              mapped: mapArtifactErrorCode('recovery_required', artifactStatus.pinned.version),
            });
          } else if (isArtifactPinnedMismatch(artifactStatus)) {
            setError({
              from: 'artifact',
              mapped: formatArtifactPinnedMismatch(artifactStatus.pinned.version),
            });
          }
          return;
        }
        setArtifactGate(null);
      } else {
        setArtifactGate(null);
      }
      if (strategy === 'adopt_existing') {
        // installation's own facts. The Admin key is committed here because the
        // strategy write that follows is authorized by it, and the console route
        // is prepared just before completion.
        let current = operation;
        if (!operation.adminInitialized) {
          current = await read();
          current = await write(current, 'admin', () =>
            transport.commitAdminKey(commitID.current, key)
          );
          setKey('');
          setVisible(false);
        }
        if (!current.routing.target && !current.routing.committed) {
          accept(
            await transport.saveRoutingDraft(path, {
              inferenceTarget: effectiveInferencePath,
              sharedEntry,
            })
          );
        }
        setStep(2);
        return;
      }
      if (strategy === 'new_managed')
        setTarget(managedTarget(await transport.request('installation/runtime')));
      else setBaseURL(address);
      setSavedKey(false);
      setStep(2);
    });
  }
  async function execute() {
    // An adoption is executed by the shared flow, which owns its two-POST commit
    // and its resumable migration. This path stays the New Managed and External
    // one.
    if (strategy === 'adopt_existing' || artifactGate) return;
    if (pending.current || (!operation.adminInitialized && !savedKey)) return;
    await task(async () => {
      setStep(3);
      let current = await read();
      if (!current.adminInitialized) {
        current = await write(current, 'admin', () =>
          transport.commitAdminKey(commitID.current, key)
        );
        setKey('');
        setVisible(false);
      }
      if (
        !current.routing.target &&
        !current.routing.committed &&
        current.strategy?.stage !== 'committed'
      ) {
        const desiredDraftInference =
          strategy === 'connect_external' ? '' : effectiveInferencePath;
        const desiredDraftShared = strategy === 'connect_external' ? false : sharedEntry;
        const draftMatches =
          current.routing.draftControlBasePath === path &&
          (current.routing.draftInferenceBasePath ?? '') === desiredDraftInference &&
          Boolean(current.routing.draftSharedEntry) === desiredDraftShared;
        if (!draftMatches) {
          try {
            current = accept(
              await transport.saveRoutingDraft(
                path,
                strategy === 'connect_external'
                  ? undefined
                  : { inferenceTarget: effectiveInferencePath, sharedEntry }
              )
            );
          } catch (err) {
            if (
              !current.strategy?.stage &&
              err instanceof BootstrapTransportError &&
              prefixCodeToJourneyKey(err.code) &&
              err.code !== 'routing_conflict'
            ) {
              setStep(1);
            }
            throw err;
          }
        }
      }
      for (let action = 0; action < 3 && current.strategy?.stage !== 'committed'; action++) {
        let body: Record<string, unknown>;
        if (strategy === 'connect_external') {
          body = { strategy, baseUrl: baseURL, managementKey: cpaKey, attemptId: attemptID() };
        } else {
          const checkpoint = current.strategy;
          const observed = checkpoint?.strategy
            ? null
            : target || managedTarget(await transport.request('installation/runtime'));
          body = {
            strategy,
            expectedRuntimeIdentity: checkpoint?.targetIdentity || observed!.runtimeIdentity,
            expectedRuntimeGeneration: checkpoint?.targetGeneration || observed!.runtimeGeneration,
            expectedArtifactId:
              checkpoint?.targetArtifactId || observed!.activeGatewayArtifact.artifactId,
            // Every New Managed attempt carries the same persisted decision, so a
            // retried destination publishes what the operator chose and nothing
            // else. An adoption sends its own opt-in through the shared flow's
            // two-POST body, and an external CPA owns its own plugin posture.
            ...(strategy === 'new_managed' && plugins ? { pluginsEnabled: true } : {}),
          };
        }
        let delivered = '';
        current = await write(current, 'strategy', async () => {
          const executed = await transport.executeStrategy(body);
          delivered = executed.initialClientKey;
          return executed.operation;
        });
        if (delivered) setInstallKey(delivered);
        setCPAKey('');
        if (current.strategy?.stage === 'starting') {
          setNote({ from: 'journey', key: 'waitingRuntime' });
          return;
        }
      }
      if (current.strategy?.stage !== 'committed') throw new Error('CPA not committed');
      if (strategy === 'new_managed') {
        const status = managedTarget(await transport.request('installation/runtime'));
        if (
          status.runtimeIdentity !== current.strategy.targetIdentity ||
          (!/^managed:sha256:[a-f0-9]{64}$/.test(
            current.strategy.destinationConnectionIdentity || ''
          ) &&
            status.runtimeGeneration !== current.strategy.targetGeneration) ||
          status.activeGatewayArtifact.artifactId !== current.strategy.targetArtifactId
        )
          throw new Error('CPA identity changed');
        if (status.state !== 'ready') {
          setNote({ from: 'journey', key: 'waitingRuntime' });
          return;
        }
      }
      if (!current.routing.target) {
        current = accept(
          await transport.prepareRouting(
            path,
            strategy === 'connect_external'
              ? undefined
              : { inferenceTarget: effectiveInferencePath, sharedEntry }
          )
        );
      }
      if (!current.routing.committed) {
        pending.current = { revision: current.revision, stage: 'route' };
        try {
          await transport.activateRouting();
        } catch (err) {
          // A named routing refusal is a recorded answer, not a lost reply. The
          // checkpoint is untouched, so the same operation stays continuable and
          // the journey must not claim the write is unconfirmed.
          if (
            err instanceof BootstrapTransportError &&
            err.code === 'routing_verification_failed'
          ) {
            pending.current = null;
            setUnknown(false);
          }
          throw err;
        }
        current = await read();
      } else {
        // Also install the canonical carrier when resuming a lost activation reply.
        await transport.activateRouting();
      }
      if (!current.productComplete)
        current = await write(current, 'complete', () => transport.complete());
      accept(current);
    });
  }
  const complete = operation.productComplete === true;
  const adoptionManual = adoptSummary?.counts.manual ?? 0;
  const adoptionFailed = adoptSummary?.counts.failed ?? 0;
  // The server completes an installation over manual items, non-retryable
  // failures, and the retryable items of the run the operator accepted, but none
  // of them were copied. The wizard's own success step uses the flow's rule, so
  // it says that instead of borrowing the clean-installation sentence.
  const adoptionPartial =
    strategy === 'adopt_existing' && adoptionCompletionState(adoptSummary, complete) === 'partial';
  const adoptionRemainder = adoptSummary
    ? adoptSummary.items.filter((item) => item.outcome === 'manual' || item.outcome === 'failed')
    : [];
  // The shared Adopt flow's carrier. The bootstrap transport owns identity,
  // revision and the canonical route, so each capability is one call.
  const adoptCarrier: AdoptCarrier = {
    readOperation: () => transport.fetchOperation(),
    observeTarget: async () => {
      const observed = managedTarget(await transport.request('installation/runtime'));
      return {
        runtimeIdentity: observed.runtimeIdentity,
        runtimeGeneration: observed.runtimeGeneration,
        artifactId: observed.activeGatewayArtifact.artifactId,
      } satisfies AdoptRuntimeTarget;
    },
    writeStrategy: async (body, fence) => (await transport.executeStrategy(body, fence)).operation,
    prepareRoute: async () => {
      // The canonical route is committed before completion, exactly as the New
      // Managed path does, so the console the operator enters is the one this
      // installation owns.
      let current = await transport.fetchOperation();
      if (!current.routing.target) {
        current = await transport.prepareRouting(path, {
          inferenceTarget: effectiveInferencePath,
          sharedEntry,
        });
      }
      await transport.activateRouting();
      return transport.fetchOperation();
    },
    complete: (body) => transport.complete(body),
    readAdoption: () => transport.readAdoption(),
  };
  // An adoption is a journey of its own inside this wizard's shell: the shared
  // flow owns the source form, the inventory confirmation, the resumable
  // migration and the completion, so the review and execute steps below never
  // render for it.
  if (strategy === 'adopt_existing' && step >= 2 && !complete && !artifactGate) {
    return (
      <section className="cpamp-installation" data-testid="fresh-setup-wizard" aria-busy={busy}>
        <div className="wizard-card">
          <ol className="journey-steps" aria-label={s('installation')}>
            {(['choose', 'configure', 'confirm', 'execute'] as const).map((label, index) => (
              <li
                key={label}
                className={index < adoptStep ? 'done' : index === adoptStep ? 'active' : ''}
                aria-current={index === adoptStep ? 'step' : undefined}
              >
                <span className="journey-step-dot">
                  {index < adoptStep ? <FullWebIcon name="check" size={22} /> : index + 1}
                </span>
                <span>{s(label)}</span>
              </li>
            ))}
          </ol>
          <FreshAdoptFlow
            embedded
            operation={operation}
            carrier={adoptCarrier}
            onOperation={accept}
            onComplete={(completed) => setAdoptSummary(completed)}
            onStage={(stage) => setAdoptStep(stage === 'migration' ? 3 : 2)}
            onExit={!operation.strategy?.strategy ? () => setStep(1) : undefined}
          />
        </div>
      </section>
    );
  }
  const previewUrls = buildPrefixPreviewUrls(
    resolveBrowserOrigin(),
    path,
    strategy === 'connect_external' ? undefined : effectiveInferencePath,
    strategy === 'connect_external' ? baseURL : undefined
  );
  const summary: Array<{
    label: string;
    value: string;
    mono?: boolean;
    badge?: string;
    testID?: string;
  }> = [
    {
      label: s('choiceTitle'),
      value:
        strategy === 'new_managed'
          ? s('newManaged')
          : strategy === 'adopt_existing'
            ? s('adopt')
            : s('external'),
    },
    {
      label: j('key'),
      value: operation.adminInitialized ? j('keepKey') : j('keyHidden'),
    },
    { label: j('managementPrefix'), value: path, mono: true },
    ...(strategy !== 'connect_external'
      ? [
          { label: j('inferencePrefix'), value: effectiveInferencePath, mono: true },
          {
            label: j('entryModeLabel'),
            value: sharedEntry ? j('sharedPrefixBadge') : j('independentPrefixBadge'),
          },
        ]
      : []),
    ...(strategy === 'connect_external' && baseURL
      ? [
          {
            label: t('externalBaseURL'),
            value: baseURL,
            mono: true,
            badge: j('externalDirectAddressBadge'),
            testID: 'review-external-url',
          },
          {
            label: t('externalManagementKey'),
            value: j('externalKeyReadySummary'),
            testID: 'review-external-key',
          },
        ]
      : []),
    ...(strategy === 'new_managed'
      ? [
          {
            label: j('plugins'),
            value: plugins ? j('enabled') : j('disabled'),
            testID: 'review-plugins',
          },
        ]
      : []),
  ];
  // The engine step is the confirmation step's content whenever the image ships
  // without the CPA: there is no summary to confirm yet, and the only action that
  // moves forward lives inside that step.
  const engineGate = step === 2 && !complete && !!artifactGate;
  return (
    <section className="cpamp-installation" data-testid="fresh-setup-wizard" aria-busy={busy}>
      <div className="wizard-card">
        <ol className="journey-steps" aria-label={s('installation')}>
          {(['choose', 'configure', 'confirm', 'execute'] as const).map((label, index) => (
            <li
              key={label}
              className={complete || index < step ? 'done' : index === step ? 'active' : ''}
              aria-current={!complete && index === step ? 'step' : undefined}
            >
              <span className="journey-step-dot">
                {complete || index < step ? <FullWebIcon name="check" size={22} /> : index + 1}
              </span>
              <span>{s(label)}</span>
            </li>
          ))}
        </ol>
        <div className="journey-body">
          <div className={`journey-heading ${complete ? 'journey-success-heading' : ''}`}>
            {complete && (
              <div className="journey-success-symbol">
                <FullWebIcon name="check" size={28} />
              </div>
            )}
            <div>
              <h1 className="wizard-title">
                {complete
                  ? adoptionPartial
                    ? adoptText('partialTitle')
                    : s('ready')
                  : step === 0
                    ? j('welcome')
                    : step === 1
                      ? j('configureHeading')
                      : step === 2
                        ? engineGate
                          ? s('engineGateTitle')
                          : s('reviewTitle')
                        : note
                          ? j('waitingRuntime')
                          : s(busy ? 'installing' : unknown ? 'unknown' : 'failure')}
              </h1>
              <p className="wizard-desc">
                {complete
                  ? adoptionPartial
                    ? adoptText('partialHelp', {
                        manual: adoptionManual,
                        failed: adoptionFailed,
                      })
                    : j('successHelp')
                  : step === 0
                    ? j('chooseHelp')
                    : step === 1
                    ? j(
                        strategy === 'adopt_existing'
                          ? 'draftHelpAdopt'
                          : strategy === 'connect_external'
                            ? 'draftHelpExternal'
                            : 'draftHelp'
                      )
                      : step === 2
                        ? engineGate
                          ? s('engineGateHelp')
                          : s('reviewHelp')
                        : j('progressHelp')}
              </p>
            </div>
          </div>
          {error && (
            <p role="alert" className="alert alert-warn">
              {wizardMessageText(error)}
            </p>
          )}
          {note &&
            (step === 1 &&
            note.from === 'journey' &&
            (note.key === 'copied' || note.key === 'urlCopied') ? (
              <span role="status" className="sr-only">
                {wizardMessageText(note)}
              </span>
            ) : (
              <p role="status" className="alert alert-info">
                {wizardMessageText(note)}
              </p>
            ))}
          {unknown && (
            <p role="status" className="alert alert-warn">
              {s('unknownHelp')}
            </p>
          )}
          {step === 0 && (
            <div>
              {(['new_managed', 'adopt_existing', 'connect_external'] as const).map((value) => (
                <button
                  type="button"
                  key={value}
                  data-testid={`choose-${value}`}
                  className={`journey-choice ${strategy === value ? 'selected' : ''}`}
                  aria-pressed={strategy === value}
                  disabled={!!operation.strategy?.strategy}
                  onClick={() => {
                    if (
                      strategy === 'connect_external' &&
                      value !== 'connect_external' &&
                      !sharedEntry &&
                      (!validAccessPrefix(inferencePath) ||
                        (path && isSameOrNestedPrefix(inferencePath, path)))
                    ) {
                      setInferencePath(generateRandomAccessPrefix(path ? [path] : []));
                    }
                    setStrategy(value);
                    setTarget(null);
                    setArtifactGate(null);
                  }}
                >
                  <span className="journey-radio" aria-hidden="true" />
                  <span className="journey-tile-icon">
                    <FullWebIcon
                      name={
                        value === 'new_managed'
                          ? 'server'
                          : value === 'adopt_existing'
                            ? 'copy'
                            : 'link'
                      }
                      size={30}
                    />
                  </span>
                  <span>
                    <h2>
                      {s(
                        value === 'new_managed'
                          ? 'newManaged'
                          : value === 'adopt_existing'
                            ? 'adopt'
                            : 'external'
                      )}
                      {value === 'new_managed' && (
                        <span className="badge badge-indigo">{j('recommended')}</span>
                      )}
                    </h2>
                    <p>
                      {s(
                        value === 'new_managed'
                          ? 'newDetail'
                          : value === 'adopt_existing'
                            ? 'adoptHelp'
                            : 'externalHelp'
                      )}
                    </p>
                  </span>
                </button>
              ))}
            </div>
          )}
          {step === 1 && (
            <div className="journey-configuration">
              {operation.adminInitialized ? (
                <p className="journey-key-strip">
                  <FullWebIcon name="check" size={18} />
                  <span>
                    <strong>{j('key')}</strong> · {j('keepKey')}
                  </span>
                </p>
              ) : (
                <div className="journey-field">
                  <div className="form-label">
                    <label htmlFor="fresh-admin-key">{j('key')}</label>
                    <button
                      type="button"
                      className="btn btn-secondary btn-xs"
                      disabled={busy}
                      data-testid="generate-key-btn"
                      onClick={() =>
                        void task(async () => {
                          setKey(await transport.generateAdminKey());
                          setVisible(true);
                          setSavedKey(false);
                          setError(null);
                        })
                      }
                    >
                      {j('generate')}
                    </button>
                  </div>
                  <div className={`input-wrap ${keyInvalidVisible ? 'input-wrap-invalid' : ''}`}>
                    <input
                      id="fresh-admin-key"
                      data-testid="admin-key-input"
                      className={`form-input ${visible ? 'form-input-mono' : ''}`}
                      type={visible ? 'text' : 'password'}
                      value={key}
                      placeholder={j('keyPlaceholder')}
                      autoComplete="off"
                      spellCheck={false}
                      aria-describedby="fresh-admin-key-rules"
                      aria-invalid={keyInvalidVisible || undefined}
                      onChange={(e) => {
                        setKey(e.target.value);
                        setSavedKey(false);
                        setCopiedTarget(null);
                        setError(null);
                      }}
                    />
                    <div className="input-actions input-actions-divided">
                      <button
                        type="button"
                        className="btn btn-ghost btn-xs"
                        aria-pressed={visible}
                        onClick={() => setVisible(!visible)}
                      >
                        <FullWebIcon name={visible ? 'eyeOff' : 'eye'} size={14} />
                        {j(visible ? 'hide' : 'show')}
                      </button>
                      <button
                        type="button"
                        className={`btn btn-ghost btn-xs ${
                          copiedTarget === 'admin-key' ? 'is-copied' : ''
                        }`}
                        disabled={!key}
                        onClick={() =>
                          void task(async () => {
                            try {
                              await navigator.clipboard.writeText(key);
                              flashCopied('admin-key', 'copied');
                            } catch {
                              setVisible(true);
                              setError({ from: 'journey', key: 'copyFailed' });
                            }
                          })
                        }
                      >
                        <FullWebIcon
                          name={copiedTarget === 'admin-key' ? 'check' : 'copy'}
                          size={14}
                        />
                        {j('copy')}
                      </button>
                    </div>
                  </div>
                  {keyValidation.valid ? (
                    <p id="fresh-admin-key-rules" className="journey-help journey-help-ok">
                      <FullWebIcon name="check" size={13} />
                      <span>{j('keyReadyHint')}</span>
                    </p>
                  ) : keyInvalidVisible ? (
                    <p id="fresh-admin-key-rules" className="journey-help journey-help-warn">
                      {j('keyNeedsPrefix')}
                      {unmetKeyRules.join(' · ')}
                    </p>
                  ) : (
                    <p id="fresh-admin-key-rules" className="journey-help">
                      {j('keyRules')}
                    </p>
                  )}
                </div>
              )}

              {strategy === 'connect_external' && (
                <div
                  className="journey-external-fields journey-external-card"
                  data-testid="external-connection-card"
                >
                  <div className="journey-external-head">
                    <h3 className="journey-external-title">
                      <FullWebIcon name="link" size={16} />
                      <span>{j('externalSectionTitle')}</span>
                    </h3>
                    <span className="badge badge-neutral">{j('externalSectionBadge')}</span>
                  </div>
                  <div className="journey-field">
                    <label className="form-label" htmlFor="setup-cpa-url">
                      {t('externalBaseURL')}
                    </label>
                    <input
                      id="setup-cpa-url"
                      data-testid="external-base-url"
                      className="form-input form-input-mono"
                      autoComplete="off"
                      spellCheck={false}
                      placeholder={j('externalUrlPlaceholder')}
                      value={baseURL}
                      aria-describedby="setup-external-help"
                      onChange={(e) => {
                        setBaseURL(e.target.value);
                        setError(null);
                      }}
                    />
                  </div>
                  <div className="journey-field">
                    <label className="form-label" htmlFor="setup-cpa-key">
                      {t('externalManagementKey')}
                    </label>
                    <div className="input-wrap">
                      <input
                        id="setup-cpa-key"
                        data-testid="external-management-key"
                        className={`form-input ${externalKeyVisible ? 'form-input-mono' : ''}`}
                        type={externalKeyVisible ? 'text' : 'password'}
                        autoComplete="off"
                        spellCheck={false}
                        placeholder={j('externalKeyPlaceholder')}
                        value={cpaKey}
                        aria-describedby="setup-external-help"
                        onChange={(e) => {
                          setCPAKey(e.target.value);
                          setError(null);
                        }}
                      />
                      <div className="input-actions input-actions-divided">
                        <button
                          type="button"
                          className="btn btn-ghost btn-xs"
                          aria-pressed={externalKeyVisible}
                          onClick={() => setExternalKeyVisible((prev) => !prev)}
                        >
                          <FullWebIcon name={externalKeyVisible ? 'eyeOff' : 'eye'} size={14} />
                          {j(externalKeyVisible ? 'hide' : 'show')}
                        </button>
                      </div>
                    </div>
                  </div>
                  <p id="setup-external-help" className="journey-help">
                    {t('externalHelp')}
                  </p>
                </div>
              )}

              <div className="info-card prefix-preview-card" data-testid="prefix-url-preview">
                {[
                  {
                    id: 'console',
                    label: j('previewConsoleUrl'),
                    urlValue: previewUrls.consoleUrl,
                    primary: true,
                  },
                  strategy !== 'connect_external'
                    ? {
                        id: 'inference',
                        label: j('previewInferenceUrl'),
                        urlValue: previewUrls.inferenceUrl,
                        primary: false,
                      }
                    : {
                        id: 'external',
                        label: j('previewExternalInferenceUrl'),
                        urlValue: previewUrls.externalUrl,
                        primary: false,
                      },
                ].map(({ id, label, urlValue, primary }) => (
                  <div key={id} className={`info-row ${primary ? 'info-row-primary' : ''}`}>
                    <span className="info-key">{label}</span>
                    <span className="info-val-group">
                      {urlValue === '—' ? (
                        <span className="info-val-placeholder">{j('externalUrlPendingHint')}</span>
                      ) : (
                        <code className="info-val">{urlValue}</code>
                      )}
                      {urlValue !== '—' && (
                        <button
                          type="button"
                          className={`btn btn-ghost btn-xs btn-icon-only ${
                            copiedTarget === id ? 'is-copied' : ''
                          }`}
                          aria-label={`${j('copy')}: ${label}`}
                          title={j('copy')}
                          onClick={() =>
                            void task(async () => {
                              try {
                                await navigator.clipboard.writeText(urlValue);
                                flashCopied(id, 'urlCopied');
                              } catch {
                                setError({ from: 'journey', key: 'copyFailed' });
                              }
                            })
                          }
                        >
                          <FullWebIcon name={copiedTarget === id ? 'check' : 'copy'} size={14} />
                        </button>
                      )}
                    </span>
                  </div>
                ))}
              </div>

              <details
                className="journey-advanced"
                open={advancedOpen || Boolean(livePrefixErrorKey)}
                data-testid="step2-advanced-options"
              >
                <summary
                  className="journey-advanced-summary"
                  onClick={(e) => {
                    e.preventDefault();
                    setAdvancedOpen((prev) => !prev);
                  }}
                >
                  <span className="journey-advanced-title">
                    <FullWebIcon
                      name="chevronRight"
                      size={15}
                      className="journey-advanced-chevron"
                    />
                    {j('advancedOptions')}
                  </span>
                  <span className="journey-advanced-meta">{advancedSummary}</span>
                </summary>

                <div className="journey-advanced-body">
                  {prefixesLocked && (
                    <p className="journey-lock-note">{j('prefixesLockedNote')}</p>
                  )}

                  <div className="journey-field">
                    <label className="form-label" htmlFor="setup-control-path">
                      {j('managementPrefix')}
                    </label>
                    <div
                      className={`input-wrap ${
                        livePrefixValidation.controlError ? 'input-wrap-invalid' : ''
                      }`}
                    >
                      <input
                        id="setup-control-path"
                        data-testid="route-target-input"
                        className="form-input form-input-mono"
                        value={path}
                        disabled={prefixesLocked}
                        autoComplete="off"
                        spellCheck={false}
                        aria-describedby="setup-control-path-help"
                        aria-invalid={Boolean(livePrefixValidation.controlError) || undefined}
                        onChange={(e) => {
                          setPath(e.target.value);
                          setError(null);
                          setNote((prev) =>
                            prev?.from === 'journey' && prev.key === 'prefixDraftSaved'
                              ? null
                              : prev
                          );
                        }}
                      />
                      <div className="input-actions">
                        <button
                          type="button"
                          className="btn btn-secondary btn-xs"
                          data-testid="randomize-control-prefix"
                          disabled={busy || prefixesLocked}
                          onClick={() => {
                            const avoid =
                              strategy !== 'connect_external' && !sharedEntry
                                ? [path, inferencePath]
                                : [path];
                            const nextControl = generateRandomAccessPrefix(avoid);
                            setPath(nextControl);
                            setError(null);
                            setNote((prev) =>
                              prev?.from === 'journey' && prev.key === 'prefixDraftSaved'
                                ? null
                                : prev
                            );
                            if (operation.adminInitialized) {
                              void persistPrefixDraft(
                                nextControl,
                                sharedEntry ? nextControl : inferencePath,
                                sharedEntry
                              );
                            }
                          }}
                        >
                          <FullWebIcon name="refresh" size={13} />
                          {j('randomizePrefix')}
                        </button>
                      </div>
                    </div>
                    <p id="setup-control-path-help" className="journey-help">
                      {j('managementPrefixHelp')}
                    </p>
                  </div>

                  {strategy !== 'connect_external' ? (
                    <>
                      <div className="journey-field">
                        <label className="form-label" htmlFor="setup-inference-path">
                          {j('inferencePrefix')}
                        </label>
                        <div
                          className={`input-wrap ${
                            livePrefixValidation.inferenceError || livePrefixValidation.pairError
                              ? 'input-wrap-invalid'
                              : ''
                          }`}
                        >
                          <input
                            id="setup-inference-path"
                            data-testid="inference-target-input"
                            className="form-input form-input-mono"
                            value={sharedEntry ? path : inferencePath}
                            disabled={prefixesLocked || sharedEntry}
                            autoComplete="off"
                            spellCheck={false}
                            aria-describedby="setup-inference-path-help"
                            aria-invalid={
                              Boolean(
                                livePrefixValidation.inferenceError ||
                                  livePrefixValidation.pairError
                              ) || undefined
                            }
                            onChange={(e) => {
                              setInferencePath(e.target.value);
                              setError(null);
                              setNote((prev) =>
                                prev?.from === 'journey' && prev.key === 'prefixDraftSaved'
                                  ? null
                                  : prev
                              );
                            }}
                          />
                          <div className="input-actions">
                            <button
                              type="button"
                              className="btn btn-secondary btn-xs"
                              data-testid="randomize-inference-prefix"
                              disabled={busy || prefixesLocked || sharedEntry}
                              onClick={() => {
                                const nextInference = generateRandomAccessPrefix([
                                  path,
                                  inferencePath,
                                ]);
                                setInferencePath(nextInference);
                                setError(null);
                                setNote((prev) =>
                                  prev?.from === 'journey' && prev.key === 'prefixDraftSaved'
                                    ? null
                                    : prev
                                );
                                if (operation.adminInitialized) {
                                  void persistPrefixDraft(path, nextInference, false);
                                }
                              }}
                            >
                              <FullWebIcon name="refresh" size={13} />
                              {j('randomizePrefix')}
                            </button>
                          </div>
                        </div>
                        <p id="setup-inference-path-help" className="journey-help">
                          {j('inferencePrefixHelp')}
                        </p>
                      </div>

                      <div className="journey-check-block">
                        <label className="journey-checkline" htmlFor="setup-shared-prefix">
                          <span className="journey-checkbox-control">
                            <input
                              id="setup-shared-prefix"
                              data-testid="shared-prefix-checkbox"
                              type="checkbox"
                              checked={sharedEntry}
                              disabled={busy || prefixesLocked}
                              aria-describedby="setup-shared-prefix-help"
                              onChange={(e) => {
                                const checked = e.target.checked;
                                setSharedEntry(checked);
                                if (
                                  !checked &&
                                  (!validAccessPrefix(inferencePath) ||
                                    (path && isSameOrNestedPrefix(inferencePath, path)))
                                ) {
                                  setInferencePath(generateRandomAccessPrefix(path ? [path] : []));
                                }
                                setError(null);
                                setNote((prev) =>
                                  prev?.from === 'journey' && prev.key === 'prefixDraftSaved'
                                    ? null
                                    : prev
                                );
                              }}
                            />
                            <span className="journey-checkbox-box" aria-hidden="true">
                              <FullWebIcon name="check" size={13} />
                            </span>
                          </span>
                          <span className="journey-check-title">{j('sharedPrefixToggle')}</span>
                        </label>
                        <p id="setup-shared-prefix-help" className="journey-help journey-check-help">
                          {j('sharedPrefixHelp')}
                        </p>
                      </div>
                    </>
                  ) : (
                    <p className="journey-help" data-testid="external-inference-note">
                      {j('externalInferenceNote')}
                    </p>
                  )}

                  {livePrefixErrorKey && (
                    <p className="journey-inline-error" aria-live="polite">
                      {j(livePrefixErrorKey)}
                    </p>
                  )}

                  {strategy === 'new_managed' && (
                    <div
                      className={`journey-check-block ${
                        plugins ? 'journey-check-block-warn' : ''
                      }`}
                    >
                      <label className="journey-checkline" htmlFor="setup-plugin-optin">
                        <span className="journey-checkbox-control">
                          <input
                            id="setup-plugin-optin"
                            data-testid="plugin-optin-checkbox"
                            type="checkbox"
                            checked={plugins}
                            disabled={!!operation.strategy?.strategy}
                            aria-describedby="setup-plugin-optin-help"
                            onChange={(e) => setPlugins(e.target.checked)}
                          />
                          <span className="journey-checkbox-box" aria-hidden="true">
                            <FullWebIcon name="check" size={13} />
                          </span>
                        </span>
                        <span className="journey-check-title">{j('pluginsOptIn')}</span>
                      </label>
                      <p id="setup-plugin-optin-help" className="journey-help journey-check-help">
                        {j('pluginsOptInHelp')}
                      </p>
                    </div>
                  )}

                  {operation.adminInitialized && !prefixesLocked && (
                    <div className="journey-section-actions">
                      <button
                        type="button"
                        className="btn btn-secondary btn-sm"
                        data-testid="save-prefix-draft"
                        disabled={busy}
                        onClick={() =>
                          void persistPrefixDraft(path, effectiveInferencePath, sharedEntry)
                        }
                      >
                        {j('savePrefixDraft')}
                      </button>
                    </div>
                  )}
                </div>
              </details>
            </div>
          )}
          {step === 2 && !complete && artifactGate && (
            <FreshArtifactStep
              hideHeader
              transport={transport}
              initialStatus={artifactGate}
              leadingAction={
                <button
                  type="button"
                  className="btn btn-secondary"
                  disabled={busy}
                  onClick={() => {
                    setStep(1);
                    setSavedKey(false);
                    setArtifactGate(null);
                    setError(null);
                  }}
                >
                  <FullWebIcon name="arrowLeft" size={18} />
                  {s('back')}
                </button>
              }
              onReady={() => {
                void review();
              }}
            />
          )}
          {step === 2 && !artifactGate && !complete && (
            <>
              <dl className="journey-summary">
                {summary.map(({ label, value, mono, badge, testID }) => (
                  <div
                    className="journey-summary-row"
                    key={label}
                    {...(testID ? { 'data-testid': testID } : {})}
                  >
                    <FullWebIcon name="check" size={16} />
                    <dt>{label}</dt>
                    <dd className={mono ? 'journey-summary-mono' : undefined}>
                      <span>{value}</span>
                      {badge && <span className="badge badge-neutral journey-summary-badge">{badge}</span>}
                    </dd>
                  </div>
                ))}
              </dl>
              <p className="alert alert-info">
                {strategy === 'connect_external'
                  ? x('setupExternalNoMigration')
                  : j('noMigration')}
              </p>
              <label
                className="journey-checkline journey-confirm-checkline"
                htmlFor="confirm-key-saved"
              >
                <span className="journey-checkbox-control">
                  <input
                    id="confirm-key-saved"
                    type="checkbox"
                    data-testid="confirm-key-saved-checkbox"
                    checked={savedKey}
                    onChange={(e) => setSavedKey(e.target.checked)}
                  />
                  <span className="journey-checkbox-box" aria-hidden="true">
                    <FullWebIcon name="check" size={13} />
                  </span>
                </span>
                <span>
                  {operation.adminInitialized ? s('acknowledgement') : j('savedConfirm')}
                </span>
              </label>
            </>
          )}
          {complete && (
            <div className="journey-complete-stack">
              {adoptionPartial && (
                <section
                  className="journey-field adopt-remaining-card"
                  data-testid="adopt-remaining-work"
                >
                  <h2 className="form-label">{adoptText('details')}</h2>
                  {needsCredentialFileNote(adoptionRemainder) && (
                    // The same sentence as the flow's group: the credentials
                    // below are record ordinals, so the block says once what the
                    // number is and how to compare the two auth directories.
                    <p className="journey-help" data-testid="adopt-credential-note">
                      {adoptText('credentialNote')}
                    </p>
                  )}
                  <ul className="adopt-items">
                    {adoptionRemainder.map((item) => (
                      <li key={item.id} data-testid={`adopt-remaining-${item.id}`}>
                        <strong className="adopt-item-name">{item.item}</strong>
                        {item.reason && (
                          <span className="adopt-item-reason">{adoptReasonText(item.reason)}</span>
                        )}
                        <p className="journey-help">
                          {adoptGuidanceText(item.kind, item.reason, item.left)}
                        </p>
                      </li>
                    ))}
                  </ul>
                </section>
              )}
              {installKey && (
                <section
                  className="journey-key"
                  data-testid="install-client-key"
                  aria-label={k('installKeyTitle')}
                >
                  <h2>{k('installKeyTitle')}</h2>
                  <p>{k('installKeyHelp')}</p>
                  <code data-testid="install-client-key-value">{installKey}</code>
                  <div className="header-actions">
                    <button
                      type="button"
                      className="btn btn-secondary"
                      data-testid="copy-install-client-key"
                      onClick={() =>
                        void task(async () => {
                          try {
                            await navigator.clipboard.writeText(installKey);
                            setInstallKeyFeedback('copied');
                          } catch {
                            setInstallKeyFeedback('failed');
                          }
                        })
                      }
                    >
                      <FullWebIcon name="copy" />
                      {k('copyKey')}
                    </button>
                    <button
                      type="button"
                      className="btn btn-ghost"
                      data-testid="dismiss-install-client-key"
                      onClick={() => {
                        setInstallKey('');
                        setInstallKeyFeedback('');
                      }}
                    >
                      {k('installKeyDismiss')}
                    </button>
                  </div>
                  {installKeyFeedback && (
                    <p role="status" data-testid="install-client-key-feedback">
                      {k(
                        installKeyFeedback === 'copied'
                          ? 'installKeyCopied'
                          : 'installKeyCopyFailed'
                      )}
                    </p>
                  )}
                  <p className="journey-quiet">{k('installKeyLater')}</p>
                </section>
              )}
              <AccessInfoSummary
                client={accessClient}
                auth={{ kind: 'session', value: transport.sessionToken() }}
                onUnauthorized={() => setAccessSessionExpired(true)}
                defaultProtocolsCollapsed
                completionRecovery
                subtitleNote={j('bookmark')}
              />
              {strategy === 'connect_external' && (
                <div
                  className="journey-external-next-hint"
                  data-testid="external-complete-next-hint"
                >
                  <FullWebIcon name="info" size={16} />
                  <span>{j('externalCompleteNextHint')}</span>
                </div>
              )}
              {accessSessionExpired && (
                <>
                  <p className="journey-help">{j('bookmark')}</p>
                  <p className="alert alert-info" role="alert" data-testid="access-session-expired">
                    <strong>{j('accessSessionExpired')}:</strong> {j('accessSessionExpiredHelp')}
                  </p>
                </>
              )}
            </div>
          )}
          {step === 3 && !complete && (
            <ol className="journey-stages journey-exec-stages" aria-live="polite">
              {[
                [j('administrator'), operation.adminInitialized],
                [
                  strategy === 'new_managed' ? s('newManaged') : s('external'),
                  operation.strategy?.stage === 'committed',
                ],
                [j('routeTitle'), operation.routing.committed],
                [s('complete'), complete],
              ].map(([label, done]) => (
                <li key={String(label)} className={done ? 'done' : ''}>
                  <span
                    className={`adopt-stage-mark ${
                      done ? 'adopt-stage-mark-emerald' : 'adopt-stage-mark-neutral'
                    }`}
                  >
                    <FullWebIcon name={done ? 'check' : 'clock'} size={15} />
                  </span>
                  <strong>{label}</strong>
                  <small>{s(done ? 'done' : 'pending')}</small>
                </li>
              ))}
            </ol>
          )}
        </div>
        <footer className={engineGate ? 'journey-sr-only' : 'journey-footer'}>
          {complete ? (
            <a
              className="btn btn-primary"
              data-testid="enter-console"
              href={operation.routing.target}
              onClick={() => {
                setInstallKey('');
                transport.clear();
              }}
            >
              {j('finishLogin')}
              <FullWebIcon name="arrowRight" size={20} />
            </a>
          ) : (
            <>
              {step < 3 && (
                <button
                  type="button"
                  className="btn btn-secondary"
                  disabled={busy || step === 0}
                  onClick={() => {
                    setStep(step - 1);
                    setSavedKey(false);
                    setArtifactGate(null);
                    setError(null);
                  }}
                >
                  <FullWebIcon name="arrowLeft" size={18} />
                  {s('back')}
                </button>
              )}
              {step === 0 && (
                <button
                  type="button"
                  className="btn btn-primary"
                  data-testid="setup-next"
                  onClick={() => setStep(1)}
                >
                  {s('next')}
                  <FullWebIcon name="arrowRight" size={18} />
                </button>
              )}
              {step === 1 && (
                <button
                  type="button"
                  className="btn btn-primary"
                  data-testid="setup-review"
                  disabled={busy}
                  onClick={() => void review()}
                >
                  {s('next')}
                  <FullWebIcon name="arrowRight" size={18} />
                </button>
              )}
              {step === 2 && !engineGate && (
                <button
                  type="button"
                  className="btn btn-primary"
                  data-testid="setup-execute"
                  disabled={busy || !savedKey}
                  onClick={() => void execute()}
                >
                  {s('submit')}
                  <FullWebIcon name="arrowRight" size={18} />
                </button>
              )}
              {engineGate && (
                <p className="journey-quiet" role="status" data-testid="engine-gate-next">
                  {s('engineGateNext')}
                </p>
              )}
              {step === 3 && (
                <>
                  <button
                    type="button"
                    className="btn btn-secondary"
                    data-testid="setup-refresh"
                    disabled={busy}
                    onClick={() =>
                      void task(async () => {
                        await read(true);
                      })
                    }
                  >
                    {s('refresh')}
                  </button>
                  <button
                    type="button"
                    className="btn btn-primary"
                    data-testid="setup-resume"
                    disabled={busy || unknown}
                    onClick={() => void execute()}
                  >
                    {s('recover')}
                  </button>
                </>
              )}
            </>
          )}
        </footer>
      </div>
    </section>
  );
}
