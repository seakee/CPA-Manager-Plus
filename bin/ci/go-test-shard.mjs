// Deterministic test sharding for the Go race gates in `PR Check`.
//
// `plan` enumerates a package's test catalog with `go test -list` (under the
// same `-race` flag CI executes it with, because packages may carry
// `//go:build race` / `!race` test files), assigns sorted test k to shard
// k % N, and prints the `-run` value for one shard. `verify` then proves that
// the shard executed exactly its assignment, so a sharding mistake can never
// shrink coverage silently.
//
// Usage:
//   node bin/ci/go-test-shard.mjs plan --pkg ./cmd/x --shards 4 --index 0 \
//     [--race] [--manifest out.json]
//   node bin/ci/go-test-shard.mjs verify --manifest out.json --results go-test.jsonl
import { createHash } from 'node:crypto';
import { spawnSync } from 'node:child_process';
import { readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { pathToFileURL } from 'node:url';

const TEST_NAME = /^Test\w*$/;
const BENCHMARK_NAME = /^Benchmark/;
const UNSUPPORTED_NAME = /^(?:Example|Fuzz)/;
const OK_LINE = /^ok\s+\S+(?:\s+\S+)?(?:\s+\[.*\])?\s*$/;

const stripTrailingSlashes = (value) => value.replace(/\/+$/, '');

export const normalizePackage = (pkg) => {
  const trimmed = pkg.trim();
  if (!trimmed) throw new Error('package is empty');
  return trimmed.startsWith('./') || trimmed === '.'
    ? stripTrailingSlashes(trimmed)
    : trimmed;
};

// `go test -list '.*' <pkg>` prints one identifier per line followed by the
// `ok <pkg> <time>` summary. Anything else means the enumeration is not the
// catalog we think it is, so the sharder refuses instead of guessing.
export const parseTestCatalog = (output) => {
  const catalog = [];
  for (const rawLine of output.split('\n')) {
    const line = rawLine.trim();
    if (!line) continue;
    if (OK_LINE.test(line)) continue;
    if (BENCHMARK_NAME.test(line)) continue;
    if (UNSUPPORTED_NAME.test(line)) {
      throw new Error(`unsupported test identifier "${line}": extend the sharder`);
    }
    if (!TEST_NAME.test(line)) {
      throw new Error(`unrecognized "go test -list" line "${line}"`);
    }
    catalog.push(line);
  }
  return catalog.sort((left, right) => (left < right ? -1 : left > right ? 1 : 0));
};

export const catalogSha256 = (catalog) =>
  createHash('sha256').update(`${catalog.join('\n')}\n`).digest('hex');

export const shardAssignments = (catalog, shards, index) => {
  const sorted = [...catalog].sort((left, right) => (left < right ? -1 : left > right ? 1 : 0));
  return {
    assigned: sorted.filter((_, position) => position % shards === index),
    unassigned: sorted.filter((_, position) => position % shards !== index),
  };
};

export const buildPlan = ({ pkg, race = false, shards, index, catalog }) => {
  if (!Number.isInteger(shards) || shards < 1) {
    throw new Error(`shards must be a positive integer, got ${shards}`);
  }
  if (!Number.isInteger(index) || index < 0 || index >= shards) {
    throw new Error(`index must be an integer in [0, ${shards}), got ${index}`);
  }
  if (catalog.length === 0) {
    throw new Error(`${pkg} reported no tests`);
  }
  if (shards > catalog.length) {
    throw new Error(`${shards} shards exceed ${catalog.length} tests in ${pkg}`);
  }
  const sorted = [...catalog].sort((left, right) => (left < right ? -1 : left > right ? 1 : 0));
  const { assigned } = shardAssignments(sorted, shards, index);
  return {
    package: pkg,
    race: Boolean(race),
    shards,
    index,
    catalog: sorted,
    catalogSha256: catalogSha256(sorted),
    assigned,
  };
};

export const runPattern = (assigned) =>
  assigned.length === 0 ? '^$' : `^(${assigned.join('|')})$`;

export const listTests = (pkg, { race = false, cwd = process.cwd(), env = process.env } = {}) => {
  const args = ['test', ...(race ? ['-race'] : []), '-list', '.*', pkg];
  const result = spawnSync('go', args, { cwd, encoding: 'utf8', env });
  if (result.error) throw result.error;
  if (result.status !== 0) {
    throw new Error(
      `go test -list failed for ${pkg} (${result.status}): ${(result.stderr || '').trim()}`
    );
  }
  return parseTestCatalog(result.stdout || '');
};

const packageMatches = (eventPackage, pkg) => {
  if (!eventPackage) return false;
  // Relative selectors (`./x`) resolve to import paths, so compare on the
  // trailing path segment boundary; a bare `.` names the module package itself
  // and carries no relative identity.
  const suffix = stripTrailingSlashes(pkg).replace(/^\.\//, '');
  if (!suffix) return false;
  return eventPackage === suffix || eventPackage.endsWith(`/${suffix}`);
};

// A top-level test of the manifest package is a unique name (no `/`). Subtests
// (`A/b`) and helper subprocesses (which reuse a top-level name) carry no new
// top-level identity.
export const parseExecutedTests = (jsonl, pkg) => {
  const executed = new Set();
  const failed = [];
  for (const rawLine of jsonl.split('\n')) {
    const line = rawLine.trim();
    if (!line) continue;
    let event;
    try {
      event = JSON.parse(line);
    } catch {
      throw new Error(`invalid "go test -json" line: ${line.slice(0, 120)}`);
    }
    if (!packageMatches(event.Package, pkg)) continue;
    if (event.Action === 'fail') {
      failed.push(event.Test ?? '');
    }
    if (event.Action !== 'run') continue;
    const name = event.Test;
    if (!name || name.includes('/') || !TEST_NAME.test(name)) continue;
    executed.add(name);
  }
  return { executed: [...executed].sort(), failed };
};

export const verifyExecution = (manifest, jsonl) => {
  const { executed, failed } = parseExecutedTests(jsonl, manifest.package);
  const expected = new Set(manifest.assigned);
  const missing = manifest.assigned.filter((name) => !executed.includes(name));
  const unexpected = executed.filter((name) => !expected.has(name));
  return { assigned: manifest.assigned, executed, missing, unexpected, failed };
};

const readManifest = (manifestPath) => {
  const manifest = JSON.parse(readFileSync(manifestPath, 'utf8'));
  for (const key of ['package', 'shards', 'index', 'catalog', 'assigned']) {
    if (manifest[key] === undefined) {
      throw new Error(`${manifestPath} is missing "${key}"`);
    }
  }
  return manifest;
};

const flagValue = (args, name) => {
  const position = args.indexOf(name);
  if (position === -1) return undefined;
  const value = args[position + 1];
  if (value === undefined || value.startsWith('--')) {
    throw new Error(`${name} requires a value`);
  }
  return value;
};

const integerFlag = (args, name, { required = true } = {}) => {
  const raw = flagValue(args, name);
  if (raw === undefined) {
    if (required) throw new Error(`${name} is required`);
    return undefined;
  }
  if (!/^-?\d+$/.test(raw)) throw new Error(`${name} must be an integer, got "${raw}"`);
  return Number(raw);
};

const planCommand = (args) => {
  const pkg = normalizePackage(flagValue(args, '--pkg') ?? '');
  const plan = buildPlan({
    pkg,
    race: args.includes('--race'),
    shards: integerFlag(args, '--shards'),
    index: integerFlag(args, '--index'),
    catalog: listTests(pkg, { race: args.includes('--race') }),
  });
  const manifestPath = flagValue(args, '--manifest');
  if (manifestPath) writeFileSync(manifestPath, `${JSON.stringify(plan, null, 2)}\n`);
  console.log(runPattern(plan.assigned));
  return 0;
};

const verifyCommand = (args) => {
  const manifest = readManifest(flagValue(args, '--manifest') ?? '');
  const resultsPath = flagValue(args, '--results') ?? '';
  const outcome = verifyExecution(manifest, readFileSync(resultsPath, 'utf8'));
  const label = `${manifest.package} shard ${manifest.index}/${manifest.shards}`;
  if (outcome.failed.length > 0) {
    console.error(`${label} failed: ${[...new Set(outcome.failed)].join(', ') || 'package'}`);
  }
  if (outcome.missing.length > 0) {
    console.error(`${label} never ran: ${outcome.missing.join(', ')}`);
  }
  if (outcome.unexpected.length > 0) {
    console.error(`${label} ran unassigned tests: ${outcome.unexpected.join(', ')}`);
  }
  if (outcome.failed.length > 0 || outcome.missing.length > 0 || outcome.unexpected.length > 0) {
    return 1;
  }
  console.log(
    `${label}: ${outcome.executed.length} test${outcome.executed.length === 1 ? '' : 's'} match the plan`
  );
  return 0;
};

export const main = (argv) => {
  const [command, ...args] = argv;
  if (command === 'plan') return planCommand(args);
  if (command === 'verify') return verifyCommand(args);
  throw new Error(`usage: go-test-shard.mjs plan <args> | verify <args>`);
};

const entryPoint = process.argv[1] ? pathToFileURL(path.resolve(process.argv[1])).href : '';
if (entryPoint === import.meta.url) {
  try {
    process.exitCode = main(process.argv.slice(2));
  } catch (error) {
    console.error(`go-test-shard: ${error.message}`);
    process.exitCode = 2;
  }
}
