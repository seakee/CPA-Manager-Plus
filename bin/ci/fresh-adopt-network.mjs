#!/usr/bin/env node
// Disposable D acceptance for network Adopt. No shared state, no pinned evidence:
// every container, network and volume this creates is removed.
//
// The journey is the operator's own. A real CPA v8.0.13 runs as its own container
// carrying credentials, provider sections and gateway settings; a second CPA is
// the Supervisor-owned destination this Manager stages; and the browser adopts
// the first into the second through the wizard, from the source form to the
// completion screen.
//
// The copy runs behind a TCP forwarder this script controls. That is what makes
// the fault deterministic: once the run has recorded its first item, the
// forwarder refuses every new connection, so the remaining items fail before they
// write anything, which is exactly the retryable state the completion refusal
// exists for. Removing the control file and pressing Resume continues from the
// durable prefix.
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { randomBytes, createHash } from 'node:crypto';
import { existsSync, mkdirSync, writeFileSync, readFileSync, rmSync } from 'node:fs';
import { createRequire } from 'node:module';
import { createServer } from 'node:net';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const [runtimeImage, managerImage, ingressImage, output] = process.argv.slice(2);
assert(
  runtimeImage && managerImage && ingressImage && output,
  'Usage: fresh-adopt-network.mjs RUNTIME_IMAGE MANAGER_IMAGE INGRESS_IMAGE OUTPUT'
);
const { chromium } = createRequire(import.meta.url)(process.env.PLAYWRIGHT_MODULE || 'playwright');
const out = resolve(output);
mkdirSync(out, { recursive: false, mode: 0o700 });
// The CPA config and the forwarder control live outside the artifact directory:
// the config carries every secret of this run, and an artifact directory is the
// one place a secret must never be.
const secretsDir = resolve(root, 'bin/tmp', 'd-adopt-secrets-' + randomBytes(6).toString('hex'));
mkdirSync(secretsDir, { recursive: true, mode: 0o700 });
const sourceConfigPath = resolve(secretsDir, 'source-config.yaml');
const sourceAuthDir = resolve(secretsDir, 'source-auth');
const emptyConfigPath = resolve(secretsDir, 'empty-config.yaml');
const emptyAuthDir = resolve(secretsDir, 'empty-auth');
const legacyConfigPath = resolve(secretsDir, 'legacy-config.yaml');
const legacyAuthDir = resolve(secretsDir, 'legacy-auth');
const forwarderPath = resolve(secretsDir, 'forwarder.py');
const controlDir = resolve(secretsDir, 'control');
for (const dir of [sourceAuthDir, emptyAuthDir, legacyAuthDir, controlDir])
  mkdirSync(dir, { recursive: true, mode: 0o700 });

// Everything this run prints is kept, unredacted, so the secret assertion below
// can look at it before redaction rewrites it.
const printed = [];
for (const level of ['log', 'error']) {
  const write = console[level].bind(console);
  console[level] = (...args) => {
    printed.push(args.map((value) => String(value)).join(' '));
    write(...args);
  };
}
const project = 'cpamp-d-adopt-' + randomBytes(6).toString('hex');
const bootstrap = '/.well-known/cpamp-bootstrap';
const api = bootstrap + '/api/v2/';
const logDir = resolve(root, 'bin/tmp/logs');
mkdirSync(logDir, { recursive: true });
const report = {
  result: 'FAIL',
  project,
  slice: 'Adopt Step 5',
  checks: [],
  screenshots: [],
  pageErrors: [],
  notRun: [
    'Real Provider model success (no Provider fixture or credential; the model request proves Client Key authentication only)',
    'Natural ten-minute claim expiry',
    'Adopt from a legacy-managed (CPAMP-owned) source: the product refuses every class except network_existing',
    'Non-English locale layouts and other browser engines',
  ],
};
// Every value that must never reach a log or the report. Every secret here is
// generated in the script, so the list is the complete set for this run.
const secretValues = [];
const managementKey = 'cpamp-d-adopt-' + randomBytes(24).toString('hex');
const emptySourceKey = 'cpamp-d-adopt-empty-' + randomBytes(24).toString('hex');
const seedKeys = [
  'sk-d-adopt-' + randomBytes(12).toString('hex'),
  'sk-d-adopt-' + randomBytes(12).toString('hex'),
];
const providerKeys = {
  gemini: 'gemini-d-adopt-' + randomBytes(10).toString('hex'),
  codex: 'codex-d-adopt-' + randomBytes(10).toString('hex'),
};
// Enough credentials that the copy is a real run rather than one round trip: the
// fault below is injected while the run is in flight, and a plan of a few items
// could settle before an observer sees it.
//
// The bytes are the form CPA itself writes an auth file in: the metadata map it
// holds, marshalled (sorted keys, `disabled` present). CPA's upload path re-marshals
// a credential whose bytes differ from that form (`sdk/auth/filestore.go:135-146` in
// v8.0.13), so bytes in any other key order come back changed and the executor's
// byte-equality read-back would report every one of them as unverified. A real
// source CPA holds canonical files; a fixture that does not is not the state the
// migration is specified against.
const credentialFiles = Object.fromEntries(
  Array.from({ length: 40 }, (_, index) => [
    'source-' + String(index).padStart(2, '0') + '.json',
    JSON.stringify({
      access_token: 'adopt-seed-' + randomBytes(12).toString('hex'),
      disabled: false,
      expired: '2099-01-01T00:00:00Z',
      type: index % 2 === 0 ? 'claude' : 'codex',
    }),
  ])
);
// A credential the source lists but the migration record cannot name: its item
// identity is past `freshstore.MaxAdoptionItemName()` (256), so the preflight
// records it as manual with an instruction and never downloads or uploads it
// (`internal/service/freshadopt/executor.go:455-475`). This is the contract's own
// manual path — "an item the migration does not carry" — and it is the one the
// completion screen's manual group must render.
const overlongName = 'overlong-' + 'a'.repeat(238) + '.json';
const overlongFile = {
  [overlongName]: JSON.stringify({
    access_token: 'adopt-seed-' + randomBytes(12).toString('hex'),
    disabled: false,
    expired: '2099-01-01T00:00:00Z',
    type: 'claude',
  }),
};
// A `.json` file the source cannot classify. CPA v8.0.13 only lists a credential it
// synthesized: a body that is not a JSON object, or one without a `type`, returns
// no auth and never reaches `/v8/management/credentials`
// (`internal/watcher/synthesizer/file.go:75-83,158-160`), and the route lists the
// auth manager's own set (`internal/api/handlers/management/auth_files.go:143-147`).
// The file therefore exists on the source host but is not an item of the migration
// inventory: it must stay off the destination and must not be invented as a manual
// item. It stays in the fixture because that distinction is what this run proves.
const unparseableFile = { 'legacy-token.json': 'not a CPA credential document\n' };
secretValues.push(managementKey, emptySourceKey, ...seedKeys, ...Object.values(providerKeys));
for (const [name, body] of Object.entries({ ...credentialFiles, ...overlongFile, ...unparseableFile })) {
  writeFileSync(resolve(sourceAuthDir, name), body, { mode: 0o600 });
  for (const value of body.match(/[A-Za-z0-9_-]{16,}/g) || []) secretValues.push(value);
}

// A free loopback port, pinned before Compose starts: publishing port 0 lets the
// host port change after a restart, which would invalidate the browser's origin.
async function freePort() {
  const probe = createServer();
  await new Promise((resolveProbe, reject) => {
    probe.once('error', reject);
    probe.listen(0, '127.0.0.1', resolveProbe);
  });
  const { port } = probe.address();
  await new Promise((resolveClose) => probe.close(resolveClose));
  return port;
}
const publicPort = await freePort();
const legacyPort = await freePort();
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
      'Command failed: ' + args.slice(0, 4).join(' ') + '\n' + redact(result.stderr || result.stdout || '')
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
// The report names the source it tested. A dirty worktree is refused: an image
// built from it carries bytes no commit names, and every check below would be
// attributed to work that does not exist.
report.gitHead = run(['git', 'rev-parse', 'HEAD']).stdout.trim();
assert.equal(
  run(['git', 'status', '--porcelain']).stdout.trim(),
  '',
  'the worktree must be clean: the report names this HEAD'
);
report.testedSourceCommit = report.gitHead;
for (const [service, revision] of Object.entries(report.imageSourceRevisions)) {
  assert.notEqual(revision, 'unknown', 'the ' + service + ' image carries no source revision');
  assert.equal(
    revision,
    report.gitHead,
    'the ' + service + ' image was built from ' + revision + ', not ' + report.gitHead
  );
}
report.imageSourceCommit = report.imageSourceRevisions['cpamp-manager'];

// The source CPA. `gemini` and `codex` are two provider sections of the approved
// scope and `routing.*` are gateway leaves, so the run has something of every
// copyable kind to report. `multimedia` is deliberately outside the copy table:
// it is a real v8 root the migration never touches, which keeps that difference
// visible in the source snapshot.
const sourceConfig = [
  'config-version: 8',
  'server:',
  '  host: "0.0.0.0"',
  '  port: 8317',
  'management:',
  '  allow-remote: true',
  `  secret-key: "${managementKey}"`,
  '  disable-control-panel: true',
  '  disable-auto-update-panel: true',
  'oauth:',
  '  auth-dir: "/cpa/auth"',
  'access:',
  '  api-keys:',
  ...seedKeys.map((key) => `    - "${key}"`),
  'routing:',
  '  strategy: round-robin',
  '  session-affinity: true',
  '  retry:',
  '    request-retry: 3',
  // A v8 provider section is a list of groups, and each group carries a `keys`
  // list: CPA refuses `api-keys.<section>[0].keys must be a list` otherwise.
  'api-keys:',
  '  gemini:',
  '    - name: "gemini-group"',
  '      keys:',
  '        - api-key: "' + providerKeys.gemini + '"',
  '  codex:',
  '    - name: "codex-group"',
  '      keys:',
  '        - api-key: "' + providerKeys.codex + '"',
  'multimedia:',
  '  disable-image-generation: chat',
  'plugins:',
  '  enabled: false',
  'observability:',
  '  logs:',
  '    logging-to-file: false',
  '  usage:',
  '    usage-statistics-enabled: false',
  '',
].join('\n');
writeFileSync(sourceConfigPath, sourceConfig, { mode: 0o600 });
// A source with no client Key: adopting it is refused with its own code rather
// than reported as unreadable.
const emptyConfig = sourceConfig
  .replace('  api-keys:\n    - "' + seedKeys[0] + '"\n    - "' + seedKeys[1] + '"\n', '  api-keys: []\n')
  .replace(managementKey, emptySourceKey);
assert(emptyConfig.includes('api-keys: []'), 'the empty source config was not reduced');
writeFileSync(emptyConfigPath, emptyConfig, { mode: 0o600 });
// The v7 negative only needs a process that answers, not a full config.
writeFileSync(
  legacyConfigPath,
  [
    'host: "0.0.0.0"',
    'port: 8317',
    'remote-management:',
    '  allow-remote: true',
    `  secret-key: "${managementKey}"`,
    'auth-dir: "/cpa/auth"',
    'api-keys:',
    `  - "${seedKeys[0]}"`,
    '',
  ].join('\n'),
  { mode: 0o600 }
);

// The forwarder between the Manager and the source CPA. It forwards byte for byte
// and refuses every NEW connection once the control file exists, which is what
// turns one honest fault into a recorded, retryable item: the connection that was
// already open when the file appeared is never disturbed, so the run's inventory
// read is not torn out from under it.
writeFileSync(
  forwarderPath,
  `import asyncio, os
CONTROL = "/control/refuse"
UPSTREAM = ("cpamp-d-adopt-cpa", 8317)

async def pipe(reader, writer):
    try:
        while True:
            chunk = await reader.read(65536)
            if not chunk:
                break
            writer.write(chunk)
            await writer.drain()
    except Exception:
        pass
    finally:
        try:
            writer.close()
        except Exception:
            pass

async def handle(reader, writer):
    if os.path.exists(CONTROL):
        writer.close()
        return
    try:
        up_r, up_w = await asyncio.open_connection(*UPSTREAM)
    except Exception:
        writer.close()
        return
    await asyncio.gather(pipe(reader, up_w), pipe(up_r, writer))

async def main():
    server = await asyncio.start_server(handle, "0.0.0.0", 8317)
    async with server:
        await server.serve_forever()

asyncio.run(main())
`,
  { mode: 0o600 }
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
const sourceContainer = project + '-cpa';
const emptyContainer = project + '-cpa-empty';
const forwarderContainer = project + '-forwarder';
const legacyContainer = project + '-cpa-v7';
const pythonImage = docker('image', 'inspect', 'python:3.13-alpine', '--format', '{{.Id}}');

function pass(name, observed) {
  report.checks.push({ name, result: 'PASS', ...(observed ? { observed } : {}) });
  console.log('PASS ' + name + (observed ? ' ' + redact(JSON.stringify(observed)) : ''));
}
function fail(name, observed) {
  report.checks.push({ name, result: 'FAIL', ...(observed ? { observed } : {}) });
  console.log('FAIL ' + name + (observed ? ' ' + redact(JSON.stringify(observed)) : ''));
  throw new Error('Check failed: ' + name);
}
function notRun(name, reason) {
  report.checks.push({ name, result: 'NOT RUN', reason });
  console.log('NOT RUN ' + name + ' — ' + reason);
}
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
    await new Promise((resolveWait) => setTimeout(resolveWait, 200));
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
    route: new URL(page.url()).pathname,
  });
}

// A secret must not survive anywhere this run wrote: the report object itself
// (asserted UNREDACTED), everything the run printed, and the log of every container
// of this run. Redaction makes the artifact readable, never safe.
function assertNoSecret(where, text) {
  for (const value of secretValues) {
    if (value && text.includes(value)) throw new Error('a secret value reached ' + where);
  }
}
function runContainerLogs() {
  const names = run(
    ['docker', 'ps', '-a', '--filter', 'name=' + project, '--format', '{{.Names}}'],
    undefined,
    false
  )
    .stdout.trim()
    .split('\n')
    .filter(Boolean);
  return names.map((name) => {
    const logs = run(['docker', 'logs', name], undefined, false);
    return { name, text: (logs.stdout || '') + (logs.stderr || '') };
  });
}
function containerLog(container, lines = 200) {
  const logs = run(['docker', 'logs', '--tail', String(lines), container], undefined, false);
  return (logs.stdout || '') + (logs.stderr || '');
}
// The source's own state, read from the CPA container itself: the config bytes and
// the credential list, as the operator would see them on that host.
const sha256 = (value) => createHash('sha256').update(value).digest('hex');
function sourceSnapshot() {
  const config = run(['docker', 'exec', sourceContainer, 'cat', '/cpa/config.yaml']).stdout;
  const names = run(['docker', 'exec', sourceContainer, 'ls', '-1', '/cpa/auth'])
    .stdout.trim()
    .split('\n')
    .filter(Boolean)
    .sort();
  const digests = Object.fromEntries(
    names.map((name) => [
      name,
      sha256(run(['docker', 'exec', sourceContainer, 'cat', '/cpa/auth/' + name]).stdout),
    ])
  );
  return { configSha256: sha256(config), config, credentials: names, digests };
}
// The source's own credential listing, read inside the source container's network
// namespace so the answer is CPA's and not a copy of it. It is the inventory the
// Adopt contract defines the credential migration over.
function sourceListing() {
  const listing = run([
    'docker', 'run', '--rm', '--network', 'container:' + sourceContainer, '--entrypoint', 'wget',
    images['cpamp-manager'], '-qO-', '--header=Authorization: Bearer ' + managementKey,
    'http://127.0.0.1:8317/v8/management/credentials',
  ]).stdout;
  return JSON.parse(listing).files.map((file) => file.name);
}
// The destination CPA is the Supervisor's own, so its state is read from the
// runtime's filesystem: that is the process the adoption actually configured,
// independent of what the Manager reports about itself. The Supervisor stages it
// under its journal root (`supervisorRoot/destination`, lifecycle/destination.go
// `EnableDestination`), and the gateway config and auth directory are its two
// files (`destination.publish`).
const destinationRoot = '/runtime/supervisor/destination';
function destinationConfig() {
  const raw = dc('exec', '-T', 'cpamp-runtime', 'cat', destinationRoot + '/config.yaml');
  return JSON.parse(raw);
}
function destinationCredentials() {
  const listing = dc('exec', '-T', 'cpamp-runtime', 'sh', '-c', 'ls -1 ' + destinationRoot + '/auth 2>/dev/null || true');
  const names = listing.trim().split('\n').filter(Boolean).sort();
  return {
    names,
    digests: Object.fromEntries(
      names.map((name) => [
        name,
        sha256(dc('exec', '-T', 'cpamp-runtime', 'cat', destinationRoot + '/auth/' + name)),
      ])
    ),
  };
}

let origin = '';
let network = '';
let adminKey = '';
const adminHeaders = () => ({ Authorization: 'Bearer ' + adminKey });
// The browser owns the one-time claim, so this script never holds a second Fresh
// authority: the recorded summary is observed from the responses the page itself
// reads. That is also the only observation that proves the page saw it.
let lastSummary = null;
// The durable adoption record, read from the Manager's own store. Progress is
// timed from the record rather than from the page's poll interval, so the fault
// cannot be injected after the run has already settled.
function adoptionRecord() {
  const volume = JSON.parse(docker('inspect', dc('ps', '-q', 'cpamp-manager')))[0].Mounts.find(
    (mount) => mount.Destination === '/data'
  ).Name;
  const raw = run(
    ['docker', 'run', '--rm', '-i', '--network', 'none', '-v', `${volume}:/data:ro`, pythonImage,
      'python3', '-c',
      "import sqlite3,json\nc=sqlite3.connect('file:/data/usage.sqlite?mode=ro',uri=True)\nr=c.execute(\"select value from settings where key='fresh_adoption_summary_v1'\").fetchone()\nprint(r[0] if r else '{}')"],
    undefined
  ).stdout.trim();
  const summary = JSON.parse(raw);
  return {
    items: summary.items || [],
    planned: summary.planned || 0,
    finished: summary.finished === true,
    // The store's own retryable rule: a failure that happened before any write.
    retryable: (summary.items || []).filter(
      (item) =>
        item.outcome === 'failed' &&
        (item.reason === 'download_failed' || item.reason === 'source_unreadable')
    ).length,
  };
}
async function adminGet(path) {
  const response = await fetch(origin + '/console/api/v2/' + path, { headers: adminHeaders() });
  assert.equal(response.status, 200, 'admin read ' + path + ': ' + response.status);
  return response.json();
}

try {
  dc('up', '-d', '--no-build', '--pull', 'never');
  const ingressID = dc('ps', '-q', 'cpamp-ingress');
  const binding = JSON.parse(docker('inspect', ingressID))[0].NetworkSettings.Ports['18317/tcp'][0];
  assert.equal(binding.HostIp, '127.0.0.1');
  origin = `http://127.0.0.1:${binding.HostPort}`;
  report.origin = origin;
  network = Object.keys(JSON.parse(docker('inspect', ingressID))[0].NetworkSettings.Networks)[0];
  await wait(async () => (await fetch(origin + bootstrap)).ok, 'public Discovery');

  // The two source-side processes and the forwarder, all on the project network
  // under their own aliases. Nothing here is the Manager's Runtime.
  docker(
    'run', '-d', '--name', sourceContainer, '--network', network,
    '--network-alias', 'cpamp-d-adopt-cpa',
    '-v', `${sourceConfigPath}:/cpa/config.yaml`,
    '-v', `${sourceAuthDir}:/cpa/auth`,
    images['cpamp-runtime'], '/usr/local/bin/cli-proxy-api', '-config', '/cpa/config.yaml'
  );
  docker(
    'run', '-d', '--name', emptyContainer, '--network', network,
    '--network-alias', 'cpamp-d-adopt-empty',
    '-v', `${emptyConfigPath}:/cpa/config.yaml`,
    '-v', `${emptyAuthDir}:/cpa/auth`,
    images['cpamp-runtime'], '/usr/local/bin/cli-proxy-api', '-config', '/cpa/config.yaml'
  );
  docker(
    'run', '-d', '--name', forwarderContainer, '--network', network,
    '--network-alias', 'cpamp-d-adopt-source',
    '-v', `${forwarderPath}:/forwarder.py:ro`,
    '-v', `${controlDir}:/control`,
    pythonImage, 'python3', '/forwarder.py'
  );
  const managementProbe = (alias, key) =>
    run(
      ['docker', 'run', '--rm', '--network', network, '--entrypoint', 'wget',
        images['cpamp-manager'], '-qO-', '--header=Authorization: Bearer ' + key,
        `http://${alias}:8317/v8/management/config`],
      undefined, false
    ).status === 0;
  await check('an independent CPA v8.0.13 source answers its v8 management API', async () => {
    await wait(async () => managementProbe('cpamp-d-adopt-cpa', managementKey), 'source CPA', 120000);
    const version = run(
      ['docker', 'run', '--rm', '--network', network, '--entrypoint', 'wget',
        images['cpamp-manager'], '-S', '-qO-', '--header=Authorization: Bearer ' + managementKey,
        'http://cpamp-d-adopt-cpa:8317/v8/management/config'],
      undefined, false
    );
    report.sourceVersion =
      /X-Cpa-Version:\s*(v?[0-9][0-9.]*)/i.exec((version.stderr || '') + (version.stdout || ''))?.[1] ||
      'unknown';
    assert.match(report.sourceVersion, /8\.0\.13/, 'the source must be CPA v8.0.13');
    return { version: report.sourceVersion };
  });
  await wait(async () => managementProbe('cpamp-d-adopt-source', managementKey), 'forwarder', 60000);
  await wait(async () => managementProbe('cpamp-d-adopt-empty', emptySourceKey), 'empty source', 120000);
  const before = sourceSnapshot();
  report.sourceSnapshot = { configSha256: before.configSha256, credentials: before.credentials };
  assert(before.credentials.includes('source-00.json'), 'the source credential list');
  assert(before.credentials.includes('legacy-token.json'), 'the unparseable file is not in the auth dir');
  // The inventory is the source's own v8 listing. A file it cannot classify exists
  // on the host but is not a credential the source reports, so it can be neither
  // copied nor recorded: this check pins that boundary before the run relies on it.
  await check('the source lists only the credentials its v8 API can classify', async () => {
    const listed = sourceListing();
    assert(listed.includes('source-00.json'), 'the source listing is missing a seeded credential');
    assert(listed.includes(overlongName), 'the source listing is missing the over-long credential');
    assert.equal(
      listed.includes('legacy-token.json'),
      false,
      'the v8 listing reported a file it cannot classify'
    );
    return { listed: listed.length };
  });

  // The Manager's own boot is the operator's: a read-only listener until the Host
  // admits the store, then the claimable service.
  dc('stop', 'cpamp-manager');
  const handoff = dc('run', '--rm', '--no-deps', 'cpamp-manager', 'initialize');
  dc('up', '-d', '--no-build', 'cpamp-manager');
  await wait(async () => (await fetch(origin + bootstrap + '/ui')).ok, 'Bootstrap UI', 120000);
  const claim = JSON.parse(Buffer.from(handoff.split('#handoff=')[1], 'base64url').toString());
  secretValues.push(claim.token);

  browser = await chromium.launch({
    headless: true,
    ...(process.env.PLAYWRIGHT_CHANNEL ? { channel: process.env.PLAYWRIGHT_CHANNEL } : {}),
  });
  const context = await browser.newContext({ locale: 'en-US', viewport: { width: 1440, height: 1000 } });
  page = await context.newPage();
  page.on('pageerror', () => report.pageErrors.push('Uncaught browser error'));
  // The page's own progress read is the product path; a request that never leaves
  // or never answers is named here rather than inferred from a spinner.
  page.on('requestfailed', (request) => {
    if (!new URL(request.url()).pathname.endsWith('/installation/adoption')) return;
    report.summaryRequestFailures = report.summaryRequestFailures || [];
    report.summaryRequestFailures.push(request.failure()?.errorText || 'failed');
  });
  page.on('console', (message) => {
    if (message.type() !== 'error') return;
    report.consoleErrors = report.consoleErrors || [];
    report.consoleErrors.push(redact(message.text()).slice(0, 200));
  });
  page.on('response', async (response) => {
    if (!new URL(response.url()).pathname.endsWith('/installation/adoption')) return;
    report.summaryStatuses = report.summaryStatuses || [];
    report.summaryStatuses.push(response.status());
    try {
      lastSummary = await response.json();
    } catch (error) {
      report.summaryParseFailure = String(error.message || error).slice(0, 300);
    }
  });
  await page.goto(origin + handoff);
  await page.getByTestId('fresh-setup-wizard').waitFor();
  await page.getByTestId('choose-adopt_existing').click();
  await page.getByTestId('setup-next').click();
  await page.getByTestId('generate-key-btn').click();
  await wait(
    async () => (await page.getByTestId('admin-key-input').inputValue()).length >= 24,
    'generated Admin Key'
  );
  adminKey = await page.getByTestId('admin-key-input').inputValue();
  secretValues.push(adminKey);
  await page.getByTestId('setup-review').click();
  await page.getByTestId('adopt-source-url').waitFor();
  pass('Adopt is selectable and the wizard opens its source form');

  // Every negative happens before the commit, so each one proves a refusal that
  // recorded nothing rather than a state the run had to undo.
  const attemptSource = async (url, key) => {
    await page.getByTestId('adopt-source-url').fill(url);
    await page.getByTestId('adopt-management-key').fill(key);
    await page.getByTestId('adopt-read-inventory').click();
    const alert = page.getByRole('alert').first();
    await alert.waitFor({ timeout: 30000 });
    const message = await alert.innerText();
    // Nothing was committed: the source form is still the page, and no inventory
    // exists to confirm.
    assert.equal(await page.getByTestId('adopt-inventory').count(), 0, 'a refusal staged an inventory');
    assert.equal(await page.getByTestId('adopt-source-url').count(), 1, 'a refusal left the source stage');
    return message;
  };
  await check('a wrong management key is refused with adoption_source_read_unavailable', async () => {
    const message = await attemptSource('http://cpamp-d-adopt-cpa:8317', 'wrong-' + randomBytes(8).toString('hex'));
    assert.match(message, /could not be read with this key|not CPA v8/i);
    return { message };
  });
  await check('a source with zero client keys is refused with adoption_source_empty', async () => {
    const message = await attemptSource('http://cpamp-d-adopt-empty:8317', emptySourceKey);
    assert.match(message, /no client keys/i);
    return { message };
  });
  await check('a source that is the destination is refused with adoption_destination_identity_conflict', async () => {
    // The destination is the CPA this Manager runs, reached on the Compose network.
    const message = await attemptSource('http://cpamp-runtime:8317', managementKey);
    assert.match(message, /this Manager.s own CPA/i);
    return { message };
  });
  if (process.env.CPAMP_D_CPA_LEGACY && existsSync(process.env.CPAMP_D_CPA_LEGACY)) {
    await check('a v7 source is refused with nothing committed', async () => {
      docker(
        'run', '-d', '--name', legacyContainer, '--network', network,
        '--network-alias', 'cpamp-d-adopt-v7',
        '-p', `127.0.0.1:${legacyPort}:8317`,
        '-v', `${legacyConfigPath}:/cpa/config.yaml`,
        '-v', `${legacyAuthDir}:/cpa/auth`,
        '-v', `${process.env.CPAMP_D_CPA_LEGACY}:/usr/local/bin/cli-proxy-api:ro`,
        images['cpamp-runtime'], '/usr/local/bin/cli-proxy-api', '-config', '/cpa/config.yaml'
      );
      await wait(
        async () =>
          (await fetch(`http://127.0.0.1:${legacyPort}/v0/management/config`, {
            headers: { Authorization: 'Bearer ' + managementKey },
          }).catch(() => ({ status: 0 }))).status === 200,
        'v7 CPA',
        120000
      );
      const message = await attemptSource('http://cpamp-d-adopt-v7:8317', managementKey);
      assert.match(message, /could not be read with this key|not CPA v8/i);
      return { message };
    });
  } else {
    notRun('a v7 source is refused with nothing committed', 'CPAMP_D_CPA_LEGACY is unset or missing');
  }

  // The real journey. The forwarder is up, so the inventory read and the copy both
  // reach the source.
  await page.getByTestId('adopt-source-url').fill('http://cpamp-d-adopt-source:8317');
  await page.getByTestId('adopt-management-key').fill(managementKey);
  await screenshot('adopt-source');
  await page.getByTestId('adopt-read-inventory').click();
  await page.getByTestId('adopt-inventory').waitFor({ timeout: 60000 });
  await screenshot('adopt-inventory');
  const inventory = await page.getByTestId('adopt-inventory').innerText();
  await check('the inventory names what copies and what needs manual work', async () => {
    assert.match(inventory, /Client API keys/i);
    assert.match(inventory, /Provider keys|Provider sections|Credential files/i);
    assert.equal(
      await page.getByTestId('adopt-confirm-migrate').isDisabled(),
      true,
      'the commit must not be possible before the inventory is confirmed'
    );
    return { stage: 'staged', text: inventory.slice(0, 200) };
  });

  await page.getByTestId('adopt-confirm-inventory').check();
  await page.getByTestId('adopt-confirm-migrate').click();
  // The run has begun. Cut the forwarder's NEW connections as soon as the first
  // item is recorded, so the remaining items fail before writing anything.
  await check('the migration commits the destination and records the first item', async () => {
    // The page's own read is the progress path, so the observer below sees what
    // the operator sees. Each round also presses Refresh, which is one more read,
    // so the first item cannot hide between two poll intervals.
    let first = null;
    await wait(async () => {
      first = adoptionRecord();
      if (first.items.length > 0) {
        writeFileSync(resolve(controlDir, 'refuse'), 'refuse new connections\n');
        return true;
      }
      const refresh = page.getByTestId('adopt-refresh');
      if ((await refresh.count()) > 0 && !(await refresh.isDisabled())) await refresh.click();
      return false;
    }, 'first recorded migration item', 180000);
    return { firstItems: first.items.length, planned: first.planned };
  });
  const retryableSummary = await check('the interrupted run settles as retryable, not as a failure', async () => {
    await wait(async () => {
      if (adoptionRecord().finished) return true;
      const refresh = page.getByTestId('adopt-refresh');
      if ((await refresh.count()) > 0 && !(await refresh.isDisabled())) await refresh.click();
      return false;
    }, 'settled migration', 240000);
    // The page renders the same record; give its last poll a moment to land.
    await wait(async () => lastSummary !== null, 'the page read the summary', 30000);
    // The route's own projection carries the per-item retryable rule; the record
    // only carries reasons. The page has read it by now, so its view is the one
    // the operator saw.
    assert(lastSummary, 'the page never read the summary');
    const summary = lastSummary;
    assert(summary.retryable > 0, 'the interrupted run recorded no retryable item: ' + JSON.stringify(summary.counts));
    // A failure that wrote nothing is retryable; one that failed after a write is
    // not, and both are honest results of the same interruption. What the run must
    // show is that the source-side failures were recorded as retryable, and that
    // the count the record reports is that set.
    const failed = summary.items.filter((item) => item.outcome === 'failed');
    const retryable = failed.filter((item) => item.retryable);
    assert(failed.length > 0, 'the interrupted run recorded no failure');
    assert(retryable.length > 0, 'no source-side failure was recorded as retryable');
    assert.equal(summary.retryable, retryable.length, 'the retryable count disagrees with the items');
    for (const item of retryable)
      assert(
        item.reason === 'download_failed' || item.reason === 'source_unreadable',
        'a retryable item failed after a write: ' + item.reason
      );
    return { counts: summary.counts, retryable: summary.retryable, planned: summary.planned };
  });
  await screenshot('adopt-migration-retryable');
  await check('completion refuses a run with retryable items until they are accepted', async () => {
    // A completion can also be refused for a run that is still winding down, so
    // the operator's action is repeated until the answer is the one that matters:
    // the acknowledgement, or a page that completed without it (which would mean
    // the run had nothing left to retry).
    let answer = null;
    for (let attempt = 0; attempt < 8 && answer === null; attempt += 1) {
      const complete = page.getByTestId('adopt-complete');
      if ((await complete.count()) > 0 && (await complete.isEnabled())) await complete.click();
      await page.waitForTimeout(700);
      if ((await page.getByTestId('adopt-accept-retryable').count()) > 0) answer = 'retryable';
      else if ((await page.getByTestId('enter-console').count()) > 0) answer = 'completed';
    }
    assert.equal(
      answer,
      'retryable',
      'completion over ' + retryableSummary.retryable + ' retryable items answered ' + answer
    );
    const message = await page.getByTestId('adopt-migration').innerText();
    assert.match(message, /could not be copied/i);
    return { retryable: retryableSummary.retryable };
  });
  await screenshot('adopt-completion-retryable');
  // Restore the source and resume from the durable prefix: the same committed
  // replay the operator's Resume button sends.
  rmSync(resolve(controlDir, 'refuse'), { force: true });
  await page.getByTestId('adopt-resume').click();
  await check('Resume continues from the recorded prefix and finishes the run', async () => {
    await wait(async () => {
      const record = adoptionRecord();
      if (record.finished && record.retryable === 0) return true;
      const refresh = page.getByTestId('adopt-refresh');
      if ((await refresh.count()) > 0 && !(await refresh.isDisabled())) await refresh.click();
      return false;
    }, 'resumed migration', 240000);
    assert(lastSummary, 'the page never read the resumed summary');
    const summary = lastSummary;
    assert(summary.planned >= retryableSummary.planned, 'the resumed plan shrank');
    return { counts: summary.counts, planned: summary.planned };
  });
  await wait(
    async () =>
      lastSummary?.finished === true &&
      lastSummary.running === false &&
      lastSummary.retryable === 0,
    'the page rendered the finished run',
    60000
  );
  await check('the completion screen groups the per-item results', async () => {
    const items = await page.getByTestId('adopt-items').innerText();
    assert.match(items, /Migrated/i);
    assert.match(items, /Needs manual work/i);
    assert.match(await page.getByTestId('adopt-progress').innerText(), /of \d+ items settled/);
    // The page's own projection is what the operator saw; the text above proves it
    // rendered the same facts these assertions read.
    assert(lastSummary, 'the page never read the summary');
    const summary = lastSummary;
    // Every seeded kind is reported, and the item the record cannot carry is manual
    // with the instruction the operator needs. The unclassifiable source file is not
    // an item at all: the source's own listing never contained it, so the run
    // neither copied it nor claimed it.
    assert(summary.counts.migrated > 0, 'nothing was migrated');
    assert(summary.counts.manual > 0, 'the item the record cannot carry was not reported as manual');
    const manual = summary.items.filter((item) => item.outcome === 'manual');
    assert(manual.every((item) => item.reason), 'a manual item carries no reason');
    assert(manual.every((item) => item.instruction), 'a manual item carries no instruction');
    assert(
      manual.some((item) => item.reason === 'adoption_item_name_too_long'),
      'the over-long credential was not the manual item: ' + JSON.stringify(manual.map((item) => item.reason))
    );
    assert(
      summary.items.every((item) => !item.item.includes('legacy-token.json')),
      'a file the source never listed was invented as an item'
    );
    assert.equal(summary.counts.failed, 0, 'a resumed run left failures behind');
    const kinds = new Set(
      summary.items.filter((item) => item.outcome === 'migrated').map((item) => item.kind)
    );
    for (const kind of ['credential', 'config_leaf', 'provider_section']) {
      assert(kinds.has(kind), 'no migrated item of kind ' + kind);
    }
    report.migration = {
      counts: summary.counts,
      planned: summary.planned,
      items: summary.items.length,
    };
    report.manualReason = summary.items.find((item) => item.outcome === 'manual')?.reason;
    return { counts: summary.counts, planned: summary.planned };
  });
  await screenshot('adopt-migration-results');
  await page.getByTestId('adopt-complete').click();
  await page.getByTestId('enter-console').waitFor({ timeout: 120000 });
  pass('completion succeeds once every planned item is settled');
  await screenshot('adopt-done');

  // The destination's real state, read from the CPA the Supervisor staged.
  const destination = destinationConfig();
  const adopted = destinationCredentials();
  await check('the destination carries exactly the source client keys', async () => {
    assert.deepEqual([...destination.access['api-keys']].sort(), [...seedKeys].sort());
    return { keys: destination.access['api-keys'].length };
  });
  await check('the gateway configuration holds the seeded source values', async () => {
    assert.equal(destination.routing?.strategy, 'round-robin');
    assert.equal(destination.routing?.['session-affinity'], true);
    assert.equal(destination.routing?.retry?.['request-retry'], 3);
    return destination.routing;
  });
  await check('the migrated credentials are listed on the destination with the source bytes', async () => {
    for (const name of Object.keys(credentialFiles)) {
      assert(adopted.names.includes(name), 'missing credential ' + name);
      assert.equal(adopted.digests[name], before.digests[name], 'credential bytes differ: ' + name);
    }
    assert.equal(adopted.names.includes('legacy-token.json'), false, 'an unparseable file was uploaded');
    assert.equal(
      adopted.names.includes(overlongName),
      false,
      'the credential the record cannot name was uploaded'
    );
    return { names: adopted.names };
  });
  await check('a client key is admitted by the Gateway through Ingress', async () => {
    const response = await fetch(origin + '/v1/models', {
      headers: { Authorization: 'Bearer ' + seedKeys[0] },
    });
    assert.equal(response.status, 200, 'admitted client key answered ' + response.status);
    const refused = await fetch(origin + '/v1/models', {
      headers: { Authorization: 'Bearer ' + 'incorrect-client-key' },
    });
    assert.equal(refused.status, 401, 'an unknown client key must be refused');
    return { models: response.status, unknown: refused.status };
  });
  await check('the usage collector started for the adopted installation', async () => {
    const logs = containerLog('cpamp-manager', 400);
    assert(!/Fresh usage collection unavailable/.test(logs), 'the collector did not start');
    assert(!/Fresh usage collection start unavailable/.test(logs), 'the collection start was not recorded');
    // The key workspace must answer for the completed installation, and the
    // Manager's own start path must have recorded the collection window; a start
    // failure logs one of the two lines above.
    const keys = await adminGet('api-keys');
    assert(Array.isArray(keys.keys), 'the key workspace answered no list');
    return { keys: keys.keys.length, presenceUnverified: keys.presenceUnverified === true };
  });
  await check('runtime status is readable for the completed installation', async () => {
    const status = await adminGet('runtime/status');
    return { state: status.state, identity: status.runtimeIdentity };
  });
  await check('the source CPA is unchanged: identical config bytes and credential list', async () => {
    const after = sourceSnapshot();
    assert.equal(after.configSha256, before.configSha256, 'the source config changed');
    assert.deepEqual(after.credentials, before.credentials, 'the source credential list changed');
    assert.deepEqual(after.digests, before.digests, 'a source credential changed');
    return { configSha256: after.configSha256, credentials: after.credentials.length };
  });
  assert.deepEqual(report.pageErrors, []);
  report.result = 'PASS';
} catch (error) {
  report.failure = String(error.stack || error);
  try {
    for (const [name, container] of [
      ['manager', dc('ps', '-q', 'cpamp-manager')],
      ['runtime', dc('ps', '-q', 'cpamp-runtime')],
      ['ingress', dc('ps', '-q', 'cpamp-ingress')],
      ['source', sourceContainer],
      ['forwarder', forwarderContainer],
    ]) {
      if (!container) continue;
      report['failure' + name + 'Logs'] = containerLog(container, 120).slice(-8000);
    }
  } catch {}
  if (page && !page.isClosed()) {
    report.failurePage = await page.locator('body').innerText().catch(() => 'Page unavailable');
    await page.screenshot({ path: resolve(out, 'failure.png'), fullPage: true }).catch(() => {});
  }
} finally {
  if (browser) await browser.close();
  // Container logs are read before anything is removed: a removed container has
  // no log, so an assertion after teardown could never fail.
  const containerLogs = runContainerLogs();
  // Only this run's containers. Nothing named cpamp-* beyond the project prefix is
  // touched, and the user's own stack is never in this project.
  run(['docker', 'rm', '-f', sourceContainer, emptyContainer, forwarderContainer, legacyContainer], undefined, false);
  dc('down', '-v', '--remove-orphans');
  // The CPA configs and the forwarder are inputs, not evidence: they hold every
  // management key and seed Client Key, so the whole directory is removed rather
  // than redacted in place.
  rmSync(secretsDir, { recursive: true, force: true });
  report.secretAssertion = 'PASS';
  try {
    assertNoSecret('the report', JSON.stringify(report));
    assertNoSecret('the run log', printed.join('\n'));
    for (const { name, text } of containerLogs) assertNoSecret('the logs of ' + name, text);
  } catch (error) {
    report.secretAssertion = 'FAIL';
    report.secretFailure = String(error.message);
    report.result = 'FAIL';
  }
  const serialized = redact(JSON.stringify(report, null, 2));
  for (const value of secretValues)
    assert(!serialized.includes(value), 'a secret reached the serialized report');
  writeFileSync(resolve(out, 'report.json'), serialized, { mode: 0o600 });
  try {
    writeFileSync(
      resolve(logDir, 'd-adopt-' + report.gitHead.slice(0, 12) + '.log'),
      redact(printed.join('\n')),
      { mode: 0o600 }
    );
  } catch {}
  console.log(report.result + ': ' + resolve(out, 'report.json'));
  if (report.failure) console.error(report.failure);
  if (report.secretFailure) console.error(report.secretFailure);
  if (report.result !== 'PASS') process.exitCode = 1;
}
