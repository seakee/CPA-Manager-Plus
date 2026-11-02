import { describe, expect, it } from 'vitest';
import {
  blockedCodeText,
  reconciliationBoundText,
  reconciliationTotalText,
  upgradeWizardText,
} from './upgradeWizard';

// Every blocking code the two read routes can write. The copy they map onto is
// operator-facing, so a code without a sentence — or a sentence that never
// reached a locale — is a missing explanation, not a cosmetic gap.
const CODES = [
  'upgrade_source_missing',
  'upgrade_source_busy',
  'upgrade_source_lock_unavailable',
  'upgrade_source_symlinked',
  'upgrade_source_unreadable',
  'upgrade_source_version_unsupported',
  'upgrade_disk_insufficient',
  'upgrade_credential_timeout',
  'upgrade_mount_timeout',
  'upgrade_legacy_root_missing',
  'upgrade_legacy_root_unusable',
  'upgrade_preflight_timeout',
  'upgrade_preflight_failed',
  'upgrade_operation_unreadable',
] as const;

const LOCALES = ['en-US', 'zh-CN', 'zh-TW', 'ru'] as const;

describe('upgrade wizard blocked codes', () => {
  it('names every reported code in all four locales', () => {
    const unknown = upgradeWizardText('codeUnknown', 'en-US');
    for (const code of CODES) {
      for (const language of LOCALES) {
        const text = blockedCodeText(code, language);
        expect(text, `${code} in ${language}`).not.toBe('');
        expect(text, `${code} in ${language}`).not.toBe(unknown);
      }
    }
  });

  it('keeps the missing source and the ownership marker apart', () => {
    // A mount with no database and a mount whose marker cannot be inspected are
    // different operator problems; they must not share one sentence.
    for (const language of LOCALES) {
      expect(blockedCodeText('upgrade_source_missing', language)).not.toBe(
        blockedCodeText('upgrade_source_lock_unavailable', language)
      );
    }
    for (const language of LOCALES) {
      expect(blockedCodeText('upgrade_source_missing', language)).not.toBe(
        blockedCodeText('upgrade_legacy_root_missing', language)
      );
    }
  });

  it('keeps a deadline apart from an unreadable source', () => {
    // A slow volume and a broken one need different operator responses, so the
    // two codes must never share one sentence.
    for (const language of LOCALES) {
      expect(blockedCodeText('upgrade_credential_timeout', language)).not.toBe(
        blockedCodeText('upgrade_source_unreadable', language)
      );
    }
  });

  it('keeps a mount that did not answer apart from an unreadable database', () => {
    // A mount that is only slow is a retry; a database that cannot be read is a
    // repair, and the credential's own deadline is a third condition. No two of
    // them may share a sentence, or the operator is sent after the wrong cause.
    for (const language of LOCALES) {
      expect(blockedCodeText('upgrade_mount_timeout', language)).not.toBe(
        blockedCodeText('upgrade_source_unreadable', language)
      );
      expect(blockedCodeText('upgrade_mount_timeout', language)).not.toBe(
        blockedCodeText('upgrade_credential_timeout', language)
      );
    }
  });

  it('translates the mount deadline rather than repeating the English text', () => {
    const english = blockedCodeText('upgrade_mount_timeout', 'en-US');
    for (const language of ['zh-CN', 'zh-TW', 'ru'] as const) {
      expect(blockedCodeText('upgrade_mount_timeout', language)).not.toBe(english);
    }
  });

  it('keeps a linked source apart from an undiscoverable ownership marker', () => {
    for (const language of LOCALES) {
      expect(blockedCodeText('upgrade_source_symlinked', language)).not.toBe(
        blockedCodeText('upgrade_source_lock_unavailable', language)
      );
    }
  });

  it('translates the missing source rather than repeating the English text', () => {
    const english = blockedCodeText('upgrade_source_missing', 'en-US');
    for (const language of ['zh-CN', 'zh-TW', 'ru'] as const) {
      expect(blockedCodeText('upgrade_source_missing', language)).not.toBe(english);
    }
  });
});

// The wizard now boots on the recorded operation and lists a bounded mismatch
// window. Both states are operator-facing, so every revised sentence has to exist
// in all four locales, and the window's total must never be replaced by the
// length of the list.
describe('upgrade wizard resume and bounded history copy', () => {
  const REVISED = [
    'bootTitle',
    'bootHelp',
    'bootReading',
    'bootFailedTitle',
    'bootRetry',
    'resumeNotice',
    'resultReconciliationTotal',
    'resultReconciliationBound',
  ] as const;

  it('translates every revised state in all four locales', () => {
    for (const key of REVISED) {
      expect(upgradeWizardText(key, 'en-US'), key).not.toBe('');
      for (const language of ['zh-CN', 'zh-TW', 'ru'] as const) {
        expect(upgradeWizardText(key, language), `${key} in ${language}`).not.toBe('');
        expect(upgradeWizardText(key, language), `${key} in ${language}`).not.toBe(
          upgradeWizardText(key, 'en-US')
        );
      }
    }
  });

  it('keeps the recorded total separate from the shown window', () => {
    for (const language of LOCALES) {
      const total = reconciliationTotalText(400, language);
      const bound = reconciliationBoundText(366, language);
      expect(total, language).toContain('400');
      expect(bound, language).toContain('366');
      expect(total, language).not.toBe(bound);
    }
  });
});
