// Canonical Client Keys have three origins. The workspace creates its own; the
// default installation mints exactly one so CPA never starts with client
// authentication disabled; and an adopted installation registers the keys the
// CPA it took over already carried. All three are the same canonical record with
// the same masked-only projection — only the secret's shape, and therefore the
// mask it allows, differs.
export type KeyOrigin = 'workspace' | 'installer' | 'adopted';

export type KeyRuntimeState =
  | 'not_applied'
  | 'applying'
  | 'effective_observed'
  | 'effect_unknown'
  | 'revoked_observed';

export type KeyLifecycle = 'active' | 'missing' | 'superseded';

export interface Key {
  id: string;
  requestId: string;
  maskedPreview: string;
  createdAtMs: number;
  managerState: 'persisted';
  runtimeState: KeyRuntimeState;
  name: string;
  nameRevision: number;
  lifecycle?: KeyLifecycle;
  origin: KeyOrigin;
  operationId?: string;
  effectCode?: string;
}

const workspaceSecret = /^cpamp_sk_[a-f0-9]{64}$/;
const installSecret = /^sk-[A-Za-z0-9]{64}$/;
// An adopted key carries the source CPA's own shape: the mask is the ellipsis and,
// from sixteen characters up, the last four characters — a quarter of the secret
// at most — and the ellipsis alone below that. Characters are code points and the
// secret is any non-control token of at most 4096 UTF-8 bytes, as the server
// counts them, so a non-ASCII or astral source key renders and copies the same way.
const adoptedMask = /^…(?:[\s\S]{4})?$/u;
const adoptedSecretMaxBytes = 4096;

// adoptedToken is the server's `validExternalKey` in the same terms: any code
// point above the space, except delete, and at most 4096 UTF-8 bytes. It is a
// predicate rather than a character class because a control range written as a
// regular expression reads as one literal control character to a linter.
function adoptedToken(secret: string): boolean {
  for (const character of secret) {
    const code = character.codePointAt(0)!;
    if (code < 0x21 || code === 0x7f) return false;
  }
  return secret !== '' && new TextEncoder().encode(secret).length <= adoptedSecretMaxBytes;
}

/** maskedPreviewOf is the only rendering a saved secret may produce. */
export function maskedPreviewOf(secret: string): string {
  if (workspaceSecret.test(secret)) return 'cpamp_sk_…' + secret.slice(-4);
  if (installSecret.test(secret)) return 'sk-…' + secret.slice(-4);
  return '';
}

/**
 * maskedPreviewFor is the mask one origin's secret must reproduce. It is the
 * server's rule, not a second policy: the two canonical origins keep their
 * shape-owning prefix, and an adopted secret — the source CPA's own printable
 * token — keeps the last four characters only from sixteen characters up.
 */
export function maskedPreviewFor(origin: KeyOrigin, secret: string): string {
  if (origin === 'workspace') return workspaceSecret.test(secret) ? 'cpamp_sk_…' + secret.slice(-4) : '';
  if (origin === 'installer') return installSecret.test(secret) ? 'sk-…' + secret.slice(-4) : '';
  if (!adoptedToken(secret)) return '';
  const characters = Array.from(secret);
  return characters.length >= 16 ? '…' + characters.slice(-4).join('') : '…';
}

/**
 * validSavedSecret verifies a secret the detail page just received against the
 * record that produced it. Copying is the same authenticated, no-store read for
 * every origin; only the shape check differs, and it is the stored preview that
 * decides whether the response is the key this page is showing.
 */
export function validSavedSecret(key: Key, secret: string): boolean {
  if (typeof secret !== 'string') return false;
  const mask = maskedPreviewFor(key.origin, secret);
  return mask !== '' && mask === key.maskedPreview;
}

/** validMask accepts the mask the origin's secret shape can produce. */
function validMask(origin: KeyOrigin, maskedPreview: string): boolean {
  if (typeof maskedPreview !== 'string') return false;
  if (origin === 'workspace') return /^cpamp_sk_…[a-f0-9]{4}$/.test(maskedPreview);
  if (origin === 'installer') return /^sk-…[A-Za-z0-9]{4}$/.test(maskedPreview);
  return adoptedMask.test(maskedPreview);
}

export function validKey(value: Key): boolean {
  return (
    !!value &&
    /^[a-f0-9]{32}$/.test(value.id) &&
    /^[a-f0-9]{32}$/.test(value.requestId) &&
    (value.origin === 'workspace' || value.origin === 'installer' || value.origin === 'adopted') &&
    validMask(value.origin, value.maskedPreview) &&
    Number.isSafeInteger(value.createdAtMs) &&
    value.createdAtMs > 0 &&
    value.managerState === 'persisted' &&
    ['not_applied', 'applying', 'effective_observed', 'effect_unknown', 'revoked_observed'].includes(
      value.runtimeState
    ) &&
    typeof value.name === 'string' &&
    [...value.name].length <= 120 &&
    Number.isSafeInteger(value.nameRevision) &&
    value.nameRevision > 0 &&
    (value.lifecycle === undefined || ['active', 'missing', 'superseded'].includes(value.lifecycle)) &&
    (value.operationId === undefined ||
      value.operationId === '' ||
      /^[a-f0-9]{32}$/.test(value.operationId)) &&
    (value.effectCode === undefined ||
      (typeof value.effectCode === 'string' &&
        value.effectCode.length <= 96 &&
        /^[A-Za-z0-9_:-]*$/.test(value.effectCode)))
  );
}

/**
 * keyDisplayName keeps a renamed key authoritative. The installation's Key and
 * the keys an adopted CPA already carried have stored default names that every
 * locale translates instead of displaying.
 */
export function keyDisplayName(
  key: Key,
  installerLabel: string,
  adoptedLabel: string,
  unnamedLabel: string
): string {
  if (key.origin === 'installer' && key.nameRevision <= 1) return installerLabel;
  if (key.origin === 'adopted' && key.nameRevision <= 1) return adoptedLabel;
  return key.name || unnamedLabel;
}

export type KeyOperationMode = 'add' | 'revoke' | 'replace';

export interface KeyOperationStep {
  name: 'add' | 'revoke';
  state: 'unattempted' | 'attempted' | 'observed';
  returned: boolean;
}

export interface KeyOperation {
  id: string;
  targetId: string;
  keeperId: string;
  mode: KeyOperationMode;
  outcome: 'pending' | 'unknown' | 'committed' | 'aborted';
  revision: number;
  steps: KeyOperationStep[];
}

export interface KeyOperationResult {
  operation: KeyOperation | null;
  runtimeState: 'effect_unknown' | 'effective_observed' | 'not_applied';
  expiresAtMs: number;
  nextStep: '' | 'add' | 'revoke';
}

export function validKeyOperationResult(r: KeyOperationResult): boolean {
  const o = r?.operation;
  if (
    !r ||
    !['effect_unknown', 'effective_observed', 'not_applied'].includes(r.runtimeState) ||
    !Number.isSafeInteger(r.expiresAtMs) ||
    !['', 'add', 'revoke'].includes(r.nextStep)
  ) {
    return false;
  }
  if (o === null) return r.runtimeState !== 'not_applied';
  if (
    !o ||
    !/^[a-f0-9]{32}$/.test(o.id) ||
    !/^[a-f0-9]{32}$/.test(o.targetId) ||
    !['add', 'revoke', 'replace'].includes(o.mode) ||
    !['pending', 'unknown', 'committed', 'aborted'].includes(o.outcome) ||
    !Number.isSafeInteger(o.revision) ||
    o.revision <= 0 ||
    !Array.isArray(o.steps)
  ) {
    return false;
  }
  if (
    o.outcome !== 'aborted' &&
    r.runtimeState === 'not_applied' &&
    !(o.outcome === 'committed' && o.mode === 'add' && r.nextStep === '')
  ) {
    return false;
  }
  if (o.outcome === 'aborted' && (r.runtimeState === 'effective_observed' || r.nextStep !== '')) {
    return false;
  }
  if (o.mode === 'add') {
    if (o.keeperId !== '' || o.steps.length !== 1 || o.steps[0].name !== 'add') return false;
  } else if (o.mode === 'revoke') {
    if (
      (o.keeperId !== '' && (!/^[a-f0-9]{32}$/.test(o.keeperId) || o.keeperId === o.targetId)) ||
      o.steps.length !== 1 ||
      o.steps[0].name !== 'revoke'
    ) {
      return false;
    }
  } else {
    if (
      !/^[a-f0-9]{32}$/.test(o.keeperId) ||
      o.keeperId === o.targetId ||
      o.steps.length !== 2 ||
      o.steps[0].name !== 'add' ||
      o.steps[1].name !== 'revoke'
    ) {
      return false;
    }
  }
  if (
    !o.steps.every(
      (s) =>
        ['unattempted', 'attempted', 'observed'].includes(s.state) &&
        typeof s.returned === 'boolean'
    )
  ) {
    return false;
  }
  if (o.outcome === 'aborted') {
    return o.steps.every((s) => s.state === 'unattempted' && !s.returned);
  }
  return o.outcome !== 'committed' || o.steps.every((s) => s.state === 'observed');
}

export type KeyRuntimeLabelKey =
  | 'runtimeEffectiveObserved'
  | 'runtimeApplying'
  | 'runtimeNotApplied'
  | 'runtimeEffectUnknown'
  | 'runtimeRevokedObserved';

export function keyRuntimeTextKey(state: KeyRuntimeState): KeyRuntimeLabelKey {
  switch (state) {
    case 'effective_observed':
      return 'runtimeEffectiveObserved';
    case 'applying':
      return 'runtimeApplying';
    case 'effect_unknown':
      return 'runtimeEffectUnknown';
    case 'revoked_observed':
      return 'runtimeRevokedObserved';
    case 'not_applied':
    default:
      return 'runtimeNotApplied';
  }
}

export function keyRuntimePillClass(state: KeyRuntimeState): string {
  switch (state) {
    case 'effective_observed':
      return 'is-effective';
    case 'revoked_observed':
      return 'is-muted';
    case 'applying':
    case 'not_applied':
    case 'effect_unknown':
    default:
      return 'is-pending';
  }
}

export type KeyLifecycleLabelKey = 'saved' | 'missingLifecycle' | 'replaced';

export function keyLifecycleTextKey(lifecycle?: KeyLifecycle): KeyLifecycleLabelKey {
  switch (lifecycle) {
    case 'superseded':
      return 'replaced';
    case 'missing':
      return 'missingLifecycle';
    case 'active':
    default:
      return 'saved';
  }
}

export function keyLifecyclePillClass(lifecycle?: KeyLifecycle): string {
  switch (lifecycle) {
    case 'superseded':
      return 'is-muted';
    case 'missing':
      return 'is-pending';
    case 'active':
    default:
      return 'is-saved';
  }
}

export function isTerminalKeyOperationOutcome(outcome: KeyOperation['outcome']): boolean {
  return outcome === 'committed' || outcome === 'aborted';
}

export type EffectBlockerKey =
  | 'effectBlockerHostProof'
  | 'effectBlockerSourceConflict'
  | 'effectBlockerConflict'
  | 'effectBlockerGeneration'
  | 'effectBlockerExternalRejected'
  | 'effectBlockerExternalMoved'
  | 'effectBlockerExternalVersion'
  | 'effectBlockerExternalUnavailable'
  | 'effectBlockerLastKey'
  | 'effectBlockerUnknown';

export function effectBlockerTextKey(code?: string): EffectBlockerKey {
  switch (code) {
    case 'key_operation_host_proof_required':
      return 'effectBlockerHostProof';
    case 'key_operation_source_conflict':
      return 'effectBlockerSourceConflict';
    case 'key_operation_conflict':
      return 'effectBlockerConflict';
    case 'key_operation_generation_mismatch':
      return 'effectBlockerGeneration';
    case 'external_management_key_rejected':
      return 'effectBlockerExternalRejected';
    case 'external_target_moved':
      return 'effectBlockerExternalMoved';
    case 'external_cpa_version_unsupported':
      return 'effectBlockerExternalVersion';
    case 'external_cpa_authority_unavailable':
      return 'effectBlockerExternalUnavailable';
    case 'last_effective_key':
      return 'effectBlockerLastKey';
    default:
      return 'effectBlockerUnknown';
  }
}
