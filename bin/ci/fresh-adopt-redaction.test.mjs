import assert from 'node:assert/strict';
import test from 'node:test';
import { createRedactor, stderrLines } from './fresh-adopt-redaction.mjs';

// One value in the shape the run generates, and one that contains it: a Client
// Key and a longer secret with the same prefix.
const key = 'sk-proj-m3taDatakeyValue0011223344556677';
const longer = key + '-copy';
const redact = createRedactor([key, longer]);

test('the stderr a failed run prints carries no secret', () => {
  // The failure is what an abandoned check leaves behind: a stack trace, which
  // can quote a value this run generated.
  const report = {
    result: 'FAIL',
    failure: 'Error: Check failed: copied ' + key + '\n    at check (fresh-adopt-network.mjs:1:1)',
  };
  // The assertion looks at the lines as the run built them ...
  const lines = stderrLines(report);
  assert(lines.join('\n').includes(key));
  // ... and the console is handed the redacted ones.
  const printed = lines.map(redact).join('\n');
  assert(!printed.includes(key), 'the printed failure carried the key');
  assert(printed.includes('[REDACTED]'));
  // The stack frame stays readable: the failure is diagnosis, not a dump.
  assert(printed.includes('at check (fresh-adopt-network.mjs:1:1)'));
});

test('a secret that contains another is redacted whole', () => {
  assert.equal(redact('value ' + longer), 'value [REDACTED]');
  assert.equal(redact('value ' + key), 'value [REDACTED]');
});

test('the secret-check text is redacted on the same boundary', () => {
  const report = { result: 'FAIL', secretFailure: 'a secret value reached ' + longer };
  assert.equal(stderrLines(report).map(redact).join('\n'), 'a secret value reached [REDACTED]');
});

test('a passing run prints no stderr at all', () => {
  assert.deepEqual(stderrLines({ result: 'PASS' }), []);
});

test('a redactor follows the runtime secrets the run appends', () => {
  const secrets = [key];
  const live = createRedactor(secrets);
  const staged = 'cpamp-staged-' + 'f'.repeat(24);
  assert.equal(live(staged), staged);
  // The staged destination key only exists once the Supervisor has made one.
  secrets.push(staged);
  assert.equal(live(staged), '[REDACTED]');
});
