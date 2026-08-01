import { describe, expect, it } from 'vitest';
import { sha256Hex, sha256RawTextHex } from './apiKeyHash';

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
