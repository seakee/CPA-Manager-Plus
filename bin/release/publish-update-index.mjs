import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { expectedReleaseAssetNames } from './verify-published-release.mjs';
import {
  validateInfo,
  resolveChannels,
  compareVersions,
  parseVersion,
  repository,
} from './update-contract.mjs';

export function verifyCandidate(release, info, sha) {
  const tag = info.release.version;
  validateInfo(info, tag);
  if (
    release.tag_name !== tag ||
    release.draft ||
    !release.immutable ||
    !release.published_at ||
    release.prerelease !== (info.release.stage !== 'stable') ||
    sha !== info.release.source_commit
  )
    throw new Error('Unverified release candidate');
  const names = release.assets.map((a) => a.name).sort();
  if (JSON.stringify(names) !== JSON.stringify(expectedReleaseAssetNames(tag)))
    throw new Error('Incomplete release asset set');
  if (
    release.assets.some(
      (a) => a.state !== 'uploaded' || a.size <= 0 || !/^sha256:[0-9a-f]{64}$/.test(a.digest)
    )
  )
    throw new Error('Unverified release assets');
}
export function resolveAliases(infos, withdrawn) {
  const selected = infos.filter((i) => !withdrawn.includes(i.release.version));
  const channels = resolveChannels(selected);
  const aliases = {};
  if (channels.stable) aliases.latest = channels.stable.version;
  if (channels.beta) aliases.preview = channels.beta.version;
  for (const info of selected) {
    if (info.release.stage !== 'stable') continue;
    const tag = info.release.version,
      minor = parseVersion(tag).core.slice(0, 2).join('.');
    if (!aliases[minor] || compareVersions(tag, aliases[minor]) > 0) aliases[minor] = tag;
  }
  return { channels, aliases };
}
export function verifyImage(inspected, info) {
  const { manifest, image } = inspected;
  const configs = Object.values(image || {});
  const platforms = manifest?.manifests || [];
  const tag = info.release.version;
  if (
    !/^sha256:[0-9a-f]{64}$/.test(manifest?.digest) ||
    configs.length !== 2 ||
    ['amd64', 'arm64'].some(
      (arch) =>
        platforms.filter((m) => m.platform?.os === 'linux' && m.platform.architecture === arch)
          .length !== 1
    ) ||
    configs.some((config) => {
      const labels = config.config?.Labels;
      return (
        labels?.['org.opencontainers.image.revision'] !== info.release.source_commit ||
        labels?.['org.opencontainers.image.version'] !== tag.slice(1) ||
        labels?.['org.opencontainers.image.source'] !== repository
      );
    })
  )
    throw new Error('Image does not match verified release: ' + tag);
  return manifest.digest;
}
async function readReleaseInfo(response, asset) {
  const reader = response.body?.getReader();
  if (!reader) throw new Error('Missing release info body');
  const chunks = [];
  let size = 0;
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      size += value.length;
      if (size > 256 * 1024) throw new Error('Release info is too large');
      chunks.push(value);
    }
  } finally {
    await reader.cancel();
  }
  const bytes = Buffer.concat(chunks);
  if (
    size !== asset.size ||
    'sha256:' + createHash('sha256').update(bytes).digest('hex') !== asset.digest
  )
    throw new Error('Release info size or digest mismatch');
  return JSON.parse(bytes.toString());
}
export async function publishUpdateIndex({
  env = process.env,
  fetchImpl = fetch,
  exec = execFileSync,
} = {}) {
  if (env.GITHUB_REPOSITORY !== 'seakee/CPA-Manager-Plus') throw new Error('Unexpected repository');
  const token = env.GITHUB_TOKEN;
  if (!token) throw new Error('Missing GitHub token');
  const apiBase = 'https://api.github.com/repos/seakee/CPA-Manager-Plus';
  const api = async (path, method = 'GET', body, allow404 = false) => {
    const res = await fetchImpl(apiBase + path, {
      method,
      headers: {
        Authorization: `Bearer ${token}`,
        Accept: 'application/vnd.github+json',
        'Content-Type': 'application/json',
      },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: AbortSignal.timeout(30_000),
    });
    if (allow404 && res.status === 404) return null;
    if (!res.ok) throw new Error(`GitHub ${method} ${path}: ${res.status}`);
    return res.json();
  };
  const ref = await api('/git/ref/heads/update-channel', 'GET', undefined, true);
  const read = async (path, fallback) => {
    if (!ref) return fallback;
    const file = await api('/contents/' + path + '?ref=' + ref.object.sha, 'GET', undefined, true);
    return file ? JSON.parse(Buffer.from(file.content, 'base64').toString()) : fallback;
  };
  const old = await read('update-index.json', null);
  if (
    old &&
    (old.schema_version !== 1 ||
      !Number.isSafeInteger(old.revision) ||
      old.revision < 1 ||
      old.revision >= Number.MAX_SAFE_INTEGER)
  )
    throw new Error('Invalid existing update index');
  const catalog = await read('release-catalog.json', {});
  const withdrawn = await read('withdrawn.json', []);
  const withdraw = env.WITHDRAW_RELEASE || '',
    restore = env.RESTORE_RELEASE || '';
  if (withdraw && restore) throw new Error('Choose withdraw or restore');
  for (const tag of [withdraw, restore].filter(Boolean)) parseVersion(tag);
  const releases = [];
  for (let page = 1; ; page++) {
    const batch = await api('/releases?per_page=100&page=' + page);
    releases.push(...batch);
    if (batch.length < 100) break;
  }
  if (withdraw) {
    const target = releases.find(
      (r) =>
        r.tag_name === withdraw &&
        !r.draft &&
        r.assets?.some((a) => a.name === 'release-info.json')
    );
    if (!target) {
      throw new Error(
        'Withdraw target must be an existing published release with release-info.json'
      );
    }
    if (!withdrawn.includes(withdraw)) withdrawn.push(withdraw);
  }
  if (restore) {
    const i = withdrawn.indexOf(restore);
    if (i >= 0) withdrawn.splice(i, 1);
  }
  const infos = [];
  for (const release of releases) {
    const tag = release.tag_name;
    // Historical releases lacking this asset are deliberately excluded.
    const asset = release.assets.find((a) => a.name === 'release-info.json');
    if (release.draft || !asset || withdrawn.includes(tag)) continue;
    let info = catalog[tag]?.info;
    if (!info || catalog[tag].digest !== asset.digest) {
      if (!Number.isSafeInteger(asset.size) || asset.size < 1 || asset.size > 256 * 1024)
        throw new Error('Invalid release info asset size: ' + tag);
      const res = await fetchImpl(
        repository + '/releases/download/' + encodeURIComponent(tag) + '/release-info.json',
        { signal: AbortSignal.timeout(30_000) }
      );
      if (!res.ok) throw new Error('Release info download failed: ' + tag);
      info = await readReleaseInfo(res, asset);
    }
    const commit = await api('/commits/' + encodeURIComponent(tag));
    verifyCandidate(release, info, commit.sha);
    catalog[tag] = { info, digest: asset.digest };
    infos.push(info);
  }
  const { channels, aliases } = resolveAliases(infos, withdrawn);
  if (!infos.length && !withdraw) throw new Error('At least one verified release is required');
  const images = ['ghcr.io/seakee/cpa-manager-plus', 'seakee/cpa-manager-plus'];
  // Validate all precise references first, before any alias mutation.
  const targets = new Set([
    ...Object.values(aliases),
    ...Object.values(channels)
      .filter(Boolean)
      .map((target) => target.version),
  ]);
  const candidates = new Map(infos.map((info) => [info.release.version, info]));
  const digests = new Map();
  const inspect = (reference) =>
    JSON.parse(
      exec('docker', ['buildx', 'imagetools', 'inspect', reference, '--format', '{{json .}}'], {
        encoding: 'utf8',
      })
    );
  for (const image of images)
    for (const tag of targets) {
      const digest = verifyImage(inspect(image + ':' + tag), candidates.get(tag));
      if (digests.has(tag) && digests.get(tag) !== digest)
        throw new Error('Registry digest mismatch: ' + tag);
      digests.set(tag, digest);
    }
  for (const image of images)
    for (const [alias, tag] of Object.entries(aliases)) {
      exec(
        'docker',
        [
          'buildx',
          'imagetools',
          'create',
          '--tag',
          image + ':' + alias,
          image + '@' + digests.get(tag),
        ],
        { stdio: 'inherit' }
      );
      const actual = verifyImage(inspect(image + ':' + alias), candidates.get(tag));
      if (actual !== digests.get(tag)) throw new Error('Alias digest mismatch: ' + alias);
    }
  if (channels.stable) {
    const stable = releases.find((r) => r.tag_name === channels.stable.version);
    await api('/releases/' + stable.id, 'PATCH', { make_latest: 'true' });
    const latest = await api('/releases/latest');
    if (latest.id !== stable.id || latest.tag_name !== channels.stable.version)
      throw new Error('GitHub Latest did not advance to the stable target');
  }
  const index = {
    schema_version: 1,
    revision: (old?.revision || 0) + 1,
    generated_at: new Date().toISOString(),
    channels,
  };
  const files = {
    'update-index.json': JSON.stringify(index, null, 2) + '\n',
    'release-catalog.json': JSON.stringify(catalog, null, 2) + '\n',
    'withdrawn.json': JSON.stringify(withdrawn, null, 2) + '\n',
    // Minimal projection for POSIX installers without a JSON parser.
    ...(channels.stable ? { 'stable-version.txt': channels.stable.version + '\n' } : {}),
  };
  const tree = await api('/git/trees', 'POST', {
    tree: Object.entries(files).map(([path, content]) => ({
      path,
      mode: '100644',
      type: 'blob',
      content,
    })),
  });
  const commit = await api('/git/commits', 'POST', {
    message: 'chore(updates): refresh verified release channels',
    tree: tree.sha,
    parents: ref ? [ref.object.sha] : [],
  });
  // Non-force ref updates reject concurrent writers. Re-run recomputes from current state.
  if (ref) await api('/git/refs/heads/update-channel', 'PATCH', { sha: commit.sha, force: false });
  else await api('/git/refs', 'POST', { ref: 'refs/heads/update-channel', sha: commit.sha });
  return index;
}
if (process.argv[1] === fileURLToPath(import.meta.url))
  publishUpdateIndex()
    .then((index) => process.stdout.write(JSON.stringify(index, null, 2) + '\n'))
    .catch((err) => {
      console.error(err.message);
      process.exitCode = 1;
    });
