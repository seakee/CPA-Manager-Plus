import { describe, expect, it } from 'vitest';
import { keyDisplayName, validKey, type Key } from './freshKeyMetadata';

// The installation's Key and the keys an adopted CPA already carried are the
// same canonical record, so the page must accept both — and it must accept the
// mask each shape can produce, never a mask the other origin owns.
const workspaceKey: Key = {
  id: 'a'.repeat(32),
  requestId: 'b'.repeat(32),
  maskedPreview: 'cpamp_sk_…1a2b',
  createdAtMs: 1,
  managerState: 'persisted',
  runtimeState: 'not_applied',
  name: '',
  nameRevision: 1,
  lifecycle: 'active',
  origin: 'workspace',
};

const installerKey: Key = { ...workspaceKey, maskedPreview: 'sk-…Z9y8', origin: 'installer' };

// An adopted key keeps the source CPA's own shape: an ellipsis and the last four
// characters, or the ellipsis alone when the secret is too short to mask.
const adoptedKey: Key = { ...workspaceKey, maskedPreview: '…y-01', origin: 'adopted' };

describe('validKey', () => {
  it('accepts each origin with the mask its secret shape produces', () => {
    expect(validKey(workspaceKey)).toBe(true);
    expect(validKey(installerKey)).toBe(true);
    expect(validKey(adoptedKey)).toBe(true);
    expect(validKey({ ...adoptedKey, maskedPreview: '…' })).toBe(true);
  });

  it('refuses a mask that belongs to another origin', () => {
    expect(validKey({ ...workspaceKey, maskedPreview: 'sk-…Z9y8' })).toBe(false);
    expect(validKey({ ...installerKey, maskedPreview: '…y-01' })).toBe(false);
    expect(validKey({ ...adoptedKey, maskedPreview: 'cpamp_sk_…1a2b' })).toBe(false);
    expect(validKey({ ...adoptedKey, maskedPreview: 'source-client-one' })).toBe(false);
  });

  it('refuses an origin outside the closed set', () => {
    expect(validKey({ ...adoptedKey, origin: 'external' as Key['origin'] })).toBe(false);
  });
});

describe('keyDisplayName', () => {
  it('translates the stored default name of an installation or adopted key', () => {
    expect(keyDisplayName(installerKey, 'Installation key', 'Adopted key', 'Unnamed')).toBe(
      'Installation key'
    );
    expect(keyDisplayName(adoptedKey, 'Installation key', 'Adopted key', 'Unnamed')).toBe(
      'Adopted key'
    );
  });

  it('keeps a rename authoritative on every origin', () => {
    expect(
      keyDisplayName({ ...adoptedKey, name: 'edge', nameRevision: 2 }, 'Installation key', 'Adopted key', 'Unnamed')
    ).toBe('edge');
    expect(keyDisplayName(workspaceKey, 'Installation key', 'Adopted key', 'Unnamed')).toBe(
      'Unnamed'
    );
  });
});
