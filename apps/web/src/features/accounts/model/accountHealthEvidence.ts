import type { MonitoringAccountLatestRequest } from '@/services/api';
import { getAuthFileCredentialStatusCode } from '@/features/authFiles/model/credentialStatus';
import type { AccountRow } from './accountRows';

export type AccountRequestEvidenceKind =
  | 'success'
  | 'credential_failure'
  | 'transient_failure'
  | 'quota'
  | 'neutral';

export type AccountRequestEvidenceDirection = 'positive' | 'negative';

export type AccountCredentialStatusEvidenceKind =
  | Exclude<AccountRequestEvidenceKind, 'success'>
  | 'healthy'
  | 'none';

export type AccountObservedDiagnosticEvidenceKind =
  | Exclude<AccountRequestEvidenceKind, 'success'>
  | 'none';

export interface AccountRequestEvidenceInput {
  latestRequest?: MonitoringAccountLatestRequest | null;
  recentRequests?: readonly MonitoringAccountLatestRequest[] | null;
}

export interface AccountRequestCredentialEvidence {
  kind: Extract<AccountRequestEvidenceKind, 'success' | 'credential_failure' | 'quota'>;
  direction: AccountRequestEvidenceDirection;
  request: MonitoringAccountLatestRequest;
}

export interface AccountRequestHealthEvidence {
  kind: Extract<
    AccountRequestEvidenceKind,
    'success' | 'credential_failure' | 'transient_failure' | 'quota'
  >;
  direction: AccountRequestEvidenceDirection;
  request: MonitoringAccountLatestRequest;
  consecutiveTransientFailures: number;
  credentialEvidence: AccountRequestCredentialEvidence | null;
}

export interface AccountRequestQuotaEvidence {
  kind: 'quota';
  request: MonitoringAccountLatestRequest;
}

export type AccountAuthenticationProblemSource =
  | 'request'
  | 'quota_refresh'
  | 'inspection'
  | 'observed_header'
  | 'credential_status';

export interface AccountAuthenticationProblemEvidence {
  source: AccountAuthenticationProblemSource;
  observedAtMs: number | null;
  statusCode: number | null;
}

export type AccountExceptionProblemSource = 'request' | 'quota_refresh' | 'inspection';

export interface AccountExceptionProblemEvidence {
  source: AccountExceptionProblemSource;
  observedAtMs: number | null;
}

export type AccountRequestEvidenceBySelectionKey = ReadonlyMap<string, AccountRequestEvidenceInput>;

const TRANSIENT_FAILURE_THRESHOLD = 2;
const NEUTRAL_HTTP_STATUS_CODES = new Set([400, 409, 413, 422, 499]);
const RESOLVED_INSPECTION_ACTION_STATUSES = new Set(['success', 'skipped', 'executed']);
const UNRESOLVED_INSPECTION_ACTION_STATUSES = new Set(['pending', 'failed', 'needs_review']);

const normalizeText = (value: string | null | undefined): string =>
  value?.trim().toLowerCase() ?? '';

const getRequestFailureText = (request: MonitoringAccountLatestRequest): string =>
  [request.fail_summary, request.header_error_kind, request.header_error_code]
    .map(normalizeText)
    .filter(Boolean)
    .join(' ');

const CREDENTIAL_FAILURE_PATTERNS = [
  /\binvalid[_ -]?grant\b/,
  /\binvalid[_ -]?(?:api[_ -]?key|token|refresh[_ -]?token|credentials?)\b/,
  /\b(?:authentication|auth)[_ -]?(?:error|failed|failure|invalid)\b/,
  /\bunauthenticated\b/,
  /\bunauthorized\b/,
  /\bbad[_ -]?credentials?\b/,
  /\b(?:token|credentials?)[_ -]?(?:expired|invalidated|revoked)\b/,
  /\b(?:expired|invalidated|revoked)[_ -]?(?:token|credentials?)\b/,
  /\brefresh[_ -]?token[_ -]?reused\b/,
  /\bno[_ -]?auth[_ -]?context\b/,
] as const;

const QUOTA_FAILURE_PATTERNS = [
  /\binsufficient[_ -]?quota\b/,
  /\bquota[_ -]?(?:exceeded|depleted|exhausted|limited|reached)\b/,
  /\bquota[_ -]?limit[_ -]?(?:exceeded|reached)\b/,
  /\bcredits?[_ -]?(?:depleted|exhausted|limited)\b/,
  /\bcredits?[_ -]?limit[_ -]?(?:exceeded|reached)\b/,
  /\bfree[_ -]?usage[_ -]?(?:depleted|exhausted|limited)\b/,
  /\bfree[_ -]?usage[_ -]?limit[_ -]?(?:exceeded|reached)\b/,
  /\brate[_ -]?limit(?:ed|[_ -]?(?:exceeded|reached))(?:\b|_)/,
  /\b(?:usage|billing|spending)[_ -]?limit(?:ed|[_ -]?(?:exceeded|reached))(?:\b|_)/,
] as const;

const REQUEST_FAILURE_PATTERNS = [
  /\binvalid[_ -]?request(?:[_ -]?error)?\b/,
  /\bbad[_ -]?request(?:[_ -]?error)?\b/,
  /\bmalformed[_ -]?request(?:[_ -]?error)?\b/,
  /\brequest[_ -]?(?:invalid|too[_ -]?large)\b/,
  /\bunsupported[_ -]?(?:parameter|model|request)\b/,
  /\bmissing[_ -]?(?:required[_ -]?)?(?:parameter|field)\b/,
  /\bunprocessable[_ -]?entity\b/,
  /\bpayload[_ -]?too[_ -]?large\b/,
] as const;

const HEALTHY_CREDENTIAL_STATUS_MESSAGES = new Set([
  'active',
  'available',
  'enabled',
  'healthy',
  'ok',
  'ready',
  'success',
  'successful',
]);

const matchesAnyPattern = (value: string, patterns: readonly RegExp[]): boolean =>
  patterns.some((pattern) => pattern.test(value));

const isCredentialFailureText = (value: string): boolean =>
  matchesAnyPattern(value, CREDENTIAL_FAILURE_PATTERNS);

const isQuotaFailureText = (value: string): boolean =>
  matchesAnyPattern(value, QUOTA_FAILURE_PATTERNS);

const isRequestFailureText = (value: string): boolean =>
  matchesAnyPattern(value, REQUEST_FAILURE_PATTERNS);

const isNeutralTransportFailureText = (value: string): boolean =>
  /\b(context[_ -]?cancel(?:ed|led)|request[_ -]?cancel(?:ed|led)|client[_ -]?cancel(?:ed|led)?|cancel(?:ed|led)[_ -]?by[_ -]?client|client[_ -]?(?:disconnect(?:ed)?|closed[_ -]?(?:request|connection))|unexpected[_ -]?eof[_ -]?(?:from|by)[_ -]?client)\b/.test(
    value
  ) || /\bwebsocket: close 100[01]\b/.test(value);

const readStatusCode = (value: unknown): number | null => {
  const parsed = typeof value === 'number' ? value : Number.parseInt(String(value ?? ''), 10);
  return Number.isFinite(parsed) ? parsed : null;
};

const readStatusCodeFromText = (value: string): number | null => {
  const match = value.match(/\b([1-5][0-9]{2})\b/);
  return match ? readStatusCode(match[1]) : null;
};

const getRawCredentialStatusCode = (row: AccountRow): number | null => {
  if (row.rawCredentialStatusSuperseded) return null;
  const statusCode = getAuthFileCredentialStatusCode(row.raw);
  if (statusCode !== null) return statusCode;
  return readStatusCodeFromText(row.statusMessage);
};

export const classifyAccountCredentialStatusEvidence = (
  row: AccountRow
): AccountCredentialStatusEvidenceKind => {
  const statusMessage = normalizeText(row.statusMessage);
  const statusCode = getRawCredentialStatusCode(row);
  if (!statusMessage && statusCode === null) return 'none';
  if (
    statusCode !== null &&
    statusCode >= 200 &&
    statusCode < 400 &&
    (!statusMessage || HEALTHY_CREDENTIAL_STATUS_MESSAGES.has(statusMessage))
  ) {
    return 'healthy';
  }
  if (statusCode === null && HEALTHY_CREDENTIAL_STATUS_MESSAGES.has(statusMessage)) {
    return 'healthy';
  }

  const requestKind = classifyAccountRequestEvidence({
    timestamp_ms: row.updatedAtMs ?? 0,
    failed: true,
    fail_status_code: statusCode,
    fail_summary: statusMessage,
  });
  return requestKind === 'success' ? 'healthy' : requestKind;
};

const getAccountQuotaRefreshEvidenceAtMs = (row: AccountRow): number =>
  row.quota.failedAtMs ?? row.quota.fetchedAtMs ?? 0;

export const classifyAccountQuotaRefreshEvidence = (
  row: AccountRow
): AccountRequestEvidenceKind | 'none' => {
  const error = normalizeText(row.quota.error);
  const statusCode = readStatusCode(row.quota.errorStatus) ?? readStatusCodeFromText(error);
  if (row.quota.status !== 'error' && !error && statusCode === null) return 'none';
  return classifyAccountRequestEvidence({
    timestamp_ms: getAccountQuotaRefreshEvidenceAtMs(row),
    failed: true,
    fail_status_code: statusCode,
    fail_summary: error,
  });
};

const toComparableRequestEvidenceKind = (
  kind: AccountCredentialStatusEvidenceKind | AccountRequestEvidenceKind
): Extract<AccountRequestHealthEvidence['kind'], 'success' | 'credential_failure'> | null => {
  if (kind === 'healthy' || kind === 'success' || kind === 'quota') return 'success';
  if (kind === 'credential_failure') return kind;
  return null;
};

const getConfirmedQuotaEvidenceObservedAtMs = (row: AccountRow): number => {
  if (
    row.quota.status !== 'ok' &&
    row.quota.status !== 'low' &&
    row.quota.status !== 'exhausted'
  ) {
    return 0;
  }
  return Math.max(row.quota.fetchedAtMs ?? 0, row.quota.observedQuotaAtMs ?? 0);
};

const getPositiveQuotaRefreshEvidenceObservedAtMs = (row: AccountRow): number =>
  classifyAccountQuotaRefreshEvidence(row) === 'quota'
    ? getAccountQuotaRefreshEvidenceAtMs(row)
    : 0;

const getPositiveObservedDiagnosticAtMs = (row: AccountRow): number =>
  classifyAccountObservedDiagnosticEvidence(row) === 'quota' ? (row.quota.observedAtMs ?? 0) : 0;

export const isAccountInspectionAuthenticationFailure = (row: AccountRow): boolean => {
  const inspection = row.inspection;
  if (!inspection) return false;
  if (inspection.statusCode === 499) return false;
  const action = normalizeText(inspection.action);
  const errorKind = normalizeText(inspection.errorKind);
  return (
    action === 'reauth' ||
    inspection.statusCode === 401 ||
    ['auth', 'oauth', 'authentication', 'authorization'].includes(errorKind) ||
    (errorKind !== '' && isCredentialFailureText(errorKind))
  );
};

const isInspectionQuotaCredentialEvidence = (row: AccountRow): boolean => {
  const inspection = row.inspection;
  return Boolean(
    inspection &&
      (inspection.isQuota === true || inspection.statusCode === 402 || inspection.statusCode === 429)
  );
};

export const isAccountInspectionHealthyEvidence = (row: AccountRow): boolean => {
  const inspection = row.inspection;
  if (
    !inspection ||
    (row.authenticationAtMs > 0 && inspection.createdAtMs < row.authenticationAtMs) ||
    isAccountInspectionAuthenticationFailure(row) ||
    isInspectionQuotaCredentialEvidence(row)
  ) {
    return false;
  }
  const action = normalizeText(inspection.action);
  const errorKind = normalizeText(inspection.errorKind);
  return (
    (inspection.statusCode !== null &&
      inspection.statusCode >= 200 &&
      inspection.statusCode < 400) ||
    action === 'enable' ||
    /(?:^|_)healthy$/.test(errorKind)
  );
};

const getInspectionCredentialEvidenceKind = (
  row: AccountRow
): Extract<AccountRequestHealthEvidence['kind'], 'success' | 'credential_failure'> | null => {
  if (isAccountInspectionAuthenticationFailure(row)) return 'credential_failure';
  if (isInspectionQuotaCredentialEvidence(row) || isAccountInspectionHealthyEvidence(row)) {
    return 'success';
  }
  return null;
};

const getPositiveInspectionObservedAtMs = (row: AccountRow): number => {
  const inspection = row.inspection;
  return inspection && getInspectionCredentialEvidenceKind(row) === 'success'
    ? inspection.createdAtMs
    : 0;
};

const getConflictingInspectionCredentialStateObservedAtMs = (
  row: AccountRow,
  evidence: Pick<AccountRequestHealthEvidence, 'kind' | 'direction' | 'request'>
): number => {
  const inspectionEvidenceKind = getInspectionCredentialEvidenceKind(row);
  const inspectionDirection =
    inspectionEvidenceKind === 'success'
      ? 'positive'
      : inspectionEvidenceKind === 'credential_failure'
        ? 'negative'
        : null;
  return inspectionDirection !== null && inspectionDirection !== evidence.direction
    ? (row.inspection?.createdAtMs ?? 0)
    : 0;
};

const hasUnknownConflictingInspectionCredentialState = (
  row: AccountRow,
  evidence: Pick<AccountRequestHealthEvidence, 'kind' | 'direction' | 'request'>
): boolean => {
  const comparableEvidenceKind = toComparableRequestEvidenceKind(evidence.kind);
  const inspectionEvidenceKind = getInspectionCredentialEvidenceKind(row);
  return (
    comparableEvidenceKind === 'success' &&
    inspectionEvidenceKind === 'credential_failure' &&
    (row.inspection?.createdAtMs ?? 0) <= 0
  );
};

const getConflictingCredentialStateObservedAtMs = (
  row: AccountRow,
  evidence: Pick<AccountRequestHealthEvidence, 'kind' | 'direction' | 'request'>
): number => {
  const comparableEvidenceKind = toComparableRequestEvidenceKind(evidence.kind);
  const credentialStatusKind = toComparableRequestEvidenceKind(
    classifyAccountCredentialStatusEvidence(row)
  );
  const credentialStateAtMs =
    comparableEvidenceKind !== null &&
    credentialStatusKind !== null &&
    credentialStatusKind !== comparableEvidenceKind
      ? (row.updatedAtMs ?? 0)
      : 0;
  const observedStatusKind = toComparableRequestEvidenceKind(
    classifyAccountObservedDiagnosticEvidence(row)
  );
  const observedDiagnosticAtMs =
    comparableEvidenceKind !== null &&
    observedStatusKind !== null &&
    observedStatusKind !== comparableEvidenceKind
      ? (row.quota.observedAtMs ?? 0)
      : 0;
  const quotaRefreshKind = toComparableRequestEvidenceKind(
    classifyAccountQuotaRefreshEvidence(row)
  );
  const quotaRefreshAtMs =
    comparableEvidenceKind !== null &&
    quotaRefreshKind !== null &&
    quotaRefreshKind !== comparableEvidenceKind
      ? getAccountQuotaRefreshEvidenceAtMs(row)
      : 0;
  const healthyProviderQuotaAtMs =
    evidence.direction === 'negative' ? getConfirmedQuotaEvidenceObservedAtMs(row) : 0;
  return Math.max(
    credentialStateAtMs,
    observedDiagnosticAtMs,
    quotaRefreshAtMs,
    healthyProviderQuotaAtMs
  );
};

const hasUnknownConflictingCredentialState = (
  row: AccountRow,
  evidence: Pick<AccountRequestHealthEvidence, 'kind' | 'direction' | 'request'>
): boolean => {
  const comparableEvidenceKind = toComparableRequestEvidenceKind(evidence.kind);
  if (comparableEvidenceKind === null) return false;

  const credentialStatusKind = toComparableRequestEvidenceKind(
    classifyAccountCredentialStatusEvidence(row)
  );
  if (
    comparableEvidenceKind === 'success' &&
    credentialStatusKind === 'credential_failure' &&
    (row.updatedAtMs ?? 0) <= 0
  ) {
    return true;
  }

  const observedStatusKind = toComparableRequestEvidenceKind(
    classifyAccountObservedDiagnosticEvidence(row)
  );
  if (
    comparableEvidenceKind === 'success' &&
    observedStatusKind === 'credential_failure' &&
    (row.quota.observedAtMs ?? 0) <= 0
  ) {
    return true;
  }

  const quotaRefreshKind = toComparableRequestEvidenceKind(
    classifyAccountQuotaRefreshEvidence(row)
  );
  return (
    comparableEvidenceKind === 'success' &&
    quotaRefreshKind === 'credential_failure' &&
    getAccountQuotaRefreshEvidenceAtMs(row) <= 0
  );
};

export const classifyAccountRequestEvidence = (
  request: MonitoringAccountLatestRequest
): AccountRequestEvidenceKind => {
  if (!request.failed) return 'success';

  const statusCode = request.fail_status_code ?? null;
  const failureText = getRequestFailureText(request);
  if (statusCode === 499) return 'neutral';
  if (statusCode === 401) return 'credential_failure';
  if (statusCode === 429 || statusCode === 402) {
    return 'quota';
  }
  if (isCredentialFailureText(failureText)) return 'credential_failure';
  if (isQuotaFailureText(failureText)) return 'quota';
  if (
    (statusCode !== null && NEUTRAL_HTTP_STATUS_CODES.has(statusCode)) ||
    isNeutralTransportFailureText(failureText) ||
    isRequestFailureText(failureText)
  ) {
    return 'neutral';
  }
  if (statusCode === 408 || (statusCode !== null && statusCode >= 500)) {
    return 'transient_failure';
  }
  if (statusCode !== null && statusCode >= 400 && statusCode < 500) {
    return 'neutral';
  }
  return 'transient_failure';
};

const requestAliasKey = (request: MonitoringAccountLatestRequest): string =>
  [request.timestamp_ms, request.failed ? 1 : 0, request.fail_status_code ?? ''].join('\u001f');

const requestRichness = (request: MonitoringAccountLatestRequest): [number, number] => {
  const details = [
    request.fail_summary,
    request.header_error_kind,
    request.header_error_code,
    request.header_trace_id,
  ]
    .map((value) => value?.trim() ?? '')
    .filter(Boolean);
  return [details.length, details.reduce((total, value) => total + value.length, 0)];
};

const isRicherRequestEvidence = (
  candidate: MonitoringAccountLatestRequest,
  current: MonitoringAccountLatestRequest
): boolean => {
  const [candidateFieldCount, candidateLength] = requestRichness(candidate);
  const [currentFieldCount, currentLength] = requestRichness(current);
  return (
    candidateFieldCount > currentFieldCount ||
    (candidateFieldCount === currentFieldCount && candidateLength > currentLength)
  );
};

export const orderAccountRequestEvidence = (
  input: AccountRequestEvidenceInput = {}
): MonitoringAccountLatestRequest[] => {
  const recentRequests = [...(input.recentRequests ?? [])];
  const latestRequest = input.latestRequest ?? null;
  const latestAliasIndex = latestRequest
    ? recentRequests.findIndex(
        (request) => requestAliasKey(request) === requestAliasKey(latestRequest)
      )
    : -1;
  if (
    latestRequest &&
    latestAliasIndex >= 0 &&
    isRicherRequestEvidence(latestRequest, recentRequests[latestAliasIndex])
  ) {
    recentRequests[latestAliasIndex] = latestRequest;
  }
  const orderedInputs =
    latestRequest && latestAliasIndex < 0 ? [latestRequest, ...recentRequests] : recentRequests;
  return orderedInputs
    .map((request, index) => ({ request, index }))
    .sort(
      (left, right) =>
        right.request.timestamp_ms - left.request.timestamp_ms || left.index - right.index
    )
    .map(({ request }) => request);
};

export const mergeAccountRequestEvidenceInputs = (
  inputs: readonly AccountRequestEvidenceInput[]
): AccountRequestEvidenceInput => {
  const mergedOccurrencesByAlias = new Map<string, MonitoringAccountLatestRequest[]>();

  for (const input of inputs) {
    const sourceOccurrencesByAlias = new Map<string, number>();
    for (const request of orderAccountRequestEvidence(input)) {
      const alias = requestAliasKey(request);
      const occurrenceIndex = sourceOccurrencesByAlias.get(alias) ?? 0;
      sourceOccurrencesByAlias.set(alias, occurrenceIndex + 1);

      const mergedOccurrences = mergedOccurrencesByAlias.get(alias) ?? [];
      const current = mergedOccurrences[occurrenceIndex];
      if (!current) {
        mergedOccurrences[occurrenceIndex] = request;
      } else if (isRicherRequestEvidence(request, current)) {
        mergedOccurrences[occurrenceIndex] = request;
      }
      mergedOccurrencesByAlias.set(alias, mergedOccurrences);
    }
  }

  return { recentRequests: Array.from(mergedOccurrencesByAlias.values()).flat() };
};

const resolveAccountRequestCredentialEvidenceFromOrderedRequests = (
  requests: readonly MonitoringAccountLatestRequest[]
): AccountRequestCredentialEvidence | null => {
  for (const request of requests) {
    const kind = classifyAccountRequestEvidence(request);
    if (kind === 'success' || kind === 'quota') {
      return { kind, direction: 'positive', request };
    }
    if (kind === 'credential_failure') {
      return { kind, direction: 'negative', request };
    }
  }
  return null;
};

export const resolveAccountRequestHealthEvidence = (
  input: AccountRequestEvidenceInput = {}
): AccountRequestHealthEvidence | null => {
  const orderedRequests = orderAccountRequestEvidence(input);
  const credentialEvidence =
    resolveAccountRequestCredentialEvidenceFromOrderedRequests(orderedRequests);
  let latestTransientFailure: MonitoringAccountLatestRequest | null = null;
  let consecutiveTransientFailures = 0;

  for (const request of orderedRequests) {
    const kind = classifyAccountRequestEvidence(request);
    if (kind === 'neutral') {
      continue;
    }
    if (kind === 'quota') {
      return {
        kind,
        direction: 'positive',
        request,
        consecutiveTransientFailures: 0,
        credentialEvidence,
      };
    }
    if (kind === 'success') {
      return {
        kind,
        direction: 'positive',
        request,
        consecutiveTransientFailures: 0,
        credentialEvidence,
      };
    }
    if (kind === 'credential_failure') {
      return {
        kind,
        direction: 'negative',
        request,
        consecutiveTransientFailures: 0,
        credentialEvidence,
      };
    }

    latestTransientFailure ??= request;
    consecutiveTransientFailures += 1;
    if (consecutiveTransientFailures >= TRANSIENT_FAILURE_THRESHOLD) {
      return {
        kind,
        direction: 'negative',
        request: latestTransientFailure,
        consecutiveTransientFailures,
        credentialEvidence,
      };
    }
  }

  return null;
};

export const getAccountRequestCredentialEvidence = (
  evidence: AccountRequestHealthEvidence | null
): AccountRequestCredentialEvidence | null => evidence?.credentialEvidence ?? null;

const getPositiveRequestCredentialEvidenceAtMs = (
  evidence: AccountRequestHealthEvidence | null
): number => {
  const credentialEvidence = getAccountRequestCredentialEvidence(evidence);
  return credentialEvidence?.direction === 'positive'
    ? credentialEvidence.request.timestamp_ms
    : 0;
};

const getPositiveRequestQuotaRecoveryEvidenceAtMs = (
  evidence: AccountRequestHealthEvidence | null
): number => {
  const credentialEvidence = getAccountRequestCredentialEvidence(evidence);
  return credentialEvidence?.kind === 'success' ? credentialEvidence.request.timestamp_ms : 0;
};

const getPositiveInspectionQuotaRecoveryObservedAtMs = (row: AccountRow): number => {
  const inspection = row.inspection;
  return inspection && isAccountInspectionHealthyEvidence(row) ? inspection.createdAtMs : 0;
};

export const resolveAccountRequestQuotaEvidence = (
  input: AccountRequestEvidenceInput = {}
): AccountRequestQuotaEvidence | null => {
  const quotaRequest = orderAccountRequestEvidence(input).find(
    (request) => classifyAccountRequestEvidence(request) === 'quota'
  );
  if (!quotaRequest) return null;

  const healthEvidence = resolveAccountRequestHealthEvidence(input);
  if (
    healthEvidence &&
    healthEvidence.request !== quotaRequest &&
    healthEvidence.request.timestamp_ms > quotaRequest.timestamp_ms
  ) {
    return null;
  }
  return { kind: 'quota', request: quotaRequest };
};

export const getAccountRequestEvidenceDetail = (
  evidence: Pick<AccountRequestHealthEvidence, 'kind' | 'direction' | 'request'>
): string => {
  const request = evidence.request;
  return (
    request.fail_summary?.trim() ||
    [request.header_error_kind, request.header_error_code].filter(Boolean).join(' / ') ||
    (request.fail_status_code ? `HTTP ${request.fail_status_code}` : '') ||
    (evidence.kind === 'quota'
      ? 'Quota limited'
      : evidence.direction === 'positive'
        ? 'HTTP success'
        : 'Request failed')
  );
};

export const getAccountRequestQuotaEvidenceDetail = (
  evidence: AccountRequestQuotaEvidence
): string => {
  const request = evidence.request;
  return (
    request.fail_summary?.trim() ||
    [request.header_error_kind, request.header_error_code].filter(Boolean).join(' / ') ||
    (request.fail_status_code ? `HTTP ${request.fail_status_code}` : '') ||
    'Quota limited'
  );
};

export const isAccountObservedDiagnosticProblemCurrent = (
  row: AccountRow,
  requestEvidence: AccountRequestHealthEvidence | null = null
): boolean => {
  const observedKind = classifyAccountObservedDiagnosticEvidence(row);
  if (observedKind === 'none') return false;
  if (observedKind === 'quota') return true;
  if (observedKind === 'neutral' || observedKind === 'transient_failure') return false;

  const observedAtMs = row.quota.observedAtMs ?? 0;
  if (observedAtMs <= 0) return true;
  const newerPositiveEvidenceAtMs = Math.max(
    getPositiveRequestCredentialEvidenceAtMs(requestEvidence),
    getConfirmedQuotaEvidenceObservedAtMs(row),
    getPositiveQuotaRefreshEvidenceObservedAtMs(row),
    getPositiveInspectionObservedAtMs(row)
  );
  return newerPositiveEvidenceAtMs <= observedAtMs;
};

export const classifyAccountObservedDiagnosticEvidence = (
  row: AccountRow
): AccountObservedDiagnosticEvidenceKind => {
  const observedErrorKind = normalizeText(row.quota.observedErrorKind);
  const observedErrorCode = normalizeText(row.quota.observedErrorCode);
  if (!observedErrorKind && !observedErrorCode) return 'none';
  const kind = classifyAccountRequestEvidence({
    timestamp_ms: row.quota.observedAtMs ?? 0,
    failed: true,
    header_error_kind: observedErrorKind,
    header_error_code: observedErrorCode,
  });
  return kind === 'success' ? 'none' : kind;
};

export const isAccountRequestQuotaEvidenceCurrent = (
  row: AccountRow,
  evidence: AccountRequestQuotaEvidence | null
): boolean => {
  if (!evidence) return false;
  const newerConfirmedQuotaAtMs = getConfirmedQuotaEvidenceObservedAtMs(row);
  return evidence.request.timestamp_ms > newerConfirmedQuotaAtMs;
};

const isAccountQuotaRefreshEvidenceSuperseded = (
  row: AccountRow,
  requestEvidence: AccountRequestHealthEvidence | null
): boolean => {
  const refreshAtMs = getAccountQuotaRefreshEvidenceAtMs(row);
  if (refreshAtMs <= 0) return false;
  const newerPositiveEvidenceAtMs = Math.max(
    getConfirmedQuotaEvidenceObservedAtMs(row),
    getPositiveObservedDiagnosticAtMs(row),
    getPositiveInspectionObservedAtMs(row),
    getPositiveRequestCredentialEvidenceAtMs(requestEvidence)
  );
  return newerPositiveEvidenceAtMs > refreshAtMs;
};

export const isAccountQuotaRefreshProblemCurrent = (
  row: AccountRow,
  requestEvidence: AccountRequestHealthEvidence | null = null
): boolean => {
  const kind = classifyAccountQuotaRefreshEvidence(row);
  if (kind !== 'credential_failure' && kind !== 'transient_failure') return false;
  return !isAccountQuotaRefreshEvidenceSuperseded(row, requestEvidence);
};

const isAccountQuotaRefreshLimitCurrent = (
  row: AccountRow,
  requestEvidence: AccountRequestHealthEvidence | null
): boolean =>
  classifyAccountQuotaRefreshEvidence(row) === 'quota' &&
  !isAccountQuotaRefreshEvidenceSuperseded(row, requestEvidence);

export const hasAccountQuotaLimitEvidence = (
  row: AccountRow,
  requestEvidenceInput: AccountRequestEvidenceInput = {}
): boolean => {
  const requestHealthEvidence = resolveAccountRequestHealthEvidence(requestEvidenceInput);
  return (
    isAccountCredentialQuotaLimitCurrent(row, requestHealthEvidence) ||
    classifyAccountObservedDiagnosticEvidence(row) === 'quota' ||
    isAccountQuotaRefreshLimitCurrent(row, requestHealthEvidence) ||
    Boolean(row.quota.rateLimitReachedType?.trim()) ||
    row.quota.spendControlReached === true ||
    row.quota.creditsOverageLimitReached === true ||
    isAccountRequestQuotaEvidenceCurrent(
      row,
      resolveAccountRequestQuotaEvidence(requestEvidenceInput)
    )
  );
};

export const isAccountRequestHealthEvidenceCurrent = (
  row: AccountRow,
  evidence: AccountRequestHealthEvidence | null
): boolean => {
  if (!evidence) return false;
  if (
    hasUnknownConflictingCredentialState(row, evidence) ||
    hasUnknownConflictingInspectionCredentialState(row, evidence)
  ) {
    return false;
  }
  if (
    (evidence.kind === 'success' || evidence.kind === 'credential_failure') &&
    evidence.request.timestamp_ms <= row.authenticationAtMs
  ) {
    return false;
  }
  return (
    evidence.request.timestamp_ms >
    Math.max(
      getConflictingCredentialStateObservedAtMs(row, evidence),
      getConflictingInspectionCredentialStateObservedAtMs(row, evidence)
    )
  );
};

export const isAccountRequestCredentialEvidenceCurrent = (
  row: AccountRow,
  evidence: AccountRequestHealthEvidence | null
): boolean => {
  const credentialEvidence = getAccountRequestCredentialEvidence(evidence);
  if (
    !credentialEvidence ||
    hasUnknownConflictingCredentialState(row, credentialEvidence) ||
    hasUnknownConflictingInspectionCredentialState(row, credentialEvidence)
  ) {
    return false;
  }
  if (
    (credentialEvidence.kind === 'success' || credentialEvidence.kind === 'credential_failure') &&
    credentialEvidence.request.timestamp_ms <= row.authenticationAtMs
  ) {
    return false;
  }
  return (
    credentialEvidence.request.timestamp_ms >
    Math.max(
      getConflictingCredentialStateObservedAtMs(row, credentialEvidence),
      getConflictingInspectionCredentialStateObservedAtMs(row, credentialEvidence)
    )
  );
};

export const isAccountInspectionActionable = (
  row: AccountRow,
  requestEvidence: AccountRequestHealthEvidence | null = null
): boolean => {
  const inspection = row.inspection;
  if (!inspection) return false;
  if (inspection.statusCode === 499) return false;
  const action = normalizeText(inspection.action);
  if (!action || action === 'keep') return false;

  const actionStatus = normalizeText(inspection.actionStatus);
  if (RESOLVED_INSPECTION_ACTION_STATUSES.has(actionStatus)) return false;
  if (action === 'enable' && !row.disabled) return false;
  if (action === 'disable' && row.disabled) return false;
  if (
    inspection.isQuota !== true &&
    Math.max(
      getConfirmedQuotaEvidenceObservedAtMs(row),
      getPositiveQuotaRefreshEvidenceObservedAtMs(row),
      getPositiveObservedDiagnosticAtMs(row)
    ) > inspection.createdAtMs
  ) {
    return false;
  }
  if (
    inspection.isQuota !== true &&
    getAccountRequestCredentialEvidence(requestEvidence)?.direction === 'positive' &&
    isAccountRequestCredentialEvidenceCurrent(row, requestEvidence)
  ) {
    return false;
  }

  if (UNRESOLVED_INSPECTION_ACTION_STATUSES.has(actionStatus)) return true;
  // Older local/server snapshots may not have persisted an action status. Keep
  // those recommendations actionable until newer qualified evidence replaces them.
  return !actionStatus || actionStatus === 'none';
};

export const isAccountInspectionStatusEvidenceCurrent = (
  row: AccountRow,
  requestEvidence: AccountRequestHealthEvidence | null = null
): boolean => {
  const inspection = row.inspection;
  if (!inspection) return false;
  if (inspection.statusCode === 499) return false;
  const action = normalizeText(inspection.action);
  const authenticationFailure = isAccountInspectionAuthenticationFailure(row);
  if (authenticationFailure && action && action !== 'keep') {
    return isAccountInspectionActionable(row, requestEvidence);
  }
  if (authenticationFailure) {
    if (inspection.createdAtMs <= 0) return true;
    const newerPositiveEvidenceAtMs = Math.max(
      getConfirmedQuotaEvidenceObservedAtMs(row),
      getPositiveQuotaRefreshEvidenceObservedAtMs(row),
      getPositiveObservedDiagnosticAtMs(row),
      getPositiveRequestCredentialEvidenceAtMs(requestEvidence)
    );
    return newerPositiveEvidenceAtMs <= inspection.createdAtMs;
  }
  if (inspection.isQuota === true || action === 'keep' || action === 'enable') return true;
  return isAccountInspectionActionable(row, requestEvidence);
};

export const isAccountCredentialStatusProblemCurrent = (
  row: AccountRow,
  requestEvidence: AccountRequestHealthEvidence | null = null
): boolean => {
  const credentialStatusKind = classifyAccountCredentialStatusEvidence(row);
  if (credentialStatusKind !== 'credential_failure') return false;
  const credentialStatusAtMs = row.updatedAtMs ?? 0;
  if (credentialStatusAtMs <= 0) return true;
  const newerPositiveEvidenceAtMs = Math.max(
    getPositiveRequestCredentialEvidenceAtMs(requestEvidence),
    getConfirmedQuotaEvidenceObservedAtMs(row),
    getPositiveQuotaRefreshEvidenceObservedAtMs(row),
    getPositiveObservedDiagnosticAtMs(row),
    getPositiveInspectionObservedAtMs(row)
  );
  return newerPositiveEvidenceAtMs <= credentialStatusAtMs;
};

export const isAccountCredentialQuotaLimitCurrent = (
  row: AccountRow,
  requestEvidence: AccountRequestHealthEvidence | null = null
): boolean => {
  if (classifyAccountCredentialStatusEvidence(row) !== 'quota') return false;
  const credentialStatusAtMs = row.updatedAtMs ?? 0;
  if (credentialStatusAtMs <= 0) return true;
  const newerQuotaRecoveryAtMs = Math.max(
    getPositiveRequestQuotaRecoveryEvidenceAtMs(requestEvidence),
    getConfirmedQuotaEvidenceObservedAtMs(row),
    getPositiveInspectionQuotaRecoveryObservedAtMs(row)
  );
  return newerQuotaRecoveryAtMs <= credentialStatusAtMs;
};

const AUTHENTICATION_PROBLEM_SOURCE_PRIORITY: Record<
  AccountAuthenticationProblemSource,
  number
> = {
  request: 5,
  observed_header: 4,
  quota_refresh: 3,
  inspection: 2,
  credential_status: 1,
};

const normalizeEvidenceTimestamp = (value: number | null | undefined): number | null =>
  typeof value === 'number' && Number.isFinite(value) && value > 0 ? value : null;

export const resolveAccountAuthenticationProblemEvidence = (
  row: AccountRow,
  requestEvidence: AccountRequestHealthEvidence | null = null
): AccountAuthenticationProblemEvidence | null => {
  const candidates: AccountAuthenticationProblemEvidence[] = [];
  const requestCredentialEvidence = getAccountRequestCredentialEvidence(requestEvidence);
  if (
    requestCredentialEvidence?.kind === 'credential_failure' &&
    isAccountRequestCredentialEvidenceCurrent(row, requestEvidence)
  ) {
    candidates.push({
      source: 'request',
      observedAtMs: normalizeEvidenceTimestamp(requestCredentialEvidence.request.timestamp_ms),
      statusCode: requestCredentialEvidence.request.fail_status_code ?? null,
    });
  }

  if (
    classifyAccountQuotaRefreshEvidence(row) === 'credential_failure' &&
    isAccountQuotaRefreshProblemCurrent(row, requestEvidence)
  ) {
    candidates.push({
      source: 'quota_refresh',
      observedAtMs: normalizeEvidenceTimestamp(getAccountQuotaRefreshEvidenceAtMs(row)),
      statusCode:
        readStatusCode(row.quota.errorStatus) ?? readStatusCodeFromText(row.quota.error ?? ''),
    });
  }

  const inspection = row.inspection;
  if (
    inspection &&
    isAccountInspectionAuthenticationFailure(row) &&
    isAccountInspectionStatusEvidenceCurrent(row, requestEvidence)
  ) {
    candidates.push({
      source: 'inspection',
      observedAtMs: normalizeEvidenceTimestamp(inspection.createdAtMs),
      statusCode: inspection.statusCode ?? null,
    });
  }

  if (
    classifyAccountObservedDiagnosticEvidence(row) === 'credential_failure' &&
    isAccountObservedDiagnosticProblemCurrent(row, requestEvidence)
  ) {
    candidates.push({
      source: 'observed_header',
      observedAtMs: normalizeEvidenceTimestamp(row.quota.observedAtMs),
      statusCode: readStatusCodeFromText(
        `${row.quota.observedErrorKind ?? ''} ${row.quota.observedErrorCode ?? ''}`
      ),
    });
  }

  if (isAccountCredentialStatusProblemCurrent(row, requestEvidence)) {
    candidates.push({
      source: 'credential_status',
      observedAtMs: normalizeEvidenceTimestamp(row.updatedAtMs),
      statusCode: getRawCredentialStatusCode(row),
    });
  }

  if (candidates.length === 0) return null;
  return candidates.sort((left, right) => {
    const leftUnknown = left.observedAtMs === null;
    const rightUnknown = right.observedAtMs === null;
    if (leftUnknown !== rightUnknown) return leftUnknown ? 1 : -1;
    const observedAtDiff = (right.observedAtMs ?? 0) - (left.observedAtMs ?? 0);
    if (observedAtDiff !== 0) return observedAtDiff;
    return (
      AUTHENTICATION_PROBLEM_SOURCE_PRIORITY[right.source] -
      AUTHENTICATION_PROBLEM_SOURCE_PRIORITY[left.source]
    );
  })[0];
};

const EXCEPTION_PROBLEM_SOURCE_PRIORITY: Record<AccountExceptionProblemSource, number> = {
  request: 3,
  quota_refresh: 2,
  inspection: 1,
};

export const resolveAccountExceptionProblemEvidence = (
  row: AccountRow,
  requestEvidence: AccountRequestHealthEvidence | null = null
): AccountExceptionProblemEvidence | null => {
  const candidates: AccountExceptionProblemEvidence[] = [];

  if (
    requestEvidence?.kind === 'transient_failure' &&
    isAccountRequestHealthEvidenceCurrent(row, requestEvidence)
  ) {
    candidates.push({
      source: 'request',
      observedAtMs: normalizeEvidenceTimestamp(requestEvidence.request.timestamp_ms),
    });
  }

  if (
    classifyAccountQuotaRefreshEvidence(row) === 'transient_failure' &&
    isAccountQuotaRefreshProblemCurrent(row, requestEvidence)
  ) {
    candidates.push({
      source: 'quota_refresh',
      observedAtMs: normalizeEvidenceTimestamp(getAccountQuotaRefreshEvidenceAtMs(row)),
    });
  }

  const inspection = row.inspection;
  const inspectionAction = normalizeText(inspection?.action);
  if (
    inspection &&
    inspectionAction !== 'reauth' &&
    inspection.statusCode !== 401 &&
    isAccountInspectionActionable(row, requestEvidence)
  ) {
    candidates.push({
      source: 'inspection',
      observedAtMs: normalizeEvidenceTimestamp(inspection.createdAtMs),
    });
  }

  if (candidates.length === 0) return null;
  return candidates.sort((left, right) => {
    const leftUnknown = left.observedAtMs === null;
    const rightUnknown = right.observedAtMs === null;
    if (leftUnknown !== rightUnknown) return leftUnknown ? 1 : -1;
    const observedAtDiff = (right.observedAtMs ?? 0) - (left.observedAtMs ?? 0);
    if (observedAtDiff !== 0) return observedAtDiff;
    return (
      EXCEPTION_PROBLEM_SOURCE_PRIORITY[right.source] -
      EXCEPTION_PROBLEM_SOURCE_PRIORITY[left.source]
    );
  })[0];
};
