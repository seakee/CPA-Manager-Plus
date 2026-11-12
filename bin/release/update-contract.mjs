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
export const v2ImageRepositories = {
  manager: 'cpa-manager-plus',
  ingress: 'cpamp-ingress',
  runtime: 'cpamp-runtime',
};
export const v2ImageRegistries = ['seakee', 'ghcr.io/seakee'];
// #1129 deliberately renames the v2 Native package so it can never collide with
// a 1.x schema-1 asset; the 1.x `cpa-manager-plus_v*` namespace is forbidden.
export const v2NativeArchiveName = (tag) => `cpa-manager-plus-native-v2_${tag}_linux_amd64.tar.gz`;

const sha256Pattern = /^sha256:[0-9a-f]{64}$/;
const sourceCommitPattern = /^[0-9a-f]{40}$/;
const v2NativePattern =
  /^cpa-manager-plus-native-v2_(.+)_(darwin|linux|windows)_(amd64|arm64)\.(?:tar\.gz|zip)$/;
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

/**
 * Gate M3 at the exported boundary, not only in the CLI wrappers: any v2 entry
 * point with an effect (disk reads included) must see BOTH a real dry-run and an
 * explicit beta opt-in, and must reject before doing anything observable.
 * 1.x behaviour is untouched.
 */
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
        ? '; only DRY_RUN with an explicit beta opt-in is enabled'
        : '; only v2.x.y-beta.N is enabled')
  );
}

export const expectedV2AssetNames = (tag) => {
  assertSupportedVersion(tag, { allowV2Beta: true });
  if (!isV2Version(tag)) throw new Error('v2 asset set requires a v2.x.y-beta.N release');
  return [
    'checksums.txt',
    'release-info.json',
    v2EmbeddedComposeFilename,
    v2PairingFilename,
    'management.html',
    v2NativeArchiveName(tag),
  ].sort();
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
  }
  const expected = expectedV2AssetNames(tag);
  if (list.length !== expected.length || list.some((name, index) => name !== expected[index]))
    throw new Error(
      `v2 release asset set mismatch: expected ${expected.join(', ')}, received ${list.join(', ')}`
    );
  return list;
}

/**
 * Validate `embedded-release-pairing.json` (Scope 4 producer, PR-1) as consumed
 * by release-info generation. Notice/provenance material travels inside the
 * images and the Native archive, so the pairing record must bind each image's
 * release-provenance digest rather than publishing separate notice assets.
 */
export function validateV2Pairing(pairing, { tag, sourceCommit }) {
  if (!pairing || typeof pairing !== 'object' || pairing.schema_version !== 1)
    throw new Error('Invalid v2 pairing record');
  if (pairing.version !== tag) throw new Error('v2 pairing version mismatch');
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
  if (!pairing.images || typeof pairing.images !== 'object')
    throw new Error('v2 pairing is missing image records');
  for (const [role, repositoryName] of Object.entries(v2ImageRepositories)) {
    const image = pairing.images[role];
    if (!image || typeof image !== 'object')
      throw new Error(`v2 pairing is missing the ${role} image`);
    const repositoryValue = String(image.repository ?? '');
    const allowedRepositories = v2ImageRegistries.map(
      (registry) => `${registry}/${repositoryName}`
    );
    if (!allowedRepositories.includes(repositoryValue))
      throw new Error(`v2 pairing has an unexpected ${role} repository`);
    if (image.tag !== tag) throw new Error(`v2 pairing ${role} tag mismatch`);
    assertSha256(image.manifest_digest, `v2 pairing ${role} manifest digest`);
    assertSha256(image.image_config_digest, `v2 pairing ${role} image config digest`);
    assertSha256(image.release_provenance_sha256, `v2 pairing ${role} release provenance digest`);
  }
  const roleDigests = Object.keys(v2ImageRepositories).map(
    (role) => pairing.images[role].manifest_digest
  );
  if (new Set(roleDigests).size !== roleDigests.length)
    throw new Error('v2 role manifest digests must be distinct');
  return pairing;
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
  if (typeof info.update?.breaking !== 'boolean') throw new Error('Explicit update flags required');
  if (info.update.migration_required !== true) throw new Error('v2 release must require migration');
  if (info.update.minimum_direct_upgrade_version !== v2MinimumDirectUpgradeVersion)
    throw new Error(
      `v2 release must record minimum_direct_upgrade_version ${v2MinimumDirectUpgradeVersion}`
    );
  const guide = new URL(info.update.upgrade_guide_url);
  if (
    guide.origin !== 'https://github.com' ||
    guide.username ||
    guide.password ||
    !guide.pathname.startsWith('/seakee/CPA-Manager-Plus/')
  )
    throw new Error('Invalid v2 upgrade guide');
  if (info.compatibility?.minimum_cpa_version !== v2MinimumCPAVersion)
    throw new Error(`v2 release must require CPA ${v2MinimumCPAVersion}`);
  if (JSON.stringify(info.supported_modes) !== JSON.stringify(v2SupportedModes))
    throw new Error('Unsupported v2 mode set');
  if (
    JSON.stringify(info.supported_platforms) !==
    JSON.stringify({ docker: v2Platforms, native: v2Platforms })
  )
    throw new Error('Unsupported v2 platform set');

  if (info.distribution?.docker?.version_tag !== tag)
    throw new Error('Invalid v2 docker distribution');
  const images = info.distribution?.docker?.images;
  if (!Array.isArray(images) || images.length !== 6)
    throw new Error('Invalid v2 image set: three roles for Docker Hub and GHCR are required');
  const seen = new Set();
  const roleDigests = new Map();
  for (const image of images) {
    const repositoryName = v2ImageRepositories[image?.role];
    if (!repositoryName) throw new Error('Invalid v2 image role');
    const expectedNames = v2ImageRegistries.map((registry) => `${registry}/${repositoryName}`);
    if (!expectedNames.includes(image.name))
      throw new Error(`Invalid v2 image reference for the ${image.role} role`);
    if (image.tag !== tag) throw new Error('Invalid v2 image tag');
    assertSha256(image.digest, 'v2 image digest');
    if (JSON.stringify(image.platforms) !== JSON.stringify(v2Platforms))
      throw new Error('Invalid v2 image platforms');
    if (seen.has(image.name)) throw new Error('Duplicate v2 image reference');
    seen.add(image.name);
    const digests = roleDigests.get(image.role) ?? new Set();
    digests.add(image.digest);
    roleDigests.set(image.role, digests);
  }
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
export function assertV2AssetBindings(info, { composeSha256, pairing }) {
  if (typeof composeSha256 !== 'string' || !sha256Pattern.test(composeSha256))
    throw new Error('Invalid v2 compose asset digest');
  if (composeSha256 !== info.distribution.compose.sha256)
    throw new Error('v2 compose asset digest does not match release info');
  const validated = validateV2Pairing(pairing, {
    tag: info.release.version,
    sourceCommit: info.release.source_commit,
  });
  if (validated.compose.sha256 !== info.pairing.compose_sha256)
    throw new Error('v2 pairing compose digest does not match release info');
  if (validated.fullweb.manifest_sha256 !== info.pairing.fullweb_manifest_sha256)
    throw new Error('v2 pairing Full Web digest does not match release info');
  if (validated.api_contract.sha256 !== info.pairing.api_contract_sha256)
    throw new Error('v2 pairing API contract digest does not match release info');
  for (const role of Object.keys(v2ImageRepositories)) {
    const digests = new Set(
      info.distribution.docker.images
        .filter((image) => image.role === role)
        .map((image) => image.digest)
    );
    if (digests.size !== 1 || !digests.has(validated.images[role].manifest_digest))
      throw new Error(`v2 pairing ${role} digest does not match release info`);
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
 * Validate a producer update index. The top-level file stays schema_version 1
 * so 1.x consumers keep parsing it; a v2 Beta target is only legal on the beta
 * channel, must carry `schema_version: 2`, and needs the explicit opt-in.
 */
export function validateIndex(index, options = {}) {
  const allowV2Beta = assertBooleanOption(options, 'allowV2Beta');
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
    const v = parseVersion(target?.version);
    if (
      !/^v/.test(target.version) ||
      target.version.includes('+') ||
      !stageAllowed(channel, v.stage)
    )
      throw new Error('Invalid update index target');
    if (v.core[0] === '2') {
      if (channel !== 'beta') throw new Error('v2 release may only target the beta channel');
      // Reject `v2.0.0` and `v2.0.0-rc.1` on the beta channel: the v2 Beta
      // channel target is exactly `v2.x.y-beta.N`.
      assertSupportedVersion(target.version, { allowV2Beta: true });
      if (v.stage !== 'beta') throw new Error('v2 beta channel target must be a beta prerelease');
      if (target.schema_version !== 2)
        throw new Error('v2 beta index target requires schema_version 2');
      if (!allowV2Beta)
        throw new Error(
          'Unqualified v2 Embedded release: v2 beta index target requires an explicit dry-run opt-in'
        );
    } else if (target.schema_version !== undefined) {
      throw new Error('Invalid update index target schema');
    }
  }
  return index;
}

export function resolveChannels(infos, withdrawn = [], options = {}) {
  const channels = { stable: null, rc: null, beta: null };
  for (const info of infos) {
    const tag = info.release.version;
    validateInfo(info, tag, options);
    if (withdrawn.includes(tag)) continue;
    for (const ch of Object.keys(channels))
      if (
        stageAllowed(ch, info.release.stage) &&
        (!channels[ch] || compareVersions(tag, channels[ch].version) > 0)
      )
        channels[ch] =
          parseVersion(tag).core[0] === '2'
            ? { version: tag, schema_version: 2 }
            : { version: tag };
  }
  return channels;
}
