#!/usr/bin/env node
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import {
  chmodSync,
  copyFileSync,
  existsSync,
  lstatSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  realpathSync,
  renameSync,
  rmSync,
  statSync,
  utimesSync,
  writeFileSync,
} from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import {
  TRUSTED_SLOT_PUBLIC_KEYS,
  assertValidKeyId,
  assertValidPublicKeyHex,
  compareCodeUnits,
  formatCanonicalJson,
  signSlotManifestBytes,
  sortKeysDeep,
  verifySlotManifestBytes,
} from './cpamp-slot-sign.mjs';
import { parseVersion } from './update-contract.mjs';

export const REQUIRED_SLOT_BINARIES = Object.freeze([
  'bin/cpamp-launcher',
  'bin/cpamp-runtime-supervisor',
  'bin/cpa-manager-plus',
  'bin/cpamp-ingress',
]);

export const REQUIRED_SLOT_SHARE_FILES = Object.freeze([
  'share/cpamp/release/base-abi.json',
  'share/cpamp/release/fullweb-manifest.json',
  'share/cpamp/release/public-api-transport-contract.json',
  'share/cpamp/runtime/cpa-artifact.json',
  'share/cpamp/runtime/config.seed.yaml',
  'share/cpamp/runtime/seed-lineage.json',
]);

export const OPTIONAL_SLOT_SHARE_FILES = Object.freeze([
  'share/cpamp/release/third-party-inventory.txt',
  'share/cpamp/release/source-dependency-inventory.json',
]);

const SUPPORTED_SLOT_PLATFORMS = new Set(['linux/amd64', 'linux/arm64']);
const SOURCE_COMMIT_PATTERN = /^[0-9a-f]{40}$/;
const MAX_TAR_BUFFER = 512 * 1024 * 1024;

function assertSafeRelativeMemberPath(relPath) {
  if (
    typeof relPath !== 'string' ||
    !relPath ||
    relPath.startsWith('/') ||
    relPath.startsWith('./') ||
    relPath.includes('\\')
  ) {
    throw new Error(`Unsafe slot member path: ${String(relPath)}`);
  }
  const parts = relPath.split('/');
  if (parts.some((part) => !part || part === '.' || part === '..')) {
    throw new Error(`Invalid relative slot member path: ${relPath}`);
  }
  return relPath;
}

function assertValidSlotVersion(version) {
  if (typeof version !== 'string' || !version.startsWith('v') || version.includes('+')) {
    throw new Error(`Slot version must be a canonical v2.x.y tag: ${String(version)}`);
  }
  const parsed = parseVersion(version);
  if (parsed.core[0] !== '2') {
    throw new Error(`Slot version must be v2.x.y: ${version}`);
  }
  return version;
}

function assertValidPlatform(platform) {
  if (!SUPPORTED_SLOT_PLATFORMS.has(platform)) {
    throw new Error(`Unsupported slot platform ${String(platform)} (expected linux/amd64 or linux/arm64)`);
  }
  return platform;
}

function assertPositiveInt(value, label) {
  const n = typeof value === 'string' && /^\d+$/.test(value.trim()) ? Number(value.trim()) : value;
  if (!Number.isSafeInteger(n) || n < 1) {
    throw new Error(`${label} must be an integer >= 1 (received ${String(value)})`);
  }
  return n;
}

function assertNoSymlinkPath(baseDir, relPath) {
  const parts = relPath.split('/');
  let cursor = baseDir;
  for (let i = 0; i < parts.length; i += 1) {
    cursor = path.join(cursor, parts[i]);
    if (!existsSync(cursor)) {
      throw new Error(`Missing required path in slot root: ${relPath}`);
    }
    const lst = lstatSync(cursor);
    if (lst.isSymbolicLink()) {
      throw new Error(`Slot root must not contain symlinks: ${path.relative(baseDir, cursor)}`);
    }
    if (i < parts.length - 1 && !lst.isDirectory()) {
      throw new Error(`Expected directory in slot root: ${path.relative(baseDir, cursor)}`);
    }
  }
  return cursor;
}

function detectRootPrefix(rootDir) {
  if (!existsSync(rootDir) || !statSync(rootDir).isDirectory()) {
    throw new Error(`Slot root does not exist or is not a directory: ${rootDir}`);
  }
  const dockerBin = path.join(rootDir, 'usr/local/bin');
  const dockerShare = path.join(rootDir, 'usr/local/share');
  if (
    existsSync(dockerBin) &&
    existsSync(dockerShare) &&
    !lstatSync(path.join(rootDir, 'usr')).isSymbolicLink() &&
    !lstatSync(path.join(rootDir, 'usr/local')).isSymbolicLink() &&
    !lstatSync(dockerBin).isSymbolicLink() &&
    !lstatSync(dockerShare).isSymbolicLink() &&
    lstatSync(dockerBin).isDirectory() &&
    lstatSync(dockerShare).isDirectory()
  ) {
    return 'usr/local/';
  }

  const flatBin = path.join(rootDir, 'bin');
  const flatShare = path.join(rootDir, 'share');
  if (
    existsSync(flatBin) &&
    existsSync(flatShare) &&
    !lstatSync(flatBin).isSymbolicLink() &&
    !lstatSync(flatShare).isSymbolicLink() &&
    lstatSync(flatBin).isDirectory() &&
    lstatSync(flatShare).isDirectory()
  ) {
    return '';
  }

  throw new Error(
    `Unrecognized slot root layout at ${rootDir}: expected usr/local/{bin,share} or {bin,share}`
  );
}

function collectLicenseFiles(rootDir, prefix) {
  const licensesRel = `${prefix}share/licenses`;
  const licensesAbs = assertNoSymlinkPath(rootDir, licensesRel);
  if (!lstatSync(licensesAbs).isDirectory()) {
    throw new Error(`Expected directory at ${licensesRel}`);
  }
  const files = [];
  function walk(dirAbs, relPrefix) {
    const entries = readdirSync(dirAbs, { withFileTypes: false }).sort(compareCodeUnits);
    for (const name of entries) {
      const childAbs = path.join(dirAbs, name);
      const lst = lstatSync(childAbs);
      const childRel = `${relPrefix}/${name}`;
      if (lst.isSymbolicLink()) {
        throw new Error(`Slot licenses directory must not contain symlinks: ${prefix}${childRel}`);
      }
      if (lst.isDirectory()) {
        walk(childAbs, childRel);
      } else if (lst.isFile()) {
        files.push({
          archivePath: assertSafeRelativeMemberPath(childRel),
          sourcePath: childAbs,
          mode: '0444',
        });
      } else {
        throw new Error(`Unsupported non-regular file in slot licenses: ${prefix}${childRel}`);
      }
    }
  }
  walk(licensesAbs, 'share/licenses');
  if (files.length === 0) {
    throw new Error(`Slot licenses directory is empty: ${licensesRel}`);
  }
  return files;
}

function resolvePrivateKeyInput(privateKeyOption) {
  const candidate = privateKeyOption ?? process.env.CPAMP_SLOT_PRIVATE_KEY;
  if (!candidate) {
    throw new Error('Missing --private-key (or CPAMP_SLOT_PRIVATE_KEY)');
  }
  if (typeof candidate === 'object' && candidate.type === 'private') {
    return candidate;
  }
  if (typeof candidate !== 'string' || !candidate.trim()) {
    throw new Error('Private key must not be empty');
  }
  const trimmed = candidate.trim();
  if (
    trimmed.startsWith('-----BEGIN') ||
    /^[0-9a-fA-F]{64}$/.test(trimmed) ||
    /^[0-9a-fA-F]{128}$/.test(trimmed)
  ) {
    return trimmed;
  }
  if (trimmed.startsWith('env:')) {
    const envName = trimmed.slice(4).trim();
    if (!envName || !process.env[envName]) {
      throw new Error(`Environment variable ${envName || '<empty>'} for --private-key is not set`);
    }
    return process.env[envName];
  }
  if (/^[A-Z][A-Z0-9_]*$/.test(trimmed) && process.env[trimmed]) {
    return process.env[trimmed];
  }
  if (existsSync(candidate) && statSync(candidate).isFile()) {
    return readFileSync(candidate, 'utf8');
  }
  throw new Error(`Private key file or environment variable not found: ${candidate}`);
}

function restoreWritableDirs(rootDir) {
  if (!existsSync(rootDir)) return;
  const dirs = [];
  function walk(current) {
    const lst = lstatSync(current);
    if (!lst.isDirectory() || lst.isSymbolicLink()) return;
    chmodSync(current, 0o755);
    dirs.push(current);
    for (const entry of readdirSync(current)) {
      walk(path.join(current, entry));
    }
  }
  walk(rootDir);
}

function buildDeterministicTarGz(stagingDir, memberPaths, tarBinary = 'tar') {
  const tmpListFile = path.join(path.dirname(stagingDir), 'members.txt');
  writeFileSync(tmpListFile, memberPaths.join('\n') + '\n', 'utf8');

  const tarVersion = execFileSync(tarBinary, ['--version'], { encoding: 'utf8' });
  const isGnuTar = /GNU tar/.test(tarVersion);
  const tarArgs = isGnuTar
    ? [
        '--owner=0',
        '--group=0',
        '--numeric-owner',
        '--sort=name',
        '--mtime=@0',
        '--format=posix',
        '--pax-option=delete=atime,delete=ctime',
        '--no-recursion',
        '-C',
        stagingDir,
        '-cf',
        '-',
        '-T',
        tmpListFile,
      ]
    : [
        '--uid',
        '0',
        '--gid',
        '0',
        '--uname',
        'root',
        '--gname',
        'root',
        '--no-mac-metadata',
        '--no-recursion',
        '-C',
        stagingDir,
        '-cf',
        '-',
        '-T',
        tmpListFile,
      ];

  const tarBytes = execFileSync(tarBinary, tarArgs, {
    env: isGnuTar ? process.env : { ...process.env, COPYFILE_DISABLE: '1' },
    maxBuffer: MAX_TAR_BUFFER,
  });
  return execFileSync('gzip', ['-n'], {
    input: tarBytes,
    maxBuffer: MAX_TAR_BUFFER,
  });
}

export function packageCpampSlot(options) {
  const rootDir = path.resolve(String(options.root || ''));
  const version = assertValidSlotVersion(options.version);
  const sourceCommit = String(options.sourceCommit || '');
  if (!SOURCE_COMMIT_PATTERN.test(sourceCommit)) {
    throw new Error(`Invalid --source-commit (expected 40 lowercase hex chars): ${sourceCommit}`);
  }
  const platform = assertValidPlatform(options.platform);
  const arch = platform.split('/')[1];
  const minBaseEpoch = assertPositiveInt(options.minBaseEpoch ?? 1, '--min-base-epoch');
  const schemaGeneration = assertPositiveInt(options.schemaGeneration ?? 1, '--schema-generation');
  const keyId = assertValidKeyId(options.keyId);
  const privateKey = resolvePrivateKeyInput(options.privateKey);
  if (!options.out) {
    throw new Error('Missing --out <dir-or-archive-path>');
  }

  const prefix = detectRootPrefix(rootDir);
  const stagedSources = [];

  for (const rel of REQUIRED_SLOT_BINARIES) {
    const abs = assertNoSymlinkPath(rootDir, `${prefix}${rel}`);
    const st = lstatSync(abs);
    if (!st.isFile() || st.size <= 0) {
      throw new Error(`Required slot binary must be a non-empty regular file: ${prefix}${rel}`);
    }
    stagedSources.push({ archivePath: assertSafeRelativeMemberPath(rel), sourcePath: abs, mode: '0555' });
  }

  for (const rel of REQUIRED_SLOT_SHARE_FILES) {
    const abs = assertNoSymlinkPath(rootDir, `${prefix}${rel}`);
    const st = lstatSync(abs);
    if (!st.isFile() || st.size <= 0) {
      throw new Error(`Required slot share file must be a non-empty regular file: ${prefix}${rel}`);
    }
    stagedSources.push({ archivePath: assertSafeRelativeMemberPath(rel), sourcePath: abs, mode: '0444' });
  }

  for (const rel of OPTIONAL_SLOT_SHARE_FILES) {
    const fullRel = `${prefix}${rel}`;
    const candidate = path.join(rootDir, fullRel);
    if (existsSync(candidate)) {
      const abs = assertNoSymlinkPath(rootDir, fullRel);
      const st = lstatSync(abs);
      if (!st.isFile() || st.size <= 0) {
        throw new Error(`Optional slot share file must be a non-empty regular file: ${fullRel}`);
      }
      stagedSources.push({ archivePath: assertSafeRelativeMemberPath(rel), sourcePath: abs, mode: '0444' });
    }
  }

  const baseAbiSource = stagedSources.find((item) => item.archivePath === 'share/cpamp/release/base-abi.json');
  const baseAbi = JSON.parse(readFileSync(baseAbiSource.sourcePath, 'utf8'));
  if (
    !baseAbi ||
    typeof baseAbi !== 'object' ||
    Array.isArray(baseAbi) ||
    baseAbi.schema_version !== 1 ||
    !Number.isSafeInteger(baseAbi.base_epoch) ||
    baseAbi.base_epoch < 1 ||
    baseAbi.platform !== platform
  ) {
    throw new Error(
      `Invalid base-abi.json in slot root (expected schema_version=1, base_epoch>=1, platform=${platform})`
    );
  }
  if (minBaseEpoch > baseAbi.base_epoch) {
    throw new Error(
      `min_base_epoch (${minBaseEpoch}) exceeds staged base-abi.json base_epoch (${baseAbi.base_epoch})`
    );
  }

  for (const licenseFile of collectLicenseFiles(rootDir, prefix)) {
    stagedSources.push(licenseFile);
  }

  stagedSources.sort((a, b) => compareCodeUnits(a.archivePath, b.archivePath));

  const filesMap = {};
  for (const item of stagedSources) {
    const bytes = readFileSync(item.sourcePath);
    filesMap[item.archivePath] = {
      size: bytes.length,
      sha256: `sha256:${createHash('sha256').update(bytes).digest('hex')}`,
      mode: item.mode,
    };
  }

  const manifest = sortKeysDeep({
    schema_version: 1,
    version,
    source_commit: sourceCommit,
    platform,
    min_base_epoch: minBaseEpoch,
    schema_generation: schemaGeneration,
    key_id: keyId,
    files: filesMap,
  });
  const manifestText = formatCanonicalJson(manifest);
  const manifestBytes = Buffer.from(manifestText, 'utf8');
  const signature = signSlotManifestBytes(manifestBytes, privateKey, keyId);
  const signatureText =
    JSON.stringify(
      {
        schema_version: signature.schema_version,
        algorithm: signature.algorithm,
        key_id: signature.key_id,
        signature: signature.signature,
      },
      null,
      2
    ) + '\n';

  if (options.publicKeyHex !== undefined && options.publicKeyHex !== null && options.publicKeyHex !== '') {
    const normalizedProvided = assertValidPublicKeyHex(options.publicKeyHex, 'publicKeyHex');
    const trustedHex = Object.hasOwn(TRUSTED_SLOT_PUBLIC_KEYS, keyId)
      ? TRUSTED_SLOT_PUBLIC_KEYS[keyId]
      : undefined;
    if (trustedHex && normalizedProvided !== trustedHex.toLowerCase()) {
      throw new Error(
        `Provided publicKeyHex for key_id ${keyId} does not match trusted slot keyring`
      );
    }
    verifySlotManifestBytes(manifestBytes, signature, {
      publicKeyHex: normalizedProvided,
      keyId,
    });
  } else {
    verifySlotManifestBytes(manifestBytes, signature, { keyId });
  }

  const archiveName = `cpamp-slot_${version}_linux_${arch}.tar.gz`;
  const outResolved = path.resolve(String(options.out));
  const archivePath =
    existsSync(outResolved) && statSync(outResolved).isDirectory()
      ? path.join(outResolved, archiveName)
      : outResolved.endsWith('.tar.gz')
        ? outResolved
        : path.join(outResolved, archiveName);
  mkdirSync(path.dirname(archivePath), { recursive: true });

  const tmpRoot = mkdtempSync(path.join(os.tmpdir(), 'cpamp-slot-pkg-'));
  const stagingDir = path.join(tmpRoot, 'stage');
  mkdirSync(stagingDir, { recursive: true });

  try {
    const dirSet = new Set();
    for (const item of stagedSources) {
      const target = path.join(stagingDir, item.archivePath);
      mkdirSync(path.dirname(target), { recursive: true });
      copyFileSync(item.sourcePath, target);
      const numericMode = Number.parseInt(item.mode, 8);
      chmodSync(target, numericMode);
      utimesSync(target, 0, 0);

      let dirRel = path.posix.dirname(item.archivePath);
      while (dirRel && dirRel !== '.') {
        dirSet.add(dirRel);
        dirRel = path.posix.dirname(dirRel);
      }
    }

    const manifestPath = path.join(stagingDir, 'slot-manifest.json');
    const sigPath = path.join(stagingDir, 'slot-manifest.json.sig');
    writeFileSync(manifestPath, manifestBytes);
    chmodSync(manifestPath, 0o444);
    utimesSync(manifestPath, 0, 0);
    writeFileSync(sigPath, signatureText, 'utf8');
    chmodSync(sigPath, 0o444);
    utimesSync(sigPath, 0, 0);

    const dirsDeepestFirst = [...dirSet].sort((a, b) => {
      const depthDiff = b.split('/').length - a.split('/').length;
      return depthDiff !== 0 ? depthDiff : compareCodeUnits(a, b);
    });
    for (const dirRel of dirsDeepestFirst) {
      const dirAbs = path.join(stagingDir, dirRel);
      const dirMode = dirRel === 'share/licenses' || dirRel.startsWith('share/licenses/') ? 0o555 : 0o755;
      chmodSync(dirAbs, dirMode);
      utimesSync(dirAbs, 0, 0);
    }

    const members = [
      ...dirSet,
      ...stagedSources.map((item) => item.archivePath),
      'slot-manifest.json',
      'slot-manifest.json.sig',
    ].sort(compareCodeUnits);

    const gzipBytes = buildDeterministicTarGz(stagingDir, members, options.tarBinary || 'tar');
    const tmpArchivePath = `${archivePath}.tmp-${process.pid}`;
    writeFileSync(tmpArchivePath, gzipBytes);
    renameSync(tmpArchivePath, archivePath);

    return {
      archivePath,
      archiveName,
      size: gzipBytes.length,
      sha256: `sha256:${createHash('sha256').update(gzipBytes).digest('hex')}`,
      manifest,
      signature,
    };
  } finally {
    restoreWritableDirs(tmpRoot);
    rmSync(tmpRoot, { recursive: true, force: true });
  }
}

export const packageSlotArchive = packageCpampSlot;

function resolveCliPublicKeyHex(flags) {
  const pubHexArg = flags['public-key-hex'];
  const pubArg = flags['public-key'];
  let resolvedFromPubArg;
  if (pubArg !== undefined) {
    const trimmed = String(pubArg).trim();
    if (/^[0-9a-fA-F]{64}$/.test(trimmed)) {
      resolvedFromPubArg = assertValidPublicKeyHex(trimmed, 'public-key');
    } else {
      if (!existsSync(trimmed) || !statSync(trimmed).isFile()) {
        throw new Error(`Slot public key file or hex not found: ${pubArg}`);
      }
      const raw = readFileSync(trimmed, 'utf8').trim();
      if (raw.startsWith('{')) {
        const parsed = JSON.parse(raw);
        if (parsed.key_id && flags['key-id'] && parsed.key_id !== flags['key-id']) {
          throw new Error(
            `Public key file key_id mismatch: expected ${flags['key-id']}, got ${parsed.key_id}`
          );
        }
        resolvedFromPubArg = assertValidPublicKeyHex(parsed.public_key_hex, 'public_key_hex');
      } else {
        resolvedFromPubArg = assertValidPublicKeyHex(raw, 'public-key');
      }
    }
  }
  let resolvedFromHexArg;
  if (pubHexArg !== undefined) {
    resolvedFromHexArg = assertValidPublicKeyHex(String(pubHexArg).trim(), 'public-key-hex');
  }
  if (resolvedFromPubArg && resolvedFromHexArg && resolvedFromPubArg !== resolvedFromHexArg) {
    throw new Error('--public-key and --public-key-hex disagree');
  }
  return resolvedFromHexArg ?? resolvedFromPubArg;
}

function parseCliArgs(args) {
  const parsed = {};
  for (let i = 0; i < args.length; i += 1) {
    const arg = args[i];
    if (arg === '--help' || arg === '-h') {
      parsed.help = true;
      continue;
    }
    if (!arg.startsWith('--')) {
      throw new Error(`Unexpected positional argument: ${arg}`);
    }
    const key = arg.slice(2);
    const val = args[i + 1];
    if (key === 'allow-ephemeral-slot-key' && (val === undefined || val.startsWith('--'))) {
      parsed[key] = 'true';
      continue;
    }
    if (val === undefined || val.startsWith('--')) {
      throw new Error(`Missing value for --${key}`);
    }
    parsed[key] = val;
    i += 1;
  }
  return parsed;
}

const isEntrypoint =
  process.argv[1] &&
  existsSync(process.argv[1]) &&
  realpathSync(fileURLToPath(import.meta.url)) === realpathSync(process.argv[1]);

if (isEntrypoint) {
  try {
    const flags = parseCliArgs(process.argv.slice(2));
    if (flags.help) {
      process.stdout.write(
        [
          'Usage: package-cpamp-slot.mjs',
          '  --root <staged-or-image-root>',
          '  --version <v2.x.y...>',
          '  --source-commit <40-hex>',
          '  --platform <linux/amd64|linux/arm64>',
          '  [--min-base-epoch <int>]',
          '  [--schema-generation <int>]',
          '  --key-id <id>',
          '  [--private-key <path-or-env>]',
          '  [--public-key <hex-or-pub-json>]',
          '  [--public-key-hex <64-hex>]',
          '  [--allow-ephemeral-slot-key]',
          '  --out <dir-or-archive-path>',
        ].join('\n') + '\n'
      );
      process.exit(0);
    }

    const publicKeyHex = resolveCliPublicKeyHex(flags);
    const result = packageCpampSlot({
      root: flags.root,
      version: flags.version,
      sourceCommit: flags['source-commit'],
      platform: flags.platform,
      minBaseEpoch: flags['min-base-epoch'] ?? 1,
      schemaGeneration: flags['schema-generation'] ?? 1,
      keyId: flags['key-id'],
      privateKey: flags['private-key'],
      ...(publicKeyHex ? { publicKeyHex } : {}),
      out: flags.out,
    });
    process.stdout.write(
      JSON.stringify(
        {
          archive_name: result.archiveName,
          archive_path: result.archivePath,
          platform: result.manifest.platform,
          version: result.manifest.version,
          min_base_epoch: result.manifest.min_base_epoch,
          schema_generation: result.manifest.schema_generation,
          sha256: result.sha256,
          size: result.size,
        },
        null,
        2
      ) + '\n'
    );
  } catch (error) {
    console.error(error instanceof Error ? error.message : String(error));
    process.exit(1);
  }
}
