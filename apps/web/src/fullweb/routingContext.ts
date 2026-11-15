import { validAccessPrefix } from './accessPrefix';

// Projection of the Manager's verified Phase4-01 RoutingSnapshot. This module
// consumes that authority; it never discovers a prefix from browser state.
export const PUBLIC_API_V2_SUFFIX = '/api/v2';


export interface RoutingContext {
  readonly basePath: string;
  readonly apiBase: string;
  readonly revision: number;
  readonly inferenceCompletionRequired?: boolean;
}


export function resolveRoutingContext(snapshot: unknown): RoutingContext | null {
  if (!snapshot || typeof snapshot !== 'object') return null;
  const projection = snapshot as Record<string, unknown>;
  if (
    projection.state !== 'verified_effective' ||
    !validAccessPrefix(projection.verifiedEffectiveControlBasePath) ||
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
    inferenceCompletionRequired: projection.inferenceCompletionRequired === true,
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
