import { readFileSync, writeFileSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { repository, parseVersion, nativeAssets, validateInfo } from './update-contract.mjs';

export function generateReleaseInfo(tag, sha, chineseNotes) {
  // One reviewed block in the existing release PR; no second notes source.
  const matches = [...chineseNotes.matchAll(/<!--\s*cpamp-update\s*\n([\s\S]*?)-->/g)];
  if (matches.length !== 1)
    throw new Error('Release notes require exactly one cpamp-update JSON comment');
  const metadata = JSON.parse(matches[0][1]);
  return validateInfo(
    {
      schema_version: 1,
      release: { version: tag, stage: parseVersion(tag).stage, source_commit: sha },
      content: {
        summary: metadata.summary,
        notes: Object.fromEntries(
          ['zh', 'en'].map((lang) => [
            lang,
            `${repository}/blob/${tag}/docs/release-notes/${tag}-${lang}.md`,
          ])
        ),
      },
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
if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const [tag, out, sourceCommit] = process.argv.slice(2);
  const sha =
    sourceCommit || execFileSync('git', ['rev-parse', 'HEAD'], { encoding: 'utf8' }).trim();
  const info = generateReleaseInfo(
    tag,
    sha,
    readFileSync(`docs/release-notes/${tag}-zh.md`, 'utf8')
  );
  if (out) writeFileSync(out, JSON.stringify(info, null, 2) + '\n');
}
