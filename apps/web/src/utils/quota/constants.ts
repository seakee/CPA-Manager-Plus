/**
 * Quota constants for API URLs, headers, and theme colors.
 */

import type { AntigravityQuotaGroupDefinition } from '@/types';

// Antigravity API configuration
export const ANTIGRAVITY_QUOTA_SUMMARY_URLS = [
  'https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary',
  'https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:retrieveUserQuotaSummary',
  'https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary',
];

export const ANTIGRAVITY_AVAILABLE_MODELS_URLS = [
  'https://daily-cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels',
  'https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:fetchAvailableModels',
  'https://cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels',
];

export const ANTIGRAVITY_QUOTA_URLS = ANTIGRAVITY_QUOTA_SUMMARY_URLS;

export const ANTIGRAVITY_CODE_ASSIST_URLS = [
  'https://daily-cloudcode-pa.googleapis.com/v1internal:loadCodeAssist',
  'https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist',
];

export const ANTIGRAVITY_CODE_ASSIST_URL = ANTIGRAVITY_CODE_ASSIST_URLS[0];

// Keep aligned with the CPA Antigravity Hub UA (internal/misc/antigravity_version.go).
// Cloud Code rejects newer models for clients below 2.23.0.
export const ANTIGRAVITY_HUB_VERSION = '2.23.0';
export const ANTIGRAVITY_HUB_PLATFORM = 'darwin/arm64';

type AntigravityUserAgentOptions = {
  version?: string;
  platform?: string;
};

export const buildAntigravityUserAgent = ({
  version = ANTIGRAVITY_HUB_VERSION,
  platform = ANTIGRAVITY_HUB_PLATFORM,
}: AntigravityUserAgentOptions = {}) => `antigravity/hub/${version} ${platform}`;

export const ANTIGRAVITY_USER_AGENT = buildAntigravityUserAgent();

export const ANTIGRAVITY_REQUEST_HEADERS = {
  Authorization: 'Bearer $TOKEN$',
  'Content-Type': 'application/json',
  'User-Agent': ANTIGRAVITY_USER_AGENT,
};

export const ANTIGRAVITY_QUOTA_GROUPS: AntigravityQuotaGroupDefinition[] = [
  {
    id: 'claude-gpt',
    label: 'Claude',
    identifiers: ['claude-sonnet-4-6', 'claude-opus-4-6-thinking', 'gpt-oss-120b-medium'],
  },
  {
    id: 'gemini-3-pro',
    label: 'Gemini 3 Pro',
    identifiers: ['gemini-3-pro-high', 'gemini-3-pro-low'],
  },
  {
    id: 'gemini-3-1-pro-series',
    label: 'Gemini 3.1 Pro Series',
    identifiers: ['gemini-3.1-pro-high', 'gemini-3.1-pro-low'],
  },
  {
    id: 'gemini-2-5-flash',
    label: 'Gemini 2.5 Flash',
    identifiers: ['gemini-2.5-flash', 'gemini-2.5-flash-thinking'],
  },
  {
    id: 'gemini-2-5-flash-lite',
    label: 'Gemini 2.5 Flash Lite',
    identifiers: ['gemini-2.5-flash-lite'],
  },
  {
    id: 'gemini-2-5-cu',
    label: 'Gemini 2.5 CU',
    identifiers: ['rev19-uic3-1p'],
  },
  {
    id: 'gemini-3-flash',
    label: 'Gemini 3 Flash',
    identifiers: ['gemini-3-flash'],
  },
  {
    id: 'gemini-image',
    label: 'gemini-3.1-flash-image',
    identifiers: ['gemini-3.1-flash-image'],
    labelFromModel: true,
  },
];

// Claude API configuration
export const CLAUDE_PROFILE_URL = 'https://api.anthropic.com/api/oauth/profile';

export const CLAUDE_USAGE_URL = 'https://api.anthropic.com/api/oauth/usage';

export const CLAUDE_REQUEST_HEADERS = {
  Authorization: 'Bearer $TOKEN$',
  'Content-Type': 'application/json',
  'anthropic-beta': 'oauth-2025-04-20',
};

export const CLAUDE_USAGE_WINDOW_KEYS = [
  { key: 'five_hour', id: 'five-hour', labelKey: 'claude_quota.five_hour' },
  { key: 'seven_day', id: 'seven-day', labelKey: 'claude_quota.seven_day' },
  {
    key: 'seven_day_oauth_apps',
    id: 'seven-day-oauth-apps',
    labelKey: 'claude_quota.seven_day_oauth_apps',
  },
  { key: 'seven_day_opus', id: 'seven-day-opus', labelKey: 'claude_quota.seven_day_opus' },
  { key: 'seven_day_sonnet', id: 'seven-day-sonnet', labelKey: 'claude_quota.seven_day_sonnet' },
  { key: 'seven_day_cowork', id: 'seven-day-cowork', labelKey: 'claude_quota.seven_day_cowork' },
  { key: 'iguana_necktie', id: 'iguana-necktie', labelKey: 'claude_quota.iguana_necktie' },
] as const;

// Codex API configuration
export const CODEX_USAGE_URL = 'https://chatgpt.com/backend-api/wham/usage';

export const CODEX_RATE_LIMIT_RESET_CREDITS_URL =
  'https://chatgpt.com/backend-api/wham/rate-limit-reset-credits';

export const CODEX_RATE_LIMIT_RESET_CREDITS_CONSUME_URL =
  'https://chatgpt.com/backend-api/wham/rate-limit-reset-credits/consume';

export const CODEX_REQUEST_HEADERS = {
  Authorization: 'Bearer $TOKEN$',
  'Content-Type': 'application/json',
  'User-Agent': 'codex-tui/0.149.1 (Mac OS 26.5.2; arm64) iTerm.app/3.6.11 (codex-tui; 0.149.1)',
};

// Kimi API configuration
export const KIMI_USAGE_URL = 'https://api.kimi.com/coding/v1/usages';

export const KIMI_REQUEST_HEADERS = {
  Authorization: 'Bearer $TOKEN$',
};

// xAI/Grok API configuration
export const XAI_BILLING_WEEKLY_URL = 'https://cli-chat-proxy.grok.com/v1/billing?format=credits';
export const XAI_BILLING_MONTHLY_URL = 'https://cli-chat-proxy.grok.com/v1/billing';
export const XAI_OFFICIAL_API_ME_URL = 'https://api.x.ai/v1/me';
export const XAI_OFFICIAL_API_BASE_URL = 'https://api.x.ai/v1';
export const XAI_CLI_CHAT_PROXY_BASE_URL = 'https://cli-chat-proxy.grok.com/v1';
export const DEFAULT_XAI_INSPECTION_MODEL = 'grok-4.5';
export const DEFAULT_XAI_INSPECTION_PROMPT = 'Reply with exactly OK.';
// xAI chat-proxy rejects Grok CLI versions older than 1.0.13 with HTTP 426.
// Keep these identity headers on one version source so future bumps stay synchronized.
export const XAI_GROK_CLIENT_VERSION = '1.0.44';
export const XAI_GROK_USER_AGENT =
  `grok-pager/${XAI_GROK_CLIENT_VERSION} grok-shell/${XAI_GROK_CLIENT_VERSION} (macos; aarch64)`;
export const XAI_INFERENCE_USER_AGENT = `xai-grok-workspace/${XAI_GROK_CLIENT_VERSION}`;

export const XAI_REQUEST_HEADERS = {
  Authorization: 'Bearer $TOKEN$',
  'x-xai-token-auth': 'xai-grok-cli',
  'x-grok-client-version': XAI_GROK_CLIENT_VERSION,
  accept: '*/*',
  'user-agent': XAI_GROK_USER_AGENT,
};
