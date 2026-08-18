import { createHash } from 'node:crypto';
import { readdirSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..');

export const nativeAssetPattern =
  /^cpa-manager-plus_(v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?)_(linux|darwin|windows)_(amd64|arm64)\.(tar\.gz|zip)$/;

const sha256File = (filePath) =>
  createHash('sha256').update(readFileSync(filePath)).digest('hex');

export const buildUpdateManifest = ({ version, nativeDir }) => {
  const assets = {};
  for (const name of readdirSync(nativeDir).sort()) {
    const match = nativeAssetPattern.exec(name);
    if (!match) continue;
    const [, assetVersion, platform, architecture] = match;
    if (assetVersion !== version) {
      throw new Error(`Native asset ${name} does not match release version ${version}`);
    }
    const key = `${platform}-${architecture}`;
    if (assets[key]) throw new Error(`Duplicate native asset for ${key}`);
    assets[key] = {
      name,
      sha256: sha256File(path.join(nativeDir, name)),
    };
  }

  const required = [
    'linux-amd64',
    'linux-arm64',
    'darwin-amd64',
    'darwin-arm64',
    'windows-amd64',
    'windows-arm64',
  ];
  const missing = required.filter((key) => !assets[key]);
  if (missing.length > 0) {
    throw new Error(`Missing native update assets: ${missing.join(', ')}`);
  }

  return {
    schemaVersion: 1,
    version,
    channel: version.includes('-') ? 'prerelease' : 'stable',
    minimumUpdaterVersion: 'v1.0.0',
    assets,
  };
};

export const writeUpdateManifest = ({ version, nativeDir, output }) => {
  const manifest = buildUpdateManifest({ version, nativeDir });
  writeFileSync(output, `${JSON.stringify(manifest, null, 2)}\n`, 'utf8');
  return manifest;
};

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const version = process.env.VERSION?.trim();
  const nativeDir = path.resolve(process.env.NATIVE_DIR || path.join(repoRoot, 'dist/native'));
  const output = path.resolve(
    process.env.OUTPUT || path.join(nativeDir, 'update-manifest.json')
  );
  if (!version) throw new Error('VERSION is required');
  writeUpdateManifest({ version, nativeDir, output });
}
