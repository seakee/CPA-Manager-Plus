import { describe, expect, it } from 'vitest';
import {
  isCodexChatgptAccountIdInvalid,
  resolveCodexChatgptAccountId,
  resolveCodexChatgptAccountIdEvidence,
} from './resolvers';

describe('resolveCodexChatgptAccountId', () => {
  it.each([
    { field: 'chatgpt_account_id', value: 'chatgpt-account' },
    { field: 'chatgptAccountId', value: 'chatgpt-account-camel' },
    { field: 'account_id', value: 'account' },
    { field: 'accountId', value: 'account-camel' },
  ])('reads a direct $field string', ({ field, value }) => {
    expect(resolveCodexChatgptAccountId({ name: 'codex.json', [field]: ` ${value} ` })).toBe(value);
  });

  it('still extracts an account ID from an id_token payload object', () => {
    expect(
      resolveCodexChatgptAccountId({
        name: 'codex.json',
        id_token: { account_id: 'token-account' },
      })
    ).toBe('token-account');
  });

  it('rejects conflicting explicit Workspace evidence instead of using the first candidate', () => {
    const file = {
      name: 'codex.json',
      account_id: 'workspace-a',
      metadata: { id_token: { chatgpt_account_id: 'workspace-b' } },
    };
    expect(resolveCodexChatgptAccountId(file)).toBeNull();
    expect(resolveCodexChatgptAccountIdEvidence(file)).toEqual({ accountId: null, invalid: true });
    expect(isCodexChatgptAccountIdInvalid(file)).toBe(true);
  });

  it('rejects malformed explicit Workspace evidence even when another candidate is valid', () => {
    const file = {
      name: 'codex.json',
      account_id: 'workspace-a',
      chatgptAccountId: { unexpected: 'object' },
    };
    expect(resolveCodexChatgptAccountId(file)).toBeNull();
    expect(isCodexChatgptAccountIdInvalid(file)).toBe(true);
  });

  it('accepts matching direct and JWT Workspace evidence', () => {
    const payload = btoa(JSON.stringify({ chatgpt_account_id: 'workspace-a' }))
      .replace(/\+/g, '-')
      .replace(/\//g, '_')
      .replace(/=+$/, '');
    expect(
      resolveCodexChatgptAccountId({
        name: 'codex.json',
        account_id: ' workspace-a ',
        id_token: `header.${payload}.signature`,
      })
    ).toBe('workspace-a');
  });

  it.each([
    ['workspace-a\t', 'workspace-a\t'],
    ['workspace-a\u2003', 'workspace-a\u2003'],
    ['workspace-a\u0000', 'workspace-a\u0000'],
    ['工作区-a', '工作区-a'],
  ])('preserves opaque Workspace value %j', (value, expected) => {
    expect(resolveCodexChatgptAccountId({ name: 'codex.json', account_id: ` ${value} ` })).toBe(expected);
  });
});
