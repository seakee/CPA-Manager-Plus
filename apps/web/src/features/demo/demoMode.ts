export const DEMO_ROUTE_BASE = '/demo';
export const DEMO_API_BASE = 'http://demo.local';
export const DEMO_MANAGEMENT_KEY = 'demo-management-key';
export const DEMO_SERVER_VERSION = 'v7.1.18-demo';
export const DEMO_SERVER_COMMIT = '5bffd1514fba2ca7cbfd13bb6530a6f7d9d72d43';
export const DEMO_MAINTENANCE_QUERY_KEY = 'maintenance';

export type DemoMaintenanceScenario = 'clean' | 'degraded';

export const formatDemoDate = (input = Date.now()): string => {
  const date = new Date(input);
  const year = date.getFullYear();
  const month = String(date.getMonth() + 1).padStart(2, '0');
  const day = String(date.getDate()).padStart(2, '0');
  return `${year}-${month}-${day}`;
};

export const getDemoServerBuildDate = (): string => formatDemoDate();

let demoModeEnabled = false;

const normalizePathname = (pathname: string): string => {
  const normalized = pathname.startsWith('/') ? pathname : `/${pathname}`;
  return normalized.length > 1 && normalized.endsWith('/')
    ? normalized.replace(/\/+$/g, '') || '/'
    : normalized;
};

export const stripRouteBase = (pathname: string, routeBase = DEMO_ROUTE_BASE): string => {
  const path = normalizePathname(pathname || '/');
  const base = normalizePathname(routeBase || '');
  if (!base || base === '/') return path;
  if (path === base) return '/';
  if (path.startsWith(`${base}/`)) {
    return normalizePathname(path.slice(base.length) || '/');
  }
  return path;
};

export const prefixRouteBase = (path: string, routeBase = DEMO_ROUTE_BASE): string => {
  const base = normalizePathname(routeBase || '');
  const target = normalizePathname(path || '/');
  if (!base || base === '/') return target;
  return target === '/' ? base : `${base}${target}`;
};

export const ensureRouteBasePathname = (pathname: string, routeBase = DEMO_ROUTE_BASE): string =>
  prefixRouteBase(stripRouteBase(pathname || '/', routeBase), routeBase);

export const getDemoLogoutPath = (routeBase = DEMO_ROUTE_BASE): string =>
  prefixRouteBase('/', routeBase || DEMO_ROUTE_BASE);

export const getDemoLogoutHash = (routeBase = DEMO_ROUTE_BASE): string =>
  `#${getDemoLogoutPath(routeBase)}`;

const readCurrentHashPathname = (): string => {
  if (typeof window === 'undefined') return '/';
  const hash =
    typeof window.location.hash === 'string' ? window.location.hash.replace(/^#/, '') : '';
  if (!hash) return '/';
  const [pathname = '/'] = hash.split(/[?#]/);
  return normalizePathname(pathname || '/');
};

// This switch exists only for the frontend demo's visual acceptance path. The
// production panel never calls the demo fixture that consumes it.
export const getDemoMaintenanceScenario = (): DemoMaintenanceScenario => {
  if (typeof window === 'undefined' || !window.location) return 'clean';

  const queries = [typeof window.location.search === 'string' ? window.location.search : ''];
  const hash =
    typeof window.location.hash === 'string' ? window.location.hash.replace(/^#/, '') : '';
  const hashQueryIndex = hash.indexOf('?');
  if (hashQueryIndex >= 0) {
    queries.push(hash.slice(hashQueryIndex));
  }

  for (const query of queries) {
    const value = new URLSearchParams(query).get(DEMO_MAINTENANCE_QUERY_KEY);
    if (value?.trim().toLowerCase() === 'degraded') return 'degraded';
  }
  return 'clean';
};

export const setDemoMode = (enabled: boolean): void => {
  demoModeEnabled = enabled;
};

export const isDemoRoutePath = (pathname: string): boolean => {
  const normalized = normalizePathname(pathname || '/');
  return normalized === DEMO_ROUTE_BASE || normalized.startsWith(`${DEMO_ROUTE_BASE}/`);
};

export const isDemoMode = (): boolean => {
  if (demoModeEnabled) return true;
  return isDemoRoutePath(readCurrentHashPathname());
};
