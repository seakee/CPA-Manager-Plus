import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import vm from 'node:vm';
import assert from 'node:assert/strict';
import { describe, expect, it } from 'vitest';
import { createRedactor, stderrLines } from '../bin/ci/fresh-adopt-redaction.mjs';

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const harnessPath = path.join(repoRoot, 'bin/ci/fresh-adopt-network.mjs');

// One value in the shape the run generates, and one that contains it: a Client
// Key and a longer secret with the same prefix.
const key = 'sk-proj-m3taDatakeyValue0011223344556677';
const longer = key + '-copy';
const redact = createRedactor([key, longer]);

describe('the D acceptance redaction boundary', () => {
  it('the stderr a failed run prints carries no secret', () => {
    // The failure is what an abandoned check leaves behind: a stack trace, which
    // can quote a value this run generated.
    const report = {
      result: 'FAIL',
      failure:
        'Error: Check failed: copied ' + key + '\n    at check (fresh-adopt-network.mjs:1:1)',
    };
    // The assertion looks at the lines as the run built them ...
    const lines = stderrLines(report);
    expect(lines.join('\n')).toContain(key);
    // ... and the console is handed the redacted ones.
    const printed = lines.map(redact).join('\n');
    expect(printed).not.toContain(key);
    expect(printed).toContain('[REDACTED]');
    // The stack frame stays readable: the failure is diagnosis, not a dump.
    expect(printed).toContain('at check (fresh-adopt-network.mjs:1:1)');
  });

  it('a secret that contains another is redacted whole', () => {
    expect(redact('value ' + longer)).toBe('value [REDACTED]');
    expect(redact('value ' + key)).toBe('value [REDACTED]');
  });

  it('the secret-check text is redacted on the same boundary', () => {
    const report = { result: 'FAIL', secretFailure: 'a secret value reached ' + longer };
    expect(stderrLines(report).map(redact).join('\n')).toBe('a secret value reached [REDACTED]');
  });

  it('a passing run prints no stderr at all', () => {
    expect(stderrLines({ result: 'PASS' })).toEqual([]);
  });

  it('a redactor follows the runtime secrets the run appends', () => {
    const secrets = [key];
    const live = createRedactor(secrets);
    const staged = 'cpamp-staged-' + 'f'.repeat(24);
    expect(live(staged)).toBe(staged);
    // The staged destination key only exists once the Supervisor has made one.
    secrets.push(staged);
    expect(live(staged)).toBe('[REDACTED]');
  });
});

// The harness's own tail is where a secret could still leave the run after every
// other boundary has passed. It is a script, so the only way to test the
// statements it actually executes is to execute them: the slice below is the
// final `finally` block, verbatim from the file, run against a report that holds
// a generated value.
const harnessSource = readFileSync(harnessPath, 'utf8');
const tailStart = "  report.secretAssertion = 'PASS';";
const tailEnd = "  if (report.result !== 'PASS') process.exitCode = 1;";
const tailFrom = harnessSource.indexOf(tailStart);
const tailTo = harnessSource.indexOf(tailEnd);
if (tailFrom < 0 || tailTo < 0 || tailTo < tailFrom) {
  throw new Error(
    'bin/ci/fresh-adopt-network.mjs no longer has the tail this test drives: update the slice, not the assertions'
  );
}
const tailSource = harnessSource
  .slice(tailFrom, tailTo)
  // The artifact writes are not what this test is about, and the directory it
  // names does not exist here.
  .replaceAll(/writeFileSync\([\s\S]*?\{ mode: 0o600 \}\s*\);/g, '/* write elided */');

const assertNoSecret = harnessSource.slice(
  harnessSource.indexOf('function assertNoSecret(where,'),
  harnessSource.indexOf('function runContainerLogs()')
);
if (!assertNoSecret) throw new Error('the harness no longer declares assertNoSecret');

const sentinel = 'redaction-boundary-synthetic-management-key';
const sentinelTail = sentinel + '-and-a-longer-staged-secret';

function driveTail(source) {
  const stdout = [];
  const stderr = [];
  const report = {
    result: 'FAIL',
    gitHead: '0'.repeat(40),
    failure: 'Error: page.fill failed with ' + sentinelTail + ' and ' + sentinel + '\n    at x',
  };
  const secretValues = [sentinel, sentinelTail];
  vm.runInNewContext([assertNoSecret, source].join('\n'), {
    secretValues,
    redact: createRedactor(secretValues),
    stderrLines,
    report,
    printed: [],
    containerLogs: [],
    assert,
    resolve: (from, to) => path.resolve(from, to),
    out: path.join(repoRoot, 'bin/tmp'),
    logDir: path.join(repoRoot, 'bin/tmp'),
    writeFileSync: () => undefined,
    console: {
      log: (value) => stdout.push(String(value)),
      error: (value) => stderr.push(String(value)),
    },
  });
  return { report, stdout, stderr };
}

describe('the harness tail', () => {
  it('prints no secret once it has detected one', () => {
    const run = driveTail(tailSource);
    // The assertion did look at the unredacted report: it failed, and it named
    // the reason. Redaction is what makes the lines safe to read, not what makes
    // them safe.
    expect(run.report.secretAssertion).toBe('FAIL');
    expect(run.report.secretFailure).toBeTruthy();
    const printed = [...run.stdout, ...run.stderr].join('\n');
    expect(printed).not.toContain(sentinel);
    expect(printed).not.toContain(sentinelTail);
    expect(printed).toContain('[REDACTED]');
    // The result line stays readable: it is the one place a run says where its
    // artifact is.
    expect(run.stdout.join('\n')).toContain('FAIL: ');
  });

  it('is the only path the final output takes', () => {
    // The same drive, with the tail's own print statements regressed to the raw
    // failure. It must leak, or this test proves nothing about the file.
    const regressed = tailSource.replace(
      'for (const line of stderrLines(report)) console.error(redact(line));',
      'console.error(report.failure);'
    );
    expect(regressed).not.toBe(tailSource);
    const run = driveTail(regressed);
    expect(run.stderr.join('\n')).toContain(sentinel);
  });
});
