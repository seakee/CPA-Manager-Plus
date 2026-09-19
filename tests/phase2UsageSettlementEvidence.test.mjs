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
const cpaSourcePath = '/Users/seakee/WorkSpace/Golang/src/github.com/seakee/CPA';

const loadExtension = () => JSON.parse(readFileSync(extensionPath, 'utf8'));

describe('Phase2-03B Usage / Reservation / Settlement Evidence', () => {
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

  it('proves present and absent fields in UsageRecord for v7.3.3 and v7.3.8', () => {
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
      'AuthType',
      'Source',
      'ReasoningEffort',
      'ServiceTier',
      'Generate',
      'RequestedAt',
      'Latency',
      'TTFT',
      'Failed',
      'Failure',
      'Detail',
      'ResponseHeaders',
    ];

    const expectedAbsentCorrelationFields = [
      'RequestID',
      'TraceID',
      'AttemptID',
      'IdempotencyKey',
    ];

    for (const limitation of [currentAnchor.limitations[0], candidateAnchor.limitations[0]]) {
      for (const field of expectedPresentFields) {
        if (['APIKey', 'SessionID', 'ParentSessionID', 'AuthID', 'AuthIndex', 'Failure', 'Detail', 'Latency', 'TTFT'].includes(field)) {
          expect(limitation).toContain(field);
        }
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
    expect(currentStream.limitations[0]).toContain('Streaming token observation depends on upstream SSE usage chunks');
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
    expect(currentZero.limitations[0]).toContain('EnsurePublished guarantees request counting');
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

  it('verifies CPA source code invariants when local checkout is present', () => {
    try {
      const typesSource = readFileSync(path.join(cpaSourcePath, 'sdk/pluginapi/types.go'), 'utf8');
      const managerSource = readFileSync(path.join(cpaSourcePath, 'sdk/cliproxy/usage/manager.go'), 'utf8');
      const adapterSource = readFileSync(
        path.join(cpaSourcePath, 'internal/pluginhost/adapters_usage_translation.go'),
        'utf8'
      );
      const helpersSource = readFileSync(
        path.join(cpaSourcePath, 'internal/runtime/executor/helps/usage_helpers.go'),
        'utf8'
      );

      // UsagePlugin interface signature: fire-and-forget, no error return
      expect(typesSource).toContain('type UsagePlugin interface {');
      expect(typesSource).toContain('HandleUsage(context.Context, UsageRecord)');
      expect(typesSource).not.toMatch(/HandleUsage\(context\.Context,\s*UsageRecord\)\s*error/);

      // UsageRecord field checks: RequestID/TraceID/AttemptID absent from UsageRecord struct
      const usageRecordSlice = typesSource.slice(
        typesSource.indexOf('type UsageRecord struct {'),
        typesSource.indexOf('type UsageFailure struct {')
      );
      expect(usageRecordSlice).toContain('APIKey');
      expect(usageRecordSlice).toContain('SessionID');
      expect(usageRecordSlice).toContain('ParentSessionID');
      expect(usageRecordSlice).toContain('AuthID');
      expect(usageRecordSlice).toContain('AuthIndex');
      expect(usageRecordSlice).toContain('Failure UsageFailure');
      expect(usageRecordSlice).toContain('Detail UsageDetail');
      expect(usageRecordSlice).not.toContain('RequestID');
      expect(usageRecordSlice).not.toContain('TraceID');
      expect(usageRecordSlice).not.toContain('AttemptID');
      expect(usageRecordSlice).not.toContain('IdempotencyKey');

      // Asynchronous in-memory queue dispatch
      expect(managerSource).toContain('queue  []queueItem');
      expect(managerSource).toContain('m.queue = append(m.queue, queueItem{ctx: ctx, record: record})');

      // Fuse on panic and context.WithoutCancel
      expect(adapterSource).toContain('ctx = context.WithoutCancel(ctx)');
      expect(adapterSource).toContain('a.host.fusePlugin(a.pluginID, "UsagePlugin.HandleUsage", recovered)');

      // Attempt-level emission
      expect(helpersSource).toContain('func (r *UsageReporter) publishAttemptRecord(');
      expect(helpersSource).toContain('emits the record for one upstream attempt');
    } catch (error) {
      if (error.code === 'ENOENT') {
        return; // Optional local source verification
      }
      throw error;
    }
  });
});
