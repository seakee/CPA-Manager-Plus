import { describe, expect, it } from 'vitest';
import { addOAuthExcludedRule, serializeOAuthExcludedRules } from './oauthExcludedRules';

describe('oauthExcludedRules', () => {
  it('normalizes, deduplicates and preserves wildcard rules', () => {
    const rules = addOAuthExcludedRule(['GPT-5-*'], '  *-preview ');
    expect(serializeOAuthExcludedRules(rules)).toEqual(['*-preview', 'gpt-5-*']);
  });
});
