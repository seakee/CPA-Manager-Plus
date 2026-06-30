/**
 * Runtime holder for the quota-lookup User-Agent.
 *
 * The quota-lookup paths (quota page refresh, monitoring account summary, etc.)
 * build request headers in pure functions that can't access the React store, so
 * a module-level variable holds the currently effective quota-lookup UA. It is
 * injected via the setter when the manager config (codex.quotaUserAgent) is
 * loaded/saved; when unset or empty it falls back to the CLIProxyAPI-aligned
 * default constant.
 */

import { CODEX_QUOTA_USER_AGENT } from './constants';

let currentCodexQuotaUserAgent = CODEX_QUOTA_USER_AGENT;

export const getCodexQuotaUserAgent = (): string => currentCodexQuotaUserAgent;

export const setCodexQuotaUserAgent = (value?: string | null): void => {
  const trimmed = String(value ?? '').trim();
  currentCodexQuotaUserAgent = trimmed || CODEX_QUOTA_USER_AGENT;
};
