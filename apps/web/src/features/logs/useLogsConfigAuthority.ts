import { useCallback, useEffect, useRef, useState } from 'react';
import { readRoutingContext } from '@/fullweb/routingContext';
import {
  createPublicApiClient,
  PublicApiError,
  type PublicApiClient,
} from '@/fullweb/publicApi';
import { configurationText } from '@/i18n/configurationWorkspace';

export type LogsAuthorityMode = 'managed' | 'external' | 'private' | 'none' | 'standalone';

interface GatewayCapabilityPayload {
  capability?: {
    mode?: string;
    readable?: boolean;
    writable?: boolean;
    rulesWritable?: boolean;
    reason?: string;
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

const defaultClient = createPublicApiClient();

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

function normalizeMode(raw: string | undefined): LogsAuthorityMode {
  switch (raw) {
    case 'managed':
    case 'external':
    case 'private':
      return raw;
    default:
      return 'none';
  }
}

interface ResolvedAuthorityState {
  scopeKey: string;
  mode: LogsAuthorityMode;
  canEdit: boolean;
  reason?: string;
}

async function probeAuthority(
  targetClient: PublicApiClient,
  targetKey: string,
  currentScope: string
): Promise<ResolvedAuthorityState> {
  try {
    const view = await targetClient.get<GatewayCapabilityPayload>('gateway/config', {
      kind: 'admin',
      value: targetKey,
    });
    const mode = normalizeMode(view?.capability?.mode);
    if (mode === 'private') {
      return {
        scopeKey: currentScope,
        mode: 'private',
        canEdit: false,
        reason: view?.capability?.reason || 'private_profile_scope_frozen',
      };
    }
    const permitted =
      Boolean(view?.capability?.writable) && (mode === 'managed' || mode === 'external');
    return {
      scopeKey: currentScope,
      mode,
      canEdit: permitted,
      reason: permitted ? undefined : view?.capability?.reason || 'config_authority_unavailable',
    };
  } catch (err: unknown) {
    if (err instanceof PublicApiError && err.kind === 'authentication') {
      if (typeof window !== 'undefined') {
        window.dispatchEvent(new Event('unauthorized'));
      }
    }
    const code = extractWriteRefusalCode(err) || 'config_authority_unavailable';
    return {
      scopeKey: currentScope,
      mode: 'none',
      canEdit: false,
      reason: code,
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
  const inFullWeb =
    Boolean(authorityClient) ||
    Boolean(typeof document !== 'undefined' && readRoutingContext(document));
  const client = authorityClient ?? (inFullWeb ? defaultClient : null);
  const scopeKey = client ? `${apiBase}::${managementKey}` : '';

  const [resolved, setResolved] = useState<ResolvedAuthorityState>({
    scopeKey: '',
    mode: 'standalone',
    canEdit: true,
  });
  const requestIdRef = useRef(0);

  useEffect(() => {
    if (!client || !managementKey) {
      return () => {
        requestIdRef.current += 1;
      };
    }
    let cancelled = false;
    const requestId = ++requestIdRef.current;
    probeAuthority(client, managementKey, scopeKey).then((next) => {
      if (cancelled || requestIdRef.current !== requestId) return;
      setResolved(next);
    });
    return () => {
      cancelled = true;
      requestIdRef.current += 1;
    };
  }, [client, managementKey, scopeKey]);

  const refresh = useCallback(async () => {
    if (!client || !managementKey) return;
    const requestId = ++requestIdRef.current;
    const next = await probeAuthority(client, managementKey, scopeKey);
    if (requestIdRef.current !== requestId) return;
    setResolved(next);
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
