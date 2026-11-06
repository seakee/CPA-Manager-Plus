#!/usr/bin/env node
// Readiness Gate B/C evidence on the final candidate images (Scope 6).
//
// What this proves, on the images the release candidate actually shipped:
//
//   * a Manager whose store cannot be opened answers the safe not-ready
//     diagnostics within the ten-second budget, discloses no secret, and leaves
//     the store's offline maintenance lock free, so an operator can run
//     `cleanup-derived` without stopping a process that never became ready;
//   * a store whose SQLite bytes are unreadable is refused into read-only
//     discovery inside the same budget instead of serving a partial graph;
//   * with the plan's fixture (100,000 usage events over 120 days, 12 models,
//     3 providers, 100 accounts, 10 API keys) the Manager reaches /health 200,
//     finishes its derived rollups, and answers Dashboard, Monitoring and Usage
//     Analytics reads under `--cpus=2 --memory=4g --memory-swap=4g`, with the
//     observed P50/P95 and the container's cgroup peak memory recorded;
//   * the same reads are recorded with the hourly rollup enabled and disabled.
//
// The harness never builds an image. It runs the already-loaded candidate
// images and records their IDs, so the evidence cannot be read against a
// different build. Everything it creates lives under one `cpampd<8 hex>` prefix
// in a host scratch directory, is registered before it is created, is released
// on the normal path, every error path and SIGINT/SIGTERM, and a cleanup that
// could not remove what this run created fails the run instead of reporting a
// pass.
//
// The benchmark store is a fixture, not an install: the candidate's
// `initialize` creates the real schema and data key, then this harness completes
// the store the way a finished bootstrap persists it (the Fresh checkpoint rows
// are removed and a bootstrap-state row is written) so the benchmark exercises
// the post-install Manager graph instead of the Fresh installer. That
// construction is recorded in the evidence and the report; it is not a claim
// about install.
//
// Usage:
//   node bin/ci/readiness-gate-bc.mjs [--manager-image REF] [--runtime-image REF]
//     [--ingress-image REF] [--output DIR] [--work-dir DIR]
//
// Defaults are the candidate images loaded at `8935bce5`. The evidence document
// lands in `--output` (default `bin/tmp/readiness-gate-bc/evidence`).

import { spawnSync } from 'node:child_process';
import { createCipheriv, createHash, hkdfSync, randomBytes } from 'node:crypto';
import fs from 'node:fs';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { DatabaseSync } from 'node:sqlite';
import { fileURLToPath } from 'node:url';

import {
  createRedactor,
  createResourceRegistry,
  finalExitCode,
  installSignalGuard,
} from './upgrade-docker.mjs';

export {
  createRedactor,
  createResourceRegistry,
  finalExitCode,
  installSignalGuard,
} from './upgrade-docker.mjs';

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');
const DAY_MS = 86_400_000;
const MIB = 1024 ** 2;
const GIB = 1024 ** 3;
const PLATFORM = 'linux/amd64';
const CANDIDATE_TAG = '8935bce5';
const CANDIDATE_COMMIT = '8935bce5ea7ce994185ee97acb8a4fdbd9cdd6cc';

// The candidate images carry the licence inventory at the observed release path.
// `release-provenance.json` and a `thirdPartyInventoryStatus` field do not exist
// at this base; the inventory's `status` is the hashed product of
// `bin/release/image-evidence.py` and is bound to the provenance file below.
const PROVENANCE_PATHS = Object.freeze({
  inventory: '/usr/local/share/cpamp/release/third-party-inventory.txt',
  provenance: '/usr/local/share/cpamp/provenance.json',
  releaseProvenance: '/usr/local/share/cpamp/release-provenance.json',
});

// The candidate asset set. The Runtime and Ingress images are inspected (their
// IDs are part of the evidence) but never started: the benchmark and the failure
// probes are Manager-store questions, and the plan's Docker items that need the
// whole stack are separate harnesses.
const DEFAULT_IMAGES = Object.freeze({
  manager: `cpampd-beta-manager:${CANDIDATE_TAG}`,
  runtime: `cpampd-beta-runtime:${CANDIDATE_TAG}`,
  ingress: `cpampd-beta-ingress:${CANDIDATE_TAG}`,
});

export const READINESS_GATE_BC_CONTRACT = Object.freeze({
  contract: 'cpamp.readiness-gate-bc.v1',
  candidateTag: CANDIDATE_TAG,
  platform: PLATFORM,
  limits: Object.freeze({ cpus: 2, memory: '4g', memorySwap: '4g' }),
  // The Gate B1 budget from the plan, and the cold-start / memory targets.
  diagnosticBudgetMs: 10_000,
  coldStartBudgetMs: 5_000,
  peakMemoryBudgetBytes: Math.floor(1.5 * GIB),
  derivedMaintenanceTimeoutMs: 600_000,
  fixtureEvents: 100_000,
  fixtureDays: 120,
  fixtureModels: Object.freeze([
    'gpt-5.1',
    'gpt-5.1-mini',
    'claude-sonnet-4.5',
    'claude-opus-4.1',
    'gemini-3-pro',
    'gemini-3-flash',
    'gpt-4.1',
    'o4-mini',
    'grok-4',
    'deepseek-v3.2',
    'qwen3-max',
    'kimi-k2',
  ]),
  fixtureProviders: Object.freeze(['openai', 'anthropic', 'gemini']),
  fixtureAccounts: 100,
  fixtureAPIKeys: 10,
  fixtureRawBytes: 850,
  sampleCount: 5,
  // `bootstrapdiscovery.NotReadyHandler` is the one not-ready body; the
  // discovery projection must say it has no observation to publish.
  safeHealthBody: Object.freeze({ ok: false }),
  storeOpenLogMarker: 'Manager store could not be opened; keeping safe not-ready diagnostics available',
  readOnlyDiscoveryLogMarker: 'keeping read-only discovery available',
  // The offline maintenance command's own words. The lock refusal is what an
  // unreleased admission looks like; the SQLite validation failure is the
  // specific defect a corrupted file produces once the command has the lock.
  offlineLockRefusal: 'acquire offline cleanup lock',
  sqliteValidationFailure: 'file is not a database',
  diskFloorBytes: 3 * GIB,
});

// ---------------------------------------------------------------------------
// fixture: the plan's synthetic distribution, in one place
// ---------------------------------------------------------------------------

// fixtureRow is the deterministic row for one synthetic event. It mirrors the
// distribution `usageevent.TestLatencyCoveringIndexPerformanceExperiment`
// documents: a coprime stride over the event count spreads timestamps across
// the whole window, and model/provider/account/key are modulo-decorrelated.
export function fixtureRow(index, { nowMs, count = READINESS_GATE_BC_CONTRACT.fixtureEvents } = {}) {
  if (!Number.isInteger(index) || index < 0) {
    throw new Error(`fixture index must be a non-negative integer, got ${index}`);
  }
  if (!Number.isInteger(count) || count <= 0) {
    throw new Error(`fixture count must be a positive integer, got ${count}`);
  }
  const windowMs = READINESS_GATE_BC_CONTRACT.fixtureDays * DAY_MS;
  const base = nowMs - windowMs;
  const timestampMs = base + Math.floor((((index * 7919) % count) * windowMs) / count);
  const models = READINESS_GATE_BC_CONTRACT.fixtureModels;
  const providers = READINESS_GATE_BC_CONTRACT.fixtureProviders;
  const model = models[index % models.length];
  const provider = providers[index % providers.length];
  const account = `account-${String(index % READINESS_GATE_BC_CONTRACT.fixtureAccounts).padStart(3, '0')}`;
  return {
    eventHash: (index + 1).toString(16).padStart(64, '0'),
    timestampMs,
    timestamp: '2026-01-01T00:00:00Z',
    model,
    requestedModel: '',
    provider,
    executorType: provider,
    authProviderSnapshot: provider,
    authFileSnapshot: `${account}.json`,
    authIndex: `auth-${String(index % READINESS_GATE_BC_CONTRACT.fixtureAccounts).padStart(3, '0')}`,
    accountSnapshot: `${account}@example.test`,
    source: `${account}.json`,
    apiKeyHash: `key-${String(index % READINESS_GATE_BC_CONTRACT.fixtureAPIKeys).padStart(2, '0')}`,
    inputTokens: 1000,
    outputTokens: 200,
    totalTokens: 1200,
    latencyMs: 50 + ((index * 37) % 20_000),
    ttftMs: 10 + ((index * 13) % 5000),
    failed: index % 20 === 0 ? 1 : 0,
    rawJson: JSON.stringify({ synthetic: 'x'.repeat(READINESS_GATE_BC_CONTRACT.fixtureRawBytes) }),
    // The rollups bucket by created_at_ms, so it must carry the same spread as
    // timestamp_ms or every window query would answer zero.
    createdAtMs: timestampMs,
  };
}

export function fixtureSummary({ nowMs, count = READINESS_GATE_BC_CONTRACT.fixtureEvents } = {}) {
  const models = new Set();
  const providers = new Set();
  const accounts = new Set();
  const apiKeys = new Set();
  const hashes = new Set();
  let minTimestampMs = Number.POSITIVE_INFINITY;
  let maxTimestampMs = Number.NEGATIVE_INFINITY;
  for (let index = 0; index < count; index += 1) {
    const row = fixtureRow(index, { nowMs, count });
    models.add(row.model);
    providers.add(row.provider);
    accounts.add(row.accountSnapshot);
    apiKeys.add(row.apiKeyHash);
    hashes.add(row.eventHash);
    minTimestampMs = Math.min(minTimestampMs, row.timestampMs);
    maxTimestampMs = Math.max(maxTimestampMs, row.timestampMs);
  }
  return {
    count,
    uniqueHashes: hashes.size,
    models: models.size,
    providers: providers.size,
    accounts: accounts.size,
    apiKeys: apiKeys.size,
    minTimestampMs,
    maxTimestampMs,
    windowDays: READINESS_GATE_BC_CONTRACT.fixtureDays,
    rawBytes: READINESS_GATE_BC_CONTRACT.fixtureRawBytes,
  };
}

const USAGE_EVENT_COLUMNS = [
  'event_hash',
  'timestamp_ms',
  'timestamp',
  'model',
  'requested_model',
  'provider',
  'executor_type',
  'auth_provider_snapshot',
  'auth_file_snapshot',
  'auth_index',
  'account_snapshot',
  'source',
  'api_key_hash',
  'input_tokens',
  'output_tokens',
  'total_tokens',
  'latency_ms',
  'ttft_ms',
  'failed',
  'raw_json',
  'created_at_ms',
];

function rowValues(row) {
  return [
    row.eventHash,
    row.timestampMs,
    row.timestamp,
    row.model,
    row.requestedModel,
    row.provider,
    row.executorType,
    row.authProviderSnapshot,
    row.authFileSnapshot,
    row.authIndex,
    row.accountSnapshot,
    row.source,
    row.apiKeyHash,
    row.inputTokens,
    row.outputTokens,
    row.totalTokens,
    row.latencyMs,
    row.ttftMs,
    row.failed,
    row.rawJson,
    row.createdAtMs,
  ];
}

// seedFixture writes the synthetic events into the store the candidate created.
// One transaction, then a WAL checkpoint, so the container mounts a settled file
// instead of inheriting a seed that is still in the log.
export function seedFixture(dbPath, { nowMs, count = READINESS_GATE_BC_CONTRACT.fixtureEvents } = {}) {
  const db = new DatabaseSync(dbPath);
  try {
    db.exec('pragma journal_mode = wal');
    db.exec('begin');
    const statement = db.prepare(
      `insert into usage_events(${USAGE_EVENT_COLUMNS.join(',')}) values (${USAGE_EVENT_COLUMNS.map(() => '?').join(',')})`
    );
    for (let index = 0; index < count; index += 1) {
      statement.run(...rowValues(fixtureRow(index, { nowMs, count })));
    }
    db.exec('commit');
    db.exec('pragma wal_checkpoint(truncate)');
  } finally {
    db.close();
  }
  return fixtureSummary({ nowMs, count });
}

// completeFixtureStore turns the store the candidate's `initialize` admitted
// into the store a finished bootstrap persists: the Fresh checkpoint rows are
// removed, the bootstrap-state row is written, and the Fresh ownership marker is
// retired. This is fixture construction for the benchmark, not an install path.
export function completeFixtureStore(dbPath, { hasHistoricalData = true } = {}) {
  const db = new DatabaseSync(dbPath);
  try {
    db.exec(`delete from settings where key in ('fresh_operation_v1', 'fresh_authority_v1')`);
    const insert = db.prepare(
      `insert into settings(key, value, updated_at_ms) values (?, ?, 0)
       on conflict(key) do update set value = excluded.value, updated_at_ms = excluded.updated_at_ms`
    );
    insert.run(
      'bootstrap_state_v1',
      JSON.stringify({
        version: 1,
        status: 'ready',
        adminReady: true,
        projectInitialized: true,
        dataKeyReady: true,
        migratedLegacy: false,
        hasHistoricalData,
        updatedAtMs: 0,
      })
    );
  } finally {
    db.close();
  }
  fs.rmSync(`${dbPath}.manager.lock`, { force: true });
}

// injectUnsupportedDerivedFormat makes the source structurally admissible (a
// native-v2 schema) but unacceptable to the real `store.Open`, which is the one
// path that logs the safe not-ready marker. The value is untrusted text and must
// never reach a log, a body or the evidence.
export function injectUnsupportedDerivedFormat(dbPath, sentinel) {
  const db = new DatabaseSync(dbPath);
  try {
    db.prepare(`update settings set value = ? where key = 'usage_monitoring_model_format_version'`).run(sentinel);
  } finally {
    db.close();
  }
}

// protectSettingValue reproduces the product's settings envelope
// (`internal/security`: HKDF-SHA256 with salt `cpa-manager-plus`, info
// `settings-secrets-v1`, AES-256-GCM with AAD `settings-secret`) so a fixture can
// persist an encrypted connection secret that only the original data key can
// decrypt. The container scenario is what proves the envelope is accepted.
export function protectSettingValue(dataKeyBase64, plaintext) {
  const rawKey = Buffer.from(String(dataKeyBase64).trim(), 'base64');
  if (rawKey.length === 0) throw new Error('data key is empty');
  const derived = Buffer.from(
    hkdfSync('sha256', rawKey, Buffer.from('cpa-manager-plus'), Buffer.from('settings-secrets-v1'), 32)
  );
  const nonce = randomBytes(12);
  const cipher = createCipheriv('aes-256-gcm', derived, nonce);
  cipher.setAAD(Buffer.from('settings-secret'));
  const ciphertext = Buffer.concat([cipher.update(String(plaintext), 'utf8'), cipher.final(), cipher.getAuthTag()]);
  const encode = (buffer) => buffer.toString('base64').replace(/=+$/, '');
  return `enc:v1:${encode(nonce)}:${encode(ciphertext)}`;
}

// classifyDockerInspect separates the one outcome that means "this object is
// gone" from every other inspect failure. A daemon that is unreachable, a
// permission error or a renamed runtime all produce a non-zero exit too, and
// treating those as "already removed" would let cleanup report a pass over an
// object that is still on the host.
export function classifyDockerInspect({ status, stdout = '', stderr = '' } = {}) {
  if (status === 0) return { state: 'present', error: null };
  const text = `${stderr || ''}${stdout || ''}`.trim();
  if (/\bNo such (?:container|volume|object|image)\b/i.test(text)) {
    return { state: 'absent', error: null };
  }
  const detail =
    text.slice(0, 300) ||
    `inspect exited ${status === null || status === undefined ? 'null' : status} with no output`;
  return { state: 'unknown', error: detail };
}

// createDockerReleaser is the registry release callback used for containers and
// volumes. Only an inspect that positively says the object is gone counts as
// released; an inspect we cannot interpret and a failed `docker rm` both throw,
// so `cleanup.ok` becomes false instead of a failed removal being dropped.
export function createDockerReleaser({ inspect, removeObject }) {
  if (typeof inspect !== 'function' || typeof removeObject !== 'function') {
    throw new TypeError('createDockerReleaser needs inspect and removeObject');
  }
  return function release(kind, name) {
    const found = inspect(kind, name);
    const state = typeof found === 'string' ? found : found?.state;
    if (state === 'absent') return { kind, name, removed: false, present: false };
    if (state !== 'present') {
      const detail = typeof found === 'object' && found ? found.error : '';
      throw new Error(`docker inspect ${kind} ${name} failed: ${detail || 'no classification'}`);
    }
    const result = removeObject(kind, name);
    const status = typeof result === 'number' ? result : result?.status;
    if (status !== 0) {
      const stderr = typeof result === 'object' && result ? result.stderr || result.message || '' : '';
      throw new Error(`docker remove ${kind} ${name} exited ${status}: ${String(stderr).slice(0, 300)}`);
    }
    return { kind, name, removed: true, present: true };
  };
}

// settleEphemeralTracking decides whether a finished `docker run --rm` may leave
// the registry. Only a positive "no such container" untracks it: when the daemon
// cannot answer, the entry stays, so the final cleanup tries again and fails
// loudly instead of assuming the container is gone.
export function settleEphemeralTracking({ registry, kind = 'container', name, inspected } = {}) {
  const state = typeof inspected === 'string' ? inspected : inspected?.state;
  if (state === 'absent') return { tracked: false, untracked: registry.untrack(kind, name) };
  if (state === 'present') return { tracked: true, untracked: false };
  return { tracked: true, untracked: false, unverified: inspected?.error ?? 'inspect did not answer' };
}

// classifyRemoveResult classifies a `docker rm -f` result the same way
// classifyDockerInspect classifies an inspect: exit 0 removed the object, an
// explicit "No such container/volume" means someone else already did, and every
// other failure — permission denied, an unreachable daemon, an unparseable exit —
// is a failed removal. The helper's own `docker rm` goes through this, so a
// removal error cannot hide behind a later "the container is gone anyway".
const DENIED_REMOVAL =
  /permission denied|cannot connect to the docker daemon|is the docker daemon running|access denied|operation not permitted/i;

export function classifyRemoveResult({ status, stdout = '', stderr = '' } = {}) {
  if (status === 0) return { ok: true, absent: false, error: null };
  const text = `${stderr || ''}${stdout || ''}`.trim();
  // A denial wins over a not-found phrase in the same message: "permission
  // denied" means the removal was not attempted, whatever else the daemon said.
  if (DENIED_REMOVAL.test(text)) {
    return { ok: false, absent: false, error: text.slice(0, 300) || `remove exited ${status} with no output` };
  }
  if (/\bNo such (?:container|volume|object)\b/i.test(text)) return { ok: true, absent: true, error: null };
  return {
    ok: false,
    absent: false,
    error: text.slice(0, 300) || `remove exited ${status === null || status === undefined ? 'null' : status} with no output`,
  };
}

// checkCleanupHelper is the one fact the fallback helper must satisfy: it is
// gone. The helper starts after the registry is fenced, so nothing else will
// ever remove it, and a helper that outlives the run is a leak like any other.
export function checkCleanupHelper({ name, inspected } = {}) {
  const state = typeof inspected === 'string' ? inspected : inspected?.state;
  if (state === 'absent') return { ok: true, name };
  return {
    ok: false,
    name,
    error: `cleanup helper ${name} is still present: ${inspected?.error ?? state ?? 'unknown'}`,
  };
}

// removeRunWorkspace removes one run's host scratch directory and reports
// whether it is really gone. The fallback exists because a container can create
// files the host user cannot unlink; the caller supplies a container-based
// remover for that case.
export function removeRunWorkspace(
  directory,
  {
    fsRemove = (target) => fs.rmSync(target, { recursive: true, force: true }),
    fallbackRemove = null,
    exists = (target) => fs.existsSync(target),
  } = {}
) {
  if (!directory) return { ok: true, method: 'none' };
  try {
    fsRemove(directory);
  } catch {}
  if (!exists(directory)) return { ok: true, method: 'fs' };
  if (typeof fallbackRemove === 'function') {
    try {
      fallbackRemove(directory);
    } catch {}
  }
  if (!exists(directory)) return { ok: true, method: 'fallback' };
  // The directory's own path is deliberately absent: this message is archived in
  // evidence on a failure path, and an operator's scratch layout is not
  // evidence. The caller knows which run this was from `runId`.
  return {
    ok: false,
    method: 'unremoved',
    error: 'run workspace remained after filesystem and fallback removal',
  };
}

// dockerRunArgs is the one place a container is described. Every container this
// harness starts — the Manager under test, the offline helpers, the cleanup
// helper — carries the same resource budget, so the run's resource claim covers
// what the run actually started.
export function dockerRunArgs({
  name,
  image,
  limits = READINESS_GATE_BC_CONTRACT.limits,
  detach = false,
  rm = false,
  mounts = [],
  ports = [],
  network = null,
  entrypoint = null,
  env = {},
} = {}) {
  if (!image) throw new Error('a run needs an image reference');
  const args = ['run'];
  if (rm) args.push('--rm');
  if (detach) args.push('-d');
  if (name) args.push('--name', name);
  args.push(
    '--pull=never',
    '--platform',
    PLATFORM,
    `--cpus=${limits.cpus}`,
    `--memory=${limits.memory}`,
    `--memory-swap=${limits.memorySwap}`
  );
  if (network) args.push('--network', network);
  for (const mount of mounts) args.push('-v', mount);
  for (const port of ports) args.push('-p', port);
  if (entrypoint) args.push('--entrypoint', entrypoint);
  for (const [key, value] of Object.entries(env)) args.push('-e', `${key}=${value}`);
  args.push(image);
  return args;
}

// verifyStartedImage is the post-start check for a long-lived container: the
// image it is actually running must be the one the evidence recorded, not a tag
// that may have moved between the pre-check and the start.
export function verifyStartedImage({ name, expected, actual }) {
  if (!expected) throw new Error(`no pre-checked image id to verify ${name} against`);
  if (actual !== expected) {
    throw new Error(`${name} runs image ${actual}, not the pre-checked ${expected}`);
  }
  return { name, image: actual };
}

// judgeCleanupHelper is the fallback helper's whole contract in one decision:
// the helper's `docker run` must not have failed, its own `docker rm` must have
// succeeded or found nothing to remove, and the container must be gone. A later
// "absent" inspect cannot excuse a permission error from the removal — the run
// has to say it could not verify its own cleanup.
export function judgeCleanupHelper({ name, runResult = {}, removeResult = {}, inspected } = {}) {
  const failures = [];
  const runStatus = runResult?.status;
  if (runStatus !== 0) {
    const detail = String(`${runResult?.stderr ?? ''}${runResult?.stdout ?? ''}`).trim().slice(0, 200);
    failures.push(`cleanup helper run exited ${runStatus === undefined ? 'null' : runStatus}${detail ? `: ${detail}` : ''}`);
  }
  const removal = classifyRemoveResult(removeResult ?? {});
  if (!removal.ok) failures.push(`cleanup helper remove failed: ${removal.error}`);
  const helper = checkCleanupHelper({ name, inspected });
  if (!helper.ok) failures.push(helper.error);
  return { ok: failures.length === 0, failures, error: failures.join('; ') };
}

// createCleanupCoordinator makes the cleanup exactly-once and awaitable by both
// exits: the normal one and a handled signal. The signal guard calls `abort`
// while the normal path calls `run`; both get the same promise, so a SIGTERM that
// lands in the middle of a release waits for the releases instead of terminating
// the process with containers still on the host. A cleanup that throws is a
// failed cleanup, never a clean exit.
export function createCleanupCoordinator({
  cleanupResources,
  exitCode = finalExitCode,
  redact = (value) => String(value),
} = {}) {
  if (typeof cleanupResources !== 'function') throw new TypeError('a cleanup coordinator needs cleanupResources');
  let promise = null;
  let archive = null;
  const run = () => {
    if (!promise) {
      promise = Promise.resolve()
        .then(() => cleanupResources())
        .catch((error) => ({
          ok: false,
          released: [],
          errors: [`cleanup threw: ${redact(error?.message || String(error))}`],
        }));
    }
    return promise;
  };
  return {
    run,
    get pending() {
      return promise !== null;
    },
    // The archive is the last thing a run publishes. While it is in flight, a
    // signal must wait for it: the archive is what rolls its own files back when
    // it sees the signal, so exiting first would leave a PASS document behind.
    trackArchive(pending) {
      archive = Promise.resolve(pending);
      return archive;
    },
    get archivePending() {
      return archive !== null;
    },
    // The signal path: await the in-flight (or not yet started) cleanup and any
    // in-flight archive, then say which code the process must exit with. The
    // signal still decides the code, and it is never 0.
    async abort({ signal, runOk = false } = {}) {
      const outcome = await run();
      if (archive) {
        try {
          await archive;
        } catch {
          // The archive reports its own failures; an aborted or failed archive
          // still must not keep the signal from exiting.
        }
      }
      return { outcome, exitCode: exitCode({ runOk, cleanupOk: outcome.ok, signal }) };
    },
  };
}

// The redactor this run built, published for the top-level catch. The catch is
// outside `runMain`, and a failed run has to print the same redacted message the
// run itself would; without this it would have to fall back to pattern matching
// alone.
let fatalRedactor = null;

// Paths in a stray error message. The redactor knows the paths this run was
// given, but a message can also carry one the harness never saw: a Docker
// socket, a temp directory, an operator's `--output /restricted`. Two shapes are
// scrubbed — a quoted span containing a slash (so a path with spaces goes as one
// unit) and any bare slash-led path, single-segment included. The error code in
// front of the message is untouched. Over-redacting a snippet is the safe
// direction for a fatal line.
const QUOTED_SLASHY = /(['"`])[^'"`\n]*\/[^'"`\n]*\1/g;
const ABSOLUTE_PATH = /(?:\/[\w.@+-]+)+/g;

export function describeFatal(error, redactor = null) {
  const name = error?.name && error.name !== 'Error' ? `${error.name}: ` : '';
  const raw = error?.message || String(error ?? 'unknown failure');
  const message = redactor ? redactor.redact(raw) : raw;
  const scrubbed = message.replace(QUOTED_SLASHY, '[redacted-path]').replace(ABSOLUTE_PATH, '[redacted-path]');
  return `readiness-gate-bc: ${name}${scrubbed}`;
}

// probeWritableDirectory proves a directory can hold an evidence file before the
// run spends two minutes producing one. `mkdirSync` succeeds on an existing
// read-only directory, so the only honest test is to create, write and remove a
// private file; the probe removes itself even when the open fails.
export function probeWritableDirectory(
  directory,
  {
    mkdirSync = fs.mkdirSync,
    openSync = fs.openSync,
    writeSync = fs.writeSync,
    closeSync = fs.closeSync,
    unlinkSync = fs.unlinkSync,
    token = randomBytes(4).toString('hex'),
    pid = process.pid,
  } = {}
) {
  mkdirSync(directory, { recursive: true });
  const probe = path.join(directory, `.readiness-gate-bc-${pid}-${token}.probe`);
  // `wx` refuses to reuse a file, so a path that is already occupied is somebody
  // else's: only a probe this call actually created may be removed, and a probe
  // that cannot be removed is a failure, not a pass.
  let created = false;
  try {
    const fd = openSync(probe, 'wx');
    created = true;
    try {
      writeSync(fd, 'readiness-gate-bc write probe\n');
    } finally {
      closeSync(fd);
    }
  } finally {
    if (created) unlinkSync(probe);
  }
  return { ok: true, directory };
}

// prepareRunDirectories is the run's first side effect: both archive directories
// are proven writable before the run workspace — and therefore any Docker work —
// exists. A directory that exists but refuses a file is the case a bare
// `mkdirSync` check misses.
export function prepareRunDirectories({
  output,
  repoEvidenceDir,
  workDir,
  prefix,
  probe = probeWritableDirectory,
  mkdirSync = fs.mkdirSync,
} = {}) {
  probe(output);
  probe(repoEvidenceDir);
  // One run owns one host directory under its own prefix, so a second run can
  // never inherit a half-initialised or corrupt store from the first.
  const runWorkDir = path.join(workDir, prefix);
  mkdirSync(runWorkDir, { recursive: true });
  return { runWorkDir };
}

// writeEvidence publishes the run copy and the versioned repo copy as one unit:
// each file is written beside its target and renamed into place, and every path
// this function may create is registered for rollback before the first byte, so
// any failure — a short write, a failed rename, a signal mid-archive — leaves
// neither a final file nor a temp file behind. A failed run therefore cannot
// leave a document that reads as a PASS.
//
// It is asynchronous on purpose. A synchronous write would block the event loop
// for its whole duration, so a real SIGTERM/SIGINT delivered during it could not
// reach its listener before the caller detached the guard: the process would exit
// 0 with both PASS files on disk. Every step yields to the loop and re-checks the
// signal, and a signal seen at any point rolls back and reports `aborted`.
export async function writeEvidence({
  document,
  redactor,
  output,
  repoEvidenceDir,
  runId,
  root = ROOT,
  interrupted = () => null,
  mkdirSync = fs.mkdirSync,
  writeFile = fs.promises.writeFile,
  rename = fs.promises.rename,
  unlinkSync = fs.unlinkSync,
  yieldToLoop = () => new Promise((resolve) => setImmediate(resolve)),
  pid = process.pid,
} = {}) {
  const runFile = path.join(output, `readiness-gate-bc-${runId}.json`);
  const repoFile = path.join(repoEvidenceDir, `readiness-gate-bc-${runId}.json`);
  // The temp names live in the target directory so the rename stays on one
  // filesystem and is atomic.
  const runTemp = `${runFile}.tmp-${pid}`;
  const repoTemp = `${repoFile}.tmp-${pid}`;
  // Registered before the first write: rollback has to attempt every path this
  // call may have created, including the file a failing write left half-written.
  const paths = [runFile, repoFile, runTemp, repoTemp];
  const removeAll = () => {
    const stuck = [];
    for (const file of [...paths].reverse()) {
      try {
        unlinkSync(file);
      } catch (error) {
        if (error?.code !== 'ENOENT') stuck.push(file);
      }
    }
    return stuck;
  };
  const abortError = (signal) => {
    const error = new Error(`interrupted by ${signal} while archiving the evidence`);
    error.interrupted = true;
    return error;
  };
  const checkInterrupt = () => {
    const signal = interrupted();
    if (signal) throw abortError(signal);
  };

  try {
    mkdirSync(output, { recursive: true });
    mkdirSync(repoEvidenceDir, { recursive: true });
    // The first turn belongs to the loop: a signal that arrived before the
    // archive started must be seen before the first byte is written.
    await yieldToLoop();
    checkInterrupt();
    // Both target paths are known up front, so the document is serialised once
    // and both copies carry the same bytes — there is no second rewrite window.
    document.evidenceFiles = {
      // The archived copy stays self-describing without carrying the operator's
      // absolute scratch path into the repository.
      runCopy: path.basename(runFile),
      repoCopy: path.relative(root, repoFile),
    };
    const serialized = `${redactor.redact(JSON.stringify(document, null, 2))}\n`;
    await writeFile(runTemp, serialized);
    await yieldToLoop();
    checkInterrupt();
    await writeFile(repoTemp, serialized);
    await yieldToLoop();
    checkInterrupt();
    await rename(runTemp, runFile);
    await yieldToLoop();
    checkInterrupt();
    await rename(repoTemp, repoFile);
    // One more turn after the last rename: a signal delivered while it ran must
    // be seen before this call reports the pair as published.
    await yieldToLoop();
    checkInterrupt();
    return { ok: true, aborted: false, runFile, repoFile };
  } catch (error) {
    const stuck = removeAll();
    if (error?.interrupted) return { ok: false, aborted: true, error: error.message };
    const unremovable = stuck.length > 0 ? `; ${stuck.length} file(s) could not be removed` : '';
    return { ok: false, aborted: false, error: `${redactor.redact(error?.message || String(error))}${unremovable}` };
  }
}

// verifyReleaseProvenance asserts the licence collection status the plan
// requires, and that the status is bound to the provenance document by the
// inventory digest. It reads fields that exist in the candidate images instead
// of assuming a filename or field name they do not carry.
export function verifyReleaseProvenance({ role, sourceCommit, inventory, provenance, inventoryBytes }) {
  const failures = [];
  const inv = parsedBody(inventory);
  const prov = parsedBody(provenance);
  const status = inv?.status ?? null;
  if (status !== 'VERIFIED_LICENSE_COLLECTION') failures.push(`inventory_status:${status}`);
  if (inv?.role !== role) failures.push(`inventory_role:${inv?.role}`);
  if (prov?.schemaVersion !== 1) failures.push(`provenance_schema:${prov?.schemaVersion}`);
  if (prov?.role !== role) failures.push(`provenance_role:${prov?.role}`);
  if (prov?.sourceCommit !== sourceCommit) failures.push(`provenance_source_commit:${prov?.sourceCommit}`);
  if (inventoryBytes !== undefined) {
    const digest = `sha256:${createHash('sha256').update(inventoryBytes).digest('hex')}`;
    if (prov?.licenseInventorySHA256 !== digest) failures.push('inventory_hash_mismatch');
  }
  const coverage = inv?.licenseCoverage;
  if (!coverage || Object.keys(coverage).length === 0) {
    failures.push('license_coverage_missing');
  } else {
    for (const [component, entry] of Object.entries(coverage)) {
      const texts = entry?.originalTexts;
      if (typeof texts !== 'string' || !(texts.startsWith('complete') || texts.startsWith('available'))) {
        failures.push(`license_coverage_${component}:${texts}`);
      }
    }
  }
  return {
    ok: failures.length === 0,
    failures,
    thirdPartyInventoryStatus: status,
    role,
    licenseInventorySHA256: prov?.licenseInventorySHA256 ?? null,
    licenseCoverage: coverage ?? null,
  };
}

// ---------------------------------------------------------------------------
// measurements and judgements
// ---------------------------------------------------------------------------

// percentile is the nearest-rank percentile, so five samples give the third
// sample as P50 and the fifth as P95 instead of an interpolated value the run
// never observed.
export function percentile(samples, fraction) {
  const values = samples.filter((value) => Number.isFinite(value)).sort((left, right) => left - right);
  if (values.length === 0) return null;
  const rank = Math.ceil(fraction * values.length) - 1;
  return values[Math.min(values.length - 1, Math.max(0, rank))];
}

export function summarizeLatencies(samples) {
  const values = samples.filter((value) => Number.isFinite(value));
  if (values.length === 0) return { count: 0, p50: null, p95: null, min: null, max: null };
  return {
    count: values.length,
    p50: percentile(values, 0.5),
    p95: percentile(values, 0.95),
    min: Math.min(...values),
    max: Math.max(...values),
  };
}

export function leaksIn(text, secrets = []) {
  const haystack = String(text ?? '');
  return secrets.filter((secret) => secret && haystack.includes(secret));
}

function parsedBody(body) {
  try {
    return JSON.parse(body);
  } catch {
    return null;
  }
}

// summarizeQuerySamples is the per-query measurement step as one pure decision:
// it takes the query definition and the responses it was answered with and
// validates EVERY sample, not just the last one. A run whose first four answers
// are empty and whose fifth is good has a broken fixture, and only a per-sample
// validator can see that.
export function summarizeQuerySamples({ query, responses = [] } = {}) {
  if (!query || !Array.isArray(responses)) {
    throw new TypeError('summarizeQuerySamples needs a query definition and a response list');
  }
  const latencies = [];
  const failures = [];
  for (let index = 0; index < responses.length; index += 1) {
    const response = responses[index] ?? {};
    latencies.push(response.ms);
    if (typeof query.validate !== 'function') continue;
    const body = typeof response.body === 'string' ? parsedBody(response.body) : response.body;
    if (query.validate(body) !== true) {
      failures.push({ sample: index + 1, body: String(response.body ?? '').slice(0, 240) });
    }
  }
  return {
    sampleCount: responses.length,
    summary: summarizeLatencies(latencies),
    validation: { ok: failures.length === 0, failures },
    validationFailures: failures.map((failure) => `${query.name}#${failure.sample}`),
  };
}

// judgeSecretFreeMismatchedKey is the secret-free `data.key` variant as one
// decision. Non-disclosure alone is not the claim: a process that never started
// also discloses nothing, so the store must still settle at /health 200.
export function judgeSecretFreeMismatchedKey({ healthStatus, leakedSecretCount } = {}) {
  const failures = [];
  if (healthStatus !== 200) failures.push(`health_status:${healthStatus}`);
  if (leakedSecretCount !== 0) failures.push('secret_leak');
  return { ok: failures.length === 0, failures };
}

// judgeDiagnostic is the Gate B1 contract as one pure decision. It fails closed:
// an unreadable body, a missing marker, a business route that answers, an overrun
// or any registered secret in a body or log line is a failure with a named code.
export function judgeDiagnostic({
  label,
  elapsedMs,
  budgetMs = READINESS_GATE_BC_CONTRACT.diagnosticBudgetMs,
  healthStatus,
  healthBody,
  discoveryStatus,
  discoveryBody,
  businessStatus,
  logText = '',
  requiredLogMarker = null,
  secrets = [],
}) {
  const failures = [];
  if (!Number.isFinite(elapsedMs)) {
    failures.push('elapsed_ms_unobserved');
  } else if (elapsedMs > budgetMs) {
    failures.push(`diagnostic_budget_exceeded:${Math.round(elapsedMs)}>${budgetMs}`);
  }
  if (healthStatus !== 503) failures.push(`health_status:${healthStatus}`);
  const health = parsedBody(healthBody);
  if (!health || typeof health !== 'object' || Object.keys(health).length !== 1 || health.ok !== false) {
    failures.push(`health_body:${String(healthBody ?? '').slice(0, 80)}`);
  }
  if (discoveryStatus !== 503) failures.push(`discovery_status:${discoveryStatus}`);
  const discovery = parsedBody(discoveryBody);
  if (!discovery || discovery.status !== 'unavailable') {
    failures.push(`discovery_body:${String(discoveryBody ?? '').slice(0, 120)}`);
  }
  if (businessStatus !== 404) failures.push(`business_route:${businessStatus}`);
  if (requiredLogMarker && !String(logText).includes(requiredLogMarker)) {
    failures.push('log_marker_missing');
  }
  const leaked = leaksIn(`${healthBody ?? ''}\n${discoveryBody ?? ''}\n${logText}`, secrets);
  if (leaked.length > 0) failures.push(`secret_leak:${leaked.length}`);
  return {
    ok: failures.length === 0,
    failures,
    observed: {
      label,
      elapsedMs: Number.isFinite(elapsedMs) ? Math.round(elapsedMs) : null,
      budgetMs,
      healthStatus,
      discoveryStatus,
      businessStatus,
      logMarkerPresent: requiredLogMarker ? String(logText).includes(requiredLogMarker) : null,
      leakedSecretCount: leaked.length,
    },
  };
}

// judgeOfflineLock is the positive form of "the failed process released the
// store's offline maintenance lock". It never infers release from the absence of
// one error string:
//
//   * a lock refusal in the output means the lock was NOT free, whatever the
//     exit code;
//   * the healthy store must let `cleanup-derived` finish (exit 0) — anything
//     else is a failure, because that command has real work to do here;
//   * the corrupted store must fail for the corrupted file's own reason, i.e.
//     the command acquired the lock and then hit SQLite's validation. Any other
//     non-zero, an empty output or an unobserved status fails.
export function judgeOfflineLock({ kind, exitCode, output = '' } = {}) {
  const contract = READINESS_GATE_BC_CONTRACT;
  const text = String(output ?? '');
  const failures = [];
  const status = typeof exitCode === 'number' && Number.isInteger(exitCode) ? exitCode : null;
  const lockRefused = text.includes(contract.offlineLockRefusal);
  if (lockRefused) failures.push(`offline_lock_unavailable:${status === null ? 'null' : status}`);
  if (status === null) {
    failures.push('offline_status_unobserved');
  } else if (kind === 'unsupported_derived_format') {
    if (status !== 0) failures.push(`offline_cleanup_failed:${status}`);
  } else if (kind === 'corrupted_database_bytes') {
    if (status === 0) {
      failures.push('offline_cleanup_unexpected_success');
    } else if (!text.includes(contract.sqliteValidationFailure)) {
      failures.push(`offline_cleanup_unexpected_failure:${status}`);
    }
  } else {
    failures.push(`offline_kind_unknown:${kind}`);
  }
  return {
    ok: failures.length === 0,
    failures,
    // "released" is the positive statement: the command either completed its
    // work or reached the store's own validation, and in both cases it held the
    // lock. A refusal keeps it false even when the exit code looks benign.
    released: failures.length === 0,
    exitCode: status,
    lockRefused,
  };
}

// judgeFixtureExercised aggregates the body checks of BOTH measured runs. Every
// sample of every query is validated where it is taken, so one bad response
// among five fails the check instead of being overwritten by the next sample,
// and the rollup-off run cannot hide behind the rollup-on run.
export function judgeFixtureExercised({
  rollupOn,
  rollupOff,
  expectedQueries = [],
  expectedSampleCount = null,
} = {}) {
  const failures = [
    ...(rollupOn?.validationFailures ?? []).map((entry) => `rollupOn:${entry}`),
    ...(rollupOff?.validationFailures ?? []).map((entry) => `rollupOff:${entry}`),
  ];
  const sampled = (run) => Object.keys(run?.samples ?? {}).sort();
  const on = sampled(rollupOn);
  const off = sampled(rollupOff);
  if (on.length === 0) failures.push('rollupOn:no_queries_sampled');
  if (off.length === 0) failures.push('rollupOff:no_queries_sampled');
  // Both configurations must have been measured on the same non-empty query set:
  // a claim that the fixture is exercised cannot rest on one run alone.
  if (on.join(',') !== off.join(',')) {
    failures.push(`query_sets_differ:${on.join('+') || '-'}|${off.join('+') || '-'}`);
  }
  const expected = [...new Set(expectedQueries)].sort();
  const missing = expected.filter((name) => !on.includes(name) || !off.includes(name));
  if (missing.length > 0) failures.push(`queries_missing:${missing.join(',')}`);
  // Each sampled query must carry the configured number of measurements, so a
  // run that silently kept only one response per query cannot pass.
  if (Number.isInteger(expectedSampleCount) && expectedSampleCount > 0) {
    for (const [label, run, names] of [
      ['rollupOn', rollupOn, on],
      ['rollupOff', rollupOff, off],
    ]) {
      for (const name of names) {
        const count = run?.samples?.[name]?.count;
        if (count !== expectedSampleCount) {
          failures.push(`${label}:${name}:sample_count:${count === undefined ? 'null' : count}`);
        }
      }
    }
  }
  return {
    ok: failures.length === 0,
    failures,
    rollupOnSampled: on,
    rollupOffSampled: off,
    expectedQueries: expected,
  };
}

// ---------------------------------------------------------------------------
// process helpers
// ---------------------------------------------------------------------------

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

function freePort() {
  return new Promise((resolve, reject) => {
    const probe = net.createServer();
    probe.once('error', reject);
    probe.listen(0, '127.0.0.1', () => {
      const { port } = probe.address();
      probe.close(() => resolve(port));
    });
  });
}

function localDayStartMs(nowMs) {
  const date = new Date(nowMs);
  date.setHours(0, 0, 0, 0);
  return date.getTime();
}

// ---------------------------------------------------------------------------
// main run
// ---------------------------------------------------------------------------

function parseArgs(argv) {
  const options = {
    images: { ...DEFAULT_IMAGES },
    output: process.env.READINESS_GATE_BC_OUTPUT || path.join(ROOT, 'bin', 'tmp', 'readiness-gate-bc', 'evidence'),
    workDir: process.env.READINESS_GATE_BC_WORK_DIR || path.join(ROOT, 'bin', 'tmp', 'readiness-gate-bc', 'work'),
    // The versioned copy the report links to, repo-relative on disk.
    repoEvidenceDir:
      process.env.READINESS_GATE_BC_REPO_EVIDENCE ||
      path.join(ROOT, 'docs', 'architecture', 'evidence', 'v2-beta-readiness'),
  };
  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index];
    const value = () => {
      const next = argv[index + 1];
      if (!next) throw new Error(`${argument} needs a value`);
      index += 1;
      return next;
    };
    switch (argument) {
      case '--manager-image':
        options.images.manager = value();
        break;
      case '--runtime-image':
        options.images.runtime = value();
        break;
      case '--ingress-image':
        options.images.ingress = value();
        break;
      case '--output':
        options.output = value();
        break;
      case '--work-dir':
        options.workDir = value();
        break;
      case '--repo-evidence-dir':
        options.repoEvidenceDir = value();
        break;
      case '--help':
      case '-h':
        options.help = true;
        break;
      default:
        throw new Error(`unknown argument ${JSON.stringify(argument)}`);
    }
  }
  return options;
}

const USAGE = `Usage: node bin/ci/readiness-gate-bc.mjs [--manager-image REF] [--runtime-image REF]
       [--ingress-image REF] [--output DIR] [--work-dir DIR] [--repo-evidence-dir DIR]

Runs the Gate B/C readiness evidence against the candidate Manager image:
the <=10s safe Store/DB diagnostics, the 2 vCPU / 4 GB benchmark on the
100k-event fixture, and the hourly-rollup on/off read latencies. Nothing is
built; every object this run creates is removed before it exits.`;

async function runMain(argv = process.argv.slice(2)) {
  const options = parseArgs(argv);
  if (options.help) {
    process.stdout.write(`${USAGE}\n`);
    return 0;
  }

  const runId = randomBytes(4).toString('hex');
  const prefix = `cpampd${runId}`;
  const redactor = createRedactor([]);
  // Published for the top-level catch: it lives outside this function, and a
  // failed run must print the same redacted message the run would.
  fatalRedactor = redactor;
  const registry = createResourceRegistry({ prefix });
  const checks = [];
  const scenarios = {};
  const imageEvidence = {};
  const ports = new Set();
  // The run's own host paths become placeholders everywhere, including on the
  // failure paths that archive an error message verbatim. The longest prefix is
  // registered first by the redactor's own ordering, so a child path collapses
  // with its parent. This happens before anything is created, so the first fs
  // error already has its path registered.
  for (const hostPath of [options.workDir, options.output, options.repoEvidenceDir]) {
    redactor.add(hostPath);
  }
  // Fail fast, before any Docker work or scratch directory: a run that cannot
  // write its evidence is not worth two minutes of containers. `mkdirSync`
  // succeeds on an existing read-only directory, so both directories are probed
  // with a private create/write/unlink. The error reaches the top-level catch,
  // which prints a path-free summary.
  const { runWorkDir } = prepareRunDirectories({
    output: options.output,
    repoEvidenceDir: options.repoEvidenceDir,
    workDir: options.workDir,
    prefix,
  });
  redactor.add(runWorkDir);
  let abortedSignal = null;
  let cleanup = { ok: true, released: [], errors: [] };

  const redact = (value) => redactor.redact(value);
  const record = (name, result, detail) => {
    checks.push({
      name,
      result,
      ...(detail === undefined ? {} : { detail: redact(JSON.stringify(detail)) }),
    });
    process.stdout.write(`${result} ${name}${detail === undefined ? '' : ` — ${redact(JSON.stringify(detail))}`}\n`);
  };

  const docker = (args, { check = true } = {}) => {
    const result = spawnSync('docker', args, {
      cwd: ROOT,
      encoding: 'utf8',
      timeout: 300_000,
      maxBuffer: 32 * MIB,
    });
    const printed = `docker ${args.slice(0, 6).join(' ')}`;
    if (result.error) throw new Error(`${printed}: ${redact(result.error.message)}`);
    if (check && result.status !== 0) {
      throw new Error(
        `${printed} exited ${result.status}: ${redact(`${result.stderr || ''}${result.stdout || ''}`).slice(0, 2000)}`
      );
    }
    return result;
  };

  // Only "No such container/volume" means the object is gone. Every other
  // inspect failure keeps the object in the registry, so cleanup has to say it
  // could not verify the removal instead of passing on an unanswered inspect.
  const inspectObject = (kind, name) =>
    classifyDockerInspect(
      docker([kind === 'container' ? 'container' : 'volume', 'inspect', name], { check: false })
    );

  // Containers start by the image id the evidence recorded, never by the mutable
  // tag, so a retag between the pre-check and the start cannot swap the build
  // under test.
  const startImage = (role) => imageEvidence[role]?.id || options.images[role];

  const releaseDocker = createDockerReleaser({
    inspect: inspectObject,
    removeObject: (kind, name) =>
      docker(kind === 'container' ? ['rm', '-f', name] : ['volume', 'rm', '-f', name], { check: false }),
  });

  // Every `docker run --rm` this harness starts is named inside the run prefix,
  // registered before it starts and built through the same `dockerRunArgs`, so it
  // carries the run's resource budget too. `--rm` normally removes it; if it
  // survives (timeout, signal, daemon error) the registry removes it — and when
  // the daemon cannot even answer whether it is gone, the entry stays so cleanup
  // fails rather than assuming.
  const runEphemeral = (label, spec, command = []) => {
    const suffix = randomBytes(3).toString('hex');
    const name = `${prefix}-${String(label)
      .replace(/[^a-z0-9]/gi, '')
      .toLowerCase()}-${suffix}`;
    registry.assertCreationAllowed('container', name);
    registry.track('container', name, () => releaseDocker('container', name));
    const result = docker([...dockerRunArgs({ ...spec, name, rm: true }), ...command], { check: false });
    const settled = settleEphemeralTracking({
      registry,
      name,
      inspected: inspectObject('container', name),
    });
    if (settled.unverified) {
      // Kept in the registry on purpose: cleanup will retry the removal and fail
      // with a named error if the container is really still there.
      result.unverifiedRemoval = settled.unverified;
    }
    return result;
  };

  const takePort = async () => {
    for (let attempt = 0; attempt < 20; attempt += 1) {
      const port = await freePort();
      if (!ports.has(port)) {
        ports.add(port);
        return port;
      }
    }
    throw new Error('could not reserve a free loopback port');
  };

  const cleanupResources = async () => {
    registry.fence('finalizing the run');
    const outcome = await registry.releaseAll();
    const cleanupContainer = `${prefix}-cleanup`;
    let helperFailure = null;
    const workspace = removeRunWorkspace(runWorkDir, {
      fallbackRemove: (directory) => {
        // A container user may own files the host user cannot unlink. The
        // candidate image removes the directory through the same mount. This is
        // the one container created after the registry is fenced, so it names
        // itself inside the same prefix, uses `--rm`, carries the same resource
        // budget, and is verified gone afterwards: a helper that outlives the
        // cleanup is a leak like any other.
        const helperRun = docker(
          [
            ...dockerRunArgs({
              name: cleanupContainer,
              image: startImage('manager'),
              rm: true,
              entrypoint: 'sh',
              mounts: [`${path.dirname(directory)}:/w`],
            }),
            '-c',
            `rm -rf /w/${path.basename(directory)}`,
          ],
          { check: false }
        );
        // The helper's own removal is classified like every other removal: exit 0
        // or a definite "No such container" may continue, anything else
        // (permissions, an unreachable daemon) fails the cleanup even when the
        // final inspect happens to report the container absent.
        const helper = judgeCleanupHelper({
          name: cleanupContainer,
          runResult: helperRun,
          removeResult: docker(['rm', '-f', cleanupContainer], { check: false }),
          inspected: inspectObject('container', cleanupContainer),
        });
        if (!helper.ok) {
          helperFailure = redact(helper.error);
          throw new Error(helperFailure);
        }
      },
    });
    outcome.workspace = workspace;
    if (helperFailure) {
      outcome.ok = false;
      outcome.errors.push(helperFailure);
    }
    if (!workspace.ok) {
      outcome.ok = false;
      outcome.errors.push(workspace.error);
    }
    // The run owns `<work-dir>/cpampd<runId>`; the parent is shared and is only
    // removed when this run left it empty, so a second run starts from nothing
    // without deleting a directory another run is using.
    try {
      fs.rmdirSync(options.workDir);
      outcome.workspace.parentRemoved = true;
    } catch {
      outcome.workspace.parentRemoved = false;
    }
    return outcome;
  };

  // One awaitable cleanup for both exits. The signal guard stays attached until
  // it settles, so a SIGTERM during finalisation waits for the releases instead
  // of terminating the process with resources still on the host.
  const cleanupCoordinator = createCleanupCoordinator({
    cleanupResources,
    exitCode: finalExitCode,
    redact,
  });
  const shutdown = async (signal) => {
    abortedSignal = abortedSignal || signal;
    registry.fence(`aborted by ${signal}`);
    return cleanupCoordinator.abort({ signal });
  };
  const signalGuard = installSignalGuard({ onSignal: shutdown });

  // -- process helpers ------------------------------------------------------

  const formatGiB = (bytes) => `${(bytes / GIB).toFixed(2)} GiB`;

  function hostFacts() {
    const server = docker(['version', '--format', '{{.Server.Os}}/{{.Server.Arch}} {{.Server.Version}}'], {
      check: false,
    });
    return {
      os: os.platform(),
      arch: os.arch(),
      release: os.release(),
      cpus: os.cpus().length,
      totalMemoryBytes: os.totalmem(),
      dockerServer: server.status === 0 ? server.stdout.trim() : null,
    };
  }

  function observeDisk() {
    const result = runEphemeral(
      'disk',
      { image: startImage('manager'), network: 'none', entrypoint: 'sh' },
      ['-c', 'df -Pk / | tail -1']
    );
    if (result.status !== 0) throw new Error(`could not read the Docker VM free space: ${redact(result.stderr)}`);
    const fields = result.stdout.trim().split(/\s+/);
    return {
      mount: fields[5] || '/',
      totalBytes: Number(fields[1]) * 1024,
      availableBytes: Number(fields[3]) * 1024,
      floorBytes: READINESS_GATE_BC_CONTRACT.diskFloorBytes,
    };
  }

  function startContainer({ name, mount, port, env }) {
    registry.assertCreationAllowed('container', name);
    registry.track('container', name, () => releaseDocker('container', name));
    const image = startImage('manager');
    const args = dockerRunArgs({
      name,
      image,
      detach: true,
      mounts: [`${mount}:/data`],
      ports: [`127.0.0.1:${port}:18317`],
      env,
    });
    docker(args);
    // The evidence records the id, so the container that actually runs must be
    // that id — not merely the tag it was started from.
    const actual = docker(['inspect', name, '--format', '{{.Image}}']).stdout.trim();
    return verifyStartedImage({ name, expected: imageEvidence.manager?.id ?? null, actual });
  }

  function containerId(name) {
    return docker(['inspect', name, '--format', '{{.Id}}']).stdout.trim();
  }

  function containerLogs(name) {
    const result = docker(['logs', '--tail', '400', name], { check: false });
    return redact(`${result.stdout || ''}${result.stderr || ''}`);
  }

  function containerState(name) {
    const raw = docker(['inspect', name, '--format', '{{json .State}} {{.RestartCount}}'], { check: false });
    if (raw.status !== 0) return null;
    const [stateJSON, restartCount] = raw.stdout.trim().split(/ (?=\d+$)/);
    const state = parsedBody(stateJSON);
    return state ? { ...state, RestartCount: Number(restartCount) } : null;
  }

  // cgroup memory.peak charges the page cache to the container, so it is not
  // the process's resident set. The plan's target is RSS, so the budget is judged
  // on VmHWM; the cgroup figure is recorded beside it for completeness.
  function processPeakRssBytes(name) {
    const result = docker(['exec', name, 'sh', '-c', "awk '/VmHWM/{print $2}' /proc/1/status"], { check: false });
    const kilobytes = Number(result.stdout.trim());
    return Number.isFinite(kilobytes) && kilobytes > 0 ? kilobytes * 1024 : null;
  }

  function cgroupPeakMemoryBytes(name) {
    const result = docker(
      [
        'exec',
        name,
        'sh',
        '-c',
        'cat /sys/fs/cgroup/memory.peak 2>/dev/null || cat /sys/fs/cgroup/memory/memory.max_usage_in_bytes',
      ],
      { check: false }
    );
    const value = Number(result.stdout.trim());
    return Number.isFinite(value) && value > 0 ? value : null;
  }

  async function request(url, { method = 'GET', body = undefined, headers = {} } = {}) {
    const startedAt = performance.now();
    const response = await fetch(url, {
      method,
      headers: {
        ...headers,
        ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
      },
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
      signal: AbortSignal.timeout(120_000),
    });
    const text = await response.text();
    return { status: response.status, body: text, ms: performance.now() - startedAt };
  }

  async function fetchUntil(url, accept, { timeoutMs, intervalMs = 50, label }) {
    const startedAt = performance.now();
    let lastError = null;
    while (performance.now() - startedAt <= timeoutMs) {
      try {
        const response = await fetch(url, { signal: AbortSignal.timeout(2000) });
        const body = await response.text();
        const value = { status: response.status, body, elapsedMs: performance.now() - startedAt };
        if (!accept || accept(value)) return value;
      } catch (error) {
        lastError = error;
      }
      await sleep(intervalMs);
    }
    throw new Error(
      `${label} did not become observable within ${timeoutMs}ms${lastError ? `: ${redact(lastError.message)}` : ''}`
    );
  }

  function initializeStore(dataDir) {
    const result = runEphemeral(
      'initialize',
      { image: startImage('manager'), network: 'none', mounts: [`${dataDir}:/data`] },
      ['initialize']
    );
    if (result.status !== 0) {
      throw new Error(`initialize exited ${result.status}: ${redact(`${result.stderr || ''}${result.stdout || ''}`).slice(0, 800)}`);
    }
    // The data key is a secret: register its contents so no log, body or
    // evidence document can carry it.
    const dataKeyPath = path.join(dataDir, 'data.key');
    if (fs.existsSync(dataKeyPath)) {
      const dataKey = fs.readFileSync(dataKeyPath, 'utf8').trim();
      if (dataKey) redactor.add(dataKey);
    }
  }

  // The evidence records the environment the run used, never the credential in
  // it: the admin key is what authenticates every benchmark read.
  function recordedEnv(env) {
    return Object.fromEntries(
      Object.entries(env).map(([key, value]) => [key, key === 'CPA_MANAGER_ADMIN_KEY' ? '[redacted]' : value])
    );
  }

  // The data key decrypts every persisted secret, so its bytes join the leak
  // checks as well as the redactor.
  function readDataKey(dataDir) {
    const keyPath = path.join(dataDir, 'data.key');
    return fs.existsSync(keyPath) ? fs.readFileSync(keyPath, 'utf8').trim() : '';
  }

  function containerEnv(adminKey, extra = {}) {
    return {
      HTTP_ADDR: '0.0.0.0:18317',
      USAGE_DB_PATH: '/data/usage.sqlite',
      CPA_MANAGER_DATA_KEY_PATH: '/data/data.key',
      CPA_MANAGER_ADMIN_KEY: adminKey,
      CPAMP_UPDATE_CHECK_ENABLED: 'false',
      ...extra,
    };
  }

  // -- scenarios ------------------------------------------------------------

  // A safe diagnostic response must be observable, must not expose the store,
  // and must leave the offline maintenance lock free while the process still
  // runs. `cleanup-derived` is the offline command an operator would run; its
  // lock acquisition is the proof the failed process released admission.
  async function runStoreOpenFailure({ name, kind, marker }) {
    const dataDir = path.join(runWorkDir, name);
    fs.mkdirSync(dataDir, { recursive: true });
    const adminKey = `Cpamp_Pr3_${randomBytes(12).toString('hex')}`;
    const sentinel = `pr3-untrusted-${randomBytes(8).toString('hex')}`;
    redactor.add(adminKey);
    redactor.add(sentinel);

    initializeStore(dataDir);
    const dbPath = path.join(dataDir, 'usage.sqlite');
    if (kind === 'unsupported_derived_format') {
      completeFixtureStore(dbPath, { hasHistoricalData: false });
      injectUnsupportedDerivedFormat(dbPath, sentinel);
    } else if (kind === 'corrupted_database_bytes') {
      fs.rmSync(`${dbPath}.manager.lock`, { force: true });
      for (const suffix of ['-wal', '-shm']) fs.rmSync(`${dbPath}${suffix}`, { force: true });
      fs.writeFileSync(dbPath, 'not a sqlite database'.repeat(100));
    } else {
      throw new Error(`unknown store failure kind ${kind}`);
    }

    const container = `${prefix}-${name.replace(/[^a-z0-9]/gi, '')}`;
    const port = await takePort();
    startContainer({
      name: container,
      mount: dataDir,
      port,
      env: containerEnv(adminKey),
    });

    const origin = `http://127.0.0.1:${port}`;
    let health;
    try {
      health = await fetchUntil(`${origin}/health`, null, { timeoutMs: 30_000, label: `${name} /health` });
    } catch (error) {
      return {
        kind,
        container,
        passed: false,
        judgement: {
          ok: false,
          failures: ['health_unobservable'],
          observed: { error: redact(error.message), logTail: containerLogs(container).slice(-2000) },
        },
      };
    }

    // The discovery projection and a business route are read after the health
    // answer, so the budget belongs to the first observable diagnostic.
    const discovery = await request(`${origin}/.well-known/cpamp-bootstrap`);
    const business = await request(`${origin}/v0/management/dashboard/summary?today_start_ms=1`);
    const logs = (await waitForMarker(container, marker, 2000)) || containerLogs(container);

    const judgement = judgeDiagnostic({
      label: name,
      elapsedMs: health.elapsedMs,
      healthStatus: health.status,
      healthBody: health.body,
      discoveryStatus: discovery.status,
      discoveryBody: discovery.body,
      businessStatus: business.status,
      logText: logs,
      requiredLogMarker: marker,
      secrets: [adminKey, sentinel, dataDir, readDataKey(dataDir)],
    });

    // The lock probe runs while the diagnostic process is still alive: a
    // release-on-exit would not prove host maintenance is unblocked.
    const lockProbe = runEphemeral(
      'lockprobe',
      { image: startImage('manager'), network: 'none', mounts: [`${dataDir}:/data`] },
      ['cleanup-derived', '--db-path', '/data/usage.sqlite']
    );
    const lockText = redact(`${lockProbe.stdout || ''}${lockProbe.stderr || ''}`);
    // Positive evidence only: the healthy store's offline command must finish,
    // and the corrupted store's must reach SQLite's own validation. Any other
    // non-zero, an empty output or an unobserved exit fails the scenario rather
    // than counting as "the lock was free".
    const lock = judgeOfflineLock({ kind, exitCode: lockProbe.status, output: lockText });
    if (!lock.ok) judgement.failures.push(...lock.failures);
    judgement.ok = judgement.failures.length === 0;
    judgement.offline = lock;

    const state = containerState(container);
    return {
      kind,
      container: { name: container, id: containerId(container), image: startImage('manager') },
      elapsedMs: Math.round(health.elapsedMs),
      health: { status: health.status, body: health.body.slice(0, 400) },
      discovery: { status: discovery.status, body: discovery.body.slice(0, 400) },
      businessStatus: business.status,
      logTail: logs.slice(-2000),
      lockProbe: {
        exitCode: lockProbe.status,
        released: lock.released,
        failures: lock.failures,
        output: lockText.slice(-500),
      },
      oomKilled: state?.OOMKilled ?? null,
      judgement,
    };
  }

  async function waitForMarker(container, marker, timeoutMs) {
    const startedAt = performance.now();
    while (performance.now() - startedAt <= timeoutMs) {
      const text = containerLogs(container);
      if (text.includes(marker)) return text;
      await sleep(50);
    }
    return null;
  }

  // The plan's second injection: a different data key over a store that carries
  // encrypted connection settings. The fixture writes `manager_config_v1` and
  // `setup` with the original key through the product's own envelope, then swaps
  // `data.key`; the candidate must fail closed and disclose neither key.
  // The secret-free variant is measured too, because it shows the ordinary
  // `/health 200` path that a bare key swap produces.
  async function runMismatchedDataKey() {
    const adminKey = `Cpamp_Pr3_${randomBytes(12).toString('hex')}`;
    redactor.add(adminKey);
    const managementKey = `pr3-cpa-management-${randomBytes(10).toString('hex')}`;
    redactor.add(managementKey);

    const observeSecretFree = async () => {
      const dataDir = path.join(runWorkDir, 'mismatched-data-key-empty');
      fs.mkdirSync(dataDir, { recursive: true });
      initializeStore(dataDir);
      completeFixtureStore(path.join(dataDir, 'usage.sqlite'), { hasHistoricalData: false });
      const mismatchedKey = randomBytes(32).toString('base64');
      redactor.add(mismatchedKey);
      fs.writeFileSync(path.join(dataDir, 'data.key'), `${mismatchedKey}\n`);

      const container = `${prefix}-mismatchedkeyempty`;
      const port = await takePort();
      startContainer({ name: container, mount: dataDir, port, env: containerEnv(adminKey) });
      const origin = `http://127.0.0.1:${port}`;
      let health = null;
      const startedAt = performance.now();
      while (performance.now() - startedAt <= 30_000) {
        try {
          const response = await fetch(`${origin}/health`, { signal: AbortSignal.timeout(2000) });
          const body = await response.text();
          health = { status: response.status, body, elapsedMs: performance.now() - startedAt };
          if (response.status === 200) break;
        } catch {}
        await sleep(100);
      }
      if (!health) throw new Error('mismatched data key (secret-free store): /health was never observable');
      const logs = containerLogs(container);
      return {
        healthStatus: health.status,
        settledAtMs: Math.round(health.elapsedMs),
        leakedSecretCount: leaksIn(logs, [adminKey, mismatchedKey, managementKey]).length,
        logTail: logs.slice(-1000),
        note:
          health.status === 200
            ? 'the secret-free fixture has no encrypted connection secret, so a mismatched data key does not reach RequireDataKeyForInspectedConnection or decryption; the candidate settles at /health 200'
            : 'the candidate did not reach /health 200 on the secret-free store',
      };
    };

    const observeEncrypted = async () => {
      const dataDir = path.join(runWorkDir, 'mismatched-data-key-encrypted');
      fs.mkdirSync(dataDir, { recursive: true });
      initializeStore(dataDir);
      const dbPath = path.join(dataDir, 'usage.sqlite');
      completeFixtureStore(dbPath, { hasHistoricalData: false });
      const originalDataKey = fs.readFileSync(path.join(dataDir, 'data.key'), 'utf8').trim();
      if (originalDataKey) redactor.add(originalDataKey);
      const envelope = protectSettingValue(originalDataKey, managementKey);
      redactor.add(envelope);
      const db = new DatabaseSync(dbPath);
      try {
        const insert = db.prepare(
          `insert into settings(key, value, updated_at_ms) values (?, ?, 0)
           on conflict(key) do update set value = excluded.value, updated_at_ms = excluded.updated_at_ms`
        );
        insert.run(
          'manager_config_v1',
          JSON.stringify({
            cpaConnection: { cpaBaseUrl: 'http://cpa.pr3.example.test:8317', managementKey: envelope },
            collector: { enabled: false },
            codexInspection: {},
            externalUsageService: { enabled: false },
          })
        );
        insert.run(
          'setup',
          JSON.stringify({ cpaBaseUrl: 'http://cpa.pr3.example.test:8317', managementKey: envelope })
        );
      } finally {
        db.close();
      }

      const mismatchedKey = randomBytes(32).toString('base64');
      redactor.add(mismatchedKey);
      fs.writeFileSync(path.join(dataDir, 'data.key'), `${mismatchedKey}\n`);

      const container = `${prefix}-mismatchedkeyencrypted`;
      const port = await takePort();
      startContainer({ name: container, mount: dataDir, port, env: containerEnv(adminKey) });
      const origin = `http://127.0.0.1:${port}`;
      const startedAt = performance.now();
      let health = null;
      while (performance.now() - startedAt <= 30_000) {
        const state = containerState(container);
        if (state && state.Running === false) break;
        try {
          const response = await fetch(`${origin}/health`, { signal: AbortSignal.timeout(1000) });
          health = { status: response.status, body: await response.text(), elapsedMs: performance.now() - startedAt };
        } catch {}
        await sleep(100);
      }
      const state = containerState(container);
      const logs = containerLogs(container);
      const secrets = [adminKey, managementKey, envelope, originalDataKey, mismatchedKey];
      const sealed = state?.ExitCode !== undefined && state.ExitCode !== 0;
      const failures = [];
      if (!sealed) failures.push(`expected_fail_closed_exit:${state?.ExitCode}`);
      if (!logs.includes('bootstrap manager server')) failures.push('bootstrap_failure_log_missing');
      const leaked = leaksIn(logs, secrets);
      if (leaked.length > 0) failures.push(`secret_leak:${leaked.length}`);
      return {
        exitCode: state?.ExitCode ?? null,
        running: state?.Running ?? null,
        elapsedMs: state ? Math.round(performance.now() - startedAt) : null,
        healthStatus: health?.status ?? null,
        failures,
        leakedSecretCount: leaked.length,
        logTail: logs.slice(-1400),
        note:
          'RequireDataKeyForInspectedConnection only checks that data.key exists; the mismatched key is rejected when bootstrap.Run decrypts manager_config_v1/setup, and main.go fails closed with log.Fatalf',
      };
    };

    const secretFree = await observeSecretFree();
    const encrypted = await observeEncrypted();
    return { secretFree, encrypted };
  }

  async function runBenchmark() {
    const dataDir = path.join(runWorkDir, 'benchmark');
    fs.mkdirSync(dataDir, { recursive: true });
    const adminKey = `Cpamp_Pr3_${randomBytes(12).toString('hex')}`;
    redactor.add(adminKey);
    initializeStore(dataDir);
    const dbPath = path.join(dataDir, 'usage.sqlite');
    completeFixtureStore(dbPath, { hasHistoricalData: true });
    const nowMs = Date.now();
    const seedStartedAt = performance.now();
    const fixture = seedFixture(dbPath, { nowMs, count: READINESS_GATE_BC_CONTRACT.fixtureEvents });
    const seedMs = performance.now() - seedStartedAt;

    // SQLite on a macOS bind mount is not the target host's storage. The fixture
    // is prepared on the host (Node can only write there) and copied into a
    // Docker volume, so every measured read and index build runs on the VM's own
    // filesystem rather than the shared-folder layer.
    const volume = `${prefix}-bench`;
    registry.assertCreationAllowed('volume', volume);
    registry.track('volume', volume, () => releaseDocker('volume', volume));
    docker(['volume', 'create', volume]);
    runEphemeral(
      'seedcopy',
      {
        image: startImage('manager'),
        entrypoint: 'sh',
        mounts: [`${dataDir}:/seed:ro`, `${volume}:/data`],
      },
      ['-c', 'cp -a /seed/. /data/']
    );
    const benchmarkMount = volume;

    const result = {
      storage: { kind: 'docker-volume', volume },
      fixture,
      seedMs: Math.round(seedMs),
      dbBytes: fs.statSync(dbPath).size,
      limits: READINESS_GATE_BC_CONTRACT.limits,
      checks: {},
      runs: {},
    };

    const measureRun = async ({ label, container, env, waitForDerived, queries }) => {
      const port = await takePort();
      const startedAt = performance.now();
      startContainer({ name: container, mount: benchmarkMount, port, env });
      const origin = `http://127.0.0.1:${port}`;
      const auth = { Authorization: `Bearer ${adminKey}` };

      const health = await fetchUntil(`${origin}/health`, (value) => value.status === 200, {
        timeoutMs: 120_000,
        intervalMs: 25,
        label: `${label} /health`,
      });
      const coldStartMs = performance.now() - startedAt;

      let maintenanceMs = null;
      let maintenanceMarker = null;
      if (waitForDerived) {
        const maintenanceStartedAt = performance.now();
        const markerPattern = /monitoring stats rebuild completed coverageEventID=(\d+)/g;
        while (
          performance.now() - maintenanceStartedAt <= READINESS_GATE_BC_CONTRACT.derivedMaintenanceTimeoutMs
        ) {
          const logs = containerLogs(container);
          let last = null;
          for (const match of logs.matchAll(markerPattern)) last = Number(match[1]);
          if (last !== null && last >= fixture.count) {
            maintenanceMs = performance.now() - maintenanceStartedAt;
            maintenanceMarker = last;
            break;
          }
          await sleep(500);
        }
        if (maintenanceMs === null) throw new Error(`${label} derived rollups did not finish within the budget`);
      }

      const status = await request(`${origin}/status?scope=database-maintenance`, { headers: auth });
      const maintenanceStatus = parsedBody(status.body);

      // Every sample is validated where it is taken, by the same exported pure
      // function the offline suite exercises: validating only the last body
      // would let four empty responses hide behind a fifth good one.
      const samples = {};
      const validations = {};
      const validationFailures = [];
      for (const query of queries) {
        const responses = [];
        for (let index = 0; index < READINESS_GATE_BC_CONTRACT.sampleCount; index += 1) {
          const response = await request(`${origin}${query.path}`, {
            method: query.method,
            body: query.body,
            headers: auth,
          });
          if (response.status !== 200) {
            throw new Error(`${label} ${query.name} answered ${response.status}: ${redact(response.body).slice(0, 400)}`);
          }
          responses.push({ ms: response.ms, body: response.body });
        }
        const measured = summarizeQuerySamples({ query, responses });
        samples[query.name] = measured.summary;
        validations[query.name] = {
          ok: measured.validation.ok,
          // Failure bodies are archived, so they go through the redactor.
          failures: measured.validation.failures.map((failure) => ({
            sample: failure.sample,
            body: redact(failure.body),
          })),
        };
        validationFailures.push(...measured.validationFailures);
      }

      const peak = processPeakRssBytes(container);
      const cgroupPeak = cgroupPeakMemoryBytes(container);
      const state = containerState(container);
      return {
        container: { name: container, id: containerId(container) },
        env: recordedEnv(env),
        coldStartMs: Math.round(coldStartMs),
        derivedMaintenanceMs: maintenanceMs === null ? null : Math.round(maintenanceMs),
        derivedCoverageEventId: maintenanceMarker,
        maintenanceStatus,
        samples,
        validations,
        validationFailures,
        peakMemoryBytes: peak,
        cgroupPeakMemoryBytes: cgroupPeak,
        memoryMetric: 'process VmHWM (RSS); cgroup memory.peak (includes page cache) recorded separately',
        state,
      };
    };

    const now = Date.now();
    const overview = (days) => ({
      method: 'POST',
      path: '/v0/management/monitoring/analytics',
      body: {
        from_ms: now - days * DAY_MS,
        to_ms: now,
        include: {
          summary: true,
          summary_profile: 'compact',
          summary_percentiles: true,
          summary_comparison: true,
          timeline: true,
          model_stats: true,
          channel_share: true,
          api_key_stats: true,
          anomaly_points: true,
          granularity: 'day',
        },
      },
      validate: (body) => {
        const calls = body?.summary?.total_calls;
        return (
          Number.isFinite(calls) &&
          calls >= Math.floor((fixture.count * days * 0.5) / READINESS_GATE_BC_CONTRACT.fixtureDays)
        );
      },
    });
    const baseQueries = [
      {
        method: 'GET',
        path: `/v0/management/dashboard/summary?today_start_ms=${localDayStartMs(now)}`,
        name: 'dashboard_summary',
        validate: (body) => Number.isFinite(body?.today?.total_calls) && body.today.total_calls > 0,
      },
      {
        method: 'GET',
        path: '/v0/management/monitoring/header-snapshots?days=30&limit=100',
        name: 'monitoring_header_snapshots',
        validate: (body) => Array.isArray(body?.items),
      },
      {
        method: 'POST',
        path: '/v0/management/monitoring/analytics',
        name: 'monitoring_events_page',
        body: {
          from_ms: now - 30 * DAY_MS,
          to_ms: now,
          include: { events_page: { limit: 50 }, granularity: 'day' },
        },
        validate: (body) => Array.isArray(body?.events?.items) && body.events.items.length === 50,
      },
      { ...overview(30), name: 'usage_analytics_overview_30d' },
      { ...overview(90), name: 'usage_analytics_overview_90d' },
      {
        method: 'POST',
        path: '/v0/management/monitoring/analytics',
        name: 'usage_analytics_hourly_24h',
        body: {
          from_ms: now - DAY_MS,
          to_ms: now,
          include: { summary: true, summary_profile: 'compact', granularity: 'hour' },
        },
        validate: (body) => Number.isFinite(body?.summary?.total_calls) && body.summary.total_calls > 0,
      },
    ];

    // Run 1: hourly rollup enabled (the default), the plan's derived-maintenance
    // path, and the full query set.
    const run1 = await measureRun({
      label: 'benchmark-rollup-on',
      container: `${prefix}-benchon`,
      waitForDerived: true,
      queries: baseQueries,
      env: containerEnv(adminKey),
    });
    result.runs.rollupOn = run1;
    docker(['stop', '-t', '20', run1.container.name], { check: false });

    // The deferred indexes are the plan's offline-maintenance step. It runs with
    // the Manager stopped, inside the candidate image, on the same mounted data.
    const cleanupStartedAt = performance.now();
    const cleanupRun = runEphemeral(
      'cleanupderived',
      { image: startImage('manager'), network: 'none', mounts: [`${benchmarkMount}:/data`] },
      ['cleanup-derived', '--db-path', '/data/usage.sqlite']
    );
    const cleanupText = redact(`${cleanupRun.stdout || ''}${cleanupRun.stderr || ''}`);
    const preparedIndexes = (cleanupText.match(/\[derived-migration\] creating index /g) || []).length;
    result.offlineCleanup = {
      exitCode: cleanupRun.status,
      durationMs: Math.round(performance.now() - cleanupStartedAt),
      preparedIndexes,
      output: cleanupText.trim().split('\n').slice(-3).join('\n'),
    };

    // Run 2: hourly rollup disabled, after the offline indexes are prepared.
    const run2 = await measureRun({
      label: 'benchmark-rollup-off',
      container: `${prefix}-benchoff`,
      waitForDerived: false,
      // The same query set as the first run: a claim that the fixture is
      // exercised must cover the queries both configurations were measured on.
      queries: baseQueries,
      env: containerEnv(adminKey, { USAGE_DASHBOARD_HOURLY_ROLLUP_ENABLED: 'false' }),
    });
    result.runs.rollupOff = run2;
    docker(['stop', '-t', '20', run2.container.name], { check: false });

    // -- the benchmark's own assertions --------------------------------------
    const check = (name, ok, detail) => {
      result.checks[name] = { ok, detail };
    };
    const budgets = READINESS_GATE_BC_CONTRACT;
    check(
      'fixture_distribution',
      fixture.count === budgets.fixtureEvents &&
        fixture.uniqueHashes === fixture.count &&
        fixture.models === 12 &&
        fixture.providers === 3 &&
        fixture.accounts === 100 &&
        fixture.apiKeys === 10,
      fixture
    );
    check(
      'cold_start_within_budget',
      run1.coldStartMs <= budgets.coldStartBudgetMs && run2.coldStartMs <= budgets.coldStartBudgetMs,
      { rollupOnMs: run1.coldStartMs, rollupOffMs: run2.coldStartMs, budgetMs: budgets.coldStartBudgetMs }
    );
    check(
      'derived_maintenance_finished',
      run1.derivedMaintenanceMs !== null && run1.derivedCoverageEventId >= fixture.count,
      { ms: run1.derivedMaintenanceMs, coverageEventId: run1.derivedCoverageEventId, status: run1.maintenanceStatus }
    );
    check(
      'offline_maintenance_cleared_deferred_indexes',
      result.offlineCleanup.exitCode === 0 && run2.maintenanceStatus?.databaseMaintenance?.required === false,
      { offline: result.offlineCleanup, afterRestart: run2.maintenanceStatus }
    );
    const maintenancePhase = (run) => ({
      required: run.maintenanceStatus?.databaseMaintenance?.required ?? null,
      deferredIndexes: run.maintenanceStatus?.databaseMaintenance?.deferredIndexes ?? null,
      performanceDegraded: run.maintenanceStatus?.databaseMaintenance?.performanceDegraded ?? null,
    });
    const exercised = judgeFixtureExercised({
      rollupOn: run1,
      rollupOff: run2,
      expectedQueries: baseQueries.map((query) => query.name),
      expectedSampleCount: READINESS_GATE_BC_CONTRACT.sampleCount,
    });
    check('fixture_exercised_by_queries', exercised.ok, {
      failures: exercised.failures,
      rollupOn: run1.validations,
      rollupOff: run2.validations,
    });
    // The two runs are sequential observations under different index states, so
    // the evidence records what each phase actually reported and never treats
    // the difference as an effect of the rollup flag.
    result.indexState = {
      rollupOn: maintenancePhase(run1),
      rollupOff: maintenancePhase(run2),
      comparable: false,
      note:
        'sequential observations: the rollup-on phase ran with 11 deferred indexes pending, the rollup-off phase after cleanup-derived prepared them; the difference is not attributable to the rollup flag alone',
    };
    check(
      'index_states_recorded',
      result.indexState.rollupOn.required === true && result.indexState.rollupOff.required === false,
      result.indexState
    );
    check(
      'peak_memory_within_budget',
      Number.isFinite(run1.peakMemoryBytes) &&
        run1.peakMemoryBytes <= budgets.peakMemoryBudgetBytes &&
        Number.isFinite(run2.peakMemoryBytes) &&
        run2.peakMemoryBytes <= budgets.peakMemoryBudgetBytes,
      {
        rollupOnRssBytes: run1.peakMemoryBytes,
        rollupOffRssBytes: run2.peakMemoryBytes,
        rollupOnCgroupBytes: run1.cgroupPeakMemoryBytes,
        rollupOffCgroupBytes: run2.cgroupPeakMemoryBytes,
        budgetBytes: budgets.peakMemoryBudgetBytes,
        metric: 'process VmHWM (RSS)',
      }
    );
    check(
      'no_oom_and_no_restart',
      run1.state?.OOMKilled === false &&
        run2.state?.OOMKilled === false &&
        run1.state?.RestartCount === 0 &&
        run2.state?.RestartCount === 0,
      { rollupOn: run1.state, rollupOff: run2.state }
    );
    return result;
  }

  // -- provenance -----------------------------------------------------------

  // The plan requires the candidate images' third-party licence collection to be
  // verified from the loaded assets. The candidate images at this base carry
  // `/usr/local/share/cpamp/release/third-party-inventory.txt` (with
  // `status: VERIFIED_LICENSE_COLLECTION`) and `/usr/local/share/cpamp/provenance.json`
  // (which binds the inventory by digest). The review named a
  // `release-provenance.json` / `thirdPartyInventoryStatus` pair that these
  // images do not contain; both the observed paths and their absence are
  // recorded, and the status assertion is made against the observed inventory.
  function runProvenanceCheck() {
    const roles = {};
    for (const [role, reference] of Object.entries(options.images)) {
      const readFromImage = (label, target) =>
        runEphemeral(
          `${label}-${role}`,
          { image: startImage(role), network: 'none', entrypoint: 'cat' },
          [target]
        );
      const inventoryRun = readFromImage('inventory', PROVENANCE_PATHS.inventory);
      const provenanceRun = readFromImage('provenance', PROVENANCE_PATHS.provenance);
      const releaseProvenanceRun = readFromImage('releaseprovenance', PROVENANCE_PATHS.releaseProvenance);
      const inventory = inventoryRun.status === 0 ? inventoryRun.stdout : '';
      const provenance = provenanceRun.status === 0 ? provenanceRun.stdout : '';
      const verification = verifyReleaseProvenance({
        role,
        sourceCommit: CANDIDATE_COMMIT,
        inventory,
        provenance,
        inventoryBytes: Buffer.from(inventory, 'utf8'),
      });
      if (!provenance) verification.failures.push('provenance_missing');
      if (!inventory) verification.failures.push('inventory_missing');
      verification.ok = verification.failures.length === 0;
      roles[role] = {
        reference,
        inventoryPath: PROVENANCE_PATHS.inventory,
        provenancePath: PROVENANCE_PATHS.provenance,
        releaseProvenancePathPresent: releaseProvenanceRun.status === 0,
        inventoryBytes: inventory.length,
        verification,
      };
    }
    return {
      sourceCommit: CANDIDATE_COMMIT,
      releaseProvenancePath: PROVENANCE_PATHS.releaseProvenance,
      roles,
      ok: Object.values(roles).every((entry) => entry.verification.ok),
    };
  }

  // -- execution and evidence ----------------------------------------------

  // The harness's own bytes are part of the evidence: a document is only
  // reproducible if a reader can tell which revision of the script produced it.
  function harnessIdentity() {
    const scriptPath = fileURLToPath(import.meta.url);
    return {
      path: path.relative(ROOT, scriptPath),
      sha256: `sha256:${createHash('sha256').update(fs.readFileSync(scriptPath)).digest('hex')}`,
    };
  }

  function baseEvidence() {
    return {
      contract: READINESS_GATE_BC_CONTRACT.contract,
      generatedAt: new Date().toISOString(),
      harness: harnessIdentity(),
      runId,
      prefix,
      candidateTag: READINESS_GATE_BC_CONTRACT.candidateTag,
      candidateCommit: CANDIDATE_COMMIT,
      platform: READINESS_GATE_BC_CONTRACT.platform,
      limits: READINESS_GATE_BC_CONTRACT.limits,
      budgets: {
        diagnosticMs: READINESS_GATE_BC_CONTRACT.diagnosticBudgetMs,
        coldStartMs: READINESS_GATE_BC_CONTRACT.coldStartBudgetMs,
        peakMemoryBytes: READINESS_GATE_BC_CONTRACT.peakMemoryBudgetBytes,
      },
      imageReferences: options.images,
      images: imageEvidence,
      host: hostFacts(),
      // The Docker host is a desktop VM on macOS x64, not a dedicated SSD Linux
      // host. Any representativeness claim about a production host is an
      // inference, never an observation.
      representativeness: {
        observed: 'Docker Desktop VM on macOS x64; container limits --cpus=2 --memory=4g --memory-swap=4g',
        productionHostClaim:
          'cannot be extrapolated: this is not a dedicated SSD Linux host, and production performance must be re-measured there',
      },
      fixtureConstruction:
        'candidate `initialize` creates the schema and data key; the harness removes the Fresh checkpoint rows, writes the bootstrap-state row and retires the Fresh marker so the benchmark serves the post-install Manager graph',
      scenarios,
      checks,
      cleanup,
      abortedSignal,
      verdict: 'fail',
    };
  }

  // execute returns false only when a blocker was recorded; the finally below
  // owns the single cleanup either way.
  async function execute() {
    for (const [role, reference] of Object.entries(options.images)) {
      const inspected = docker(['image', 'inspect', reference, '--format', '{{.Id}}'], { check: false });
      if (inspected.status !== 0) {
        imageEvidence[role] = { reference, id: null };
        record(`image:${role}`, 'BLOCKED', {
          reference,
          reason: 'not loaded; this harness never builds images',
        });
        scenarios.blocker = {
          code: 'candidate_image_missing',
          role,
          reference,
        };
        return false;
      }
      imageEvidence[role] = { reference, id: inspected.stdout.trim() };
      record(`image:${role}`, 'PASS', { reference, id: inspected.stdout.trim() });
    }

    const disk = observeDisk();
    if (disk.availableBytes < READINESS_GATE_BC_CONTRACT.diskFloorBytes) {
      record('disk_guard', 'BLOCKED', disk);
      scenarios.blocker = {
        code: 'disk_below_floor',
        availableBytes: disk.availableBytes,
        floorBytes: READINESS_GATE_BC_CONTRACT.diskFloorBytes,
        formatted: formatGiB(disk.availableBytes),
      };
      return false;
    }
    scenarios.disk = disk;
    record('disk_guard', 'PASS', disk);

    const provenance = runProvenanceCheck();
    scenarios.provenance = provenance;
    for (const [role, entry] of Object.entries(provenance.roles)) {
      record(`provenance:${role}`, entry.verification.ok ? 'PASS' : 'FAIL', {
        status: entry.verification.thirdPartyInventoryStatus,
        failures: entry.verification.failures,
        inventoryPath: entry.inventoryPath,
      });
    }

    const storeOpen = await runStoreOpenFailure({
      name: 'store-open-failure',
      kind: 'unsupported_derived_format',
      marker: READINESS_GATE_BC_CONTRACT.storeOpenLogMarker,
    });
    scenarios.storeOpenFailure = storeOpen;
    record('store_open_failure:safe_diagnostics', storeOpen.judgement.ok ? 'PASS' : 'FAIL', storeOpen.judgement);

    const corrupted = await runStoreOpenFailure({
      name: 'corrupted-database',
      kind: 'corrupted_database_bytes',
      marker: READINESS_GATE_BC_CONTRACT.readOnlyDiscoveryLogMarker,
    });
    scenarios.corruptedDatabase = corrupted;
    record('corrupted_database:safe_diagnostics', corrupted.judgement.ok ? 'PASS' : 'FAIL', corrupted.judgement);

    const mismatched = await runMismatchedDataKey();
    scenarios.mismatchedDataKey = mismatched;
    // The secret-free variant asserts the ordinary outcome it claims to show:
    // the store settles at /health 200. Disclosure-free alone is not a pass — a
    // process that never started also discloses nothing.
    const secretFree = judgeSecretFreeMismatchedKey(mismatched.secretFree);
    record('mismatched_data_key:secret_free_store', secretFree.ok ? 'PASS' : 'FAIL', {
      healthStatus: mismatched.secretFree.healthStatus,
      settledAtMs: mismatched.secretFree.settledAtMs,
      leakedSecretCount: mismatched.secretFree.leakedSecretCount,
      failures: secretFree.failures,
      note: mismatched.secretFree.note,
    });
    record(
      'mismatched_data_key:encrypted_settings_fail_closed',
      mismatched.encrypted.failures.length === 0 ? 'PASS' : 'FAIL',
      {
        exitCode: mismatched.encrypted.exitCode,
        elapsedMs: mismatched.encrypted.elapsedMs,
        failures: mismatched.encrypted.failures,
        note: mismatched.encrypted.note,
      }
    );

    const benchmark = await runBenchmark();
    scenarios.benchmark = benchmark;
    for (const [name, outcome] of Object.entries(benchmark.checks)) {
      record(`benchmark:${name}`, outcome.ok ? 'PASS' : 'FAIL', outcome.detail);
    }
    return true;
  }

  let runOk = false;
  try {
    runOk = await execute();
    if (runOk) runOk = checks.every((check) => check.result === 'PASS');
  } catch (error) {
    record('harness', 'FAIL', { error: redact(error?.message || String(error)) });
    runOk = false;
  } finally {
    // Await the same promise the signal path awaits — cleanup runs once. The
    // guard stays attached past this point: it also covers the evidence archive,
    // which must not be interrupted half-way.
    cleanup = await cleanupCoordinator.run();
  }

  // A handled signal owns the exit code and is already exiting this process once
  // the cleanup settles; archiving a verdict here would race that exit and could
  // leave a half-written document behind.
  if (signalGuard.signal) {
    return finalExitCode({ runOk: false, cleanupOk: cleanup.ok, signal: signalGuard.signal });
  }

  const document = baseEvidence();
  document.cleanup = cleanup;
  document.verdict = abortedSignal
    ? 'aborted'
    : document.scenarios.blocker
      ? 'blocked'
      : runOk && cleanup.ok
        ? 'pass'
        : 'fail';
  document.exitCode = finalExitCode({
    runOk: document.verdict === 'pass',
    cleanupOk: cleanup.ok,
    signal: abortedSignal,
  });
  document.redactions = redactor.size();

  // The last redaction pass covers the whole document, so a captured field that
  // was not individually scrubbed still cannot carry a registered secret. Both
  // copies are written as one unit: a failure removes whatever was written.
  // The coordinator owns the archive while it is in flight: a signal arriving
  // now makes `shutdown` await this promise, which rolls its own files back
  // before the process exits with the signal's code.
  const written = await cleanupCoordinator.trackArchive(
    writeEvidence({
      document,
      redactor,
      output: options.output,
      repoEvidenceDir: options.repoEvidenceDir,
      runId,
      interrupted: () => signalGuard.signal,
    })
  );
  // The archive is settled: from here the signal guard has nothing left to protect.
  signalGuard.detach();
  if (!written.ok) {
    // An interrupted archive is not a failed one: the signal owns the exit code,
    // and `writeEvidence` has already removed every file it created.
    if (written.aborted) {
      return finalExitCode({ runOk: false, cleanupOk: cleanup.ok, signal: signalGuard.signal });
    }
    throw new Error(`could not write the evidence: ${written.error}`);
  }
  process.stdout.write(`evidence: ${path.basename(written.runFile)}\n`);
  process.stdout.write(`repo evidence: ${path.relative(ROOT, written.repoFile)}\n`);
  process.stdout.write(`verdict: ${document.verdict}\n`);
  return document.exitCode;
}

const isEntryPoint =
  process.argv[1] !== undefined && path.resolve(process.argv[1]) === path.resolve(fileURLToPath(import.meta.url));

if (isEntryPoint) {
  runMain()
    .then((code) => {
      process.exitCode = code;
    })
    .catch((error) => {
      // No stack: it carries the operator's absolute paths, and a failed run
      // must not print them.
      process.stderr.write(`${describeFatal(error, fatalRedactor)}\n`);
      process.exitCode = 1;
    });
}
