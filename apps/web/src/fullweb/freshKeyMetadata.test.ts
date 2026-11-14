import { describe, expect, it } from 'vitest';
import {
  effectBlockerTextKey,
  isTerminalKeyOperationOutcome,
  keyDisplayName,
  keyLifecyclePillClass,
  keyLifecycleTextKey,
  keyRuntimePillClass,
  keyRuntimeTextKey,
  maskedPreviewFor,
  validKey,
  validKeyOperationResult,
  validSavedSecret,
  type Key,
  type KeyOperationResult,
} from './freshKeyMetadata';
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

  it('accepts all five backend runtimeState values and validates optional operationId and effectCode', () => {
    for (const runtimeState of [
      'not_applied',
      'applying',
      'effective_observed',
      'effect_unknown',
      'revoked_observed',
    ] as const) {
      expect(validKey({ ...workspaceKey, runtimeState })).toBe(true);
    }
    expect(validKey({ ...workspaceKey, runtimeState: 'invalid' as Key['runtimeState'] })).toBe(false);
    expect(validKey({ ...workspaceKey, operationId: 'c'.repeat(32) })).toBe(true);
    expect(validKey({ ...workspaceKey, operationId: 'bad-id' })).toBe(false);
    expect(validKey({ ...workspaceKey, effectCode: 'key_operation_unknown' })).toBe(true);
    expect(validKey({ ...workspaceKey, effectCode: 'bad code with spaces' })).toBe(false);
  });

  it('accepts missing lifecycle as valid non-retired and rejects unknown lifecycles', () => {
    expect(validKey({ ...workspaceKey, lifecycle: 'missing' })).toBe(true);
    expect(validKey({ ...workspaceKey, lifecycle: 'superseded' })).toBe(true);
    expect(validKey({ ...workspaceKey, lifecycle: undefined })).toBe(true);
    expect(validKey({ ...workspaceKey, lifecycle: 'retired' as Key['lifecycle'] })).toBe(false);
    expect(keyLifecycleTextKey('active')).toBe('saved');
    expect(keyLifecyclePillClass('active')).toBe('is-saved');
    expect(keyLifecycleTextKey('missing')).toBe('missingLifecycle');
    expect(keyLifecyclePillClass('missing')).toBe('is-pending');
    expect(keyLifecycleTextKey('superseded')).toBe('replaced');
    expect(keyLifecyclePillClass('superseded')).toBe('is-muted');
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

describe('validKeyOperationResult and status helpers', () => {
  it('validates add, keeperless revoke, keeper revoke, and replace operations', () => {
    const base: KeyOperationResult = {
      operation: {
        id: '1'.repeat(32),
        targetId: 'a'.repeat(32),
        keeperId: '',
        mode: 'add',
        outcome: 'committed',
        revision: 2,
        steps: [{ name: 'add', state: 'observed', returned: true }],
      },
      runtimeState: 'effective_observed',
      expiresAtMs: 0,
      nextStep: '',
    };
    expect(validKeyOperationResult(base)).toBe(true);
    expect(validKeyOperationResult({ ...base, runtimeState: 'not_applied' })).toBe(true);
    expect(
      validKeyOperationResult({ ...base, runtimeState: 'not_applied', nextStep: 'add' })
    ).toBe(false);
    expect(
      validKeyOperationResult({
        ...base,
        runtimeState: 'not_applied',
        operation: { ...base.operation!, outcome: 'pending' },
      })
    ).toBe(false);
    expect(
      validKeyOperationResult({
        ...base,
        operation: {
          ...base.operation!,
          mode: 'revoke',
          steps: [{ name: 'revoke', state: 'observed', returned: true }],
        },
      })
    ).toBe(true);
    expect(
      validKeyOperationResult({
        ...base,
        operation: {
          ...base.operation!,
          mode: 'revoke',
          keeperId: 'b'.repeat(32),
          steps: [{ name: 'revoke', state: 'observed', returned: true }],
        },
      })
    ).toBe(true);
    expect(
      validKeyOperationResult({
        ...base,
        operation: {
          ...base.operation!,
          mode: 'replace',
          keeperId: 'b'.repeat(32),
          steps: [
            { name: 'add', state: 'observed', returned: true },
            { name: 'revoke', state: 'observed', returned: true },
          ],
        },
      })
    ).toBe(true);
    expect(
      validKeyOperationResult({
        ...base,
        operation: {
          ...base.operation!,
          keeperId: 'b'.repeat(32),
        },
      })
    ).toBe(false);
    expect(
      validKeyOperationResult({
        ...base,
        operation: {
          ...base.operation!,
          outcome: 'committed',
          steps: [{ name: 'add', state: 'unattempted', returned: false }],
        },
      })
    ).toBe(false);
  });

  it('validates aborted operation as strictly unattempted and never effective', () => {
    const abortedBase: KeyOperationResult = {
      operation: {
        id: '1'.repeat(32),
        targetId: 'a'.repeat(32),
        keeperId: 'b'.repeat(32),
        mode: 'replace',
        outcome: 'aborted',
        revision: 2,
        steps: [
          { name: 'add', state: 'unattempted', returned: false },
          { name: 'revoke', state: 'unattempted', returned: false },
        ],
      },
      runtimeState: 'not_applied',
      expiresAtMs: 0,
      nextStep: '',
    };
    expect(validKeyOperationResult(abortedBase)).toBe(true);
    expect(validKeyOperationResult({ ...abortedBase, runtimeState: 'effect_unknown' })).toBe(true);
    expect(validKeyOperationResult({ ...abortedBase, runtimeState: 'effective_observed' })).toBe(false);
    expect(validKeyOperationResult({ ...abortedBase, nextStep: 'add' })).toBe(false);
    expect(
      validKeyOperationResult({
        ...abortedBase,
        operation: {
          ...abortedBase.operation!,
          steps: [
            { name: 'add', state: 'attempted', returned: true },
            { name: 'revoke', state: 'unattempted', returned: false },
          ],
        },
      })
    ).toBe(false);
    expect(isTerminalKeyOperationOutcome('committed')).toBe(true);
    expect(isTerminalKeyOperationOutcome('aborted')).toBe(true);
    expect(isTerminalKeyOperationOutcome('pending')).toBe(false);
    expect(isTerminalKeyOperationOutcome('unknown')).toBe(false);
  });

  it('maps runtime states and effect blocker codes accurately', () => {
    expect(keyRuntimeTextKey('effective_observed')).toBe('runtimeEffectiveObserved');
    expect(keyRuntimePillClass('effective_observed')).toBe('is-effective');
    expect(keyRuntimeTextKey('effect_unknown')).toBe('runtimeEffectUnknown');
    expect(keyRuntimePillClass('effect_unknown')).toBe('is-pending');
    expect(effectBlockerTextKey('external_management_key_rejected')).toBe(
      'effectBlockerExternalRejected'
    );
    expect(effectBlockerTextKey('last_effective_key')).toBe('effectBlockerLastKey');
    expect(effectBlockerTextKey('unrecognized')).toBe('effectBlockerUnknown');
  });
});
