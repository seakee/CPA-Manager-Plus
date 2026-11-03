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
 * createRedactor replaces every declared secret in one pass over the original
 * text. All matches are located first, then the touching or overlapping
 * intervals are merged and written out left to right.
 *
 * Replacing value by value is not equivalent: two secrets that overlap from
 * different starting positions are not nested, so replacing one first destroys
 * the other's full match and leaves its readable remainder in place. A union of
 * intervals cannot leave a fragment of any declared value.
 *
 * The secret list is read on every call because the run appends its runtime
 * secrets to one array as they come into existence.
 */
export function createRedactor(secrets) {
  return (text) => {
    const message = String(text);
    const values = [...new Set(secrets.filter(Boolean))];
    const ranges = [];
    for (const value of values) {
      // Every starting position, including overlapping occurrences of the same
      // value, so the union below is over the text and not over occurrences.
      for (let at = message.indexOf(value); at !== -1; at = message.indexOf(value, at + 1))
        ranges.push([at, at + value.length]);
    }
    if (ranges.length === 0) return message;
    ranges.sort((left, right) => left[0] - right[0] || left[1] - right[1]);
    const merged = [];
    for (const range of ranges) {
      const last = merged[merged.length - 1];
      // Touching intervals merge too: two secrets written back to back are one
      // redacted region, not two replacements that would be indistinguishable
      // from an unredacted neighbour.
      if (last && range[0] <= last[1]) last[1] = Math.max(last[1], range[1]);
      else merged.push([range[0], range[1]]);
    }
    let redacted = '';
    let cursor = 0;
    for (const [start, end] of merged) {
      redacted += message.slice(cursor, start) + '[REDACTED]';
      cursor = end;
    }
    return redacted + message.slice(cursor);
  };
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
