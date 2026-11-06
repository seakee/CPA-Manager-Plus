#!/usr/bin/env node
import { createHash } from 'node:crypto';
import {
  constants,
  openSync,
  closeSync,
  readSync,
  fstatSync,
  lstatSync,
  realpathSync,
} from 'node:fs';
import path from 'node:path';
import { pathToFileURL } from 'node:url';

const MiB = 1024 * 1024;
const CONTRACT = 'cpamp-cpa-v8-qualification';
const ID = /^[A-Za-z0-9][A-Za-z0-9._-]{0,95}$/;
const SHA = /^[a-f0-9]{40}$/;
const DIGEST = /^[a-f0-9]{64}$/;
const CHECKS =
  'MANIFEST_MAPPING SURFACE_POLICY AUTH_BOUNDARY BODY_SHAPE POSTCONDITION FRESHNESS FAILURE_BEHAVIOR COVERAGE RECOVERY_BOUNDARY PROVENANCE PRIVACY EVIDENCE_BINDING'.split(
    ' '
  );
const REASONS =
  'NONE NOT_RUN NOT_IMPLEMENTED OPTIONAL_DISABLED SETUP_FAILED TIMEOUT CANCELLED INPUT_LIMIT AUTH_REJECTED TARGET_UNREACHABLE TLS_INVALID REDIRECT_BLOCKED CAPABILITY_MISSING BODY_SCHEMA_MISMATCH ASSERTION_FAILED COVERAGE_UNKNOWN CONFLICT_RECOVERY_UNKNOWN CLEANUP_FAILED'.split(
    ' '
  );
const OBSERVATIONS =
  'NONE EXPECTED_MATCH EXPECTED_MISMATCH SHAPE_VALID SHAPE_INVALID VALUE_PRESENT VALUE_ABSENT VALUE_UNKNOWN SAFE_DENIAL STATE_CHANGED STATE_UNCHANGED EFFECT_UNKNOWN GAP_OBSERVED COVERAGE_UNKNOWN'.split(
    ' '
  );
const OUTCOMES = ['pass', 'fail', 'error', 'not_run'];
const ERRORS = new Set(
  'USAGE INPUT_INVALID INPUT_LIMIT NON_CANONICAL SCHEMA_VERSION FIELD_REJECTED ID_OR_REF_INVALID COVERAGE_INVALID SURFACE_POLICY_INVALID ARTIFACT_BINDING_INVALID RESULT_STATE_INVALID EVIDENCE_INVALID PRIVACY_REJECTED SIDE_EFFECT_REJECTED'.split(
    ' '
  )
);

class Invalid extends Error {
  constructor(code) {
    super(code);
    this.code = code;
  }
}
const requireThat = (condition, code) => {
  if (!condition) throw new Invalid(code);
};
const equal = (a, b) => JSON.stringify(a) === JSON.stringify(b);
const sameFields = (a, b) => Object.keys(b).every((key) => a[key] === b[key]);
const digest = (bytes) => createHash('sha256').update(bytes).digest('hex');
const fields = (value, keys, code = 'FIELD_REJECTED') => {
  requireThat(value !== null && typeof value === 'object' && !Array.isArray(value), code);
  requireThat(
    Object.keys(value).length === keys.length && keys.every((k) => Object.hasOwn(value, k)),
    code
  );
};
const list = (value, max = 256) => {
  requireThat(Array.isArray(value), 'FIELD_REJECTED');
  requireThat(value.length <= max, 'INPUT_LIMIT');
};
const member = (value, values, code = 'FIELD_REJECTED') =>
  requireThat(values.includes(value), code);
const identifier = (value) =>
  requireThat(typeof value === 'string' && ID.test(value), 'ID_OR_REF_INVALID');
const ids = (items, key = 'id') => {
  const map = new Map();
  for (const item of items) {
    identifier(item[key]);
    requireThat(!map.has(item[key]), 'ID_OR_REF_INVALID');
    map.set(item[key], item);
  }
  return map;
};
const refs = (values, targets, max = 256) => {
  list(values, max);
  const seen = new Set();
  for (const value of values) {
    identifier(value);
    requireThat(targets.has(value) && !seen.has(value), 'ID_OR_REF_INVALID');
    seen.add(value);
  }
  return seen;
};
const sameSet = (left, right) =>
  left.length === right.length && left.every((v) => right.includes(v));

// Fixed r1 source-observed surfaces, not a URL dispatcher or compatibility adapter.
// New upstream paths, candidate identities or fixture inputs require a reviewed revision.
const management = (method, suffix, effect = 'read-only', probeEligible = false) => ({
  transport: 'http',
  ownerClass: 'cpa-core-management',
  method,
  pathTemplate: '/v8/management' + suffix,
  effect,
  authClass: 'management',
  probeEligible,
});
const fixtureSurface = (effect) => ({
  transport: 'fixture',
  ownerClass: 'contract-fixture',
  method: null,
  pathTemplate: null,
  effect,
  authClass: 'fixture-only',
  probeEligible: false,
});
const configRead = () => management('GET', '/config', 'read-only', true);
const surfaceDefinitions = {
  'AUTH-01': [configRead()],
  'AUTH-02': [
    {
      transport: 'http',
      ownerClass: 'cpa-data-plane',
      method: 'POST',
      pathTemplate: '/v1/chat/completions',
      effect: 'model-request',
      authClass: 'client-key',
      probeEligible: false,
    },
  ],
  'CFG-01': [configRead()],
  'CFG-02': [
    management('PUT', '/config', 'mutating'),
    management('PATCH', '/config', 'mutating'),
    configRead(),
  ],
  'KEY-01': [management('GET', '/config/access/api-keys')],
  'KEY-02': [
    management('PUT', '/config/access/api-keys', 'mutating'),
    management('PATCH', '/config/access/api-keys', 'mutating'),
    management('DELETE', '/config/access/api-keys', 'mutating'),
    management('GET', '/config/access/api-keys'),
  ],
  'CRED-01': [management('GET', '/credentials')],
  'CRED-02': [
    management('POST', '/credentials', 'mutating'),
    management('DELETE', '/credentials', 'mutating'),
    management('PATCH', '/credentials/status', 'mutating'),
    management('PATCH', '/credentials/fields', 'mutating'),
    management('POST', '/credentials/refresh', 'mutating'),
    management('GET', '/credentials'),
  ],
  'OAUTH-01': [
    management('GET', '/oauth/auth-url', 'flow-start'),
    management('GET', '/oauth/status'),
    ...['GET', 'POST'].map((method) => ({
      ...management(method, '/oauth/callback', 'mutating'),
      authClass: 'oauth-state',
    })),
    management('DELETE', '/oauth/session', 'mutating'),
  ],
  'MODEL-01': [
    management('GET', '/credentials/models'),
    management('GET', '/routing/model-definitions/:channel'),
  ],
  'USAGE-01': [configRead(), management('PATCH', '/config', 'mutating')],
  'USAGE-02': [management('GET', '/observability/usage/queue', 'consuming')],
  'STAGE-01': [fixtureSurface('staging')],
  'EXT-GUARD-01': [fixtureSurface('guard-only')],
  'PLUGIN-CORE-01': [
    management('GET', '/plugins'),
    management('DELETE', '/plugins/:id', 'mutating'),
    management('GET', '/plugins/store'),
    management('POST', '/plugins/store/:id/install', 'mutating'),
    management('GET', '/plugins/:id/quota'),
    management('POST', '/plugins/:id/quota', 'mutating'),
    management('DELETE', '/plugins/:id/quota', 'mutating'),
  ],
  'EXT-ENABLE-01': [],
};
const OPERATIONS = Object.entries(surfaceDefinitions).map(([id, surfaces]) => ({
  id,
  kind: id === 'EXT-GUARD-01' ? 'guard' : 'function',
  surfaces: surfaces.map((surface, i) => ({ id: id + '-S' + (i + 1), ...surface })),
}));
const profileDefinitions = [
  [
    'CPA8-MGMT-MIN-r1',
    true,
    [
      'AUTH-01',
      'AUTH-02',
      'CFG-01',
      'CFG-02',
      'KEY-01',
      'KEY-02',
      'CRED-01',
      'CRED-02',
      'OAUTH-01',
      'MODEL-01',
    ],
  ],
  ['CPA8-USAGE-MIN-r1', true, ['USAGE-01', 'USAGE-02']],
  ['CPA8-STAGE-r1', true, ['STAGE-01']],
  ['CPA8-EXTERNAL-SAFETY-r1', true, ['AUTH-01', 'CFG-01']],
  ['CPA8-EXTENSION-GUARD-r1', true, ['EXT-GUARD-01']],
  ['CPA8-PLUGIN-CORE-r1', false, ['PLUGIN-CORE-01']],
  ['CPA8-EXTENSION-ENABLE-r1', false, ['EXT-ENABLE-01', 'EXT-GUARD-01']],
];
const PROFILES = profileDefinitions.map(([id, selected, requiredOperationRefs]) => ({
  id,
  revision: 'r1',
  selected,
  requiredOperationRefs,
  optionalOperationRefs: [],
}));
const FIXTURES = [
  [
    'CPA8-UPSTREAM-r1',
    'upstream-real',
    'upstream-function',
    'isolated-management-r1',
    'isolated-loopback-r1',
  ],
  [
    'CPA8-STAGE-r1',
    'upstream-real',
    'upstream-function',
    'synthetic-source-r1',
    'isolated-staging-r1',
  ],
  [
    'CPA8-HARNESS-r1',
    'local-stub',
    'harness-behavior',
    'synthetic-http-r1',
    'isolated-loopback-r1',
  ],
  ['CPA8-GUARD-r1', 'boundary-deny', 'boundary-guard', 'synthetic-guard-r1', 'offline-r1'],
].map(([id, kind, proofScope, configProfileId, modeProfileId]) => ({
  id,
  revision: 'r1',
  kind,
  proofScope,
  lifecycle: 'declared',
  configProfileId,
  modeProfileId,
}));
const PLAN_INPUT = {
  planCommit: '85d4f9942049fb3fdbf8358a5d8a9d7624f110bd',
  gateBlobSha: '779860b2126a95b1878af6645497e95419ce1775',
  scopeRevision: 'r1',
};
const BASE = 'f5d12c6ca4af1f36f087e8acd28e565c5a0ae655';
const CANDIDATE = {
  id: 'CPA8-8.0.55-linux-amd64-plugin',
  version: 'v8.0.55',
  sourceCommit: 'e6e5708e677adb2463d45accdd24bc9fd028ddd8',
  os: 'linux',
  arch: 'amd64',
  variant: 'plugin',
  assetName: 'CLIProxyAPI_8.0.55_linux_amd64.tar.gz',
  declaredSha256: '4cd593251db85e50c820f42e6ea5cecdd05f902201d3dbdb025671d4f306a123',
  provenance: 'declared',
};

// Return fresh data, so importing callers cannot mutate the trusted catalogue.
export function createSeedManifest() {
  return structuredClone({
    schemaVersion: 1,
    contractId: CONTRACT,
    revision: 'r1',
    planInput: PLAN_INPUT,
    cpampBaselineCommit: BASE,
    candidate: CANDIDATE,
    operations: OPERATIONS,
    profiles: PROFILES,
    fixtures: FIXTURES,
    cases: OPERATIONS.map((operation) => {
      const fixtureRef =
        operation.kind === 'guard'
          ? 'CPA8-GUARD-r1'
          : operation.id === 'STAGE-01'
            ? 'CPA8-STAGE-r1'
            : 'CPA8-UPSTREAM-r1';
      return {
        id: 'AQ01-' + operation.id,
        operationRef: operation.id,
        profileRefs: PROFILES.filter((p) => p.requiredOperationRefs.includes(operation.id)).map(
          (p) => p.id
        ),
        surfaceRefs: operation.surfaces.map((s) => s.id),
        fixtureRef,
        proofScope: operation.kind === 'guard' ? 'boundary-guard' : 'upstream-function',
        expectations: [{ id: operation.id + '-DECLARED', mandatory: true, checkCode: 'COVERAGE' }],
        budgetRef: 'standard-r1',
      };
    }),
  });
}

// r2 freezes leaf inputs before execution. The runner dispatches these IDs only;
// no URL, executable, credential or payload is supplied by the manifest.
const MANAGEMENT_GROUPS = [
  ['AUTH-01', 'bearer'],
  ['AUTH-01', 'header bearer-precedence'],
  ['AUTH-01', 'missing wrong'],
  ['AUTH-01', 'ban'],
  [
    'AUTH-01',
    'client-as-management upstream-as-management management-as-client upstream-as-client',
  ],
  ['AUTH-01', 'no-secret env-secret'],
  ['AUTH-02', 'client'],
  ['AUTH-02', 'missing wrong provider-error'],
  ['AUTH-02', 'empty absent null'],
  ['AUTH-02', 'rotate remove'],
  ['CFG-01', 'root subtree yaml'],
  ['CFG-01', 'read-only'],
  ['CFG-02', 'root-put'],
  ['CFG-02', 'subtree-put map-patch list-patch'],
  ['CFG-02', 'delete absent null empty false zero invalid-type'],
  ['CFG-02', 'readonly invalid-path invalid-body'],
  ['CFG-02', 'yaml-put response-loss'],
  ['KEY-01', 'populated empty'],
  ['KEY-02', 'put patch delete'],
  ['KEY-02', 'invalid duplicate write-failure response-loss'],
  ['CRED-01', 'empty populated'],
  ['CRED-02', 'raw multipart invalid duplicate partial'],
  ['CRED-02', 'delete missing unsafe'],
  ['CRED-02', 'status fields invalid readonly conflict'],
  ['CRED-02', 'missing invalid refresh-error real-refresh'],
  ['OAUTH-01', 'start missing-provider unknown-provider'],
  ['OAUTH-01', 'pending unknown auth'],
  ['OAUTH-01', 'get-missing post-missing invalid unknown mismatch'],
  ['OAUTH-01', 'error-callback'],
  ['OAUTH-01', 'cancel auth replay missing'],
  ['OAUTH-01', 'repeated race'],
  ['OAUTH-01', 'natural-expiry'],
  ['OAUTH-01', 'real-exchange'],
  ['MODEL-01', 'registered missing unknown'],
  ['MODEL-01', 'codex unknown'],
  ['MODEL-01', 'model-reload'],
];

export function createManagementManifest() {
  const m = createSeedManifest();
  m.revision = 'r2';
  m.planInput = {
    planCommit: 'b654d625902c0e810f9bfc581aa17c35f4b0cff0',
    gateBlobSha: 'd5367e63d4feda5854d0639746c18d6a78e0b274',
    scopeRevision: 'r2',
  };
  // The prerequisite head is distinct from the implementation's v2 merge base.
  m.cpampBaselineCommit = '32ba418c2a5f09aaaba0f887a8d429da91df2fbe';
  const add = (op, surface) => {
    const item = m.operations.find((o) => o.id === op);
    item.surfaces.push({ id: op + '-S' + (item.surfaces.length + 1), ...surface });
  };
  for (const op of ['CFG-01', 'CFG-02']) {
    add(op, management('GET', '/config/:path'));
    add(op, management('GET', '/config.yaml'));
  }
  for (const method of ['PUT', 'PATCH', 'DELETE'])
    add('CFG-02', management(method, '/config/:path', 'mutating'));
  add('CFG-02', management('PUT', '/config.yaml', 'mutating'));
  for (const id of ['CPA8-MANAGEMENT-r2', 'CPA8-OAUTH-EXPIRY-r2'])
    m.fixtures.push({
      id,
      revision: 'r2',
      kind: 'upstream-real',
      proofScope: 'upstream-function',
      lifecycle: 'declared',
      configProfileId:
        id === 'CPA8-MANAGEMENT-r2' ? 'management-per-case-r2' : 'codex-natural-expiry-r2',
      modeProfileId: 'linux-amd64-network-none-r2',
    });
  m.cases = m.cases.filter((c) => !MANAGEMENT_GROUPS.some(([op]) => op === c.operationRef));
  MANAGEMENT_GROUPS.forEach(([operationRef, variants], index) => {
    for (const variant of variants.split(' ')) {
      const id = `AQ02-M${String(index + 1).padStart(2, '0')}-${variant}`;
      const long = index === 31;
      const checks = long
        ? ['PENDING', 'ERROR', 'EXPIRY', 'NO-CREDENTIAL']
        : ['HTTP', 'STATE', 'CONTROL'];
      m.cases.push({
        id,
        operationRef,
        profileRefs: m.profiles
          .filter((p) => p.requiredOperationRefs.includes(operationRef))
          .map((p) => p.id),
        surfaceRefs: m.operations.find((o) => o.id === operationRef).surfaces.map((s) => s.id),
        fixtureRef: long ? 'CPA8-OAUTH-EXPIRY-r2' : 'CPA8-MANAGEMENT-r2',
        proofScope: 'upstream-function',
        expectations: checks.map((check, i) => ({
          id: id + '-' + check,
          mandatory: true,
          checkCode: i === 0 ? 'BODY_SHAPE' : 'POSTCONDITION',
        })),
        budgetRef: long ? 'oauth-expiry-r2' : 'standard-r1',
      });
    }
  });
  return m;
}

export const EXTERNAL_GROUPS = [
  ['AUTH-01', 'upstream-function', 'real-auth'],
  ['CFG-01', 'upstream-function', 'real-config'],
  ['CFG-01', 'harness-behavior', 'root prefix'],
  ['AUTH-01', 'boundary-guard', 'private remote'],
  ['AUTH-01', 'harness-behavior', 'ipv6'],
  ['CFG-01', 'boundary-guard', 'effects'],
  ['CFG-01', 'boundary-guard', 'methods-routes'],
  ['AUTH-01', 'boundary-guard', 'raw-url'],
  ['AUTH-01', 'boundary-guard', 'direct proxy'],
  ['AUTH-01', 'harness-behavior', 'untrusted'],
  ['AUTH-01', 'harness-behavior', 'expired'],
  ['AUTH-01', 'harness-behavior', 'identity'],
  ['AUTH-01', 'boundary-guard', 'same-origin'],
  ['AUTH-01', 'boundary-guard', 'cross-origin downgrade userinfo'],
  ['AUTH-01', 'upstream-function', 'real-missing real-wrong'],
  ['AUTH-01', 'harness-behavior', 'dns refused'],
  ['CFG-01', 'harness-behavior', 'dns tls headers body'],
  ['CFG-01', 'harness-behavior', 'headers'],
  ['CFG-01', 'harness-behavior', 'encoded gzip'],
  ['CFG-01', 'harness-behavior', 'encoding gzip utf8 json truncated shape'],
  ['AUTH-01', 'boundary-guard', 'pin'],
  ['AUTH-01', 'boundary-guard', 'rebind'],
  ['AUTH-01', 'boundary-guard', 'environment'],
  ['CFG-01', 'harness-behavior', 'reported'],
  ['CFG-01', 'harness-behavior', 'projection'],
  ['CFG-01', 'boundary-guard', 'aba'],
  ['AUTH-01', 'boundary-guard', 'scope'],
  ['CFG-01', 'harness-behavior', 'cancel'],
];

export function createExternalManifest() {
  const m = createManagementManifest();
  m.revision = 'r3';
  m.planInput.scopeRevision = 'r3';
  m.cpampBaselineCommit = 'bda686bcdf93a1dcb1e71a3d8d6047323a0224e8';
  // Keep prior manifests immutable. Other operations are pending declarations,
  // never copies of r2 execution or substitutes for its original leaf coverage.
  m.cases = createSeedManifest().cases.filter(
    (c) => !['AUTH-01', 'CFG-01'].includes(c.operationRef)
  );
  for (const [kind, proofScope] of [
    ['upstream-real', 'upstream-function'],
    ['local-stub', 'harness-behavior'],
    ['boundary-deny', 'boundary-guard'],
  ]) {
    m.fixtures.push({
      id: 'CPA8-EXTERNAL-' + kind + '-r3',
      revision: 'r3',
      kind,
      proofScope,
      lifecycle: 'declared',
      configProfileId: 'external-owned-r3',
      modeProfileId: 'linux-amd64-network-none-r3',
    });
  }
  for (const id of ['AUTH-01', 'CFG-01']) {
    const op = m.operations.find((o) => o.id === id);
    op.surfaces.push({ id: id + '-EXTERNAL-r3', ...fixtureSurface('read-only') });
  }
  const add = (number, operationRef, proofScope, variant) => {
    const id = `AQ03-E${String(number).padStart(2, '0')}-${variant}`;
    m.cases.push({
      id,
      operationRef,
      profileRefs: m.profiles
        .filter((p) => p.requiredOperationRefs.includes(operationRef))
        .map((p) => p.id),
      surfaceRefs: [
        proofScope === 'upstream-function' ? operationRef + '-S1' : operationRef + '-EXTERNAL-r3',
      ],
      fixtureRef: m.fixtures.find(
        (f) => f.id.startsWith('CPA8-EXTERNAL-') && f.proofScope === proofScope
      ).id,
      proofScope,
      expectations: ['HTTP', 'STATE', 'CONTROL'].map((suffix) => ({
        id: id + '-' + suffix,
        mandatory: true,
        checkCode: suffix === 'HTTP' ? 'FAILURE_BEHAVIOR' : 'POSTCONDITION',
      })),
      budgetRef: 'standard-r1',
    });
  };
  EXTERNAL_GROUPS.forEach(([op, proof, variants], index) =>
    variants.split(' ').forEach((variant) => add(index + 1, op, proof, variant))
  );
  add(1, 'CFG-01', 'upstream-function', 'real-config');
  add(2, 'AUTH-01', 'upstream-function', 'real-auth');
  add(2, 'CFG-01', 'harness-behavior', 'mediation');
  add(5, 'CFG-01', 'harness-behavior', 'ipv6-config');
  for (const variant of ['denied', 'forbidden']) add(15, 'AUTH-01', 'harness-behavior', variant);
  return m;
}

// AQ-04 has scope-local leaves. r1-r3 remain immutable evidence inputs.
export const USAGE_STAGE_GROUPS = [
  ['U01', 'USAGE-01', 'upstream-function', 'config'],
  ['U02', 'USAGE-01', 'upstream-function', 'enable'],
  ['U02', 'USAGE-02', 'upstream-function', 'receipt'],
  ['U03', 'USAGE-02', 'upstream-function', 'batch'],
  ['U04', 'USAGE-01', 'upstream-function', 'disable'],
  ['U04', 'USAGE-02', 'upstream-function', 'backlog'],
  ['U05', 'USAGE-01', 'upstream-function', 'availability'],
  ['U05', 'USAGE-02', 'upstream-function', 'clear'],
  ['U06', 'USAGE-02', 'upstream-function', 'five-seconds'],
  ['U07', 'USAGE-02', 'upstream-function', 'sixty-seconds'],
  ['U08', 'USAGE-01', 'upstream-function', 'bounds'],
  ['U09', 'USAGE-02', 'upstream-function', 'pop'],
  ['U09', 'USAGE-02', 'harness-behavior', 'relay'],
  ['U10', 'USAGE-02', 'harness-behavior', 'unknown-cutpoint'],
  ['U11', 'USAGE-02', 'harness-behavior', 'projection'],
  ['U12', 'USAGE-02', 'upstream-function', 'graceful'],
  ['U13', 'USAGE-02', 'upstream-function', 'forced'],
  ['U14', 'USAGE-02', 'upstream-function', 'competition'],
  ['U15', 'USAGE-02', 'upstream-function', 'subscribe'],
  ['U16', 'USAGE-02', 'upstream-function', 'fanout'],
  ['U17', 'USAGE-02', 'upstream-function', 'pressure'],
  ['U17', 'USAGE-02', 'harness-behavior', 'overflow-unknown'],
  ['U18', 'USAGE-02', 'harness-behavior', 'binding'],
  ['S01', 'STAGE-01', 'harness-behavior', 'snapshot'],
  ['S02', 'STAGE-01', 'upstream-function', 'legacy'],
  ['S03', 'STAGE-01', 'upstream-function', 'plaintext'],
  ['S04', 'STAGE-01', 'upstream-function', 'mixed'],
  ['S05', 'STAGE-01', 'upstream-function', 'v8'],
  ['S06', 'STAGE-01', 'upstream-function', 'paths'],
  ['S07', 'STAGE-01', 'upstream-function', 'invalid'],
  ['S08', 'STAGE-01', 'upstream-function', 'readonly'],
  ['S09', 'STAGE-01', 'upstream-function', 'commit'],
  ['S10', 'STAGE-01', 'upstream-function', 'rejected'],
  ['S11', 'STAGE-01', 'upstream-function', 'reconcile'],
  ['S11', 'STAGE-01', 'harness-behavior', 'lost-reply'],
  ['S12', 'STAGE-01', 'harness-behavior', 'precommit'],
  ['S13', 'STAGE-01', 'harness-behavior', 'restore'],
  ['S14', 'STAGE-01', 'boundary-guard', 'fence'],
  ['S15', 'STAGE-01', 'boundary-guard', 'tamper'],
  ['S16', 'STAGE-01', 'harness-behavior', 'crash'],
];

export function createUsageStageManifest() {
  const m = createExternalManifest();
  m.revision = m.planInput.scopeRevision = 'r4';
  m.cpampBaselineCommit = '4343c7aa597fa133aa112d45dcec5ca07e8dd33a';
  const ops = ['USAGE-01', 'USAGE-02', 'STAGE-01'];
  m.cases = createSeedManifest().cases.filter((c) => !ops.includes(c.operationRef));
  for (const [kind, proofScope] of [
    ['upstream-real', 'upstream-function'],
    ['local-stub', 'harness-behavior'],
    ['boundary-deny', 'boundary-guard'],
  ])
    m.fixtures.push({
      id: `CPA8-USAGE-STAGE-${kind}-r4`,
      revision: 'r4',
      kind,
      proofScope,
      lifecycle: 'declared',
      configProfileId: 'usage-stage-owned-r4',
      modeProfileId: 'linux-amd64-network-none-r4',
    });
  for (const id of ops)
    m.operations
      .find((o) => o.id === id)
      .surfaces.push({
        id: `${id}-FIXTURE-r4`,
        ...fixtureSurface('read-only'),
      });
  const leaves = [
    ...USAGE_STAGE_GROUPS,
    ['P01', 'USAGE-02', 'upstream-function', 'max-natural-retention'],
    ['P02', 'USAGE-02', 'upstream-function', 'observed-subscriber-overflow'],
  ];
  for (const [group, operationRef, proofScope, variant] of leaves) {
    const id = `AQ04-${group}-${variant}`;
    m.cases.push({
      id,
      operationRef,
      profileRefs: m.profiles
        .filter((p) => p.requiredOperationRefs.includes(operationRef))
        .map((p) => p.id),
      surfaceRefs: [
        proofScope === 'upstream-function' ? `${operationRef}-S1` : `${operationRef}-FIXTURE-r4`,
      ],
      fixtureRef: m.fixtures.find(
        (f) => f.id.startsWith('CPA8-USAGE-STAGE-') && f.proofScope === proofScope
      ).id,
      proofScope,
      expectations: ['OBSERVATION', 'STATE', 'CONTROL'].map((suffix) => ({
        id: `${id}-${suffix}`,
        mandatory: true,
        checkCode: 'POSTCONDITION',
      })),
      budgetRef: 'standard-r1',
    });
  }
  return m;
}

// Fixed inert guard recipes. These paths never become upstream surfaces.
export const EXTENSION_GROUPS = [
  ['protected'],
  ['public'],
  ['unknown', 'absent'],
  ['menu', 'header', 'list', 'hash'],
  ['core', 'custom', 'unknown-v0'],
  ['duplicate', 'same-plugin', 'core-collision'],
  ['plugin', 'resource'],
  ['wildcard', 'prefix', 'host'],
  ['POST', 'HEAD', 'OPTIONS', 'override'],
  ['dot', 'dot-encoded', 'slash-encoded', 'double-encoded', 'encoded-letter'],
  ['backslash', 'duplicate-slash', 'trailing-slash'],
  ['absolute', 'userinfo', 'fragment', 'query', 'control'],
  ['missing', 'unknown', 'capability', 'public-mutation'],
  ['disabled', 'uninstalled', 'revoked'],
  ['manifest', 'source', 'version'],
  ['connection', 'credential', 'profile'],
  ['queued', 'late', 'queued-aba', 'late-aba'],
  ['protected-headers'],
  ['public-headers'],
  ['301', '302', '303', '307', '308', 'cookie', 'location'],
  ['html', 'javascript', 'mime', 'unsafe-header'],
  ['json', 'text', 'body-limit', 'header-limit', 'status', 'shape', 'invalid-json'],
  ['exception', 'stale-grant', 'policy', 'partial-registry'],
  ['native', 'browser'],
];

export function createExtensionManifest() {
  const m = createUsageStageManifest();
  m.revision = m.planInput.scopeRevision = 'r5';
  m.cpampBaselineCommit = 'aacf002b9b80bad3fbebb84f5478ca89cd0ff4ef';
  m.cases = createSeedManifest().cases.filter((c) => c.operationRef !== 'EXT-GUARD-01');
  for (const [kind, proofScope] of [
    ['boundary-deny', 'boundary-guard'],
    ['local-stub', 'harness-behavior'],
  ])
    m.fixtures.push({
      id: `CPA8-EXTENSION-${kind}-r5`,
      revision: 'r5',
      kind,
      proofScope,
      lifecycle: 'declared',
      configProfileId: 'inert-extension-r5',
      modeProfileId: 'offline-fixture-r5',
    });
  EXTENSION_GROUPS.forEach((variants, index) =>
    variants.forEach((variant) => {
      const id = `AQ05-G${String(index + 1).padStart(2, '0')}-${variant}`;
      const proofScope = index >= 19 && index <= 21 ? 'harness-behavior' : 'boundary-guard';
      m.cases.push({
        id,
        operationRef: 'EXT-GUARD-01',
        profileRefs: ['CPA8-EXTENSION-GUARD-r1'],
        surfaceRefs: ['EXT-GUARD-01-S1'],
        fixtureRef: m.fixtures.find(
          (f) => f.id.startsWith('CPA8-EXTENSION-') && f.proofScope === proofScope
        ).id,
        proofScope,
        expectations: ['DECISION', 'DISPATCH', 'DELIVERY'].map((suffix) => ({
          id: `${id}-${suffix}`,
          mandatory: true,
          checkCode: 'POSTCONDITION',
        })),
        budgetRef: 'standard-r1',
      });
    })
  );
  return m;
}

const stamp = (stat) => [stat.dev, stat.ino, stat.mode, stat.size, stat.mtimeMs, stat.ctimeMs];
const sameFile = (a, b) => equal(stamp(a), stamp(b));
function boundedRead(filename, limit, code = 'INPUT_INVALID') {
  let fd;
  try {
    const before = lstatSync(filename);
    requireThat(before.isFile() && !before.isSymbolicLink(), code);
    fd = openSync(filename, constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK);
    const opened = fstatSync(fd);
    requireThat(sameFile(before, opened), code);
    const buffer = Buffer.alloc(limit + 1);
    let length = 0;
    while (length < buffer.length) {
      const count = readSync(fd, buffer, length, Math.min(65536, buffer.length - length), null);
      if (count === 0) break;
      length += count;
    }
    requireThat(length <= limit, 'INPUT_LIMIT');
    requireThat(sameFile(opened, fstatSync(fd)) && sameFile(opened, lstatSync(filename)), code);
    return buffer.subarray(0, length);
  } catch (error) {
    if (error instanceof Invalid) throw error;
    throw new Invalid(code);
  } finally {
    if (fd !== undefined) closeSync(fd);
  }
}

function parseCanonical(bytes) {
  let data;
  try {
    requireThat(!(bytes[0] === 0xef && bytes[1] === 0xbb && bytes[2] === 0xbf), 'INPUT_INVALID');
    data = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(bytes));
  } catch {
    throw new Invalid('INPUT_INVALID');
  }
  const stack = [[data, 0]];
  let nodes = 0;
  while (stack.length) {
    const [value, depth] = stack.pop();
    requireThat(depth <= 16 && ++nodes <= 65536, 'INPUT_LIMIT');
    if (typeof value === 'number')
      requireThat(Number.isSafeInteger(value) && value >= 0, 'NON_CANONICAL');
    if (value !== null && typeof value === 'object') {
      const children = Object.values(value);
      requireThat(children.length <= 256, 'INPUT_LIMIT');
      for (const child of children) stack.push([child, depth + 1]);
    }
  }
  // Round-trip comparison also rejects duplicate members and lossy number decoding.
  requireThat(bytes.equals(Buffer.from(JSON.stringify(data, null, 2) + '\n')), 'NON_CANONICAL');
  return data;
}

function validateManifest(m) {
  fields(m, [
    'schemaVersion',
    'contractId',
    'revision',
    'planInput',
    'cpampBaselineCommit',
    'candidate',
    'operations',
    'profiles',
    'fixtures',
    'cases',
  ]);
  requireThat(
    m.schemaVersion === 1 &&
      m.contractId === CONTRACT &&
      ['r1', 'r2', 'r3', 'r4', 'r5'].includes(m.revision),
    'SCHEMA_VERSION'
  );
  const trustedManifest =
    m.revision === 'r5'
      ? createExtensionManifest()
      : m.revision === 'r4'
        ? createUsageStageManifest()
        : m.revision === 'r3'
          ? createExternalManifest()
          : m.revision === 'r2'
            ? createManagementManifest()
            : createSeedManifest();
  fields(m.planInput, Object.keys(PLAN_INPUT));
  fields(m.candidate, Object.keys(CANDIDATE));
  for (const key of ['operations', 'profiles', 'fixtures', 'cases']) list(m[key]);
  for (const op of m.operations) {
    fields(op, ['id', 'kind', 'surfaces']);
    list(op.surfaces, 32);
    for (const surface of op.surfaces)
      fields(surface, [
        'id',
        'transport',
        'ownerClass',
        'method',
        'pathTemplate',
        'effect',
        'authClass',
        'probeEligible',
      ]);
  }
  for (const profile of m.profiles) {
    fields(profile, [
      'id',
      'revision',
      'selected',
      'requiredOperationRefs',
      'optionalOperationRefs',
    ]);
    requireThat(typeof profile.selected === 'boolean', 'FIELD_REJECTED');
  }
  for (const f of m.fixtures) fields(f, Object.keys(FIXTURES[0]));
  for (const c of m.cases) {
    fields(c, [
      'id',
      'operationRef',
      'profileRefs',
      'surfaceRefs',
      'fixtureRef',
      'proofScope',
      'expectations',
      'budgetRef',
    ]);
    list(c.expectations, 32);
    for (const e of c.expectations) {
      fields(e, ['id', 'mandatory', 'checkCode']);
      requireThat(typeof e.mandatory === 'boolean', 'FIELD_REJECTED');
      member(e.checkCode, CHECKS, 'PRIVACY_REJECTED');
    }
  }
  const operations = ids(m.operations),
    profiles = ids(m.profiles),
    fixtures = ids(m.fixtures),
    cases = ids(m.cases);
  const surfaces = ids(m.operations.flatMap((op) => op.surfaces));
  ids(m.cases.flatMap((c) => c.expectations));
  requireThat(
    [...operations.keys()].every((id) => Object.hasOwn(surfaceDefinitions, id)),
    'ID_OR_REF_INVALID'
  );
  requireThat(
    [...profiles.keys()].every((id) => PROFILES.some((p) => p.id === id)),
    'ID_OR_REF_INVALID'
  );
  requireThat(
    [...fixtures.keys()].every((id) => trustedManifest.fixtures.some((f) => f.id === id)),
    'ID_OR_REF_INVALID'
  );
  requireThat(
    operations.size === 16 &&
      profiles.size === 7 &&
      fixtures.size === trustedManifest.fixtures.length,
    'COVERAGE_INVALID'
  );
  for (const p of m.profiles) {
    refs(p.requiredOperationRefs, operations, 16);
    refs(p.optionalOperationRefs, operations, 16);
    const expected = PROFILES.find((profile) => profile.id === p.id);
    requireThat(p.revision === 'r1', 'SCHEMA_VERSION');
    requireThat(
      p.selected === expected.selected &&
        sameSet(p.requiredOperationRefs, expected.requiredOperationRefs) &&
        sameSet(p.optionalOperationRefs, expected.optionalOperationRefs),
      'COVERAGE_INVALID'
    );
  }
  for (const c of m.cases) {
    refs([c.operationRef], operations);
    refs([c.fixtureRef], fixtures);
    refs(c.profileRefs, profiles, 7);
    refs(c.surfaceRefs, surfaces, 32);
    const op = operations.get(c.operationRef);
    requireThat(
      c.surfaceRefs.every((s) => op.surfaces.some((surface) => surface.id === s)),
      'ID_OR_REF_INVALID'
    );
    requireThat(
      c.profileRefs.length > 0 &&
        c.profileRefs.every((id) => {
          const p = profiles.get(id);
          return [...p.requiredOperationRefs, ...p.optionalOperationRefs].includes(c.operationRef);
        }),
      'COVERAGE_INVALID'
    );
    requireThat(
      c.budgetRef ===
        (m.revision === 'r2' && c.id === 'AQ02-M32-natural-expiry'
          ? 'oauth-expiry-r2'
          : 'standard-r1'),
      'FIELD_REJECTED'
    );
    requireThat(c.proofScope === fixtures.get(c.fixtureRef).proofScope, 'ARTIFACT_BINDING_INVALID');
    requireThat(op.surfaces.length === 0 || c.surfaceRefs.length > 0, 'COVERAGE_INVALID');
  }
  for (const p of m.profiles.filter((p) => p.selected)) {
    for (const op of p.requiredOperationRefs) {
      requireThat(
        m.cases.some(
          (c) =>
            c.operationRef === op &&
            c.profileRefs.includes(p.id) &&
            c.expectations.some((e) => e.mandatory)
        ),
        'COVERAGE_INVALID'
      );
    }
  }
  for (const op of m.operations) {
    const expected = trustedManifest.operations.find((item) => item.id === op.id);
    requireThat(
      op.kind === expected.kind && op.surfaces.length === expected.surfaces.length,
      'SURFACE_POLICY_INVALID'
    );
    for (const s of op.surfaces) {
      const trusted = expected.surfaces.find((surface) => surface.id === s.id);
      requireThat(
        trusted && Object.keys(trusted).every((k) => s[k] === trusted[k]),
        'SURFACE_POLICY_INVALID'
      );
    }
  }
  requireThat(
    sameFields(m.planInput, trustedManifest.planInput) &&
      m.cpampBaselineCommit === trustedManifest.cpampBaselineCommit &&
      sameFields(m.candidate, CANDIDATE),
    'ARTIFACT_BINDING_INVALID'
  );
  for (const f of m.fixtures)
    requireThat(
      sameFields(
        f,
        trustedManifest.fixtures.find((item) => item.id === f.id)
      ),
      'ARTIFACT_BINDING_INVALID'
    );
  if (m.revision !== 'r1') requireThat(equal(m.cases, trustedManifest.cases), 'COVERAGE_INVALID');
  return { operations, profiles, fixtures, cases };
}

const timestamp = (value) => {
  requireThat(
    typeof value === 'string' && /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$/.test(value),
    'RESULT_STATE_INVALID'
  );
  const time = Date.parse(value);
  requireThat(
    Number.isFinite(time) && new Date(time).toISOString() === value,
    'RESULT_STATE_INVALID'
  );
  return time;
};

function validateReport(r, m, manifestBytes, catalogue) {
  const { cases, fixtures, operations } = catalogue;
  fields(r, [
    'schemaVersion',
    'contractId',
    'manifestRevision',
    'manifestSha256',
    'run',
    'artifactObservation',
    'results',
    'evidence',
  ]);
  requireThat(
    r.schemaVersion === 1 && r.contractId === CONTRACT && r.manifestRevision === m.revision,
    'SCHEMA_VERSION'
  );
  fields(r.run, [
    'id',
    'kind',
    'runnerCommit',
    'state',
    'selectedCaseRefs',
    'startedAt',
    'endedAt',
    'setupState',
    'cleanupState',
    'fixtureInputRefs',
  ]);
  fields(r.artifactObservation, [
    'candidateRef',
    'provenance',
    'observedArchiveSha256',
    'observedBinarySha256',
  ]);
  list(r.results);
  list(r.evidence);
  list(r.run.fixtureInputRefs, m.fixtures.length);
  for (const f of r.run.fixtureInputRefs)
    fields(f, ['fixtureRef', 'revision', 'configProfileId', 'modeProfileId']);
  for (const result of r.results) {
    fields(result, [
      'caseRef',
      'executionState',
      'assertionOutcome',
      'capability',
      'reasonCode',
      'assertions',
      'evidenceRefs',
    ]);
    list(result.assertions, 32);
    for (const a of result.assertions) fields(a, ['expectationRef', 'outcome', 'observationCode']);
  }
  for (const evidence of r.evidence) fields(evidence, ['id', 'kind', 'relativePath', 'sha256']);
  identifier(r.run.id);
  const selected = refs(r.run.selectedCaseRefs, cases);
  const results = ids(r.results, 'caseRef'),
    evidence = ids(r.evidence);
  requireThat(
    [...results.keys()].every((id) => selected.has(id)),
    'ID_OR_REF_INVALID'
  );
  const inputFixtures = ids(r.run.fixtureInputRefs, 'fixtureRef');
  const selectedFixtures = new Set([...selected].map((id) => cases.get(id).fixtureRef));
  requireThat(
    inputFixtures.size === selectedFixtures.size &&
      [...inputFixtures.keys()].every((id) => selectedFixtures.has(id)),
    'ARTIFACT_BINDING_INVALID'
  );
  for (const f of r.run.fixtureInputRefs) {
    const trusted = fixtures.get(f.fixtureRef);
    requireThat(
      ['revision', 'configProfileId', 'modeProfileId'].every((key) => f[key] === trusted[key]),
      'ARTIFACT_BINDING_INVALID'
    );
  }
  const usedEvidence = new Map();
  for (const result of r.results) {
    const c = cases.get(result.caseRef),
      expectations = ids(c.expectations);
    ids(result.assertions, 'expectationRef');
    refs(
      result.assertions.map((a) => a.expectationRef),
      expectations,
      32
    );
    refs(result.evidenceRefs, evidence);
    for (const id of result.evidenceRefs) {
      requireThat(!usedEvidence.has(id), 'ID_OR_REF_INVALID');
      usedEvidence.set(id, result.caseRef);
    }
  }
  requireThat(
    equal([...evidence.keys()].sort(), [...usedEvidence.keys()].sort()),
    'ID_OR_REF_INVALID'
  );
  requireThat(r.manifestSha256 === digest(manifestBytes), 'ARTIFACT_BINDING_INVALID');
  const artifact = r.artifactObservation;
  requireThat(artifact.candidateRef === m.candidate.id, 'ARTIFACT_BINDING_INVALID');
  member(artifact.provenance, ['not-verified', 'locally-verified'], 'ARTIFACT_BINDING_INVALID');
  const verified = artifact.provenance === 'locally-verified';
  requireThat(
    verified
      ? artifact.observedArchiveSha256 === m.candidate.declaredSha256 &&
          typeof artifact.observedBinarySha256 === 'string' &&
          DIGEST.test(artifact.observedBinarySha256)
      : artifact.observedArchiveSha256 === null && artifact.observedBinarySha256 === null,
    'ARTIFACT_BINDING_INVALID'
  );

  const run = r.run;
  member(run.kind, ['foundation-fixture', 'upstream-run'], 'RESULT_STATE_INVALID');
  member(run.state, ['not_run', 'completed', 'aborted'], 'RESULT_STATE_INVALID');
  member(run.setupState, ['not_run', 'ready', 'failed'], 'RESULT_STATE_INVALID');
  member(run.cleanupState, ['not_run', 'completed', 'failed'], 'RESULT_STATE_INVALID');
  requireThat(results.size === selected.size, 'RESULT_STATE_INVALID');
  if (run.state === 'not_run') {
    requireThat(
      run.runnerCommit === null &&
        run.startedAt === null &&
        run.endedAt === null &&
        run.setupState === 'not_run' &&
        run.cleanupState === 'not_run' &&
        !verified,
      'RESULT_STATE_INVALID'
    );
  } else {
    requireThat(
      typeof run.runnerCommit === 'string' && SHA.test(run.runnerCommit),
      'RESULT_STATE_INVALID'
    );
    requireThat(timestamp(run.endedAt) >= timestamp(run.startedAt), 'RESULT_STATE_INVALID');
    requireThat(
      run.setupState !== 'not_run' && run.cleanupState !== 'not_run',
      'RESULT_STATE_INVALID'
    );
    if (run.state === 'completed')
      requireThat(
        run.setupState === 'ready' && run.cleanupState === 'completed',
        'RESULT_STATE_INVALID'
      );
  }
  if (run.kind === 'foundation-fixture') requireThat(!verified, 'RESULT_STATE_INVALID');
  for (const result of r.results) {
    const c = cases.get(result.caseRef),
      f = fixtures.get(c.fixtureRef),
      op = operations.get(c.operationRef);
    member(result.executionState, ['not_run', 'aborted', 'completed'], 'RESULT_STATE_INVALID');
    member(result.assertionOutcome, OUTCOMES, 'RESULT_STATE_INVALID');
    member(
      result.capability,
      ['unknown', 'limited', 'unsupported', 'supported'],
      'RESULT_STATE_INVALID'
    );
    member(result.reasonCode, REASONS, 'PRIVACY_REJECTED');
    for (const a of result.assertions) {
      member(a.outcome, OUTCOMES, 'RESULT_STATE_INVALID');
      member(a.observationCode, OBSERVATIONS, 'PRIVACY_REJECTED');
      if (a.outcome === 'not_run')
        requireThat(a.observationCode === 'NONE', 'RESULT_STATE_INVALID');
    }
    if (run.state === 'not_run')
      requireThat(result.executionState === 'not_run', 'RESULT_STATE_INVALID');
    if (run.state === 'completed')
      requireThat(result.executionState === 'completed', 'RESULT_STATE_INVALID');
    if (result.executionState === 'not_run') {
      requireThat(
        result.assertionOutcome === 'not_run' &&
          result.capability === 'unknown' &&
          result.assertions.length === 0 &&
          result.evidenceRefs.length === 0 &&
          ['NOT_RUN', 'NOT_IMPLEMENTED', 'OPTIONAL_DISABLED'].includes(result.reasonCode),
        'RESULT_STATE_INVALID'
      );
      continue;
    }
    requireThat(run.state !== 'not_run', 'RESULT_STATE_INVALID');
    if (result.executionState === 'aborted') {
      requireThat(
        run.state === 'aborted' &&
          result.assertionOutcome === 'error' &&
          result.capability === 'unknown' &&
          [
            'SETUP_FAILED',
            'TIMEOUT',
            'CANCELLED',
            'INPUT_LIMIT',
            'TARGET_UNREACHABLE',
            'CLEANUP_FAILED',
          ].includes(result.reasonCode),
        'RESULT_STATE_INVALID'
      );
      continue;
    }
    requireThat(
      run.setupState === 'ready' && ['pass', 'fail'].includes(result.assertionOutcome),
      'RESULT_STATE_INVALID'
    );
    member(
      result.reasonCode,
      [
        'NONE',
        'AUTH_REJECTED',
        'TLS_INVALID',
        'REDIRECT_BLOCKED',
        'CAPABILITY_MISSING',
        'BODY_SCHEMA_MISMATCH',
        'ASSERTION_FAILED',
        'COVERAGE_UNKNOWN',
        'CONFLICT_RECOVERY_UNKNOWN',
      ],
      'RESULT_STATE_INVALID'
    );
    requireThat(
      c.profileRefs.some((id) => catalogue.profiles.get(id).selected),
      'RESULT_STATE_INVALID'
    );
    const mandatory = c.expectations.filter((e) => e.mandatory);
    const mandatoryPass =
      mandatory.length > 0 &&
      mandatory.every((e) =>
        result.assertions.some((a) => a.expectationRef === e.id && a.outcome === 'pass')
      );
    if (result.assertionOutcome === 'pass') requireThat(mandatoryPass, 'RESULT_STATE_INVALID');
    else
      requireThat(
        result.assertions.some((a) => a.outcome === 'fail'),
        'RESULT_STATE_INVALID'
      );
    requireThat(result.evidenceRefs.length > 0, 'RESULT_STATE_INVALID');
    if (run.kind === 'foundation-fixture' && op.kind === 'function')
      requireThat(result.capability === 'unknown', 'RESULT_STATE_INVALID');
    if (result.capability === 'supported') {
      requireThat(
        mandatoryPass && result.assertionOutcome === 'pass' && result.reasonCode === 'NONE',
        'RESULT_STATE_INVALID'
      );
      requireThat(
        op.kind === 'guard'
          ? c.proofScope === 'boundary-guard' && f.kind === 'boundary-deny'
          : c.proofScope === 'upstream-function' &&
              f.kind === 'upstream-real' &&
              run.kind === 'upstream-run' &&
              verified,
        'RESULT_STATE_INVALID'
      );
    }
    if (
      op.kind === 'function' &&
      (f.kind !== 'upstream-real' || c.proofScope !== 'upstream-function')
    ) {
      requireThat(result.capability === 'unknown', 'RESULT_STATE_INVALID');
    }
  }
  return { results, evidence, usedEvidence };
}

function directorySnapshot(directory, owned = false) {
  const stat = lstatSync(directory);
  requireThat(
    stat.isDirectory() && !stat.isSymbolicLink() && realpathSync(directory) === directory,
    'EVIDENCE_INVALID'
  );
  if (owned)
    requireThat(
      (typeof process.getuid !== 'function' || stat.uid === process.getuid()) &&
        (stat.mode & 0o022) === 0,
      'EVIDENCE_INVALID'
    );
  return { directory, stat };
}
function checkSnapshot(snapshot) {
  requireThat(
    sameFile(snapshot.stat, lstatSync(snapshot.directory)) &&
      realpathSync(snapshot.directory) === snapshot.directory,
    'EVIDENCE_INVALID'
  );
}

function validateEvidenceFiles(root, report, reportIndex) {
  try {
    const absoluteRoot = path.resolve(root);
    const rootSnapshot = directorySnapshot(absoluteRoot, true);
    let totalBytes = 0;
    const coverage = new Map();
    for (const e of report.evidence) {
      requireThat(
        e.kind === 'sanitized-trace' && typeof e.sha256 === 'string' && DIGEST.test(e.sha256),
        'EVIDENCE_INVALID'
      );
      requireThat(
        typeof e.relativePath === 'string' &&
          e.relativePath.length <= 256 &&
          e.relativePath.split('/').every((p) => /^[A-Za-z0-9][A-Za-z0-9._-]{0,95}$/.test(p)) &&
          !e.relativePath.includes('\\'),
        'EVIDENCE_INVALID'
      );
      const components = e.relativePath.split('/');
      const snapshots = [rootSnapshot];
      let directory = absoluteRoot;
      for (const component of components.slice(0, -1)) {
        directory = path.join(directory, component);
        snapshots.push(directorySnapshot(directory, true));
      }
      const filename = path.join(directory, components.at(-1));
      requireThat(realpathSync(filename) === filename, 'EVIDENCE_INVALID');
      const stat = lstatSync(filename);
      requireThat(
        (typeof process.getuid !== 'function' || stat.uid === process.getuid()) &&
          (stat.mode & 0o022) === 0 &&
          stat.nlink === 1,
        'EVIDENCE_INVALID'
      );
      const bytes = boundedRead(filename, Math.min(MiB, 16 * MiB - totalBytes), 'EVIDENCE_INVALID');
      totalBytes += bytes.length;
      for (const snapshot of snapshots) checkSnapshot(snapshot);
      requireThat(digest(bytes) === e.sha256, 'EVIDENCE_INVALID');
      let trace;
      try {
        trace = parseCanonical(bytes);
      } catch (error) {
        throw new Invalid(error.code === 'INPUT_LIMIT' ? 'INPUT_LIMIT' : 'EVIDENCE_INVALID');
      }
      fields(trace, ['schemaVersion', 'runId', 'caseRef', 'records'], 'EVIDENCE_INVALID');
      const caseRef = reportIndex.usedEvidence.get(e.id);
      requireThat(
        trace.schemaVersion === 1 && trace.runId === report.run.id && trace.caseRef === caseRef,
        'EVIDENCE_INVALID'
      );
      requireThat(Array.isArray(trace.records), 'EVIDENCE_INVALID');
      requireThat(trace.records.length <= 32, 'INPUT_LIMIT');
      const result = reportIndex.results.get(caseRef);
      const covered = coverage.get(caseRef) ?? new Set();
      coverage.set(caseRef, covered);
      const seen = new Set();
      for (const record of trace.records) {
        fields(
          record,
          [
            'expectationRef',
            'outcome',
            'observationCode',
            'httpStatus',
            'elapsedMs',
            'requestBytes',
            'responseBytes',
          ],
          'EVIDENCE_INVALID'
        );
        requireThat(!seen.has(record.expectationRef), 'EVIDENCE_INVALID');
        seen.add(record.expectationRef);
        covered.add(record.expectationRef);
        const assertion = result.assertions.find((a) => a.expectationRef === record.expectationRef);
        requireThat(
          assertion &&
            assertion.outcome !== 'not_run' &&
            assertion.outcome === record.outcome &&
            assertion.observationCode === record.observationCode,
          'EVIDENCE_INVALID'
        );
        member(record.observationCode, OBSERVATIONS, 'PRIVACY_REJECTED');
        requireThat(
          record.httpStatus === null ||
            (Number.isSafeInteger(record.httpStatus) &&
              record.httpStatus >= 100 &&
              record.httpStatus <= 599),
          'EVIDENCE_INVALID'
        );
        requireThat(
          ['elapsedMs', 'requestBytes', 'responseBytes'].every(
            (k) => Number.isSafeInteger(record[k]) && record[k] >= 0
          ),
          'EVIDENCE_INVALID'
        );
      }
    }
    // Every recorded assertion needs a matching typed trace, including mandatory
    // assertions behind a supported claim. An empty/hash-only trace cannot pass.
    for (const result of report.results) {
      const covered = coverage.get(result.caseRef) ?? new Set();
      requireThat(
        result.assertions.every((a) => a.outcome === 'not_run' || covered.has(a.expectationRef)),
        'EVIDENCE_INVALID'
      );
    }
    checkSnapshot(rootSnapshot);
  } catch (error) {
    if (error instanceof Invalid) throw error;
    throw new Invalid('EVIDENCE_INVALID');
  }
}

export function validateEvidence(options) {
  try {
    requireThat(
      options &&
        typeof options === 'object' &&
        !Array.isArray(options) &&
        Object.keys(options).every((k) =>
          ['manifestPath', 'reportPath', 'evidenceRoot'].includes(k)
        ),
      'SIDE_EFFECT_REJECTED'
    );
    requireThat(
      typeof options.manifestPath === 'string' && options.manifestPath.length > 0,
      'USAGE'
    );
    const hasReport = Object.hasOwn(options, 'reportPath'),
      hasRoot = Object.hasOwn(options, 'evidenceRoot');
    requireThat(
      hasReport === hasRoot &&
        (!hasReport ||
          (typeof options.reportPath === 'string' &&
            options.reportPath.length > 0 &&
            typeof options.evidenceRoot === 'string' &&
            options.evidenceRoot.length > 0)),
      'USAGE'
    );
    const manifestBytes = boundedRead(options.manifestPath, MiB);
    const m = parseCanonical(manifestBytes);
    const catalogue = validateManifest(m);
    const requiredCases = m.cases.filter((c) =>
      c.profileRefs.some((id) => {
        const p = catalogue.profiles.get(id);
        return p.selected && p.requiredOperationRefs.includes(c.operationRef);
      })
    );
    const summary = {
      validationStatus: 'manifest_valid',
      declared: m.cases.length,
      selected: 0,
      completed: 0,
      pending: m.cases.length,
      requiredPending: requiredCases.length,
      setupFailures: 0,
      cleanupFailures: 0,
    };
    if (!hasReport) return summary;
    const r = parseCanonical(boundedRead(options.reportPath, 4 * MiB));
    const index = validateReport(r, m, manifestBytes, catalogue);
    validateEvidenceFiles(options.evidenceRoot, r, index);
    summary.validationStatus = 'report_valid';
    summary.selected = r.run.selectedCaseRefs.length;
    summary.completed = r.results.filter((result) => result.executionState === 'completed').length;
    summary.pending = m.cases.length - summary.completed;
    // Evidence completeness counter only; no qualification or acceptance decision.
    summary.requiredPending = requiredCases.filter((c) => {
      const result = index.results.get(c.id);
      return !result || result.executionState !== 'completed' || result.capability !== 'supported';
    }).length;
    summary.setupFailures = Number(r.run.setupState === 'failed');
    summary.cleanupFailures = Number(r.run.cleanupState === 'failed');
    return summary;
  } catch (error) {
    return {
      validationStatus: 'invalid',
      errorCode: error instanceof Invalid && ERRORS.has(error.code) ? error.code : 'INPUT_INVALID',
    };
  }
}

export function runCli(argv) {
  const flags = new Map([
    ['--manifest', 'manifestPath'],
    ['--report', 'reportPath'],
    ['--evidence-root', 'evidenceRoot'],
  ]);
  const options = {};
  for (let i = 0; i < argv.length; i += 2) {
    const key = flags.get(argv[i]);
    if (
      !key ||
      Object.hasOwn(options, key) ||
      typeof argv[i + 1] !== 'string' ||
      argv[i + 1].length === 0 ||
      argv[i + 1].startsWith('--')
    ) {
      return { validationStatus: 'invalid', errorCode: 'USAGE' };
    }
    options[key] = argv[i + 1];
  }
  return validateEvidence(options);
}
const entryUrl = process.argv[1] ? pathToFileURL(path.resolve(process.argv[1])).href : '';
let isCli = entryUrl === import.meta.url;
if (entryUrl && !isCli) {
  try {
    // Node resolves main-module symlinks while argv retains the launch path.
    isCli = pathToFileURL(realpathSync(process.argv[1])).href === import.meta.url;
  } catch {
    // Imports from eval/stdin can have a non-file argv entry.
  }
}
if (isCli) {
  const result = runCli(process.argv.slice(2));
  process.stdout.write(JSON.stringify(result) + '\n');
  process.exitCode =
    result.validationStatus === 'invalid' ? (result.errorCode === 'USAGE' ? 2 : 1) : 0;
}
