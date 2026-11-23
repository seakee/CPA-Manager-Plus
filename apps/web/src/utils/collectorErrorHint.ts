export type CollectorErrorHintKey =
  | 'runtime_status.collector_hint_resp_http_mismatch'
  | 'runtime_status.collector_hint_resp_disabled'
  | 'runtime_status.collector_hint_auth_refused';

/**
 * Maps well-known low-level collector transport errors to an actionable i18n
 * troubleshooting hint while leaving the verbatim raw error visible alongside it.
 */
export function resolveCollectorErrorHintKey(
  rawError?: string | null
): CollectorErrorHintKey | null {
  const message = String(rawError ?? '').trim().toLowerCase();
  if (!message) {
    return null;
  }
  if (
    message.includes('management credential refused') ||
    message.includes('invalid management key')
  ) {
    return 'runtime_status.collector_hint_auth_refused';
  }
  if (message.includes('redis usage output disabled')) {
    return 'runtime_status.collector_hint_resp_disabled';
  }
  if (
    message.includes('unsupported resp prefix') ||
    message.includes('unexpected resp')
  ) {
    return 'runtime_status.collector_hint_resp_http_mismatch';
  }
  return null;
}
