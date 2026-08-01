import { describe, expect, it } from 'vitest';
import { hasCaseInsensitiveAliasConflict } from './aliasValidation';

describe('hasCaseInsensitiveAliasConflict', () => {
  it('detects aliases that differ only by case', () => {
    expect(hasCaseInsensitiveAliasConflict(['GPT-5'], 'gpt-5')).toBe(true);
  });

  it('allows a rename that only changes the current alias casing', () => {
    expect(hasCaseInsensitiveAliasConflict(['GPT-5'], 'gpt-5', 'GPT-5')).toBe(false);
  });

  it('still detects another alias with the same normalized value', () => {
    expect(hasCaseInsensitiveAliasConflict(['GPT-5', 'gpt-5'], 'Gpt-5', 'GPT-5')).toBe(true);
  });
});
