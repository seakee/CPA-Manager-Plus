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
import { EventEmitter } from 'node:events';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { DatabaseSync } from 'node:sqlite';
import test from 'node:test';

import {
  READINESS_GATE_BC_CONTRACT,
  completeFixtureStore,
  createDockerReleaser,
  createResourceRegistry,
  finalExitCode,
  fixtureRow,
  fixtureSummary,
  injectUnsupportedDerivedFormat,
  installSignalGuard,
  judgeDiagnostic,
  leaksIn,
  percentile,
  protectSettingValue,
  removeRunWorkspace,
  seedFixture,
  summarizeLatencies,
  verifyReleaseProvenance,
} from './readiness-gate-bc.mjs';

const DAY_MS = 86_400_000;
const PREFIX = 'cpampd1a2b3c4d';

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

test('the docker releaser throws when removal fails and skips objects that are already gone', () => {
  const removed = [];
  const release = createDockerReleaser({
    objectExists: (kind, name) => name !== 'cpampd1a2b3c4d-gone',
    removeObject: (kind, name) => {
      removed.push(`${kind}:${name}`);
      return { status: name === 'cpampd1a2b3c4d-stuck' ? 1 : 0, stderr: 'volume is in use' };
    },
  });

  assert.deepEqual(release('container', 'cpampd1a2b3c4d-mgr'), {
    kind: 'container',
    name: 'cpampd1a2b3c4d-mgr',
    removed: true,
  });
  assert.deepEqual(release('container', 'cpampd1a2b3c4d-gone'), {
    kind: 'container',
    name: 'cpampd1a2b3c4d-gone',
    removed: false,
  });
  assert.deepEqual(removed, ['container:cpampd1a2b3c4d-mgr']);
  assert.throws(() => release('volume', 'cpampd1a2b3c4d-stuck'), /exited 1: volume is in use/);

  // An unregistered kind must not silently succeed.
  assert.throws(() => createDockerReleaser({ objectExists: null, removeObject: () => 0 }), /objectExists/);
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
