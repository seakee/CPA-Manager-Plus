import { describe, expect, it } from 'vitest';
import { validateDevinCallback } from './devinOAuth';

describe('validateDevinCallback', () => {
  it('accepts standard loopback /callback with code and matching state', () => {
    const result = validateDevinCallback(
      'http://127.0.0.1:8317/callback?code=abc&state=expected',
      'expected'
    );
    expect(result).toEqual({ valid: true });
  });

  it('accepts alternative path /devin/callback with code and matching state', () => {
    const result = validateDevinCallback(
      'http://127.0.0.1:8317/devin/callback?code=abc&state=expected',
      'expected'
    );
    expect(result).toEqual({ valid: true });
  });

  it('accepts custom domain or forwarded https URL', () => {
    const result = validateDevinCallback(
      'https://proxy.example:9443/custom/callback?code=abc&state=expected',
      'expected'
    );
    expect(result).toEqual({ valid: true });
  });

  it('accepts callback with error or error_description instead of code', () => {
    expect(
      validateDevinCallback(
        'http://127.0.0.1:8317/callback?error=access_denied&state=expected',
        'expected'
      )
    ).toEqual({ valid: true });

    expect(
      validateDevinCallback(
        'http://127.0.0.1:8317/callback?error_description=user_cancelled&state=expected',
        'expected'
      )
    ).toEqual({ valid: true });
  });

  it('rejects invalid or relative URLs', () => {
    expect(validateDevinCallback('not-a-url', 'expected')).toEqual({
      valid: false,
      errorKey: 'auth_login.devin_callback_invalid',
    });
    expect(validateDevinCallback('/callback?code=123&state=expected', 'expected')).toEqual({
      valid: false,
      errorKey: 'auth_login.devin_callback_invalid',
    });
    expect(
      validateDevinCallback('javascript:alert(1)?code=123&state=expected', 'expected')
    ).toEqual({
      valid: false,
      errorKey: 'auth_login.devin_callback_invalid',
    });
  });

  it('rejects missing or empty state', () => {
    expect(
      validateDevinCallback('http://127.0.0.1:8317/callback?code=abc', 'expected')
    ).toEqual({
      valid: false,
      errorKey: 'auth_login.devin_callback_invalid',
    });
    expect(
      validateDevinCallback('http://127.0.0.1:8317/callback?code=abc&state=', 'expected')
    ).toEqual({
      valid: false,
      errorKey: 'auth_login.devin_callback_invalid',
    });
  });

  it('rejects duplicate state parameters', () => {
    expect(
      validateDevinCallback(
        'http://127.0.0.1:8317/callback?code=abc&state=expected&state=another',
        'expected'
      )
    ).toEqual({
      valid: false,
      errorKey: 'auth_login.devin_callback_invalid',
    });
  });

  it('rejects state mismatch when expected state is provided', () => {
    expect(
      validateDevinCallback(
        'http://127.0.0.1:8317/callback?code=abc&state=wrong-state',
        'expected'
      )
    ).toEqual({
      valid: false,
      errorKey: 'auth_login.devin_callback_state_mismatch',
    });
  });

  it('rejects callback when expected state is undefined or empty', () => {
    expect(
      validateDevinCallback(
        'http://127.0.0.1:8317/callback?code=abc&state=state-a',
        undefined
      )
    ).toEqual({
      valid: false,
      errorKey: 'auth_login.devin_callback_state_mismatch',
    });
    expect(
      validateDevinCallback(
        'http://127.0.0.1:8317/callback?code=abc&state=state-a',
        '   '
      )
    ).toEqual({
      valid: false,
      errorKey: 'auth_login.devin_callback_state_mismatch',
    });
  });

  it('rejects callback missing code, error, and error_description', () => {
    expect(
      validateDevinCallback('http://127.0.0.1:8317/callback?state=expected', 'expected')
    ).toEqual({
      valid: false,
      errorKey: 'auth_login.devin_callback_invalid',
    });
  });
});
