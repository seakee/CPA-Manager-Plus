import { readFileSync } from 'node:fs';
import { createServer } from 'node:http';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import ts from 'typescript';
import { describe, expect, it, vi } from 'vitest';
import { createPublicApiClient } from '../apps/web/src/fullweb/publicApi.ts';
import { resolveRoutingContext } from '../apps/web/src/fullweb/routingContext.ts';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const web = path.join(root, 'apps/web');
const src = path.join(web, 'src');
const managerApi = path.join(src, 'services/api/usageService.ts');
const panelShim = path.join(src, 'panel/unsupportedManagerApi.ts');
const config = ts.readConfigFile(path.join(web, 'tsconfig.json'), ts.sys.readFile);
const { options } = ts.parseJsonConfigFileContent(config.config, ts.sys, web);
const resolutionCache = ts.createModuleResolutionCache(web, (file) => file, options);

// Follow actual runtime imports, re-exports and lazy imports using the installed
// TypeScript resolver. Type-only compatibility declarations create no JS edge.
function runtimeImports(file) {
  const parsed = ts.createSourceFile(
    file,
    readFileSync(file, 'utf8'),
    ts.ScriptTarget.Latest,
    true
  );
  const imports = [];
  const visit = (node) => {
    if (ts.isImportDeclaration(node)) {
      const clause = node.importClause;
      const bindings = clause?.namedBindings;
      const onlyTypes =
        clause?.isTypeOnly ||
        (!clause?.name &&
          bindings &&
          ts.isNamedImports(bindings) &&
          bindings.elements.every((element) => element.isTypeOnly));
      if (!onlyTypes) imports.push(node.moduleSpecifier.text);
    } else if (ts.isExportDeclaration(node) && node.moduleSpecifier && !node.isTypeOnly) {
      const onlyTypes =
        node.exportClause &&
        ts.isNamedExports(node.exportClause) &&
        node.exportClause.elements.every((element) => element.isTypeOnly);
      if (!onlyTypes) imports.push(node.moduleSpecifier.text);
    } else if (ts.isCallExpression(node) && node.expression.kind === ts.SyntaxKind.ImportKeyword) {
      if (!node.arguments[0] || !ts.isStringLiteral(node.arguments[0])) {
        throw new Error(`Cannot verify a computed dynamic import in ${file}`);
      }
      imports.push(node.arguments[0].text);
    }
    ts.forEachChild(node, visit);
  };
  visit(parsed);
  return imports;
}

function graph(entry, panel = false, { skipLegacyCompatibility = false } = {}) {
  const seen = new Set();
  const pending = [path.join(src, entry)];
  while (pending.length) {
    const file = pending.pop();
    if (seen.has(file)) continue;
    seen.add(file);
    for (const specifier of runtimeImports(file)) {
      // Bootstrap and the canonical transport stay isolated. The verified
      // Console route explicitly reuses the original login/session modules,
      // and only mounts business pages after ProductComplete. These are the
      // bounded compatibility edges; Fresh login runtime tests assert
      // that they never call legacy discovery/login instead of canonical auth.
      if (
        skipLegacyCompatibility &&
        file.endsWith('/fullweb/FreshConsolePage.tsx') &&
        ['./LegacyConsolePage', './FullWebLoginPage', './freshSession'].includes(specifier)
      ) {
        continue;
      }
      // Upgrade reuses the same login UI and original credential persistence.
      // Keep the Upgrade entry/session themselves in the checked core graph:
      // only these exact UI/storage/credential-key edges may reach legacy dependencies.
      if (
        skipLegacyCompatibility &&
        ((file.endsWith('/fullweb/UpgradeAuthenticationEntry.tsx') &&
          specifier === './FullWebLoginPage') ||
          (file.endsWith('/fullweb/upgradeSession.ts') &&
            [
              '../stores/useAuthStore',
              '../services/storage/secureStorage',
              '../utils/constants',
            ].includes(specifier)))
      ) {
        continue;
      }
      const resolution = ts.resolveModuleName(
        specifier,
        file,
        options,
        ts.sys,
        resolutionCache
      ).resolvedModule;
      if (!resolution || resolution.isExternalLibraryImport) continue;
      let target = resolution.resolvedFileName;
      if (panel && target === managerApi) target = panelShim;
      if (/\.[cm]?[jt]sx?$/.test(target) && !target.endsWith('.d.ts')) pending.push(target);
    }
  }
  return [...seen].map((file) => path.relative(src, file));
}

describe('Full Web / Panel Lite runtime dependency isolation', () => {
  it('blocks real redirects before external, Gateway and CPA destinations receive the credential', async () => {
    const received = [];
    const server = createServer((req, res) => {
      received.push({ path: req.url, authorization: req.headers.authorization });
      const target = req.url?.split('/').pop();
      res.writeHead(302, {
        Location:
          target === 'external'
            ? 'https://external.example/leak'
            : target === 'cpa'
              ? '/v0/management'
              : '/v1/models',
      });
      res.end();
    });
    await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
    const origin = `http://127.0.0.1:${server.address().port}`;
    vi.stubGlobal('window', { location: new URL(origin) });
    try {
      const context = resolveRoutingContext({
        state: 'verified_effective',
        verifiedEffectiveControlBasePath: '/phase4-test',
        revision: 1,
      });
      const client = createPublicApiClient(() => context);
      for (const resource of ['external', 'gateway', 'cpa']) {
        await expect(
          client.get(resource, { kind: 'admin', value: 'full-web-secret' })
        ).rejects.toMatchObject({ kind: 'network' });
      }
      expect(received).toEqual(
        ['external', 'gateway', 'cpa'].map((name) => ({
          path: `/phase4-test/api/v2/${name}`,
          authorization: 'Bearer full-web-secret',
        }))
      );
    } finally {
      vi.unstubAllGlobals();
      await new Promise((resolve, reject) =>
        server.close((error) => (error ? reject(error) : resolve()))
      );
    }
  });

  it('Full Web core excludes legacy transport outside the explicit login/session/Console/Upgrade compatibility edges', () => {
    const files = [
      ...new Set([
        ...graph('fullweb/main.tsx', false, { skipLegacyCompatibility: true }),
        ...graph('fullweb/publicApi.ts'),
      ]),
    ];
    expect(files.length).toBeGreaterThan(2);
    expect(files).toContain('fullweb/UpgradeAuthenticationEntry.tsx');
    expect(files).toContain('fullweb/upgradeSession.ts');
    expect(
      files.filter(
        (file) =>
          /^(?:services\/api|panel|entities\/usageService)\//.test(file) ||
          /^(?:stores\/useAuthStore|utils\/connection)\./.test(file)
      )
    ).toEqual([]);
    for (const file of files) {
      const source = readFileSync(path.join(src, file), 'utf8');
      expect(source, file).not.toMatch(/localStorage|sessionStorage|\/v0\/management/);
    }
  });

  it('limits Upgrade compatibility imports to the original credential persistence modules', () => {
    const session = path.join(src, 'fullweb/upgradeSession.ts');
    expect(runtimeImports(session).sort()).toEqual([
      '../services/storage/secureStorage',
      '../stores/useAuthStore',
      '../utils/constants',
    ]);
    // The shared constants module also defines legacy CPA endpoints. Only the
    // original auth-storage key belongs to this compatibility boundary.
    const parsed = ts.createSourceFile(session, readFileSync(session, 'utf8'), ts.ScriptTarget.Latest);
    const constants = parsed.statements.find(
      (node) => ts.isImportDeclaration(node) && node.moduleSpecifier.text === '../utils/constants'
    );
    expect(constants?.importClause?.name).toBeUndefined();
    const bindings = constants?.importClause?.namedBindings;
    expect(bindings && ts.isNamedImports(bindings)).toBe(true);
    expect(bindings.elements.map((element) => (element.propertyName ?? element.name).text)).toEqual([
      'STORAGE_KEY_AUTH',
    ]);
  });

  it('mounts the existing Console only through the authenticated canonical bridge seam', () => {
    const seam = readFileSync(path.join(src, 'fullweb/LegacyConsolePage.tsx'), 'utf8');
    const session = readFileSync(path.join(src, 'fullweb/freshSession.ts'), 'utf8');
    expect(seam).toContain('activateFreshSession(verified, credential,');
    expect(session).toContain("new URL(`${context.apiBase}/cpamp`, window.location.origin)");
    expect(session).toContain('apiClient.setConfig({ apiBase, managementKey: credential })');
    expect(session).toContain('useAuthStore.setState');
    expect(seam).toContain('resolveRoutingContext');
  });

  it('Panel graph excludes Full Web transport/auth and replaces the legacy Manager transport', () => {
    const files = graph('main.tsx', true);
    expect(files).toContain('panel/unsupportedManagerApi.ts');
    expect(files).toContain('services/api/client.ts');
    expect(files).not.toContain('services/api/usageService.ts');
    expect(files.filter((file) => file.startsWith('fullweb/'))).toEqual([]);
    const vite = readFileSync(path.join(web, 'vite.config.ts'), 'utf8');
    expect(vite).toContain('unsupportedManagerApi.ts');
  }, 30_000);

  it('uses the Phase4-01 ledger suffix for the browser canonical API base', () => {
    const ledger = JSON.parse(
      readFileSync(path.join(root, 'tests/fixtures/phase4-routing/route-owners.json'), 'utf8')
    );
    const browser = readFileSync(path.join(src, 'fullweb/routingContext.ts'), 'utf8');
    expect(browser).toContain(`PUBLIC_API_V2_SUFFIX = '${ledger.canonicalPublicApiV2Suffix}'`);
    const backend = readFileSync(
      path.join(root, 'apps/manager-server/internal/domain/routing/path.go'),
      'utf8'
    );
    expect(backend).toContain(`PublicAPIV2Suffix = "${ledger.canonicalPublicApiV2Suffix}"`);
    const prefix = readFileSync(
      path.join(root, 'apps/manager-server/internal/domain/bootstrap/prefix.go'),
      'utf8'
    );
    expect(backend).toContain('return bootstrapdomain.ValidControlBasePath(value)');
    expect(backend).not.toContain('canonicalControlBasePathCharacters');
    expect(prefix).not.toContain('validControlBasePath(');
    const grammar = prefix.match(
      /canonicalControlBasePathCharacters = regexp.MustCompile\(`([^`]+)`\)/
    )?.[1];
    expect(grammar).toBeDefined();
    expect(browser).toContain(`/${grammar.replace('^/', '^\\/')}/.test(value)`);
    const reserved = backend.match(
      /func reservedNamespaceCollision[\s\S]*?\[\]string\{([\s\S]*?)\}/
    )?.[1];
    expect(reserved).toBeDefined();
    const paths = [...reserved.matchAll(/"([^"]+)"/g)].map((match) => match[1]);
    if (reserved.includes('BootstrapDiscoveryPath')) paths.push(ledger.fixedBootstrapDiscoveryPath);
    // Both are validation mirrors of the server contract, never prefix discovery.
    const browserReserved = browser.match(/const RESERVED_CONTROL_PATHS = \[([\s\S]*?)\]/)?.[1];
    expect([...browserReserved.matchAll(/'([^']+)'/g)].map((match) => match[1]).sort()).toEqual(
      paths.sort()
    );
  });

  it('guards the two remaining legacy Manager update transports in Panel builds', () => {
    const vite = readFileSync(path.join(web, 'vite.config.ts'), 'utf8');
    expect(vite).toContain('__PANEL_LITE__: JSON.stringify(panelBuild)');
    const manager = readFileSync(path.join(src, 'features/system/managerUpdateApi.ts'), 'utf8');
    expect(manager.indexOf('if (__PANEL_LITE__)')).toBeLessThan(
      manager.indexOf('await axios.request')
    );
    const cpa = readFileSync(path.join(src, 'features/system/cpaUpdateApi.ts'), 'utf8');
    expect(cpa).toContain('if (__PANEL_LITE__ ||');
    expect(cpa.indexOf('if (__PANEL_LITE__ ||')).toBeLessThan(cpa.indexOf('await axios.request'));
  });
});
