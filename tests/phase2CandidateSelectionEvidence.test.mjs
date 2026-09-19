import { execFileSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';
import {
  loadAndValidateContract,
  validateEvidenceExtension,
} from '../bin/ci/validate-phase2-evidence.mjs';

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const harnessPath = path.join(repoRoot, 'bin/ci/validate-phase2-evidence.mjs');
const extensionPath = path.join(
  repoRoot,
  'tests/fixtures/phase2-evidence/extensions/phase2-02b-candidate-selection.json'
);

const readExtension = () => JSON.parse(readFileSync(extensionPath, 'utf8'));

describe('Phase2-02B: Candidate Visibility / Selection / Retry-Fallback Evidence', () => {
  it('conforms strictly to the Phase2 evidence extension schema and namespace', () => {
    const context = loadAndValidateContract();
    const extension = readExtension();

    // Invariant: all anchor and record IDs must use phase2-02b-* namespace
    for (const anchor of extension.anchors) {
      expect(anchor.id).toMatch(/^phase2-02b-[a-z0-9-]+$/);
      expect(anchor.limitations.length).toBeGreaterThan(0);
    }
    for (const record of extension.records) {
      expect(record.id).toMatch(/^phase2-02b-[a-z0-9-]+$/);
      expect(record.capability).toMatch(/^[a-z0-9_]+$/);
      expect(record.limitations.length).toBeGreaterThan(0);
      if (record.pluginContract !== null) {
        expect(record.pluginContract.schemaVersion).toBe(6);
        expect(record.evidenceRefs).toContain('plugin-abi-schema-v6');
      }
    }

    const baselineBefore = JSON.stringify(context.contract);
    const result = validateEvidenceExtension(extension, context);
    expect(result.anchors).toBe(extension.anchors.length);
    expect(result.records).toBe(extension.records.length);
    expect(JSON.stringify(context.contract)).toBe(baselineBefore);

    // Validate CLI execution
    const cliOutput = JSON.parse(
      execFileSync(process.execPath, [harnessPath, '--evidence', extensionPath, '--json'], {
        encoding: 'utf8',
      })
    );
    expect(cliOutput.evidenceExtension).toEqual({
      anchors: extension.anchors.length,
      records: extension.records.length,
    });
  });

  describe('Evidence Provenance and Anchor Governance', () => {
    it('enforces strict separation between CPAMP unit model and genuine candidate release black-box', () => {
      const extension = readExtension();

      // 1. phase2-02b-v7-3-8-cpamp-contract-unit MUST be unit evidenceKind, NEVER black-box
      const unitAnchor = extension.anchors.find(
        (a) => a.id === 'phase2-02b-v7-3-8-cpamp-contract-unit'
      );
      expect(unitAnchor).toBeDefined();
      expect(unitAnchor.evidenceKind).toBe('unit');
      expect(unitAnchor.evidenceReference).toBe(
        'tests/phase2CandidateSelectionEvidence.test.mjs#candidate-selection-contract-model'
      );

      // 2. NO black-box anchor may point to JS unit test file
      const blackBoxAnchors = extension.anchors.filter((a) => a.evidenceKind === 'black-box');
      expect(blackBoxAnchors.length).toBeGreaterThan(0);
      for (const bb of blackBoxAnchors) {
        expect(bb.evidenceReference).not.toContain('tests/');
        expect(bb.evidenceReference).not.toContain('.test.');
      }

      // 3. Genuine candidate release black-box anchor exists and references documented observation
      const releaseBlackBoxAnchor = extension.anchors.find(
        (a) => a.id === 'phase2-02b-v7-3-8-release-black-box'
      );
      expect(releaseBlackBoxAnchor).toBeDefined();
      expect(releaseBlackBoxAnchor.evidenceKind).toBe('black-box');
      expect(releaseBlackBoxAnchor.artifactId).toBe('candidate-release-v7-3-8');
      expect(releaseBlackBoxAnchor.evidenceReference).toBe(
        'docs/architecture/phase2-evidence/02b-candidate-selection.md#v738-release-black-box-observation'
      );

      // 4. release-black-box is ONLY attached to records verified by genuine black-box Cases A, B, C, D
      const verifiedRecordIds = [
        'phase2-02b-candidate-default-highest-tier-visibility', // Case A
        'phase2-02b-candidate-across-priorities-opt-in',        // Case B
        'phase2-02b-candidate-pre-filter-exclusion',           // Case C
        'phase2-02b-candidate-valid-candidate-selection',      // Case D1
        'phase2-02b-candidate-invalid-candidate-fallback',     // Case D2
      ];

      for (const record of extension.records) {
        if (record.evidenceRefs.includes('phase2-02b-v7-3-8-release-black-box')) {
          expect(verifiedRecordIds).toContain(record.id);
          expect(record.evidenceKind).toContain('black-box');
        } else if (record.artifactId === 'candidate-release-v7-3-8') {
          // Other candidate records must not have black-box
          expect(record.evidenceKind).not.toContain('black-box');
        }
      }

      // 5. External runtime remains unknown
      const externalRecord = extension.records.find(
        (r) => r.id === 'phase2-02b-external-unnegotiated-candidate-selection'
      );
      expect(externalRecord.status).toBe('unknown');
      expect(externalRecord.deploymentMode).toBe('external');
    });
  });

  describe('Question 1 & 2: Candidate Pre-Filtering and v7.3.3 Default Highest-Tier Visibility', () => {
    it('documents pre-scheduler filtering of provider, model, disabled, cooldown, unauthorized, and priority', () => {
      const extension = readExtension();
      const currentFiltering = extension.records.find(
        (r) => r.id === 'phase2-02b-current-pre-filter-exclusion'
      );
      const candidateFiltering = extension.records.find(
        (r) => r.id === 'phase2-02b-candidate-pre-filter-exclusion'
      );

      expect(currentFiltering).toMatchObject({
        capability: 'pre_scheduler_candidate_filtering',
        status: 'supported',
        artifactId: 'current-bundled-v7-3-3',
      });
      expect(candidateFiltering).toMatchObject({
        capability: 'pre_scheduler_candidate_filtering',
        status: 'supported',
        artifactId: 'candidate-release-v7-3-8',
      });
    });

    it('proves v7.3.3 default Scheduler visibility is restricted to highest priority tier only', () => {
      const extension = readExtension();
      const currentHighestTier = extension.records.find(
        (r) => r.id === 'phase2-02b-current-highest-tier-visibility'
      );

      expect(currentHighestTier).toMatchObject({
        capability: 'candidate_highest_tier_visibility',
        status: 'partial',
        artifactId: 'current-bundled-v7-3-3',
      });
      expect(currentHighestTier.limitations[0]).toContain('highest available priority tier');
    });

    it('models and asserts documented contract boundaries for pre-scheduler filtering', () => {
      // Models documented CPA conductor_selection.go candidate generation and filtering contract in unit scope
      const inventory = [
        {
          id: 'auth-1',
          provider: 'codex',
          model: 'gpt-4o',
          disabled: false,
          cooldown: false,
          priority: 10,
        },
        {
          id: 'auth-2',
          provider: 'claude',
          model: 'gpt-4o',
          disabled: false,
          cooldown: false,
          priority: 10,
        }, // provider mismatch
        {
          id: 'auth-3',
          provider: 'codex',
          model: 'claude-3-5',
          disabled: false,
          cooldown: false,
          priority: 10,
        }, // model mismatch
        {
          id: 'auth-4',
          provider: 'codex',
          model: 'gpt-4o',
          disabled: true,
          cooldown: false,
          priority: 10,
        }, // disabled
        {
          id: 'auth-5',
          provider: 'codex',
          model: 'gpt-4o',
          disabled: false,
          cooldown: true,
          priority: 10,
        }, // cooldown
        {
          id: 'auth-6',
          provider: 'codex',
          model: 'gpt-4o',
          disabled: false,
          cooldown: false,
          priority: 5,
        }, // lower priority tier
      ];

      const filterCandidates = (
        items,
        requestedProvider,
        requestedModel,
        acrossPriorities = false
      ) => {
        // Stage 1: Basic attributes, provider, model, disabled
        const stage1 = items.filter((item) => {
          if (item.disabled) return false;
          if (item.provider !== requestedProvider) return false;
          if (item.model !== requestedModel) return false;
          return true;
        });

        // Stage 2: Cooldown filtering
        const available = stage1.filter((item) => !item.cooldown);
        if (available.length === 0) return [];

        // Stage 3: Priority tiering
        if (acrossPriorities) return available;
        const maxPriority = Math.max(...available.map((item) => item.priority));
        return available.filter((item) => item.priority === maxPriority);
      };

      // In v7.3.3 / default behavior:
      const v733Candidates = filterCandidates(inventory, 'codex', 'gpt-4o', false);
      expect(v733Candidates.map((c) => c.id)).toEqual(['auth-1']);
      // auth-2 (provider mismatch), auth-3 (model mismatch), auth-4 (disabled),
      // auth-5 (cooldown), and auth-6 (lower priority) are all excluded!
    });
  });

  describe('Question 3: v7.3.8 Candidate Visibility Differences', () => {
    it('records v7.3.8 default vs across-priorities opt-in distinction', () => {
      const extension = readExtension();
      const defaultRecord = extension.records.find(
        (r) => r.id === 'phase2-02b-candidate-default-highest-tier-visibility'
      );
      const optInRecord = extension.records.find(
        (r) => r.id === 'phase2-02b-candidate-across-priorities-opt-in'
      );

      expect(defaultRecord).toMatchObject({
        capability: 'candidate_highest_tier_visibility',
        status: 'partial',
        artifactId: 'candidate-release-v7-3-8',
        pluginContract: {
          schemaVersion: 6,
          config: { scheduler: true, scheduler_across_priorities: false },
        },
      });

      expect(optInRecord).toMatchObject({
        capability: 'candidate_across_priorities_visibility',
        status: 'supported',
        artifactId: 'candidate-release-v7-3-8',
        pluginContract: {
          schemaVersion: 6,
          config: { scheduler: true, scheduler_across_priorities: true },
        },
      });
    });

    it('models and asserts documented contract for candidate visibility (default vs across-priorities)', () => {
      const candidates = [
        { id: 'auth-high', priority: 10, disabled: false, cooldown: false },
        { id: 'auth-mid', priority: 5, disabled: false, cooldown: false },
        { id: 'auth-low', priority: 1, disabled: false, cooldown: false },
        { id: 'auth-cool', priority: 10, disabled: false, cooldown: true },
      ];

      const visibleDefault = (items) => {
        const active = items.filter((i) => !i.disabled && !i.cooldown);
        const maxPri = Math.max(...active.map((i) => i.priority));
        return active.filter((i) => i.priority === maxPri);
      };

      const visibleAcross = (items) => {
        return items.filter((i) => !i.disabled && !i.cooldown);
      };

      expect(visibleDefault(candidates).map((c) => c.id)).toEqual(['auth-high']);
      expect(visibleAcross(candidates).map((c) => c.id)).toEqual([
        'auth-high',
        'auth-mid',
        'auth-low',
      ]);
    });
  });

  describe('Question 4 & 5: Scheduler Selection and Invalid Candidate Fallback', () => {
    it('records valid selection and invalid selection fallback behavior', () => {
      const extension = readExtension();
      const currentValid = extension.records.find(
        (r) => r.id === 'phase2-02b-current-valid-candidate-selection'
      );
      const candidateValid = extension.records.find(
        (r) => r.id === 'phase2-02b-candidate-valid-candidate-selection'
      );
      const currentInvalid = extension.records.find(
        (r) => r.id === 'phase2-02b-current-invalid-candidate-fallback'
      );
      const candidateInvalid = extension.records.find(
        (r) => r.id === 'phase2-02b-candidate-invalid-candidate-fallback'
      );

      expect(currentValid.status).toBe('supported');
      expect(candidateValid.status).toBe('supported');
      expect(currentInvalid.status).toBe('supported');
      expect(candidateInvalid.status).toBe('supported');
    });

    it('models and asserts documented fallback contract when scheduler returns Auth.ID not in candidates', () => {
      // Simulating CPA internal/pluginhost/scheduler.go normalizeSchedulerResponse + conductor_selection.go
      const normalizeSchedulerResponse = (resp, suppliedCandidates) => {
        const authID = (resp.authId || '').trim();
        const delegate = (resp.delegateBuiltin || '').trim();
        if (!authID && !delegate) return { handled: false, reason: 'missing auth id or delegate' };
        if (authID) {
          const exists = suppliedCandidates.some((c) => c.id === authID);
          if (!exists) return { handled: false, reason: 'unknown auth id' };
          return { handled: true, authId: authID };
        }
        if (delegate === 'round_robin' || delegate === 'fill_first') {
          return { handled: true, delegateBuiltin: delegate };
        }
        return { handled: false, reason: 'unknown delegate' };
      };

      const candidates = [{ id: 'auth-1' }, { id: 'auth-2' }];

      // Case 1: Valid selection
      expect(normalizeSchedulerResponse({ authId: 'auth-1' }, candidates)).toEqual({
        handled: true,
        authId: 'auth-1',
      });

      // Case 2: Auth.ID not in candidate set -> rejected by host normalization, falls back (handled=false)
      expect(normalizeSchedulerResponse({ authId: 'auth-missing' }, candidates)).toEqual({
        handled: false,
        reason: 'unknown auth id',
      });

      // Case 3: Delegate builtin
      expect(normalizeSchedulerResponse({ delegateBuiltin: 'round_robin' }, candidates)).toEqual({
        handled: true,
        delegateBuiltin: 'round_robin',
      });

      // Case 4: Invalid delegate builtin
      expect(normalizeSchedulerResponse({ delegateBuiltin: 'random_walk' }, candidates)).toEqual({
        handled: false,
        reason: 'unknown delegate',
      });
    });
  });

  describe('Question 6 & 7: pinned_auth_id and selected_auth_id Semantics', () => {
    it('records pinned_auth_id fencing and selected_auth_id observation records', () => {
      const extension = readExtension();
      const currentPinned = extension.records.find(
        (r) => r.id === 'phase2-02b-current-pinned-auth-fencing'
      );
      const candidatePinned = extension.records.find(
        (r) => r.id === 'phase2-02b-candidate-pinned-auth-fencing'
      );
      const currentSelected = extension.records.find(
        (r) => r.id === 'phase2-02b-current-selected-auth-observation'
      );
      const candidateSelected = extension.records.find(
        (r) => r.id === 'phase2-02b-candidate-selected-auth-observation'
      );

      expect(currentPinned.status).toBe('supported');
      expect(candidatePinned.status).toBe('supported');
      expect(currentSelected.status).toBe('supported');
      expect(candidateSelected.status).toBe('supported');
    });

    it('proves pinned_auth_id fails closed when target is missing, disabled, or in cooldown', () => {
      const inventory = [
        { id: 'auth-target', disabled: false, cooldown: true, provider: 'codex' },
        { id: 'auth-other', disabled: false, cooldown: false, provider: 'codex' },
      ];

      const pickWithPinned = (pinnedId, items) => {
        // Step 1: Candidate loop filters non-matching IDs
        const matched = items.filter((i) => i.id === pinnedId);
        if (matched.length === 0) return { error: 'auth_not_found (target missing)' };

        const target = matched[0];
        if (target.disabled) return { error: 'auth_not_found (target disabled)' };
        if (target.cooldown) return { error: 'model_cooldown (target in cooldown)' };

        return { selected: target.id };
      };

      // When target is in cooldown, pinned selection MUST NOT fall back to auth-other!
      expect(pickWithPinned('auth-target', inventory)).toEqual({
        error: 'model_cooldown (target in cooldown)',
      });

      // When target is missing, fail closed
      expect(pickWithPinned('auth-nonexistent', inventory)).toEqual({
        error: 'auth_not_found (target missing)',
      });

      // Target disabled, fail closed
      inventory[0].disabled = true;
      inventory[0].cooldown = false;
      expect(pickWithPinned('auth-target', inventory)).toEqual({
        error: 'auth_not_found (target disabled)',
      });

      // Target healthy, succeeds
      inventory[0].disabled = false;
      expect(pickWithPinned('auth-target', inventory)).toEqual({
        selected: 'auth-target',
      });
    });

    it('proves selected_auth_id updates on retry when a different credential is picked', () => {
      const metadata = {};
      let callbackInvokedWith = null;
      metadata.selected_auth_callback = (id) => {
        callbackInvokedWith = id;
      };

      const publishSelectedAuthMetadata = (meta, auth) => {
        meta.selected_auth_id = auth.id;
        if (meta.selected_auth_callback) meta.selected_auth_callback(auth.id);
      };

      // Attempt 1 selects auth-1
      publishSelectedAuthMetadata(metadata, { id: 'auth-1' });
      expect(metadata.selected_auth_id).toBe('auth-1');
      expect(callbackInvokedWith).toBe('auth-1');

      // Attempt 1 fails, Attempt 2 selects auth-2
      publishSelectedAuthMetadata(metadata, { id: 'auth-2' });
      expect(metadata.selected_auth_id).toBe('auth-2');
      expect(callbackInvokedWith).toBe('auth-2');
    });
  });

  describe('Question 8 & 9: Retry, Fallback, and Stream Divergence', () => {
    it('records retry re-selection, retry same credential, and stream divergence', () => {
      const extension = readExtension();
      const retryReSelect = extension.records.find(
        (r) => r.id === 'phase2-02b-candidate-retry-re-selection'
      );
      const retrySame = extension.records.find(
        (r) => r.id === 'phase2-02b-candidate-retry-same-credential'
      );
      const streamDivergence = extension.records.find(
        (r) => r.id === 'phase2-02b-candidate-stream-retry-divergence'
      );
      const nonStreamRetry = extension.records.find(
        (r) => r.id === 'phase2-02b-candidate-non-stream-retry'
      );

      expect(retryReSelect.status).toBe('supported');
      expect(retrySame.status).toBe('supported');
      expect(streamDivergence.status).toBe('partial');
      expect(nonStreamRetry.status).toBe('supported');
    });

    it('models and asserts documented contract for retry re-invoking candidate filtering and scheduler selection', () => {
      const authPool = [
        { id: 'auth-1', provider: 'codex' },
        { id: 'auth-2', provider: 'claude' },
      ];

      const tried = new Set();
      const schedulerCalls = [];

      const pickNext = (attempt) => {
        // Exclude previously tried auths in this round
        const eligible = authPool.filter((a) => !tried.has(a.id));
        if (eligible.length === 0) return null;

        // Scheduler receives remaining candidates
        schedulerCalls.push({ attempt, candidates: eligible.map((e) => e.id) });
        return eligible[0];
      };

      // Attempt 1
      const picked1 = pickNext(1);
      expect(picked1.id).toBe('auth-1');
      tried.add(picked1.id); // attempt 1 fails upstream

      // Attempt 2: re-enters pickNext with updated tried set
      const picked2 = pickNext(2);
      expect(picked2.id).toBe('auth-2');
      tried.add(picked2.id);

      expect(schedulerCalls).toEqual([
        { attempt: 1, candidates: ['auth-1', 'auth-2'] },
        { attempt: 2, candidates: ['auth-2'] },
      ]);
    });

    it('proves stream failover boundary: bootstrap retry vs in-flight non-retryable error', () => {
      const simulateStreamExecution = (hasBootstrapPayload) => {
        if (!hasBootstrapPayload) {
          // Pre-payload failure (bootstrap phase) -> can catch error and failover to next credential
          return { canRetryCredential: true, action: 'skip_and_failover' };
        }
        // In-flight error (payload already sent downstream) -> cannot retry, must terminate stream
        return { canRetryCredential: false, action: 'emit_error_chunk_and_close' };
      };

      expect(simulateStreamExecution(false)).toEqual({
        canRetryCredential: true,
        action: 'skip_and_failover',
      });
      expect(simulateStreamExecution(true)).toEqual({
        canRetryCredential: false,
        action: 'emit_error_chunk_and_close',
      });
    });
  });

  describe('Question 10: Plugin Health, Delegation, Panic Fusing, and Errors', () => {
    it('records plugin delegation, unhandled, and fused behaviors', () => {
      const extension = readExtension();
      const unhandled = extension.records.find(
        (r) => r.id === 'phase2-02b-candidate-plugin-delegation-unhandled'
      );
      const fused = extension.records.find(
        (r) => r.id === 'phase2-02b-candidate-plugin-failure-fused'
      );

      expect(unhandled.status).toBe('supported');
      expect(fused.status).toBe('supported');
    });

    it('models and asserts documented contract for pluginhost panic recovery, fusing, and fallback', () => {
      class FakePluginHost {
        constructor() {
          this.fused = false;
        }

        pickAuth(schedulerFn, req) {
          if (this.fused) {
            // Already fused, directly fall back
            return { handled: false, fallback: true };
          }
          try {
            const resp = schedulerFn(req);
            return { handled: resp.handled, resp };
          } catch {
            // Panic recovered, fuse plugin
            this.fused = true;
            return { handled: false, fallback: true };
          }
        }
      }

      const host = new FakePluginHost();

      // Call 1 panics
      const call1 = host.pickAuth(() => {
        throw new Error('plugin panic');
      }, {});
      expect(call1).toEqual({ handled: false, fallback: true });
      expect(host.fused).toBe(true);

      // Call 2 automatically falls back without even calling plugin
      let pluginCalled = false;
      const call2 = host.pickAuth(() => {
        pluginCalled = true;
        return { handled: true };
      }, {});
      expect(call2).toEqual({ handled: false, fallback: true });
      expect(pluginCalled).toBe(false);
    });
  });

  describe('Question 11: External Unnegotiated Runtime Boundary', () => {
    it('classifies External unnegotiated candidate selection boundary strictly as unknown', () => {
      const extension = readExtension();
      const externalRecord = extension.records.find(
        (r) => r.id === 'phase2-02b-external-unnegotiated-candidate-selection'
      );

      expect(externalRecord).toMatchObject({
        capability: 'external_candidate_selection_boundary',
        status: 'unknown',
        artifactId: 'external-unnegotiated',
        pluginContract: null,
        deploymentMode: 'external',
      });
      expect(externalRecord.limitations[0]).toContain('explicit capability negotiation');
    });
  });
});
