import { createHash } from 'node:crypto';
import { readFileSync, writeFileSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { gunzipSync } from 'node:zlib';
import {
  repository,
  parseVersion,
  nativeAssets,
  validateInfo,
  assertSupportedVersion,
  assertBooleanOption,
  assertV2UpgradeGuideUrl,
  validateV2Pairing,
  v2EmbeddedComposeFilename,
  v2PairingFilename,
  v2MinimumCPAVersion,
  v2SupportedModes,
  v2Platforms,
  v2ImageRegistries,
  v2ImageRepositoryName,
  v2ImageRoles,
  v2DockerVersionTag,
  v2DockerRoleTag,
  v2NativeArchiveName,
  v2SlotArchiveName,
} from './update-contract.mjs';
import { resolveSlotPublicKeyHex, verifySlotManifestWithPolicy } from './cpamp-slot-sign.mjs';

const resolveOptionPublicKeyHex = (options = {}) => {
  const hasSlotPub = options.slotPublicKeyHex !== undefined && options.slotPublicKeyHex !== null;
  const hasPub = options.publicKeyHex !== undefined && options.publicKeyHex !== null;
  if (
    hasSlotPub &&
    hasPub &&
    String(options.slotPublicKeyHex).trim().toLowerCase() !==
      String(options.publicKeyHex).trim().toLowerCase()
  ) {
    throw new Error('slotPublicKeyHex and publicKeyHex disagree');
  }
  return hasSlotPub ? options.slotPublicKeyHex : options.publicKeyHex;
};
const REQUIRED_SLOT_FILE_MODES = Object.freeze({
  'bin/cpamp-launcher': '0555',
  'bin/cpamp-runtime-supervisor': '0555',
  'bin/cpa-manager-plus': '0555',
  'bin/cpamp-ingress': '0555',
  'share/cpamp/runtime/config.seed.yaml': '0444',
  'share/cpamp/runtime/seed-lineage.json': '0444',
  'share/cpamp/runtime/cpa-artifact.json': '0444',
  'share/cpamp/release/base-abi.json': '0444',
});

const SHA256_PREFIXED_PATTERN = /^sha256:[0-9a-f]{64}$/;
const COMMIT_SHA40_PATTERN = /^[0-9a-f]{40}$/;

const isPlainObject = (value) =>
  Boolean(value) && typeof value === 'object' && !Array.isArray(value);

const isSafeSlotMemberPath = (relPath, { allowDirTrailingSlash = false } = {}) => {
  if (typeof relPath !== 'string' || relPath.length === 0 || relPath.length > 512) {
    return false;
  }
  // eslint-disable-next-line no-control-regex
  if (/[\x00-\x1f\x7f\\]/.test(relPath)) return false;
  if (relPath.startsWith('/') || relPath.startsWith('./')) return false;
  const candidate =
    allowDirTrailingSlash && relPath.endsWith('/') ? relPath.slice(0, -1) : relPath;
  if (!candidate || candidate.endsWith('/')) return false;
  const parts = candidate.split('/');
  if (parts.some((part) => !part || part === '.' || part === '..')) return false;
  return true;
};

const isPermittedSlotManifestFilePath = (relPath) => {
  if (!isSafeSlotMemberPath(relPath)) return false;
  if (
    relPath === 'slot-manifest.json' ||
    relPath === 'slot-manifest.json.sig' ||
    relPath === 'share/cpamp/provenance.json'
  ) {
    return false;
  }
  const parts = relPath.split('/');
  if (parts[0] === 'bin') return parts.length === 2;
  if (parts[0] === 'share') return parts.length >= 2;
  return false;
};

const parsePaxHeaders = (body) => {
  const pax = {};
  let cursor = 0;
  while (cursor < body.length) {
    const spaceIdx = body.indexOf(0x20, cursor);
    if (spaceIdx <= cursor) {
      throw new Error('Malformed PAX header record in slot archive');
    }
    const recLenStr = body.subarray(cursor, spaceIdx).toString('ascii');
    if (!/^\d+$/.test(recLenStr)) {
      throw new Error('Malformed PAX header length in slot archive');
    }
    const recLen = Number.parseInt(recLenStr, 10);
    if (!Number.isSafeInteger(recLen) || recLen < 5 || cursor + recLen > body.length) {
      throw new Error('Invalid PAX header record bounds in slot archive');
    }
    const rec = body.subarray(cursor, cursor + recLen);
    if (rec[rec.length - 1] !== 0x0a) {
      throw new Error('Malformed PAX header terminator in slot archive');
    }
    const kv = rec.subarray(spaceIdx - cursor + 1, rec.length - 1).toString('utf8');
    const eqIdx = kv.indexOf('=');
    if (eqIdx <= 0) {
      throw new Error('Malformed PAX header entry in slot archive');
    }
    const key = kv.slice(0, eqIdx);
    const val = kv.slice(eqIdx + 1);
    if (key === 'path') {
      pax.path = val;
    } else if (key === 'size') {
      if (!/^\d+$/.test(val)) {
        throw new Error('Invalid PAX size in slot archive');
      }
      pax.size = Number.parseInt(val, 10);
    }
    cursor += recLen;
  }
  return pax;
};

const INVALID_TAR_CHECKSUM_ERROR = 'Slot archive has invalid tar header checksum';

const parseTarChecksumOctal = (field) => {
  if (!Buffer.isBuffer(field) || field.length === 0) {
    throw new Error(INVALID_TAR_CHECKSUM_ERROR);
  }
  // Match Go 1.26 archive/tar format.go:181: header chksum is strictly octal (parseOctal), never base-256.
  if ((field[0] & 0x80) !== 0) {
    throw new Error(INVALID_TAR_CHECKSUM_ERROR);
  }
  let start = 0;
  let end = field.length;
  while (start < end && (field[start] === 0x20 || field[start] === 0x00)) {
    start += 1;
  }
  while (end > start && (field[end - 1] === 0x20 || field[end - 1] === 0x00)) {
    end -= 1;
  }
  if (start === end) {
    throw new Error(INVALID_TAR_CHECKSUM_ERROR);
  }
  let value = 0n;
  for (let index = start; index < end; index += 1) {
    const byte = field[index];
    if (byte < 0x30 || byte > 0x37) {
      throw new Error(INVALID_TAR_CHECKSUM_ERROR);
    }
    value = (value << 3n) | BigInt(byte - 0x30);
  }
  return value;
};

const verifyTarHeaderChecksum = (block) => {
  if (!Buffer.isBuffer(block) || block.length !== 512) {
    throw new Error(INVALID_TAR_CHECKSUM_ERROR);
  }
  const storedChecksum = parseTarChecksumOctal(block.subarray(148, 156));
  let unsignedSum = 0;
  let signedSum = 0;
  for (let index = 0; index < 512; index += 1) {
    const byte = index >= 148 && index < 156 ? 0x20 : block[index];
    unsignedSum += byte;
    signedSum += byte >= 0x80 ? byte - 0x100 : byte;
  }
  if (storedChecksum !== BigInt(unsignedSum) && storedChecksum !== BigInt(signedSum)) {
    throw new Error(INVALID_TAR_CHECKSUM_ERROR);
  }
};

const readTarEntries = (tarBytes) => {
  if (!Buffer.isBuffer(tarBytes) || tarBytes.length < 512 || tarBytes.length % 512 !== 0) {
    throw new Error('Invalid tar archive length in slot archive');
  }
  const files = new Map();
  const dirs = new Map();
  const dotSlashPaths = [];
  let offset = 0;
  let nextPax = null;
  let sawEofMarker = false;

  while (offset + 512 <= tarBytes.length) {
    const header = tarBytes.subarray(offset, offset + 512);
    if (header.every((byte) => byte === 0)) {
      if (nextPax !== null) {
        throw new Error('Dangling PAX header before tar end-of-archive in slot archive');
      }
      if (!tarBytes.subarray(offset).every((byte) => byte === 0)) {
        throw new Error('Unexpected trailing non-zero tar blocks after end-of-archive in slot archive');
      }
      sawEofMarker = true;
      break;
    }
    verifyTarHeaderChecksum(header);
    const rawName = header.subarray(0, 100).toString('utf8').replace(/\0.*$/, '');
    const rawPrefix = header.subarray(345, 500).toString('utf8').replace(/\0.*$/, '');
    const modeOctal = header.subarray(100, 108).toString('utf8').replace(/\0.*$/, '').trim();
    const sizeOctal = header.subarray(124, 136).toString('utf8').replace(/\0.*$/, '').trim();
    const typeflag = header.subarray(156, 157).toString('utf8');
    if (sizeOctal !== '' && !/^[0-7]+$/.test(sizeOctal)) {
      throw new Error('Invalid tar entry size in slot archive');
    }
    let size = sizeOctal ? Number.parseInt(sizeOctal, 8) : 0;
    if (nextPax?.size !== undefined) {
      size = nextPax.size;
    }
    if (!Number.isSafeInteger(size) || size < 0) {
      throw new Error('Invalid tar entry size in slot archive');
    }
    const dataStart = offset + 512;
    const dataEnd = dataStart + size;
    const paddedEnd = dataStart + Math.ceil(size / 512) * 512;
    if (dataEnd > tarBytes.length || paddedEnd > tarBytes.length) {
      throw new Error('Truncated tar entry in slot archive');
    }
    const body = tarBytes.subarray(dataStart, dataEnd);
    offset = paddedEnd;

    if (typeflag === 'x') {
      if (nextPax !== null) {
        throw new Error('Unexpected consecutive PAX header in slot archive');
      }
      nextPax = parsePaxHeaders(body);
      continue;
    }

    const rawEntryName = nextPax?.path ?? (rawPrefix ? `${rawPrefix}/${rawName}` : rawName);
    nextPax = null;

    // package-cpamp-slot.mjs emits deterministic directory headers (typeflag '5') for
    // parent directories of manifest.files (validated against expectedDirs); reject all
    // other non-regular tar entry types (symlinks, hardlinks, devices, FIFOs, etc.).
    if (typeflag !== '0' && typeflag !== '\0' && typeflag !== '5') {
      throw new Error(
        `Slot archive contains forbidden non-regular tar entry (${JSON.stringify(typeflag)}) at ${rawEntryName}`
      );
    }
    if (!/^[0-7]+$/.test(modeOctal)) {
      throw new Error(`Invalid tar entry mode for ${rawEntryName} in slot archive`);
    }
    const mode = Number.parseInt(modeOctal, 8) & 0o7777;

    // eslint-disable-next-line no-control-regex
    if (typeof rawEntryName !== 'string' || !rawEntryName || /[\x00-\x1f\x7f\\]/.test(rawEntryName)) {
      throw new Error(`Unsafe tar entry path in slot archive: ${String(rawEntryName)}`);
    }
    if (rawEntryName.startsWith('/')) {
      throw new Error(`Slot archive tar entry must not start with /: ${rawEntryName}`);
    }

    let candidate = rawEntryName;
    if (candidate.startsWith('./')) {
      dotSlashPaths.push(rawEntryName);
      candidate = candidate.slice(2);
    }
    if (typeflag === '5' && candidate === '') {
      continue;
    }
    if (!isSafeSlotMemberPath(candidate, { allowDirTrailingSlash: typeflag === '5' })) {
      throw new Error(`Unsafe tar entry path in slot archive: ${rawEntryName}`);
    }
    const normalized =
      typeflag === '5' && candidate.endsWith('/') ? candidate.slice(0, -1) : candidate;
    if (files.has(normalized) || dirs.has(normalized)) {
      throw new Error(`Duplicate tar entry path in slot archive: ${normalized}`);
    }
    if (typeflag === '5') {
      if (size !== 0) {
        throw new Error(`Directory tar entry ${normalized} must have size 0`);
      }
      dirs.set(normalized, { mode });
      continue;
    }
    files.set(normalized, { body: Buffer.from(body), mode, typeflag });
  }
  if (nextPax !== null) {
    throw new Error('Dangling PAX header at end of slot archive');
  }
  if (!sawEofMarker) {
    throw new Error('Truncated tar stream missing end-of-archive marker in slot archive');
  }
  return { files, dirs, dotSlashPaths };
};

export const extractSlotMetadataFromArchive = (archivePath, tag, options = {}) => {
  const dryRun = assertBooleanOption(options, 'dryRun');
  const allowEphemeralSlotKey = assertBooleanOption(options, 'allowEphemeralSlotKey');
  if (allowEphemeralSlotKey && !dryRun) {
    throw new Error('--allow-ephemeral-slot-key requires --dry-run');
  }
  const gzipBytes = readFileSync(archivePath);
  const sha256 = `sha256:${createHash('sha256').update(gzipBytes).digest('hex')}`;
  const size = gzipBytes.length;
  const { files, dirs, dotSlashPaths } = readTarEntries(gunzipSync(gzipBytes));
  if (files.has('share/cpamp/provenance.json') || dirs.has('share/cpamp/provenance.json')) {
    throw new Error(`Slot archive ${archivePath} must not package share/cpamp/provenance.json`);
  }
  const manifestEntry = files.get('slot-manifest.json');
  const sigEntry = files.get('slot-manifest.json.sig');
  if (!manifestEntry || !sigEntry) {
    throw new Error(`Slot archive ${archivePath} is missing slot-manifest.json or signature`);
  }
  const manifestRaw = manifestEntry.body;
  const sigRaw = sigEntry.body;
  const manifest = JSON.parse(manifestRaw.toString('utf8'));
  const sig = JSON.parse(sigRaw.toString('utf8'));
  if (
    !isPlainObject(manifest) ||
    !isPlainObject(sig) ||
    manifest.schema_version !== 1 ||
    sig.schema_version !== 1 ||
    sig.algorithm !== 'ed25519'
  ) {
    throw new Error(`Slot archive ${archivePath} has invalid manifest or signature metadata`);
  }
  if (manifest.key_id !== sig.key_id) {
    throw new Error(
      `Slot archive key_id mismatch between manifest (${manifest.key_id}) and signature (${sig.key_id})`
    );
  }
  if (isPlainObject(manifest.files) && Object.hasOwn(manifest.files, 'share/cpamp/provenance.json')) {
    throw new Error(`Slot archive ${archivePath} manifest must not list share/cpamp/provenance.json`);
  }
  const publicKeyHex = resolveOptionPublicKeyHex(options);
  verifySlotManifestWithPolicy(manifestRaw, sigRaw, {
    dryRun,
    allowEphemeralSlotKey,
    keyId: sig.key_id,
    ...(publicKeyHex !== undefined ? { publicKeyHex } : {}),
  });
  if (dotSlashPaths.length > 0) {
    throw new Error(
      `Slot archive ${archivePath} contains forbidden ./ entry prefix: ${dotSlashPaths[0]}`
    );
  }
  if (manifest.version !== tag) {
    throw new Error(`Slot archive version mismatch: expected ${tag}, got ${manifest.version}`);
  }
  if (typeof manifest.source_commit !== 'string' || !COMMIT_SHA40_PATTERN.test(manifest.source_commit)) {
    throw new Error(`Slot archive ${archivePath} has invalid source_commit`);
  }
  if (!Number.isSafeInteger(manifest.min_base_epoch) || manifest.min_base_epoch < 1) {
    throw new Error(`Slot archive ${archivePath} has invalid min_base_epoch`);
  }
  if (!Number.isSafeInteger(manifest.schema_generation) || manifest.schema_generation < 1) {
    throw new Error(`Slot archive ${archivePath} has invalid schema_generation`);
  }
  const arch =
    manifest.platform === 'linux/amd64'
      ? 'amd64'
      : manifest.platform === 'linux/arm64'
        ? 'arm64'
        : '';
  if (!arch) {
    throw new Error(`Unsupported slot platform in ${archivePath}: ${manifest.platform}`);
  }
  const expectedName = v2SlotArchiveName(tag, arch);
  const actualName = path.basename(archivePath);
  if (actualName !== expectedName) {
    throw new Error(`Slot archive filename mismatch: expected ${expectedName}, got ${actualName}`);
  }
  if (!isPlainObject(manifest.files)) {
    throw new Error(`Slot archive ${archivePath} manifest.files must be a plain object`);
  }
  for (const [requiredPath, requiredMode] of Object.entries(REQUIRED_SLOT_FILE_MODES)) {
    if (!Object.hasOwn(manifest.files, requiredPath)) {
      throw new Error(
        `Slot archive ${archivePath} manifest.files is missing required entry ${requiredPath}`
      );
    }
    if (manifest.files[requiredPath]?.mode !== requiredMode) {
      throw new Error(
        `Slot archive ${archivePath} manifest.files[${requiredPath}] must declare mode ${requiredMode}`
      );
    }
  }

  const expectedDirs = new Set();
  for (const [relPath, entryMeta] of Object.entries(manifest.files)) {
    if (!isPermittedSlotManifestFilePath(relPath)) {
      throw new Error(`Slot archive ${archivePath} manifest contains invalid file path: ${relPath}`);
    }
    if (!isPlainObject(entryMeta)) {
      throw new Error(`Slot archive ${archivePath} manifest.files[${relPath}] must be an object`);
    }
    const expectedModeStr = relPath.startsWith('bin/') ? '0555' : '0444';
    if (entryMeta.mode !== expectedModeStr) {
      throw new Error(
        `Slot archive ${archivePath} manifest.files[${relPath}] must declare mode ${expectedModeStr}`
      );
    }
    const expectedMode = Number.parseInt(expectedModeStr, 8);
    if (
      !Number.isSafeInteger(entryMeta.size) ||
      entryMeta.size < 0 ||
      (relPath.startsWith('bin/') && entryMeta.size === 0)
    ) {
      throw new Error(`Slot archive ${archivePath} manifest.files[${relPath}] has invalid size`);
    }
    if (typeof entryMeta.sha256 !== 'string' || !SHA256_PREFIXED_PATTERN.test(entryMeta.sha256)) {
      throw new Error(`Slot archive ${archivePath} manifest.files[${relPath}] has invalid sha256`);
    }
    const tarEntry = files.get(relPath);
    if (!tarEntry) {
      throw new Error(`Slot archive ${archivePath} is missing manifested file ${relPath}`);
    }
    if (tarEntry.body.length !== entryMeta.size) {
      throw new Error(
        `Slot archive ${archivePath} file ${relPath} size mismatch: expected ${entryMeta.size}, got ${tarEntry.body.length}`
      );
    }
    if ((tarEntry.mode & 0o7777) !== expectedMode) {
      throw new Error(
        `Slot archive ${archivePath} file ${relPath} mode mismatch: expected ${expectedModeStr}, got 0${(tarEntry.mode & 0o7777).toString(8)}`
      );
    }
    const actualSha256 = `sha256:${createHash('sha256').update(tarEntry.body).digest('hex')}`;
    if (actualSha256 !== entryMeta.sha256) {
      throw new Error(
        `Slot archive ${archivePath} file ${relPath} sha256 mismatch: expected ${entryMeta.sha256}, got ${actualSha256}`
      );
    }
    let parentDir = path.posix.dirname(relPath);
    while (parentDir && parentDir !== '.') {
      expectedDirs.add(parentDir);
      parentDir = path.posix.dirname(parentDir);
    }
  }
  for (const relPath of files.keys()) {
    if (relPath === 'slot-manifest.json' || relPath === 'slot-manifest.json.sig') {
      continue;
    }
    if (!Object.hasOwn(manifest.files, relPath)) {
      throw new Error(`Slot archive ${archivePath} contains unmanifested file ${relPath}`);
    }
  }
  for (const [dirPath, dirMeta] of dirs.entries()) {
    if (!expectedDirs.has(dirPath)) {
      throw new Error(`Slot archive ${archivePath} contains unmanifested directory ${dirPath}`);
    }
    if ((dirMeta.mode & 0o022) !== 0 || (dirMeta.mode & 0o7000) !== 0) {
      throw new Error(
        `Slot archive ${archivePath} directory ${dirPath} has unsafe mode 0${dirMeta.mode.toString(8)}`
      );
    }
  }
  return {
    name: actualName,
    platform: manifest.platform,
    sha256,
    size,
    min_base_epoch: manifest.min_base_epoch,
    schema_generation: manifest.schema_generation,
  };
};
const parseUpdateMetadata = (chineseNotes) => {
  // One reviewed block in the existing release PR; no second notes source.
  const matches = [...String(chineseNotes).matchAll(/<!--\s*cpamp-update\s*\n([\s\S]*?)-->/g)];
  if (matches.length !== 1)
    throw new Error('Release notes require exactly one cpamp-update JSON comment');
  return JSON.parse(matches[0][1]);
};

const contentFor = (tag, metadata) => ({
  summary: metadata.summary,
  notes: Object.fromEntries(
    ['zh', 'en'].map((lang) => [
      lang,
      `${repository}/blob/${tag}/docs/release-notes/${tag}-${lang}.md`,
    ])
  ),
});

const validateV2NotesMetadata = (metadata) => {
  for (const language of ['zh', 'en']) {
    const summary = metadata.summary?.[language];
    if (typeof summary !== 'string' || !summary.trim() || Buffer.byteLength(summary, 'utf8') > 4096)
      throw new Error('Invalid v2 release content');
  }
  assertV2UpgradeGuideUrl(metadata.update?.upgrade_guide_url);
  if (
    typeof metadata.update?.breaking !== 'boolean' ||
    typeof metadata.update?.migration_required !== 'boolean'
  )
    throw new Error('Explicit update flags required');
  if (
    !('minimum_direct_upgrade_version' in (metadata.update || {})) ||
    metadata.update.minimum_direct_upgrade_version === undefined
  )
    throw new Error('v2 beta release notes must declare minimum_direct_upgrade_version');
  if (metadata.update.minimum_direct_upgrade_version !== null) {
    if (typeof metadata.update.minimum_direct_upgrade_version !== 'string')
      throw new Error('Invalid v2 minimum_direct_upgrade_version');
    parseVersion(metadata.update.minimum_direct_upgrade_version);
  }
  if (metadata.compatibility?.minimum_cpa_version !== v2MinimumCPAVersion)
    throw new Error(
      `v2 beta release notes must declare minimum_cpa_version ${v2MinimumCPAVersion}`
    );
};

// Validate reviewed note metadata before OCI evidence/pairing exists. This is
// deliberately a notes check, not a generated release-info or publication.
export function validateReleaseMetadata(tag, chineseNotes, options = {}) {
  assertBooleanOption(options, 'dryRun');
  const version = assertSupportedVersion(tag, options);
  const metadata = parseUpdateMetadata(chineseNotes);
  if (version.core[0] === '2') validateV2NotesMetadata(metadata);
  return metadata;
}

const normalizeSlotDistributionInput = (tag, options = {}) => {
  const dryRun = assertBooleanOption(options, 'dryRun');
  const allowEphemeralSlotKey = assertBooleanOption(options, 'allowEphemeralSlotKey');
  if (allowEphemeralSlotKey && !dryRun) {
    throw new Error('--allow-ephemeral-slot-key requires --dry-run');
  }
  const fromArchives = [];
  const archivePaths = [
    ...(options.slotArchive ? [options.slotArchive] : []),
    ...(Array.isArray(options.slotArchives) ? options.slotArchives : []),
  ];
  const publicKeyHex = resolveOptionPublicKeyHex(options);
  for (const archivePath of archivePaths) {
    fromArchives.push(
      extractSlotMetadataFromArchive(archivePath, tag, {
        dryRun,
        allowEphemeralSlotKey,
        ...(publicKeyHex !== undefined ? { publicKeyHex } : {}),
      })
    );
  }
  const rawSlot = options.slot ?? options.pairing?.slot;
  const explicitAssets = options.slotAssets ?? (Array.isArray(rawSlot) ? rawSlot : rawSlot?.assets);
  const assets = [...(explicitAssets || []), ...fromArchives];
  if (assets.length === 0) return undefined;
  return {
    assets: assets.map((asset) => ({
      name: asset.name,
      platform: asset.platform,
      sha256: asset.sha256,
      size: asset.size,
      min_base_epoch: asset.min_base_epoch,
      schema_generation: asset.schema_generation,
    })),
  };
};

const buildV2ReleaseInfo = (tag, sha, metadata, pairingInput, slotDistribution) => {
  const pairing = validateV2Pairing(pairingInput, { tag, sourceCommit: sha });
  const versionTag = v2DockerVersionTag(tag);
  const images = v2ImageRegistries.flatMap((registry) =>
    v2ImageRoles.map((role) => ({
      role,
      name: `${registry}/${v2ImageRepositoryName}`,
      tag: v2DockerRoleTag(tag, role),
      digest: pairing.images[role].manifest_digest,
      platforms: [...v2Platforms],
    }))
  );
  const distribution = {
    docker: { version_tag: versionTag, images },
    native: { assets: [v2NativeArchiveName(tag)] },
    compose: { filename: v2EmbeddedComposeFilename, sha256: pairing.compose.sha256 },
  };
  if (slotDistribution !== undefined) {
    distribution.slot = slotDistribution;
  }
  return validateInfo(
    {
      schema_version: 2,
      release: { version: tag, stage: 'beta', source_commit: sha },
      content: contentFor(tag, metadata),
      update: {
        breaking: metadata.update.breaking,
        migration_required: metadata.update.migration_required,
        minimum_direct_upgrade_version: metadata.update.minimum_direct_upgrade_version,
        upgrade_guide_url: metadata.update.upgrade_guide_url,
      },
      distribution,
      compatibility: { minimum_cpa_version: v2MinimumCPAVersion },
      supported_modes: [...v2SupportedModes],
      supported_platforms: { docker: [...v2Platforms], native: [...v2Platforms] },
      pairing: {
        filename: v2PairingFilename,
        source_commit: pairing.source_commit,
        fullweb_manifest_sha256: pairing.fullweb.manifest_sha256,
        api_contract_sha256: pairing.api_contract.sha256,
        compose_sha256: pairing.compose.sha256,
      },
    },
    tag,
    { allowV2Beta: true }
  );
};

export function generateReleaseInfo(tag, sha, chineseNotes, options = {}) {
  const metadata = validateReleaseMetadata(tag, chineseNotes, options);
  const allowEphemeralSlotKey = assertBooleanOption(options, 'allowEphemeralSlotKey');
  if (allowEphemeralSlotKey && options.dryRun !== true) {
    throw new Error('--allow-ephemeral-slot-key requires dryRun === true');
  }
  if (parseVersion(tag).core[0] === '2') {
    const slotDistribution = normalizeSlotDistributionInput(tag, options);
    return buildV2ReleaseInfo(tag, sha, metadata, options.pairing, slotDistribution);
  }
  return validateInfo(
    {
      schema_version: 1,
      release: { version: tag, stage: parseVersion(tag).stage, source_commit: sha },
      content: contentFor(tag, metadata),
      update: metadata.update,
      distribution: {
        docker: { image: 'seakee/cpa-manager-plus', version_tag: tag },
        native: { assets: nativeAssets(tag) },
      },
      compatibility: metadata.compatibility,
    },
    tag
  );
}

const parseCliArguments = (argumentsList) => {
  const options = {
    dryRun: process.env.DRY_RUN === 'true',
    allowEphemeralSlotKey: false,
    slotArchives: [],
  };
  const positional = [];
  for (let index = 0; index < argumentsList.length; index += 1) {
    const argument = argumentsList[index];
    if (argument === '--dry-run') options.dryRun = true;
    else if (argument === '--allow-v2-beta') options.allowV2Beta = true;
    else if (argument === '--allow-ephemeral-slot-key') {
      const next = argumentsList[index + 1];
      if (next === 'true' || next === 'false') {
        options.allowEphemeralSlotKey = next === 'true';
        index += 1;
      } else {
        options.allowEphemeralSlotKey = true;
      }
    } else if (argument === '--pairing') {
      const value = argumentsList[++index];
      if (!value) throw new Error('--pairing requires a path');
      options.pairingPath = value;
    } else if (argument === '--slot-archive') {
      const value = argumentsList[++index];
      if (!value) throw new Error('--slot-archive requires a path');
      options.slotArchives.push(value);
    } else if (argument === '--slot-info') {
      const value = argumentsList[++index];
      if (!value) throw new Error('--slot-info requires a path');
      options.slotInfoPath = value;
    } else if (argument === '--slot-public-key') {
      const value = argumentsList[++index];
      if (!value || value.startsWith('--')) {
        throw new Error('--slot-public-key requires a 64-hex string or public key file path');
      }
      if (options.slotPublicKeyHex !== undefined) {
        throw new Error('Duplicate argument: --slot-public-key');
      }
      options.slotPublicKeyHex = resolveSlotPublicKeyHex(value);
    } else if (argument.startsWith('--')) throw new Error(`Unknown argument: ${argument}`);
    else positional.push(argument);
  }
  if (options.allowEphemeralSlotKey && !options.dryRun) {
    throw new Error('--allow-ephemeral-slot-key requires --dry-run');
  }
  return { options, positional };
};

const runCli = () => {
  const { options, positional } = parseCliArguments(process.argv.slice(2));
  const [tag, out, sourceCommit] = positional;
  if (!tag) throw new Error('Missing release tag');
  // Reject unsupported versions and missing opt-in before reading any files.
  const isV2 = assertSupportedVersion(tag, options).core[0] === '2';
  const infoOptions = {
    dryRun: options.dryRun,
    allowV2Beta: options.allowV2Beta,
    allowEphemeralSlotKey: options.allowEphemeralSlotKey,
    ...(options.slotPublicKeyHex !== undefined
      ? { slotPublicKeyHex: options.slotPublicKeyHex }
      : {}),
  };
  if (isV2) {
    if (!options.pairingPath)
      throw new Error('v2 beta release info requires --pairing embedded-release-pairing.json');
    infoOptions.pairing = JSON.parse(readFileSync(options.pairingPath, 'utf8'));
    if (options.slotArchives.length > 0) {
      infoOptions.slotArchives = options.slotArchives;
    }
    if (options.slotInfoPath) {
      infoOptions.slot = JSON.parse(readFileSync(options.slotInfoPath, 'utf8'));
    }
  }
  const sha =
    sourceCommit || execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim();
  const info = generateReleaseInfo(
    tag,
    sha,
    readFileSync(`docs/release-notes/${tag}-zh.md`, 'utf8'),
    infoOptions
  );
  if (out) writeFileSync(out, JSON.stringify(info, null, 2) + '\n');
};

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  try {
    runCli();
  } catch (error) {
    console.error(error instanceof Error ? error.message : String(error));
    process.exitCode = 1;
  }
}
