import { useCallback, useEffect, useRef, useState } from 'react';
import { PublicApiError, type PublicApiClient } from '@/fullweb/publicApi';
import { configurationText } from '@/i18n/configurationWorkspace';
import { useLogsAuthorityClient } from './LogsAuthorityContext';

export type LogsAuthorityMode = 'managed' | 'external' | 'private' | 'none' | 'standalone';

interface GatewayCapabilityPayload {
  capability?: {
    mode?: unknown;
    readable?: unknown;
    writable?: unknown;
    rulesWritable?: unknown;
    reason?: unknown;
  };
}

export interface LogsConfigAuthority {
  mode: LogsAuthorityMode;
  loading: boolean;
  canEditLogs: boolean;
  reason?: string;
  reasonText?: string;
  refresh: () => Promise<void>;
}

type WorkspaceTextKey = Parameters<typeof configurationText>[0];

export function mapAuthorityReasonKey(reason: string | undefined): WorkspaceTextKey {
  switch (reason) {
    case 'external_rule_write_unproven':
      return 'reasonExternalRules';
    case 'external_management_key_rejected':
    case 'external_management_forbidden':
      return 'reasonKeyRejected';
    case 'external_target_moved':
    case 'external_cpa_target_moved':
      return 'reasonTargetMoved';
    case 'external_cpa_version_unsupported':
      return 'reasonVersionUnsupported';
    case 'private_profile_scope_frozen':
      return 'reasonPrivateFrozen';
    case 'config_journal_invalid':
      return 'reasonJournalInvalid';
    case 'original_operation_unknown':
      return 'reasonUnknownOriginal';
    case 'legacy_operation_unresolved':
      return 'reasonLegacyBlocked';
    case 'config_read_unavailable':
      return 'reasonReadUnavailable';
    case 'installation_incomplete':
      return 'reasonNotInstalled';
    default:
      return 'reasonAuthorityUnavailable';
  }
}

export function extractWriteRefusalCode(error: unknown): string | undefined {
  if (error instanceof PublicApiError && error.code) {
    return error.code;
  }
  if (error !== null && typeof error === 'object') {
    const candidates: unknown[] = [];
    if ('data' in error) candidates.push(error.data);
    if ('details' in error) candidates.push(error.details);
    for (const candidate of candidates) {
      if (candidate !== null && typeof candidate === 'object' && 'code' in candidate) {
        const code = candidate.code;
        if (typeof code === 'string' && code.trim() !== '') {
          return code.trim();
        }
      }
    }
  }
  return undefined;
}

function normalizeMode(raw: unknown): LogsAuthorityMode {
  switch (raw) {
    case 'managed':
    case 'external':
    case 'private':
      return raw;
    default:
      return 'none';
  }
}

function normalizeReason(raw: unknown): string | undefined {
  if (typeof raw !== 'string') return undefined;
  const trimmed = raw.trim();
  return trimmed !== '' ? trimmed : undefined;
}

interface ResolvedAuthorityState {
  scopeKey: string;
  mode: LogsAuthorityMode;
  canEdit: boolean;
  reason?: string;
}

interface ProbeOutcome {
  state: ResolvedAuthorityState;
  authenticationFailed: boolean;
}

function dispatchUnauthorizedIfPresent(): void {
  if (typeof window !== 'undefined') {
    window.dispatchEvent(new Event('unauthorized'));
  }
}

async function probeAuthority(
  targetClient: PublicApiClient,
  targetKey: string,
  currentScope: string
): Promise<ProbeOutcome> {
  try {
    const view = await targetClient.get<GatewayCapabilityPayload>('gateway/config', {
      kind: 'admin',
      value: targetKey,
    });
    const mode = normalizeMode(view?.capability?.mode);
    const reason = normalizeReason(view?.capability?.reason);
    if (mode === 'private') {
      return {
        state: {
          scopeKey: currentScope,
          mode: 'private',
          canEdit: false,
          reason: reason || 'private_profile_scope_frozen',
        },
        authenticationFailed: false,
      };
    }
    const permitted =
      view?.capability?.writable === true && (mode === 'managed' || mode === 'external');
    return {
      state: {
        scopeKey: currentScope,
        mode,
        canEdit: permitted,
        reason: permitted ? undefined : reason || 'config_authority_unavailable',
      },
      authenticationFailed: false,
    };
  } catch (err: unknown) {
    const authenticationFailed = err instanceof PublicApiError && err.kind === 'authentication';
    const code = extractWriteRefusalCode(err) || 'config_authority_unavailable';
    return {
      state: {
        scopeKey: currentScope,
        mode: 'none',
        canEdit: false,
        reason: code,
      },
      authenticationFailed,
    };
  }
}

export function useLogsConfigAuthority({
  connectionStatus,
  apiBase,
  managementKey,
  authorityClient,
}: {
  connectionStatus: string;
  apiBase: string;
  managementKey: string;
  authorityClient?: PublicApiClient;
}): LogsConfigAuthority {
  const contextClient = useLogsAuthorityClient();
  const client = authorityClient ?? contextClient;
  const scopeKey = client ? `${apiBase}::${managementKey}` : '';

  const [resolved, setResolved] = useState<ResolvedAuthorityState>({
    scopeKey: '',
    mode: 'standalone',
    canEdit: true,
  });
  const mountedRef = useRef(false);
  const activeScopeRef = useRef('');
  const requestIdRef = useRef(0);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      activeScopeRef.current = '';
      requestIdRef.current += 1;
    };
  }, []);

  useEffect(() => {
    activeScopeRef.current = scopeKey;
    if (!client || !managementKey) {
      return () => {
        requestIdRef.current += 1;
      };
    }
    let cancelled = false;
    const requestId = ++requestIdRef.current;
    probeAuthority(client, managementKey, scopeKey).then((outcome) => {
      if (
        cancelled ||
        !mountedRef.current ||
        requestIdRef.current !== requestId ||
        activeScopeRef.current !== scopeKey
      ) {
        return;
      }
      if (outcome.authenticationFailed) {
        dispatchUnauthorizedIfPresent();
      }
      setResolved(outcome.state);
    });
    return () => {
      cancelled = true;
      requestIdRef.current += 1;
    };
  }, [client, managementKey, scopeKey]);

  const refresh = useCallback(async () => {
    if (!client || !managementKey) return;
    const requestId = ++requestIdRef.current;
    const outcome = await probeAuthority(client, managementKey, scopeKey);
    if (
      !mountedRef.current ||
      requestIdRef.current !== requestId ||
      activeScopeRef.current !== scopeKey
    ) {
      return;
    }
    if (outcome.authenticationFailed) {
      dispatchUnauthorizedIfPresent();
    }
    setResolved(outcome.state);
  }, [client, managementKey, scopeKey]);

  if (!client) {
    return {
      mode: 'standalone',
      loading: false,
      canEditLogs: connectionStatus === 'connected',
      refresh,
    };
  }

  if (!managementKey) {
    return {
      mode: 'none',
      loading: false,
      canEditLogs: false,
      reason: 'config_authority_unavailable',
      reasonText: configurationText('reasonAuthorityUnavailable'),
      refresh,
    };
  }

  const loading = resolved.scopeKey !== scopeKey;
  if (loading) {
    return {
      mode: 'none',
      loading: true,
      canEditLogs: false,
      refresh,
    };
  }

  const canEditLogs = connectionStatus === 'connected' && resolved.canEdit;
  const reasonText = resolved.reason
    ? configurationText(mapAuthorityReasonKey(resolved.reason))
    : undefined;

  return {
    mode: resolved.mode,
    loading: false,
    canEditLogs,
    reason: resolved.reason,
    reasonText,
    refresh,
  };
}
