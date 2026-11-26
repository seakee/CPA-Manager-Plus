import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
import { existsSync, readFileSync } from 'node:fs';
import { expectedReleaseAssetNames, verifyLocalReleaseAssets } from './verify-published-release.mjs';
import { resolveSlotPublicKeyHex } from './cpamp-slot-sign.mjs';
import {
  validateInfo,
  resolveChannels,
  resolveV2Channels,
  compareVersions,
  parseVersion,
  assertSupportedVersion,
  validateIndex,
  validateV2Index,
  assertV2AssetSet,
  assertBooleanOption,
  isV2Version,
  v2EmbeddedComposeFilename,
  v2UpdateIndexFilename,
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
    const { name, tag: imageTag, role } = options.expectedImage ?? {};
    const record = info.distribution.docker.images.find(
      (entry) => entry.role === role && entry.name === name && entry.tag === imageTag
    );
    const roleLabel = manifest?.labels?.['io.cpamp.candidate.role'];
    // Buildx returns a single image config directly, not a platform-keyed map,
    // for a linux/amd64 manifest. Accept a single-platform index as well.
    const config = image?.config ? image : configs[0];
    const linuxAmd64 = (platform) => platform?.os === 'linux' && platform.architecture === 'amd64';
    const singlePlatform = manifest?.manifests === undefined
      ? linuxAmd64(config)
      : platforms.length === 1 && linuxAmd64(platforms[0].platform);
    const labels = config?.config?.Labels;
    if (
      !record ||
      !/^sha256:[0-9a-f]{64}$/.test(manifest?.digest) ||
      record.digest !== manifest.digest ||
      (roleLabel !== undefined && roleLabel !== role) ||
      (!image?.config && configs.length !== 1) ||
      !singlePlatform ||
      labels?.['org.opencontainers.image.revision'] !== info.release.source_commit ||
      labels?.['org.opencontainers.image.version'] !== tag.slice(1) ||
      labels?.['org.opencontainers.image.source'] !== repository ||
      labels?.['io.cpamp.candidate.role'] !== role
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

// Verify the complete local asset set before considering a v2 candidate.
const readCandidateInfo = (assetsDir, options = {}) => {
  const infoPath = path.join(assetsDir, 'release-info.json');
  if (!existsSync(infoPath))
    throw new Error('Candidate assets directory is missing release-info.json');
  const rawBytes = readFileSync(infoPath);
  const info = JSON.parse(rawBytes.toString('utf8'));
  if (info?.schema_version !== 2)
    throw new Error('Candidate assets are only supported for v2 Beta');
  const tag = info.release?.version;
  const hasSlotPub = options.slotPublicKeyHex !== undefined && options.slotPublicKeyHex !== null;
  const hasPub = options.publicKeyHex !== undefined && options.publicKeyHex !== null;
  if (
    hasSlotPub &&
    hasPub &&
    String(options.slotPublicKeyHex).trim().toLowerCase() !==
      String(options.publicKeyHex).trim().toLowerCase()
  ) {
    throw new Error('slotPublicKeyHex and publicKeyHex disagree');
  }
  const resolvedSlotPublicKeyHex = hasSlotPub ? options.slotPublicKeyHex : options.publicKeyHex;
  verifyLocalReleaseAssets({
    tag,
    assetsDir,
    dryRun: true,
    allowV2Beta: true,
    allowEphemeralSlotKey: options.allowEphemeralSlotKey === true,
    ...(resolvedSlotPublicKeyHex !== undefined
      ? { slotPublicKeyHex: resolvedSlotPublicKeyHex }
      : {}),
  });
  const digest = 'sha256:' + createHash('sha256').update(rawBytes).digest('hex');
  return { info, digest };
};

export async function publishUpdateIndex({
  env = process.env,
  fetchImpl = fetch,
  exec = execFileSync,
  dryRun = env.DRY_RUN === 'true',
  allowV2Beta = false,
  allowV2BetaDryRun = false,
  allowEphemeralSlotKey = false,
  slotPublicKeyHex,
  publicKeyHex,
  candidateAssetsDir,
  candidateInfoPath,
} = {}) {
  // Explicit booleans only: a truthy string such as 'false' never authorizes.
  const dryRunValue = assertBooleanOption({ dryRun }, 'dryRun');
  const allowBeta = assertBooleanOption({ allowV2Beta }, 'allowV2Beta');
  const legacyRehearsal = assertBooleanOption({ allowV2BetaDryRun }, 'allowV2BetaDryRun');
  const allowEphemeralSlotKeyValue = assertBooleanOption(
    { allowEphemeralSlotKey },
    'allowEphemeralSlotKey'
  );
  if (allowEphemeralSlotKeyValue && !dryRunValue)
    throw new Error('--allow-ephemeral-slot-key requires --dry-run');
  if ((candidateAssetsDir !== undefined || candidateInfoPath !== undefined) && !dryRunValue)
    throw new Error('Candidate release inputs require --dry-run');
  if (legacyRehearsal && !dryRunValue)
    throw new Error('--allow-v2-beta-dry-run requires --dry-run');
  const v2BetaOptIn = allowBeta || legacyRehearsal;
  if ((candidateAssetsDir !== undefined || candidateInfoPath !== undefined) && !v2BetaOptIn)
    throw new Error('Candidate release requires --allow-v2-beta or --allow-v2-beta-dry-run');
  if (candidateAssetsDir || candidateInfoPath) {
    candidateAssetsDir ??= path.dirname(candidateInfoPath);
    if (
      candidateInfoPath &&
      path.resolve(candidateInfoPath) !== path.resolve(candidateAssetsDir, 'release-info.json')
    )
      throw new Error('--candidate must match --candidate-assets-dir/release-info.json');
  }
  const withdraw = env.WITHDRAW_RELEASE || '',
    restore = env.RESTORE_RELEASE || '';
  if (withdraw && restore) throw new Error('Choose withdraw or restore');
  if (v2BetaOptIn && (withdraw || restore))
    throw new Error('v2 Beta publication must not change withdrawals');
  for (const tag of [withdraw, restore].filter(Boolean)) {
    parseVersion(tag);
    if (isV2Version(tag)) throw new Error('1.x maintenance cannot change v2 withdrawals');
  }
  // Local files first: a tampered candidate must fail before any GitHub request.
  const hasSlotPub = slotPublicKeyHex !== undefined && slotPublicKeyHex !== null;
  const hasPub = publicKeyHex !== undefined && publicKeyHex !== null;
  if (
    hasSlotPub &&
    hasPub &&
    String(slotPublicKeyHex).trim().toLowerCase() !== String(publicKeyHex).trim().toLowerCase()
  ) {
    throw new Error('slotPublicKeyHex and publicKeyHex disagree');
  }
  const effectiveSlotPublicKeyHex = hasSlotPub ? slotPublicKeyHex : publicKeyHex;
  const candidateRecord = candidateAssetsDir
    ? readCandidateInfo(candidateAssetsDir, {
        allowEphemeralSlotKey: allowEphemeralSlotKeyValue,
        ...(effectiveSlotPublicKeyHex !== undefined
          ? { slotPublicKeyHex: effectiveSlotPublicKeyHex }
          : {}),
      })
    : null;
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
  if (old) {
    if (
      old.schema_version !== 1 ||
      !Number.isSafeInteger(old.revision) ||
      old.revision < 1 ||
      old.revision >= Number.MAX_SAFE_INTEGER
    )
      throw new Error('Invalid existing update index');
    validateIndex(old);
  }
  if (v2BetaOptIn && !old) throw new Error('v2 Beta publication requires an existing 1.x update index');
  const oldV2 = await read(v2UpdateIndexFilename, null);
  if (oldV2) {
    if (
      oldV2.schema_version !== 2 ||
      !Number.isSafeInteger(oldV2.revision) ||
      oldV2.revision < 1 ||
      oldV2.revision >= Number.MAX_SAFE_INTEGER
    )
      throw new Error('Invalid existing v2 update index');
    validateV2Index(oldV2, { allowV2Beta: true });
  }
  const catalog = await read('release-catalog.json', {});
  const withdrawn = await read('withdrawn.json', []);
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
  const releaseInfoDigests = new Map();
  for (const release of releases) {
    const tag = release.tag_name;
    // Historical releases lacking this asset are deliberately excluded.
    const asset = release.assets.find((a) => a.name === 'release-info.json');
    if (release.draft || !asset || withdrawn.includes(tag)) continue;
    // Release lines are maintained independently. Never refresh 1.x catalog
    // entries, targets or aliases as a side effect of a v2 publication.
    if (isV2Version(tag) !== v2BetaOptIn) continue;
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
    releaseInfoDigests.set(tag, asset.digest);
    infos.push(info);
  }
  if (candidateRecord) {
    const { info: candidateInfo, digest: candidateDigest } = candidateRecord;
    const tag = candidateInfo.release.version;
    if (withdrawn.includes(tag)) throw new Error('Candidate release is withdrawn: ' + tag);
    if (releaseInfoDigests.has(tag)) {
      if (releaseInfoDigests.get(tag) !== candidateDigest)
        throw new Error('Candidate release-info differs from the published asset: ' + tag);
    } else {
      validateInfo(candidateInfo, tag, { allowV2Beta: v2BetaOptIn });
      catalog[tag] = { info: candidateInfo, digest: candidateDigest };
      releaseInfoDigests.set(tag, candidateDigest);
      infos.push(candidateInfo);
    }
  }
  const generatedAt = new Date().toISOString();
  const inspect = (reference) =>
    JSON.parse(
      exec('docker', ['buildx', 'imagetools', 'inspect', reference, '--format', '{{json .}}'], {
        encoding: 'utf8',
      })
    );
  const publishFiles = async (files, baseTree) => {
    const tree = await api('/git/trees', 'POST', {
      ...(baseTree ? { base_tree: baseTree } : {}),
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
    // Non-force ref updates reject concurrent writers.
    if (ref) await api('/git/refs/heads/update-channel', 'PATCH', { sha: commit.sha, force: false });
    else await api('/git/refs', 'POST', { ref: 'refs/heads/update-channel', sha: commit.sha });
  };

  if (v2BetaOptIn) {
    const index = {
      schema_version: 2,
      revision: (oldV2?.revision || 0) + 1,
      generated_at: generatedAt,
      channels: resolveV2Channels(infos, withdrawn, { allowV2Beta: true, releaseInfoDigests }),
    };
    validateV2Index(index, { allowV2Beta: true, requireReleaseInfoSha256: true });
    if (!index.channels.beta) throw new Error('At least one verified v2 Beta release is required');
    for (const info of infos) {
      // The legacy workflow rehearses local candidates before their images are pushed.
      if (legacyRehearsal && !allowBeta && candidateRecord?.info === info) continue;
      for (const { name, tag, role } of info.distribution.docker.images) {
        verifyImage(inspect(name + ':' + tag), info, {
          allowV2Beta: true,
          expectedImage: { name, tag, role },
        });
      }
    }
    const nextIndex = JSON.stringify(index.channels) === JSON.stringify(oldV2?.channels)
      ? oldV2
      : index;
    if (!dryRunValue) {
      // Overlay only v2 metadata on the pinned parent tree. Omitting files
      // without base_tree would delete 1.x metadata from the new commit.
      const parent = await api('/git/commits/' + ref.object.sha);
      if (!parent.tree?.sha) throw new Error('Missing update-channel base tree');
      await publishFiles({
        'release-catalog.json': JSON.stringify(catalog, null, 2) + '\n',
        [v2UpdateIndexFilename]: JSON.stringify(nextIndex, null, 2) + '\n',
      }, parent.tree.sha);
    }
    return nextIndex;
  }

  const { channels, aliases } = resolveAliases(infos, withdrawn);
  if (!infos.length && !withdraw) throw new Error('At least one verified release is required');
  const index = {
    schema_version: 1,
    revision: (old?.revision || 0) + 1,
    generated_at: generatedAt,
    channels,
  };
  validateIndex(index);
  const images = ['ghcr.io/seakee/cpa-manager-plus', 'seakee/cpa-manager-plus'];
  // Validate all precise references first, before any alias mutation.
  const targets = new Set([
    ...Object.values(aliases),
    ...Object.values(channels).filter(Boolean).map((target) => target.version),
  ]);
  const candidates = new Map(infos.map((info) => [info.release.version, info]));
  const digests = new Map();
  for (const image of images)
    for (const tag of targets) {
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
  await publishFiles({
    'update-index.json': JSON.stringify(index, null, 2) + '\n',
    ...(oldV2 ? { [v2UpdateIndexFilename]: JSON.stringify(oldV2, null, 2) + '\n' } : {}),
    'release-catalog.json': JSON.stringify(catalog, null, 2) + '\n',
    'withdrawn.json': JSON.stringify(withdrawn, null, 2) + '\n',
    // Minimal projection for POSIX installers without a JSON parser.
    ...(channels.stable ? { 'stable-version.txt': channels.stable.version + '\n' } : {}),
  });
  return index;
}

const parseCliOptions = (argumentsList) => {
  const options = {};
  for (let index = 0; index < argumentsList.length; index += 1) {
    const argument = argumentsList[index];
    if (argument === '--dry-run') options.dryRun = true;
    else if (argument === '--allow-v2-beta') options.allowV2Beta = true;
    else if (argument === '--allow-v2-beta-dry-run') options.allowV2BetaDryRun = true;
    else if (argument === '--allow-ephemeral-slot-key') options.allowEphemeralSlotKey = true;
    else if (argument === '--slot-public-key') {
      const value = argumentsList[++index];
      if (!value || value.startsWith('--')) {
        throw new Error('--slot-public-key requires a 64-hex string or public key file path');
      }
      if (options.slotPublicKeyHex !== undefined) {
        throw new Error('Duplicate argument: --slot-public-key');
      }
      options.slotPublicKeyHex = resolveSlotPublicKeyHex(value);
    } else if (argument === '--candidate-assets-dir') {
      const value = argumentsList[++index];
      if (!value) throw new Error('--candidate-assets-dir requires a path');
      options.candidateAssetsDir = value;
    } else if (argument === '--candidate') {
      const value = argumentsList[++index];
      if (!value) throw new Error('--candidate requires a path');
      options.candidateInfoPath = value;
    } else throw new Error(`Unknown argument: ${argument}`);
  }
  if (
    options.dryRun !== true &&
    (options.candidateAssetsDir !== undefined || options.candidateInfoPath !== undefined)
  )
    throw new Error('Candidate release inputs require --dry-run');
  return options;
};

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  try {
    const options = parseCliOptions(process.argv.slice(2));
    const dryRun = options.dryRun === true || process.env.DRY_RUN === 'true';
    publishUpdateIndex({
      dryRun,
      allowV2Beta: options.allowV2Beta,
      allowV2BetaDryRun: options.allowV2BetaDryRun,
      allowEphemeralSlotKey: options.allowEphemeralSlotKey,
      slotPublicKeyHex: options.slotPublicKeyHex,
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
