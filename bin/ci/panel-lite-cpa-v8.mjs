#!/usr/bin/env node
// Panel Lite acceptance against the pinned CPA v8 (v2 Beta, Lite 1-5, D).
//
// Panel Lite is the single-file `management.html` that CPA itself hosts. In the
// v2 Beta it keeps talking to the CPA Management API under `/v0/management`,
// which CPA v8 still serves; moving to `/v8/management` is a post-Stable change.
// This harness proves the whole chain, on the real pinned CPA v8 executable:
//
//   0. `npm --workspace apps/web run build:panel` produces exactly one file,
//      `apps/web/dist/panel/management.html`, with no sibling `assets/` reference.
//   1. CPA, configured with `panel-github-repository` for the CPAMP repository,
//      downloads that file from a local release stub (CPA only resolves GitHub
//      hosts, so the stub is an HTTP CONNECT proxy that answers for
//      `api.github.com` with a throw-away certificate) -- or, with
//      `--panel-source mounted`, serves the file placed at `static/management.html`.
//   2. `static/management.html` equals the build byte for byte, and
//      `GET /management.html` answers 200 with those same bytes.
//   3. The Lite panel contract holds on CPA v8: the v8 management key
//      authenticates `/v0/management/config` (and a missing or wrong key does
//      not), a `PUT /v0/management/request-retry` -- the call the panel's
//      configuration page makes -- is written to `config.yaml` on disk and
//      survives a CPA restart.
//   4. With `disable-control-panel: true`, `GET /management.html` is 404 even
//      though a bundle is present in `static/`, and CPA never contacts the stub.
//
// Both config layouts CPA v8 reads are exercised: the legacy `remote-management:`
// layout the user documentation shows, and the v8 `management:` layout.
//
// The CPA executable is the one `Dockerfile.runtime` pins (`cpa-v8-download`
// stage): the harness reads the version and amd64 executable sha256 from that
// file, refuses any image whose `/usr/local/bin/cli-proxy-api` differs, and so
// follows a re-pin without edits.
//
// Isolation: every container is named `cpampd-lite-<run>-<layout>-<role>` and is
// published only on an ephemeral 127.0.0.1 port; `cpamp-*` resources are never
// created, stopped or removed, and port 18317 is never bound. Everything the run
// created is released on success, on failure and on SIGINT/SIGTERM; a release that
// fails makes the run exit non-zero.
//
// Usage:
//   node bin/ci/panel-lite-cpa-v8.mjs --bundle-only [--skip-build]
//   node bin/ci/panel-lite-cpa-v8.mjs [--cpa-image REF | --build-cpa-image]
//        [--skip-build] [--panel-source stub|mounted] [--layouts legacy,v8]
//        [--repository owner/name] [--report FILE]
//
// `--cpa-image` falls back to `CPAMP_LITE_CPA_IMAGE`, then to the local image
// `cpampd-lite-cpa-v8:<version>-<sha12>` that `--build-cpa-image` produces from
// the `runtime` target of `Dockerfile.runtime`. Exit codes: 0 pass, 1 failed
// check, 2 usage or missing prerequisite.

import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import crypto from 'node:crypto';
import fs from 'node:fs';
import http from 'node:http';
import https from 'node:https';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

export const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');
export const DEFAULT_PANEL = path.join(ROOT, 'apps', 'web', 'dist', 'panel', 'management.html');
export const BUILD_PANEL_COMMAND = ['npm', '--workspace', 'apps/web', 'run', 'build:panel'];
export const CONTRACT = 'cpamp.panel-lite-cpa-v8.v1';

export const LAYOUTS = ['legacy', 'v8'];
export const PANEL_SOURCES = ['stub', 'mounted'];
export const DEFAULT_REPOSITORY = 'seakee/CPA-Manager-Plus';

export const CONTAINER_PREFIX = 'cpampd-lite-';
const OWNED_NAME = /^cpampd-lite-[0-9a-f]{8}-[a-z0-9-]{1,40}$/;
const CPA_PORT = 8317;
const CPA_BINARY = '/usr/local/bin/cli-proxy-api';
const CPA_MANIFEST = '/usr/local/share/cpamp/runtime/cpa-artifact.json';
const CONFIG_MOUNT = '/cpa';
const STUB_MOUNT = '/cpampd-lite-stub';
const STUB_HOST = 'api.github.com';
const STUB_ASSET_PATH = '/cpampd-lite/assets/management.html';
export const STUB_TAG = 'v2.0.0-beta.0-cpampd-lite-stub';

// The Lite panel's configuration page writes through
// `apiClient.put('/request-retry', { value })`; the harness drives the same call.
const RETRY_BEFORE = 3;
const RETRY_AFTER = 11;

// How long a CPA that is going to contact GitHub takes to do so after it is up;
// the enabled run observes the download well inside this window.
const SETTLE_MS = 6_000;
// What a stub "1.x" release serves in mounted mode, where nothing may fetch it.
export const DECOY_1X_PANEL = Buffer.from('<!doctype html><html><head><style>a{}</style></head><body><script>/* 1.x decoy */</script></body></html>');

export class UsageError extends Error {}

// --------------------------------------------------------------------------
// Pin and bundle
// --------------------------------------------------------------------------

/**
 * The CPA v8 pin of `Dockerfile.runtime`: the version and the amd64 executable
 * sha256 of its `cpa-v8-download` stage. Nothing here is a second copy of the pin.
 */
export function readCpaV8Pin(dockerfile) {
  const start = dockerfile.search(/^FROM\b[^\n]*\sAS\s+cpa-v8-download\s*$/m);
  assert(start >= 0, 'Dockerfile.runtime has no cpa-v8-download stage');
  const rest = dockerfile.slice(start).split('\n').slice(1).join('\n');
  const next = rest.search(/^FROM\b/m);
  const stage = next < 0 ? rest : rest.slice(0, next);
  const version = /CLIProxyAPI_(\d+\.\d+\.\d+)_linux_/.exec(stage)?.[1];
  assert(version, 'cpa-v8-download stage names no CLIProxyAPI version');
  const amd64 = /amd64\)[\s\S]*?executable_sha256="([0-9a-f]{64})"/.exec(stage)?.[1];
  assert(amd64, 'cpa-v8-download stage has no amd64 executable_sha256');
  return { version, executableSha256: amd64 };
}

export function loadCpaV8Pin(root = ROOT) {
  return readCpaV8Pin(fs.readFileSync(path.join(root, 'Dockerfile.runtime'), 'utf8'));
}

export const defaultImageTag = (pin) =>
  `${CONTAINER_PREFIX.slice(0, -1)}-cpa-v8:${pin.version}-${pin.executableSha256.slice(0, 12)}`;

const sha256 = (bytes) => crypto.createHash('sha256').update(bytes).digest('hex');

/**
 * Panel Lite must be one self-contained HTML file named like the release asset
 * CPA looks for. A sibling file or any resource reference CPA cannot serve is a
 * failure: CPA stores and serves `management.html` alone.
 */
export function assertSingleFilePanel({ name, entries, bytes }) {
  assert.equal(name, 'management.html', 'Panel Lite artifact must be named management.html');
  assert.deepEqual(entries, ['management.html'], 'Panel Lite must be one HTML artifact');
  assert(bytes.length > 0, 'management.html is empty');
  const html = bytes.toString('utf8');
  assert(/<!doctype html/i.test(html), 'management.html is not an HTML document');
  assert(html.includes('<script') && html.includes('<style'), 'Panel JS/CSS is not inlined');
  const external = [
    [/(?:src|href)=["'](?:\.\/|\/)?assets\//i, 'attribute reference to assets/'],
    [/(?:^|[^\w.-])(?:\.\/|\/)assets\/[\w.-]/, 'path reference to ./assets/ or /assets/'],
    [/url\(\s*["']?(?:\.\/|\/)?assets\//i, 'url() reference to assets/'],
    [/<script\b[^>]*\ssrc=/i, '<script src=...> sibling resource'],
    [/<link\b[^>]*\shref=["'](?!data:)/i, '<link href=...> sibling resource'],
  ];
  for (const [pattern, label] of external) {
    assert(!pattern.test(html), `Panel references runtime sibling resources: ${label}`);
  }
}

export function loadPanel(panelPath) {
  const resolved = path.resolve(panelPath);
  const bytes = fs.readFileSync(resolved);
  assertSingleFilePanel({
    name: path.basename(resolved),
    entries: fs.readdirSync(path.dirname(resolved)).sort(),
    bytes,
  });
  return { path: resolved, bytes, size: bytes.length, sha256: sha256(bytes) };
}

export function buildPanel({ exec = defaultBuildExec, root = ROOT } = {}) {
  const result = exec(BUILD_PANEL_COMMAND, { cwd: root });
  if (result.status !== 0) {
    throw new Error(`${BUILD_PANEL_COMMAND.join(' ')} failed (${result.status ?? result.error})`);
  }
}

function defaultBuildExec(command, { cwd }) {
  return spawnSync(command[0], command.slice(1), { cwd, stdio: ['ignore', 2, 2] });
}

// --------------------------------------------------------------------------
// CPA configuration
// --------------------------------------------------------------------------

const SAFE_VALUE = /^[A-Za-z0-9_./:@-]+$/;
const quote = (value) => {
  assert(SAFE_VALUE.test(value), `unsafe config value: ${value}`);
  return `"${value}"`;
};

/**
 * One CPA config in either layout CPA v8 accepts: the legacy `remote-management:`
 * layout and the v8 `management:` layout. `proxyUrl` routes CPA's outbound
 * requests through the release stub.
 */
export function renderCpaConfig({
  layout,
  managementKey,
  repository,
  disableControlPanel,
  disableAutoUpdatePanel,
  proxyUrl,
  requestRetry = RETRY_BEFORE,
}) {
  assert(LAYOUTS.includes(layout), `unknown config layout: ${layout}`);
  assert(Number.isInteger(requestRetry) && requestRetry >= 0, 'request-retry must be an integer');
  const lines = [];
  const management = (indent) => [
    `${indent}allow-remote: true`,
    `${indent}secret-key: ${quote(managementKey)}`,
    `${indent}disable-control-panel: ${disableControlPanel ? 'true' : 'false'}`,
    `${indent}disable-auto-update-panel: ${disableAutoUpdatePanel ? 'true' : 'false'}`,
    `${indent}panel-github-repository: ${quote(repository)}`,
  ];
  if (layout === 'legacy') {
    lines.push(
      `host: ${quote('0.0.0.0')}`,
      `port: ${CPA_PORT}`,
      `auth-dir: ${quote(`${CONFIG_MOUNT}/auth`)}`,
      'logging-to-file: false',
      `request-retry: ${requestRetry}`
    );
    if (proxyUrl) lines.push(`proxy-url: ${quote(proxyUrl)}`);
    lines.push('remote-management:', ...management('  '));
  } else {
    lines.push(
      'config-version: 8',
      'server:',
      `  host: ${quote('0.0.0.0')}`,
      `  port: ${CPA_PORT}`,
      'management:',
      ...management('  '),
      'oauth:',
      `  auth-dir: ${quote(`${CONFIG_MOUNT}/auth`)}`,
      'routing:',
      '  retry:',
      `    request-retry: ${requestRetry}`
    );
    if (proxyUrl) lines.push('requests:', `  proxy-url: ${quote(proxyUrl)}`);
    lines.push('observability:', '  logs:', '    logging-to-file: false');
  }
  return `${lines.join('\n')}\n`;
}

/**
 * The `request-retry` value CPA persisted. The key sits at the top level in the
 * legacy layout and under `routing.retry` in the v8 layout, so it is matched by
 * name; exactly one occurrence must exist.
 */
export function persistedRequestRetry(configText) {
  const values = [...configText.matchAll(/^[ \t]*request-retry:[ \t]*(-?\d+)[ \t]*(?:#.*)?$/gm)].map(
    (match) => Number(match[1])
  );
  assert.equal(values.length, 1, `expected exactly one request-retry in config.yaml, got ${values.length}`);
  return values[0];
}

// --------------------------------------------------------------------------
// Release stub: answers for api.github.com through an HTTP CONNECT proxy
// --------------------------------------------------------------------------

export function releaseDocument({ repository, tag = STUB_TAG, panel }) {
  assert(/^[\w.-]+\/[\w.-]+$/.test(repository), `bad repository: ${repository}`);
  return {
    url: `https://${STUB_HOST}/repos/${repository}/releases/latest`,
    tag_name: tag,
    name: tag,
    draft: false,
    prerelease: false,
    assets: [
      {
        name: 'management.html',
        size: panel.length,
        content_type: 'application/octet-stream',
        digest: `sha256:${sha256(panel)}`,
        browser_download_url: `https://${STUB_HOST}${STUB_ASSET_PATH}`,
      },
    ],
  };
}

/** A self-signed server certificate for api.github.com, valid for one day. */
export function generateStubCertificate({ exec = defaultOpensslExec } = {}) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'cpampd-lite-cert-'));
  try {
    const config = path.join(dir, 'openssl.cnf');
    fs.writeFileSync(
      config,
      [
        '[req]',
        'distinguished_name = dn',
        'x509_extensions = v3',
        'prompt = no',
        '[dn]',
        `CN = ${STUB_HOST}`,
        '[v3]',
        `subjectAltName = DNS:${STUB_HOST}`,
        'basicConstraints = critical,CA:FALSE',
        'keyUsage = critical,digitalSignature,keyEncipherment',
        'extendedKeyUsage = serverAuth',
        '',
      ].join('\n')
    );
    const key = path.join(dir, 'key.pem');
    const cert = path.join(dir, 'cert.pem');
    const result = exec([
      'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-sha256', '-days', '1',
      '-config', config, '-keyout', key, '-out', cert,
    ]);
    if (result.status !== 0) {
      throw new UsageError(
        `openssl is required to create the release stub certificate: ${result.stderr || result.error?.message || result.status}`
      );
    }
    return { key: fs.readFileSync(key, 'utf8'), cert: fs.readFileSync(cert, 'utf8') };
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
}

function defaultOpensslExec(args) {
  const result = spawnSync('openssl', args, { encoding: 'utf8' });
  return { status: result.status, stderr: result.stderr, error: result.error };
}

/**
 * The local release stub. CPA only resolves GitHub hosts for
 * `panel-github-repository`, so the stub is the proxy CPA is told to use
 * (`proxy-url`): it accepts CONNECT for api.github.com:443 only, terminates TLS
 * with `certificate`, and serves the `releases/latest` document and the asset.
 * Every other CONNECT target and every plain proxied request is refused, which
 * keeps the run hermetic. `events` records what CPA asked for.
 */
export async function createReleaseStub({
  panel,
  repository,
  certificate,
  host = '0.0.0.0',
  tag = STUB_TAG,
}) {
  const events = [];
  const sockets = new Set();
  const latest = `/repos/${repository}/releases/latest`;
  const document = releaseDocument({ repository, tag, panel });

  const tlsServer = https.createServer(
    { key: certificate.key, cert: certificate.cert },
    (req, res) => {
      events.push({ kind: 'request', method: req.method, path: req.url });
      if (req.method === 'GET' && req.url === latest) {
        const body = Buffer.from(JSON.stringify(document));
        res.writeHead(200, { 'content-type': 'application/json', 'content-length': body.length });
        res.end(body);
      } else if (req.method === 'GET' && req.url === STUB_ASSET_PATH) {
        res.writeHead(200, {
          'content-type': 'application/octet-stream',
          'content-length': panel.length,
        });
        res.end(panel);
      } else {
        res.writeHead(404, { 'content-type': 'text/plain' });
        res.end('not found');
      }
    }
  );
  const proxy = http.createServer((req, res) => {
    events.push({ kind: 'plain', method: req.method, target: req.url });
    res.writeHead(403);
    res.end();
  });
  proxy.on('connect', (req, socket) => {
    sockets.add(socket);
    socket.on('close', () => sockets.delete(socket));
    socket.on('error', () => {});
    if (req.url !== `${STUB_HOST}:443`) {
      events.push({ kind: 'deny-connect', target: req.url });
      socket.end('HTTP/1.1 403 Forbidden\r\nConnection: close\r\n\r\n');
      return;
    }
    events.push({ kind: 'connect', target: req.url });
    socket.write('HTTP/1.1 200 Connection Established\r\n\r\n');
    tlsServer.emit('connection', socket);
  });
  proxy.on('connection', (socket) => {
    sockets.add(socket);
    socket.on('close', () => sockets.delete(socket));
  });
  await new Promise((resolve, reject) => {
    proxy.once('error', reject);
    proxy.listen(0, host, resolve);
  });
  return {
    port: proxy.address().port,
    caPem: certificate.cert,
    events,
    latestPath: latest,
    assetPath: STUB_ASSET_PATH,
    async close() {
      for (const socket of sockets) socket.destroy();
      await new Promise((resolve) => proxy.close(resolve));
      tlsServer.close();
    },
  };
}

// --------------------------------------------------------------------------
// Docker, restricted to this harness's own namespace
// --------------------------------------------------------------------------

export function assertOwnedName(name) {
  assert(
    typeof name === 'string' && OWNED_NAME.test(name) && !/^cpamp[-_]/.test(name),
    `refusing container name outside ${CONTAINER_PREFIX}<run>-*: ${name}`
  );
  return name;
}

export const containerName = (runId, layout, role) =>
  assertOwnedName(`${CONTAINER_PREFIX}${runId}-${layout}-${role}`);

function assertMountSource(source) {
  assert(
    path.isAbsolute(source) && !/[:,\n]/.test(source),
    `unsafe bind-mount source: ${source}`
  );
}

/** argv of the long-running CPA container. */
export function dockerRunArgs({ name, runId, image, configDir, stubDir, uid, gid }) {
  assertOwnedName(name);
  assert(/^[0-9a-f]{8}$/.test(runId), 'bad run id');
  assertMountSource(configDir);
  const args = [
    'run', '-d', '--pull', 'never', '--platform', 'linux/amd64',
    '--name', name,
    '--label', `cpampd.lite.run=${runId}`,
  ];
  if (uid !== undefined) args.push('--user', `${uid}:${gid}`);
  args.push(
    '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges',
    '-p', `127.0.0.1:0:${CPA_PORT}`,
    '-v', `${configDir}:${CONFIG_MOUNT}`
  );
  if (stubDir) {
    assertMountSource(stubDir);
    args.push(
      '--add-host', 'host.docker.internal:host-gateway',
      '-e', `SSL_CERT_FILE=${STUB_MOUNT}/ca.pem`,
      '-v', `${stubDir}:${STUB_MOUNT}:ro`
    );
  }
  // `-local-model` keeps CPA from fetching its remote model catalogs.
  args.push('--entrypoint', CPA_BINARY, image, '-config', `${CONFIG_MOUNT}/config.yaml`, '-local-model');
  assert(!args.some((arg) => /18317/.test(arg)), 'the harness never uses port 18317');
  return args;
}

/** argv of the throw-away container that reads the image's CPA identity. */
export function identityProbeArgs({ name, runId, image }) {
  assertOwnedName(name);
  return [
    'run', '--rm', '--pull', 'never', '--platform', 'linux/amd64',
    '--name', name,
    '--label', `cpampd.lite.run=${runId}`,
    '--entrypoint', 'sh', image, '-c',
    `sha256sum ${CPA_BINARY} && cat ${CPA_MANIFEST} && ${CPA_BINARY} -help 2>&1 | head -n 1`,
  ];
}

function defaultDockerExec(args, { timeoutMs = 300_000 } = {}) {
  const result = spawnSync('docker', args, {
    encoding: 'utf8',
    timeout: timeoutMs,
    maxBuffer: 64 * 1024 * 1024,
  });
  return { status: result.status, stdout: result.stdout ?? '', stderr: result.stderr ?? '', error: result.error };
}

export function createDocker({ exec = defaultDockerExec } = {}) {
  const owned = new Set();
  const must = (args, options) => {
    const result = exec(args, options);
    if (result.status !== 0) {
      const detail = (result.stderr || result.error?.message || '').trim();
      throw new Error(`docker ${args[0]} failed (${result.status}): ${detail}`);
    }
    return result.stdout.trim();
  };
  return {
    owned,
    available() {
      const result = exec(['version', '--format', '{{.Server.Os}}/{{.Server.Arch}}'], { timeoutMs: 30_000 });
      return result.status === 0 ? result.stdout.trim() : undefined;
    },
    imageInfo(image) {
      const result = exec(['image', 'inspect', image], { timeoutMs: 60_000 });
      if (result.status !== 0) return undefined;
      const [meta] = JSON.parse(result.stdout);
      return { id: meta.Id, os: meta.Os, architecture: meta.Architecture };
    },
    build({ image, root = ROOT }) {
      assert(image.startsWith(`${CONTAINER_PREFIX.slice(0, -1)}-cpa-v8:`), 'image outside the harness namespace');
      must(
        ['build', '--platform', 'linux/amd64', '--target', 'runtime', '-f', 'Dockerfile.runtime', '-t', image, root],
        { timeoutMs: 1_800_000 }
      );
    },
    probeImage({ name, runId, image }) {
      owned.add(name);
      return must(identityProbeArgs({ name, runId, image }), { timeoutMs: 120_000 });
    },
    start(spec) {
      // Registered before the call: Docker can create the container and still fail.
      owned.add(assertOwnedName(spec.name));
      must(dockerRunArgs(spec), { timeoutMs: 120_000 });
    },
    port(name) {
      const out = must(['port', assertOwnedName(name), `${CPA_PORT}/tcp`], { timeoutMs: 30_000 });
      const port = /:(\d+)\s*$/m.exec(out)?.[1];
      assert(port, `no published port for ${name}: ${out}`);
      return Number(port);
    },
    restart(name) {
      must(['restart', '-t', '5', assertOwnedName(name)], { timeoutMs: 60_000 });
    },
    logs(name, lines = 40) {
      const result = exec(['logs', '--tail', String(lines), assertOwnedName(name)], { timeoutMs: 30_000 });
      return `${result.stdout}${result.stderr}`;
    },
    /** Removes exactly the names this run registered; returns what it could not remove. */
    releaseAll() {
      const failures = [];
      for (const name of owned) {
        const result = exec(['rm', '-f', assertOwnedName(name)], { timeoutMs: 60_000 });
        const detail = `${result.stderr}`.trim();
        if (result.status !== 0 && !/No such container/i.test(detail)) {
          failures.push(`${name}: ${detail || result.error?.message || result.status}`);
        }
      }
      owned.clear();
      return failures;
    },
  };
}

// --------------------------------------------------------------------------
// HTTP checks against a running CPA
// --------------------------------------------------------------------------

export function httpRequest({ port, method = 'GET', path: requestPath, headers = {}, body, timeoutMs = 10_000 }) {
  return new Promise((resolve, reject) => {
    const req = http.request(
      { host: '127.0.0.1', port, method, path: requestPath, headers, timeout: timeoutMs },
      (res) => {
        const chunks = [];
        res.on('data', (chunk) => chunks.push(chunk));
        res.on('end', () => resolve({ status: res.statusCode, headers: res.headers, body: Buffer.concat(chunks) }));
      }
    );
    req.on('timeout', () => req.destroy(new Error(`${method} ${requestPath} timed out`)));
    req.on('error', reject);
    req.end(body);
  });
}

export async function waitFor(probe, { what, timeoutMs = 60_000, intervalMs = 250 }) {
  const deadline = Date.now() + timeoutMs;
  let last;
  for (;;) {
    try {
      const value = await probe();
      if (value) return value;
      last = undefined;
    } catch (error) {
      last = error;
    }
    if (Date.now() >= deadline) {
      throw new Error(`timed out waiting for ${what}${last ? `: ${last.message}` : ''}`);
    }
    await new Promise((resolve) => setTimeout(resolve, intervalMs));
  }
}

const bearer = (key) => ({ authorization: `Bearer ${key}` });

/** Waits until CPA answers the management API with the key. */
export const waitForManagement = ({ port, key, request = httpRequest }) =>
  waitFor(
    async () =>
      (await request({ port, path: '/v0/management/config', headers: bearer(key) })).status === 200,
    { what: 'CPA management API' }
  );

/** (b) and the served half of (a): `/management.html` is the build, byte for byte. */
export async function checkServedPanel({ port, panel, request = httpRequest }) {
  const response = await request({ port, path: '/management.html' });
  assert.equal(response.status, 200, `GET /management.html answered ${response.status}, want 200`);
  assert(
    /^text\/html\b/i.test(String(response.headers['content-type'])),
    `GET /management.html content-type is ${response.headers['content-type']}`
  );
  assert(
    response.body.equals(panel),
    `served management.html (${response.body.length} bytes, sha256 ${sha256(response.body)}) is not byte-identical to the build (${panel.length} bytes, sha256 ${sha256(panel)})`
  );
  return { status: 200, bytes: response.body.length, sha256: sha256(response.body) };
}

/** (a) `static/management.html` equals the build byte for byte. */
export function checkStaticPanel({ staticPath, panel }) {
  assert(fs.existsSync(staticPath), 'static/management.html does not exist');
  const stored = fs.readFileSync(staticPath);
  assert(
    stored.equals(panel),
    `static/management.html (${stored.length} bytes, sha256 ${sha256(stored)}) is not byte-identical to the build (${panel.length} bytes, sha256 ${sha256(panel)})`
  );
  return { bytes: stored.length, sha256: sha256(stored) };
}

/**
 * (c) The v8 management key against `/v0/management/config`, the Lite contract:
 * no key and a wrong key are refused, the key is accepted, and a
 * `request-retry` update is applied and written to `config.yaml`.
 */
export async function checkManagementContract({
  port,
  key,
  configPath,
  request = httpRequest,
  persistTimeoutMs = 10_000,
}) {
  const read = async (headers) => request({ port, path: '/v0/management/config', headers });
  assert.equal((await read({})).status, 401, 'management API answered a request without a key');
  assert.equal((await read(bearer(`${key}-wrong`))).status, 401, 'management API accepted a wrong key');
  const config = await read(bearer(key));
  assert.equal(config.status, 200, `management key refused by /v0/management/config (${config.status})`);
  const parsed = JSON.parse(config.body.toString('utf8'));
  assert(parsed && typeof parsed === 'object' && !Array.isArray(parsed), '/v0/management/config is not a JSON object');

  const retry = async () => {
    const response = await request({ port, path: '/v0/management/request-retry', headers: bearer(key) });
    assert.equal(response.status, 200, `GET /v0/management/request-retry answered ${response.status}`);
    return JSON.parse(response.body.toString('utf8'))['request-retry'];
  };
  assert.equal(await retry(), RETRY_BEFORE, 'CPA did not load the configured request-retry');
  assert.equal(persistedRequestRetry(fs.readFileSync(configPath, 'utf8')), RETRY_BEFORE);

  const update = await request({
    port,
    method: 'PUT',
    path: '/v0/management/request-retry',
    headers: { ...bearer(key), 'content-type': 'application/json' },
    body: JSON.stringify({ value: RETRY_AFTER }),
  });
  assert.equal(update.status, 200, `PUT /v0/management/request-retry answered ${update.status}`);
  assert.equal(await retry(), RETRY_AFTER, 'CPA does not report the updated request-retry');
  await waitFor(() => persistedRequestRetry(fs.readFileSync(configPath, 'utf8')) === RETRY_AFTER, {
    what: 'request-retry to be written to config.yaml',
    timeoutMs: persistTimeoutMs,
  });
  return { authenticated: true, field: 'request-retry', from: RETRY_BEFORE, to: RETRY_AFTER, persisted: true };
}

/** After a CPA restart the updated field is read back from disk. */
export async function checkPersistedAfterRestart({ port, key, configPath, request = httpRequest }) {
  const response = await request({ port, path: '/v0/management/request-retry', headers: bearer(key) });
  assert.equal(response.status, 200, `GET /v0/management/request-retry answered ${response.status} after restart`);
  assert.equal(JSON.parse(response.body.toString('utf8'))['request-retry'], RETRY_AFTER);
  assert.equal(persistedRequestRetry(fs.readFileSync(configPath, 'utf8')), RETRY_AFTER);
  return { restarted: true, field: 'request-retry', value: RETRY_AFTER };
}

/** (d) `disable-control-panel: true`: the route is 404 although a bundle is stored. */
export async function checkDisabledPanel({ port, key, staticPath, panel, request = httpRequest }) {
  const response = await request({ port, path: '/management.html' });
  assert.equal(response.status, 404, `GET /management.html answered ${response.status} with the panel disabled, want 404`);
  const config = await request({ port, path: '/v0/management/config', headers: bearer(key) });
  assert.equal(config.status, 200, `disabling the panel must not disable the management API (${config.status})`);
  checkStaticPanel({ staticPath, panel });
  return { status: 404, managementApi: 200, storedBundleUntouched: true };
}

/** The stub saw exactly what a GitHub-backed CPA panel update asks for. */
export function checkStubTraffic({ events, stub }) {
  assert(events.some((e) => e.kind === 'connect' && e.target === `${STUB_HOST}:443`), 'CPA never connected to api.github.com through the stub');
  const requests = events.filter((e) => e.kind === 'request').map((e) => `${e.method} ${e.path}`);
  assert(requests.includes(`GET ${stub.latestPath}`), `CPA never asked for ${stub.latestPath}; saw ${JSON.stringify(requests)}`);
  assert(requests.includes(`GET ${stub.assetPath}`), `CPA never downloaded the management.html asset; saw ${JSON.stringify(requests)}`);
  const unexpected = requests.filter((r) => r !== `GET ${stub.latestPath}` && r !== `GET ${stub.assetPath}`);
  assert.deepEqual(unexpected, [], 'CPA asked the release stub for something other than releases/latest and its asset');
  return { latestRequested: stub.latestPath, assetDownloaded: true };
}

/** Stub requests and CONNECTs since `since`, i.e. what CPA actually asked the "GitHub" for. */
export const stubContacts = (events, since = 0) =>
  events.slice(since).filter((e) => e.kind === 'connect' || e.kind === 'request');

/**
 * Manual-install flow (`disable-auto-update-panel: true`): a hand-placed bundle is
 * served and left alone. The stub offers a different panel, so an update would
 * either show in the stored bytes or as a stub contact; neither may happen.
 */
export function checkPlacedPanelKept({ staticPath, panel, events, since }) {
  const contacts = stubContacts(events, since);
  assert.deepEqual(contacts, [], 'CPA contacted the release stub although disable-auto-update-panel is true');
  checkStaticPanel({ staticPath, panel });
  return { stubContacted: false, bytes: panel.length, sha256: sha256(panel) };
}

// --------------------------------------------------------------------------
// Acceptance flow
// --------------------------------------------------------------------------

export function parseArgs(argv) {
  const options = {
    bundleOnly: false,
    skipBuild: false,
    buildCpaImage: false,
    cpaImage: process.env.CPAMP_LITE_CPA_IMAGE || undefined,
    panel: DEFAULT_PANEL,
    panelSource: 'stub',
    layouts: [...LAYOUTS],
    repository: DEFAULT_REPOSITORY,
    report: undefined,
    help: false,
  };
  const value = (i, flag) => {
    if (i + 1 >= argv.length || argv[i + 1].startsWith('--')) throw new UsageError(`${flag} needs a value`);
    return argv[i + 1];
  };
  for (let i = 0; i < argv.length; i += 1) {
    const flag = argv[i];
    switch (flag) {
      case '--help': case '-h': options.help = true; break;
      case '--bundle-only': options.bundleOnly = true; break;
      case '--skip-build': options.skipBuild = true; break;
      case '--build-cpa-image': options.buildCpaImage = true; break;
      case '--cpa-image': options.cpaImage = value(i, flag); i += 1; break;
      case '--panel': options.panel = path.resolve(value(i, flag)); i += 1; break;
      case '--report': options.report = path.resolve(value(i, flag)); i += 1; break;
      case '--repository': options.repository = value(i, flag); i += 1; break;
      case '--panel-source': options.panelSource = value(i, flag); i += 1; break;
      case '--layouts': options.layouts = value(i, flag).split(',').filter(Boolean); i += 1; break;
      default: throw new UsageError(`unknown argument: ${flag}`);
    }
  }
  if (!PANEL_SOURCES.includes(options.panelSource)) {
    throw new UsageError(`--panel-source must be one of ${PANEL_SOURCES.join(', ')}`);
  }
  if (!options.layouts.length || options.layouts.some((l) => !LAYOUTS.includes(l))) {
    throw new UsageError(`--layouts must be a comma list of ${LAYOUTS.join(', ')}`);
  }
  if (!/^[\w.-]+\/[\w.-]+$/.test(options.repository)) {
    throw new UsageError('--repository must be owner/name');
  }
  return options;
}

const USAGE = `Usage:
  node bin/ci/panel-lite-cpa-v8.mjs --bundle-only [--skip-build]
  node bin/ci/panel-lite-cpa-v8.mjs [--cpa-image REF | --build-cpa-image] [--skip-build]
       [--panel-source stub|mounted] [--layouts legacy,v8] [--repository owner/name] [--report FILE]
`;

const redactor = (secrets) => (text) =>
  secrets.reduce((out, secret) => (secret ? out.split(secret).join('[redacted]') : out), String(text));

/**
 * The image must carry exactly the CPA the Dockerfile.runtime pin names. It is
 * inspected and probed by its id, so a tag that moves afterwards changes nothing.
 */
export function verifyImageIdentity({ docker, image, pin, runId }) {
  const info = docker.imageInfo(image);
  if (!info) throw new UsageError(`CPA image ${image} is not available locally (it is never pulled)`);
  assert(
    info.os === 'linux' && info.architecture === 'amd64',
    `CPA image ${image} is ${info.os}/${info.architecture}, want linux/amd64`
  );
  const out = docker.probeImage({ name: `${CONTAINER_PREFIX}${runId}-image-probe`, runId, image: info.id });
  // sha256sum line, the one-line cpa-artifact.json, then the first line of `-help`.
  const [digestLine, manifestLine, banner = ''] = String(out).split('\n');
  const digest = /^([0-9a-f]{64})\s/.exec(digestLine ?? '')?.[1];
  assert.equal(digest, pin.executableSha256, `${image} carries CPA ${digest}, not the Dockerfile.runtime pin ${pin.executableSha256}`);
  let manifest;
  try {
    manifest = JSON.parse(manifestLine);
  } catch {
    throw new Error(`cpa-artifact.json in ${image} is malformed: ${JSON.stringify(manifestLine)}`);
  }
  assert(manifest && typeof manifest === 'object' && !Array.isArray(manifest), 'cpa-artifact.json is not a JSON object');
  assert.equal(manifest.version, pin.version, 'cpa-artifact.json version differs from the pin');
  assert.equal(manifest.artifactId, `sha256:${pin.executableSha256}`, 'cpa-artifact.json artifactId differs from the pin');
  assert(banner.includes(`Version: ${pin.version}`), `cli-proxy-api reports "${banner.trim()}", want Version: ${pin.version}`);
  return { image, imageId: info.id, version: pin.version, executableSha256: digest };
}

export async function runLayout({ layout, runId, docker, image, stub, stubDir, panel, options, tmpRoot, secrets }) {
  const key = `cpampd-lite-key-${crypto.randomBytes(12).toString('hex')}`;
  secrets.push(key);
  const useStub = options.panelSource === 'stub';
  const uid = typeof process.getuid === 'function' ? process.getuid() : undefined;
  const gid = typeof process.getgid === 'function' ? process.getgid() : undefined;
  const repositoryUrl = `https://github.com/${options.repository}`;
  const proxyUrl = `http://host.docker.internal:${stub.port}`;

  const prepare = (role, flags) => {
    const dir = fs.mkdtempSync(path.join(tmpRoot, `${layout}-${role}-`));
    fs.mkdirSync(path.join(dir, 'auth'), { recursive: true });
    fs.mkdirSync(path.join(dir, 'static'), { recursive: true });
    const configPath = path.join(dir, 'config.yaml');
    fs.writeFileSync(configPath, renderCpaConfig({ layout, managementKey: key, repository: repositoryUrl, ...flags }));
    return { dir, configPath, staticPath: path.join(dir, 'static', 'management.html') };
  };
  const start = async (role, paths, withStub) => {
    const name = containerName(runId, layout, role);
    docker.start({ name, runId, image, configDir: paths.dir, stubDir: withStub ? stubDir : undefined, uid, gid });
    try {
      const port = docker.port(name);
      await waitForManagement({ port, key });
      return { name, port };
    } catch (error) {
      throw new Error(`${error.message}\n--- ${name} log ---\n${docker.logs(name)}`);
    }
  };

  const result = { layout };
  const settle = () => new Promise((resolve) => setTimeout(resolve, SETTLE_MS));

  // Panel enabled. In stub mode CPA obtains management.html from the release stub; in
  // mounted mode the file is placed by hand, `disable-auto-update-panel` is true and
  // the stub (routed through `proxy-url`, serving a different "1.x" panel) must stay
  // silent: the documented manual-install flow.
  const enabled = prepare('enabled', {
    disableControlPanel: false,
    disableAutoUpdatePanel: !useStub,
    proxyUrl,
  });
  if (!useStub) fs.writeFileSync(enabled.staticPath, panel.bytes);
  const eventsBefore = stub.events.length;
  const cpa = await start('enabled', enabled, true);
  let { port } = cpa;
  try {
    await waitFor(async () => (await httpRequest({ port, path: '/management.html' })).status === 200, {
      what: 'CPA to serve /management.html',
    });
  } catch (error) {
    throw new Error(`${error.message}\n--- ${cpa.name} log ---\n${docker.logs(cpa.name)}`);
  }
  result.enabled = {
    panelSource: options.panelSource,
    static: checkStaticPanel({ staticPath: enabled.staticPath, panel: panel.bytes }),
    served: await checkServedPanel({ port, panel: panel.bytes }),
  };
  if (useStub) {
    result.enabled.releaseStub = checkStubTraffic({ events: stub.events.slice(eventsBefore), stub });
  } else {
    await settle();
    result.enabled.placedPanelKept = checkPlacedPanelKept({
      staticPath: enabled.staticPath,
      panel: panel.bytes,
      events: stub.events,
      since: eventsBefore,
    });
  }
  result.enabled.management = await checkManagementContract({ port, key, configPath: enabled.configPath });
  docker.restart(cpa.name);
  port = docker.port(cpa.name);
  await waitForManagement({ port, key });
  result.enabled.afterRestart = await checkPersistedAfterRestart({ port, key, configPath: enabled.configPath });
  if (!useStub) {
    await settle();
    result.enabled.placedPanelKeptAfterRestart = checkPlacedPanelKept({
      staticPath: enabled.staticPath,
      panel: panel.bytes,
      events: stub.events,
      since: eventsBefore,
    });
  }
  result.enabled.servedAfterRestart = await checkServedPanel({ port, panel: panel.bytes });

  // Panel disabled: the bundle is present and the stub reachable, yet no route.
  const disabled = prepare('disabled', {
    disableControlPanel: true,
    disableAutoUpdatePanel: false,
    proxyUrl,
  });
  fs.writeFileSync(disabled.staticPath, panel.bytes);
  const disabledBefore = stub.events.length;
  const off = await start('disabled', disabled, true);
  // The enabled run downloads within seconds of startup; the same window must stay silent here.
  await settle();
  result.disabled = await checkDisabledPanel({
    port: off.port,
    key,
    staticPath: disabled.staticPath,
    panel: panel.bytes,
  });
  assert.deepEqual(
    stubContacts(stub.events, disabledBefore),
    [],
    'CPA contacted the release stub although the control panel is disabled'
  );
  result.disabled.releaseStubContacted = false;
  return result;
}

/**
 * The whole acceptance run. The collaborators are injectable so the failure and
 * signal paths can be driven without Docker: `docker` (see `createDocker`),
 * `stubFactory` / `certificateFactory`, `layoutRunner`, `build`, the `signals`
 * emitter whose SIGINT/SIGTERM are handled, and `exit` called after a signal.
 */
export async function runAcceptance(
  argv,
  {
    stdout = process.stdout,
    stderr = process.stderr,
    docker = createDocker(),
    stubFactory = createReleaseStub,
    certificateFactory = generateStubCertificate,
    layoutRunner = runLayout,
    build = buildPanel,
    signals = process,
    exit = (code) => process.exit(code),
    tmpBase = os.tmpdir(),
  } = {}
) {
  let options;
  try {
    options = parseArgs(argv);
  } catch (error) {
    if (!(error instanceof UsageError)) throw error;
    stderr.write(`${error.message}\n${USAGE}`);
    return 2;
  }
  if (options.help) {
    stdout.write(USAGE);
    return 0;
  }

  const secrets = [];
  const redact = redactor(secrets);
  const report = { contract: CONTRACT, verdict: 'fail', startedAt: new Date().toISOString() };
  const runId = crypto.randomBytes(4).toString('hex');
  let tmpRoot;
  let stub;
  let exitCode = 1;
  const release = async () => {
    const failures = docker.releaseAll();
    if (stub) {
      try {
        await stub.close();
      } catch (error) {
        failures.push(`stub: ${error.message}`);
      }
    }
    stub = undefined;
    if (tmpRoot) fs.rmSync(tmpRoot, { recursive: true, force: true });
    tmpRoot = undefined;
    return failures;
  };
  const onSignal = (signal) => async () => {
    stderr.write(`${signal}: releasing ${CONTAINER_PREFIX}${runId}-* resources\n`);
    await release();
    exit(signal === 'SIGINT' ? 130 : 143);
  };
  const handlers = [['SIGINT', onSignal('SIGINT')], ['SIGTERM', onSignal('SIGTERM')]];
  for (const [signal, handler] of handlers) signals.on(signal, handler);

  try {
    if (!options.skipBuild) build();
    const panel = loadPanel(options.panel);
    report.panel = { path: path.relative(ROOT, panel.path), bytes: panel.size, sha256: panel.sha256, singleFile: true };

    if (!options.bundleOnly) {
      const pin = loadCpaV8Pin();
      report.cpaPin = pin;
      const serverPlatform = docker.available();
      if (!serverPlatform) throw new UsageError('docker is not available');

      let image = options.cpaImage;
      if (!image) {
        const candidate = defaultImageTag(pin);
        if (docker.imageInfo(candidate)) image = candidate;
        else if (options.buildCpaImage) {
          docker.build({ image: candidate });
          image = candidate;
        } else {
          throw new UsageError(
            `no CPA v8 image: pass --cpa-image REF, set CPAMP_LITE_CPA_IMAGE, or use --build-cpa-image (builds ${candidate})`
          );
        }
      }
      report.cpa = verifyImageIdentity({ docker, image, pin, runId });
      // Every container runs the image id that was verified, never the mutable tag.
      const imageId = report.cpa.imageId;

      tmpRoot = fs.mkdtempSync(path.join(tmpBase, `${CONTAINER_PREFIX}${runId}-`));
      const stubDir = path.join(tmpRoot, 'stub');
      fs.mkdirSync(stubDir);
      const certificate = certificateFactory();
      fs.writeFileSync(path.join(stubDir, 'ca.pem'), certificate.cert, { mode: 0o644 });
      // In mounted mode the stub offers a different, "1.x" panel: if CPA ever updated
      // the placed file, the bytes would change and the checks would see it.
      const stubPanel = options.panelSource === 'stub' ? panel.bytes : DECOY_1X_PANEL;
      stub = await stubFactory({ panel: stubPanel, repository: options.repository, certificate });
      report.layouts = [];
      for (const layout of options.layouts) {
        stderr.write(`[panel-lite] layout=${layout} source=${options.panelSource}\n`);
        report.layouts.push(
          await layoutRunner({ layout, runId, docker, image: imageId, stub, stubDir, panel, options, tmpRoot, secrets })
        );
      }
    }
    report.verdict = 'pass';
    exitCode = 0;
  } catch (error) {
    report.error = redact(error instanceof Error ? error.message : String(error));
    exitCode = error instanceof UsageError ? 2 : 1;
  } finally {
    const failures = await release();
    for (const [signal, handler] of handlers) signals.off(signal, handler);
    report.cleanup = { ok: failures.length === 0, failures: failures.map(redact), runId };
    if (failures.length) exitCode = 1;
  }
  report.finishedAt = new Date().toISOString();
  if (report.verdict === 'pass' && !report.cleanup.ok) report.verdict = 'fail';
  const text = `${JSON.stringify(report, null, 2)}\n`;
  stdout.write(text);
  if (options.report) fs.writeFileSync(options.report, text);
  return exitCode;
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  process.exitCode = await runAcceptance(process.argv.slice(2));
}
