import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import i18n from './index';
import { configurationText as c } from './configurationWorkspace';

// The gateway page is one write per page group. These assertions pin the copy
// that used to describe the removed per-field writes, in every locale the page
// ships, so a translator cannot quietly restore a false statement.
const LOCALES = ['en-US', 'zh-CN', 'zh-TW', 'ru'] as const;

describe('configuration workspace copy', () => {
  let previous: string | undefined;

  beforeAll(() => {
    previous = i18n.resolvedLanguage;
  });

  afterAll(async () => {
    if (previous) await i18n.changeLanguage(previous);
  });

  async function each(locale: (typeof LOCALES)[number]) {
    await i18n.changeLanguage(locale);
    return {
      text: (key: Parameters<typeof c>[0]) => c(key),
    };
  }

  it('describes group saves instead of claiming only retry controls are live', async () => {
    for (const locale of LOCALES) {
      const { text } = await each(locale);
      // "only retry controls read live values" was true for the removed C2 page.
      expect(text('gatewayHelp').toLowerCase()).not.toContain('only retry');
      expect(text('catalogNotice').toLowerCase()).not.toContain('only retry');
      expect(text('catalogNotice')).not.toContain('仅重试');
      expect(text('catalogNotice')).not.toContain('僅重試');
      expect(text('catalogNotice')).not.toContain('только для повторов');
      // Each locale names the group-level write that actually happens.
      expect(text('gatewayHelp').length).toBeGreaterThan(20);
    }
  });

  it('never describes a save as one operation per field', async () => {
    for (const locale of LOCALES) {
      const { text } = await each(locale);
      // "each changed field is saved as a separate operation".
      expect(text('diffHelp')).not.toContain('separate operation');
      expect(text('diffHelp')).not.toContain('独立 operation');
      expect(text('diffHelp')).not.toContain('獨立 operation');
      expect(text('diffHelp')).not.toContain('отдельной операцией');
      // The notice used to talk about "the two retry controls".
      expect(text('diffNotice').toLowerCase()).not.toContain('two retry');
      expect(text('diffNotice')).not.toContain('两个 Retry');
      expect(text('diffNotice')).not.toContain('兩個 Retry');
      expect(text('diffNotice')).not.toContain('Два действия повторов');
    }
  });

  it('does not tell the user that discarding drafts signs them out', async () => {
    for (const locale of LOCALES) {
      const { text } = await each(locale);
      // The per-group discard dialog must describe a discard, not a sign-out.
      for (const key of ['discardTitle', 'discardHelp', 'confirmDiscard'] as const) {
        expect(text(key)).not.toMatch(/sign out|退出登录|登出|退出登入|выйти/i);
      }
      expect(text('discardTitle').length).toBeGreaterThan(5);
    }
    // The session-end dialog keeps its own sign-out wording; the two are distinct.
    const leaving = await each('zh-CN');
    expect(leaving.text('leaveTitle')).toContain('退出登录');
    const discarding = await each('en-US');
    expect(discarding.text('discardTitle')).not.toBe(discarding.text('leaveTitle'));
  });

  it('states each new rejection and control in every locale', async () => {
    const english: Record<string, string> = {};
    for (const locale of LOCALES) {
      const { text } = await each(locale);
      for (const key of [
        'valueInheritOn',
        'valueInheritOff',
        'profileSingleEdit',
        'fieldProxyCredentials',
        'fieldPrivateRange',
        'groupSavePrivateRange',
        'groupSavePrivateRangeDetail',
        'groupSaveProxyCredentials',
        'groupSaveGenerationMismatch',
        'groupSaveLegacyBlocked',
        'groupSaveNoDefault',
        'operationPartial',
        'runtimeLinkHelp',
        'reasonLegacyBlocked',
      ] as const) {
        expect(text(key).trim().length).toBeGreaterThan(0);
        if (locale === 'en-US') english[key] = text(key);
        // A locale that was left as the English fallback would repeat it here.
        else expect(text(key)).not.toBe(english[key] ?? text(key));
      }
      // The control states which CPA default "inherit" resolves to, so the two
      // inherit labels are never the same string.
      expect(text('valueInheritOn')).not.toBe(text('valueInheritOff'));
    }
  });
});
