const normalizePathname = (pathname: string): string => {
  const normalized = pathname.startsWith('/') ? pathname : `/${pathname}`;
  return normalized.length > 1 && normalized.endsWith('/')
    ? normalized.replace(/\/+$/g, '') || '/'
    : normalized;
};

export const stripRouteBase = (pathname: string, routeBase = ''): string => {
  const path = normalizePathname(pathname || '/');
  const base = normalizePathname(routeBase || '');
  if (!base || base === '/') return path;
  if (path === base) return '/';
  if (path.startsWith(`${base}/`)) {
    return normalizePathname(path.slice(base.length) || '/');
  }
  return path;
};

export const prefixRouteBase = (path: string, routeBase = ''): string => {
  const base = normalizePathname(routeBase || '');
  const target = normalizePathname(path || '/');
  if (!base || base === '/') return target;
  return target === '/' ? base : `${base}${target}`;
};

export const ensureRouteBasePathname = (pathname: string, routeBase = ''): string =>
  prefixRouteBase(stripRouteBase(pathname || '/', routeBase), routeBase);
