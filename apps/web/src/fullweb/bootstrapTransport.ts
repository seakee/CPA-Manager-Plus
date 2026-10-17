// Bounded pre-canonical carrier. It has no CPA/Admin transport and persists no
// secrets. This module belongs to the existing Full Web entry graph.
export const BOOTSTRAP_ROOT = '/.well-known/cpamp-bootstrap';
export const BOOTSTRAP_UI = `${BOOTSTRAP_ROOT}/ui`;
const API = `${BOOTSTRAP_ROOT}/api/v2/`;

export interface FreshIdentity {
  admissionId: string;
  installationId: string;
  generation: string;
  databasePath: string;
  operationId: string;
}

export interface FreshOperation {
  identity: FreshIdentity;
  revision: number;
  adminInitialized: boolean;
  gatewayPending: boolean;
  routing: { target?: string; committed: boolean };
}

interface Handoff {
  identity: FreshIdentity;
  claimGeneration: number;
  token: string;
  resume: boolean;
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

const methods = {
  'claim/exchange': 'POST',
  operation: 'GET',
  'admin/generate': 'POST',
  'admin/commit': 'POST',
  'routing/prepare': 'POST',
  'routing/activate': 'POST',
  strategy: 'POST',
} as const;
type Resource = keyof typeof methods;

export function createBootstrapTransport(transport: typeof fetch = globalThis.fetch) {
  let token: string | undefined;
  let operation: FreshOperation | undefined;
  async function request<T>(resource: Resource, body?: unknown): Promise<T> {
    if (
      window.location.pathname !== BOOTSTRAP_UI ||
      !['http:', 'https:'].includes(window.location.protocol) ||
      !Object.prototype.hasOwnProperty.call(methods, resource) ||
      (resource !== 'claim/exchange' && !token)
    ) {
      throw new Error('Bootstrap authority unavailable');
    }
    const target = new URL(API + resource, window.location.origin);
    const headers: Record<string, string> = { Accept: 'application/json' };
    if (body !== undefined) headers['Content-Type'] = 'application/json';
    if (token) headers.Authorization = `Fresh ${token}`;
    let response: Response;
    try {
      response = await transport(target.href, {
        method: methods[resource],
        headers,
        ...(body === undefined ? {} : { body: JSON.stringify(body) }),
        mode: 'same-origin',
        credentials: 'omit',
        redirect: 'error',
        cache: 'no-store',
      });
    } catch {
      throw new Error('Bootstrap transport unavailable');
    }
    if (!response.ok) throw new Error('Bootstrap request unavailable');
    try {
      return (await response.json()) as T;
    } catch {
      throw new Error('Bootstrap response unavailable');
    }
  }
  return {
    async start(handoff: Handoff): Promise<FreshOperation> {
      if (token) throw new Error('Bootstrap authority already installed');
      if (handoff.resume) {
        token = handoff.token;
        operation = await request<FreshOperation>('operation');
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
      if (
        !operation ||
        Object.keys(handoff.identity).some(
          (key) =>
            operation?.identity[key as keyof FreshIdentity] !==
            handoff.identity[key as keyof FreshIdentity]
        )
      ) {
        token = undefined;
        operation = undefined;
        throw new Error('Bootstrap authority mismatch');
      }
      return operation;
    },
    request,
    clear() {
      token = undefined;
      operation = undefined;
    },
  };
}
