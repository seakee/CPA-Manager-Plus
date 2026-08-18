import { createHash } from 'node:crypto';
import { readFileSync, readdirSync, statSync, writeFileSync } from 'node:fs';
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

export const expectedReleaseAssetNames = (tag) => {
  parseReleaseTag(tag);
  const prefix = `cpa-manager-plus_${tag}`;
  return [
    'checksums.txt',
    `${prefix}_darwin_amd64.tar.gz`,
    `${prefix}_darwin_arm64.tar.gz`,
    `${prefix}_linux_amd64.tar.gz`,
    `${prefix}_linux_arm64.tar.gz`,
    `${prefix}_windows_amd64.zip`,
    `${prefix}_windows_arm64.zip`,
    'management.html',
    'update-manifest.json',
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

export const buildExpectedReleaseAssets = (assetsDir, tag) => {
  const managementPath = path.join(assetsDir, 'management.html');
  const nativeDir = path.join(assetsDir, 'native');
  const nativeAssets = readdirSync(nativeDir, { withFileTypes: true })
    .filter((entry) => entry.isFile())
    .map((entry) => assetRecord(path.join(nativeDir, entry.name)));
  const assets = [assetRecord(managementPath), ...nativeAssets].sort((left, right) =>
    left.name.localeCompare(right.name)
  );
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

const verifyReleaseIdentity = ({ release, tag, prerelease, body, draft }) => {
  parseReleaseTag(tag);
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
}) => {
  verifyReleaseIdentity({ release, tag, prerelease, body, draft: false });
  return verifyReleaseAssets({ release, tag, assets, allowMissingAssets });
};

export const verifyDraftRelease = ({
  release,
  tag,
  prerelease,
  body,
  assets,
  allowMissingAssets = false,
}) => {
  verifyReleaseIdentity({ release, tag, prerelease, body, draft: true });
  return verifyReleaseAssets({
    release,
    tag,
    assets,
    allowMissingAssets,
  });
};

export const verifyPublishedReleaseMetadata = ({ release, tag, prerelease, body }) => {
  verifyReleaseIdentity({ release, tag, prerelease, body, draft: false });
  const actualAssets = sortedPublishedAssets(release);
  const expectedNames = expectedReleaseAssetNames(tag);
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
    'result-path',
  ]);
  const unknown = Object.keys(options).filter((key) => !allowed.has(key));
  if (unknown.length > 0) fail(`Unknown arguments: ${unknown.map((key) => `--${key}`).join(', ')}`);

  const tag = requiredOption(options, 'tag');
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
      ? verifyPublishedReleaseMetadata({ release, tag, prerelease, body })
      : mode === 'draft'
        ? verifyDraftRelease({
            release,
            tag,
            prerelease,
            body,
            assets: buildExpectedReleaseAssets(requiredOption(options, 'assets-dir'), tag),
            allowMissingAssets: booleanOption(options, 'allow-missing-assets'),
          })
        : verifyPublishedRelease({
            release,
            tag,
            prerelease,
            body,
            assets: buildExpectedReleaseAssets(requiredOption(options, 'assets-dir'), tag),
            allowMissingAssets: booleanOption(options, 'allow-missing-assets'),
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
