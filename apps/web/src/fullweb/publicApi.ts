import { readRoutingContext, resolveRoutingContext, type RoutingContext } from './routingContext';
import { parseGatewayJSON, stringifyGatewayJSON } from './gatewayRuleJSON';
import { parseRuntimeJSON, runtimeRequestJSON } from './runtimeGeneration';

export type PublicApiErrorKind = 'routing_unavailable' | 'network' | 'http' | 'authentication';

// Stable codes the gateway configuration page can act on. Every other failure
// stays a bare HTTP error: the page never guesses a cause from a status alone.
const GATEWAY_CONFIG_ERROR_CODES: Record<string, true> = {
  operation_not_found: true,
  source_or_operation_conflict: true,
  config_key_writer_not_isolated: true,
  original_operation_unknown: true,
  configuration_unavailable: true,
  config_authority_unavailable: true,
  gateway_config_read_only: true,
  payload_rules_read_only: true,
  invalid_config_request: true,
  // A CPA refusal. The code is bounded, and the field and redacted diagnostic
  // travel with it so the failure lands on the leaf the user must fix.
  cpa_rejected_config: true,
  // The browser read a different CPA target than the one it saves into.
  generation_mismatch: true,
  // A pre-cutover field operation still holds this group.
  legacy_operation_unresolved: true,
  // The remaining two are the Manager's own pre-CPA refusals. Neither is ever
  // CPA's string: the Manager maps its refusals onto these codes.
  //
  // A proxy URL with inline credentials is rejected on requests.proxy-url.
  proxy_url_credentials: true,
  // A value outside the private profile's field range.
  invalid_config: true,
};

// The gateway configuration surface is `gateway/config` plus its sub-resources.
const isGatewayConfigResource = (resource: string) =>
  resource === 'gateway/config' || resource.startsWith('gateway/config/');

// Typed External codes the pages act on. The Manager maps its own refusals onto
// these; no branch carries a credential, an address or any text from the CPA.
const EXTERNAL_ERROR_CODES: Record<string, true> = {
  // The user's CPA refused the saved management key. Only a rebind resolves it.
  external_management_key_rejected: true,
  // The committed target and the live connection name different CPAs.
  external_target_moved: true,
  // The user's CPA did not prove the v8 contract this Manager writes.
  external_cpa_version_unsupported: true,
  // A rebind that named another address, or refused for another reason.
  external_rebind_refused: true,
  // Something else moved the installation while the candidate was proven.
  external_rebind_stale_generation: true,
  // The committed External authority could not be resolved.
  external_cpa_authority_unavailable: true,
};

const isExternalResource = (resource: string) =>
  resource === 'external/usage' ||
  resource.startsWith('external/usage/') ||
  resource === 'external/rebind' ||
  resource.startsWith('external/rebind/');

// A gateway resource is the Manager's own document, but on an External install the
// save is written through the committed CPA credential: the same conflict can
// carry any External code. The page merges both sets, so the blocker renders
// instead of the code being dropped as an unrecognised payload.
const GATEWAY_ERROR_CODES: Record<string, true> = {
  ...GATEWAY_CONFIG_ERROR_CODES,
  ...EXTERNAL_ERROR_CODES,
};

// Every resource whose failure carries a typed code the page must act on. The
// payload is parsed for exactly these; every other failure stays a bare HTTP
// error, because a page must never guess a cause from a status alone.
const isTypedErrorResource = (resource: string) =>
  isGatewayConfigResource(resource) || isExternalResource(resource);

export class PublicApiError extends Error {
  constructor(
    readonly kind: PublicApiErrorKind,
    readonly status?: number,
    readonly operationResult?: unknown,
    readonly code?: string,
    // A CPA rejection names the field and the redacted diagnostic it refused.
    readonly field?: string,
    readonly detail?: string
  ) {
    super(`Public API ${kind}`);
    this.name = 'PublicApiError';
  }
}

// Only an explicit in-memory Full Web credential may be attached. There is no
// connection-store import, persisted credential lookup, or CPA compatibility.
export interface FullWebCredential {
  readonly kind: 'admin' | 'session';
  readonly value: string;
}

export interface PublicApiClient {
  get: <T = unknown>(resource: string, credential?: FullWebCredential) => Promise<T>;
  post: <T = unknown>(resource: string, body: unknown, credential: FullWebCredential) => Promise<T>;
  put: <T = unknown>(resource: string, body: unknown, credential: FullWebCredential) => Promise<T>;
}

export function createPublicApiClient(
  readContext: () => RoutingContext | null = () => readRoutingContext(document),
  transport: typeof fetch = globalThis.fetch
) {
  async function request<T>(
    resource: string,
    credential?: FullWebCredential,
    body?: unknown,
    method = body === undefined ? 'GET' : 'POST'
  ): Promise<T> {
    const context = readContext();
    const canonical = resolveRoutingContext({
      state: 'verified_effective',
      verifiedEffectiveControlBasePath: context?.basePath,
      revision: context?.revision,
    });
    // One normalized resource below the canonical namespace. Query/absolute
    // URLs and URL normalization cannot select another transport destination.
    if (
      !context ||
      !canonical ||
      context.apiBase !== canonical.apiBase ||
      !/^[a-zA-Z0-9_-]+(?:\/[a-zA-Z0-9_-]+)*$/.test(resource) ||
      !['http:', 'https:'].includes(window.location.protocol)
    ) {
      throw new PublicApiError('routing_unavailable');
    }
    const target = new URL(`${context.apiBase}/${resource}`, window.location.origin);
    if (
      target.origin !== window.location.origin ||
      target.pathname !== `${context.apiBase}/${resource}` ||
      target.search ||
      target.hash
    ) {
      throw new PublicApiError('routing_unavailable');
    }
    const headers: Record<string, string> = { Accept: 'application/json' };
    if (credential) headers.Authorization = `Bearer ${credential.value}`;
    if (body !== undefined) headers['Content-Type'] = 'application/json';
    const controller =
      body !== undefined ||
      resource === 'operation' ||
      resource === 'installation/runtime' ||
      resource === 'runtime/status' ||
      resource === 'runtime/operations' ||
      resource.startsWith('runtime/operations/') ||
      resource.startsWith('api-keys/') ||
      resource === 'settings/update-channel' ||
      isTypedErrorResource(resource)
        ? new AbortController()
        : undefined;
    const timer = controller ? setTimeout(() => controller.abort(), 15000) : undefined;
    try {
      const response = await transport(target.href, {
        method,
        headers,
        ...(body !== undefined
          ? {
              // A payload rule family is arbitrary JSON and may hold integers wider
              // than a double: stringify keeps them exact instead of sending a
              // rounded number back to the Manager.
              body:
                resource === 'strategy'
                  ? runtimeRequestJSON(body)
                  : resource.startsWith('gateway/config/rules/')
                    ? stringifyGatewayJSON(body)
                    : JSON.stringify(body),
            }
          : {}),
        ...(controller ? { signal: controller.signal } : {}),
        mode: 'same-origin',
        credentials: 'omit',
        redirect: 'error',
        cache: 'no-store',
      });
      if (response.status === 401 || response.status === 403) {
        throw new PublicApiError('authentication', response.status);
      }
      if (!response.ok) {
        // Failed operation reads may still carry a durable journal; they must
        // retain their original 409/503 status and cannot authorize writes.
        let operationResult: unknown;
        let code: string | undefined;
        let field: string | undefined;
        let detail: string | undefined;
        const retryResource = isGatewayConfigResource(resource);
        // The External pages act on the same wire shape: a bounded `code` and,
        // for a refused credential, the rebind `detail`. The key list answers
        // from the same authority, so it is read the same way.
        const externalResource =
          isExternalResource(resource) ||
          resource === 'api-keys' ||
          // A single key answers the same typed codes on its own writes: renaming,
          // revoking and replacing one are written through the same credential, and
          // the operation journal keeps its own answer below.
          (resource.startsWith('api-keys/') && !resource.startsWith('api-keys/operations/'));
        const operationResource = method === 'GET' && resource.startsWith('api-keys/operations/');
        if (
          (retryResource ||
            externalResource ||
            operationResource ||
            (resource === 'strategy' && response.status === 409)) &&
          (response.status === 409 ||
            response.status === 503 ||
            (retryResource &&
              (response.status === 400 || response.status === 422)))
        ) {
          try {
            const payload = (await response.json()) as Record<string, unknown>;
            if (retryResource || externalResource) {
              // Only a recognised code unlocks the diagnostic: an unrecognised
              // upstream payload must not smuggle its message into the UI.
              const known = retryResource ? GATEWAY_ERROR_CODES : EXTERNAL_ERROR_CODES;
              if (typeof payload.code === 'string' && known[payload.code] === true)
                code = payload.code;
              if (code) {
                if (typeof payload.field === 'string') field = payload.field;
                if (typeof payload.message === 'string') detail = payload.message;
                if (typeof payload.detail === 'string') detail = payload.detail;
              }
              // A 400 shape error and a 422 CPA rejection carry no journal; only a
              // conflict or an unavailable authority can hold an operation.
              if (
                (response.status === 409 || response.status === 503) &&
                'result' in payload &&
                payload.result &&
                typeof payload.result === 'object'
              )
                operationResult = payload.result;
            } else if (resource === 'strategy') {
              // Only one bounded CPA-source error is actionable to this UI.
              // Admin authentication errors remain separate 401/403 responses.
              if (payload.Code === 'management_authentication_rejected')
                code = 'management_authentication_rejected';
            } else if (
              typeof payload.code === 'string' &&
              payload.code.startsWith('key_operation_') &&
              'result' in payload
            ) {
              operationResult = payload.result;
            }
          } catch {
            // A malformed failed response does not provide a journal snapshot.
          }
        }
        throw new PublicApiError('http', response.status, operationResult, code, field, detail);
      }
      // The gateway projection carries family rules, whose wide integers must not
      // be rounded on the way in either.
      if (isGatewayConfigResource(resource)) return parseGatewayJSON(await response.text()) as T;
      return (
        ['operation', 'strategy', 'complete', 'installation/runtime'].includes(resource)
          ? parseRuntimeJSON(await response.text())
          : await response.json()
      ) as T;
    } catch (err) {
      if (err instanceof PublicApiError) throw err;
      throw new PublicApiError('network');
    } finally {
      if (timer !== undefined) clearTimeout(timer);
    }
  }
  const api: PublicApiClient = {
    get: <T = unknown>(resource: string, credential?: FullWebCredential) =>
      request<T>(resource, credential),
    post: <T = unknown>(resource: string, body: unknown, credential: FullWebCredential) =>
      request<T>(resource, credential, body),
    put: <T = unknown>(resource: string, body: unknown, credential: FullWebCredential) =>
      request<T>(resource, credential, body, 'PUT'),
  };
  return api;
}
