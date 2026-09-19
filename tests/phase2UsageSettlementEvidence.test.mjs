import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';
import {
  CAPABILITY_STATUSES,
  loadAndValidateContract,
  validateEvidenceExtension,
} from '../bin/ci/validate-phase2-evidence.mjs';

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const extensionPath = path.join(
  repoRoot,
  'tests/fixtures/phase2-evidence/extensions/phase2-03b-usage-settlement.json'
);

const loadExtension = () => JSON.parse(readFileSync(extensionPath, 'utf8'));

describe('Phase2-03B Usage / Reservation / Settlement Evidence Contract', () => {
  it('validates extension schema and integrates with frozen contract baseline', () => {
    const loaded = loadAndValidateContract();
    const extension = loadExtension();
    const summary = validateEvidenceExtension(extension, loaded);

    expect(summary.anchors).toBe(17);
    expect(summary.records).toBe(29);
  });

  it('strictly restricts all anchor and record IDs to the phase2-03b-* namespace', () => {
    const extension = loadExtension();

    for (const anchor of extension.anchors) {
      expect(anchor.id).toMatch(/^phase2-03b-[a-z0-9-]+$/);
    }
    for (const record of extension.records) {
      expect(record.id).toMatch(/^phase2-03b-[a-z0-9-]+$/);
    }
  });

  it('enforces clean version provenance and forbids candidate-only symbols in v7.3.3 evidence', () => {
    const extension = loadExtension();
    const currentAnchors = extension.anchors.filter((a) => a.artifactId === 'current-bundled-v7-3-3');
    const candidateAnchors = extension.anchors.filter((a) => a.artifactId === 'candidate-release-v7-3-8');

    expect(currentAnchors.length).toBe(8);
    expect(candidateAnchors.length).toBe(8);

    // Provenance labeling check
    for (const anchor of currentAnchors) {
      expect(anchor.evidenceReference).toContain('@v7.3.3:');
      expect(anchor.evidenceReference).not.toContain('@v7.3.8:');
      // publishAttemptRecord was introduced in v7.3.8; it must NOT appear as a positive reference in v7.3.3 evidence
      expect(anchor.evidenceReference).not.toContain('publishAttemptRecord');
    }

    const currentAttemptAnchor = currentAnchors.find((a) => a.id === 'phase2-03b-current-usage-attempt-dispatch');
    expect(currentAttemptAnchor.evidenceReference).toContain('publishWithOutcome');
    expect(currentAttemptAnchor.evidenceReference).toContain('publishRecord');
    expect(currentAttemptAnchor.limitations[0]).toContain('publishAttemptRecord does not exist in v7.3.3');

    for (const anchor of candidateAnchors) {
      expect(anchor.evidenceReference).toContain('@v7.3.8:');
      expect(anchor.evidenceReference).not.toContain('@v7.3.3:');
    }

    const candidateAttemptAnchor = candidateAnchors.find((a) => a.id === 'phase2-03b-candidate-usage-attempt-dispatch');
    expect(candidateAttemptAnchor.evidenceReference).toContain('publishAttemptRecord');
  });

  it('enforces UsageRecord presence and absence claims in v7.3.3 and v7.3.8 anchors', () => {
    const extension = loadExtension();
    const currentAnchor = extension.anchors.find(
      (a) => a.id === 'phase2-03b-current-usage-record-structure'
    );
    const candidateAnchor = extension.anchors.find(
      (a) => a.id === 'phase2-03b-candidate-usage-record-structure'
    );

    expect(currentAnchor).toBeDefined();
    expect(candidateAnchor).toBeDefined();

    const expectedPresentFields = [
      'APIKey',
      'SessionID',
      'ParentSessionID',
      'AuthID',
      'AuthIndex',
      'Failure',
      'Detail',
      'Latency',
      'TTFT',
    ];

    const expectedAbsentCorrelationFields = [
      'RequestID',
      'TraceID',
      'AttemptID',
      'IdempotencyKey',
    ];

    for (const limitation of [currentAnchor.limitations[0], candidateAnchor.limitations[0]]) {
      for (const field of expectedPresentFields) {
        expect(limitation).toContain(field);
      }
      for (const absentField of expectedAbsentCorrelationFields) {
        expect(limitation).toContain(absentField);
      }
    }
  });

  it('classifies successful non-stream and stream usage observation as supported with limitations', () => {
    const extension = loadExtension();
    const records = new Map(extension.records.map((r) => [r.id, r]));

    const currentNonStream = records.get('phase2-03b-current-successful-non-stream-usage');
    const candidateNonStream = records.get('phase2-03b-candidate-successful-non-stream-usage');
    expect(currentNonStream.status).toBe('supported');
    expect(candidateNonStream.status).toBe('supported');
    expect(currentNonStream.pluginContract.schemaVersion).toBe(6);
    expect(candidateNonStream.pluginContract.schemaVersion).toBe(6);

    const currentStream = records.get('phase2-03b-current-successful-stream-usage');
    const candidateStream = records.get('phase2-03b-candidate-successful-stream-usage');
    expect(currentStream.status).toBe('supported');
    expect(candidateStream.status).toBe('supported');
    expect(currentStream.limitations[0]).toContain('Streaming token observation in v7.3.3 depends on upstream SSE usage chunks');
  });

  it('proves upstream failure and cancellation reporting behavior', () => {
    const extension = loadExtension();
    const records = new Map(extension.records.map((r) => [r.id, r]));

    const currentFailure = records.get('phase2-03b-current-upstream-failure-reporting');
    const candidateFailure = records.get('phase2-03b-candidate-upstream-failure-reporting');
    expect(currentFailure.status).toBe('supported');
    expect(candidateFailure.status).toBe('supported');
    expect(currentFailure.limitations[0]).toContain('Failed=true');
    expect(currentFailure.limitations[0]).toContain('zero token detail');

    const currentCancel = records.get('phase2-03b-current-cancellation-reporting');
    const candidateCancel = records.get('phase2-03b-candidate-cancellation-reporting');
    expect(currentCancel.status).toBe('supported');
    expect(candidateCancel.status).toBe('supported');
    expect(currentCancel.limitations[0]).toContain('context.WithoutCancel');
  });

  it('proves retry and fallback multi-attempt dispatch is partial and emits multiple callbacks', () => {
    const extension = loadExtension();
    const records = new Map(extension.records.map((r) => [r.id, r]));

    const currentRetry = records.get('phase2-03b-current-retry-multiple-attempts');
    const candidateRetry = records.get('phase2-03b-candidate-retry-multiple-attempts');
    expect(currentRetry.status).toBe('partial');
    expect(candidateRetry.status).toBe('partial');
    expect(currentRetry.limitations[0]).toContain('multiple callbacks occur for a single logical request');

    const currentExactCorrelation = records.get('phase2-03b-current-exact-request-correlation');
    const candidateExactCorrelation = records.get('phase2-03b-candidate-exact-request-correlation');
    expect(currentExactCorrelation.status).toBe('requires_upstream');
    expect(candidateExactCorrelation.status).toBe('requires_upstream');
  });

  it('proves zero-token and unknown usage handling preserves request counting', () => {
    const extension = loadExtension();
    const records = new Map(extension.records.map((r) => [r.id, r]));

    const currentZero = records.get('phase2-03b-current-zero-unknown-usage');
    const candidateZero = records.get('phase2-03b-candidate-zero-unknown-usage');
    expect(currentZero.status).toBe('supported');
    expect(candidateZero.status).toBe('supported');
    expect(currentZero.limitations[0]).toContain('EnsurePublished in v7.3.3 guarantees request counting');
  });

  it('proves plugin executor usage dispatch path is supported', () => {
    const extension = loadExtension();
    const records = new Map(extension.records.map((r) => [r.id, r]));

    const currentPluginExec = records.get('phase2-03b-current-plugin-executor-usage');
    const candidatePluginExec = records.get('phase2-03b-candidate-plugin-executor-usage');
    expect(currentPluginExec.status).toBe('supported');
    expect(candidatePluginExec.status).toBe('supported');
  });

  it('proves UsagePlugin failure, panic fuse, and asynchronous queue resilience', () => {
    const extension = loadExtension();
    const records = new Map(extension.records.map((r) => [r.id, r]));

    const currentResilience = records.get('phase2-03b-current-usage-plugin-resilience');
    const candidateResilience = records.get('phase2-03b-candidate-usage-plugin-resilience');
    expect(currentResilience.status).toBe('supported');
    expect(candidateResilience.status).toBe('supported');
    expect(currentResilience.limitations[0]).toContain('fuses the plugin without failing client requests');

    const currentDelivery = records.get('phase2-03b-current-exactly-once-delivery');
    const candidateDelivery = records.get('phase2-03b-candidate-exactly-once-delivery');
    expect(currentDelivery.status).toBe('unsupported');
    expect(candidateDelivery.status).toBe('unsupported');
    expect(currentDelivery.limitations[0]).toContain('best-effort in-memory observation without persistent journaling');
  });

  it('proves pre-request reservation and authoritative settlement primitives are unsupported', () => {
    const extension = loadExtension();
    const records = new Map(extension.records.map((r) => [r.id, r]));

    const currentReservation = records.get('phase2-03b-current-pre-request-reservation');
    const candidateReservation = records.get('phase2-03b-candidate-pre-request-reservation');
    expect(currentReservation.status).toBe('unsupported');
    expect(candidateReservation.status).toBe('unsupported');
    expect(currentReservation.limitations[0]).toContain('no pre-request token reservation or admission hook');

    const currentSettlement = records.get('phase2-03b-current-authoritative-settlement');
    const candidateSettlement = records.get('phase2-03b-candidate-authoritative-settlement');
    expect(currentSettlement.status).toBe('unsupported');
    expect(candidateSettlement.status).toBe('unsupported');
    expect(currentSettlement.limitations[0]).toContain('no two-phase commit, rollback, or idempotent settlement primitives');
  });

  it('bounds unnegotiated External runtime evidence to unknown/partial without pluginContract', () => {
    const extension = loadExtension();
    const externalRecords = extension.records.filter((r) => r.artifactId === 'external-unnegotiated');

    expect(externalRecords.length).toBe(3);
    for (const record of externalRecords) {
      expect(['unknown', 'partial']).toContain(record.status);
      expect(record.pluginContract).toBeNull();
      expect(record.deploymentMode).toBe('external');
      expect(CAPABILITY_STATUSES).toContain(record.status);
    }
  });

  it('strictly ensures no hardcoded machine or developer paths exist in evidence files', () => {
    const extensionFileContent = readFileSync(extensionPath, 'utf8');
    const localUserPrefix = String.fromCharCode(47) + 'Users' + String.fromCharCode(47);
    const localHomePrefix = String.fromCharCode(47) + 'home' + String.fromCharCode(47);

    expect(extensionFileContent).not.toContain(localUserPrefix);
    expect(extensionFileContent).not.toContain(localHomePrefix);
  });
});
