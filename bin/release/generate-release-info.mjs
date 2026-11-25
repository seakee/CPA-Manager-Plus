import { readFileSync, writeFileSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import {
  repository,
  parseVersion,
  nativeAssets,
  validateInfo,
  assertSupportedVersion,
  assertBooleanOption,
  assertV2UpgradeGuideUrl,
  validateV2Pairing,
  v2EmbeddedComposeFilename,
  v2PairingFilename,
  v2MinimumCPAVersion,
  v2SupportedModes,
  v2Platforms,
  v2ImageRegistries,
  v2ImageRepositoryName,
  v2ImageRoles,
  v2DockerVersionTag,
  v2DockerRoleTag,
  v2NativeArchiveName,
} from './update-contract.mjs';

const parseUpdateMetadata = (chineseNotes) => {
  // One reviewed block in the existing release PR; no second notes source.
  const matches = [...String(chineseNotes).matchAll(/<!--\s*cpamp-update\s*\n([\s\S]*?)-->/g)];
  if (matches.length !== 1)
    throw new Error('Release notes require exactly one cpamp-update JSON comment');
  return JSON.parse(matches[0][1]);
};

const contentFor = (tag, metadata) => ({
  summary: metadata.summary,
  notes: Object.fromEntries(
    ['zh', 'en'].map((lang) => [
      lang,
      `${repository}/blob/${tag}/docs/release-notes/${tag}-${lang}.md`,
    ])
  ),
});

const validateV2NotesMetadata = (metadata) => {
  for (const language of ['zh', 'en']) {
    const summary = metadata.summary?.[language];
    if (typeof summary !== 'string' || !summary.trim() || Buffer.byteLength(summary, 'utf8') > 4096)
      throw new Error('Invalid v2 release content');
  }
  assertV2UpgradeGuideUrl(metadata.update?.upgrade_guide_url);
  if (
    typeof metadata.update?.breaking !== 'boolean' ||
    typeof metadata.update?.migration_required !== 'boolean'
  )
    throw new Error('Explicit update flags required');
  if (
    !('minimum_direct_upgrade_version' in (metadata.update || {})) ||
    metadata.update.minimum_direct_upgrade_version === undefined
  )
    throw new Error('v2 beta release notes must declare minimum_direct_upgrade_version');
  if (metadata.update.minimum_direct_upgrade_version !== null) {
    if (typeof metadata.update.minimum_direct_upgrade_version !== 'string')
      throw new Error('Invalid v2 minimum_direct_upgrade_version');
    parseVersion(metadata.update.minimum_direct_upgrade_version);
  }
  if (metadata.compatibility?.minimum_cpa_version !== v2MinimumCPAVersion)
    throw new Error(
      `v2 beta release notes must declare minimum_cpa_version ${v2MinimumCPAVersion}`
    );
};

// Validate reviewed note metadata before OCI evidence/pairing exists. This is
// deliberately a notes check, not a generated release-info or publication.
export function validateReleaseMetadata(tag, chineseNotes, options = {}) {
  assertBooleanOption(options, 'dryRun');
  const version = assertSupportedVersion(tag, options);
  const metadata = parseUpdateMetadata(chineseNotes);
  if (version.core[0] === '2') validateV2NotesMetadata(metadata);
  return metadata;
}

const buildV2ReleaseInfo = (tag, sha, metadata, pairingInput) => {
  const pairing = validateV2Pairing(pairingInput, { tag, sourceCommit: sha });
  const versionTag = v2DockerVersionTag(tag);
  const images = v2ImageRegistries.flatMap((registry) =>
    v2ImageRoles.map((role) => ({
      role,
      name: `${registry}/${v2ImageRepositoryName}`,
      tag: v2DockerRoleTag(tag, role),
      digest: pairing.images[role].manifest_digest,
      platforms: [...v2Platforms],
    }))
  );
  return validateInfo(
    {
      schema_version: 2,
      release: { version: tag, stage: 'beta', source_commit: sha },
      content: contentFor(tag, metadata),
      update: {
        breaking: metadata.update.breaking,
        migration_required: metadata.update.migration_required,
        minimum_direct_upgrade_version: metadata.update.minimum_direct_upgrade_version,
        upgrade_guide_url: metadata.update.upgrade_guide_url,
      },
      distribution: {
        docker: { version_tag: versionTag, images },
        native: { assets: [v2NativeArchiveName(tag)] },
        compose: { filename: v2EmbeddedComposeFilename, sha256: pairing.compose.sha256 },
      },
      compatibility: { minimum_cpa_version: v2MinimumCPAVersion },
      supported_modes: [...v2SupportedModes],
      supported_platforms: { docker: [...v2Platforms], native: [...v2Platforms] },
      pairing: {
        filename: v2PairingFilename,
        source_commit: pairing.source_commit,
        fullweb_manifest_sha256: pairing.fullweb.manifest_sha256,
        api_contract_sha256: pairing.api_contract.sha256,
        compose_sha256: pairing.compose.sha256,
      },
    },
    tag,
    { allowV2Beta: true }
  );
};

export function generateReleaseInfo(tag, sha, chineseNotes, options = {}) {
  const metadata = validateReleaseMetadata(tag, chineseNotes, options);
  if (parseVersion(tag).core[0] === '2') return buildV2ReleaseInfo(tag, sha, metadata, options.pairing);
  return validateInfo(
    {
      schema_version: 1,
      release: { version: tag, stage: parseVersion(tag).stage, source_commit: sha },
      content: contentFor(tag, metadata),
      update: metadata.update,
      distribution: {
        docker: { image: 'seakee/cpa-manager-plus', version_tag: tag },
        native: { assets: nativeAssets(tag) },
      },
      compatibility: metadata.compatibility,
    },
    tag
  );
}

const parseCliArguments = (argumentsList) => {
  const options = { dryRun: process.env.DRY_RUN === 'true' };
  const positional = [];
  for (let index = 0; index < argumentsList.length; index += 1) {
    const argument = argumentsList[index];
    if (argument === '--dry-run') options.dryRun = true;
    else if (argument === '--allow-v2-beta') options.allowV2Beta = true;
    else if (argument === '--pairing') {
      const value = argumentsList[++index];
      if (!value) throw new Error('--pairing requires a path');
      options.pairingPath = value;
    } else if (argument.startsWith('--')) throw new Error(`Unknown argument: ${argument}`);
    else positional.push(argument);
  }
  return { options, positional };
};

const runCli = () => {
  const { options, positional } = parseCliArguments(process.argv.slice(2));
  const [tag, out, sourceCommit] = positional;
  if (!tag) throw new Error('Missing release tag');
  // Reject unsupported versions and missing opt-in before reading any files.
  const isV2 = assertSupportedVersion(tag, options).core[0] === '2';
  const infoOptions = { dryRun: options.dryRun, allowV2Beta: options.allowV2Beta };
  if (isV2) {
    if (!options.pairingPath)
      throw new Error('v2 beta release info requires --pairing embedded-release-pairing.json');
    infoOptions.pairing = JSON.parse(readFileSync(options.pairingPath, 'utf8'));
  }
  const sha =
    sourceCommit || execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim();
  const info = generateReleaseInfo(
    tag,
    sha,
    readFileSync(`docs/release-notes/${tag}-zh.md`, 'utf8'),
    infoOptions
  );
  if (out) writeFileSync(out, JSON.stringify(info, null, 2) + '\n');
};

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  try {
    runCli();
  } catch (error) {
    console.error(error instanceof Error ? error.message : String(error));
    process.exitCode = 1;
  }
}
