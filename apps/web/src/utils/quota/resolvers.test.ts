import { describe, expect, it } from 'vitest';
import { resolveCodexChatgptAccountId } from './resolvers';

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
});
