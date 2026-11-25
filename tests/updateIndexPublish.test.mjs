import { createHash } from 'node:crypto';
import { spawnSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { afterEach, describe, expect, it } from 'vitest';
import { generateReleaseInfo } from '../bin/release/generate-release-info.mjs';
import { expectedReleaseAssetNames } from '../bin/release/verify-published-release.mjs';
import { publishUpdateIndex } from '../bin/release/publish-update-index.mjs';
import { repository, resolveChannels, v2DockerRoleTag } from '../bin/release/update-contract.mjs';

function scenario({
  tags = ['v1.14.7'],
  failImage = false,
  conflict = false,
  existing = false,
  withdrawn = [],
  withdraw = '',
  restore = '',
  wrongRevision = false,
  registryMismatch = false,
  latestMismatch = false,
  existingBeta,
} = {}) {
  const sha = 'a'.repeat(40),
    calls = [];
  const candidates = tags.map((tag, index) => {
    const info = generateReleaseInfo(
      tag,
      sha,
      '<!-- cpamp-update\n' +
        JSON.stringify({
          summary: { zh: '更新', en: 'Update' },
          update: {
            breaking: true,
            migration_required: true,
            minimum_direct_upgrade_version: null,
            upgrade_guide_url: repository + '/releases/tag/' + tag,
          },
          compatibility: { minimum_cpa_version: null },
        }) +
        '\n-->'
    );
    const data = JSON.stringify(info);
    const digest = 'sha256:' + createHash('sha256').update(data).digest('hex');
    const imageDigest = 'sha256:' + createHash('sha256').update(tag).digest('hex');
    const release = {
      id: 123 + index,
      tag_name: tag,
      draft: false,
      immutable: true,
      prerelease: info.release.stage !== 'stable',
      published_at: '2026-09-08T00:00:00Z',
      assets: expectedReleaseAssetNames(tag).map((name) => ({
        name,
        state: 'uploaded',
        size: name === 'release-info.json' ? Buffer.byteLength(data) : 10,
        digest: name === 'release-info.json' ? digest : 'sha256:' + 'b'.repeat(64),
      })),
    };
    return { info, data, digest, imageDigest, release };
  });
  const oldIndex = {
    schema_version: 1,
    revision: 7,
    generated_at: '2026-09-08T00:00:00Z',
    channels: resolveChannels(
      candidates.map((c) => c.info),
      withdrawn
    ),
  };
  if (existingBeta) oldIndex.channels.beta = existingBeta;
  const files = {
    'update-index.json': oldIndex,
    'release-catalog.json': Object.fromEntries(
      candidates.map((c) => [c.info.release.version, { info: c.info, digest: c.digest }])
    ),
    'withdrawn.json': withdrawn,
  };
  let latest;
  const response = (body, status = 200) => new Response(JSON.stringify(body), { status });
  const fetchImpl = async (url, options = {}) => {
    const method = options.method || 'GET';
    calls.push({ url, method, body: options.body && JSON.parse(options.body) });
    if (url.includes('/git/ref/heads/'))
      return existing ? response({ object: { sha: 'old-ref' } }) : response({}, 404);
    if (url.includes('/contents/')) {
      const name = new URL(url).pathname.split('/').at(-1);
      if (!(name in files) || files[name] == null) return response({}, 404);
      return response({ content: Buffer.from(JSON.stringify(files[name])).toString('base64') });
    }
    if (url.includes('/releases?')) return response(candidates.map((c) => c.release));
    if (url.includes('/releases/download/')) {
      const tag = decodeURIComponent(new URL(url).pathname.split('/').at(-2));
      return new Response(candidates.find((c) => c.info.release.version === tag).data);
    }
    if (url.includes('/commits/v')) return response({ sha });
    if (url.endsWith('/releases/latest')) return response(latestMismatch ? {} : latest);
    if (/\/releases\/\d+$/.test(url)) {
      latest = candidates.find((c) => String(c.release.id) === url.split('/').at(-1)).release;
      return response(latest);
    }
    if (url.endsWith('/git/trees')) return response({ sha: 'tree' });
    if (url.endsWith('/git/commits')) return response({ sha: 'commit' });
    if (url.includes('/git/refs')) return response({}, conflict ? 422 : 201);
    throw new Error('Unexpected request: ' + url);
  };
  const aliases = new Map();
  const exec = (_cmd, args) => {
    calls.push({ docker: args });
    if (args[2] === 'create') {
      const digest = args[5].split('@')[1];
      aliases.set(
        args[4],
        candidates.find((c) => c.imageDigest === digest)
      );
      return '';
    }
    const reference = args[3];
    if (failImage === true || (failImage && reference.endsWith(':' + failImage)))
      throw new Error('Registry unavailable');
    const candidate =
      aliases.get(reference) ||
      candidates.find((c) => reference.endsWith(':' + c.info.release.version));
    if (!candidate) throw new Error('Unexpected image: ' + reference);
    const labels = {
      'org.opencontainers.image.revision': wrongRevision ? 'c'.repeat(40) : sha,
      'org.opencontainers.image.version': candidate.info.release.version.slice(1),
      'org.opencontainers.image.source': repository,
    };
    return JSON.stringify({
      manifest: {
        digest:
          registryMismatch && reference.startsWith('seakee/')
            ? 'sha256:' + 'f'.repeat(64)
            : candidate.imageDigest,
        manifests: ['amd64', 'arm64'].map((architecture) => ({
          platform: { os: 'linux', architecture },
        })),
      },
      image: Object.fromEntries(
        ['amd64', 'arm64'].map((arch) => ['linux/' + arch, { config: { Labels: labels } }])
      ),
    });
  };
  return {
    calls,
    run: () =>
      publishUpdateIndex({
        env: {
          GITHUB_REPOSITORY: 'seakee/CPA-Manager-Plus',
          GITHUB_TOKEN: 'fixture',
          WITHDRAW_RELEASE: withdraw,
          RESTORE_RELEASE: restore,
        },
        fetchImpl,
        exec,
      }),
  };
}

describe('update index publication', () => {
  it.each([
    { version: 'v2.0.0-beta.1', schema_version: 2 },
    { version: 'v1.14.7', schema_version: 2 },
    { version: 'v2.0.0-beta.1' },
  ])('rejects a 1.x update-index.json polluted with v2 or schema 2 targets before any write: %j', async (existingBeta) => {
    const s = scenario({ existing: true, existingBeta });
    await expect(s.run()).rejects.toThrow(/only accepts 1\.x versions|Invalid update index target/);
    expect(s.calls.every((call) => call.method === 'GET')).toBe(true);
    expect(s.calls.some((call) => call.docker)).toBe(false);
  });

  it('rejects a v2 Manager-only schema1 record before release candidate construction', () => {
    // The shared validator now rejects in generateReleaseInfo, before
    // the channel producer can make any Docker or GitHub write.
    expect(() => scenario({ tags: ['v2.0.0'] })).toThrow(/v2 Embedded/);
  });

  it('publishes the index after exact image validation, aliases and GitHub Latest', async () => {
    const s = scenario();
    const index = await s.run();
    expect(index.channels.stable.version).toBe('v1.14.7');
    expect(s.calls.at(-1).body).toEqual({ ref: 'refs/heads/update-channel', sha: 'commit' });
    const tree = s.calls.find((c) => c.url?.endsWith('/git/trees'));
    expect(tree.body.tree.find((f) => f.path === 'stable-version.txt').content).toBe('v1.14.7\n');
    const latest = s.calls.findIndex((c) => c.url?.endsWith('/releases/123'));
    expect(s.calls.slice(latest + 1).some((c) => c.docker)).toBe(false);
    expect(s.calls.slice(0, latest).some((c) => c.docker?.includes('create'))).toBe(true);
    expect(
      s.calls
        .filter((c) => c.docker?.includes('create'))
        .every((c) => c.docker.at(-1).includes('@sha256:'))
    ).toBe(true);
  });
  it('validates a distinct RC target before any alias or index mutation', async () => {
    const s = scenario({
      tags: ['v1.13.0', 'v1.14.8-rc.2', 'v1.14.9-beta.1'],
      failImage: 'v1.14.8-rc.2',
    });
    await expect(s.run()).rejects.toThrow('Registry unavailable');
    expect(s.calls.some((c) => c.docker?.includes('create'))).toBe(false);
    expect(s.calls.some((c) => c.method && c.method !== 'GET')).toBe(false);
  });
  it.each([
    [{ failImage: true }, 'Registry unavailable'],
    [{ wrongRevision: true }, 'Image does not match verified release'],
    [{ registryMismatch: true }, 'Registry digest mismatch'],
  ])('rejects unavailable or inconsistent images: %j', async (options, error) => {
    const s = scenario(options);
    await expect(s.run()).rejects.toThrow(error);
    expect(s.calls.some((c) => c.url?.includes('/git/trees'))).toBe(false);
    expect(s.calls.some((c) => c.docker?.includes('create'))).toBe(false);
  });
  it('retains the index if GitHub Latest does not match the stable target', async () => {
    const s = scenario({ latestMismatch: true });
    await expect(s.run()).rejects.toThrow('GitHub Latest');
    expect(s.calls.some((c) => c.url?.includes('/git/trees'))).toBe(false);
  });
  it.each([false, true])(
    'reports a ref conflict without forcing an update (existing=%s)',
    async (existing) => {
      const s = scenario({ conflict: true, existing });
      await expect(s.run()).rejects.toThrow('422');
      expect(s.calls.some((c) => c.body?.force === true)).toBe(false);
    }
  );
  it('recovers an existing index from cached immutable metadata without downloading it again', async () => {
    const s = scenario({ existing: true });
    const index = await s.run();
    expect(index.revision).toBe(8);
    expect(s.calls.some((c) => c.url?.includes('/releases/download/'))).toBe(false);
    expect(s.calls.at(-1).body).toEqual({ sha: 'commit', force: false });
  });
  it('can bootstrap preview channels before the first metadata-bearing stable release', async () => {
    const s = scenario({ tags: ['v1.14.8-beta.1'] });
    const index = await s.run();
    expect(index.channels).toEqual({ stable: null, rc: null, beta: { version: 'v1.14.8-beta.1' } });
    expect(s.calls.some((c) => c.method === 'PATCH' && c.url?.includes('/releases/'))).toBe(false);
    const tree = s.calls.find((c) => c.url?.endsWith('/git/trees'));
    expect(tree.body.tree.some((f) => f.path === 'stable-version.txt')).toBe(false);
  });
  it('withdraws a preview recommendation and can restore it through the same flow', async () => {
    const tags = ['v1.14.7', 'v1.14.9-beta.1'];
    const withdrawn = scenario({ existing: true, tags, withdraw: tags[1] });
    expect((await withdrawn.run()).channels.beta.version).toBe(tags[0]);
    const restored = scenario({ existing: true, tags, withdrawn: [tags[1]], restore: tags[1] });
    expect((await restored.run()).channels.beta.version).toBe(tags[1]);
  });
  it('allows withdrawing the only stable release and generates all-null channels without stable-version.txt', async () => {
    const s = scenario({ existing: true, tags: ['v1.14.7'], withdraw: 'v1.14.7' });
    const index = await s.run();
    expect(index.channels).toEqual({ stable: null, rc: null, beta: null });
    const tree = s.calls.find((c) => c.url?.endsWith('/git/trees'));
    expect(tree.body.tree.some((f) => f.path === 'stable-version.txt')).toBe(false);
    const withdrawnEntry = tree.body.tree.find((f) => f.path === 'withdrawn.json');
    expect(JSON.parse(withdrawnEntry.content)).toContain('v1.14.7');
  });

  it('allows withdrawing stable when beta exists, resulting in stable=null and retaining beta', async () => {
    const s = scenario({
      existing: true,
      tags: ['v1.14.7', 'v1.14.9-beta.1'],
      withdraw: 'v1.14.7',
    });
    const index = await s.run();
    expect(index.channels.stable).toBeNull();
    expect(index.channels.rc).toBeNull();
    expect(index.channels.beta.version).toBe('v1.14.9-beta.1');
    const tree = s.calls.find((c) => c.url?.endsWith('/git/trees'));
    expect(tree.body.tree.some((f) => f.path === 'stable-version.txt')).toBe(false);
    const withdrawnEntry = tree.body.tree.find((f) => f.path === 'withdrawn.json');
    expect(JSON.parse(withdrawnEntry.content)).toContain('v1.14.7');
  });

  it('restores a withdrawn stable release and republishes stable-version.txt', async () => {
    const s = scenario({
      existing: true,
      tags: ['v1.14.7'],
      withdrawn: ['v1.14.7'],
      restore: 'v1.14.7',
    });
    const index = await s.run();
    expect(index.channels.stable.version).toBe('v1.14.7');
    const tree = s.calls.find((c) => c.url?.endsWith('/git/trees'));
    const stableFile = tree.body.tree.find((f) => f.path === 'stable-version.txt');
    expect(stableFile).toBeDefined();
    expect(stableFile.content).toBe('v1.14.7\n');
  });

  it('rejects withdrawing a non-existent or unverified release tag', async () => {
    const s = scenario({ existing: true, tags: ['v1.14.7'], withdraw: 'v99.99.99' });
    await expect(s.run()).rejects.toThrow(
      'Withdraw target must be an existing published release with release-info.json'
    );
  });
});

const v2Tag = 'v2.0.0-beta.1';
const v2Sha = '8935bce5ea7ce994185ee97acb8a4fdbd9cdd6cc';
const sha256Of = (value) => `sha256:${createHash('sha256').update(value).digest('hex')}`;
const notesOneX = (tag) =>
  '<!-- cpamp-update\n' +
  JSON.stringify({
    summary: { zh: '更新', en: 'Update' },
    update: {
      breaking: false,
      migration_required: false,
      minimum_direct_upgrade_version: null,
      upgrade_guide_url: repository + '/releases/tag/' + tag,
    },
    compatibility: { minimum_cpa_version: null },
  }) +
  '\n-->';
const v2ImageRef = (tag, role) =>
  `seakee/cpa-manager-plus:${v2DockerRoleTag(tag, role)}@${sha256Of(`${role}${tag}-manifest`)}`;
const v2ComposeFor = (tag) => [
  'name: cpa-manager-plus',
  'services:',
  '  cpa-manager-plus:',
  `    image: '${v2ImageRef(tag, 'full')}'`,
  '',
].join('\n');
const v2ComposeDigest = sha256Of(v2ComposeFor(v2Tag));
const v2Pairing = (tag) => ({
  schema_version: 1,
  version: tag,
  source_commit: v2Sha,
  platform: 'linux/amd64',
  cpa: { version: '8.0.55', artifact_id: sha256Of('cpa-artifact') },
  fullweb: {
    manifest_sha256: sha256Of('fullweb-manifest'),
    index_html_sha256: sha256Of('management-html'),
    asset_count: 3,
  },
  api_contract: { sha256: sha256Of('embedded-api-contract') },
  compose: {
    filename: 'docker-compose.v2-embedded.yml',
    sha256: sha256Of(v2ComposeFor(tag)),
    size: Buffer.byteLength(v2ComposeFor(tag), 'utf8'),
  },
  images: Object.fromEntries(
    ['slim', 'full'].map((role) => [
      role,
      {
        repository: 'seakee/cpa-manager-plus',
        tag: v2DockerRoleTag(tag, role),
        image_ref: v2ImageRef(tag, role),
        manifest_digest: sha256Of(`${role}${tag}-manifest`),
        image_config_digest: sha256Of(`${role}${tag}-config`),
        binary_sha256: sha256Of(`${role}${tag}-binary`),
        release_provenance_sha256: sha256Of(`${role}${tag}-provenance`),
      },
    ])
  ),
});
const v2Notes = (tag) =>
  '<!-- cpamp-update\n' +
  JSON.stringify({
    summary: { zh: '首个 v2 Beta', en: 'First v2 Beta' },
    update: {
      breaking: false,
      migration_required: false,
      minimum_direct_upgrade_version: 'v2.0.0-beta.1',
      upgrade_guide_url: 'https://cpamp-docs.seakee.dev/next/operations/update.html',
    },
    compatibility: { minimum_cpa_version: 'v8.0.55' },
  }) +
  '\n-->';
const makeV2Info = (tag = v2Tag) =>
  generateReleaseInfo(tag, v2Sha, v2Notes(tag), {
    dryRun: true,
    allowV2Beta: true,
    pairing: v2Pairing(tag),
  });
const v2StablePayload = (tag) =>
  JSON.stringify({
    schema_version: 2,
    release: { version: tag, stage: 'stable', source_commit: v2Sha },
  });
const temporaryCandidateDirectories = [];
afterEach(() => {
  for (const directory of temporaryCandidateDirectories.splice(0))
    rmSync(directory, { recursive: true, force: true });
});
const makeCandidateDirectory = (info) => {
  const directory = mkdtempSync(path.join(tmpdir(), 'cpamp-update-index-v2-'));
  temporaryCandidateDirectories.push(directory);
  const tag = info.release.version;
  writeFileSync(path.join(directory, 'release-info.json'), JSON.stringify(info, null, 2));
  const checksumLines = [];
  for (const name of expectedReleaseAssetNames(tag, { allowV2Beta: true })) {
    if (name === 'checksums.txt') continue;
    const relativePath = name.includes('_linux_amd64') ? `native/${name}` : name;
    const target = path.join(directory, relativePath);
    if (name !== 'release-info.json') {
      mkdirSync(path.dirname(target), { recursive: true });
      writeFileSync(
        target,
        name === 'embedded-release-pairing.json'
          ? JSON.stringify(v2Pairing(tag))
          : name === 'docker-compose.v2-embedded.yml'
            ? v2ComposeFor(tag)
            : `${name}\n`
      );
    }
    const content = name === 'release-info.json' ? JSON.stringify(info, null, 2) : (
      name === 'embedded-release-pairing.json'
        ? JSON.stringify(v2Pairing(tag))
        : name === 'docker-compose.v2-embedded.yml'
          ? v2ComposeFor(tag)
          : `${name}\n`
    );
    checksumLines.push(`${sha256Of(content).slice(7)}  ${relativePath}\n`);
  }
  writeFileSync(path.join(directory, 'checksums.txt'), checksumLines.join(''));
  return directory;
};

const publishedV2Assets = (tag, payload, composeDigest = v2ComposeDigest) =>
  expectedReleaseAssetNames(tag, { allowV2Beta: true }).map((name) => ({
    name,
    state: 'uploaded',
    size: name === 'release-info.json' ? Buffer.byteLength(payload) : 10,
    digest:
      name === 'release-info.json'
        ? sha256Of(payload)
        : name === 'docker-compose.v2-embedded.yml'
          ? composeDigest
          : sha256Of(name),
  }));

function v2Scenario({
  dryRun = true,
  allowV2Beta = true,
  allowV2BetaDryRun = false,
  candidate = true,
  candidateDir,
  existingIndex = true,
  existingV2Index = null,
  extraRelease,
  extraPayload,
  publishedCommit = v2Sha,
  candidateInfoPath,
  adjustImage = (inspection) => inspection,
} = {}) {
  const calls = [];
  const localInfoPath = candidateInfoPath ?? (candidateDir && path.join(candidateDir, 'release-info.json'));
  const localInfo = localInfoPath
    ? JSON.parse(readFileSync(localInfoPath, 'utf8'))
    : candidate ? makeV2Info() : null;
  const publishedInfo = extraPayload ? JSON.parse(extraPayload) : null;
  const images = [localInfo, publishedInfo].flatMap((info) => info?.distribution?.docker?.images ?? []);
  const stableTag = 'v1.14.10';
  const sha = 'a'.repeat(40);
  const stableInfo = generateReleaseInfo(stableTag, sha, notesOneX(stableTag));
  const stableData = JSON.stringify(stableInfo);
  const stableRelease = {
    id: 500,
    tag_name: stableTag,
    draft: false,
    immutable: true,
    prerelease: false,
    published_at: '2026-10-01T00:00:00Z',
    assets: expectedReleaseAssetNames(stableTag).map((name) => ({
      name,
      state: 'uploaded',
      size: name === 'release-info.json' ? Buffer.byteLength(stableData) : 10,
      digest: name === 'release-info.json' ? sha256Of(stableData) : `sha256:${'a'.repeat(64)}`,
    })),
  };
  const oldIndex = {
    schema_version: 1,
    revision: 7,
    generated_at: '2026-10-01T00:00:00Z',
    channels: {
      stable: { version: stableTag },
      rc: { version: stableTag },
      beta: { version: stableTag },
    },
  };
  const files = {
    'update-index.json': oldIndex,
    ...(existingV2Index ? { 'update-index-v2.json': existingV2Index } : {}),
    'release-catalog.json': { [stableTag]: { info: stableInfo, digest: sha256Of(stableData) } },
    'withdrawn.json': [],
  };
  const response = (body, status = 200) => new Response(JSON.stringify(body), { status });
  const fetchImpl = async (url, options = {}) => {
    const method = options.method || 'GET';
    calls.push({ url, method, body: options.body && JSON.parse(options.body) });
    if (url.includes('/git/ref/heads/'))
      return existingIndex ? response({ object: { sha: 'old-ref' } }) : response({}, 404);
    if (url.includes('/contents/')) {
      const name = new URL(url).pathname.split('/').at(-1);
      if (!(name in files) || files[name] == null) return response({}, 404);
      return response({ content: Buffer.from(JSON.stringify(files[name])).toString('base64') });
    }
    if (url.includes('/releases?'))
      return response([stableRelease, ...(extraRelease ? [extraRelease] : [])]);
    if (url.includes('/releases/download/')) {
      const tag = decodeURIComponent(new URL(url).pathname.split('/').at(-2));
      if (tag === stableTag) return new Response(stableData);
      if (extraPayload && extraRelease && tag === extraRelease.tag_name)
        return new Response(extraPayload);
      return new Response(v2StablePayload(tag));
    }
    if (url.endsWith('/git/commits/old-ref')) return response({ tree: { sha: 'original-tree' } });
    if (url.includes('/commits/')) {
      const tag = decodeURIComponent(new URL(url).pathname.split('/commits/')[1] || '');
      return response({ sha: extraRelease && tag === extraRelease.tag_name ? publishedCommit : sha });
    }
    // A plain 1.x maintenance run still advances Latest and commits the index.
    if (url.endsWith('/releases/latest')) return response(stableRelease);
    if (/\/releases\/\d+$/.test(url)) return response(stableRelease);
    if (url.endsWith('/git/trees')) return response({ sha: 'tree' });
    if (url.endsWith('/git/commits')) return response({ sha: 'commit' });
    if (url.includes('/git/refs')) return response({}, 201);
    throw new Error('Unexpected request: ' + url);
  };
  const aliases = new Map();
  const inspectPayload = (digest) =>
    JSON.stringify({
      manifest: {
        digest,
        manifests: ['amd64', 'arm64'].map((architecture) => ({
          platform: { os: 'linux', architecture },
        })),
      },
      image: Object.fromEntries(
        ['amd64', 'arm64'].map((arch) => [
          'linux/' + arch,
          {
            config: {
              Labels: {
                'org.opencontainers.image.revision': sha,
                'org.opencontainers.image.version': stableTag.slice(1),
                'org.opencontainers.image.source': repository,
              },
            },
          },
        ])
      ),
    });
  const exec = (_cmd, args) => {
    calls.push({ docker: args });
    if (args[2] === 'create') {
      if (dryRun) throw new Error('v2 dry run must not create image aliases');
      aliases.set(args[4], args[5].split('@')[1]);
      return '';
    }
    const reference = args[3];
    const image = images.find(({ name, tag }) => reference === name + ':' + tag);
    if (image) {
      return JSON.stringify(adjustImage({
        manifest: { digest: image.digest },
        image: {
          os: 'linux',
          architecture: 'amd64',
          config: { Labels: {
            'org.opencontainers.image.revision': v2Sha,
            'org.opencontainers.image.version': image.tag.replace(/-full$/, ''),
            'org.opencontainers.image.source': repository,
            'io.cpamp.candidate.role': image.role,
          } },
        },
      }, image, images));
    }
    if (aliases.has(reference)) return inspectPayload(aliases.get(reference));
    if (!reference.endsWith(':' + stableTag)) throw new Error('Unexpected image: ' + reference);
    return inspectPayload(sha256Of('stable-image'));
  };
  return {
    calls,
    run: () =>
      publishUpdateIndex({
        env: {
          GITHUB_REPOSITORY: 'seakee/CPA-Manager-Plus',
          GITHUB_TOKEN: 'fixture',
          WITHDRAW_RELEASE: '',
          RESTORE_RELEASE: '',
        },
        fetchImpl,
        exec,
        dryRun,
        allowV2Beta,
        allowV2BetaDryRun,
        candidateInfoPath,
        candidateAssetsDir:
          candidateDir ?? (candidate && !candidateInfoPath ? makeCandidateDirectory(localInfo) : undefined),
      }),
  };
}

describe('v2 beta update index rehearsal', () => {
  const runCli = (env, ...args) =>
    spawnSync(process.execPath, [fileURLToPath(new URL('../bin/release/publish-update-index.mjs', import.meta.url)), ...args], {
      encoding: 'utf8',
      env: { ...process.env, DRY_RUN: 'false', GITHUB_REPOSITORY: '', ...env },
    });

  it.each(['--candidate-assets-dir', '--candidate'])(
    'requires an explicit CLI --dry-run for %s even with v2 opt-in', (flag) => {
      for (const DRY_RUN of ['false', 'true']) {
        const result = runCli({ DRY_RUN }, '--allow-v2-beta', flag, 'unused');
        expect(result.status).toBe(1);
        expect(result.stderr).toContain('Candidate release inputs require --dry-run');
      }
    }
  );

  it.each(['candidateAssetsDir', 'candidateInfoPath'])(
    'rejects local %s in live v2 mode before GitHub or registry calls', async (input) => {
      const directory = makeCandidateDirectory(makeV2Info());
      const s = v2Scenario({
        dryRun: false,
        allowV2Beta: true,
        candidate: false,
        ...(input === 'candidateAssetsDir'
          ? { candidateDir: directory }
          : { candidateInfoPath: path.join(directory, 'release-info.json') }),
      });
      await expect(s.run()).rejects.toThrow('Candidate release inputs require --dry-run');
      expect(s.calls).toEqual([]);
    }
  );

  it('emits schema_version 2 update-index-v2.json in a dry run while keeping 1.x untouched and writing nothing', async () => {
    const candidateDir = makeCandidateDirectory(makeV2Info());
    const expectedSha = sha256Of(readFileSync(path.join(candidateDir, 'release-info.json')));
    const s = v2Scenario({ candidateDir });
    const indexV2 = await s.run();
    expect(indexV2).toMatchObject({
      schema_version: 2,
      revision: 1,
      channels: {
        stable: null,
        rc: null,
        beta: {
          version: v2Tag,
          schema_version: 2,
          release_info_path: `releases/download/${v2Tag}/release-info.json`,
          release_info_sha256: expectedSha,
        },
      },
    });
    expect(s.calls.some((call) => call.method && call.method !== 'GET')).toBe(false);
    expect(s.calls.some((call) => call.docker?.includes('create'))).toBe(false);
    expect(s.calls.some((call) => call.url?.includes('/git/trees'))).toBe(false);
    expect(s.calls.some((call) => call.url?.includes('/git/commits'))).toBe(false);
    expect(s.calls.some((call) => call.url?.includes('/git/refs'))).toBe(false);
    expect(s.calls.some((call) => call.url?.endsWith('/releases/latest'))).toBe(false);

    // Increments revision relative to an existing update-index-v2.json.
    const bumped = await v2Scenario({
      candidateDir,
      existingV2Index: {
        schema_version: 2,
        revision: 4,
        generated_at: '2026-11-20T00:00:00.000Z',
        channels: { stable: null, rc: null, beta: null },
      },
    }).run();
    expect(bumped.revision).toBe(5);
  });
  it('retains the legacy offline rehearsal and requires --dry-run for its flag', async () => {
    const s = v2Scenario({ allowV2Beta: false, allowV2BetaDryRun: true });
    expect((await s.run()).channels.beta.version).toBe(v2Tag);
    expect(s.calls.some((call) => call.docker)).toBe(false);
    expect(runCli({}, '--allow-v2-beta-dry-run').stderr).toContain('--allow-v2-beta-dry-run requires --dry-run');
    expect(runCli({}, '--allow-v2-beta').stderr).toContain('Unexpected repository');
  });

  it('refuses to rehearse a v2 Beta without an existing 1.x index', async () => {
    await expect(v2Scenario({ existingIndex: false }).run()).rejects.toThrow(
      /existing 1.x update index/
    );
  });

  it('rejects a candidate whose compose bytes or pairing disagree with release-info', async () => {
    const tampered = makeCandidateDirectory(makeV2Info());
    writeFileSync(path.join(tampered, 'docker-compose.v2-embedded.yml'), 'services: {}\n');
    await expect(
      publishUpdateIndex({
        env: { GITHUB_REPOSITORY: 'seakee/CPA-Manager-Plus', GITHUB_TOKEN: 'fixture' },
        fetchImpl: async () => {
          throw new Error('no request expected');
        },
        dryRun: true,
        allowV2Beta: true,
        candidateAssetsDir: tampered,
      })
    ).rejects.toThrow(/compose asset digest does not match release info/);
  });

  it('rejects a candidate whose checksums.txt digest or relative path is tampered', async () => {
    const env = { GITHUB_REPOSITORY: 'seakee/CPA-Manager-Plus', GITHUB_TOKEN: 'fixture' };
    const fetchImpl = async () => {
      throw new Error('no request expected');
    };
    const tamperedDigestDir = makeCandidateDirectory(makeV2Info());
    writeFileSync(
      path.join(tamperedDigestDir, 'checksums.txt'),
      `${'0'.repeat(64)}  management.html\n`
    );
    await expect(
      publishUpdateIndex({
        env,
        fetchImpl,
        dryRun: true,
        allowV2Beta: true,
        candidateAssetsDir: tamperedDigestDir,
      })
    ).rejects.toThrow(/checksums/);

    const tamperedPathDir = makeCandidateDirectory(makeV2Info());
    const nativeName = `cpa-manager-plus-native-v2_${v2Tag}_linux_amd64.tar.gz`;
    const validChecksums = readFileSync(path.join(tamperedPathDir, 'checksums.txt'), 'utf8');
    writeFileSync(
      path.join(tamperedPathDir, 'checksums.txt'),
      validChecksums.replace(`  native/${nativeName}\n`, `  ${nativeName}\n`)
    );
    await expect(
      publishUpdateIndex({
        env,
        fetchImpl,
        dryRun: true,
        allowV2Beta: true,
        candidateAssetsDir: tamperedPathDir,
      })
    ).rejects.toThrow(/checksums/);
  });

  it('rejects a candidate whose native archive bytes disagree with checksums.txt', async () => {
    const tamperedNative = makeCandidateDirectory(makeV2Info());
    writeFileSync(
      path.join(
        tamperedNative,
        'native',
        `cpa-manager-plus-native-v2_${v2Tag}_linux_amd64.tar.gz`
      ),
      'tampered-native-archive\n'
    );
    await expect(
      publishUpdateIndex({
        env: { GITHUB_REPOSITORY: 'seakee/CPA-Manager-Plus', GITHUB_TOKEN: 'fixture' },
        fetchImpl: async () => {
          throw new Error('no request expected');
        },
        dryRun: true,
        allowV2Beta: true,
        candidateAssetsDir: tamperedNative,
      })
    ).rejects.toThrow(/checksums/);
  });

  it('rejects a candidate whose management.html bytes disagree with checksums.txt', async () => {
    const tamperedHtml = makeCandidateDirectory(makeV2Info());
    writeFileSync(path.join(tamperedHtml, 'management.html'), 'tampered-management-panel\n');
    await expect(
      publishUpdateIndex({
        env: { GITHUB_REPOSITORY: 'seakee/CPA-Manager-Plus', GITHUB_TOKEN: 'fixture' },
        fetchImpl: async () => {
          throw new Error('no request expected');
        },
        dryRun: true,
        allowV2Beta: true,
        candidateAssetsDir: tamperedHtml,
      })
    ).rejects.toThrow(/checksums/);
  });

  it('rejects a --candidate path that is not the candidate release-info.json', async () => {
    const directory = makeCandidateDirectory(makeV2Info());
    await expect(
      publishUpdateIndex({
        env: { GITHUB_REPOSITORY: 'seakee/CPA-Manager-Plus', GITHUB_TOKEN: 'fixture' },
        dryRun: true,
        allowV2Beta: true,
        candidateAssetsDir: directory,
        candidateInfoPath: path.join(directory, 'other.json'),
      })
    ).rejects.toThrow(/--candidate must match/);
  });

  it('rejects a published v2 Beta whose compose asset digest drifted', async () => {
    const info = makeV2Info();
    const payload = JSON.stringify(info);
    const drifted = {
      id: 778,
      tag_name: v2Tag,
      draft: false,
      immutable: true,
      prerelease: true,
      published_at: '2026-11-06T00:00:00Z',
      assets: publishedV2Assets(v2Tag, payload, sha256Of('drifted')),
    };
    const scenario = v2Scenario({
      candidate: false,
      extraRelease: drifted,
      extraPayload: payload,
    });
    await expect(scenario.run()).rejects.toThrow(/compose asset digest mismatch/);
  });

  it('rejects a published v2 Beta made Latest instead of a Prerelease', async () => {
    const info = makeV2Info();
    const payload = JSON.stringify(info);
    const madeLatest = {
      id: 779,
      tag_name: v2Tag,
      draft: false,
      immutable: true,
      prerelease: true,
      make_latest: 'true',
      published_at: '2026-11-06T00:00:00Z',
      assets: publishedV2Assets(v2Tag, payload),
    };
    await expect(
      v2Scenario({ candidate: false, extraRelease: madeLatest, extraPayload: payload }).run()
    ).rejects.toThrow(/must stay a Prerelease and never become Latest/);
  });

  it('rejects a published v2 stable release instead of treating it as a channel update', async () => {
    const stableV2Release = {
      id: 777,
      tag_name: 'v2.0.0',
      draft: false,
      immutable: true,
      prerelease: false,
      published_at: '2026-11-06T00:00:00Z',
      assets: [
        {
          name: 'release-info.json',
          state: 'uploaded',
          size: Buffer.byteLength(v2StablePayload('v2.0.0')),
          digest: sha256Of(v2StablePayload('v2.0.0')),
        },
      ],
    };
    await expect(
      v2Scenario({ candidate: false, extraRelease: stableV2Release }).run()
    ).rejects.toThrow(/v2 Embedded/);
  });

  it('fails before IO for legacy opt-in without dry-run or a candidate without opt-in', async () => {
    const noDryRun = v2Scenario({ dryRun: false, allowV2Beta: false, allowV2BetaDryRun: true, candidate: false });
    await expect(noDryRun.run()).rejects.toThrow(/requires --dry-run/);
    expect(noDryRun.calls).toEqual([]);

    const noOptIn = v2Scenario({ allowV2Beta: false });
    await expect(noOptIn.run()).rejects.toThrow(/requires --allow-v2-beta/);
    expect(noOptIn.calls).toEqual([]);

    // A candidate path is v2 intent by itself: reject before reading it, which
    // is why a non-existent directory reports the gate error, not ENOENT.
    await expect(
      publishUpdateIndex({
        env: { GITHUB_REPOSITORY: 'seakee/CPA-Manager-Plus', GITHUB_TOKEN: 'fixture' },
        fetchImpl: async () => {
          throw new Error('no request expected');
        },
        dryRun: true,
        candidateAssetsDir: '/nonexistent-v2-candidate',
        candidateInfoPath: '/nonexistent-v2-candidate/release-info.json',
      })
    ).rejects.toThrow(/requires --allow-v2-beta/);
  });

  const publishedBeta = (tag = v2Tag) => {
    const extraPayload = JSON.stringify(makeV2Info(tag));
    return {
      extraPayload,
      extraRelease: {
        id: 781,
        tag_name: tag,
        draft: false,
        immutable: true,
        prerelease: true,
        published_at: '2026-11-06T00:00:00Z',
        assets: publishedV2Assets(tag, extraPayload, sha256Of(v2ComposeFor(tag))),
      },
    };
  };

  it.each([
    ['missing', () => undefined, 'At least one verified v2 Beta release is required'],
    ['draft', (release) => ({ ...release, draft: true }), 'At least one verified v2 Beta release is required'],
    ['unpublished', (release) => ({ ...release, published_at: null }), 'Unverified release candidate'],
    ['mutable', (release) => ({ ...release, immutable: false }), 'Unverified release candidate'],
    ['wrong tag commit', (release) => release, 'Unverified release candidate'],
    ['unverified asset', (release) => ({
      ...release,
      assets: release.assets.map((asset) => ({ ...asset, state: 'new' })),
    }), 'Unverified release assets'],
  ])('does not publish the v2 index for a %s prerelease even when its images exist', async (kind, adjustRelease, error) => {
    // Keep the registry fixtures for both v2 tags independent of the release list.
    const { extraRelease, extraPayload } = publishedBeta();
    const s = v2Scenario({
      dryRun: false,
      allowV2Beta: true,
      candidate: false,
      extraRelease: adjustRelease(extraRelease),
      extraPayload,
      publishedCommit: kind === 'wrong tag commit' ? 'b'.repeat(40) : v2Sha,
    });
    await expect(s.run()).rejects.toThrow(error);
    expect(s.calls.every((call) => call.method === 'GET')).toBe(true);
  });

  it('commits only v2 metadata over the existing tree without touching Latest or Docker aliases', async () => {
    const published = publishedBeta();
    const s = v2Scenario({ ...published, dryRun: false, candidate: false });
    const index = await s.run();
    expect(index).toMatchObject({
      schema_version: 2,
      channels: { stable: null, rc: null, beta: {
        version: v2Tag, release_info_sha256: sha256Of(published.extraPayload),
      } },
    });
    const info = JSON.parse(published.extraPayload);
    const dockerCalls = s.calls.filter((call) => call.docker).map((call) => call.docker);
    expect(dockerCalls.map((args) => [args[2], args[3]])).toEqual(
      info.distribution.docker.images.map(({ name, tag }) => ['inspect', name + ':' + tag])
    );
    const writes = s.calls.filter((call) => ['POST', 'PATCH'].includes(call.method));
    expect(writes.map((call) => [call.method, new URL(call.url).pathname.split('/repos/seakee/CPA-Manager-Plus')[1]])).toEqual([
      ['POST', '/git/trees'], ['POST', '/git/commits'], ['PATCH', '/git/refs/heads/update-channel'],
    ]);
    const [tree, commit, ref] = writes.map((call) => call.body);
    expect(tree.base_tree).toBe('original-tree');
    const files = Object.fromEntries(tree.tree.map((file) => [file.path, JSON.parse(file.content)]));
    expect(Object.keys(files).sort()).toEqual(['release-catalog.json', 'update-index-v2.json']);
    expect(files['update-index-v2.json']).toEqual(index);
    expect(files['release-catalog.json'][v2Tag]).toEqual({ info, digest: sha256Of(published.extraPayload) });
    expect(files['release-catalog.json']['v1.14.10'].info.release.version).toBe('v1.14.10');
    expect(commit).toMatchObject({ parents: ['old-ref'], tree: 'tree' });
    expect(ref).toEqual({ sha: 'commit', force: false });
    expect(s.calls.some((call) => call.url?.endsWith('/releases/latest'))).toBe(false);
  });

  it('checks local and published v2 images with the new dry-run flag without any writes', async () => {
    const candidateDir = makeCandidateDirectory(makeV2Info());
    const s = v2Scenario({
      ...publishedBeta('v2.0.0-beta.2'),
      candidate: false,
      candidateInfoPath: path.join(candidateDir, 'release-info.json'),
    });
    expect((await s.run()).channels.beta.version).toBe('v2.0.0-beta.2');
    expect(s.calls.filter((call) => call.docker?.[2] === 'inspect')).toHaveLength(8);
    expect(s.calls.some((call) => call.method && call.method !== 'GET')).toBe(false);
  });

  it.each(['wrong digest', 'wrong manifest role', 'wrong config role', 'missing config role'])(
    'rejects the slim tag with %s before publication', async (failure) => {
      const s = v2Scenario({
        ...publishedBeta(),
        dryRun: false,
        candidate: false,
        adjustImage: (inspection, record, images) => {
          if (record.role !== 'slim') return inspection;
          if (failure === 'wrong digest') {
            inspection.manifest.digest = images.find(({ role }) => role === 'full').digest;
          } else if (failure === 'wrong manifest role') {
            inspection.manifest.labels = { 'io.cpamp.candidate.role': 'full' };
          } else if (failure === 'missing config role') {
            inspection.manifest.labels = { 'io.cpamp.candidate.role': record.role };
            delete inspection.image.config.Labels['io.cpamp.candidate.role'];
          } else {
            inspection.image.config.Labels['io.cpamp.candidate.role'] = 'full';
          }
          return inspection;
        },
      });
      await expect(s.run()).rejects.toThrow(/Image does not match verified release/);
      expect(s.calls.some((call) => call.method && call.method !== 'GET')).toBe(false);
      expect(s.calls.some((call) => call.docker?.[2] === 'create')).toBe(false);
    }
  );

  it('keeps a plain 1.x run working and preserves existing update-index-v2.json while a remote v2 Beta exists', async () => {
    const payload = JSON.stringify(makeV2Info());
    const v2BetaRelease = {
      id: 780,
      tag_name: v2Tag,
      draft: false,
      immutable: true,
      prerelease: true,
      published_at: '2026-11-06T00:00:00Z',
      assets: publishedV2Assets(v2Tag, payload),
    };
    const existingV2Index = {
      schema_version: 2,
      revision: 2,
      generated_at: '2026-11-06T00:00:00.000Z',
      channels: {
        stable: null,
        rc: null,
        beta: {
          version: v2Tag,
          schema_version: 2,
          release_info_path: `releases/download/${v2Tag}/release-info.json`,
          release_info_sha256: sha256Of(payload),
        },
      },
    };
    const s = v2Scenario({
      dryRun: false,
      allowV2Beta: false,
      candidate: false,
      existingV2Index,
      extraRelease: v2BetaRelease,
      extraPayload: payload,
    });
    const index = await s.run();
    expect(index.schema_version).toBe(1);
    expect(index.channels).toEqual({
      stable: { version: 'v1.14.10' },
      rc: { version: 'v1.14.10' },
      beta: { version: 'v1.14.10' },
    });
    // No v2 download, commit lookup, or catalog entry: the release is skipped
    // before its release-info is fetched.
    expect(s.calls.some((call) => call.url?.includes('/releases/download/'))).toBe(false);
    expect(s.calls.some((call) => call.url?.includes('/commits/v2'))).toBe(false);
    const tree = s.calls.find((call) => call.url?.endsWith('/git/trees'));
    const files = Object.fromEntries(tree.body.tree.map((file) => [file.path, file.content]));
    expect(files['release-catalog.json']).not.toContain(v2Tag);
    expect(files['withdrawn.json']).toBe('[]\n');
    expect(files['update-index.json']).not.toContain(v2Tag);
    expect(JSON.parse(files['update-index-v2.json'])).toEqual(existingV2Index);
    // The 1.x maintenance path still commits its index and alias.
    expect(
      s.calls.some((call) => call.url?.endsWith('/git/commits') && call.method === 'POST')
    ).toBe(true);
    expect(s.calls.some((call) => call.docker?.includes('create'))).toBe(true);
  });
  it('rejects truthy non-boolean gate values', async () => {
    const env = { GITHUB_REPOSITORY: 'seakee/CPA-Manager-Plus', GITHUB_TOKEN: 'fixture' };
    const fetchImpl = async () => {
      throw new Error('no request expected');
    };
    for (const value of ['false', 'true', '0', '', 0, 1, null])
      await expect(
        publishUpdateIndex({ env, fetchImpl, dryRun: value, allowV2Beta: true })
      ).rejects.toThrow(/dryRun must be a boolean/);
    for (const value of ['false', 'true', '0', '', 0, 1, null])
      await expect(
        publishUpdateIndex({ env, fetchImpl, dryRun: true, allowV2Beta: value })
      ).rejects.toThrow(/allowV2Beta must be a boolean/);
    // Real booleans open the gate; the sentinel proves the request was attempted.
    await expect(
      publishUpdateIndex({ env, fetchImpl, dryRun: true, allowV2Beta: true })
    ).rejects.toThrow(/no request expected/);
    await expect(
      publishUpdateIndex({ env, fetchImpl, dryRun: false, allowV2Beta: false })
    ).rejects.toThrow(/no request expected/);
    // Explicit undefined means "not provided" and takes the documented default.
    await expect(
      publishUpdateIndex({ env, fetchImpl, dryRun: undefined, allowV2Beta: undefined })
    ).rejects.toThrow(/no request expected/);
  });
});
