// The wire contract of the Adopt flow, shared by both carriers.
//
// An adoption is readable through the bootstrap carrier before routing exists
// and through the canonical public API afterwards, so the two transports must
// agree on one shape. This module is that shape: the summary the progress screen
// polls, the request body the strategy call takes, and the only two validators
// that decide whether either can be used.
import { validRuntimeGeneration, type RuntimeGeneration } from './runtimeGeneration';

/** Validators and the bounded vocabulary of the Adopt wire contract. */

/**
 * The strategy blockers an Adopt attempt can be refused with. Both carriers
 * forward only these: a refusal outside the set stays a plain HTTP failure, so an
 * upstream payload can never smuggle a message into the UI.
 */
export const ADOPT_BLOCKERS: Record<string, true> = {
  adoption_source_read_unavailable: true,
  adoption_source_empty: true,
  adoption_source_class_refused: true,
  adoption_source_qualification_required: true,
  adoption_destination_identity_conflict: true,
  adoption_destination_staging_required: true,
  adoption_destination_stage_unresolved: true,
  adoption_destination_stage_failed: true,
  adoption_destination_start_unresolved: true,
  adoption_destination_start_failed: true,
  adoption_destination_verification_required: true,
  adoption_resource_qualification_required: true,
  adoption_snapshot_unavailable: true,
  adopt_plugin_optin_conflict: true,
  exact_v8_artifact_qualification_required: true,
  management_connection_invalid: true,
  management_secret_storage_unavailable: true,
  managed_target_observation_unavailable: true,
  managed_target_identity_mismatch: true,
  fresh_strategy_owner_conflict: true,
  fresh_strategy_authority_unavailable: true,
  fresh_strategy_invalid: true,
};

export type AdoptBlockerCode = keyof typeof ADOPT_BLOCKERS;

export function isAdoptBlocker(code: unknown): code is AdoptBlockerCode {
  return typeof code === 'string' && ADOPT_BLOCKERS[code] === true;
}

/** The only source class the v8 migration executor can read. */
export const ADOPT_SOURCE_CLASS = 'network_existing';

export type AdoptionKind = 'credential' | 'config_leaf' | 'config_family' | 'provider_section';

export type AdoptionOutcome = 'migrated' | 'manual' | 'failed' | 'absent_at_source';

/** One recorded per-item result. It never carries a credential or a value. */
export interface AdoptionItem {
  item: string;
  kind: AdoptionKind;
  outcome: AdoptionOutcome;
  reason: string;
  instruction: string;
  retryable: boolean;
}

/**
 * One recorded inventory decision of a staged adoption.
 *
 * The fields are PascalCase because the pinned domain type has no JSON tags, so
 * the wire name is the Go field name. Reading it as it is keeps the browser and
 * the record on one name; adding tags would change a pinned file.
 */
export interface AdoptionDecision {
  ResourceRef: string;
  Kind: string;
  Scope: string;
  ReadEvidenceRef: string;
  ReplayEvidenceRef: string;
  VerifyEvidenceRef: string;
  Qualification: string;
  ManualAction: string;
  Disposition: 'migrate' | 'manual_action' | 'unsupported';
}

/** The recorded progress of one run plus the live in-flight flag. */
export interface AdoptionSummary {
  items: AdoptionItem[];
  planned: number;
  finished: boolean;
  running: boolean;
  retryable: number;
  counts: { migrated: number; manual: number; failed: number; absent_at_source: number };
}

/** One group per recorded outcome, plus the plan the record never settled. */
export interface AdoptionGroups {
  migrated: AdoptionItem[];
  manual: AdoptionItem[];
  failed: AdoptionItem[];
  absent_at_source: AdoptionItem[];
  /**
   * Planned items the record never settled. A run can finish with this above
   * zero — the plan is fixed before the first write, and a run that stopped
   * early leaves the remainder unrecorded — so it is a fact the completion
   * screen has to state rather than hide behind the finished flag.
   */
  remaining: number;
}

/**
 * groupAdoptionItems is the completion screen's only read of the record.
 *
 * Each item lands in the group its own outcome names. `absent_at_source` is a
 * settled result about a resource the source never had, so it is its own group:
 * grouping it with `migrated` would put a copy count on the screen that no copy
 * produced.
 */
export function groupAdoptionItems(summary: AdoptionSummary): AdoptionGroups {
  const groups: AdoptionGroups = {
    migrated: [],
    manual: [],
    failed: [],
    absent_at_source: [],
    remaining: Math.max(summary.planned, summary.items.length) - summary.items.length,
  };
  for (const item of summary.items) groups[item.outcome].push(item);
  return groups;
}

const kinds: Record<string, true> = {
  credential: true,
  config_leaf: true,
  config_family: true,
  provider_section: true,
};
const outcomes: Record<string, true> = {
  migrated: true,
  manual: true,
  failed: true,
  absent_at_source: true,
};

function count(value: unknown): value is number {
  return Number.isSafeInteger(value) && (value as number) >= 0;
}

/**
 * parseAdoptionSummary accepts only the reply shape the Manager produces.
 *
 * The summary drives the completion screen, so a foreign or partial body has to
 * fail here rather than render as an almost-finished run: an unknown outcome, a
 * non-boolean flag or a missing item list is a response the browser refuses.
 */
export function parseAdoptionSummary(value: unknown): AdoptionSummary {
  const reply = value as AdoptionSummary | null;
  if (
    !reply ||
    typeof reply !== 'object' ||
    !Array.isArray(reply.items) ||
    !count(reply.planned) ||
    typeof reply.finished !== 'boolean' ||
    typeof reply.running !== 'boolean' ||
    !count(reply.retryable) ||
    !reply.counts ||
    typeof reply.counts !== 'object' ||
    !count(reply.counts.migrated) ||
    !count(reply.counts.manual) ||
    !count(reply.counts.failed) ||
    !count(reply.counts.absent_at_source)
  )
    throw new Error('Invalid adoption summary');
  const items = reply.items.map((item) => {
    if (
      !item ||
      typeof item !== 'object' ||
      typeof item.item !== 'string' ||
      !item.item ||
      !kinds[item.kind] ||
      !outcomes[item.outcome] ||
      (item.reason !== undefined && typeof item.reason !== 'string') ||
      (item.instruction !== undefined && typeof item.instruction !== 'string') ||
      typeof item.retryable !== 'boolean'
    )
      throw new Error('Invalid adoption item');
    return {
      item: item.item,
      kind: item.kind,
      outcome: item.outcome,
      reason: item.reason || '',
      instruction: item.instruction || '',
      retryable: item.retryable,
    };
  });
  return {
    items,
    planned: reply.planned,
    finished: reply.finished,
    running: reply.running,
    retryable: reply.retryable,
    counts: {
      migrated: reply.counts.migrated,
      manual: reply.counts.manual,
      failed: reply.counts.failed,
      absent_at_source: reply.counts.absent_at_source,
    },
  };
}

/**
 * normalizeCPAAddress is the one address rule both the External and the Adopt
 * form apply: http or https, no userinfo, no query or fragment, no escapes, and
 * no trailing slash. The server normalizes again; this only keeps an obviously
 * unusable address out of the request.
 */
export function normalizeCPAAddress(value: string): string {
  const normalized = value.trim().replace(/\/+$/, '');
  if (/[\\%@?#\s]/.test(normalized)) throw new Error('Invalid CPA address');
  const url = new URL(normalized);
  if (
    !['http:', 'https:'].includes(url.protocol) ||
    url.username ||
    url.password ||
    url.origin + url.pathname.replace(/\/+$/, '') !== normalized
  )
    throw new Error('Invalid CPA address');
  return normalized;
}

/** A management key is a bearer secret the browser holds in memory only. */
export function validManagementKey(value: string): boolean {
  return !!value && value.length <= 4096 && !/[\s\p{Cc}]/u.test(value);
}

/** The destination this Manager owns, observed before the source is read. */
export interface AdoptRuntimeTarget {
  runtimeIdentity: string;
  runtimeGeneration: RuntimeGeneration;
  artifactId: string;
}

/**
 * adoptStrategyBody is the exact body both POSTs of an adoption send.
 *
 * The first POST reads the source and answers `staged`; the second, byte
 * identical, stages the destination and answers `committed`. The revision and
 * the identity are added by the carrier, because they are the operation's fact
 * rather than the operator's input. Every field is required: the server decodes
 * with DisallowUnknownFields, and the destination snapshot is keyed by the
 * management key, so a body that differs between the two calls is a different
 * request, not a retry.
 */
export function adoptStrategyBody(
  source: { baseUrl: string; managementKey: string; pluginsEnabled: boolean },
  target: AdoptRuntimeTarget
): Record<string, unknown> {
  const baseUrl = normalizeCPAAddress(source.baseUrl);
  if (!validManagementKey(source.managementKey)) throw new Error('Invalid management key');
  if (
    typeof target.runtimeIdentity !== 'string' ||
    !target.runtimeIdentity.trim() ||
    !validRuntimeGeneration(target.runtimeGeneration) ||
    !/^sha256:[a-f0-9]{64}$/.test(target.artifactId)
  )
    throw new Error('Invalid installation target');
  return {
    strategy: 'adopt_existing',
    adoptSourceClass: ADOPT_SOURCE_CLASS,
    baseUrl,
    managementKey: source.managementKey,
    expectedRuntimeIdentity: target.runtimeIdentity,
    expectedRuntimeGeneration: target.runtimeGeneration,
    expectedArtifactId: target.artifactId,
    // The opt-in belongs to the destination this Manager stages, never to the
    // source. It is sent on every attempt so a replay cannot change it.
    pluginsEnabled: source.pluginsEnabled,
  };
}
