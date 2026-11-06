import { readFileSync, writeFileSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import {
  repository,
  parseVersion,
  nativeAssets,
  validateInfo,
  assertSupportedVersion,
  assertV2Rehearsal,
  validateV2Pairing,
  v2EmbeddedComposeFilename,
  v2PairingFilename,
  v2MinimumDirectUpgradeVersion,
  v2MinimumCPAVersion,
  v2SupportedModes,
  v2Platforms,
  v2ImageRegistries,
  v2ImageRepositories,
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

const buildV2ReleaseInfo = (tag, sha, metadata, pairingInput) => {
  const pairing = validateV2Pairing(pairingInput, { tag, sourceCommit: sha });
  if (typeof metadata.update?.breaking !== 'boolean')
    throw new Error('Explicit update flags required');
  if (metadata.update.migration_required !== true)
    throw new Error('v2 beta release notes must declare migration_required true');
  if (metadata.update.minimum_direct_upgrade_version !== v2MinimumDirectUpgradeVersion)
    throw new Error(
      `v2 beta release notes must declare minimum_direct_upgrade_version ${v2MinimumDirectUpgradeVersion}`
    );
  if (metadata.compatibility?.minimum_cpa_version !== v2MinimumCPAVersion)
    throw new Error(
      `v2 beta release notes must declare minimum_cpa_version ${v2MinimumCPAVersion}`
    );
  const images = v2ImageRegistries.flatMap((registry) =>
    Object.entries(v2ImageRepositories).map(([role, repositoryName]) => ({
      role,
      name: `${registry}/${repositoryName}`,
      tag,
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
        migration_required: true,
        minimum_direct_upgrade_version: v2MinimumDirectUpgradeVersion,
        upgrade_guide_url: metadata.update.upgrade_guide_url,
      },
      distribution: {
        docker: { version_tag: tag, images },
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
  // Gate M3 at the exported boundary: no v2 record may be produced without a
  // real dry-run and an explicit beta opt-in, before notes are even parsed.
  assertV2Rehearsal(tag, options);
  const v = assertSupportedVersion(tag, options);
  const metadata = parseUpdateMetadata(chineseNotes);
  if (v.core[0] === '2') return buildV2ReleaseInfo(tag, sha, metadata, options.pairing);
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
  const isV2 = parseVersion(tag).core[0] === '2';
  const dryRunV2OptIn = isV2 && options.allowV2Beta === true && options.dryRun === true;
  if (isV2 && !dryRunV2OptIn)
    throw new Error(
      `v2 release contract is fail-closed (Gate M3); DRY_RUN and --allow-v2-beta are both required: ${tag}`
    );
  // A stable v2 tag never passes, even with the opt-in, before any file is read.
  assertSupportedVersion(tag, { allowV2Beta: dryRunV2OptIn });
  const infoOptions = isV2 ? { dryRun: true, allowV2Beta: true } : {};
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
