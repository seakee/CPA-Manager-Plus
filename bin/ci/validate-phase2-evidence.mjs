#!/usr/bin/env node

import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { createReadStream, readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const scriptPath = fileURLToPath(import.meta.url);
export const repoRoot = path.resolve(path.dirname(scriptPath), '../..');
export const defaultContractPath = path.join(
  repoRoot,
  'tests/fixtures/phase2-evidence/contract.json'
);

export const CAPABILITY_STATUSES = [
  'supported',
  'partial',
  'unsupported',
  'unknown',
  'requires_upstream',
];

export const CANONICAL_LIFECYCLE_ORDER = [
  'request_received',
  'client_api_key_resolved',
  'model_resolved',
  'credential_candidates_generated',
  'disabled_cooldown_priority_filtering',
  'credential_selected',
  'provider_endpoint_resolved',
  'upstream_request_stream',
  'retry_fallback',
  'response_cancel_reject_failure',
  'usage_accounting_settlement',
];

export const FROZEN_BASELINE = Object.freeze({
  cpampCommit: '2850980d3d08fa30878744e1d67fc04607504c01',
  schedulerAcrossPrioritiesCommit: 'b715526add0c452acc62062bf4fcef53897be604',
  releases: Object.freeze({
    'v7.3.3': Object.freeze({
      sourceCommit: '7bbfeaf8a7acf2cd5a834dcb0842539fe6aabc2b',
      includesSchedulerAcrossPriorities: false,
      assets: Object.freeze({
        amd64: '7af8c99cd08eee3ccc81d1596e8a31785674d3de6bd7ec61416d59493dd8fc01',
        arm64: '5f320e3fae52af00f07b78201311e9d096b36e759441d948de48a10f49e71883',
      }),
    }),
    'v7.3.7': Object.freeze({
      sourceCommit: 'b773607e3e7756dc6020a291825e4eb08899595a',
      includesSchedulerAcrossPriorities: true,
    }),
    'v7.3.8': Object.freeze({
      sourceCommit: 'c93978c4ea2e908255a2a06c37599fda3651554a',
      includesSchedulerAcrossPriorities: true,
      assets: Object.freeze({
        amd64: '3fe5228c458624175d5e4e81d9dd003d82de688ca498fa368a790ea120bda0e3',
        arm64: '8d09ce286d857b2d0e6d77c39e08a755246120df58c02a115d58c391fc73e3f1',
      }),
    }),
  }),
});

const EVIDENCE_KINDS = new Set(['source', 'unit', 'black-box', 'integration']);

const invariant = (condition, message) => {
  if (!condition) throw new Error(message);
};

const jsonEqual = (left, right) => JSON.stringify(left) === JSON.stringify(right);

const readJSON = (filePath) => JSON.parse(readFileSync(filePath, 'utf8'));

const resolveJSONPointer = (schema, pointer) => {
  invariant(
    pointer.startsWith('#/'),
    `Only local JSON Schema references are supported: ${pointer}`
  );
  return pointer
    .slice(2)
    .split('/')
    .map((part) => part.replaceAll('~1', '/').replaceAll('~0', '~'))
    .reduce((current, part) => current?.[part], schema);
};

const matchesType = (value, type) => {
  if (type === 'null') return value === null;
  if (type === 'array') return Array.isArray(value);
  if (type === 'integer') return Number.isInteger(value);
  if (type === 'number') return typeof value === 'number' && Number.isFinite(value);
  if (type === 'object')
    return value !== null && typeof value === 'object' && !Array.isArray(value);
  return typeof value === type;
};

export function validateJSONSchema(value, schema, rootSchema = schema, location = '$') {
  if (schema.$ref) {
    const resolved = resolveJSONPointer(rootSchema, schema.$ref);
    invariant(resolved, `${location}: unresolved JSON Schema reference ${schema.$ref}`);
    validateJSONSchema(value, resolved, rootSchema, location);
    return;
  }

  if (schema.anyOf) {
    const failures = [];
    for (const candidate of schema.anyOf) {
      try {
        validateJSONSchema(value, candidate, rootSchema, location);
        return;
      } catch (error) {
        failures.push(error.message);
      }
    }
    throw new Error(`${location}: no anyOf schema matched (${failures.join('; ')})`);
  }

  if (Object.hasOwn(schema, 'const')) {
    invariant(jsonEqual(value, schema.const), `${location}: value does not match const`);
  }
  if (schema.enum) {
    invariant(
      schema.enum.some((candidate) => jsonEqual(value, candidate)),
      `${location}: invalid enum value ${JSON.stringify(value)}`
    );
  }
  if (schema.type) {
    const types = Array.isArray(schema.type) ? schema.type : [schema.type];
    invariant(
      types.some((type) => matchesType(value, type)),
      `${location}: expected type ${types.join('|')}`
    );
  }

  if (typeof value === 'string') {
    if (schema.minLength !== undefined)
      invariant(value.length >= schema.minLength, `${location}: string is too short`);
    if (schema.pattern)
      invariant(
        new RegExp(schema.pattern).test(value),
        `${location}: string does not match ${schema.pattern}`
      );
  }
  if (typeof value === 'number' && schema.minimum !== undefined) {
    invariant(value >= schema.minimum, `${location}: number is below minimum ${schema.minimum}`);
  }
  if (Array.isArray(value)) {
    if (schema.minItems !== undefined)
      invariant(value.length >= schema.minItems, `${location}: array has too few items`);
    if (schema.maxItems !== undefined)
      invariant(value.length <= schema.maxItems, `${location}: array has too many items`);
    if (schema.uniqueItems) {
      const unique = new Set(value.map((item) => JSON.stringify(item)));
      invariant(unique.size === value.length, `${location}: array items must be unique`);
    }
    if (schema.items) {
      value.forEach((item, index) =>
        validateJSONSchema(item, schema.items, rootSchema, `${location}[${index}]`)
      );
    }
  }
  if (value !== null && typeof value === 'object' && !Array.isArray(value)) {
    for (const key of schema.required || []) {
      invariant(Object.hasOwn(value, key), `${location}: missing required property ${key}`);
    }
    if (schema.additionalProperties === false) {
      const allowed = new Set(Object.keys(schema.properties || {}));
      for (const key of Object.keys(value)) {
        invariant(allowed.has(key), `${location}: unexpected property ${key}`);
      }
    }
    for (const [key, propertySchema] of Object.entries(schema.properties || {})) {
      if (Object.hasOwn(value, key)) {
        validateJSONSchema(value[key], propertySchema, rootSchema, `${location}.${key}`);
      }
    }
  }
}

const uniqueMap = (items, name) => {
  const result = new Map();
  for (const item of items) {
    invariant(!result.has(item.id), `Duplicate ${name} id: ${item.id}`);
    result.set(item.id, item);
  }
  return result;
};

const frozenAsset = (artifact, arch, expectedDigest) => {
  const asset = artifact.releaseAssets.find((candidate) => candidate.arch === arch);
  invariant(asset, `${artifact.id}: missing ${arch} release asset`);
  invariant(asset.sha256 === expectedDigest, `${artifact.id}: ${arch} digest drifted`);
  return asset;
};

export function validateCapabilityRecord(record, context) {
  const { artifactById, anchorById } = context;
  invariant(
    CAPABILITY_STATUSES.includes(record.status),
    `${record.id}: illegal capability status ${record.status}`
  );
  invariant(
    record.capability && typeof record.capability === 'string',
    `${record.id}: capability is required`
  );
  const artifact = artifactById.get(record.artifactId);
  invariant(artifact, `${record.id}: unknown artifact ${record.artifactId}`);
  invariant(
    record.deploymentMode === artifact.deploymentMode,
    `${record.id}: deployment mode does not match its artifact`
  );
  invariant(
    Array.isArray(record.evidenceKind) && record.evidenceKind.length > 0,
    `${record.id}: evidenceKind is required`
  );
  for (const kind of record.evidenceKind)
    invariant(EVIDENCE_KINDS.has(kind), `${record.id}: illegal evidence kind ${kind}`);
  invariant(
    Array.isArray(record.limitations) && record.limitations.length > 0,
    `${record.id}: limitations are required`
  );
  invariant(
    Array.isArray(record.evidenceRefs) && record.evidenceRefs.length > 0,
    `${record.id}: evidenceRefs are required`
  );

  for (const evidenceRef of record.evidenceRefs) {
    const anchor = anchorById.get(evidenceRef);
    invariant(anchor, `${record.id}: unknown evidence reference ${evidenceRef}`);
    invariant(
      anchor.versions.some((version) => version.artifactId === record.artifactId),
      `${record.id}: ${evidenceRef} has no evidence for ${record.artifactId}`
    );
  }
  for (const kind of record.evidenceKind) {
    invariant(
      record.evidenceRefs.some((evidenceRef) => anchorById.get(evidenceRef).evidenceKind === kind),
      `${record.id}: evidence kind ${kind} has no matching evidence reference`
    );
  }

  if (!artifact.versionKnown) {
    invariant(
      record.status === 'unknown' || record.status === 'partial',
      `${record.id}: unversioned External evidence cannot be promoted to ${record.status}`
    );
    invariant(
      record.pluginContract === null,
      `${record.id}: unnegotiated External plugin contract must be null`
    );
  }
}

const validateFrozenArtifacts = (contract, artifactById) => {
  invariant(
    contract.cpampBaselineCommit === FROZEN_BASELINE.cpampCommit,
    'CPAMP baseline commit drifted'
  );
  const current = artifactById.get('current-bundled-v7-3-3');
  const candidate = artifactById.get('candidate-release-v7-3-8');
  const external = artifactById.get('external-unnegotiated');
  invariant(current?.classification === 'current_bundled', 'Current bundled artifact is missing');
  invariant(
    candidate?.classification === 'candidate_release',
    'Candidate release artifact is missing'
  );
  invariant(external?.classification === 'external', 'External artifact boundary is missing');
  invariant(current.cpaVersion === 'v7.3.3', 'Current bundled CPA must remain v7.3.3');
  invariant(candidate.cpaVersion === 'v7.3.8', 'Candidate fixture must remain v7.3.8');
  invariant(
    current.sourceCommit === FROZEN_BASELINE.releases['v7.3.3'].sourceCommit,
    'v7.3.3 source commit drifted'
  );
  invariant(
    candidate.sourceCommit === FROZEN_BASELINE.releases['v7.3.8'].sourceCommit,
    'v7.3.8 source commit drifted'
  );
  invariant(current.deploymentMode === 'embedded', 'Current artifact must be Embedded');
  invariant(
    candidate.deploymentMode === 'release-artifact-fixture',
    'Candidate must remain a fixture, not a product upgrade'
  );
  invariant(
    external.versionKnown === false && external.cpaVersion === null,
    'External version must remain unknown until observed'
  );
  invariant(
    external.pluginAvailability === 'unknown',
    'External plugin availability must remain unknown'
  );
  frozenAsset(current, 'amd64', FROZEN_BASELINE.releases['v7.3.3'].assets.amd64);
  frozenAsset(current, 'arm64', FROZEN_BASELINE.releases['v7.3.3'].assets.arm64);
  frozenAsset(candidate, 'amd64', FROZEN_BASELINE.releases['v7.3.8'].assets.amd64);
  frozenAsset(candidate, 'arm64', FROZEN_BASELINE.releases['v7.3.8'].assets.arm64);
};

const validateSchedulerProvenance = (contract) => {
  const provenance = contract.schedulerAcrossPrioritiesProvenance;
  invariant(
    provenance.commit === FROZEN_BASELINE.schedulerAcrossPrioritiesCommit,
    'SchedulerAcrossPriorities commit drifted'
  );
  invariant(provenance.default === false, 'SchedulerAcrossPriorities default must remain false');
  const ancestry = new Map(provenance.releaseAncestry.map((entry) => [entry.version, entry]));
  for (const [version, expected] of Object.entries(FROZEN_BASELINE.releases)) {
    const actual = ancestry.get(version);
    invariant(actual, `Missing SchedulerAcrossPriorities ancestry for ${version}`);
    invariant(
      actual.sourceCommit === expected.sourceCommit,
      `${version}: ancestry source commit drifted`
    );
    invariant(
      actual.includesCommit === expected.includesSchedulerAcrossPriorities,
      `${version}: SchedulerAcrossPriorities ancestry result drifted`
    );
  }
};

export function validateContract(contract, schema) {
  validateJSONSchema(contract, schema);
  invariant(
    jsonEqual(contract.capabilityStatuses, CAPABILITY_STATUSES),
    'Capability vocabulary or ordering drifted'
  );
  invariant(
    jsonEqual(schema.$defs.capabilityStatus.enum, CAPABILITY_STATUSES),
    'JSON Schema capability vocabulary drifted'
  );

  const artifactById = uniqueMap(contract.artifacts, 'artifact');
  const anchorById = uniqueMap(contract.sourceAnchors, 'source anchor');
  const assetIds = new Set();
  for (const artifact of contract.artifacts) {
    for (const asset of artifact.releaseAssets) {
      invariant(!assetIds.has(asset.id), `Duplicate release asset id: ${asset.id}`);
      assetIds.add(asset.id);
    }
  }

  validateFrozenArtifacts(contract, artifactById);
  validateSchedulerProvenance(contract);

  for (const anchor of contract.sourceAnchors) {
    for (const version of anchor.versions) {
      const artifact = artifactById.get(version.artifactId);
      invariant(artifact, `${anchor.id}: unknown artifact ${version.artifactId}`);
      if (anchor.repository === 'cpa' && artifact.sourceCommit !== null) {
        invariant(
          version.sourceCommit === artifact.sourceCommit,
          `${anchor.id}: source ref mixes CPA artifact versions`
        );
      }
      if (anchor.repository === 'cpamp') {
        invariant(
          version.sourceCommit === contract.cpampBaselineCommit,
          `${anchor.id}: CPAMP evidence is not pinned to the accepted baseline`
        );
      }
    }
  }

  invariant(
    jsonEqual(contract.requestLifecycle.canonicalOrder, CANONICAL_LIFECYCLE_ORDER),
    'Canonical lifecycle order drifted'
  );
  const lifecycleIds = contract.requestLifecycle.stages.map((stage) => stage.id);
  invariant(
    jsonEqual(lifecycleIds, CANONICAL_LIFECYCLE_ORDER),
    'Lifecycle stages do not follow canonical order'
  );
  contract.requestLifecycle.stages.forEach((stage, ordinal) => {
    invariant(stage.ordinal === ordinal, `${stage.id}: lifecycle ordinal must be ${ordinal}`);
    for (const artifactId of stage.artifactScope)
      invariant(artifactById.has(artifactId), `${stage.id}: unknown artifact ${artifactId}`);
    for (const evidenceRef of stage.evidenceRefs)
      invariant(
        anchorById.has(evidenceRef),
        `${stage.id}: unknown evidence reference ${evidenceRef}`
      );
  });

  const recordById = uniqueMap(contract.capabilityRecords, 'capability record');
  for (const record of recordById.values())
    validateCapabilityRecord(record, { artifactById, anchorById });

  return { artifactById, anchorById, recordById };
}

export function loadAndValidateContract(contractPath = defaultContractPath) {
  const contract = readJSON(contractPath);
  const schemaPath = path.resolve(path.dirname(contractPath), contract.$schema);
  const schema = readJSON(schemaPath);
  const indexes = validateContract(contract, schema);
  return { contract, schema, ...indexes };
}

export function validateBundledDockerfile(
  contract,
  dockerfilePath = path.join(repoRoot, 'Dockerfile.runtime')
) {
  const dockerfile = readFileSync(dockerfilePath, 'utf8');
  const current = contract.artifacts.find(
    (artifact) => artifact.classification === 'current_bundled'
  );
  const candidate = contract.artifacts.find(
    (artifact) => artifact.classification === 'candidate_release'
  );
  invariant(
    dockerfile.includes('cpa_version="7.3.3"'),
    'Dockerfile.runtime no longer bundles CPA v7.3.3'
  );
  for (const asset of current.releaseAssets)
    invariant(
      dockerfile.includes(asset.sha256),
      `Dockerfile.runtime is missing ${asset.id} digest`
    );
  invariant(
    !dockerfile.includes(candidate.cpaVersion.slice(1)),
    'Dockerfile.runtime must not use the candidate CPA version'
  );
  for (const asset of candidate.releaseAssets)
    invariant(
      !dockerfile.includes(asset.sha256),
      'Dockerfile.runtime must not use a candidate digest'
    );
}

const releaseAssets = (contract) =>
  contract.artifacts.flatMap((artifact) =>
    artifact.releaseAssets.map((asset) => ({ ...asset, artifactId: artifact.id }))
  );

export async function sha256File(filePath) {
  const hash = createHash('sha256');
  for await (const chunk of createReadStream(filePath)) hash.update(chunk);
  return hash.digest('hex');
}

export async function verifyArtifactDigest(contract, assetId, filePath) {
  const asset = releaseAssets(contract).find((candidate) => candidate.id === assetId);
  invariant(asset, `Unknown release asset id: ${assetId}`);
  const actual = await sha256File(filePath);
  invariant(
    actual === asset.sha256,
    `${assetId}: SHA256 mismatch; got ${actual}, want ${asset.sha256}`
  );
  return { assetId, artifactId: asset.artifactId, filePath, sha256: actual };
}

const git = (repository, args, encoding = 'utf8') =>
  execFileSync('git', ['-C', repository, ...args], { encoding, stdio: ['ignore', 'pipe', 'pipe'] });

export function verifySourceProvenance(contract, cpaSource, cpampSource = repoRoot) {
  invariant(cpaSource, 'CPA source checkout is required for source verification');
  const checkedCommits = new Set();
  for (const anchor of contract.sourceAnchors) {
    const repository = anchor.repository === 'cpa' ? cpaSource : cpampSource;
    for (const version of anchor.versions) {
      const commitKey = `${repository}:${version.sourceCommit}`;
      if (!checkedCommits.has(commitKey)) {
        git(repository, ['cat-file', '-e', `${version.sourceCommit}^{commit}`]);
        checkedCommits.add(commitKey);
      }
      const source = git(repository, ['show', `${version.sourceCommit}:${anchor.path}`]);
      for (const symbol of anchor.symbols) {
        invariant(
          source.includes(symbol),
          `${anchor.id}@${version.sourceCommit}: missing source symbol ${symbol}`
        );
      }
    }
  }

  const featureCommit = contract.schedulerAcrossPrioritiesProvenance.commit;
  git(cpaSource, ['cat-file', '-e', `${featureCommit}^{commit}`]);
  for (const release of contract.schedulerAcrossPrioritiesProvenance.releaseAncestry) {
    let includes = true;
    try {
      git(cpaSource, ['merge-base', '--is-ancestor', featureCommit, release.sourceCommit]);
    } catch {
      includes = false;
    }
    invariant(
      includes === release.includesCommit,
      `${release.version}: live local ancestry does not match the frozen fixture`
    );
  }
  return { anchors: contract.sourceAnchors.length, commits: checkedCommits.size };
}

export function validateAdditionalRecords(records, context) {
  invariant(Array.isArray(records), 'Additional capability evidence must be a JSON array');
  const ids = new Set(context.recordById.keys());
  for (const [index, record] of records.entries()) {
    invariant(context.schema, 'Capability record JSON Schema is required');
    validateJSONSchema(
      record,
      context.schema.$defs.capabilityRecord,
      context.schema,
      `$additionalRecords[${index}]`
    );
    invariant(!ids.has(record.id), `Duplicate capability record id: ${record.id}`);
    validateCapabilityRecord(record, context);
    ids.add(record.id);
  }
  return records.length;
}

const parseArgs = (argv) => {
  const options = { artifacts: [] };
  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index];
    if (arg === '--json') options.json = true;
    else if (arg === '--contract') options.contract = argv[++index];
    else if (arg === '--cpa-source') options.cpaSource = argv[++index];
    else if (arg === '--records') options.records = argv[++index];
    else if (arg === '--artifact') options.artifacts.push(argv[++index]);
    else throw new Error(`Unknown argument: ${arg}`);
  }
  for (const [name, value] of Object.entries(options)) {
    if (name !== 'json' && name !== 'artifacts')
      invariant(value !== undefined, `Missing value for --${name}`);
  }
  return options;
};

const parseArtifactSpec = (spec) => {
  const separator = spec?.indexOf('=') ?? -1;
  invariant(
    separator > 0 && separator < spec.length - 1,
    '--artifact must use <asset-id>=<local-file>'
  );
  return { assetId: spec.slice(0, separator), filePath: spec.slice(separator + 1) };
};

async function main() {
  const options = parseArgs(process.argv.slice(2));
  const loaded = loadAndValidateContract(options.contract || defaultContractPath);
  validateBundledDockerfile(loaded.contract);

  const summary = {
    contractId: loaded.contract.contractId,
    artifacts: loaded.contract.artifacts.length,
    lifecycleStages: loaded.contract.requestLifecycle.stages.length,
    capabilityRecords: loaded.contract.capabilityRecords.length,
    verifiedArtifacts: [],
    verifiedSource: null,
    additionalRecords: 0,
  };

  for (const spec of options.artifacts) {
    const { assetId, filePath } = parseArtifactSpec(spec);
    summary.verifiedArtifacts.push(await verifyArtifactDigest(loaded.contract, assetId, filePath));
  }
  if (options.cpaSource)
    summary.verifiedSource = verifySourceProvenance(loaded.contract, options.cpaSource);
  if (options.records) {
    const additional = readJSON(options.records);
    summary.additionalRecords = validateAdditionalRecords(additional, loaded);
  }

  if (options.json) process.stdout.write(`${JSON.stringify(summary, null, 2)}\n`);
  else
    process.stdout.write(
      `Phase2 evidence contract valid: ${summary.lifecycleStages} lifecycle stages, ${summary.capabilityRecords} capability records\n`
    );
}

if (process.argv[1] && path.resolve(process.argv[1]) === scriptPath) {
  main().catch((error) => {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
  });
}
