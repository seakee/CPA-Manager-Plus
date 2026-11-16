import type {
  PanelFeatureUnavailableReason,
  PanelHostMode,
} from '@/hooks/usePanelFeatureAvailability';
import type { UsageServiceStatus } from '@/services/api/usageService';
import type { ConnectionStatus } from '@/types';
import { compareVersions, type VersionComparison } from '@/utils/version';
import type { UpdateStatus } from '@/features/system/managerUpdateApi';

export type CollectorHealthState =
  | 'confirmed_healthy'
  | 'attention_required'
  | 'loading_unconfirmed'
  | 'disabled'
  | 'not_applicable';

export type CollectorUnconfirmedReason =
  | 'capability_checking'
  | 'status_missing'
  | 'collector_incomplete'
  | 'collector_starting'
  | 'collector_phase_unknown';

export type CollectorAttentionReason =
  | 'service_unavailable'
  | 'status_fetch_error'
  | 'collector_last_error'
  | 'collector_phase_error'
  | 'collector_stopped'
  | 'dead_letters_present'
  | 'wal_checkpoint_error';

export type DashboardUnavailableVariant =
  | 'panel_mode_not_applicable'
  | 'monitoring_disabled'
  | 'usage_not_configured';

export interface CollectorHealthEvaluation {
  state: CollectorHealthState;
  reasons: CollectorAttentionReason[];
  unconfirmedReason?: CollectorUnconfirmedReason;
  unavailableVariant?: DashboardUnavailableVariant;
  deadLetters: number;
  lastError: string;
}

export interface EvaluateCollectorHealthInput {
  isPanelLite?: boolean;
  panelHostMode: PanelHostMode;
  managerServiceAvailable: boolean;
  featureChecking: boolean;
  featureReason: PanelFeatureUnavailableReason | '';
  usageEnabled: boolean;
  currentScope: string;
  evidenceScope: string;
  status: UsageServiceStatus | null;
  loading: boolean;
  error?: string;
}

const toPositiveFiniteCount = (value: unknown): number =>
  typeof value === 'number' && Number.isFinite(value) && value > 0 ? value : 0;

export function resolveCollectorDeadLetters(status: UsageServiceStatus | null): number {
  if (!status) return 0;
  return Math.max(
    toPositiveFiniteCount(status.deadLetters),
    toPositiveFiniteCount(status.collector?.deadLetters)
  );
}

export function resolveDashboardUnavailableVariant(input: {
  isPanelLite: boolean;
  panelHostMode: PanelHostMode;
  managerServiceAvailable: boolean;
  featureReason: PanelFeatureUnavailableReason | '';
}): DashboardUnavailableVariant {
  if (
    input.isPanelLite ||
    (input.panelHostMode === 'external_panel' &&
      !input.managerServiceAvailable &&
      input.featureReason === 'service_not_configured')
  ) {
    return 'panel_mode_not_applicable';
  }
  if (input.featureReason === 'monitoring_disabled') {
    return 'monitoring_disabled';
  }
  return 'usage_not_configured';
}

export function evaluateCollectorHealth(
  input: EvaluateCollectorHealthInput
): CollectorHealthEvaluation {
  const isPanelLite = input.isPanelLite ?? __PANEL_LITE__;
  const scopeValid = input.currentScope !== '' && input.currentScope === input.evidenceScope;
  const effectiveStatus = scopeValid ? input.status : null;
  const fetchError = scopeValid ? (input.error?.trim() ?? '') : '';

  if (isPanelLite) {
    return {
      state: 'not_applicable',
      reasons: [],
      unavailableVariant: 'panel_mode_not_applicable',
      deadLetters: 0,
      lastError: '',
    };
  }

  const collector = effectiveStatus?.collector;
  const collectorLastError = collector?.lastError?.trim() ?? '';
  const checkpointError = effectiveStatus?.database?.checkpoint?.error?.trim() ?? '';
  const deadLetters = resolveCollectorDeadLetters(effectiveStatus);
  const collectorPhase = collector?.collector?.trim().toLowerCase() ?? '';

  if (scopeValid && (input.usageEnabled || input.featureChecking)) {
    const reasons: CollectorAttentionReason[] = [];
    if (fetchError) reasons.push('status_fetch_error');
    if (collectorLastError) reasons.push('collector_last_error');
    if (collectorPhase === 'error') reasons.push('collector_phase_error');
    if (collectorPhase === 'stopped') reasons.push('collector_stopped');
    if (deadLetters > 0) reasons.push('dead_letters_present');
    if (checkpointError) reasons.push('wal_checkpoint_error');

    if (reasons.length > 0) {
      return {
        state: 'attention_required',
        reasons,
        deadLetters,
        lastError: collectorLastError || fetchError || checkpointError,
      };
    }
  }

  if (input.featureChecking) {
    return {
      state: 'loading_unconfirmed',
      reasons: [],
      unconfirmedReason: 'capability_checking',
      deadLetters: 0,
      lastError: '',
    };
  }

  if (!input.usageEnabled) {
    if (input.featureReason === 'service_unavailable') {
      return {
        state: 'attention_required',
        reasons: ['service_unavailable'],
        deadLetters: 0,
        lastError: fetchError,
      };
    }
    const unavailableVariant = resolveDashboardUnavailableVariant({
      isPanelLite,
      panelHostMode: input.panelHostMode,
      managerServiceAvailable: input.managerServiceAvailable,
      featureReason: input.featureReason,
    });
    return {
      state: unavailableVariant === 'monitoring_disabled' ? 'disabled' : 'not_applicable',
      reasons: [],
      unavailableVariant,
      deadLetters: 0,
      lastError: '',
    };
  }

  if (effectiveStatus === null) {
    return {
      state: 'loading_unconfirmed',
      reasons: [],
      unconfirmedReason: 'status_missing',
      deadLetters: 0,
      lastError: '',
    };
  }
  if (!collector || collectorPhase === '') {
    return {
      state: 'loading_unconfirmed',
      reasons: [],
      unconfirmedReason: 'collector_incomplete',
      deadLetters: 0,
      lastError: '',
    };
  }
  if (collectorPhase === 'starting') {
    return {
      state: 'loading_unconfirmed',
      reasons: [],
      unconfirmedReason: 'collector_starting',
      deadLetters: 0,
      lastError: '',
    };
  }
  if (collectorPhase === 'running') {
    return {
      state: 'confirmed_healthy',
      reasons: [],
      deadLetters: 0,
      lastError: '',
    };
  }

  return {
    state: 'loading_unconfirmed',
    reasons: [],
    unconfirmedReason: 'collector_phase_unknown',
    deadLetters: 0,
    lastError: '',
  };
}

export function hasCollectorAttention(input: EvaluateCollectorHealthInput): boolean {
  const scopeValid = input.currentScope !== '' && input.currentScope === input.evidenceScope;
  const evaluation = evaluateCollectorHealth(input);
  if (evaluation.state !== 'attention_required') {
    return false;
  }
  if (evaluation.reasons.includes('service_unavailable')) {
    return true;
  }
  return scopeValid && (input.usageEnabled || input.featureChecking);
}

export type DashboardHealthSummaryStatus =
  | 'confirmed_healthy'
  | 'attention_required'
  | 'loading_unconfirmed'
  | 'unavailable_not_applicable';

export interface EvaluateDashboardHealthSummaryInput extends EvaluateCollectorHealthInput {
  connectionStatus: ConnectionStatus;
  usageEvidenceScope?: string;
  usageLoaded: boolean;
  usageLoading: boolean;
  usageError?: string;
  errorLogsLoaded: boolean;
  errorLogsLoading: boolean;
  errorLogCount: number;
  errorLogsError?: string;
  healthMetaError?: string;
}

export interface DashboardHealthSummaryEvaluation {
  status: DashboardHealthSummaryStatus;
  unavailableVariant?: DashboardUnavailableVariant;
  collector: CollectorHealthEvaluation;
  hasConnectionAttention: boolean;
  hasUsageAttention: boolean;
  hasErrorLogAttention: boolean;
  hasHealthMetaAttention: boolean;
}

export function evaluateDashboardHealthSummary(
  input: EvaluateDashboardHealthSummaryInput
): DashboardHealthSummaryEvaluation {
  const healthScopeValid =
    input.currentScope !== '' && input.currentScope === input.evidenceScope;
  const usageScopeKey = input.usageEvidenceScope ?? input.evidenceScope;
  const usageScopeValid =
    input.currentScope !== '' && input.currentScope === usageScopeKey;
  const collector = evaluateCollectorHealth(input);
  const hasConnectionAttention =
    input.connectionStatus === 'disconnected' || input.connectionStatus === 'error';
  const hasUsageAttention =
    usageScopeValid &&
    (input.usageEnabled || input.featureChecking) &&
    Boolean(input.usageError?.trim());
  const hasErrorLogAttention =
    healthScopeValid &&
    (input.usageEnabled || input.featureChecking) &&
    (Boolean(input.errorLogsError?.trim()) ||
      (input.errorLogsLoaded && input.errorLogCount > 0));
  const hasHealthMetaAttention =
    healthScopeValid && Boolean(input.healthMetaError?.trim());

  if (
    hasConnectionAttention ||
    collector.state === 'attention_required' ||
    hasUsageAttention ||
    hasErrorLogAttention ||
    hasHealthMetaAttention
  ) {
    return {
      status: 'attention_required',
      collector,
      hasConnectionAttention,
      hasUsageAttention,
      hasErrorLogAttention,
      hasHealthMetaAttention,
    };
  }

  const effectiveUsageLoaded = usageScopeValid && input.usageLoaded;
  const effectiveErrorLogsLoaded = healthScopeValid && input.errorLogsLoaded;

  if (
    input.connectionStatus === 'connecting' ||
    input.featureChecking ||
    (input.usageEnabled &&
      (!effectiveUsageLoaded ||
        collector.state === 'loading_unconfirmed' ||
        !effectiveErrorLogsLoaded))
  ) {
    return {
      status: 'loading_unconfirmed',
      collector,
      hasConnectionAttention: false,
      hasUsageAttention: false,
      hasErrorLogAttention: false,
      hasHealthMetaAttention: false,
    };
  }

  if (!input.usageEnabled) {
    return {
      status: 'unavailable_not_applicable',
      unavailableVariant:
        collector.unavailableVariant ??
        resolveDashboardUnavailableVariant({
          isPanelLite: input.isPanelLite ?? __PANEL_LITE__,
          panelHostMode: input.panelHostMode,
          managerServiceAvailable: input.managerServiceAvailable,
          featureReason: input.featureReason,
        }),
      collector,
      hasConnectionAttention: false,
      hasUsageAttention: false,
      hasErrorLogAttention: false,
      hasHealthMetaAttention: false,
    };
  }

  return {
    status: 'confirmed_healthy',
    collector,
    hasConnectionAttention: false,
    hasUsageAttention: false,
    hasErrorLogAttention: false,
    hasHealthMetaAttention: false,
  };
}

export interface EvaluateVersionUpdatesInput {
  appVersion: string;
  apiVersion: string;
  latestApiVersion: string;
  managerServiceAvailable: boolean;
  managerUpdatesAvailable: boolean;
  managerUpdatesError: boolean;
  managerUpdateStatus: UpdateStatus | null | undefined;
  externalManagerUpdateFallback: boolean;
  externalStableVersion: string | null | undefined;
  externalStableError: boolean;
}

export interface DashboardVersionUpdateEvaluation {
  managerHostedUpdateAvailable: boolean;
  externalManagerUpdateComparison: VersionComparison;
  externalManagerUpdateAvailable: boolean;
  coreApiUpdateComparison: VersionComparison;
  coreApiUpdateAvailable: boolean;
  hasAnyUpdate: boolean;
}

export function evaluateDashboardVersionUpdates(
  input: EvaluateVersionUpdatesInput
): DashboardVersionUpdateEvaluation {
  const managerHostedUpdateAvailable = Boolean(
    input.managerServiceAvailable &&
      input.managerUpdatesAvailable &&
      !input.managerUpdatesError &&
      !input.managerUpdateStatus?.last_error &&
      !input.managerUpdateStatus?.stale &&
      input.managerUpdateStatus?.state === 'update_available' &&
      input.managerUpdateStatus.target
  );

  const externalManagerUpdateComparison: VersionComparison =
    input.externalManagerUpdateFallback &&
    input.externalStableVersion &&
    !input.externalStableError
      ? compareVersions(input.externalStableVersion, input.appVersion)
      : null;
  const externalManagerUpdateAvailable =
    externalManagerUpdateComparison !== null && externalManagerUpdateComparison > 0;

  const coreApiUpdateComparison: VersionComparison = compareVersions(
    input.latestApiVersion,
    input.apiVersion
  );
  const coreApiUpdateAvailable =
    coreApiUpdateComparison !== null && coreApiUpdateComparison > 0;

  return {
    managerHostedUpdateAvailable,
    externalManagerUpdateComparison,
    externalManagerUpdateAvailable,
    coreApiUpdateComparison,
    coreApiUpdateAvailable,
    hasAnyUpdate:
      managerHostedUpdateAvailable ||
      externalManagerUpdateAvailable ||
      coreApiUpdateAvailable,
  };
}
