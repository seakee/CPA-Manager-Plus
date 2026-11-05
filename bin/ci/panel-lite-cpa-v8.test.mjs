// Behavioural tests for `panel-lite-cpa-v8.mjs`. They run without Docker and
// without a CPA: the pin reader, the bundle contract, the CPA config renderer,
// the release-server document and request log, the Docker namespace and
// ownership guards, the in-container HTTP transport, the egress assertions and
// every acceptance check are driven through their own contracts, so a
// regression fails here instead of only on a host that can run the real CPA v8.
//
// Run with:
//   node --test bin/ci/panel-lite-cpa-v8.test.mjs

import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import crypto from 'node:crypto';
import { EventEmitter } from 'node:events';
import fs from 'node:fs';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';

import {
  BUILD_PANEL_COMMAND,
  CONTAINER_PREFIX,
  DECOY_1X_PANEL,
  DECOY_CONFIG_STATIC_PANEL,
  DEFAULT_PANEL,
  EGRESS_TARGETS,
  LAYOUTS,
  ROOT,
  RUN_LABEL,
  STATIC_PATH_MODES,
  STUB_TAG,
  UsageError,
  assertOwnedName,
  assertRunToken,
  assertSingleFilePanel,
  buildPanel,
  caseId,
  checkDisabledPanel,
  checkEgressIsolation,
  checkManagementContract,
  checkPersistedAfterRestart,
  checkPlacedPanelKept,
  checkReleaseDownload,
  checkServedPanel,
  checkStaticPanel,
  containerName,
  createDocker,
  createExecRequest,
  defaultImageTag,
  dockerRunArgs,
  generateReleaseCertificate,
  httpRequest,
  identityProbeArgs,
  loadCpaV8Pin,
  networkName,
  parseArgs,
  parseContainerResponse,
  persistedRequestRetry,
  readCpaV8Pin,
  releaseDocument,
  releasePaths,
  releaseRequests,
  releaseServerArgs,
  renderCpaConfig,
  runEgressProbe,
  runAcceptance,
  verifyImageIdentity,
  waitFor,
  writeReleaseTree,
} from './panel-lite-cpa-v8.mjs';

const sha256 = (bytes) => crypto.createHash('sha256').update(bytes).digest('hex');
const GOOD_HTML = Buffer.from(
  '<!doctype html><html><head><style>a{color:red}</style></head><body><script>1</script></body></html>'
);
const KEY = 'cpampd-lite-key-test';
const RUN = '0123abcd';
const TOKEN = 'a1b2c3d4e5f60718293a4b5c6d7e8f90';
const NETWORK = `${CONTAINER_PREFIX}${RUN}-net`;

const tempDir = (t) => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'cpampd-lite-test-'));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  return dir;
};

// ---------------------------------------------------------------- pin

test('the pin is read from the cpa-v8-download stage of Dockerfile.runtime, amd64 only', () => {
  const pin = loadCpaV8Pin();
  assert.match(pin.version, /^\d+\.\d+\.\d+$/);
  assert.match(pin.executableSha256, /^[0-9a-f]{64}$/);

  const dockerfile = [
    'FROM alpine AS cpa-download',
    '  amd64) executable_sha256="' + 'a'.repeat(64) + '" ;;',
    'FROM --platform=$BUILDPLATFORM alpine:3.21 AS cpa-v8-download',
    '    case "$TARGETARCH" in \\',
    '      amd64) asset_arch="amd64"; \\',
    '        archive_sha256="' + '1'.repeat(64) + '"; \\',
    '        executable_sha256="' + 'b'.repeat(64) + '" ;; \\',
    '      arm64) asset_arch="aarch64"; \\',
    '        executable_sha256="' + 'c'.repeat(64) + '" ;; \\',
    '    esac; \\',
    '    asset_name="CLIProxyAPI_8.0.55_linux_${asset_arch}.tar.gz"; \\',
    'FROM alpine AS next',
    '  amd64) executable_sha256="' + 'd'.repeat(64) + '" ;;',
  ].join('\n');
  assert.deepEqual(readCpaV8Pin(dockerfile), { version: '8.0.55', executableSha256: 'b'.repeat(64) });
  assert.equal(defaultImageTag(pin), `cpampd-lite-cpa-v8:${pin.version}-${pin.executableSha256.slice(0, 12)}`);
});

test('a Dockerfile without a usable v8 pin is refused', () => {
  assert.throws(() => readCpaV8Pin('FROM alpine AS other\n'), /no cpa-v8-download stage/);
  assert.throws(
    () => readCpaV8Pin('FROM alpine AS cpa-v8-download\nRUN echo nothing\n'),
    /names no CLIProxyAPI version/
  );
  assert.throws(
    () => readCpaV8Pin('FROM alpine AS cpa-v8-download\nRUN echo CLIProxyAPI_8.0.1_linux_x\n'),
    /no amd64 executable_sha256/
  );
});

// ---------------------------------------------------------------- bundle

const bundle = (html, entries = ['management.html'], name = 'management.html') =>
  assertSingleFilePanel({ name, entries, bytes: Buffer.from(html) });

test('a self-contained management.html passes the single-file contract', () => {
  assertSingleFilePanel({ name: 'management.html', entries: ['management.html'], bytes: GOOD_HTML });
  // Inline data: icons and strings that merely mention assets are not references.
  bundle(
    '<!doctype html><html><head><link rel="icon" href="data:image/svg+xml;base64,AAAA"><style>a{}</style></head>' +
      '<body><script type="module" crossorigin>const note="static assets are inlined";</script></body></html>'
  );
});

test('the single-file contract rejects siblings, wrong names and external references', () => {
  assert.throws(() => bundle(GOOD_HTML, ['assets', 'management.html']), /one HTML artifact/);
  assert.throws(() => bundle(GOOD_HTML, ['index.html'], 'index.html'), /named management\.html/);
  assert.throws(() => bundle(''), /empty|not an HTML document/);
  assert.throws(() => bundle('<html></html>'), /not an HTML document/);
  assert.throws(() => bundle('<!doctype html><body>text</body>'), /not inlined/);
  const wrap = (inner) =>
    `<!doctype html><html><head><style>a{}</style>${inner}</head><body><script>1</script></body></html>`;
  for (const [label, inner] of [
    ['src attribute', '<img src="./assets/logo.svg">'],
    ['href attribute', '<a href="assets/app.css">x</a>'],
    ['rooted href', '<link rel="stylesheet" href="/assets/app.css">'],
    ['url()', '<style>a{background:url(./assets/bg.png)}</style>'],
    ['import path', '<script type="module">import("./assets/chunk-1.js")</script>'],
    ['script src', '<script src="app.js"></script>'],
    ['link href', '<link rel="modulepreload" href="chunk.js">'],
  ]) {
    assert.throws(() => bundle(wrap(inner)), /sibling resources/, label);
  }
});

test('the built panel, when present, satisfies the contract and is the only file', (t) => {
  if (!fs.existsSync(DEFAULT_PANEL)) {
    t.skip('apps/web/dist/panel/management.html is not built; run npm --workspace apps/web run build:panel');
    return;
  }
  assertSingleFilePanel({
    name: path.basename(DEFAULT_PANEL),
    entries: fs.readdirSync(path.dirname(DEFAULT_PANEL)).sort(),
    bytes: fs.readFileSync(DEFAULT_PANEL),
  });
});

test('buildPanel runs build:panel from the repository root and fails when it fails', () => {
  const calls = [];
  buildPanel({ exec: (command, options) => (calls.push({ command, options }), { status: 0 }) });
  assert.deepEqual(calls, [{ command: BUILD_PANEL_COMMAND, options: { cwd: ROOT } }]);
  assert.deepEqual(BUILD_PANEL_COMMAND, ['npm', '--workspace', 'apps/web', 'run', 'build:panel']);
  assert.throws(() => buildPanel({ exec: () => ({ status: 1 }) }), /build:panel failed/);
});

// ---------------------------------------------------------------- CPA config

const configOptions = {
  managementKey: KEY,
  repository: 'https://github.com/seakee/CPA-Manager-Plus',
  disableControlPanel: false,
  disableAutoUpdatePanel: true,
};

test('the legacy layout is the documented remote-management configuration', () => {
  const config = renderCpaConfig({ layout: 'legacy', ...configOptions });
  assert.match(config, /^remote-management:\n {2}allow-remote: true\n {2}secret-key: "cpampd-lite-key-test"\n/m);
  assert.match(config, /^ {2}disable-control-panel: false$/m);
  assert.match(config, /^ {2}disable-auto-update-panel: true$/m);
  assert.match(config, /^ {2}panel-github-repository: "https:\/\/github\.com\/seakee\/CPA-Manager-Plus"$/m);
  assert.match(config, /^request-retry: 3$/m);
  assert.doesNotMatch(config, /config-version|^management:/m);
  assert.equal(persistedRequestRetry(config), 3);
});

test('the v8 layout moves the same fields under management and routing', () => {
  const config = renderCpaConfig({ layout: 'v8', ...configOptions, disableControlPanel: true, disableAutoUpdatePanel: false });
  assert.match(config, /^config-version: 8$/m);
  assert.match(config, /^management:\n {2}allow-remote: true\n {2}secret-key: "cpampd-lite-key-test"\n/m);
  assert.match(config, /^ {2}disable-control-panel: true$/m);
  assert.match(config, /^ {2}disable-auto-update-panel: false$/m);
  assert.match(config, /^ {2}panel-github-repository: "https:\/\/github\.com\/seakee\/CPA-Manager-Plus"$/m);
  assert.match(config, /^routing:\n {2}retry:\n {4}request-retry: 3$/m);
  assert.doesNotMatch(config, /remote-management|^requests:/m);
  assert.equal(persistedRequestRetry(config), 3);
});

test('the configured download is addressed, never proxied', () => {
  for (const layout of LAYOUTS) {
    const config = renderCpaConfig({ layout, ...configOptions });
    assert.doesNotMatch(config, /proxy-url/, `${layout}: the panel download must not depend on a proxy setting`);
  }
});

test('config values that need YAML escaping and unknown layouts are refused', () => {
  assert.throws(() => renderCpaConfig({ layout: 'legacy', ...configOptions, managementKey: 'a"b' }), /unsafe config value/);
  assert.throws(() => renderCpaConfig({ layout: 'legacy', ...configOptions, repository: 'x\ny: z' }), /unsafe config value/);
  assert.throws(() => renderCpaConfig({ layout: 'v9', ...configOptions }), /unknown config layout/);
  assert.deepEqual(LAYOUTS, ['legacy', 'v8']);
  assert.deepEqual(STATIC_PATH_MODES, ['config-sibling-static', 'explicit-management-static-path']);
});

test('request-retry is found at any depth and must be unambiguous', () => {
  assert.equal(persistedRequestRetry('routing:\n    retry:\n        request-retry: 11 # set\n'), 11);
  assert.equal(persistedRequestRetry('request-retry: 0\n'), 0);
  assert.throws(() => persistedRequestRetry('debug: false\n'), /exactly one request-retry/);
  assert.throws(() => persistedRequestRetry('request-retry: 1\nx:\n  request-retry: 2\n'), /exactly one request-retry/);
});

// ---------------------------------------------------------------- Lite contract

test('the Lite panel still talks to /v0/management and updates request-retry with { value }', () => {
  const read = (file) => fs.readFileSync(path.join(ROOT, file), 'utf8');
  // The harness drives exactly this call; a panel that moved to /v8/management or
  // changed the payload must change the harness (and the compatibility ledger) too.
  assert.match(
    read('apps/web/src/services/api/config.ts'),
    /updateRequestRetry:[^\n]*apiClient\.put\('\/request-retry', \{ value: retryCount \}\)/
  );
  assert.match(read('apps/web/src/panel/managementTarget.ts'), /endsWith\('\/v0\/management'\)/);
});

// ---------------------------------------------------------------- release server

test('the release document and asset tree describe the built panel', (t) => {
  const docRoot = tempDir(t);
  fs.rmSync(docRoot, { recursive: true, force: true });
  const { latestFile, assetFile } = writeReleaseTree({
    docRoot,
    repository: 'seakee/CPA-Manager-Plus',
    panel: GOOD_HTML,
  });
  const paths = releasePaths('seakee/CPA-Manager-Plus');
  assert.equal(latestFile, path.join(docRoot, paths.latest));
  assert.equal(assetFile, path.join(docRoot, paths.asset));

  assert(fs.readFileSync(assetFile).equals(GOOD_HTML), 'the asset is stored byte for byte');
  const document = JSON.parse(fs.readFileSync(latestFile, 'utf8'));
  assert.deepEqual(document, releaseDocument({ repository: 'seakee/CPA-Manager-Plus', panel: GOOD_HTML }));
  assert.equal(document.tag_name, STUB_TAG);
  assert.equal(document.prerelease, false);
  const [asset] = document.assets;
  assert.equal(asset.name, 'management.html');
  assert.equal(asset.size, GOOD_HTML.length);
  assert.equal(asset.digest, `sha256:${sha256(GOOD_HTML)}`);
  assert.equal(asset.browser_download_url, `https://api.github.com${paths.asset}`);
  // Both decoys must be distinguishable from the build, otherwise an accidental
  // update or a served decoy would pass unnoticed.
  assert(!DECOY_1X_PANEL.equals(GOOD_HTML));
  assert(!DECOY_CONFIG_STATIC_PANEL.equals(GOOD_HTML));
});

test('the release certificate is a self-signed certificate for api.github.com', () => {
  const certificate = generateReleaseCertificate();
  assert.match(certificate.cert, /-----BEGIN CERTIFICATE-----/);
  assert.match(certificate.key, /-----BEGIN PRIVATE KEY-----/);
  const probe = spawnSync('openssl', ['x509', '-noout', '-subject', '-ext', 'subjectAltName'], {
    input: certificate.cert,
    encoding: 'utf8',
  });
  assert.equal(probe.status, 0, probe.stderr);
  assert.match(probe.stdout, /DNS:api\.github\.com/);
});

test('the release server log names exactly the files it served', () => {
  const log = [
    'Using default temp DH parameters',
    'ACCEPT',
    'FILE:repos/seakee/CPA-Manager-Plus/releases/latest',
    'FILE:cpampd-lite/assets/management.html',
    'FILE:repos/seakee/CPA-Manager-Plus/releases/latest',
    '',
  ].join('\n');
  assert.deepEqual(releaseRequests(log), [
    '/repos/seakee/CPA-Manager-Plus/releases/latest',
    '/cpampd-lite/assets/management.html',
    '/repos/seakee/CPA-Manager-Plus/releases/latest',
  ]);
  assert.deepEqual(releaseRequests('ACCEPT\n'), []);
});

test('a download is accepted only with releases/latest and the asset, and nothing else', () => {
  const paths = releasePaths('o/r');
  const latest = { kind: 'latest' };
  const asset = paths.asset;
  const good = [paths.latest, asset];
  assert.deepEqual(checkReleaseDownload({ requests: good, paths }), {
    latestServed: true,
    assetServed: true,
    requests: good,
  });
  assert.equal(checkReleaseDownload({ requests: [...good, ...good], since: 2, paths }).requests.length, 2);
  assert.throws(() => checkReleaseDownload({ requests: [asset], paths }), /never served \/repos\/o\/r\/releases\/latest/);
  assert.throws(() => checkReleaseDownload({ requests: [paths.latest], paths }), /never served the management\.html asset/);
  assert.throws(
    () => checkReleaseDownload({ requests: [...good, '/repos/o/r/releases'], paths }),
    /served something other than releases\/latest/
  );
  assert(JSON.stringify(latest) && asset);
});

// ---------------------------------------------------------------- Docker namespace

test('only cpampd-lite-<run>-* names are accepted; cpamp-* is never touchable', () => {
  assert.equal(assertOwnedName(`cpampd-lite-${RUN}-legacy-sibling-enabled`), `cpampd-lite-${RUN}-legacy-sibling-enabled`);
  assert.equal(containerName(RUN, 'v8-sibling', 'disabled'), `cpampd-lite-${RUN}-v8-sibling-disabled`);
  assert.equal(networkName(RUN), NETWORK);
  assert.equal(caseId('legacy', 'config-sibling-static'), 'legacy-sibling');
  assert.equal(caseId('v8', 'explicit-management-static-path'), 'v8-explicit');
  for (const name of [
    'cpamp-runtime',
    'cpamp-d-external-cpa',
    'cpamp_runtime',
    'cpampd-runtime',
    'cpampd-d-runtime',
    'cpampd-lite-',
    'cpampd-lite-xyz-a',
    `cpampd-lite-${RUN}`,
    `cpampd-lite-${RUN}-UPPER`,
    `cpampd-lite-${RUN}-a b`,
    `cpampd-lite-${RUN}-a;rm`,
    'cpa-manager-plus-cpa-manager-plus-1',
    '',
    undefined,
  ]) {
    assert.throws(() => assertOwnedName(name), /refusing container name/, String(name));
  }
});

test('the CPA container is on the run network, labelled with the run token and publishes nothing', () => {
  const name = `cpampd-lite-${RUN}-legacy-sibling-enabled`;
  const args = dockerRunArgs({
    name,
    runId: RUN,
    runToken: TOKEN,
    image: 'cpampd-lite-cpa-v8:8.0.13-b682e9e42586',
    network: NETWORK,
    configDir: '/tmp/cpampd-lite-x/legacy',
    panelDir: '/tmp/cpampd-lite-x/legacy/panel-static',
    releaseAddress: '172.19.0.2',
    caPath: '/tmp/cpampd-lite-x/certs/ca.pem',
    uid: 501,
    gid: 20,
  });
  const at = (flag) => args.flatMap((arg, i) => (arg === flag ? [args[i + 1]] : []));
  assert.deepEqual(at('--name'), [name]);
  assert.deepEqual(at('--network'), [NETWORK]);
  assert.deepEqual(at('--label'), [`${RUN_LABEL}=${TOKEN}`]);
  assert.deepEqual(at('--pull'), ['never']);
  assert.deepEqual(at('--platform'), ['linux/amd64']);
  assert.deepEqual(at('--cap-drop'), ['ALL']);
  assert.deepEqual(at('--security-opt'), ['no-new-privileges']);
  assert.deepEqual(at('--add-host'), ['api.github.com:172.19.0.2']);
  assert.deepEqual(at('--user'), ['501:20']);
  assert.deepEqual(at('-e'), [
    'SSL_CERT_FILE=/cpampd-lite-stub/ca.pem',
    'MANAGEMENT_STATIC_PATH=/panel-static',
  ]);
  assert.deepEqual(at('-v'), [
    '/tmp/cpampd-lite-x/legacy:/cpa',
    '/tmp/cpampd-lite-x/certs/ca.pem:/cpampd-lite-stub/ca.pem:ro',
    '/tmp/cpampd-lite-x/legacy/panel-static:/panel-static',
  ]);
  assert.deepEqual(at('--entrypoint'), ['/usr/local/bin/cli-proxy-api']);
  assert.deepEqual(args.slice(-3), ['-config', '/cpa/config.yaml', '-local-model']);
  // No host port is published: the internal network has no route to the host.
  assert(!args.includes('-p') && !args.includes('-P'));
  for (const forbidden of ['--privileged', '--net', '--pid', '--volumes-from', '--device', '--env-file']) {
    assert(!args.includes(forbidden), forbidden);
  }
  assert(!args.join(' ').includes('18317'));
  assert(!args.join(' ').includes('cpamp-'));
});

test('without an explicit static path the container gets no MANAGEMENT_STATIC_PATH', () => {
  const args = dockerRunArgs({
    name: `cpampd-lite-${RUN}-v8-sibling-enabled`,
    runId: RUN,
    runToken: TOKEN,
    image: 'img',
    network: NETWORK,
    configDir: '/tmp/cpampd-lite-x/v8',
    releaseAddress: '172.19.0.2',
    caPath: '/tmp/cpampd-lite-x/ca.pem',
  });
  assert(!args.some((arg) => arg.startsWith('MANAGEMENT_STATIC_PATH')));
  assert.deepEqual(args.flatMap((a, i) => (a === '-v' ? [args[i + 1]] : [])), [
    '/tmp/cpampd-lite-x/v8:/cpa',
    '/tmp/cpampd-lite-x/ca.pem:/cpampd-lite-stub/ca.pem:ro',
  ]);
  assert(!args.includes('--user'));
});

test('docker run arguments refuse foreign names, foreign networks, unsafe mounts and bad tokens', () => {
  const base = {
    runId: RUN,
    runToken: TOKEN,
    image: 'img',
    network: NETWORK,
    configDir: '/tmp/x',
    caPath: '/tmp/x/ca.pem',
    releaseAddress: '172.19.0.2',
  };
  const name = `cpampd-lite-${RUN}-legacy-sibling-enabled`;
  assert.throws(() => dockerRunArgs({ ...base, name: 'cpamp-runtime' }), /refusing container name/);
  assert.throws(() => dockerRunArgs({ ...base, name, network: 'cpamp-net' }), /refusing container name/);
  assert.throws(() => dockerRunArgs({ ...base, name, configDir: 'relative' }), /unsafe bind-mount/);
  assert.throws(() => dockerRunArgs({ ...base, name, configDir: '/tmp/x:/etc' }), /unsafe bind-mount/);
  assert.throws(() => dockerRunArgs({ ...base, name, panelDir: '/tmp/a,b' }), /unsafe bind-mount/);
  assert.throws(() => dockerRunArgs({ ...base, name, runId: 'xyz' }), /bad run id/);
  assert.throws(() => dockerRunArgs({ ...base, name, runToken: RUN }), /CSPRNG/);
  assert.throws(() => dockerRunArgs({ ...base, name, releaseAddress: 'api.github.com' }), /not an IPv4 address/);
});

test('the release server is a read-only TLS file server on the run network', () => {
  const name = `cpampd-lite-${RUN}-release-server`;
  const args = releaseServerArgs({
    name,
    runToken: TOKEN,
    image: 'sha256:good',
    network: NETWORK,
    docRoot: '/tmp/cpampd-lite-x/release',
    certDir: '/tmp/cpampd-lite-x/certs',
  });
  const at = (flag) => args.flatMap((arg, i) => (arg === flag ? [args[i + 1]] : []));
  assert.deepEqual(at('--name'), [name]);
  assert.deepEqual(at('--network'), [NETWORK]);
  assert.deepEqual(at('--label'), [`${RUN_LABEL}=${TOKEN}`]);
  assert.deepEqual(at('--cap-drop'), ['ALL']);
  assert.deepEqual(at('--workdir'), ['/cpampd-lite-release']);
  assert.deepEqual(at('-v'), [
    '/tmp/cpampd-lite-x/release:/cpampd-lite-release:ro',
    '/tmp/cpampd-lite-x/certs:/cpampd-lite-certs:ro',
  ]);
  assert.deepEqual(at('--entrypoint'), ['openssl']);
  assert.deepEqual(args.slice(args.indexOf('s_server')), [
    's_server', '-accept', '443',
    '-cert', '/cpampd-lite-certs/cert.pem',
    '-key', '/cpampd-lite-certs/key.pem',
    '-WWW',
  ]);
  assert(!args.includes('-p') && !args.includes('-P'));
  assert.throws(
    () => releaseServerArgs({ name, runToken: TOKEN, image: 'img', network: NETWORK, docRoot: 'rel', certDir: '/tmp/c' }),
    /unsafe bind-mount/
  );
});

test('the identity probe is a throw-away container carrying the run label', () => {
  const args = identityProbeArgs({ name: `${CONTAINER_PREFIX}${RUN}-image-probe`, runToken: TOKEN, image: 'img' });
  assert(args.includes('--rm') && args.includes('--pull'));
  assert.deepEqual(args.slice(args.indexOf('--label'), args.indexOf('--label') + 2), ['--label', `${RUN_LABEL}=${TOKEN}`]);
  assert.deepEqual(args.slice(args.indexOf('--entrypoint'), args.indexOf('--entrypoint') + 3), ['--entrypoint', 'sh', 'img']);
  assert.match(
    args.at(-1),
    /sha256sum \/usr\/local\/bin\/cli-proxy-api && cat \/usr\/local\/share\/cpamp\/runtime\/cpa-artifact\.json/
  );
  assert.throws(() => identityProbeArgs({ name: 'cpamp-probe', runToken: TOKEN, image: 'img' }), /refusing container name/);
  assert.throws(() => identityProbeArgs({ name: `${CONTAINER_PREFIX}${RUN}-image-probe`, runToken: 'short', image: 'img' }), /CSPRNG/);
});

test('a run token is a per-run CSPRNG value, not the name prefix', () => {
  assert.equal(assertRunToken(TOKEN), TOKEN);
  for (const token of [RUN, 'a'.repeat(31), 'A'.repeat(32), '', undefined]) {
    assert.throws(() => assertRunToken(token), /CSPRNG/, String(token));
    assert.throws(() => createDocker({ exec: () => ({ status: 0, stdout: '', stderr: '' }), runToken: token }), /CSPRNG/);
  }
});

// ---------------------------------------------------------------- Docker ownership

/** A Docker double that keeps a name -> run-label map and refuses duplicate names. */
function ownedDockerDouble({ token = TOKEN, labels = new Map(), networkLabels = new Map() } = {}) {
  const calls = [];
  const exec = (args) => {
    calls.push(args);
    const name = args.at(-1);
    if (args[0] === 'run') {
      const container = args[args.indexOf('--name') + 1];
      if (labels.has(container)) {
        return {
          status: 125,
          stdout: '',
          stderr: `docker: Error response from daemon: Conflict. The container name "/${container}" is already in use.`,
        };
      }
      labels.set(container, args[args.indexOf('--label') + 1].split('=')[1]);
      return { status: 0, stdout: 'container-id\n', stderr: '' };
    }
    if (args[0] === 'inspect') {
      if (!labels.has(name)) return { status: 1, stdout: '', stderr: `Error: No such object: ${name}` };
      return { status: 0, stdout: `${labels.get(name)}\n`, stderr: '' };
    }
    if (args[0] === 'network' && args[1] === 'inspect') {
      if (!networkLabels.has(name)) return { status: 1, stdout: '', stderr: `Error: No such network: ${name}` };
      return { status: 0, stdout: `${networkLabels.get(name)}\n`, stderr: '' };
    }
    if (args[0] === 'network' && args[1] === 'create') {
      if (networkLabels.has(name)) return { status: 1, stdout: '', stderr: `Error response from daemon: network with name ${name} already exists` };
      networkLabels.set(name, args[args.indexOf('--label') + 1].split('=')[1]);
      return { status: 0, stdout: `${name}\n`, stderr: '' };
    }
    if (args[0] === 'rm') {
      labels.delete(name);
      return { status: 0, stdout: `${name}\n`, stderr: '' };
    }
    if (args[0] === 'network' && args[1] === 'rm') {
      networkLabels.delete(name);
      return { status: 0, stdout: `${name}\n`, stderr: '' };
    }
    return { status: 0, stdout: '', stderr: '' };
  };
  return { calls, labels, networkLabels, exec };
}

test('a container name conflict never turns into removing the foreign container', () => {
  const foreign = `cpampd-lite-${RUN}-legacy-sibling-enabled`;
  const own = `cpampd-lite-${RUN}-legacy-sibling-disabled`;
  const model = ownedDockerDouble({ labels: new Map([[foreign, 'ff'.repeat(16)]]) });
  const docker = createDocker({ exec: model.exec, runToken: TOKEN });

  assert.throws(() => docker.start({ name: foreign, args: ['run', '-d', '--name', foreign, '--label', `${RUN_LABEL}=${TOKEN}`] }), /docker run failed \(125\)/);
  docker.start({ name: own, args: ['run', '-d', '--name', own, '--label', `${RUN_LABEL}=${TOKEN}`] });

  const failures = docker.releaseAll();
  const removals = model.calls.filter((args) => args[0] === 'rm').map((args) => args.at(-1));
  assert.deepEqual(removals, [own], 'only this run\'s container is removed');
  assert(model.labels.has(foreign), 'the pre-existing container is still there');
  assert.deepEqual(failures, [
    `${foreign}: carries ${RUN_LABEL}="ff${'ff'.repeat(15)}", not this run's token; left in place`,
  ]);
  assert.equal(docker.owned.size, 0);
});

test('a network is removed only when it carries this run token', () => {
  const model = ownedDockerDouble({
    networkLabels: new Map([[`${CONTAINER_PREFIX}${RUN}-net`, 'ff'.repeat(16)]]),
  });
  const docker = createDocker({ exec: model.exec, runToken: TOKEN });
  // A name that exists without this run's label is never created by us and must
  // survive even if it ends up in the owned set.
  assert.throws(() => docker.createNetwork(`${CONTAINER_PREFIX}${RUN}-net`), /already exists/);
  assert.equal(docker.networks.size, 0);
  docker.networks.add(`${CONTAINER_PREFIX}${RUN}-net`);

  const failures = docker.releaseAll();
  assert.deepEqual(model.calls.filter((args) => args[0] === 'network' && args[1] === 'rm'), []);
  assert(model.networkLabels.has(`${CONTAINER_PREFIX}${RUN}-net`), 'the foreign network is still there');
  assert.match(failures[0], /not this run's token; left in place/);
});

test('this run network is created internal, labelled and removed after its containers', () => {
  const model = ownedDockerDouble();
  const docker = createDocker({ exec: model.exec, runToken: TOKEN });
  docker.createNetwork(NETWORK);
  const created = model.calls.find((args) => args[0] === 'network' && args[1] === 'create');
  assert.deepEqual(created, ['network', 'create', '--internal', '--label', `${RUN_LABEL}=${TOKEN}`, NETWORK]);

  const name = `cpampd-lite-${RUN}-release-server`;
  docker.start({ name, args: ['run', '-d', '--name', name, '--label', `${RUN_LABEL}=${TOKEN}`] });
  const failures = docker.releaseAll();
  assert.deepEqual(failures, []);
  assert.deepEqual(
    model.calls.filter((args) => args[0] === 'rm' || (args[0] === 'network' && args[1] === 'rm')),
    [['rm', '-f', name], ['network', 'rm', NETWORK]],
    'containers are removed before the network'
  );
  assert.equal(model.labels.size, 0);
  assert.equal(model.networkLabels.size, 0);
});

test('an already-gone container is not a cleanup failure, a stuck one is reported', () => {
  const gone = `cpampd-lite-${RUN}-gone`;
  const stuck = `cpampd-lite-${RUN}-stuck`;
  const exec = (args) => {
    const name = args.at(-1);
    if (args[0] === 'inspect') return { status: 0, stdout: `${TOKEN}\n`, stderr: '' };
    if (args[0] === 'rm' && name === gone) return { status: 1, stdout: '', stderr: 'Error: No such container: x' };
    if (args[0] === 'rm' && name === stuck) return { status: 1, stdout: '', stderr: 'device busy' };
    return { status: 0, stdout: '', stderr: '' };
  };
  const docker = createDocker({ exec, runToken: TOKEN });
  docker.start({ name: gone, args: ['run', '-d', '--name', gone] });
  docker.start({ name: stuck, args: ['run', '-d', '--name', stuck] });
  assert.deepEqual(docker.releaseAll(), [`${stuck}: device busy`]);
});

test('the Docker wrapper resolves the container address, the image platform and the build namespace', () => {
  const docker = createDocker({
    exec: (args) => {
      if (args[0] === 'inspect') return { status: 0, stdout: '172.19.0.7\n', stderr: '' };
      if (args[0] === 'image') {
        return { status: 0, stdout: JSON.stringify([{ Id: 'sha256:abc', Os: 'linux', Architecture: 'amd64' }]), stderr: '' };
      }
      return { status: 1, stdout: '', stderr: '' };
    },
    runToken: TOKEN,
  });
  assert.equal(docker.address(`cpampd-lite-${RUN}-release-server`), '172.19.0.7');
  assert.deepEqual(docker.imageInfo('img'), { id: 'sha256:abc', os: 'linux', architecture: 'amd64' });
  assert.equal(createDocker({ exec: () => ({ status: 1, stdout: '', stderr: '' }), runToken: TOKEN }).imageInfo('missing'), undefined);
  assert.throws(() => docker.build({ image: 'cpamp-runtime:latest' }), /outside the harness namespace/);
});

// ---------------------------------------------------------------- in-container transport

test('the in-container response decoder reads status, headers and the exact body', () => {
  const raw = Buffer.concat([
    Buffer.from('HTTP/1.1 200 OK\r\nContent-Type: text/html; charset=utf-8\r\nContent-Length: 3\r\n\r\n'),
    Buffer.from([0x00, 0xff, 0x41]),
  ]);
  const decoded = parseContainerResponse(raw.toString('base64'));
  assert.equal(decoded.status, 200);
  assert.equal(decoded.headers['content-type'], 'text/html; charset=utf-8');
  assert.deepEqual([...decoded.body], [0x00, 0xff, 0x41]);
  assert.throws(() => parseContainerResponse(Buffer.from('not http').toString('base64')), /no header terminator/);
  assert.throws(() => parseContainerResponse(Buffer.from('BROKEN\r\n\r\n').toString('base64')), /unparseable status line/);
});

test('a chunked response body is decoded before it is read', () => {
  const chunked = ['a\r\n0123456789\r\n', '5;ext=1\r\nabcde\r\n', '0\r\n\r\n'].join('');
  const raw = `HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\nContent-Type: application/json\r\n\r\n${chunked}`;
  const decoded = parseContainerResponse(Buffer.from(raw, 'latin1').toString('base64'));
  assert.equal(decoded.status, 200);
  assert.equal(decoded.body.toString(), '0123456789abcde');
  assert.throws(
    () => parseContainerResponse(Buffer.from('HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\nzz\r\n', 'latin1').toString('base64')),
    /unparseable chunk size/
  );
  assert.throws(
    () => parseContainerResponse(Buffer.from('HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\na\r\nshort', 'latin1').toString('base64')),
    /ended mid-chunk/
  );
});

test('the in-container transport passes method, path, headers and body as separate arguments', async () => {
  const container = `cpampd-lite-${RUN}-legacy-sibling-enabled`;
  const raw = Buffer.concat([Buffer.from('HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n'), Buffer.from('{"ok":true}')]);
  const calls = [];
  const docker = { execInContainer: (args) => (calls.push(args), raw.toString('base64')) };
  const request = createExecRequest({ docker, container });

  const response = await request({
    method: 'PUT',
    path: '/v0/management/request-retry',
    headers: { authorization: `Bearer ${KEY}`, 'content-type': 'application/json' },
    body: JSON.stringify({ value: 11 }),
  });
  assert.equal(response.status, 200);
  assert.equal(response.body.toString(), '{"ok":true}');

  assert.equal(calls.length, 1);
  const [, name, shell, dashC, script, argv0, method, target, headers, body] = calls[0];
  assert.deepEqual([name, shell, dashC, argv0], [container, 'bash', '-c', 'cpampd-lite-http']);
  assert.equal(method, 'PUT');
  assert.equal(target, '/v0/management/request-retry');
  assert.equal(
    Buffer.from(headers, 'base64').toString(),
    `authorization: Bearer ${KEY}\r\ncontent-type: application/json\r\n`
  );
  assert.deepEqual(JSON.parse(Buffer.from(body, 'base64').toString()), { value: 11 });
  assert(!script.includes(KEY), 'the probe script is fixed; values never travel inside it');
  assert.throws(() => createExecRequest({ docker, container: 'cpamp-runtime' }), /refusing container name/);
});

test('the reachability probe reports each target once', () => {
  const container = `cpampd-lite-${RUN}-isolation-fallback`;
  const targets = ['172.19.0.2:443', '1.1.1.1:443'];
  const calls = [];
  const docker = {
    execInContainer: (args) => {
      calls.push(args);
      return `open 172.19.0.2:443\nblocked 1.1.1.1:443\n`;
    },
  };
  assert.deepEqual(runEgressProbe({ docker, container, targets }), {
    open: ['172.19.0.2:443'],
    blocked: ['1.1.1.1:443'],
  });
  assert.deepEqual(calls[0].slice(0, 5), ['exec', container, 'bash', '-c', calls[0][4]]);
  assert.deepEqual(calls[0].slice(5), ['cpampd-lite-probe', ...targets]);
  assert.throws(
    () => runEgressProbe({ docker: { execInContainer: () => 'open 172.19.0.2:443\n' }, container, targets }),
    /answered for 1 of 2 targets/
  );
});

test('isolation holds only when the release server is reachable and every public target is not', () => {
  const release = '172.19.0.2';
  const blocked = [...EGRESS_TARGETS];
  assert.deepEqual(checkEgressIsolation({ open: [`${release}:443`], blocked, release }), {
    releaseServerReachable: `${release}:443`,
    blocked,
  });
  assert.throws(
    () => checkEgressIsolation({ open: [], blocked, release }),
    /was not reachable from the run network, so the reachability result says nothing/
  );
  assert.throws(
    () => checkEgressIsolation({ open: [`${release}:443`, '1.1.1.1:443'], blocked: blocked.slice(1), release }),
    /reached 1\.1\.1\.1:443/
  );
  assert(EGRESS_TARGETS.includes('1.1.1.1:443') && EGRESS_TARGETS.includes('cpamc.router-for.me:443'));
});

// ---------------------------------------------------------------- acceptance checks against a stand-in CPA

/**
 * A stand-in for the CPA surfaces the checks touch. `faults` switch one defect on.
 */
async function standInCpa(t, { panel, configPath, faults = {} }) {
  let retry = 3;
  fs.writeFileSync(configPath, 'host: "0.0.0.0"\nrequest-retry: 3\n');
  const server = http.createServer((req, res) => {
    const chunks = [];
    req.on('data', (chunk) => chunks.push(chunk));
    req.on('end', () => {
      const send = (status, body, type = 'application/json') => {
        res.writeHead(status, { 'content-type': type });
        res.end(body);
      };
      if (req.url === '/management.html') {
        if (faults.panelStatus) return send(faults.panelStatus, 'x', 'text/plain');
        return send(200, faults.servedPanel ?? panel, faults.contentType ?? 'text/html; charset=utf-8');
      }
      const authorized = req.headers.authorization === `Bearer ${KEY}`;
      if (!authorized && !faults.openApi) return send(401, '{"error":"invalid management key"}');
      if (req.url === '/v0/management/config') return send(faults.configStatus ?? 200, faults.configBody ?? '{"debug":false}');
      if (req.url === '/v0/management/request-retry') {
        if (req.method === 'PUT') {
          if (faults.putStatus) return send(faults.putStatus, '{}');
          retry = JSON.parse(Buffer.concat(chunks).toString()).value;
          if (!faults.noPersist) fs.writeFileSync(configPath, `host: "0.0.0.0"\nrequest-retry: ${retry}\n`);
          return send(200, '{"status":"ok"}');
        }
        return send(200, JSON.stringify({ 'request-retry': faults.staleRead ? 3 : retry }));
      }
      return send(404, 'not found', 'text/plain');
    });
  });
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise((resolve) => server.close(resolve)));
  return server.address().port;
}

test('a CPA that serves the build and honours the management contract passes every check', async (t) => {
  const dir = tempDir(t);
  const configPath = path.join(dir, 'config.yaml');
  const staticPath = path.join(dir, 'management.html');
  fs.writeFileSync(staticPath, GOOD_HTML);
  const port = await standInCpa(t, { panel: GOOD_HTML, configPath });

  assert.deepEqual(checkStaticPanel({ staticPath, panel: GOOD_HTML }), { bytes: GOOD_HTML.length, sha256: sha256(GOOD_HTML) });
  assert.deepEqual(await checkServedPanel({ port, panel: GOOD_HTML }), {
    status: 200,
    bytes: GOOD_HTML.length,
    sha256: sha256(GOOD_HTML),
  });
  assert.deepEqual(await checkManagementContract({ port, key: KEY, configPath }), {
    authenticated: true,
    field: 'request-retry',
    from: 3,
    to: 11,
    persisted: true,
  });
  assert.deepEqual(await checkPersistedAfterRestart({ port, key: KEY, configPath }), {
    restarted: true,
    field: 'request-retry',
    value: 11,
  });
});

test('served or stored bytes that differ from the build by one byte are rejected', async (t) => {
  const dir = tempDir(t);
  const configPath = path.join(dir, 'config.yaml');
  const staticPath = path.join(dir, 'management.html');
  const tampered = Buffer.from(GOOD_HTML);
  tampered[tampered.length - 2] ^= 1;

  fs.writeFileSync(staticPath, tampered);
  assert.throws(() => checkStaticPanel({ staticPath, panel: GOOD_HTML }), /not byte-identical/);
  fs.rmSync(staticPath);
  assert.throws(() => checkStaticPanel({ staticPath, panel: GOOD_HTML }), /does not exist/);

  const port = await standInCpa(t, { panel: GOOD_HTML, configPath, faults: { servedPanel: tampered } });
  await assert.rejects(checkServedPanel({ port, panel: GOOD_HTML }), /not byte-identical/);
});

test('a decoy outside MANAGEMENT_STATIC_PATH is never served nor overwritten', async (t) => {
  const dir = tempDir(t);
  const configPath = path.join(dir, 'config.yaml');
  const staticPath = path.join(dir, 'panel-static', 'management.html');
  const decoyPath = path.join(dir, 'static', 'management.html');
  fs.mkdirSync(path.dirname(staticPath), { recursive: true });
  fs.mkdirSync(path.dirname(decoyPath), { recursive: true });
  fs.writeFileSync(decoyPath, DECOY_CONFIG_STATIC_PANEL);
  fs.writeFileSync(staticPath, GOOD_HTML);

  assert.deepEqual(
    checkStaticPanel({ staticPath, panel: GOOD_HTML, decoyPath, decoy: DECOY_CONFIG_STATIC_PANEL }),
    { bytes: GOOD_HTML.length, sha256: sha256(GOOD_HTML), decoyUntouched: true }
  );

  // A CPA that wrote to the default location has ignored MANAGEMENT_STATIC_PATH.
  fs.writeFileSync(decoyPath, GOOD_HTML);
  assert.throws(
    () => checkStaticPanel({ staticPath, panel: GOOD_HTML, decoyPath, decoy: DECOY_CONFIG_STATIC_PANEL }),
    /wrote to .* although MANAGEMENT_STATIC_PATH/
  );
  fs.rmSync(decoyPath);
  assert.throws(
    () => checkStaticPanel({ staticPath, panel: GOOD_HTML, decoyPath, decoy: DECOY_CONFIG_STATIC_PANEL }),
    /decoy bundle .* disappeared/
  );

  // The decoy differs from the build, so serving it instead would be visible.
  const port = await standInCpa(t, { panel: GOOD_HTML, configPath, faults: { servedPanel: DECOY_CONFIG_STATIC_PANEL } });
  await assert.rejects(checkServedPanel({ port, panel: GOOD_HTML }), /not byte-identical/);
});

test('/management.html must be 200 text/html', async (t) => {
  const dir = tempDir(t);
  const configPath = path.join(dir, 'config.yaml');
  const missing = await standInCpa(t, { panel: GOOD_HTML, configPath, faults: { panelStatus: 404 } });
  await assert.rejects(checkServedPanel({ port: missing, panel: GOOD_HTML }), /answered 404, want 200/);
  const typed = await standInCpa(t, { panel: GOOD_HTML, configPath, faults: { contentType: 'application/octet-stream' } });
  await assert.rejects(checkServedPanel({ port: typed, panel: GOOD_HTML }), /content-type/);
});

test('the management contract rejects an open API, a refused key and a missing or stale update', async (t) => {
  const dir = tempDir(t);
  const configPath = path.join(dir, 'config.yaml');
  const run = async (faults, expected, extra = {}) => {
    const port = await standInCpa(t, { panel: GOOD_HTML, configPath, faults });
    await assert.rejects(checkManagementContract({ port, key: KEY, configPath, persistTimeoutMs: 400, ...extra }), expected);
  };
  await run({ openApi: true }, /without a key/);
  await run({ configStatus: 403 }, /refused by \/v0\/management\/config \(403\)/);
  await run({ configBody: '[]' }, /not a JSON object/);
  await run({ putStatus: 500 }, /PUT \/v0\/management\/request-retry answered 500/);
  await run({ staleRead: true }, /does not report the updated request-retry/);
  await run({ noPersist: true }, /timed out waiting for request-retry to be written to config\.yaml/);
});

test('a wrong key that is accepted is caught even when the right key works', async (t) => {
  const dir = tempDir(t);
  const configPath = path.join(dir, 'config.yaml');
  const port = await standInCpa(t, { panel: GOOD_HTML, configPath });
  const lenient = (options) =>
    options.headers?.authorization === `Bearer ${KEY}-wrong`
      ? Promise.resolve({ status: 200, headers: {}, body: Buffer.from('{}') })
      : fetchLike(options);
  const fetchLike = (options) =>
    new Promise((resolve, reject) => {
      http
        .request({ host: '127.0.0.1', ...options }, (res) => {
          const chunks = [];
          res.on('data', (c) => chunks.push(c));
          res.on('end', () => resolve({ status: res.statusCode, headers: res.headers, body: Buffer.concat(chunks) }));
        })
        .on('error', reject)
        .end(options.body);
    });
  const request = ({ port: p, path: requestPath, ...rest }) => lenient({ port: p, path: requestPath, ...rest });
  await assert.rejects(checkManagementContract({ port, key: KEY, configPath, request }), /accepted a wrong key/);
});

test('after a restart the persisted value must still be served and on disk', async (t) => {
  const dir = tempDir(t);
  const configPath = path.join(dir, 'config.yaml');
  const port = await standInCpa(t, { panel: GOOD_HTML, configPath, faults: { staleRead: true } });
  await assert.rejects(checkPersistedAfterRestart({ port, key: KEY, configPath }), /3 !== 11|Expected values/);
});

test('with the panel disabled only a 404 passes, and the API and stored bundle must remain', async (t) => {
  const dir = tempDir(t);
  const configPath = path.join(dir, 'config.yaml');
  const staticPath = path.join(dir, 'management.html');
  fs.writeFileSync(staticPath, GOOD_HTML);

  const disabled = await standInCpa(t, { panel: GOOD_HTML, configPath, faults: { panelStatus: 404 } });
  assert.deepEqual(await checkDisabledPanel({ port: disabled, key: KEY, staticPath, panel: GOOD_HTML }), {
    status: 404,
    managementApi: 200,
    storedBundleUntouched: true,
  });

  const stillServing = await standInCpa(t, { panel: GOOD_HTML, configPath });
  await assert.rejects(
    checkDisabledPanel({ port: stillServing, key: KEY, staticPath, panel: GOOD_HTML }),
    /answered 200 with the panel disabled, want 404/
  );
  const apiDown = await standInCpa(t, { panel: GOOD_HTML, configPath, faults: { panelStatus: 404, configStatus: 404 } });
  await assert.rejects(
    checkDisabledPanel({ port: apiDown, key: KEY, staticPath, panel: GOOD_HTML }),
    /must not disable the management API \(404\)/
  );
  fs.writeFileSync(staticPath, 'changed');
  await assert.rejects(
    checkDisabledPanel({ port: disabled, key: KEY, staticPath, panel: GOOD_HTML }),
    /not byte-identical/
  );
});

test('a placed panel is kept only while the release server stays silent', (t) => {
  const dir = tempDir(t);
  const staticPath = path.join(dir, 'management.html');
  fs.writeFileSync(staticPath, GOOD_HTML);
  assert.deepEqual(checkPlacedPanelKept({ staticPath, panel: GOOD_HTML, requests: [], since: 0 }), {
    bytes: GOOD_HTML.length,
    sha256: sha256(GOOD_HTML),
    releaseServerRequests: 0,
  });
  assert.throws(
    () => checkPlacedPanelKept({ staticPath, panel: GOOD_HTML, requests: ['/cpampd-lite/assets/management.html'], since: 0 }),
    /asked the release server for a panel/
  );
  assert.deepEqual(
    checkPlacedPanelKept({ staticPath, panel: GOOD_HTML, requests: ['/x'], since: 1 }),
    { bytes: GOOD_HTML.length, sha256: sha256(GOOD_HTML), releaseServerRequests: 0 },
    'requests before this case are not this case\'s contacts'
  );
});

test('waitFor returns the first truthy probe and names what timed out', async () => {
  let n = 0;
  assert.equal(await waitFor(() => ++n >= 3 && 'ready', { what: 'x', timeoutMs: 2_000, intervalMs: 5 }), 'ready');
  await assert.rejects(
    waitFor(() => Promise.reject(new Error('boom')), { what: 'the thing', timeoutMs: 50, intervalMs: 5 }),
    /timed out waiting for the thing: boom/
  );
});

// ---------------------------------------------------------------- CLI

test('arguments: defaults, every flag, and usage errors', () => {
  const defaults = parseArgs([]);
  assert.deepEqual(defaults.layouts, ['legacy', 'v8']);
  assert.deepEqual(defaults.staticPathModes, ['config-sibling-static', 'explicit-management-static-path']);
  assert.equal(defaults.panelSource, 'stub');
  assert.equal(defaults.panel, DEFAULT_PANEL);
  assert.equal(defaults.repository, 'seakee/CPA-Manager-Plus');
  assert(!defaults.bundleOnly && !defaults.skipBuild && !defaults.buildCpaImage);

  const parsed = parseArgs([
    '--bundle-only', '--skip-build', '--build-cpa-image', '--cpa-image', 'img:1', '--panel', 'x/management.html',
    '--panel-source', 'mounted', '--layouts', 'v8', '--static-path-modes', 'explicit-management-static-path',
    '--repository', 'o/r', '--report', 'out.json',
  ]);
  assert.equal(parsed.cpaImage, 'img:1');
  assert.deepEqual(parsed.layouts, ['v8']);
  assert.deepEqual(parsed.staticPathModes, ['explicit-management-static-path']);
  assert.equal(parsed.panelSource, 'mounted');
  assert.equal(parsed.repository, 'o/r');
  assert(path.isAbsolute(parsed.panel) && path.isAbsolute(parsed.report));

  for (const bad of [
    ['--nope'],
    ['--cpa-image'],
    ['--cpa-image', '--skip-build'],
    ['--panel-source', 'remote'],
    ['--layouts', 'legacy,v9'],
    ['--layouts', ','],
    ['--static-path-modes', 'config-sibling-static,other'],
    ['--static-path-modes', ','],
    ['--repository', 'not-a-repo'],
  ]) {
    assert.throws(() => parseArgs(bad), UsageError, bad.join(' '));
  }
});

const capture = () => {
  const streams = { out: '', err: '' };
  return {
    streams,
    stdout: { write: (text) => (streams.out += text) },
    stderr: { write: (text) => (streams.err += text) },
  };
};

test('--help and a bad flag exit without touching Docker', async () => {
  const touched = [];
  const dockerFactory = () => {
    touched.push('docker');
    throw new Error('docker must not be created');
  };
  const help = capture();
  assert.equal(await runAcceptance(['--help'], { ...help, dockerFactory }), 0);
  assert.match(help.streams.out, /--bundle-only/);
  const bad = capture();
  assert.equal(await runAcceptance(['--bogus'], { ...bad, dockerFactory }), 2);
  assert.match(bad.streams.err, /unknown argument: --bogus/);
  assert.equal(bad.streams.out, '');
  assert.deepEqual(touched, []);
});

test('--bundle-only verifies a panel directory without Docker and reports its digest', async (t) => {
  const dir = tempDir(t);
  const panel = path.join(dir, 'management.html');
  fs.writeFileSync(panel, GOOD_HTML);
  const ok = capture();
  assert.equal(await runAcceptance(['--bundle-only', '--skip-build', '--panel', panel], ok), 0);
  const report = JSON.parse(ok.streams.out);
  assert.equal(report.contract, 'cpamp.panel-lite-cpa-v8.v2');
  assert.equal(report.verdict, 'pass');
  assert.deepEqual(
    { bytes: report.panel.bytes, sha256: report.panel.sha256, singleFile: report.panel.singleFile },
    { bytes: GOOD_HTML.length, sha256: sha256(GOOD_HTML), singleFile: true }
  );
  assert.deepEqual(report.phases, { build: { requested: false, built: false } });
  assert.equal(report.cleanup.ok, true);
  assert.equal(report.cpa, undefined, 'bundle-only never reaches Docker');

  fs.writeFileSync(path.join(dir, 'chunk.js'), '1');
  const sibling = capture();
  assert.equal(await runAcceptance(['--bundle-only', '--skip-build', '--panel', panel], sibling), 1);
  const failed = JSON.parse(sibling.streams.out);
  assert.equal(failed.verdict, 'fail');
  assert.match(failed.error, /one HTML artifact/);
});

test('a built panel that references assets/ fails the run', async (t) => {
  const dir = tempDir(t);
  const panel = path.join(dir, 'management.html');
  fs.writeFileSync(panel, GOOD_HTML.toString().replace('<script>1</script>', '<script>1</script><link rel="stylesheet" href="./assets/app.css">'));
  const run = capture();
  assert.equal(await runAcceptance(['--bundle-only', '--skip-build', '--panel', panel], run), 1);
  assert.match(JSON.parse(run.streams.out).error, /sibling resources/);
});

test('the user documentation states the v2 Beta Lite contract in both languages', () => {
  const read = (file) => fs.readFileSync(path.join(ROOT, file), 'utf8');
  for (const [file, prerelease] of [
    ['apps/docs/deployment/cpa-panel.md', '预发布'],
    ['apps/docs/en/deployment/cpa-panel.md', 'prerelease'],
  ]) {
    const doc = read(file);
    for (const needle of [
      prerelease,
      'releases/latest',
      '`management.html`',
      '/v0/management',
      '/v8/management',
      'disable-auto-update-panel: true',
      'static/management.html',
      'panel-github-repository',
      'CPA v8',
    ]) {
      assert(doc.includes(needle), `${file} must mention ${needle}`);
    }
    assert(!doc.includes('remote-management.panel-repo'), file);
  }
  // P3-1/P3-2: the cache lives next to config.yaml, and only the api.github.com
  // URL form gets the /releases/latest suffix; the browser form stays 1.x.
  for (const [file, needles] of [
    ['apps/docs/deployment/cpa-panel.md', ['`config.yaml` 同级的 `static/`', 'cpamc.router-for.me', '`api.github.com` 形式', 'github.com/<owner>/<repo>/releases/tag/<tag>']],
    ['apps/docs/en/deployment/cpa-panel.md', ["next to CPA's `config.yaml`", 'cpamc.router-for.me', '`api.github.com` URL', 'github.com/<owner>/<repo>/releases/tag/<tag>']],
  ]) {
    const doc = read(file);
    for (const needle of needles) assert(doc.includes(needle), `${file} must mention ${needle}`);
    assert(!doc.includes('working directory') && !doc.includes('工作目录'), `${file} must not say working directory`);
  }
});

// ---------------------------------------------------------------- image identity

const PIN = loadCpaV8Pin();

const identityProbeOutput = ({
  sha = PIN.executableSha256,
  version = PIN.version,
  artifactId = `sha256:${sha}`,
  manifestLine,
  banner,
} = {}) =>
  [
    `${sha}  /usr/local/bin/cli-proxy-api`,
    manifestLine ?? JSON.stringify({ version, artifactId }),
    banner ?? `Version: ${version}`,
  ].join('\n');

function identityDocker({
  info = { id: 'sha256:good', os: 'linux', architecture: 'amd64' },
  available = true,
  output = identityProbeOutput(),
} = {}) {
  const probeCalls = [];
  return {
    probeCalls,
    imageInfo: () => (available ? info : undefined),
    probeImage: (spec) => {
      probeCalls.push(spec);
      return output;
    },
  };
}

test('an image passes identity only when it matches the Dockerfile.runtime pin, and it is probed by image id', () => {
  const docker = identityDocker();
  const result = verifyImageIdentity({ docker, image: 'cpampd-lite-cpa-v8:tag', pin: PIN, runId: RUN });
  assert.deepEqual(result, {
    image: 'cpampd-lite-cpa-v8:tag',
    imageId: 'sha256:good',
    version: PIN.version,
    executableSha256: PIN.executableSha256,
  });
  assert.deepEqual(
    docker.probeCalls,
    [{ name: `${CONTAINER_PREFIX}${RUN}-image-probe`, image: 'sha256:good' }],
    'the probe runs the verified image id, never the mutable tag'
  );
});

test('an image that differs from the pin in any probed field is refused', () => {
  const verify = (docker) => () => verifyImageIdentity({ docker, image: 'cpampd-lite-cpa-v8:tag', pin: PIN, runId: RUN });
  const sha = 'f'.repeat(64);
  const cases = [
    ['not available locally', identityDocker({ available: false }), /is not available locally/],
    [
      'arm64 image',
      identityDocker({ info: { id: 'sha256:arm', os: 'linux', architecture: 'arm64' } }),
      /is linux\/arm64, want linux\/amd64/,
    ],
    ['wrong executable sha256', identityDocker({ output: identityProbeOutput({ sha }) }), /carries CPA/],
    [
      'wrong cpa-artifact.json version',
      identityDocker({ output: identityProbeOutput({ version: '8.0.12' }) }),
      /cpa-artifact\.json version differs from the pin/,
    ],
    [
      'wrong cpa-artifact.json artifactId',
      identityDocker({ output: identityProbeOutput({ artifactId: `sha256:${sha}` }) }),
      /cpa-artifact\.json artifactId differs from the pin/,
    ],
    [
      'malformed manifest line',
      identityDocker({ output: identityProbeOutput({ manifestLine: '{not json' }) }),
      /cpa-artifact\.json in .* is malformed/,
    ],
    [
      'manifest that is not an object',
      identityDocker({ output: identityProbeOutput({ manifestLine: '[]' }) }),
      /cpa-artifact\.json is not a JSON object/,
    ],
    ['wrong Version: banner', identityDocker({ output: identityProbeOutput({ banner: 'Version: 8.0.12' }) }), /want Version: /],
  ];
  for (const [label, docker, expected] of cases) {
    assert.throws(verify(docker), expected, label);
  }
  assert.throws(verify(identityDocker({ available: false })), UsageError);
});

// ---------------------------------------------------------------- acceptance flow

/** A stand-in CPA for one container: reads the key from the config it was given. */
async function startContainerStandIn(t, { configDir, disabled = false }) {
  const configPath = path.join(configDir, 'config.yaml');
  const server = http.createServer((req, res) => {
    const chunks = [];
    req.on('data', (chunk) => chunks.push(chunk));
    req.on('end', () => {
      const send = (status, body, type = 'application/json') => {
        res.writeHead(status, { 'content-type': type });
        res.end(body);
      };
      if (req.url === '/management.html') {
        if (disabled) return send(404, 'not found', 'text/plain');
        return send(200, GOOD_HTML, 'text/html; charset=utf-8');
      }
      const text = fs.existsSync(configPath) ? fs.readFileSync(configPath, 'utf8') : '';
      const key = /secret-key: "([^"]+)"/.exec(text)?.[1];
      if (!key || req.headers.authorization !== `Bearer ${key}`) {
        return send(401, '{"error":"invalid management key"}');
      }
      if (req.url === '/v0/management/config') return send(200, '{}');
      if (req.url === '/v0/management/request-retry') {
        if (req.method === 'PUT') {
          const value = JSON.parse(Buffer.concat(chunks).toString('utf8')).value;
          fs.writeFileSync(configPath, text.replace(/^([ \t]*request-retry:)[ \t]*-?\d+/m, `$1 ${value}`));
          return send(200, '{"status":"ok"}');
        }
        return send(200, JSON.stringify({ 'request-retry': persistedRequestRetry(text) }));
      }
      return send(404, 'not found', 'text/plain');
    });
  });
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  t.after(() => new Promise((resolve) => server.close(resolve)));
  return { port: server.address().port };
}

const configDirOf = (args) => {
  const at = args.indexOf('-v');
  return args[at + 1].split(':')[0];
};
const roleOf = (name) => name.split('-').at(-1);

/** A Docker double that records what a real container would have been asked to run. */
function fakeAcceptanceDocker({ releaseAddress = '172.19.0.2', onStart = () => {}, releaseFailures = [], logsOf = () => '' } = {}) {
  const starts = [];
  const probeCalls = [];
  const rm = [];
  const networks = [];
  const createdNetworks = [];
  const containers = [];
  let releaseAllCalls = 0;
  return {
    starts,
    probeCalls,
    rm,
    networks,
    createdNetworks,
    get releaseAllCalls() {
      return releaseAllCalls;
    },
    available: () => 'linux/amd64',
    imageInfo: () => ({ id: 'sha256:good', os: 'linux', architecture: 'amd64' }),
    probeImage: (spec) => {
      probeCalls.push(spec);
      return identityProbeOutput();
    },
    createNetwork(name) {
      networks.push(name);
      createdNetworks.push(name);
    },
    start(spec) {
      starts.push(spec);
      containers.push(spec.name);
      onStart(spec);
    },
    address() {
      return releaseAddress;
    },
    execInContainer(args) {
      if (args[5] === 'cpampd-lite-probe') {
        return args
          .slice(6)
          .map((target) => `${target === `${releaseAddress}:443` ? 'open' : 'blocked'} ${target}`)
          .join('\n');
      }
      throw new Error(`the Docker double does not run ${args[5]}`);
    },
    restart() {},
    logs() {
      return logsOf();
    },
    releaseAll() {
      releaseAllCalls += 1;
      for (const name of containers.splice(0)) rm.push(['rm', '-f', name]);
      for (const name of networks.splice(0)) rm.push(['network', 'rm', name]);
      return [...releaseFailures];
    },
  };
}

/** Requests reach the stand-in CPA for that exact container. */
const requestFactoryFor = (servers) => ({ container }) => async (options) => {
  const entry = servers.get(container);
  assert(entry, `no stand-in CPA for ${container}`);
  const { port } = await entry;
  return httpRequest({ ...options, port });
};

const mountedArgs = (panelPath, staticPathModes, layouts = 'legacy') => [
  '--skip-build',
  '--panel',
  panelPath,
  '--cpa-image',
  'img:tag',
  '--panel-source',
  'mounted',
  '--layouts',
  layouts,
  '--static-path-modes',
  staticPathModes,
];

test('mounted mode keeps the placed panel in both static path modes and never contacts the release server', async (t) => {
  const panelPath = path.join(tempDir(t), 'management.html');
  fs.writeFileSync(panelPath, GOOD_HTML);
  const tmpBase = fs.mkdtempSync(path.join(os.tmpdir(), 'cpampd-lite-test-base-'));
  t.after(() => fs.rmSync(tmpBase, { recursive: true, force: true }));

  const servers = new Map();
  const captured = new Map();
  const docker = fakeAcceptanceDocker({
    onStart: (spec) => {
      if (roleOf(spec.name) === 'server') return; // the release server is not a CPA stand-in
      const dir = configDirOf(spec.args);
      const explicit = spec.args.includes('MANAGEMENT_STATIC_PATH=/panel-static');
      const capturedHere = { decoy: undefined, placed: undefined };
      if (roleOf(spec.name) !== 'fallback') {
        capturedHere.decoy = explicit ? fs.readFileSync(path.join(dir, 'static', 'management.html')) : undefined;
        capturedHere.placed = fs.readFileSync(
          path.join(dir, explicit ? 'panel-static' : 'static', 'management.html')
        );
      }
      captured.set(spec.name, capturedHere);
      servers.set(spec.name, startContainerStandIn(t, { configDir: dir, disabled: roleOf(spec.name) !== 'enabled' }));
    },
  });

  const run = capture();
  const code = await runAcceptance(mountedArgs(panelPath, 'config-sibling-static,explicit-management-static-path'), {
    dockerFactory: () => docker,
    requestFactory: requestFactoryFor(servers),
    certificateFactory: () => ({ key: 'k', cert: 'c' }),
    stdout: run.stdout,
    stderr: run.stderr,
    tmpBase,
    signals: new EventEmitter(),
  });

  assert.equal(code, 0, `${run.streams.err}\n${run.streams.out}`);
  const report = JSON.parse(run.streams.out);
  assert.equal(report.verdict, 'pass');
  assert.deepEqual(report.phases.build, { requested: false, built: false });
  assert.equal(report.phases.runtime.network, docker.createdNetworks[0]);
  assert.match(report.phases.runtime.network, /^cpampd-lite-[0-9a-f]{8}-net$/);
  assert.equal(report.phases.runtime.internal, true);
  assert.equal(report.phases.runtime.publishedPorts, 0);
  assert.equal(report.phases.runtime.probes, 'docker-exec');

  assert.equal(report.cases.length, 2);
  const [sibling, explicit] = report.cases;
  assert.equal(sibling.configLayout, 'legacy');
  assert.equal(sibling.staticPathMode, 'config-sibling-static');
  assert.equal(explicit.configLayout, 'legacy');
  assert.equal(explicit.staticPathMode, 'explicit-management-static-path');
  for (const entry of report.cases) {
    assert.equal(entry.enabled.panelSource, 'mounted');
    assert.equal(entry.enabled.static.sha256, sha256(GOOD_HTML));
    assert.equal(entry.enabled.served.sha256, sha256(GOOD_HTML));
    assert.equal(entry.enabled.servedAfterRestart.sha256, sha256(GOOD_HTML));
    assert.deepEqual(entry.enabled.placedPanelKept, entry.enabled.placedPanelKeptAfterRestart);
    assert.equal(entry.enabled.placedPanelKept.releaseServerRequests, 0);
    assert.equal(entry.enabled.management.persisted, true);
    assert.equal(entry.enabled.afterRestart.value, 11);
    assert.equal(entry.disabled.status, 404);
    assert.equal(entry.disabled.releaseServerRequests, 0);
  }
  assert.equal(sibling.enabled.static.decoyUntouched, undefined, 'the sibling layout has no decoy');
  assert.equal(explicit.enabled.static.decoyUntouched, true, 'the explicit layout keeps its decoy');

  const cpaStarts = docker.starts.filter((spec) => ['enabled', 'disabled'].includes(roleOf(spec.name)));
  assert.equal(cpaStarts.length, 4, 'enabled and disabled for two static path modes');
  for (const spec of cpaStarts) {
    assert.equal(spec.args[spec.args.indexOf('--network') + 1], report.phases.runtime.network, spec.name);
    assert.match(spec.args[spec.args.indexOf('--label') + 1], /^cpamp\.panel-lite\.run=[0-9a-f]{32}$/);
    assert(!spec.args.includes('-p') && !spec.args.includes('-P'), 'no host port is published');
    assert.equal(
      spec.args.includes('MANAGEMENT_STATIC_PATH=/panel-static'),
      spec.name.includes('-explicit-'),
      spec.name
    );
  }
  const explicitEnabled = cpaStarts.find((spec) => spec.name.endsWith('-explicit-enabled'));
  assert(captured.get(explicitEnabled.name).decoy.equals(DECOY_CONFIG_STATIC_PANEL), 'the decoy is placed outside the configured path');
  assert(captured.get(explicitEnabled.name).placed.equals(GOOD_HTML), 'the placed bundle sits at the configured path');

  const releaseStart = docker.starts.find((spec) => spec.name.endsWith('-release-server'));
  assert(releaseStart.args.includes('443') && releaseStart.args.includes('-WWW'));

  const fallback = docker.starts.find((spec) => spec.name.endsWith('-isolation-fallback'));
  assert.equal(fallback.args[fallback.args.indexOf('--add-host') + 1], 'api.github.com:127.0.0.1');
  const emptyFallback = docker.starts.find((spec) => spec.name.endsWith('-isolation-empty-fallback'));
  assert.equal(emptyFallback.args[emptyFallback.args.indexOf('--add-host') + 1], 'api.github.com:172.19.0.2');
  assert.equal(report.isolation.unreachable.releaseServer, '127.0.0.1 (unreachable)');
  assert.match(report.isolation.releaseServerWithoutPanel.releaseServer, /^172\.19\.0\.2 \(serves no panel\)$/);
  for (const scenario of [report.isolation.unreachable, report.isolation.releaseServerWithoutPanel]) {
    assert.equal(scenario.panelServed, 404);
    assert.equal(scenario.stored, false);
    assert.equal(scenario.releaseServerReachable, '172.19.0.2:443');
    assert.deepEqual(scenario.blocked, EGRESS_TARGETS);
  }

  assert.deepEqual(report.cleanup.failures, []);
  assert.equal(report.cleanup.ok, true);
  assert.deepEqual(docker.rm, [
    ...docker.starts.map((spec) => ['rm', '-f', spec.name]),
    ['network', 'rm', docker.createdNetworks[0]],
  ]);
  assert.deepEqual(fs.readdirSync(tmpBase), [], 'the run tmpdir is removed');
});

test('stub mode accepts the served download and records the two release-server requests', async (t) => {
  const panelPath = path.join(tempDir(t), 'management.html');
  fs.writeFileSync(panelPath, GOOD_HTML);
  const tmpBase = fs.mkdtempSync(path.join(os.tmpdir(), 'cpampd-lite-test-base-'));
  t.after(() => fs.rmSync(tmpBase, { recursive: true, force: true }));
  const paths = releasePaths('seakee/CPA-Manager-Plus');

  const servers = new Map();
  let log = 'Using default temp DH parameters\nACCEPT\n';
  const docker = fakeAcceptanceDocker({
    onStart: (spec) => {
      if (roleOf(spec.name) === 'server') return; // the release server is not a CPA stand-in
      const dir = configDirOf(spec.args);
      const role = roleOf(spec.name);
      const explicit = spec.args.includes('MANAGEMENT_STATIC_PATH=/panel-static');
      servers.set(spec.name, startContainerStandIn(t, { configDir: dir, disabled: role !== 'enabled' }));
      // A CPA that downloaded the panel stores it at the configured static path.
      if (role === 'enabled') {
        fs.mkdirSync(path.join(dir, explicit ? 'panel-static' : 'static'), { recursive: true });
        fs.writeFileSync(path.join(dir, explicit ? 'panel-static' : 'static', 'management.html'), GOOD_HTML);
        log += `FILE:${paths.latest.slice(1)}\nFILE:${paths.asset.slice(1)}\n`;
      }
    },
    logsOf: () => log,
  });

  const run = capture();
  const code = await runAcceptance(
    ['--skip-build', '--panel', panelPath, '--cpa-image', 'img:tag', '--layouts', 'v8',
      '--static-path-modes', 'explicit-management-static-path'],
    {
      dockerFactory: () => docker,
      requestFactory: requestFactoryFor(servers),
      certificateFactory: () => ({ key: 'k', cert: 'c' }),
      stdout: run.stdout,
      stderr: run.stderr,
      tmpBase,
      signals: new EventEmitter(),
    }
  );

  assert.equal(code, 0, `${run.streams.err}\n${run.streams.out}`);
  const report = JSON.parse(run.streams.out);
  assert.equal(report.verdict, 'pass');
  const [entry] = report.cases;
  assert.equal(entry.configLayout, 'v8');
  assert.equal(entry.staticPathMode, 'explicit-management-static-path');
  assert.equal(entry.enabled.panelSource, 'stub');
  assert.deepEqual(entry.enabled.releaseServer, {
    latestServed: true,
    assetServed: true,
    requests: [paths.latest, paths.asset],
  });
  assert.equal(entry.enabled.static.sha256, sha256(GOOD_HTML));
  assert.equal(entry.enabled.static.decoyUntouched, true);
  assert.equal(entry.enabled.management.persisted, true);
  assert.equal(entry.disabled.status, 404);
  assert.equal(entry.disabled.releaseServerRequests, 0);
  assert.equal(report.cleanup.ok, true);
});

// ---------------------------------------------------------------- failure and signal cleanup

const failingArgs = (panelPath) => mountedArgs(panelPath, 'config-sibling-static');

test('a failed case releases every container and the run network, and exits 1', async (t) => {
  const panelPath = path.join(tempDir(t), 'management.html');
  fs.writeFileSync(panelPath, GOOD_HTML);
  const tmpBase = fs.mkdtempSync(path.join(os.tmpdir(), 'cpampd-lite-test-base-'));
  t.after(() => fs.rmSync(tmpBase, { recursive: true, force: true }));
  const docker = fakeAcceptanceDocker();
  const run = capture();

  const code = await runAcceptance(failingArgs(panelPath), {
    dockerFactory: () => docker,
    certificateFactory: () => ({ key: 'k', cert: 'c' }),
    stdout: run.stdout,
    stderr: run.stderr,
    tmpBase,
    signals: new EventEmitter(),
    caseRunner: async ({ runId, docker: d }) => {
      d.start({ name: containerName(runId, 'legacy-sibling', 'enabled'), args: ['run', '-d'] });
      throw new Error('the stored bundle is not byte-identical to the build');
    },
    isolationRunner: async () => ({ skipped: true }),
  });

  assert.equal(code, 1);
  const report = JSON.parse(run.streams.out);
  assert.equal(report.verdict, 'fail');
  assert.match(report.error, /not byte-identical/);
  assert.deepEqual(report.cleanup.failures, []);
  assert.equal(report.cleanup.ok, true);
  assert.equal(report.cleanup.runId.length, 8);
  assert.deepEqual(docker.rm, [
    ...docker.starts.map((spec) => ['rm', '-f', spec.name]),
    ['network', 'rm', docker.createdNetworks[0]],
  ]);
  assert.deepEqual(fs.readdirSync(tmpBase), [], 'the run tmpdir is removed');
});

test('a cleanup failure is reported and forces exit 1 even when every check passed', async (t) => {
  const panelPath = path.join(tempDir(t), 'management.html');
  fs.writeFileSync(panelPath, GOOD_HTML);
  const tmpBase = fs.mkdtempSync(path.join(os.tmpdir(), 'cpampd-lite-test-base-'));
  t.after(() => fs.rmSync(tmpBase, { recursive: true, force: true }));
  const docker = fakeAcceptanceDocker({ releaseFailures: ['cpampd-lite-stuck: device busy'] });
  const run = capture();

  const code = await runAcceptance(failingArgs(panelPath), {
    dockerFactory: () => docker,
    certificateFactory: () => ({ key: 'k', cert: 'c' }),
    stdout: run.stdout,
    stderr: run.stderr,
    tmpBase,
    signals: new EventEmitter(),
    caseRunner: async () => ({ configLayout: 'legacy', staticPathMode: 'config-sibling-static' }),
    isolationRunner: async () => ({ skipped: true }),
  });

  assert.equal(code, 1, 'a failed release makes the run non-zero');
  const report = JSON.parse(run.streams.out);
  assert.equal(report.verdict, 'fail');
  assert.deepEqual(report.cleanup, {
    ok: false,
    failures: ['cpampd-lite-stuck: device busy'],
    runId: report.cleanup.runId,
  });
});

test('a temporary-directory removal error is reported and still writes the report', async (t) => {
  const panelPath = path.join(tempDir(t), 'management.html');
  fs.writeFileSync(panelPath, GOOD_HTML);
  const tmpBase = fs.mkdtempSync(path.join(os.tmpdir(), 'cpampd-lite-test-base-'));
  t.after(() => fs.rmSync(tmpBase, { recursive: true, force: true }));
  const docker = fakeAcceptanceDocker();
  const run = capture();
  const removed = [];

  const code = await runAcceptance(failingArgs(panelPath), {
    dockerFactory: () => docker,
    certificateFactory: () => ({ key: 'k', cert: 'c' }),
    stdout: run.stdout,
    stderr: run.stderr,
    tmpBase,
    signals: new EventEmitter(),
    caseRunner: async () => ({ configLayout: 'legacy', staticPathMode: 'config-sibling-static' }),
    isolationRunner: async () => ({ skipped: true }),
    removeTempRoot: (dir) => {
      removed.push(dir);
      throw new Error('EBUSY: resource busy or locked');
    },
  });

  assert.equal(code, 1, 'a temporary directory that cannot be removed makes the run non-zero');
  const report = JSON.parse(run.streams.out);
  assert.equal(report.verdict, 'fail');
  assert.equal(report.cleanup.ok, false);
  assert.equal(removed.length, 1);
  assert.match(report.cleanup.failures[0], /^tmpdir .*: EBUSY: resource busy or locked$/);
  assert.match(report.finishedAt, /^\d{4}-/);
});

for (const [signal, expectedExit] of [
  ['SIGINT', 130],
  ['SIGTERM', 143],
]) {
  test(`${signal} releases the run's resources and exits ${expectedExit}`, async (t) => {
    const panelPath = path.join(tempDir(t), 'management.html');
    fs.writeFileSync(panelPath, GOOD_HTML);
    const tmpBase = fs.mkdtempSync(path.join(os.tmpdir(), 'cpampd-lite-test-base-'));
    t.after(() => fs.rmSync(tmpBase, { recursive: true, force: true }));
    const docker = fakeAcceptanceDocker();
    const signals = new EventEmitter();
    const exits = [];
    let resolveExit;
    const exited = new Promise((resolve) => {
      resolveExit = resolve;
    });
    const run = capture();

    const code = await runAcceptance(failingArgs(panelPath), {
      dockerFactory: () => docker,
      certificateFactory: () => ({ key: 'k', cert: 'c' }),
      stdout: run.stdout,
      stderr: run.stderr,
      tmpBase,
      signals,
      exit: (value) => {
        exits.push(value);
        resolveExit();
      },
      caseRunner: async ({ runId, docker: d }) => {
        d.start({ name: containerName(runId, 'legacy-sibling', 'enabled'), args: ['run', '-d'] });
        signals.emit(signal);
        await exited;
        throw new Error(`${signal} interrupted the run`);
      },
      isolationRunner: async () => ({ skipped: true }),
    });

    assert.deepEqual(exits, [expectedExit]);
    assert.match(run.streams.err, new RegExp(`${signal}: releasing ${CONTAINER_PREFIX}`));
    assert.deepEqual(docker.rm, [
      ...docker.starts.map((spec) => ['rm', '-f', spec.name]),
      ['network', 'rm', docker.createdNetworks[0]],
    ]);
    assert.deepEqual(fs.readdirSync(tmpBase), []);
    assert.equal(code, 1);
  });
}

test('a signal and the run exit share a single cleanup execution', async (t) => {
  const panelPath = path.join(tempDir(t), 'management.html');
  fs.writeFileSync(panelPath, GOOD_HTML);
  const tmpBase = fs.mkdtempSync(path.join(os.tmpdir(), 'cpampd-lite-test-base-'));
  t.after(() => fs.rmSync(tmpBase, { recursive: true, force: true }));
  const docker = fakeAcceptanceDocker();
  const signals = new EventEmitter();
  let resolveExit;
  const exited = new Promise((resolve) => {
    resolveExit = resolve;
  });
  const removed = [];
  const run = capture();

  const code = await runAcceptance(failingArgs(panelPath), {
    dockerFactory: () => docker,
    certificateFactory: () => ({ key: 'k', cert: 'c' }),
    stdout: run.stdout,
    stderr: run.stderr,
    tmpBase,
    signals,
    exit: () => resolveExit(),
    removeTempRoot: (dir) => {
      removed.push(dir);
      fs.rmSync(dir, { recursive: true, force: true });
    },
    caseRunner: async ({ runId, docker: d }) => {
      d.start({ name: containerName(runId, 'legacy-sibling', 'enabled'), args: ['run', '-d'] });
      signals.emit('SIGTERM');
      await exited;
      throw new Error('SIGTERM interrupted the run');
    },
    isolationRunner: async () => ({ skipped: true }),
  });

  assert.equal(docker.releaseAllCalls, 1, 'the signal handler and the run exit wait on one cleanup');
  assert.equal(removed.length, 1, 'the temporary directory is removed once');
  assert.deepEqual(docker.rm, [
    ...docker.starts.map((spec) => ['rm', '-f', spec.name]),
    ['network', 'rm', docker.createdNetworks[0]],
  ]);
  assert.equal(code, 1);
});
