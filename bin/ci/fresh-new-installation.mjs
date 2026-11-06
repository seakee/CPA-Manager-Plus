#!/usr/bin/env node
// Disposable normal-Compose Fresh New acceptance. No S1/S2 seed or shared state.
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { randomBytes, createHash } from 'node:crypto';
import { mkdirSync, writeFileSync, readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { createServer } from 'node:net';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const [runtimeImage, managerImage, ingressImage, output] = process.argv.slice(2);
assert(
  runtimeImage && managerImage && ingressImage && output,
  'Usage: fresh-new-installation.mjs RUNTIME_IMAGE MANAGER_IMAGE INGRESS_IMAGE NEW_OUTPUT'
);
const { chromium } = createRequire(import.meta.url)(process.env.PLAYWRIGHT_MODULE || 'playwright');
const out = resolve(output);
mkdirSync(out, { recursive: false, mode: 0o700 });
const project = 'cpamp-fresh-new-' + randomBytes(6).toString('hex');
const bootstrap = '/.well-known/cpamp-bootstrap';
const api = bootstrap + '/api/v2/';
const pin = 'e48c85c75a19adae186cbee590f6e6143fd8085417d8066b545e8533877175f7';
const report = {
  result: 'FAIL',
  project,
  checks: [],
  screenshots: [],
  pageErrors: [],
  notRun: [
    'Real Provider model success (no Provider fixture or credential exists in this disposable installation; the model-request check proves Client Key authentication only)',
    'Real Provider/model traffic, other four journeys, arm64, power-loss and production rollback',
    'Natural ten-minute claim expiry (expired server timestamp is injected offline only in this disposable fixture)',
    'Non-Chinese locale layouts, other browser engines',
  ],
};
// The model-request probe must reach CPA where this deployment publishes it.
// A Fresh Managed install keeps CPA on the Compose network, and the Ingress
// Gateway publishes model traffic once it is admitted, so both origins reach
// the same CPA. The value is an origin, never a full request path.
const modelTarget = process.env.CPAMP_SMOKE_MODEL_TARGET || 'http://cpamp-runtime:8317';
let modelTargetIsIngress = false;
{
  const parsed = new URL(modelTarget);
  assert(
    (parsed.protocol === 'http:' || parsed.protocol === 'https:') && parsed.host,
    'CPAMP_SMOKE_MODEL_TARGET must be an http(s) origin'
  );
  // The probe reaches CPA through the Runtime network or the admitted Ingress
  // Gateway; the report records which one so a failure names its path.
  modelTargetIsIngress = parsed.hostname === 'cpamp-ingress' || parsed.port === '18317';
}
report.modelTarget = modelTarget;
report.modelTargetIsIngress = modelTargetIsIngress;
// Pin a free loopback port in HostConfig. Publishing port 0 can allocate a new
// host port after restart, which would invalidate the browser's saved origin.
const portProbe = createServer();
await new Promise((resolve, reject) => {
  portProbe.once('error', reject);
  portProbe.listen(0, '127.0.0.1', resolve);
});
const publicPort = portProbe.address().port;
await new Promise((resolve) => portProbe.close(resolve));
const env = { ...process.env, CPAMP_PUBLIC_PORT: `127.0.0.1:${publicPort}` };
const secretValues = [];
let browser;
let page;
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
        args.slice(0, 3).join(' ') +
        '\n' +
        secretValues.reduce(
          (message, value) => message.replaceAll(value, '[REDACTED]'),
          result.stderr || ''
        )
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
report.testedSourceCommit = report.imageSourceRevisions['cpamp-manager'];
report.source = docker('version', '--format', '{{.Server.Os}}/{{.Server.Arch}}');
report.gitHead = run(['git', 'rev-parse', 'HEAD']).stdout.trim();
report.diffSha256 = createHash('sha256')
  .update(run(['git', 'diff', '--binary', 'HEAD']).stdout)
  .digest('hex');
report.sourceFiles = Object.fromEntries(
  [
    ...new Set(
      (
        run(['git', 'diff', '--name-only', 'HEAD']).stdout +
        run(['git', 'ls-files', '--others', '--exclude-standard']).stdout
      )
        .trim()
        .split('\n')
    ),
  ]
    .filter((file) =>
      /^(apps\/(web\/src|manager-server\/cmd)\/|bin\/ci\/fresh-new-installation)/.test(file)
    )
    .map((file) => [
      file,
      createHash('sha256')
        .update(readFileSync(resolve(root, file)))
        .digest('hex'),
    ])
);
const override = resolve(out, 'compose.override.json');
writeFileSync(
  override,
  JSON.stringify(
    {
      services: Object.fromEntries(
        Object.entries(images).map(([service, image]) => [
          service,
          {
            image,
            platform: 'linux/amd64',
            ...(service === 'cpamp-manager'
              ? { environment: { CPAMP_PUBLIC_ORIGIN: 'http://cpamp-ingress:18317' } }
              : {}),
          },
        ])
      ),
    },
    null,
    2
  )
);
const compose = [
  'docker',
  'compose',
  '--project-directory',
  root,
  '-p',
  project,
  '-f',
  resolve(root, 'docker-compose.yml'),
  '-f',
  override,
];
const dc = (...args) => run([...compose, ...args]).stdout.trim();
function pass(name) {
  report.checks.push({ name, result: 'PASS' });
  console.log('PASS ' + name);
}
function notRun(name) {
  report.checks.push({ name, result: 'NOT RUN' });
  console.log('NOT RUN ' + name);
}
async function screenshot(name, fullPage = true) {
  const overflow = await page.evaluate(() => ({
    width: innerWidth,
    scrollWidth: document.documentElement.scrollWidth,
    elements: [...document.querySelectorAll('body *')]
      .filter((element) => element.getBoundingClientRect().right > innerWidth + 1)
      .slice(0, 15)
      .map((element) => ({
        tag: element.tagName,
        class: element.getAttribute('class'),
        right: element.getBoundingClientRect().right,
      })),
  }));
  assert(
    await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth),
    'horizontal overflow: ' + name + ' ' + JSON.stringify(overflow)
  );
  await page.screenshot({
    path: resolve(out, name + '.png'),
    fullPage: fullPage && page.viewportSize().width > 768,
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
async function installationScreenshots(stage) {
  for (const [name, width, light] of [
    ['desktop-dark', 1440, false],
    ['desktop-light', 1440, true],
    ['mobile-light', 390, true],
    ['mobile-dark', 390, false],
  ]) {
    await page.setViewportSize({ width, height: 1000 });
    if ((await page.locator('html').evaluate((html) => html.classList.contains('light'))) !== light)
      await page.getByRole('button', { name: '切换明暗主题', exact: true }).click();
    await screenshot('installation-' + stage + '-' + name);
  }
  await page.setViewportSize({ width: 1440, height: 1000 });
}
async function wait(check, label, timeout = 45000) {
  const end = Date.now() + timeout;
  do {
    try {
      if (await check()) return;
    } catch {}
    await new Promise((r) => setTimeout(r, 250));
  } while (Date.now() < end);
  throw new Error('Timed out: ' + label);
}
const pythonImage = docker('image', 'inspect', 'python:3.13-alpine', '--format', '{{.Id}}');
function python(volume, program, input, readonly = false) {
  return run(
    [
      'docker',
      'run',
      '--rm',
      '-i',
      '--network',
      'none',
      '-v',
      `${volume}:/data${readonly ? ':ro' : ''}`,
      pythonImage,
      'python3',
      '-c',
      program,
    ],
    input
  ).stdout.trim();
}
const snapshotProgram = `import os,hashlib,json
v={}
for root,ds,fs in os.walk('/data'):
 for f in fs:
  p=os.path.join(root,f);s=os.stat(p);v[p]=[s.st_uid,s.st_gid,s.st_mode,hashlib.sha256(open(p,'rb').read()).hexdigest()]
print(json.dumps(v,sort_keys=True))`;
const snapshot = (volume) => python(volume, snapshotProgram, undefined, true);
const helperVolumes = [];
let taskNetwork;

try {
  dc('up', '-d', '--no-build', '--pull', 'never');
  const ingress = dc('ps', '-q', 'cpamp-ingress');
  const runtime = dc('ps', '-q', 'cpamp-runtime');
  const binding = JSON.parse(docker('inspect', ingress))[0].NetworkSettings.Ports['18317/tcp'][0];
  assert.equal(binding.HostIp, '127.0.0.1');
  const origin = `http://127.0.0.1:${binding.HostPort}`;
  report.origin = origin;
  const managerVolume = JSON.parse(
    docker('inspect', dc('ps', '-q', 'cpamp-manager'))
  )[0].Mounts.find((m) => m.Destination === '/data').Name;
  const network = Object.keys(
    JSON.parse(docker('inspect', ingress))[0].NetworkSettings.Networks
  )[0];
  taskNetwork = network;
  await wait(async () => (await fetch(origin + bootstrap)).ok, 'public Discovery');
  assert.equal((await fetch(origin + bootstrap + '/ui')).status, 200);
  for (const path of ['claim/exchange', 'admin/generate', 'admin/commit']) {
    assert.equal(
      (
        await fetch(origin + api + path, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: '{}',
        })
      ).status,
      404,
      'read-only discovery must not expose ' + path
    );
  }
  assert.equal(
    dc(
      'exec',
      '-T',
      'cpamp-manager',
      'sh',
      '-ec',
      'test ! -e /data/usage.sqlite; test ! -e /data/data.key; echo pristine'
    ),
    'pristine'
  );
  pass('first boot waits for Host initialize without creating database or data key');
  assert.equal(
    (await fetch(origin + '/health')).status,
    503,
    'unclaimed Manager must report /health 503'
  );
  pass('unclaimed Manager /health is 503 before Host initialize');
  dc('stop', 'cpamp-manager');
  const issue = () => dc('run', '--rm', '--no-deps', 'cpamp-manager', 'initialize');
  let handoff = issue();
  const decode = (link) =>
    JSON.parse(Buffer.from(link.split('#handoff=')[1], 'base64url').toString());
  const fragment = (value) =>
    bootstrap + '/ui#handoff=' + Buffer.from(JSON.stringify(value)).toString('base64url');
  const expired = decode(handoff);
  secretValues.push(expired.token);
  python(
    managerVolume,
    `import sqlite3,json
c=sqlite3.connect('/data/usage.sqlite');v=json.loads(c.execute("select value from settings where key='fresh_authority_v1'").fetchone()[0]);v['claimExpiresAtMs']=1;c.execute("update settings set value=? where key='fresh_authority_v1'",(json.dumps(v),));c.commit()`
  );
  dc('up', '-d', '--no-build', 'cpamp-manager');
  await wait(async () => (await fetch(origin + bootstrap + '/ui')).ok, 'Bootstrap UI');
  browser = await chromium.launch({
    headless: true,
    ...(process.env.PLAYWRIGHT_CHANNEL ? { channel: process.env.PLAYWRIGHT_CHANNEL } : {}),
  });
  const context = await browser.newContext({
    locale: 'zh-CN',
    viewport: { width: 1440, height: 1000 },
  });
  page = await context.newPage();
  report.strategyGenerations = [];
  const setupAPI = new URL(api, origin).pathname;
  const strategyPaths = ['/console/api/v2/strategy', setupAPI + 'strategy'];
  const completionPaths = ['/console/api/v2/complete', setupAPI + 'complete'];
  page.on('request', (request) => {
    if (strategyPaths.includes(new URL(request.url()).pathname) && request.method() === 'POST') {
      report.strategyGenerations.push(
        request.postData()?.match(/"expectedRuntimeGeneration":([0-9]+)/)?.[1] || 'INVALID'
      );
    }
  });
  page.on('response', async (response) => {
    const path = new URL(response.url()).pathname;
    if (strategyPaths.includes(path) || completionPaths.includes(path)) {
      report.checks.push({
        name: response.request().method() + ' ' + path,
        status: response.status(),
      });
      if (response.status() === 409) {
        try {
          report.strategyBlocker = (await response.json()).Code;
        } catch {}
      }
    }
  });
  page.on('pageerror', () => report.pageErrors.push('Uncaught browser error'));
  await page.goto(origin + handoff);
  await page.getByTestId('bootstrap-authority-error').waitFor();
  assert((await page.getByTestId('init-error-title').innerText()).includes('401'));
  pass('expired Handoff is rejected with Host recovery guidance (offline expiry fixture)');
  dc('stop', 'cpamp-manager');
  handoff = issue();
  const claim = decode(handoff);
  secretValues.push(claim.token);
  dc('up', '-d', '--no-build', 'cpamp-manager');
  await wait(async () => (await fetch(origin + bootstrap + '/ui')).ok, 'reissued Bootstrap UI');
  await page.goto(
    origin +
      fragment({ ...claim, identity: { ...claim.identity, installationId: 'wrong-installation' } })
  );
  await page.getByTestId('bootstrap-authority-error').waitFor();
  pass('wrong Handoff installation identity is rejected without consuming the valid claim');
  await page.goto(origin + bootstrap + '/ui');
  await page.getByTestId('bootstrap-discovery-guidance').waitFor();
  await page.goto(origin + handoff);
  await page.getByTestId('fresh-setup-wizard').waitFor();
  assert.equal(new URL(page.url()).hash, '');
  await screenshot('bootstrap-administrator-desktop-light');
  const replayPage = await context.newPage();
  await replayPage.goto(origin + handoff);
  await replayPage.getByTestId('bootstrap-authority-error').waitFor();
  await replayPage.close();
  pass('real browser claims once, removes fragment and rejects consumed Handoff');
  const takeoverHealth = await fetch(origin + '/health');
  assert.equal(takeoverHealth.status, 200, 'claimed Fresh takeover must report /health 200');
  assert(
    (await takeoverHealth.text()).includes('"service":"cpa-manager-plus"'),
    'takeover /health must carry the Manager service identity'
  );
  pass('Fresh takeover /health is 200 with the Manager service identity');
  // Two distinct Gateway states, both hard requirements:
  // - `pending`: the Fresh install is not admitted yet, so Ingress answers every
  //   path and credential with the Pending placeholder;
  // - `keyed`: admission is granted and CPA owns client authentication, so every
  //   path refuses an anonymous or unknown credential with 401 instead. F5
  //   removed the keyless window, so 503 on the keyed path is now a regression.
  const gatewayPaths = [
    '/v1/models',
    '/v1/chat/completions',
    '/v1/responses',
    '/v1/messages',
    '/v1beta/models',
  ];
  const closed = async (state = 'keyed') => {
    for (const path of gatewayPaths) {
      for (const key of ['', 'incorrect-client-key']) {
        const response = await fetch(origin + path, {
          method: path.includes('models') ? 'GET' : 'POST',
          headers: key ? { Authorization: 'Bearer ' + key } : {},
        });
        const body = (await response.text()).trim();
        if (state === 'pending') {
          assert.equal(response.status, 503, 'an unadmitted Gateway must answer 503: ' + path);
          assert.equal(body, 'gateway pending configuration');
        } else {
          assert.notEqual(
            body,
            'gateway pending configuration',
            'the Gateway must not still be Pending after completion: ' + path
          );
          assert.equal(
            response.status,
            401,
            'an admitted key-protected Gateway must refuse ' +
              (key || 'an anonymous credential') +
              ': ' +
              path +
              ' -> ' +
              response.status +
              ' ' +
              body
          );
        }
      }
    }
  };
  // Genuine pre-completion negative control: the Fresh install is claimed but no
  // destination has been described yet, so no Gateway path may forward at all.
  await closed('pending');
  pass(
    'before any installation submission: all tested Gateway paths remain Pending on every credential'
  );
  const wizardIdle = (label) =>
    wait(
      async () =>
        (await page.getByTestId('fresh-setup-wizard').getAttribute('aria-busy')) === 'false',
      'setup idle: ' + label,
      180000
    );
  // The wizard owns the whole Fresh setup before Console login: administrator
  // key, one installation submission and the public route. A submitted write is
  // never replayed; an unconfirmed state stays locked until the canonical
  // operation is read again.
  await page.getByTestId('choose-new_managed').click();
  await page.getByTestId('setup-next').click();
  await page.getByTestId('admin-key-input').fill('weak');
  await page.getByTestId('setup-review').click();
  await page.getByRole('alert').waitFor();
  assert((await page.getByRole('alert').innerText()).includes('请检查管理员密钥和管理入口路径'));
  await page.getByTestId('generate-key-btn').click();
  await wait(
    async () => (await page.getByTestId('admin-key-input').inputValue()).length >= 24,
    'generated Admin Key'
  );
  const admin = await page.getByTestId('admin-key-input').inputValue();
  secretValues.push(admin);
  assert.equal(await page.getByTestId('route-target-input').inputValue(), '/console');
  await page.getByTestId('setup-review').click();
  await page.getByTestId('confirm-key-saved-checkbox').check();
  let commits = 0;
  let activations = 0;
  let strategyWrites = 0;
  await page.route('**' + api + 'admin/commit', async (route) => {
    commits++;
    const response = await route.fetch();
    assert.equal(response.status(), 200);
    await route.abort('failed'); // Real server committed; response lost to browser.
  });
  // Capture the reply that commits the destination: it is the single delivery of
  // the installer Client Key. A committed replay must never repeat it.
  const committingReplies = [];
  await page.route('**' + api + 'strategy', async (route) => {
    strategyWrites++;
    const response = await route.fetch();
    committingReplies.push({
      status: response.status(),
      body: await response.text(),
      cacheControl: response.headers()['cache-control'] || '',
    });
    await route.fulfill({ response });
  });
  // The pre-submission read must succeed; the recovery read that follows the
  // lost commit reply is the one that is also lost.
  let lostRecoveryRead = false;
  await page.route('**' + api + 'operation', async (route) => {
    if (commits > 0 && !lostRecoveryRead) {
      lostRecoveryRead = true;
      await route.abort('failed');
    } else await route.continue();
  });
  await page.route('**' + api + 'routing/activate', async (route) => {
    activations++;
    if (activations === 1) await route.fulfill({ status: 503, body: '' });
    else await route.continue();
  });
  await page.getByTestId('setup-execute').click();
  await wait(
    async () =>
      (await page.getByTestId('setup-refresh').count()) === 1 &&
      (await page.getByTestId('fresh-setup-wizard').getAttribute('aria-busy')) === 'false',
    'unconfirmed original installation'
  );
  assert(await page.getByTestId('setup-resume').isDisabled());
  assert.equal(commits, 1);
  await page.unroute('**' + api + 'operation');
  await page.getByTestId('setup-refresh').click();
  await wizardIdle('administrator recovery');
  await wait(
    async () => !(await page.getByTestId('setup-resume').isDisabled()),
    'recovered administrator commit'
  );
  assert.equal(commits, 1);
  await page.unroute('**' + api + 'admin/commit');
  pass('weak Admin Key rejected; server-generated Key commits once despite lost response');
  // Resume until the original operation completes. Each click only reconciles
  // the persisted operation; it never selects another target or replays a write.
  for (let round = 0; round < 10 && (await page.getByTestId('enter-console').count()) === 0; round++) {
    const resume = page.getByTestId('setup-resume');
    if ((await resume.count()) > 0 && !(await resume.isDisabled())) await resume.click();
    else {
      const refresh = page.getByTestId('setup-refresh');
      if ((await refresh.count()) > 0 && !(await refresh.isDisabled())) await refresh.click();
    }
    await wizardIdle('setup resume ' + round);
  }
  await page.getByTestId('enter-console').waitFor({ timeout: 60000 });
  await page.unroute('**' + api + 'routing/activate');
  await page.unroute('**' + api + 'strategy');
  assert.equal(commits, 1);
  assert.equal(activations, 2, 'one controlled activation failure, then one real activation');
  assert(strategyWrites >= 1, 'the installation submitted at least one strategy write');
  report.strategyWrites = strategyWrites;
  report.activations = activations;
  pass(
    'one installation submission committed official CPA and the exact public route; controlled activation failure and lost replies recovered without replay'
  );
  await installationScreenshots('complete');
  await page.getByTestId('enter-console').click();
  await page.getByTestId('console-login').waitFor();
  assert.equal(await page.locator('.console-login-panel').count(), 0);
  await page.getByRole('checkbox', { name: '记住凭证', exact: true }).waitFor();
  for (const [name, width, light] of [
    ['desktop-light', 1440, true],
    ['desktop-dark', 1440, false],
    ['mobile-dark', 390, false],
    ['mobile-light', 390, true],
  ]) {
    await page.setViewportSize({ width, height: 1000 });
    for (let attempt = 0; attempt < 3; attempt++) {
      const dark = await page.locator('html').getAttribute('data-theme') === 'dark';
      if (dark === !light) break;
      await page.getByRole('button', { name: '主题', exact: true }).click();
    }
    assert.equal(await page.locator('html').getAttribute('data-theme') === 'dark', !light);
    assert.equal(await page.getByRole('button', { name: '主题', exact: true }).count(), 1);
    await screenshot('original-login-' + name, false);
  }
  await page.setViewportSize({ width: 1440, height: 1000 });
  pass('original CPAMP login page, language/theme tools and remember-credential control render on desktop/mobile light/dark');
  const login = async (key) => {
    await page.locator('#console-admin-key').fill(key);
    await page.getByTestId('console-login').locator('button[type=submit]').click();
  };
  assert(await page.getByTestId('console-login').locator('button[type=submit]').isDisabled());
  await login('incorrect-admin-key');
  await page.getByRole('alert').waitFor();
  assert(await page.getByTestId('console-login').isVisible());
  await login(admin);
  const legacyConsole = page.getByTestId('legacy-console');
  await legacyConsole.waitFor();
  await legacyConsole.locator('.app-shell').waitFor();
  pass('Console rejects missing/wrong Admin Key and accepts the installed Admin Key');
  const adminGet = async (path) => {
    const response = await fetch(origin + '/console/api/v2/' + path, {
      headers: { Authorization: 'Bearer ' + admin },
    });
    assert.equal(response.status, 200);
    return response.json();
  };
  for (const key of ['', 'incorrect-admin-key']) {
    const denied = await fetch(origin + '/console/api/v2/installation/runtime', {
      headers: key ? { Authorization: 'Bearer ' + key } : {},
    });
    assert.equal(denied.status, 401);
  }
  pass('deployment Runtime observation rejects missing/wrong administrator credentials');
  const operation = await adminGet('operation');
  const operationId = operation.identity.operationId;
  report.operationId = operationId;
  assert.equal(operation.productComplete, true);
  assert.equal(operation.strategy.stage, 'committed');
  assert.equal(operation.routing.committed, true);
  assert.equal(operation.routing.target, '/console');
  const runtimeToken = dc('exec', '-T', 'cpamp-runtime', 'cat', '/run/cpamp/runtime-secret/token');
  secretValues.push(runtimeToken);
  const statusProgram = `import urllib.request,json,sys
r=urllib.request.Request('http://cpamp-runtime:9081/v1/runtime/status',headers={'Authorization':'Bearer '+sys.stdin.read().strip(),'X-CPAMP-Runtime-Features':'active-gateway-artifact-v1'})
with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(r) as v:print(v.read().decode())`;
  const statusJSON = run(
    [
      'docker',
      'run',
      '--rm',
      '-i',
      '--network',
      network,
      pythonImage,
      'python3',
      '-c',
      statusProgram,
    ],
    runtimeToken
  ).stdout;
  const status = JSON.parse(statusJSON);
  status.runtimeGeneration = statusJSON.match(/"runtimeGeneration"\s*:\s*([0-9]+)/)[1];
  report.runtimeGeneration = status.runtimeGeneration;
  assert.equal(status.activeGatewayArtifact.artifactId, 'sha256:' + pin);
  assert.equal(status.activeGatewayArtifact.version, '8.0.55');
  assert.equal(
    dc('exec', '-T', 'cpamp-runtime', 'sha256sum', '/usr/local/bin/cli-proxy-api').split(/\s/)[0],
    pin
  );
  assert.equal(status.state, 'ready');
  assert(
    report.strategyGenerations.every((value) => value === status.runtimeGeneration),
    'every strategy write used the observed Runtime generation'
  );
  const liveHash = dc(
    'exec',
    '-T',
    '--user',
    '10001:10001',
    'cpamp-runtime',
    'sh',
    '-ec',
    `for p in /proc/[0-9]*; do
    [ -r "$p/comm" ] || continue
    [ "$(cat "$p/comm")" = cli-proxy-api ] || continue
    sha256sum "$p/exe"; exit 0
  done; exit 1`
  );
  assert.equal(liveHash.split(/\s/)[0], pin);
  assert.equal(
    dc('exec', '-T', 'cpamp-runtime', 'sha256sum', '/runtime/gateway/config.yaml').split(/\s/)[0],
    createHash('sha256')
      .update(readFileSync(resolve(root, 'docker/runtime/config.v8.default.seed.yaml')))
      .digest('hex')
  );
  pass(
    'one user installation started the pinned official CPA; running executable and saved seed match'
  );
  assert.equal(new URL(page.url()).pathname, '/console');
  const bridgeInfo = await fetch(origin + '/console/api/v2/cpamp/usage-service/info', {
    headers: { Authorization: 'Bearer ' + admin },
  });
  assert.equal(bridgeInfo.status, 200);
  const configBefore = await fetch(origin + '/console/api/v2/cpamp/config', {
    headers: { Authorization: 'Bearer ' + admin },
  });
  assert.equal(configBefore.status, 200);
  const configPayload = await configBefore.json();
  assert(configPayload.config && typeof configPayload.config === 'object');
  const configToSave = {
    ...configPayload.config,
    // A fresh Managed install intentionally has no CPA Management Key yet.
    // Keep the existing manager-config invariant truthful in this disposable
    // probe: an empty connection may be saved only with collection disabled.
    collector: {
      ...configPayload.config.collector,
      ...(configPayload.config.cpaConnection?.cpaBaseUrl
        ? {}
        : { enabled: false }),
    },
  };
  const configSave = await fetch(origin + '/console/api/v2/cpamp/config', {
    method: 'PUT',
    headers: {
      Authorization: 'Bearer ' + admin,
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({ config: configToSave }),
  });
  assert.equal(configSave.status, 200);
  const configAfter = await fetch(origin + '/console/api/v2/cpamp/config', {
    headers: { Authorization: 'Bearer ' + admin },
  });
  assert.equal(configAfter.status, 200);
  assert.equal((await adminGet('operation')).productComplete, true);
  pass(
    'Fresh New completion enters the existing CPAMP MainLayout; authenticated Config GET/PUT remains readable'
  );
  const before = await adminGet('operation');
  assert.equal(before.productComplete, true);
  assert.equal(before.gatewayPending, true);
  const normalizeConsolePath = (path) => {
    const pathname = new URL(page.url()).pathname.replace(/\/$/, '');
    return pathname || '/';
  };
  const navigate = async (path) => {
    if (page.viewportSize().width < 768 && (await page.locator('aside.sidebar.open').count()) === 0)
      await page.locator('button.hamburger-container').click();
    const href = '/console' + (path || '/');
    const link =
      path === ''
        ? page.locator('aside.sidebar a[href="/console"], aside.sidebar a[href="/console/"]')
        : page.locator(`aside.sidebar a[href="${href}"]`);
    if (!(await link.isVisible())) {
      const section = path.startsWith('/ai-providers') || ['/accounts', '/oauth'].includes(path)
        ? 'resources'
        : path === '/api-keys' ? 'clients'
          : ['/monitoring', '/usage-analytics', '/model-prices'].includes(path) ? 'observe'
            : 'system';
      await page.locator(`aside.sidebar button[aria-controls="nav-${section}"]`).click();
    }
    await link.waitFor();
    await link.click();
    await wait(
      async () => normalizeConsolePath() === href.replace(/\/$/, ''),
      'navigation ' + path
    );
    const currentLink = page.locator('a[aria-current="page"]');
    await wait(
      async () =>
        (await currentLink.count()) === 1 &&
        (await currentLink.getAttribute('href'))?.replace(/\/$/, '') === href.replace(/\/$/, ''),
      'active navigation ' + path
    );
    assert.equal(
      (await currentLink.getAttribute('href'))?.replace(/\/$/, ''),
      href.replace(/\/$/, '')
    );
    assert((await page.locator('.app-breadcrumb').innerText()).length > 0);
  };
  for (const [name, width, light] of [
    ['desktop-dark', 1440, false],
    ['desktop-light', 1440, true],
    ['mobile-light', 390, true],
    ['mobile-dark', 390, false],
  ]) {
    await page.setViewportSize({ width, height: 1000 });
    const isLight = await page
      .locator('html')
      .evaluate((html) => html.getAttribute('data-theme') !== 'dark');
    if (isLight !== light) {
      await page.getByRole('button', { name: '主题', exact: true }).click();
      await page
        .getByRole('menuitemradio', { name: light ? '明亮' : '黑夜', exact: true })
        .click();
    }
    await navigate('');
    await screenshot('legacy-home-' + name);
    await navigate('/config');
    await page.locator('[role="tablist"]').first().waitFor();
    await screenshot('legacy-config-' + name);
    const gatewayPanel = page.getByTestId('gateway-config-workspace');
    if (await gatewayPanel.isVisible()) {
      await gatewayPanel.scrollIntoViewIfNeeded();
      await wait(async () => page.evaluate(() => {
        const gateway = document.querySelector('[data-testid="gateway-config-workspace"]').getBoundingClientRect();
        const toggle = document.querySelector('.hamburger-container');
        const box = toggle.getBoundingClientRect();
        if (!toggle.contains(document.elementFromPoint(box.x + box.width / 2, box.y + box.height / 2))) return false;
        return [...document.querySelectorAll('[class*="floatingSidebarContainer"]')].every((rail) => {
          if (getComputedStyle(rail).opacity === '0') return true;
          return rail.getBoundingClientRect().bottom <= gateway.top + 1;
        });
      }), 'configuration navigation stays inside its original workspace');
      await screenshot('integrated-gateway-config-' + name);
    } else if (!report.gatewayPanelDeferred) {
      // The gateway configuration workspace renders only when the Manager can
      // read the CPA configuration. A deployment whose authority is unresolved
      // records the layout check as NOT RUN instead of passing on an absent page.
      report.gatewayPanelDeferred = true;
      notRun(
        'gateway configuration workspace layout (hidden: the Manager cannot read the CPA configuration)'
      );
    }
    if (width < 768) {
      await page.locator('button.hamburger-container').click();
      await page.locator('aside.sidebar.open').waitFor();
      await screenshot('legacy-navigation-' + name);
      await page
        .locator('.sidebar-backdrop.visible')
        .click({ position: { x: page.viewportSize().width - 20, y: 200 } });
    } else {
      await page.locator('button.hamburger-container').click();
      await page.locator('aside.sidebar.collapsed').waitFor();
      await screenshot('legacy-navigation-collapsed-' + name);
      await page.locator('button.hamburger-container').click();
    }
    await navigate('/system');
    await page.locator('.main-content').waitFor();
    // The Fresh system workspace is the primary content; the original system
    // cards live in a collapsed disclosure below it.
    const additional = page.locator('details.system-additional-info');
    if ((await additional.count()) > 0 && !(await additional.first().getAttribute('open'))) {
      await additional.first().locator('summary').click();
    }
    await page.getByRole('button', { name: '清理登录信息', exact: true }).click();
    await page.getByRole('dialog').waitFor();
    await page.getByRole('dialog').getByRole('button', { name: '取消', exact: true }).click();
    await page.getByRole('dialog').waitFor({ state: 'detached' });
    assert(await legacyConsole.isVisible());
    await screenshot('legacy-system-' + name);
  }
  await page.goBack();
  assert.equal(normalizeConsolePath(), '/console/config');
  await page.goForward();
  assert.equal(normalizeConsolePath(), '/console/system');
  pass(
    'existing CPAMP two-level navigation, breadcrumbs, history, desktop collapse and mobile drawer work in light/dark themes'
  );
  await page.getByRole('button', { name: '语言', exact: true }).click();
  await page.getByRole('menuitemradio', { name: 'English', exact: true }).click();
  await page.getByRole('button', { name: 'Language', exact: true }).click();
  await page.getByRole('menuitemradio', { name: '简体中文', exact: true }).click();
  await page.getByRole('button', { name: '语言', exact: true }).waitFor();
  pass(
    'language selection updates the existing Console and restores Chinese'
  );
  await navigate('/system/updates');
  const updateSettings = page.locator('details').filter({ has: page.locator('#manager-update-channel') });
  await updateSettings.locator('summary').click();
  const updateChannel = page.locator('#manager-update-channel');
  await updateChannel.waitFor();
  await updateChannel.click();
  await page.getByRole('option', { name: '测试版 Beta', exact: true }).click();
  await wait(async () => (await adminGet('cpamp/usage-service/updates')).channel_preference === 'beta', 'original update page saves channel');
  await screenshot('original-updates-working');
  pass('original global confirmation can be canceled without logout; original update provider loads and persists channel');
  const storedSecrets = await page.evaluate((adminKey) =>
    [...Object.values(localStorage), ...Object.values(sessionStorage)].some((value) =>
      value.includes(adminKey)
    ), admin);
  assert.equal(storedSecrets, false);
  // Completion admitted the destination and it was staged with its installer
  // Client Key, so every Gateway path now refuses an anonymous or unknown
  // credential instead of answering the Pending placeholder.
  await closed('keyed');
  pass(
    'after completion: all tested Gateway paths refuse an anonymous or unknown credential with 401 instead of staying Pending'
  );
  // The committed default Managed destination binds CPA to the Compose network,
  // so an anonymous peer is expected to reach the model API and be rejected with
  // 401. 'unreachable' only means the peer could not open the connection at all;
  // either verdict is safe, a successful unauthenticated read is not.
  const peer = run(
    [
      'docker',
      'run',
      '--rm',
      '--network',
      network,
      pythonImage,
      'python3',
      '-c',
      `import socket,urllib.request,urllib.error
s=socket.socket();s.settimeout(3)
try:
 s.connect(('cpamp-runtime',8317));s.close()
except OSError:
 print('unreachable');raise SystemExit(0)
o=urllib.request.build_opener(urllib.request.ProxyHandler({}))
try:
 with o.open(urllib.request.Request('http://cpamp-runtime:8317/v1/models'),timeout=10) as r:print(r.status)
except urllib.error.HTTPError as e:print(e.code)
except OSError:print('unreachable')`,
    ]
  );
  assert.equal(peer.status, 0);
  const peerVerdict = peer.stdout.trim();
  assert(
    ['unreachable', '401'].includes(peerVerdict),
    'anonymous peer must not read the CPA model API: ' + peerVerdict
  );
  report.peerCPA = peerVerdict;
  pass(
    'the anonymous Compose peer cannot read the key-protected CPA model API'
  );
  // Data layer: the installed deployment must read real CPA management data,
  // install one Client Key and have CPA authenticate it. The committed default
  // New Managed destination is the installation-derived CPA management
  // authority, so every step below is a hard requirement: any other status is a
  // failure, never an absorbed known gap.
  report.dataLayer = [];
  const recordDataLayer = (name, result, observed) => {
    report.dataLayer.push({ name, result, ...(observed ? { observed } : {}) });
    console.log(result + ' ' + name + (observed ? ' ' + JSON.stringify(observed) : ''));
  };
  const adminHeaders = { Authorization: 'Bearer ' + admin };
  const authFilesResponse = await fetch(
    origin + '/console/api/v2/cpamp/v0/management/auth-files',
    { headers: adminHeaders }
  );
  const authFilesBody = await authFilesResponse.text();
  assert.equal(
    authFilesResponse.status,
    200,
    'the cpamp/ bridge must read CPA /auth-files through the committed managed authority: ' +
      authFilesResponse.status +
      ' ' +
      authFilesBody.trim()
  );
  const authFiles = JSON.parse(authFilesBody);
  assert(Array.isArray(authFiles.files), 'CPA auth files payload must be a list');
  recordDataLayer('cpamp resource bridge reads CPA /auth-files', 'PASS', {
    files: authFiles.files.length,
  });
  // The installation minted exactly one Client Key and staged the destination
  // with it, so CPA starts with client authentication on. The list proves that
  // canonical record exists; the on-demand secret read is the only way to get
  // the plaintext back.
  const listResponse = await fetch(origin + '/console/api/v2/api-keys', {
    headers: adminHeaders,
  });
  assert.equal(listResponse.status, 200, 'the Client Key list must be readable');
  const listed = await listResponse.json();
  assert(Array.isArray(listed.keys), 'Client Key list payload must be a list of keys');
  const installKeyRecord = listed.keys.find((key) => key.origin === 'installer');
  assert(installKeyRecord, 'the installation must own exactly one installer Client Key');
  assert.equal(
    listed.keys.filter((key) => key.origin === 'installer').length,
    1,
    'the installation must own exactly one installer Client Key'
  );
  assert.match(installKeyRecord.maskedPreview, /^sk-…[A-Za-z0-9]{4}$/);
  assert(!('createdSecret' in installKeyRecord), 'the list must never carry a plaintext key');
  const secretResponse = await fetch(
    origin + '/console/api/v2/api-keys/' + installKeyRecord.id + '/secret',
    {
      method: 'POST',
      headers: { ...adminHeaders, 'Content-Type': 'application/json' },
      body: '{}',
    }
  );
  const secretBody = await secretResponse.text();
  assert.equal(
    secretResponse.status,
    200,
    'the on-demand Client Key read must return the install secret: ' +
      secretResponse.status +
      ' ' +
      secretBody.trim()
  );
  assert.equal(secretResponse.headers.get('cache-control'), 'no-store');
  const installKey = JSON.parse(secretBody).secret;
  assert.match(installKey, /^sk-[A-Za-z0-9]{64}$/);
  assert.equal(installKeyRecord.maskedPreview, 'sk-…' + installKey.slice(-4));
  secretValues.push(installKey);
  // The committing strategy reply is the single browser delivery of this key.
  // It must carry it with the canonical shape, be uncacheable, and no replay of
  // the committed operation may repeat it.
  const committing = committingReplies.filter((reply) => reply.status === 200);
  assert(
    committing.length >= 1,
    'the installation must have one successful committing strategy reply'
  );
  const parseReply = (reply) => {
    try {
      return JSON.parse(reply.body);
    } catch {
      return {};
    }
  };
  const committingKeys = committing.map((reply) => parseReply(reply).initialClientKey).filter(Boolean);
  assert.equal(
    committingKeys.length,
    1,
    'exactly one strategy reply may carry the installer Client Key, saw ' + committingKeys.length
  );
  assert.match(committingKeys[0], /^sk-[A-Za-z0-9]{64}$/);
  assert.equal(
    committingKeys[0],
    installKey,
    'the committing reply and the on-demand read must return the same key'
  );
  assert(
    committing.some(
      (reply) => reply.body.includes(committingKeys[0]) && reply.cacheControl === 'no-store'
    ),
    'the reply carrying the installer Client Key must be Cache-Control: no-store'
  );
  // The committed operation replays in place: same identity, same revision and
  // the Runtime facts its own checkpoint recorded. The fixed Bootstrap carrier is
  // retired once routing commits, so the replay is issued on the canonical
  // Console API with the Admin credential.
  const committed = parseReply(committing.find((reply) => parseReply(reply).initialClientKey));
  assert.equal(committed.strategy.stage, 'committed', 'the committing reply must be committed');
  // Completion advances the revision, so the replay uses the operation's current
  // revision and the Runtime facts its own committed checkpoint recorded. The
  // 64-bit generation is read as text: it exceeds Number.MAX_SAFE_INTEGER, and a
  // round-trip through a JavaScript number would corrupt it.
  const currentRaw = await (
    await fetch(origin + '/console/api/v2/operation', { headers: adminHeaders })
  ).text();
  const current = JSON.parse(currentRaw);
  const targetGeneration = currentRaw.match(/"targetGeneration"\s*:\s*([0-9]+)/)[1];
  assert.equal(current.strategy.stage, 'committed');
  const replayed = await fetch(origin + '/console/api/v2/strategy', {
    method: 'POST',
    headers: { ...adminHeaders, 'Content-Type': 'application/json' },
    body: JSON.stringify({
      identity: current.identity,
      expectedRevision: current.revision,
      strategy: 'new_managed',
      expectedRuntimeIdentity: current.strategy.targetIdentity,
      // Splice the exact digits so the 64-bit value is never re-encoded.
      expectedRuntimeGeneration: '#GENERATION#',
      expectedArtifactId: current.strategy.targetArtifactId,
    }).replace('"#GENERATION#"', targetGeneration),
  });
  const replayedBody = await replayed.text();
  assert.equal(
    replayed.status,
    200,
    'a committed strategy replay must return the checkpoint: ' +
      replayed.status +
      ' ' +
      replayedBody.trim()
  );
  assert.equal(JSON.parse(replayedBody).strategy.stage, 'committed');
  assert(
    !replayedBody.includes(installKey) && !replayedBody.includes('initialClientKey'),
    'a committed strategy replay must not repeat the installer Client Key: ' + replayedBody.trim()
  );
  recordDataLayer('committing strategy reply carries the installer Client Key once', 'PASS', {
    committingReplies: committing.length,
    cacheControl: 'no-store',
    replayRepeated: false,
  });
  // The install route observes that already-installed key and writes nothing.
  const installKeyResponse = await fetch(
    origin + '/console/api/v2/api-keys/' + installKeyRecord.id + '/initial-install',
    {
      method: 'POST',
      headers: { ...adminHeaders, 'Content-Type': 'application/json' },
      body: '{}',
    }
  );
  const installKeyBody = await installKeyResponse.text();
  assert.equal(
    installKeyResponse.status,
    200,
    'Client Key initial install must observe the managed CPA: ' +
      installKeyResponse.status +
      ' ' +
      installKeyBody.trim()
  );
  const installedState = JSON.parse(installKeyBody);
  assert.equal(installedState.managerStaged, true, 'installed Client Key stays Manager-staged');
  assert.equal(
    installedState.attempted,
    true,
    'installed Client Key records the durable attempt'
  );
  assert.equal(installedState.cpaConfigured, true, 'installed Client Key is configured in CPA');
  assert.equal(installedState.installAvailable, false, 'no second initial install remains');
  recordDataLayer('installation Client Key installed in CPA', 'PASS', {
    runtimeState: installedState.runtimeState,
  });
  // A key created later can never consume that single install journal, and CPA
  // already has a non-empty client set, so a second install is refused.
  const createdKeyResponse = await fetch(origin + '/console/api/v2/api-keys', {
    method: 'POST',
    headers: { ...adminHeaders, 'Content-Type': 'application/json' },
    body: JSON.stringify({ requestId: randomBytes(16).toString('hex') }),
  });
  assert.equal(
    createdKeyResponse.status,
    201,
    'a Fresh workspace Client Key must be created with a one-time secret'
  );
  const createdKey = await createdKeyResponse.json();
  assert.match(createdKey.createdSecret, /^cpamp_sk_[a-f0-9]{64}$/);
  assert.equal(createdKey.key.maskedPreview, 'cpamp_sk_…' + createdKey.createdSecret.slice(-4));
  assert.equal(createdKey.key.origin, 'workspace');
  secretValues.push(createdKey.createdSecret);
  // CPA acceptance is proven where this deployment publishes CPA: the Compose
  // network (CPAMP_SMOKE_MODEL_TARGET default) or the Ingress Gateway, which
  // admits an installed Client Key once Manager proves the default Managed
  // authority.
  const cpaProbe = (program, input) =>
    run(
      [
        'docker',
        'run',
        '--rm',
        '-i',
        '--network',
        network,
        pythonImage,
        'python3',
        '-c',
        program,
      ],
      input
    ).stdout.trim();
  const modelsProbe = `import sys,urllib.request,urllib.error
key=sys.stdin.read().strip()
target=${JSON.stringify(modelTarget)}
o=urllib.request.build_opener(urllib.request.ProxyHandler({}))
r=urllib.request.Request(target+'/v1/models',headers={'Authorization':'Bearer '+key} if key else {})
try:
 with o.open(r,timeout=20) as response:print(response.status)
except urllib.error.HTTPError as error:print(error.code)
except OSError:print('unreachable')`;
  const chatProbe = `import json,sys,urllib.request,urllib.error
key=sys.stdin.read().strip()
target=${JSON.stringify(modelTarget)}
body=json.dumps({'model':'gpt-4o-mini','messages':[{'role':'user','content':'cpamp authority smoke'}],'max_tokens':1}).encode()
o=urllib.request.build_opener(urllib.request.ProxyHandler({}))
r=urllib.request.Request(target+'/v1/chat/completions',data=body,headers={'Authorization':'Bearer '+key,'Content-Type':'application/json'},method='POST')
try:
 with o.open(r,timeout=30) as response:print(response.status)
except urllib.error.HTTPError as error:print(error.code)
except OSError:print('unreachable')`;
  const cpaChecks = [
    'CPA model API accepts the installed Client Key',
    'CPA rejects an unknown Client Key',
    // Proves Client Key authentication only: without a Provider fixture CPA may
    // answer 500/503, so this must never be reported as a real model success.
    'CPA authenticates the installed Client Key on a model request (non-401)',
  ];
  const accepted = cpaProbe(modelsProbe, installKey);
  assert.equal(accepted, '200', 'installed Client Key must read the CPA model API: ' + accepted);
  recordDataLayer(cpaChecks[0], 'PASS', { status: 200 });
  const rejected = cpaProbe(modelsProbe, 'cpamp_sk_' + '0'.repeat(64));
  assert.equal(rejected, '401', 'unknown Client Key must be rejected: ' + rejected);
  recordDataLayer(cpaChecks[1], 'PASS', { status: 401 });
  const modelStatus = cpaProbe(chatProbe, installKey);
  assert(
    /^[1-5][0-9]{2}$/.test(modelStatus) && modelStatus !== '401',
    'CPA must authenticate the installed Client Key on one model request: ' + modelStatus
  );
  recordDataLayer(cpaChecks[2], 'PASS', { status: Number(modelStatus) });
  // The Ingress Gateway admits the installation key once Manager proves the
  // default Managed authority. The pre-completion read stays the negative
  // control. Ingress re-reads Manager within 1s after a negative answer, so the
  // first request right after completion may still observe the Pending
  // placeholder. The destination never starts keyless, so CPA rejects the
  // anonymous call whether or not admission has already been granted.
  const admitted = async (label) => {
    let status = 0;
    await wait(async () => {
      const response = await fetch(origin + '/v1/chat/completions', {
        method: 'POST',
        headers: {
          Authorization: 'Bearer ' + installKey,
          'Content-Type': 'application/json',
        },
        body: '{}',
      });
      status = response.status;
      return (await response.text()).trim() !== 'gateway pending configuration';
    }, label);
    assert.notEqual(status, 401, 'Ingress must authenticate the installed Client Key');
    const anonymous = await fetch(origin + '/v1/chat/completions', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: '{}',
    });
    assert.equal(
      anonymous.status,
      401,
      'an admitted Gateway must still have CPA reject a missing Client Key'
    );
    return { status };
  };
  recordDataLayer(
    'Ingress Gateway admits the installed Client Key',
    'PASS',
    await admitted('Ingress admits the installed Client Key')
  );
  const keyDigest = dc('exec', '-T', 'cpamp-manager', 'sha256sum', '/data/data.key');
  dc('restart', 'cpamp-manager', 'cpamp-runtime', 'cpamp-ingress');
  await wait(
    async () => (await fetch(origin + '/console', { headers: { Accept: 'text/html' } })).ok,
    'restored Console'
  );
  await page.reload();
  await page.getByTestId('console-login').waitFor();
  await login(admin);
  await page.getByTestId('legacy-console').waitFor();
  await page.locator('.app-shell').waitFor();
  const after = await adminGet('operation');
  assert.deepEqual(after, before);
  assert.equal(dc('exec', '-T', 'cpamp-manager', 'sha256sum', '/data/data.key'), keyDigest);
  assert.equal((await fetch(origin + bootstrap + '/ui')).status, 404);
  // The restarted Runtime reports a new generation, so admission is withdrawn:
  // Manager's predicate pins the recorded identity, generation and artifact and
  // never rebinds on its own. The Gateway must therefore stay closed instead of
  // serving the installed Client Key from a stale admission.
  await closed('pending');
  // Runtime process generation changes across restart. Observe it but never
  // silently bind the original completed strategy to this new generation.
  const restartedStatusJSON = run(
    [
      'docker',
      'run',
      '--rm',
      '-i',
      '--network',
      network,
      pythonImage,
      'python3',
      '-c',
      statusProgram,
    ],
    runtimeToken
  ).stdout;
  const restartedStatus = JSON.parse(restartedStatusJSON);
  restartedStatus.runtimeGeneration = restartedStatusJSON.match(
    /"runtimeGeneration"\s*:\s*([0-9]+)/
  )[1];
  report.runtimeAfterRestart = {
    state: restartedStatus.state,
    generationChanged: restartedStatus.runtimeGeneration !== status.runtimeGeneration,
  };
  assert(report.runtimeAfterRestart.generationChanged);
  pass(
    'Manager/Runtime/Ingress restart preserves identity, completion, routing, data key and saved setting; Admin relogin works'
  );
  const mismatch = await fetch(origin + '/console/api/v2/strategy', {
    method: 'POST',
    headers: { Authorization: 'Bearer ' + admin, 'Content-Type': 'application/json' },
    body: JSON.stringify({
      identity: after.identity,
      expectedRevision: after.revision,
      strategy: 'new_managed',
      expectedRuntimeIdentity: status.runtimeIdentity,
      expectedRuntimeGeneration: status.runtimeGeneration,
      expectedArtifactId: status.activeGatewayArtifact.artifactId,
    }).replace(/("expectedRuntimeGeneration":)"([0-9]+)"/, '$1$2'),
  });
  assert.equal(mismatch.status, 409);
  assert.equal((await mismatch.json()).Code, 'managed_target_identity_mismatch');
  assert.deepEqual(await adminGet('operation'), before);
  pass(
    'stale Runtime generation returns 409 managed_target_identity_mismatch without rebinding or retry'
  );
  await page.getByRole('button', { name: '登出', exact: true }).click();
  await page.getByTestId('console-login').waitFor();
  await page.getByText('记住凭证', { exact: true }).click();
  assert(await page.getByRole('checkbox', { name: '记住凭证', exact: true }).isChecked());
  await login(admin);
  await page.getByTestId('legacy-console').waitFor();
  await navigate('/config');
  await page.reload();
  await page.getByTestId('legacy-console').waitFor();
  assert.equal(new URL(page.url()).pathname, '/console/config');
  assert.equal(await page.getByTestId('console-login').count(), 0);
  assert.deepEqual(await adminGet('operation'), before);
  await screenshot('remembered-console-after-refresh');
  pass('remembered login revalidates the real Admin Key after refresh and restores the original deep route');
  await page.getByRole('button', { name: '登出', exact: true }).click();
  await page.getByTestId('console-login').waitFor();
  await page.reload();
  await page.getByTestId('console-login').waitFor();
  assert.equal(await page.getByTestId('legacy-console').count(), 0);
  if (await page.getByRole('checkbox', { name: '记住凭证', exact: true }).isChecked())
    await page.getByText('记住凭证', { exact: true }).click();
  await login(admin);
  await page.getByTestId('legacy-console').waitFor();
  pass('explicit logout clears remembered authority; refresh stays on the original login page');
  const recoverProgram = `import urllib.request,json,sys,time
token=sys.stdin.read().strip(); headers={'Authorization':'Bearer '+token,'X-CPAMP-Runtime-Features':'active-gateway-artifact-v1'}
opener=urllib.request.build_opener(urllib.request.ProxyHandler({}))
with opener.open(urllib.request.Request('http://cpamp-runtime:9081/v1/runtime/status',headers=headers),timeout=10) as r:s=json.load(r)
assert s['state']=='offline'
body={'operationId':'host-recovery:'+str(s['runtimeGeneration']),'expectedRuntimeIdentity':s['runtimeIdentity'],'expectedRuntimeGeneration':s['runtimeGeneration'],'expectedActiveArtifactId':s['activeGatewayArtifact']['artifactId']}
headers['Content-Type']='application/json'
with opener.open(urllib.request.Request('http://cpamp-runtime:9081/v1/runtime/operations/start',data=json.dumps(body).encode(),headers=headers),timeout=30) as r:result=json.load(r)
assert result['state']=='succeeded'
for _ in range(120):
 with opener.open(urllib.request.Request('http://cpamp-runtime:9081/v1/runtime/status',headers=headers),timeout=10) as r:state=json.load(r)['state']
 if state=='ready':break
 time.sleep(0.25)
assert state=='ready'
print('ready')`;
  assert.equal(
    run(
      [
        'docker',
        'run',
        '--rm',
        '-i',
        '--network',
        network,
        pythonImage,
        'python3',
        '-c',
        recoverProgram,
      ],
      runtimeToken
    ).stdout.trim(),
    'ready'
  );
  assert.deepEqual(await adminGet('operation'), before);
  // The new generation is not the one admission pinned, so the Gateway is Pending
  // again until a completion binds this generation.
  await closed('pending');
  pass(
    'explicit Host lifecycle Start restores the new Runtime generation without rewriting installation provenance'
  );
  dc('stop', 'cpamp-manager');
  const persistentIdentityFiles = () => {
    const files = JSON.parse(snapshot(managerVolume));
    delete files['/data/usage.sqlite'];
    return files;
  };
  const settingsDigest = () =>
    python(
      managerVolume,
      `import sqlite3,hashlib,json,os
# Manager is stopped and the cold snapshot has no WAL. immutable avoids creating
# SQLite WAL bookkeeping on the read-only evidence mount.
assert not os.path.exists('/data/usage.sqlite-wal')
c=sqlite3.connect('file:/data/usage.sqlite?mode=ro&immutable=1',uri=True)
rows=c.execute('select key,hex(value) from settings order by key').fetchall()
print(hashlib.sha256(json.dumps(rows,ensure_ascii=False).encode()).hexdigest())`,
      undefined,
      true
    );
  const beforeRepeat = persistentIdentityFiles();
  const beforeSettings = settingsDigest();
  const repeat = run(
    [...compose, 'run', '--rm', '--no-deps', 'cpamp-manager', 'initialize'],
    undefined,
    false
  );
  assert.notEqual(repeat.status, 0);
  assert.deepEqual(persistentIdentityFiles(), beforeRepeat);
  assert.equal(settingsDigest(), beforeSettings);
  pass(
    'completed installation rejects repeated initialize; admin/authority/settings and identity/key files unchanged'
  );
  for (const kind of ['empty-existing-db', 'unknown-db', 'legacy-settings', 'incomplete-marker']) {
    const volume = project + '-' + kind;
    docker('volume', 'create', volume);
    helperVolumes.push(volume);
    python(
      volume,
      `import sqlite3,os
kind=${JSON.stringify(kind)}
if kind=='empty-existing-db':open('/data/usage.sqlite','wb').close()
elif kind=='incomplete-marker':open('/data/usage.sqlite.manager.lock','w').write('{broken-fresh-marker')
else:
 c=sqlite3.connect('/data/usage.sqlite')
 if kind=='unknown-db':c.execute('create table unknown_product(value text)');c.execute("insert into unknown_product values('preserve')")
 else:c.execute('create table settings(key text primary key,value text not null)');c.execute("insert into settings values('setup','{}')")
 c.commit();c.close()`
    );
    const old = snapshot(volume);
    const rejected = run(
      [
        'docker',
        'run',
        '--rm',
        '--network',
        'none',
        '-v',
        volume + ':/data',
        images['cpamp-manager'],
        'initialize',
      ],
      undefined,
      false
    );
    assert.notEqual(rejected.status, 0);
    assert.equal(snapshot(volume), old);
    pass(kind + ' cannot be admitted as Fresh; source bytes and modes unchanged');
  }
  const logs = dc('logs', '--no-color');
  assert(!secretValues.some((value) => logs.includes(value)), 'secrets appeared in service logs');
  assert.equal(report.pageErrors.length, 0);
  pass('no browser storage, uncaught browser errors or secrets in service logs');
  report.result = 'PASS';
} catch (err) {
  if (taskNetwork) {
    const probe = `import urllib.request,urllib.error,json
o=urllib.request.build_opener(urllib.request.ProxyHandler({}));v={}
for u in ['http://cpamp-manager:18317/internal/routing/fresh-authority','http://cpamp-ingress:9082/internal/routing/fresh','http://cpamp-manager:18317/console','http://cpamp-ingress:18317/console']:
 try:
  try:r=o.open(urllib.request.Request(u,headers={'Accept':'text/html' if u.endswith('/console') else 'application/json'}),timeout=3)
  except urllib.error.HTTPError as e:r=e
  v[u]=[r.status,r.read(512).decode(errors='replace')]
 except Exception as e:v[u]=type(e).__name__
print(json.dumps(v))`;
    try {
      report.failureProbe = JSON.parse(
        run([
          'docker',
          'run',
          '--rm',
          '--network',
          taskNetwork,
          pythonImage,
          'python3',
          '-c',
          probe,
        ]).stdout
      );
    } catch {}
  }
  try {
    report.failureServiceLogs = secretValues.reduce(
      (logs, value) => logs.replaceAll(value, '[REDACTED]'),
      dc('logs', '--no-color', '--tail', '60')
    );
  } catch {}
  if (page && !page.isClosed()) {
    report.failurePage = await page
      .locator('body')
      .innerText()
      .catch(() => 'Page unavailable');
    await page
      .screenshot({
        path: resolve(out, 'failure.png'),
        fullPage: true,
        mask: [page.locator('input, textarea')],
      })
      .catch(() => {});
  }
  report.failure = secretValues.reduce(
    (text, value) => text.replaceAll(value, '[REDACTED]'),
    String(err.stack || err)
  );
  process.exitCode = 1;
} finally {
  if (browser) await browser.close();
  const cleanup = run([...compose, 'down', '-v', '--remove-orphans'], undefined, false);
  report.cleanup = cleanup.status === 0 ? 'PASS' : 'FAIL';
  for (const volume of helperVolumes) run(['docker', 'volume', 'rm', volume], undefined, false);
  writeFileSync(resolve(out, 'report.json'), JSON.stringify(report, null, 2), { mode: 0o600 });
  console.log(report.result + ': ' + resolve(out, 'report.json'));
  if (report.failure) console.error(report.failure);
}
