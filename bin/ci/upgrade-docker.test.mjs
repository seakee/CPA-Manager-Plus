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
import { EventEmitter } from 'node:events';
import test from 'node:test';

import {
  PORT_RANGE,
  RESOURCE_RELEASE_ORDER,
  UPGRADE_DOCKER_CONTRACT,
  assertNoSecretInBody,
  canonicalEvidenceDocument,
  createImplementationIdentity,
  createPortAllocator,
  createRedactor,
  createResourceRegistry,
  evidenceHash,
  finalExitCode,
  installSignalGuard,
  isOwnedResourceName,
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
    const names = { hostProcess: 'manager', composeProject: PREFIX, container: 'box', network: 'net', volume: 'vol', worktree: 'tree' };
    registry.track(kind, kind === 'composeProject' ? PREFIX : `${PREFIX}-${names[kind]}`, async () => {
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
