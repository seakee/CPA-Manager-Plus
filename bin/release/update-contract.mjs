export const repository = 'https://github.com/seakee/CPA-Manager-Plus';
const pattern =
  /^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$/;
export function parseVersion(tag) {
  const m = typeof tag === 'string' && pattern.exec(tag);
  if (!m) throw new Error('Invalid semantic version');
  const pre = m[4]?.split('.') || [];
  if (pre.some((p) => /^0\d+$/.test(p))) throw new Error('Leading zero in prerelease');
  const stage = !pre.length
    ? 'stable'
    : pre.length === 2 && /^(beta|rc)$/.test(pre[0]) && /^\d+$/.test(pre[1])
      ? pre[0]
      : '';
  return { core: m.slice(1, 4), pre, stage };
}
const textCompare = (a, b) => (a < b ? -1 : a > b ? 1 : 0);
const numberCompare = (a, b) =>
  a.length === b.length ? textCompare(a, b) : Math.sign(a.length - b.length);
export function compareVersions(a, b) {
  a = parseVersion(a);
  b = parseVersion(b);
  for (let i = 0; i < 3; i++) {
    const c = numberCompare(a.core[i], b.core[i]);
    if (c) return c;
  }
  if (!a.pre.length || !b.pre.length)
    return !a.pre.length && !b.pre.length ? 0 : !a.pre.length ? 1 : -1;
  for (let i = 0; i < Math.min(a.pre.length, b.pre.length); i++) {
    const x = a.pre[i],
      y = b.pre[i],
      xn = /^\d+$/.test(x),
      yn = /^\d+$/.test(y);
    const c = xn && yn ? numberCompare(x, y) : xn !== yn ? (xn ? -1 : 1) : textCompare(x, y);
    if (c) return c;
  }
  return Math.sign(a.pre.length - b.pre.length);
}
export const stageAllowed = (channel, stage) =>
  stage === 'stable' ||
  (channel === 'rc' && stage === 'rc') ||
  (channel === 'beta' && ['beta', 'rc'].includes(stage));
export const nativeAssets = (tag) =>
  ['darwin', 'linux', 'windows']
    .flatMap((os) =>
      ['amd64', 'arm64'].map(
        (arch) => `cpa-manager-plus_${tag}_${os}_${arch}${os === 'windows' ? '.zip' : '.tar.gz'}`
      )
    )
    .sort();

// v2 Embedded Beta contract (Scope 5/9 contract half). Values are fixed by the
// maintainer decisions for the first Beta and by the merged linux/amd64 Native
// package (#1129); none of them may be inferred from a release tag.
export const v2EmbeddedComposeFilename = 'docker-compose.v2-embedded.yml';
export const v2PairingFilename = 'embedded-release-pairing.json';
export const v2MinimumDirectUpgradeVersion = 'v1.14.10';
export const v2MinimumCPAVersion = 'v8.0.55';
export const v2Platforms = ['linux/amd64'];
export const v2SupportedModes = ['docker', 'native', 'panel-only'];
export const v2UpdateIndexFilename = 'update-index-v2.json';
export const v2ImageRepositoryName = 'cpa-manager-plus';
export const v2ImageRoles = Object.freeze(['slim', 'full']);
export const v2ImageRegistries = ['seakee', 'ghcr.io/seakee'];
export function v2DockerVersionTag(tag) {
  const v = parseVersion(tag);
  if (v.core[0] !== '2' || !/^v/.test(tag) || tag.includes('+'))
    throw new Error(`Invalid v2 release tag: ${tag}`);
  return tag.slice(1);
}
export function v2DockerRoleTag(tag, role) {
  const versionTag = v2DockerVersionTag(tag);
  if (role === 'slim') return versionTag;
  if (role === 'full') return `${versionTag}-full`;
  throw new Error(`Invalid v2 image role: ${role}`);
}
export function v2ReleaseInfoPath(tag) {
  v2DockerVersionTag(tag);
  return `releases/download/${tag}/release-info.json`;
}
// #1129 deliberately renames the v2 Native package so it can never collide with
// a 1.x schema-1 asset; the 1.x `cpa-manager-plus_v*` namespace is forbidden.
export const v2NativeArchiveName = (tag) => `cpa-manager-plus-native-v2_${tag}_linux_amd64.tar.gz`;
export const v2SlotArchiveName = (tag, arch = 'amd64') => {
  if (!['amd64', 'arm64'].includes(arch)) {
    throw new Error(`Unsupported v2 slot architecture: ${arch}`);
  }
  return `cpamp-slot_${tag}_linux_${arch}.tar.gz`;
};
export const v2SlotRequiresAsset = (tag) =>
  isV2Version(tag) && compareVersions(tag, 'v2.0.0-beta.2') >= 0;

const sha256Pattern = /^sha256:[0-9a-f]{64}$/;
const sourceCommitPattern = /^[0-9a-f]{40}$/;
const v2NativePattern =
  /^cpa-manager-plus-native-v2_(.+)_(darwin|linux|windows)_(amd64|arm64)\.(?:tar\.gz|zip)$/;
const v2SlotPattern = /^cpamp-slot_(.+)_linux_(amd64|arm64)\.tar\.gz$/;
const legacyNativePattern =
  /^cpa-manager-plus_v.+_(darwin|linux|windows)_(amd64|arm64)\.(?:tar\.gz|zip)$/;
const assertSha256 = (value, label) => {
  if (typeof value !== 'string' || !sha256Pattern.test(value)) throw new Error(`Invalid ${label}`);
};

export const isV2Version = (tag) => parseVersion(tag).core[0] === '2';

/**
 * Exported entry points receive caller/API input, so a truthy string such as
 * `'false'` must never authorize anything. `undefined` means "not provided".
 */
export const assertBooleanOption = (options, name, fallback = false) => {
  const value = options?.[name];
  if (value === undefined) return fallback;
  if (typeof value !== 'boolean') throw new TypeError(`${name} must be a boolean`);
  return value;
};

// The release workflow validators still require a dry-run for v2 rehearsal.
export function assertV2Rehearsal(raw, options = {}) {
  const allowV2Beta = assertBooleanOption(options, 'allowV2Beta');
  const dryRun = assertBooleanOption(options, 'dryRun');
  if (!isV2Version(raw)) return;
  if (!(allowV2Beta === true && dryRun === true))
    throw new Error(
      'Unqualified v2 Embedded release: v2 rehearsal is fail-closed (Gate M3); the v2 beta DRY_RUN opt-in requires dryRun === true and allowV2Beta === true'
    );
}

/**
 * Version gate shared by every producer/verifier.
 *
 * 1.x is unchanged. Any v2 tag stays fail-closed (Gate M3) unless the caller
 * explicitly opts in AND the tag is a beta prerelease: `v2.x.y` stable can never
 * pass, with or without the opt-in, so GitHub `releases/latest` stays 1.x.
 */
export function assertSupportedVersion(raw, options = {}) {
  const allowV2Beta = assertBooleanOption(options, 'allowV2Beta');
  const v = parseVersion(raw);
  if (v.core[0] === '1') return v;
  if (v.core[0] === '2' && v.stage === 'beta' && allowV2Beta) return v;
  throw new Error(
    `Unqualified v2 Embedded release: v${v.core[0]}.x release contract is fail-closed (Gate M3) for ${raw}` +
      (v.core[0] === '2' && v.stage === 'beta'
        ? '; explicit v2 beta opt-in is required'
        : '; only v2.x.y-beta.N is enabled')
  );
}

export const expectedV2AssetNames = (tag) => {
  assertSupportedVersion(tag, { allowV2Beta: true });
  if (!isV2Version(tag)) throw new Error('v2 asset set requires a v2.x.y-beta.N release');
  const assets = [
    'checksums.txt',
    'release-info.json',
    v2EmbeddedComposeFilename,
    v2PairingFilename,
    'management.html',
    v2NativeArchiveName(tag),
  ];
  if (v2SlotRequiresAsset(tag)) {
    assets.push(v2SlotArchiveName(tag, 'amd64'));
  }
  return assets.sort();
};

/**
 * Assert the exact v2 release asset set and the Lite/1.x continuity invariants:
 * no `stable-version.txt`, no 1.x-style `cpa-manager-plus_v*` package, and no
 * v2 Native package for a platform other than linux/amd64.
 */
export function assertV2AssetSet(names, tag) {
  if (!Array.isArray(names) || names.some((name) => typeof name !== 'string'))
    throw new Error('Invalid v2 release asset list');
  const list = [...names].sort();
  if (new Set(list).size !== list.length) throw new Error('Duplicate v2 release asset');
  for (const name of list) {
    if (name === 'stable-version.txt')
      throw new Error('v2 release must not publish stable-version.txt');
    if (legacyNativePattern.test(name))
      throw new Error(`1.x native asset is forbidden in a v2 release: ${name}`);
    const native = v2NativePattern.exec(name);
    if (native) {
      if (native[1] !== tag) throw new Error(`v2 native asset version mismatch: ${name}`);
      if (native[2] !== 'linux' || native[3] !== 'amd64')
        throw new Error(`Unsupported v2 native platform: ${name}`);
    }
    const slot = v2SlotPattern.exec(name);
    if (slot) {
      if (slot[1] !== tag) throw new Error(`v2 slot asset version mismatch: ${name}`);
      if (slot[2] !== 'amd64')
        throw new Error(`Unsupported v2 slot platform: ${name}`);
    }
  }
  const expected = expectedV2AssetNames(tag);
  const matchesExact = (candidate) =>
    list.length === candidate.length && list.every((name, index) => name === candidate[index]);
  if (matchesExact(expected)) return list;
  if (!v2SlotRequiresAsset(tag)) {
    const withOptionalSlot = [...expected, v2SlotArchiveName(tag, 'amd64')].sort();
    if (matchesExact(withOptionalSlot)) return list;
  }
  throw new Error(
    `v2 release asset set mismatch: expected ${expected.join(', ')}, received ${list.join(', ')}`
  );
}

/**
 * Validate `embedded-release-pairing.json` (Scope 4 producer, PR-1) as consumed
 * by release-info generation. Notice/provenance material travels inside the
 * images and the Native archive, so the pairing record must bind each image's
 * release-provenance digest rather than publishing separate notice assets.
 */
export function assertV2UpgradeGuideUrl(raw) {
  if (typeof raw !== 'string' || !raw || raw.trim() !== raw || /[\\#?\r\n\t ]/.test(raw))
    throw new Error('Invalid v2 upgrade guide');
  if (/%2e|%2f/i.test(raw)) throw new Error('Invalid v2 upgrade guide');
  let guide;
  try {
    guide = new URL(raw);
  } catch {
    throw new Error('Invalid v2 upgrade guide');
  }
  if (
    guide.protocol !== 'https:' ||
    guide.username ||
    guide.password ||
    guide.search ||
    guide.hash
  )
    throw new Error('Invalid v2 upgrade guide');
  const rawPath = raw.slice(guide.origin.length);
  if (!rawPath.startsWith('/') || rawPath.split('/').some((seg) => seg === '.' || seg === '..'))
    throw new Error('Invalid v2 upgrade guide');
  const cleanPath = guide.pathname.replace(/\/+/g, '/').replace(/\/$/, '') || '/';
  if (guide.origin === 'https://github.com') {
    if (
      !cleanPath.startsWith('/seakee/CPA-Manager-Plus/') ||
      cleanPath.length <= '/seakee/CPA-Manager-Plus/'.length
    )
      throw new Error('Invalid v2 upgrade guide');
    return raw;
  }
  if (guide.origin === 'https://cpamp-docs.seakee.dev') {
    if (!cleanPath.startsWith('/') || cleanPath === '/')
      throw new Error('Invalid v2 upgrade guide');
    return raw;
  }
  throw new Error('Invalid v2 upgrade guide');
}

export function validateV2Pairing(pairing, { tag, sourceCommit }) {
  if (!pairing || typeof pairing !== 'object' || pairing.schema_version !== 1)
    throw new Error('Invalid v2 pairing record');
  const expectedVersionTag = v2DockerVersionTag(tag);
  if (pairing.version !== tag && pairing.version !== expectedVersionTag)
    throw new Error('v2 pairing version mismatch');
  if (
    !sourceCommitPattern.test(pairing.source_commit || '') ||
    pairing.source_commit !== sourceCommit
  )
    throw new Error('v2 pairing source commit mismatch');
  if (pairing.platform !== 'linux/amd64')
    throw new Error('v2 pairing platform must be linux/amd64');
  if (pairing.cpa?.version !== v2MinimumCPAVersion.replace(/^v/, ''))
    throw new Error(`v2 pairing must record CPA ${v2MinimumCPAVersion}`);
  assertSha256(pairing.cpa?.artifact_id, 'v2 pairing CPA artifact digest');
  assertSha256(pairing.fullweb?.manifest_sha256, 'v2 pairing Full Web manifest digest');
  assertSha256(pairing.api_contract?.sha256, 'v2 pairing API contract digest');
  if (pairing.compose?.filename !== v2EmbeddedComposeFilename)
    throw new Error('v2 pairing compose filename mismatch');
  assertSha256(pairing.compose?.sha256, 'v2 pairing compose digest');
  if (
    pairing.compose?.size !== undefined &&
    (!Number.isSafeInteger(pairing.compose.size) || pairing.compose.size <= 0)
  )
    throw new Error('Invalid v2 pairing compose size');
  if (!pairing.images || typeof pairing.images !== 'object' || Array.isArray(pairing.images))
    throw new Error('v2 pairing is missing image records');
  const allowedRepositories = v2ImageRegistries.map(
    (registry) => `${registry}/${v2ImageRepositoryName}`
  );
  for (const role of v2ImageRoles) {
    const image = pairing.images[role];
    if (!image || typeof image !== 'object')
      throw new Error(`v2 pairing is missing the ${role} image`);
    const repositoryValue = String(image.repository ?? '');
    if (!allowedRepositories.includes(repositoryValue))
      throw new Error(`v2 pairing has an unexpected ${role} repository`);
    const expectedRoleTag = v2DockerRoleTag(tag, role);
    if (image.tag !== expectedRoleTag)
      throw new Error(`v2 pairing ${role} tag mismatch`);
    assertSha256(image.manifest_digest, `v2 pairing ${role} manifest digest`);
    if (image.image_ref !== `${repositoryValue}:${expectedRoleTag}@${image.manifest_digest}`)
      throw new Error(`v2 pairing ${role} image_ref mismatch`);
    assertSha256(image.image_config_digest, `v2 pairing ${role} image config digest`);
    if (image.binary_sha256 !== undefined)
      assertSha256(image.binary_sha256, `v2 pairing ${role} binary digest`);
    assertSha256(image.release_provenance_sha256, `v2 pairing ${role} release provenance digest`);
  }
  if (Object.keys(pairing.images).some((role) => !v2ImageRoles.includes(role)))
    throw new Error('v2 pairing has unexpected image roles');
  const roleDigests = v2ImageRoles.map((role) => pairing.images[role].manifest_digest);
  if (new Set(roleDigests).size !== roleDigests.length)
    throw new Error('v2 role manifest digests must be distinct');
  return pairing;
}
export function validateV2SlotDistribution(slot, tag) {
  if (slot === undefined) {
    if (v2SlotRequiresAsset(tag)) {
      throw new Error(`v2 slot distribution is required for ${tag}`);
    }
    return undefined;
  }
  if (!slot || typeof slot !== 'object' || Array.isArray(slot)) {
    throw new Error('Invalid v2 slot distribution');
  }
  if (!Array.isArray(slot.assets) || slot.assets.length === 0) {
    throw new Error('v2 slot distribution requires a non-empty assets array');
  }
  const seenNames = new Set();
  const seenPlatforms = new Set();
  for (const asset of slot.assets) {
    if (!asset || typeof asset !== 'object' || Array.isArray(asset)) {
      throw new Error('Invalid v2 slot asset entry');
    }
    const match = typeof asset.name === 'string' ? v2SlotPattern.exec(asset.name) : null;
    if (!match) {
      throw new Error(`Invalid v2 slot asset name: ${String(asset.name)}`);
    }
    if (match[1] !== tag) {
      throw new Error(`v2 slot asset version mismatch: ${asset.name}`);
    }
    const expectedPlatform = `linux/${match[2]}`;
    if (asset.platform !== expectedPlatform) {
      throw new Error(
        `v2 slot asset platform mismatch for ${asset.name}: expected ${expectedPlatform}`
      );
    }
    if (seenNames.has(asset.name) || seenPlatforms.has(asset.platform)) {
      throw new Error(`Duplicate v2 slot asset or platform: ${asset.name}`);
    }
    seenNames.add(asset.name);
    seenPlatforms.add(asset.platform);
    assertSha256(asset.sha256, 'v2 slot asset digest');
    if (!Number.isSafeInteger(asset.size) || asset.size <= 0) {
      throw new Error(`Invalid v2 slot asset size for ${asset.name}`);
    }
    if (!Number.isSafeInteger(asset.min_base_epoch) || asset.min_base_epoch < 1) {
      throw new Error(`Invalid v2 slot asset min_base_epoch for ${asset.name}`);
    }
    if (!Number.isSafeInteger(asset.schema_generation) || asset.schema_generation < 1) {
      throw new Error(`Invalid v2 slot asset schema_generation for ${asset.name}`);
    }
  }
  return slot;
}

export function validateV2Info(info, tag) {
  const v = assertSupportedVersion(tag, { allowV2Beta: true });
  if (v.core[0] !== '2' || v.stage !== 'beta' || !/^v/.test(tag) || tag.includes('+'))
    throw new Error('Invalid v2 release identity');
  if (!info || typeof info !== 'object') throw new Error('Invalid v2 release info');
  if (info.schema_version !== 2) throw new Error('v2 release info requires schema_version 2');
  if (
    info.release?.version !== tag ||
    info.release?.stage !== 'beta' ||
    !sourceCommitPattern.test(info.release?.source_commit || '')
  )
    throw new Error('Invalid v2 release identity');
  for (const lang of ['zh', 'en']) {
    if (
      typeof info.content?.summary?.[lang] !== 'string' ||
      !info.content.summary[lang].trim() ||
      Buffer.byteLength(info.content.summary[lang], 'utf8') > 4096 ||
      info.content?.notes?.[lang] !==
        `${repository}/blob/${tag}/docs/release-notes/${tag}-${lang}.md`
    )
      throw new Error('Invalid v2 release content');
  }
  if (
    typeof info.update?.breaking !== 'boolean' ||
    typeof info.update?.migration_required !== 'boolean'
  )
    throw new Error('Explicit update flags required');
  if (
    !('minimum_direct_upgrade_version' in (info.update || {})) ||
    info.update.minimum_direct_upgrade_version === undefined
  )
    throw new Error('v2 release must declare minimum_direct_upgrade_version');
  if (info.update.minimum_direct_upgrade_version !== null) {
    if (typeof info.update.minimum_direct_upgrade_version !== 'string')
      throw new Error('Invalid v2 minimum_direct_upgrade_version');
    parseVersion(info.update.minimum_direct_upgrade_version);
  }
  assertV2UpgradeGuideUrl(info.update.upgrade_guide_url);
  if (info.compatibility?.minimum_cpa_version !== v2MinimumCPAVersion)
    throw new Error(`v2 release must require CPA ${v2MinimumCPAVersion}`);
  if (JSON.stringify(info.supported_modes) !== JSON.stringify(v2SupportedModes))
    throw new Error('Unsupported v2 mode set');
  if (
    JSON.stringify(info.supported_platforms) !==
    JSON.stringify({ docker: v2Platforms, native: v2Platforms })
  )
    throw new Error('Unsupported v2 platform set');

  const expectedVersionTag = v2DockerVersionTag(tag);
  if (info.distribution?.docker?.version_tag !== expectedVersionTag)
    throw new Error('Invalid v2 docker distribution');
  const images = info.distribution?.docker?.images;
  const expectedImageCount = v2ImageRoles.length * v2ImageRegistries.length;
  if (!Array.isArray(images) || images.length !== expectedImageCount)
    throw new Error('Invalid v2 image set: slim and full roles for Docker Hub and GHCR are required');
  const expectedNames = v2ImageRegistries.map(
    (registry) => `${registry}/${v2ImageRepositoryName}`
  );
  const seen = new Set();
  const roleDigests = new Map();
  for (const image of images) {
    if (!v2ImageRoles.includes(image?.role)) throw new Error('Invalid v2 image role');
    if (!expectedNames.includes(image.name))
      throw new Error(`Invalid v2 image reference for the ${image.role} role`);
    if (image.tag !== v2DockerRoleTag(tag, image.role))
      throw new Error(`Invalid v2 image tag for the ${image.role} role`);
    assertSha256(image.digest, 'v2 image digest');
    if (JSON.stringify(image.platforms) !== JSON.stringify(v2Platforms))
      throw new Error('Invalid v2 image platforms');
    const entryKey = `${image.role}@${image.name}`;
    if (seen.has(entryKey)) throw new Error('Duplicate v2 image reference');
    seen.add(entryKey);
    const digests = roleDigests.get(image.role) ?? new Set();
    digests.add(image.digest);
    roleDigests.set(image.role, digests);
  }
  if (roleDigests.size !== v2ImageRoles.length)
    throw new Error('v2 image set must include both slim and full roles');
  for (const [role, digests] of roleDigests)
    if (digests.size !== 1) throw new Error(`v2 registry digest mismatch: ${role}`);
  const distinctRoleDigests = new Set([...roleDigests.values()].map((digests) => [...digests][0]));
  if (distinctRoleDigests.size !== roleDigests.size)
    throw new Error('v2 role manifest digests must be distinct');
  if (
    JSON.stringify(info.distribution?.native?.assets) !== JSON.stringify([v2NativeArchiveName(tag)])
  )
    throw new Error('Invalid v2 native distribution');
  if (info.distribution?.compose?.filename !== v2EmbeddedComposeFilename)
    throw new Error('Invalid v2 compose distribution');
  assertSha256(info.distribution.compose?.sha256, 'v2 compose digest');
  validateV2SlotDistribution(info.distribution?.slot, tag);
  const pairing = info.pairing;
  if (!pairing || pairing.filename !== v2PairingFilename)
    throw new Error('Invalid v2 pairing reference');
  if (pairing.source_commit !== info.release.source_commit)
    throw new Error('v2 pairing source commit mismatch');
  assertSha256(pairing.fullweb_manifest_sha256, 'v2 pairing Full Web manifest digest');
  assertSha256(pairing.api_contract_sha256, 'v2 pairing API contract digest');
  assertSha256(pairing.compose_sha256, 'v2 pairing compose digest');
  if (pairing.compose_sha256 !== info.distribution.compose.sha256)
    throw new Error('v2 pairing compose digest mismatch');
  return info;
}

/**
 * Bind the distributed bytes to the release metadata: the compose file digest,
 * the pairing record, and every image digest must agree with `release-info.json`.
 * Callers pass what they actually have on disk (or in the published asset list).
 */
export function assertV2AssetBindings(
  info,
  { composeSha256, composeSize, pairing, slotAssets }
) {
  if (typeof composeSha256 !== 'string' || !sha256Pattern.test(composeSha256))
    throw new Error('Invalid v2 compose asset digest');
  if (composeSha256 !== info.distribution.compose.sha256)
    throw new Error('v2 compose asset digest does not match release info');
  const validated = validateV2Pairing(pairing, {
    tag: info.release.version,
    sourceCommit: info.release.source_commit,
  });
  if (
    composeSize !== undefined &&
    validated.compose.size !== undefined &&
    composeSize !== validated.compose.size
  )
    throw new Error('v2 compose asset size does not match pairing record');
  if (validated.compose.sha256 !== info.pairing.compose_sha256)
    throw new Error('v2 pairing compose digest does not match release info');
  if (validated.fullweb.manifest_sha256 !== info.pairing.fullweb_manifest_sha256)
    throw new Error('v2 pairing Full Web digest does not match release info');
  if (validated.api_contract.sha256 !== info.pairing.api_contract_sha256)
    throw new Error('v2 pairing API contract digest does not match release info');
  for (const role of v2ImageRoles) {
    const digests = new Set(
      info.distribution.docker.images
        .filter((image) => image.role === role)
        .map((image) => image.digest)
    );
    if (digests.size !== 1 || !digests.has(validated.images[role].manifest_digest))
      throw new Error(`v2 pairing ${role} digest does not match release info`);
  }
  if (slotAssets !== undefined) {
    if (!Array.isArray(slotAssets)) {
      throw new Error('Invalid v2 slotAssets binding list');
    }
    const slotMetadata = info.distribution?.slot;
    if (slotAssets.length > 0 && !slotMetadata) {
      throw new Error('v2 slot asset is present on disk but release info lacks distribution.slot');
    }
    if (slotMetadata) {
      const byName = new Map(slotMetadata.assets.map((asset) => [asset.name, asset]));
      const diskNames = new Set();
      for (const disk of slotAssets) {
        const diskDigest = disk.sha256 ?? disk.digest;
        const meta = byName.get(disk.name);
        if (!meta) {
          throw new Error(`v2 slot asset missing from release info distribution.slot: ${disk.name}`);
        }
        if (diskDigest !== meta.sha256) {
          throw new Error(`v2 slot asset digest does not match release info: ${disk.name}`);
        }
        if (disk.size !== meta.size) {
          throw new Error(`v2 slot asset size does not match release info: ${disk.name}`);
        }
        diskNames.add(disk.name);
      }
      for (const meta of slotMetadata.assets) {
        if (!diskNames.has(meta.name)) {
          throw new Error(`v2 release info distribution.slot asset missing on disk: ${meta.name}`);
        }
      }
    }
  }
  return info;
}

export function validateInfo(info, tag, options = {}) {
  const v = assertSupportedVersion(tag, options);
  if (v.core[0] === '2') return validateV2Info(info, tag);
  if (
    !/^v/.test(tag) ||
    tag.includes('+') ||
    !v.stage ||
    info?.schema_version !== 1 ||
    info.release?.version !== tag ||
    info.release.stage !== v.stage ||
    !sourceCommitPattern.test(info.release.source_commit)
  )
    throw new Error('Invalid release identity');
  for (const lang of ['zh', 'en']) {
    if (
      typeof info.content?.summary?.[lang] !== 'string' ||
      !info.content.summary[lang].trim() ||
      Buffer.byteLength(info.content.summary[lang], 'utf8') > 4096 ||
      info.content?.notes?.[lang] !==
        `${repository}/blob/${tag}/docs/release-notes/${tag}-${lang}.md`
    )
      throw new Error('Invalid release content');
  }
  if (
    typeof info.update?.breaking !== 'boolean' ||
    typeof info.update?.migration_required !== 'boolean'
  )
    throw new Error('Explicit update flags required');
  if (info.update.minimum_direct_upgrade_version !== null)
    parseVersion(info.update.minimum_direct_upgrade_version);
  const guide = new URL(info.update.upgrade_guide_url);
  if (
    guide.origin !== 'https://github.com' ||
    guide.username ||
    guide.password ||
    !guide.pathname.startsWith('/seakee/CPA-Manager-Plus/')
  )
    throw new Error('Invalid upgrade guide');
  if (
    info.distribution?.docker?.image !== 'seakee/cpa-manager-plus' ||
    info.distribution.docker.version_tag !== tag ||
    JSON.stringify([...(info.distribution?.native?.assets || [])].sort()) !==
      JSON.stringify(nativeAssets(tag))
  )
    throw new Error('Invalid distribution');
  if (info.compatibility?.minimum_cpa_version !== null)
    parseVersion(info.compatibility?.minimum_cpa_version);
  return info;
}

/**
 * Validate the 1.x update index (`update-index.json`, `schema_version: 1`).
 * Only `major == 1` targets are allowed here so 1.x consumers (including 1.x
 * beta users) never encounter v2 targets or schema_version 2 payloads.
 */
export function validateIndex(index, options = {}) {
  assertBooleanOption(options, 'allowV2Beta');
  if (
    !index ||
    typeof index !== 'object' ||
    index.schema_version !== 1 ||
    !Number.isSafeInteger(index.revision) ||
    index.revision < 1 ||
    typeof index.channels !== 'object' ||
    index.channels === null
  )
    throw new Error('Invalid update index');
  for (const channel of ['stable', 'rc', 'beta']) {
    if (!(channel in index.channels)) throw new Error('Missing update index channel');
    const target = index.channels[channel];
    if (target === null) continue;
    if (!target || typeof target !== 'object' || Array.isArray(target))
      throw new Error('Invalid update index target');
    const v = parseVersion(target.version);
    if (
      !/^v/.test(target.version) ||
      target.version.includes('+') ||
      !stageAllowed(channel, v.stage)
    )
      throw new Error('Invalid update index target');
    if (v.core[0] !== '1')
      throw new Error('update-index.json only accepts 1.x versions; use update-index-v2.json for v2');
    if (target.schema_version !== undefined || Object.keys(target).some((k) => k !== 'version'))
      throw new Error('Invalid update index target schema');
  }
  if (Object.keys(index.channels).some((channel) => !['stable', 'rc', 'beta'].includes(channel)))
    throw new Error('Unexpected update index channel');
  return index;
}

/**
 * Validate the v2 update index (`update-index-v2.json`, `schema_version: 2`).
 */
export function validateV2Index(index, options = {}) {
  const allowV2Beta = assertBooleanOption(options, 'allowV2Beta');
  const requireReleaseInfoSha256 = assertBooleanOption(options, 'requireReleaseInfoSha256');
  if (
    !index ||
    typeof index !== 'object' ||
    Array.isArray(index) ||
    index.schema_version !== 2 ||
    !Number.isSafeInteger(index.revision) ||
    index.revision < 1 ||
    typeof index.generated_at !== 'string' ||
    !index.generated_at.trim() ||
    Number.isNaN(Date.parse(index.generated_at)) ||
    typeof index.channels !== 'object' ||
    index.channels === null ||
    Array.isArray(index.channels)
  )
    throw new Error('Invalid v2 update index');
  const allowedTargetKeys = new Set([
    'version',
    'schema_version',
    'release_info_path',
    'release_info_sha256',
  ]);
  for (const channel of ['stable', 'rc', 'beta']) {
    if (!(channel in index.channels)) throw new Error('Missing v2 update index channel');
    const target = index.channels[channel];
    if (target === null) continue;
    if (!target || typeof target !== 'object' || Array.isArray(target))
      throw new Error('Invalid v2 update index target');
    for (const key of Object.keys(target))
      if (!allowedTargetKeys.has(key)) throw new Error('Invalid v2 update index target schema');
    const v = parseVersion(target.version);
    if (
      v.core[0] !== '2' ||
      !/^v/.test(target.version) ||
      target.version.includes('+') ||
      !stageAllowed(channel, v.stage)
    )
      throw new Error('Invalid v2 update index target');
    if (channel !== 'beta') throw new Error('v2 release may only target the beta channel');
    assertSupportedVersion(target.version, { allowV2Beta: true });
    if (v.stage !== 'beta') throw new Error('v2 beta channel target must be a beta prerelease');
    if (target.schema_version !== 2)
      throw new Error('v2 beta index target requires schema_version 2');
    if (target.release_info_path !== v2ReleaseInfoPath(target.version))
      throw new Error('Invalid v2 index target release_info_path');
    if (requireReleaseInfoSha256 || (target.release_info_sha256 !== undefined && target.release_info_sha256 !== '')) {
      assertSha256(target.release_info_sha256, 'v2 index target release_info_sha256');
    } else if (
      target.release_info_sha256 !== undefined &&
      typeof target.release_info_sha256 !== 'string'
    ) {
      throw new Error('Invalid v2 index target release_info_sha256');
    }
    if (!allowV2Beta)
      throw new Error(
        'Unqualified v2 Embedded release: v2 beta index target requires an explicit dry-run opt-in'
      );
  }
  if (Object.keys(index.channels).some((channel) => !['stable', 'rc', 'beta'].includes(channel)))
    throw new Error('Unexpected v2 update index channel');
  return index;
}

export function resolveChannels(infos, withdrawn = [], options = {}) {
  const channels = { stable: null, rc: null, beta: null };
  for (const info of infos) {
    const tag = info.release.version;
    validateInfo(info, tag, options);
    if (parseVersion(tag).core[0] !== '1') continue;
    if (withdrawn.includes(tag)) continue;
    for (const ch of Object.keys(channels))
      if (
        stageAllowed(ch, info.release.stage) &&
        (!channels[ch] || compareVersions(tag, channels[ch].version) > 0)
      )
        channels[ch] = { version: tag };
  }
  return channels;
}

export function resolveV2Channels(infos, withdrawn = [], options = {}) {
  assertBooleanOption(options, 'allowV2Beta');
  const digests = options.releaseInfoDigests;
  const lookupDigest = (tag) =>
    digests instanceof Map ? digests.get(tag) : digests?.[tag];
  const channels = { stable: null, rc: null, beta: null };
  for (const info of infos) {
    const tag = info.release.version;
    validateInfo(info, tag, options);
    if (parseVersion(tag).core[0] !== '2') continue;
    if (withdrawn.includes(tag)) continue;
    const sha256 = lookupDigest(tag);
    if (sha256 !== undefined && sha256 !== '')
      assertSha256(sha256, 'v2 release_info_sha256');
    for (const ch of Object.keys(channels))
      if (
        stageAllowed(ch, info.release.stage) &&
        (!channels[ch] || compareVersions(tag, channels[ch].version) > 0)
      )
        channels[ch] = {
          version: tag,
          schema_version: 2,
          release_info_path: v2ReleaseInfoPath(tag),
          ...(sha256 ? { release_info_sha256: sha256 } : {}),
        };
  }
  return channels;
}
