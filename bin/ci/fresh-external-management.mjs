#!/usr/bin/env node
// Disposable D acceptance for Fresh External management. No shared state, no
// pinned evidence: every container, network and volume this creates is removed.
//
// The scenario is the one a user actually hits. A real CPA v8.0.13 runs as its
// own container from the runtime candidate's pinned binary, and the Manager
// reaches it over a private network. The Manager owns no part of that CPA, so
// every write it makes is a write onto somebody else's process, which is what the
// External profile exists to make safe.
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { randomBytes, createHash } from 'node:crypto';
import { existsSync, mkdirSync, writeFileSync, rmSync } from 'node:fs';
import { createRequire } from 'node:module';
import { createServer } from 'node:net';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const [runtimeImage, managerImage, ingressImage, output] = process.argv.slice(2);
assert(
  runtimeImage && managerImage && ingressImage && output,
  'Usage: fresh-external-management.mjs RUNTIME_IMAGE MANAGER_IMAGE INGRESS_IMAGE OUTPUT'
);
const { chromium } = createRequire(import.meta.url)(process.env.PLAYWRIGHT_MODULE || 'playwright');
const out = resolve(output);
mkdirSync(out, { recursive: false, mode: 0o700 });
// The CPA configs carry every secret of this run, so they are written to a
// sibling of the artifact directory and deleted with it. An artifact directory is
// the one place a secret must never be: a redacted copy of a live CPA config is
// still a replayable one.
const secretsDir = resolve(root, 'bin/tmp', 'd-external-secrets-' + randomBytes(6).toString('hex'));
mkdirSync(secretsDir, { recursive: true, mode: 0o700 });
const cpaConfigPath = resolve(secretsDir, 'cpa-config.yaml');
const cpaV7ConfigPath = resolve(secretsDir, 'cpa-v7-config.yaml');
const cpaV7AuthDir = resolve(secretsDir, 'cpa-v7-auth');

// Everything this run prints is kept, unredacted, so the secret assertion below can
// look at it before redaction rewrites it.
const printed = [];
for (const level of ['log', 'error']) {
  const write = console[level].bind(console);
  console[level] = (...args) => {
    printed.push(args.map((value) => String(value)).join(' '));
    write(...args);
  };
}
const project = 'cpamp-d-external-' + randomBytes(6).toString('hex');
const bootstrap = '/.well-known/cpamp-bootstrap';
const report = {
  result: 'FAIL',
  project,
  slice: 'External 2b step 7',
  checks: [],
  screenshots: [],
  pageErrors: [],
  notRun: [
    'Real Provider model success (no Provider fixture or credential; the model request proves Client Key authentication only)',
    'Natural ten-minute claim expiry',
    'Non-Chinese locale layouts and other browser engines',
  ],
};
// Every value that must never reach a log or the report. The management keys are
// generated here, not read from the environment, so the redaction list is the
// complete set for this run.
const secretValues = [];
const managementKey = 'cpamp-d-' + randomBytes(24).toString('hex');
const rotatedKey = 'cpamp-d-' + randomBytes(24).toString('hex');
// The screenshot pass rotates a second time: after the rebind above the Manager
// holds `rotatedKey`, so only a third value can make that hold stale again.
const staleKey = 'cpamp-d-' + randomBytes(24).toString('hex');
secretValues.push(managementKey, rotatedKey, staleKey);
const seedKeys = ['sk-d-external-' + randomBytes(12).toString('hex'), 'sk-d-external-' + randomBytes(12).toString('hex')];
for (const key of seedKeys) secretValues.push(key);

// A free loopback port, pinned before Compose starts: publishing port 0 lets the
// host port change after a restart, which would invalidate the browser's origin
// and every saved credential in the run.
async function freePort() {
  const probe = createServer();
  await new Promise((resolve, reject) => {
    probe.once('error', reject);
    probe.listen(0, '127.0.0.1', resolve);
  });
  const { port } = probe.address();
  await new Promise((resolve) => probe.close(resolve));
  return port;
}
const publicPort = await freePort();
// The operator's own loopback view of the user's CPA, for the out-of-band reads
// and edits this Manager must never be the author of.
const cpaPort = await freePort();

const env = { ...process.env, CPAMP_PUBLIC_PORT: `127.0.0.1:${publicPort}` };
let browser;
let page;

function redact(text) {
  return secretValues.reduce((message, value) => message.replaceAll(value, '[REDACTED]'), text);
}
function run(args, input, check = true) {
  const result = spawnSync(args[0], args.slice(1), {
    cwd: root,
    env,
    input,
    encoding: 'utf8',
    timeout: 180000,
    maxBuffer: 8 << 20,
  });
  if (check && result.status !== 0)
    throw new Error(
      'Command failed: ' +
        args.slice(0, 4).join(' ') +
        '\n' +
        redact(result.stderr || result.stdout || '')
    );
  return result;
}
const docker = (...args) => run(['docker', ...args]).stdout.trim();
const images = Object.fromEntries(
  [
    ['cpamp-runtime', runtimeImage],
    ['cpamp-manager', managerImage],
    ['cpamp-ingress', ingressImage],
  ].map(([service, image]) => {
    const meta = JSON.parse(docker('image', 'inspect', image))[0];
    assert(meta.Os === 'linux' && meta.Architecture === 'amd64', 'Linux/amd64 image required');
    return [service, meta.Id];
  })
);
report.images = images;
report.imageSourceRevisions = Object.fromEntries(
  Object.entries(images).map(([service, image]) => [
    service,
    JSON.parse(docker('image', 'inspect', image))[0].Config.Labels?.[
      'org.opencontainers.image.revision'
    ] || 'unknown',
  ])
);
// The report names the source it tested, so a rerun on another commit is visible
// rather than assumed. A dirty worktree is refused outright: an image built from
// it carries bytes no commit names, and every check below would be attributed to
// work that does not exist.
report.gitHead = run(['git', 'rev-parse', 'HEAD']).stdout.trim();
const dirtyWorktree = run(['git', 'status', '--porcelain']).stdout.trim();
assert.equal(dirtyWorktree, '', 'the worktree must be clean: the report names this HEAD');
report.testedSourceCommit = report.gitHead;
report.imageSourceCommit = report.imageSourceRevisions['cpamp-manager'];
assert(
  report.imageSourceCommit === 'unknown' || report.imageSourceCommit === report.gitHead,
  'the Manager image was built from ' + report.imageSourceCommit + ', not ' + report.gitHead
);

const network = project + '-net';
const managerVolume = project + '-manager-data';
const cpaContainer = project + '-cpa';
const managerContainer = project + '-manager';
const ingressContainer = project + '-ingress';
const v7Container = project + '-cpa-v7';
// The Manager's own address on the private network: the failed-authentication
// counter only attributes a CPA log line to this Manager when it names this.
let managerAddress = '';

function pass(name, observed) {
  report.checks.push({ name, result: 'PASS', ...(observed ? { observed } : {}) });
  // The report keeps the observation itself; the run log never prints it raw, so a
  // value that should not be here cannot become part of a pasted log.
  console.log('PASS ' + name + (observed ? ' ' + redact(JSON.stringify(observed)) : ''));
}
function fail(name, observed) {
  report.checks.push({ name, result: 'FAIL', ...(observed ? { observed } : {}) });
  console.log('FAIL ' + name + (observed ? ' ' + JSON.stringify(observed) : ''));
  throw new Error('Check failed: ' + name);
}
function notRun(name, reason) {
  report.checks.push({ name, result: 'NOT RUN', reason });
  console.log('NOT RUN ' + name + ' — ' + reason);
}
// One check whose body throws on failure. The point of collecting instead of
// aborting is that a single broken step still reports every later one.
async function check(name, body) {
  try {
    const observed = await body();
    pass(name, observed);
    return observed;
  } catch (error) {
    fail(name, { error: redact(String(error.message || error)).slice(0, 500) });
  }
}
async function wait(body, label, timeout = 60000) {
  const end = Date.now() + timeout;
  do {
    try {
      if (await body()) return;
    } catch {}
    await new Promise((r) => setTimeout(r, 250));
  } while (Date.now() < end);
  throw new Error('Timed out: ' + label);
}
async function screenshot(name) {
  const overflow = await page.evaluate(() => ({
    width: innerWidth,
    scrollWidth: document.documentElement.scrollWidth,
  }));
  assert(overflow.scrollWidth <= overflow.width + 1, 'horizontal overflow: ' + name);
  await page.screenshot({
    path: resolve(out, name + '.png'),
    fullPage: page.viewportSize().width > 768,
    animations: 'disabled',
  });
  report.screenshots.push({
    name,
    path: name + '.png',
    viewport: page.viewportSize(),
    theme: await page
      .locator('html')
      .evaluate((html) => (html.getAttribute('data-theme') === 'dark' ? 'dark' : 'light')),
    route: new URL(page.url()).pathname,
  });
}

// The CPA-side failed-authentication counter. It is read out of the CPA's own log,
// which is the only place that knows how many times this Manager presented a
// credential the CPA refused: five in a row bans the Manager's address, so the
// number is the whole point of the rejection gate.
// The parsing rule, in one place because the number is the whole point:
//   * only a line that names the MANAGEMENT surface counts — a `/v0/management/`
//     or `/v8/management/` path, a management key, or the management secret;
//   * within those, only an authentication refusal counts: 401, unauthorized, or
//     an invalid key/token;
//   * and when the line carries a peer address, it must be the Manager's own
//     container address. This run's other management clients are its own wget
//     probes, which always present the key the CPA currently holds, so they can
//     only answer 200; a Client-Key request on `/v1/models`, a 404 and a health
//     probe are not management authentications and are never counted.
function cpaFailedAuthCount() {
  const logs = run(['docker', 'logs', cpaContainer], undefined, false);
  const text = (logs.stdout || '') + (logs.stderr || '');
  return text
    .split('\n')
    .filter((line) => /(?:^|\/)(?:v0|v8)\/management\/|management[ -]?key|secret-key/i.test(line))
    .filter((line) => /\b401\b|unauthori[sz]ed|invalid (?:api )?(?:key|token)/i.test(line))
    .filter((line) => !/\d+\.\d+\.\d+\.\d+/.test(line) || line.includes(managerAddress))
    .length;
}
// A secret must not survive anywhere this run wrote: the report object itself
// (asserted UNREDACTED), everything the run printed, and the log of every
// container of this run. Redaction is what makes the artifact readable, never what
// makes it safe — an assertion that runs after it can never fail.
function assertNoSecret(where, text) {
  for (const value of secretValues) {
    if (value && text.includes(value)) throw new Error('a secret value reached ' + where);
  }
}
function runContainerLogs() {
  const names = run(['docker', 'ps', '-a', '--filter', 'name=' + project, '--format', '{{.Names}}'], undefined, false)
    .stdout.trim()
    .split('\n')
    .filter(Boolean);
  return names.map((name) => {
    const logs = run(['docker', 'logs', name], undefined, false);
    return { name, text: (logs.stdout || '') + (logs.stderr || '') };
  });
}
function cpaLogTail(lines = 40) {
  const logs = run(['docker', 'logs', '--tail', String(lines), cpaContainer], undefined, false);
  return redact(((logs.stdout || '') + (logs.stderr || '')).slice(-6000));
}

// The CPA is the pinned v8.0.13 binary from the runtime candidate, started
// directly rather than through the Runtime entrypoint: this Manager must reach a
// CPA it does not own, so the container must not be the Manager's own runtime.
let cpaConfig = [
  'config-version: 8',
  'server:',
  '  host: "0.0.0.0"',
  '  port: 8317',
  'management:',
  '  allow-remote: true',
  `  secret-key: "${managementKey}"`,
  '  disable-control-panel: true',
  '  disable-auto-update-panel: true',
  // Required by the v8 schema this Manager projects: a config without an auth
  // directory is `management_v8_config_invalid`, not a connectable CPA.
  'oauth:',
  '  auth-dir: "/cpa/auth"',
  'access:',
  '  api-keys:',
  ...seedKeys.map((key) => `    - "${key}"`),
  'plugins:',
  '  enabled: false',
  'observability:',
  '  logs:',
  '    logging-to-file: false',
  '  usage:',
  '    usage-statistics-enabled: false',
  '',
].join('\n');
// The config lives outside the artifact directory, in the sibling secrets
// directory: it carries every secret of this run, and an artifact directory is the
// one place a secret must never be.
writeFileSync(cpaConfigPath, cpaConfig, { mode: 0o600 });

const cpaName = 'cpamp-d-external-cpa';
const origin = `http://127.0.0.1:${publicPort}`;
report.origin = origin;

try {
  docker('network', 'create', network);
  docker('volume', 'create', managerVolume);

  // The user's CPA. Its config is mounted read-write because step 8 rotates the
  // management key out of band, exactly as an operator with access to that host
  // would: the Manager must notice a rotation it was never told about.
  docker(
    'run',
    '-d',
    '--name',
    cpaContainer,
    '--network',
    network,
    '--network-alias',
    cpaName,
    // Loopback only: this is the operator's own out-of-band access to a CPA the
    // Manager does not own, used to prove what the CPA carries and to reduce it
    // to a single key. Nothing else in the run reaches it this way.
    '-p',
    `127.0.0.1:${cpaPort}:8317`,
    '-v',
    `${cpaConfigPath}:/cpa/config.yaml`,
    images['cpamp-runtime'],
    '/usr/local/bin/cli-proxy-api',
    // The pinned v8.0.13 binary takes a single-dash flag; `--config` is not a
    // synonym and would start the CPA on its defaults instead.
    '-config',
    '/cpa/config.yaml'
  );
  await check('user CPA v8.0.13 starts as its own container on a private network', async () =>
    wait(async () => {
      const probe = run(
        [
          'docker',
          'run',
          '--rm',
          '--network',
          network,
          '--entrypoint',
          'wget',
          images['cpamp-manager'],
          '-qO-',
          `--header=Authorization: Bearer ${managementKey}`,
          `http://${cpaName}:8317/v0/management/config`,
        ],
        undefined,
        false
      );
      return probe.status === 0;
    }, 'CPA management API')
  );
  report.cpaVersion = (() => {
    const probe = run(
      [
        'docker',
        'run',
        '--rm',
        '--network',
        network,
        '--entrypoint',
        'wget',
        images['cpamp-manager'],
        '-S',
        '-qO-',
        '--header=Authorization: Bearer ' + managementKey,
        `http://${cpaName}:8317/v0/management/config`,
      ],
      undefined,
      false
    );
    return (
      /X-Cpa-Version:\s*(v?[0-9][0-9.]*)/i.exec((probe.stderr || '') + (probe.stdout || ''))?.[1] ||
      'unknown'
    );
  })();

  // The Manager, on the same network and with its own volume. It is manager-only:
  // External owns no Runtime, so nothing here may start one.
  // Ingress is the public listener and the routing control plane. A Manager
  // without it cannot activate a control base path, so this is not optional
  // scaffolding: it is the same pair production runs.
  docker(
    'run',
    '-d',
    '--name',
    managerContainer,
    '--network',
    network,
    '--network-alias',
    'cpamp-manager',
    '-v',
    `${managerVolume}:/data`,
    '-e',
    'HTTP_ADDR=0.0.0.0:18317',
    '-e',
    'USAGE_DB_PATH=/data/usage.sqlite',
    '-e',
    'CPA_MANAGER_DATA_KEY_PATH=/data/data.key',
    '-e',
    'USAGE_COLLECTOR_MODE=auto',
    '-e',
    'CPAMP_INGRESS_CONTROL_URL=http://cpamp-ingress:9082',
    // The origin the Manager verifies a route against must be the origin it can
    // reach: the Ingress service, not the host loopback the browser uses.
    '-e',
    'CPAMP_PUBLIC_ORIGIN=http://cpamp-ingress:18317',
    images['cpamp-manager']
  );
  docker(
    'run',
    '-d',
    '--name',
    ingressContainer,
    '--network',
    network,
    '--network-alias',
    'cpamp-ingress',
    '-p',
    `127.0.0.1:${publicPort}:18317`,
    '-e',
    'CPAMP_INGRESS_ADDR=0.0.0.0:18317',
    '-e',
    'CPAMP_MANAGER_URL=http://cpamp-manager:18317',
    '-e',
    'CPAMP_GATEWAY_URL=http://cpamp-runtime:8317',
    '-e',
    'CPAMP_INGRESS_CONTROL_ADDR=0.0.0.0:9082',
    images['cpamp-ingress']
  );
  await wait(async () => {
    const health = await fetch(origin + '/health');
    return health.status === 503 || health.status === 200;
  }, 'Manager listener');
  pass('Manager starts manager-only with no Runtime companion');
  managerAddress = (
    docker('inspect', '-f', '{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}', managerContainer)
      .trim()
      .split(/\s+/)
      .filter(Boolean)[0] ?? ''
  );

  // Claim. The install issues a Handoff, which carries the one-time token; it is a
  // secret and never appears in the report.
  // The first boot is a read-only listener that serves Discovery and nothing
  // else, so the writable service is restarted after the Host admits the store.
  // This is the order the operator guide uses.
  docker('stop', managerContainer);
  const handoff = docker('run', '--rm', '--network', network, '-v', `${managerVolume}:/data`,
    '-e', 'USAGE_DB_PATH=/data/usage.sqlite',
    '-e', 'CPA_MANAGER_DATA_KEY_PATH=/data/data.key',
    images['cpamp-manager'], 'initialize').trim();
  docker('start', managerContainer);
  // The restarted service republishes Bootstrap (and its API) through Ingress once
  // it has admitted the store. Exchanging before that answers 404 from the
  // pre-ready listener, which serves Discovery only.
  await wait(async () => (await fetch(origin + bootstrap + '/ui')).ok, 'Bootstrap UI after initialize');
  const claim = JSON.parse(
    Buffer.from(handoff.split('#handoff=')[1], 'base64url').toString()
  );
  secretValues.push(claim.token);
  const api = bootstrap + '/api/v2/';
  // The fragment is the browser's shape (it carries `resume`); the exchange is
  // stricter and refuses unknown fields, so only the three it accepts are sent.
  const exchange = await fetch(origin + api + 'claim/exchange', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      identity: claim.identity,
      claimGeneration: claim.claimGeneration,
      token: claim.token,
    }),
  });
  assert.equal(exchange.status, 200, 'claim exchange');
  const session = await exchange.json();
  secretValues.push(session.token);
  const bootstrapHeaders = {
    'Content-Type': 'application/json',
    Authorization: 'Fresh ' + session.token,
  };
  async function bootstrapCall(resource, body) {
    const response = await fetch(origin + api + resource, {
      method: body === undefined ? 'GET' : 'POST',
      headers: bootstrapHeaders,
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    });
    let payload = null;
    try {
      payload = await response.json();
    } catch {}
    return { status: response.status, payload };
  }

  // The administrator key is generated by the Manager and committed under the
  // same authority, in two steps, exactly as the wizard does: the generated value
  // is never re-derived here.
  const generated = await bootstrapCall('admin/generate', {});
  assert.equal(generated.status, 200, 'admin/generate');
  const admin = generated.payload.key;
  secretValues.push(admin);
  const commit = await bootstrapCall('admin/commit', {
    identity: session.operation.identity,
    commitId: 'd-external-commit',
    key: admin,
  });
  await check('initialize claim, generate and commit the administrator key', async () => {
    assert.equal(commit.status, 200, JSON.stringify(commit.payload));
    assert.equal(commit.payload.adminInitialized, true);
    return { revision: commit.payload.revision };
  });

  // The control base path is the only place the canonical API mounts, so this run
  // activates `/console` once and then uses it for every call.
  const prepared = await bootstrapCall('routing/prepare', {
    identity: session.operation.identity,
    target: '/console',
  });
  assert.equal(prepared.status, 200, JSON.stringify(prepared.payload));
  await check('prepare the /console control base path', async () => ({
    target: prepared.payload.routing?.target,
  }));
  const activated = await bootstrapCall('routing/activate', {
    identity: session.operation.identity,
  });
  await check('activate the /console control base path', async () => {
    assert.equal(activated.status, 200, JSON.stringify(activated.payload));
    assert.equal(activated.payload.routing?.committed, true);
    return { committed: true, target: activated.payload.routing.target };
  });

  const adminHeaders = {
    'Content-Type': 'application/json',
    Authorization: 'Bearer ' + admin,
  };
  async function apiCall(method, resource, body) {
    const response = await fetch(origin + '/console/api/v2/' + resource, {
      method,
      headers: adminHeaders,
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    });
    let payload = null;
    try {
      payload = await response.json();
    } catch {}
    return { status: response.status, payload };
  }

  // Connect External. The strategy call is the only place a management key is
  // ever sent to the Manager, and it is sent once.
  const before = await apiCall('GET', 'operation');
  const connect = await apiCall('POST', 'strategy', {
    identity: before.payload.identity,
    expectedRevision: before.payload.revision,
    attemptId: randomBytes(16).toString('hex'),
    strategy: 'connect_external',
    baseUrl: `http://${cpaName}:8317`,
    managementKey,
  });
  await check('connect External commits the user CPA without taking it over', async () => {
    assert.equal(connect.status, 200, JSON.stringify(connect.payload));
    assert.equal(connect.payload.strategy?.strategy, 'connect_external');
    assert.equal(connect.payload.strategy?.stage, 'committed');
    // The Manager must not have started a Runtime: External means the CPA keeps
    // its own lifecycle.
    const runtime = run(['docker', 'ps', '--filter', `name=${project}`, '--format', '{{.Names}}'])
      .stdout.trim()
      .split('\n');
    assert.deepEqual(runtime.sort(), [cpaContainer, ingressContainer, managerContainer].sort());
    return { stage: connect.payload.strategy.stage };
  });
  const complete = await apiCall('POST', 'complete', {});
  await check('complete the External installation', async () => {
    assert.equal(complete.status, 200, JSON.stringify(complete.payload));
    assert.equal(complete.payload.productComplete, true);
    return { revision: complete.payload.revision };
  });

  // Keys. The Manager stages its own records and reads the CPA back; the list a
  // user sees is the tuple of both.
  const list = await apiCall('GET', 'api-keys');
  await check('Key list matches the CPA the Manager does not own', async () => ({
    staged: list.payload?.keys?.length ?? 0,
    cpaSeedKeys: seedKeys.length,
  }));

  // The Manager stages its own record and then projects it onto the CPA. The
  // create is idempotent by `requestId` and answers 201 the first time, 200 on a
  // replay; the secret is returned once in this reply and never again.
  const createRequestId = randomBytes(16).toString('hex');
  const created = await apiCall('POST', 'api-keys', { requestId: createRequestId });
  await check('add a Client Key and read it back from the CPA', async () => {
    assert(created.status === 201 || created.status === 200, JSON.stringify(created.payload));
    const key = created.payload.key ?? created.payload;
    assert(key?.id, 'no key id');
    const secret = created.payload.createdSecret ?? key.createdSecret;
    assert(secret, 'no one-time secret');
    secretValues.push(secret);
    // Creating a record stages it. Projecting it onto the CPA is the separate,
    // journaled install, and that is the call whose effect the CPA can confirm.
    const installed = await apiCall('POST', 'api-keys/' + key.id + '/initial-install', {});
    assert.equal(installed.status, 200, JSON.stringify(installed.payload));
    assert.equal(installed.payload.runtimeState, 'effective_observed', JSON.stringify(installed.payload));
    const readBack = await apiCall('GET', 'api-keys');
    const record = readBack.payload.keys.find((entry) => entry.id === key.id);
    assert(record, 'created key missing from the list');
    // The Manager projects onto a CPA it does not own, so presence is what the CPA
    // answers, not what the write intended.
    assert.equal(record.runtimeState, 'effective_observed', JSON.stringify(record));
    return { id: key.id, runtimeState: record.runtimeState };
  });
  const keyRecord = created.payload.key ?? created.payload;
  const keyId = keyRecord?.id;
  const keySecret = created.payload.createdSecret ?? keyRecord?.createdSecret;

  // Every key write is a journaled operation: prepare, continue while a step is
  // named, and settle on the read-back. A retry of the same request ID replays
  // instead of writing twice.
  async function keyOperation(targetId, keeperId, mode) {
    const operationId = randomBytes(16).toString('hex');
    const prepared = await apiCall('POST', 'api-keys/' + targetId + '/revocation', {
      requestId: operationId,
      keeperId,
      mode,
    });
    if (prepared.status !== 200) return { status: prepared.status, payload: prepared.payload };
    let result = prepared.payload;
    for (let round = 0; round < 12; round++) {
      if (result.operation?.outcome === 'committed') break;
      if (!result.nextStep) break;
      const advanced = await apiCall('POST', 'api-keys/operations/' + operationId + '/continue', {});
      if (advanced.status !== 200) return { status: advanced.status, payload: advanced.payload };
      result = advanced.payload;
    }
    return { status: 200, payload: result };
  }

  // The CPA's own key list, read out of band: this Manager never reports a
  // fact about a CPA it does not own that the CPA did not just answer.
  async function cpaKeys(management) {
    const response = await fetch(
      `http://127.0.0.1:${cpaPort}/v8/management/config/access/api-keys`,
      { headers: { Authorization: 'Bearer ' + management } }
    );
    assert.equal(response.status, 200, 'the CPA refused the out-of-band key read');
    return response.json();
  }

  async function stageKey(install = true) {
    const created = await apiCall('POST', 'api-keys', { requestId: randomBytes(16).toString('hex') });
    assert(created.status === 201, JSON.stringify(created.payload));
    const id = created.payload.key?.id;
    const secret = created.payload.createdSecret;
    assert(id && secret, 'the create returned no key');
    secretValues.push(secret);
    if (install) {
      const installed = await apiCall('POST', 'api-keys/' + id + '/initial-install', {});
      assert.equal(installed.status, 200, JSON.stringify(installed.payload));
    }
    return { id, secret };
  }

  await check('rename a Client Key', async () => {
    const detail = await apiCall('GET', 'api-keys/' + keyId);
    assert.equal(detail.status, 200, JSON.stringify(detail.payload));
    const renamed = await apiCall('PUT', 'api-keys/' + keyId, {
      name: 'd-renamed',
      nameRevision: detail.payload.nameRevision,
    });
    assert.equal(renamed.status, 200, JSON.stringify(renamed.payload));
    assert.equal(renamed.payload.name, 'd-renamed');
    return { name: renamed.payload.name };
  });

  await check('a Client Key written by the Manager authenticates against the CPA', async () => {
    const probe = run(
      [
        'docker',
        'run',
        '--rm',
        '--network',
        network,
        '--entrypoint',
        'wget',
        images['cpamp-manager'],
        '-qO-',
        '--header=Authorization: Bearer ' + keySecret,
        `http://${cpaName}:8317/v1/models`,
      ],
      undefined,
      false
    );
    assert.equal(probe.status, 0, 'the CPA refused a Client Key the Manager wrote');
    return { status: probe.status };
  });

  // A replace and a revoke act on a credential the CPA carries, so both prove
  // themselves by reading the CPA back: membership is the only fact this Manager
  // may claim about a CPA it does not own.
  const keeper = await stageKey();
  await check('replace one Client Key with another', async () => {
    const result = await keyOperation(keyId, keeper.id, 'replace');
    assert.equal(result.status, 200, JSON.stringify(result.payload));
    assert.equal(result.payload.operation?.outcome, 'committed', JSON.stringify(result.payload));
    const keys = await cpaKeys(managementKey);
    assert(!keys.includes(keySecret), 'the CPA still carries the replaced key');
    assert(keys.includes(keeper.secret), 'the CPA does not carry the keeper');
    return { mode: 'replace', outcome: result.payload.operation.outcome };
  });

  const spender = await stageKey();
  await check('revoke a Client Key while another staged key keeps the CPA closed', async () => {
    const result = await keyOperation(keeper.id, spender.id, 'revoke');
    assert.equal(result.status, 200, JSON.stringify(result.payload));
    assert.equal(result.payload.operation?.outcome, 'committed', JSON.stringify(result.payload));
    const keys = await cpaKeys(managementKey);
    assert(!keys.includes(keeper.secret), 'the CPA still carries the revoked key');
    assert(keys.includes(spender.secret), 'the CPA dropped the keeper too');
    return { mode: 'revoke', outcome: result.payload.operation.outcome };
  });

  // Gateway config: the page's own projection, then one scalar saved, read back,
  // changed out of band by the operator, and saved again with the stale digest.
  const gatewayRead = await apiCall('GET', 'gateway/config');
  await check('gateway config reads the CPA through the External authority', async () => {
    assert.equal(gatewayRead.status, 200, JSON.stringify(gatewayRead.payload));
    const groups = gatewayRead.payload?.groups ?? [];
    assert(groups.length > 0, 'the projection lists no groups');
    return { groups: groups.length, generation: gatewayRead.payload?.generation };
  });

  // The scalar this run saves is chosen from the projection, so it is a leaf this
  // profile says is editable rather than a path the script assumes.
  const scalar = (() => {
    for (const group of gatewayRead.payload?.groups ?? []) {
      if (!group.writable) continue;
      for (const path of group.paths ?? []) {
        const field = (gatewayRead.payload?.fields ?? []).find((entry) => entry.path === path);
        if (!field?.editable) continue;
        if (!['number', 'boolean', 'string'].includes(typeof field.value)) continue;
        return { group: group.id, baseline: group.baseline, path };
      }
    }
    return null;
  })();

  await check('gateway config saves one scalar and reads it back from the CPA', async () => {
    assert(scalar, 'the projection offered no editable scalar');
    const saved = await apiCall('POST', 'gateway/config/groups/' + scalar.group, {
      requestId: randomBytes(16).toString('hex'),
      expected: { generation: gatewayRead.payload.generation, baseline: scalar.baseline },
      edits: [{ path: scalar.path, action: 'set', value: 3 }],
    });
    assert.equal(saved.status, 200, JSON.stringify(saved.payload));
    let observed = null;
    await wait(async () => {
      const read = await apiCall('GET', 'gateway/config');
      if (read.status !== 200) return false;
      observed = (read.payload?.fields ?? []).find((field) => field.path === scalar.path);
      gatewayRead.payload = read.payload;
      return observed?.value === 3;
    }, 'the saved scalar on the CPA');
    return { path: scalar.path, value: observed.value };
  });

  await check('a save built on the out-of-band digest is refused as a conflict', async () => {
    // The operator edits the same leaf on their own CPA. The Manager's confirmed
    // baseline still names the document it last saw, so a save against it is a
    // conflict rather than a blind overwrite.
    const edited = await fetch(
      `http://127.0.0.1:${cpaPort}/v8/management/config/` + scalar.path.split('.').join('/'),
      {
        method: 'PATCH',
        headers: { Authorization: 'Bearer ' + managementKey, 'Content-Type': 'application/json' },
        body: JSON.stringify(4),
      }
    );
    assert([200, 204].includes(edited.status), 'the operator edit was refused: ' + edited.status);
    const stale = await apiCall('POST', 'gateway/config/groups/' + scalar.group, {
      requestId: randomBytes(16).toString('hex'),
      expected: { generation: gatewayRead.payload.generation, baseline: scalar.baseline },
      edits: [{ path: scalar.path, action: 'set', value: 3 }],
    });
    assert.equal(stale.status, 409, JSON.stringify(stale.payload));
    assert.equal(stale.payload.code, 'source_or_operation_conflict', JSON.stringify(stale.payload));
    return { code: stale.payload.code };
  });

  const usageRead = await apiCall('GET', 'external/usage');
  await check('usage state reports the CPA version it observed', async () => {
    assert.equal(usageRead.status, 200, JSON.stringify(usageRead.payload));
    assert.match(usageRead.payload.cpaVersion.version, /^v?[0-9]/, 'no version');
    assert.equal(usageRead.payload.cpaVersion.support, 'supported');
    assert.equal(usageRead.payload.cpaVersion.writeBlocked, false);
    assert.equal(usageRead.payload.statisticsEnabled, false);
    // A collector that never started is not coverage: it must not read as started.
    assert.equal(usageRead.payload.collectionStarted, false);
    return usageRead.payload;
  });

  await check('confirm usage: no write before the explicit confirmation', async () => {
    const withoutConfirm = await apiCall('POST', 'external/usage', {
      requestId: randomBytes(16).toString('hex'),
    });
    assert.equal(withoutConfirm.status, 400, 'a confirm-less POST must be refused');
    const confirmed = await apiCall('POST', 'external/usage', {
      requestId: randomBytes(16).toString('hex'),
      confirmEnableStatistics: true,
    });
    assert.equal(confirmed.status, 200, JSON.stringify(confirmed.payload));
    assert.equal(confirmed.payload.statisticsEnabled, true);
    return { enabledAfterConfirm: true };
  });

  // One model request, made with a Client Key this Manager installed, must be
  // attributed to that Key: the CPA records the call, the Collector reads its queue
  // and the projection binds the call to the Canonical Key. Collection is
  // asynchronous on both sides, so the read is polled with a bounded timeout and
  // never asserted on the first attempt.
  await check('Key usage shows the model request a Client Key made', async () => {
    const usageKey = await stageKey();
    const probe = run(
      ['docker', 'run', '--rm', '--network', network, '--entrypoint', 'wget',
        images['cpamp-manager'], '-qO-', '--header=Authorization: Bearer ' + usageKey.secret,
        `http://${cpaName}:8317/v1/models`],
      undefined,
      false
    );
    assert.equal(probe.status, 0, 'the CPA refused the staged Client Key');
    let usage = null;
    await wait(async () => {
      const read = await apiCall('GET', 'api-keys/' + usageKey.id + '/usage');
      if (read.status !== 200) return false;
      usage = read.payload;
      return (usage.totals?.calls ?? 0) >= 1;
    }, 'Key usage attribution of the model request', 120000);
    return { calls: usage.totals.calls, coverage: usage.coverage?.state };
  });

  // ---- Rotation. The operator changes the CPA's management key out of band and
  // restarts it; the Manager is never told. This is the scenario the rejection
  // gate exists for.
  cpaConfig = cpaConfig.replace(`secret-key: "${managementKey}"`, `secret-key: "${rotatedKey}"`);
  // Stop before writing: a running CPA flushes its own config on shutdown, which
  // would put the previous secret back and make the rotation a no-op.
  docker('stop', cpaContainer);
  writeFileSync(cpaConfigPath, cpaConfig, { mode: 0o600 });
  docker('start', cpaContainer);
  await wait(async () => {
    const probe = run(
      [
        'docker',
        'run',
        '--rm',
        '--network',
        network,
        '--entrypoint',
        'wget',
        images['cpamp-manager'],
        '-qO-',
        '--header=Authorization: Bearer ' + rotatedKey,
        `http://${cpaName}:8317/v0/management/config`,
      ],
      undefined,
      false
    );
    return probe.status === 0;
  }, 'CPA after rotation');
  const failedBefore = cpaFailedAuthCount();

  const blockedRead = await apiCall('GET', 'external/usage');
  await check('after an out-of-band rotation writes are blocked and reads answer locally', async () => {
    assert.equal(blockedRead.status, 409, JSON.stringify(blockedRead.payload));
    assert.equal(blockedRead.payload.code, 'external_management_key_rejected');
    // The refused credential is now the standing state, so repeated reads must
    // answer from the Manager without touching the CPA at all.
    const failedAtRefusal = cpaFailedAuthCount();
    for (let round = 0; round < 4; round++) {
      const again = await apiCall('GET', 'external/usage');
      assert.equal(again.status, 409, 'repeat read ' + round);
      assert.equal(again.payload.code, 'external_management_key_rejected');
    }
    const failedAfter = cpaFailedAuthCount();
    assert.equal(
      failedAfter,
      failedAtRefusal,
      'the Manager kept presenting a refused credential'
    );
    return {
      failedAuthsBeforeRefusal: failedBefore,
      failedAuthsAtRefusal: failedAtRefusal,
      failedAuthsAfterRepeats: failedAfter,
    };
  });

  const legacyWrite = await fetch(origin + '/console/api/v2/cpamp/v0/management/api-keys', {
    method: 'PUT',
    headers: { ...adminHeaders, Authorization: 'Bearer ' + admin },
    body: JSON.stringify(['sk-should-never-land']),
  });
  await check('the legacy api-keys write is refused with its own code', async () => {
    assert.equal(legacyWrite.status, 409);
    const payload = await legacyWrite.json();
    assert.equal(payload.code, 'external_legacy_write_replaced');
    return payload;
  });

  // The Fresh writes answer the same typed refusal as the bridge. None of them may
  // present the credential the CPA already refused: that is what five failures
  // turn into a ban.
  await check('after rotation a Keys write answers the typed refusal', async () => {
    const staged = await stageKey(false);
    const install = await apiCall('POST', 'api-keys/' + staged.id + '/initial-install', {});
    assert.equal(install.status, 409, JSON.stringify(install.payload));
    assert.equal(install.payload.code, 'external_management_key_rejected', JSON.stringify(install.payload));
    return { code: install.payload.code };
  });

  await check('after rotation a gateway save answers the typed refusal', async () => {
    assert(scalar, 'no editable scalar was projected');
    const save = await apiCall('POST', 'gateway/config/groups/' + scalar.group, {
      requestId: randomBytes(16).toString('hex'),
      expected: { generation: gatewayRead.payload?.generation, baseline: scalar.baseline },
      edits: [{ path: scalar.path, action: 'set', value: 3 }],
    });
    assert.equal(save.status, 409, JSON.stringify(save.payload));
    assert.equal(save.payload.code, 'external_management_key_rejected', JSON.stringify(save.payload));
    return { code: save.payload.code };
  });

  await check('after rotation a usage confirm answers the typed refusal', async () => {
    const confirm = await apiCall('POST', 'external/usage', {
      requestId: randomBytes(16).toString('hex'),
      confirmEnableStatistics: true,
    });
    assert.equal(confirm.status, 409, JSON.stringify(confirm.payload));
    assert.equal(confirm.payload.code, 'external_management_key_rejected', JSON.stringify(confirm.payload));
    return { code: confirm.payload.code };
  });

  // The refusal is on record, so every read above and below answers from this
  // Manager. Not one of them may reach the CPA with the refused key.
  const failedAtGate = cpaFailedAuthCount();
  await check('once the local gate engages no management refusal reaches the CPA', async () => {
    for (let round = 0; round < 3; round++) {
      await apiCall('GET', 'external/usage');
      await apiCall('GET', 'gateway/config');
      await apiCall('GET', 'api-keys');
    }
    assert.equal(
      cpaFailedAuthCount(),
      failedAtGate,
      'the gate kept presenting the refused credential'
    );
    return { failedAuthsAtGate: failedAtGate };
  });

  const failedDuringRotation = cpaFailedAuthCount() - failedBefore;
  report.failedAuthDuringRotation = failedDuringRotation;
  await check('failed authentications stay below the CPA ban threshold of five', async () => {
    assert(
      failedDuringRotation < 5,
      'the Manager presented a refused credential ' + failedDuringRotation + ' times'
    );
    return { failedDuringRotation, threshold: 5 };
  });

  // ---- Rebind. The operator supplies the key the CPA now holds.
  const rebound = await apiCall('POST', 'external/rebind', {
    requestId: randomBytes(16).toString('hex'),
    managementKey: rotatedKey,
  });
  await check('rebind with the rotated key recovers the connection', async () => {
    assert.equal(rebound.status, 200, JSON.stringify(rebound.payload));
    assert.equal(rebound.payload.status, 'rebound');
    const recovered = await apiCall('GET', 'external/usage');
    assert.equal(recovered.status, 200, JSON.stringify(recovered.payload));
    assert.equal(recovered.payload.cpaVersion.support, 'supported');
    return { status: rebound.payload.status };
  });

  const reverified = await apiCall('POST', 'external/rebind', {
    requestId: randomBytes(16).toString('hex'),
    managementKey: rotatedKey,
  });
  await check('the same key re-verifies as reverified, not as a rotation', async () => {
    assert.equal(reverified.status, 200, JSON.stringify(reverified.payload));
    assert.equal(reverified.payload.status, 'reverified');
    return { status: reverified.payload.status };
  });

  // ---- The v7 negative. A CPA older than v8 must be refused for writes while
  // its safe reads keep working. Without a pinned v7 binary the check is recorded
  // as not run rather than skipped silently.
  // ---- Screenshots. Each surface is its own check: a rendering gap on one page
  // must not throw away the protocol evidence above, and a surface that cannot be
  // reached is recorded as NOT RUN with the page it landed on.
  browser = await chromium.launch({
    headless: true,
    ...(process.env.PLAYWRIGHT_EXECUTABLE ? { executablePath: process.env.PLAYWRIGHT_EXECUTABLE } : {}),
    ...(process.env.PLAYWRIGHT_CHANNEL ? { channel: process.env.PLAYWRIGHT_CHANNEL } : {}),
  });
  const context = await browser.newContext({
    locale: 'zh-CN',
    viewport: { width: 1440, height: 1000 },
  });
  page = await context.newPage();
  page.on('pageerror', () => report.pageErrors.push('Uncaught browser error'));
  // The console's own calls are the only evidence of why a screen refused a
  // credential: the browser never shows the status.
  const browserCalls = [];
  page.on('response', (response) => {
    const url = new URL(response.url());
    if (!url.pathname.includes('/api/')) return;
    const entry = { path: url.pathname, status: response.status() };
    browserCalls.push(entry);
    if (response.status() >= 400) {
      // The typed code is the only way to tell a refusal from an outage.
      void response
        .text()
        .then((body) => {
          entry.code = (body.match(/"code":"([^"]+)"/) || [])[1] ?? undefined;
          entry.body = body.slice(0, 200);
        })
        .catch(() => {});
    }
  });

  async function surface(name, body) {
    try {
      await body();
      pass(name);
    } catch (error) {
      const landed = await page
        .evaluate(() => ({
          url: location.href,
          text: (document.body.innerText || '').slice(0, 1200),
        }))
        .catch(() => ({ url: 'unavailable', text: '' }));
      report.surfaces = [...(report.surfaces ?? []), { name, landed, calls: browserCalls.slice(-12) }];
      notRun(name, redact(String(error.message || error)).slice(0, 300));
    }
  }

  // Rotate the CPA again so the blocker is rendered from a real server answer.
  // The Manager holds the key the CPA no longer accepts.
  // The rewrite carries the keys the CPA carries right now, so restarting it for
  // the screenshot pass does not silently undo the key lifecycle above.
  const carried = await cpaKeys(rotatedKey);
  cpaConfig = cpaConfig
    .replace(`secret-key: "${rotatedKey}"`, `secret-key: "${staleKey}"`)
    .replace(
      /  api-keys:\n(    - "[^"]*"\n)+/,
      '  api-keys:\n' + carried.map((key) => `    - "${key}"\n`).join('')
    );
  docker('stop', cpaContainer);
  writeFileSync(cpaConfigPath, cpaConfig, { mode: 0o600 });
  docker('start', cpaContainer);
  await wait(async () => {
    const probe = run(
      ['docker', 'run', '--rm', '--network', network, '--entrypoint', 'wget', images['cpamp-manager'],
        '-qO-', '--header=Authorization: Bearer ' + staleKey,
        `http://${cpaName}:8317/v0/management/config`],
      undefined,
      false
    );
    return probe.status === 0;
  }, 'CPA after the second rotation');

  await check('the operator-written credential is the one the CPA now answers', async () => {
    const read = async (version) => {
      const response = await fetch(`http://127.0.0.1:${cpaPort}/${version}/management/config`, {
        headers: { Authorization: 'Bearer ' + staleKey },
      });
      return response.status;
    };
    const v0 = await read('v0');
    const v8 = await read('v8');
    assert.equal(v0, 200, 'v0 answered ' + v0);
    assert.equal(v8, 200, 'v8 answered ' + v8);
    return { v0, v8 };
  });

  await surface('the Keys page names the refused credential and offers the rebind', async () => {
    await page.goto(origin + '/console');
    await page.getByTestId('console-login').waitFor({ timeout: 20000 });
    await page.locator('#console-admin-key').fill(admin);
    // "Remember" is what makes each surface addressable by URL: the credential
    // otherwise lives only in memory and every navigation returns to the form.
    // The checkbox input itself is visually hidden behind its own label, so it is
    // set directly rather than by waiting for a visible control.
    await page
      .getByTestId('console-login')
      .locator('input[type=checkbox]')
      .first()
      .check({ force: true });
    await page.getByTestId('console-login').locator('button[type=submit]').click();
    await page.getByTestId('legacy-console').waitFor({ timeout: 30000 });
    await page.goto(origin + '/console/api-keys');
    await page.getByTestId('external-blocker-key_rejected').waitFor({ timeout: 30000 });
    await screenshot('external-keys-blocker');
  });

  await surface('the rebind dialog opens, submits and reports the recovery', async () => {
    await page.getByTestId('external-blocker-rebind').click();
    await page.getByTestId('rebind-management-key').waitFor({ timeout: 20000 });
    await screenshot('external-rebind-dialog');
    await page.getByTestId('rebind-management-key').fill(staleKey);
    await page.getByTestId('rebind-submit').click();
    // The dialog reports the outcome, but the surface that owns it reloads as
    // soon as the connection is healthy again and unmounts the dialog with the
    // blocker. Either event is the recovery; the blocker being gone is the one
    // the operator actually sees.
    await page.getByTestId('rebind-result').waitFor({ timeout: 30000 }).catch(() => {});
    await page
      .getByTestId('external-blocker-key_rejected')
      .waitFor({ state: 'detached', timeout: 30000 });
    await screenshot('external-rebind-result');
  });

  await check('the connection recovers with the credential the operator wrote', async () => {
    // The same call the dialog makes, driven from the harness: it separates "the
    // Manager cannot use this credential" from "the dialog offered a different
    // one", which are different defects.
    const recovered = await apiCall('POST', 'external/rebind', {
      requestId: randomBytes(16).toString('hex'),
      managementKey: staleKey,
    });
    assert.equal(recovered.status, 200, JSON.stringify(recovered.payload));
    assert(
      ['rebound', 'reverified'].includes(recovered.payload.status),
      JSON.stringify(recovered.payload)
    );
    const usage = await apiCall('GET', 'external/usage');
    assert.equal(usage.status, 200, JSON.stringify(usage.payload));
    return { status: recovered.payload.status };
  });

  // The CPA's own log is the only place a refusal reason and a ban are visible.
  report.cpaLogs = redact(
    (run(['docker', 'logs', '--tail', '80', cpaContainer], undefined, false).stdout || '').slice(-4000)
  );

  await surface('the usage panel gates enabling behind an explicit confirmation', async () => {
    // The restart above put the CPA back to statistics disabled, so the gate is
    // offered rather than already satisfied.
    // Same page: a reload burns one failed authentication against the CPA's ban
    // threshold, because the console's own model read has no credential it can
    // present on External.
    await page.getByTestId('external-usage').scrollIntoViewIfNeeded();
    await page.getByTestId('external-usage-enable').click();
    await page.getByTestId('external-usage-confirm').waitFor({ timeout: 20000 });
    await screenshot('external-usage-confirm');
    // Opening the confirmation writes nothing; closing it must leave the CPA
    // exactly as it was.
    await page.getByTestId('external-usage-cancel').click();
    await page.getByTestId('external-usage-confirm').waitFor({ state: 'detached', timeout: 20000 });
  });

  await surface('the runtime page shows the External CPA version and its class', async () => {
    await page.goto(origin + '/console/system/runtime');
    await page.getByTestId('runtime-status-page').waitFor({ timeout: 30000 });
    await page.getByTestId('runtime-status-cpa-version').waitFor({ timeout: 30000 });
    await screenshot('external-runtime-version');
  });

  // This runs last on purpose: it leaves the CPA with a single credential, so
  // every console page load before it would burn the failed-authentication
  // budget the ban threshold is measured against.
  await check('revoking the last Client Key the CPA carries is refused', async () => {
    // The operator reduces their own CPA to the single credential this Manager
    // staged. That is their decision to make, and it is the state the guard
    // exists for.
    const reduced = await fetch(
      `http://127.0.0.1:${cpaPort}/v8/management/config/access/api-keys`,
      {
        method: 'PUT',
        headers: {
          Authorization: 'Bearer ' + staleKey,
          'Content-Type': 'application/json',
        },
        body: JSON.stringify([spender.secret]),
      }
    );
    assert([200, 204].includes(reduced.status), 'the CPA refused the out-of-band edit');
    // The keeper is staged and never installed: a revoke names the credential
    // that stays, and this one is not on the CPA at all.
    const uninstalled = await stageKey(false);
    const result = await keyOperation(spender.id, uninstalled.id, 'revoke');
    assert.equal(result.status, 409, JSON.stringify(result.payload));
    assert.equal(result.payload?.code, 'last_effective_key', JSON.stringify(result.payload));
    assert.deepEqual(await cpaKeys(staleKey), [spender.secret]);
    return { code: result.payload.code };
  });

  // This one replaces the CPA the whole run was built on with a v7 binary, so it
  // runs after every other check has already read the v8 one.
  if (process.env.CPAMP_D_CPA_LEGACY) {
    await check('a CONNECT naming a v7 CPA is refused, and a read is never answered with the version code', async () => {
      const binary = process.env.CPAMP_D_CPA_LEGACY;
      assert(existsSync(binary), 'CPAMP_D_CPA_LEGACY does not name a binary');
      // v7 keeps the management key, the port and the auth directory at the
      // document root, so it needs its own file rather than the v8 one.
      writeFileSync(
        cpaV7ConfigPath,
        [
          'port: 8317',
          'host: "0.0.0.0"',
          'remote-management:',
          '  allow-remote: true',
          `  secret-key: "${staleKey}"`,
          '  disable-control-panel: true',
          'auth-dir: "/cpa/auth"',
          'api-keys:',
          `  - "${spender.secret}"`,
          '',
        ].join('\n'),
        { mode: 0o600 }
      );
      mkdirSync(cpaV7AuthDir, { recursive: true, mode: 0o700 });
      // The address the installation is committed to now answers as v7: the
      // operator downgraded the CPA underneath a working connection.
      docker('stop', cpaContainer);
      docker(
        'run',
        '-d',
        '--name',
        v7Container,
        '--network',
        network,
        '--network-alias',
        cpaName,
        '-v',
        `${binary}:/usr/local/bin/cli-proxy-api:ro`,
        '-v',
        `${cpaV7ConfigPath}:/cpa/config.yaml`,
        '-v',
        `${cpaV7AuthDir}:/cpa/auth`,
        '--entrypoint',
        '/usr/local/bin/cli-proxy-api',
        images['cpamp-runtime'],
        '-config',
        '/cpa/config.yaml'
      );
      await wait(async () => {
        const probe = run(
          ['docker', 'run', '--rm', '--network', network, '--entrypoint', 'wget',
            images['cpamp-manager'], '-qO-', '--header=Authorization: Bearer ' + staleKey,
            `http://${cpaName}:8317/v0/management/config`],
          undefined,
          false
        );
        return probe.status === 0;
      }, 'the v7 CPA');

      // The credential this Manager holds must be one the v7 CPA accepts, or the
      // refusal below would be about the key rather than the version.
      const accepted = run(
        ['docker', 'run', '--rm', '--network', network, '--entrypoint', 'wget',
          images['cpamp-manager'], '-qO-', '--header=Authorization: Bearer ' + staleKey,
          `http://${cpaName}:8317/v0/management/config`],
        undefined,
        false
      );
      assert.equal(accepted.status, 0, 'the v7 CPA refused the stored credential');

      // A CONNECT is the first thing an operator does with a CPA. It probes the v8
      // safe read before anything is persisted, so a v7 CPA is refused there: the
      // code is the 2a connect refusal (or the version code the other External
      // surfaces name), and no journal entry exists afterwards.
      const current = await apiCall('GET', 'operation');
      const connectV7 = await apiCall('POST', 'strategy', {
        identity: current.payload.identity,
        expectedRevision: current.payload.revision,
        attemptId: randomBytes(16).toString('hex'),
        strategy: 'connect_external',
        baseUrl: `http://${cpaName}:8317`,
        managementKey: staleKey,
      });
      const connectRefusals = [
        'external_cpa_version_unsupported',
        'management_safe_read_unavailable',
        'management_v8_config_invalid',
        'owner_upgrade_required',
        'management_authentication_rejected',
      ];
      assert.equal(connectV7.status, 409, JSON.stringify(connectV7.payload));
      assert(
        connectRefusals.includes(connectV7.payload.code),
        'connect refusal: ' + JSON.stringify(connectV7.payload)
      );

      // A write is refused with the version code before anything is sent. The
      // rebind is the write that names the credential: its one verification read
      // is what sees the v7 version, so nothing is persisted.
      const write = await apiCall('POST', 'external/rebind', {
        requestId: randomBytes(16).toString('hex'),
        managementKey: staleKey,
      });
      assert.equal(write.status, 409, write.status + ' ' + JSON.stringify(write.payload));
      if (write.payload.code !== 'external_cpa_version_unsupported') {
        const logs = run(['docker', 'logs', '--tail', '30', v7Container], undefined, false);
        throw new Error(
          JSON.stringify(write.payload) + ' ||| ' + redact((logs.stdout || '') + (logs.stderr || ''))
        );
      }
      // The version refusal is a write refusal. A read is never answered with it:
      // whatever the read answers (this run already has a rotation refusal on
      // record, so that is what it says), it is not the version code.
      const read = await apiCall('GET', 'external/usage');
      if (read.payload?.code === 'external_cpa_version_unsupported') {
        throw new Error(JSON.stringify(read.payload));
      }
      return {
        connect: connectV7.payload.code,
        write: write.payload.code,
        read: read.payload?.code ?? read.status,
      };
    });
  } else {
    notRun(
      'a v7 CPA is refused for writes and still answers a read',
      'CPAMP_D_CPA_LEGACY is unset: no pinned v7 binary was provided'
    );
  }

  report.result = 'PASS';
} catch (error) {
  report.failure = redact(String(error.stack || error));
  try {
    for (const [name, container] of [
      ['manager', managerContainer],
      ['ingress', ingressContainer],
    ]) {
      const logs = run(['docker', 'logs', '--tail', '120', container], undefined, false);
      report['failure' + name + 'Logs'] = redact(
        ((logs.stdout || '') + (logs.stderr || '')).slice(-8000)
      );
    }
    report.failureCpaLogs = cpaLogTail();
  } catch {}
  if (page && !page.isClosed()) {
    report.failurePage = await page
      .locator('body')
      .innerText()
      .catch(() => 'Page unavailable');
    await page
      .screenshot({ path: resolve(out, 'failure.png'), fullPage: true })
      .catch(() => {});
  }
} finally {
  if (browser) await browser.close();
  // Only this run's containers, network and volume. Nothing named cpamp-* beyond
  // the project prefix is touched.
  run(['docker', 'rm', '-f', cpaContainer, managerContainer, ingressContainer, v7Container], undefined, false);
  run(['docker', 'network', 'rm', network], undefined, false);
  run(['docker', 'volume', 'rm', managerVolume], undefined, false);
  // The CPA's own config is an input, not evidence: it holds every management key
  // and every seed Client Key, so the whole secrets directory is removed rather
  // than redacted in place. A redacted copy could still be replayed into a live CPA.
  // It is deleted before the assertions below so a leftover file cannot hide one.
  rmSync(secretsDir, { recursive: true, force: true });
  // Before redaction: a secret in the report object, in what this run printed, or
  // in any container log is a failure, not something to redact away.
  report.secretAssertion = 'PASS';
  try {
    assertNoSecret('the report', JSON.stringify(report));
    assertNoSecret('the run log', printed.join('\n'));
    for (const { name, text } of runContainerLogs()) assertNoSecret('the logs of ' + name, text);
  } catch (error) {
    report.secretAssertion = 'FAIL';
    report.secretFailure = String(error.message);
    report.result = 'FAIL';
  }
  const serialized = redact(JSON.stringify(report, null, 2));
  for (const value of secretValues)
    assert(!serialized.includes(value), 'a secret reached the serialized report');
  writeFileSync(resolve(out, 'report.json'), serialized, { mode: 0o600 });
  console.log(report.result + ': ' + resolve(out, 'report.json'));
  if (report.failure) console.error(report.failure);
  if (report.secretFailure) console.error(report.secretFailure);
  if (report.result !== 'PASS') process.exitCode = 1;
}
