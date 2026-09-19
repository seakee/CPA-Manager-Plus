import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';
import {
  loadAndValidateContract,
  validateEvidenceExtension,
} from '../bin/ci/validate-phase2-evidence.mjs';

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const extensionPath = path.join(
  repoRoot,
  'tests/fixtures/phase2-evidence/extensions/phase2-02a-caller-identity.json'
);
const loaded = () => loadAndValidateContract();

/**
 * Replicates Go CPA coresession.CallerScope(value):
 * sha256("cli-proxy-api:caller-scope:v1\x00" + strings.TrimSpace(value)) -> hex
 */
function computeCallerScope(value) {
  if (value === null || value === undefined) return '';
  const trimmed = String(value).trim();
  if (trimmed === '') return '';
  const hasher = createHash('sha256');
  hasher.update('cli-proxy-api:caller-scope:v1\0' + trimmed);
  return hasher.digest('hex');
}

describe('Phase2-02A caller identity and scope evidence', () => {
  it('validates phase2-02a extension fixture against the frozen baseline contract', () => {
    const context = loaded();
    const raw = JSON.parse(readFileSync(extensionPath, 'utf8'));
    const summary = validateEvidenceExtension(raw, context);

    expect(summary.anchors).toBe(12);
    expect(summary.records).toBe(11);
  });

  it('enforces phase2-02a-* namespace for all added anchors and records', () => {
    const raw = JSON.parse(readFileSync(extensionPath, 'utf8'));

    for (const anchor of raw.anchors) {
      expect(anchor.id).toMatch(/^phase2-02a-[a-z0-9-]+$/);
    }
    for (const record of raw.records) {
      expect(record.id).toMatch(/^phase2-02a-[a-z0-9-]+$/);
    }
  });

  it('proves caller_scope derivation is deterministic, irreversible, and whitespace-normalized', () => {
    const key = 'sk-prod-user-key-987654321';
    const scope1 = computeCallerScope(key);
    const scope2 = computeCallerScope(key);

    expect(scope1).toBe(scope2);
    expect(scope1).toHaveLength(64);
    expect(scope1).toMatch(/^[0-9a-f]{64}$/);

    // Whitespace trimming behavior matches Go strings.TrimSpace
    expect(computeCallerScope(`  ${key}  \n`)).toBe(scope1);
    expect(computeCallerScope('\t' + key)).toBe(scope1);

    // Empty or whitespace-only principal produces empty scope
    expect(computeCallerScope('')).toBe('');
    expect(computeCallerScope('   ')).toBe('');
    expect(computeCallerScope(null)).toBe('');

    // Pre-image / secret redaction: hash does not reveal key contents
    expect(scope1).not.toContain(key);
    expect(scope1).not.toContain('prod-user');
  });

  it('proves caller_scope partitions distinct callers and isolates their identity', () => {
    const callerA = 'sk-alpha-tenant-111';
    const callerB = 'sk-beta-tenant-222';

    const scopeA = computeCallerScope(callerA);
    const scopeB = computeCallerScope(callerB);

    expect(scopeA).not.toBe(scopeB);
    expect(scopeA).toHaveLength(64);
    expect(scopeB).toHaveLength(64);

    // Case sensitivity: CPA strings.TrimSpace preserves case
    const scopeLower = computeCallerScope('sk-test');
    const scopeUpper = computeCallerScope('SK-TEST');
    expect(scopeLower).not.toBe(scopeUpper);
  });

  it('classifies lifecycle identity availability and secret redaction boundaries', () => {
    const rawSecret = 'sk-live-confidential-credential';
    const expectedScope = computeCallerScope(rawSecret);

    // Simulating CPA request lifecycle progression
    // 1. request_received (pre-auth)
    const stage0 = {
      userApiKey: null,
      callerScope: null,
      inboundHeaders: { authorization: `Bearer ${rawSecret}` },
    };
    expect(stage0.callerScope).toBeNull();

    // 2. client_api_key_resolved (accessAuthMiddleware)
    const stage1 = {
      userApiKey: rawSecret, // Built-in config_access sets candidate.value as principal
      accessProvider: 'config-inline',
    };
    expect(stage1.userApiKey).toBe(rawSecret);

    // 3. model_resolved (handlers_execution.go requestExecutionMetadata)
    const stage2 = {
      metadata: {
        caller_scope: computeCallerScope(stage1.userApiKey),
      },
    };
    expect(stage2.metadata.caller_scope).toBe(expectedScope);
    expect(stage2.metadata.userApiKey).toBeUndefined(); // Raw key NOT placed in metadata map

    // 4. credential_selected / scheduler options
    const schedulerReq = {
      options: {
        headers: { authorization: `Bearer ${rawSecret}` }, // Raw secret leaks in options.Headers
        metadata: { caller_scope: expectedScope }, // Only hashed scope in metadata
      },
    };
    expect(schedulerReq.options.metadata.caller_scope).toBe(expectedScope);
    expect(schedulerReq.options.headers.authorization).toContain(rawSecret);

    // 5. response_cancel_reject_failure / terminal RequestCompletion
    const completion = {
      requestID: 'req-uuid-1',
      outcome: 'succeeded',
      statusCode: 200,
      metadata: { caller_scope: expectedScope },
      // RequestCompletion struct has NO headers field
    };
    expect(completion.metadata.caller_scope).toBe(expectedScope);
    expect(completion.headers).toBeUndefined();
    expect(JSON.stringify(completion)).not.toContain(rawSecret);

    // 6. usage_accounting_settlement / UsageRecord
    const usageRecord = {
      model: 'gpt-4o',
      apiKey: stage1.userApiKey, // APIKeyFromContext retrieves ginCtx userApiKey -> raw secret!
    };
    expect(usageRecord.apiKey).toBe(rawSecret);
  });

  it('verifies v7.3.3 and v7.3.8 identity semantics parity', () => {
    const raw = JSON.parse(readFileSync(extensionPath, 'utf8'));

    const currentRecords = raw.records.filter((r) => r.artifactId === 'current-bundled-v7-3-3');
    const candidateRecords = raw.records.filter((r) => r.artifactId === 'candidate-release-v7-3-8');

    expect(currentRecords).toHaveLength(4);
    expect(candidateRecords).toHaveLength(4);

    const currentCaps = new Map(currentRecords.map((r) => [r.capability, r.status]));
    const candidateCaps = new Map(candidateRecords.map((r) => [r.capability, r.status]));

    for (const [cap, status] of currentCaps.entries()) {
      expect(candidateCaps.get(cap)).toBe(status);
    }

    // Both retain Plugin ABI schema 6
    for (const record of [...currentRecords, ...candidateRecords]) {
      expect(record.pluginContract.schemaVersion).toBe(6);
    }
  });

  it('keeps External unnegotiated capability records unknown and unpromoted', () => {
    const raw = JSON.parse(readFileSync(extensionPath, 'utf8'));
    const externalRecords = raw.records.filter((r) => r.artifactId === 'external-unnegotiated');

    expect(externalRecords.length).toBeGreaterThanOrEqual(3);
    for (const record of externalRecords) {
      expect(record.status).toBe('unknown');
      expect(record.pluginContract).toBeNull();
      expect(record.deploymentMode).toBe('external');
      expect(record.limitations.length).toBeGreaterThan(0);
    }
  });

  it('runs the offline phase2 evidence CLI validator with the phase2-02a extension', () => {
    const result = execFileSync(
      process.execPath,
      [
        path.join(repoRoot, 'bin/ci/validate-phase2-evidence.mjs'),
        '--evidence',
        extensionPath,
        '--json',
      ],
      { encoding: 'utf8' }
    );

    const parsed = JSON.parse(result);
    expect(parsed.contractId).toBe('cpamp-v2-phase2-evidence-v1');
    expect(parsed.evidenceExtension).toEqual({
      anchors: 12,
      records: 11,
    });
  });
});
