import { describe, expect, it } from 'vitest';
import { isInitialRoutingContext, readRoutingContext, resolveRoutingContext } from './routingContext';

const snapshot = {
  state: 'verified_effective',
  verifiedEffectiveControlBasePath: '/phase4-test',
  revision: 1,
};

describe('Full Web consumes the verified Manager routing projection', () => {
  it('derives one immutable router/static/API context from the effective prefix', () => {
    const context = resolveRoutingContext(snapshot);
    expect(context).toEqual({
      basePath: '/phase4-test',
      apiBase: '/phase4-test/api/v2',
      revision: 1,
    });
    expect(Object.isFrozen(context)).toBe(true);
  });

  it.each(['desired_only', 'unknown', 'unavailable', 'stale', '', null])(
    'fails closed for snapshot state %s',
    (state) => expect(resolveRoutingContext({ ...snapshot, state })).toBeNull()
  );

  it.each([
    '',
    '/',
    'phase4',
    '//phase4',
    '/phase4/',
    '/phase4//test',
    '/phase4/../test',
    '/phase4/./test',
    '/phase4?x',
    '/phase4#x',
    '/phase4\\x',
    '/phase4%2fx',
    '/phase4%252fx',
    '/phase4%2ex',
    '/phase4 x',
    '/phase4\u0000x',
    '/phase4\u0085x',
    '/控制',
    '/café',
    '/😀',
    '/control[admin]',
    '/control!',
    '/control%',
    '/control"',
    '/control`',
    '/control{admin}',
  ])('rejects non-canonical control base %j', (verifiedEffectiveControlBasePath) => {
    expect(resolveRoutingContext({ ...snapshot, verifiedEffectiveControlBasePath })).toBeNull();
  });

  it.each(['/phase4-test', '/control/sub-path', '/AZaz09-._~$&+,/:;=@'])(
    'keeps canonical router, static and API paths unchanged by URL serialization: %s',
    (basePath) => {
      const context = resolveRoutingContext({
        ...snapshot,
        verifiedEffectiveControlBasePath: basePath,
      });
      expect(context?.basePath).toBe(basePath);
      for (const suffix of ['', '/technical-route', '/assets/index-AbCd1234.js', '/api/v2/probe']) {
        expect(new URL(basePath + suffix, 'https://manager.example').pathname).toBe(
          basePath + suffix
        );
      }
    }
  );

  it.each([0, -1, 1.5, '1', Number.MAX_SAFE_INTEGER + 1, null])(
    'requires a positive safe snapshot revision %j',
    (revision) => expect(resolveRoutingContext({ ...snapshot, revision })).toBeNull()
  );

  it.each([
    '/v0',
    '/v0/management',
    '/v1',
    '/health',
    '/setup',
    '/.well-known',
    '/favicon.ico',
    '/favicon.ico/nested',
    '/apple-touch-icon.png',
  ])('rejects the same reserved collisions as Manager: %s', (verifiedEffectiveControlBasePath) => {
    expect(resolveRoutingContext({ ...snapshot, verifiedEffectiveControlBasePath })).toBeNull();
  });

  it('ignores desired paths and legacy browser connection data', () => {
    expect(
      resolveRoutingContext({
        ...snapshot,
        desiredControlBasePath: '/desired',
        apiBase: 'http://cpa:8317',
        managementKey: 'cpa-secret',
        controlBasePath: '/legacy',
      })?.apiBase
    ).toBe('/phase4-test/api/v2');
    expect(
      resolveRoutingContext({
        state: 'verified_effective',
        controlBasePath: '/legacy',
        revision: 1,
      })
    ).toBeNull();
  });

  it('reads only the server JSON projection; missing/unreplaced/malformed carrier fails closed', () => {
    const source = (content: string, type = 'application/json') =>
      ({
        getElementById: (id: string) =>
          id === 'cpamp-routing-context'
            ? {
                textContent: content,
                getAttribute: () => type,
              }
            : null,
      }) as unknown as Pick<Document, 'getElementById'>;
    expect(readRoutingContext(source(JSON.stringify(snapshot)))?.basePath).toBe('/phase4-test');
    expect(readRoutingContext(source('__CPAMP_ROUTING_CONTEXT__'))).toBeNull();
    expect(readRoutingContext(source(JSON.stringify(snapshot), 'text/javascript'))).toBeNull();
    expect(readRoutingContext({ getElementById: () => null })).toBeNull();
    expect(readRoutingContext(source('{"state":"initial_routing"}'))).toBeNull();
    expect(isInitialRoutingContext(source('{"state":"initial_routing"}'))).toBe(true);
    expect(isInitialRoutingContext(source('{"state":"initial_routing"}', 'text/javascript'))).toBe(
      false
    );
    expect(isInitialRoutingContext(source(JSON.stringify(snapshot)))).toBe(false);
    expect(isInitialRoutingContext(source('__CPAMP_ROUTING_CONTEXT__'))).toBe(false);
  });
});
