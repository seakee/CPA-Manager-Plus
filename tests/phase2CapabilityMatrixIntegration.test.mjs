import { existsSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';
import {
  CAPABILITY_STATUSES,
  loadAndValidateContract,
  validateEvidenceExtension,
} from '../bin/ci/validate-phase2-evidence.mjs';

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const manifestPath = path.join(
  repoRoot,
  'tests/fixtures/phase2-evidence/phase2-exit-decisions.json'
);
const architectureDocPath = path.join(
  repoRoot,
  'docs/architecture/phase2-evidence/04-capability-matrix-go-no-go.md'
);

const PRODUCT_DECISIONS = ['go', 'limited', 'no_go', 'deferred'];
const STATUS_INPUTS = {
  supported: new Set(['supported']),
  partial: new Set(['supported', 'partial']),
  unsupported: new Set(['unsupported']),
  unknown: new Set(['unknown']),
  requires_upstream: new Set(['requires_upstream']),
};
const PRODUCT_STATUS = {
  go: new Set(['supported']),
  limited: new Set(['supported', 'partial']),
  no_go: new Set(['unsupported']),
  deferred: new Set(['unknown', 'requires_upstream']),
};

const readJSON = (filePath) => JSON.parse(readFileSync(filePath, 'utf8'));
const loadManifest = () => readJSON(manifestPath);

const loadAcceptedEvidence = (manifest = loadManifest()) => {
  const context = loadAndValidateContract();
  const recordById = new Map(
    context.contract.capabilityRecords.map((record) => [record.id, record])
  );

  for (const source of manifest.acceptedEvidenceSources) {
    const sourcePath = path.join(repoRoot, source.path);
    expect(existsSync(sourcePath), source.path).toBe(true);
    if (!source.path.includes('/extensions/')) continue;

    const extension = readJSON(sourcePath);
    expect(() => validateEvidenceExtension(extension, context), source.path).not.toThrow();
    for (const record of extension.records) {
      expect(recordById.has(record.id), record.id).toBe(false);
      recordById.set(record.id, record);
    }
  }

  return { ...context, recordById };
};

const validateDecision = (decision, recordById, artifactById) => {
  expect(CAPABILITY_STATUSES, decision.id).toContain(decision.capabilityStatus);
  expect(PRODUCT_DECISIONS, decision.id).toContain(decision.productDecision);
  expect(PRODUCT_STATUS[decision.productDecision].has(decision.capabilityStatus), decision.id).toBe(
    true
  );
  expect(decision.evidenceRecordRefs.length, decision.id).toBeGreaterThan(0);
  expect(decision.limitations.length, decision.id).toBeGreaterThan(0);
  expect(decision.failureBehavior.length, decision.id).toBeGreaterThan(0);
  expect(decision.fallback.length, decision.id).toBeGreaterThan(0);
  expect(decision.downstreamOwners.length, decision.id).toBeGreaterThan(0);

  const artifact = artifactById.get(decision.artifactId);
  expect(artifact, decision.id).toBeDefined();
  expect(decision.cpaVersion, decision.id).toBe(artifact.cpaVersion);
  expect(decision.deploymentMode, decision.id).toBe(artifact.deploymentMode);

  const acceptedStatuses = STATUS_INPUTS[decision.capabilityStatus];
  for (const reference of decision.evidenceRecordRefs) {
    const record = recordById.get(reference);
    expect(record, `${decision.id}: ${reference}`).toBeDefined();
    expect(record.artifactId, `${decision.id}: ${reference}`).toBe(decision.artifactId);
    expect(
      acceptedStatuses.has(record.status),
      `${decision.id}: ${reference} (${record.status})`
    ).toBe(true);

    if (record.pluginContract !== null) {
      expect(
        decision.requiredPlugin.schemaVersion,
        `${decision.id}: ${reference} Plugin schema`
      ).toBe(record.pluginContract.schemaVersion);
      for (const [configKey, requiredValue] of Object.entries(record.pluginContract.config)) {
        expect(
          Object.hasOwn(decision.requiredPlugin.config, configKey),
          `${decision.id}: ${reference} missing Plugin config ${configKey}`
        ).toBe(true);
        expect(
          decision.requiredPlugin.config[configKey],
          `${decision.id}: ${reference} Plugin config ${configKey}`
        ).toEqual(requiredValue);
      }
    }
  }

  if (decision.artifactId === 'external-unnegotiated') {
    expect(decision.requiredPlugin, decision.id).toEqual({
      schemaVersion: null,
      config: null,
      capabilityGeneration: 'negotiated-current',
    });
  } else {
    expect(decision.requiredPlugin.schemaVersion, decision.id).toBe(6);
    expect(decision.requiredPlugin.config, decision.id).not.toBeNull();
    expect(decision.requiredPlugin.capabilityGeneration, decision.id).toBe('artifact-pinned');
  }
};

describe('Phase2-04 Capability Matrix Integration / Go-No-Go', () => {
  it('integrates only the five independently accepted evidence sources', () => {
    const manifest = loadManifest();
    const context = loadAcceptedEvidence(manifest);

    expect(manifest.integrationBaseline).toBe('d3ddec55346085edae6cc36527106b650f5f438d');
    expect(manifest.evidenceContract).toBe('tests/fixtures/phase2-evidence/contract.json');
    expect(manifest.capabilityStatusVocabulary).toEqual(CAPABILITY_STATUSES);
    expect(manifest.productDecisionVocabulary).toEqual(PRODUCT_DECISIONS);
    expect(manifest.acceptedEvidenceSources.map((source) => source.slice)).toEqual([
      'phase2-01',
      'phase2-02a',
      'phase2-02b',
      'phase2-03a',
      'phase2-03b',
    ]);
    expect(context.recordById.size).toBe(87);
  });

  it('keeps current, candidate, and External artifacts isolated without a bundle promotion', () => {
    const manifest = loadManifest();
    const { contract } = loadAndValidateContract();
    const expectedArtifacts = new Map(
      contract.artifacts.map((artifact) => [artifact.id, artifact])
    );

    expect(manifest.artifacts.map((artifact) => artifact.artifactId)).toEqual([
      'current-bundled-v7-3-3',
      'candidate-release-v7-3-8',
      'external-unnegotiated',
    ]);

    for (const artifact of manifest.artifacts) {
      const expected = expectedArtifacts.get(artifact.artifactId);
      expect(expected, artifact.artifactId).toBeDefined();
      expect(artifact.cpaVersion, artifact.artifactId).toBe(expected.cpaVersion);
      expect(artifact.deploymentMode, artifact.artifactId).toBe(expected.deploymentMode);
    }

    expect(manifest.artifacts[0]).toMatchObject({
      productRole: 'current_bundled',
      bundleDecision: 'remain_current',
    });
    expect(manifest.artifacts[1]).toMatchObject({
      productRole: 'candidate_evidence_only',
      bundleDecision: 'no_upgrade_in_phase2',
    });
    expect(manifest.artifacts[2]).toMatchObject({
      productRole: 'advanced_compatibility_unnegotiated',
      pluginAbiSchemaVersion: null,
    });
  });

  it('keeps every product decision traceable, monotonic, and artifact-scoped', () => {
    const manifest = loadManifest();
    const { recordById, artifactById } = loadAcceptedEvidence(manifest);
    const decisionIds = manifest.decisions.map((decision) => decision.id);

    expect(manifest.decisions).toHaveLength(16);
    expect(new Set(decisionIds).size).toBe(decisionIds.length);
    for (const decision of manifest.decisions) {
      validateDecision(decision, recordById, artifactById);
    }

    const promoted = structuredClone(
      manifest.decisions.find((decision) => decision.id === 'current-caller-identity-source')
    );
    promoted.capabilityStatus = 'supported';
    promoted.productDecision = 'go';
    expect(() => validateDecision(promoted, recordById, artifactById)).toThrow();

    const crossArtifact = structuredClone(
      manifest.decisions.find(
        (decision) => decision.id === 'current-request-correlation-continuity'
      )
    );
    crossArtifact.evidenceRecordRefs.push('phase2-03a-candidate-request-correlation-continuity');
    expect(() => validateDecision(crossArtifact, recordById, artifactById)).toThrow();

    const crossConfig = structuredClone(
      manifest.decisions.find(
        (decision) => decision.id === 'candidate-hard-routing-across-priorities-opt-in'
      )
    );
    crossConfig.requiredPlugin.config.scheduler_across_priorities = false;
    expect(() => validateDecision(crossConfig, recordById, artifactById)).toThrow();
  });

  it('separates current default, candidate default, and candidate opt-in routing limits', () => {
    const manifest = loadManifest();
    const decisions = new Map(manifest.decisions.map((decision) => [decision.id, decision]));
    const current = decisions.get('current-hard-routing-default');
    const candidateDefault = decisions.get('candidate-hard-routing-default');
    const candidateOptIn = decisions.get('candidate-hard-routing-across-priorities-opt-in');

    for (const decision of [current, candidateDefault, candidateOptIn]) {
      expect(decision).toMatchObject({
        area: 'credential_selection',
        capabilityStatus: 'partial',
        productDecision: 'limited',
      });
      expect(decision.failureBehavior).toMatch(/fail closed/i);
      expect(
        decision.evidenceRecordRefs.some((reference) => reference.includes('pinned-auth'))
      ).toBe(true);
      expect(
        decision.evidenceRecordRefs.some((reference) => reference.includes('stream-retry'))
      ).toBe(true);
    }

    expect(current.requiredPlugin.config).toEqual({ scheduler: true });
    expect(candidateDefault.requiredPlugin.config).toEqual({
      scheduler: true,
      scheduler_across_priorities: false,
    });
    expect(candidateDefault.evidenceRecordRefs).not.toContain(
      'phase2-02b-candidate-pre-filter-exclusion'
    );
    expect(candidateOptIn.requiredPlugin.config).toEqual({
      scheduler: true,
      scheduler_across_priorities: true,
    });
    expect(candidateOptIn.evidenceRecordRefs).toContain(
      'phase2-02b-candidate-across-priorities-opt-in'
    );
  });

  it('keeps caller, request, attempt, and terminal meanings independent', () => {
    const decisions = new Map(loadManifest().decisions.map((decision) => [decision.id, decision]));

    for (const id of ['current-caller-identity-source', 'candidate-caller-identity-source']) {
      expect(decisions.get(id)).toMatchObject({
        area: 'caller_identity',
        capabilityStatus: 'partial',
        productDecision: 'limited',
      });
      expect(decisions.get(id).limitations.join(' ')).toMatch(/Canonical APIKeyID/);
    }

    for (const id of [
      'current-request-correlation-continuity',
      'candidate-request-correlation-continuity',
    ]) {
      expect(decisions.get(id)).toMatchObject({
        area: 'request_correlation',
        capabilityStatus: 'supported',
        productDecision: 'go',
      });
    }

    for (const id of ['current-attempt-correlation', 'candidate-attempt-correlation']) {
      expect(decisions.get(id)).toMatchObject({
        area: 'attempt_correlation',
        capabilityStatus: 'requires_upstream',
        productDecision: 'deferred',
      });
      expect(decisions.get(id).upstreamDependencies).toContain(
        'upstream-attempt-and-usage-correlation'
      );
      expect(decisions.get(id).requiredPlugin.config).toEqual({
        request_lifecycle_plugin: true,
        interceptor: true,
        usage_plugin: true,
      });
    }

    for (const id of ['current-terminal-observation', 'candidate-terminal-observation']) {
      expect(decisions.get(id)).toMatchObject({
        area: 'terminal_observation',
        capabilityStatus: 'partial',
        productDecision: 'limited',
      });
      expect(decisions.get(id).failureBehavior).toMatch(/unknown/i);
    }
  });

  it('allows observed usage while rejecting precise token and cost hard quota', () => {
    const decisions = new Map(loadManifest().decisions.map((decision) => [decision.id, decision]));

    for (const id of ['current-usage-observation', 'candidate-usage-observation']) {
      expect(decisions.get(id)).toMatchObject({
        area: 'usage_observation',
        capabilityStatus: 'partial',
        productDecision: 'limited',
      });
      expect(decisions.get(id).fallback).toMatch(/observed\/notify/i);
    }

    for (const id of ['current-precise-token-cost-quota', 'candidate-precise-token-cost-quota']) {
      const decision = decisions.get(id);
      expect(decision).toMatchObject({
        area: 'reservation_settlement',
        capabilityStatus: 'unsupported',
        productDecision: 'no_go',
      });
      expect(
        decision.evidenceRecordRefs.some((reference) => reference.includes('exactly-once'))
      ).toBe(true);
      expect(
        decision.evidenceRecordRefs.some((reference) =>
          reference.includes('pre-request-reservation')
        )
      ).toBe(true);
      expect(
        decision.evidenceRecordRefs.some((reference) =>
          reference.includes('authoritative-settlement')
        )
      ).toBe(true);
      expect(decision.failureBehavior).toMatch(/remain disabled/i);
    }
  });

  it('fails unnegotiated or stale External capability state closed', () => {
    const manifest = loadManifest();
    const external = manifest.decisions.find(
      (decision) => decision.id === 'external-capability-negotiation'
    );

    expect(external).toMatchObject({
      artifactId: 'external-unnegotiated',
      cpaVersion: null,
      deploymentMode: 'external',
      capabilityStatus: 'unknown',
      productDecision: 'deferred',
    });
    expect(external.evidenceRecordRefs).toHaveLength(12);
    expect(external.failureBehavior).toMatch(/fail closed/i);
    expect(manifest.externalNegotiation).toEqual({
      requiredFields: [
        'artifact_id',
        'cpa_version',
        'deployment_mode',
        'plugin_abi_schema_version',
        'enabled_plugin_config',
        'capability_generation',
      ],
      unknownBehavior: 'fail_closed',
      staleGenerationBehavior: 'fail_closed',
      inheritEmbeddedEvidence: false,
    });
  });

  it('maps every upstream dependency and downstream handoff to an explicit owner', () => {
    const manifest = loadManifest();
    const { recordById } = loadAcceptedEvidence(manifest);

    expect(manifest.upstreamDependencies.map((dependency) => dependency.id)).toEqual([
      'upstream-attempt-and-usage-correlation',
      'upstream-durable-observation-delivery',
      'upstream-reservation-and-settlement',
      'external-capability-negotiation-contract',
    ]);
    for (const dependency of manifest.upstreamDependencies) {
      expect(dependency.requiredContract.length, dependency.id).toBeGreaterThan(0);
      expect(dependency.owners.length, dependency.id).toBeGreaterThan(0);
      for (const reference of dependency.evidenceRecordRefs) {
        expect(recordById.has(reference), `${dependency.id}: ${reference}`).toBe(true);
      }
    }

    expect(manifest.downstreamHandoffs.map((handoff) => handoff.id)).toEqual([
      'phase3-g1',
      'phase3-g2',
      'phase5-r1',
      'phase5-r2-r4',
      'runtime-bridge',
      'release-matrix',
    ]);
    for (const handoff of manifest.downstreamHandoffs) {
      expect(handoff.mayConsume.length, handoff.id).toBeGreaterThan(0);
      expect(handoff.mustNotAssume.length, handoff.id).toBeGreaterThan(0);
      expect(handoff.requiredAction.length, handoff.id).toBeGreaterThan(0);
    }
    expect(
      manifest.downstreamHandoffs.find((handoff) => handoff.id === 'phase3-g2').requiredAction
    ).toContain('usage_events');
  });

  it('keeps the Phase2 exit gate closed until required checks and independent acceptance pass', () => {
    const manifest = loadManifest();

    expect(manifest.phase2Exit).toEqual({
      status: 'ready_for_independent_acceptance',
      result: 'go_with_explicit_limits',
      blockingContradictions: [],
      requiredAcceptance: ['required_checks_green', 'independent_acceptance'],
      phase3Gate: 'closed_until_independent_acceptance',
      bundleUpgrade: false,
      productBehaviorChanged: false,
    });
  });

  it('publishes every decision in the architecture document with repository-relative links', () => {
    const manifest = loadManifest();
    const document = readFileSync(architectureDocPath, 'utf8');

    expect(document).toContain(
      '../../../tests/fixtures/phase2-evidence/phase2-exit-decisions.json'
    );
    expect(document).toContain('../../../tests/phase2CapabilityMatrixIntegration.test.mjs');
    expect(document).toMatch(/Go\/No-Go/);
    expect(document).toContain('Upstream dependency');
    expect(document).toContain('Downstream handoff');
    for (const decision of manifest.decisions) {
      expect(document, decision.id).toContain(`\`${decision.id}\``);
    }

    const localUserPrefix = String.fromCharCode(47) + 'Users' + String.fromCharCode(47);
    const localHomePrefix = String.fromCharCode(47) + 'home' + String.fromCharCode(47);
    const localFileProtocol = 'file:' + String.fromCharCode(47) + String.fromCharCode(47);
    expect(document).not.toContain(localUserPrefix);
    expect(document).not.toContain(localHomePrefix);
    expect(document).not.toContain(localFileProtocol);
  });
});
