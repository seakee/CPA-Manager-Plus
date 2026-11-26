import axios, { type AxiosRequestConfig } from 'axios';
import { normalizeUsageServiceBase } from '@/services/api/usageService';
import { REQUEST_TIMEOUT_MS } from '@/utils/constants';

export const CPA_UPDATE_MAX_UPLOAD_BYTES = 64 * 1024 * 1024;
export const CPA_UPDATE_MAX_CDN_PREFIX_LENGTH = 512;

export type CPAUpdatePhase = 'prepare' | 'activate';
export type CPAUpdatePrepareSourceMode = 'github' | 'cdn';
export type CPAUpdateIntentSourceMode = CPAUpdatePrepareSourceMode | 'upload';
export type CPAUpdateState =
  | 'never_checked'
  | 'up_to_date'
  | 'update_available'
  | 'ahead_of_stable'
  | 'unknown_version'
  | 'unsupported'
  | 'managed_externally';

export interface CPABaselineStatus {
  pinned_version: string;
  pinned_artifact_id: string;
  bundled_available?: boolean;
  adopt_baseline_available: boolean;
  baseline_upgrade_available: boolean;
}
export interface CPABaselineAdoptResult {
  active_artifact_id: string;
  baseline: CPABaselineStatus;
}

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
  baseline?: CPABaselineStatus;
}
export interface CPAUpdateIdentity {
  request_id: string;
  target_version: string;
}

export interface CPAUpdateRequest extends CPAUpdateIdentity {
  expected_active_artifact_id: string;
  source_mode?: CPAUpdatePrepareSourceMode;
  cdn_prefix?: string;
}
export type CPAUpdateUploadRequest = Omit<CPAUpdateRequest, 'source_mode'> & {
  source_mode?: CPAUpdateIntentSourceMode;
};


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
  stage_phase?: string;
  received_bytes?: number;
  total_bytes?: number;
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
    readonly kind: 'rejected' | 'context_changed' | 'unavailable',
    readonly code: string
  ) {
    super(code);
  }
}

const isRecord = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value);
// Manager's CPA contract uses canonical versions, not GitHub release tags.
const stableVersion = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/;
const artifactID = /^sha256:[a-f0-9]{64}$/;
const safeCode = (value: unknown): value is string =>
  typeof value === 'string' && /^[a-z0-9_]{1,64}$/.test(value);
function utf8ByteLength(value: string): number {
  let bytes = 0;
  for (let i = 0; i < value.length; i += 1) {
    const code = value.codePointAt(i)!;
    if (code <= 0x7f) bytes += 1;
    else if (code <= 0x7ff) bytes += 2;
    else if (code <= 0xffff) bytes += 3;
    else {
      bytes += 4;
      i += 1;
    }
  }
  return bytes;
}

function parseStrictIPv4(host: string): [number, number, number, number] | null {
  const parts = host.split('.');
  if (parts.length !== 4) return null;
  const octets: number[] = [];
  for (const part of parts) {
    if (!/^(0|[1-9]\d{0,2})$/.test(part)) return null;
    const n = Number(part);
    if (n > 255) return null;
    octets.push(n);
  }
  return octets as [number, number, number, number];
}

function parseStrictIPv6(host: string): number[] | null {
  if (!host || host.includes('%')) return null;
  const doubleColonIndex = host.indexOf('::');
  if (doubleColonIndex !== -1 && host.indexOf('::', doubleColonIndex + 2) !== -1) {
    return null;
  }
  const parseSide = (side: string, allowIPv4Tail: boolean): number[] | null => {
    if (!side) return [];
    const parts = side.split(':');
    const groups: number[] = [];
    for (let i = 0; i < parts.length; i += 1) {
      const part = parts[i];
      if (!part) return null;
      if (allowIPv4Tail && i === parts.length - 1 && part.includes('.')) {
        const v4 = parseStrictIPv4(part);
        if (!v4) return null;
        groups.push((v4[0] << 8) | v4[1], (v4[2] << 8) | v4[3]);
        continue;
      }
      if (!/^[0-9a-fA-F]{1,4}$/.test(part)) return null;
      groups.push(Number.parseInt(part, 16));
    }
    return groups;
  };
  if (doubleColonIndex === -1) {
    const groups = parseSide(host, true);
    return groups && groups.length === 8 ? groups : null;
  }
  const leftStr = host.slice(0, doubleColonIndex);
  const rightStr = host.slice(doubleColonIndex + 2);
  const left = parseSide(leftStr, false);
  const right = parseSide(rightStr, true);
  if (!left || !right) return null;
  const missing = 8 - (left.length + right.length);
  if (missing < 1) return null;
  return [...left, ...new Array<number>(missing).fill(0), ...right];
}

function isPublicIPv4([a, b, c, d]: [number, number, number, number]): boolean {
  if (a === 192 && b === 0 && c === 0 && (d === 9 || d === 10)) return true;
  if (a === 0 || a === 10 || a === 127) return false;
  if (a === 100 && b >= 64 && b <= 127) return false;
  if (a === 169 && b === 254) return false;
  if (a === 172 && b >= 16 && b <= 31) return false;
  if (a === 192 && b === 0 && (c === 0 || c === 2)) return false;
  if (a === 192 && b === 88 && c === 99) return false;
  if (a === 192 && b === 168) return false;
  if (a === 198 && (b === 18 || b === 19)) return false;
  if (a === 198 && b === 51 && c === 100) return false;
  if (a === 203 && b === 0 && c === 113) return false;
  if (a >= 224) return false;
  return true;
}

function isPublicIPv6(g: number[]): boolean {
  if (g.length !== 8) return false;
  if (g[0] === 0 && g[1] === 0 && g[2] === 0 && g[3] === 0 && g[4] === 0 && g[5] === 0xffff) {
    return isPublicIPv4([g[6] >> 8, g[6] & 0xff, g[7] >> 8, g[7] & 0xff]);
  }
  if (
    g[0] === 0x0064 &&
    g[1] === 0xff9b &&
    g[2] === 0 &&
    g[3] === 0 &&
    g[4] === 0 &&
    g[5] === 0
  ) {
    return isPublicIPv4([g[6] >> 8, g[6] & 0xff, g[7] >> 8, g[7] & 0xff]);
  }
  if ((g[0] & 0xe000) !== 0x2000) return false;
  if (g[0] === 0x2001 && (g[1] & 0xfe00) === 0x0000) return false;
  if (g[0] === 0x2001 && g[1] === 0x0db8) return false;
  if (g[0] === 0x2002) return false;
  if (g[0] === 0x3fff && (g[1] & 0xf000) === 0x0000) return false;
  if (g[0] === 0x5f00) return false;
  return true;
}

function isValidCDNHostLiteral(rawHost: string, isBracketedIPv6: boolean): boolean {
  if (!rawHost) return false;
  if (isBracketedIPv6) {
    const groups = parseStrictIPv6(rawHost);
    return groups !== null && isPublicIPv6(groups);
  }
  const v4 = parseStrictIPv4(rawHost);
  if (v4) return isPublicIPv4(v4);
  if (rawHost.includes(':')) return false;
  const normalized = rawHost.toLowerCase().replace(/\.$/, '');
  if (!normalized || normalized === 'localhost' || normalized.endsWith('.localhost')) {
    return false;
  }
  const labels = normalized.split('.');
  if (labels.some((label) => label.length === 0)) return false;
  const lastLabel = labels[labels.length - 1];
  if (!lastLabel || lastLabel.startsWith('0x') || /^[0-9]+$/.test(lastLabel)) {
    return false;
  }
  return true;
}

export function isValidHttpsCDNPrefix(value: string): boolean {
  if (
    !value ||
    value !== value.trim() ||
    utf8ByteLength(value) > CPA_UPDATE_MAX_CDN_PREFIX_LENGTH ||
    !value.startsWith('https://')
  ) {
    return false;
  }
  for (let i = 0; i < value.length; i += 1) {
    const code = value.charCodeAt(i);
    if (
      code < 0x20 ||
      code === 0x7f ||
      value[i] === '\\' ||
      value[i] === '?' ||
      value[i] === '#' ||
      value[i] === '@'
    ) {
      return false;
    }
  }
  if (/\s/.test(value)) return false;

  const rest = value.slice('https://'.length);
  const slashIndex = rest.indexOf('/');
  const rawAuthority = slashIndex === -1 ? rest : rest.slice(0, slashIndex);
  const rawPath = slashIndex === -1 ? '' : rest.slice(slashIndex);
  if (!rawAuthority || rawAuthority.endsWith(':')) return false;

  let rawHost: string;
  let rawPort = '';
  let isBracketedIPv6 = false;
  if (rawAuthority.startsWith('[')) {
    const closeBracket = rawAuthority.indexOf(']');
    if (closeBracket <= 1) return false;
    rawHost = rawAuthority.slice(1, closeBracket);
    isBracketedIPv6 = true;
    const suffix = rawAuthority.slice(closeBracket + 1);
    if (suffix.length > 0) {
      if (!suffix.startsWith(':')) return false;
      rawPort = suffix.slice(1);
      if (!rawPort) return false;
    }
  } else {
    if (rawAuthority.includes('[') || rawAuthority.includes(']')) return false;
    const colonIndex = rawAuthority.indexOf(':');
    if (colonIndex !== -1) {
      rawHost = rawAuthority.slice(0, colonIndex);
      rawPort = rawAuthority.slice(colonIndex + 1);
      if (!rawPort) return false;
    } else {
      rawHost = rawAuthority;
    }
  }
  if (rawPort) {
    if (!/^[0-9]+$/.test(rawPort)) return false;
    const portNumber = Number(rawPort);
    if (!Number.isSafeInteger(portNumber) || portNumber < 1 || portNumber > 65535) return false;
  }
  if (!isValidCDNHostLiteral(rawHost, isBracketedIPv6)) return false;

  if (rawPath) {
    const trimmedRawPath = rawPath.replace(/\/+$/, '');
    if (
      trimmedRawPath.includes('//') ||
      trimmedRawPath.split('/').some((seg) => seg === '.' || seg === '..')
    ) {
      return false;
    }
    let decodedPath: string;
    try {
      decodedPath = decodeURIComponent(rawPath);
    } catch {
      return false;
    }
    const trimmedDecodedPath = decodedPath.replace(/\/+$/, '');
    if (
      trimmedDecodedPath.includes('//') ||
      trimmedDecodedPath.split('/').some((seg) => seg === '.' || seg === '..')
    ) {
      return false;
    }
  }

  try {
    const parsed = new URL(value);
    if (
      parsed.protocol !== 'https:' ||
      !parsed.hostname ||
      parsed.username ||
      parsed.password ||
      parsed.search ||
      parsed.hash
    ) {
      return false;
    }
    const parsedHost = parsed.hostname;
    const parsedBracketed = parsedHost.startsWith('[') && parsedHost.endsWith(']');
    const unbracketedParsedHost = parsedBracketed ? parsedHost.slice(1, -1) : parsedHost;
    return isValidCDNHostLiteral(unbracketedParsedHost, parsedBracketed);
  } catch {
    return false;
  }
}


export const isCPAUpdateIdentity = (value: unknown): value is CPAUpdateIdentity =>
  isRecord(value) &&
  typeof value.request_id === 'string' &&
  /^[A-Za-z0-9_.:-]{1,64}$/.test(value.request_id) &&
  typeof value.target_version === 'string' &&
  value.target_version.length <= 96 &&
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

function isBaselineStatus(value: unknown): value is CPABaselineStatus {
  return (
    isRecord(value) &&
    typeof value.pinned_version === 'string' &&
    typeof value.pinned_artifact_id === 'string' &&
    (value.bundled_available === undefined || typeof value.bundled_available === 'boolean') &&
    typeof value.adopt_baseline_available === 'boolean' &&
    typeof value.baseline_upgrade_available === 'boolean'
  );
}
function parseAdoptBaseline(value: unknown): CPABaselineAdoptResult {
  if (
    !isRecord(value) ||
    typeof value.active_artifact_id !== 'string' ||
    !artifactID.test(value.active_artifact_id) ||
    !isBaselineStatus(value.baseline)
  ) {
    throw new CPAUpdateAPIError('unavailable', 'response_unreliable');
  }
  return value as unknown as CPABaselineAdoptResult;
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
    ) ||
    (value.baseline !== undefined && !isBaselineStatus(value.baseline))
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
  const observation: CPAUpdateObservation = {
    phase,
    ...request,
    runtime_operation_id: value.runtime_operation_id as string,
    state: value.state as CPAUpdateObservation['state'],
    failure_code: value.failure_code as string | undefined,
  };
  if (typeof value.stage_phase === 'string' && /^[a-z0-9_]{1,32}$/.test(value.stage_phase)) {
    observation.stage_phase = value.stage_phase;
  }
  if (typeof value.received_bytes === 'number' && Number.isFinite(value.received_bytes) && value.received_bytes >= 0) {
    observation.received_bytes = value.received_bytes;
  }
  if (typeof value.total_bytes === 'number' && Number.isFinite(value.total_bytes) && value.total_bytes >= 0) {
    observation.total_bytes = value.total_bytes;
  }
  return observation;
}

function normalizeError(error: unknown): CPAUpdateAPIError {
  if (error instanceof CPAUpdateAPIError) return error;
  const response = axios.isAxiosError(error) ? error.response : undefined;
  if (response?.status === 413) {
    const code =
      isRecord(response.data) && safeCode(response.data.code)
        ? response.data.code
        : 'archive_too_large';
    return new CPAUpdateAPIError('rejected', code);
  }
  if (response?.status === 400) {
    const code =
      isRecord(response.data) && safeCode(response.data.code)
        ? response.data.code
        : 'invalid_request';
    return new CPAUpdateAPIError('rejected', code);
  }
  const code =
    isRecord(response?.data) && safeCode(response.data.code)
      ? response.data.code
      : 'observation_unavailable';
  return new CPAUpdateAPIError(
    response && [401, 403, 409].includes(response.status) ? 'context_changed' : 'unavailable',
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
  // Guard the transport as well as the UI, including direct calls.
  if (__PANEL_LITE__ || !connection.base || !connection.key) {
    throw new CPAUpdateAPIError('unavailable', 'update_unavailable');
  }
  try {
    const response = await axios.request<unknown>({
      ...options,
      url: normalizeUsageServiceBase(connection.base) + '/usage-service/runtime/updates' + suffix,
      timeout: options.timeout ?? REQUEST_TIMEOUT_MS,
      headers: {
        ...options.headers,
        Authorization: 'Bearer ' + connection.key,
      },
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
      return Promise.reject(new CPAUpdateAPIError('rejected', 'invalid_request'));
    }
    const rawSourceMode: unknown = intent.source_mode;
    const rawCDNPrefix: unknown = intent.cdn_prefix;
    let sourceFields: { source_mode: 'cdn'; cdn_prefix: string } | Record<string, never> = {};
    if (phase === 'prepare') {
      if (rawSourceMode === 'cdn') {
        const trimmed = typeof rawCDNPrefix === 'string' ? rawCDNPrefix.trim() : '';
        if (!isValidHttpsCDNPrefix(trimmed)) {
          return Promise.reject(new CPAUpdateAPIError('rejected', 'invalid_request'));
        }
        sourceFields = { source_mode: 'cdn', cdn_prefix: trimmed };
      } else if (rawSourceMode === undefined || rawSourceMode === 'github') {
        if (typeof rawCDNPrefix === 'string' && rawCDNPrefix.trim() !== '') {
          return Promise.reject(new CPAUpdateAPIError('rejected', 'invalid_request'));
        }
      } else {
        return Promise.reject(new CPAUpdateAPIError('rejected', 'invalid_request'));
      }
    }
    const data: CPAUpdateRequest = {
      request_id: intent.request_id,
      target_version: intent.target_version,
      expected_active_artifact_id: intent.expected_active_artifact_id,
      ...sourceFields,
    };
    return request(
      connection,
      '/' + phase,
      (value) => parseMutation(value, phase, data),
      { method: 'POST', data, signal },
      true
    );
  },
  prepareUpload: (
    connection: CPAUpdateConnection,
    intent: CPAUpdateUploadRequest,
    file: Blob,
    onProgress?: (loaded: number, total: number) => void,
    signal?: AbortSignal
  ) => {
    if (!isCPAUpdateRequest(intent)) {
      return Promise.reject(new CPAUpdateAPIError('rejected', 'invalid_request'));
    }
    const rawSourceMode: unknown = intent.source_mode;
    if (
      (rawSourceMode !== undefined && rawSourceMode !== 'upload') ||
      (typeof intent.cdn_prefix === 'string' && intent.cdn_prefix.trim() !== '')
    ) {
      return Promise.reject(new CPAUpdateAPIError('rejected', 'invalid_request'));
    }
    if (!file || typeof file.size !== 'number' || !Number.isFinite(file.size) || file.size <= 0) {
      return Promise.reject(new CPAUpdateAPIError('rejected', 'invalid_request'));
    }
    if (file.size > CPA_UPDATE_MAX_UPLOAD_BYTES) {
      return Promise.reject(new CPAUpdateAPIError('rejected', 'archive_too_large'));
    }
    const expected: CPAUpdateRequest = {
      request_id: intent.request_id,
      target_version: intent.target_version,
      expected_active_artifact_id: intent.expected_active_artifact_id,
    };
    const params = {
      request_id: intent.request_id,
      target_version: intent.target_version,
      expected_active_artifact_id: intent.expected_active_artifact_id,
    };
    return request(
      connection,
      '/prepare-upload',
      (value) => parseMutation(value, 'prepare', expected),
      {
        method: 'POST',
        params,
        data: file,
        timeout: 900_000,
        signal,
        headers: { 'Content-Type': 'application/octet-stream' },
        onUploadProgress: onProgress
          ? (event) => {
              onProgress(event.loaded ?? 0, event.total ?? file.size ?? 0);
            }
          : undefined,
      },
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
      return Promise.reject(new CPAUpdateAPIError('rejected', 'invalid_request'));
    }
    const params = { request_id: intent.request_id, target_version: intent.target_version };
    return request(
      connection,
      '/operations/' + phase,
      (value) => parseObservation(value, phase, params),
      { method: 'GET', params, signal }
    );
  },
  adoptBaseline: (
    connection: CPAUpdateConnection,
    expectedActiveArtifactId: string,
    signal?: AbortSignal
  ) => {
    if (!expectedActiveArtifactId || !artifactID.test(expectedActiveArtifactId)) {
      return Promise.reject(new CPAUpdateAPIError('rejected', 'invalid_request'));
    }
    return request(connection, '/adopt-baseline', parseAdoptBaseline, {
      method: 'POST',
      data: { expected_active_artifact_id: expectedActiveArtifactId },
      signal,
    });
  },
};
