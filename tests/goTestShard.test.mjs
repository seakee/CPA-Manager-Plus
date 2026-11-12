import { spawnSync } from 'node:child_process';
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { afterAll, describe, expect, it } from 'vitest';
import {
  buildPlan,
  parseExecutedTests,
  parseTestCatalog,
  runPattern,
  shardAssignments,
  verifyExecution,
} from '../bin/ci/go-test-shard.mjs';

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const toolPath = path.join(repoRoot, 'bin', 'ci', 'go-test-shard.mjs');

const catalogFixture = (count) =>
  Array.from({ length: count }, (_, index) => `TestCase${String(index).padStart(2, '0')}`);

const jsonlEvent = (event) => JSON.stringify(event);

const goJsonl = ({ pkg, runs, fails = [] }) => {
  const lines = [
    jsonlEvent({ Time: '2026-01-01T00:00:00Z', Action: 'start', Package: pkg }),
    jsonlEvent({ Time: '2026-01-01T00:00:00Z', Action: 'output', Package: pkg, Output: 'ok\n' }),
  ];
  for (const name of runs) {
    lines.push(jsonlEvent({ Time: '2026-01-01T00:00:00Z', Action: 'run', Package: pkg, Test: name }));
    lines.push(
      jsonlEvent({ Time: '2026-01-01T00:00:00Z', Action: 'pass', Package: pkg, Test: name })
    );
  }
  for (const name of fails) {
    lines.push(jsonlEvent({ Time: '2026-01-01T00:00:00Z', Action: 'fail', Package: pkg, Test: name }));
  }
  if (fails.length > 0) {
    lines.push(jsonlEvent({ Time: '2026-01-01T00:00:00Z', Action: 'fail', Package: pkg }));
  } else {
    lines.push(jsonlEvent({ Time: '2026-01-01T00:00:00Z', Action: 'pass', Package: pkg }));
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
    const plan = buildPlan({ pkg: './pkg', shards: 3, index: 1, catalog });

    expect(plan.assigned).toEqual(
      catalog.filter((_, position) => position % 3 === 1)
    );
    expect(plan.catalog).toEqual(catalog);
    expect(plan.race).toBe(false);
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
      const plan = buildPlan({ pkg: './pkg', shards, index, catalog });
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
    expect(() => buildPlan({ pkg: './pkg', shards: 3, index: 0, catalog })).toThrow(/exceed/);
    expect(() => buildPlan({ pkg: './pkg', shards: 0, index: 0, catalog })).toThrow(/positive/);
    expect(() => buildPlan({ pkg: './pkg', shards: 1.5, index: 0, catalog })).toThrow(/positive/);
    expect(() => buildPlan({ pkg: './pkg', shards: 2, index: -1, catalog })).toThrow(/index/);
    expect(() => buildPlan({ pkg: './pkg', shards: 2, index: 2, catalog })).toThrow(/index/);
    expect(() => buildPlan({ pkg: './pkg', shards: 2, index: 0, catalog: [] })).toThrow(/no tests/);
  });

  it('parses the go test -list catalog and ignores benchmarks', () => {
    const output = [
      'TestBeta',
      'BenchmarkAlpha',
      'TestAlpha',
      'ok  \texample.com/pkg\t0.123s',
      '',
    ].join('\n');
    expect(parseTestCatalog(output)).toEqual(['TestAlpha', 'TestBeta']);
  });

  it('rejects Example, Fuzz and unrecognized list output', () => {
    expect(() => parseTestCatalog('TestAlpha\nExampleAlpha\nok  \tp\t0.1s')).toThrow(
      /extend the sharder/
    );
    expect(() => parseTestCatalog('TestAlpha\nFuzzAlpha\nok  \tp\t0.1s')).toThrow(
      /extend the sharder/
    );
    expect(() => parseTestCatalog('TestAlpha\nnot-a-test\nok  \tp\t0.1s')).toThrow(/unrecognized/);
    expect(() => parseTestCatalog('build failed\n')).toThrow(/unrecognized/);
  });

  it('detects a missing assigned test', () => {
    const plan = buildPlan({ pkg: './pkg', shards: 1, index: 0, catalog: ['TestAlpha', 'TestBeta'] });
    const jsonl = goJsonl({ pkg: 'example.com/pkg', runs: ['TestAlpha'] });
    const outcome = verifyExecution(plan, jsonl);
    expect(outcome.missing).toEqual(['TestBeta']);
    expect(outcome.unexpected).toEqual([]);
    expect(outcome.failed).toEqual([]);
  });

  it('detects a test that ran outside the assignment', () => {
    const plan = buildPlan({ pkg: './pkg', shards: 1, index: 0, catalog: ['TestAlpha'] });
    const jsonl = goJsonl({ pkg: 'example.com/pkg', runs: ['TestAlpha', 'TestBeta'] });
    const outcome = verifyExecution(plan, jsonl);
    expect(outcome.unexpected).toEqual(['TestBeta']);
    expect(outcome.missing).toEqual([]);
  });

  it('detects a failing package or test', () => {
    const plan = buildPlan({ pkg: './pkg', shards: 1, index: 0, catalog: ['TestAlpha'] });
    const jsonl = goJsonl({ pkg: 'example.com/pkg', runs: ['TestAlpha'], fails: ['TestAlpha'] });
    const outcome = verifyExecution(plan, jsonl);
    expect(outcome.failed).toContain('TestAlpha');
    expect(outcome.missing).toEqual([]);
    expect(outcome.unexpected).toEqual([]);
  });

  it('counts subtests as the assigned parent test, not as new tests', () => {
    const plan = buildPlan({ pkg: './pkg', shards: 1, index: 0, catalog: ['TestAlpha'] });
    const jsonl = goJsonl({
      pkg: 'example.com/pkg',
      runs: ['TestAlpha', 'TestAlpha/b', 'TestAlpha/b/c'],
    });
    const { executed, failed } = parseExecutedTests(jsonl, './pkg');
    expect(executed).toEqual(['TestAlpha']);
    expect(failed).toEqual([]);
    expect(verifyExecution(plan, jsonl)).toMatchObject({
      missing: [],
      unexpected: [],
      failed: [],
    });
  });

  describe('against the real Go toolchain', () => {
    const moduleDir = mkdtempSync(path.join(tmpdir(), 'go-test-shard-'));

    afterAll(() => {
      rmSync(moduleDir, { recursive: true, force: true });
    });

    it('plans, executes and verifies two shards of a real package', () => {
      writeFileSync(
        path.join(moduleDir, 'go.mod'),
        'module example.com/sharded\n\ngo 1.22\n'
      );
      const packageDir = path.join(moduleDir, 'sharded');
      mkdirSync(packageDir, { recursive: true });
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
  });
});
