import { readFileSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const fixturePath = path.join(repoRoot, 'tests/fixtures/phase4-routing/route-owners.json');
const schemaPath = path.join(repoRoot, 'tests/fixtures/phase4-routing/route-owners.schema.json');
const ingressClassifierPath = path.join(repoRoot, 'apps/ingress/internal/ingress/classifier.go');
const managerRoutingPath = path.join(
  repoRoot,
  'apps/manager-server/internal/domain/routing/path.go'
);
const managerStartupPath = path.join(
  repoRoot,
  'apps/manager-server/cmd/cpa-manager-plus/startup_discovery.go'
);
const managerDiscoveryContractPath = path.join(
  repoRoot,
  'apps/manager-server/internal/domain/bootstrap/public_contract.go'
);

const readJSON = (filePath) => JSON.parse(readFileSync(filePath, 'utf8'));

describe('Phase4-03 routing ownership successor contract', () => {
  it('advances the identity and preserves every Phase4-02 assertion verbatim', () => {
    const fixture = readJSON(fixturePath);
    const schema = readJSON(schemaPath);
    const previous = JSON.parse(
      execFileSync(
        'git',
        [
          'show',
          '63cf85c9b84de87bc469d9aab8d492c18cd7a0bf:tests/fixtures/phase4-routing/route-owners.json',
        ],
        { cwd: repoRoot, encoding: 'utf8' }
      )
    );
    expect(fixture.schemaVersion).toBe(3);
    expect(fixture.contractId).toBe('cpamp-v2-phase4-03-routing-ownership-v3');
    expect(schema.properties.schemaVersion.const).toBe(3);
    expect(schema.properties.contractId.const).toBe(fixture.contractId);
    for (const [key, value] of Object.entries(previous)) {
      if (['schemaVersion', 'contractId'].includes(key)) continue;
      expect(fixture[key]).toEqual(value);
    }
    expect(fixture.fixedBootstrapNamespace).toEqual(schema.properties.fixedBootstrapNamespace.const);
    expect(fixture.fixedBootstrapNamespace.authentication).toBe('fresh_session_only');
    expect(fixture.fixedBootstrapNamespace.retirement).toBe('verified_initial_route_commit_generic_404');
    expect(fixture.fixedBrandResources.map((resource) => resource.path)).toEqual([
      '/favicon.ico',
      '/apple-touch-icon.png',
    ]);
    for (const resource of fixture.fixedBrandResources) {
      expect(resource.methods).toEqual(['GET', 'HEAD']);
      expect(resource.nonMatchingRequest).toBe('normal_classifier');
      expect(resource.missingOrDisabled).toBe('manager_local_404');
    }
  });
  it('keeps one exact discovery path and one canonical Public API v2 suffix', () => {
    const fixture = readJSON(fixturePath);
    const schema = readJSON(schemaPath);
    const ingress = readFileSync(ingressClassifierPath, 'utf8');
    const manager = readFileSync(managerRoutingPath, 'utf8');
    const startup = readFileSync(managerStartupPath, 'utf8');
    const discoveryContract = readFileSync(managerDiscoveryContractPath, 'utf8');

    expect(fixture.fixedBootstrapDiscoveryPath).toBe('/.well-known/cpamp-bootstrap');
    expect(fixture.canonicalPublicApiV2Suffix).toBe('/api/v2');
    expect(schema.properties.fixedBootstrapDiscoveryPath.const).toBe(
      fixture.fixedBootstrapDiscoveryPath
    );
    expect(schema.properties.canonicalPublicApiV2Suffix.const).toBe(
      fixture.canonicalPublicApiV2Suffix
    );
    expect(ingress).toContain(
      `FixedBootstrapDiscoveryPath = "${fixture.fixedBootstrapDiscoveryPath}"`
    );
    expect(manager).toContain(
      'BootstrapDiscoveryPath = bootstrapdomain.FixedBootstrapDiscoveryPath'
    );
    expect(manager).toContain(`PublicAPIV2Suffix = "${fixture.canonicalPublicApiV2Suffix}"`);
    expect(startup).toContain('routingdomain.ProtectFixedDiscovery(');
    expect(discoveryContract).toContain(
      'PublicDiscoveryContractVersion = "cpamp.phase4-01.discovery.v1"'
    );
    expect(discoveryContract).toContain('FixedBootstrapDiscoveryPath');
    expect(discoveryContract).toContain(fixture.fixedBootstrapDiscoveryPath);
    expect(fixture.routes.filter((route) => route.id === 'bootstrap_discovery')).toHaveLength(1);
    expect(fixture.routes.filter((route) => route.id === 'public_api_v2_namespace')).toHaveLength(
      1
    );
    expect(fixture.routes.filter((route) => route.id === 'retired_control_namespace')).toHaveLength(
      1
    );
  });

  it('keeps the route-owner ledger strict and non-overlapping', () => {
    const fixture = readJSON(fixturePath);
    const expected = {
      fixed_brand_resources: ['exact_fixed_brand_get_head', 'manager', 'deny'],
      bootstrap_discovery: ['exact_fixed_path', 'manager', 'deny'],
      public_api_v2_namespace: ['verified_effective_segment_prefix', 'manager', 'deny'],
      retired_control_namespace: ['previous_verified_segment_prefix', 'manager', 'deny'],
      legacy_manager_routes: ['existing_fixed_or_segment_prefix', 'manager', 'deny'],
      gateway_model_protocol: ['existing_data_plane_protocol', 'gateway', 'preserve'],
      unknown_non_control_path: ['outside_control_namespaces', 'gateway', 'preserve'],
    };

    expect(fixture.routes).toHaveLength(Object.keys(expected).length);
    expect(new Set(fixture.routes.map((route) => route.id)).size).toBe(fixture.routes.length);
    expect(new Set(fixture.routes.map((route) => route.match)).size).toBe(fixture.routes.length);
    expect([...fixture.routes.map((route) => route.id)].sort()).toEqual(
      Object.keys(expected).sort()
    );

    for (const route of fixture.routes) {
      expect([route.match, route.owner, route.gatewayFallback]).toEqual(expected[route.id]);
    }
  });

  it('keeps routing authority and credential decisions separate', () => {
    const fixture = readJSON(fixturePath);
    const discovery = fixture.routes.find((route) => route.id === 'bootstrap_discovery');
    const publicAPI = fixture.routes.find((route) => route.id === 'public_api_v2_namespace');
    const retired = fixture.routes.find((route) => route.id === 'retired_control_namespace');
    const gateway = fixture.routes.find((route) => route.id === 'unknown_non_control_path');

    expect(discovery.activeMethods).toEqual(['GET', 'OPTIONS']);
    expect(discovery.unsupportedMethods).toBe('manager_local_rejection');
    expect(discovery.completedBehavior).toBe('generic_404');
    expect(publicAPI.authority).toBe('verified_effective_snapshot_only');
    expect(publicAPI.noAuthorityBehavior).toBe('inactive_fail_closed');
    expect(retired.behavior).toBe('deny_only');
    expect(retired.gatewayFallback).toBe('deny');
    expect(gateway.owner).toBe('gateway');
    expect(gateway.credentialHeaders).toBe('do_not_classify');
    expect(fixture.normalization.credentialRouting).toBe('route_ownership_precedes_credentials');
  });

  it('keeps the discovery carrier connected to the Manager-owned read-only path', () => {
    const startup = readFileSync(managerStartupPath, 'utf8');
    expect(startup).toContain('http.HandlerFunc(h.serveDiscovery)');
    expect(startup).toContain('bootstrapdiscovery.Handler{Discovery: h.projection}');
    expect(startup).toMatch(/discovery\s+routingdomain\.DiscoveryHandler/);
    expect(startup).toContain('h.lifecycle.Store(routingdomain.DiscoveryActive)');
    expect(startup).toContain('h.lifecycle.Store(routingdomain.DiscoveryCompleted)');
  });
});
