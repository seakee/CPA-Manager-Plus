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
  readdirSync,
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
      version: '8.0.55',
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
      '    const cpaExec = process.env.MOCK_CPA_SPAWN_EXECUTABLE || process.env.CPAMP_CPA_EXECUTABLE;',
      '    const child = spawn(cpaExec, ["-config", configPath], { stdio: "ignore" });',
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
      'let shuttingDown = false;',
      'const shutdown = (sig) => {',
      '  if (shuttingDown) return;',
      '  shuttingDown = true;',
      '  recordEvent(`sig:${sig}`);',
      '  const drainMs = role === "ingress" ? Number(process.env.MOCK_INGRESS_DRAIN_MS || 0) : 0;',
      '  setTimeout(() => {',
      '    Promise.all(servers.map((s) => new Promise((r) => s.close(() => r())))).finally(() => {',
      '      recordEvent("exit");',
      '      process.exit(0);',
      '    });',
      '  }, drainMs);',
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

const writeFixtureFile = (root, relative, data) => {
  const target = path.join(root, relative);
  mkdirSync(path.dirname(target), { recursive: true });
  writeFileSync(target, data);
  return data;
};

const fixtureDigest = (data) => `sha256:${createHash('sha256').update(data).digest('hex')}`;
const fixtureFileDigest = (filePath) => fixtureDigest(readFileSync(filePath));

const LICENSE_COLLECTOR_STUB = [
  "const fs = require('node:fs');",
  "const path = require('node:path');",
  "const crypto = require('node:crypto');",
  '// argv[2] is the intercepted collector script path; argv[3] is the mode.',
  'const args = process.argv.slice(2).slice(1);',
  'const value = (flag) => {',
  '  const index = args.indexOf(flag);',
  '  return index >= 0 ? args[index + 1] : undefined;',
  '};',
  'const mode = args[0];',
  "const output = value('--output');",
  "const ledger = JSON.parse(fs.readFileSync(process.env.CPAMP_TEST_LEDGER, 'utf8'));",
  "const sha256 = (data) => 'sha256:' + crypto.createHash('sha256').update(data).digest('hex');",
  "const slug = (name, version) => (name + '-' + version).replace(/[^A-Za-z0-9._-]/g, '_');",
  'const moduleDirectories = {',
  "  manager: 'apps/manager-server',",
  "  ingress: 'apps/ingress',",
  "  'runtime-supervisor': 'apps/runtime-supervisor',",
  '};',
  "const component = value('--component');",
  'fs.appendFileSync(process.env.CPAMP_TEST_ORDER_LOG, `collect ${component || mode} goroot=${process.env.GOROOT || \'\'}\\n`);',
  'if (process.env.CPAMP_TEST_OMIT_COMPONENT === (component || mode)) process.exit(0);',
  'fs.mkdirSync(output, { recursive: true });',
  'const writeNotice = (name, version) => {',
  "  const relative = 'fixture/' + slug(name, version) + '/LICENSE';",
  "  const text = 'Original license text fixture for ' + name + '@' + version + '\\n';",
  '  if (process.env.CPAMP_TEST_MISSING_NOTICE !== name) {',
  '    fs.mkdirSync(path.join(output, path.dirname(relative)), { recursive: true });',
  '    fs.writeFileSync(path.join(output, relative), text);',
  '  }',
  '  return [{ path: relative, sha256: sha256(Buffer.from(text)) }];',
  '};',
  'let inventory;',
  "if (mode === 'go') {",
  '  const directory = moduleDirectories[component];',
  '  const inputs = {};',
  '  for (const key of Object.keys(ledger.inputs)) {',
  "    if (key === directory + '/go.mod' || key === directory + '/go.sum') {",
  '      inputs[path.basename(key)] = ledger.inputs[key];',
  '    }',
  '  }',
  "  const dropComponent = process.env.CPAMP_TEST_DROP_COMPONENT || 'manager';",
  '  const declared = (ledger.goModules[component] || []).filter((item) =>',
  '    !(component === dropComponent && process.env.CPAMP_TEST_DROP_MODULE === item.name));',
  '  const dependencies = declared.map((item) => ({',
  '    name: item.name,',
  '    version: item.version,',
  "    integrity: 'h1:fixture',",
  '    notices: writeNotice(item.name, item.version),',
  '  }));',
  '  dependencies.push({',
  "    name: 'Go standard library and bundled sources',",
  "    version: 'go1.26.1',",
  "    notices: writeNotice('Go standard library and bundled sources', 'go1.26.1'),",
  '  });',
  '  inventory = { schemaVersion: 1, component, ecosystem: \'go\', inputs, dependencies };',
  '} else {',
  '  const dependencies = Object.keys(ledger.webDependencies).sort().map((name) => ({',
  '    name,',
  '    version: ledger.webDependencies[name],',
  "    integrity: 'sha512-fixture',",
  "    licenseExpression: 'MIT',",
  '    notices: writeNotice(name, ledger.webDependencies[name]),',
  '  }));',
  '  inventory = {',
  '    schemaVersion: 1,',
  "    component: 'web',",
  "    ecosystem: 'npm',",
  '    inputs: {',
  "      'apps/web/package.json': ledger.inputs['apps/web/package.json'],",
  "      'package-lock.json': ledger.inputs['package-lock.json'],",
  '    },',
  '    dependencies,',
  '  };',
  '}',
  "fs.writeFileSync(path.join(output, 'inventory.json'), JSON.stringify(inventory, null, 2) + '\\n');",
  '',
].join('\n');

/**
 * Test-only harness. The release packaging path has no license fixture
 * switch: it always runs bin/release/collect-third-party-licenses.py against
 * the checked-in source ledger. Tests substitute a PATH-stubbed python3 that
 * intercepts only that collector entry point, plus a go recorder and a real
 * Full Web dist. Everything else (evidence generator, ledger copy) stays real.
 */
const FULLWEB_STAGING_VERIFIER = [
  "const fs = require('node:fs');",
  "const path = require('node:path');",
  "const crypto = require('node:crypto');",
  '',
  'const moduleDir = process.argv[2];',
  "const embedDir = path.join(moduleDir, 'internal/httpapi/web/fullweb');",
  "const manifestPath = path.join(moduleDir, 'internal/httpapi/release-fullweb-manifest.json');",
  "const mutation = process.env.CPAMP_TEST_FULLWEB_MUTATION || '';",
  '',
  'const listFiles = (directory, prefix = \'\') => {',
  '  const files = [];',
  '  for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {',
  "    const relative = prefix ? prefix + '/' + entry.name : entry.name;",
  '    const absolute = path.join(directory, entry.name);',
  '    if (entry.isDirectory()) files.push(...listFiles(absolute, relative));',
  '    else files.push(relative);',
  '  }',
  '  return files.sort();',
  '};',
  '',
  "const manifest = JSON.parse(fs.readFileSync(manifestPath, 'utf8'));",
  'const expected = new Map(manifest.files.map((item) => [item.path, item.sha256]));',
  '',
  '// Test-only staging corruption: proves this verifier rejects exactly the',
  '// conditions the real Go embed test rejects.',
  "if (mutation === 'add-gitkeep') fs.writeFileSync(path.join(embedDir, '.gitkeep'), '');",
  "if (mutation === 'delete-asset') fs.rmSync(path.join(embedDir, [...expected.keys()].find((name) => name !== 'index.html')));",
  "if (mutation === 'modify-asset') fs.appendFileSync(path.join(embedDir, [...expected.keys()].find((name) => name !== 'index.html')), 'corrupted');",
  '',
  'const actual = listFiles(embedDir);',
  'let failed = false;',
  'for (const file of actual) {',
  '  if (!expected.has(file)) {',
  "    console.error('staged Full Web contains unexpected file: ' + file);",
  '    failed = true;',
  '  }',
  '}',
  'for (const [file, sha] of expected) {',
  '  if (!actual.includes(file)) {',
  "    console.error('staged Full Web is missing manifest entry: ' + file);",
  '    failed = true;',
  '    continue;',
  '  }',
  "  const digest = 'sha256:' + crypto.createHash('sha256').update(fs.readFileSync(path.join(embedDir, file))).digest('hex');",
  '  if (digest !== sha) {',
  "    console.error('staged Full Web digest mismatch: ' + file);",
  '    failed = true;',
  '  }',
  '}',
  'if (failed) process.exit(1);',
  '',
].join('\n');

const createLicenseTestHarness = (baseDir) => {
  const root = path.join(baseDir, 'license-harness');
  const binDir = path.join(root, 'bin');
  const fullWebDist = path.join(root, 'fullweb');
  const webHtml = path.join(root, 'panel/management.html');
  const goroot = path.join(root, 'goroot');
  const goroot124 = path.join(root, 'goroot-1.24');
  const gorootB = path.join(root, 'goroot-b');
  const staleGoroot = path.join(root, 'stale-goroot');
  const orderLog = path.join(root, 'order.log');
  const collectorStub = path.join(root, 'collector-stub.cjs');
  const fullWebVerifier = path.join(root, 'fullweb-verifier.cjs');
  mkdirSync(binDir, { recursive: true });
  writeFixtureFile(fullWebDist, 'index.html', '<!doctype html><title>fixture</title>\n');
  writeFixtureFile(fullWebDist, 'assets/app.js', 'console.log("fixture");\n');
  writeFixtureFile(fullWebDist, 'assets/app.css', 'body{color:#111}\n');
  writeFixtureFile(webHtml, '', '<!doctype html><title>panel</title>\n');
  writeFixtureFile(goroot, 'VERSION', 'go1.26.1\n');
  writeFixtureFile(goroot124, 'VERSION', 'go1.24.13\n');
  writeFixtureFile(gorootB, 'VERSION', 'go1.26.1\n');
  // A root whose VERSION disagrees with the running tool: the collector must
  // never label notices with a version the root does not contain.
  writeFixtureFile(staleGoroot, 'VERSION', 'go1.20.14\n');
  writeFileSync(collectorStub, LICENSE_COLLECTOR_STUB);
  writeFileSync(fullWebVerifier, FULLWEB_STAGING_VERIFIER);
  writeFileSync(orderLog, '');

  const realPython = execFileSync('bash', ['-c', 'command -v python3'], {
    encoding: 'utf8',
  }).trim();
  assert.ok(realPython, 'python3 is required by the Native packaging path');

  const pythonWrapper = path.join(binDir, 'python3');
  writeFileSync(
    pythonWrapper,
    [
      '#!/usr/bin/env bash',
      'set -euo pipefail',
      'for arg in "$@"; do',
      '  case "$arg" in',
      '    */collect-third-party-licenses.py)',
      '      exec "${CPAMP_TEST_NODE}" "${CPAMP_TEST_COLLECTOR_STUB}" "$@"',
      '      ;;',
      '  esac',
      'done',
      'exec "${CPAMP_TEST_REAL_PYTHON}" "$@"',
      '',
    ].join('\n'),
    { mode: 0o755 }
  );

  const goWrapper = path.join(binDir, 'go');
  writeFileSync(
    goWrapper,
    [
      '#!/usr/bin/env bash',
      'set -euo pipefail',
      'printf \'go %s @ %s\\n\' "$*" "$PWD" >> "${CPAMP_TEST_ORDER_LOG}"',
      'collector_version="${CPAMP_TEST_COLLECTOR_GOVERSION:-${CPAMP_TEST_GOVERSION:-go1.26.1}}"',
      'binary_version="${CPAMP_TEST_GOVERSION:-go1.26.1}"',
      'case "${1:-}" in',
      '  env)',
      '    module_root="${CPAMP_TEST_GOROOT}"',
      '    module_version="${collector_version}"',
      '    case "$PWD" in',
      '      */apps/manager-server) module_root="${CPAMP_TEST_GOROOT_MANAGER:-${module_root}}"; module_version="${CPAMP_TEST_GOVERSION_MANAGER:-${module_version}}" ;;',
      '      */apps/ingress) module_root="${CPAMP_TEST_GOROOT_INGRESS:-${module_root}}"; module_version="${CPAMP_TEST_GOVERSION_INGRESS:-${module_version}}" ;;',
      '      */apps/runtime-supervisor) module_root="${CPAMP_TEST_GOROOT_SUPERVISOR:-${module_root}}"; module_version="${CPAMP_TEST_GOVERSION_SUPERVISOR:-${module_version}}" ;;',
      '    esac',
      '    if [ "${2:-}" = "GOVERSION" ]; then printf \'%s\\n\' "${module_version}"; fi',
      '    if [ "${2:-}" = "GOROOT" ]; then printf \'%s\\n\' "${module_root}"; fi',
      '    exit 0',
      '    ;;',
      '  test)',
      '    exec "${CPAMP_TEST_NODE}" "${CPAMP_TEST_FULLWEB_VERIFIER}" "$PWD"',
      '    ;;',
      '  version)',
      '    if [ "${2:-}" = "-m" ]; then',
      '      case "$(basename "${3:-}")" in',
      '        cpa-manager-plus) printf \'%s: %s\\n\' "${3}" "${CPAMP_TEST_MANAGER_GO_VERSION:-${binary_version}}" ;;',
      '        cpa-manager-plus-ingress) printf \'%s: %s\\n\' "${3}" "${CPAMP_TEST_INGRESS_GO_VERSION:-${binary_version}}" ;;',
      '        cpa-manager-plus-runtime-supervisor) printf \'%s: %s\\n\' "${3}" "${CPAMP_TEST_SUPERVISOR_GO_VERSION:-${binary_version}}" ;;',
      '        *) printf \'%s: %s\\n\' "${3}" "${binary_version}" ;;',
      '      esac',
      '      exit 0',
      '    fi',
      '    printf \'go version %s\\n\' "${binary_version}"',
      '    exit 0',
      '    ;;',
      '  build)',
      '    out=""',
      '    previous=""',
      '    for arg in "$@"; do',
      '      if [ "${previous}" = "-o" ]; then out="${arg}"; fi',
      '      previous="${arg}"',
      '    done',
      '    if [ -z "${out}" ]; then',
      '      echo "stub go build requires -o" >&2',
      '      exit 1',
      '    fi',
      '    case "$(basename "${out}")" in',
      '      cpa-manager-plus) cp "${CPAMP_TEST_BUILD_MANAGER_BIN}" "${out}" ;;',
      '      cpa-manager-plus-ingress) cp "${CPAMP_TEST_BUILD_INGRESS_BIN}" "${out}" ;;',
      '      cpa-manager-plus-runtime-supervisor) cp "${CPAMP_TEST_BUILD_RUNTIME_SUPERVISOR_BIN}" "${out}" ;;',
      '      *) echo "stub go build: unknown output ${out}" >&2; exit 1 ;;',
      '    esac',
      '    chmod 0755 "${out}"',
      '    exit 0',
      '    ;;',
      'esac',
      'exit 0',
      '',
    ].join('\n'),
    { mode: 0o755 }
  );

  return {
    binDir,
    fullWebDist,
    webHtml,
    goroot,
    goroot124,
    gorootB,
    staleGoroot,
    orderLog,
    // Harness-only variables consumed by the stubs above; package-native.sh
    // never reads any of them.
    env: {
      CPAMP_TEST_LEDGER: path.join(repoRoot, 'bin/release/embedded-third-party-inventory.txt'),
      CPAMP_TEST_ORDER_LOG: orderLog,
      CPAMP_TEST_REAL_PYTHON: realPython,
      CPAMP_TEST_NODE: process.execPath,
      CPAMP_TEST_COLLECTOR_STUB: collectorStub,
      CPAMP_TEST_FULLWEB_VERIFIER: fullWebVerifier,
      CPAMP_TEST_GOROOT: goroot,
    },
  };
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
    assert.equal(runtimeDump.env.CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK, '1');
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
    assert.equal(managerDump.env.CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK, undefined);

    // Verify Ingress env wiring
    assert.equal(ingressDump.env.CPAMP_INGRESS_ADDR, `127.0.0.1:${ports.ingressPort}`);
    assert.equal(ingressDump.env.CPAMP_MANAGER_URL, `http://127.0.0.1:${ports.managerPort}`);
    assert.equal(ingressDump.env.CPAMP_GATEWAY_URL, `http://127.0.0.1:${ports.cpaPort}`);
    assert.equal(ingressDump.env.CPAMP_INGRESS_CONTROL_ADDR, `127.0.0.1:${ports.controlPort}`);
    assert.equal(ingressDump.env.CPAMP_RUNTIME_TOKEN, undefined);
    assert.equal(ingressDump.env.CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK, undefined);

    // The bootstrap Gateway seed must never disable CPA client authentication.
    const gatewayConfig = readFileSync(
      path.join(dataDir, 'runtime/gateway/config.yaml'),
      'utf8'
    );
    assert.match(gatewayConfig, /api-keys: \["[0-9a-f]{64}"\]/);
    assert.doesNotMatch(gatewayConfig, /api-keys: \[\]/);
    assert.equal(gatewayConfig.includes(`port: ${ports.cpaPort}`), true);
    assert.equal(gatewayConfig.includes('host: "127.0.0.1"'), true);
    assert.equal(gatewayConfig.includes('allow-remote: false'), true);
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
    assert.match(unitContent, /Environment="CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK=1"/);
    assert.match(unitContent, /^Environment="CPAMP_DRAIN_TIMEOUT=90s"$/m);
    assert.match(unitContent, /^TimeoutStopSec=150$/m);
    const staticServiceContent = readFileSync(
      path.join(repoRoot, 'bin/native/cpa-manager-plus.service'),
      'utf8'
    );
    assert.match(staticServiceContent, /^Environment="CPAMP_DRAIN_TIMEOUT=90s"$/m);
    assert.match(staticServiceContent, /^TimeoutStopSec=150$/m);
    assert.match(staticServiceContent, /Environment="CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK=1"/);
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
    const harness = createLicenseTestHarness(baseDir);

    const pkgEnv = {
      ...process.env,
      VERSION: 'v2.0.0-beta.1',
      PACKAGE_MODE: 'v2',
      OUT_DIR: outDir,
      CPA_BINARY_PATH: cpaBin,
      CPA_LICENSE_PATH: cpaLicensePath,
      CPAMP_CPA_VERSION: '8.0.55',
      CPAMP_CPA_EXECUTABLE_SHA256: cpaSha,
      // The v2 path always builds from source; the harness go stub writes the
      // fixture service wrappers and reports their toolchain, while the real
      // collector entry point and evidence generator run unchanged.
      ...harness.env,
      PATH: `${harness.binDir}:${process.env.PATH}`,
      WEB_HTML: harness.webHtml,
      FULL_WEB_DIST: harness.fullWebDist,
      CPAMP_TEST_BUILD_MANAGER_BIN: path.join(binDir, 'cpa-manager-plus'),
      CPAMP_TEST_BUILD_INGRESS_BIN: path.join(binDir, 'cpa-manager-plus-ingress'),
      CPAMP_TEST_BUILD_RUNTIME_SUPERVISOR_BIN: path.join(
        binDir,
        'cpa-manager-plus-runtime-supervisor'
      ),
      // This fixture binary is not the released artifact, so the packaging
      // guard requires an explicit opt-in for the override.
      CPAMP_ALLOW_UNPINNED_CPA: '1',
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
        CPAMP_CPA_ASSET_NAME: 'CLIProxyAPI_8.0.55_linux_amd64_no-plugin.tar.gz',
      },
      encoding: 'utf8',
    });
    assert.notEqual(noPluginResult.status, 0);
    assert.match(noPluginResult.stderr, /Refusing _no-plugin CPA artifact/);

    // 2b. A component inventory the build never collected fails closed
    // instead of shipping a package that silently drops a linked closure.
    const missingComponentResult = spawnSync('bash', [packageNativeScript], {
      env: {
        ...pkgEnv,
        OUT_DIR: path.join(baseDir, 'dist-missing-component'),
        CPAMP_TEST_OMIT_COMPONENT: 'ingress',
      },
      encoding: 'utf8',
    });
    assert.notEqual(missingComponentResult.status, 0);
    assert.match(missingComponentResult.stderr, /STOP: invalid provenance evidence/);
    assert.match(missingComponentResult.stderr, /share\/licenses\/ingress\/inventory\.json/);

    // 2c. An inventory that references a license text the build does not carry
    // must fail closed: hashes alone never stand in for the original text.
    const missingTextResult = spawnSync('bash', [packageNativeScript], {
      env: {
        ...pkgEnv,
        OUT_DIR: path.join(baseDir, 'dist-missing-text'),
        CPAMP_TEST_MISSING_NOTICE: 'modernc.org/sqlite',
      },
      encoding: 'utf8',
    });
    assert.notEqual(missingTextResult.status, 0);
    assert.match(missingTextResult.stderr, /STOP: invalid provenance evidence/);
    assert.match(missingTextResult.stderr, /share\/licenses\/manager\/fixture\//);

    // 2c-bis. The checked-in source ledger declares every linked Go module; a
    // collected inventory that omits one of them is incomplete even when every
    // text it does ship is intact.
    const incompleteCoverageResult = spawnSync('bash', [packageNativeScript], {
      env: {
        ...pkgEnv,
        OUT_DIR: path.join(baseDir, 'dist-incomplete-coverage'),
        CPAMP_TEST_DROP_COMPONENT: 'runtime-supervisor',
        CPAMP_TEST_DROP_MODULE: 'modernc.org/sqlite',
      },
      encoding: 'utf8',
    });
    assert.notEqual(incompleteCoverageResult.status, 0);
    assert.match(incompleteCoverageResult.stderr, /STOP: invalid provenance evidence/);
    assert.match(
      incompleteCoverageResult.stderr,
      /incomplete declared Go module coverage: runtime-supervisor/
    );

    // 2d. Provenance cannot claim a moving ref as the source commit.
    const badCommitResult = spawnSync('bash', [packageNativeScript], {
      env: {
        ...pkgEnv,
        OUT_DIR: path.join(baseDir, 'dist-bad-commit'),
        SOURCE_COMMIT: 'HEAD',
      },
      encoding: 'utf8',
    });
    assert.notEqual(badCommitResult.status, 0);
    assert.match(badCommitResult.stderr, /SOURCE_COMMIT must be an exact 40-hex commit/);

    // 2e. Binaries built by different Go toolchains cannot share one
    // standard-library notice set, so mixed inputs fail closed.
    const mixedToolchainResult = spawnSync('bash', [packageNativeScript], {
      env: {
        ...pkgEnv,
        OUT_DIR: path.join(baseDir, 'dist-mixed-toolchain'),
        CPAMP_TEST_GOVERSION: 'go1.24.13',
        CPAMP_TEST_GOROOT: harness.goroot124,
        CPAMP_TEST_INGRESS_GO_VERSION: 'go1.26.1',
      },
      encoding: 'utf8',
    });
    assert.notEqual(mixedToolchainResult.status, 0);
    assert.match(
      mixedToolchainResult.stderr,
      /bin\/cpa-manager-plus-ingress was built with go1\.26\.1 but the collector toolchain is go1\.24\.13/
    );

    // 2f. GOROOT is authoritative for the standard-library notices: a root
    // whose VERSION differs from the running tool must never be collected.
    const staleGorootResult = spawnSync('bash', [packageNativeScript], {
      env: {
        ...pkgEnv,
        OUT_DIR: path.join(baseDir, 'dist-stale-goroot'),
        CPAMP_TEST_GOROOT: harness.staleGoroot,
      },
      encoding: 'utf8',
    });
    assert.notEqual(staleGorootResult.status, 0);
    assert.match(
      staleGorootResult.stderr,
      /GOROOT .*stale-goroot contains go1\.20\.14 but go env GOVERSION reports go1\.26\.1/
    );

    // 2g. A binary from another toolchain than the collector fails closed.
    const collectorMismatchResult = spawnSync('bash', [packageNativeScript], {
      env: {
        ...pkgEnv,
        OUT_DIR: path.join(baseDir, 'dist-collector-toolchain'),
        CPAMP_TEST_MANAGER_GO_VERSION: 'go1.24.13',
      },
      encoding: 'utf8',
    });
    assert.notEqual(collectorMismatchResult.status, 0);
    assert.match(
      collectorMismatchResult.stderr,
      /bin\/cpa-manager-plus was built with go1\.24\.13 but the collector toolchain is go1\.26\.1/
    );

    // 2h. v2 packaging builds from source; prebuilt binaries cannot be bound to
    // the embedded Full Web file set and must be refused.
    const prebuiltResult = spawnSync('bash', [packageNativeScript], {
      env: {
        ...pkgEnv,
        OUT_DIR: path.join(baseDir, 'dist-prebuilt'),
        CPAMP_PREBUILT_MANAGER_BIN: path.join(binDir, 'cpa-manager-plus'),
        CPAMP_PREBUILT_INGRESS_BIN: path.join(binDir, 'cpa-manager-plus-ingress'),
        CPAMP_PREBUILT_RUNTIME_SUPERVISOR_BIN: path.join(
          binDir,
          'cpa-manager-plus-runtime-supervisor'
        ),
      },
      encoding: 'utf8',
    });
    assert.notEqual(prebuiltResult.status, 0);
    assert.match(
      prebuiltResult.stderr,
      /v2 Native packaging builds Manager, Ingress and Runtime Supervisor from source/
    );

    // 2i. A custom CPA archive SHA with the opt-in builds a local package that
    // must never claim the official release or VERIFIED status.
    const cpaArchiveDir = path.join(baseDir, 'cpa-archive');
    mkdirSync(cpaArchiveDir, { recursive: true });
    writeFileSync(path.join(cpaArchiveDir, 'cli-proxy-api'), 'fake cpa archive binary\n');
    writeFileSync(path.join(cpaArchiveDir, 'LICENSE'), 'CLIProxyAPI archive license\n');
    const cpaArchivePath = path.join(baseDir, 'CLIProxyAPI_8.0.55_linux_amd64.tar.gz');
    execFileSync('tar', ['-czf', cpaArchivePath, '-C', cpaArchiveDir, 'cli-proxy-api', 'LICENSE']);
    const archiveOut = path.join(baseDir, 'dist-custom-archive');
    const archiveResult = spawnSync('bash', [packageNativeScript], {
      env: {
        ...pkgEnv,
        OUT_DIR: archiveOut,
        CPA_BINARY_PATH: '',
        CPA_ARCHIVE_PATH: cpaArchivePath,
        CPAMP_CPA_ARCHIVE_SHA256: fixtureFileDigest(cpaArchivePath).slice('sha256:'.length),
        CPAMP_CPA_EXECUTABLE_SHA256: fixtureFileDigest(
          path.join(cpaArchiveDir, 'cli-proxy-api')
        ).slice('sha256:'.length),
      },
      encoding: 'utf8',
    });
    assert.equal(archiveResult.status, 0, archiveResult.stderr);
    assert.match(archiveResult.stderr, /provenance will be labelled UNPINNED_LOCAL_CPA/);
    const archiveExtract = path.join(baseDir, 'extracted-archive');
    mkdirSync(archiveExtract, { recursive: true });
    execFileSync('tar', [
      '--no-same-owner',
      '-xzf',
      path.join(archiveOut, 'cpa-manager-plus-native-v2_v2.0.0-beta.1_linux_amd64.tar.gz'),
      '-C',
      archiveExtract,
    ]);
    const archivePkgRoot = path.join(
      archiveExtract,
      'cpa-manager-plus-native-v2_v2.0.0-beta.1_linux_amd64'
    );
    const archiveProvenance = JSON.parse(
      readFileSync(path.join(archivePkgRoot, 'share/cpamp/provenance.json'), 'utf8')
    );
    assert.equal(archiveProvenance.status, 'UNPINNED_LOCAL_CPA');
    const archiveCpa = archiveProvenance.components.find((item) => item.name === 'cpa');
    assert.equal(archiveCpa.source, 'local-unpinned-cpa');
    assert.equal(archiveCpa.release, undefined);
    assert.equal(
      JSON.parse(
        readFileSync(path.join(archivePkgRoot, 'share/cpamp/release/third-party-inventory.txt'))
      ).status,
      'UNPINNED_LOCAL_CPA'
    );

    // 2j-2l. The staged embed tree is verified exactly like the Go test would:
    // an extra placeholder, a deleted asset or a modified asset must fail
    // before the first go build.
    for (const [mutation, pattern] of [
      ['add-gitkeep', /unexpected file: \.gitkeep/],
      ['delete-asset', /missing manifest entry/],
      ['modify-asset', /digest mismatch/],
    ]) {
      writeFileSync(harness.orderLog, '');
      const stagingResult = spawnSync('bash', [packageNativeScript], {
        env: {
          ...pkgEnv,
          OUT_DIR: path.join(baseDir, `dist-staging-${mutation}`),
          CPAMP_TEST_FULLWEB_MUTATION: mutation,
        },
        encoding: 'utf8',
      });
      assert.notEqual(stagingResult.status, 0, `staging mutation ${mutation} must fail`);
      assert.match(stagingResult.stderr, pattern);
      assert.ok(
        !readFileSync(harness.orderLog, 'utf8').includes('go build '),
        `staging mutation ${mutation} must fail before go build`
      );
    }

    // 2m. Different roots with the same toolchain are still a mismatch.
    const differentRootResult = spawnSync('bash', [packageNativeScript], {
      env: {
        ...pkgEnv,
        OUT_DIR: path.join(baseDir, 'dist-different-root'),
        CPAMP_TEST_GOROOT_INGRESS: harness.gorootB,
      },
      encoding: 'utf8',
    });
    assert.notEqual(differentRootResult.status, 0);
    assert.match(differentRootResult.stderr, /Go toolchains differ across module directories/);

    // 2n. A per-module GOVERSION difference is rejected even when each root is
    // self-consistent.
    const differentVersionResult = spawnSync('bash', [packageNativeScript], {
      env: {
        ...pkgEnv,
        OUT_DIR: path.join(baseDir, 'dist-different-version'),
        CPAMP_TEST_GOROOT_INGRESS: harness.goroot124,
        CPAMP_TEST_GOVERSION_INGRESS: 'go1.24.13',
      },
      encoding: 'utf8',
    });
    assert.notEqual(differentVersionResult.status, 0);
    assert.match(differentVersionResult.stderr, /Go toolchains differ across module directories/);

    // 3. Builds valid v2 linux/amd64 archive. Reset the recorder so the
    // ordering assertion below only sees this successful run.
    writeFileSync(harness.orderLog, '');
    execFileSync('bash', [packageNativeScript], { env: pkgEnv, encoding: 'utf8' });

    // 3a. Declared modules are downloaded before the collector runs, matching
    // the image build order: a linux/amd64 build need not fetch every module.
    const order = readFileSync(harness.orderLog, 'utf8').split('\n');
    const downloadIndexes = [];
    for (const directory of ['apps/manager-server', 'apps/ingress', 'apps/runtime-supervisor']) {
      const index = order.findIndex(
        (line) => line.startsWith('go mod download') && line.endsWith(`@ ${path.join(repoRoot, directory)}`)
      );
      assert.notEqual(index, -1, `go mod download must run in ${directory}`);
      downloadIndexes.push(index);
    }
    const firstCollect = order.findIndex((line) => line.startsWith('collect '));
    assert.ok(firstCollect >= 0, 'the license collector must run');
    assert.ok(
      Math.max(...downloadIndexes) < firstCollect,
      'every go mod download must precede license collection'
    );

    // 3b. The staged embed tree is verified against its manifest before any
    // binary is compiled, exactly like the image build.
    const goTestIndex = order.findIndex(
      (line) => line.startsWith('go test ') && line.includes('TestPackagedFullWeb(Dist|Manifest)')
    );
    assert.notEqual(goTestIndex, -1, 'the staged Full Web embed must be tested');
    const goBuildIndex = order.findIndex((line) => line.startsWith('go build '));
    assert.ok(goBuildIndex >= 0, 'the binaries must be built');
    assert.ok(goTestIndex < goBuildIndex, 'the embed test must precede go build');

    // 3c. The collector receives the resolved GOROOT explicitly instead of
    // re-deriving one from a possibly different environment.
    for (const line of order.filter(
      (item) => item.startsWith('collect ') && !item.startsWith('collect web ')
    )) {
      assert.ok(
        line.endsWith(`goroot=${harness.goroot}`),
        `collector must use the resolved GOROOT: ${line}`
      );
    }

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
      'share/licenses/cpamp/LICENSE',
      'share/licenses/manager/inventory.json',
      'share/licenses/web/inventory.json',
      'share/licenses/ingress/inventory.json',
      'share/licenses/runtime-supervisor/inventory.json',
      'share/cpamp/release/third-party-inventory.txt',
      'share/cpamp/release/source-dependency-inventory.json',
      'share/cpamp/release/fullweb-manifest.json',
      'share/cpamp/provenance.json',
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
      version: '8.0.55',
      artifactId: `sha256:${cpaSha}`,
    });

    // 4b. The archive carries the same license/provenance contract as images:
    // per-component original texts, an aggregate inventory, and a record that
    // binds the packaged bytes and the CPA upstream artifact.
    assert.deepEqual(
      readFileSync(path.join(pkgRoot, 'share/licenses/cpamp/LICENSE')),
      readFileSync(path.join(repoRoot, 'LICENSE'))
    );
    assert.deepEqual(
      readFileSync(path.join(pkgRoot, 'share/licenses/cli-proxy-api/LICENSE')),
      readFileSync(cpaLicensePath)
    );

    const provenance = JSON.parse(
      readFileSync(path.join(pkgRoot, 'share/cpamp/provenance.json'), 'utf8')
    );
    const sourceCommit = execFileSync('git', ['rev-parse', 'HEAD'], {
      cwd: repoRoot,
      encoding: 'utf8',
    }).trim();
    assert.equal(provenance.schemaVersion, 1);
    assert.equal(provenance.role, 'native');
    assert.equal(provenance.version, 'v2.0.0-beta.1');
    assert.equal(provenance.sourceCommit, sourceCommit);
    assert.equal(provenance.platform, 'linux/amd64');
    assert.equal(provenance.source, 'https://github.com/seakee/CPA-Manager-Plus');
    assert.deepEqual(
      provenance.components.map((item) => item.name),
      ['manager', 'ingress', 'runtime-supervisor', 'cpa', 'web']
    );
    for (const item of provenance.components) {
      assert.equal(
        item.sha256,
        fixtureFileDigest(path.join(pkgRoot, item.path)),
        `provenance must bind ${item.path}`
      );
    }
    // The fixture CPA is not the pinned official artifact, so the package is
    // local evidence: it must never claim the official release or VERIFIED.
    assert.equal(provenance.status, 'UNPINNED_LOCAL_CPA');
    const cpaProvenance = provenance.components.find((item) => item.name === 'cpa');
    assert.equal(cpaProvenance.version, '8.0.55');
    assert.equal(cpaProvenance.sha256, `sha256:${cpaSha}`);
    assert.equal(cpaProvenance.source, 'local-unpinned-cpa');
    assert.equal(cpaProvenance.release, undefined);
    assert.equal(
      provenance.components.find((item) => item.name === 'manager').module,
      'github.com/seakee/cpa-manager-plus/apps/manager-server'
    );

    const aggregatePath = path.join(pkgRoot, 'share/cpamp/release/third-party-inventory.txt');
    const aggregateBytes = readFileSync(aggregatePath);
    assert.equal(provenance.licenseInventorySHA256, fixtureDigest(aggregateBytes));
    const aggregate = JSON.parse(aggregateBytes);
    assert.equal(aggregate.schemaVersion, 1);
    assert.equal(aggregate.status, 'UNPINNED_LOCAL_CPA');
    assert.equal(aggregate.role, 'native');
    assert.deepEqual(Object.keys(aggregate.components).sort(), [
      'ingress',
      'manager',
      'runtime-supervisor',
      'web',
    ]);
    for (const [component, entry] of Object.entries(aggregate.components)) {
      const inventoryPath = path.join(pkgRoot, 'share/licenses', component, 'inventory.json');
      assert.equal(entry.inventorySHA256, fixtureFileDigest(inventoryPath));
      const inventory = JSON.parse(readFileSync(inventoryPath, 'utf8'));
      assert.equal(inventory.component, component);
      for (const dependency of inventory.dependencies) {
        assert.ok(
          dependency.notices.length > 0,
          `${component}/${dependency.name} needs an original notice`
        );
        for (const notice of dependency.notices) {
          assert.equal(
            notice.sha256,
            fixtureFileDigest(path.join(pkgRoot, 'share/licenses', component, notice.path)),
            `notice text must match its inventory digest: ${component}/${notice.path}`
          );
        }
      }
    }
    assert.equal(
      JSON.parse(
        readFileSync(
          path.join(pkgRoot, 'share/cpamp/release/source-dependency-inventory.json'),
          'utf8'
        )
      ).status,
      'SOURCE_MANIFESTS_VERIFIED'
    );

    // 4c. The packaged inventories must cover the checked-in source ledger,
    // not merely hash whatever the build happened to collect.
    const sourceLedger = JSON.parse(
      readFileSync(path.join(repoRoot, 'bin/release/embedded-third-party-inventory.txt'), 'utf8')
    );
    for (const component of ['manager', 'ingress', 'runtime-supervisor']) {
      const inventory = JSON.parse(
        readFileSync(path.join(pkgRoot, 'share/licenses', component, 'inventory.json'), 'utf8')
      );
      const identities = new Set(
        inventory.dependencies.map((dependency) => `${dependency.name}@${dependency.version}`)
      );
      for (const module of sourceLedger.goModules[component]) {
        assert.equal(
          identities.has(`${module.name}@${module.version}`),
          true,
          `${component} must license declared module ${module.name}@${module.version}`
        );
      }
      assert.equal(
        identities.has('Go standard library and bundled sources@go1.26.1'),
        true,
        `${component} must license the Go standard library`
      );
    }
    const webInventory = JSON.parse(
      readFileSync(path.join(pkgRoot, 'share/licenses/web/inventory.json'), 'utf8')
    );
    const webNames = new Set(webInventory.dependencies.map((dependency) => dependency.name));
    for (const name of Object.keys(sourceLedger.webDependencies)) {
      assert.equal(webNames.has(name), true, `web must license direct dependency ${name}`);
    }

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
      const harness = createLicenseTestHarness(baseDir);

      const pkgEnv = {
        ...process.env,
        VERSION: 'v2.0.0-beta.1',
        PACKAGE_MODE: 'v2',
        CPA_BINARY_PATH: cpaBin,
        CPA_LICENSE_PATH: cpaLicensePath,
        CPAMP_CPA_VERSION: '8.0.55',
        CPAMP_CPA_EXECUTABLE_SHA256: cpaSha,
        CPAMP_ALLOW_UNPINNED_CPA: '1',
        ...harness.env,
        WEB_HTML: harness.webHtml,
        FULL_WEB_DIST: harness.fullWebDist,
        CPAMP_TEST_BUILD_MANAGER_BIN: path.join(binDir, 'cpa-manager-plus'),
        CPAMP_TEST_BUILD_INGRESS_BIN: path.join(binDir, 'cpa-manager-plus-ingress'),
        CPAMP_TEST_BUILD_RUNTIME_SUPERVISOR_BIN: path.join(
          binDir,
          'cpa-manager-plus-runtime-supervisor'
        ),
        CPAMP_SKIP_PACKAGE_DOCS: '1',
        // Force this exact tar implementation for both builds; the harness
        // stubs stay first so license collection remains deterministic.
        PATH: `${harness.binDir}:${path.dirname(tarPath)}:${process.env.PATH}`,
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

  it('enforces NH2 CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK=1 across static unit, generated unit, ctl start, and supervisor subshell only', async () => {
    const baseDir = makeTempDir('cpamp-native-nh2-');
    const { homeDir, runDir } = createMockNativeHome(baseDir);
    const ctl = path.join(homeDir, 'cpa-manager-plusctl');
    const systemdDir = path.join(baseDir, 'systemd');
    const envDumpDir = path.join(baseDir, 'env-dumps');
    mkdirSync(envDumpDir, { recursive: true });

    const staticUnit = readFileSync(
      path.join(repoRoot, 'bin/native/cpa-manager-plus.service'),
      'utf8'
    );
    assert.match(staticUnit, /^Environment="CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK=1"$/m);

    const rawEnv = await buildPortEnv(homeDir, {
      CPA_MANAGER_PLUS_SKIP_SYSTEMCTL: '1',
      MOCK_ENV_DUMP_DIR: envDumpDir,
    });
    const { _ports: _ports, ...env } = rawEnv;

    execFileSync('bash', [ctl, 'install', '--unit-dir', systemdDir, '--no-systemctl'], {
      env,
      encoding: 'utf8',
    });
    const generatedUnit = readFileSync(
      path.join(systemdDir, 'cpa-manager-plus.service'),
      'utf8'
    );
    assert.match(generatedUnit, /^Environment="CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK=1"$/m);

    execFileSync('bash', [ctl, 'start'], { env, encoding: 'utf8' });
    try {
      assert.equal(await waitForFile(path.join(runDir, 'launcher.ready')), true);
      const runtimeDump = JSON.parse(
        readFileSync(path.join(envDumpDir, 'runtime-supervisor-env.json'), 'utf8')
      );
      const managerDump = JSON.parse(
        readFileSync(path.join(envDumpDir, 'manager-env.json'), 'utf8')
      );
      const ingressDump = JSON.parse(
        readFileSync(path.join(envDumpDir, 'ingress-env.json'), 'utf8')
      );
      assert.equal(runtimeDump.env.CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK, '1');
      assert.equal(managerDump.env.CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK, undefined);
      assert.equal(ingressDump.env.CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK, undefined);
    } finally {
      spawnSync('bash', [ctl, 'stop'], { env, encoding: 'utf8' });
    }
  });

  it('clamps overflowing/invalid CPAMP_DRAIN_TIMEOUT to 90s and unconditionally exports CPAMP_INGRESS_SHUTDOWN_TIMEOUT', async () => {
    const invalidCases = [
      '9999999999',
      '99999999999999999999',
      '3000000h',
      '-5s',
      '0s',
      '0.5ns',
      '3601s',
      '1h1ns',
      'invalid',
    ];
    for (const badValue of invalidCases) {
      const baseDir = makeTempDir('cpamp-native-drain-bad-');
      const { homeDir, runDir } = createMockNativeHome(baseDir);
      const envDumpDir = path.join(baseDir, 'env-dumps');
      mkdirSync(envDumpDir, { recursive: true });

      const rawEnv = await buildPortEnv(homeDir, {
        MOCK_ENV_DUMP_DIR: envDumpDir,
        CPAMP_DRAIN_TIMEOUT: badValue,
        CPAMP_INGRESS_SHUTDOWN_TIMEOUT: '300s',
      });
      const { _ports: _ports, ...env } = rawEnv;
      const proc = spawn('bash', [path.join(homeDir, 'bin/cpa-manager-plus-launcher')], {
        env,
        stdio: ['ignore', 'pipe', 'pipe'],
      });
      activeProcesses.push(proc);
      let stderr = '';
      proc.stderr.on('data', (chunk) => {
        stderr += chunk.toString();
      });
      try {
        assert.equal(await waitForFile(path.join(runDir, 'launcher.ready')), true);
        const ingressDump = JSON.parse(
          readFileSync(path.join(envDumpDir, 'ingress-env.json'), 'utf8')
        );
        assert.equal(
          ingressDump.env.CPAMP_INGRESS_SHUTDOWN_TIMEOUT,
          '90s',
          `bad CPAMP_DRAIN_TIMEOUT=${badValue} must fall back to 90s`
        );
        assert.match(stderr, /Invalid CPAMP_DRAIN_TIMEOUT/);
      } finally {
        proc.kill('SIGTERM');
        await waitForExit(proc);
      }
    }

    const validCases = [
      ['200ms', '200ms'],
      ['1m30s', '1m30s'],
      ['500µs', '500us'],
      ['90', '90s'],
    ];
    for (const [input, expected] of validCases) {
      const baseDir = makeTempDir('cpamp-native-drain-ok-');
      const { homeDir, runDir } = createMockNativeHome(baseDir);
      const envDumpDir = path.join(baseDir, 'env-dumps');
      mkdirSync(envDumpDir, { recursive: true });

      const rawEnv = await buildPortEnv(homeDir, {
        MOCK_ENV_DUMP_DIR: envDumpDir,
        CPAMP_DRAIN_TIMEOUT: input,
        CPAMP_INGRESS_SHUTDOWN_TIMEOUT: '300s',
      });
      const { _ports: _ports, ...env } = rawEnv;
      const proc = spawn('bash', [path.join(homeDir, 'bin/cpa-manager-plus-launcher')], {
        env,
        stdio: ['ignore', 'pipe', 'pipe'],
      });
      activeProcesses.push(proc);
      try {
        assert.equal(await waitForFile(path.join(runDir, 'launcher.ready')), true);
        const ingressDump = JSON.parse(
          readFileSync(path.join(envDumpDir, 'ingress-env.json'), 'utf8')
        );
        assert.equal(
          ingressDump.env.CPAMP_INGRESS_SHUTDOWN_TIMEOUT,
          expected,
          `valid CPAMP_DRAIN_TIMEOUT=${input} must override stale CPAMP_INGRESS_SHUTDOWN_TIMEOUT`
        );
      } finally {
        proc.kill('SIGTERM');
        await waitForExit(proc);
      }
    }
  });

  it('keeps serial stop worst-case budgets bounded within TimeoutStopSec=150', () => {
    const launcherContent = readFileSync(launcherScript, 'utf8');
    assert.match(launcherContent, /ingress_budget_sec=\$\(\(drain_timeout_sec \+ 5\)\)/);
    assert.match(launcherContent, /manager_stop_timeout_sec=\$\(\(\s*stop_timeout_sec < 10 \? stop_timeout_sec : 10\s*\)\)/);
    assert.match(launcherContent, /runtime_stop_timeout_sec=\$\(\(\s*stop_timeout_sec < 10 \? stop_timeout_sec : 10\s*\)\)/);
    assert.match(launcherContent, /cpa_stop_timeout_sec=\$\(\(\s*stop_timeout_sec < 5 \? stop_timeout_sec : 5\s*\)\)/);
    assert.match(
      launcherContent,
      /stop_child_process "ingress" "\$\{ingress_pid\}" "\$\{ingress_pid_file\}" "\$\{ingress_deadline\}" "\$\{ingress_budget_sec\}" 3/
    );
    assert.match(
      launcherContent,
      /stop_child_process "manager" "\$\{manager_pid\}" "\$\{manager_pid_file\}" "\$\{manager_deadline\}" "\$\{manager_stop_timeout_sec\}" 2/
    );
    assert.match(
      launcherContent,
      /stop_child_process "runtime-supervisor" "\$\{runtime_pid\}" "\$\{runtime_pid_file\}" "\$\{runtime_deadline\}" "\$\{runtime_stop_timeout_sec\}" 2/
    );

    const ctlContent = readFileSync(ctlScript, 'utf8');
    assert.match(ctlContent, /stop_timeout=\$\(\(drain_sec \+ 45\)\)/);
  });

  it('supports atomic root-safe cpa-manager-plusctl upgrade, enforces safety checks, and rolls back cleanly on swap failure', async () => {
    const createBundleDir = (parentDir, name, versionTag) => {
      const { homeDir: bundleDir } = createMockNativeHome(path.join(parentDir, name));
      mkdirSync(path.join(bundleDir, 'share/cpamp'), { recursive: true });
      mkdirSync(path.join(bundleDir, 'licenses'), { recursive: true });
      mkdirSync(path.join(bundleDir, 'docs'), { recursive: true });
      writeFileSync(
        path.join(bundleDir, 'share/cpamp/provenance.json'),
        JSON.stringify({ version: versionTag }) + '\n'
      );
      writeFileSync(
        path.join(bundleDir, 'share/cpamp/runtime/seed-lineage.json'),
        JSON.stringify({ seed: versionTag }) + '\n'
      );
      writeFileSync(path.join(bundleDir, 'README.md'), `release ${versionTag}\n`);
      writeFileSync(path.join(bundleDir, 'LICENSE'), 'MIT\n');
      writeFileSync(path.join(bundleDir, 'THIRD_PARTY_NOTICES.txt'), `notices ${versionTag}\n`);
      writeFileSync(path.join(bundleDir, 'licenses/app.txt'), `license ${versionTag}\n`);
      writeFileSync(path.join(bundleDir, 'docs/notes.md'), `docs ${versionTag}\n`);
      return bundleDir;
    };

    // 1. Refuses non-root invocation without test override
    if (typeof process.getuid === 'function' && process.getuid() !== 0) {
      const baseDir = makeTempDir('cpamp-upgrade-nonroot-');
      const { homeDir: targetHome } = createMockNativeHome(path.join(baseDir, 'target'));
      const bundleDir = createBundleDir(baseDir, 'bundle', 'v2.0.0-beta.2');
      const nonRootRes = spawnSync(
        'bash',
        [path.join(bundleDir, 'cpa-manager-plusctl'), 'upgrade', '--home', targetHome, '--no-systemctl'],
        { encoding: 'utf8' }
      );
      assert.notEqual(nonRootRes.status, 0);
      assert.match(nonRootRes.stderr, /sudo/);
    }

    // 2. Refuses running upgrade from inside target_home
    {
      const baseDir = makeTempDir('cpamp-upgrade-self-');
      const targetHome = createBundleDir(baseDir, 'target', 'v2.0.0-beta.1');
      const selfRes = spawnSync(
        'bash',
        [path.join(targetHome, 'cpa-manager-plusctl'), 'upgrade', '--home', targetHome, '--no-systemctl'],
        {
          env: { ...process.env, CPAMP_TEST_ALLOW_NON_ROOT_UPGRADE: '1' },
          encoding: 'utf8',
        }
      );
      assert.notEqual(selfRes.status, 0);
      assert.match(selfRes.stderr, /inside target installation/);
    }

    // 3. Refuses symlinks inside bundle bin/ or share/
    {
      const baseDir = makeTempDir('cpamp-upgrade-symlink-');
      const { homeDir: targetHome } = createMockNativeHome(path.join(baseDir, 'target'));
      const bundleDir = createBundleDir(baseDir, 'bundle', 'v2.0.0-beta.2');
      symlinkSync('/etc/passwd', path.join(bundleDir, 'share/cpamp/evil-link'));
      const symRes = spawnSync(
        'bash',
        [path.join(bundleDir, 'cpa-manager-plusctl'), 'upgrade', '--home', targetHome, '--no-systemctl'],
        {
          env: { ...process.env, CPAMP_TEST_ALLOW_NON_ROOT_UPGRADE: '1' },
          encoding: 'utf8',
        }
      );
      assert.notEqual(symRes.status, 0);
      assert.match(symRes.stderr, /refusing symlink/);
    }

    // 4. Performs atomic upgrade with systemd mock, preserves data + .env, strips go+w
    {
      const baseDir = makeTempDir('cpamp-upgrade-ok-');
      const targetHome = createBundleDir(baseDir, 'target', 'v2.0.0-beta.1');
      const bundleDir = createBundleDir(baseDir, 'bundle', 'v2.0.0-beta.2');
      const systemdDir = path.join(baseDir, 'systemd');
      const systemctlLog = path.join(baseDir, 'systemctl.log');
      const mockSystemctl = path.join(baseDir, 'mock-systemctl');

      // Simulate permissive group/world write bits in extracted tarball
      chmodSync(path.join(bundleDir, 'bin/cpa-manager-plus'), 0o777);
      chmodSync(path.join(bundleDir, 'cpa-manager-plusctl'), 0o777);
      chmodSync(path.join(bundleDir, 'share/cpamp/provenance.json'), 0o666);

      // Populate persistent user data & env file in targetHome
      writeFileSync(path.join(targetHome, 'data/usage.sqlite'), 'sqlite-data-keep');
      writeFileSync(path.join(targetHome, 'data/data.key'), 'secret-key-keep');
      writeFileSync(path.join(targetHome, 'cpa-manager-plus.env'), 'CPAMP_CUSTOM_ENV=keep\n');

      const systemdStateFile = path.join(baseDir, 'systemd-state');
      const unitPidFile = path.join(baseDir, 'unit.pid');
      writeFileSync(systemdStateFile, 'active\n');
      writeFileSync(
        mockSystemctl,
        [
          '#!/usr/bin/env bash',
          `printf '%s\\n' "$*" >> "${systemctlLog}"`,
          `state_file="${systemdStateFile}"`,
          `pid_file="${unitPidFile}"`,
          `launcher_bin="${targetHome}/bin/cpa-manager-plus-launcher"`,
          `log_out="${targetHome}/logs/cpa-manager-plus.log"`,
          'case "${1:-}" in',
          '  is-active)',
          '    [ "$(cat "${state_file}" 2>/dev/null || true)" = "active" ] && exit 0',
          '    exit 3',
          '    ;;',
          '  show)',
          '    if [ "$(cat "${state_file}" 2>/dev/null || true)" = "active" ]; then',
          '      printf "loaded\\nactive\\nrunning\\n"',
          '    else',
          '      printf "loaded\\ninactive\\ndead\\n"',
          '    fi',
          '    exit 0',
          '    ;;',
          '  stop)',
          '    if [ -f "${pid_file}" ]; then',
          '      up="$(cat "${pid_file}" 2>/dev/null || true)"',
          '      if [ -n "${up}" ] && kill -0 "${up}" >/dev/null 2>&1; then',
          '        kill -TERM "${up}" >/dev/null 2>&1 || true',
          '        for _ in $(seq 1 30); do',
          '          kill -0 "${up}" >/dev/null 2>&1 || break',
          '          sleep 0.1',
          '        done',
          '      fi',
          '      rm -f "${pid_file}"',
          '    fi',
          '    echo "inactive" > "${state_file}"',
          '    exit 0',
          '    ;;',
          '  start)',
          '    mkdir -p "$(dirname "${log_out}")"',
          '    "${launcher_bin}" </dev/null >>"${log_out}" 2>&1 &',
          '    echo "$!" > "${pid_file}"',
          '    echo "active" > "${state_file}"',
          '    exit 0',
          '    ;;',
          'esac',
          '',
        ].join('\n'),
        { mode: 0o755 }
      );
      chmodSync(mockSystemctl, 0o755);

      const rawEnv = await buildPortEnv(targetHome, {
        CPAMP_TEST_ALLOW_NON_ROOT_UPGRADE: '1',
        CPA_MANAGER_PLUS_SYSTEMCTL_BIN: mockSystemctl,
      });
      const { _ports: _ports, ...env } = rawEnv;

      try {
        const upgradeOut = execFileSync(
          'bash',
          [
            path.join(bundleDir, 'cpa-manager-plusctl'),
            'upgrade',
            '--home',
            targetHome,
            '--unit-dir',
            systemdDir,
          ],
          { env, encoding: 'utf8' }
        );
        assert.match(upgradeOut, /Installed systemd unit:/);

        // New files installed cleanly
        assert.equal(
          JSON.parse(readFileSync(path.join(targetHome, 'share/cpamp/provenance.json'), 'utf8')).version,
          'v2.0.0-beta.2'
        );
        assert.equal(
          JSON.parse(readFileSync(path.join(targetHome, 'share/cpamp/runtime/seed-lineage.json'), 'utf8'))
            .seed,
          'v2.0.0-beta.2'
        );
        assert.equal(readFileSync(path.join(targetHome, 'THIRD_PARTY_NOTICES.txt'), 'utf8'), 'notices v2.0.0-beta.2\n');
        assert.equal(
          readFileSync(path.join(targetHome, 'cpa-manager-plus-launcher'), 'utf8'),
          readFileSync(launcherScript, 'utf8')
        );
        assert.equal(
          readFileSync(path.join(targetHome, 'cpa-manager-plusctl'), 'utf8'),
          readFileSync(ctlScript, 'utf8')
        );

        // Group/world write bits stripped
        assert.equal(statSync(path.join(targetHome, 'bin/cpa-manager-plus')).mode & 0o022, 0);
        assert.equal(statSync(path.join(targetHome, 'cpa-manager-plusctl')).mode & 0o022, 0);
        assert.equal(statSync(path.join(targetHome, 'share/cpamp/provenance.json')).mode & 0o022, 0);

        // Persistent state preserved
        assert.equal(readFileSync(path.join(targetHome, 'data/usage.sqlite'), 'utf8'), 'sqlite-data-keep');
        assert.equal(readFileSync(path.join(targetHome, 'data/data.key'), 'utf8'), 'secret-key-keep');
        assert.equal(
          readFileSync(path.join(targetHome, 'cpa-manager-plus.env'), 'utf8'),
          'CPAMP_CUSTOM_ENV=keep\n'
        );

        // Systemd unit updated & lifecycle calls executed in order with readiness polling
        const unitContent = readFileSync(path.join(systemdDir, 'cpa-manager-plus.service'), 'utf8');
        assert.match(unitContent, /^TimeoutStopSec=150$/m);
        assert.match(unitContent, /^Environment="CPAMP_DRAIN_TIMEOUT=90s"$/m);
        assert.match(unitContent, /^Environment="CPAMP_SERVICE_STOP_TIMEOUT_SEC=150"$/m);
        assert.equal(existsSync(path.join(targetHome, 'run/launcher.ready')), true);
        const sysLines = readFileSync(systemctlLog, 'utf8').trim().split('\n');
        const expectedPrefix = [
          'show -p LoadState,ActiveState,SubState --value cpa-manager-plus.service',
          'is-active --quiet cpa-manager-plus.service',
          'stop cpa-manager-plus.service',
          'show -p LoadState,ActiveState,SubState --value cpa-manager-plus.service',
          'is-active --quiet cpa-manager-plus.service',
          'daemon-reload',
          'enable cpa-manager-plus.service',
          'start cpa-manager-plus.service',
        ];
        assert.deepEqual(sysLines.slice(0, expectedPrefix.length), expectedPrefix);
        const pollLines = sysLines.slice(expectedPrefix.length);
        assert.ok(pollLines.length >= 4 && pollLines.length % 2 === 0);
        for (let i = 0; i < pollLines.length; i += 2) {
          assert.equal(pollLines[i], 'is-active --quiet cpa-manager-plus.service');
          assert.equal(pollLines[i + 1], 'show -p LoadState,ActiveState,SubState --value cpa-manager-plus.service');
        }
      } finally {
        spawnSync('bash', [path.join(targetHome, 'cpa-manager-plusctl'), 'stop'], {
          env,
          encoding: 'utf8',
        });
        if (existsSync(unitPidFile)) {
          const up = Number(readFileSync(unitPidFile, 'utf8').trim());
          if (up > 0) {
            try { process.kill(up, 'SIGKILL'); } catch {}
          }
        }
      }
    }

    // 5. Rolls back cleanly when mv fails mid-swap
    {
      const baseDir = makeTempDir('cpamp-upgrade-rollback-');
      const targetHome = createBundleDir(baseDir, 'target', 'v2.0.0-beta.1');
      const bundleDir = createBundleDir(baseDir, 'bundle', 'v2.0.0-beta.2');
      const stubBinDir = path.join(baseDir, 'stub-bin');
      mkdirSync(stubBinDir, { recursive: true });

      writeFileSync(path.join(targetHome, 'data/usage.sqlite'), 'keep-on-rollback');
      writeFileSync(path.join(targetHome, 'cpa-manager-plus.env'), 'ROLLBACK_ENV=1\n');
      writeFileSync(path.join(targetHome, 'bin/custom-marker'), 'old-bin-marker\n');

      const realMv = execFileSync('which', ['mv'], { encoding: 'utf8' }).trim();
      const mvStub = path.join(stubBinDir, 'mv');
      writeFileSync(
        mvStub,
        [
          '#!/usr/bin/env bash',
          'case "${1:-}" in',
          '  */.upgrade-stage.*/share)',
          '    echo "simulated mv failure on share" >&2',
          '    exit 1',
          '    ;;',
          'esac',
          `exec "${realMv}" "$@"`,
          '',
        ].join('\n'),
        { mode: 0o755 }
      );
      chmodSync(mvStub, 0o755);

      const rawEnv = await buildPortEnv(targetHome, {
        CPAMP_TEST_ALLOW_NON_ROOT_UPGRADE: '1',
        PATH: `${stubBinDir}:${process.env.PATH || ''}`,
      });
      const { _ports: _ports, ...env } = rawEnv;

      const failRes = spawnSync(
        'bash',
        [path.join(bundleDir, 'cpa-manager-plusctl'), 'upgrade', '--home', targetHome, '--no-systemctl'],
        { env, encoding: 'utf8' }
      );
      assert.notEqual(failRes.status, 0);
      assert.match(failRes.stderr, /rolling back/);

      // Original v2.0.0-beta.1 artifacts + user data are intact
      assert.equal(
        JSON.parse(readFileSync(path.join(targetHome, 'share/cpamp/provenance.json'), 'utf8')).version,
        'v2.0.0-beta.1'
      );
      assert.equal(readFileSync(path.join(targetHome, 'bin/custom-marker'), 'utf8'), 'old-bin-marker\n');
      assert.equal(readFileSync(path.join(targetHome, 'data/usage.sqlite'), 'utf8'), 'keep-on-rollback');
      assert.equal(readFileSync(path.join(targetHome, 'cpa-manager-plus.env'), 'utf8'), 'ROLLBACK_ENV=1\n');
    }

    // 6. Stops ctl-managed running daemon before swap and restarts it afterward when --no-systemctl is used
    {
      const baseDir = makeTempDir('cpamp-upgrade-ctl-live-');
      const targetHome = createBundleDir(baseDir, 'target', 'v2.0.0-beta.1');
      const bundleDir = createBundleDir(baseDir, 'bundle', 'v2.0.0-beta.2');
      const systemdDir = path.join(baseDir, 'systemd');
      const rawEnv = await buildPortEnv(targetHome, {
        CPAMP_TEST_ALLOW_NON_ROOT_UPGRADE: '1',
        CPA_MANAGER_PLUS_SKIP_SYSTEMCTL: '1',
      });
      const { _ports: ports, ...env } = rawEnv;
      // Remove CPA_MANAGER_PLUS_HOME from caller env so --home proves it works standalone
      const upgradeEnv = { ...env };
      delete upgradeEnv.CPA_MANAGER_PLUS_HOME;

      execFileSync('bash', [path.join(targetHome, 'cpa-manager-plusctl'), 'start'], {
        env,
        encoding: 'utf8',
      });
      try {
        execFileSync(
          'bash',
          [
            path.join(bundleDir, 'cpa-manager-plusctl'),
            'upgrade',
            '--home',
            targetHome,
            '--unit-dir',
            systemdDir,
            '--no-systemctl',
          ],
          { env: upgradeEnv, encoding: 'utf8' }
        );
        const statusOut = execFileSync('bash', [path.join(targetHome, 'cpa-manager-plusctl'), 'status'], {
          env,
          encoding: 'utf8',
        });
        assert.match(statusOut, /cpa-manager-plus is running with PID \d+/);
        const healthRes = await fetch(`http://127.0.0.1:${ports.ingressPort}/health`);
        assert.equal(healthRes.status, 200);
        assert.equal(
          JSON.parse(readFileSync(path.join(targetHome, 'share/cpamp/provenance.json'), 'utf8')).version,
          'v2.0.0-beta.2'
        );
      } finally {
        spawnSync('bash', [path.join(targetHome, 'cpa-manager-plusctl'), 'stop'], {
          env,
          encoding: 'utf8',
        });
      }
    }

    // 7. Refuses arbitrary unrelated dirs, symlinks as target_home, and concurrent upgrade locks (clearing dead stale locks)
    {
      const baseDir = makeTempDir('cpamp-upgrade-target-guards-');
      const unrelatedDir = path.join(baseDir, 'unrelated');
      mkdirSync(unrelatedDir, { recursive: true, mode: 0o755 });
      const bundleDir = createBundleDir(baseDir, 'bundle', 'v2.0.0-beta.2');
      const targetHome = createBundleDir(baseDir, 'target', 'v2.0.0-beta.1');
      const symlinkHome = path.join(baseDir, 'symlink-home');
      symlinkSync(targetHome, symlinkHome);

      const guardEnv = {
        ...process.env,
        CPAMP_TEST_ALLOW_NON_ROOT_UPGRADE: '1',
      };

      const unrelatedRes = spawnSync(
        'bash',
        [path.join(bundleDir, 'cpa-manager-plusctl'), 'upgrade', '--home', unrelatedDir, '--no-systemctl'],
        { env: guardEnv, encoding: 'utf8' }
      );
      assert.notEqual(unrelatedRes.status, 0);
      assert.match(unrelatedRes.stderr, /existing CPAMP Native installation/);

      const rootRes = spawnSync(
        'bash',
        [path.join(bundleDir, 'cpa-manager-plusctl'), 'upgrade', '--home', '/', '--no-systemctl'],
        { env: guardEnv, encoding: 'utf8' }
      );
      assert.notEqual(rootRes.status, 0);

      const symlinkHomeRes = spawnSync(
        'bash',
        [path.join(bundleDir, 'cpa-manager-plusctl'), 'upgrade', '--home', symlinkHome, '--no-systemctl'],
        { env: guardEnv, encoding: 'utf8' }
      );
      assert.notEqual(symlinkHomeRes.status, 0);
      assert.match(symlinkHomeRes.stderr, /symlink/);

      // Live PID in .upgrade.lock/pid fails closed
      const lockDir = path.join(targetHome, '.upgrade.lock');
      mkdirSync(lockDir, { mode: 0o700 });
      writeFileSync(path.join(lockDir, 'pid'), `${process.pid}\n`);
      const liveLockRes = spawnSync(
        'bash',
        [path.join(bundleDir, 'cpa-manager-plusctl'), 'upgrade', '--home', targetHome, '--no-systemctl', '--no-restart'],
        { env: guardEnv, encoding: 'utf8' }
      );
      assert.notEqual(liveLockRes.status, 0);
      assert.match(liveLockRes.stderr, /another upgrade is active/);
      assert.equal(existsSync(lockDir), true);

      // Stale dead PID lock is cleared safely and upgrade proceeds
      const deadPid = spawnSync('bash', ['-c', 'echo $$'], { encoding: 'utf8' }).stdout.trim();
      writeFileSync(path.join(lockDir, 'pid'), `${deadPid}\n`);
      const systemdDir = path.join(baseDir, 'systemd');
      const rawEnv = await buildPortEnv(targetHome, { CPAMP_TEST_ALLOW_NON_ROOT_UPGRADE: '1' });
      const { _ports: _ports, ...staleEnv } = rawEnv;
      const staleRes = spawnSync(
        'bash',
        [
          path.join(bundleDir, 'cpa-manager-plusctl'),
          'upgrade',
          '--home',
          targetHome,
          '--unit-dir',
          systemdDir,
          '--no-systemctl',
          '--no-restart',
        ],
        { env: staleEnv, encoding: 'utf8' }
      );
      assert.equal(staleRes.status, 0, staleRes.stderr);
      assert.match(staleRes.stderr, /Clearing stale upgrade lock/);
      assert.equal(existsSync(lockDir), false);

      // Refuses symlinked unit path
      const symlinkUnitDir = path.join(baseDir, 'symlink-units');
      mkdirSync(symlinkUnitDir, { recursive: true });
      symlinkSync('/etc/passwd', path.join(symlinkUnitDir, 'cpa-manager-plus.service'));
      const symUnitRes = spawnSync(
        'bash',
        [
          path.join(bundleDir, 'cpa-manager-plusctl'),
          'upgrade',
          '--home',
          targetHome,
          '--unit-dir',
          symlinkUnitDir,
          '--no-systemctl',
          '--no-restart',
        ],
        { env: staleEnv, encoding: 'utf8' }
      );
      assert.notEqual(symUnitRes.status, 0);
      assert.match(symUnitRes.stderr, /Refusing symlinked systemd unit path/);

      // Refuses conflict PID in cpa-manager-plus.pid without killing foreign process
      const foreignProc = spawn('bash', ['-c', 'sleep 10'], { stdio: 'ignore' });
      try {
        writeFileSync(path.join(targetHome, 'run/cpa-manager-plus.pid'), `${foreignProc.pid}\n`, { mode: 0o600 });
        const conflictRes = spawnSync(
          'bash',
          [
            path.join(bundleDir, 'cpa-manager-plusctl'),
            'upgrade',
            '--home',
            targetHome,
            '--unit-dir',
            systemdDir,
            '--no-systemctl',
            '--no-restart',
          ],
          { env: staleEnv, encoding: 'utf8' }
        );
        assert.notEqual(conflictRes.status, 0);
        assert.match(conflictRes.stderr, /could not be strongly verified/);
        assert.doesNotThrow(() => process.kill(foreignProc.pid, 0));
      } finally {
        foreignProc.kill('SIGKILL');
        await waitForExit(foreignProc);
      }
    }
    // 8. Detects, stops, verifies quiescence, upgrades, and restarts a degraded ctl instance (status exits 1) without starting systemd
    {
      const baseDir = makeTempDir('cpamp-upgrade-degraded-ctl-');
      const targetHome = createBundleDir(baseDir, 'target', 'v2.0.0-beta.1');
      const bundleDir = createBundleDir(baseDir, 'bundle', 'v2.0.0-beta.2');
      const systemdDir = path.join(baseDir, 'systemd');
      const systemctlLog = path.join(baseDir, 'systemctl.log');
      const mockSystemctl = path.join(baseDir, 'mock-systemctl');
      writeFileSync(
        mockSystemctl,
        [
          '#!/usr/bin/env bash',
          `printf '%s\\n' "$*" >> "${systemctlLog}"`,
          'case "${1:-}" in',
          '  is-active) exit 3 ;;',
          '  show) printf "loaded\\ninactive\\ndead\\n"; exit 0 ;;',
          '  *) exit 0 ;;',
          'esac',
          '',
        ].join('\n'),
        { mode: 0o755 }
      );
      chmodSync(mockSystemctl, 0o755);

      const rawEnv = await buildPortEnv(targetHome, {
        CPAMP_TEST_ALLOW_NON_ROOT_UPGRADE: '1',
        CPA_MANAGER_PLUS_SYSTEMCTL_BIN: mockSystemctl,
        CPAMP_MAX_RESTARTS: '3',
        CPAMP_STARTUP_TIMEOUT_SEC: '15',
      });
      const { _ports: ports, ...env } = rawEnv;
      const upgradeEnv = { ...env };
      delete upgradeEnv.CPA_MANAGER_PLUS_HOME;

      execFileSync('bash', [path.join(targetHome, 'cpa-manager-plusctl'), 'start'], {
        env,
        encoding: 'utf8',
      });
      try {
        // Force ingress restart to stall without readiness so launcher stays alive while ctl status exits 1.
        const ingressPid = Number(readFileSync(path.join(targetHome, 'run/ingress.pid'), 'utf8').match(/^pid=(\d+)/m)[1]);
        writeFileSync(
          path.join(targetHome, 'bin/cpa-manager-plus-ingress'),
          '#!/usr/bin/env bash\ntrap "exit 0" TERM INT\nsleep 20 &\nwait $!\n',
          { mode: 0o755 }
        );
        process.kill(ingressPid, 'SIGKILL');

        let degradedSeen = false;
        for (let i = 0; i < 40; i++) {
          const statusRes = spawnSync('bash', [path.join(targetHome, 'cpa-manager-plusctl'), 'status'], {
            env,
            encoding: 'utf8',
          });
          if (statusRes.status !== 0 && /degraded/.test(statusRes.stderr) && /ingress: stopped/.test(statusRes.stdout)) {
            degradedSeen = true;
            break;
          }
          await new Promise((resolve) => setTimeout(resolve, 100));
        }
        assert.equal(degradedSeen, true, 'expected degraded ctl instance where launcher is alive and status exits 1');

        execFileSync(
          'bash',
          [
            path.join(bundleDir, 'cpa-manager-plusctl'),
            'upgrade',
            '--home',
            targetHome,
            '--unit-dir',
            systemdDir,
          ],
          { env: upgradeEnv, encoding: 'utf8' }
        );

        const postStatus = execFileSync('bash', [path.join(targetHome, 'cpa-manager-plusctl'), 'status'], {
          env,
          encoding: 'utf8',
        });
        assert.match(postStatus, /cpa-manager-plus is running with PID \d+/);
        const healthRes = await fetch(`http://127.0.0.1:${ports.ingressPort}/health`);
        assert.equal(healthRes.status, 200);
        assert.equal(
          JSON.parse(readFileSync(path.join(targetHome, 'share/cpamp/provenance.json'), 'utf8')).version,
          'v2.0.0-beta.2'
        );
        const sysCalls = readFileSync(systemctlLog, 'utf8');
        assert.doesNotMatch(sysCalls, /^start /m);
      } finally {
        spawnSync('bash', [path.join(targetHome, 'cpa-manager-plusctl'), 'stop'], {
          env,
          encoding: 'utf8',
        });
      }
    }

    // 9. Hard quiescence aborts BEFORE swapping any files when systemd stays transitioning after stop
    {
      const baseDir = makeTempDir('cpamp-upgrade-quiesce-abort-');
      const targetHome = createBundleDir(baseDir, 'target', 'v2.0.0-beta.1');
      const bundleDir = createBundleDir(baseDir, 'bundle', 'v2.0.0-beta.2');
      const systemdDir = path.join(baseDir, 'systemd');
      const mockSystemctl = path.join(baseDir, 'mock-systemctl');
      writeFileSync(
        mockSystemctl,
        [
          '#!/usr/bin/env bash',
          'case "${1:-}" in',
          '  is-active) exit 3 ;;',
          '  show) printf "loaded\\ndeactivating\\nstop-sigterm\\n"; exit 0 ;;',
          '  stop) exit 0 ;;',
          '  *) exit 0 ;;',
          'esac',
          '',
        ].join('\n'),
        { mode: 0o755 }
      );
      chmodSync(mockSystemctl, 0o755);

      const rawEnv = await buildPortEnv(targetHome, {
        CPAMP_TEST_ALLOW_NON_ROOT_UPGRADE: '1',
        CPA_MANAGER_PLUS_SYSTEMCTL_BIN: mockSystemctl,
      });
      const { _ports: _ports, ...env } = rawEnv;
      const res = spawnSync(
        'bash',
        [path.join(bundleDir, 'cpa-manager-plusctl'), 'upgrade', '--home', targetHome, '--unit-dir', systemdDir],
        { env, encoding: 'utf8' }
      );
      assert.notEqual(res.status, 0);
      assert.match(res.stderr, /still active or transitioning after stop/);
      assert.equal(
        JSON.parse(readFileSync(path.join(targetHome, 'share/cpamp/provenance.json'), 'utf8')).version,
        'v2.0.0-beta.1'
      );
    }

    // 10. Preserves customized systemd unit overrides across upgrade without caller env vars
    {
      const baseDir = makeTempDir('cpamp-upgrade-preserve-unit-');
      const targetHome = createBundleDir(baseDir, 'target', 'v2.0.0-beta.1');
      const bundleDir = createBundleDir(baseDir, 'bundle', 'v2.0.0-beta.2');
      const systemdDir = path.join(baseDir, 'systemd');
      mkdirSync(systemdDir, { recursive: true, mode: 0o755 });
      const extDataDir = path.join(baseDir, 'ext-data');
      const extDbDir = path.join(baseDir, 'ext-db');
      const extKeysDir = path.join(baseDir, 'ext-keys');
      const extRunDir = path.join(baseDir, 'ext-run');
      const extLogDir = path.join(baseDir, 'ext-logs');
      for (const dir of [extDataDir, extDbDir, extKeysDir, extRunDir, extLogDir]) {
        mkdirSync(dir, { recursive: true, mode: 0o700 });
      }
      const customDbPath = path.join(extDbDir, 'custom-usage.sqlite');
      const customKeyPath = path.join(extKeysDir, 'custom.key');
      const customTokenFile = path.join(extRunDir, 'custom-runtime-token');
      writeFileSync(customTokenFile, 'deadbeef', { mode: 0o600 });

      const unitPath = path.join(systemdDir, 'cpa-manager-plus.service');
      writeFileSync(
        unitPath,
        [
          '[Unit]',
          'Description=Custom CPAMP Unit',
          '',
          '[Service]',
          `WorkingDirectory=${targetHome}`,
          `Environment="CPA_MANAGER_PLUS_HOME=${targetHome}"`,
          'Environment="HTTP_ADDR=127.0.0.1:18317"',
          `Environment="USAGE_DATA_DIR=${extDataDir}"`,
          `Environment="USAGE_DB_PATH=${customDbPath}"`,
          `Environment="CPA_MANAGER_DATA_KEY_PATH=${customKeyPath}"`,
          `Environment="CPA_MANAGER_PLUS_RUN_DIR=${extRunDir}"`,
          `Environment="CPA_MANAGER_PLUS_LOG_DIR=${extLogDir}"`,
          `Environment="CPAMP_RUNTIME_TOKEN_FILE=${customTokenFile}"`,
          `Environment="CPAMP_CPA_EXECUTABLE=${targetHome}/bin/cli-proxy-api"`,
          'Environment="CPAMP_RUNTIME_ADDR=127.0.0.1:18391"',
          'Environment="CPAMP_MANAGER_ADDR=127.0.0.1:18392"',
          'Environment="CPAMP_INGRESS_CONTROL_ADDR=127.0.0.1:18393"',
          'Environment="CPAMP_RUNTIME_CPA_ADDR=127.0.0.1:18394"',
          'Environment="CPAMP_DRAIN_TIMEOUT=120s"',
          'Environment="CPAMP_RUNTIME_CPA_UID=10002"',
          'Environment="CPAMP_RUNTIME_CPA_GID=10002"',
          `ExecStart=${targetHome}/cpa-manager-plus-launcher`,
          'TimeoutStopSec=180',
          `ReadWritePaths=${extDataDir} ${extRunDir} ${extLogDir}`,
          '',
          '[Install]',
          'WantedBy=multi-user.target',
          '',
        ].join('\n'),
        { mode: 0o644 }
      );

      const cleanUpgradeEnv = {
        PATH: process.env.PATH || '',
        HOME: process.env.HOME || '',
        CPAMP_TEST_ALLOW_NON_ROOT_UPGRADE: '1',
      };
      const res = spawnSync(
        'bash',
        [
          path.join(bundleDir, 'cpa-manager-plusctl'),
          'upgrade',
          '--home',
          targetHome,
          '--unit-dir',
          systemdDir,
          '--no-systemctl',
          '--no-restart',
        ],
        { env: cleanUpgradeEnv, encoding: 'utf8' }
      );
      assert.equal(res.status, 0, res.stderr);

      const updatedUnit = readFileSync(unitPath, 'utf8');
      assert.match(updatedUnit, /^Environment="HTTP_ADDR=127\.0\.0\.1:18317"$/m);
      assert.match(updatedUnit, new RegExp(`^Environment="USAGE_DATA_DIR=${extDataDir}"$`, 'm'));
      assert.match(updatedUnit, new RegExp(`^Environment="USAGE_DB_PATH=${customDbPath}"$`, 'm'));
      assert.match(updatedUnit, new RegExp(`^Environment="CPA_MANAGER_DATA_KEY_PATH=${customKeyPath}"$`, 'm'));
      assert.match(updatedUnit, new RegExp(`^Environment="CPA_MANAGER_PLUS_RUN_DIR=${extRunDir}"$`, 'm'));
      assert.match(updatedUnit, new RegExp(`^Environment="CPA_MANAGER_PLUS_LOG_DIR=${extLogDir}"$`, 'm'));
      assert.match(updatedUnit, new RegExp(`^Environment="CPAMP_RUNTIME_TOKEN_FILE=${customTokenFile}"$`, 'm'));
      assert.match(updatedUnit, /^Environment="CPAMP_RUNTIME_ADDR=127\.0\.0\.1:18391"$/m);
      assert.match(updatedUnit, /^Environment="CPAMP_MANAGER_ADDR=127\.0\.0\.1:18392"$/m);
      assert.match(updatedUnit, /^Environment="CPAMP_INGRESS_CONTROL_ADDR=127\.0\.0\.1:18393"$/m);
      assert.match(updatedUnit, /^Environment="CPAMP_RUNTIME_CPA_ADDR=127\.0\.0\.1:18394"$/m);
      assert.match(updatedUnit, /^Environment="CPAMP_DRAIN_TIMEOUT=120s"$/m);
      assert.match(updatedUnit, /^Environment="CPAMP_RUNTIME_CPA_UID=10002"$/m);
      assert.match(updatedUnit, /^Environment="CPAMP_RUNTIME_CPA_GID=10002"$/m);
      assert.match(updatedUnit, /^Environment="CPAMP_SERVICE_STOP_TIMEOUT_SEC=180"$/m);
      assert.match(updatedUnit, /^TimeoutStopSec=180$/m);
      assert.match(updatedUnit, new RegExp(`^ExecStart=${targetHome}/cpa-manager-plus-launcher$`, 'm'));
      assert.match(updatedUnit, new RegExp(`^ReadWritePaths=${extDataDir} ${extRunDir} ${extLogDir}`, 'm'));

      // Invalid CPAMP_DRAIN_TIMEOUT is rejected at install time
      const invalidInstall = spawnSync(
        'bash',
        [path.join(targetHome, 'cpa-manager-plusctl'), 'install', '--unit-dir', systemdDir, '--no-systemctl'],
        { env: { ...cleanUpgradeEnv, CPAMP_DRAIN_TIMEOUT: '0s' }, encoding: 'utf8' }
      );
      assert.notEqual(invalidInstall.status, 0);
      assert.match(invalidInstall.stderr, /Invalid CPAMP_DRAIN_TIMEOUT/);
    }

    // 11. Rolls back BOTH target_home and systemd unit on daemon-reload failure, and preserves backup_dir on rollback mv failure
    {
      // 11a. daemon-reload failure after unit write restores previous unit and target_home
      const baseDir = makeTempDir('cpamp-upgrade-unit-rollback-');
      const targetHome = createBundleDir(baseDir, 'target', 'v2.0.0-beta.1');
      const bundleDir = createBundleDir(baseDir, 'bundle', 'v2.0.0-beta.2');
      const systemdDir = path.join(baseDir, 'systemd');
      mkdirSync(systemdDir, { recursive: true, mode: 0o755 });
      const unitPath = path.join(systemdDir, 'cpa-manager-plus.service');
      const originalUnit = [
        '[Unit]',
        'Description=Original',
        '',
        '[Service]',
        `WorkingDirectory=${targetHome}`,
        'Environment="HTTP_ADDR=127.0.0.1:18317"',
        `ExecStart=${targetHome}/bin/cpa-manager-plus-launcher`,
        '',
      ].join('\n');
      writeFileSync(unitPath, originalUnit, { mode: 0o644 });

      const mockSystemctl = path.join(baseDir, 'mock-systemctl');
      writeFileSync(
        mockSystemctl,
        [
          '#!/usr/bin/env bash',
          'case "${1:-}" in',
          '  is-active) exit 3 ;;',
          '  show) printf "loaded\\ninactive\\ndead\\n"; exit 0 ;;',
          '  daemon-reload) echo "simulated daemon-reload failure" >&2; exit 1 ;;',
          '  *) exit 0 ;;',
          'esac',
          '',
        ].join('\n'),
        { mode: 0o755 }
      );
      chmodSync(mockSystemctl, 0o755);

      const rawEnv = await buildPortEnv(targetHome, {
        CPAMP_TEST_ALLOW_NON_ROOT_UPGRADE: '1',
        CPA_MANAGER_PLUS_SYSTEMCTL_BIN: mockSystemctl,
      });
      const { _ports: _ports, ...env } = rawEnv;
      const res = spawnSync(
        'bash',
        [path.join(bundleDir, 'cpa-manager-plusctl'), 'upgrade', '--home', targetHome, '--unit-dir', systemdDir],
        { env, encoding: 'utf8' }
      );
      assert.notEqual(res.status, 0);
      assert.equal(readFileSync(unitPath, 'utf8'), originalUnit);
      assert.equal(
        JSON.parse(readFileSync(path.join(targetHome, 'share/cpamp/provenance.json'), 'utf8')).version,
        'v2.0.0-beta.1'
      );
    }

    {
      // 11b. Simulated rollback mv failure preserves backup_dir on disk and exits non-zero
      const baseDir = makeTempDir('cpamp-upgrade-rollback-preserve-');
      const targetHome = createBundleDir(baseDir, 'target', 'v2.0.0-beta.1');
      const bundleDir = createBundleDir(baseDir, 'bundle', 'v2.0.0-beta.2');
      const stubBinDir = path.join(baseDir, 'stub-bin');
      mkdirSync(stubBinDir, { recursive: true });

      const realMv = execFileSync('which', ['mv'], { encoding: 'utf8' }).trim();
      const mvStub = path.join(stubBinDir, 'mv');
      writeFileSync(
        mvStub,
        [
          '#!/usr/bin/env bash',
          'case "${1:-}" in',
          '  */.upgrade-stage.*/share)',
          '    echo "simulated forward mv failure on share" >&2',
          '    exit 1',
          '    ;;',
          '  */.upgrade-backup.*/bin)',
          '    echo "simulated rollback mv failure on bin" >&2',
          '    exit 1',
          '    ;;',
          'esac',
          `exec "${realMv}" "$@"`,
          '',
        ].join('\n'),
        { mode: 0o755 }
      );
      chmodSync(mvStub, 0o755);

      const rawEnv = await buildPortEnv(targetHome, {
        CPAMP_TEST_ALLOW_NON_ROOT_UPGRADE: '1',
        PATH: `${stubBinDir}:${process.env.PATH || ''}`,
      });
      const { _ports: _ports, ...env } = rawEnv;
      const res = spawnSync(
        'bash',
        [path.join(bundleDir, 'cpa-manager-plusctl'), 'upgrade', '--home', targetHome, '--no-systemctl'],
        { env, encoding: 'utf8' }
      );
      assert.notEqual(res.status, 0);
      assert.match(res.stderr, /CRITICAL: Rollback encountered errors; backup preserved at /);
      const backupDirs = readdirSync(targetHome).filter((name) => name.startsWith('.upgrade-backup.'));
      assert.equal(backupDirs.length, 1);
      assert.equal(existsSync(path.join(targetHome, backupDirs[0], 'bin')), true);
      assert.equal(existsSync(path.join(targetHome, '.upgrade.lock/swapping')), true);
      const lockedStartRes = spawnSync(
        'bash',
        [path.join(targetHome, 'cpa-manager-plusctl'), 'start'],
        { env, encoding: 'utf8' }
      );
      assert.notEqual(lockedStartRes.status, 0);
      assert.match(lockedStartRes.stderr, /in-place upgrade is currently in progress/);
    }
  });

  it('clamps CPAMP_DRAIN_TIMEOUT=3m to 90s when CPAMP_SERVICE_STOP_TIMEOUT_SEC=150 in launcher', async () => {
    const baseDir = makeTempDir('cpamp-native-service-budget-clamp-');
    const { homeDir, runDir } = createMockNativeHome(baseDir);
    const envDumpDir = path.join(baseDir, 'env-dumps');
    mkdirSync(envDumpDir, { recursive: true });

    const rawEnv = await buildPortEnv(homeDir, {
      MOCK_ENV_DUMP_DIR: envDumpDir,
      CPAMP_DRAIN_TIMEOUT: '3m',
      CPAMP_SERVICE_STOP_TIMEOUT_SEC: '150',
    });
    const { _ports: _ports, ...env } = rawEnv;
    const proc = spawn('bash', [path.join(homeDir, 'bin/cpa-manager-plus-launcher')], {
      env,
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    activeProcesses.push(proc);
    let stderr = '';
    proc.stderr.on('data', (chunk) => {
      stderr += chunk.toString('utf8');
    });
    try {
      assert.equal(await waitForFile(path.join(runDir, 'launcher.ready')), true);
      const ingressDump = JSON.parse(readFileSync(path.join(envDumpDir, 'ingress-env.json'), 'utf8'));
      assert.equal(ingressDump.env.CPAMP_INGRESS_SHUTDOWN_TIMEOUT, '90s');
      assert.match(stderr, /CPAMP_DRAIN_TIMEOUT \(3m\) exceeds outer service stop budget; clamping to 90s/);
    } finally {
      proc.kill('SIGTERM');
      await waitForExit(proc);
    }
  });

  it('reaps staged hot-updated CPA children on stop and detects them during upgrade quiescence without touching lookalikes', async () => {
    const baseDir = makeTempDir('cpamp-staged-cpa-');
    const { homeDir, runDir, dataDir, cpaBin } = createMockNativeHome(baseDir);
    const stagedDir = path.join(dataDir, 'runtime/supervisor/artifacts/cpa/8.0.56');
    mkdirSync(stagedDir, { recursive: true, mode: 0o755 });
    const stagedCpaBin = path.join(stagedDir, 'cli-proxy-api');
    copyFileSync(cpaBin, stagedCpaBin);
    chmodSync(stagedCpaBin, 0o755);

    const traversalBadDir = path.join(dataDir, 'runtime/supervisor/artifacts/cpa/a..b');
    mkdirSync(traversalBadDir, { recursive: true, mode: 0o755 });
    const traversalCpaBin = path.join(traversalBadDir, 'cli-proxy-api');
    copyFileSync(cpaBin, traversalCpaBin);
    chmodSync(traversalCpaBin, 0o755);

    const nestedBadDir = path.join(stagedDir, 'sub');
    mkdirSync(nestedBadDir, { recursive: true, mode: 0o755 });
    const nestedCpaBin = path.join(nestedBadDir, 'cli-proxy-api');
    copyFileSync(cpaBin, nestedCpaBin);
    chmodSync(nestedCpaBin, 0o755);

    const cpaPidFile = path.join(baseDir, 'staged-cpa.pid');
    const rawEnv = await buildPortEnv(homeDir, {
      MOCK_SUPERVISOR_SPAWN_CPA: '1',
      MOCK_CPA_SPAWN_EXECUTABLE: stagedCpaBin,
      MOCK_CPA_PID_FILE: cpaPidFile,
    });
    const { _ports: ports, ...env } = rawEnv;

    execFileSync('bash', [path.join(homeDir, 'cpa-manager-plusctl'), 'start'], { env, encoding: 'utf8' });
    try {
      assert.equal(await waitForFile(cpaPidFile), true);
      const stagedPid = Number(readFileSync(cpaPidFile, 'utf8').trim());
      assert.ok(stagedPid > 0);
      assert.doesNotThrow(() => process.kill(stagedPid, 0));

      // Launcher monitor loop pins the staged hot-updated CPA child in provenance
      const provFile = path.join(runDir, 'cpa-instance.provenance');
      let pinned = false;
      for (let i = 0; i < 40; i++) {
        if (existsSync(provFile) && new RegExp(`^${stagedPid}\\|`, 'm').test(readFileSync(provFile, 'utf8'))) {
          pinned = true;
          break;
        }
        await sleep(100);
      }
      assert.equal(pinned, true, 'expected staged hot-updated CPA child to be pinned in cpa-instance.provenance');

      // Even if launcher and supervisor die abruptly, ctl stop must reap the orphaned staged CPA child
      const launcherPid = Number(readFileSync(path.join(runDir, 'cpa-manager-plus.pid'), 'utf8').match(/^pid=(\d+)/m)[1]);
      const supervisorPid = Number(readFileSync(path.join(runDir, 'runtime-supervisor.pid'), 'utf8').match(/^pid=(\d+)/m)[1]);
      process.kill(launcherPid, 'SIGKILL');
      process.kill(supervisorPid, 'SIGKILL');
      await sleep(150);

      const stopOut = spawnSync('bash', [path.join(homeDir, 'cpa-manager-plusctl'), 'stop'], { env, encoding: 'utf8' });
      assert.equal(stopOut.status, 0, stopOut.stderr);
      let stagedExited = false;
      for (let i = 0; i < 30; i++) {
        try {
          process.kill(stagedPid, 0);
          await sleep(100);
        } catch {
          stagedExited = true;
          break;
        }
      }
      assert.equal(stagedExited, true, 'expected orphaned staged CPA child to be reaped on ctl stop');

      // Lookalikes under traversal-like or nested segments must NOT be attributed or signalled
      const [badPort1, badPort2] = await allocateLoopbackPorts(2);
      const badCfg1 = path.join(baseDir, 'bad1-config.yaml');
      const badCfg2 = path.join(baseDir, 'bad2-config.yaml');
      writeFileSync(badCfg1, `host: "127.0.0.1"\nport: ${badPort1}\n`);
      writeFileSync(badCfg2, `host: "127.0.0.1"\nport: ${badPort2}\n`);
      const badChild1 = spawn(traversalCpaBin, ['-config', badCfg1], { stdio: 'ignore' });
      const badChild2 = spawn(nestedCpaBin, ['-config', badCfg2], { stdio: 'ignore' });
      activeProcesses.push(badChild1, badChild2);
      try {
        await sleep(300);
        assert.doesNotThrow(() => process.kill(badChild1.pid, 0));
        assert.doesNotThrow(() => process.kill(badChild2.pid, 0));
        const stopLookalike = spawnSync('bash', [path.join(homeDir, 'cpa-manager-plusctl'), 'stop'], {
          env,
          encoding: 'utf8',
        });
        assert.equal(stopLookalike.status, 0);
        assert.doesNotThrow(() => process.kill(badChild1.pid, 0));
        assert.doesNotThrow(() => process.kill(badChild2.pid, 0));
      } finally {
        if (badChild1.exitCode === null) badChild1.kill('SIGKILL');
        if (badChild2.exitCode === null) badChild2.kill('SIGKILL');
        await Promise.all([
          badChild1.exitCode === null ? waitForExit(badChild1) : Promise.resolve(),
          badChild2.exitCode === null ? waitForExit(badChild2) : Promise.resolve(),
        ]);
      }

      // Upgrade quiescence must detect a running staged hot-updated CPA child and fail closed if it ignores stop
      const unkillableDir = path.join(dataDir, 'runtime/supervisor/artifacts/cpa/8.0.57');
      mkdirSync(unkillableDir, { recursive: true, mode: 0o755 });
      const unkillableCpa = path.join(unkillableDir, 'cli-proxy-api');
      writeFileSync(
        unkillableCpa,
        '#!/usr/bin/env bash\ntrap "" TERM\nsleep 20 &\nwait $!\n',
        { mode: 0o755 }
      );
      const cpaPid2 = path.join(baseDir, 'staged-cpa2.pid');
      const env2 = {
        ...env,
        MOCK_CPA_SPAWN_EXECUTABLE: unkillableCpa,
        MOCK_CPA_PID_FILE: cpaPid2,
      };
      execFileSync('bash', [path.join(homeDir, 'cpa-manager-plusctl'), 'start'], { env: env2, encoding: 'utf8' });
      assert.equal(await waitForFile(cpaPid2), true);
      const pinnedPid2 = Number(readFileSync(cpaPid2, 'utf8').trim());

      // Wait for provenance pin, then spawn an external target-home-like upgrade bundle that checks quiescence
      let pinned2 = false;
      for (let i = 0; i < 40; i++) {
        if (existsSync(provFile) && new RegExp(`^${pinnedPid2}\\|`, 'm').test(readFileSync(provFile, 'utf8'))) {
          pinned2 = true;
          break;
        }
        await sleep(100);
      }
      assert.equal(pinned2, true);

      // Kill launcher & supervisor while leaving pinned staged CPA child alive (ignoring SIGTERM).
      // Replace target's ctl with a no-op stop stub so quiescence check hits the live staged CPA child!
      const launcherPid2 = Number(readFileSync(path.join(runDir, 'cpa-manager-plus.pid'), 'utf8').match(/^pid=(\d+)/m)[1]);
      const supervisorPid2 = Number(readFileSync(path.join(runDir, 'runtime-supervisor.pid'), 'utf8').match(/^pid=(\d+)/m)[1]);
      const mgrPid2 = Number(readFileSync(path.join(runDir, 'manager.pid'), 'utf8').match(/^pid=(\d+)/m)[1]);
      const ingPid2 = Number(readFileSync(path.join(runDir, 'ingress.pid'), 'utf8').match(/^pid=(\d+)/m)[1]);
      for (const p of [launcherPid2, supervisorPid2, mgrPid2, ingPid2]) {
        try { process.kill(p, 'SIGKILL'); } catch {}
      }
      for (const pf of ['cpa-manager-plus.pid', 'runtime-supervisor.pid', 'manager.pid', 'ingress.pid']) {
        rmSync(path.join(runDir, pf), { force: true });
      }
      // Create a bundle and run upgrade where target's ctl stop leaves the staged CPA child running
      const bundleDir = path.join(baseDir, 'bundle');
      mkdirSync(path.join(bundleDir, 'bin'), { recursive: true, mode: 0o755 });
      mkdirSync(path.join(bundleDir, 'share/cpamp/runtime'), { recursive: true, mode: 0o755 });
      for (const binName of ['cpa-manager-plus', 'cpa-manager-plus-ingress', 'cpa-manager-plus-runtime-supervisor', 'cli-proxy-api']) {
        copyFileSync(path.join(homeDir, 'bin', binName), path.join(bundleDir, 'bin', binName));
        chmodSync(path.join(bundleDir, 'bin', binName), 0o755);
      }
      copyFileSync(launcherScript, path.join(bundleDir, 'bin/cpa-manager-plus-launcher'));
      copyFileSync(launcherScript, path.join(bundleDir, 'cpa-manager-plus-launcher'));
      copyFileSync(ctlScript, path.join(bundleDir, 'cpa-manager-plusctl'));
      chmodSync(path.join(bundleDir, 'bin/cpa-manager-plus-launcher'), 0o755);
      chmodSync(path.join(bundleDir, 'cpa-manager-plus-launcher'), 0o755);
      chmodSync(path.join(bundleDir, 'cpa-manager-plusctl'), 0o755);
      copyFileSync(systemdUnitTemplate, path.join(bundleDir, 'cpa-manager-plus.service'));
      writeFileSync(path.join(bundleDir, 'share/cpamp/provenance.json'), JSON.stringify({ version: 'v2.0.0-beta.2' }));
      writeFileSync(path.join(bundleDir, 'share/cpamp/runtime/seed-lineage.json'), JSON.stringify({ seed: 'v2.0.0-beta.2' }));
      writeFileSync(path.join(homeDir, 'share/cpamp/provenance.json'), JSON.stringify({ version: 'v2.0.0-beta.1' }));
      writeFileSync(path.join(homeDir, 'share/cpamp/runtime/seed-lineage.json'), JSON.stringify({ seed: 'v2.0.0-beta.1' }));
      writeFileSync(path.join(homeDir, 'cpa-manager-plusctl'), '#!/usr/bin/env bash\nexit 0\n', { mode: 0o755 });

      try {
        const qFailRes = spawnSync(
          'bash',
          [path.join(bundleDir, 'cpa-manager-plusctl'), 'upgrade', '--home', homeDir, '--no-systemctl', '--no-restart'],
          { env: { ...env2, CPAMP_TEST_ALLOW_NON_ROOT_UPGRADE: '1' }, encoding: 'utf8' }
        );
        assert.notEqual(qFailRes.status, 0);
        assert.match(qFailRes.stderr, /attributable CPA child process\(es\) still running/);
        assert.equal(JSON.parse(readFileSync(path.join(homeDir, 'share/cpamp/provenance.json'), 'utf8')).version, 'v2.0.0-beta.1');
      } finally {
        try { process.kill(pinnedPid2, 'SIGKILL'); } catch {}
      }
    } finally {
      void ports;
      spawnSync('bash', [path.join(homeDir, 'cpa-manager-plusctl'), 'stop'], { env, encoding: 'utf8' });
    }
  });

  it('rejects 1.x native layouts, foreign units, unsafe directives, failed systemd queries, and concurrent lock races; preserves EnvironmentFile/ReadWritePaths/drop-ins and rolls back on post-start failure', async () => {
    const createBundleDir = (baseDir, name, versionLabel) => {
      const dir = path.join(baseDir, name);
      const mock = createMockNativeHome(path.join(baseDir, `${name}-mock`));
      mkdirSync(path.join(dir, 'bin'), { recursive: true, mode: 0o755 });
      mkdirSync(path.join(dir, 'share/cpamp/runtime'), { recursive: true, mode: 0o755 });
      mkdirSync(path.join(dir, 'data'), { recursive: true, mode: 0o700 });
      mkdirSync(path.join(dir, 'run'), { recursive: true, mode: 0o700 });
      mkdirSync(path.join(dir, 'logs'), { recursive: true, mode: 0o700 });
      for (const binName of ['cpa-manager-plus', 'cpa-manager-plus-ingress', 'cpa-manager-plus-runtime-supervisor', 'cli-proxy-api']) {
        copyFileSync(path.join(mock.binDir, binName), path.join(dir, 'bin', binName));
        chmodSync(path.join(dir, 'bin', binName), 0o755);
      }
      copyFileSync(launcherScript, path.join(dir, 'bin/cpa-manager-plus-launcher'));
      copyFileSync(launcherScript, path.join(dir, 'cpa-manager-plus-launcher'));
      copyFileSync(ctlScript, path.join(dir, 'cpa-manager-plusctl'));
      chmodSync(path.join(dir, 'bin/cpa-manager-plus-launcher'), 0o755);
      chmodSync(path.join(dir, 'cpa-manager-plus-launcher'), 0o755);
      chmodSync(path.join(dir, 'cpa-manager-plusctl'), 0o755);
      copyFileSync(systemdUnitTemplate, path.join(dir, 'cpa-manager-plus.service'));
      writeFileSync(path.join(dir, 'share/cpamp/provenance.json'), JSON.stringify({ version: versionLabel }));
      writeFileSync(path.join(dir, 'share/cpamp/runtime/seed-lineage.json'), JSON.stringify({ seed: versionLabel }));
      return dir;
    };

    // A. Rejects 1.x Native layouts before stopping or touching files
    {
      const baseDir = makeTempDir('cpamp-reject-v1-');
      const bundleDir = createBundleDir(baseDir, 'bundle', 'v2.0.0-beta.2');
      const v1Home = path.join(baseDir, 'v1-home');
      mkdirSync(v1Home, { recursive: true, mode: 0o755 });
      writeFileSync(path.join(v1Home, 'cpa-manager-plusctl'), '#!/usr/bin/env bash\nexit 0\n', { mode: 0o755 });
      writeFileSync(path.join(v1Home, 'cpa-manager-plus'), '#!/usr/bin/env bash\nexit 0\n', { mode: 0o755 });
      writeFileSync(path.join(v1Home, 'run.sh'), '#!/usr/bin/env bash\nexit 0\n', { mode: 0o755 });
      const systemdDir = path.join(baseDir, 'systemd');
      mkdirSync(systemdDir, { recursive: true });
      const systemctlLog = path.join(baseDir, 'systemctl.log');
      const mockSystemctl = path.join(baseDir, 'mock-systemctl');
      writeFileSync(
        mockSystemctl,
        `#!/usr/bin/env bash\nprintf '%s\\n' "$*" >> "${systemctlLog}"\nexit 0\n`,
        { mode: 0o755 }
      );
      writeFileSync(
        path.join(systemdDir, 'cpa-manager-plus.service'),
        `[Unit]\nDescription=v1\n[Service]\nUser=cpamp\nWorkingDirectory=${v1Home}\nExecStart=${v1Home}/run.sh\n`
      );

      const v1Res = spawnSync(
        'bash',
        [path.join(bundleDir, 'cpa-manager-plusctl'), 'upgrade', '--home', v1Home, '--unit-dir', systemdDir],
        {
          env: {
            ...process.env,
            CPAMP_TEST_ALLOW_NON_ROOT_UPGRADE: '1',
            CPA_MANAGER_PLUS_SYSTEMCTL_BIN: mockSystemctl,
          },
          encoding: 'utf8',
        }
      );
      assert.notEqual(v1Res.status, 0);
      assert.match(v1Res.stderr, /appears to be a 1\.x Native installation; run the 1\.x -> v2 migration workflow instead/);
      assert.equal(existsSync(systemctlLog), false);

      // Also reject 1.x directory that has cpa-manager-plusctl + cpa-manager-plus without run.sh
      rmSync(path.join(v1Home, 'run.sh'), { force: true });
      const v1NoRunRes = spawnSync(
        'bash',
        [path.join(bundleDir, 'cpa-manager-plusctl'), 'upgrade', '--home', v1Home, '--no-systemctl'],
        { env: { ...process.env, CPAMP_TEST_ALLOW_NON_ROOT_UPGRADE: '1' }, encoding: 'utf8' }
      );
      assert.notEqual(v1NoRunRes.status, 0);
      assert.match(v1NoRunRes.stderr, /appears to be a 1\.x Native installation/);

      // Rejects partial v2 layout missing bin/cpa-manager-plus-runtime-supervisor (even if cpa-manager-plusctl exists)
      const partialHome = createBundleDir(baseDir, 'partial-v2', 'v2.0.0-beta.1');
      rmSync(path.join(partialHome, 'bin/cpa-manager-plus-runtime-supervisor'), { force: true });
      const partialRes = spawnSync(
        'bash',
        [path.join(bundleDir, 'cpa-manager-plusctl'), 'upgrade', '--home', partialHome, '--no-systemctl'],
        { env: { ...process.env, CPAMP_TEST_ALLOW_NON_ROOT_UPGRADE: '1' }, encoding: 'utf8' }
      );
      assert.notEqual(partialRes.status, 0);
      assert.match(partialRes.stderr, /existing CPAMP Native installation/);
    }

    // B. Rejects foreign WorkingDirectory, foreign ExecStart, non-root User, and quoted ReadWritePaths BEFORE stopping
    {
      const baseDir = makeTempDir('cpamp-foreign-unit-');
      const targetHome = createBundleDir(baseDir, 'target', 'v2.0.0-beta.1');
      const bundleDir = createBundleDir(baseDir, 'bundle', 'v2.0.0-beta.2');
      const systemdDir = path.join(baseDir, 'systemd');
      mkdirSync(systemdDir, { recursive: true });
      const systemctlLog = path.join(baseDir, 'systemctl.log');
      const mockSystemctl = path.join(baseDir, 'mock-systemctl');
      writeFileSync(
        mockSystemctl,
        `#!/usr/bin/env bash\nprintf '%s\\n' "$*" >> "${systemctlLog}"\nexit 0\n`,
        { mode: 0o755 }
      );
      const unitPath = path.join(systemdDir, 'cpa-manager-plus.service');
      const unitCases = [
        {
          content: `[Service]\nWorkingDirectory=/srv/cpamp-other\nExecStart=${targetHome}/bin/cpa-manager-plus-launcher\n`,
          err: /WorkingDirectory .* does not match target installation/,
        },
        {
          content: `[Service]\nWorkingDirectory=${targetHome}\nExecStart=/srv/cpamp-other/bin/cpa-manager-plus-launcher\n`,
          err: /ExecStart .* does not belong to target installation/,
        },
        {
          content: `[Service]\nUser=cpamp\nWorkingDirectory=${targetHome}\nExecStart=${targetHome}/bin/cpa-manager-plus-launcher\n`,
          err: /User=cpamp; v2 native launcher must run as root/,
        },
        {
          content: `[Service]\nWorkingDirectory=${targetHome}\nExecStart=${targetHome}/bin/cpa-manager-plus-launcher\nReadWritePaths="/tmp/quoted space"\n`,
          err: /ReadWritePaths .* unsupported quoting/,
        },
      ];
      for (const uc of unitCases) {
        writeFileSync(unitPath, uc.content);
        const res = spawnSync(
          'bash',
          [path.join(bundleDir, 'cpa-manager-plusctl'), 'upgrade', '--home', targetHome, '--unit-dir', systemdDir],
          {
            env: {
              ...process.env,
              CPAMP_TEST_ALLOW_NON_ROOT_UPGRADE: '1',
              CPA_MANAGER_PLUS_SYSTEMCTL_BIN: mockSystemctl,
            },
            encoding: 'utf8',
          }
        );
        assert.notEqual(res.status, 0);
        assert.match(res.stderr, uc.err);
        assert.equal(existsSync(systemctlLog), false);
      }
    }

    // C. Preserves custom EnvironmentFile=, custom ReadWritePaths=, drop-ins, and honors EnvironmentFile= > Environment=
    {
      const baseDir = makeTempDir('cpamp-preserve-envfile-');
      const targetHome = createBundleDir(baseDir, 'target', 'v2.0.0-beta.1');
      const bundleDir = createBundleDir(baseDir, 'bundle', 'v2.0.0-beta.2');
      const systemdDir = path.join(baseDir, 'systemd');
      mkdirSync(systemdDir, { recursive: true });
      const extDbDir = path.join(baseDir, 'ext-db');
      const extKeysDir = path.join(baseDir, 'ext-keys');
      const customRwDir = path.join(baseDir, 'custom-rw');
      for (const d of [extDbDir, extKeysDir, customRwDir]) {
        mkdirSync(d, { recursive: true, mode: 0o700 });
      }
      const customDb = path.join(extDbDir, 'prod-usage.sqlite');
      const customKey = path.join(extKeysDir, 'prod-data.key');
      const customEnvFile = path.join(baseDir, 'prod.env');
      writeFileSync(
        customEnvFile,
        [
          '# Production overrides',
          `export USAGE_DB_PATH="${customDb}"`,
          `CPA_MANAGER_DATA_KEY_PATH='${customKey}'`,
          'CPAMP_DRAIN_TIMEOUT=75s # overrides unit Environment=',
          '',
        ].join('\n'),
        { mode: 0o600 }
      );
      const unitPath = path.join(systemdDir, 'cpa-manager-plus.service');
      writeFileSync(
        unitPath,
        [
          '[Unit]',
          'Description=Prod CPAMP',
          '',
          '[Service]',
          `WorkingDirectory=${targetHome}`,
          `ExecStart=${targetHome}/bin/cpa-manager-plus-launcher`,
          'Environment="CPAMP_DRAIN_TIMEOUT=120s"',
          `EnvironmentFile=-${customEnvFile}`,
          `ReadWritePaths=${targetHome}/data ${targetHome}/run ${targetHome}/logs ${customRwDir}`,
          '',
        ].join('\n')
      );
      const dropinDir = `${unitPath}.d`;
      mkdirSync(dropinDir, { recursive: true });
      const dropinPath = path.join(dropinDir, '10-extra.conf');
      writeFileSync(dropinPath, '[Service]\nEnvironment="HTTP_ADDR=127.0.0.1:19400"\n');

      const cleanEnv = {
        PATH: process.env.PATH || '',
        HOME: process.env.HOME || '',
        CPAMP_TEST_ALLOW_NON_ROOT_UPGRADE: '1',
      };
      const res = spawnSync(
        'bash',
        [
          path.join(bundleDir, 'cpa-manager-plusctl'),
          'upgrade',
          '--home',
          targetHome,
          '--unit-dir',
          systemdDir,
          '--no-systemctl',
          '--no-restart',
        ],
        { env: cleanEnv, encoding: 'utf8' }
      );
      assert.equal(res.status, 0, res.stderr);
      const updatedUnit = readFileSync(unitPath, 'utf8');
      assert.match(updatedUnit, new RegExp(`^EnvironmentFile=-${customEnvFile}$`, 'm'));
      assert.match(updatedUnit, new RegExp(`^Environment="USAGE_DB_PATH=${customDb}"$`, 'm'));
      assert.match(updatedUnit, new RegExp(`^Environment="CPA_MANAGER_DATA_KEY_PATH=${customKey}"$`, 'm'));
      assert.match(updatedUnit, /^Environment="CPAMP_DRAIN_TIMEOUT=75s"$/m);
      assert.match(updatedUnit, /^TimeoutStopSec=150$/m);
      assert.match(updatedUnit, /^Environment="HTTP_ADDR=127\.0\.0\.1:19400"$/m);
      assert.match(updatedUnit, new RegExp(`^ReadWritePaths=.*${customRwDir}`, 'm'));
      assert.match(updatedUnit, new RegExp(`^ReadWritePaths=.*${extDbDir}`, 'm'));
      assert.match(updatedUnit, new RegExp(`^ReadWritePaths=.*${extKeysDir}`, 'm'));
      assert.equal(readFileSync(dropinPath, 'utf8'), '[Service]\nEnvironment="HTTP_ADDR=127.0.0.1:19400"\n');
    }

    // D. Fail-closed systemctl show error & unknown ActiveState, plus cross-command upgrade lock
    {
      const baseDir = makeTempDir('cpamp-systemd-lock-');
      const targetHome = createBundleDir(baseDir, 'target', 'v2.0.0-beta.1');
      const bundleDir = createBundleDir(baseDir, 'bundle', 'v2.0.0-beta.2');
      const systemdDir = path.join(baseDir, 'systemd');
      mkdirSync(systemdDir, { recursive: true });

      const failShowCtl = path.join(baseDir, 'mock-systemctl-fail');
      writeFileSync(
        failShowCtl,
        '#!/usr/bin/env bash\nif [ "${1:-}" = "show" ]; then echo "Failed to connect to bus" >&2; exit 1; fi\nexit 0\n',
        { mode: 0o755 }
      );
      const failShowRes = spawnSync(
        'bash',
        [path.join(bundleDir, 'cpa-manager-plusctl'), 'upgrade', '--home', targetHome, '--unit-dir', systemdDir],
        {
          env: {
            ...process.env,
            CPAMP_TEST_ALLOW_NON_ROOT_UPGRADE: '1',
            CPA_MANAGER_PLUS_SYSTEMCTL_BIN: failShowCtl,
          },
          encoding: 'utf8',
        }
      );
      assert.notEqual(failShowRes.status, 0);
      assert.match(failShowRes.stderr, /Failed to query systemd state/);

      const maintCtl = path.join(baseDir, 'mock-systemctl-maint');
      writeFileSync(
        maintCtl,
        [
          '#!/usr/bin/env bash',
          'case "${1:-}" in',
          '  show) printf "loaded\\nmaintenance\\nrunning\\n"; exit 0 ;;',
          '  is-active) exit 3 ;;',
          '  *) exit 0 ;;',
          'esac',
          '',
        ].join('\n'),
        { mode: 0o755 }
      );
      const maintRes = spawnSync(
        'bash',
        [path.join(bundleDir, 'cpa-manager-plusctl'), 'upgrade', '--home', targetHome, '--unit-dir', systemdDir],
        {
          env: {
            ...process.env,
            CPAMP_TEST_ALLOW_NON_ROOT_UPGRADE: '1',
            CPA_MANAGER_PLUS_SYSTEMCTL_BIN: maintCtl,
          },
          encoding: 'utf8',
        }
      );
      assert.notEqual(maintRes.status, 0);
      assert.match(maintRes.stderr, /still active or transitioning after stop/);

      // ActiveState=activating, SubState=auto-restart MUST be treated as BUSY and fail quiescence
      const autoRestartCtl = path.join(baseDir, 'mock-systemctl-autorestart');
      writeFileSync(
        autoRestartCtl,
        [
          '#!/usr/bin/env bash',
          'case "${1:-}" in',
          '  show) printf "loaded\\nactivating\\nauto-restart\\n"; exit 0 ;;',
          '  is-active) exit 3 ;;',
          '  *) exit 0 ;;',
          'esac',
          '',
        ].join('\n'),
        { mode: 0o755 }
      );
      const autoRestartRes = spawnSync(
        'bash',
        [path.join(bundleDir, 'cpa-manager-plusctl'), 'upgrade', '--home', targetHome, '--unit-dir', systemdDir],
        {
          env: {
            ...process.env,
            CPAMP_TEST_ALLOW_NON_ROOT_UPGRADE: '1',
            CPA_MANAGER_PLUS_SYSTEMCTL_BIN: autoRestartCtl,
          },
          encoding: 'utf8',
        }
      );
      assert.notEqual(autoRestartRes.status, 0);
      assert.match(autoRestartRes.stderr, /still active or transitioning after stop/);

      // Cross-command upgrade lock blocks ctl start, ctl restart (without stopping!), ctl install, and launcher
      const lockDir = path.join(targetHome, '.upgrade.lock');
      mkdirSync(lockDir, { mode: 0o700 });
      writeFileSync(path.join(lockDir, 'pid'), `${process.pid}\n`);
      writeFileSync(path.join(lockDir, 'token'), 'secret-token-1\n');
      writeFileSync(path.join(lockDir, 'swapping'), `${process.pid}\n`);

      const rawEnv = await buildPortEnv(targetHome);
      const { _ports: _ports, ...env } = rawEnv;
      for (const cmd of [
        ['start'],
        ['restart'],
        ['install', '--home', targetHome, '--unit-dir', systemdDir, '--no-systemctl'],
      ]) {
        const lockRes = spawnSync('bash', [path.join(targetHome, 'cpa-manager-plusctl'), ...cmd], {
          env,
          encoding: 'utf8',
        });
        assert.notEqual(lockRes.status, 0);
        assert.match(lockRes.stderr, /an in-place upgrade is currently in progress/);
      }

      const launcherRes = spawnSync('bash', [path.join(targetHome, 'bin/cpa-manager-plus-launcher')], {
        env,
        encoding: 'utf8',
      });
      assert.notEqual(launcherRes.status, 0);
      assert.match(launcherRes.stderr, /Refusing to start launcher while upgrade file swap is in progress/);
      rmSync(lockDir, { recursive: true, force: true });
    }

    // E. Post-upgrade start failure rolls back target_home + unit + restarts restored instance; daemon-reload rollback failure preserves backup_dir
    {
      // E1: systemd post-start failure rolls back and restarts previous active systemd unit
      const baseDir = makeTempDir('cpamp-post-start-systemd-rollback-');
      const targetHome = createBundleDir(baseDir, 'target', 'v2.0.0-beta.1');
      const bundleDir = createBundleDir(baseDir, 'bundle', 'v2.0.0-beta.2');
      const systemdDir = path.join(baseDir, 'systemd');
      mkdirSync(systemdDir, { recursive: true });
      const unitPath = path.join(systemdDir, 'cpa-manager-plus.service');
      const origUnit = [
        '[Unit]',
        'Description=Original Active',
        '',
        '[Service]',
        `WorkingDirectory=${targetHome}`,
        `ExecStart=${targetHome}/bin/cpa-manager-plus-launcher`,
        'Environment="HTTP_ADDR=127.0.0.1:18317"',
        '',
      ].join('\n');
      writeFileSync(unitPath, origUnit);

      const stateFile = path.join(baseDir, 'state');
      const startCountFile = path.join(baseDir, 'start-count');
      const unitPidFile = path.join(baseDir, 'unit.pid');
      writeFileSync(stateFile, 'active\n');
      writeFileSync(startCountFile, '0\n');
      const mockSystemctl = path.join(baseDir, 'mock-systemctl');
      writeFileSync(
        mockSystemctl,
        [
          '#!/usr/bin/env bash',
          `state_file="${stateFile}"`,
          `count_file="${startCountFile}"`,
          `pid_file="${unitPidFile}"`,
          `launcher_bin="${targetHome}/bin/cpa-manager-plus-launcher"`,
          `log_out="${targetHome}/logs/cpa-manager-plus.log"`,
          'case "${1:-}" in',
          '  is-active) [ "$(cat "${state_file}")" = "active" ] && exit 0; exit 3 ;;',
          '  show)',
          '    if [ "$(cat "${state_file}")" = "active" ]; then printf "loaded\\nactive\\nrunning\\n"; else printf "loaded\\ninactive\\ndead\\n"; fi',
          '    exit 0 ;;',
          '  stop)',
          '    if [ -f "${pid_file}" ]; then',
          '      up="$(cat "${pid_file}" 2>/dev/null || true)"',
          '      if [ -n "${up}" ] && kill -0 "${up}" >/dev/null 2>&1; then',
          '        kill -TERM "${up}" >/dev/null 2>&1 || true',
          '        for _ in $(seq 1 30); do kill -0 "${up}" >/dev/null 2>&1 || break; sleep 0.1; done',
          '      fi',
          '      rm -f "${pid_file}"',
          '    fi',
          '    echo "inactive" > "${state_file}"; exit 0 ;;',
          '  start)',
          '    n=$(( $(cat "${count_file}") + 1 ))',
          '    echo "${n}" > "${count_file}"',
          '    if [ "${n}" -eq 1 ]; then echo "inactive" > "${state_file}"; exit 1; fi',
          '    mkdir -p "$(dirname "${log_out}")"',
          '    "${launcher_bin}" </dev/null >>"${log_out}" 2>&1 &',
          '    echo "$!" > "${pid_file}"',
          '    echo "active" > "${state_file}"; exit 0 ;;',
          '  *) exit 0 ;;',
          'esac',
          '',
        ].join('\n'),
        { mode: 0o755 }
      );
      const rawEnv = await buildPortEnv(targetHome, {
        CPAMP_TEST_ALLOW_NON_ROOT_UPGRADE: '1',
        CPA_MANAGER_PLUS_SYSTEMCTL_BIN: mockSystemctl,
      });
      const { _ports: _ports, ...env } = rawEnv;
      try {
        const failRes = spawnSync(
          'bash',
          [path.join(bundleDir, 'cpa-manager-plusctl'), 'upgrade', '--home', targetHome, '--unit-dir', systemdDir],
          { env, encoding: 'utf8' }
        );
        assert.notEqual(failRes.status, 0);
        assert.match(failRes.stderr, /post-upgrade start\/readiness check; rolling back/);
        assert.doesNotMatch(failRes.stderr, /failed to restart previously active instance/);
        assert.equal(readFileSync(unitPath, 'utf8'), origUnit);
        assert.equal(JSON.parse(readFileSync(path.join(targetHome, 'share/cpamp/provenance.json'), 'utf8')).version, 'v2.0.0-beta.1');
        assert.equal(readFileSync(stateFile, 'utf8').trim(), 'active');
        assert.equal(existsSync(path.join(targetHome, 'run/launcher.ready')), true);
        assert.equal(readdirSync(targetHome).filter((n) => n.startsWith('.upgrade-backup.')).length, 0);
      } finally {
        spawnSync('bash', [path.join(targetHome, 'cpa-manager-plusctl'), 'stop'], {
          env,
          encoding: 'utf8',
        });
        if (existsSync(unitPidFile)) {
          const up = Number(readFileSync(unitPidFile, 'utf8').trim());
          if (up > 0) {
            try { process.kill(up, 'SIGKILL'); } catch {}
          }
        }
      }
    }

    {
      // E2: ctl-managed post-start failure rolls back and restarts previous healthy v2.0.0-beta.1 instance
      const baseDir = makeTempDir('cpamp-post-start-ctl-rollback-');
      const targetHome = createBundleDir(baseDir, 'target', 'v2.0.0-beta.1');
      const bundleDir = createBundleDir(baseDir, 'bundle', 'v2.0.0-beta.2');
      // Make new bundle fail to start by having ingress exit 1 immediately
      writeFileSync(path.join(bundleDir, 'bin/cpa-manager-plus-ingress'), '#!/usr/bin/env bash\nexit 1\n', { mode: 0o755 });
      const systemdDir = path.join(baseDir, 'systemd');
      const rawEnv = await buildPortEnv(targetHome, {
        CPAMP_TEST_ALLOW_NON_ROOT_UPGRADE: '1',
        CPA_MANAGER_PLUS_SKIP_SYSTEMCTL: '1',
        CPAMP_STARTUP_TIMEOUT_SEC: '4',
      });
      const { _ports: ports, ...env } = rawEnv;
      const upgradeEnv = { ...env };
      delete upgradeEnv.CPA_MANAGER_PLUS_HOME;

      execFileSync('bash', [path.join(targetHome, 'cpa-manager-plusctl'), 'start'], { env, encoding: 'utf8' });
      try {
        const failRes = spawnSync(
          'bash',
          [
            path.join(bundleDir, 'cpa-manager-plusctl'),
            'upgrade',
            '--home',
            targetHome,
            '--unit-dir',
            systemdDir,
            '--no-systemctl',
          ],
          { env: upgradeEnv, encoding: 'utf8' }
        );
        assert.notEqual(failRes.status, 0);
        assert.match(failRes.stderr, /post-upgrade start\/readiness check; rolling back/);
        assert.equal(JSON.parse(readFileSync(path.join(targetHome, 'share/cpamp/provenance.json'), 'utf8')).version, 'v2.0.0-beta.1');
        const healthRes = await fetch(`http://127.0.0.1:${ports.ingressPort}/health`);
        assert.equal(healthRes.status, 200);
      } finally {
        spawnSync('bash', [path.join(targetHome, 'cpa-manager-plusctl'), 'stop'], { env, encoding: 'utf8' });
      }
    }

    {
      // E3: rollback daemon-reload failure preserves backup_dir on disk and reports CRITICAL
      const baseDir = makeTempDir('cpamp-rollback-daemon-reload-fail-');
      const targetHome = createBundleDir(baseDir, 'target', 'v2.0.0-beta.1');
      const bundleDir = createBundleDir(baseDir, 'bundle', 'v2.0.0-beta.2');
      const systemdDir = path.join(baseDir, 'systemd');
      mkdirSync(systemdDir, { recursive: true });
      const unitPath = path.join(systemdDir, 'cpa-manager-plus.service');
      const origUnit = `[Service]\nWorkingDirectory=${targetHome}\nExecStart=${targetHome}/bin/cpa-manager-plus-launcher\n`;
      writeFileSync(unitPath, origUnit);
      const reloadCountFile = path.join(baseDir, 'reload-count');
      writeFileSync(reloadCountFile, '0\n');
      const mockSystemctl = path.join(baseDir, 'mock-systemctl');
      writeFileSync(
        mockSystemctl,
        [
          '#!/usr/bin/env bash',
          `rcf="${reloadCountFile}"`,
          'case "${1:-}" in',
          '  is-active) exit 3 ;;',
          '  show) printf "loaded\\ninactive\\ndead\\n"; exit 0 ;;',
          '  daemon-reload)',
          '    n=$(( $(cat "${rcf}") + 1 )); echo "${n}" > "${rcf}"',
          '    [ "${n}" -eq 1 ] && exit 0',
          '    exit 1 ;;',
          '  start) exit 1 ;;',
          '  *) exit 0 ;;',
          'esac',
          '',
        ].join('\n'),
        { mode: 0o755 }
      );
      const rawEnv = await buildPortEnv(targetHome, {
        CPAMP_TEST_ALLOW_NON_ROOT_UPGRADE: '1',
        CPA_MANAGER_PLUS_SYSTEMCTL_BIN: mockSystemctl,
      });
      const { _ports: _ports, ...env } = rawEnv;
      const failRes = spawnSync(
        'bash',
        [path.join(bundleDir, 'cpa-manager-plusctl'), 'upgrade', '--home', targetHome, '--unit-dir', systemdDir],
        { env, encoding: 'utf8' }
      );
      assert.notEqual(failRes.status, 0);
      assert.match(failRes.stderr, /CRITICAL: Rollback encountered errors; backup preserved at /);
      const backups = readdirSync(targetHome).filter((n) => n.startsWith('.upgrade-backup.'));
      assert.equal(backups.length, 1);
      assert.equal(readFileSync(unitPath, 'utf8'), origUnit);
    }
  });

  it('clamps CPAMP_STOP_TIMEOUT_SEC to active instance drain budget + 45s so Ingress drains cleanly without premature SIGKILL', async () => {
    const baseDir = makeTempDir('cpamp-stop-drain-floor-');
    const { homeDir, runDir } = createMockNativeHome(baseDir);
    const eventLog = path.join(baseDir, 'events.log');
    const rawEnv = await buildPortEnv(homeDir, {
      MOCK_EVENT_LOG: eventLog,
      MOCK_INGRESS_DRAIN_MS: '1500',
      CPAMP_DRAIN_TIMEOUT: '2s',
    });
    const { _ports: _ports, ...env } = rawEnv;

    execFileSync('bash', [path.join(homeDir, 'cpa-manager-plusctl'), 'start'], { env, encoding: 'utf8' });
    try {
      const drainPath = path.join(runDir, 'launcher.drain');
      assert.equal(existsSync(drainPath), true);
      assert.equal(readFileSync(drainPath, 'utf8').trim(), '2');
      assert.equal(statSync(drainPath).mode & 0o777, 0o600);
      assert.match(readFileSync(path.join(runDir, 'launcher.ready'), 'utf8'), /^drain_timeout_sec=2$/m);

      // Run ctl stop with short CPAMP_STOP_TIMEOUT_SEC=1 and WITHOUT CPAMP_DRAIN_TIMEOUT in caller env
      const stopEnv = { ...env, CPAMP_STOP_TIMEOUT_SEC: '1' };
      delete stopEnv.CPAMP_DRAIN_TIMEOUT;
      const stopRes = spawnSync('bash', [path.join(homeDir, 'cpa-manager-plusctl'), 'stop'], {
        env: stopEnv,
        encoding: 'utf8',
      });
      assert.equal(stopRes.status, 0, stopRes.stderr);
      assert.match(
        stopRes.stderr,
        /Clamping CPAMP_STOP_TIMEOUT_SEC \(1s -> 47s\) to honor active 2s Ingress drain budget/
      );
      assert.equal(existsSync(drainPath), false);

      const events = readFileSync(eventLog, 'utf8')
        .trim()
        .split('\n')
        .map((line) => JSON.parse(line));
      const ingExitIdx = events.findIndex((e) => e.role === 'ingress' && e.event === 'exit');
      const supTermIdx = events.findIndex((e) => e.role === 'runtime-supervisor' && e.event === 'sig:SIGTERM');
      assert.notEqual(ingExitIdx, -1, 'expected ingress to exit cleanly');
      assert.ok(ingExitIdx < supTermIdx, 'expected ingress to finish draining before supervisor shutdown');
    } finally {
      spawnSync('bash', [path.join(homeDir, 'cpa-manager-plusctl'), 'stop'], { env, encoding: 'utf8' });
    }
  });

  it('polls Type=simple systemd post-start readiness until launcher.ready + 3 child PIDs and rolls back on delayed startup failure or timeout', async () => {
    const createBundleDir = (baseDir, name, versionLabel) => {
      const dir = path.join(baseDir, name);
      const mock = createMockNativeHome(path.join(baseDir, `${name}-mock`));
      mkdirSync(path.join(dir, 'bin'), { recursive: true, mode: 0o755 });
      mkdirSync(path.join(dir, 'share/cpamp/runtime'), { recursive: true, mode: 0o755 });
      mkdirSync(path.join(dir, 'data'), { recursive: true, mode: 0o700 });
      mkdirSync(path.join(dir, 'run'), { recursive: true, mode: 0o700 });
      mkdirSync(path.join(dir, 'logs'), { recursive: true, mode: 0o700 });
      for (const binName of ['cpa-manager-plus', 'cpa-manager-plus-ingress', 'cpa-manager-plus-runtime-supervisor', 'cli-proxy-api']) {
        copyFileSync(path.join(mock.binDir, binName), path.join(dir, 'bin', binName));
        chmodSync(path.join(dir, 'bin', binName), 0o755);
      }
      copyFileSync(path.join(mock.binDir, 'mock-service.mjs'), path.join(dir, 'bin/mock-service.mjs'));
      chmodSync(path.join(dir, 'bin/mock-service.mjs'), 0o644);
      copyFileSync(launcherScript, path.join(dir, 'bin/cpa-manager-plus-launcher'));
      copyFileSync(launcherScript, path.join(dir, 'cpa-manager-plus-launcher'));
      copyFileSync(ctlScript, path.join(dir, 'cpa-manager-plusctl'));
      chmodSync(path.join(dir, 'bin/cpa-manager-plus-launcher'), 0o755);
      chmodSync(path.join(dir, 'cpa-manager-plus-launcher'), 0o755);
      chmodSync(path.join(dir, 'cpa-manager-plusctl'), 0o755);
      copyFileSync(systemdUnitTemplate, path.join(dir, 'cpa-manager-plus.service'));
      writeFileSync(path.join(dir, 'share/cpamp/provenance.json'), JSON.stringify({ version: versionLabel }));
      writeFileSync(path.join(dir, 'share/cpamp/runtime/seed-lineage.json'), JSON.stringify({ seed: versionLabel }));
      return dir;
    };

    // 1. Type=simple systemctl start exits 0 & reports active/running, then Manager crashes after 300ms
    {
      const baseDir = makeTempDir('cpamp-systemd-delayed-crash-');
      const targetHome = createBundleDir(baseDir, 'target', 'v2.0.0-beta.1');
      const bundleDir = createBundleDir(baseDir, 'bundle', 'v2.0.0-beta.2');
      const systemdDir = path.join(baseDir, 'systemd');

      // Make upgraded Manager binary exit 1 after 300ms before launcher.ready is written
      writeFileSync(
        path.join(bundleDir, 'bin/cpa-manager-plus'),
        '#!/usr/bin/env bash\nsleep 0.3\nexit 1\n',
        { mode: 0o755 }
      );
      chmodSync(path.join(bundleDir, 'bin/cpa-manager-plus'), 0o755);

      const stateFile = path.join(baseDir, 'systemd-state');
      const unitPidFile = path.join(baseDir, 'unit.pid');
      const rawEnv = await buildPortEnv(targetHome, {
        CPAMP_TEST_ALLOW_NON_ROOT_UPGRADE: '1',
        CPAMP_STARTUP_TIMEOUT_SEC: '15',
      });
      const { _ports: ports, ...env } = rawEnv;

      const mockSystemctl = path.join(baseDir, 'mock-systemctl');
      writeFileSync(
        mockSystemctl,
        [
          '#!/usr/bin/env bash',
          `state_file="${stateFile}"`,
          `pid_file="${unitPidFile}"`,
          `launcher_bin="${targetHome}/bin/cpa-manager-plus-launcher"`,
          `log_out="${targetHome}/logs/cpa-manager-plus.log"`,
          'refresh_state() {',
          '  if [ "$(cat "${state_file}" 2>/dev/null || true)" = "active" ] && [ -f "${pid_file}" ]; then',
          '    up="$(cat "${pid_file}" 2>/dev/null || true)"',
          '    if [ -z "${up}" ] || ! kill -0 "${up}" >/dev/null 2>&1; then',
          '      echo "activating-autorestart" > "${state_file}"',
          '    fi',
          '  fi',
          '}',
          'case "${1:-}" in',
          '  is-active)',
          '    refresh_state',
          '    [ "$(cat "${state_file}" 2>/dev/null || true)" = "active" ] && exit 0',
          '    exit 3',
          '    ;;',
          '  show)',
          '    refresh_state',
          '    case "$(cat "${state_file}" 2>/dev/null || true)" in',
          '      active) printf "loaded\\nactive\\nrunning\\n" ;;',
          '      activating-autorestart) printf "loaded\\nactivating\\nauto-restart\\n" ;;',
          '      *) printf "loaded\\ninactive\\ndead\\n" ;;',
          '    esac',
          '    exit 0',
          '    ;;',
          '  stop)',
          '    if [ -f "${pid_file}" ]; then',
          '      up="$(cat "${pid_file}" 2>/dev/null || true)"',
          '      if [ -n "${up}" ] && kill -0 "${up}" >/dev/null 2>&1; then',
          '        kill -TERM "${up}" >/dev/null 2>&1 || true',
          '        for _ in $(seq 1 30); do kill -0 "${up}" >/dev/null 2>&1 || break; sleep 0.1; done',
          '      fi',
          '      rm -f "${pid_file}"',
          '    fi',
          '    echo "inactive" > "${state_file}"',
          '    exit 0',
          '    ;;',
          '  start)',
          '    mkdir -p "$(dirname "${log_out}")"',
          '    "${launcher_bin}" </dev/null >>"${log_out}" 2>&1 &',
          '    echo "$!" > "${pid_file}"',
          '    echo "active" > "${state_file}"',
          '    exit 0',
          '    ;;',
          '  *) exit 0 ;;',
          'esac',
          '',
        ].join('\n'),
        { mode: 0o755 }
      );
      chmodSync(mockSystemctl, 0o755);

      const fullEnv = { ...env, CPA_MANAGER_PLUS_SYSTEMCTL_BIN: mockSystemctl };
      execFileSync('bash', [path.join(targetHome, 'cpa-manager-plusctl'), 'install', '--home', targetHome, '--unit-dir', systemdDir], {
        env: fullEnv,
        encoding: 'utf8',
      });
      const unitPath = path.join(systemdDir, 'cpa-manager-plus.service');
      const initialUnit = readFileSync(unitPath, 'utf8');
      // Start v1 active via mockSystemctl
      execFileSync('bash', [mockSystemctl, 'start', 'cpa-manager-plus.service'], { env: fullEnv });
      assert.equal(await waitForFile(path.join(targetHome, 'run/launcher.ready')), true);

      const upgradeEnv = { ...fullEnv };
      delete upgradeEnv.CPA_MANAGER_PLUS_HOME;

      try {
        const startedAt = Date.now();
        const res = spawnSync(
          'bash',
          [path.join(bundleDir, 'cpa-manager-plusctl'), 'upgrade', '--home', targetHome, '--unit-dir', systemdDir],
          { env: upgradeEnv, encoding: 'utf8' }
        );
        const elapsedMs = Date.now() - startedAt;
        assert.notEqual(res.status, 0);
        assert.ok(elapsedMs < 13000, `expected immediate abort on auto-restart transition instead of 15s timeout (took ${elapsedMs}ms)`);
        assert.match(res.stderr, /post-upgrade start\/readiness check; rolling back/);
        assert.doesNotMatch(res.stderr, /failed to restart previously active instance/);
        assert.equal(readFileSync(unitPath, 'utf8'), initialUnit);
        assert.equal(
          JSON.parse(readFileSync(path.join(targetHome, 'share/cpamp/provenance.json'), 'utf8')).version,
          'v2.0.0-beta.1'
        );
        assert.equal(readdirSync(targetHome).filter((n) => n.startsWith('.upgrade-backup.')).length, 0);
        assert.equal(existsSync(path.join(targetHome, 'run/launcher.ready')), true);
        const statusOut = execFileSync('bash', [path.join(targetHome, 'cpa-manager-plusctl'), 'status'], {
          env: fullEnv,
          encoding: 'utf8',
        });
        assert.match(statusOut, /cpa-manager-plus is running with PID \d+/);
        const healthRes = await fetch(`http://127.0.0.1:${ports.ingressPort}/health`, {
          headers: { connection: 'close' },
        });
        assert.equal(healthRes.status, 200);
        const healthBody = await healthRes.json();
        assert.equal(healthBody.ok, true);
      } finally {
        spawnSync('bash', [path.join(targetHome, 'cpa-manager-plusctl'), 'stop'], {
          env: fullEnv,
          encoding: 'utf8',
        });
        if (existsSync(unitPidFile)) {
          const up = Number(readFileSync(unitPidFile, 'utf8').trim());
          if (up > 0) {
            try { process.kill(up, 'SIGKILL'); } catch {}
          }
        }
      }
    }

    // 2. Stale launcher.ready is removed before systemctl start & timeout rolls back if readiness never arrives
    {
      const baseDir = makeTempDir('cpamp-systemd-stale-ready-timeout-');
      const targetHome = createBundleDir(baseDir, 'target', 'v2.0.0-beta.1');
      const bundleDir = createBundleDir(baseDir, 'bundle', 'v2.0.0-beta.2');
      const systemdDir = path.join(baseDir, 'systemd');
      const stateFile = path.join(baseDir, 'state');
      const unitPidFile = path.join(baseDir, 'unit.pid');
      writeFileSync(stateFile, 'inactive\n');

      // Upgraded ingress hangs without listening so launcher.ready is never written
      writeFileSync(
        path.join(bundleDir, 'bin/cpa-manager-plus-ingress'),
        '#!/usr/bin/env bash\ntrap "exit 0" TERM INT\nsleep 20 &\nwait $!\n',
        { mode: 0o755 }
      );
      chmodSync(path.join(bundleDir, 'bin/cpa-manager-plus-ingress'), 0o755);

      // Pre-seed a stale launcher.ready that would fool a naive existence check
      writeFileSync(path.join(targetHome, 'run/launcher.ready'), 'stale=1\n');
      const mockSystemctl = path.join(baseDir, 'mock-systemctl');
      writeFileSync(
        mockSystemctl,
        [
          '#!/usr/bin/env bash',
          `state_file="${stateFile}"`,
          `pid_file="${unitPidFile}"`,
          `launcher_bin="${targetHome}/bin/cpa-manager-plus-launcher"`,
          `log_out="${targetHome}/logs/cpa-manager-plus.log"`,
          'case "${1:-}" in',
          '  is-active) [ "$(cat "${state_file}")" = "active" ] && exit 0; exit 3 ;;',
          '  show)',
          '    if [ "$(cat "${state_file}")" = "active" ]; then printf "loaded\\nactive\\nrunning\\n"; else printf "loaded\\ninactive\\ndead\\n"; fi',
          '    exit 0 ;;',
          '  stop)',
          '    if [ -f "${pid_file}" ]; then',
          '      up="$(cat "${pid_file}" 2>/dev/null || true)"',
          '      if [ -n "${up}" ] && kill -0 "${up}" >/dev/null 2>&1; then',
          '        kill -TERM "${up}" >/dev/null 2>&1 || true',
          '        for _ in $(seq 1 30); do kill -0 "${up}" >/dev/null 2>&1 || break; sleep 0.1; done',
          '      fi',
          '      rm -f "${pid_file}"',
          '    fi',
          '    echo "inactive" > "${state_file}"; exit 0 ;;',
          '  start)',
          '    mkdir -p "$(dirname "${log_out}")"',
          '    "${launcher_bin}" </dev/null >>"${log_out}" 2>&1 &',
          '    echo "$!" > "${pid_file}"',
          '    echo "active" > "${state_file}"; exit 0 ;;',
          '  *) exit 0 ;;',
          'esac',
          '',
        ].join('\n'),
        { mode: 0o755 }
      );
      chmodSync(mockSystemctl, 0o755);

      const rawEnv = await buildPortEnv(targetHome, {
        CPAMP_TEST_ALLOW_NON_ROOT_UPGRADE: '1',
        CPA_MANAGER_PLUS_SYSTEMCTL_BIN: mockSystemctl,
        CPAMP_STARTUP_TIMEOUT_SEC: '2',
      });
      const { _ports: _ports, ...env } = rawEnv;
      try {
        const res = spawnSync(
          'bash',
          [path.join(bundleDir, 'cpa-manager-plusctl'), 'upgrade', '--home', targetHome, '--unit-dir', systemdDir],
          { env, encoding: 'utf8' }
        );
        assert.notEqual(res.status, 0);
        assert.match(res.stderr, /post-upgrade start\/readiness check; rolling back/);
        assert.equal(
          JSON.parse(readFileSync(path.join(targetHome, 'share/cpamp/provenance.json'), 'utf8')).version,
          'v2.0.0-beta.1'
        );
      } finally {
        spawnSync('bash', [path.join(targetHome, 'cpa-manager-plusctl'), 'stop'], { env, encoding: 'utf8' });
        if (existsSync(unitPidFile)) {
          const up = Number(readFileSync(unitPidFile, 'utf8').trim());
          if (up > 0) {
            try { process.kill(up, 'SIGKILL'); } catch {}
          }
        }
      }
    }
  });

  it('honors systemd.exec(5) precedence (EnvironmentFile= overrides Environment= and empty directives reset) across install and upgrade', async () => {
    const createBundleDir = (baseDir, name, versionLabel) => {
      const dir = path.join(baseDir, name);
      const mock = createMockNativeHome(path.join(baseDir, `${name}-mock`));
      mkdirSync(path.join(dir, 'bin'), { recursive: true, mode: 0o755 });
      mkdirSync(path.join(dir, 'share/cpamp/runtime'), { recursive: true, mode: 0o755 });
      mkdirSync(path.join(dir, 'data'), { recursive: true, mode: 0o700 });
      mkdirSync(path.join(dir, 'run'), { recursive: true, mode: 0o700 });
      mkdirSync(path.join(dir, 'logs'), { recursive: true, mode: 0o700 });
      for (const binName of ['cpa-manager-plus', 'cpa-manager-plus-ingress', 'cpa-manager-plus-runtime-supervisor', 'cli-proxy-api']) {
        copyFileSync(path.join(mock.binDir, binName), path.join(dir, 'bin', binName));
        chmodSync(path.join(dir, 'bin', binName), 0o755);
      }
      copyFileSync(launcherScript, path.join(dir, 'bin/cpa-manager-plus-launcher'));
      copyFileSync(launcherScript, path.join(dir, 'cpa-manager-plus-launcher'));
      copyFileSync(ctlScript, path.join(dir, 'cpa-manager-plusctl'));
      chmodSync(path.join(dir, 'bin/cpa-manager-plus-launcher'), 0o755);
      chmodSync(path.join(dir, 'cpa-manager-plus-launcher'), 0o755);
      chmodSync(path.join(dir, 'cpa-manager-plusctl'), 0o755);
      copyFileSync(systemdUnitTemplate, path.join(dir, 'cpa-manager-plus.service'));
      writeFileSync(path.join(dir, 'share/cpamp/provenance.json'), JSON.stringify({ version: versionLabel }));
      writeFileSync(path.join(dir, 'share/cpamp/runtime/seed-lineage.json'), JSON.stringify({ seed: versionLabel }));
      return dir;
    };

    const baseDir = makeTempDir('cpamp-envfile-precedence-e2e-');
    const targetHome = createBundleDir(baseDir, 'target', 'v2.0.0-beta.1');
    const bundleDir = createBundleDir(baseDir, 'bundle', 'v2.0.0-beta.2');
    const systemdDir = path.join(baseDir, 'systemd');
    const rawEnv = await buildPortEnv(targetHome);
    const { _ports: _ports, ...installEnv } = rawEnv;

    // 1. Run real install to produce full 18-key default unit + EnvironmentFile=-<targetHome>/cpa-manager-plus.env
    execFileSync(
      'bash',
      [path.join(targetHome, 'cpa-manager-plusctl'), 'install', '--home', targetHome, '--unit-dir', systemdDir, '--no-systemctl'],
      { env: installEnv, encoding: 'utf8' }
    );
    const unitPath = path.join(systemdDir, 'cpa-manager-plus.service');
    const initialUnit = readFileSync(unitPath, 'utf8');
    assert.match(initialUnit, /^Environment="CPAMP_DRAIN_TIMEOUT=90s"$/m);
    assert.match(initialUnit, new RegExp(`^EnvironmentFile=-${targetHome}/cpa-manager-plus\\.env$`, 'm'));

    // 2. Write custom overrides into cpa-manager-plus.env
    const extDataDir = path.join(baseDir, 'ext-data');
    const extRunDir = path.join(baseDir, 'ext-run');
    mkdirSync(path.join(extDataDir, 'runtime/gateway'), { recursive: true, mode: 0o700 });
    mkdirSync(extRunDir, { recursive: true, mode: 0o700 });
    chmodSync(extDataDir, 0o700);
    const [customCpaPort] = await allocateLoopbackPorts(1);
    const customCpaAddr = `127.0.0.1:${customCpaPort}`;

    writeFileSync(
      path.join(targetHome, 'cpa-manager-plus.env'),
      [
        `USAGE_DATA_DIR=${extDataDir}`,
        `CPA_MANAGER_PLUS_RUN_DIR=${extRunDir}`,
        `CPAMP_RUNTIME_CPA_ADDR=${customCpaAddr}`,
        'CPAMP_DRAIN_TIMEOUT=180s',
        '',
      ].join('\n'),
      { mode: 0o600 }
    );

    const cleanEnv = {
      PATH: process.env.PATH || '',
      HOME: process.env.HOME || '',
      CPAMP_TEST_ALLOW_NON_ROOT_UPGRADE: '1',
    };

    // 3a. Prove upgrade checks quiescence on .env-configured CPA_MANAGER_PLUS_RUN_DIR (not base unit's targetHome/run)
    const foreignProc = spawn('bash', ['-c', 'sleep 20'], { stdio: 'ignore' });
    try {
      writeFileSync(path.join(extRunDir, 'cpa-manager-plus.pid'), `${foreignProc.pid}\n`, { mode: 0o600 });
      const runDirConflict = spawnSync(
        'bash',
        [path.join(bundleDir, 'cpa-manager-plusctl'), 'upgrade', '--home', targetHome, '--unit-dir', systemdDir, '--no-systemctl', '--no-restart'],
        { env: cleanEnv, encoding: 'utf8' }
      );
      assert.notEqual(runDirConflict.status, 0);
      assert.match(runDirConflict.stderr, new RegExp(`${extRunDir.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}/cpa-manager-plus\\.pid points to a running process`));
    } finally {
      rmSync(path.join(extRunDir, 'cpa-manager-plus.pid'), { force: true });
      foreignProc.kill('SIGKILL');
      await waitForExit(foreignProc);
    }

    // 3b. Prove upgrade checks quiescence on .env-configured CPAMP_RUNTIME_CPA_ADDR + USAGE_DATA_DIR (even with no cpa.address file)
    const unkillableCpa = path.join(targetHome, 'bin/cli-proxy-api');
    const savedCpaScript = readFileSync(unkillableCpa, 'utf8');
    const savedTargetCtl = readFileSync(path.join(targetHome, 'cpa-manager-plusctl'), 'utf8');
    const savedEnvContent = readFileSync(path.join(targetHome, 'cpa-manager-plus.env'), 'utf8');
    writeFileSync(
      unkillableCpa,
      [
        '#!/usr/bin/env node',
        'const http = require("node:http");',
        'process.on("SIGTERM", () => {});',
        `const srv = http.createServer((req, res) => res.end("cpa"));`,
        `srv.listen(${customCpaPort}, "127.0.0.1");`,
        'setInterval(() => {}, 60000);',
        '',
      ].join('\n'),
      { mode: 0o755 }
    );
    const cfgPath = path.join(extDataDir, 'runtime/gateway/config.yaml');
    writeFileSync(cfgPath, `host: "127.0.0.1"\nport: ${customCpaPort}\n`, { mode: 0o600 });
    const spawnOpts = { stdio: 'ignore' };
    if (typeof process.getuid === 'function' && process.getuid() === 0) {
      spawnOpts.uid = 10001;
      spawnOpts.gid = 10001;
    } else if (typeof process.getuid === 'function' && process.getuid() > 0) {
      writeFileSync(
        path.join(targetHome, 'cpa-manager-plus.env'),
        `${savedEnvContent}CPAMP_RUNTIME_CPA_UID=${process.getuid()}\nCPAMP_RUNTIME_CPA_GID=${process.getgid()}\n`,
        { mode: 0o600 }
      );
    }
    // Simulate target ctl stop leaving the CPA child running so upgrade's own hard quiescence check catches it
    writeFileSync(path.join(targetHome, 'cpa-manager-plusctl'), '#!/usr/bin/env bash\nexit 0\n', { mode: 0o755 });
    const cpaChild = spawn(unkillableCpa, ['-config', cfgPath], spawnOpts);
    activeProcesses.push(cpaChild);
    try {
      assert.equal(await waitForPortAccepting(customCpaPort), true);
      const startRes = spawnSync('bash', ['-c', `ps -ww -p ${cpaChild.pid} -o lstart=`], { encoding: 'utf8' });
      const startMarker = startRes.stdout.trim().replace(/\s+/g, ' ');
      writeFileSync(
        path.join(extRunDir, 'cpa-instance.provenance'),
        `instance|${path.join(extDataDir, 'runtime')}\n${cpaChild.pid}|${startMarker}\n`,
        { mode: 0o600 }
      );
      const cpaQuiesceRes = spawnSync(
        'bash',
        [path.join(bundleDir, 'cpa-manager-plusctl'), 'upgrade', '--home', targetHome, '--unit-dir', systemdDir, '--no-systemctl', '--no-restart'],
        { env: cleanEnv, encoding: 'utf8' }
      );
      assert.notEqual(cpaQuiesceRes.status, 0);
      assert.match(cpaQuiesceRes.stderr, /attributable CPA child process\(es\) still running/);
    } finally {
      cpaChild.kill('SIGKILL');
      await waitForExit(cpaChild);
      rmSync(path.join(extRunDir, 'cpa-instance.provenance'), { force: true });
      writeFileSync(unkillableCpa, savedCpaScript, { mode: 0o755 });
      writeFileSync(path.join(targetHome, 'cpa-manager-plusctl'), savedTargetCtl, { mode: 0o755 });
      writeFileSync(path.join(targetHome, 'cpa-manager-plus.env'), savedEnvContent, { mode: 0o600 });
    }

    // 4. Perform upgrade with clean env and verify regenerated unit reflects .env overrides
    const upgradeRes = spawnSync(
      'bash',
      [path.join(bundleDir, 'cpa-manager-plusctl'), 'upgrade', '--home', targetHome, '--unit-dir', systemdDir, '--no-systemctl', '--no-restart'],
      { env: cleanEnv, encoding: 'utf8' }
    );
    assert.equal(upgradeRes.status, 0, upgradeRes.stderr);
    const updatedUnit = readFileSync(unitPath, 'utf8');
    assert.match(updatedUnit, new RegExp(`^Environment="USAGE_DATA_DIR=${extDataDir}"$`, 'm'));
    assert.match(updatedUnit, new RegExp(`^Environment="CPA_MANAGER_PLUS_RUN_DIR=${extRunDir}"$`, 'm'));
    assert.match(updatedUnit, new RegExp(`^Environment="CPAMP_RUNTIME_CPA_ADDR=${customCpaAddr}"$`, 'm'));
    assert.match(updatedUnit, /^Environment="CPAMP_DRAIN_TIMEOUT=180s"$/m);
    assert.match(updatedUnit, /^Environment="CPAMP_SERVICE_STOP_TIMEOUT_SEC=240"$/m);
    assert.match(updatedUnit, /^TimeoutStopSec=240$/m);
    assert.match(updatedUnit, new RegExp(`^ReadWritePaths=.*${extDataDir}`, 'm'));
    assert.match(updatedUnit, new RegExp(`^ReadWritePaths=.*${extRunDir}`, 'm'));

    // 5. Drop-in empty Environment= / EnvironmentFile= / ReadWritePaths= reset earlier values per systemd.exec(5)
    const dropinDir = `${unitPath}.d`;
    mkdirSync(dropinDir, { recursive: true });
    const dropinEnvFile = path.join(baseDir, 'dropin-reset.env');
    writeFileSync(dropinEnvFile, 'CPAMP_DRAIN_TIMEOUT=110s\n', { mode: 0o600 });
    writeFileSync(
      path.join(dropinDir, '20-reset.conf'),
      [
        '[Service]',
        'Environment=',
        'Environment="CPAMP_DRAIN_TIMEOUT=220s"',
        'EnvironmentFile=',
        `EnvironmentFile=-${dropinEnvFile}`,
        'ReadWritePaths=',
        `ReadWritePaths=${extDataDir}`,
        '',
      ].join('\n')
    );
    const resetRes = spawnSync(
      'bash',
      [path.join(bundleDir, 'cpa-manager-plusctl'), 'upgrade', '--home', targetHome, '--unit-dir', systemdDir, '--no-systemctl', '--no-restart'],
      { env: cleanEnv, encoding: 'utf8' }
    );
    assert.equal(resetRes.status, 0, resetRes.stderr);
    const resetUnit = readFileSync(unitPath, 'utf8');
    // Empty EnvironmentFile= in drop-in cleared base unit's preserved EnvironmentFile=, so default cpa-manager-plus.env is emitted;
    // dropinEnvFile overrides drop-in Environment="CPAMP_DRAIN_TIMEOUT=220s" -> 110s (stop timeout 170).
    assert.match(resetUnit, /^Environment="CPAMP_DRAIN_TIMEOUT=110s"$/m);
    assert.match(resetUnit, /^TimeoutStopSec=170$/m);
    assert.match(resetUnit, /^Environment="CPAMP_SERVICE_STOP_TIMEOUT_SEC=170"$/m);
    // Empty Environment= cleared base unit's custom USAGE_DATA_DIR/CPA_MANAGER_PLUS_RUN_DIR before dropinEnvFile (which didn't set them)
    assert.match(resetUnit, new RegExp(`^Environment="USAGE_DATA_DIR=${targetHome}/data"$`, 'm'));
    assert.match(resetUnit, new RegExp(`^ReadWritePaths=${targetHome}/data ${targetHome}/run ${targetHome}/logs ${extDataDir}$`, 'm'));
  });

  it('falls back expected_cpa_uid to 10001 on Linux root when cpamp-cpa account is absent', () => {
    const baseDir = makeTempDir('cpamp-expected-uid-');
    const stubDir = path.join(baseDir, 'stub-bin');
    mkdirSync(stubDir, { recursive: true });
    writeFileSync(
      path.join(stubDir, 'uname'),
      '#!/usr/bin/env bash\nif [ "${1:-}" = "-s" ]; then echo "Linux"; exit 0; fi\nexec /usr/bin/uname "$@"\n',
      { mode: 0o755 }
    );
    writeFileSync(
      path.join(stubDir, 'id'),
      [
        '#!/usr/bin/env bash',
        'if [ "${1:-}" = "-u" ] && [ "$#" -eq 1 ]; then echo "${MOCK_ID_U:-0}"; exit 0; fi',
        'if [ "${1:-}" = "-u" ] && [ "${2:-}" = "cpamp-cpa" ]; then',
        '  if [ -n "${MOCK_CPA_USER_UID:-}" ]; then echo "${MOCK_CPA_USER_UID}"; exit 0; fi',
        '  exit 1',
        'fi',
        'exit 1',
        '',
      ].join('\n'),
      { mode: 0o755 }
    );

    const ctlLibPath = path.join(baseDir, 'ctl-lib.sh');
    const ctlBody = readFileSync(ctlScript, 'utf8').replace(/\ncommand="\$\{1:-status\}[\s\S]*$/, '\n');
    writeFileSync(ctlLibPath, ctlBody, { mode: 0o644 });

    const runCheck = (extraEnv) => {
      const res = spawnSync(
        'bash',
        [
          '-c',
          'source "$1" && expected_cpa_uid',
          '_',
          ctlLibPath,
        ],
        {
          env: {
            PATH: `${stubDir}:${process.env.PATH || ''}`,
            ...extraEnv,
          },
          encoding: 'utf8',
        }
      );
      assert.equal(res.status, 0, res.stderr);
      return res.stdout.trim();
    };

    assert.equal(runCheck({ MOCK_ID_U: '0' }), '10001');
    assert.equal(runCheck({ MOCK_ID_U: '0', MOCK_CPA_USER_UID: '10042' }), '10042');
    assert.equal(runCheck({ MOCK_ID_U: '0', CPAMP_RUNTIME_CPA_UID: '10099' }), '10099');
    assert.equal(runCheck({ MOCK_ID_U: '501' }), '501');
  });

  it('preserves optional ReadWritePaths=-/path without globbing, fails closed on malformed swapping lock metadata, and handles swap signals', async () => {
    const createBundleDir = (baseDir, name, versionLabel) => {
      const dir = path.join(baseDir, name);
      const mock = createMockNativeHome(path.join(baseDir, `${name}-mock`));
      mkdirSync(path.join(dir, 'bin'), { recursive: true, mode: 0o755 });
      mkdirSync(path.join(dir, 'share/cpamp/runtime'), { recursive: true, mode: 0o755 });
      mkdirSync(path.join(dir, 'data'), { recursive: true, mode: 0o700 });
      mkdirSync(path.join(dir, 'run'), { recursive: true, mode: 0o700 });
      mkdirSync(path.join(dir, 'logs'), { recursive: true, mode: 0o700 });
      for (const binName of ['cpa-manager-plus', 'cpa-manager-plus-ingress', 'cpa-manager-plus-runtime-supervisor', 'cli-proxy-api']) {
        copyFileSync(path.join(mock.binDir, binName), path.join(dir, 'bin', binName));
        chmodSync(path.join(dir, 'bin', binName), 0o755);
      }
      copyFileSync(launcherScript, path.join(dir, 'bin/cpa-manager-plus-launcher'));
      copyFileSync(launcherScript, path.join(dir, 'cpa-manager-plus-launcher'));
      copyFileSync(ctlScript, path.join(dir, 'cpa-manager-plusctl'));
      chmodSync(path.join(dir, 'bin/cpa-manager-plus-launcher'), 0o755);
      chmodSync(path.join(dir, 'cpa-manager-plus-launcher'), 0o755);
      chmodSync(path.join(dir, 'cpa-manager-plusctl'), 0o755);
      copyFileSync(systemdUnitTemplate, path.join(dir, 'cpa-manager-plus.service'));
      writeFileSync(path.join(dir, 'share/cpamp/provenance.json'), JSON.stringify({ version: versionLabel }));
      writeFileSync(path.join(dir, 'share/cpamp/runtime/seed-lineage.json'), JSON.stringify({ seed: versionLabel }));
      return dir;
    };

    // 1. Carbon Low-1: optional ReadWritePaths=-/path preserved verbatim without glob expansion
    {
      const baseDir = makeTempDir('cpamp-rw-optional-');
      const targetHome = createBundleDir(baseDir, 'target', 'v2.0.0-beta.1');
      const bundleDir = createBundleDir(baseDir, 'bundle', 'v2.0.0-beta.2');
      const systemdDir = path.join(baseDir, 'systemd');
      mkdirSync(systemdDir, { recursive: true });
      const unitPath = path.join(systemdDir, 'cpa-manager-plus.service');
      writeFileSync(
        unitPath,
        [
          '[Service]',
          `WorkingDirectory=${targetHome}`,
          `ExecStart=${targetHome}/bin/cpa-manager-plus-launcher`,
          `ReadWritePaths=${targetHome}/data ${targetHome}/run ${targetHome}/logs -/var/opt/cpamp-optional -/var/opt/glob*dir`,
          '',
        ].join('\n')
      );
      const cleanEnv = {
        PATH: process.env.PATH || '',
        HOME: process.env.HOME || '',
        CPAMP_TEST_ALLOW_NON_ROOT_UPGRADE: '1',
      };
      const okRes = spawnSync(
        'bash',
        [path.join(bundleDir, 'cpa-manager-plusctl'), 'upgrade', '--home', targetHome, '--unit-dir', systemdDir, '--no-systemctl', '--no-restart'],
        { env: cleanEnv, encoding: 'utf8' }
      );
      assert.equal(okRes.status, 0, okRes.stderr);
      const updated = readFileSync(unitPath, 'utf8');
      assert.match(updated, /^ReadWritePaths=.*-\/var\/opt\/cpamp-optional -\/var\/opt\/glob\*dir/m);

      // Rejects double-dash or non-absolute path after '-'
      for (const badEntry of ['--/var/opt/bad', '-relative/path']) {
        writeFileSync(
          unitPath,
          [
            '[Service]',
            `WorkingDirectory=${targetHome}`,
            `ExecStart=${targetHome}/bin/cpa-manager-plus-launcher`,
            `ReadWritePaths=${targetHome}/data ${badEntry}`,
            '',
          ].join('\n')
        );
        const badRes = spawnSync(
          'bash',
          [path.join(bundleDir, 'cpa-manager-plusctl'), 'upgrade', '--home', targetHome, '--unit-dir', systemdDir, '--no-systemctl', '--no-restart'],
          { env: cleanEnv, encoding: 'utf8' }
        );
        assert.notEqual(badRes.status, 0);
        assert.match(badRes.stderr, /must be an absolute path/);
      }
    }

    // 2. Carbon Low-2: check_upgrade_swap_lock fails closed on missing/empty/invalid/symlinked lock metadata
    {
      const baseDir = makeTempDir('cpamp-swap-lock-hardening-');
      const { homeDir, binDir } = createMockNativeHome(baseDir);
      const rawEnv = await buildPortEnv(homeDir, { MOCK_FAIL_ROLE: 'runtime-supervisor' });
      const { _ports: _ports, ...env } = rawEnv;
      const lockDir = path.join(homeDir, '.upgrade.lock');
      const swappingPath = path.join(lockDir, 'swapping');
      const pidPath = path.join(lockDir, 'pid');

      const resetLockDir = () => {
        rmSync(lockDir, { recursive: true, force: true });
        mkdirSync(lockDir, { mode: 0o700 });
      };

      const expectLockRefusal = () => {
        const res = spawnSync('bash', [path.join(binDir, 'cpa-manager-plus-launcher')], {
          env,
          encoding: 'utf8',
        });
        assert.notEqual(res.status, 0);
        assert.match(res.stderr, /Refusing to start launcher while upgrade file swap is in progress/);
      };

      // Missing pid file
      resetLockDir();
      writeFileSync(swappingPath, '1\n');
      expectLockRefusal();

      // Empty pid file
      resetLockDir();
      writeFileSync(swappingPath, '1\n');
      writeFileSync(pidPath, '   \n');
      expectLockRefusal();

      // Non-numeric / zero pid
      for (const badPid of ['abc', '0', '-5']) {
        resetLockDir();
        writeFileSync(swappingPath, '1\n');
        writeFileSync(pidPath, `${badPid}\n`);
        expectLockRefusal();
      }

      // Symlinked pid file or symlinked swapping file or directory swapping
      resetLockDir();
      writeFileSync(swappingPath, '1\n');
      const outsideFile = path.join(baseDir, 'outside-pid');
      writeFileSync(outsideFile, '999999\n');
      symlinkSync(outsideFile, pidPath);
      expectLockRefusal();

      resetLockDir();
      symlinkSync(outsideFile, swappingPath);
      writeFileSync(pidPath, '999999\n');
      expectLockRefusal();

      resetLockDir();
      mkdirSync(swappingPath, { mode: 0o700 });
      writeFileSync(pidPath, '999999\n');
      expectLockRefusal();

      // Valid dead positive integer PID ignores swapping_flag and proceeds past check_upgrade_swap_lock
      const deadPid = spawnSync('bash', ['-c', 'echo $$'], { encoding: 'utf8' }).stdout.trim();
      resetLockDir();
      writeFileSync(swappingPath, `${deadPid}\n`);
      writeFileSync(pidPath, `${deadPid}\n`);
      const deadRes = spawnSync('bash', [path.join(binDir, 'cpa-manager-plus-launcher')], {
        env,
        encoding: 'utf8',
      });
      assert.doesNotMatch(deadRes.stderr, /Refusing to start launcher while upgrade file swap is in progress/);
    }

    // 3. Opus L2: SIGTERM mid-swap triggers clean rollback of target_home and unit
    {
      const baseDir = makeTempDir('cpamp-swap-sigterm-rollback-');
      const targetHome = createBundleDir(baseDir, 'target', 'v2.0.0-beta.1');
      const bundleDir = createBundleDir(baseDir, 'bundle', 'v2.0.0-beta.2');
      const systemdDir = path.join(baseDir, 'systemd');
      mkdirSync(systemdDir, { recursive: true });
      const unitPath = path.join(systemdDir, 'cpa-manager-plus.service');
      const origUnit = `[Service]\nWorkingDirectory=${targetHome}\nExecStart=${targetHome}/bin/cpa-manager-plus-launcher\n`;
      writeFileSync(unitPath, origUnit);
      writeFileSync(path.join(targetHome, 'data/usage.sqlite'), 'sigterm-keep');

      const stubBinDir = path.join(baseDir, 'stub-bin');
      mkdirSync(stubBinDir, { recursive: true });
      const realMv = execFileSync('which', ['mv'], { encoding: 'utf8' }).trim();
      const mvStub = path.join(stubBinDir, 'mv');
      writeFileSync(
        mvStub,
        [
          '#!/usr/bin/env bash',
          'case "${1:-}" in',
          '  */.upgrade-stage.*/share)',
          `    "${realMv}" "$@"`,
          '    kill -TERM "$PPID" >/dev/null 2>&1 || true',
          '    sleep 0.2',
          '    exit 0',
          '    ;;',
          'esac',
          `exec "${realMv}" "$@"`,
          '',
        ].join('\n'),
        { mode: 0o755 }
      );
      chmodSync(mvStub, 0o755);

      const rawEnv = await buildPortEnv(targetHome, {
        CPAMP_TEST_ALLOW_NON_ROOT_UPGRADE: '1',
        PATH: `${stubBinDir}:${process.env.PATH || ''}`,
      });
      const { _ports: _ports, ...env } = rawEnv;
      const res = spawnSync(
        'bash',
        [path.join(bundleDir, 'cpa-manager-plusctl'), 'upgrade', '--home', targetHome, '--unit-dir', systemdDir, '--no-systemctl'],
        { env, encoding: 'utf8' }
      );
      assert.notEqual(res.status, 0);
      assert.match(res.stderr, /Upgrade swap interrupted by SIGTERM; rolling back/);
      assert.equal(
        JSON.parse(readFileSync(path.join(targetHome, 'share/cpamp/provenance.json'), 'utf8')).version,
        'v2.0.0-beta.1'
      );
      assert.equal(readFileSync(path.join(targetHome, 'data/usage.sqlite'), 'utf8'), 'sigterm-keep');
      assert.equal(readFileSync(unitPath, 'utf8'), origUnit);
      assert.equal(readdirSync(targetHome).filter((n) => n.startsWith('.upgrade-backup.')).length, 0);
      assert.equal(existsSync(path.join(targetHome, '.upgrade.lock')), false);
    }
  });
});
