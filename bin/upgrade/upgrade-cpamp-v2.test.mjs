#!/usr/bin/env node
// Synthetic orchestration only. A private fake Docker executable fails closed
// on unknown commands. No Docker daemon, live deployment or user data is read.
// Run at integration: node --test bin/upgrade/upgrade-cpamp-v2.test.mjs
import assert from 'node:assert/strict';
import { spawn, spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, statSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import test, { after, before } from 'node:test';
import { fileURLToPath } from 'node:url';

const script = resolve(dirname(fileURLToPath(import.meta.url)), '../upgrade-cpamp-v2.sh');
const managerID = 'a'.repeat(64);
const cpaID = 'b'.repeat(64);
const targetID = 'c'.repeat(64);
const imageID = `sha256:${'d'.repeat(64)}`;
const nextImageID = `sha256:${'8'.repeat(64)}`;
const managerImage = `sha256:${'e'.repeat(64)}`;
const cpaImage = `sha256:${'f'.repeat(64)}`;
const revision = '1'.repeat(40);
const operation = `cpamp-v2-${managerID.slice(0, 16)}`;

// Synthetic Docker handler shared by the persistent per-suite UDS worker and standalone Node fallback.
const fakeDockerHandler = String.raw`
function handleFakeDocker(args, statePath) {
if (!statePath) throw new Error('Synthetic state required');
const s = JSON.parse(fs.readFileSync(statePath, 'utf8'));
s.calls.push(args);
const recordMutationLock = () => {
  if (s.stateDirForLockCheck) {
    s.locksObserved = s.locksObserved || [];
    s.locksObserved.push({
      op: args[0],
      locked: fs.existsSync(s.stateDirForLockCheck + '/.lock/pid'),
    });
  }
};
const save = () => fs.writeFileSync(statePath, JSON.stringify(s));
const exit = (code, stdout = '', stderr = '') => {
  const err = new Error('FAKE_DOCKER_EXIT');
  err.fakeDockerResult = { exitCode: code, stdout, stderr };
  throw err;
};
const out = value => { save(); exit(0, value !== undefined && value !== '' ? String(value) + '\n' : '', ''); };
const refuse = message => { save(); exit(1, '', String(message) + '\n'); };
const goJSON = value => JSON.stringify(value).replace(/&/g, '\\u0026');
const opt = name => { const idx = args.indexOf(name); return idx >= 0 ? args[idx + 1] : ''; };
const values = name => args.flatMap((v, i) => v === name ? [args[i + 1]] : []);
const container = name => Object.values(s.containers).filter(Boolean).find(c => c.id === name || c.name === name);

const allowedEnvValueReads = new Set([
  'USAGE_DATA_DIR', 'USAGE_DB_PATH', 'CPA_MANAGER_DATA_KEY_PATH',
  'CPA_MANAGER_CONFIG', 'CPA_MANAGER_DATA_KEY_FILE', 'CPAMP_PUBLIC_ORIGIN',
  'CPAMP_UPGRADE_WORKER_SOCKET', 'CPAMP_UPGRADE_SOURCE_ROOT',
  'CPAMP_UPGRADE_CPA_ROOT', 'CPAMP_UPGRADE_CPA_AUTH_ROOT',
  'CPAMP_UPGRADE_CPA_VERSION', 'CPAMP_LEGACY_ROOT', 'CPAMP_RUNTIME_DATA_DIR',
]);
const allowedEnvPresenceChecks = new Set(['CPA_MANAGER_DATA_KEY', 'CPAMP_LEGACY_ROOT']);

const parsePublish = spec => {
  if (spec.startsWith('[')) {
    const m = spec.match(/^\[([^\]]+)\]:(\d+):(\d+)$/);
    if (!m) throw new Error('Invalid IPv6 --publish: ' + spec);
    return { ip: m[1], port: m[2], proto: m[3] + '/tcp' };
  }
  const parts = spec.split(':');
  if (parts.length === 3) return { ip: parts[0], port: parts[1], proto: parts[2] + '/tcp' };
  if (parts.length === 2) return { ip: '', port: parts[0], proto: parts[1] + '/tcp' };
  throw new Error('Invalid --publish: ' + spec);
};
const parseMount = value => {
  const parts = value.split(',');
  const fields = {};
  for (const part of parts) {
    const idx = part.indexOf('=');
    if (idx > 0) fields[part.slice(0, idx)] = part.slice(idx + 1);
  }
  const readonly = parts.includes('readonly') || parts.includes('ro');
  return { type: fields.type, src: fields.src, dst: fields.dst, readonly };
};
const format = (c, t) => {
  if (t === '{{.Id}}') return c.id;
  if (t === '{{.Image}}') return c.image;
  if (t === '{{.Config.Image}}') return c.imageRef;
  if (t === '{{.State.Running}}') return String(c.running);
  if (t === '{{.HostConfig.Privileged}}') return String(Boolean(c.privileged));
  if (t === '{{.HostConfig.NetworkMode}}') return c.networkMode || c.networks[0] || 'bridge';
  if (t.includes('.Config.Labels')) {
    const key = t.match(/index \.Config\.Labels "([^"]+)"/)[1];
    return c.labels[key] || '';
  }
  if (t.includes('.Config.Env')) {
    const key = t.match(/\) "([^"]+)"/)[1];
    if (t.includes('println "1"')) {
      if (!allowedEnvPresenceChecks.has(key)) throw new Error('Unapproved env presence check: ' + key);
      return c.env[key] !== undefined && c.env[key] !== '' ? '1' : '';
    }
    if (!allowedEnvValueReads.has(key)) throw new Error('Unapproved env read: ' + key);
    return c.env[key] !== undefined && c.env[key] !== '' ? key + '=' + c.env[key] : '';
  }
  if (t.includes('.HostConfig.Tmpfs')) {
    return Object.keys(c.tmpfs || {}).join('\n');
  }
  if (t.includes('range .Mounts')) {
    const dashEmptySrc = t.includes('if .Source');
    return c.mounts.map(m => [
      m.type,
      m.type === 'volume' ? (m.name || m.src || '-') : '-',
      m.src ? m.src : (dashEmptySrc ? '-' : ''),
      m.dst,
      String(!m.readonly),
    ].join('\t')).join('\n');
  }
  if (t.includes('.NetworkSettings.Ports') || t.includes('.HostConfig.PortBindings')) {
    if (t.includes('.NetworkSettings.Ports') && !c.running) return '';
    if (t.includes('%s=')) {
      const hostBindings = c.portBindings ?? c.ports;
      return hostBindings.map(p => (p.proto || '18317/tcp') + '=' + (p.ip || '0.0.0.0') + ':' + p.port + ';').join('');
    }
    const sourceList = t.includes('.NetworkSettings.Ports') ? c.ports : (c.portBindings ?? c.ports);
    const bindings = sourceList.filter(p => (p.proto || '18317/tcp') === '18317/tcp');
    if (t.includes('%s:%s;')) {
      const publishAllPrefix = t.includes('if .HostConfig.PublishAllPorts') && c.publishAllPorts ? 'publish-all;' : '';
      return publishAllPrefix + bindings.map(p => p.ip + ':' + p.port + ';').join('');
    }
    const fallbackZero = t.includes('else}}0.0.0.0');
    return bindings.map(p => (fallbackZero ? (p.ip || '0.0.0.0') : p.ip) + '\t' + p.port).join('\n');
  }
  if (t.includes('index .NetworkSettings.Networks')) {
    const name = t.match(/Networks "([^"]+)"/)[1];
    return c.networks.includes(name) ? 'yes' : '';
  }
  if (t.includes('range $name,$network := .NetworkSettings.Networks')) return c.networks.join('\n');
  throw new Error('Unknown inspect format: ' + t);
};

if (args[0] === 'ps') {
  if (args.some(a => a === 'label=com.docker.compose.service=cli-proxy-api')) out(s.cpaID || '');
  if (args.some(a => a === 'label=com.docker.compose.service=cpa-manager-plus')) out(s.ambiguous ? s.managerID + '\n' + s.cpaID : s.managerID);
  throw new Error('Unknown container discovery');
}
if (args[0] === 'inspect') {
  const c = container(args.at(-1));
  if (!c) refuse('No such synthetic container');
  const fmt = args.includes('--format') ? opt('--format') : '';
  if (
    fmt === '{{.State.Running}}' &&
    c.id === s.targetID &&
    s.stopTargetAfterCanonicalPolls &&
    (s.canonicalRoutePolls || 0) >= s.stopTargetAfterCanonicalPolls
  ) {
    c.running = false;
  }
  out(fmt ? format(c, fmt) : '[]');
}
if (args[0] === 'image' && args[1] === 'inspect') {
  const ref = args.at(-1);
  const image = s.images[ref];
  if (!image) refuse('No synthetic image');
  const t = opt('--format');
  if (t === '{{.Id}}') out(image.id);
  const key = t.match(/index \.Config\.Labels "([^"]+)"/)[1];
  out(image.labels[key] || '');
}
if (args[0] === 'pull') {
  const ref = args[1];
  if (!s.pullable[ref]) refuse('Pull failed');
  s.images[ref] = s.pullable[ref];
  s.images[s.pullable[ref].id] = s.pullable[ref];
  out('Synthetic image prefetched');
}
if (args[0] === 'volume') {
  const name = args.at(-1);
  if (args[1] === 'create' && s.unlinkLockPidAndFailOnce) {
    s.unlinkLockPidAndFailOnce = false;
    if (s.stateDirForLockCheck) {
      fs.rmSync(s.stateDirForLockCheck + '/.lock/pid', { force: true });
    }
    refuse('Synthetic interruption before lock pid persistence');
  }
  if (args[1] === 'create') recordMutationLock();
  if (args[1] === 'create') { s.volumes[name] = { operation: opt('--label').split('=')[1] }; out(name); }
  if (!s.volumes[name]) refuse('No synthetic volume');
  out(args.includes('--format') ? (s.volumes[name].operation || '') : '[]');
}
if (args[0] === 'network') {
  if (args[1] === 'inspect') {
    const ref = args.at(-1);
    const alias = s.networkAliases ? s.networkAliases[ref] : undefined;
    const resolved = alias !== undefined ? alias : (s.networks.includes(ref) ? ref : undefined);
    if (resolved === undefined) refuse('No synthetic network');
    if (args.includes('--format')) {
      if (opt('--format') !== '{{.Name}}') throw new Error('Unexpected network inspect format: ' + opt('--format'));
      out(resolved);
    }
    out('[]');
  }
  if (args[1] === 'connect') {
    if (s.failConnectOnce) { s.failConnectOnce = false; refuse('Synthetic network interruption'); }
    recordMutationLock();
    const c = container(args.at(-1));
    if (!c) refuse('No synthetic container');
    if (!c.networks.includes(args[2])) c.networks.push(args[2]);
    out();
  }
}
if (args[0] === 'run') {
  recordMutationLock();
  if (opt('--network') !== 'none') throw new Error('Offline upgrade CLI must run with --network none');
  if (opt('--user') !== '0:0') throw new Error('Offline upgrade CLI must run with --user 0:0');
  if (opt('--entrypoint') !== '/usr/local/bin/cpa-manager-plus') throw new Error('Unexpected entrypoint');
  const mounts = values('--mount').map(parseMount);
  if (mounts.some(m => m.dst === '/legacy' || m.dst.startsWith('/legacy/'))) throw new Error('Forbidden /legacy mount');
  const srcMount = mounts.find(m => m.dst === '/upgrade/source');
  const dataMount = mounts.find(m => m.dst === '/data');
  const backupMount = mounts.find(m => m.dst === '/upgrade/backup');
  if (!srcMount || !srcMount.readonly) throw new Error('Offline source mount must be read-only');
  if (!dataMount || dataMount.readonly || dataMount.type !== 'volume') throw new Error('Offline /data mount must be RW volume');
  if (!backupMount || backupMount.readonly || backupMount.type !== 'volume') throw new Error('Offline /upgrade/backup mount must be RW volume');
  const upIdx = args.indexOf('upgrade');
  const subcommand = args[upIdx + 1];
  const subArgs = args.slice(upIdx + 2);
  if (subArgs.some(a => a.startsWith('--acknowledge'))) throw new Error('Forbidden silent acknowledgement flag');
  const subOpt = flag => { const idx = subArgs.indexOf(flag); return idx >= 0 ? subArgs[idx + 1] : undefined; };
  const srcVer = subOpt('--source-version');
  if (srcVer !== undefined && !/^v?1\.14\.(0|[1-9][0-9]{0,2})$/.test(srcVer)) {
    save(); exit(1);
  }
  if (subcommand === 'preflight') {
    if (subOpt('--legacy') !== '/upgrade/source' || subOpt('--target-dir') !== '/data' || subOpt('--target') !== undefined) {
      throw new Error('Invalid upgrade preflight flags: ' + subArgs.join(' '));
    }
    save(); exit(s.exitCodes.preflight || 0);
  }
  if (subcommand === 'run') {
    if (subOpt('--legacy') !== '/upgrade/source' || subOpt('--target') !== '/data' || subOpt('--bundle') !== '/upgrade/backup/bundle' || subOpt('--target-dir') !== undefined) {
      throw new Error('Invalid upgrade run flags: ' + subArgs.join(' '));
    }
    const code = s.exitCodes.run || 0;
    if (code === 0) {
      s.backupVerified = true;
      if (!s.operationStage) {
        s.operationStage = 'copy_in';
        s.copyInMaterializations = (s.copyInMaterializations || 0) + 1;
      }
    }
    save(); exit(code);
  }
  if (subcommand === 'reconcile') {
    if (subOpt('--legacy') !== '/upgrade/source' || subOpt('--target') !== '/data' || subOpt('--bundle') !== '/upgrade/backup/bundle' || !subArgs.includes('--json')) {
      throw new Error('Invalid upgrade reconcile flags: ' + subArgs.join(' '));
    }
    if (!s.backupVerified) throw new Error('Reconcile requires verified backup from upgrade run');
    const code = s.exitCodes.reconcile || 0;
    if (code === 0) {
      s.operationStage = 'reconcile';
      out(JSON.stringify({
        preflight: { ready: true },
        report: { recorded: true, stage: 'reconcile', unacknowledgedDays: [] },
      }));
    }
    if (code === 1) {
      const preflightReady = s.reconcilePreflightReady !== false;
      const errorCode = s.reconcileErrorCode !== undefined ? s.reconcileErrorCode : 'upgrade_reconcile_mismatch';
      const responseBody = { preflight: { ready: preflightReady } };
      if (preflightReady && errorCode) {
        responseBody.report = {
          recorded: false,
          unacknowledgedDays: errorCode === 'upgrade_reconcile_mismatch' ? ['2026-04-01'] : [],
        };
        responseBody.error = errorCode + ': synthetic reconcile refusal';
        responseBody.errorCode = errorCode;
      } else if (preflightReady) {
        responseBody.error = 'synthetic untyped reconcile refusal';
      }
      save();
      exit(1, JSON.stringify(responseBody) + '\n', '');
    }
    save(); exit(code);
  }
  throw new Error('Unexpected upgrade subcommand: ' + subcommand);
}
if (args[0] === 'create') {
  const labels = Object.fromEntries(values('--label').map(l => { const eq = l.indexOf('='); return [l.slice(0, eq), l.slice(eq + 1)]; }));
  const rawMounts = values('--mount').map(parseMount);
  const hasFullVolumeTopology = ['/data', '/runtime', '/upgrade/backup'].every(dst => {
    const m = rawMounts.find(x => x.dst === dst);
    if (!m || m.type !== 'volume' || !s.volumes[m.src]) return false;
    return dst === '/upgrade/backup' ? m.readonly : !m.readonly;
  }) && rawMounts.some(x => x.dst === '/upgrade/source' && x.readonly);
  const normalizeBind = m => (
    m.type === 'bind' && s.daemonBindPrefix && hasFullVolumeTopology && !m.src.startsWith(s.daemonBindPrefix + '/')
      ? { ...m, src: s.daemonBindPrefix + m.src }
      : m
  );
  const mounts = rawMounts.map(normalizeBind);
  if (labels['dev.cpamp.upgrade.mount-probe']) {
    if (opt('--network') !== 'none') throw new Error('Mount probe must use --network none');
    if (!hasFullVolumeTopology) throw new Error('Mount probe must match full target volume+bind topology');
    if (mounts.filter(m => m.dst.startsWith('/upgrade/')).some(m => !m.readonly)) {
      throw new Error('Mount probe /upgrade/* mounts must all be read-only');
    }
    const probeID = '7'.repeat(64);
    s.containers.mountProbe = {
      id: probeID, name: '', image: args.at(-1), imageRef: args.at(-1), running: false, privileged: false,
      networkMode: 'none', labels, mounts, networks: ['none'], env: {}, ports: [],
    };
    out(probeID);
  }
  if (s.failTargetCreateOnce) { s.failTargetCreateOnce = false; refuse('Synthetic target create interruption'); }
  recordMutationLock();
  const name = opt('--name');
  if (container(name)) refuse('Synthetic name exists');
  const ports = values('--publish').map(parsePublish);
  const createdImg = args.at(-1);
  s.containers.target = {
    id: s.targetID, name, image: createdImg, imageRef: createdImg, running: false, privileged: args.includes('--privileged'),
    networkMode: opt('--network'), labels, mounts, networks: [opt('--network')],
    env: Object.fromEntries(values('--env').map(v => { const eq = v.indexOf('='); return [v.slice(0, eq), v.slice(eq + 1)]; })),
    ports,
  };
  out(s.targetID);
}
if (args[0] === 'container' && args[1] === 'rm') {
  if (args.length !== 3) throw new Error('Unexpected container rm flags: ' + args.join(' '));
  const probe = s.containers.mountProbe;
  if (!probe || probe.id !== args[2] || probe.running || !probe.labels['dev.cpamp.upgrade.mount-probe']) {
    throw new Error('Forbidden container rm target: ' + args[2]);
  }
  delete s.containers.mountProbe;
  out(probe.id);
}
if (args[0] === 'stop' || args[0] === 'start') {
  recordMutationLock();
  const c = container(args.at(-1));
  if (!c) refuse('No synthetic service');
  if (c.labels['dev.cpamp.upgrade.mount-probe']) throw new Error('Mount probe must never be started or stopped');
  c.running = args[0] === 'start';
  out(c.id);
}
if (args[0] === 'exec') {
  const c = container(args[1]);
  if (!c || !c.running) refuse('Target container is not running');
  const cmd = args.slice(2);
  if (cmd[0] === 'wget') {
    if (cmd.includes('--spider')) refuse('HEAD/spider probe rejected; use GET');
    const url = cmd.at(-1);
    if (url === 'http://127.0.0.1:9082/internal/routing/fresh') {
      s.canonicalRoutePolls = (s.canonicalRoutePolls || 0) + 1;
      if (s.canonicalReadyAfterPolls && s.canonicalRoutePolls <= s.canonicalReadyAfterPolls) {
        save(); exit(1);
      }
      if (s.operationStage === 'complete' && s.committedControlPath && !s.hideFreshControlRoute) {
        out(goJSON({
          installationId: 'inst-1', generation: 'gen-1', operationId: 'op-1', revision: 1,
          target: s.freshRouteTarget ?? s.committedControlPath, committed: s.freshRouteCommitted !== false,
        }));
      }
      save(); exit(1);
    }
    if (url.endsWith('/api/v2/bootstrap/identity')) {
      s.identityRoutePolls = (s.identityRoutePolls || 0) + 1;
      if (s.canonicalReadyAfterPolls && s.identityRoutePolls <= s.canonicalReadyAfterPolls) {
        save(); exit(1);
      }
      const expected = s.committedControlPath ? 'http://127.0.0.1:18317' + s.committedControlPath + '/api/v2/bootstrap/identity' : '';
      if (s.operationStage === 'complete' && url === expected && !s.identityProbeFails) out('{}');
      save(); exit(1);
    }
    s.readinessPolls = (s.readinessPolls || 0) + 1;
    if (s.targetUnready || (s.readyAfterPolls && s.readinessPolls <= s.readyAfterPolls)) {
      save(); exit(1);
    }
    // Unfinished upgrade: /.well-known/cpamp-bootstrap/ui is 200 OK while /health legitimately returns 503.
    if (url === 'http://127.0.0.1:18317/.well-known/cpamp-bootstrap/ui') {
      if (s.operationStage !== 'complete' && !s.forgedHealthOnly) out('');
      save(); exit(1);
    }
    if (url === 'http://127.0.0.1:18317/health') {
      if (s.operationStage === 'complete' || s.forgedHealthOnly) out('');
      save(); exit(1);
    }
    save(); exit(1);
  }
  if (cmd[0] === '/usr/local/bin/cpa-manager-plus' && cmd[1] === 'upgrade' && cmd[2] === 'status') {
    if (cmd[cmd.indexOf('--db-path') + 1] !== '/data/usage.sqlite') throw new Error('Unexpected status --db-path');
    if (s.statusTransientFailures && s.statusTransientFailures > 0) {
      s.statusTransientFailures -= 1;
      save(); exit(2);
    }
    if (!s.operationStage) {
      out(cmd.includes('--json') ? JSON.stringify({ recorded: false }) : 'no upgrade operation recorded');
    }
    if (cmd.includes('--json')) {
      out(goJSON({
        recorded: true,
        operation: {
          stage: s.operationStage,
          domainsDone: ['source_copy'],
          routingCommitted: Boolean(s.committedControlPath),
          controlBasePath: s.committedControlPath || '',
        },
      }));
    }
    out('stage: ' + s.operationStage + '\ndomains done: source_copy');
  }
  throw new Error('Unexpected docker exec command: ' + cmd.join(' '));
}
if (args[0] === 'context' && args[1] === 'inspect') out(s.contextHost || 'unix:///synthetic/docker.sock');
throw new Error('Unknown synthetic Docker call: ' + JSON.stringify(args));
}
`;

const fakeDockerStandalone = `#!/usr/bin/env node
const fs = require('node:fs');
${fakeDockerHandler}
try {
  handleFakeDocker(process.argv.slice(2), process.env.CPAMP_SYNTHETIC_DOCKER_STATE);
} catch (err) {
  if (err && err.fakeDockerResult) {
    if (err.fakeDockerResult.stdout) process.stdout.write(err.fakeDockerResult.stdout);
    if (err.fakeDockerResult.stderr) process.stderr.write(err.fakeDockerResult.stderr);
    process.exit(err.fakeDockerResult.exitCode);
  }
  throw err;
}
`;

const fakeDockerDaemonSource = `const fs = require('node:fs');
const http = require('node:http');
const path = require('node:path');
${fakeDockerHandler}
const sockPath = process.argv[2];
const server = http.createServer((req, res) => {
  const chunks = [];
  req.on('data', chunk => chunks.push(chunk));
  req.on('end', () => {
    const raw = Buffer.concat(chunks);
    const parts = raw.length > 0 && raw[raw.length - 1] === 0
      ? raw.subarray(0, raw.length - 1).toString('utf8').split('\\0')
      : raw.toString('utf8').split('\\0');
    const statePath = parts[0] || '';
    const args = parts.slice(1);
    try {
      handleFakeDocker(args, statePath);
      res.writeHead(200);
      res.end();
    } catch (err) {
      if (err && err.fakeDockerResult) {
        const { exitCode, stdout, stderr } = err.fakeDockerResult;
        if (stderr) {
          res.writeHead(400 + exitCode);
          res.end(stderr);
        } else {
          res.writeHead(200 + exitCode);
          res.end(stdout);
        }
        return;
      }
      res.writeHead(401);
      res.end((err && err.stack ? err.stack : String(err)) + '\\n');
    }
  });
});
let exiting = false;
const cleanupAndExit = () => {
  if (exiting) return;
  exiting = true;
  try { server.close(); } catch {}
  if (sockPath) {
    try { fs.rmSync(path.dirname(sockPath), { recursive: true, force: true }); } catch {}
  }
  process.exit(0);
};
process.stdin.resume();
process.stdin.on('end', cleanupAndExit);
process.stdin.on('close', cleanupAndExit);
process.stdin.on('error', cleanupAndExit);
process.on('SIGTERM', cleanupAndExit);
process.on('SIGINT', cleanupAndExit);
server.on('error', cleanupAndExit);
server.listen(sockPath, () => process.stdout.write('READY\\n'));
`;

const fakeDockerCurlShim = [
  '#!/bin/bash',
  'body="$(printf \'%s\\0\' "$CPAMP_SYNTHETIC_DOCKER_STATE" "$@" | curl -sS --unix-socket "$CPAMP_SYNTHETIC_DOCKER_SOCK" --data-binary @- -w \'\\n%{http_code}\' http://localhost/)" || exit 1',
  'code="${body##*$\'\\n\'}"',
  'payload="${body%$\'\\n\'*}"',
  '[[ "$code" =~ ^[0-9]+$ ]] && [ "$code" -ge 200 ] || exit 1',
  'if [ "$code" -ge 400 ]; then',
  '  [ -z "$payload" ] || printf \'%s\' "$payload" >&2',
  '  exit $((code - 400))',
  'fi',
  '[ -z "$payload" ] || printf \'%s\' "$payload"',
  'exit $((code - 200))',
  '',
].join('\n');

let daemonDir = '';
let daemonSock = '';
let daemonProc = null;
const stopDaemon = () => {
  if (daemonProc) {
    try { daemonProc.stdin?.destroy(); } catch {}
    try { daemonProc.kill(); } catch {}
    daemonProc = null;
  }
  if (daemonDir) {
    try { rmSync(daemonDir, { recursive: true, force: true }); } catch {}
    daemonDir = '';
    daemonSock = '';
  }
};
process.on('exit', stopDaemon);
const detachStartupSignalHandlers = () => {
  process.off('SIGINT', onStartupSigInt);
  process.off('SIGTERM', onStartupSigTerm);
};
const stopDaemonOnSignal = signal => {
  detachStartupSignalHandlers();
  stopDaemon();
  if (process.listenerCount(signal) === 0) process.kill(process.pid, signal);
};
const onStartupSigInt = () => stopDaemonOnSignal('SIGINT');
const onStartupSigTerm = () => stopDaemonOnSignal('SIGTERM');
before(async () => {
  if (spawnSync('curl', ['--version'], { stdio: 'ignore' }).status !== 0) return;
  // Catch SIGINT/SIGTERM during async daemon startup before daemon.cjs arms its
  // stdin-EOF watchdog; detach once READY arrives so synchronous spawnSync tests
  // keep SIG_DFL and terminate immediately instead of deferring signals past spawnSync.
  process.once('SIGINT', onStartupSigInt);
  process.once('SIGTERM', onStartupSigTerm);
  try {
    const baseDir = join(tmpdir(), 'cpamp-ds-').length + 12 < 90 ? tmpdir() : '/tmp';
    daemonDir = mkdtempSync(join(baseDir, 'cpamp-ds-'));
    daemonSock = join(daemonDir, 's');
    const daemonScript = join(daemonDir, 'daemon.cjs');
    writeFileSync(daemonScript, fakeDockerDaemonSource, { mode: 0o600 });
    daemonProc = spawn(process.execPath, [daemonScript, daemonSock], { stdio: ['pipe', 'pipe', 'pipe'] });
    daemonProc.stdin.on('error', () => {});
    const ready = await new Promise(resolveReady => {
      let buf = '';
      const timer = setTimeout(() => resolveReady(false), 5_000);
      daemonProc.stdout.on('data', chunk => {
        buf += chunk.toString('utf8');
        if (buf.includes('READY\n')) {
          clearTimeout(timer);
          resolveReady(true);
        }
      });
      daemonProc.once('exit', () => {
        clearTimeout(timer);
        resolveReady(false);
      });
    });
    if (!ready) stopDaemon();
  } finally {
    detachStartupSignalHandlers();
  }
});
after(stopDaemon);

function fixture(t, overrides = {}) {
  const root = mkdtempSync(join(tmpdir(), 'cpamp-v2-synthetic-'));
  const source = join(root, 'source with spaces; literal-dollar-$value');
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const executableDir = join(root, 'executables');
  mkdirSync(executableDir, { mode: 0o700 });
  writeFileSync(join(executableDir, 'package.json'), '{"type":"commonjs"}\n', { mode: 0o600 });
  writeFileSync(join(executableDir, 'docker'), daemonSock ? fakeDockerCurlShim : fakeDockerStandalone, { mode: 0o700 });
  writeFileSync(join(executableDir, 'sleep'), '#!/bin/sh\nexit 0\n', { mode: 0o700 });
  const cpaRoot = join(root, 'synthetic-cpa');
  const cpaConfigPath = join(cpaRoot, 'config.yaml');
  const cpaAuthDir = join(cpaRoot, 'auths');
  mkdirSync(source, { mode: 0o750 });
  mkdirSync(cpaAuthDir, { recursive: true, mode: 0o700 });
  writeFileSync(join(source, 'usage.sqlite'), 'synthetic metadata harness; not a database', { mode: 0o640 });
  writeFileSync(join(source, 'data.key'), 'synthetic-not-a-credential', { mode: 0o600 });
  writeFileSync(cpaConfigPath, 'synthetic fixture; never opened by orchestration', { mode: 0o600 });
  const manager = {
    id: managerID, name: 'synthetic-manager', image: managerImage, imageRef: 'synthetic/manager:old', running: true,
    labels: { 'com.docker.compose.project': 'synthetic-project', 'com.docker.compose.service': 'cpa-manager-plus' },
    env: {
      CPA_MANAGEMENT_KEY: 'synthetic-cpa-secret-not-for-host-state',
      CPA_MANAGER_ADMIN_KEY: 'synthetic-admin-secret-not-for-host-state',
      ...(overrides.managerEnv || {}),
    },
    mounts: overrides.managerMounts || [{
      type: overrides.bind ? 'bind' : 'volume',
      src: overrides.bind ? source : 'synthetic-manager-data',
      dst: '/data',
      readonly: false,
    }],
    networks: ['synthetic-default', 'synthetic-cpa'],
    ports: [{ ip: '0.0.0.0', port: '38684', proto: '18317/tcp' }],
  };
  const cpa = {
    id: cpaID, name: 'synthetic-cpa', image: cpaImage, imageRef: overrides.cpaImageRef ?? 'synthetic/cpa:v8.0.55', running: true,
    labels: { 'com.docker.compose.project': 'synthetic-project', 'com.docker.compose.service': 'cli-proxy-api' },
    env: {},
    mounts: overrides.cpaMounts || [
      { type: 'bind', src: cpaConfigPath, dst: '/CLIProxyAPI/config.yaml', readonly: false },
      { type: 'bind', src: cpaAuthDir, dst: '/root/.cli-proxy-api', readonly: false },
    ],
    networks: ['synthetic-cpa'],
    ports: [],
  };
  const image = { id: imageID, labels: { 'org.opencontainers.image.version': 'v2.0.0-beta.1', 'org.opencontainers.image.revision': revision } };
  const nextImage = { id: nextImageID, labels: { 'org.opencontainers.image.version': 'v2.0.0-beta.2', 'org.opencontainers.image.revision': '2'.repeat(40) } };
  const cpaLabels = overrides.cpaVersion === null ? {} : { 'org.opencontainers.image.version': overrides.cpaVersion || 'v8.0.55' };
  const managerLabels = overrides.managerLabelVersion ? { 'org.opencontainers.image.version': overrides.managerLabelVersion } : {};
  const state = {
    contextHost: overrides.contextHost || 'unix:///synthetic/docker.sock',
    managerID, cpaID, targetID, imageID,
    networks: ['synthetic-default', 'synthetic-cpa', 'extra-cpa-net'],
    containers: { manager, cpa },
    volumes: {},
    pullable: {
      'synthetic/manager:v2.0.0-beta.1': image,
      'synthetic/manager:v2.0.0-beta.2': nextImage,
    },
    images: {
      [imageID]: image,
      [nextImageID]: nextImage,
      [managerImage]: { id: managerImage, labels: managerLabels },
      [cpaImage]: { id: cpaImage, labels: cpaLabels },
    },
    calls: [],
    exitCodes: overrides.exitCodes || {},
    ambiguous: overrides.ambiguous || false,
    failConnectOnce: overrides.failConnectOnce || false,
    readyAfterPolls: overrides.readyAfterPolls || 0,
    targetUnready: overrides.targetUnready || false,
    forgedHealthOnly: overrides.forgedHealthOnly || false,
    daemonBindPrefix: overrides.daemonBindPrefix || '',
  };
  if (overrides.noCPA) { state.cpaID = ''; delete state.containers.cpa; }
  const stateDir = join(root, 'deployment-state');
  state.stateDirForLockCheck = stateDir;
  const statePath = join(root, 'docker-state.json');
  writeFileSync(statePath, JSON.stringify(state));
  const invoke = (args = [], fresh = false, options = {}) => {
    const env = { ...process.env, PATH: `${executableDir}:${dirname(process.execPath)}:${process.env.PATH}`, CPAMP_SYNTHETIC_DOCKER_STATE: statePath, ...(daemonSock ? { CPAMP_SYNTHETIC_DOCKER_SOCK: daemonSock } : {}) };
    delete env.CPAMP_PUBLIC_ORIGIN;
    if (overrides.envPublicOrigin !== undefined) env.CPAMP_PUBLIC_ORIGIN = overrides.envPublicOrigin;
    if (options.env) {
      for (const [k, v] of Object.entries(options.env)) {
        if (v === undefined) delete env[k];
        else env[k] = v;
      }
    }
    const cli = [];
    if (fresh && !args.includes('--image')) cli.push('--image', 'synthetic/manager:v2.0.0-beta.1');
    if (!options.omitStateDir) cli.push('--state-dir', options.stateDir ?? stateDir);
    cli.push(...args);
    return spawnSync('/bin/bash', [script, ...cli], {
      cwd: options.cwd ?? root,
      encoding: 'utf8',
      timeout: 30_000,
      env,
    });
  };
  const get = () => JSON.parse(readFileSync(statePath, 'utf8'));
  const change = update => { const next = get(); update(next); writeFileSync(statePath, JSON.stringify(next)); };
  return { root, source, cpaRoot, cpaConfigPath, cpaAuthDir, stateDir, invoke, get, change };
}

const isProbeCreate = call => call[0] === 'create' && call.some(a => a.startsWith('dev.cpamp.upgrade.mount-probe='));
const isProbeRm = call => call[0] === 'container' && call[1] === 'rm';
const targetCreates = state => state.calls.filter(call => call[0] === 'create' && !isProbeCreate(call));
const commands = state => state.calls.filter(call => {
  if (['run', 'stop', 'start', 'rm', 'rmi', 'kill'].includes(call[0])) return true;
  if (call[0] === 'volume') return call[1] !== 'inspect';
  if (call[0] === 'create') return !isProbeCreate(call);
  if (call[0] === 'container') return !isProbeRm(call);
  return false;
});
const runs = state => state.calls.filter(call => call[0] === 'run');
const succeeded = result => assert.equal(result.status, 0, `${result.stdout}\n${result.stderr}`);
const manifestMap = stateDir => Object.fromEntries(
  readFileSync(join(stateDir, 'manifest'), 'utf8').trimEnd().split('\n').map(line => {
    const tab = line.indexOf('\t');
    return [line.slice(0, tab), line.slice(tab + 1)];
  }),
);

test('Managed preparation succeeds with bootstrap UI 200 while /health is 503, preserves source permissions, and outputs all three volumes', t => {
  const f = fixture(t, { bind: true });
  const beforeEntries = readdirSync(f.source).sort();
  const beforeSourceMode = statSync(f.source).mode & 0o777;
  const beforeKeyMode = statSync(join(f.source, 'data.key')).mode & 0o777;
  const result = f.invoke(['--confirm'], true);
  succeeded(result);

  const state = f.get();
  assert.deepEqual(state.calls.filter(c => c[0] === 'stop').map(c => c.at(-1)), [managerID, cpaID]);
  assert.equal(state.containers.manager.running, false);
  assert.equal(state.containers.cpa.running, false);
  assert.equal(state.containers.target.running, true);
  const target = state.containers.target;
  assert.equal(target.env.CPAMP_UPGRADE_WORKER_SOCKET, '/run/cpamp-upgrade/worker.sock');
  assert.equal(target.env.CPAMP_UPGRADE_SOURCE_ROOT, '/upgrade/source');
  assert.equal(target.env.CPAMP_LEGACY_ROOT, undefined);
  assert.equal(target.env.CPAMP_UPGRADE_CPA_VERSION, 'v8.0.55');
  assert.deepEqual(
    target.mounts.map(m => [m.dst, m.src, m.readonly]),
    [
      ['/data', `${operation}-data`, false],
      ['/runtime', `${operation}-runtime`, false],
      ['/upgrade/backup', `${operation}-backup`, true],
      ['/upgrade/source', f.source, true],
      ['/upgrade/cpa/config.yaml', f.cpaConfigPath, true],
      ['/upgrade/auths', f.cpaAuthDir, true],
    ],
  );
  assert.deepEqual(readdirSync(f.source).sort(), beforeEntries);
  assert.equal(statSync(f.source).mode & 0o777, beforeSourceMode);
  assert.equal(statSync(join(f.source, 'data.key')).mode & 0o777, beforeKeyMode);
  assert.equal(readFileSync(join(f.source, 'usage.sqlite'), 'utf8'), 'synthetic metadata harness; not a database');
  assert.ok(runs(state).every(c => !c.includes('--source-version') && !c.some(a => a.startsWith('--acknowledge'))));
  assert.ok(state.calls.findIndex(c => c[0] === 'pull') < state.calls.findIndex(c => c[0] === 'stop'));
  const upgradeURL = new URL(result.stdout.match(/^Upgrade UI: (\S+)$/m)[1]);
  assert.equal(upgradeURL.origin, 'http://localhost:38684');
  assert.equal(upgradeURL.pathname, '/.well-known/cpamp-bootstrap/ui');
  assert.match(result.stdout, new RegExp(`${operation}-data`));
  assert.match(result.stdout, new RegExp(`${operation}-backup`));
  assert.match(result.stdout, new RegExp(`${operation}-runtime`));
  const manifest = readFileSync(join(f.stateDir, 'manifest'), 'utf8');
  assert.equal(manifestMap(f.stateDir).target_id, targetID);
  assert.doesNotMatch(manifest, /synthetic-not-a-credential|synthetic-cpa-secret|synthetic-admin-secret|CPA_MANAGEMENT_KEY|Config\.Env/);
});

test('Readiness polls through startup delay, fails closed without auto-rollback when unready, and switches output on activated resume', t => {
  const delayed = fixture(t, { readyAfterPolls: 2 });
  succeeded(delayed.invoke(['--confirm'], true));
  assert.equal(delayed.get().containers.target.running, true);

  // Simulate resume after UI verify + activate retired /.well-known/cpamp-bootstrap/ui and committed /cpamp-admin.
  delayed.change(s => {
    s.operationStage = 'complete';
    s.committedControlPath = '/cpamp-admin';
    s.containers.target.running = false;
  });
  const activatedResume = delayed.invoke();
  succeeded(activatedResume);
  assert.equal(activatedResume.stdout.includes('Upgrade UI:'), false);
  assert.match(activatedResume.stdout, /^Management console: http:\/\/localhost:38684\/cpamp-admin$/m);

  // Fallback through upgrade status --json when internal fresh-routing control read is unavailable.
  delayed.change(s => { s.hideFreshControlRoute = true; });
  const statusFallbackResume = delayed.invoke();
  succeeded(statusFallbackResume);
  assert.equal(statusFallbackResume.stdout.includes('Upgrade UI:'), false);
  assert.match(statusFallbackResume.stdout, /^Management console: http:\/\/localhost:38684\/cpamp-admin$/m);

  // Delayed canonical route publication on port 18317 is polled until active before reporting completion.
  delayed.change(s => {
    s.hideFreshControlRoute = false;
    s.canonicalReadyAfterPolls = 2;
    s.canonicalRoutePolls = 0;
    s.identityRoutePolls = 0;
  });
  const delayedCanonical = delayed.invoke();
  succeeded(delayedCanonical);
  assert.match(delayedCanonical.stdout, /^Management console: http:\/\/localhost:38684\/cpamp-admin$/m);

  // Stage complete without an active verified canonical route fails closed rather than claiming readiness.
  delayed.change(s => { s.canonicalReadyAfterPolls = 0; s.committedControlPath = ''; });
  assert.notEqual(delayed.invoke().status, 0);
  delayed.change(s => { s.committedControlPath = '/cpamp-admin'; s.identityProbeFails = true; });
  assert.notEqual(delayed.invoke().status, 0);
  delayed.change(s => { s.identityProbeFails = false; s.freshRouteTarget = '/mismatched-admin'; });
  assert.notEqual(delayed.invoke().status, 0);
  // Persistent unready target fails closed without starting original services.
  const unready = fixture(t, { targetUnready: true });
  assert.notEqual(unready.invoke(['--confirm'], true).status, 0);
  assert.equal(unready.get().containers.manager.running, false);
  assert.equal(unready.get().containers.cpa.running, false);
  assert.equal(unready.get().calls.some(c => c[0] === 'start' && c.at(-1) === managerID), false);

  // /health 200 without stage: complete in upgrade status is refused.
  const forgedHealth = fixture(t, { forgedHealthOnly: true });
  assert.notEqual(forgedHealth.invoke(['--confirm'], true).status, 0);
});

test('Validated --public-origin and CPAMP_PUBLIC_ORIGIN bind target env, manifest and URLs; malformed origins fail before effects', t => {
  const invalidOrigins = [
    'ftp://cpamp.example.com',
    'https://user:pass@cpamp.example.com',
    'https://cpamp.example.com/console',
    'https://cpamp.example.com//',
    'https://cpamp.example.com?q=1',
    'https://cpamp.example.com#frag',
    'http://0.0.0.0:18317',
    'https://[::]:8443',
    'https://::1:8443',
    'https://[2001:db8::1::2]:8443',
    'http://999.0.0.1:8080',
    'http://127.0.1:8080',
    'https://cpamp.example.com:0',
    'https://cpamp.example.com:70000',
    'https://-bad.example.com',
    'https://bad.999',
  ];
  for (const invalid of invalidOrigins) {
    const f = fixture(t);
    assert.notEqual(f.invoke(['--public-origin', invalid, '--confirm'], true).status, 0, `Expected rejection for ${invalid}`);
    assert.equal(commands(f.get()).length, 0);
    assert.equal(existsSync(f.stateDir), false);
  }

  const envConflict = fixture(t, { envPublicOrigin: 'https://one.example.com' });
  assert.notEqual(envConflict.invoke(['--public-origin', 'https://two.example.com', '--confirm'], true).status, 0);
  assert.equal(commands(envConflict.get()).length, 0);

  const remoteTls = fixture(t, { contextHost: 'tcp://docker.internal:2376' });
  const first = remoteTls.invoke(['--public-origin', 'HTTPS://CPAMP.Example.COM:443/', '--public-host', 'ignored.internal', '--confirm'], true);
  succeeded(first);
  assert.equal(remoteTls.get().containers.target.env.CPAMP_PUBLIC_ORIGIN, 'https://cpamp.example.com');
  assert.equal(manifestMap(remoteTls.stateDir).public_origin, 'https://cpamp.example.com');
  assert.match(first.stdout, /https:\/\/cpamp\.example\.com\//);

  const resumed = remoteTls.invoke();
  succeeded(resumed);
  assert.match(resumed.stdout, /https:\/\/cpamp\.example\.com\//);

  remoteTls.change(s => { s.operationStage = 'complete'; s.committedControlPath = '/console'; });
  const completed = remoteTls.invoke();
  succeeded(completed);
  assert.match(completed.stdout, /^Management console: https:\/\/cpamp\.example\.com\/console$/m);

  const ipv6Origin = fixture(t, { envPublicOrigin: 'https://[2001:DB8::1]:8443/' });
  const ipv6Out = ipv6Origin.invoke(['--confirm'], true);
  succeeded(ipv6Out);
  assert.equal(ipv6Origin.get().containers.target.env.CPAMP_PUBLIC_ORIGIN, 'https://[2001:db8::1]:8443');
  assert.match(ipv6Out.stdout, /https:\/\/\[2001:db8::1\]:8443\//);
});

test('CPA release gate enforces exact v8 SemVer floor and honest image discovery while allowing unknown Manager patch', t => {
  for (const rejected of ['v7.1.18', 'v9.0.0', '10.0.0', 'v8.0.0-rc.1', '8.0.0-alpha.1', 'v8.0.55.1']) {
    const f = fixture(t, { cpaVersion: null, cpaImageRef: 'synthetic/cpa:latest' });
    assert.notEqual(f.invoke(['--cpa-version', rejected, '--source-version', 'v1.14.9', '--confirm'], true).status, 0, `Expected CPA gate rejection for ${rejected}`);
    assert.equal(commands(f.get()).length, 0);
    assert.equal(f.get().containers.manager.running, true);
    assert.equal(f.get().containers.cpa.running, true);
  }

  // Explicit --cpa-version takes precedence over image label.
  const explicitWins = fixture(t, { cpaVersion: 'v8.0.55' });
  assert.notEqual(explicitWins.invoke(['--cpa-version', 'v7.1.18', '--confirm'], true).status, 0);
  assert.equal(commands(explicitWins.get()).length, 0);

  for (const supported of ['v8.0.0', '8.0.0+20261101', 'v8.0.1-rc.1', 'V8.1.0-beta.2+sha.123']) {
    const f = fixture(t, { cpaVersion: null, cpaImageRef: 'synthetic/cpa:latest' });
    succeeded(f.invoke(['--cpa-version', supported, '--confirm'], true));
    assert.equal(f.get().containers.target.env.CPAMP_UPGRADE_CPA_VERSION, supported);
  }

  // Registry port without tag, digest-pinned ref, moving latest, and 4-segment tag do not fabricate a release.
  for (const untrustedRef of [
    'registry.internal:8317/cli-proxy-api',
    'eceasy/cli-proxy-api:v8.0.55@sha256:' + '2'.repeat(64),
    'eceasy/cli-proxy-api@sha256:' + '2'.repeat(64),
    'eceasy/cli-proxy-api:latest',
    'eceasy/cli-proxy-api:v8.0.55.1',
  ]) {
    const f = fixture(t, { cpaVersion: null, cpaImageRef: untrustedRef });
    assert.notEqual(f.invoke(['--confirm'], true).status, 0, `Expected unknown CPA refusal for ${untrustedRef}`);
    assert.equal(commands(f.get()).length, 0);
    succeeded(f.invoke(['--cpa-version', 'v8.0.55', '--confirm'], true));
  }
});

test('Manager OCI version auto-discovery normalizes exact 1.14.x tags and keeps prerelease or build tags undeclared', t => {
  for (const [rawLabel, expectedNormalized, fullRun] of [
    ['V1.14.10', 'v1.14.10', true],
    ['1.14.10', 'v1.14.10', false],
    ['v1.14.10', 'v1.14.10', false],
    ['v1.14.0', 'v1.14.0', false],
  ]) {
    const labeledManager = fixture(t, {
      managerLabelVersion: rawLabel,
      ...(fullRun ? {} : { exitCodes: { preflight: 1 } }),
    });
    const labeledOut = labeledManager.invoke(['--confirm'], true);
    if (fullRun) {
      succeeded(labeledOut);
      assert.equal(manifestMap(labeledManager.stateDir).source_version, expectedNormalized);
    } else {
      assert.notEqual(labeledOut.status, 0);
    }
    assert.match(labeledOut.stdout, new RegExp(`Manager release: ${expectedNormalized.replaceAll('.', '\\.')}$`, 'm'));
    const labeledRuns = runs(labeledManager.get());
    assert.equal(labeledRuns.length, fullRun ? 3 : 1);
    assert.ok(labeledRuns.every(c => {
      const idx = c.indexOf('--source-version');
      return idx >= 0 && c[idx + 1] === expectedNormalized;
    }));
  }

  for (const [unqualifyingLabel, fullRun] of [
    ['v1.14.10-rc1', true],
    ['dev', false],
    ['v1.14.10+custom', false],
    ['v1.14.01', false],
    ['v1.14.1000', false],
    ['V1.13.9', false],
  ]) {
    const unlabeledManager = fixture(t, {
      managerLabelVersion: unqualifyingLabel,
      ...(fullRun ? {} : { exitCodes: { preflight: 1 } }),
    });
    const unlabeledOut = unlabeledManager.invoke(['--confirm'], true);
    if (fullRun) {
      succeeded(unlabeledOut);
      assert.equal(manifestMap(unlabeledManager.stateDir).source_version, '');
    } else {
      assert.notEqual(unlabeledOut.status, 0);
    }
    assert.match(unlabeledOut.stdout, /Manager release: unknown \(engine must qualify source family\)$/m);
    const unlabeledRuns = runs(unlabeledManager.get());
    assert.equal(unlabeledRuns.length, fullRun ? 3 : 1);
    assert.ok(unlabeledRuns.every(c => !c.includes('--source-version')));
  }

  // Explicit CLI --source-version is preserved and forwarded to the engine even when OCI label differs.
  const explicitValid = fixture(t, { managerLabelVersion: 'v1.14.10' });
  succeeded(explicitValid.invoke(['--source-version', 'v1.14.4', '--confirm'], true));
  assert.equal(manifestMap(explicitValid.stateDir).source_version, 'v1.14.4');
  assert.ok(runs(explicitValid.get()).every(c => c[c.indexOf('--source-version') + 1] === 'v1.14.4'));

  const explicitUnsupported = fixture(t, { managerLabelVersion: 'v1.14.10' });
  assert.notEqual(explicitUnsupported.invoke(['--source-version', 'v1.13.9', '--confirm'], true).status, 0);
  const unsupportedRuns = runs(explicitUnsupported.get());
  assert.equal(unsupportedRuns.length, 1);
  assert.equal(unsupportedRuns[0][unsupportedRuns[0].indexOf('--source-version') + 1], 'v1.13.9');
});

test('Confirmation, unique Manager discovery, and config-driven or external-secret Manager paths fail closed before effects', t => {
  const unconfirmed = fixture(t);
  assert.notEqual(unconfirmed.invoke([], true).status, 0);
  assert.equal(commands(unconfirmed.get()).length, 0);
  assert.equal(existsSync(unconfirmed.stateDir), false);

  const ambiguous = fixture(t, { ambiguous: true });
  assert.notEqual(ambiguous.invoke(['--confirm'], true).status, 0);
  assert.equal(commands(ambiguous.get()).length, 0);

  const configEnv = fixture(t, { managerEnv: { CPA_MANAGER_CONFIG: '/etc/cpamp/config.json' } });
  assert.notEqual(configEnv.invoke(['--confirm'], true).status, 0);
  assert.equal(commands(configEnv.get()).length, 0);

  const secretKeyEnv = fixture(t, { managerEnv: { CPA_MANAGER_DATA_KEY: 'raw-key-secret' } });
  const secretOut = secretKeyEnv.invoke(['--confirm'], true);
  assert.notEqual(secretOut.status, 0);
  assert.equal(secretOut.stdout.includes('raw-key-secret'), false);
  assert.equal(secretOut.stderr.includes('raw-key-secret'), false);
  assert.equal(commands(secretKeyEnv.get()).length, 0);

  const mountedConfig = fixture(t);
  mountedConfig.change(s => {
    s.containers.manager.mounts.push({ type: 'bind', name: '', src: join(mountedConfig.root, 'config.json'), dst: '/app/config.json', readonly: true });
  });
  assert.notEqual(mountedConfig.invoke(['--confirm'], true).status, 0);
  assert.equal(commands(mountedConfig.get()).length, 0);

  // Configured custom data root via USAGE_DATA_DIR and via documented --source-mount both select exact mount.
  const customEnv = fixture(t, { bind: true });
  customEnv.change(s => { s.containers.manager.env.USAGE_DATA_DIR = '/custom-data'; s.containers.manager.mounts[0].dst = '/custom-data'; });
  succeeded(customEnv.invoke(['--confirm'], true));
  assert.equal(customEnv.get().containers.target.mounts.find(m => m.dst === '/upgrade/source').src, customEnv.source);

  const customFlag = fixture(t, { bind: true });
  customFlag.change(s => { s.containers.manager.mounts[0].dst = '/srv/cpamp-data'; });
  succeeded(customFlag.invoke(['--source-mount', '/srv/cpamp-data', '--confirm'], true));
  assert.equal(customFlag.get().containers.target.mounts.find(m => m.dst === '/upgrade/source').src, customFlag.source);
});

test('Source and CPA bind paths reject overlapping default or explicit state directories across symlinks, dot segments and resume', t => {
  const f = fixture(t, { bind: true });
  const beforeSourceEntries = readdirSync(f.source).sort();
  const beforeAuthEntries = readdirSync(f.cpaAuthDir).sort();

  assert.notEqual(f.invoke(['--confirm'], true, { cwd: f.source, omitStateDir: true }).status, 0);
  assert.equal(commands(f.get()).length, 0);
  assert.deepEqual(readdirSync(f.source).sort(), beforeSourceEntries);

  assert.notEqual(f.invoke(['--confirm'], true, { cwd: f.cpaAuthDir, omitStateDir: true }).status, 0);
  assert.equal(commands(f.get()).length, 0);
  assert.deepEqual(readdirSync(f.cpaAuthDir).sort(), beforeAuthEntries);

  const sourceAlias = join(f.root, 'source-alias');
  symlinkSync(f.source, sourceAlias);
  const overlappingDirs = [
    f.source,
    join(f.source, 'nested', 'state'),
    join(f.source, '..', 'source with spaces; literal-dollar-$value', 'state'),
    join(sourceAlias, 'nested-state'),
    f.root,
    f.cpaConfigPath,
    join(f.cpaAuthDir, 'nested-state'),
  ];
  for (const badDir of overlappingDirs) {
    assert.notEqual(f.invoke(['--confirm'], true, { stateDir: badDir }).status, 0, `Expected overlap refusal for ${badDir}`);
    assert.equal(commands(f.get()).length, 0);
  }
  assert.deepEqual(readdirSync(f.source).sort(), beforeSourceEntries);

  succeeded(f.invoke(['--confirm'], true));
  const relocated = join(f.source, '.relocated-state');
  mkdirSync(relocated, { mode: 0o700 });
  writeFileSync(join(relocated, 'manifest'), readFileSync(join(f.stateDir, 'manifest')));
  f.change(s => { s.calls = []; });
  assert.notEqual(f.invoke([], false, { stateDir: relocated }).status, 0);
  assert.equal(commands(f.get()).length, 0);
});

test('Strict manifest validation and CLI conflict checks reject tampered state and conflicting resume/rollback flags', t => {
  const f = fixture(t, { bind: true });
  succeeded(f.invoke(['--public-origin', 'https://cpamp.example.com', '--confirm'], true));
  const originalManifest = readFileSync(join(f.stateDir, 'manifest'), 'utf8');
  const replaceField = (key, val) => {
    const lines = originalManifest.trimEnd().split('\n').map(line => line.startsWith(`${key}\t`) ? `${key}\t${val}` : line);
    writeFileSync(join(f.stateDir, 'manifest'), `${lines.join('\n')}\n`, { mode: 0o600 });
  };

  const tamperedCases = [
    ['cpa_config_source', `${f.cpaConfigPath},rw`],
    ['cpa_config_type', 'volume'],
    ['cpa_auth_source', `${f.cpaAuthDir},rw`],
    ['cpa_auth_type', 'tmpfs'],
    ['manager_running', '1'],
    ['cpa_running', 'yes'],
    ['target_id', ''],
    ['cpa_version', ''],
    ['public_origin', 'https://cpamp.example.com/subpath'],
  ];
  for (const [key, val] of tamperedCases) {
    replaceField(key, val);
    f.change(s => { s.calls = []; });
    assert.notEqual(f.invoke().status, 0, `Expected manifest validation failure for ${key}=${val}`);
    assert.equal(commands(f.get()).length, 0);
  }
  writeFileSync(join(f.stateDir, 'manifest'), originalManifest, { mode: 0o600 });

  // Matching resolved identities succeed on resume.
  succeeded(f.invoke(['--manager', 'synthetic-manager', '--cpa', 'synthetic-cpa', '--image', imageID, '--mode', 'managed', '--public-origin', 'https://cpamp.example.com']));

  // Conflicting CLI flags and environment origin fail closed on both resume and rollback.
  const conflicts = [
    ['--mode', 'external'],
    ['--image', nextImageID],
    ['--source-mount', '/other'],
    ['--source-version', 'v1.14.9'],
    ['--cpa-version', 'v8.0.54'],
    ['--network', 'extra-cpa-net'],
    ['--public-origin', 'https://other.example.com'],
  ];
  for (const conflictArgs of conflicts) {
    f.change(s => { s.calls = []; });
    assert.notEqual(f.invoke(conflictArgs).status, 0, `Expected resume conflict for ${conflictArgs.join(' ')}`);
    assert.notEqual(f.invoke([...conflictArgs, '--rollback', '--confirm']).status, 0, `Expected rollback conflict for ${conflictArgs.join(' ')}`);
    assert.equal(commands(f.get()).length, 0);
  }
  assert.notEqual(f.invoke([], false, { env: { CPAMP_PUBLIC_ORIGIN: 'https://other.example.com' } }).status, 0);

  // Pre-existing 33-key manifest without public_origin is accepted ONLY when deployed target has empty CPAMP_PUBLIC_ORIGIN.
  const legacyLines = originalManifest.trimEnd().split('\n').filter(l => !l.startsWith('public_origin\t'));
  writeFileSync(join(f.stateDir, 'manifest'), `${legacyLines.join('\n')}\n`, { mode: 0o600 });
  assert.notEqual(f.invoke().status, 0);
  f.change(s => { delete s.containers.target.env.CPAMP_PUBLIC_ORIGIN; });
  succeeded(f.invoke());
  assert.notEqual(f.invoke(['--public-origin', 'https://cpamp.example.com']).status, 0);
});

test('Target identity enforces RW/RO mount access modes, /legacy absence, canonical env and published port before stop/start', t => {
  const f = fixture(t, { bind: true });
  succeeded(f.invoke(['--confirm'], true));
  const baseline = JSON.stringify(f.get().containers.target);
  const restore = () => f.change(s => { s.containers.target = JSON.parse(baseline); s.containers.target.running = false; s.calls = []; });

  const mutations = [
    ['data RO', tObj => { tObj.mounts.find(m => m.dst === '/data').readonly = true; }],
    ['runtime RO', tObj => { tObj.mounts.find(m => m.dst === '/runtime').readonly = true; }],
    ['source RW', tObj => { tObj.mounts.find(m => m.dst === '/upgrade/source').readonly = false; }],
    ['backup RW', tObj => { tObj.mounts.find(m => m.dst === '/upgrade/backup').readonly = false; }],
    ['cpa config RW', tObj => { tObj.mounts.find(m => m.dst === '/upgrade/cpa/config.yaml').readonly = false; }],
    ['cpa auth RW', tObj => { tObj.mounts.find(m => m.dst === '/upgrade/auths').readonly = false; }],
    ['/legacy mount', tObj => { tObj.mounts.push({ type: 'bind', src: f.source, dst: '/legacy', readonly: true }); }],
    ['CPAMP_LEGACY_ROOT env', tObj => { tObj.env.CPAMP_LEGACY_ROOT = '/upgrade/source'; }],
    ['worker socket env', tObj => { tObj.env.CPAMP_UPGRADE_WORKER_SOCKET = '/tmp/other.sock'; }],
    ['source root env', tObj => { tObj.env.CPAMP_UPGRADE_SOURCE_ROOT = '/data'; }],
    ['USAGE_DATA_DIR env', tObj => { tObj.env.USAGE_DATA_DIR = '/upgrade/source'; }],
    ['published port drift', tObj => { tObj.ports = [{ ip: '0.0.0.0', port: '39999', proto: '18317/tcp' }]; }],
    ['privileged target', tObj => { tObj.privileged = true; }],
    ['replaced target ID', tObj => { tObj.id = '9'.repeat(64); }],
  ];
  for (const [label, mutate] of mutations) {
    restore();
    f.change(s => mutate(s.containers.target));
    assert.notEqual(f.invoke().status, 0, `Expected target identity refusal on resume for ${label}`);
    assert.notEqual(f.invoke(['--rollback', '--confirm']).status, 0, `Expected target identity refusal on rollback for ${label}`);
    assert.equal(commands(f.get()).length, 0, `Expected zero mutations for ${label}`);
  }
});

test('External mode, local digest image, stopped Manager, interrupted create/reconcile/preflight, foreign volume and rollback boundaries', t => {
  // External mode retains CPA running and attaches all original Docker networks.
  const ext = fixture(t);
  succeeded(ext.invoke(['--confirm', '--mode', 'external'], true));
  assert.deepEqual(ext.get().calls.filter(c => c[0] === 'stop').map(c => c.at(-1)), [managerID]);
  assert.equal(ext.get().containers.cpa.running, true);
  assert.deepEqual(ext.get().containers.target.networks, ['synthetic-default', 'synthetic-cpa']);
  assert.equal(ext.get().containers.target.env.CPAMP_UPGRADE_CPA_ROOT, undefined);

  // External deployment without associated CPA sets no fabricated CPA version.
  const noCpa = fixture(t, { noCPA: true });
  succeeded(noCpa.invoke(['--confirm'], true));
  assert.equal(noCpa.get().containers.target.env.CPAMP_UPGRADE_CPA_VERSION, undefined);
  assert.equal(manifestMap(noCpa.stateDir).mode, 'external');

  // Already-local immutable image ID needs no pull.
  const localDigest = fixture(t);
  succeeded(localDigest.invoke(['--confirm', '--image', imageID], true));
  assert.equal(localDigest.get().calls.some(c => c[0] === 'pull'), false);
  assert.equal(localDigest.get().containers.target.image, imageID);

  // Stopped Manager preserves configured publication and stays stopped across rollback.
  const stoppedMgr = fixture(t);
  stoppedMgr.change(s => { s.containers.manager.running = false; s.containers.manager.ports[0].ip = ''; });
  succeeded(stoppedMgr.invoke(['--confirm'], true));
  succeeded(stoppedMgr.invoke(['--rollback', '--confirm']));
  assert.equal(stoppedMgr.get().containers.manager.running, false);

  // Foreign pre-existing volume is refused before stopping original containers.
  const foreignVol = fixture(t);
  foreignVol.change(s => { s.volumes[`${operation}-data`] = { operation: 'unrelated-synthetic-operation' }; });
  assert.notEqual(foreignVol.invoke(['--confirm'], true).status, 0);
  assert.equal(foreignVol.get().containers.manager.running, true);
  assert.equal(runs(foreignVol.get()).length, 0);

  // Failed preflight preserves stopped originals and resumes the same operation.
  const preflightFail = fixture(t, { exitCodes: { preflight: 1 } });
  assert.notEqual(preflightFail.invoke(['--confirm'], true).status, 0);
  assert.equal(manifestMap(preflightFail.stateDir).phase, 'stopped');
  assert.equal(preflightFail.get().containers.manager.running, false);
  assert.equal(preflightFail.get().calls.some(c => c[0] === 'start'), false);
  preflightFail.change(s => { s.exitCodes = {}; });
  succeeded(preflightFail.invoke());
  assert.equal(targetCreates(preflightFail.get()).length, 1);

  // Reconcile exit 2 holds deployment; resume re-verifies idempotently without repeating copy-in materialization; reconcile exit 1 opens UI without --acknowledge.
  const reconcileFlow = fixture(t, { exitCodes: { reconcile: 2 } });
  assert.notEqual(reconcileFlow.invoke(['--confirm'], true).status, 0);
  assert.equal(manifestMap(reconcileFlow.stateDir).phase, 'copied-in');
  assert.equal(reconcileFlow.get().copyInMaterializations, 1);
  assert.equal(targetCreates(reconcileFlow.get()).length, 0);
  assert.equal(reconcileFlow.get().calls.some(c => c[0] === 'start'), false);
  reconcileFlow.change(s => { s.exitCodes = { reconcile: 1 }; });
  succeeded(reconcileFlow.invoke());
  assert.equal(reconcileFlow.get().copyInMaterializations, 1);
  assert.equal(reconcileFlow.get().containers.target.running, true);
  assert.ok(runs(reconcileFlow.get()).every(c => !c.some(a => a.startsWith('--acknowledge'))));

  // Deployed resume never repeats offline run/reconcile; changed source port prevents resume; interrupted network connect captures target_id on create.
  const interruptedNet = fixture(t, { failConnectOnce: true });
  assert.notEqual(interruptedNet.invoke(['--confirm'], true).status, 0);
  assert.equal(manifestMap(interruptedNet.stateDir).target_id, targetID);
  assert.equal(manifestMap(interruptedNet.stateDir).phase, 'deployed');
  const runsBeforeResume = runs(interruptedNet.get()).length;
  succeeded(interruptedNet.invoke());
  assert.equal(runs(interruptedNet.get()).length, runsBeforeResume);
  assert.deepEqual(interruptedNet.get().containers.target.networks, ['synthetic-default', 'synthetic-cpa']);

  interruptedNet.change(s => { s.containers.manager.ports[0].port = '38685'; s.calls = []; });
  assert.notEqual(interruptedNet.invoke().status, 0);
  assert.equal(commands(interruptedNet.get()).length, 0);
  interruptedNet.change(s => { s.containers.manager.ports[0].port = '38684'; });

  interruptedNet.change(s => { s.containers.manager.mounts[0].src = 'different-synthetic-source'; s.calls = []; });
  assert.notEqual(interruptedNet.invoke(['--rollback', '--confirm']).status, 0);
  assert.equal(commands(interruptedNet.get()).length, 0);
  interruptedNet.change(s => { s.containers.manager.mounts[0].src = 'synthetic-manager-data'; });

  const rolledBack = interruptedNet.invoke(['--rollback', '--confirm']);
  succeeded(rolledBack);
  assert.equal(interruptedNet.get().containers.target.running, false);
  assert.equal(interruptedNet.get().containers.manager.running, true);
  assert.equal(interruptedNet.get().containers.cpa.running, true);
  assert.equal(Object.keys(interruptedNet.get().volumes).length, 3);
  assert.match(rolledBack.stdout, new RegExp(`${operation}-data`));
  assert.match(rolledBack.stdout, new RegExp(`${operation}-backup`));
  assert.match(rolledBack.stdout, new RegExp(`${operation}-runtime`));
  assert.notEqual(interruptedNet.invoke().status, 0);
});

test('Daemon bind normalization (/host_mnt on Docker Desktop) binds exact probe-normalized target paths without prefix-stripping shortcuts', t => {
  // Named-volume Manager + bind-mounted CPA under /host_mnt daemon normalization (exact Docker Desktop baseline failure shape).
  const namedDesktop = fixture(t, { daemonBindPrefix: '/host_mnt' });
  succeeded(namedDesktop.invoke(['--confirm'], true));
  assert.equal(namedDesktop.get().containers.mountProbe, undefined);
  assert.equal(namedDesktop.get().containers.target.running, true);
  const namedManifest = manifestMap(namedDesktop.stateDir);
  assert.equal(namedManifest.cpa_config_source, namedDesktop.cpaConfigPath);
  assert.equal(namedManifest.target_cpa_config_source, `/host_mnt${namedDesktop.cpaConfigPath}`);
  assert.equal(namedManifest.target_cpa_auth_source, `/host_mnt${namedDesktop.cpaAuthDir}`);
  succeeded(namedDesktop.invoke());

  // Tampering target mount to even the un-prefixed host path or another /host_mnt path is strictly rejected (no blind /host_mnt stripping).
  namedDesktop.change(s => {
    s.containers.target.running = false;
    s.containers.target.mounts.find(m => m.dst === '/upgrade/cpa/config.yaml').src = namedDesktop.cpaConfigPath;
    s.calls = [];
  });
  assert.notEqual(namedDesktop.invoke().status, 0);
  assert.equal(commands(namedDesktop.get()).length, 0);
  namedDesktop.change(s => {
    s.containers.target.mounts.find(m => m.dst === '/upgrade/cpa/config.yaml').src = `/host_mnt${namedDesktop.cpaConfigPath}`;
  });
  succeeded(namedDesktop.invoke(['--rollback', '--confirm']));

  // Bind-mounted Manager + bind-mounted CPA under /host_mnt daemon normalization, plus resume from legacy 33-key reconciled manifest.
  const bindDesktop = fixture(t, { bind: true, daemonBindPrefix: '/host_mnt' });
  succeeded(bindDesktop.invoke(['--confirm'], true));
  assert.equal(manifestMap(bindDesktop.stateDir).source_source, bindDesktop.source);
  assert.equal(manifestMap(bindDesktop.stateDir).target_source_source, `/host_mnt${bindDesktop.source}`);

  // Simulate interrupted pre-fix 33-key reconciled manifest where target was created with /host_mnt mounts before phase=deployed.
  const legacyLines = readFileSync(join(bindDesktop.stateDir, 'manifest'), 'utf8')
    .trimEnd()
    .split('\n')
    .filter(l => !l.startsWith('public_origin\t') && !l.startsWith('cpa_auth_destination\t') && !l.startsWith('target_source_source\t') && !l.startsWith('target_cpa_config_source\t') && !l.startsWith('target_cpa_auth_source\t'))
    .map(l => l.startsWith('phase\t') ? 'phase\treconciled' : (l.startsWith('target_id\t') ? 'target_id\t' : l));
  writeFileSync(join(bindDesktop.stateDir, 'manifest'), `${legacyLines.join('\n')}\n`, { mode: 0o600 });
  // Legacy resume must strictly verify existing operation volumes rather than auto-creating or adopting missing/foreign volumes.
  bindDesktop.change(s => {
    s.containers.target.running = false;
    delete s.volumes[`${operation}-runtime`];
    s.calls = [];
  });
  assert.notEqual(bindDesktop.invoke().status, 0);
  assert.equal(commands(bindDesktop.get()).length, 0);
  assert.equal(bindDesktop.get().volumes[`${operation}-runtime`], undefined);

  bindDesktop.change(s => {
    s.volumes[`${operation}-runtime`] = { operation: 'foreign-operation' };
    s.calls = [];
  });
  assert.notEqual(bindDesktop.invoke().status, 0);
  assert.equal(commands(bindDesktop.get()).length, 0);

  bindDesktop.change(s => {
    s.volumes[`${operation}-runtime`] = { operation };
  });
  succeeded(bindDesktop.invoke());
  assert.equal(manifestMap(bindDesktop.stateDir).phase, 'deployed');
  assert.equal(manifestMap(bindDesktop.stateDir).target_id, targetID);
  assert.equal(manifestMap(bindDesktop.stateDir).target_cpa_config_source, `/host_mnt${bindDesktop.cpaConfigPath}`);

  // Pre-existing 33-key phase=deployed manifest on Docker Desktop succeeds across both resume and rollback via exact topology probe + conservative overlap.
  const deployedLegacyLines = readFileSync(join(bindDesktop.stateDir, 'manifest'), 'utf8')
    .trimEnd()
    .split('\n')
    .filter(l => !l.startsWith('public_origin\t') && !l.startsWith('cpa_auth_destination\t') && !l.startsWith('target_source_source\t') && !l.startsWith('target_cpa_config_source\t') && !l.startsWith('target_cpa_auth_source\t'));
  writeFileSync(join(bindDesktop.stateDir, 'manifest'), `${deployedLegacyLines.join('\n')}\n`, { mode: 0o600 });
  // Relocating that legacy deployed manifest under the local host source path is conservatively rejected before mutations.
  const overlappingLegacyDir = join(bindDesktop.source, 'legacy-desktop-overlap');
  mkdirSync(overlappingLegacyDir, { mode: 0o700 });
  writeFileSync(join(overlappingLegacyDir, 'manifest'), `${deployedLegacyLines.join('\n')}\n`, { mode: 0o600 });
  bindDesktop.change(s => { s.calls = []; });
  assert.notEqual(bindDesktop.invoke([], false, { stateDir: overlappingLegacyDir }).status, 0);
  assert.equal(commands(bindDesktop.get()).length, 0);
  rmSync(overlappingLegacyDir, { recursive: true, force: true });

  succeeded(bindDesktop.invoke());
  assert.equal(manifestMap(bindDesktop.stateDir).target_source_source, `/host_mnt${bindDesktop.source}`);
  assert.equal(manifestMap(bindDesktop.stateDir).target_cpa_config_source, `/host_mnt${bindDesktop.cpaConfigPath}`);
  // Re-write 33-key deployed manifest and prove --rollback also probes exact normalized topology before stopping v2.
  writeFileSync(join(bindDesktop.stateDir, 'manifest'), `${deployedLegacyLines.join('\n')}\n`, { mode: 0o600 });
  succeeded(bindDesktop.invoke(['--rollback', '--confirm']));
  assert.equal(bindDesktop.get().containers.target.running, false);
  assert.equal(bindDesktop.get().containers.manager.running, true);
});

test('State directory isolation rejects local host paths when mixed volume+bind sources already carry /host_mnt prefixes', t => {
  const mixed = fixture(t, { daemonBindPrefix: '/host_mnt' });
  mixed.change(s => {
    s.containers.manager.mounts = [
      { type: 'bind', src: `/host_mnt${mixed.source}`, dst: '/data', readonly: false },
      { type: 'volume', src: 'legacy-aux-volume', dst: '/aux', readonly: true },
    ];
    s.containers.cpa.mounts = [
      { type: 'bind', src: `/host_mnt${mixed.cpaConfigPath}`, dst: '/CLIProxyAPI/config.yaml', readonly: false },
      { type: 'bind', src: `/host_mnt${mixed.cpaAuthDir}`, dst: '/root/.cli-proxy-api', readonly: false },
    ];
  });
  const beforeSourceEntries = readdirSync(mixed.source).sort();
  const beforeAuthEntries = readdirSync(mixed.cpaAuthDir).sort();
  const sourceAlias = join(mixed.root, 'host-source-link');
  symlinkSync(mixed.source, sourceAlias);

  // Running inside local source (default state_dir), nested under local source, via symlink, or inside local CPA auth dir must fail closed before mkdir/pull/stop.
  assert.notEqual(mixed.invoke(['--confirm'], true, { cwd: mixed.source, omitStateDir: true }).status, 0);
  assert.notEqual(mixed.invoke(['--confirm'], true, { stateDir: join(mixed.source, 'nested-state') }).status, 0);
  assert.notEqual(mixed.invoke(['--confirm'], true, { stateDir: join(sourceAlias, 'via-link-state') }).status, 0);
  assert.notEqual(mixed.invoke(['--confirm'], true, { stateDir: join(mixed.cpaAuthDir, 'nested-auth-state') }).status, 0);
  assert.equal(commands(mixed.get()).length, 0);
  assert.equal(mixed.get().calls.some(c => c[0] === 'pull'), false);
  assert.deepEqual(readdirSync(mixed.source).sort(), beforeSourceEntries);
  assert.deepEqual(readdirSync(mixed.cpaAuthDir).sort(), beforeAuthEntries);

  // Isolated external state_dir succeeds cleanly while keeping exact /host_mnt source mount strings.
  succeeded(mixed.invoke(['--confirm'], true));
  assert.equal(manifestMap(mixed.stateDir).source_source, `/host_mnt${mixed.source}`);
  assert.equal(manifestMap(mixed.stateDir).target_source_source, `/host_mnt${mixed.source}`);
});

test('Typed reconcile refusal admits only upgrade_reconcile_mismatch and fails closed on other typed or untyped exit-1 errors', t => {
  const nonWaivableCodes = [
    'upgrade_reconcile_target_incomplete',
    'upgrade_reconcile_nothing_compared',
    'upgrade_reconcile_not_copied_in',
    'upgrade_reconcile_stage_closed',
    'upgrade_reconcile_record_incomplete',
    'source_busy',
    'upgrade_target_locked',
    '',
  ];
  for (const code of nonWaivableCodes) {
    const f = fixture(t, { exitCodes: { reconcile: 1 } });
    f.change(s => { s.reconcileErrorCode = code; });
    assert.notEqual(f.invoke(['--confirm'], true).status, 0, `Expected fail-closed reconcile for errorCode=${JSON.stringify(code)}`);
    assert.equal(manifestMap(f.stateDir).phase, 'copied-in');
    assert.equal(targetCreates(f.get()).length, 0);
  }

  // Blocked preflight inside reconcile (exit 1 without errorCode) also fails closed.
  const blockedPreflight = fixture(t, { exitCodes: { reconcile: 1 } });
  blockedPreflight.change(s => { s.reconcilePreflightReady = false; });
  assert.notEqual(blockedPreflight.invoke(['--confirm'], true).status, 0);
  assert.equal(manifestMap(blockedPreflight.stateDir).phase, 'copied-in');
  assert.equal(targetCreates(blockedPreflight.get()).length, 0);

  // Exact typed upgrade_reconcile_mismatch advances to reconciled and deploys Upgrade-only UI.
  const mismatch = fixture(t, { exitCodes: { reconcile: 1 } });
  mismatch.change(s => { s.reconcileErrorCode = 'upgrade_reconcile_mismatch'; });
  const out = mismatch.invoke(['--confirm'], true);
  succeeded(out);
  assert.match(out.stdout, /upgrade_reconcile_mismatch/);
  assert.equal(manifestMap(mismatch.stateDir).phase, 'deployed');
});

test('Exclusive state lock covers fresh, resume and rollback operations and rejects concurrent holders cleanly', t => {
  const f = fixture(t);
  succeeded(f.invoke(['--confirm'], true));
  assert.equal(existsSync(join(f.stateDir, '.lock')), false);
  assert.ok(f.get().locksObserved.length > 0);
  assert.ok(f.get().locksObserved.every(entry => entry.locked === true));

  // Active lock holder blocks both resume and rollback with zero Docker mutations and preserves holder lock.
  const lockDir = join(f.stateDir, '.lock');
  mkdirSync(lockDir, { mode: 0o700 });
  writeFileSync(join(lockDir, 'pid'), '424242\n', { mode: 0o600 });
  f.change(s => { s.calls = []; s.locksObserved = []; });
  assert.notEqual(f.invoke().status, 0);
  assert.notEqual(f.invoke(['--rollback', '--confirm']).status, 0);
  assert.equal(commands(f.get()).length, 0);
  assert.equal(readFileSync(join(lockDir, 'pid'), 'utf8'), '424242\n');
  rmSync(lockDir, { recursive: true, force: true });

  // Rollback acquires the exclusive lock for all mutations and releases it on exit.
  f.change(s => { s.locksObserved = []; });
  succeeded(f.invoke(['--rollback', '--confirm']));
  assert.ok(f.get().locksObserved.length > 0);
  assert.ok(f.get().locksObserved.every(entry => entry.locked === true));
  assert.equal(existsSync(lockDir), false);
});

test('Recorded target identity and original stopped posture are enforced across resume and rollback', t => {
  // Renamed or missing recorded target container is refused on both resume and rollback so originals never start alongside a live target.
  const renamed = fixture(t);
  succeeded(renamed.invoke(['--confirm'], true));
  renamed.change(s => { s.containers.target.name = 'renamed-v2-container'; s.calls = []; });
  assert.notEqual(renamed.invoke().status, 0);
  assert.notEqual(renamed.invoke(['--rollback', '--confirm']).status, 0);
  assert.equal(commands(renamed.get()).length, 0);
  assert.equal(renamed.get().containers.target.running, true);
  assert.equal(renamed.get().containers.manager.running, false);

  renamed.change(s => { delete s.containers.target; s.calls = []; });
  assert.notEqual(renamed.invoke().status, 0);
  assert.notEqual(renamed.invoke(['--rollback', '--confirm']).status, 0);
  assert.equal(commands(renamed.get()).length, 0);

  // Originally stopped Manager and CPA avoid redundant stop calls, reject unexpected restarts during upgrade or before rollback, and remain stopped after rollback.
  const stoppedBoth = fixture(t);
  stoppedBoth.change(s => {
    s.containers.manager.running = false;
    s.containers.manager.ports[0].ip = '';
    s.containers.cpa.running = false;
  });
  succeeded(stoppedBoth.invoke(['--confirm'], true));
  assert.equal(stoppedBoth.get().calls.some(c => c[0] === 'stop' && c.at(-1) === managerID), false);
  assert.equal(stoppedBoth.get().calls.some(c => c[0] === 'stop' && c.at(-1) === cpaID), false);

  // Unexpectedly started original container is refused on rollback instead of leaving it running.
  stoppedBoth.change(s => { s.containers.cpa.running = true; s.calls = []; });
  assert.notEqual(stoppedBoth.invoke(['--rollback', '--confirm']).status, 0);
  assert.equal(commands(stoppedBoth.get()).length, 0);
  stoppedBoth.change(s => { s.containers.cpa.running = false; });
  succeeded(stoppedBoth.invoke(['--rollback', '--confirm']));
  assert.equal(stoppedBoth.get().containers.manager.running, false);
  assert.equal(stoppedBoth.get().containers.cpa.running, false);

  // Restarting a stopped source container mid-upgrade (at phase=stopped) fails closed on resume instead of silently stopping it again.
  const restartedSource = fixture(t, { exitCodes: { preflight: 1 } });
  assert.notEqual(restartedSource.invoke(['--confirm'], true).status, 0);
  assert.equal(manifestMap(restartedSource.stateDir).phase, 'stopped');
  restartedSource.change(s => { s.exitCodes = {}; s.containers.cpa.running = true; s.calls = []; });
  assert.notEqual(restartedSource.invoke().status, 0);
  assert.equal(commands(restartedSource.get()).length, 0);
});

test('tmpfs empty Source column and /run/secrets directory or data-key mounts are handled accurately', t => {
  // Benign tmpfs at /tmp plus individual /run/secrets/cpamp_admin_key mount succeed without column shifting.
  const allowedSecrets = fixture(t, {
    managerMounts: [
      { type: 'volume', src: 'synthetic-manager-data', dst: '/data', readonly: false },
      { type: 'tmpfs', src: '', dst: '/tmp', readonly: false },
      { type: 'bind', src: '/host/secrets/cpamp_admin_key', dst: '/run/secrets/cpamp_admin_key', readonly: true },
    ],
  });
  succeeded(allowedSecrets.invoke(['--confirm'], true));

  // tmpfs or directory /run/secrets, /run/secrets/cpa_data_key, and nested /data/cache tmpfs fail closed before effects.
  const rejectedMountLayouts = [
    [
      { type: 'volume', src: 'synthetic-manager-data', dst: '/data', readonly: false },
      { type: 'tmpfs', src: '', dst: '/run/secrets', readonly: true },
    ],
    [
      { type: 'volume', src: 'synthetic-manager-data', dst: '/data', readonly: false },
      { type: 'tmpfs', src: '', dst: '/run/secrets/cpa_data_key', readonly: true },
    ],
    [
      { type: 'volume', src: 'synthetic-manager-data', dst: '/data', readonly: false },
      { type: 'tmpfs', src: '', dst: '/data/cache', readonly: false },
    ],
  ];
  for (const managerMounts of rejectedMountLayouts) {
    const rejected = fixture(t, { managerMounts });
    assert.notEqual(rejected.invoke(['--confirm'], true).status, 0);
    assert.equal(commands(rejected.get()).length, 0);
  }

  // HostConfig.Tmpfs entries at /run/secrets or under /data also fail closed.
  for (const tmpfsPath of ['/run/secrets', '/data/ephemeral']) {
    const hostTmpfs = fixture(t);
    hostTmpfs.change(s => { s.containers.manager.tmpfs = { [tmpfsPath]: '' }; });
    assert.notEqual(hostTmpfs.invoke(['--confirm'], true).status, 0);
    assert.equal(commands(hostTmpfs.get()).length, 0);
  }
});

test('CPA ~/auth-dir (/root/auth-dir) mount is discovered, isolated, verified and rejected when ambiguous', t => {
  const tildeAuth = fixture(t, { bind: true });
  tildeAuth.change(s => {
    s.containers.cpa.mounts = [
      { type: 'bind', src: tildeAuth.cpaConfigPath, dst: '/CLIProxyAPI/config.yaml', readonly: false },
      { type: 'bind', src: tildeAuth.cpaAuthDir, dst: '/root/auth-dir', readonly: false },
    ];
  });
  // Overlapping state-dir inside ~/auth-dir bind path is rejected before effects.
  assert.notEqual(tildeAuth.invoke(['--confirm'], true, { stateDir: join(tildeAuth.cpaAuthDir, 'nested-state') }).status, 0);
  assert.equal(commands(tildeAuth.get()).length, 0);

  succeeded(tildeAuth.invoke(['--confirm'], true));
  const m = manifestMap(tildeAuth.stateDir);
  assert.equal(m.mode, 'managed');
  assert.equal(m.cpa_auth_destination, '/root/auth-dir');
  assert.equal(m.cpa_auth_source, tildeAuth.cpaAuthDir);
  assert.equal(tildeAuth.get().containers.target.mounts.find(x => x.dst === '/upgrade/auths').src, tildeAuth.cpaAuthDir);
  succeeded(tildeAuth.invoke());
  succeeded(tildeAuth.invoke(['--rollback', '--confirm']));

  // Ambiguous /root/.cli-proxy-api + /root/auth-dir fails closed in auto/managed mode and allows explicit --mode external.
  const dualAuth = fixture(t);
  dualAuth.change(s => {
    s.containers.cpa.mounts.push({ type: 'bind', src: join(dualAuth.cpaRoot, 'second-auths'), dst: '/root/auth-dir', readonly: false });
  });
  assert.notEqual(dualAuth.invoke(['--confirm'], true).status, 0);
  assert.notEqual(dualAuth.invoke(['--mode', 'managed', '--confirm'], true).status, 0);
  assert.equal(commands(dualAuth.get()).length, 0);
  succeeded(dualAuth.invoke(['--mode', 'external', '--confirm'], true));
  assert.equal(manifestMap(dualAuth.stateDir).mode, 'external');
});

test('--public-host validation, remote pre-stop check and Docker -P (PublishAllPorts) lifecycle succeed cleanly', t => {
  const badPublicHosts = [
    'https://cpamp.internal',
    'cpamp.internal:8080',
    '0.0.0.0',
    '::',
    '[::]',
    '999.999.999.999',
    '-bad.internal',
    'bad..internal',
  ];
  for (const host of badPublicHosts) {
    const bad = fixture(t);
    assert.notEqual(bad.invoke(['--public-host', host, '--confirm'], true).status, 0, `Expected rejection for --public-host ${host}`);
    assert.equal(commands(bad.get()).length, 0);
    assert.equal(existsSync(bad.stateDir), false);
  }

  // Remote Docker daemon with wildcard bind IP fails BEFORE pull, mkdir or container stop when --public-host/--public-origin are absent.
  const remoteMissingHost = fixture(t, { contextHost: 'tcp://docker.internal:2376' });
  assert.notEqual(remoteMissingHost.invoke(['--confirm'], true).status, 0);
  assert.equal(commands(remoteMissingHost.get()).length, 0);
  assert.equal(remoteMissingHost.get().calls.some(c => c[0] === 'pull'), false);
  assert.equal(existsSync(remoteMissingHost.stateDir), false);

  // Bracketed uppercase IPv6 --public-host normalizes cleanly and enforces resume/rollback conflict checks.
  const remoteIpv6Host = fixture(t, { contextHost: 'tcp://docker.internal:2376' });
  const hostOut = remoteIpv6Host.invoke(['--public-host', '[2001:DB8::42]', '--confirm'], true);
  succeeded(hostOut);
  assert.equal(manifestMap(remoteIpv6Host.stateDir).public_host, '2001:db8::42');
  assert.match(hostOut.stdout, /http:\/\/\[2001:db8::42\]:38684\/\.well-known\/cpamp-bootstrap\/ui/);
  assert.notEqual(remoteIpv6Host.invoke(['--public-host', 'other.internal']).status, 0);
  assert.notEqual(remoteIpv6Host.invoke(['--public-host', 'other.internal', '--rollback', '--confirm']).status, 0);

  // Running Manager published via Docker -P (PublishAllPorts=true, empty HostConfig.PortBindings) upgrades, resumes and rolls back cleanly.
  const publishAll = fixture(t);
  publishAll.change(s => {
    s.containers.manager.publishAllPorts = true;
    s.containers.manager.portBindings = [];
    s.containers.manager.ports = [{ ip: '0.0.0.0', port: '49153', proto: '18317/tcp' }];
  });
  const pubOut = publishAll.invoke(['--confirm'], true);
  succeeded(pubOut);
  assert.equal(manifestMap(publishAll.stateDir).published_ports, 'publish-all;');
  assert.equal(manifestMap(publishAll.stateDir).host_port, '49153');
  assert.match(pubOut.stdout, /http:\/\/localhost:49153\/\.well-known\/cpamp-bootstrap\/ui/);
  succeeded(publishAll.invoke());
  succeeded(publishAll.invoke(['--rollback', '--confirm']));

  // Stopped -P container with no concrete allocated port fails closed before effects.
  const stoppedPublishAll = fixture(t);
  stoppedPublishAll.change(s => {
    s.containers.manager.running = false;
    s.containers.manager.publishAllPorts = true;
    s.containers.manager.portBindings = [];
    s.containers.manager.ports = [];
  });
  assert.notEqual(stoppedPublishAll.invoke(['--confirm'], true).status, 0);
  assert.equal(commands(stoppedPublishAll.get()).length, 0);
  assert.equal(existsSync(stoppedPublishAll.stateDir), false);
});

test('Duplicate /data mounts and invalid CPA mounts without --mode fail closed before pull or stop under set -e', t => {
  const dupData = fixture(t, {
    managerMounts: [
      { type: 'volume', src: 'synthetic-manager-data-1', dst: '/data', readonly: false },
      { type: 'volume', src: 'synthetic-manager-data-2', dst: '/data', readonly: false },
    ],
  });
  const dupRes = dupData.invoke(['--confirm'], true);
  assert.notEqual(dupRes.status, 0);
  assert.match(dupRes.stderr, /Multiple mounts at \/data/);
  assert.equal(commands(dupData.get()).length, 0);
  assert.equal(dupData.get().calls.some(c => c[0] === 'pull'), false);
  assert.equal(dupData.get().containers.manager.running, true);

  const invalidCpaMounts = [
    [
      { type: 'tmpfs', src: '', dst: '/CLIProxyAPI/config.yaml', readonly: false },
      { type: 'bind', src: '/valid/auths', dst: '/root/.cli-proxy-api', readonly: false },
    ],
    [
      { type: 'bind', src: '/valid/config.yaml', dst: '/CLIProxyAPI/config.yaml', readonly: false },
      { type: 'tmpfs', src: '', dst: '/root/.cli-proxy-api', readonly: false },
    ],
    [
      { type: 'bind', src: '/bad,comma/config.yaml', dst: '/CLIProxyAPI/config.yaml', readonly: false },
      { type: 'bind', src: '/valid/auths', dst: '/root/.cli-proxy-api', readonly: false },
    ],
    [
      { type: 'bind', src: '/valid/config.yaml', dst: '/CLIProxyAPI/config.yaml', readonly: false },
      { type: 'bind', src: '/bad,comma/auths', dst: '/root/.cli-proxy-api', readonly: false },
    ],
    [
      { type: 'bind', src: '/valid/config-1.yaml', dst: '/CLIProxyAPI/config.yaml', readonly: false },
      { type: 'bind', src: '/valid/config-2.yaml', dst: '/CLIProxyAPI/config.yaml', readonly: false },
      { type: 'bind', src: '/valid/auths', dst: '/root/.cli-proxy-api', readonly: false },
    ],
  ];
  for (const cpaMounts of invalidCpaMounts) {
    const badCpa = fixture(t, { cpaMounts });
    const res = badCpa.invoke(['--confirm'], true);
    assert.notEqual(res.status, 0);
    assert.equal(commands(badCpa.get()).length, 0);
    assert.equal(badCpa.get().calls.some(c => c[0] === 'pull'), false);
    assert.equal(badCpa.get().containers.manager.running, true);
    assert.equal(badCpa.get().containers.cpa.running, true);
  }
});

test('Legacy 33-key manifest on /host_mnt resumed from phase=preflighted preserves unprobed state across reconcile exit 2 and succeeds on second resume', t => {
  const f = fixture(t, { bind: true, daemonBindPrefix: '/host_mnt', exitCodes: { run: 1 } });
  assert.notEqual(f.invoke(['--confirm'], true).status, 0);
  assert.equal(manifestMap(f.stateDir).phase, 'preflighted');

  // Rewrite manifest as a legacy 33-key manifest at phase=preflighted (without target_*_source, public_origin, cpa_auth_destination).
  const legacyPreflightedLines = readFileSync(join(f.stateDir, 'manifest'), 'utf8')
    .trimEnd()
    .split('\n')
    .filter(l => !l.startsWith('public_origin\t') && !l.startsWith('cpa_auth_destination\t') && !l.startsWith('target_source_source\t') && !l.startsWith('target_cpa_config_source\t') && !l.startsWith('target_cpa_auth_source\t'));
  writeFileSync(join(f.stateDir, 'manifest'), `${legacyPreflightedLines.join('\n')}\n`, { mode: 0o600 });

  // First resume advances from preflighted -> copied-in and fails at reconcile (exit 2); write_state must NOT persist unprobed target_*_source keys.
  f.change(s => { s.exitCodes = { reconcile: 2 }; });
  assert.notEqual(f.invoke().status, 0);
  const afterFirstResume = manifestMap(f.stateDir);
  assert.equal(afterFirstResume.phase, 'copied-in');
  assert.equal('target_source_source' in afterFirstResume, false);
  assert.equal('target_cpa_config_source' in afterFirstResume, false);
  assert.equal('target_cpa_auth_source' in afterFirstResume, false);

  // Second resume runs probe_daemon_bind_sources resume, persists /host_mnt target_*_source keys, and deploys cleanly.
  f.change(s => { s.exitCodes = {}; });
  succeeded(f.invoke());
  const afterSecondResume = manifestMap(f.stateDir);
  assert.equal(afterSecondResume.phase, 'deployed');
  assert.equal(afterSecondResume.target_source_source, `/host_mnt${f.source}`);
  assert.equal(afterSecondResume.target_cpa_config_source, `/host_mnt${f.cpaConfigPath}`);
  assert.equal(afterSecondResume.target_cpa_auth_source, `/host_mnt${f.cpaAuthDir}`);
  assert.equal(f.get().containers.target.running, true);
});

test('Checkpointing distinguishes phase=reconcile-pending from phase=reconciled and validates target_id boundaries', t => {
  // Reconcile exit 1 (upgrade_reconcile_mismatch) checkpoints phase=reconcile-pending before docker create.
  const pending = fixture(t, { exitCodes: { reconcile: 1 } });
  pending.change(s => {
    s.reconcileErrorCode = 'upgrade_reconcile_mismatch';
    s.failTargetCreateOnce = true;
  });
  assert.notEqual(pending.invoke(['--confirm'], true).status, 0);
  assert.equal(manifestMap(pending.stateDir).phase, 'reconcile-pending');
  assert.equal(manifestMap(pending.stateDir).target_id, '');

  // Non-empty target_id in manifest at phase=reconcile-pending is rejected before effects.
  const pendingManifest = readFileSync(join(pending.stateDir, 'manifest'), 'utf8');
  writeFileSync(
    join(pending.stateDir, 'manifest'),
    pendingManifest.replace('target_id\t', `target_id\t${targetID}`),
    { mode: 0o600 },
  );
  pending.change(s => { s.calls = []; });
  assert.notEqual(pending.invoke().status, 0);
  assert.equal(commands(pending.get()).length, 0);
  writeFileSync(join(pending.stateDir, 'manifest'), pendingManifest, { mode: 0o600 });

  // Resuming from phase=reconcile-pending when target container was not yet created re-runs idempotent run+reconcile and deploys.
  succeeded(pending.invoke());
  assert.equal(manifestMap(pending.stateDir).phase, 'deployed');
  assert.equal(manifestMap(pending.stateDir).target_id, targetID);

  // Interrupted right after docker create at phase=reconcile-pending (target exists in Docker, target_id empty in manifest) is accepted on resume and rollback.
  writeFileSync(join(pending.stateDir, 'manifest'), pendingManifest, { mode: 0o600 });
  pending.change(s => { s.containers.target.running = false; });
  succeeded(pending.invoke());
  assert.equal(manifestMap(pending.stateDir).phase, 'deployed');
  writeFileSync(join(pending.stateDir, 'manifest'), pendingManifest, { mode: 0o600 });
  succeeded(pending.invoke(['--rollback', '--confirm']));
  assert.equal(manifestMap(pending.stateDir).phase, 'rolled-back');

  // Reconcile exit 0 (recorded:true) checkpoints phase=reconciled before docker create and resumes cleanly.
  const cleanReconcile = fixture(t);
  cleanReconcile.change(s => { s.failTargetCreateOnce = true; });
  assert.notEqual(cleanReconcile.invoke(['--confirm'], true).status, 0);
  assert.equal(manifestMap(cleanReconcile.stateDir).phase, 'reconciled');
  assert.equal(manifestMap(cleanReconcile.stateDir).target_id, '');
  succeeded(cleanReconcile.invoke());
  assert.equal(manifestMap(cleanReconcile.stateDir).phase, 'deployed');
});

test('--network rejects host/none before pull or stop, canonicalizes network IDs to .Name, and deduplicates existing Manager networks', t => {
  for (const forbidden of ['host', 'none', 'alias-to-host', 'alias-to-none', 'alias-to-empty']) {
    const f = fixture(t);
    f.change(s => {
      s.networkAliases = {
        'alias-to-host': 'host',
        'alias-to-none': 'none',
        'alias-to-empty': '',
      };
    });
    const res = f.invoke(['--network', forbidden, '--confirm'], true);
    assert.notEqual(res.status, 0, `Expected rejection for --network ${forbidden}`);
    assert.equal(commands(f.get()).length, 0);
    assert.equal(f.get().calls.some(c => c[0] === 'pull'), false);
    assert.equal(existsSync(f.stateDir), false);
    assert.equal(f.get().containers.manager.running, true);
  }

  // Canonicalize network ID to .Name and deduplicate both original Manager networks and already-present extra_network.
  const dedup = fixture(t);
  dedup.change(s => {
    s.containers.manager.networks = ['synthetic-default', 'synthetic-cpa', 'synthetic-default'];
    s.networkAliases = {
      'net-id-existing-cpa': 'synthetic-cpa',
      'net-id-extra-cpa': 'extra-cpa-net',
    };
  });
  const existingNetOut = dedup.invoke(['--network', 'net-id-existing-cpa', '--confirm'], true);
  succeeded(existingNetOut);
  assert.equal(manifestMap(dedup.stateDir).networks, 'synthetic-default,synthetic-cpa');
  assert.equal(manifestMap(dedup.stateDir).extra_network, 'synthetic-cpa');
  assert.match(existingNetOut.stdout, /networks: synthetic-default,synthetic-cpa$/m);
  assert.deepEqual(dedup.get().containers.target.networks, ['synthetic-default', 'synthetic-cpa']);
  // Resuming with either the canonical name or the network ID succeeds; a different network conflicts.
  succeeded(dedup.invoke(['--network', 'synthetic-cpa']));
  succeeded(dedup.invoke(['--network', 'net-id-existing-cpa']));
  assert.notEqual(dedup.invoke(['--network', 'extra-cpa-net']).status, 0);

  // Tampering extra_network in manifest to host or none fails closed in load_state before effects.
  const baseManifest = readFileSync(join(dedup.stateDir, 'manifest'), 'utf8');
  for (const badNet of ['host', 'none']) {
    writeFileSync(
      join(dedup.stateDir, 'manifest'),
      baseManifest.replace('extra_network\tsynthetic-cpa', `extra_network\t${badNet}`),
      { mode: 0o600 },
    );
    dedup.change(s => { s.calls = []; });
    assert.notEqual(dedup.invoke().status, 0);
    assert.notEqual(dedup.invoke(['--rollback', '--confirm']).status, 0);
    assert.equal(commands(dedup.get()).length, 0);
  }
  writeFileSync(join(dedup.stateDir, 'manifest'), baseManifest, { mode: 0o600 });

  // Network ID for a distinct extra network canonicalizes to extra-cpa-net and attaches once.
  const extraId = fixture(t);
  extraId.change(s => {
    s.networkAliases = { 'net-id-extra-cpa': 'extra-cpa-net' };
  });
  const extraOut = extraId.invoke(['--network', 'net-id-extra-cpa', '--confirm'], true);
  succeeded(extraOut);
  assert.equal(manifestMap(extraId.stateDir).extra_network, 'extra-cpa-net');
  assert.match(extraOut.stdout, /networks: synthetic-default,synthetic-cpa,extra-cpa-net$/m);
  assert.deepEqual(extraId.get().containers.target.networks, ['synthetic-default', 'synthetic-cpa', 'extra-cpa-net']);
});

test('release_state_lock cleans up .lock and uncommitted state_dir when interrupted before pid file is present', t => {
  const f = fixture(t, { bind: true });
  f.change(s => { s.unlinkLockPidAndFailOnce = true; });
  assert.notEqual(f.invoke(['--confirm'], true).status, 0);
  assert.equal(existsSync(join(f.stateDir, '.lock')), false);
  assert.equal(existsSync(f.stateDir), false);

  // Subsequent retry on the same state_dir succeeds without being blocked by an orphaned .lock directory.
  succeeded(f.invoke(['--confirm'], true));
  assert.equal(manifestMap(f.stateDir).phase, 'deployed');
});

test('Explicit --mode external skips Managed-only CPA nested auth and tmpfs overlay checks without stopping CPA', t => {
  // Nested child mount under /root/.cli-proxy-api refuses auto/managed mode, while --mode external succeeds untouched.
  const nested = fixture(t);
  nested.change(s => {
    s.containers.cpa.mounts.push({ type: 'bind', src: join(nested.cpaRoot, 'sub'), dst: '/root/.cli-proxy-api/sub', readonly: false });
  });
  const nestedDefault = nested.invoke(['--confirm'], true);
  assert.notEqual(nestedDefault.status, 0);
  assert.match(nestedDefault.stderr, /Nested CPA auth mounts are unsupported/);
  const nestedManaged = nested.invoke(['--mode', 'managed', '--confirm'], true);
  assert.notEqual(nestedManaged.status, 0);
  assert.match(nestedManaged.stderr, /Nested CPA auth mounts are unsupported/);
  assert.equal(commands(nested.get()).length, 0);
  assert.equal(nested.get().containers.manager.running, true);
  assert.equal(nested.get().containers.cpa.running, true);

  succeeded(nested.invoke(['--mode', 'external', '--confirm'], true));
  const nestedManifest = manifestMap(nested.stateDir);
  assert.equal(nestedManifest.mode, 'external');
  assert.equal(nestedManifest.cpa_config_type, '');
  assert.equal(nestedManifest.cpa_config_source, '');
  assert.equal(nestedManifest.cpa_auth_destination, '');
  assert.equal(nestedManifest.cpa_auth_type, '');
  assert.equal(nestedManifest.cpa_auth_source, '');
  assert.equal(nested.get().containers.cpa.running, true);
  assert.deepEqual(nested.get().calls.filter(c => c[0] === 'stop').map(c => c.at(-1)), [managerID]);
  assert.equal(nested.get().containers.target.mounts.some(m => m.dst === '/upgrade/cpa/config.yaml' || m.dst === '/upgrade/auths'), false);
  succeeded(nested.invoke());

  // tmpfs overlay on /root/.cli-proxy-api refuses auto/managed mode, while --mode external succeeds untouched.
  const tmpfsOverlay = fixture(t);
  tmpfsOverlay.change(s => {
    s.containers.cpa.tmpfs = { '/root/.cli-proxy-api': '' };
  });
  const tmpfsDefault = tmpfsOverlay.invoke(['--confirm'], true);
  assert.notEqual(tmpfsDefault.status, 0);
  assert.match(tmpfsDefault.stderr, /CPA auth directory cannot be backed or overlaid by tmpfs/);
  const tmpfsManaged = tmpfsOverlay.invoke(['--mode', 'managed', '--confirm'], true);
  assert.notEqual(tmpfsManaged.status, 0);
  assert.match(tmpfsManaged.stderr, /CPA auth directory cannot be backed or overlaid by tmpfs/);
  assert.equal(commands(tmpfsOverlay.get()).length, 0);
  assert.equal(tmpfsOverlay.get().containers.manager.running, true);
  assert.equal(tmpfsOverlay.get().containers.cpa.running, true);

  succeeded(tmpfsOverlay.invoke(['--mode', 'external', '--confirm'], true));
  const tmpfsManifest = manifestMap(tmpfsOverlay.stateDir);
  assert.equal(tmpfsManifest.mode, 'external');
  assert.equal(tmpfsManifest.cpa_config_type, '');
  assert.equal(tmpfsManifest.cpa_config_source, '');
  assert.equal(tmpfsManifest.cpa_auth_destination, '');
  assert.equal(tmpfsManifest.cpa_auth_type, '');
  assert.equal(tmpfsManifest.cpa_auth_source, '');
  assert.equal(tmpfsOverlay.get().containers.cpa.running, true);
  assert.deepEqual(tmpfsOverlay.get().calls.filter(c => c[0] === 'stop').map(c => c.at(-1)), [managerID]);
  assert.equal(tmpfsOverlay.get().containers.target.mounts.some(m => m.dst === '/upgrade/cpa/config.yaml' || m.dst === '/upgrade/auths'), false);
  succeeded(tmpfsOverlay.invoke());
});

test('Activated v2 resume accepts canonical $ and & control prefixes including Go JSON \\u0026 escaping', t => {
  const f = fixture(t);
  succeeded(f.invoke(['--confirm'], true));
  for (const prefix of ['/ops$admin', '/ops&admin', '/ops&a&b$1']) {
    f.change(s => {
      s.operationStage = 'complete';
      s.committedControlPath = prefix;
      s.hideFreshControlRoute = false;
      s.stopTargetAfterCanonicalPolls = 0;
      s.canonicalRoutePolls = 0;
      s.identityRoutePolls = 0;
      s.containers.target.running = false;
    });
    const resumed = f.invoke();
    succeeded(resumed);
    assert.equal(resumed.stdout.includes('Upgrade UI:'), false);
    assert.equal(
      resumed.stdout.split('\n').includes(`Management console: http://localhost:38684${prefix}`),
      true,
      `Expected exact Management console line for ${prefix}, got:\n${resumed.stdout}`,
    );

    // Fallback through upgrade status --json when internal fresh-routing control read is unavailable.
    f.change(s => { s.hideFreshControlRoute = true; });
    const fallback = f.invoke();
    succeeded(fallback);
    assert.equal(fallback.stdout.includes('Upgrade UI:'), false);
    assert.equal(
      fallback.stdout.split('\n').includes(`Management console: http://localhost:38684${prefix}`),
      true,
      `Expected exact fallback Management console line for ${prefix}, got:\n${fallback.stdout}`,
    );
  }

  // Non-canonical characters remain rejected even after JSON decoding.
  // Stop the synthetic target on the post-check State.Running inspect after 1 canonical poll so each
  // case exits deterministically with status 1 instead of spinning 30 polls into spawnSync ETIMEDOUT.
  for (const invalid of ['/ops\\admin', '/ops?admin', '/ops#admin', '/ops admin', '/ops/../admin']) {
    f.change(s => {
      s.operationStage = 'complete';
      s.committedControlPath = invalid;
      s.hideFreshControlRoute = false;
      s.canonicalRoutePolls = 0;
      s.identityRoutePolls = 0;
      s.readinessPolls = 0;
      s.stopTargetAfterCanonicalPolls = 1;
      s.containers.target.running = false;
    });
    const res = f.invoke();
    assert.equal(res.error, undefined, `Expected clean exit without spawn error for ${invalid}: ${res.error}`);
    assert.equal(res.status, 1, `Expected exit 1 for invalid control prefix ${invalid}, got ${res.status}\n${res.stderr}`);
    assert.match(
      res.stderr,
      /Target UI or committed canonical management route is not ready/,
      `Expected readiness refusal stderr for invalid control prefix ${invalid}, got:\n${res.stderr}`,
    );
    const after = f.get();
    assert.equal(after.canonicalRoutePolls, 1, `Expected bounded canonical route poll for ${invalid}`);
    assert.equal(after.identityRoutePolls || 0, 0, `Expected valid_control_prefix to block identity wget for ${invalid}`);
  }
});

test('External mode resume and rollback allow an associated CPA recorded as stopped to start later', t => {
  // Deployed external upgrade: associated CPA starts stopped (cpa_running=false) and is started later.
  const ext = fixture(t);
  ext.change(s => { s.containers.cpa.running = false; });
  succeeded(ext.invoke(['--mode', 'external', '--confirm'], true));
  const deployedManifest = manifestMap(ext.stateDir);
  assert.equal(deployedManifest.mode, 'external');
  assert.equal(deployedManifest.cpa_id, cpaID);
  assert.equal(deployedManifest.cpa_running, 'false');
  assert.equal(deployedManifest.phase, 'deployed');
  assert.equal(ext.get().containers.cpa.running, false);

  // Operator starts the external CPA after preparation; resume must succeed without stopping CPA.
  ext.change(s => {
    s.containers.cpa.running = true;
    s.containers.target.running = false;
    s.calls = [];
  });
  succeeded(ext.invoke());
  assert.equal(ext.get().containers.target.running, true);
  assert.equal(ext.get().containers.cpa.running, true);
  assert.equal(ext.get().calls.some(c => c[0] === 'stop' && c.at(-1) === cpaID), false);

  // Explicit rollback must also succeed while the external CPA is running and leave it untouched.
  ext.change(s => { s.calls = []; });
  succeeded(ext.invoke(['--rollback', '--confirm']));
  assert.equal(manifestMap(ext.stateDir).phase, 'rolled-back');
  assert.equal(ext.get().containers.target.running, false);
  assert.equal(ext.get().containers.manager.running, true);
  assert.equal(ext.get().containers.cpa.running, true);
  assert.equal(ext.get().calls.some(c => (c[0] === 'stop' || c[0] === 'start') && c.at(-1) === cpaID), false);

  // Mid-upgrade resume from phase=stopped also succeeds when the associated external CPA starts later.
  const mid = fixture(t, { exitCodes: { preflight: 1 } });
  mid.change(s => { s.containers.cpa.running = false; });
  const failedPreflight = mid.invoke(['--mode', 'external', '--confirm'], true);
  assert.equal(failedPreflight.error, undefined);
  assert.notEqual(failedPreflight.status, 0);
  assert.equal(manifestMap(mid.stateDir).phase, 'stopped');
  assert.equal(manifestMap(mid.stateDir).cpa_running, 'false');

  mid.change(s => {
    s.exitCodes = {};
    s.containers.cpa.running = true;
    s.calls = [];
  });
  succeeded(mid.invoke());
  assert.equal(manifestMap(mid.stateDir).phase, 'deployed');
  assert.equal(mid.get().containers.cpa.running, true);
  assert.equal(mid.get().calls.some(c => c[0] === 'stop' && c.at(-1) === cpaID), false);
  succeeded(mid.invoke(['--rollback', '--confirm']));
  assert.equal(manifestMap(mid.stateDir).phase, 'rolled-back');
  assert.equal(mid.get().containers.manager.running, true);
  assert.equal(mid.get().containers.cpa.running, true);
});
