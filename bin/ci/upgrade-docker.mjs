#!/usr/bin/env node
// Real-Docker acceptance for the v2 Upgrade chain (U1-8).
//
// What it proves, against a real 1.x release image, a real seeded data volume
// and the real v2 binary:
//
//   * preflight, copy-in, reconciliation and the persisted status, end to end;
//   * the two read-only Upgrade wizard routes, on a real Fresh installation,
//     with the 1.x administrator key, with no secret in the body, and with a
//     cross-site `Origin` refused on each of them;
//   * six isolated fault cases, each asserted by exit code, typed code and an
//     unchanged target root;
//   * a rollback recorded against a real 1.x stack that wrote after the copy,
//     restarted on the very directory `--verify` fingerprints, so the row that
//     stack wrote is what the verdict observed;
//   * that the manager admits only the Upgrade surface while the operation is
//     unfinished — both wizard reads included, from the copied root alone —
//     that those reads keep answering from the Manager store with a typed
//     `sourceRoot` marker when the 1.x source is no longer mounted while the
//     preflight keeps its 503, that a mounted source whose usage.sqlite is a
//     symbolic link stays reachable the same way with `upgrade_source_symlinked`
//     instead of bricking the surface, that the copy-in marker window reads
//     `copy_in`, that it serves the full surface and retires those reads once the
//     operation is complete, and that a probe answered by the manager itself is
//     refused.
//
// Everything it creates is tracked before it is created: containers, volumes,
// networks, compose projects and `git worktree` checkouts, all under the
// `cpampd<runid>` prefix. Images are built and tagged only in the harness's own
// `cpampd-` namespaces (`cpampd-fixture/legacy-v1:<version>` and this run's
// `cpampd<runid>/upgrade` alias); `cpamp-*` images and the shared
// `cpampd-fixture/upgrade:*` tags are inspect-only. Nothing else on the host is
// stopped, removed or renamed. The tracked set is released on the normal path,
// on every error path and on SIGINT/SIGTERM, and a cleanup that could not remove
// what this run created makes the process exit non-zero instead of reporting
// PASS.
//
// Usage:
//   node bin/ci/upgrade-docker.mjs [--reuse-fixture] [--keep] [--v1-tag v1.14.10]
//
// The evidence document lands in bin/ci/evidence/upgrade-d in CI (the workflow
// reads the published path) and in the ignored bin/tmp/u1-docker/evidence
// locally, so one local run's output cannot make the next local run refuse the
// tree. UPGRADE_DOCKER_EVIDENCE_DIR overrides both.
//                                  [--source-tag v1.14.4]
//   node bin/ci/upgrade-docker.mjs --verify-evidence <file> [--expect-head <sha>]
//
// `--reuse-fixture` skips `git worktree` + `docker build` and uses the recorded
// `cpampd-fixture/legacy-v1:<version>` image, which is what a second run (or the
// U1-1b re-pin) does. The image must carry the provenance labels this harness
// writes when it builds it, and its rollback alias must resolve to the same
// image ID; an image that does not is refused instead of being rebuilt over.
// `--verify-evidence` re-checks one evidence document without Docker: the
// canonical hash, the verdict, the implementation binding, the built binary
// digest and a clean tree.
// `SKIP_UPGRADE_DOCKER=1` is the only way to skip the run, and it says so.

import { spawn, spawnSync } from 'node:child_process';
import crypto from 'node:crypto';
import fs from 'node:fs';
import http from 'node:http';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { DatabaseSync } from 'node:sqlite';
import { fileURLToPath } from 'node:url';

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');
const MANAGER_SERVER = path.join(ROOT, 'apps', 'manager-server');

// The planned direct-upgrade release. It is the fixture the U1-1b re-pin
// reuses, so it is built and recorded on every run.
const DEFAULT_V1_TAG = 'v1.14.10';
// The release the running contract accepts. It is only the fallback: the real
// value is read from the product's own preflight report, so a re-pin needs no
// change here.
const DEFAULT_SOURCE_TAG = 'v1.14.4';

const ADMIN_V1 = 'Cpamp_U1D_Harness_Legacy_Admin_Key_11410';
const ADMIN_V2 = 'Cpamp_U1D_Harness_V2_Admin_Key_11410';

// Fixed dataset: three UTC days, six hours, two models, two client keys.
const DATASET_DAYS = ['2026-10-20', '2026-10-21', '2026-10-22'];
const DATASET_HOURS = [3, 7, 11, 15, 19, 23];
const DATASET_MODELS = [
  { model: 'gpt-5.1', provider: 'openai', executor: 'codex' },
  { model: 'claude-sonnet-4.5', provider: 'anthropic', executor: 'claude' },
];
const DATASET_KEYS = [
  { name: 'cpamp-u1d-primary', alias: 'U1D primary' },
  { name: 'cpamp-u1d-secondary', alias: 'U1D secondary' },
];
const DATASET_PRICES = {
  'gpt-5.1': { prompt: 1.25, completion: 10, cacheRead: 0.125 },
  'claude-sonnet-4.5': { prompt: 3, completion: 15, cacheRead: 0.3 },
};

const DAY_MS = 86_400_000;
const dayStartMS = (day) => Date.parse(`${day}T00:00:00Z`);

const TABLES = {
  retained: 'usage_events',
  pricing: 'usage_pricing_hourly_rollups_v1',
  coverage: 'usage_archive_deleted_coverage_daily',
  history: 'usage_monitoring_account_daily_rollups_v1',
};

// The fixture image this harness builds and reuses. It lives in the harness's
// own cpampd- namespace: no cpamp-* image is ever built, retagged or removed,
// because that namespace belongs to the collaborator's stacks and to older
// harness builds whose images this run only inspects.
export const IMAGE_REPOSITORY = 'cpampd-fixture/legacy-v1';
// The shared rollback repository older harness builds wrote to. Its tags are
// READ-ONLY inputs for this run: unlabeled images already occupy them, so this
// harness never retags, removes or reuses them. It only inspects them, to prove
// this run left them unchanged.
const SHARED_ROLLBACK_IMAGE_REPOSITORY = 'cpampd-fixture/upgrade';
const REDACTED = '[redacted]';
// The helper image the harness already runs for a container that only touches a
// mount (the same-volume probe). Ownership normalization is the same shape, and
// it must not need a second image contract.
const OWNERSHIP_HELPER_IMAGE = 'alpine:3.21';
// The management and client keys the harness's synthetic v8 CPA accepts. They
// are this run's own secrets, registered with the redactor beside the two
// administrator keys.
const SYNTHETIC_CPA_MANAGEMENT_KEY = 'Cpamp_U1D_Harness_CPA_Management_Key_0001';
const SYNTHETIC_CPA_CLIENT_KEY = 'cpampd-u1d-client-key-0001';

const isEntryPoint = (() => {
  try {
    return Boolean(process.argv[1]) && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url);
  } catch {
    return false;
  }
})();

const args = isEntryPoint
  ? parseArgs(process.argv.slice(2))
  : { reuseFixture: false, keep: false, v1Tag: DEFAULT_V1_TAG, sourceTag: DEFAULT_SOURCE_TAG, verifyEvidence: null, expectHead: null };
const runId = crypto.randomBytes(4).toString('hex');
const PREFIX = `cpampd${runId}`;
const SCRATCH = path.join(ROOT, 'bin', 'tmp', 'u1-8', runId);
// evidenceDirectory is where this run publishes its document. CI keeps the
// committed, uploaded location and the workflow reads it back through
// `evidence_path`. A local run writes under the ignored scratch tree instead:
// the committed records next to it are tracked files, so a local run that wrote
// beside them would leave the tree dirty and the *next* local run would refuse
// with `implementation.treeClean is not true` before doing any work.
// `UPGRADE_DOCKER_EVIDENCE_DIR` overrides both, which is how a local run can
// publish into a specific directory on purpose (and then owns that choice).
export function evidenceDirectory({ env = process.env, root = ROOT } = {}) {
  const override = typeof env.UPGRADE_DOCKER_EVIDENCE_DIR === 'string' ? env.UPGRADE_DOCKER_EVIDENCE_DIR.trim() : '';
  if (override !== '') {
    return path.isAbsolute(override) ? override : path.join(root, override);
  }
  return env.GITHUB_ACTIONS === 'true'
    ? path.join(root, 'bin', 'ci', 'evidence', 'upgrade-d')
    : path.join(root, 'bin', 'tmp', 'u1-docker', 'evidence');
}

const EVIDENCE_DIR = evidenceDirectory();
// This run's own rollback alias. The accepted rollback grammar needs a
// repository-qualified reference at an exact `vX.Y.Z` tag, and the shared
// `cpampd-fixture/upgrade` names are occupied by images this run cannot
// account for. The alias is derived from the run prefix, carries the fixture
// bytes (and therefore this build's provenance labels), is tracked in the
// registry before it is created, and is the only image reference the run
// removes.
const ROLLBACK_ALIAS_REPOSITORY = `${PREFIX}/upgrade`;
const RELEASE_TAG_PATTERN = /^v\d+\.\d+\.\d+$/;

// The release every source-reading command declares. The probe declares the
// fixture tag it is inspecting; once the product's own preflight report names
// the accepted release, that value replaces it, so the harness holds no second
// copy of the pin.
let acceptedRelease = null;

// The harness contract this file and its test both read. It names what the
// evidence claim rests on, not merely the file locations.
export const UPGRADE_DOCKER_CONTRACT = Object.freeze({
  contract: 'cpamp.upgrade-docker.v1',
  prefixPattern: /^cpampd[0-9a-f]{8}$/,
  resourceNamePattern: /^cpampd[0-9a-f]{8}(?:[-_].+)?$/,
  skipVariable: 'SKIP_UPGRADE_DOCKER',
  // Where CI publishes (see evidenceDirectory): a local run writes under the
  // ignored scratch tree so its own output cannot dirty the next local run.
  evidenceDirectory: path.join('bin', 'ci', 'evidence', 'upgrade-d'),
  // The labels a build writes onto the fixture image. A reused image without
  // them cannot be shown to come from this harness's build of that tag.
  fixtureContract: 'cpamp.upgrade-docker.fixture.v1',
  evidenceHashRule: 'sha256(JSON.stringify(document without its "sha256" field, null, 2))',
  // Objects outside this run's ownership. They never carry the prefix, so this
  // is a second refusal rather than the only one.
  protectedResources: Object.freeze(['cpa-manager-plus-cpa-manager-plus-1', 'cpa-usage-keeper']),
  protectedPrefixes: Object.freeze(['cpamp-', 'buildx_buildkit_']),
  signalExitCodes: Object.freeze({ SIGINT: 130, SIGTERM: 143 }),
  evidenceVerdicts: Object.freeze(['pass', 'fail', 'aborted']),
});

// ---------------------------------------------------------------------------
// owned resources: one registry for every object this run creates
// ---------------------------------------------------------------------------

// Release order: a process first, then the stack that holds containers and
// networks, then the containers, then what they may still reference.
export const RESOURCE_RELEASE_ORDER = Object.freeze(['hostProcess', 'composeProject', 'container', 'network', 'volume', 'image', 'worktree']);

function isWithin(root, value) {
  const target = path.resolve(String(value ?? ''));
  const base = path.resolve(String(root ?? ''));
  return target === base || target.startsWith(`${base}${path.sep}`);
}

// isOwnedResourceName reports whether a Docker object name belongs to this run.
// Only `<prefix>`, `<prefix>-…` and `<prefix>_…` are accepted, the protected
// names are refused explicitly, and a collaborator's stack (`cpamp-*`) can never
// match, whatever a future prefix looks like.
export function isOwnedResourceName(prefix, name) {
  const value = String(name ?? '');
  if (!UPGRADE_DOCKER_CONTRACT.prefixPattern.test(String(prefix ?? ''))) return false;
  if (!UPGRADE_DOCKER_CONTRACT.resourceNamePattern.test(value)) return false;
  if (UPGRADE_DOCKER_CONTRACT.protectedResources.includes(value)) return false;
  if (UPGRADE_DOCKER_CONTRACT.protectedPrefixes.some((protectedPrefix) => value.startsWith(protectedPrefix))) return false;
  return value === prefix || value.startsWith(`${prefix}-`) || value.startsWith(`${prefix}_`);
}

// isOwnedImageAlias accepts only the run-owned rollback alias: a repository
// under the run prefix, at a release tag the accepted rollback grammar reads as
// `vX.Y.Z`. The shared `cpampd-fixture/upgrade` repository and a bare
// `1.14.10` tag are never this run's to tag or remove.
export function isOwnedImageAlias(prefix, reference) {
  if (!UPGRADE_DOCKER_CONTRACT.prefixPattern.test(String(prefix ?? ''))) return false;
  const value = String(reference ?? '');
  const separator = value.lastIndexOf(':');
  if (separator <= 0) return false;
  const repository = value.slice(0, separator);
  const tag = value.slice(separator + 1);
  return repository === `${prefix}/upgrade` && RELEASE_TAG_PATTERN.test(tag);
}

// rollbackAliasReference derives this run's rollback alias from the release
// tag under test. The fixture bytes stay where they are; only the run-owned
// reference is created, tracked and removed.
export function rollbackAliasReference(tag, prefix = PREFIX) {
  if (!UPGRADE_DOCKER_CONTRACT.prefixPattern.test(String(prefix ?? ''))) {
    throw new Error(`a rollback alias needs a cpampd<8 hex> prefix, got ${JSON.stringify(prefix)}`);
  }
  const value = String(tag ?? '');
  if (!RELEASE_TAG_PATTERN.test(value)) {
    throw new Error(`a rollback alias tag must be an exact vX.Y.Z release, got ${JSON.stringify(value)}`);
  }
  return `${prefix}/upgrade:${value}`;
}

// imageRepository is the repository part of a Docker image reference: the tag,
// the digest and a registry port are not part of it.
export function imageRepository(reference) {
  const value = String(reference ?? '');
  const withoutDigest = value.split('@')[0];
  const separator = withoutDigest.lastIndexOf(':');
  return separator > withoutDigest.lastIndexOf('/') ? withoutDigest.slice(0, separator) : withoutDigest;
}

// assertImageWriteArgv guards the only Docker subcommands this harness may use
// to create or remove an image reference: build -t/--tag, tag, and image rm.
// Every reference must sit in the harness's own cpampd- namespace — the fixture
// repository or this run's rollback alias — so a cpamp-* image (a collaborator's
// object, or an older harness image whose tag is inspect-only) can never be
// built over, retagged or removed, whatever argv a future change constructs.
export function assertImageWriteArgv(argv, { prefix = PREFIX } = {}) {
  const tokens = Array.isArray(argv) ? argv.map(String) : [];
  const references = [];
  switch (tokens[0]) {
    case 'tag':
      references.push(tokens[1], tokens[2]);
      break;
    case 'build':
      for (let index = 1; index < tokens.length; index += 1) {
        const token = tokens[index];
        if (token === '-t' || token === '--tag') references.push(tokens[index + 1]);
        else if (token.startsWith('--tag=')) references.push(token.slice('--tag='.length));
        else if (token.startsWith('-t=')) references.push(token.slice(3));
      }
      break;
    case 'image':
      if (tokens[1] === 'rm' || tokens[1] === 'rmi') {
        references.push(...tokens.slice(2).filter((token) => !token.startsWith('-')));
      }
      break;
    case 'rmi':
      references.push(...tokens.slice(1).filter((token) => !token.startsWith('-')));
      break;
    default:
      throw new Error(`refusing an unsupported image write subcommand ${JSON.stringify(tokens[0])}`);
  }
  const allowed = new Set([IMAGE_REPOSITORY, `${prefix}/upgrade`]);
  for (const reference of references) {
    if (!reference) throw new Error(`an image write argument is missing from ${JSON.stringify(tokens)}`);
    const repository = imageRepository(reference);
    if (!allowed.has(repository)) {
      throw new Error(
        `refusing to write image reference ${reference}: ${repository} is not one of this harness's repositories (${[...allowed].join(', ')})`,
      );
    }
  }
  return tokens;
}

// shutdownRefusal is the error a producer gets once the registry is fenced. Its
// code lets the acceptance path tell "the run is stopping" apart from a real
// failure without parsing the message.
function shutdownRefusal(kind, name, reason) {
  const error = new Error(`refusing to track ${kind} ${String(name)}: the run is shutting down (${reason})`);
  error.code = 'run_shutting_down';
  error.resourceKind = kind;
  return error;
}

// createResourceRegistry tracks what this run created, before it is created. A
// tracked entry owns its own release; a release that throws stays tracked and is
// reported, so a partial cleanup can never be mistaken for a clean one. Nothing
// is released twice and nothing outside the prefix is ever tracked.
//
// The registry is also the run's creation boundary: once `fence` is called it
// refuses every new `track`, synchronously and for good. A shutdown that only
// set a flag would still let an already-awaited step resume and create after
// the release snapshot was taken; a fenced registry cannot.
export function createResourceRegistry({ prefix, root = null } = {}) {
  if (!UPGRADE_DOCKER_CONTRACT.prefixPattern.test(String(prefix ?? ''))) {
    throw new Error(`a resource registry needs a cpampd<8 hex> prefix, got ${JSON.stringify(prefix)}`);
  }
  const entries = new Map();
  const inFlight = new Map();
  const keyOf = (kind, name) => `${kind}:${name}`;
  let shutdownReason = null;

  const owns = (kind, name) => {
    if (!RESOURCE_RELEASE_ORDER.includes(kind)) return false;
    // An image is only ever this run's own rollback alias. A container-style
    // `${prefix}-…` name is not an image this run may remove.
    if (kind === 'image') return isOwnedImageAlias(prefix, name);
    if (isOwnedResourceName(prefix, name)) return true;
    // A worktree checkout is owned by this run's scratch root, not by a Docker
    // name, and never by a protected object.
    return kind === 'worktree' && root !== null && isWithin(root, name);
  };

  const registry = {
    prefix,
    get shutdownReason() {
      return shutdownReason;
    },
    has: (kind, name) => entries.has(keyOf(kind, name)),
    entry: (kind, name) => entries.get(keyOf(kind, name)) ?? null,
    list: (kind) => [...entries.values()].filter((entry) => entry.kind === kind),
    all: () => [...entries.values()],
    // fence closes the creation boundary. It is synchronous on purpose: a
    // signal handler calls it before its first await, so a step that resumes
    // later cannot race a release snapshot.
    fence(reason = 'shutdown requested') {
      if (!shutdownReason) shutdownReason = String(reason);
      return shutdownReason;
    },
    // assertCreationAllowed is what a producer calls before spawning something
    // it will then track; track itself refuses too, but a spawn that happens
    // first would already have escaped the registry.
    assertCreationAllowed(kind = 'resource', name = '') {
      if (shutdownReason) throw shutdownRefusal(kind, name, shutdownReason);
    },
    track(kind, name, release, extra = {}) {
      if (!RESOURCE_RELEASE_ORDER.includes(kind)) throw new Error(`unknown resource kind ${kind}`);
      if (typeof release !== 'function') throw new Error(`a tracked ${kind} needs a release function`);
      if (shutdownReason) throw shutdownRefusal(kind, name, shutdownReason);
      if (!owns(kind, name)) {
        throw new Error(`refusing to track ${kind} ${String(name)}: it is not one of this run's own resources`);
      }
      const existing = entries.get(keyOf(kind, name));
      if (existing) return existing;
      const entry = { kind, name: String(name), release, ...extra, trackedAt: Date.now() };
      entries.set(keyOf(kind, name), entry);
      return entry;
    },
    untrack(kind, name) {
      return entries.delete(keyOf(kind, name));
    },
    async releaseEntry(kind, name) {
      const key = keyOf(kind, name);
      const entry = entries.get(key);
      if (!entry) return { kind, name, released: false, skipped: true };
      // A cleanup and a step's `finally` can release the same entry at the same
      // time. Both callers observe one release instead of signalling a process
      // twice or removing a container that the first call already removed.
      if (inFlight.has(key)) return inFlight.get(key);
      const releasing = (async () => {
        try {
          await entry.release();
          entries.delete(key);
          return { kind, name, released: true, skipped: false };
        } catch (error) {
          return { kind, name, released: false, skipped: false, error: error?.message || String(error) };
        } finally {
          inFlight.delete(key);
        }
      })();
      inFlight.set(key, releasing);
      return releasing;
    },
    async releaseKind(kind, result = newCleanupResult()) {
      for (const entry of registry.list(kind)) {
        recordRelease(result, await registry.releaseEntry(entry.kind, entry.name));
      }
      return result;
    },
    async releaseAll(result = newCleanupResult()) {
      for (const kind of RESOURCE_RELEASE_ORDER) {
        await registry.releaseKind(kind, result);
      }
      return result;
    },
  };
  return registry;
}

export function newCleanupResult() {
  return { ok: true, released: [], errors: [] };
}

function recordRelease(result, outcome) {
  if (outcome.skipped) return;
  if (outcome.released) {
    result.released.push(`${outcome.kind}:${outcome.name}`);
    return;
  }
  result.ok = false;
  result.errors.push(`${outcome.kind}:${outcome.name}: ${outcome.error ?? 'release failed'}`);
}

// finalExitCode is the one verdict: a failed run and a run whose cleanup left
// anything behind both fail, and an abort is the signal's own code.
export function finalExitCode({ runOk, cleanupOk = true, signal = null }) {
  if (signal) return UPGRADE_DOCKER_CONTRACT.signalExitCodes[signal] ?? 1;
  return runOk && cleanupOk ? 0 : 1;
}

// ---------------------------------------------------------------------------
// cancellation: a handled signal runs the same cleanup as the normal path
// ---------------------------------------------------------------------------

// installSignalGuard runs `onSignal` once for SIGINT/SIGTERM, then exits with the
// signal's code. Node's default signal termination never runs a `finally`, so the
// release path has to be attached to the signal itself. The listener stays
// attached for the whole handler: the `handled` guard absorbs every later signal
// instead of letting Node's default kill interrupt the cleanup, which is what a
// second Ctrl-C or a repeated SIGTERM from a supervisor would otherwise do.
// detach removes the listeners once the run no longer needs them.
export function installSignalGuard({
  onSignal,
  exit = (code) => process.exit(code),
  target = process,
  signals = Object.keys(UPGRADE_DOCKER_CONTRACT.signalExitCodes),
} = {}) {
  if (typeof onSignal !== 'function') throw new Error('a signal guard needs an onSignal handler');
  let handled = null;
  const listeners = new Map();
  const guard = {
    get signal() {
      return handled;
    },
    async handle(signal) {
      if (handled) return UPGRADE_DOCKER_CONTRACT.signalExitCodes[handled] ?? 1;
      handled = signal;
      let exitCode = UPGRADE_DOCKER_CONTRACT.signalExitCodes[signal] ?? 1;
      try {
        const outcome = await onSignal(signal);
        if (outcome && Number.isInteger(outcome.exitCode) && outcome.exitCode !== 0) exitCode = outcome.exitCode;
      } catch {
        // The run was aborted either way; the signal's own code still says so.
      }
      exit(exitCode);
      return exitCode;
    },
    detach() {
      for (const [signal, listener] of listeners) target.removeListener(signal, listener);
      listeners.clear();
      return guard;
    },
  };
  for (const signal of signals) {
    const listener = () => {
      void guard.handle(signal);
    };
    listeners.set(signal, listener);
    // on, not once: the handler is the only thing that may decide the process
    // exit, and a repeat signal has to reach `handle` (which returns without
    // exiting) instead of Node's default termination in the middle of cleanup.
    target.on(signal, listener);
  }
  return guard;
}

// ---------------------------------------------------------------------------
// ports: one allocator per run, so two ports of the same run are never equal
// ---------------------------------------------------------------------------

export const PORT_RANGE = Object.freeze({ min: 18_900, max: 19_799 });

export function createPortAllocator({ probe = probePortAvailable, random = Math.random, reserved = [], range = PORT_RANGE } = {}) {
  const used = new Set(reserved);
  const span = range.max - range.min + 1;
  return {
    used,
    take() {
      const start = range.min + Math.floor(random() * span);
      for (let offset = 0; offset < span; offset += 1) {
        const candidate = range.min + ((start - range.min + offset) % span);
        if (used.has(candidate)) continue;
        if (!probe(candidate)) continue;
        used.add(candidate);
        return candidate;
      }
      throw new Error(`no free port in ${range.min}-${range.max} could be reserved`);
    },
  };
}

// ---------------------------------------------------------------------------
// secrets: one redactor for every log line, tail and evidence field
// ---------------------------------------------------------------------------

// createRedactor removes every registered secret from a text before it is
// printed or written. The registration is what makes the redaction complete: a
// value the harness knows (its admin keys, the source data key, the handoff
// fragment) is added when it becomes known.
export function createRedactor(initial = []) {
  const secrets = new Set();
  const add = (value) => {
    const text = value === null || value === undefined ? '' : String(value);
    if (text.length >= 4) secrets.add(text);
  };
  for (const secret of initial) add(secret);
  const ordered = () => [...secrets].sort((left, right) => right.length - left.length);
  const redact = (value) => {
    let text = value === null || value === undefined ? '' : String(value);
    for (const secret of ordered()) {
      if (text.includes(secret)) text = text.split(secret).join(REDACTED);
    }
    return text;
  };
  return {
    add,
    redact,
    has: (value) => secrets.has(String(value ?? '')),
    size: () => secrets.size,
  };
}

// ---------------------------------------------------------------------------
// evidence: canonical hash, implementation binding, verdict
// ---------------------------------------------------------------------------

// canonicalEvidenceDocument is the document a hash covers: the evidence itself,
// without the field that carries the hash.
export function canonicalEvidenceDocument(document) {
  const { sha256: _hash, ...rest } = document ?? {};
  void _hash;
  return rest;
}

export function evidenceHash(document) {
  return sha256(JSON.stringify(canonicalEvidenceDocument(document), null, 2));
}

export function withEvidenceHash(document) {
  return { ...document, sha256: evidenceHash(document) };
}

const SHA1_PATTERN = /^[0-9a-f]{40}$/;
const SHA256_PATTERN = /^[0-9a-f]{64}$/;

// createImplementationIdentity is the binding an evidence document carries: which
// commit, which harness bytes, which binary. It is invalid rather than incomplete
// when a digest is missing, so a document cannot claim a binding it does not have.
export function createImplementationIdentity({ headSha, harnessSha256, binarySha256 = null, branch = null, treeClean = null } = {}) {
  const invalid = [];
  if (!SHA1_PATTERN.test(String(headSha ?? ''))) invalid.push('headSha');
  if (!SHA256_PATTERN.test(String(harnessSha256 ?? ''))) invalid.push('harnessSha256');
  return { headSha, harnessSha256, binarySha256, branch, treeClean, valid: invalid.length === 0, invalid };
}

// verifyEvidenceDocument re-checks a document the way CI does: the canonical
// hash, a recorded verdict, the implementation binding (optionally pinned to the
// commit the workflow ran), the digest of the binary that ran, and a clean tree.
// The last two are required: a run that did not build a binary or that ran over
// uncommitted changes cannot be presented as a binding to the commit.
export function verifyEvidenceDocument(document, { expectHead = null } = {}) {
  const problems = [];
  const expected = evidenceHash(document);
  if (document?.sha256 !== expected) problems.push('sha256 does not match the canonical hash');
  if (!UPGRADE_DOCKER_CONTRACT.evidenceVerdicts.includes(document?.verdict)) problems.push('verdict is not pass, fail or aborted');
  if (!SHA1_PATTERN.test(String(document?.implementation?.headSha ?? ''))) problems.push('implementation.headSha is missing');
  if (!SHA256_PATTERN.test(String(document?.implementation?.harnessSha256 ?? ''))) problems.push('implementation.harnessSha256 is missing');
  if (!SHA256_PATTERN.test(String(document?.implementation?.binarySha256 ?? ''))) problems.push('implementation.binarySha256 is missing');
  if (document?.implementation?.treeClean !== true) problems.push('implementation.treeClean is not true');
  if (typeof document?.startedAt !== 'string' || document.startedAt === '') problems.push('startedAt is missing');
  if (expectHead && document?.implementation?.headSha !== expectHead) problems.push('implementation.headSha is not the expected commit');
  return { ok: problems.length === 0, problems, expected, actual: document?.sha256 ?? null };
}

const redactor = createRedactor([ADMIN_V1, ADMIN_V2, SYNTHETIC_CPA_MANAGEMENT_KEY]);

const evidence = {
  contract: UPGRADE_DOCKER_CONTRACT.contract,
  runId,
  prefix: PREFIX,
  mode: { reuseFixture: args.reuseFixture, v1Tag: args.v1Tag, sourceTagOverride: args.sourceTag },
  host: {
    platform: process.platform,
    arch: process.arch,
    node: process.version,
    docker: null,
  },
  fixtures: [],
  commands: [],
  chains: {},
  faults: [],
  rollback: {},
  wizard: {},
  admission: {},
  cleanup: {},
  findings: [],
};
const stableFacts = [];

let started = 0;
let abortedSignal = null;
let runFailure = null;
let hostBeforeRun = null;
const registry = createResourceRegistry({ prefix: PREFIX, root: SCRATCH });
const hostManagers = new Map();
const ports = createPortAllocator();
let evidenceFile = null;
let finalizing = null;
let acceptanceSettled = null;

// abortError marks a step that stopped because the run is shutting down. It is
// not a product failure, so main never records it as one.
function abortError(stage) {
  const error = new Error(`acceptance stopped at ${stage} after ${abortedSignal ?? 'shutdown'}`);
  error.code = 'run_aborted';
  error.stage = stage;
  return error;
}

function assertAcceptanceRunning(stage) {
  if (abortedSignal) throw abortError(stage);
}

// acceptanceStep is the continuation boundary. Checking only before a step is
// not enough: an abort can arrive while the step is awaited (a process being
// stopped, a container going away), and without the check after the await the
// next step would start its own resources after cleanup already snapshotted the
// registry. Both checks are synchronous with the step, so no producer between
// them can slip past the registry fence.
async function acceptanceStep(stage, run) {
  assertAcceptanceRunning(stage);
  const value = await run();
  assertAcceptanceRunning(stage);
  return value;
}

// ---------------------------------------------------------------------------
// entry point
// ---------------------------------------------------------------------------

if (isEntryPoint) {
  if (args.verifyEvidence) {
    process.exitCode = verifyEvidenceCli(args);
  } else {
    main().then(
      (result) => {
        console.log(result.exitCode === 0 ? 'upgrade-docker: PASS' : 'upgrade-docker: FAIL');
        process.exit(result.exitCode);
      },
      (error) => {
        console.error(`upgrade-docker: ${redactor.redact(error?.message || error)}`);
        if (error?.stack) console.error(redactor.redact(error.stack));
        process.exit(1);
      },
    );
  }
}

async function main() {
  if (process.env[UPGRADE_DOCKER_CONTRACT.skipVariable] === '1') {
    console.log(`SKIP: ${UPGRADE_DOCKER_CONTRACT.skipVariable}=1 was set explicitly; the real-Docker Upgrade acceptance did not run`);
    return { exitCode: 0 };
  }
  started = Date.now();
  evidence.startedAt = new Date(started).toISOString();
  fs.mkdirSync(SCRATCH, { recursive: true });
  fs.mkdirSync(EVIDENCE_DIR, { recursive: true });
  evidence.implementation = captureImplementationIdentity();
  if (!evidence.implementation.valid) {
    throw new Error(`the implementation binding is incomplete: ${evidence.implementation.invalid.join(', ')}`);
  }
  // The signal handler has to exist before the first object is created, so an
  // interrupt during the build or the seed still releases what exists. It stays
  // attached through finalisation: a signal while the evidence is being written
  // joins that same release instead of terminating without one.
  const guard = installSignalGuard({ onSignal: (signal) => abortRun(signal) });
  let runOk = false;
  try {
    const acceptance = runAcceptance();
    // Cleanup releases what the in-flight step may be waiting on; this barrier
    // is what lets it wait for that step to stop before it calls the host clean.
    // The promise never rejects: main below is the one that reports the error.
    acceptanceSettled = acceptance.then(
      () => null,
      (error) => ({ code: error?.code ?? null, stage: error?.stage ?? null }),
    );
    runOk = await acceptance;
  } catch (error) {
    if (abortedSignal) {
      const stage = error?.stage ? ` at ${error.stage}` : '';
      const detail = error?.code === 'run_aborted' ? '' : `: ${redactor.redact(error?.message || String(error))}`;
      console.log(`upgrade-docker: acceptance stopped by ${abortedSignal}${stage}${detail}`);
      if (error?.code !== 'run_aborted') evidence.abortedError = redactor.redact(error?.message || String(error));
    } else {
      runFailure = `${error?.message || error}`;
      console.error(`upgrade-docker: ${redactor.redact(runFailure)}`);
      if (error?.stack) console.error(redactor.redact(error.stack));
    }
  }
  const outcome = await finalize({ runOk });
  guard.detach();
  return { exitCode: outcome.exitCode };
}

async function runAcceptance() {
  requireDocker();
  hostBeforeRun = hostSnapshot();
  refusePrefixCollision();
  recordProductFixes();

  const binary = buildManagerBinary();
  const nextFixture = await acceptanceStep('fixture', () => ensureFixture(args.v1Tag));
  const nextSource = await acceptanceStep('seed', () => seedSource(nextFixture));

  // Read the release the running contract accepts from the product itself, so
  // no second copy of the pinned version lives in this harness. The probe is a
  // preflight, so it declares the tag it actually reads; the report's
  // `expected=` value is then the single declared version every later command
  // receives.
  acceptedRelease = args.v1Tag;
  const probe = runCli(binary, ['upgrade', 'preflight', '--legacy', nextSource.sourceRoot, '--json'], 'preflight(next-fixture)');
  const expected = expectedReleaseOf(probe) || args.sourceTag;
  if (!RELEASE_TAG_PATTERN.test(String(expected ?? ''))) {
    throw new Error(`could not read the release the contract accepts from the preflight report (got ${JSON.stringify(expected)})`);
  }
  acceptedRelease = expected;
  evidence.mode.acceptedRelease = expected;
  evidence.mode.declaredSourceVersion = acceptedRelease;

  let sourceFixture = nextFixture;
  let source = nextSource;
  if (expected !== args.v1Tag) {
    // The planned fixture is newer than what the contract accepts. The next
    // re-pin (U1-1b) is what closes this, so the harness records the typed
    // refusal and proves the chain against the accepted release instead of
    // pretending the planned one works.
    assertVersionGap(binary, probe, expected, nextFixture, nextSource);
    sourceFixture = await acceptanceStep('accepted-fixture', () => ensureFixture(expected));
    source = await acceptanceStep('accepted-seed', () => seedSource(sourceFixture));
    stableFacts.push(`version-gap:${args.v1Tag}>${expected}`);
  } else {
    evidence.mode.sourceFixture = 'same';
  }
  evidence.fixtures.push({ role: 'source', ...sourceFixture, dataset: source.dataset });
  if (sourceFixture.tag !== nextFixture.tag) {
    evidence.fixtures.push({ role: 'next', ...nextFixture, dataset: nextSource.dataset });
  }

  const workspace = createWorkspace(source);
  for (const [name, dir] of Object.entries(workspace.snapshots)) {
    stableFacts.push(`snapshot:${name}`);
    evidence[`snapshot_${name}`] = dir;
  }

  // The seeded shape: the archived day's raw rows are deleted, so the retained
  // domain holds two days, coverage names the one deleted day, and the two
  // derived domains cover all three.
  assertAcceptanceRunning('chain');
  const primary = chain(binary, workspace.main.source, workspace.main.target, 'main', {
    sourceDays: {
      retained: DATASET_DAYS.length - 1,
      pricing: DATASET_DAYS.length,
      coverage: 1,
      history: DATASET_DAYS.length,
    },
    domainsDone: CHAIN_DOMAINS_DONE,
  });
  evidence.chains.main = primary;
  assertAcceptanceRunning('chain');

  await acceptanceStep('wizard', () => assertWizard(binary, workspace.main.source));
  await acceptanceStep('admission', () => assertAdmission(binary, workspace));

  await acceptanceStep('fault:source-touched', () => faultSourceTouched(binary, workspace));
  await acceptanceStep('fault:interrupted-copy-in', () => faultInterruptedCopyIn(binary, workspace));
  await acceptanceStep('fault:rollup-recoverable', () => faultClearedRollupRecoverable(binary, workspace));
  await acceptanceStep('fault:rollup-unrecoverable', () => faultClearedRollupUnrecoverable(binary, workspace));
  await acceptanceStep('fault:dropped-table', () => faultDroppedTable(binary, workspace));
  await acceptanceStep('fault:busy-source', () => faultBusySource(binary, workspace));

  await acceptanceStep('rollback', () => proveRollback(binary, workspace, sourceFixture));

  return true;
}

// ---------------------------------------------------------------------------
// finalisation: cleanup, evidence, verdict
// ---------------------------------------------------------------------------

async function abortRun(signal) {
  // Everything before the first await is the synchronous part of shutdown: the
  // fence must be closed in the signal's own turn, not after a log write or a
  // lock, so no resumed step can create between the signal and the release.
  if (!abortedSignal) abortedSignal = signal;
  registry.fence(`aborted by ${abortedSignal}`);
  console.log(`upgrade-docker: received ${signal}; releasing this run's resources before exiting`);
  return finalize({ runOk: false, signal: abortedSignal });
}

function finalize({ runOk, signal = null }) {
  if (!finalizing) {
    if (signal) abortedSignal = signal;
    // The normal and failure paths cross the same creation boundary. A signal
    // that arrives later joins this promise instead of releasing twice.
    registry.fence(abortedSignal ? `aborted by ${abortedSignal}` : 'finalizing the run');
    finalizing = runFinalize({ runOk, signal });
  }
  return finalizing;
}

async function runFinalize({ runOk, signal }) {
  if (signal) abortedSignal = signal;
  // Releases run first: stopping a process or a container is often what lets an
  // interrupted step return. cleanupResources then waits for that continuation
  // before it decides the host is clean. The fence is already closed, so the
  // wait cannot be extended by fresh creation.
  const cleanup = await cleanupResources(acceptanceSettled);
  // The comparison belongs after the release: "this run created nothing that
  // survives, and nothing outside the prefix moved" is only true then.
  if (hostBeforeRun && evidence.host.docker) {
    try {
      assertHostUntouched(hostBeforeRun);
    } catch (error) {
      cleanup.ok = false;
      cleanup.errors.push(redactor.redact(error?.message || error));
    }
  }
  evidence.cleanup = cleanup;
  if (runFailure) evidence.failure = redactor.redact(runFailure);
  evidence.verdict = abortedSignal ? 'aborted' : runOk && cleanup.ok ? 'pass' : 'fail';
  if (abortedSignal) evidence.abortedBy = abortedSignal;
  evidence.exitCode = finalExitCode({ runOk, cleanupOk: cleanup.ok, signal: abortedSignal });
  evidence.endedAt = new Date().toISOString();
  evidence.durationMs = started ? Date.now() - started : 0;
  try {
    writeEvidence();
  } catch (error) {
    console.error(`upgrade-docker: writing the evidence failed: ${redactor.redact(error?.message || error)}`);
    evidence.exitCode = abortedSignal ? evidence.exitCode : Math.max(1, evidence.exitCode);
  }
  return { exitCode: evidence.exitCode, cleanup };
}

// captureImplementationIdentity binds the document to the bytes that produced it:
// the commit, the harness file, the built binary and whether the tree was clean.
function captureImplementationIdentity() {
  const head = git(['rev-parse', 'HEAD']);
  const branch = git(['rev-parse', '--abbrev-ref', 'HEAD']);
  const status = git(['status', '--porcelain']);
  let harnessSha256 = null;
  try {
    harnessSha256 = sha256(fs.readFileSync(fileURLToPath(import.meta.url)));
  } catch {
    harnessSha256 = null;
  }
  const binaryPath = managerBinary();
  let binarySha256 = null;
  try {
    binarySha256 = fs.existsSync(binaryPath) ? sha256(fs.readFileSync(binaryPath)) : null;
  } catch {
    binarySha256 = null;
  }
  return createImplementationIdentity({
    headSha: head.code === 0 ? head.stdout.trim() : null,
    branch: branch.code === 0 ? branch.stdout.trim() : null,
    harnessSha256,
    binarySha256,
    treeClean: status.code === 0 ? status.stdout.trim() === '' : null,
  });
}

// ---------------------------------------------------------------------------
// guards
// ---------------------------------------------------------------------------

function requireDocker() {
  const version = sh('docker', ['version', '--format', '{{.Server.Version}}']);
  if (version.code !== 0) {
    throw new Error(`docker is unavailable; the real-Docker Upgrade acceptance cannot run (${version.stderr.trim()})`);
  }
  evidence.host.docker = version.stdout.trim();
}

// dockerNames lists one kind of Docker object, or null when the listing itself
// failed: a cleanup that cannot see the host cannot claim the host is clean.
function dockerNames(commandArgs) {
  const result = sh('docker', commandArgs);
  if (result.code !== 0) return null;
  return result.stdout
    .split('\n')
    .map((line) => line.trim())
    .filter(Boolean);
}

function refusePrefixCollision() {
  for (const [kind, list] of [
    ['container', dockerJson(['ps', '-a', '--format', '{{.Names}}'])],
    ['volume', dockerJson(['volume', 'ls', '--format', '{{.Name}}'])],
    ['network', dockerJson(['network', 'ls', '--format', '{{.Name}}'])],
    ['image', dockerJson(['image', 'ls', '--format', '{{.Repository}}:{{.Tag}}'])],
  ]) {
    const collision = list.find((name) => name.startsWith(PREFIX));
    if (collision) {
      throw new Error(`the ${kind} ${collision} already uses this run's prefix ${PREFIX}; refusing to reuse an object this run did not create`);
    }
  }
}

// sharedImageReferences names the pre-existing image tags this harness must
// leave alone: the shared rollback repository older builds wrote to. The fixture
// repository may be rebuilt (that is what a non-reuse run is for) and the
// run-owned alias is this run's own, so neither belongs here.
function sharedImageReferences(tags) {
  const references = new Set();
  for (const tag of tags) {
    const value = String(tag ?? '').trim();
    if (!RELEASE_TAG_PATTERN.test(value)) continue;
    const version = value.slice(1);
    references.add(`${SHARED_ROLLBACK_IMAGE_REPOSITORY}:${version}`);
  }
  return [...references].sort();
}

// sharedImageSnapshot records the image ID behind each shared reference, or
// null when the tag does not exist on this host. Only the non-null entries can
// be asserted unchanged: a fixture this run builds did not exist before it.
function sharedImageSnapshot(references) {
  return references.map((reference) => ({ reference, imageId: imageId(reference) }));
}

// assertSharedImagesUnchanged proves two things by inspect, never by a tag
// command: every pre-existing shared image still resolves to the same ID, and
// no run-owned alias survived cleanup.
function assertSharedImagesUnchanged(before) {
  const changed = [];
  let checked = 0;
  for (const entry of before) {
    if (!entry.imageId) continue;
    checked += 1;
    const now = imageId(entry.reference);
    if (now !== entry.imageId) {
      changed.push(`image ${entry.reference} changed: was ${entry.imageId}, now ${now ?? 'missing'}`);
    }
  }
  const references = dockerNames(['image', 'ls', '--format', '{{.Repository}}:{{.Tag}}']);
  if (references === null) {
    throw new Error("cannot list the host's images after cleanup, so the pre-existing images cannot be shown unchanged");
  }
  const leftovers = references.filter((reference) => isOwnedImageAlias(PREFIX, reference));
  for (const reference of leftovers) changed.push(`image ${reference} survived the release boundary`);
  return { changed, checked, leftovers };
}

function hostSnapshot() {
  return {
    containers: dockerJson(['ps', '-a', '--format', '{{.Names}}']).sort(),
    volumes: dockerJson(['volume', 'ls', '--format', '{{.Name}}']).sort(),
    networks: dockerJson(['network', 'ls', '--format', '{{.Name}}']).sort(),
    images: sharedImageSnapshot(sharedImageReferences([args.v1Tag, args.sourceTag])),
  };
}

// assertHostUntouched proves the harness only ever addressed its own prefix: the
// containers, volumes and networks that existed before the run are exactly the
// ones that exist after it.
function assertHostUntouched(before) {
  const after = hostSnapshot();
  const outside = (list) => list.filter((name) => !name.startsWith(PREFIX));
  const changed = [];
  for (const kind of ['containers', 'volumes', 'networks']) {
    const listedBefore = outside(before[kind] ?? []);
    const listedAfter = outside(after[kind] ?? []);
    if (listedBefore.join('\n') !== listedAfter.join('\n')) {
      changed.push(`${kind} outside ${PREFIX} changed: ${diff(listedBefore, listedAfter)}`);
    }
    evidence.hostUntouched = {
      ...(evidence.hostUntouched ?? {}),
      [kind]: listedBefore.length,
    };
  }
  const images = assertSharedImagesUnchanged(before.images ?? []);
  evidence.hostUntouched = {
    ...(evidence.hostUntouched ?? {}),
    images: images.checked,
    runOwnedImageAliases: images.leftovers.length,
  };
  changed.push(...images.changed);
  if (changed.length) throw new Error(changed.join('; '));
  stableFacts.push(`host-untouched:images=${images.checked}`);
}

function diff(before, after) {
  const removed = before.filter((name) => !after.includes(name));
  const added = after.filter((name) => !before.includes(name));
  return `removed=[${removed.join(',')}] added=[${added.join(',')}]`;
}

// recordProductFixes names the product defects this harness found, with the
// regression that keeps each closed. They are recorded in every run so the
// evidence says what the chain could not have proved before them.
function recordProductFixes() {
  evidence.findings.push({
    code: 'upgrade_source_version_ladder',
    status: 'fixed',
    detail:
      'the deleted-raw summary table was listed as a v1.14.6 marker although every 1.14.x release creates it, so a real source of the pinned release was reported newer than the pin and refused with upgrade_source_version_unsupported: no real 1.x install could pass preflight',
    file: 'apps/manager-server/internal/service/upgrade/schema.go:366',
    regression: 'apps/manager-server/internal/service/upgrade/preflight_test.go TestSourceVersionAcceptsThePinnedReleaseSchema',
  });
  evidence.findings.push({
    code: 'upgrade_copy_in_interrupted_migration',
    status: 'fixed',
    detail:
      'a copy-in killed after materialize and before the operation row committed left its provenance marker, but the resume compared the migration-rewritten database sidecars with the backup, deleted the marker and then refused to replace the members it had already moved, so the window the marker exists for could not be resumed',
    file: 'apps/manager-server/internal/service/upgrade/copyin.go:657',
    regression: 'apps/manager-server/internal/service/upgrade/copyin_test.go TestCopyInResumesAfterAnInterruptionInsideTheMigration',
  });
  evidence.findings.push({
    code: 'upgrade_wizard_unreachable_after_copy_in',
    status: 'fixed',
    detail:
      'an unfinished operation admits only the fixed Bootstrap surface, and the copy-in target replaces usage.sqlite so it carries no Fresh record for the canonical continuation: both wizard read routes answered 404 while the wizard had a stage recorded, so the operator could never see the copy_in or reconcile the process had just written. The two reads are now mounted beside the Upgrade entry leaf on the fixed surface, under the same gate and the same source credential, and retire with completion',
    file: 'apps/manager-server/cmd/cpa-manager-plus/startup_discovery.go:73',
    regression: 'apps/manager-server/cmd/cpa-manager-plus/startup_upgrade_wizard_test.go TestStartupServesTheUpgradeWizardFromTheCopiedOperation',
  });
  evidence.findings.push({
    code: 'upgrade_wizard_unreachable_on_a_refused_mount',
    status: 'fixed',
    detail:
      'the mount lookup counted a mount\'s usage.sqlite with os.Stat, which follows a symbolic link: the mount looked like a source, the credential read then refused the linked file under the shared database file policy, and the entry page and both wizard reads answered 503, so the operator could not reach the page that names the condition. The mount question now asks the credential read\'s own inspection through one MountDatabase lookup, so a mount the policy refuses holds no database for the surface: the credential resolves to the Manager store, the surface stays reachable, and status and preflight report upgrade_source_symlinked for that mount',
    file: 'apps/manager-server/internal/service/upgrade/preflight.go:397',
    regression: 'apps/manager-server/cmd/cpa-manager-plus/startup_upgrade_surface_test.go TestStartupUpgradeSurfaceAcceptsOneCredentialEverywhere',
  });
}

// ---------------------------------------------------------------------------
// fixtures
// ---------------------------------------------------------------------------

function buildManagerBinary() {
  const out = path.join(SCRATCH, 'cpa-manager-plus');
  const build = sh('go', ['build', '-o', out, './cmd/cpa-manager-plus'], { cwd: MANAGER_SERVER });
  if (build.code !== 0) {
    throw new Error(`build the v2 binary: ${redactor.redact(tail(build.stderr))}`);
  }
  evidence.commands.push(commandRecord('go build ./cmd/cpa-manager-plus', build.code));
  // The binary is half the implementation binding: it is the thing that ran.
  evidence.implementation = captureImplementationIdentity();
  return out;
}

// A fixture image is reusable only when it can be shown to be the image this
// harness built from that tag: the labels are written at build time and cover
// the contract, the tag and the commit the image was built from.
function fixtureLabels(tag, commit) {
  return {
    'io.cpamp.upgrade.fixture.contract': UPGRADE_DOCKER_CONTRACT.fixtureContract,
    'io.cpamp.upgrade.fixture.tag': tag,
    'io.cpamp.upgrade.fixture.commit': commit,
  };
}

function imageLabels(image) {
  const result = sh('docker', ['image', 'inspect', '-f', '{{json .Config.Labels}}', image]);
  if (result.code !== 0) return null;
  try {
    return JSON.parse(result.stdout.trim() || 'null') ?? {};
  } catch {
    return null;
  }
}

function assertFixtureProvenance(tag, commit, labels) {
  for (const [key, value] of Object.entries(fixtureLabels(tag, commit))) {
    assertValue(
      labels?.[key] === value,
      `the image behind ${IMAGE_REPOSITORY}:${tag} does not carry ${key}=${value}; only this harness's own cpampd- fixture build can be presented as the recorded fixture, so the image is refused instead of being rebuilt over`,
    );
  }
}

function resolveTagCommit(tag) {
  const resolved = git(['rev-parse', `${tag}^{commit}`]);
  assertValue(resolved.code === 0, `resolve ${tag} to a commit: ${redactor.redact(resolved.stderr.trim())}`);
  const commit = resolved.stdout.trim();
  assertValue(SHA1_PATTERN.test(commit), `resolve ${tag} to a commit: got ${JSON.stringify(commit)}`);
  return commit;
}

// connectRollbackAlias makes this run's rollback reference resolve to exactly
// the fixture bytes. An alias that already resolves elsewhere is refused rather
// than repointed: an image whose provenance this run cannot show must not be
// pinned. The alias is tracked before it is created, so shutdown refuses the tag
// and cleanup removes only this run's reference.
function connectRollbackAlias(fixture) {
  const alias = fixture.rollbackImage;
  if (!isOwnedImageAlias(PREFIX, alias)) {
    throw new Error(`refusing to create ${alias}: it is not this run's rollback alias under ${PREFIX}/upgrade`);
  }
  registry.assertCreationAllowed('image', alias);
  const existing = imageId(alias);
  if (existing && existing !== fixture.imageId) {
    throw new Error(
      `the rollback alias ${alias} resolves to ${existing}, not to the fixture ${fixture.imageId}; refusing to retag an image this run cannot account for`,
    );
  }
  if (!existing) {
    const tagged = dockerImageWrite(['tag', fixture.image, alias]);
    assertValue(tagged.code === 0, `tag ${alias}: ${redactor.redact(tagged.stderr.trim())}`);
    evidence.commands.push(commandRecord(`docker tag ${fixture.image} ${alias}`, tagged.code));
  }
  const resolved = imageId(alias);
  assertValue(resolved === fixture.imageId, `the rollback alias ${alias} resolves to ${resolved}, not to ${fixture.imageId}`);
  // The alias resolves to the fixture bytes, so it must carry the same labels;
  // checking them here is what makes "this run's provenance" an observed fact
  // instead of a claim about the tag name.
  const labels = imageLabels(alias);
  assertFixtureProvenance(fixture.tag, fixture.commit, labels);
  registry.track('image', alias, () => releaseImageAlias(alias, fixture.imageId), { imageId: fixture.imageId });
  return {
    ...fixture,
    rollbackImageId: resolved,
    provenance: { labels: fixtureLabels(fixture.tag, fixture.commit), aliasResolvesToFixture: true },
    rollbackAlias: { reference: alias, repository: ROLLBACK_ALIAS_REPOSITORY, tag: alias.slice(alias.lastIndexOf(':') + 1) },
  };
}

// ensureFixture builds the 1.x image from the tag once and records its id and
// source commit, so the same fixture is reused instead of rebuilt per test. A
// reuse verifies the recorded provenance instead of trusting the local tag.
async function ensureFixture(tag) {
  const version = tag.replace(/^v/, '');
  const image = `${IMAGE_REPOSITORY}:${version}`;
  // The rollback plan pins a repository-qualified reference at an exact
  // vX.Y.Z tag. The alias is run-owned, so the pre-existing shared tags stay
  // untouched even when they already hold an old image.
  const rollbackImage = rollbackAliasReference(tag);
  const commit = resolveTagCommit(tag);
  const known = imageId(image);
  if (args.reuseFixture) {
    if (!known) {
      throw new Error(
        `--reuse-fixture was asked for but ${image} is not on this host; build it once without the flag so its provenance is recorded`,
      );
    }
    const labels = imageLabels(image);
    assertFixtureProvenance(tag, commit, labels);
    return connectRollbackAlias({ tag, commit, image, rollbackImage, imageId: known, built: false });
  }
  const source = path.join(SCRATCH, `fixture-${tag}`);
  fs.rmSync(source, { recursive: true, force: true });
  registry.track('worktree', source, () => releaseWorktree(source));
  const add = git(['worktree', 'add', '--detach', source, tag]);
  if (add.code !== 0) {
    throw new Error(`check out ${tag}: ${redactor.redact(tail(add.stderr))}`);
  }
  const build = dockerImageWrite(
    [
      'build',
      '-f', 'Dockerfile.manager-server',
      '-t', image,
      '--build-arg', `VERSION=${tag}`,
      '--build-arg', `SOURCE_COMMIT=${commit}`,
      ...Object.entries(fixtureLabels(tag, commit)).flatMap(([key, value]) => ['--label', `${key}=${value}`]),
      '.',
    ],
    { cwd: source, timeout: 1_800_000 },
  );
  evidence.commands.push(commandRecord(`docker build ${image}`, build.code));
  if (build.code !== 0) {
    throw new Error(`build the ${tag} fixture image: ${redactor.redact(tail(build.stderr))}`);
  }
  const builtImageId = imageId(image);
  assertValue(Boolean(builtImageId), `the ${tag} fixture build did not produce ${image} on this host`);
  return connectRollbackAlias({ tag, commit, image, rollbackImage, imageId: builtImageId, built: true });
}

function releaseWorktree(directory) {
  if (!fs.existsSync(directory)) {
    sh('git', ['worktree', 'prune']);
    return;
  }
  const removal = sh('git', ['worktree', 'remove', '--force', directory]);
  if (removal.code !== 0) {
    throw new Error(`git worktree remove ${directory}: ${redactor.redact(removal.stderr.trim())}`);
  }
  sh('git', ['worktree', 'prune']);
}

// seedSource starts the fixture on an isolated port with a data directory this
// harness owns, seeds a realistic 1.x dataset through the 1.x API, snapshots the
// two states the fault cases need, and stops it cleanly.
async function seedSource(fixture) {
  const root = path.join(SCRATCH, `source-${fixture.tag}`);
  fs.rmSync(root, { recursive: true, force: true });
  fs.mkdirSync(root, { recursive: true });
  const port = ports.take();
  const name = `${PREFIX}-seed-${fixture.tag.replace(/[^a-z0-9]/gi, '')}`;
  startContainer({
    name,
    image: fixture.image,
    port,
    dataDir: root,
    adminKey: ADMIN_V1,
  });
  await waitForHealth(port, `${name} /health`);

  const keyHashes = DATASET_KEYS.map((key) => sha256(Buffer.from(key.name)));
  const events = buildEvents(keyHashes);
  const imported = await v1Json(port, 'POST', '/v0/management/usage/import', events);
  assertValue(imported.body?.added === events.length, `seed usage events: added=${imported.body?.added} want ${events.length}`);
  const prices = await v1Json(port, 'PUT', '/v0/management/model-prices', { prices: DATASET_PRICES });
  assertValue(prices.status === 200, `seed model prices: ${prices.status}`);
  const aliases = await v1Json(port, 'PUT', '/v0/management/api-key-aliases', {
    items: DATASET_KEYS.map((key, index) => ({ apiKeyHash: keyHashes[index], alias: key.alias })),
  });
  assertValue(aliases.status === 200, `seed api key aliases: ${aliases.status}`);

  // One archive run over the first day, then verify and delete the raw rows:
  // a real 1.x install ends up with archive segments and recorded coverage.
  const run = await v1Json(port, 'POST', '/v0/management/usage/archives', {
    cutoff_timestamp_ms: dayStartMS(DATASET_DAYS[1]),
  });
  const runIdValue = run.body?.run?.id;
  assertValue(Boolean(runIdValue), `archive run id: ${preview(run.body)}`);
  const archived = await v1Json(port, 'POST', `/v0/management/usage/archives/${runIdValue}/resume?expected_stage=archiving&wait=true`);
  assertValue(archived.body?.run?.status === 'archived', `archive run status: ${archived.body?.run?.status}`);

  const archivedSnapshot = path.join(SCRATCH, `source-archived-${fixture.tag}`);
  stopContainer(name);
  await waitForContainerExit(name);
  // The seed ran in a container. Before the host copies the tree it wrote, the
  // tree is given back to this process, so the copy is a normal read instead of
  // a permission failure — or, on Node's native recursive copy, a native abort.
  normalizeScratchOwnership(root, 'seed-archived');
  copyTree(root, archivedSnapshot);
  await removeContainer(name);

  startContainer({ name, image: fixture.image, port, dataDir: root, adminKey: ADMIN_V1 });
  await waitForHealth(port, `${name} /health`);
  const verified = await v1Json(port, 'POST', `/v0/management/usage/archives/${runIdValue}/verify?wait=true`);
  assertValue(verified.body?.run?.status === 'verified', `archive verify status: ${verified.body?.run?.status}`);
  const deleted = await v1Json(port, 'POST', `/v0/management/usage/archives/${runIdValue}/delete?wait=true`);
  assertValue(deleted.body?.run?.status === 'completed', `archive delete status: ${deleted.body?.run?.status}`);

  const counts = v1Counts(port, events.length);
  stopContainer(name);
  await waitForContainerExit(name);
  normalizeScratchOwnership(root, 'seed-final');
  await removeContainer(name);

  const dataset = {
    events: events.length,
    days: DATASET_DAYS,
    models: DATASET_MODELS.map((entry) => entry.model),
    apiKeys: DATASET_KEYS.length,
    prices: Object.keys(DATASET_PRICES).length,
    archivedSegments: archived.body?.segments?.length ?? null,
    archivedEvents: archived.body?.run?.archived_event_count ?? null,
    ...counts,
    sourceSha256: dirDigest(root),
  };
  return { fixture, sourceRoot: root, archivedRoot: archivedSnapshot, dataset };
}

// buildEvents is the fixed dataset: every hour of every day for both models,
// alternating between the two client keys.
function buildEvents(keyHashes) {
  const events = [];
  DATASET_DAYS.forEach((day, dayIndex) => {
    DATASET_HOURS.forEach((hour, hourIndex) => {
      DATASET_MODELS.forEach((entry, modelIndex) => {
        const second = (hourIndex * 3 + modelIndex * 7) % 60;
        const timestampMS = Date.parse(`${day}T${String(hour).padStart(2, '0')}:00:${String(second).padStart(2, '0')}Z`);
        const apiKey = keyHashes[(dayIndex + hourIndex + modelIndex) % keyHashes.length];
        const input = 100 + dayIndex * 10 + hourIndex * 3 + modelIndex;
        const output = 50 + hourIndex * 2 + modelIndex;
        events.push({
          event_hash: sha256(Buffer.from(`u1d:${day}:${hour}:${entry.model}`)),
          timestamp_ms: timestampMS,
          timestamp: new Date(timestampMS).toISOString(),
          model: entry.model,
          provider: entry.provider,
          executor_type: entry.executor,
          api_key_hash: apiKey,
          input_tokens: input,
          output_tokens: output,
          cached_tokens: 10 * (modelIndex + 1),
          cache_read_tokens: 0,
          cache_creation_tokens: 0,
          total_tokens: input + output,
          cost: 0.0021 * (dayIndex + 1),
          failed: false,
          stream: true,
        });
      });
    });
  });
  return events;
}

function v1Counts(port, expectedEvents) {
  // Read the seeded state back through the 1.x API, so the dataset is proven by
  // the product rather than by the payload this harness sent.
  const archives = httpJson('GET', `http://127.0.0.1:${port}/v0/management/usage/archives?limit=10`, {
    headers: { Authorization: `Bearer ${ADMIN_V1}` },
  });
  const archivedSegments = (archives.body?.runs ?? []).reduce((sum, run) => sum + (run.archived_event_count ?? 0), 0);
  return {
    importedEvents: expectedEvents,
    archivedRunEvents: archivedSegments,
    archiveSegments: (archives.body?.runs ?? []).length,
  };
}

// ---------------------------------------------------------------------------
// the v2 chain
// ---------------------------------------------------------------------------

// createWorkspace lays out the working copies the chain and the fault cases
// need: the main pair, a copy per fault, and the two snapshots of the seeded
// source (with and without raw-deleted days).
function createWorkspace(source) {
  const workspace = {
    main: {
      source: path.join(SCRATCH, 'work', 'main', 'source'),
      target: path.join(SCRATCH, 'work', 'main', 'target'),
    },
    deleted: path.join(SCRATCH, 'work', 'deleted'),
    archived: path.join(SCRATCH, 'work', 'archived'),
  };
  fs.mkdirSync(path.dirname(workspace.main.source), { recursive: true });
  copyTree(source.sourceRoot, workspace.main.source);
  fs.mkdirSync(workspace.main.target, { recursive: true });
  fs.mkdirSync(path.dirname(workspace.deleted), { recursive: true });
  copyTree(source.sourceRoot, workspace.deleted);
  copyTree(source.archivedRoot, workspace.archived);
  return {
    ...workspace,
    snapshots: {
      source: source.sourceRoot,
      archived: source.archivedRoot,
    },
  };
}

// The four comparison domains, in report order, and the store table each one
// compares. A report that does not name all four did not compare what the copy
// claims to preserve.
const RECONCILE_DOMAINS = Object.freeze(['retained', 'pricing', 'coverage', 'history']);
const RECONCILE_DOMAIN_TABLE = Object.freeze({
  retained: TABLES.retained,
  pricing: TABLES.pricing,
  coverage: TABLES.coverage,
  history: TABLES.history,
});
// The operation-domain vocabulary a full chain records. The row is the
// authority here: a reconcile that compared nothing leaves it untouched.
const CHAIN_DOMAINS_DONE = Object.freeze(['source_copy', 'retained_projection', 'pricing', 'coverage']);

// assertReconcileReport proves the comparison really happened: all four domains
// present on the table each one names, each applicable, every source day
// compared and zero mismatches. `sourceDays` pins the seeded shape where the
// caller knows it; `domainsDone` pins the recorded operation vocabulary where the
// caller knows the whole chain. Without it, every domain with a compared day has
// to be recorded, and the two copy-in domains are always required: a run that
// compared nothing must not read as a completed comparison.
function assertReconcileReport(label, report, { sourceDays = null, domainsDone = null } = {}) {
  assertValue(report?.stage === 'reconcile', `${label}: reconcile stage=${report?.stage}`);
  assertValue((report?.unacknowledgedDays ?? []).length === 0, `${label}: reconcile left unacknowledged days`);
  const domains = report?.domains ?? [];
  const names = domains.map((domain) => domain.domain);
  assertValue(
    names.join(',') === RECONCILE_DOMAINS.join(','),
    `${label}: reconcile compared ${JSON.stringify(names)}; ${JSON.stringify(RECONCILE_DOMAINS)} is required, in report order`,
  );
  for (const domain of domains) {
    assertValue(
      domain.table === RECONCILE_DOMAIN_TABLE[domain.domain],
      `${label}: ${domain.domain} compared ${domain.table} instead of ${RECONCILE_DOMAIN_TABLE[domain.domain]}`,
    );
    assertValue(
      domain.applicable === true,
      `${label}: ${domain.domain} is not applicable (${domain.reason ?? 'no reason'}); a seeded domain has to be compared`,
    );
    assertValue(
      domain.comparedDays === domain.sourceDays,
      `${label}: ${domain.domain} compared ${domain.comparedDays} of ${domain.sourceDays} source days`,
    );
    assertValue(
      domain.mismatches.length === 0,
      `${label}: ${domain.domain} reported ${domain.mismatches.length} mismatch(es) instead of none`,
    );
    if (sourceDays) {
      assertValue(
        domain.sourceDays === sourceDays[domain.domain],
        `${label}: ${domain.domain} sourceDays=${domain.sourceDays} want ${sourceDays[domain.domain]} from the seed`,
      );
    }
  }
  const recorded = report?.domainsDone ?? [];
  if (domainsDone) {
    assertValue(
      recorded.join(',') === domainsDone.join(','),
      `${label}: domainsDone=${JSON.stringify(recorded)} want ${JSON.stringify(domainsDone)}`,
    );
  } else {
    for (const required of ['source_copy', 'retained_projection']) {
      assertValue(recorded.includes(required), `${label}: the recorded row does not name the ${required} domain`);
    }
    for (const comparison of ['pricing', 'coverage']) {
      const domain = domains.find((entry) => entry.domain === comparison);
      if ((domain?.comparedDays ?? 0) > 0) {
        assertValue(recorded.includes(comparison), `${label}: the recorded row does not name the ${comparison} domain it compared`);
      }
    }
  }
  assertValue((report?.incompleteDays ?? []).length === 0, `${label}: reconcile marked an incomplete day`);
  return { domains, domainsDone: recorded };
}

// chain runs the whole CLI chain and asserts each exit code and report.
function chain(binary, sourceRoot, targetRoot, label, { sourceDays = null, domainsDone = null } = {}) {
  const preflight = runCli(binary, ['upgrade', 'preflight', '--legacy', sourceRoot, '--json'], `${label}:preflight`, { expectExit: 0 });
  const preflightReport = parseJson(preflight.stdout, `${label}:preflight`);
  assertValue(preflightReport.ready === true, `${label}: preflight ready=false (blocked=${JSON.stringify(preflightReport.blocked)})`);
  assertValue(preflightReport.checks.every((check) => check.status !== 'blocked'), `${label}: preflight has a blocked check`);

  const run = runCli(binary, ['upgrade', 'run', '--legacy', sourceRoot, '--target', targetRoot, '--json'], `${label}:run`, { expectExit: 0 });
  const runReport = parseJson(run.stdout, `${label}:run`);
  assertValue(runReport.result?.stage === 'copy_in', `${label}: run stage=${runReport.result?.stage}`);
  assertValue(runReport.result?.restored === true, `${label}: run restored=${runReport.result?.restored}`);
  // The next command the run prints is the reconciliation the operator can
  // actually run. Exact tokens, not a substring: the source, the target and the
  // declared release are each one argument, and no extra word can hide in them.
  assertValue(
    typeof runReport.next === 'string' && runReport.next !== '',
    `${label}: run printed no reconcile next step: ${JSON.stringify(runReport.next)}`,
  );
  assertValue(
    JSON.stringify(shellTokens(runReport.next)) ===
      JSON.stringify([
        'cpa-manager-plus',
        'upgrade',
        'reconcile',
        '--legacy',
        sourceRoot,
        '--target',
        targetRoot,
        '--source-version',
        acceptedRelease,
      ]),
    `${label}: the printed next step is not the exact reconcile command for this source and release: ${JSON.stringify(runReport.next)}`,
  );

  const reconcile = runCli(binary, ['upgrade', 'reconcile', '--legacy', sourceRoot, '--target', targetRoot, '--json'], `${label}:reconcile`, { expectExit: 0 });
  const reconcileReport = parseJson(reconcile.stdout, `${label}:reconcile`);
  assertReconcileReport(`${label}:reconcile`, reconcileReport.report, { sourceDays, domainsDone });

  const status = runCli(binary, ['upgrade', 'status', '--db-path', path.join(targetRoot, 'usage.sqlite'), '--json'], `${label}:status`, { expectExit: 0 });
  const statusReport = parseJson(status.stdout, `${label}:status`);
  assertValue(statusReport.recorded === true, `${label}: status recorded=false`);
  assertValue(statusReport.operation?.stage === 'reconcile', `${label}: status stage=${statusReport.operation?.stage}`);
  assertValue(
    (statusReport.operation?.domainsDone ?? []).join(',') === (reconcileReport.report?.domainsDone ?? []).join(','),
    `${label}: the persisted row records ${JSON.stringify(statusReport.operation?.domainsDone)} but the reconcile report compared ${JSON.stringify(reconcileReport.report?.domainsDone)}`,
  );

  stableFacts.push(`chain:${label}:${preflightReport.sourceVersion}:${statusReport.operation.stage}`);
  return {
    source: sourceRoot,
    target: targetRoot,
    preflight: {
      exitCode: preflight.code,
      ready: preflightReport.ready,
      sourceKind: preflightReport.sourceKind,
      sourceVersion: preflightReport.sourceVersion,
      fingerprint: preflightReport.fingerprint,
      parityGaps: preflightReport.parityGaps,
      reportSha256: sha256(preflight.stdout),
    },
    copyIn: {
      exitCode: run.code,
      stage: runReport.result.stage,
      domainsDone: runReport.result.domainsDone,
      backupManifestSha256: runReport.result.backupManifestSha256,
      sourceFingerprintBefore: runReport.result.sourceFingerprintBefore,
      sourceFingerprintAfter: runReport.result.sourceFingerprintAfter,
      reportSha256: sha256(run.stdout),
    },
    reconcile: {
      exitCode: reconcile.code,
      stage: reconcileReport.report.stage,
      domainsDone: reconcileReport.report.domainsDone,
      domains: (reconcileReport.report.domains ?? []).map((domain) => ({
        domain: domain.domain,
        table: domain.table,
        applicable: domain.applicable,
        sourceDays: domain.sourceDays,
        comparedDays: domain.comparedDays,
        mismatches: domain.mismatches.length,
      })),
      reportSha256: sha256(reconcile.stdout),
    },
    status: {
      exitCode: status.code,
      recorded: statusReport.recorded,
      stage: statusReport.operation?.stage,
      domainsDone: statusReport.operation?.domainsDone,
      reportSha256: sha256(status.stdout),
    },
  };
}

function assertVersionGap(binary, probe, expected, fixture, source) {
  const report = parseJson(probe.stdout, 'preflight(next-fixture)');
  const versionCheck = (report.checks ?? []).find((check) => check.name === 'source_version');
  assertValue(probe.code === 1, `preflight of ${fixture.tag}: exit=${probe.code} want 1`);
  assertValue(versionCheck?.code === 'upgrade_source_version_unsupported', `preflight of ${fixture.tag}: code=${versionCheck?.code}`);
  assertValue(report.ready === false, `preflight of ${fixture.tag}: ready=${report.ready}`);
  assertValue(
    !/observed=/.test(versionCheck.detail) || versionCheck.detail.includes(`expected=${expected}`),
    `preflight of ${fixture.tag}: accepted release not named (${preview(versionCheck.detail)})`,
  );

  // The target root was never created: a blocked preflight writes nothing.
  const target = path.join(SCRATCH, 'work', `next-${fixture.tag}`, 'target');
  const run = runCli(
    binary,
    ['upgrade', 'run', '--legacy', source.sourceRoot, '--target', target, '--json'],
    `next-fixture:run`,
    { expectExit: 1, allowMissingBinary: true },
  );
  const runReport = parseJson(run.stdout, 'next-fixture:run');
  assertValue(runReport.preflight?.ready === false, 'next-fixture: run refused a blocked preflight');
  assertValue(!fs.existsSync(path.join(target, 'usage.sqlite')), 'next-fixture: run wrote a store for a blocked source');

  evidence.versionGap = {
    plannedTag: fixture.tag,
    acceptedRelease: expected,
    image: fixture.image,
    imageId: fixture.imageId,
    commit: fixture.commit,
    preflight: {
      exitCode: probe.code,
      code: versionCheck?.code,
      detail: versionCheck?.detail,
      reportSha256: sha256(probe.stdout),
    },
    targetRootCreated: fs.existsSync(path.join(target, 'usage.sqlite')),
  };
  evidence.findings.push({
    code: 'upgrade_source_version_pinned',
    severity: 'blocker-for-u1-1b',
    detail: `the contract accepts ${expected}, so a real ${fixture.tag} source is refused with upgrade_source_version_unsupported; the U1-1b re-pin is what makes the planned fixture usable`,
    file: 'apps/manager-server/internal/domain/bootstrap/contract.go:12',
    fixtureReusedForRePin: `${fixture.image} (${fixture.imageId})`,
  });
}

function expectedReleaseOf(command) {
  const report = parseJson(command.stdout, 'preflight(next-fixture)');
  const check = (report.checks ?? []).find((entry) => entry.name === 'source_version');
  const match = /expected=(v\d+\.\d+\.\d+)/.exec(check?.detail ?? '');
  return match ? match[1] : null;
}

// ---------------------------------------------------------------------------
// wizard read routes
// ---------------------------------------------------------------------------

// assertNoSecretInBody refuses a read body that publishes a credential, the
// configured source path, the harness's own paths or a full digest. The failure
// names what leaked and never repeats it: this assertion runs precisely when a
// credential regression happened, so its own output must not spread the secret
// into the log or the evidence.
function secretLabel(secret) {
  if (secret === ADMIN_V1) return 'the source administrator key';
  if (secret === ADMIN_V2) return 'the v2 administrator key';
  if (redactor.has(secret)) return 'registered credential material';
  return 'a source path or the data key';
}

export function assertNoSecretInBody(label, body, secrets) {
  const text = JSON.stringify(body);
  for (const secret of secrets) {
    if (secret && text.includes(secret)) {
      throw new Error(`${label} published ${secretLabel(secret)}; the body is withheld from this message`);
    }
  }
  if (/[0-9a-f]{64}/.test(text)) {
    throw new Error(`${label} published a full digest; the body is withheld from this message`);
  }
}

// assertOriginRule proves the Upgrade surface's cross-site rule on the given read
// leaves: browser provenance from another site is refused, a browser on this very
// origin is served, and an unauthenticated caller never learns the origin verdict
// — the credential's shape is decided first, from the request alone. With no
// CPAMP_PUBLIC_ORIGIN configured the request's own scheme and Host are the
// expected origin, which is what the manager sees here.
//
// Both shapes a browser sends are refused: an `Origin` from another site answers
// the surface's own `origin_rejected` document, and `Sec-Fetch-Site: cross-site`
// answers 403 as well. On the Fresh admin surface the transport's fetch-metadata
// policy owns that leg and answers its plain-text refusal first, so `fetchVerdict`
// names which answer that surface owes: the surface's own code, or
// `transport_refusal` when the transport's fetch-metadata policy answers first
// with its plain-text refusal.
function assertOriginRule(port, base, resources, label, { fetchVerdict = 'origin_rejected' } = {}) {
  const result = {};
  const surfaceCode = (answer) => answer.body?.error?.code ?? null;
  const owesVerdict = (answer, verdict) => (verdict === 'transport_refusal' ? surfaceCode(answer) === null : surfaceCode(answer) === verdict);
  for (const resource of resources) {
    const target = `${base}/${resource}`;
    const crossSite = httpJson('GET', target, {
      headers: { Authorization: `Bearer ${ADMIN_V1}`, Origin: 'https://upgrade-attacker.invalid' },
    });
    assertValue(
      crossSite.status === 403 && surfaceCode(crossSite) === 'origin_rejected',
      `${label}: GET ${resource} from a cross-site Origin = ${crossSite.status} ${preview(crossSite.body)} want 403 origin_rejected`,
    );
    const crossSiteFetch = httpJson('GET', target, {
      headers: { Authorization: `Bearer ${ADMIN_V1}`, 'Sec-Fetch-Site': 'cross-site' },
    });
    assertValue(
      crossSiteFetch.status === 403 && owesVerdict(crossSiteFetch, fetchVerdict),
      `${label}: GET ${resource} with Sec-Fetch-Site: cross-site = ${crossSiteFetch.status} ${preview(crossSiteFetch.body)} want 403 ${fetchVerdict}`,
    );
    const sameOrigin = httpJson('GET', target, {
      headers: { Authorization: `Bearer ${ADMIN_V1}`, Origin: `http://127.0.0.1:${port}` },
    });
    assertValue(sameOrigin.status === 200, `${label}: GET ${resource} from this very origin = ${sameOrigin.status} want 200`);
    const anonymous = httpJson('GET', target, { headers: { Origin: 'https://upgrade-attacker.invalid' } });
    assertValue(
      anonymous.status === 401,
      `${label}: anonymous GET ${resource} from a cross-site Origin = ${anonymous.status} want 401: the credential's shape is decided before the origin rule`,
    );
    result[resource] = {
      crossSiteOrigin: crossSite.status,
      crossSiteFetchMetadata: crossSiteFetch.status,
      sameOrigin: sameOrigin.status,
      anonymous: anonymous.status,
      code: surfaceCode(crossSite),
      fetchVerdict,
    };
  }
  return result;
}

// assertWizard proves the two read-only Upgrade routes on a real Fresh
// installation: the 1.x administrator key is the only credential they accept,
// the preflight is the real report of the real source, and no secret reaches the
// body.
async function assertWizard(binary, sourceRoot) {
  const root = path.join(SCRATCH, 'wizard');
  fs.mkdirSync(root, { recursive: true });
  const env = {
    USAGE_DATA_DIR: root,
    USAGE_DB_PATH: path.join(root, 'usage.sqlite'),
    CPA_MANAGER_DATA_KEY_PATH: path.join(root, 'data.key'),
    CPA_MANAGER_ADMIN_KEY: ADMIN_V2,
  };
  const init = sh(binary, ['initialize'], { env });
  evidence.commands.push(commandRecord('cpa-manager-plus initialize', init.code));
  if (init.code !== 0) {
    throw new Error(`initialize a Fresh installation: ${redactor.redact(tail(init.stderr))}`);
  }
  const fragment = init.stdout.trim().split('\n').filter(Boolean).pop() ?? '';
  // The handoff fragment carries a one-time credential: register it before any
  // later failure can print or record it.
  const handoff = fragment.includes('#') ? fragment.slice(fragment.indexOf('#') + 1) : fragment;
  redactor.add(fragment);
  redactor.add(handoff);

  const port = ports.take();
  const name = `${PREFIX}-wizard`;
  startHostManager({ name, binary, dataDir: root, port, adminKey: ADMIN_V2, legacyRoot: sourceRoot });
  await waitForHealth(port, `${name} /health`, 200);

  const base = `http://127.0.0.1:${port}/.well-known/cpamp-bootstrap/api/v2/installation/upgrade`;
  const unauthorized = httpJson('GET', `${base}/preflight`, {});
  assertValue(unauthorized.status === 401, `wizard preflight without a credential = ${unauthorized.status}`);
  const wrongKey = httpJson('GET', `${base}/preflight`, { headers: { Authorization: `Bearer ${ADMIN_V2}` } });
  assertValue(wrongKey.status === 401, `wizard preflight with the v2 Admin key = ${wrongKey.status}`);
  const write = httpJson('POST', `${base}/status`, { headers: { Authorization: `Bearer ${ADMIN_V1}` } });
  assertValue(write.status === 405, `wizard status write = ${write.status}`);

  const preflight = httpJson('GET', `${base}/preflight`, { headers: { Authorization: `Bearer ${ADMIN_V1}` } });
  assertValue(preflight.status === 200, `wizard preflight = ${preflight.status}: ${preview(preflight.body)}`);
  assertValue(preflight.body?.contractVersion === 'cpamp.upgrade-wizard.v1', `wizard contract = ${preflight.body?.contractVersion}`);
  assertValue(preflight.body?.sourceKind === 'legacy-manager', `wizard source kind = ${preflight.body?.sourceKind}`);
  assertValue(preflight.body?.ready === true, `wizard preflight ready = ${preflight.body?.ready}`);
  // The read-only preflight cannot declare the 1.x release: the CLI copy-in
  // declares it. That is a pending observation, never the exact-equality
  // acceptance the CLI makes, and never a refusal of a sound source.
  assertValue(
    preflight.body?.pendingDeclaration === true,
    `wizard preflight pendingDeclaration = ${preflight.body?.pendingDeclaration}`,
  );
  assertValue(
    preflight.body?.expectedVersion === acceptedRelease,
    `wizard preflight expectedVersion = ${preflight.body?.expectedVersion} want ${acceptedRelease}`,
  );
  const wizardVersionCheck = (preflight.body?.checks ?? []).find((check) => check.name === 'source_version');
  assertValue(
    wizardVersionCheck?.status === 'pending',
    `wizard source version status = ${wizardVersionCheck?.status}`,
  );
  assertValue(
    wizardVersionCheck?.code === 'upgrade_source_version_declared_at_copy_in',
    `wizard source version code = ${wizardVersionCheck?.code}`,
  );
  assertValue(
    (preflight.body?.checks ?? []).every((check) => check.status !== 'blocked'),
    `wizard preflight blocked a check on a sound source: ${preview(preflight.body?.checks)}`,
  );
  assertValue(
    (preflight.body?.blocked ?? []).length === 0,
    `wizard preflight blocked = ${preview(preflight.body?.blocked)}`,
  );

  const status = httpJson('GET', `${base}/status`, { headers: { Authorization: `Bearer ${ADMIN_V1}` } });
  assertValue(status.status === 200, `wizard status = ${status.status}`);
  assertValue(status.body?.recorded === false, `wizard status recorded = ${status.body?.recorded}`);
  assertValue(
    status.body?.nextAction === expectedNextAction(''),
    `wizard status nextAction = ${status.body?.nextAction} want ${expectedNextAction('')} for an unrecorded operation`,
  );
  const origin = assertOriginRule(port, base, ['preflight', 'status'], 'wizard', { fetchVerdict: 'transport_refusal' });

  // No secret in either body: the source administrator key, the data key
  // material, any absolute path, the full fingerprint or the v2 Admin key.
  const dataKey = fs.readFileSync(path.join(sourceRoot, 'data.key'), 'utf8').trim();
  redactor.add(dataKey);
  for (const body of [preflight.body, status.body]) {
    assertNoSecretInBody('wizard', body, [ADMIN_V1, ADMIN_V2, dataKey, sourceRoot, SCRATCH, root]);
  }

  evidence.wizard = {
    fragment: `${fragment.split('#')[0]}#handoff=redacted`,
    port,
    contracts: {
      preflight: {
        exitCode: preflight.status,
        contractVersion: preflight.body?.contractVersion,
        ready: preflight.body?.ready,
        sourceKind: preflight.body?.sourceKind,
        // The read cannot declare the release, so it reports the declaration as
        // pending and names the release the copy-in must be told. The document
        // records the pair it asserted, not only their hash.
        pendingDeclaration: preflight.body?.pendingDeclaration,
        expectedVersion: preflight.body?.expectedVersion,
      },
      status: { exitCode: status.status, recorded: status.body?.recorded, nextAction: status.body?.nextAction, stage: status.body?.stage },
    },
    credential: {
      sourceAdminKey: 'accepted',
      v2AdminKey: `refused (${wrongKey.status})`,
      anonymous: `refused (${unauthorized.status})`,
      write: `refused (${write.status})`,
    },
    noSecretInBody: true,
    origin,
    bodySha256: { preflight: sha256(JSON.stringify(preflight.body)), status: sha256(JSON.stringify(status.body)) },
  };
  stableFacts.push(`wizard:${preflight.body.ready}:${preflight.body.pendingDeclaration}:${preflight.body.expectedVersion}:${status.body.nextAction}`);
  stableFacts.push(`wizard:origin:${origin.preflight.crossSiteOrigin}:${origin.preflight.sameOrigin}:${origin.status.crossSiteOrigin}:${origin.status.sameOrigin}`);

  await stopHostManager(name);
}

// ---------------------------------------------------------------------------
// admission: the Upgrade surface
// ---------------------------------------------------------------------------

// The wizard's stage→action ladder, mirrored from the product's
// `nextActionForRecordedStage`
// (apps/manager-server/internal/http/controller/upgradewizard/handler.go, which
// projects every recorded row through it). A stage is a checkpoint, not a pending
// step: a recorded `reconcile` offers the CPA import that follows it, so "the next
// action is the recorded stage" was only ever true for `copy_in`. The product's
// own tables are the authority for the mirror —
// `TestNextActionForAnExternalConnectRefusal` (a refusal on
// `copy_in`/`reconcile`/`cpa_import` offers the connect step again) and
// `TestVerifiedRowOffersTheGateAgain` (a waiting verify row offers the gate) — and
// the harness test repeats their cases, so a change to the ladder fails the run as
// staleness instead of passing a guess.
const WIZARD_STAGE_ACTIONS = Object.freeze({
  '': 'preflight',
  preflight: 'preflight',
  copy_in: 'copy_in',
  reconcile: 'cpa_import',
  external_connect: 'verify',
  verify: 'complete',
  complete: 'complete',
  failed: 'reconcile',
  rolled_back: 'rolled_back',
});
const WIZARD_EXTERNAL_CONNECT_STAGES = Object.freeze(['copy_in', 'reconcile', 'cpa_import']);
const WIZARD_EXTERNAL_CONNECT_REFUSALS = Object.freeze([
  'external_cpa_version_unsupported',
  'external_cpa_config_document_invalid',
  'external_management_key_rejected',
  'external_target_moved',
]);
const WIZARD_VERIFY_WAITING_FOR_REPIN = 'upgrade_verify_waiting_for_repin';
const WIZARD_CPA_IMPORT_DEFERRED_MARKER = '(upgrade_cpa_import_requires_qualification)';

export function expectedNextAction(stage, { errorCode = '', domains = [], manualItems = [] } = {}) {
  if (WIZARD_EXTERNAL_CONNECT_STAGES.includes(stage) && WIZARD_EXTERNAL_CONNECT_REFUSALS.includes(errorCode)) {
    return 'external_connect';
  }
  if (stage === 'verify' && errorCode === WIZARD_VERIFY_WAITING_FOR_REPIN) return 'verify';
  if (stage === 'cpa_import') {
    // The import has run when it recorded the domain, and it stands deferred when
    // the row carries its typed item; both leave only the verification.
    const deferred = manualItems.some((item) => String(item).includes(WIZARD_CPA_IMPORT_DEFERRED_MARKER));
    return domains.includes('cpa_config') || deferred ? 'verify' : 'cpa_import';
  }
  return WIZARD_STAGE_ACTIONS[stage] ?? '';
}

// proveWizardReadContract asserts the contract of both read routes on a manager
// the harness already started. The source administrator key is proved before any
// path or verb verdict — an anonymous or foreign caller never learns the verb —
// a write is the 405 only a proved credential sees, and each body is the recorded
// operation or the source preflight, with no credential, path or full digest.
function proveWizardReadContract(port, label, expectedStage, sourceRoot) {
  const base = `http://127.0.0.1:${port}/.well-known/cpamp-bootstrap/api/v2/installation/upgrade`;
  const read = {};
  for (const resource of ['preflight', 'status']) {
    const target = `${base}/${resource}`;
    const anonymous = httpJson('GET', target, {});
    assertValue(anonymous.status === 401, `${label}: anonymous GET ${resource} = ${anonymous.status} want 401`);
    const foreign = httpJson('GET', target, { headers: { Authorization: `Bearer ${ADMIN_V2}` } });
    assertValue(foreign.status === 401, `${label}: GET ${resource} with the v2 Admin key = ${foreign.status} want 401`);
    const anonymousWrite = httpJson('POST', target, {});
    assertValue(anonymousWrite.status === 401, `${label}: anonymous POST ${resource} = ${anonymousWrite.status} want 401`);
    // Only a proved credential learns the verb.
    const write = httpJson('POST', target, { headers: { Authorization: `Bearer ${ADMIN_V1}` } });
    assertValue(
      write.status === 405 && write.allow === 'GET',
      `${label}: POST ${resource} with the source Admin key = ${write.status} Allow=${write.allow} want 405 with Allow: GET`,
    );
    const proved = httpJson('GET', target, { headers: { Authorization: `Bearer ${ADMIN_V1}` } });
    assertValue(proved.status === 200, `${label}: GET ${resource} with the source Admin key = ${proved.status}: ${preview(proved.body)}`);
    assertValue(proved.body?.contractVersion === 'cpamp.upgrade-wizard.v1', `${label}: GET ${resource} contract = ${proved.body?.contractVersion}`);
    assertNoSecretInBody(`${label}: GET ${resource}`, proved.body, [ADMIN_V1, ADMIN_V2, sourceRoot, SCRATCH]);
    if (resource === 'preflight') {
      assertValue(proved.body?.ready === true, `${label}: preflight ready = ${proved.body?.ready}`);
      assertValue(proved.body?.sourceKind === 'legacy-manager', `${label}: preflight source kind = ${proved.body?.sourceKind}`);
      read.preflight = { status: proved.status, contractVersion: proved.body.contractVersion, ready: proved.body.ready, sourceKind: proved.body.sourceKind };
      continue;
    }
    // The status body is the operation the copy-in recorded: the wizard can show
    // the stage it just reached from the copied root alone.
    assertValue(proved.body?.recorded === true, `${label}: status recorded = ${proved.body?.recorded}`);
    assertValue(proved.body?.stage === expectedStage, `${label}: status stage = ${proved.body?.stage} want ${expectedStage}`);
    const wantAction = expectedNextAction(expectedStage);
    assertValue(
      proved.body?.nextAction === wantAction,
      `${label}: status nextAction = ${proved.body?.nextAction} want ${wantAction} for a recorded ${expectedStage}`,
    );
    read.status = {
      status: proved.status,
      contractVersion: proved.body.contractVersion,
      recorded: proved.body.recorded,
      stage: proved.body.stage,
      nextAction: proved.body.nextAction,
    };
  }
  // One origin rule for the whole surface: the entry leaf's cross-site refusal is
  // the reads' refusal, with the same code.
  const origin = assertOriginRule(port, base, ['preflight', 'status'], label);
  // Only the two exact leaves are mounted: a query string, a trailing slash, an
  // extra segment and another fixed-carrier resource answer 404 even to the key
  // the reads accept.
  const fixedAPI = `http://127.0.0.1:${port}/.well-known/cpamp-bootstrap/api/v2`;
  for (const shape of [`${base}`, `${base}/status?wait=1`, `${base}/status/`, `${base}/status/extra`, `${fixedAPI}/operation`]) {
    const probe = httpJson('GET', shape, { headers: { Authorization: `Bearer ${ADMIN_V1}` } });
    assertValue(probe.status === 404, `${label}: ${shape} = ${probe.status} want 404: only the two exact leaves are mounted`);
  }
  // The rest of the product is not admitted either: the console, the key/pricing
  // API and the gateway/model traffic each answer 404 even to the key that reads
  // the wizard, which is what "only the Upgrade surface" means.
  const closedSurfaces = probeClosedSurfaces(port, label, { Authorization: `Bearer ${ADMIN_V1}` });
  return {
    credential: { anonymous: 401, v2AdminKey: 401, write: 405, allow: 'GET' },
    pathShapes: 404,
    closedSurfaces,
    origin,
    ...read,
  };
}

// proveUnfinishedAdmission starts the manager on a copied root whose recorded
// row is unfinished and proves the whole admission contract of that state: 503
// on /health, both reads answering 200 to the 1.x administrator key alone, and
// no canonical continuation, because the copy-in target carries no Fresh record.
async function proveUnfinishedAdmission(binary, root, sourceRoot, stage) {
  const port = ports.take();
  const name = `${PREFIX}-admission-${stage.replace(/_/g, '')}`;
  startHostManager({ name, binary, dataDir: root, port, adminKey: ADMIN_V2, legacyRoot: sourceRoot });
  try {
    await waitForContainerExitOrHealth(name, port);
    const health = httpJson('GET', `http://127.0.0.1:${port}/health`, {});
    assertValue(health.status === 503, `admission(${stage}): /health = ${health.status} want 503 while the operation is unfinished`);
    const reads = proveWizardReadContract(port, `admission(${stage})`, stage, sourceRoot);
    const canonical = httpJson('GET', `http://127.0.0.1:${port}/control/api/v2/installation/upgrade/status`, {
      headers: { Authorization: `Bearer ${ADMIN_V1}` },
    });
    assertValue(canonical.status === 404, `admission(${stage}): canonical wizard status = ${canonical.status} want 404: the copied root carries no Fresh record`);
    return { health: health.status, surface: 'upgrade-only', ...reads, canonicalWizardStatus: canonical.status };
  } finally {
    await stopHostManager(name);
  }
}

// proveMountlessStatus starts the manager on a root whose recorded operation is
// unfinished and whose 1.x source is no longer mounted, and proves the projection
// does not need it: the status answers 200 from the Manager store, keeps the
// recorded stage, and names the absent source as a typed marker, while the
// preflight — the step that does need the source — keeps its typed 503. The
// credential is the store's own, which is the key the mount accepted, so the key
// kept in the other file stays refused.
async function proveMountlessStatus(binary, root, stage) {
  const port = ports.take();
  const name = `${PREFIX}-mountless-${stage.replace(/_/g, '')}`;
  // An explicit absent mount, so the case does not depend on the host's /legacy.
  const absentRoot = path.join(SCRATCH, 'work', 'mountless', `${stage}-absent`);
  fs.rmSync(absentRoot, { recursive: true, force: true });
  startHostManager({ name, binary, dataDir: root, port, adminKey: ADMIN_V2, legacyRoot: absentRoot });
  const label = `mountless(${stage})`;
  try {
    await waitForHealth(port, `${name} /health`, 503);
    const base = `http://127.0.0.1:${port}/.well-known/cpamp-bootstrap/api/v2/installation/upgrade`;
    const status = httpJson('GET', `${base}/status`, { headers: { Authorization: `Bearer ${ADMIN_V1}` } });
    assertValue(status.status === 200, `${label}: status without the source mount = ${status.status}: ${preview(status.body)} want 200`);
    assertValue(status.body?.recorded === true, `${label}: status recorded without the source mount = ${status.body?.recorded}`);
    assertValue(status.body?.evidence === 'operation_row', `${label}: status evidence = ${status.body?.evidence} want operation_row`);
    assertValue(status.body?.stage === stage, `${label}: status stage without the source mount = ${status.body?.stage} want ${stage}`);
    const wantAction = expectedNextAction(stage);
    assertValue(
      status.body?.nextAction === wantAction,
      `${label}: status nextAction without the source mount = ${status.body?.nextAction} want ${wantAction} for a recorded ${stage}`,
    );
    // The mount is named, not hidden: the projection says what the operator has
    // to supply, with the same code the preflight reports.
    assertValue(
      status.body?.sourceRoot === 'upgrade_legacy_root_missing',
      `${label}: status sourceRoot = ${status.body?.sourceRoot} want upgrade_legacy_root_missing`,
    );
    assertNoSecretInBody(`${label}: status`, status.body, [ADMIN_V1, ADMIN_V2, root, SCRATCH, absentRoot]);
    // The store is the credential, and it holds the source's key: the key the
    // mount accepted keeps working, the other file's key never does.
    const foreign = httpJson('GET', `${base}/status`, { headers: { Authorization: `Bearer ${ADMIN_V2}` } });
    assertValue(foreign.status === 401, `${label}: status with the v2 Admin key = ${foreign.status} want 401 without the mount`);
    const preflight = httpJson('GET', `${base}/preflight`, { headers: { Authorization: `Bearer ${ADMIN_V1}` } });
    assertValue(
      preflight.status === 503 && preflight.body?.error?.code === 'upgrade_legacy_root_missing',
      `${label}: preflight without the source mount = ${preflight.status} ${preview(preflight.body)} want 503 upgrade_legacy_root_missing`,
    );
    return {
      mount: 'absent',
      health: 503,
      status: {
        status: status.status,
        recorded: status.body.recorded,
        evidence: status.body.evidence,
        stage: status.body.stage,
        nextAction: status.body.nextAction,
        sourceRoot: status.body.sourceRoot,
      },
      preflight: { status: preflight.status, code: preflight.body?.error?.code },
      credential: { storeAdminKey: status.status, v2AdminKey: foreign.status },
    };
  } finally {
    await stopHostManager(name);
  }
}

// proveRefusedMountStatus starts the manager on a root whose recorded operation
// is unfinished and whose 1.x mount holds a usage.sqlite that is a symbolic
// link. The shared database file policy refuses a link on both the mount lookup
// and the credential read, so the mount counts as no source at all: the surface
// must resolve the Manager store, keep the recorded stage readable on a 200 and
// name the condition, rather than answering 503 on the entry page and both
// reads — which is what the regression this case exists for did, so the operator
// could not even reach the page that names the file policy's verdict.
async function proveRefusedMountStatus(binary, root, stage, sourceRoot) {
  const label = `refused-mount(${stage})`;
  // A real copy of the source the chain copied, so the mount still looks like a
  // 1.x data directory; only the file behind usage.sqlite is a link.
  const mountRoot = path.join(SCRATCH, 'work', 'refused-mount', stage, 'source');
  copyTree(sourceRoot, mountRoot);
  const database = path.join(mountRoot, 'usage.sqlite');
  const linkTarget = path.join(mountRoot, 'usage.actual.sqlite');
  fs.renameSync(database, linkTarget);
  fs.symlinkSync(linkTarget, database);
  assertValue(
    fs.lstatSync(database).isSymbolicLink() && fs.statSync(database).isFile(),
    `${label}: the mount's usage.sqlite is not a symbolic link to a file`,
  );

  const port = ports.take();
  const name = `${PREFIX}-refusedmount-${stage.replace(/_/g, '')}`;
  startHostManager({ name, binary, dataDir: root, port, adminKey: ADMIN_V2, legacyRoot: mountRoot });
  try {
    await waitForHealth(port, `${name} /health`, 503);
    const base = `http://127.0.0.1:${port}/.well-known/cpamp-bootstrap/api/v2/installation/upgrade`;
    // Reachability is the assertion: a refused mount may not take the surface
    // down. The store answers the projection with the stage the chain recorded.
    const status = httpJson('GET', `${base}/status`, { headers: { Authorization: `Bearer ${ADMIN_V1}` } });
    assertValue(
      status.status === 200,
      `${label}: status with a symlinked mount database = ${status.status}: ${preview(status.body)} want 200 from the Manager store`,
    );
    assertValue(status.body?.recorded === true, `${label}: status recorded with a symlinked mount database = ${status.body?.recorded}`);
    assertValue(status.body?.evidence === 'operation_row', `${label}: status evidence = ${status.body?.evidence} want operation_row`);
    assertValue(status.body?.stage === stage, `${label}: status stage = ${status.body?.stage} want ${stage}`);
    const wantAction = expectedNextAction(stage);
    assertValue(
      status.body?.nextAction === wantAction,
      `${label}: status nextAction = ${status.body?.nextAction} want ${wantAction} for a recorded ${stage}`,
    );
    // The refused mount is named on the successful projection, with the code the
    // preflight reports for the same mount.
    assertValue(
      status.body?.sourceRoot === 'upgrade_source_symlinked',
      `${label}: status sourceRoot = ${status.body?.sourceRoot} want upgrade_source_symlinked`,
    );
    assertNoSecretInBody(`${label}: status`, status.body, [ADMIN_V1, ADMIN_V2, root, SCRATCH, mountRoot, linkTarget]);
    // The store is the credential, and it holds the key the mount accepted: that
    // key keeps working, the one in the other file never does.
    const foreign = httpJson('GET', `${base}/status`, { headers: { Authorization: `Bearer ${ADMIN_V2}` } });
    assertValue(foreign.status === 401, `${label}: status with the v2 Admin key = ${foreign.status} want 401 on a refused mount`);
    // The preflight inspects the mount the operator supplied, never the file the
    // credential fell back to, so it names the link itself.
    const preflight = httpJson('GET', `${base}/preflight`, { headers: { Authorization: `Bearer ${ADMIN_V1}` } });
    assertValue(preflight.status === 200, `${label}: preflight = ${preflight.status}: ${preview(preflight.body)} want 200`);
    assertValue(preflight.body?.ready === false, `${label}: preflight ready with a symlinked mount database = ${preflight.body?.ready} want false`);
    const ownership = (preflight.body?.checks ?? []).find((check) => check.name === 'source_ownership');
    assertValue(
      ownership?.code === 'upgrade_source_symlinked',
      `${label}: preflight source_ownership code = ${ownership?.code} want upgrade_source_symlinked`,
    );
    assertValue(
      (preflight.body?.blocked ?? []).includes('upgrade_source_symlinked'),
      `${label}: preflight blocked = ${JSON.stringify(preflight.body?.blocked)} want upgrade_source_symlinked`,
    );
    assertNoSecretInBody(`${label}: preflight`, preflight.body, [ADMIN_V1, ADMIN_V2, root, SCRATCH, mountRoot, linkTarget]);
    return {
      mount: 'symlinked',
      health: 503,
      status: {
        status: status.status,
        recorded: status.body.recorded,
        evidence: status.body.evidence,
        stage: status.body.stage,
        nextAction: status.body.nextAction,
        sourceRoot: status.body.sourceRoot,
      },
      preflight: { status: preflight.status, ready: preflight.body.ready, code: ownership.code, blocked: preflight.body.blocked },
      credential: { storeAdminKey: status.status, v2AdminKey: foreign.status },
    };
  } finally {
    await stopHostManager(name);
  }
}

// completeThroughTheGate finishes the operation the way an operator does. The
// External connection is real (a synthetic v8 CPA this harness serves, with its
// own management key and one client key), the Manager's own connection is
// stored, and then `upgrade verify` runs — the same command the operator runs —
// on the copied root. Both halves of the verdict are asserted: the CLI report
// and the row the gate wrote, including the evidence beside the stage.
async function completeThroughTheGate(binary, completedRoot, sourceRoot, databasePath) {
  const cpa = await startSyntheticCPA();
  try {
    const keyFile = path.join(SCRATCH, 'work', 'admission', 'management-key');
    fs.writeFileSync(keyFile, `${SYNTHETIC_CPA_MANAGEMENT_KEY}\n`, { mode: 0o600 });

    const connect = runCli(
      binary,
      ['upgrade', 'external-connect', '--target', completedRoot, '--cpa-base-url', cpa.url, '--management-key-file', keyFile, '--json'],
      'admission:connect',
      { expectExit: 0 },
    );
    const connected = parseJson(connect.stdout, 'admission:connect');
    assertValue(connected.result?.stage === 'external_connect', `admission:connect stage=${connected.result?.stage}`);

    // The gate compares the recorded binding with the CPA the Manager itself
    // resolves, so the Manager's own configuration has to name this CPA: the
    // same command an operator runs, against the copied root's own store.
    const configPath = path.join(SCRATCH, 'work', 'admission', 'manager-config.json');
    fs.writeFileSync(configPath, JSON.stringify({ dataDir: '.' }));
    runCli(
      binary,
      [
        'store-cpa-connection',
        '--cpa-base-url', cpa.url,
        '--management-key-file', keyFile,
        '--db-path', path.join(completedRoot, 'usage.sqlite'),
        '--data-key-path', path.join(completedRoot, 'data.key'),
      ],
      'admission:store-connection',
      { expectExit: 0, env: { CPA_MANAGER_CONFIG: configPath } },
    );

    const verify = runCli(
      binary,
      ['upgrade', 'verify', '--legacy', sourceRoot, '--target', completedRoot, '--json'],
      'admission:verify',
      { expectExit: 0 },
    );
    const report = parseJson(verify.stdout, 'admission:verify');
    assertValue(report.result?.complete === true, `admission:verify complete=${report.result?.complete}`);
    assertValue(report.result?.stage === 'complete', `admission:verify stage=${report.result?.stage}`);
    assertValue(report.result?.targetMode === 'external', `admission:verify targetMode=${report.result?.targetMode}`);
    assertValue(report.result?.alreadyVerified === false, 'admission:verify reported the first verdict as a repeat');
    for (const domain of ['source_copy', 'cpa_config', 'identity']) {
      assertValue(
        (report.result?.domainsDone ?? []).includes(domain),
        `admission:verify domainsDone=${JSON.stringify(report.result?.domainsDone)} is missing ${domain}`,
      );
    }

    const row = readRowJson(databasePath, 'select stage, error_code, verification from upgrade_operations where id = 1');
    assertValue(row?.stage === 'complete', `admission: the gate did not record complete (stage=${row?.stage})`);
    const recorded = JSON.parse(String(row.verification ?? '{}'));
    assertValue(recorded.targetMode === 'external', `admission: the recorded evidence target=${recorded.targetMode}`);
    for (const domain of ['source_copy', 'cpa_config']) {
      assertValue(
        Array.isArray(recorded.domains) && recorded.domains.includes(domain),
        `admission: the recorded evidence domains=${JSON.stringify(recorded.domains)} is missing ${domain}`,
      );
    }
    assertValue(
      typeof recorded.sourceFingerprint === 'string' && SHA256_PATTERN.test(recorded.sourceFingerprint),
      'admission: the recorded evidence carries no source fingerprint',
    );
    // The verdict is repeatable and writes nothing the second time, which is
    // what the wizard and the CLI both report.
    const repeat = runCli(
      binary,
      ['upgrade', 'verify', '--legacy', sourceRoot, '--target', completedRoot, '--json'],
      'admission:verify-repeat',
      { expectExit: 0 },
    );
    const repeated = parseJson(repeat.stdout, 'admission:verify-repeat');
    assertValue(repeated.result?.alreadyVerified === true, 'admission:verify repeat was not reported as already recorded');

    return {
      verify: {
        exitCode: verify.code,
        stage: report.result?.stage,
        targetMode: report.result?.targetMode,
        domainsDone: report.result?.domainsDone,
      },
      row: { stage: row.stage, errorCode: row.error_code ?? null, verificationKeys: Object.keys(recorded).sort() },
      repeat: { exitCode: repeat.code, alreadyVerified: repeated.result?.alreadyVerified, updatedAtMs: repeated.result?.updatedAtMs },
    };
  } finally {
    await cpa.close();
  }
}

// syntheticCPAProgram is the standalone server the harness starts for the
// External connect step: the CLI runs under spawnSync, which blocks this
// process's event loop, so a server living in this process could accept the TCP
// connection and then never answer it. The program serves the five authenticated
// reads the step makes — the version probe, the v8 document, the client-key
// list, the config YAML the step digests into the server fingerprint, and the
// model surface — with the credential and version it expects.
function syntheticCPAProgram({ port, model, configYAML }) {
  return `import http from 'node:http';

const port = ${JSON.stringify(port)};
const model = ${JSON.stringify(model)};
const configYAML = ${JSON.stringify(configYAML)};
const management = process.env.CPA_MANAGEMENT_KEY;
const clientKey = process.env.CPA_CLIENT_KEY;

const send = (response, status, payload, headers = {}) => {
  response.writeHead(status, { 'content-type': 'application/json', ...headers });
  response.end(payload);
};

const server = http.createServer((request, response) => {
  const url = request.url ?? '';
  // The model surface is a gateway read with a client credential, not a
  // management read: it answers before the management credential is checked.
  if (url === '/v1/models') {
    send(response, 200, JSON.stringify({ object: 'list', data: [{ id: model }] }));
    return;
  }
  if (request.headers.authorization !== 'Bearer ' + management) {
    send(response, 401, JSON.stringify({ error: 'unauthorized' }));
    return;
  }
  if (url === '/v0/management/config') {
    send(response, 200, '{}', { 'x-cpa-version': 'v8.0.13' });
    return;
  }
  if (url === '/v8/management/config') {
    send(response, 200, JSON.stringify({
      'config-version': 8,
      server: { port },
      oauth: { 'auth-dir': '/auth' },
      access: { 'api-keys': [clientKey] },
    }));
    return;
  }
  if (url === '/v8/management/config/access/api-keys') {
    send(response, 200, JSON.stringify([clientKey]));
    return;
  }
  if (url === '/v8/management/config.yaml') {
    send(response, 200, configYAML, { 'content-type': 'application/yaml' });
    return;
  }
  send(response, 404, JSON.stringify({ error: 'not found' }));
});

server.listen(port, '127.0.0.1', () => {
  console.log('synthetic CPA listening on 127.0.0.1:' + port);
});
`;
}

// syntheticCPAConfigYAML is the v8 configuration document the synthetic CPA
// serves at /v8/management/config.yaml, shaped like the YAML a v8 CPA writes.
// Its bytes are what the connect step digests, so it names the same client key
// and port the other routes answer with.
function syntheticCPAConfigYAML(port) {
  return [
    'config-version: 8',
    'server:',
    `  port: ${port}`,
    'oauth:',
    '  auth-dir: /auth',
    'access:',
    '  api-keys:',
    `    - ${SYNTHETIC_CPA_CLIENT_KEY}`,
    '',
  ].join('\n');
}

// startSyntheticCPA starts the program above as its own process, on a port this
// run allocated, and returns a handle that stops it. It is a host process in the
// registry's vocabulary: nothing about it outlives the run.
async function startSyntheticCPA() {
  const port = ports.take();
  const name = `${PREFIX}-cpa`;
  registry.assertCreationAllowed('hostProcess', name);
  const program = path.join(SCRATCH, 'synthetic-cpa.mjs');
  fs.mkdirSync(SCRATCH, { recursive: true });
  fs.writeFileSync(program, syntheticCPAProgram({
    port,
    model: DATASET_MODELS[0].model,
    configYAML: syntheticCPAConfigYAML(port),
  }));
  const logFile = path.join(SCRATCH, `${name}.log`);
  const out = fs.openSync(logFile, 'w');
  const child = spawn(process.execPath, [program], {
    env: {
      ...process.env,
      CPA_MANAGEMENT_KEY: SYNTHETIC_CPA_MANAGEMENT_KEY,
      CPA_CLIENT_KEY: SYNTHETIC_CPA_CLIENT_KEY,
    },
    stdio: ['ignore', out, out],
    detached: true,
  });
  child.unref();
  hostManagers.set(name, { child, logFile, port });
  registry.track('hostProcess', name, () => stopHostProcess(name), { pid: child.pid, port });
  await waitFor(async () => portOpen(port), `synthetic CPA on 127.0.0.1:${port}`, 30_000);
  return {
    url: `http://127.0.0.1:${port}`,
    close: () => stopHostManager(name),
  };
}

function readRowJson(dbPath, sql) {
  const db = new DatabaseSync(dbPath, { readOnly: true });
  try {
    return db.prepare(sql).get();
  } finally {
    db.close();
  }
}

// proveCompletedAdmission proves the reads end with the operation. The recorded
// row is the terminal `complete` one, so admission serves the full writable
// surface — /health 200 — and both fixed-surface reads answer 404 to every
// caller, including the key they accepted a moment earlier.
async function proveCompletedAdmission(binary, completedRoot, sourceRoot) {
  const port = ports.take();
  const name = `${PREFIX}-admission-complete`;
  startHostManager({ name, binary, dataDir: completedRoot, port, adminKey: ADMIN_V2, legacyRoot: sourceRoot });
  try {
    await waitForHealth(port, `${name} /health`, 200);
    const base = `http://127.0.0.1:${port}/.well-known/cpamp-bootstrap/api/v2/installation/upgrade`;
    const reads = {};
    for (const resource of ['preflight', 'status']) {
      for (const [credential, headers] of [
        ['anonymous', {}],
        ['the source Admin key', { Authorization: `Bearer ${ADMIN_V1}` }],
      ]) {
        const read = httpJson('GET', `${base}/${resource}`, { headers });
        assertValue(read.status === 404, `admission(complete): GET ${resource} with ${credential} = ${read.status} want 404 after ProductComplete`);
      }
      reads[resource] = 404;
    }
    return { health: 200, surface: 'full', preflight: reads.preflight, status: reads.status };
  } finally {
    await stopHostManager(name);
  }
}

// assertAdmission proves both sides of the Upgrade surface's admission: while
// the recorded operation is unfinished (copy_in and reconcile) the fixed
// Bootstrap carrier serves the wizard's two read routes beside the Upgrade entry
// leaf under the source credential, and once the operation is complete the same
// process serves the full surface and the reads are gone.
async function assertAdmission(binary, workspace) {
  const sourceRoot = workspace.main.source;
  // The copy_in state: the same source the chain copied, stopped after `run`, so
  // the row is at a stage the wizard has to be able to show before reconcile.
  const copyInRoot = path.join(SCRATCH, 'work', 'admission', 'copy_in');
  fs.rmSync(copyInRoot, { recursive: true, force: true });
  fs.mkdirSync(copyInRoot, { recursive: true });
  const preflight = runCli(binary, ['upgrade', 'preflight', '--legacy', sourceRoot, '--json'], 'admission:preflight', { expectExit: 0 });
  assertValue(parseJson(preflight.stdout, 'admission:preflight').ready === true, 'admission: the chain source is not ready');
  const run = runCli(binary, ['upgrade', 'run', '--legacy', sourceRoot, '--target', copyInRoot, '--json'], 'admission:run', { expectExit: 0 });
  const runStage = parseJson(run.stdout, 'admission:run').result?.stage;
  assertValue(runStage === 'copy_in', `admission: run stage=${runStage}`);

  const copyIn = await proveUnfinishedAdmission(binary, copyInRoot, sourceRoot, 'copy_in');
  const reconcile = await proveUnfinishedAdmission(binary, workspace.main.target, sourceRoot, 'reconcile');
  // The same two roots with the source mount gone: the recorded stage is still
  // readable, and the missing source is a typed marker on a 200 rather than a
  // refusal to answer.
  const mountless = {
    copy_in: await proveMountlessStatus(binary, copyInRoot, 'copy_in'),
    reconcile: await proveMountlessStatus(binary, workspace.main.target, 'reconcile'),
  };
  // And the same two roots with the mount present but refused by the shared
  // database file policy (usage.sqlite is a link): the mount holds no database the
  // surface may read, so the Manager store is the credential and the surface stays
  // reachable while both projections name the condition.
  const refusedMount = {
    copy_in: await proveRefusedMountStatus(binary, copyInRoot, 'copy_in', sourceRoot),
    reconcile: await proveRefusedMountStatus(binary, workspace.main.target, 'reconcile', sourceRoot),
  };

  // Completion is the product's own verdict. The operation on the copied root is
  // connected to a real v8 CPA, the Manager's own connection is stored, and the
  // built binary runs the completion gate on it. The gate records the stage and
  // the evidence it rests on in one transaction; the harness writes no stage
  // token and reads back only what the verdict recorded.
  const completedRoot = path.join(SCRATCH, 'work', 'admission', 'complete');
  fs.rmSync(completedRoot, { recursive: true, force: true });
  copyTree(workspace.main.target, completedRoot);
  fs.rmSync(path.join(completedRoot, 'usage.sqlite.manager.lock'), { force: true });
  const completedDatabase = path.join(completedRoot, 'usage.sqlite');
  const completion = await completeThroughTheGate(binary, completedRoot, sourceRoot, completedDatabase);
  const completed = await proveCompletedAdmission(binary, completedRoot, sourceRoot);

  evidence.admission = {
    surface: 'upgrade-only',
    states: { copy_in: copyIn, reconcile },
    mountless,
    refusedMount,
    completed,
    completion,
  };
  stableFacts.push(`admission:copy_in:${copyIn.health}:${copyIn.status.status}:${copyIn.status.stage}:${copyIn.status.nextAction}:${copyIn.preflight.status}`);
  stableFacts.push(`admission:reconcile:${reconcile.health}:${reconcile.status.status}:${reconcile.status.stage}:${reconcile.status.nextAction}:${reconcile.preflight.status}`);
  stableFacts.push(
    `admission:mountless:copy_in:${mountless.copy_in.status.status}:${mountless.copy_in.status.stage}:${mountless.copy_in.status.nextAction}:${mountless.copy_in.status.sourceRoot}:${mountless.copy_in.preflight.status}:${mountless.copy_in.preflight.code}`,
  );
  stableFacts.push(
    `admission:mountless:reconcile:${mountless.reconcile.status.status}:${mountless.reconcile.status.stage}:${mountless.reconcile.status.nextAction}:${mountless.reconcile.status.sourceRoot}:${mountless.reconcile.preflight.status}:${mountless.reconcile.preflight.code}`,
  );
  stableFacts.push(
    `admission:refused-mount:copy_in:${refusedMount.copy_in.health}:${refusedMount.copy_in.status.status}:${refusedMount.copy_in.status.stage}:${refusedMount.copy_in.status.nextAction}:${refusedMount.copy_in.status.sourceRoot}:${refusedMount.copy_in.preflight.status}:${refusedMount.copy_in.preflight.ready}:${refusedMount.copy_in.preflight.code}`,
  );
  stableFacts.push(
    `admission:refused-mount:reconcile:${refusedMount.reconcile.health}:${refusedMount.reconcile.status.status}:${refusedMount.reconcile.status.stage}:${refusedMount.reconcile.status.nextAction}:${refusedMount.reconcile.status.sourceRoot}:${refusedMount.reconcile.preflight.status}:${refusedMount.reconcile.preflight.ready}:${refusedMount.reconcile.preflight.code}`,
  );
  stableFacts.push(`admission:complete:${completed.health}:${completed.status}:${completed.preflight}`);
  stableFacts.push(
    `admission:completion:${completion.verify.stage}:${completion.verify.targetMode}:${completion.row.stage}:${completion.row.verificationKeys.join(',')}`,
  );
}

// ---------------------------------------------------------------------------
// fault cases
// ---------------------------------------------------------------------------

// 1. The source moves after the copy-in. The recorded provenance must stay the
// provenance: a second run does not re-copy, and a rollback plan refuses the
// directory that no longer reproduces the fingerprint the copy came with.
function faultSourceTouched(binary, workspace) {
  const source = path.join(SCRATCH, 'work', 'touched', 'source');
  const target = path.join(SCRATCH, 'work', 'touched', 'target');
  copyTree(workspace.main.source, source);
  fs.mkdirSync(target, { recursive: true });
  chain(binary, source, target, 'touched');

  // Touch the source: one more row, written through SQLite exactly as a restarted
  // 1.x writer would.
  sqliteExec(path.join(source, 'usage.sqlite'), `insert into settings (key, value, updated_at_ms) values ('u1d_fault_touched', '1', 1)`);
  const digestAfterTouch = dirDigest(target);

  const rerun = runCli(binary, ['upgrade', 'run', '--legacy', source, '--target', target, '--json'], 'touched:rerun', { expectExit: 0 });
  const rerunReport = parseJson(rerun.stdout, 'touched:rerun');
  assertValue(Boolean(rerunReport.operation), 'touched: rerun did not report the recorded operation');
  assertValue(rerunReport.operation.stage === 'reconcile', `touched: rerun stage=${rerunReport.operation.stage}`);
  assertValue(dirDigest(target) === digestAfterTouch, 'touched: a second run rewrote the v2 data root');

  const prepare = runCli(
    binary,
    ['upgrade', 'rollback', '--prepare', '--legacy', source, '--target', target, '--json'],
    'touched:rollback-prepare',
    // One typed code has one exit code on every subcommand: the retryable
    // upgrade_source_changed is exit 2 here exactly as it is for run and
    // reconcile.
    { expectExit: 2 },
  );
  const code = typedCode(prepare);
  assertValue(code === 'upgrade_source_changed', `touched: refusal code=${code}`);
  assertValue(dirDigest(target) === digestAfterTouch, 'touched: the refused rollback plan wrote the v2 data root');
  recordFault('source-touched-after-copy-in', prepare, code, true, { rerunExit: rerun.code, rerunStage: rerunReport.operation.stage });
}

// proveCopyInMarkerWindow reads the window the interrupted copy-in leaves behind
// over the operator's own HTTP projection. The provenance marker is published
// before the operation row commits, so the store holds a marker and no row:
// status must report the copy_in the admission already enforces, name the marker
// as its evidence, and offer copy_in as the next action instead of claiming that
// nothing had started.
async function proveCopyInMarkerWindow(binary, target, sourceRoot) {
  const port = ports.take();
  const name = `${PREFIX}-marker-window`;
  const label = 'marker-window';
  startHostManager({ name, binary, dataDir: target, port, adminKey: ADMIN_V2, legacyRoot: sourceRoot });
  try {
    await waitForHealth(port, `${name} /health`, 503);
    const base = `http://127.0.0.1:${port}/.well-known/cpamp-bootstrap/api/v2/installation/upgrade`;
    const status = httpJson('GET', `${base}/status`, { headers: { Authorization: `Bearer ${ADMIN_V1}` } });
    assertValue(status.status === 200, `${label}: status = ${status.status}: ${preview(status.body)} want 200`);
    assertValue(status.body?.recorded === true, `${label}: status recorded = ${status.body?.recorded} want true: the marker records an operation in progress`);
    assertValue(status.body?.evidence === 'copy_in_marker', `${label}: status evidence = ${status.body?.evidence} want copy_in_marker`);
    assertValue(status.body?.stage === 'copy_in', `${label}: status stage = ${status.body?.stage} want copy_in`);
    assertValue(
      status.body?.nextAction === expectedNextAction('copy_in'),
      `${label}: status nextAction = ${status.body?.nextAction} want ${expectedNextAction('copy_in')}, not the step before any work started`,
    );
    assertValue(status.body?.sourceRoot === 'available', `${label}: status sourceRoot = ${status.body?.sourceRoot} want available`);
    assertNoSecretInBody(`${label}: status`, status.body, [ADMIN_V1, ADMIN_V2, sourceRoot, target, SCRATCH]);
    return {
      status: status.status,
      recorded: status.body.recorded,
      evidence: status.body.evidence,
      stage: status.body.stage,
      nextAction: status.body.nextAction,
      sourceRoot: status.body.sourceRoot,
      domainsDone: status.body.domainsDone,
    };
  } finally {
    await stopHostManager(name);
  }
}

// 2. The copy-in is killed after materialize and before the operation row
// commits. The provenance marker must stay, the row must be absent, and a
// resumed run must adopt the materialized store instead of restoring again.
async function faultInterruptedCopyIn(binary, workspace) {
  const source = path.join(SCRATCH, 'work', 'interrupted', 'source');
  const target = path.join(SCRATCH, 'work', 'interrupted', 'target');
  copyTree(workspace.main.source, source);
  fs.mkdirSync(target, { recursive: true });
  const markerPath = path.join(target, '.upgrade-copy-in', 'copy-in.json');

  await new Promise((resolvePromise, rejectPromise) => {
    const child = spawn(
      binary,
      withDeclaredSourceVersion(['upgrade', 'run', '--legacy', source, '--target', target, '--json'], acceptedRelease),
      { stdio: ['ignore', 'pipe', 'pipe'] },
    );
    // The marker is published before the first member moves, and its phase says
    // how far the materialization got. Killing at the first sight of it catches
    // a stage where restoring is the right answer, so the fault waits for
    // `migrating`: every member is in place and the row is not committed yet.
    // That is the window a resume has to adopt instead of restoring again.
    let killed = false;
    let caughtPhase = '';
    const timer = setInterval(() => {
      if (killed) return;
      try {
        const marker = JSON.parse(fs.readFileSync(markerPath, 'utf8'));
        caughtPhase = marker?.phase ?? caughtPhase;
        if (marker?.phase === 'migrating') {
          killed = true;
          clearInterval(timer);
          child.kill('SIGKILL');
        }
      } catch {
        // No marker yet, or it is being written: the next tick retries.
      }
    }, 2);
    const deadline = setTimeout(() => {
      clearInterval(timer);
      child.kill('SIGKILL');
      rejectPromise(new Error('interrupted copy-in: the marker never appeared'));
    }, 120_000);
    child.on('exit', (code, signal) => {
      clearInterval(timer);
      clearTimeout(deadline);
      evidence.commands.push(commandRecord('upgrade run (SIGKILL after the marker)', code ?? -1));
      if (!killed) {
        rejectPromise(new Error(`interrupted copy-in: the run finished before the migrating marker could be caught (code=${code} signal=${signal} lastPhase=${caughtPhase})`));
        return;
      }
      resolvePromise();
    });
  });

  assertValue(fs.existsSync(markerPath), 'interrupted: the provenance marker is missing');
  const status = runCli(binary, ['upgrade', 'status', '--db-path', path.join(target, 'usage.sqlite'), '--json'], 'interrupted:status', { expectExit: 0 });
  const statusReport = parseJson(status.stdout, 'interrupted:status');
  assertValue(statusReport.recorded === false, 'interrupted: an operation row was committed before the migration finished');

  // The window the marker covers, read over the projection the operator uses.
  const markerWindow = await proveCopyInMarkerWindow(binary, target, source);

  const resume = runCli(binary, ['upgrade', 'run', '--legacy', source, '--target', target, '--json'], 'interrupted:resume', { expectExit: 0 });
  const resumeReport = parseJson(resume.stdout, 'interrupted:resume');
  assertValue(resumeReport.result?.stage === 'copy_in', `interrupted: resume stage=${resumeReport.result?.stage}`);
  assertValue(resumeReport.result?.restored === false, 'interrupted: the resume restored the members again');
  assertValue(!fs.existsSync(path.join(target, '.upgrade-copy-in')), 'interrupted: the resume left the work directory behind');
  const recorded = runCli(binary, ['upgrade', 'status', '--db-path', path.join(target, 'usage.sqlite'), '--json'], 'interrupted:recorded', { expectExit: 0 });
  assertValue(parseJson(recorded.stdout, 'interrupted:recorded').operation?.stage === 'copy_in', 'interrupted: the resume did not record copy_in');

  evidence.faults.push({
    name: 'copy-in-interrupted-before-the-row',
    exitCode: resume.code,
    code: 'resumed_copy_in',
    targetRootUnchanged: false,
    markerSeen: true,
    recordedBeforeResume: false,
    resumedStage: resumeReport.result.stage,
    restoredAgain: resumeReport.result.restored,
    markerWindow,
  });
  stableFacts.push('fault:interrupted:resumed');
  stableFacts.push(`fault:interrupted:marker-window:${markerWindow.status}:${markerWindow.recorded}:${markerWindow.evidence}:${markerWindow.stage}:${markerWindow.nextAction}:${markerWindow.sourceRoot}`);
}

// 3. A derived rollup cleared on a target that still holds the retained events
// a rebuild would read: waivable, and marked once the operator acknowledges it.
function faultClearedRollupRecoverable(binary, workspace) {
  const source = path.join(SCRATCH, 'work', 'rollup-recoverable', 'source');
  const target = path.join(SCRATCH, 'work', 'rollup-recoverable', 'target');
  copyTree(workspace.archived, source);
  fs.mkdirSync(target, { recursive: true });
  chain(binary, source, target, 'rollup-recoverable');

  injectClearedRollup(target, TABLES.pricing);
  const digestAfterInjection = dirDigest(target);

  const refused = runCli(binary, ['upgrade', 'reconcile', '--legacy', source, '--target', target, '--json'], 'rollup-recoverable:refused', { expectExit: 1 });
  const refusedReport = parseJson(refused.stdout, 'rollup-recoverable:refused');
  const code = refusedReport.errorCode || typedCode(refused);
  assertValue(code === 'upgrade_reconcile_mismatch', `rollup-recoverable: code=${code}`);
  const unacknowledged = refusedReport.report?.unacknowledgedDays ?? [];
  assertValue(unacknowledged.length > 0, 'rollup-recoverable: the refused reconcile named no day to acknowledge');
  assertValue(dirDigest(target) === digestAfterInjection, 'rollup-recoverable: the refused reconcile wrote the v2 data root');

  const acknowledge = unacknowledged.flatMap((day) => ['--acknowledge', day]);
  const accepted = runCli(
    binary,
    ['upgrade', 'reconcile', '--legacy', source, '--target', target, '--json', ...acknowledge],
    'rollup-recoverable:acknowledged',
    { expectExit: 0 },
  );
  const acceptedReport = parseJson(accepted.stdout, 'rollup-recoverable:acknowledged');
  assertValue(acceptedReport.report?.newlyMarkedDays === unacknowledged.length, `rollup-recoverable: newlyMarked=${acceptedReport.report?.newlyMarkedDays}`);
  const reasons = (acceptedReport.report?.incompleteDays ?? []).map((day) => day.reason);
  assertValue(
    reasons.length === unacknowledged.length && reasons.every((reason) => reason === 'derived_rollup_lost'),
    `rollup-recoverable: marked reasons=${JSON.stringify(reasons)}`,
  );
  recordFault('cleared-derived-rollup-with-retained-support', refused, code, true, {
    acknowledgedDays: unacknowledged,
    newlyMarkedDays: acceptedReport.report?.newlyMarkedDays,
    acknowledgedExit: accepted.code,
  });
}

// 4. The same cleared rollup, but a day the store holds no retained event of:
// nothing can rebuild it, so no acknowledgement may waive it.
function faultClearedRollupUnrecoverable(binary, workspace) {
  const source = path.join(SCRATCH, 'work', 'rollup-unrecoverable', 'source');
  const target = path.join(SCRATCH, 'work', 'rollup-unrecoverable', 'target');
  copyTree(workspace.deleted, source);
  fs.mkdirSync(target, { recursive: true });
  chain(binary, source, target, 'rollup-unrecoverable');

  injectClearedRollup(target, TABLES.pricing);
  const digestAfterInjection = dirDigest(target);
  const lost = DATASET_DAYS[0];
  const refused = runCli(
    binary,
    ['upgrade', 'reconcile', '--legacy', source, '--target', target, '--json', '--acknowledge', lost],
    'rollup-unrecoverable:refused',
    { expectExit: 1 },
  );
  const refusedReport = parseJson(refused.stdout, 'rollup-unrecoverable:refused');
  const code = refusedReport.errorCode || typedCode(refused);
  assertValue(code === 'upgrade_reconcile_target_incomplete', `rollup-unrecoverable: code=${code}`);
  assertValue(/no retained events/.test(refusedReport.error ?? ''), `rollup-unrecoverable: refusal did not name the lost support: ${refusedReport.error}`);
  assertValue(dirDigest(target) === digestAfterInjection, 'rollup-unrecoverable: the refused reconcile wrote the v2 data root');
  recordFault('cleared-derived-rollup-without-support', refused, code, true, { acknowledgeAttempted: lost });
}

// 5. A compared table dropped on the target: lost data, never a disagreement.
function faultDroppedTable(binary, workspace) {
  const source = path.join(SCRATCH, 'work', 'dropped', 'source');
  const target = path.join(SCRATCH, 'work', 'dropped', 'target');
  copyTree(workspace.deleted, source);
  fs.mkdirSync(target, { recursive: true });
  chain(binary, source, target, 'dropped');

  sqliteExec(path.join(target, 'usage.sqlite'), `drop table ${TABLES.history}`);
  assertValue(
    sqliteCount(path.join(target, 'usage.sqlite'), `select count(*) as count from sqlite_master where type = 'table' and name = '${TABLES.history}'`) === 0,
    'dropped: the history table is still there after the injection',
  );
  const digestAfterInjection = dirDigest(target);
  const refused = runCli(binary, ['upgrade', 'reconcile', '--legacy', source, '--target', target, '--json'], 'dropped:refused', { expectExit: 1 });
  const refusedReport = parseJson(refused.stdout, 'dropped:refused');
  const code = refusedReport.errorCode || typedCode(refused);
  assertValue(code === 'upgrade_reconcile_target_incomplete', `dropped: code=${code}`);
  assertValue((refusedReport.report?.domains ?? []).some((domain) => domain.domain === 'history' && domain.applicable === false), 'dropped: the report did not name the missing history table');
  assertValue(dirDigest(target) === digestAfterInjection, 'dropped: the refused reconcile wrote the v2 data root');
  recordFault('dropped-table-on-the-target', refused, code, true, { table: TABLES.history });
}

// 6. A natively busy source: a live 1.x writer holds the source's ownership
// fence, so the preflight is blocked before anything is read or written.
//
// The fence is `flock` on `<database>.manager.lock`. A container's flock lives
// in the Docker VM's kernel, so on this platform the harness holds the same
// fence itself, with the same primitive and the same file the product locks;
// on Linux it is the same lock the 1.x process would hold.
async function faultBusySource(binary, workspace) {
  const source = path.join(SCRATCH, 'work', 'busy', 'source');
  const target = path.join(SCRATCH, 'work', 'busy', 'target');
  copyTree(workspace.main.source, source);
  // A real v2 store on the target, so "the blocked write changed nothing" is a
  // claim about data, not about an empty directory. Its recorded row is cleared
  // first: a target whose row is already past the copy-in is reported and left
  // alone, because the run owns only the preflight and the copy-in, so the fence
  // would never be reached. With no row the store is data the copy-in would
  // write into, which is what the busy source has to stop.
  copyTree(workspace.main.target, target);
  sqliteExec(path.join(target, 'usage.sqlite'), 'delete from upgrade_operations');
  const holder = spawn(
    'python3',
    [
      '-c',
      'import fcntl, sys, time\nf = open(sys.argv[1], "a")\nfcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB)\nprint("locked", flush=True)\ntime.sleep(600)\n',
      path.join(source, 'usage.sqlite.manager.lock'),
    ],
    { stdio: ['ignore', 'pipe', 'pipe'] },
  );
  try {
    await new Promise((resolvePromise, rejectPromise) => {
      const timer = setTimeout(() => rejectPromise(new Error('busy: the source fence was not taken')), 30_000);
      holder.stdout.on('data', (chunk) => {
        if (String(chunk).includes('locked')) {
          clearTimeout(timer);
          resolvePromise();
        }
      });
      holder.on('exit', (code) => {
        clearTimeout(timer);
        rejectPromise(new Error(`busy: the source fence holder exited with ${code}`));
      });
    });
    const digestBefore = dirDigest(target);
    const blocked = runCli(binary, ['upgrade', 'preflight', '--legacy', source, '--json'], 'busy:preflight', { expectExit: 1 });
    const report = parseJson(blocked.stdout, 'busy:preflight');
    const check = (report.checks ?? []).find((entry) => entry.name === 'source_ownership');
    assertValue(report.ready === false, 'busy: preflight ready=true while a 1.x writer holds the source');
    assertValue(check?.code === 'upgrade_source_busy', `busy: code=${check?.code}`);

    // The actual write path, not a preflight on a target it never receives: the
    // copy-in must refuse the fenced source and leave the v2 store byte-identical.
    const run = runCli(binary, ['upgrade', 'run', '--legacy', source, '--target', target, '--json'], 'busy:run', {
      expectExit: 1,
    });
    const runReport = parseJson(run.stdout, 'busy:run');
    assertValue(runReport.preflight?.ready === false, 'busy: copy-in ran a blocked preflight');
    assertValue(dirDigest(target) === digestBefore, 'busy: the refused copy-in wrote the v2 data root');
    const recorded = readCliOperation(binary, target, 'busy:status');
    assertValue(recorded.recorded === false, `busy: the refused copy-in recorded ${JSON.stringify(recorded.operation)}`);
    recordFault('natively-busy-source', run, check.code, true, {
      fence: 'flock(LOCK_EX) on usage.sqlite.manager.lock',
      writePath: 'upgrade run --legacy <busy source> --target <v2 root>',
      preflightExitCode: blocked.code,
      persistedStage: recorded.recorded === false ? null : recorded.operation?.stage,
    });
  } finally {
    holder.kill('SIGKILL');
  }
}

function recordFault(name, command, code, targetRootUnchanged, extra = {}) {
  evidence.faults.push({ name, exitCode: command.code, code, targetRootUnchanged, ...extra });
  stableFacts.push(`fault:${name}:${command.code}:${code}`);
}

// ---------------------------------------------------------------------------
// rollback
// ---------------------------------------------------------------------------

// proveRollback prepares a plan, applies it exactly as printed against a real
// 1.x stack that runs on the very directory `--verify` fingerprints, lets that
// stack write, records the verdict, and proves the manager refuses a probe of
// its own listener. It then proves the recorded `rolled_back` root still admits
// only the Upgrade surface.
//
// Every host-side read of that directory happens with the stack stopped. The
// product fences a host read of a 1.x source (`upgrade preflight` takes the
// source lock), and a container's flock is the host's flock wherever the mount
// is a real bind mount — Linux — while Docker Desktop's file-sharing boundary
// hides it on macOS. A proof that reads the directory while the 1.x serves
// therefore passes for the wrong reason on one host and is refused on the other,
// so the write is proved between two stopped readings and the verdict's own
// fingerprint is proved to differ from the pre-rollback copy.
async function proveRollback(binary, workspace, fixture) {
  const source = fs.realpathSync(workspace.main.source);
  const target = workspace.main.target;
  const project = PREFIX;
  const port = ports.take();
  const managerPort = ports.take();
  assertValue(port !== managerPort, 'rollback: the plan port and the manager port must be two different ports');

  // The volume the plan will name is decided here, from the plan's own naming
  // rule, and bound to the 1.x directory before anything is prepared: a Docker
  // local volume bound to that directory makes the container's writes land in
  // the very files `--verify` reads. The probe write proves the binding on the
  // host, so a host whose Docker cannot bind the directory fails here instead of
  // recording a verdict against another directory.
  const expectedVolume = `${project}_cpa-manager-plus-data`;
  const sameVolume = createSameVolumeDirectory(expectedVolume, source);

  const prepare = runCli(
    binary,
    [
      'upgrade', 'rollback', '--prepare',
      '--legacy', source,
      '--target', target,
      '--project', project,
      '--port', String(port),
      '--manager-addr', `127.0.0.1:${managerPort}`,
      '--legacy-image', `${fixture.rollbackImage}`,
      '--json',
    ],
    'rollback:prepare',
    { expectExit: 0 },
  );
  const plan = parseJson(prepare.stdout, 'rollback:prepare');
  assertValue(plan.volume === expectedVolume, `rollback: plan volume=${plan.volume} but this run bound ${expectedVolume} to the 1.x directory`);
  assertValue(path.resolve(plan.sourceRoot) === source, `rollback: the plan names ${plan.sourceRoot} as the 1.x root, not the directory --verify reads (${source})`);
  assertValue(plan.port === port && plan.port !== managerPort, `rollback: plan port=${plan.port} manager port=${plan.managerPort}`);
  assertValue(plan.managerPort === managerPort, `rollback: plan manager port=${plan.managerPort} want ${managerPort}`);
  assertValue(plan.image === fixture.rollbackImage, `rollback: plan image=${plan.image}`);
  assertValue(typeof plan.composeFile === 'string' && plan.composeFile.includes(plan.volume), 'rollback: the plan carries no compose file');

  // Apply the plan exactly as printed: the compose file on stdin.
  trackComposeProject(project, plan.composeFile);
  const up = sh('docker', ['compose', '-p', project, '-f', '-', 'up', '-d'], { input: plan.composeFile, timeout: 300_000 });
  evidence.commands.push(commandRecord(`docker compose -p ${project} -f - up -d`, up.code));
  const composeResources = trackComposeResources(project);
  if (up.code !== 0) {
    throw new Error(`rollback: apply the plan: ${redactor.redact(tail(up.stderr))}`);
  }
  await waitForHealth(port, 'restarted 1.x /health');

  // The restarted 1.x writes after the copy-in, into the volume that is the
  // directory --verify fingerprints. The response is recorded as it happened: a
  // hard-coded status is not evidence about this run.
  const writtenRowHash = sha256(Buffer.from('u1d:rollback:written-after-copy-in'));
  const written = await v1JsonAt(port, 'POST', '/v0/management/usage/import', [
    {
      event_hash: writtenRowHash,
      timestamp_ms: dayStartMS(DATASET_DAYS[2]) + 6 * 3_600_000,
      timestamp: new Date(dayStartMS(DATASET_DAYS[2]) + 6 * 3_600_000).toISOString(),
      model: DATASET_MODELS[0].model,
      provider: DATASET_MODELS[0].provider,
      executor_type: DATASET_MODELS[0].executor,
      api_key_hash: sha256(Buffer.from(DATASET_KEYS[0].name)),
      input_tokens: 7,
      output_tokens: 3,
      total_tokens: 10,
      failed: false,
    },
  ]);
  assertValue(
    written.status >= 200 && written.status < 300,
    `rollback: the restarted 1.x answered HTTP ${written.status} for the new row: ${redactor.redact(JSON.stringify(written.body))}`,
  );
  assertValue(written.body?.added === 1, `rollback: the restarted 1.x did not accept the new row: ${redactor.redact(JSON.stringify(written.body))}`);

  // The restarted 1.x wrote through the plan's bind mount as its own user. The
  // host reads that same directory next (preflight, the verdict and the row
  // count), so the tree is given back to this process first while the stack
  // keeps serving: chown is metadata only and does not disturb its open handles.
  normalizeScratchOwnership(source, 'rollback-live');

  // The directory's post-write fingerprint, read where the product can read it
  // honestly on every host. `upgrade preflight` takes the source lock, and the
  // running 1.x holds it: on a host where the container's flock and the host's
  // are one lock (Linux, a bind mount) that read is refused — correctly — with
  // `upgrade_source_busy`. Docker Desktop's file-sharing boundary hides the
  // flock from the host, so a reading taken while the 1.x served only ever
  // worked on macOS. The 1.x is stopped for the reading and started again for
  // the verdict below, which proves it serves.
  const containerName = (composeResources.containers ?? [])[0];
  assertValue(
    Boolean(containerName),
    `rollback: the applied plan reported no container: ${redactor.redact(JSON.stringify(composeResources.containers))}`,
  );
  stopContainer(containerName);
  await waitForContainerExit(containerName);
  normalizeScratchOwnership(source, 'rollback-post-write');
  const postWriteProbe = runCli(
    binary,
    ['upgrade', 'preflight', '--legacy', source, '--json'],
    'rollback:post-write-fingerprint',
    { expectExit: 0 },
  );
  const postWriteReport = parseJson(postWriteProbe.stdout, 'rollback:post-write-fingerprint');
  const postWriteFingerprint = postWriteReport.fingerprint;
  assertValue(
    SHA256_PATTERN.test(String(postWriteFingerprint ?? '')),
    `rollback: the stopped post-write directory has no fingerprint (${redactor.redact(JSON.stringify(postWriteReport.checks ?? []).slice(0, 300))})`,
  );
  // Both readings describe a stopped directory, so the only difference between
  // them is what the restarted 1.x wrote.
  assertValue(
    postWriteFingerprint !== plan.sourceFingerprint,
    'rollback: the restarted 1.x write did not change the directory the verdict reads',
  );
  const restart = sh('docker', ['start', containerName]);
  evidence.commands.push(commandRecord(`docker start ${containerName}`, restart.code));
  if (restart.code !== 0) {
    throw new Error(`rollback: restart the 1.x for the verdict: ${redactor.redact(tail(restart.stderr))}`);
  }
  await waitForHealth(port, 'restarted 1.x /health after the post-write reading');

  // A probe answered by the manager itself is refused, and the refusal must
  // leave the recorded row exactly as it was.
  const beforeSelfProbe = readCliOperation(binary, target, 'rollback:status-before-self-probe');
  const managerName = `${PREFIX}-selfprobe`;
  startHostManager({ name: managerName, binary, dataDir: target, port: managerPort, adminKey: ADMIN_V2, legacyRoot: '' });
  await waitForHealth(managerPort, `${managerName} /health`, 503);
  const selfProbe = runCli(
    binary,
    [
      'upgrade', 'rollback', '--verify',
      '--legacy', source,
      '--target', target,
      '--backup', plan.backupDirectory,
      '--legacy-image', plan.image,
      '--status-url', `http://127.0.0.1:${managerPort}/health`,
      '--manager-addr', `127.0.0.1:${managerPort}`,
      '--json',
    ],
    'rollback:self-probe',
    { expectExit: 1 },
  );
  const selfProbeCode = typedCode(selfProbe);
  assertValue(selfProbeCode === 'upgrade_rollback_probe_self', `rollback: self-probe code=${selfProbeCode}`);
  await stopHostManager(managerName);
  const afterSelfProbe = readCliOperation(binary, target, 'rollback:status-after-self-probe');
  assertValue(
    afterSelfProbe.operation?.stage === beforeSelfProbe.operation?.stage &&
      afterSelfProbe.operation?.updatedAtMs === beforeSelfProbe.operation?.updatedAtMs,
    `rollback: the refused self-probe changed the recorded row (stage ${beforeSelfProbe.operation?.stage}→${afterSelfProbe.operation?.stage}, updatedAtMs ${beforeSelfProbe.operation?.updatedAtMs}→${afterSelfProbe.operation?.updatedAtMs})`,
  );

  normalizeScratchOwnership(source, 'rollback-verify');
  // The v2 stack is down, so the probe can only be answered by the restarted 1.x.
  const verify = runCli(
    binary,
    [
      'upgrade', 'rollback', '--verify',
      '--legacy', source,
      '--target', target,
      '--backup', plan.backupDirectory,
      '--legacy-image', plan.image,
      '--status-url', planHealthURL(plan),
      '--manager-addr', `127.0.0.1:${managerPort}`,
      '--json',
    ],
    'rollback:verify',
    { expectExit: 0 },
  );
  const operation = parseJson(verify.stdout, 'rollback:verify');
  assertValue(operation.stage === 'rolled_back', `rollback: recorded stage=${operation.stage}`);
  assertValue(operation.rollback?.image === plan.image, `rollback: recorded image=${operation.rollback?.image}`);

  const observed = operation.rollback?.sourceFingerprint;
  assertValue(SHA256_PATTERN.test(String(observed ?? '')), 'rollback: no observed fingerprint was recorded');
  // What the verdict must show, on every host: it described the post-write
  // directory and not the pre-rollback copy. The write moved that directory (the
  // stopped readings above, which differ only by the write), and the row the 1.x
  // wrote is found in the same directory afterwards. A digest equality with a
  // host reading is deliberately not asserted: a serving 1.x holds the source
  // lock, so a host reading while it serves is refused, and a stopped reading is
  // a different state — the values differ by the WAL a live 1.x keeps, not by
  // anything the verdict got wrong.
  assertValue(
    observed !== plan.sourceFingerprint,
    `rollback: verify observed ${observed}, the pre-rollback source fingerprint; the verdict did not read the volume the restarted 1.x wrote`,
  );

  const persisted = readCliOperation(binary, target, 'rollback:status');
  assertValue(persisted.operation?.stage === 'rolled_back', 'rollback: the persisted stage is not rolled_back');

  // Release the restarted stack before reading the volume from the host and
  // before the manager restart: the source mount must be free for the surface
  // to resolve its credential from it, exactly as an operator's restart is.
  const composeRelease = await registry.releaseKind('composeProject');
  assertValue(
    composeRelease.ok,
    `rollback: releasing the restarted 1.x stack failed: ${composeRelease.errors.join('; ')}`,
  );
  assertValue(await waitForPortClosed(port), 'rollback: the restarted 1.x still holds its port after the stack was released');

  // The recorded `rolled_back` root still admits only the Upgrade surface: this
  // is a manager restart on that root, not an inference from the CLI status.
  const rolledBackSurface = await proveRolledBackSurface(binary, target, source);

  // The row that 1.x wrote is in the directory the verdict fingerprinted, read
  // outside the container from the host path itself.
  const rowsInLiveDirectory = countSeededEventRow(source, writtenRowHash);
  assertValue(
    rowsInLiveDirectory === 1,
    `rollback: the row the restarted 1.x wrote is not in ${source} (found ${rowsInLiveDirectory}); verify's evidence did not describe this directory`,
  );

  evidence.rollback = {
    plan: {
      exitCode: prepare.code,
      digest: plan.digest,
      image: plan.image,
      volume: plan.volume,
      port: plan.port,
      managerPort: plan.managerPort,
      backupDirectory: plan.backupDirectory,
      backupManifestSha256: plan.backupManifestSha256,
      sourceRoot: plan.sourceRoot,
      sourceFingerprint: plan.sourceFingerprint,
      reportSha256: sha256(prepare.stdout),
    },
    sameVolumeDirectory: {
      volume: plan.volume,
      directory: source,
      mountpoint: sameVolume.mountpoint,
      probeWrittenInContainer: true,
      probeObservedOnHost: true,
    },
    applied: { exitCode: up.code, health: 'ok', containers: composeResources.containers, networks: composeResources.networks },
    v1WroteAfterCopyIn: { status: written.status, added: written.body.added },
    postWriteFingerprint: {
      exitCode: postWriteProbe.code,
      fingerprint: postWriteFingerprint,
      movedByTheWrite: postWriteFingerprint !== plan.sourceFingerprint,
    },
    selfProbe: {
      exitCode: selfProbe.code,
      code: selfProbeCode,
      operationRowUnchanged: {
        stage: beforeSelfProbe.operation?.stage,
        updatedAtMs: beforeSelfProbe.operation?.updatedAtMs,
      },
    },
    verify: {
      exitCode: verify.code,
      stage: operation.stage,
      image: operation.rollback?.image,
      statusUrl: operation.rollback?.statusUrl,
      observedFingerprint: observed,
      observedFingerprintDiffersFromPlan: observed !== plan.sourceFingerprint,
      reportSha256: sha256(verify.stdout),
    },
    writtenRowInLiveDirectory: rowsInLiveDirectory,
    persistedStage: persisted.operation?.stage,
    rolledBackSurface,
  };
  stableFacts.push(`rollback:${operation.stage}:${stableImageRef(operation.rollback?.image)}`);
  stableFacts.push('rollback:same-volume-write-observed');
}

// stableImageRef normalises an image reference this run owns, so a stable fact
// can be compared between runs: the alias carries the run's prefix by design
// (`cpampd<runid>/upgrade:vX.Y.Z`), and only its tag says anything the runs
// share. Any other reference is left exactly as it is.
export function stableImageRef(reference, prefix = PREFIX) {
  const text = String(reference ?? '');
  return text.startsWith(`${prefix}/`) ? `<run>${text.slice(prefix.length)}` : text;
}

function planHealthURL(plan) {
  return `http://127.0.0.1:${plan.port}/health`;
}

// createSameVolumeDirectory creates the plan's named volume as a Docker local
// volume bound to the 1.x directory itself. The compose document the plan prints
// then mounts that name, so the restarted container writes into the very files
// `--verify --legacy <directory>` fingerprints. A probe write through the volume
// proves the binding with the host's own filesystem, not with inspect metadata.
function createSameVolumeDirectory(volume, directory) {
  registry.track('volume', volume, () => releaseDockerResource('volume', volume));
  const createdVolume = sh('docker', [
    'volume', 'create',
    '--driver', 'local',
    '--opt', 'type=none',
    '--opt', 'o=bind',
    '--opt', `device=${directory}`,
    volume,
  ]);
  evidence.commands.push(commandRecord(`docker volume create ${volume} (bind ${directory})`, createdVolume.code));
  if (createdVolume.code !== 0) {
    throw new Error(`rollback: create ${volume} bound to ${directory}: ${redactor.redact(tail(createdVolume.stderr))}`);
  }
  const inspected = sh('docker', ['volume', 'inspect', '-f', '{{.Mountpoint}}', volume]);
  assertValue(inspected.code === 0, `rollback: inspect ${volume}: ${redactor.redact(inspected.stderr.trim())}`);
  const mountpoint = inspected.stdout.trim();

  const probeName = `${PREFIX}-volume-probe`;
  const probeContent = `cpampd-same-volume-${runId}`;
  registry.track('container', probeName, () => releaseDockerResource('container', probeName));
  const probe = sh('docker', [
    'run', '--rm', '--name', probeName,
    '-v', `${volume}:/data`,
    'alpine:3.21', 'sh', '-c', `printf %s '${probeContent}' > /data/.cpampd-volume-probe`,
  ]);
  evidence.commands.push(commandRecord(`docker run ${probeName} (same-volume probe)`, probe.code));
  if (probe.code !== 0) {
    throw new Error(`rollback: write through ${volume}: ${redactor.redact(tail(probe.stderr))}`);
  }
  registry.untrack('container', probeName);
  const probeFile = path.join(directory, '.cpampd-volume-probe');
  const observed = fs.existsSync(probeFile) ? fs.readFileSync(probeFile, 'utf8') : null;
  fs.rmSync(probeFile, { force: true });
  assertValue(
    observed === probeContent,
    `rollback: a write through ${volume} did not appear in ${directory}; the volume is not the directory --verify reads, so the verdict would not describe the restarted 1.x`,
  );
  assertValue(!fs.existsSync(probeFile), 'rollback: the same-volume probe file could not be removed before the plan fingerprint');
  return { volume, directory, mountpoint };
}

function trackComposeProject(project, composeFile) {
  registry.track('composeProject', project, () => releaseComposeProject(project, composeFile), { composeFile });
}

function trackComposeResources(project) {
  const containers = dockerNames(['ps', '-a', '--filter', `label=com.docker.compose.project=${project}`, '--format', '{{.Names}}']) ?? [];
  for (const name of containers) {
    try {
      registry.track('container', name, () => releaseDockerResource('container', name));
    } catch {
      // A compose container outside this run's prefix is not this run's to
      // remove; the project release still brings the stack down.
    }
  }
  const networks = dockerNames(['network', 'ls', '--filter', `label=com.docker.compose.project=${project}`, '--format', '{{.Name}}']) ?? [];
  for (const name of networks) {
    try {
      registry.track('network', name, () => releaseDockerResource('network', name));
    } catch {
      /* see above */
    }
  }
  return { containers, networks };
}

function releaseComposeProject(project, composeFile) {
  const down = sh('docker', ['compose', '-p', project, '-f', '-', 'down', '-t', '20', '--remove-orphans'], {
    input: composeFile,
    timeout: 180_000,
  });
  evidence.commands.push(commandRecord(`docker compose -p ${project} -f - down`, down.code));
  if (down.code !== 0) {
    throw new Error(`docker compose -p ${project} down: ${redactor.redact(tail(down.stderr))}`);
  }
}

// releaseImageAlias removes this run's rollback tag, never the image bytes and
// never a shared reference. The inspect before the removal is the ownership
// proof: a tag that resolves to another image is not this run's to remove, and a
// tag that is already gone is already released.
function releaseImageAlias(reference, expectedImageId) {
  const current = imageId(reference);
  if (!current) return;
  if (!expectedImageId || current !== expectedImageId) {
    throw new Error(
      `refusing to remove ${reference}: it resolves to ${current}, not to this run's fixture ${expectedImageId ?? 'unknown'}`,
    );
  }
  const removed = dockerImageWrite(['image', 'rm', reference]);
  if (removed.code !== 0) {
    throw new Error(`docker image rm ${reference}: ${redactor.redact(tail(removed.stderr, 400))}`);
  }
  const after = imageId(reference);
  if (after) {
    throw new Error(`docker image rm ${reference} left the tag resolving to ${after}`);
  }
}

// releaseDockerResource removes one object this run tracked. A missing object is
// already released; anything else that fails is thrown so the registry reports
// it instead of dropping the entry.
function releaseDockerResource(kind, name) {
  if (!dockerObjectExists(kind, name)) return;
  const command =
    kind === 'container'
      ? ['rm', '-f', name]
      : kind === 'volume'
        ? ['volume', 'rm', '-f', name]
        : kind === 'network'
          ? ['network', 'rm', name]
          : null;
  if (!command) throw new Error(`no release command for ${kind}`);
  const result = sh('docker', command);
  if (result.code !== 0) {
    throw new Error(`docker ${command.join(' ')}: ${redactor.redact(tail(result.stderr, 400))}`);
  }
}

function dockerObjectExists(kind, name) {
  const command =
    kind === 'container'
      ? ['inspect', '--type', 'container', name]
      : kind === 'volume'
        ? ['volume', 'inspect', name]
        : ['network', 'inspect', name];
  return sh('docker', command).code === 0;
}

// readCliOperation reads the recorded row through the CLI, so the comparison
// after a refusal uses the product's own reader rather than raw SQL.
function readCliOperation(binary, targetRoot, label) {
  const status = runCli(binary, ['upgrade', 'status', '--db-path', path.join(targetRoot, 'usage.sqlite'), '--json'], label, {
    expectExit: 0,
  });
  return parseJson(status.stdout, label);
}

// countSeededEventRow reads the 1.x store on the host at the directory the
// verdict fingerprinted. The restarted 1.x wrote through the volume, so finding
// the row here is the same-directory proof. The open is read-only because the
// container writes as its own user: the harness only ever reads these files.
function countSeededEventRow(root, eventHash) {
  const database = path.join(root, 'usage.sqlite');
  let db;
  try {
    db = new DatabaseSync(database, { readOnly: true });
    const row = db.prepare('select count(*) as count from usage_events where event_hash = ?').get(eventHash);
    return Number(row?.count ?? 0);
  } catch (error) {
    throw new Error(
      `rollback: read ${database} read-only after the stack stopped (${redactor.redact(error?.message || error)}); an unclean 1.x shutdown leaves a WAL this host cannot join`,
    );
  } finally {
    db?.close();
  }
}

// probeClosedSurfaces proves the routes an unfinished operation keeps closed
// really answer 404. The fixed surface serves only /health, the Bootstrap shell,
// the Upgrade entry leaf and its two reads, so each of these is a route the full
// surface owns and this one must not.
const CLOSED_SURFACE_PATHS = Object.freeze([
  '/management.html',
  '/usage-service/info',
  '/v0/management/model-prices',
  '/v0/management/models',
]);

function probeClosedSurfaces(port, label, headers = {}) {
  const answers = {};
  for (const pathName of CLOSED_SURFACE_PATHS) {
    const answer = httpJson('GET', `http://127.0.0.1:${port}${pathName}`, { headers });
    assertValue(
      answer.status === 404,
      `${label}: ${pathName} = ${answer.status} want 404: only the Upgrade surface is admitted while the operation is unfinished`,
    );
    answers[pathName] = answer.status;
  }
  return answers;
}

// proveRolledBackSurface restarts the manager on the recorded `rolled_back` root
// and proves that admission still serves only the Upgrade surface: /health 503,
// the wizard status readable with the source key alone, and the console, key and
// gateway routes closed.
async function proveRolledBackSurface(binary, target, sourceRoot) {
  const port = ports.take();
  const name = `${PREFIX}-rolledback-surface`;
  const label = 'admission(rolled_back)';
  startHostManager({ name, binary, dataDir: target, port, adminKey: ADMIN_V2, legacyRoot: sourceRoot });
  try {
    await waitForHealth(port, `${name} /health`, 503);
    const base = `http://127.0.0.1:${port}/.well-known/cpamp-bootstrap/api/v2/installation/upgrade`;
    const status = httpJson('GET', `${base}/status`, { headers: { Authorization: `Bearer ${ADMIN_V1}` } });
    assertValue(status.status === 200, `${label}: status = ${status.status} want 200 from the Manager store`);
    assertValue(status.body?.recorded === true, `${label}: status recorded = ${status.body?.recorded}`);
    assertValue(status.body?.stage === 'rolled_back', `${label}: status stage = ${status.body?.stage} want rolled_back`);
    assertValue(
      status.body?.nextAction === expectedNextAction('rolled_back'),
      `${label}: status nextAction = ${status.body?.nextAction} want ${expectedNextAction('rolled_back')}`,
    );
    assertNoSecretInBody(`${label}: status`, status.body, [ADMIN_V1, ADMIN_V2, sourceRoot, SCRATCH, target]);
    const foreign = httpJson('GET', `${base}/status`, { headers: { Authorization: `Bearer ${ADMIN_V2}` } });
    assertValue(foreign.status === 401, `${label}: status with the v2 Admin key = ${foreign.status} want 401`);
    const closed = probeClosedSurfaces(port, label, { Authorization: `Bearer ${ADMIN_V1}` });
    return {
      health: 503,
      status: { status: status.status, recorded: status.body.recorded, stage: status.body.stage, nextAction: status.body.nextAction },
      credential: { sourceAdminKey: status.status, v2AdminKey: foreign.status },
      closedSurfaces: closed,
    };
  } finally {
    await stopHostManager(name);
  }
}

// ---------------------------------------------------------------------------
// host processes
// ---------------------------------------------------------------------------

function startHostManager({ name, binary, dataDir, port, adminKey, legacyRoot }) {
  // The only producer that spawns before it tracks; without this check a fence
  // hit between the spawn and `track` would leave an untracked process behind.
  registry.assertCreationAllowed('hostProcess', name);
  const logFile = path.join(SCRATCH, `${name}.log`);
  const env = {
    ...process.env,
    USAGE_DATA_DIR: dataDir,
    USAGE_DB_PATH: path.join(dataDir, 'usage.sqlite'),
    CPA_MANAGER_DATA_KEY_PATH: path.join(dataDir, 'data.key'),
    CPA_MANAGER_ADMIN_KEY: adminKey,
    HTTP_ADDR: `127.0.0.1:${port}`,
  };
  if (legacyRoot) env.CPAMP_LEGACY_ROOT = legacyRoot;
  const out = fs.openSync(logFile, 'w');
  const child = spawn(binary, [], { env, stdio: ['ignore', out, out], detached: true });
  child.unref();
  hostManagers.set(name, { child, logFile, port });
  registry.track('hostProcess', name, () => stopHostProcess(name), { pid: child.pid, port });
}

// stopHostProcess proves the manager actually stopped: SIGTERM, wait for the
// listener to close, then SIGKILL and wait again. A process that still holds the
// port is a cleanup failure, not a silent success.
async function stopHostProcess(name) {
  const entry = hostManagers.get(name);
  if (!entry) return;
  const signalGroup = (signal) => {
    try {
      process.kill(-entry.child.pid, signal);
      return true;
    } catch {
      try {
        entry.child.kill(signal);
        return true;
      } catch {
        return false;
      }
    }
  };
  signalGroup('SIGTERM');
  let closed = await waitForPortClosed(entry.port, 20_000);
  if (!closed) {
    signalGroup('SIGKILL');
    closed = await waitForPortClosed(entry.port, 10_000);
  }
  hostManagers.delete(name);
  if (!closed) {
    throw new Error(`host manager ${name} still holds 127.0.0.1:${entry.port} after SIGTERM and SIGKILL`);
  }
}

async function stopHostManager(name) {
  return registry.releaseEntry('hostProcess', name);
}

async function waitForPortClosed(port, timeout = 30_000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    if (!(await portOpen(port))) return true;
    await sleep(100);
  }
  return !(await portOpen(port));
}

function portOpen(port) {
  return new Promise((resolvePromise) => {
    const socket = net.createConnection({ host: '127.0.0.1', port });
    const done = (open) => {
      socket.removeAllListeners();
      socket.destroy();
      resolvePromise(open);
    };
    socket.setTimeout(500, () => done(false));
    socket.once('connect', () => done(true));
    socket.once('error', () => done(false));
  });
}

async function waitForContainerExitOrHealth(name, port) {
  // The manager comes up with a listener before admission finishes; both a
  // 503 and a 200 are "the listener is up".
  await waitFor(async () => {
    const probe = await fetch(`http://127.0.0.1:${port}/health`).catch(() => null);
    return probe !== null;
  }, `${name} listener`, 30_000);
}

// ---------------------------------------------------------------------------
// containers
// ---------------------------------------------------------------------------

// containerUserArgv gives a container that writes a harness-owned mount the
// invoking identity. A 1.x image runs as its own user — root on a Linux runner —
// so whatever it writes through the bind mount comes back owned by a uid this
// process does not have, and every host read of that tree then fails: Node's
// native recursive copy aborts the whole process on the C++ directory iterator
// instead of throwing. The mount is the harness's own scratch tree, so the
// container is given exactly the identity that owns it.
function containerUserArgv() {
  const uid = typeof process.getuid === 'function' ? process.getuid() : null;
  const gid = typeof process.getgid === 'function' ? process.getgid() : null;
  return uid === null || gid === null ? [] : ['--user', `${uid}:${gid}`];
}

function startContainer({ name, image, port, dataDir, adminKey }) {
  // Tracked before `docker run`: a command that fails after creating the
  // container still leaves it in the cleanup set.
  registry.track('container', name, () => releaseDockerResource('container', name));
  const result = sh('docker', [
    'run', '-d',
    '--name', name,
    '-p', `127.0.0.1:${port}:${port}`,
    '-e', `HTTP_ADDR=0.0.0.0:${port}`,
    '-e', 'USAGE_DATA_DIR=/data',
    '-e', 'USAGE_DB_PATH=/data/usage.sqlite',
    '-e', 'CPA_MANAGER_DATA_KEY_PATH=/data/data.key',
    '-e', `CPA_MANAGER_ADMIN_KEY=${adminKey}`,
    '-e', 'USAGE_COLLECTOR_MODE=disabled',
    '-e', 'USAGE_CORS_ORIGINS=*',
    ...containerUserArgv(),
    '-v', `${dataDir}:/data`,
    image,
  ]);
  evidence.commands.push(commandRecord(`docker run ${name}`, result.code));
  if (result.code !== 0) {
    throw new Error(`start ${name}: ${redactor.redact(tail(result.stderr))}`);
  }
}

function stopContainer(name) {
  const result = sh('docker', ['stop', '-t', '20', name]);
  evidence.commands.push(commandRecord(`docker stop ${name}`, result.code));
  if (result.code !== 0) {
    throw new Error(`stop ${name}: ${redactor.redact(tail(result.stderr))}`);
  }
}

async function removeContainer(name) {
  const outcome = await registry.releaseEntry('container', name);
  if (!outcome.released && !outcome.skipped) {
    throw new Error(`remove ${name}: ${outcome.error}`);
  }
}

async function waitForContainerExit(name) {
  await waitFor(() => sh('docker', ['inspect', '-f', '{{.State.Running}}', name]).stdout.trim() === 'false', `${name} stopped`, 60_000);
}

async function waitForHealth(port, label, expectStatus = 200) {
  await waitFor(async () => {
    try {
      const response = await fetch(`http://127.0.0.1:${port}/health`);
      return response.status === expectStatus;
    } catch {
      return false;
    }
  }, label, 60_000);
}

// ---------------------------------------------------------------------------
// v1 API
// ---------------------------------------------------------------------------

async function v1Json(port, method, pathName, body) {
  return v1JsonAt(port, method, pathName, body);
}

async function v1JsonAt(port, method, pathName, body) {
  const response = await fetch(`http://127.0.0.1:${port}${pathName}`, {
    method,
    headers: { Authorization: `Bearer ${ADMIN_V1}`, 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await response.text();
  let parsed = null;
  try {
    parsed = text ? JSON.parse(text) : null;
  } catch {
    parsed = { raw: text.slice(0, 400) };
  }
  return { status: response.status, body: parsed };
}

function httpJson(method, url, { headers = {}, body } = {}) {
  const result = spawnSync(
    process.execPath,
    [
      '-e',
      `
      const [method, url, headersJSON, body] = process.argv.slice(1);
      (async () => {
        const response = await fetch(url, {
          method,
          headers: JSON.parse(headersJSON),
          body: body ? body : undefined,
        });
        const text = await response.text();
        // The Allow verdict is the 405 contract a proved credential sees, so it
        // travels with the answer instead of being inferred from the status.
        process.stdout.write(JSON.stringify({ status: response.status, allow: response.headers.get('allow'), body: text }));
      })().catch((error) => {
        process.stdout.write(JSON.stringify({ status: 0, body: String(error) }));
      });
      `,
      method,
      url,
      JSON.stringify(headers),
      body ?? '',
    ],
    { encoding: 'utf8', maxBuffer: 32 * 1024 * 1024 },
  );
  if (result.status !== 0) {
    return { status: 0, body: null, raw: result.stderr };
  }
  const parsed = JSON.parse(result.stdout || '{"status":0,"body":""}');
  let payload = null;
  try {
    payload = parsed.body ? JSON.parse(parsed.body) : null;
  } catch {
    payload = { raw: String(parsed.body).slice(0, 400) };
  }
  return { status: parsed.status, allow: parsed.allow ?? null, body: payload, raw: parsed.body };
}

// ---------------------------------------------------------------------------
// CLI
// ---------------------------------------------------------------------------

// withDeclaredSourceVersion keeps one rule for every source-reading command:
// preflight, run and reconcile always declare the pinned accepted release. A
// caller cannot forget the flag, and a flag the caller already passed is kept.
export function withDeclaredSourceVersion(cliArgs, release) {
  const declared = [...cliArgs];
  if (declared[0] !== 'upgrade' || !['preflight', 'run', 'reconcile'].includes(declared[1])) return declared;
  if (declared.includes('--source-version')) return declared;
  if (!RELEASE_TAG_PATTERN.test(String(release ?? ''))) {
    throw new Error(
      `the harness must know the pinned accepted release before it runs upgrade ${declared[1]}, got ${JSON.stringify(release)}`,
    );
  }
  return [declared[0], declared[1], '--source-version', release, ...declared.slice(2)];
}

// shellTokens splits a printed command the way a shell reads the quoting
// ShellQuote produces: single-quoted runs (including the '\'' escape) and
// backslash escapes outside them. The harness asserts the exact argv of the next
// command it prints, so a substring match cannot accept a different flag value
// or an extra word.
export function shellTokens(command) {
  const text = String(command ?? '');
  const tokens = [];
  let current = '';
  let started = false;
  for (let index = 0; index < text.length; index += 1) {
    const char = text[index];
    if (char === "'") {
      started = true;
      index += 1;
      while (index < text.length && text[index] !== "'") {
        current += text[index];
        index += 1;
      }
      continue;
    }
    if (char === '\\' && index + 1 < text.length) {
      current += text[index + 1];
      started = true;
      index += 1;
      continue;
    }
    if (char === ' ' || char === '\t' || char === '\n') {
      if (started) {
        tokens.push(current);
        current = '';
        started = false;
      }
      continue;
    }
    current += char;
    started = true;
  }
  if (started) tokens.push(current);
  return tokens;
}

function runCli(binary, cliArgs, name, { expectExit = null, allowMissingBinary = false, env = null } = {}) {
  const effectiveArgs = withDeclaredSourceVersion(cliArgs, acceptedRelease);
  const result = sh(binary, effectiveArgs, { timeout: 600_000, ...(env ? { env } : {}) });
  evidence.commands.push(commandRecord(name, result.code, result.stdout, result.stderr));
  if (result.code === null && !allowMissingBinary) {
    throw new Error(`${name}: the command did not run: ${redactor.redact(result.stderr.trim())}`);
  }
  if (expectExit !== null && result.code !== expectExit) {
    throw new Error(
      `${name}: exit=${result.code} want ${expectExit}\n${redactor.redact(tail(result.stderr))}${redactor.redact(tail(result.stdout))}`,
    );
  }
  return result;
}

function managerBinary() {
  return path.join(SCRATCH, 'cpa-manager-plus');
}

function parseJson(text, label) {
  try {
    return JSON.parse(text);
  } catch (error) {
    throw new Error(`${label}: the report is not JSON (${redactor.redact(error.message)}): ${redactor.redact(tail(text, 400))}`);
  }
}

function typedCode(command) {
  for (const text of [command.stdout, command.stderr]) {
    const match = /"errorCode":"([a-z0-9_]+)"/.exec(text) || /(upgrade_[a-z0-9_]+)/.exec(text);
    if (match) return match[1];
  }
  return null;
}

// ---------------------------------------------------------------------------
// plumbing
// ---------------------------------------------------------------------------

function parseArgs(argv) {
  const parsed = { reuseFixture: false, keep: false, v1Tag: DEFAULT_V1_TAG, sourceTag: DEFAULT_SOURCE_TAG, verifyEvidence: null, expectHead: null };
  for (let index = 0; index < argv.length; index += 1) {
    const value = argv[index];
    switch (value) {
      case '--reuse-fixture':
        parsed.reuseFixture = true;
        break;
      case '--keep':
        parsed.keep = true;
        break;
      case '--v1-tag':
        parsed.v1Tag = argv[++index];
        break;
      case '--source-tag':
        parsed.sourceTag = argv[++index];
        break;
      case '--verify-evidence':
        parsed.verifyEvidence = argv[++index];
        break;
      case '--expect-head':
        parsed.expectHead = argv[++index];
        break;
      case '--help':
      case '-h':
        console.log(
          'usage: node bin/ci/upgrade-docker.mjs [--reuse-fixture] [--keep] [--v1-tag TAG] [--source-tag TAG]\n' +
            '       node bin/ci/upgrade-docker.mjs --verify-evidence FILE [--expect-head SHA]',
        );
        process.exit(0);
        break;
      default:
        throw new Error(`unknown argument ${value}`);
    }
  }
  return parsed;
}

// verifyEvidenceCli re-checks one evidence document without Docker. CI runs it on
// exactly the file this run wrote, so an upload can only carry a document that
// matches its own canonical hash and names the commit the workflow checked out.
function verifyEvidenceCli({ verifyEvidence, expectHead }) {
  let document;
  try {
    document = JSON.parse(fs.readFileSync(verifyEvidence, 'utf8'));
  } catch (error) {
    console.error(`upgrade-docker: cannot read the evidence ${verifyEvidence}: ${redactor.redact(error?.message || error)}`);
    return 1;
  }
  const verdict = verifyEvidenceDocument(document, { expectHead });
  if (!verdict.ok) {
    console.error(`upgrade-docker: ${verifyEvidence} is not a valid evidence document: ${verdict.problems.join('; ')}`);
    return 1;
  }
  console.log(
    `upgrade-docker: ${verifyEvidence} is bound to ${document.implementation.headSha} (${document.verdict}, ${document.exitCode ?? 'no exit code'})`,
  );
  return 0;
}

function sh(command, commandArgs, { cwd = ROOT, input, env, timeout = 120_000 } = {}) {
  const result = spawnSync(command, commandArgs, {
    cwd,
    input,
    env: env ? { ...process.env, ...env } : process.env,
    encoding: 'utf8',
    maxBuffer: 256 * 1024 * 1024,
    timeout,
  });
  return {
    code: result.status === null ? null : result.status,
    stdout: result.stdout ?? '',
    stderr: result.stderr ?? result.error?.message ?? '',
  };
}

function git(commandArgs) {
  return sh('git', commandArgs);
}

// dockerImageWrite is the only path to a Docker subcommand that creates or
// removes an image reference. The argv is checked before spawnSync, so a
// reference outside the harness's repositories never reaches Docker.
function dockerImageWrite(commandArgs, options = {}) {
  assertImageWriteArgv(commandArgs);
  return sh('docker', commandArgs, options);
}

function dockerJson(commandArgs) {
  const result = sh('docker', commandArgs);
  if (result.code !== 0) {
    throw new Error(`docker ${commandArgs.join(' ')}: ${redactor.redact(result.stderr.trim())}`);
  }
  return result.stdout.split('\n').map((line) => line.trim()).filter(Boolean);
}

function imageId(image) {
  const result = sh('docker', ['image', 'inspect', '-f', '{{.Id}}', image]);
  return result.code === 0 ? result.stdout.trim() : null;
}

function probePortAvailable(candidate) {
  const probe = spawnSync(
    process.execPath,
    ['-e', `require('net').createServer().listen(${candidate}, '127.0.0.1').on('listening', function () { this.close(); process.exit(0); }).on('error', () => process.exit(1));`],
    { encoding: 'utf8' },
  );
  return probe.status === 0;
}

function commandRecord(name, exitCode, stdout = '', stderr = '') {
  // A failure path is exactly where output may carry credential material, so
  // the record keeps only redacted text: the digest covers what may be shown.
  const safeStdout = redactor.redact(stdout);
  const safeStderr = redactor.redact(stderr);
  return {
    name,
    exitCode,
    stdoutSha256: safeStdout ? sha256(safeStdout) : null,
    stderrSha256: safeStderr ? sha256(safeStderr) : null,
    stderrTail: safeStderr ? tail(safeStderr, 400) : '',
  };
}

function tail(text, limit = 2_000) {
  const trimmed = String(text ?? '').trim();
  return trimmed.length <= limit ? trimmed : `…${trimmed.slice(-limit)}`;
}

// preview is the only way a body or report reaches a message: credentials the
// harness knows are removed first, then the text is shortened.
function preview(value, limit = 200) {
  const text = redactor.redact(typeof value === 'string' ? value : JSON.stringify(value));
  return text.length <= limit ? text : `…${text.slice(0, limit)}`;
}

function sha256(value) {
  return crypto.createHash('sha256').update(value).digest('hex');
}

function sleep(ms) {
  return new Promise((resolvePromise) => setTimeout(resolvePromise, ms));
}

async function waitFor(predicate, label, timeout, interval = 100) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    // A shutdown must not leave the run waiting out a predicate that only a
    // resource release would satisfy; the release runs in finalize and the
    // barrier there waits for this rejection.
    if (abortedSignal) throw abortError(label);
    if (await predicate()) return;
    await sleep(interval);
  }
  throw new Error(`timed out waiting for ${label}`);
}

function assertValue(condition, message) {
  if (!condition) throw new Error(message);
}

// copyTree copies a harness-owned tree on the host. It walks the tree itself
// because Node's native recursive copy aborts the process — not throws — when a
// subdirectory cannot be read: `std::filesystem::recursive_directory_iterator`
// raises a C++ exception in that path, and the process dies with it before any
// evidence is written. A walk that throws keeps the failure inside the harness,
// where it is reported with the path that produced it.
export function copyDirectory(from, to) {
  fs.mkdirSync(to, { recursive: true });
  for (const entry of fs.readdirSync(from, { withFileTypes: true })) {
    const source = path.join(from, entry.name);
    const destination = path.join(to, entry.name);
    if (entry.isDirectory()) {
      copyDirectory(source, destination);
      continue;
    }
    if (entry.isSymbolicLink()) {
      fs.symlinkSync(fs.readlinkSync(source), destination);
      continue;
    }
    if (!entry.isFile()) {
      // Sockets, FIFOs and devices are not content; a scratch tree the harness
      // owns never holds one, and copying one would hang the run.
      continue;
    }
    fs.copyFileSync(source, destination);
    const stat = fs.lstatSync(source);
    fs.chmodSync(destination, stat.mode & 0o7777);
    fs.utimesSync(destination, stat.atime, stat.mtime);
  }
}

function copyTree(from, to) {
  fs.rmSync(to, { recursive: true, force: true });
  fs.mkdirSync(path.dirname(to), { recursive: true });
  try {
    copyDirectory(from, to);
  } catch (error) {
    const reason = error?.code || error?.message || String(error);
    const where = error?.path ? ` at ${path.relative(ROOT, error.path)}` : '';
    throw new Error(`copy the harness tree ${path.relative(ROOT, from)}: ${reason}${where}`);
  }
}

// ownershipNormalizationArgv is the command that repairs a tree a container
// wrote: it runs the helper image as root with the directory mounted, and gives
// the tree back to the invoking uid:gid with owner access. It is a pure function
// so its exact shape is testable without Docker.
export function ownershipNormalizationArgv({ name, directory, uid, gid, image = OWNERSHIP_HELPER_IMAGE }) {
  for (const [label, value] of [['uid', uid], ['gid', gid]]) {
    if (!Number.isInteger(value) || value < 0) {
      throw new Error(`ownership normalization needs a numeric ${label}: ${JSON.stringify(value)}`);
    }
  }
  if (typeof directory !== 'string' || directory === '') {
    throw new Error('ownership normalization needs a directory');
  }
  return [
    'run', '--rm',
    '--name', name,
    '--user', '0:0',
    '-e', 'HOME=/tmp',
    '-v', `${directory}:/data`,
    image, 'sh', '-c', `chown -R ${uid}:${gid} /data && chmod -R u+rwX /data`,
  ];
}

// normalizeScratchOwnership returns a directory a container wrote to the
// invoking user before the host reads it. A container that ignores --user, or an
// image that keeps its own uid, still leaves host-unreadable files; this makes
// the read succeed instead of turning it into a reported failure.
function normalizeScratchOwnership(directory, label) {
  if (!fs.existsSync(directory)) return false;
  if (typeof process.getuid !== 'function' || typeof process.getgid !== 'function') return false;
  const name = `${PREFIX}-own-${String(label).replace(/[^a-z0-9]/gi, '').slice(0, 20)}`;
  const argv = ownershipNormalizationArgv({ name, directory, uid: process.getuid(), gid: process.getgid() });
  registry.track('container', name, () => releaseDockerResource('container', name));
  const result = sh('docker', argv, { timeout: 180_000 });
  evidence.commands.push(commandRecord(`docker run ${name} (ownership)`, result.code));
  if (result.code !== 0) {
    throw new Error(`normalize the ownership of ${path.relative(ROOT, directory)}: ${redactor.redact(tail(result.stderr))}`);
  }
  registry.untrack('container', name);
  return true;
}

// dirDigest is the content digest of a directory: every entry, its size and its
// file digest. It is what "the target root is unchanged" means for a fault case.
// dirDigest is the content digest the fault cases compare. It covers every
// regular file under the root except SQLite's lock sidecars: `-shm` is SQLite's
// shared-memory index, and an empty `-wal` is what a connection leaves behind
// when it opens a WAL-mode store and closes without writing. Both are the open,
// not a write; a non-empty `-wal` is content and stays in the digest, so a
// step that really wrote through the journal still trips the comparison.
export function dirDigest(root) {
  if (!fs.existsSync(root)) return 'missing';
  const lines = [];
  const walk = (dir) => {
    for (const entry of fs.readdirSync(dir, { withFileTypes: true }).sort((a, b) => a.name.localeCompare(b.name))) {
      const full = path.join(dir, entry.name);
      const relative = path.relative(root, full);
      if (entry.isDirectory()) {
        lines.push(`d ${relative}`);
        walk(full);
        continue;
      }
      if (entry.name.endsWith('-shm')) {
        // SQLite's shared-memory index is a lock table, not content: a
        // read-only open may rewrite it without changing the store.
        continue;
      }
      const stat = fs.statSync(full);
      if (entry.name.endsWith('-wal') && stat.size === 0) {
        // An empty journal is the sidecar of an open, not a write; a step that
        // committed anything leaves it non-empty and is still caught.
        continue;
      }
      lines.push(`f ${relative} ${stat.size} ${sha256(fs.readFileSync(full))}`);
    }
  };
  walk(root);
  return sha256(lines.join('\n'));
}

// sqliteExec applies one fault to a stopped store and checkpoints it, so the
// change is in the database file the next command opens instead of waiting in a
// WAL that a read-only open may or may not join.
function sqliteExec(dbPath, sql) {
  const db = new DatabaseSync(dbPath);
  try {
    db.exec(sql);
    db.exec('pragma wal_checkpoint(truncate)');
  } finally {
    db.close();
  }
}

// injectClearedRollup empties a derived rollup exactly as a rebuild that lost
// its rows would, and proves the injection landed before the command runs.
function injectClearedRollup(targetRoot, table) {
  const database = path.join(targetRoot, 'usage.sqlite');
  const seeded = sqliteCount(database, `select count(*) as count from ${table}`);
  assertValue(seeded > 0, `inject: ${table} was empty before the fault`);
  sqliteExec(database, `delete from ${table}`);
  assertValue(sqliteCount(database, `select count(*) as count from ${table}`) === 0, `inject: ${table} still holds rows`);
}

function sqliteCount(dbPath, sql) {
  const db = new DatabaseSync(dbPath, { readOnly: true });
  try {
    return db.prepare(sql).get().count;
  } finally {
    db.close();
  }
}

// ---------------------------------------------------------------------------
// cleanup and evidence
// ---------------------------------------------------------------------------

// cleanupResources releases everything the registry tracked, then proves the
// host holds nothing else under this run's prefix. A release that failed, a
// listing that could not be read, a surviving object or a scratch tree that
// could not be removed all leave `ok` false, which fails the run.
//
// `continuation` is the acceptance promise. Releases happen first because they
// are often the thing an interrupted step is waiting on; waiting for the step
// afterwards is what makes the registry check describe the final set, not a
// snapshot taken while a step could still resume. The fence was closed before
// this function ran, so the wait cannot grow new work.
async function cleanupResources(continuation = null) {
  const result = newCleanupResult();
  await registry.releaseAll(result);
  if (continuation) await continuation;
  if (evidence.host.docker) {
    const listings = {
      containers: dockerNames(['ps', '-a', '--format', '{{.Names}}']),
      volumes: dockerNames(['volume', 'ls', '--format', '{{.Name}}']),
      networks: dockerNames(['network', 'ls', '--format', '{{.Name}}']),
    };
    const leftovers = {};
    for (const [kind, names] of Object.entries(listings)) {
      if (names === null) {
        result.ok = false;
        result.errors.push(`cannot list the host's ${kind} after cleanup, so the host cannot be shown clean`);
        continue;
      }
      leftovers[kind] = names.filter((name) => name.startsWith(PREFIX));
      for (const name of leftovers[kind]) {
        result.errors.push(`leftover ${kind.replace(/s$/, '')} ${name}`);
      }
    }
    result.leftovers = leftovers;
  }
  // Docker listings only cover containers, volumes and networks. Host processes
  // and worktree checkouts are invisible there, so the registry is checked for
  // every kind, whether or not Docker answered: a successful listing is not
  // evidence that a process was released.
  const remaining = registry.all();
  if (remaining.length > 0) {
    result.ok = false;
    result.unreleased = remaining.map((entry) => `${entry.kind}:${entry.name}`);
    for (const entry of remaining) {
      const prefix = `${entry.kind}:${entry.name}:`;
      if (!result.errors.some((message) => message.startsWith(prefix))) {
        result.errors.push(`${prefix} still owned after the release boundary`);
      }
    }
  }
  if (args.keep) {
    result.scratchKept = SCRATCH;
  } else {
    try {
      fs.rmSync(SCRATCH, { recursive: true, force: true });
      result.scratchRemoved = true;
    } catch (error) {
      result.ok = false;
      result.scratchRemoved = false;
      result.errors.push(`remove the scratch tree: ${redactor.redact(error?.message || error)}`);
    }
  }
  result.ok = result.ok && result.errors.length === 0;
  console.log(`cleanup: ${JSON.stringify({ ok: result.ok, released: result.released.length, errors: result.errors })}`);
  return result;
}

function writeEvidence() {
  evidence.stableDigest = sha256(stableFacts.join('|'));
  // stableDigest covers the hand-written stable facts; resultsDigest covers what
  // the run actually observed (the chain reports, the faults, the rollback and
  // the admission projections), so a reader can detect a substitution of the
  // results even without re-running Docker.
  evidence.resultsDigest = sha256(
    JSON.stringify({
      chains: evidence.chains,
      faults: evidence.faults,
      rollback: evidence.rollback,
      wizard: evidence.wizard,
      admission: evidence.admission,
    }),
  );
  evidence.hashRule = UPGRADE_DOCKER_CONTRACT.evidenceHashRule;
  const document = withEvidenceHash(evidence);
  // The document is checked against its own canonical hash before it is
  // written, and the check is repeated on the bytes on disk: an upload of this
  // file is then at least the file this run computed.
  const verified = verifyEvidenceDocument(document, evidence.implementation?.headSha ? { expectHead: evidence.implementation.headSha } : {});
  if (!verified.ok) {
    throw new Error(`the evidence document is not bound to this run: ${verified.problems.join('; ')}`);
  }
  const file = path.join(EVIDENCE_DIR, `${runId}.json`);
  fs.mkdirSync(EVIDENCE_DIR, { recursive: true });
  fs.writeFileSync(file, `${JSON.stringify(document, null, 2)}\n`);
  const onDisk = verifyEvidenceDocument(JSON.parse(fs.readFileSync(file, 'utf8')));
  if (!onDisk.ok) {
    throw new Error(`the evidence file on disk does not verify: ${onDisk.problems.join('; ')}`);
  }
  evidenceFile = file;
  publishEvidencePath(file);
  console.log(`evidence: ${path.relative(ROOT, file)} (sha256 ${document.sha256})`);
  console.log(`stable digest: ${evidence.stableDigest}`);
}

// publishEvidencePath hands the one file this run wrote to the workflow, so the
// artifact upload can name it instead of a directory glob that also matches the
// committed historical records.
function publishEvidencePath(file) {
  const outputFile = process.env.GITHUB_OUTPUT;
  if (!outputFile) return;
  try {
    fs.appendFileSync(outputFile, `evidence_path=${file}\nevidence_run_id=${runId}\n`);
  } catch (error) {
    console.error(`upgrade-docker: could not publish the evidence path: ${redactor.redact(error?.message || error)}`);
  }
}

export const evidencePath = () => evidenceFile;
