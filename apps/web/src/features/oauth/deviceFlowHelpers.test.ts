import { describe, expect, it } from 'vitest';
import { extractDeviceFlowDetails } from './deviceFlowHelpers';

describe('device flow helpers', () => {
  it('extracts and removes a GitHub-style user code', () => {
    expect(extractDeviceFlowDetails('https://github.com/login/device?user_code=abcd-efgh')).toEqual(
      {
        userCode: 'ABCD-EFGH',
        verificationUrl: 'https://github.com/login/device',
      }
    );
  });

  it('preserves unrelated verification URL parameters', () => {
    expect(
      extractDeviceFlowDetails(
        'https://example.com/device?prompt=select_account&user_code=ABCD-EFGH'
      )
    ).toEqual({
      userCode: 'ABCD-EFGH',
      verificationUrl: 'https://example.com/device?prompt=select_account',
    });
  });

  it('ignores malformed or unrelated URLs', () => {
    expect(extractDeviceFlowDetails('not-a-url')).toBeNull();
    expect(extractDeviceFlowDetails('https://example.com/device?user_code=secret')).toBeNull();
  });
});
