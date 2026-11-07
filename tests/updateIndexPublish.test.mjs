import { createHash } from 'node:crypto';
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import { generateReleaseInfo } from '../bin/release/generate-release-info.mjs';
import { expectedReleaseAssetNames } from '../bin/release/verify-published-release.mjs';
import { publishUpdateIndex } from '../bin/release/publish-update-index.mjs';
import { repository, resolveChannels } from '../bin/release/update-contract.mjs';

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
  ])('preserves an existing v2 beta target during plain 1.x maintenance: %j', async (existingBeta) => {
    const s = scenario({ existing: true, existingBeta });
    await expect(s.run()).rejects.toThrow('refusing a 1.x overwrite');
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
const v2ComposeDigest = sha256Of(v2ComposeContent);
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
    sha256: v2ComposeDigest,
    size: v2ComposeContent.length,
  },
  images: Object.fromEntries(
    Object.entries({
      manager: 'cpa-manager-plus',
      ingress: 'cpamp-ingress',
      runtime: 'cpamp-runtime',
    }).map(([role, repositoryName]) => [
      role,
      {
        repository: `seakee/${repositoryName}`,
        tag,
        manifest_digest: sha256Of(`${role}${tag}-manifest`),
        image_config_digest: sha256Of(`${role}${tag}-config`),
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
      breaking: true,
      migration_required: true,
      minimum_direct_upgrade_version: 'v1.14.10',
      upgrade_guide_url: repository + '/releases/tag/' + tag,
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
            ? v2ComposeContent
            : `${name}\n`
      );
    }
    const content = name === 'release-info.json' ? JSON.stringify(info, null, 2) : (
      name === 'embedded-release-pairing.json'
        ? JSON.stringify(v2Pairing(tag))
        : name === 'docker-compose.v2-embedded.yml'
          ? v2ComposeContent
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
  candidate = true,
  candidateDir,
  existingIndex = true,
  extraRelease,
  extraPayload,
} = {}) {
  const calls = [];
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
    if (url.includes('/commits/')) {
      const tag = decodeURIComponent(new URL(url).pathname.split('/commits/')[1] || '');
      return response({ sha: extraRelease && tag === extraRelease.tag_name ? v2Sha : sha });
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
        candidateAssetsDir:
          candidateDir ?? (candidate ? makeCandidateDirectory(makeV2Info()) : undefined),
      }),
  };
}

describe('v2 beta update index rehearsal', () => {
  it('moves only channels.beta in a dry run and writes nothing', async () => {
    const s = v2Scenario();
    const index = await s.run();
    expect(index).toMatchObject({
      schema_version: 1,
      revision: 8,
      channels: {
        stable: { version: 'v1.14.10' },
        rc: { version: 'v1.14.10' },
        beta: { version: v2Tag, schema_version: 2 },
      },
    });
    expect(JSON.stringify(index.channels.stable)).toBe(JSON.stringify({ version: 'v1.14.10' }));
    expect(s.calls.some((call) => call.method && call.method !== 'GET')).toBe(false);
    expect(s.calls.some((call) => call.docker?.includes('create'))).toBe(false);
    expect(s.calls.some((call) => call.url?.includes('/git/trees'))).toBe(false);
    expect(s.calls.some((call) => call.url?.includes('/git/commits'))).toBe(false);
    expect(s.calls.some((call) => call.url?.includes('/git/refs'))).toBe(false);
    expect(s.calls.some((call) => call.url?.endsWith('/releases/latest'))).toBe(false);
  });

  it('keeps the v2 publication fail-closed without the dry-run opt-in', async () => {
    await expect(v2Scenario({ dryRun: false }).run()).rejects.toThrow(/DRY_RUN opt-in/);
    await expect(v2Scenario({ allowV2Beta: false }).run()).rejects.toThrow(/DRY_RUN opt-in/);
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

  it('fails closed before any IO when only one gate is present', async () => {
    const noDryRun = v2Scenario({ dryRun: false, candidate: false });
    await expect(noDryRun.run()).rejects.toThrow(/DRY_RUN opt-in/);
    expect(noDryRun.calls).toEqual([]);

    const noOptIn = v2Scenario({ allowV2Beta: false });
    await expect(noOptIn.run()).rejects.toThrow(/DRY_RUN opt-in/);
    expect(noOptIn.calls).toEqual([]);

    // A candidate path is v2 intent by itself: reject before reading it, which
    // is why a non-existent directory reports the gate error, not ENOENT.
    await expect(
      publishUpdateIndex({
        env: { GITHUB_REPOSITORY: 'seakee/CPA-Manager-Plus', GITHUB_TOKEN: 'fixture' },
        fetchImpl: async () => {
          throw new Error('no request expected');
        },
        candidateAssetsDir: '/nonexistent-v2-candidate',
        candidateInfoPath: '/nonexistent-v2-candidate/release-info.json',
      })
    ).rejects.toThrow(/DRY_RUN opt-in/);
  });

  it('keeps a plain 1.x run working while a remote v2 Beta exists', async () => {
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
    const s = v2Scenario({
      dryRun: false,
      allowV2Beta: false,
      candidate: false,
      extraRelease: v2BetaRelease,
      extraPayload: payload,
    });
    const index = await s.run();
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
