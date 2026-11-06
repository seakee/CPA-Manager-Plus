import { createHash } from 'node:crypto';
import { existsSync, readdirSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

const EXPECTED = {
  repo: 'router-for-me/CLIProxyAPI',
  version: '8.0.55',
  tag: 'v8.0.55',
  amd64: {
    asset: 'CLIProxyAPI_8.0.55_linux_amd64.tar.gz',
    archiveSha256: '4cd593251db85e50c820f42e6ea5cecdd05f902201d3dbdb025671d4f306a123',
    executableSha256: 'e48c85c75a19adae186cbee590f6e6143fd8085417d8066b545e8533877175f7',
    archiveSize: 25229124,
  },
  arm64: {
    asset: 'CLIProxyAPI_8.0.55_linux_aarch64.tar.gz',
    archiveSha256: '62c136d7c6ee03cedebaad4be3e7fb9bcf14d095c96597fd92e68554c2a785c5',
    executableSha256: '4a5b43cc3700d36c72b7c1953e9b11eca0888c13b789ea2f99471117504a7d5d',
  },
  cpaFileSha256: {
    'v8.go': 'a95c04d680ed32ae7beae0a617f16af94b824f82da46543070381d0f37dff26e',
    'v8_adoption.go': 'ee44597642ad072b1ef97b9ecbdea505cd5fc84b58824cf122a005145b0aaed0',
    'legacy_adoption.go': 'ca6fdf41b7ac7735768502cd1fadd367c7304bcf50ce25b54850309181bc68ed',
    'adoption_ref.go': 'd89dfd3de89af2fdbda76c9c6dc89adf7e2c78066a54ef03008f8d7b977c46e5',
  },
  freshStrategyFileSha256: {
    'production.go': 'f89f964f664fddb4b0c88067ead4e35dd177c7a48994f605e37099463cae88c2',
    'service.go': '850c5fea3de69c03eb35cac1d6b39f8f7125f6c8e06f1af5141264e5ca90549b',
    'adoption.go': '1c594a3e031c97d130777476e6970b1945e53dca02e9ad9a52e76c4762882e68',
    'legacy_transition.go': '140152beaaa7112674782ad35810a4d0dda1eeda0e94652487c786f91d604320',
    'adoption_resource.go': '0a3e565bcf80646e7b437ee4d7d5974b12e511f51b8f8ad35fbf2c7473879dbe',
  },
  adoptionUniverseDigest: '5bc717adc4673e700affc7c954705b713deaa650487138d5f165529b23f48576',
  safeReadImplementationSha256: '75b509c09585da6a89dcd2594532ca9b4d810ec8f8a20b5da425fbb9b5b7fcf1',
  freshStrategyImplementationSha256:
    '1c1a72c7c3f280d7379a5bb0eaf87141bfeaf232365d8a5a144da426b6ae6c85',
};

function readText(relPath) {
  return readFileSync(path.join(ROOT, relPath), 'utf8');
}

function readBytes(relPath) {
  return readFileSync(path.join(ROOT, relPath));
}

function sha256Hex(input) {
  return createHash('sha256').update(input).digest('hex');
}

function extractDockerfileV8Stage() {
  const dockerfile = readText('Dockerfile.runtime');
  const match = dockerfile.match(
    /FROM[^\n]*AS cpa-v8-download([\s\S]*?)FROM[^\n]*AS supervisor-build/
  );
  expect(match).not.toBeNull();
  return match[1];
}

function computeRuntimeDestinationImplementationPin() {
  const paths = [
    'apps/manager-server/internal/model/runtime.go',
    'apps/manager-server/internal/model/runtime_destination.go',
    'apps/manager-server/internal/service/runtime/destination.go',
    'apps/manager-server/internal/service/runtime/embedded.go',
    'apps/runtime-supervisor/cmd/cpamp-runtime-supervisor/runtime.go',
    'apps/runtime-supervisor/internal/lifecycle/destination.go',
    'apps/runtime-supervisor/internal/lifecycle/start.go',
    'apps/runtime-supervisor/internal/lifecycle/restart.go',
    'apps/runtime-supervisor/internal/lifecycle/recovery.go',
    'apps/runtime-supervisor/internal/protocol/destination.go',
    'apps/runtime-supervisor/internal/protocol/handler.go',
  ];
  const hash = createHash('sha256');
  for (const relPath of paths) {
    hash.update(`${relPath}\n`);
    hash.update(readBytes(relPath));
    hash.update('\n');
  }
  return hash.digest('hex');
}

function computeSafeReadImplementationSha256() {
  const schemaSrc = readText('apps/manager-server/internal/service/cpa/config_schema.go');
  const gatewayLeavesBlock = schemaSrc.match(/var gatewayLeaves = \[\]ConfigLeaf\{([\s\S]*?)\n\}/);
  expect(gatewayLeavesBlock).not.toBeNull();
  const leafPaths = [...gatewayLeavesBlock[1].matchAll(/\{Path:\s*"([^"]+)",\s*Type:/g)].map(
    (m) => m[1]
  );
  const familyPaths = [
    ...schemaSrc.matchAll(/\{Family:\s*"[^"]+",\s*Path:\s*"([^"]+)"/g),
  ].map((m) => m[1]);
  const providerSections = [
    'api-keys.gemini',
    'api-keys.interactions',
    'api-keys.vertex',
    'api-keys.codex',
    'api-keys.claude',
    'api-keys.xai',
    'api-keys.meta',
    'api-keys.openai-compatibility',
    'oauth.model-alias',
    'oauth.excluded-models',
    'oauth.settings',
    'oauth.request-scoped-errors',
  ];
  const uncopiedSourceKeys = ['oauth.auth-auto-refresh-workers', 'oauth.providers'];
  const items = [
    ...leafPaths,
    ...familyPaths,
    ...providerSections,
    ...uncopiedSourceKeys,
    'credential:<name>',
    'unknown_cpa_native_resource.<key>',
  ];
  const classify = (item) => {
    if (item.startsWith('credential:') || item.startsWith('credential-name-sha256:')) {
      return 'cpa-auth-files';
    }
    if (providerSections.includes(item)) {
      return 'provider-state-api-keys';
    }
    if (leafPaths.includes(item) || familyPaths.includes(item)) {
      return 'cpa-config-routing';
    }
    return 'cpa-config-unknown';
  };
  const pairs = items.map((item) => `${item}\x00${classify(item)}`).sort();
  const universeHash = createHash('sha256');
  for (const pair of pairs) {
    universeHash.update(pair);
    universeHash.update('\n');
  }
  const adoptionUniverseDigest = universeHash.digest('hex');

  const safeReadHash = createHash('sha256');
  for (const name of ['v8.go', 'v8_adoption.go', 'legacy_adoption.go', 'adoption_ref.go']) {
    safeReadHash.update(
      readBytes(path.posix.join('apps/manager-server/internal/service/cpa', name))
    );
  }
  safeReadHash.update(adoptionUniverseDigest);
  return {
    adoptionUniverseDigest,
    safeReadSha256: safeReadHash.digest('hex'),
  };
}

function computeFreshStrategyImplementationSha256() {
  const hash = createHash('sha256');
  for (const name of ['qualification.go', 'adoption.go', 'fresh.go']) {
    hash.update(
      readBytes(path.posix.join('apps/manager-server/internal/domain/bootstrap', name))
    );
  }
  return hash.digest('hex');
}

function computeConsumerImplementationDigest(safeReadSha256, freshStrategySha256, pinBytes) {
  const hash = createHash('sha256');
  for (const name of [
    'production.go',
    'service.go',
    'adoption.go',
    'legacy_transition.go',
    'adoption_resource.go',
  ]) {
    hash.update(
      readBytes(path.posix.join('apps/manager-server/internal/service/freshstrategy', name))
    );
  }
  hash.update(safeReadSha256);
  hash.update(freshStrategySha256);
  hash.update(pinBytes);
  return hash.digest('hex');
}

describe('CPA v8.0.55 pin consistency and NH2 network posture contract (§3 P10)', () => {
  it('Check 1: keeps amd64 and arm64 archive/executable SHA-256 pairs consistent across Dockerfile, CI verifiers, smoke docs, and workflow', () => {
    const v8Stage = extractDockerfileV8Stage();
    const amd64Match = v8Stage.match(
      /amd64\)\s*asset_arch="amd64";\s*\\\s*archive_sha256="([0-9a-f]{64})";\s*\\\s*executable_sha256="([0-9a-f]{64})"/
    );
    const arm64Match = v8Stage.match(
      /arm64\)\s*asset_arch="aarch64";\s*\\\s*archive_sha256="([0-9a-f]{64})";\s*\\\s*executable_sha256="([0-9a-f]{64})"/
    );
    expect(amd64Match?.[1]).toBe(EXPECTED.amd64.archiveSha256);
    expect(amd64Match?.[2]).toBe(EXPECTED.amd64.executableSha256);
    expect(arm64Match?.[1]).toBe(EXPECTED.arm64.archiveSha256);
    expect(arm64Match?.[2]).toBe(EXPECTED.arm64.executableSha256);

    const checkCandidate = readText('bin/ci/check-v2-private-candidate.py');
    const candAmd64 = checkCandidate.match(/"amd64":\s*"([0-9a-f]{64})"/);
    const candArm64 = checkCandidate.match(/"arm64":\s*"([0-9a-f]{64})"/);
    expect(candAmd64?.[1]).toBe(EXPECTED.amd64.executableSha256);
    expect(candArm64?.[1]).toBe(EXPECTED.arm64.executableSha256);

    const defaultSmokePy = readText('bin/ci/runtime-v8-default-smoke.py');
    const smokeAmd64 = defaultSmokePy.match(/"amd64":\s*"([0-9a-f]{64})"/);
    const smokeArm64 = defaultSmokePy.match(/"arm64":\s*"([0-9a-f]{64})"/);
    expect(smokeAmd64?.[1]).toBe(EXPECTED.amd64.executableSha256);
    expect(smokeArm64?.[1]).toBe(EXPECTED.arm64.executableSha256);

    const defaultSmokeMd = readText('bin/ci/runtime-v8-default-smoke.md');
    const mdAmd64 = defaultSmokeMd.match(
      /\|\s*linux\/amd64\s*\|\s*([0-9a-f]{64})\s*\|\s*([0-9a-f]{64})\s*\|/
    );
    const mdArm64 = defaultSmokeMd.match(
      /\|\s*linux\/arm64\s*\|\s*([0-9a-f]{64})\s*\|\s*([0-9a-f]{64})\s*\|/
    );
    expect(mdAmd64?.[1]).toBe(EXPECTED.amd64.archiveSha256);
    expect(mdAmd64?.[2]).toBe(EXPECTED.amd64.executableSha256);
    expect(mdArm64?.[1]).toBe(EXPECTED.arm64.archiveSha256);
    expect(mdArm64?.[2]).toBe(EXPECTED.arm64.executableSha256);

    const verifyEmbedded = readText('bin/release/verify-embedded-candidate.py');
    const embExec = verifyEmbedded.match(/CPA_SHA\s*=\s*"([0-9a-f]{64})"/);
    const embArch = verifyEmbedded.match(/CPA_ARCHIVE_SHA\s*=\s*"([0-9a-f]{64})"/);
    expect(embArch?.[1]).toBe(EXPECTED.amd64.archiveSha256);
    expect(embExec?.[1]).toBe(EXPECTED.amd64.executableSha256);

    const prCheck = readText('.github/workflows/pr-check.yml');
    const prCheckTarHash = prCheck.match(
      /printf\s+'%s\s+%s\\n'\s+([0-9a-f]{64})\s+"\$\{inputs\}\/v8\/cpa\.tar\.gz"/
    );
    const prCheckBinHash = prCheck.match(
      /printf\s+'%s\s+%s\\n'\s+([0-9a-f]{64})\s+"\$\{inputs\}\/v8\/cli-proxy-api"/
    );
    const prCheckTargetArchive = prCheck.match(/target_archive\s*=\s*'([0-9a-f]{64})'/);
    const prCheckTargetBinary = prCheck.match(/target_binary\s*=\s*'([0-9a-f]{64})'/);
    expect(prCheckTarHash?.[1]).toBe(EXPECTED.amd64.archiveSha256);
    expect(prCheckBinHash?.[1]).toBe(EXPECTED.amd64.executableSha256);
    expect(prCheckTargetArchive?.[1]).toBe(EXPECTED.amd64.archiveSha256);
    expect(prCheckTargetBinary?.[1]).toBe(EXPECTED.amd64.executableSha256);
  });

  it('Check 2: pins release coordinates to router-for-me/CLIProxyAPI v8.0.55 and rejects _no-plugin asset variants', () => {
    const v8Stage = extractDockerfileV8Stage();
    expect(v8Stage).toContain('asset_name="CLIProxyAPI_8.0.55_linux_${asset_arch}.tar.gz"');
    expect(v8Stage).toContain(
      'https://github.com/router-for-me/CLIProxyAPI/releases/download/v8.0.55/${asset_name}'
    );

    const prCheck = readText('.github/workflows/pr-check.yml');
    expect(prCheck).toContain(
      `https://github.com/${EXPECTED.repo}/releases/download/${EXPECTED.tag}/${EXPECTED.amd64.asset}`
    );

    const prepareArtifact = readText('bin/ci/prepare-cpa-v8-artifact.py');
    expect(prepareArtifact).toContain(`ASSET = "${EXPECTED.amd64.asset}"`);

    const qualManifest = JSON.parse(readText('tests/fixtures/cpa-v8-qualification/manifest.json'));
    expect(qualManifest.candidate?.version).toBe(EXPECTED.tag);
    expect(qualManifest.candidate?.assetName).toBe(EXPECTED.amd64.asset);
    expect(qualManifest.candidate?.declaredSha256).toBe(EXPECTED.amd64.archiveSha256);

    const validateEvidence = readText('bin/ci/validate-cpa-v8-evidence.mjs');
    expect(validateEvidence).toContain(`assetName: '${EXPECTED.amd64.asset}'`);

    for (const relPath of [
      'Dockerfile.runtime',
      '.github/workflows/pr-check.yml',
      'bin/ci/prepare-cpa-v8-artifact.py',
      'tests/fixtures/cpa-v8-qualification/manifest.json',
      'bin/ci/validate-cpa-v8-evidence.mjs',
    ]) {
      expect(readText(relPath)).not.toContain('_no-plugin');
    }
  });

  it('Check 3: maintains version 8.0.55 and artifact ID coherence across manifests, stage scripts, and smoke harnesses', () => {
    const expectedArtifactId = `sha256:${EXPECTED.amd64.executableSha256}`;

    const privateInitialKey = readText(
      'apps/manager-server/internal/service/cpa/private_initial_key.go'
    );
    expect(privateInitialKey).toContain(`privateCPAArtifact     = "${expectedArtifactId}"`);

    const privateAuthority = readText(
      'apps/runtime-supervisor/internal/privateconfig/authority.go'
    );
    expect(privateAuthority).toContain(`const Artifact = "${expectedArtifactId}"`);

    const v8Stage = extractDockerfileV8Stage();
    expect(v8Stage).toContain(
      '{"schemaVersion":1,"engine":"cpa","version":"8.0.55","artifactId":"sha256:%s"}'
    );

    const verifyEmbedded = readText('bin/release/verify-embedded-candidate.py');
    expect(verifyEmbedded).toContain('"version": "8.0.55", "artifactId": "sha256:" + CPA_SHA');
    expect(verifyEmbedded).toContain(
      '"version": "8.0.55", "dockerBuildPinArchiveSHA256": CPA_ARCHIVE_SHA'
    );
    expect(verifyEmbedded).toContain('official CPA v8.0.55 executable hash mismatch');

    const prepareArtifact = readText('bin/ci/prepare-cpa-v8-artifact.py');
    expect(prepareArtifact).toContain(`ASSET = "${EXPECTED.amd64.asset}"`);
    expect(prepareArtifact).toContain(`SHA256 = "${EXPECTED.amd64.archiveSha256}"`);
    expect(prepareArtifact).toContain(`SIZE = ${EXPECTED.amd64.archiveSize}`);

    const checkCandidate = readText('bin/ci/check-v2-private-candidate.py');
    expect(checkCandidate).toContain('CPA_VERSION = "8.0.55"');

    const defaultSmoke = readText('bin/ci/runtime-v8-default-smoke.py');
    expect(defaultSmoke).toContain('initial["activeGatewayArtifact"]["version"] == "8.0.55"');
    expect(defaultSmoke).toContain(
      '{"schemaVersion": 1, "engine": "cpa", "version": "8.0.55", "artifactId": "sha256:"+pin}'
    );

    const bundleSmoke = readText('bin/ci/runtime-v8-bundle-smoke.py');
    expect(bundleSmoke).toContain(`PIN = "${EXPECTED.amd64.executableSha256}"`);
    expect(bundleSmoke).toContain('"version": "8.0.55"');
    expect(bundleSmoke).toContain('assert version == "8.0.55"');

    const prCheck = readText('.github/workflows/pr-check.yml');
    expect(prCheck).toContain(EXPECTED.tag);
    expect(prCheck).toContain(EXPECTED.amd64.asset);
  });

  it('Check 4: aligns both live runtime version equality gates with 8.0.55', () => {
    const freshKeyApply = readText('apps/manager-server/internal/service/freshkeyapply/service.go');
    const supervisorGate = readText(
      'apps/runtime-supervisor/cmd/cpamp-runtime-supervisor/private_runtime.go'
    );

    const managerGateMatch = freshKeyApply.match(
      /v\.ActiveGatewayArtifact\.Version\s*!=\s*"([^"]+)"/
    );
    const supervisorGateMatch = supervisorGate.match(/o\.Version\s*!=\s*"([^"]+)"/);

    expect(managerGateMatch?.[1]).toBe(EXPECTED.version);
    expect(supervisorGateMatch?.[1]).toBe(EXPECTED.version);
  });

  it('Check 5: binds qualification/consumer.json artifactId, archiveSha256, and binarySha256 to the v8.0.55 amd64 pair', () => {
    const consumer = JSON.parse(
      readText('apps/manager-server/internal/service/freshstrategy/qualification/consumer.json')
    );
    expect(consumer.archiveSha256).toBe(EXPECTED.amd64.archiveSha256);
    expect(consumer.binarySha256).toBe(EXPECTED.amd64.executableSha256);
    expect(consumer.artifactId).toBe(`sha256:${EXPECTED.amd64.executableSha256}`);
  });

  it('Check 6: enforces the digest ledger across safe-read, freshstrategy, runtime destination pin, and consumer.json', () => {
    for (const [name, expectedSha] of Object.entries(EXPECTED.cpaFileSha256)) {
      const actualSha = sha256Hex(
        readBytes(path.posix.join('apps/manager-server/internal/service/cpa', name))
      );
      expect(actualSha, `cpa/${name} byte digest must not drift`).toBe(expectedSha);
    }

    for (const [name, expectedSha] of Object.entries(EXPECTED.freshStrategyFileSha256)) {
      const actualSha = sha256Hex(
        readBytes(path.posix.join('apps/manager-server/internal/service/freshstrategy', name))
      );
      expect(actualSha, `freshstrategy/${name} byte digest must not drift`).toBe(expectedSha);
    }

    const { adoptionUniverseDigest, safeReadSha256 } = computeSafeReadImplementationSha256();
    expect(adoptionUniverseDigest).toBe(EXPECTED.adoptionUniverseDigest);
    expect(safeReadSha256).toBe(EXPECTED.safeReadImplementationSha256);

    const freshStrategySha256 = computeFreshStrategyImplementationSha256();
    expect(freshStrategySha256).toBe(EXPECTED.freshStrategyImplementationSha256);

    const pinFileRel =
      'apps/manager-server/internal/service/freshstrategy/qualification/runtime_destination_implementation.sha256';
    const pinBytes = readBytes(pinFileRel);
    const pinText = pinBytes.toString('utf8').trim();
    const recomputedDestinationPin = computeRuntimeDestinationImplementationPin();
    expect(pinText).toBe(recomputedDestinationPin);

    const recomputedConsumerDigest = computeConsumerImplementationDigest(
      safeReadSha256,
      freshStrategySha256,
      pinBytes
    );
    const consumer = JSON.parse(
      readText('apps/manager-server/internal/service/freshstrategy/qualification/consumer.json')
    );
    expect(consumer.implementationSha256).toBe(recomputedConsumerDigest);
  });

  it('Check 7: enforces NH2 loopback opt-in posture in Supervisor and forbids CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK in Docker surfaces', () => {
    const rootEntries = readdirSync(ROOT);
    const dockerFiles = rootEntries.filter(
      (name) =>
        (name.startsWith('docker-compose') &&
          (name.endsWith('.yml') || name.endsWith('.yaml'))) ||
        name === 'Dockerfile' ||
        name.startsWith('Dockerfile.')
    );
    expect(dockerFiles.length).toBeGreaterThan(0);
    for (const file of dockerFiles) {
      expect(readText(file), `${file} must not define CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK`).not.toContain(
        'CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK'
      );
    }

    const supervisorMain = readText('apps/runtime-supervisor/cmd/cpamp-runtime-supervisor/main.go');
    expect(supervisorMain).toContain('CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK');
    expect(supervisorMain).toContain('CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK: must be 1 or true');

    const supervisorRuntime = readText(
      'apps/runtime-supervisor/cmd/cpamp-runtime-supervisor/runtime.go'
    );
    expect(supervisorRuntime).toContain('lifecycle.LoopbackListenHost(cfg.cpaAddr)');
    expect(supervisorRuntime).toContain(
      'CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK: CPAMP_RUNTIME_CPA_ADDR host must be 127.0.0.1, ::1, or localhost'
    );

    const destinationGo = readText('apps/runtime-supervisor/internal/lifecycle/destination.go');
    expect(destinationGo).toContain('func LoopbackListenHost(address string) (string, error)');
    expect(destinationGo).toContain('case d.listenLoopback:');
    expect(destinationGo).toContain('host, allowRemote = d.listenHost, false');

    const processGo = readText('apps/runtime-supervisor/internal/cpaprocess/process.go');
    expect(processGo).toContain('key == "MANAGEMENT_PASSWORD"');

    const launcherRel = 'bin/native/cpa-manager-plus-launcher';
    if (existsSync(path.join(ROOT, launcherRel))) {
      expect(readText(launcherRel)).toContain('CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK=1');
      expect(readText('bin/native/cpa-manager-plus.service')).toContain(
        'CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK=1'
      );
      expect(readText('bin/native/cpa-manager-plusctl.sh')).toContain(
        'CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK=1'
      );
    }
  });
});
