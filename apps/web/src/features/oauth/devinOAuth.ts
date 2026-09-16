export interface DevinCallbackValidationResult {
  valid: boolean;
  errorKey?: string;
}

/**
 * Validates a user-provided Devin callback URL without modifying it.
 *
 * Rules:
 * 1. Must parse with URL()
 * 2. Protocol must be http: or https:
 * 3. Exactly one non-empty state parameter
 * 4. At least one of code, error, or error_description present
 * 5. State must match expectedState when provided
 * 6. Does NOT restrict hostname, port, or pathname
 */
export function validateDevinCallback(
  input: string,
  expectedState?: string
): DevinCallbackValidationResult {
  const trimmed = input.trim();
  if (!trimmed) {
    return { valid: false, errorKey: 'auth_login.devin_callback_invalid' };
  }

  let parsed: URL;
  try {
    parsed = new URL(trimmed);
  } catch {
    return { valid: false, errorKey: 'auth_login.devin_callback_invalid' };
  }

  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
    return { valid: false, errorKey: 'auth_login.devin_callback_invalid' };
  }

  const allStates = parsed.searchParams.getAll('state');
  if (allStates.length !== 1) {
    return { valid: false, errorKey: 'auth_login.devin_callback_invalid' };
  }

  const callbackState = allStates[0].trim();
  if (!callbackState) {
    return { valid: false, errorKey: 'auth_login.devin_callback_invalid' };
  }

  const code = parsed.searchParams.get('code')?.trim();
  const error = parsed.searchParams.get('error')?.trim();
  const errorDescription = parsed.searchParams.get('error_description')?.trim();

  if (!code && !error && !errorDescription) {
    return { valid: false, errorKey: 'auth_login.devin_callback_invalid' };
  }

  if (!expectedState?.trim() || callbackState !== expectedState.trim()) {
    return {
      valid: false,
      errorKey: 'auth_login.devin_callback_state_mismatch',
    };
  }

  return { valid: true };
}
