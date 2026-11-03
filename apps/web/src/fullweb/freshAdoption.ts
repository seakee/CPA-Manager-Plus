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

/**
 * One recorded per-item result.
 *
 * The reply is a projection, not the record: the Manager chooses every field at
 * its output boundary. A credential item is described by the Manager's own
 * generic credential label plus its stable ordinal, never by the source file
 * name and never by a digest of it. A non-credential item keeps a canonical
 * reference from the Manager's closed vocabulary; upstream or free text does not
 * flow through on the strength of a prefix match. So `item` is a product label,
 * not a value the source wrote, and it cannot confirm a guessed credential
 * offline.
 *
 * `id` is the record's public identity for the item. It derives only from that
 * public label and the item's ordinal in the record: two items whose labels
 * coincide are still two items, and an acknowledgement has to be about the
 * record's item rather than about text that happens to be equal. It carries no
 * source name or name digest either.
 *
 * `left` is how many source items the record could not hold at all, set on the
 * aggregate excess marker only.
 *
 * The item never carries the record's free-text instruction: the page states the
 * next step from `kind` and `reason`, so upstream metadata cannot reach the
 * screen as prose.
 */
export interface AdoptionItem {
  id: string;
  item: string;
  kind: AdoptionKind;
  outcome: AdoptionOutcome;
  reason: string;
  retryable: boolean;
  left: number;
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
  /**
   * The identity of the run these items belong to: 32 lowercase hex assigned
   * when the run started and persisted with it. Every actual start is a new
   * identity even when it rediscovers the same items and counts. A legacy record
   * written before run identity existed parses as the empty string; it cannot be
   * acknowledged and has to be resumed into a tracked run first.
   */
  runId: string;
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

/**
 * needsCredentialFileNote says whether a group of items has to state what a
 * credential label means.
 *
 * The reply projects every credential as "Credential file #N": the ordinal comes
 * from the migration record, not from the source file name, because the name is
 * source metadata. An operator told to copy "this file" therefore has nothing to
 * look up, so the group carrying manual or failed credentials says once that the
 * number is a record number and how to find the missing files. The per-item line
 * stays one step.
 */
export function needsCredentialFileNote(items: readonly AdoptionItem[]): boolean {
  return items.some(
    (item) => item.kind === 'credential' && (item.outcome === 'manual' || item.outcome === 'failed')
  );
}

/**
 * What a recorded run lets its host say about the installation and the copy.
 *
 * The two are different facts. The installation completing is the server's
 * decision, and the server grants it over retryable items once the operator has
 * accepted this exact run: the acknowledgement is durable and run-bound, so a
 * positive retryable count means the decision was taken, not that it is still
 * open. What the copy left behind is the record's own count of manual and failed
 * items, and only a settled run that recorded its whole plan with nothing left
 * over may say the adoption itself is complete.
 *
 * Both hosts call this, so the completion step of a wizard and the shared flow
 * cannot disagree about the same run.
 */
export type AdoptionCompletionState = 'none' | 'partial' | 'complete';

export function adoptionCompletionState(
  summary: AdoptionSummary | null | undefined,
  productComplete: boolean
): AdoptionCompletionState {
  if (!summary || !summary.finished || summary.running || !productComplete) return 'none';
  const left = Math.max(summary.planned, summary.items.length) - summary.items.length;
  if (left !== 0) return 'none';
  if (summary.counts.manual === 0 && summary.counts.failed === 0)
    return summary.retryable === 0 ? 'complete' : 'none';
  return 'partial';
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
    // Only a legacy record may omit the run identity; a present value has to be
    // the exact opaque identity the Manager assigns, never a placeholder.
    (reply.runId !== undefined &&
      (typeof reply.runId !== 'string' ||
        (reply.runId !== '' && !/^[a-f0-9]{32}$/.test(reply.runId)))) ||
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
      typeof item.id !== 'string' ||
      !/^[a-f0-9]{64}$/.test(item.id) ||
      typeof item.item !== 'string' ||
      !item.item ||
      !kinds[item.kind] ||
      !outcomes[item.outcome] ||
      (item.reason !== undefined && typeof item.reason !== 'string') ||
      typeof item.retryable !== 'boolean' ||
      (item.left !== undefined && !count(item.left))
    )
      throw new Error('Invalid adoption item');
    return {
      id: item.id,
      item: item.item,
      kind: item.kind,
      outcome: item.outcome,
      reason: item.reason || '',
      retryable: item.retryable,
      left: item.left ?? 0,
    };
  });
  return {
    runId: reply.runId ?? '',
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

/**
 * The completion body.
 *
 * An ordinary completion is `{}`. Only an operator who accepted the retryable
 * items sends an acknowledgement, and that acknowledgement names the durable run
 * it was given for: a newer run with the same items and counts must ask again,
 * and the server refuses a mismatched identity with `adopt_migration_run_changed`.
 */
export interface AdoptCompletionBody {
  acceptRetryable?: true;
  runId?: string;
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
