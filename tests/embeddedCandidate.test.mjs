import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync, renameSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import { generateReleaseInfo } from '../bin/release/generate-release-info.mjs';
import { verifyCandidate, verifyImage } from '../bin/release/publish-update-index.mjs';
import * as productionRendererModule from '../bin/release/render-embedded-compose.mjs';
import {
  assertCanonicalCpaArtifact,
  COMPOSE_FILENAME,
  EXPECTED_CPA_ARTIFACT_ID,
  composeYamlToRuntime12ConfigJson,
  EXPECTED_CPA_SHA256,
  PACKAGED_PATHS,
  PAIRING_FILENAME,
  renderEmbeddedReleaseArtifacts as publicRender,
  verifyEmbeddedReleaseArtifacts as publicVerify,
  resolveFullWebAndApiMetadata,
  renderEmbeddedComposeYaml,
  runCli as publicRunCli,
  validateCandidateEvidence as publicValidateCandidateEvidence,
  verifyOciArchiveAgainstRoleEvidence as publicVerifyOciArchiveAgainstRoleEvidence,
} from '../bin/release/render-embedded-compose.mjs';
import { expectedReleaseAssetNames } from '../bin/release/verify-published-release.mjs';
import { repository, validateInfo } from '../bin/release/update-contract.mjs';
describe('R1-1 local-only OCI candidate contracts', () => {
  it('keeps the no-push candidate builder syntactically valid', () => {
    execFileSync('bash', ['-n', 'bin/release/build-embedded-candidate.sh'], {
      cwd: new URL('..', import.meta.url),
      stdio: 'pipe',
    });
  });

  it('requires immutable platform manifests and rejects bad source, platform and byte identities', () => {
    execFileSync('python3', ['tests/embedded_candidate_contract_test.py'], {
      cwd: new URL('..', import.meta.url),
      env: { ...process.env, PYTHONDONTWRITEBYTECODE: '1' },
      stdio: 'pipe',
      timeout: 90_000,
    });
  }, 90_000);

  it('replays the strict published 1.x consumer without R1 fields or single-platform relaxation', () => {
    const tag = 'v1.14.7';
    // Captured from the real published update-channel catalog.
    const info = JSON.parse(readFileSync(new URL('./fixtures/release-v1.14.7-info.json', import.meta.url), 'utf8'));
    const sha = info.release.source_commit;
    expect(sha).toBe('64ce94e6aa1af691555429a3211e67c59c0480ad');
    expect(validateInfo(info, tag)).toBe(info);
    expect(info.schema_version).toBe(1);
    expect(info.distribution.native.assets).toHaveLength(6);
    expect(info.distribution.embedded).toBeUndefined();
    const names = expectedReleaseAssetNames(tag);
    expect(names).toHaveLength(9);
    const published = {
      tag_name: tag, draft: false, immutable: true, published_at: 'test',
      prerelease: false,
      assets: names.map((name) => ({
        name, state: 'uploaded', size: 1, digest: 'sha256:' + 'c'.repeat(64),
      })),
    };
    expect(() => verifyCandidate(published, info, sha)).not.toThrow();
    expect(() => verifyCandidate({ ...published, tag_name: 'v2.0.0' }, { ...info, release: { ...info.release, version: 'v2.0.0' } }, sha)).toThrow(/v2 Embedded/);
    const labels = {
      'org.opencontainers.image.revision': sha,
      'org.opencontainers.image.version': tag.slice(1),
      'org.opencontainers.image.source': repository,
    };
    const manifest = { digest: 'sha256:' + 'b'.repeat(64), manifests: [
      { platform: { os: 'linux', architecture: 'amd64' } },
      { platform: { os: 'linux', architecture: 'arm64' } },
    ] };
    const inspected = { manifest, image: {
      amd64: { config: { Labels: labels } },
      arm64: { config: { Labels: labels } },
    } };
    expect(verifyImage(inspected, info)).toBe(manifest.digest);
    expect(() => verifyImage(inspected, { ...info, release: { ...info.release, version: 'v2.0.0' } })).toThrow(/v2 Embedded/);
    expect(() => verifyImage({
      ...inspected, manifest: { ...manifest, manifests: [manifest.manifests[0]] },
    }, info)).toThrow();
  });
});

describe('R1-1 candidate local load contracts', () => {
  const builder = readFileSync(
    new URL('../bin/release/build-embedded-candidate.sh', import.meta.url),
    'utf8'
  );

  it('names every role and exports the engine-loadable transport in the same build invocation', () => {
    expect(builder).toContain(
      '--output "type=oci,name=cpamp-r1-$role:candidate,dest=$output/$role.oci.tar"'
    );
    expect(builder).toContain('--output "type=docker,dest=$output/$role.docker.tar"');
    expect(builder).toContain('--tag "cpamp-r1-$role:candidate"');
    // One role block holds both outputs, so the transport cannot be a rebuild.
    const argsBlock = builder.slice(
      builder.indexOf('  args=('),
      builder.indexOf('  if [[ -n "$target" ]]')
    );
    expect(argsBlock).toContain('type=oci,name=cpamp-r1-$role:candidate');
    expect(argsBlock).toContain('type=docker,dest=$output/$role.docker.tar');
    expect(builder.indexOf('type=docker,dest')).toBeLessThan(
      builder.indexOf('docker buildx build ${builder_args[@]+')
    );
  });

  it('refuses the docker driver and forwards the explicitly selected container builder', () => {
    expect(builder).toContain('builder_args=(--builder "$CPAMP_CANDIDATE_BUILDER")');
    expect(builder).toContain('docker buildx inspect "$CPAMP_CANDIDATE_BUILDER"');
    expect(builder).toContain("awk '/^Driver:/ {print $2; exit}'");
    expect(builder).toContain('cannot export an OCI layout');
    expect(builder).toContain('select a container builder with CPAMP_CANDIDATE_BUILDER=<name>');
    // Every role build carries the selection; the driver guard runs before it.
    expect(builder.indexOf('cannot export an OCI layout')).toBeLessThan(
      builder.indexOf('docker buildx build ${builder_args[@]+')
    );
  });

  it('keeps the load opt-in and verifies the loaded id against the verified evidence', () => {
    expect(builder).toContain('if [[ "${CPAMP_CANDIDATE_LOAD:-0}" == "1" ]]; then');
    expect(builder).toContain('docker load --input "$output/$role.docker.tar"');
    // The OCI archive is evidence only: the engine never loads it directly.
    expect(builder).not.toContain('docker load --input "$output/$role.oci.tar"');
    expect(builder).toContain(
      'actual_id="$(docker image inspect --format \'{{.Id}}\' "$reference" 2>/dev/null || true)"'
    );
    expect(builder).toContain('the build transport did not carry the reference');
    expect(builder).toContain('"$actual_id" != "$expected_id"');
    expect(builder).toContain('"images"][sys.argv[2]]["imageConfigDigest"]');
    expect(builder).toContain('load-evidence.json');
  });

  it('never rebuilds, pushes or tags a substitute candidate', () => {
    expect(builder).not.toContain('--load');
    expect(builder).not.toContain('--push');
    expect(builder).not.toContain('docker push');
    expect(builder).not.toContain('skopeo');
    expect(builder).not.toMatch(/^\s*docker tag /m);
  });
});

describe('Fresh New automated smoke contracts', () => {
  const smoke = readFileSync(
    new URL('../bin/ci/fresh-new-installation.mjs', import.meta.url),
    'utf8'
  );

  it('stays syntactically valid', () => {
    execFileSync('node', ['--check', 'bin/ci/fresh-new-installation.mjs'], {
      cwd: new URL('..', import.meta.url),
      stdio: 'pipe',
    });
  });

  it('asserts the Fresh liveness transition from 503 to 200', () => {
    expect(smoke).toMatch(
      /fetch\(origin \+ '\/health'\)\)\.status,\s*503,\s*'unclaimed Manager must report \/health 503'/
    );
    expect(smoke).toMatch(/takeoverHealth[\s\S]{0,400}status,\s*200,\s*'claimed Fresh takeover/);
    expect(smoke).toContain('"service":"cpa-manager-plus"');
  });

  it('drives the current Fresh setup wizard instead of a retired claim flow', () => {
    expect(smoke).toContain("getByTestId('fresh-setup-wizard')");
    expect(smoke).toContain("getByTestId('choose-new_managed')");
    expect(smoke).toContain("getByTestId('setup-execute')");
    expect(smoke).toContain("getByTestId('setup-refresh')");
    expect(smoke).toContain("getByTestId('enter-console')");
    for (const retired of [
      'bootstrap-operation-ready',
      'commit-admin-btn',
      'enable-console',
      'continue-console',
    ])
      expect(smoke).not.toContain(`getByTestId('${retired}')`);
  });

  // A smoke that drives a test id the shipped Full Web no longer renders fails
  // only against real images. Keep that drift an offline contract failure.
  it('only drives test ids the Full Web source actually renders', () => {
    const ids = [...smoke.matchAll(/getByTestId\('([a-z0-9-]+)'\)/g)].map((match) => match[1]);
    expect(ids.length).toBeGreaterThan(10);
    const files = [];
    const walk = (directory) => {
      for (const entry of readdirSync(directory, { withFileTypes: true })) {
        const path = join(directory, entry.name);
        if (entry.isDirectory()) walk(path);
        else if (/\.tsx?$/.test(entry.name) && !/\.test\.tsx?$/.test(entry.name))
          files.push(readFileSync(path, 'utf8'));
      }
    };
    walk(fileURLToPath(new URL('../apps/web/src', import.meta.url)));
    const source = files.join('\n');
    const missing = ids.filter(
      (id) => !source.includes(`'${id}'`) && !source.includes(`"${id}"`)
    );
    expect(missing).toEqual([]);
  });

  it('asserts the CPA data layer and Gateway admission as hard requirements', () => {
    expect(smoke).toContain("'/console/api/v2/cpamp/v0/management/auth-files'");
    expect(smoke).toMatch(/api-keys\/' \+ installKeyRecord\.id \+ '\/initial-install'/);
    expect(smoke).toMatch(/api-keys\/' \+ installKeyRecord\.id \+ '\/secret'/);
    expect(smoke).toContain(
      "process.env.CPAMP_SMOKE_MODEL_TARGET || 'http://cpamp-runtime:8317'"
    );
    expect(smoke).toContain("target+'/v1/models'");
    expect(smoke).toContain("target+'/v1/chat/completions'");
    expect(smoke).toContain('report.modelTarget = modelTarget');
    expect(smoke).toContain('CPAMP_SMOKE_MODEL_TARGET must be an http(s) origin');
    // The committed default Managed authority closes the pre-authority gap, so
    // the data layer keeps no tolerance that could absorb a real regression.
    expect(smoke).not.toContain('CPAMP_SMOKE_EXPECT_AUTHORITY_GAP');
    expect(smoke).not.toContain('DEFAULT AUTHORITY MISSING');
    expect(smoke).toContain(
      'cpamp/ bridge must read CPA /auth-files through the committed managed authority'
    );
    expect(smoke).toContain('Client Key initial install must observe the managed CPA');
    // The model-request check proves Client Key authentication only.
    expect(smoke).toContain(
      "'CPA authenticates the installed Client Key on a model request (non-401)'"
    );
    expect(smoke).toContain('Real Provider model success');
    // Ingress admits the installation Client Key and keeps the pre-admission
    // negative control closed; the probe origin is still reported.
    expect(smoke).toContain("parsed.port === '18317'");
    expect(smoke).toContain("'Ingress Gateway admits the installed Client Key'");
    expect(smoke).toContain("'gateway pending configuration'");
    // F5 removed the keyless window: the post-completion path is keyed, so the
    // script asserts 401 there and keeps Pending only as a genuine pre-admission
    // and post-restart negative control.
    expect(smoke).toContain("await closed('keyed');");
    expect(smoke).toContain("await closed('pending');");
    expect(smoke).toContain(
      'an admitted Gateway must still have CPA reject a missing Client Key'
    );
    // The anonymous peer may only be closed or 401.
    expect(smoke).toContain("['unreachable', '401'].includes(peerVerdict)");
  });
});

describe('Immutable v2 Embedded compose and pairing contracts', () => {
  const apiContractBytes = readFileSync(
    new URL('./fixtures/r1-embedded-api-transport.json', import.meta.url)
  );
  const apiContractSha =
    'sha256:' + createHash('sha256').update(apiContractBytes).digest('hex');
  const sourceCommit = 'a'.repeat(40);
  let fixtureDir, fixture, fixtureRenderer;
  beforeAll(async () => {
    fixtureDir = mkdtempSync(join(tmpdir(), 'cpamp-offline-oci-'));
    fixture = JSON.parse(execFileSync('python3', ['tests/embedded_renderer_fixture.py', fixtureDir], {
      cwd: new URL('..', import.meta.url), encoding: 'utf8',
    }));
    fixtureRenderer = await import(/* @vite-ignore */ pathToFileURL(fixture.module).href);
  }, 120000);
  afterAll(() => rmSync(fixtureDir, { recursive: true, force: true }));

  function makeCandidateEvidence() {
    return JSON.parse(readFileSync(join(fixtureDir, 'candidate-evidence.json'), 'utf8'));
  }
  // Synthetic positives use only the isolated test module, never the public runCli.
  function renderEmbeddedReleaseArtifacts(evidence, options = {}) {
    return fixtureRenderer.renderEmbeddedReleaseArtifacts(evidence, { evidenceDir: fixtureDir, ...options });
  }
  function verifyEmbeddedReleaseArtifacts(options) {
    return fixtureRenderer.verifyEmbeddedReleaseArtifacts({ evidenceDir: fixtureDir, ...options });
  }

  it('accepts only the exact four-field CPA artifact document, like the Python verifier', () => {
    const canonical = {
      schemaVersion: 1,
      engine: 'cpa',
      version: '8.0.55',
      artifactId: EXPECTED_CPA_ARTIFACT_ID,
    };
    expect(() =>
      assertCanonicalCpaArtifact(canonical, EXPECTED_CPA_ARTIFACT_ID, EXPECTED_CPA_ARTIFACT_ID)
    ).not.toThrow();
    for (const artifact of [
      { ...canonical, note: 'extra' },
      { schemaVersion: 1, engine: 'cpa', version: '8.0.55' },
      { ...canonical, version: '8.0.54' },
    ]) {
      expect(() =>
        assertCanonicalCpaArtifact(artifact, EXPECTED_CPA_ARTIFACT_ID, EXPECTED_CPA_ARTIFACT_ID)
      ).toThrow(/CPA artifact manifest mismatch for runtime/);
    }
  });

  it('renders digest-pinned three-image compose and pairing manifest that pass Runtime 12/18 validation', () => {
    const evidence = makeCandidateEvidence();
    const { composeYaml, composeJson, pairing } = renderEmbeddedReleaseArtifacts(evidence, {
      version: 'v2.0.0-beta.1',
    });

    expect(composeYaml).not.toContain(':latest');
    expect(composeYaml).not.toMatch(/^\s*build\s*:/m);
    expect(composeYaml).not.toContain('CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK');
    expect(composeJson.services['cpamp-ingress'].image).toBe(
      `seakee/cpamp-ingress:v2.0.0-beta.1@${evidence.images.ingress.platformManifestDigest}`
    );
    expect(composeJson.services['cpamp-manager'].image).toBe(
      `seakee/cpa-manager-plus:v2.0.0-beta.1@${evidence.images.manager.platformManifestDigest}`
    );
    expect(composeJson.services['cpamp-runtime'].image).toBe(
      `seakee/cpamp-runtime:v2.0.0-beta.1@${evidence.images.runtime.platformManifestDigest}`
    );

    expect(pairing).toMatchObject({
      schema_version: 1,
      version: 'v2.0.0-beta.1',
      source_commit: sourceCommit,
      platform: 'linux/amd64',
      cpa: {
        version: '8.0.55',
        artifact_id: `sha256:${fixture.sha}`,
      },
      fullweb: {
        manifest_sha256: evidence.images.manager.fullWebSummary.manifestSHA256,
        index_html_sha256: evidence.images.manager.fullWebSummary.indexHtmlSHA256,
        asset_count: evidence.images.manager.fullWebSummary.assetCount,
      },
      api_contract: {
        sha256: apiContractSha,
        qualification: 'SOURCE_TRANSPORT_CONTRACT_ONLY_NOT_COMPLETE_WEB_API_COMPATIBILITY',
        api_suffix: '/api/v2',
        expected_path: '/phase4-test/api/v2/technical',
      },
      compose: {
        filename: COMPOSE_FILENAME,
        size: Buffer.byteLength(composeYaml, 'utf8'),
      },
    });
    expect(pairing.images.manager.image_config_digest).toBe(
      evidence.images.manager.imageConfigDigest
    );
    expect(pairing.images.ingress.image_config_digest).toBe(
      evidence.images.ingress.imageConfigDigest
    );
    expect(pairing.images.runtime.image_config_digest).toBe(
      evidence.images.runtime.imageConfigDigest
    );
  });

  it('fails closed on :latest, tag-only references, digest mismatches, source commit drift, and tampered Web/API or compose invariants', () => {
    const evidence = makeCandidateEvidence();
    const { composeYaml, pairing } = renderEmbeddedReleaseArtifacts(evidence, {
      version: 'v2.0.0-beta.1',
    });

    // 1. :latest version or image reference
    expect(() => renderEmbeddedReleaseArtifacts(evidence, { version: 'latest' })).toThrow(
      /latest/i
    );
    expect(() =>
      renderEmbeddedReleaseArtifacts(evidence, {
        version: 'v2.0.0-beta.1',
        imageRefs: {
          manager:
            'seakee/cpa-manager-plus:latest@sha256:be0dee922347da5e33bf9546bb86447a7afa9973e3bc77a965908a31e43a0258',
        },
      })
    ).toThrow(/latest/i);

    // 2. Tag-only reference without @sha256:<64-hex>
    expect(() =>
      renderEmbeddedReleaseArtifacts(evidence, {
        version: 'v2.0.0-beta.1',
        imageRefs: {
          ingress: 'seakee/cpamp-ingress:v2.0.0-beta.1',
        },
      })
    ).toThrow(/@sha256/);

    // 3. Mismatched explicit digest for any role
    expect(() =>
      renderEmbeddedReleaseArtifacts(evidence, {
        version: 'v2.0.0-beta.1',
        digests: {
          runtime: 'sha256:' + '9'.repeat(64),
        },
      })
    ).toThrow(/manifest digest mismatch for runtime/);

    // 4. Mismatched source_commit across roles / Manager binary
    const driftedSourceEvidence = structuredClone(evidence);
    driftedSourceEvidence.images.manager.goBinaryIdentity.managerCompiledSourceClaim =
      '0'.repeat(40);
    expect(() => renderEmbeddedReleaseArtifacts(driftedSourceEvidence)).toThrow(
      /Manager compiled source commit/
    );

    for (const role of ['manager', 'ingress', 'runtime']) {
      const driftedRoleEvidence = structuredClone(evidence);
      driftedRoleEvidence.images[role].sourceCommitClaim = '0'.repeat(40);
      expect(() => renderEmbeddedReleaseArtifacts(driftedRoleEvidence)).toThrow(
        new RegExp(`${role} sourceCommitClaim differs`)
      );
    }

    // 5. Tampered Full Web manifest or API contract digest in pairing
    const badWebPairing = structuredClone(pairing);
    badWebPairing.fullweb.manifest_sha256 = 'sha256:' + '0'.repeat(64);
    expect(() =>
      verifyEmbeddedReleaseArtifacts({
        evidence,
        composeText: composeYaml,
        pairing: badWebPairing,
      })
    ).toThrow(/fullweb manifest summary mismatch/);

    const badApiPairing = structuredClone(pairing);
    badApiPairing.api_contract.sha256 = 'sha256:' + '0'.repeat(64);
    expect(() =>
      verifyEmbeddedReleaseArtifacts({
        evidence,
        composeText: composeYaml,
        pairing: badApiPairing,
      })
    ).toThrow(/api_contract summary mismatch/);

    // 6. Compose injected with CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK, extra port, deeper-indented extra port, or unrecognized property
    const loopbackCompose = composeYaml.replace(
      "      CPAMP_RUNTIME_CPA_GID: '10001'",
      "      CPAMP_RUNTIME_CPA_GID: '10001'\n      CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK: '1'"
    );
    expect(() => composeYamlToRuntime12ConfigJson(loopbackCompose)).toThrow(
      /CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK/
    );

    const extraPortCompose = composeYaml.replace(
      "    expose:\n      - '18317'",
      "    ports:\n      - '18318:18317'\n    expose:\n      - '18317'"
    );
    expect(() => composeYamlToRuntime12ConfigJson(extraPortCompose)).toThrow(
      /expected only cpamp-ingress 18317:18317/
    );

    const deepIndentExtraPortCompose = composeYaml.replace(
      "    expose:\n      - '18317'",
      "    ports:\n        - '18318:18317'\n    expose:\n      - '18317'"
    );
    const resyncedDeepIndentPairing = structuredClone(pairing);
    resyncedDeepIndentPairing.compose.sha256 =
      'sha256:' + createHash('sha256').update(deepIndentExtraPortCompose, 'utf8').digest('hex');
    resyncedDeepIndentPairing.compose.size = Buffer.byteLength(deepIndentExtraPortCompose, 'utf8');
    expect(() =>
      verifyEmbeddedReleaseArtifacts({
        evidence,
        composeText: deepIndentExtraPortCompose,
        pairing: resyncedDeepIndentPairing,
      })
    ).toThrow(/expected only cpamp-ingress 18317:18317/);

    const unrecognizedKeyCompose = composeYaml.replace(
      '    restart: unless-stopped\n    expose:\n      - \'18317\'',
      '    restart: unless-stopped\n    extra_hosts:\n      - "host.docker.internal:host-gateway"\n    expose:\n      - \'18317\''
    );
    expect(() => composeYamlToRuntime12ConfigJson(unrecognizedKeyCompose)).toThrow(
      /unrecognized compose service property cpamp-manager\.extra_hosts/
    );

    // 7. P1-2: Non-boolean quoted strings for privileged or read_only
    const quotedPrivilegedTrue = composeYaml.replace(
      '  cpamp-runtime:\n',
      '  cpamp-runtime:\n    privileged: "true"\n'
    );
    expect(() => composeYamlToRuntime12ConfigJson(quotedPrivilegedTrue)).toThrow(
      /cpamp-runtime\.privileged must be a boolean/
    );
    const quotedPrivilegedFalse = composeYaml.replace(
      '  cpamp-runtime:\n',
      '  cpamp-runtime:\n    privileged: "false"\n'
    );
    expect(() => composeYamlToRuntime12ConfigJson(quotedPrivilegedFalse)).toThrow(
      /cpamp-runtime\.privileged must be a boolean/
    );
    const quotedReadOnlyFalse = composeYaml.replace(
      '      - cpamp-runtime-transport-secret:/run/cpamp/runtime-secret:ro',
      '      - type: volume\n        source: cpamp-runtime-transport-secret\n        target: /run/cpamp/runtime-secret\n        read_only: "false"'
    );
    expect(() => composeYamlToRuntime12ConfigJson(quotedReadOnlyFalse)).toThrow(
      /cpamp-manager volume read_only must be a boolean/
    );
    const quotedReadOnlyTrue = composeYaml.replace(
      '      - cpamp-runtime-transport-secret:/run/cpamp/runtime-secret:ro',
      '      - type: volume\n        source: cpamp-runtime-transport-secret\n        target: /run/cpamp/runtime-secret\n        read_only: "true"'
    );
    expect(() => composeYamlToRuntime12ConfigJson(quotedReadOnlyTrue)).toThrow(
      /cpamp-manager volume read_only must be a boolean/
    );

    // 8. P1-3: Cross-role state volume mount on Manager
    const crossRoleMountCompose = composeYaml.replace(
      '      - cpamp-manager-data:/data',
      '      - cpamp-manager-data:/data\n      - cpamp-runtime-data:/runtime-copy'
    );
    const resyncedCrossMountPairing = structuredClone(pairing);
    resyncedCrossMountPairing.compose.sha256 =
      'sha256:' + createHash('sha256').update(crossRoleMountCompose, 'utf8').digest('hex');
    resyncedCrossMountPairing.compose.size = Buffer.byteLength(crossRoleMountCompose, 'utf8');
    expect(() =>
      verifyEmbeddedReleaseArtifacts({
        evidence,
        composeText: crossRoleMountCompose,
        pairing: resyncedCrossMountPairing,
      })
    ).toThrow(/strict volume whitelist violation on cpamp-manager/);

    // 9. P1-4 / Round-3 P2: Canonical port template verifies deterministically without reading process.env,
    // while UDP protocol, long-syntax port object, or literal different container port fail closed.
    const verifiedPort18317 = verifyEmbeddedReleaseArtifacts({
      evidence,
      composeText: composeYaml,
      pairing,
    });
    expect(verifiedPort18317.composeDoc.services['cpamp-ingress'].ports).toEqual([
      { mode: 'ingress', published: '18317', target: 18317, protocol: 'tcp' },
    ]);

    // Round-3 P1: Top-level volume overrides (name/external/driver/driver_opts/labels) fail closed
    for (const badVolBlock of [
      '  cpamp-manager-data:\n    name: cpamp-shared-state\n',
      '  cpamp-manager-data:\n    external: true\n',
      '  cpamp-manager-data:\n    driver: local\n',
      '  cpamp-manager-data:\n    driver_opts:\n      o: bind\n',
      '  cpamp-manager-data:\n    labels:\n      env: prod\n',
    ]) {
      const badVolCompose = composeYaml.replace('  cpamp-manager-data:\n', badVolBlock);
      expect(() => composeYamlToRuntime12ConfigJson(badVolCompose)).toThrow(
        /top-level volume cpamp-manager-data must be a canonical empty definition/
      );
    }

    // Round-3 P1: Service mount volume sub-options fail closed
    const badSubVolCompose = composeYaml.replace(
      '      - cpamp-manager-data:/data',
      '      - type: volume\n        source: cpamp-manager-data\n        target: /data\n        volume:\n          nocopy: true'
    );
    expect(() => composeYamlToRuntime12ConfigJson(badSubVolCompose)).toThrow(
      /volume sub-options on cpamp-manager mounts are forbidden/
    );
    const udpPortCompose = composeYaml.replace(
      "      - '${CPAMP_PUBLIC_PORT:-18317}:18317'",
      "      - '${CPAMP_PUBLIC_PORT:-18317}:18317/udp'"
    );
    expect(() => composeYamlToRuntime12ConfigJson(udpPortCompose)).toThrow(
      /expected only cpamp-ingress 18317:18317 via canonical/
    );

    const diffContainerPortCompose = composeYaml.replace(
      "      - '${CPAMP_PUBLIC_PORT:-18317}:18317'",
      "      - '${CPAMP_PUBLIC_PORT:-18317}:18318'"
    );
    expect(() => composeYamlToRuntime12ConfigJson(diffContainerPortCompose)).toThrow(
      /expected only cpamp-ingress 18317:18317 via canonical/
    );

    const longFormPortCompose = composeYaml.replace(
      "      - '${CPAMP_PUBLIC_PORT:-18317}:18317'",
      '      - target: 18317\n        published: "18317"\n        protocol: udp\n        host_ip: "0.0.0.0"\n        mode: host'
    );
    expect(() => composeYamlToRuntime12ConfigJson(longFormPortCompose)).toThrow(
      /long-syntax or non-string port definitions are forbidden/
    );

    // 10. P2-1: Required api_contract.contract_id and images.<role>.image_ref
    const badContractIdPairing = structuredClone(pairing);
    badContractIdPairing.api_contract.contract_id = 'forged-contract-id';
    expect(() =>
      verifyEmbeddedReleaseArtifacts({
        evidence,
        composeText: composeYaml,
        pairing: badContractIdPairing,
      })
    ).toThrow(/api_contract summary mismatch/);

    const missingImageRefPairing = structuredClone(pairing);
    delete missingImageRefPairing.images.manager.image_ref;
    expect(() =>
      verifyEmbeddedReleaseArtifacts({
        evidence,
        composeText: composeYaml,
        pairing: missingImageRefPairing,
      })
    ).toThrow(/image_ref missing or mismatched for manager/);
  });

  function officialEvidence() {
    const evidence = makeCandidateEvidence();
    evidence.officialCPA.verifiedFinalImageExecutableSHA256 = EXPECTED_CPA_SHA256;
    evidence.images.runtime.packagedFiles[PACKAGED_PATHS.cpaBinary] = EXPECTED_CPA_ARTIFACT_ID;
    return evidence;
  }

  it('requires all three archives on every public release API regardless of caller options', () => {
    const evidence = officialEvidence();
    const { composeYaml, pairing } = renderEmbeddedReleaseArtifacts(makeCandidateEvidence());
    pairing.cpa.artifact_id = EXPECTED_CPA_ARTIFACT_ID;
    for (const options of [{}, { requireOciArchives: false }]) {
      expect(() => publicRender(evidence, options)).toThrow(/missing required OCI archive for manager/);
      expect(() => resolveFullWebAndApiMetadata(evidence, options)).toThrow(/missing required OCI archive for manager/);
      expect(() => publicVerify({ evidence, composeText: composeYaml, pairing, ...options }))
        .toThrow(/missing required OCI archive for manager/);
    }
    for (const missing of ['manager', 'ingress', 'runtime']) {
      const roleOciPaths = Object.fromEntries(['manager', 'ingress', 'runtime'].map(role =>
        [role, join(fixtureDir, role === missing ? `missing-${role}.oci.tar` : `${role}.oci.tar`)]));
      const error = new RegExp(`missing required OCI archive for ${missing}`);
      expect(() => publicRender(evidence, { roleOciPaths })).toThrow(error);
      expect(() => resolveFullWebAndApiMetadata(evidence, { roleOciPaths })).toThrow(error);
      expect(() => publicVerify({ evidence, composeText: composeYaml, pairing, roleOciPaths })).toThrow(error);
    }
  });

  it('keeps the public official CPA pin immutable and exposes no pin-override or test-setter surface', async () => {
    // (a) Production module surface contract: exact allowed export list (kept in sync with tests/embedded_candidate_contract_test.py)
    const exportNames = Object.keys(productionRendererModule).sort();
    expect(exportNames).toEqual([
      'CANONICAL_INGRESS_PORT_SPEC',
      'CLI_HELP_TEXT',
      'COMPOSE_FILENAME',
      'COMPOSE_SERVICES',
      'DEFAULT_REGISTRY_PREFIX',
      'DEFAULT_REPOSITORIES',
      'EMBEDDED_VERIFIER_CONTRACT_NOTICE',
      'EXPECTED_API_CONTRACT_ID',
      'EXPECTED_API_CONTRACT_QUALIFICATION',
      'EXPECTED_API_PATH',
      'EXPECTED_API_SUFFIX',
      'EXPECTED_CPA_ARTIFACT_ID',
      'EXPECTED_CPA_SHA256',
      'EXPECTED_CPA_VERSION',
      'PACKAGED_PATHS',
      'PAIRING_FILENAME',
      'ROLES',
      'assertCanonicalCpaArtifact',
      'composeYamlToRuntime12ConfigJson',
      'extractFilesFromOciArchive',
      'parseEmbeddedComposeText',
      'parsePinnedImageReference',
      'renderEmbeddedComposeYaml',
      'renderEmbeddedReleaseArtifacts',
      'resolveFullWebAndApiMetadata',
      'runCli',
      'validateCandidateEvidence',
      'validateRegistryPrefix',
      'validateRuntime12ComposeDocument',
      'validateVersionTag',
      'verifyEmbeddedReleaseArtifacts',
      'verifyOciArchiveAgainstRoleEvidence',
    ]);
    expect(
      exportNames.filter(
        (name) =>
          name !== 'parsePinnedImageReference' &&
          /pin|ForTest|setExpected|activeExpected/i.test(name)
      )
    ).toEqual([]);
    expect(EXPECTED_CPA_SHA256).toBe(
      'e48c85c75a19adae186cbee590f6e6143fd8085417d8066b545e8533877175f7'
    );
    expect(EXPECTED_CPA_ARTIFACT_ID).toBe(
      'sha256:e48c85c75a19adae186cbee590f6e6143fd8085417d8066b545e8533877175f7'
    );

    // (b) After synthetic fixture rendering, every public production check still uses the constant official pin
    const syntheticEvidence = makeCandidateEvidence();
    const { composeYaml, pairing } = renderEmbeddedReleaseArtifacts(syntheticEvidence);
    const syntheticPairing = structuredClone(pairing);
    expect(syntheticPairing.cpa.artifact_id).toBe(`sha256:${fixture.sha}`);
    const runtimeTar = join(fixtureDir, 'runtime.oci.tar');
    const syntheticEvidencePath = join(fixtureDir, 'candidate-evidence.json');
    const syntheticPairingPath = join(fixtureDir, 'synthetic-pairing.json');
    const evidencePath = join(fixtureDir, 'official-evidence.json');
    const composePath = join(fixtureDir, COMPOSE_FILENAME);
    const pairingPath = join(fixtureDir, PAIRING_FILENAME);
    writeFileSync(syntheticPairingPath, JSON.stringify(syntheticPairing));

    const evidence = officialEvidence();
    pairing.cpa.artifact_id = EXPECTED_CPA_ARTIFACT_ID;
    writeFileSync(evidencePath, JSON.stringify(evidence));
    writeFileSync(composePath, composeYaml);
    writeFileSync(pairingPath, JSON.stringify(pairing));

    const assertPublicEntriesRejectNonOfficialCpa = (mod) => {
      expect(() => mod.validateCandidateEvidence(syntheticEvidence)).toThrow(
        /officialCPA pin mismatch/
      );
      expect(() =>
        mod.resolveFullWebAndApiMetadata(syntheticEvidence, { evidenceDir: fixtureDir })
      ).toThrow(/officialCPA pin mismatch/);
      expect(() =>
        mod.renderEmbeddedReleaseArtifacts(syntheticEvidence, { evidenceDir: fixtureDir })
      ).toThrow(/officialCPA pin mismatch/);
      expect(() =>
        mod.verifyEmbeddedReleaseArtifacts({
          evidence: syntheticEvidence,
          composeText: composeYaml,
          pairing: syntheticPairing,
          evidenceDir: fixtureDir,
        })
      ).toThrow(/officialCPA pin mismatch/);
      expect(() =>
        mod.runCli([
          '--evidence',
          syntheticEvidencePath,
          '--compose-out',
          composePath,
          '--pairing-out',
          pairingPath,
        ])
      ).toThrow(/officialCPA pin mismatch/);
      expect(() =>
        mod.runCli([
          '--verify',
          '--evidence',
          syntheticEvidencePath,
          '--compose',
          composePath,
          '--pairing',
          syntheticPairingPath,
        ])
      ).toThrow(/officialCPA pin mismatch/);

      expect(() =>
        mod.verifyOciArchiveAgainstRoleEvidence(runtimeTar, 'runtime', evidence)
      ).toThrow(/official CPA v8\.0\.55 executable hash mismatch/);
      expect(() =>
        mod.resolveFullWebAndApiMetadata(evidence, { evidenceDir: fixtureDir })
      ).toThrow(/official CPA v8\.0\.55 executable hash mismatch/);
      expect(() =>
        mod.renderEmbeddedReleaseArtifacts(evidence, { evidenceDir: fixtureDir })
      ).toThrow(/official CPA v8\.0\.55 executable hash mismatch/);
      expect(() =>
        mod.verifyEmbeddedReleaseArtifacts({
          evidence,
          composeText: composeYaml,
          pairing,
          evidenceDir: fixtureDir,
        })
      ).toThrow(/official CPA v8\.0\.55 executable hash mismatch/);
      expect(() =>
        mod.runCli([
          '--evidence',
          evidencePath,
          '--compose-out',
          composePath,
          '--pairing-out',
          pairingPath,
        ])
      ).toThrow(/official CPA v8\.0\.55 executable hash mismatch/);
      expect(() =>
        mod.runCli([
          '--verify',
          '--evidence',
          evidencePath,
          '--compose',
          composePath,
          '--pairing',
          pairingPath,
        ])
      ).toThrow(/official CPA v8\.0\.55 executable hash mismatch/);
    };

    assertPublicEntriesRejectNonOfficialCpa({
      validateCandidateEvidence: publicValidateCandidateEvidence,
      resolveFullWebAndApiMetadata,
      renderEmbeddedReleaseArtifacts: publicRender,
      verifyEmbeddedReleaseArtifacts: publicVerify,
      verifyOciArchiveAgainstRoleEvidence: publicVerifyOciArchiveAgainstRoleEvidence,
      runCli: publicRunCli,
    });

    // Fresh cache-busting import with process.env replaced by a Proxy that returns fixture.sha
    // for every key outside the explicit allowlist the module legitimately reads (`NODE_PATH`).
    const allowedEnvKeys = new Set(['NODE_PATH']);
    const originalEnvDescriptor = Object.getOwnPropertyDescriptor(process, 'env');
    const realEnv = process.env;
    const proxyEnv = new Proxy(realEnv, {
      get(target, prop, receiver) {
        if (typeof prop === 'string' && !allowedEnvKeys.has(prop)) {
          return fixture.sha;
        }
        return Reflect.get(target, prop, receiver);
      },
    });
    Object.defineProperty(process, 'env', {
      ...originalEnvDescriptor,
      value: proxyEnv,
    });
    try {
      const freshUrl = `${new URL('../bin/release/render-embedded-compose.mjs', import.meta.url).href}?env-probe=${Date.now()}`;
      const freshProductionRenderer = await import(/* @vite-ignore */ freshUrl);
      assertPublicEntriesRejectNonOfficialCpa(freshProductionRenderer);
    } finally {
      Object.defineProperty(process, 'env', originalEnvDescriptor);
    }

    const poisonedEnv = {
      ...process.env,
      CPAMP_CPA_SHA256: fixture.sha,
      EXPECTED_CPA_SHA256: fixture.sha,
      CPA_SHA: fixture.sha,
      CPA_SHA256: fixture.sha,
      CPAMP_EXPECTED_CPA_SHA256: fixture.sha,
    };
    const run = (program, args) =>
      execFileSync(program, args, {
        cwd: new URL('..', import.meta.url),
        env: poisonedEnv,
        stdio: 'pipe',
      });
    const entry = 'bin/release/render-embedded-compose.mjs';
    expect(() =>
      run(process.execPath, [
        entry,
        '--evidence',
        syntheticEvidencePath,
        '--compose-out',
        composePath,
        '--pairing-out',
        pairingPath,
      ])
    ).toThrow(/officialCPA pin mismatch/);
    expect(() =>
      run(process.execPath, [
        entry,
        '--verify',
        '--evidence',
        syntheticEvidencePath,
        '--compose',
        composePath,
        '--pairing',
        syntheticPairingPath,
      ])
    ).toThrow(/officialCPA pin mismatch/);
    expect(() =>
      run(process.execPath, [
        entry,
        '--evidence',
        evidencePath,
        '--compose-out',
        composePath,
        '--pairing-out',
        pairingPath,
      ])
    ).toThrow(/official CPA v8\.0\.55 executable hash mismatch/);
    expect(() =>
      run(process.execPath, [
        entry,
        '--verify',
        '--evidence',
        evidencePath,
        '--compose',
        composePath,
        '--pairing',
        pairingPath,
      ])
    ).toThrow(/official CPA v8\.0\.55 executable hash mismatch/);
    expect(() =>
      run('python3', [
        'bin/release/verify-embedded-candidate.py',
        '--evidence',
        evidencePath,
        '--verify-compose',
        composePath,
        '--verify-pairing',
        pairingPath,
      ])
    ).toThrow(/official CPA v8\.0\.55 executable hash mismatch/);
  });

  it('rejects missing archives and malformed receipts in the isolated fixture', () => {
    const evidence = makeCandidateEvidence();
    const { composeYaml, pairing } = renderEmbeddedReleaseArtifacts(evidence);
    for (const role of ['manager', 'ingress', 'runtime']) {
      const path = join(fixtureDir, `${role}.oci.tar`);
      renameSync(path, path + '.saved');
      try {
        const error = new RegExp(`missing required OCI archive for ${role}`);
        expect(() => renderEmbeddedReleaseArtifacts(evidence)).toThrow(error);
        expect(() => verifyEmbeddedReleaseArtifacts({ evidence, composeText: composeYaml, pairing })).toThrow(error);
      } finally { renameSync(path + '.saved', path); }
    }
    for (const archive of [null, '', 'wrong.tar']) {
      const bad = structuredClone(evidence);
      bad.images.manager.archive = archive;
      expect(() => renderEmbeddedReleaseArtifacts(bad)).toThrow(/invalid or missing archive filename for manager/);
    }
    evidence.images.runtime.packagedFiles[PACKAGED_PATHS.cpaArtifact] = 'sha256:' + '7'.repeat(64);
    expect(() => renderEmbeddedReleaseArtifacts(evidence)).toThrow(/CPA artifact manifest digest mismatch/);
    expect(() => verifyEmbeddedReleaseArtifacts({ evidence, composeText: composeYaml, pairing })).toThrow(/CPA artifact manifest digest mismatch/);
  });

  it('documents static port and Go certification boundaries in CLI help', () => {
    const help = execFileSync(process.execPath, ['bin/release/render-embedded-compose.mjs', '--help'], {
      cwd: new URL('..', import.meta.url), encoding: 'utf8',
    });
    expect(help).toContain('${CPAMP_PUBLIC_PORT:-18317}:18317');
    expect(help).toContain('CPAMP_PUBLIC_PORT=[HOST_IP:]PORT');
    expect(help).toContain('does NOT certify Go build identity');
    expect(help).toContain('verify-embedded-candidate.py');
  });

  it('keeps template verification independent of deployment host binding', () => {
    const refs = Object.fromEntries(['manager', 'ingress', 'runtime'].map(role => [role,
      `seakee/${role}:v2.0.0-beta.1@${makeCandidateEvidence().images[role].platformManifestDigest}`]));
    const compose = renderEmbeddedComposeYaml(refs);
    const path = join(fixtureDir, 'port-template.yml');
    writeFileSync(path, compose);
    for (const value of [undefined, '18317', '18318', '127.0.0.1:18318']) {
      const env = { ...process.env };
      if (value === undefined) delete env.CPAMP_PUBLIC_PORT;
      else env.CPAMP_PUBLIC_PORT = value;
      const projected = execFileSync(process.execPath, ['--input-type=module', '-e',
        `import { composeYamlToRuntime12ConfigJson } from './bin/release/render-embedded-compose.mjs';
         import { readFileSync } from 'node:fs';
         console.log(JSON.stringify(composeYamlToRuntime12ConfigJson(readFileSync(process.argv[1], 'utf8'))));`, path],
        { cwd: new URL('..', import.meta.url), env, encoding: 'utf8' });
      expect(JSON.parse(projected).services['cpamp-ingress'].ports).toEqual([
        { mode: 'ingress', published: '18317', target: 18317, protocol: 'tcp' },
      ]);
    }
  });
});
