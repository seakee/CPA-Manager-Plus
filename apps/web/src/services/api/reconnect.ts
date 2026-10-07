import axios from 'axios';
import { normalizeUsageServiceBase } from '@/services/api/usageService';

// Self-service reconnect API (Manager Server only). Admin calls need the
// panel/admin key; link calls are public and authorised by the link token.

export const RECONNECT_PROVIDERS = ['claude', 'codex', 'antigravity', 'xai', 'meta'] as const;
export type ReconnectProvider = (typeof RECONNECT_PROVIDERS)[number];

export interface ReconnectSettings {
  enabled: boolean;
  publicUrl: string;
  webhookUrl?: string;
  webhookConfigured?: boolean;
  senderName: string;
  checkIntervalMinutes: number;
  linkTtlHours: number;
  followupHours: number;
  followupStartHour: number;
  followupEndHour: number;
  followupTimeZone: string;
}

export type ReconnectStatus = 'pending' | 'completed' | 'resolved' | 'expired';

export interface ReconnectOutage {
  id: number;
  provider: ReconnectProvider;
  email: string;
  status: ReconnectStatus;
  purpose: 'reconnect' | 'test' | 'invite';
  manual: boolean;
  reason?: string;
  firstNotifiedAtMs: number;
  lastMessageAtMs: number;
  reminders: number;
  expiresAtMs: number;
  closedAtMs?: number;
}

export interface ReconnectSummary {
  provider: ReconnectProvider;
  logins: number;
  needingReconnect: number;
}

export interface ReconnectSendResult {
  sent: boolean;
  purpose?: 'reconnect' | 'test' | 'invite';
  notice?: string;
  expiresAtMs?: number;
}

export interface ReconnectLink {
  provider: ReconnectProvider;
  email: string;
  status: ReconnectStatus | 'unused';
  expiresAtMs: number;
}

export interface ReconnectAttempt {
  authUrl: string;
  deadlineMs: number;
  device: boolean;
  userCode?: string;
}

const TIMEOUT_MS = 30_000;
// Submitting waits for CPA to confirm the token exchange.
const SUBMIT_TIMEOUT_MS = 100_000;

const url = (base: string, path: string) =>
  `${normalizeUsageServiceBase(base).replace(/\/+$/, '')}/usage-service/reconnect/${path}`;

const auth = (key?: string) => (key ? { Authorization: `Bearer ${key}` } : undefined);

/** Server error message ({error}) or the transport error. */
export const reconnectErrorMessage = (error: unknown): string => {
  if (axios.isAxiosError(error)) {
    const data = error.response?.data as { error?: unknown } | undefined;
    if (data && typeof data.error === 'string' && data.error) return data.error;
    return error.message;
  }
  return error instanceof Error ? error.message : String(error);
};

export const reconnectApi = {
  getSettings: async (base: string, key?: string) =>
    (
      await axios.get<ReconnectSettings>(url(base, 'settings'), {
        timeout: TIMEOUT_MS,
        headers: auth(key),
      })
    ).data,
  updateSettings: async (base: string, key: string | undefined, settings: ReconnectSettings) =>
    (
      await axios.put<ReconnectSettings>(url(base, 'settings'), settings, {
        timeout: TIMEOUT_MS,
        headers: auth(key),
      })
    ).data,
  listRequests: async (base: string, key?: string) =>
    (
      await axios.get<{ items: ReconnectOutage[] }>(url(base, 'requests'), {
        timeout: TIMEOUT_MS,
        headers: auth(key),
      })
    ).data.items ?? [],
  getSummary: async (base: string, key?: string) =>
    (
      await axios.get<{ providers: ReconnectSummary[] }>(url(base, 'summary'), {
        timeout: TIMEOUT_MS,
        headers: auth(key),
      })
    ).data.providers ?? [],
  send: async (base: string, key: string | undefined, provider: ReconnectProvider, email: string) =>
    (
      await axios.post<ReconnectSendResult>(
        url(base, 'send'),
        { provider, email },
        { timeout: TIMEOUT_MS, headers: auth(key) }
      )
    ).data,

  describeLink: async (base: string, token: string) =>
    (
      await axios.get<ReconnectLink>(url(base, `link/${encodeURIComponent(token)}`), {
        timeout: TIMEOUT_MS,
      })
    ).data,
  startLink: async (base: string, token: string) =>
    (
      await axios.post<ReconnectAttempt>(
        url(base, `link/${encodeURIComponent(token)}/start`),
        {},
        {
          timeout: TIMEOUT_MS,
        }
      )
    ).data,
  submitLink: async (base: string, token: string, callbackUrl: string) =>
    (
      await axios.post<{ status: string }>(
        url(base, `link/${encodeURIComponent(token)}/submit`),
        { callbackUrl },
        { timeout: SUBMIT_TIMEOUT_MS }
      )
    ).data,
  pollLink: async (base: string, token: string) =>
    (
      await axios.get<{ status: 'wait' | 'ok' }>(
        url(base, `link/${encodeURIComponent(token)}/poll`),
        {
          timeout: TIMEOUT_MS,
        }
      )
    ).data,
};
