// The D harness's lifecycle, driven as the script and as child processes.
//
// Every probe here is a real process and a real directory: the sequential
// teardown this file guards against (one throwing step skipping the rest, a
// signal killing the process mid-cleanup) only exists once the steps and the
// signals actually run. The one thing that is controlled is the command adapter:
// the precondition probes put a fake `docker` first on PATH, so no Docker daemon
// and no image is required and none is touched. They are reported as controlled
// probes, not as a Docker D run.
import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import {
  appendFileSync,
  chmodSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from 'node:fs';
import { createServer } from 'node:http';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { after, describe, it } from 'node:test';

const here = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(here, '../..');
const harnessPath = resolve(here, 'fresh-adopt-network.mjs');
const tempRoot = mkdtempSync(join(tmpdir(), 'cpampd-lifecycle-'));
const secretsPrefix = 'cpampd-adopt-secrets-';

after(() => rmSync(tempRoot, { recursive: true, force: true }));

const wait = (ms) => new Promise((done) => setTimeout(done, ms));

function waitForFile(path, timeout = 10000) {
  return new Promise((done, fail) => {
    const end = Date.now() + timeout;
    const poll = () => {
      if (existsSync(path)) return done();
      if (Date.now() > end) return fail(new Error('timed out waiting for ' + path));
      setTimeout(poll, 25);
    };
    poll();
  });
}

function waitForExit(child) {
  return new Promise((done) => child.once('exit', (code, signal) => done({ code, signal })));
}

function markerSet(root) {
  return new Set(readdirSync(root));
}

// The fixture registers the same steps the harness does, with the same names and
// the same order, so a step that stops the next one is observable. Its "browser"
// and "docker" are functions: the controlled adapter, reported as such.
const fixturePath = join(tempRoot, 'lifecycle-fixture.mjs');
writeFileSync(
  fixturePath,
  `import { appendFileSync, rmSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { createLifecycle } from ${JSON.stringify(pathToFileURL(harnessPath).href)};

const scenario = process.env.CPAMP_LIFECYCLE_SCENARIO;
const secretsDir = process.env.CPAMP_LIFECYCLE_SECRETS;
const markers = process.env.CPAMP_LIFECYCLE_MARKERS;
const mark = (name) => writeFileSync(join(markers, name), '1');
const append = (name) => appendFileSync(join(markers, name), 'x');
const signalScenario = scenario === 'second-sigterm' || scenario === 'cross-signal';

const lifecycle = createLifecycle({ installSignalHandlers: true });
// A live handle before the signal: without it the process would exit before a
// signal could be delivered. Cleanup clears it as its first step.
const keepAlive = setInterval(() => {}, 1000);
lifecycle.add('keep-alive', () => clearInterval(keepAlive));
lifecycle.add('browser.close', async () => {
  if (signalScenario) {
    append('slow-start');
    await new Promise((done) => setTimeout(done, 400));
    mark('slow-end');
  }
  mark('browser-close');
  if (scenario === 'browser-close-failure') throw new Error('browser refused to close');
});
lifecycle.add('container logs', () => mark('logs'));
lifecycle.add('containers', () => {
  mark('containers');
  if (scenario === 'teardown-failure') throw new Error('docker rm failed');
});
lifecycle.add('compose down', () => mark('compose-down'));
lifecycle.add('secrets directory', () => {
  rmSync(secretsDir, { recursive: true, force: true });
  mark('secrets-removed');
});

if (!signalScenario) {
  const { failures } = await lifecycle.close();
  writeFileSync(
    join(markers, 'result.json'),
    JSON.stringify({ failures, signals: lifecycle.signals })
  );
  if (failures.length > 0) process.exitCode = 1;
} else {
  // The listeners exist and the process is alive: the parent may signal now.
  mark('started');
}
`,
  { mode: 0o700 }
);

function runFixture(scenario) {
  const caseDir = join(tempRoot, scenario);
  const markers = join(caseDir, 'markers');
  const secretsDir = join(caseDir, secretsPrefix + scenario);
  mkdirSync(markers, { recursive: true });
  mkdirSync(secretsDir, { recursive: true });
  writeFileSync(join(secretsDir, 'management-key'), 'cpampd-fixture-secret\n', { mode: 0o600 });
  const child = spawn(process.execPath, [fixturePath], {
    env: {
      ...process.env,
      CPAMP_LIFECYCLE_SCENARIO: scenario,
      CPAMP_LIFECYCLE_SECRETS: secretsDir,
      CPAMP_LIFECYCLE_MARKERS: markers,
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  return { child, markers, secretsDir };
}

function readFailures(markers) {
  return JSON.parse(readFileSync(join(markers, 'result.json'), 'utf8')).failures;
}

describe('the harness lifecycle in a real child process', () => {
  it('removes the secret directory and reports no failure', async () => {
    const { child, markers, secretsDir } = runFixture('cleanup');
    const exit = await waitForExit(child);
    assert.equal(exit.signal, null);
    assert.equal(exit.code, 0);
    assert.equal(existsSync(secretsDir), false, 'the secret directory survived cleanup');
    assert.deepEqual(markerSet(markers), new Set(['browser-close', 'logs', 'containers', 'compose-down', 'secrets-removed', 'result.json']));
    assert.deepEqual(readFailures(markers), []);
  });

  it('runs every later step after the browser refuses to close', async () => {
    const { child, markers, secretsDir } = runFixture('browser-close-failure');
    const exit = await waitForExit(child);
    assert.equal(exit.signal, null);
    assert.equal(exit.code, 1, 'a teardown failure must exit nonzero');
    assert.equal(existsSync(secretsDir), false, 'the secret directory survived a failed browser close');
    const markersSeen = markerSet(markers);
    for (const step of ['browser-close', 'logs', 'containers', 'compose-down', 'secrets-removed'])
      assert(markersSeen.has(step), step + ' did not run after a failed browser close');
    assert.deepEqual(
      readFailures(markers).map(({ step, message }) => ({ step, message })),
      [{ step: 'browser.close', message: 'browser refused to close' }]
    );
  });

  it('removes the secret directory after a failing teardown step', async () => {
    const { child, markers, secretsDir } = runFixture('teardown-failure');
    const exit = await waitForExit(child);
    assert.equal(exit.signal, null);
    assert.equal(exit.code, 1, 'a teardown failure must exit nonzero');
    assert.equal(existsSync(secretsDir), false, 'the secret directory survived a failed teardown');
    const markersSeen = markerSet(markers);
    assert(markersSeen.has('containers'), 'the failing step did not run');
    assert(markersSeen.has('compose-down'), 'the step after the failing one did not run');
    assert(markersSeen.has('secrets-removed'), 'the secret removal after the failing step did not run');
    assert.deepEqual(
      readFailures(markers).map(({ step }) => step),
      ['containers']
    );
  });

  it('does not let a second SIGTERM kill the process in the middle of cleanup', async () => {
    const { child, markers, secretsDir } = runFixture('second-sigterm');
    const exitPromise = waitForExit(child);
    await waitForFile(join(markers, 'started'));
    child.kill('SIGTERM');
    await waitForFile(join(markers, 'slow-start'));
    await wait(80);
    // The persistent listener stays attached while close() runs; a harness that
    // removed it after the first signal would die here, mid-cleanup.
    child.kill('SIGTERM');
    const exit = await exitPromise;
    assert.equal(exit.signal, null, 'the process was killed by the second signal');
    assert.equal(exit.code, 143);
    assert.equal(existsSync(secretsDir), false, 'the secret directory survived the signal');
    assert(markerSet(markers).has('slow-end'), 'cleanup did not finish');
    assert(markerSet(markers).has('secrets-removed'), 'secrets were not removed after the signal');
    assert.equal(readFileSync(join(markers, 'slow-start'), 'utf8').length, 1, 'cleanup started more than once');
  });

  it('handles SIGINT and then SIGTERM on the same cleanup', async () => {
    const { child, markers, secretsDir } = runFixture('cross-signal');
    const exitPromise = waitForExit(child);
    await waitForFile(join(markers, 'started'));
    child.kill('SIGINT');
    await waitForFile(join(markers, 'slow-start'));
    await wait(80);
    child.kill('SIGTERM');
    const exit = await exitPromise;
    assert.equal(exit.signal, null, 'the process was killed by the cross signal');
    assert.equal(exit.code, 130);
    assert.equal(existsSync(secretsDir), false, 'the secret directory survived the cross signal');
    assert(markerSet(markers).has('secrets-removed'));
    assert.equal(readFileSync(join(markers, 'slow-start'), 'utf8').length, 1, 'cleanup started more than once');
  });
});

describe('the harness as the real CLI, with a controlled command adapter', () => {
  // The adapter is a fake `docker` on PATH that fails the first precondition
  // (image inspect) and logs its arguments. No daemon, image, container or
  // network is created; this is the lifecycle probe, not the Docker D run.
  function fakeDocker(root, dockerLog) {
    const bin = join(root, 'bin');
    mkdirSync(bin, { recursive: true });
    const shim = join(bin, 'docker');
    writeFileSync(
      shim,
      `#!/bin/sh
printf '%s\\n' "$*" >> "$CPAMP_LIFECYCLE_DOCKER_LOG"
case " $* " in
  *" image inspect "*) echo "Error: No such image" >&2; exit 1;;
esac
exit 0
`,
      { mode: 0o700 }
    );
    chmodSync(shim, 0o700);
    return { bin, dockerLog };
  }

  it('cleans up when the image precondition fails before any Docker resource exists', () => {
    const root = join(tempRoot, 'precondition');
    const out = join(root, 'artifact');
    const dockerLog = join(root, 'docker.log');
    const { bin } = fakeDocker(root, dockerLog);
    writeFileSync(dockerLog, '');
    const before = new Set(readdirSync(join(repoRoot, 'bin/tmp')).filter((name) => name.startsWith(secretsPrefix)));
    const result = spawnSync(
      process.execPath,
      [harnessPath, 'cpampd-probe-runtime', 'cpampd-probe-manager', 'cpampd-probe-ingress', out],
      {
        cwd: repoRoot,
        env: {
          ...process.env,
          PATH: bin + ':' + process.env.PATH,
          CPAMP_LIFECYCLE_DOCKER_LOG: dockerLog,
          // The browser never starts; a module that cannot be resolved here must
          // not change what this probe observes.
          PLAYWRIGHT_MODULE: join(root, 'no-playwright'),
        },
        encoding: 'utf8',
        timeout: 60000,
      }
    );
    assert.equal(result.status, 1, 'a failed prerequisite must exit nonzero');
    const after = new Set(readdirSync(join(repoRoot, 'bin/tmp')).filter((name) => name.startsWith(secretsPrefix)));
    assert.deepEqual([...after].filter((name) => !before.has(name)), [], 'a secret directory survived');
    const reportPath = join(out, 'report.json');
    assert.equal(existsSync(reportPath), true, 'the redacted artifact was not attempted');
    const report = JSON.parse(readFileSync(reportPath, 'utf8'));
    assert.equal(report.result, 'FAIL');
    assert.equal(report.secretAssertion, 'PASS');
    assert.equal(report.checkRegistry, 'PASS');
    assert.match(report.failure, /No such image|image inspect|Command failed/);
    // The teardown ran no destructive command: no project was ever created, so
    // there is nothing to remove and nothing outside this run may be touched.
    const dockerCalls = readFileSync(dockerLog, 'utf8');
    for (const forbidden of ['prune', ' system ', ' rmi ', ' tag ', ' build ', ' down ', ' rm '])
      assert.equal(dockerCalls.includes(forbidden), false, 'the teardown ran `' + forbidden.trim() + '`');
    rmSync(join(repoRoot, 'bin/tmp/logs', 'd-adopt-unknown.log'), { force: true });
  });

  it('does not overwrite an artifact directory it did not create', () => {
    const root = join(tempRoot, 'artifact-exists');
    const out = join(root, 'artifact');
    const dockerLog = join(root, 'docker.log');
    mkdirSync(out, { recursive: true });
    writeFileSync(join(out, 'report.json'), '{"result":"PRIOR"}', { mode: 0o600 });
    const { bin } = fakeDocker(root, dockerLog);
    writeFileSync(dockerLog, '');
    const before = new Set(
      readdirSync(join(repoRoot, 'bin/tmp')).filter((name) => name.startsWith(secretsPrefix))
    );
    const result = spawnSync(
      process.execPath,
      [harnessPath, 'cpampd-probe-runtime', 'cpampd-probe-manager', 'cpampd-probe-ingress', out],
      {
        cwd: repoRoot,
        env: {
          ...process.env,
          PATH: bin + ':' + process.env.PATH,
          CPAMP_LIFECYCLE_DOCKER_LOG: dockerLog,
          PLAYWRIGHT_MODULE: join(root, 'no-playwright'),
        },
        encoding: 'utf8',
        timeout: 60000,
      }
    );
    assert.equal(result.status, 1);
    assert.equal(readFileSync(join(out, 'report.json'), 'utf8'), '{"result":"PRIOR"}', 'an earlier artifact was overwritten');
    const after = new Set(
      readdirSync(join(repoRoot, 'bin/tmp')).filter((name) => name.startsWith(secretsPrefix))
    );
    assert.deepEqual([...after].filter((name) => !before.has(name)), [], 'a secret directory survived');
    assert.equal(readFileSync(dockerLog, 'utf8').trim(), '', 'the refused run touched a command');
  });

  it('removes the secret directory when Compose down itself fails', async () => {
    const root = join(tempRoot, 'teardown-cli');
    const out = join(root, 'artifact');
    const dockerLog = join(root, 'docker.log');
    const revision = 'f'.repeat(40);
    const bin = join(root, 'bin');
    mkdirSync(bin, { recursive: true });
    writeFileSync(dockerLog, '');
    // A complete adapter: image inspect and the revision check pass, Compose
    // answers, and only `down` fails. That takes the real script past its
    // prerequisites with real containers in its owned set, so the teardown path
    // under test is the script's own wiring, not the fixture's.
    writeFileSync(
      join(bin, 'docker'),
      `#!/bin/sh
printf '%s\\n' "$*" >> "$CPAMP_LIFECYCLE_DOCKER_LOG"
case " $* " in
  *" image inspect "*)
    case "$*" in
      *python:3.13-alpine*) printf 'sha256:3333\\n'; exit 0;;
      *) printf '[{"Os":"linux","Architecture":"amd64","Id":"sha256:1111","Config":{"Labels":{"org.opencontainers.image.revision":"%s"}}}]\\n' "$FAKE_REVISION"; exit 0;;
    esac;;
  *" compose "*" ps -q cpamp-ingress"*) printf 'fake-ingress-id\\n'; exit 0;;
  *" down "*) echo 'compose down refused' >&2; exit 1;;
  *" ps -a "*) exit 0;;
  *" inspect "*) printf '[{"NetworkSettings":{"Ports":{"18317/tcp":[{"HostIp":"127.0.0.1","HostPort":"%s"}]},"Networks":{"fake-net":{}}}}]\\n' "$FAKE_PORT"; exit 0;;
esac
exit 0
`,
      { mode: 0o700 }
    );
    writeFileSync(
      join(bin, 'git'),
      `#!/bin/sh
case "$1" in
  rev-parse) printf '%s\\n' "$FAKE_REVISION";;
  status) ;;
esac
exit 0
`,
      { mode: 0o700 }
    );
    const server = createServer((request, response) => {
      response.writeHead(200, { 'content-type': 'application/json' });
      response.end('{}');
    });
    await new Promise((done) => server.listen(0, '127.0.0.1', done));
    const port = server.address().port;
    const before = new Set(
      readdirSync(join(repoRoot, 'bin/tmp')).filter((name) => name.startsWith(secretsPrefix))
    );
    const child = spawn(
      process.execPath,
      [harnessPath, 'cpampd-probe-runtime', 'cpampd-probe-manager', 'cpampd-probe-ingress', out],
      {
        cwd: repoRoot,
        env: {
          ...process.env,
          PATH: bin + ':' + process.env.PATH,
          CPAMP_LIFECYCLE_DOCKER_LOG: dockerLog,
          PLAYWRIGHT_MODULE: join(root, 'no-playwright'),
          FAKE_REVISION: revision,
          FAKE_PORT: String(port),
        },
        stdio: ['ignore', 'pipe', 'pipe'],
      }
    );
    const exitPromise = waitForExit(child);
    let timer;
    const timedOut = new Promise((done, fail) => {
      timer = setTimeout(() => {
        child.kill('SIGKILL');
        fail(new Error('the CLI probe did not exit'));
      }, 30000);
    });
    const exit = await Promise.race([exitPromise, timedOut]);
    clearTimeout(timer);
    await new Promise((done) => server.close(done));
    assert.equal(exit.signal, null);
    assert.equal(exit.code, 1, 'a failed teardown must exit nonzero');
    const after = new Set(
      readdirSync(join(repoRoot, 'bin/tmp')).filter((name) => name.startsWith(secretsPrefix))
    );
    assert.deepEqual([...after].filter((name) => !before.has(name)), [], 'a secret directory survived');
    const report = JSON.parse(readFileSync(join(out, 'report.json'), 'utf8'));
    assert.equal(report.result, 'FAIL', 'a failed teardown was reported as a PASS');
    assert.deepEqual(
      report.teardown.map(({ step }) => step),
      ['compose down']
    );
    assert.match(report.teardown[0].message, /compose down refused/);
    const calls = readFileSync(dockerLog, 'utf8').trim().split('\n').filter(Boolean);
    const composeLine = calls.find((line) => line.startsWith('compose ') && line.includes(' -p '));
    assert(composeLine, 'the run never owned a Compose project: ' + calls.join(' | '));
    const project = composeLine.split(' ')[composeLine.split(' ').indexOf('-p') + 1];
    assert.match(project, /^cpampd-adopt-[0-9a-f]{12}$/);
    const removeLine = calls.find((line) => line.startsWith('rm -f '));
    assert(removeLine, 'the owned containers were never removed: ' + calls.join(' | '));
    for (const name of removeLine.split(' ').slice(2))
      assert(name.startsWith(project + '-'), 'cleanup removed a container outside this run: ' + name);
    const downLine = calls.find((line) => line.startsWith('compose ') && line.includes(' down '));
    assert(downLine, 'the owned Compose project was never taken down: ' + calls.join(' | '));
    assert(downLine.includes(' -p ' + project + ' '), 'the teardown named another project');
    for (const forbidden of ['prune', ' system ', ' rmi ', ' tag ', ' build '])
      assert.equal(readFileSync(dockerLog, 'utf8').includes(forbidden), false, 'the probe ran `' + forbidden.trim() + '`');
    rmSync(join(repoRoot, 'bin/tmp/logs', 'd-adopt-' + revision.slice(0, 12) + '.log'), { force: true });
  });

  it('imports without creating anything or touching a command', () => {
    const root = join(tempRoot, 'import');
    const dockerLog = join(root, 'docker.log');
    const { bin } = fakeDocker(root, dockerLog);
    writeFileSync(dockerLog, '');
    const before = new Set(readdirSync(join(repoRoot, 'bin/tmp')).filter((name) => name.startsWith(secretsPrefix)));
    const result = spawnSync(
      process.execPath,
      [
        '--input-type=module',
        '-e',
        `import(${JSON.stringify(pathToFileURL(harnessPath).href)}).then((module) => console.log(typeof module.createLifecycle, typeof module.runAcceptance))`,
      ],
      {
        cwd: root,
        env: {
          ...process.env,
          PATH: bin + ':' + process.env.PATH,
          CPAMP_LIFECYCLE_DOCKER_LOG: dockerLog,
          PLAYWRIGHT_MODULE: join(root, 'no-playwright'),
        },
        encoding: 'utf8',
        timeout: 60000,
      }
    );
    assert.equal(result.status, 0, result.stderr);
    assert.equal(result.stdout.trim(), 'function function');
    assert.equal(readFileSync(dockerLog, 'utf8'), '', 'importing the harness ran a command');
    const after = new Set(readdirSync(join(repoRoot, 'bin/tmp')).filter((name) => name.startsWith(secretsPrefix)));
    assert.deepEqual([...after].filter((name) => !before.has(name)), [], 'importing the harness created a secret directory');
  });
});
