import { describe, expect, it } from 'vitest';
import {
  blockedCodeText,
  nextActionText,
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
  // The verification's own vocabulary. Every one of them is answered as a typed
  // code on a route the wizard calls, so every one needs a sentence in every
  // locale: a refusal the operator cannot read is a dead end.
  'upgrade_verify_not_applicable',
  'upgrade_verify_v2_running',
  'upgrade_verify_source_unavailable',
  'upgrade_verify_source_mismatch',
  'upgrade_verify_reconciliation_invalid',
  'upgrade_verify_reconciliation_unacknowledged',
  'upgrade_verify_domains_incomplete',
  'upgrade_verify_binding_missing',
  'upgrade_verify_binding_drifted',
  'upgrade_verify_connection_unproven',
  'upgrade_verify_conflict',
  'upgrade_verify_failed',
  'upgrade_request_invalid',
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

describe('upgrade wizard next actions', () => {
  it('gives the CPA import its own sentence instead of the reconciliation', () => {
    // The import writes the destination and proves it, so the wizard must name
    // it as its own step; a missing one falls back to the reconciliation an
    // operator already finished.
    for (const language of LOCALES) {
      const importAction = nextActionText('cpa_import', language);
      expect(importAction, `cpa_import in ${language}`).not.toBe('');
      expect(importAction, `cpa_import in ${language}`).not.toBe(nextActionText('reconcile', language));
      expect(importAction, `cpa_import in ${language}`).not.toBe(nextActionText('copy_in', language));
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

// The verification is the one place this page acts, so its copy has to say what
// acting does and what waiting means. A locale that falls back to English is a
// missing translation, not a cosmetic gap.
describe('upgrade wizard verification copy', () => {
  const VERIFY = [
    'verifyTitle',
    'verifyHelp',
    'verifySourceRequired',
    'verifyAction',
    'verifyRetry',
    'verifyRunning',
    'verifyDoneTitle',
    'verifyComplete',
    'verifyAlready',
    'verifyFailedTitle',
    'verifyTargetExternal',
    'verifyTargetManaged',
    'verifyReasonLabel',
  ] as const;

  it('translates every verification sentence in all four locales', () => {
    for (const key of VERIFY) {
      expect(upgradeWizardText(key, 'en-US'), key).not.toBe('');
      for (const language of ['zh-CN', 'zh-TW', 'ru'] as const) {
        expect(upgradeWizardText(key, language), `${key} in ${language}`).not.toBe('');
        expect(upgradeWizardText(key, language), `${key} in ${language}`).not.toBe(
          upgradeWizardText(key, 'en-US')
        );
      }
    }
  });

});
