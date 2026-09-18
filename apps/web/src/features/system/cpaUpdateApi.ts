import axios, { type AxiosRequestConfig } from 'axios';
import { isDemoMode } from '@/features/demo/demoMode';
import { normalizeUsageServiceBase } from '@/services/api/usageService';
import { REQUEST_TIMEOUT_MS } from '@/utils/constants';

export type CPAUpdatePhase = 'prepare' | 'activate';
export type CPAUpdateState =
  | 'never_checked'
  | 'up_to_date'
  | 'update_available'
  | 'ahead_of_stable'
  | 'unknown_version'
  | 'unsupported'
  | 'managed_externally';

export interface CPAUpdateStatus {
  mode: 'embedded' | 'external';
  state: CPAUpdateState;
  current_version?: string;
  active_artifact_id?: string;
  target_version?: string;
  last_success_at: string;
  last_error?: string;
  stale: boolean;
  prepare_supported: boolean;
  activate_supported: boolean;
  actionable: boolean;
}

export interface CPAUpdateIdentity {
  request_id: string;
  target_version: string;
}

export interface CPAUpdateRequest extends CPAUpdateIdentity {
  expected_active_artifact_id: string;
}

export interface CPAUpdateMutationResult extends CPAUpdateRequest {
  phase: CPAUpdatePhase;
  runtime_operation_id: string;
  state: 'succeeded' | 'failed';
  failure_code?: string;
  already_applied: boolean;
}

export interface CPAUpdateObservation extends CPAUpdateIdentity {
  phase: CPAUpdatePhase;
  runtime_operation_id: string;
  state: 'not_found' | 'accepted' | 'running' | 'succeeded' | 'failed';
  failure_code?: string;
  created_at?: string;
  updated_at?: string;
  completed_at?: string;
}

export interface CPAUpdateConnection {
  base: string;
  key: string;
}

export class CPAUpdateAPIError extends Error {
  constructor(
    readonly kind: 'context_changed' | 'unavailable',
    readonly code: string
  ) {
    super(code);
  }
}

const isRecord = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value);
const stableVersion = /^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/;
const artifactID = /^sha256:[a-f0-9]{64}$/;
const safeCode = (value: unknown): value is string =>
  typeof value === 'string' && /^[a-z0-9_]{1,64}$/.test(value);

export const isCPAUpdateIdentity = (value: unknown): value is CPAUpdateIdentity =>
  isRecord(value) &&
  typeof value.request_id === 'string' &&
  /^[A-Za-z0-9_.:-]{1,64}$/.test(value.request_id) &&
  typeof value.target_version === 'string' &&
  stableVersion.test(value.target_version);

export const isCPAUpdateRequest = (value: unknown): value is CPAUpdateRequest =>
  isCPAUpdateIdentity(value) &&
  'expected_active_artifact_id' in value &&
  typeof value.expected_active_artifact_id === 'string' &&
  artifactID.test(value.expected_active_artifact_id);

export function canPrepareCPAUpdate(status: CPAUpdateStatus | null): boolean {
  return !!(
    status?.mode === 'embedded' &&
    status.state === 'update_available' &&
    status.actionable &&
    !status.stale &&
    !status.last_error &&
    status.prepare_supported &&
    status.activate_supported &&
    isCPAUpdateRequest({
      request_id: 'guard',
      target_version: status.target_version,
      expected_active_artifact_id: status.active_artifact_id,
    })
  );
}

function parseStatus(value: unknown): CPAUpdateStatus {
  const states: CPAUpdateState[] = [
    'never_checked',
    'up_to_date',
    'update_available',
    'ahead_of_stable',
    'unknown_version',
    'unsupported',
    'managed_externally',
  ];
  if (
    !isRecord(value) ||
    !['embedded', 'external'].includes(String(value.mode)) ||
    !states.includes(value.state as CPAUpdateState) ||
    ['stale', 'prepare_supported', 'activate_supported', 'actionable'].some(
      (field) => typeof value[field] !== 'boolean'
    ) ||
    typeof value.last_success_at !== 'string' ||
    ['current_version', 'target_version', 'active_artifact_id', 'last_error'].some(
      (field) => value[field] !== undefined && typeof value[field] !== 'string'
    )
  ) {
    throw new CPAUpdateAPIError('unavailable', 'response_unreliable');
  }
  return value as unknown as CPAUpdateStatus;
}

function matchesOperation(
  value: unknown,
  phase: CPAUpdatePhase,
  request: CPAUpdateIdentity
): value is Record<string, unknown> {
  return (
    isRecord(value) &&
    value.phase === phase &&
    value.request_id === request.request_id &&
    value.target_version === request.target_version &&
    typeof value.runtime_operation_id === 'string' &&
    value.runtime_operation_id.length > 0
  );
}

function parseMutation(
  value: unknown,
  phase: CPAUpdatePhase,
  request: CPAUpdateRequest
): CPAUpdateMutationResult {
  if (
    !matchesOperation(value, phase, request) ||
    value.expected_active_artifact_id !== request.expected_active_artifact_id ||
    !['succeeded', 'failed'].includes(String(value.state)) ||
    typeof value.already_applied !== 'boolean' ||
    (value.already_applied && (phase !== 'activate' || value.state !== 'succeeded')) ||
    (value.state === 'failed' && !safeCode(value.failure_code)) ||
    (value.state === 'succeeded' && !!value.failure_code)
  ) {
    throw new CPAUpdateAPIError('unavailable', 'response_unreliable');
  }
  return value as unknown as CPAUpdateMutationResult;
}

function parseObservation(
  value: unknown,
  phase: CPAUpdatePhase,
  request: CPAUpdateIdentity
): CPAUpdateObservation {
  if (
    !matchesOperation(value, phase, request) ||
    !['not_found', 'accepted', 'running', 'succeeded', 'failed'].includes(String(value.state)) ||
    (value.state === 'failed' && !safeCode(value.failure_code)) ||
    (value.state !== 'failed' && !!value.failure_code)
  ) {
    throw new CPAUpdateAPIError('unavailable', 'response_unreliable');
  }
  // In particular, observation cannot supply already_applied evidence.
  return {
    phase,
    ...request,
    runtime_operation_id: value.runtime_operation_id as string,
    state: value.state as CPAUpdateObservation['state'],
    failure_code: value.failure_code as string | undefined,
  };
}

function normalizeError(error: unknown): CPAUpdateAPIError {
  if (error instanceof CPAUpdateAPIError) return error;
  const response = axios.isAxiosError(error) ? error.response : undefined;
  const code =
    isRecord(response?.data) && safeCode(response.data.code)
      ? response.data.code
      : 'observation_unavailable';
  return new CPAUpdateAPIError(
    response && [400, 401, 403, 409].includes(response.status) ? 'context_changed' : 'unavailable',
    code
  );
}

async function request<T>(
  connection: CPAUpdateConnection,
  suffix: string,
  parse: (data: unknown) => T,
  options: AxiosRequestConfig = {},
  acceptsTerminalFailure = false
): Promise<T> {
  // Guard the transport as well as the UI, including direct calls in demo mode.
  if (!connection.base || !connection.key || (__DEMO_SITE__ && isDemoMode())) {
    throw new CPAUpdateAPIError('unavailable', 'update_unavailable');
  }
  try {
    const response = await axios.request<unknown>({
      ...options,
      url: normalizeUsageServiceBase(connection.base) + '/usage-service/runtime/updates' + suffix,
      timeout: REQUEST_TIMEOUT_MS,
      headers: { Authorization: 'Bearer ' + connection.key },
    });
    return parse(response.data);
  } catch (error) {
    // Only a correlated durable failed result is confirmation of failure.
    // A generic 502/503, timeout, or lost response carries no such evidence.
    if (
      acceptsTerminalFailure &&
      axios.isAxiosError(error) &&
      error.response?.status === 502 &&
      isRecord(error.response.data) &&
      error.response.data.state === 'failed'
    ) {
      return parse(error.response.data);
    }
    throw normalizeError(error);
  }
}

export const cpaUpdateApi = {
  status: (connection: CPAUpdateConnection, signal?: AbortSignal) =>
    request(connection, '', parseStatus, { method: 'GET', signal }),
  check: (connection: CPAUpdateConnection, signal?: AbortSignal) =>
    request(connection, '/check', parseStatus, { method: 'POST', signal }),
  mutate: (
    connection: CPAUpdateConnection,
    phase: CPAUpdatePhase,
    intent: CPAUpdateRequest,
    signal?: AbortSignal
  ) => {
    if (!isCPAUpdateRequest(intent)) {
      return Promise.reject(new CPAUpdateAPIError('context_changed', 'invalid_request'));
    }
    const data: CPAUpdateRequest = {
      request_id: intent.request_id,
      target_version: intent.target_version,
      expected_active_artifact_id: intent.expected_active_artifact_id,
    };
    return request(
      connection,
      '/' + phase,
      (value) => parseMutation(value, phase, data),
      { method: 'POST', data, signal },
      true
    );
  },
  observe: (
    connection: CPAUpdateConnection,
    phase: CPAUpdatePhase,
    intent: CPAUpdateIdentity,
    signal?: AbortSignal
  ) => {
    if (!isCPAUpdateIdentity(intent)) {
      return Promise.reject(new CPAUpdateAPIError('context_changed', 'invalid_request'));
    }
    const params = { request_id: intent.request_id, target_version: intent.target_version };
    return request(
      connection,
      '/operations/' + phase,
      (value) => parseObservation(value, phase, params),
      { method: 'GET', params, signal }
    );
  },
};
