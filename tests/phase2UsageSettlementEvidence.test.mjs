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
const architectureDocPath = path.join(
  repoRoot,
  'docs/architecture/phase2-evidence/03b-usage-settlement.md'
);

const loadExtension = () => JSON.parse(readFileSync(extensionPath, 'utf8'));

describe('Phase2-03B Usage / Reservation / Settlement Evidence Contract', () => {
  it('validates extension schema and integrates with frozen contract baseline', () => {
    const loaded = loadAndValidateContract();
    const extension = loadExtension();
    const summary = validateEvidenceExtension(extension, loaded);

    expect(summary.anchors).toBe(15);
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

    expect(currentAnchors.length).toBe(7);
    expect(candidateAnchors.length).toBe(7);

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
    expect(currentAttemptAnchor.evidenceReference).toContain('resolveUsageSource');
    expect(currentAttemptAnchor.evidenceReference).toContain('NewUsageReporter');
    expect(currentAttemptAnchor.limitations[0]).toContain('publishAttemptRecord does not exist in v7.3.3');

    for (const anchor of candidateAnchors) {
      expect(anchor.evidenceReference).toContain('@v7.3.8:');
      expect(anchor.evidenceReference).not.toContain('@v7.3.3:');
    }

    const candidateAttemptAnchor = candidateAnchors.find((a) => a.id === 'phase2-03b-candidate-usage-attempt-dispatch');
    expect(candidateAttemptAnchor.evidenceReference).toContain('publishAttemptRecord');
    expect(candidateAttemptAnchor.evidenceReference).toContain('resolveUsageSource');
    expect(candidateAttemptAnchor.evidenceReference).toContain('NewUsageReporter');

    for (const anchor of [currentAttemptAnchor, candidateAttemptAnchor]) {
      expect(anchor.evidenceReference).toContain('PublishAdditionalModel');
      expect(anchor.evidenceReference).toContain('buildAdditionalModelRecord');
      expect(anchor.evidenceReference).toContain('codex_executor_request.go');
      expect(anchor.evidenceReference).toContain('home_result.go');
      expect(anchor.evidenceReference).toContain('reportHomeUnauthorized');
    }
  });

  it('enforces anti-regression guards against fake unit evidence and requires source chains for retry', () => {
    const extension = loadExtension();
    const anchorIds = new Set(extension.anchors.map((a) => a.id));

    // Fake unit anchors must be purged
    expect(anchorIds.has('phase2-03b-current-retry-multiple-attempts-unit')).toBe(false);
    expect(anchorIds.has('phase2-03b-candidate-retry-multiple-attempts-unit')).toBe(false);

    // conductor_usage_test.go only tests context metadata, never retry/fallback
    for (const anchor of extension.anchors) {
      expect(anchor.evidenceReference).not.toContain('conductor_usage_test.go');
    }

    const records = new Map(extension.records.map((r) => [r.id, r]));
    const currentRetry = records.get('phase2-03b-current-retry-multiple-attempts');
    const candidateRetry = records.get('phase2-03b-candidate-retry-multiple-attempts');

    expect(currentRetry.evidenceKind).toEqual(['source']);
    expect(candidateRetry.evidenceKind).toEqual(['source']);
    expect(currentRetry.evidenceRefs).toContain('phase2-03b-current-usage-attempt-dispatch');
    expect(candidateRetry.evidenceRefs).toContain('phase2-03b-candidate-usage-attempt-dispatch');
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

  it('classifies upstream failure and cancellation reporting behavior with scoped context semantics', () => {
    const extension = loadExtension();
    const records = new Map(extension.records.map((r) => [r.id, r]));
    const anchors = new Map(extension.anchors.map((a) => [a.id, a]));

    const currentFailure = records.get('phase2-03b-current-upstream-failure-reporting');
    const candidateFailure = records.get('phase2-03b-candidate-upstream-failure-reporting');
    expect(currentFailure.status).toBe('supported');
    expect(candidateFailure.status).toBe('supported');
    expect(currentFailure.limitations[0]).toContain('Failed=true');
    expect(currentFailure.limitations[0]).toContain('stream');
    expect(currentFailure.limitations[0]).toContain('partial');
    expect(candidateFailure.limitations[0]).toContain('Failed=true');
    expect(candidateFailure.limitations[0]).toContain('stream');
    expect(candidateFailure.limitations[0]).toContain('partial');

    const currentCancel = records.get('phase2-03b-current-cancellation-reporting');
    const candidateCancel = records.get('phase2-03b-candidate-cancellation-reporting');
    expect(currentCancel.status).toBe('supported');
    expect(candidateCancel.status).toBe('supported');
    expect(currentCancel.limitations[0]).toContain('context.WithoutCancel');
    expect(currentCancel.limitations[0]).toContain('does not provide durable delivery');
    expect(candidateCancel.limitations[0]).toContain('does not provide durable delivery');

    // Provenance check: cancellation records must link to WithoutCancel source anchor
    for (const cancelRecord of [currentCancel, candidateCancel]) {
      const referencedAnchors = cancelRecord.evidenceRefs.map((id) => anchors.get(id)).filter(Boolean);
      const hasWithoutCancelAnchor = referencedAnchors.some(
        (a) =>
          a.evidenceReference.includes('adapters_usage_translation.go') &&
          a.evidenceReference.includes('usageAdapter.HandleUsage') &&
          a.limitations.some((l) => l.includes('context.WithoutCancel'))
      );
      expect(hasWithoutCancelAnchor).toBe(true);
    }
  });

  it('classifies retry and fallback multi-attempt dispatch from pinned source evidence', () => {
    const extension = loadExtension();
    const records = new Map(extension.records.map((r) => [r.id, r]));

    const currentRetry = records.get('phase2-03b-current-retry-multiple-attempts');
    const candidateRetry = records.get('phase2-03b-candidate-retry-multiple-attempts');
    expect(currentRetry.status).toBe('partial');
    expect(candidateRetry.status).toBe('partial');
    expect(currentRetry.limitations[0]).toContain('a logical request can span multiple executor attempts');
    expect(currentRetry.limitations[0]).toContain('independent usage callback');
    expect(currentRetry.limitations[0]).toContain('Primary attempt callbacks are attempt-scoped');
    expect(currentRetry.limitations[0]).toContain('Codex image-tool');
    expect(currentRetry.limitations[0]).toContain('increase callback count beyond the number of attempts');
    expect(candidateRetry.limitations[0]).toContain('a logical request can span multiple executor attempts');
    expect(candidateRetry.limitations[0]).toContain('independent usage callback');
    expect(candidateRetry.limitations[0]).toContain('Primary attempt callbacks are attempt-scoped');
    expect(candidateRetry.limitations[0]).toContain('Codex image-tool');
    expect(candidateRetry.limitations[0]).toContain('increase callback count beyond the number of attempts');

    const currentExactCorrelation = records.get('phase2-03b-current-exact-request-correlation');
    const candidateExactCorrelation = records.get('phase2-03b-candidate-exact-request-correlation');
    expect(currentExactCorrelation.status).toBe('requires_upstream');
    expect(candidateExactCorrelation.status).toBe('requires_upstream');
  });

  it('classifies zero-token and unknown usage handling with reporter fallback scope', () => {
    const extension = loadExtension();
    const records = new Map(extension.records.map((r) => [r.id, r]));

    const currentZero = records.get('phase2-03b-current-zero-unknown-usage');
    const candidateZero = records.get('phase2-03b-candidate-zero-unknown-usage');
    expect(currentZero.status).toBe('supported');
    expect(candidateZero.status).toBe('supported');
    expect(currentZero.limitations[0]).toContain('UsageReporter');
    expect(currentZero.limitations[0]).toContain('logical');
    expect(currentZero.limitations[0]).not.toMatch(/one record per logical request|guarantees request counting/i);
    expect(candidateZero.limitations[0]).toContain('UsageReporter');
    expect(candidateZero.limitations[0]).toContain('logical');
    expect(candidateZero.limitations[0]).not.toMatch(/one record per logical request|guarantees request counting/i);
  });

  it('classifies plugin executor usage dispatch path as supported', () => {
    const extension = loadExtension();
    const records = new Map(extension.records.map((r) => [r.id, r]));
    const anchors = new Map(extension.anchors.map((a) => [a.id, a]));

    const currentPluginExec = records.get('phase2-03b-current-plugin-executor-usage');
    const candidatePluginExec = records.get('phase2-03b-candidate-plugin-executor-usage');
    expect(currentPluginExec.status).toBe('supported');
    expect(candidatePluginExec.status).toBe('supported');

    const currentPluginAnchor = anchors.get('phase2-03b-current-plugin-executor-usage-dispatch');
    const candidatePluginAnchor = anchors.get('phase2-03b-candidate-plugin-executor-usage-dispatch');
    for (const anchor of [currentPluginAnchor, candidatePluginAnchor]) {
      expect(anchor.evidenceReference).toContain('executeWithPluginExecutor');
      expect(anchor.evidenceReference).toContain('streamWithPluginExecutor');
      expect(anchor.evidenceReference).toContain('parsePluginExecutorResponseUsage');
    }

    for (const record of [currentPluginExec, candidatePluginExec]) {
      const limitation = record.limitations.join(' ');
      expect(limitation).toContain('InternalSource');
      expect(limitation).toContain('nestedTracker');
      expect(limitation).toContain('PublishFailureWithDetail');
      expect(limitation).toContain('EnsurePublished');
      expect(limitation).toContain('Pre-stream');
      expect(limitation).not.toContain('Top-level non-InternalSource');
    }
  });

  it('classifies UsagePlugin failure, panic fuse, and asynchronous queue resilience', () => {
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

  it('classifies pre-request reservation and authoritative settlement primitives as unsupported', () => {
    const extension = loadExtension();
    const records = new Map(extension.records.map((r) => [r.id, r]));
    const anchors = new Map(extension.anchors.map((a) => [a.id, a]));

    const currentReservation = records.get('phase2-03b-current-pre-request-reservation');
    const candidateReservation = records.get('phase2-03b-candidate-pre-request-reservation');
    expect(currentReservation.status).toBe('unsupported');
    expect(candidateReservation.status).toBe('unsupported');
    expect(currentReservation.limitations[0]).toContain('first-class quota reservation');
    expect(currentReservation.limitations[0]).toContain('RequestInterceptResponse.Terminate');
    expect(candidateReservation.limitations[0]).toContain('first-class quota reservation');
    expect(candidateReservation.limitations[0]).toContain('RequestInterceptResponse.Terminate');

    expect(currentReservation.limitations[0]).not.toContain('RequestInterceptor.Terminate');
    expect(candidateReservation.limitations[0]).not.toContain('RequestInterceptor.Terminate');

    const currentReservationAnchor = anchors.get('phase2-03b-current-reservation-settlement-absent');
    const candidateReservationAnchor = anchors.get('phase2-03b-candidate-reservation-settlement-absent');
    expect(currentReservationAnchor.evidenceReference).toContain('RequestInterceptResponse');
    expect(candidateReservationAnchor.evidenceReference).toContain('RequestInterceptResponse');
    expect(candidateReservationAnchor.evidenceReference).toContain('QuotaProvider');
    expect(candidateReservationAnchor.evidenceReference).toContain('QuotaMetric');

    const currentSettlement = records.get('phase2-03b-current-authoritative-settlement');
    const candidateSettlement = records.get('phase2-03b-candidate-authoritative-settlement');
    expect(currentSettlement.status).toBe('unsupported');
    expect(candidateSettlement.status).toBe('unsupported');
    expect(currentSettlement.limitations[0]).toContain('no two-phase commit, rollback, or idempotent settlement primitives');
  });

  it('bounds unnegotiated External runtime evidence to conservative unknown without pluginContract', () => {
    const extension = loadExtension();
    const externalRecords = extension.records.filter((r) => r.artifactId === 'external-unnegotiated');

    expect(externalRecords.length).toBe(3);

    const externalAnchor = extension.anchors.find(
      (a) => a.id === 'phase2-03b-external-usage-settlement-boundary'
    );
    expect(externalAnchor).toBeDefined();
    expect(externalAnchor.evidenceKind).toBe('source');
    expect(externalAnchor.limitations[0]).toContain(
      'External runtime version/plugin/capability state cannot be inferred until runtime negotiation/observation occurs'
    );

    for (const record of externalRecords) {
      expect(record.status).toBe('unknown');
      expect(record.pluginContract).toBeNull();
      expect(record.deploymentMode).toBe('external');
      expect(record.evidenceKind).not.toContain('black-box');
      expect(record.evidenceKind).toContain('source');
      expect(CAPABILITY_STATUSES).toContain(record.status);
    }

    for (const anchor of extension.anchors) {
      if (anchor.evidenceKind === 'black-box') {
        expect(anchor.evidenceReference).not.toMatch(/\.go\b/);
      }
    }
  });

  it('enforces architecture document relative links to repository root tests directory', () => {
    const docContent = readFileSync(architectureDocPath, 'utf8');

    // Must use ../../../tests/... from docs/architecture/phase2-evidence/
    expect(docContent).toContain('../../../tests/fixtures/phase2-evidence/extensions/phase2-03b-usage-settlement.json');
    expect(docContent).toContain('../../../tests/phase2UsageSettlementEvidence.test.mjs');

    // Must forbid erroneous ../../tests/ pattern
    expect(docContent).not.toMatch(/\]\(\.\.\/\.\.\/tests\//);

    // QuotaProvider management-plane boundary and WithoutCancel anti-regression
    expect(docContent).toContain('QuotaProvider');
    expect(docContent).toContain('FetchQuota');
    expect(docContent).toContain('ResetQuota');
    expect(docContent).not.toContain('静态只读查询');
    expect(docContent).not.toContain('脱敏上下文');
    expect(docContent).not.toContain('脱敏上报');

    // UsageRecord Source and ServiceTier semantics anti-regression
    expect(docContent).toContain('resolveUsageSource');
    expect(docContent).toContain('auth.AuthSourceKind()');
    expect(docContent).toContain('ResponseServiceTier');
    expect(docContent).not.toContain('凭证来源类型（`auth.AuthSourceKind()`）');
    expect(docContent).not.toContain('客户端请求或响应的服务层级');

    // Failure path and zero-value detail anti-regression
    expect(docContent).toContain('PublishFailureWithDetail');
    expect(docContent).toContain('zero-value');
    expect(docContent).toContain('Pre-stream failure');
    expect(docContent).not.toMatch(/buffer.*(?:no|没有).*detail.*PublishFailure/i);

    // Primary and additional-model cardinality anti-regression
    expect(docContent).toContain('PublishAdditionalModel');
    expect(docContent).toContain('primary');
    expect(docContent).toContain('additional-model');
    expect(docContent).toContain('Codex');
    expect(docContent).toContain('image-tool');
    expect(docContent).toContain('callback cardinality can exceed');

    expect(docContent).not.toContain('仅限制该 reporter 实例的发布次数');
    expect(docContent).not.toContain('最多可观测到 3 个独立的 `HandleUsage` 回调');
    expect(docContent).not.toContain('总 callbacks 不存在固定上限');
    expect(docContent).not.toContain('zero or more additional-model records');
    expect(docContent).not.toMatch(/Publish \/ PublishFailure.*EnsurePublished/);

    // InternalSource and nested tracker boundary anti-regression
    expect(docContent).toContain('InternalSource');
    expect(docContent).toContain('nestedTracker.hasNestedExecution');
    expect(docContent).not.toContain('只有顶层非内部来源请求');
  });

  it('strictly ensures no hardcoded machine or developer paths exist in evidence files', () => {
    const extensionFileContent = readFileSync(extensionPath, 'utf8');
    const docContent = readFileSync(architectureDocPath, 'utf8');
    const localUserPrefix = String.fromCharCode(47) + 'Users' + String.fromCharCode(47);
    const localHomePrefix = String.fromCharCode(47) + 'home' + String.fromCharCode(47);
    const localFileProtocol = 'file:' + String.fromCharCode(47) + String.fromCharCode(47);

    expect(extensionFileContent).not.toContain(localUserPrefix);
    expect(extensionFileContent).not.toContain(localHomePrefix);
    expect(extensionFileContent).not.toContain(localFileProtocol);

    expect(docContent).not.toContain(localUserPrefix);
    expect(docContent).not.toContain(localHomePrefix);
    expect(docContent).not.toContain(localFileProtocol);
  });
});
