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
  compareVersions,
  expectedV2AssetNames,
  parseVersion,
  resolveChannels,
  validateIndex,
  validateInfo,
  v2ImageRepositories,
  v2NativeArchiveName,
  validateV2Pairing,
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
  'services:',
  '  cpamp-ingress:',
  '    image: seakee/cpamp-ingress:v2.0.0-beta.1',
  '  cpamp-manager:',
  '    image: seakee/cpa-manager-plus:v2.0.0-beta.1',
  '  cpamp-runtime:',
  '    image: seakee/cpamp-runtime:v2.0.0-beta.1',
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
    size: v2ComposeContent.length,
  },
  images: Object.fromEntries(
    Object.entries(v2ImageRepositories).map(([role, name]) => [
      role,
      {
        repository: `seakee/${name}`,
        tag: v2Tag,
        image_ref: `seakee/${name}:${v2Tag}`,
        manifest_digest: sha256Of(`${role}-manifest`),
        image_config_digest: sha256Of(`${role}-config`),
        binary_sha256: sha256Of(`${role}-binary`),
        release_provenance_sha256: sha256Of(`${role}-provenance`),
      },
    ])
  ),
});
const v2Notes = () =>
  '<!-- cpamp-update\n' +
  JSON.stringify({
    summary: {
      zh: '首个 v2 Beta：不可变三镜像、Full Web/API 配对与 linux/amd64 Native 包。',
      en: 'First v2 Beta: immutable three-image set, Full Web/API pairing, and the linux/amd64 Native package.',
    },
    update: {
      breaking: true,
      migration_required: true,
      minimum_direct_upgrade_version: 'v1.14.10',
      upgrade_guide_url: `https://github.com/seakee/CPA-Manager-Plus/releases/tag/${v2Tag}`,
    },
    compatibility: { minimum_cpa_version: 'v8.0.55' },
  }) +
  '\n-->';
const makeV2Info = () =>
  generateReleaseInfo(v2Tag, v2SourceCommit, v2Notes(), {
    dryRun: true,
    allowV2Beta: true,
    pairing: v2Pairing(),
  });

describe('v2 beta release contract', () => {
  it('keeps the v2 contract fail-closed outside an explicit beta dry run', () => {
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

  it('requires dryRun and the beta opt-in at every exported v2 entry, before IO', () => {
    // Producer: neither flag alone may produce a v2 record, and notes are not
    // parsed until both are present.
    for (const options of [{}, { allowV2Beta: true }, { dryRun: true }])
      expect(() => generateReleaseInfo(v2Tag, v2SourceCommit, 'not notes', options)).toThrow(
        /DRY_RUN opt-in/
      );
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

  it('builds a schema 2 beta record bound to the pairing manifest', () => {
    const info = makeV2Info();
    expect(info).toMatchObject({
      schema_version: 2,
      release: { version: v2Tag, stage: 'beta', source_commit: v2SourceCommit },
      update: { migration_required: true, minimum_direct_upgrade_version: 'v1.14.10' },
      compatibility: { minimum_cpa_version: 'v8.0.55' },
      supported_modes: ['docker', 'native', 'panel-only'],
      supported_platforms: { docker: ['linux/amd64'], native: ['linux/amd64'] },
    });
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
    expect(info.distribution.docker.images).toHaveLength(6);
    for (const [role, name] of Object.entries(v2ImageRepositories)) {
      expect(
        info.distribution.docker.images
          .filter((image) => image.role === role)
          .map((image) => image.name)
          .sort()
      ).toEqual([`ghcr.io/seakee/${name}`, `seakee/${name}`]);
      expect(
        info.distribution.docker.images
          .filter((image) => image.role === role)
          .every((image) => image.digest === sha256Of(`${role}-manifest`))
      ).toBe(true);
    }
    expect(validateInfo(info, v2Tag, { allowV2Beta: true })).toBe(info);
    expect(() =>
      validateInfo(
        { ...info, update: { ...info.update, minimum_direct_upgrade_version: null } },
        v2Tag,
        { allowV2Beta: true }
      )
    ).toThrow(/minimum_direct_upgrade_version/);
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
    tamperedImage.images.ingress.manifest_digest = sha256Of('tampered-ingress');
    expect(() =>
      assertV2AssetBindings(info, {
        composeSha256: sha256Of(v2ComposeContent),
        pairing: tamperedImage,
      })
    ).toThrow(/ingress digest/);
  });

  it('requires one digest per role across both registries and distinct roles', () => {
    const info = makeV2Info();
    const diverged = structuredClone(info);
    diverged.distribution.docker.images.find(
      (image) => image.name === 'ghcr.io/seakee/cpamp-ingress'
    ).digest = sha256Of('other-ingress');
    expect(() => validateInfo(diverged, v2Tag, { allowV2Beta: true })).toThrow(
      /registry digest mismatch: ingress/
    );
    const collided = structuredClone(info);
    for (const image of collided.distribution.docker.images)
      if (image.role === 'runtime') image.digest = sha256Of('manager-manifest');
    expect(() => validateInfo(collided, v2Tag, { allowV2Beta: true })).toThrow(/distinct/);
    const pairing = v2Pairing();
    pairing.images.runtime.manifest_digest = pairing.images.manager.manifest_digest;
    expect(() =>
      generateReleaseInfo(v2Tag, v2SourceCommit, v2Notes(), {
        dryRun: true,
        allowV2Beta: true,
        pairing,
      })
    ).toThrow(/distinct/);
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
    // Independent inputs: the pairing and compose bytes were produced by the
    // real Embedded renderer from the candidate evidence (PR-1), and the notes
    // file is a fixed reviewed input. None of them is derived from the fixture.
    const fixtureRoot = new URL('./fixtures/', import.meta.url);
    const fixtureInfo = JSON.parse(
      readFileSync(new URL('release-v2.0.0-beta.1-info.json', fixtureRoot), 'utf8')
    );
    const pairingPath = fileURLToPath(new URL('v2-beta-pairing.json', fixtureRoot));
    const pairing = JSON.parse(readFileSync(pairingPath, 'utf8'));
    const composeBytes = readFileSync(new URL('v2-beta-compose.yml', fixtureRoot));
    expect(`sha256:${createHash('sha256').update(composeBytes).digest('hex')}`).toBe(
      pairing.compose.sha256
    );

    const notes = readFileSync(new URL('v2-beta-notes-zh.md', fixtureRoot), 'utf8');
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
            '--dry-run',
            '--allow-v2-beta',
            '--pairing',
            pairingFile,
          ],
          { cwd: workdir, stdio: 'pipe' }
        );
        return JSON.parse(readFileSync(output, 'utf8'));
      };

      // The real producer CLI, driven only by the golden inputs, must reproduce
      // the shared consumer fixture exactly.
      expect(generate(path.join(workdir, 'release-info.json'))).toEqual(fixtureInfo);

      // Single-field tampering must surface as drift, not silently pass.
      const tamperedCompose = structuredClone(pairing);
      tamperedCompose.compose.sha256 = sha256Of('tampered-compose');
      const composePath = path.join(workdir, 'tampered-compose-pairing.json');
      writeFileSync(composePath, JSON.stringify(tamperedCompose));
      const driftedCompose = generate(path.join(workdir, 'drifted-compose.json'), composePath);
      expect(driftedCompose.distribution.compose.sha256).toBe(sha256Of('tampered-compose'));
      expect(driftedCompose).not.toEqual(fixtureInfo);

      const tamperedImage = structuredClone(pairing);
      tamperedImage.images.runtime.manifest_digest = sha256Of('tampered-runtime');
      const imagePath = path.join(workdir, 'tampered-image-pairing.json');
      writeFileSync(imagePath, JSON.stringify(tamperedImage));
      const driftedImage = generate(path.join(workdir, 'drifted-image.json'), imagePath);
      expect(
        driftedImage.distribution.docker.images
          .filter((image) => image.role === 'runtime')
          .map((image) => image.digest)
      ).toEqual([sha256Of('tampered-runtime'), sha256Of('tampered-runtime')]);
      expect(driftedImage).not.toEqual(fixtureInfo);

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

  it('shares the consumer fixture and keeps v2 Beta out of stable, rc and aliases', () => {
    const fixtureInfo = JSON.parse(
      readFileSync(new URL('./fixtures/release-v2.0.0-beta.1-info.json', import.meta.url), 'utf8')
    );
    expect(fixtureInfo.schema_version).toBe(2);
    expect(() => validateInfo(fixtureInfo, v2Tag)).toThrow(/fail-closed/);
    expect(validateInfo(fixtureInfo, v2Tag, { allowV2Beta: true })).toBe(fixtureInfo);

    const infos = [makeInfo('v1.14.10'), makeInfo('v1.14.11'), fixtureInfo];
    const channels = resolveChannels(infos, [], { allowV2Beta: true });
    expect(channels).toEqual({
      stable: { version: 'v1.14.11' },
      rc: { version: 'v1.14.11' },
      beta: { version: v2Tag, schema_version: 2 },
    });
    expect(() => resolveChannels(infos, [])).toThrow(/fail-closed/);

    const aliases = resolveAliases(infos, [], { allowV2Beta: true }).aliases;
    expect(aliases.latest).toBe('v1.14.11');
    expect(aliases.preview).toBeUndefined();
    expect(Object.values(aliases)).not.toContain(v2Tag);

    const index = {
      schema_version: 1,
      revision: 3,
      generated_at: '2026-11-06T00:00:00Z',
      channels,
    };
    expect(validateIndex(index, { allowV2Beta: true })).toBe(index);
    expect(() => validateIndex(index)).toThrow(/dry-run opt-in/);
    expect(() =>
      validateIndex(
        { ...index, channels: { ...channels, stable: { version: 'v2.0.0', schema_version: 2 } } },
        { allowV2Beta: true }
      )
    ).toThrow(/only target the beta channel/);
    expect(() =>
      validateIndex(
        { ...index, channels: { ...channels, beta: { version: v2Tag } } },
        { allowV2Beta: true }
      )
    ).toThrow(/schema_version 2/);
    expect(() => validateIndex({ ...index, schema_version: 2 }, { allowV2Beta: true })).toThrow(
      /Invalid update index/
    );
  });

  it('only accepts v2.x.y-beta.N as the beta channel target', () => {
    const channels = {
      stable: { version: 'v1.14.11' },
      rc: { version: 'v1.14.11' },
      beta: { version: v2Tag, schema_version: 2 },
    };
    const index = { schema_version: 1, revision: 3, channels };
    expect(validateIndex(index, { allowV2Beta: true })).toBe(index);
    for (const version of ['v2.0.0', 'v2.0.0-rc.1', 'v2.0.0-BETA.1', 'v2.0.0-beta.1+build']) {
      expect(() =>
        validateIndex(
          { ...index, channels: { ...channels, beta: { version, schema_version: 2 } } },
          { allowV2Beta: true }
        )
      ).toThrow(
        /v2 beta channel target must be a beta prerelease|Invalid update index target|only v2.x.y-beta.N is enabled/
      );
    }
    // A truthy string is not an opt-in.
    expect(() => validateIndex(index, { allowV2Beta: 'true' })).toThrow(/must be a boolean/);
  });

  it('pins every pairing image to the approved registries', () => {
    const valid = v2Pairing();
    const expectValid = (pairing) =>
      expect(validateV2Pairing(pairing, { tag: v2Tag, sourceCommit: v2SourceCommit })).toBe(
        pairing
      );
    expectValid(valid);
    const ghcr = structuredClone(valid);
    ghcr.images.manager.repository = 'ghcr.io/seakee/cpa-manager-plus';
    expectValid(ghcr);
    for (const repository of [
      'untrusted.example/cpa-manager-plus',
      'seakee/extra/cpa-manager-plus',
      'docker.io/seakee/cpa-manager-plus',
      'cpa-manager-plus',
      'ghcr.io/other/cpa-manager-plus',
      'seakee/cpamp-ingress',
    ]) {
      const tampered = structuredClone(valid);
      tampered.images.manager.repository = repository;
      expect(() =>
        validateV2Pairing(tampered, { tag: v2Tag, sourceCommit: v2SourceCommit })
      ).toThrow(/unexpected manager repository/);
    }
  });
});
