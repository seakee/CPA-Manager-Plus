// Canonical Client Keys have three origins. The workspace creates its own; the
// default installation mints exactly one so CPA never starts with client
// authentication disabled; and an adopted installation registers the keys the
// CPA it took over already carried. All three are the same canonical record with
// the same masked-only projection — only the secret's shape, and therefore the
// mask it allows, differs.
export type KeyOrigin = 'workspace' | 'installer' | 'adopted';

export interface Key {
  id: string;
  requestId: string;
  maskedPreview: string;
  createdAtMs: number;
  managerState: 'persisted';
  runtimeState: 'not_applied' | 'effect_unknown';
  name: string;
  nameRevision: number;
  lifecycle?: 'active' | 'superseded';
  origin: KeyOrigin;
}

const workspaceSecret = /^cpamp_sk_[a-f0-9]{64}$/;
const installSecret = /^sk-[A-Za-z0-9]{64}$/;
// An adopted key carries the source CPA's own shape, so its mask is the
// ellipsis and, for a secret long enough to keep them, the last four characters.
const adoptedMask = /^…[\s\S]{0,4}$/u;

/** maskedPreviewOf is the only rendering a saved secret may produce. */
export function maskedPreviewOf(secret: string): string {
  if (workspaceSecret.test(secret)) return 'cpamp_sk_…' + secret.slice(-4);
  if (installSecret.test(secret)) return 'sk-…' + secret.slice(-4);
  return '';
}

/** validSecretShape accepts exactly the two canonical Client Key shapes. */
export function validSecretShape(secret: string): boolean {
  return workspaceSecret.test(secret) || installSecret.test(secret);
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
    ['not_applied', 'effect_unknown'].includes(value.runtimeState) &&
    typeof value.name === 'string' &&
    [...value.name].length <= 120 &&
    Number.isSafeInteger(value.nameRevision) &&
    value.nameRevision > 0 &&
    (value.lifecycle === undefined || ['active', 'superseded'].includes(value.lifecycle))
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
