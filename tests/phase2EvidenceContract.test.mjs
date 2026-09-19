import { createHash } from 'node:crypto';
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';
import {
  CANONICAL_LIFECYCLE_ORDER,
  CAPABILITY_STATUSES,
  FROZEN_BASELINE,
  loadAndValidateContract,
  validateAdditionalRecords,
  validateBundledDockerfile,
  validateCapabilityRecord,
  validateContract,
  verifyArtifactDigest,
} from '../bin/ci/validate-phase2-evidence.mjs';

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const harnessPath = path.join(repoRoot, 'bin/ci/validate-phase2-evidence.mjs');
const loaded = () => loadAndValidateContract();

describe('Phase2 evidence contract', () => {
  it('pins current, candidate, and External evidence without cross-version promotion', () => {
    const { contract, artifactById } = loaded();
    const current = artifactById.get('current-bundled-v7-3-3');
    const candidate = artifactById.get('candidate-release-v7-3-8');
    const external = artifactById.get('external-unnegotiated');

    expect(contract.cpampBaselineCommit).toBe(FROZEN_BASELINE.cpampCommit);
    expect(current).toMatchObject({
      classification: 'current_bundled',
      deploymentMode: 'embedded',
      cpaVersion: 'v7.3.3',
      sourceCommit: FROZEN_BASELINE.releases['v7.3.3'].sourceCommit,
    });
    expect(candidate).toMatchObject({
      classification: 'candidate_release',
      deploymentMode: 'release-artifact-fixture',
      cpaVersion: 'v7.3.8',
      sourceCommit: FROZEN_BASELINE.releases['v7.3.8'].sourceCommit,
    });
    expect(external).toMatchObject({
      classification: 'external',
      deploymentMode: 'external',
      versionKnown: false,
      cpaVersion: null,
      pluginAvailability: 'unknown',
    });
    expect(candidate.classification).not.toBe('current_bundled');
  });

  it('keeps Dockerfile.runtime on the current v7.3.3 bundle', () => {
    const { contract } = loaded();
    expect(() => validateBundledDockerfile(contract)).not.toThrow();
    const dockerfile = readFileSync(path.join(repoRoot, 'Dockerfile.runtime'), 'utf8');
    expect(dockerfile).toContain('cpa_version="7.3.3"');
    expect(dockerfile).not.toContain('cpa_version="7.3.8"');
  });

  it('freezes SchedulerAcrossPriorities ancestry and default/opt-in scenarios', () => {
    const { contract, recordById } = loaded();
    expect(contract.schedulerAcrossPrioritiesProvenance).toMatchObject({
      commit: FROZEN_BASELINE.schedulerAcrossPrioritiesCommit,
      default: false,
    });
    expect(contract.schedulerAcrossPrioritiesProvenance.releaseAncestry).toEqual([
      {
        version: 'v7.3.3',
        sourceCommit: FROZEN_BASELINE.releases['v7.3.3'].sourceCommit,
        includesCommit: false,
      },
      {
        version: 'v7.3.7',
        sourceCommit: FROZEN_BASELINE.releases['v7.3.7'].sourceCommit,
        includesCommit: true,
      },
      {
        version: 'v7.3.8',
        sourceCommit: FROZEN_BASELINE.releases['v7.3.8'].sourceCommit,
        includesCommit: true,
      },
    ]);
    expect(
      recordById.get('current-scheduler-candidate-visibility-default').pluginContract.config
    ).toMatchObject({ scheduler_across_priorities: false });
    expect(
      recordById.get('candidate-scheduler-candidate-visibility-default').pluginContract.config
    ).toMatchObject({ scheduler_across_priorities: false });
    expect(
      recordById.get('candidate-scheduler-candidate-visibility-opt-in').pluginContract.config
    ).toMatchObject({ scheduler_across_priorities: true });
    expect(recordById.get('candidate-scheduler-candidate-visibility-opt-in').status).toBe(
      'partial'
    );
  });

  it('validates the canonical lifecycle order and every stage evidence boundary', () => {
    const { contract, anchorById, artifactById } = loaded();
    expect(contract.requestLifecycle.canonicalOrder).toEqual(CANONICAL_LIFECYCLE_ORDER);
    expect(contract.requestLifecycle.stages.map((stage) => stage.id)).toEqual(
      CANONICAL_LIFECYCLE_ORDER
    );
    for (const [ordinal, stage] of contract.requestLifecycle.stages.entries()) {
      expect(stage.ordinal).toBe(ordinal);
      expect(stage.evidenceRefs.length).toBeGreaterThan(0);
      expect(stage.limitations.length).toBeGreaterThan(0);
      expect(Object.keys(stage.hostAccess).sort()).toEqual([
        'blockable',
        'mutable',
        'observable',
        'terminalOnly',
      ]);
      expect(Object.keys(stage.pluginAccess).sort()).toEqual([
        'blockable',
        'mutable',
        'observable',
        'terminalOnly',
      ]);
      stage.evidenceRefs.forEach((reference) => expect(anchorById.has(reference)).toBe(true));
      stage.artifactScope.forEach((artifactId) => expect(artifactById.has(artifactId)).toBe(true));
    }
    expect(contract.requestLifecycle.observedOrderingConstraints.join('\n')).toContain(
      'Usage publication is attempt-coupled'
    );
  });

  it('accepts only the five frozen capability statuses', () => {
    const { contract, schema, artifactById, anchorById } = loaded();
    expect(contract.capabilityStatuses).toEqual(CAPABILITY_STATUSES);
    expect(schema.$defs.capabilityStatus.enum).toEqual(CAPABILITY_STATUSES);

    const invalid = structuredClone(contract.capabilityRecords[0]);
    invalid.status = 'probably';
    expect(() => validateCapabilityRecord(invalid, { artifactById, anchorById })).toThrow(
      'illegal capability status probably'
    );

    const invalidSchemaContract = structuredClone(contract);
    invalidSchemaContract.capabilityRecords[0].status = 'mostly works';
    expect(() => validateContract(invalidSchemaContract, schema)).toThrow('invalid enum value');
  });

  it('rejects automatic promotion of unknown External capability evidence', () => {
    const context = loaded();
    const external = structuredClone(
      context.recordById.get('external-scheduler-candidate-visibility')
    );
    external.id = 'external-invalid-promotion';
    external.status = 'supported';
    expect(() =>
      validateCapabilityRecord(external, {
        artifactById: context.artifactById,
        anchorById: context.anchorById,
      })
    ).toThrow('unversioned External evidence cannot be promoted');
  });

  it('lets C2/C3 validate additional records against the same vocabulary and evidence refs', () => {
    const context = loaded();
    const record = structuredClone(context.recordById.get('candidate-request-terminal-callback'));
    record.id = 'phase2-follow-up-fixture-record';
    record.capability = 'follow_up_fixture_contract';
    record.status = 'requires_upstream';
    expect(validateAdditionalRecords([record], context)).toBe(1);

    const invalid = { ...record, id: 'phase2-invalid-extra-record', extraGuess: true };
    expect(() => validateAdditionalRecords([invalid], context)).toThrow(
      'unexpected property extraGuess'
    );

    const unsupportedClaim = {
      ...record,
      id: 'phase2-unreferenced-evidence-kind',
      evidenceKind: [...record.evidenceKind, 'black-box'],
    };
    expect(() => validateAdditionalRecords([unsupportedClaim], context)).toThrow(
      'evidence kind black-box has no matching evidence reference'
    );
  });

  it('verifies locally supplied release artifacts by exact SHA256 and fails closed', async () => {
    const { contract } = loaded();
    const scratch = mkdtempSync(path.join(tmpdir(), 'cpamp-phase2-evidence-'));
    try {
      const localArtifact = path.join(scratch, 'candidate.tar.gz');
      const payload = Buffer.from('deterministic local artifact fixture');
      writeFileSync(localArtifact, payload);
      const digest = createHash('sha256').update(payload).digest('hex');
      const syntheticContract = structuredClone(contract);
      const asset = syntheticContract.artifacts
        .flatMap((artifact) => artifact.releaseAssets)
        .find((candidate) => candidate.id === 'candidate-linux-amd64');
      asset.sha256 = digest;

      await expect(
        verifyArtifactDigest(syntheticContract, 'candidate-linux-amd64', localArtifact)
      ).resolves.toMatchObject({ sha256: digest });
      asset.sha256 = '0'.repeat(64);
      await expect(
        verifyArtifactDigest(syntheticContract, 'candidate-linux-amd64', localArtifact)
      ).rejects.toThrow('SHA256 mismatch');
    } finally {
      rmSync(scratch, { recursive: true, force: true });
    }
  });

  it('keeps the required harness offline and free of production startup hooks', () => {
    const source = readFileSync(harnessPath, 'utf8');
    expect(source).not.toMatch(/\bfetch\s*\(/);
    expect(source).not.toMatch(/https?\.get\s*\(/);
    expect(source).not.toMatch(/execFileSync\(['"](?:gh|curl|wget)['"]/);
    expect(readFileSync(path.join(repoRoot, 'package.json'), 'utf8')).toContain(
      '"evidence:phase2": "node bin/ci/validate-phase2-evidence.mjs"'
    );
  });
});
