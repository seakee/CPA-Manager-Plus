#!/usr/bin/env node
// Offline tests for the Gate B/C readiness harness. No Docker, no network, no
// candidate image: every decision the harness makes is a pure function or a
// local SQLite/registry interaction, so the contract is testable in the Frontend
// job beside the rest of the no-Docker suite.
//
// What is asserted is consumer-visible behaviour, not source text: the ten-second
// budget boundary, the fail-closed diagnostic body/marker/leak rules, the
// nearest-rank percentile the report quotes, the fixture's distribution and
// rollup timestamp, and the registry/signal cleanup that makes "PASS" mean the
// host is clean.

import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { spawn } from 'node:child_process';
import { EventEmitter } from 'node:events';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { DatabaseSync } from 'node:sqlite';
import test from 'node:test';
import { fileURLToPath, pathToFileURL } from 'node:url';

import {
  READINESS_GATE_BC_CONTRACT,
  checkCleanupHelper,
  classifyDockerInspect,
  classifyRemoveResult,
  completeFixtureStore,
  createCleanupCoordinator,
  createDockerReleaser,
  createRedactor,
  createResourceRegistry,
  describeFatal,
  dockerRunArgs,
  finalExitCode,
  fixtureRow,
  fixtureSummary,
  injectUnsupportedDerivedFormat,
  installSignalGuard,
  judgeDiagnostic,
  judgeCleanupHelper,
  judgeFixtureExercised,
  judgeOfflineLock,
  judgeSecretFreeMismatchedKey,
  leaksIn,
  percentile,
  prepareRunDirectories,
  probeWritableDirectory,
  protectSettingValue,
  removeRunWorkspace,
  seedFixture,
  settleEphemeralTracking,
  summarizeLatencies,
  summarizeQuerySamples,
  verifyReleaseProvenance,
  verifyStartedImage,
  writeEvidence,
} from './readiness-gate-bc.mjs';

const DAY_MS = 86_400_000;
const PREFIX = 'cpampd1a2b3c4d';
const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');

function workspace(t, prefix) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), `pr3-${prefix}-`));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  return directory;
}

function safeDiagnosticInput(overrides = {}) {
  return {
    label: 'store-open-failure',
    elapsedMs: 640,
    healthStatus: 503,
    healthBody: '{"ok":false}\n',
    discoveryStatus: 503,
    discoveryBody: '{"contract_version":"x","status":"unavailable"}',
    businessStatus: 404,
    logText: 'listening\nManager store could not be opened; keeping safe not-ready diagnostics available\n',
    requiredLogMarker: READINESS_GATE_BC_CONTRACT.storeOpenLogMarker,
    secrets: ['Cpamp_Private_Key_0001'],
    ...overrides,
  };
}

test('the diagnostic budget is exactly the plan’s ten seconds and fails closed at the boundary', () => {
  assert.equal(READINESS_GATE_BC_CONTRACT.diagnosticBudgetMs, 10_000);
  assert.equal(judgeDiagnostic(safeDiagnosticInput({ elapsedMs: 10_000 })).ok, true);
  const overBudget = judgeDiagnostic(safeDiagnosticInput({ elapsedMs: 10_001 }));
  assert.equal(overBudget.ok, false);
  assert.match(overBudget.failures.join(' '), /diagnostic_budget_exceeded:10001>10000/);
});

test('a diagnostic must be a 503 safe body, an unavailable discovery projection and a closed business surface', () => {
  assert.equal(judgeDiagnostic(safeDiagnosticInput()).ok, true);

  const leaked = judgeDiagnostic(
    safeDiagnosticInput({ logText: 'boom Cpamp_Private_Key_0001 at /Users/operator/data/usage.sqlite' })
  );
  assert.equal(leaked.ok, false);
  assert.match(leaked.failures.join(' '), /secret_leak:1/);

  const wrongHealth = judgeDiagnostic(safeDiagnosticInput({ healthBody: '{"status":"starting","ready":false}' }));
  assert.equal(wrongHealth.ok, false);
  assert.match(wrongHealth.failures.join(' '), /health_body/);

  const exposedRoute = judgeDiagnostic(safeDiagnosticInput({ businessStatus: 200 }));
  assert.equal(exposedRoute.ok, false);
  assert.match(exposedRoute.failures.join(' '), /business_route:200/);

  const wrongDiscovery = judgeDiagnostic(safeDiagnosticInput({ discoveryStatus: 200 }));
  assert.equal(wrongDiscovery.ok, false);
  assert.match(wrongDiscovery.failures.join(' '), /discovery_status:200/);

  const missingMarker = judgeDiagnostic(safeDiagnosticInput({ logText: 'listening\n' }));
  assert.equal(missingMarker.ok, false);
  assert.match(missingMarker.failures.join(' '), /log_marker_missing/);

  const unobserved = judgeDiagnostic(safeDiagnosticInput({ elapsedMs: null }));
  assert.equal(unobserved.ok, false);
  assert.match(unobserved.failures.join(' '), /elapsed_ms_unobserved/);
});

test('the nearest-rank percentile reports samples the run actually observed', () => {
  assert.equal(percentile([5, 1, 4, 2, 3], 0.5), 3);
  assert.equal(percentile([5, 1, 4, 2, 3], 0.95), 5);
  assert.equal(percentile([42], 0.95), 42);
  assert.equal(percentile([], 0.5), null);
  assert.deepEqual(summarizeLatencies([]), { count: 0, p50: null, p95: null, min: null, max: null });
  const summary = summarizeLatencies([10, 20, 30, 40, 50]);
  assert.deepEqual(summary, { count: 5, p50: 30, p95: 50, min: 10, max: 50 });
});

test('the fixture is deterministic and covers the plan’s 120-day / 12-model / 3-provider / 100-account / 10-key distribution', () => {
  const nowMs = Date.UTC(2026, 10, 6, 12, 0, 0);
  const first = fixtureRow(7, { nowMs });
  assert.deepEqual(first, fixtureRow(7, { nowMs }));
  assert.equal(first.createdAtMs, first.timestampMs, 'rollups bucket by created_at_ms; it must carry the spread');

  const summary = fixtureSummary({ nowMs, count: 1200 });
  assert.equal(summary.uniqueHashes, 1200);
  assert.equal(summary.models, 12);
  assert.equal(summary.providers, 3);
  assert.equal(summary.accounts, 100);
  assert.equal(summary.apiKeys, 10);
  assert.ok(summary.minTimestampMs >= nowMs - 120 * DAY_MS);
  assert.ok(summary.maxTimestampMs < nowMs);
  assert.ok(summary.maxTimestampMs - summary.minTimestampMs > 110 * DAY_MS, 'events must span the whole window');

  assert.equal(fixtureRow(0, { nowMs }).failed, 1);
  assert.equal(fixtureRow(1, { nowMs }).failed, 0);
  assert.throws(() => fixtureRow(-1, { nowMs }), /non-negative integer/);
  assert.throws(() => fixtureRow(0, { nowMs, count: 0 }), /positive integer/);
});

test('leaksIn reports every registered secret the text discloses', () => {
  assert.deepEqual(leaksIn('a b c', ['b', 'z']), ['b']);
  assert.deepEqual(leaksIn('', ['x']), []);
  assert.deepEqual(leaksIn('abc', []), []);
});

test('the fixture store drops the Fresh checkpoint and persists a bootable bootstrap state', (t) => {
  const directory = workspace(t, 'fixture-store');
  const dbPath = path.join(directory, 'usage.sqlite');
  const db = new DatabaseSync(dbPath);
  db.exec(`create table settings(key text primary key, value text, updated_at_ms integer)`);
  db.exec(`create table usage_events(
    id integer primary key autoincrement, request_id text, event_hash text not null unique,
    timestamp_ms integer not null, timestamp text not null, provider text, executor_type text,
    model text not null, endpoint text, method text, path text, client_ip text, x_forwarded_for text,
    user_agent text, auth_type text, auth_index text, source text, source_hash text, api_key_hash text,
    account_snapshot text, auth_label_snapshot text, auth_file_snapshot text, auth_provider_snapshot text,
    auth_account_id_snapshot text, auth_project_id_snapshot text, auth_snapshot_at_ms integer,
    requested_model text, resolved_model text, reasoning_effort text, service_tier text,
    request_service_tier text, response_service_tier text, cache_input_mode text,
    input_tokens integer not null default 0, output_tokens integer not null default 0,
    reasoning_tokens integer not null default 0, cached_tokens integer not null default 0,
    cache_tokens integer not null default 0, cache_read_tokens integer not null default 0,
    cache_creation_tokens integer not null default 0, normalized_uncached_input_tokens integer,
    normalized_total_input_tokens integer, normalized_cache_read_tokens integer,
    normalized_cache_creation_tokens integer, total_tokens integer not null default 0,
    latency_ms integer, ttft_ms integer, failed integer not null default 0, fail_status_code integer,
    fail_summary text, response_metadata_json text, header_quota_recover_at_ms integer,
    header_quota_used_percent real, header_quota_plan_type text, header_error_kind text,
    header_error_code text, header_trace_id text, fail_body text, raw_json text, response_model text,
    created_at_ms integer not null default 0)`);
  db.exec(`insert into settings values
    ('fresh_operation_v1','{}',0),
    ('fresh_authority_v1','{}',0),
    ('usage_monitoring_model_format_version','1',0)`);
  db.close();
  fs.writeFileSync(`${dbPath}.manager.lock`, 'fresh marker');

  completeFixtureStore(dbPath, { hasHistoricalData: true });
  assert.equal(fs.existsSync(`${dbPath}.manager.lock`), false, 'the Fresh ownership marker is retired');

  const check = new DatabaseSync(dbPath);
  const keys = check.prepare('select key from settings order by key').all().map((row) => row.key);
  assert.deepEqual(keys, ['bootstrap_state_v1', 'usage_monitoring_model_format_version']);
  const state = JSON.parse(check.prepare(`select value from settings where key='bootstrap_state_v1'`).get().value);
  assert.equal(state.adminReady, true);
  assert.equal(state.hasHistoricalData, true);
  assert.equal(state.version, 1);
  check.close();

  injectUnsupportedDerivedFormat(dbPath, 'future-PRIVATE-SCHEMA-CONTENT');
  const injected = new DatabaseSync(dbPath);
  assert.equal(
    injected.prepare(`select value from settings where key='usage_monitoring_model_format_version'`).get().value,
    'future-PRIVATE-SCHEMA-CONTENT'
  );
  injected.close();
});

test('seeding writes the deterministic events in one settled commit', (t) => {
  const directory = workspace(t, 'seed');
  const dbPath = path.join(directory, 'usage.sqlite');
  const db = new DatabaseSync(dbPath);
  db.exec(`create table usage_events(
    id integer primary key autoincrement, request_id text, event_hash text not null unique,
    timestamp_ms integer not null, timestamp text not null, provider text, executor_type text,
    model text not null, endpoint text, method text, path text, client_ip text, x_forwarded_for text,
    user_agent text, auth_type text, auth_index text, source text, source_hash text, api_key_hash text,
    account_snapshot text, auth_label_snapshot text, auth_file_snapshot text, auth_provider_snapshot text,
    auth_account_id_snapshot text, auth_project_id_snapshot text, auth_snapshot_at_ms integer,
    requested_model text, resolved_model text, reasoning_effort text, service_tier text,
    request_service_tier text, response_service_tier text, cache_input_mode text,
    input_tokens integer not null default 0, output_tokens integer not null default 0,
    reasoning_tokens integer not null default 0, cached_tokens integer not null default 0,
    cache_tokens integer not null default 0, cache_read_tokens integer not null default 0,
    cache_creation_tokens integer not null default 0, normalized_uncached_input_tokens integer,
    normalized_total_input_tokens integer, normalized_cache_read_tokens integer,
    normalized_cache_creation_tokens integer, total_tokens integer not null default 0,
    latency_ms integer, ttft_ms integer, failed integer not null default 0, fail_status_code integer,
    fail_summary text, response_metadata_json text, header_quota_recover_at_ms integer,
    header_quota_used_percent real, header_quota_plan_type text, header_error_kind text,
    header_error_code text, header_trace_id text, fail_body text, raw_json text, response_model text,
    created_at_ms integer not null default 0)`);
  db.close();

  const nowMs = Date.UTC(2026, 10, 6, 12, 0, 0);
  const summary = seedFixture(dbPath, { nowMs, count: 500 });
  assert.equal(summary.count, 500);

  const check = new DatabaseSync(dbPath);
  assert.equal(check.prepare('select count(*) as c from usage_events').get().c, 500);
  const rows = check.prepare('select timestamp_ms, created_at_ms, event_hash, failed from usage_events order by id').all();
  assert.equal(rows[0].timestamp_ms, rows[0].created_at_ms);
  assert.equal(rows[0].event_hash, (1).toString(16).padStart(64, '0'));
  assert.equal(rows[0].failed, 1);
  assert.equal(rows[1].failed, 0);
  assert.equal(new Set(rows.map((row) => row.event_hash)).size, 500);
  check.close();
});

test('the resource registry releases everything it created, in release order, and refuses creation after the fence', async () => {
  const released = [];
  const registry = createResourceRegistry({ prefix: PREFIX });
  const makeRelease = (kind, name) => async () => {
    released.push(`${kind}:${name}`);
  };
  registry.track('volume', `${PREFIX}-data`, makeRelease('volume', `${PREFIX}-data`));
  registry.track('network', `${PREFIX}-net`, makeRelease('network', `${PREFIX}-net`));
  registry.track('container', `${PREFIX}-mgr`, makeRelease('container', `${PREFIX}-mgr`));

  assert.throws(() => registry.track('container', 'cpamp-foreign', async () => {}), /not one of this run's own resources/);
  assert.throws(() => registry.track('container', 'cpa-usage-keeper', async () => {}), /not one of this run's own resources/);

  const result = await registry.releaseAll();
  assert.equal(result.ok, true);
  assert.deepEqual(released, [`container:${PREFIX}-mgr`, `network:${PREFIX}-net`, `volume:${PREFIX}-data`]);

  registry.fence('aborted by SIGTERM');
  assert.throws(
    () => registry.track('container', `${PREFIX}-late`, async () => {}),
    /the run is shutting down/
  );

  // A release that fails stays visible and fails the cleanup, so a partial
  // cleanup can never be reported as a clean one.
  const failing = createResourceRegistry({ prefix: PREFIX });
  failing.track('container', `${PREFIX}-bad`, async () => {
    throw new Error('daemon unavailable');
  });
  const failed = await failing.releaseAll();
  assert.equal(failed.ok, false);
  assert.match(failed.errors[0], /daemon unavailable/);
});

test('a handled signal runs cleanup once and exits with the signal’s own code', async () => {
  const target = new EventEmitter();
  const exits = [];
  let cleanups = 0;
  const guard = installSignalGuard({
    target,
    exit: (code) => exits.push(code),
    onSignal: async (signal) => {
      cleanups += 1;
      assert.equal(signal, 'SIGINT');
    },
  });
  target.emit('SIGINT');
  target.emit('SIGINT');
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(cleanups, 1);
  assert.deepEqual(exits, [130]);
  assert.equal(guard.signal, 'SIGINT');
  guard.detach();
  assert.equal(target.listenerCount('SIGINT'), 0);
});

test('the verdict is pass only when the run and its cleanup both succeeded', () => {
  assert.equal(finalExitCode({ runOk: true, cleanupOk: true }), 0);
  assert.equal(finalExitCode({ runOk: true, cleanupOk: false }), 1);
  assert.equal(finalExitCode({ runOk: false, cleanupOk: true }), 1);
  assert.equal(finalExitCode({ runOk: true, cleanupOk: true, signal: 'SIGTERM' }), 143);
  assert.equal(finalExitCode({ runOk: true, cleanupOk: true, signal: 'SIGINT' }), 130);
});

test('only a positive not-found inspect may count as a released object', () => {
  assert.deepEqual(classifyDockerInspect({ status: 0, stdout: '[]' }), { state: 'present', error: null });
  assert.equal(
    classifyDockerInspect({ status: 1, stderr: 'Error: No such container: cpampd1a2b3c4d-a' }).state,
    'absent'
  );
  assert.equal(
    classifyDockerInspect({ status: 1, stderr: 'Error: No such volume: cpampd1a2b3c4d-v' }).state,
    'absent'
  );
  for (const failed of [
    { status: 1, stderr: 'Cannot connect to the Docker daemon at unix:///var/run/docker.sock' },
    { status: 1, stderr: 'permission denied while trying to connect to the Docker daemon socket' },
    { status: 137, stderr: '' },
    { status: null, stderr: '' },
  ]) {
    const classified = classifyDockerInspect(failed);
    assert.equal(classified.state, 'unknown', JSON.stringify(classified));
    assert.ok(classified.error.length > 0);
  }
});

test('the docker releaser never treats an unreadable inspect or a failed removal as released', () => {
  const removed = [];
  const states = new Map([
    ['cpampd1a2b3c4d-mgr', { state: 'present' }],
    ['cpampd1a2b3c4d-gone', { state: 'absent' }],
    ['cpampd1a2b3c4d-daemon', { state: 'unknown', error: 'Cannot connect to the Docker daemon' }],
    ['cpampd1a2b3c4d-stuck', { state: 'present' }],
  ]);
  const release = createDockerReleaser({
    inspect: (kind, name) => states.get(name) ?? { state: 'absent' },
    removeObject: (kind, name) => {
      removed.push(`${kind}:${name}`);
      return { status: name === 'cpampd1a2b3c4d-stuck' ? 1 : 0, stderr: 'volume is in use' };
    },
  });

  assert.deepEqual(release('container', 'cpampd1a2b3c4d-mgr'), {
    kind: 'container',
    name: 'cpampd1a2b3c4d-mgr',
    removed: true,
    present: true,
  });
  assert.deepEqual(release('container', 'cpampd1a2b3c4d-gone'), {
    kind: 'container',
    name: 'cpampd1a2b3c4d-gone',
    removed: false,
    present: false,
  });
  // A daemon that cannot answer must fail the release, never skip the object.
  assert.throws(() => release('container', 'cpampd1a2b3c4d-daemon'), /Cannot connect to the Docker daemon/);
  assert.throws(() => release('volume', 'cpampd1a2b3c4d-stuck'), /exited 1: volume is in use/);
  assert.deepEqual(removed, ['container:cpampd1a2b3c4d-mgr', 'volume:cpampd1a2b3c4d-stuck']);

  // An unregistered releaser must not silently succeed.
  assert.throws(() => createDockerReleaser({ inspect: null, removeObject: () => 0 }), /inspect/);
});

test('an unverifiable release makes the run exit non-zero instead of reporting a clean host', async () => {
  const releaser = (inspect) =>
    createDockerReleaser({ inspect, removeObject: () => ({ status: 0 }) });

  // A container the daemon says is gone needs no removal and leaves nothing.
  const goneRegistry = createResourceRegistry({ prefix: PREFIX });
  goneRegistry.track('container', `${PREFIX}-mgr`, () => {});
  const goneOutcome = await goneRegistry.releaseAll();
  assert.equal(goneOutcome.ok, true);

  // The same shape with an unreadable inspect: the entry stays in the registry,
  // the cleanup result is a failure, and the process cannot exit 0 even though
  // the run itself succeeded.
  const stuckRegistry = createResourceRegistry({ prefix: PREFIX });
  const stuckRelease = releaser(() => ({ state: 'unknown', error: 'Cannot connect to the Docker daemon' }));
  stuckRegistry.track('container', `${PREFIX}-mgr`, () => stuckRelease('container', `${PREFIX}-mgr`));
  const stuckOutcome = await stuckRegistry.releaseAll();
  assert.equal(stuckOutcome.ok, false);
  assert.match(stuckOutcome.errors.join(' '), /docker inspect container/);
  assert.equal(finalExitCode({ runOk: true, cleanupOk: stuckOutcome.ok }), 1);

  // A failed `docker rm` on a present container is the third failing shape.
  const stuckRm = createResourceRegistry({ prefix: PREFIX });
  const stuckRmRelease = createDockerReleaser({
    inspect: () => ({ state: 'present' }),
    removeObject: () => ({ status: 1, stderr: 'volume is in use' }),
  });
  stuckRm.track('volume', `${PREFIX}-bench`, () => stuckRmRelease('volume', `${PREFIX}-bench`));
  const stuckRmOutcome = await stuckRm.releaseAll();
  assert.equal(stuckRmOutcome.ok, false);
  assert.match(stuckRmOutcome.errors.join(' '), /volume is in use/);
  assert.equal(finalExitCode({ runOk: true, cleanupOk: stuckRmOutcome.ok }), 1);
});

test('the cleanup helper must be verified gone, not assumed', () => {
  assert.deepEqual(checkCleanupHelper({ name: `${PREFIX}-cleanup`, inspected: { state: 'absent' } }), {
    ok: true,
    name: `${PREFIX}-cleanup`,
  });
  for (const inspected of [
    { state: 'present' },
    { state: 'unknown', error: 'Cannot connect to the Docker daemon' },
    undefined,
  ]) {
    const checked = checkCleanupHelper({ name: `${PREFIX}-cleanup`, inspected });
    assert.equal(checked.ok, false, JSON.stringify(inspected));
    assert.match(checked.error, /cleanup helper .* still present/);
  }
});

test('a run that cannot confirm its container is gone stays tracked', () => {
  const registry = createResourceRegistry({ prefix: PREFIX });
  registry.track('container', `${PREFIX}-initialize-ab12cd`, () => {});

  // The daemon could not answer: the entry must survive so cleanup retries and
  // fails loudly instead of reporting a clean host.
  const unknown = settleEphemeralTracking({
    registry,
    name: `${PREFIX}-initialize-ab12cd`,
    inspected: { state: 'unknown', error: 'Cannot connect to the Docker daemon' },
  });
  assert.equal(unknown.untracked, false);
  assert.match(unknown.unverified, /Cannot connect/);
  assert.equal(registry.has('container', `${PREFIX}-initialize-ab12cd`), true);

  // A container that is still running is also kept, so the registry removes it.
  const present = settleEphemeralTracking({
    registry,
    name: `${PREFIX}-initialize-ab12cd`,
    inspected: { state: 'present' },
  });
  assert.equal(present.tracked, true);
  assert.equal(registry.has('container', `${PREFIX}-initialize-ab12cd`), true);

  // Only the positive "No such container" untracks it.
  const absent = settleEphemeralTracking({
    registry,
    name: `${PREFIX}-initialize-ab12cd`,
    inspected: { state: 'absent' },
  });
  assert.equal(absent.untracked, true);
  assert.equal(registry.has('container', `${PREFIX}-initialize-ab12cd`), false);

  // Another run's container is never touched by this registry.
  const foreign = createResourceRegistry({ prefix: PREFIX });
  assert.throws(() => foreign.track('container', 'cpampdffffffff-mgr', () => {}), /not one of this run/);
});

test('the run workspace remover reports a directory it could not remove', () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'pr3-workspace-'));
  try {
    const directory = path.join(root, 'cpampd1a2b3c4d');
    fs.mkdirSync(path.join(directory, 'nested'), { recursive: true });
    fs.writeFileSync(path.join(directory, 'nested', 'usage.sqlite'), 'x');

    const removed = removeRunWorkspace(directory);
    assert.equal(removed.ok, true);
    assert.equal(removed.method, 'fs');
    assert.equal(fs.existsSync(directory), false);

    // A host that cannot unlink the files falls back to the container remover.
    const sticky = path.join(root, 'cpampd1a2b3c4d-sticky');
    fs.mkdirSync(sticky);
    let fallbackCalls = 0;
    const fallback = removeRunWorkspace(sticky, {
      fsRemove: () => {
        throw new Error('EPERM');
      },
      fallbackRemove: () => {
        fallbackCalls += 1;
        fs.rmSync(sticky, { recursive: true, force: true });
      },
    });
    assert.equal(fallbackCalls, 1);
    assert.equal(fallback.ok, true);
    assert.equal(fallback.method, 'fallback');

    // If neither remover works the cleanup must fail instead of reporting a
    // clean host.
    const stuck = path.join(root, 'cpampd1a2b3c4d-stuck');
    fs.mkdirSync(stuck);
    const failed = removeRunWorkspace(stuck, {
      fsRemove: () => {
        throw new Error('EPERM');
      },
      fallbackRemove: () => {},
    });
    assert.equal(failed.ok, false);
    assert.match(failed.error, /run workspace remained/);
    // The failure message is archived in evidence: an operator's scratch layout
    // must not travel with it.
    assert.equal(failed.error.includes(root), false);
    assert.equal(failed.error.includes(os.tmpdir()), false);
    assert.match(failed.error, /^[^/]*$/);
    fs.rmSync(stuck, { recursive: true, force: true });
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
});

test('the settings envelope is the product format and never repeats a nonce', () => {
  const dataKey = Buffer.alloc(32, 7).toString('base64');
  const first = protectSettingValue(dataKey, 'pr3-management-key');
  const second = protectSettingValue(dataKey, 'pr3-management-key');

  assert.match(first, /^enc:v1:[A-Za-z0-9+/]+:[A-Za-z0-9+/]+$/);
  assert.notEqual(first, second, 'each envelope must carry a fresh nonce');
  const [nonce, ciphertext] = first.slice('enc:v1:'.length).split(':');
  assert.equal(Buffer.from(nonce, 'base64').length, 12);
  assert.ok(Buffer.from(ciphertext, 'base64').length > 16);
  assert.equal(first.includes('pr3-management-key'), false);
  assert.throws(() => protectSettingValue('', 'value'), /data key is empty/);
});

test('the licence status assertion is bound to the provenance digest and fails closed', () => {
  const inventory = JSON.stringify({
    schemaVersion: 1,
    status: 'VERIFIED_LICENSE_COLLECTION',
    role: 'manager',
    licenseCoverage: {
      manager: { originalTexts: 'complete-for-installed-dependencies', packages: 13 },
      'base-image': { originalTexts: 'available-packaged-notices-only', packages: 21 },
    },
  });
  const inventoryBytes = Buffer.from(inventory, 'utf8');
  const digest = `sha256:${createHash('sha256').update(inventoryBytes).digest('hex')}`;
  const provenance = JSON.stringify({
    schemaVersion: 1,
    role: 'manager',
    sourceCommit: '8935bce5ea7ce994185ee97acb8a4fdbd9cdd6cc',
    licenseInventorySHA256: digest,
  });

  const pass = verifyReleaseProvenance({
    role: 'manager',
    sourceCommit: '8935bce5ea7ce994185ee97acb8a4fdbd9cdd6cc',
    inventory,
    provenance,
    inventoryBytes,
  });
  assert.equal(pass.ok, true);
  assert.equal(pass.thirdPartyInventoryStatus, 'VERIFIED_LICENSE_COLLECTION');

  const wrongStatus = verifyReleaseProvenance({
    role: 'manager',
    sourceCommit: '8935bce5ea7ce994185ee97acb8a4fdbd9cdd6cc',
    inventory: inventory.replace('VERIFIED_LICENSE_COLLECTION', 'NOT_VERIFIED'),
    provenance,
    inventoryBytes,
  });
  assert.equal(wrongStatus.ok, false);
  assert.match(wrongStatus.failures.join(' '), /inventory_status:NOT_VERIFIED/);

  const wrongDigest = verifyReleaseProvenance({
    role: 'manager',
    sourceCommit: '8935bce5ea7ce994185ee97acb8a4fdbd9cdd6cc',
    inventory,
    provenance: provenance.replace(digest, 'sha256:' + '0'.repeat(64)),
    inventoryBytes,
  });
  assert.equal(wrongDigest.ok, false);
  assert.match(wrongDigest.failures.join(' '), /inventory_hash_mismatch/);

  const wrongRole = verifyReleaseProvenance({
    role: 'runtime',
    sourceCommit: '8935bce5ea7ce994185ee97acb8a4fdbd9cdd6cc',
    inventory,
    provenance,
    inventoryBytes,
  });
  assert.equal(wrongRole.ok, false);
  assert.match(wrongRole.failures.join(' '), /inventory_role:manager/);
});

test('the offline lock needs positive evidence and never infers release from a missing message', () => {
  const healthy = judgeOfflineLock({
    kind: 'unsupported_derived_format',
    exitCode: 0,
    output: '[derived-migration] creating index idx_usage_events_a',
  });
  assert.equal(healthy.ok, true);
  assert.equal(healthy.released, true);
  assert.equal(healthy.exitCode, 0);

  // The healthy store's command must actually finish.
  const healthyFailed = judgeOfflineLock({ kind: 'unsupported_derived_format', exitCode: 1, output: 'boom' });
  assert.equal(healthyFailed.ok, false);
  assert.match(healthyFailed.failures.join(' '), /offline_cleanup_failed:1/);

  // A lock refusal is a failure whatever the exit code says.
  const refused = judgeOfflineLock({
    kind: 'unsupported_derived_format',
    exitCode: 0,
    output: 'could not acquire offline cleanup lock: database is locked',
  });
  assert.equal(refused.ok, false);
  assert.equal(refused.released, false);
  assert.match(refused.failures.join(' '), /offline_lock_unavailable:0/);

  // Changed wording on a later release must not be read as "lock released".
  const renamed = judgeOfflineLock({
    kind: 'corrupted_database_bytes',
    exitCode: 1,
    output: 'refusing to run: the manager lock is held by process 42',
  });
  assert.equal(renamed.ok, false);
  assert.match(renamed.failures.join(' '), /offline_cleanup_unexpected_failure:1/);

  // The corrupted store must fail for its own reason, i.e. after it had the lock.
  const corrupt = judgeOfflineLock({
    kind: 'corrupted_database_bytes',
    exitCode: 1,
    output: 'cleanup-derived: open usage.sqlite: file is not a database (26)',
  });
  assert.equal(corrupt.ok, true);
  assert.equal(corrupt.released, true);
  assert.equal(corrupt.exitCode, 1);

  // An unexpected success on a corrupt file, an empty non-zero, an unobserved
  // status and an unknown scenario all fail.
  for (const [expected, input] of [
    [/offline_cleanup_unexpected_success/, { kind: 'corrupted_database_bytes', exitCode: 0, output: '' }],
    [/offline_cleanup_unexpected_failure:1/, { kind: 'corrupted_database_bytes', exitCode: 1, output: '' }],
    [/offline_status_unobserved/, { kind: 'unsupported_derived_format', exitCode: null, output: '' }],
    [/offline_status_unobserved/, { kind: 'unsupported_derived_format', output: '' }],
    [/offline_kind_unknown/, { kind: 'other', exitCode: 0, output: '' }],
  ]) {
    const judged = judgeOfflineLock(input);
    assert.equal(judged.ok, false, JSON.stringify(input));
    assert.equal(judged.released, false, JSON.stringify(input));
    assert.match(judged.failures.join(' '), expected);
  }
});

test('the per-query sampler validates every response, not only the last one', () => {
  const query = {
    name: 'monitoring_events_page',
    validate: (body) => body?.items?.length === 50,
  };
  const good = JSON.stringify({ items: new Array(50).fill({ id: 1 }) });
  const empty = JSON.stringify({ items: [] });

  // Four empty answers and one good one is a broken fixture. Validating only the
  // last response would report a clean run, so this sequence is the regression
  // for the "validate only sample 5" mutation.
  const fourBadOneGood = [empty, empty, empty, empty, good].map((body, index) => ({ ms: 10 + index, body }));
  const summarised = summarizeQuerySamples({ query, responses: fourBadOneGood });
  assert.equal(summarised.sampleCount, 5);
  assert.equal(summarised.summary.count, 5);
  assert.equal(summarised.validation.ok, false);
  assert.deepEqual(
    summarised.validationFailures,
    ['monitoring_events_page#1', 'monitoring_events_page#2', 'monitoring_events_page#3', 'monitoring_events_page#4']
  );
  assert.deepEqual(
    summarised.validation.failures.map((failure) => failure.sample),
    [1, 2, 3, 4]
  );

  // The mirror case: one bad answer at the end is also a failure.
  const fourGoodOneBad = [good, good, good, good, empty].map((body, index) => ({ ms: 10 + index, body }));
  assert.deepEqual(
    summarizeQuerySamples({ query, responses: fourGoodOneBad }).validationFailures,
    ['monitoring_events_page#5']
  );

  const allGood = summarizeQuerySamples({ query, responses: fourGoodOneBad.map((r) => ({ ...r, body: good })) });
  assert.equal(allGood.validation.ok, true);
  assert.equal(allGood.validationFailures.length, 0);
  assert.equal(allGood.summary.p50, 12);
  assert.equal(allGood.summary.p95, 14);

  // A query without a validator still contributes its latencies.
  const bare = summarizeQuerySamples({
    query: { name: 'noop' },
    responses: [{ ms: 4, body: 'not json' }],
  });
  assert.deepEqual(bare.validation, { ok: true, failures: [] });
  assert.equal(bare.summary.count, 1);

  // Unparsable bodies fail a query that expects JSON, rather than throwing.
  const unparsable = summarizeQuerySamples({ query, responses: [{ ms: 1, body: '<<html>>' }] });
  assert.equal(unparsable.validation.ok, false);
});

test('the fixture-exercised verdict covers every sample of both measured runs', () => {
  const run = (names, extra = {}) => ({
    samples: Object.fromEntries(names.map((name) => [name, { count: 5 }])),
    validationFailures: [],
    ...extra,
  });
  const all = ['dashboard_summary', 'monitoring_header_snapshots', 'monitoring_events_page'];
  const on = run(all);
  const off = run(all);

  // Same non-empty query set on both sides, five samples each.
  assert.equal(judgeFixtureExercised({ rollupOn: on, rollupOff: off }).ok, true);
  assert.equal(
    judgeFixtureExercised({ rollupOn: on, rollupOff: off, expectedQueries: all, expectedSampleCount: 5 }).ok,
    true
  );

  // One invalid response in the rollup-off run fails the whole check, even
  // though the rollup-on run was clean.
  const offFalse = run(all, { validationFailures: ['monitoring_events_page#3'] });
  const offVerdict = judgeFixtureExercised({ rollupOn: on, rollupOff: offFalse });
  assert.equal(offVerdict.ok, false);
  assert.deepEqual(offVerdict.failures, ['rollupOff:monitoring_events_page#3']);

  // Four empty responses and a fifth good one is a failure, not a pass.
  const early = run(all, { validationFailures: ['dashboard_summary#1', 'dashboard_summary#2'] });
  assert.equal(judgeFixtureExercised({ rollupOn: early, rollupOff: off }).ok, false);

  // A run that sampled nothing cannot support a claim either.
  assert.equal(judgeFixtureExercised({ rollupOn: on, rollupOff: run([]) }).ok, false);

  // A query measured in one configuration but not the other fails, as does a
  // set that is merely a subset.
  const missing = [all[0], all[1]];
  const offMissing = judgeFixtureExercised({ rollupOn: on, rollupOff: run(missing) });
  assert.equal(offMissing.ok, false);
  assert.match(offMissing.failures.join(' '), /query_sets_differ/);

  // Every configured query must appear in both runs.
  const abbreviated = judgeFixtureExercised({
    rollupOn: run(missing),
    rollupOff: run(missing),
    expectedQueries: all,
    expectedSampleCount: 5,
  });
  assert.equal(abbreviated.ok, false);
  assert.match(abbreviated.failures.join(' '), /queries_missing:monitoring_events_page/);

  // A run that kept only one response per query is not a five-sample run.
  const thin = {
    samples: Object.fromEntries(all.map((name) => [name, { count: 1 }])),
    validationFailures: [],
  };
  const thinVerdict = judgeFixtureExercised({
    rollupOn: thin,
    rollupOff: off,
    expectedQueries: all,
    expectedSampleCount: 5,
  });
  assert.equal(thinVerdict.ok, false);
  assert.match(thinVerdict.failures.join(' '), /dashboard_summary:sample_count:1/);
});

test('the secret-free mismatched key needs a healthy store, not merely silence', () => {
  assert.deepEqual(judgeSecretFreeMismatchedKey({ healthStatus: 200, leakedSecretCount: 0 }), {
    ok: true,
    failures: [],
  });

  // The scenario the review names: no leak, but the process never became
  // healthy — non-disclosure alone must not pass.
  const unhealthy = judgeSecretFreeMismatchedKey({ healthStatus: 503, leakedSecretCount: 0 });
  assert.equal(unhealthy.ok, false);
  assert.deepEqual(unhealthy.failures, ['health_status:503']);

  const leaking = judgeSecretFreeMismatchedKey({ healthStatus: 200, leakedSecretCount: 1 });
  assert.equal(leaking.ok, false);
  assert.deepEqual(leaking.failures, ['secret_leak']);

  const both = judgeSecretFreeMismatchedKey({ healthStatus: 0, leakedSecretCount: 2 });
  assert.equal(both.ok, false);
  assert.deepEqual(both.failures, ['health_status:0', 'secret_leak']);

  // An unobserved status is a failure too, not a pass by omission.
  assert.equal(judgeSecretFreeMismatchedKey({}).ok, false);
});

test('every container this harness starts carries the same resource budget', () => {
  const limits = READINESS_GATE_BC_CONTRACT.limits;
  assert.deepEqual(limits, { cpus: 2, memory: '4g', memorySwap: '4g' });

  const specs = {
    manager: dockerRunArgs({
      name: `${PREFIX}-benchon`,
      image: 'sha256:' + 'a'.repeat(64),
      detach: true,
      mounts: ['/tmp/data:/data'],
      ports: ['127.0.0.1:19319:18317'],
      env: { HTTP_ADDR: '0.0.0.0:18317' },
    }),
    initialize: dockerRunArgs({
      name: `${PREFIX}-initialize-ab12cd`,
      image: 'sha256:' + 'a'.repeat(64),
      rm: true,
      network: 'none',
      mounts: ['/tmp/data:/data'],
    }),
    cleanupHelper: dockerRunArgs({
      name: `${PREFIX}-cleanup`,
      image: 'sha256:' + 'a'.repeat(64),
      rm: true,
      entrypoint: 'sh',
      mounts: ['/tmp:/w'],
    }),
  };

  for (const [label, args] of Object.entries(specs)) {
    const joined = args.join(' ');
    assert.ok(joined.includes('--cpus=2'), `${label} has no cpu budget`);
    assert.ok(joined.includes('--memory=4g'), `${label} has no memory budget`);
    assert.ok(joined.includes('--memory-swap=4g'), `${label} has no swap budget`);
    assert.ok(joined.includes('--pull=never'), `${label} could pull`);
    assert.ok(joined.includes('--platform linux/amd64'), `${label} is not amd64`);
  }
  assert.ok(specs.manager.includes('-d'));
  assert.ok(specs.initialize.includes('--rm'));
  assert.ok(specs.cleanupHelper.includes('--entrypoint'));
  assert.throws(() => dockerRunArgs({ name: `${PREFIX}-x` }), /image/);
});

test('a started container must run the pre-checked image id', () => {
  const id = 'sha256:' + 'b'.repeat(64);
  assert.deepEqual(verifyStartedImage({ name: 'cpampd1a2b3c4d-benchon', expected: id, actual: id }), {
    name: 'cpampd1a2b3c4d-benchon',
    image: id,
  });
  assert.throws(
    () => verifyStartedImage({ name: 'cpampd1a2b3c4d-benchon', expected: id, actual: 'sha256:' + 'c'.repeat(64) }),
    /pre-checked/
  );
  assert.throws(() => verifyStartedImage({ name: 'cpampd1a2b3c4d-benchon', expected: null, actual: id }), /no pre-checked/);
});

test('evidence redaction turns the run workspace into a placeholder', () => {
  const redactor = createRedactor([]);
  const workDir = '/tmp/pr3-work';
  const runWorkDir = path.join(workDir, PREFIX);
  for (const hostPath of [runWorkDir, workDir]) redactor.add(hostPath);

  const document = JSON.stringify({
    scenario: { dataDir: path.join(runWorkDir, 'benchmark', 'usage.sqlite') },
    error: `could not inspect ${runWorkDir}: EPERM`,
  });
  const redacted = redactor.redact(document);
  assert.equal(redacted.includes(workDir), false);
  assert.equal(redacted.includes(os.tmpdir()), false);
  assert.match(redacted, /\[redacted\]/);
});

test('a removal counts only on exit zero or a definite not-found, and never over a denial', () => {
  assert.deepEqual(classifyRemoveResult({ status: 0 }), { ok: true, absent: false, error: null });
  const gone = classifyRemoveResult({
    status: 1,
    stderr: `Error response from daemon: No such container: ${PREFIX}-cleanup`,
  });
  assert.equal(gone.ok, true);
  assert.equal(gone.absent, true);
  assert.equal(classifyRemoveResult({ status: 1, stderr: `No such volume: ${PREFIX}-bench` }).absent, true);

  for (const stderr of [
    'permission denied while trying to connect to the Docker daemon socket at unix:///var/run/docker.sock',
    'Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?',
    'Error response from daemon: operation not permitted',
  ]) {
    const removal = classifyRemoveResult({ status: 1, stderr });
    assert.equal(removal.ok, false, stderr);
    assert.equal(removal.absent, false, stderr);
    assert.equal(removal.error.length > 0, true, stderr);
  }
  // A denial is not excused by a not-found phrase in the same message.
  assert.equal(
    classifyRemoveResult({ status: 1, stderr: 'permission denied; No such container: x' }).ok,
    false
  );
  const unparseable = classifyRemoveResult({ status: null });
  assert.equal(unparseable.ok, false);
  assert.match(unparseable.error, /exited null/);
});

test('a cleanup helper whose own removal failed fails the run even when the container is gone', () => {
  const name = `${PREFIX}-cleanup`;
  const denied = judgeCleanupHelper({
    name,
    runResult: { status: 0 },
    removeResult: { status: 1, stderr: 'permission denied while trying to connect to the Docker daemon' },
    inspected: { state: 'absent' },
  });
  assert.equal(denied.ok, false);
  assert.match(denied.error, /cleanup helper remove failed/);
  assert.equal(finalExitCode({ runOk: true, cleanupOk: denied.ok }), 1);

  const unreachable = judgeCleanupHelper({
    name,
    runResult: { status: 0 },
    removeResult: { status: 1, stderr: 'Cannot connect to the Docker daemon at unix:///var/run/docker.sock' },
    inspected: { state: 'absent' },
  });
  assert.equal(unreachable.ok, false);
  assert.equal(finalExitCode({ runOk: true, cleanupOk: unreachable.ok }), 1);

  const alreadyGone = judgeCleanupHelper({
    name,
    runResult: { status: 0 },
    removeResult: { status: 1, stderr: `Error response from daemon: No such container: ${name}` },
    inspected: { state: 'absent' },
  });
  assert.deepEqual(alreadyGone, { ok: true, failures: [], error: '' });

  const removed = judgeCleanupHelper({
    name,
    runResult: { status: 0 },
    removeResult: { status: 0 },
    inspected: { state: 'absent' },
  });
  assert.equal(removed.ok, true);

  const stillThere = judgeCleanupHelper({
    name,
    runResult: { status: 0 },
    removeResult: { status: 0 },
    inspected: { state: 'present' },
  });
  assert.equal(stillThere.ok, false);
  assert.match(stillThere.error, /still present/);

  const helperFailed = judgeCleanupHelper({
    name,
    runResult: { status: 137, stderr: 'Killed' },
    removeResult: { status: 0 },
    inspected: { state: 'absent' },
  });
  assert.equal(helperFailed.ok, false);
  assert.match(helperFailed.error, /helper run exited 137/);
});

test('concurrent cleanup callers share one release and a failed cleanup is never a pass', async () => {
  let calls = 0;
  const coordinator = createCleanupCoordinator({
    cleanupResources: async () => {
      calls += 1;
      await new Promise((resolve) => setTimeout(resolve, 5));
      return { ok: true, released: ['container', 'volume'], errors: [] };
    },
  });
  assert.equal(coordinator.pending, false);
  const [first, second] = await Promise.all([coordinator.run(), coordinator.run()]);
  const third = await coordinator.abort({ signal: 'SIGTERM' });
  assert.equal(calls, 1);
  assert.equal(coordinator.pending, true);
  assert.equal(first, second);
  assert.deepEqual(third.outcome.released, ['container', 'volume']);
  assert.equal(third.exitCode, 143);

  const failing = createCleanupCoordinator({
    cleanupResources: async () => {
      throw new Error(`could not remove /Users/operator/scratch/${PREFIX}`);
    },
    redact: (value) => String(value).replace('/Users/operator/scratch/', '[redacted]/'),
  });
  const outcome = await failing.run();
  assert.equal(outcome.ok, false);
  assert.deepEqual(outcome.released, []);
  assert.equal(outcome.errors.length, 1);
  assert.match(outcome.errors[0], /cleanup threw/);
  assert.equal(outcome.errors[0].includes('/Users/operator'), false);
  assert.equal(finalExitCode({ runOk: true, cleanupOk: outcome.ok }), 1);
  assert.equal((await failing.abort({ runOk: true })).exitCode, 1);
});

test('a signal that lands during finalisation waits for the cleanup already in flight', async () => {
  const target = new EventEmitter();
  const exits = [];
  const released = [];
  let calls = 0;
  const coordinator = createCleanupCoordinator({
    cleanupResources: async () => {
      calls += 1;
      await new Promise((resolve) => setTimeout(resolve, 20));
      released.push(`${PREFIX}-manager`, `${PREFIX}-bench`);
      return { ok: true, released: [...released], errors: [] };
    },
  });
  const guard = installSignalGuard({
    target,
    exit: (code) => exits.push({ code, released: released.length }),
    onSignal: (signal) => coordinator.abort({ signal }),
  });

  const normal = coordinator.run();
  target.emit('SIGTERM');
  target.emit('SIGTERM');
  await normal;
  await new Promise((resolve) => setImmediate(resolve));

  assert.equal(calls, 1);
  assert.deepEqual(exits, [{ code: 143, released: 2 }]);
  assert.equal(guard.signal, 'SIGTERM');
  guard.detach();
  assert.equal(target.listenerCount('SIGTERM'), 0);
  assert.equal(target.listenerCount('SIGINT'), 0);
});

test('a fatal line carries the message and no stack or absolute path', () => {
  const redactor = createRedactor([]);
  const output = '/Users/operator/scratch/evidence';
  redactor.add(output);
  const error = new Error(`EACCES: permission denied, mkdir '${output}/run-1'`);
  const line = describeFatal(error, redactor);
  assert.match(line, /^readiness-gate-bc: EACCES: permission denied/);
  assert.equal(line.includes('/Users'), false);
  assert.equal(line.includes(output), false);
  assert.match(line, /\[redacted(-path)?\]/);
  assert.equal(line.includes('\n'), false);

  // Without a redactor the absolute path still must not be printed.
  const bare = describeFatal(new Error('spawn docker ENOENT at /Users/operator/bin/docker'));
  assert.equal(bare.includes('/Users/operator'), false);
  assert.match(bare, /\[redacted-path\]/);

  // Single-segment paths and paths with spaces are host paths too, and the error
  // code stays readable in front of the redacted location.
  for (const [message, code] of [
    [`EACCES: permission denied, mkdir '/restricted'`, 'EACCES'],
    [`ENOTDIR: not a directory, open '/tmp'`, 'ENOTDIR'],
    [`ENOSPC: no space left on device, write '/path with spaces/out'`, 'ENOSPC'],
  ]) {
    const line = describeFatal(new Error(message));
    assert.match(line, new RegExp(`^readiness-gate-bc: ${code}:`), message);
    assert.equal(line.includes('/restricted'), false, message);
    assert.equal(line.includes('/tmp'), false, message);
    assert.equal(line.includes('/path with spaces'), false, message);
    assert.equal(line.includes('spaces/out'), false, message);
    assert.match(line, /\[redacted-path\]/, message);
  }

  const named = describeFatal(new TypeError('bad argument'), redactor);
  assert.match(named, /^readiness-gate-bc: TypeError: bad argument$/);
});

test('evidence is published as one unit: both copies or neither', async (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'gate-bc-evidence-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const redactor = createRedactor([]);
  for (const hostPath of [root]) redactor.add(hostPath);
  const output = path.join(root, 'run');
  const repoEvidenceDir = path.join(root, 'docs', 'evidence');

  const document = { schema_version: 1, verdict: 'pass', images: { manager: { id: 'sha256:abc' } } };
  const written = await writeEvidence({ document, redactor, output, repoEvidenceDir, runId: 'deadbeef', root, pid: 1 });
  assert.equal(written.ok, true, written.error);
  assert.equal(written.aborted, false);
  const runCopy = fs.readFileSync(written.runFile, 'utf8');
  const repoCopy = fs.readFileSync(written.repoFile, 'utf8');
  assert.equal(repoCopy, runCopy);
  assert.equal(runCopy.includes(root), false);
  assert.equal(JSON.parse(repoCopy).verdict, 'pass');
  assert.equal(JSON.parse(repoCopy).evidenceFiles.runCopy, 'readiness-gate-bc-deadbeef.json');
  // No temp file survives a successful publish.
  for (const entry of [...fs.readdirSync(output), ...fs.readdirSync(repoEvidenceDir)]) {
    assert.equal(/\.tmp-/.test(entry), false, entry);
  }
});

test('a write that fails after creating bytes leaves neither copy and no temp file', async (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'gate-bc-evidence-fail-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const redactor = createRedactor([]);
  redactor.add(root);

  // Each stage fails after it has really put bytes on disk, which is the case the
  // earlier "throw before touching the file" test could not reach.
  const stages = ['runTemp', 'repoTemp', 'runRename', 'repoRename'];
  for (const stage of stages) {
    const caseRoot = path.join(root, stage);
    const output = path.join(caseRoot, 'run');
    const repoEvidenceDir = path.join(caseRoot, 'repo');
    const runId = 'cafebabe';
    const runFile = path.join(output, `readiness-gate-bc-${runId}.json`);
    const repoFile = path.join(repoEvidenceDir, `readiness-gate-bc-${runId}.json`);
    let writes = 0;
    let renames = 0;
    const result = await writeEvidence({
      document: { verdict: 'pass' },
      redactor,
      output,
      repoEvidenceDir,
      runId,
      root: caseRoot,
      pid: 1,
      writeFile: async (file, data) => {
        await fs.promises.writeFile(file, data);
        writes += 1;
        const failing = stage === 'runTemp' ? 1 : stage === 'repoTemp' ? 2 : 0;
        if (writes === failing) throw new Error(`ENOSPC: no space left on device, write '${file}'`);
      },
      rename: async (from, to) => {
        await fs.promises.rename(from, to);
        renames += 1;
        const failing = stage === 'runRename' ? 1 : stage === 'repoRename' ? 2 : 0;
        if (renames === failing) throw new Error(`EIO: i/o error, rename '${from}'`);
      },
    });
    assert.equal(result.ok, false, stage);
    assert.equal(result.aborted, false, stage);
    assert.match(result.error, /ENOSPC|EIO/, stage);
    assert.equal(result.error.includes(caseRoot), false, stage);
    const left = [runFile, repoFile, `${runFile}.tmp-1`, `${repoFile}.tmp-1`].filter((file) =>
      fs.existsSync(file)
    );
    assert.deepEqual(left, [], `${stage} left ${left.join(', ')}`);
    const listed = [
      ...(fs.existsSync(output) ? fs.readdirSync(output) : []),
      ...(fs.existsSync(repoEvidenceDir) ? fs.readdirSync(repoEvidenceDir) : []),
    ];
    assert.deepEqual(listed, [], `${stage} left ${listed.join(', ')}`);
  }
});

test('the archive yields to the event loop before its first write', async (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'gate-bc-evidence-yield-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const redactor = createRedactor([]);
  redactor.add(root);
  const output = path.join(root, 'run');
  const repoEvidenceDir = path.join(root, 'repo');

  // A signal listener is a macrotask. If the archive only yielded microtasks, this
  // callback would not run before the first write and the archive would publish a
  // PASS document after the process had already been asked to stop.
  let signal = null;
  setImmediate(() => {
    signal = 'SIGTERM';
  });
  // No real I/O: those calls would yield to the loop by themselves and hide the
  // question this test asks. With instantly-resolving writes, only the archive's
  // own yields can let the queued macrotask run.
  const result = await writeEvidence({
    document: { verdict: 'pass' },
    redactor,
    output,
    repoEvidenceDir,
    runId: '0ddba11',
    root,
    pid: 1,
    writeFile: async () => {},
    rename: async () => {},
    interrupted: () => signal,
  });
  assert.equal(result.ok, false);
  assert.equal(result.aborted, true);
  assert.deepEqual(fs.readdirSync(output), []);
  assert.deepEqual(fs.readdirSync(repoEvidenceDir), []);
});

test('a signal during the archive removes every file it had created', async (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'gate-bc-evidence-signal-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const redactor = createRedactor([]);
  redactor.add(root);
  const output = path.join(root, 'run');
  const repoEvidenceDir = path.join(root, 'repo');
  const runFile = path.join(output, 'readiness-gate-bc-feedface.json');
  const repoFile = path.join(repoEvidenceDir, 'readiness-gate-bc-feedface.json');

  let checks = 0;
  const result = await writeEvidence({
    document: { verdict: 'pass' },
    redactor,
    output,
    repoEvidenceDir,
    runId: 'feedface',
    root,
    pid: 1,
    // The first check passes; the signal is seen once the run copy is on disk.
    interrupted: () => {
      checks += 1;
      return checks >= 3 ? 'SIGTERM' : null;
    },
  });
  assert.equal(result.ok, false);
  assert.equal(result.aborted, true);
  assert.match(result.error, /interrupted by SIGTERM/);
  assert.equal(fs.existsSync(runFile), false);
  assert.equal(fs.existsSync(repoFile), false);
  assert.deepEqual(fs.readdirSync(output), []);
  assert.deepEqual(fs.readdirSync(repoEvidenceDir), []);
});

// A real signal, in a real process: the in-process test above can only prove the
// decision, not that the event loop gets a turn to deliver the signal before the
// guard is detached. The child wires the same way the harness does — coordinator,
// signal guard, awaitable archive — and the parent signals it while the archive
// is provably in flight (after the run temp file is on disk).
function runSignalledArchive(t, signalName) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), `gate-bc-signal-${signalName}-`));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const output = path.join(root, 'run');
  const repoEvidenceDir = path.join(root, 'repo');
  const runId = 'feebdaed';
  const childPath = path.join(root, 'child.mjs');
  fs.writeFileSync(
    childPath,
    `import fs from 'node:fs';
import path from 'node:path';

const [harnessUrl, output, repoDir, runId, signalName] = process.argv.slice(2);
const { createCleanupCoordinator, createRedactor, finalExitCode, installSignalGuard, writeEvidence } =
  await import(harnessUrl);

let signal = null;
const seen = { value: false };
process.on(signalName, () => {
  signal = signalName;
  seen.value = true;
});

const redactor = createRedactor([]);
redactor.add(output);
redactor.add(repoDir);
const coordinator = createCleanupCoordinator({
  cleanupResources: async () => ({ ok: true, released: [], errors: [] }),
});
const guard = installSignalGuard({ onSignal: (pending) => coordinator.abort({ signal: pending }) });

const written = await coordinator.trackArchive(
  writeEvidence({
    document: { schema_version: 1, verdict: 'pass' },
    redactor,
    output,
    repoEvidenceDir: repoDir,
    runId,
    interrupted: () => signal,
    // Hold the archive in flight after the run copy exists, so the signal lands
    // in the middle of publishing rather than before it starts. The poll keeps
    // the event loop alive: a bare pending promise lets Node exit before the
    // signal is delivered.
    writeFile: async (file, data) => {
      await fs.promises.writeFile(file, data);
      process.stdout.write('WROTE ' + path.basename(file) + '\\n');
      await new Promise((resolve) => {
        const timer = setInterval(() => {
          if (seen.value) {
            clearInterval(timer);
            resolve();
          }
        }, 5);
      });
    },
  })
);
guard.detach();
process.exitCode = written.aborted ? finalExitCode({ runOk: false, signal: guard.signal }) : written.ok ? 0 : 1;
`
  );

  const child = spawn(
    process.execPath,
    [childPath, pathToFileURL(path.join(ROOT, 'bin/ci/readiness-gate-bc.mjs')).href, output, repoEvidenceDir, runId, signalName],
    { stdio: ['ignore', 'pipe', 'pipe'] }
  );
  t.after(() => child.kill('SIGKILL'));

  return new Promise((resolve, reject) => {
    let stdout = '';
    let stderr = '';
    let signalled = false;
    const timer = setTimeout(() => {
      child.kill('SIGKILL');
      reject(new Error(`child did not finish; stdout=${stdout} stderr=${stderr}`));
    }, 15_000);
    child.stdout.on('data', (chunk) => {
      stdout += String(chunk);
      if (!signalled && stdout.includes('WROTE')) {
        signalled = true;
        process.kill(child.pid, signalName);
      }
    });
    child.stderr.on('data', (chunk) => {
      stderr += String(chunk);
    });
    child.on('error', reject);
    child.on('exit', (code, bySignal) => {
      clearTimeout(timer);
      resolve({ code, bySignal, stdout, stderr });
    });
  }).then((result) => {
    const expected = signalName === 'SIGTERM' ? 143 : 130;
    assert.equal(result.code, expected, `${signalName}: ${JSON.stringify(result)}`);
    for (const file of [
      path.join(output, `readiness-gate-bc-${runId}.json`),
      path.join(repoEvidenceDir, `readiness-gate-bc-${runId}.json`),
      path.join(output, `readiness-gate-bc-${runId}.json.tmp-${child.pid}`),
      path.join(repoEvidenceDir, `readiness-gate-bc-${runId}.json.tmp-${child.pid}`),
    ]) {
      assert.equal(fs.existsSync(file), false, `${signalName} left ${file}`);
    }
    assert.deepEqual(fs.readdirSync(output), [], signalName);
    assert.deepEqual(fs.readdirSync(repoEvidenceDir), [], signalName);
  });
}

test('a real SIGTERM during the archive exits 143 with no file left behind', (t) => runSignalledArchive(t, 'SIGTERM'));

test('a real SIGINT during the archive exits 130 with no file left behind', (t) => runSignalledArchive(t, 'SIGINT'));

test('the write probe never removes a file it did not create', (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'gate-bc-probe-owner-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const occupied = path.join(root, '.readiness-gate-bc-11-abcd.probe');
  fs.writeFileSync(occupied, 'not ours');
  assert.throws(
    () => probeWritableDirectory(root, { pid: 11, token: 'abcd' }),
    (error) => error.code === 'EEXIST'
  );
  // The file that occupied the probe path is somebody else's: it survives intact.
  assert.equal(fs.readFileSync(occupied, 'utf8'), 'not ours');

  // A probe this call created is removed even when writing it fails, and a
  // neighbouring file is untouched.
  const neighbour = path.join(root, 'neighbour.json');
  fs.writeFileSync(neighbour, '{"keep":true}');
  assert.throws(
    () =>
      probeWritableDirectory(root, {
        pid: 12,
        token: 'ef01',
        writeSync: () => {
          throw Object.assign(new Error('ENOSPC: no space left on device'), { code: 'ENOSPC' });
        },
      }),
    (error) => error.code === 'ENOSPC'
  );
  assert.equal(fs.existsSync(path.join(root, '.readiness-gate-bc-12-ef01.probe')), false);
  assert.equal(fs.existsSync(occupied), true);
  assert.equal(fs.readFileSync(neighbour, 'utf8'), '{"keep":true}');
});

test('the run workspace is only created after both archive directories are proven writable', () => {
  const probes = [];
  const created = [];
  assert.throws(
    () =>
      prepareRunDirectories({
        output: '/restricted/out',
        repoEvidenceDir: '/restricted/repo',
        workDir: '/tmp/gate-bc-work',
        prefix: 'cpampd0badf00d',
        probe: (directory) => {
          probes.push(directory);
          if (directory === '/restricted/repo') {
            const error = new Error(`EACCES: permission denied, open '${directory}/.probe'`);
            error.code = 'EACCES';
            throw error;
          }
          return { ok: true };
        },
        mkdirSync: (directory) => created.push(directory),
      }),
    /EACCES/
  );
  // Both directories are probed, in order, and the workspace is never created:
  // no scratch directory, and therefore no Docker call, follows a failed probe.
  assert.deepEqual(probes, ['/restricted/out', '/restricted/repo']);
  assert.deepEqual(created, []);

  const ok = prepareRunDirectories({
    output: '/out',
    repoEvidenceDir: '/repo',
    workDir: '/tmp/gate-bc-work',
    prefix: 'cpampd0badf00d',
    probe: () => ({ ok: true }),
    mkdirSync: (directory) => created.push(directory),
  });
  assert.deepEqual(created, [path.join('/tmp/gate-bc-work', 'cpampd0badf00d')]);
  assert.equal(ok.runWorkDir, path.join('/tmp/gate-bc-work', 'cpampd0badf00d'));
});

test('the output preflight proves writability with a file it removes again', (t) => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'gate-bc-probe-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const target = path.join(root, 'evidence');
  assert.deepEqual(probeWritableDirectory(target, { pid: 7, token: 'abcd' }), { ok: true, directory: target });
  assert.deepEqual(fs.readdirSync(target), []);

  // mkdir succeeding is not writability: an existing directory that refuses the
  // probe file must stop the run before it creates a workspace or a container.
  const denied = path.join(root, 'denied');
  fs.mkdirSync(denied, { recursive: true });
  assert.throws(
    () =>
      probeWritableDirectory(denied, {
        pid: 7,
        token: 'abcd',
        openSync: () => {
          throw Object.assign(new Error(`EACCES: permission denied, open '${denied}/.probe'`), {
            code: 'EACCES',
          });
        },
      }),
    /EACCES/
  );
  assert.deepEqual(fs.readdirSync(denied), []);
});

test('the archived report cites evidence this harness build actually produced', () => {
  const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');
  const evidenceDir = path.join(root, 'docs', 'architecture', 'evidence', 'v2-beta-readiness');
  const report = fs.readFileSync(path.join(evidenceDir, 'gate-bc-readiness-report.md'), 'utf8');
  const digest = `sha256:${createHash('sha256')
    .update(fs.readFileSync(path.join(root, 'bin', 'ci', 'readiness-gate-bc.mjs')))
    .digest('hex')}`;

  const cited = new Set(
    [...report.matchAll(/readiness-gate-bc-([0-9a-f]{8})\.json/g)].map((match) => match[1])
  );
  assert.equal(cited.size > 0, true, 'the report must cite an archived evidence file');

  const archived = new Set(
    fs
      .readdirSync(evidenceDir)
      .filter((entry) => /^readiness-gate-bc-[0-9a-f]{8}\.json$/.test(entry))
  );
  for (const runId of cited) {
    const file = `readiness-gate-bc-${runId}.json`;
    assert.equal(archived.has(file), true, `the report cites ${file}, which is not archived`);
    const document = JSON.parse(fs.readFileSync(path.join(evidenceDir, file), 'utf8'));
    assert.equal(document.verdict, 'pass', `${file} is cited but not a pass`);
    assert.equal(
      document.harness.sha256,
      digest,
      `${file} was produced by a different build of the harness; re-run it and resync the report`
    );
  }
});
