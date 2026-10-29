/** A missing REST percentage is unknown. Only a complete, successful gRPC
 * response for the same active period can establish a proto3 implicit zero.
 * See CodexBar #3261/#3325 and CPA Manager Plus #710.
 */
export const XAI_WEB_BILLING_URL =
  'https://grok.com/grok_api_v2.GrokBuildBilling/GetGrokCreditsConfig';
export const XAI_WEB_BILLING_DATA = 'AAAAAAIIAA==';
export const XAI_WEB_BILLING_HEADERS = {
  Authorization: 'Bearer $TOKEN$',
  'Content-Type': 'application/grpc-web-text+proto',
  Accept: 'application/grpc-web-text+proto',
  'x-grpc-web': '1',
  Origin: 'https://grok.com',
};

// Only schema-declared messages are traversed; unknown bytes remain opaque.
const messages = new Set([
  '1',
  '1.2',
  '1.3',
  '1.4',
  '1.5',
  '1.6',
  '1.7',
  '1.8',
  '1.12',
  '1.6.1',
  '1.6.2',
  '1.6.3',
  '1.8.2',
  '1.8.3',
  '1.6.3.2',
  '1.6.3.3',
]);

export function isXaiValidatedZero(
  text: string,
  start: string | undefined,
  end: string | undefined,
  now = Date.now()
): boolean {
  try {
    const from = Date.parse(start ?? '');
    const until = Date.parse(end ?? '');
    if (!(from <= now && now < until) || text.length > 64 * 1024) return false;
    // grpc-web-text may flush multiple separately padded base64 chunks.
    const encoded = text.replace(/\s/g, '');
    if (!encoded || encoded.length % 4 || /[^A-Za-z0-9+/=]/.test(encoded)) return false;
    const chunks = encoded.match(/[A-Za-z0-9+/]+={0,2}/g) ?? [];
    if (
      chunks.join('') !== encoded ||
      chunks.some((chunk) => chunk.length % 4 || btoa(atob(chunk)) !== chunk)
    )
      return false;
    const bytes = Uint8Array.from(chunks.map((chunk) => atob(chunk)).join(''), (c) =>
      c.charCodeAt(0)
    );
    let offset = 0;
    let data: Uint8Array | undefined;
    let done = false;
    while (offset < bytes.length) {
      if (done || offset + 5 > bytes.length) return false;
      const flag = bytes[offset];
      const length = new DataView(bytes.buffer).getUint32(offset + 1);
      offset += 5;
      if (offset + length > bytes.length) return false;
      const payload = bytes.slice(offset, offset + length);
      offset += length;
      if (flag === 0 && !data) data = payload;
      else if (flag === 128 && data) {
        const trailer = new TextDecoder('utf-8', { fatal: true }).decode(payload);
        const statuses = trailer.split('\r\n').filter((line) => /^grpc-status:/i.test(line));
        if (statuses.length !== 1 || !/^grpc-status:\s*0\s*$/i.test(statuses[0])) return false;
        done = true;
      } else return false;
    }
    if (!data || !done) return false;
    const values = new Map<string, bigint>();
    const seen = new Set<string>();
    const scan = (buffer: Uint8Array, path: string) => {
      let i = 0;
      const varint = (): bigint => {
        let value = 0n;
        for (let shift = 0; shift < 70; shift += 7) {
          if (i >= buffer.length) throw Error('truncated');
          const b = buffer[i++];
          if (shift === 63 && b > 1) throw Error('overflow');
          value |= BigInt(b & 127) << BigInt(shift);
          if (b < 128) return value;
        }
        throw Error('overflow');
      };
      while (i < buffer.length) {
        const tag = varint();
        const field = tag >> 3n;
        const wire = Number(tag & 7n);
        if (field < 1n || field > 536870911n) throw Error('invalid tag');
        const key = path ? `${path}.${field}` : `${field}`;
        if (key === '1.1' || (messages.has(key) && wire !== 2))
          throw Error('unexpected field type');
        // Duplicate structural fields could hide a conflicting period/config.
        if (['1', '1.8', '1.8.1', '1.8.2', '1.8.3', '1.8.2.1', '1.8.3.1'].includes(key)) {
          if (seen.has(key)) throw Error('duplicate');
          seen.add(key);
        }
        if (wire === 0) values.set(key, varint());
        else if (wire === 2) {
          const length = varint();
          if (length > BigInt(buffer.length - i)) throw Error('truncated');
          const next = i + Number(length);
          if (messages.has(key)) scan(buffer.slice(i, next), key);
          i = next;
        } else if (wire === 1) i += 8;
        // Any float could contradict implicit zero. Never infer from it.
        else throw Error('not an implicit zero');
        if (i > buffer.length) throw Error('truncated');
      }
    };
    scan(data, '');
    return (
      values.get('1.8.1') === 2n &&
      values.get('1.8.2.1') === BigInt(Math.floor(from / 1000)) &&
      values.get('1.8.3.1') === BigInt(Math.floor(until / 1000))
    );
  } catch {
    return false;
  }
}
