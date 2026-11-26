import { execFileSync } from 'node:child_process';
import { createHash, sign as cryptoSign } from 'node:crypto';
import {
  chmodSync,
  existsSync,
  lstatSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  statSync,
  symlinkSync,
  utimesSync,
  writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { gunzipSync, gzipSync } from 'node:zlib';
import { afterEach, describe, expect, it } from 'vitest';
import {
  DEFAULT_SLOT_KEY_ID,
  DEFAULT_TRUSTED_SLOT_KEYS,
  TRUSTED_SLOT_PUBLIC_KEYS,
  generateSlotKeypair,
  resolveSlotPublicKeyHex,
  signSlotManifestBytes,
  verifySlotManifestBytes,
} from '../bin/release/cpamp-slot-sign.mjs';
import { packageCpampSlot, packageSlotArchive } from '../bin/release/package-cpamp-slot.mjs';
import {
  extractSlotMetadataFromArchive,
  generateReleaseInfo,
} from '../bin/release/generate-release-info.mjs';
const SIGN_SCRIPT = fileURLToPath(new URL('../bin/release/cpamp-slot-sign.mjs', import.meta.url));
const PACKAGE_SCRIPT = fileURLToPath(new URL('../bin/release/package-cpamp-slot.mjs', import.meta.url));
const SOURCE_COMMIT = 'a'.repeat(40);
const TEMP_DIRS = [];

const makeTempDir = (prefix = 'cpamp-slot-test-') => {
  const dir = mkdtempSync(path.join(tmpdir(), prefix));
  TEMP_DIRS.push(dir);
  return dir;
};

afterEach(() => {
  while (TEMP_DIRS.length > 0) {
    const dir = TEMP_DIRS.pop();
    if (!existsSync(dir)) continue;
    const makeWritable = (target) => {
      const lst = lstatSync(target);
      if (lst.isSymbolicLink() || !lst.isDirectory()) return;
      chmodSync(target, 0o755);
      for (const child of readdirSafe(target)) {
        makeWritable(path.join(target, child));
      }
    };
    makeWritable(dir);
    rmSync(dir, { recursive: true, force: true });
  }
});

import { readdirSync } from 'node:fs';
function readdirSafe(dir) {
  try {
    return readdirSync(dir);
  } catch {
    return [];
  }
}

function parseTarArchive(gzipBytes) {
  expect(gzipBytes[0]).toBe(0x1f);
  expect(gzipBytes[1]).toBe(0x8b);
  // gzip -n sets mtime=0 in bytes 4..7 and clears FNAME flag (0x08) in byte 3.
  expect(gzipBytes.readUInt32LE(4)).toBe(0);
  expect(gzipBytes[3] & 0x08).toBe(0);

  const tarBytes = gunzipSync(gzipBytes);
  const entries = [];
  let offset = 0;
  let paxOverrides = {};

  while (offset + 512 <= tarBytes.length) {
    const header = tarBytes.subarray(offset, offset + 512);
    if (header.every((b) => b === 0)) break;
    const name = header.subarray(0, 100).toString('utf8').replace(/\0.*$/, '');
    const mode = Number.parseInt(header.subarray(100, 108).toString('utf8').replace(/\0.*$/, '').trim(), 8);
    const uid = Number.parseInt(header.subarray(108, 116).toString('utf8').replace(/\0.*$/, '').trim(), 8);
    const gid = Number.parseInt(header.subarray(116, 124).toString('utf8').replace(/\0.*$/, '').trim(), 8);
    const size = Number.parseInt(header.subarray(124, 136).toString('utf8').replace(/\0.*$/, '').trim() || '0', 8);
    const mtime = Number.parseInt(header.subarray(136, 148).toString('utf8').replace(/\0.*$/, '').trim() || '0', 8);
    const typeflag = header.subarray(156, 157).toString('utf8') || '0';
    const uname = header.subarray(265, 297).toString('utf8').replace(/\0.*$/, '');
    const gname = header.subarray(297, 329).toString('utf8').replace(/\0.*$/, '');
    const prefix = header.subarray(345, 500).toString('utf8').replace(/\0.*$/, '');

    const dataStart = offset + 512;
    const dataEnd = dataStart + size;
    const body = Buffer.from(tarBytes.subarray(dataStart, dataEnd));
    offset = dataStart + Math.ceil(size / 512) * 512;

    if (typeflag === 'x') {
      const text = body.toString('utf8');
      for (const line of text.split('\n')) {
        const m = /^\d+ ([^=]+)=(.*)$/.exec(line);
        if (m) paxOverrides[m[1]] = m[2];
      }
      continue;
    }

    const fullName = paxOverrides.path ?? (prefix ? `${prefix}/${name}` : name);
    const resolvedUid = paxOverrides.uid !== undefined ? Number(paxOverrides.uid) : uid;
    const resolvedGid = paxOverrides.gid !== undefined ? Number(paxOverrides.gid) : gid;
    const resolvedMtime = paxOverrides.mtime !== undefined ? Number(paxOverrides.mtime) : mtime;
    paxOverrides = {};

    entries.push({
      name: fullName,
      mode: mode & 0o7777,
      uid: resolvedUid,
      gid: resolvedGid,
      uname,
      gname,
      size,
      mtime: resolvedMtime,
      typeflag,
      body,
    });
  }
  return entries;
}

function stageSlotRoot({ layout = 'docker', version = 'v2.0.0-beta.2', platform = 'linux/amd64', baseEpoch = 1 } = {}) {
  const root = makeTempDir(`cpamp-slot-root-${layout}-`);
  const prefix = layout === 'docker' ? 'usr/local/' : '';

  const writeMember = (rel, content, mode = 0o644) => {
    const abs = path.join(root, prefix + rel);
    mkdirSync(path.dirname(abs), { recursive: true });
    writeFileSync(abs, content);
    chmodSync(abs, mode);
    // Deliberately stamp non-zero mtimes to prove deterministic @0 normalization.
    utimesSync(abs, 1700000000, 1700000000);
    return abs;
  };

  writeMember('bin/cpamp-launcher', '#!/bin/sh\necho launcher\n', 0o755);
  writeMember('bin/cpamp-runtime-supervisor', '#!/bin/sh\necho supervisor\n', 0o755);
  writeMember('bin/cpa-manager-plus', '#!/bin/sh\necho manager\n', 0o755);
  writeMember('bin/cpamp-ingress', '#!/bin/sh\necho ingress\n', 0o755);
  // Extra CPA binary in full image root must NOT be packaged into control-plane slot.
  writeMember('bin/cli-proxy-api', '#!/bin/sh\necho cpa\n', 0o755);

  writeMember(
    'share/cpamp/provenance.json',
    JSON.stringify(
      {
        schemaVersion: 1,
        role: 'cpamp',
        version,
        sourceCommit: SOURCE_COMMIT,
        platform,
      },
      null,
      2
    ) + '\n',
    0o444
  );
  writeMember(
    'share/cpamp/release/base-abi.json',
    JSON.stringify(
      {
        base_epoch: baseEpoch,
        platform,
        schema_version: 1,
      },
      null,
      2
    ) + '\n',
    0o444
  );
  writeMember('share/cpamp/release/fullweb-manifest.json', '{"schemaVersion":1,"files":[]}\n', 0o444);
  writeMember(
    'share/cpamp/release/public-api-transport-contract.json',
    '{"schemaVersion":1}\n',
    0o444
  );
  writeMember(
    'share/cpamp/runtime/cpa-artifact.json',
    '{"schemaVersion":1,"engine":"cpa","version":"8.0.55"}\n',
    0o444
  );
  writeMember('share/cpamp/runtime/config.seed.yaml', 'port: 8317\n', 0o644);
  writeMember('share/cpamp/runtime/seed-lineage.json', '{"schema_version":1}\n', 0o644);
  writeMember('share/licenses/cpamp/LICENSE', 'MIT License\n', 0o444);
  writeMember('share/licenses/manager/NOTICE.txt', 'Manager third-party notices\n', 0o444);

  return { root, prefix };
}

describe('cpamp-slot-sign and package-cpamp-slot', () => {
  it('exposes the default contract root keyring', () => {
    expect(DEFAULT_SLOT_KEY_ID).toBe('cpamp-root-2026-01');
    expect(TRUSTED_SLOT_PUBLIC_KEYS).toBe(DEFAULT_TRUSTED_SLOT_KEYS);
    expect(DEFAULT_TRUSTED_SLOT_KEYS).toEqual({
      'cpamp-root-2026-01': 'e6eb42c6a92a4c78d1c2e6b050234824b17936788799036b4201f98e6291d41f',
    });
  });

  it('generates Ed25519 keypairs via API and CLI with 0600 private key', () => {
    const dir = makeTempDir('cpamp-keygen-');
    const pair = generateSlotKeypair('release-slot-2026');
    expect(pair.key_id).toBe('release-slot-2026');
    expect(pair.algorithm).toBe('ed25519');
    expect(pair.publicKeyHex).toMatch(/^[0-9a-f]{64}$/);
    expect(pair.privateKeyPem).toContain('BEGIN PRIVATE KEY');

    const cliOut = JSON.parse(
      execFileSync(process.execPath, [SIGN_SCRIPT, 'keygen', '--key-id', 'cli-key-1', '--out-dir', dir], {
        encoding: 'utf8',
      })
    );
    expect(cliOut.key_id).toBe('cli-key-1');
    expect(statSync(path.join(dir, 'cli-key-1.priv.pem')).mode & 0o777).toBe(0o600);
    const pubJson = JSON.parse(readFileSync(path.join(dir, 'cli-key-1.pub.json'), 'utf8'));
    expect(pubJson).toEqual({
      key_id: 'cli-key-1',
      algorithm: 'ed25519',
      public_key_hex: cliOut.public_key_hex,
    });
    expect(pubJson.public_key_hex).toMatch(/^[0-9a-f]{64}$/);

    expect(() => generateSlotKeypair('../bad')).toThrow(/Invalid slot signing key_id/);
    expect(() => generateSlotKeypair('')).toThrow(/Invalid slot signing key_id/);
  });

  it('signs with domain separation and rejects tampered bytes, wrong key_id, and bad keys', () => {
    const dir = makeTempDir('cpamp-sign-');
    const pair = generateSlotKeypair('slot-sign-test');
    const otherPair = generateSlotKeypair('slot-sign-other');
    const manifestPath = path.join(dir, 'slot-manifest.json');
    const sigPath = path.join(dir, 'slot-manifest.json.sig');
    const manifestBytes = Buffer.from('{\n  "schema_version": 1,\n  "version": "v2.0.0-beta.2"\n}\n', 'utf8');
    writeFileSync(manifestPath, manifestBytes);

    const privPath = path.join(dir, 'slot-sign-test.priv.pem');
    writeFileSync(privPath, pair.privateKeyPem, { mode: 0o600 });

    const sig = signSlotManifestBytes(manifestBytes, pair.privateKeyPem, 'slot-sign-test');
    expect(sig.schema_version).toBe(1);
    expect(sig.algorithm).toBe('ed25519');
    expect(sig.key_id).toBe('slot-sign-test');
    expect(verifySlotManifestBytes(manifestBytes, sig, {
      publicKeyHex: pair.publicKeyHex,
      keyId: 'slot-sign-test',
    })).toBe(true);

    // CLI sign + verify roundtrip
    execFileSync(process.execPath, [
      SIGN_SCRIPT,
      'sign',
      '--manifest',
      manifestPath,
      '--private-key',
      privPath,
      '--key-id',
      'slot-sign-test',
      '--out',
      sigPath,
    ]);
    const verifyStdout = execFileSync(
      process.execPath,
      [
        SIGN_SCRIPT,
        'verify',
        '--manifest',
        manifestPath,
        '--signature',
        sigPath,
        '--public-key-hex',
        pair.publicKeyHex,
        '--key-id',
        'slot-sign-test',
      ],
      { encoding: 'utf8' }
    );
    expect(JSON.parse(verifyStdout)).toEqual({ ok: true, key_id: 'slot-sign-test' });

    // Supports 64-hex seed and 128-hex Go-style private key
    const pkcs8Der = Buffer.from(
      pair.privateKeyPem
        .replace(/-----BEGIN PRIVATE KEY-----|-----END PRIVATE KEY-----|\s+/g, ''),
      'base64'
    );
    const seedHex = pkcs8Der.subarray(pkcs8Der.length - 32).toString('hex');
    const goHex128 = seedHex + pair.publicKeyHex;
    expect(
      verifySlotManifestBytes(
        manifestBytes,
        signSlotManifestBytes(manifestBytes, seedHex, 'slot-sign-test'),
        { publicKeyHex: pair.publicKeyHex, keyId: 'slot-sign-test' }
      )
    ).toBe(true);
    expect(
      verifySlotManifestBytes(
        manifestBytes,
        signSlotManifestBytes(manifestBytes, goHex128, 'slot-sign-test'),
        { publicKeyHex: pair.publicKeyHex, keyId: 'slot-sign-test' }
      )
    ).toBe(true);

    // Signing WITHOUT domain prefix fails verification
    const rawSig = cryptoSign(null, manifestBytes, pair.privateKeyPem).toString('base64');
    expect(() =>
      verifySlotManifestBytes(
        manifestBytes,
        { schema_version: 1, algorithm: 'ed25519', key_id: 'slot-sign-test', signature: rawSig },
        { publicKeyHex: pair.publicKeyHex, keyId: 'slot-sign-test' }
      )
    ).toThrow(/signature verification failed/);

    // Tampered manifest bytes fail both API and CLI verify
    const tamperedBytes = Buffer.from(manifestBytes);
    tamperedBytes[tamperedBytes.length - 4] ^= 0x01;
    expect(() =>
      verifySlotManifestBytes(tamperedBytes, sig, {
        publicKeyHex: pair.publicKeyHex,
        keyId: 'slot-sign-test',
      })
    ).toThrow(/signature verification failed/);
    writeFileSync(manifestPath, tamperedBytes);
    expect(() =>
      execFileSync(process.execPath, [
        SIGN_SCRIPT,
        'verify',
        '--manifest',
        manifestPath,
        '--signature',
        sigPath,
        '--public-key-hex',
        pair.publicKeyHex,
        '--key-id',
        'slot-sign-test',
      ])
    ).toThrow();
    writeFileSync(manifestPath, manifestBytes);

    // Wrong key_id fails even when public key matches signer
    expect(() =>
      verifySlotManifestBytes(manifestBytes, sig, {
        publicKeyHex: pair.publicKeyHex,
        keyId: 'wrong-key-id',
      })
    ).toThrow(/key_id mismatch/);
    expect(() =>
      execFileSync(process.execPath, [
        SIGN_SCRIPT,
        'verify',
        '--manifest',
        manifestPath,
        '--signature',
        sigPath,
        '--public-key-hex',
        pair.publicKeyHex,
        '--key-id',
        'wrong-key-id',
      ])
    ).toThrow();

    // Wrong public key fails
    expect(() =>
      verifySlotManifestBytes(manifestBytes, sig, {
        publicKeyHex: otherPair.publicKeyHex,
        keyId: 'slot-sign-test',
      })
    ).toThrow(/signature verification failed/);
  });

  it('packages deterministic 0:0 @0 gzip -n slot tarballs from both docker and flat layouts', () => {
    const pair = generateSlotKeypair('cpamp-root-test');
    const keyDir = makeTempDir('cpamp-slot-key-');
    const privPath = path.join(keyDir, 'cpamp-root-test.priv.pem');
    writeFileSync(privPath, pair.privateKeyPem, { mode: 0o600 });

    for (const layout of ['docker', 'flat']) {
      for (const platform of ['linux/amd64', 'linux/arm64']) {
        const arch = platform.split('/')[1];
        const { root } = stageSlotRoot({ layout, version: 'v2.0.0-beta.2', platform });
        const outDir1 = makeTempDir(`cpamp-slot-out1-${layout}-${arch}-`);
        const outDir2 = makeTempDir(`cpamp-slot-out2-${layout}-${arch}-`);

        const pkg1 = packageCpampSlot({
          root,
          version: 'v2.0.0-beta.2',
          sourceCommit: SOURCE_COMMIT,
          platform,
          minBaseEpoch: 1,
          schemaGeneration: 1,
          keyId: 'cpamp-root-test',
          privateKey: privPath,
          publicKeyHex: pair.publicKeyHex,
          out: outDir1,
        });
        const pkg2 = packageCpampSlot({
          root,
          version: 'v2.0.0-beta.2',
          sourceCommit: SOURCE_COMMIT,
          platform,
          minBaseEpoch: 1,
          schemaGeneration: 1,
          keyId: 'cpamp-root-test',
          privateKey: pair.privateKeyPem,
          publicKeyHex: pair.publicKeyHex,
          out: outDir2,
        });

        expect(pkg1.archiveName).toBe(`cpamp-slot_v2.0.0-beta.2_linux_${arch}.tar.gz`);
        const bytes1 = readFileSync(pkg1.archivePath);
        const bytes2 = readFileSync(pkg2.archivePath);
        expect(bytes1.equals(bytes2)).toBe(true);

        const entries = parseTarArchive(bytes1);
        const names = entries.map((e) => e.name);
        expect(names).toEqual([...names].sort((a, b) => (a < b ? -1 : a > b ? 1 : 0)));
        expect(names.some((n) => n.startsWith('/') || n.startsWith('./') || n.includes('..'))).toBe(false);
        expect(names).not.toContain('bin/cli-proxy-api');
        expect(names).not.toContain('share/cpamp/provenance.json');
        for (const entry of entries) {
          expect(entry.uid).toBe(0);
          expect(entry.gid).toBe(0);
          expect(entry.mtime).toBe(0);
          if (entry.typeflag === '5') {
            if (entry.name === 'share/licenses' || entry.name.startsWith('share/licenses/')) {
              expect(entry.mode).toBe(0o555);
            }
          } else {
            expect(entry.mode).toBe(entry.name.startsWith('bin/') ? 0o555 : 0o444);
          }
        }

        const manifestEntry = entries.find((e) => e.name === 'slot-manifest.json');
        const sigEntry = entries.find((e) => e.name === 'slot-manifest.json.sig');
        expect(manifestEntry).toBeDefined();
        expect(sigEntry).toBeDefined();

        expect(
          verifySlotManifestBytes(manifestEntry.body, sigEntry.body, {
            publicKeyHex: pair.publicKeyHex,
            keyId: 'cpamp-root-test',
          })
        ).toBe(true);

        const manifest = JSON.parse(manifestEntry.body.toString('utf8'));
        expect(manifest.schema_version).toBe(1);
        expect(manifest.version).toBe('v2.0.0-beta.2');
        expect(manifest.source_commit).toBe(SOURCE_COMMIT);
        expect(manifest.platform).toBe(platform);
        expect(manifest.min_base_epoch).toBe(1);
        expect(manifest.schema_generation).toBe(1);
        expect(manifest.key_id).toBe('cpamp-root-test');

        const regularEntries = entries.filter(
          (e) => e.typeflag === '0' && e.name !== 'slot-manifest.json' && e.name !== 'slot-manifest.json.sig'
        );
        expect(Object.keys(manifest.files)).toEqual(regularEntries.map((e) => e.name));
        for (const entry of regularEntries) {
          const meta = manifest.files[entry.name];
          expect(meta.size).toBe(entry.body.length);
          expect(meta.sha256).toBe(
            `sha256:${createHash('sha256').update(entry.body).digest('hex')}`
          );
          expect(meta.mode).toBe(entry.name.startsWith('bin/') ? '0555' : '0444');
        }
      }
    }

    // Verify CLI invocation with env private key + generate-release-info --slot-archive integration
    const { root } = stageSlotRoot({ layout: 'docker', version: 'v2.0.0-beta.2', platform: 'linux/amd64' });
    const cliOutDir = makeTempDir('cpamp-slot-cli-');
    const cliStdout = execFileSync(
      process.execPath,
      [
        PACKAGE_SCRIPT,
        '--root',
        root,
        '--version',
        'v2.0.0-beta.2',
        '--source-commit',
        SOURCE_COMMIT,
        '--platform',
        'linux/amd64',
        '--min-base-epoch',
        '1',
        '--schema-generation',
        '1',
        '--key-id',
        'cpamp-root-test',
        '--private-key',
        'env:CPAMP_TEST_SLOT_PRIV_KEY',
        '--public-key-hex',
        pair.publicKeyHex,
        '--out',
        cliOutDir,
      ],
      {
        encoding: 'utf8',
        env: { ...process.env, CPAMP_TEST_SLOT_PRIV_KEY: pair.privateKeyPem },
      }
    );
    const summary = JSON.parse(cliStdout);
    expect(summary.archive_name).toBe('cpamp-slot_v2.0.0-beta.2_linux_amd64.tar.gz');
    expect(existsSync(summary.archive_path)).toBe(true);

    // Check BSD tar determinism explicitly if /usr/bin/tar exists alongside PATH tar
    if (existsSync('/usr/bin/tar')) {
      const bsdOut1 = makeTempDir('cpamp-bsd-1-');
      const bsdOut2 = makeTempDir('cpamp-bsd-2-');
      const r1 = packageCpampSlot({
        root,
        version: 'v2.0.0-beta.2',
        sourceCommit: SOURCE_COMMIT,
        platform: 'linux/amd64',
        keyId: 'cpamp-root-test',
        privateKey: pair.privateKeyPem,
        publicKeyHex: pair.publicKeyHex,
        out: bsdOut1,
        tarBinary: '/usr/bin/tar',
      });
      const r2 = packageCpampSlot({
        root,
        version: 'v2.0.0-beta.2',
        sourceCommit: SOURCE_COMMIT,
        platform: 'linux/amd64',
        keyId: 'cpamp-root-test',
        privateKey: pair.privateKeyPem,
        publicKeyHex: pair.publicKeyHex,
        out: bsdOut2,
        tarBinary: '/usr/bin/tar',
      });
      expect(readFileSync(r1.archivePath).equals(readFileSync(r2.archivePath))).toBe(true);
      for (const entry of parseTarArchive(readFileSync(r1.archivePath))) {
        expect(entry.uid).toBe(0);
        expect(entry.gid).toBe(0);
        expect(entry.mtime).toBe(0);
      }
    }

    // Feed generated slot archive into generateReleaseInfo
    const v2Sha = (v) => `sha256:${createHash('sha256').update(v).digest('hex')}`;
    const pairing = {
      schema_version: 1,
      version: 'v2.0.0-beta.2',
      source_commit: SOURCE_COMMIT,
      platform: 'linux/amd64',
      cpa: { version: '8.0.55', artifact_id: v2Sha('cpa') },
      fullweb: { manifest_sha256: v2Sha('web') },
      api_contract: { sha256: v2Sha('api') },
      compose: { filename: 'docker-compose.v2-embedded.yml', sha256: v2Sha('compose'), size: 120 },
      images: {
        slim: {
          repository: 'seakee/cpa-manager-plus',
          tag: '2.0.0-beta.2',
          manifest_digest: v2Sha('slim'),
          image_ref: `seakee/cpa-manager-plus:2.0.0-beta.2@${v2Sha('slim')}`,
          image_config_digest: v2Sha('slim-cfg'),
          release_provenance_sha256: v2Sha('slim-prov'),
        },
        full: {
          repository: 'seakee/cpa-manager-plus',
          tag: '2.0.0-beta.2-full',
          manifest_digest: v2Sha('full'),
          image_ref: `seakee/cpa-manager-plus:2.0.0-beta.2-full@${v2Sha('full')}`,
          image_config_digest: v2Sha('full-cfg'),
          release_provenance_sha256: v2Sha('full-prov'),
        },
      },
    };
    const notes = `<!-- cpamp-update\n${JSON.stringify({
      summary: { zh: 'Beta 2', en: 'Beta 2' },
      update: {
        breaking: false,
        migration_required: false,
        minimum_direct_upgrade_version: 'v2.0.0-beta.1',
        upgrade_guide_url: 'https://cpamp-docs.seakee.dev/next/operations/update.html',
      },
      compatibility: { minimum_cpa_version: 'v8.0.55' },
    })}\n-->`;
    const info = generateReleaseInfo('v2.0.0-beta.2', SOURCE_COMMIT, notes, {
      allowV2Beta: true,
      pairing,
      slotArchive: summary.archive_path,
      slotPublicKeyHex: pair.publicKeyHex,
    });
    expect(info.distribution.slot).toEqual({
      assets: [
        {
          name: 'cpamp-slot_v2.0.0-beta.2_linux_amd64.tar.gz',
          platform: 'linux/amd64',
          sha256: summary.sha256,
          size: summary.size,
          min_base_epoch: 1,
          schema_generation: 1,
        },
      ],
    });
  });

  it('rejects missing members, symlinks, and base-abi incompatibility', () => {
    const pair = generateSlotKeypair('cpamp-root-test');
    const { root } = stageSlotRoot({ layout: 'flat', version: 'v2.0.0-beta.2', platform: 'linux/amd64' });
    const outDir = makeTempDir('cpamp-slot-err-');

    // min_base_epoch > base_epoch
    expect(() =>
      packageCpampSlot({
        root,
        version: 'v2.0.0-beta.2',
        sourceCommit: SOURCE_COMMIT,
        platform: 'linux/amd64',
        minBaseEpoch: 2,
        keyId: 'cpamp-root-test',
        privateKey: pair.privateKeyPem,
        out: outDir,
      })
    ).toThrow(/min_base_epoch/);

    // Symlink rejection
    const { root: symRoot } = stageSlotRoot({ layout: 'flat' });
    rmSync(path.join(symRoot, 'bin/cpamp-ingress'));
    symlinkSync(path.join(symRoot, 'bin/cpamp-launcher'), path.join(symRoot, 'bin/cpamp-ingress'));
    expect(() =>
      packageCpampSlot({
        root: symRoot,
        version: 'v2.0.0-beta.2',
        sourceCommit: SOURCE_COMMIT,
        platform: 'linux/amd64',
        keyId: 'cpamp-root-test',
        privateKey: pair.privateKeyPem,
        out: outDir,
      })
    ).toThrow(/symlinks/);

    // Missing required member
    const { root: missRoot } = stageSlotRoot({ layout: 'flat' });
    rmSync(path.join(missRoot, 'share/cpamp/runtime/seed-lineage.json'));
    expect(() =>
      packageCpampSlot({
        root: missRoot,
        version: 'v2.0.0-beta.2',
        sourceCommit: SOURCE_COMMIT,
        platform: 'linux/amd64',
        keyId: 'cpamp-root-test',
        privateKey: pair.privateKeyPem,
        out: outDir,
      })
    ).toThrow(/Missing required path/);
  });

  it('excludes share/cpamp/provenance.json and fails closed when cpamp-root-2026-01 is signed with an unrelated key', () => {
    const { root: rootWithProv, prefix } = stageSlotRoot({
      layout: 'docker',
      version: 'v2.0.0-beta.2',
      platform: 'linux/amd64',
    });
    const { root: rootWithoutProv } = stageSlotRoot({
      layout: 'flat',
      version: 'v2.0.0-beta.2',
      platform: 'linux/amd64',
    });
    rmSync(path.join(rootWithoutProv, 'share/cpamp/provenance.json'));

    const dryPair = generateSlotKeypair('cpamp-dry-run-2026');
    const outWithProv = makeTempDir('cpamp-slot-with-prov-');
    const outWithoutProv = makeTempDir('cpamp-slot-without-prov-');

    const pkgWithProv = packageSlotArchive({
      root: rootWithProv,
      version: 'v2.0.0-beta.2',
      sourceCommit: SOURCE_COMMIT,
      platform: 'linux/amd64',
      keyId: 'cpamp-dry-run-2026',
      privateKey: dryPair.privateKeyPem,
      publicKeyHex: dryPair.publicKeyHex,
      out: outWithProv,
    });
    const pkgWithoutProv = packageSlotArchive({
      root: rootWithoutProv,
      version: 'v2.0.0-beta.2',
      sourceCommit: SOURCE_COMMIT,
      platform: 'linux/amd64',
      keyId: 'cpamp-dry-run-2026',
      privateKey: dryPair.privateKeyPem,
      publicKeyHex: dryPair.publicKeyHex,
      out: outWithoutProv,
    });

    for (const pkg of [pkgWithProv, pkgWithoutProv]) {
      const entries = parseTarArchive(readFileSync(pkg.archivePath));
      const names = entries.map((e) => e.name);
      expect(names).not.toContain('share/cpamp/provenance.json');
      expect(pkg.manifest.files['share/cpamp/provenance.json']).toBeUndefined();
      expect(existsSync(path.join(rootWithProv, prefix + 'share/cpamp/provenance.json'))).toBe(true);
    }

    // Dry-run ephemeral archive is rejected unless both dryRun and allowEphemeralSlotKey are set
    expect(() => extractSlotMetadataFromArchive(pkgWithProv.archivePath, 'v2.0.0-beta.2')).toThrow(
      /Unknown untrusted slot key_id/
    );
    expect(() =>
      extractSlotMetadataFromArchive(pkgWithProv.archivePath, 'v2.0.0-beta.2', { dryRun: true })
    ).toThrow(/Unknown untrusted slot key_id/);
    expect(() =>
      extractSlotMetadataFromArchive(pkgWithProv.archivePath, 'v2.0.0-beta.2', {
        dryRun: true,
        allowEphemeralSlotKey: true,
      })
    ).toThrow(/Ephemeral slot key verification requires --slot-public-key/);
    expect(
      extractSlotMetadataFromArchive(pkgWithProv.archivePath, 'v2.0.0-beta.2', {
        dryRun: true,
        allowEphemeralSlotKey: true,
        publicKeyHex: dryPair.publicKeyHex,
      })
    ).toMatchObject({
      name: 'cpamp-slot_v2.0.0-beta.2_linux_amd64.tar.gz',
      platform: 'linux/amd64',
    });
    const pubJsonPath = path.join(makeTempDir('cpamp-slot-pubjson-'), 'cpamp-dry-run-2026.pub.json');
    writeFileSync(pubJsonPath, JSON.stringify(dryPair.publicRecord, null, 2) + '\n');
    expect(resolveSlotPublicKeyHex(pubJsonPath)).toBe(dryPair.publicKeyHex);
    expect(resolveSlotPublicKeyHex(dryPair.publicKeyHex.toUpperCase())).toBe(dryPair.publicKeyHex);

    // Flipping a bit in the 64-byte Ed25519 signature of an ephemeral cpamp-dry-run-2026 archive must fail cryptographic verification
    const bitFlipExtractDir = makeTempDir('cpamp-slot-bitflip-extract-');
    const bitFlipOutDir = makeTempDir('cpamp-slot-bitflip-out-');
    execFileSync('tar', ['-xzf', pkgWithoutProv.archivePath, '-C', bitFlipExtractDir]);
    const bitFlipSigPath = path.join(bitFlipExtractDir, 'slot-manifest.json.sig');
    chmodSync(bitFlipSigPath, 0o644);
    const bitFlipSigObj = JSON.parse(readFileSync(bitFlipSigPath, 'utf8'));
    const bitFlipSigBytes = Buffer.from(bitFlipSigObj.signature, 'base64');
    bitFlipSigBytes[0] ^= 0x01;
    bitFlipSigObj.signature = bitFlipSigBytes.toString('base64');
    writeFileSync(bitFlipSigPath, JSON.stringify(bitFlipSigObj, null, 2) + '\n');
    chmodSync(bitFlipSigPath, 0o444);
    const bitFlipArchivePath = path.join(bitFlipOutDir, 'cpamp-slot_v2.0.0-beta.2_linux_amd64.tar.gz');
    execFileSync('tar', ['-czf', bitFlipArchivePath, '-C', bitFlipExtractDir, '.']);
    execFileSync('chmod', ['-R', 'u+rwX', bitFlipExtractDir]);
    expect(() =>
      extractSlotMetadataFromArchive(bitFlipArchivePath, 'v2.0.0-beta.2', {
        dryRun: true,
        allowEphemeralSlotKey: true,
        publicKeyHex: dryPair.publicKeyHex,
      })
    ).toThrow(/signature verification failed/);

    // Unknown non-dry-run key_id is rejected without explicit publicKeyHex even with allowEphemeralSlotKey
    const nonDryPair = generateSlotKeypair('cpamp-root-test');
    expect(() =>
      packageSlotArchive({
        root: rootWithoutProv,
        version: 'v2.0.0-beta.2',
        sourceCommit: SOURCE_COMMIT,
        platform: 'linux/amd64',
        keyId: 'cpamp-root-test',
        privateKey: nonDryPair.privateKeyPem,
        out: makeTempDir('cpamp-slot-untrusted-'),
      })
    ).toThrow(/Unknown untrusted slot key_id/);

    // Production key_id signed with an unrelated Ed25519 private key must fail closed everywhere
    const unrelatedPair = generateSlotKeypair('cpamp-root-2026-01');
    expect(() =>
      packageSlotArchive({
        root: rootWithoutProv,
        version: 'v2.0.0-beta.2',
        sourceCommit: SOURCE_COMMIT,
        platform: 'linux/amd64',
        keyId: 'cpamp-root-2026-01',
        privateKey: unrelatedPair.privateKeyPem,
        out: makeTempDir('cpamp-slot-bad-root-1-'),
      })
    ).toThrow(/signature verification failed/);
    expect(() =>
      packageSlotArchive({
        root: rootWithoutProv,
        version: 'v2.0.0-beta.2',
        sourceCommit: SOURCE_COMMIT,
        platform: 'linux/amd64',
        keyId: 'cpamp-root-2026-01',
        privateKey: unrelatedPair.privateKeyPem,
        publicKeyHex: unrelatedPair.publicKeyHex,
        out: makeTempDir('cpamp-slot-bad-root-2-'),
      })
    ).toThrow(/does not match trusted slot keyring/);

    // Craft an archive claiming cpamp-root-2026-01 signed with an unrelated key and verify extractSlotMetadataFromArchive rejects it even in dryRun + allowEphemeralSlotKey
    const forgedDir = makeTempDir('cpamp-slot-forged-');
    const extractDir = makeTempDir('cpamp-slot-extract-');
    execFileSync('tar', ['-xzf', pkgWithoutProv.archivePath, '-C', extractDir]);
    const manifestPath = path.join(extractDir, 'slot-manifest.json');
    const sigPath = path.join(extractDir, 'slot-manifest.json.sig');
    chmodSync(manifestPath, 0o644);
    chmodSync(sigPath, 0o644);
    const manifestObj = JSON.parse(readFileSync(manifestPath, 'utf8'));
    manifestObj.key_id = 'cpamp-root-2026-01';
    const forgedManifestText = JSON.stringify(manifestObj, null, 2) + '\n';
    writeFileSync(manifestPath, forgedManifestText);
    const forgedSig = signSlotManifestBytes(
      Buffer.from(forgedManifestText, 'utf8'),
      unrelatedPair.privateKeyPem,
      'cpamp-root-2026-01'
    );
    writeFileSync(sigPath, JSON.stringify(forgedSig, null, 2) + '\n');
    chmodSync(manifestPath, 0o444);
    chmodSync(sigPath, 0o444);
    const forgedArchivePath = path.join(forgedDir, 'cpamp-slot_v2.0.0-beta.2_linux_amd64.tar.gz');
    execFileSync('tar', ['-czf', forgedArchivePath, '-C', extractDir, '.']);

    expect(() => extractSlotMetadataFromArchive(forgedArchivePath, 'v2.0.0-beta.2')).toThrow(
      /signature verification failed/
    );
    expect(() =>
      extractSlotMetadataFromArchive(forgedArchivePath, 'v2.0.0-beta.2', {
        dryRun: true,
        allowEphemeralSlotKey: true,
      })
    ).toThrow(/signature verification failed/);
  });

  it('verifies tar member paths, modes, sizes, hashes, and required entries against signed slot-manifest.json', () => {
    const buildUstarHeader = ({ name, typeflag = '0', mode = 0o444, size = 0, linkname = '' }) => {
      const hdr = Buffer.alloc(512, 0);
      Buffer.from(name, 'utf8').copy(hdr, 0, 0, Math.min(Buffer.byteLength(name, 'utf8'), 100));
      Buffer.from(`${mode.toString(8).padStart(7, '0')}\0`, 'ascii').copy(hdr, 100);
      Buffer.from('0000000\0', 'ascii').copy(hdr, 108);
      Buffer.from('0000000\0', 'ascii').copy(hdr, 116);
      Buffer.from(`${size.toString(8).padStart(11, '0')}\0`, 'ascii').copy(hdr, 124);
      Buffer.from('00000000000\0', 'ascii').copy(hdr, 136);
      hdr.fill(0x20, 148, 156);
      hdr.write(typeflag, 156, 1, 'ascii');
      if (linkname) {
        Buffer.from(linkname, 'utf8').copy(
          hdr,
          157,
          0,
          Math.min(Buffer.byteLength(linkname, 'utf8'), 100)
        );
      }
      Buffer.from('ustar\0', 'ascii').copy(hdr, 257);
      Buffer.from('00', 'ascii').copy(hdr, 263);
      let sum = 0;
      for (const byte of hdr) sum += byte;
      Buffer.from(`${sum.toString(8).padStart(6, '0')}\0 `, 'ascii').copy(hdr, 148);
      return hdr;
    };

    const packUstarRecords = (records) => {
      const chunks = [];
      for (const rec of records) {
        const body = rec.body ? Buffer.from(rec.body) : Buffer.alloc(0);
        chunks.push(
          buildUstarHeader({
            name: rec.name,
            typeflag: rec.typeflag ?? '0',
            mode: rec.mode ?? (rec.typeflag === '5' ? 0o755 : 0o444),
            size: rec.typeflag === '5' ? 0 : body.length,
            linkname: rec.linkname ?? '',
          })
        );
        if (body.length > 0) {
          chunks.push(body);
          const rem = body.length % 512;
          if (rem !== 0) chunks.push(Buffer.alloc(512 - rem, 0));
        }
      }
      chunks.push(Buffer.alloc(1024, 0));
      return gzipSync(Buffer.concat(chunks));
    };

    const { root } = stageSlotRoot({
      layout: 'flat',
      version: 'v2.0.0-beta.2',
      platform: 'linux/amd64',
    });
    rmSync(path.join(root, 'share/cpamp/provenance.json'));
    const dryPair = generateSlotKeypair('cpamp-dry-run-2026');
    const pkg = packageSlotArchive({
      root,
      version: 'v2.0.0-beta.2',
      sourceCommit: SOURCE_COMMIT,
      platform: 'linux/amd64',
      keyId: 'cpamp-dry-run-2026',
      privateKey: dryPair.privateKeyPem,
      publicKeyHex: dryPair.publicKeyHex,
      out: makeTempDir('cpamp-slot-member-base-'),
    });
    const verifyOpts = {
      dryRun: true,
      allowEphemeralSlotKey: true,
      publicKeyHex: dryPair.publicKeyHex,
    };
    const baseEntries = parseTarArchive(readFileSync(pkg.archivePath));
    const baseRecords = baseEntries.map((entry) => ({
      name: entry.typeflag === '5' ? `${entry.name.replace(/\/$/, '')}/` : entry.name,
      typeflag: entry.typeflag,
      mode: entry.mode,
      body: entry.typeflag === '5' ? Buffer.alloc(0) : entry.body,
    }));

    const writeArchive = (suffix, gzBytes) => {
      const dir = makeTempDir(`cpamp-slot-member-${suffix}-`);
      const p = path.join(dir, 'cpamp-slot_v2.0.0-beta.2_linux_amd64.tar.gz');
      writeFileSync(p, gzBytes);
      return p;
    };

    // 1. 1-byte modification inside bin/cpa-manager-plus without re-signing fails closed
    const rawTar = Buffer.from(gunzipSync(readFileSync(pkg.archivePath)));
    let offset = 0;
    let flipped = false;
    while (offset + 512 <= rawTar.length) {
      const hdr = rawTar.subarray(offset, offset + 512);
      if (hdr.every((b) => b === 0)) break;
      const name = hdr.subarray(0, 100).toString('utf8').replace(/\0.*$/, '');
      const size = Number.parseInt(hdr.subarray(124, 136).toString('utf8').replace(/\0.*$/, '').trim() || '0', 8);
      const dataStart = offset + 512;
      if (name === 'bin/cpa-manager-plus') {
        rawTar[dataStart] ^= 0x01;
        flipped = true;
        break;
      }
      offset = dataStart + Math.ceil(size / 512) * 512;
    }
    expect(flipped).toBe(true);
    expect(() =>
      extractSlotMetadataFromArchive(writeArchive('one-byte', gzipSync(rawTar)), 'v2.0.0-beta.2', verifyOpts)
    ).toThrow(/sha256 mismatch/);

    // 2. Size mismatch on manifested member fails closed
    const sizeMutated = baseRecords.map((r) =>
      r.name === 'bin/cpa-manager-plus' ? { ...r, body: Buffer.concat([r.body, Buffer.from('x')]) } : r
    );
    expect(() =>
      extractSlotMetadataFromArchive(writeArchive('size', packUstarRecords(sizeMutated)), 'v2.0.0-beta.2', verifyOpts)
    ).toThrow(/size mismatch/);

    // 3. Mode mismatch on manifested member fails closed
    const modeMutated = baseRecords.map((r) =>
      r.name === 'bin/cpa-manager-plus' ? { ...r, mode: 0o644 } : r
    );
    expect(() =>
      extractSlotMetadataFromArchive(writeArchive('mode', packUstarRecords(modeMutated)), 'v2.0.0-beta.2', verifyOpts)
    ).toThrow(/mode mismatch/);

    // 4. Unmanifested extra file fails closed
    const extraFile = [
      ...baseRecords,
      { name: 'bin/unlisted-tool', typeflag: '0', mode: 0o555, body: Buffer.from('extra\n') },
    ];
    expect(() =>
      extractSlotMetadataFromArchive(writeArchive('extra', packUstarRecords(extraFile)), 'v2.0.0-beta.2', verifyOpts)
    ).toThrow(/unmanifested file bin\/unlisted-tool/);

    // 5. Missing manifested file fails closed
    const missingFile = baseRecords.filter((r) => r.name !== 'share/licenses/cpamp/LICENSE');
    expect(() =>
      extractSlotMetadataFromArchive(writeArchive('missing', packUstarRecords(missingFile)), 'v2.0.0-beta.2', verifyOpts)
    ).toThrow(/missing manifested file share\/licenses\/cpamp\/LICENSE/);

    // 6. Duplicate path fails closed
    const duplicateFile = [
      ...baseRecords,
      baseRecords.find((r) => r.name === 'bin/cpa-manager-plus'),
    ];
    expect(() =>
      extractSlotMetadataFromArchive(writeArchive('dup', packUstarRecords(duplicateFile)), 'v2.0.0-beta.2', verifyOpts)
    ).toThrow(/Duplicate tar entry path/);

    // 7. Non-regular entry (symlink) fails closed
    const symlinkArchive = [
      ...baseRecords,
      { name: 'bin/evil-link', typeflag: '2', mode: 0o777, linkname: '/etc/passwd' },
    ];
    expect(() =>
      extractSlotMetadataFromArchive(writeArchive('symlink', packUstarRecords(symlinkArchive)), 'v2.0.0-beta.2', verifyOpts)
    ).toThrow(/forbidden non-regular tar entry/);

    // 8. Traversal, leading slash, ./ prefix, and unmanifested directory fail closed
    for (const [label, badName] of [
      ['traversal', 'share/../evil'],
      ['leading-slash', '/etc/passwd'],
    ]) {
      const badPathArchive = [
        ...baseRecords,
        { name: badName, typeflag: '0', mode: 0o444, body: Buffer.from('x') },
      ];
      expect(() =>
        extractSlotMetadataFromArchive(
          writeArchive(label, packUstarRecords(badPathArchive)),
          'v2.0.0-beta.2',
          verifyOpts
        )
      ).toThrow(/Unsafe tar entry path|must not start with \//);
    }

    const dotSlashRenamed = baseRecords.map((r) =>
      r.name === 'bin/cpa-manager-plus' ? { ...r, name: './bin/cpa-manager-plus' } : r
    );
    expect(() =>
      extractSlotMetadataFromArchive(
        writeArchive('dot-slash', packUstarRecords(dotSlashRenamed)),
        'v2.0.0-beta.2',
        verifyOpts
      )
    ).toThrow(/forbidden \.\/ entry prefix/);

    const unlistedDir = [
      ...baseRecords,
      { name: 'extra/', typeflag: '5', mode: 0o755 },
    ];
    expect(() =>
      extractSlotMetadataFromArchive(
        writeArchive('unlisted-dir', packUstarRecords(unlistedDir)),
        'v2.0.0-beta.2',
        verifyOpts
      )
    ).toThrow(/unmanifested directory extra/);

    // 9. Signed manifest omitting one of the 8 required slot entries fails closed
    const incompleteManifest = JSON.parse(
      baseRecords.find((r) => r.name === 'slot-manifest.json').body.toString('utf8')
    );
    delete incompleteManifest.files['share/cpamp/runtime/config.seed.yaml'];
    const incompleteManifestText = JSON.stringify(incompleteManifest, null, 2) + '\n';
    const incompleteManifestBytes = Buffer.from(incompleteManifestText, 'utf8');
    const incompleteSigBytes = Buffer.from(
      JSON.stringify(
        signSlotManifestBytes(incompleteManifestBytes, dryPair.privateKeyPem, 'cpamp-dry-run-2026'),
        null,
        2
      ) + '\n',
      'utf8'
    );
    const incompleteRecords = baseRecords
      .filter((r) => r.name !== 'share/cpamp/runtime/config.seed.yaml')
      .map((r) =>
        r.name === 'slot-manifest.json'
          ? { ...r, body: incompleteManifestBytes }
          : r.name === 'slot-manifest.json.sig'
            ? { ...r, body: incompleteSigBytes }
            : r
      );
    expect(() =>
      extractSlotMetadataFromArchive(
        writeArchive('missing-required', packUstarRecords(incompleteRecords)),
        'v2.0.0-beta.2',
        verifyOpts
      )
    ).toThrow(/missing required entry share\/cpamp\/runtime\/config\.seed\.yaml/);
  });

  it('validates tar header checksums (octal, signed, base-256) and fails closed on corrupted header blocks', () => {
    const { root } = stageSlotRoot({
      layout: 'flat',
      version: 'v2.0.0-beta.2',
      platform: 'linux/amd64',
    });
    rmSync(path.join(root, 'share/cpamp/provenance.json'));
    const dryPair = generateSlotKeypair('cpamp-dry-run-2026');
    const pkg = packageSlotArchive({
      root,
      version: 'v2.0.0-beta.2',
      sourceCommit: SOURCE_COMMIT,
      platform: 'linux/amd64',
      keyId: 'cpamp-dry-run-2026',
      privateKey: dryPair.privateKeyPem,
      publicKeyHex: dryPair.publicKeyHex,
      out: makeTempDir('cpamp-slot-chksum-base-'),
    });
    const verifyOpts = {
      dryRun: true,
      allowEphemeralSlotKey: true,
      publicKeyHex: dryPair.publicKeyHex,
    };
    const writeArchive = (suffix, gzBytes) => {
      const dir = makeTempDir(`cpamp-slot-chksum-${suffix}-`);
      const archivePath = path.join(dir, 'cpamp-slot_v2.0.0-beta.2_linux_amd64.tar.gz');
      writeFileSync(archivePath, gzBytes);
      return archivePath;
    };
    const findEntryHeaderOffset = (tarBuf, targetName) => {
      let off = 0;
      let paxPath;
      while (off + 512 <= tarBuf.length) {
        const hdr = tarBuf.subarray(off, off + 512);
        if (hdr.every((b) => b === 0)) break;
        const name = hdr.subarray(0, 100).toString('utf8').replace(/\0.*$/, '');
        const prefix = hdr.subarray(345, 500).toString('utf8').replace(/\0.*$/, '');
        const typeflag = hdr.subarray(156, 157).toString('utf8') || '0';
        const size = Number.parseInt(
          hdr.subarray(124, 136).toString('utf8').replace(/\0.*$/, '').trim() || '0',
          8
        );
        const dataStart = off + 512;
        if (typeflag === 'x') {
          const text = tarBuf.subarray(dataStart, dataStart + size).toString('utf8');
          for (const line of text.split('\n')) {
            const match = /^\d+ path=(.*)$/.exec(line);
            if (match) paxPath = match[1];
          }
          off = dataStart + Math.ceil(size / 512) * 512;
          continue;
        }
        const resolvedName = paxPath ?? (prefix ? `${prefix}/${name}` : name);
        paxPath = undefined;
        if (resolvedName === targetName) return off;
        off = dataStart + Math.ceil(size / 512) * 512;
      }
      return -1;
    };
    const computeBlockChecksums = (block) => {
      let unsignedSum = 0;
      let signedSum = 0;
      for (let i = 0; i < 512; i += 1) {
        const byte = i >= 148 && i < 156 ? 0x20 : block[i];
        unsignedSum += byte;
        signedSum += byte >= 0x80 ? byte - 0x100 : byte;
      }
      return { unsignedSum, signedSum };
    };
    const buildRawHeader = ({ name, typeflag = '0', mode = 0o444, size = 0 }) => {
      const hdr = Buffer.alloc(512, 0);
      Buffer.from(name, 'utf8').copy(hdr, 0, 0, Math.min(Buffer.byteLength(name, 'utf8'), 100));
      Buffer.from(`${mode.toString(8).padStart(7, '0')}\0`, 'ascii').copy(hdr, 100);
      Buffer.from('0000000\0', 'ascii').copy(hdr, 108);
      Buffer.from('0000000\0', 'ascii').copy(hdr, 116);
      Buffer.from(`${size.toString(8).padStart(11, '0')}\0`, 'ascii').copy(hdr, 124);
      Buffer.from('00000000000\0', 'ascii').copy(hdr, 136);
      hdr.fill(0x20, 148, 156);
      hdr.write(typeflag, 156, 1, 'ascii');
      Buffer.from('ustar\0', 'ascii').copy(hdr, 257);
      Buffer.from('00', 'ascii').copy(hdr, 263);
      const { unsignedSum } = computeBlockChecksums(hdr);
      Buffer.from(`${unsignedSum.toString(8).padStart(6, '0')}\0 `, 'ascii').copy(hdr, 148);
      return hdr;
    };

    const baseTar = gunzipSync(readFileSync(pkg.archivePath));
    const targetOffset = findEntryHeaderOffset(baseTar, 'bin/cpa-manager-plus');
    expect(targetOffset).toBeGreaterThanOrEqual(0);

    // 1. Altering 1 octal digit in checksum field (offset 148..154) without touching payload or manifest(.sig) fails closed
    const singleDigitTar = Buffer.from(baseTar);
    let octalIdx = -1;
    for (let i = 154; i >= 148; i -= 1) {
      if (singleDigitTar[targetOffset + i] >= 0x30 && singleDigitTar[targetOffset + i] <= 0x37) {
        octalIdx = i;
        break;
      }
    }
    expect(octalIdx).toBeGreaterThanOrEqual(148);
    expect(octalIdx).toBeLessThanOrEqual(154);
    singleDigitTar[targetOffset + octalIdx] ^= 0x01;
    expect(() =>
      extractSlotMetadataFromArchive(
        writeArchive('single-octal', gzipSync(singleDigitTar)),
        'v2.0.0-beta.2',
        verifyOpts
      )
    ).toThrow(/invalid tar header checksum/);

    // 2. Altering a header field without updating checksum fails closed
    const mutatedHeaderTar = Buffer.from(baseTar);
    mutatedHeaderTar[targetOffset + 108] = 0x31; // uid '0' -> '1'
    expect(() =>
      extractSlotMetadataFromArchive(
        writeArchive('mutated-uid', gzipSync(mutatedHeaderTar)),
        'v2.0.0-beta.2',
        verifyOpts
      )
    ).toThrow(/invalid tar header checksum/);

    // 3. Empty or malformed checksum fields (non-octal, embedded space, sign, trailing garbage, bit-7 set) fail closed
    for (const [label, chksumBytes] of [
      ['spaces', Buffer.from('        ', 'ascii')],
      ['nuls', Buffer.alloc(8, 0)],
      ['digit-8', Buffer.from('012348\0 ', 'ascii')],
      ['inner-space', Buffer.from('012 45\0 ', 'ascii')],
      ['plus-sign', Buffer.from('+01234\0 ', 'ascii')],
      ['garbage-after-nul', Buffer.from('012345\0X', 'ascii')],
    ]) {
      const badFieldTar = Buffer.from(baseTar);
      chksumBytes.copy(badFieldTar, targetOffset + 148);
      expect(() =>
        extractSlotMetadataFromArchive(
          writeArchive(label, gzipSync(badFieldTar)),
          'v2.0.0-beta.2',
          verifyOpts
        )
      ).toThrow(/invalid tar header checksum/);
    }

    const bit7Tar = Buffer.from(baseTar);
    bit7Tar[targetOffset + 148] |= 0x80;
    expect(() =>
      extractSlotMetadataFromArchive(
        writeArchive('bit7-corrupt', gzipSync(bit7Tar)),
        'v2.0.0-beta.2',
        verifyOpts
      )
    ).toThrow(/invalid tar header checksum/);

    // 4. Signed (Sun int8) octal header checksums match Go archive/tar, while base-256 checksum fields are rejected
    const signedTar = Buffer.from(baseTar);
    signedTar[targetOffset + 265] = 0x80; // non-ASCII byte in ignored uname field
    const { signedSum } = computeBlockChecksums(signedTar.subarray(targetOffset, targetOffset + 512));
    expect(signedSum).toBeGreaterThan(0);
    Buffer.from(`${signedSum.toString(8).padStart(6, '0')}\0 `, 'ascii').copy(signedTar, targetOffset + 148);
    expect(
      extractSlotMetadataFromArchive(
        writeArchive('signed-sum', gzipSync(signedTar)),
        'v2.0.0-beta.2',
        verifyOpts
      ).name
    ).toBe('cpamp-slot_v2.0.0-beta.2_linux_amd64.tar.gz');

    const base256Tar = Buffer.from(baseTar);
    const { unsignedSum: b256Sum } = computeBlockChecksums(
      base256Tar.subarray(targetOffset, targetOffset + 512)
    );
    const b256Field = Buffer.alloc(8, 0);
    b256Field[0] = 0x80;
    b256Field.writeUIntBE(b256Sum, 2, 6);
    b256Field.copy(base256Tar, targetOffset + 148);
    expect(() =>
      extractSlotMetadataFromArchive(
        writeArchive('base256-checksum-rejected', gzipSync(base256Tar)),
        'v2.0.0-beta.2',
        verifyOpts
      )
    ).toThrow(/invalid tar header checksum/);
    // 5. PAX extended header ('x') and global PAX header ('g') must pass checksum before PAX/typeflag handling
    const paxKv = 'path=share/licenses/cpamp/LICENSE\n';
    let paxLen = Buffer.byteLength(paxKv, 'utf8') + 2;
    while (`${paxLen} ${paxKv}`.length !== paxLen) {
      paxLen = Buffer.byteLength(`${paxLen} ${paxKv}`, 'utf8');
    }
    const paxRecordBody = Buffer.from(`${paxLen} ${paxKv}`, 'utf8');
    const paxHeader = buildRawHeader({
      name: 'PaxHeaders.0/license',
      typeflag: 'x',
      mode: 0o644,
      size: paxRecordBody.length,
    });
    const paxPadLen = (512 - (paxRecordBody.length % 512)) % 512;
    const paxPadded = Buffer.concat([paxHeader, paxRecordBody, Buffer.alloc(paxPadLen, 0)]);
    const licenseOffset = findEntryHeaderOffset(baseTar, 'share/licenses/cpamp/LICENSE');
    expect(licenseOffset).toBeGreaterThanOrEqual(0);
    const paxValidTar = Buffer.concat([
      baseTar.subarray(0, licenseOffset),
      paxPadded,
      baseTar.subarray(licenseOffset),
    ]);
    expect(
      extractSlotMetadataFromArchive(
        writeArchive('pax-valid', gzipSync(paxValidTar)),
        'v2.0.0-beta.2',
        verifyOpts
      ).name
    ).toBe('cpamp-slot_v2.0.0-beta.2_linux_amd64.tar.gz');

    // Corrupt the 'x' header checksum AND its PAX body: checksum failure must trigger first
    const paxBadChecksumTar = Buffer.from(paxValidTar);
    Buffer.from('malformed', 'ascii').copy(paxBadChecksumTar, licenseOffset + 512);
    paxBadChecksumTar[licenseOffset + 153] ^= 0x01;
    expect(() =>
      extractSlotMetadataFromArchive(
        writeArchive('pax-bad-chksum', gzipSync(paxBadChecksumTar)),
        'v2.0.0-beta.2',
        verifyOpts
      )
    ).toThrow(/invalid tar header checksum/);
    const globalPaxHeader = buildRawHeader({
      name: 'pax_global_header',
      typeflag: 'g',
      mode: 0o644,
      size: 0,
    });
    const globalPaxValidTar = Buffer.concat([globalPaxHeader, baseTar]);
    expect(() =>
      extractSlotMetadataFromArchive(
        writeArchive('pax-global-valid-chksum', gzipSync(globalPaxValidTar)),
        'v2.0.0-beta.2',
        verifyOpts
      )
    ).toThrow(/forbidden non-regular tar entry \("g"\)/);

    const globalPaxBadTar = Buffer.from(globalPaxValidTar);
    globalPaxBadTar[153] ^= 0x01;
    expect(() =>
      extractSlotMetadataFromArchive(
        writeArchive('pax-global-bad-chksum', gzipSync(globalPaxBadTar)),
        'v2.0.0-beta.2',
        verifyOpts
      )
    ).toThrow(/invalid tar header checksum/);
  });
});
