import { execFileSync } from 'node:child_process';
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
  validateBundledDockerfile,
  validateCapabilityRecord,
  validateContract,
  validateEvidenceExtension,
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
    const { contract, anchorById, recordById } = loaded();
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

    const schemaAnchor = anchorById.get('plugin-abi-schema-v6');
    expect(schemaAnchor).toMatchObject({
      path: 'sdk/pluginabi/types.go',
      symbols: ['SchemaVersion uint32 = 6'],
    });
    expect(schemaAnchor.versions.map((version) => version.artifactId)).toEqual([
      'current-bundled-v7-3-3',
      'candidate-release-v7-3-8',
    ]);
    for (const record of contract.capabilityRecords.filter(
      (candidate) => candidate.pluginContract !== null
    )) {
      expect(record.pluginContract.schemaVersion).toBe(FROZEN_BASELINE.pluginABISchemaVersion);
      expect(record.evidenceRefs).toContain('plugin-abi-schema-v6');
    }
    expect(
      recordById.get('current-scheduler-candidate-visibility-default').pluginContract.schemaVersion
    ).toBe(
      recordById.get('candidate-scheduler-candidate-visibility-default').pluginContract
        .schemaVersion
    );
    expect(
      contract.schedulerAcrossPrioritiesProvenance.releaseAncestry.find(
        (release) => release.version === 'v7.3.3'
      ).includesCommit
    ).toBe(false);
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

    const missingPluginSchema = structuredClone(contract.capabilityRecords[0]);
    missingPluginSchema.pluginContract.schemaVersion = null;
    expect(() =>
      validateCapabilityRecord(missingPluginSchema, { artifactById, anchorById })
    ).toThrow('known plugin contract requires an integer schemaVersion');

    const invalidPluginSchemaContract = structuredClone(contract);
    invalidPluginSchemaContract.capabilityRecords[0].pluginContract.schemaVersion = null;
    expect(() => validateContract(invalidPluginSchemaContract, schema)).toThrow(
      'expected type integer'
    );
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

  it('lets C2/C3 append independent anchors and records without mutating the baseline', () => {
    const context = loaded();
    const baselineBefore = JSON.stringify(context.contract);
    const c2Record = structuredClone(context.recordById.get('candidate-request-terminal-callback'));
    c2Record.id = 'phase2-02-candidate-selection-evidence';
    c2Record.capability = 'candidate_selection_evidence';
    c2Record.status = 'partial';
    c2Record.evidenceKind.push('black-box');
    c2Record.evidenceRefs.push('phase2-02-candidate-selection-black-box');
    const c2Extension = {
      anchors: [
        {
          id: 'phase2-02-candidate-selection-black-box',
          artifactId: 'candidate-release-v7-3-8',
          evidenceKind: 'black-box',
          evidenceReference: 'tests/fixtures/phase2-02/candidate-selection.test.mjs#selects-target',
          limitations: ['Fixture-only observation; no Hard Routing decision is implied.'],
        },
      ],
      records: [c2Record],
    };

    const c3Record = structuredClone(context.recordById.get('candidate-completed-usage-callback'));
    c3Record.id = 'phase2-03-usage-settlement-evidence';
    c3Record.capability = 'usage_settlement_evidence';
    c3Record.status = 'requires_upstream';
    c3Record.evidenceKind.push('integration');
    c3Record.evidenceRefs.push('phase2-03-usage-integration');
    const c3Extension = {
      anchors: [
        {
          id: 'phase2-03-usage-integration',
          artifactId: 'candidate-release-v7-3-8',
          evidenceKind: 'integration',
          evidenceReference: 'tests/fixtures/phase2-03/usage.test.mjs#cancel-and-duplicate',
          limitations: ['Fixture-only observation; no Quota enforcement decision is implied.'],
        },
      ],
      records: [c3Record],
    };

    expect(validateEvidenceExtension(c2Extension, context)).toEqual({ anchors: 1, records: 1 });
    expect(validateEvidenceExtension(c3Extension, context)).toEqual({ anchors: 1, records: 1 });
    expect(JSON.stringify(context.contract)).toBe(baselineBefore);
    expect(context.anchorById.has(c2Extension.anchors[0].id)).toBe(false);
    expect(context.recordById.has(c2Extension.records[0].id)).toBe(false);

    const scratch = mkdtempSync(path.join(tmpdir(), 'cpamp-phase2-extension-'));
    try {
      const extensionPath = path.join(scratch, 'c2-evidence.json');
      writeFileSync(extensionPath, JSON.stringify(c2Extension));
      const result = JSON.parse(
        execFileSync(process.execPath, [harnessPath, '--evidence', extensionPath, '--json'], {
          encoding: 'utf8',
        })
      );
      expect(result.evidenceExtension).toEqual({ anchors: 1, records: 1 });
    } finally {
      rmSync(scratch, { recursive: true, force: true });
    }
  });

  it('rejects extension attempts to replace baseline IDs, artifacts, or contract fields', () => {
    const context = loaded();
    const record = structuredClone(context.recordById.get('candidate-request-terminal-callback'));
    record.id = 'phase2-extension-record';
    record.capability = 'extension_contract_guard';
    record.evidenceKind.push('black-box');
    record.evidenceRefs.push('phase2-extension-black-box');
    const extension = {
      anchors: [
        {
          id: 'phase2-extension-black-box',
          artifactId: 'candidate-release-v7-3-8',
          evidenceKind: 'black-box',
          evidenceReference: 'tests/fixtures/phase2-extension.test.mjs#guard',
          limitations: ['Contract test evidence only.'],
        },
      ],
      records: [record],
    };

    expect(() =>
      validateEvidenceExtension({ ...extension, artifacts: context.contract.artifacts }, context)
    ).toThrow('unexpected property artifacts');

    const baselineAnchorOverride = structuredClone(extension);
    baselineAnchorOverride.anchors[0].id = 'plugin-abi-schema-v6';
    baselineAnchorOverride.records[0].evidenceRefs[
      baselineAnchorOverride.records[0].evidenceRefs.length - 1
    ] = 'plugin-abi-schema-v6';
    expect(() => validateEvidenceExtension(baselineAnchorOverride, context)).toThrow(
      'Duplicate evidence anchor id: plugin-abi-schema-v6'
    );

    const baselineRecordOverride = structuredClone(extension);
    baselineRecordOverride.records[0].id = 'candidate-request-terminal-callback';
    expect(() => validateEvidenceExtension(baselineRecordOverride, context)).toThrow(
      'Duplicate capability record id: candidate-request-terminal-callback'
    );

    const unknownAnchorArtifact = structuredClone(extension);
    unknownAnchorArtifact.anchors[0].artifactId = 'moving-latest';
    expect(() => validateEvidenceExtension(unknownAnchorArtifact, context)).toThrow(
      'phase2-extension-black-box: unknown artifact moving-latest'
    );

    const unknownRecordArtifact = structuredClone(extension);
    unknownRecordArtifact.records[0].artifactId = 'moving-latest';
    expect(() => validateEvidenceExtension(unknownRecordArtifact, context)).toThrow(
      'phase2-extension-record: unknown artifact moving-latest'
    );

    const duplicateExtensionAnchor = structuredClone(extension);
    duplicateExtensionAnchor.anchors.push(structuredClone(duplicateExtensionAnchor.anchors[0]));
    expect(() => validateEvidenceExtension(duplicateExtensionAnchor, context)).toThrow(
      'Duplicate evidence anchor id: phase2-extension-black-box'
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
