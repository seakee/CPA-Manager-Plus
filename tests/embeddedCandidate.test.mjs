import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';
import { generateReleaseInfo } from '../bin/release/generate-release-info.mjs';
import { verifyCandidate, verifyImage } from '../bin/release/publish-update-index.mjs';
import {
  COMPOSE_FILENAME,
  composeYamlToRuntime12ConfigJson,
  EXPECTED_CPA_SHA256,
  PACKAGED_PATHS,
  PAIRING_FILENAME,
  renderEmbeddedReleaseArtifacts,
  verifyEmbeddedReleaseArtifacts,
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
  const sourceCommit = '8935bce5ea7ce994185ee97acb8a4fdbd9cdd6cc';

  function makeCandidateEvidence() {
    const webManifestSha = 'sha256:' + 'e'.repeat(64);
    const indexHtmlSha = 'sha256:' + 'f'.repeat(64);
    return {
      schemaVersion: 1,
      status: 'STRUCTURAL_CANDIDATE_EVIDENCE_ONLY_NOT_RELEASE_QUALIFIED',
      sourceCommitClaim: sourceCommit,
      candidateBuildEventClaim: 'local-r1-7d339900903248d580b067e35cdea3a9',
      candidateVersionClaim: '2.0.0-r1candidate',
      platform: 'linux/amd64',
      officialCPA: {
        version: '8.0.55',
        dockerBuildPinArchiveSHA256:
          '4cd593251db85e50c820f42e6ea5cecdd05f902201d3dbdb025671d4f306a123',
        verifiedFinalImageExecutableSHA256: EXPECTED_CPA_SHA256,
      },
      images: {
        manager: {
          archive: 'manager.oci.tar',
          archiveSHA256: 'sha256:' + '1'.repeat(64),
          platform: 'linux/amd64',
          platformManifestDigest:
            'sha256:be0dee922347da5e33bf9546bb86447a7afa9973e3bc77a965908a31e43a0258',
          imageConfigDigest:
            'sha256:38ade7eae6d09e7c05501eae543992cdbea99e0090e6d656651ed12744706f86',
          goBinaryIdentity: {
            entrypoint:
              'github.com/seakee/cpa-manager-plus/apps/manager-server/cmd/cpa-manager-plus',
            goTarget: 'linux/amd64',
            managerCompiledSourceClaim: sourceCommit,
            module: 'github.com/seakee/cpa-manager-plus/apps/manager-server',
          },
          fullWebSummary: {
            manifestSHA256: webManifestSha,
            indexHtmlSHA256: indexHtmlSha,
            assetCount: 14,
          },
          apiTransportContract: {
            sha256: apiContractSha,
            contractId: 'cpamp-r1-embedded-public-api-transport-v1',
            qualification:
              'SOURCE_TRANSPORT_CONTRACT_ONLY_NOT_COMPLETE_WEB_API_COMPATIBILITY',
            apiSuffix: '/api/v2',
            expectedPath: '/phase4-test/api/v2/technical',
          },
          packagedFiles: {
            [PACKAGED_PATHS.binaries.manager]: 'sha256:' + 'a'.repeat(64),
            [PACKAGED_PATHS.provenance]: 'sha256:' + 'b'.repeat(64),
            [PACKAGED_PATHS.webManifest]: webManifestSha,
            [PACKAGED_PATHS.apiContract]: apiContractSha,
          },
        },
        ingress: {
          archive: 'ingress.oci.tar',
          archiveSHA256: 'sha256:' + '2'.repeat(64),
          platform: 'linux/amd64',
          platformManifestDigest:
            'sha256:ec2d8db9eca2c102a13f05667f01db2bf62c0208bcade582a05a23eedb276c4d',
          imageConfigDigest:
            'sha256:5cfa0720fb17822a3b682fe311b336aafe2ebca7c8a037b89ca1df679a7625fb',
          goBinaryIdentity: {
            entrypoint: 'github.com/seakee/cpa-manager-plus/apps/ingress/cmd/cpamp-ingress',
            goTarget: 'linux/amd64',
            managerCompiledSourceClaim: null,
            module: 'github.com/seakee/cpa-manager-plus/apps/ingress',
          },
          packagedFiles: {
            [PACKAGED_PATHS.binaries.ingress]: 'sha256:' + 'c'.repeat(64),
            [PACKAGED_PATHS.provenance]: 'sha256:' + 'd'.repeat(64),
          },
        },
        runtime: {
          archive: 'runtime.oci.tar',
          archiveSHA256: 'sha256:' + '3'.repeat(64),
          platform: 'linux/amd64',
          platformManifestDigest:
            'sha256:9e334bf2941c959cf9220f2da126086673eff10fbab4da621a3477c138c967d1',
          imageConfigDigest:
            'sha256:8c6a7ed65a2c4cd15c5a65b57e0e296bb519c5f8e3130f87c77e1a3647861797',
          goBinaryIdentity: {
            entrypoint:
              'github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/cmd/cpamp-runtime-supervisor',
            goTarget: 'linux/amd64',
            managerCompiledSourceClaim: null,
            module: 'github.com/seakee/cpa-manager-plus/apps/runtime-supervisor',
          },
          packagedFiles: {
            [PACKAGED_PATHS.binaries.runtime]: 'sha256:' + '4'.repeat(64),
            [PACKAGED_PATHS.provenance]: 'sha256:' + '5'.repeat(64),
            [PACKAGED_PATHS.cpaBinary]: 'sha256:' + EXPECTED_CPA_SHA256,
          },
        },
      },
    };
  }

  it('renders digest-pinned three-image compose and pairing manifest that pass Runtime 12/18 validation', () => {
    const evidence = makeCandidateEvidence();
    const { composeYaml, composeJson, pairing } = renderEmbeddedReleaseArtifacts(evidence, {
      version: 'v2.0.0-beta.1',
    });

    expect(composeYaml).not.toContain(':latest');
    expect(composeYaml).not.toMatch(/^\s*build\s*:/m);
    expect(composeYaml).not.toContain('CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK');
    expect(composeJson.services['cpamp-ingress'].image).toBe(
      'seakee/cpamp-ingress:v2.0.0-beta.1@sha256:ec2d8db9eca2c102a13f05667f01db2bf62c0208bcade582a05a23eedb276c4d'
    );
    expect(composeJson.services['cpamp-manager'].image).toBe(
      'seakee/cpa-manager-plus:v2.0.0-beta.1@sha256:be0dee922347da5e33bf9546bb86447a7afa9973e3bc77a965908a31e43a0258'
    );
    expect(composeJson.services['cpamp-runtime'].image).toBe(
      'seakee/cpamp-runtime:v2.0.0-beta.1@sha256:9e334bf2941c959cf9220f2da126086673eff10fbab4da621a3477c138c967d1'
    );

    expect(pairing).toMatchObject({
      schema_version: 1,
      version: 'v2.0.0-beta.1',
      source_commit: sourceCommit,
      platform: 'linux/amd64',
      cpa: {
        version: '8.0.55',
        artifact_id: `sha256:${EXPECTED_CPA_SHA256}`,
      },
      fullweb: {
        manifest_sha256: 'sha256:' + 'e'.repeat(64),
        index_html_sha256: 'sha256:' + 'f'.repeat(64),
        asset_count: 14,
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
      'sha256:38ade7eae6d09e7c05501eae543992cdbea99e0090e6d656651ed12744706f86'
    );
    expect(pairing.images.ingress.image_config_digest).toBe(
      'sha256:5cfa0720fb17822a3b682fe311b336aafe2ebca7c8a037b89ca1df679a7625fb'
    );
    expect(pairing.images.runtime.image_config_digest).toBe(
      'sha256:8c6a7ed65a2c4cd15c5a65b57e0e296bb519c5f8e3130f87c77e1a3647861797'
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

    const driftedRoleEvidence = structuredClone(evidence);
    driftedRoleEvidence.images.ingress.sourceCommitClaim = '0'.repeat(40);
    expect(() => renderEmbeddedReleaseArtifacts(driftedRoleEvidence)).toThrow(
      /ingress sourceCommitClaim differs/
    );

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

    // 6. Compose injected with CPAMP_RUNTIME_CPA_LISTEN_LOOPBACK or extra port
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
  });

  it('runs the CLI renderer and --verify mode end-to-end', () => {
    const dir = mkdtempSync(join(tmpdir(), 'cpamp-compose-pairing-'));
    try {
      const evidence = makeCandidateEvidence();
      const evidencePath = join(dir, 'candidate-evidence.json');
      const composePath = join(dir, COMPOSE_FILENAME);
      const pairingPath = join(dir, PAIRING_FILENAME);
      const composeJsonPath = join(dir, 'rendered-compose.json');
      writeFileSync(evidencePath, JSON.stringify(evidence, null, 2) + '\n', 'utf8');

      execFileSync(
        process.execPath,
        [
          'bin/release/render-embedded-compose.mjs',
          '--evidence',
          evidencePath,
          '--version',
          'v2.0.0-beta.1',
          '--compose-out',
          composePath,
          '--pairing-out',
          pairingPath,
          '--compose-json-out',
          composeJsonPath,
        ],
        { cwd: new URL('..', import.meta.url), stdio: 'pipe' }
      );

      execFileSync(
        process.execPath,
        [
          'bin/release/render-embedded-compose.mjs',
          '--verify',
          '--evidence',
          evidencePath,
          '--compose',
          composePath,
          '--pairing',
          pairingPath,
        ],
        { cwd: new URL('..', import.meta.url), stdio: 'pipe' }
      );

      const validatorOut = execFileSync(
        process.execPath,
        ['bin/ci/validate-runtime12-compose.mjs'],
        {
          cwd: new URL('..', import.meta.url),
          input: readFileSync(composeJsonPath, 'utf8'),
          encoding: 'utf8',
        }
      );
      expect(validatorOut).toContain('Runtime 18 Compose validation passed');
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  });
});
