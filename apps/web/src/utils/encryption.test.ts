import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

describe('encryption (obfuscation) service', () => {
  beforeEach(() => {
    vi.resetModules();
    vi.stubGlobal('window', {
      location: {
        host: 'localhost:8317',
      },
    });
    vi.stubGlobal('navigator', {
      userAgent: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)',
    });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  describe('format detection', () => {
    it('identifies v1, v2, and plaintext versions correctly', async () => {
      const { getObfuscationVersion, isObfuscated } = await import('./encryption');

      expect(getObfuscationVersion('enc::v1::YWJjZA==')).toBe('v1');
      expect(getObfuscationVersion('enc::v2::YWJjZA==')).toBe('v2');
      expect(getObfuscationVersion('plain-text-value')).toBeNull();
      expect(getObfuscationVersion('')).toBeNull();
      expect(getObfuscationVersion(null as unknown as string)).toBeNull();

      expect(isObfuscated('enc::v1::YWJjZA==')).toBe(true);
      expect(isObfuscated('enc::v2::YWJjZA==')).toBe(true);
      expect(isObfuscated('plain-text-value')).toBe(false);
      expect(isObfuscated('')).toBe(false);
    });
  });

  describe('v2 UA resilience', () => {
    it('produces enc::v2:: prefix and is unaffected by userAgent changes across module reloads', async () => {
      // Set fixed host
      vi.stubGlobal('window', {
        location: {
          host: 'cpa.example.com:8317',
        },
      });

      // First run with Chrome UA
      vi.stubGlobal('navigator', {
        userAgent:
          'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36',
      });

      const { obfuscateData, deobfuscateData: deobfuscate1 } = await import('./encryption');
      const plaintext = JSON.stringify({ key: 'admin-secret-password-xyz', count: 42 });
      const ciphertext = obfuscateData(plaintext);

      expect(ciphertext.startsWith('enc::v2::')).toBe(true);
      expect(deobfuscate1(ciphertext)).toBe(plaintext);

      // Reset modules and change UA to Firefox (simulating browser upgrade / alternate UA)
      vi.resetModules();
      vi.stubGlobal('window', {
        location: {
          host: 'cpa.example.com:8317',
        },
      });
      vi.stubGlobal('navigator', {
        userAgent: 'Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:130.0) Gecko/20100101 Firefox/130.0',
      });

      const { deobfuscateData: deobfuscate2 } = await import('./encryption');
      // deobfuscate must produce identical plaintext regardless of new UA
      expect(deobfuscate2(ciphertext)).toBe(plaintext);
    });
  });

  describe('v1 backward compatibility fixture', () => {
    const FIXTURE_HOST = 'cpa.local:8317';
    const FIXTURE_UA_ORIGINAL = 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Chrome/120.0.0.0';
    const FIXTURE_UA_CHANGED =
      'Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:130.0) Gecko/20100101 Firefox/130.0';
    const FIXTURE_PLAINTEXT =
      '{"apiBase":"http://cpa.local:8317","managementKey":"test-admin-key-888-long-credential"}';
    const FIXTURE_V1_CIPHERTEXT =
      'enc::v1::GE4IXRkwDgscD1tSAVkDFVhaRllKEksPGhEEQUlMXENWRUleDhEPTwsKDgQCTnNWSBVGbxsfGhhBAEtYR14NQygYTlFWTEIfB1VHZA0GAAhFIxUKQSNxXQ==';

    it('can decode historical v1 ciphertext when userAgent matches the original', async () => {
      vi.stubGlobal('window', {
        location: {
          host: FIXTURE_HOST,
        },
      });
      vi.stubGlobal('navigator', { userAgent: FIXTURE_UA_ORIGINAL });

      const { deobfuscateData } = await import('./encryption');
      const decoded = deobfuscateData(FIXTURE_V1_CIPHERTEXT);
      expect(decoded).toBe(FIXTURE_PLAINTEXT);
    });

    it('demonstrates that v1 ciphertext produces corrupted plaintext when userAgent changes', async () => {
      vi.resetModules();
      vi.stubGlobal('window', {
        location: {
          host: FIXTURE_HOST,
        },
      });
      vi.stubGlobal('navigator', {
        userAgent: FIXTURE_UA_CHANGED,
      });

      const { deobfuscateData } = await import('./encryption');
      const decoded = deobfuscateData(FIXTURE_V1_CIPHERTEXT);
      // UA mismatch causes decrypted bytes to diverge from the original plaintext
      expect(decoded).not.toBe(FIXTURE_PLAINTEXT);
    });
  });

  describe('backward-compatible aliases', () => {
    it('preserves encryptData, decryptData, and isEncrypted exports', async () => {
      const { encryptData, decryptData, isEncrypted, obfuscateData, deobfuscateData, isObfuscated } =
        await import('./encryption');

      expect(encryptData).toBe(obfuscateData);
      expect(decryptData).toBe(deobfuscateData);
      expect(isEncrypted).toBe(isObfuscated);
    });
  });
});
