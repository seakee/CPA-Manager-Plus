import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { AccountCredentialEvidenceBoundary } from './accountCredentialEvidence';
import {
  clearAccountCredentialEvidenceBoundaryStateCache,
  loadAccountCredentialEvidenceBoundaryState,
  saveAccountCredentialEvidenceBoundaryState,
} from './accountCredentialEvidenceStorage';
import { createCredentialInspectionSnapshotScopeKey } from '../hooks/useCredentialInspectionSnapshot';

const boundary = (
  localAtMs: number,
  overrides: Partial<AccountCredentialEvidenceBoundary> = {}
): AccountCredentialEvidenceBoundary => ({
  localAtMs,
  inspectionAtMs: 0,
  inspectionBaselinePending: false,
  headerAtMs: 0,
  headerBaselinePending: false,
  actionAtMs: 0,
  actionBaselinePending: false,
  authenticationActionAtMs: 0,
  authenticationActionBaselinePending: false,
  quotaActionAtMs: 0,
  quotaActionBaselinePending: false,
  cooldownAtMs: 0,
  cooldownBaselinePending: false,
  fallbackInspectionAtMs: 0,
  fallbackInspectionBaselinePending: false,
  fallbackHeaderAtMs: 0,
  fallbackHeaderBaselinePending: false,
  fallbackActionAtMs: 0,
  fallbackActionBaselinePending: false,
  fallbackCooldownAtMs: 0,
  fallbackCooldownBaselinePending: false,
  authenticationAtMs: 0,
  rawStatusAtMs: 0,
  rawStatusMessages: [],
  rawStatusCodes: [],
  ...overrides,
});

const createStorage = (): Storage => {
  const values = new Map<string, string>();
  return {
    get length() {
      return values.size;
    },
    clear: () => values.clear(),
    getItem: (key) => values.get(key) ?? null,
    key: (index) => Array.from(values.keys())[index] ?? null,
    removeItem: (key) => values.delete(key),
    setItem: (key, value) => values.set(key, value),
  };
};

describe('account credential evidence boundary storage', () => {
  beforeEach(() => {
    clearAccountCredentialEvidenceBoundaryStateCache();
    vi.stubGlobal('window', { sessionStorage: createStorage() });
  });

  afterEach(() => {
    clearAccountCredentialEvidenceBoundaryStateCache();
    vi.unstubAllGlobals();
  });

  it('retains the most recently updated boundaries instead of the newest insertion positions', () => {
    const evidence = new Map<string, AccountCredentialEvidenceBoundary>();
    evidence.set('updated-old-key', boundary(10_000));
    for (let index = 1; index <= 256; index += 1) {
      evidence.set(`key-${index}`, boundary(index));
    }

    saveAccountCredentialEvidenceBoundaryState('scope-a', {
      evidence,
      status: new Map(),
    });

    const stored = loadAccountCredentialEvidenceBoundaryState('scope-a');
    expect(stored.evidence.size).toBe(256);
    expect(stored.evidence.has('updated-old-key')).toBe(true);
    expect(stored.evidence.has('key-1')).toBe(false);
  });

  it('persists authentication recovery boundaries and raw status codes', () => {
    const original = boundary(1_000, {
      authenticationAtMs: 12_345,
      rawStatusAtMs: 12_300,
      rawStatusMessages: ['token_expired'],
      rawStatusCodes: [401],
    });

    saveAccountCredentialEvidenceBoundaryState('scope-a', {
      evidence: new Map([['credential-a', original]]),
      status: new Map(),
    });

    expect(
      loadAccountCredentialEvidenceBoundaryState('scope-a').evidence.get('credential-a')
    ).toEqual(original);
  });

  it('retains a boundary whose only recent evidence is authentication recovery', () => {
    const evidence = new Map<string, AccountCredentialEvidenceBoundary>();
    evidence.set('reauthenticated-key', boundary(0, { authenticationAtMs: 10_000 }));
    for (let index = 1; index <= 256; index += 1) {
      evidence.set(`key-${index}`, boundary(index));
    }

    saveAccountCredentialEvidenceBoundaryState('scope-a', {
      evidence,
      status: new Map(),
    });

    const stored = loadAccountCredentialEvidenceBoundaryState('scope-a');
    expect(stored.evidence.has('reauthenticated-key')).toBe(true);
    expect(stored.evidence.has('key-1')).toBe(false);
  });

  it('ignores malformed persisted state', () => {
    window.sessionStorage.setItem(
      'cpa.accounts.credential-evidence-boundaries.v1',
      JSON.stringify({ version: 1, scopes: { broken: { evidence: 'invalid', status: null } } })
    );

    expect(loadAccountCredentialEvidenceBoundaryState('broken')).toEqual({
      evidence: new Map(),
      status: new Map(),
    });
  });

  it('keeps evidence boundaries isolated between Manager Server identities', () => {
    const firstScope = createCredentialInspectionSnapshotScopeKey(
      'cpa-scope',
      'https://manager-a.example.test',
      'key-a'
    );
    const secondScope = createCredentialInspectionSnapshotScopeKey(
      'cpa-scope',
      'https://manager-b.example.test',
      'key-a'
    );
    saveAccountCredentialEvidenceBoundaryState(firstScope, {
      evidence: new Map([['credential-a', boundary(1_000)]]),
      status: new Map(),
    });

    expect(loadAccountCredentialEvidenceBoundaryState(secondScope)).toEqual({
      evidence: new Map(),
      status: new Map(),
    });
  });

  it('persists only opaque CPA and Manager fingerprints in scope identifiers', () => {
    const managerBase = 'https://manager-secret.example.test';
    const managementKey = 'manager-super-secret';
    const scope = createCredentialInspectionSnapshotScopeKey(
      'v1:cpaopaque',
      managerBase,
      managementKey
    );

    saveAccountCredentialEvidenceBoundaryState(scope, {
      evidence: new Map([['credential-a', boundary(1_000)]]),
      status: new Map(),
    });

    const stored = window.sessionStorage.getItem('cpa.accounts.credential-evidence-boundaries.v1');
    expect(scope).not.toContain(managerBase);
    expect(scope).not.toContain(managementKey);
    expect(stored).not.toContain(managerBase);
    expect(stored).not.toContain(managementKey);
  });
});
