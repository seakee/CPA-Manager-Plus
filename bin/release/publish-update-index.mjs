import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
import { existsSync, readFileSync } from 'node:fs';
import { expectedReleaseAssetNames, verifyLocalReleaseAssets } from './verify-published-release.mjs';
import {
  validateInfo,
  resolveChannels,
  compareVersions,
  parseVersion,
  assertSupportedVersion,
  validateIndex,
  assertV2AssetSet,
  assertBooleanOption,
  isV2Version,
  v2EmbeddedComposeFilename,
  repository,
} from './update-contract.mjs';

export function verifyCandidate(release, info, sha, options = {}) {
  const tag = info.release.version;
  const version = assertSupportedVersion(tag, options);
  validateInfo(info, tag, options);
  if (
    release.tag_name !== tag ||
    release.draft ||
    !release.immutable ||
    !release.published_at ||
    release.prerelease !== (info.release.stage !== 'stable') ||
    sha !== info.release.source_commit
  )
    throw new Error('Unverified release candidate');
  if (version.core[0] === '2') {
    if (
      release.prerelease !== true ||
      release.make_latest === 'true' ||
      release.make_latest === true
    )
      throw new Error('v2 Beta must stay a Prerelease and never become Latest');
    const compose = release.assets.find((asset) => asset.name === v2EmbeddedComposeFilename);
    if (!compose || compose.digest !== info.distribution.compose.sha256)
      throw new Error('v2 compose asset digest mismatch');
  }
  const names = release.assets.map((a) => a.name).sort();
  if (version.core[0] === '2') assertV2AssetSet(names, tag);
  else if (JSON.stringify(names) !== JSON.stringify(expectedReleaseAssetNames(tag)))
    throw new Error('Incomplete release asset set');
  if (
    release.assets.some(
      (a) => a.state !== 'uploaded' || a.size <= 0 || !/^sha256:[0-9a-f]{64}$/.test(a.digest)
    )
  )
    throw new Error('Unverified release assets');
}
export function resolveAliases(infos, withdrawn, options = {}) {
  const selected = infos.filter((i) => !withdrawn.includes(i.release.version));
  const channels = resolveChannels(selected, [], options);
  const aliases = {};
  if (channels.stable) aliases.latest = channels.stable.version;
  // v2 Beta is never an alias target: the 1.x image namespace must stay put.
  if (channels.beta && parseVersion(channels.beta.version).core[0] === '1')
    aliases.preview = channels.beta.version;
  for (const info of selected) {
    if (parseVersion(info.release.version).core[0] !== '1') continue;
    if (info.release.stage !== 'stable') continue;
    const tag = info.release.version,
      minor = parseVersion(tag).core.slice(0, 2).join('.');
    if (!aliases[minor] || compareVersions(tag, aliases[minor]) > 0) aliases[minor] = tag;
  }
  return { channels, aliases };
}
export function verifyImage(inspected, info, options = {}) {
  const tag = info.release.version;
  const version = assertSupportedVersion(tag, options);
  const { manifest, image } = inspected;
  const configs = Object.values(image || {});
  const platforms = manifest?.manifests || [];
  if (version.core[0] === '2') {
    const record = info.distribution.docker.images.find(
      ({ digest }) => digest === manifest?.digest
    );
    const matches = platforms.filter(
      (entry) => entry.platform?.os === 'linux' && entry.platform.architecture === 'amd64'
    );
    if (
      !record ||
      !/^sha256:[0-9a-f]{64}$/.test(manifest?.digest) ||
      configs.length !== 1 ||
      platforms.length !== 1 ||
      matches.length !== 1 ||
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

// Local pre-publication rehearsal: the v2 Beta release does not exist on GitHub
// yet, so the candidate asset directory is the source of truth.
const readCandidateInfo = (assetsDir) => {
  const infoPath = path.join(assetsDir, 'release-info.json');
  if (!existsSync(infoPath))
    throw new Error('Candidate assets directory is missing release-info.json');
  const info = JSON.parse(readFileSync(infoPath, 'utf8'));
  if (info?.schema_version !== 2)
    throw new Error('Candidate assets are only supported for the v2 beta dry run');
  const tag = info.release?.version;
  verifyLocalReleaseAssets({
    tag,
    assetsDir,
    dryRun: true,
    allowV2Beta: true,
  });
  return info;
};

export async function publishUpdateIndex({
  env = process.env,
  fetchImpl = fetch,
  exec = execFileSync,
  dryRun = env.DRY_RUN === 'true',
  allowV2Beta = false,
  candidateAssetsDir,
  candidateInfoPath,
} = {}) {
  // Explicit booleans only: a truthy string such as 'false' never authorizes.
  const dryRunValue = assertBooleanOption({ dryRun }, 'dryRun');
  const allowV2BetaValue = assertBooleanOption({ allowV2Beta }, 'allowV2Beta');
  const v2BetaOptIn = allowV2BetaValue === true && dryRunValue === true;
  const v2Intent =
    allowV2BetaValue === true ||
    candidateAssetsDir !== undefined ||
    candidateInfoPath !== undefined;
  // Gate M3: fail closed before any file read or network request. A plain 1.x
  // enumeration carries no v2 intent, so a remote v2 Beta is skipped in the
  // release loop instead of breaking the 1.x maintenance job.
  if (v2Intent && !v2BetaOptIn)
    throw new Error(
      'Unqualified v2 Embedded release: v2 rehearsal is fail-closed (Gate M3); the v2 beta DRY_RUN opt-in requires dryRun === true and allowV2Beta === true'
    );
  if (candidateAssetsDir || candidateInfoPath) {
    if (!candidateAssetsDir)
      throw new Error('Candidate release info requires a candidate assets directory');
    if (
      candidateInfoPath &&
      path.resolve(candidateInfoPath) !== path.resolve(candidateAssetsDir, 'release-info.json')
    )
      throw new Error('--candidate must match --candidate-assets-dir/release-info.json');
  }
  const withdraw = env.WITHDRAW_RELEASE || '',
    restore = env.RESTORE_RELEASE || '';
  if (withdraw && restore) throw new Error('Choose withdraw or restore');
  for (const tag of [withdraw, restore].filter(Boolean)) {
    parseVersion(tag);
    if (isV2Version(tag) && !v2BetaOptIn)
      throw new Error(
        'Unqualified v2 Embedded release: withdrawing a v2 release requires the v2 beta DRY_RUN opt-in'
      );
  }
  // Local files first: a tampered candidate must fail before any GitHub request.
  const candidateInfo = candidateAssetsDir ? readCandidateInfo(candidateAssetsDir) : null;
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
  if (
    !v2BetaOptIn &&
    (old?.channels?.beta?.schema_version === 2 ||
      /^v2\./.test(old?.channels?.beta?.version || ''))
  )
    throw new Error('Existing v2 beta channel requires the v2 beta DRY_RUN opt-in; refusing a 1.x overwrite');
  const catalog = await read('release-catalog.json', {});
  const withdrawn = await read('withdrawn.json', []);
  const previousCatalog = JSON.parse(JSON.stringify(catalog));
  const releases = [];
  for (let page = 1; ; page++) {
    const batch = await api('/releases?per_page=100&page=' + page);
    releases.push(...batch);
    if (batch.length < 100) break;
  }
  if (withdraw) {
    const target = releases.find(
      (r) =>
        r.tag_name === withdraw && !r.draft && r.assets?.some((a) => a.name === 'release-info.json')
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
    // A 1.x maintenance run must keep working while a v2 Beta exists remotely:
    // skip it before downloading release-info or resolving its commit.
    if (isV2Version(tag) && !v2BetaOptIn) continue;
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
    verifyCandidate(release, info, commit.sha, { allowV2Beta: v2BetaOptIn });
    catalog[tag] = { info, digest: asset.digest };
    infos.push(info);
  }
  if (candidateInfo) {
    const tag = candidateInfo.release.version;
    if (infos.some((existing) => existing.release.version === tag))
      throw new Error('Candidate release is already published: ' + tag);
    validateInfo(candidateInfo, tag, { allowV2Beta: v2BetaOptIn });
    infos.push(candidateInfo);
  }
  const { channels, aliases } = resolveAliases(infos, withdrawn, { allowV2Beta: v2BetaOptIn });
  if (!infos.length && !withdraw) throw new Error('At least one verified release is required');
  const v2Tags = new Set(
    infos
      .filter((info) => parseVersion(info.release.version).core[0] === '2')
      .map((info) => info.release.version)
  );
  const index = {
    schema_version: 1,
    revision: (old?.revision || 0) + 1,
    generated_at: new Date().toISOString(),
    channels,
  };
  validateIndex(index, { allowV2Beta: v2BetaOptIn });
  if (v2Tags.size > 0) {
    // Scope 5/9: a v2 Beta rehearsal may only move `channels.beta`.
    if (!old) throw new Error('v2 Beta publication requires an existing 1.x update index');
    if (withdraw || restore) throw new Error('v2 Beta publication must not change withdrawals');
    if (JSON.stringify(channels.stable) !== JSON.stringify(old.channels?.stable ?? null))
      throw new Error('v2 Beta must not move the stable channel');
    if (JSON.stringify(channels.rc) !== JSON.stringify(old.channels?.rc ?? null))
      throw new Error('v2 Beta must not move the rc channel');
    if (!channels.beta || !v2Tags.has(channels.beta.version) || channels.beta.schema_version !== 2)
      throw new Error('v2 Beta must be the schema_version 2 beta channel target');
    for (const [alias, tag] of Object.entries(aliases))
      if (parseVersion(tag).core[0] === '2')
        throw new Error(`v2 Beta must not move the ${alias} image alias`);
    for (const [tag, entry] of Object.entries(previousCatalog))
      if (JSON.stringify(catalog[tag]) !== JSON.stringify(entry))
        throw new Error('v2 Beta must not rewrite 1.x release catalog entries: ' + tag);
  }
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
      // v2 Beta images are not pushed before the rehearsal; their digests are
      // pinned by the verified pairing record in the schema 2 release info.
      if (v2Tags.has(tag)) continue;
      const digest = verifyImage(inspect(image + ':' + tag), candidates.get(tag));
      if (digests.has(tag) && digests.get(tag) !== digest)
        throw new Error('Registry digest mismatch: ' + tag);
      digests.set(tag, digest);
    }
  if (!dryRunValue)
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
  if (channels.stable && !dryRunValue) {
    const stable = releases.find((r) => r.tag_name === channels.stable.version);
    await api('/releases/' + stable.id, 'PATCH', { make_latest: 'true' });
    const latest = await api('/releases/latest');
    if (latest.id !== stable.id || latest.tag_name !== channels.stable.version)
      throw new Error('GitHub Latest did not advance to the stable target');
  }
  // Dry-run scope: zero GitHub writes (release, tree, commit, refs, Latest) and
  // zero registry writes (alias creates); only GETs and local Docker inspects ran.
  if (dryRunValue) return index;
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

const parseCliOptions = (argumentsList) => {
  const options = {};
  for (let index = 0; index < argumentsList.length; index += 1) {
    const argument = argumentsList[index];
    if (argument === '--dry-run') options.dryRun = true;
    else if (argument === '--allow-v2-beta-dry-run') options.allowV2BetaDryRun = true;
    else if (argument === '--candidate-assets-dir') {
      const value = argumentsList[++index];
      if (!value) throw new Error('--candidate-assets-dir requires a path');
      options.candidateAssetsDir = value;
    } else if (argument === '--candidate') {
      const value = argumentsList[++index];
      if (!value) throw new Error('--candidate requires a path');
      options.candidateInfoPath = value;
    } else throw new Error(`Unknown argument: ${argument}`);
  }
  return options;
};

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  try {
    const options = parseCliOptions(process.argv.slice(2));
    const dryRun = options.dryRun === true || process.env.DRY_RUN === 'true';
    publishUpdateIndex({
      dryRun,
      // Raw intent: the function itself combines it with dryRun and fails
      // closed before IO when only one of the two is present.
      allowV2Beta: options.allowV2BetaDryRun === true,
      candidateAssetsDir: options.candidateAssetsDir,
      candidateInfoPath: options.candidateInfoPath,
    })
      .then((index) => process.stdout.write(JSON.stringify(index, null, 2) + '\n'))
      .catch((err) => {
        console.error(err.message);
        process.exitCode = 1;
      });
  } catch (err) {
    console.error(err instanceof Error ? err.message : String(err));
    process.exitCode = 1;
  }
}
