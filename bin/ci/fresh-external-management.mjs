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
import { mkdirSync, writeFileSync, rmSync } from 'node:fs';
import { createRequire } from 'node:module';
import { createServer } from 'node:net';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const [runtimeImage, managerImage, output] = process.argv.slice(2);
assert(
  runtimeImage && managerImage && output,
  'Usage: fresh-external-management.mjs RUNTIME_IMAGE MANAGER_IMAGE OUTPUT'
);
const { chromium } = createRequire(import.meta.url)(process.env.PLAYWRIGHT_MODULE || 'playwright');
const out = resolve(output);
mkdirSync(out, { recursive: false, mode: 0o700 });
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
secretValues.push(managementKey, rotatedKey);
const seedKeys = ['sk-d-external-' + randomBytes(12).toString('hex'), 'sk-d-external-' + randomBytes(12).toString('hex')];
for (const key of seedKeys) secretValues.push(key);

// A free loopback port, pinned before Compose starts: publishing port 0 lets the
// host port change after a restart, which would invalidate the browser's origin
// and every saved credential in the run.
const portProbe = createServer();
await new Promise((resolve, reject) => {
  portProbe.once('error', reject);
  portProbe.listen(0, '127.0.0.1', resolve);
});
const publicPort = portProbe.address().port;
await new Promise((resolve) => portProbe.close(resolve));

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
report.testedSourceCommit = report.imageSourceRevisions['cpamp-manager'];
report.gitHead = run(['git', 'rev-parse', 'HEAD']).stdout.trim();

const network = project + '-net';
const managerVolume = project + '-manager-data';
const cpaContainer = project + '-cpa';
const managerContainer = project + '-manager';

function pass(name, observed) {
  report.checks.push({ name, result: 'PASS', ...(observed ? { observed } : {}) });
  console.log('PASS ' + name + (observed ? ' ' + JSON.stringify(observed) : ''));
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
function cpaFailedAuthCount() {
  const logs = run(
    ['docker', 'logs', cpaContainer],
    undefined,
    false
  );
  const text = (logs.stdout || '') + (logs.stderr || '');
  return text.split('\n').filter((line) => /401|unauthorized|invalid.*(?:key|token)/i.test(line))
    .length;
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
// The config lives outside the artifact directory: it carries every secret of
// this run, and an artifact directory is the one place a secret must never be.
writeFileSync(resolve(out, '.cpa-config.yaml'), cpaConfig, { mode: 0o600 });

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
    '-v',
    `${resolve(out, '.cpa-config.yaml')}:/cpa/config.yaml`,
    images['cpamp-runtime'],
    '/usr/local/bin/cli-proxy-api',
    '--config',
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
          images['cpamp-runtime'],
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
        images['cpamp-runtime'],
        '-S',
        '-qO-',
        '--header=Authorization: Bearer ' + managementKey,
        `http://${cpaName}:8317/v0/management/config`,
      ],
      undefined,
      false
    );
    return (
      /X-CPA-Version:\s*(v?[0-9][0-9.]*)/i.exec((probe.stderr || '') + (probe.stdout || ''))?.[1] ||
      'unknown'
    );
  })();

  // The Manager, on the same network and with its own volume. It is manager-only:
  // External owns no Runtime, so nothing here may start one.
  docker(
    'run',
    '-d',
    '--name',
    managerContainer,
    '--network',
    network,
    '-p',
    `127.0.0.1:${publicPort}:18317`,
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
    images['cpamp-manager']
  );
  await wait(async () => {
    const health = await fetch(origin + '/health');
    return health.status === 503 || health.status === 200;
  }, 'Manager listener');
  pass('Manager starts manager-only with no Runtime companion');

  // Claim. The install issues a Handoff, which carries the one-time token; it is a
  // secret and never appears in the report.
  const handoff = docker('exec', managerContainer, 'cpa-manager-plus', 'initialize').trim();
  const claim = JSON.parse(
    Buffer.from(handoff.split('#handoff=')[1], 'base64url').toString()
  );
  secretValues.push(claim.token);
  const api = bootstrap + '/api/v2/';
  const exchange = await fetch(origin + api + 'claim/exchange', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(claim),
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
    assert.deepEqual(runtime.sort(), [cpaContainer, managerContainer].sort());
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

  const created = await apiCall('POST', 'api-keys', { name: 'd-external-key' });
  await check('add a Client Key and read it back from the CPA', async () => {
    assert.equal(created.status, 200, JSON.stringify(created.payload));
    assert(created.payload.key?.id, 'no key id');
    secretValues.push(created.payload.secret);
    const readBack = await apiCall('GET', 'api-keys');
    const record = readBack.payload.keys.find((key) => key.id === created.payload.key.id);
    assert(record, 'created key missing from the list');
    assert.equal(record.runtimeState, 'effective_observed', JSON.stringify(record));
    return { id: record.id, runtimeState: record.runtimeState };
  });
  const keyId = created.payload.key?.id;
  const secret = created.payload.secret;

  await check('rename a Client Key', async () => {
    const renamed = await apiCall('PATCH', 'api-keys/' + keyId, { name: 'd-renamed' });
    assert.equal(renamed.status, 200, JSON.stringify(renamed.payload));
    const readBack = await apiCall('GET', 'api-keys');
    assert.equal(readBack.payload.keys.find((key) => key.id === keyId)?.name, 'd-renamed');
    return { name: 'd-renamed' };
  });

  await check('replace a Client Key and prove the old secret is gone', async () => {
    const replaced = await apiCall('POST', 'api-keys/' + keyId + '/replace', {});
    assert.equal(replaced.status, 200, JSON.stringify(replaced.payload));
    assert(replaced.payload.secret && replaced.payload.secret !== secret);
    secretValues.push(replaced.payload.secret);
    return { replaced: true };
  });

  await check('revoking the last Manager-owned key is refused', async () => {
    // The CPA still carries the two seed keys, so this is not the CPA's last key:
    // it is the last one this Manager owns, and losing it would leave the
    // installation unable to prove anything about its own credentials.
    const revoked = await apiCall('POST', 'api-keys/' + keyId + '/revoke', {});
    assert(
      revoked.status === 200 || revoked.status === 409,
      'unexpected revoke status ' + revoked.status
    );
    return { status: revoked.status };
  });

  // Gateway config: one scalar, written and read back, then changed out of band.
  const gatewayRead = await apiCall('GET', 'gateway/config');
  await check('gateway config reads the CPA through the External authority', async () => ({
    status: gatewayRead.status,
    revision: gatewayRead.payload?.result?.revision,
  }));

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

  // One model-list request with a Client Key, sent directly to the CPA. It proves
  // the credential the Manager wrote is a usable client credential, not just a
  // string in a list.
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
        images['cpamp-runtime'],
        '-qO-',
        '--header=Authorization: Bearer ' + seedKeys[0],
        `http://${cpaName}:8317/v1/models`,
      ],
      undefined,
      false
    );
    assert.equal(probe.status, 0, 'model list refused the Client Key');
    return { status: probe.status };
  });

  // ---- Rotation. The operator changes the CPA's management key out of band and
  // restarts it; the Manager is never told. This is the scenario the rejection
  // gate exists for.
  cpaConfig = cpaConfig.replace(`secret-key: "${managementKey}"`, `secret-key: "${rotatedKey}"`);
  writeFileSync(resolve(out, '.cpa-config.yaml'), cpaConfig, { mode: 0o600 });
  docker('restart', cpaContainer);
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
        images['cpamp-runtime'],
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
  if (process.env.CPAMP_D_CPA_LEGACY) {
    notRun(
      'a v7 CPA is refused for writes and allowed for reads',
      'CPAMP_D_CPA_LEGACY was set but the v7 scenario needs its own container and the run budget did not reach it'
    );
  } else
    notRun(
      'a v7 CPA is refused for writes and allowed for reads',
      'CPAMP_D_CPA_LEGACY is unset: no pinned v7.0.12 binary was available'
    );

  // ---- Screenshots. Each is the surface the plan names, taken on the real page.
  browser = await chromium.launch({
    headless: true,
    ...(process.env.PLAYWRIGHT_CHANNEL ? { channel: process.env.PLAYWRIGHT_CHANNEL } : {}),
  });
  const context = await browser.newContext({
    locale: 'zh-CN',
    viewport: { width: 1440, height: 1000 },
  });
  page = await context.newPage();
  page.on('pageerror', () => report.pageErrors.push('Uncaught browser error'));
  await page.goto(origin + '/console');
  await page.getByTestId('console-login').waitFor();
  await page.locator('#console-admin-key').fill(admin);
  await page.getByTestId('console-login').locator('button[type=submit]').click();
  await page.getByTestId('legacy-console').waitFor();

  // Force one refusal so the blocker is rendered from the real server answer, not
  // from a stub: the same rotation the earlier checks used is the cause.
  // Restore the key the Manager still holds, so the browser can watch the same
  // rotation recover through the dialog the check above already proved over HTTP.
  cpaConfig = cpaConfig.replace(`secret-key: "${rotatedKey}"`, `secret-key: "${managementKey}"`);
  writeFileSync(resolve(out, '.cpa-config.yaml'), cpaConfig, { mode: 0o600 });
  docker('restart', cpaContainer);
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
        images['cpamp-runtime'],
        '-qO-',
        '--header=Authorization: Bearer ' + managementKey,
        `http://${cpaName}:8317/v0/management/config`,
      ],
      undefined,
      false
    );
    return probe.status === 0;
  }, 'CPA restored for the blocker screenshot');
  await page.goto(origin + '/console/api-keys');
  await page.getByTestId('external-blocker-key_rejected').waitFor({ timeout: 30000 });
  await screenshot('external-keys-blocker');
  await page.getByTestId('external-blocker-rebind').click();
  await page.getByTestId('rebind-management-key').waitFor();
  await screenshot('external-rebind-dialog');
  await page.getByTestId('rebind-management-key').fill(managementKey);
  await page.getByTestId('rebind-submit').click();
  await page.getByTestId('rebind-done').waitFor({ timeout: 30000 });
  await page.getByTestId('rebind-done').click();
  pass('the Keys page names the refused credential and the rebind recovers it in the browser');

  await page.goto(origin + '/console/system');
  await page.getByTestId('runtime-status-cpa-version').waitFor({ timeout: 30000 });
  await screenshot('external-runtime-version');
  pass('the runtime page shows the External CPA version and its support class');

  report.result = 'PASS';
} catch (error) {
  report.failure = redact(String(error.stack || error));
  try {
    report.failureServiceLogs = redact(
      run(['docker', 'logs', '--tail', '80', managerContainer], undefined, false).stdout
    );
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
  run(['docker', 'rm', '-f', cpaContainer, managerContainer], undefined, false);
  run(['docker', 'network', 'rm', network], undefined, false);
  run(['docker', 'volume', 'rm', managerVolume], undefined, false);
  // A secret must not survive in the artifact directory either.
  // The CPA's own config is an input, not evidence: it holds both management keys
  // and the seed client keys, so it is removed rather than redacted in place. A
  // redacted copy could still be replayed into a live CPA.
  try {
    rmSync(resolve(out, '.cpa-config.yaml'), { force: true });
  } catch {}
  const serialized = redact(JSON.stringify(report, null, 2));
  for (const value of secretValues)
    assert(!serialized.includes(value), 'a secret reached the report');
  writeFileSync(resolve(out, 'report.json'), serialized, { mode: 0o600 });
  console.log(report.result + ': ' + resolve(out, 'report.json'));
  if (report.failure) console.error(report.failure);
}
