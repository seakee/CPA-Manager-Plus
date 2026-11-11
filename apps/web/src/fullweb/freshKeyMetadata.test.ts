import { describe, expect, it } from 'vitest';
import { keyDisplayName, maskedPreviewFor, validKey, validSavedSecret, type Key } from './freshKeyMetadata';

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
const adoptedKey: Key = { ...workspaceKey, maskedPreview: '…-one', origin: 'adopted' };

describe('validKey', () => {
  it('accepts each origin with the mask its secret shape produces', () => {
    expect(validKey(workspaceKey)).toBe(true);
    expect(validKey(installerKey)).toBe(true);
    expect(validKey(adoptedKey)).toBe(true);
    expect(validKey({ ...adoptedKey, maskedPreview: '…' })).toBe(true);
  });

  it('refuses a partial adopted mask', () => {
    expect(validKey({ ...adoptedKey, maskedPreview: '…-tw' })).toBe(false);
    expect(validKey({ ...adoptedKey, maskedPreview: '…-twox' })).toBe(false);
  });

  it('refuses a mask that belongs to another origin', () => {
    expect(validKey({ ...workspaceKey, maskedPreview: 'sk-…Z9y8' })).toBe(false);
    expect(validKey({ ...installerKey, maskedPreview: '…-one' })).toBe(false);
    expect(validKey({ ...adoptedKey, maskedPreview: 'cpamp_sk_…1a2b' })).toBe(false);
    expect(validKey({ ...adoptedKey, maskedPreview: 'source-client-one' })).toBe(false);
  });

  it('refuses an origin outside the closed set', () => {
    expect(validKey({ ...adoptedKey, origin: 'external' as Key['origin'] })).toBe(false);
  });
});

describe('maskedPreviewFor and validSavedSecret', () => {
  it('reproduces the server mask per origin', () => {
    expect(maskedPreviewFor('workspace', 'cpamp_sk_' + 'a'.repeat(64))).toBe('cpamp_sk_…aaaa');
    expect(maskedPreviewFor('installer', 'sk-' + 'B'.repeat(64))).toBe('sk-…BBBB');
    // Four characters are at most a quarter of the secret only from sixteen up.
    expect(maskedPreviewFor('adopted', 'source-client-two')).toBe('…-two');
    expect(maskedPreviewFor('adopted', 'fifteen-chars-1')).toBe('…');
    expect(maskedPreviewFor('adopted', '123456789012345')).toBe('…');
    expect(maskedPreviewFor('adopted', '1234567890123456')).toBe('…3456');
    expect(maskedPreviewFor('adopted', 'has space here')).toBe('');
    expect(maskedPreviewFor('adopted', '')).toBe('');
  });

  it('accepts only the secret the record was made from', () => {
    expect(validSavedSecret(adoptedKey, 'source-client-one')).toBe(true);
    expect(validSavedSecret(adoptedKey, 'source-client-two')).toBe(false);
    expect(validSavedSecret(adoptedKey, maskedPreviewFor('adopted', 'source-client-one'))).toBe(false);
    expect(validSavedSecret(workspaceKey, 'cpamp_sk_' + 'a'.repeat(60) + '1a2b')).toBe(true);
    expect(validSavedSecret(workspaceKey, 'sk-' + 'a'.repeat(64))).toBe(false);
  });

  it('counts characters as the server does for non-ASCII source keys', () => {
    // The server masks by code point and accepts any non-control token.
    const cjk = 'sk-源密钥-0123456789';
    const astral = 'sk-source-key-01😀😀😀😀';
    expect(maskedPreviewFor('adopted', cjk)).toBe('…6789');
    expect(maskedPreviewFor('adopted', astral)).toBe('…😀😀😀😀');
    expect(validKey({ ...adoptedKey, maskedPreview: '…😀😀😀😀' })).toBe(true);
    expect(validSavedSecret({ ...adoptedKey, maskedPreview: '…6789' }, cjk)).toBe(true);
    expect(maskedPreviewFor('adopted', 'x'.repeat(4097))).toBe('');
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
