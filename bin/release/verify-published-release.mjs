import { createHash } from 'node:crypto';
import {
  validateInfo,
  assertSupportedVersion,
  assertV2Rehearsal,
  assertV2AssetBindings,
  assertV2AssetSet,
  assertBooleanOption,
  expectedV2AssetNames,
  isV2Version,
  parseVersion,
  v2EmbeddedComposeFilename,
  v2PairingFilename,
} from './update-contract.mjs';
import { existsSync, readFileSync, readdirSync, statSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { parseReleaseTag } from './validate-release.mjs';

const fail = (message) => {
  throw new Error(message);
};

const normalizeBody = (body) =>
  String(body ?? '')
    .replace(/\r\n?/g, '\n')
    .trimEnd();
const sha256DigestPattern = /^sha256:[0-9a-f]{64}$/i;

export const expectedReleaseAssetNames = (tag, options = {}) => {
  parseReleaseTag(tag);
  if (assertSupportedVersion(tag, options).core[0] === '2') return expectedV2AssetNames(tag);
  const prefix = `cpa-manager-plus_${tag}`;
  return [
    'checksums.txt',
    'release-info.json',
    `${prefix}_darwin_amd64.tar.gz`,
    `${prefix}_darwin_arm64.tar.gz`,
    `${prefix}_linux_amd64.tar.gz`,
    `${prefix}_linux_arm64.tar.gz`,
    `${prefix}_windows_amd64.zip`,
    `${prefix}_windows_arm64.zip`,
    'management.html',
  ].sort((left, right) => left.localeCompare(right));
};

const sha256File = (filePath) =>
  `sha256:${createHash('sha256').update(readFileSync(filePath)).digest('hex')}`;

const assetRecord = (filePath) => {
  const stats = statSync(filePath);
  if (!stats.isFile() || stats.size === 0) fail(`Expected a non-empty release asset: ${filePath}`);

  return {
    name: path.basename(filePath),
    size: stats.size,
    digest: sha256File(filePath),
    filePath,
  };
};

const buildExpectedV2ReleaseAssets = (assetsDir, tag) => {
  const expected = expectedV2AssetNames(tag);
  const locate = (name) => {
    const candidates = [path.join(assetsDir, name), path.join(assetsDir, 'native', name)].filter(
      (candidate) => existsSync(candidate)
    );
    if (candidates.length === 0) fail(`Missing expected v2 release asset: ${name}`);
    if (candidates.length > 1) fail(`Duplicate v2 release asset: ${name}`);
    return assetRecord(candidates[0]);
  };
  const collectNames = (directory, { root }) => {
    if (!existsSync(directory)) return [];
    const names = [];
    for (const entry of readdirSync(directory, { withFileTypes: true })) {
      if (entry.isDirectory()) {
        if (root && entry.name === 'native') {
          names.push(...collectNames(path.join(directory, entry.name), { root: false }));
          continue;
        }
        fail(`Unexpected v2 release asset directory: ${entry.name}`);
      }
      if (!entry.isFile()) fail(`Unexpected v2 release asset entry: ${entry.name}`);
      // release.yml stages the reviewed notes for `--body-path`; like 1.x it is
      // never part of the published asset set.
      if (root && entry.name === 'release-notes.md') continue;
      names.push(entry.name);
    }
    return names;
  };
  const unexpected = collectNames(assetsDir, { root: true }).filter(
    (name) => !expected.includes(name)
  );
  if (unexpected.length > 0)
    fail(`v2 release assets directory contains unexpected assets: ${unexpected.sort().join(', ')}`);

  return expected.map(locate).sort((left, right) => left.name.localeCompare(right.name));
};

export const buildExpectedReleaseAssets = (assetsDir, tag, options = {}) => {
  // Gate M3 at the exported boundary, before the first readFileSync.
  assertV2Rehearsal(tag, options);
  if (assertSupportedVersion(tag, options).core[0] === '2') {
    const info = JSON.parse(readFileSync(path.join(assetsDir, 'release-info.json'), 'utf8'));
    validateInfo(info, tag, options);
    const assets = buildExpectedV2ReleaseAssets(assetsDir, tag);
    const byName = new Map(assets.map((asset) => [asset.name, asset]));
    const compose = byName.get(v2EmbeddedComposeFilename);
    const pairingRecord = byName.get(v2PairingFilename);
    if (!compose || !pairingRecord) fail('v2 release assets are missing the compose pairing');
    assertV2AssetBindings(info, {
      composeSha256: compose.digest,
      pairing: JSON.parse(readFileSync(pairingRecord.filePath, 'utf8')),
    });
    return assets;
  }
  validateInfo(JSON.parse(readFileSync(path.join(assetsDir, 'release-info.json'), 'utf8')), tag);
  const managementPath = path.join(assetsDir, 'management.html');
  const nativeDir = path.join(assetsDir, 'native');
  const nativeAssets = readdirSync(nativeDir, { withFileTypes: true })
    .filter((entry) => entry.isFile())
    .map((entry) => assetRecord(path.join(nativeDir, entry.name)));
  const assets = [
    assetRecord(managementPath),
    assetRecord(path.join(assetsDir, 'release-info.json')),
    ...nativeAssets,
  ].sort((left, right) => left.name.localeCompare(right.name));
  const actualNames = assets.map(({ name }) => name);
  const expectedNames = expectedReleaseAssetNames(tag);
  if (
    actualNames.length !== expectedNames.length ||
    actualNames.some((name, index) => name !== expectedNames[index])
  ) {
    fail(
      `Built release asset set mismatch: expected ${expectedNames.join(', ')}, received ${actualNames.join(', ')}`
    );
  }
  return assets;
};

const verifyReleaseIdentity = ({ release, tag, prerelease, body, draft, allowV2Beta = false }) => {
  parseReleaseTag(tag);
  const version = assertSupportedVersion(tag, { allowV2Beta });
  if (version.core[0] === '2') {
    // Scope 9: the v2 first Beta is a GitHub Prerelease and must never take the
    // `releases/latest` pointer, so CPA's latest/download/management.html path
    // keeps serving the newest 1.x stable asset.
    if (prerelease !== true) fail(`v2 Beta ${tag} must be published as a GitHub Prerelease`);
    if (release?.make_latest === 'true' || release?.make_latest === true)
      fail(`v2 Beta ${tag} must not be made Latest`);
  }
  if (!release || typeof release !== 'object') fail('GitHub Release response must be an object');
  if (release.tag_name !== tag) {
    fail(`Existing GitHub Release tag mismatch: expected ${tag}, received ${release.tag_name}`);
  }
  if (release.draft !== draft) {
    fail(
      `Existing GitHub Release ${tag} draft mismatch: expected ${draft}, received ${release.draft}`
    );
  }
  if (release.prerelease !== prerelease) {
    fail(
      `Existing GitHub Release prerelease mismatch: expected ${prerelease}, received ${release.prerelease}`
    );
  }
  if (draft) {
    if (release.published_at !== null) {
      fail(`Existing GitHub Release ${tag} draft has an unexpected published_at value`);
    }
  } else if (typeof release.published_at !== 'string' || release.published_at.trim() === '') {
    fail(`Existing GitHub Release ${tag} is not published`);
  }
  if (normalizeBody(release.body) !== normalizeBody(body)) {
    fail(`Existing GitHub Release ${tag} body differs from the checked release notes`);
  }
};

const sortedPublishedAssets = (release) => {
  if (!Array.isArray(release.assets)) fail('Existing GitHub Release assets must be an array');
  const assets = [...release.assets].sort((left, right) =>
    String(left.name).localeCompare(String(right.name))
  );
  for (let index = 1; index < assets.length; index += 1) {
    if (assets[index - 1].name === assets[index].name) {
      fail(`Existing GitHub Release contains duplicate asset name: ${assets[index].name}`);
    }
  }
  return assets;
};

const verifyUploadedAssetMetadata = (asset) => {
  if (asset.state !== 'uploaded') {
    fail(`Existing GitHub Release asset ${asset.name || '<unnamed>'} is not uploaded`);
  }
  if (!Number.isInteger(asset.size) || asset.size <= 0) {
    fail(`Existing GitHub Release asset ${asset.name || '<unnamed>'} has an invalid size`);
  }
  if (typeof asset.digest !== 'string' || !sha256DigestPattern.test(asset.digest)) {
    fail(`Existing GitHub Release asset ${asset.name || '<unnamed>'} has no valid SHA-256 digest`);
  }
};

const verifyReleaseAssets = ({ release, tag, assets, allowMissingAssets }) => {
  const actualAssets = sortedPublishedAssets(release);
  if (isV2Version(tag)) {
    // A v2 Release has exactly six files: an incomplete or extra set can never
    // be reported as complete, and the 1.x resume relaxation does not apply.
    if (allowMissingAssets === true) fail('v2 release verification does not allow missing assets');
    assertV2AssetSet(
      assets.map((asset) => asset.name),
      tag
    );
    assertV2AssetSet(
      actualAssets.map((asset) => asset.name),
      tag
    );
  }
  const expectedByName = new Map(assets.map((asset) => [asset.name, asset]));

  for (const actual of actualAssets) {
    const expected = expectedByName.get(actual.name);
    if (!expected) fail(`Existing GitHub Release contains unexpected asset: ${actual.name}`);
    verifyUploadedAssetMetadata(actual);
    if (actual.size !== expected.size) {
      fail(
        `Existing GitHub Release asset ${actual.name} size mismatch: expected ${expected.size}, received ${actual.size}`
      );
    }
    if (actual.digest !== expected.digest) {
      fail(
        `Existing GitHub Release asset ${actual.name} digest mismatch: expected ${expected.digest}, received ${actual.digest}`
      );
    }
  }

  const actualNames = new Set(actualAssets.map(({ name }) => name));
  const missingAssets = assets
    .filter(({ name }) => !actualNames.has(name))
    .map(({ name, filePath }) => ({ name, filePath }));
  if (missingAssets.length > 0 && !allowMissingAssets) {
    fail(
      `Existing GitHub Release asset count mismatch: expected ${assets.length}, received ${actualAssets.length}`
    );
  }
  if (missingAssets.length > 0 && release.immutable === true) {
    fail(`Existing GitHub Release ${tag} is immutable and cannot resume missing assets`);
  }

  return {
    expectedAssets: assets.length,
    publishedAssets: actualAssets.length,
    missingAssets,
    complete: missingAssets.length === 0,
    immutable: release.immutable === true,
  };
};

export const verifyPublishedRelease = ({
  release,
  tag,
  prerelease,
  body,
  assets,
  allowMissingAssets = false,
  allowV2Beta = false,
  dryRun = false,
}) => {
  // Gate M3 at the exported boundary: v2 verification is a rehearsal only.
  assertV2Rehearsal(tag, { allowV2Beta, dryRun });
  allowMissingAssets = assertBooleanOption({ allowMissingAssets }, 'allowMissingAssets');
  verifyReleaseIdentity({ release, tag, prerelease, body, draft: false, allowV2Beta });
  return verifyReleaseAssets({ release, tag, assets, allowMissingAssets });
};

export const verifyDraftRelease = ({
  release,
  tag,
  prerelease,
  body,
  assets,
  allowMissingAssets = false,
  allowV2Beta = false,
  dryRun = false,
}) => {
  assertV2Rehearsal(tag, { allowV2Beta, dryRun });
  allowMissingAssets = assertBooleanOption({ allowMissingAssets }, 'allowMissingAssets');
  verifyReleaseIdentity({ release, tag, prerelease, body, draft: true, allowV2Beta });
  return verifyReleaseAssets({
    release,
    tag,
    assets,
    allowMissingAssets,
  });
};

export const verifyPublishedReleaseMetadata = ({
  release,
  tag,
  prerelease,
  body,
  allowV2Beta = false,
  dryRun = false,
}) => {
  assertV2Rehearsal(tag, { allowV2Beta, dryRun });
  verifyReleaseIdentity({ release, tag, prerelease, body, draft: false, allowV2Beta });
  const actualAssets = sortedPublishedAssets(release);
  if (isV2Version(tag))
    assertV2AssetSet(
      actualAssets.map((asset) => asset.name),
      tag
    );
  const expectedNames = expectedReleaseAssetNames(tag, { allowV2Beta });
  if (actualAssets.length !== expectedNames.length) {
    fail(
      `Existing GitHub Release asset count mismatch: expected ${expectedNames.length}, received ${actualAssets.length}`
    );
  }

  for (let index = 0; index < expectedNames.length; index += 1) {
    const actual = actualAssets[index];
    if (actual.name !== expectedNames[index]) {
      fail(
        `Existing GitHub Release asset mismatch: expected ${expectedNames[index]}, received ${actual.name}`
      );
    }
    verifyUploadedAssetMetadata(actual);
  }

  return {
    expectedAssets: expectedNames.length,
    publishedAssets: actualAssets.length,
    missingAssets: [],
    complete: true,
    immutable: release.immutable === true,
  };
};

const parseArguments = (argumentsList) => {
  const options = {};
  for (let index = 0; index < argumentsList.length; index += 2) {
    const name = argumentsList[index];
    const value = argumentsList[index + 1];
    if (!name?.startsWith('--') || value === undefined) fail('Expected --name value arguments');
    const key = name.slice(2);
    if (options[key] !== undefined) fail(`Duplicate argument: ${name}`);
    options[key] = value;
  }
  return options;
};

const requiredOption = (options, name) => {
  if (options[name] === undefined) fail(`Missing required argument: --${name}`);
  return options[name];
};

const booleanOption = (options, name, fallback = false) => {
  const value = options[name];
  if (value === undefined) return fallback;
  if (value !== 'true' && value !== 'false') fail(`--${name} must be true or false`);
  return value === 'true';
};

const runCli = () => {
  const options = parseArguments(process.argv.slice(2));
  const allowed = new Set([
    'tag',
    'assets-dir',
    'body-path',
    'release-json',
    'prerelease',
    'mode',
    'allow-missing-assets',
    'allow-v2-beta',
    'dry-run',
    'result-path',
  ]);
  const unknown = Object.keys(options).filter((key) => !allowed.has(key));
  if (unknown.length > 0) fail(`Unknown arguments: ${unknown.map((key) => `--${key}`).join(', ')}`);

  const tag = requiredOption(options, 'tag');
  const allowV2Beta = booleanOption(options, 'allow-v2-beta');
  const dryRun = booleanOption(options, 'dry-run', process.env.DRY_RUN === 'true');
  // Gate M3: v2 verification is a rehearsal only, exactly like the producers.
  // Dry-run scope for v2: zero GitHub Release, update-index, image-tag and
  // registry writes. The only permitted side effect is the local `--result-path`
  // diagnostic report (stdout remains the authoritative result).
  if (parseVersion(tag).core[0] === '2' && !(allowV2Beta && dryRun))
    fail(
      'v2 release verification is fail-closed (Gate M3); DRY_RUN and --allow-v2-beta are both required'
    );
  const bodyPath = requiredOption(options, 'body-path');
  const releaseJsonPath = requiredOption(options, 'release-json');
  const prerelease = booleanOption(options, 'prerelease');
  const mode = options.mode || 'artifact';
  if (mode !== 'artifact' && mode !== 'draft' && mode !== 'metadata') {
    fail('--mode must be artifact, draft, or metadata');
  }

  const release = JSON.parse(readFileSync(releaseJsonPath, 'utf8'));
  const body = readFileSync(bodyPath, 'utf8');
  const result =
    mode === 'metadata'
      ? verifyPublishedReleaseMetadata({ release, tag, prerelease, body, allowV2Beta, dryRun })
      : mode === 'draft'
        ? verifyDraftRelease({
            release,
            tag,
            prerelease,
            body,
            assets: buildExpectedReleaseAssets(requiredOption(options, 'assets-dir'), tag, {
              allowV2Beta,
              dryRun,
            }),
            allowMissingAssets: booleanOption(options, 'allow-missing-assets'),
            allowV2Beta,
            dryRun,
          })
        : verifyPublishedRelease({
            release,
            tag,
            prerelease,
            body,
            assets: buildExpectedReleaseAssets(requiredOption(options, 'assets-dir'), tag, {
              allowV2Beta,
              dryRun,
            }),
            allowMissingAssets: booleanOption(options, 'allow-missing-assets'),
            allowV2Beta,
            dryRun,
          });

  if (options['result-path']) {
    writeFileSync(options['result-path'], `${JSON.stringify(result, null, 2)}\n`);
  }
  console.log(
    `Existing GitHub Release ${tag} has ${result.publishedAssets}/${result.expectedAssets} checked assets${
      result.complete ? '' : `; ${result.missingAssets.length} may be resumed`
    }${result.immutable ? ' and is immutable' : ''}.`
  );
  if (!result.immutable) {
    console.warn(
      `::warning::GitHub Release ${tag} is mutable; enable Immutable Releases before the next production tag.`
    );
  }
};

const entryPoint = process.argv[1]
  ? fileURLToPath(import.meta.url) === path.resolve(process.argv[1])
  : false;
if (entryPoint) {
  try {
    runCli();
  } catch (error) {
    console.error(error instanceof Error ? error.message : String(error));
    process.exitCode = 1;
  }
}
