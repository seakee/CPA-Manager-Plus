import { describe, expect, it } from 'vitest';
import { isOAuthAliasDraftDirty } from './oauthEditorState';

describe('oauthEditorState', () => {
  it('detects force-mapping and fork changes', () => {
    const initial = [{ name: 'source', alias: 'target', fork: true, forceMapping: true }];
    expect(isOAuthAliasDraftDirty([{ ...initial[0], fork: false }], initial)).toBe(true);
  });
});
