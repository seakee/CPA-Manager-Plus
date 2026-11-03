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
import { basename, dirname, join, resolve } from 'node:path';
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

  const controlledRevision = 'f'.repeat(40);
  const controlledHead = controlledRevision.slice(0, 12);

  // The secrets directories this run may leave behind. Read from the repository's
  // scratch tree, which is where the script creates them; a checkout without that
  // directory has none.
  function secretDirectories() {
    const dir = join(repoRoot, 'bin/tmp');
    return existsSync(dir) ? readdirSync(dir).filter((name) => name.startsWith(secretsPrefix)) : [];
  }

  function savedLogNames(dir) {
    return existsSync(dir) ? readdirSync(dir).filter((name) => name.endsWith('.log')) : [];
  }

  // The adapter's daemon state: the containers `docker run -d` created and no
  // `docker rm -f` removed. The test seeds it with containers the run does not
  // own, so touching one would be visible here as well as in the command log.
  function daemonContainers(file) {
    return readFileSync(file, 'utf8').split('\n').filter(Boolean);
  }

  // A complete adapter for the runs that must take the script's own teardown. The
  // container listing is derived from the filter the script itself passes, so this
  // run's random project is enumerated without the test naming it in advance. The
  // fault switches are environment flags: one adapter serves every injected
  // failure, and each run is a controlled probe, not a Docker D run.
  function completeAdapter(root) {
    const bin = join(root, 'bin');
    mkdirSync(bin, { recursive: true });
    writeFileSync(
      join(bin, 'docker'),
      `#!/bin/sh
printf '%s\\n' "$*" >> "$CPAMP_LIFECYCLE_DOCKER_LOG"
case " $* " in
  " run -d --name "*)
    # The daemon owns the container the moment 'run -d' creates it; the CLI can
    # still exit nonzero (a refused start, a timeout, a lost reply). That is the
    # state cleanup has to survive, so the name is recorded here before the
    # injected failure, exactly as the created container would exist.
    name=$4
    printf '%s\\n' "$name" >> "$CPAMP_LIFECYCLE_CONTAINER_STATE"
    if [ -n "$CPAMP_FAKE_RUN_FAILS" ] && [ "\${name%-\${CPAMP_FAKE_RUN_FAILS}}" != "$name" ]; then
      echo 'Error response from daemon: the container was created but the process failed to start' >&2
      exit 1
    fi
    exit 0;;
  " rm -f "*)
    # Real removal semantics: only a name the daemon has is removed, and a name it
    # does not have gets the idempotent "No such container" answer.
    name=$3
    if [ "$CPAMP_FAKE_RM_MISSING" != 1 ] && grep -qxF "$name" "$CPAMP_LIFECYCLE_CONTAINER_STATE" 2>/dev/null; then
      grep -vxF "$name" "$CPAMP_LIFECYCLE_CONTAINER_STATE" > "\${CPAMP_LIFECYCLE_CONTAINER_STATE}.next"
      mv "\${CPAMP_LIFECYCLE_CONTAINER_STATE}.next" "$CPAMP_LIFECYCLE_CONTAINER_STATE"
      exit 0
    fi
    echo "Error response from daemon: No such container: $name" >&2
    exit 1;;
  *" image inspect "*)
    case "$*" in
      *python:3.13-alpine*) printf 'sha256:3333\\n'; exit 0;;
      *) printf '[{"Os":"linux","Architecture":"amd64","Id":"sha256:1111","Config":{"Labels":{"org.opencontainers.image.revision":"%s"}}}]\\n' "$FAKE_REVISION"; exit 0;;
    esac;;
  *" compose "*" ps -q cpamp-ingress"*) printf 'fake-ingress-id\\n'; exit 0;;
  *" ps -a "*)
    if [ "$CPAMP_FAKE_PS_FAILS" = 1 ]; then echo 'the container listing refused' >&2; exit 1; fi
    prefix=$(printf '%s\\n' "$*" | sed -n 's/.*name=\\([^ ]*\\).*/\\1/p')
    printf '%s\\n' "\${prefix}cpamp-manager-1" "\${prefix}cpamp-runtime-1" "\${prefix}cpamp-ingress-1" "\${prefix}cpa" "\${prefix}cpa-empty" "\${prefix}forwarder"
    exit 0;;
  " logs "*)
    if [ "$CPAMP_FAKE_LOGS_FAILS" = 1 ]; then echo 'the container log refused' >&2; exit 1; fi
    echo 'container log line'
    exit 0;;
  *" down "*)
    if [ "$CPAMP_FAKE_DOWN_FAILS" = 1 ]; then echo 'compose down refused' >&2; exit 1; fi
    exit 0;;
  *" inspect "*)
    printf '[{"NetworkSettings":{"Ports":{"18317/tcp":[{"HostIp":"127.0.0.1","HostPort":"%s"}]},"Networks":{"fake-net":{}}}}]\\n' "$FAKE_PORT"; exit 0;;
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
    return bin;
  }

  // One controlled run of the real CLI, from its prerequisites into its own
  // teardown. `logDir` lets a test place — or block — the saved log without
  // touching the shared directory real runs write to. `containers` seeds the
  // adapter's daemon state with containers this run does not own, so a cleanup
  // that reached one of them would be observable.
  async function runControlled(name, { env = {}, logDir: logDirOverride, containers = [] } = {}) {
    const caseDir = join(tempRoot, 'controlled', name);
    const out = join(caseDir, 'artifact');
    const logDir = logDirOverride || join(caseDir, 'logs');
    const dockerLog = join(caseDir, 'docker.log');
    const containerState = join(caseDir, 'containers.state');
    mkdirSync(caseDir, { recursive: true });
    writeFileSync(dockerLog, '');
    writeFileSync(containerState, containers.map((entry) => entry + '\n').join(''));
    const bin = completeAdapter(caseDir);
    // The bootstrap probe only needs an answer: the run stops at its first check,
    // which is long before a browser or a real Manager is needed.
    const server = createServer((request, response) => {
      response.writeHead(200, { 'content-type': 'application/json' });
      response.end('{}');
    });
    await new Promise((done) => server.listen(0, '127.0.0.1', done));
    const port = server.address().port;
    const before = new Set(secretDirectories());
    try {
      const child = spawn(
        process.execPath,
        [harnessPath, 'cpampd-probe-runtime', 'cpampd-probe-manager', 'cpampd-probe-ingress', out],
        {
          cwd: repoRoot,
          env: {
            ...process.env,
            PATH: bin + ':' + process.env.PATH,
            CPAMP_LIFECYCLE_DOCKER_LOG: dockerLog,
            CPAMP_LIFECYCLE_CONTAINER_STATE: containerState,
            CPAMP_D_ADOPT_LOG_DIR: logDir,
            PLAYWRIGHT_MODULE: join(caseDir, 'no-playwright'),
            FAKE_REVISION: controlledRevision,
            FAKE_PORT: String(port),
            ...env,
          },
          stdio: ['ignore', 'pipe', 'pipe'],
        }
      );
      const output = [];
      child.stdout.on('data', (chunk) => output.push(String(chunk)));
      child.stderr.on('data', (chunk) => output.push(String(chunk)));
      let timer;
      const exit = await Promise.race([
        waitForExit(child),
        new Promise((done, fail) => {
          timer = setTimeout(() => {
            child.kill('SIGKILL');
            fail(new Error('the controlled run did not exit'));
          }, 60000);
        }),
      ]);
      clearTimeout(timer);
      const reportPath = join(out, 'report.json');
      const report = existsSync(reportPath) ? JSON.parse(readFileSync(reportPath, 'utf8')) : null;
      return {
        exit,
        out,
        logDir,
        report,
        output: output.join(''),
        calls: readFileSync(dockerLog, 'utf8').trim().split('\n').filter(Boolean),
        containerState,
        survivedSecrets: secretDirectories().filter((entry) => !before.has(entry)),
      };
    } finally {
      await new Promise((done) => server.close(done));
    }
  }

  it('cleans up when the image precondition fails before any Docker resource exists', () => {
    const root = join(tempRoot, 'precondition');
    const out = join(root, 'artifact');
    const logDir = join(root, 'logs');
    const dockerLog = join(root, 'docker.log');
    const { bin } = fakeDocker(root, dockerLog);
    writeFileSync(dockerLog, '');
    const before = new Set(secretDirectories());
    const result = spawnSync(
      process.execPath,
      [harnessPath, 'cpampd-probe-runtime', 'cpampd-probe-manager', 'cpampd-probe-ingress', out],
      {
        cwd: repoRoot,
        env: {
          ...process.env,
          PATH: bin + ':' + process.env.PATH,
          CPAMP_LIFECYCLE_DOCKER_LOG: dockerLog,
          CPAMP_D_ADOPT_LOG_DIR: logDir,
          // The browser never starts; a module that cannot be resolved here must
          // not change what this probe observes.
          PLAYWRIGHT_MODULE: join(root, 'no-playwright'),
        },
        encoding: 'utf8',
        timeout: 60000,
      }
    );
    assert.equal(result.status, 1, 'a failed prerequisite must exit nonzero');
    const after = new Set(secretDirectories());
    assert.deepEqual([...after].filter((name) => !before.has(name)), [], 'a secret directory survived');
    const reportPath = join(out, 'report.json');
    assert.equal(existsSync(reportPath), true, 'the redacted artifact was not attempted');
    const report = JSON.parse(readFileSync(reportPath, 'utf8'));
    assert.equal(report.result, 'FAIL');
    assert.equal(report.secretAssertion, 'PASS');
    assert.equal(report.containerLogEvidence, 'PASS');
    assert.equal(report.checkRegistry, 'PASS');
    assert.match(report.failure, /No such image|image inspect|Command failed/);
    // The teardown ran no destructive command: no project was ever created, so
    // there is nothing to remove and nothing outside this run may be touched.
    const dockerCalls = readFileSync(dockerLog, 'utf8');
    for (const forbidden of ['prune', ' system ', ' rmi ', ' tag ', ' build ', ' down ', ' rm '])
      assert.equal(dockerCalls.includes(forbidden), false, 'the teardown ran `' + forbidden.trim() + '`');
    // The saved log is this run's own file, named for the run as well as the
    // head, and the probe removes only the file the report names.
    assert.equal(typeof report.logPath, 'string', 'the report does not name its saved log');
    const savedLog = resolve(repoRoot, report.logPath);
    assert.equal(dirname(savedLog), logDir);
    assert.match(basename(savedLog), /^d-adopt-unknown-[0-9a-f]{12}\.log$/);
    assert.match(readFileSync(savedLog, 'utf8'), /NOT RUN/, 'the saved log does not record this run');
    rmSync(savedLog);
    assert.deepEqual(savedLogNames(logDir), [], 'the probe left a saved log behind');
  });

  it('does not overwrite an artifact directory it did not create', () => {
    const root = join(tempRoot, 'artifact-exists');
    const out = join(root, 'artifact');
    const logDir = join(root, 'logs');
    const dockerLog = join(root, 'docker.log');
    mkdirSync(out, { recursive: true });
    writeFileSync(join(out, 'report.json'), '{"result":"PRIOR"}', { mode: 0o600 });
    const { bin } = fakeDocker(root, dockerLog);
    writeFileSync(dockerLog, '');
    const before = new Set(secretDirectories());
    const result = spawnSync(
      process.execPath,
      [harnessPath, 'cpampd-probe-runtime', 'cpampd-probe-manager', 'cpampd-probe-ingress', out],
      {
        cwd: repoRoot,
        env: {
          ...process.env,
          PATH: bin + ':' + process.env.PATH,
          CPAMP_LIFECYCLE_DOCKER_LOG: dockerLog,
          CPAMP_D_ADOPT_LOG_DIR: logDir,
          PLAYWRIGHT_MODULE: join(root, 'no-playwright'),
        },
        encoding: 'utf8',
        timeout: 60000,
      }
    );
    assert.equal(result.status, 1);
    assert.equal(readFileSync(join(out, 'report.json'), 'utf8'), '{"result":"PRIOR"}', 'an earlier artifact was overwritten');
    const after = new Set(secretDirectories());
    assert.deepEqual([...after].filter((name) => !before.has(name)), [], 'a secret directory survived');
    assert.equal(readFileSync(dockerLog, 'utf8').trim(), '', 'the refused run touched a command');
    // A refused run writes no report, so it saves no log either: the artifact it
    // did not own is the only thing its evidence could have touched.
    assert.deepEqual(savedLogNames(logDir), [], 'the refused run saved a log beside its own artifact');
  });

  it('removes the secret directory when Compose down itself fails', async () => {
    const run = await runControlled('teardown-cli', { env: { CPAMP_FAKE_DOWN_FAILS: '1' } });
    assert.equal(run.exit.signal, null);
    assert.equal(run.exit.code, 1, 'a failed teardown must exit nonzero');
    assert.deepEqual(run.survivedSecrets, [], 'a secret directory survived');
    const { report, calls } = run;
    assert.equal(report.result, 'FAIL', 'a failed teardown was reported as a PASS');
    // The container logs were read, so the secret assertion was allowed to claim
    // PASS despite an unrelated teardown failure.
    assert.equal(report.containerLogEvidence, 'PASS');
    assert.equal(report.secretAssertion, 'PASS');
    assert.deepEqual(
      report.teardown.map(({ step }) => step),
      ['compose down']
    );
    assert.match(report.teardown[0].message, /compose down refused/);
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
      assert.equal(calls.join('\n').includes(forbidden), false, 'the probe ran `' + forbidden.trim() + '`');
    // The saved log is this run's own file, and the test removes only that file.
    const savedLog = resolve(repoRoot, report.logPath);
    assert.equal(dirname(savedLog), run.logDir);
    assert.match(basename(savedLog), new RegExp('^d-adopt-' + controlledHead + '-[0-9a-f]{12}\\.log$'));
    assert.match(readFileSync(savedLog, 'utf8'), /FAIL/);
    rmSync(savedLog);
    assert.deepEqual(savedLogNames(run.logDir), []);
  });

  // `docker run -d` can create the container and still exit nonzero: a refused
  // start, a timeout, a lost reply. Compose does not own that container, so the
  // only handle cleanup has is the name the run registered before the call. The
  // adapter records creation and removal, so a container left behind is visible.
  it('removes the container a failed docker run created, for each container the run starts', async () => {
    const unrelated = 'cpamp-existing-user-container';
    for (const suffix of ['cpa', 'cpa-empty', 'forwarder']) {
      const run = await runControlled('run-failure-' + suffix, {
        env: { CPAMP_FAKE_RUN_FAILS: suffix },
        containers: [unrelated],
      });
      assert.equal(run.exit.signal, null, suffix + ': the run was killed');
      assert.equal(run.exit.code, 1, suffix + ': a failed docker run must fail the run');
      const project = /(cpampd-adopt-[0-9a-f]{12})-/.exec(run.calls.join('\n'))?.[1];
      assert(project, suffix + ': the run never named its project');
      const created = ['cpa', 'cpa-empty', 'forwarder']
        .map((entry) => project + '-' + entry)
        .filter((container) => run.calls.some((line) => line.startsWith('run -d --name ' + container + ' ')));
      assert(created.includes(project + '-' + suffix), suffix + ': the injected run created no container');
      for (const container of created) {
        assert(
          run.calls.some((line) => line === 'rm -f ' + container),
          suffix + ': cleanup did not remove ' + container
        );
      }
      assert.equal(
        run.calls.some((line) => line.includes(unrelated)),
        false,
        suffix + ': cleanup named a container outside this run'
      );
      assert.deepEqual(
        daemonContainers(run.containerState),
        [unrelated],
        suffix + ': a container this run created survived cleanup'
      );
      assert(
        run.calls.some((line) => line.startsWith('compose ') && line.includes(' down ')),
        suffix + ': the Compose project was not taken down'
      );
      assert.deepEqual(run.survivedSecrets, [], suffix + ': a secret directory survived');
      assert.equal(run.report?.result, 'FAIL', suffix + ': the failed run was reported as a PASS');
    }
  });

  // Docker answers a container it does not have with `No such container`. A name
  // that is already gone — the container was never created, or was already
  // removed — leaves nothing to clean, so that answer must not fail the teardown.
  it('treats a container Docker no longer has as already removed', async () => {
    const run = await runControlled('container-missing', { env: { CPAMP_FAKE_RM_MISSING: '1' } });
    assert.equal(run.exit.signal, null);
    assert.equal(run.exit.code, 1, 'the run still fails on its own journey');
    assert.equal(run.report?.result, 'FAIL');
    assert.deepEqual(
      (run.report?.teardown || []).map(({ step }) => step),
      [],
      'a container Docker does not have was recorded as a teardown failure'
    );
    const project = /(cpampd-adopt-[0-9a-f]{12})-/.exec(run.calls.join('\n'))?.[1];
    assert(project, 'the run never named its project');
    for (const entry of ['cpa', 'cpa-empty', 'forwarder']) {
      assert(
        run.calls.some((line) => line === 'rm -f ' + project + '-' + entry),
        entry + ': cleanup did not attempt the exact name'
      );
    }
    assert.deepEqual(run.survivedSecrets, [], 'a secret directory survived');
  });

  it('fails the persisted report when the log directory cannot be created', async () => {
    const caseDir = join(tempRoot, 'controlled', 'log-dir-failure');
    mkdirSync(caseDir, { recursive: true });
    // A regular file where the log directory's parent must be: creating the
    // directory itself fails, which used to happen after the report had already
    // claimed its result on disk.
    writeFileSync(join(caseDir, 'logs'), 'not a directory');
    const blocked = join(caseDir, 'logs', 'nested');
    const run = await runControlled('log-dir-failure', { logDir: blocked });
    assert.equal(run.exit.signal, null);
    assert.equal(run.exit.code, 1, 'a run that saved no log must exit nonzero');
    assert.notEqual(run.report, null, 'the run wrote no report at all');
    assert.equal(run.report.result, 'FAIL', 'the persisted report claims a result the run did not have');
    assert.equal(typeof run.report.logWriteFailure, 'string', 'the persisted report does not say the log is missing');
    assert.match(run.report.logWriteFailure, /ENOTDIR|not a directory/i);
    assert.equal(run.report.logPath, undefined, 'a report that saved no log names one');
  });

  it('fails the persisted report when the saved log cannot be written', async () => {
    const caseDir = join(tempRoot, 'controlled', 'log-write-failure');
    const logDir = join(caseDir, 'logs');
    mkdirSync(logDir, { recursive: true });
    chmodSync(logDir, 0o500);
    try {
      const run = await runControlled('log-write-failure', { logDir });
      assert.equal(run.exit.code, 1, 'a run that saved no log must exit nonzero');
      assert.notEqual(run.report, null, 'the run wrote no report at all');
      assert.equal(run.report.result, 'FAIL', 'the persisted report claims a result the run did not have');
      assert.equal(typeof run.report.logWriteFailure, 'string', 'the persisted report does not say the log is missing');
      assert.match(run.report.logWriteFailure, /EACCES|permission denied/i);
      assert.equal(run.report.logPath, undefined, 'a report that saved no log names one');
      assert.deepEqual(savedLogNames(logDir), []);
    } finally {
      chmodSync(logDir, 0o700);
    }
  });

  it('does not assert a secret-free run when the container listing cannot be read', async () => {
    const run = await runControlled('container-listing-failure', { env: { CPAMP_FAKE_PS_FAILS: '1' } });
    assert.equal(run.exit.code, 1);
    const { report } = run;
    assert.equal(report.result, 'FAIL');
    // The assertion scans every container log of the run; a listing that failed is
    // not a listing that found nothing, so it cannot end as a PASS.
    assert.notEqual(report.secretAssertion, 'PASS', 'a run with no container logs asserted a secret-free PASS');
    assert.equal(report.containerLogEvidence, 'FAIL');
    assert.match(report.containerLogFailure, /docker ps -a failed/);
    assert.match(report.secretFailure, /container log evidence unavailable/);
    assert.deepEqual(
      report.teardown.map(({ step }) => step),
      ['container logs']
    );
    // The evidence failure does not stop cleanup: the containers and the project
    // are still removed, in their own steps.
    assert(run.calls.some((line) => line.startsWith('rm -f ')), 'the owned containers were not removed');
    assert(
      run.calls.some((line) => line.startsWith('compose ') && line.includes(' down ')),
      'the owned project was not taken down'
    );
    assert.deepEqual(run.survivedSecrets, []);
  });

  it('does not assert a secret-free run when a container log cannot be read', async () => {
    const run = await runControlled('container-log-failure', { env: { CPAMP_FAKE_LOGS_FAILS: '1' } });
    assert.equal(run.exit.code, 1);
    const { report } = run;
    assert.equal(report.result, 'FAIL');
    assert.notEqual(report.secretAssertion, 'PASS', 'a run that could not read a log asserted a secret-free PASS');
    assert.equal(report.containerLogEvidence, 'FAIL');
    assert.match(report.containerLogFailure, /docker logs .+ failed/);
    assert.match(report.secretFailure, /container log evidence unavailable/);
    assert.deepEqual(
      report.teardown.map(({ step }) => step),
      ['container logs']
    );
    assert(run.calls.some((line) => line.startsWith('rm -f ')), 'the owned containers were not removed');
    assert(
      run.calls.some((line) => line.startsWith('compose ') && line.includes(' down ')),
      'the owned project was not taken down'
    );
    assert.deepEqual(run.survivedSecrets, []);
  });

  it('saves a distinct log for each run of the same head', async () => {
    const caseDir = join(tempRoot, 'controlled', 'same-head');
    const logDir = join(caseDir, 'logs');
    mkdirSync(logDir, { recursive: true });
    // A log an earlier run left behind: neither run may overwrite it, and the
    // cleanup below removes only the two files these two runs created.
    const unrelated = join(logDir, 'd-adopt-' + controlledHead + '-000000000000.log');
    writeFileSync(unrelated, 'an earlier run\n', { mode: 0o600 });
    const first = await runControlled('same-head-1', { logDir });
    const second = await runControlled('same-head-2', { logDir });
    for (const run of [first, second]) {
      assert.equal(run.exit.code, 1);
      assert.equal(typeof run.report.logPath, 'string', 'the report does not name its saved log');
      assert.equal(dirname(resolve(repoRoot, run.report.logPath)), logDir);
    }
    const firstLog = resolve(repoRoot, first.report.logPath);
    const secondLog = resolve(repoRoot, second.report.logPath);
    assert.notEqual(firstLog, secondLog, 'two runs of the same head share one log file');
    assert.match(readFileSync(firstLog, 'utf8'), /FAIL/);
    assert.match(readFileSync(secondLog, 'utf8'), /FAIL/);
    assert.equal(readFileSync(unrelated, 'utf8'), 'an earlier run\n', 'a run overwrote an unrelated log');
    assert.deepEqual(
      savedLogNames(logDir).sort(),
      [basename(unrelated), basename(firstLog), basename(secondLog)].sort()
    );
    rmSync(firstLog);
    rmSync(secondLog);
    assert.equal(existsSync(unrelated), true, 'cleanup removed a log it did not create');
    rmSync(unrelated);
  });

  it('imports without creating anything or touching a command', () => {
    const root = join(tempRoot, 'import');
    const dockerLog = join(root, 'docker.log');
    const { bin } = fakeDocker(root, dockerLog);
    writeFileSync(dockerLog, '');
    const before = new Set(secretDirectories());
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
    const after = new Set(secretDirectories());
    assert.deepEqual([...after].filter((name) => !before.has(name)), [], 'importing the harness created a secret directory');
  });
});
