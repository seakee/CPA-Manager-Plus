// The External pages share one vocabulary of states. Keeping it in a plain module
// rather than beside a component is what lets the pages import the mapping
// without a Fast Refresh boundary between data and view.

// The states an External installation can be blocked in. Each is a different
// problem with a different way out, so a page names the state instead of
// collapsing every failure into one "unavailable".
export type ExternalBlocker =
  | { kind: 'key_rejected'; detail?: string }
  | { kind: 'target_moved' }
  | { kind: 'version_unsupported'; version?: string; support?: string }
  | { kind: 'legacy_write_replaced' }
  | { kind: 'rebind_refused' }
  | { kind: 'rebind_stale' }
  | { kind: 'authority_unavailable' };

// blockerOf maps a stable wire code onto the state a page acts on. A code this
// table does not know is not a state: the caller keeps its own contract instead
// of guessing a cause from a status alone.
export function blockerOf(code: string | undefined): ExternalBlocker | null {
  switch (code) {
    case 'external_management_key_rejected':
      return { kind: 'key_rejected' };
    case 'external_target_moved':
      return { kind: 'target_moved' };
    case 'external_cpa_version_unsupported':
      return { kind: 'version_unsupported' };
    case 'external_legacy_write_replaced':
      return { kind: 'legacy_write_replaced' };
    case 'external_rebind_refused':
      return { kind: 'rebind_refused' };
    case 'external_rebind_stale_generation':
      return { kind: 'rebind_stale' };
    case 'external_cpa_authority_unavailable':
      return { kind: 'authority_unavailable' };
    default:
      return null;
  }
}

// What `GET external/usage` reports. Every field is a fact about the user's own
// CPA; none of it is a credential and none of it is inferred.
export type ExternalUsageState = {
  statisticsEnabled: boolean;
  collector: string;
  collectionStarted: boolean;
  cpaVersion: { version: string; support: string; writeBlocked: boolean };
  blockedReason?: string;
};

// parseExternalUsageState validates the whole reply. A partial answer is not a
// state: a page reporting coverage from a guess is worse than one reporting that
// the state is unavailable.
export function parseExternalUsageState(value: unknown): ExternalUsageState | null {
  if (!value || typeof value !== 'object') return null;
  const raw = value as Record<string, unknown>;
  const version = raw.cpaVersion as Record<string, unknown> | undefined;
  if (
    typeof raw.statisticsEnabled !== 'boolean' ||
    typeof raw.collector !== 'string' ||
    typeof raw.collectionStarted !== 'boolean' ||
    !version ||
    typeof version.version !== 'string' ||
    typeof version.support !== 'string' ||
    typeof version.writeBlocked !== 'boolean' ||
    (raw.blockedReason !== undefined && typeof raw.blockedReason !== 'string')
  )
    return null;
  return {
    statisticsEnabled: raw.statisticsEnabled,
    collector: raw.collector,
    collectionStarted: raw.collectionStarted,
    cpaVersion: {
      version: version.version,
      support: version.support,
      writeBlocked: version.writeBlocked,
    },
    blockedReason: raw.blockedReason as string | undefined,
  };
}

// newExternalRequestId is the idempotency identity of one External write. It is
// kept across a retry so a lost answer can be replayed rather than repeated.
export function newExternalRequestId(): string {
  return Array.from(crypto.getRandomValues(new Uint8Array(16)), (byte) =>
    byte.toString(16).padStart(2, '0')
  ).join('');
}
