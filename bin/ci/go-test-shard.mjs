// Deterministic test sharding for the Go race gates in `PR Check`.
//
// `plan` enumerates a package's test catalog with `go test -list` (under the
// same `-race` flag CI executes it with, because packages may carry
// `//go:build race` / `!race` test files), assigns sorted test k to shard
// k % N, and prints the `-run` value for one shard. `verify` then proves that
// the shard executed exactly its assignment to a terminal pass or skip and that
// the package itself passed, so a sharding mistake or an aborted run can never
// shrink coverage silently.
//
// A module-root selector (`.`, `./`) is refused: it names no relative path, so
// its import path could only be guessed from the enumeration anyway.
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
const OK_LINE = /^ok\s+(\S+)(?:\s+\S+)?(?:\s+\[.*\])?\s*$/;
const TERMINAL_ACTIONS = new Set(['pass', 'skip']);

const stripTrailingSlashes = (value) => value.replace(/\/+$/, '');

export const normalizePackage = (pkg) => {
  const trimmed = pkg.trim();
  if (!trimmed) throw new Error('package is empty');
  if (trimmed === '.' || trimmed === './') {
    throw new Error('package selector "." names the module root and no relative path; pass ./<dir>');
  }
  return trimmed.startsWith('./') || trimmed.startsWith('../')
    ? stripTrailingSlashes(trimmed)
    : trimmed;
};

// `go test -list '.*' <pkg>` prints one identifier per line followed by the
// `ok <importpath> <time>` summary. Anything else means the enumeration is not
// the catalog we think it is, so the sharder refuses instead of guessing.
export const parseTestListing = (output) => {
  const catalog = [];
  let importPath;
  for (const rawLine of output.split('\n')) {
    const line = rawLine.trim();
    if (!line) continue;
    const listResult = OK_LINE.exec(line);
    if (listResult) {
      importPath = listResult[1];
      continue;
    }
    if (BENCHMARK_NAME.test(line)) continue;
    if (UNSUPPORTED_NAME.test(line)) {
      throw new Error(`unsupported test identifier "${line}": extend the sharder`);
    }
    if (!TEST_NAME.test(line)) {
      throw new Error(`unrecognized "go test -list" line "${line}"`);
    }
    catalog.push(line);
  }
  if (!importPath) {
    throw new Error('"go test -list" printed no "ok <importpath>" summary line');
  }
  if (importPath === '.' || importPath === './') {
    throw new Error(`"go test -list" reported the module root as "${importPath}"`);
  }
  return {
    importPath,
    catalog: catalog.sort((left, right) => (left < right ? -1 : left > right ? 1 : 0)),
  };
};

export const parseTestCatalog = (output) => parseTestListing(output).catalog;

export const catalogSha256 = (catalog) =>
  createHash('sha256').update(`${catalog.join('\n')}\n`).digest('hex');

export const shardAssignments = (catalog, shards, index) => {
  const sorted = [...catalog].sort((left, right) => (left < right ? -1 : left > right ? 1 : 0));
  return {
    assigned: sorted.filter((_, position) => position % shards === index),
    unassigned: sorted.filter((_, position) => position % shards !== index),
  };
};

export const buildPlan = ({ pkg, importPath, race = false, shards, index, catalog }) => {
  if (!importPath) {
    throw new Error(`${pkg} has no resolved import path`);
  }
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
    importPath,
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
  return parseTestListing(result.stdout || '');
};

// A top-level test of the manifest package is a unique name (no `/`). Subtests
// (`A/b`) and helper subprocesses (which reuse a top-level name) carry no new
// top-level identity, so only a top-level terminal `pass`/`skip` event proves
// that a test ran to completion.
export const parseExecutedTests = (jsonl, importPath) => {
  if (!importPath) throw new Error('no import path to match results against');
  const executed = new Set();
  const failed = [];
  let packagePassed = false;
  let packageFailed = false;
  for (const rawLine of jsonl.split('\n')) {
    const line = rawLine.trim();
    if (!line) continue;
    let event;
    try {
      event = JSON.parse(line);
    } catch {
      throw new Error(`invalid "go test -json" line: ${line.slice(0, 120)}`);
    }
    if (event.Package !== importPath) continue;
    const name = event.Test;
    // The package result is the only event without a test identity.
    if (name === undefined) {
      if (event.Action === 'pass') packagePassed = true;
      if (event.Action === 'fail') packageFailed = true;
      continue;
    }
    if (name.includes('/') || !TEST_NAME.test(name)) continue;
    if (event.Action === 'fail') failed.push(name);
    if (TERMINAL_ACTIONS.has(event.Action)) executed.add(name);
  }
  return { executed: [...executed].sort(), failed: [...new Set(failed)].sort(), packagePassed, packageFailed };
};

export const verifyExecution = (manifest, jsonl) => {
  const { executed, failed, packagePassed, packageFailed } = parseExecutedTests(
    jsonl,
    manifest.importPath
  );
  const expected = new Set(manifest.assigned);
  const executedSet = new Set(executed);
  return {
    assigned: manifest.assigned,
    executed,
    failed,
    packagePassed,
    packageFailed,
    missing: manifest.assigned.filter((name) => !executedSet.has(name)),
    unfinished: failed.filter((name) => expected.has(name) && !executedSet.has(name)),
    unexpected: executed.filter((name) => !expected.has(name)),
  };
};

const readManifest = (manifestPath) => {
  const manifest = JSON.parse(readFileSync(manifestPath, 'utf8'));
  for (const key of ['package', 'importPath', 'shards', 'index', 'catalog', 'assigned']) {
    if (manifest[key] === undefined || manifest[key] === '') {
      throw new Error(`${manifestPath} is missing "${key}"`);
    }
  }
  return manifest;
};

// Flags are parsed strictly left to right: an unknown flag, a repeated flag or
// a flag without a value is a usage error (exit code 2), never ignored.
const VALUE_FLAGS = {
  plan: new Set(['--pkg', '--shards', '--index', '--manifest']),
  verify: new Set(['--manifest', '--results']),
};
const BOOLEAN_FLAGS = {
  plan: new Set(['--race']),
  verify: new Set(),
};

export const parseArgs = (command, args) => {
  const valueFlags = VALUE_FLAGS[command];
  const booleanFlags = BOOLEAN_FLAGS[command];
  if (!valueFlags) throw new Error(`unknown command "${command}"`);
  const parsed = {};
  for (let position = 0; position < args.length; position += 1) {
    const flag = args[position];
    if (flag.startsWith('-') && !valueFlags.has(flag) && !booleanFlags.has(flag)) {
      throw new Error(`unknown flag "${flag}" for ${command}`);
    }
    if (Object.hasOwn(parsed, flag)) {
      throw new Error(`duplicate flag "${flag}"`);
    }
    if (booleanFlags.has(flag)) {
      parsed[flag] = true;
      continue;
    }
    const value = args[position + 1];
    if (value === undefined || value.startsWith('--')) {
      throw new Error(`${flag} requires a value`);
    }
    parsed[flag] = value;
    position += 1;
  }
  return parsed;
};

const integerOption = (parsed, flag, { required = true } = {}) => {
  const raw = parsed[flag];
  if (raw === undefined) {
    if (required) throw new Error(`${flag} is required`);
    return undefined;
  }
  if (!/^-?\d+$/.test(raw)) throw new Error(`${flag} must be an integer, got "${raw}"`);
  return Number(raw);
};

const planCommand = (args) => {
  const parsed = parseArgs('plan', args);
  const pkg = normalizePackage(parsed['--pkg'] ?? '');
  const listing = listTests(pkg, { race: Boolean(parsed['--race']) });
  const plan = buildPlan({
    pkg,
    importPath: listing.importPath,
    race: Boolean(parsed['--race']),
    shards: integerOption(parsed, '--shards'),
    index: integerOption(parsed, '--index'),
    catalog: listing.catalog,
  });
  if (parsed['--manifest']) {
    writeFileSync(parsed['--manifest'], `${JSON.stringify(plan, null, 2)}\n`);
  }
  console.log(runPattern(plan.assigned));
  return 0;
};

const verifyCommand = (args) => {
  const parsed = parseArgs('verify', args);
  const manifestPath = parsed['--manifest'];
  if (!manifestPath) throw new Error('--manifest is required');
  if (!parsed['--results']) throw new Error('--results is required');
  const manifest = readManifest(manifestPath);
  const outcome = verifyExecution(manifest, readFileSync(parsed['--results'], 'utf8'));
  const label = `${manifest.package} shard ${manifest.index}/${manifest.shards}`;
  // A test that started and then failed never reaches a terminal pass/skip; it
  // is reported separately so a mid-test abort is not read as "never ran".
  const unfinished = outcome.unfinished;
  const neverRan = outcome.missing.filter((name) => !unfinished.includes(name));
  if (outcome.failed.length > 0) {
    console.error(`${label} failed: ${outcome.failed.join(', ')}`);
  }
  if (neverRan.length > 0) {
    console.error(`${label} never ran: ${neverRan.join(', ')}`);
  }
  if (unfinished.length > 0) {
    console.error(`${label} did not reach pass or skip: ${unfinished.join(', ')}`);
  }
  if (outcome.unexpected.length > 0) {
    console.error(`${label} ran unassigned tests: ${outcome.unexpected.join(', ')}`);
  }
  if (!outcome.packagePassed) {
    console.error(
      `${label}: package did not pass${
        outcome.packageFailed ? ' (reported fail)' : ' (no pass event)'
      } for ${manifest.importPath}`
    );
  }
  if (
    outcome.failed.length > 0 ||
    neverRan.length > 0 ||
    unfinished.length > 0 ||
    outcome.unexpected.length > 0 ||
    !outcome.packagePassed
  ) {
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
