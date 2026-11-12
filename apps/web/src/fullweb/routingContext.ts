// Projection of the Manager's verified Phase4-01 RoutingSnapshot. This module
// consumes that authority; it never discovers a prefix from browser state.
export const PUBLIC_API_V2_SUFFIX = '/api/v2';

// Shape validation mirrors RoutingSnapshot.Validate's reserved collisions.
// A test compares this set with Go; neither set activates a browser mount.
const RESERVED_CONTROL_PATHS = [
  '/.well-known/cpamp-bootstrap',
  '/health',
  '/management.html',
  '/models',
  '/setup',
  '/status',
  '/v0',
  '/v1',
  '/usage-service',
  '/internal/routing/fresh',
  '/internal/routing/fresh-authority',
  '/favicon.ico',
  '/apple-touch-icon.png',
];

export interface RoutingContext {
  readonly basePath: string;
  readonly apiBase: string;
  readonly revision: number;
}

const canonicalBasePath = (value: unknown): value is string =>
  typeof value === 'string' &&
  // Same URL-serialization-stable grammar as Manager ValidControlBasePath.
  /^\/[A-Za-z0-9._~$&+,/:;=@-]+$/.test(value) &&
  value.startsWith('/') &&
  value !== '/' &&
  !/[\s\p{Cc}?#\\]/u.test(value) &&
  !/%[\da-f]{2}/i.test(value) &&
  value
    .split('/')
    .slice(1)
    .every((part) => part !== '' && part !== '.' && part !== '..');

// Shape checking only; this never grants verified routing authority.
export function validControlBasePath(value: unknown): value is string {
  return (
    canonicalBasePath(value) &&
    !RESERVED_CONTROL_PATHS.some(
      (reserved) =>
        value === reserved || value.startsWith(reserved + '/') || reserved.startsWith(value + '/')
    )
  );
}

export function resolveRoutingContext(snapshot: unknown): RoutingContext | null {
  if (!snapshot || typeof snapshot !== 'object') return null;
  const projection = snapshot as Record<string, unknown>;
  if (
    projection.state !== 'verified_effective' ||
    !validControlBasePath(projection.verifiedEffectiveControlBasePath) ||
    !Number.isSafeInteger(projection.revision) ||
    (projection.revision as number) <= 0
  ) {
    return null;
  }
  const basePath = projection.verifiedEffectiveControlBasePath;
  return Object.freeze({
    basePath,
    apiBase: basePath + PUBLIC_API_V2_SUFFIX,
    revision: projection.revision as number,
  });
}

export function readRoutingContext(
  source: Pick<Document, 'getElementById'>
): RoutingContext | null {
  const element = source.getElementById('cpamp-routing-context');
  if (!element || element.getAttribute('type') !== 'application/json') return null;
  try {
    return resolveRoutingContext(JSON.parse(element.textContent ?? ''));
  } catch {
    return null;
  }
}

export function isInitialRoutingContext(source: Pick<Document, 'getElementById'>): boolean {
  const element = source.getElementById('cpamp-routing-context');
  if (!element || element.getAttribute('type') !== 'application/json') return false;
  try {
    const parsed: unknown = JSON.parse(element.textContent ?? '');
    return (
      !!parsed &&
      typeof parsed === 'object' &&
      (parsed as Record<string, unknown>).state === 'initial_routing'
    );
  } catch {
    return false;
  }
}
