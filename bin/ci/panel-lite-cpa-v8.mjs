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
//      downloads that file from a per-run release server (a container of the same
//      verified image that answers for `api.github.com` with a throw-away
//      certificate) -- or, with `--panel-source mounted`, serves the file placed
//      at the storage location by hand.
//   2. The stored bundle equals the build byte for byte, and `GET
//      /management.html` answers 200 with those same bytes.
//   3. The Lite panel contract holds on CPA v8: the v8 management key
//      authenticates `/v0/management/config` (and a missing or wrong key does
//      not), a `PUT /v0/management/request-retry` -- the call the panel's
//      configuration page makes -- is written to `config.yaml` on disk and
//      survives a CPA restart.
//   4. With `disable-control-panel: true`, `GET /management.html` is 404 even
//      though a bundle is present, and CPA never asks the release server.
//
// Two independent axes are exercised, and each case report names both:
//
//   configLayout    `legacy` (`remote-management:` YAML schema) and `v8`
//                   (config-version 8, `management:` schema).
//   staticPathMode  `config-sibling-static`: the bundle lives at
//                   `<config dir>/static/management.html` (CPA's default).
//                   `explicit-management-static-path`: `MANAGEMENT_STATIC_PATH`
//                   names `/panel-static`, mounted from a directory outside
//                   `<config dir>/static`, which holds a decoy that is never
//                   served nor overwritten.
//
// Isolation: the run creates one `--internal` Docker network
// (`cpampd-lite-<run>-net`) and puts every container it starts on it. An
// internal network has no default route, so a container on it cannot reach any
// address outside that network: the release server is reachable because it is
// on the same network, and the internet is not. Nothing is published on a host
// port -- every HTTP check runs inside the CPA container through `docker exec`,
// in the very network namespace the acceptance claims something about. Two CPAs
// that cannot obtain a panel -- one whose release server address is unreachable,
// one whose release server is reachable but serves none -- prove CPA does not
// fall back to the internet, and an in-container reachability probe proves the
// boundary itself: the release server on this run's network is reachable while
// public addresses are not.
//
// Docker ownership: every container and network is labelled
// `cpamp.panel-lite.run=<runToken>` with a per-run CSPRNG token that is not
// reused and appears nowhere in the names. `releaseAll()` inspects that label
// before removing anything and leaves any foreign object in place, so a name
// conflict can never turn into "remove someone else's container".
//
// The CPA executable is the one `Dockerfile.runtime` pins (`cpa-v8-download`
// stage): the harness reads the version and amd64 executable sha256 from that
// file, refuses any image whose `/usr/local/bin/cli-proxy-api` differs, and so
// follows a re-pin without edits. `--build-cpa-image` is an optional build phase
// that may fetch build dependencies; it is reported separately from the isolated
// runtime phase and is not part of the isolation claim.
//
// Everything the run created is released on success, on failure and on
// SIGINT/SIGTERM, through a single idempotent cleanup; a release that fails --
// Docker, the release server or the temporary directory -- is reported in
// `cleanup.failures` and makes the run exit non-zero.
//
// Usage:
//   node bin/ci/panel-lite-cpa-v8.mjs --bundle-only [--skip-build]
//   node bin/ci/panel-lite-cpa-v8.mjs [--cpa-image REF | --build-cpa-image]
//        [--skip-build] [--panel-source stub|mounted] [--layouts legacy,v8]
//        [--static-path-modes config-sibling-static,explicit-management-static-path]
//        [--repository owner/name] [--report FILE]
//
// `--cpa-image` falls back to `CPAMP_LITE_CPA_IMAGE`, then to the local image
// `cpampd-lite-cpa-v8:<version>-<sha12>` that `--build-cpa-image` produces from
// the `runtime` target of `Dockerfile.runtime`. Exit codes: 0 pass, 1 failed
// check, 2 usage or missing prerequisite.
//
// The `node --test` suite runs in the PR Check Frontend job; the Docker
// acceptance is a manual gate, not a CI job. Keep the `--report` JSON with the
// two commands it was produced by (`--panel-source stub` and `mounted`), the CPA
// image id and the exit code: that is the evidence, and it contains no secret
// (the management key is redacted from every reported text).

import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import crypto from 'node:crypto';
import fs from 'node:fs';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

export const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');
export const DEFAULT_PANEL = path.join(ROOT, 'apps', 'web', 'dist', 'panel', 'management.html');
export const BUILD_PANEL_COMMAND = ['npm', '--workspace', 'apps/web', 'run', 'build:panel'];
export const CONTRACT = 'cpamp.panel-lite-cpa-v8.v2';

export const LAYOUTS = ['legacy', 'v8'];
export const STATIC_PATH_MODES = ['config-sibling-static', 'explicit-management-static-path'];
export const PANEL_SOURCES = ['stub', 'mounted'];
export const DEFAULT_REPOSITORY = 'seakee/CPA-Manager-Plus';

export const CONTAINER_PREFIX = 'cpampd-lite-';
const OWNED_NAME = /^cpampd-lite-[0-9a-f]{8}-[a-z0-9-]{1,40}$/;
const RUN_TOKEN = /^[0-9a-f]{32}$/;
const IPV4 = /^\d{1,3}(?:\.\d{1,3}){3}$/;
const CPA_PORT = 8317;
const CPA_BINARY = '/usr/local/bin/cli-proxy-api';
const CPA_MANIFEST = '/usr/local/share/cpamp/runtime/cpa-artifact.json';
const CONFIG_MOUNT = '/cpa';
const PANEL_MOUNT = '/panel-static';
const RELEASE_MOUNT = '/cpampd-lite-release';
const CERT_MOUNT = '/cpampd-lite-certs';
const CA_MOUNT = '/cpampd-lite-stub/ca.pem';
const RELEASE_HOST = 'api.github.com';
const RELEASE_PORT = 443;
const RELEASE_ASSET_PATH = '/cpampd-lite/assets/management.html';
export const RUN_LABEL = 'cpamp.panel-lite.run';
export const STUB_TAG = 'v2.0.0-beta.0-cpampd-lite-stub';

// The Lite panel's configuration page writes through
// `apiClient.put('/request-retry', { value })`; the harness drives the same call.
const RETRY_BEFORE = 3;
const RETRY_AFTER = 11;

// How long a CPA that is going to contact the release server takes to do so
// after it is up; the enabled run observes the download well inside this window.
const SETTLE_MS = 6_000;

// What the release server offers in mounted mode, where nothing may fetch it.
export const DECOY_1X_PANEL = Buffer.from('<!doctype html><html><head><style>a{}</style></head><body><script>/* 1.x decoy */</script></body></html>');
// What sits at `<config dir>/static/management.html` when `MANAGEMENT_STATIC_PATH`
// names another directory: it must neither be served nor be overwritten.
export const DECOY_CONFIG_STATIC_PANEL = Buffer.from('<!doctype html><html><head><style>a{}</style></head><body><script>/* decoy outside the configured static path */</script></body></html>');

// Public addresses a CPA container must not reach. The raw addresses cannot be
// explained away by DNS, and the names are the panel host and the documented
// upstream fallback. `api.github.com` is deliberately absent: the container's
// own host entry answers it from this run's release server, so it can never
// carry internet traffic, and a "blocked" result there would mean nothing.
export const EGRESS_TARGETS = [
  '1.1.1.1:443',
  '93.184.216.34:443',
  'github.com:443',
  'cpamc.router-for.me:443',
];

// Fixed scripts run inside the container through `docker exec`. Values travel
// as separate argv entries, never interpolated into the script text.
const HTTP_PROBE_SCRIPT = [
  'set -u',
  'method=$1; target=$2; headers=$3; body=$4; req=/tmp/cpampd-lite-http.request; res=/tmp/cpampd-lite-http.response',
  ': > "$req"',
  'if [ -n "$body" ]; then printf \'%s\' "$body" | base64 -d > "$req"; fi',
  'length=$(wc -c < "$req" | tr -d \' \')',
  'exec 3<>/dev/tcp/127.0.0.1/8317 || exit 90',
  '{',
  '  printf \'%s %s HTTP/1.1\\r\\n\' "$method" "$target"',
  '  printf \'Host: 127.0.0.1:8317\\r\\n\'',
  '  printf \'Connection: close\\r\\n\'',
  '  printf \'Content-Length: %s\\r\\n\' "$length"',
  '  if [ -n "$headers" ]; then printf \'%s\' "$headers" | base64 -d; fi',
  '  printf \'\\r\\n\'',
  '  cat "$req"',
  '} >&3',
  'cat <&3 > "$res"',
  'exec 3>&-',
  'base64 -w0 "$res"',
  'rm -f "$req" "$res"',
].join('\n');

const EGRESS_PROBE_SCRIPT = [
  'set -u',
  'for target in "$@"; do',
  '  host=${target%:*}; port=${target##*:}',
  '  if timeout 5 bash -c "exec 3<>/dev/tcp/$host/$port" 2>/dev/null; then',
  '    printf \'open %s\\n\' "$target"',
  '  else',
  '    printf \'blocked %s\\n\' "$target"',
  '  fi',
  'done',
].join('\n');

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
 * layout and the v8 `management:` layout. The release server is reached by
 * addressing, not by a proxy setting: the container's `api.github.com` host entry
 * points at it, so no `proxy-url` participates in the acceptance.
 */
export function renderCpaConfig({
  layout,
  managementKey,
  repository,
  disableControlPanel,
  disableAutoUpdatePanel,
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
      `request-retry: ${requestRetry}`,
      'remote-management:',
      ...management('  ')
    );
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
      `    request-retry: ${requestRetry}`,
      'observability:',
      '  logs:',
      '    logging-to-file: false'
    );
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
// Release server: the per-run container that answers for api.github.com
// --------------------------------------------------------------------------

export function releaseDocument({ repository, tag = STUB_TAG, panel }) {
  assert(/^[\w.-]+\/[\w.-]+$/.test(repository), `bad repository: ${repository}`);
  return {
    url: `https://${RELEASE_HOST}/repos/${repository}/releases/latest`,
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
        browser_download_url: `https://${RELEASE_HOST}${RELEASE_ASSET_PATH}`,
      },
    ],
  };
}

/** The two request paths a GitHub-backed CPA panel update asks the release server for. */
export function releasePaths(repository) {
  assert(/^[\w.-]+\/[\w.-]+$/.test(repository), `bad repository: ${repository}`);
  return { latest: `/repos/${repository}/releases/latest`, asset: RELEASE_ASSET_PATH };
}

/**
 * The files `openssl s_server -WWW` serves, taken from its connection log. Each
 * request the release server answered leaves one `FILE:<path>` line, so this is
 * the record of what CPA actually asked for.
 */
export function releaseRequests(log) {
  return String(log)
    .split('\n')
    .map((line) => /^FILE:(.+)$/.exec(line.trim())?.[1])
    .filter(Boolean)
    .map((file) => `/${file}`);
}

/**
 * Writes what the release server offers: the `releases/latest` document, the
 * panel asset and a self-signed certificate for `api.github.com`. `openssl
 * s_server -WWW` serves the tree as files, so the request paths are directories
 * and file names.
 */
export function writeReleaseTree({ docRoot, repository, panel }) {
  const { latest, asset } = releasePaths(repository);
  const latestFile = path.join(docRoot, latest.slice(1));
  const assetFile = path.join(docRoot, asset.slice(1));
  fs.mkdirSync(path.dirname(latestFile), { recursive: true });
  fs.mkdirSync(path.dirname(assetFile), { recursive: true });
  fs.writeFileSync(latestFile, `${JSON.stringify(releaseDocument({ repository, panel }))}\n`);
  fs.writeFileSync(assetFile, panel);
  return { latestFile, assetFile };
}

/** A self-signed server certificate for api.github.com, valid for one day. */
export function generateReleaseCertificate({ exec = defaultOpensslExec } = {}) {
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
        `CN = ${RELEASE_HOST}`,
        '[v3]',
        `subjectAltName = DNS:${RELEASE_HOST}`,
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
        `openssl is required to create the release server certificate: ${result.stderr || result.error?.message || result.status}`
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

export function assertRunToken(token) {
  assert(
    typeof token === 'string' && RUN_TOKEN.test(token),
    `refusing a run token that is not a per-run CSPRNG value: ${token}`
  );
  return token;
}

/** `legacy`/`v8` plus the static path mode, as a single name-safe case id. */
export const caseId = (configLayout, staticPathMode) =>
  `${configLayout}-${staticPathMode === 'explicit-management-static-path' ? 'explicit' : 'sibling'}`;

export const containerName = (runId, caseName, role) =>
  assertOwnedName(`${CONTAINER_PREFIX}${runId}-${caseName}-${role}`);

export const networkName = (runId) => assertOwnedName(`${CONTAINER_PREFIX}${runId}-net`);

function assertMountSource(source) {
  assert(
    path.isAbsolute(source) && !/[:,\n]/.test(source),
    `unsafe bind-mount source: ${source}`
  );
}

const assertAddress = (address) => {
  assert(IPV4.test(address), `release server address is not an IPv4 address: ${address}`);
  return address;
};

/**
 * argv of the long-running CPA container. It joins this run's internal network
 * and resolves `api.github.com` to the release server on that network; nothing
 * is published on the host, so the management API is reached through
 * `docker exec` inside this container.
 */
export function dockerRunArgs({
  name,
  runId,
  runToken,
  image,
  network,
  configDir,
  panelDir,
  releaseAddress,
  caPath,
  uid,
  gid,
}) {
  assertOwnedName(name);
  assertOwnedName(network);
  assertRunToken(runToken);
  assert(/^[0-9a-f]{8}$/.test(runId), 'bad run id');
  assertMountSource(configDir);
  assertMountSource(caPath);
  const args = [
    'run', '-d', '--pull', 'never', '--platform', 'linux/amd64',
    '--name', name,
    '--label', `${RUN_LABEL}=${runToken}`,
    '--network', network,
    '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges',
    '--add-host', `${RELEASE_HOST}:${assertAddress(releaseAddress)}`,
    '-e', `SSL_CERT_FILE=${CA_MOUNT}`,
    '-v', `${configDir}:${CONFIG_MOUNT}`,
    '-v', `${caPath}:${CA_MOUNT}:ro`,
  ];
  if (panelDir) {
    assertMountSource(panelDir);
    args.push('-e', `MANAGEMENT_STATIC_PATH=${PANEL_MOUNT}`, '-v', `${panelDir}:${PANEL_MOUNT}`);
  }
  if (uid !== undefined) args.push('--user', `${uid}:${gid}`);
  // `-local-model` keeps CPA from fetching its remote model catalogs.
  args.push('--entrypoint', CPA_BINARY, image, '-config', `${CONFIG_MOUNT}/config.yaml`, '-local-model');
  assert(!args.some((arg) => /18317/.test(arg)), 'the harness never uses port 18317');
  return args;
}

/** argv of the per-run release server: a TLS file server for api.github.com. */
export function releaseServerArgs({ name, runToken, image, network, docRoot, certDir }) {
  assertOwnedName(name);
  assertOwnedName(network);
  assertRunToken(runToken);
  assertMountSource(docRoot);
  assertMountSource(certDir);
  return [
    'run', '-d', '--pull', 'never', '--platform', 'linux/amd64',
    '--name', name,
    '--label', `${RUN_LABEL}=${runToken}`,
    '--network', network,
    '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges',
    '--workdir', RELEASE_MOUNT,
    '-v', `${docRoot}:${RELEASE_MOUNT}:ro`,
    '-v', `${certDir}:${CERT_MOUNT}:ro`,
    '--entrypoint', 'openssl', image,
    's_server', '-accept', String(RELEASE_PORT),
    '-cert', `${CERT_MOUNT}/cert.pem`,
    '-key', `${CERT_MOUNT}/key.pem`,
    '-WWW',
  ];
}

/** argv of the throw-away container that reads the image's CPA identity. */
export function identityProbeArgs({ name, runToken, image }) {
  assertOwnedName(name);
  assertRunToken(runToken);
  return [
    'run', '--rm', '--pull', 'never', '--platform', 'linux/amd64',
    '--name', name,
    '--label', `${RUN_LABEL}=${runToken}`,
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

/**
 * The Docker surface this harness uses, restricted to `cpampd-lite-*` names and
 * to objects labelled with this run's token. `releaseAll()` reads the label of
 * every object it is about to remove and leaves foreign objects untouched, so a
 * name conflict is reported instead of deleting someone else's resource.
 */
export function createDocker({ exec = defaultDockerExec, runToken } = {}) {
  assertRunToken(runToken);
  const owned = new Set();
  const networks = new Set();
  const must = (args, options) => {
    const result = exec(args, options);
    if (result.status !== 0) {
      const detail = (result.stderr || result.error?.message || '').trim();
      throw new Error(`docker ${args[0]} failed (${result.status}): ${detail}`);
    }
    return result.stdout.trim();
  };
  const labelOf = (kind, name) => {
    const format = kind === 'network' ? '{{index .Labels "cpamp.panel-lite.run"}}' : '{{index .Config.Labels "cpamp.panel-lite.run"}}';
    const args = kind === 'network'
      ? ['network', 'inspect', '--format', format, name]
      : ['inspect', '--format', format, name];
    const result = exec(args, { timeoutMs: 60_000 });
    if (result.status !== 0) return { exists: false };
    return { exists: true, token: String(result.stdout).trim() };
  };
  return {
    owned,
    networks,
    runToken,
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
    createNetwork(name) {
      must(['network', 'create', '--internal', '--label', `${RUN_LABEL}=${runToken}`, assertOwnedName(name)], {
        timeoutMs: 60_000,
      });
      networks.add(name);
    },
    probeImage({ name, image }) {
      owned.add(assertOwnedName(name));
      return must(identityProbeArgs({ name, runToken, image }), { timeoutMs: 120_000 });
    },
    start({ name, args }) {
      // Registered before the call: Docker can create the container and still fail.
      owned.add(assertOwnedName(name));
      must(args, { timeoutMs: 120_000 });
    },
    address(name) {
      const out = must(
        ['inspect', '--format', '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}', assertOwnedName(name)],
        { timeoutMs: 30_000 }
      );
      return assertAddress(out);
    },
    execInContainer(args, { timeoutMs = 30_000 } = {}) {
      return must(args, { timeoutMs });
    },
    restart(name) {
      must(['restart', '-t', '5', assertOwnedName(name)], { timeoutMs: 60_000 });
    },
    logs(name, lines = 40) {
      const result = exec(['logs', '--tail', String(lines), assertOwnedName(name)], { timeoutMs: 30_000 });
      return `${result.stdout}${result.stderr}`;
    },
    /**
     * Removes exactly the objects this run created. Ownership is the run label,
     * not the name: an object whose label is missing or different is reported and
     * left in place, and an object that is already gone is not an error.
     */
    releaseAll() {
      const failures = [];
      for (const name of owned) {
        const label = labelOf('container', name);
        if (!label.exists) continue;
        if (label.token !== runToken) {
          failures.push(`${name}: carries ${RUN_LABEL}="${label.token}", not this run's token; left in place`);
          continue;
        }
        const result = exec(['rm', '-f', name], { timeoutMs: 60_000 });
        const detail = `${result.stderr}`.trim();
        if (result.status !== 0 && !/No such container/i.test(detail)) {
          failures.push(`${name}: ${detail || result.error?.message || result.status}`);
        }
      }
      owned.clear();
      for (const name of networks) {
        const label = labelOf('network', name);
        if (!label.exists) continue;
        if (label.token !== runToken) {
          failures.push(`${name}: carries ${RUN_LABEL}="${label.token}", not this run's token; left in place`);
          continue;
        }
        const result = exec(['network', 'rm', name], { timeoutMs: 60_000 });
        const detail = `${result.stderr}`.trim();
        if (result.status !== 0 && !/No such network/i.test(detail)) {
          failures.push(`${name}: ${detail || result.error?.message || result.status}`);
        }
      }
      networks.clear();
      return failures;
    },
  };
}

// --------------------------------------------------------------------------
// HTTP: host-side transport for unit checks, `docker exec` transport for a CPA
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

/** Decodes `Transfer-Encoding: chunked`, which CPA uses for JSON responses. */
export function decodeChunked(body) {
  const chunks = [];
  let offset = 0;
  for (;;) {
    const lineEnd = body.indexOf('\r\n', offset);
    assert(lineEnd >= 0, 'chunked response ended before a chunk size');
    const size = Number.parseInt(body.subarray(offset, lineEnd).toString('latin1').split(';')[0].trim(), 16);
    assert(Number.isInteger(size), `chunked response has an unparseable chunk size: ${body.subarray(offset, lineEnd)}`);
    offset = lineEnd + 2;
    if (size === 0) return Buffer.concat(chunks);
    assert(offset + size <= body.length, 'chunked response ended mid-chunk');
    chunks.push(body.subarray(offset, offset + size));
    offset += size + 2;
  }
}

/** Decodes what the in-container probe printed: a base64 HTTP/1.1 response. */
export function parseContainerResponse(stdout) {
  const raw = Buffer.from(String(stdout).trim(), 'base64');
  const split = raw.indexOf('\r\n\r\n');
  assert(split >= 0, 'the in-container HTTP probe returned no header terminator');
  const head = raw.subarray(0, split).toString('latin1');
  const [statusLine, ...headerLines] = head.split('\r\n');
  const status = Number(/^HTTP\/1\.[01] (\d{3})/.exec(statusLine)?.[1]);
  assert(Number.isInteger(status), `unparseable status line from the in-container probe: ${statusLine}`);
  const headers = {};
  for (const line of headerLines) {
    const at = line.indexOf(':');
    if (at > 0) headers[line.slice(0, at).trim().toLowerCase()] = line.slice(at + 1).trim();
  }
  const body = raw.subarray(split + 4);
  return {
    status,
    headers,
    body: /\bchunked\b/i.test(String(headers['transfer-encoding'] ?? '')) ? decodeChunked(body) : body,
  };
}

/**
 * An HTTP client that speaks to CPA through `docker exec`, inside the container
 * that holds the CPA process and therefore inside the network this run isolated.
 */
export function createExecRequest({ docker, container }) {
  assertOwnedName(container);
  return async ({ method = 'GET', path: requestPath, headers = {}, body }) => {
    const headerBlock = Object.entries(headers)
      .map(([name, value]) => `${name}: ${value}\r\n`)
      .join('');
    const stdout = docker.execInContainer([
      'exec', container, 'bash', '-c', HTTP_PROBE_SCRIPT, 'cpampd-lite-http',
      method,
      requestPath,
      Buffer.from(headerBlock).toString('base64'),
      body === undefined ? '' : Buffer.from(body).toString('base64'),
    ]);
    return parseContainerResponse(stdout);
  };
}

/** Runs the reachability probe inside `container` and returns open/blocked targets. */
export function runEgressProbe({ docker, container, targets = EGRESS_TARGETS }) {
  assertOwnedName(container);
  const stdout = docker.execInContainer([
    'exec', container, 'bash', '-c', EGRESS_PROBE_SCRIPT, 'cpampd-lite-probe',
    ...targets,
  ]);
  const open = [];
  const blocked = [];
  for (const line of String(stdout).split('\n')) {
    const match = /^(open|blocked) (\S+)$/.exec(line.trim());
    if (!match) continue;
    (match[1] === 'open' ? open : blocked).push(match[2]);
  }
  assert(open.length + blocked.length === targets.length, `the reachability probe answered for ${open.length + blocked.length} of ${targets.length} targets`);
  return { open, blocked };
}

/**
 * The isolation claim, stated as what was observed: the release server on this
 * run's network is reachable, and no public address is. A blocked set that is
 * missing an entry is a failure; a reachable release server is the positive
 * control that the probe itself works.
 */
export function checkEgressIsolation({ open, blocked, release, targets = EGRESS_TARGETS }) {
  const releaseTarget = `${assertAddress(release)}:${RELEASE_PORT}`;
  assert(
    open.includes(releaseTarget),
    `the release server at ${releaseTarget} was not reachable from the run network, so the reachability result says nothing: open=${JSON.stringify(open)}`
  );
  for (const target of targets) {
    assert(
      blocked.includes(target),
      `the CPA container reached ${target}; outbound traffic on the run network is not denied: open=${JSON.stringify(open)}`
    );
  }
  return { releaseServerReachable: releaseTarget, blocked: [...targets] };
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

/**
 * (a) The stored bundle equals the build byte for byte.
 *
 * With `MANAGEMENT_STATIC_PATH` the effective location is outside
 * `<config dir>/static`; `decoyPath` is the file that must then stay exactly as
 * it was placed, because a CPA that fell back to the default location would
 * either serve it or overwrite it.
 */
export function checkStaticPanel({ staticPath, panel, decoyPath, decoy }) {
  assert(fs.existsSync(staticPath), `${staticPath} does not exist`);
  const stored = fs.readFileSync(staticPath);
  assert(
    stored.equals(panel),
    `${staticPath} (${stored.length} bytes, sha256 ${sha256(stored)}) is not byte-identical to the build (${panel.length} bytes, sha256 ${sha256(panel)})`
  );
  const result = { bytes: stored.length, sha256: sha256(stored) };
  if (decoyPath) {
    assert(fs.existsSync(decoyPath), `the decoy bundle at ${decoyPath} disappeared`);
    assert(
      fs.readFileSync(decoyPath).equals(decoy),
      `CPA wrote to ${decoyPath} although MANAGEMENT_STATIC_PATH names another directory`
    );
    result.decoyUntouched = true;
  }
  return result;
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
export async function checkDisabledPanel({ port, key, staticPath, panel, decoyPath, decoy, request = httpRequest }) {
  const response = await request({ port, path: '/management.html' });
  assert.equal(response.status, 404, `GET /management.html answered ${response.status} with the panel disabled, want 404`);
  const config = await request({ port, path: '/v0/management/config', headers: bearer(key) });
  assert.equal(config.status, 200, `disabling the panel must not disable the management API (${config.status})`);
  checkStaticPanel({ staticPath, panel, decoyPath, decoy });
  return { status: 404, managementApi: 200, storedBundleUntouched: true };
}

/** The release server served exactly the two requests a panel update makes. */
export function checkReleaseDownload({ requests, since = 0, paths }) {
  const seen = requests.slice(since);
  assert(
    seen.includes(paths.latest),
    `the release server never served ${paths.latest}; it served ${JSON.stringify(seen)}`
  );
  assert(
    seen.includes(paths.asset),
    `the release server never served the management.html asset; it served ${JSON.stringify(seen)}`
  );
  const unexpected = seen.filter((requestPath) => requestPath !== paths.latest && requestPath !== paths.asset);
  assert.deepEqual(unexpected, [], 'the release server served something other than releases/latest and its asset');
  return { latestServed: true, assetServed: true, requests: seen };
}

/**
 * Manual-install flow (`disable-auto-update-panel: true`): a hand-placed bundle is
 * served and left alone. The release server offers a different panel, so an
 * update would either show in the stored bytes or as a served request; neither
 * may happen.
 */
export function checkPlacedPanelKept({ staticPath, panel, decoyPath, decoy, requests, since }) {
  assert.deepEqual(
    requests.slice(since),
    [],
    'CPA asked the release server for a panel although disable-auto-update-panel is true'
  );
  const stored = checkStaticPanel({ staticPath, panel, decoyPath, decoy });
  return { ...stored, releaseServerRequests: 0 };
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
    staticPathModes: [...STATIC_PATH_MODES],
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
      case '--static-path-modes': options.staticPathModes = value(i, flag).split(',').filter(Boolean); i += 1; break;
      default: throw new UsageError(`unknown argument: ${flag}`);
    }
  }
  if (!PANEL_SOURCES.includes(options.panelSource)) {
    throw new UsageError(`--panel-source must be one of ${PANEL_SOURCES.join(', ')}`);
  }
  if (!options.layouts.length || options.layouts.some((l) => !LAYOUTS.includes(l))) {
    throw new UsageError(`--layouts must be a comma list of ${LAYOUTS.join(', ')}`);
  }
  if (!options.staticPathModes.length || options.staticPathModes.some((m) => !STATIC_PATH_MODES.includes(m))) {
    throw new UsageError(`--static-path-modes must be a comma list of ${STATIC_PATH_MODES.join(', ')}`);
  }
  if (!/^[\w.-]+\/[\w.-]+$/.test(options.repository)) {
    throw new UsageError('--repository must be owner/name');
  }
  return options;
}

const USAGE = `Usage:
  node bin/ci/panel-lite-cpa-v8.mjs --bundle-only [--skip-build]
  node bin/ci/panel-lite-cpa-v8.mjs [--cpa-image REF | --build-cpa-image] [--skip-build]
       [--panel-source stub|mounted] [--layouts legacy,v8]
       [--static-path-modes ${STATIC_PATH_MODES.join(',')}] [--repository owner/name] [--report FILE]
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
  const out = docker.probeImage({ name: `${CONTAINER_PREFIX}${runId}-image-probe`, image: info.id });
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

/**
 * One case: a config layout crossed with a static panel path mode. Both roles
 * run their own container on this run's network; every HTTP check runs inside
 * the container, so what is verified is what the container's network allows.
 */
export async function runCase({
  configLayout,
  staticPathMode,
  runId,
  runToken,
  docker,
  image,
  network,
  release,
  caPath,
  panel,
  options,
  tmpRoot,
  secrets,
  requestFor,
}) {
  const key = `cpampd-lite-key-${crypto.randomBytes(12).toString('hex')}`;
  secrets.push(key);
  const useStub = options.panelSource === 'stub';
  const explicit = staticPathMode === 'explicit-management-static-path';
  const caseName = caseId(configLayout, staticPathMode);
  const uid = typeof process.getuid === 'function' ? process.getuid() : undefined;
  const gid = typeof process.getgid === 'function' ? process.getgid() : undefined;
  const repository = `https://github.com/${options.repository}`;

  const prepare = (role, flags) => {
    const dir = fs.mkdtempSync(path.join(tmpRoot, `${caseName}-${role}-`));
    fs.mkdirSync(path.join(dir, 'auth'), { recursive: true });
    fs.mkdirSync(path.join(dir, 'static'), { recursive: true });
    const decoyPath = explicit ? path.join(dir, 'static', 'management.html') : undefined;
    if (decoyPath) fs.writeFileSync(decoyPath, DECOY_CONFIG_STATIC_PANEL);
    const panelDir = explicit ? path.join(dir, 'panel-static') : undefined;
    if (panelDir) fs.mkdirSync(panelDir);
    const staticPath = path.join(panelDir ?? path.join(dir, 'static'), 'management.html');
    const configPath = path.join(dir, 'config.yaml');
    fs.writeFileSync(
      configPath,
      renderCpaConfig({ layout: configLayout, managementKey: key, repository, ...flags })
    );
    return { dir, configPath, staticPath, panelDir, decoyPath };
  };

  const stored = (paths) =>
    checkStaticPanel({
      staticPath: paths.staticPath,
      panel: panel.bytes,
      decoyPath: paths.decoyPath,
      decoy: DECOY_CONFIG_STATIC_PANEL,
    });

  const start = async (role, paths, releaseAddress = release.address) => {
    const name = containerName(runId, caseName, role);
    docker.start({
      name,
      args: dockerRunArgs({
        name,
        runId,
        runToken,
        image,
        network,
        configDir: paths.dir,
        panelDir: paths.panelDir,
        releaseAddress,
        caPath,
        uid,
        gid,
      }),
    });
    const request = requestFor(name);
    try {
      await waitForManagement({ port: CPA_PORT, key, request });
      return { name, request };
    } catch (error) {
      throw new Error(`${error.message}\n--- ${name} log ---\n${docker.logs(name)}`);
    }
  };

  const result = { configLayout, staticPathMode };
  const settle = () => new Promise((resolve) => setTimeout(resolve, SETTLE_MS));

  // Panel enabled. In stub mode CPA downloads management.html from the release
  // server; in mounted mode the file is placed by hand, `disable-auto-update-panel`
  // is true and the release server (serving a different "1.x" panel) must stay
  // silent: the documented manual-install flow.
  const enabled = prepare('enabled', {
    disableControlPanel: false,
    disableAutoUpdatePanel: !useStub,
  });
  if (!useStub) fs.writeFileSync(enabled.staticPath, panel.bytes);
  const enabledBefore = release.requests().length;
  const cpa = await start('enabled', enabled);
  try {
    await waitFor(async () => (await cpa.request({ path: '/management.html' })).status === 200, {
      what: 'CPA to serve /management.html',
    });
  } catch (error) {
    throw new Error(`${error.message}\n--- ${cpa.name} log ---\n${docker.logs(cpa.name)}`);
  }
  result.enabled = {
    panelSource: options.panelSource,
    static: stored(enabled),
    served: await checkServedPanel({ port: CPA_PORT, panel: panel.bytes, request: cpa.request }),
  };
  if (useStub) {
    result.enabled.releaseServer = checkReleaseDownload({
      requests: release.requests(),
      since: enabledBefore,
      paths: release.paths,
    });
  } else {
    await settle();
    result.enabled.placedPanelKept = checkPlacedPanelKept({
      staticPath: enabled.staticPath,
      panel: panel.bytes,
      decoyPath: enabled.decoyPath,
      decoy: DECOY_CONFIG_STATIC_PANEL,
      requests: release.requests(),
      since: enabledBefore,
    });
  }
  result.enabled.management = await checkManagementContract({
    port: CPA_PORT,
    key,
    configPath: enabled.configPath,
    request: cpa.request,
  });
  docker.restart(cpa.name);
  await waitForManagement({ port: CPA_PORT, key, request: cpa.request });
  result.enabled.afterRestart = await checkPersistedAfterRestart({
    port: CPA_PORT,
    key,
    configPath: enabled.configPath,
    request: cpa.request,
  });
  if (!useStub) {
    await settle();
    result.enabled.placedPanelKeptAfterRestart = checkPlacedPanelKept({
      staticPath: enabled.staticPath,
      panel: panel.bytes,
      decoyPath: enabled.decoyPath,
      decoy: DECOY_CONFIG_STATIC_PANEL,
      requests: release.requests(),
      since: enabledBefore,
    });
  }
  result.enabled.servedAfterRestart = await checkServedPanel({
    port: CPA_PORT,
    panel: panel.bytes,
    request: cpa.request,
  });

  // Panel disabled: the bundle is present and the release server reachable, yet no route.
  const disabled = prepare('disabled', {
    disableControlPanel: true,
    disableAutoUpdatePanel: false,
  });
  fs.writeFileSync(disabled.staticPath, panel.bytes);
  const disabledBefore = release.requests().length;
  const off = await start('disabled', disabled);
  // The enabled run downloads within seconds of startup; the same window must stay silent here.
  await settle();
  result.disabled = await checkDisabledPanel({
    port: CPA_PORT,
    key,
    staticPath: disabled.staticPath,
    panel: panel.bytes,
    decoyPath: disabled.decoyPath,
    decoy: DECOY_CONFIG_STATIC_PANEL,
    request: off.request,
  });
  result.disabled.releaseServerRequests = release.requests().length - disabledBefore;
  assert.equal(
    result.disabled.releaseServerRequests,
    0,
    'CPA asked the release server for a panel although the control panel is disabled'
  );
  return result;
}

/**
 * The isolation half of the run. Two CPAs that cannot obtain a panel -- one whose
 * release server address is unreachable, one whose release server is reachable
 * but serves no panel -- must not fall back to the internet, and the network they
 * share with every other CPA container of this run must deny public addresses
 * while the release server on it stays reachable.
 */
export async function runIsolation({
  runId,
  runToken,
  docker,
  image,
  network,
  release,
  caPath,
  panel,
  options,
  tmpRoot,
  secrets,
  requestFor,
}) {
  const key = `cpampd-lite-key-${crypto.randomBytes(12).toString('hex')}`;
  secrets.push(key);
  const uid = typeof process.getuid === 'function' ? process.getuid() : undefined;
  const gid = typeof process.getgid === 'function' ? process.getgid() : undefined;
  const certDir = path.dirname(caPath);

  /** One CPA with no usable panel, plus the reachability of the network it sits on. */
  const scenario = async ({ caseName, releaseAddress, releaseServer }) => {
    const dir = fs.mkdtempSync(path.join(tmpRoot, `${caseName}-fallback-`));
    fs.mkdirSync(path.join(dir, 'auth'), { recursive: true });
    fs.mkdirSync(path.join(dir, 'static'), { recursive: true });
    const staticPath = path.join(dir, 'static', 'management.html');
    fs.writeFileSync(
      path.join(dir, 'config.yaml'),
      renderCpaConfig({
        layout: 'v8',
        managementKey: key,
        repository: `https://github.com/${options.repository}`,
        disableControlPanel: false,
        disableAutoUpdatePanel: false,
      })
    );

    const name = containerName(runId, caseName, 'fallback');
    docker.start({
      name,
      args: dockerRunArgs({
        name,
        runId,
        runToken,
        image,
        network,
        configDir: dir,
        releaseAddress,
        caPath,
        uid,
        gid,
      }),
    });
    const request = requestFor(name);
    try {
      await waitForManagement({ port: CPA_PORT, key, request });
    } catch (error) {
      throw new Error(`${error.message}\n--- ${name} log ---\n${docker.logs(name)}`);
    }
    await new Promise((resolve) => setTimeout(resolve, SETTLE_MS));

    const served = await request({ path: '/management.html' });
    assert.equal(
      served.status,
      404,
      `CPA served a panel although its release server ${releaseServer} (${served.status})`
    );
    assert(!fs.existsSync(staticPath), `CPA stored ${staticPath} although its release server ${releaseServer}`);

    const probe = runEgressProbe({
      docker,
      container: name,
      targets: [...EGRESS_TARGETS, `${release.address}:${RELEASE_PORT}`],
    });
    return {
      releaseServer,
      panelServed: 404,
      stored: false,
      ...checkEgressIsolation({ open: probe.open, blocked: probe.blocked, release: release.address }),
    };
  };

  // api.github.com answers on CPA's own loopback, where nothing listens: the
  // release server is unreachable and a fallback would have to leave the network.
  const unreachable = await scenario({
    caseName: 'isolation',
    releaseAddress: '127.0.0.1',
    releaseServer: '127.0.0.1 (unreachable)',
  });

  // The release server answers, but with an empty tree: a reachable failure is
  // the case CPA's updater documents a fallback for, and it must fail closed too.
  const emptyDocRoot = path.join(tmpRoot, 'release-empty');
  fs.mkdirSync(emptyDocRoot);
  const emptyServerName = containerName(runId, 'isolation-empty', 'server');
  docker.start({
    name: emptyServerName,
    args: releaseServerArgs({
      name: emptyServerName,
      runToken,
      image,
      network,
      docRoot: emptyDocRoot,
      certDir,
    }),
  });
  const emptyAddress = docker.address(emptyServerName);
  const withoutPanel = await scenario({
    caseName: 'isolation-empty',
    releaseAddress: emptyAddress,
    releaseServer: `${emptyAddress} (serves no panel)`,
  });

  return { unreachable, releaseServerWithoutPanel: withoutPanel };
}

/**
 * The whole acceptance run. The collaborators are injectable so the failure and
 * signal paths can be driven without Docker: `dockerFactory` (see `createDocker`),
 * `certificateFactory`, `caseRunner` / `isolationRunner`, `build`, the
 * `signals` emitter whose SIGINT/SIGTERM are handled, `exit` called after a
 * signal, and `removeTempRoot` for the temporary directory.
 */
export async function runAcceptance(
  argv,
  {
    stdout = process.stdout,
    stderr = process.stderr,
    dockerFactory = createDocker,
    certificateFactory = generateReleaseCertificate,
    caseRunner = runCase,
    isolationRunner = runIsolation,
    requestFactory = createExecRequest,
    build = buildPanel,
    signals = process,
    exit = (code) => process.exit(code),
    removeTempRoot = (dir) => fs.rmSync(dir, { recursive: true, force: true }),
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
  const runToken = crypto.randomBytes(16).toString('hex');
  const docker = dockerFactory({ runToken });
  const requestFor = (container) => requestFactory({ docker, container });
  let tmpRoot;
  let releasePromise;
  let exitCode = 1;
  const release = () =>
    (releasePromise ??= (async () => {
      const failures = [];
      // Each step runs even when an earlier one failed, so one broken resource
      // never hides the state of the others.
      try {
        failures.push(...docker.releaseAll());
      } catch (error) {
        failures.push(`docker: ${error.message}`);
      }
      if (tmpRoot) {
        try {
          removeTempRoot(tmpRoot);
        } catch (error) {
          failures.push(`tmpdir ${tmpRoot}: ${error.message}`);
        }
      }
      tmpRoot = undefined;
      return failures;
    })());
  const onSignal = (signal) => async () => {
    stderr.write(`${signal}: releasing ${CONTAINER_PREFIX}${runId}-* resources\n`);
    await release();
    exit(signal === 'SIGINT' ? 130 : 143);
  };
  const handlers = [['SIGINT', onSignal('SIGINT')], ['SIGTERM', onSignal('SIGTERM')]];
  for (const [signal, handler] of handlers) signals.on(signal, handler);

  // The build phase is optional and may fetch build dependencies; the runtime
  // phase below runs on a network that denies everything but this run's server.
  report.phases = { build: { requested: options.buildCpaImage, built: false } };

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
          report.phases.build.built = true;
          image = candidate;
        } else {
          throw new UsageError(
            `no CPA v8 image: pass --cpa-image REF, set CPAMP_LITE_CPA_IMAGE, or use --build-cpa-image (builds ${candidate})`
          );
        }
      }
      report.cpa = verifyImageIdentity({ docker, image, pin, runId, runToken });
      // Every container runs the image id that was verified, never the mutable tag.
      const imageId = report.cpa.imageId;

      tmpRoot = fs.mkdtempSync(path.join(tmpBase, `${CONTAINER_PREFIX}${runId}-`));
      const certDir = path.join(tmpRoot, 'certs');
      fs.mkdirSync(certDir);
      const certificate = certificateFactory();
      fs.writeFileSync(path.join(certDir, 'cert.pem'), certificate.cert);
      fs.writeFileSync(path.join(certDir, 'key.pem'), certificate.key, { mode: 0o600 });
      fs.writeFileSync(path.join(certDir, 'ca.pem'), certificate.cert);
      const caPath = path.join(certDir, 'ca.pem');

      const docRoot = path.join(tmpRoot, 'release');
      fs.mkdirSync(docRoot);
      // In mounted mode the release server offers a different, "1.x" panel: if CPA
      // ever updated the placed file, the bytes would change and the checks would see it.
      writeReleaseTree({
        docRoot,
        repository: options.repository,
        panel: options.panelSource === 'stub' ? panel.bytes : DECOY_1X_PANEL,
      });

      const network = networkName(runId);
      docker.createNetwork(network);
      const releaseName = containerName(runId, 'release', 'server');
      docker.start({
        name: releaseName,
        args: releaseServerArgs({
          name: releaseName,
          runToken,
          image: imageId,
          network,
          docRoot,
          certDir,
        }),
      });
      const releaseAddress = docker.address(releaseName);
      const releaseServer = {
        name: releaseName,
        address: releaseAddress,
        paths: releasePaths(options.repository),
        requests: () => releaseRequests(docker.logs(releaseName, 400)),
      };

      report.phases.runtime = {
        network,
        internal: true,
        publishedPorts: 0,
        probes: 'docker-exec',
      };
      report.cases = [];
      for (const configLayout of options.layouts) {
        for (const staticPathMode of options.staticPathModes) {
          stderr.write(`[panel-lite] layout=${configLayout} static=${staticPathMode} source=${options.panelSource}\n`);
          report.cases.push(
            await caseRunner({
              configLayout,
              staticPathMode,
              runId,
              runToken,
              docker,
              image: imageId,
              network,
              release: releaseServer,
              caPath,
              panel,
              options,
              tmpRoot,
              secrets,
              requestFor,
            })
          );
        }
      }
      report.isolation = await isolationRunner({
        runId,
        runToken,
        docker,
        image: imageId,
        network,
        release: releaseServer,
        caPath,
        options,
        tmpRoot,
        secrets,
        requestFor,
      });
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
