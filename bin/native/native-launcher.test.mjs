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
import { gunzipSync } from 'node:zlib';

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

const TAR_BLOCK_SIZE = 512;

const readTarString = (buffer, offset, length) =>
  buffer
    .subarray(offset, offset + length)
    .toString('utf8')
    .replace(/\0[\s\S]*$/, '');

/**
 * Reads a tar header numeric field. Traditional archives store octal digits
 * padded with spaces and NULs; GNU tar switches to a big-endian base-256
 * integer when the value cannot be represented in the octal field.
 */
const readTarNumber = (header, offset, length) => {
  const field = header.subarray(offset, offset + length);
  if ((field[0] & 0x80) !== 0) {
    let value = 0n;
    for (const byte of field) value = (value << 8n) | BigInt(byte);
    // Clear the base-256 marker bit; the rest is the unsigned magnitude.
    value &= (1n << BigInt(length * 8 - 1)) - 1n;
    return Number(value);
  }
  const digits = field
    .toString('utf8')
    .replace(/[\0 ]+$/, '')
    .trim();
  if (digits === '') return 0;
  if (!/^[0-7]+$/.test(digits)) {
    throw new Error(`unreadable tar numeric field: ${JSON.stringify(digits)}`);
  }
  return Number.parseInt(digits, 8);
};

/**
 * Parses pax extended header records ("<length> <key>=<value>\n") into a Map.
 */
const parsePaxRecords = (buffer) => {
  const records = new Map();
  let offset = 0;
  while (offset < buffer.length) {
    const space = buffer.indexOf(0x20, offset);
    if (space === -1) break;
    const recordLength = Number.parseInt(buffer.subarray(offset, space).toString('utf8'), 10);
    if (!Number.isInteger(recordLength) || recordLength <= 0) break;
    if (offset + recordLength > buffer.length) break;
    const record = buffer
      .subarray(space + 1, offset + recordLength)
      .toString('utf8')
      .replace(/\n$/, '');
    const equals = record.indexOf('=');
    if (equals > 0) records.set(record.slice(0, equals), record.slice(equals + 1));
    offset += recordLength;
  }
  return records;
};

/**
 * Reads every archive member's recorded owner and group from the tar headers.
 *
 * The human-readable `tar -tv` listing is not parseable portably: GNU tar
 * renders ownership as "0/0", while bsdtar prints uid and gid as separate
 * columns. The ustar header fields are identical across GNU tar, bsdtar and
 * pax archives, so ownership is read from the 512-byte headers themselves.
 */
const readTarMembers = (archivePath) => {
  const raw = gunzipSync(readFileSync(archivePath));
  const members = [];
  const globalPax = new Map();
  let pendingPax = new Map();
  let pendingName = null;
  let offset = 0;

  while (offset + TAR_BLOCK_SIZE <= raw.length) {
    const header = raw.subarray(offset, offset + TAR_BLOCK_SIZE);
    if (header.every((byte) => byte === 0)) break; // end-of-archive zero blocks

    const typeflag = header[156] === 0 ? '0' : String.fromCharCode(header[156]);
    const size = readTarNumber(header, 124, 12);
    const dataStart = offset + TAR_BLOCK_SIZE;
    const dataEnd = dataStart + size;
    if (dataEnd > raw.length) {
      throw new Error(`truncated tar member header at byte ${offset}`);
    }

    if (typeflag === 'x' || typeflag === 'g') {
      // Extended pax records: 'x' applies to the next member, 'g' to all
      // following members. Long paths and names travel through these records.
      for (const [key, value] of parsePaxRecords(raw.subarray(dataStart, dataEnd))) {
        if (typeflag === 'g') globalPax.set(key, value);
        else pendingPax.set(key, value);
      }
    } else if (typeflag === 'L' || typeflag === 'K') {
      // GNU long name ('L') and long link name ('K') describe the next header.
      if (typeflag === 'L') {
        pendingName = readTarString(raw, dataStart, size);
      }
    } else {
      // POSIX ustar splits long paths into a 155-byte prefix plus the 100-byte
      // name field. GNU tar writes "ustar  " there and reuses that space for
      // atime/ctime, so only a NUL-terminated magic has a real prefix.
      const isPosixUstar = header.subarray(257, 265).toString('latin1').startsWith('ustar\0');
      const prefix = isPosixUstar ? readTarString(header, 345, 155) : '';
      const headerName = readTarString(header, 0, 100);
      const name =
        pendingName ?? pendingPax.get('path') ?? (prefix ? `${prefix}/${headerName}` : headerName);
      const paxUid = pendingPax.get('uid') ?? globalPax.get('uid');
      const paxGid = pendingPax.get('gid') ?? globalPax.get('gid');
      members.push({
        name,
        uid: paxUid === undefined ? readTarNumber(header, 108, 8) : Number(paxUid),
        gid: paxGid === undefined ? readTarNumber(header, 116, 8) : Number(paxGid),
      });
      pendingPax = new Map();
      pendingName = null;
    }

    offset = dataStart + Math.ceil(size / TAR_BLOCK_SIZE) * TAR_BLOCK_SIZE;
  }

  return members;
};

/**
 * Asserts that every archive member records numeric owner and group 0.
 *
 * The archive is unpacked by root, and GNU tar restores archived ownership, so
 * a CI account's uid would otherwise own the launcher that systemd executes as
 * root. Only the numeric fields are asserted: GNU tar leaves the name fields
 * empty when --owner=0 --group=0 is used, while bsdtar records root there.
 */
const assertRootOwnedArchive = (archivePath) => {
  const members = readTarMembers(archivePath);
  assert.ok(members.length > 0, 'archive must contain members');
  for (const member of members) {
    assert.equal(
      member.uid,
      0,
      `archive member must record uid 0, got uid ${member.uid} / gid ${member.gid}: ${member.name}`
    );
    assert.equal(
      member.gid,
      0,
      `archive member must record gid 0, got uid ${member.uid} / gid ${member.gid}: ${member.name}`
    );
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
  // The mock CPA is a real listener so instance-provenance and shutdown paths
  // are exercised against actual process state: argv carries `-config <path>`
  // exactly like the Supervisor's spawn, and the port comes from that file.
  const cpaContent = [
    '#!/usr/bin/env node',
    'const http = require("node:http");',
    'const fs = require("node:fs");',
    '',
    'const args = process.argv.slice(2);',
    'const configIndex = args.indexOf("-config");',
    'const configPath = configIndex >= 0 ? args[configIndex + 1] : "";',
    'let host = "127.0.0.1";',
    'let port = 0;',
    'if (configPath) {',
    '  const text = fs.readFileSync(configPath, "utf8");',
    '  const portMatch = text.match(/^[ \\t]*port:[ \\t]*(\\d+)/m);',
    '  const hostMatch = text.match(/^[ \\t]*host:[ \\t]*"?([0-9a-fA-F:.]+)"?/m);',
    '  if (portMatch) port = Number(portMatch[1]);',
    '  if (hostMatch) host = hostMatch[1];',
    '}',
    'if (!port) { process.exit(2); }',
    'const eventLog = process.env.MOCK_EVENT_LOG || "";',
    'const record = (event) => {',
    '  if (eventLog) {',
    '    fs.appendFileSync(eventLog, JSON.stringify({ ts: Date.now(), role: "cpa", pid: process.pid, event }) + "\\n");',
    '  }',
    '};',
    'const server = http.createServer((req, res) => { res.writeHead(200); res.end("cpa"); });',
    'server.listen(port, host, () => { record("ready"); });',
    'server.on("error", (err) => { record("error:" + err.code); process.exit(3); });',
    'const shutdown = (sig) => {',
    '  record("sig:" + sig);',
    '  server.close(() => { record("exit"); process.exit(0); });',
    '  setTimeout(() => { record("exit"); process.exit(0); }, 1000).unref();',
    '};',
    'process.on("SIGTERM", () => shutdown("SIGTERM"));',
    'process.on("SIGINT", () => shutdown("SIGINT"));',
    'setInterval(() => {}, 60000);',
    '',
  ].join('\n');
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
      'import { spawn } from "node:child_process";',
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
      '  // Mirrors the real Supervisor: CPA is a direct child started with',
      '  // `-config <gateway>/config.yaml`, and Close never stops it.',
      '  if (process.env.MOCK_SUPERVISOR_SPAWN_CPA === "1") {',
      '    const configPath = path.join(process.cwd(), "config.yaml");',
      '    const child = spawn(process.env.CPAMP_CPA_EXECUTABLE, ["-config", configPath], { stdio: "ignore" });',
      '    recordEvent("cpa-spawn:" + child.pid);',
      '    if (process.env.MOCK_CPA_PID_FILE) {',
      '      writeFileSync(process.env.MOCK_CPA_PID_FILE, String(child.pid));',
      '    }',
      '  }',
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

const waitFor = async (predicate, timeoutMs = 8000) => {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (predicate()) return true;
    await sleep(50);
  }
  return false;
};

const waitForPortAccepting = async (port, timeoutMs = 8000) => {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const res = await fetch(`http://127.0.0.1:${port}/`);
      await res.text();
      return true;
    } catch {
      await sleep(50);
    }
  }
  return false;
};

const readReadyState = (runDir) => {
  const readyFile = path.join(runDir, 'launcher.ready');
  if (!existsSync(readyFile)) return null;
  return Object.fromEntries(
    readFileSync(readyFile, 'utf8')
      .trim()
      .split('\n')
      .map((line) => line.split('='))
  );
};

/**
 * PIDs recorded in the launcher's instance-provenance file. The launcher pins
 * every CPA child it observed so a child that outlives its Supervisor stays
 * attributable to this installation.
 */
const readProvenancePids = (homeDir) => {
  const file = path.join(homeDir, 'run/cpa-instance.provenance');
  if (!existsSync(file)) return [];
  return readFileSync(file, 'utf8')
    .split('\n')
    .filter((line) => line && !line.startsWith('#') && !line.startsWith('instance|'))
    .map((line) => Number(line.split('|')[0]))
    .filter((pid) => Number.isInteger(pid) && pid > 0);
};

/**
 * Resolves `[code, signal]` when the process exits, and fails instead of hanging
 * forever when it does not. A launcher that never returns from shutdown must
 * surface as a diagnosable failure, not as a stuck test run.
 */
const waitForExit = (proc, timeoutMs = 30000) =>
  new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      reject(
        new Error(
          `launcher (PID ${proc.pid}) did not exit within ${timeoutMs}ms; alive=${isPidAlive(proc.pid)}`
        )
      );
    }, timeoutMs);
    proc.once('exit', (code, signal) => {
      clearTimeout(timer);
      resolve([code, signal]);
    });
    proc.once('error', (error) => {
      clearTimeout(timer);
      reject(error);
    });
  });

/** Waits until the mock Supervisor has spawned its CPA child and it is listening. */
const waitForSpawnedCpa = async (pidFile, port, timeoutMs = 10000) => {
  const ok = await waitFor(() => {
    if (!existsSync(pidFile)) return false;
    const pid = Number(readFileSync(pidFile, 'utf8').trim());
    return Number.isInteger(pid) && pid > 0 && isPidAlive(pid);
  }, timeoutMs);
  assert.equal(ok, true, 'the mock Supervisor should have spawned a CPA child');
  const pid = Number(readFileSync(pidFile, 'utf8').trim());
  assert.equal(await waitForPortAccepting(port), true, 'the CPA child should be listening');
  return pid;
};

/**
 * Spawns an unrelated same-UID TCP listener that carries no instance
 * provenance. `argvHint` is embedded in its command line so the process also
 * matches a naive "argv contains the CPA binary path" substring scan.
 */
const spawnForeignListener = (port, argvHint = '') =>
  spawn(
    process.execPath,
    [
      '-e',
      `// argv probe: ${argvHint}
       const http = require("node:http");
       http.createServer((req, res) => res.end("foreign")).listen(${port}, "127.0.0.1");`,
    ],
    { stdio: 'ignore' }
  );

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
    const [exitCode] = await waitForExit(proc);
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
      await waitForExit(proc);
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
    const [exitCode] = await waitForExit(proc);
    assert.equal(exitCode, 0);
  });

  it('reaps an instance-attributable CPA child that outlives the Supervisor on shutdown', async () => {
    const baseDir = makeTempDir('cpamp-native-orphan-');
    const { homeDir, binDir, runDir } = createMockNativeHome(baseDir);
    const cpaPidFile = path.join(baseDir, 'cpa.pid');
    const rawEnv = await buildPortEnv(homeDir, {
      CPAMP_STOP_TIMEOUT_SEC: '5',
      MOCK_SUPERVISOR_SPAWN_CPA: '1',
      MOCK_CPA_PID_FILE: cpaPidFile,
    });
    const { _ports: ports, ...env } = rawEnv;

    const proc = spawn('bash', [path.join(binDir, 'cpa-manager-plus-launcher')], {
      env,
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    activeProcesses.push(proc);
    let output = '';
    proc.stdout.on('data', (chunk) => {
      output += chunk.toString();
    });
    proc.stderr.on('data', (chunk) => {
      output += chunk.toString();
    });

    assert.equal(await waitForFile(path.join(runDir, 'launcher.ready')), true);

    // The Supervisor's Close contract never stops the CPA child; a container
    // runtime reaps the process tree, so the native launcher must. The child is
    // started by the Supervisor with this installation's config, and the
    // launcher pins its (pid, start time) while it is still a direct child.
    const cpaPid = await waitForSpawnedCpa(cpaPidFile, ports.cpaPort);
    const pinned = await waitFor(() => readProvenancePids(homeDir).includes(cpaPid), 5000);
    assert.equal(pinned, true, 'the launcher must pin the observed CPA child');

    proc.kill('SIGTERM');
    const [exitCode] = await waitForExit(proc);
    assert.equal(exitCode, 0);
    assert.equal(isPidAlive(cpaPid), false, 'orphaned instance CPA child must be reaped');
    assert.match(
      output,
      new RegExp(`Stopping orphaned CPA child \\(PID ${cpaPid}\\)`),
      'the reaped child must be the attributed instance child'
    );
  });

  it('leaves an unrelated same-UID CPA-port listener alone and fails closed on start', async () => {
    const baseDir = makeTempDir('cpamp-native-foreign-cpa-');
    const { homeDir, binDir, runDir, cpaBin } = createMockNativeHome(baseDir);
    const rawEnv = await buildPortEnv(homeDir, { CPAMP_STOP_TIMEOUT_SEC: '5' });
    const { _ports: ports, ...env } = rawEnv;

    const proc = spawn('bash', [path.join(binDir, 'cpa-manager-plus-launcher')], {
      env,
      stdio: ['ignore', 'ignore', 'pipe'],
    });
    activeProcesses.push(proc);
    let stderr = '';
    proc.stderr.on('data', (chunk) => {
      stderr += chunk.toString();
    });
    assert.equal(await waitForFile(path.join(runDir, 'launcher.ready')), true);

    // Same UID, same port, the CPA binary path in its command line, and no
    // instance provenance: it must never be signalled.
    const foreign = spawnForeignListener(ports.cpaPort, cpaBin);
    try {
      assert.equal(
        await waitForPortAccepting(ports.cpaPort),
        true,
        'foreign CPA-port listener should be up'
      );

      const startedAt = Date.now();
      proc.kill('SIGTERM');
      const [exitCode] = await waitForExit(proc);
      const elapsedMs = Date.now() - startedAt;

      assert.equal(exitCode, 0);
      assert.equal(isPidAlive(foreign.pid), true, 'an unattributable listener must not be signalled');
      assert.match(
        stderr,
        /Refusing to signal PID \d+ on 127\.0\.0\.1:\d+: it cannot be attributed/
      );
      assert.ok(
        elapsedMs < 4000,
        `shutdown must not wait the full 5s budget on an unattributable listener (took ${elapsedMs}ms)`
      );

      // A start while that listener holds the CPA port must fail closed instead
      // of signalling it or starting a stack behind it.
      const startRes = spawnSync('bash', [path.join(binDir, 'cpa-manager-plus-launcher')], {
        env,
        encoding: 'utf8',
      });
      assert.notEqual(startRes.status, 0, 'start must fail closed on an unattributable CPA listener');
      assert.match(startRes.stderr, /cpa-gateway.*already bound|already bound.*cpa-gateway|already bound/);
      assert.equal(isPidAlive(foreign.pid), true);
      assert.equal(existsSync(path.join(runDir, 'launcher.ready')), false);
    } finally {
      try {
        foreign.kill('SIGKILL');
      } catch {
        // ignore
      }
    }
  });

  it('drains the previous CPA child before a replacement Supervisor becomes ready', async () => {
    const baseDir = makeTempDir('cpamp-native-supervisor-crash-');
    const { homeDir, binDir, runDir } = createMockNativeHome(baseDir);
    const eventLog = path.join(baseDir, 'events.jsonl');
    const cpaPidFile = path.join(baseDir, 'cpa.pid');
    const rawEnv = await buildPortEnv(homeDir, {
      CPAMP_MAX_RESTARTS: '1',
      MOCK_EVENT_LOG: eventLog,
      MOCK_SUPERVISOR_SPAWN_CPA: '1',
      MOCK_CPA_PID_FILE: cpaPidFile,
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

    assert.equal(await waitForFile(path.join(runDir, 'launcher.ready')), true);
    const first = readReadyState(runDir);
    const firstCpaPid = await waitForSpawnedCpa(cpaPidFile, ports.cpaPort);
    const pinned = await waitFor(() => readProvenancePids(homeDir).includes(firstCpaPid), 5000);
    assert.equal(pinned, true, 'the running CPA child must be pinned before the crash');

    // The Supervisor dies without stopping CPA (its Close contract).
    process.kill(Number(first.runtime_pid), 'SIGKILL');

    const restarted = await waitFor(() => {
      const state = readReadyState(runDir);
      return (
        state &&
        Number(state.runtime_pid) > 0 &&
        state.runtime_pid !== first.runtime_pid &&
        isPidAlive(Number(state.runtime_pid))
      );
    }, 10000);
    assert.equal(restarted, true, 'the launcher must restart the Supervisor');
    assert.equal(isPidAlive(firstCpaPid), false, 'the old CPA child must be terminated');

    const events = readFileSync(eventLog, 'utf8')
      .trim()
      .split('\n')
      .map((line) => JSON.parse(line));
    const oldCpaExit = events.find((e) => e.role === 'cpa' && e.pid === firstCpaPid && e.event === 'exit');
    assert.ok(oldCpaExit, 'the old CPA child must have been drained');
    const replacementStart = events.find(
      (e) => e.role === 'runtime-supervisor' && e.event === 'start' && e.pid !== Number(first.runtime_pid)
    );
    assert.ok(replacementStart, 'a replacement Supervisor must have started');
    assert.ok(
      oldCpaExit.ts <= replacementStart.ts,
      'the CPA port must be drained before the replacement Supervisor starts'
    );
    assert.equal(
      existsSync(path.join(runDir, 'launcher.ready')),
      true,
      'the launcher must be ready again'
    );

    proc.kill('SIGTERM');
    const [exitCode] = await waitForExit(proc);
    assert.equal(exitCode, 0);
    assert.doesNotMatch(stderr, /is already bound by a process that cannot be attributed/);
  });

  it('fails closed and stops the stack when an unattributable listener holds the CPA port after a Supervisor crash', async () => {
    const baseDir = makeTempDir('cpamp-native-supervisor-crash-foreign-');
    const { homeDir, binDir, runDir, cpaBin } = createMockNativeHome(baseDir);
    const cpaPidFile = path.join(baseDir, 'cpa.pid');
    const rawEnv = await buildPortEnv(homeDir, {
      CPAMP_MAX_RESTARTS: '2',
      MOCK_SUPERVISOR_SPAWN_CPA: '1',
      MOCK_CPA_PID_FILE: cpaPidFile,
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

    assert.equal(await waitForFile(path.join(runDir, 'launcher.ready')), true);
    const ready = readReadyState(runDir);
    const cpaPid = await waitForSpawnedCpa(cpaPidFile, ports.cpaPort);

    // The instance child dies and an unattributable same-UID listener takes the
    // port; then the Supervisor crashes.
    process.kill(cpaPid, 'SIGKILL');
    assert.equal(await waitFor(() => !isPidAlive(cpaPid), 5000), true);
    const foreign = spawnForeignListener(ports.cpaPort, cpaBin);
    try {
      assert.equal(await waitForPortAccepting(ports.cpaPort), true);

      process.kill(Number(ready.runtime_pid), 'SIGKILL');
      const [exitCode] = await waitForExit(proc);

      assert.notEqual(exitCode, 0, 'the launcher must fail closed instead of restarting');
      assert.match(stderr, /is already bound by a process that cannot be attributed/);
      assert.equal(isPidAlive(foreign.pid), true, 'the foreign listener must not be signalled');
      assert.equal(isPidAlive(Number(ready.manager_pid)), false, 'the manager must be stopped');
      assert.equal(isPidAlive(Number(ready.ingress_pid)), false, 'the ingress must be stopped');
      assert.equal(existsSync(path.join(runDir, 'launcher.ready')), false);
    } finally {
      try {
        foreign.kill('SIGKILL');
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
    const [exitCode] = await waitForExit(proc);
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
    const [exitCode] = await waitForExit(proc);
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
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    activeProcesses.push(proc);
    let sigintOutput = '';
    proc.stdout.on('data', (chunk) => {
      sigintOutput += chunk.toString();
    });
    proc.stderr.on('data', (chunk) => {
      sigintOutput += chunk.toString();
    });

    const readyFile = path.join(runDir, 'launcher.ready');
    assert.equal(await waitForFile(readyFile), true);

    proc.kill('SIGINT');
    const [exitCode, exitSignal] = await waitForExit(proc);
    assert.equal(
      exitSignal,
      null,
      `SIGINT must be handled by the launcher, not terminate it\n${sigintOutput}`
    );
    assert.equal(exitCode, 0, `SIGINT shutdown must exit 0, got ${exitCode}\n${sigintOutput}`);
    assert.equal(existsSync(readyFile), false);
  });

  it('exits 0 on SIGINT and SIGTERM with an active CPA child and reaps it', async () => {
    for (const signal of ['SIGINT', 'SIGTERM']) {
      const baseDir = makeTempDir(`cpamp-native-signal-${signal.toLowerCase()}-`);
      const { homeDir, binDir, runDir } = createMockNativeHome(baseDir);
      const cpaPidFile = path.join(baseDir, 'cpa.pid');
      const rawEnv = await buildPortEnv(homeDir, {
        CPAMP_STOP_TIMEOUT_SEC: '5',
        MOCK_SUPERVISOR_SPAWN_CPA: '1',
        MOCK_CPA_PID_FILE: cpaPidFile,
      });
      const { _ports: ports, ...env } = rawEnv;

      const proc = spawn('bash', [path.join(binDir, 'cpa-manager-plus-launcher')], {
        env,
        stdio: ['ignore', 'pipe', 'pipe'],
      });
      activeProcesses.push(proc);
      let output = '';
      proc.stdout.on('data', (chunk) => {
        output += chunk.toString();
      });
      proc.stderr.on('data', (chunk) => {
        output += chunk.toString();
      });

      const readyFile = path.join(runDir, 'launcher.ready');
      assert.equal(await waitForFile(readyFile), true);
      const cpaPid = await waitForSpawnedCpa(cpaPidFile, ports.cpaPort);
      assert.equal(
        await waitFor(() => readProvenancePids(homeDir).includes(cpaPid), 5000),
        true,
        'the active CPA child must be pinned before the signal'
      );

      const startedAt = Date.now();
      proc.kill(signal);
      const [exitCode, exitSignal] = await waitForExit(proc);
      const elapsedMs = Date.now() - startedAt;

      assert.equal(
        exitSignal,
        null,
        `${signal} must be handled by the launcher, not terminate it\n${output}`
      );
      assert.equal(exitCode, 0, `${signal} shutdown must exit 0, got ${exitCode}\n${output}`);
      assert.equal(existsSync(readyFile), false);
      assert.equal(
        await waitFor(() => !isPidAlive(cpaPid), 5000),
        true,
        `${signal}: the CPA child must be reaped\n${output}`
      );
      assert.ok(elapsedMs < 8000, `${signal} shutdown took ${elapsedMs}ms\n${output}`);
      assert.match(output, new RegExp(`Received SIG${signal.slice(3)}`));
    }
  });

  it('never signals a same-session lookalike that copies the instance CPA command line', async () => {
    const baseDir = makeTempDir('cpamp-native-lookalike-');
    const { homeDir, binDir, dataDir, runDir, cpaBin } = createMockNativeHome(baseDir);
    const rawEnv = await buildPortEnv(homeDir, { CPAMP_STOP_TIMEOUT_SEC: '5' });
    const { _ports: ports, ...env } = rawEnv;

    const proc = spawn('bash', [path.join(binDir, 'cpa-manager-plus-launcher')], {
      env,
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    activeProcesses.push(proc);
    let output = '';
    proc.stdout.on('data', (chunk) => {
      output += chunk.toString();
    });
    proc.stderr.on('data', (chunk) => {
      output += chunk.toString();
    });
    assert.equal(await waitForFile(path.join(runDir, 'launcher.ready')), true);

    // Same UID, same session and process group, the installation's own CPA
    // entry point, and a -config path under this installation's runtime root.
    // Only the missing Supervisor parent and the missing pin distinguish it
    // from the child the launcher owns, so it must never be signalled.
    const lookalike = spawn(
      cpaBin,
      ['-config', path.join(dataDir, 'runtime/gateway/config.yaml')],
      { stdio: 'ignore' }
    );
    try {
      assert.equal(await waitForPortAccepting(ports.cpaPort), true, 'lookalike should be listening');

      const startedAt = Date.now();
      proc.kill('SIGINT');
      const [exitCode] = await waitForExit(proc);
      const elapsedMs = Date.now() - startedAt;

      assert.equal(exitCode, 0, `launcher must exit 0\n${output}`);
      assert.equal(
        isPidAlive(lookalike.pid),
        true,
        'a same-session lookalike must not be signalled'
      );
      assert.match(
        output,
        new RegExp(`Refusing to signal PID ${lookalike.pid} on 127\\.0\\.0\\.1:${ports.cpaPort}`)
      );
      assert.ok(
        elapsedMs < 4000,
        `shutdown must not wait on an unattributable process (took ${elapsedMs}ms)\n${output}`
      );
    } finally {
      try {
        lookalike.kill('SIGKILL');
      } catch {
        // ignore
      }
    }
  });

  it('resolves the install unit CPA address from the target instance, not the caller', async () => {
    const sourceBase = makeTempDir('cpamp-native-install-source-');
    const source = createMockNativeHome(sourceBase);
    const targetBase = makeTempDir('cpamp-native-install-target-');
    const target = createMockNativeHome(targetBase);
    const ctl = path.join(source.homeDir, 'cpa-manager-plusctl');
    const unitDir = path.join(targetBase, 'systemd');
    const unitPath = path.join(unitDir, 'cpa-manager-plus.service');

    // The caller's instance and the install target record different ports.
    writeFileSync(path.join(source.runDir, 'cpa.address'), '127.0.0.1:19111\n');
    writeFileSync(path.join(target.runDir, 'cpa.address'), '127.0.0.1:19222\n');

    const baseEnv = { ...process.env, CPA_MANAGER_PLUS_HOME: source.homeDir };
    delete baseEnv.CPAMP_RUNTIME_CPA_ADDR;
    delete baseEnv.CPAMP_CPA_PORT;
    delete baseEnv.CPAMP_GATEWAY_URL;
    delete baseEnv.CPA_UPSTREAM_URL;

    const install = (extraEnv) =>
      spawnSync(
        'bash',
        [ctl, 'install', '--home', target.homeDir, '--unit-dir', unitDir, '--no-systemctl'],
        { env: { ...baseEnv, ...extraEnv }, encoding: 'utf8' }
      );

    const baseRes = install({});
    assert.equal(baseRes.status, 0, baseRes.stderr);
    const baseUnit = readFileSync(unitPath, 'utf8');
    assert.match(baseUnit, /Environment="CPAMP_RUNTIME_CPA_ADDR=127\.0\.0\.1:19222"/);
    assert.doesNotMatch(
      baseUnit,
      /127\.0\.0\.1:19111/,
      "the caller instance's recorded address must never leak into the target unit"
    );

    // An explicit override still wins over either recorded address.
    const overrideRes = install({ CPAMP_CPA_PORT: '19111' });
    assert.equal(overrideRes.status, 0, overrideRes.stderr);
    assert.match(
      readFileSync(unitPath, 'utf8'),
      /Environment="CPAMP_RUNTIME_CPA_ADDR=127\.0\.0\.1:19111"/
    );

    // No override and no record for the target: the documented default applies.
    rmSync(path.join(target.runDir, 'cpa.address'), { force: true });
    const defaultRes = install({});
    assert.equal(defaultRes.status, 0, defaultRes.stderr);
    assert.match(
      readFileSync(unitPath, 'utf8'),
      /Environment="CPAMP_RUNTIME_CPA_ADDR=127\.0\.0\.1:8317"/
    );
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
    assertRootOwnedArchive(archivePath);

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

  it('rejects an archive whose members record a non-root owner', () => {
    const baseDir = makeTempDir('cpamp-native-owner-');
    const memberDir = path.join(baseDir, 'member');
    mkdirSync(path.join(memberDir, 'bin'), { recursive: true });
    writeFileSync(path.join(memberDir, 'bin', 'payload'), 'payload\n', { mode: 0o755 });

    // Both tar flavors record an explicit numeric owner without root
    // privileges, but they spell the flags differently.
    const tarIsGnu = /GNU tar/.test(execFileSync('tar', ['--version'], { encoding: 'utf8' }));
    const ownerFlags = (uid, gid) =>
      tarIsGnu
        ? [`--owner=${uid}`, `--group=${gid}`, '--numeric-owner']
        : ['--uid', String(uid), '--gid', String(gid), '--uname', 'ci-user', '--gname', 'ci-group'];

    // A non-zero uid and a non-zero gid are each rejected on their own, on real
    // archive bytes rather than only for archives built by package-native.sh.
    for (const [uid, gid, expected] of [
      [1000, 1000, /must record uid 0/],
      [0, 1000, /must record gid 0/],
    ]) {
      const archivePath = path.join(baseDir, `owner-${uid}-${gid}.tar.gz`);
      execFileSync('tar', [...ownerFlags(uid, gid), '-czf', archivePath, '-C', baseDir, 'member']);
      assert.throws(() => assertRootOwnedArchive(archivePath), expected);
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

  it('reaps an instance-attributable CPA child from ctl stop after the launcher died', async () => {
    const baseDir = makeTempDir('cpamp-native-ctl-orphan-');
    const { homeDir, runDir } = createMockNativeHome(baseDir);
    const ctl = path.join(homeDir, 'cpa-manager-plusctl');
    const cpaPidFile = path.join(baseDir, 'cpa.pid');
    const rawEnv = await buildPortEnv(homeDir, {
      CPAMP_STOP_TIMEOUT_SEC: '5',
      MOCK_SUPERVISOR_SPAWN_CPA: '1',
      MOCK_CPA_PID_FILE: cpaPidFile,
    });
    const { _ports: ports, ...env } = rawEnv;

    const startOut = execFileSync('bash', [ctl, 'start'], { env, encoding: 'utf8' });
    assert.match(startOut, /cpa-manager-plus started with PID \d+/);
    const launcherPid = Number(readFileSync(path.join(runDir, 'cpa-manager-plus.pid'), 'utf8').match(/pid=(\d+)/)[1]);

    // The Supervisor never stops its CPA child, so after both processes die the
    // Gateway listener survives and the next start would fail "already bound".
    const cpaPid = await waitForSpawnedCpa(cpaPidFile, ports.cpaPort);
    const pinned = await waitFor(() => readProvenancePids(homeDir).includes(cpaPid), 5000);
    assert.equal(pinned, true, 'the launcher must pin the CPA child before it dies');

    const ready = readReadyState(runDir);
    process.kill(launcherPid, 'SIGKILL');
    process.kill(Number(ready.runtime_pid), 'SIGKILL');
    await sleep(300);

    // `ctl stop` reaps it from the pinned (pid, start time) identity plus the
    // instance-scoped argv/config tuple - not from a bare port listener.
    const stopOut = execFileSync('bash', [ctl, 'stop'], { env, encoding: 'utf8' });
    assert.match(stopOut, /cpa-manager-plus stopped|Removed stale PID file/);
    assert.equal(await waitFor(() => !isPidAlive(cpaPid), 5000), true, 'ctl must reap the orphaned instance CPA child');
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
      const [exitCode] = await waitForExit(proc);
      assert.notEqual(exitCode, 0, 'the launcher must not report ready while a foreign listener shares the port');
      assert.match(stderr, /Timed out after 3s waiting for runtime-supervisor/);
      assert.equal(existsSync(path.join(runDir, 'launcher.ready')), false);
    } finally {
      await new Promise((resolve) => foreign.close(resolve));
    }
  });

  it('resolves the same CPA address in the launcher and ctl for every supported override', async () => {
    const defaultListener = net.createServer();
    const defaultBound = await new Promise((resolve) => {
      defaultListener.once('error', () => resolve(false));
      defaultListener.listen(8317, '127.0.0.1', () => resolve(true));
    });

    const overrides = [
      ['CPAMP_RUNTIME_CPA_ADDR', (port) => ({ CPAMP_RUNTIME_CPA_ADDR: `127.0.0.1:${port}` })],
      ['CPAMP_CPA_PORT', (port) => ({ CPAMP_CPA_PORT: String(port) })],
      ['CPAMP_GATEWAY_URL', (port) => ({ CPAMP_GATEWAY_URL: `http://127.0.0.1:${port}` })],
      ['CPA_UPSTREAM_URL', (port) => ({ CPA_UPSTREAM_URL: `http://127.0.0.1:${port}` })],
    ];
    const stripCpaOverrides = (source) => {
      const copy = { ...source };
      delete copy.CPAMP_RUNTIME_CPA_ADDR;
      delete copy.CPAMP_CPA_PORT;
      delete copy.CPAMP_GATEWAY_URL;
      delete copy.CPA_UPSTREAM_URL;
      return copy;
    };

    try {
      for (const [label, build] of overrides) {
        const baseDir = makeTempDir(`cpamp-native-cpa-address-${label}-`);
        const { homeDir, runDir } = createMockNativeHome(baseDir);
        const ctl = path.join(homeDir, 'cpa-manager-plusctl');
        const cpaPidFile = path.join(baseDir, 'cpa.pid');
        const rawEnv = await buildPortEnv(homeDir, {
          MOCK_SUPERVISOR_SPAWN_CPA: '1',
          MOCK_CPA_PID_FILE: cpaPidFile,
        });
        const { _ports: ports, ...baseEnv } = rawEnv;
        const startEnv = { ...stripCpaOverrides(baseEnv), ...build(ports.cpaPort) };

        const startOut = execFileSync('bash', [ctl, 'start'], { env: startEnv, encoding: 'utf8' });
        assert.match(startOut, /cpa-manager-plus started with PID \d+/);

        const expectedAddress = `127.0.0.1:${ports.cpaPort}`;
        const ready = readReadyState(runDir);
        assert.equal(ready.cpa_addr, expectedAddress, `${label} must select the configured CPA port`);
        const addressFile = path.join(runDir, 'cpa.address');
        assert.equal(existsSync(addressFile), true, 'the launcher must persist the resolved CPA address');
        assert.equal(readFileSync(addressFile, 'utf8').trim(), expectedAddress);
        assert.equal(statSync(addressFile).mode & 0o777, 0o600);

        const cpaPid = await waitForSpawnedCpa(cpaPidFile, ports.cpaPort);
        assert.equal(
          await waitFor(() => readProvenancePids(homeDir).includes(cpaPid), 5000),
          true,
          'the CPA child must be pinned before the launcher dies'
        );

        const launcherPid = Number(
          readFileSync(path.join(runDir, 'cpa-manager-plus.pid'), 'utf8').match(/pid=(\d+)/)[1]
        );
        process.kill(launcherPid, 'SIGKILL');
        process.kill(Number(ready.runtime_pid), 'SIGKILL');
        await sleep(300);

        // `ctl stop` runs without any CPA override in its environment. It must
        // use the persisted address, never the hardcoded 8317 default.
        const stopRes = spawnSync('bash', [ctl, 'stop'], {
          env: stripCpaOverrides(startEnv),
          encoding: 'utf8',
        });
        assert.match(
          stopRes.stderr,
          new RegExp(`Stopping orphaned CPA child \\(PID ${cpaPid}\\) on ${expectedAddress}`),
          `${label}: ctl must target the configured CPA port`
        );
        assert.equal(
          await waitFor(() => !isPidAlive(cpaPid), 5000),
          true,
          `${label}: ctl must reap the CPA child on the configured port`
        );

        // Starting again with no CPA override must reuse the recorded address,
        // never the hardcoded default.
        const emptyEnv = stripCpaOverrides(startEnv);
        const restartOut = execFileSync('bash', [ctl, 'start'], { env: emptyEnv, encoding: 'utf8' });
        assert.match(restartOut, /cpa-manager-plus started with PID \d+/);
        assert.equal(
          readReadyState(runDir).cpa_addr,
          expectedAddress,
          `${label}: start must reuse the recorded CPA address`
        );
        const restartCpaPid = await waitForSpawnedCpa(cpaPidFile, ports.cpaPort);
        execFileSync('bash', [ctl, 'stop'], { env: emptyEnv, encoding: 'utf8' });
        assert.equal(
          await waitFor(() => !isPidAlive(restartCpaPid), 8000),
          true,
          `${label}: the restarted stack must be reaped on stop`
        );
      }

      // A listener on the untouched 8317 default is unrelated to this instance
      // and must survive every stop above.
      if (defaultBound) {
        assert.equal(defaultListener.listening, true, 'the default-port listener must not be signalled');
      }
    } finally {
      if (defaultBound) {
        await new Promise((resolve) => defaultListener.close(resolve));
      }
    }
  });

  it('rejects non-canonical leading-zero ports and reads 08/09 digits as decimal', async () => {
    const baseDir = makeTempDir('cpamp-native-port-');
    const { homeDir, binDir } = createMockNativeHome(baseDir);
    const ctl = path.join(homeDir, 'cpa-manager-plusctl');
    const rawEnv = await buildPortEnv(homeDir);
    const { _ports: _ports, ...env } = rawEnv;

    const stripCpaOverrides = (source) => {
      const copy = { ...source };
      delete copy.CPAMP_RUNTIME_CPA_ADDR;
      delete copy.CPAMP_CPA_PORT;
      delete copy.CPAMP_GATEWAY_URL;
      delete copy.CPA_UPSTREAM_URL;
      return copy;
    };
    const bareEnv = stripCpaOverrides(env);

    for (const overrides of [
      { CPAMP_RUNTIME_CPA_ADDR: '127.0.0.1:08317' },
      { CPAMP_CPA_PORT: '0080' },
      { CPAMP_RUNTIME_CPA_ADDR: '127.0.0.1:08' },
      { CPAMP_CPA_PORT: '09' },
    ]) {
      const res = spawnSync('bash', [path.join(binDir, 'cpa-manager-plus-launcher')], {
        env: { ...bareEnv, ...overrides },
        encoding: 'utf8',
      });
      assert.notEqual(res.status, 0, `leading-zero port must be refused: ${JSON.stringify(overrides)}`);
      assert.match(res.stderr, /Invalid internal address for CPA Gateway/);
      assert.doesNotMatch(res.stderr, /value too great for base/);
    }

    const unitDir = path.join(baseDir, 'systemd');
    const installEnv = { ...env, CPAMP_CPA_PORT: '08317' };
    delete installEnv.CPAMP_RUNTIME_CPA_ADDR;
    const ctlRes = spawnSync('bash', [ctl, 'install', '--unit-dir', unitDir, '--no-systemctl'], {
      env: installEnv,
      encoding: 'utf8',
    });
    assert.notEqual(ctlRes.status, 0);
    assert.match(ctlRes.stderr, /Invalid cpa address/);
    assert.doesNotMatch(ctlRes.stderr, /value too great for base/);
    assert.equal(existsSync(path.join(unitDir, 'cpa-manager-plus.service')), false);

    // A canonical port that merely contains "08"/"09" digits must be accepted
    // and reported verbatim: no octal interpretation anywhere.
    const candidates = [1808, 1809, 1908, 1909, 1080, 1081, 2080, 2090, 3808, 3909];
    let decimalPort = 0;
    for (const candidate of candidates) {
      const server = net.createServer();
      const bound = await new Promise((resolve) => {
        server.once('error', () => resolve(false));
        server.listen(candidate, '127.0.0.1', () => resolve(true));
      });
      await new Promise((resolve) => server.close(resolve));
      if (bound) {
        decimalPort = candidate;
        break;
      }
    }
    assert.ok(decimalPort > 0, 'a free port containing 08/09 digits is required');

    const startEnv = { ...env, CPAMP_CPA_PORT: String(decimalPort) };
    delete startEnv.CPAMP_RUNTIME_CPA_ADDR;
    const proc = spawn('bash', [path.join(binDir, 'cpa-manager-plus-launcher')], {
      env: startEnv,
      stdio: 'ignore',
    });
    activeProcesses.push(proc);
    assert.equal(await waitForFile(path.join(homeDir, 'run/launcher.ready')), true);
    assert.equal(
      readFileSync(path.join(homeDir, 'run/cpa.address'), 'utf8').trim(),
      `127.0.0.1:${decimalPort}`
    );
    assert.equal(readReadyState(path.join(homeDir, 'run')).cpa_addr, `127.0.0.1:${decimalPort}`);

    proc.kill('SIGTERM');
    const [exitCode] = await waitForExit(proc);
    assert.equal(exitCode, 0);
  });

  it('produces byte-identical native archives on every available tar implementation', async () => {
    const tarCandidates = new Set();
    for (const candidate of ['tar', 'gtar', '/usr/bin/tar', '/bin/tar']) {
      try {
        const resolved = execFileSync('bash', ['-c', `command -v ${candidate}`], {
          encoding: 'utf8',
        }).trim();
        if (resolved) {
          tarCandidates.add(realpathSync(resolved));
        }
      } catch {
        // not installed
      }
    }
    assert.ok(tarCandidates.size > 0, 'at least one tar implementation must be available');

    const archiveName = 'cpa-manager-plus-native-v2_v2.0.0-beta.1_linux_amd64.tar.gz';
    for (const tarPath of tarCandidates) {
      const baseDir = makeTempDir(`cpamp-native-deterministic-${path.basename(tarPath)}-`);
      const { binDir, cpaBin } = createMockNativeHome(baseDir);
      const cpaLicensePath = path.join(baseDir, 'cli-proxy-api-LICENSE');
      writeFileSync(cpaLicensePath, 'CLIProxyAPI license fixture\n');
      const cpaSha = createHash('sha256').update(readFileSync(cpaBin)).digest('hex');

      const pkgEnv = {
        ...process.env,
        VERSION: 'v2.0.0-beta.1',
        PACKAGE_MODE: 'v2',
        CPA_BINARY_PATH: cpaBin,
        CPA_LICENSE_PATH: cpaLicensePath,
        CPAMP_CPA_VERSION: '8.0.13',
        CPAMP_CPA_EXECUTABLE_SHA256: cpaSha,
        CPAMP_ALLOW_UNPINNED_CPA: '1',
        CPAMP_PREBUILT_MANAGER_BIN: path.join(binDir, 'cpa-manager-plus'),
        CPAMP_PREBUILT_INGRESS_BIN: path.join(binDir, 'cpa-manager-plus-ingress'),
        CPAMP_PREBUILT_RUNTIME_SUPERVISOR_BIN: path.join(
          binDir,
          'cpa-manager-plus-runtime-supervisor'
        ),
        CPAMP_SKIP_PACKAGE_DOCS: '1',
        // Force this exact tar implementation for both builds.
        PATH: `${path.dirname(tarPath)}:${process.env.PATH}`,
      };

      const digests = [];
      for (const stamp of ['202001020304.05', '202106070809.10']) {
        execFileSync('touch', ['-t', stamp, cpaBin, cpaLicensePath]);
        const outDir = path.join(baseDir, `dist-${stamp.replace(/[^0-9]/g, '')}`);
        execFileSync('bash', [packageNativeScript], {
          env: { ...pkgEnv, OUT_DIR: outDir },
          encoding: 'utf8',
        });
        const archivePath = path.join(outDir, archiveName);
        assert.equal(existsSync(archivePath), true);
        digests.push(createHash('sha256').update(readFileSync(archivePath)).digest('hex'));
        // The staging copies pick up "now" as their mtime; a fresh second
        // guarantees the two builds would differ without normalization.
        await sleep(1100);
      }

      assert.equal(
        digests[0],
        digests[1],
        `archives built with ${tarPath} must be byte-identical across runs`
      );
      const archivePath = path.join(
        baseDir,
        `dist-${'202106070809.10'.replace(/[^0-9]/g, '')}/${archiveName}`
      );
      assert.ok(readTarMembers(archivePath).length > 10, 'normalization must not empty the archive');
    }
  });
});
