import { execFileSync } from 'node:child_process';
import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';
import { generateReleaseInfo } from '../bin/release/generate-release-info.mjs';
import { verifyCandidate, verifyImage } from '../bin/release/publish-update-index.mjs';
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
