import { describe, expect, it } from 'vitest';
import { resolveCollectorErrorHintKey } from './collectorErrorHint';

describe('resolveCollectorErrorHintKey', () => {
  it('identifies HTTP-on-RESP mismatch errors', () => {
    expect(resolveCollectorErrorHintKey("auth: unsupported RESP prefix 'H'")).toBe(
      'runtime_status.collector_hint_resp_http_mismatch'
    );
    expect(resolveCollectorErrorHintKey('unexpected RESP header "HTTP/1.1"')).toBe(
      'runtime_status.collector_hint_resp_http_mismatch'
    );
  });

  it('identifies disabled RESP usage output on upstream CPA', () => {
    expect(resolveCollectorErrorHintKey('ERR redis usage output disabled in home mode')).toBe(
      'runtime_status.collector_hint_resp_disabled'
    );
  });

  it('identifies rejected management credentials', () => {
    expect(
      resolveCollectorErrorHintKey(
        'consume: connection reset; management credential refused: ERR invalid management key'
      )
    ).toBe('runtime_status.collector_hint_auth_refused');
  });

  it('returns null for unrecognized or empty errors', () => {
    expect(resolveCollectorErrorHintKey('')).toBeNull();
    expect(resolveCollectorErrorHintKey(null)).toBeNull();
    expect(resolveCollectorErrorHintKey('dial tcp 127.0.0.1:8317: connection refused')).toBeNull();
  });
});
