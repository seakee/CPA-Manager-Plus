import { describe, expect, it } from 'vitest';
import { resolveAccountReauthAction } from './accountReauth';

describe('accountReauth', () => {
  it('keeps Codex on the dedicated reauth dialog', () => {
    expect(resolveAccountReauthAction({ name: 'codex.json', type: 'codex' })).toEqual({
      kind: 'codex-dialog',
    });
  });

  it('routes xAI and Claude accounts to their OAuth providers', () => {
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
    expect(resolveAccountReauthAction({ name: 'devin.json', type: 'devin' })).toEqual({
      kind: 'navigate',
      oauthProvider: 'devin',
      path: '/oauth#oauth-provider-devin',
    });
    expect(resolveAccountReauthAction({ name: 'kimi-ai.json', type: 'kimi-ai' })).toEqual({
      kind: 'navigate',
      oauthProvider: 'kimi-ai',
      path: '/oauth#oauth-provider-kimi-ai',
    });
    expect(resolveAccountReauthAction({ name: 'meta.json', type: 'meta' })).toEqual({
      kind: 'navigate',
      oauthProvider: 'meta',
      path: '/oauth#oauth-provider-meta',
    });
  });

  it('returns an explicit unsupported action for providers without OAuth login', () => {
    expect(resolveAccountReauthAction({ name: 'vertex.json', type: 'vertex' })).toEqual({
      kind: 'unsupported',
      provider: 'vertex',
    });
  });
});
