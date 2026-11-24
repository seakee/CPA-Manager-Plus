import type { OAuthFlowKind } from './providers';

export interface ProviderFlowState {
  url?: string;
  userCode?: string;
  flow?: string;
  expiresAtMs?: number;
  state?: string;
  status?: 'idle' | 'waiting' | 'success' | 'error';
  error?: string;
  polling?: boolean;
  pollingError?: string;
  cancelling?: boolean;
  cancelError?: string;
  callbackUrl?: string;
  callbackSubmitting?: boolean;
  callbackStatus?: 'success' | 'error';
  callbackError?: string;
  callbackValidation?: string;
  reauthCompleted?: boolean;
}

export type FlowView = 'idle' | 'flow' | 'success' | 'failed';

/**
 * Derive modal view state:
 * - Active URL / device code / waiting -> stepped flow view (even if a transient
 *   polling error occurs, keep the user in flow view so they can retry polling or
 *   submit a manual callback URL without losing the live session state).
 * - Error with no active link/code -> failed view with one-click retry.
 */
export const resolveFlowView = (state: ProviderFlowState): FlowView => {
  if (state.status === 'success') return 'success';
  if (state.url || state.userCode || state.status === 'waiting') return 'flow';
  if (state.status === 'error') return 'failed';
  return 'idle';
};

/**
 * Determine whether the current session is a device-code flow.
 * Prioritizes backend response metadata (`flow` / `userCode`) so providers whose
 * flow evolved between CPA v7 and v8 (such as xAI) adapt automatically.
 */
export const isDeviceFlowSession = (
  state: Pick<ProviderFlowState, 'flow' | 'userCode'>,
  defaultFlow: OAuthFlowKind
): boolean => {
  const reportedFlow = state.flow?.trim().toLowerCase();
  if (reportedFlow === 'device' || Boolean(state.userCode?.trim())) {
    return true;
  }
  if (reportedFlow === 'browser' || reportedFlow === 'oauth' || reportedFlow === 'callback') {
    return false;
  }
  return defaultFlow === 'device';
};

/** Split an authorization URL into highlighted host prefix + remaining path/query. */
export const splitAuthUrl = (url: string): { host: string; rest: string } => {
  try {
    const { host } = new URL(url);
    if (!host) return { host: '', rest: url };
    const hostEnd = url.indexOf(host) + host.length;
    return { host, rest: url.slice(hostEnd) };
  } catch {
    return { host: '', rest: url };
  }
};
