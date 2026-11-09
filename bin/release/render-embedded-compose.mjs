import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { existsSync, mkdirSync, readFileSync, realpathSync, writeFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { delimiter, dirname, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { gunzipSync } from 'node:zlib';

const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '..', '..');

function loadYamlModule() {
  const candidates = [
    import.meta.url,
    pathToFileURL(resolve(repoRoot, 'package.json')).href,
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

export const ROLES = Object.freeze(['slim', 'full']);
export const COMPOSE_SERVICES = Object.freeze({
  slim: 'cpa-manager-plus',
  full: 'cpa-manager-plus',
});
export const DEFAULT_REPOSITORIES = Object.freeze({
  slim: 'cpa-manager-plus',
  full: 'cpa-manager-plus',
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
    launcher: '/usr/local/bin/cpamp-launcher',
    manager: '/usr/local/bin/cpa-manager-plus',
    ingress: '/usr/local/bin/cpamp-ingress',
    'runtime-supervisor': '/usr/local/bin/cpamp-runtime-supervisor',
  }),
  provenance: '/usr/local/share/cpamp/provenance.json',
  webManifest: '/usr/local/share/cpamp/release/fullweb-manifest.json',
  apiContract: '/usr/local/share/cpamp/release/public-api-transport-contract.json',
  cpaBinary: '/usr/local/bin/cli-proxy-api',
  cpaArtifact: '/usr/local/share/cpamp/runtime/cpa-artifact.json',
  cpaLicense: '/usr/local/share/licenses/cli-proxy-api/LICENSE',
  seedConfig: '/usr/local/share/cpamp/runtime/config.seed.yaml',
});

const SHA256_DIGEST_RE = /^sha256:[0-9a-f]{64}$/;
const SOURCE_COMMIT_RE = /^[0-9a-f]{40}$/;
const VERSION_TAG_RE = /^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$/;
const REGISTRY_PREFIX_RE = /^[A-Za-z0-9][A-Za-z0-9._/-]{0,127}$/;
const PINNED_IMAGE_REF_RE =
  /^([A-Za-z0-9][A-Za-z0-9._/-]*\/[A-Za-z0-9._-]+):([A-Za-z0-9][A-Za-z0-9._+-]{0,127})@(sha256:[0-9a-f]{64})$/;

// repoRoot is defined above before loadYamlModule

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

export const CANONICAL_INGRESS_PORT_SPEC = '${CPAMP_PUBLIC_PORT:-18317}:18317';
const EXPECTED_TOP_VOLUMES = Object.freeze(['cpamp-data']);
const EXPECTED_SERVICE_MOUNTS = Object.freeze({
  'cpa-manager-plus': Object.freeze(['volume:cpamp-data:/data:rw']),
});

export function extractFilesFromOciArchive(ociTarPath, wantedPaths, options = {}) {
  if (!existsSync(ociTarPath)) {
    fail(`missing OCI archive: ${ociTarPath}`);
  }
  const rawArchive = readFileSync(ociTarPath);
  const archiveSHA256 = sha256Digest(rawArchive);
  const outerEntries = parseTarEntries(rawArchive);
  const layoutBytes = outerEntries.get('oci-layout');
  if (!layoutBytes) fail(`missing oci-layout in OCI archive: ${ociTarPath}`);
  const layout = JSON.parse(layoutBytes.toString('utf8'));
  if (layout?.imageLayoutVersion !== '1.0.0') {
    fail(`invalid oci-layout version in OCI archive: ${ociTarPath}`);
  }
  const indexBytes = outerEntries.get('index.json');
  if (!indexBytes) fail(`missing index.json in OCI archive: ${ociTarPath}`);
  const indexDigest = sha256Digest(indexBytes);
  const index = JSON.parse(indexBytes.toString('utf8'));
  const descriptors = Array.isArray(index.manifests) ? index.manifests : [];
  const linuxAmd64 = descriptors.filter(
    (m) =>
      isPlainObject(m) &&
      isPlainObject(m.platform) &&
      m.platform.os === 'linux' &&
      m.platform.architecture === 'amd64'
  );
  if (descriptors.length !== 1 || linuxAmd64.length !== 1) {
    fail(`OCI archive must hold exactly one linux/amd64 manifest: ${ociTarPath}`);
  }
  const manifestDigest = linuxAmd64[0]?.digest;
  if (!SHA256_DIGEST_RE.test(manifestDigest ?? '')) {
    fail(`invalid manifest digest in OCI archive: ${ociTarPath}`);
  }
  const manifestBlob = outerEntries.get(`blobs/sha256/${manifestDigest.slice(7)}`);
  if (!manifestBlob || sha256Digest(manifestBlob) !== manifestDigest) {
    fail(`manifest blob missing or corrupt in OCI archive: ${ociTarPath}`);
  }
  const manifest = JSON.parse(manifestBlob.toString('utf8'));
  const configDigest = manifest?.config?.digest;
  if (!SHA256_DIGEST_RE.test(configDigest ?? '')) {
    fail(`invalid config digest in OCI archive: ${ociTarPath}`);
  }
  const configBlob = outerEntries.get(`blobs/sha256/${configDigest.slice(7)}`);
  if (!configBlob || sha256Digest(configBlob) !== configDigest) {
    fail(`config blob missing or corrupt in OCI archive: ${ociTarPath}`);
  }
  const config = JSON.parse(configBlob.toString('utf8'));
  if (config?.os !== 'linux' || config?.architecture !== 'amd64') {
    fail(`OCI image config platform mismatch in ${ociTarPath}`);
  }
  const layers = Array.isArray(manifest.layers) ? manifest.layers : [];
  const diffIds = config?.rootfs?.diff_ids;
  if (!Array.isArray(diffIds) || diffIds.length !== layers.length) {
    fail(`OCI image config rootfs.diff_ids mismatch in ${ociTarPath}`);
  }
  const wantedSet = new Set(wantedPaths.map((p) => (p.startsWith('/') ? p : `/${p}`)));
  const forbiddenSet = new Set(
    (options.forbiddenPaths ?? []).map((p) => (p.startsWith('/') ? p : `/${p}`))
  );
  const found = new Map();

  for (let idx = 0; idx < layers.length; idx += 1) {
    const layerDescriptor = layers[idx];
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
    if (sha256Digest(uncompressed) !== diffIds[idx]) {
      fail(`OCI layer diff_id mismatch at layer ${idx} in ${ociTarPath}`);
    }
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
      if (wantedSet.has(canonical) || forbiddenSet.has(canonical)) {
        found.set(canonical, bytes);
      }
    }
  }

  for (const path of forbiddenSet) {
    if (found.has(path)) {
      fail(`unexpected packaged file ${path} in OCI archive ${ociTarPath}`);
    }
  }
  for (const path of wantedSet) {
    if (!found.has(path)) {
      fail(`missing packaged file ${path} in OCI archive ${ociTarPath}`);
    }
  }
  if (options.returnMetadata) {
    return {
      archiveSHA256,
      indexDigest,
      manifestDigest,
      configDigest,
      config,
      files: found,
    };
  }
  return found;
}

export function verifyOciArchiveAgainstRoleEvidence(ociTarPath, role, evidence) {
  const evImg = evidence.images[role];
  const cpaBundled = role === 'full';
  const wanted = [
    ...Object.values(PACKAGED_PATHS.binaries),
    PACKAGED_PATHS.provenance,
    PACKAGED_PATHS.webManifest,
    PACKAGED_PATHS.apiContract,
    PACKAGED_PATHS.cpaArtifact,
    PACKAGED_PATHS.seedConfig,
  ];
  const forbiddenPaths = [];
  if (cpaBundled) {
    wanted.push(PACKAGED_PATHS.cpaBinary, PACKAGED_PATHS.cpaLicense);
  } else {
    forbiddenPaths.push(PACKAGED_PATHS.cpaBinary, PACKAGED_PATHS.cpaLicense);
  }
  const inspected = extractFilesFromOciArchive(ociTarPath, wanted, {
    returnMetadata: true,
    forbiddenPaths,
  });
  if (evImg.archiveSHA256 && inspected.archiveSHA256 !== evImg.archiveSHA256) {
    fail(
      `OCI archive SHA256 mismatch for ${role}: expected ${evImg.archiveSHA256}, got ${inspected.archiveSHA256}`
    );
  }
  if (evImg.ociLayoutIndexDigest && inspected.indexDigest !== evImg.ociLayoutIndexDigest) {
    fail(
      `OCI index digest mismatch for ${role}: expected ${evImg.ociLayoutIndexDigest}, got ${inspected.indexDigest}`
    );
  }
  if (inspected.manifestDigest !== evImg.platformManifestDigest) {
    fail(
      `OCI archive platformManifestDigest for ${role} (${inspected.manifestDigest}) differs from candidate evidence (${evImg.platformManifestDigest})`
    );
  }
  if (inspected.configDigest !== evImg.imageConfigDigest) {
    fail(
      `OCI archive imageConfigDigest for ${role} (${inspected.configDigest}) differs from candidate evidence (${evImg.imageConfigDigest})`
    );
  }
  const labels = inspected.config?.config?.Labels ?? {};
  if (labels['org.opencontainers.image.revision'] !== evidence.sourceCommitClaim) {
    fail(
      `OCI archive revision label for ${role} (${String(labels['org.opencontainers.image.revision'])}) differs from candidate sourceCommitClaim (${evidence.sourceCommitClaim})`
    );
  }
  if (labels['io.cpamp.candidate.role'] !== role) {
    fail(`OCI archive role label mismatch for ${role}: ${String(labels['io.cpamp.candidate.role'])}`);
  }
  if (
    evidence.candidateBuildEventClaim &&
    labels['io.cpamp.candidate.event'] !== evidence.candidateBuildEventClaim
  ) {
    fail(`OCI archive build event label mismatch for ${role}`);
  }
  for (const binaryPath of Object.values(PACKAGED_PATHS.binaries)) {
    const actualBinarySha = sha256Digest(inspected.files.get(binaryPath));
    if (actualBinarySha !== evImg.packagedFiles[binaryPath]) {
      fail(`OCI archive binary digest mismatch for ${role} (${binaryPath})`);
    }
  }
  const provBytes = inspected.files.get(PACKAGED_PATHS.provenance);
  if (sha256Digest(provBytes) !== evImg.packagedFiles[PACKAGED_PATHS.provenance]) {
    fail(`OCI archive provenance digest mismatch for ${role}`);
  }
  const prov = JSON.parse(provBytes.toString('utf8'));
  if (
    prov?.sourceCommit !== evidence.sourceCommitClaim ||
    prov?.role !== 'cpamp' ||
    Boolean(prov?.cpaBundled ?? true) !== cpaBundled
  ) {
    fail(
      `OCI archive provenance sourceCommit/role mismatch for ${role}: ${String(prov?.sourceCommit)}`
    );
  }
  const cpaArtifactBytes = inspected.files.get(PACKAGED_PATHS.cpaArtifact);
  const actualCpaArtifactDocSha = sha256Digest(cpaArtifactBytes);
  const expectedCpaArtifactDocSha = evImg.packagedFiles[PACKAGED_PATHS.cpaArtifact];
  if (
    !SHA256_DIGEST_RE.test(expectedCpaArtifactDocSha ?? '') ||
    actualCpaArtifactDocSha !== expectedCpaArtifactDocSha
  ) {
    fail(`OCI archive CPA artifact manifest digest mismatch against evidence for ${role}`);
  }
  const parsedCpaArtifact = JSON.parse(cpaArtifactBytes.toString('utf8'));
  if (cpaBundled) {
    const cpaBinaryBytes = inspected.files.get(PACKAGED_PATHS.cpaBinary);
    const actualCpaSha = sha256Digest(cpaBinaryBytes);
    if (
      actualCpaSha !== EXPECTED_CPA_ARTIFACT_ID ||
      actualCpaSha !== evImg.packagedFiles[PACKAGED_PATHS.cpaBinary]
    ) {
      fail(`OCI archive official CPA v8.0.55 executable hash mismatch for ${role}`);
    }
    assertCanonicalCpaArtifact(
      parsedCpaArtifact,
      EXPECTED_CPA_ARTIFACT_ID,
      actualCpaSha
    );
  } else {
    assertCanonicalCpaArtifact(
      parsedCpaArtifact,
      parsedCpaArtifact?.artifactId,
      parsedCpaArtifact?.artifactId
    );
  }
  return inspected;
}

const CPA_ARTIFACT_KEYS = ['artifactId', 'engine', 'schemaVersion', 'version'];

// Mirrors verify-embedded-candidate.py: the runtime image's cpa-artifact.json
// must be exactly the four-field document the candidate build writes, so both
// verifiers reach the same verdict on the same archive.
export function assertCanonicalCpaArtifact(cpaArtifact, expectedCpaArtifactId, actualCpaSha) {
  if (
    !isPlainObject(cpaArtifact) ||
    Object.keys(cpaArtifact).sort().join(',') !== CPA_ARTIFACT_KEYS.join(',') ||
    cpaArtifact.schemaVersion !== 1 ||
    cpaArtifact.engine !== 'cpa' ||
    cpaArtifact.version !== EXPECTED_CPA_VERSION ||
    cpaArtifact.artifactId !== expectedCpaArtifactId ||
    cpaArtifact.artifactId !== actualCpaSha
  ) {
    fail('OCI archive CPA artifact manifest mismatch for runtime');
  }
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
    if (record.archive !== `${role}.oci.tar`) {
      fail(
        `invalid or missing archive filename for ${role} in candidate evidence: expected ${role}.oci.tar, got ${String(record.archive)}`
      );
    }
    if (!SHA256_DIGEST_RE.test(record.archiveSHA256 ?? '')) {
      fail(`invalid or missing archiveSHA256 for ${role} in candidate evidence`);
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
    for (const binaryPath of Object.values(PACKAGED_PATHS.binaries)) {
      if (!SHA256_DIGEST_RE.test(files[binaryPath] ?? '')) {
        fail(`${role} missing or invalid binary digest for ${binaryPath}`);
      }
    }
    if (!SHA256_DIGEST_RE.test(files[PACKAGED_PATHS.provenance] ?? '')) {
      fail(`${role} missing or invalid provenance digest for ${PACKAGED_PATHS.provenance}`);
    }
    const compiledClaim = record.goBinaryIdentity?.managerCompiledSourceClaim;
    if (compiledClaim !== sourceCommit) {
      fail(
        `Manager compiled source commit (${String(compiledClaim)}) differs from sourceCommitClaim (${sourceCommit})`
      );
    }
    if (!SHA256_DIGEST_RE.test(files[PACKAGED_PATHS.webManifest] ?? '')) {
      fail(`${role} missing packaged Full Web manifest digest (${PACKAGED_PATHS.webManifest})`);
    }
    if (!SHA256_DIGEST_RE.test(files[PACKAGED_PATHS.apiContract] ?? '')) {
      fail(`${role} missing packaged API contract digest (${PACKAGED_PATHS.apiContract})`);
    }
    if (!SHA256_DIGEST_RE.test(files[PACKAGED_PATHS.cpaArtifact] ?? '')) {
      fail(
        `${role} missing packaged CPA artifact manifest digest (${PACKAGED_PATHS.cpaArtifact})`
      );
    }
    if (role === 'full') {
      if (files[PACKAGED_PATHS.cpaBinary] !== EXPECTED_CPA_ARTIFACT_ID) {
        fail('Full packaged CPA executable hash mismatch');
      }
    } else if (files[PACKAGED_PATHS.cpaBinary] !== undefined) {
      fail('Slim variant must not record a packaged CPA executable hash');
    }
  }

  if (manifestDigests.size !== ROLES.length) {
    fail('slim and full must have distinct platformManifestDigests');
  }
  if (configDigests.size !== ROLES.length) {
    fail('slim and full must have distinct imageConfigDigests');
  }
  return evidence;
}

export function resolveFullWebAndApiMetadata(evidence, options = {}) {
  validateCandidateEvidence(evidence);
  const fullRecord = evidence.images.full;
  const expectedWebSha = fullRecord.packagedFiles[PACKAGED_PATHS.webManifest];
  const expectedApiSha = fullRecord.packagedFiles[PACKAGED_PATHS.apiContract];

  const evidenceDir = options.evidenceDir ?? null;
  const roleOciPaths = options.roleOciPaths ?? {};
  const inspectedRoles = {};
  const resolvedRolePaths = {};
  for (const role of ROLES) {
    const expectedArchiveName = `${role}.oci.tar`;
    if (evidence.images[role].archive !== expectedArchiveName) {
      fail(
        `invalid or missing archive filename for ${role}: expected ${expectedArchiveName}, got ${String(evidence.images[role].archive)}`
      );
    }
    const explicitRolePath =
      roleOciPaths[role] ?? (role === 'full' ? options.fullOciPath : null);
    const candidateRolePath =
      explicitRolePath ??
      (evidenceDir ? resolve(evidenceDir, expectedArchiveName) : null);
    if (!candidateRolePath || !existsSync(candidateRolePath)) {
      fail(
        `missing required OCI archive for ${role}: ${String(candidateRolePath ?? expectedArchiveName)}`
      );
    }
    resolvedRolePaths[role] = candidateRolePath;
  }
  for (const role of ROLES) {
    inspectedRoles[role] = verifyOciArchiveAgainstRoleEvidence(
      resolvedRolePaths[role],
      role,
      evidence
    );
  }
  const archiveFiles = inspectedRoles.full.files;
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

  if (isPlainObject(fullRecord.fullWebSummary)) {
    const summary = fullRecord.fullWebSummary;
    if (
      summary.manifestSHA256 !== expectedWebSha ||
      !SHA256_DIGEST_RE.test(summary.indexHtmlSHA256 ?? '') ||
      !Number.isInteger(summary.assetCount) ||
      summary.assetCount < 2
    ) {
      fail('invalid full.fullWebSummary in candidate evidence');
    }
    if (
      fullwebSummary &&
      (fullwebSummary.index_html_sha256 !== summary.indexHtmlSHA256 ||
        fullwebSummary.asset_count !== summary.assetCount)
    ) {
      fail('full.fullWebSummary in candidate evidence differs from Full OCI archive');
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
      'cannot resolve Full Web index.html digest and asset count: provide full.fullWebSummary in candidate-evidence.json or full.oci.tar in evidence directory'
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
  if (isPlainObject(fullRecord.apiTransportContract)) {
    const evApi = fullRecord.apiTransportContract;
    if (
      evApi.sha256 !== expectedApiSha ||
      (evApi.contractId !== undefined && evApi.contractId !== EXPECTED_API_CONTRACT_ID) ||
      evApi.qualification !== EXPECTED_API_CONTRACT_QUALIFICATION ||
      evApi.apiSuffix !== EXPECTED_API_SUFFIX ||
      evApi.expectedPath !== EXPECTED_API_PATH
    ) {
      fail('full.apiTransportContract in candidate evidence differs from verified contract');
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

// Pure Compose YAML template renderer.
// Static port contract: only cpamp-ingress defines '${CPAMP_PUBLIC_PORT:-18317}:18317'
// (container target 18317/tcp, no other service or port mappings); the deployer may
// set CPAMP_PUBLIC_PORT=[HOST_IP:]PORT at deployment time to choose host bind IP/port.
export function renderEmbeddedComposeYaml(imageRefs) {
  const fullRef =
    typeof imageRefs === 'string'
      ? imageRefs
      : (imageRefs?.['cpa-manager-plus'] ?? imageRefs?.full);
  parsePinnedImageReference(fullRef, 'full');
  for (const role of ROLES) {
    if (imageRefs?.[role] !== undefined) {
      parsePinnedImageReference(imageRefs[role], role);
    }
  }
  const lines = [
    '# Immutable v2 Embedded single-image deployment compose.',
    '# Rendered from verified candidate evidence; service image is digest-pinned.',
    'name: cpa-manager-plus',
    'services:',
    '  cpa-manager-plus:',
    `    image: '${fullRef}'`,
    '    restart: unless-stopped',
    '    stop_grace_period: 150s',
    '    ports:',
    "      - '${CPAMP_PUBLIC_PORT:-18317}:18317'",
    '    environment:',
    "      CPAMP_PUBLIC_ORIGIN: '${CPAMP_PUBLIC_ORIGIN:-}'",
    "      CPAMP_DRAIN_TIMEOUT: '${CPAMP_DRAIN_TIMEOUT:-90s}'",
    '    volumes:',
    '      - cpamp-data:/data',
    '',
    'volumes:',
    '  cpamp-data:',
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
  'stop_grace_period',
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

const ALLOWED_VOLUME_OBJECT_KEYS = new Set(['type', 'source', 'target', 'read_only', 'volume']);
const ALLOWED_PORT_OBJECT_KEYS = new Set(['mode', 'published', 'target', 'protocol']);

const DEFAULT_TEMPLATE_ENV = Object.freeze({});

export function parseEmbeddedComposeText(composeText) {
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
    if (topKey !== 'name' && topKey !== 'services' && topKey !== 'volumes') {
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
    if (
      volConfig !== null &&
      (!isPlainObject(volConfig) || Object.keys(volConfig).length > 0)
    ) {
      fail(
        `top-level volume ${volName} must be a canonical empty definition without overrides (name/external/driver/driver_opts/labels are forbidden): ${JSON.stringify(volConfig)}`
      );
    }
    volumes[volName] = {};
  }

  const services = {};
  for (const [serviceName, rawService] of Object.entries(rawDoc.services)) {
    if (!isPlainObject(rawService)) {
      fail(`service ${serviceName} must be a mapping`);
    }
    if ('build' in rawService) {
      fail(`${serviceName} must not define a build block`);
    }
    if ('pid' in rawService && rawService.pid !== undefined && rawService.pid !== null && String(rawService.pid).trim() !== '') {
      fail(`${serviceName} must not set pid (${JSON.stringify(rawService.pid)}); cpamp-launcher requires a private container PID namespace`);
    }
    if ('init' in rawService) {
      if (typeof rawService.init !== 'boolean') {
        fail(`${serviceName}.init must be a boolean`);
      }
      if (rawService.init === true) {
        fail(`${serviceName} must not set init: true; cpamp-launcher must run as PID 1`);
      }
    }
    for (const key of Object.keys(rawService)) {
      if (!ALLOWED_SERVICE_KEYS.has(key)) {
        fail(`unrecognized compose service property ${serviceName}.${key}`);
      }
    }
    if ('privileged' in rawService) {
      if (typeof rawService.privileged !== 'boolean') {
        fail(
          `${serviceName}.privileged must be a boolean, got ${typeof rawService.privileged} (${JSON.stringify(rawService.privileged)})`
        );
      }
      if (rawService.privileged === true) {
        fail(`${serviceName} must not run privileged`);
      }
    }

    const normalized = {};
    for (const [key, value] of Object.entries(rawService)) {
      if (key === 'ports') {
        if (!Array.isArray(value)) {
          fail(`${serviceName}.ports must be a sequence`);
        }
        if (serviceName !== 'cpa-manager-plus') {
          if (value.length > 0) {
            fail(
              `Runtime 13 Compose validation failed: expected only cpa-manager-plus 18317:18317, but ${serviceName} defines ports`
            );
          }
          normalized.ports = [];
          continue;
        }
        if (value.length !== 1) {
          fail(
            `Runtime 13 Compose validation failed: expected only cpa-manager-plus 18317:18317, got ${JSON.stringify(value)}`
          );
        }
        const rawPortItem = value[0];
        if (typeof rawPortItem !== 'string') {
          fail(
            `cpa-manager-plus.ports must use canonical short syntax '${CANONICAL_INGRESS_PORT_SPEC}'; long-syntax or non-string port definitions are forbidden: ${JSON.stringify(rawPortItem)}`
          );
        }
        if (rawPortItem !== CANONICAL_INGRESS_PORT_SPEC) {
          fail(
            `Runtime 13 Compose validation failed: expected only cpa-manager-plus 18317:18317 via canonical '${CANONICAL_INGRESS_PORT_SPEC}', got ${JSON.stringify(rawPortItem)}`
          );
        }
        // Static template '${CPAMP_PUBLIC_PORT:-18317}:18317' verified above:
        // guarantees container target 18317/tcp and no other service/port mappings.
        // Host bind address/port (CPAMP_PUBLIC_PORT=[HOST_IP:]PORT) are the deployer's
        // choice at deployment time; the verifier never reads process.env.
        normalized.ports = [
          {
            mode: 'ingress',
            published: '18317',
            target: 18317,
            protocol: 'tcp',
          },
        ];
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
            for (const volKey of Object.keys(item)) {
              if (!ALLOWED_VOLUME_OBJECT_KEYS.has(volKey)) {
                fail(`unrecognized volume mount key ${serviceName}.volumes.${volKey}`);
              }
            }
            if (
              'volume' in item &&
              item.volume !== null &&
              (!isPlainObject(item.volume) || Object.keys(item.volume).length > 0)
            ) {
              fail(`volume sub-options on ${serviceName} mounts are forbidden`);
            }
            if (item.type !== undefined && item.type !== 'volume') {
              fail(`unsupported volume mount type on ${serviceName}: ${String(item.type)}`);
            }
            if (typeof item.source !== 'string' || !item.source || typeof item.target !== 'string' || !item.target) {
              fail(`invalid volume object on ${serviceName}`);
            }
            if ('read_only' in item && typeof item.read_only !== 'boolean') {
              fail(
                `${serviceName} volume read_only must be a boolean, got ${typeof item.read_only} (${JSON.stringify(item.read_only)})`
              );
            }
            return {
              type: 'volume',
              source: item.source,
              target: item.target,
              ...(item.read_only === true ? { read_only: true } : {}),
            };
          }
          return fail(`invalid volume entry on ${serviceName}`);
        });
      } else if (key === 'environment') {
        if (isPlainObject(value)) {
          const envMap = {};
          for (const [envKey, envVal] of Object.entries(value)) {
            envMap[envKey] = interpolateComposeVars(String(envVal ?? ''), DEFAULT_TEMPLATE_ENV);
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
            envMap[envKey] = interpolateComposeVars(envVal, DEFAULT_TEMPLATE_ENV);
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
  return { name: rawDoc.name ?? 'cpa-manager-plus', services, volumes };
}

const GO_DURATION_UNIT_SECONDS = Object.freeze({
  ns: 1e-9,
  us: 1e-6,
  '\u00b5s': 1e-6,
  '\u03bcs': 1e-6,
  ms: 1e-3,
  s: 1,
  m: 60,
  h: 3600,
});

// Matches Go time.ParseDuration grammar: optional leading '+', then one or more
// (decimal with optional fraction)(unit) segments where unit is ns|us|µs|μs|ms|s|m|h,
// or the special bare "0" / "+0". Negative durations and bare non-zero numbers are
// rejected. Docker Compose stop_grace_period uses the same Go duration format
// (normalized by `docker compose config --format json` to e.g. "2m30s").
function parseGoDurationSeconds(raw) {
  if (typeof raw !== 'string' && typeof raw !== 'number') {
    return NaN;
  }
  const text = String(raw).trim();
  if (!text || text.startsWith('-')) {
    return NaN;
  }
  const unsigned = text.startsWith('+') ? text.slice(1) : text;
  if (!unsigned) {
    return NaN;
  }
  if (unsigned === '0') {
    return 0;
  }
  const tokenRe = /(\d+(?:\.\d*)?|\.\d+)(ns|us|\u00b5s|\u03bcs|ms|s|m|h)/gy;
  let pos = 0;
  let totalSeconds = 0;
  let matchedAny = false;
  while (pos < unsigned.length) {
    tokenRe.lastIndex = pos;
    const match = tokenRe.exec(unsigned);
    if (!match) {
      return NaN;
    }
    matchedAny = true;
    const value = Number(match[1]);
    const factor = GO_DURATION_UNIT_SECONDS[match[2]];
    if (!Number.isFinite(value) || !Number.isFinite(factor)) {
      return NaN;
    }
    totalSeconds += value * factor;
    pos = tokenRe.lastIndex;
  }
  return matchedAny && Number.isFinite(totalSeconds) ? totalSeconds : NaN;
}

export function validateRuntime12ComposeDocument(document, options = {}) {
  const services = document?.services ?? {};
  const serviceNames = Object.keys(services).sort();
  const expectedNames = ['cpa-manager-plus'];
  if (JSON.stringify(serviceNames) !== JSON.stringify(expectedNames)) {
    fail(
      `compose must define exact services ${expectedNames.join(', ')}, got ${serviceNames.join(', ')}`
    );
  }

  const rawTopVolumes = document?.volumes;
  if (!isPlainObject(rawTopVolumes)) {
    fail('compose must define a top-level volumes object');
  }
  const topVolumes = Object.keys(rawTopVolumes).sort();
  if (JSON.stringify(topVolumes) !== JSON.stringify(EXPECTED_TOP_VOLUMES)) {
    fail(
      `compose must define exact top-level volumes ${EXPECTED_TOP_VOLUMES.join(', ')}, got ${topVolumes.join(', ')}`
    );
  }
  for (const [volName, volConfig] of Object.entries(rawTopVolumes)) {
    if (
      volConfig !== null &&
      (!isPlainObject(volConfig) || Object.keys(volConfig).length > 0)
    ) {
      fail(
        `top-level volume ${volName} must be a canonical empty definition without overrides: ${JSON.stringify(volConfig)}`
      );
    }
  }

  for (const [serviceName, serviceConfig] of Object.entries(services)) {
    if ('build' in serviceConfig) {
      fail(`${serviceName} must not contain a build block`);
    }
    if ('privileged' in serviceConfig) {
      if (typeof serviceConfig.privileged !== 'boolean') {
        fail(`${serviceName}.privileged must be a boolean`);
      }
      if (serviceConfig.privileged === true) {
        fail(`${serviceName} must not run privileged`);
      }
    }
    if (serviceConfig.network_mode === 'host') {
      fail(`${serviceName} must not use host networking`);
    }
    if ('pid' in serviceConfig && serviceConfig.pid !== undefined && serviceConfig.pid !== null && String(serviceConfig.pid).trim() !== '') {
      fail(`${serviceName} must not set pid (${JSON.stringify(serviceConfig.pid)}); cpamp-launcher requires a private container PID namespace`);
    }
    if ('init' in serviceConfig) {
      if (typeof serviceConfig.init !== 'boolean') {
        fail(`${serviceName}.init must be a boolean`);
      }
      if (serviceConfig.init === true) {
        fail(`${serviceName} must not set init: true; cpamp-launcher must run as PID 1`);
      }
    }
    if (serviceName === 'cpa-manager-plus' && 'user' in serviceConfig && String(serviceConfig.user ?? '') !== '') {
      fail('cpa-manager-plus must start as root so cpamp-launcher can provision /data and drop privileges per child');
    }
    if (serviceName === 'cpa-manager-plus') {
      const env = serviceConfig.environment ?? {};
      const drainTimeoutRaw =
        env.CPAMP_DRAIN_TIMEOUT && String(env.CPAMP_DRAIN_TIMEOUT).trim() !== ''
          ? String(env.CPAMP_DRAIN_TIMEOUT).trim()
          : '90s';
      const drainSeconds = parseGoDurationSeconds(drainTimeoutRaw);
      const graceSeconds = parseGoDurationSeconds(serviceConfig.stop_grace_period);
      const minGraceSeconds = Math.max(150, (Number.isFinite(drainSeconds) ? drainSeconds : 90) + 60);
      if (
        !Number.isFinite(drainSeconds) ||
        drainSeconds <= 0 ||
        !Number.isFinite(graceSeconds) ||
        graceSeconds < minGraceSeconds
      ) {
        fail(
          `cpa-manager-plus must set stop_grace_period >= CPAMP_DRAIN_TIMEOUT + 60s (at least ${minGraceSeconds}s for CPAMP_DRAIN_TIMEOUT=${drainTimeoutRaw}), got ${JSON.stringify(serviceConfig.stop_grace_period)}`
        );
      }
      const loopbackAddrRules = {
        CPAMP_INGRESS_CONTROL_ADDR: '127.0.0.1:9082',
        HTTP_ADDR: '127.0.0.1:18319',
        CPAMP_RUNTIME_ADDR: '127.0.0.1:9081',
        CPAMP_RUNTIME_LISTEN_ADDR: '127.0.0.1:9081',
        CPAMP_RUNTIME_CPA_ADDR: '127.0.0.1:8317',
      };
      for (const [key, expected] of Object.entries(loopbackAddrRules)) {
        if (key in env && env[key] !== '' && env[key] !== expected) {
          fail(`${key} override must stay on in-container loopback ${expected}, got ${JSON.stringify(env[key])}`);
        }
      }
    }
    parsePinnedImageReference(serviceConfig.image, serviceName);
    const ports = serviceConfig.ports ?? [];
    if (serviceName === 'cpa-manager-plus') {
      if (!Array.isArray(ports) || ports.length !== 1 || !isPlainObject(ports[0])) {
        fail(`cpa-manager-plus must publish exactly one 18317:18317/tcp port`);
      }
      const portObj = ports[0];
      for (const portKey of Object.keys(portObj)) {
        if (!ALLOWED_PORT_OBJECT_KEYS.has(portKey)) {
          fail(`forbidden cpa-manager-plus port property: ${portKey}`);
        }
      }
      if (
        (portObj.mode !== undefined && portObj.mode !== 'ingress') ||
        (portObj.protocol !== undefined && portObj.protocol !== 'tcp') ||
        String(portObj.published) !== '18317' ||
        Number(portObj.target) !== 18317
      ) {
        fail(
          `Runtime 13 Compose validation failed: expected only cpa-manager-plus 18317:18317/tcp, got ${JSON.stringify(portObj)}`
        );
      }
    } else if (Array.isArray(ports) && ports.length > 0) {
      fail(
        `Runtime 13 Compose validation failed: expected only cpa-manager-plus 18317:18317, but ${serviceName} defines ports`
      );
    }

    const mounts = serviceConfig.volumes ?? [];
    if (!Array.isArray(mounts)) {
      fail(`${serviceName}.volumes must be an array`);
    }
    const actualMountSpecs = mounts
      .map((m) => {
        if (!isPlainObject(m)) {
          fail(`invalid mount entry on ${serviceName}`);
        }
        if ('read_only' in m && typeof m.read_only !== 'boolean') {
          fail(`${serviceName} volume read_only must be a boolean`);
        }
        if (String(m.source ?? '').includes('docker.sock') || String(m.target ?? '').includes('docker.sock')) {
          fail(`${serviceName} must not mount the Docker socket`);
        }
        return `${m.type ?? 'volume'}:${m.source}:${m.target}:${m.read_only === true ? 'ro' : 'rw'}`;
      })
      .sort();
    const expectedMountSpecs = [...EXPECTED_SERVICE_MOUNTS[serviceName]].sort();
    if (JSON.stringify(actualMountSpecs) !== JSON.stringify(expectedMountSpecs)) {
      if (
        serviceName === 'cpa-manager-plus' &&
        mounts.some(
          (m) => m.target === '/data' && m.read_only === true
        )
      ) {
        fail('cpa-manager-plus /data volume must be writable');
      }
      fail(
        `strict volume whitelist violation on ${serviceName}: expected [${expectedMountSpecs.join(', ')}], got [${actualMountSpecs.join(', ')}]`
      );
    }
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
  const document = parseEmbeddedComposeText(composeText);
  return validateRuntime12ComposeDocument(document, options);
}

// Note: Node verification checks all three role OCI archives (manifest/config/layer digests,
// OCI labels, provenance, official CPA v8.0.55 binary/manifest digests, Full Web/API contracts)
// and the static Compose topology, but does NOT certify Go build identity (`go version -m`)
// on its own. Full candidate certification requires `verify-embedded-candidate.py`.
export function verifyEmbeddedReleaseArtifacts({
  evidence,
  composeText,
  pairing,
  evidenceDir = null,
  roleOciPaths = null,
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
    roleOciPaths,
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
    pairing.api_contract.contract_id !== expectedMeta.api_contract.contract_id ||
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
    const expectedBinarySha = evImg.packagedFiles[PACKAGED_PATHS.binaries.manager];
    if (imgEntry.binary_sha256 !== expectedBinarySha) {
      fail(`binary_sha256 mismatch for ${role} in pairing`);
    }
    const expectedProvenanceSha = evImg.packagedFiles[PACKAGED_PATHS.provenance];
    if (imgEntry.release_provenance_sha256 !== expectedProvenanceSha) {
      fail(`release_provenance_sha256 mismatch for ${role} in pairing`);
    }

    const expectedImageRef = `${imgEntry.repository}:${imgEntry.tag}@${imgEntry.manifest_digest}`;
    if (typeof imgEntry.image_ref !== 'string' || imgEntry.image_ref !== expectedImageRef) {
      fail(`image_ref missing or mismatched for ${role} in pairing`);
    }
    if (role === 'full') {
      const serviceName = 'cpa-manager-plus';
      const actualServiceImage = composeDoc.services[serviceName]?.image;
      if (actualServiceImage !== expectedImageRef) {
        fail(
          `compose service ${serviceName} image (${String(actualServiceImage)}) does not match pairing (${expectedImageRef})`
        );
      }
    }
  }

  if (seenManifestDigests.size !== ROLES.length) {
    fail('pairing images must have distinct manifest digests for slim and full');
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
      binary_sha256: evImg.packagedFiles[PACKAGED_PATHS.binaries.manager],
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
    roleOciPaths: options.roleOciPaths ?? null,
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

export const EMBEDDED_VERIFIER_CONTRACT_NOTICE =
  "static port template '${CPAMP_PUBLIC_PORT:-18317}:18317' -> container 18317/tcp only (deployer chooses host bind [HOST_IP:]PORT via CPAMP_PUBLIC_PORT); Node verifier does not certify Go build identity (go version -m) on its own — run verify-embedded-candidate.py for Go build metadata certification";

export const CLI_HELP_TEXT = [
  'Usage: node bin/release/render-embedded-compose.mjs --evidence <candidate-evidence.json> [options]',
  '',
  'Render or verify (`--verify`) immutable digest-pinned docker-compose.v2-embedded.yml and embedded-release-pairing.json.',
  'Requires both verified variant OCI archives (slim.oci.tar, full.oci.tar).',
  '',
  'Contract guarantees and boundaries:',
  "  - Port template: guarantees static '${CPAMP_PUBLIC_PORT:-18317}:18317' on cpamp-ingress, container side 18317/tcp,",
  '    and no other service/port mappings. Host bind address/port (CPAMP_PUBLIC_PORT=[HOST_IP:]PORT) are the',
  '    deployer choice at deployment time; this verifier never reads process.env.',
  '  - Go build identity: this Node tool does NOT certify Go build identity (`go version -m`) on its own.',
  '    Compiled Go build metadata is certified by `python3 bin/release/verify-embedded-candidate.py` (`verify_archive_anchor`).',
].join('\n');

function parseCliArgs(argv) {
  const args = {
    help: false,
    verify: false,
    digests: {},
    imageRefs: {},
    roleOciPaths: {},
  };
  for (let i = 0; i < argv.length; i += 1) {
    const token = argv[i];
    const next = () => {
      if (i + 1 >= argv.length) fail(`missing value for ${token}`);
      i += 1;
      return argv[i];
    };
    switch (token) {
      case '--help':
      case '-h':
        args.help = true;
        break;
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
      case '--slim-digest':
        args.digests.slim = next();
        break;
      case '--full-digest':
        args.digests.full = next();
        break;
      case '--slim-image':
        args.imageRefs.slim = next();
        break;
      case '--full-image':
        args.imageRefs.full = next();
        break;
      case '--slim-oci':
        args.roleOciPaths.slim = resolve(next());
        break;
      case '--full-oci':
        args.roleOciPaths.full = resolve(next());
        break;
      default:
        fail(`unknown argument: ${token}`);
    }
  }
  if (args.help) {
    return args;
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

export function runCli(argv = process.argv.slice(2)) {
  const cli = parseCliArgs(argv);
  if (cli.help) {
    console.log(CLI_HELP_TEXT);
    return;
  }
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
      roleOciPaths: Object.keys(cli.roleOciPaths).length ? cli.roleOciPaths : null,
    });
    if (cli.composeJsonPath) {
      mkdirSync(dirname(cli.composeJsonPath), { recursive: true });
      writeFileSync(cli.composeJsonPath, JSON.stringify(composeDoc, null, 2) + '\n', 'utf8');
    }
    console.log(
      `Embedded compose/pairing verification passed: ${cli.composePath} + ${cli.pairingPath} (${EMBEDDED_VERIFIER_CONTRACT_NOTICE})`
    );
    return;
  }

  const { composeYaml, composeJson, pairing } = renderEmbeddedReleaseArtifacts(evidence, {
    version: cli.version,
    registryPrefix: cli.registryPrefix,
    digests: Object.keys(cli.digests).length ? cli.digests : undefined,
    imageRefs: Object.keys(cli.imageRefs).length ? cli.imageRefs : undefined,
    evidenceDir,
    roleOciPaths: Object.keys(cli.roleOciPaths).length ? cli.roleOciPaths : null,
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
    `Rendered and verified immutable Embedded compose and pairing: ${cli.composePath} + ${cli.pairingPath} (${EMBEDDED_VERIFIER_CONTRACT_NOTICE})`
  );
}

if (process.argv[1] && import.meta.url === pathToFileURL(realpathSync(process.argv[1])).href) {
  try {
    runCli(process.argv.slice(2));
  } catch (error) {
    console.error(`STOP: ${error.message}`);
    process.exit(1);
  }
}
