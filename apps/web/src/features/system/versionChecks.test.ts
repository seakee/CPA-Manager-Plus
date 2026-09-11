import { describe, expect, it } from 'vitest';
import {
  readApiLatestVersion,
  readManagerLatestTag,
  readManagerStableVersion,
} from './versionChecks';

describe('versionChecks', () => {
  describe('readManagerLatestTag', () => {
    it('extracts tag_name or fallback fields', () => {
      expect(readManagerLatestTag({ tag_name: 'v1.0.0' })).toBe('v1.0.0');
      expect(readManagerLatestTag({ name: '1.0.0' })).toBe('1.0.0');
      expect(readManagerLatestTag({ latest_version: 'v1.1.0' })).toBe('v1.1.0');
      expect(readManagerLatestTag(null)).toBe('');
    });
  });

  describe('readApiLatestVersion', () => {
    it('extracts latest-version or fallback fields', () => {
      expect(readApiLatestVersion({ 'latest-version': 'v2.0.0' })).toBe('v2.0.0');
      expect(readApiLatestVersion({ latest_version: '2.0.0' })).toBe('2.0.0');
      expect(readApiLatestVersion(null)).toBe('');
    });
  });

  describe('readManagerStableVersion', () => {
    it('returns stable version string for valid candidate', () => {
      const payload = {
        schema_version: 1,
        revision: 42,
        generated_at: '2026-09-08T00:00:00Z',
        channels: {
          stable: {
            version: 'v1.12.12',
          },
          rc: null,
          beta: null,
        },
      };

      expect(readManagerStableVersion(payload)).toBe('v1.12.12');
    });

    it('trims version string if surrounded by whitespace', () => {
      const payload = {
        schema_version: 1,
        channels: {
          stable: {
            version: '  v1.12.12  ',
          },
        },
      };

      expect(readManagerStableVersion(payload)).toBe('v1.12.12');
    });

    it('returns null for valid stable=null state (no candidate)', () => {
      const payload = {
        schema_version: 1,
        revision: 43,
        channels: {
          stable: null,
          rc: null,
          beta: null,
        },
      };

      expect(readManagerStableVersion(payload)).toBeNull();
    });

    it('throws when payload is not an object', () => {
      expect(() => readManagerStableVersion(null)).toThrow(
        'Invalid update index: payload must be an object'
      );
      expect(() => readManagerStableVersion('string')).toThrow(
        'Invalid update index: payload must be an object'
      );
      expect(() => readManagerStableVersion(123)).toThrow(
        'Invalid update index: payload must be an object'
      );
      expect(() => readManagerStableVersion([])).toThrow(
        'Invalid update index: payload must be an object'
      );
    });

    it('throws when schema_version is not 1', () => {
      expect(() =>
        readManagerStableVersion({
          schema_version: 2,
          channels: { stable: { version: 'v1.12.12' } },
        })
      ).toThrow('Invalid update index: schema_version must be 1');
    });

    it('throws when channels is missing or not an object', () => {
      expect(() =>
        readManagerStableVersion({
          schema_version: 1,
        })
      ).toThrow('Invalid update index: channels must be an object');

      expect(() =>
        readManagerStableVersion({
          schema_version: 1,
          channels: null,
        })
      ).toThrow('Invalid update index: channels must be an object');
    });

    it('throws when stable channel is missing', () => {
      expect(() =>
        readManagerStableVersion({
          schema_version: 1,
          channels: {
            rc: null,
          },
        })
      ).toThrow('Invalid update index: stable channel is missing');
    });

    it('throws when stable is neither null nor an object', () => {
      expect(() =>
        readManagerStableVersion({
          schema_version: 1,
          channels: {
            stable: 'v1.12.12',
          },
        })
      ).toThrow('Invalid update index: stable channel must be an object or null');

      expect(() =>
        readManagerStableVersion({
          schema_version: 1,
          channels: {
            stable: 123,
          },
        })
      ).toThrow('Invalid update index: stable channel must be an object or null');
    });

    it('throws when stable.version is missing, empty, or not a string', () => {
      expect(() =>
        readManagerStableVersion({
          schema_version: 1,
          channels: {
            stable: {},
          },
        })
      ).toThrow('Invalid update index: stable version must be a non-empty string');

      expect(() =>
        readManagerStableVersion({
          schema_version: 1,
          channels: {
            stable: {
              version: '   ',
            },
          },
        })
      ).toThrow('Invalid update index: stable version must be a non-empty string');

      expect(() =>
        readManagerStableVersion({
          schema_version: 1,
          channels: {
            stable: {
              version: 123,
            },
          },
        })
      ).toThrow('Invalid update index: stable version must be a non-empty string');
    });
  });
});
