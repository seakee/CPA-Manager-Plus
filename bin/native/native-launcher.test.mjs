import assert from 'node:assert/strict';
import { execFileSync, spawn, spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import {
  chmodSync,
  copyFileSync,
  existsSync,
  lstatSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  realpathSync,
  rmSync,
  statSync,
  symlinkSync,
  writeFileSync,
} from 'node:fs';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { afterEach, describe, it } from 'node:test';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(__dirname, '../..');
const launcherScript = path.join(repoRoot, 'bin/native/cpa-manager-plus-launcher');
const ctlScript = path.join(repoRoot, 'bin/native/cpa-manager-plusctl.sh');
const systemdUnitTemplate = path.join(repoRoot, 'bin/native/cpa-manager-plus.service');
const packageNativeScript = path.join(repoRoot, 'bin/release/package-native.sh');

const tempDirs = [];
const activeProcesses = [];

const makeTempDir = (prefix) => {
  const dir = realpathSync(mkdtempSync(path.join(os.tmpdir(), prefix)));
  tempDirs.push(dir);
  return dir;
};

/**
 * Allocates N distinct free loopback ports on 127.0.0.1, guaranteed never to be 18317.
 */
const allocateLoopbackPorts = async (count) => {
  const servers = [];
  const ports = [];
  try {
    while (ports.length < count) {
      const server = net.createServer();
      await new Promise((resolve, reject) => {
        server.once('error', reject);
        server.listen(0, '127.0.0.1', resolve);
      });
      const address = server.address();
      const port = typeof address === 'object' && address ? address.port : 0;
      servers.push(server);
      if (port > 0 && port !== 18317 && !ports.includes(port)) {
        ports.push(port);
      }
    }
  } finally {
    await Promise.all(
      servers.map(
        (s) =>
          new Promise((resolve) => {
            s.close(() => resolve());
          })
      )
    );
  }
  return ports;
};

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

const isPidAlive = (pid) => {
  if (!Number.isInteger(pid) || pid <= 0) return false;
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
};

/**
 * Creates a full v2 Native layout with lightweight Node.js mock binaries for
 * runtime-supervisor, manager, ingress, and cli-proxy-api.
 */
const createMockNativeHome = (baseDir) => {
  const homeDir = path.join(baseDir, 'cpamp-home');
  const binDir = path.join(homeDir, 'bin');
  const dataDir = path.join(homeDir, 'data');
  const runDir = path.join(homeDir, 'run');
  const logDir = path.join(homeDir, 'logs');
  const shareRuntimeDir = path.join(homeDir, 'share/cpamp/runtime');

  mkdirSync(binDir, { recursive: true });
  mkdirSync(dataDir, { recursive: true, mode: 0o700 });
  mkdirSync(runDir, { recursive: true, mode: 0o700 });
  mkdirSync(logDir, { recursive: true, mode: 0o700 });
  mkdirSync(shareRuntimeDir, { recursive: true });

  copyFileSync(launcherScript, path.join(binDir, 'cpa-manager-plus-launcher'));
  chmodSync(path.join(binDir, 'cpa-manager-plus-launcher'), 0o755);
  copyFileSync(launcherScript, path.join(homeDir, 'cpa-manager-plus-launcher'));
  chmodSync(path.join(homeDir, 'cpa-manager-plus-launcher'), 0o755);
  copyFileSync(ctlScript, path.join(homeDir, 'cpa-manager-plusctl'));
  chmodSync(path.join(homeDir, 'cpa-manager-plusctl'), 0o755);
  copyFileSync(systemdUnitTemplate, path.join(homeDir, 'cpa-manager-plus.service'));
  chmodSync(path.join(homeDir, 'cpa-manager-plus.service'), 0o644);

  const cpaBin = path.join(binDir, 'cli-proxy-api');
  const cpaContent = '#!/usr/bin/env bash\nexit 0\n';
  writeFileSync(cpaBin, cpaContent, { mode: 0o755 });
  chmodSync(cpaBin, 0o755);
  const cpaSha = createHash('sha256').update(cpaContent).digest('hex');

  writeFileSync(
    path.join(shareRuntimeDir, 'cpa-artifact.json'),
    JSON.stringify({
      schemaVersion: 1,
      engine: 'cpa',
      version: '8.0.13',
      artifactId: `sha256:${cpaSha}`,
    }) + '\n',
    { mode: 0o444 }
  );
  copyFileSync(
    path.join(repoRoot, 'docker/runtime/config.v8.default.seed.yaml'),
    path.join(shareRuntimeDir, 'config.seed.yaml')
  );

  // Shared mock service script driven by role argument
  const mockServiceJs = path.join(binDir, 'mock-service.mjs');
  writeFileSync(
    mockServiceJs,
    [
      'import { appendFileSync, writeFileSync } from "node:fs";',
      'import http from "node:http";',
      'import path from "node:path";',
      '',
      'const role = process.argv[2];',
      'const eventLog = process.env.MOCK_EVENT_LOG || "";',
      'const envDumpDir = process.env.MOCK_ENV_DUMP_DIR || "";',
      'const recordEvent = (event) => {',
      '  if (eventLog) {',
      '    appendFileSync(eventLog, JSON.stringify({ ts: Date.now(), role, pid: process.pid, event }) + "\\n");',
      '  }',
      '};',
      '',
      'recordEvent("start");',
      'if (envDumpDir) {',
      '  writeFileSync(path.join(envDumpDir, `${role}-env.json`), JSON.stringify({ pid: process.pid, cwd: process.cwd(), env: process.env }, null, 2));',
      '}',
      '',
      'const failRole = process.env.MOCK_FAIL_ROLE || "";',
      'if (failRole === role) {',
      '  recordEvent("forced-failure");',
      '  process.exit(1);',
      '}',
      '',
      'const delayMs = Number(process.env[`MOCK_DELAY_${role.toUpperCase().replace(/-/g, "_")}_MS`] || "0");',
      'let ready = delayMs <= 0;',
      'if (!ready) {',
      '  setTimeout(() => {',
      '    ready = true;',
      '    recordEvent("ready");',
      '  }, delayMs);',
      '} else {',
      '  recordEvent("ready");',
      '}',
      '',
      'const parseHostPort = (addr) => {',
      '  const idx = addr.lastIndexOf(":");',
      '  return { host: addr.slice(0, idx) || "127.0.0.1", port: Number(addr.slice(idx + 1)) };',
      '};',
      '',
      'const servers = [];',
      'if (role === "runtime-supervisor") {',
      '  const { host, port } = parseHostPort(process.env.CPAMP_RUNTIME_ADDR);',
      '  const expectedToken = process.env.CPAMP_RUNTIME_TOKEN || "";',
      '  const srv = http.createServer((req, res) => {',
      '    if (!ready) { res.writeHead(503); res.end("starting"); return; }',
      '    if (req.url === "/v1/runtime/status") {',
      '      const auth = req.headers["authorization"] || "";',
      '      if (auth !== `Bearer ${expectedToken}`) {',
      '        res.writeHead(401, { "Content-Type": "application/json" });',
      '        res.end(JSON.stringify({ error: { code: "unauthorized" } }));',
      '        return;',
      '      }',
      '      res.writeHead(200, { "Content-Type": "application/json" });',
      '      res.end(JSON.stringify({ protocolVersion: "v1", runtimeIdentity: process.env.CPAMP_RUNTIME_IDENTITY, runtimeGeneration: 1, state: "ready" }));',
      '      return;',
      '    }',
      '    res.writeHead(404); res.end();',
      '  });',
      '  srv.listen(port, host);',
      '  servers.push(srv);',
      '} else if (role === "manager") {',
      '  const { host, port } = parseHostPort(process.env.HTTP_ADDR);',
      '  const discoveryOnly = process.env.MOCK_MANAGER_DISCOVERY_ONLY === "1";',
      '  const srv = http.createServer((req, res) => {',
      '    if (!ready) { res.writeHead(503); res.end("starting"); return; }',
      '    if (req.url === "/health") {',
      '      if (discoveryOnly) {',
      '        res.writeHead(503, { "Content-Type": "application/json" });',
      '        res.end(JSON.stringify({ ok: false, status: "not_ready" }));',
      '        return;',
      '      }',
      '      res.writeHead(200, { "Content-Type": "application/json" });',
      '      res.end(JSON.stringify({ ok: true }));',
      '      return;',
      '    }',
      '    if (req.url === "/.well-known/cpamp-bootstrap") {',
      '      res.writeHead(200, { "Content-Type": "application/json" });',
      '      res.end(JSON.stringify({ contractVersion: "v1", status: "fresh_claim_required" }));',
      '      return;',
      '    }',
      '    res.writeHead(404); res.end();',
      '  });',
      '  srv.listen(port, host);',
      '  servers.push(srv);',
      '} else if (role === "ingress") {',
      '  const pub = parseHostPort(process.env.CPAMP_INGRESS_ADDR);',
      '  const ctrl = parseHostPort(process.env.CPAMP_INGRESS_CONTROL_ADDR);',
      '  const pubSrv = http.createServer((req, res) => {',
      '    if (!ready) { res.writeHead(503); res.end("starting"); return; }',
      '    if (req.url === "/health") {',
      '      res.writeHead(200, { "Content-Type": "application/json" });',
      '      res.end(JSON.stringify({ ok: true, via: "ingress" }));',
      '      return;',
      '    }',
      '    res.writeHead(200); res.end("ok");',
      '  });',
      '  pubSrv.listen(pub.port, pub.host === "0.0.0.0" ? "127.0.0.1" : pub.host);',
      '  servers.push(pubSrv);',
      '  const ctrlSrv = http.createServer((req, res) => {',
      '    if (!ready) { res.writeHead(503); res.end("starting"); return; }',
      '    res.writeHead(200, { "Content-Type": "application/json" });',
      '    res.end(JSON.stringify({ installationId: "" }));',
      '  });',
      '  ctrlSrv.listen(ctrl.port, ctrl.host);',
      '  servers.push(ctrlSrv);',
      '}',
      '',
      'const shutdown = (sig) => {',
      '  recordEvent(`sig:${sig}`);',
      '  Promise.all(servers.map((s) => new Promise((r) => s.close(() => r())))).finally(() => {',
      '    recordEvent("exit");',
      '    process.exit(0);',
      '  });',
      '};',
      'process.on("SIGTERM", () => shutdown("SIGTERM"));',
      'process.on("SIGINT", () => shutdown("SIGINT"));',
      '',
    ].join('\n'),
    { mode: 0o644 }
  );

  const writeWrapper = (binName, role) => {
    const target = path.join(binDir, binName);
    writeFileSync(
      target,
      `#!/usr/bin/env bash\nexec "${process.execPath}" "${mockServiceJs}" "${role}" "$@"\n`,
      { mode: 0o755 }
    );
    chmodSync(target, 0o755);
  };

  writeWrapper('cpa-manager-plus-runtime-supervisor', 'runtime-supervisor');
  writeWrapper('cpa-manager-plus', 'manager');
  writeWrapper('cpa-manager-plus-ingress', 'ingress');

  return { homeDir, binDir, dataDir, runDir, logDir, cpaBin, cpaSha };
};

const buildPortEnv = async (homeDir, overrides = {}) => {
  const [ingressPort, runtimePort, managerPort, controlPort, cpaPort] =
    await allocateLoopbackPorts(5);
  return {
    ...process.env,
    CPA_MANAGER_PLUS_HOME: homeDir,
    HTTP_ADDR: `127.0.0.1:${ingressPort}`,
    CPAMP_RUNTIME_ADDR: `127.0.0.1:${runtimePort}`,
    CPAMP_MANAGER_ADDR: `127.0.0.1:${managerPort}`,
    CPAMP_INGRESS_CONTROL_ADDR: `127.0.0.1:${controlPort}`,
    CPAMP_RUNTIME_CPA_ADDR: `127.0.0.1:${cpaPort}`,
    CPAMP_STARTUP_TIMEOUT_SEC: '10',
    CPAMP_STOP_TIMEOUT_SEC: '5',
    ...overrides,
    _ports: { ingressPort, runtimePort, managerPort, controlPort, cpaPort },
  };
};

const waitForFile = async (filePath, timeoutMs = 8000) => {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (existsSync(filePath)) return true;
    await sleep(50);
  }
  return false;
};

afterEach(() => {
  for (const proc of activeProcesses.splice(0)) {
    try {
      proc.kill('SIGTERM');
    } catch {
      // ignore
    }
  }
  for (const dir of tempDirs.splice(0)) {
    try {
      const ctl = path.join(dir, 'cpamp-home/cpa-manager-plusctl');
      if (existsSync(ctl)) {
        spawnSync('bash', [ctl, 'stop'], {
          env: { ...process.env, CPA_MANAGER_PLUS_HOME: path.join(dir, 'cpamp-home') },
          encoding: 'utf8',
        });
      }
    } catch {
      // ignore
    }
    rmSync(dir, { force: true, recursive: true });
  }
});

describe('v2 Native linux/amd64 3-process launcher, ctl, and packaging', () => {
  it('generates a 0600 runtime token file and wires all inter-process loopback env vars', async () => {
    const baseDir = makeTempDir('cpamp-native-env-');
    const { homeDir, binDir, dataDir, runDir, cpaBin } = createMockNativeHome(baseDir);
    const envDumpDir = path.join(baseDir, 'env-dumps');
    mkdirSync(envDumpDir, { recursive: true });

    const rawEnv = await buildPortEnv(homeDir, { MOCK_ENV_DUMP_DIR: envDumpDir });
    const { _ports: ports, ...env } = rawEnv;

    const proc = spawn('bash', [path.join(binDir, 'cpa-manager-plus-launcher')], {
      env,
      stdio: 'ignore',
    });
    activeProcesses.push(proc);

    const readyFile = path.join(runDir, 'launcher.ready');
    assert.equal(await waitForFile(readyFile), true, 'launcher.ready should be created');

    const tokenFile = path.join(runDir, 'runtime-token');
    assert.equal(existsSync(tokenFile), true, 'runtime-token must exist');
    assert.equal(lstatSync(tokenFile).isSymbolicLink(), false);
    assert.equal(statSync(tokenFile).mode & 0o777, 0o600, 'runtime-token must have mode 0600');
    const tokenValue = readFileSync(tokenFile, 'utf8');
    assert.match(tokenValue, /^[0-9a-f]{64}$/, 'generated token must be 64 hex chars');

    const runtimeDump = JSON.parse(
      readFileSync(path.join(envDumpDir, 'runtime-supervisor-env.json'), 'utf8')
    );
    const managerDump = JSON.parse(
      readFileSync(path.join(envDumpDir, 'manager-env.json'), 'utf8')
    );
    const ingressDump = JSON.parse(
      readFileSync(path.join(envDumpDir, 'ingress-env.json'), 'utf8')
    );

    // Verify Runtime Supervisor env wiring
    assert.equal(runtimeDump.env.CPAMP_RUNTIME_ADDR, `127.0.0.1:${ports.runtimePort}`);
    assert.equal(runtimeDump.env.CPAMP_RUNTIME_IDENTITY, 'embedded-default');
    assert.equal(runtimeDump.env.CPAMP_RUNTIME_TOKEN, tokenValue);
    assert.equal(runtimeDump.env.CPAMP_RUNTIME_TOKEN_FILE, tokenFile);
    assert.equal(
      runtimeDump.env.CPAMP_RUNTIME_JOURNAL_PATH,
      path.join(dataDir, 'runtime/supervisor/operations.sqlite')
    );
    assert.equal(runtimeDump.env.CPAMP_RUNTIME_CPA_ADDR, `127.0.0.1:${ports.cpaPort}`);
    assert.equal(runtimeDump.env.CPAMP_CPA_EXECUTABLE, cpaBin);
    assert.equal(
      runtimeDump.env.CPAMP_CPA_ARTIFACT_MANIFEST,
      path.join(homeDir, 'share/cpamp/runtime/cpa-artifact.json')
    );

    // Verify Manager Server env wiring
    assert.equal(managerDump.env.HTTP_ADDR, `127.0.0.1:${ports.managerPort}`);
    assert.equal(managerDump.env.USAGE_DATA_DIR, dataDir);
    assert.equal(managerDump.env.USAGE_DB_PATH, path.join(dataDir, 'usage.sqlite'));
    assert.equal(managerDump.env.CPA_MANAGER_DATA_KEY_PATH, path.join(dataDir, 'data.key'));
    assert.equal(managerDump.env.CPA_UPSTREAM_URL, `http://127.0.0.1:${ports.cpaPort}`);
    assert.equal(managerDump.env.CPAMP_RUNTIME_URL, `http://127.0.0.1:${ports.runtimePort}`);
    assert.equal(managerDump.env.CPAMP_RUNTIME_TOKEN_FILE, tokenFile);
    assert.equal(
      managerDump.env.CPAMP_INGRESS_CONTROL_URL,
      `http://127.0.0.1:${ports.controlPort}`
    );
    // Compose parity: an unset public origin must stay unset so remote browsers
    // are validated against their own Host instead of 127.0.0.1.
    assert.equal(managerDump.env.CPAMP_PUBLIC_ORIGIN, undefined);
    // The explicit runtime secret must never reach Manager or Ingress.
    assert.equal(managerDump.env.CPAMP_RUNTIME_TOKEN, undefined);

    // Verify Ingress env wiring
    assert.equal(ingressDump.env.CPAMP_INGRESS_ADDR, `127.0.0.1:${ports.ingressPort}`);
    assert.equal(ingressDump.env.CPAMP_MANAGER_URL, `http://127.0.0.1:${ports.managerPort}`);
    assert.equal(ingressDump.env.CPAMP_GATEWAY_URL, `http://127.0.0.1:${ports.cpaPort}`);
    assert.equal(ingressDump.env.CPAMP_INGRESS_CONTROL_ADDR, `127.0.0.1:${ports.controlPort}`);
    assert.equal(ingressDump.env.CPAMP_RUNTIME_TOKEN, undefined);

    // The bootstrap Gateway seed must never disable CPA client authentication.
    const gatewayConfig = readFileSync(
      path.join(dataDir, 'runtime/gateway/config.yaml'),
      'utf8'
    );
    assert.match(gatewayConfig, /api-keys: \["[0-9a-f]{64}"\]/);
    assert.doesNotMatch(gatewayConfig, /api-keys: \[\]/);
    assert.equal(gatewayConfig.includes(`port: ${ports.cpaPort}`), true);
    const bootstrapKeyFile = path.join(runDir, 'gateway-bootstrap-key');
    assert.equal(existsSync(bootstrapKeyFile), true);
    assert.equal(statSync(bootstrapKeyFile).mode & 0o777, 0o600);
    assert.match(readFileSync(bootstrapKeyFile, 'utf8'), /^[0-9a-f]{64}$/);

    proc.kill('SIGTERM');
    const [exitCode] = await new Promise((resolve) =>
      proc.once('exit', (code, sig) => resolve([code, sig]))
    );
    assert.equal(exitCode, 0);

    // Token file is preserved across shutdown
    assert.equal(readFileSync(tokenFile, 'utf8'), tokenValue);
  });

  it('rejects non-loopback internal addresses and port collisions fail-closed', async () => {
    const baseDir = makeTempDir('cpamp-native-loopback-');
    const { homeDir, binDir } = createMockNativeHome(baseDir);
    const rawEnv = await buildPortEnv(homeDir);
    const { _ports: ports, ...env } = rawEnv;

    const nonLoopback = spawnSync('bash', [path.join(binDir, 'cpa-manager-plus-launcher')], {
      env: { ...env, CPAMP_MANAGER_ADDR: `0.0.0.0:${ports.managerPort}` },
      encoding: 'utf8',
    });
    assert.notEqual(nonLoopback.status, 0);
    assert.match(nonLoopback.stderr, /Refusing non-loopback internal address/);

    const collision = spawnSync('bash', [path.join(binDir, 'cpa-manager-plus-launcher')], {
      env: { ...env, CPAMP_MANAGER_ADDR: `127.0.0.1:${ports.runtimePort}` },
      encoding: 'utf8',
    });
    assert.notEqual(collision.status, 0);
    assert.match(collision.stderr, /Port collision detected/);
  });

  it('refuses to start when an internal port is already bound by another process', async () => {
    const baseDir = makeTempDir('cpamp-native-prebind-');
    const { homeDir, binDir } = createMockNativeHome(baseDir);
    const rawEnv = await buildPortEnv(homeDir);
    const { _ports: _ports, ...env } = rawEnv;

    const squatter = net.createServer();
    await new Promise((resolve, reject) => {
      squatter.once('error', reject);
      squatter.listen(0, '127.0.0.1', resolve);
    });
    const squattedPort = squatter.address().port;
    try {
      const res = spawnSync('bash', [path.join(binDir, 'cpa-manager-plus-launcher')], {
        env: { ...env, CPAMP_RUNTIME_ADDR: `127.0.0.1:${squattedPort}` },
        encoding: 'utf8',
      });
      assert.notEqual(res.status, 0);
      assert.match(res.stderr, /already bound/);
    } finally {
      await new Promise((resolve) => squatter.close(resolve));
    }
  });

  it('requires root before dropping CPA to a dedicated identity', async () => {
    if (process.geteuid && process.geteuid() === 0) {
      return;
    }
    const baseDir = makeTempDir('cpamp-native-cpa-drop-');
    const { homeDir, binDir } = createMockNativeHome(baseDir);
    const rawEnv = await buildPortEnv(homeDir);
    const { _ports: _ports, ...env } = rawEnv;

    const res = spawnSync('bash', [path.join(binDir, 'cpa-manager-plus-launcher')], {
      env: { ...env, CPAMP_RUNTIME_CPA_UID: '10001', CPAMP_RUNTIME_CPA_GID: '10001' },
      encoding: 'utf8',
    });
    assert.notEqual(res.status, 0);
    assert.match(res.stderr, /requires the launcher to run as root/);
  });

  it('keeps the runtime bearer out of every probe process argv', async () => {
    for (const implementation of ['curl', 'node']) {
      const baseDir = makeTempDir(`cpamp-native-argv-${implementation}-`);
      const { homeDir, binDir, runDir } = createMockNativeHome(baseDir);
      const toolsDir = path.join(baseDir, 'tools');
      const argvLog = path.join(baseDir, 'probe-argv.log');
      const stdinLog = path.join(baseDir, 'probe-stdin.log');
      mkdirSync(toolsDir, { recursive: true });

      const realBin = execFileSync('bash', ['-lc', `command -v ${implementation}`], {
        encoding: 'utf8',
      }).trim();
      assert.notEqual(realBin, '', `missing ${implementation}`);

      const wrapperPath = path.join(toolsDir, implementation);
      writeFileSync(
        wrapperPath,
        [
          '#!/usr/bin/env bash',
          `printf '%s\\n' "$*" >> "\${CPAMP_ARGV_LOG}"`,
          `tee -a "\${CPAMP_STDIN_LOG}" | "${realBin}" "$@"`,
          '',
        ].join('\n'),
        { mode: 0o755 }
      );
      chmodSync(wrapperPath, 0o755);

      const rawEnv = await buildPortEnv(homeDir, {
        CPAMP_PROBE_IMPLEMENTATION: implementation,
        CPAMP_ARGV_LOG: argvLog,
        CPAMP_STDIN_LOG: stdinLog,
        PATH: `${toolsDir}:${process.env.PATH}`,
      });
      const { _ports: _ports, ...env } = rawEnv;

      const proc = spawn('bash', [path.join(binDir, 'cpa-manager-plus-launcher')], {
        env,
        stdio: 'ignore',
      });
      activeProcesses.push(proc);

      assert.equal(await waitForFile(path.join(runDir, 'launcher.ready')), true);
      const tokenValue = readFileSync(path.join(runDir, 'runtime-token'), 'utf8');
      assert.equal(existsSync(argvLog), true, 'the probe wrapper must have run');
      const argvLines = readFileSync(argvLog, 'utf8').split('\n');
      for (const line of argvLines) {
        assert.equal(
          line.includes(tokenValue),
          false,
          `runtime token leaked into ${implementation} argv: ${line}`
        );
      }
      const stdinCapture = readFileSync(stdinLog, 'utf8');
      assert.equal(
        stdinCapture.includes(tokenValue),
        true,
        `runtime token must travel over ${implementation} stdin`
      );

      proc.kill('SIGTERM');
      await new Promise((resolve) => proc.once('exit', resolve));
    }
  });

  it('ignores SIGHUP instead of stopping the whole stack', async () => {
    const baseDir = makeTempDir('cpamp-native-hup-');
    const { homeDir, binDir, runDir } = createMockNativeHome(baseDir);
    const rawEnv = await buildPortEnv(homeDir);
    const { _ports: _ports, ...env } = rawEnv;

    const proc = spawn('bash', [path.join(binDir, 'cpa-manager-plus-launcher')], {
      env,
      stdio: 'ignore',
    });
    activeProcesses.push(proc);

    const readyFile = path.join(runDir, 'launcher.ready');
    assert.equal(await waitForFile(readyFile), true);
    const ready = Object.fromEntries(
      readFileSync(readyFile, 'utf8')
        .trim()
        .split('\n')
        .map((line) => line.split('='))
    );

    proc.kill('SIGHUP');
    await sleep(600);

    assert.equal(isPidAlive(Number(ready.launcher_pid)), true);
    assert.equal(isPidAlive(Number(ready.runtime_pid)), true);
    assert.equal(isPidAlive(Number(ready.manager_pid)), true);
    assert.equal(isPidAlive(Number(ready.ingress_pid)), true);
    assert.equal(existsSync(readyFile), true);

    proc.kill('SIGTERM');
    const [exitCode] = await new Promise((resolve) => proc.once('exit', (code) => resolve([code])));
    assert.equal(exitCode, 0);
  });

  it('stops an orphaned CPA child that outlives the Supervisor on shutdown', async () => {
    const baseDir = makeTempDir('cpamp-native-orphan-');
    const { homeDir, binDir, runDir } = createMockNativeHome(baseDir);
    const rawEnv = await buildPortEnv(homeDir, { CPAMP_STOP_TIMEOUT_SEC: '5' });
    const { _ports: ports, ...env } = rawEnv;

    const proc = spawn('bash', [path.join(binDir, 'cpa-manager-plus-launcher')], {
      env,
      stdio: 'ignore',
    });
    activeProcesses.push(proc);
    assert.equal(await waitForFile(path.join(runDir, 'launcher.ready')), true);

    // The Supervisor's Close contract never stops the CPA child; a container
    // runtime reaps it, so the native launcher must.
    const orphan = spawn(
      process.execPath,
      [
        '-e',
        `const http = require("node:http");
         http.createServer((req, res) => res.end("cpa")).listen(${ports.cpaPort}, "127.0.0.1");`,
      ],
      { stdio: 'ignore' }
    );
    try {
      const deadline = Date.now() + 5000;
      let listening = false;
      while (Date.now() < deadline) {
        try {
          const res = await fetch(`http://127.0.0.1:${ports.cpaPort}/`);
          await res.text();
          listening = true;
          break;
        } catch {
          await sleep(50);
        }
      }
      assert.equal(listening, true, 'orphan CPA listener should be up');

      proc.kill('SIGTERM');
      const [exitCode] = await new Promise((resolve) =>
        proc.once('exit', (code) => resolve([code]))
      );
      assert.equal(exitCode, 0);
      assert.equal(isPidAlive(orphan.pid), false, 'orphaned CPA listener must be reaped');
    } finally {
      try {
        orphan.kill('SIGKILL');
      } catch {
        // ignore
      }
    }
  });

  it('enforces dependency startup order (Runtime -> Manager -> Ingress) and reverse shutdown order on SIGTERM', async () => {
    const baseDir = makeTempDir('cpamp-native-order-');
    const { homeDir, binDir, runDir } = createMockNativeHome(baseDir);
    const eventLog = path.join(baseDir, 'events.jsonl');

    const rawEnv = await buildPortEnv(homeDir, {
      MOCK_EVENT_LOG: eventLog,
      MOCK_DELAY_RUNTIME_SUPERVISOR_MS: '150',
      MOCK_DELAY_MANAGER_MS: '150',
    });
    const { _ports: _unused, ...env } = rawEnv;

    const proc = spawn('bash', [path.join(binDir, 'cpa-manager-plus-launcher')], {
      env,
      stdio: 'ignore',
    });
    activeProcesses.push(proc);

    const readyFile = path.join(runDir, 'launcher.ready');
    assert.equal(await waitForFile(readyFile), true, 'launcher should reach ready state');

    proc.kill('SIGTERM');
    const [exitCode] = await new Promise((resolve) => proc.once('exit', (code) => resolve([code])));
    assert.equal(exitCode, 0);

    const events = readFileSync(eventLog, 'utf8')
      .trim()
      .split('\n')
      .map((line) => JSON.parse(line));
    const labels = events.map((e) => `${e.role}:${e.event}`);

    const idx = (label) => labels.indexOf(label);
    assert.ok(idx('runtime-supervisor:start') >= 0);
    assert.ok(
      idx('runtime-supervisor:ready') < idx('manager:start'),
      `manager must not start until runtime-supervisor is ready; got: ${labels.join(' -> ')}`
    );
    assert.ok(
      idx('manager:ready') < idx('ingress:start'),
      `ingress must not start until manager is ready; got: ${labels.join(' -> ')}`
    );
    assert.ok(
      idx('ingress:exit') < idx('manager:sig:SIGTERM'),
      `ingress must exit before manager receives SIGTERM; got: ${labels.join(' -> ')}`
    );
    assert.ok(
      idx('manager:exit') < idx('runtime-supervisor:sig:SIGTERM'),
      `manager must exit before runtime-supervisor receives SIGTERM; got: ${labels.join(' -> ')}`
    );

    assert.equal(existsSync(readyFile), false);
    assert.equal(existsSync(path.join(runDir, 'runtime-supervisor.pid')), false);
    assert.equal(existsSync(path.join(runDir, 'manager.pid')), false);
    assert.equal(existsSync(path.join(runDir, 'ingress.pid')), false);
  });

  it('supervises child crashes by restarting crashed children and tearing down when max restarts is exceeded', async () => {
    const baseDir = makeTempDir('cpamp-native-crash-');
    const { homeDir, binDir, runDir } = createMockNativeHome(baseDir);

    // 1. Auto-restart supervision (CPAMP_MAX_RESTARTS=1)
    const rawEnv = await buildPortEnv(homeDir, { CPAMP_MAX_RESTARTS: '1' });
    const { _ports: _unused, ...env } = rawEnv;

    const proc = spawn('bash', [path.join(binDir, 'cpa-manager-plus-launcher')], {
      env,
      stdio: 'ignore',
    });
    activeProcesses.push(proc);

    const readyFile = path.join(runDir, 'launcher.ready');
    assert.equal(await waitForFile(readyFile), true);

    const parseReady = () =>
      Object.fromEntries(
        readFileSync(readyFile, 'utf8')
          .trim()
          .split('\n')
          .map((line) => line.split('='))
      );

    const initialReady = parseReady();
    const firstManagerPid = Number(initialReady.manager_pid);
    assert.ok(isPidAlive(firstManagerPid));

    // Simulate unexpected crash of manager
    process.kill(firstManagerPid, 'SIGKILL');

    // Wait for launcher to restart manager with a new PID
    let restartedManagerPid = firstManagerPid;
    const deadline = Date.now() + 8000;
    while (Date.now() < deadline) {
      if (existsSync(readyFile)) {
        const current = parseReady();
        const candidate = Number(current.manager_pid);
        if (candidate > 0 && candidate !== firstManagerPid && isPidAlive(candidate)) {
          restartedManagerPid = candidate;
          break;
        }
      }
      await sleep(100);
    }
    assert.notEqual(
      restartedManagerPid,
      firstManagerPid,
      'launcher should have restarted the crashed manager with a new PID'
    );
    assert.ok(isPidAlive(Number(initialReady.runtime_pid)));
    assert.ok(isPidAlive(Number(initialReady.ingress_pid)));

    // 2. Second crash exceeds CPAMP_MAX_RESTARTS=1 -> launcher tears down remaining children and exits non-zero
    process.kill(restartedManagerPid, 'SIGKILL');
    const [exitCode] = await new Promise((resolve) => proc.once('exit', (code) => resolve([code])));
    assert.notEqual(exitCode, 0, 'launcher must exit non-zero when child exceeds max restarts');
    assert.equal(isPidAlive(Number(initialReady.runtime_pid)), false);
    assert.equal(isPidAlive(Number(initialReady.ingress_pid)), false);
    assert.equal(existsSync(readyFile), false);
  });

  it('supports cpa-manager-plusctl install, start, status, logs, restart, and stop in v2 3-process mode', async () => {
    const baseDir = makeTempDir('cpamp-native-ctl-');
    const { homeDir, runDir } = createMockNativeHome(baseDir);
    const ctl = path.join(homeDir, 'cpa-manager-plusctl');
    const systemdDir = path.join(baseDir, 'systemd');
    const systemctlLog = path.join(baseDir, 'systemctl.log');
    const mockSystemctl = path.join(baseDir, 'mock-systemctl');

    writeFileSync(
      mockSystemctl,
      `#!/usr/bin/env bash\nprintf '%s\\n' "$*" >> "${systemctlLog}"\n`,
      { mode: 0o755 }
    );
    chmodSync(mockSystemctl, 0o755);

    const rawEnv = await buildPortEnv(homeDir, {
      CPA_MANAGER_PLUS_SYSTEMCTL_BIN: mockSystemctl,
    });
    const { _ports: ports, ...env } = rawEnv;

    // 1. install
    const installOut = execFileSync(
      'bash',
      [ctl, 'install', '--unit-dir', systemdDir],
      { env, encoding: 'utf8' }
    );
    const unitFile = path.join(systemdDir, 'cpa-manager-plus.service');
    assert.match(installOut, /Installed systemd unit:/);
    assert.match(installOut, /CPA child identity: \S+ \(UID \d+, GID \d+\)/);
    assert.equal(existsSync(unitFile), true);
    const unitContent = readFileSync(unitFile, 'utf8');
    assert.match(unitContent, new RegExp(`WorkingDirectory=${homeDir}`));
    assert.match(unitContent, new RegExp(`ExecStart=${homeDir}/bin/cpa-manager-plus-launcher`));
    assert.doesNotMatch(unitContent, /^User=/m);
    assert.doesNotMatch(unitContent, /^Group=/m);
    assert.doesNotMatch(unitContent, /ExecReload=/);
    assert.match(unitContent, /CPAMP_RUNTIME_CPA_UID=\d+/);
    assert.match(unitContent, /CPAMP_RUNTIME_CPA_GID=\d+/);
    assert.match(unitContent, /KillMode=mixed/);
    for (const directive of [
      'ProtectSystem=strict',
      'ProtectHome=true',
      'PrivateDevices=true',
      'NoNewPrivileges=true',
      'RestrictSUIDSGID=true',
      'LockPersonality=true',
      'ProtectKernelTunables=true',
      'ProtectKernelModules=true',
      'ProtectKernelLogs=true',
      'RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6',
      'CapabilityBoundingSet=CAP_CHOWN CAP_DAC_OVERRIDE CAP_FOWNER CAP_KILL CAP_SETGID CAP_SETUID',
    ]) {
      assert.match(unitContent, new RegExp(directive.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')));
    }
    assert.match(
      unitContent,
      new RegExp(
        `ReadWritePaths=${path.join(homeDir, 'data')} ${path.join(homeDir, 'run')} ${path.join(homeDir, 'logs')}`
      )
    );
    assert.equal(
      readFileSync(systemctlLog, 'utf8').trim(),
      'daemon-reload\nenable cpa-manager-plus.service'
    );

    // --user cannot work end to end: the Supervisor needs root to drop CPA.
    const rejectedUser = spawnSync('bash', [ctl, 'install', '--unit-dir', systemdDir, '--user', 'nobody'], {
      env,
      encoding: 'utf8',
    });
    assert.notEqual(rejectedUser.status, 0);
    assert.match(rejectedUser.stderr, /install --user is not supported/);

    // 2. start
    const startOut = execFileSync('bash', [ctl, 'start'], { env, encoding: 'utf8' });
    assert.match(startOut, /cpa-manager-plus started with PID \d+/);
    assert.match(
      startOut,
      /Components: runtime-supervisor \(PID \d+\), manager \(PID \d+\), ingress \(PID \d+\)/
    );

    // Verify public Ingress endpoint is reachable on the configured random loopback port
    const healthRes = await fetch(`http://127.0.0.1:${ports.ingressPort}/health`);
    assert.equal(healthRes.status, 200);
    const healthBody = await healthRes.json();
    assert.equal(healthBody.ok, true);
    assert.equal(healthBody.via, 'ingress');

    // 3. status
    const statusOut = execFileSync('bash', [ctl, 'status'], { env, encoding: 'utf8' });
    assert.match(statusOut, /cpa-manager-plus is running with PID \d+/);
    assert.match(statusOut, /runtime-supervisor: running \(PID \d+\)/);
    assert.match(statusOut, /manager: running \(PID \d+\)/);
    assert.match(statusOut, /ingress: running \(PID \d+\)/);

    // 4. logs
    const logsOut = execFileSync('bash', [ctl, 'logs', '50'], { env, encoding: 'utf8' });
    assert.match(logsOut, /All 3 processes started and ready/);

    const invalidLogs = spawnSync('bash', [ctl, 'logs', 'abc'], { env, encoding: 'utf8' });
    assert.notEqual(invalidLogs.status, 0);
    assert.match(invalidLogs.stderr, /Invalid log line count/);

    // 5. restart
    const beforePid = readFileSync(path.join(runDir, 'cpa-manager-plus.pid'), 'utf8');
    const restartOut = execFileSync('bash', [ctl, 'restart'], { env, encoding: 'utf8' });
    assert.match(restartOut, /cpa-manager-plus stopped/);
    assert.match(restartOut, /cpa-manager-plus started with PID \d+/);
    const afterPid = readFileSync(path.join(runDir, 'cpa-manager-plus.pid'), 'utf8');
    assert.notEqual(afterPid, beforePid);

    // 6. stop
    const stopOut = execFileSync('bash', [ctl, 'stop'], { env, encoding: 'utf8' });
    assert.match(stopOut, /cpa-manager-plus stopped/);
    assert.equal(existsSync(path.join(runDir, 'cpa-manager-plus.pid')), false);
    assert.equal(existsSync(path.join(runDir, 'launcher.ready')), false);

    const statusStopped = spawnSync('bash', [ctl, 'status'], { env, encoding: 'utf8' });
    assert.notEqual(statusStopped.status, 0);
    assert.match(statusStopped.stdout, /is not running/);
  });

  it('supports PORT configuration, uninitialized Fresh discovery readiness, SIGINT propagation, and startup failure rollback', async () => {
    const baseDir = makeTempDir('cpamp-native-sigint-');
    const { homeDir, binDir, runDir } = createMockNativeHome(baseDir);

    // 1. Reject symlinked token file
    const symlinkToken = path.join(runDir, 'runtime-token');
    const externalTarget = path.join(baseDir, 'outside-token');
    writeFileSync(externalTarget, 'a'.repeat(64), { mode: 0o600 });
    symlinkSync(externalTarget, symlinkToken);
    const rawEnv1 = await buildPortEnv(homeDir);
    const { _ports: ports1, ...env1 } = rawEnv1;
    const symlinkRes = spawnSync('bash', [path.join(binDir, 'cpa-manager-plus-launcher')], {
      env: env1,
      encoding: 'utf8',
    });
    assert.notEqual(symlinkRes.status, 0);
    assert.match(symlinkRes.stderr, /Refusing to use symlinked runtime file/);
    rmSync(symlinkToken, { force: true });

    // 2. Startup failure rollback: if manager fails during startup, runtime-supervisor is stopped cleanly
    const eventLog = path.join(baseDir, 'rollback-events.jsonl');
    const failRes = spawnSync('bash', [path.join(binDir, 'cpa-manager-plus-launcher')], {
      env: { ...env1, MOCK_FAIL_ROLE: 'manager', MOCK_EVENT_LOG: eventLog },
      encoding: 'utf8',
    });
    assert.notEqual(failRes.status, 0);
    const rollbackEvents = readFileSync(eventLog, 'utf8')
      .trim()
      .split('\n')
      .map((l) => JSON.parse(l));
    const runtimeStartEvent = rollbackEvents.find(
      (e) => e.role === 'runtime-supervisor' && e.event === 'start'
    );
    assert.ok(runtimeStartEvent, 'runtime-supervisor should have started before manager failed');
    assert.equal(
      isPidAlive(runtimeStartEvent.pid),
      false,
      'runtime-supervisor must be stopped when manager fails during startup'
    );
    assert.equal(existsSync(path.join(runDir, 'runtime-supervisor.pid')), false);

    // 3. PORT configuration + Fresh discovery-only readiness (503 /health + 200 /__cpamp/bootstrap/v1/discovery) + SIGINT shutdown
    const env2 = {
      ...env1,
      PORT: `127.0.0.1:${ports1.ingressPort}`,
      MOCK_MANAGER_DISCOVERY_ONLY: '1',
    };
    delete env2.HTTP_ADDR;

    const proc = spawn('bash', [path.join(binDir, 'cpa-manager-plus-launcher')], {
      env: env2,
      stdio: 'ignore',
    });
    activeProcesses.push(proc);

    const readyFile = path.join(runDir, 'launcher.ready');
    assert.equal(await waitForFile(readyFile), true);

    proc.kill('SIGINT');
    const [exitCode] = await new Promise((resolve) => proc.once('exit', (code) => resolve([code])));
    assert.equal(exitCode, 0);
    assert.equal(existsSync(readyFile), false);
  });

  it('packages the v2 linux/amd64 Native archive with distinct naming, pinned CPA sha256 verification, root-owned members, and runnable layout', async () => {
    const baseDir = makeTempDir('cpamp-native-pkg-');
    const { binDir, cpaBin, cpaSha } = createMockNativeHome(baseDir);
    const outDir = path.join(baseDir, 'dist-native');
    const cpaLicensePath = path.join(baseDir, 'cli-proxy-api-LICENSE');
    writeFileSync(cpaLicensePath, 'CLIProxyAPI license fixture\n');

    const pkgEnv = {
      ...process.env,
      VERSION: 'v2.0.0-beta.1',
      PACKAGE_MODE: 'v2',
      OUT_DIR: outDir,
      CPA_BINARY_PATH: cpaBin,
      CPA_LICENSE_PATH: cpaLicensePath,
      CPAMP_CPA_VERSION: '8.0.13',
      CPAMP_CPA_EXECUTABLE_SHA256: cpaSha,
      // This fixture binary is not the released artifact, so the packaging
      // guard requires an explicit opt-in for the override.
      CPAMP_ALLOW_UNPINNED_CPA: '1',
      CPAMP_PREBUILT_MANAGER_BIN: path.join(binDir, 'cpa-manager-plus'),
      CPAMP_PREBUILT_INGRESS_BIN: path.join(binDir, 'cpa-manager-plus-ingress'),
      CPAMP_PREBUILT_RUNTIME_SUPERVISOR_BIN: path.join(
        binDir,
        'cpa-manager-plus-runtime-supervisor'
      ),
      CPAMP_SKIP_PACKAGE_DOCS: '1',
    };

    // 0. An unpinned CPA override is refused unless it is explicitly approved.
    const unpinnedEnv = { ...pkgEnv };
    delete unpinnedEnv.CPAMP_ALLOW_UNPINNED_CPA;
    const unpinnedResult = spawnSync('bash', [packageNativeScript], {
      env: unpinnedEnv,
      encoding: 'utf8',
    });
    assert.notEqual(unpinnedResult.status, 0);
    assert.match(
      unpinnedResult.stderr,
      /CPA input differs from the Dockerfile.runtime linux\/amd64 pin/
    );

    // 0b. A CPA binary without its own license is refused: the repository
    // LICENSE is not CLIProxyAPI's license.
    const noLicenseEnv = { ...pkgEnv };
    delete noLicenseEnv.CPA_LICENSE_PATH;
    const noLicenseResult = spawnSync('bash', [packageNativeScript], {
      env: noLicenseEnv,
      encoding: 'utf8',
    });
    assert.notEqual(noLicenseResult.status, 0);
    assert.match(noLicenseResult.stderr, /CPA_LICENSE_PATH is required with CPA_BINARY_PATH/);

    // 1. Rejects mismatched CPA binary SHA-256
    const badShaResult = spawnSync('bash', [packageNativeScript], {
      env: { ...pkgEnv, CPAMP_CPA_EXECUTABLE_SHA256: '0'.repeat(64) },
      encoding: 'utf8',
    });
    assert.notEqual(badShaResult.status, 0);
    assert.match(badShaResult.stderr, /Pinned CPA v8 executable SHA-256 mismatch/);

    // 2. Rejects _no-plugin CPA artifact
    const noPluginResult = spawnSync('bash', [packageNativeScript], {
      env: {
        ...pkgEnv,
        CPAMP_CPA_ASSET_NAME: 'CLIProxyAPI_8.0.13_linux_amd64_no-plugin.tar.gz',
      },
      encoding: 'utf8',
    });
    assert.notEqual(noPluginResult.status, 0);
    assert.match(noPluginResult.stderr, /Refusing _no-plugin CPA artifact/);

    // 3. Builds valid v2 linux/amd64 archive
    execFileSync('bash', [packageNativeScript], { env: pkgEnv, encoding: 'utf8' });

    const archiveName = 'cpa-manager-plus-native-v2_v2.0.0-beta.1_linux_amd64.tar.gz';
    assert.equal(
      /^cpa-manager-plus_v/.test(archiveName),
      false,
      'v2 asset name must never collide with 1.x cpa-manager-plus_v*_* schema-1 assets'
    );
    const archivePath = path.join(outDir, archiveName);
    const checksumsPath = path.join(outDir, 'checksums.txt');
    assert.equal(existsSync(archivePath), true);
    assert.equal(existsSync(checksumsPath), true);

    const archiveSha = createHash('sha256').update(readFileSync(archivePath)).digest('hex');
    const checksumsContent = readFileSync(checksumsPath, 'utf8');
    assert.match(checksumsContent, new RegExp(`${archiveSha}\\s+\\./${archiveName}`));

    // Every member must record root:root. GNU tar run as root restores archived
    // ownership, so a CI account's UID would otherwise own the launcher that
    // systemd executes as root.
    const listing = execFileSync('tar', ['--numeric-owner', '-tvzf', archivePath], {
      encoding: 'utf8',
    });
    const listingLines = listing.trim().split('\n').filter(Boolean);
    assert.ok(listingLines.length > 0, 'archive must contain members');
    for (const line of listingLines) {
      assert.match(
        line,
        /^\S+\s+(?:0\/0|root\/root)\s/,
        `archive member must be root-owned: ${line}`
      );
    }

    // 4. Extract archive and validate complete directory & file layout
    const extractDir = path.join(baseDir, 'extracted');
    mkdirSync(extractDir, { recursive: true });
    execFileSync('tar', ['--no-same-owner', '-xzf', archivePath, '-C', extractDir]);

    const pkgRoot = path.join(extractDir, 'cpa-manager-plus-native-v2_v2.0.0-beta.1_linux_amd64');
    for (const relPath of [
      'bin/cpa-manager-plus',
      'bin/cpa-manager-plus-ingress',
      'bin/cpa-manager-plus-runtime-supervisor',
      'bin/cli-proxy-api',
      'bin/cpa-manager-plus-launcher',
      'cpa-manager-plus-launcher',
      'cpa-manager-plusctl',
      'cpa-manager-plus.service',
      'share/cpamp/runtime/cpa-artifact.json',
      'share/cpamp/runtime/config.seed.yaml',
      'share/licenses/cli-proxy-api/LICENSE',
      'LICENSE',
      'README.md',
      'README_CN.md',
      'data',
      'run',
      'logs',
    ]) {
      assert.equal(
        existsSync(path.join(pkgRoot, relPath)),
        true,
        `expected ${relPath} in v2 native package`
      );
    }

    const artifactManifest = JSON.parse(
      readFileSync(path.join(pkgRoot, 'share/cpamp/runtime/cpa-artifact.json'), 'utf8')
    );
    assert.deepEqual(artifactManifest, {
      schemaVersion: 1,
      engine: 'cpa',
      version: '8.0.13',
      artifactId: `sha256:${cpaSha}`,
    });

    // 5. Run cpa-manager-plusctl start/status/stop directly inside the extracted package
    const extractedEnvRaw = await buildPortEnv(pkgRoot);
    const { _ports: extractedPorts, ...extractedEnv } = extractedEnvRaw;
    const pkgCtl = path.join(pkgRoot, 'cpa-manager-plusctl');
    try {
      const startOut = execFileSync('bash', [pkgCtl, 'start'], {
        cwd: pkgRoot,
        env: extractedEnv,
        encoding: 'utf8',
      });
      assert.match(startOut, /cpa-manager-plus started with PID \d+/);

      const res = await fetch(`http://127.0.0.1:${extractedPorts.ingressPort}/health`);
      assert.equal(res.status, 200);

      const statusOut = execFileSync('bash', [pkgCtl, 'status'], {
        cwd: pkgRoot,
        env: extractedEnv,
        encoding: 'utf8',
      });
      assert.match(statusOut, /runtime-supervisor: running/);
      assert.match(statusOut, /manager: running/);
      assert.match(statusOut, /ingress: running/);

      const stopOut = execFileSync('bash', [pkgCtl, 'stop'], {
        cwd: pkgRoot,
        env: extractedEnv,
        encoding: 'utf8',
      });
      assert.match(stopOut, /cpa-manager-plus stopped/);
    } finally {
      spawnSync('bash', [pkgCtl, 'stop'], { cwd: pkgRoot, env: extractedEnv, encoding: 'utf8' });
    }
  });

  it('fails closed when a symlink is planted in the CPA-owned Gateway directory', async () => {
    const baseDir = makeTempDir('cpamp-native-gateway-symlink-');
    const { homeDir, binDir, dataDir } = createMockNativeHome(baseDir);
    const gatewayDir = path.join(dataDir, 'runtime/gateway');
    mkdirSync(gatewayDir, { recursive: true });

    const target = path.join(baseDir, 'root-owned-target');
    writeFileSync(target, 'root secret\n', { mode: 0o600 });
    chmodSync(target, 0o600);

    // A compromised cpamp-cpa process can plant this: the Gateway tree belongs
    // to it, and root must never chmod/chown/write through such a link.
    const planted = path.join(gatewayDir, 'config.yaml');
    symlinkSync(target, planted);

    const rawEnv = await buildPortEnv(homeDir);
    const { _ports: _ports, ...env } = rawEnv;

    const startRes = spawnSync('bash', [path.join(binDir, 'cpa-manager-plus-launcher')], {
      env,
      encoding: 'utf8',
    });
    assert.notEqual(startRes.status, 0, 'launcher must refuse a symlinked Gateway tree');
    assert.match(startRes.stderr, /is a symlink inside/);
    assert.match(startRes.stderr, new RegExp(path.join(gatewayDir, 'config.yaml').replace(/[.*+?^${}()|[\]\\]/g, '\\$&')));
    assert.equal(existsSync(path.join(homeDir, 'run/launcher.ready')), false);

    // The link is untouched and its root-owned target keeps its mode/content.
    assert.equal(lstatSync(planted).isSymbolicLink(), true);
    assert.equal(statSync(target).mode & 0o777, 0o600);
    assert.equal(readFileSync(target, 'utf8'), 'root secret\n');

    // install must fail closed the same way instead of chowning through the link.
    const ctl = path.join(homeDir, 'cpa-manager-plusctl');
    const installRes = spawnSync(
      'bash',
      [ctl, 'install', '--unit-dir', path.join(baseDir, 'systemd'), '--no-systemctl'],
      { env, encoding: 'utf8' }
    );
    assert.notEqual(installRes.status, 0, 'install must refuse a symlinked Gateway tree');
    assert.match(installRes.stderr, /is a symlink inside/);
    assert.equal(statSync(target).mode & 0o777, 0o600);
    assert.equal(lstatSync(planted).isSymbolicLink(), true);
    assert.equal(existsSync(path.join(baseDir, 'systemd/cpa-manager-plus.service')), false);
  });

  it('rejects unpaired CPA identity overrides and unsafe unit values at install time', async () => {
    const baseDir = makeTempDir('cpamp-native-ctl-validation-');
    const { homeDir, binDir } = createMockNativeHome(baseDir);
    const ctl = path.join(homeDir, 'cpa-manager-plusctl');
    const unitDir = path.join(baseDir, 'systemd');
    const rawEnv = await buildPortEnv(homeDir);
    const { _ports: _ports, ...env } = rawEnv;

    // A half-configured identity must never be completed silently from the
    // account database.
    for (const overrides of [
      { CPAMP_RUNTIME_CPA_UID: '10001' },
      { CPAMP_RUNTIME_CPA_GID: '10001' },
    ]) {
      const res = spawnSync('bash', [ctl, 'install', '--unit-dir', unitDir, '--no-systemctl'], {
        env: { ...env, ...overrides },
        encoding: 'utf8',
      });
      assert.notEqual(res.status, 0);
      assert.match(res.stderr, /CPAMP_RUNTIME_CPA_UID and CPAMP_RUNTIME_CPA_GID must be configured together/);
    }

    // The account name is an operand of id(1)/useradd(8).
    const badUser = spawnSync('bash', [ctl, 'install', '--unit-dir', unitDir, '--no-systemctl'], {
      env: { ...env, CPAMP_CPA_USER: 'cpamp-cpa;reboot' },
      encoding: 'utf8',
    });
    assert.notEqual(badUser.status, 0);
    assert.match(badUser.stderr, /Invalid CPAMP_CPA_USER/);

    // systemd would expand/mangle these instead of writing them verbatim.
    const dollarCpa = path.join(binDir, 'cli$proxy-api');
    writeFileSync(dollarCpa, '#!/usr/bin/env bash\nexit 0\n', { mode: 0o755 });
    chmodSync(dollarCpa, 0o755);
    const unsafeValue = spawnSync('bash', [ctl, 'install', '--unit-dir', unitDir, '--no-systemctl'], {
      env: { ...env, CPAMP_CPA_EXECUTABLE: dollarCpa },
      encoding: 'utf8',
    });
    assert.notEqual(unsafeValue.status, 0);
    assert.match(unsafeValue.stderr, /Refusing to write CPA executable into the systemd unit/);

    const backslashCpa = path.join(binDir, 'cli\\proxy-api');
    writeFileSync(backslashCpa, '#!/usr/bin/env bash\nexit 0\n', { mode: 0o755 });
    chmodSync(backslashCpa, 0o755);
    const unsafeBackslash = spawnSync('bash', [ctl, 'install', '--unit-dir', unitDir, '--no-systemctl'], {
      env: { ...env, CPAMP_CPA_EXECUTABLE: backslashCpa },
      encoding: 'utf8',
    });
    assert.notEqual(unsafeBackslash.status, 0);
    assert.match(unsafeBackslash.stderr, /Refusing to write CPA executable into the systemd unit/);
  });

  it('reaps an orphaned CPA child from ctl stop after the launcher died', async () => {
    const baseDir = makeTempDir('cpamp-native-ctl-orphan-');
    const { homeDir, runDir } = createMockNativeHome(baseDir);
    const ctl = path.join(homeDir, 'cpa-manager-plusctl');
    const rawEnv = await buildPortEnv(homeDir, { CPAMP_STOP_TIMEOUT_SEC: '5' });
    const { _ports: ports, ...env } = rawEnv;

    const startOut = execFileSync('bash', [ctl, 'start'], { env, encoding: 'utf8' });
    assert.match(startOut, /cpa-manager-plus started with PID \d+/);
    const launcherPid = Number(readFileSync(path.join(runDir, 'cpa-manager-plus.pid'), 'utf8').match(/pid=(\d+)/)[1]);

    // Simulate a crashed launcher: the Supervisor never stops its CPA child, so
    // the Gateway listener survives and the next start would fail "already bound".
    const orphan = spawn(
      process.execPath,
      [
        '-e',
        `const http = require("node:http");
         http.createServer((req, res) => res.end("cpa")).listen(${ports.cpaPort}, "127.0.0.1");`,
      ],
      { stdio: 'ignore' }
    );
    try {
      const deadline = Date.now() + 5000;
      let listening = false;
      while (Date.now() < deadline) {
        try {
          const res = await fetch(`http://127.0.0.1:${ports.cpaPort}/`);
          await res.text();
          listening = true;
          break;
        } catch {
          await sleep(50);
        }
      }
      assert.equal(listening, true, 'orphan CPA listener should be up');

      process.kill(launcherPid, 'SIGKILL');
      await sleep(300);

      const stopOut = execFileSync('bash', [ctl, 'stop'], { env, encoding: 'utf8' });
      assert.match(stopOut, /cpa-manager-plus stopped|Removed stale PID file/);
      await sleep(300);
      assert.equal(isPidAlive(orphan.pid), false, 'ctl must reap the orphaned CPA listener');
    } finally {
      try {
        orphan.kill('SIGKILL');
      } catch {
        // ignore
      }
    }
  });

  it('refuses Runtime readiness when a second listener shares the port on another address family', async () => {
    const baseDir = makeTempDir('cpamp-native-dual-listen-');
    const { homeDir, binDir, runDir } = createMockNativeHome(baseDir);
    const rawEnv = await buildPortEnv(homeDir, {
      CPAMP_STARTUP_TIMEOUT_SEC: '3',
      CPAMP_STOP_TIMEOUT_SEC: '3',
      // Keep the child unready while the foreign listener appears.
      MOCK_DELAY_RUNTIME_SUPERVISOR_MS: '10000',
    });
    const { _ports: ports, ...env } = rawEnv;

    let stderr = '';
    const proc = spawn('bash', [path.join(binDir, 'cpa-manager-plus-launcher')], {
      env,
      stdio: ['ignore', 'ignore', 'pipe'],
    });
    activeProcesses.push(proc);
    proc.stderr.on('data', (chunk) => {
      stderr += chunk.toString();
    });

    // Wait for the child to bind 127.0.0.1:<port>, then take [::1]:<port> as a
    // foreign listener. A "some inode is mine" check would still report the
    // port as owned and hand the runtime bearer to whichever address curl picks.
    const deadline = Date.now() + 5000;
    let childBound = false;
    while (Date.now() < deadline) {
      try {
        await new Promise((resolve, reject) => {
          const socket = net.connect(ports.runtimePort, '127.0.0.1');
          socket.once('connect', () => {
            socket.destroy();
            resolve();
          });
          socket.once('error', reject);
        });
        childBound = true;
        break;
      } catch {
        await sleep(50);
      }
    }
    assert.equal(childBound, true, 'the runtime child should bind its loopback port');

    const foreign = net.createServer();
    await new Promise((resolve, reject) => {
      foreign.once('error', reject);
      foreign.listen(ports.runtimePort, '::1', resolve);
    });
    try {
      const [exitCode] = await new Promise((resolve) =>
        proc.once('exit', (code, signal) => resolve([code, signal]))
      );
      assert.notEqual(exitCode, 0, 'the launcher must not report ready while a foreign listener shares the port');
      assert.match(stderr, /Timed out after 3s waiting for runtime-supervisor/);
      assert.equal(existsSync(path.join(runDir, 'launcher.ready')), false);
    } finally {
      await new Promise((resolve) => foreign.close(resolve));
    }
  });
});
