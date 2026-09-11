import { createHash } from 'node:crypto';
import { describe, expect, it } from 'vitest';
import { generateReleaseInfo } from '../bin/release/generate-release-info.mjs';
import { expectedReleaseAssetNames } from '../bin/release/verify-published-release.mjs';
import { publishUpdateIndex } from '../bin/release/publish-update-index.mjs';
import { repository, resolveChannels } from '../bin/release/update-contract.mjs';

function scenario({
  tags = ['v2.0.0'],
  failImage = false,
  conflict = false,
  existing = false,
  withdrawn = [],
  withdraw = '',
  restore = '',
  wrongRevision = false,
  registryMismatch = false,
  latestMismatch = false,
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
  it('publishes the index after exact image validation, aliases and GitHub Latest', async () => {
    const s = scenario();
    const index = await s.run();
    expect(index.channels.stable.version).toBe('v2.0.0');
    expect(s.calls.at(-1).body).toEqual({ ref: 'refs/heads/update-channel', sha: 'commit' });
    const tree = s.calls.find((c) => c.url?.endsWith('/git/trees'));
    expect(tree.body.tree.find((f) => f.path === 'stable-version.txt').content).toBe('v2.0.0\n');
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
      tags: ['v1.13.0', 'v2.0.0-rc.2', 'v2.1.0-beta.1'],
      failImage: 'v2.0.0-rc.2',
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
    const s = scenario({ tags: ['v2.0.0-beta.1'] });
    const index = await s.run();
    expect(index.channels).toEqual({ stable: null, rc: null, beta: { version: 'v2.0.0-beta.1' } });
    expect(s.calls.some((c) => c.method === 'PATCH' && c.url?.includes('/releases/'))).toBe(false);
    const tree = s.calls.find((c) => c.url?.endsWith('/git/trees'));
    expect(tree.body.tree.some((f) => f.path === 'stable-version.txt')).toBe(false);
  });
  it('withdraws a preview recommendation and can restore it through the same flow', async () => {
    const tags = ['v2.0.0', 'v2.1.0-beta.1'];
    const withdrawn = scenario({ existing: true, tags, withdraw: tags[1] });
    expect((await withdrawn.run()).channels.beta.version).toBe(tags[0]);
    const restored = scenario({ existing: true, tags, withdrawn: [tags[1]], restore: tags[1] });
    expect((await restored.run()).channels.beta.version).toBe(tags[1]);
  });
  it('allows withdrawing the only stable release and generates all-null channels without stable-version.txt', async () => {
    const s = scenario({ existing: true, tags: ['v2.0.0'], withdraw: 'v2.0.0' });
    const index = await s.run();
    expect(index.channels).toEqual({ stable: null, rc: null, beta: null });
    const tree = s.calls.find((c) => c.url?.endsWith('/git/trees'));
    expect(tree.body.tree.some((f) => f.path === 'stable-version.txt')).toBe(false);
    const withdrawnEntry = tree.body.tree.find((f) => f.path === 'withdrawn.json');
    expect(JSON.parse(withdrawnEntry.content)).toContain('v2.0.0');
  });

  it('allows withdrawing stable when beta exists, resulting in stable=null and retaining beta', async () => {
    const s = scenario({ existing: true, tags: ['v2.0.0', 'v2.1.0-beta.1'], withdraw: 'v2.0.0' });
    const index = await s.run();
    expect(index.channels.stable).toBeNull();
    expect(index.channels.rc).toBeNull();
    expect(index.channels.beta.version).toBe('v2.1.0-beta.1');
    const tree = s.calls.find((c) => c.url?.endsWith('/git/trees'));
    expect(tree.body.tree.some((f) => f.path === 'stable-version.txt')).toBe(false);
    const withdrawnEntry = tree.body.tree.find((f) => f.path === 'withdrawn.json');
    expect(JSON.parse(withdrawnEntry.content)).toContain('v2.0.0');
  });

  it('restores a withdrawn stable release and republishes stable-version.txt', async () => {
    const s = scenario({
      existing: true,
      tags: ['v2.0.0'],
      withdrawn: ['v2.0.0'],
      restore: 'v2.0.0',
    });
    const index = await s.run();
    expect(index.channels.stable.version).toBe('v2.0.0');
    const tree = s.calls.find((c) => c.url?.endsWith('/git/trees'));
    const stableFile = tree.body.tree.find((f) => f.path === 'stable-version.txt');
    expect(stableFile).toBeDefined();
    expect(stableFile.content).toBe('v2.0.0\n');
  });

  it('rejects withdrawing a non-existent or unverified release tag', async () => {
    const s = scenario({ existing: true, tags: ['v2.0.0'], withdraw: 'v99.99.99' });
    await expect(s.run()).rejects.toThrow(
      'Withdraw target must be an existing published release with release-info.json'
    );
  });
});
