import { describe, expect, it } from 'vitest';
import { Sha256Incremental, sha256Hex, sha256RawTextHex } from './apiKeyHash';

describe('sha256Hex', () => {
  it('matches standard SHA-256 hex output and trims input like Usage Service', () => {
    expect(sha256Hex('abc')).toBe(
      'ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad'
    );
    expect(sha256Hex('  abc  ')).toBe(
      'ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad'
    );
    expect(sha256Hex('')).toBe('');
  });
});

describe('sha256RawTextHex', () => {
  it('hashes exact text without trimming and supports empty content', () => {
    expect(sha256RawTextHex(' abc ')).toBe(
      '3eaf1941003943dfaa935adecffcaaa217e290def6fb0181141ced6c9daabaad'
    );
    expect(sha256RawTextHex('')).toBe(
      'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855'
    );
  });
});

describe('Sha256Incremental', () => {
  it('matches the one-shot digest across arbitrary chunk boundaries', () => {
    const input = new TextEncoder().encode('streaming prefix digest '.repeat(37));
    const hasher = new Sha256Incremental();
    for (let offset = 0; offset < input.length; offset += 7) {
      hasher.update(input.subarray(offset, Math.min(input.length, offset + 7)));
    }
    expect(hasher.digestHex()).toBe(sha256RawTextHex(new TextDecoder().decode(input)));
  });

  it('hashes arbitrary binary file bytes without text decoding', () => {
    const input = new Uint8Array(256);
    input.forEach((_, index) => {
      input[index] = index;
    });
    const hasher = new Sha256Incremental();
    for (let offset = 0; offset < input.length; offset += 11) {
      hasher.update(input.subarray(offset, Math.min(input.length, offset + 11)));
    }
    expect(hasher.digestHex()).toBe(
      '40aff2e9d2d8922e47afd4648e6967497158785fbd1da870e7110266bf944880'
    );
  });
});
