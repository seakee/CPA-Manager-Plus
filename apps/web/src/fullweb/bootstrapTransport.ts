// Bounded pre-canonical carrier. It has no CPA/Admin transport and persists no
// secrets. This module belongs to the existing Full Web entry graph.
import { validControlBasePath } from './routingContext';
import { ADOPT_SOURCE_CLASS, parseAdoptionSummary, type AdoptionSummary } from './freshAdoption';
import {
  parseRuntimeJSON,
  runtimeRequestJSON,
  validRuntimeGeneration,
  type RuntimeGeneration,
} from './runtimeGeneration';
export const BOOTSTRAP_ROOT = '/.well-known/cpamp-bootstrap';
export const BOOTSTRAP_UI = `${BOOTSTRAP_ROOT}/ui`;
const API = `${BOOTSTRAP_ROOT}/api/v2/`;

// The completion refusals an adoption's operator can act on. A code outside this
// set never reaches the UI: the completion screen shows a message for a cause it
// knows, and a plain failure otherwise.
const COMPLETE_REFUSALS: Record<string, true> = {
  adopt_migration_running: true,
  adopt_migration_incomplete: true,
  adopt_migration_retryable: true,
  adopt_destination_unproven: true,
};

export interface FreshIdentity {
  admissionId: string;
  installationId: string;
  generation: string;
  databasePath: string;
  operationId: string;
}

export interface FreshRoutingCheckpoint {
  operationId?: string;
  revision?: number;
  target?: string;
  stage?: string;
  committed: boolean;
}

export interface FreshStrategyCheckpoint {
  strategy?: string;
  targetIdentity?: string;
  targetArtifactId?: string;
  targetGeneration?: RuntimeGeneration;
  stage?: string;
  /** The persisted plugin opt-in of a New Managed install; absent means off. */
  pluginsEnabled?: boolean;
}

export interface FreshOperation {
  version?: number;
  identity: FreshIdentity;
  revision: number;
  adminInitialized: boolean;
  productComplete?: boolean;
  gatewayPending: boolean;
  routing: FreshRoutingCheckpoint;
  strategy?: FreshStrategyCheckpoint;
}

export interface Handoff {
  identity: FreshIdentity;
  claimGeneration: number;
  token: string;
  resume: boolean;
}

export interface AdminCommitPayload {
  identity: FreshIdentity;
  commitId: string;
  key: string;
}

export type BootstrapErrorKind =
  | 'authority'
  | 'transport'
  | 'rejected'
  | 'bad_request'
  | 'conflict'
  | 'not_found'
  | 'unavailable'
  | 'response'
  | 'mismatch';

export class BootstrapTransportError extends Error {
  readonly kind: BootstrapErrorKind;
  readonly status?: number;
  // Server-owned refusal code of the bounded blocker envelope ({ "Code": "…" }).
  // Absent when the refusal carried none, which keeps a lost reply and a typed
  // refusal distinguishable.
  readonly code?: string;
  // The bounded capability list a strategy blocker names
  // ({ "Code": "…", "Unsupported": ["…"] }): what the refusal says is not
  // supported, never free text.
  readonly unsupported?: readonly string[];
  // The item count of an `adopt_migration_retryable` completion refusal, which
  // is the number the operator decides over.
  readonly retryable?: number;

  constructor(
    message: string,
    kind: BootstrapErrorKind,
    status?: number,
    code?: string,
    unsupported?: readonly string[],
    retryable?: number
  ) {
    super(message);
    this.name = 'BootstrapTransportError';
    this.kind = kind;
    this.status = status;
    this.code = code;
    this.unsupported = unsupported;
    this.retryable = retryable;
  }
}

export interface AdminKeyValidationResult {
  valid: boolean;
  byteLength: number;
  distinctCount: number;
  classCount: number;
  hasForbiddenChar: boolean;
  reason: string;
}

// Mirrors apps/manager-server/internal/security/fresh_admin.go ValidateFreshAdminKey.
export function validateFreshAdminKey(key: string): AdminKeyValidationResult {
  const byteLength = new TextEncoder().encode(key).length;
  const trimmedMatches = key.trim() === key;
  let hasForbiddenChar = !trimmedMatches;
  const classes = new Set<number>();
  const unique = new Set<string>();

  for (const ch of key) {
    if (/[\s\p{Cc}]/u.test(ch)) {
      hasForbiddenChar = true;
    }
    unique.add(ch);
    if (/\p{Ll}/u.test(ch)) {
      classes.add(0);
    } else if (/\p{Lu}/u.test(ch)) {
      classes.add(1);
    } else if (/\p{Nd}/u.test(ch)) {
      classes.add(2);
    } else {
      classes.add(3);
    }
  }

  const classCount = classes.size;
  const distinctCount = unique.size;

  if (!key) {
    return {
      valid: false,
      byteLength,
      distinctCount,
      classCount,
      hasForbiddenChar,
      reason: '可手动输入高强度管理员密钥，或点击“一键生成”由服务端生成。',
    };
  }
  if (hasForbiddenChar) {
    return {
      valid: false,
      byteLength,
      distinctCount,
      classCount,
      hasForbiddenChar,
      reason: '管理员密钥不能包含空格、换行或控制字符。',
    };
  }
  if (byteLength < 24 || byteLength > 256) {
    return {
      valid: false,
      byteLength,
      distinctCount,
      classCount,
      hasForbiddenChar,
      reason: '管理员密钥长度需为 24 至 256 个非空白字符。',
    };
  }
  if (classCount < 3 || distinctCount < 12) {
    return {
      valid: false,
      byteLength,
      distinctCount,
      classCount,
      hasForbiddenChar,
      reason: '需包含大小写字母、数字、符号中的至少 3 类，且不少于 12 个不同字符。',
    };
  }
  return {
    valid: true,
    byteLength,
    distinctCount,
    classCount,
    hasForbiddenChar: false,
    reason: '密钥强度符合 CPAMP 初始化安全要求。',
  };
}

export function sameIdentity(a: FreshIdentity | undefined, b: FreshIdentity | undefined): boolean {
  if (!a || !b) return false;
  return (
    a.admissionId === b.admissionId &&
    a.installationId === b.installationId &&
    a.generation === b.generation &&
    a.databasePath === b.databasePath &&
    a.operationId === b.operationId
  );
}

function classifyStatus(status: number): BootstrapErrorKind {
  if (status === 401 || status === 403) return 'rejected';
  if (status === 400) return 'bad_request';
  if (status === 404) return 'not_found';
  if (status === 409) return 'conflict';
  return 'unavailable';
}

function validOperationState(next: FreshOperation | undefined, previous?: FreshOperation): boolean {
  const checkpoint = next?.strategy;
  const previousCheckpoint = previous?.strategy;
  return (
    !!next &&
    typeof next.adminInitialized === 'boolean' &&
    next.gatewayPending === true &&
    (!next.productComplete || (next.routing.committed && checkpoint?.stage === 'committed')) &&
    (!previous?.productComplete || next.productComplete === true) &&
    (!checkpoint?.strategy ||
      (next.adminInitialized &&
        typeof checkpoint.targetIdentity === 'string' &&
        !!checkpoint.targetIdentity.trim() &&
        (checkpoint.strategy === 'new_managed'
          ? validRuntimeGeneration(checkpoint.targetGeneration) &&
            /^sha256:[a-f0-9]{64}$/.test(checkpoint.targetArtifactId || '') &&
            ['staged', 'starting', 'committed'].includes(checkpoint.stage || '')
          : checkpoint.strategy === 'adopt_existing'
            ? // An adoption has no starting stage: it is staged by the two-POST
              // inventory, or committed once the destination is verified.
              validRuntimeGeneration(checkpoint.targetGeneration) &&
              /^sha256:[a-f0-9]{64}$/.test(checkpoint.targetArtifactId || '') &&
              ['staged', 'committed'].includes(checkpoint.stage || '')
            : checkpoint.strategy === 'connect_external' &&
              checkpoint.targetGeneration === 1 &&
              !checkpoint.targetArtifactId &&
              /^external:sha256:[a-f0-9]{64}$/.test(checkpoint.targetIdentity) &&
              ['validated', 'committed'].includes(checkpoint.stage || '')))) &&
    (!previousCheckpoint?.strategy ||
      (checkpoint?.strategy === previousCheckpoint.strategy &&
        checkpoint.targetIdentity === previousCheckpoint.targetIdentity &&
        checkpoint.targetGeneration === previousCheckpoint.targetGeneration &&
        checkpoint.targetArtifactId === previousCheckpoint.targetArtifactId &&
        (previousCheckpoint.stage !== 'committed' || checkpoint.stage === 'committed'))) &&
    Number.isSafeInteger(next.revision) &&
    next.revision > 0 &&
    (!previous ||
      (next.revision >= previous.revision &&
        (!previous.adminInitialized || next.adminInitialized))) &&
    typeof next.routing?.committed === 'boolean' &&
    (!next.routing.committed ||
      (next.adminInitialized &&
        next.routing.stage === 'committed' &&
        validControlBasePath(next.routing.target) &&
        next.routing.operationId === next.identity.operationId &&
        Number.isSafeInteger(next.routing.revision) &&
        next.routing.revision! > 0))
  );
}

// Remove the fragment before decoding or making a request, including malformed
// handoffs. Errors deliberately contain neither input nor request bodies.
export function consumeHandoff(
  location: Pick<Location, 'hash' | 'pathname' | 'search'>,
  history: Pick<History, 'replaceState'>
): Handoff | null {
  const fragment = location.hash;
  if (fragment) history.replaceState(null, '', location.pathname + location.search);
  if (location.pathname !== BOOTSTRAP_UI || !fragment) return null;
  try {
    const params = new URLSearchParams(fragment.slice(1));
    if (params.size !== 1 || !params.has('handoff')) return null;
    const encoded = params.get('handoff')!;
    if (!/^[A-Za-z0-9_-]{1,8192}$/.test(encoded)) return null;
    const bytes = Uint8Array.from(atob(encoded.replace(/-/g, '+').replace(/_/g, '/')), (c) =>
      c.charCodeAt(0)
    );
    const value = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(bytes)) as Handoff;
    if (
      !value ||
      typeof value !== 'object' ||
      typeof value.resume !== 'boolean' ||
      typeof value.token !== 'string' ||
      value.token.length < 32 ||
      value.token.length > 256 ||
      !Number.isSafeInteger(value.claimGeneration) ||
      value.claimGeneration < 0 ||
      !value.identity ||
      ['admissionId', 'installationId', 'generation', 'databasePath', 'operationId'].some(
        (key) =>
          typeof value.identity[key as keyof FreshIdentity] !== 'string' ||
          !value.identity[key as keyof FreshIdentity]
      )
    )
      return null;
    return value;
  } catch {
    return null;
  }
}

// The exact field set of an adoption strategy body. The carrier adds the
// identity and the revision, so anything else is a body this client never
// builds and the server would refuse.
const ADOPT_BODY_FIELDS: Record<string, true> = {
  strategy: true,
  adoptSourceClass: true,
  baseUrl: true,
  managementKey: true,
  expectedRuntimeIdentity: true,
  expectedRuntimeGeneration: true,
  expectedArtifactId: true,
  pluginsEnabled: true,
};

const methods = {
  'claim/exchange': 'POST',
  operation: 'GET',
  'admin/generate': 'POST',
  'admin/commit': 'POST',
  'routing/prepare': 'POST',
  'routing/activate': 'POST',
  strategy: 'POST',
  'installation/runtime': 'GET',
  'installation/adoption': 'GET',
  complete: 'POST',
} as const;
type Resource = keyof typeof methods;

export function createBootstrapTransport(transport: typeof fetch = globalThis.fetch) {
  let token: string | undefined;
  let operation: FreshOperation | undefined;
  let canonicalBase: string | undefined;
  async function request<T>(resource: Resource, body?: unknown): Promise<T> {
    if (
      window.location.pathname !== BOOTSTRAP_UI ||
      !['http:', 'https:'].includes(window.location.protocol) ||
      !Object.prototype.hasOwnProperty.call(methods, resource) ||
      (canonicalBase &&
        ![
          'operation',
          'strategy',
          'installation/runtime',
          'installation/adoption',
          'complete',
        ].includes(resource)) ||
      (resource !== 'claim/exchange' && !token)
    ) {
      throw new BootstrapTransportError('Bootstrap authority unavailable', 'authority');
    }
    const target = new URL(
      (canonicalBase ? canonicalBase + '/api/v2/' : API) + resource,
      window.location.origin
    );
    const headers: Record<string, string> = { Accept: 'application/json' };
    if (body !== undefined) headers['Content-Type'] = 'application/json';
    if (token) headers.Authorization = `Fresh ${token}`;
    let response: Response;
    try {
      response = await transport(target.href, {
        method: methods[resource],
        headers,
        ...(body === undefined
          ? {}
          : { body: resource === 'strategy' ? runtimeRequestJSON(body) : JSON.stringify(body) }),
        mode: 'same-origin',
        credentials: 'omit',
        redirect: 'error',
        cache: 'no-store',
      });
    } catch {
      throw new BootstrapTransportError('Bootstrap transport unavailable', 'transport');
    }
    if (!response.ok) {
      // Two bounded refusal envelopes exist, and they are read by the call that
      // can act on them:
      //  - a strategy blocker answers `{"Code": "…", "Unsupported": […]}`;
      //  - an adoption completion answers `{"code": "…", "retryable": N}`.
      // Preserving them keeps a typed refusal distinguishable from a reply that
      // was lost after the server may have acted.
      let code: string | undefined;
      let unsupported: readonly string[] | undefined;
      let retryable: number | undefined;
      try {
        const refusal = (await response.json()) as {
          Code?: unknown;
          Unsupported?: unknown;
          code?: unknown;
          retryable?: unknown;
        };
        if (resource === 'complete') {
          if (typeof refusal?.code === 'string' && COMPLETE_REFUSALS[refusal.code] === true)
            code = refusal.code;
          if (code && Number.isSafeInteger(refusal.retryable) && (refusal.retryable as number) > 0)
            retryable = refusal.retryable as number;
        } else {
          if (typeof refusal?.Code === 'string' && refusal.Code) code = refusal.Code;
          if (Array.isArray(refusal?.Unsupported))
            unsupported = refusal.Unsupported.filter(
              (entry): entry is string => typeof entry === 'string' && !!entry && entry.length <= 64
            );
        }
      } catch {
        /* A refusal without a bounded envelope stays a plain status failure. */
      }
      throw new BootstrapTransportError(
        'Bootstrap request unavailable',
        classifyStatus(response.status),
        response.status,
        code,
        unsupported,
        retryable
      );
    }
    try {
      return parseRuntimeJSON(await response.text()) as T;
    } catch {
      throw new BootstrapTransportError(
        'Bootstrap response unavailable',
        'response',
        response.status
      );
    }
  }
  async function verifyCanonical(prepared: FreshOperation): Promise<string> {
    // No Fresh/Admin credential is ever sent to this public verification
    // leaf. Its path is the server-persisted prepare checkpoint, not input.
    let response: Response;
    try {
      response = await transport(
        new URL(prepared.routing.target! + '/api/v2/bootstrap/identity', window.location.origin)
          .href,
        {
          method: 'GET',
          headers: { Accept: 'application/json' },
          mode: 'same-origin',
          credentials: 'omit',
          redirect: 'error',
          cache: 'no-store',
        }
      );
    } catch {
      throw new BootstrapTransportError('Routing verification unavailable', 'transport');
    }
    if (!response.ok)
      throw new BootstrapTransportError(
        'Routing verification unavailable',
        classifyStatus(response.status),
        response.status
      );
    let state: {
      installationId: string;
      generation: string;
      operationId: string;
      revision: number;
      target: string;
      committed: boolean;
    };
    try {
      state = await response.json();
    } catch {
      throw new BootstrapTransportError('Routing verification unavailable', 'response');
    }
    if (
      !state ||
      response.headers.get('X-CPAMP-Route-Owner') !== 'manager' ||
      state.installationId !== prepared.identity.installationId ||
      state.generation !== prepared.identity.generation ||
      state.operationId !== prepared.identity.operationId ||
      state.revision !== prepared.routing.revision ||
      state.target !== prepared.routing.target
    ) {
      throw new BootstrapTransportError('Routing verification incomplete', 'mismatch');
    }
    if (state.committed !== true)
      throw new BootstrapTransportError('Routing verification incomplete', 'mismatch');
    return state.target;
  }
  return {
    async start(handoff: Handoff): Promise<FreshOperation> {
      if (token) {
        throw new BootstrapTransportError('Bootstrap authority already installed', 'authority');
      }
      if (handoff.resume) {
        token = handoff.token;
        try {
          operation = await request<FreshOperation>('operation');
        } catch (err) {
          token = undefined;
          operation = undefined;
          throw err;
        }
      } else {
        const result = await request<{ token: string; operation: FreshOperation }>(
          'claim/exchange',
          {
            identity: handoff.identity,
            claimGeneration: handoff.claimGeneration,
            token: handoff.token,
          }
        );
        token = result.token;
        operation = result.operation;
      }
      if (!operation || !sameIdentity(operation.identity, handoff.identity)) {
        token = undefined;
        operation = undefined;
        throw new BootstrapTransportError('Bootstrap authority mismatch', 'mismatch');
      }
      if (!validOperationState(operation)) {
        token = undefined;
        operation = undefined;
        throw new BootstrapTransportError('Bootstrap response unavailable', 'response');
      }
      return operation;
    },
    async fetchOperation(): Promise<FreshOperation> {
      let next: FreshOperation;
      try {
        next = await request<FreshOperation>('operation');
      } catch (err) {
        if (
          canonicalBase ||
          !operation?.routing.target ||
          !(err instanceof BootstrapTransportError) ||
          !['transport', 'not_found', 'unavailable'].includes(err.kind)
        )
          throw err;
        // Read-only reconciliation of a lost cutover reply. Verify the exact
        // saved route before attaching the bounded installation credential.
        canonicalBase = await verifyCanonical(operation);
        next = await request<FreshOperation>('operation');
      }
      if (!operation || !sameIdentity(next.identity, operation.identity)) {
        token = undefined;
        operation = undefined;
        throw new BootstrapTransportError('Bootstrap authority mismatch', 'mismatch');
      }
      if (!validOperationState(next, operation)) {
        throw new BootstrapTransportError('Bootstrap response unavailable', 'response');
      }
      operation = next;
      return next;
    },
    async generateAdminKey(): Promise<string> {
      const result = await request<{ key: string }>('admin/generate');
      if (!result || typeof result.key !== 'string' || !validateFreshAdminKey(result.key).valid) {
        throw new BootstrapTransportError('Bootstrap response unavailable', 'response');
      }
      return result.key;
    },
    async commitAdminKey(commitId: string, key: string): Promise<FreshOperation> {
      if (!operation || !commitId || commitId.length > 128) {
        throw new BootstrapTransportError('Bootstrap authority unavailable', 'authority');
      }
      const payload: AdminCommitPayload = {
        identity: operation.identity,
        commitId,
        key,
      };
      const next = await request<FreshOperation>('admin/commit', payload);
      if (
        !next ||
        !sameIdentity(next.identity, operation.identity) ||
        !validOperationState(next, operation) ||
        next.adminInitialized !== true
      ) {
        throw new BootstrapTransportError('Bootstrap response unavailable', 'response');
      }
      operation = next;
      return next;
    },
    async prepareRouting(target: string): Promise<FreshOperation> {
      if (
        !operation?.adminInitialized ||
        !validControlBasePath(target) ||
        (operation.routing.target && operation.routing.target !== target)
      ) {
        throw new BootstrapTransportError('Routing target unavailable', 'bad_request');
      }
      const next = await request<FreshOperation>('routing/prepare', {
        identity: operation.identity,
        target,
      });
      if (
        !sameIdentity(next?.identity, operation.identity) ||
        !next.adminInitialized ||
        next.routing?.target !== target ||
        next.routing.operationId !== operation.identity.operationId ||
        !Number.isSafeInteger(next.routing.revision) ||
        next.routing.revision! <= 0
      ) {
        throw new BootstrapTransportError('Routing response unavailable', 'response');
      }
      operation = next;
      return next;
    },
    async activateRouting(): Promise<string> {
      if (
        !operation?.adminInitialized ||
        !validControlBasePath(operation.routing.target) ||
        operation.routing.operationId !== operation.identity.operationId ||
        !Number.isSafeInteger(operation.routing.revision) ||
        operation.routing.revision! <= 0
      ) {
        throw new BootstrapTransportError('Routing authority unavailable', 'authority');
      }
      const prepared = operation;
      if (canonicalBase) return canonicalBase;
      try {
        const next = await request<FreshOperation>('routing/activate', {
          identity: prepared.identity,
        });
        if (
          !sameIdentity(next?.identity, prepared.identity) ||
          !next.adminInitialized ||
          next.routing?.target !== prepared.routing.target ||
          next.routing.operationId !== prepared.routing.operationId ||
          next.routing.revision !== prepared.routing.revision ||
          next.routing.committed !== true ||
          next.routing.stage !== 'committed'
        ) {
          throw new BootstrapTransportError('Routing response unavailable', 'mismatch');
        }
        operation = next;
      } catch (err) {
        // Commit retires the fixed carrier. A lost reply can therefore be
        // followed by 404, even though retrying the original operation worked.
        if (
          !(err instanceof BootstrapTransportError) ||
          !['transport', 'not_found', 'unavailable', 'response'].includes(err.kind)
        )
          throw err;
      }
      const verified = await verifyCanonical(prepared);
      // Retire the fixed carrier but keep the same bounded installation
      // authority in memory. This is not a Console/Admin login or a redirect.
      canonicalBase = verified;
      return verified;
    },
    async executeStrategy(
      body: Record<string, unknown>
    ): Promise<{ operation: FreshOperation; initialClientKey: string }> {
      if (!operation?.adminInitialized) {
        throw new BootstrapTransportError('Installation authority unavailable', 'authority');
      }
      // An adoption body is the whole two-POST contract, so it is validated as a
      // whole before it leaves the browser: the server decodes it strictly, and
      // the destination snapshot is keyed by it, so a malformed body is a
      // request the server would refuse rather than an attempt worth sending.
      if (body.strategy === 'adopt_existing') {
        const adopt = body as Record<string, unknown>;
        if (
          Object.keys(adopt).some((key) => !ADOPT_BODY_FIELDS[key]) ||
          adopt.adoptSourceClass !== ADOPT_SOURCE_CLASS ||
          typeof adopt.baseUrl !== 'string' ||
          typeof adopt.managementKey !== 'string' ||
          typeof adopt.pluginsEnabled !== 'boolean' ||
          typeof adopt.expectedRuntimeIdentity !== 'string' ||
          typeof adopt.expectedArtifactId !== 'string' ||
          !validRuntimeGeneration(adopt.expectedRuntimeGeneration)
        )
          throw new BootstrapTransportError('Adoption request unavailable', 'bad_request');
      }
      const next = await request<FreshOperation & { initialClientKey?: unknown }>('strategy', {
        ...body,
        identity: operation.identity,
        expectedRevision: operation.revision,
      });
      if (
        !sameIdentity(next?.identity, operation.identity) ||
        !validOperationState(next, operation)
      ) {
        throw new BootstrapTransportError('Installation response unavailable', 'mismatch');
      }
      // The install-generated Client Key is delivered by the call that commits a
      // New Managed destination and never by a replay. It stays in memory only.
      const delivered = next.initialClientKey;
      if (
        delivered !== undefined &&
        (typeof delivered !== 'string' ||
          !/^sk-[A-Za-z0-9]{64}$/.test(delivered) ||
          next.strategy?.strategy !== 'new_managed' ||
          next.strategy.stage !== 'committed')
      ) {
        throw new BootstrapTransportError('Installation response unavailable', 'mismatch');
      }
      operation = next;
      return { operation: next, initialClientKey: delivered ?? '' };
    },
    async complete(body: { acceptRetryable?: true } = {}): Promise<FreshOperation> {
      if (!canonicalBase || !operation?.adminInitialized) {
        throw new BootstrapTransportError('Installation authority unavailable', 'authority');
      }
      const next = await request<FreshOperation>('complete', body);
      if (
        !sameIdentity(next?.identity, operation.identity) ||
        !validOperationState(next, operation) ||
        next.productComplete !== true
      ) {
        throw new BootstrapTransportError('Installation response unavailable', 'mismatch');
      }
      operation = next;
      return next;
    },
    async readAdoption(): Promise<AdoptionSummary> {
      if (!operation?.adminInitialized) {
        throw new BootstrapTransportError('Installation authority unavailable', 'authority');
      }
      const next = await request<unknown>('installation/adoption');
      try {
        return parseAdoptionSummary(next);
      } catch {
        throw new BootstrapTransportError('Adoption response unavailable', 'response');
      }
    },
    request,
    clear() {
      token = undefined;
      operation = undefined;
      canonicalBase = undefined;
    },
  };
}

export type BootstrapTransportClient = ReturnType<typeof createBootstrapTransport>;
