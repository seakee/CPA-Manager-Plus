import type {
  CPAUpdateMutationResult,
  CPAUpdateObservation,
  CPAUpdatePhase,
  CPAUpdateRequest,
  CPAUpdateStatus,
} from './cpaUpdateApi';
import type { CPAUpdateIntent } from './cpaUpdateIntentStorage';

export const originalArtifact = 'sha256:' + 'a'.repeat(64);
export const replacementArtifact = 'sha256:' + 'b'.repeat(64);
export const updateStatus = (patch: Partial<CPAUpdateStatus> = {}): CPAUpdateStatus => ({
  mode: 'embedded',
  state: 'update_available',
  current_version: 'v7.1.0',
  active_artifact_id: originalArtifact,
  target_version: 'v7.2.0',
  last_success_at: '2026-09-18T12:00:00Z',
  stale: false,
  prepare_supported: true,
  activate_supported: true,
  actionable: true,
  ...patch,
});
export const updateIntent = (patch: Partial<CPAUpdateIntent> = {}): CPAUpdateIntent => ({
  schema_version: 1,
  request_id: 'test-update-123',
  target_version: 'v7.2.0',
  expected_active_artifact_id: originalArtifact,
  phase: 'prepare',
  client_stage: 'submitted',
  created_at_ms: 1000,
  updated_at_ms: 1000,
  ...patch,
});
export const mutationResult = (
  phase: CPAUpdatePhase,
  request: CPAUpdateRequest,
  patch: Partial<CPAUpdateMutationResult> = {}
): CPAUpdateMutationResult => ({
  phase,
  request_id: request.request_id,
  target_version: request.target_version,
  expected_active_artifact_id: request.expected_active_artifact_id,
  runtime_operation_id: 'cpa-update-' + phase + '-test',
  state: 'succeeded',
  already_applied: false,
  ...patch,
});
export const observationResult = (
  intent: CPAUpdateIntent,
  state: CPAUpdateObservation['state'] = 'running',
  patch: Partial<CPAUpdateObservation> = {}
): CPAUpdateObservation => ({
  phase: intent.phase,
  request_id: intent.request_id,
  target_version: intent.target_version,
  runtime_operation_id: 'cpa-update-' + intent.phase + '-test',
  state,
  ...(state === 'failed' ? { failure_code: 'target_stage_corrupt' } : {}),
  ...patch,
});

export function memoryStorage(): Storage {
  const values = new Map<string, string>();
  return {
    get length() {
      return values.size;
    },
    getItem: (key) => values.get(key) ?? null,
    setItem: (key, value) => {
      values.set(key, value);
    },
    removeItem: (key) => {
      values.delete(key);
    },
    clear: () => values.clear(),
    key: (index) => Array.from(values.keys())[index] ?? null,
  };
}
