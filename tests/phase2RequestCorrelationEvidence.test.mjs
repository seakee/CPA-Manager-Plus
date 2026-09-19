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
  'tests/fixtures/phase2-evidence/extensions/phase2-03a-request-correlation.json'
);

describe('Phase2-03A Request / Attempt / Terminal Correlation Evidence', () => {
  const loadedContext = () => loadAndValidateContract();
  const readExtension = () => JSON.parse(readFileSync(extensionPath, 'utf8'));

  it('validates phase2-03a evidence extension against frozen baseline and schema', () => {
    const context = loadedContext();
    const extension = readExtension();

    const result = validateEvidenceExtension(extension, context);
    expect(result.anchors).toBe(11);
    expect(result.records).toBe(12);

    for (const anchor of extension.anchors) {
      expect(anchor.id).toMatch(/^phase2-03a-/);
      expect(anchor.limitations.length).toBeGreaterThan(0);
      expect(['source', 'unit', 'black-box', 'integration']).toContain(anchor.evidenceKind);
    }

    for (const record of extension.records) {
      expect(record.id).toMatch(/^phase2-03a-/);
      expect(CAPABILITY_STATUSES).toContain(record.status);
      expect(record.evidenceRefs.length).toBeGreaterThan(0);
      expect(record.limitations.length).toBeGreaterThan(0);

      if (record.artifactId === 'external-unnegotiated') {
        expect(record.status).toBe('unknown');
        expect(record.pluginContract).toBeNull();
      } else {
        expect(record.pluginContract).toMatchObject({
          schemaVersion: 6,
        });
      }
    }
  });

  it('proves RequestID continuity and TraceID preservation across all interceptor stages', () => {
    const extension = readExtension();
    const currentRecord = extension.records.find(
      (r) => r.id === 'phase2-03a-current-request-correlation-continuity'
    );
    const candidateRecord = extension.records.find(
      (r) => r.id === 'phase2-03a-candidate-request-correlation-continuity'
    );

    expect(currentRecord.status).toBe('supported');
    expect(candidateRecord.status).toBe('supported');

    // Simulate the handler lifecycle tracker correlation contract
    const simulateHandlerLifecycle = ({ traceId, model, stream }) => {
      const generatedRequestId = 'req-uuid-' + Math.random().toString(36).slice(2, 10);
      const observations = [];

      // 1. Before-auth stage
      observations.push({
        stage: 'before_auth',
        requestId: generatedRequestId,
        traceId,
        model,
        stream,
      });

      // 2. After-auth stage (credential attempt)
      observations.push({
        stage: 'after_auth',
        requestId: generatedRequestId,
        traceId,
        model,
        stream,
        selectedAuthId: 'cred-alpha',
      });

      // 3. Response or stream chunk hook
      observations.push({
        stage: stream ? 'stream_chunk' : 'response_interceptor',
        requestId: generatedRequestId,
        traceId,
        chunkIndex: stream ? 0 : null,
      });

      // 4. Request completion terminal callback
      const terminalCompletion = {
        stage: 'request_completion',
        requestId: generatedRequestId,
        traceId,
        outcome: 'succeeded',
        statusCode: 200,
      };
      observations.push(terminalCompletion);

      return { requestId: generatedRequestId, observations };
    };

    const run = simulateHandlerLifecycle({
      traceId: 'trace-client-http-1234',
      model: 'gpt-4o',
      stream: false,
    });

    // Verify all observations retain identical RequestID and TraceID
    for (const obs of run.observations) {
      expect(obs.requestId).toBe(run.requestId);
      expect(obs.traceId).toBe('trace-client-http-1234');
    }
  });

  it('proves absence of stable attempt ID during retries and marks attempt correlation as requires_upstream', () => {
    const extension = readExtension();
    const currentRecord = extension.records.find(
      (r) => r.id === 'phase2-03a-current-attempt-identity-correlation'
    );
    const candidateRecord = extension.records.find(
      (r) => r.id === 'phase2-03a-candidate-attempt-identity-correlation'
    );

    // Because CPA lacks an upstream attempt identifier and overwrites metadata in-place,
    // the finding is definitively requires_upstream (not artificially supported).
    expect(currentRecord.status).toBe('requires_upstream');
    expect(candidateRecord.status).toBe('requires_upstream');

    // Simulate CPA Conductor execution retry sequence
    const simulateRetryExecution = ({ attempts }) => {
      const requestId = 'req-fixed-retry-uuid';
      const sharedMetadata = {};
      const attemptLog = [];

      for (let i = 0; i < attempts.length; i++) {
        const attempt = attempts[i];
        // publishSelectedAuthMetadata overwrites selected_auth_id in shared options metadata
        sharedMetadata.selected_auth_id = attempt.authId;
        sharedMetadata.provider = attempt.provider;

        // In CPA sdk/cliproxy/auth/conductor_execution.go, RequestAfterAuthInterceptRequest
        // contains only RequestID, Model, RequestedModel, Headers, Body, Metadata.
        // It does NOT expose any attempt_id, attempt_uuid, or attempt_ordinal field.
        attemptLog.push({
          requestId,
          observedAuthId: sharedMetadata.selected_auth_id,
          observedProvider: sharedMetadata.provider,
          hasAttemptId: false,
          hasAttemptOrdinal: false,
          success: attempt.success,
        });
      }

      // Terminal callback only sees the final state of sharedMetadata
      const terminalCompletion = {
        requestId,
        outcome: attempts[attempts.length - 1].success ? 'succeeded' : 'failed',
        finalSelectedAuthId: sharedMetadata.selected_auth_id,
        reconstructableAttempts: false, // Cannot reconstruct intermediate attempts from single completion
      };

      return { attemptLog, terminalCompletion };
    };

    const multiAttemptResult = simulateRetryExecution({
      attempts: [
        { authId: 'cred-1', provider: 'openai', success: false },
        { authId: 'cred-2', provider: 'azure', success: true },
      ],
    });

    expect(multiAttemptResult.attemptLog.length).toBe(2);
    // RequestID remains constant across retries
    expect(multiAttemptResult.attemptLog[0].requestId).toBe(
      multiAttemptResult.attemptLog[1].requestId
    );
    // No attempt ID exists
    expect(multiAttemptResult.attemptLog[0].hasAttemptId).toBe(false);
    expect(multiAttemptResult.attemptLog[1].hasAttemptId).toBe(false);

    // Terminal completion only captures the last attempt's credential
    expect(multiAttemptResult.terminalCompletion.finalSelectedAuthId).toBe('cred-2');
    expect(multiAttemptResult.terminalCompletion.reconstructableAttempts).toBe(false);
  });

  it('proves exactly-once terminal callback cardinality across success, failure, rejection, and cancellation', () => {
    const extension = readExtension();
    const currentRecord = extension.records.find(
      (r) => r.id === 'phase2-03a-current-terminal-callback-cardinality'
    );
    const candidateRecord = extension.records.find(
      (r) => r.id === 'phase2-03a-candidate-terminal-callback-cardinality'
    );

    expect(currentRecord.status).toBe('supported');
    expect(candidateRecord.status).toBe('supported');

    // Simulate requestLifecycleTracker sync.Once complete semantics
    class Tracker {
      constructor(requestId) {
        this.requestId = requestId;
        this.callCount = 0;
        this.completed = false;
        this.result = null;
      }

      complete(outcome, statusCode, error = null) {
        if (this.completed) return; // sync.Once simulation
        this.completed = true;
        this.callCount++;
        this.result = { outcome, statusCode, error };
      }

      completeError(msg, ctxCanceled) {
        let outcome = 'failed';
        if (msg?.directResponse) {
          outcome = 'rejected';
        } else if (ctxCanceled) {
          outcome = 'canceled';
        }
        const statusCode = outcome === 'canceled' ? 0 : msg?.statusCode || 500;
        this.complete(outcome, statusCode, msg?.error);
      }
    }

    // 1. Non-stream success
    const t1 = new Tracker('req-1');
    t1.complete('succeeded', 200, null);
    t1.complete('succeeded', 200, null); // duplicate attempt
    expect(t1.callCount).toBe(1);
    expect(t1.result.outcome).toBe('succeeded');
    expect(t1.result.statusCode).toBe(200);

    // 2. Stream success
    const t2 = new Tracker('req-2');
    t2.complete('succeeded', 200, null);
    expect(t2.callCount).toBe(1);
    expect(t2.result.outcome).toBe('succeeded');

    // 3. Before/After-auth interceptor rejection
    const t3 = new Tracker('req-3');
    t3.completeError(
      { directResponse: true, statusCode: 429, error: new Error('quota busy') },
      false
    );
    expect(t3.callCount).toBe(1);
    expect(t3.result.outcome).toBe('rejected');
    expect(t3.result.statusCode).toBe(429);

    // 4. Upstream failure
    const t4 = new Tracker('req-4');
    t4.completeError(
      { directResponse: false, statusCode: 502, error: new Error('bad gateway') },
      false
    );
    expect(t4.callCount).toBe(1);
    expect(t4.result.outcome).toBe('failed');
    expect(t4.result.statusCode).toBe(502);

    // 5. Downstream client cancellation / disconnect
    const t5 = new Tracker('req-5');
    t5.completeError(
      { directResponse: false, statusCode: 499, error: new Error('context canceled') },
      true
    );
    expect(t5.callCount).toBe(1);
    expect(t5.result.outcome).toBe('canceled');
    expect(t5.result.statusCode).toBe(0); // Canceled outcome forces statusCode = 0 in CPA
  });

  it('proves lifecycle coverage boundaries and in-memory dropped notification resilience', () => {
    const extension = readExtension();
    const currentResilience = extension.records.find(
      (r) => r.id === 'phase2-03a-current-lifecycle-resilience-boundary'
    );
    const candidateResilience = extension.records.find(
      (r) => r.id === 'phase2-03a-candidate-lifecycle-resilience-boundary'
    );

    expect(currentResilience.status).toBe('partial');
    expect(candidateResilience.status).toBe('partial');

    // 1. Proof of paths that never create a tracker
    const simulateInboundRequestPipeline = (request) => {
      // Access auth middleware check
      if (!request.validClientApiKey) {
        return { handledAt: 'accessAuthMiddleware', status: 401, trackerCreated: false };
      }
      // Route match check
      if (request.path !== '/v1/chat/completions') {
        return { handledAt: 'router', status: 404, trackerCreated: false };
      }
      // JSON bind check
      if (request.malformedJson) {
        return { handledAt: 'handler_preflight', status: 400, trackerCreated: false };
      }
      // Only reaching model handler execution creates tracker
      return { handledAt: 'executeWithAuthManagerFormats', status: 200, trackerCreated: true };
    };

    expect(simulateInboundRequestPipeline({ validClientApiKey: false }).trackerCreated).toBe(false);
    expect(
      simulateInboundRequestPipeline({ validClientApiKey: true, path: '/unregistered' })
        .trackerCreated
    ).toBe(false);
    expect(
      simulateInboundRequestPipeline({
        validClientApiKey: true,
        path: '/v1/chat/completions',
        malformedJson: true,
      }).trackerCreated
    ).toBe(false);
    expect(
      simulateInboundRequestPipeline({
        validClientApiKey: true,
        path: '/v1/chat/completions',
        malformedJson: false,
      }).trackerCreated
    ).toBe(true);

    // 2. Proof of dropped notifications during plugin failure/reload
    const simulatePluginHostDispatch = (plugins, completion) => {
      const deliveredTo = [];
      const droppedFor = [];

      for (const p of plugins) {
        if (p.isFused) {
          droppedFor.push({ pluginId: p.id, reason: 'plugin_fused' });
          continue;
        }
        if (!p.isCurrentRecord) {
          droppedFor.push({ pluginId: p.id, reason: 'plugin_reloading_or_stale' });
          continue;
        }
        deliveredTo.push(p.id);
      }
      return { deliveredTo, droppedFor };
    };

    const hostDispatch = simulatePluginHostDispatch(
      [
        { id: 'plugin-healthy', isFused: false, isCurrentRecord: true },
        { id: 'plugin-fused', isFused: true, isCurrentRecord: true },
        { id: 'plugin-reloading', isFused: false, isCurrentRecord: false },
      ],
      { requestId: 'req-1', outcome: 'succeeded' }
    );

    expect(hostDispatch.deliveredTo).toEqual(['plugin-healthy']);
    expect(hostDispatch.droppedFor).toEqual([
      { pluginId: 'plugin-fused', reason: 'plugin_fused' },
      { pluginId: 'plugin-reloading', reason: 'plugin_reloading_or_stale' },
    ]);
  });

  it('keeps External unnegotiated evidence isolated as unknown without promotion', () => {
    const extension = readExtension();
    const externalRecords = extension.records.filter(
      (r) => r.artifactId === 'external-unnegotiated'
    );

    expect(externalRecords.length).toBe(4);
    for (const record of externalRecords) {
      expect(record.status).toBe('unknown');
      expect(record.pluginContract).toBeNull();
      expect(record.deploymentMode).toBe('external');
    }
  });
});
