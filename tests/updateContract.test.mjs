import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';
import {
  assertSupportedVersion,
  assertV2AssetBindings,
  assertV2AssetSet,
  assertV2UpgradeGuideUrl,
  compareVersions,
  expectedV2AssetNames,
  parseVersion,
  resolveChannels,
  resolveV2Channels,
  validateIndex,
  validateV2Index,
  validateInfo,
  v2DockerRoleTag,
  v2DockerVersionTag,
  v2ImageRepositoryName,
  v2ImageRoles,
  v2NativeArchiveName,
  v2ReleaseInfoPath,
  v2SlotArchiveName,
  v2SlotRequiresAsset,
  validateV2Pairing,
  validateV2SlotDistribution,
} from '../bin/release/update-contract.mjs';
import { generateReleaseInfo } from '../bin/release/generate-release-info.mjs';
import {
  buildExpectedReleaseAssets,
  verifyDraftRelease,
  verifyPublishedRelease,
  verifyPublishedReleaseMetadata,
} from '../bin/release/verify-published-release.mjs';
import { resolveAliases, verifyCandidate } from '../bin/release/publish-update-index.mjs';
const corpus = JSON.parse(
  readFileSync(new URL('./fixtures/update-versions.json', import.meta.url), 'utf8')
);
const makeInfo = (tag) =>
  generateReleaseInfo(
    tag,
    'a'.repeat(40),
    '<!-- cpamp-update\n' +
      JSON.stringify({
        summary: { zh: '更新', en: 'Update' },
        update: {
          breaking: false,
          migration_required: false,
          minimum_direct_upgrade_version: null,
          upgrade_guide_url: 'https://github.com/seakee/CPA-Manager-Plus/releases/tag/' + tag,
        },
        compatibility: { minimum_cpa_version: null },
      }) +
      '\n-->'
  );
describe('update contract', () => {
  it('uses the same semantic ordering as the server', () => {
    corpus.ordered
      .slice(1)
      .forEach((tag, i) => expect(compareVersions(corpus.ordered[i], tag)).toBe(-1));
    corpus.equal.forEach(([a, b]) => expect(compareVersions(a, b)).toBe(0));
    corpus.invalid.forEach((tag) => expect(() => parseVersion(tag)).toThrow());
  });
  it('keeps stage and channel distinct and avoids backport alias regression', () => {
    const infos = ['v1.14.7', 'v1.14.8-beta.1', 'v1.13.10'].map(makeInfo);
    expect(resolveChannels(infos)).toEqual({
      stable: { version: 'v1.14.7' },
      rc: { version: 'v1.14.7' },
      beta: { version: 'v1.14.8-beta.1' },
    });
    expect(resolveAliases(infos, []).aliases).toEqual({
      latest: 'v1.14.7',
      preview: 'v1.14.8-beta.1',
      1.14: 'v1.14.7',
      1.13: 'v1.13.10',
    });
    expect(resolveChannels(infos, ['v1.14.8-beta.1']).beta.version).toBe('v1.14.7');
  });
  it('fails closed on invalid identity, distribution and incomplete release', () => {
    const info = makeInfo('v1.14.7');
    expect(() =>
      validateInfo({ ...info, release: { ...info.release, stage: 'rc' } }, 'v1.14.7')
    ).toThrow();
    expect(() =>
      verifyCandidate(
        {
          tag_name: 'v1.14.7',
          draft: false,
          immutable: true,
          published_at: 'now',
          prerelease: false,
          assets: [],
        },
        info,
        'a'.repeat(40)
      )
    ).toThrow();
    expect(() => generateReleaseInfo('v1.14.7', 'a'.repeat(40), 'No metadata')).toThrow();
  });
  it('rejects Manager-only v2 schema1 at the shared validator before channels or generator', () => {
    const historical = makeInfo('v1.14.7');
    const forged = {
      ...historical,
      release: { ...historical.release, version: 'v2.0.0', stage: 'stable' },
      distribution: {
        docker: { ...historical.distribution.docker, version_tag: 'v2.0.0' },
        native: {
          assets: historical.distribution.native.assets.map((asset) =>
            asset.replaceAll('v1.14.7', 'v2.0.0')
          ),
        },
      },
    };
    expect(validateInfo(historical, 'v1.14.7')).toBe(historical);
    expect(() => validateInfo(forged, 'v2.0.0')).toThrow(/v2 Embedded/);
    expect(() => resolveChannels([historical, forged])).toThrow(/v2 Embedded/);
    expect(() => makeInfo('v2.0.0')).toThrow(/v2 Embedded/);
  });
  it('generates recovery metadata from the explicit release SHA independently of the checkout', () => {
    const cwd = mkdtempSync(path.join(tmpdir(), 'cpamp-release-info-'));
    try {
      const tag = 'v1.14.7',
        sha = 'b'.repeat(40),
        expected = makeInfo(tag);
      mkdirSync(path.join(cwd, 'docs/release-notes'), { recursive: true });
      writeFileSync(
        path.join(cwd, `docs/release-notes/${tag}-zh.md`),
        '<!-- cpamp-update\n' +
          JSON.stringify({
            summary: expected.content.summary,
            update: expected.update,
            compatibility: expected.compatibility,
          }) +
          '\n-->'
      );
      const output = path.join(cwd, 'release-info.json');
      execFileSync(
        process.execPath,
        [
          fileURLToPath(new URL('../bin/release/generate-release-info.mjs', import.meta.url)),
          tag,
          output,
          sha,
        ],
        { cwd }
      );
      expect(JSON.parse(readFileSync(output, 'utf8')).release.source_commit).toBe(sha);
    } finally {
      rmSync(cwd, { recursive: true, force: true });
    }
  });
});

const v2Tag = 'v2.0.0-beta.1';
const v2SourceCommit = '8935bce5ea7ce994185ee97acb8a4fdbd9cdd6cc';
const v2ComposeContent = [
  'name: cpa-manager-plus',
  'services:',
  '  cpa-manager-plus:',
  `    image: seakee/cpa-manager-plus:2.0.0-beta.1-full@sha256:${createHash('sha256').update('full-manifest').digest('hex')}`,
  '',
].join('\n');
const sha256Of = (value) => `sha256:${createHash('sha256').update(value).digest('hex')}`;
const v2Pairing = () => ({
  schema_version: 1,
  version: v2Tag,
  source_commit: v2SourceCommit,
  platform: 'linux/amd64',
  cpa: {
    version: '8.0.55',
    artifact_id: 'sha256:e48c85c75a19adae186cbee590f6e6143fd8085417d8066b545e8533877175f7',
  },
  fullweb: {
    manifest_sha256: sha256Of('fullweb-manifest'),
    index_html_sha256: sha256Of('management-html'),
    asset_count: 42,
  },
  api_contract: {
    sha256: sha256Of('embedded-api-contract'),
    contract_id: 'cpamp-r1-embedded-public-api-transport-v1',
  },
  compose: {
    filename: 'docker-compose.v2-embedded.yml',
    sha256: sha256Of(v2ComposeContent),
    size: Buffer.byteLength(v2ComposeContent, 'utf8'),
  },
  images: Object.fromEntries(
    v2ImageRoles.map((role) => [
      role,
      {
        repository: `seakee/${v2ImageRepositoryName}`,
        tag: v2DockerRoleTag(v2Tag, role),
        image_ref: `seakee/${v2ImageRepositoryName}:${v2DockerRoleTag(v2Tag, role)}@${sha256Of(`${role}-manifest`)}`,
        manifest_digest: sha256Of(`${role}-manifest`),
        image_config_digest: sha256Of(`${role}-config`),
        binary_sha256: sha256Of(`${role}-binary`),
        release_provenance_sha256: sha256Of(`${role}-provenance`),
      },
    ])
  ),
});
const v2Notes = (overrides = {}) =>
  '<!-- cpamp-update\n' +
  JSON.stringify({
    summary: {
      zh: '首个 v2 Beta：单镜像双变体（slim/full）、Full Web/API 配对与 linux/amd64 Native 包。',
      en: 'First v2 Beta: single-image dual-variant (slim/full), Full Web/API pairing, and the linux/amd64 Native package.',
    },
    update: {
      breaking: false,
      migration_required: false,
      minimum_direct_upgrade_version: 'v2.0.0-beta.1',
      upgrade_guide_url: 'https://cpamp-docs.seakee.dev/next/operations/update.html',
    },
    compatibility: { minimum_cpa_version: 'v8.0.55' },
    ...overrides,
  }) +
  '\n-->';
const makeV2Info = () =>
  generateReleaseInfo(v2Tag, v2SourceCommit, v2Notes(), {
    dryRun: true,
    allowV2Beta: true,
    pairing: v2Pairing(),
  });

describe('v2 beta release contract', () => {
  it('requires explicit opt-in and rejects non-Beta v2 releases', () => {
    expect(() => assertSupportedVersion(v2Tag)).toThrow(/v2 Embedded/);
    expect(() => assertSupportedVersion(v2Tag)).toThrow(/fail-closed/);
    expect(() => generateReleaseInfo(v2Tag, v2SourceCommit, v2Notes())).toThrow(/fail-closed/);
    // A stable v2 release never opens `releases/latest`, with or without opt-in.
    expect(() => assertSupportedVersion('v2.0.0', { allowV2Beta: true })).toThrow(/v2 Embedded/);
    expect(() =>
      generateReleaseInfo('v2.0.0', v2SourceCommit, v2Notes(), { allowV2Beta: true })
    ).toThrow(/v2 Embedded/);
    expect(assertSupportedVersion(v2Tag, { allowV2Beta: true }).stage).toBe('beta');
    expect(assertSupportedVersion('v1.14.10').core[0]).toBe('1');
  });

  it('requires explicit opt-in for the producer and both gates for release rehearsal', () => {
    for (const options of [{}, { dryRun: true }])
      expect(() => generateReleaseInfo(v2Tag, v2SourceCommit, 'not notes', options)).toThrow(
        /explicit v2 beta opt-in/
      );
    expect(generateReleaseInfo(v2Tag, v2SourceCommit, v2Notes(), {
      allowV2Beta: true,
      dryRun: false,
      pairing: v2Pairing(),
    })).toEqual(makeV2Info());
    expect(() => generateReleaseInfo('v2.0.0-rc.1', v2SourceCommit, 'not notes', {
      allowV2Beta: true,
    })).toThrow(/only v2.x.y-beta.N is enabled/);
    expect(() =>
      generateReleaseInfo(v2Tag, v2SourceCommit, v2Notes(), {
        allowV2Beta: 'true',
        dryRun: true,
      })
    ).toThrow(/allowV2Beta must be a boolean/);
    expect(() =>
      generateReleaseInfo('v2.0.0', v2SourceCommit, v2Notes(), {
        allowV2Beta: true,
        dryRun: true,
      })
    ).toThrow(/only v2.x.y-beta.N is enabled/);

    // Asset builder: the gate rejects before the first readFileSync. A directory
    // containing invalid JSON proves the file was never parsed.
    const directory = mkdtempSync(path.join(tmpdir(), 'cpamp-v2-gate-'));
    try {
      writeFileSync(path.join(directory, 'release-info.json'), '{not json');
      for (const options of [{}, { allowV2Beta: true }, { dryRun: true }])
        expect(() => buildExpectedReleaseAssets(directory, v2Tag, options)).toThrow(
          /DRY_RUN opt-in/
        );
      expect(() =>
        buildExpectedReleaseAssets(directory, v2Tag, {
          allowV2Beta: true,
          dryRun: 'true',
        })
      ).toThrow(/dryRun must be a boolean/);
      expect(() =>
        buildExpectedReleaseAssets('/nonexistent-v2-assets', v2Tag, { allowV2Beta: true })
      ).toThrow(/DRY_RUN opt-in/);
    } finally {
      rmSync(directory, { recursive: true, force: true });
    }

    // Published/draft/metadata verifiers: same boundary, same fail-closed path
    // even with a well-formed release object.
    const release = {
      tag_name: v2Tag,
      draft: false,
      prerelease: true,
      immutable: true,
      published_at: '2026-11-06T00:00:00Z',
      body: '# v2 Beta\n',
      assets: [],
    };
    const base = { release, tag: v2Tag, prerelease: true, body: '# v2 Beta\n' };
    expect(() => verifyPublishedRelease({ ...base, assets: [] })).toThrow(/DRY_RUN opt-in/);
    expect(() => verifyDraftRelease({ ...base, assets: [] })).toThrow(/DRY_RUN opt-in/);
    expect(() => verifyPublishedReleaseMetadata(base)).toThrow(/DRY_RUN opt-in/);
    expect(() => verifyPublishedRelease({ ...base, assets: [], allowV2Beta: true })).toThrow(
      /DRY_RUN opt-in/
    );
    expect(() => verifyPublishedRelease({ ...base, assets: [], dryRun: true })).toThrow(
      /DRY_RUN opt-in/
    );
    // With both flags the exact six-asset invariant still rejects empty input.
    expect(() =>
      verifyPublishedRelease({ ...base, assets: [], allowV2Beta: true, dryRun: true })
    ).toThrow(/asset set mismatch/);
  });

  it('builds a schema 2 beta record with 4 single-image slim/full references and unprefixed tags', () => {
    expect(v2DockerVersionTag(v2Tag)).toBe('2.0.0-beta.1');
    expect(v2DockerRoleTag(v2Tag, 'slim')).toBe('2.0.0-beta.1');
    expect(v2DockerRoleTag(v2Tag, 'full')).toBe('2.0.0-beta.1-full');
    expect(v2ReleaseInfoPath(v2Tag)).toBe('releases/download/v2.0.0-beta.1/release-info.json');
    expect(() => v2DockerVersionTag('v1.14.10')).toThrow(/Invalid v2 release tag/);
    expect(() => v2DockerRoleTag(v2Tag, 'manager')).toThrow(/Invalid v2 image role/);

    const info = makeV2Info();
    expect(info).toMatchObject({
      schema_version: 2,
      release: { version: v2Tag, stage: 'beta', source_commit: v2SourceCommit },
      update: {
        breaking: false,
        migration_required: false,
        minimum_direct_upgrade_version: 'v2.0.0-beta.1',
        upgrade_guide_url: 'https://cpamp-docs.seakee.dev/next/operations/update.html',
      },
      compatibility: { minimum_cpa_version: 'v8.0.55' },
      supported_modes: ['docker', 'native', 'panel-only'],
      supported_platforms: { docker: ['linux/amd64'], native: ['linux/amd64'] },
    });
    expect(info.distribution.docker.version_tag).toBe('2.0.0-beta.1');
    expect(info.distribution.native.assets).toEqual([v2NativeArchiveName(v2Tag)]);
    expect(info.distribution.compose).toEqual({
      filename: 'docker-compose.v2-embedded.yml',
      sha256: sha256Of(v2ComposeContent),
    });
    expect(info.pairing).toMatchObject({
      filename: 'embedded-release-pairing.json',
      source_commit: v2SourceCommit,
      fullweb_manifest_sha256: sha256Of('fullweb-manifest'),
      api_contract_sha256: sha256Of('embedded-api-contract'),
      compose_sha256: sha256Of(v2ComposeContent),
    });
    expect(info.distribution.docker.images).toEqual([
      {
        role: 'slim',
        name: 'seakee/cpa-manager-plus',
        tag: '2.0.0-beta.1',
        digest: sha256Of('slim-manifest'),
        platforms: ['linux/amd64'],
      },
      {
        role: 'full',
        name: 'seakee/cpa-manager-plus',
        tag: '2.0.0-beta.1-full',
        digest: sha256Of('full-manifest'),
        platforms: ['linux/amd64'],
      },
      {
        role: 'slim',
        name: 'ghcr.io/seakee/cpa-manager-plus',
        tag: '2.0.0-beta.1',
        digest: sha256Of('slim-manifest'),
        platforms: ['linux/amd64'],
      },
      {
        role: 'full',
        name: 'ghcr.io/seakee/cpa-manager-plus',
        tag: '2.0.0-beta.1-full',
        digest: sha256Of('full-manifest'),
        platforms: ['linux/amd64'],
      },
    ]);
    expect(validateInfo(info, v2Tag, { allowV2Beta: true })).toBe(info);

    // null and SemVer strings are valid for minimum_direct_upgrade_version; undefined is rejected.
    const withNullMin = { ...info, update: { ...info.update, migration_required: true, minimum_direct_upgrade_version: null } };
    expect(validateInfo(withNullMin, v2Tag, { allowV2Beta: true })).toBe(withNullMin);
    const omittedMin = { ...info, update: { ...info.update } };
    delete omittedMin.update.minimum_direct_upgrade_version;
    expect(() => validateInfo(omittedMin, v2Tag, { allowV2Beta: true })).toThrow(
      /minimum_direct_upgrade_version/
    );
    expect(() =>
      validateInfo(
        { ...info, update: { ...info.update, minimum_direct_upgrade_version: 'not-semver' } },
        v2Tag,
        { allowV2Beta: true }
      )
    ).toThrow();

    for (const validGuide of [
      `https://github.com/seakee/CPA-Manager-Plus/releases/tag/${v2Tag}`,
      'https://cpamp-docs.seakee.dev/next/operations/update.html',
    ]) {
      expect(assertV2UpgradeGuideUrl(validGuide)).toBe(validGuide);
    }
    for (const badGuide of [
      'http://cpamp-docs.seakee.dev/next/operations/update.html',
      'https://cpamp-docs.seakee.dev/',
      'https://user:pass@cpamp-docs.seakee.dev/next/operations/update.html',
      'https://cpamp-docs.seakee.dev/next/operations/update.html?x=1',
      'https://cpamp-docs.seakee.dev/next/../operations/update.html',
      'https://github.com/other/repo/releases/tag/v2.0.0-beta.1',
      'https://docs.example.com/update.html',
    ]) {
      expect(() => assertV2UpgradeGuideUrl(badGuide)).toThrow(/Invalid v2 upgrade guide/);
    }

    expect(() =>
      validateInfo(
        {
          ...info,
          distribution: {
            ...info.distribution,
            docker: { ...info.distribution.docker, version_tag: v2Tag },
          },
        },
        v2Tag,
        { allowV2Beta: true }
      )
    ).toThrow(/docker distribution/);
    expect(() =>
      validateInfo({ ...info, schema_version: 1 }, v2Tag, { allowV2Beta: true })
    ).toThrow(/schema_version 2/);
    expect(() =>
      validateInfo({ ...info, compatibility: { minimum_cpa_version: 'v8.0.54' } }, v2Tag, {
        allowV2Beta: true,
      })
    ).toThrow(/CPA/);
    expect(() =>
      validateInfo(
        {
          ...info,
          distribution: {
            ...info.distribution,
            docker: { ...info.distribution.docker, images: [] },
          },
        },
        v2Tag,
        { allowV2Beta: true }
      )
    ).toThrow(/image set/);
  });

  it('rejects 1.x-only and non-linux/amd64 assets in the v2 asset set', () => {
    const expected = expectedV2AssetNames(v2Tag);
    expect(assertV2AssetSet(expected, v2Tag)).toEqual(expected);
    expect(() => assertV2AssetSet([...expected, 'stable-version.txt'], v2Tag)).toThrow(
      /stable-version.txt/
    );
    const replaceNative = (name) =>
      expected.map((asset) => (asset === v2NativeArchiveName(v2Tag) ? name : asset));
    expect(() =>
      assertV2AssetSet(replaceNative(`cpa-manager-plus_${v2Tag}_darwin_arm64.tar.gz`), v2Tag)
    ).toThrow(/1.x native asset/);
    expect(() =>
      assertV2AssetSet(
        replaceNative(`cpa-manager-plus-native-v2_${v2Tag}_linux_arm64.tar.gz`),
        v2Tag
      )
    ).toThrow(/Unsupported v2 native platform/);
    expect(() =>
      assertV2AssetSet(
        replaceNative(`cpa-manager-plus-native-v2_${v2Tag}_windows_amd64.zip`),
        v2Tag
      )
    ).toThrow(/Unsupported v2 native platform/);
    expect(() =>
      assertV2AssetSet(
        expected.filter((asset) => asset !== 'management.html'),
        v2Tag
      )
    ).toThrow(/asset set mismatch/);
  });

  it('binds the compose bytes and pairing record to the release info', () => {
    const info = makeV2Info();
    expect(() =>
      assertV2AssetBindings(info, {
        composeSha256: sha256Of(v2ComposeContent),
        pairing: v2Pairing(),
      })
    ).not.toThrow();
    expect(() =>
      assertV2AssetBindings(info, {
        composeSha256: sha256Of(`${v2ComposeContent}\n`),
        pairing: v2Pairing(),
      })
    ).toThrow(/compose asset digest/);
    const tamperedPairing = v2Pairing();
    tamperedPairing.fullweb.manifest_sha256 = sha256Of('tampered-fullweb');
    expect(() =>
      assertV2AssetBindings(info, {
        composeSha256: sha256Of(v2ComposeContent),
        pairing: tamperedPairing,
      })
    ).toThrow(/Full Web digest/);
    const tamperedImage = v2Pairing();
    tamperedImage.images.slim.manifest_digest = sha256Of('tampered-slim');
    tamperedImage.images.slim.image_ref = `seakee/${v2ImageRepositoryName}:2.0.0-beta.1@${tamperedImage.images.slim.manifest_digest}`;
    expect(() =>
      assertV2AssetBindings(info, {
        composeSha256: sha256Of(v2ComposeContent),
        pairing: tamperedImage,
      })
    ).toThrow(/slim digest/);
    expect(() =>
      assertV2AssetBindings(info, {
        composeSha256: sha256Of(v2ComposeContent),
        composeSize: 99999,
        pairing: v2Pairing(),
      })
    ).toThrow(/compose asset size/);
  });

  it('requires one digest per role across both registries and distinct slim/full roles', () => {
    const info = makeV2Info();
    const diverged = structuredClone(info);
    diverged.distribution.docker.images.find(
      (image) => image.role === 'slim' && image.name === 'ghcr.io/seakee/cpa-manager-plus'
    ).digest = sha256Of('other-slim');
    expect(() => validateInfo(diverged, v2Tag, { allowV2Beta: true })).toThrow(
      /registry digest mismatch: slim/
    );
    const collided = structuredClone(info);
    for (const image of collided.distribution.docker.images)
      if (image.role === 'full') image.digest = sha256Of('slim-manifest');
    expect(() => validateInfo(collided, v2Tag, { allowV2Beta: true })).toThrow(/distinct/);
    const pairing = v2Pairing();
    pairing.images.full.manifest_digest = pairing.images.slim.manifest_digest;
    pairing.images.full.image_ref = `seakee/${v2ImageRepositoryName}:2.0.0-beta.1-full@${pairing.images.full.manifest_digest}`;
    expect(() =>
      generateReleaseInfo(v2Tag, v2SourceCommit, v2Notes(), {
        dryRun: true,
        allowV2Beta: true,
        pairing,
      })
    ).toThrow(/distinct/);

    // Unprefixed pairing version and role tags are also accepted.
    const unprefixed = v2Pairing();
    unprefixed.version = '2.0.0-beta.1';
    delete unprefixed.images.slim.binary_sha256;
    expect(validateV2Pairing(unprefixed, { tag: v2Tag, sourceCommit: v2SourceCommit })).toBe(
      unprefixed
    );
  });

  it('rejects version tags without role normalization and mismatched pinned references', () => {
    const options = { tag: v2Tag, sourceCommit: v2SourceCommit };
    for (const role of v2ImageRoles) {
      const pairing = v2Pairing();
      const image = pairing.images[role];
      image.tag = pairing.version;
      image.image_ref = `${image.repository}:${image.tag}@${image.manifest_digest}`;
      expect(() => validateV2Pairing(pairing, options)).toThrow(/tag mismatch/);
      image.tag = v2DockerRoleTag(v2Tag, role);
      expect(() => validateV2Pairing(pairing, options)).toThrow(/image_ref mismatch/);
    }
  });

  it('requires the v2 notes metadata to declare both upgrade boundaries', () => {
    const withoutField = (mutate) => {
      const metadata = JSON.parse(v2Notes().match(/<!--\s*cpamp-update\s*\n([\s\S]*?)-->/)[1]);
      mutate(metadata);
      return '<!-- cpamp-update\n' + JSON.stringify(metadata) + '\n-->';
    };
    expect(() =>
      generateReleaseInfo(
        v2Tag,
        v2SourceCommit,
        withoutField((metadata) => delete metadata.update.minimum_direct_upgrade_version),
        { dryRun: true, allowV2Beta: true, pairing: v2Pairing() }
      )
    ).toThrow(/must declare minimum_direct_upgrade_version/);
    expect(() =>
      generateReleaseInfo(
        v2Tag,
        v2SourceCommit,
        withoutField((metadata) => delete metadata.compatibility.minimum_cpa_version),
        { dryRun: true, allowV2Beta: true, pairing: v2Pairing() }
      )
    ).toThrow(/must declare minimum_cpa_version/);
  });

  it('regenerates the shared consumer fixture from independent golden inputs', () => {
    // Use the candidate pairing and the actual release notes, not a second notes source.
    const fixtureRoot = new URL('./fixtures/', import.meta.url);
    const fixtureBytes = readFileSync(new URL('release-v2.0.0-beta.1-info.json', fixtureRoot));
    const fixtureInfo = JSON.parse(fixtureBytes);
    const pairingPath = fileURLToPath(new URL('v2-beta-pairing.json', fixtureRoot));
    const pairing = JSON.parse(readFileSync(pairingPath, 'utf8'));
    const composeBytes = readFileSync(new URL('v2-beta-compose.yml', fixtureRoot));
    expect(`sha256:${createHash('sha256').update(composeBytes).digest('hex')}`).toBe(
      pairing.compose.sha256
    );
    expect(composeBytes.byteLength).toBe(pairing.compose.size);
    expect(composeBytes.toString()).toContain(`image: '${pairing.images.full.image_ref}'`);

    const notes = readFileSync(new URL(`../docs/release-notes/${v2Tag}-zh.md`, import.meta.url), 'utf8');
    const producer = fileURLToPath(
      new URL('../bin/release/generate-release-info.mjs', import.meta.url)
    );
    const workdir = mkdtempSync(path.join(tmpdir(), 'cpamp-v2-fixture-'));
    try {
      mkdirSync(path.join(workdir, 'docs', 'release-notes'), { recursive: true });
      writeFileSync(
        path.join(workdir, 'docs', 'release-notes', `${fixtureInfo.release.version}-zh.md`),
        notes
      );
      const generate = (output, pairingFile = pairingPath) => {
        execFileSync(
          process.execPath,
          [
            producer,
            fixtureInfo.release.version,
            output,
            fixtureInfo.release.source_commit,
            '--allow-v2-beta',
            '--pairing',
            pairingFile,
          ],
          { cwd: workdir, stdio: 'pipe', env: { ...process.env, DRY_RUN: 'false' } }
        );
        return readFileSync(output);
      };

      // The real producer CLI, driven only by the golden inputs, must reproduce
      // the shared consumer fixture exactly.
      expect(generate(path.join(workdir, 'release-info.json'))).toEqual(fixtureBytes);

      // Single-field tampering must surface as drift, not silently pass.
      const tamperedCompose = structuredClone(pairing);
      tamperedCompose.compose.sha256 = sha256Of('tampered-compose');
      const composePath = path.join(workdir, 'tampered-compose-pairing.json');
      writeFileSync(composePath, JSON.stringify(tamperedCompose));
      const driftedCompose = JSON.parse(generate(path.join(workdir, 'drifted-compose.json'), composePath));
      expect(driftedCompose.distribution.compose.sha256).toBe(sha256Of('tampered-compose'));
      expect(driftedCompose).not.toEqual(fixtureInfo);

      const tamperedImage = structuredClone(pairing);
      tamperedImage.images.full.manifest_digest = sha256Of('tampered-full');
      const imagePath = path.join(workdir, 'tampered-image-pairing.json');
      writeFileSync(imagePath, JSON.stringify(tamperedImage));
      expect(() => generate(path.join(workdir, 'drifted-image.json'), imagePath)).toThrow(/full image_ref mismatch/);

      // The CLI is itself fail-closed on a pairing that does not bind the commit.
      const wrongCommit = structuredClone(pairing);
      wrongCommit.source_commit = 'b'.repeat(40);
      const commitPath = path.join(workdir, 'wrong-commit-pairing.json');
      writeFileSync(commitPath, JSON.stringify(wrongCommit));
      expect(() => generate(path.join(workdir, 'wrong-commit.json'), commitPath)).toThrow(
        /source commit mismatch/
      );
    } finally {
      rmSync(workdir, { recursive: true, force: true });
    }
  });

  it('keeps 1.x update-index.json and aliases completely unpolluted while emitting update-index-v2.json', () => {
    const fixtureRaw = readFileSync(
      new URL('./fixtures/release-v2.0.0-beta.1-info.json', import.meta.url),
      'utf8'
    );
    const fixtureInfo = JSON.parse(fixtureRaw);
    const fixtureSha = sha256Of(fixtureRaw);
    expect(fixtureInfo.schema_version).toBe(2);
    expect(() => validateInfo(fixtureInfo, v2Tag)).toThrow(/fail-closed/);
    expect(validateInfo(fixtureInfo, v2Tag, { allowV2Beta: true })).toBe(fixtureInfo);

    const infos = [
      makeInfo('v1.14.10'),
      makeInfo('v1.14.11'),
      makeInfo('v1.14.12-beta.1'),
      fixtureInfo,
    ];
    const channels = resolveChannels(infos, [], { allowV2Beta: true });
    expect(channels).toEqual({
      stable: { version: 'v1.14.11' },
      rc: { version: 'v1.14.11' },
      beta: { version: 'v1.14.12-beta.1' },
    });
    expect(() => resolveChannels(infos, [])).toThrow(/fail-closed/);
    expect(() => resolveV2Channels(infos, [])).toThrow(/fail-closed/);

    const aliases = resolveAliases(infos, [], { allowV2Beta: true }).aliases;
    expect(aliases.latest).toBe('v1.14.11');
    expect(aliases.preview).toBe('v1.14.12-beta.1');
    expect(Object.values(aliases)).not.toContain(v2Tag);

    const index = {
      schema_version: 1,
      revision: 3,
      generated_at: '2026-11-06T00:00:00Z',
      channels,
    };
    expect(validateIndex(index, { allowV2Beta: true })).toBe(index);
    // 1.x update-index.json must reject any v2 target even with allowV2Beta: true.
    expect(() =>
      validateIndex(
        { ...index, channels: { ...channels, beta: { version: v2Tag, schema_version: 2 } } },
        { allowV2Beta: true }
      )
    ).toThrow(/only accepts 1\.x versions/);
    expect(() =>
      validateIndex(
        { ...index, channels: { ...channels, beta: { version: v2Tag } } },
        { allowV2Beta: true }
      )
    ).toThrow(/only accepts 1\.x versions/);
    expect(() => validateIndex({ ...index, schema_version: 2 }, { allowV2Beta: true })).toThrow(
      /Invalid update index/
    );

    const channelsV2 = resolveV2Channels(infos, [], {
      allowV2Beta: true,
      releaseInfoDigests: { [v2Tag]: fixtureSha },
    });
    expect(channelsV2).toEqual({
      stable: null,
      rc: null,
      beta: {
        version: v2Tag,
        schema_version: 2,
        release_info_path: `releases/download/${v2Tag}/release-info.json`,
        release_info_sha256: fixtureSha,
      },
    });
    const indexV2 = {
      schema_version: 2,
      revision: 1,
      generated_at: '2026-11-25T00:00:00.000Z',
      channels: channelsV2,
    };
    expect(
      validateV2Index(indexV2, { allowV2Beta: true, requireReleaseInfoSha256: true })
    ).toBe(indexV2);
    expect(() => validateV2Index(indexV2)).toThrow(/dry-run opt-in/);
  });

  it('only accepts v2.x.y-beta.N as the beta channel target in update-index-v2.json', () => {
    const validBeta = {
      version: v2Tag,
      schema_version: 2,
      release_info_path: `releases/download/${v2Tag}/release-info.json`,
      release_info_sha256: sha256Of('release-info'),
    };
    const indexV2 = {
      schema_version: 2,
      revision: 3,
      generated_at: '2026-11-25T00:00:00.000Z',
      channels: { stable: null, rc: null, beta: validBeta },
    };
    expect(validateV2Index(indexV2, { allowV2Beta: true })).toBe(indexV2);
    for (const version of ['v2.0.0', 'v2.0.0-rc.1', 'v2.0.0-BETA.1', 'v2.0.0-beta.1+build', 'v1.14.11-beta.1']) {
      expect(() =>
        validateV2Index(
          {
            ...indexV2,
            channels: {
              stable: null,
              rc: null,
              beta: {
                version,
                schema_version: 2,
                release_info_path: `releases/download/${version}/release-info.json`,
              },
            },
          },
          { allowV2Beta: true }
        )
      ).toThrow();
    }
    expect(() =>
      validateV2Index(
        {
          ...indexV2,
          channels: { ...indexV2.channels, stable: validBeta },
        },
        { allowV2Beta: true }
      )
    ).toThrow(/Invalid v2 update index target|only target the beta channel/);
    expect(() =>
      validateV2Index(
        {
          ...indexV2,
          channels: {
            stable: null,
            rc: null,
            beta: { ...validBeta, release_info_path: `/releases/download/${v2Tag}/release-info.json` },
          },
        },
        { allowV2Beta: true }
      )
    ).toThrow(/release_info_path/);
    expect(() => validateV2Index(indexV2, { allowV2Beta: 'true' })).toThrow(/must be a boolean/);
    expect(() => validateIndex({ schema_version: 1, revision: 1, channels: { stable: null, rc: null, beta: null } }, { allowV2Beta: 'true' })).toThrow(/must be a boolean/);
  });

  it('pins every pairing image to the approved single-image repositories', () => {
    const valid = v2Pairing();
    const expectValid = (pairing) =>
      expect(validateV2Pairing(pairing, { tag: v2Tag, sourceCommit: v2SourceCommit })).toBe(
        pairing
      );
    expectValid(valid);
    const ghcr = structuredClone(valid);
    for (const image of Object.values(ghcr.images)) {
      image.repository = 'ghcr.io/seakee/cpa-manager-plus';
      image.image_ref = `${image.repository}:${image.tag}@${image.manifest_digest}`;
    }
    expectValid(ghcr);
    for (const badRepository of [
      'untrusted.example/cpa-manager-plus',
      'seakee/extra/cpa-manager-plus',
      'docker.io/seakee/cpa-manager-plus',
      'cpa-manager-plus',
      'ghcr.io/other/cpa-manager-plus',
      'seakee/cpamp-ingress',
      'seakee/cpamp-runtime',
    ]) {
      const tampered = structuredClone(valid);
      tampered.images.slim.repository = badRepository;
      expect(() =>
        validateV2Pairing(tampered, { tag: v2Tag, sourceCommit: v2SourceCommit })
      ).toThrow(/unexpected slim repository/);
    }
  });

  it('enforces v2 slot asset naming, beta.1 optionality, and beta.2+ requirement', () => {
    expect(v2SlotArchiveName('v2.0.0-beta.2')).toBe('cpamp-slot_v2.0.0-beta.2_linux_amd64.tar.gz');
    expect(v2SlotArchiveName('v2.0.0-beta.2', 'arm64')).toBe(
      'cpamp-slot_v2.0.0-beta.2_linux_arm64.tar.gz'
    );
    expect(() => v2SlotArchiveName('v2.0.0-beta.2', '386')).toThrow(/Unsupported v2 slot/);

    expect(v2SlotRequiresAsset('v1.17.1')).toBe(false);
    expect(v2SlotRequiresAsset('v2.0.0-beta.1')).toBe(false);
    expect(v2SlotRequiresAsset('v2.0.0-beta.2')).toBe(true);
    expect(v2SlotRequiresAsset('v2.0.0-beta.3')).toBe(true);

    const beta1Assets = expectedV2AssetNames('v2.0.0-beta.1');
    expect(beta1Assets).toHaveLength(6);
    expect(assertV2AssetSet(beta1Assets, 'v2.0.0-beta.1')).toEqual(beta1Assets);
    const beta1WithSlot = [...beta1Assets, v2SlotArchiveName('v2.0.0-beta.1', 'amd64')].sort();
    expect(assertV2AssetSet(beta1WithSlot, 'v2.0.0-beta.1')).toEqual(beta1WithSlot);

    const beta2Assets = expectedV2AssetNames('v2.0.0-beta.2');
    expect(beta2Assets).toHaveLength(7);
    expect(beta2Assets).toContain('cpamp-slot_v2.0.0-beta.2_linux_amd64.tar.gz');
    expect(assertV2AssetSet(beta2Assets, 'v2.0.0-beta.2')).toEqual(beta2Assets);
    expect(() =>
      assertV2AssetSet(
        beta2Assets.filter((name) => !name.startsWith('cpamp-slot_')),
        'v2.0.0-beta.2'
      )
    ).toThrow(/asset set mismatch/);
    expect(() =>
      assertV2AssetSet(
        [
          ...beta2Assets.filter((name) => !name.startsWith('cpamp-slot_')),
          'cpamp-slot_v2.0.0-beta.1_linux_amd64.tar.gz',
        ],
        'v2.0.0-beta.2'
      )
    ).toThrow(/slot asset version mismatch/);
    expect(() =>
      assertV2AssetSet(
        [
          ...beta2Assets.filter((name) => !name.startsWith('cpamp-slot_')),
          'cpamp-slot_v2.0.0-beta.2_linux_arm64.tar.gz',
        ],
        'v2.0.0-beta.2'
      )
    ).toThrow(/Unsupported v2 slot platform/);
  });

  it('validates distribution.slot schema and asset bindings across beta.1 and beta.2+', () => {
    const beta2Tag = 'v2.0.0-beta.2';
    const beta2Pairing = () => {
      const base = v2Pairing();
      base.version = beta2Tag;
      for (const role of v2ImageRoles) {
        base.images[role].tag = v2DockerRoleTag(beta2Tag, role);
        base.images[role].image_ref =
          `${base.images[role].repository}:${base.images[role].tag}@${base.images[role].manifest_digest}`;
      }
      return base;
    };
    const validSlot = {
      assets: [
        {
          name: v2SlotArchiveName(beta2Tag, 'amd64'),
          platform: 'linux/amd64',
          sha256: sha256Of('slot-amd64'),
          size: 4096,
          min_base_epoch: 1,
          schema_generation: 1,
        },
      ],
    };

    // beta.2+ fails closed without distribution.slot
    expect(() =>
      generateReleaseInfo(beta2Tag, v2SourceCommit, v2Notes(), {
        dryRun: true,
        allowV2Beta: true,
        pairing: beta2Pairing(),
      })
    ).toThrow(/slot distribution is required/);

    const infoBeta2 = generateReleaseInfo(beta2Tag, v2SourceCommit, v2Notes(), {
      dryRun: true,
      allowV2Beta: true,
      pairing: beta2Pairing(),
      slot: validSlot,
    });
    expect(infoBeta2.distribution.slot).toEqual(validSlot);
    expect(
      assertV2AssetBindings(infoBeta2, {
        composeSha256: sha256Of(v2ComposeContent),
        composeSize: Buffer.byteLength(v2ComposeContent, 'utf8'),
        pairing: beta2Pairing(),
        slotAssets: [
          {
            name: v2SlotArchiveName(beta2Tag, 'amd64'),
            sha256: sha256Of('slot-amd64'),
            size: 4096,
          },
        ],
      })
    ).toBe(infoBeta2);

    // beta.1 allows omitting distribution.slot, and validates it when supplied
    expect(validateV2SlotDistribution(undefined, v2Tag)).toBeUndefined();
    const beta1Slot = {
      assets: [
        {
          name: v2SlotArchiveName(v2Tag, 'arm64'),
          platform: 'linux/arm64',
          sha256: sha256Of('slot-arm64'),
          size: 2048,
          min_base_epoch: 1,
          schema_generation: 1,
        },
      ],
    };
    expect(validateV2SlotDistribution(beta1Slot, v2Tag)).toEqual(beta1Slot);

    // Invalid slot entries fail closed
    const invalidCases = [
      [{ ...validSlot.assets[0], name: 'cpamp-slot_v2.0.0-beta.1_linux_amd64.tar.gz' }, /version mismatch/],
      [{ ...validSlot.assets[0], platform: 'linux/arm64' }, /platform mismatch/],
      [{ ...validSlot.assets[0], sha256: 'deadbeef' }, /slot asset digest/],
      [{ ...validSlot.assets[0], size: 0 }, /size/],
      [{ ...validSlot.assets[0], min_base_epoch: 0 }, /min_base_epoch/],
      [{ ...validSlot.assets[0], schema_generation: 0 }, /schema_generation/],
    ];
    for (const [badAsset, errorPattern] of invalidCases) {
      expect(() => validateV2SlotDistribution({ assets: [badAsset] }, beta2Tag)).toThrow(
        errorPattern
      );
    }
    expect(() =>
      assertV2AssetBindings(infoBeta2, {
        composeSha256: sha256Of(v2ComposeContent),
        composeSize: Buffer.byteLength(v2ComposeContent, 'utf8'),
        pairing: beta2Pairing(),
        slotAssets: [
          {
            name: v2SlotArchiveName(beta2Tag, 'amd64'),
            sha256: sha256Of('tampered-slot'),
            size: 4096,
          },
        ],
      })
    ).toThrow(/digest does not match/);
  });
});
