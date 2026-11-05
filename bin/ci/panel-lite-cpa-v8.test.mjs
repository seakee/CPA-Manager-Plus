// Behavioural tests for `panel-lite-cpa-v8.mjs`. They run without Docker and
// without a CPA: the pin reader, the bundle contract, the CPA config renderer,
// the release stub (over a real CONNECT + TLS handshake), the Docker namespace
// guard and every acceptance check are driven through their own contracts, so a
// regression fails here instead of only on a host that can run the real CPA v8.
//
// Run with:
//   node --test bin/ci/panel-lite-cpa-v8.test.mjs

import assert from 'node:assert/strict';
import crypto from 'node:crypto';
import { EventEmitter } from 'node:events';
import fs from 'node:fs';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import test from 'node:test';
import tls from 'node:tls';

import {
  BUILD_PANEL_COMMAND,
  CONTAINER_PREFIX,
  DECOY_1X_PANEL,
  DEFAULT_PANEL,
  LAYOUTS,
  ROOT,
  STUB_TAG,
  UsageError,
  assertOwnedName,
  assertSingleFilePanel,
  buildPanel,
  checkDisabledPanel,
  checkManagementContract,
  checkPersistedAfterRestart,
  checkServedPanel,
  checkStaticPanel,
  checkStubTraffic,
  containerName,
  createDocker,
  createReleaseStub,
  defaultImageTag,
  dockerRunArgs,
  generateStubCertificate,
  identityProbeArgs,
  loadCpaV8Pin,
  parseArgs,
  persistedRequestRetry,
  readCpaV8Pin,
  releaseDocument,
  renderCpaConfig,
  runAcceptance,
  verifyImageIdentity,
  waitFor,
} from './panel-lite-cpa-v8.mjs';

const sha256 = (bytes) => crypto.createHash('sha256').update(bytes).digest('hex');
const GOOD_HTML = Buffer.from(
  '<!doctype html><html><head><style>a{color:red}</style></head><body><script>1</script></body></html>'
);
const KEY = 'cpampd-lite-key-test';
const RUN = '0123abcd';

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
  const config = renderCpaConfig({ layout: 'legacy', ...configOptions, proxyUrl: 'http://host.docker.internal:1' });
  assert.match(config, /^remote-management:\n {2}allow-remote: true\n {2}secret-key: "cpampd-lite-key-test"\n/m);
  assert.match(config, /^ {2}disable-control-panel: false$/m);
  assert.match(config, /^ {2}disable-auto-update-panel: true$/m);
  assert.match(config, /^ {2}panel-github-repository: "https:\/\/github\.com\/seakee\/CPA-Manager-Plus"$/m);
  assert.match(config, /^proxy-url: "http:\/\/host\.docker\.internal:1"$/m);
  assert.match(config, /^request-retry: 3$/m);
  assert.doesNotMatch(config, /config-version|^management:/m);
  assert.equal(persistedRequestRetry(config), 3);
});

test('the v8 layout moves the same fields under management, routing and requests', () => {
  const config = renderCpaConfig({ layout: 'v8', ...configOptions, disableControlPanel: true, disableAutoUpdatePanel: false });
  assert.match(config, /^config-version: 8$/m);
  assert.match(config, /^management:\n {2}allow-remote: true\n {2}secret-key: "cpampd-lite-key-test"\n/m);
  assert.match(config, /^ {2}disable-control-panel: true$/m);
  assert.match(config, /^ {2}disable-auto-update-panel: false$/m);
  assert.match(config, /^ {2}panel-github-repository: "https:\/\/github\.com\/seakee\/CPA-Manager-Plus"$/m);
  assert.match(config, /^routing:\n {2}retry:\n {4}request-retry: 3$/m);
  assert.doesNotMatch(config, /remote-management|^requests:/m);
  assert.match(
    renderCpaConfig({ layout: 'v8', ...configOptions, proxyUrl: 'http://h:2' }),
    /^requests:\n {2}proxy-url: "http:\/\/h:2"$/m
  );
  assert.equal(persistedRequestRetry(config), 3);
});

test('config values that need YAML escaping and unknown layouts are refused', () => {
  assert.throws(() => renderCpaConfig({ layout: 'legacy', ...configOptions, managementKey: 'a"b' }), /unsafe config value/);
  assert.throws(() => renderCpaConfig({ layout: 'legacy', ...configOptions, repository: 'x\ny: z' }), /unsafe config value/);
  assert.throws(() => renderCpaConfig({ layout: 'v9', ...configOptions }), /unknown config layout/);
  assert.deepEqual(LAYOUTS, ['legacy', 'v8']);
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

// ---------------------------------------------------------------- release stub

async function throughStub(stub, { target = `api.github.com:443`, method = 'GET', requestPath }) {
  const tunnel = await new Promise((resolve, reject) => {
    const req = http.request({ host: '127.0.0.1', port: stub.port, method: 'CONNECT', path: target });
    req.on('connect', (res, socket) => {
      if (res.statusCode === 200) resolve(socket);
      else {
        socket.destroy();
        reject(Object.assign(new Error(`CONNECT ${target} answered ${res.statusCode}`), { status: res.statusCode }));
      }
    });
    req.on('error', reject);
    req.end();
  });
  const secure = tls.connect({ socket: tunnel, servername: 'api.github.com', ca: stub.caPem });
  const chunks = [];
  secure.write(`${method} ${requestPath} HTTP/1.1\r\nHost: api.github.com\r\nConnection: close\r\n\r\n`);
  await new Promise((resolve, reject) => {
    secure.on('data', (chunk) => chunks.push(chunk));
    secure.on('end', resolve);
    secure.on('error', reject);
  });
  const raw = Buffer.concat(chunks);
  const split = raw.indexOf('\r\n\r\n');
  return { status: Number(/^HTTP\/1\.1 (\d+)/.exec(raw.toString('latin1', 0, split))[1]), body: raw.subarray(split + 4) };
}

test('the release stub answers for api.github.com only, over verified TLS', async (t) => {
  const certificate = generateStubCertificate();
  const stub = await createReleaseStub({
    panel: GOOD_HTML,
    repository: 'seakee/CPA-Manager-Plus',
    certificate,
    host: '127.0.0.1',
  });
  t.after(() => stub.close());

  const release = await throughStub(stub, { requestPath: stub.latestPath });
  assert.equal(release.status, 200);
  const document = JSON.parse(release.body.toString('utf8'));
  assert.deepEqual(document, releaseDocument({ repository: 'seakee/CPA-Manager-Plus', panel: GOOD_HTML }));
  assert.equal(document.tag_name, STUB_TAG);
  assert.equal(document.prerelease, false);
  const [asset] = document.assets;
  assert.equal(asset.name, 'management.html');
  assert.equal(asset.digest, `sha256:${sha256(GOOD_HTML)}`);
  assert.equal(asset.size, GOOD_HTML.length);
  assert(asset.browser_download_url.startsWith('https://api.github.com/'));

  const download = await throughStub(stub, { requestPath: stub.assetPath });
  assert.equal(download.status, 200);
  assert(download.body.equals(GOOD_HTML), 'the asset is served byte for byte');

  assert.equal((await throughStub(stub, { requestPath: '/repos/other/repo/releases/latest' })).status, 404);
  assert.equal((await throughStub(stub, { requestPath: `${stub.latestPath}/..` })).status, 404);

  // Any other CONNECT target, and any plain proxied request, is refused.
  await assert.rejects(throughStub(stub, { target: 'raw.githubusercontent.com:443', requestPath: '/' }), { status: 403 });
  const plain = await new Promise((resolve, reject) => {
    http
      .request({ host: '127.0.0.1', port: stub.port, path: 'http://example.com/', headers: { host: 'example.com' } }, resolve)
      .on('error', reject)
      .end();
  });
  assert.equal(plain.statusCode, 403);
  plain.resume();

  const kinds = stub.events.map((e) => e.kind);
  assert(kinds.includes('deny-connect') && kinds.includes('plain'));
  assert.deepEqual(
    stub.events.filter((e) => e.kind === 'deny-connect').map((e) => e.target),
    ['raw.githubusercontent.com:443']
  );
});

test('a client that does not trust the stub certificate cannot talk to it', async (t) => {
  const stub = await createReleaseStub({
    panel: GOOD_HTML,
    repository: 'seakee/CPA-Manager-Plus',
    certificate: generateStubCertificate(),
    host: '127.0.0.1',
  });
  t.after(() => stub.close());
  const other = generateStubCertificate();
  await assert.rejects(throughStub({ ...stub, caPem: other.cert }, { requestPath: stub.latestPath }), /self.signed|unable to verify|certificate/i);
});

test('the stub traffic check wants the connect, releases/latest and the asset, nothing else', () => {
  const stub = { latestPath: '/repos/o/r/releases/latest', assetPath: '/cpampd-lite/assets/management.html' };
  const connect = { kind: 'connect', target: 'api.github.com:443' };
  const get = (p) => ({ kind: 'request', method: 'GET', path: p });
  const good = [connect, get(stub.latestPath), get(stub.assetPath)];
  assert.equal(checkStubTraffic({ events: good, stub }).assetDownloaded, true);
  assert.throws(() => checkStubTraffic({ events: good.slice(1), stub }), /never connected/);
  assert.throws(() => checkStubTraffic({ events: [connect, get(stub.assetPath)], stub }), /never asked for/);
  assert.throws(() => checkStubTraffic({ events: [connect, get(stub.latestPath)], stub }), /never downloaded/);
  assert.throws(
    () => checkStubTraffic({ events: [...good, get('/repos/o/r/releases')], stub }),
    /something other than releases\/latest/
  );
});

// ---------------------------------------------------------------- Docker namespace

test('only cpampd-lite-<run>-* names are accepted; cpamp-* is never touchable', () => {
  assert.equal(assertOwnedName(`cpampd-lite-${RUN}-legacy-enabled`), `cpampd-lite-${RUN}-legacy-enabled`);
  assert.equal(containerName(RUN, 'v8', 'disabled'), `cpampd-lite-${RUN}-v8-disabled`);
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

test('the CPA container is loopback-published, unprivileged, local-image-only and off port 18317', () => {
  const args = dockerRunArgs({
    name: `cpampd-lite-${RUN}-legacy-enabled`,
    runId: RUN,
    image: 'cpampd-lite-cpa-v8:8.0.13-b682e9e42586',
    configDir: '/tmp/cpampd-lite-x/legacy',
    stubDir: '/tmp/cpampd-lite-x/stub',
    uid: 501,
    gid: 20,
  });
  const at = (flag) => args.flatMap((arg, i) => (arg === flag ? [args[i + 1]] : []));
  assert.deepEqual(at('--name'), [`cpampd-lite-${RUN}-legacy-enabled`]);
  assert.deepEqual(at('-p'), ['127.0.0.1:0:8317']);
  assert.deepEqual(at('--pull'), ['never']);
  assert.deepEqual(at('--platform'), ['linux/amd64']);
  assert.deepEqual(at('--cap-drop'), ['ALL']);
  assert.deepEqual(at('--user'), ['501:20']);
  assert.deepEqual(at('-v'), ['/tmp/cpampd-lite-x/legacy:/cpa', '/tmp/cpampd-lite-x/stub:/cpampd-lite-stub:ro']);
  assert.deepEqual(at('--entrypoint'), ['/usr/local/bin/cli-proxy-api']);
  assert.deepEqual(args.slice(-3), ['-config', '/cpa/config.yaml', '-local-model']);
  for (const forbidden of ['--privileged', '--network', '--net', '--pid', '--volumes-from', '--device', '-P']) {
    assert(!args.includes(forbidden), forbidden);
  }
  assert(!args.join(' ').includes('18317'));
  assert(!args.join(' ').includes('cpamp-'));
  assert(!args.some((arg) => arg.startsWith('--env') || arg.startsWith('--env-file')));
  assert.deepEqual(at('-e'), ['SSL_CERT_FILE=/cpampd-lite-stub/ca.pem']);
});

test('without a stub the container gets no extra host, certificate or mount', () => {
  const args = dockerRunArgs({
    name: `cpampd-lite-${RUN}-v8-enabled`,
    runId: RUN,
    image: 'img',
    configDir: '/tmp/cpampd-lite-x/v8',
  });
  assert(!args.includes('--add-host') && !args.includes('-e') && !args.includes('--user'));
  assert.deepEqual(args.flatMap((a, i) => (a === '-v' ? [args[i + 1]] : [])), ['/tmp/cpampd-lite-x/v8:/cpa']);
});

test('docker run arguments refuse foreign names and unsafe mounts', () => {
  const base = { runId: RUN, image: 'img', configDir: '/tmp/x' };
  assert.throws(() => dockerRunArgs({ ...base, name: 'cpamp-runtime' }), /refusing container name/);
  assert.throws(() => dockerRunArgs({ ...base, name: `cpampd-lite-${RUN}-a`, configDir: 'relative' }), /unsafe bind-mount/);
  assert.throws(() => dockerRunArgs({ ...base, name: `cpampd-lite-${RUN}-a`, configDir: '/tmp/x:/etc' }), /unsafe bind-mount/);
  assert.throws(() => dockerRunArgs({ ...base, name: `cpampd-lite-${RUN}-a`, stubDir: '/tmp/a,b' }), /unsafe bind-mount/);
  assert.throws(() => dockerRunArgs({ ...base, name: `cpampd-lite-${RUN}-a`, runId: 'xyz' }), /bad run id/);
});

test('the identity probe is a throw-away container in the harness namespace', () => {
  const args = identityProbeArgs({ name: `${CONTAINER_PREFIX}${RUN}-image-probe`, runId: RUN, image: 'img' });
  assert(args.includes('--rm') && args.includes('--pull'));
  assert.deepEqual(args.slice(args.indexOf('--entrypoint'), args.indexOf('--entrypoint') + 3), ['--entrypoint', 'sh', 'img']);
  assert.match(args.at(-1), /sha256sum \/usr\/local\/bin\/cli-proxy-api && cat \/usr\/local\/share\/cpamp\/runtime\/cpa-artifact\.json/);
  assert.throws(() => identityProbeArgs({ name: 'cpamp-probe', runId: RUN, image: 'img' }), /refusing container name/);
});

test('the Docker wrapper registers a name before the call and releases only its own', () => {
  const calls = [];
  let failRun = true;
  const exec = (args) => {
    calls.push(args);
    if (args[0] === 'run' && failRun) return { status: 125, stdout: '', stderr: 'daemon refused' };
    if (args[0] === 'rm' && args[2].endsWith('-gone')) return { status: 1, stdout: '', stderr: 'Error: No such container: x' };
    if (args[0] === 'rm' && args[2].endsWith('-stuck')) return { status: 1, stdout: '', stderr: 'device busy' };
    return { status: 0, stdout: '', stderr: '' };
  };
  const docker = createDocker({ exec });
  const spec = (role) => ({ name: `cpampd-lite-${RUN}-${role}`, runId: RUN, image: 'img', configDir: '/tmp/x' });

  assert.throws(() => docker.start(spec('first')), /docker run failed \(125\): daemon refused/);
  assert(docker.owned.has(`cpampd-lite-${RUN}-first`), 'a container Docker may have created is still released');
  failRun = false;
  docker.start(spec('gone'));
  docker.start(spec('stuck'));
  assert.throws(() => docker.start({ ...spec('x'), name: 'cpamp-runtime' }), /refusing container name/);
  assert(!docker.owned.has('cpamp-runtime'));

  calls.length = 0;
  const failures = docker.releaseAll();
  assert.deepEqual(
    calls,
    [
      ['rm', '-f', `cpampd-lite-${RUN}-first`],
      ['rm', '-f', `cpampd-lite-${RUN}-gone`],
      ['rm', '-f', `cpampd-lite-${RUN}-stuck`],
    ],
    'rm -f of exactly the registered names'
  );
  assert.deepEqual(failures, [`cpampd-lite-${RUN}-stuck: device busy`], 'an already-gone container is success; others are reported');
  assert.equal(docker.owned.size, 0);
});

test('the Docker wrapper resolves the published port and the image platform', () => {
  const docker = createDocker({
    exec: (args) => {
      if (args[0] === 'port') return { status: 0, stdout: '127.0.0.1:55012\n', stderr: '' };
      if (args[0] === 'image') {
        return { status: 0, stdout: JSON.stringify([{ Id: 'sha256:abc', Os: 'linux', Architecture: 'amd64' }]), stderr: '' };
      }
      return { status: 1, stdout: '', stderr: '' };
    },
  });
  assert.equal(docker.port(`cpampd-lite-${RUN}-a`), 55012);
  assert.deepEqual(docker.imageInfo('img'), { id: 'sha256:abc', os: 'linux', architecture: 'amd64' });
  assert.equal(createDocker({ exec: () => ({ status: 1, stdout: '', stderr: '' }) }).imageInfo('missing'), undefined);
  assert.throws(() => docker.build({ image: 'cpamp-runtime:latest' }), /outside the harness namespace/);
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
  assert.equal(defaults.panelSource, 'stub');
  assert.equal(defaults.panel, DEFAULT_PANEL);
  assert.equal(defaults.repository, 'seakee/CPA-Manager-Plus');
  assert(!defaults.bundleOnly && !defaults.skipBuild && !defaults.buildCpaImage);

  const parsed = parseArgs([
    '--bundle-only', '--skip-build', '--build-cpa-image', '--cpa-image', 'img:1', '--panel', 'x/management.html',
    '--panel-source', 'mounted', '--layouts', 'v8', '--repository', 'o/r', '--report', 'out.json',
  ]);
  assert.equal(parsed.cpaImage, 'img:1');
  assert.deepEqual(parsed.layouts, ['v8']);
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
  const help = capture();
  assert.equal(await runAcceptance(['--help'], help), 0);
  assert.match(help.streams.out, /--bundle-only/);
  const bad = capture();
  assert.equal(await runAcceptance(['--bogus'], bad), 2);
  assert.match(bad.streams.err, /unknown argument: --bogus/);
  assert.equal(bad.streams.out, '');
});

test('--bundle-only verifies a panel directory without Docker and reports its digest', async (t) => {
  const dir = tempDir(t);
  const panel = path.join(dir, 'management.html');
  fs.writeFileSync(panel, GOOD_HTML);
  const ok = capture();
  assert.equal(await runAcceptance(['--bundle-only', '--skip-build', '--panel', panel], ok), 0);
  const report = JSON.parse(ok.streams.out);
  assert.equal(report.contract, 'cpamp.panel-lite-cpa-v8.v1');
  assert.equal(report.verdict, 'pass');
  assert.deepEqual(
    { bytes: report.panel.bytes, sha256: report.panel.sha256, singleFile: report.panel.singleFile },
    { bytes: GOOD_HTML.length, sha256: sha256(GOOD_HTML), singleFile: true }
  );
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
    [{ name: `${CONTAINER_PREFIX}${RUN}-image-probe`, runId: RUN, image: 'sha256:good' }],
    'the probe runs the verified image id, never the mutable tag'
  );
});

test('an image that differs from the pin in any probed field is refused', () => {
  const verify = (docker) => () =>
    verifyImageIdentity({ docker, image: 'cpampd-lite-cpa-v8:tag', pin: PIN, runId: RUN });
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
    [
      'wrong Version: banner',
      identityDocker({ output: identityProbeOutput({ banner: 'Version: 8.0.12' }) }),
      /want Version: /,
    ],
  ];
  for (const [label, docker, expected] of cases) {
    assert.throws(verify(docker), expected, label);
  }
  assert.throws(verify(identityDocker({ available: false })), UsageError);
});

// ---------------------------------------------------------------- manual install (mounted)

/** A stand-in CPA for one layout container: serves the panel unless disabled. */
async function standInLayoutCpa(t, { configPath, panel, disabled = false }) {
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
        return send(200, panel, 'text/html; charset=utf-8');
      }
      const text = fs.existsSync(configPath()) ? fs.readFileSync(configPath(), 'utf8') : '';
      const key = /secret-key: "([^"]+)"/.exec(text)?.[1];
      if (!key || req.headers.authorization !== `Bearer ${key}`) {
        return send(401, '{"error":"invalid management key"}');
      }
      if (req.url === '/v0/management/config') return send(200, '{}');
      if (req.url === '/v0/management/request-retry') {
        if (req.method === 'PUT') {
          const value = JSON.parse(Buffer.concat(chunks).toString('utf8')).value;
          fs.writeFileSync(configPath(), text.replace(/^([ \t]*request-retry:)[ \t]*-?\d+/m, `$1 ${value}`));
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

/** A Docker double that records the argv a real container would get. */
function fakeAcceptanceDocker({ portFor = () => 1, onStart = () => {}, releaseFailures = [] } = {}) {
  const starts = [];
  const probeCalls = [];
  const rm = [];
  const owned = [];
  return {
    starts,
    probeCalls,
    rm,
    available: () => 'linux/amd64',
    imageInfo: () => ({ id: 'sha256:good', os: 'linux', architecture: 'amd64' }),
    probeImage: (spec) => {
      probeCalls.push(spec);
      return identityProbeOutput();
    },
    start(spec) {
      starts.push(spec);
      owned.push(spec.name);
      onStart(spec);
    },
    port(name) {
      return portFor(name);
    },
    restart() {},
    logs() {
      return '';
    },
    releaseAll() {
      for (const name of owned.splice(0)) rm.push(['rm', '-f', name]);
      return releaseFailures;
    },
  };
}

test('mounted mode keeps the placed panel across a restart and never contacts the stub', async (t) => {
  const certificate = generateStubCertificate();
  const panelPath = path.join(tempDir(t), 'management.html');
  fs.writeFileSync(panelPath, GOOD_HTML);

  const configPaths = {};
  const configs = {};
  const statics = {};
  const enabled = await standInLayoutCpa(t, { configPath: () => configPaths.enabled, panel: GOOD_HTML });
  const disabled = await standInLayoutCpa(t, {
    configPath: () => configPaths.disabled,
    panel: GOOD_HTML,
    disabled: true,
  });
  const docker = fakeAcceptanceDocker({
    portFor: (name) => (name.endsWith('-enabled') ? enabled.port : disabled.port),
    onStart: (spec) => {
      const role = spec.name.split('-').at(-1);
      const configPath = path.join(spec.configDir, 'config.yaml');
      configPaths[role] = configPath;
      configs[role] = fs.readFileSync(configPath, 'utf8');
      statics[role] = fs.readFileSync(path.join(spec.configDir, 'static', 'management.html'));
    },
  });

  const stubArgs = [];
  let stub;
  let prefetch;
  const stubFactory = async (args) => {
    stubArgs.push(args);
    stub = await createReleaseStub(args);
    const served = await throughStub(stub, { requestPath: stub.assetPath });
    assert(served.body.equals(DECOY_1X_PANEL), 'the stub serves the 1.x decoy panel');
    prefetch = [...stub.events];
    return stub;
  };
  const tmpBase = fs.mkdtempSync(path.join(os.tmpdir(), 'cpampd-lite-test-base-'));
  t.after(() => fs.rmSync(tmpBase, { recursive: true, force: true }));

  const run = capture();
  const code = await runAcceptance(
    ['--skip-build', '--panel', panelPath, '--cpa-image', 'img:tag', '--panel-source', 'mounted', '--layouts', 'legacy'],
    {
      docker,
      stubFactory,
      certificateFactory: () => certificate,
      stdout: run.stdout,
      stderr: run.stderr,
      tmpBase,
    }
  );

  assert.equal(code, 0, run.streams.err);
  const report = JSON.parse(run.streams.out);
  const [layoutReport] = report.layouts;
  assert.equal(report.verdict, 'pass');
  assert.equal(layoutReport.enabled.panelSource, 'mounted');
  assert.equal(layoutReport.enabled.static.sha256, sha256(GOOD_HTML));
  assert.equal(layoutReport.enabled.served.sha256, sha256(GOOD_HTML));
  assert.equal(layoutReport.enabled.servedAfterRestart.sha256, sha256(GOOD_HTML));
  assert.deepEqual(layoutReport.enabled.placedPanelKept, {
    stubContacted: false,
    bytes: GOOD_HTML.length,
    sha256: sha256(GOOD_HTML),
  });
  assert.deepEqual(
    layoutReport.enabled.placedPanelKeptAfterRestart,
    layoutReport.enabled.placedPanelKept,
    'a restart with disable-auto-update-panel must still not touch the placed file or the stub'
  );
  assert.equal(layoutReport.enabled.management.persisted, true);
  assert.equal(layoutReport.enabled.afterRestart.value, 11);
  assert.equal(layoutReport.disabled.status, 404);
  assert.equal(layoutReport.disabled.releaseStubContacted, false);
  assert.equal(report.cleanup.ok, true);
  assert.deepEqual(stub.events, prefetch, 'mounted mode adds no CONNECT or request to the stub');

  // The image is probed and every container is started from the verified id.
  assert.equal(docker.probeCalls[0].image, 'sha256:good');
  assert.equal(docker.starts.length, 2);
  for (const spec of docker.starts) {
    assert.equal(spec.image, 'sha256:good');
    assert(spec.stubDir, 'mounted mode still mounts the stub directory and certificate');
  }

  // The stub serves a different "1.x" panel, so an accidental update is visible.
  assert.equal(stubArgs.length, 1);
  assert(stubArgs[0].panel.equals(DECOY_1X_PANEL), 'mounted mode wires a decoy 1.x panel');
  assert(!stubArgs[0].panel.equals(GOOD_HTML));

  const enabledConfig = configs.enabled;
  assert.match(enabledConfig, /disable-auto-update-panel: true/);
  assert.match(enabledConfig, /^proxy-url: "http:\/\/host\.docker\.internal:\d+"$/m);
  assert(statics.enabled.equals(GOOD_HTML), 'the hand-placed management.html is byte-unchanged');
  assert(statics.disabled.equals(GOOD_HTML), 'the disabled layout keeps its stored bundle untouched');
});

// ---------------------------------------------------------------- failure and signal cleanup

const failingLayoutArgs = (panelPath) => [
  '--skip-build',
  '--panel',
  panelPath,
  '--cpa-image',
  'img:tag',
  '--panel-source',
  'mounted',
  '--layouts',
  'legacy',
];

test('a failed layout check releases every container, the stub and the tmpdir, and exits 1', async (t) => {
  const panelPath = path.join(tempDir(t), 'management.html');
  fs.writeFileSync(panelPath, GOOD_HTML);
  const tmpBase = fs.mkdtempSync(path.join(os.tmpdir(), 'cpampd-lite-test-base-'));
  t.after(() => fs.rmSync(tmpBase, { recursive: true, force: true }));
  const closes = [];
  const stubFactory = async () => ({
    events: [],
    latestPath: '/x',
    assetPath: '/y',
    close: async () => {
      closes.push('stub');
    },
  });
  const docker = fakeAcceptanceDocker();
  const run = capture();

  const code = await runAcceptance(failingLayoutArgs(panelPath), {
    docker,
    stubFactory,
    certificateFactory: () => ({ key: 'k', cert: 'c' }),
    stdout: run.stdout,
    stderr: run.stderr,
    tmpBase,
    layoutRunner: async ({ layout, runId, docker: d }) => {
      d.start({ name: containerName(runId, layout, 'enabled'), runId, image: 'sha256:good', configDir: path.join(tmpBase, 'enabled') });
      d.start({ name: containerName(runId, layout, 'disabled'), runId, image: 'sha256:good', configDir: path.join(tmpBase, 'disabled') });
      throw new Error('static/management.html is not byte-identical to the build');
    },
  });

  assert.equal(code, 1);
  const report = JSON.parse(run.streams.out);
  assert.equal(report.verdict, 'fail');
  assert.match(report.error, /not byte-identical/);
  assert.equal(report.cleanup.ok, true);
  assert.deepEqual(report.cleanup.failures, []);
  assert.equal(report.cleanup.runId.length, 8);
  assert.deepEqual(
    docker.rm,
    [['rm', '-f', docker.starts[0].name], ['rm', '-f', docker.starts[1].name]],
    'every registered container is removed with rm -f'
  );
  assert.deepEqual(closes, ['stub'], 'the stub is closed exactly once even when the run failed');
  assert.deepEqual(fs.readdirSync(tmpBase), [], 'the run tmpdir is removed');
});

test('a cleanup failure is reported and forces exit 1 even when every check passed', async (t) => {
  const panelPath = path.join(tempDir(t), 'management.html');
  fs.writeFileSync(panelPath, GOOD_HTML);
  const tmpBase = fs.mkdtempSync(path.join(os.tmpdir(), 'cpampd-lite-test-base-'));
  t.after(() => fs.rmSync(tmpBase, { recursive: true, force: true }));
  const docker = fakeAcceptanceDocker({ releaseFailures: ['cpampd-lite-stuck: device busy'] });
  const run = capture();

  const code = await runAcceptance(failingLayoutArgs(panelPath), {
    docker,
    stubFactory: async () => ({ events: [], latestPath: '/x', assetPath: '/y', close: async () => {} }),
    certificateFactory: () => ({ key: 'k', cert: 'c' }),
    stdout: run.stdout,
    stderr: run.stderr,
    tmpBase,
    layoutRunner: async ({ layout }) => ({ layout }),
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

for (const [signal, expectedExit] of [
  ['SIGINT', 130],
  ['SIGTERM', 143],
]) {
  test(`${signal} releases the run's resources and exits ${expectedExit}`, async (t) => {
    const panelPath = path.join(tempDir(t), 'management.html');
    fs.writeFileSync(panelPath, GOOD_HTML);
    const tmpBase = fs.mkdtempSync(path.join(os.tmpdir(), 'cpampd-lite-test-base-'));
    t.after(() => fs.rmSync(tmpBase, { recursive: true, force: true }));
    const closes = [];
    const docker = fakeAcceptanceDocker();
    const signals = new EventEmitter();
    const exits = [];
    let resolveExit;
    const exited = new Promise((resolve) => {
      resolveExit = resolve;
    });
    const run = capture();
    let container;

    const code = await runAcceptance(failingLayoutArgs(panelPath), {
      docker,
      stubFactory: async () => ({
        events: [],
        latestPath: '/x',
        assetPath: '/y',
        close: async () => {
          closes.push('stub');
        },
      }),
      certificateFactory: () => ({ key: 'k', cert: 'c' }),
      stdout: run.stdout,
      stderr: run.stderr,
      tmpBase,
      signals,
      exit: (value) => {
        exits.push(value);
        resolveExit();
      },
      layoutRunner: async ({ layout, runId, docker: d }) => {
        container = containerName(runId, layout, 'enabled');
        d.start({ name: container, runId, image: 'sha256:good', configDir: path.join(tmpBase, 'enabled') });
        signals.emit(signal);
        await exited;
        throw new Error(`${signal} interrupted the run`);
      },
    });

    assert.deepEqual(exits, [expectedExit]);
    assert.match(run.streams.err, new RegExp(`${signal}: releasing ${CONTAINER_PREFIX}`));
    assert.deepEqual(docker.rm, [['rm', '-f', container]]);
    assert.deepEqual(closes, ['stub']);
    assert.deepEqual(fs.readdirSync(tmpBase), []);
    assert.equal(code, 1);
  });
}
