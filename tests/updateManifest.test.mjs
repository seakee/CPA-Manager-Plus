import { mkdtempSync, rmSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { describe, expect, it } from 'vitest';
import { buildUpdateManifest } from '../bin/release/generate-update-manifest.mjs';

const targets = [
  ['linux', 'amd64', 'tar.gz'],
  ['linux', 'arm64', 'tar.gz'],
  ['darwin', 'amd64', 'tar.gz'],
  ['darwin', 'arm64', 'tar.gz'],
  ['windows', 'amd64', 'zip'],
  ['windows', 'arm64', 'zip'],
];

describe('update release manifest', () => {
  it('binds every supported native asset to its release version and checksum', () => {
    const dir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-update-manifest-'));
    try {
      for (const [platform, architecture, extension] of targets) {
        writeFileSync(
          path.join(dir, `cpa-manager-plus_v1.2.3_${platform}_${architecture}.${extension}`),
          `${platform}-${architecture}`
        );
      }
      const manifest = buildUpdateManifest({ version: 'v1.2.3', nativeDir: dir });
      expect(manifest.channel).toBe('stable');
      expect(Object.keys(manifest.assets)).toHaveLength(6);
      expect(manifest.assets['windows-amd64'].sha256).toMatch(/^[0-9a-f]{64}$/);
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  });

  it('fails closed when a platform asset is absent', () => {
    const dir = mkdtempSync(path.join(os.tmpdir(), 'cpamp-update-manifest-'));
    try {
      expect(() => buildUpdateManifest({ version: 'v1.2.3', nativeDir: dir })).toThrow(
        'Missing native update assets'
      );
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  });
});
