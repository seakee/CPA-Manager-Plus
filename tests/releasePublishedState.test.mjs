import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { gunzipSync, gzipSync } from 'node:zlib';
import { generateSlotKeypair } from '../bin/release/cpamp-slot-sign.mjs';
import { packageSlotArchive } from '../bin/release/package-cpamp-slot.mjs';
import { generateReleaseInfo } from '../bin/release/generate-release-info.mjs';
import { v2DockerRoleTag, v2SlotArchiveName } from '../bin/release/update-contract.mjs';
import { afterEach, describe, expect, it } from 'vitest';
import {
  buildExpectedReleaseAssets,
  expectedReleaseAssetNames,
  verifyDraftRelease,
  verifyPublishedRelease,
  verifyPublishedReleaseMetadata,
  verifyLocalReleaseAssets,
} from '../bin/release/verify-published-release.mjs';

const tag = 'v1.2.3-beta.1';
const temporaryDirectories = [];

const makeReleaseFixture = () => {
  const directory = mkdtempSync(path.join(tmpdir(), 'cpamp-published-release-'));
  temporaryDirectories.push(directory);
  const nativeDirectory = path.join(directory, 'native');
  mkdirSync(nativeDirectory);

  for (const assetName of expectedReleaseAssetNames(tag)) {
    const assetPath = ['management.html', 'release-info.json'].includes(assetName)
      ? path.join(directory, assetName)
      : path.join(nativeDirectory, assetName);
    const info =
      assetName === 'release-info.json'
        ? generateReleaseInfo(
            tag,
            'a'.repeat(40),
            '<!-- cpamp-update\n' +
              JSON.stringify({
                summary: { zh: '更新', en: 'Update' },
                update: {
                  breaking: false,
                  migration_required: false,
                  minimum_direct_upgrade_version: null,
                  upgrade_guide_url:
                    'https://github.com/seakee/CPA-Manager-Plus/releases/tag/' + tag,
                },
                compatibility: { minimum_cpa_version: null },
              }) +
              '\n-->'
          )
        : null;
    writeFileSync(assetPath, info ? JSON.stringify(info) : `${assetName}\n`);
  }
  return directory;
};

const publishedAsset = ({ name, size, digest }) => ({
  name,
  size,
  digest,
  state: 'uploaded',
});

const makePublishedRelease = ({ assets, overrides = {} }) => ({
  tag_name: tag,
  draft: false,
  prerelease: true,
  immutable: true,
  published_at: '2026-08-12T00:00:00Z',
  body: '# Release\n',
  assets: assets.map(publishedAsset),
  ...overrides,
});

const makeDraftRelease = ({ assets, overrides = {} }) => ({
  tag_name: tag,
  draft: true,
  prerelease: true,
  immutable: false,
  published_at: null,
  body: '# Release\n',
  assets: assets.map(publishedAsset),
  ...overrides,
});

afterEach(() => {
  for (const directory of temporaryDirectories.splice(0)) {
    rmSync(directory, { recursive: true, force: true });
  }
});

describe('published release verification', () => {
  it('builds the complete deterministic asset manifest and accepts an exact Release', () => {
    const assets = buildExpectedReleaseAssets(makeReleaseFixture(), tag);
    expect(assets.map((asset) => asset.name)).toEqual(expectedReleaseAssetNames(tag));

    expect(
      verifyPublishedRelease({
        release: makePublishedRelease({ assets }),
        tag,
        prerelease: true,
        body: '# Release\n',
        assets,
      })
    ).toEqual({
      expectedAssets: 9,
      publishedAssets: 9,
      missingAssets: [],
      complete: true,
      immutable: true,
    });
  });

  it('accepts a complete draft Release before final publication', () => {
    const assets = buildExpectedReleaseAssets(makeReleaseFixture(), tag);

    expect(
      verifyDraftRelease({
        release: makeDraftRelease({ assets }),
        tag,
        prerelease: true,
        body: '# Release\n',
        assets,
      })
    ).toEqual({
      expectedAssets: 9,
      publishedAssets: 9,
      missingAssets: [],
      complete: true,
      immutable: false,
    });
  });

  it('allows a draft with matching missing assets to be completed', () => {
    const assets = buildExpectedReleaseAssets(makeReleaseFixture(), tag);
    const release = makeDraftRelease({ assets: assets.slice(1) });

    expect(() =>
      verifyDraftRelease({
        release,
        tag,
        prerelease: true,
        body: '# Release\n',
        assets,
      })
    ).toThrow('asset count mismatch');

    expect(
      verifyDraftRelease({
        release,
        tag,
        prerelease: true,
        body: '# Release\n',
        assets,
        allowMissingAssets: true,
      })
    ).toMatchObject({
      expectedAssets: 9,
      publishedAssets: 8,
      missingAssets: [{ name: assets[0].name, filePath: assets[0].filePath }],
      complete: false,
      immutable: false,
    });
  });

  it('allows only a matching missing-asset subset during an approved rerun', () => {
    const assets = buildExpectedReleaseAssets(makeReleaseFixture(), tag);
    const release = makePublishedRelease({
      assets: assets.slice(1),
      overrides: { immutable: false },
    });

    expect(() =>
      verifyPublishedRelease({
        release,
        tag,
        prerelease: true,
        body: '# Release',
        assets,
      })
    ).toThrow('asset count mismatch');

    expect(
      verifyPublishedRelease({
        release,
        tag,
        prerelease: true,
        body: '# Release',
        assets,
        allowMissingAssets: true,
      })
    ).toMatchObject({
      expectedAssets: 9,
      publishedAssets: 8,
      missingAssets: [{ name: assets[0].name, filePath: assets[0].filePath }],
      complete: false,
    });

    expect(() =>
      verifyPublishedRelease({
        release: makePublishedRelease({ assets: assets.slice(1) }),
        tag,
        prerelease: true,
        body: '# Release',
        assets,
        allowMissingAssets: true,
      })
    ).toThrow('is immutable and cannot resume');
  });

  it('rejects unexpected, digest-mismatched, or size-mismatched existing assets', () => {
    const assets = buildExpectedReleaseAssets(makeReleaseFixture(), tag);
    const assertRejected = (release, message) =>
      expect(() =>
        verifyPublishedRelease({
          release,
          tag,
          prerelease: true,
          body: '# Release',
          assets,
          allowMissingAssets: true,
        })
      ).toThrow(message);

    assertRejected(
      makePublishedRelease({
        assets: [...assets.slice(1), { ...assets[0], name: 'unexpected.tar.gz' }],
      }),
      'unexpected asset'
    );
    assertRejected(
      makePublishedRelease({
        assets: assets.map((asset, index) =>
          index === 0 ? { ...asset, digest: `sha256:${'0'.repeat(64)}` } : asset
        ),
      }),
      'digest mismatch'
    );
    assertRejected(
      makePublishedRelease({
        assets: assets.map((asset, index) =>
          index === 0 ? { ...asset, size: asset.size + 1 } : asset
        ),
      }),
      'size mismatch'
    );
  });

  it('rejects body and published-state drift', () => {
    const assets = buildExpectedReleaseAssets(makeReleaseFixture(), tag);
    const assertRejected = (release, message) =>
      expect(() =>
        verifyPublishedRelease({
          release,
          tag,
          prerelease: true,
          body: '# Release',
          assets,
        })
      ).toThrow(message);

    assertRejected(
      makePublishedRelease({ assets, overrides: { body: '# Other' } }),
      'body differs'
    );
    assertRejected(makePublishedRelease({ assets, overrides: { draft: true } }), 'draft mismatch');
    assertRejected(
      makePublishedRelease({ assets, overrides: { prerelease: false } }),
      'prerelease mismatch'
    );
    assertRejected(
      makePublishedRelease({ assets, overrides: { published_at: null } }),
      'is not published'
    );

    expect(() =>
      verifyDraftRelease({
        release: makeDraftRelease({ assets, overrides: { published_at: '2026-08-12T00:00:00Z' } }),
        tag,
        prerelease: true,
        body: '# Release',
        assets,
      })
    ).toThrow('draft has an unexpected published_at value');
  });

  it('requires a complete healthy asset set for Telegram recovery', () => {
    const assets = buildExpectedReleaseAssets(makeReleaseFixture(), tag);
    expect(
      verifyPublishedReleaseMetadata({
        release: makePublishedRelease({ assets }),
        tag,
        prerelease: true,
        body: '# Release',
      })
    ).toMatchObject({ complete: true, expectedAssets: 9, publishedAssets: 9 });

    expect(() =>
      verifyPublishedReleaseMetadata({
        release: makePublishedRelease({ assets: assets.slice(1) }),
        tag,
        prerelease: true,
        body: '# Release',
      })
    ).toThrow('asset count mismatch');

    const invalidAssets = assets.map(publishedAsset);
    invalidAssets[0] = { ...invalidAssets[0], state: 'new' };
    expect(() =>
      verifyPublishedReleaseMetadata({
        release: makePublishedRelease({ assets, overrides: { assets: invalidAssets } }),
        tag,
        prerelease: true,
        body: '# Release',
      })
    ).toThrow('is not uploaded');
  });
});

const v2Tag = 'v2.0.0-beta.1';
const v2SourceCommit = '8935bce5ea7ce994185ee97acb8a4fdbd9cdd6cc';
const v2ComposeContent = [
  'name: cpa-manager-plus',
  'services:',
  '  cpa-manager-plus:',
  `    image: seakee/cpa-manager-plus:2.0.0-beta.1-full@sha256:${'6'.repeat(64)}`,
  '',
].join('\n');
const v2ComposeDigest = `sha256:${createHash('sha256').update(v2ComposeContent).digest('hex')}`;
const v2PairingRecord = () => ({
  schema_version: 1,
  version: v2Tag,
  source_commit: v2SourceCommit,
  platform: 'linux/amd64',
  cpa: {
    version: '8.0.55',
    artifact_id: 'sha256:e48c85c75a19adae186cbee590f6e6143fd8085417d8066b545e8533877175f7',
  },
  fullweb: {
    manifest_sha256: `sha256:${'1'.repeat(64)}`,
    index_html_sha256: `sha256:${'2'.repeat(64)}`,
    asset_count: 3,
  },
  api_contract: { sha256: `sha256:${'3'.repeat(64)}` },
  compose: {
    filename: 'docker-compose.v2-embedded.yml',
    sha256: v2ComposeDigest,
    size: Buffer.byteLength(v2ComposeContent, 'utf8'),
  },
  images: {
    slim: {
      repository: 'seakee/cpa-manager-plus',
      tag: v2DockerRoleTag(v2Tag, 'slim'),
      image_ref: `seakee/cpa-manager-plus:2.0.0-beta.1@sha256:${'5'.repeat(64)}`,
      manifest_digest: `sha256:${'5'.repeat(64)}`,
      image_config_digest: `sha256:${'7'.repeat(64)}`,
      binary_sha256: `sha256:${'8'.repeat(64)}`,
      release_provenance_sha256: `sha256:${'9'.repeat(64)}`,
    },
    full: {
      repository: 'seakee/cpa-manager-plus',
      tag: v2DockerRoleTag(v2Tag, 'full'),
      image_ref: `seakee/cpa-manager-plus:2.0.0-beta.1-full@sha256:${'6'.repeat(64)}`,
      manifest_digest: `sha256:${'6'.repeat(64)}`,
      image_config_digest: `sha256:${'a'.repeat(64)}`,
      binary_sha256: `sha256:${'8'.repeat(64)}`,
      release_provenance_sha256: `sha256:${'b'.repeat(64)}`,
    },
  },
});
const v2Notes = () =>
  '<!-- cpamp-update\n' +
  JSON.stringify({
    summary: { zh: '更新', en: 'Update' },
    update: {
      breaking: true,
      migration_required: true,
      minimum_direct_upgrade_version: 'v1.14.10',
      upgrade_guide_url: `https://github.com/seakee/CPA-Manager-Plus/releases/tag/${v2Tag}`,
    },
    compatibility: { minimum_cpa_version: 'v8.0.55' },
  }) +
  '\n-->';
const makeV2ReleaseFixture = () => {
  const directory = mkdtempSync(path.join(tmpdir(), 'cpamp-v2-published-release-'));
  temporaryDirectories.push(directory);
  const nativeDirectory = path.join(directory, 'native');
  mkdirSync(nativeDirectory);
  const info = generateReleaseInfo(v2Tag, v2SourceCommit, v2Notes(), {
    dryRun: true,
    allowV2Beta: true,
    pairing: v2PairingRecord(),
  });
  writeFileSync(path.join(directory, 'release-info.json'), JSON.stringify(info, null, 2));
  for (const name of expectedReleaseAssetNames(v2Tag, { dryRun: true, allowV2Beta: true })) {
    if (name === 'release-info.json') continue;
    const assetPath = [
      'management.html',
      'checksums.txt',
      'docker-compose.v2-embedded.yml',
      'embedded-release-pairing.json',
    ].includes(name)
      ? path.join(directory, name)
      : path.join(nativeDirectory, name);
    writeFileSync(
      assetPath,
      name === 'embedded-release-pairing.json'
        ? JSON.stringify(v2PairingRecord())
        : name === 'docker-compose.v2-embedded.yml'
          ? v2ComposeContent
          : `${name}\n`
    );
  }
  return directory;
};
const v2Release = ({ assets, overrides = {} }) => ({
  tag_name: v2Tag,
  draft: false,
  prerelease: true,
  immutable: true,
  published_at: '2026-11-06T00:00:00Z',
  body: '# v2 Beta\n',
  assets: assets.map(publishedAsset),
  ...overrides,
});

describe('v2 beta published release verification', () => {
  const checksummedFixture = () => {
    const assetsDir = makeV2ReleaseFixture();
    const assets = buildExpectedReleaseAssets(assetsDir, v2Tag, { dryRun: true, allowV2Beta: true });
    writeFileSync(
      path.join(assetsDir, 'checksums.txt'),
      assets
        .filter(({ name }) => name !== 'checksums.txt')
        .map(
          ({ filePath, digest }) =>
            `${digest.slice(7)}  ${path.relative(assetsDir, filePath).replaceAll('\\', '/')}\n`
        )
        .join('')
    );
    return assetsDir;
  };

  it('verifies six local assets and all five checksums without a remote Release object', () => {
    const assetsDir = checksummedFixture();
    expect(verifyLocalReleaseAssets({ tag: v2Tag, assetsDir, dryRun: true, allowV2Beta: true }))
      .toMatchObject({ mode: 'assets', expectedAssets: 6, complete: true });
    expect(() => verifyLocalReleaseAssets({ tag: v2Tag, assetsDir, allowV2Beta: true })).toThrow(/fail-closed/);
    writeFileSync(path.join(assetsDir, 'management.html'), 'changed panel bytes');
    expect(() => verifyLocalReleaseAssets({ tag: v2Tag, assetsDir, dryRun: true, allowV2Beta: true })).toThrow(/checksums/);
  });

  it.each(['duplicate', 'missing', 'extra', 'invalid'])('requires an exact checksum list: %s', (change) => {
    const assetsDir = checksummedFixture();
    const checksumPath = path.join(assetsDir, 'checksums.txt');
    const lines = readFileSync(checksumPath, 'utf8').trimEnd().split('\n');
    if (change === 'duplicate') lines.push(lines[0]);
    if (change === 'missing') lines.pop();
    if (change === 'extra') lines.push(`${'a'.repeat(64)}  stable-version.txt`);
    if (change === 'invalid') lines[0] = `${'a'.repeat(64)}  ../management.html`;
    writeFileSync(checksumPath, lines.join('\n') + '\n');
    expect(() => verifyLocalReleaseAssets({ tag: v2Tag, assetsDir, dryRun: true, allowV2Beta: true })).toThrow(/checksums/);
  });

  it('rejects checksum entries whose relative path disagrees with the discovered asset path', () => {
    // (a) Root asset management.html erroneously given a native/ prefix.
    const rootWithNativePrefix = checksummedFixture();
    const checksumPathA = path.join(rootWithNativePrefix, 'checksums.txt');
    writeFileSync(
      checksumPathA,
      readFileSync(checksumPathA, 'utf8').replace('  management.html\n', '  native/management.html\n')
    );
    expect(() =>
      verifyLocalReleaseAssets({
        tag: v2Tag,
        assetsDir: rootWithNativePrefix,
        dryRun: true,
        allowV2Beta: true,
      })
    ).toThrow(/checksums/);

    // (b1) Native asset in native/ subdirectory missing the native/ prefix in checksums.txt.
    const nativeMissingPrefix = checksummedFixture();
    const nativeFilename = `cpa-manager-plus-native-v2_${v2Tag}_linux_amd64.tar.gz`;
    const checksumPathB1 = path.join(nativeMissingPrefix, 'checksums.txt');
    writeFileSync(
      checksumPathB1,
      readFileSync(checksumPathB1, 'utf8').replace(
        `  native/${nativeFilename}\n`,
        `  ${nativeFilename}\n`
      )
    );
    expect(() =>
      verifyLocalReleaseAssets({
        tag: v2Tag,
        assetsDir: nativeMissingPrefix,
        dryRun: true,
        allowV2Beta: true,
      })
    ).toThrow(/checksums/);

    // (b2) Native asset placed at root directory but listed with native/ prefix in checksums.txt.
    const nativeAtRootWithPrefix = checksummedFixture();
    const stagedNativePath = path.join(nativeAtRootWithPrefix, 'native', nativeFilename);
    writeFileSync(
      path.join(nativeAtRootWithPrefix, nativeFilename),
      readFileSync(stagedNativePath)
    );
    rmSync(stagedNativePath);
    expect(() =>
      verifyLocalReleaseAssets({
        tag: v2Tag,
        assetsDir: nativeAtRootWithPrefix,
        dryRun: true,
        allowV2Beta: true,
      })
    ).toThrow(/checksums/);
  });

  it.each([['--dry-run', '--allow-v2-beta'], ['--dry-run', 'true', '--allow-v2-beta', 'true']])(
    'accepts bare and valued boolean flags for local assets: %j', (...flags) => {
      const result = execFileSync(process.execPath, [
        fileURLToPath(new URL('../bin/release/verify-published-release.mjs', import.meta.url)),
        '--tag', v2Tag, '--mode', 'assets', '--assets-dir', checksummedFixture(), ...flags,
      ], { encoding: 'utf8' });
      expect(JSON.parse(result)).toMatchObject({ mode: 'assets', complete: true, expectedAssets: 6 });
    }
  );

  it('accepts the exact v2 asset set published as a GitHub Prerelease', () => {
    const assets = buildExpectedReleaseAssets(makeV2ReleaseFixture(), v2Tag, {
      dryRun: true,
      allowV2Beta: true,
    });
    expect(assets.map(({ name }) => name)).toEqual(
      expectedReleaseAssetNames(v2Tag, { dryRun: true, allowV2Beta: true })
    );
    expect(assets).toHaveLength(6);
    expect(assets.map(({ name }) => name)).toContain('docker-compose.v2-embedded.yml');
    expect(assets.map(({ name }) => name)).toContain(
      `cpa-manager-plus-native-v2_${v2Tag}_linux_amd64.tar.gz`
    );
    expect(() =>
      verifyPublishedRelease({
        release: v2Release({ assets }),
        tag: v2Tag,
        prerelease: true,
        body: '# v2 Beta\n',
        assets,
      })
    ).toThrow(/fail-closed/);
    expect(
      verifyPublishedRelease({
        release: v2Release({ assets }),
        tag: v2Tag,
        prerelease: true,
        body: '# v2 Beta\n',
        assets,
        dryRun: true,
        allowV2Beta: true,
      })
    ).toEqual({
      expectedAssets: 6,
      publishedAssets: 6,
      missingAssets: [],
      complete: true,
      immutable: true,
    });
  });

  it('requires a Prerelease that never becomes GitHub Latest', () => {
    const assets = buildExpectedReleaseAssets(makeV2ReleaseFixture(), v2Tag, {
      dryRun: true,
      allowV2Beta: true,
    });
    const verify = (release, prerelease) =>
      verifyPublishedRelease({
        release,
        tag: v2Tag,
        prerelease,
        body: '# v2 Beta\n',
        assets,
        dryRun: true,
        allowV2Beta: true,
      });
    expect(() => verify(v2Release({ assets, overrides: { prerelease: false } }), false)).toThrow(
      /GitHub Prerelease/
    );
    expect(() => verify(v2Release({ assets, overrides: { make_latest: 'true' } }), true)).toThrow(
      /must not be made Latest/
    );
  });

  it('rejects stable-version.txt and 1.x or cross-platform native packages', () => {
    const directory = makeV2ReleaseFixture();
    writeFileSync(path.join(directory, 'stable-version.txt'), 'v1.14.10\n');
    expect(() =>
      buildExpectedReleaseAssets(directory, v2Tag, { dryRun: true, allowV2Beta: true })
    ).toThrow(/unexpected assets: stable-version.txt/);

    const legacy = makeV2ReleaseFixture();
    writeFileSync(
      path.join(legacy, 'native', `cpa-manager-plus_${v2Tag}_darwin_arm64.tar.gz`),
      'x\n'
    );
    expect(() =>
      buildExpectedReleaseAssets(legacy, v2Tag, { dryRun: true, allowV2Beta: true })
    ).toThrow(/unexpected assets/);

    const crossPlatform = makeV2ReleaseFixture();
    writeFileSync(
      path.join(crossPlatform, 'native', `cpa-manager-plus-native-v2_${v2Tag}_linux_arm64.tar.gz`),
      'x\n'
    );
    expect(() =>
      buildExpectedReleaseAssets(crossPlatform, v2Tag, { dryRun: true, allowV2Beta: true })
    ).toThrow(/unexpected assets/);
  });

  it('rejects a schema_version 1 release-info.json inside the v2 asset set', () => {
    const directory = makeV2ReleaseFixture();
    writeFileSync(
      path.join(directory, 'release-info.json'),
      JSON.stringify(
        generateReleaseInfo(
          'v1.14.10',
          'a'.repeat(40),
          '<!-- cpamp-update\n' +
            JSON.stringify({
              summary: { zh: '更新', en: 'Update' },
              update: {
                breaking: false,
                migration_required: false,
                minimum_direct_upgrade_version: null,
                upgrade_guide_url:
                  'https://github.com/seakee/CPA-Manager-Plus/releases/tag/v1.14.10',
              },
              compatibility: { minimum_cpa_version: null },
            }) +
            '\n-->'
        )
      )
    );
    expect(() =>
      buildExpectedReleaseAssets(directory, v2Tag, { dryRun: true, allowV2Beta: true })
    ).toThrow(/schema_version 2/);
  });

  it('requires management.html and the embedded compose asset', () => {
    for (const missing of [
      'management.html',
      'docker-compose.v2-embedded.yml',
      'embedded-release-pairing.json',
    ]) {
      const directory = makeV2ReleaseFixture();
      rmSync(path.join(directory, missing));
      expect(() =>
        buildExpectedReleaseAssets(directory, v2Tag, { dryRun: true, allowV2Beta: true })
      ).toThrow(new RegExp(`Missing expected v2 release asset: ${missing.replaceAll('.', '\\.')}`));
    }
  });

  it('rejects a tampered compose file or pairing record', () => {
    const composeDir = makeV2ReleaseFixture();
    writeFileSync(path.join(composeDir, 'docker-compose.v2-embedded.yml'), 'services: {}\n');
    expect(() =>
      buildExpectedReleaseAssets(composeDir, v2Tag, { dryRun: true, allowV2Beta: true })
    ).toThrow(/compose asset digest does not match release info/);

    const pairingDir = makeV2ReleaseFixture();
    const tamperedPairing = v2PairingRecord();
    tamperedPairing.fullweb.manifest_sha256 = `sha256:${'9'.repeat(64)}`;
    writeFileSync(
      path.join(pairingDir, 'embedded-release-pairing.json'),
      JSON.stringify(tamperedPairing)
    );
    expect(() =>
      buildExpectedReleaseAssets(pairingDir, v2Tag, { dryRun: true, allowV2Beta: true })
    ).toThrow(/Full Web digest does not match release info/);

    const imageDir = makeV2ReleaseFixture();
    const renamedImage = v2PairingRecord();
    renamedImage.images.slim.manifest_digest = `sha256:${'c'.repeat(64)}`;
    renamedImage.images.slim.image_ref = `seakee/cpa-manager-plus:2.0.0-beta.1@${renamedImage.images.slim.manifest_digest}`;
    writeFileSync(
      path.join(imageDir, 'embedded-release-pairing.json'),
      JSON.stringify(renamedImage)
    );
    expect(() =>
      buildExpectedReleaseAssets(imageDir, v2Tag, { dryRun: true, allowV2Beta: true })
    ).toThrow(/slim digest does not match release info/);
  });

  it('ignores release-notes.md like 1.x but rejects unexpected subdirectories', () => {
    const directory = makeV2ReleaseFixture();
    writeFileSync(path.join(directory, 'release-notes.md'), '# reviewed notes\n');
    expect(
      buildExpectedReleaseAssets(directory, v2Tag, { dryRun: true, allowV2Beta: true }).map(
        ({ name }) => name
      )
    ).toEqual(expectedReleaseAssetNames(v2Tag, { dryRun: true, allowV2Beta: true }));

    const nested = makeV2ReleaseFixture();
    mkdirSync(path.join(nested, 'extra'));
    expect(() =>
      buildExpectedReleaseAssets(nested, v2Tag, { dryRun: true, allowV2Beta: true })
    ).toThrow(/Unexpected v2 release asset directory: extra/);
  });

  it('requires DRY_RUN and --allow-v2-beta together on the verification CLI', () => {
    const directory = makeV2ReleaseFixture();
    const assets = buildExpectedReleaseAssets(directory, v2Tag, {
      dryRun: true,
      allowV2Beta: true,
    });
    const releaseJson = path.join(directory, 'github-release.json');
    writeFileSync(releaseJson, JSON.stringify(v2Release({ assets })));
    const bodyPath = path.join(directory, 'body.md');
    writeFileSync(bodyPath, '# v2 Beta\n');
    const script = fileURLToPath(
      new URL('../bin/release/verify-published-release.mjs', import.meta.url)
    );
    const baseArgs = [
      script,
      '--tag',
      v2Tag,
      '--release-json',
      releaseJson,
      '--body-path',
      bodyPath,
      '--prerelease',
      'true',
      '--mode',
      'metadata',
    ];
    const env = { ...process.env };
    delete env.DRY_RUN;

    expect(() =>
      execFileSync(process.execPath, [...baseArgs, '--allow-v2-beta', 'true'], {
        env,
        stdio: 'pipe',
      })
    ).toThrow(/v2 release verification is fail-closed/);

    const stdout = execFileSync(
      process.execPath,
      [...baseArgs, '--allow-v2-beta', 'true', '--dry-run', 'true'],
      { env, stdio: 'pipe' }
    ).toString();
    expect(stdout).toContain('6/6');
  });

  it('never reports an incomplete or mutated v2 asset set as complete', () => {
    const assets = buildExpectedReleaseAssets(makeV2ReleaseFixture(), v2Tag, {
      dryRun: true,
      allowV2Beta: true,
    });
    const base = {
      tag: v2Tag,
      prerelease: true,
      body: '# v2 Beta\n',
      dryRun: true,
      allowV2Beta: true,
    };
    expect(verifyPublishedRelease({ ...base, release: v2Release({ assets }), assets })).toEqual({
      expectedAssets: 6,
      publishedAssets: 6,
      missingAssets: [],
      complete: true,
      immutable: true,
    });
    const native = assets.find(({ name }) => name.includes('_linux_amd64'));
    const extra = { name: 'stable-version.txt', size: 3, digest: `sha256:${'a'.repeat(64)}` };
    const cases = [
      { expected: [], published: [], error: /asset set mismatch/ },
      {
        expected: assets.filter(({ name }) => name !== 'docker-compose.v2-embedded.yml'),
        published: assets.filter(({ name }) => name !== 'docker-compose.v2-embedded.yml'),
        error: /asset set mismatch/,
      },
      {
        expected: assets,
        published: assets.filter(({ name }) => name !== 'management.html'),
        error: /asset set mismatch/,
      },
      { expected: [...assets, native], published: [...assets, native], error: /duplicate/i },
      { expected: [...assets, extra], published: [...assets, extra], error: /stable-version\.txt/ },
    ];
    for (const { expected, published, error } of cases)
      expect(() =>
        verifyPublishedRelease({
          ...base,
          release: v2Release({ assets: published }),
          assets: expected,
        })
      ).toThrow(error);
  });

  it('rejects the missing-asset relaxation for v2 releases and drafts', () => {
    const assets = buildExpectedReleaseAssets(makeV2ReleaseFixture(), v2Tag, {
      dryRun: true,
      allowV2Beta: true,
    });
    const partial = assets.filter(({ name }) => name !== 'management.html');
    const common = {
      tag: v2Tag,
      prerelease: true,
      body: '# v2 Beta\n',
      assets,
      allowMissingAssets: true,
      allowV2Beta: true,
      dryRun: true,
    };
    expect(() =>
      verifyPublishedRelease({
        ...common,
        release: v2Release({ assets: partial, overrides: { immutable: false } }),
      })
    ).toThrow(/does not allow missing assets/);
    expect(() =>
      verifyDraftRelease({
        ...common,
        release: v2Release({
          assets: partial,
          overrides: { draft: true, published_at: null, immutable: false },
        }),
      })
    ).toThrow(/does not allow missing assets/);
  });

  it('limits the v2 dry-run side effect to the local --result-path report', () => {
    const assetsDir = makeV2ReleaseFixture();
    const metadataDir = mkdtempSync(path.join(tmpdir(), 'cpamp-v2-cli-'));
    temporaryDirectories.push(metadataDir);
    const assets = buildExpectedReleaseAssets(assetsDir, v2Tag, {
      dryRun: true,
      allowV2Beta: true,
    });
    const releaseJson = path.join(metadataDir, 'github-release.json');
    writeFileSync(releaseJson, JSON.stringify(v2Release({ assets })));
    const bodyPath = path.join(metadataDir, 'body.md');
    writeFileSync(bodyPath, '# v2 Beta\n');
    const resultPath = path.join(metadataDir, 'result.json');
    const script = fileURLToPath(
      new URL('../bin/release/verify-published-release.mjs', import.meta.url)
    );
    const env = { ...process.env };
    delete env.DRY_RUN;
    const args = [
      script,
      '--tag',
      v2Tag,
      '--release-json',
      releaseJson,
      '--body-path',
      bodyPath,
      '--prerelease',
      'true',
      '--mode',
      'artifact',
      '--assets-dir',
      assetsDir,
      '--allow-v2-beta',
      'true',
      '--dry-run',
      'true',
    ];

    // The only write is the local diagnostic report; no remote call exists in
    // this verifier, and the report is explicitly part of the dry-run contract.
    const stdout = execFileSync(process.execPath, [...args, '--result-path', resultPath], {
      env,
      stdio: 'pipe',
    }).toString();
    expect(stdout).toContain('6/6');
    expect(JSON.parse(readFileSync(resultPath, 'utf8'))).toMatchObject({
      expectedAssets: 6,
      publishedAssets: 6,
      complete: true,
    });

    // The 1.x resume relaxation is never a v2 authorization.
    expect(() =>
      execFileSync(process.execPath, [...args, '--allow-missing-assets', 'true'], {
        env,
        stdio: 'pipe',
      })
    ).toThrow(/does not allow missing assets/);
  });

  const buildSignedSlotBytes = (targetTag) => {
    const stageRoot = mkdtempSync(path.join(tmpdir(), 'cpamp-pubstate-stage-'));
    temporaryDirectories.push(stageRoot);
    const writeMember = (rel, body, mode = 0o644) => {
      const full = path.join(stageRoot, rel);
      mkdirSync(path.dirname(full), { recursive: true });
      writeFileSync(full, body);
      chmodSync(full, mode);
    };
    writeMember('bin/cpamp-launcher', '#!/bin/sh\necho launcher\n', 0o755);
    writeMember('bin/cpamp-runtime-supervisor', '#!/bin/sh\necho supervisor\n', 0o755);
    writeMember('bin/cpa-manager-plus', '#!/bin/sh\necho manager\n', 0o755);
    writeMember('bin/cpamp-ingress', '#!/bin/sh\necho ingress\n', 0o755);
    writeMember(
      'share/cpamp/release/base-abi.json',
      JSON.stringify({ schema_version: 1, base_epoch: 1, platform: 'linux/amd64' }) + '\n',
      0o444
    );
    writeMember('share/cpamp/release/fullweb-manifest.json', '{"schemaVersion":1}\n', 0o444);
    writeMember('share/cpamp/release/public-api-transport-contract.json', '{"schemaVersion":1}\n', 0o444);
    writeMember('share/cpamp/runtime/cpa-artifact.json', '{"schemaVersion":1}\n', 0o444);
    writeMember('share/cpamp/runtime/config.seed.yaml', 'port: 8317\n', 0o644);
    writeMember('share/cpamp/runtime/seed-lineage.json', '{"schema_version":1}\n', 0o644);
    writeMember('share/licenses/cpamp/LICENSE', 'MIT\n', 0o444);
    const outDir = mkdtempSync(path.join(tmpdir(), 'cpamp-pubstate-slotout-'));
    temporaryDirectories.push(outDir);
    const pair = generateSlotKeypair('cpamp-dry-run-2026');
    const pkg = packageSlotArchive({
      root: stageRoot,
      version: targetTag,
      sourceCommit: v2SourceCommit,
      platform: 'linux/amd64',
      keyId: 'cpamp-dry-run-2026',
      privateKey: pair.privateKeyPem,
      publicKeyHex: pair.publicKeyHex,
      out: outDir,
    });
    return { bytes: readFileSync(pkg.archivePath), publicKeyHex: pair.publicKeyHex, archivePath: pkg.archivePath };
  };

  const makeV2TagFixture = (targetTag, { includeSlot = true, tamperSlotInInfo = false } = {}) => {
    const directory = mkdtempSync(path.join(tmpdir(), `cpamp-${targetTag}-`));
    temporaryDirectories.push(directory);
    const nativeDirectory = path.join(directory, 'native');
    mkdirSync(nativeDirectory);
    const pairing = v2PairingRecord();
    pairing.version = targetTag;
    for (const role of ['slim', 'full']) {
      pairing.images[role].tag = v2DockerRoleTag(targetTag, role);
      pairing.images[role].image_ref =
        `${pairing.images[role].repository}:${pairing.images[role].tag}@${pairing.images[role].manifest_digest}`;
    }
    const slotName = v2SlotArchiveName(targetTag, 'amd64');
    const slotFixture = includeSlot ? buildSignedSlotBytes(targetTag) : null;
    const slotBytes = slotFixture ? slotFixture.bytes : Buffer.alloc(0);
    const slotDigest = `sha256:${createHash('sha256').update(slotBytes).digest('hex')}`;
    const slotInfo = includeSlot
      ? {
          assets: [
            {
              name: slotName,
              platform: 'linux/amd64',
              sha256: tamperSlotInInfo ? `sha256:${'f'.repeat(64)}` : slotDigest,
              size: slotBytes.length,
              min_base_epoch: 1,
              schema_generation: 1,
            },
          ],
        }
      : undefined;
    const info = generateReleaseInfo(targetTag, v2SourceCommit, v2Notes(), {
      dryRun: true,
      allowV2Beta: true,
      pairing,
      ...(slotInfo ? { slot: slotInfo } : {}),
    });
    const writtenFiles = [];
    const recordAsset = (filePath, contents) => {
      writeFileSync(filePath, contents);
      writtenFiles.push({ filePath, contents });
    };
    recordAsset(path.join(directory, 'release-info.json'), JSON.stringify(info, null, 2));
    for (const name of expectedReleaseAssetNames(targetTag, { dryRun: true, allowV2Beta: true })) {
      if (name === 'release-info.json' || name === 'checksums.txt') continue;
      const assetPath = [
        'management.html',
        'docker-compose.v2-embedded.yml',
        'embedded-release-pairing.json',
      ].includes(name)
        ? path.join(directory, name)
        : path.join(nativeDirectory, name);
      const body =
        name === 'embedded-release-pairing.json'
          ? JSON.stringify(pairing)
          : name === 'docker-compose.v2-embedded.yml'
            ? v2ComposeContent
            : name === slotName
              ? slotBytes
              : `${name}\n`;
      recordAsset(assetPath, body);
    }
    if (includeSlot && targetTag === v2Tag) {
      recordAsset(path.join(nativeDirectory, slotName), slotBytes);
    }
    writeFileSync(
      path.join(directory, 'checksums.txt'),
      writtenFiles
        .map(
          ({ filePath, contents }) =>
            `${createHash('sha256').update(contents).digest('hex')}  ${path
              .relative(directory, filePath)
              .replaceAll('\\', '/')}\n`
        )
        .join('')
    );
    return { directory, slotName, slotPublicKeyHex: slotFixture?.publicKeyHex, slotArchivePath: slotFixture?.archivePath };
  };

  it('verifies v2.0.0-beta.2 seven-asset releases including distribution.slot binding', () => {
    const beta2Tag = 'v2.0.0-beta.2';
    const { directory, slotName, slotPublicKeyHex, slotArchivePath } = makeV2TagFixture(beta2Tag, { includeSlot: true });
    expect(() =>
      verifyLocalReleaseAssets({
        tag: beta2Tag,
        assetsDir: directory,
        dryRun: true,
        allowV2Beta: true,
        allowEphemeralSlotKey: true,
      })
    ).toThrow(/Ephemeral slot key verification requires --slot-public-key/);
    expect(
      verifyLocalReleaseAssets({
        tag: beta2Tag,
        assetsDir: directory,
        dryRun: true,
        allowV2Beta: true,
        allowEphemeralSlotKey: true,
        slotPublicKeyHex,
      })
    ).toMatchObject({ mode: 'assets', expectedAssets: 7, complete: true });

    const assets = buildExpectedReleaseAssets(directory, beta2Tag, {
      dryRun: true,
      allowV2Beta: true,
      allowEphemeralSlotKey: true,
      slotPublicKeyHex,
    });
    const release = v2Release({
      assets,
      overrides: { tag_name: beta2Tag },
    });
    expect(
      verifyPublishedRelease({
        release,
        tag: beta2Tag,
        prerelease: true,
        body: '# v2 Beta\n',
        assets,
        allowV2Beta: true,
        dryRun: true,
      })
    ).toMatchObject({ expectedAssets: 7, publishedAssets: 7, complete: true });
    expect(
      verifyPublishedReleaseMetadata({
        release,
        tag: beta2Tag,
        prerelease: true,
        body: '# v2 Beta\n',
        allowV2Beta: true,
        dryRun: true,
      })
    ).toMatchObject({ expectedAssets: 7, publishedAssets: 7, complete: true });

    // Reject tampered slot digest in release-info.json
    const { directory: badDigestDir, slotPublicKeyHex: badDigestPubKey } = makeV2TagFixture(beta2Tag, {
      includeSlot: true,
      tamperSlotInInfo: true,
    });
    expect(() =>
      buildExpectedReleaseAssets(badDigestDir, beta2Tag, {
        dryRun: true,
        allowV2Beta: true,
        allowEphemeralSlotKey: true,
        slotPublicKeyHex: badDigestPubKey,
      })
    ).toThrow(/slot asset digest does not match/);
    // Reject bit-flipped Ed25519 signature in ephemeral cpamp-dry-run-2026 slot archive
    const bitFlipExtractDir = mkdtempSync(path.join(tmpdir(), 'cpamp-pubstate-bitflip-'));
    temporaryDirectories.push(bitFlipExtractDir);
    execFileSync('tar', ['-xzf', slotArchivePath, '-C', bitFlipExtractDir]);
    const bitFlipSigPath = path.join(bitFlipExtractDir, 'slot-manifest.json.sig');
    chmodSync(bitFlipSigPath, 0o644);
    const bitFlipSigObj = JSON.parse(readFileSync(bitFlipSigPath, 'utf8'));
    const bitFlipSigBytes = Buffer.from(bitFlipSigObj.signature, 'base64');
    bitFlipSigBytes[0] ^= 0x01;
    bitFlipSigObj.signature = bitFlipSigBytes.toString('base64');
    writeFileSync(bitFlipSigPath, JSON.stringify(bitFlipSigObj, null, 2) + '\n');
    chmodSync(bitFlipSigPath, 0o444);
    const bitFlipArchiveDir = mkdtempSync(path.join(tmpdir(), 'cpamp-pubstate-bitflip-out-'));
    temporaryDirectories.push(bitFlipArchiveDir);
    const bitFlipArchivePath = path.join(bitFlipArchiveDir, slotName);
    execFileSync('tar', ['-czf', bitFlipArchivePath, '-C', bitFlipExtractDir, '.']);
    execFileSync('chmod', ['-R', 'u+rwX', bitFlipExtractDir]);
    const bitFlipBytes = readFileSync(bitFlipArchivePath);
    const bitFlipInfo = JSON.parse(readFileSync(path.join(directory, 'release-info.json'), 'utf8'));
    bitFlipInfo.distribution.slot.assets[0].sha256 = `sha256:${createHash('sha256').update(bitFlipBytes).digest('hex')}`;
    bitFlipInfo.distribution.slot.assets[0].size = bitFlipBytes.length;
    const bitFlipDir = mkdtempSync(path.join(tmpdir(), 'cpamp-pubstate-bitflip-dir-'));
    temporaryDirectories.push(bitFlipDir);
    execFileSync('cp', ['-R', `${directory}/.`, bitFlipDir]);
    writeFileSync(path.join(bitFlipDir, 'release-info.json'), JSON.stringify(bitFlipInfo, null, 2));
    writeFileSync(path.join(bitFlipDir, 'native', slotName), bitFlipBytes);
    const bitFlipChecksums = readFileSync(path.join(bitFlipDir, 'checksums.txt'), 'utf8')
      .trimEnd()
      .split('\n')
      .map((line) => {
        const rel = line.slice(66);
        const abs = path.join(bitFlipDir, rel);
        return `${createHash('sha256').update(readFileSync(abs)).digest('hex')}  ${rel}\n`;
      })
      .join('');
    writeFileSync(path.join(bitFlipDir, 'checksums.txt'), bitFlipChecksums);
    expect(() =>
      verifyLocalReleaseAssets({
        tag: beta2Tag,
        assetsDir: bitFlipDir,
        dryRun: true,
        allowV2Beta: true,
        allowEphemeralSlotKey: true,
        slotPublicKeyHex,
      })
    ).toThrow(/signature verification failed/);

    // Reject missing slot asset on disk in beta.2
    rmSync(path.join(directory, 'native', slotName));
    expect(() =>
      buildExpectedReleaseAssets(directory, beta2Tag, {
        dryRun: true,
        allowV2Beta: true,
        allowEphemeralSlotKey: true,
        slotPublicKeyHex,
      })
    ).toThrow(/Missing expected v2 release asset/);

    // Modifying 1 byte inside bin/cpa-manager-plus without re-signing slot-manifest.json
    // must fail verifyLocalReleaseAssets even when release-info.json & checksums.txt match the re-gzipped archive
    const modTar = Buffer.from(gunzipSync(readFileSync(slotArchivePath)));
    let offset = 0;
    let flipped = false;
    while (offset + 512 <= modTar.length) {
      const hdr = modTar.subarray(offset, offset + 512);
      if (hdr.every((b) => b === 0)) break;
      const name = hdr.subarray(0, 100).toString('utf8').replace(/\0.*$/, '');
      const size = Number.parseInt(hdr.subarray(124, 136).toString('utf8').replace(/\0.*$/, '').trim() || '0', 8);
      const dataStart = offset + 512;
      if (name === 'bin/cpa-manager-plus') {
        modTar[dataStart] ^= 0x01;
        flipped = true;
        break;
      }
      offset = dataStart + Math.ceil(size / 512) * 512;
    }
    expect(flipped).toBe(true);
    const modBytes = gzipSync(modTar);
    const modDir = mkdtempSync(path.join(tmpdir(), 'cpamp-pubstate-modbyte-dir-'));
    temporaryDirectories.push(modDir);
    execFileSync('cp', ['-R', `${directory}/.`, modDir]);
    const modInfo = JSON.parse(readFileSync(path.join(modDir, 'release-info.json'), 'utf8'));
    modInfo.distribution.slot.assets[0].sha256 = `sha256:${createHash('sha256').update(modBytes).digest('hex')}`;
    modInfo.distribution.slot.assets[0].size = modBytes.length;
    writeFileSync(path.join(modDir, 'release-info.json'), JSON.stringify(modInfo, null, 2));
    writeFileSync(path.join(modDir, 'native', slotName), modBytes);
    const modChecksums = readFileSync(path.join(modDir, 'checksums.txt'), 'utf8')
      .trimEnd()
      .split('\n')
      .map((line) => {
        const rel = line.slice(66);
        const abs = path.join(modDir, rel);
        return `${createHash('sha256').update(readFileSync(abs)).digest('hex')}  ${rel}\n`;
      })
      .join('');
    writeFileSync(path.join(modDir, 'checksums.txt'), modChecksums);
    expect(() =>
      verifyLocalReleaseAssets({
        tag: beta2Tag,
        assetsDir: modDir,
        dryRun: true,
        allowV2Beta: true,
        allowEphemeralSlotKey: true,
        slotPublicKeyHex,
      })
    ).toThrow(/sha256 mismatch/);
  });

  it('accepts optional 7-asset slot publication for v2.0.0-beta.1 and rejects half-wired slot state', () => {
    const { directory, slotName, slotPublicKeyHex } = makeV2TagFixture(v2Tag, { includeSlot: true });
    expect(
      verifyLocalReleaseAssets({
        tag: v2Tag,
        assetsDir: directory,
        dryRun: true,
        allowV2Beta: true,
        allowEphemeralSlotKey: true,
        slotPublicKeyHex,
      })
    ).toMatchObject({ mode: 'assets', expectedAssets: 7, complete: true });
    const assets = buildExpectedReleaseAssets(directory, v2Tag, {
      dryRun: true,
      allowV2Beta: true,
      allowEphemeralSlotKey: true,
      slotPublicKeyHex,
    });
    expect(
      verifyPublishedReleaseMetadata({
        release: v2Release({ assets }),
        tag: v2Tag,
        prerelease: true,
        body: '# v2 Beta\n',
        allowV2Beta: true,
        dryRun: true,
      })
    ).toMatchObject({ expectedAssets: 7, publishedAssets: 7, complete: true });

    // Half-wired: slot file on disk when release-info.json omits distribution.slot
    const sixFileDir = makeV2ReleaseFixture();
    writeFileSync(path.join(sixFileDir, 'native', slotName), 'extra-slot\n');
    expect(() =>
      buildExpectedReleaseAssets(sixFileDir, v2Tag, { dryRun: true, allowV2Beta: true })
    ).toThrow(/lacks distribution\.slot/);
  });
});
