import { execFileSync } from 'node:child_process';
import { readdirSync, readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { isatty } from 'node:tty';
import { fileURLToPath } from 'node:url';

const fail = (message) => {
  throw new Error(`Runtime 13 Compose validation failed: ${message}`);
};

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..');
const stdinChunks = [];
if (!isatty(0)) {
  for await (const chunk of process.stdin) {
    stdinChunks.push(Buffer.from(chunk));
  }
}
const stdinText = Buffer.concat(stdinChunks).toString('utf8').trim();
const document = JSON.parse(
  stdinText ||
    execFileSync(
      'docker',
      ['compose', '-f', resolve(repoRoot, 'docker-compose.yml'), 'config', '--format', 'json'],
      { cwd: repoRoot, encoding: 'utf8' }
    )
);
const services = document.services ?? {};
const serviceNames = Object.keys(services).sort();
if (JSON.stringify(serviceNames) !== JSON.stringify(['cpa-manager-plus'])) {
  fail(`expected single service cpa-manager-plus, got ${serviceNames.join(', ')}`);
}

const cpamp = services['cpa-manager-plus'];
const published = Object.entries(services).flatMap(([service, config]) =>
  (config.ports ?? []).map((port) => ({ service, ...port }))
);
if (
  published.length !== 1 ||
  published[0].service !== 'cpa-manager-plus' ||
  Number(published[0].target) !== 18317 ||
  String(published[0].published) !== '18317'
) {
  fail(`expected only cpa-manager-plus 18317:18317, got ${JSON.stringify(published)}`);
}

if (cpamp.user) {
  fail('cpa-manager-plus must start as root so cpamp-launcher can provision /data and drop privileges per child');
}
const stopGrace = String(cpamp.stop_grace_period ?? '');
if (stopGrace !== '2m0s' && stopGrace !== '120s') {
  fail(`cpa-manager-plus must set stop_grace_period >= 120s, got ${JSON.stringify(cpamp.stop_grace_period)}`);
}

const env = cpamp.environment ?? {};
const loopbackUrlRules = {
  CPAMP_MANAGER_URL: 'http://127.0.0.1:18319',
  CPAMP_GATEWAY_URL: 'http://127.0.0.1:8317',
  CPA_UPSTREAM_URL: 'http://127.0.0.1:8317',
  CPAMP_RUNTIME_URL: 'http://127.0.0.1:9081',
  CPAMP_INGRESS_CONTROL_URL: 'http://127.0.0.1:9082',
};
for (const [key, expected] of Object.entries(loopbackUrlRules)) {
  if (key in env && env[key] !== '' && env[key] !== expected) {
    fail(`${key} override must stay on in-container loopback ${expected}, got ${JSON.stringify(env[key])}`);
  }
}
const loopbackAddrRules = {
  CPAMP_INGRESS_CONTROL_ADDR: '127.0.0.1:9082',
  HTTP_ADDR: '127.0.0.1:18319',
  CPAMP_RUNTIME_LISTEN_ADDR: '127.0.0.1:9081',
};
for (const [key, expected] of Object.entries(loopbackAddrRules)) {
  if (key in env && env[key] !== '' && env[key] !== expected) {
    fail(`${key} override must stay on in-container loopback ${expected}, got ${JSON.stringify(env[key])}`);
  }
}
if ('CPAMP_RUNTIME_TOKEN' in env && String(env.CPAMP_RUNTIME_TOKEN ?? '') !== '') {
  fail('cpa-manager-plus must not inline CPAMP_RUNTIME_TOKEN in Compose environment');
}

for (const [name, service] of Object.entries(services)) {
  if ('CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK' in (service.environment ?? {})) {
    fail(`${name} must not define CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK`);
  }
}
const isDockerSurfaceEntry = (entry) =>
  /^Dockerfile($|\.)|(^|\.)docker-compose.*\.ya?ml$|compose.*\.ya?ml$/.test(entry);
const dockerSurfaceFiles = [
  ...readdirSync(repoRoot)
    .filter(isDockerSurfaceEntry)
    .map((entry) => resolve(repoRoot, entry)),
  ...readdirSync(resolve(repoRoot, 'bin', 'ci'))
    .filter(isDockerSurfaceEntry)
    .map((entry) => resolve(repoRoot, 'bin', 'ci', entry)),
  ...readdirSync(resolve(repoRoot, 'tests', 'fixtures', 'cpa-v8-qualification'))
    .filter(isDockerSurfaceEntry)
    .map((entry) => resolve(repoRoot, 'tests', 'fixtures', 'cpa-v8-qualification', entry)),
];
for (const filePath of dockerSurfaceFiles) {
  if (readFileSync(filePath, 'utf8').includes('CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK')) {
    fail(`${filePath} must not define CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK`);
  }
}

const isDockerSocketPath = (value) =>
  typeof value === 'string' && /(^|\/)docker\.sock$/.test(value.replaceAll('\\', '/'));
const mountsDockerSocket = (service) =>
  (service.volumes ?? []).some((volume) => {
    if (typeof volume === 'string') {
      return volume.split(':').some(isDockerSocketPath);
    }
    return isDockerSocketPath(volume.source) || isDockerSocketPath(volume.target);
  });

for (const [name, service] of Object.entries(services)) {
  if (service.network_mode === 'host') fail(`${name} must not use host networking`);
  if (service.privileged === true) fail(`${name} must not run privileged`);
  if (mountsDockerSocket(service)) fail(`${name} must not mount a Docker socket`);
}

const volumes = cpamp.volumes ?? [];
const dataMount = volumes.find(
  (volume) =>
    volume.type === 'volume' &&
    volume.source === 'cpamp-data' &&
    volume.target === '/data' &&
    volume.read_only !== true
);
if (!dataMount || volumes.length !== 1) {
  fail(`cpa-manager-plus must mount only named volume cpamp-data:/data rw, got ${JSON.stringify(volumes)}`);
}

const topVolumes = Object.keys(document.volumes ?? {}).sort();
if (JSON.stringify(topVolumes) !== JSON.stringify(['cpamp-data'])) {
  fail(`compose must define exact top-level volume cpamp-data, got ${topVolumes.join(', ')}`);
}

console.log(
  'Single-image Compose validation passed: cpa-manager-plus service, one public 18317 mapping, single /data volume, and loopback/socket/privilege invariants'
);
