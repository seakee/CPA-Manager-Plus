/**
 * Checks whether a credential string can be placed in an HTTP Authorization
 * header value without causing Fetch or XMLHttpRequest to throw a synchronous
 * ByteString TypeError / DOMException before sending any network request.
 *
 * Never trims or normalizes the input so exact-match credential contracts stay
 * intact: rejects empty strings, NUL/LF/CR control bytes, and UTF-16 code units
 * above U+00FF (non-ISO-8859-1 characters such as fullwidth punctuation or
 * box-drawing borders).
 */
export function isValidHeaderCredentialValue(value: string): boolean {
  if (!value) return false;
  for (let i = 0; i < value.length; i += 1) {
    const code = value.charCodeAt(i);
    if (code === 0x00 || code === 0x0a || code === 0x0d || code > 0xff) {
      return false;
    }
  }
  return true;
}
