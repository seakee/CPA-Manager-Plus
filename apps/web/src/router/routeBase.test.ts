import { describe, expect, it } from 'vitest';
import { ensureRouteBasePathname, prefixRouteBase, stripRouteBase } from './routeBase';

describe('routeBase', () => {
  it('strips an explicit route base while preserving nested paths and queries', () => {
    expect(stripRouteBase('/console', '/console')).toBe('/');
    expect(stripRouteBase('/console/', '/console/')).toBe('/');
    expect(stripRouteBase('/console/config', '/console')).toBe('/config');
    expect(stripRouteBase('/console/monitoring?tab=events', '/console')).toBe(
      '/monitoring?tab=events'
    );
    expect(stripRouteBase('/config', '/console')).toBe('/config');
    expect(stripRouteBase('/console-other', '/console')).toBe('/console-other');
  });

  it('prefixes paths under an explicit route base', () => {
    expect(prefixRouteBase('/', '/console')).toBe('/console');
    expect(prefixRouteBase('/config', '/console')).toBe('/console/config');
    expect(prefixRouteBase('config/', '/console/')).toBe('/console/config');
    expect(prefixRouteBase('/monitoring/account-actions', '/console')).toBe(
      '/console/monitoring/account-actions'
    );
  });

  it('leaves root-relative paths unchanged when no route base is configured', () => {
    expect(stripRouteBase('/config')).toBe('/config');
    expect(stripRouteBase('/config', '')).toBe('/config');
    expect(stripRouteBase('/config', '/')).toBe('/config');

    expect(prefixRouteBase('/')).toBe('/');
    expect(prefixRouteBase('/config')).toBe('/config');
    expect(prefixRouteBase('/config', '')).toBe('/config');
    expect(prefixRouteBase('/config', '/')).toBe('/config');

    expect(ensureRouteBasePathname('/config')).toBe('/config');
    expect(ensureRouteBasePathname('/config', '')).toBe('/config');
  });

  it('ensures pathnames carry the route base without double-prefixing', () => {
    expect(ensureRouteBasePathname('/', '/console')).toBe('/console');
    expect(ensureRouteBasePathname('/config', '/console')).toBe('/console/config');
    expect(ensureRouteBasePathname('/console/config', '/console')).toBe('/console/config');
  });
});
