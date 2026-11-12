import { spawnSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import {
  buildPlan,
  normalizePackage,
  parseArgs,
  parseExecutedTests,
  parseTestCatalog,
  parseTestListing,
  runPattern,
  shardAssignments,
  verifyExecution,
} from '../bin/ci/go-test-shard.mjs';

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const toolPath = path.join(repoRoot, 'bin', 'ci', 'go-test-shard.mjs');

const importPath = 'example.com/pkg';

const catalogFixture = (count) =>
  Array.from({ length: count }, (_, index) => `TestCase${String(index).padStart(2, '0')}`);

const planFixture = (catalog, { shards = 1, index = 0 } = {}) =>
  buildPlan({ pkg: './pkg', importPath, shards, index, catalog });

const jsonlEvent = (event) => JSON.stringify(event);

// A well-formed `go test -json` stream for one package: every test runs and
// ends in a terminal pass, then the package itself passes.
const goJsonl = ({ pkg = importPath, runs, fails = [], packagePass = true }) => {
  const lines = [jsonlEvent({ Time: '2026-01-01T00:00:00Z', Action: 'start', Package: pkg })];
  for (const name of runs) {
    lines.push(jsonlEvent({ Time: '2026-01-01T00:00:00Z', Action: 'run', Package: pkg, Test: name }));
    lines.push(
      jsonlEvent({ Time: '2026-01-01T00:00:00Z', Action: 'pass', Package: pkg, Test: name })
    );
  }
  for (const name of fails) {
    lines.push(jsonlEvent({ Time: '2026-01-01T00:00:00Z', Action: 'run', Package: pkg, Test: name }));
    lines.push(jsonlEvent({ Time: '2026-01-01T00:00:00Z', Action: 'fail', Package: pkg, Test: name }));
  }
  if (packagePass) {
    lines.push(
      jsonlEvent({ Time: '2026-01-01T00:00:00Z', Action: 'pass', Package: pkg, Elapsed: 0.01 })
    );
  }
  return `${lines.join('\n')}\n`;
};

const runTool = (args, options = {}) =>
  spawnSync(process.execPath, [toolPath, ...args], {
    encoding: 'utf8',
    cwd: options.cwd ?? repoRoot,
  });

describe('go test shard planner', () => {
  it('assigns every catalog entry to exactly one shard', () => {
    const catalog = catalogFixture(10);
    const plan = planFixture(catalog, { shards: 3, index: 1 });

    expect(plan.assigned).toEqual(catalog.filter((_, position) => position % 3 === 1));
    expect(plan.catalog).toEqual(catalog);
    expect(plan.race).toBe(false);
    expect(plan.importPath).toBe(importPath);
    for (const index of [0, 1, 2]) {
      const { assigned } = shardAssignments(catalog, 3, index);
      expect(assigned, `shard ${index}`).toEqual(
        catalog.filter((_, position) => position % 3 === index)
      );
    }
  });

  it('keeps shards disjoint and their union equal to the sorted catalog', () => {
    const catalog = catalogFixture(17);
    const shards = 4;
    const union = [];
    const seen = [];
    for (let index = 0; index < shards; index += 1) {
      const plan = planFixture(catalog, { shards, index });
      union.push(...plan.assigned);
      expect(new Set(plan.assigned).size).toBe(plan.assigned.length);
      for (const name of plan.assigned) {
        // Disjoint: shards are laid out round-robin over the sorted catalog.
        expect(catalog.indexOf(name) % shards, name).toBe(index);
      }
      seen.push(...plan.assigned);
    }
    expect(seen.sort()).toEqual([...catalog].sort());
    expect(union.sort()).toEqual([...catalog].sort());
  });

  it('builds a single regular expression from the assignment', () => {
    expect(runPattern(['TestA', 'TestB'])).toBe('^(TestA|TestB)$');
    expect(runPattern([])).toBe('^$');
  });

  it('refuses more shards than tests, a bad shard count and a bad index', () => {
    const catalog = catalogFixture(2);
    expect(() => planFixture(catalog, { shards: 3, index: 0 })).toThrow(/exceed/);
    expect(() => planFixture(catalog, { shards: 0, index: 0 })).toThrow(/positive/);
    expect(() => planFixture(catalog, { shards: 1.5, index: 0 })).toThrow(/positive/);
    expect(() => planFixture(catalog, { shards: 2, index: -1 })).toThrow(/index/);
    expect(() => planFixture(catalog, { shards: 2, index: 2 })).toThrow(/index/);
    expect(() => planFixture([], { shards: 2, index: 0 })).toThrow(/no tests/);
    expect(() =>
      buildPlan({ pkg: './pkg', importPath: '', shards: 1, index: 0, catalog })
    ).toThrow(/import path/);
  });

  it('refuses a module-root selector before it can be resolved', () => {
    for (const selector of ['.', './']) {
      expect(() => normalizePackage(selector), selector).toThrow(/module root/);
    }
    expect(normalizePackage('  ./internal/service/maintenancebundle/ ')).toBe(
      './internal/service/maintenancebundle'
    );
    expect(normalizePackage('example.com/pkg')).toBe('example.com/pkg');
  });

  it('parses the go test -list catalog and ignores benchmarks', () => {
    const output = [
      'TestBeta',
      'BenchmarkAlpha',
      'TestAlpha',
      `ok  \t${importPath}\t0.123s`,
      '',
    ].join('\n');
    expect(parseTestCatalog(output)).toEqual(['TestAlpha', 'TestBeta']);
    expect(parseTestListing(output)).toEqual({
      importPath,
      catalog: ['TestAlpha', 'TestBeta'],
    });
  });

  it('rejects Example, Fuzz and unrecognized list output', () => {
    const summary = `ok  \t${importPath}\t0.1s`;
    expect(() => parseTestCatalog(`TestAlpha\nExampleAlpha\n${summary}`)).toThrow(
      /extend the sharder/
    );
    expect(() => parseTestCatalog(`TestAlpha\nFuzzAlpha\n${summary}`)).toThrow(
      /extend the sharder/
    );
    expect(() => parseTestCatalog(`TestAlpha\nnot-a-test\n${summary}`)).toThrow(/unrecognized/);
    expect(() => parseTestCatalog('build failed\n')).toThrow(/unrecognized/);
    // Without the summary line the import path is unknown, so the manifest
    // could not carry one.
    expect(() => parseTestListing('TestAlpha\n')).toThrow(/import path|ok </);
  });

  it('parses flags strictly and rejects unknown, duplicate and valueless flags', () => {
    expect(parseArgs('plan', ['--pkg', './x', '--race', '--shards', '4', '--index', '0'])).toEqual({
      '--pkg': './x',
      '--race': true,
      '--shards': '4',
      '--index': '0',
    });
    expect(() => parseArgs('plan', ['--pkg', './x', '--bogus'])).toThrow(/unknown flag "--bogus"/);
    expect(() => parseArgs('plan', ['--pkg', './x', '--pkg', './y'])).toThrow(/duplicate flag/);
    expect(() => parseArgs('plan', ['--race', '--race'])).toThrow(/duplicate flag/);
    expect(() => parseArgs('plan', ['--pkg'])).toThrow(/requires a value/);
    expect(() => parseArgs('plan', ['--pkg', '--race'])).toThrow(/requires a value/);
    expect(() => parseArgs('verify', ['--manifest', 'm.json', '--race'])).toThrow(/unknown flag/);
  });

  it('reports an unknown flag through the CLI with exit code 2', () => {
    const result = runTool(['verify', '--manifest', 'manifest.json', '--race']);
    expect(result.status).toBe(2);
    expect(result.stderr).toContain('unknown flag "--race"');
  });

  it('detects a missing assigned test', () => {
    const plan = planFixture(['TestAlpha', 'TestBeta']);
    const outcome = verifyExecution(plan, goJsonl({ runs: ['TestAlpha'] }));
    expect(outcome.missing).toEqual(['TestBeta']);
    expect(outcome.unfinished).toEqual([]);
    expect(outcome.unexpected).toEqual([]);
    expect(outcome.failed).toEqual([]);
    expect(outcome.packagePassed).toBe(true);
  });

  it('treats a run without a terminal pass or skip as unfinished', () => {
    const plan = planFixture(['TestAlpha']);
    const jsonl = [
      jsonlEvent({ Action: 'start', Package: importPath }),
      jsonlEvent({ Action: 'run', Package: importPath, Test: 'TestAlpha' }),
    ].join('\n');
    const outcome = verifyExecution(plan, `${jsonl}\n`);
    expect(outcome.missing).toEqual(['TestAlpha']);
    expect(outcome.unfinished).toEqual([]);
    expect(outcome.packagePassed).toBe(false);
  });

  it('fails a truncated stream that stopped after run', () => {
    const plan = planFixture(['TestAlpha']);
    const truncated = [
      jsonlEvent({ Action: 'start', Package: importPath }),
      jsonlEvent({ Action: 'run', Package: importPath, Test: 'TestAlpha' }),
    ].join('\n');
    const outcome = verifyExecution(plan, truncated);
    expect(outcome.executed).toEqual([]);
    expect(outcome.missing).toEqual(['TestAlpha']);
    expect(outcome.packagePassed).toBe(false);
  });

  it('requires a package-level pass event', () => {
    const plan = planFixture(['TestAlpha']);
    const outcome = verifyExecution(plan, goJsonl({ runs: ['TestAlpha'], packagePass: false }));
    expect(outcome.packagePassed).toBe(false);
    expect(outcome.packageFailed).toBe(false);
    expect(outcome.missing).toEqual([]);
    expect(outcome.unexpected).toEqual([]);
  });

  it('reports a failed test as failed and unfinished', () => {
    const plan = planFixture(['TestAlpha']);
    const outcome = verifyExecution(plan, goJsonl({ runs: [], fails: ['TestAlpha'] }));
    expect(outcome.failed).toEqual(['TestAlpha']);
    expect(outcome.unfinished).toEqual(['TestAlpha']);
    expect(outcome.missing).toEqual(['TestAlpha']);
  });

  it('accepts a skipped test as terminal', () => {
    const plan = planFixture(['TestAlpha']);
    const jsonl = [
      jsonlEvent({ Action: 'start', Package: importPath }),
      jsonlEvent({ Action: 'run', Package: importPath, Test: 'TestAlpha' }),
      jsonlEvent({ Action: 'skip', Package: importPath, Test: 'TestAlpha' }),
      jsonlEvent({ Action: 'pass', Package: importPath }),
    ].join('\n');
    const outcome = verifyExecution(plan, `${jsonl}\n`);
    expect(outcome.missing).toEqual([]);
    expect(outcome.failed).toEqual([]);
    expect(outcome.packagePassed).toBe(true);
  });

  it('detects a test that ran outside the assignment', () => {
    const plan = planFixture(['TestAlpha']);
    const outcome = verifyExecution(plan, goJsonl({ runs: ['TestAlpha', 'TestBeta'] }));
    expect(outcome.unexpected).toEqual(['TestBeta']);
    expect(outcome.missing).toEqual([]);
  });

  it('matches the exact import path only', () => {
    const plan = planFixture(['TestAlpha']);
    expect(parseExecutedTests(goJsonl({ runs: ['TestAlpha'] }), importPath).executed).toEqual([
      'TestAlpha',
    ]);
    expect(
      parseExecutedTests(goJsonl({ pkg: `${importPath}/internal`, runs: ['TestAlpha'] }), importPath)
        .executed
    ).toEqual([]);
    expect(
      parseExecutedTests(goJsonl({ pkg: '', runs: ['TestAlpha'] }), importPath).executed
    ).toEqual([]);
  });

  it('counts subtests as the assigned parent test, not as new tests', () => {
    const plan = planFixture(['TestAlpha']);
    const jsonl = goJsonl({ runs: ['TestAlpha'] }).replace(
      '\n',
      `\n${jsonlEvent({ Action: 'run', Package: importPath, Test: 'TestAlpha/b' })}\n` +
        `${jsonlEvent({ Action: 'pass', Package: importPath, Test: 'TestAlpha/b' })}\n`
    );
    const { executed, failed } = parseExecutedTests(jsonl, importPath);
    expect(executed).toEqual(['TestAlpha']);
    expect(failed).toEqual([]);
    expect(verifyExecution(plan, jsonl)).toMatchObject({
      missing: [],
      unfinished: [],
      unexpected: [],
      failed: [],
      packagePassed: true,
    });
  });

  describe('against the real Go toolchain', () => {
    let moduleDir;
    let packageDir;

    beforeAll(() => {
      moduleDir = mkdtempSync(path.join(tmpdir(), 'go-test-shard-'));
      packageDir = path.join(moduleDir, 'sharded');
      mkdirSync(packageDir, { recursive: true });
      writeFileSync(path.join(moduleDir, 'go.mod'), 'module example.com/sharded\n\ngo 1.22\n');
      writeFileSync(
        path.join(packageDir, 'sharded_test.go'),
        [
          'package sharded',
          '',
          'import "testing"',
          '',
          'func TestAlpha(t *testing.T) {',
          '\tt.Run("b", func(t *testing.T) {})',
          '}',
          '',
          'func TestBeta(t *testing.T)  {}',
          'func TestGamma(t *testing.T) {}',
          '',
        ].join('\n')
      );
      // Enumerated only: `plan` must see this test under -race and must not see
      // it without, because no build without the race detector contains it.
      writeFileSync(
        path.join(packageDir, 'raceonly_test.go'),
        [
          '//go:build race',
          '',
          'package sharded',
          '',
          'import "testing"',
          '',
          'func TestRaceOnly(t *testing.T) {}',
          '',
        ].join('\n')
      );
    });

    afterAll(() => {
      if (moduleDir) rmSync(moduleDir, { recursive: true, force: true });
    });

    it('enumerates race-only files only under --race', () => {
      const manifestPath = path.join(moduleDir, 'race-manifest.json');
      const plain = runTool(
        ['plan', '--pkg', './sharded', '--shards', '3', '--index', '0', '--manifest', manifestPath],
        { cwd: moduleDir }
      );
      expect(plain.status, plain.stderr).toBe(0);
      expect(JSON.parse(readFileSync(manifestPath, 'utf8')).catalog).toEqual([
        'TestAlpha',
        'TestBeta',
        'TestGamma',
      ]);

      const race = runTool(
        [
          'plan',
          '--pkg',
          './sharded',
          '--race',
          '--shards',
          '4',
          '--index',
          '0',
          '--manifest',
          manifestPath,
        ],
        { cwd: moduleDir }
      );
      expect(race.status, race.stderr).toBe(0);
      const raceManifest = JSON.parse(readFileSync(manifestPath, 'utf8'));
      expect(raceManifest.catalog).toEqual([
        'TestAlpha',
        'TestBeta',
        'TestGamma',
        'TestRaceOnly',
      ]);
      expect(raceManifest.importPath).toBe('example.com/sharded/sharded');
      expect(raceManifest.race).toBe(true);
      // `-race -list` builds the race runtime, which is slow on a cold runner.
    }, 120_000);

    it('plans, executes and verifies two shards of a real package', () => {
      const catalog = ['TestAlpha', 'TestBeta', 'TestGamma'];
      const assigned = [];
      for (const index of [0, 1]) {
        const manifestPath = path.join(moduleDir, `manifest-${index}.json`);
        const resultsPath = path.join(moduleDir, `results-${index}.jsonl`);
        const plan = runTool(
          [
            'plan',
            '--pkg',
            './sharded',
            '--shards',
            '2',
            '--index',
            String(index),
            '--manifest',
            manifestPath,
          ],
          { cwd: moduleDir }
        );
        expect(plan.status, plan.stderr).toBe(0);
        const pattern = plan.stdout.trim();
        expect(pattern).toMatch(/^\^\(Test\w+(?:\|Test\w+)*\)\$$/);

        const execution = spawnSync(
          'go',
          ['test', '-count=1', '-json', '-run', pattern, './sharded'],
          { cwd: moduleDir, encoding: 'utf8' }
        );
        expect(execution.status, execution.stderr).toBe(0);
        writeFileSync(resultsPath, execution.stdout);

        const verify = runTool(['verify', '--manifest', manifestPath, '--results', resultsPath], {
          cwd: moduleDir,
        });
        expect(verify.status, verify.stderr).toBe(0);

        const manifest = JSON.parse(readFileSync(manifestPath, 'utf8'));
        expect(manifest.catalog).toEqual(catalog);
        assigned.push(...manifest.assigned);
      }

      expect(assigned.sort()).toEqual(catalog);
    }, 120_000);

    it('exits non-zero when the results stream omits an assigned test', () => {
      const manifestPath = path.join(moduleDir, 'verify-manifest.json');
      const resultsPath = path.join(moduleDir, 'verify-results.jsonl');
      const plan = runTool(
        ['plan', '--pkg', './sharded', '--shards', '2', '--index', '0', '--manifest', manifestPath],
        { cwd: moduleDir }
      );
      expect(plan.status, plan.stderr).toBe(0);
      const manifest = JSON.parse(readFileSync(manifestPath, 'utf8'));
      const omitted = manifest.assigned[0];

      const execution = spawnSync(
        'go',
        ['test', '-count=1', '-json', '-run', runPattern(manifest.assigned.slice(1)), './sharded'],
        { cwd: moduleDir, encoding: 'utf8' }
      );
      expect(execution.status, execution.stderr).toBe(0);
      writeFileSync(resultsPath, execution.stdout);

      const verify = runTool(['verify', '--manifest', manifestPath, '--results', resultsPath], {
        cwd: moduleDir,
      });
      expect(verify.status).toBe(1);
      expect(verify.stderr).toContain(`never ran: ${omitted}`);
    }, 120_000);
  });
});
