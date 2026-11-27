/**
 * Validates whether a credential string can be placed into an HTTP
 * `Authorization` header (`Bearer <value>` / `Fresh <value>`) without triggering
 * WebIDL ByteString TypeError, Chromium/XHR header rejections, or HTTP parser
 * 400 malformed-header rejections.
 *
 * RFC 9110 §5.5 allows HTAB (0x09), SP (0x20), VCHAR (0x21..0x7E), and obs-text
 * (0x80..0xFF); C0 controls (0x00..0x08, 0x0A..0x1F), DEL (0x7F), and code
 * units > 0xFF are disallowed.
 */
export function isValidHeaderCredentialValue(value: string): boolean {
  if (!value) return false;
  for (let i = 0; i < value.length; i += 1) {
    const code = value.charCodeAt(i);
    if ((code < 0x20 && code !== 0x09) || code === 0x7f || code > 0xff) {
      return false;
    }
  }
  return true;
}

/**
 * Validates an exact-match credential for flows whose server handlers reject
 * surrounding whitespace (`strings.TrimSpace(key) != key`). Rejecting boundary
 * whitespace before Fetch/XHR normalization prevents trailing SP/HTAB in
 * `Bearer ${value}` from being silently stripped on the wire.
 */
export function isValidExactHeaderCredentialValue(value: string): boolean {
  if (!isValidHeaderCredentialValue(value)) return false;
  const first = value.charCodeAt(0);
  const last = value.charCodeAt(value.length - 1);
  if (first === 0x09 || first === 0x20 || last === 0x09 || last === 0x20) {
    return false;
  }
  return value.trim() === value;
}
