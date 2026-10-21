const DEFAULT_ANTHROPIC_VERSION = '2023-06-01';
const CLAUDE_OAUTH_BETA = 'oauth-2025-04-20';
const ANTHROPIC_FIRST_PARTY_URL_PATTERN =
  /^https:\/\/api\.anthropic\.com(?::(?:443)?)?(?:[/?#]|$)/i;

export type ClaudeBaseType = 'anthropic' | 'custom';

export interface ClaudeRequestHeaderOptions {
  url: string;
  apiKey?: string;
  authIndex?: string;
  customHeaders?: Record<string, string>;
  contentType?: string;
}

export interface ClaudeRequestHeaderResolution {
  headers: Record<string, string>;
  baseType: ClaudeBaseType;
  effectiveXApiKey: boolean;
  effectiveAuthorization: boolean;
}

const findHeaderKey = (headers: Record<string, string>, name: string): string | undefined => {
  const target = name.toLowerCase();
  return Object.keys(headers).find((key) => key.toLowerCase() === target);
};

export const hasClaudeHeader = (headers: Record<string, string>, name: string): boolean =>
  findHeaderKey(headers, name) !== undefined;

const deleteHeader = (headers: Record<string, string>, name: string) => {
  const target = name.toLowerCase();
  Object.keys(headers).forEach((key) => {
    if (key.toLowerCase() === target) delete headers[key];
  });
};

const setHeader = (headers: Record<string, string>, name: string, value: string) => {
  deleteHeader(headers, name);
  headers[name] = value;
};

const applyCustomHeaders = (
  headers: Record<string, string>,
  customHeaders: Record<string, string>
) => {
  Object.entries(customHeaders).forEach(([rawName, rawValue]) => {
    const name = String(rawName ?? '').trim();
    const value = String(rawValue ?? '').trim();
    if (!name || !value) return;
    setHeader(headers, name, value);
  });
};

export const isAnthropicFirstPartyUrl = (value: string): boolean => {
  const raw = String(value ?? '').trim();
  // Keep this gate on the raw authority instead of WHATWG URL normalization.
  // CPA's Go gate accepts only https://api.anthropic.com with an empty port or exact :443.
  return ANTHROPIC_FIRST_PARTY_URL_PATTERN.test(raw);
};

export const isClaudeOAuthToken = (value: string): boolean =>
  String(value ?? '').includes('sk-ant-oat');

export const buildClaudeRequestHeaders = (
  options: ClaudeRequestHeaderOptions
): ClaudeRequestHeaderResolution => {
  const customHeaders = options.customHeaders ?? {};
  const headers: Record<string, string> = {};
  const apiKey = String(options.apiKey ?? '').trim();
  const authIndex = String(options.authIndex ?? '').trim();
  const firstParty = isAnthropicFirstPartyUrl(options.url);
  const oauthToken = isClaudeOAuthToken(apiKey);
  const hasCustomAuth = Object.entries(customHeaders).some(([name, value]) => {
    const normalizedName = name.trim().toLowerCase();
    return (
      (normalizedName === 'authorization' || normalizedName === 'x-api-key') &&
      String(value ?? '').trim() !== ''
    );
  });

  if (apiKey) {
    if (firstParty && !oauthToken) {
      setHeader(headers, 'x-api-key', apiKey);
    } else {
      setHeader(headers, 'Authorization', `Bearer ${apiKey}`);
    }
  } else if (authIndex && !hasCustomAuth) {
    if (firstParty) {
      setHeader(headers, 'x-api-key', '$TOKEN$');
    } else {
      setHeader(headers, 'Authorization', 'Bearer $TOKEN$');
    }
  }

  setHeader(headers, 'anthropic-version', DEFAULT_ANTHROPIC_VERSION);
  if (oauthToken) {
    setHeader(headers, 'anthropic-beta', CLAUDE_OAUTH_BETA);
  }
  if (options.contentType) {
    setHeader(headers, 'Content-Type', options.contentType);
  }
  applyCustomHeaders(headers, customHeaders);

  // CPA treats provider custom headers as an escape hatch for third-party gateways,
  // but protects Anthropic-Beta on api.anthropic.com after custom headers are applied.
  if (firstParty) {
    if (oauthToken) {
      setHeader(headers, 'anthropic-beta', CLAUDE_OAUTH_BETA);
    } else {
      deleteHeader(headers, 'anthropic-beta');
    }
  }

  return {
    headers,
    baseType: firstParty ? 'anthropic' : 'custom',
    effectiveXApiKey: hasClaudeHeader(headers, 'x-api-key'),
    effectiveAuthorization: hasClaudeHeader(headers, 'authorization'),
  };
};

export const formatClaudeAuthDiagnostic = (
  resolution: ClaudeRequestHeaderResolution,
  apiKey?: string,
  authIndex?: string
): string =>
  `[diag: apiKeyField=${String(apiKey ?? '').trim() ? 'yes' : 'no'}, authIndex=${
    String(authIndex ?? '').trim() ? 'yes' : 'no'
  }, baseType=${resolution.baseType}, effectiveXApiKey=${
    resolution.effectiveXApiKey ? 'yes' : 'no'
  }, effectiveAuthorization=${resolution.effectiveAuthorization ? 'yes' : 'no'}]`;
