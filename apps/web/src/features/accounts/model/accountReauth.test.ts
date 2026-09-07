import { describe, expect, it } from 'vitest';
import { resolveAccountReauthAction } from './accountReauth';

describe('accountReauth', () => {
  it('keeps Codex on the dedicated reauth dialog', () => {
    expect(resolveAccountReauthAction({ name: 'codex.json', type: 'codex' })).toEqual({
      kind: 'codex-dialog',
    });
  });

  it('routes xAI, Claude, and Qoder accounts to their OAuth providers', () => {
    expect(resolveAccountReauthAction({ name: 'xai.json', type: 'xai' })).toEqual({
      kind: 'navigate',
      oauthProvider: 'xai',
      path: '/oauth#oauth-provider-xai',
    });
    expect(resolveAccountReauthAction({ name: 'claude.json', type: 'claude' })).toEqual({
      kind: 'navigate',
      oauthProvider: 'anthropic',
      path: '/oauth#oauth-provider-anthropic',
    });
    expect(resolveAccountReauthAction({ name: 'qoder.json', type: 'qoder' })).toEqual({
      kind: 'navigate',
      oauthProvider: 'qoder',
      path: '/oauth#oauth-provider-qoder',
    });
  });

  it('returns an explicit unsupported action for providers without OAuth login', () => {
    expect(resolveAccountReauthAction({ name: 'vertex.json', type: 'vertex' })).toEqual({
      kind: 'unsupported',
      provider: 'vertex',
    });
  });
});
