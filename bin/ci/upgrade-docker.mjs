#!/usr/bin/env node
// Real-Docker acceptance for the v2 Upgrade chain (U1-8).
//
// What it proves, against a real 1.x release image, a real seeded data volume
// and the real v2 binary:
//
//   * preflight, copy-in, reconciliation and the persisted status, end to end;
//   * the two read-only Upgrade wizard routes, on a real Fresh installation,
//     with the 1.x administrator key and with no secret in the body;
//   * five isolated fault cases, each asserted by exit code, typed code and an
//     unchanged target root;
//   * a rollback recorded against a real 1.x stack that wrote after the copy;
//   * that the manager admits only the Upgrade surface while the operation is
//     unfinished — both wizard reads included, from the copied root alone —
//     that it serves the full surface and retires those reads once the operation
//     is complete, and that a probe answered by the manager itself is refused.
//
// Everything it creates is named `<prefix>-*` where the prefix is
// `cpampd<runid>`; nothing else on the host is stopped, removed or renamed, and
// the cleanup in `finally` only ever addresses that prefix.
//
// Usage:
//   node bin/ci/upgrade-docker.mjs [--reuse-fixture] [--keep] [--v1-tag v1.14.10]
//                                  [--source-tag v1.14.4] [--evidence <path>]
//
// `--reuse-fixture` skips `git worktree` + `docker build` and uses the recorded
// image, which is what a second run (or the U1-1b re-pin) does.
// `SKIP_UPGRADE_DOCKER=1` is the only way to skip the run, and it says so.

import { spawn, spawnSync } from 'node:child_process';
import crypto from 'node:crypto';
import fs from 'node:fs';
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

const args = parseArgs(process.argv.slice(2));
const runId = crypto.randomBytes(4).toString('hex');
const PREFIX = `cpampd${runId}`;
const SCRATCH = path.join(ROOT, 'bin', 'tmp', 'u1-8', runId);
const EVIDENCE_DIR = path.join(ROOT, 'bin', 'ci', 'evidence', 'upgrade-d');
const IMAGE_REPOSITORY = 'cpamp-d-fixture-v1';
// Rollback pins a reference that names a repository, so the fixture is also
// tagged this way; the bytes are the same image.
const ROLLBACK_IMAGE_REPOSITORY = 'cpampd-fixture/upgrade';

const evidence = {
  contract: 'cpamp.upgrade-docker.v1',
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
const created = { containers: new Set(), volumes: new Set(), worktrees: new Set(), networks: new Set() };
const hostManagers = new Map();
let evidenceFile = null;

// ---------------------------------------------------------------------------
// entry point
// ---------------------------------------------------------------------------

main().then(
  (result) => {
    console.log(result.exitCode === 0 ? 'upgrade-docker: PASS' : 'upgrade-docker: FAIL');
    process.exit(result.exitCode);
  },
  (error) => {
    console.error(`upgrade-docker: ${error?.message || error}`);
    if (error?.stack) console.error(error.stack);
    process.exit(1);
  },
);

async function main() {
  if (process.env.SKIP_UPGRADE_DOCKER === '1') {
    console.log('SKIP: SKIP_UPGRADE_DOCKER=1 was set explicitly; the real-Docker Upgrade acceptance did not run');
    return { exitCode: 0 };
  }
  started = Date.now();
  evidence.startedAt = new Date(started).toISOString();
  fs.mkdirSync(SCRATCH, { recursive: true });
  fs.mkdirSync(EVIDENCE_DIR, { recursive: true });

  const hostBefore = hostSnapshot();
  try {
    requireDocker();
    refusePrefixCollision();
    recordProductFixes();

    const binary = buildManagerBinary();
    const nextFixture = await ensureFixture(args.v1Tag);
    const nextSource = await seedSource(nextFixture);

    // Read the release the running contract accepts from the product itself, so
    // no second copy of the pinned version lives in this harness.
    const probe = runCli(binary, ['upgrade', 'preflight', '--legacy', nextSource.sourceRoot, '--json'], 'preflight(next-fixture)');
    const expected = expectedReleaseOf(probe) || args.sourceTag;
    if (!expected) {
      throw new Error('could not read the release the contract accepts from the preflight report');
    }
    evidence.mode.acceptedRelease = expected;

    let sourceFixture = nextFixture;
    let source = nextSource;
    if (expected !== args.v1Tag) {
      // The planned fixture is newer than what the contract accepts. The next
      // re-pin (U1-1b) is what closes this, so the harness records the typed
      // refusal and proves the chain against the accepted release instead of
      // pretending the planned one works.
      assertVersionGap(binary, probe, expected, nextFixture, nextSource);
      sourceFixture = await ensureFixture(expected);
      source = await seedSource(sourceFixture);
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

    const primary = chain(binary, workspace.main.source, workspace.main.target, 'main');
    evidence.chains.main = primary;

    await assertWizard(binary, workspace.main.source);
    await assertAdmission(binary, workspace);

    await faultSourceTouched(binary, workspace);
    await faultInterruptedCopyIn(binary, workspace);
    await faultClearedRollupRecoverable(binary, workspace);
    await faultClearedRollupUnrecoverable(binary, workspace);
    await faultDroppedTable(binary, workspace);
    await faultBusySource(binary, workspace, sourceFixture);

    await proveRollback(binary, workspace, sourceFixture);

    assertHostUntouched(hostBefore);
    return { exitCode: 0 };
  } finally {
    evidence.endedAt = new Date().toISOString();
    evidence.durationMs = Date.now() - started;
    cleanup();
    writeEvidence();
  }
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

function refusePrefixCollision() {
  for (const [kind, list] of [
    ['container', dockerJson(['ps', '-a', '--format', '{{.Names}}'])],
    ['volume', dockerJson(['volume', 'ls', '--format', '{{.Name}}'])],
  ]) {
    const collision = list.find((name) => name.startsWith(PREFIX));
    if (collision) {
      throw new Error(`the ${kind} ${collision} already uses this run's prefix ${PREFIX}; refusing to reuse an object this run did not create`);
    }
  }
}

function hostSnapshot() {
  return {
    containers: dockerJson(['ps', '-a', '--format', '{{.Names}}']).sort(),
    volumes: dockerJson(['volume', 'ls', '--format', '{{.Name}}']).sort(),
  };
}

// assertHostUntouched proves the harness only ever addressed its own prefix: the
// containers and volumes that existed before the run are exactly the ones that
// exist after it, and the ones it created are gone.
function assertHostUntouched(before) {
  const after = hostSnapshot();
  const outside = (list) => list.filter((name) => !name.startsWith(PREFIX));
  const containersBefore = outside(before.containers);
  const containersAfter = outside(after.containers);
  const volumesBefore = outside(before.volumes);
  const volumesAfter = outside(after.volumes);
  if (containersBefore.join('\n') !== containersAfter.join('\n')) {
    throw new Error(`containers outside ${PREFIX} changed: ${diff(containersBefore, containersAfter)}`);
  }
  if (volumesBefore.join('\n') !== volumesAfter.join('\n')) {
    throw new Error(`volumes outside ${PREFIX} changed: ${diff(volumesBefore, volumesAfter)}`);
  }
  evidence.hostUntouched = { containers: containersBefore.length, volumes: volumesBefore.length };
  stableFacts.push('host-untouched');
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
}

// ---------------------------------------------------------------------------
// fixtures
// ---------------------------------------------------------------------------

function buildManagerBinary() {
  const out = path.join(SCRATCH, 'cpa-manager-plus');
  const build = sh('go', ['build', '-o', out, './cmd/cpa-manager-plus'], { cwd: MANAGER_SERVER });
  if (build.code !== 0) {
    throw new Error(`build the v2 binary: ${build.stderr.trim()}`);
  }
  evidence.commands.push(commandRecord('go build ./cmd/cpa-manager-plus', build.code));
  return out;
}

// ensureFixture builds the 1.x image from the tag once and records its id and
// source commit, so the same fixture is reused instead of rebuilt per test.
async function ensureFixture(tag) {
  const version = tag.replace(/^v/, '');
  const image = `${IMAGE_REPOSITORY}:${version}`;
  // The rollback plan pins a published-style reference and refuses a name with
  // no repository part, so the same local bytes are also tagged that way.
  const rollbackImage = `${ROLLBACK_IMAGE_REPOSITORY}:${version}`;
  const known = imageId(image);
  if (args.reuseFixture && known) {
    if (!imageId(rollbackImage)) {
      const tagged = sh('docker', ['tag', image, rollbackImage]);
      assertValue(tagged.code === 0, `tag ${rollbackImage}: ${tagged.stderr.trim()}`);
    }
    return {
      tag,
      commit: git(['rev-parse', `${tag}^{commit}`]).stdout.trim(),
      image,
      rollbackImage,
      imageId: known,
      built: false,
    };
  }
  const source = path.join(SCRATCH, `fixture-${tag}`);
  fs.rmSync(source, { recursive: true, force: true });
  const add = git(['worktree', 'add', '--detach', source, tag]);
  created.worktrees.add(source);
  if (add.code !== 0) {
    throw new Error(`check out ${tag}: ${add.stderr.trim()}`);
  }
  const commit = git(['rev-parse', `${tag}^{commit}`]).stdout.trim();
  const build = sh(
    'docker',
    [
      'build',
      '-f', 'Dockerfile.manager-server',
      '-t', image,
      '--build-arg', `VERSION=${tag}`,
      '--build-arg', `SOURCE_COMMIT=${commit}`,
      '.',
    ],
    { cwd: source, timeout: 1_800_000 },
  );
  evidence.commands.push(commandRecord(`docker build ${image}`, build.code));
  if (build.code !== 0) {
    throw new Error(`build the ${tag} fixture image: ${tail(build.stderr)}`);
  }
  const tagged = sh('docker', ['tag', image, rollbackImage]);
  assertValue(tagged.code === 0, `tag ${rollbackImage}: ${tagged.stderr.trim()}`);
  return { tag, commit, image, rollbackImage, imageId: imageId(image), built: true };
}

// seedSource starts the fixture on an isolated port with a data directory this
// harness owns, seeds a realistic 1.x dataset through the 1.x API, snapshots the
// two states the fault cases need, and stops it cleanly.
async function seedSource(fixture) {
  const root = path.join(SCRATCH, `source-${fixture.tag}`);
  fs.rmSync(root, { recursive: true, force: true });
  fs.mkdirSync(root, { recursive: true });
  const port = freePort();
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
  assertValue(Boolean(runIdValue), `archive run id: ${JSON.stringify(run.body).slice(0, 200)}`);
  const archived = await v1Json(port, 'POST', `/v0/management/usage/archives/${runIdValue}/resume?expected_stage=archiving&wait=true`);
  assertValue(archived.body?.run?.status === 'archived', `archive run status: ${archived.body?.run?.status}`);

  const archivedSnapshot = path.join(SCRATCH, `source-archived-${fixture.tag}`);
  stopContainer(name);
  await waitForContainerExit(name);
  copyTree(root, archivedSnapshot);
  removeContainer(name);

  startContainer({ name, image: fixture.image, port, dataDir: root, adminKey: ADMIN_V1 });
  await waitForHealth(port, `${name} /health`);
  const verified = await v1Json(port, 'POST', `/v0/management/usage/archives/${runIdValue}/verify?wait=true`);
  assertValue(verified.body?.run?.status === 'verified', `archive verify status: ${verified.body?.run?.status}`);
  const deleted = await v1Json(port, 'POST', `/v0/management/usage/archives/${runIdValue}/delete?wait=true`);
  assertValue(deleted.body?.run?.status === 'completed', `archive delete status: ${deleted.body?.run?.status}`);

  const counts = v1Counts(port, events.length);
  stopContainer(name);
  await waitForContainerExit(name);
  removeContainer(name);

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

// chain runs the whole CLI chain and asserts each exit code and report.
function chain(binary, sourceRoot, targetRoot, label) {
  const preflight = runCli(binary, ['upgrade', 'preflight', '--legacy', sourceRoot, '--json'], `${label}:preflight`, { expectExit: 0 });
  const preflightReport = parseJson(preflight.stdout, `${label}:preflight`);
  assertValue(preflightReport.ready === true, `${label}: preflight ready=false (blocked=${JSON.stringify(preflightReport.blocked)})`);
  assertValue(preflightReport.checks.every((check) => check.status !== 'blocked'), `${label}: preflight has a blocked check`);

  const run = runCli(binary, ['upgrade', 'run', '--legacy', sourceRoot, '--target', targetRoot, '--json'], `${label}:run`, { expectExit: 0 });
  const runReport = parseJson(run.stdout, `${label}:run`);
  assertValue(runReport.result?.stage === 'copy_in', `${label}: run stage=${runReport.result?.stage}`);
  assertValue(runReport.result?.restored === true, `${label}: run restored=${runReport.result?.restored}`);

  const reconcile = runCli(binary, ['upgrade', 'reconcile', '--legacy', sourceRoot, '--target', targetRoot, '--json'], `${label}:reconcile`, { expectExit: 0 });
  const reconcileReport = parseJson(reconcile.stdout, `${label}:reconcile`);
  assertValue(reconcileReport.report?.stage === 'reconcile', `${label}: reconcile stage=${reconcileReport.report?.stage}`);
  assertValue((reconcileReport.report?.unacknowledgedDays ?? []).length === 0, `${label}: reconcile left unacknowledged days`);

  const status = runCli(binary, ['upgrade', 'status', '--db-path', path.join(targetRoot, 'usage.sqlite'), '--json'], `${label}:status`, { expectExit: 0 });
  const statusReport = parseJson(status.stdout, `${label}:status`);
  assertValue(statusReport.recorded === true, `${label}: status recorded=false`);
  assertValue(statusReport.operation?.stage === 'reconcile', `${label}: status stage=${statusReport.operation?.stage}`);

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
  assertValue(!/observed=/.test(versionCheck.detail) || versionCheck.detail.includes(`expected=${expected}`), `preflight of ${fixture.tag}: accepted release not named (${versionCheck.detail})`);

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
// configured source path, the harness's own paths or a full digest.
function assertNoSecretInBody(label, body, secrets) {
  const text = JSON.stringify(body);
  for (const secret of secrets) {
    if (secret && text.includes(secret)) {
      throw new Error(`${label} leaked ${JSON.stringify(String(secret).slice(0, 24))}: ${text.slice(0, 200)}`);
    }
  }
  const fullDigest = /[0-9a-f]{64}/.exec(text);
  if (fullDigest) {
    throw new Error(`${label} published a full digest (${fullDigest[0].slice(0, 12)}…): ${text.slice(0, 200)}`);
  }
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
    throw new Error(`initialize a Fresh installation: ${tail(init.stderr)}`);
  }
  const fragment = init.stdout.trim().split('\n').filter(Boolean).pop() ?? '';

  const port = freePort();
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
  assertValue(preflight.status === 200, `wizard preflight = ${preflight.status}: ${JSON.stringify(preflight.body).slice(0, 200)}`);
  assertValue(preflight.body?.contractVersion === 'cpamp.upgrade-wizard.v1', `wizard contract = ${preflight.body?.contractVersion}`);
  assertValue(preflight.body?.sourceKind === 'legacy-manager', `wizard source kind = ${preflight.body?.sourceKind}`);
  assertValue(preflight.body?.ready === true, `wizard preflight ready = ${preflight.body?.ready}`);
  assertValue(
    (preflight.body?.checks ?? []).some((check) => check.name === 'source_version' && check.status === 'pass'),
    'wizard preflight did not pass the source version check',
  );

  const status = httpJson('GET', `${base}/status`, { headers: { Authorization: `Bearer ${ADMIN_V1}` } });
  assertValue(status.status === 200, `wizard status = ${status.status}`);
  assertValue(status.body?.recorded === false, `wizard status recorded = ${status.body?.recorded}`);
  assertValue(status.body?.nextAction === 'preflight', `wizard status nextAction = ${status.body?.nextAction}`);

  // No secret in either body: the source administrator key, the data key
  // material, any absolute path, the full fingerprint or the v2 Admin key.
  const dataKey = fs.readFileSync(path.join(sourceRoot, 'data.key'), 'utf8').trim();
  for (const body of [preflight.body, status.body]) {
    assertNoSecretInBody('wizard', body, [ADMIN_V1, ADMIN_V2, dataKey, sourceRoot, SCRATCH, root]);
  }

  evidence.wizard = {
    fragment: `${fragment.split('#')[0]}#handoff=redacted`,
    port,
    contracts: {
      preflight: { exitCode: preflight.status, contractVersion: preflight.body?.contractVersion, ready: preflight.body?.ready, sourceKind: preflight.body?.sourceKind },
      status: { exitCode: status.status, recorded: status.body?.recorded, nextAction: status.body?.nextAction, stage: status.body?.stage },
    },
    credential: {
      sourceAdminKey: 'accepted',
      v2AdminKey: `refused (${wrongKey.status})`,
      anonymous: `refused (${unauthorized.status})`,
      write: `refused (${write.status})`,
    },
    noSecretInBody: true,
    bodySha256: { preflight: sha256(JSON.stringify(preflight.body)), status: sha256(JSON.stringify(status.body)) },
  };
  stableFacts.push(`wizard:${preflight.body.ready}:${status.body.nextAction}`);

  stopHostManager(name);
}

// ---------------------------------------------------------------------------
// admission: the Upgrade surface
// ---------------------------------------------------------------------------

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
    assertValue(proved.status === 200, `${label}: GET ${resource} with the source Admin key = ${proved.status}: ${JSON.stringify(proved.body).slice(0, 200)}`);
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
    assertValue(proved.body?.nextAction === expectedStage, `${label}: status nextAction = ${proved.body?.nextAction} want ${expectedStage}`);
    read.status = {
      status: proved.status,
      contractVersion: proved.body.contractVersion,
      recorded: proved.body.recorded,
      stage: proved.body.stage,
      nextAction: proved.body.nextAction,
    };
  }
  // Only the two exact leaves are mounted: a query string, a trailing slash, an
  // extra segment and another fixed-carrier resource answer 404 even to the key
  // the reads accept.
  const fixedAPI = `http://127.0.0.1:${port}/.well-known/cpamp-bootstrap/api/v2`;
  for (const shape of [`${base}`, `${base}/status?wait=1`, `${base}/status/`, `${base}/status/extra`, `${fixedAPI}/operation`]) {
    const probe = httpJson('GET', shape, { headers: { Authorization: `Bearer ${ADMIN_V1}` } });
    assertValue(probe.status === 404, `${label}: ${shape} = ${probe.status} want 404: only the two exact leaves are mounted`);
  }
  return {
    credential: { anonymous: 401, v2AdminKey: 401, write: 405, allow: 'GET' },
    pathShapes: 404,
    ...read,
  };
}

// proveUnfinishedAdmission starts the manager on a copied root whose recorded
// row is unfinished and proves the whole admission contract of that state: 503
// on /health, both reads answering 200 to the 1.x administrator key alone, and
// no canonical continuation, because the copy-in target carries no Fresh record.
async function proveUnfinishedAdmission(binary, root, sourceRoot, stage) {
  const port = freePort();
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
    stopHostManager(name);
  }
}

// proveCompletedAdmission proves the reads end with the operation. The recorded
// row is the terminal `complete` one, so admission serves the full writable
// surface — /health 200 — and both fixed-surface reads answer 404 to every
// caller, including the key they accepted a moment earlier.
async function proveCompletedAdmission(binary, completedRoot, sourceRoot) {
  const port = freePort();
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
    stopHostManager(name);
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

  // Completion is the terminal stage on the row the copy-in recorded. No
  // completion transition ships in this milestone (it is the operation's own
  // later steps), so the harness writes that stage onto the real row: the
  // evidence admission demands — the upgraded-legacy kind, both fingerprints and
  // the backup manifest digest — is the copy-in's own, and the stage token is
  // the only input the harness supplies.
  const completedRoot = path.join(SCRATCH, 'work', 'admission', 'complete');
  fs.rmSync(completedRoot, { recursive: true, force: true });
  copyTree(workspace.main.target, completedRoot);
  fs.rmSync(path.join(completedRoot, 'usage.sqlite.manager.lock'), { force: true });
  const completedDatabase = path.join(completedRoot, 'usage.sqlite');
  sqliteExec(completedDatabase, "update upgrade_operations set stage = 'complete' where id = 1");
  assertValue(
    sqliteCount(completedDatabase, "select count(*) as count from upgrade_operations where id = 1 and stage = 'complete'") === 1,
    'admission: the terminal stage did not land on the recorded row',
  );
  const completed = await proveCompletedAdmission(binary, completedRoot, sourceRoot);

  evidence.admission = {
    surface: 'upgrade-only',
    states: { copy_in: copyIn, reconcile },
    completed,
    completionInput: 'terminal stage written onto the row the real copy-in recorded; the evidence the admission demands is the copy-in\'s own',
  };
  stableFacts.push(`admission:copy_in:${copyIn.health}:${copyIn.status.status}:${copyIn.status.stage}:${copyIn.status.nextAction}:${copyIn.preflight.status}`);
  stableFacts.push(`admission:reconcile:${reconcile.health}:${reconcile.status.status}:${reconcile.status.stage}:${reconcile.status.nextAction}:${reconcile.preflight.status}`);
  stableFacts.push(`admission:complete:${completed.health}:${completed.status}:${completed.preflight}`);
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
    { expectExit: 1 },
  );
  const code = typedCode(prepare);
  assertValue(code === 'upgrade_source_changed', `touched: refusal code=${code}`);
  assertValue(dirDigest(target) === digestAfterTouch, 'touched: the refused rollback plan wrote the v2 data root');
  recordFault('source-touched-after-copy-in', prepare, code, true, { rerunExit: rerun.code, rerunStage: rerunReport.operation.stage });
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
    const child = spawn(binary, ['upgrade', 'run', '--legacy', source, '--target', target, '--json'], {
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    let killed = false;
    const timer = setInterval(() => {
      if (!killed && fs.existsSync(markerPath)) {
        killed = true;
        clearInterval(timer);
        child.kill('SIGKILL');
      }
    }, 10);
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
        rejectPromise(new Error(`interrupted copy-in: the run finished before the marker could be caught (code=${code} signal=${signal})`));
        return;
      }
      resolvePromise();
    });
  });

  assertValue(fs.existsSync(markerPath), 'interrupted: the provenance marker is missing');
  const status = runCli(binary, ['upgrade', 'status', '--db-path', path.join(target, 'usage.sqlite'), '--json'], 'interrupted:status', { expectExit: 0 });
  const statusReport = parseJson(status.stdout, 'interrupted:status');
  assertValue(statusReport.recorded === false, 'interrupted: an operation row was committed before the migration finished');

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
  });
  stableFacts.push('fault:interrupted:resumed');
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
  fs.mkdirSync(target, { recursive: true });
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
    assertValue(dirDigest(target) === digestBefore, 'busy: the blocked preflight wrote the v2 data root');
    recordFault('natively-busy-source', blocked, check.code, true, { fence: 'flock(LOCK_EX) on usage.sqlite.manager.lock' });
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

// proveRollback prepares a plan, applies it against a real 1.x stack on a real
// volume, lets that stack write, records the verdict, and proves the manager
// refuses a probe of its own listener.
async function proveRollback(binary, workspace, fixture) {
  const source = workspace.main.source;
  const target = workspace.main.target;
  const project = PREFIX;
  const port = freePort();
  const managerPort = freePort();

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
  assertValue(plan.volume === `${project}_cpa-manager-plus-data`, `rollback: plan volume=${plan.volume}`);
  assertValue(plan.port === port, `rollback: plan port=${plan.port}`);
  assertValue(plan.image === fixture.rollbackImage, `rollback: plan image=${plan.image}`);
  assertValue(typeof plan.composeFile === 'string' && plan.composeFile.includes(plan.volume), 'rollback: the plan carries no compose file');

  // The plan's volume must exist before compose is applied.
  const volume = plan.volume;
  const created2 = sh('docker', ['volume', 'create', volume]);
  assertValue(created2.code === 0, `rollback: docker volume create ${volume}: ${created2.stderr.trim()}`);
  created.volumes.add(volume);
  const seed = sh('docker', [
    'run', '--rm', '--name', `${PREFIX}-volume-seed`,
    '-v', `${source}:/from:ro`,
    '-v', `${volume}:/to`,
    'alpine:3.21', 'sh', '-c', 'cp -a /from/. /to/',
  ]);
  assertValue(seed.code === 0, `rollback: seed ${volume}: ${seed.stderr.trim()}`);

  // Apply the plan exactly as printed: the compose file on stdin.
  const up = sh('docker', ['compose', '-p', project, '-f', '-', 'up', '-d'], { input: plan.composeFile, timeout: 300_000 });
  evidence.commands.push(commandRecord(`docker compose -p ${project} -f - up -d`, up.code));
  if (up.code !== 0) {
    throw new Error(`rollback: apply the plan: ${tail(up.stderr)}`);
  }
  await waitForHealth(port, 'restarted 1.x /health');

  // The restarted 1.x writes after the copy-in: the recorded fingerprint no
  // longer describes the live data, which is exactly the state a rollback
  // verdict has to tolerate.
  const written = await v1JsonAt(port, 'POST', '/v0/management/usage/import', [
    {
      event_hash: sha256(Buffer.from('u1d:rollback:written-after-copy-in')),
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
  assertValue(written.body?.added === 1, `rollback: the restarted 1.x did not accept the new row: ${JSON.stringify(written.body)}`);

  // A probe answered by the manager itself is refused, and the refusal leaves
  // the recorded row untouched.
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
  stopHostManager(managerName);

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
  assertValue(Boolean(operation.rollback?.sourceFingerprint), 'rollback: no observed fingerprint was recorded');

  const status = runCli(binary, ['upgrade', 'status', '--db-path', path.join(target, 'usage.sqlite'), '--json'], 'rollback:status', { expectExit: 0 });
  assertValue(parseJson(status.stdout, 'rollback:status').operation?.stage === 'rolled_back', 'rollback: the persisted stage is not rolled_back');

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
      sourceFingerprint: plan.sourceFingerprint,
      reportSha256: sha256(prepare.stdout),
    },
    applied: { exitCode: up.code, health: 'ok' },
    v1WroteAfterCopyIn: { exitCode: 201, added: written.body.added },
    selfProbe: { exitCode: selfProbe.code, code: selfProbeCode },
    verify: {
      exitCode: verify.code,
      stage: operation.stage,
      image: operation.rollback?.image,
      statusUrl: operation.rollback?.statusUrl,
      observedFingerprint: operation.rollback?.sourceFingerprint,
      reportSha256: sha256(verify.stdout),
    },
    persistedStage: parseJson(status.stdout, 'rollback:status').operation?.stage,
  };
  stableFacts.push(`rollback:${operation.stage}:${operation.rollback?.image}`);

  stopCompose(project);
  await waitForPortClosed(port);
}

function planHealthURL(plan) {
  return `http://127.0.0.1:${plan.port}/health`;
}

// ---------------------------------------------------------------------------
// host processes
// ---------------------------------------------------------------------------

function startHostManager({ name, binary, dataDir, port, adminKey, legacyRoot }) {
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
}

function stopHostManager(name) {
  const entry = hostManagers.get(name);
  if (!entry) return;
  try {
    process.kill(-entry.child.pid, 'SIGTERM');
  } catch {
    try {
      entry.child.kill('SIGTERM');
    } catch {
      /* already gone */
    }
  }
  waitForPortClosed(entry.port);
  hostManagers.delete(name);
}

async function waitForPortClosed(port) {
  const deadline = Date.now() + 30_000;
  while (Date.now() < deadline) {
    if (!(await portOpen(port))) return;
    await sleep(100);
  }
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

function startContainer({ name, image, port, dataDir, adminKey }) {
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
    '-v', `${dataDir}:/data`,
    image,
  ]);
  evidence.commands.push(commandRecord(`docker run ${name}`, result.code));
  if (result.code !== 0) {
    throw new Error(`start ${name}: ${tail(result.stderr)}`);
  }
  created.containers.add(name);
}

function stopContainer(name) {
  const result = sh('docker', ['stop', '-t', '20', name]);
  evidence.commands.push(commandRecord(`docker stop ${name}`, result.code));
}

function removeContainer(name) {
  const result = sh('docker', ['rm', '-f', name]);
  created.containers.delete(name);
  if (result.code !== 0) {
    evidence.commands.push(commandRecord(`docker rm -f ${name}`, result.code, '', result.stderr));
  }
}

function stopCompose(project) {
  const result = sh('docker', ['compose', '-p', project, 'down', '-t', '20']);
  evidence.commands.push(commandRecord(`docker compose -p ${project} down`, result.code));
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

function runCli(binary, cliArgs, name, { expectExit = null, allowMissingBinary = false } = {}) {
  const result = sh(binary, cliArgs, { timeout: 600_000 });
  evidence.commands.push(commandRecord(name, result.code, result.stdout, result.stderr));
  if (result.code === null && !allowMissingBinary) {
    throw new Error(`${name}: the command did not run: ${result.stderr.trim()}`);
  }
  if (expectExit !== null && result.code !== expectExit) {
    throw new Error(`${name}: exit=${result.code} want ${expectExit}\n${tail(result.stderr)}${tail(result.stdout)}`);
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
    throw new Error(`${label}: the report is not JSON (${error.message}): ${tail(text, 400)}`);
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
  const parsed = { reuseFixture: false, keep: false, v1Tag: DEFAULT_V1_TAG, sourceTag: DEFAULT_SOURCE_TAG };
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
      case '--help':
      case '-h':
        console.log('usage: node bin/ci/upgrade-docker.mjs [--reuse-fixture] [--keep] [--v1-tag TAG] [--source-tag TAG]');
        process.exit(0);
        break;
      default:
        throw new Error(`unknown argument ${value}`);
    }
  }
  return parsed;
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

function dockerJson(commandArgs) {
  const result = sh('docker', commandArgs);
  if (result.code !== 0) {
    throw new Error(`docker ${commandArgs.join(' ')}: ${result.stderr.trim()}`);
  }
  return result.stdout.split('\n').map((line) => line.trim()).filter(Boolean);
}

function imageId(image) {
  const result = sh('docker', ['image', 'inspect', '-f', '{{.Id}}', image]);
  return result.code === 0 ? result.stdout.trim() : null;
}

function freePort() {
  // Sequential allocation keeps the reports stable; a port already in use is
  // skipped rather than reused.
  for (let attempt = 0; attempt < 200; attempt += 1) {
    const candidate = 18_900 + Math.floor(Math.random() * 900);
    const probe = spawnSync(
      process.execPath,
      ['-e', `require('net').createServer().listen(${candidate}, '127.0.0.1').on('listening', function () { this.close(); process.exit(0); }).on('error', () => process.exit(1));`],
      { encoding: 'utf8' },
    );
    if (probe.status === 0) return candidate;
  }
  throw new Error('no free port could be reserved');
}

function commandRecord(name, exitCode, stdout = '', stderr = '') {
  return {
    name,
    exitCode,
    stdoutSha256: stdout ? sha256(stdout) : null,
    stderrSha256: stderr ? sha256(stderr) : null,
    stderrTail: stderr ? tail(stderr, 400) : '',
  };
}

function tail(text, limit = 2_000) {
  const trimmed = String(text ?? '').trim();
  return trimmed.length <= limit ? trimmed : `…${trimmed.slice(-limit)}`;
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
    if (await predicate()) return;
    await sleep(interval);
  }
  throw new Error(`timed out waiting for ${label}`);
}

function assertValue(condition, message) {
  if (!condition) throw new Error(message);
}

function copyTree(from, to) {
  fs.rmSync(to, { recursive: true, force: true });
  fs.mkdirSync(path.dirname(to), { recursive: true });
  fs.cpSync(from, to, { recursive: true, force: true, preserveTimestamps: true });
}

// dirDigest is the content digest of a directory: every entry, its size and its
// file digest. It is what "the target root is unchanged" means for a fault case.
function dirDigest(root) {
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

function cleanup() {
  const result = { containers: [], volumes: [], worktrees: [], errors: [] };
  for (const name of hostManagers.keys()) {
    stopHostManager(name);
  }
  for (const name of created.containers) {
    if (!name.startsWith(PREFIX)) continue;
    const removal = sh('docker', ['rm', '-f', name]);
    result.containers.push({ name, exitCode: removal.code });
  }
  for (const volume of created.volumes) {
    if (!volume.startsWith(PREFIX)) continue;
    const removal = sh('docker', ['volume', 'rm', '-f', volume]);
    result.volumes.push({ volume, exitCode: removal.code });
  }
  for (const worktree of created.worktrees) {
    const removal = sh('git', ['worktree', 'remove', '--force', worktree]);
    if (removal.code === 0) sh('git', ['worktree', 'prune']);
    result.worktrees.push({ worktree, exitCode: removal.code });
  }
  // Nothing may survive this run under its own prefix.
  const leftovers = dockerJson(['ps', '-a', '--format', '{{.Names}}']).filter((name) => name.startsWith(PREFIX));
  const leftoverVolumes = dockerJson(['volume', 'ls', '--format', '{{.Name}}']).filter((name) => name.startsWith(PREFIX));
  result.leftovers = { containers: leftovers, volumes: leftoverVolumes };
  if (leftovers.length || leftoverVolumes.length) {
    result.errors.push('cleanup left objects behind');
  }
  if (!args.keep) {
    fs.rmSync(SCRATCH, { recursive: true, force: true });
  }
  evidence.cleanup = result;
  console.log(`cleanup: ${JSON.stringify(result)}`);
}

function writeEvidence() {
  evidence.stableDigest = sha256(stableFacts.join('|'));
  const body = JSON.stringify(evidence, null, 2);
  const document = { ...evidence, sha256: sha256(body) };
  const file = path.join(EVIDENCE_DIR, `${runId}.json`);
  fs.mkdirSync(EVIDENCE_DIR, { recursive: true });
  fs.writeFileSync(file, `${JSON.stringify(document, null, 2)}\n`);
  evidenceFile = file;
  console.log(`evidence: ${path.relative(ROOT, file)} (sha256 ${document.sha256})`);
  console.log(`stable digest: ${evidence.stableDigest}`);
}

// exported for the repository test that checks the harness contract
export const UPGRADE_DOCKER_CONTRACT = {
  prefixPattern: /^cpampd[0-9a-f]{8}$/,
  skipVariable: 'SKIP_UPGRADE_DOCKER',
  evidenceDirectory: path.join('bin', 'ci', 'evidence', 'upgrade-d'),
};

export const evidencePath = () => evidenceFile;
