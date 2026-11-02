// The D acceptance's one redaction boundary.
//
// Redaction makes an artifact readable, never safe: the report's own fields and
// the console record stay unredacted so the secret assertion can look at what the
// run actually held, and only the files and streams the run writes pass through
// here. Every value the run generated is declared before it runs, plus the three
// that only exist at runtime — so this module is the only place a value can be
// replaced, and it is the only thing the report, the saved log and the final
// stderr have in common.

/**
 * createRedactor replaces the longest declared secret first, so a secret that
 * contains another is redacted whole rather than left with a readable tail. The
 * secret list is read on every call because the run appends its runtime secrets
 * to one array as they come into existence.
 */
export function createRedactor(secrets) {
  return (text) =>
    [...new Set(secrets.filter(Boolean))]
      .sort((left, right) => right.length - left.length)
      .reduce((message, value) => message.replaceAll(value, '[REDACTED]'), String(text));
}

/**
 * stderrLines is everything a run prints to stderr, exactly as the run built it:
 * the failure (a stack trace can quote a value the run generated) and the
 * secret-check text. Printing redacts each line through the same boundary as the
 * report and the saved log, and the secret assertion examines these lines before
 * that — the output is the last place a secret could reach.
 */
export function stderrLines(report) {
  return [report.failure, report.secretFailure].filter(Boolean);
}
