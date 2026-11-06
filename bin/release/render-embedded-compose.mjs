import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { existsSync, mkdirSync, readFileSync, realpathSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { delimiter, dirname, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { gunzipSync } from 'node:zlib';

function loadYamlModule() {
  const candidates = [
    import.meta.url,
    pathToFileURL(resolve(process.cwd(), 'package.json')).href,
  ];
  for (const dir of (process.env.NODE_PATH ?? '').split(delimiter).filter(Boolean)) {
    candidates.push(pathToFileURL(resolve(dir, 'package.json')).href);
  }
  for (const base of candidates) {
    try {
      return createRequire(base)('yaml');
    } catch {
      // continue searching
    }
  }
  throw new Error('Required dependency "yaml" could not be resolved');
}

const yaml = loadYamlModule();

export const ROLES = Object.freeze(['manager', 'ingress', 'runtime']);
export const COMPOSE_SERVICES = Object.freeze({
  ingress: 'cpamp-ingress',
  manager: 'cpamp-manager',
  runtime: 'cpamp-runtime',
});
export const DEFAULT_REPOSITORIES = Object.freeze({
  manager: 'cpa-manager-plus',
  ingress: 'cpamp-ingress',
  runtime: 'cpamp-runtime',
});
export const DEFAULT_REGISTRY_PREFIX = 'seakee';
export const COMPOSE_FILENAME = 'docker-compose.v2-embedded.yml';
export const PAIRING_FILENAME = 'embedded-release-pairing.json';

export const EXPECTED_CPA_VERSION = '8.0.55';
export const EXPECTED_CPA_SHA256 =
  'e48c85c75a19adae186cbee590f6e6143fd8085417d8066b545e8533877175f7';
export const EXPECTED_CPA_ARTIFACT_ID = `sha256:${EXPECTED_CPA_SHA256}`;
export const EXPECTED_API_CONTRACT_ID = 'cpamp-r1-embedded-public-api-transport-v1';
export const EXPECTED_API_CONTRACT_QUALIFICATION =
  'SOURCE_TRANSPORT_CONTRACT_ONLY_NOT_COMPLETE_WEB_API_COMPATIBILITY';
export const EXPECTED_API_SUFFIX = '/api/v2';
export const EXPECTED_API_PATH = '/phase4-test/api/v2/technical';

export const PACKAGED_PATHS = Object.freeze({
  binaries: Object.freeze({
    manager: '/usr/local/bin/cpa-manager-plus',
    ingress: '/usr/local/bin/cpamp-ingress',
    runtime: '/usr/local/bin/cpamp-runtime-supervisor',
  }),
  provenance: '/usr/local/share/cpamp/provenance.json',
  webManifest: '/usr/local/share/cpamp/release/fullweb-manifest.json',
  apiContract: '/usr/local/share/cpamp/release/public-api-transport-contract.json',
  cpaBinary: '/usr/local/bin/cli-proxy-api',
  cpaArtifact: '/usr/local/share/cpamp/runtime/cpa-artifact.json',
});

const SHA256_DIGEST_RE = /^sha256:[0-9a-f]{64}$/;
const SOURCE_COMMIT_RE = /^[0-9a-f]{40}$/;
const VERSION_TAG_RE = /^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$/;
const REGISTRY_PREFIX_RE = /^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$/;
const PINNED_IMAGE_REF_RE =
  /^([A-Za-z0-9][A-Za-z0-9._/-]*\/[A-Za-z0-9._-]+):([A-Za-z0-9][A-Za-z0-9._+-]{0,127})@(sha256:[0-9a-f]{64})$/;

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..');

const fail = (message) => {
  throw new Error(`Embedded compose/pairing contract failed: ${message}`);
};

const sha256Digest = (bufferOrString) =>
  'sha256:' + createHash('sha256').update(bufferOrString).digest('hex');

const isPlainObject = (value) =>
  typeof value === 'object' && value !== null && !Array.isArray(value);

function parseTarEntries(buffer) {
  const entries = new Map();
  let offset = 0;
  let pendingPaxPath = null;
  let pendingLongName = null;

  while (offset + 512 <= buffer.length) {
    const header = buffer.subarray(offset, offset + 512);
    let allZero = true;
    for (let i = 0; i < 512; i += 1) {
      if (header[i] !== 0) {
        allZero = false;
        break;
      }
    }
    if (allZero) break;

    const rawName = header.subarray(0, 100).toString('utf8').replace(/\0.*$/, '');
    const rawPrefix = header.subarray(345, 500).toString('utf8').replace(/\0.*$/, '');
    const sizeOctal = header.subarray(124, 136).toString('utf8').replace(/\0.*$/, '').trim();
    const size = sizeOctal ? Number.parseInt(sizeOctal, 8) : 0;
    if (!Number.isFinite(size) || size < 0) {
      fail('invalid tar entry size');
    }
    const typeFlag = header[156] === 0 ? '0' : String.fromCharCode(header[156]);
    const dataStart = offset + 512;
    const dataEnd = dataStart + size;
    if (dataEnd > buffer.length) {
      fail('truncated tar archive entry');
    }
    const payload = buffer.subarray(dataStart, dataEnd);
    offset = dataStart + Math.ceil(size / 512) * 512;

    if (typeFlag === 'x' || typeFlag === 'g') {
      const paxText = payload.toString('utf8');
      for (const line of paxText.split('\n')) {
        const spaceIdx = line.indexOf(' ');
        if (spaceIdx > 0) {
          const kv = line.slice(spaceIdx + 1);
          if (kv.startsWith('path=')) {
            pendingPaxPath = kv.slice('path='.length);
          }
        }
      }
      continue;
    }
    if (typeFlag === 'L') {
      pendingLongName = payload.toString('utf8').replace(/\0.*$/, '');
      continue;
    }

    const combinedName =
      pendingPaxPath ?? pendingLongName ?? (rawPrefix ? `${rawPrefix}/${rawName}` : rawName);
    pendingPaxPath = null;
    pendingLongName = null;

    const normalized = combinedName.replace(/^\.?\//, '');
    if (typeFlag === '0' && normalized) {
      entries.set(normalized, payload);
    }
  }
  return entries;
}

export function extractFilesFromOciArchive(ociTarPath, wantedPaths) {
  if (!existsSync(ociTarPath)) {
    fail(`missing OCI archive: ${ociTarPath}`);
  }
  const outerEntries = parseTarEntries(readFileSync(ociTarPath));
  const indexBytes = outerEntries.get('index.json');
  if (!indexBytes) fail(`missing index.json in OCI archive: ${ociTarPath}`);
  const index = JSON.parse(indexBytes.toString('utf8'));
  const descriptors = Array.isArray(index.manifests) ? index.manifests : [];
  if (descriptors.length !== 1) {
    fail(`OCI archive must hold exactly one manifest: ${ociTarPath}`);
  }
  const manifestDigest = descriptors[0]?.digest;
  if (!SHA256_DIGEST_RE.test(manifestDigest ?? '')) {
    fail(`invalid manifest digest in OCI archive: ${ociTarPath}`);
  }
  const manifestBlob = outerEntries.get(`blobs/sha256/${manifestDigest.slice(7)}`);
  if (!manifestBlob || sha256Digest(manifestBlob) !== manifestDigest) {
    fail(`manifest blob missing or corrupt in OCI archive: ${ociTarPath}`);
  }
  const manifest = JSON.parse(manifestBlob.toString('utf8'));
  const layers = Array.isArray(manifest.layers) ? manifest.layers : [];
  const wantedSet = new Set(wantedPaths.map((p) => (p.startsWith('/') ? p : `/${p}`)));
  const found = new Map();

  for (const layerDescriptor of layers) {
    const layerDigest = layerDescriptor?.digest;
    if (!SHA256_DIGEST_RE.test(layerDigest ?? '')) {
      fail(`invalid layer digest in OCI archive: ${ociTarPath}`);
    }
    const rawLayer = outerEntries.get(`blobs/sha256/${layerDigest.slice(7)}`);
    if (!rawLayer || sha256Digest(rawLayer) !== layerDigest) {
      fail(`layer blob mismatch in OCI archive: ${ociTarPath}`);
    }
    const mediaType = layerDescriptor?.mediaType;
    const uncompressed =
      mediaType === 'application/vnd.oci.image.layer.v1.tar+gzip'
        ? gunzipSync(rawLayer)
        : mediaType === 'application/vnd.oci.image.layer.v1.tar'
          ? rawLayer
          : fail(`unsupported OCI layer mediaType: ${String(mediaType)}`);
    const layerEntries = parseTarEntries(uncompressed);
    for (const [entryPath, bytes] of layerEntries.entries()) {
      const canonical = `/${entryPath.replace(/^\/+/, '')}`;
      const parts = canonical.split('/').filter(Boolean);
      const base = parts[parts.length - 1] ?? '';
      const parent = '/' + parts.slice(0, -1).join('/');
      if (base === '.wh..wh..opq') {
        const prefix = parent === '/' ? '/' : `${parent}/`;
        for (const existing of [...found.keys()]) {
          if (existing.startsWith(prefix)) found.delete(existing);
        }
        continue;
      }
      if (base.startsWith('.wh.')) {
        const target = (parent === '/' ? '' : parent) + '/' + base.slice(4);
        for (const existing of [...found.keys()]) {
          if (existing === target || existing.startsWith(`${target}/`)) {
            found.delete(existing);
          }
        }
        continue;
      }
      if (wantedSet.has(canonical)) {
        found.set(canonical, bytes);
      }
    }
  }

  for (const path of wantedSet) {
    if (!found.has(path)) {
      fail(`missing packaged file ${path} in OCI archive ${ociTarPath}`);
    }
  }
  return found;
}

export function validateVersionTag(version) {
  if (typeof version !== 'string' || !VERSION_TAG_RE.test(version)) {
    fail(`invalid version tag: ${String(version)}`);
  }
  const lower = version.toLowerCase();
  if (lower === 'latest' || lower === 'unknown') {
    fail(`forbidden mutable or unknown version tag: ${version}`);
  }
  return version;
}

export function validateRegistryPrefix(registryPrefix) {
  if (typeof registryPrefix !== 'string') {
    fail('registryPrefix must be a string');
  }
  const trimmed = registryPrefix.replace(/\/+$/, '');
  if (
    !trimmed ||
    !REGISTRY_PREFIX_RE.test(trimmed) ||
    trimmed.includes('..') ||
    trimmed.includes('//')
  ) {
    fail(`invalid registry prefix: ${registryPrefix}`);
  }
  return trimmed;
}

export function parsePinnedImageReference(ref, roleLabel = 'service') {
  if (typeof ref !== 'string' || !ref.trim()) {
    fail(`missing image reference for ${roleLabel}`);
  }
  if (/(^|:)latest($|@)/i.test(ref)) {
    fail(`${roleLabel} image reference must not use :latest (${ref})`);
  }
  if (!ref.includes('@sha256:')) {
    fail(`${roleLabel} image reference must pin an immutable @sha256:<digest> (${ref})`);
  }
  const match = PINNED_IMAGE_REF_RE.exec(ref);
  if (!match) {
    fail(`invalid pinned image reference for ${roleLabel}: ${ref}`);
  }
  const [, repository, tag, manifestDigest] = match;
  validateVersionTag(tag);
  return { repository, tag, manifestDigest, imageRef: ref };
}

export function validateCandidateEvidence(evidence) {
  if (!isPlainObject(evidence)) {
    fail('candidate evidence must be a JSON object');
  }
  if (evidence.schemaVersion !== 1) {
    fail(`unsupported candidate evidence schemaVersion: ${String(evidence.schemaVersion)}`);
  }
  if (evidence.platform !== 'linux/amd64') {
    fail(`candidate evidence platform must be linux/amd64, got ${String(evidence.platform)}`);
  }
  const sourceCommit = evidence.sourceCommitClaim;
  if (typeof sourceCommit !== 'string' || !SOURCE_COMMIT_RE.test(sourceCommit)) {
    fail(`invalid candidate sourceCommitClaim: ${String(sourceCommit)}`);
  }
  if (!isPlainObject(evidence.officialCPA)) {
    fail('candidate evidence missing officialCPA section');
  }
  if (
    evidence.officialCPA.version !== EXPECTED_CPA_VERSION ||
    evidence.officialCPA.verifiedFinalImageExecutableSHA256 !== EXPECTED_CPA_SHA256
  ) {
    fail('candidate evidence officialCPA pin mismatch');
  }
  if (!isPlainObject(evidence.images)) {
    fail('candidate evidence missing images section');
  }
  const roleKeys = Object.keys(evidence.images).sort();
  if (JSON.stringify(roleKeys) !== JSON.stringify([...ROLES].sort())) {
    fail(`candidate evidence images must contain exactly ${ROLES.join(', ')}`);
  }

  const manifestDigests = new Set();
  const configDigests = new Set();

  for (const role of ROLES) {
    const record = evidence.images[role];
    if (!isPlainObject(record)) {
      fail(`candidate evidence missing image record for ${role}`);
    }
    if (record.platform !== 'linux/amd64') {
      fail(`${role} image platform must be linux/amd64`);
    }
    if (!SHA256_DIGEST_RE.test(record.platformManifestDigest ?? '')) {
      fail(`invalid platformManifestDigest for ${role}`);
    }
    if (!SHA256_DIGEST_RE.test(record.imageConfigDigest ?? '')) {
      fail(`invalid imageConfigDigest for ${role}`);
    }
    if (
      record.sourceCommitClaim !== undefined &&
      record.sourceCommitClaim !== sourceCommit
    ) {
      fail(`${role} sourceCommitClaim differs from candidate sourceCommitClaim`);
    }
    manifestDigests.add(record.platformManifestDigest);
    configDigests.add(record.imageConfigDigest);

    const files = record.packagedFiles;
    if (!isPlainObject(files)) {
      fail(`${role} missing packagedFiles map`);
    }
    const binaryPath = PACKAGED_PATHS.binaries[role];
    if (!SHA256_DIGEST_RE.test(files[binaryPath] ?? '')) {
      fail(`${role} missing or invalid binary digest for ${binaryPath}`);
    }
    if (!SHA256_DIGEST_RE.test(files[PACKAGED_PATHS.provenance] ?? '')) {
      fail(`${role} missing or invalid provenance digest for ${PACKAGED_PATHS.provenance}`);
    }

    if (role === 'manager') {
      const compiledClaim = record.goBinaryIdentity?.managerCompiledSourceClaim;
      if (compiledClaim !== sourceCommit) {
        fail(
          `Manager compiled source commit (${String(compiledClaim)}) differs from sourceCommitClaim (${sourceCommit})`
        );
      }
      if (!SHA256_DIGEST_RE.test(files[PACKAGED_PATHS.webManifest] ?? '')) {
        fail(`Manager missing packaged Full Web manifest digest (${PACKAGED_PATHS.webManifest})`);
      }
      if (!SHA256_DIGEST_RE.test(files[PACKAGED_PATHS.apiContract] ?? '')) {
        fail(`Manager missing packaged API contract digest (${PACKAGED_PATHS.apiContract})`);
      }
    }

    if (role === 'runtime') {
      if (files[PACKAGED_PATHS.cpaBinary] !== EXPECTED_CPA_ARTIFACT_ID) {
        fail('Runtime packaged CPA executable hash mismatch');
      }
    }
  }

  if (manifestDigests.size !== 3) {
    fail('manager, ingress and runtime must have three distinct platformManifestDigests');
  }
  if (configDigests.size !== 3) {
    fail('manager, ingress and runtime must have three distinct imageConfigDigests');
  }
  return evidence;
}

export function resolveFullWebAndApiMetadata(evidence, options = {}) {
  validateCandidateEvidence(evidence);
  const managerRecord = evidence.images.manager;
  const expectedWebSha = managerRecord.packagedFiles[PACKAGED_PATHS.webManifest];
  const expectedApiSha = managerRecord.packagedFiles[PACKAGED_PATHS.apiContract];

  const evidenceDir = options.evidenceDir ?? null;
  const managerArchivePath =
    options.managerOciPath ??
    (evidenceDir && typeof managerRecord.archive === 'string'
      ? resolve(evidenceDir, managerRecord.archive)
      : null);

  let archiveFiles = null;
  if (
    (!isPlainObject(managerRecord.fullWebSummary) || options.verifyOciArchive === true) &&
    managerArchivePath &&
    existsSync(managerArchivePath)
  ) {
    archiveFiles = extractFilesFromOciArchive(managerArchivePath, [
      PACKAGED_PATHS.webManifest,
      PACKAGED_PATHS.apiContract,
    ]);
  }

  let fullwebSummary = null;
  if (archiveFiles) {
    const rawWebManifest = archiveFiles.get(PACKAGED_PATHS.webManifest);
    const actualWebSha = sha256Digest(rawWebManifest);
    if (actualWebSha !== expectedWebSha) {
      fail(
        `Full Web manifest SHA256 in OCI archive (${actualWebSha}) differs from candidate evidence (${expectedWebSha})`
      );
    }
    const parsedWeb = JSON.parse(rawWebManifest.toString('utf8'));
    if (
      !isPlainObject(parsedWeb) ||
      parsedWeb.schemaVersion !== 1 ||
      !Array.isArray(parsedWeb.files) ||
      parsedWeb.files.length < 2
    ) {
      fail('invalid Full Web manifest structure in Manager OCI archive');
    }
    const indexEntry = parsedWeb.files.find(
      (item) => isPlainObject(item) && item.path === 'index.html'
    );
    if (!indexEntry || !SHA256_DIGEST_RE.test(indexEntry.sha256 ?? '')) {
      fail('Full Web manifest in Manager OCI archive is missing a valid index.html entry');
    }
    fullwebSummary = {
      manifest_sha256: actualWebSha,
      index_html_sha256: indexEntry.sha256,
      asset_count: parsedWeb.files.length,
    };
  }

  if (isPlainObject(managerRecord.fullWebSummary)) {
    const summary = managerRecord.fullWebSummary;
    if (
      summary.manifestSHA256 !== expectedWebSha ||
      !SHA256_DIGEST_RE.test(summary.indexHtmlSHA256 ?? '') ||
      !Number.isInteger(summary.assetCount) ||
      summary.assetCount < 2
    ) {
      fail('invalid manager.fullWebSummary in candidate evidence');
    }
    if (
      fullwebSummary &&
      (fullwebSummary.index_html_sha256 !== summary.indexHtmlSHA256 ||
        fullwebSummary.asset_count !== summary.assetCount)
    ) {
      fail('manager.fullWebSummary in candidate evidence differs from Manager OCI archive');
    }
    fullwebSummary = {
      manifest_sha256: summary.manifestSHA256,
      index_html_sha256: summary.indexHtmlSHA256,
      asset_count: summary.assetCount,
    };
  }

  if (!fullwebSummary && isPlainObject(options.fullwebManifest)) {
    const parsedWeb = options.fullwebManifest;
    const files = Array.isArray(parsedWeb.files) ? parsedWeb.files : [];
    const indexEntry = files.find((item) => isPlainObject(item) && item.path === 'index.html');
    if (!indexEntry || !SHA256_DIGEST_RE.test(indexEntry.sha256 ?? '') || files.length < 2) {
      fail('invalid fullwebManifest option');
    }
    fullwebSummary = {
      manifest_sha256: expectedWebSha,
      index_html_sha256: indexEntry.sha256,
      asset_count: files.length,
    };
  }

  if (!fullwebSummary) {
    fail(
      'cannot resolve Full Web index.html digest and asset count: provide manager.fullWebSummary in candidate-evidence.json or manager.oci.tar in evidence directory'
    );
  }

  // Resolve and verify API transport contract against checked source fixture and OCI archive
  const checkedApiContractPath =
    options.apiContractPath ?? resolve(repoRoot, 'tests/fixtures/r1-embedded-api-transport.json');
  const checkedApiBytes = options.apiContractBytes ?? readFileSync(checkedApiContractPath);
  const checkedApiSha = sha256Digest(checkedApiBytes);
  if (checkedApiSha !== expectedApiSha) {
    fail(
      `Manager packaged API contract digest (${expectedApiSha}) differs from checked source fixture (${checkedApiSha})`
    );
  }
  if (archiveFiles) {
    const archiveApiBytes = archiveFiles.get(PACKAGED_PATHS.apiContract);
    if (sha256Digest(archiveApiBytes) !== expectedApiSha) {
      fail('Manager OCI archive API contract bytes differ from candidate evidence');
    }
  }
  const parsedApi = JSON.parse(checkedApiBytes.toString('utf8'));
  if (
    !isPlainObject(parsedApi) ||
    parsedApi.schemaVersion !== 1 ||
    parsedApi.qualification !== EXPECTED_API_CONTRACT_QUALIFICATION ||
    parsedApi.apiSuffix !== EXPECTED_API_SUFFIX ||
    parsedApi.expectedPath !== EXPECTED_API_PATH
  ) {
    fail('unqualified or mismatched Web/API transport contract content');
  }
  if (isPlainObject(managerRecord.apiTransportContract)) {
    const evApi = managerRecord.apiTransportContract;
    if (
      evApi.sha256 !== expectedApiSha ||
      evApi.qualification !== EXPECTED_API_CONTRACT_QUALIFICATION ||
      evApi.apiSuffix !== EXPECTED_API_SUFFIX ||
      evApi.expectedPath !== EXPECTED_API_PATH
    ) {
      fail('manager.apiTransportContract in candidate evidence differs from verified contract');
    }
  }

  const apiContractSummary = {
    sha256: expectedApiSha,
    contract_id: parsedApi.contractId ?? EXPECTED_API_CONTRACT_ID,
    qualification: parsedApi.qualification,
    api_suffix: parsedApi.apiSuffix,
    expected_path: parsedApi.expectedPath,
  };

  return { fullweb: fullwebSummary, api_contract: apiContractSummary };
}

export function renderEmbeddedComposeYaml(imageRefs) {
  for (const role of ROLES) {
    parsePinnedImageReference(imageRefs?.[role], role);
  }
  const lines = [
    '# Immutable v2 Embedded three-image deployment compose.',
    '# Rendered from verified candidate evidence; every service image is digest-pinned.',
    'services:',
    '  cpamp-ingress:',
    `    image: '${imageRefs.ingress}'`,
    '    restart: unless-stopped',
    '    ports:',
    "      - '${CPAMP_PUBLIC_PORT:-18317}:18317'",
    '    environment:',
    "      CPAMP_INGRESS_ADDR: '0.0.0.0:18317'",
    "      CPAMP_MANAGER_URL: 'http://cpamp-manager:18317'",
    "      CPAMP_GATEWAY_URL: 'http://cpamp-runtime:8317'",
    "      CPAMP_INGRESS_CONTROL_ADDR: '0.0.0.0:9082'",
    '    depends_on:',
    '      - cpamp-manager',
    '      - cpamp-runtime',
    '',
    '  cpamp-manager:',
    `    image: '${imageRefs.manager}'`,
    '    restart: unless-stopped',
    '    expose:',
    "      - '18317'",
    '    environment:',
    "      HTTP_ADDR: '0.0.0.0:18317'",
    "      USAGE_DB_PATH: '/data/usage.sqlite'",
    "      CPA_MANAGER_DATA_KEY_PATH: '/data/data.key'",
    "      CPA_UPSTREAM_URL: 'http://cpamp-runtime:8317'",
    "      CPAMP_RUNTIME_URL: 'http://cpamp-runtime:9081'",
    "      CPAMP_RUNTIME_TOKEN_FILE: '/run/cpamp/runtime-secret/token'",
    "      CPAMP_INGRESS_CONTROL_URL: 'http://cpamp-ingress:9082'",
    "      CPAMP_PUBLIC_ORIGIN: '${CPAMP_PUBLIC_ORIGIN:-}'",
    "      USAGE_COLLECTOR_MODE: 'auto'",
    "      USAGE_RESP_QUEUE: 'usage'",
    "      USAGE_RESP_POP_SIDE: 'right'",
    "      USAGE_BATCH_SIZE: '100'",
    "      USAGE_POLL_INTERVAL_MS: '500'",
    "      USAGE_QUERY_LIMIT: '50000'",
    "      USAGE_CORS_ORIGINS: '*'",
    '    volumes:',
    '      - cpamp-manager-data:/data',
    '      - cpamp-runtime-transport-secret:/run/cpamp/runtime-secret:ro',
    '    healthcheck:',
    "      test: ['CMD', 'wget', '-qO-', 'http://127.0.0.1:18317/health']",
    '      interval: 10s',
    '      timeout: 3s',
    '      retries: 3',
    '',
    '  cpamp-runtime:',
    `    image: '${imageRefs.runtime}'`,
    '    restart: unless-stopped',
    '    expose:',
    "      - '8317'",
    "      - '9081'",
    '    environment:',
    "      CPAMP_RUNTIME_ADDR: '0.0.0.0:9081'",
    "      CPAMP_RUNTIME_IDENTITY: 'embedded-default'",
    "      CPAMP_RUNTIME_JOURNAL_PATH: '/runtime/supervisor/operations.sqlite'",
    "      CPAMP_RUNTIME_CPA_ADDR: '127.0.0.1:8317'",
    "      CPAMP_RUNTIME_CPA_UID: '10001'",
    "      CPAMP_RUNTIME_CPA_GID: '10001'",
    "      CPAMP_CPA_EXECUTABLE: '/usr/local/bin/cli-proxy-api'",
    "      CPAMP_RUNTIME_TOKEN_FILE: '/run/cpamp/runtime-secret/token'",
    '    volumes:',
    '      - cpamp-runtime-data:/runtime',
    '      - cpamp-runtime-transport-secret:/run/cpamp/runtime-secret',
    '',
    'volumes:',
    '  cpamp-manager-data:',
    '  cpamp-runtime-data:',
    '  cpamp-runtime-transport-secret:',
    '',
  ];
  return lines.join('\n');
}

const interpolateComposeVars = (value, env = {}) =>
  String(value).replace(/\$\{([A-Za-z_][A-Za-z0-9_]*):-([^}]*)\}/g, (_, name, fallback) =>
    env[name] !== undefined && env[name] !== '' ? String(env[name]) : fallback
  );

const ALLOWED_SERVICE_KEYS = new Set([
  'image',
  'restart',
  'ports',
  'expose',
  'environment',
  'depends_on',
  'volumes',
  'healthcheck',
  'privileged',
  'network_mode',
  'user',
]);

export function parseEmbeddedComposeText(composeText, env = {}) {
  if (typeof composeText !== 'string' || !composeText.trim()) {
    fail('compose YAML text must be a non-empty string');
  }
  if (composeText.includes('CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK')) {
    fail('compose YAML must not define CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK');
  }
  if (/:latest($|[\s'"`@])/im.test(composeText)) {
    fail('compose YAML must not reference :latest');
  }
  if (/^\s*build\s*:/m.test(composeText)) {
    fail('compose YAML must not contain local build blocks');
  }

  let rawDoc;
  try {
    rawDoc = yaml.parse(composeText, { uniqueKeys: true });
  } catch (error) {
    fail(`invalid compose YAML syntax: ${error.message}`);
  }
  if (!isPlainObject(rawDoc)) {
    fail('compose YAML root must be a mapping');
  }
  for (const topKey of Object.keys(rawDoc)) {
    if (topKey !== 'services' && topKey !== 'volumes') {
      fail(`unexpected top-level compose section: ${topKey}`);
    }
  }
  if (!isPlainObject(rawDoc.services)) {
    fail('compose YAML must define a services mapping');
  }
  if (!isPlainObject(rawDoc.volumes)) {
    fail('compose YAML must define a volumes mapping');
  }

  const volumes = {};
  for (const [volName, volConfig] of Object.entries(rawDoc.volumes)) {
    if (volConfig !== null && !isPlainObject(volConfig)) {
      fail(`invalid volume definition for ${volName}`);
    }
    volumes[volName] = volConfig ?? {};
  }

  const services = {};
  for (const [serviceName, rawService] of Object.entries(rawDoc.services)) {
    if (!isPlainObject(rawService)) {
      fail(`service ${serviceName} must be a mapping`);
    }
    if ('build' in rawService) {
      fail(`${serviceName} must not define a build block`);
    }
    for (const key of Object.keys(rawService)) {
      if (!ALLOWED_SERVICE_KEYS.has(key)) {
        fail(`unrecognized compose service property ${serviceName}.${key}`);
      }
    }

    const normalized = {};
    for (const [key, value] of Object.entries(rawService)) {
      if (key === 'ports') {
        if (!Array.isArray(value)) {
          fail(`${serviceName}.ports must be a sequence`);
        }
        normalized.ports = value.map((item) => {
          if (typeof item === 'string' || typeof item === 'number') {
            const resolvedPort = interpolateComposeVars(String(item), env);
            const parts = resolvedPort.split(':');
            if (
              parts.length !== 2 ||
              !parts[0] ||
              !parts[1] ||
              !Number.isInteger(Number(parts[1]))
            ) {
              fail(`invalid port mapping on ${serviceName}: ${String(item)}`);
            }
            return {
              mode: 'ingress',
              published: String(parts[0]),
              target: Number(parts[1]),
              protocol: 'tcp',
            };
          }
          if (isPlainObject(item)) {
            const published = interpolateComposeVars(String(item.published ?? ''), env);
            const target = Number(item.target);
            if (!published || !Number.isInteger(target)) {
              fail(`invalid port object on ${serviceName}`);
            }
            return {
              mode: item.mode ?? 'ingress',
              published,
              target,
              protocol: item.protocol ?? 'tcp',
            };
          }
          return fail(`invalid port entry on ${serviceName}`);
        });
      } else if (key === 'volumes') {
        if (!Array.isArray(value)) {
          fail(`${serviceName}.volumes must be a sequence`);
        }
        normalized.volumes = value.map((item) => {
          if (typeof item === 'string') {
            const parts = item.split(':');
            if (parts.length < 2 || parts.length > 3 || !parts[0] || !parts[1]) {
              fail(`invalid volume mount on ${serviceName}: ${item}`);
            }
            const mode = parts[2] ?? '';
            if (mode && mode !== 'ro' && mode !== 'rw') {
              fail(`unsupported volume mode on ${serviceName}: ${item}`);
            }
            return {
              type: 'volume',
              source: parts[0],
              target: parts[1],
              ...(mode === 'ro' ? { read_only: true } : {}),
            };
          }
          if (isPlainObject(item)) {
            if (typeof item.source !== 'string' || typeof item.target !== 'string') {
              fail(`invalid volume object on ${serviceName}`);
            }
            return {
              type: item.type ?? 'volume',
              source: item.source,
              target: item.target,
              ...(item.read_only ? { read_only: true } : {}),
            };
          }
          return fail(`invalid volume entry on ${serviceName}`);
        });
      } else if (key === 'environment') {
        if (isPlainObject(value)) {
          const envMap = {};
          for (const [envKey, envVal] of Object.entries(value)) {
            envMap[envKey] = interpolateComposeVars(String(envVal ?? ''), env);
          }
          normalized.environment = envMap;
        } else if (Array.isArray(value)) {
          const envMap = {};
          for (const entry of value) {
            if (typeof entry !== 'string' || !entry.includes('=')) {
              fail(`invalid environment entry on ${serviceName}: ${String(entry)}`);
            }
            const eqIdx = entry.indexOf('=');
            const envKey = entry.slice(0, eqIdx);
            const envVal = entry.slice(eqIdx + 1);
            envMap[envKey] = interpolateComposeVars(envVal, env);
          }
          normalized.environment = envMap;
        } else {
          fail(`${serviceName}.environment must be a mapping or sequence`);
        }
      } else {
        normalized[key] = value;
      }
    }
    services[serviceName] = normalized;
  }

  return { services, volumes };
}

export function validateRuntime12ComposeDocument(document, options = {}) {
  const services = document?.services ?? {};
  const serviceNames = Object.keys(services).sort();
  const expectedNames = Object.values(COMPOSE_SERVICES).sort();
  if (JSON.stringify(serviceNames) !== JSON.stringify(expectedNames)) {
    fail(
      `compose must define exact services ${expectedNames.join(', ')}, got ${serviceNames.join(', ')}`
    );
  }

  for (const [serviceName, serviceConfig] of Object.entries(services)) {
    if ('build' in serviceConfig) {
      fail(`${serviceName} must not contain a build block`);
    }
    parsePinnedImageReference(serviceConfig.image, serviceName);
  }

  if (options.validateComposeScript !== false) {
    const validatorPath = resolve(repoRoot, 'bin/ci/validate-runtime12-compose.mjs');
    try {
      execFileSync(process.execPath, [validatorPath], {
        cwd: repoRoot,
        input: JSON.stringify(document),
        encoding: 'utf8',
        stdio: ['pipe', 'pipe', 'pipe'],
      });
    } catch (error) {
      const stderr = error?.stderr ? String(error.stderr).trim() : '';
      fail(stderr || error.message);
    }
  }
  return document;
}

export function composeYamlToRuntime12ConfigJson(composeText, options = {}) {
  const document = parseEmbeddedComposeText(composeText, options.env);
  return validateRuntime12ComposeDocument(document, options);
}

export function verifyEmbeddedReleaseArtifacts({
  evidence,
  composeText,
  pairing,
  evidenceDir = null,
  apiContractPath = null,
  apiContractBytes = null,
  fullwebManifest = null,
  validateComposeScript = true,
}) {
  validateCandidateEvidence(evidence);
  if (!isPlainObject(pairing)) {
    fail('pairing record must be a JSON object');
  }
  if (pairing.schema_version !== 1) {
    fail(`unsupported pairing schema_version: ${String(pairing.schema_version)}`);
  }
  const version = validateVersionTag(pairing.version);
  if (pairing.source_commit !== evidence.sourceCommitClaim) {
    fail(
      `pairing source_commit (${String(pairing.source_commit)}) differs from candidate evidence (${evidence.sourceCommitClaim})`
    );
  }
  if (pairing.platform !== 'linux/amd64' || pairing.platform !== evidence.platform) {
    fail(`pairing platform must be linux/amd64, got ${String(pairing.platform)}`);
  }
  if (
    !isPlainObject(pairing.cpa) ||
    pairing.cpa.version !== EXPECTED_CPA_VERSION ||
    pairing.cpa.artifact_id !== EXPECTED_CPA_ARTIFACT_ID
  ) {
    fail('pairing CPA pin mismatch');
  }

  const expectedMeta = resolveFullWebAndApiMetadata(evidence, {
    evidenceDir,
    apiContractPath,
    apiContractBytes,
    fullwebManifest,
  });

  if (
    !isPlainObject(pairing.fullweb) ||
    pairing.fullweb.manifest_sha256 !== expectedMeta.fullweb.manifest_sha256 ||
    pairing.fullweb.index_html_sha256 !== expectedMeta.fullweb.index_html_sha256 ||
    pairing.fullweb.asset_count !== expectedMeta.fullweb.asset_count
  ) {
    fail('pairing fullweb manifest summary mismatch');
  }

  if (
    !isPlainObject(pairing.api_contract) ||
    pairing.api_contract.sha256 !== expectedMeta.api_contract.sha256 ||
    pairing.api_contract.qualification !== expectedMeta.api_contract.qualification ||
    pairing.api_contract.api_suffix !== expectedMeta.api_contract.api_suffix ||
    pairing.api_contract.expected_path !== expectedMeta.api_contract.expected_path
  ) {
    fail('pairing api_contract summary mismatch');
  }

  const composeBytes = Buffer.from(composeText, 'utf8');
  const expectedComposeSha = sha256Digest(composeBytes);
  if (
    !isPlainObject(pairing.compose) ||
    pairing.compose.filename !== COMPOSE_FILENAME ||
    pairing.compose.sha256 !== expectedComposeSha ||
    pairing.compose.size !== composeBytes.length
  ) {
    fail('pairing compose digest, filename or byte size mismatch');
  }

  const composeDoc = composeYamlToRuntime12ConfigJson(composeText, {
    validateComposeScript,
  });

  if (!isPlainObject(pairing.images)) {
    fail('pairing missing images map');
  }
  const pairingRoleKeys = Object.keys(pairing.images).sort();
  if (JSON.stringify(pairingRoleKeys) !== JSON.stringify([...ROLES].sort())) {
    fail(`pairing images must contain exactly ${ROLES.join(', ')}`);
  }

  const seenManifestDigests = new Set();
  for (const role of ROLES) {
    const imgEntry = pairing.images[role];
    if (!isPlainObject(imgEntry)) {
      fail(`pairing missing image record for ${role}`);
    }
    const evImg = evidence.images[role];
    if (typeof imgEntry.repository !== 'string' || !imgEntry.repository.includes('/')) {
      fail(`invalid repository for ${role} in pairing`);
    }
    if (imgEntry.tag !== version) {
      fail(`pairing image tag for ${role} (${String(imgEntry.tag)}) must match version (${version})`);
    }
    if (!SHA256_DIGEST_RE.test(imgEntry.manifest_digest ?? '')) {
      fail(`invalid manifest_digest for ${role} in pairing`);
    }
    if (imgEntry.manifest_digest !== evImg.platformManifestDigest) {
      fail(
        `manifest_digest for ${role} (${imgEntry.manifest_digest}) differs from candidate platformManifestDigest (${evImg.platformManifestDigest})`
      );
    }
    seenManifestDigests.add(imgEntry.manifest_digest);

    if (imgEntry.image_config_digest !== evImg.imageConfigDigest) {
      fail(`image_config_digest mismatch for ${role} in pairing`);
    }
    const expectedBinarySha = evImg.packagedFiles[PACKAGED_PATHS.binaries[role]];
    if (imgEntry.binary_sha256 !== expectedBinarySha) {
      fail(`binary_sha256 mismatch for ${role} in pairing`);
    }
    const expectedProvenanceSha = evImg.packagedFiles[PACKAGED_PATHS.provenance];
    if (imgEntry.release_provenance_sha256 !== expectedProvenanceSha) {
      fail(`release_provenance_sha256 mismatch for ${role} in pairing`);
    }

    const expectedImageRef = `${imgEntry.repository}:${imgEntry.tag}@${imgEntry.manifest_digest}`;
    if (imgEntry.image_ref !== undefined && imgEntry.image_ref !== expectedImageRef) {
      fail(`image_ref mismatch for ${role} in pairing`);
    }
    const serviceName = COMPOSE_SERVICES[role];
    const actualServiceImage = composeDoc.services[serviceName]?.image;
    if (actualServiceImage !== expectedImageRef) {
      fail(
        `compose service ${serviceName} image (${String(actualServiceImage)}) does not match pairing (${expectedImageRef})`
      );
    }
  }

  if (seenManifestDigests.size !== 3) {
    fail('pairing images must have three distinct manifest digests');
  }

  return { composeDoc, pairing };
}

export function renderEmbeddedReleaseArtifacts(evidence, options = {}) {
  validateCandidateEvidence(evidence);
  const version = validateVersionTag(options.version ?? evidence.candidateVersionClaim);
  const registryPrefix = validateRegistryPrefix(
    options.registryPrefix ?? DEFAULT_REGISTRY_PREFIX
  );
  const resolvedImages = {};
  const imageRefs = {};

  for (const role of ROLES) {
    const evImg = evidence.images[role];
    let repository = `${registryPrefix}/${DEFAULT_REPOSITORIES[role]}`;
    let tag = version;
    let manifestDigest = evImg.platformManifestDigest;

    if (options.imageRefs?.[role] !== undefined) {
      const parsed = parsePinnedImageReference(options.imageRefs[role], role);
      repository = parsed.repository;
      tag = parsed.tag;
      manifestDigest = parsed.manifestDigest;
      if (tag !== version) {
        fail(
          `${role} image reference tag (${tag}) differs from target release version (${version})`
        );
      }
    }

    if (options.digests?.[role] !== undefined) {
      const explicitDigest = options.digests[role];
      if (typeof explicitDigest !== 'string' || !SHA256_DIGEST_RE.test(explicitDigest)) {
        fail(`invalid digest for ${role}: ${String(explicitDigest)}`);
      }
      manifestDigest = explicitDigest;
    }

    if (manifestDigest !== evImg.platformManifestDigest) {
      fail(
        `manifest digest mismatch for ${role}: expected ${evImg.platformManifestDigest}, got ${manifestDigest}`
      );
    }

    const imageRef = `${repository}:${tag}@${manifestDigest}`;
    imageRefs[role] = imageRef;
    resolvedImages[role] = {
      repository,
      tag,
      image_ref: imageRef,
      manifest_digest: manifestDigest,
      image_config_digest: evImg.imageConfigDigest,
      binary_sha256: evImg.packagedFiles[PACKAGED_PATHS.binaries[role]],
      release_provenance_sha256: evImg.packagedFiles[PACKAGED_PATHS.provenance],
    };
  }

  const { fullweb, api_contract } = resolveFullWebAndApiMetadata(evidence, options);
  const composeYaml = renderEmbeddedComposeYaml(imageRefs);
  const composeBytes = Buffer.from(composeYaml, 'utf8');

  const pairing = {
    schema_version: 1,
    version,
    source_commit: evidence.sourceCommitClaim,
    platform: 'linux/amd64',
    cpa: {
      version: EXPECTED_CPA_VERSION,
      artifact_id: EXPECTED_CPA_ARTIFACT_ID,
    },
    fullweb,
    api_contract,
    compose: {
      filename: COMPOSE_FILENAME,
      sha256: sha256Digest(composeBytes),
      size: composeBytes.length,
    },
    images: resolvedImages,
  };

  const { composeDoc } = verifyEmbeddedReleaseArtifacts({
    evidence,
    composeText: composeYaml,
    pairing,
    evidenceDir: options.evidenceDir ?? null,
    apiContractPath: options.apiContractPath ?? null,
    apiContractBytes: options.apiContractBytes ?? null,
    fullwebManifest: options.fullwebManifest ?? null,
    validateComposeScript: options.validateComposeScript !== false,
  });

  return {
    composeYaml,
    composeJson: composeDoc,
    pairing,
  };
}

function parseCliArgs(argv) {
  const args = {
    verify: false,
    digests: {},
    imageRefs: {},
  };
  for (let i = 0; i < argv.length; i += 1) {
    const token = argv[i];
    const next = () => {
      if (i + 1 >= argv.length) fail(`missing value for ${token}`);
      i += 1;
      return argv[i];
    };
    switch (token) {
      case '--verify':
        args.verify = true;
        break;
      case '--evidence':
        args.evidencePath = next();
        break;
      case '--out-dir':
        args.outDir = next();
        break;
      case '--compose':
      case '--compose-out':
        args.composePath = next();
        break;
      case '--pairing':
      case '--pairing-out':
        args.pairingPath = next();
        break;
      case '--compose-json-out':
        args.composeJsonPath = next();
        break;
      case '--version':
        args.version = next();
        break;
      case '--registry-prefix':
        args.registryPrefix = next();
        break;
      case '--manager-digest':
        args.digests.manager = next();
        break;
      case '--ingress-digest':
        args.digests.ingress = next();
        break;
      case '--runtime-digest':
        args.digests.runtime = next();
        break;
      case '--manager-image':
        args.imageRefs.manager = next();
        break;
      case '--ingress-image':
        args.imageRefs.ingress = next();
        break;
      case '--runtime-image':
        args.imageRefs.runtime = next();
        break;
      default:
        fail(`unknown argument: ${token}`);
    }
  }
  if (!args.evidencePath) {
    fail('--evidence <candidate-evidence.json> is required');
  }
  const baseOutDir = args.outDir ? resolve(args.outDir) : dirname(resolve(args.evidencePath));
  if (!args.composePath) {
    args.composePath = resolve(baseOutDir, COMPOSE_FILENAME);
  } else {
    args.composePath = resolve(args.composePath);
  }
  if (!args.pairingPath) {
    args.pairingPath = resolve(baseOutDir, PAIRING_FILENAME);
  } else {
    args.pairingPath = resolve(args.pairingPath);
  }
  if (args.composeJsonPath) {
    args.composeJsonPath = resolve(args.composeJsonPath);
  }
  return args;
}

if (process.argv[1] && import.meta.url === pathToFileURL(realpathSync(process.argv[1])).href) {
  try {
    const cli = parseCliArgs(process.argv.slice(2));
    const evidenceFile = resolve(cli.evidencePath);
    const evidence = JSON.parse(readFileSync(evidenceFile, 'utf8'));
    const evidenceDir = dirname(evidenceFile);

    if (cli.verify) {
      const composeText = readFileSync(cli.composePath, 'utf8');
      const pairing = JSON.parse(readFileSync(cli.pairingPath, 'utf8'));
      const { composeDoc } = verifyEmbeddedReleaseArtifacts({
        evidence,
        composeText,
        pairing,
        evidenceDir,
      });
      if (cli.composeJsonPath) {
        mkdirSync(dirname(cli.composeJsonPath), { recursive: true });
        writeFileSync(cli.composeJsonPath, JSON.stringify(composeDoc, null, 2) + '\n', 'utf8');
      }
      console.log(
        `Embedded compose/pairing verification passed: ${cli.composePath} + ${cli.pairingPath}`
      );
    } else {
      const { composeYaml, composeJson, pairing } = renderEmbeddedReleaseArtifacts(evidence, {
        version: cli.version,
        registryPrefix: cli.registryPrefix,
        digests: Object.keys(cli.digests).length ? cli.digests : undefined,
        imageRefs: Object.keys(cli.imageRefs).length ? cli.imageRefs : undefined,
        evidenceDir,
      });
      mkdirSync(dirname(cli.composePath), { recursive: true });
      mkdirSync(dirname(cli.pairingPath), { recursive: true });
      writeFileSync(cli.composePath, composeYaml, 'utf8');
      writeFileSync(cli.pairingPath, JSON.stringify(pairing, null, 2) + '\n', 'utf8');
      if (cli.composeJsonPath) {
        mkdirSync(dirname(cli.composeJsonPath), { recursive: true });
        writeFileSync(cli.composeJsonPath, JSON.stringify(composeJson, null, 2) + '\n', 'utf8');
      }
      console.log(
        `Rendered and verified immutable Embedded compose and pairing: ${cli.composePath} + ${cli.pairingPath}`
      );
    }
  } catch (error) {
    console.error(`STOP: ${error.message}`);
    process.exit(1);
  }
}
