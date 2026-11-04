// Behavioural tests for the parts of `upgrade-docker.mjs` that decide what the
// real-Docker harness may touch, what it releases, how it allocates ports, what
// it redacts, and what its evidence binds to. Everything here runs without
// Docker, without a container and without the product: the functions are driven
// through their own contracts with controlled stand-ins, so a regression in the
// cleanup registry, the signal path, the port allocator, the redactor or the
// evidence hash fails here instead of only on a host with Docker.
//
// Run with:
//   node --test bin/ci/upgrade-docker.test.mjs

import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { EventEmitter } from 'node:events';
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import test from 'node:test';
import vm from 'node:vm';

import {
  IMAGE_REPOSITORY,
  PORT_RANGE,
  RESOURCE_RELEASE_ORDER,
  UPGRADE_DOCKER_CONTRACT,
  assertImageWriteArgv,
  imageRepository,
  isOwnedImageAlias,
  rollbackAliasReference,
  shellTokens,
  withDeclaredSourceVersion,
  assertNoSecretInBody,
  canonicalEvidenceDocument,
  copyDirectory,
  evidenceDirectory,
  expectedNextAction,
  ownershipNormalizationArgv,
  createImplementationIdentity,
  createPortAllocator,
  createRedactor,
  createResourceRegistry,
  evidenceHash,
  finalExitCode,
  installSignalGuard,
  isOwnedResourceName,
  newCleanupResult,
  verifyEvidenceDocument,
  withEvidenceHash,
} from './upgrade-docker.mjs';

const PREFIX = 'cpampd01234567';
const HEAD = 'bde8aecd08f515f52b0346db992ce5a7d263d0bc';
const HARNESS_SHA = 'a'.repeat(64);

const evidenceDocument = (overrides = {}) =>
  withEvidenceHash({
    contract: 'cpamp.upgrade-docker.v1',
    verdict: 'pass',
    exitCode: 0,
    startedAt: '2026-11-02T00:00:00.000Z',
    implementation: {
      headSha: HEAD,
      harnessSha256: HARNESS_SHA,
      binarySha256: 'b'.repeat(64),
      treeClean: true,
      valid: true,
    },
    rollback: { writtenRowInLiveDirectory: 1 },
    ...overrides,
  });

// ---------------------------------------------------------------------------
// owned resources and their release
// ---------------------------------------------------------------------------

test('the registry only ever owns objects under this run prefix', () => {
  assert.equal(isOwnedResourceName(PREFIX, PREFIX), true);
  assert.equal(isOwnedResourceName(PREFIX, `${PREFIX}-seed-v11410`), true);
  assert.equal(isOwnedResourceName(PREFIX, `${PREFIX}_cpa-manager-plus-data`), true);
  assert.equal(isOwnedResourceName(PREFIX, `${PREFIX}-cpa-manager-plus-1`), true);
  for (const foreign of [
    'cpamp-manager-plus',
    'cpamp-ingress',
    'buildx_buildkit_default0',
    'cpa-manager-plus-cpa-manager-plus-1',
    'cpa-usage-keeper',
    'cpampd0123456f-extra',
    'someone-else',
  ]) {
    assert.equal(isOwnedResourceName(PREFIX, foreign), false, `${foreign} must not be owned`);
  }
});

test('the registry refuses to adopt an object it did not create', () => {
  const registry = createResourceRegistry({ prefix: PREFIX });
  let ran = false;
  assert.throws(
    () => registry.track('container', 'cpa-usage-keeper', () => {
      ran = true;
    }),
    /own resources/,
  );
  assert.throws(
    () => registry.track('volume', 'cpamp-manager-plus-data', () => {
      ran = true;
    }),
    /own resources/,
  );
  assert.equal(registry.all().length, 0);
  assert.equal(ran, false);
});

test('releaseAll frees what this run created, in the order the kinds require', async () => {
  const registry = createResourceRegistry({ prefix: PREFIX });
  const released = [];
  for (const kind of RESOURCE_RELEASE_ORDER) {
    // Track in reverse so the assertion is about the release order, not insertion.
    const names = { hostProcess: 'manager', composeProject: PREFIX, container: 'box', network: 'net', volume: 'vol', image: 'upgrade:v1.14.10', worktree: 'tree' };
    const name = kind === 'composeProject' ? PREFIX : kind === 'image' ? `${PREFIX}/${names[kind]}` : `${PREFIX}-${names[kind]}`;
    registry.track(kind, name, async () => {
      released.push(kind);
    }, { root: '/tmp/u1-8' });
  }
  const result = await registry.releaseAll();
  assert.deepEqual(result.errors, []);
  assert.equal(result.ok, true);
  assert.deepEqual(released, [...RESOURCE_RELEASE_ORDER]);
  assert.equal(registry.all().length, 0);
});

test('a worktree is owned by the scratch root, never by a protected name', () => {
  const registry = createResourceRegistry({ prefix: PREFIX, root: '/w/bin/tmp/u1-8/01234567' });
  assert.throws(
    () => registry.track('worktree', '/Users/someone/checkout', async () => {}),
    /own resources/,
  );
  const entry = registry.track('worktree', '/w/bin/tmp/u1-8/01234567/fixture-v1.14.10', async () => {});
  assert.equal(entry.kind, 'worktree');
});

test('a release failure is reported and the object stays owned for the next attempt', async () => {
  const registry = createResourceRegistry({ prefix: PREFIX });
  let attempts = 0;
  registry.track('network', `${PREFIX}_default`, async () => {
    attempts += 1;
    if (attempts === 1) throw new Error('network has active endpoints');
  });
  const first = await registry.releaseAll();
  assert.equal(first.ok, false);
  assert.match(first.errors[0], /network has active endpoints/);
  assert.equal(registry.has('network', `${PREFIX}_default`), true);

  const second = await registry.releaseAll();
  assert.equal(second.ok, true);
  assert.equal(registry.has('network', `${PREFIX}_default`), false);
});

test('a cleanup that left something behind cannot produce a passing exit code', () => {
  assert.equal(finalExitCode({ runOk: true, cleanupOk: true }), 0);
  assert.equal(finalExitCode({ runOk: false, cleanupOk: true }), 1);
  assert.equal(finalExitCode({ runOk: true, cleanupOk: false }), 1);
  assert.equal(finalExitCode({ runOk: false, cleanupOk: false }), 1);
});

test('an aborted run exits with the signal code, whatever the cleanup found', () => {
  assert.equal(finalExitCode({ runOk: false, cleanupOk: true, signal: 'SIGINT' }), 130);
  assert.equal(finalExitCode({ runOk: false, cleanupOk: false, signal: 'SIGTERM' }), 143);
  assert.ok(UPGRADE_DOCKER_CONTRACT.signalExitCodes.SIGTERM > 0);
});

// ---------------------------------------------------------------------------
// cancellation
// ---------------------------------------------------------------------------

test('a handled signal runs the same release path and exits non-zero', async () => {
  const target = new EventEmitter();
  const exits = [];
  const signals = [];
  const guard = installSignalGuard({
    target,
    onSignal: async (signal) => {
      signals.push(signal);
      return { exitCode: 143 };
    },
    exit: (code) => exits.push(code),
  });
  target.emit('SIGTERM');
  await new Promise((resolvePromise) => setImmediate(resolvePromise));
  assert.deepEqual(signals, ['SIGTERM']);
  assert.deepEqual(exits, [143]);

  // A second signal must not start a second release.
  target.emit('SIGTERM');
  target.emit('SIGINT');
  await new Promise((resolvePromise) => setImmediate(resolvePromise));
  assert.deepEqual(signals, ['SIGTERM']);
  assert.deepEqual(exits, [143]);
  guard.detach();
  assert.equal(target.listenerCount('SIGTERM'), 0);
});

test('a release that throws still exits with the signal code', async () => {
  const target = new EventEmitter();
  const exits = [];
  installSignalGuard({
    target,
    onSignal: async () => {
      throw new Error('cleanup failed');
    },
    exit: (code) => exits.push(code),
  });
  target.emit('SIGINT');
  await new Promise((resolvePromise) => setImmediate(resolvePromise));
  assert.deepEqual(exits, [130]);
});

// The EventEmitter tests above cannot see Node's default signal termination: a
// listener registered with once() is removed after the first signal, so emitting
// a second one on a stand-in does nothing, while a real process is killed in the
// middle of the release. This test runs the guard in a real child process and
// sends SIGTERM twice while its cleanup is asleep, which is what a second Ctrl-C
// or a repeated supervisor signal does.
test('a repeated signal cannot kill the process before cleanup finishes', { timeout: 15_000 }, async () => {
  const harnessURL = new URL('./upgrade-docker.mjs', import.meta.url).href;
  const directory = mkdtempSync(path.join(tmpdir(), 'upgrade-docker-signal-'));
  const evidencePath = path.join(directory, 'evidence.json');
  const childSource = `
import { writeFileSync } from 'node:fs';
import { createResourceRegistry, installSignalGuard, UPGRADE_DOCKER_CONTRACT } from ${JSON.stringify(harnessURL)};
// A lone signal listener does not hold the event loop open on every Node
// version; the real harness always has an awaited step pending. The interval is
// the child's stand-in for that work: the guard is what decides the exit.
setInterval(() => {}, 1000);
const registry = createResourceRegistry({ prefix: 'cpampd01234567' });
registry.track('container', 'cpampd01234567-child', async () => {});
installSignalGuard({
  onSignal: async (signal) => {
    process.stdout.write('cleanup-start\\n');
    await new Promise((resolve) => setTimeout(resolve, 300));
    const cleanup = await registry.releaseAll();
    writeFileSync(${JSON.stringify(evidencePath)}, JSON.stringify({
      signal,
      registry: registry.all().map((entry) => entry.name),
      cleanupOk: cleanup.ok,
    }));
    process.stdout.write('cleanup-done\\n');
    return { exitCode: UPGRADE_DOCKER_CONTRACT.signalExitCodes[signal] };
  },
});
process.stdout.write('ready\\n');
`;
  const child = spawn(process.execPath, ['--input-type=module', '--eval', childSource], {
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  let stdout = '';
  let stderr = '';
  let signalled = false;
  child.stdout.setEncoding('utf8');
  child.stderr.setEncoding('utf8');
  child.stdout.on('data', (chunk) => {
    stdout += chunk;
    if (!signalled && stdout.includes('ready\n')) {
      signalled = true;
      child.kill('SIGTERM');
      // The second signal lands while onSignal is still sleeping: the release is
      // already running and must be allowed to finish.
      setTimeout(() => child.kill('SIGTERM'), 60);
    }
  });
  child.stderr.on('data', (chunk) => {
    stderr += chunk;
  });
  try {
    const [code, signal] = await new Promise((resolvePromise) => {
      child.on('close', (exitCode, exitSignal) => resolvePromise([exitCode, exitSignal]));
    });
    assert.equal(signal, null, `the second SIGTERM killed the process: ${stderr}`);
    assert.equal(code, 143, `child exit=${code} signal=${signal}; stdout: ${stdout} stderr: ${stderr}`);
    assert.match(stdout, /cleanup-done/, `cleanup never finished; stdout: ${stdout} stderr: ${stderr}`);
    const evidence = JSON.parse(readFileSync(evidencePath, 'utf8'));
    assert.equal(evidence.signal, 'SIGTERM');
    assert.deepEqual(evidence.registry, [], 'the registry held a resource after cleanup');
    assert.equal(evidence.cleanupOk, true);
  } finally {
    rmSync(directory, { recursive: true, force: true });
  }
});

// ---------------------------------------------------------------------------
// port allocation
// ---------------------------------------------------------------------------

test('two ports taken in one run are never the same port', () => {
  const allocator = createPortAllocator({ probe: () => true, random: () => 0.5 });
  const first = allocator.take();
  const second = allocator.take();
  const third = allocator.take();
  assert.notEqual(first, second);
  assert.notEqual(second, third);
  assert.notEqual(first, third);
  for (const port of [first, second, third]) {
    assert.ok(port >= PORT_RANGE.min && port <= PORT_RANGE.max);
  }
});

test('the allocator skips a busy or already reserved port', () => {
  const busy = PORT_RANGE.min + 10;
  const allocator = createPortAllocator({
    probe: (port) => port !== busy,
    random: () => 0,
    reserved: [PORT_RANGE.min],
  });
  const taken = allocator.take();
  assert.notEqual(taken, busy);
  assert.notEqual(taken, PORT_RANGE.min);
});

test('the allocator refuses instead of returning a colliding port', () => {
  const allocator = createPortAllocator({ probe: () => false, range: { min: 20_000, max: 20_002 } });
  assert.throws(() => allocator.take(), /no free port/);
});

// ---------------------------------------------------------------------------
// redaction of secret-bearing failure paths
// ---------------------------------------------------------------------------

test('a registered secret never survives redaction, and unrelated text does', () => {
  const redactor = createRedactor(['Cpamp_Source_Admin_Key', 'v2-admin-key-0123456789']);
  redactor.add('data-key-material-0123456789');
  redactor.add('handoff-token-0123456789');
  const message = [
    'command failed',
    'source key Cpamp_Source_Admin_Key',
    'v2 key v2-admin-key-0123456789',
    'data key material data-key-material-0123456789',
    'fragment #handoff-token-0123456789',
    'exit code 1',
  ].join(' | ');
  const redacted = redactor.redact(message);
  for (const secret of [
    'Cpamp_Source_Admin_Key',
    'v2-admin-key-0123456789',
    'data-key-material-0123456789',
    'handoff-token-0123456789',
  ]) {
    assert.equal(redacted.includes(secret), false, `${secret} survived redaction`);
  }
  assert.match(redacted, /command failed/);
  assert.match(redacted, /exit code 1/);
});

test('a leak assertion reports the category and never reprints the secret', () => {
  const canary = 'Canary_Leaked_Secret_0123456789';
  let message = null;
  try {
    assertNoSecretInBody('wizard status', { value: canary }, [canary]);
  } catch (error) {
    message = error.message;
  }
  assert.ok(message, 'a body that carries the secret must be refused');
  assert.equal(message.includes(canary), false, 'the refusal must not reprint the secret');
  assert.equal(message.includes(canary.slice(0, 24)), false, 'not even a prefix of the secret may be reprinted');
});

test('a clean body still passes the leak assertion', () => {
  assert.doesNotThrow(() => assertNoSecretInBody('wizard status', { stage: 'copy_in', recorded: true }, ['a-secret-value']));
});

// ---------------------------------------------------------------------------
// evidence binding
// ---------------------------------------------------------------------------

test('an untouched evidence document verifies against itself', () => {
  const document = evidenceDocument();
  const verdict = verifyEvidenceDocument(document, { expectHead: HEAD });
  assert.deepEqual(verdict.problems, []);
  assert.equal(verdict.ok, true);
  assert.equal(document.sha256, evidenceHash(document));
});

test('editing a recorded result without rehashing the document is refused', () => {
  const document = evidenceDocument();
  const edited = { ...document, rollback: { writtenRowInLiveDirectory: 999 } };
  const verdict = verifyEvidenceDocument(edited);
  assert.equal(verdict.ok, false);
  assert.match(verdict.problems.join('; '), /sha256/);
});

test('a rehashed edit still cannot claim a different commit than the one that ran', () => {
  const document = evidenceDocument();
  const tampered = withEvidenceHash({ ...document, rollback: { writtenRowInLiveDirectory: 999 } });
  // Recomputing the hash makes the document self-consistent, which is why the
  // implementation binding is the part that carries authenticity.
  assert.equal(verifyEvidenceDocument(tampered).ok, true);
  const verdict = verifyEvidenceDocument(tampered, { expectHead: 'f'.repeat(40) });
  assert.equal(verdict.ok, false);
  assert.match(verdict.problems.join('; '), /expected commit/);
});

test('a document without a verdict or a head binding is refused', () => {
  const document = evidenceDocument();
  const withoutVerdict = withEvidenceHash({ ...document, verdict: undefined });
  assert.match(verifyEvidenceDocument(withoutVerdict).problems.join('; '), /verdict/);

  const withoutHead = withEvidenceHash({ ...document, implementation: { harnessSha256: HARNESS_SHA } });
  assert.match(verifyEvidenceDocument(withoutHead).problems.join('; '), /headSha/);
});

test('a document without a binary digest or a clean tree is refused', () => {
  const document = evidenceDocument();
  for (const [name, implementation, problem] of [
    ['no binary digest', { ...document.implementation, binarySha256: null }, /binarySha256/],
    ['an unknown tree state', { ...document.implementation, treeClean: null }, /treeClean/],
    ['a dirty tree', { ...document.implementation, treeClean: false }, /treeClean/],
  ]) {
    const verdict = verifyEvidenceDocument(withEvidenceHash({ ...document, implementation }));
    assert.equal(verdict.ok, false, name);
    assert.match(verdict.problems.join('; '), problem, name);
  }
});

test('the printed next command is compared as exact shell tokens', () => {
  assert.deepEqual(shellTokens("cpa-manager-plus upgrade reconcile --legacy '/srv/a b' --source-version 'v1.14.4'"), [
    'cpa-manager-plus',
    'upgrade',
    'reconcile',
    '--legacy',
    '/srv/a b',
    '--source-version',
    'v1.14.4',
  ]);
  // The ShellQuote escape for a literal quote must not split the argument.
  assert.deepEqual(shellTokens(`'it'\\''s'`), ["it's"]);
  // A substring check would accept this; the tokens are what the test asserts.
  assert.notDeepEqual(shellTokens("cpa-manager-plus upgrade reconcile --legacy '/srv/source'"), [
    'cpa-manager-plus',
    'upgrade',
    'reconcile',
    '--legacy',
    '/srv/source',
    '--target',
    '/srv/target',
  ]);
});

test('the canonical hash ignores only the hash field itself', () => {
  const document = evidenceDocument();
  assert.equal(canonicalEvidenceDocument(document).sha256, undefined);
  assert.notEqual(evidenceHash({ ...document, verdict: 'fail' }), document.sha256);
});

test('an implementation identity is invalid until the commit and harness digest are there', () => {
  assert.equal(createImplementationIdentity({ headSha: HEAD, harnessSha256: HARNESS_SHA }).valid, true);
  const incomplete = createImplementationIdentity({ headSha: 'not-a-sha', harnessSha256: '' });
  assert.equal(incomplete.valid, false);
  assert.deepEqual(incomplete.invalid.sort(), ['harnessSha256', 'headSha']);
});

// ---------------------------------------------------------------------------
// declared source version: every source-reading command names the pinned release
// ---------------------------------------------------------------------------

test('preflight, run and reconcile always declare the pinned accepted release', () => {
  for (const subcommand of ['preflight', 'run', 'reconcile']) {
    const declared = withDeclaredSourceVersion(['upgrade', subcommand, '--legacy', '/source', '--json'], 'v1.14.4');
    assert.deepEqual(declared, ['upgrade', subcommand, '--source-version', 'v1.14.4', '--legacy', '/source', '--json']);
  }
  // A caller that already declared a version keeps exactly what it passed.
  const explicit = ['upgrade', 'run', '--source-version', 'v1.14.10', '--legacy', '/source'];
  assert.deepEqual(withDeclaredSourceVersion(explicit, 'v1.14.4'), explicit);
  // Commands that read the recorded row rather than a source are untouched.
  for (const command of [['upgrade', 'status', '--json'], ['upgrade', 'rollback', '--prepare'], ['initialize']]) {
    assert.deepEqual(withDeclaredSourceVersion(command, 'v1.14.4'), command);
  }
  // There is no implicit version: the harness fails closed instead of calling
  // the product without the declaration the accepted copy-in requires.
  assert.throws(
    () => withDeclaredSourceVersion(['upgrade', 'run', '--legacy', '/source'], null),
    /must know the pinned accepted release/,
  );
  assert.throws(
    () => withDeclaredSourceVersion(['upgrade', 'reconcile'], '1.14.4'),
    /must know the pinned accepted release/,
  );
});

test('every source-reading invocation in the harness runs through the declaration', () => {
  // The declaration is applied once, in runCli, so a new call site cannot
  // silently omit it. This pins that wiring: both the helper call and the
  // subcommand list live in the shipped source, and the only direct spawn of
  // `upgrade run` (the interrupted-copy-in fault) opts in explicitly.
  assert.match(HARNESS_SOURCE, /const effectiveArgs = withDeclaredSourceVersion\(cliArgs, acceptedRelease\);/);
  assert.match(HARNESS_SOURCE, /\[.*'upgrade', 'preflight'.*\]/);
  assert.match(HARNESS_SOURCE, /\[.*'upgrade', 'run'.*\]/);
  assert.match(HARNESS_SOURCE, /\[.*'upgrade', 'reconcile'.*\]/);
  const directSpawns = [...HARNESS_SOURCE.matchAll(/spawn\(binary, (\[[^\]]*\])/g)].map((match) => match[1]);
  const sourceSpawns = directSpawns.filter((args) => args.includes("'upgrade', 'run'"));
  assert.deepEqual(sourceSpawns.length, 0, 'a direct spawn of upgrade run must pass the declared release through the helper');
  assert.match(HARNESS_SOURCE, /withDeclaredSourceVersion\(\['upgrade', 'run'/);
});

// ---------------------------------------------------------------------------
// rollback alias: run-owned, vX.Y.Z, removed with the run
// ---------------------------------------------------------------------------

test('the rollback alias is derived from the run prefix at an exact vX.Y.Z tag', () => {
  assert.equal(rollbackAliasReference('v1.14.10', PREFIX), `${PREFIX}/upgrade:v1.14.10`);
  assert.equal(rollbackAliasReference('v1.14.4', PREFIX), `${PREFIX}/upgrade:v1.14.4`);
  for (const bad of ['1.14.10', 'v1.14', 'v1.14.10-rc1', 'latest', '']) {
    assert.throws(() => rollbackAliasReference(bad, PREFIX), /exact vX\.Y\.Z release/, `${bad} must be refused`);
  }
  assert.throws(() => rollbackAliasReference('v1.14.10', 'cpampd-fixture'), /cpampd<8 hex> prefix/);
});

test('only the run-owned image alias is an owned image reference', () => {
  assert.equal(isOwnedImageAlias(PREFIX, `${PREFIX}/upgrade:v1.14.10`), true);
  for (const foreign of [
    // The shared repository older runs left behind: read-only, never ours.
    'cpampd-fixture/upgrade:v1.14.10',
    'cpampd-fixture/upgrade:1.14.10',
    // Same repository, but not a release tag the accepted grammar reads.
    `${PREFIX}/upgrade:1.14.10`,
    `${PREFIX}/upgrade:latest`,
    // Not the alias repository, or not this run's prefix.
    `${PREFIX}/other:v1.14.10`,
    `${PREFIX}-upgrade:v1.14.10`,
    'cpampd0123456f/upgrade:v1.14.10',
    'someone-else/upgrade:v1.14.10',
  ]) {
    assert.equal(isOwnedImageAlias(PREFIX, foreign), false, `${foreign} must not be an owned alias`);
  }
});

test('the registry tracks the run alias and refuses the shared image names', () => {
  const registry = createResourceRegistry({ prefix: PREFIX });
  const alias = `${PREFIX}/upgrade:v1.14.10`;
  const entry = registry.track('image', alias, async () => {});
  assert.equal(entry.kind, 'image');
  assert.deepEqual(registry.list('image').map((item) => item.name), [alias]);
  for (const foreign of ['cpampd-fixture/upgrade:v1.14.10', `${PREFIX}-upgrade:v1.14.10`, `${PREFIX}/upgrade:1.14.10`]) {
    assert.throws(() => registry.track('image', foreign, async () => {}), /own resources/);
  }
});

// ---------------------------------------------------------------------------
// image writes: only the harness's own cpampd- namespace
// ---------------------------------------------------------------------------

test('every image write the harness plans stays in its own cpampd- namespace', () => {
  const fixture = `${IMAGE_REPOSITORY}:1.14.10`;
  const alias = rollbackAliasReference('v1.14.10', PREFIX);
  assert.equal(IMAGE_REPOSITORY.startsWith('cpampd-'), true, 'the fixture repository must be in the cpampd namespace');
  assert.equal(IMAGE_REPOSITORY.startsWith('cpamp-'), false, 'the fixture repository must not be a cpamp- object');
  assert.equal(imageRepository(fixture), IMAGE_REPOSITORY);
  assert.equal(imageRepository(alias), `${PREFIX}/upgrade`);

  // The three argv shapes the harness actually plans.
  assert.doesNotThrow(() =>
    assertImageWriteArgv(
      ['build', '-f', 'Dockerfile.manager-server', '-t', fixture, '--build-arg', 'VERSION=v1.14.10', '.'],
      { prefix: PREFIX },
    ),
  );
  assert.doesNotThrow(() => assertImageWriteArgv(['tag', fixture, alias], { prefix: PREFIX }));
  assert.doesNotThrow(() => assertImageWriteArgv(['image', 'rm', alias], { prefix: PREFIX }));
  assert.doesNotThrow(() => assertImageWriteArgv(['rmi', alias], { prefix: PREFIX }));

  // No cpamp- (non-cpampd) reference, shared read-only image or bare tag may be
  // built over, retagged or removed.
  for (const argv of [
    ['build', '-t', 'cpamp-d-fixture-v1:1.14.10', '.'],
    ['build', '--tag=cpamp-d-fixture-v1:1.14.4', '.'],
    ['build', '-t=cpamp-d-fixture-v1:1.14.4', '.'],
    ['tag', 'cpamp-d-fixture-v1:1.14.10', alias],
    ['image', 'rm', 'cpamp-d-fixture-v1:1.14.4'],
    ['rmi', 'cpamp-d-fixture-v1:1.14.4'],
    ['tag', 'cpampd-fixture/upgrade:1.14.10', alias],
    ['image', 'rm', 'cpampd-fixture/upgrade:1.14.10'],
    ['image', 'rm', '1.14.10'],
    ['tag', fixture, 'cpampd0123456f/upgrade:v1.14.10'],
  ]) {
    assert.throws(() => assertImageWriteArgv(argv, { prefix: PREFIX }), /not one of this harness's repositories/, argv.join(' '));
  }
  assert.throws(() => assertImageWriteArgv(['push', fixture], { prefix: PREFIX }), /unsupported image write subcommand/);
});

test('releasing the alias removes only this run tag and refuses another image', async () => {
  const { context, calls } = aliasHarness({
    ids: { [`${PREFIX}/upgrade:v1.14.10`]: 'sha256:fixture' },
  });
  await context.releaseImageAlias(`${PREFIX}/upgrade:v1.14.10`, 'sha256:fixture');
  assert.deepEqual(calls, [
    ['docker', 'image', 'inspect', '-f', '{{.Id}}', `${PREFIX}/upgrade:v1.14.10`],
    ['docker', 'image', 'rm', `${PREFIX}/upgrade:v1.14.10`],
    ['docker', 'image', 'inspect', '-f', '{{.Id}}', `${PREFIX}/upgrade:v1.14.10`],
  ]);

  // A tag that resolves to somebody else's image is inspected, never removed.
  const foreign = aliasHarness({ ids: { [`${PREFIX}/upgrade:v1.14.10`]: 'sha256:old-unlabeled' } });
  assert.throws(
    () => foreign.context.releaseImageAlias(`${PREFIX}/upgrade:v1.14.10`, 'sha256:fixture'),
    /refusing to remove/,
  );
  assert.deepEqual(foreign.calls.filter((command) => command[1] === 'image' && command[2] === 'rm'), []);

  // A tag that is already gone is already released.
  const gone = aliasHarness({ ids: {} });
  await gone.context.releaseImageAlias(`${PREFIX}/upgrade:v1.14.10`, 'sha256:fixture');
  assert.deepEqual(gone.calls.filter((command) => command[1] === 'image' && command[2] === 'rm'), []);

  // A cpamp- reference is refused before the remove, even when its ID matches.
  const protectedImage = aliasHarness({ ids: { 'cpamp-d-fixture-v1:1.14.10': 'sha256:fixture' } });
  assert.throws(
    () => protectedImage.context.releaseImageAlias('cpamp-d-fixture-v1:1.14.10', 'sha256:fixture'),
    /not one of this harness's repositories/,
  );
  assert.deepEqual(protectedImage.calls.filter((command) => command[1] === 'image' && command[2] === 'rm'), []);
});

test('the cleanup proof keeps pre-existing shared images by ID and rejects a surviving alias', () => {
  const before = [
    { reference: 'cpampd-fixture/upgrade:1.14.10', imageId: 'sha256:old-unlabeled' },
    { reference: 'cpampd-fixture/upgrade:1.14.4', imageId: 'sha256:older-unlabeled' },
    { reference: 'cpampd-fixture/upgrade:1.14.11', imageId: null },
  ];
  const clean = imageGuardHarness({
    ids: {
      'cpampd-fixture/upgrade:1.14.10': 'sha256:old-unlabeled',
      'cpampd-fixture/upgrade:1.14.4': 'sha256:older-unlabeled',
    },
    listed: ['cpampd-fixture/upgrade:1.14.10', 'cpampd-fixture/upgrade:1.14.4', 'cpamp-d-fixture-v1:1.14.10'],
  });
  const cleanResult = clean.context.assertSharedImagesUnchanged(before);
  assert.equal(cleanResult.checked, 2);
  assert.deepEqual([...cleanResult.changed], []);
  assert.deepEqual([...cleanResult.leftovers], []);

  const changed = imageGuardHarness({
    ids: { 'cpampd-fixture/upgrade:1.14.10': 'sha256:repointed' },
    listed: [],
  });
  assert.match(
    changed.context.assertSharedImagesUnchanged(before).changed.join('; '),
    /cpampd-fixture\/upgrade:1\.14\.10 changed: was sha256:old-unlabeled, now sha256:repointed/,
  );

  const leaked = imageGuardHarness({
    ids: { 'cpampd-fixture/upgrade:1.14.10': 'sha256:old-unlabeled' },
    listed: [`${PREFIX}/upgrade:v1.14.10`],
  });
  assert.deepEqual([...leaked.context.assertSharedImagesUnchanged(before).leftovers], [`${PREFIX}/upgrade:v1.14.10`]);

  const blind = imageGuardHarness({ ids: {}, listed: null });
  assert.throws(() => blind.context.assertSharedImagesUnchanged(before), /cannot list the host's images/);
});

// ---------------------------------------------------------------------------
// shutdown interleaving: the real main -> runAcceptance -> abortRun -> finalize
// -> cleanupResources flow, with stand-ins only at the Docker, product and
// process boundaries
// ---------------------------------------------------------------------------

// main, runAcceptance and cleanupResources own module globals because exactly
// one real run mutates them. Extracting these declarations into a fresh context
// exercises that same control flow with controlled boundaries; a second copy of
// the sequencing in this file could drift from what the harness actually does.
const HARNESS_SOURCE = readFileSync(new URL('./upgrade-docker.mjs', import.meta.url), 'utf8');
const LIFECYCLE_FUNCTIONS = [
  'main',
  'runAcceptance',
  'abortRun',
  'finalize',
  'runFinalize',
  'cleanupResources',
  'acceptanceStep',
  'assertAcceptanceRunning',
  'abortError',
];

function harnessFunction(name) {
  const match = HARNESS_SOURCE.match(new RegExp(`(?:^|\\n)(?:async )?function ${name}\\(`));
  assert.ok(match, `upgrade-docker.mjs has no function ${name}`);
  const start = match.index + (match[0].startsWith('\n') ? 1 : 0);
  const end = HARNESS_SOURCE.indexOf('\n}', start);
  assert.ok(end > start, `cannot find the end of ${name}`);
  return HARNESS_SOURCE.slice(start, end + 2);
}

// aliasHarness extracts the shipped release function and supplies the Docker
// and inspect boundaries, so the test drives the real cleanup code without a
// daemon.
function aliasHarness({ ids = {} } = {}) {
  const calls = [];
  const state = { ids: { ...ids } };
  const sh = (command, commandArgs) => {
    calls.push([command, ...commandArgs]);
    if (command !== 'docker') throw new Error(`unexpected command ${command}`);
    const verb = commandArgs.slice(0, 2).join(' ');
    if (verb === 'image inspect') {
      const reference = commandArgs[commandArgs.length - 1];
      const id = state.ids[reference];
      return id ? { code: 0, stdout: id + '\n', stderr: '' } : { code: 1, stdout: '', stderr: 'no such image' };
    }
    if (verb === 'image rm') {
      const reference = commandArgs[commandArgs.length - 1];
      if (!state.ids[reference]) return { code: 1, stdout: '', stderr: 'no such image' };
      delete state.ids[reference];
      return { code: 0, stdout: `Untagged: ${reference}\n`, stderr: '' };
    }
    throw new Error(`unexpected docker command ${commandArgs.join(' ')}`);
  };
  // imageId goes through the same inspect command the harness uses, so the
  // test observes the real inspect-before-remove sequence.
  const imageId = (reference) => {
    const result = sh('docker', ['image', 'inspect', '-f', '{{.Id}}', reference]);
    return result.code === 0 ? result.stdout.trim() : null;
  };
  const context = vm.createContext({
    sh,
    imageId,
    redactor: createRedactor([]),
    tail: (text, limit = 2_000) => String(text ?? '').slice(-limit),
    // The extracted release function writes through the same guard the harness
    // uses, so the test observes the refused-reference check as well as the
    // inspect-before-remove sequence.
    dockerImageWrite: (commandArgs) => {
      assertImageWriteArgv(commandArgs, { prefix: PREFIX });
      return sh('docker', commandArgs);
    },
  });
  vm.runInContext(harnessFunction('releaseImageAlias'), context);
  return { context, calls, state };
}

function imageGuardHarness({ ids = {}, listed = [] } = {}) {
  const context = vm.createContext({
    PREFIX,
    imageId: (reference) => ids[reference] ?? null,
    dockerNames: () => (listed === null ? null : listed),
    isOwnedImageAlias,
  });
  vm.runInContext(harnessFunction('assertSharedImagesUnchanged'), context);
  return { context };
}

function createLifecycleHarness({ failWizardRelease = false } = {}) {
  const registry = createResourceRegistry({ prefix: PREFIX, root: '/owned/scratch' });
  const events = [];
  const logs = [];
  const exits = [];
  const listings = [];
  const noop = () => {};
  const signalTarget = new EventEmitter();
  const state = {
    admissionEntered: false,
    admissionStarted: false,
    faultsStarted: 0,
    lateAlive: false,
    releaseCalls: 0,
    released: [],
    written: null,
  };
  const wizardGate = new Promise((resolvePromise) => {
    state.resumeWizard = resolvePromise;
  });
  state.wizardStarted = new Promise((resolvePromise) => {
    state.resolveWizardStarted = resolvePromise;
  });
  const evidence = {
    contract: UPGRADE_DOCKER_CONTRACT.contract,
    mode: {},
    host: { docker: 'stand-in' },
    fixtures: [],
    chains: {},
    faults: [],
    rollback: {},
    wizard: {},
    admission: {},
    cleanup: {},
    findings: [],
  };

  // The release is what the interrupted wizard is waiting on, exactly like a
  // host manager whose process goes away under an in-flight HTTP wait.
  const releaseWizard = async () => {
    state.releaseCalls += 1;
    events.push('release-wizard');
    state.released.push('wizard');
    state.resumeWizard();
    await new Promise((resolvePromise) => setImmediate(resolvePromise));
    if (failWizardRelease) throw new Error('the wizard host process still holds its port');
  };

  const step = () => {
    state.faultsStarted += 1;
  };
  const context = vm.createContext({
    console: {
      log: (...parts) => logs.push(parts.join(' ')),
      error: (...parts) => logs.push(parts.join(' ')),
    },
    Date,
    process: { env: {} },
    fs: { mkdirSync: noop, rmSync: noop },
    SCRATCH: '/owned/scratch',
    EVIDENCE_DIR: '/owned/evidence',
    args: { reuseFixture: false, keep: false, v1Tag: 'v1.14.10', sourceTag: 'v1.14.10' },
    started: 0,
    abortedSignal: null,
    runFailure: null,
    hostBeforeRun: null,
    finalizing: null,
    acceptanceSettled: null,
    evidence,
    stableFacts: [],
    registry,
    newCleanupResult,
    finalExitCode,
    UPGRADE_DOCKER_CONTRACT,
    redactor: createRedactor([]),
    captureImplementationIdentity: () => ({ valid: true }),
    installSignalGuard: (options) =>
      installSignalGuard({ ...options, target: signalTarget, exit: (code) => exits.push(code) }),
    requireDocker: noop,
    hostSnapshot: () => ({ docker: 'stand-in' }),
    refusePrefixCollision: noop,
    recordProductFixes: noop,
    buildManagerBinary: () => '/fake/binary',
    ensureFixture: async (tag) => ({ tag }),
    seedSource: async () => ({ sourceRoot: '/fake/source', dataset: {} }),
    runCli: () => ({}),
    expectedReleaseOf: () => 'v1.14.10',
    createWorkspace: () => ({ main: { source: '/fake/source', target: '/fake/target' }, snapshots: {} }),
    DATASET_DAYS: [1, 2, 3],
    CHAIN_DOMAINS_DONE: [],
    chain: () => ({}),
    assertWizard: async () => {
      registry.track('hostProcess', `${PREFIX}-wizard`, releaseWizard);
      state.resolveWizardStarted();
      await wizardGate;
      events.push('wizard-returned');
    },
    assertAdmission: async () => {
      // The step must never be entered after the cancellation point. Entering
      // it is observable separately from the fence refusing the spawn, so the
      // test can tell "the continuation stopped" from "the producer refused".
      state.admissionEntered = true;
      // startHostManager's order: refuse a fenced run before spawning, then
      // track. A late admission can therefore neither start nor survive.
      registry.assertCreationAllowed('hostProcess', `${PREFIX}-admission`);
      state.admissionStarted = true;
      state.lateAlive = true;
      registry.track('hostProcess', `${PREFIX}-admission`, async () => {
        state.lateAlive = false;
      });
    },
    faultSourceTouched: step,
    faultInterruptedCopyIn: step,
    faultClearedRollupRecoverable: step,
    faultClearedRollupUnrecoverable: step,
    faultDroppedTable: step,
    faultBusySource: step,
    proveRollback: async () => {
      state.faultsStarted += 1;
    },
    assertHostUntouched: noop,
    dockerNames: (...command) => {
      listings.push(command.join(' '));
      return [];
    },
    PREFIX,
    RELEASE_TAG_PATTERN: /^v\d+\.\d+\.\d+$/,
    acceptedRelease: null,
    writeEvidence: () => {
      state.written = {
        verdict: evidence.verdict,
        exitCode: evidence.exitCode,
        abortedBy: evidence.abortedBy,
        cleanup: evidence.cleanup,
      };
    },
  });
  for (const name of LIFECYCLE_FUNCTIONS) vm.runInContext(harnessFunction(name), context);
  return { context, registry, state, events, logs, exits, listings, signalTarget };
}

test('a signal during an awaited step fences creation and stops the continuation before it starts the next step', async () => {
  const harness = createLifecycleHarness();
  const { context, registry, state, events, exits, signalTarget } = harness;

  const running = context.main();
  await state.wizardStarted;

  // The signal arrives while the wizard step is awaited. A second signal during
  // that same cleanup must not start a second release.
  signalTarget.emit('SIGTERM');
  signalTarget.emit('SIGINT');

  const outcome = await running;
  await new Promise((resolvePromise) => setImmediate(resolvePromise));

  assert.equal(outcome.exitCode, 143);
  assert.deepEqual(exits, [143], 'the handled signal exited once with its own code');
  assert.equal(state.admissionEntered, false, 'the admission step was entered after the signal');
  assert.equal(state.admissionStarted, false, 'the admission step started after the signal');
  assert.equal(state.faultsStarted, 0, 'a fault step started after the signal');
  assert.equal(state.lateAlive, false, 'a host process created after the release survived');
  assert.deepEqual(events, ['release-wizard', 'wizard-returned'], 'the wizard had to finish, and nothing new started');
  assert.equal(state.releaseCalls, 1, 'the existing resource was not released exactly once');
  assert.deepEqual(registry.all(), [], 'the registry held a resource after cleanup');
  assert.match(registry.shutdownReason, /SIGTERM/);

  // The fence is the backstop even if a future step forgets its checkpoint.
  assert.throws(() => registry.track('hostProcess', `${PREFIX}-late`, async () => {}), /shutting down/);
  assert.throws(() => registry.assertCreationAllowed('hostProcess', `${PREFIX}-late`), /shutting down/);

  assert.ok(state.written, 'the aborted run wrote no evidence');
  assert.equal(state.written.verdict, 'aborted');
  assert.equal(state.written.exitCode, 143);
  assert.equal(state.written.abortedBy, 'SIGTERM');
  assert.equal(state.written.cleanup.ok, true);
  assert.deepEqual(state.written.cleanup.errors, []);
  assert.deepEqual(state.written.cleanup.released, [`hostProcess:${PREFIX}-wizard`]);

  const repeated = await context.abortRun('SIGTERM');
  assert.equal(repeated.exitCode, 143);
  assert.deepEqual(repeated.cleanup.released, [`hostProcess:${PREFIX}-wizard`]);
  assert.equal(state.releaseCalls, 1, 'a repeated signal released the wizard process again');
});

test('a successful Docker listing cannot hide a resource the release pass did not reach', async () => {
  const harness = createLifecycleHarness();
  const { context, registry, state, listings, signalTarget } = harness;

  // Model the pre-fix timing bug directly: the release pass runs before the
  // resource is in its snapshot, so it reports no error and the three Docker
  // listings answer normally (host processes are not in them). Only the
  // registry check can tell that a process is still owned.
  registry.releaseAll = async (result = newCleanupResult()) => result;
  signalTarget.once('SIGTERM', () => {
    queueMicrotask(() => state.resumeWizard());
  });

  const running = context.main();
  await state.wizardStarted;
  signalTarget.emit('SIGTERM');
  const outcome = await running;

  assert.equal(outcome.exitCode, 143);
  assert.equal(state.admissionEntered, false);
  assert.equal(listings.length, 3, 'the containers, volumes and networks listings were read successfully');
  assert.ok(state.written, 'the aborted run wrote no evidence');
  assert.equal(state.written.cleanup.ok, false, 'a surviving host process was reported as a clean host');
  assert.deepEqual(state.written.cleanup.unreleased, [`hostProcess:${PREFIX}-wizard`]);
  assert.match(state.written.cleanup.errors.join('; '), /still owned after the release boundary/);
  assert.equal(registry.all().length, 1);
  assert.equal(state.releaseCalls, 0, 'the test deliberately skipped the release pass');
});

test('a release that fails stays owned, fails cleanup and keeps the signal code', async () => {
  const harness = createLifecycleHarness({ failWizardRelease: true });
  const { context, registry, state, listings, signalTarget } = harness;

  const running = context.main();
  await state.wizardStarted;
  signalTarget.emit('SIGTERM');
  const outcome = await running;

  assert.equal(outcome.exitCode, 143, 'an aborted run keeps its signal code even when cleanup failed');
  assert.equal(state.admissionEntered, false);
  assert.equal(state.admissionStarted, false);
  assert.equal(listings.length, 3, 'the containers, volumes and networks listings were read successfully');
  assert.ok(state.written, 'the aborted run wrote no evidence');
  assert.equal(state.written.verdict, 'aborted');
  assert.equal(state.written.cleanup.ok, false, 'a surviving host process was reported as a clean host');
  assert.deepEqual(state.written.cleanup.unreleased, [`hostProcess:${PREFIX}-wizard`]);
  assert.match(state.written.cleanup.errors.join('; '), /still holds its port/);
  assert.equal(registry.all().length, 1, 'the failed release stayed owned for a later attempt');
  assert.equal(state.releaseCalls, 1);
});

// ---------------------------------------------------------------------------
// ownership normalisation and the host-side copy
// ---------------------------------------------------------------------------

// The real-Docker harness copies a tree a 1.x container wrote. On a Linux runner
// that container's uid is not the runner's, so a directory inside the tree can be
// unreadable to this process; Node's native recursive copy then aborts the whole
// process on a C++ filesystem exception instead of throwing, and no evidence file
// is ever written. The walker below is what the harness uses, and it must fail as
// a reportable error that names the path.
test('copyDirectory copies a nested tree and preserves file modes', () => {
  const root = mkdtempSync(path.join(tmpdir(), 'u1d-copy-'));
  try {
    const from = path.join(root, 'from');
    mkdirSync(path.join(from, 'nested', 'deeper'), { recursive: true });
    writeFileSync(path.join(from, 'top.txt'), 'top');
    writeFileSync(path.join(from, 'nested', 'deeper', 'leaf.txt'), 'leaf');
    chmodSync(path.join(from, 'nested', 'deeper', 'leaf.txt'), 0o640);

    const to = path.join(root, 'to');
    copyDirectory(from, to);

    assert.equal(readFileSync(path.join(to, 'top.txt'), 'utf8'), 'top');
    assert.equal(readFileSync(path.join(to, 'nested', 'deeper', 'leaf.txt'), 'utf8'), 'leaf');
    assert.equal(
      readFileSync(path.join(to, 'nested', 'deeper', 'leaf.txt')).length,
      readFileSync(path.join(from, 'nested', 'deeper', 'leaf.txt')).length,
    );
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test('copyDirectory reports an unreadable directory instead of aborting the process', () => {
  const root = mkdtempSync(path.join(tmpdir(), 'u1d-copy-denied-'));
  const blocked = path.join(root, 'from', 'usage-archives');
  try {
    mkdirSync(blocked, { recursive: true });
    writeFileSync(path.join(blocked, 'segment.jsonl'), '{}');
    chmodSync(blocked, 0o000);

    // The assertion is the throw itself: the process is still here to make it,
    // with the path named, which is what turns a host permission problem into a
    // reported harness failure with evidence.
    assert.throws(
      () => copyDirectory(path.join(root, 'from'), path.join(root, 'to')),
      (error) => {
        assert.ok(error instanceof Error, 'the failure is an Error');
        assert.equal(error.code, 'EACCES');
        assert.match(String(error.path), /usage-archives$/);
        return true;
      },
    );
  } finally {
    chmodSync(blocked, 0o700);
    rmSync(root, { recursive: true, force: true });
  }
});

test('ownershipNormalizationArgv repairs the tree as root and returns it to the invoking ids', () => {
  assert.deepEqual(
    ownershipNormalizationArgv({ name: 'cpampd00000000-own-seed', directory: '/tmp/scratch/source-v1.14.10', uid: 1001, gid: 121 }),
    [
      'run', '--rm',
      '--name', 'cpampd00000000-own-seed',
      '--user', '0:0',
      '-e', 'HOME=/tmp',
      '-v', '/tmp/scratch/source-v1.14.10:/data',
      'alpine:3.21', 'sh', '-c',
      'chown -R 1001:121 /data && chmod -R u+rwX /data',
    ],
  );
  assert.throws(() => ownershipNormalizationArgv({ name: 'x', directory: '/data', uid: 'me', gid: 1 }), /numeric uid/);
  assert.throws(() => ownershipNormalizationArgv({ name: 'x', directory: '/data', uid: 1, gid: -2 }), /numeric gid/);
  assert.throws(() => ownershipNormalizationArgv({ name: 'x', directory: '', uid: 1, gid: 1 }), /needs a directory/);
});

test('evidenceDirectory keeps CI on the published path and a local run out of the tracked tree', () => {
  const root = path.join(path.sep, 'repo');
  const tracked = path.join(root, 'bin', 'ci', 'evidence', 'upgrade-d');

  // CI semantics are unchanged: the workflow reads evidence_path from here.
  assert.equal(evidenceDirectory({ env: { GITHUB_ACTIONS: 'true' }, root }), tracked);
  // A local run writes under the ignored scratch tree, so its own output cannot
  // make the next local run fail with implementation.treeClean is not true.
  const local = evidenceDirectory({ env: {}, root });
  assert.equal(local, path.join(root, 'bin', 'tmp', 'u1-docker', 'evidence'));
  assert.ok(!local.startsWith(path.join(root, 'bin', 'ci')), 'a local run must not publish into the tracked evidence tree');
  // The override is explicit in both directions.
  assert.equal(
    evidenceDirectory({ env: { GITHUB_ACTIONS: 'true', UPGRADE_DOCKER_EVIDENCE_DIR: 'out/evidence' }, root }),
    path.join(root, 'out', 'evidence'),
  );
  assert.equal(
    evidenceDirectory({ env: { UPGRADE_DOCKER_EVIDENCE_DIR: path.join(path.sep, 'tmp', 'e') }, root }),
    path.join(path.sep, 'tmp', 'e'),
  );
  assert.equal(evidenceDirectory({ env: { UPGRADE_DOCKER_EVIDENCE_DIR: '   ' }, root }), local);
});

test('expectedNextAction mirrors the product ladder instead of expecting a stage to name itself', () => {
  // The cases are the product's own tables:
  // internal/http/controller/upgradewizard/external_connect_test.go
  // (TestNextActionForAnExternalConnectRefusal) and verify_action_test.go
  // (TestVerifiedRowOffersTheGateAgain), plus the stage table they run against.
  const cases = [
    // the stage table
    ['', {}, 'preflight'],
    ['preflight', {}, 'preflight'],
    ['copy_in', {}, 'copy_in'],
    // a stage is a checkpoint, not a pending step: reconciliation is done, so the
    // projection offers the CPA import that follows it
    ['reconcile', {}, 'cpa_import'],
    ['cpa_import', {}, 'cpa_import'],
    ['external_connect', {}, 'verify'],
    ['verify', {}, 'complete'],
    ['complete', {}, 'complete'],
    ['failed', {}, 'reconcile'],
    ['rolled_back', {}, 'rolled_back'],
    // a recorded External refusal keeps offering the step that resolves it, from
    // every stage that step can run from
    ['cpa_import', { errorCode: 'external_cpa_version_unsupported' }, 'external_connect'],
    ['cpa_import', { errorCode: 'external_cpa_config_document_invalid' }, 'external_connect'],
    ['cpa_import', { errorCode: 'external_management_key_rejected' }, 'external_connect'],
    ['cpa_import', { errorCode: 'external_target_moved' }, 'external_connect'],
    ['copy_in', { errorCode: 'external_cpa_version_unsupported' }, 'external_connect'],
    ['reconcile', { errorCode: 'external_management_key_rejected' }, 'external_connect'],
    ['reconcile', { errorCode: 'external_target_moved' }, 'external_connect'],
    // the same code at a stage the step cannot run from keeps the stage table
    ['verify', { errorCode: 'external_cpa_version_unsupported' }, 'complete'],
    // an unrelated refusal keeps the stage table
    ['cpa_import', { errorCode: 'upgrade_reconcile_blocked' }, 'cpa_import'],
    // the import has run when it recorded the domain…
    ['cpa_import', { domains: ['source_copy', 'cpa_config'] }, 'verify'],
    // …and it stands deferred when the row carries its typed item
    ['cpa_import', { manualItems: ['Managed CPA import deferred (upgrade_cpa_import_requires_qualification)'] }, 'verify'],
    ['cpa_import', { manualItems: ['reconcile the retained projection'] }, 'cpa_import'],
    // a verify row that stopped waiting for a re-pin offers the gate again
    ['verify', { errorCode: 'upgrade_verify_waiting_for_repin' }, 'verify'],
    // an unknown stage projects nothing rather than a guessed action
    ['never-recorded', {}, ''],
  ];
  for (const [stage, options, want] of cases) {
    assert.equal(expectedNextAction(stage, options), want, `expectedNextAction(${JSON.stringify(stage)}, ${JSON.stringify(options)})`);
  }
});
